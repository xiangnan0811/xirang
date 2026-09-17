package bootstrap

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"
	taskPkg "xirang/backend/internal/task"
)

// MigrateLegacyResticTaskConfigs performs the one-time domain migration for
// persisted Restic task configurations. It runs after encryption migration and
// before any scheduler/asset runtime is started. The task row is read again
// under a row lock, and the update is a narrow executor_config + updated_at CAS;
// no stale in-memory Task can overwrite execution state, references, or
// diagnostics.
//
// Invalid configuration rows are isolated and logged so one historical bad
// task cannot prevent the rest of the Core from starting. Database and
// encryption failures while writing a valid migration remain infrastructure
// failures and are returned to the startup caller.
func MigrateLegacyResticTaskConfigs(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("restic task config migration requires a database")
	}

	type candidateRow struct {
		ID uint
	}
	var candidates []candidateRow
	query := db.Session(&gorm.Session{SkipHooks: true}).Table("tasks").
		Select("id").Where("LOWER(executor_type) = ? AND executor_config <> ''", "restic").Find(&candidates)
	if query.Error != nil {
		if isMissingRelation(query.Error) {
			return nil
		}
		return fmt.Errorf("load Restic task configs for migration: %w", query.Error)
	}

	migrated := 0
	isolated := 0
	for _, candidate := range candidates {
		changed, skipped, err := migrateLegacyResticTaskConfigRow(db, candidate.ID)
		if err != nil {
			return err
		}
		if changed {
			migrated++
		}
		if skipped {
			isolated++
		}
	}
	if migrated > 0 || isolated > 0 {
		bootLog.Info().Int("migrated", migrated).Int("isolated", isolated).
			Msg("Restic 任务配置兼容字段迁移完成")
	}
	return nil
}

func migrateLegacyResticTaskConfigRow(db *gorm.DB, taskID uint) (changed, skipped bool, returnErr error) {
	type taskConfigRow struct {
		ID             uint
		ExecutorType   string    `gorm:"column:executor_type"`
		ExecutorConfig string    `gorm:"column:executor_config"`
		UpdatedAt      time.Time `gorm:"column:updated_at"`
	}

	var skipReason error
	err := db.Transaction(func(tx *gorm.DB) error {
		var current taskConfigRow
		result := tx.Session(&gorm.Session{SkipHooks: true}).
			Clauses(clause.Locking{Strength: "UPDATE"}).Table("tasks").
			Select("id", "executor_type", "executor_config", "updated_at").
			Where("id = ?", taskID).Take(&current)
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil
		}
		if result.Error != nil {
			return fmt.Errorf("lock Restic task %d for config migration: %w", taskID, result.Error)
		}
		if !strings.EqualFold(strings.TrimSpace(current.ExecutorType), "restic") ||
			strings.TrimSpace(current.ExecutorConfig) == "" {
			return nil
		}

		plain, err := secure.DecryptIfNeeded(current.ExecutorConfig)
		if err != nil {
			if isEncryptionInfrastructureError(err) {
				return fmt.Errorf("decrypt Restic task %d executor_config: %w", taskID, err)
			}
			// A single corrupt historical ciphertext is task-local data damage;
			// keep it isolated so valid tasks and Core infrastructure can start.
			skipReason = fmt.Errorf("decrypt executor_config: %w", err)
			return nil
		}
		migratedConfig, didChange, err := taskPkg.MigrateLegacyResticConfig(plain)
		if err != nil {
			// The converter never includes payload bytes in its diagnostics.
			skipReason = err
			return nil
		}
		if !didChange {
			return nil
		}

		persisted := model.Task{ExecutorConfig: migratedConfig}
		if err := persisted.BeforeSave(tx); err != nil {
			return fmt.Errorf("encrypt migrated Restic task %d config: %w", taskID, err)
		}
		now := time.Now().UTC()
		update := tx.Session(&gorm.Session{SkipHooks: true}).Model(&model.Task{}).
			Where("id = ? AND updated_at = ?", current.ID, current.UpdatedAt).
			Updates(map[string]any{
				"executor_config": persisted.ExecutorConfig,
				"updated_at":      now,
			})
		if update.Error != nil {
			return fmt.Errorf("persist migrated Restic task %d config: %w", taskID, update.Error)
		}
		if update.RowsAffected != 1 {
			// A concurrent writer won the CAS. Never apply a stale migration over
			// its newer row; the next startup will re-evaluate it.
			skipReason = fmt.Errorf("task revision changed during migration")
			return nil
		}
		changed = true
		return nil
	})
	if err != nil {
		return false, false, err
	}
	if skipReason != nil {
		bootLog.Warn().Uint("task_id", taskID).Err(skipReason).
			Msg("隔离无效的历史 Restic 任务配置，继续启动")
		skipped = true
	}
	return changed, skipped, nil
}

// secure currently exposes key/bootstrap failures as ordinary errors. Keep
// those distinct from malformed ciphertext so missing encryption
// infrastructure fails startup rather than silently isolating every task.
func isEncryptionInfrastructureError(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	for _, marker := range []string{
		"DATA_ENCRYPTION_KEY",
		"DATA_ENCRYPTION_LEGACY_KEY",
		"无效的数据加密密钥",
		"生成临时加密密钥失败",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}
