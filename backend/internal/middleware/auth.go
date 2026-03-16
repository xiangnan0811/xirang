package middleware

import (
	"net/http"
	"strings"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	CtxUserID   = "userID"
	CtxUsername = "username"
	CtxRole     = "role"
	CtxToken    = "token"
)

func AuthMiddleware(jwtManager *auth.JWTManager, db *gorm.DB) gin.HandlerFunc {
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
		if claims.Purpose == "2fa_pending" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "需要完成两步验证"})
			c.Abort()
			return
		}
		// 校验 token_version：密码修改、角色变更、2FA 禁用后旧 token 自动失效
		if db != nil {
			var user model.User
			if err := db.Select("token_version").First(&user, claims.UserID).Error; err != nil {
				c.JSON(http.StatusUnauthorized, gin.H{"error": "用户不存在或已删除"})
				c.Abort()
				return
			}
			if user.TokenVersion != claims.TokenVersion {
				c.JSON(http.StatusUnauthorized, gin.H{"error": "token 已失效，请重新登录"})
				c.Abort()
				return
			}
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
