package recovery

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"strconv"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	targetChainRevisionDomain        = "xirang/recovery/target-chain/v1"
	targetAbsenceChainRevisionDomain = "xirang/recovery/target-absence-chain/v1"
	recoveryOperationRowDigestDomain = "xirang/recovery/operation-row/v1"
	recoveryPostPauseFailureCategory = "post_pause_failure"
)

var (
	ErrInvalidTargetChain                   = errors.New("invalid recovery target revision chain")
	errOrdinaryTargetVerificationMismatch   = errors.New("ordinary recovery target verification mismatch")
	errOrdinaryRemoteOutcomeUnresolved      = errors.New("ordinary recovery remote outcome unresolved")
	errOrdinaryDeleteObservationUnavailable = errors.New("ordinary recovery delete observation unavailable")
)

// ExecuteClaim is the ordinary fenced execution boundary. The durable
// first-write barrier remains the sole authority mint for all later target
// mutations, after the source has been freshly revalidated.
func (coordinator *WorkerCoordinator) ExecuteClaim(
	ctx context.Context,
	claim RecoveryWorkerClaim,
	source provider.RsyncRestoreSource,
	deleteGrantSecret string,
) (resultErr error) {
	return coordinator.executeClaim(ctx, claim, source, deleteGrantSecret, nil, provider.RsyncBoundRemoteTarget{})
}

func (coordinator *WorkerCoordinator) executeClaim(
	ctx context.Context,
	claim RecoveryWorkerClaim,
	source provider.RsyncRestoreSource,
	deleteGrantSecret string,
	declaredWriter provider.RsyncTargetWriter,
	declaredTarget provider.RsyncBoundRemoteTarget,
) (resultErr error) {
	if coordinator == nil || coordinator.target == nil || source == nil {
		return ErrInvalidRecoveryWorker
	}
	ctx = recoveryWorkerContext(ctx)
	defer func() {
		if closeErr := source.Close(); closeErr != nil && resultErr == nil {
			resultErr = publicOrdinaryExecutionError(closeErr)
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	snapshot, err := coordinator.loadOrdinarySourceDeclarationSnapshot(ctx, claim)
	if errors.Is(err, ErrRecoverySourceChanged) {
		_, driftErr := coordinator.prepareFirstWrite(ctx, claim, "", func(lockedCtx context.Context, tx *gorm.DB) error {
			lockedCoordinator := coordinator.withTransactionDB(tx)
			if _, loadErr := lockedCoordinator.loadOrdinarySourceDeclarationSnapshot(lockedCtx, claim); loadErr != nil {
				return loadErr
			}
			return ErrRecoveryWorkerFenceLost
		})
		if driftErr == nil {
			return ErrRecoveryWorkerFenceLost
		}
		return publicOrdinaryExecutionError(driftErr)
	}
	if err != nil {
		return publicOrdinaryExecutionError(fmt.Errorf("load ordinary source declaration: %w", err))
	}
	if revalidateErr := source.Revalidate(ctx); revalidateErr != nil {
		reconciledCheckpointID, reconcileErr := coordinator.reconcileCompletedOrdinaryOverwrites(ctx, claim)
		if reconcileErr != nil {
			return publicOrdinaryExecutionError(fmt.Errorf(
				"reconcile completed ordinary overwrites before source failure: %w", reconcileErr,
			))
		}
		projected, projectionErr := coordinator.tryProjectCompletedOperationSourceFailure(
			ctx, claim, snapshot.targetChainRevision, reconciledCheckpointID, revalidateErr,
		)
		if projectionErr != nil {
			return publicOrdinaryExecutionError(projectionErr)
		}
		if projected {
			return publicOrdinaryExecutionError(revalidateErr)
		}
		if errors.Is(revalidateErr, provider.ErrRsyncRestoreSourceDrift) {
			_, driftErr := coordinator.prepareFirstWrite(ctx, claim, "", func(context.Context, *gorm.DB) error {
				return ErrRecoverySourceChanged
			})
			if driftErr == nil {
				return ErrRecoveryWorkerFenceLost
			}
			return publicOrdinaryExecutionError(driftErr)
		}
		return publicOrdinaryExecutionError(fmt.Errorf(
			"revalidate ordinary source before declaration: %w", revalidateErr,
		))
	}
	reconciledOverwriteCheckpointID, err := coordinator.reconcileCompletedOrdinaryOverwrites(ctx, claim)
	if err != nil {
		return publicOrdinaryExecutionError(fmt.Errorf(
			"reconcile completed ordinary overwrites: %w", err,
		))
	}
	if len(snapshot.pendingJobItemIDs) == 0 {
		if err := coordinator.completeReconciledOrdinaryOverwrite(ctx, claim); err != nil {
			return publicOrdinaryExecutionError(fmt.Errorf(
				"complete reconciled ordinary overwrite: %w", err,
			))
		}
		return nil
	}
	materialized, err := coordinator.materializeOrdinarySourceDeclarations(ctx, source, claim, snapshot)
	if err != nil {
		if !errors.Is(err, ErrRecoveryWorkerFenceLost) {
			projected, projectionErr := coordinator.tryProjectCompletedOperationSourceFailure(
				ctx, claim, snapshot.targetChainRevision, reconciledOverwriteCheckpointID, err,
			)
			if projectionErr != nil {
				return publicOrdinaryExecutionError(projectionErr)
			}
			if projected {
				return publicOrdinaryExecutionError(err)
			}
		}
		return publicOrdinaryExecutionError(fmt.Errorf("materialize ordinary source declaration: %w", err))
	}
	itemIDs := materialized.snapshot.pendingJobItemIDs
	if len(itemIDs) == 0 {
		return ErrRecoveryWorkerFenceLost
	}
	basePermit, err := coordinator.prepareFirstWrite(ctx, claim, reconciledOverwriteCheckpointID, func(lockedCtx context.Context, tx *gorm.DB) error {
		lockedCoordinator := coordinator.withTransactionDB(tx)
		currentSnapshot, loadErr := lockedCoordinator.loadOrdinarySourceDeclarationSnapshot(lockedCtx, claim)
		if loadErr != nil {
			return loadErr
		}
		return validateOrdinarySourceDeclarationSnapshot(materialized.snapshot, currentSnapshot)
	})
	if err != nil {
		return fmt.Errorf("prepare ordinary first write: %w", err)
	}

	first, err := coordinator.loadOrdinaryOperationHandoff(ctx, claim, itemIDs[0])
	if err != nil {
		return publicOrdinaryExecutionError(fmt.Errorf("load ordinary item 0: %w", err))
	}
	if TargetMode(first.job.TargetMode) != TargetModeIsolated &&
		TargetMode(first.job.TargetMode) != TargetModeInPlace {
		return ErrRecoveryWorkerFenceLost
	}
	targetMode := TargetMode(first.job.TargetMode)
	var preloaded []ordinaryOperationExecution
	if targetMode == TargetModeIsolated {
		preloaded = make([]ordinaryOperationExecution, len(itemIDs))
		for index, itemID := range itemIDs {
			handoff := first
			if index != 0 {
				var loadErr error
				handoff, loadErr = coordinator.loadOrdinaryOperationHandoff(ctx, claim, itemID)
				if loadErr != nil {
					return publicOrdinaryExecutionError(fmt.Errorf("load ordinary item %d: %w", index, loadErr))
				}
			}
			if err := validateOrdinaryOperationExecutionHandoff(handoff); err != nil {
				return err
			}
			preloaded[index].handoff = handoff
		}
		if err := attachOrdinarySourceMaterialization(preloaded, materialized); err != nil {
			return fmt.Errorf("attach ordinary source materialization: %w", err)
		}
	}
	currentRevision := first.job.TargetChainRevision
	for index, itemID := range itemIDs {
		if revalidateErr := source.Revalidate(ctx); revalidateErr != nil {
			if _, projectionErr := coordinator.tryProjectCompletedOperationSourceFailure(
				ctx, claim, currentRevision, reconciledOverwriteCheckpointID, revalidateErr,
			); projectionErr != nil {
				return publicOrdinaryExecutionError(projectionErr)
			}
			return publicOrdinaryExecutionError(revalidateErr)
		}
		var execution ordinaryOperationExecution
		if targetMode == TargetModeIsolated {
			execution = preloaded[index]
		} else {
			handoff, loadErr := coordinator.loadOrdinaryOperationHandoff(ctx, claim, itemID)
			if loadErr != nil {
				return publicOrdinaryExecutionError(fmt.Errorf("load ordinary item %d: %w", index, loadErr))
			}
			if handoff.job.TargetChainRevision != currentRevision {
				return ErrRecoveryWorkerFenceLost
			}
			if err := validateOrdinaryOperationExecutionHandoff(handoff); err != nil {
				return err
			}
			executions := []ordinaryOperationExecution{{handoff: handoff}}
			if err := attachOrdinarySourceMaterialization(executions, materialized); err != nil {
				return fmt.Errorf("attach ordinary source materialization for item %d: %w", index, err)
			}
			execution = executions[0]
		}
		if execution.handoff.operation.Kind == RecoveryOperationDelete && deleteGrantSecret == "" &&
			!execution.handoff.deleteAuthorityConsumed {
			if err := coordinator.pauseOrdinaryDeleteAuthority(ctx, claim, execution.handoff, currentRevision); err != nil {
				return publicOrdinaryExecutionError(err)
			}
			return nil
		}
		basePermit, err = coordinator.refreshOrdinaryMutationPermit(
			ctx, claim, basePermit, execution.handoff, currentRevision,
		)
		if err != nil {
			return fmt.Errorf("refresh ordinary item %d permit: %w", index, err)
		}
		writePermit, permitErr := coordinator.ordinaryItemWritePermit(
			claim, basePermit, execution.handoff, currentRevision,
		)
		if permitErr != nil {
			return fmt.Errorf("mint ordinary item %d permit: %w", index, permitErr)
		}
		if writePermit.ValidateObjectAt(coordinator.now().UTC(), execution.handoff.object) != nil {
			return fmt.Errorf("validate ordinary item %d permit: %w", index, ErrRecoveryWorkerFenceLost)
		}

		operationResult, operationErr := coordinator.executeOrdinaryOperation(
			ctx, claim, source, execution, writePermit, basePermit, currentRevision, deleteGrantSecret,
			declaredWriter, declaredTarget,
		)
		postRevalidateErr := source.Revalidate(ctx)
		sourceOutcome := classifySourceRevalidationOutcome(postRevalidateErr)
		if operationResult.sourceStreamCloseErr != nil {
			operationSourceOutcome := classifySourceRevalidationOutcome(operationResult.sourceStreamCloseErr)
			if operationSourceOutcome == SourceRevalidationDrifted ||
				sourceOutcome == SourceRevalidationMatched {
				sourceOutcome = operationSourceOutcome
			}
		}
		if execution.handoff.operation.Kind == RecoveryOperationDelete &&
			operationResult.deleteDisposition != 0 {
			switch operationResult.deleteDisposition {
			case ordinaryDeleteRetryable:
				return publicOrdinaryExecutionError(operationErr)
			case ordinaryDeleteFenceLost:
				return ErrRecoveryWorkerFenceLost
			case ordinaryDeleteContradictory:
				// Continue through the existing unresolved-outcome projection.
			default:
				return ErrRecoveryWorkerFenceLost
			}
		}
		if operationResult.postPauseDisposition {
			if _, projectionErr := coordinator.projectOrdinaryPostPauseFailure(
				ctx, claim, execution.handoff, currentRevision, deleteGrantSecret,
				sourceOutcome, coordinator.now().UTC(),
			); projectionErr != nil {
				return publicOrdinaryExecutionError(projectionErr)
			}
			return publicOrdinaryExecutionError(operationErr)
		}
		if operationResult.unresolvedCategory.Valid() {
			if _, projectionErr := coordinator.projectPendingOperationUnresolved(
				ctx, claim, execution.handoff, currentRevision, operationResult,
				sourceOutcome, coordinator.now().UTC(),
			); projectionErr != nil {
				return publicOrdinaryExecutionError(projectionErr)
			}
			return ErrInvalidTargetVerification
		}
		if operationErr != nil {
			if operationResult.sourceOpenErr != nil {
				if _, projectionErr := coordinator.tryProjectCompletedOperationSourceFailure(
					ctx, claim, currentRevision, reconciledOverwriteCheckpointID,
					operationResult.sourceOpenErr,
				); projectionErr != nil {
					return publicOrdinaryExecutionError(projectionErr)
				}
			}
			return publicOrdinaryExecutionError(operationErr)
		}
		currentRevision, err = coordinator.projectOrdinaryOperation(
			ctx, claim, execution.handoff, currentRevision, operationResult, sourceOutcome,
		)
		if err != nil {
			return publicOrdinaryExecutionError(fmt.Errorf("project ordinary item %d: %w", index, err))
		}
		if execution.handoff.operation.Kind == RecoveryOperationOverwrite &&
			targetMode == TargetModeInPlace {
			if _, finalizeErr := coordinator.finalizeOrdinaryOverwrite(
				ctx, claim, execution.handoff, currentRevision,
			); finalizeErr != nil {
				return publicOrdinaryExecutionError(fmt.Errorf(
					"finalize ordinary overwrite item %d: %w", index, finalizeErr,
				))
			}
			if continueErr := coordinator.continueOrdinaryAfterOverwriteFinalize(
				ctx, claim, execution.handoff, currentRevision, sourceOutcome,
			); continueErr != nil {
				return publicOrdinaryExecutionError(fmt.Errorf(
					"continue ordinary overwrite item %d: %w", index, continueErr,
				))
			}
		}
		if operationResult.sourceStreamCloseErr != nil {
			return publicOrdinaryExecutionError(operationResult.sourceStreamCloseErr)
		}
		if postRevalidateErr != nil {
			return publicOrdinaryExecutionError(postRevalidateErr)
		}
	}
	return nil
}

func (coordinator *WorkerCoordinator) refreshOrdinaryMutationPermit(
	ctx context.Context,
	claim RecoveryWorkerClaim,
	base TargetWritePermit,
	handoff interruptedOperationHandoff,
	expectedRevision string,
) (TargetWritePermit, error) {
	if coordinator == nil || coordinator.db == nil || coordinator.now == nil ||
		!validRecoveryWorkerClaim(claim) || validateOrdinaryOperationExecutionHandoff(handoff) != nil ||
		!validOpaqueRevision(expectedRevision) || base.itemProof != nil || base.permit.proof == nil ||
		base.permit.proof.validateAt == nil ||
		base.permit.proof.sessionBinding != handoff.targetSessionBinding ||
		base.permit.proof.bindingDigest != targetMutationPermitProofDigest(
			base.permit, handoff.targetSessionBinding,
		) {
		return TargetWritePermit{}, ErrRecoveryWorkerFenceLost
	}
	ctx = recoveryWorkerContext(ctx)
	if err := ctx.Err(); err != nil {
		return TargetWritePermit{}, err
	}

	mutation := base.permit
	mutation.proof = nil
	err := coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := coordinator.now().UTC()
		if now.IsZero() || !claim.AbsoluteDeadline.After(now) {
			return ErrRecoveryWorkerFenceLost
		}

		var plan model.BackupAssetRecoveryPlan
		loaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ?", handoff.plan.ID).Limit(1).Find(&plan)
		if loaded.Error != nil {
			return loaded.Error
		}
		binding, bindingErr := newRecoveryTargetSessionBinding(plan)
		if loaded.RowsAffected != 1 || PlanState(plan.State) != PlanStateExecuted ||
			bindingErr != nil || binding != handoff.targetSessionBinding {
			return ErrRecoveryWorkerFenceLost
		}

		var job model.BackupAssetRecoveryJob
		loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ? AND plan_id = ?", claim.JobID, plan.ID).Limit(1).Find(&job)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || job.State != string(JobStateRunning) ||
			job.TransitionRevision != claim.TransitionRevision ||
			job.TargetChainRevision != expectedRevision || job.TargetMode != handoff.job.TargetMode ||
			job.TargetNodeID != binding.NodeID || job.TargetRootID != binding.RootID ||
			job.RootLocatorDigest != binding.RootLocatorDigest {
			return ErrRecoveryWorkerFenceLost
		}

		var source model.RecoveryPointLease
		loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ?", claim.SourceFence.LeaseID).Limit(1).Find(&source)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 ||
			!matchesCurrentRecoverySourceFence(source, claim.SourceFence, plan.RecoveryPointID, now) ||
			!source.AbsoluteDeadline.UTC().Equal(claim.AbsoluteDeadline.UTC()) {
			return ErrRecoveryWorkerFenceLost
		}

		var node model.BackupAssetRecoveryNodeLease
		loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ?", claim.NodeLeaseID).Limit(1).Find(&node)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || !matchesCurrentRecoveryNodeFence(node, claim, job, now) {
			return ErrRecoveryWorkerFenceLost
		}

		var attempt model.BackupAssetRecoveryAttempt
		loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ? AND job_id = ?", claim.AttemptID, job.ID).Limit(1).Find(&attempt)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || !attempt.MutationArmed ||
			!matchesCurrentRecoveryAttemptFence(attempt, claim, now) {
			return ErrRecoveryWorkerFenceLost
		}

		var latch model.BackupAssetRecoveryEvidence
		loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ?", recoverySchemaUseLatchRowID).Limit(1).Find(&latch)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || !validRecoverySchemaUseLatch(latch) {
			return ErrRecoveryWorkerFenceLost
		}

		expiresAt := earliestRecoveryFirstWriteExpiry(
			source.LeaseExpiresAt, source.AbsoluteDeadline, node.LeaseExpiresAt, *attempt.LeaseExpiresAt,
		)
		if !expiresAt.After(now) {
			return ErrRecoveryWorkerFenceLost
		}
		mutation.ExpiresAt = expiresAt
		mutation.ExpectedTargetRevision = expectedRevision
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrRecoveryWorkerFenceLost) {
			return TargetWritePermit{}, ErrRecoveryWorkerFenceLost
		}
		if contextErr := ctx.Err(); contextErr != nil {
			return TargetWritePermit{}, contextErr
		}
		return TargetWritePermit{}, fmt.Errorf("%w: refresh ordinary mutation permit", ErrRecoveryWorkerUnavailable)
	}

	var sealed TargetMutationPermit
	sealed = issueTargetMutationPermit(mutation, func(now time.Time) error {
		return coordinator.validateFirstWritePermitAt(claim, sealed, now)
	}, handoff.targetSessionBinding)
	permit, err := NewTargetWritePermit(sealed, coordinator.now().UTC())
	if err != nil {
		return TargetWritePermit{}, ErrRecoveryWorkerFenceLost
	}
	return permit, nil
}

