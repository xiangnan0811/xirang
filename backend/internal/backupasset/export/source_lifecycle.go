package export

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

// SourceLifecycle routes exact-point Export jobs through their existing
// ordered job lifecycle without destroying payloads during prepare.
type SourceLifecycle struct {
	db         *gorm.DB
	lifecycle  *Lifecycle
	sourcePort exportSourcePrepareLifecyclePort
	now        func() time.Time
	batchSize  int
}

// exportSourcePrepareLifecyclePort is narrower than LifecyclePort because
// prepare releases only the lifecycle request's exact RecoveryPoint source.
// Full job cleanup remains responsible for all sources and quota reservations.
type exportSourcePrepareLifecyclePort interface {
	ReleaseRecoveryPointSource(context.Context, backupasset.SourceLifecycleRequest, string) error
}

func NewSourceLifecycle(db *gorm.DB, lifecycle *Lifecycle, now func() time.Time, batchSize int) (*SourceLifecycle, error) {
	if db == nil || lifecycle == nil || lifecycle.db != db || lifecycle.port == nil || batchSize <= 0 || batchSize > 1000 {
		return nil, fmt.Errorf("%w: invalid Export source lifecycle dependencies", backupasset.ErrInvalidState)
	}
	sourcePort, ok := lifecycle.port.(exportSourcePrepareLifecyclePort)
	if !ok {
		return nil, fmt.Errorf("%w: Export source lifecycle port cannot release an exact source", backupasset.ErrInvalidState)
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &SourceLifecycle{db: db, lifecycle: lifecycle, sourcePort: sourcePort, now: now, batchSize: batchSize}, nil
}

func (owner *SourceLifecycle) ExpireRecoveryPoint(ctx context.Context, request backupasset.SourceLifecycleRequest) error {
	if owner == nil || owner.db == nil {
		return fmt.Errorf("%w: Export source lifecycle is unavailable", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := owner.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := backupasset.ValidateSourceLifecycleAttemptTx(ctx, tx, request); err != nil {
			return err
		}
		return owner.validateRepresentationsTx(ctx, tx, request.RecoveryPointID)
	}); err != nil {
		return err
	}
	cursor := ""
	for {
		var jobIDs []string
		err := owner.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := backupasset.ValidateSourceLifecycleAttemptTx(ctx, tx, request); err != nil {
				return err
			}
			if err := owner.validateRepresentationsTx(ctx, tx, request.RecoveryPointID); err != nil {
				return err
			}
			query := tx.Table("backup_asset_export_jobs AS jobs").Distinct("jobs.id").
				Where(`EXISTS (
					SELECT 1 FROM backup_asset_export_items AS exact_items
					WHERE exact_items.job_id = jobs.id AND exact_items.recovery_point_id = ?
				) AND EXISTS (
					SELECT 1 FROM backup_asset_export_source_leases AS exact_sources
					WHERE exact_sources.job_id = jobs.id AND exact_sources.recovery_point_id = ?
				)`, request.RecoveryPointID, request.RecoveryPointID)
			if cursor != "" {
				query = query.Where("jobs.id > ?", cursor)
			}
			return query.Order("jobs.id ASC").Limit(owner.batchSize).Pluck("jobs.id", &jobIDs).Error
		})
		if err != nil {
			return fmt.Errorf("load Export source jobs: %w", err)
		}
		if len(jobIDs) == 0 {
			return owner.proveSettled(ctx, request)
		}
		for _, jobID := range jobIDs {
			if err := ctx.Err(); err != nil {
				return err
			}
			if request.Stage == backupasset.SourceLifecyclePrepare {
				if err := owner.prepareJob(ctx, request, jobID); err != nil {
					return err
				}
			} else if err := owner.cleanupJob(ctx, request, jobID); err != nil {
				return fmt.Errorf("clean Export source job: %w", err)
			}
			cursor = jobID
		}
	}
}

