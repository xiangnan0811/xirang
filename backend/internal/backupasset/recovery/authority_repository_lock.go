package recovery

import (
	"context"
	"errors"
	"fmt"

	"xirang/backend/internal/backupasset"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var errRecoveryAuthorityRepositoryLock = errors.New("recovery authority repository lock unavailable")

type recoveryAuthorityRepositoryLockError struct {
	cause error
}

func (err *recoveryAuthorityRepositoryLockError) Error() string {
	return errRecoveryAuthorityRepositoryLock.Error()
}

func (err *recoveryAuthorityRepositoryLockError) Unwrap() error {
	return err.cause
}

func (err *recoveryAuthorityRepositoryLockError) Is(target error) bool {
	return target == errRecoveryAuthorityRepositoryLock || target == ErrRecoveryTargetUnavailable
}

// lockRecoveryAuthorityRepositoryTx serializes authority transactions sharing
// an exact repository before they acquire source or target row locks.
func lockRecoveryAuthorityRepositoryTx(ctx context.Context, tx *gorm.DB, repositoryID string) error {
	ctx = sourceValidationContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validOpaqueID(repositoryID) || tx == nil {
		return ErrRecoverySourceUnavailable
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
			return &recoveryAuthorityRepositoryLockError{cause: err}
		}
		return nil
	case "sqlite":
		// Production SQLite transactions begin IMMEDIATE and serialize writers.
		return nil
	default:
		return fmt.Errorf("%w: unsupported database for recovery authority repository lock", backupasset.ErrInvalidState)
	}
}