func (coordinator *WorkerCoordinator) executeOrdinaryOperation(
	ctx context.Context,
	claim RecoveryWorkerClaim,
	source provider.RsyncRestoreSource,
	execution ordinaryOperationExecution,
	writePermit TargetWritePermit,
	basePermit TargetWritePermit,
	currentRevision string,
	deleteGrantSecret string,
	declaredWriter provider.RsyncTargetWriter,
	declaredTarget provider.RsyncBoundRemoteTarget,
) (ordinaryOperationResult, error) {
	var result ordinaryOperationResult
	deleteAuthorityConsumed := execution.handoff.deleteAuthorityConsumed
	switch execution.handoff.operation.Kind {
	case RecoveryOperationCreate, RecoveryOperationOverwrite:
		if !execution.hasEntry {
			return result, ErrRecoveryWorkerFenceLost
		}
		stream, err := source.OpenDeclaredRegular(ctx, execution.entry)
		if err != nil {
			result.sourceOpenErr = err
			return result, err
		}
		var writeResult TargetWriteResult
		var writeErr error
		if declaredWriter != nil {
			coordinator.declaredMu.Lock()
			coordinator.declaredWrites[declaredWriteKey(claim, execution.entry.AssetRef.EntryID)] = &declaredWriteContext{
				permit: writePermit, object: execution.handoff.object, entry: execution.entry,
				target: declaredTarget,
			}
			coordinator.declaredMu.Unlock()
			writeErr = declaredWriter.WriteDeclaredRegular(ctx, provider.RsyncTargetWriteCall{
				Target: declaredTarget, Entry: execution.entry, Source: stream,
				Permit: targetMutationPermitProjection(writePermit, declaredTarget.TargetBindingDigest),
			})
			if writeErr == nil {
				writeResult, _ = coordinator.takeDeclaredRegularResult(claim, execution.entry)
				if writeResult.TargetRevision == "" {
					writeErr = ErrRecoveryWorkerUnavailable
				}
			} else {
				coordinator.discardDeclaredRegular(claim, execution.entry.AssetRef.EntryID)
			}
		} else {
			writeResult, writeErr = coordinator.target.WriteAtomic(ctx, writePermit, TargetWriteAtomicRequest{
				Object: execution.handoff.object, ExpectedBytes: execution.entry.ExpectedSize,
				ExpectedDigest: execution.entry.ExpectedDigest, Content: stream,
			})
		}
		closeErr := stream.Close()
		if closeErr != nil {
			result.sourceStreamCloseErr = closeErr
		}
		if writeErr != nil {
			result.writeCallFailed = true
			result.unresolvedCategory = UnresolvedOperationWriteResultInvalid
			return result, errOrdinaryRemoteOutcomeUnresolved
		}
		result.writeResult = writeResult
		result.writeResultReturned = true
		if writeResult.BytesWritten != execution.entry.ExpectedSize ||
			writeResult.IdentityDigest != execution.entry.ExpectedDigest ||
			!validOpaqueRevision(writeResult.TargetRevision) {
			result.unresolvedCategory = UnresolvedOperationWriteResultInvalid
			return result, errOrdinaryRemoteOutcomeUnresolved
		}
	case RecoveryOperationSkip:
		if !execution.hasEntry {
			return result, ErrRecoveryWorkerFenceLost
		}
	case RecoveryOperationDelete:
		deleteObservation, err := coordinator.observeOrdinaryDeleteTarget(
			ctx, execution.handoff, basePermit,
		)
		if err != nil {
			if deleteAuthorityConsumed {
				result.deleteDisposition = classifyOrdinaryDeleteDisposition(err)
				if result.deleteDisposition == ordinaryDeleteContradictory {
					result.unresolvedCategory = UnresolvedOperationObservationInvalid
					return result, errOrdinaryRemoteOutcomeUnresolved
				}
				return result, err
			}
			result.postPauseDisposition = execution.handoff.deleteAuthorityConsumed ||
				errors.Is(err, errOrdinaryDeleteObservationUnavailable)
			return result, err
		}
		var consumedAuthority consumedOrdinaryDeleteAuthority
		if !execution.handoff.deleteAuthorityConsumed {
			consumedAuthority, err = coordinator.consumeOrdinaryDeleteAuthority(
				ctx, claim, execution.handoff, currentRevision, deleteGrantSecret, deleteObservation,
			)
			if err != nil {
				result.postPauseDisposition = !errors.Is(err, ErrRecoveryWorkerFenceLost)
				return result, err
			}
			deleteAuthorityConsumed = true
		}
		deletePermit, err := coordinator.ordinaryDeletePermit(
			ctx, claim, execution.handoff, currentRevision,
		)
		if err != nil {
			if deleteAuthorityConsumed {
				result.deleteDisposition = classifyOrdinaryDeleteDisposition(err)
				if result.deleteDisposition == ordinaryDeleteContradictory {
					result.unresolvedCategory = UnresolvedOperationWriteResultInvalid
					result.writeCallFailed = true
				}
				return result, err
			}
			result.postPauseDisposition = execution.handoff.deleteAuthorityConsumed
			return result, err
		}
		if consumedAuthority.CheckpointID != "" &&
			(deletePermit.proof == nil ||
				deletePermit.proof.consumedCheckpointID != consumedAuthority.CheckpointID ||
				deletePermit.proof.consumedGrantID != consumedAuthority.GrantID ||
				deletePermit.proof.consumedGrantDigest != consumedAuthority.GrantDigest) {
			return result, ErrRecoveryWorkerFenceLost
		}
		deleteResult, err := coordinator.target.Delete(
			ctx, deletePermit, TargetDeleteRequest{Object: execution.handoff.object},
		)
		if err != nil {
			if deleteAuthorityConsumed {
				result.deleteDisposition = classifyOrdinaryDeleteDisposition(err)
				if result.deleteDisposition == ordinaryDeleteContradictory {
					result.writeCallFailed = true
					result.unresolvedCategory = UnresolvedOperationWriteResultInvalid
					return result, errOrdinaryRemoteOutcomeUnresolved
				}
				return result, err
			}
			result.writeCallFailed = true
			result.unresolvedCategory = UnresolvedOperationWriteResultInvalid
			return result, errOrdinaryRemoteOutcomeUnresolved
		}
		result.writeResult = deleteResult
		result.writeResultReturned = true
		if deleteResult.BytesWritten != 0 || deleteResult.IdentityDigest != "" ||
			!validOpaqueRevision(deleteResult.TargetRevision) {
			if deleteAuthorityConsumed {
				result.deleteDisposition = ordinaryDeleteContradictory
			}
			result.unresolvedCategory = UnresolvedOperationWriteResultInvalid
			return result, errOrdinaryRemoteOutcomeUnresolved
		}
	default:
		return result, ErrRecoveryWorkerFenceLost
	}

	verifyPermit, err := newRecoveryTargetVerifyPermit(
		execution.handoff, basePermit.permit.ExpiresAt, coordinator.now().UTC(),
	)
	if err != nil {
		return result, ErrRecoveryWorkerFenceLost
	}
	observation, err := coordinator.target.Verify(
		ctx, verifyPermit, execution.handoff.object, cloneTargetVerifyExpectation(execution.handoff.expectation),
	)
	if err != nil {
		if execution.handoff.operation.Kind == RecoveryOperationDelete &&
			deleteAuthorityConsumed {
			result.deleteDisposition = classifyOrdinaryDeleteDisposition(err)
			if result.deleteDisposition == ordinaryDeleteContradictory {
				result.unresolvedCategory = UnresolvedOperationObservationInvalid
				result.observationCallFailed = true
				return result, errOrdinaryRemoteOutcomeUnresolved
			}
			return result, err
		}
		result.observationCallFailed = true
		result.unresolvedCategory = UnresolvedOperationObservationInvalid
		return result, errOrdinaryRemoteOutcomeUnresolved
	}
	result.observation = observation
	result.observationReturned = true
	if observation.Validate() != nil {
		if execution.handoff.operation.Kind == RecoveryOperationDelete &&
			deleteAuthorityConsumed {
			result.deleteDisposition = ordinaryDeleteContradictory
		}
		result.unresolvedCategory = UnresolvedOperationObservationInvalid
		return result, errOrdinaryRemoteOutcomeUnresolved
	}
	if result.writeResultReturned && result.writeResult.TargetRevision != observation.ObservedRevision {
		if execution.handoff.operation.Kind == RecoveryOperationDelete &&
			deleteAuthorityConsumed {
			result.deleteDisposition = ordinaryDeleteContradictory
		}
		result.unresolvedCategory = UnresolvedOperationRevisionDisagreement
		return result, errOrdinaryRemoteOutcomeUnresolved
	}
	if observation.ValidateAgainst(execution.handoff.expectation) != nil {
		if execution.handoff.operation.Kind == RecoveryOperationDelete &&
			deleteAuthorityConsumed {
			result.deleteDisposition = ordinaryDeleteContradictory
		}
		result.unresolvedCategory = UnresolvedOperationVerificationMismatch
		return result, errOrdinaryTargetVerificationMismatch
	}
	return result, nil
}

type ordinaryDeleteDisposition uint8

const (
	ordinaryDeleteRetryable ordinaryDeleteDisposition = iota + 1
	ordinaryDeleteContradictory
	ordinaryDeleteFenceLost
)

func classifyOrdinaryDeleteDisposition(err error) ordinaryDeleteDisposition {
	if err == nil {
		return ordinaryDeleteFenceLost
	}
	if errors.Is(err, ErrRecoveryTargetChanged) {
		return ordinaryDeleteContradictory
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, ErrRecoveryTargetUnavailable) ||
		errors.Is(err, backupasset.ErrKeyUnavailable) || errors.Is(err, backupasset.ErrKeyLost) ||
		errors.Is(err, errOrdinaryDeleteObservationUnavailable) {
		return ordinaryDeleteRetryable
	}
	if errors.Is(err, ErrInvalidTargetPermit) || errors.Is(err, ErrRecoveryWorkerFenceLost) {
		return ordinaryDeleteFenceLost
	}
	// An unclassified error is a contradiction unless the target adapter has
	// already normalized it to ErrRecoveryTargetUnavailable. This preserves
	// fail-closed evidence for forged or otherwise unexplained products.
	return ordinaryDeleteContradictory
}

type ordinaryOperationResult struct {
	observation           TargetVerifyObservation
	writeResult           TargetWriteResult
	observationReturned   bool
	writeResultReturned   bool
	observationCallFailed bool
	writeCallFailed       bool
	adoptionNoWrite       bool
	unresolvedCategory    UnresolvedOperationCategory
	postPauseDisposition  bool
	deleteDisposition     ordinaryDeleteDisposition
	sourceOpenErr         error
	sourceStreamCloseErr  error
}

func classifySourceRevalidationOutcome(err error) SourceRevalidationOutcome {
	if err == nil {
		return SourceRevalidationMatched
	}
	if errors.Is(err, provider.ErrRsyncRestoreSourceDrift) || errors.Is(err, ErrRecoverySourceChanged) {
		return SourceRevalidationDrifted
	}
	return SourceRevalidationFailed
}

func (coordinator *WorkerCoordinator) tryProjectCompletedOperationSourceFailure(
	ctx context.Context,
	claim RecoveryWorkerClaim,
	expectedRevision string,
	reconciledCheckpointID string,
	sourceErr error,
) (bool, error) {
	if sourceErr == nil {
		return false, ErrInvalidRecoveryWorker
	}
	_, err := coordinator.projectCompletedOperationSourceFailure(
		ctx, claim, expectedRevision, reconciledCheckpointID,
		classifySourceRevalidationOutcome(sourceErr), coordinator.now().UTC(),
	)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, ErrRecoveryWorkerFenceLost) {
		return false, nil
	}
	return false, err
}

type ordinaryOperationExecution struct {
	handoff  interruptedOperationHandoff
	entry    provider.RestoreEntry
	hasEntry bool
}

func validateOrdinaryOperationExecutionHandoff(handoff interruptedOperationHandoff) error {
	switch handoff.operation.Kind {
	case RecoveryOperationCreate, RecoveryOperationOverwrite, RecoveryOperationSkip:
		if handoff.operation.DisplayClass != RecoveryDisplayClassRegular ||
			handoff.operation.Source.AssetRef == nil || handoff.item.PlanItemID == nil {
			return ErrRecoveryWorkerFenceLost
		}
	case RecoveryOperationDelete:
	default:
		return ErrRecoveryWorkerFenceLost
	}
	return nil
}

type ordinarySourceDeclarationBinding struct {
	assetRef           backupasset.AssetRef
	entryType          backupasset.CatalogEntryType
	expectedSize       int64
	targetObjectDigest string
}

type ordinarySourceDeclaration struct {
	entry                      provider.RestoreEntry
	planItemID                 string
	jobItemID                  string
	operationKind              RecoveryOperationKind
	targetPathDigest           string
	expectedPostIdentityDigest string
	expectedPostBytes          int64
	expectedPriorBytes         int64
	estimatedBytes             int64
	jobItemDigest              string
}

type ordinarySourceDeclarationSnapshot struct {
	jobID                string
	planID               string
	recoveryPointID      string
	targetChainRevision  string
	declarations         []ordinarySourceDeclaration
	pendingJobItemIDs    []string
	operationItemDigests []string
}

type ordinarySourceMaterializedEntry struct {
	declaration ordinarySourceDeclaration
	entry       provider.RestoreEntry
}

type ordinarySourceMaterialization struct {
	snapshot          ordinarySourceDeclarationSnapshot
	entriesByPlanItem map[string]ordinarySourceMaterializedEntry
}

func ordinarySourceBinding(entry provider.RestoreEntry) ordinarySourceDeclarationBinding {
	return ordinarySourceDeclarationBinding{
		assetRef: entry.AssetRef, entryType: entry.Type, expectedSize: entry.ExpectedSize,
		targetObjectDigest: entry.TargetObjectDigest,
	}
}

func (coordinator *WorkerCoordinator) materializeOrdinarySourceDeclarations(
	ctx context.Context,
	source provider.RsyncRestoreSource,
	claim RecoveryWorkerClaim,
	snapshot ordinarySourceDeclarationSnapshot,
) (ordinarySourceMaterialization, error) {
	if coordinator == nil || coordinator.db == nil || source == nil || !validRecoveryWorkerClaim(claim) {
		return ordinarySourceMaterialization{}, ErrRecoveryWorkerFenceLost
	}
	declarationByBinding := make(map[ordinarySourceDeclarationBinding]ordinarySourceDeclaration, len(snapshot.declarations))
	for _, declaration := range snapshot.declarations {
		binding := ordinarySourceBinding(declaration.entry)
		if _, duplicate := declarationByBinding[binding]; duplicate {
			return ordinarySourceMaterialization{}, ErrRecoveryWorkerFenceLost
		}
		declarationByBinding[binding] = declaration
	}

	requested := make([]provider.RestoreEntry, len(snapshot.declarations))
	for index := range snapshot.declarations {
		requested[index] = snapshot.declarations[index].entry
	}
	materializedEntries, err := source.MaterializeDeclaredEntries(ctx, requested)
	if err != nil {
		return ordinarySourceMaterialization{}, err
	}
	if len(materializedEntries) != len(snapshot.declarations) {
		return ordinarySourceMaterialization{}, ErrRecoverySourceChanged
	}
	seen := make(map[ordinarySourceDeclarationBinding]struct{}, len(materializedEntries))
	entriesByPlanItem := make(map[string]ordinarySourceMaterializedEntry, len(materializedEntries))
	for _, entry := range materializedEntries {
		binding := ordinarySourceBinding(entry)
		declaration, declared := declarationByBinding[binding]
		if !declared || entry.Validate(snapshot.recoveryPointID) != nil {
			return ordinarySourceMaterialization{}, ErrRecoverySourceChanged
		}
		if _, duplicate := seen[binding]; duplicate {
			return ordinarySourceMaterialization{}, ErrRecoverySourceChanged
		}
		seen[binding] = struct{}{}
		if !ordinarySourceDigestMatchesFrozenOperation(
			declaration.operationKind, entry.ExpectedDigest, declaration.expectedPostIdentityDigest,
		) {
			return ordinarySourceMaterialization{}, ErrRecoverySourceChanged
		}
		if _, duplicate := entriesByPlanItem[declaration.planItemID]; duplicate {
			return ordinarySourceMaterialization{}, ErrRecoveryWorkerFenceLost
		}
		entriesByPlanItem[declaration.planItemID] = ordinarySourceMaterializedEntry{
			declaration: declaration,
			entry:       entry,
		}
	}
	if len(seen) != len(snapshot.declarations) || len(entriesByPlanItem) != len(snapshot.declarations) {
		return ordinarySourceMaterialization{}, ErrRecoverySourceChanged
	}
	return ordinarySourceMaterialization{
		snapshot: snapshot, entriesByPlanItem: entriesByPlanItem,
	}, nil
}

