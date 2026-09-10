package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"
	taskpkg "xirang/backend/internal/task"
	taskscheduler "xirang/backend/internal/task/scheduler"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var r306PostgresSchemaSequence atomic.Uint64

func openR306DB(t *testing.T, engine string, models ...any) *gorm.DB {
	t.Helper()
	gormConfig := &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)}
	if engine == "postgres" {
		dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
		if dsn == "" {
			t.Skip("TEST_POSTGRES_DSN required for R3-06 PostgreSQL acceptance")
		}
		parsed, err := url.Parse(dsn)
		if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
			t.Fatalf("TEST_POSTGRES_DSN must be a PostgreSQL URL: %v", err)
		}
		base, err := gorm.Open(postgres.Open(dsn), gormConfig)
		if err != nil {
			t.Fatalf("open PostgreSQL base connection: %v", err)
		}
		schema := fmt.Sprintf("r306_%d_%d", os.Getpid(), r306PostgresSchemaSequence.Add(1))
		if err := base.Exec("CREATE SCHEMA " + schema).Error; err != nil {
			t.Fatalf("create PostgreSQL test schema: %v", err)
		}
		query := parsed.Query()
		query.Set("search_path", schema)
		parsed.RawQuery = query.Encode()
		db, err := gorm.Open(postgres.Open(parsed.String()), gormConfig)
		if err != nil {
			_ = base.Exec("DROP SCHEMA " + schema + " CASCADE").Error
			t.Fatalf("open PostgreSQL isolated connection: %v", err)
		}
		scopedSQLDB, err := db.DB()
		if err != nil {
			t.Fatalf("get PostgreSQL isolated sql.DB: %v", err)
		}
		baseSQLDB, err := base.DB()
		if err != nil {
			_ = scopedSQLDB.Close()
			t.Fatalf("get PostgreSQL base sql.DB: %v", err)
		}
		t.Cleanup(func() {
			_ = scopedSQLDB.Close()
			_ = base.Exec("DROP SCHEMA " + schema + " CASCADE").Error
			_ = baseSQLDB.Close()
		})
		if err := db.AutoMigrate(models...); err != nil {
			t.Fatalf("migrate PostgreSQL R3-06 tables: %v", err)
		}
		return db
	}

	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "r306.sqlite")+"?_busy_timeout=5000&_txlock=immediate"), gormConfig)
	if err != nil {
		t.Fatalf("open SQLite R3-06 database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get SQLite sql.DB: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(models...); err != nil {
		t.Fatalf("migrate SQLite R3-06 tables: %v", err)
	}
	return db
}

func r306AdminRouter() *gin.Engine {
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("role", "admin")
		c.Next()
	})
	return r
}

