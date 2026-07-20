package processing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrNotDeployed = errors.New("backup asset worker is not deployed")
	ErrQueueFull   = errors.New("backup asset processing queue is full")
	ErrNoWork      = errors.New("no compatible backup asset work is available")
	ErrAttemptLost = errors.New("backup asset processing attempt lease is lost")
)

type InterestOwnerKind string

const (
	InterestWorkspace InterestOwnerKind = "workspace"
	InterestSearch    InterestOwnerKind = "search"
	InterestSystem    InterestOwnerKind = "system"
)

type InterestRemovedReason string

const (
	InterestRemovedCompleted  InterestRemovedReason = "completed"
	InterestRemovedCanceled   InterestRemovedReason = "canceled"
	InterestRemovedExpired    InterestRemovedReason = "expired"
	InterestRemovedSuperseded InterestRemovedReason = "superseded"
)

type CoordinatorConfig struct {
	QueueMax                 int
	InteractiveReservedSlots int
	BackgroundSlots          int
	PullLease                time.Duration
	AttemptTimeout           time.Duration
	RetryMax                 int
}

type InterestRequest struct {
	OwnerKind     InterestOwnerKind
	OwnerKey      string
	PriorityClass PriorityClass
	Priority      int
}

type WorkRequest struct {
	Descriptor WorkDescriptorV1
	Interest   InterestRequest
}

type WorkResult struct {
	JobID      string
	InterestID string
	WorkKey    string
	Created    bool
}

type PullRequest struct {
	WorkerID string
}

type AttemptLease struct {
	JobID                       string
	AttemptID                   string
	WorkerID                    string
	SlotClass                   SlotClass
	DescriptorCanonical         []byte
	WorkerLeaseExpiresAt        time.Time
	RecoveryPointLeaseExpiresAt time.Time
	RecoveryPointFence          backupasset.LeaseFence
}

type LeasedAttempt struct {
	Lease  AttemptLease
	Grants AttemptGrantMaterial
}

type HeartbeatRequest struct {
	AttemptID string
	WorkerID  string
}

type HeartbeatResult struct {
	WorkerLeaseExpiresAt        time.Time
	RecoveryPointLeaseExpiresAt time.Time
	EffectiveLeaseExpiresAt     time.Time
	GrantExpiresAt              time.Time
	TransitionRevision          int64
	CancelRequested             bool
	CancelReason                CancelReason
	WorkerDraining              bool
}

type AttemptTransitionRequest struct {
	JobID            string
	AttemptID        string
	WorkerID         string
	ExpectedRevision int64
	To               ProcessingState
	ErrorCode        ProcessingErrorCode
	RetryAt          *time.Time
	CancelReason     CancelReason
	SupersedeReason  SupersedeReason
	ExpiryReason     ExpiryReason
}

type AttemptTransitionResult struct {
	State           ProcessingState
	Revision        int64
	CancelRequested bool
}

type Coordinator struct {
	db           *gorm.DB
	leaseService *backupasset.LeaseService
	now          func() time.Time
	config       CoordinatorConfig
}

func NewCoordinator(db *gorm.DB, leaseService *backupasset.LeaseService, now func() time.Time, config CoordinatorConfig) (*Coordinator, error) {
	if db == nil || leaseService == nil {
		return nil, fmt.Errorf("%w: coordinator dependencies are unavailable", ErrInvalidContract)
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	if config.QueueMax <= 0 || config.InteractiveReservedSlots < 0 || config.BackgroundSlots < 0 ||
		config.InteractiveReservedSlots+config.BackgroundSlots <= 0 || config.PullLease <= 0 ||
		config.AttemptTimeout < config.PullLease || config.RetryMax < 0 || config.RetryMax > 20 {
		return nil, fmt.Errorf("%w: invalid coordinator configuration", ErrInvalidContract)
	}
	return &Coordinator{db: db, leaseService: leaseService, now: now, config: config}, nil
}

func (coordinator *Coordinator) RequestWork(ctx context.Context, request WorkRequest) (WorkResult, error) {
	if err := ValidateWorkDescriptorV1(request.Descriptor); err != nil {
		return WorkResult{}, err
	}
	if err := validateInterestRequest(request.Interest); err != nil {
		return WorkResult{}, err
	}
	workKey, err := ComputeWorkKey(request.Descriptor)
	if err != nil {
		return WorkResult{}, err
	}
	canonical, err := json.Marshal(request.Descriptor)
	if err != nil {
		return WorkResult{}, fmt.Errorf("marshal work descriptor: %w", err)
	}
	compatible, err := coordinator.hasCompatibleWorker(ctx, request.Descriptor)
	if err != nil {
		return WorkResult{}, err
	}
	if !compatible {
		return WorkResult{}, ErrNotDeployed
	}

	var result WorkResult
	err = coordinator.retryConflicts(ctx, func() error {
		return coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			job, created, err := coordinator.findOrCreateJobTx(ctx, tx, request, workKey, canonical)
			if err != nil {
				return err
			}
			interestID, err := coordinator.upsertInterestTx(ctx, tx, job.ID, request.Interest)
			if err != nil {
				return err
			}
			if err := coordinator.recomputePriorityTx(ctx, tx, job.ID); err != nil {
				return err
			}
			result = WorkResult{JobID: job.ID, InterestID: interestID, WorkKey: workKey, Created: created}
			return nil
		})
	})
	if err != nil {
		return WorkResult{}, err
	}
	return result, nil
}