func (coordinator *WorkerCoordinator) loadOrdinarySourceDeclarationSnapshot(
	ctx context.Context,
	claim RecoveryWorkerClaim,
) (ordinarySourceDeclarationSnapshot, error) {
	if coordinator == nil || coordinator.db == nil || !validRecoveryWorkerClaim(claim) {
		return ordinarySourceDeclarationSnapshot{}, ErrRecoveryWorkerFenceLost
	}
	var jobReference struct {
		PlanID string `gorm:"column:plan_id"`
	}
	loaded := coordinator.db.WithContext(ctx).Table((model.BackupAssetRecoveryJob{}).TableName()).
		Select("plan_id").Where("id = ?", claim.JobID).Limit(1).Find(&jobReference)
	if loaded.Error != nil {
		return ordinarySourceDeclarationSnapshot{}, loaded.Error
	}
	if loaded.RowsAffected != 1 || !validOpaqueID(jobReference.PlanID) {
		return ordinarySourceDeclarationSnapshot{}, ErrRecoveryWorkerFenceLost
	}

	var snapshot ordinarySourceDeclarationSnapshot
	err := coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := coordinator.now().UTC()
		lock := clause.Locking{Strength: clause.LockingStrengthUpdate}

		var plan model.BackupAssetRecoveryPlan
		loaded = tx.WithContext(ctx).Clauses(lock).
			Where("id = ?", jobReference.PlanID).Limit(1).Find(&plan)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || PlanState(plan.State) != PlanStateExecuted {
			return ErrRecoveryWorkerFenceLost
		}

		var job model.BackupAssetRecoveryJob
		loaded = tx.WithContext(ctx).Clauses(lock).
			Where("id = ? AND plan_id = ?", claim.JobID, plan.ID).Limit(1).Find(&job)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || job.State != string(JobStateRunning) ||
			job.TransitionRevision != claim.TransitionRevision ||
			(TargetMode(job.TargetMode) != TargetModeIsolated && TargetMode(job.TargetMode) != TargetModeInPlace) {
			return ErrRecoveryWorkerFenceLost
		}

		var preflight model.BackupAssetRecoveryPreflight
		loaded = tx.WithContext(ctx).Clauses(lock).
			Where("id = ? AND plan_id = ?", job.PreflightID, plan.ID).Limit(1).Find(&preflight)
		if loaded.Error != nil {
			return loaded.Error
		}
		var grant model.BackupAssetRecoveryGrant
		loadedGrant := tx.WithContext(ctx).Clauses(lock).
			Where("id = ? AND plan_id = ?", job.AuthorityGrantID, plan.ID).Limit(1).Find(&grant)
		if loadedGrant.Error != nil {
			return loadedGrant.Error
		}
		if loaded.RowsAffected != 1 || loadedGrant.RowsAffected != 1 ||
			!validRecoveryJobBinding(plan, job, preflight, grant, now, false) {
			return ErrRecoveryWorkerFenceLost
		}

		var sourceLease model.RecoveryPointLease
		loaded = tx.WithContext(ctx).Clauses(lock).
			Where("id = ?", claim.SourceFence.LeaseID).Limit(1).Find(&sourceLease)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 ||
			!matchesCurrentRecoverySourceFence(sourceLease, claim.SourceFence, plan.RecoveryPointID, now) {
			return ErrRecoveryWorkerFenceLost
		}

		var nodeLease model.BackupAssetRecoveryNodeLease
		loaded = tx.WithContext(ctx).Clauses(lock).
			Where("id = ?", claim.NodeLeaseID).Limit(1).Find(&nodeLease)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || !matchesCurrentRecoveryNodeFence(nodeLease, claim, job, now) {
			return ErrRecoveryWorkerFenceLost
		}

		var attempt model.BackupAssetRecoveryAttempt
		loaded = tx.WithContext(ctx).Clauses(lock).
			Where("id = ? AND job_id = ?", claim.AttemptID, job.ID).Limit(1).Find(&attempt)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || !matchesCurrentRecoveryAttemptFence(attempt, claim, now) ||
			(!attempt.MutationArmed && job.TargetChainRevision != preflight.TargetRevision) {
			return ErrRecoveryWorkerFenceLost
		}

		operationRows, err := rebuildExecuteOperationRows(plan, preflight, tx.WithContext(ctx).Clauses(lock))
		if err != nil {
			return ErrRecoveryWorkerFenceLost
		}
		var planItems []model.BackupAssetRecoveryPlanItem
		loaded = tx.WithContext(ctx).Clauses(lock).
			Where("plan_id = ?", plan.ID).Order("ordinal ASC, id ASC").Find(&planItems)
		if loaded.Error != nil {
			return loaded.Error
		}
		var jobItems []model.BackupAssetRecoveryJobItem
		loaded = tx.WithContext(ctx).Clauses(lock).
			Where("job_id = ? AND plan_id = ?", job.ID, plan.ID).Order("ordinal ASC").Find(&jobItems)
		if loaded.Error != nil {
			return loaded.Error
		}
		if len(planItems) == 0 || len(jobItems) != len(operationRows) {
			return ErrRecoveryWorkerFenceLost
		}

		planItemByID := make(map[string]model.BackupAssetRecoveryPlanItem, len(planItems))
		entryIDs := make([]string, 0, len(planItems))
		for ordinal, planItem := range planItems {
			ref := backupasset.AssetRef{RecoveryPointID: planItem.RecoveryPointID, EntryID: planItem.EntryID}
			if !validOpaqueID(planItem.ID) || planItem.PlanID != plan.ID || planItem.Ordinal != ordinal ||
				planItem.RecoveryPointID != plan.RecoveryPointID ||
				planItem.CatalogGenerationID != plan.CatalogGenerationID ||
				planItem.EntryType != string(backupasset.CatalogEntryFile) ||
				backupasset.ValidateAssetRef(ref) != nil || !validDigest(planItem.RelativePathDigest) {
				return ErrRecoveryWorkerFenceLost
			}
			if _, duplicate := planItemByID[planItem.ID]; duplicate {
				return ErrRecoveryWorkerFenceLost
			}
			planItemByID[planItem.ID] = planItem
			entryIDs = append(entryIDs, planItem.EntryID)
		}

		var catalogEntries []model.CatalogEntry
		loaded = tx.WithContext(ctx).Clauses(lock).
			Select("generation_id", "recovery_point_id", "entry_id", "entry_type", "size").
			Where(
				"generation_id = ? AND recovery_point_id = ? AND entry_id IN ?",
				plan.CatalogGenerationID, plan.RecoveryPointID, entryIDs,
			).Find(&catalogEntries)
		if loaded.Error != nil {
			return loaded.Error
		}
		if len(catalogEntries) != len(planItems) {
			return ErrRecoverySourceChanged
		}
		catalogEntryByID := make(map[string]model.CatalogEntry, len(catalogEntries))
		for _, entry := range catalogEntries {
			if entry.GenerationID != plan.CatalogGenerationID || entry.RecoveryPointID != plan.RecoveryPointID ||
				entry.EntryType != string(backupasset.CatalogEntryFile) || entry.Size < 0 {
				return ErrRecoverySourceChanged
			}
			if _, duplicate := catalogEntryByID[entry.EntryID]; duplicate {
				return ErrRecoverySourceChanged
			}
			catalogEntryByID[entry.EntryID] = entry
		}

		declarationByPlanItem := make(map[string]ordinarySourceDeclaration, len(planItems))
		operationItemDigests := make([]string, len(jobItems))
		pendingJobItemIDs := make([]string, 0, len(jobItems))
		for index, item := range jobItems {
			operationRow := operationRows[index]
			if !ordinarySourceJobItemMatchesOperation(plan, job, item, operationRow, index) {
				return ErrRecoveryWorkerFenceLost
			}
			operationItemDigests[index] = ordinarySourceJobItemSnapshotDigest(item)
			if item.Outcome == "" && item.FailureCategory == "" {
				pendingJobItemIDs = append(pendingJobItemIDs, item.ID)
			}
			if operationRow.planItemID == nil {
				if operationRow.operation.Kind != RecoveryOperationDelete {
					return ErrRecoveryWorkerFenceLost
				}
				continue
			}
			planItem, found := planItemByID[*operationRow.planItemID]
			if !found || operationRow.operation.Source.AssetRef == nil ||
				operationRow.sourceRecoveryPointID != planItem.RecoveryPointID ||
				operationRow.sourceEntryID != planItem.EntryID ||
				*operationRow.operation.Source.AssetRef != (backupasset.AssetRef{
					RecoveryPointID: planItem.RecoveryPointID, EntryID: planItem.EntryID,
				}) || operationRow.operation.DisplayClass != RecoveryDisplayClassRegular {
				return ErrRecoveryWorkerFenceLost
			}
			catalogEntry, found := catalogEntryByID[planItem.EntryID]
			if !found || catalogEntry.EntryType != planItem.EntryType {
				return ErrRecoverySourceChanged
			}
			if item.Outcome == "" && item.FailureCategory == "" &&
				!ordinarySourceCatalogSizeMatchesFrozenOperation(
					operationRow.operation.Kind, catalogEntry.Size, item.ExpectedPostBytes, item.EstimatedBytes,
				) {
				return ErrRecoverySourceChanged
			}
			entry := provider.RestoreEntry{
				AssetRef: backupasset.AssetRef{
					RecoveryPointID: planItem.RecoveryPointID,
					EntryID:         planItem.EntryID,
				},
				Type:               backupasset.CatalogEntryFile,
				ExpectedSize:       catalogEntry.Size,
				TargetObjectDigest: planItem.RelativePathDigest,
			}
			if _, duplicate := declarationByPlanItem[planItem.ID]; duplicate {
				return ErrRecoveryWorkerFenceLost
			}
			declarationByPlanItem[planItem.ID] = ordinarySourceDeclaration{
				entry: entry, planItemID: planItem.ID, jobItemID: item.ID,
				operationKind: operationRow.operation.Kind, targetPathDigest: operationRow.operation.TargetPathDigest,
				expectedPostIdentityDigest: item.ExpectedPostIdentityDigest,
				expectedPostBytes:          item.ExpectedPostBytes,
				expectedPriorBytes:         item.ExpectedPriorBytes,
				estimatedBytes:             item.EstimatedBytes,
				jobItemDigest:              recoveryJobItemOperationDigest(item),
			}
		}
		if len(declarationByPlanItem) != len(planItems) {
			return ErrRecoveryWorkerFenceLost
		}
		declarations := make([]ordinarySourceDeclaration, len(planItems))
		seenBindings := make(map[ordinarySourceDeclarationBinding]struct{}, len(planItems))
		for index, planItem := range planItems {
			declaration, found := declarationByPlanItem[planItem.ID]
			if !found {
				return ErrRecoveryWorkerFenceLost
			}
			binding := ordinarySourceBinding(declaration.entry)
			if _, duplicate := seenBindings[binding]; duplicate {
				return ErrRecoveryWorkerFenceLost
			}
			seenBindings[binding] = struct{}{}
			declarations[index] = declaration
		}
		snapshot = ordinarySourceDeclarationSnapshot{
			jobID: job.ID, planID: plan.ID, recoveryPointID: plan.RecoveryPointID,
			targetChainRevision: job.TargetChainRevision,
			declarations:        declarations, pendingJobItemIDs: pendingJobItemIDs,
			operationItemDigests: operationItemDigests,
		}
		return nil
	})
	return snapshot, err
}

func ordinarySourceJobItemMatchesOperation(
	plan model.BackupAssetRecoveryPlan,
	job model.BackupAssetRecoveryJob,
	item model.BackupAssetRecoveryJobItem,
	operationRow executeOperationRow,
	ordinal int,
) bool {
	operation := operationRow.operation
	semanticDigest, err := SemanticTargetDigest(
		TargetMode(job.TargetMode), job.TargetRootID, job.RootLocatorDigest, operation.TargetRelativeLocator,
	)
	if err != nil || semanticDigest != operation.SemanticTargetDigest {
		return false
	}
	finalLocator := operation.TargetRelativeLocator
	if TargetMode(job.TargetMode) == TargetModeIsolated {
		finalLocator = job.EncryptedWorkspaceRelativeLocator + "/" + finalLocator
	}
	targetObjectDigest, err := TargetObjectDigest(job.TargetRootID, job.RootLocatorDigest, finalLocator)
	return err == nil && targetObjectDigest != semanticDigest &&
		sameAuthorizationString(item.PlanItemID, operationRow.planItemID) && validOpaqueID(item.ID) &&
		item.PlanID == plan.ID && item.JobID == job.ID && item.Ordinal == ordinal &&
		item.OperationKind == string(operation.Kind) && item.TargetPathDigest == operation.TargetPathDigest &&
		item.SemanticTargetDigest == semanticDigest && item.TargetObjectDigest == targetObjectDigest &&
		item.ExpectedPriorKind == string(operation.ExpectedPrior.Kind) &&
		item.ExpectedPriorDigest == operation.ExpectedPrior.Digest &&
		item.ExpectedPostIdentityDigest == operation.ExpectedPostIdentityDigest &&
		item.ExpectedPostBytes == operation.ExpectedPostBytes && item.ExpectedPriorBytes == operation.ExpectedPriorBytes &&
		item.EncryptedTargetRelativeLocator != "" && item.TargetLocatorKeyVersion > 0 &&
		item.TargetLocatorCipherVersion == targetLocatorCipherVersion &&
		item.DisplayClass == string(operation.DisplayClass) && item.EstimatedBytes == operation.EstimatedBytes
}

func ordinarySourceCatalogSizeMatchesFrozenOperation(
	kind RecoveryOperationKind,
	catalogSize int64,
	expectedPostBytes int64,
	estimatedBytes int64,
) bool {
	switch kind {
	case RecoveryOperationCreate, RecoveryOperationOverwrite:
		return catalogSize == expectedPostBytes
	case RecoveryOperationSkip:
		return catalogSize == estimatedBytes
	default:
		return false
	}
}

func ordinarySourceDigestMatchesFrozenOperation(
	kind RecoveryOperationKind,
	materializedDigest string,
	expectedPostIdentityDigest string,
) bool {
	switch kind {
	case RecoveryOperationCreate, RecoveryOperationOverwrite:
		return materializedDigest == expectedPostIdentityDigest
	case RecoveryOperationSkip:
		return validDigest(materializedDigest)
	default:
		return false
	}
}

func ordinarySourceJobItemSnapshotDigest(item model.BackupAssetRecoveryJobItem) string {
	return framedDigest(
		"xirang/recovery/ordinary-source-job-item-snapshot/v1",
		recoveryJobItemOperationDigest(item), item.Outcome, strconv.FormatInt(item.BytesWritten, 10),
		strconv.FormatInt(item.VerifiedSize, 10), item.VerifiedDigest, item.FailureCategory,
	)
}

func validateOrdinarySourceDeclarationSnapshot(
	frozen ordinarySourceDeclarationSnapshot,
	current ordinarySourceDeclarationSnapshot,
) error {
	if frozen.jobID != current.jobID || frozen.planID != current.planID ||
		frozen.recoveryPointID != current.recoveryPointID ||
		frozen.targetChainRevision != current.targetChainRevision ||
		len(frozen.operationItemDigests) != len(current.operationItemDigests) ||
		len(frozen.pendingJobItemIDs) != len(current.pendingJobItemIDs) ||
		len(frozen.declarations) != len(current.declarations) {
		return ErrRecoveryWorkerFenceLost
	}
	for index := range frozen.operationItemDigests {
		if frozen.operationItemDigests[index] != current.operationItemDigests[index] {
			return ErrRecoveryWorkerFenceLost
		}
	}
	for index := range frozen.pendingJobItemIDs {
		if frozen.pendingJobItemIDs[index] != current.pendingJobItemIDs[index] {
			return ErrRecoveryWorkerFenceLost
		}
	}
	for index := range frozen.declarations {
		before := frozen.declarations[index]
		after := current.declarations[index]
		if before.planItemID != after.planItemID || before.jobItemID != after.jobItemID ||
			before.operationKind != after.operationKind || before.targetPathDigest != after.targetPathDigest ||
			before.expectedPostIdentityDigest != after.expectedPostIdentityDigest ||
			before.expectedPostBytes != after.expectedPostBytes || before.expectedPriorBytes != after.expectedPriorBytes ||
			before.estimatedBytes != after.estimatedBytes || before.jobItemDigest != after.jobItemDigest {
			return ErrRecoveryWorkerFenceLost
		}
		if before.entry != after.entry {
			return ErrRecoverySourceChanged
		}
	}
	return nil
}

func attachOrdinarySourceMaterialization(
	executions []ordinaryOperationExecution,
	materialized ordinarySourceMaterialization,
) error {
	if len(executions) == 0 || !validOpaqueID(materialized.snapshot.jobID) ||
		!validOpaqueID(materialized.snapshot.planID) || len(materialized.entriesByPlanItem) == 0 {
		return ErrRecoveryWorkerFenceLost
	}
	attached := make(map[string]struct{}, len(executions))
	for index := range executions {
		execution := &executions[index]
		if execution.handoff.job.ID != materialized.snapshot.jobID ||
			execution.handoff.plan.ID != materialized.snapshot.planID {
			return ErrRecoveryWorkerFenceLost
		}
		switch execution.handoff.operation.Kind {
		case RecoveryOperationCreate, RecoveryOperationOverwrite, RecoveryOperationSkip:
			if execution.handoff.item.PlanItemID == nil {
				return ErrRecoveryWorkerFenceLost
			}
			planItemID := *execution.handoff.item.PlanItemID
			cached, found := materialized.entriesByPlanItem[planItemID]
			operation := execution.handoff.operation
			item := execution.handoff.item
			if !found {
				return ErrRecoveryWorkerFenceLost
			}
			if !ordinarySourceCatalogSizeMatchesFrozenOperation(
				operation.Kind, cached.entry.ExpectedSize, item.ExpectedPostBytes, item.EstimatedBytes,
			) {
				return ErrRecoverySourceChanged
			}
			if cached.declaration.planItemID != planItemID || cached.declaration.jobItemID != item.ID ||
				cached.declaration.operationKind != operation.Kind || item.OperationKind != string(operation.Kind) ||
				cached.declaration.targetPathDigest != operation.TargetPathDigest ||
				cached.declaration.expectedPostIdentityDigest != item.ExpectedPostIdentityDigest ||
				cached.declaration.expectedPostIdentityDigest != operation.ExpectedPostIdentityDigest ||
				cached.declaration.expectedPostBytes != item.ExpectedPostBytes ||
				cached.declaration.expectedPostBytes != operation.ExpectedPostBytes ||
				cached.declaration.expectedPriorBytes != item.ExpectedPriorBytes ||
				cached.declaration.expectedPriorBytes != operation.ExpectedPriorBytes ||
				cached.declaration.estimatedBytes != item.EstimatedBytes ||
				cached.declaration.estimatedBytes != operation.EstimatedBytes ||
				cached.declaration.jobItemDigest != recoveryJobItemOperationDigest(item) ||
				!ordinarySourceDigestMatchesFrozenOperation(
					operation.Kind, cached.entry.ExpectedDigest, item.ExpectedPostIdentityDigest,
				) ||
				operation.Source.AssetRef == nil || *operation.Source.AssetRef != cached.entry.AssetRef {
				return ErrRecoveryWorkerFenceLost
			}
			if _, duplicate := attached[planItemID]; duplicate {
				return ErrRecoveryWorkerFenceLost
			}
			attached[planItemID] = struct{}{}
			execution.entry = cached.entry
			execution.hasEntry = true
		case RecoveryOperationDelete:
			if execution.handoff.item.PlanItemID != nil {
				return ErrRecoveryWorkerFenceLost
			}
		default:
			return ErrRecoveryWorkerFenceLost
		}
	}
	return nil
}

