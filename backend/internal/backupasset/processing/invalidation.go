package processing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type InvalidationTarget struct {
	Capability                string
	OutputProfile             string
	ActivePipelineFingerprint string
}

type InvalidationRequest struct {
	Targets         []InvalidationTarget
	BatchSize       int
	RequeuePriority int
}

type InvalidationResult struct {
	StaleSets      int
	SupersededJobs int
	RequeuedJobs   int
	NotDeployed    int
}

type InvalidationController struct {
	db          *gorm.DB
	coordinator *Coordinator
	lifecycle   *DerivedLifecycle
	now         func() time.Time
}

func NewInvalidationController(
	db *gorm.DB,
	coordinator *Coordinator,
	lifecycle *DerivedLifecycle,
	now func() time.Time,
) (*InvalidationController, error) {
	if db == nil || coordinator == nil || lifecycle == nil || coordinator.db != db || lifecycle.db != db {
		return nil, ErrInvalidContract
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &InvalidationController{db: db, coordinator: coordinator, lifecycle: lifecycle, now: now}, nil
}

func (controller *InvalidationController) Invalidate(ctx context.Context, request InvalidationRequest) (InvalidationResult, error) {
	if controller == nil || !validInvalidationRequest(request) {
		return InvalidationResult{}, ErrInvalidContract
	}
	if ctx == nil {
		ctx = context.Background()
	}
	where, arguments := invalidationWhere("jobs", request.Targets)
	var setIDs []string
	if err := controller.db.WithContext(ctx).Table("backup_asset_derived_artifact_sets AS sets").
		Select("sets.id").
		Joins("JOIN backup_asset_processing_jobs AS jobs ON jobs.id = sets.job_id").
		Where("sets.state = ? AND ("+where+")", append([]any{"active"}, arguments...)...).
		Order("sets.id ASC").Limit(request.BatchSize).Pluck("sets.id", &setIDs).Error; err != nil {
		return InvalidationResult{}, fmt.Errorf("load affected Derived sets: %w", err)
	}
	var result InvalidationResult
	for _, setID := range setIDs {
		if err := controller.lifecycle.MarkSetStale(ctx, setID); err != nil {
			return result, err
		}
		result.StaleSets++
	}

	var jobs []model.BackupAssetProcessingJob
	if err := controller.db.WithContext(ctx).Where("is_current = ? AND ("+strings.ReplaceAll(where, "jobs.", "")+")", append([]any{true}, arguments...)...).
		Order("id ASC").Limit(request.BatchSize).Find(&jobs).Error; err != nil {
		return result, fmt.Errorf("load affected processing jobs: %w", err)
	}
	targets := make(map[string]InvalidationTarget, len(request.Targets))
	for _, target := range request.Targets {
		targets[invalidationTargetKey(target.Capability, target.OutputProfile)] = target
	}
	for _, job := range jobs {
		if isTerminalState(ProcessingState(job.State)) {
			continue
		}
		descriptor, err := DecodeWorkDescriptorV1(job.DescriptorCanonical)
		if err != nil {
			return result, ErrInvalidContract
		}
		target := targets[invalidationTargetKey(job.Capability, job.OutputProfile)]
		if target.ActivePipelineFingerprint == "" || descriptor.PipelineFingerprint == target.ActivePipelineFingerprint {
			continue
		}
		if err := controller.supersedeJob(ctx, job.ID); err != nil {
			return result, err
		}
		result.SupersededJobs++
		descriptor.PipelineFingerprint = target.ActivePipelineFingerprint
		_, err = controller.coordinator.RequestWork(ctx, WorkRequest{
			Descriptor: descriptor,
			Interest: InterestRequest{
				OwnerKind: InterestSystem, OwnerKey: invalidationOwnerKey(target),
				PriorityClass: PriorityBackground, Priority: request.RequeuePriority,
			},
		})
		if errors.Is(err, ErrNotDeployed) {
			result.NotDeployed++
			continue
		}
		if err != nil {
			return result, err
		}
		result.RequeuedJobs++
	}
	return result, nil
}

func (controller *InvalidationController) supersedeJob(ctx context.Context, jobID string) error {
	return controller.coordinator.retryConflicts(ctx, func() error {
		return controller.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var job model.BackupAssetProcessingJob
			result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", jobID).Limit(1).Find(&job)
			if result.Error != nil || result.RowsAffected != 1 {
				return errors.Join(ErrInvalidContract, result.Error)
			}
			if !job.IsCurrent || isTerminalState(ProcessingState(job.State)) {
				return nil
			}
			now := controller.now().UTC()
			if job.CurrentAttemptID != nil {
				var attempt model.BackupAssetProcessingAttempt
				result = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND job_id = ?", *job.CurrentAttemptID, job.ID).
					Limit(1).Find(&attempt)
				if result.Error != nil || result.RowsAffected != 1 || attempt.State != "active" || !attempt.IsCurrent {
					return errors.Join(ErrAttemptLost, result.Error)
				}
				if err := revokeAttemptGrantsTx(tx, attempt.ID, now, "source_changed"); err != nil {
					return err
				}
				if err := finishAttemptTx(tx, attempt.ID, now, "superseded", ""); err != nil {
					return err
				}
				var lease model.RecoveryPointLease
				result = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", attempt.RecoveryPointLeaseID).Limit(1).Find(&lease)
				if result.Error != nil || result.RowsAffected != 1 || lease.OwnerID != job.ID ||
					hashFence(lease.FenceToken) != attempt.RecoveryPointFenceHash {
					return errors.Join(ErrManifestFenceLost, result.Error)
				}
				if lease.Status == string(backupasset.LeaseActive) {
					if err := controller.coordinator.leaseService.ReleaseTx(ctx, tx, leaseFenceFromRow(lease)); err != nil {
						return err
					}
				}
			}
			if err := tx.Model(&model.BackupAssetProcessingInterest{}).Where("job_id = ? AND active = ?", job.ID, true).
				Updates(map[string]any{
					"active": false, "removed_reason": string(InterestRemovedSuperseded), "removed_at": now, "updated_at": now,
				}).Error; err != nil {
				return fmt.Errorf("supersede processing interests: %w", err)
			}
			revision, err := ValidateTransition(TransitionRequest{
				From: ProcessingState(job.State), To: ProcessingSuperseded,
				CurrentRevision: job.TransitionRevision, ExpectedRevision: job.TransitionRevision,
				SupersedeReason: SupersedeReasonPipelineChanged,
			})
			if err != nil {
				return err
			}
			updated := tx.Model(&model.BackupAssetProcessingJob{}).
				Where("id = ? AND transition_revision = ? AND is_current = ?", job.ID, job.TransitionRevision, true).
				Updates(map[string]any{
					"state": string(ProcessingSuperseded), "transition_revision": revision,
					"supersede_reason": string(SupersedeReasonPipelineChanged), "current_attempt_id": nil,
					"is_current": false, "finished_at": now, "updated_at": now, "version": gorm.Expr("version + 1"),
				})
			if updated.Error != nil || updated.RowsAffected != 1 {
				return errors.Join(ErrRevisionConflict, updated.Error)
			}
			return nil
		})
	})
}