func r306JSONRequest(t *testing.T, method, path string, body any) *http.Request {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal R3-06 request: %v", err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestR306ServiceMonitorCreateValuesAcrossDatabases(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)

	for _, engine := range []string{"sqlite", "postgres"} {
		t.Run(engine, func(t *testing.T) {
			db := openR306DB(t, engine, &model.ServiceMonitor{})
			handler := NewServiceMonitorHandler(db, nil)
			r := r306AdminRouter()
			r.POST("/service-monitors", handler.Create)
			r.GET("/status-page", handler.StatusPage)

			const headers = `{"Authorization":"FAKE_R306_MONITOR_HEADER_SECRET_FOR_TEST_ONLY"}`
			cases := []struct {
				name        string
				body        map[string]any
				wantEnabled bool
			}{
				{
					name: "omitted",
					body: map[string]any{
						"name": "r306-monitor-omitted", "type": "http", "target": "https://example.invalid",
					},
					wantEnabled: true,
				},
				{
					name: "explicit false",
					body: map[string]any{
						"name": "r306-monitor-disabled", "type": "http", "target": "https://disabled.invalid",
						"enabled": false, "http_headers": headers,
					},
					wantEnabled: false,
				},
				{
					name: "explicit true",
					body: map[string]any{
						"name": "r306-monitor-enabled", "type": "tcp", "target": "127.0.0.1:65535",
						"enabled": true,
					},
					wantEnabled: true,
				},
			}

			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					response := httptest.NewRecorder()
					r.ServeHTTP(response, r306JSONRequest(t, http.MethodPost, "/service-monitors", tc.body))
					if response.Code != http.StatusCreated {
						t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
					}
					var envelope struct {
						Code int                    `json:"code"`
						Data serviceMonitorResponse `json:"data"`
					}
					if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
						t.Fatalf("decode create response: %v", err)
					}
					if envelope.Code != http.StatusCreated || envelope.Data.ID == 0 {
						t.Fatalf("unexpected response envelope: %+v", envelope)
					}
					if envelope.Data.Enabled != tc.wantEnabled {
						t.Fatalf("response enabled=%v, want %v", envelope.Data.Enabled, tc.wantEnabled)
					}

					var rawEnabled bool
					var rawHeaders string
					row := db.Raw("SELECT enabled, http_headers FROM service_monitors WHERE id = ?", envelope.Data.ID).Row()
					if err := row.Scan(&rawEnabled, &rawHeaders); err != nil {
						t.Fatalf("read raw monitor values: %v", err)
					}
					if rawEnabled != tc.wantEnabled {
						t.Fatalf("raw enabled=%v, want %v", rawEnabled, tc.wantEnabled)
					}
					if tc.name == "explicit false" {
						if !secure.IsEncrypted(rawHeaders) {
							t.Fatalf("HTTP headers were not encrypted at the database boundary: %q", rawHeaders)
						}
						var loaded model.ServiceMonitor
						if err := db.First(&loaded, envelope.Data.ID).Error; err != nil {
							t.Fatalf("reload monitor: %v", err)
						}
						if loaded.HTTPHeaders != headers {
							t.Fatalf("decrypted headers=%q, want %q", loaded.HTTPHeaders, headers)
						}
					}
				})
			}

			response := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/status-page", nil)
			r.ServeHTTP(response, req)
			if response.Code != http.StatusOK {
				t.Fatalf("status-page status=%d body=%s", response.Code, response.Body.String())
			}
			var statusEnvelope struct {
				Data []struct {
					Name string `json:"name"`
				} `json:"data"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &statusEnvelope); err != nil {
				t.Fatalf("decode status-page response: %v", err)
			}
			for _, item := range statusEnvelope.Data {
				if item.Name == "r306-monitor-disabled" {
					t.Fatal("disabled monitor appeared on the public status page")
				}
			}
		})
	}
}

func TestR306PolicyCreateValuesAndSchedulesAcrossDatabases(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)

	for _, engine := range []string{"sqlite", "postgres"} {
		t.Run(engine, func(t *testing.T) {
			db := openR306DB(t, engine, &model.Node{}, &model.Policy{}, &model.PolicyNode{}, &model.Task{})
			node := model.Node{
				Name: fmt.Sprintf("r306-node-%s", engine), Host: "127.0.0.1", Port: 22,
				Username: "root", AuthType: "key", BackupDir: "r306-node-" + engine,
			}
			if err := db.Create(&node).Error; err != nil {
				t.Fatalf("create policy test node: %v", err)
			}

			cronScheduler := taskscheduler.NewCronScheduler()
			manager := taskpkg.NewManager(db, nil, nil, cronScheduler, nil, nil, 8, 90)
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				if err := manager.Shutdown(ctx); err != nil {
					t.Errorf("shutdown task manager: %v", err)
				}
				cronScheduler.Stop()
			})

			handler := NewPolicyHandler(db, manager)
			r := r306AdminRouter()
			r.POST("/policies", handler.Create)
			const cronSpec = "0 2 * * *"
			const preHook = "echo FAKE_R306_POLICY_HOOK_FOR_TEST_ONLY"
			cases := []struct {
				name           string
				body           map[string]any
				wantEnabled    bool
				wantVerify     bool
				wantMaxRetries int
				wantCron       string
				wantScheduled  bool
				wantHook       bool
			}{
				{
					name:        "omitted",
					body:        map[string]any{"name": "r306-policy-omitted", "source_path": "/data", "cron_spec": cronSpec, "node_ids": []uint{node.ID}},
					wantEnabled: true, wantVerify: true, wantMaxRetries: 2, wantCron: cronSpec, wantScheduled: true,
				},
				{
					name:        "enabled false",
					body:        map[string]any{"name": "r306-policy-disabled", "source_path": "/data", "cron_spec": cronSpec, "enabled": false, "node_ids": []uint{node.ID}},
					wantEnabled: false, wantVerify: true, wantMaxRetries: 2, wantCron: "", wantScheduled: false,
				},
				{
					name:        "enabled true",
					body:        map[string]any{"name": "r306-policy-enabled", "source_path": "/data", "cron_spec": cronSpec, "enabled": true, "node_ids": []uint{node.ID}},
					wantEnabled: true, wantVerify: true, wantMaxRetries: 2, wantCron: cronSpec, wantScheduled: true,
				},
				{
					name:        "verify false",
					body:        map[string]any{"name": "r306-policy-no-verify", "source_path": "/data", "cron_spec": cronSpec, "verify_enabled": false, "node_ids": []uint{node.ID}},
					wantEnabled: true, wantVerify: false, wantMaxRetries: 2, wantCron: cronSpec, wantScheduled: true,
				},
				{
					name:        "verify true",
					body:        map[string]any{"name": "r306-policy-verify", "source_path": "/data", "cron_spec": cronSpec, "verify_enabled": true, "node_ids": []uint{node.ID}},
					wantEnabled: true, wantVerify: true, wantMaxRetries: 2, wantCron: cronSpec, wantScheduled: true,
				},
				{
					name:        "max retries zero",
					body:        map[string]any{"name": "r306-policy-no-retries", "source_path": "/data", "cron_spec": cronSpec, "max_retries": 0, "pre_hook": preHook, "node_ids": []uint{node.ID}},
					wantEnabled: true, wantVerify: true, wantMaxRetries: 0, wantCron: cronSpec, wantScheduled: true, wantHook: true,
				},
				{
					name:        "max retries nonzero",
					body:        map[string]any{"name": "r306-policy-retries", "source_path": "/data", "cron_spec": cronSpec, "max_retries": 4, "node_ids": []uint{node.ID}},
					wantEnabled: true, wantVerify: true, wantMaxRetries: 4, wantCron: cronSpec, wantScheduled: true,
				},
			}

			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					response := httptest.NewRecorder()
					r.ServeHTTP(response, r306JSONRequest(t, http.MethodPost, "/policies", tc.body))
					if response.Code != http.StatusCreated {
						t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
					}
					var envelope struct {
						Code int `json:"code"`
						Data struct {
							ID            uint   `json:"id"`
							Enabled       bool   `json:"enabled"`
							VerifyEnabled bool   `json:"verify_enabled"`
							MaxRetries    int    `json:"max_retries"`
							PreHook       string `json:"pre_hook"`
						} `json:"data"`
					}
					if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
						t.Fatalf("decode policy response: %v", err)
					}
					if envelope.Code != http.StatusCreated || envelope.Data.ID == 0 {
						t.Fatalf("unexpected response envelope: %+v", envelope)
					}
					if envelope.Data.Enabled != tc.wantEnabled || envelope.Data.VerifyEnabled != tc.wantVerify || envelope.Data.MaxRetries != tc.wantMaxRetries {
						t.Fatalf("response values enabled=%v verify=%v max_retries=%d, want %v/%v/%d", envelope.Data.Enabled, envelope.Data.VerifyEnabled, envelope.Data.MaxRetries, tc.wantEnabled, tc.wantVerify, tc.wantMaxRetries)
					}
					if tc.wantHook && envelope.Data.PreHook != preHook {
						t.Fatalf("response pre_hook=%q, want %q", envelope.Data.PreHook, preHook)
					}

					var raw struct {
						Enabled       bool
						VerifyEnabled bool
						MaxRetries    int
						PreHook       string
					}
					row := db.Raw("SELECT enabled, verify_enabled, max_retries, pre_hook FROM policies WHERE id = ?", envelope.Data.ID).Row()
					if err := row.Scan(&raw.Enabled, &raw.VerifyEnabled, &raw.MaxRetries, &raw.PreHook); err != nil {
						t.Fatalf("read raw policy values: %v", err)
					}
					if raw.Enabled != tc.wantEnabled || raw.VerifyEnabled != tc.wantVerify || raw.MaxRetries != tc.wantMaxRetries {
						t.Fatalf("raw values enabled=%v verify=%v max_retries=%d, want %v/%v/%d", raw.Enabled, raw.VerifyEnabled, raw.MaxRetries, tc.wantEnabled, tc.wantVerify, tc.wantMaxRetries)
					}
					if tc.wantHook && !secure.IsEncrypted(raw.PreHook) {
						t.Fatalf("policy hook was not encrypted at the database boundary: %q", raw.PreHook)
					}

					var linked model.Task
					if err := db.Where("policy_id = ?", envelope.Data.ID).First(&linked).Error; err != nil {
						t.Fatalf("load generated task: %v", err)
					}
					if linked.CronSpec != tc.wantCron {
						t.Fatalf("generated task cron_spec=%q, want %q", linked.CronSpec, tc.wantCron)
					}
					if got := cronScheduler.HasTask(linked.ID); got != tc.wantScheduled {
						t.Fatalf("scheduler registration=%v for task %d, want %v", got, linked.ID, tc.wantScheduled)
					}
				})
			}
		})
	}
}