func (coordinator *WorkerCoordinator) ordinaryItemWritePermit(
	claim RecoveryWorkerClaim,
	base TargetWritePermit,
	handoff interruptedOperationHandoff,
	expectedRevision string,
) (TargetWritePermit, error) {
	if coordinator == nil || coordinator.now == nil {
		return TargetWritePermit{}, ErrRecoveryWorkerFenceLost
	}
	now := coordinator.now().UTC()
	binding := handoff.targetSessionBinding
	mode := TargetMode(handoff.job.TargetMode)
	operation := handoff.operation
	item := handoff.item
	if base.permit.validateShapeAt(now) != nil || base.permit.proof == nil ||
		base.permit.proof.validateAt == nil || base.permit.proof.sessionBinding != binding ||
		base.permit.proof.bindingDigest != targetMutationPermitProofDigest(base.permit, binding) {
		return TargetWritePermit{}, ErrRecoveryWorkerFenceLost
	}
	if !validRecoveryWorkerClaim(claim) || validateOrdinaryOperationExecutionHandoff(handoff) != nil ||
		!validOpaqueRevision(expectedRevision) ||
		!binding.valid() || mode.Validate() != nil || binding.PlanID != handoff.plan.ID ||
		binding.PlanBindingDigest != handoff.plan.BindingDigest || handoff.job.PlanID != handoff.plan.ID ||
		handoff.job.PlanBindingDigest != handoff.plan.BindingDigest || binding.NodeID != handoff.plan.TargetNodeID ||
		binding.NodeID != handoff.job.TargetNodeID || binding.NodeRevision != handoff.plan.TargetBaseRevision ||
		binding.CredentialRevision != handoff.plan.CredentialScopeRevision ||
		binding.RootID != handoff.plan.TargetRootID || binding.RootID != handoff.job.TargetRootID ||
		binding.RootID != handoff.object.RootID || binding.RootLocator != handoff.plan.EncryptedTargetRootLocator ||
		binding.RootLocatorDigest != handoff.plan.RootLocatorDigest ||
		binding.RootLocatorDigest != handoff.job.RootLocatorDigest ||
		binding.RootLocatorDigest != handoff.object.RootLocatorDigest ||
		binding.RootRevision != handoff.plan.RootRevision || handoff.plan.TargetMode != handoff.job.TargetMode {
		return TargetWritePermit{}, ErrRecoveryWorkerFenceLost
	}
	if !handoff.object.valid() || handoff.object.TargetPathDigest != item.TargetObjectDigest ||
		item.PlanID != handoff.plan.ID || item.JobID != handoff.job.ID ||
		item.OperationKind != string(operation.Kind) || item.TargetPathDigest != operation.TargetPathDigest ||
		item.ExpectedPriorKind != string(operation.ExpectedPrior.Kind) ||
		item.ExpectedPriorDigest != operation.ExpectedPrior.Digest ||
		item.ExpectedPostIdentityDigest != operation.ExpectedPostIdentityDigest ||
		item.ExpectedPostBytes != operation.ExpectedPostBytes ||
		item.ExpectedPriorBytes != operation.ExpectedPriorBytes ||
		item.DisplayClass != string(operation.DisplayClass) ||
		handoff.operationDigest != recoveryJobItemOperationDigest(item) {
		return TargetWritePermit{}, ErrRecoveryWorkerFenceLost
	}
	if base.permit.JobID != claim.JobID || base.permit.JobID != handoff.job.ID ||
		base.permit.AttemptID != claim.AttemptID || base.permit.NodeLeaseID != claim.NodeLeaseID ||
		base.permit.AttemptFence != claim.AttemptFence || base.permit.NodeFence != claim.NodeFence ||
		base.permit.NodeID != binding.NodeID || base.permit.RootID != binding.RootID ||
		base.permit.RootLocatorDigest != binding.RootLocatorDigest || base.permit.RootRevision != binding.RootRevision {
		return TargetWritePermit{}, ErrRecoveryWorkerFenceLost
	}
	mutation := base.permit
	mutation.TargetPathDigest = handoff.object.TargetPathDigest
	mutation.ExpectedTargetRevision = expectedRevision
	mutation.proof = nil
	var sealed TargetMutationPermit
	sealed = issueTargetMutationPermit(mutation, func(now time.Time) error {
		return coordinator.validateFirstWritePermitAt(claim, sealed, now)
	}, binding)
	permit, err := NewTargetWritePermit(sealed, now)
	if err != nil {
		return TargetWritePermit{}, ErrRecoveryWorkerFenceLost
	}
	if operation.Kind == RecoveryOperationCreate || operation.Kind == RecoveryOperationOverwrite {
		if operation.Source.Kind != RecoveryOperationSourceAssetRef ||
			operation.Source.AssetRef == nil || operation.DisplayClass != RecoveryDisplayClassRegular ||
			!operation.ExpectedPrior.valid() || !operation.validExpectedPostFacts() {
			return TargetWritePermit{}, ErrRecoveryWorkerFenceLost
		}
		if operation.Kind == RecoveryOperationOverwrite && mode == TargetModeInPlace {
			if !handoff.overwriteArtifacts.valid() {
				return TargetWritePermit{}, ErrRecoveryWorkerFenceLost
			}
		} else if handoff.overwriteArtifacts != (recoveryOverwriteArtifactBinding{}) {
			return TargetWritePermit{}, ErrRecoveryWorkerFenceLost
		}
		permit = issueTargetItemWritePermit(permit, targetItemWritePermitProof{
			sessionBinding:     binding,
			jobID:              handoff.job.ID,
			jobItemID:          item.ID,
			operationDigest:    handoff.operationDigest,
			targetMode:         mode,
			object:             handoff.object,
			operation:          operation.Kind,
			expectedPrior:      operation.ExpectedPrior,
			expectedPriorBytes: operation.ExpectedPriorBytes,
			expectedDigest:     operation.ExpectedPostIdentityDigest,
			expectedBytes:      operation.ExpectedPostBytes,
			artifacts:          handoff.overwriteArtifacts,
		})
		if permit.itemProof == nil {
			return TargetWritePermit{}, ErrRecoveryWorkerFenceLost
		}
	}
	return permit, nil
}

func (coordinator *WorkerCoordinator) ordinaryDeletePermit(
	ctx context.Context,
	claim RecoveryWorkerClaim,
	handoff interruptedOperationHandoff,
	expectedRevision string,
) (TargetDeletePermit, error) {
	if coordinator == nil || coordinator.db == nil || coordinator.workspaceKeys == nil ||
		!validRecoveryWorkerClaim(claim) || handoff.operation.Kind != RecoveryOperationDelete ||
		TargetMode(handoff.job.TargetMode) != TargetModeInPlace || !validOpaqueRevision(expectedRevision) {
		return TargetDeletePermit{}, ErrRecoveryWorkerFenceLost
	}
	observedAuthority, err := coordinator.observeRecoveryAuthority(ctx, handoff.plan, handoff.preflight)
	if err != nil {
		return TargetDeletePermit{}, err
	}

	var mutation TargetMutationPermit
	var proof targetDeletePermitProof
	err = coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := coordinator.now().UTC()
		fence, err := coordinator.lockOrdinaryExecutionDispositionTx(
			ctx, tx, claim, handoff, expectedRevision, now, false, observedAuthority,
		)
		if err != nil {
			return err
		}
		if fence.item.OperationKind != string(RecoveryOperationDelete) ||
			fence.item.Outcome != "" || fence.item.FailureCategory != "" ||
			fence.item.TargetLocatorKeyVersion <= 0 ||
			fence.item.ExpectedPriorBytes != -1 ||
			fence.item.ExpectedPriorKind != string(ExpectedTargetPresent) ||
			fence.item.ExpectedPriorDigest == "" ||
			fence.item.TargetObjectDigest != handoff.object.TargetPathDigest {
			return ErrRecoveryWorkerFenceLost
		}
		var checkpoints []model.BackupAssetRecoveryCheckpoint
		loaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("job_id = ?", fence.job.ID).Order("sequence ASC").Find(&checkpoints)
		if loaded.Error != nil {
			return loaded.Error
		}
		if _, _, _, err := validateInPlaceOrdinaryCheckpointHistory(
			fence.plan, fence.job, claim, checkpoints, handoff.checkpointOperations, now,
		); err != nil {
			return err
		}
		required, consumed, found := ordinaryConsumedDeleteCheckpoints(checkpoints)
		if !found || consumed.ID == "" ||
			validateConsumedOrdinaryDeleteGrantTx(ctx, tx, fence.plan, fence.job, required, consumed) != nil {
			return ErrRecoveryWorkerFenceLost
		}

		var latch model.BackupAssetRecoveryEvidence
		loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ?", recoverySchemaUseLatchRowID).Limit(1).Find(&latch)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || !validRecoverySchemaUseLatch(latch) {
			return ErrRecoveryWorkerFenceLost
		}

		material, err := coordinator.workspaceKeys.ByVersion(
			ctx, backupasset.KeyDomainRecoveryCleanupOwnership, fence.item.TargetLocatorKeyVersion,
		)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
				errors.Is(err, backupasset.ErrKeyUnavailable) || errors.Is(err, backupasset.ErrKeyLost) {
				return err
			}
			return ErrRecoveryWorkerFenceLost
		}
		if !validTargetLocatorKey(material, fence.item.TargetLocatorKeyVersion) {
			return ErrRecoveryWorkerFenceLost
		}
		artifacts, err := deriveRecoveryDeleteArtifactBinding(material, recoveryDeleteArtifactBindingInput{
			keyVersion: fence.item.TargetLocatorKeyVersion,
			planID:     fence.plan.ID, planBindingDigest: fence.plan.BindingDigest,
			jobID: fence.job.ID, jobItemID: fence.item.ID,
			operationDigest:      handoff.operationDigest,
			consumedCheckpointID: consumed.ID, consumedGrantID: consumed.DeleteGrantID,
			consumedGrantDigest: consumed.DeleteGrantBindingDigest,
			targetMode:          TargetModeInPlace, nodeID: fence.job.TargetNodeID,
			rootID: fence.job.TargetRootID, rootLocatorDigest: fence.job.RootLocatorDigest,
			rootRevision: fence.plan.RootRevision, object: handoff.object,
			expectedPrior: ExpectedTargetIdentity{
				Kind:   ExpectedTargetIdentityKind(fence.item.ExpectedPriorKind),
				Digest: fence.item.ExpectedPriorDigest,
			},
			expectedPriorBytes: fence.item.ExpectedPriorBytes,
		})
		clear(material.Key)
		if err != nil {
			return ErrRecoveryWorkerFenceLost
		}
		expiresAt := earliestRecoveryFirstWriteExpiry(
			fence.source.LeaseExpiresAt, fence.source.AbsoluteDeadline,
			fence.node.LeaseExpiresAt, *fence.attempt.LeaseExpiresAt,
		)
		if !expiresAt.After(now) {
			return ErrRecoveryWorkerFenceLost
		}
		binding, err := newRecoveryTargetSessionBinding(fence.plan)
		if err != nil || binding != handoff.targetSessionBinding {
			return ErrRecoveryWorkerFenceLost
		}
		mutation = TargetMutationPermit{
			SchemaVersion: 1, NodeID: fence.job.TargetNodeID, Purpose: TargetPurposeWrite,
			RootID: fence.job.TargetRootID, RootLocatorDigest: fence.job.RootLocatorDigest,
			TargetPathDigest: handoff.object.TargetPathDigest, RootRevision: fence.plan.RootRevision,
			ExpiresAt: expiresAt, UseLatchID: RecoverySchemaUseLatchID,
			JobID: fence.job.ID, AttemptID: claim.AttemptID, NodeLeaseID: claim.NodeLeaseID,
			AttemptFence: claim.AttemptFence, NodeFence: claim.NodeFence,
			ExpectedTargetRevision: expectedRevision,
		}
		proof = targetDeletePermitProof{
			sessionBinding: binding, jobID: fence.job.ID, jobItemID: fence.item.ID,
			operationDigest:      handoff.operationDigest,
			consumedCheckpointID: consumed.ID, consumedGrantID: consumed.DeleteGrantID,
			consumedGrantDigest: consumed.DeleteGrantBindingDigest,
			currentAttemptID:    claim.AttemptID, currentAttemptFence: claim.AttemptFence,
			currentNodeLeaseID: claim.NodeLeaseID, currentNodeFence: claim.NodeFence,
			currentSourceFence: claim.SourceFence, targetChainRevision: expectedRevision,
			targetMode: TargetModeInPlace, object: handoff.object,
			expectedPrior: ExpectedTargetIdentity{
				Kind:   ExpectedTargetIdentityKind(fence.item.ExpectedPriorKind),
				Digest: fence.item.ExpectedPriorDigest,
			},
			expectedPriorBytes: fence.item.ExpectedPriorBytes, artifacts: artifacts,
		}
		return nil
	})
	if err != nil {
		return TargetDeletePermit{}, err
	}
	var sealed TargetMutationPermit
	sealed = issueTargetMutationPermit(mutation, func(now time.Time) error {
		return coordinator.validateOrdinaryDeletePermitAt(claim, sealed, proof, now)
	}, proof.sessionBinding)
	permit := issueTargetDeletePermit(sealed, proof)
	if permit.proof == nil {
		return TargetDeletePermit{}, ErrRecoveryWorkerFenceLost
	}
	if _, err := permit.authorityAt(coordinator.now().UTC(), TargetDeleteRequest{Object: proof.object}); err != nil {
		return TargetDeletePermit{}, ErrRecoveryWorkerFenceLost
	}
	return permit, nil
}

func (coordinator *WorkerCoordinator) validateOrdinaryDeletePermitAt(
	claim RecoveryWorkerClaim,
	permit TargetMutationPermit,
	proof targetDeletePermitProof,
	now time.Time,
) error {
	if coordinator == nil || coordinator.db == nil || now.IsZero() ||
		permit.validateShapeAt(now) != nil || !validRecoveryWorkerClaim(claim) ||
		claim.JobID != permit.JobID || claim.AttemptID != permit.AttemptID ||
		claim.NodeLeaseID != permit.NodeLeaseID || claim.AttemptFence != permit.AttemptFence ||
		claim.NodeFence != permit.NodeFence || proof.currentSourceFence != claim.SourceFence ||
		proof.targetChainRevision != permit.ExpectedTargetRevision ||
		proof.object.TargetPathDigest != permit.TargetPathDigest {
		return ErrInvalidTargetPermit
	}
	db := coordinator.db.WithContext(context.Background())
	var latch model.BackupAssetRecoveryEvidence
	loaded := db.Where("id = ?", recoverySchemaUseLatchRowID).Limit(1).Find(&latch)
	if loaded.Error != nil || loaded.RowsAffected != 1 || !validRecoverySchemaUseLatch(latch) {
		return ErrInvalidTargetPermit
	}
	var job model.BackupAssetRecoveryJob
	loaded = db.Where("id = ?", claim.JobID).Limit(1).Find(&job)
	if loaded.Error != nil || loaded.RowsAffected != 1 || job.State != string(JobStateRunning) ||
		job.TargetMode != string(TargetModeInPlace) || job.TargetNodeID != permit.NodeID ||
		job.TargetRootID != permit.RootID || job.RootLocatorDigest != permit.RootLocatorDigest ||
		job.TargetChainRevision != permit.ExpectedTargetRevision ||
		job.TransitionRevision != claim.TransitionRevision {
		return ErrInvalidTargetPermit
	}
	var plan model.BackupAssetRecoveryPlan
	loaded = db.Where("id = ? AND state = ?", job.PlanID, PlanStateExecuted).Limit(1).Find(&plan)
	if loaded.Error != nil || loaded.RowsAffected != 1 || plan.ID != proof.sessionBinding.PlanID ||
		plan.BindingDigest != proof.sessionBinding.PlanBindingDigest || plan.RootRevision != permit.RootRevision {
		return ErrInvalidTargetPermit
	}
	var item model.BackupAssetRecoveryJobItem
	loaded = db.Where("id = ? AND job_id = ?", proof.jobItemID, job.ID).Limit(1).Find(&item)
	if loaded.Error != nil || loaded.RowsAffected != 1 || item.OperationKind != string(RecoveryOperationDelete) ||
		item.Outcome != "" || item.FailureCategory != "" || item.TargetObjectDigest != proof.object.TargetPathDigest ||
		recoveryJobItemOperationDigest(item) != proof.operationDigest || item.ExpectedPriorBytes != -1 ||
		item.ExpectedPriorKind != string(ExpectedTargetPresent) || item.ExpectedPriorDigest != proof.expectedPrior.Digest {
		return ErrInvalidTargetPermit
	}
	var checkpoints []model.BackupAssetRecoveryCheckpoint
	loaded = db.Where("job_id = ?", job.ID).Order("sequence ASC").Find(&checkpoints)
	if loaded.Error != nil {
		return ErrInvalidTargetPermit
	}
	required, consumed, found := ordinaryConsumedDeleteCheckpoints(checkpoints)
	if !found || consumed.ID != proof.consumedCheckpointID || consumed.DeleteGrantID != proof.consumedGrantID ||
		consumed.DeleteGrantBindingDigest != proof.consumedGrantDigest ||
		validateConsumedOrdinaryDeleteGrantTx(context.Background(), db, plan, job, required, consumed) != nil {
		return ErrInvalidTargetPermit
	}
	var attempt model.BackupAssetRecoveryAttempt
	loaded = db.Where("id = ? AND job_id = ?", claim.AttemptID, job.ID).Limit(1).Find(&attempt)
	if loaded.Error != nil || loaded.RowsAffected != 1 || !attempt.MutationArmed ||
		!matchesCurrentRecoveryAttemptFence(attempt, claim, now) {
		return ErrInvalidTargetPermit
	}
	var node model.BackupAssetRecoveryNodeLease
	loaded = db.Where("id = ?", claim.NodeLeaseID).Limit(1).Find(&node)
	if loaded.Error != nil || loaded.RowsAffected != 1 || !matchesCurrentRecoveryNodeFence(node, claim, job, now) {
		return ErrInvalidTargetPermit
	}
	var source model.RecoveryPointLease
	loaded = db.Where("id = ?", claim.SourceFence.LeaseID).Limit(1).Find(&source)
	if loaded.Error != nil || loaded.RowsAffected != 1 ||
		!matchesCurrentRecoverySourceFence(source, claim.SourceFence, plan.RecoveryPointID, now) {
		return ErrInvalidTargetPermit
	}
	return nil
}

type ordinaryOverwriteFinalizeIssuance struct {
	permit     TargetFinalizeOverwritePermit
	request    TargetFinalizeOverwriteRequest
	checkpoint model.BackupAssetRecoveryCheckpoint
	item       model.BackupAssetRecoveryJobItem
	job        model.BackupAssetRecoveryJob
}

