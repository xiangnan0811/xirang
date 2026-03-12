package middleware

import (
	"time"

	"xirang/backend/internal/logger"

	"github.com/gin-gonic/gin"
)

// StructuredLogger 替代 gin.Logger()，输出结构化 JSON 日志
func StructuredLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path

		c.Next()

		latency := time.Since(start)
		status := c.Writer.Status()

		event := logger.Module("http").Info()
		event.Str("method", c.Request.Method).
			Str("path", path).
			Int("status", status).
			Int64("latency_ms", latency.Milliseconds()).
			Str("client_ip", c.ClientIP())

		if reqID, exists := c.Get(RequestIDKey); exists {
			event.Str("request_id", reqID.(string))
		}

		// 认证上下文中的用户 ID（如有）
		if userID, exists := c.Get("userID"); exists {
			event.Interface("user_id", userID)
		}

		event.Msg("")
	}
}
