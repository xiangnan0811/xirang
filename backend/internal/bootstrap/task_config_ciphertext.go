package bootstrap

import (
	"database/sql"
	"errors"
	"os"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"

	"xirang/backend/internal/secure"
)

var (
	errTaskConfigRead        = errors.New("task executor config encryption: database read failed")
	errTaskConfigEncrypt     = errors.New("task executor config encryption: encryption failed")
	errTaskConfigKey         = errors.New("task executor config encryption: configured encryption key required")
	errTaskConfigWrite       = errors.New("task executor config encryption: database write failed")
	errTaskConfigTransaction = errors.New("task executor config encryption: transaction failed")
)

func requireTaskConfigEncryptionKey() error {
	if strings.TrimSpace(os.Getenv("DATA_ENCRYPTION_KEY")) == "" {
		return errTaskConfigKey
	}
	return nil
}

// EncryptPlaintextTaskExecutorConfigs repairs residual plaintext without changing
// task revisions or interpreting the configuration. Each row is re-read under a
// write lock; completed rows remain safe to revisit after a partial failure.
// SQLite callers must use the production immediate transaction configuration.
func EncryptPlaintextTaskExecutorConfigs(db *gorm.DB) error {
	if db == nil {
		return errTaskConfigRead
	}
	db = db.Session(&gorm.Session{Logger: logger.Discard, SkipHooks: true})
	type row struct {
		ID             int64
		ExecutorConfig sql.NullString `gorm:"column:executor_config"`
	}
	const pageSize = 256
	var after int64
	first := true
	for {
		var rows []row
		query := db.Table("tasks").Select("id", "executor_config").
			Where("executor_config IS NOT NULL")
		if !first {
			query = query.Where("id > ?", after)
		}
		result := query.Order("id").Limit(pageSize).Find(&rows)
		if result.Error != nil {
			return errTaskConfigRead
		}
		if len(rows) == 0 {
			return nil
		}
		first = false
		for _, candidate := range rows {
			after = candidate.ID
			if !candidate.ExecutorConfig.Valid || strings.TrimSpace(candidate.ExecutorConfig.String) == "" || secure.IsEncrypted(candidate.ExecutorConfig.String) {
				continue
			}
			err := db.Transaction(func(tx *gorm.DB) error {
				var current row
				query := tx.Table("tasks").Select("id", "executor_config").Where("id = ?", candidate.ID)
				if tx.Name() != "sqlite" {
					query = query.Clauses(clause.Locking{Strength: "UPDATE"})
				}
				result := query.Take(&current)
				if errors.Is(result.Error, gorm.ErrRecordNotFound) {
					return nil
				}
				if result.Error != nil {
					return errTaskConfigRead
				}
				if !current.ExecutorConfig.Valid || strings.TrimSpace(current.ExecutorConfig.String) == "" || secure.IsEncrypted(current.ExecutorConfig.String) {
					return nil
				}
				if err := requireTaskConfigEncryptionKey(); err != nil {
					return err
				}
				ciphertext, err := secure.EncryptIfNeeded(current.ExecutorConfig.String)
				if err != nil {
					return errTaskConfigEncrypt
				}
				update := tx.Table("tasks").Where("id = ?", current.ID).UpdateColumn("executor_config", ciphertext)
				if update.Error != nil || update.RowsAffected != 1 {
					return errTaskConfigWrite
				}
				return nil
			})
			if err != nil {
				if err == errTaskConfigRead || err == errTaskConfigEncrypt || err == errTaskConfigWrite || err == errTaskConfigKey {
					return err
				}
				return errTaskConfigTransaction
			}
		}
	}
}
