package recovery

import (
	"context"
	"errors"

	"xirang/backend/internal/backupasset/internal/authoritylock"

	"gorm.io/gorm"
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
	err := authoritylock.LockRepository(ctx, tx, repositoryID)
	if err == nil || tx.Name() != "postgres" || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	// The shared lock sanitizes its public message; retain the underlying
	// database diagnostic for recovery's existing retry classification.
	cause := errors.Unwrap(err)
	if cause == nil {
		cause = err
	}
	return &recoveryAuthorityRepositoryLockError{cause: cause}
}