func (coordinator *WorkerCoordinator) ordinaryOverwriteFinalizePermit(
	ctx context.Context,
	claim RecoveryWorkerClaim,
	handoff interruptedOperationHandoff,
	expectedRevision string,
) (ordinaryOverwriteFinalizeIssuance, error) {
	var issuance ordinaryOverwriteFinalizeIssuance
	if coordinator == nil || coordinator.db == nil || coordinator.target == nil ||
		!validRecoveryWorkerClaim(claim) || handoff.operation.Kind != RecoveryOperationOverwrite ||
		TargetMode(handoff.job.TargetMode) != TargetModeInPlace ||
		!validOpaqueRevision(expectedRevision) || !handoff.overwriteArtifacts.valid() {
		return issuance, ErrRecoveryWorkerFenceLost
	}
	observedAuthority, err := coordinator.observeRecoveryAuthority(ctx, handoff.plan, handoff.preflight)
	if err != nil {
		return issuance, err
	}
	var mutation TargetMutationPermit
	var proof targetFinalizeOverwritePermitProof
	err = coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := coordinator.now().UTC()
		fence, err := coordinator.lockOrdinaryExecutionDispositionTx(
			ctx, tx, claim, handoff, expectedRevision, now, true, observedAuthority,
		)
		if err != nil {
			return err
		}
		operations, err := loadInPlaceOrdinaryCheckpointOperationsTx(
			ctx, tx, fence.plan, fence.preflight, fence.job,
		)
		if err != nil {
			return err
		}
		var checkpoints []model.BackupAssetRecoveryCheckpoint
		loaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("job_id = ?", fence.job.ID).Order("sequence ASC").Find(&checkpoints)
		if loaded.Error != nil {
			return loaded.Error
		}
		if _, _, _, err := validateInPlaceOrdinaryCheckpointHistory(
			fence.plan, fence.job, claim, checkpoints, operations, now,
		); err != nil {
			return err
		}
		var checkpoint model.BackupAssetRecoveryCheckpoint
		for _, candidate := range checkpoints {
			if candidate.Phase != string(CheckpointPhaseOperation) ||
				candidate.JobItemID != fence.item.ID {
				continue
			}
			if checkpoint.ID != "" {
				return ErrRecoveryWorkerFenceLost
			}
			checkpoint = candidate
		}
		if checkpoint.ID == "" || checkpoint.OperationDigest != handoff.operationDigest ||
			checkpoint.NextTargetRevision == checkpoint.PriorTargetRevision ||
			fence.item.PlanItemID == nil ||
			handoff.object.TargetPathDigest != fence.item.TargetObjectDigest ||
			handoff.operation.ExpectedPostIdentityDigest != fence.item.ExpectedPostIdentityDigest ||
			handoff.operation.ExpectedPostBytes != fence.item.ExpectedPostBytes ||
			handoff.operation.ExpectedPrior.Kind != ExpectedTargetIdentityKind(fence.item.ExpectedPriorKind) ||
			handoff.operation.ExpectedPrior.Digest != fence.item.ExpectedPriorDigest ||
			handoff.operation.ExpectedPriorBytes != fence.item.ExpectedPriorBytes {
			return ErrRecoveryWorkerFenceLost
		}
		binding, err := newRecoveryTargetSessionBinding(fence.plan)
		if err != nil || binding != handoff.targetSessionBinding {
			return ErrRecoveryWorkerFenceLost
		}
		expiresAt := earliestRecoveryFirstWriteExpiry(
			fence.source.LeaseExpiresAt, fence.source.AbsoluteDeadline,
			fence.node.LeaseExpiresAt, *fence.attempt.LeaseExpiresAt,
		)
		if !expiresAt.After(now) {
			return ErrRecoveryWorkerFenceLost
		}
		mutation = TargetMutationPermit{
			SchemaVersion: 1, NodeID: fence.job.TargetNodeID, Purpose: TargetPurposeWrite,
			RootID: fence.job.TargetRootID, RootLocatorDigest: fence.job.RootLocatorDigest,
			TargetPathDigest: handoff.object.TargetPathDigest, RootRevision: fence.plan.RootRevision,
			ExpiresAt: expiresAt, UseLatchID: RecoverySchemaUseLatchID,
			JobID: fence.job.ID, AttemptID: claim.AttemptID, NodeLeaseID: claim.NodeLeaseID,
			AttemptFence: claim.AttemptFence, NodeFence: claim.NodeFence,
			ExpectedTargetRevision: fence.job.TargetChainRevision,
		}
		issuance.request = TargetFinalizeOverwriteRequest{
			Object: handoff.object, ExpectedDigest: fence.item.ExpectedPostIdentityDigest,
			ExpectedBytes: fence.item.ExpectedPostBytes,
		}
		issuance.checkpoint = checkpoint
		issuance.item = fence.item
		issuance.job = fence.job
		proof = targetFinalizeOverwritePermitProof{
			sessionBinding: binding, jobID: fence.job.ID, jobItemID: fence.item.ID,
			checkpointID: checkpoint.ID, operationDigest: checkpoint.OperationDigest,
			checkpointAttemptID:    checkpoint.AttemptID,
			checkpointAttemptFence: checkpoint.AttemptFence,
			checkpointNodeFence:    checkpoint.NodeFence,
			currentAttemptID:       claim.AttemptID, currentAttemptFence: claim.AttemptFence,
			currentNodeLeaseID: claim.NodeLeaseID, currentNodeFence: claim.NodeFence,
			currentSourceFence:  claim.SourceFence,
			targetChainRevision: fence.job.TargetChainRevision,
			priorTargetRevision: checkpoint.PriorTargetRevision,
			nextTargetRevision:  checkpoint.NextTargetRevision,
			object:              handoff.object,
			expectedPrior: ExpectedTargetIdentity{
				Kind:   ExpectedTargetIdentityKind(fence.item.ExpectedPriorKind),
				Digest: fence.item.ExpectedPriorDigest,
			},
			expectedPriorBytes: fence.item.ExpectedPriorBytes,
			expectedPostDigest: fence.item.ExpectedPostIdentityDigest,
			expectedPostBytes:  fence.item.ExpectedPostBytes,
			artifacts:          handoff.overwriteArtifacts,
		}
		return nil
	})
	if err != nil {
		return ordinaryOverwriteFinalizeIssuance{}, err
	}
	mutation = issueTargetMutationPermit(mutation, func(now time.Time) error {
		return coordinator.validateOverwriteFinalizePermitAt(claim, mutation, proof, now)
	}, proof.sessionBinding)
	issuance.permit = issueTargetFinalizeOverwritePermit(mutation, proof)
	if issuance.permit.proof == nil {
		return ordinaryOverwriteFinalizeIssuance{}, ErrRecoveryWorkerFenceLost
	}
	if _, err := issuance.permit.authorityAt(coordinator.now().UTC(), issuance.request); err != nil {
		return ordinaryOverwriteFinalizeIssuance{}, ErrRecoveryWorkerFenceLost
	}
	return issuance, nil
}

type ordinaryExecutionFence struct {
	plan      model.BackupAssetRecoveryPlan
	preflight model.BackupAssetRecoveryPreflight
	job       model.BackupAssetRecoveryJob
	item      model.BackupAssetRecoveryJobItem
	attempt   model.BackupAssetRecoveryAttempt
	source    model.RecoveryPointLease
	node      model.BackupAssetRecoveryNodeLease
}

func (coordinator *WorkerCoordinator) lockOrdinaryExecutionTx(
	ctx context.Context,
	tx *gorm.DB,
	claim RecoveryWorkerClaim,
	handoff interruptedOperationHandoff,
	expectedRevision string,
	now time.Time,
	observedAuthority observedRecoveryAuthority,
) (ordinaryExecutionFence, error) {
	return coordinator.lockOrdinaryExecutionDispositionTx(
		ctx, tx, claim, handoff, expectedRevision, now, false, observedAuthority,
	)
}

func (coordinator *WorkerCoordinator) lockOrdinaryExecutionDispositionTx(
	ctx context.Context,
	tx *gorm.DB,
	claim RecoveryWorkerClaim,
	handoff interruptedOperationHandoff,
	expectedRevision string,
	now time.Time,
	completedOverwrite bool,
	observedAuthority observedRecoveryAuthority,
) (ordinaryExecutionFence, error) {
	var fence ordinaryExecutionFence
	var jobReference struct {
		PlanID string `gorm:"column:plan_id"`
	}
	loaded := tx.WithContext(ctx).Table((model.BackupAssetRecoveryJob{}).TableName()).
		Select("plan_id").Where("id = ?", claim.JobID).Limit(1).Find(&jobReference)
	if loaded.Error != nil {
		return fence, loaded.Error
	}
	if loaded.RowsAffected != 1 || !validOpaqueID(jobReference.PlanID) || jobReference.PlanID != handoff.job.PlanID {
		return fence, ErrRecoveryWorkerFenceLost
	}
	loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ?", jobReference.PlanID).Limit(1).Find(&fence.plan)
	if loaded.Error != nil {
		return fence, loaded.Error
	}
	if loaded.RowsAffected != 1 || PlanState(fence.plan.State) != PlanStateExecuted ||
		fence.plan.BindingDigest != handoff.plan.BindingDigest ||
		fence.plan.OperationSetDigest != handoff.plan.OperationSetDigest ||
		fence.plan.DeleteSetDigest != handoff.plan.DeleteSetDigest {
		return fence, ErrRecoveryWorkerFenceLost
	}
	loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ? AND plan_id = ?", claim.JobID, fence.plan.ID).Limit(1).Find(&fence.job)
	if loaded.Error != nil {
		return fence, loaded.Error
	}
	if loaded.RowsAffected != 1 || fence.job.State != string(JobStateRunning) ||
		fence.job.TransitionRevision != claim.TransitionRevision ||
		fence.job.PlanID != handoff.job.PlanID || fence.job.PreflightID != handoff.job.PreflightID ||
		fence.job.TargetChainRevision != expectedRevision || fence.job.TargetMode != handoff.job.TargetMode ||
		fence.job.TargetNodeID != handoff.job.TargetNodeID || fence.job.TargetRootID != handoff.job.TargetRootID ||
		fence.job.RootLocatorDigest != handoff.job.RootLocatorDigest {
		return fence, ErrRecoveryWorkerFenceLost
	}
	loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ? AND plan_id = ?", fence.job.PreflightID, fence.plan.ID).Limit(1).Find(&fence.preflight)
	if loaded.Error != nil {
		return fence, loaded.Error
	}
	var grant model.BackupAssetRecoveryGrant
	loadedGrant := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ? AND plan_id = ?", fence.job.AuthorityGrantID, fence.plan.ID).Limit(1).Find(&grant)
	if loadedGrant.Error != nil {
		return fence, loadedGrant.Error
	}
	if loaded.RowsAffected != 1 || loadedGrant.RowsAffected != 1 ||
		!validRecoveryJobBinding(fence.plan, fence.job, fence.preflight, grant, now, false) {
		return fence, ErrRecoveryWorkerFenceLost
	}
	if err := coordinator.sourceValidator.RevalidatePlanTx(ctx, tx, fence.plan); err != nil {
		return fence, err
	}
	if err := coordinator.revalidateObservedRecoveryAuthorityTx(
		ctx, tx, fence.plan, fence.preflight, observedAuthority,
	); err != nil {
		return fence, err
	}
	loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ?", claim.SourceFence.LeaseID).Limit(1).Find(&fence.source)
	if loaded.Error != nil {
		return fence, loaded.Error
	}
	if loaded.RowsAffected != 1 ||
		!matchesCurrentRecoverySourceFence(fence.source, claim.SourceFence, fence.plan.RecoveryPointID, now) {
		return fence, ErrRecoveryWorkerFenceLost
	}
	loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ?", claim.NodeLeaseID).Limit(1).Find(&fence.node)
	if loaded.Error != nil {
		return fence, loaded.Error
	}
	if loaded.RowsAffected != 1 || !matchesCurrentRecoveryNodeFence(fence.node, claim, fence.job, now) {
		return fence, ErrRecoveryWorkerFenceLost
	}
	loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ? AND job_id = ?", claim.AttemptID, fence.job.ID).Limit(1).Find(&fence.attempt)
	if loaded.Error != nil {
		return fence, loaded.Error
	}
	if loaded.RowsAffected != 1 || !fence.attempt.MutationArmed ||
		!matchesCurrentRecoveryAttemptFence(fence.attempt, claim, now) {
		return fence, ErrRecoveryWorkerFenceLost
	}
	loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ? AND job_id = ? AND plan_id = ?", handoff.item.ID, fence.job.ID, fence.plan.ID).
		Limit(1).Find(&fence.item)
	if loaded.Error != nil {
		return fence, loaded.Error
	}
	validItemDisposition := fence.item.Outcome == "" && fence.item.FailureCategory == ""
	if completedOverwrite {
		validItemDisposition = handoff.operation.Kind == RecoveryOperationOverwrite &&
			TargetMode(fence.job.TargetMode) == TargetModeInPlace &&
			fence.item.Outcome == "succeeded" && fence.item.FailureCategory == "" &&
			fence.item.BytesWritten == fence.item.ExpectedPostBytes &&
			fence.item.VerifiedSize == fence.item.ExpectedPostBytes &&
			fence.item.VerifiedDigest == fence.item.ExpectedPostIdentityDigest
	}
	if loaded.RowsAffected != 1 || !validItemDisposition ||
		recoveryJobItemOperationDigest(fence.item) != handoff.operationDigest {
		return fence, ErrRecoveryWorkerFenceLost
	}
	return fence, nil
}

func (coordinator *WorkerCoordinator) validateOverwriteFinalizePermitAt(
	claim RecoveryWorkerClaim,
	permit TargetMutationPermit,
	proof targetFinalizeOverwritePermitProof,
	now time.Time,
) error {
	if coordinator == nil || coordinator.db == nil || now.IsZero() ||
		permit.validateShapeAt(now) != nil || !validRecoveryWorkerClaim(claim) ||
		claim.JobID != permit.JobID || claim.AttemptID != permit.AttemptID ||
		claim.NodeLeaseID != permit.NodeLeaseID || claim.AttemptFence != permit.AttemptFence ||
		claim.NodeFence != permit.NodeFence || proof.currentSourceFence != claim.SourceFence ||
		proof.targetChainRevision != permit.ExpectedTargetRevision ||
		proof.object.TargetPathDigest != permit.TargetPathDigest {
		return ErrInvalidTargetPermit
	}
	db := coordinator.db.WithContext(context.Background())
	var latch model.BackupAssetRecoveryEvidence
	loaded := db.Where("id = ?", recoverySchemaUseLatchRowID).Limit(1).Find(&latch)
	if loaded.Error != nil || loaded.RowsAffected != 1 || !validRecoverySchemaUseLatch(latch) {
		return ErrInvalidTargetPermit
	}
	var job model.BackupAssetRecoveryJob
	loaded = db.Where("id = ?", claim.JobID).Limit(1).Find(&job)
	if loaded.Error != nil || loaded.RowsAffected != 1 || job.State != string(JobStateRunning) ||
		job.TransitionRevision != claim.TransitionRevision ||
		job.TargetMode != string(TargetModeInPlace) || job.TargetNodeID != permit.NodeID ||
		job.TargetRootID != permit.RootID || job.RootLocatorDigest != permit.RootLocatorDigest ||
		job.TargetChainRevision != permit.ExpectedTargetRevision {
		return ErrInvalidTargetPermit
	}
	var plan model.BackupAssetRecoveryPlan
	loaded = db.Where("id = ? AND state = ?", job.PlanID, PlanStateExecuted).Limit(1).Find(&plan)
	if loaded.Error != nil || loaded.RowsAffected != 1 || plan.ID != proof.sessionBinding.PlanID ||
		plan.BindingDigest != proof.sessionBinding.PlanBindingDigest ||
		plan.RootRevision != permit.RootRevision {
		return ErrInvalidTargetPermit
	}
	var item model.BackupAssetRecoveryJobItem
	loaded = db.Where("id = ? AND job_id = ?", proof.jobItemID, job.ID).Limit(1).Find(&item)
	if loaded.Error != nil || loaded.RowsAffected != 1 || item.OperationKind != string(RecoveryOperationOverwrite) ||
		item.Outcome != "succeeded" || item.FailureCategory != "" ||
		item.TargetObjectDigest != proof.object.TargetPathDigest ||
		recoveryJobItemOperationDigest(item) != proof.operationDigest ||
		item.ExpectedPriorKind != string(proof.expectedPrior.Kind) ||
		item.ExpectedPriorDigest != proof.expectedPrior.Digest ||
		item.ExpectedPriorBytes != proof.expectedPriorBytes ||
		item.ExpectedPostIdentityDigest != proof.expectedPostDigest ||
		item.ExpectedPostBytes != proof.expectedPostBytes ||
		item.BytesWritten != item.ExpectedPostBytes || item.VerifiedSize != item.ExpectedPostBytes ||
		item.VerifiedDigest != item.ExpectedPostIdentityDigest {
		return ErrInvalidTargetPermit
	}
	var checkpoint model.BackupAssetRecoveryCheckpoint
	loaded = db.Where("id = ? AND job_id = ?", proof.checkpointID, job.ID).Limit(1).Find(&checkpoint)
	if loaded.Error != nil || loaded.RowsAffected != 1 ||
		checkpoint.Phase != string(CheckpointPhaseOperation) || checkpoint.JobItemID != item.ID ||
		checkpoint.OperationDigest != proof.operationDigest ||
		checkpoint.AttemptID != proof.checkpointAttemptID ||
		checkpoint.AttemptFence != proof.checkpointAttemptFence ||
		checkpoint.NodeFence != proof.checkpointNodeFence ||
		checkpoint.PriorTargetRevision != proof.priorTargetRevision ||
		checkpoint.NextTargetRevision != proof.nextTargetRevision {
		return ErrInvalidTargetPermit
	}
	var attempt model.BackupAssetRecoveryAttempt
	loaded = db.Where("id = ? AND job_id = ?", claim.AttemptID, job.ID).Limit(1).Find(&attempt)
	if loaded.Error != nil || loaded.RowsAffected != 1 || !attempt.MutationArmed ||
		!matchesCurrentRecoveryAttemptFence(attempt, claim, now) {
		return ErrInvalidTargetPermit
	}
	var node model.BackupAssetRecoveryNodeLease
	loaded = db.Where("id = ?", claim.NodeLeaseID).Limit(1).Find(&node)
	if loaded.Error != nil || loaded.RowsAffected != 1 ||
		!matchesCurrentRecoveryNodeFence(node, claim, job, now) {
		return ErrInvalidTargetPermit
	}
	var source model.RecoveryPointLease
	loaded = db.Where("id = ?", claim.SourceFence.LeaseID).Limit(1).Find(&source)
	if loaded.Error != nil || loaded.RowsAffected != 1 ||
		!matchesCurrentRecoverySourceFence(source, claim.SourceFence, plan.RecoveryPointID, now) {
		return ErrInvalidTargetPermit
	}
	return nil
}

type consumedOrdinaryDeleteAuthority struct {
	CheckpointID string
	GrantID      string
	GrantDigest  string
}

