package recovery

import (
	"context"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func captureRecoveryAuthorityTarget(ctx context.Context, db *gorm.DB, claim RecoveryWorkerClaim, intent recoveryAuthorityTargetIntent, now time.Time) (recoveryAuthorityTargetExpectation, error) {
	var captured recoveryAuthorityTargetExpectation
	if db == nil {
		return captured, ErrRecoveryWorkerFenceLost
	}
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		captured, err = loadRecoveryAuthorityTargetExpectationTx(ctx, tx, claim, intent, now)
		return err
	})
	return captured, err
}

func revalidateRecoveryAuthorityTargetTx(ctx context.Context, tx *gorm.DB, claim RecoveryWorkerClaim, intent recoveryAuthorityTargetIntent, captured recoveryAuthorityTargetExpectation, now time.Time) error {
	current, err := loadRecoveryAuthorityTargetExpectationTx(ctx, tx, claim, intent, now)
	if err != nil {
		return err
	}
	if current != captured {
		return ErrRecoveryTargetChanged
	}
	return nil
}

func recoveryAuthoritySourceFenceDigest(fence backupasset.LeaseFence) string {
	return framedDigest("xirang/recovery/authority-source-fence/v1", fence.LeaseID, fence.RecoveryPointID,
		string(fence.HolderType), fence.OwnerID, fence.AttemptID, fence.FenceToken)
}

type recoveryAuthorityTargetFacts struct {
	sampledAt   time.Time
	plan        model.BackupAssetRecoveryPlan
	job         model.BackupAssetRecoveryJob
	preflight   model.BackupAssetRecoveryPreflight
	grant       model.BackupAssetRecoveryGrant
	source      model.RecoveryPointLease
	node        model.BackupAssetRecoveryNodeLease
	attempt     model.BackupAssetRecoveryAttempt
	rows        []executeOperationRow
	items       []model.BackupAssetRecoveryJobItem
	checkpoints []model.BackupAssetRecoveryCheckpoint
}

// loadRecoveryAuthorityTargetExpectationTx only reads locked durable facts. In
// particular it cannot infer completion from the current remote filesystem.
func loadRecoveryAuthorityTargetExpectationTx(ctx context.Context, tx *gorm.DB, claim RecoveryWorkerClaim, intent recoveryAuthorityTargetIntent, now time.Time) (recoveryAuthorityTargetExpectation, error) {
	var result recoveryAuthorityTargetExpectation
	if tx == nil || ctx == nil || now.IsZero() || !validRecoveryWorkerClaim(claim) ||
		!now.Before(claim.AbsoluteDeadline) || intent.phase == recoveryAuthorityTargetPreJob {
		return result, ErrRecoveryWorkerFenceLost
	}
	var reference struct{ PlanID string }
	loaded := tx.WithContext(ctx).Model(&model.BackupAssetRecoveryJob{}).Select("plan_id").Where("id = ?", claim.JobID).Limit(1).Find(&reference)
	if loaded.Error != nil {
		return result, loaded.Error
	}
	if loaded.RowsAffected != 1 {
		return result, ErrRecoveryWorkerFenceLost
	}
	locked := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).Session(&gorm.Session{})
	var plan model.BackupAssetRecoveryPlan
	loaded = locked.Where("id = ?", reference.PlanID).Limit(1).Find(&plan)
	if loaded.Error != nil {
		return result, loaded.Error
	}
	if loaded.RowsAffected != 1 || PlanState(plan.State) != PlanStateExecuted {
		return result, ErrRecoveryWorkerFenceLost
	}
	var job model.BackupAssetRecoveryJob
	loaded = locked.Where("id = ? AND plan_id = ?", claim.JobID, plan.ID).Limit(1).Find(&job)
	if loaded.Error != nil {
		return result, loaded.Error
	}
	if loaded.RowsAffected != 1 || job.State != string(JobStateRunning) || job.TransitionRevision != claim.TransitionRevision {
		return result, ErrRecoveryWorkerFenceLost
	}
	var preflight model.BackupAssetRecoveryPreflight
	loaded = locked.Where("id = ? AND plan_id = ?", job.PreflightID, plan.ID).Limit(1).Find(&preflight)
	if loaded.Error != nil {
		return result, loaded.Error
	}
	if loaded.RowsAffected != 1 {
		return result, ErrRecoveryWorkerFenceLost
	}
	var grant model.BackupAssetRecoveryGrant
	loaded = locked.Where("id = ? AND plan_id = ?", job.AuthorityGrantID, plan.ID).Limit(1).Find(&grant)
	if loaded.Error != nil {
		return result, loaded.Error
	}
	if loaded.RowsAffected != 1 || !validRecoveryJobBinding(plan, job, preflight, grant, now, false) {
		return result, ErrRecoveryWorkerFenceLost
	}
	var source model.RecoveryPointLease
	loaded = locked.Where("id = ?", claim.SourceFence.LeaseID).Limit(1).Find(&source)
	if loaded.Error != nil {
		return result, loaded.Error
	}
	if loaded.RowsAffected != 1 || !matchesCurrentRecoverySourceFence(source, claim.SourceFence, plan.RecoveryPointID, now) || !source.AbsoluteDeadline.Equal(claim.AbsoluteDeadline) {
		return result, ErrRecoveryWorkerFenceLost
	}
	var node model.BackupAssetRecoveryNodeLease
	loaded = locked.Where("id = ?", claim.NodeLeaseID).Limit(1).Find(&node)
	if loaded.Error != nil {
		return result, loaded.Error
	}
	if loaded.RowsAffected != 1 || !matchesCurrentRecoveryNodeFence(node, claim, job, now) {
		return result, ErrRecoveryWorkerFenceLost
	}
	var attempt model.BackupAssetRecoveryAttempt
	loaded = locked.Where("id = ? AND job_id = ?", claim.AttemptID, job.ID).Limit(1).Find(&attempt)
	if loaded.Error != nil {
		return result, loaded.Error
	}
	if loaded.RowsAffected != 1 || !matchesCurrentRecoveryAttemptFence(attempt, claim, now) ||
		(attempt.MutationArmed == (intent.phase == recoveryAuthorityTargetFreshFirstWrite)) {
		return result, ErrRecoveryWorkerFenceLost
	}
	rows, err := rebuildExecuteOperationRows(plan, preflight, tx.WithContext(ctx))
	if err != nil {
		return result, ErrRecoveryWorkerFenceLost
	}
	var items []model.BackupAssetRecoveryJobItem
	if err := locked.Where("job_id = ? AND plan_id = ?", job.ID, plan.ID).Order("ordinal ASC").Find(&items).Error; err != nil {
		return result, err
	}
	if len(items) == 0 || len(items) != len(rows) {
		return result, ErrRecoveryWorkerFenceLost
	}
	var checkpoints []model.BackupAssetRecoveryCheckpoint
	if err := locked.Where("job_id = ?", job.ID).Order("sequence ASC").Find(&checkpoints).Error; err != nil {
		return result, err
	}
	return recoveryAuthorityTargetExpectationFromFacts(claim, intent, now, recoveryAuthorityTargetFacts{
		plan: plan, job: job, preflight: preflight, grant: grant, source: source, node: node,
		attempt: attempt, rows: rows, items: items, checkpoints: checkpoints,
	})
}

