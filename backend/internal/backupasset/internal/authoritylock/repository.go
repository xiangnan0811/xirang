package authoritylock

import (
	"context"
	"errors"
	"fmt"

	"xirang/backend/internal/backupasset"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type repositoryLockError struct {
	cause error
}

func (err *repositoryLockError) Error() string {
	return "repository authority lock unavailable"
}

func (err *repositoryLockError) Unwrap() error {
	return err.cause
}

// LockRepository serializes authority transactions for an exact source
// repository before they acquire shared rows. PostgreSQL execution errors have
// a sanitized message and unwrap to the underlying database error. SQLite
// transactions rely on their existing IMMEDIATE writer serialization.
func LockRepository(ctx context.Context, tx *gorm.DB, repositoryID string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if backupasset.ValidateOpaqueID(repositoryID) != nil || tx == nil {
		return fmt.Errorf("%w: invalid repository authority lock input", backupasset.ErrInvalidState)
	}
	if tx.Error != nil {
		return tx.Error
	}
	switch tx.Name() {
	case "postgres":
		key := "xirang:recovery-authority-repository:v1:" + repositoryID
		// GORM's SQL trace may interpolate bound parameters into logs.
		err := tx.Session(&gorm.Session{Logger: logger.Discard}).WithContext(ctx).
			Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", key).Error
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if errors.Is(err, context.Canceled) {
			return context.Canceled
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return context.DeadlineExceeded
		}
		if err != nil {
			return &repositoryLockError{cause: err}
		}
		return nil
	case "sqlite":
		return nil
	default:
		return fmt.Errorf("%w: unsupported database for recovery authority repository lock", backupasset.ErrInvalidState)
	}
}
