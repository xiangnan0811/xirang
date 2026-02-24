package middleware

import (
	"net/http"
	"strings"

	"xirang/backend/internal/auth"

	"github.com/gin-gonic/gin"
)

const (
	CtxUserID   = "userID"
	CtxUsername = "username"
	CtxRole     = "role"
	CtxToken    = "token"
)

func AuthMiddleware(jwtManager *auth.JWTManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		if header == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "缺少 Authorization 头"})
			c.Abort()
			return
		}
		parts := strings.SplitN(header, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Authorization 格式错误"})
			c.Abort()
			return
		}
		claims, err := jwtManager.ParseToken(parts[1])
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "token 无效或过期"})
			c.Abort()
			return
		}
		c.Set(CtxUserID, claims.UserID)
		c.Set(CtxUsername, claims.Username)
		c.Set(CtxRole, claims.Role)
		c.Set(CtxToken, parts[1])
		c.Next()
	}
}

func CurrentRole(c *gin.Context) string {
	role, _ := c.Get(CtxRole)
	value, _ := role.(string)
	return value
}