func (coordinator *Coordinator) RemoveInterest(ctx context.Context, jobID string, ownerKind InterestOwnerKind, ownerKey string, reason InterestRemovedReason) error {
	if backupasset.ValidateOpaqueID(jobID) != nil || !validInterestOwner(ownerKind) || !validOwnerKey(ownerKey) || !validInterestRemoval(reason) {
		return fmt.Errorf("%w: invalid interest removal", ErrInvalidContract)
	}
	return coordinator.retryConflicts(ctx, func() error {
		return coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var job model.BackupAssetProcessingJob
			result := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", jobID).Limit(1).Find(&job)
			if result.Error != nil {
				return fmt.Errorf("load processing job: %w", result.Error)
			}
			if result.RowsAffected != 1 {
				return fmt.Errorf("%w: processing job is missing", ErrInvalidContract)
			}
			now := coordinator.utcNow()
			removed := tx.WithContext(ctx).Model(&model.BackupAssetProcessingInterest{}).
				Where("job_id = ? AND owner_kind = ? AND owner_key = ? AND active = ?", jobID, ownerKind, ownerKey, true).
				Updates(map[string]any{"active": false, "removed_reason": string(reason), "removed_at": now, "updated_at": now})
			if removed.Error != nil {
				return fmt.Errorf("remove processing interest: %w", removed.Error)
			}
			if removed.RowsAffected != 1 {
				return fmt.Errorf("%w: active processing interest is missing", ErrInvalidContract)
			}

			var remaining int64
			if err := tx.WithContext(ctx).Model(&model.BackupAssetProcessingInterest{}).
				Where("job_id = ? AND active = ?", jobID, true).Count(&remaining).Error; err != nil {
				return fmt.Errorf("count processing interests: %w", err)
			}
			if remaining > 0 {
				return coordinator.recomputePriorityTx(ctx, tx, jobID)
			}

			// Grant revocation deliberately precedes publishing cancel_requested.
			if err := tx.WithContext(ctx).Model(&model.BackupAssetProcessingGrant{}).
				Where("job_id = ? AND state IN ?", jobID, []string{"issued", "active"}).
				Updates(map[string]any{
					"activation_secret_hash": "", "state": "revoked", "revoked_at": now,
					"revocation_reason": "cancel", "updated_at": now, "version": gorm.Expr("version + 1"),
				}).Error; err != nil {
				return fmt.Errorf("revoke processing grants: %w", err)
			}
			if isTerminalState(ProcessingState(job.State)) || job.State == string(ProcessingCancelRequested) {
				return nil
			}
			nextRevision, err := ValidateTransition(TransitionRequest{
				From: ProcessingState(job.State), To: ProcessingCancelRequested,
				CurrentRevision: job.TransitionRevision, ExpectedRevision: job.TransitionRevision,
				CancelReason: CancelReasonInterestWithdrawn,
			})
			if err != nil {
				return err
			}
			updated := tx.WithContext(ctx).Model(&model.BackupAssetProcessingJob{}).
				Where("id = ? AND transition_revision = ?", job.ID, job.TransitionRevision).
				Updates(map[string]any{
					"state": string(ProcessingCancelRequested), "cancel_reason": string(CancelReasonInterestWithdrawn),
					"transition_revision": nextRevision, "updated_at": now, "version": gorm.Expr("version + 1"),
				})
			if updated.Error != nil {
				return fmt.Errorf("request processing cancellation: %w", updated.Error)
			}
			if updated.RowsAffected != 1 {
				return ErrRevisionConflict
			}
			return nil
		})
	})
}

func (coordinator *Coordinator) Pull(ctx context.Context, request PullRequest) (AttemptLease, error) {
	if backupasset.ValidateOpaqueID(request.WorkerID) != nil {
		return AttemptLease{}, fmt.Errorf("%w: invalid Worker identity", ErrInvalidContract)
	}
	var result AttemptLease
	err := coordinator.retryConflicts(ctx, func() error {
		return coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var err error
			result, err = coordinator.pullTx(ctx, tx, request)
			return err
		})
	})
	if err != nil {
		return AttemptLease{}, err
	}
	return result, nil
}

func (coordinator *Coordinator) PullAttempt(ctx context.Context, request PullRequest, grants *GrantService) (LeasedAttempt, error) {
	if backupasset.ValidateOpaqueID(request.WorkerID) != nil || grants == nil || grants.db != coordinator.db || grants.leaseService != coordinator.leaseService {
		return LeasedAttempt{}, fmt.Errorf("%w: invalid atomic pull dependencies", ErrInvalidContract)
	}
	var result LeasedAttempt
	err := coordinator.retryConflicts(ctx, func() error {
		return coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			lease, err := coordinator.pullTx(ctx, tx, request)
			if err != nil {
				return err
			}
			material, err := grants.issueAttemptGrantsTx(ctx, tx, IssueGrantsRequest{
				JobID: lease.JobID, AttemptID: lease.AttemptID, WorkerID: lease.WorkerID,
				RecoveryPointFence: lease.RecoveryPointFence,
			})
			if err != nil {
				return err
			}
			result = LeasedAttempt{Lease: lease, Grants: material}
			return nil
		})
	})
	if err != nil {
		return LeasedAttempt{}, err
	}
	return result, nil
}

