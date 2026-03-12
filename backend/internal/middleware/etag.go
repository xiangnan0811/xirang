package middleware

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
)

// ETag wraps the response writer to capture response body for ETag computation.
// Apply to specific low-frequency routes only.
func ETag() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Only apply to GET requests
		if c.Request.Method != http.MethodGet {
			c.Next()
			return
		}

		// Capture response
		w := &etagResponseWriter{ResponseWriter: c.Writer, body: &bytes.Buffer{}}
		c.Writer = w
		c.Next()

		// Only process 200 responses
		if w.Status() != http.StatusOK {
			return
		}

		// Compute ETag from response body
		hash := sha256.Sum256(w.body.Bytes())
		etag := fmt.Sprintf(`"%x"`, hash[:8])

		c.Header("ETag", etag)

		// Check If-None-Match
		ifNoneMatch := c.Request.Header.Get("If-None-Match")
		if ifNoneMatch == etag {
			// Reset writer and send 304
			c.Status(http.StatusNotModified)
		}
	}
}

type etagResponseWriter struct {
	gin.ResponseWriter
	body   *bytes.Buffer
	status int
}

func (w *etagResponseWriter) Write(b []byte) (int, error) {
	w.body.Write(b)
	return w.ResponseWriter.Write(b)
}

func (w *etagResponseWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *etagResponseWriter) Status() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}
