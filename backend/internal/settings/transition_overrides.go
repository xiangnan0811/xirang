package settings

import (
	"context"
	"database/sql"
	"errors"
	"maps"
	"sort"
	"time"

	"xirang/backend/internal/dbtx"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var (
	// ErrSettingOverrideConflict means the protected database state changed.
	ErrSettingOverrideConflict = errors.New("settings override conflict")
	// ErrSettingOverrideCommitUncertain means the transaction body completed,
	// but its commit outcome could not be established.
	ErrSettingOverrideCommitUncertain = errors.New("settings override commit uncertain")
)

// SettingOverrideReceipt is an opaque before- and after-image of a committed
// settings transition. It may only be used to compensate that transition.
type SettingOverrideReceipt struct {
	keys   []string
	before map[string]transitionOverrideRow
	after  map[string]transitionOverrideRow
}

type transitionOverrideRow struct {
	value         string
	updatedAt     time.Time
	sqliteUpdated string
}

// safeSettingError preserves error identity without exposing driver messages,
// which can contain stored values.
type safeSettingError struct {
	message string
	cause   error
}

func (err *safeSettingError) Error() string { return err.message }
func (err *safeSettingError) Unwrap() error { return err.cause }

type uncertainSettingCommitError struct{ cause error }

func (err *uncertainSettingCommitError) Error() string {
	return ErrSettingOverrideCommitUncertain.Error()
}
func (err *uncertainSettingCommitError) Unwrap() error { return err.cause }
func (err *uncertainSettingCommitError) Is(target error) bool {
	return target == ErrSettingOverrideCommitUncertain
}

// UpdateTransitionOverridesContext persists the supplied registered settings
// and returns the exact images needed to conditionally compensate the write.
// The caller owns the Foundation mutation gate.
func (s *Service) UpdateTransitionOverridesContext(ctx context.Context, current, values map[string]string) (*SettingOverrideReceipt, error) {
	if s == nil || s.db == nil || len(values) == 0 || len(values) > len(registry) {
		return nil, errors.New("settings transition is unavailable")
	}
	keys := make([]string, 0, len(values))
	for key, value := range values {
		if err := s.Validate(key, value); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return s.persistTransitionOverrides(ctx, current, keys, func(ctx context.Context, tx *gorm.DB) error {
		for _, key := range keys {
			if err := s.UpdateWithTxContext(ctx, tx, key, values[key]); err != nil {
				return err
			}
		}
		return nil
	})
}

// DeleteTransitionOverrideContext deletes a Foundation override with the same
// guarded compensation semantics as UpdateTransitionOverridesContext.
func (s *Service) DeleteTransitionOverrideContext(ctx context.Context, current map[string]string, key string) (*SettingOverrideReceipt, error) {
	if s == nil || s.db == nil || !IsBackupAssetFoundationSetting(key) {
		return nil, errors.New("settings transition is unavailable")
	}
	return s.persistTransitionOverrides(ctx, current, []string{key}, func(ctx context.Context, tx *gorm.DB) error {
		return s.DeleteWithTxContext(ctx, tx, key)
	})
}

func (s *Service) persistTransitionOverrides(ctx context.Context, current map[string]string, keys []string, persist func(context.Context, *gorm.DB) error) (*SettingOverrideReceipt, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	receipt := &SettingOverrideReceipt{keys: keys}
	err := s.withTransitionTransaction(ctx, func(tx *gorm.DB) error {
		actual, err := s.BackupAssetSettingsSnapshotTx(ctx, tx)
		if err != nil {
			return &safeSettingError{message: "read Foundation settings failed", cause: err}
		}
		if !maps.Equal(current, actual) {
			return ErrSettingOverrideConflict
		}
		receipt.before, err = readTransitionRows(tx, keys)
		if err != nil {
			return err
		}
		if err := persist(ctx, tx); err != nil {
			return err
		}
		receipt.after, err = readTransitionRows(tx, keys)
		return err
	})
	if err != nil {
		return nil, err
	}
	s.InvalidateCachedValues(keys)
	return receipt, nil
}

// RestoreTransitionOverridesContext restores a transition only if every
// affected row still equals its recorded post-image. All checks precede writes.
func (s *Service) RestoreTransitionOverridesContext(ctx context.Context, receipt *SettingOverrideReceipt) error {
	if s == nil || s.db == nil || receipt == nil || len(receipt.keys) == 0 || len(receipt.keys) > len(registry) {
		return errors.New("settings transition restoration is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	err := s.withTransitionTransaction(ctx, func(tx *gorm.DB) error {
		actual, err := readTransitionRows(tx, receipt.keys)
		if err != nil {
			return err
		}
		for _, key := range receipt.keys {
			want, wantExists := receipt.after[key]
			got, exists := actual[key]
			if exists != wantExists || (exists && !equalTransitionRows(tx.Name(), got, want)) {
				return ErrSettingOverrideConflict
			}
		}
		for _, key := range receipt.keys {
			before, wasPresent := receipt.before[key]
			_, isPresent := actual[key]
			if !wasPresent {
				if !isPresent {
					continue
				}
				err = tx.Exec("DELETE FROM system_settings WHERE key = ?", key).Error
			} else {
				var timestamp any = before.updatedAt
				if tx.Name() == "sqlite" {
					timestamp = before.sqliteUpdated
				}
				if isPresent {
					err = tx.Exec("UPDATE system_settings SET value = ?, updated_at = ? WHERE key = ?", before.value, timestamp, key).Error
				} else {
					err = tx.Exec("INSERT INTO system_settings (key, value, updated_at) VALUES (?, ?, ?)", key, before.value, timestamp).Error
				}
			}
			if err != nil {
				return &safeSettingError{message: "restore settings override failed", cause: err}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.InvalidateCachedValues(receipt.keys)
	return nil
}

func equalTransitionRows(dialect string, left, right transitionOverrideRow) bool {
	if left.value != right.value {
		return false
	}
	if dialect == "sqlite" {
		return left.sqliteUpdated == right.sqliteUpdated
	}
	return left.updatedAt.Equal(right.updatedAt)
}

func readTransitionRows(tx *gorm.DB, keys []string) (result map[string]transitionOverrideRow, resultErr error) {
	timestamp := "updated_at"
	if tx.Name() == "sqlite" {
		timestamp = "CAST(updated_at AS TEXT)"
	}
	rows, err := tx.Raw("SELECT key, value, "+timestamp+" FROM system_settings WHERE key IN ? ORDER BY key LIMIT ?", keys, len(keys)+1).Rows()
	if err != nil {
		return nil, &safeSettingError{message: "read settings overrides failed", cause: err}
	}
	defer func() {
		if err := rows.Close(); err != nil {
			result = nil
			resultErr = errors.Join(resultErr, &safeSettingError{message: "read settings overrides failed", cause: err})
		}
	}()
	result = make(map[string]transitionOverrideRow, len(keys))
	for rows.Next() {
		var key string
		var row transitionOverrideRow
		if tx.Name() == "sqlite" {
			err = rows.Scan(&key, &row.value, &row.sqliteUpdated)
		} else {
			err = rows.Scan(&key, &row.value, &row.updatedAt)
		}
		if err != nil {
			return nil, &safeSettingError{message: "read settings overrides failed", cause: err}
		}
		if _, exists := result[key]; exists || len(result) >= len(keys) {
			return nil, errors.New("invalid settings override rows")
		}
		result[key] = row
	}
	if err := rows.Err(); err != nil {
		return nil, &safeSettingError{message: "read settings overrides failed", cause: err}
	}
	return result, nil
}

// withTransitionTransaction serializes all settings writers before any read.
// The retry helper may retry busy aborted bodies, but never a completed body.
func (s *Service) withTransitionTransaction(ctx context.Context, body func(*gorm.DB) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	db := s.db.WithContext(ctx).Session(&gorm.Session{Logger: logger.Discard})
	if db.Dialector == nil || (db.Name() != "sqlite" && db.Name() != "postgres") {
		return errors.New("unsupported settings database")
	}
	completed := false
	wrapped := func(tx *gorm.DB) error {
		var err error
		if tx.Name() == "sqlite" {
			err = tx.Exec("UPDATE system_settings SET key = key WHERE 1 = 0").Error
		} else {
			err = tx.Exec("LOCK TABLE system_settings IN SHARE ROW EXCLUSIVE MODE").Error
		}
		if err != nil {
			return &safeSettingError{message: "lock settings overrides failed", cause: err}
		}
		if err := body(tx); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		completed = true
		return nil
	}
	var err error
	commitNotApplied := false
	if db.Name() == "sqlite" {
		outcome := dbtx.WithSQLiteBusyRetryTxOutcome(ctx, db, wrapped)
		err = outcome.TransactionErr
		completed = outcome.BodyCompleted
		commitNotApplied = outcome.CommitNotApplied
		if err != nil && outcome.CleanupErr != nil {
			classified := classifyTransitionError(err, completed, commitNotApplied, db.Name())
			return &safeSettingError{message: classified.Error(), cause: errors.Join(classified, outcome.CleanupErr)}
		}
	} else {
		err = db.Transaction(wrapped, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	}
	return classifyTransitionError(err, completed, commitNotApplied, db.Name())
}

func classifyTransitionError(err error, completed, commitNotApplied bool, dialect string) error {
	if err == nil {
		return nil
	}
	if err == context.DeadlineExceeded || (dialect == "sqlite" && err == context.Canceled) {
		// database/sql returns the bare deadline before calling the driver.
		// pgx wraps driver deadlines, but may return bare cancellation after sending COMMIT.
		commitNotApplied = true
	}
	if dialect == "postgres" && pgconn.SafeToRetry(err) {
		commitNotApplied = true
	}
	if completed && !commitNotApplied && !errors.Is(err, sql.ErrTxDone) && !errors.Is(err, pgx.ErrTxCommitRollback) {
		return &uncertainSettingCommitError{cause: err}
	}
	if errors.Is(err, ErrSettingOverrideConflict) {
		return ErrSettingOverrideConflict
	}
	return &safeSettingError{message: "settings transition failed", cause: err}
}
