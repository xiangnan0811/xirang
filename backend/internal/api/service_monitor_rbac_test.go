package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type serviceMonitorRBACTestFixture struct {
	db     *gorm.DB
	router *gin.Engine
	tokens map[string]string
}

func setupServiceMonitorRBACFixture(t *testing.T) serviceMonitorRBACTestFixture {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	gin.SetMode(gin.TestMode)

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
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
		db:     db,
		router: router,
		tokens: tokens,
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
