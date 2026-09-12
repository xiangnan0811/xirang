package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"
	"xirang/backend/internal/uptime"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type serviceMonitorRBACTestFixture struct {
	db         *gorm.DB
	router     *gin.Engine
	tokens     map[string]string
	jwtManager *auth.JWTManager
}

func setupServiceMonitorRBACFixture(t *testing.T) serviceMonitorRBACTestFixture {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	gin.SetMode(gin.TestMode)

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		NowFunc: func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.ServiceMonitor{}, &model.AuditLog{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}

	jwtManager := auth.NewJWTManager("FAKE_SERVICE_MONITOR_JWT_SECRET_FOR_TEST_ONLY", time.Hour)
	tokens := make(map[string]string, 3)
	for _, role := range []string{"admin", "operator", "viewer"} {
		user := model.User{
			Username:     "service-monitor-rbac-" + role,
			PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY",
			Role:         role,
		}
		if err := db.Create(&user).Error; err != nil {
			t.Fatalf("create %s user: %v", role, err)
		}
		token, err := jwtManager.GenerateToken(user)
		if err != nil {
			t.Fatalf("generate %s token: %v", role, err)
		}
		tokens[role] = token
	}

	router := NewRouter(Dependencies{
		DB:         db,
		JWTManager: jwtManager,
	})

	return serviceMonitorRBACTestFixture{
		db:         db,
		router:     router,
		tokens:     tokens,
		jwtManager: jwtManager,
	}
}

func seedServiceMonitorForRBACTest(t *testing.T, db *gorm.DB) uint {
	t.Helper()
	monitor := model.ServiceMonitor{
		Name:               "FAKE_SERVICE_MONITOR_RBAC_FOR_TEST_ONLY",
		Type:               "http",
		Target:             "https://example.com",
		IntervalSeconds:    60,
		TimeoutSeconds:     10,
		HTTPMethod:         "GET",
		HTTPExpectedStatus: 200,
		HTTPHeaders:        "{}",
		Enabled:            true,
		LastStatus:         "unknown",
	}
	if err := db.Create(&monitor).Error; err != nil {
		t.Fatalf("seed service monitor: %v", err)
	}
	return monitor.ID
}

func performServiceMonitorRBACRequest(t *testing.T, router *gin.Engine, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if strings.TrimSpace(body) != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	return resp
}

func TestServiceMonitorRoutesRBACReadAuthorizedRoles(t *testing.T) {
	cases := []struct {
		name string
		path func(*testing.T, serviceMonitorRBACTestFixture) string
	}{
		{
			name: "list monitors",
			path: func(*testing.T, serviceMonitorRBACTestFixture) string { return "/api/v1/service-monitors" },
		},
		{
			name: "get monitor",
			path: func(t *testing.T, fx serviceMonitorRBACTestFixture) string {
				id := seedServiceMonitorForRBACTest(t, fx.db)
				return fmt.Sprintf("/api/v1/service-monitors/%d", id)
			},
		},
	}

	for _, role := range []string{"admin", "operator", "viewer"} {
		for _, tc := range cases {
			t.Run(role+" "+tc.name, func(t *testing.T) {
				fx := setupServiceMonitorRBACFixture(t)
				resp := performServiceMonitorRBACRequest(t, fx.router, http.MethodGet, tc.path(t, fx), fx.tokens[role], "")
				if resp.Code != http.StatusOK {
					t.Fatalf("expected 200, got %d: %s", resp.Code, resp.Body.String())
				}
			})
		}
	}
}