func (coordinator *Coordinator) pullTx(ctx context.Context, tx *gorm.DB, request PullRequest) (AttemptLease, error) {
	worker, err := coordinator.loadReadyWorkerTx(ctx, tx, request.WorkerID)
	if err != nil {
		return AttemptLease{}, err
	}
	job, slot, err := coordinator.selectJobTx(ctx, tx, worker)
	if err != nil {
		return AttemptLease{}, err
	}
	now := coordinator.utcNow()
	workerDeadline := now.Add(coordinator.config.PullLease)
	if workerDeadline.After(job.AbsoluteDeadline.UTC()) {
		workerDeadline = job.AbsoluteDeadline.UTC()
	}
	if !workerDeadline.After(now) {
		return AttemptLease{}, fmt.Errorf("%w: processing deadline reached", ErrAttemptLost)
	}
	attemptID, err := backupasset.NewOpaqueID()
	if err != nil {
		return AttemptLease{}, err
	}
	var attemptCount int64
	if err := tx.WithContext(ctx).Model(&model.BackupAssetProcessingAttempt{}).Where("job_id = ?", job.ID).Count(&attemptCount).Error; err != nil {
		return AttemptLease{}, fmt.Errorf("count processing attempts: %w", err)
	}
	recoveryLease, err := coordinator.leaseService.AcquireTx(ctx, tx, backupasset.AcquireLeaseRequest{
		RecoveryPointID: job.RecoveryPointID, HolderType: backupasset.LeaseHolderProcessingJob,
		OwnerID: job.ID, AbsoluteDeadline: job.AbsoluteDeadline.UTC(),
	})
	if err != nil {
		return AttemptLease{}, fmt.Errorf("acquire processing RecoveryPoint lease: %w", err)
	}
	attempt := model.BackupAssetProcessingAttempt{
		ID: attemptID, JobID: job.ID, AttemptNumber: int(attemptCount) + 1, WorkerID: worker.ID,
		SlotClass: string(slot), State: "active", WorkerLeaseExpiresAt: workerDeadline, LastHeartbeatAt: now,
		RecoveryPointLeaseID: recoveryLease.ID, RecoveryPointAttemptID: recoveryLease.Fence.AttemptID,
		RecoveryPointFenceHash: hashFence(recoveryLease.Fence.FenceToken), AbsoluteDeadline: job.AbsoluteDeadline.UTC(),
		IsCurrent: true, StartedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	if err := tx.WithContext(ctx).Create(&attempt).Error; err != nil {
		return AttemptLease{}, fmt.Errorf("create processing attempt: %w", err)
	}
	nextRevision, err := ValidateTransition(TransitionRequest{
		From: ProcessingState(job.State), To: ProcessingLeased,
		CurrentRevision: job.TransitionRevision, ExpectedRevision: job.TransitionRevision,
	})
	if err != nil {
		return AttemptLease{}, err
	}
	updated := tx.WithContext(ctx).Model(&model.BackupAssetProcessingJob{}).
		Where("id = ? AND state = ? AND transition_revision = ?", job.ID, ProcessingQueued, job.TransitionRevision).
		Updates(map[string]any{
			"state": string(ProcessingLeased), "transition_revision": nextRevision, "current_attempt_id": attempt.ID,
			"started_at": now, "updated_at": now, "version": gorm.Expr("version + 1"),
		})
	if updated.Error != nil {
		return AttemptLease{}, fmt.Errorf("lease processing job: %w", updated.Error)
	}
	if updated.RowsAffected != 1 {
		return AttemptLease{}, ErrRevisionConflict
	}
	return AttemptLease{
		JobID: job.ID, AttemptID: attempt.ID, WorkerID: worker.ID, SlotClass: slot,
		DescriptorCanonical: append([]byte(nil), job.DescriptorCanonical...), WorkerLeaseExpiresAt: workerDeadline,
		RecoveryPointLeaseExpiresAt: recoveryLease.LeaseExpiresAt, RecoveryPointFence: recoveryLease.Fence,
	}, nil
}

func (coordinator *Coordinator) Heartbeat(ctx context.Context, request HeartbeatRequest) (HeartbeatResult, error) {
	return coordinator.heartbeat(ctx, request, nil)
}

func (coordinator *Coordinator) HeartbeatAttempt(ctx context.Context, request HeartbeatRequest, grants *GrantService) (HeartbeatResult, error) {
	if grants == nil || grants.db != coordinator.db || grants.leaseService != coordinator.leaseService {
		return HeartbeatResult{}, fmt.Errorf("%w: invalid atomic heartbeat dependencies", ErrInvalidContract)
	}
	return coordinator.heartbeat(ctx, request, grants)
}

func (coordinator *Coordinator) heartbeat(ctx context.Context, request HeartbeatRequest, grants *GrantService) (HeartbeatResult, error) {
	if backupasset.ValidateOpaqueID(request.AttemptID) != nil || backupasset.ValidateOpaqueID(request.WorkerID) != nil {
		return HeartbeatResult{}, fmt.Errorf("%w: invalid heartbeat identity", ErrInvalidContract)
	}
	var locator model.BackupAssetProcessingAttempt
	located := coordinator.db.WithContext(ctx).Select("id", "job_id").Where("id = ? AND worker_id = ?", request.AttemptID, request.WorkerID).Limit(1).Find(&locator)
	if located.Error != nil {
		return HeartbeatResult{}, fmt.Errorf("locate processing attempt: %w", located.Error)
	}
	if located.RowsAffected != 1 {
		return HeartbeatResult{}, ErrAttemptLost
	}
	var response HeartbeatResult
	err := coordinator.retryConflicts(ctx, func() error {
		return coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			now := coordinator.utcNow()
			var job model.BackupAssetProcessingJob
			jobResult := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("id = ? AND current_attempt_id = ? AND is_current = ?", locator.JobID, request.AttemptID, true).Limit(1).Find(&job)
			if jobResult.Error != nil {
				return fmt.Errorf("load heartbeat job state: %w", jobResult.Error)
			}
			if jobResult.RowsAffected != 1 || (ProcessingState(job.State) != ProcessingCancelRequested && !isAttemptOwnedState(ProcessingState(job.State))) {
				return ErrAttemptLost
			}
			var attempt model.BackupAssetProcessingAttempt
			attemptResult := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("id = ? AND job_id = ? AND worker_id = ?", request.AttemptID, job.ID, request.WorkerID).Limit(1).Find(&attempt)
			if attemptResult.Error != nil {
				return fmt.Errorf("load processing attempt: %w", attemptResult.Error)
			}
			if attemptResult.RowsAffected != 1 || attempt.State != "active" || !attempt.IsCurrent ||
				!now.Before(attempt.WorkerLeaseExpiresAt.UTC()) || !now.Before(attempt.AbsoluteDeadline.UTC()) {
				return ErrAttemptLost
			}
			var leaseRow model.RecoveryPointLease
			leaseResult := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("id = ?", attempt.RecoveryPointLeaseID).Limit(1).Find(&leaseRow)
			if leaseResult.Error != nil {
				return fmt.Errorf("load processing RecoveryPoint lease: %w", leaseResult.Error)
			}
			if leaseResult.RowsAffected != 1 || leaseRow.RecoveryPointID != job.RecoveryPointID ||
				leaseRow.HolderType != string(backupasset.LeaseHolderProcessingJob) || leaseRow.OwnerID != job.ID ||
				leaseRow.AttemptID != attempt.RecoveryPointAttemptID || hashFence(leaseRow.FenceToken) != attempt.RecoveryPointFenceHash {
				return ErrAttemptLost
			}
			fence := leaseFenceFromRow(leaseRow)
			recoveryLease, err := coordinator.leaseService.RenewTx(ctx, tx, fence)
			if err != nil {
				return fmt.Errorf("renew processing RecoveryPoint lease: %w", err)
			}
			var worker model.BackupAssetWorkerIdentity
			workerResult := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
				Select("id", "trust_state", "health_state").Where("id = ?", request.WorkerID).Limit(1).Find(&worker)
			if workerResult.Error != nil || workerResult.RowsAffected != 1 || worker.TrustState != "active" {
				return ErrAttemptLost
			}
			workerDeadline := now.Add(coordinator.config.PullLease)
			if workerDeadline.After(attempt.AbsoluteDeadline.UTC()) {
				workerDeadline = attempt.AbsoluteDeadline.UTC()
			}
			updated := tx.WithContext(ctx).Model(&model.BackupAssetProcessingAttempt{}).
				Where("id = ? AND worker_id = ? AND state = ? AND is_current = ? AND worker_lease_expires_at = ?",
					attempt.ID, attempt.WorkerID, "active", true, attempt.WorkerLeaseExpiresAt).
				Updates(map[string]any{"worker_lease_expires_at": workerDeadline, "last_heartbeat_at": now, "updated_at": now})
			if updated.Error != nil {
				return fmt.Errorf("renew processing Worker lease: %w", updated.Error)
			}
			if updated.RowsAffected != 1 {
				return ErrAttemptLost
			}
			workerUpdate := tx.WithContext(ctx).Model(&model.BackupAssetWorkerIdentity{}).
				Where("id = ? AND trust_state = ?", request.WorkerID, "active").Updates(map[string]any{"last_seen_at": now, "updated_at": now})
			if workerUpdate.Error != nil {
				return fmt.Errorf("record Worker heartbeat: %w", workerUpdate.Error)
			}
			if workerUpdate.RowsAffected != 1 {
				return ErrAttemptLost
			}
			effectiveLeaseExpiresAt := minimumTime(workerDeadline, recoveryLease.LeaseExpiresAt)
			grantExpiresAt := effectiveLeaseExpiresAt
			if grants != nil {
				grantExpiresAt, err = grants.renewAttemptGrantsTx(ctx, tx, attempt.ID, effectiveLeaseExpiresAt)
				if err != nil {
					return fmt.Errorf("renew processing grant authority: %w", err)
				}
			}
			response = HeartbeatResult{
				WorkerLeaseExpiresAt: workerDeadline, RecoveryPointLeaseExpiresAt: recoveryLease.LeaseExpiresAt,
				EffectiveLeaseExpiresAt: effectiveLeaseExpiresAt, GrantExpiresAt: grantExpiresAt,
				TransitionRevision: job.TransitionRevision,
				CancelRequested:    ProcessingState(job.State) == ProcessingCancelRequested,
				CancelReason:       CancelReason(job.CancelReason), WorkerDraining: worker.HealthState == "draining",
			}
			return nil
		})
	})
	if err != nil {
		return HeartbeatResult{}, err
	}
	return response, nil
}

