package middleware

import (
	"net/http"

	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func AuditLogger(db *gorm.DB) gin.HandlerFunc {
	if db == nil {
		return func(c *gin.Context) {
			c.Next()
		}
	}

	return func(c *gin.Context) {
		skip := c.Request.Method == http.MethodGet || c.Request.Method == http.MethodHead || c.Request.Method == http.MethodOptions
		if skip {
			c.Next()
			return
		}

		path := c.FullPath()
		if path == "" {
			path = c.Request.URL.Path
		}

		c.Next()

		record := model.AuditLog{
			UserID:     extractUserIDFromContext(c),
			Username:   c.GetString(CtxUsername),
			Role:       c.GetString(CtxRole),
			Method:     c.Request.Method,
			Path:       path,
			StatusCode: c.Writer.Status(),
			ClientIP:   c.ClientIP(),
			UserAgent:  c.Request.UserAgent(),
		}
		_ = db.Create(&record).Error
	}
}

func extractUserID(raw interface{}) uint {
	switch value := raw.(type) {
	case uint:
		return value
	case uint64:
		return uint(value)
	case int:
		if value < 0 {
			return 0
		}
		return uint(value)
	default:
		return 0
	}
}

func extractUserIDFromContext(c *gin.Context) uint {
	raw, _ := c.Get(CtxUserID)
	return extractUserID(raw)
}