func validInvalidationRequest(request InvalidationRequest) bool {
	if len(request.Targets) == 0 || len(request.Targets) > 64 || request.BatchSize <= 0 || request.BatchSize > 10000 ||
		request.RequeuePriority < 900 || request.RequeuePriority > 1000 {
		return false
	}
	last := ""
	for _, target := range request.Targets {
		key := invalidationTargetKey(target.Capability, target.OutputProfile)
		if target.Capability == "" || len(target.Capability) > 64 || target.OutputProfile == "" || len(target.OutputProfile) > 64 ||
			target.ActivePipelineFingerprint == "" || len(target.ActivePipelineFingerprint) > 128 || key <= last {
			return false
		}
		last = key
	}
	return true
}

func invalidationWhere(alias string, targets []InvalidationTarget) (string, []any) {
	conditions := make([]string, 0, len(targets))
	arguments := make([]any, 0, len(targets)*3)
	for range targets {
		conditions = append(conditions, alias+".capability = ? AND "+alias+".output_profile = ? AND "+alias+".pipeline_fingerprint <> ?")
	}
	for _, target := range targets {
		arguments = append(arguments, target.Capability, target.OutputProfile, target.ActivePipelineFingerprint)
	}
	return strings.Join(conditions, " OR "), arguments
}

func invalidationTargetKey(capability, profile string) string { return capability + "\x00" + profile }

func invalidationOwnerKey(target InvalidationTarget) string {
	digest := sha256.Sum256([]byte(invalidationTargetKey(target.Capability, target.OutputProfile) + "\x00" + target.ActivePipelineFingerprint))
	return "pipeline:" + hex.EncodeToString(digest[:16])
}

func SortInvalidationTargets(targets []InvalidationTarget) {
	sort.Slice(targets, func(left, right int) bool {
		return invalidationTargetKey(targets[left].Capability, targets[left].OutputProfile) <
			invalidationTargetKey(targets[right].Capability, targets[right].OutputProfile)
	})
}
