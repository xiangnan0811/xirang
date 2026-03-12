package middleware

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
)

// ETag computes an ETag from the response body and supports 304 Not Modified.
// Apply to specific low-frequency GET routes only.
func ETag() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method != http.MethodGet {
			c.Next()
			return
		}

		// Buffer the response instead of writing directly
		bw := &bufferedWriter{
			ResponseWriter: c.Writer,
			buf:            &bytes.Buffer{},
		}
		c.Writer = bw
		c.Next()

		status := bw.code
		if status == 0 {
			status = http.StatusOK
		}
		body := bw.buf.Bytes()

		// Non-200: flush as-is
		if status != http.StatusOK {
			bw.ResponseWriter.WriteHeader(status)
			if len(body) > 0 {
				bw.ResponseWriter.Write(body)
			}
			return
		}

		// Compute ETag
		hash := sha256.Sum256(body)
		etag := fmt.Sprintf(`"%x"`, hash[:8])
		bw.ResponseWriter.Header().Set("ETag", etag)

		// Check If-None-Match BEFORE writing body
		if c.Request.Header.Get("If-None-Match") == etag {
			bw.ResponseWriter.WriteHeader(http.StatusNotModified)
			return
		}

		// Send full response with ETag
		bw.ResponseWriter.WriteHeader(status)
		bw.ResponseWriter.Write(body)
	}
}

// bufferedWriter captures the response body and status without writing to the client.
type bufferedWriter struct {
	gin.ResponseWriter
	buf  *bytes.Buffer
	code int
}

func (w *bufferedWriter) Write(b []byte) (int, error) {
	return w.buf.Write(b)
}

func (w *bufferedWriter) WriteString(s string) (int, error) {
	return w.buf.WriteString(s)
}

func (w *bufferedWriter) WriteHeader(code int) {
	w.code = code
}

func (w *bufferedWriter) WriteHeaderNow() {}

func (w *bufferedWriter) Written() bool {
	return false
}

func (w *bufferedWriter) Status() int {
	if w.code == 0 {
		return http.StatusOK
	}
	return w.code
}

func (w *bufferedWriter) Size() int {
	return w.buf.Len()
}
