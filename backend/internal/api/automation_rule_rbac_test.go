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

type automationRuleRBACTestFixture struct {
	db     *gorm.DB
	router *gin.Engine
	tokens map[string]string
}

func setupAutomationRuleRBACFixture(t *testing.T) automationRuleRBACTestFixture {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	gin.SetMode(gin.TestMode)

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.AutomationRule{}, &model.AutomationRuleLog{}, &model.AuditLog{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}

	jwtManager := auth.NewJWTManager("FAKE_AUTOMATION_JWT_SECRET_FOR_TEST_ONLY", time.Hour)
	tokens := make(map[string]string, 3)
	for _, role := range []string{"admin", "operator", "viewer"} {
		user := model.User{
			Username:     "automation-rbac-" + role,
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

	return automationRuleRBACTestFixture{
		db:     db,
		router: router,
		tokens: tokens,
	}
}

func seedAutomationRuleForRBACTest(t *testing.T, db *gorm.DB) uint {
	t.Helper()
	rule := model.AutomationRule{
		Name:         "FAKE_AUTOMATION_RBAC_RULE_FOR_TEST_ONLY",
		EventType:    "backup_failed",
		EventFilter:  "{}",
		ActionType:   "send_notification",
		ActionConfig: "{}",
		Enabled:      true,
	}
	if err := db.Create(&rule).Error; err != nil {
		t.Fatalf("seed automation rule: %v", err)
	}
	return rule.ID
}

func performAutomationRuleRBACRequest(t *testing.T, router *gin.Engine, method, path, token, body string) *httptest.ResponseRecorder {
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

func TestAutomationRuleRoutesRBACAdminAuthorized(t *testing.T) {
	type routeCase struct {
		name   string
		method string
		path   func(*testing.T, automationRuleRBACTestFixture) string
		body   string
		want   int
	}

	cases := []routeCase{
		{
			name:   "list rules",
			method: http.MethodGet,
			path:   func(*testing.T, automationRuleRBACTestFixture) string { return "/api/v1/automation-rules" },
			want:   http.StatusOK,
		},
		{
			name:   "get rule",
			method: http.MethodGet,
			path: func(t *testing.T, fx automationRuleRBACTestFixture) string {
				id := seedAutomationRuleForRBACTest(t, fx.db)
				return fmt.Sprintf("/api/v1/automation-rules/%d", id)
			},
			want: http.StatusOK,
		},
		{
			name:   "create rule",
			method: http.MethodPost,
			path:   func(*testing.T, automationRuleRBACTestFixture) string { return "/api/v1/automation-rules" },
			body:   `{"name":"FAKE_AUTOMATION_RBAC_CREATED_FOR_TEST_ONLY","event_type":"backup_failed","action_type":"send_notification","enabled":true}`,
			want:   http.StatusCreated,
		},
		{
			name:   "update rule",
			method: http.MethodPut,
			path: func(t *testing.T, fx automationRuleRBACTestFixture) string {
				id := seedAutomationRuleForRBACTest(t, fx.db)
				return fmt.Sprintf("/api/v1/automation-rules/%d", id)
			},
			body: `{"name":"FAKE_AUTOMATION_RBAC_UPDATED_FOR_TEST_ONLY","event_type":"node_offline","event_filter":"{\"node_id\":\"1\"}","action_type":"pause_policy","action_config":"{\"policy_id\":\"1\"}","enabled":false}`,
			want: http.StatusOK,
		},
		{
			name:   "delete rule",
			method: http.MethodDelete,
			path: func(t *testing.T, fx automationRuleRBACTestFixture) string {
				id := seedAutomationRuleForRBACTest(t, fx.db)
				return fmt.Sprintf("/api/v1/automation-rules/%d", id)
			},
			want: http.StatusOK,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := setupAutomationRuleRBACFixture(t)
			resp := performAutomationRuleRBACRequest(t, fx.router, tc.method, tc.path(t, fx), fx.tokens["admin"], tc.body)
			if resp.Code != tc.want {
				t.Fatalf("expected %d, got %d: %s", tc.want, resp.Code, resp.Body.String())
			}
		})
	}
}

func TestAutomationRuleRoutesRBACNonAdminForbidden(t *testing.T) {
	type routeCase struct {
		name   string
		method string
		path   func(*testing.T, automationRuleRBACTestFixture) string
		body   string
	}

	cases := []routeCase{
		{
			name:   "list rules",
			method: http.MethodGet,
			path:   func(*testing.T, automationRuleRBACTestFixture) string { return "/api/v1/automation-rules" },
		},
		{
			name:   "get rule",
			method: http.MethodGet,
			path: func(t *testing.T, fx automationRuleRBACTestFixture) string {
				id := seedAutomationRuleForRBACTest(t, fx.db)
				return fmt.Sprintf("/api/v1/automation-rules/%d", id)
			},
		},
		{
			name:   "create rule",
			method: http.MethodPost,
			path:   func(*testing.T, automationRuleRBACTestFixture) string { return "/api/v1/automation-rules" },
			body:   `{"name":"FAKE_AUTOMATION_RBAC_FORBIDDEN_FOR_TEST_ONLY","event_type":"backup_failed","action_type":"send_notification","enabled":true}`,
		},
		{
			name:   "update rule",
			method: http.MethodPut,
			path: func(t *testing.T, fx automationRuleRBACTestFixture) string {
				id := seedAutomationRuleForRBACTest(t, fx.db)
				return fmt.Sprintf("/api/v1/automation-rules/%d", id)
			},
			body: `{"name":"FAKE_AUTOMATION_RBAC_FORBIDDEN_UPDATE_FOR_TEST_ONLY","event_type":"node_offline","action_type":"pause_policy","enabled":false}`,
		},
		{
			name:   "delete rule",
			method: http.MethodDelete,
			path: func(t *testing.T, fx automationRuleRBACTestFixture) string {
				id := seedAutomationRuleForRBACTest(t, fx.db)
				return fmt.Sprintf("/api/v1/automation-rules/%d", id)
			},
		},
	}

	for _, role := range []string{"operator", "viewer"} {
		for _, tc := range cases {
			t.Run(role+" "+tc.name, func(t *testing.T) {
				fx := setupAutomationRuleRBACFixture(t)
				resp := performAutomationRuleRBACRequest(t, fx.router, tc.method, tc.path(t, fx), fx.tokens[role], tc.body)
				if resp.Code != http.StatusForbidden {
					t.Fatalf("expected 403, got %d: %s", resp.Code, resp.Body.String())
				}
			})
		}
	}
}