func (coordinator *WorkerCoordinator) consumeOrdinaryDeleteAuthority(
	ctx context.Context,
	claim RecoveryWorkerClaim,
	handoff interruptedOperationHandoff,
	expectedRevision string,
	secret string,
	observation TargetLstatResult,
) (consumedOrdinaryDeleteAuthority, error) {
	if handoff.operation.Kind != RecoveryOperationDelete || !validAuthorizationGrantSecret(secret) ||
		!validDeletePauseObservation(handoff, observation) {
		return consumedOrdinaryDeleteAuthority{}, ErrRecoveryWorkerFenceLost
	}
	observedAuthority, err := coordinator.observeRecoveryAuthority(ctx, handoff.plan, handoff.preflight)
	if err != nil {
		return consumedOrdinaryDeleteAuthority{}, err
	}
	var authority consumedOrdinaryDeleteAuthority
	err = coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := coordinator.now().UTC()
		fence, err := coordinator.lockOrdinaryExecutionTx(
			ctx, tx, claim, handoff, expectedRevision, now, observedAuthority,
		)
		if err != nil {
			return err
		}
		var checkpoints []model.BackupAssetRecoveryCheckpoint
		loaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("job_id = ?", fence.job.ID).Order("sequence ASC").Find(&checkpoints)
		if loaded.Error != nil {
			return loaded.Error
		}
		required, hasRequired, _, err := validateInPlaceOrdinaryCheckpointHistory(
			fence.plan, fence.job, claim, checkpoints, handoff.checkpointOperations, now,
		)
		if err != nil || !hasRequired || !validDeletePauseObservation(handoff, observation) {
			return ErrRecoveryWorkerFenceLost
		}
		if required.PriorTargetRevision != expectedRevision {
			return ErrRecoveryWorkerFenceLost
		}
		secretHash := authorizationGrantSecretHash(
			AuthorizationReceiptCategoryExactMirrorDelete,
			fence.plan.ID, fence.job.ID, required.ID, secret,
		)
		var grant model.BackupAssetRecoveryGrant
		loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("plan_id = ? AND job_id = ? AND delete_checkpoint_id = ? AND authority_category = ?",
				fence.plan.ID, fence.job.ID, required.ID, AuthorityExactMirrorDelete).
			Limit(1).Find(&grant)
		if loaded.Error != nil {
			return loaded.Error
		}
		bindingDigest := framedDigest(
			recoveryAuthorizationGrantBindingDomain, string(AuthorizationReceiptCategoryExactMirrorDelete),
			fence.plan.ID, fence.job.ID, required.ID, secretHash, grant.ExpiresAt.Format(time.RFC3339Nano),
		)
		if loaded.RowsAffected != 1 || grant.JobID == nil || *grant.JobID != fence.job.ID ||
			grant.DeleteCheckpointID == nil || *grant.DeleteCheckpointID != required.ID ||
			grant.DeleteAttemptID == nil || *grant.DeleteAttemptID != claim.AttemptID ||
			grant.DeleteSetDigest != fence.job.DeleteSetDigest || grant.DeleteTargetRevision != expectedRevision ||
			grant.DeleteAttemptFence != claim.AttemptFence || grant.DeleteNodeFence != claim.NodeFence ||
			grant.BindingDigest != bindingDigest || grant.ConsumedAt != nil || grant.RevokedAt != nil ||
			!grant.ExpiresAt.After(now) || grant.ExpiresAt.After(required.DeleteAuthorityExpiresAt.UTC()) ||
			subtle.ConstantTimeCompare([]byte(grant.GrantHash), []byte(secretHash)) != 1 {
			return ErrRecoveryWorkerFenceLost
		}
		updated := tx.WithContext(ctx).Model(&model.BackupAssetRecoveryGrant{}).
			Where("id = ? AND consumed_at IS NULL AND revoked_at IS NULL AND expires_at > ?", grant.ID, now).
			Updates(map[string]any{"consumed_at": now, "updated_at": now})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrRecoveryWorkerFenceLost
		}
		checkpointID, err := backupasset.NewOpaqueID()
		if err != nil {
			return err
		}
		consumed := required
		consumed.ID = checkpointID
		consumed.Sequence = required.Sequence + 1
		consumed.Phase = string(CheckpointPhaseDeleteAuthorityConsumed)
		consumed.NextTargetRevision = required.PriorTargetRevision
		consumed.DeleteGrantID = grant.ID
		consumed.DeleteGrantBindingDigest = grant.BindingDigest
		consumed.DeleteGrantExpiresAt = timePointerValue(grant.ExpiresAt)
		consumed.DeleteGrantConsumedAt = timePointerValue(now)
		consumed.CreatedAt = now
		if err := tx.WithContext(ctx).Create(&consumed).Error; err != nil {
			return err
		}
		authority = consumedOrdinaryDeleteAuthority{
			CheckpointID: consumed.ID,
			GrantID:      grant.ID,
			GrantDigest:  grant.BindingDigest,
		}
		return nil
	})
	if err != nil {
		return consumedOrdinaryDeleteAuthority{}, err
	}
	return authority, nil
}

func (coordinator *WorkerCoordinator) pauseOrdinaryDeleteAuthority(
	ctx context.Context,
	claim RecoveryWorkerClaim,
	handoff interruptedOperationHandoff,
	expectedRevision string,
) error {
	if handoff.operation.Kind != RecoveryOperationDelete ||
		TargetMode(handoff.job.TargetMode) != TargetModeInPlace ||
		ConflictPolicy(handoff.plan.ConflictPolicy) != ConflictExactMirror {
		return ErrRecoveryWorkerFenceLost
	}
	renewedClaim, err := coordinator.Heartbeat(ctx, claim)
	if err != nil {
		return err
	}
	now := coordinator.now().UTC()
	verifyPermit, err := newRecoveryTargetVerifyPermit(handoff, renewedClaim.LeaseExpiresAt, now)
	if err != nil {
		return ErrRecoveryWorkerFenceLost
	}
	observation, err := coordinator.target.Lstat(ctx, verifyPermit, TargetLstatRequest{Object: handoff.object})
	if err != nil || !validDeletePauseObservation(handoff, observation) {
		return ErrRecoveryWorkerFenceLost
	}
	observedAuthority, err := coordinator.observeRecoveryAuthority(ctx, handoff.plan, handoff.preflight)
	if err != nil {
		return err
	}

	return coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := coordinator.now().UTC()
		fence, err := coordinator.lockOrdinaryExecutionTx(
			ctx, tx, renewedClaim, handoff, expectedRevision, now, observedAuthority,
		)
		if err != nil {
			return err
		}
		if !validDeletePauseObservation(handoff, observation) {
			return ErrRecoveryWorkerFenceLost
		}

		var items []model.BackupAssetRecoveryJobItem
		loaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("job_id = ?", fence.job.ID).Order("ordinal ASC, id ASC").Find(&items)
		if loaded.Error != nil {
			return loaded.Error
		}
		deleteItems := 0
		for _, item := range items {
			switch RecoveryOperationKind(item.OperationKind) {
			case RecoveryOperationCreate, RecoveryOperationOverwrite:
				if item.Outcome != "succeeded" || item.FailureCategory != "" {
					return ErrRecoveryWorkerFenceLost
				}
			case RecoveryOperationSkip:
				if item.Outcome != "skipped" || item.FailureCategory != "" {
					return ErrRecoveryWorkerFenceLost
				}
			case RecoveryOperationDelete:
				deleteItems++
				if item.Outcome != "" || item.FailureCategory != "" {
					return ErrRecoveryWorkerFenceLost
				}
			default:
				return ErrRecoveryWorkerFenceLost
			}
		}
		if deleteItems == 0 {
			return ErrRecoveryWorkerFenceLost
		}

		var checkpoints []model.BackupAssetRecoveryCheckpoint
		loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("job_id = ?", fence.job.ID).Order("sequence ASC").Find(&checkpoints)
		if loaded.Error != nil {
			return loaded.Error
		}
		if len(checkpoints) == 0 {
			return ErrRecoveryWorkerFenceLost
		}
		_, hasRequired, _, err := validateInPlaceOrdinaryCheckpointHistory(
			fence.plan, fence.job, renewedClaim, checkpoints, handoff.checkpointOperations, now,
		)
		if err != nil || hasRequired {
			return ErrRecoveryWorkerFenceLost
		}
		guard := CheckpointAppendGuard{
			SameAttempt: true, SameAttemptFence: true, SameNodeFence: true, MutationArmed: true,
			ExactMirror: true, NextSequence: len(checkpoints),
		}
		last := checkpoints[len(checkpoints)-1]
		cursor := CheckpointCursor{Sequence: last.Sequence, Phase: CheckpointPhase(last.Phase)}
		if !cursor.CanAppend(CheckpointPhaseDeleteAuthorityRequired, guard) {
			return ErrRecoveryWorkerFenceLost
		}

		deadline := renewedClaim.LeaseExpiresAt.UTC()
		for _, candidate := range []time.Time{fence.job.AuthorityExpiresAt.UTC(), fence.job.PreflightExpiresAt.UTC()} {
			if candidate.Before(deadline) {
				deadline = candidate
			}
		}
		checkpointID, err := backupasset.NewOpaqueID()
		if err != nil {
			return err
		}
		binding := DeleteAuthorityCheckpointBinding{
			CheckpointID: checkpointID, JobID: fence.job.ID, AttemptID: renewedClaim.AttemptID,
			DeleteSetDigest: fence.job.DeleteSetDigest, TargetRevision: expectedRevision,
			NodeRevision: fence.job.PreflightNodeRevision, RootRevision: fence.plan.RootRevision,
			AttemptFence: renewedClaim.AttemptFence, NodeFence: renewedClaim.NodeFence,
			AuthorizationExpiresAt: deadline,
		}
		if binding.ValidateAt(now) != nil {
			return ErrRecoveryWorkerFenceLost
		}
		checkpoint := model.BackupAssetRecoveryCheckpoint{
			ID: checkpointID, JobID: fence.job.ID, AttemptID: renewedClaim.AttemptID,
			Sequence: len(checkpoints), Phase: string(CheckpointPhaseDeleteAuthorityRequired),
			AuthorityCategory: string(AuthorityExactMirrorDelete), OperationDigest: fence.job.DeleteSetDigest,
			PriorTargetRevision: expectedRevision, NodeFence: renewedClaim.NodeFence,
			AttemptFence: renewedClaim.AttemptFence, PlanBindingDigest: fence.job.PlanBindingDigest,
			SourceRevisionDigest: fence.job.SourceRevisionDigest, PreflightID: fence.job.PreflightID,
			PreflightRevision: fence.job.PreflightRevision, PreflightExpiresAt: fence.job.PreflightExpiresAt,
			SecurityDecision: fence.job.SecurityDecision, SecurityDecisionDigest: fence.job.SecurityDecisionDigest,
			SecurityFindingSetDigest: fence.job.SecurityFindingSetDigest,
			SecurityPolicyRevision:   fence.job.SecurityPolicyRevision, AuthorityGrantID: fence.job.AuthorityGrantID,
			JobAuthorityCategory: fence.job.AuthorityCategory, AuthorityBindingDigest: fence.job.AuthorityBindingDigest,
			AuthorityExpiresAt: fence.job.AuthorityExpiresAt, DeleteNodeRevision: binding.NodeRevision,
			DeleteRootRevision: binding.RootRevision, DeleteAuthorityExpiresAt: timePointerValue(deadline),
			CreatedAt: now,
		}
		return tx.WithContext(ctx).Create(&checkpoint).Error
	})
}

func (coordinator *WorkerCoordinator) observeOrdinaryDeleteTarget(
	ctx context.Context,
	handoff interruptedOperationHandoff,
	basePermit TargetWritePermit,
) (TargetLstatResult, error) {
	if handoff.operation.Kind != RecoveryOperationDelete {
		return TargetLstatResult{}, ErrRecoveryWorkerFenceLost
	}
	now := coordinator.now().UTC()
	verifyPermit, err := newRecoveryTargetVerifyPermit(handoff, basePermit.permit.ExpiresAt, now)
	if err != nil {
		return TargetLstatResult{}, ErrRecoveryWorkerFenceLost
	}
	observation, err := coordinator.target.Lstat(
		ctx, verifyPermit, TargetLstatRequest{Object: handoff.object},
	)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return TargetLstatResult{}, err
		}
		return TargetLstatResult{}, fmt.Errorf("%w: %w", errOrdinaryDeleteObservationUnavailable, err)
	}
	if !validDeletePauseObservation(handoff, observation) &&
		(!handoff.deleteAuthorityConsumed || !validConsumedDeleteAbsenceObservation(observation)) {
		if handoff.deleteAuthorityConsumed {
			return TargetLstatResult{}, ErrRecoveryTargetChanged
		}
		return TargetLstatResult{}, ErrRecoveryWorkerFenceLost
	}
	return observation, nil
}

func validConsumedDeleteAbsenceObservation(observation TargetLstatResult) bool {
	return observation.Kind == TargetEntryMissing && observation.IdentityDigest == "" &&
		validOpaqueRevision(observation.TargetRevision)
}

func validDeletePauseObservation(
	handoff interruptedOperationHandoff,
	observation TargetLstatResult,
) bool {
	var expectedKind TargetEntryKind
	switch handoff.operation.DisplayClass {
	case RecoveryDisplayClassRegular:
		expectedKind = TargetEntryRegular
	case RecoveryDisplayClassDirectory:
		expectedKind = TargetEntryDirectory
	case RecoveryDisplayClassLink:
		expectedKind = TargetEntrySymlink
	case RecoveryDisplayClassSpecial:
		expectedKind = TargetEntrySpecial
	default:
		return false
	}
	return handoff.operation.Kind == RecoveryOperationDelete &&
		handoff.operation.ExpectedPrior.Kind == ExpectedTargetPresent &&
		observation.Kind == expectedKind && observation.IdentityDigest == handoff.operation.ExpectedPrior.Digest &&
		validOpaqueRevision(observation.TargetRevision)
}

func (coordinator *WorkerCoordinator) projectOrdinaryOperation(
	ctx context.Context,
	claim RecoveryWorkerClaim,
	handoff interruptedOperationHandoff,
	expectedRevision string,
	result ordinaryOperationResult,
	sourceOutcome SourceRevalidationOutcome,
) (string, error) {
	if !sourceOutcome.Valid() {
		return expectedRevision, ErrInvalidRecoveryWorker
	}
	observedAuthority, err := coordinator.observeRecoveryAuthority(ctx, handoff.plan, handoff.preflight)
	if err != nil {
		return expectedRevision, err
	}
	nextRevision := expectedRevision
	var terminalState JobState
	err = coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := coordinator.now().UTC()
		fence, err := coordinator.lockOrdinaryExecutionTx(
			ctx, tx, claim, handoff, expectedRevision, now, observedAuthority,
		)
		if err != nil {
			return err
		}
		observation := result.observation
		if !result.observationReturned || observation.ValidateAgainst(handoff.expectation) != nil {
			return ErrRecoveryWorkerFenceLost
		}
		if handoff.operation.Kind == RecoveryOperationDelete {
			var checkpoints []model.BackupAssetRecoveryCheckpoint
			loaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
				Where("job_id = ?", fence.job.ID).Order("sequence ASC").Find(&checkpoints)
			if loaded.Error != nil {
				return loaded.Error
			}
			if _, _, _, err := validateInPlaceOrdinaryCheckpointHistory(
				fence.plan, fence.job, claim, checkpoints, handoff.checkpointOperations, now,
			); err != nil {
				return ErrRecoveryWorkerFenceLost
			}
			required, consumed, found := ordinaryConsumedDeleteCheckpoints(checkpoints)
			if !found {
				return ErrRecoveryWorkerFenceLost
			}
			if err := validateConsumedOrdinaryDeleteGrantTx(
				ctx, tx, fence.plan, fence.job, required, consumed,
			); err != nil {
				return err
			}
		}
		outcome := "succeeded"
		bytesWritten, verifiedSize, verifiedDigest := int64(0), int64(0), ""
		if observation.Present != nil {
			verifiedSize = observation.Present.Bytes
			verifiedDigest = observation.Present.IdentityDigest
		}
		switch handoff.operation.Kind {
		case RecoveryOperationSkip:
			outcome = "skipped"
		case RecoveryOperationCreate, RecoveryOperationOverwrite:
			bytesWritten = fence.item.ExpectedPostBytes
		}
		updated := tx.WithContext(ctx).Model(&model.BackupAssetRecoveryJobItem{}).
			Where("id = ? AND job_id = ? AND outcome = '' AND failure_category = ''", fence.item.ID, fence.job.ID).
			Updates(map[string]any{
				"outcome": outcome, "bytes_written": bytesWritten, "verified_size": verifiedSize,
				"verified_digest": verifiedDigest, "updated_at": now,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrRecoveryWorkerFenceLost
		}

		switch handoff.operation.Kind {
		case RecoveryOperationCreate, RecoveryOperationOverwrite:
			if fence.item.PlanItemID == nil || observation.Present == nil {
				return ErrRecoveryWorkerFenceLost
			}
			advance := TargetChainAdvance{
				PriorRevision: expectedRevision, OperationDigest: handoff.operationDigest,
				PlanItemID: *fence.item.PlanItemID, SourceRevisionDigest: fence.job.SourceRevisionDigest,
				AttemptID: claim.AttemptID, AttemptFence: claim.AttemptFence, NodeFence: claim.NodeFence,
				VerifiedIdentity: observation.Present.IdentityDigest, TargetRevision: observation.ObservedRevision,
			}
			nextRevision, err = advance.NextRevision()
		case RecoveryOperationDelete:
			if fence.item.PlanItemID != nil || observation.Absent == nil ||
				(result.writeResultReturned && (!validOrdinaryWriteResult(handoff, result.writeResult) ||
					result.writeResult.TargetRevision != observation.ObservedRevision)) ||
				(!result.writeResultReturned && !handoff.reconcileConsumedDelete) {
				return ErrRecoveryWorkerFenceLost
			}
			advance := TargetAbsenceChainAdvance{
				PriorRevision: expectedRevision, OperationDigest: handoff.operationDigest,
				JobItemID: fence.item.ID, SourceRevisionDigest: fence.job.SourceRevisionDigest,
				AttemptID: claim.AttemptID, AttemptFence: claim.AttemptFence, NodeFence: claim.NodeFence,
				AbsenceEvidence: observation.Absent.Evidence, TargetRevision: observation.ObservedRevision,
			}
			nextRevision, err = advance.NextRevision()
		case RecoveryOperationSkip:
			nextRevision = expectedRevision
		default:
			return ErrRecoveryWorkerFenceLost
		}
		if err != nil {
			return ErrRecoveryWorkerFenceLost
		}
		sequence := 0
		var last model.BackupAssetRecoveryCheckpoint
		loaded := tx.WithContext(ctx).Where("job_id = ?", fence.job.ID).
			Order("sequence DESC").Limit(1).Find(&last)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected == 1 {
			sequence = last.Sequence + 1
		}
		checkpointID, idErr := backupasset.NewOpaqueID()
		if idErr != nil {
			return idErr
		}
		checkpoint := model.BackupAssetRecoveryCheckpoint{
			ID: checkpointID, JobID: fence.job.ID, JobItemID: fence.item.ID,
			AttemptID: claim.AttemptID, Sequence: sequence,
			Phase: string(CheckpointPhaseOperation), AuthorityCategory: string(AuthorityWrite),
			OperationDigest: handoff.operationDigest, PriorTargetRevision: expectedRevision,
			NextTargetRevision: nextRevision, NodeFence: claim.NodeFence, AttemptFence: claim.AttemptFence,
			PlanBindingDigest: fence.job.PlanBindingDigest, SourceRevisionDigest: fence.job.SourceRevisionDigest,
			PreflightID: fence.job.PreflightID, PreflightRevision: fence.job.PreflightRevision,
			PreflightExpiresAt: fence.job.PreflightExpiresAt, SecurityDecision: fence.job.SecurityDecision,
			SecurityDecisionDigest:   fence.job.SecurityDecisionDigest,
			SecurityFindingSetDigest: fence.job.SecurityFindingSetDigest,
			SecurityPolicyRevision:   fence.job.SecurityPolicyRevision, AuthorityGrantID: fence.job.AuthorityGrantID,
			JobAuthorityCategory:   fence.job.AuthorityCategory,
			AuthorityBindingDigest: fence.job.AuthorityBindingDigest,
			AuthorityExpiresAt:     fence.job.AuthorityExpiresAt, CreatedAt: now,
		}
		if err := tx.WithContext(ctx).Create(&checkpoint).Error; err != nil {
			return err
		}
		nextPhase := fence.job.WorkspacePhase
		if TargetMode(fence.job.TargetMode) == TargetModeIsolated {
			nextPhase = string(WorkspacePhaseWriting)
		}
		jobUpdates := map[string]any{
			"target_chain_revision": nextRevision,
			"workspace_phase":       nextPhase,
			"updated_at":            now,
		}
		updated = tx.WithContext(ctx).Model(&model.BackupAssetRecoveryJob{}).
			Where("id = ? AND state = ? AND transition_revision = ? AND workspace_phase = ? AND target_chain_revision = ?",
				fence.job.ID, JobStateRunning, claim.TransitionRevision,
				fence.job.WorkspacePhase, expectedRevision).
			Updates(jobUpdates)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrRecoveryWorkerFenceLost
		}
		fence.job.TargetChainRevision = nextRevision
		fence.job.WorkspacePhase = nextPhase
		if handoff.operation.Kind == RecoveryOperationOverwrite &&
			TargetMode(fence.job.TargetMode) == TargetModeInPlace {
			return nil
		}
		if sourceOutcome != SourceRevalidationMatched {
			_, sourceErr := coordinator.projectSourceRevalidationFailureTx(
				ctx, tx, claim, fence.plan, fence.job, checkpoint, sourceOutcome, now, now, false,
			)
			if sourceErr == nil {
				terminalState = JobStateNeedsAttention
			}
			return sourceErr
		}

		var pending int64
		if err := tx.WithContext(ctx).Model(&model.BackupAssetRecoveryJobItem{}).
			Where("job_id = ? AND outcome = ''", fence.job.ID).Count(&pending).Error; err != nil {
			return err
		}
		if pending != 0 {
			return nil
		}
		if err := coordinator.completeOrdinaryRecoveryJobTx(ctx, tx, claim, fence.job, nextRevision, now); err != nil {
			return err
		}
		terminalState = JobStateSucceeded
		return nil
	})
	if err == nil && terminalState.Valid() {
		coordinator.observeJobOutcome(ctx, claim.JobID, terminalState)
	}
	return nextRevision, err
}