func TestServiceMonitorRoutesRBACWritePermissions(t *testing.T) {
	writeCases := []struct {
		name   string
		method string
		path   func(*testing.T, serviceMonitorRBACTestFixture) string
		body   string
		want   int
	}{
		{
			name:   "create monitor",
			method: http.MethodPost,
			path:   func(*testing.T, serviceMonitorRBACTestFixture) string { return "/api/v1/service-monitors" },
			body:   `{"name":"FAKE_SERVICE_MONITOR_CREATED_FOR_TEST_ONLY","type":"http","target":"https://example.com","interval_seconds":60,"timeout_seconds":10,"http_method":"GET","http_expected_status":200,"enabled":true}`,
			want:   http.StatusCreated,
		},
		{
			name:   "update monitor",
			method: http.MethodPut,
			path: func(t *testing.T, fx serviceMonitorRBACTestFixture) string {
				id := seedServiceMonitorForRBACTest(t, fx.db)
				return fmt.Sprintf("/api/v1/service-monitors/%d", id)
			},
			body: `{"name":"FAKE_SERVICE_MONITOR_UPDATED_FOR_TEST_ONLY","type":"tcp","target":"127.0.0.1:443","interval_seconds":60,"timeout_seconds":10,"enabled":true}`,
			want: http.StatusOK,
		},
		{
			name:   "delete monitor",
			method: http.MethodDelete,
			path: func(t *testing.T, fx serviceMonitorRBACTestFixture) string {
				id := seedServiceMonitorForRBACTest(t, fx.db)
				return fmt.Sprintf("/api/v1/service-monitors/%d", id)
			},
			want: http.StatusOK,
		},
	}

	for _, role := range []string{"admin", "operator"} {
		for _, tc := range writeCases {
			t.Run(role+" "+tc.name, func(t *testing.T) {
				fx := setupServiceMonitorRBACFixture(t)
				resp := performServiceMonitorRBACRequest(t, fx.router, tc.method, tc.path(t, fx), fx.tokens[role], tc.body)
				if resp.Code != tc.want {
					t.Fatalf("expected %d, got %d: %s", tc.want, resp.Code, resp.Body.String())
				}
			})
		}
	}
}

