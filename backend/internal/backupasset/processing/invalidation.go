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

type invalidationJobPlan struct {
	job        model.BackupAssetProcessingJob
	descriptor WorkDescriptorV1
	target     InvalidationTarget
	requeue    bool
}

type invalidationLeasePlan struct {
	lease    backupasset.Lease
	prepared []preparedStaleDerivedSet
}

type invalidationSourceRecord struct {
	GenerationID                string
	GenerationState             string
	GenerationActive            bool
	GenerationSourceFingerprint string
	EntryRecoveryPointID        string
	EntryFingerprint            string
	PointRepositoryID           string
	PointSemantics              string
	PointState                  string
	PointSourceFingerprint      string
	PointCapabilityRevision     int64
	PointPhysicalAvailability   string
	PointRetiredAt              *time.Time
	RepositoryProvider          string
	RepositoryStatus            string
	RepositoryCapability        int64
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
	var jobs []model.BackupAssetProcessingJob
	jobArguments := []any{true, string(ProcessingSucceeded), string(ProcessingSuperseded), string(SupersedeReasonPipelineChanged)}
	jobArguments = append(jobArguments, arguments...)
	if err := controller.db.WithContext(ctx).Where(
		`(is_current = ? OR (state = ? AND current_artifact_set_id IS NOT NULL)
			OR (state = ? AND supersede_reason = ?)) AND (`+strings.ReplaceAll(where, "jobs.", "")+")",
		jobArguments...,
	).
		Order("id ASC").Limit(request.BatchSize).Find(&jobs).Error; err != nil {
		return InvalidationResult{}, fmt.Errorf("load affected processing jobs: %w", err)
	}
	targets := make(map[string]InvalidationTarget, len(request.Targets))
	for _, target := range request.Targets {
		targets[invalidationTargetKey(target.Capability, target.OutputProfile)] = target
	}
	jobPlans := make([]invalidationJobPlan, 0, len(jobs))
	for _, job := range jobs {
		descriptor, err := DecodeWorkDescriptorV1(job.DescriptorCanonical)
		if err != nil {
			return InvalidationResult{}, ErrInvalidContract
		}
		target := targets[invalidationTargetKey(job.Capability, job.OutputProfile)]
		if target.ActivePipelineFingerprint == "" || descriptor.PipelineFingerprint == target.ActivePipelineFingerprint {
			continue
		}
		state := ProcessingState(job.State)
		if isTerminalState(state) &&
			(state != ProcessingSucceeded || job.CurrentArtifactSetID == nil) &&
			(state != ProcessingSuperseded || job.SupersedeReason != string(SupersedeReasonPipelineChanged)) {
			continue
		}
		current, err := controller.descriptorStillCurrent(ctx, controller.db, descriptor)
		if err != nil {
			return InvalidationResult{}, err
		}
		descriptor.PipelineFingerprint = target.ActivePipelineFingerprint
		jobPlans = append(jobPlans, invalidationJobPlan{job: job, descriptor: descriptor, target: target, requeue: current})
	}

	var setRows []model.BackupAssetDerivedArtifactSet
	if err := controller.db.WithContext(ctx).Table("backup_asset_derived_artifact_sets AS sets").
		Select("sets.*").
		Joins("JOIN backup_asset_processing_jobs AS jobs ON jobs.id = sets.job_id").
		Where("sets.state = ? AND ("+where+")", append([]any{"active"}, arguments...)...).
		Order("sets.id ASC").Limit(request.BatchSize).Find(&setRows).Error; err != nil {
		return InvalidationResult{}, fmt.Errorf("load affected Derived sets: %w", err)
	}
	leasePlans, err := controller.prepareSetInvalidation(ctx, setRows)
	if err != nil {
		return InvalidationResult{}, err
	}
	released := false
	defer func() {
		if !released {
			controller.releaseInvalidationLeases(context.WithoutCancel(ctx), leasePlans)
		}
	}()

	var result InvalidationResult
	err = controller.coordinator.retryConflicts(ctx, func() error {
		candidate := InvalidationResult{}
		transactionErr := controller.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			for _, leasePlan := range leasePlans {
				if err := controller.coordinator.leaseService.ValidateFenceTx(ctx, tx, leasePlan.lease.Fence); err != nil {
					return err
				}
				for _, prepared := range leasePlan.prepared {
					if err := controller.lifecycle.markSetStaleFencedTx(ctx, tx, prepared); err != nil {
						return err
					}
					if prepared.initial.State == "active" {
						candidate.StaleSets++
					}
				}
			}
			for _, plan := range jobPlans {
				if !isTerminalState(ProcessingState(plan.job.State)) {
					changed, err := controller.supersedeJobTx(ctx, tx, plan.job.ID)
					if err != nil {
						return err
					}
					if changed {
						candidate.SupersededJobs++
					}
				}
				if !plan.requeue {
					continue
				}
				current, err := controller.descriptorStillCurrent(ctx, tx, plan.descriptor)
				if err != nil {
					return err
				}
				if !current {
					continue
				}
				_, err = controller.coordinator.requestWorkTx(ctx, tx, WorkRequest{
					Descriptor: plan.descriptor,
					Interest: InterestRequest{
						OwnerKind: InterestSystem, OwnerKey: invalidationOwnerKey(plan.target),
						PriorityClass: PriorityBackground, Priority: request.RequeuePriority,
					},
				})
				if errors.Is(err, ErrNotDeployed) {
					candidate.NotDeployed++
					continue
				}
				if err != nil {
					return err
				}
				candidate.RequeuedJobs++
			}
			for _, leasePlan := range leasePlans {
				if err := controller.coordinator.leaseService.ReleaseTx(ctx, tx, leasePlan.lease.Fence); err != nil {
					return err
				}
			}
			return nil
		})
		if transactionErr == nil {
			result = candidate
		}
		return transactionErr
	})
	if err != nil {
		return InvalidationResult{}, err
	}
	released = true
	return result, nil
}