func (owner *SourceLifecycle) cleanupJob(
	ctx context.Context,
	request backupasset.SourceLifecycleRequest,
	jobID string,
) error {
	resumeOriginalCleanup := false
	cleanupComplete := false
	if err := owner.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := backupasset.ValidateSourceLifecycleAttemptTx(ctx, tx, request); err != nil {
			return err
		}
		if err := owner.validateRepresentationsTx(ctx, tx, request.RecoveryPointID); err != nil {
			return err
		}
		var job model.BackupAssetExportJob
		result := tx.WithContext(ctx).Where("id = ?", jobID).Limit(1).Find(&job)
		if result.Error != nil {
			return fmt.Errorf("load Export job cleanup disposition: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return ErrNotFound
		}
		switch ExecutionState(job.ExecutionState) {
		case ExecutionFailed, ExecutionCanceled, ExecutionExpired:
			prepared, err := owner.sourcePreparedTx(ctx, tx, request, jobID)
			if err != nil {
				return err
			}
			if !prepared {
				return fmt.Errorf("%w: closed Export source release is unproven", backupasset.ErrConflict)
			}
			cleanupComplete = CleanupState(job.CleanupState) == CleanupPurged
			resumeOriginalCleanup = !cleanupComplete
		}
		return nil
	}); err != nil {
		return err
	}
	if cleanupComplete {
		return nil
	}
	if resumeOriginalCleanup {
		state, err := owner.lifecycle.Cleanup(ctx, jobID)
		if err != nil {
			return err
		}
		if state != CleanupPurged {
			return fmt.Errorf("%w: closed Export cleanup remains incomplete", backupasset.ErrConflict)
		}
		return nil
	}
	return owner.lifecycle.FailSourceExpired(ctx, jobID)
}

func (owner *SourceLifecycle) prepareJob(ctx context.Context, request backupasset.SourceLifecycleRequest, jobID string) error {
	prepared := false
	if err := owner.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := backupasset.ValidateSourceLifecycleAttemptTx(ctx, tx, request); err != nil {
			return err
		}
		if err := owner.validateRepresentationsTx(ctx, tx, request.RecoveryPointID); err != nil {
			return err
		}
		var job model.BackupAssetExportJob
		result := tx.WithContext(ctx).Where("id = ?", jobID).Limit(1).Find(&job)
		if result.Error != nil {
			return fmt.Errorf("load Export job prepare disposition: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return ErrNotFound
		}
		if CleanupState(job.CleanupState) == CleanupPurged {
			switch ExecutionState(job.ExecutionState) {
			case ExecutionFailed, ExecutionCanceled, ExecutionExpired:
				var err error
				prepared, err = owner.sourcePreparedTx(ctx, tx, request, jobID)
				if err != nil {
					return err
				}
				if !prepared {
					return fmt.Errorf("%w: pre-purged Export source release is unproven", backupasset.ErrConflict)
				}
				return nil
			}
		}
		if err := owner.lifecycle.prepareSourceExpirationTx(ctx, tx, jobID); err != nil {
			return err
		}
		var err error
		prepared, err = owner.sourcePreparedTx(ctx, tx, request, jobID)
		return err
	}); err != nil {
		return fmt.Errorf("prepare Export source state: %w", err)
	}
	if prepared {
		return nil
	}
	for _, step := range []struct {
		name string
		run  func(context.Context, string) error
	}{
		{name: "fence attempts", run: owner.lifecycle.port.FenceAttempts},
		{name: "revoke deliveries", run: owner.lifecycle.port.RevokeDeliveries},
		{name: "drain streams", run: owner.lifecycle.port.DrainStreams},
	} {
		if err := step.run(ctx, jobID); err != nil {
			return fmt.Errorf("prepare Export source %s: %w", step.name, err)
		}
	}
	if err := owner.sourcePort.ReleaseRecoveryPointSource(ctx, request, jobID); err != nil {
		return fmt.Errorf("prepare Export source release: %w", err)
	}
	return owner.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := backupasset.ValidateSourceLifecycleAttemptTx(ctx, tx, request); err != nil {
			return err
		}
		prepared, err := owner.sourcePreparedTx(ctx, tx, request, jobID)
		if err != nil {
			return err
		}
		if !prepared {
			return fmt.Errorf("%w: Export source release did not settle", backupasset.ErrConflict)
		}
		return nil
	})
}

