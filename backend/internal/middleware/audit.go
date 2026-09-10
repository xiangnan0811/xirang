package middleware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"time"

	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const auditWriteTimeout = 5 * time.Second

// auditWriteLock serializes hash-chain writers while allowing a caller's
// context to cancel both queueing and the database work that follows.
type auditWriteLock struct {
	sem chan struct{}
}

func newAuditWriteLock() *auditWriteLock {
	return &auditWriteLock{sem: make(chan struct{}, 1)}
}

func (lock *auditWriteLock) Lock() {
	_ = lock.LockContext(context.Background())
}

func (lock *auditWriteLock) LockContext(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case lock.sem <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-lock.sem
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (lock *auditWriteLock) Unlock() {
	<-lock.sem
}

var auditWriteMu = newAuditWriteLock()

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

		userIDVal, exists := c.Get(CtxUserID)
		if !exists || userIDVal == nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "audit log missing user context"})
			return
		}

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
		record.CreatedAt = time.Now().UTC()
		auditDB := db
		if c.Request != nil {
			auditDB = db.WithContext(c.Request.Context())
		}
		if err := SaveAuditLogWithHashChain(auditDB, &record); err != nil {
			logger.Module("audit").Warn().Err(err).Msg("审计日志写入失败")
		}
	}
}

// SaveAuditLogWithHashChain 在事务中写入审计日志并计算哈希链。
// 导出供 WebSocket 等非中间件路径使用。db.Statement.Context（若有）
// 同时约束等待全局哈希链锁和事务中的数据库操作。
func SaveAuditLogWithHashChain(db *gorm.DB, record *model.AuditLog) error {
	if db == nil {
		return errors.New("audit database unavailable")
	}
	ctx, cancel := auditWriteContext(db)
	defer cancel()
	if err := auditWriteMu.LockContext(ctx); err != nil {
		return err
	}
	defer auditWriteMu.Unlock()

	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var previous model.AuditLog
		err := tx.Select("entry_hash").Order("id desc").Take(&previous).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		record.PrevHash = previous.EntryHash
		record.EntryHash = hashAuditLogEntry(record)
		return tx.Create(record).Error
	})
}

func auditWriteContext(db *gorm.DB) (context.Context, context.CancelFunc) {
	ctx := context.Background()
	if db != nil && db.Statement != nil && db.Statement.Context != nil {
		ctx = db.Statement.Context
	}
	return context.WithTimeout(ctx, auditWriteTimeout)
}

func hashAuditLogEntry(record *model.AuditLog) string {
	payload := fmt.Sprintf(
		"%d|%s|%s|%s|%s|%d|%s|%s|%s|%s",
		record.UserID,
		record.Username,
		record.Role,
		record.Method,
		record.Path,
		record.StatusCode,
		record.ClientIP,
		record.UserAgent,
		record.CreatedAt.UTC().Format(time.RFC3339Nano),
		record.PrevHash,
	)
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
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
