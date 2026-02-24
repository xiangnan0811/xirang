package middleware

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

var auditWriteMu sync.Mutex

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
		record.CreatedAt = time.Now().UTC()
		if err := saveAuditLogWithHashChain(db, &record); err != nil {
			log.Printf("审计日志写入失败: %v", err)
		}
	}
}

func saveAuditLogWithHashChain(db *gorm.DB, record *model.AuditLog) error {
	auditWriteMu.Lock()
	defer auditWriteMu.Unlock()
	return db.Transaction(func(tx *gorm.DB) error {
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
