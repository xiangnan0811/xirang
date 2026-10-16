package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"xirang/backend/internal/api/handlers"
	"xirang/backend/internal/auth"
	"xirang/backend/internal/database"
	"xirang/backend/internal/model"
	"xirang/backend/internal/reporting"
	"xirang/backend/internal/settings"
)

const reportSchedulePreviewPath = "/api/v1/report-configs/schedule-preview"
const reportSchedulePreviewJWTSecret = "FAKE_REPORT_PREVIEW_JWT_SECRET_FOR_TEST_ONLY"

// Trace records actual GORM executions, including raw SQL and middleware queries.
// Recording is enabled only around requests, excluding fixture and snapshot SQL.
type reportSchedulePreviewSQL struct {
	logger.Interface
	mu         sync.Mutex
	recording  bool
	statements []string
}

func (l *reportSchedulePreviewSQL) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.recording {
		sql, _ := fc()
		l.statements = append(l.statements, sql)
	}
}

func (l *reportSchedulePreviewSQL) start() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.statements = nil
	l.recording = true
}

func (l *reportSchedulePreviewSQL) stop() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.recording = false
	return append([]string(nil), l.statements...)
}

type reportSchedulePreviewFixture struct {
	db       *gorm.DB
	router   *gin.Engine
	tokens   map[string]string
	sql      *reportSchedulePreviewSQL
	configID uint
	reportID uint
}