// recoveryAuthorityTargetExpectationFromFacts derives target authority only
// from the rows already locked and validated by its caller.
func recoveryAuthorityTargetExpectationFromFacts(
	claim RecoveryWorkerClaim, intent recoveryAuthorityTargetIntent, now time.Time, facts recoveryAuthorityTargetFacts,
) (recoveryAuthorityTargetExpectation, error) {
	var result recoveryAuthorityTargetExpectation
	plan, job, preflight := facts.plan, facts.job, facts.preflight
	source, node, attempt := facts.source, facts.node, facts.attempt
	rows, items, checkpoints := facts.rows, facts.items, facts.checkpoints
	if len(items) == 0 || len(items) != len(rows) {
		return result, ErrRecoveryWorkerFenceLost
	}
	var operations []ordinaryCheckpointOperation
	var err error
	if TargetMode(job.TargetMode) == TargetModeInPlace {
		operations, err = newInPlaceOrdinaryCheckpointOperations(plan, job, items, rows)
	} else {
		operations, err = newIsolatedOrdinaryCheckpointOperations(plan, job, items, rows)
	}
	if err != nil {
		return result, err
	}
	firstPending, anchorOrdinal, anchorExecution, currentExecution := -1, -1, -1, -1
	for ordinal := range items {
		if TargetMode(job.TargetMode) == TargetModeInPlace {
			op := rows[ordinal].operation
			digest, digestErr := TargetPathDigest(job.TargetRootID, job.RootLocatorDigest, op.TargetRelativeLocator)
			if digestErr != nil || digest != op.TargetPathDigest {
				return result, ErrRecoveryWorkerFenceLost
			}
			if op.TargetRelativeLocator == plan.EncryptedTargetRelativePath && digest == plan.PathDigest {
				if anchorOrdinal != -1 || op.Kind == RecoveryOperationDelete {
					return result, ErrRecoveryWorkerFenceLost
				}
				anchorOrdinal = ordinal
			}
		}
	}
	for index, operation := range operations {
		if !operation.completed && firstPending == -1 {
			firstPending = index
		}
		if operation.itemID == intent.itemID && intent.itemID != "" {
			currentExecution = index
		}
		if anchorOrdinal >= 0 && operation.itemID == items[anchorOrdinal].ID {
			anchorExecution = index
		}
	}
	if intent.itemID != "" && currentExecution == -1 {
		return result, ErrRecoveryWorkerFenceLost
	}
	if currentExecution >= 0 && !operations[currentExecution].completed && currentExecution != firstPending {
		return result, ErrRecoveryWorkerFenceLost
	}
	if intent.phase == recoveryAuthorityTargetFreshFirstWrite && (len(checkpoints) != 0 || firstPending != 0) {
		return result, ErrRecoveryWorkerFenceLost
	}
	if len(checkpoints) == 0 {
		if job.TargetChainRevision != preflight.TargetRevision {
			return result, ErrRecoveryWorkerFenceLost
		}
		for _, operation := range operations {
			if operation.completed {
				return result, ErrRecoveryWorkerFenceLost
			}
		}
		result.historyDigest = framedDigest("xirang/recovery/authority-empty-history/v1", job.ID, preflight.TargetRevision)
	} else if TargetMode(job.TargetMode) == TargetModeInPlace {
		_, _, result.historyDigest, err = validateInPlaceOrdinaryCheckpointHistory(plan, job, claim, checkpoints, operations, now)
	} else {
		selectedItemID := ""
		if firstPending >= 0 {
			selectedItemID = operations[firstPending].itemID
		}
		result.historyDigest, err = validateIsolatedInterruptedCheckpointHistory(plan, job, claim, checkpoints, operations, selectedItemID, now)
	}
	if err != nil {
		return result, err
	}
	result.phase = intent.phase
	result.jobID, result.jobTransitionRevision = job.ID, job.TransitionRevision
	result.chainRevision, result.workerID = job.TargetChainRevision, claim.WorkerID
	result.attemptID, result.attemptFence = attempt.ID, attempt.Fence
	result.nodeLeaseID, result.nodeFence = node.ID, node.Fence
	result.sourceLeaseID, result.sourceFenceDigest = source.ID, recoveryAuthoritySourceFenceDigest(recoverySourceFence(source))
	if currentExecution >= 0 {
		result.currentItemID, result.currentOperationDigest = operations[currentExecution].itemID, operations[currentExecution].digest
	}
	if TargetMode(job.TargetMode) == TargetModeInPlace {
		if anchorOrdinal < 0 || anchorExecution < 0 {
			return recoveryAuthorityTargetExpectation{}, ErrRecoveryWorkerFenceLost
		}
		item, operation := items[anchorOrdinal], rows[anchorOrdinal].operation
		result.anchorItemID, result.anchorOperationDigest = item.ID, operations[anchorExecution].digest
		if operations[anchorExecution].completed {
			for _, checkpoint := range checkpoints {
				if checkpoint.JobItemID == item.ID && checkpoint.Phase == string(CheckpointPhaseOperation) {
					if result.anchorCheckpointID != "" || checkpoint.OperationDigest != result.anchorOperationDigest {
						return recoveryAuthorityTargetExpectation{}, ErrRecoveryWorkerFenceLost
					}
					result.anchorCheckpointID = checkpoint.ID
				}
			}
			if result.anchorCheckpointID == "" {
				return recoveryAuthorityTargetExpectation{}, ErrRecoveryWorkerFenceLost
			}
			switch operation.Kind {
			case RecoveryOperationCreate, RecoveryOperationOverwrite:
				if item.Outcome != "succeeded" || item.BytesWritten != operation.ExpectedPostBytes || item.VerifiedSize != operation.ExpectedPostBytes || item.VerifiedDigest != operation.ExpectedPostIdentityDigest {
					return recoveryAuthorityTargetExpectation{}, ErrRecoveryWorkerFenceLost
				}
				result.expectedDigest, result.expectedBytes = operation.ExpectedPostIdentityDigest, operation.ExpectedPostBytes
			case RecoveryOperationSkip:
				if item.Outcome != "skipped" || item.VerifiedSize != operation.ExpectedPriorBytes || item.VerifiedDigest != operation.ExpectedPrior.Digest {
					return recoveryAuthorityTargetExpectation{}, ErrRecoveryWorkerFenceLost
				}
				result.expectedDigest, result.expectedBytes = operation.ExpectedPrior.Digest, operation.ExpectedPriorBytes
			default:
				return recoveryAuthorityTargetExpectation{}, ErrRecoveryWorkerFenceLost
			}
		}
		if intent.phase == recoveryAuthorityTargetCurrentPost {
			if currentExecution != anchorExecution || firstPending != anchorExecution || operations[anchorExecution].completed ||
				(operation.Kind != RecoveryOperationCreate && operation.Kind != RecoveryOperationOverwrite) {
				return recoveryAuthorityTargetExpectation{}, ErrRecoveryWorkerFenceLost
			}
			result.expectedDigest, result.expectedBytes = operation.ExpectedPostIdentityDigest, operation.ExpectedPostBytes
		}
	}
	if !result.valid(TargetMode(job.TargetMode)) {
		return recoveryAuthorityTargetExpectation{}, ErrRecoveryWorkerFenceLost
	}
	return result, nil
}
