package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

var rolePermissions = map[string]map[string]bool{
	"admin": {
		"nodes:read":         true,
		"nodes:write":        true,
		"nodes:test":         true,
		"policies:read":      true,
		"policies:write":     true,
		"tasks:read":         true,
		"tasks:write":        true,
		"tasks:trigger":      true,
		"ssh_keys:read":      true,
		"ssh_keys:write":     true,
		"integrations:read":  true,
		"integrations:write": true,
		"alerts:read":        true,
		"alerts:deliveries":  true,
		"alerts:write":       true,
		"audit:read":         true,
		"users:manage":       true,
	},
	"operator": {
		"nodes:read":        true,
		"nodes:test":        true,
		"policies:read":     true,
		"tasks:read":        true,
		"tasks:write":       true,
		"tasks:trigger":     true,
		"ssh_keys:read":     true,
		"integrations:read": true,
		"alerts:read":       true,
		"alerts:deliveries": true,
		"alerts:write":      true,
	},
	"viewer": {
		"nodes:read":        true,
		"policies:read":     true,
		"tasks:read":        true,
		"ssh_keys:read":     true,
		"integrations:read": true,
		"alerts:read":       true,
		"alerts:deliveries": true,
	},
}

// HasPermission checks if a role has a specific permission.
func HasPermission(role, permission string) bool {
	permissions, ok := rolePermissions[role]
	return ok && permissions[permission]
}

// RequireRole 校验当前用户是否具有指定角色（而非权限），用于只允许特定角色的操作。
func RequireRole(role string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if CurrentRole(c) != role {
			c.JSON(http.StatusForbidden, gin.H{"error": "权限不足"})
			c.Abort()
			return
		}
		c.Next()
	}
}

func RBAC(permission string) gin.HandlerFunc {
	return func(c *gin.Context) {
		role := CurrentRole(c)
		permissions, ok := rolePermissions[role]
		if !ok || !permissions[permission] {
			c.JSON(http.StatusForbidden, gin.H{"error": "权限不足"})
			c.Abort()
			return
		}
		c.Next()
	}
}