func newReportSchedulePreviewFixture(t *testing.T) reportSchedulePreviewFixture {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_REPORT_PREVIEW_ENCRYPTION_KEY_FOR_TEST_ONLY")
	gin.SetMode(gin.TestMode)
	trace := &reportSchedulePreviewSQL{Interface: logger.Default.LogMode(logger.Silent)}
	dsn := filepath.Join(t.TempDir(), "report-preview.sqlite") + "?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=on&_synchronous=NORMAL&_txlock=immediate&_loc=UTC"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: trace, NowFunc: func() time.Time { return time.Now().UTC() }})
	if err != nil {
		t.Fatalf("open report preview SQLite: %v", err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatalf("get report preview SQLite pool: %v", err)
	}
	pool.SetMaxOpenConns(1)
	pool.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	if err := database.RunMigrations(db, "sqlite"); err != nil {
		t.Fatalf("run report preview production migrations: %v", err)
	}
	manager := auth.NewJWTManager(reportSchedulePreviewJWTSecret, time.Hour)
	manager.SetDB(db)
	tokens := make(map[string]string)
	var admin model.User
	for _, role := range []string{"admin", "operator", "viewer", "unknown", ""} {
		user := model.User{Username: "report-preview-" + role, PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY", Role: role}
		if err := db.Create(&user).Error; err != nil {
			t.Fatalf("create %q user: %v", role, err)
		}
		token, err := manager.GenerateToken(user)
		if err != nil {
			t.Fatalf("generate %q token: %v", role, err)
		}
		tokens[role] = token
		if role == "admin" {
			admin = user
		}
	}
	expired, err := auth.NewJWTManager(reportSchedulePreviewJWTSecret, -time.Hour).GenerateToken(admin)
	if err != nil {
		t.Fatalf("generate expired token: %v", err)
	}
	tokens["expired"] = expired
	revoked, err := manager.GenerateToken(admin)
	if err != nil {
		t.Fatalf("generate revoked token: %v", err)
	}
	claims, err := manager.ParseToken(revoked)
	if err != nil {
		t.Fatalf("parse revoked token: %v", err)
	}
	if err := manager.RevokeSession(claims.ID, admin.ID, claims.ExpiresAt.Time); err != nil {
		t.Fatalf("revoke session: %v", err)
	}
	tokens["revoked"] = revoked
	tokens["invalid"] = "FAKE_INVALID_JWT_FOR_TEST_ONLY"

	// Seed real rows so read-only assertions detect mutations as well as inserts.
	now := time.Now().UTC()
	config := model.ReportConfig{Name: "preview-sentinel", Cron: "0 8 * * 1"}
	if err := db.Create(&config).Error; err != nil {
		t.Fatalf("seed report config: %v", err)
	}
	report := model.Report{ConfigID: config.ID, PeriodStart: now.Add(-24 * time.Hour), PeriodEnd: now, GeneratedAt: now}
	if err := db.Create(&report).Error; err != nil {
		t.Fatalf("seed report: %v", err)
	}
	for _, row := range []any{
		&model.AuditLog{UserID: admin.ID, Username: admin.Username, Role: "admin", Method: "POST", Path: "/fixture", StatusCode: 200, CreatedAt: now},
		&model.CredentialAuditEvent{UserID: admin.ID, Username: admin.Username, Role: "admin", Action: "preview-fixture", Purpose: "fixture", CredentialKind: "none", CredentialSource: "none", Outcome: "success", CreatedAt: now},
		&model.CredentialAccessGrant{RequesterUserID: admin.ID, RequesterUsername: admin.Username, RequesterRole: "admin", Action: "config.import", Purpose: "config_import", Status: "approved", RequestedAt: now, ExpiresAt: now.Add(time.Hour)},
	} {
		if err := db.Create(row).Error; err != nil {
			t.Fatalf("seed preview sentinel %T: %v", row, err)
		}
	}
	// Consumed step-up proofs are persisted in token_revocations, not a
	// separate proof table. Preserve an actual consumed proof here.
	proof, _, err := manager.GenerateStepUpToken(admin, auth.StepUpActionConfigImport)
	if err != nil {
		t.Fatalf("generate proof sentinel: %v", err)
	}
	if err := manager.RevokeToken(proof); err != nil {
		t.Fatalf("consume proof sentinel: %v", err)
	}
	mainSessionOnlyProof, _, err := manager.GenerateStepUpToken(admin, auth.StepUpActionConfigImport)
	if err != nil {
		t.Fatalf("generate purpose-bound token: %v", err)
	}
	tokens["step-up"] = mainSessionOnlyProof
	appCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	router := NewRouter(Dependencies{AppContext: appCtx, DB: db, JWTManager: manager, SettingsService: settings.NewService(db)})
	return reportSchedulePreviewFixture{db: db, router: router, tokens: tokens, sql: trace, configID: config.ID, reportID: report.ID}
}

// Snapshot every migrated table, including reports/configs, HTTP and credential
// audit, consumed proofs, all grants, and SQLite sequences. No model hooks hide
// stored columns; canonical JSON plus row sorting removes SELECT ordering noise.
func reportSchedulePreviewSnapshot(t *testing.T, db *gorm.DB) map[string][]string {
	t.Helper()
	var tables []string
	if err := db.Raw("SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name").Scan(&tables).Error; err != nil {
		t.Fatalf("list preview snapshot tables: %v", err)
	}
	snapshot := make(map[string][]string, len(tables))
	for _, table := range tables {
		var rows []map[string]any
		quoted := `"` + strings.ReplaceAll(table, `"`, `""`) + `"`
		if err := db.Raw("SELECT * FROM " + quoted).Scan(&rows).Error; err != nil {
			t.Fatalf("snapshot %s: %v", table, err)
		}
		encoded := make([]string, 0, len(rows))
		for _, row := range rows {
			data, err := json.Marshal(row)
			if err != nil {
				t.Fatalf("encode snapshot %s: %v", table, err)
			}
			encoded = append(encoded, string(data))
		}
		sort.Strings(encoded)
		snapshot[table] = encoded
	}
	for _, table := range []string{"reports", "report_configs", "audit_logs", "credential_audit_events", "token_revocations", "credential_access_grants"} {
		if len(snapshot[table]) == 0 {
			t.Fatalf("snapshot lacks seeded %s rows", table)
		}
	}
	return snapshot
}

var reportSchedulePreviewWriteSQL = regexp.MustCompile(`(?i)\b(INSERT|UPDATE|DELETE|REPLACE)\b`)

func assertReportSchedulePreviewReadOnly(t *testing.T, fixture reportSchedulePreviewFixture, before map[string][]string, statements []string, noBusinessReads bool) {
	t.Helper()
	for _, sql := range statements {
		if reportSchedulePreviewWriteSQL.MatchString(sql) {
			t.Errorf("preview executed write SQL: %s", sql)
		}
		if noBusinessReads {
			for _, table := range []string{"report_configs", "reports", "audit_logs", "credential_audit_events", "credential_access_grants", "node_owners"} {
				if strings.Contains(strings.ToLower(sql), table) {
					t.Errorf("preview unexpectedly read business state %s: %s", table, sql)
				}
			}
		}
	}
	after := reportSchedulePreviewSnapshot(t, fixture.db)
	if !reflect.DeepEqual(before, after) {
		for table, rows := range before {
			if !reflect.DeepEqual(rows, after[table]) {
				t.Errorf("preview changed persistent rows in %s", table)
			}
		}
		if len(before) != len(after) {
			t.Error("preview changed persistent table set")
		}
	}
}

func reportSchedulePreviewRequest(router http.Handler, path, token string, ctx context.Context) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if ctx != nil {
		request = request.WithContext(ctx)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func reportSchedulePreviewURL(cron string) string {
	return reportSchedulePreviewPath + "?" + url.Values{"cron": {cron}}.Encode()
}

func assertReportSchedulePreviewError(t *testing.T, response *httptest.ResponseRecorder, status int, message string) {
	t.Helper()
	var envelope struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode preview error: %v; body=%s", err, response.Body.String())
	}
	if response.Code != status || envelope.Code != status || envelope.Message == "" || string(envelope.Data) != "null" {
		t.Fatalf("unexpected preview rejection: status=%d body=%s", response.Code, response.Body.String())
	}
	if message != "" && envelope.Message != message {
		t.Errorf("unsafe or unexpected error: %q, want %q", envelope.Message, message)
	}
}

func TestReportSchedulePreviewRouter(t *testing.T) {
	fixture := newReportSchedulePreviewFixture(t)
	t.Run("permissions and sessions", func(t *testing.T) {
		for _, test := range []struct {
			name  string
			token string
			code  int
		}{
			{"missing", "", 401},
			{"expired", fixture.tokens["expired"], 401},
			{"revoked", fixture.tokens["revoked"], 401},
			{"invalid", fixture.tokens["invalid"], 401},
			{"purpose-bound step-up token", fixture.tokens["step-up"], 401},
			{"operator", fixture.tokens["operator"], 403},
			{"viewer", fixture.tokens["viewer"], 403},
			{"unknown role", fixture.tokens["unknown"], 403},
			{"empty role", fixture.tokens[""], 403},
		} {
			t.Run(test.name, func(t *testing.T) {
				before := reportSchedulePreviewSnapshot(t, fixture.db)
				fixture.sql.start()
				response := reportSchedulePreviewRequest(fixture.router, reportSchedulePreviewURL("* * * * *"), test.token, nil)
				statements := fixture.sql.stop()
				assertReportSchedulePreviewError(t, response, test.code, "")
				assertReportSchedulePreviewReadOnly(t, fixture, before, statements, true)
			})
		}
		// Invalid query must not bypass or run ahead of authentication/RBAC.
		for _, test := range []struct {
			token string
			code  int
		}{{"", 401}, {fixture.tokens["viewer"], 403}} {
			before := reportSchedulePreviewSnapshot(t, fixture.db)
			fixture.sql.start()
			response := reportSchedulePreviewRequest(fixture.router, reportSchedulePreviewPath+"?now=unsafe", test.token, nil)
			statements := fixture.sql.stop()
			assertReportSchedulePreviewError(t, response, test.code, "")
			assertReportSchedulePreviewReadOnly(t, fixture, before, statements, true)
		}
	})

	t.Run("strict safe query validation", func(t *testing.T) {
		for _, test := range []struct{ name, query string }{
			{"missing", ""},
			{"empty", "cron="},
			{"whitespace", "cron=++%09+"},
			{"duplicate", "cron=*+*+*+*+*&cron=*+*+*+*+*"},
			{"encoded duplicate", "cron=*+*+*+*+*&%63ron=*+*+*+*+*"},
			{"unknown", "cron=*+*+*+*+*&extra=unsafe"},
			{"clock override", "cron=*+*+*+*+*&now=unsafe"},
			{"timezone override", "cron=*+*+*+*+*&timezone=unsafe"},
			{"invalid escape", "cron=*+*+*+*+*&extra=%ZZ"},
			{"invalid cron escape", "cron=%ZZ"},
			{"semicolon", "cron=*+*+*+*+*;extra=unsafe"},
			{"four fields", url.Values{"cron": {"* * * *"}}.Encode()},
			{"six fields", url.Values{"cron": {"* * * * * *"}}.Encode()},
			{"descriptor", url.Values{"cron": {"@daily"}}.Encode()},
			{"over byte limit", url.Values{"cron": {strings.Repeat(" ", 504) + "* * * * *"}}.Encode()},
			{"multibyte limit", url.Values{"cron": {strings.Repeat("界", 169) + " * * * *"}}.Encode()},
		} {
			t.Run(test.name, func(t *testing.T) {
				before := reportSchedulePreviewSnapshot(t, fixture.db)
				fixture.sql.start()
				response := reportSchedulePreviewRequest(fixture.router, reportSchedulePreviewPath+"?"+test.query, fixture.tokens["admin"], nil)
				statements := fixture.sql.stop()
				assertReportSchedulePreviewError(t, response, 400, "无效的报表预估表达式")
				if response.Header().Get("Cache-Control") != "no-store" {
					t.Error("handler validation error lacks no-store")
				}
				assertReportSchedulePreviewReadOnly(t, fixture, before, statements, true)
			})
		}
	})

	t.Run("real DTO and matcher semantics", func(t *testing.T) {
		for _, test := range []struct {
			name, cron, status, reason string
		}{
			{"current", "* * * * *", "match", ""},
			{"month", "* * * 1 *", "no_match", "unsupported_month"},
			{"step", "*/15 * * * *", "no_match", "unmatchable_field"},
			{"impossible day", "0 8 32 * *", "no_match", "unmatchable_field"},
			{"numeric prefix", "5,10 * * * *", "match", ""},
			{"exact decoded byte limit", strings.Repeat(" ", 503) + "* * * * *", "match", ""},
		} {
			t.Run(test.name, func(t *testing.T) {
				before := reportSchedulePreviewSnapshot(t, fixture.db)
				started := time.Now()
				fixture.sql.start()
				response := reportSchedulePreviewRequest(fixture.router, reportSchedulePreviewURL(test.cron), fixture.tokens["admin"], nil)
				statements := fixture.sql.stop()
				ended := time.Now()
				if response.Code != 200 || response.Header().Get("Cache-Control") != "no-store" {
					t.Fatalf("unexpected preview success: status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
				}
				var envelope struct {
					Code    int                       `json:"code"`
					Message string                    `json:"message"`
					Data    reporting.SchedulePreview `json:"data"`
				}
				if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
					t.Fatalf("decode preview DTO: %v", err)
				}
				data := envelope.Data
				if envelope.Code != 200 || envelope.Message != "ok" || data.Status != test.status || data.Reason != test.reason {
					t.Fatalf("unexpected preview envelope: %s", response.Body.String())
				}
				if data.EvaluatedAt.Before(started) || data.EvaluatedAt.After(ended) || data.Timezone != time.Local.String() {
					t.Errorf("preview did not use server current time/location: %+v", data)
				}
				now := data.EvaluatedAt.In(time.Local)
				minuteStart := now.Add(-time.Duration(now.Second())*time.Second - time.Duration(now.Nanosecond()))
				if !data.ExpiresAt.Equal(minuteStart.Add(time.Minute)) || !data.SearchUntil.Equal(now.AddDate(5, 0, 0)) {
					t.Errorf("incorrect DTO evaluation boundaries: %+v", data)
				}
				_, gotOffset := data.EvaluatedAt.Zone()
				_, wantOffset := now.Zone()
				if gotOffset != wantOffset {
					t.Errorf("response lost server UTC offset: got %d want %d", gotOffset, wantOffset)
				}
				if test.status == "no_match" {
					if data.WindowStart != nil || data.WindowEnd != nil || !strings.Contains(response.Body.String(), `"window_start":null,"window_end":null`) {
						t.Errorf("no_match must have explicit null windows: %s", response.Body.String())
					}
				} else {
					if data.WindowStart == nil || data.WindowEnd == nil {
						t.Fatalf("match lacks window: %+v", data)
					}
					if data.WindowEnd.Sub(*data.WindowStart) != time.Minute || !data.WindowEnd.After(data.EvaluatedAt) || !data.WindowStart.Before(data.SearchUntil) {
						t.Errorf("contradictory matching window: %+v", data)
					}
					if test.name == "current" && (!data.WindowStart.Equal(minuteStart) || !data.WindowEnd.Equal(data.ExpiresAt)) {
						t.Errorf("wildcard must describe current matching minute: %+v", data)
					}
					if test.name == "numeric prefix" && data.WindowStart.In(time.Local).Minute() != 5 {
						t.Errorf("numeric prefix must use historical matcher, not standard list: %+v", data)
					}
				}
				if len(statements) == 0 {
					t.Fatal("SQL recorder missed real authentication queries")
				}
				assertReportSchedulePreviewReadOnly(t, fixture, before, statements, true)
			})
		}
	})

	t.Run("existing child routes remain distinct", func(t *testing.T) {
		for _, test := range []struct {
			path string
			code int
		}{
			{fmt.Sprintf("/api/v1/report-configs/%d/reports", fixture.configID), 200},
			{reportSchedulePreviewPath + "/reports", 400},
			{fmt.Sprintf("/api/v1/reports/%d", fixture.reportID), 200},
		} {
			before := reportSchedulePreviewSnapshot(t, fixture.db)
			fixture.sql.start()
			response := reportSchedulePreviewRequest(fixture.router, test.path, fixture.tokens["admin"], nil)
			statements := fixture.sql.stop()
			if response.Code != test.code || strings.Contains(response.Body.String(), `"evaluated_at"`) {
				t.Errorf("route confused with preview: %s status=%d body=%s", test.path, response.Code, response.Body.String())
			}
			assertReportSchedulePreviewReadOnly(t, fixture, before, statements, false)
		}
	})
}

func TestReportSchedulePreviewRouterRateLimitReadOnly(t *testing.T) {
	fixture := newReportSchedulePreviewFixture(t)
	before := reportSchedulePreviewSnapshot(t, fixture.db)
	fixture.sql.start()
	defer func() {
		assertReportSchedulePreviewReadOnly(t, fixture, before, fixture.sql.stop(), true)
	}()
	for i := range 200 {
		response := reportSchedulePreviewRequest(fixture.router, reportSchedulePreviewURL("* * * 1 *"), fixture.tokens["admin"], nil)
		if response.Code != 200 {
			t.Fatalf("request %d unexpectedly rejected: %d %s", i+1, response.Code, response.Body.String())
		}
	}
	response := reportSchedulePreviewRequest(fixture.router, reportSchedulePreviewURL("* * * 1 *"), fixture.tokens["admin"], nil)
	var envelope struct {
		Code int `json:"code"`
		Data struct {
			RetryAfter int `json:"retry_after"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode rate limit: %v", err)
	}
	if response.Code != 429 || envelope.Code != 429 || envelope.Data.RetryAfter <= 0 || response.Header().Get("Retry-After") != fmt.Sprint(envelope.Data.RetryAfter) {
		t.Errorf("rate limit contract not preserved: status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
}

func TestReportSchedulePreviewRouterCanceledAuthenticationReadOnly(t *testing.T) {
	fixture := newReportSchedulePreviewFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	before := reportSchedulePreviewSnapshot(t, fixture.db)
	fixture.sql.start()
	response := reportSchedulePreviewRequest(fixture.router, reportSchedulePreviewURL("* * * * *"), fixture.tokens["admin"], ctx)
	statements := fixture.sql.stop()
	// The real auth middleware fails first. Do not claim handler headers or
	// messages apply before authentication has admitted a request.
	assertReportSchedulePreviewError(t, response, 503, "认证服务不可用")
	assertReportSchedulePreviewReadOnly(t, fixture, before, statements, true)
}

func TestReportSchedulePreviewHandlerCancellation(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	deadline, stop := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer stop()
	for _, test := range []struct {
		name string
		ctx  context.Context
	}{{"canceled", canceled}, {"deadline exceeded", deadline}} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(response)
			c.Request = httptest.NewRequest(http.MethodGet, reportSchedulePreviewURL("* * * * *"), nil).WithContext(test.ctx)
			// A nil DB is deliberate: this real handler must never need one.
			handlers.NewReportHandler(nil).PreviewSchedule(c)
			assertReportSchedulePreviewError(t, response, 503, "报表预估暂不可用")
			if response.Header().Get("Cache-Control") != "no-store" {
				t.Error("handler cancellation error lacks no-store")
			}
		})
	}
}