func (controller *InvalidationController) descriptorStillCurrent(
	ctx context.Context,
	tx *gorm.DB,
	descriptor WorkDescriptorV1,
) (bool, error) {
	if controller == nil || tx == nil || ValidateWorkDescriptorV1(descriptor) != nil {
		return false, ErrInvalidContract
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var rows []invalidationSourceRecord
	err := tx.WithContext(ctx).Table("catalog_entries AS entries").
		Select(`generations.id AS generation_id,
			generations.state AS generation_state,
			generations.is_active AS generation_active,
			generations.source_fingerprint AS generation_source_fingerprint,
			entries.recovery_point_id AS entry_recovery_point_id,
			entries.fingerprint AS entry_fingerprint,
			points.repository_id AS point_repository_id,
			points.semantics AS point_semantics,
			points.state AS point_state,
			points.source_fingerprint AS point_source_fingerprint,
			points.capability_revision AS point_capability_revision,
			points.physical_availability AS point_physical_availability,
			points.retired_at AS point_retired_at,
			repositories.provider_kind AS repository_provider,
			repositories.status AS repository_status,
			repositories.capability_revision AS repository_capability`).
		Joins(`JOIN catalog_generations AS generations
			ON generations.id = entries.generation_id
			AND generations.recovery_point_id = entries.recovery_point_id`).
		Joins("JOIN recovery_points AS points ON points.id = entries.recovery_point_id").
		Joins("JOIN backup_repositories AS repositories ON repositories.id = points.repository_id").
		Where(`entries.generation_id = ? AND entries.recovery_point_id = ? AND entries.entry_id = ?`,
			descriptor.CatalogGenerationID, descriptor.Source.RecoveryPointID, descriptor.Source.EntryID).
		Limit(2).Scan(&rows).Error
	if err != nil {
		return false, fmt.Errorf("revalidate invalidation source: %w", err)
	}
	if len(rows) != 1 {
		return false, nil
	}
	record := rows[0]
	if record.GenerationID != descriptor.CatalogGenerationID || record.GenerationState != "complete" || !record.GenerationActive ||
		record.GenerationSourceFingerprint != descriptor.SourceFingerprint ||
		record.EntryRecoveryPointID != descriptor.Source.RecoveryPointID || record.EntryFingerprint != descriptor.EntryFingerprint ||
		record.PointRepositoryID == "" || record.PointSourceFingerprint != descriptor.SourceFingerprint ||
		record.PointCapabilityRevision != descriptor.ProviderCapabilityRevision ||
		record.RepositoryCapability != descriptor.ProviderCapabilityRevision ||
		record.PointPhysicalAvailability != string(backupasset.PhysicalOnline) || record.PointRetiredAt != nil ||
		record.RepositoryStatus != string(backupasset.RepositoryOnline) ||
		!invalidationProviderSupported(backupasset.ProviderKind(record.RepositoryProvider)) ||
		!invalidationPointCurrent(record.PointSemantics, record.PointState) {
		return false, nil
	}
	return true, nil
}

func (controller *InvalidationController) prepareSetInvalidation(
	ctx context.Context,
	sets []model.BackupAssetDerivedArtifactSet,
) ([]invalidationLeasePlan, error) {
	if controller == nil || controller.coordinator == nil || controller.coordinator.leaseService == nil || controller.lifecycle == nil {
		return nil, ErrInvalidContract
	}
	if ctx == nil {
		ctx = context.Background()
	}
	type group struct {
		recoveryPointID string
		jobID           string
		sets            []model.BackupAssetDerivedArtifactSet
	}
	groups := make([]group, 0, len(sets))
	groupIndexes := make(map[string]int, len(sets))
	for _, set := range sets {
		if backupasset.ValidateOpaqueID(set.ID) != nil || backupasset.ValidateOpaqueID(set.RecoveryPointID) != nil ||
			backupasset.ValidateOpaqueID(set.JobID) != nil {
			return nil, ErrInvalidContract
		}
		key := set.RecoveryPointID + "\x00" + set.JobID
		index, exists := groupIndexes[key]
		if !exists {
			index = len(groups)
			groupIndexes[key] = index
			groups = append(groups, group{recoveryPointID: set.RecoveryPointID, jobID: set.JobID})
		}
		groups[index].sets = append(groups[index].sets, set)
	}

	plans := make([]invalidationLeasePlan, 0, len(groups))
	for _, group := range groups {
		lease, err := controller.coordinator.leaseService.Acquire(ctx, backupasset.AcquireLeaseRequest{
			RecoveryPointID: group.recoveryPointID,
			HolderType:      backupasset.LeaseHolderProcessingJob,
			OwnerID:         group.jobID,
		})
		if err != nil {
			controller.releaseInvalidationLeases(context.WithoutCancel(ctx), plans)
			return nil, fmt.Errorf("acquire Derived invalidation fence: %w", err)
		}
		plan := invalidationLeasePlan{lease: lease, prepared: make([]preparedStaleDerivedSet, 0, len(group.sets))}
		for _, set := range group.sets {
			prepared, prepareErr := controller.lifecycle.prepareMarkSetStaleFenced(ctx, set.ID, lease.Fence)
			if prepareErr != nil {
				plans = append(plans, plan)
				controller.releaseInvalidationLeases(context.WithoutCancel(ctx), plans)
				return nil, prepareErr
			}
			plan.prepared = append(plan.prepared, prepared)
		}
		plans = append(plans, plan)
	}
	return plans, nil
}

func (controller *InvalidationController) releaseInvalidationLeases(ctx context.Context, plans []invalidationLeasePlan) {
	if controller == nil || controller.coordinator == nil || controller.coordinator.leaseService == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for index := len(plans) - 1; index >= 0; index-- {
		_ = controller.coordinator.leaseService.Release(ctx, plans[index].lease.Fence)
	}
}

func (controller *InvalidationController) supersedeJobTx(ctx context.Context, tx *gorm.DB, jobID string) (bool, error) {
	if controller == nil || tx == nil || backupasset.ValidateOpaqueID(jobID) != nil {
		return false, ErrInvalidContract
	}
	var job model.BackupAssetProcessingJob
	result := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", jobID).Limit(1).Find(&job)
	if result.Error != nil || result.RowsAffected != 1 {
		return false, errors.Join(ErrInvalidContract, result.Error)
	}
	if !job.IsCurrent || isTerminalState(ProcessingState(job.State)) {
		return false, nil
	}
	now := controller.now().UTC()
	if job.CurrentAttemptID != nil {
		var attempt model.BackupAssetProcessingAttempt
		result = tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND job_id = ?", *job.CurrentAttemptID, job.ID).
			Limit(1).Find(&attempt)
		if result.Error != nil || result.RowsAffected != 1 || attempt.State != "active" || !attempt.IsCurrent {
			return false, errors.Join(ErrAttemptLost, result.Error)
		}
		if err := revokeAttemptGrantsTx(tx, attempt.ID, now, "source_changed"); err != nil {
			return false, err
		}
		if err := finishAttemptTx(tx, attempt.ID, now, "superseded", ""); err != nil {
			return false, err
		}
		var lease model.RecoveryPointLease
		result = tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", attempt.RecoveryPointLeaseID).Limit(1).Find(&lease)
		if result.Error != nil || result.RowsAffected != 1 || lease.OwnerID != job.ID ||
			hashFence(lease.FenceToken) != attempt.RecoveryPointFenceHash {
			return false, errors.Join(ErrManifestFenceLost, result.Error)
		}
		if lease.Status == string(backupasset.LeaseActive) {
			if err := controller.coordinator.leaseService.ReleaseTx(ctx, tx, leaseFenceFromRow(lease)); err != nil {
				return false, err
			}
		}
	}
	if err := tx.WithContext(ctx).Model(&model.BackupAssetProcessingInterest{}).Where("job_id = ? AND active = ?", job.ID, true).
		Updates(map[string]any{
			"active": false, "removed_reason": string(InterestRemovedSuperseded), "removed_at": now, "updated_at": now,
		}).Error; err != nil {
		return false, fmt.Errorf("supersede processing interests: %w", err)
	}
	revision, err := ValidateTransition(TransitionRequest{
		From: ProcessingState(job.State), To: ProcessingSuperseded,
		CurrentRevision: job.TransitionRevision, ExpectedRevision: job.TransitionRevision,
		SupersedeReason: SupersedeReasonPipelineChanged,
	})
	if err != nil {
		return false, err
	}
	updated := tx.WithContext(ctx).Model(&model.BackupAssetProcessingJob{}).
		Where("id = ? AND transition_revision = ? AND is_current = ?", job.ID, job.TransitionRevision, true).
		Updates(map[string]any{
			"state": string(ProcessingSuperseded), "transition_revision": revision,
			"supersede_reason": string(SupersedeReasonPipelineChanged), "current_attempt_id": nil,
			"is_current": false, "finished_at": now, "updated_at": now, "version": gorm.Expr("version + 1"),
		})
	if updated.Error != nil || updated.RowsAffected != 1 {
		return false, errors.Join(ErrRevisionConflict, updated.Error)
	}
	return true, nil
}

func invalidationProviderSupported(provider backupasset.ProviderKind) bool {
	switch provider {
	case backupasset.ProviderRestic, backupasset.ProviderRsync, backupasset.ProviderRclone:
		return true
	default:
		return false
	}
}

func invalidationPointCurrent(semantics, state string) bool {
	switch backupasset.PointVersionSemantics(semantics) {
	case backupasset.PointMutableHead:
		return backupasset.RecoveryPointState(state) == backupasset.RecoveryPointObserved
	case backupasset.PointNativeSnapshot, backupasset.PointXirangManifest, backupasset.PointImportedBaseline:
		current := backupasset.RecoveryPointState(state)
		return current == backupasset.RecoveryPointCommitted || current == backupasset.RecoveryPointDegraded
	default:
		return false
	}
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
