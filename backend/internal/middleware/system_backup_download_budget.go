package middleware

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const systemBackupDownloadBudgetKey = "systemBackupDownloadBudget"

// SystemBackupDownloadBudgetState belongs to one request and its synchronous
// middleware chain. Cancellations remain live until the entire chain returns.
type SystemBackupDownloadBudgetState struct {
	client          context.Context
	total           context.Context
	prepare         context.Context
	absolute        time.Time
	prepareDeadline time.Time
	transferring    bool
	cancels         []context.CancelFunc
}

// SystemBackupDownloadBudget installs transport protection before authentication.
// The private constructor also lets transport tests use short, real deadlines.
func SystemBackupDownloadBudget() gin.HandlerFunc {
	return systemBackupDownloadBudget(10*time.Minute, 120*time.Second)
}

func systemBackupDownloadBudget(total, prepare time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !isSystemBackupDownloadRequest(c.Request) {
			c.Next()
			return
		}
		started := time.Now()
		state := &SystemBackupDownloadBudgetState{
			client:          c.Request.Context(),
			absolute:        started.Add(total),
			prepareDeadline: started.Add(prepare),
		}
		if state.absolute.Before(state.prepareDeadline) {
			state.prepareDeadline = state.absolute
		}
		state.bindContexts(c)
		defer func() {
			for _, cancel := range state.cancels {
				cancel()
			}
		}()
		c.Set(systemBackupDownloadBudgetKey, state)
		if HandleSystemBackupDownloadBudgetFailure(c) {
			return
		}
		if err := systemBackupDownloadResponseController(c).SetWriteDeadline(state.absolute); err != nil {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, apiResponse{
				Code: http.StatusServiceUnavailable, Message: "备份下载传输不可用",
				Data: gin.H{"error_code": "BACKUP_TRANSPORT_UNAVAILABLE"},
			})
			return
		}
		c.Next()
	}
}

func isSystemBackupDownloadRequest(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	parts := strings.Split(r.URL.Path, "/")
	return len(parts) == 8 && parts[0] == "" && parts[1] == "api" && parts[2] == "v1" &&
		parts[3] == "system" && parts[4] == "backups" && parts[5] != "" && parts[6] != "" && parts[7] == "download"
}

func SystemBackupDownloadBudgetFromContext(c *gin.Context) (*SystemBackupDownloadBudgetState, bool) {
	value, exists := c.Get(systemBackupDownloadBudgetKey)
	state, ok := value.(*SystemBackupDownloadBudgetState)
	return state, exists && ok && state != nil
}

func (s *SystemBackupDownloadBudgetState) bindContexts(c *gin.Context) {
	total, cancelTotal := context.WithDeadline(s.client, s.absolute)
	prepare, cancelPrepare := context.WithDeadline(total, s.prepareDeadline)
	s.cancels = append(s.cancels, cancelTotal, cancelPrepare)
	s.total, s.prepare = total, prepare
	if s.transferring {
		c.Request = c.Request.WithContext(total)
	} else {
		c.Request = c.Request.WithContext(prepare)
	}
}

// LimitDeadline can only tighten the session/grant lease, never extend it.
func (s *SystemBackupDownloadBudgetState) LimitDeadline(c *gin.Context, expiresAt time.Time) error {
	if !expiresAt.Before(s.absolute) {
		return nil
	}
	s.absolute = expiresAt
	if expiresAt.Before(s.prepareDeadline) {
		s.prepareDeadline = expiresAt
	}
	s.bindContexts(c)
	return systemBackupDownloadResponseController(c).SetWriteDeadline(s.absolute)
}

func (s *SystemBackupDownloadBudgetState) BeginTransfer(c *gin.Context) bool {
	if HandleSystemBackupDownloadBudgetFailure(c) {
		return false
	}
	s.transferring = true
	c.Request = c.Request.WithContext(s.total)
	return true
}

func (s *SystemBackupDownloadBudgetState) AbsoluteDeadline() time.Time {
	return s.absolute
}

// HandleSystemBackupDownloadBudgetFailure must precede any consumer's error
// response. It deliberately writes its own envelope, without response helpers.
func HandleSystemBackupDownloadBudgetFailure(c *gin.Context) bool {
	s, ok := SystemBackupDownloadBudgetFromContext(c)
	if !ok {
		return false
	}
	if s.client.Err() != nil {
		c.Abort()
		return true
	}
	now := time.Now()
	if !now.Before(s.absolute) {
		panic(http.ErrAbortHandler)
	}
	if !s.transferring && (s.prepare.Err() != nil || !now.Before(s.prepareDeadline)) {
		if c.Writer.Written() {
			panic(http.ErrAbortHandler)
		}
		deadline := now.Add(5 * time.Second)
		if s.absolute.Before(deadline) {
			deadline = s.absolute
		}
		if err := systemBackupDownloadResponseController(c).SetWriteDeadline(deadline); err != nil {
			panic(http.ErrAbortHandler)
		}
		c.AbortWithStatusJSON(http.StatusGatewayTimeout, apiResponse{
			Code: http.StatusGatewayTimeout, Message: "备份下载准备超时",
			Data: gin.H{"error_code": "BACKUP_PREPARE_TIMEOUT"},
		})
		return true
	}
	return false
}

// Gin's Flush discards errors. Strip explicit wrappers before using the
// transport controller; this also avoids accidentally selecting Gin's Flush.
func systemBackupDownloadResponseController(c *gin.Context) *http.ResponseController {
	var writer http.ResponseWriter = c.Writer
	for {
		unwrapper, ok := writer.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			break
		}
		writer = unwrapper.Unwrap()
	}
	return http.NewResponseController(writer)
}
