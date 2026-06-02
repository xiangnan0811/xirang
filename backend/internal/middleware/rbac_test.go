package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// =============================================================================
// RBAC 中间件测试
// =============================================================================

// setupRBACHandler 创建带 RBAC 中间件的路由
func setupRBACHandler(permission string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	// 注入 AuthMiddleware 设置上下文，然后走 RBAC
	r.Use(func(c *gin.Context) {
		// 模拟 AuthMiddleware 设置的上下文值
		c.Set(CtxUserID, uint(1))
		c.Set(CtxUsername, "testuser")
		c.Set(CtxRole, c.GetHeader("X-Test-Role"))
		c.Next()
	})
	r.Use(RBAC(permission))
	r.GET("/test-rbac", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	return r
}

func rbacPerformRequest(r *gin.Engine, role string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/test-rbac", nil)
	req.Header.Set("X-Test-Role", role)
	r.ServeHTTP(w, req)
	return w
}

func TestRBAC_AdminHasAllWritePermissions(t *testing.T) {
	r := setupRBACHandler("nodes:write")
	w := rbacPerformRequest(r, "admin")
	if w.Code != http.StatusOK {
		t.Fatalf("admin 应能访问 nodes:write，期望 200 实际 %d", w.Code)
	}
}

func TestRBAC_ViewerCannotWrite(t *testing.T) {
	r := setupRBACHandler("nodes:write")
	w := rbacPerformRequest(r, "viewer")
	if w.Code != http.StatusForbidden {
		t.Fatalf("viewer 不应能访问 nodes:write，期望 403 实际 %d", w.Code)
	}
}

func TestRBAC_OperatorCanTriggerTask(t *testing.T) {
	r := setupRBACHandler("tasks:trigger")
	w := rbacPerformRequest(r, "operator")
	if w.Code != http.StatusOK {
		t.Fatalf("operator 应能访问 tasks:trigger，期望 200 实际 %d", w.Code)
	}
}

func TestRBAC_OperatorCannotManageUsers(t *testing.T) {
	r := setupRBACHandler("users:manage")
	w := rbacPerformRequest(r, "operator")
	if w.Code != http.StatusForbidden {
		t.Fatalf("operator 不应能访问 users:manage，期望 403 实际 %d", w.Code)
	}
}

func TestRBAC_ViewerHasReadOnlyPermissions(t *testing.T) {
	readPerms := []string{
		"nodes:read", "policies:read", "tasks:read",
		"ssh_keys:read", "integrations:read", "alerts:read",
		"alerts:deliveries", "reports:read", "logs:read",
		"dashboards:read", "escalation:read", "service_monitors:read",
	}
	for _, perm := range readPerms {
		r := setupRBACHandler(perm)
		w := rbacPerformRequest(r, "viewer")
		if w.Code != http.StatusOK {
			t.Errorf("viewer 应能访问 %s，期望 200 实际 %d", perm, w.Code)
		}
	}
}

func TestRBAC_UnknownRoleIsForbidden(t *testing.T) {
	r := setupRBACHandler("nodes:read")
	w := rbacPerformRequest(r, "unknown_role")
	if w.Code != http.StatusForbidden {
		t.Fatalf("未知角色应被拒绝，期望 403 实际 %d", w.Code)
	}
}

func TestRBAC_NoRoleIsForbidden(t *testing.T) {
	r := setupRBACHandler("nodes:read")
	w := rbacPerformRequest(r, "") // 空角色
	if w.Code != http.StatusForbidden {
		t.Fatalf("空角色应被拒绝，期望 403 实际 %d", w.Code)
	}
}

// =============================================================================
// RequireRole 中间件测试
// =============================================================================

func setupRequireRoleHandler(requiredRole string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(CtxRole, c.GetHeader("X-Test-Role"))
		c.Next()
	})
	r.Use(RequireRole(requiredRole))
	r.GET("/test-require-role", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	return r
}

func requireRolePerformRequest(r *gin.Engine, role string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/test-require-role", nil)
	req.Header.Set("X-Test-Role", role)
	r.ServeHTTP(w, req)
	return w
}

