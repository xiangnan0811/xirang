package recovery

import (
	"context"
	"fmt"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

// RecoverySourceJobCanceler is the Recovery-owned, source-scoped handoff for
// terminalizing one active source job. Source lifecycle never owns result or
// workspace cleanup state.
type RecoverySourceJobCanceler interface {
	CancelRecoveryPoint(context.Context, backupasset.SourceLifecycleRequest, string) error
}

// SourceLifecycle fences only Recovery jobs interested in an exact source
// RecoveryPoint. RecoveryResult and workspace cleanup remain independently
// owned by ResultLifecycleService.
type SourceLifecycle struct {
	db        *gorm.DB
	canceler  RecoverySourceJobCanceler
	batchSize int
}

func NewSourceLifecycle(db *gorm.DB, canceler RecoverySourceJobCanceler, batchSize int) (*SourceLifecycle, error) {
	if db == nil || canceler == nil || batchSize <= 0 || batchSize > 1000 {
		return nil, fmt.Errorf("%w: invalid Recovery source lifecycle dependencies", backupasset.ErrInvalidState)
	}
	return &SourceLifecycle{db: db, canceler: canceler, batchSize: batchSize}, nil
}

func (owner *SourceLifecycle) CancelRecoveryPointInterests(
	ctx context.Context,
	request backupasset.SourceLifecycleRequest,
) error {
	if owner == nil || owner.db == nil || owner.canceler == nil {
		return fmt.Errorf("%w: Recovery source lifecycle is unavailable", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cursor := ""
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		jobIDs, err := owner.activeSourceJobIDs(ctx, request, cursor)
		if err != nil {
			return err
		}
		if len(jobIDs) == 0 {
			return owner.proveSettled(ctx, request)
		}
		for _, jobID := range jobIDs {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := owner.canceler.CancelRecoveryPoint(ctx, request, jobID); err != nil {
				return fmt.Errorf("cancel Recovery source job: %w", err)
			}
			cursor = jobID
		}
	}
}

func (owner *SourceLifecycle) activeSourceJobIDs(
	ctx context.Context,
	request backupasset.SourceLifecycleRequest,
	cursor string,
) ([]string, error) {
	var jobIDs []string
	err := owner.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := backupasset.ValidateSourceLifecycleAttemptTx(ctx, tx, request); err != nil {
			return err
		}
		query := tx.WithContext(ctx).Table("backup_asset_recovery_jobs AS jobs").
			Select("jobs.id").
			Joins("JOIN backup_asset_recovery_plans AS plans ON plans.id = jobs.plan_id").
			Where("plans.recovery_point_id = ? AND jobs.state IN ?", request.RecoveryPointID, []string{
				string(JobStateQueued), string(JobStateRunning), string(JobStateVerifying), string(JobStateCancelRequested),
			})
		if cursor != "" {
			query = query.Where("jobs.id > ?", cursor)
		}
		return query.Order("jobs.id ASC").Limit(owner.batchSize).Pluck("jobs.id", &jobIDs).Error
	})
	if err != nil {
		return nil, fmt.Errorf("load Recovery source jobs: %w", err)
	}
	return jobIDs, nil
}

func (owner *SourceLifecycle) proveSettled(
	ctx context.Context,
	request backupasset.SourceLifecycleRequest,
) error {
	return owner.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := backupasset.ValidateSourceLifecycleAttemptTx(ctx, tx, request); err != nil {
			return err
		}
		var activeJobs int64
		if err := tx.WithContext(ctx).Table("backup_asset_recovery_jobs AS jobs").
			Joins("JOIN backup_asset_recovery_plans AS plans ON plans.id = jobs.plan_id").
			Where("plans.recovery_point_id = ? AND jobs.state IN ?", request.RecoveryPointID, []string{
				string(JobStateQueued), string(JobStateRunning), string(JobStateVerifying), string(JobStateCancelRequested),
			}).Count(&activeJobs).Error; err != nil {
			return fmt.Errorf("prove Recovery source jobs settled: %w", err)
		}
		if activeJobs != 0 {
			return fmt.Errorf("%w: Recovery source job remains active", backupasset.ErrConflict)
		}
		var liveLeases int64
		if err := tx.WithContext(ctx).Model(&model.RecoveryPointLease{}).
			Where("recovery_point_id = ? AND holder_type = ? AND status = ?", request.RecoveryPointID, backupasset.LeaseHolderRecoveryJob, backupasset.LeaseActive).
			Count(&liveLeases).Error; err != nil {
			return fmt.Errorf("prove Recovery source leases settled: %w", err)
		}
		if liveLeases != 0 {
			return fmt.Errorf("%w: Recovery source lease remains live", backupasset.ErrConflict)
		}
		return nil
	})
}
