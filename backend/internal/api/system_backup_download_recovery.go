package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"xirang/backend/internal/logger"
	"xirang/backend/internal/middleware"

	"github.com/gin-gonic/gin"
)

// systemBackupDownloadRecovery leaves ordinary Gin recovery unchanged, but must
// never let it consume a streaming abort or dump a request carrying step-up proof.
func systemBackupDownloadRecovery() gin.HandlerFunc {
	ordinary := gin.Recovery()
	return func(c *gin.Context) {
		path := c.Request.URL.Path
		if path != "/api/v1/system/backups" && !strings.HasPrefix(path, "/api/v1/system/backups/") &&
			path != "/api/v1/credential-access-grants/system-backup-download" {
			ordinary(c)
			return
		}

		c.Header("Cache-Control", "private, no-store")
		// The budget middleware cancels its derived contexts while unwinding.
		// Keep the original client context so that cancellation is not mistaken
		// for a disconnected client when recovering an otherwise writable panic.
		client := c.Request.Context()
		defer func() {
			value := recover()
			if value == nil {
				return
			}
			c.Abort()
			if err, ok := value.(error); ok && errors.Is(err, http.ErrAbortHandler) {
				panic(http.ErrAbortHandler)
			}

			logger.Module("http_system_backup").Error().
				Str("event", "system_backup_request_panic").
				Str("request_id", c.GetString(middleware.RequestIDKey)).
				Msg("system backup request panic recovered")
			if c.Writer.Written() || client.Err() != nil {
				panic(http.ErrAbortHandler)
			}

			deadline := time.Now().Add(5 * time.Second)
			if budget, ok := middleware.SystemBackupDownloadBudgetFromContext(c); ok {
				absolute := budget.AbsoluteDeadline()
				if !time.Now().Before(absolute) {
					panic(http.ErrAbortHandler)
				}
				if absolute.Before(deadline) {
					deadline = absolute
				}
			}
			controller := systemBackupRecoveryResponseController(c.Writer)
			if err := controller.SetWriteDeadline(deadline); err != nil {
				panic(http.ErrAbortHandler)
			}
			// No stale attachment/length/encoding metadata may describe this JSON.
			header := c.Writer.Header()
			for _, name := range []string{"Content-Length", "Content-Encoding", "Content-Disposition", "ETag"} {
				header.Del(name)
			}
			header.Set("Cache-Control", "private, no-store")
			header.Set("Content-Type", "application/json; charset=utf-8")
			c.Status(http.StatusInternalServerError)
			if _, err := c.Writer.Write([]byte(`{"code":500,"message":"服务器内部错误","data":null}`)); err != nil {
				panic(http.ErrAbortHandler)
			}
			if err := controller.Flush(); err != nil {
				panic(http.ErrAbortHandler)
			}
		}()
		c.Next()
	}
}

// Gin's Flush discards transport errors. Explicitly unwrap it (and any explicit
// transport wrapper) before asking ResponseController for an observable flush.
func systemBackupRecoveryResponseController(writer http.ResponseWriter) *http.ResponseController {
	for {
		unwrapper, ok := writer.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return http.NewResponseController(writer)
		}
		writer = unwrapper.Unwrap()
	}
}