func (coordinator *Coordinator) TransitionAttempt(ctx context.Context, request AttemptTransitionRequest) (AttemptTransitionResult, error) {
	if backupasset.ValidateOpaqueID(request.JobID) != nil || backupasset.ValidateOpaqueID(request.AttemptID) != nil ||
		backupasset.ValidateOpaqueID(request.WorkerID) != nil || request.ExpectedRevision <= 0 || !workerTransitionTarget(request.To) {
		return AttemptTransitionResult{}, fmt.Errorf("%w: invalid Worker transition", ErrInvalidContract)
	}
	var response AttemptTransitionResult
	err := coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job model.BackupAssetProcessingJob
		result := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", request.JobID).Limit(1).Find(&job)
		if result.Error != nil {
			return fmt.Errorf("load processing transition job: %w", result.Error)
		}
		if result.RowsAffected != 1 || !job.IsCurrent || job.CurrentAttemptID == nil || *job.CurrentAttemptID != request.AttemptID {
			return ErrAttemptLost
		}
		if job.TransitionRevision != request.ExpectedRevision {
			return ErrRevisionConflict
		}
		var attempt model.BackupAssetProcessingAttempt
		result = tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND job_id = ? AND worker_id = ?", request.AttemptID, request.JobID, request.WorkerID).Limit(1).Find(&attempt)
		now := coordinator.utcNow()
		if result.Error != nil {
			return fmt.Errorf("load processing transition attempt: %w", result.Error)
		}
		if result.RowsAffected != 1 || attempt.State != "active" || !attempt.IsCurrent ||
			!now.Before(attempt.WorkerLeaseExpiresAt.UTC()) || !now.Before(attempt.AbsoluteDeadline.UTC()) {
			return ErrAttemptLost
		}
		var lease model.RecoveryPointLease
		result = tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", attempt.RecoveryPointLeaseID).Limit(1).Find(&lease)
		if result.Error != nil {
			return fmt.Errorf("load processing transition lease: %w", result.Error)
		}
		if result.RowsAffected != 1 || lease.AttemptID != attempt.RecoveryPointAttemptID || hashFence(lease.FenceToken) != attempt.RecoveryPointFenceHash {
			return ErrAttemptLost
		}
		fence := leaseFenceFromRow(lease)
		if err := coordinator.leaseService.ValidateFenceTx(ctx, tx, fence); err != nil {
			return errors.Join(ErrAttemptLost, err)
		}
		effective := request
		if request.To == ProcessingRetryWait && job.RetryCount >= coordinator.config.RetryMax {
			effective.To = ProcessingFailed
			effective.RetryAt = nil
		}
		nextRevision, err := ValidateTransition(TransitionRequest{
			From: ProcessingState(job.State), To: effective.To,
			CurrentRevision: job.TransitionRevision, ExpectedRevision: request.ExpectedRevision,
			ErrorCode: effective.ErrorCode, RetryAt: effective.RetryAt,
			RetryExhausted: request.To == ProcessingRetryWait && effective.To == ProcessingFailed,
			CancelReason:   effective.CancelReason, SupersedeReason: effective.SupersedeReason, ExpiryReason: effective.ExpiryReason,
		})
		if err != nil {
			return err
		}
		updates := map[string]any{
			"state": string(effective.To), "transition_revision": nextRevision,
			"updated_at": now, "version": gorm.Expr("version + 1"),
		}
		if attemptTerminalTransition(effective.To) {
			if err := revokeAttemptGrantsTx(tx, attempt.ID, now, transitionRevocationReason(effective)); err != nil {
				return err
			}
			attemptState, outcome := transitionAttemptOutcome(effective)
			if err := finishAttemptTx(tx, attempt.ID, now, attemptState, outcome); err != nil {
				return err
			}
			if err := coordinator.leaseService.ReleaseTx(ctx, tx, fence); err != nil {
				return err
			}
			updates["current_attempt_id"] = nil
			switch effective.To {
			case ProcessingRetryWait:
				updates["error_code"] = string(effective.ErrorCode)
				updates["retry_count"] = job.RetryCount + 1
				updates["retry_at"] = effective.RetryAt.UTC()
			case ProcessingFailed:
				updates["error_code"] = string(effective.ErrorCode)
				updates["is_current"] = false
				updates["finished_at"] = now
			case ProcessingCanceled:
				updates["cancel_reason"] = string(request.CancelReason)
				updates["is_current"] = false
				updates["finished_at"] = now
			case ProcessingSuperseded:
				updates["supersede_reason"] = string(request.SupersedeReason)
				updates["is_current"] = false
				updates["finished_at"] = now
			case ProcessingExpired:
				updates["expiry_reason"] = string(request.ExpiryReason)
				updates["is_current"] = false
				updates["finished_at"] = now
			}
		}
		updated := tx.WithContext(ctx).Model(&model.BackupAssetProcessingJob{}).
			Where("id = ? AND transition_revision = ?", job.ID, job.TransitionRevision).Updates(updates)
		if updated.Error != nil {
			return fmt.Errorf("advance processing transition: %w", updated.Error)
		}
		if updated.RowsAffected != 1 {
			return ErrRevisionConflict
		}
		if effective.To == ProcessingFailed {
			category, _ := effective.ErrorCode.Category()
			if category == ContractSecurityError {
				if err := quarantineWorkerTx(tx, request.WorkerID, effective.ErrorCode, now); err != nil {
					return err
				}
			}
		}
		response = AttemptTransitionResult{State: effective.To, Revision: nextRevision, CancelRequested: effective.To == ProcessingCancelRequested}
		return nil
	})
	if err != nil {
		return AttemptTransitionResult{}, err
	}
	return response, nil
}

