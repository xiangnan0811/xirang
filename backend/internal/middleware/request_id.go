package middleware

import (
	"crypto/rand"
	"encoding/hex"

	"github.com/gin-gonic/gin"
)

// RequestIDKey 是 gin.Context 中存储请求 ID 的键名
const RequestIDKey = "request_id"

// RequestID 为每个请求生成唯一 ID 并写入响应头
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := generateRequestID()
		c.Set(RequestIDKey, id)
		c.Header("X-Request-ID", id)
		c.Next()
	}
}

func generateRequestID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
