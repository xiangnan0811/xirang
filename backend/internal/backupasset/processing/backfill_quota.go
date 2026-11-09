package processing

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type BackfillPolicySource func(context.Context, *gorm.DB) (BackfillPolicy, error)

var ErrBackfillDenied = errors.New("backup asset background work admission denied")

type BackfillAdmissionError struct{ Reason BackfillAdmissionReason }

func (err *BackfillAdmissionError) Error() string { return ErrBackfillDenied.Error() }
func (err *BackfillAdmissionError) Unwrap() error { return ErrBackfillDenied }

func backfillTransaction(ctx context.Context, db *gorm.DB, operation func(*gorm.DB) error) error {
	if db.Dialector.Name() == "postgres" {
		return db.WithContext(ctx).Transaction(operation, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	}
	return db.WithContext(ctx).Transaction(operation)
}

type backfillSchemaVersion struct {
	Version int64
	Dirty   bool
}

func readBackfillMetadata(ctx context.Context, tx *gorm.DB, lock bool) error {
	var rows []backfillSchemaVersion
	query := tx.WithContext(ctx).Table("schema_migrations").Select("version, dirty")
	if lock && tx.Dialector.Name() == "postgres" {
		query = query.Clauses(clause.Locking{Strength: "SHARE"})
	}
	if err := query.Find(&rows).Error; err != nil {
		return fmt.Errorf("read backfill schema metadata: %w", err)
	}
	if len(rows) != 1 || rows[0].Dirty || rows[0].Version < 99 {
		return fmt.Errorf("%w: backfill schema metadata unavailable", ErrInvalidContract)
	}
	return nil
}

// lockBackfillSerialization holds the schema and ledger locks until the caller's
// transaction ends. It does not consult or apply the admission policy.
func lockBackfillSerialization(ctx context.Context, tx *gorm.DB) (model.BackupAssetProcessingBackfillLedger, error) {
	var ledger model.BackupAssetProcessingBackfillLedger
	if err := readBackfillMetadata(ctx, tx, true); err != nil {
		return ledger, err
	}
	var ledgers []model.BackupAssetProcessingBackfillLedger
	result := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Find(&ledgers)
	if result.Error != nil {
		return ledger, fmt.Errorf("lock backfill ledger: %w", result.Error)
	}
	if len(ledgers) != 1 {
		return ledger, fmt.Errorf("%w: invalid backfill ledger", ErrInvalidContract)
	}
	ledger = ledgers[0]
	if ledger.ID != 1 || ledger.JobsCount < 0 || ledger.BytesCount < 0 || ledger.WindowStartedAt.IsZero() || !ledger.WindowStartedAt.UTC().Equal(ledger.WindowStartedAt.UTC().Truncate(time.Hour)) || ledger.UpdatedAt.IsZero() {
		return ledger, fmt.Errorf("%w: invalid backfill ledger", ErrInvalidContract)
	}
	if err := readBackfillMetadata(ctx, tx, false); err != nil {
		return ledger, err
	}
	return ledger, nil
}

func (coordinator *Coordinator) lockBackfillState(ctx context.Context, tx *gorm.DB) (BackfillPolicy, model.BackupAssetProcessingBackfillLedger, time.Time, error) {
	ledger, err := lockBackfillSerialization(ctx, tx)
	if err != nil {
		return BackfillPolicy{}, ledger, time.Time{}, err
	}
	policy, err := coordinator.config.BackfillPolicySource(ctx, tx)
	if err != nil {
		return BackfillPolicy{}, ledger, time.Time{}, err
	}
	if !validBackfillPolicy(policy) {
		return BackfillPolicy{}, ledger, time.Time{}, fmt.Errorf("%w: invalid backfill policy", ErrInvalidContract)
	}
	return policy, ledger, coordinator.utcNow(), nil
}

func backfillWindow(ledger model.BackupAssetProcessingBackfillLedger, now time.Time) (BackfillUsage, error) {
	window := now.UTC().Truncate(time.Hour)
	stored := ledger.WindowStartedAt.UTC()
	if stored.After(window) {
		return BackfillUsage{}, &BackfillAdmissionError{Reason: BackfillAdmissionInvalid}
	}
	usage := BackfillUsage{WindowStartedAt: window, ProviderActive: map[string]int{}, CapabilityActive: map[string]int{}}
	if stored.Equal(window) {
		usage.Jobs, usage.Bytes = ledger.JobsCount, ledger.BytesCount
	}
	return usage, nil
}

func sourceBackfillMetadata(ctx context.Context, tx *gorm.DB, descriptor WorkDescriptorV1) (string, int64, error) {
	var rows []struct {
		Provider string
		Size     int64
	}
	err := tx.WithContext(ctx).Table("catalog_entries AS entries").Select("repositories.provider_kind AS provider, entries.size AS size").
		Joins("JOIN catalog_generations AS generations ON generations.id = entries.generation_id AND generations.recovery_point_id = entries.recovery_point_id").
		Joins("JOIN recovery_points AS points ON points.id = entries.recovery_point_id").
		Joins("JOIN backup_repositories AS repositories ON repositories.id = points.repository_id").
		Where("entries.generation_id = ? AND entries.entry_id = ? AND entries.recovery_point_id = ?", descriptor.CatalogGenerationID, descriptor.Source.EntryID, descriptor.Source.RecoveryPointID).Limit(2).Scan(&rows).Error
	if err != nil {
		return "", 0, fmt.Errorf("read processing source metadata: %w", err)
	}
	if len(rows) != 1 || rows[0].Size < 0 {
		return "", 0, fmt.Errorf("%w: processing source metadata unavailable", ErrInvalidContract)
	}
	provider := strings.ToLower(strings.TrimSpace(rows[0].Provider))
	switch provider {
	case "restic", "rsync", "rclone", "command":
	default:
		return "", 0, fmt.Errorf("%w: invalid processing provider", ErrInvalidContract)
	}
	return provider, rows[0].Size, nil
}

// backfillActiveRowsSQL is also the execution occupancy EXPLAIN target. The
// jobs index bounds the scan to current background jobs before joining their
// exact current attempt and its RecoveryPoint lease.
const backfillActiveRowsSQL = `SELECT jobs.id, jobs.provider_kind, jobs.capability, attempts.recovery_point_fence_hash, leases.fence_token
FROM backup_asset_processing_jobs AS jobs
JOIN backup_asset_processing_attempts AS attempts ON attempts.id = jobs.current_attempt_id AND attempts.job_id = jobs.id
JOIN recovery_point_leases AS leases ON leases.id = attempts.recovery_point_lease_id AND leases.recovery_point_id = jobs.recovery_point_id AND leases.owner_id = jobs.id AND leases.attempt_id = attempts.recovery_point_attempt_id
WHERE jobs.is_current = ? AND jobs.priority_class = ? AND jobs.state IN ? AND jobs.absolute_deadline > ?
AND attempts.is_current = ? AND attempts.state = ? AND attempts.worker_lease_expires_at > ? AND attempts.absolute_deadline > ?
AND leases.holder_type = ? AND leases.status = ? AND leases.lease_expires_at > ? AND leases.absolute_deadline > ?`

func backfillOccupancy(ctx context.Context, tx *gorm.DB, now time.Time, includeQueued bool) (map[string]int, map[string]int, error) {
	var rows []struct {
		ID                     string
		ProviderKind           string
		Capability             string
		RecoveryPointFenceHash string
		FenceToken             string
	}
	states := []string{string(ProcessingLeased), string(ProcessingFetching), string(ProcessingMaterializing), string(ProcessingProcessing), string(ProcessingUploading), string(ProcessingValidating)}
	err := tx.WithContext(ctx).Raw(backfillActiveRowsSQL, true, string(PriorityBackground), states, now, true, "active", now, now, string(backupasset.LeaseHolderProcessingJob), string(backupasset.LeaseActive), now, now).Scan(&rows).Error
	if err != nil {
		return nil, nil, fmt.Errorf("read backfill active attempts: %w", err)
	}
	providers, capabilities := map[string]int{}, map[string]int{}
	for _, row := range rows {
		if hashFence(row.FenceToken) != row.RecoveryPointFenceHash {
			continue
		}
		providers[row.ProviderKind]++
		capabilities[row.Capability]++
	}
	if includeQueued {
		var queued []struct {
			ProviderKind string
			Capability   string
		}
		if err := tx.WithContext(ctx).Table("backup_asset_processing_jobs").Select("provider_kind, capability").Where("is_current = ? AND priority_class = ? AND state IN ?", true, string(PriorityBackground), []string{string(ProcessingQueued), string(ProcessingRetryWait)}).Scan(&queued).Error; err != nil {
			return nil, nil, fmt.Errorf("read backfill queued jobs: %w", err)
		}
		for _, row := range queued {
			providers[row.ProviderKind]++
			capabilities[row.Capability]++
		}
	}
	return providers, capabilities, nil
}

func (coordinator *Coordinator) BackfillSnapshot(ctx context.Context) (BackfillPolicy, BackfillUsage, error) {
	var policy BackfillPolicy
	var usage BackfillUsage
	err := coordinator.retryConflicts(ctx, func() error {
		return backfillTransaction(ctx, coordinator.db, func(tx *gorm.DB) error {
			if tx.Dialector.Name() == "postgres" {
				if err := tx.Exec("LOCK TABLE backup_asset_processing_jobs IN ACCESS SHARE MODE").Error; err != nil {
					return err
				}
			}
			var ledger model.BackupAssetProcessingBackfillLedger
			var now time.Time
			var err error
			policy, ledger, now, err = coordinator.lockBackfillState(ctx, tx)
			if err != nil {
				return err
			}
			usage, err = backfillWindow(ledger, now)
			if err != nil {
				return err
			}
			usage.ProviderActive, usage.CapabilityActive, err = backfillOccupancy(ctx, tx, now, true)
			return err
		})
	})
	if err != nil {
		return BackfillPolicy{}, BackfillUsage{}, err
	}
	return policy, usage, nil
}