func (coordinator *Coordinator) findOrCreateJobTx(ctx context.Context, tx *gorm.DB, request WorkRequest, workKey string, canonical []byte) (model.BackupAssetProcessingJob, bool, error) {
	var existing model.BackupAssetProcessingJob
	result := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("work_key = ? AND is_current = ?", workKey, true).Limit(1).Find(&existing)
	if result.Error != nil {
		return model.BackupAssetProcessingJob{}, false, fmt.Errorf("load current processing job: %w", result.Error)
	}
	if result.RowsAffected == 1 {
		return existing, false, nil
	}
	var queued int64
	if err := tx.WithContext(ctx).Model(&model.BackupAssetProcessingJob{}).
		Where("is_current = ?", true).Count(&queued).Error; err != nil {
		return model.BackupAssetProcessingJob{}, false, fmt.Errorf("count processing queue: %w", err)
	}
	if queued >= int64(coordinator.config.QueueMax) {
		return model.BackupAssetProcessingJob{}, false, ErrQueueFull
	}
	id, err := backupasset.NewOpaqueID()
	if err != nil {
		return model.BackupAssetProcessingJob{}, false, err
	}
	now := coordinator.utcNow()
	job := model.BackupAssetProcessingJob{
		ID: id, WorkKey: workKey, DescriptorSchemaVersion: request.Descriptor.SchemaVersion,
		DescriptorCanonical: append([]byte(nil), canonical...), RecoveryPointID: request.Descriptor.Source.RecoveryPointID,
		CatalogGenerationID: request.Descriptor.CatalogGenerationID, EntryID: request.Descriptor.Source.EntryID,
		SourceFingerprint: request.Descriptor.SourceFingerprint, EntryFingerprint: request.Descriptor.EntryFingerprint,
		ProviderCapabilityRevision: request.Descriptor.ProviderCapabilityRevision, Capability: request.Descriptor.Capability,
		CapabilitySchema: request.Descriptor.CapabilitySchema, PipelineFingerprint: request.Descriptor.PipelineFingerprint,
		OutputProfile: request.Descriptor.OutputProfile, SecurityPolicyRevision: request.Descriptor.SecurityPolicyRevision,
		PriorityClass: string(request.Interest.PriorityClass), EffectivePriority: request.Interest.Priority,
		State: string(ProcessingQueued), TransitionRevision: 1, IsCurrent: true,
		QueuedAt: now, AbsoluteDeadline: now.Add(coordinator.config.AttemptTimeout), CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	if err := tx.WithContext(ctx).Create(&job).Error; err != nil {
		return model.BackupAssetProcessingJob{}, false, fmt.Errorf("create processing job: %w", err)
	}
	return job, true, nil
}

func (coordinator *Coordinator) upsertInterestTx(ctx context.Context, tx *gorm.DB, jobID string, request InterestRequest) (string, error) {
	var existing model.BackupAssetProcessingInterest
	result := tx.WithContext(ctx).Where("job_id = ? AND owner_kind = ? AND owner_key = ? AND active = ?", jobID, request.OwnerKind, request.OwnerKey, true).
		Limit(1).Find(&existing)
	if result.Error != nil {
		return "", fmt.Errorf("load processing interest: %w", result.Error)
	}
	now := coordinator.utcNow()
	if result.RowsAffected == 1 {
		if err := tx.WithContext(ctx).Model(&existing).Updates(map[string]any{
			"priority_class": string(request.PriorityClass), "priority": request.Priority, "updated_at": now,
		}).Error; err != nil {
			return "", fmt.Errorf("update processing interest: %w", err)
		}
		return existing.ID, nil
	}
	id, err := backupasset.NewOpaqueID()
	if err != nil {
		return "", err
	}
	interest := model.BackupAssetProcessingInterest{
		ID: id, JobID: jobID, OwnerKind: string(request.OwnerKind), OwnerKey: request.OwnerKey,
		PriorityClass: string(request.PriorityClass), Priority: request.Priority, Active: true,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := tx.WithContext(ctx).Create(&interest).Error; err != nil {
		return "", fmt.Errorf("create processing interest: %w", err)
	}
	return id, nil
}

func (coordinator *Coordinator) recomputePriorityTx(ctx context.Context, tx *gorm.DB, jobID string) error {
	var interests []model.BackupAssetProcessingInterest
	if err := tx.WithContext(ctx).Where("job_id = ? AND active = ?", jobID, true).Find(&interests).Error; err != nil {
		return fmt.Errorf("load active processing interests: %w", err)
	}
	if len(interests) == 0 {
		return nil
	}
	priorityClass := PriorityBackground
	priority := -1
	for _, interest := range interests {
		candidateClass := PriorityClass(interest.PriorityClass)
		if candidateClass == PriorityInteractive && priorityClass != PriorityInteractive {
			priorityClass = PriorityInteractive
			priority = interest.Priority
			continue
		}
		if candidateClass == priorityClass && interest.Priority > priority {
			priority = interest.Priority
		}
	}
	if err := tx.WithContext(ctx).Model(&model.BackupAssetProcessingJob{}).Where("id = ?", jobID).
		Updates(map[string]any{
			"priority_class": string(priorityClass), "effective_priority": priority,
			"updated_at": coordinator.utcNow(), "version": gorm.Expr("version + 1"),
		}).Error; err != nil {
		return fmt.Errorf("recompute processing priority: %w", err)
	}
	return nil
}

func (coordinator *Coordinator) hasCompatibleWorker(ctx context.Context, descriptor WorkDescriptorV1) (bool, error) {
	var count int64
	err := coordinator.db.WithContext(ctx).Table("backup_asset_worker_identities AS workers").
		Joins("JOIN backup_asset_worker_capabilities AS capabilities ON capabilities.worker_id = workers.id").
		Where(`workers.trust_state = ? AND workers.health_state = ? AND capabilities.health_state = ?
			AND capabilities.capability = ? AND capabilities.capability_schema = ?
			AND capabilities.pipeline_fingerprint = ? AND capabilities.output_profile = ?`,
			"active", "ready", "ready", descriptor.Capability, descriptor.CapabilitySchema,
			descriptor.PipelineFingerprint, descriptor.OutputProfile).
		Count(&count).Error
	if err != nil {
		return false, fmt.Errorf("check compatible Workers: %w", err)
	}
	return count > 0, nil
}

func (coordinator *Coordinator) loadReadyWorkerTx(ctx context.Context, tx *gorm.DB, workerID string) (model.BackupAssetWorkerIdentity, error) {
	var worker model.BackupAssetWorkerIdentity
	result := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND trust_state = ? AND health_state = ?", workerID, "active", "ready").Limit(1).Find(&worker)
	if result.Error != nil {
		return model.BackupAssetWorkerIdentity{}, fmt.Errorf("load ready Worker: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return model.BackupAssetWorkerIdentity{}, ErrNotDeployed
	}
	return worker, nil
}

func (coordinator *Coordinator) selectJobTx(ctx context.Context, tx *gorm.DB, worker model.BackupAssetWorkerIdentity) (model.BackupAssetProcessingJob, SlotClass, error) {
	var jobs []model.BackupAssetProcessingJob
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
		Where("state = ? AND is_current = ?", ProcessingQueued, true).Find(&jobs).Error; err != nil {
		return model.BackupAssetProcessingJob{}, "", fmt.Errorf("load processing queue: %w", err)
	}
	var capabilities []model.BackupAssetWorkerCapability
	if err := tx.WithContext(ctx).Where("worker_id = ? AND health_state = ?", worker.ID, "ready").Find(&capabilities).Error; err != nil {
		return model.BackupAssetProcessingJob{}, "", fmt.Errorf("load Worker capabilities: %w", err)
	}
	compatible := make(map[string]bool, len(capabilities))
	for _, capability := range capabilities {
		compatible[capabilityKey(capability.Capability, capability.CapabilitySchema, capability.PipelineFingerprint, capability.OutputProfile)] = true
	}
	usage, err := coordinator.workerSlotUsageTx(ctx, tx, worker)
	if err != nil {
		return model.BackupAssetProcessingJob{}, "", err
	}
	candidates := make([]QueueCandidate, 0, len(jobs))
	byID := make(map[string]model.BackupAssetProcessingJob, len(jobs))
	for _, job := range jobs {
		if compatible[capabilityKey(job.Capability, job.CapabilitySchema, job.PipelineFingerprint, job.OutputProfile)] {
			candidates = append(candidates, QueueCandidate{ID: job.ID, PriorityClass: PriorityClass(job.PriorityClass), Priority: job.EffectivePriority, QueuedUnixNano: job.QueuedAt.UTC().UnixNano()})
			byID[job.ID] = job
		}
	}
	SortQueueCandidates(candidates)
	for _, candidate := range candidates {
		slot, ok := ChooseSlot(usage, candidate.PriorityClass)
		if ok {
			return byID[candidate.ID], slot, nil
		}
	}
	return model.BackupAssetProcessingJob{}, "", ErrNoWork
}

func (coordinator *Coordinator) workerSlotUsageTx(ctx context.Context, tx *gorm.DB, worker model.BackupAssetWorkerIdentity) (SlotUsage, error) {
	interactiveTotal := worker.InteractiveSlots
	if interactiveTotal > coordinator.config.InteractiveReservedSlots {
		interactiveTotal = coordinator.config.InteractiveReservedSlots
	}
	backgroundTotal := worker.BackgroundSlots
	if backgroundTotal > coordinator.config.BackgroundSlots {
		backgroundTotal = coordinator.config.BackgroundSlots
	}
	var attempts []model.BackupAssetProcessingAttempt
	if err := tx.WithContext(ctx).Where("worker_id = ? AND state = ? AND is_current = ?", worker.ID, "active", true).Find(&attempts).Error; err != nil {
		return SlotUsage{}, fmt.Errorf("load Worker slot usage: %w", err)
	}
	usage := SlotUsage{InteractiveTotal: interactiveTotal, BackgroundTotal: backgroundTotal}
	for _, attempt := range attempts {
		switch SlotClass(attempt.SlotClass) {
		case SlotInteractive:
			usage.InteractiveUsed++
		case SlotBackground, SlotBackgroundBorrowed:
			usage.BackgroundUsed++
		default:
			return SlotUsage{}, fmt.Errorf("%w: invalid persisted slot class", ErrInvalidContract)
		}
	}
	return usage, nil
}

func (coordinator *Coordinator) retryConflicts(ctx context.Context, operation func() error) error {
	var lastErr error
	for attempt := 0; attempt < 20; attempt++ {
		if err := operation(); err != nil {
			lastErr = err
			if !retryableCoordinatorConflict(err) {
				return err
			}
			select {
			case <-ctx.Done():
				return fmt.Errorf("processing database conflict: %w", ctx.Err())
			case <-time.After(time.Duration(attempt+1) * time.Millisecond):
			}
			continue
		}
		return nil
	}
	return fmt.Errorf("processing database conflict after retries: %w", lastErr)
}

func validateInterestRequest(request InterestRequest) error {
	if !validInterestOwner(request.OwnerKind) || !validOwnerKey(request.OwnerKey) ||
		(request.PriorityClass != PriorityInteractive && request.PriorityClass != PriorityBackground) ||
		request.Priority < 0 || request.Priority > 1000 {
		return fmt.Errorf("%w: invalid processing interest", ErrInvalidContract)
	}
	return nil
}

func validInterestOwner(value InterestOwnerKind) bool {
	return value == InterestWorkspace || value == InterestSearch || value == InterestSystem
}

func validInterestRemoval(value InterestRemovedReason) bool {
	return value == InterestRemovedCompleted || value == InterestRemovedCanceled || value == InterestRemovedExpired || value == InterestRemovedSuperseded
}

func validOwnerKey(value string) bool {
	return strings.TrimSpace(value) == value && value != "" && len(value) <= 128 && !strings.ContainsAny(value, "\r\n\x00")
}

func capabilityKey(capability, schema, pipeline, profile string) string {
	return capability + "\x00" + schema + "\x00" + pipeline + "\x00" + profile
}

func hashFence(fence string) string {
	digest := sha256.Sum256([]byte(fence))
	return hex.EncodeToString(digest[:])
}

func retryableCoordinatorConflict(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "database is locked") || strings.Contains(message, "database table is locked") ||
		strings.Contains(message, "unique constraint") || strings.Contains(message, "duplicate key") ||
		strings.Contains(message, "sqlstate 23505") || strings.Contains(message, "serialization failure") ||
		strings.Contains(message, "deadlock detected")
}

func workerTransitionTarget(state ProcessingState) bool {
	switch state {
	case ProcessingFetching, ProcessingMaterializing, ProcessingProcessing, ProcessingUploading,
		ProcessingRetryWait, ProcessingCanceled, ProcessingFailed, ProcessingSuperseded, ProcessingExpired:
		return true
	default:
		return false
	}
}

func attemptTerminalTransition(state ProcessingState) bool {
	switch state {
	case ProcessingRetryWait, ProcessingCanceled, ProcessingFailed, ProcessingSuperseded, ProcessingExpired:
		return true
	default:
		return false
	}
}

func transitionRevocationReason(request AttemptTransitionRequest) string {
	if request.To == ProcessingFailed {
		if category, err := request.ErrorCode.Category(); err == nil && category == ContractSecurityError {
			return "quarantine"
		}
	}
	switch request.To {
	case ProcessingCanceled:
		return "cancel"
	case ProcessingSuperseded:
		return "source_changed"
	case ProcessingExpired:
		return "expired"
	default:
		return "lease_lost"
	}
}

func transitionAttemptOutcome(request AttemptTransitionRequest) (string, string) {
	switch request.To {
	case ProcessingCanceled:
		return "canceled", ""
	case ProcessingSuperseded:
		return "superseded", ""
	case ProcessingExpired:
		return "expired", ""
	default:
		return "failed", string(request.ErrorCode)
	}
}

func revokeAttemptGrantsTx(tx *gorm.DB, attemptID string, now time.Time, reason string) error {
	if err := tx.Model(&model.BackupAssetProcessingGrantRequest{}).
		Where("grant_id IN (?) AND state IN ?", tx.Model(&model.BackupAssetProcessingGrant{}).Select("id").Where("attempt_id = ?", attemptID), []string{"reserved", "streaming"}).
		Updates(map[string]any{
			"state": string(GrantRequestReconciled), "provider_bytes": gorm.Expr("reserved_bytes"),
			"failure_code": string(GrantFailureReconciledCrash), "finished_at": now, "updated_at": now,
		}).Error; err != nil {
		return fmt.Errorf("reconcile transitioned grant reservations: %w", err)
	}
	if err := tx.Model(&model.BackupAssetProcessingGrant{}).Where("attempt_id = ? AND state IN ?", attemptID, []string{"issued", "active"}).
		Updates(map[string]any{
			"state": string(GrantRevoked), "activation_secret_hash": "", "revoked_at": now,
			"revocation_reason": reason, "consumed_bytes": gorm.Expr("consumed_bytes + reserved_bytes"),
			"reserved_bytes": 0, "in_flight": 0, "updated_at": now, "version": gorm.Expr("version + 1"),
		}).Error; err != nil {
		return fmt.Errorf("revoke transitioned attempt grants: %w", err)
	}
	return nil
}

func quarantineWorkerTx(tx *gorm.DB, workerID string, code ProcessingErrorCode, now time.Time) error {
	if err := tx.Model(&model.BackupAssetProcessingGrant{}).Where("worker_id = ? AND state IN ?", workerID, []string{"issued", "active"}).
		Updates(map[string]any{
			"state": string(GrantRevoked), "activation_secret_hash": "", "revoked_at": now,
			"revocation_reason": "quarantine", "updated_at": now, "version": gorm.Expr("version + 1"),
		}).Error; err != nil {
		return fmt.Errorf("revoke quarantined Worker grants: %w", err)
	}
	updated := tx.Model(&model.BackupAssetWorkerIdentity{}).Where("id = ? AND trust_state = ?", workerID, "active").
		Updates(map[string]any{
			"trust_state": "quarantined", "health_state": "draining", "quarantine_code": string(code), "updated_at": now,
		})
	if updated.Error != nil || updated.RowsAffected != 1 {
		return errors.Join(ErrWorkerUnauthenticated, updated.Error)
	}
	return nil
}

func (coordinator *Coordinator) utcNow() time.Time { return coordinator.now().UTC() }
