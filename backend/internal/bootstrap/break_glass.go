package bootstrap

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
	"xirang/backend/internal/auth"
	"xirang/backend/internal/dbtx"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	breakGlassConfirmationEnv = "XIRANG_BREAK_GLASS_CONFIRMATION"
	breakGlassTimeout         = 30 * time.Second
	breakGlassReasonMax       = 512
)

var (
	ErrBreakGlassConfirmation = errors.New("break-glass confirmation is invalid")
	ErrBreakGlassReason       = errors.New("break-glass reason is required")
	ErrBreakGlassAlreadyAdmin = errors.New("target user is already an administrator")
	ErrBreakGlassAdminExists  = errors.New("break-glass requires no existing administrator")
)

type BreakGlassRequest struct {
	Username     string
	Reason       string
	Confirmation string
}

// BreakGlassPromoteAdmin is an explicit, operator-invoked recovery procedure.
// It never runs from startup/bootstrap. The confirmation must be provisioned
// separately in XIRANG_BREAK_GLASS_CONFIRMATION, and the bounded transaction
// atomically promotes the selected account, invalidates its pending sessions,
// and appends a hash-chained audit record. Missing audit storage fails closed.
func BreakGlassPromoteAdmin(ctx context.Context, db *gorm.DB, request BreakGlassRequest) error {
	if db == nil {
		return errors.New("break-glass database is unavailable")
	}
	username := strings.TrimSpace(request.Username)
	if username == "" {
		return fmt.Errorf("%w: username", ErrBreakGlassReason)
	}
	reason := strings.TrimSpace(request.Reason)
	if len(reason) < 8 || len(reason) > breakGlassReasonMax {
		return ErrBreakGlassReason
	}
	expected := strings.TrimSpace(os.Getenv(breakGlassConfirmationEnv))
	provided := strings.TrimSpace(request.Confirmation)
	if expected == "" || provided == "" || subtle.ConstantTimeCompare([]byte(expected), []byte(provided)) != 1 {
		return ErrBreakGlassConfirmation
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, breakGlassTimeout)
	defer cancel()

	return dbtx.WithSQLiteBusyRetryTx(ctx, db, func(tx *gorm.DB) error {
		// Lock all identity rows using the same cross-process serialization
		// point as role mutations; this also covers zero-admin deployments.
		if err := auth.SerializeIdentityMutationLock(tx); err != nil {
			return err
		}
		var adminCount int64
		if err := tx.Model(&model.User{}).Where("role = ?", "admin").Count(&adminCount).Error; err != nil {
			return err
		}
		if adminCount > 0 {
			return ErrBreakGlassAdminExists
		}

		query := tx.Where("username = ?", username)
		if tx.Name() != "sqlite" {
			query = query.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		var user model.User
		if err := query.Take(&user).Error; err != nil {
			return err
		}
		if user.Role == "admin" {
			return ErrBreakGlassAlreadyAdmin
		}
		result := tx.Table("users").Where(
			"id = ? AND token_version = ? AND role = ?",
			user.ID, user.TokenVersion, user.Role,
		).Updates(map[string]any{
			"role":          "admin",
			"token_version": gorm.Expr("token_version + 1"),
			"updated_at":    time.Now().UTC(),
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("break-glass target changed concurrently")
		}
		if err := tx.Where("user_id = ?", user.ID).Delete(&model.PendingAuthToken{}).Error; err != nil {
			return err
		}

		if err := tx.Create(&model.BreakGlassAudit{
			TargetUserID:   user.ID,
			TargetUsername: user.Username,
			Reason:         reason,
			CreatedAt:      time.Now().UTC(),
		}).Error; err != nil {
			return err
		}
		record := &model.AuditLog{
			UserID:     user.ID,
			Username:   user.Username,
			Role:       "admin",
			Method:     "CLI",
			Path:       "break-glass/admin-promote",
			StatusCode: 200,
			ClientIP:   "local",
			UserAgent:  "xirang-recover-admin",
			CreatedAt:  time.Now().UTC(),
		}
		// SaveAuditLogWithHashChain uses a nested savepoint when called with
		// this transaction. Any audit failure therefore rolls back promotion.
		return middleware.SaveAuditLogWithHashChain(tx, record)
	})
}
