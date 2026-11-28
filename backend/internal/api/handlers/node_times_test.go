package handlers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/config"
	"xirang/backend/internal/database"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"
	nodepkg "xirang/backend/internal/node"
	"xirang/backend/internal/repository"
	gormrepo "xirang/backend/internal/repository/gorm"
	"xirang/backend/internal/secure"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// pauseNodeTimeUpdate pauses the first repository write after the service has read
// the node, leaving the real repository to perform both writes and validation.
type pauseNodeTimeUpdate struct {
	repository.NodeRepository
	claimed atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (r *pauseNodeTimeUpdate) Update(ctx context.Context, node *model.Node, options repository.NodeUpdateOptions) error {
	if r.claimed.CompareAndSwap(false, true) {
		close(r.entered)
		select {
		case <-r.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return r.NodeRepository.Update(ctx, node, options)
}

func TestNodeTimestampWritesSQLitePostgres(t *testing.T) {
	for _, engine := range []string{"sqlite", "postgres"} {
		t.Run(engine, func(t *testing.T) {
			t.Setenv("APP_ENV", "development")
			key := sha256.Sum256([]byte("FAKE_NODE_TIMESTAMP_ENCRYPTION_KEY_FOR_TEST_ONLY"))
			t.Setenv("DATA_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(key[:]))
			secure.ResetForTesting()
			t.Cleanup(secure.ResetForTesting)
			db := openMigratedRetryTransactionDB(t, engine)
			repo := gormrepo.NewNodeRepository(db)
			router := func(nodeRepo repository.NodeRepository) *gin.Engine {
				r := gin.New()
				h := NewNodeHandler(db, nil, nodepkg.NewNodeService(nodeRepo))
				r.POST("/nodes", h.Create)
				r.PUT("/nodes/:id", h.Update)
				return r
			}
			r := router(repo)
			request := func(r *gin.Engine, method, path string, body []byte) *httptest.ResponseRecorder {
				req := httptest.NewRequest(method, path, bytes.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				resp := httptest.NewRecorder()
				r.ServeHTTP(resp, req)
				return resp
			}
			payload := func(name string, fields map[string]any) []byte {
				t.Helper()
				m := map[string]any{"name": name, "host": "192.0.2.10", "username": "tester", "auth_type": "password", "password": "FAKE_NODE_PASSWORD_FOR_TEST_ONLY"}
				for k, v := range fields {
					m[k] = v
				}
				body, err := json.Marshal(m)
				if err != nil {
					t.Fatalf("marshal request: %v", err)
				}
				return body
			}
			var sequence int
			create := func(fields map[string]any) (uint, string) {
				t.Helper()
				sequence++
				name := fmt.Sprintf("times-node-%d", sequence)
				resp := request(r, http.MethodPost, "/nodes", payload(name, fields))
				if resp.Code != http.StatusCreated {
					t.Fatalf("create node: status=%d body=%s", resp.Code, resp.Body.String())
				}
				var envelope struct {
					Data struct {
						ID uint `json:"id"`
					} `json:"data"`
				}
				if err := json.Unmarshal(resp.Body.Bytes(), &envelope); err != nil || envelope.Data.ID == 0 {
					t.Fatalf("decode create response: %v body=%s", err, resp.Body.String())
				}
				return envelope.Data.ID, name
			}
			load := func(id uint) model.Node {
				t.Helper()
				var n model.Node
				if err := db.First(&n, id).Error; err != nil {
					t.Fatalf("load node %d: %v", id, err)
				}
				return n
			}
			put := func(id uint, name string, fields map[string]any) *httptest.ResponseRecorder {
				return request(r, http.MethodPut, fmt.Sprintf("/nodes/%d", id), payload(name, fields))
			}
			assertTime := func(field string, got *time.Time, want string) {
				t.Helper()
				if want == "" {
					if got != nil {
						t.Errorf("%s=%v, want nil", field, got)
					}
					return
				}
				expected, err := time.Parse(time.RFC3339Nano, want)
				if err != nil {
					t.Fatalf("invalid expected timestamp: %v", err)
				}
				if got == nil || !got.Equal(expected) || got.Nanosecond() != expected.Nanosecond() {
					t.Errorf("%s=%v, want %s", field, got, want)
				}
				if got != nil && engine == "sqlite" {
					_, offset := got.Zone()
					if offset != 0 {
						t.Errorf("%s has UTC offset %d, want UTC", field, offset)
					}
				}
			}
			assertTimes := func(n model.Node, start, end, expiry string) {
				t.Helper()
				assertTime("maintenance_start", n.MaintenanceStart, start)
				assertTime("maintenance_end", n.MaintenanceEnd, end)
				assertTime("expiry_date", n.ExpiryDate, expiry)
			}
			checkStatus := func(resp *httptest.ResponseRecorder, want int) {
				t.Helper()
				if resp.Code != want {
					t.Fatalf("status=%d want=%d body=%s", resp.Code, want, resp.Body.String())
				}
			}
			const start = "2025-01-02T01:00:00.123456Z"
			const end = "2025-01-02T04:00:00.123457Z"
			const expiry = "2001-01-01T00:00:00.654321Z"
			valid := map[string]any{"maintenance_start": "2025-01-02T03:00:00.123456789+02:00", "maintenance_end": "2025-01-02T06:00:00.123457999+02:00", "expiry_date": "2000-12-31T19:00:00.654321987-05:00"}

			t.Run("create-update-and-invalid", func(t *testing.T) {
				id, name := create(valid)
				assertTimes(load(id), start, end, expiry)
				for _, fields := range []map[string]any{
					{"maintenance_start": start},
					{"maintenance_start": "2025-01-02T02:00:00.000000100Z", "maintenance_end": "2025-01-02T02:00:00.000000999Z"},
				} {
					var before, after int64
					if err := db.Model(&model.Node{}).Count(&before).Error; err != nil {
						t.Fatal(err)
					}
					checkStatus(request(r, http.MethodPost, "/nodes", payload("invalid-pair", fields)), http.StatusBadRequest)
					if err := db.Model(&model.Node{}).Count(&after).Error; err != nil {
						t.Fatal(err)
					}
					if before != after {
						t.Error("invalid create persisted a node")
					}
				}
				for _, field := range []string{"maintenance_start", "maintenance_end", "expiry_date"} {
					for _, method := range []string{http.MethodPost, http.MethodPut} {
						bad := "invalid-FAKE_TIMESTAMP_FOR_TEST_ONLY"
						fields := map[string]any{field: bad, "tags": "must-not-persist"}
						before := load(id)
						var countBefore int64
						if err := db.Model(&model.Node{}).Count(&countBefore).Error; err != nil {
							t.Fatal(err)
						}
						path := fmt.Sprintf("/nodes/%d", id)
						if method == http.MethodPost {
							path = "/nodes"
						}
						resp := request(r, method, path, payload(name, fields))
						checkStatus(resp, http.StatusBadRequest)
						if !strings.Contains(resp.Body.String(), field) || strings.Contains(resp.Body.String(), bad) {
							t.Errorf("unsafe or non-specific error for %s: %s", field, resp.Body.String())
						}
						var countAfter int64
						if err := db.Model(&model.Node{}).Count(&countAfter).Error; err != nil {
							t.Fatal(err)
						}
						if countBefore != countAfter || !reflect.DeepEqual(before, load(id)) {
							t.Errorf("invalid %s %s modified persisted data", method, field)
						}
					}
				}
				bad := "9999-12-31T23:59:59-01:00"
				for _, method := range []string{http.MethodPost, http.MethodPut} {
					before := load(id)
					path := fmt.Sprintf("/nodes/%d", id)
					if method == http.MethodPost {
						path = "/nodes"
					}
					resp := request(r, method, path, payload(name, map[string]any{"expiry_date": bad, "tags": "must-not-persist"}))
					checkStatus(resp, http.StatusBadRequest)
					if !strings.Contains(resp.Body.String(), "expiry_date") || strings.Contains(resp.Body.String(), bad) {
						t.Errorf("unsafe overflow error: %s", resp.Body.String())
					}
					if !reflect.DeepEqual(before, load(id)) {
						t.Error("overflow modified persisted data")
					}
				}
				checkStatus(put(id, name, nil), http.StatusOK)
				assertTimes(load(id), start, end, expiry)
				checkStatus(put(id, name, map[string]any{"maintenance_start": nil, "maintenance_end": nil, "expiry_date": nil}), http.StatusOK)
				assertTimes(load(id), start, end, expiry)
				checkStatus(put(id, name, map[string]any{"maintenance_start": "2025-01-01T20:00:00.123456999-05:00", "maintenance_end": "2025-01-02T05:00:00.123457100+01:00", "expiry_date": "2000-12-31T19:00:00.654321999-05:00"}), http.StatusOK)
				assertTimes(load(id), start, end, expiry)
				checkStatus(put(id, name, map[string]any{"maintenance_start": "", "maintenance_end": "", "expiry_date": ""}), http.StatusOK)
				assertTimes(load(id), "", "", "")
				checkStatus(put(id, name, valid), http.StatusOK)
				assertTimes(load(id), start, end, expiry)
				id2, _ := create(map[string]any{"maintenance_start": nil, "maintenance_end": "", "expiry_date": nil})
				assertTimes(load(id2), "", "", "")
			})

			t.Run("preserve-existing-precision", func(t *testing.T) {
				id, name := create(nil)
				zone := time.FixedZone("legacy", 2*60*60)
				legacyStart := time.Date(2025, 1, 2, 3, 0, 0, 123456789, zone)
				legacyEnd := time.Date(2025, 1, 2, 6, 0, 0, 123457789, zone)
				legacyExpiry := time.Date(2001, 1, 1, 2, 0, 0, 654321789, zone)
				if err := db.Model(&model.Node{}).Where("id = ?", id).Updates(map[string]any{"maintenance_start": legacyStart, "maintenance_end": legacyEnd, "expiry_date": legacyExpiry}).Error; err != nil {
					t.Fatal(err)
				}
				before := load(id)
				repeat := func(value *time.Time) string { return value.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano) }
				checkStatus(put(id, name, map[string]any{"maintenance_start": repeat(before.MaintenanceStart), "maintenance_end": repeat(before.MaintenanceEnd), "expiry_date": repeat(before.ExpiryDate)}), http.StatusOK)
				after := load(id)
				if !reflect.DeepEqual(before.MaintenanceStart, after.MaintenanceStart) || !reflect.DeepEqual(before.MaintenanceEnd, after.MaintenanceEnd) || !reflect.DeepEqual(before.ExpiryDate, after.ExpiryDate) {
					t.Error("equivalent timestamps changed existing stored values")
				}
			})

			t.Run("ordering-and-microseconds", func(t *testing.T) {
				id, name := create(valid)
				for _, fields := range []map[string]any{
					{"maintenance_start": "2025-01-02T02:00:00.000000100Z", "maintenance_end": "2025-01-02T02:00:00.000000999Z"},
					{"maintenance_start": "2025-01-02T05:00:00Z"},
					{"maintenance_start": ""},
				} {
					before := load(id)
					checkStatus(put(id, name, fields), http.StatusBadRequest)
					if !reflect.DeepEqual(before, load(id)) {
						t.Error("invalid pair modified node")
					}
				}
				checkStatus(put(id, name, map[string]any{"maintenance_start": "2025-01-02T02:00:00.000000999Z", "maintenance_end": "2025-01-02T02:00:00.000001001Z"}), http.StatusOK)
				assertTimes(load(id), "2025-01-02T02:00:00Z", "2025-01-02T02:00:00.000001Z", expiry)
			})

			t.Run("legacy-invalid-pairs", func(t *testing.T) {
				for _, legacy := range []struct {
					name       string
					start, end any
					repeat     map[string]any
					clear      map[string]any
				}{
					{"incomplete-start", start, nil, map[string]any{"maintenance_start": "2025-01-02T03:00:00.123456+02:00"}, map[string]any{"maintenance_start": ""}},
					{"incomplete-end", nil, end, map[string]any{"maintenance_end": "2025-01-02T06:00:00.123457+02:00"}, map[string]any{"maintenance_end": ""}},
					{"equal", start, start, map[string]any{"maintenance_start": "2025-01-02T03:00:00.123456+02:00", "maintenance_end": "2025-01-02T03:00:00.123456+02:00"}, map[string]any{"maintenance_start": "", "maintenance_end": ""}},
					{"inverted", end, start, map[string]any{"maintenance_start": "2025-01-02T06:00:00.123457+02:00", "maintenance_end": "2025-01-02T03:00:00.123456+02:00"}, map[string]any{"maintenance_start": "", "maintenance_end": ""}},
				} {
					t.Run(legacy.name, func(t *testing.T) {
						id, name := create(nil)
						if err := db.Model(&model.Node{}).Where("id = ?", id).Updates(map[string]any{"maintenance_start": legacy.start, "maintenance_end": legacy.end}).Error; err != nil {
							t.Fatal(err)
						}
						before := load(id)
						for _, fields := range []map[string]any{nil, {"maintenance_start": nil, "maintenance_end": nil}, legacy.repeat} {
							checkStatus(put(id, name, fields), http.StatusOK)
							after := load(id)
							if !reflect.DeepEqual(before.MaintenanceStart, after.MaintenanceStart) || !reflect.DeepEqual(before.MaintenanceEnd, after.MaintenanceEnd) {
								t.Fatal("no-op changed legacy timestamps")
							}
						}
						checkStatus(put(id, name, legacy.clear), http.StatusOK)
						assertTimes(load(id), "", "", "")
						if err := db.Model(&model.Node{}).Where("id = ?", id).Updates(map[string]any{"maintenance_start": legacy.start, "maintenance_end": legacy.end}).Error; err != nil {
							t.Fatal(err)
						}
						checkStatus(put(id, name, map[string]any{"maintenance_start": start, "maintenance_end": end}), http.StatusOK)
						assertTimes(load(id), start, end, "")
					})
				}
			})

			t.Run("concurrent-updates", func(t *testing.T) {
				for _, scenario := range []struct {
					name               string
					first, second      map[string]any
					firstStatus        int
					wantStart, wantEnd string
				}{
					{"incompatible-one-sided", map[string]any{"maintenance_start": "2025-01-02T03:00:00Z"}, map[string]any{"maintenance_end": "2025-01-02T02:00:00Z"}, http.StatusBadRequest, start, "2025-01-02T02:00:00Z"},
					{"unrelated-preserves-newer", map[string]any{"tags": "concurrent"}, map[string]any{"maintenance_start": "2025-01-02T02:00:00Z", "maintenance_end": "2025-01-02T03:00:00Z"}, http.StatusOK, "2025-01-02T02:00:00Z", "2025-01-02T03:00:00Z"},
				} {
					t.Run(scenario.name, func(t *testing.T) {
						id, name := create(valid)
						gate := &pauseNodeTimeUpdate{NodeRepository: repo, entered: make(chan struct{}), release: make(chan struct{})}
						concurrentRouter := router(gate)
						path := fmt.Sprintf("/nodes/%d", id)
						firstBody := payload(name, scenario.first)
						result := make(chan *httptest.ResponseRecorder, 1)
						ctx, cancel := context.WithCancel(context.Background())
						defer cancel()
						go func() {
							req := httptest.NewRequest(http.MethodPut, path, bytes.NewReader(firstBody)).WithContext(ctx)
							req.Header.Set("Content-Type", "application/json")
							resp := httptest.NewRecorder()
							concurrentRouter.ServeHTTP(resp, req)
							result <- resp
						}()
						select {
						case <-gate.entered:
						case <-time.After(10 * time.Second):
							t.Fatal("first update did not reach repository")
						}
						checkStatus(request(concurrentRouter, http.MethodPut, path, payload(name, scenario.second)), http.StatusOK)
						close(gate.release)
						select {
						case resp := <-result:
							checkStatus(resp, scenario.firstStatus)
						case <-time.After(10 * time.Second):
							t.Fatal("first update did not finish")
						}
						got := load(id)
						assertTimes(got, scenario.wantStart, scenario.wantEnd, expiry)
						if scenario.firstStatus == http.StatusOK && got.Tags != "concurrent" {
							t.Errorf("unrelated update lost: tags=%q", got.Tags)
						}
					})
				}
			})
		})
	}
}

func TestNodeSanitizedSchedulingTimes(t *testing.T) {
	parse := func(value string) time.Time {
		t.Helper()
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			t.Fatal(err)
		}
		return parsed
	}
	for _, tc := range []struct {
		name, input, want string
	}{
		{"normal", "2025-01-02T03:04:05.123456789+02:00", "2025-01-02T01:04:05.123456789Z"},
		{"upper-scanned-year-overflow", "9999-12-31T23:30:00.123456-01:00", "9999-12-31T23:30:00.123456-01:00"},
		{"lower-scanned-year-overflow", "0000-01-01T00:30:00.123456+01:00", "0000-01-01T00:30:00.123456+01:00"},
		{"upper-utc-year-valid", "9999-12-31T23:30:00.123456+01:00", "9999-12-31T22:30:00.123456Z"},
		{"lower-utc-year-valid", "0000-01-01T00:30:00.123456-01:00", "0000-01-01T01:30:00.123456Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := parse(tc.input)
			before := original
			n := model.Node{Password: "secret", PrivateKey: "secret", MaintenanceStart: &original, MaintenanceEnd: &original, ExpiryDate: &original, LastSeenAt: &original}
			safe := n.Sanitized()
			if original != before || n.MaintenanceStart != &original || safe.LastSeenAt != &original || safe.Password != "" || safe.PrivateKey != "" {
				t.Fatal("sanitizing changed the source, unrelated timestamp, or credentials")
			}
			encoded, err := json.Marshal(safe)
			if err != nil {
				t.Fatalf("marshal sanitized node: %v", err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &fields); err != nil {
				t.Fatal(err)
			}
			for _, field := range []string{"maintenance_start", "maintenance_end", "expiry_date"} {
				if string(fields[field]) != `"`+tc.want+`"` {
					t.Errorf("%s=%s, want %q", field, fields[field], tc.want)
				}
			}
		})
	}
	if safe := (model.Node{}).Sanitized(); safe.MaintenanceStart != nil || safe.MaintenanceEnd != nil || safe.ExpiryDate != nil {
		t.Fatal("nil scheduling times changed")
	}
}

func TestNodeTimestampWirePostgresNonUTC(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	key := sha256.Sum256([]byte("FAKE_NODE_TIMESTAMP_ENCRYPTION_KEY_FOR_TEST_ONLY"))
	t.Setenv("DATA_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(key[:]))
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)

	fixture := openMigratedRetryTransactionDB(t, "postgres")
	var schema string
	if err := fixture.Raw("SELECT current_schema()").Scan(&schema).Error; err != nil || schema == "" {
		t.Fatalf("read fixture schema: %v", err)
	}
	parsed, err := url.Parse(strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN")))
	if err != nil {
		t.Fatalf("parse PostgreSQL URL: %v", err)
	}
	open := func(timezone string) (*gin.Engine, *gorm.DB) {
		t.Helper()
		scoped := *parsed
		query := scoped.Query()
		query.Set("search_path", schema)
		query.Set("timezone", timezone)
		query.Del("TimeZone")
		scoped.RawQuery = query.Encode()
		db, err := database.OpenQuiet(config.Config{DBType: "postgres", PostgresDSN: scoped.String()})
		if err != nil {
			t.Fatalf("open production PostgreSQL connection: %v", err)
		}
		sqlDB, err := db.DB()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = sqlDB.Close() })
		var actualZone string
		if err := db.Raw("SHOW timezone").Scan(&actualZone).Error; err != nil || actualZone != timezone {
			t.Fatalf("PostgreSQL timezone=%q, want %q: %v", actualZone, timezone, err)
		}
		r := gin.New()
		r.Use(func(c *gin.Context) { c.Set(middleware.CtxRole, "admin"); c.Next() })
		h := NewNodeHandler(db, nil, nodepkg.NewNodeService(gormrepo.NewNodeRepository(db)))
		r.POST("/nodes", h.Create)
		r.PUT("/nodes/:id", h.Update)
		r.GET("/nodes/:id", h.Get)
		r.GET("/nodes", h.List)
		return r, db
	}
	r, productionDB := open("Etc/GMT-10")
	body := func(fields map[string]string) []byte {
		t.Helper()
		values := map[string]string{"name": "wire-time-node", "host": "192.0.2.10", "username": "tester", "auth_type": "password", "password": "FAKE_NODE_PASSWORD_FOR_TEST_ONLY"}
		for k, v := range fields {
			values[k] = v
		}
		data, err := json.Marshal(values)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	request := func(router *gin.Engine, method, path string, payload []byte, status int) json.RawMessage {
		t.Helper()
		req := httptest.NewRequest(method, path, bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)
		if resp.Code != status {
			t.Fatalf("%s %s status=%d want=%d body=%s", method, path, resp.Code, status, resp.Body.String())
		}
		var envelope struct {
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(resp.Body.Bytes(), &envelope); err != nil || len(envelope.Data) == 0 {
			t.Fatalf("%s %s invalid response: %v body=%s", method, path, err, resp.Body.String())
		}
		return envelope.Data
	}
	assertNode := func(raw json.RawMessage, want map[string]string) uint {
		t.Helper()
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatalf("decode node: %v data=%s", err, raw)
		}
		var id uint
		if err := json.Unmarshal(fields["id"], &id); err != nil || id == 0 {
			t.Fatalf("invalid node id: %v data=%s", err, raw)
		}
		for _, field := range []string{"maintenance_start", "maintenance_end", "expiry_date"} {
			value, present := want[field]
			if !present {
				if _, exists := fields[field]; exists {
					t.Errorf("%s should be omitted: %s", field, raw)
				}
			} else if string(fields[field]) != `"`+value+`"` {
				t.Errorf("%s=%s, want %q", field, fields[field], value)
			}
		}
		if _, exists := fields["password"]; exists {
			t.Errorf("response contains password: %s", raw)
		}
		return id
	}
	assertReads := func(router *gin.Engine, id uint, want map[string]string) {
		t.Helper()
		path := fmt.Sprintf("/nodes/%d", id)
		if got := assertNode(request(router, http.MethodGet, path, nil, http.StatusOK), want); got != id {
			t.Fatalf("GET id=%d, want %d", got, id)
		}
		var nodes []json.RawMessage
		if err := json.Unmarshal(request(router, http.MethodGet, "/nodes", nil, http.StatusOK), &nodes); err != nil || len(nodes) != 1 {
			t.Fatalf("decode list: %v nodes=%s", err, nodes)
		}
		if got := assertNode(nodes[0], want); got != id {
			t.Fatalf("list id=%d, want %d", got, id)
		}
	}
	created := map[string]string{"maintenance_start": "9999-12-31T22:30:00.123456Z", "maintenance_end": "9999-12-31T23:30:00.654321Z", "expiry_date": "2025-01-02T03:04:05.000001Z"}
	id := assertNode(request(r, http.MethodPost, "/nodes", body(created), http.StatusCreated), created)
	var scanned model.Node
	// The production driver must actually expose the rollover before sanitizing.
	if err := productionDB.First(&scanned, id).Error; err != nil || scanned.MaintenanceStart == nil || scanned.MaintenanceStart.Year() != 10000 {
		t.Fatalf("expected non-UTC scanned year 10000, node=%+v err=%v", scanned, err)
	}
	assertReads(r, id, created)
	updated := map[string]string{"maintenance_start": "9999-12-31T22:31:00.123456Z", "maintenance_end": "9999-12-31T23:31:00.654321Z", "expiry_date": "2025-01-02T03:04:06.000002Z"}
	path := fmt.Sprintf("/nodes/%d", id)
	var update struct {
		Node json.RawMessage `json:"node"`
	}
	if err := json.Unmarshal(request(r, http.MethodPut, path, body(updated), http.StatusOK), &update); err != nil {
		t.Fatal(err)
	}
	assertNode(update.Node, updated)
	assertReads(r, id, updated)

	// A legacy instant beyond UTC year 9999 is still serializable in -05:00.
	if err := fixture.Exec("UPDATE nodes SET maintenance_start = NULL, maintenance_end = NULL, expiry_date = TIMESTAMPTZ '9999-12-31 23:30:00.123456-05:00' WHERE id = ?", id).Error; err != nil {
		t.Fatal(err)
	}
	legacyRouter, legacyDB := open("Etc/GMT+5")
	legacy := map[string]string{"expiry_date": "9999-12-31T23:30:00.123456-05:00"}
	if err := json.Unmarshal(request(legacyRouter, http.MethodPut, path, body(nil), http.StatusOK), &update); err != nil {
		t.Fatal(err)
	}
	assertNode(update.Node, legacy)
	assertReads(legacyRouter, id, legacy)

	// The inverse boundary scans into year -1, but remains valid in UTC.
	lower := map[string]string{"expiry_date": "0000-01-01T01:00:00.123456Z"}
	if err := json.Unmarshal(request(legacyRouter, http.MethodPut, path, body(lower), http.StatusOK), &update); err != nil {
		t.Fatal(err)
	}
	assertNode(update.Node, lower)
	if err := legacyDB.First(&scanned, id).Error; err != nil || scanned.ExpiryDate == nil || scanned.ExpiryDate.Year() != -1 {
		t.Fatalf("expected non-UTC scanned year -1, node=%+v err=%v", scanned, err)
	}
	assertReads(legacyRouter, id, lower)
}
