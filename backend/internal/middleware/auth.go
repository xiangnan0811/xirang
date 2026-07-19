package middleware

import (
	"net/http"
	"strings"
	"time"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	CtxUserID         = "userID"
	CtxUsername       = "username"
	CtxRole           = "role"
	CtxToken          = "token"
	CtxSessionBinding = "sessionBinding"
)

type SessionBinding struct {
	JTI          string    `json:"-"`
	UserID       uint      `json:"-"`
	Role         string    `json:"-"`
	TokenVersion uint      `json:"-"`
	ExpiresAt    time.Time `json:"-"`
}

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
		if strings.TrimSpace(claims.Purpose) != "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "认证令牌用途不匹配"})
			c.Abort()
			return
		}
		// 校验 token_version：密码修改、角色变更、2FA 禁用后旧 token 自动失效
		if claims.ID == "" || claims.ExpiresAt == nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "token 会话绑定无效"})
			c.Abort()
			return
		}
		if db != nil {
			var user model.User
			if err := db.Select("token_version", "role").First(&user, claims.UserID).Error; err != nil {
				c.JSON(http.StatusUnauthorized, gin.H{"error": "用户不存在或已删除"})
				c.Abort()
				return
			}
			if user.TokenVersion != claims.TokenVersion {
				c.JSON(http.StatusUnauthorized, gin.H{"error": "token 已失效，请重新登录"})
				c.Abort()
				return
			}
			if user.Role != claims.Role {
				c.JSON(http.StatusUnauthorized, gin.H{"error": "用户角色已变更，请重新登录"})
				c.Abort()
				return
			}
		}
		binding := SessionBinding{
			JTI: claims.ID, UserID: claims.UserID, Role: claims.Role,
			TokenVersion: claims.TokenVersion, ExpiresAt: claims.ExpiresAt.UTC(),
		}
		c.Set(CtxUserID, claims.UserID)
		c.Set(CtxUsername, claims.Username)
		c.Set(CtxRole, claims.Role)
		c.Set(CtxSessionBinding, binding)
		c.Next()
	}
}

func CurrentSessionBinding(c *gin.Context) (SessionBinding, bool) {
	value, exists := c.Get(CtxSessionBinding)
	if !exists {
		return SessionBinding{}, false
	}
	binding, ok := value.(SessionBinding)
	return binding, ok
}

func CurrentRole(c *gin.Context) string {
	role, _ := c.Get(CtxRole)
	value, _ := role.(string)
	return value
}