func (coordinator *WorkerCoordinator) finalizeOrdinaryOverwrite(
	ctx context.Context,
	claim RecoveryWorkerClaim,
	handoff interruptedOperationHandoff,
	expectedRevision string,
) (ordinaryOverwriteFinalizeIssuance, error) {
	issuance, err := coordinator.ordinaryOverwriteFinalizePermit(
		ctx, claim, handoff, expectedRevision,
	)
	if err != nil {
		return ordinaryOverwriteFinalizeIssuance{}, err
	}
	result, err := coordinator.target.FinalizeOverwrite(ctx, issuance.permit, issuance.request)
	if err != nil {
		return ordinaryOverwriteFinalizeIssuance{}, err
	}
	if result.BytesWritten != issuance.request.ExpectedBytes ||
		result.IdentityDigest != issuance.request.ExpectedDigest ||
		!validOpaqueRevision(result.TargetRevision) || issuance.item.PlanItemID == nil {
		return ordinaryOverwriteFinalizeIssuance{}, ErrRecoveryWorkerFenceLost
	}
	advance := TargetChainAdvance{
		PriorRevision:        issuance.checkpoint.PriorTargetRevision,
		OperationDigest:      issuance.checkpoint.OperationDigest,
		PlanItemID:           *issuance.item.PlanItemID,
		SourceRevisionDigest: issuance.job.SourceRevisionDigest,
		AttemptID:            issuance.checkpoint.AttemptID,
		AttemptFence:         issuance.checkpoint.AttemptFence,
		NodeFence:            issuance.checkpoint.NodeFence,
		VerifiedIdentity:     result.IdentityDigest,
		TargetRevision:       result.TargetRevision,
	}
	nextRevision, err := advance.NextRevision()
	if err != nil || nextRevision != issuance.checkpoint.NextTargetRevision {
		return ordinaryOverwriteFinalizeIssuance{}, ErrRecoveryWorkerFenceLost
	}
	return issuance, nil
}

func (coordinator *WorkerCoordinator) reconcileCompletedOrdinaryOverwrites(
	ctx context.Context,
	claim RecoveryWorkerClaim,
) (string, error) {
	if coordinator == nil || coordinator.db == nil || coordinator.target == nil ||
		!validRecoveryWorkerClaim(claim) {
		return "", ErrInvalidRecoveryWorker
	}
	var job model.BackupAssetRecoveryJob
	loaded := coordinator.db.WithContext(ctx).Where("id = ?", claim.JobID).Limit(1).Find(&job)
	if loaded.Error != nil {
		return "", loaded.Error
	}
	if loaded.RowsAffected != 1 || job.State != string(JobStateRunning) ||
		job.TransitionRevision != claim.TransitionRevision {
		return "", ErrRecoveryWorkerFenceLost
	}
	if TargetMode(job.TargetMode) != TargetModeInPlace {
		return "", nil
	}
	var items []model.BackupAssetRecoveryJobItem
	loaded = coordinator.db.WithContext(ctx).
		Where("job_id = ? AND operation_kind = ? AND outcome = ? AND failure_category = ''",
			claim.JobID, RecoveryOperationOverwrite, "succeeded").
		Order("ordinal ASC").Find(&items)
	if loaded.Error != nil {
		return "", loaded.Error
	}
	reconciledCheckpointID := ""
	for _, item := range items {
		handoff, err := coordinator.loadCompletedOrdinaryOverwriteHandoff(ctx, claim, item.ID)
		if err != nil {
			return "", err
		}
		issuance, err := coordinator.finalizeOrdinaryOverwrite(
			ctx, claim, handoff, handoff.job.TargetChainRevision,
		)
		if err != nil {
			return "", err
		}
		reconciledCheckpointID = issuance.checkpoint.ID
	}
	return reconciledCheckpointID, nil
}

func (coordinator *WorkerCoordinator) completeReconciledOrdinaryOverwrite(
	ctx context.Context,
	claim RecoveryWorkerClaim,
) error {
	var item model.BackupAssetRecoveryJobItem
	loaded := coordinator.db.WithContext(ctx).
		Where("job_id = ? AND operation_kind = ? AND outcome = ? AND failure_category = ''",
			claim.JobID, RecoveryOperationOverwrite, "succeeded").
		Order("ordinal DESC").Limit(1).Find(&item)
	if loaded.Error != nil {
		return loaded.Error
	}
	if loaded.RowsAffected != 1 {
		return ErrRecoveryWorkerFenceLost
	}
	handoff, err := coordinator.loadCompletedOrdinaryOverwriteHandoff(ctx, claim, item.ID)
	if err != nil {
		return err
	}
	return coordinator.continueOrdinaryAfterOverwriteFinalize(
		ctx, claim, handoff, handoff.job.TargetChainRevision, SourceRevalidationMatched,
	)
}

func (coordinator *WorkerCoordinator) continueOrdinaryAfterOverwriteFinalize(
	ctx context.Context,
	claim RecoveryWorkerClaim,
	handoff interruptedOperationHandoff,
	expectedRevision string,
	sourceOutcome SourceRevalidationOutcome,
) error {
	if !sourceOutcome.Valid() {
		return ErrInvalidRecoveryWorker
	}
	observedAuthority, err := coordinator.observeRecoveryAuthority(ctx, handoff.plan, handoff.preflight)
	if err != nil {
		return err
	}
	var terminalState JobState
	err = coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := coordinator.now().UTC()
		fence, err := coordinator.lockOrdinaryExecutionDispositionTx(
			ctx, tx, claim, handoff, expectedRevision, now, true, observedAuthority,
		)
		if err != nil {
			return err
		}
		var checkpoint model.BackupAssetRecoveryCheckpoint
		loaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("job_id = ? AND job_item_id = ? AND phase = ?",
				fence.job.ID, fence.item.ID, CheckpointPhaseOperation).
			Limit(2).Find(&checkpoint)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || checkpoint.OperationDigest != handoff.operationDigest ||
			checkpoint.NextTargetRevision != expectedRevision {
			return ErrRecoveryWorkerFenceLost
		}
		if sourceOutcome != SourceRevalidationMatched {
			_, err := coordinator.projectSourceRevalidationFailureTx(
				ctx, tx, claim, fence.plan, fence.job, checkpoint, sourceOutcome, now, now, false,
			)
			if err == nil {
				terminalState = JobStateNeedsAttention
			}
			return err
		}
		var pending int64
		if err := tx.WithContext(ctx).Model(&model.BackupAssetRecoveryJobItem{}).
			Where("job_id = ? AND outcome = ''", fence.job.ID).Count(&pending).Error; err != nil {
			return err
		}
		if pending != 0 {
			return nil
		}
		if err := coordinator.completeOrdinaryRecoveryJobTx(
			ctx, tx, claim, fence.job, expectedRevision, now,
		); err != nil {
			return err
		}
		terminalState = JobStateSucceeded
		return nil
	})
	if err == nil && terminalState.Valid() {
		coordinator.observeJobOutcome(ctx, claim.JobID, terminalState)
	}
	return err
}

func (coordinator *WorkerCoordinator) projectCompletedOperationSourceFailure(
	ctx context.Context,
	claim RecoveryWorkerClaim,
	expectedRevision string,
	reconciledCheckpointID string,
	sourceOutcome SourceRevalidationOutcome,
	failedAt time.Time,
) (model.BackupAssetRecoveryEvidence, error) {
	if coordinator == nil || coordinator.db == nil || !validRecoveryWorkerClaim(claim) ||
		!validOpaqueRevision(expectedRevision) || sourceOutcome == SourceRevalidationMatched ||
		!sourceOutcome.Valid() || failedAt.IsZero() {
		return model.BackupAssetRecoveryEvidence{}, ErrInvalidRecoveryWorker
	}
	ctx = recoveryWorkerContext(ctx)
	failedAt = failedAt.UTC()
	var evidence model.BackupAssetRecoveryEvidence
	err := coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job model.BackupAssetRecoveryJob
		loaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ?", claim.JobID).Limit(1).Find(&job)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || job.State != string(JobStateRunning) ||
			job.TransitionRevision != claim.TransitionRevision || job.TargetChainRevision != expectedRevision {
			return ErrRecoveryWorkerFenceLost
		}
		var plan model.BackupAssetRecoveryPlan
		loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ?", job.PlanID).Limit(1).Find(&plan)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || PlanState(plan.State) != PlanStateExecuted ||
			plan.BindingDigest != job.PlanBindingDigest {
			return ErrRecoveryWorkerFenceLost
		}
		var checkpoint model.BackupAssetRecoveryCheckpoint
		loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("job_id = ? AND phase = ?", job.ID, CheckpointPhaseOperation).
			Order("sequence DESC").Limit(1).Find(&checkpoint)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || checkpoint.NextTargetRevision != expectedRevision ||
			!validOpaqueID(checkpoint.JobItemID) {
			return ErrRecoveryWorkerFenceLost
		}
		historicalCheckpoint := checkpoint.AttemptID != claim.AttemptID ||
			checkpoint.AttemptFence != claim.AttemptFence || checkpoint.NodeFence != claim.NodeFence
		if historicalCheckpoint && checkpoint.ID != reconciledCheckpointID {
			return ErrRecoveryWorkerFenceLost
		}
		var projectErr error
		evidence, projectErr = coordinator.projectSourceRevalidationFailureTx(
			ctx, tx, claim, plan, job, checkpoint, sourceOutcome, failedAt,
			coordinator.now().UTC(), historicalCheckpoint,
		)
		return projectErr
	})
	if err != nil {
		if errors.Is(err, ErrRecoveryWorkerFenceLost) || errors.Is(err, ErrRecoverySourceChanged) {
			return model.BackupAssetRecoveryEvidence{}, err
		}
		return model.BackupAssetRecoveryEvidence{}, fmt.Errorf(
			"%w: project completed recovery source failure", ErrRecoveryWorkerUnavailable,
		)
	}
	coordinator.observeJobOutcome(ctx, claim.JobID, JobStateNeedsAttention)
	return evidence, nil
}

func (coordinator *WorkerCoordinator) projectSourceRevalidationFailureTx(
	ctx context.Context,
	tx *gorm.DB,
	claim RecoveryWorkerClaim,
	plan model.BackupAssetRecoveryPlan,
	job model.BackupAssetRecoveryJob,
	checkpoint model.BackupAssetRecoveryCheckpoint,
	sourceOutcome SourceRevalidationOutcome,
	failedAt time.Time,
	now time.Time,
	historicalCheckpoint bool,
) (model.BackupAssetRecoveryEvidence, error) {
	if tx == nil || !validRecoveryWorkerClaim(claim) || sourceOutcome == SourceRevalidationMatched ||
		!sourceOutcome.Valid() || failedAt.IsZero() || now.IsZero() || failedAt.After(now) ||
		PlanState(plan.State) != PlanStateExecuted || plan.ID != job.PlanID ||
		job.ID != claim.JobID || job.State != string(JobStateRunning) ||
		job.TransitionRevision != claim.TransitionRevision ||
		checkpoint.JobID != job.ID || checkpoint.JobItemID == "" ||
		checkpoint.Phase != string(CheckpointPhaseOperation) ||
		checkpoint.NextTargetRevision != job.TargetChainRevision || checkpoint.UnresolvedCategory != "" ||
		checkpoint.WriteResultDigest != "" || checkpoint.WriteTargetRevision != "" ||
		checkpoint.ObservationDigest != "" || checkpoint.ObservedTargetRevision != "" ||
		checkpoint.ObservedPresence != "" || checkpoint.SourceRevalidationOutcome != "" {
		return model.BackupAssetRecoveryEvidence{}, ErrRecoveryWorkerFenceLost
	}
	checkpointMatchesCurrent := checkpoint.AttemptID == claim.AttemptID &&
		checkpoint.AttemptFence == claim.AttemptFence && checkpoint.NodeFence == claim.NodeFence
	if historicalCheckpoint == checkpointMatchesCurrent ||
		(historicalCheckpoint && (!validOpaqueID(checkpoint.AttemptID) ||
			checkpoint.AttemptFence == 0 || checkpoint.NodeFence == 0)) {
		return model.BackupAssetRecoveryEvidence{}, ErrRecoveryWorkerFenceLost
	}

	var currentJob model.BackupAssetRecoveryJob
	loaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ? AND plan_id = ?", job.ID, plan.ID).Limit(1).Find(&currentJob)
	if loaded.Error != nil {
		return model.BackupAssetRecoveryEvidence{}, loaded.Error
	}
	if loaded.RowsAffected != 1 || currentJob.State != job.State ||
		currentJob.TransitionRevision != job.TransitionRevision ||
		currentJob.TargetChainRevision != job.TargetChainRevision ||
		currentJob.WorkspacePhase != job.WorkspacePhase ||
		currentJob.AuthorityGrantID != job.AuthorityGrantID ||
		currentJob.AuthorityBindingDigest != job.AuthorityBindingDigest {
		return model.BackupAssetRecoveryEvidence{}, ErrRecoveryWorkerFenceLost
	}
	var currentCheckpoint model.BackupAssetRecoveryCheckpoint
	loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ? AND job_id = ?", checkpoint.ID, job.ID).Limit(1).Find(&currentCheckpoint)
	if loaded.Error != nil {
		return model.BackupAssetRecoveryEvidence{}, loaded.Error
	}
	if loaded.RowsAffected != 1 || currentCheckpoint != checkpoint {
		return model.BackupAssetRecoveryEvidence{}, ErrRecoveryWorkerFenceLost
	}
	var item model.BackupAssetRecoveryJobItem
	loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ? AND job_id = ? AND plan_id = ?", checkpoint.JobItemID, job.ID, plan.ID).
		Limit(1).Find(&item)
	if loaded.Error != nil {
		return model.BackupAssetRecoveryEvidence{}, loaded.Error
	}
	wantOutcome := "succeeded"
	if RecoveryOperationKind(item.OperationKind) == RecoveryOperationSkip {
		wantOutcome = "skipped"
	}
	if loaded.RowsAffected != 1 || item.Outcome != wantOutcome || item.FailureCategory != "" ||
		recoveryJobItemOperationDigest(item) != checkpoint.OperationDigest {
		return model.BackupAssetRecoveryEvidence{}, ErrRecoveryWorkerFenceLost
	}
	if historicalCheckpoint && RecoveryOperationKind(item.OperationKind) != RecoveryOperationOverwrite {
		return model.BackupAssetRecoveryEvidence{}, ErrRecoveryWorkerFenceLost
	}
	if (RecoveryOperationKind(item.OperationKind) == RecoveryOperationSkip) !=
		(checkpoint.NextTargetRevision == checkpoint.PriorTargetRevision) {
		return model.BackupAssetRecoveryEvidence{}, ErrRecoveryWorkerFenceLost
	}
	var attempt model.BackupAssetRecoveryAttempt
	loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ? AND job_id = ?", claim.AttemptID, job.ID).Limit(1).Find(&attempt)
	if loaded.Error != nil {
		return model.BackupAssetRecoveryEvidence{}, loaded.Error
	}
	if loaded.RowsAffected != 1 || !attempt.MutationArmed ||
		!matchesCurrentRecoveryAttemptFence(attempt, claim, now) {
		return model.BackupAssetRecoveryEvidence{}, ErrRecoveryWorkerFenceLost
	}
	var source model.RecoveryPointLease
	loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ?", claim.SourceFence.LeaseID).Limit(1).Find(&source)
	if loaded.Error != nil {
		return model.BackupAssetRecoveryEvidence{}, loaded.Error
	}
	if loaded.RowsAffected != 1 ||
		!matchesCurrentRecoverySourceFence(source, claim.SourceFence, plan.RecoveryPointID, now) {
		return model.BackupAssetRecoveryEvidence{}, ErrRecoveryWorkerFenceLost
	}
	var node model.BackupAssetRecoveryNodeLease
	loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ?", claim.NodeLeaseID).Limit(1).Find(&node)
	if loaded.Error != nil {
		return model.BackupAssetRecoveryEvidence{}, loaded.Error
	}
	if loaded.RowsAffected != 1 || !matchesCurrentRecoveryNodeFence(node, claim, job, now) {
		return model.BackupAssetRecoveryEvidence{}, ErrRecoveryWorkerFenceLost
	}

	evidenceID, err := backupasset.NewOpaqueID()
	if err != nil {
		return model.BackupAssetRecoveryEvidence{}, err
	}
	jobID, planID, checkpointID, grantID := job.ID, plan.ID, checkpoint.ID, job.AuthorityGrantID
	attemptID, sourceLeaseID, nodeLeaseID := claim.AttemptID, claim.SourceFence.LeaseID, claim.NodeLeaseID
	evidence := model.BackupAssetRecoveryEvidence{
		ID: evidenceID, JobID: &jobID, PlanID: &planID, CheckpointID: &checkpointID, GrantID: &grantID,
		AttemptID: &attemptID, SourceLeaseID: &sourceLeaseID, NodeLeaseID: &nodeLeaseID,
		NodeLeaseFence: claim.NodeFence, Kind: "failure", Outcome: "needs_attention",
		SummaryDigest: framedDigest(
			"xirang/recovery/source-revalidation-failure/v1", claim.JobID, claim.AttemptID,
			claim.NodeLeaseID, strconv.FormatUint(claim.AttemptFence, 10),
			strconv.FormatUint(claim.NodeFence, 10), checkpoint.ID, checkpoint.JobItemID,
			checkpoint.OperationDigest, checkpoint.PriorTargetRevision, checkpoint.NextTargetRevision,
			string(sourceOutcome), failedAt.UTC().Format(time.RFC3339Nano),
		),
		DifferenceCount: 0, VerifiedAt: timePointerValue(failedAt.UTC()),
		CreatedAt: failedAt.UTC(), UpdatedAt: now,
	}
	if err := tx.WithContext(ctx).Create(&evidence).Error; err != nil {
		return model.BackupAssetRecoveryEvidence{}, err
	}
	updated := tx.WithContext(ctx).Model(&model.BackupAssetRecoveryAttempt{}).
		Where(`id = ? AND job_id = ? AND owner_id = ? AND fence = ? AND state = ?
			AND mutation_armed = ? AND lease_expires_at > ?`,
			claim.AttemptID, claim.JobID, claim.WorkerID, claim.AttemptFence,
			AttemptStateRunning, true, now).
		Updates(map[string]any{"state": string(AttemptStateFailed), "closed_at": now, "updated_at": now})
	if updated.Error != nil {
		return model.BackupAssetRecoveryEvidence{}, updated.Error
	}
	if updated.RowsAffected != 1 {
		return model.BackupAssetRecoveryEvidence{}, ErrRecoveryWorkerFenceLost
	}
	if err := coordinator.sourceLeases.ReleaseTx(ctx, tx, claim.SourceFence); err != nil {
		return model.BackupAssetRecoveryEvidence{}, recoveryWorkerSourceError(err)
	}
	updated = tx.WithContext(ctx).Model(&model.BackupAssetRecoveryNodeLease{}).
		Where("id = ? AND job_id = ? AND attempt_id = ? AND owner_id = ? AND fence = ? AND state = ?",
			claim.NodeLeaseID, claim.JobID, claim.AttemptID, claim.WorkerID, claim.NodeFence, "active").
		Updates(map[string]any{"state": "released", "released_at": now, "updated_at": now})
	if updated.Error != nil {
		return model.BackupAssetRecoveryEvidence{}, updated.Error
	}
	if updated.RowsAffected != 1 {
		return model.BackupAssetRecoveryEvidence{}, ErrRecoveryWorkerFenceLost
	}
	nextWorkspacePhase := job.WorkspacePhase
	if TargetMode(job.TargetMode) == TargetModeIsolated {
		nextWorkspacePhase = string(WorkspacePhaseCleanupDue)
	}
	updated = tx.WithContext(ctx).Model(&model.BackupAssetRecoveryJob{}).
		Where(`id = ? AND state = ? AND failure_category = '' AND transition_revision = ?
			AND workspace_phase = ? AND target_chain_revision = ?`,
			job.ID, JobStateRunning, job.TransitionRevision, job.WorkspacePhase, job.TargetChainRevision).
		Updates(map[string]any{
			"state": string(JobStateNeedsAttention), "failure_category": "source_revalidation_failed",
			"transition_revision": job.TransitionRevision + 1, "workspace_phase": nextWorkspacePhase,
			"updated_at": now,
		})
	if updated.Error != nil {
		return model.BackupAssetRecoveryEvidence{}, updated.Error
	}
	if updated.RowsAffected != 1 {
		return model.BackupAssetRecoveryEvidence{}, ErrRecoveryWorkerFenceLost
	}
	return evidence, nil
}

