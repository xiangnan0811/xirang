package middleware

import (
	"net/http"
	"strings"

	"xirang/backend/internal/logger"

	"github.com/gin-gonic/gin"
)

const BackupContentSafeRoute = "/api/v1/asset-content/:deliveryId"

func IsBackupContentShapedPath(path string) bool {
	return path == "/api/v1/asset-content" || strings.HasPrefix(path, "/api/v1/asset-content/")
}

// ContentSafeRecovery catches content-local panics before Gin's outer recovery
// can dump a request carrying the delivery path or cookie. Panic values and
// request metadata are deliberately excluded from the log event.
func ContentSafeRecovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if recover() == nil {
				return
			}
			setContentRecoveryHeaders(c.Writer.Header())
			c.Abort()
			if !c.Writer.Written() {
				c.Status(http.StatusInternalServerError)
			}
			event := logger.Module("http_content").Error().Str("category", "content_panic")
			if requestID, exists := c.Get(RequestIDKey); exists {
				if value, ok := requestID.(string); ok && value != "" {
					event.Str("request_id", value)
				}
			}
			event.Msg("content request panic recovered")
		}()
		c.Next()
	}
}

func setContentRecoveryHeaders(header http.Header) {
	for _, name := range []string{
		"Access-Control-Allow-Origin", "Access-Control-Allow-Credentials", "Access-Control-Allow-Headers", "Access-Control-Allow-Methods",
	} {
		header.Del(name)
	}
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Cross-Origin-Resource-Policy", "same-origin")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("Content-Security-Policy", "sandbox; default-src 'none'; frame-ancestors 'self'; object-src 'none'")
	header.Set("X-Frame-Options", "SAMEORIGIN")
	header.Set("Cache-Control", "private, no-store")
}
