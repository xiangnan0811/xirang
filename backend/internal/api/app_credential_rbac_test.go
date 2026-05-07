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

type appCredentialRBACTestFixture struct {
	db     *gorm.DB
	router *gin.Engine
	tokens map[string]string
}

func setupAppCredentialRBACFixture(t *testing.T) appCredentialRBACTestFixture {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	gin.SetMode(gin.TestMode)

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.AppCredential{}, &model.Policy{}, &model.AuditLog{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}

	jwtManager := auth.NewJWTManager("FAKE_JWT_SECRET_FOR_TEST_ONLY", time.Hour)
	tokens := make(map[string]string, 3)
	for _, role := range []string{"admin", "operator", "viewer"} {
		user := model.User{
			Username:     "rbac-" + role,
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

	return appCredentialRBACTestFixture{
		db:     db,
		router: router,
		tokens: tokens,
	}
}

func seedAppCredentialForRBACTest(t *testing.T, db *gorm.DB) uint {
	t.Helper()
	credential := model.AppCredential{
		Name:   "rbac-mysql",
		Type:   "mysql",
		Config: `{"host":"127.0.0.1","password":"FAKE_RBAC_PASSWORD_FOR_TEST_ONLY"}`,
	}
	if err := db.Create(&credential).Error; err != nil {
		t.Fatalf("seed app credential: %v", err)
	}
	return credential.ID
}

func performAppCredentialRBACRequest(t *testing.T, router *gin.Engine, method, path, token, body string) *httptest.ResponseRecorder {
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

func TestAppCredentialRoutesRBACAdminAuthorized(t *testing.T) {
	type routeCase struct {
		name   string
		method string
		path   func(*testing.T, appCredentialRBACTestFixture) string
		body   string
		want   int
	}

	cases := []routeCase{
		{
			name:   "list profiles",
			method: http.MethodGet,
			path:   func(*testing.T, appCredentialRBACTestFixture) string { return "/api/v1/app-credentials/profiles" },
			want:   http.StatusOK,
		},
		{
			name:   "list saved credentials",
			method: http.MethodGet,
			path: func(t *testing.T, fx appCredentialRBACTestFixture) string {
				seedAppCredentialForRBACTest(t, fx.db)
				return "/api/v1/app-credentials"
			},
			want: http.StatusOK,
		},
		{
			name:   "get saved credential",
			method: http.MethodGet,
			path: func(t *testing.T, fx appCredentialRBACTestFixture) string {
				id := seedAppCredentialForRBACTest(t, fx.db)
				return fmt.Sprintf("/api/v1/app-credentials/%d", id)
			},
			want: http.StatusOK,
		},
		{
			name:   "create saved credential",
			method: http.MethodPost,
			path:   func(*testing.T, appCredentialRBACTestFixture) string { return "/api/v1/app-credentials" },
			body:   `{"type":"mysql","name":"rbac-created","host":"127.0.0.1","user":"root","password":"FAKE_RBAC_CREATED_PASSWORD_FOR_TEST_ONLY"}`,
			want:   http.StatusCreated,
		},
		{
			name:   "update saved credential",
			method: http.MethodPut,
			path: func(t *testing.T, fx appCredentialRBACTestFixture) string {
				id := seedAppCredentialForRBACTest(t, fx.db)
				return fmt.Sprintf("/api/v1/app-credentials/%d", id)
			},
			body: `{"type":"mysql","name":"rbac-updated","host":"127.0.0.2","user":"root","password":"FAKE_RBAC_UPDATED_PASSWORD_FOR_TEST_ONLY"}`,
			want: http.StatusOK,
		},
		{
			name:   "delete saved credential",
			method: http.MethodDelete,
			path: func(t *testing.T, fx appCredentialRBACTestFixture) string {
				id := seedAppCredentialForRBACTest(t, fx.db)
				return fmt.Sprintf("/api/v1/app-credentials/%d", id)
			},
			want: http.StatusOK,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := setupAppCredentialRBACFixture(t)
			resp := performAppCredentialRBACRequest(t, fx.router, tc.method, tc.path(t, fx), fx.tokens["admin"], tc.body)
			if resp.Code != tc.want {
				t.Fatalf("expected %d, got %d: %s", tc.want, resp.Code, resp.Body.String())
			}
		})
	}
}

func TestAppCredentialRoutesRBACNonAdminForbidden(t *testing.T) {
	type routeCase struct {
		name   string
		method string
		path   func(*testing.T, appCredentialRBACTestFixture) string
		body   string
	}

	cases := []routeCase{
		{
			name:   "list profiles",
			method: http.MethodGet,
			path:   func(*testing.T, appCredentialRBACTestFixture) string { return "/api/v1/app-credentials/profiles" },
		},
		{
			name:   "list saved credentials",
			method: http.MethodGet,
			path: func(t *testing.T, fx appCredentialRBACTestFixture) string {
				seedAppCredentialForRBACTest(t, fx.db)
				return "/api/v1/app-credentials"
			},
		},
		{
			name:   "get saved credential",
			method: http.MethodGet,
			path: func(t *testing.T, fx appCredentialRBACTestFixture) string {
				id := seedAppCredentialForRBACTest(t, fx.db)
				return fmt.Sprintf("/api/v1/app-credentials/%d", id)
			},
		},
		{
			name:   "create saved credential",
			method: http.MethodPost,
			path:   func(*testing.T, appCredentialRBACTestFixture) string { return "/api/v1/app-credentials" },
			body:   `{"type":"mysql","name":"rbac-forbidden","host":"127.0.0.1","user":"root","password":"FAKE_RBAC_FORBIDDEN_PASSWORD_FOR_TEST_ONLY"}`,
		},
		{
			name:   "update saved credential",
			method: http.MethodPut,
			path: func(t *testing.T, fx appCredentialRBACTestFixture) string {
				id := seedAppCredentialForRBACTest(t, fx.db)
				return fmt.Sprintf("/api/v1/app-credentials/%d", id)
			},
			body: `{"type":"mysql","name":"rbac-forbidden-update","host":"127.0.0.2","user":"root","password":"FAKE_RBAC_FORBIDDEN_UPDATE_PASSWORD_FOR_TEST_ONLY"}`,
		},
		{
			name:   "delete saved credential",
			method: http.MethodDelete,
			path: func(t *testing.T, fx appCredentialRBACTestFixture) string {
				id := seedAppCredentialForRBACTest(t, fx.db)
				return fmt.Sprintf("/api/v1/app-credentials/%d", id)
			},
		},
	}

	for _, role := range []string{"operator", "viewer"} {
		for _, tc := range cases {
			t.Run(role+" "+tc.name, func(t *testing.T) {
				fx := setupAppCredentialRBACFixture(t)
				resp := performAppCredentialRBACRequest(t, fx.router, tc.method, tc.path(t, fx), fx.tokens[role], tc.body)
				if resp.Code != http.StatusForbidden {
					t.Fatalf("expected 403, got %d: %s", resp.Code, resp.Body.String())
				}
			})
		}
	}
}
