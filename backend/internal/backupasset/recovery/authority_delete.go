package recovery

import (
	"context"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type recoveryDeleteAuthorityState struct {
	job       model.BackupAssetRecoveryJob
	preflight model.BackupAssetRecoveryPreflight
	required  model.BackupAssetRecoveryCheckpoint
	claim     RecoveryWorkerClaim
	target    recoveryAuthorityTargetExpectation
}

// Discover the unique active claim, then authenticate it and the requested
// history tail under the same ordered locks used by worker authority issuance.
func loadRecoveryDeleteAuthorityTx(ctx context.Context, tx *gorm.DB, request RecoveryAuthorizationRequest, now time.Time) (recoveryDeleteAuthorityState, error) {
	var state recoveryDeleteAuthorityState
	if tx == nil || now.IsZero() || request.Operation != AuthorizationReceiptDeleteAuthorize ||
		!validOpaqueID(request.JobID) || !validOpaqueID(request.AttemptID) || !validOpaqueID(request.CheckpointID) {
		return state, ErrAuthorizationDenied
	}
	var job model.BackupAssetRecoveryJob
	loaded := tx.WithContext(ctx).Where("id = ? AND plan_id = ?", request.JobID, request.PlanID).Limit(1).Find(&job)
	if loaded.Error != nil {
		return state, loaded.Error
	}
	if loaded.RowsAffected != 1 || job.State != string(JobStateRunning) || TargetMode(job.TargetMode) != TargetModeInPlace {
		return state, ErrAuthorizationDenied
	}
	var attempts []model.BackupAssetRecoveryAttempt
	if err := tx.WithContext(ctx).Where("job_id = ? AND state = ?", job.ID, AttemptStateRunning).Limit(2).Find(&attempts).Error; err != nil {
		return state, err
	}
	if len(attempts) != 1 || attempts[0].ID != request.AttemptID || !attempts[0].MutationArmed || attempts[0].LeaseExpiresAt == nil {
		return state, ErrAuthorizationDenied
	}
	attempt := attempts[0]
	var nodes []model.BackupAssetRecoveryNodeLease
	if err := tx.WithContext(ctx).Where("job_id = ? AND attempt_id = ? AND state = ?", job.ID, attempt.ID, "active").Limit(2).Find(&nodes).Error; err != nil {
		return state, err
	}
	var sources []model.RecoveryPointLease
	if err := tx.WithContext(ctx).Where("holder_type = ? AND owner_id = ? AND attempt_id = ? AND status = ?",
		backupasset.LeaseHolderRecoveryJob, job.ID, attempt.ID, backupasset.LeaseActive).Limit(2).Find(&sources).Error; err != nil {
		return state, err
	}
	if len(nodes) != 1 || len(sources) != 1 {
		return state, ErrAuthorizationDenied
	}
	claim := RecoveryWorkerClaim{JobID: job.ID, AttemptID: attempt.ID, NodeLeaseID: nodes[0].ID, WorkerID: attempt.OwnerID,
		AttemptFence: attempt.Fence, NodeFence: nodes[0].Fence, TransitionRevision: job.TransitionRevision,
		LeaseExpiresAt: attempt.LeaseExpiresAt.UTC(), AbsoluteDeadline: sources[0].AbsoluteDeadline.UTC(), SourceFence: recoverySourceFence(sources[0])}
	target, err := loadRecoveryAuthorityTargetExpectationTx(ctx, tx, claim, recoveryAuthorityTargetIntent{phase: recoveryAuthorityTargetCheckpointed}, now)
	if err != nil {
		return state, err
	}
	locked := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).Session(&gorm.Session{})
	loaded = locked.Where("id = ? AND plan_id = ?", job.PreflightID, job.PlanID).Limit(1).Find(&state.preflight)
	if loaded.Error != nil {
		return state, loaded.Error
	}
	if loaded.RowsAffected != 1 {
		return state, ErrAuthorizationDenied
	}
	loaded = locked.Where("job_id = ?", job.ID).Order("sequence DESC").Limit(1).Find(&state.required)
	if loaded.Error != nil {
		return state, loaded.Error
	}
	required := state.required
	if loaded.RowsAffected != 1 || required.ID != request.CheckpointID || required.AttemptID != claim.AttemptID ||
		required.AttemptFence != claim.AttemptFence || required.NodeFence != claim.NodeFence ||
		required.Phase != string(CheckpointPhaseDeleteAuthorityRequired) || required.DeleteAuthorityExpiresAt == nil ||
		!now.Before(required.DeleteAuthorityExpiresAt.UTC()) {
		return state, ErrAuthorizationDenied
	}
	state.job, state.claim, state.target = job, claim, target
	return state, nil
}