func (coordinator *WorkerCoordinator) projectOrdinaryPostPauseFailure(
	ctx context.Context,
	claim RecoveryWorkerClaim,
	handoff interruptedOperationHandoff,
	expectedRevision string,
	deleteGrantSecret string,
	sourceOutcome SourceRevalidationOutcome,
	failedAt time.Time,
) (model.BackupAssetRecoveryEvidence, error) {
	if coordinator == nil || coordinator.db == nil || coordinator.sourceLeases == nil ||
		!validRecoveryWorkerClaim(claim) || handoff.operation.Kind != RecoveryOperationDelete ||
		!validOpaqueRevision(expectedRevision) || !sourceOutcome.Valid() || failedAt.IsZero() {
		return model.BackupAssetRecoveryEvidence{}, ErrInvalidRecoveryWorker
	}
	ctx = recoveryWorkerContext(ctx)
	if ctx.Err() != nil {
		ctx = context.WithoutCancel(ctx)
	}
	failedAt = failedAt.UTC()
	observedAuthority, err := coordinator.observeRecoveryAuthority(ctx, handoff.plan, handoff.preflight)
	if err != nil {
		return model.BackupAssetRecoveryEvidence{}, err
	}

	var evidence model.BackupAssetRecoveryEvidence
	err = coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := coordinator.now().UTC()
		if failedAt.After(now) {
			return ErrRecoveryWorkerFenceLost
		}
		fence, err := coordinator.lockOrdinaryExecutionTx(
			ctx, tx, claim, handoff, expectedRevision, now, observedAuthority,
		)
		if err != nil {
			return err
		}

		var checkpoints []model.BackupAssetRecoveryCheckpoint
		loaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("job_id = ?", fence.job.ID).Order("sequence ASC").Find(&checkpoints)
		if loaded.Error != nil {
			return loaded.Error
		}
		required, hasRequired, _, err := validateInPlaceOrdinaryCheckpointHistory(
			fence.plan, fence.job, claim, checkpoints, handoff.checkpointOperations, now,
		)
		if err != nil || !hasRequired || len(checkpoints) == 0 {
			return ErrRecoveryWorkerFenceLost
		}
		last := checkpoints[len(checkpoints)-1]
		deleteGrantID := ""
		if handoff.deleteAuthorityConsumed {
			requiredCheckpoint, consumed, found := ordinaryConsumedDeleteCheckpoints(checkpoints)
			if !found || validateConsumedOrdinaryDeleteGrantTx(
				ctx, tx, fence.plan, fence.job, requiredCheckpoint, consumed,
			) != nil {
				return ErrRecoveryWorkerFenceLost
			}
			deleteGrantID = consumed.DeleteGrantID
		} else if CheckpointPhase(last.Phase) == CheckpointPhaseDeleteAuthorityRequired {
			grant, validateErr := validatePendingOrdinaryDeleteGrantTx(
				ctx, tx, fence.plan, fence.job, claim, required, deleteGrantSecret, now,
			)
			if validateErr != nil {
				return validateErr
			}
			deleteGrantID = grant.ID
		} else {
			return ErrRecoveryWorkerFenceLost
		}

		updated := tx.WithContext(ctx).Model(&model.BackupAssetRecoveryJobItem{}).
			Where("id = ? AND job_id = ? AND outcome = '' AND failure_category = ''", fence.item.ID, fence.job.ID).
			Updates(map[string]any{
				"outcome": "failed", "bytes_written": int64(0), "verified_size": int64(0),
				"verified_digest": "", "failure_category": recoveryPostPauseFailureCategory,
				"updated_at": now,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrRecoveryWorkerFenceLost
		}

		evidenceID, err := backupasset.NewOpaqueID()
		if err != nil {
			return err
		}
		jobID, planID, checkpointID := fence.job.ID, fence.plan.ID, last.ID
		attemptID, sourceLeaseID, nodeLeaseID := claim.AttemptID, claim.SourceFence.LeaseID, claim.NodeLeaseID
		evidence = model.BackupAssetRecoveryEvidence{
			ID: evidenceID, JobID: &jobID, PlanID: &planID, CheckpointID: &checkpointID,
			GrantID: &deleteGrantID, AttemptID: &attemptID, SourceLeaseID: &sourceLeaseID,
			NodeLeaseID: &nodeLeaseID, NodeLeaseFence: claim.NodeFence,
			Kind: "failure", Outcome: "needs_attention",
			SummaryDigest: framedDigest(
				"xirang/recovery/post-pause-failure/v1", claim.JobID, claim.AttemptID,
				claim.NodeLeaseID, strconv.FormatUint(claim.AttemptFence, 10),
				strconv.FormatUint(claim.NodeFence, 10), last.ID, fence.item.ID,
				handoff.operationDigest, expectedRevision, deleteGrantID,
				string(sourceOutcome), failedAt.Format(time.RFC3339Nano),
			),
			DifferenceCount: 0, VerifiedAt: timePointerValue(failedAt),
			CreatedAt: failedAt, UpdatedAt: now,
		}
		if err := tx.WithContext(ctx).Create(&evidence).Error; err != nil {
			return err
		}

		updated = tx.WithContext(ctx).Model(&model.BackupAssetRecoveryAttempt{}).
			Where(`id = ? AND job_id = ? AND owner_id = ? AND fence = ? AND state = ?
				AND mutation_armed = ? AND lease_expires_at > ?`,
				claim.AttemptID, claim.JobID, claim.WorkerID, claim.AttemptFence,
				AttemptStateRunning, true, now).
			Updates(map[string]any{"state": string(AttemptStateFailed), "closed_at": now, "updated_at": now})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrRecoveryWorkerFenceLost
		}
		if err := coordinator.sourceLeases.ReleaseTx(ctx, tx, recoverySourceFence(fence.source)); err != nil {
			return recoveryWorkerSourceError(err)
		}
		updated = tx.WithContext(ctx).Model(&model.BackupAssetRecoveryNodeLease{}).
			Where("id = ? AND job_id = ? AND attempt_id = ? AND owner_id = ? AND fence = ? AND state = ?",
				fence.node.ID, fence.job.ID, claim.AttemptID, claim.WorkerID, claim.NodeFence, "active").
			Updates(map[string]any{"state": "released", "released_at": now, "updated_at": now})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrRecoveryWorkerFenceLost
		}
		updated = tx.WithContext(ctx).Model(&model.BackupAssetRecoveryJob{}).
			Where(`id = ? AND state = ? AND failure_category = '' AND transition_revision = ?
				AND workspace_phase = ? AND target_chain_revision = ?`,
				fence.job.ID, JobStateRunning, fence.job.TransitionRevision,
				fence.job.WorkspacePhase, expectedRevision).
			Updates(map[string]any{
				"state": string(JobStateNeedsAttention), "failure_category": recoveryPostPauseFailureCategory,
				"transition_revision": fence.job.TransitionRevision + 1, "updated_at": now,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrRecoveryWorkerFenceLost
		}
		return nil
	})
	if err == nil {
		coordinator.observeJobOutcome(ctx, claim.JobID, JobStateNeedsAttention)
	}
	return evidence, err
}

func validatePendingOrdinaryDeleteGrantTx(
	ctx context.Context,
	tx *gorm.DB,
	plan model.BackupAssetRecoveryPlan,
	job model.BackupAssetRecoveryJob,
	claim RecoveryWorkerClaim,
	required model.BackupAssetRecoveryCheckpoint,
	secret string,
	now time.Time,
) (model.BackupAssetRecoveryGrant, error) {
	var grant model.BackupAssetRecoveryGrant
	if tx == nil || !validAuthorizationGrantSecret(secret) || required.ID == "" ||
		CheckpointPhase(required.Phase) != CheckpointPhaseDeleteAuthorityRequired {
		return grant, ErrRecoveryWorkerFenceLost
	}
	loaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("plan_id = ? AND job_id = ? AND delete_checkpoint_id = ? AND authority_category = ?",
			plan.ID, job.ID, required.ID, AuthorityExactMirrorDelete).
		Limit(1).Find(&grant)
	if loaded.Error != nil {
		return grant, loaded.Error
	}
	secretHash := authorizationGrantSecretHash(
		AuthorizationReceiptCategoryExactMirrorDelete, plan.ID, job.ID, required.ID, secret,
	)
	bindingDigest := framedDigest(
		recoveryAuthorizationGrantBindingDomain, string(AuthorizationReceiptCategoryExactMirrorDelete),
		plan.ID, job.ID, required.ID, secretHash, grant.ExpiresAt.Format(time.RFC3339Nano),
	)
	if loaded.RowsAffected != 1 || grant.JobID == nil || *grant.JobID != job.ID ||
		grant.DeleteCheckpointID == nil || *grant.DeleteCheckpointID != required.ID ||
		grant.DeleteAttemptID == nil || *grant.DeleteAttemptID != claim.AttemptID ||
		grant.DeleteSetDigest != job.DeleteSetDigest || grant.DeleteTargetRevision != job.TargetChainRevision ||
		grant.DeleteAttemptFence != claim.AttemptFence || grant.DeleteNodeFence != claim.NodeFence ||
		grant.BindingDigest != bindingDigest || grant.ConsumedAt != nil || grant.RevokedAt != nil ||
		!grant.ExpiresAt.After(now) || required.DeleteAuthorityExpiresAt == nil ||
		grant.ExpiresAt.After(*required.DeleteAuthorityExpiresAt) ||
		subtle.ConstantTimeCompare([]byte(grant.GrantHash), []byte(secretHash)) != 1 {
		return model.BackupAssetRecoveryGrant{}, ErrRecoveryWorkerFenceLost
	}
	return grant, nil
}

func publicOrdinaryExecutionError(err error) error {
	if errors.Is(err, errOrdinaryTargetVerificationMismatch) ||
		errors.Is(err, errOrdinaryRemoteOutcomeUnresolved) {
		return ErrInvalidTargetVerification
	}
	if errors.Is(err, provider.ErrRsyncRestoreSourceDrift) {
		return ErrRecoverySourceChanged
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, ErrInvalidRecoveryWorker) || errors.Is(err, ErrRecoveryWorkerFenceLost) ||
		errors.Is(err, ErrRecoverySourceChanged) {
		return err
	}
	return fmt.Errorf("%w: execute ordinary recovery claim", ErrRecoveryWorkerUnavailable)
}

// TargetChainAdvance is the immutable input committed by one successful
// operation checkpoint. A later attempt may adopt a remote write only by
// reproducing this exact current-fence product.
type TargetChainAdvance struct {
	PriorRevision        string
	OperationDigest      string
	PlanItemID           string
	SourceRevisionDigest string
	AttemptID            string
	AttemptFence         uint64
	NodeFence            uint64
	VerifiedIdentity     string
	TargetRevision       string
}

func (advance TargetChainAdvance) NextRevision() (string, error) {
	if !validOpaqueRevision(advance.PriorRevision) || !validDigest(advance.OperationDigest) ||
		!validOpaqueID(advance.PlanItemID) || !validDigest(advance.SourceRevisionDigest) ||
		!validOpaqueID(advance.AttemptID) || advance.AttemptFence == 0 || advance.NodeFence == 0 ||
		!validDigest(advance.VerifiedIdentity) || !validOpaqueRevision(advance.TargetRevision) {
		return "", ErrInvalidTargetChain
	}
	return framedDigest(
		targetChainRevisionDomain,
		advance.PriorRevision,
		advance.OperationDigest,
		advance.PlanItemID,
		advance.SourceRevisionDigest,
		advance.AttemptID,
		strconv.FormatUint(advance.AttemptFence, 10),
		strconv.FormatUint(advance.NodeFence, 10),
		advance.VerifiedIdentity,
		advance.TargetRevision,
	), nil
}

// TargetAbsenceChainAdvance commits a verified exact absence for one delete
// job item. The separate domain prevents an absent target from being confused
// with a present target identity in the ordinary target chain.
type TargetAbsenceChainAdvance struct {
	PriorRevision        string
	OperationDigest      string
	JobItemID            string
	SourceRevisionDigest string
	AttemptID            string
	AttemptFence         uint64
	NodeFence            uint64
	AbsenceEvidence      TargetAbsenceEvidenceKind
	TargetRevision       string
}

func (advance TargetAbsenceChainAdvance) NextRevision() (string, error) {
	if !validOpaqueRevision(advance.PriorRevision) || !validDigest(advance.OperationDigest) ||
		!validOpaqueID(advance.JobItemID) || !validDigest(advance.SourceRevisionDigest) ||
		!validOpaqueID(advance.AttemptID) || advance.AttemptFence == 0 || advance.NodeFence == 0 ||
		advance.AbsenceEvidence != TargetAbsenceEvidenceExact ||
		!validOpaqueRevision(advance.TargetRevision) {
		return "", ErrInvalidTargetChain
	}
	return framedDigest(
		targetAbsenceChainRevisionDomain,
		advance.PriorRevision,
		advance.OperationDigest,
		advance.JobItemID,
		advance.SourceRevisionDigest,
		advance.AttemptID,
		strconv.FormatUint(advance.AttemptFence, 10),
		strconv.FormatUint(advance.NodeFence, 10),
		string(advance.AbsenceEvidence),
		advance.TargetRevision,
	), nil
}

// recoveryJobItemOperationDigest binds an adopted target observation to the
// exact immutable job-item projection rather than an aggregate operation set.
func recoveryJobItemOperationDigest(item model.BackupAssetRecoveryJobItem) string {
	planItemID := ""
	if item.PlanItemID != nil {
		planItemID = *item.PlanItemID
	}
	return framedDigest(
		recoveryOperationRowDigestDomain,
		item.ID,
		item.PlanID,
		item.JobID,
		planItemID,
		strconv.Itoa(item.Ordinal),
		item.OperationKind,
		item.TargetPathDigest,
		item.SemanticTargetDigest,
		item.TargetObjectDigest,
		item.ExpectedPriorKind,
		item.ExpectedPriorDigest,
		item.ExpectedPostIdentityDigest,
		strconv.FormatInt(item.ExpectedPostBytes, 10),
		strconv.FormatInt(item.ExpectedPriorBytes, 10),
		item.EncryptedTargetRelativeLocator,
		strconv.Itoa(item.TargetLocatorKeyVersion),
		strconv.Itoa(item.TargetLocatorCipherVersion),
		item.DisplayClass,
		strconv.FormatInt(item.EstimatedBytes, 10),
	)
}
