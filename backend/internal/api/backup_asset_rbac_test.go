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

type backupAssetRBACTestFixture struct {
	router *gin.Engine
	tokens map[string]string
}

func setupBackupAssetRBACFixture(t *testing.T) backupAssetRBACTestFixture {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	gin.SetMode(gin.TestMode)

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open backup asset RBAC database: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.AuditLog{}); err != nil {
		t.Fatalf("migrate backup asset RBAC database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("open backup asset RBAC SQL database: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	jwtManager := auth.NewJWTManager("FAKE_BACKUP_ASSET_RBAC_JWT_SECRET_FOR_TEST_ONLY", time.Hour)
	tokens := make(map[string]string, 4)
	for _, role := range []string{"admin", "operator", "viewer", "unknown"} {
		user := model.User{
			Username:     "backup-asset-rbac-" + role,
			PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY",
			Role:         role,
		}
		if err := db.Create(&user).Error; err != nil {
			t.Fatalf("create %s backup asset RBAC user: %v", role, err)
		}
		token, err := jwtManager.GenerateToken(user)
		if err != nil {
			t.Fatalf("generate %s backup asset RBAC token: %v", role, err)
		}
		tokens[role] = token
	}

	return backupAssetRBACTestFixture{
		router: NewRouter(Dependencies{DB: db, JWTManager: jwtManager}),
		tokens: tokens,
	}
}

func performBackupAssetRBACRequest(t *testing.T, fixture backupAssetRBACTestFixture, method, path, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	fixture.router.ServeHTTP(response, request)
	return response
}

func TestBackupAssetRBACUsesAuthAndExactRepositoryPermissionsBeforeFeatureGate(t *testing.T) {
	fixture := setupBackupAssetRBACFixture(t)
	const repositoryID = "0123456789abcdef0123456789abcdef"
	routes := []struct {
		name       string
		method     string
		path       string
		body       string
		readAccess bool
	}{
		{name: "connect", method: http.MethodPost, path: "/api/v1/backup-repositories/connect", body: `{"task_id":1}`},
		{name: "list", method: http.MethodGet, path: "/api/v1/backup-repositories", readAccess: true},
		{name: "detail", method: http.MethodGet, path: "/api/v1/backup-repositories/" + repositoryID, readAccess: true},
		{name: "reconcile", method: http.MethodPost, path: "/api/v1/backup-repositories/" + repositoryID + "/reconcile"},
		{name: "disconnect", method: http.MethodPost, path: "/api/v1/backup-repositories/" + repositoryID + "/disconnect"},
	}

	for _, route := range routes {
		t.Run(route.name+"/unauthenticated", func(t *testing.T) {
			response := performBackupAssetRBACRequest(t, fixture, route.method, route.path, route.body, "")
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
		for _, role := range []string{"admin", "operator", "viewer", "unknown"} {
			role := role
			t.Run(route.name+"/"+role, func(t *testing.T) {
				response := performBackupAssetRBACRequest(t, fixture, route.method, route.path, route.body, fixture.tokens[role])
				expected := http.StatusForbidden
				if role == "admin" || (role == "operator" && route.readAccess) {
					expected = http.StatusServiceUnavailable
				}
				if response.Code != expected {
					t.Fatalf("status=%d want=%d body=%s", response.Code, expected, response.Body.String())
				}
				if expected == http.StatusServiceUnavailable && !strings.Contains(response.Body.String(), "feature_disabled") {
					t.Fatalf("authorized request did not reach feature gate: %s", response.Body.String())
				}
			})
		}
	}
}

func TestRsyncVersioningMigrationRoutesRequireAdminBeforeFeatureGate(t *testing.T) {
	fixture := setupBackupAssetRBACFixture(t)
	routes := []struct {
		name string
		path string
		body string
	}{
		{"preflight", "/api/v1/tasks/7/rsync-versioning/preflights", `{"expected_task_revision":1,"requested_mode":"versioned_full_copy"}`},
		{"activate", "/api/v1/tasks/7/rsync-versioning/activate", `{"expected_task_revision":1,"preflight_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","migration_choice":"first_new_point"}`},
		{"rollback", "/api/v1/tasks/7/rsync-versioning/rollback-preparations", `{"expected_task_revision":1}`},
	}
	for _, route := range routes {
		route := route
		t.Run(route.name+"/unauthenticated", func(t *testing.T) {
			response := performBackupAssetRBACRequest(t, fixture, http.MethodPost, route.path, route.body, "")
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
		for _, role := range []string{"admin", "operator", "viewer", "unknown"} {
			role := role
			t.Run(route.name+"/"+role, func(t *testing.T) {
				response := performBackupAssetRBACRequest(t, fixture, http.MethodPost, route.path, route.body, fixture.tokens[role])
				expected := http.StatusForbidden
				if role == "admin" {
					expected = http.StatusServiceUnavailable
				}
				if response.Code != expected {
					t.Fatalf("status=%d want=%d body=%s", response.Code, expected, response.Body.String())
				}
				if expected == http.StatusServiceUnavailable && !strings.Contains(response.Body.String(), "feature_disabled") {
					t.Fatalf("admin request did not reach feature gate: %s", response.Body.String())
				}
			})
		}
	}
}