func TestRequireRole_AdminCanAccessAdminOnly(t *testing.T) {
	r := setupRequireRoleHandler("admin")
	w := requireRolePerformRequest(r, "admin")
	if w.Code != http.StatusOK {
		t.Fatalf("admin 角色应能通过 RequireRole(admin)，期望 200 实际 %d", w.Code)
	}
}

func TestRequireRole_OperatorCannotAccessAdminOnly(t *testing.T) {
	r := setupRequireRoleHandler("admin")
	w := requireRolePerformRequest(r, "operator")
	if w.Code != http.StatusForbidden {
		t.Fatalf("operator 不应通过 RequireRole(admin)，期望 403 实际 %d", w.Code)
	}
}

func TestRequireRole_ViewerCannotAccessAdminOnly(t *testing.T) {
	r := setupRequireRoleHandler("admin")
	w := requireRolePerformRequest(r, "viewer")
	if w.Code != http.StatusForbidden {
		t.Fatalf("viewer 不应通过 RequireRole(admin)，期望 403 实际 %d", w.Code)
	}
}

func TestRequireRole_NoRoleCannotAccessAdminOnly(t *testing.T) {
	r := setupRequireRoleHandler("admin")
	w := requireRolePerformRequest(r, "") // 无角色上下文
	if w.Code != http.StatusForbidden {
		t.Fatalf("无角色不应通过 RequireRole(admin)，期望 403 实际 %d", w.Code)
	}
}

// =============================================================================
// HasPermission 函数测试
// =============================================================================

func TestHasPermission_AdminHasAllPermissions(t *testing.T) {
	allPerms := []string{
		"nodes:read", "nodes:write", "nodes:test", "nodes:owners",
		"policies:read", "policies:write",
		"tasks:read", "tasks:write", "tasks:trigger",
		"ssh_keys:read", "ssh_keys:write",
		"integrations:read", "integrations:write",
		"app_credentials:read", "app_credentials:write",
		"alerts:read", "alerts:deliveries", "alerts:write",
		"audit:read", "users:manage",
		"reports:read", "reports:write",
		"logs:read", "logs:write",
		"dashboards:read", "dashboards:write",
		"escalation:read", "escalation:write",
		"service_monitors:read", "service_monitors:write",
		"automation:read", "automation:write",
	}
	for _, perm := range allPerms {
		if !HasPermission("admin", perm) {
			t.Errorf("admin 应有权限 %s", perm)
		}
	}
}

func TestHasPermission_ViewerCannotWrite(t *testing.T) {
	if HasPermission("viewer", "nodes:write") {
		t.Error("viewer 不应有 nodes:write 权限")
	}
	if HasPermission("viewer", "users:manage") {
		t.Error("viewer 不应有 users:manage 权限")
	}
}

func TestHasPermission_UnknownRole(t *testing.T) {
	if HasPermission("nonexistent", "nodes:read") {
		t.Error("不存在的角色不应有任何权限")
	}
}

func TestHasPermission_UnknownPermission(t *testing.T) {
	if HasPermission("admin", "nonexistent:perm") {
		t.Error("admin 不应有不存在权限的定义")
	}
}

// =============================================================================
// RBAC error response body 测试
// =============================================================================

func TestRBAC_ErrorResponseBody(t *testing.T) {
	r := setupRBACHandler("nodes:write")
	w := rbacPerformRequest(r, "viewer")

	if w.Code != http.StatusForbidden {
		t.Fatalf("期望 403，实际 %d", w.Code)
	}

	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	msg, ok := body["error"].(string)
	if !ok || msg != "权限不足" {
		t.Errorf("期望错误信息 '权限不足'，实际 %v", body["error"])
	}
}

func TestRequireRole_ErrorResponseBody(t *testing.T) {
	r := setupRequireRoleHandler("admin")
	w := requireRolePerformRequest(r, "viewer")

	if w.Code != http.StatusForbidden {
		t.Fatalf("期望 403，实际 %d", w.Code)
	}

	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	msg, ok := body["error"].(string)
	if !ok || msg != "权限不足" {
		t.Errorf("期望错误信息 '权限不足'，实际 %v", body["error"])
	}
}