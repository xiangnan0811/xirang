package retention

import (
	"context"
	"fmt"

	"xirang/backend/internal/backupasset"

	"gorm.io/gorm"
)

// lockRetentionRepositoryTx serializes retention and explicit purge work for a
// repository before either operation acquires business row locks.
func lockRetentionRepositoryTx(ctx context.Context, tx *gorm.DB, repositoryID string) error {
	if err := backupasset.ValidateOpaqueID(repositoryID); err != nil {
		return fmt.Errorf("lock retention repository: %w", err)
	}
	if tx == nil {
		return fmt.Errorf("%w: retention repository transaction is unavailable", backupasset.ErrInvalidState)
	}
	switch tx.Name() {
	case "postgres":
		key := "xirang:retention-repository:v1:" + repositoryID
		if err := tx.WithContext(ctx).Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", key).Error; err != nil {
			return fmt.Errorf("lock retention repository: %w", err)
		}
		return nil
	case "sqlite":
		// Production SQLite transactions begin IMMEDIATE and already serialize writers.
		return nil
	default:
		return fmt.Errorf("%w: unsupported database for retention repository lock: %s", backupasset.ErrInvalidState, tx.Name())
	}
}