func (owner *SourceLifecycle) sourcePreparedTx(
	ctx context.Context,
	tx *gorm.DB,
	request backupasset.SourceLifecycleRequest,
	jobID string,
) (bool, error) {
	var sources []model.BackupAssetExportSourceLease
	if err := tx.WithContext(ctx).
		Where("job_id = ? AND recovery_point_id = ?", jobID, request.RecoveryPointID).
		Order("id ASC").Limit(2).Find(&sources).Error; err != nil {
		return false, fmt.Errorf("load exact Export source lease: %w", err)
	}
	if len(sources) != 1 {
		return false, fmt.Errorf("%w: exact Export source lease is unavailable", backupasset.ErrConflict)
	}
	source := sources[0]
	var lease model.RecoveryPointLease
	result := tx.WithContext(ctx).Where("id = ?", source.LeaseID).Limit(1).Find(&lease)
	if result.Error != nil {
		return false, fmt.Errorf("load exact Export RecoveryPoint lease: %w", result.Error)
	}
	if result.RowsAffected != 1 || lease.RecoveryPointID != request.RecoveryPointID ||
		lease.HolderType != string(backupasset.LeaseHolderExportJob) || lease.OwnerID != jobID ||
		!lease.AbsoluteDeadline.UTC().Equal(source.AbsoluteDeadline.UTC()) {
		return false, ErrAttemptFenceLost
	}
	digest := sha256.Sum256([]byte(lease.FenceToken))
	if lease.AttemptID != source.LeaseAttemptID || hex.EncodeToString(digest[:]) != source.FenceHash {
		return false, ErrAttemptFenceLost
	}
	switch source.State {
	case "active":
		if lease.Status != string(backupasset.LeaseActive) && lease.Status != string(backupasset.LeaseReleased) &&
			lease.Status != string(backupasset.LeaseExpired) {
			return false, ErrAttemptFenceLost
		}
		return false, nil
	case "released":
		if lease.Status != string(backupasset.LeaseReleased) || source.ReleasedAt == nil || lease.ReleasedAt == nil {
			return false, ErrAttemptFenceLost
		}
		return true, nil
	case "expired":
		if lease.Status != string(backupasset.LeaseExpired) || source.ReleasedAt == nil {
			return false, ErrAttemptFenceLost
		}
		return true, nil
	default:
		return false, ErrAttemptFenceLost
	}
}

func (owner *SourceLifecycle) proveSettled(ctx context.Context, request backupasset.SourceLifecycleRequest) error {
	return owner.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := backupasset.ValidateSourceLifecycleAttemptTx(ctx, tx, request); err != nil {
			return err
		}
		if err := owner.validateRepresentationsTx(ctx, tx, request.RecoveryPointID); err != nil {
			return err
		}
		var liveLeases, liveSourceLeases int64
		if err := tx.Model(&model.RecoveryPointLease{}).
			Where("recovery_point_id = ? AND holder_type = ? AND status = ?", request.RecoveryPointID, backupasset.LeaseHolderExportJob, backupasset.LeaseActive).
			Count(&liveLeases).Error; err != nil {
			return fmt.Errorf("prove Export source leases released: %w", err)
		}
		if err := tx.Model(&model.BackupAssetExportSourceLease{}).
			Where("recovery_point_id = ? AND state NOT IN ?", request.RecoveryPointID, []string{"released", "expired"}).
			Count(&liveSourceLeases).Error; err != nil {
			return fmt.Errorf("prove Export source lease evidence released: %w", err)
		}
		if liveLeases != 0 || liveSourceLeases != 0 {
			return fmt.Errorf("%w: Export source lease remains live", backupasset.ErrConflict)
		}
		return nil
	})
}

func (owner *SourceLifecycle) validateRepresentationsTx(ctx context.Context, tx *gorm.DB, pointID string) error {
	var divergent int64
	result := tx.WithContext(ctx).Table("backup_asset_export_jobs AS jobs").
		Where(`(
			EXISTS (SELECT 1 FROM backup_asset_export_items AS exact_items WHERE exact_items.job_id = jobs.id AND exact_items.recovery_point_id = ?) OR
			EXISTS (SELECT 1 FROM backup_asset_export_source_leases AS exact_sources WHERE exact_sources.job_id = jobs.id AND exact_sources.recovery_point_id = ?)
		) AND (
			NOT EXISTS (SELECT 1 FROM backup_asset_export_items AS exact_items WHERE exact_items.job_id = jobs.id AND exact_items.recovery_point_id = ?) OR
			NOT EXISTS (SELECT 1 FROM backup_asset_export_source_leases AS exact_sources WHERE exact_sources.job_id = jobs.id AND exact_sources.recovery_point_id = ?) OR
			EXISTS (
				SELECT 1 FROM backup_asset_export_items AS items
				WHERE items.job_id = jobs.id AND NOT EXISTS (
					SELECT 1 FROM backup_asset_export_source_leases AS sources
					WHERE sources.job_id = jobs.id AND sources.recovery_point_id = items.recovery_point_id
				)
			) OR
			EXISTS (
				SELECT 1 FROM backup_asset_export_source_leases AS sources
				WHERE sources.job_id = jobs.id AND NOT EXISTS (
					SELECT 1 FROM backup_asset_export_items AS items
					WHERE items.job_id = jobs.id AND items.recovery_point_id = sources.recovery_point_id
				)
			)
		)`, pointID, pointID, pointID, pointID).
		Count(&divergent)
	if result.Error != nil {
		return fmt.Errorf("validate Export source representation: %w", result.Error)
	}
	if divergent != 0 {
		return fmt.Errorf("%w: Export item/source representation diverged", backupasset.ErrConflict)
	}
	return nil
}
