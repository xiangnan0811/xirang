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

type taskStatisticsRouterRBACFixture struct {
	router *gin.Engine
	tokens map[string]string
}

func setupTaskStatisticsRouterRBACFixture(t *testing.T) taskStatisticsRouterRBACFixture {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	gin.SetMode(gin.TestMode)

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(
		&model.User{}, &model.TokenRevocation{}, &model.AuditLog{}, &model.NodeOwner{},
		&model.Task{}, &model.TaskRun{}, &model.TaskTrafficSample{},
	); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}

	jwtManager := auth.NewJWTManager("FAKE_TASK_STATS_RBAC_JWT_SECRET_FOR_TEST_ONLY", time.Hour)
	jwtManager.SetDB(db)
	tokens := make(map[string]string, 4)
	for _, role := range []string{"admin", "operator", "viewer", "guest"} {
		user := model.User{
			Username:     "task-statistics-rbac-" + role,
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

	return taskStatisticsRouterRBACFixture{
		router: NewRouter(Dependencies{DB: db, JWTManager: jwtManager}),
		tokens: tokens,
	}
}

func performTaskStatisticsRouterRequest(t *testing.T, router *gin.Engine, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/tasks/statistics/query", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func TestTaskStatisticsRouteUsesTasksReadAuthorization(t *testing.T) {
	fixture := setupTaskStatisticsRouterRBACFixture(t)
	base := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	body := fmt.Sprintf(`{"metric":"task.success_rate","filters":{},"aggregation":"avg","start":"%s","end":"%s"}`,
		base.Format(time.RFC3339), base.Add(time.Hour).Format(time.RFC3339))

	if response := performTaskStatisticsRouterRequest(t, fixture.router, "", body); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d body=%s", response.Code, response.Body.String())
	}
	for _, role := range []string{"admin", "operator", "viewer"} {
		response := performTaskStatisticsRouterRequest(t, fixture.router, fixture.tokens[role], body)
		if response.Code != http.StatusOK {
			t.Fatalf("role=%s status=%d body=%s", role, response.Code, response.Body.String())
		}
	}
	if response := performTaskStatisticsRouterRequest(t, fixture.router, fixture.tokens["guest"], body); response.Code != http.StatusForbidden {
		t.Fatalf("guest status=%d body=%s", response.Code, response.Body.String())
	}
}