func TestServiceMonitorOperatorRetargetRequiresHeaderDecision(t *testing.T) {
	fx := setupServiceMonitorRBACFixture(t)
	const secret = "FAKE_SERVICE_MONITOR_OPERATOR_RETARGET_SECRET_FOR_TEST_ONLY"
	monitor := model.ServiceMonitor{
		Name:               "FAKE_SERVICE_MONITOR_OPERATOR_RETARGET_FOR_TEST_ONLY",
		Type:               "http",
		Target:             "https://receiver-a.example/health?probe=1",
		IntervalSeconds:    60,
		TimeoutSeconds:     10,
		HTTPMethod:         "GET",
		HTTPExpectedStatus: 200,
		HTTPHeaders:        fmt.Sprintf(`{"Authorization":%q}`, secret),
		Enabled:            true,
		LastStatus:         "unknown",
	}
	if err := fx.db.Create(&monitor).Error; err != nil {
		t.Fatalf("seed configured monitor: %v", err)
	}

	resp := performServiceMonitorRBACRequest(
		t,
		fx.router,
		http.MethodPut,
		fmt.Sprintf("/api/v1/service-monitors/%d", monitor.ID),
		fx.tokens["operator"],
		`{"name":"FAKE_SERVICE_MONITOR_OPERATOR_RETARGETED_FOR_TEST_ONLY","type":"http","target":"https://receiver-b.example/collect?probe=2"}`,
	)
	if resp.Code != http.StatusConflict {
		t.Fatalf("operator retarget status=%d body=%s", resp.Code, resp.Body.String())
	}
	var envelope struct {
		Data struct {
			Reason struct {
				Code string `json:"code"`
			} `json:"reason"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode operator retarget conflict: %v", err)
	}
	if envelope.Data.Reason.Code != "service_monitor_target_change_requires_headers" {
		t.Fatalf("retarget conflict code=%q", envelope.Data.Reason.Code)
	}

	var persisted model.ServiceMonitor
	if err := fx.db.First(&persisted, monitor.ID).Error; err != nil {
		t.Fatalf("reload configured monitor: %v", err)
	}
	if persisted.Target != monitor.Target || persisted.HTTPHeaders != fmt.Sprintf(`{"Authorization":%q}`, secret) {
		t.Fatalf("rejected retarget changed persisted monitor: %+v", persisted)
	}
}

func TestServiceMonitorAdminCreateOperatorRetargetAndProberDoNotLeakHeaders(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_SERVICE_MONITOR_E2E_ENCRYPTION_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)

	fx := setupServiceMonitorRBACFixture(t)
	if err := fx.db.AutoMigrate(&model.ServiceUptimeSample{}); err != nil {
		t.Fatalf("migrate uptime samples: %v", err)
	}

	const secret = "FAKE_SERVICE_MONITOR_E2E_SECRET_FOR_TEST_ONLY"
	firstAuth := make(chan string, 4)
	secondAuth := make(chan string, 4)
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case firstAuth <- r.Header.Get("Authorization"):
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case secondAuth <- r.Header.Get("Authorization"):
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer second.Close()

	prober := uptime.NewProber(fx.db, time.Second)
	prober.SetNowForTesting(func() time.Time { return time.Now().UTC() })
	router := NewRouter(Dependencies{
		DB:                     fx.db,
		JWTManager:             fx.jwtManager,
		ServiceMonitorNotifier: prober,
	})
	ctx, cancel := context.WithCancel(context.Background())
	prober.Start(ctx)
	t.Cleanup(func() {
		cancel()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer shutdownCancel()
		if err := prober.Shutdown(shutdownCtx); err != nil {
			t.Errorf("shutdown prober: %v", err)
		}
	})

	headerJSON, err := json.Marshal(map[string]string{"Authorization": secret})
	if err != nil {
		t.Fatalf("marshal headers: %v", err)
	}
	createBody, err := json.Marshal(map[string]interface{}{
		"name":                 "FAKE_SERVICE_MONITOR_E2E_ADMIN_CREATED_FOR_TEST_ONLY",
		"type":                 "http",
		"target":               first.URL + "/health?probe=1",
		"interval_seconds":     5,
		"timeout_seconds":      2,
		"http_method":          "GET",
		"http_expected_status": 200,
		"http_headers":         string(headerJSON),
		"enabled":              true,
	})
	if err != nil {
		t.Fatalf("marshal create body: %v", err)
	}
	created := performServiceMonitorRBACRequest(t, router, http.MethodPost, "/api/v1/service-monitors", fx.tokens["admin"], string(createBody))
	if created.Code != http.StatusCreated {
		t.Fatalf("admin create status=%d body=%s", created.Code, created.Body.String())
	}
	if strings.Contains(created.Body.String(), secret) {
		t.Fatalf("admin create response leaked secret: %s", created.Body.String())
	}
	var createdEnvelope struct {
		Data struct {
			ID uint `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &createdEnvelope); err != nil {
		t.Fatalf("decode admin create response: %v", err)
	}
	if createdEnvelope.Data.ID == 0 {
		t.Fatalf("admin create returned no monitor id: %s", created.Body.String())
	}

	select {
	case got := <-firstAuth:
		if got != secret {
			t.Fatalf("admin-created monitor authorization=%q, want configured fake secret", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("prober did not reach admin-created receiver")
	}

	retargetBody := `{"name":"FAKE_SERVICE_MONITOR_E2E_OPERATOR_RETARGET_FOR_TEST_ONLY","type":"http","target":"` + second.URL + `/collect?probe=2"}`
	rejected := performServiceMonitorRBACRequest(
		t,
		router,
		http.MethodPut,
		fmt.Sprintf("/api/v1/service-monitors/%d", createdEnvelope.Data.ID),
		fx.tokens["operator"],
		retargetBody,
	)
	if rejected.Code != http.StatusConflict {
		t.Fatalf("operator omitted-header retarget status=%d body=%s", rejected.Code, rejected.Body.String())
	}
	var persisted model.ServiceMonitor
	if err := fx.db.First(&persisted, createdEnvelope.Data.ID).Error; err != nil {
		t.Fatalf("load monitor after rejected retarget: %v", err)
	}
	if persisted.Target != first.URL+"/health?probe=1" || persisted.HTTPHeaders != string(headerJSON) {
		t.Fatalf("rejected retarget changed persisted monitor: target=%q headers=%q", persisted.Target, persisted.HTTPHeaders)
	}
	select {
	case got := <-secondAuth:
		t.Fatalf("receiver observed request before explicit header decision, authorization=%q", got)
	case <-time.After(100 * time.Millisecond):
	}

	clearBody, err := json.Marshal(map[string]interface{}{
		"name":                 "FAKE_SERVICE_MONITOR_E2E_OPERATOR_CLEARED_FOR_TEST_ONLY",
		"type":                 "http",
		"target":               second.URL + "/collect?probe=2",
		"interval_seconds":     5,
		"timeout_seconds":      2,
		"http_method":          "GET",
		"http_expected_status": 200,
		"http_headers":         "{}",
		"enabled":              true,
	})
	if err != nil {
		t.Fatalf("marshal clear body: %v", err)
	}
	cleared := performServiceMonitorRBACRequest(
		t,
		router,
		http.MethodPut,
		fmt.Sprintf("/api/v1/service-monitors/%d", createdEnvelope.Data.ID),
		fx.tokens["operator"],
		string(clearBody),
	)
	if cleared.Code != http.StatusOK {
		t.Fatalf("operator explicit clear retarget status=%d body=%s", cleared.Code, cleared.Body.String())
	}
	select {
	case got := <-secondAuth:
		if got != "" {
			t.Fatalf("retargeted receiver observed hidden authorization after explicit clear: %q", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("prober did not reach explicitly-cleared retarget")
	}
}

func TestServiceMonitorRoutesRBACViewerWriteForbidden(t *testing.T) {
	writeCases := []struct {
		name   string
		method string
		path   func(*testing.T, serviceMonitorRBACTestFixture) string
		body   string
	}{
		{
			name:   "create monitor",
			method: http.MethodPost,
			path:   func(*testing.T, serviceMonitorRBACTestFixture) string { return "/api/v1/service-monitors" },
			body:   `{"name":"FAKE_SERVICE_MONITOR_FORBIDDEN_CREATE_FOR_TEST_ONLY","type":"http","target":"https://example.com","interval_seconds":60,"timeout_seconds":10,"http_method":"GET","http_expected_status":200,"enabled":true}`,
		},
		{
			name:   "update monitor",
			method: http.MethodPut,
			path: func(t *testing.T, fx serviceMonitorRBACTestFixture) string {
				id := seedServiceMonitorForRBACTest(t, fx.db)
				return fmt.Sprintf("/api/v1/service-monitors/%d", id)
			},
			body: `{"name":"FAKE_SERVICE_MONITOR_FORBIDDEN_UPDATE_FOR_TEST_ONLY","type":"tcp","target":"127.0.0.1:443","interval_seconds":60,"timeout_seconds":10,"enabled":true}`,
		},
		{
			name:   "delete monitor",
			method: http.MethodDelete,
			path: func(t *testing.T, fx serviceMonitorRBACTestFixture) string {
				id := seedServiceMonitorForRBACTest(t, fx.db)
				return fmt.Sprintf("/api/v1/service-monitors/%d", id)
			},
		},
	}

	for _, tc := range writeCases {
		t.Run(tc.name, func(t *testing.T) {
			fx := setupServiceMonitorRBACFixture(t)
			resp := performServiceMonitorRBACRequest(t, fx.router, tc.method, tc.path(t, fx), fx.tokens["viewer"], tc.body)
			if resp.Code != http.StatusForbidden {
				t.Fatalf("expected 403, got %d: %s", resp.Code, resp.Body.String())
			}
		})
	}
}
