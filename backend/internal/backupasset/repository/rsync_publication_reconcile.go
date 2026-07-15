package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type rsyncPreparingClaim struct {
	point            model.RecoveryPoint
	lineage          backupasset.PublicationLineageV1
	consistency      backupasset.PublicationConsistencyV1
	lease            backupasset.Lease
	attempt          provider.RsyncTreeAttemptV1
	childFenceDigest string
}

type rsyncVerifyingClaim struct {
	point       model.RecoveryPoint
	lineage     backupasset.PublicationLineageV1
	consistency backupasset.PublicationConsistencyV1
	locator     managedRsyncPointLocatorV1
	lease       backupasset.Lease
	attempt     provider.RsyncTreeAttemptV1
}

func (service *PublicationService) processRsyncPreparingPoint(ctx context.Context, pointID string) (publication.Outcome, error) {
	token, err := service.admission.Acquire(ctx, publication.OperationReconcile)
	if err != nil {
		return publication.Outcome{}, err
	}
	defer func() { _ = token.Close() }()
	if token.Mode() != publication.AdmissionManaged {
		return publication.Outcome{}, fmt.Errorf("%w: managed Rsync reconciliation is not admitted", backupasset.ErrForbidden)
	}
	leaseConfig, err := service.foundation.LeaseConfig()
	if err != nil {
		return publication.Outcome{}, err
	}
	claim, err := service.claimRsyncPreparingPoint(ctx, pointID)
	if err != nil {
		return publication.Outcome{}, err
	}
	runtime, err := service.loadExactManagedRsyncPublicationRuntime(ctx, claim.lineage.TaskID)
	if err != nil {
		return publication.Outcome{}, err
	}
	if runtime.repository.ID != claim.point.RepositoryID || runtime.link.ID != claim.attempt.TaskRepositoryLinkID ||
		runtime.binding.validateForAttempt(claim.attempt) != nil {
		return publication.Outcome{}, fmt.Errorf("%w: managed Rsync reconciliation binding drift", backupasset.ErrConflict)
	}
	markerKey, err := service.rsyncMarkerKey(ctx, runtime.repository.ID)
	if err != nil {
		return publication.Outcome{}, err
	}
	input, err := service.rsyncTreeReconcileInput(runtime.binding, markerKey, claim.attempt.RecoveryPointID, claim.childFenceDigest)
	if err != nil {
		return publication.Outcome{}, err
	}
	strategy, err := service.registry.PublicationStrategy(backupasset.ProviderRsync)
	if err != nil {
		return publication.Outcome{}, err
	}
	workCtx, stopHeartbeat := service.startPublicationHeartbeat(ctx, claim.lease.Fence, leaseConfig)
	service.metrics.ObserveAttempt(backupasset.ProviderRsync, publication.StageReconciliation)
	reconcileResult, reconcileErr := strategy.Reconcile(workCtx, provider.PublicationReconcileRequest{
		Attempt: provider.NewRsyncTreePublicationAttempt(claim.attempt), RsyncTreeInput: &input,
	})
	workCause := context.Cause(workCtx)
	stopHeartbeat()
	if workCause != nil && reconcileErr == nil {
		reconcileErr = workCause
	}
	if reconcileErr != nil {
		return publication.Outcome{}, reconcileErr
	}
	fact, err := reconcileResult.RsyncTreeResult()
	if err != nil {
		return publication.Outcome{}, err
	}
	switch fact.State {
	case provider.RsyncTreeReconcileFinal:
		outcome, err := service.recordReconciledRsyncProviderCommit(ctx, claim, runtime.binding, markerKey, fact)
		if err != nil {
			return publication.Outcome{}, err
		}
		service.metrics.ObserveOutcome(backupasset.ProviderRsync, publication.StageReconciliation, backupasset.PublicationOutcomeSuccess)
		return outcome, nil
	case provider.RsyncTreeReconcileStaging:
		return service.failRsyncPreparingReconciliation(ctx, claim, backupasset.FailureProviderCompletionUnproven)
	case provider.RsyncTreeReconcileAbsent:
		return publication.Outcome{
			RepositoryID: claim.point.RepositoryID, RecoveryPointID: claim.point.ID, TaskID: claim.attempt.TaskID, TaskRunID: claim.attempt.TaskRunID,
			State: backupasset.RecoveryPointPreparing,
		}, nil
	default:
		return publication.Outcome{}, fmt.Errorf("%w: unsupported managed Rsync reconciliation state", backupasset.ErrInvalidState)
	}
}

func (service *PublicationService) failRsyncPreparingReconciliation(ctx context.Context, claim rsyncPreparingClaim, code backupasset.PublicationFailureCode) (publication.Outcome, error) {
	if backupasset.ValidatePublicationFailureCode(code) != nil {
		return publication.Outcome{}, fmt.Errorf("%w: invalid managed Rsync reconciliation failure", backupasset.ErrInvalidState)
	}
	var outcome publication.Outcome
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var point model.RecoveryPoint
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&point, "id = ?", claim.point.ID).Error; err != nil {
			return fmt.Errorf("lock failed managed Rsync reconciliation point: %w", err)
		}
		if point.State != string(backupasset.RecoveryPointPreparing) || point.ConsistencyJSON != claim.point.ConsistencyJSON {
			return fmt.Errorf("%w: managed Rsync preparing point changed", backupasset.ErrLeaseFenceLost)
		}
		if err := service.lease.ValidateFenceTx(ctx, tx, claim.lease.Fence); err != nil {
			return err
		}
		if err := backupasset.ValidateRecoveryPointTransition(rsyncPointProfile(point, claim.attempt.PublicationMode), backupasset.RecoveryPointFailed); err != nil {
			return err
		}
		consistency := claim.consistency
		consistency.Completion = ""
		consistency.Code = code
		encoded, err := backupasset.EncodePublicationConsistency(consistency)
		if err != nil {
			return err
		}
		point.ConsistencyJSON = encoded
		point.State = string(backupasset.RecoveryPointFailed)
		point.UpdatedAt = service.now().UTC()
		if err := tx.Save(&point).Error; err != nil {
			return fmt.Errorf("save failed managed Rsync reconciliation point: %w", err)
		}
		if err := service.lease.ReleaseTx(ctx, tx, claim.lease.Fence); err != nil {
			return err
		}
		outcome = publication.Outcome{
			RepositoryID: point.RepositoryID, RecoveryPointID: point.ID, TaskID: claim.attempt.TaskID, TaskRunID: claim.attempt.TaskRunID,
			State: backupasset.RecoveryPointFailed, Code: code,
		}
		return nil
	})
	return outcome, err
}

func (service *PublicationService) processRsyncVerifyingPoint(ctx context.Context, pointID string) (publication.Outcome, error) {
	token, err := service.admission.Acquire(ctx, publication.OperationManifest)
	if err != nil {
		return publication.Outcome{}, err
	}
	defer func() { _ = token.Close() }()
	if token.Mode() != publication.AdmissionManaged {
		return publication.Outcome{}, fmt.Errorf("%w: managed Rsync verification is not admitted", backupasset.ErrForbidden)
	}
	leaseConfig, err := service.foundation.LeaseConfig()
	if err != nil {
		return publication.Outcome{}, err
	}
	claim, err := service.claimRsyncVerifyingPoint(ctx, pointID)
	if err != nil {
		return publication.Outcome{}, err
	}
	runtime, err := service.loadExactManagedRsyncPublicationRuntime(ctx, claim.lineage.TaskID)
	if err != nil {
		return publication.Outcome{}, err
	}
	if runtime.repository.ID != claim.point.RepositoryID || runtime.link.ID != claim.attempt.TaskRepositoryLinkID ||
		runtime.binding.validateForAttempt(claim.attempt) != nil {
		return publication.Outcome{}, fmt.Errorf("%w: managed Rsync verification binding drift", backupasset.ErrConflict)
	}
	markerKey, err := service.rsyncMarkerKey(ctx, runtime.repository.ID)
	if err != nil {
		return publication.Outcome{}, err
	}
	input, err := service.rsyncTreeReconcileInput(runtime.binding, markerKey, claim.attempt.RecoveryPointID, claim.locator.ChildFenceDigest)
	if err != nil {
		return publication.Outcome{}, err
	}
	strategy, err := service.registry.PublicationStrategy(backupasset.ProviderRsync)
	if err != nil {
		return publication.Outcome{}, err
	}
	workCtx, stopHeartbeat := service.startPublicationHeartbeat(ctx, claim.lease.Fence, leaseConfig)
	service.metrics.ObserveAttempt(backupasset.ProviderRsync, publication.StageManifest)
	reconcileResult, reconcileErr := strategy.Reconcile(workCtx, provider.PublicationReconcileRequest{
		Attempt: provider.NewRsyncTreePublicationAttempt(claim.attempt), RsyncTreeInput: &input,
	})
	workCause := context.Cause(workCtx)
	stopHeartbeat()
	if workCause != nil && reconcileErr == nil {
		reconcileErr = workCause
	}
	if reconcileErr != nil {
		return publication.Outcome{}, reconcileErr
	}
	fact, err := reconcileResult.RsyncTreeResult()
	if err != nil {
		return publication.Outcome{}, err
	}
	if fact.State != provider.RsyncTreeReconcileFinal {
		return service.failRsyncVerifyingReconciliation(ctx, claim, backupasset.FailureManifestUnavailable)
	}
	outcome, err := service.commitReconciledRsyncManifest(ctx, claim, runtime.binding, markerKey, fact)
	if err != nil {
		return publication.Outcome{}, err
	}
	service.metrics.ObserveOutcome(backupasset.ProviderRsync, publication.StageManifest, backupasset.PublicationOutcomeSuccess)
	return outcome, nil
}

func (service *PublicationService) failRsyncVerifyingReconciliation(ctx context.Context, claim rsyncVerifyingClaim, code backupasset.PublicationFailureCode) (publication.Outcome, error) {
	if backupasset.ValidatePublicationFailureCode(code) != nil {
		return publication.Outcome{}, fmt.Errorf("%w: invalid managed Rsync verification failure", backupasset.ErrInvalidState)
	}
	var outcome publication.Outcome
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var point model.RecoveryPoint
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&point, "id = ?", claim.point.ID).Error; err != nil {
			return fmt.Errorf("lock failed managed Rsync verifying point: %w", err)
		}
		if point.State != string(backupasset.RecoveryPointVerifying) || point.ConsistencyJSON != claim.point.ConsistencyJSON {
			return fmt.Errorf("%w: managed Rsync verifying point changed", backupasset.ErrLeaseFenceLost)
		}
		if err := service.lease.ValidateFenceTx(ctx, tx, claim.lease.Fence); err != nil {
			return err
		}
		if err := backupasset.ValidateRecoveryPointTransition(rsyncPointProfile(point, claim.attempt.PublicationMode), backupasset.RecoveryPointFailed); err != nil {
			return err
		}
		consistency := claim.consistency
		consistency.Code = code
		encoded, err := backupasset.EncodePublicationConsistency(consistency)
		if err != nil {
			return err
		}
		point.ConsistencyJSON = encoded
		point.State = string(backupasset.RecoveryPointFailed)
		point.UpdatedAt = service.now().UTC()
		if err := tx.Save(&point).Error; err != nil {
			return fmt.Errorf("save failed managed Rsync verifying point: %w", err)
		}
		if err := service.lease.ReleaseTx(ctx, tx, claim.lease.Fence); err != nil {
			return err
		}
		capturedAt := time.Time{}
		if point.CapturedAt != nil {
			capturedAt = point.CapturedAt.UTC()
		}
		outcome = publication.Outcome{
			RepositoryID: point.RepositoryID, RecoveryPointID: point.ID, TaskID: claim.attempt.TaskID, TaskRunID: claim.attempt.TaskRunID,
			State: backupasset.RecoveryPointFailed, CapturedAt: capturedAt, ProviderCommitRecorded: true, Code: code,
		}
		return nil
	})
	return outcome, err
}

func (service *PublicationService) claimRsyncPreparingPoint(ctx context.Context, pointID string) (rsyncPreparingClaim, error) {
	var claim rsyncPreparingClaim
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var point model.RecoveryPoint
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&point, "id = ?", pointID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: managed Rsync publication point", backupasset.ErrNotFound)
			}
			return fmt.Errorf("lock managed Rsync preparing point: %w", err)
		}
		if point.State != string(backupasset.RecoveryPointPreparing) {
			return fmt.Errorf("%w: managed Rsync point is no longer preparing", backupasset.ErrConflict)
		}
		providerKind, lineage, consistency, err := publicationReconciliationFacts(point)
		if err != nil {
			return err
		}
		if providerKind != backupasset.ProviderRsync {
			return fmt.Errorf("%w: managed Rsync preparing point provider mismatch", backupasset.ErrInvalidState)
		}
		attempt, childFenceDigest, err := decodeManagedRsyncPreparedAttemptRecord(point.EncryptedProviderLocator)
		if err != nil {
			return err
		}
		if err := validateRsyncPreparingClaim(point, lineage, attempt, childFenceDigest); err != nil {
			return err
		}
		lease, err := service.acquireOrTakeoverPublicationLeaseTx(ctx, tx, point.ID, lineage.PointDeadlineAt, "managed Rsync preparing reconciliation")
		if err != nil {
			return err
		}
		now := service.now().UTC()
		consistency.PublicationRevision++
		consistency.AttemptCount++
		consistency.LastAttemptAt = &now
		encoded, err := backupasset.EncodePublicationConsistency(consistency)
		if err != nil {
			return err
		}
		point.ConsistencyJSON = encoded
		point.UpdatedAt = now
		if err := tx.Save(&point).Error; err != nil {
			return fmt.Errorf("record managed Rsync reconciliation claim: %w", err)
		}
		claim = rsyncPreparingClaim{
			point: point, lineage: lineage, consistency: consistency, lease: lease, attempt: attempt, childFenceDigest: childFenceDigest,
		}
		return nil
	})
	return claim, err
}

func (service *PublicationService) claimRsyncVerifyingPoint(ctx context.Context, pointID string) (rsyncVerifyingClaim, error) {
	var claim rsyncVerifyingClaim
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var point model.RecoveryPoint
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&point, "id = ?", pointID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: managed Rsync verification point", backupasset.ErrNotFound)
			}
			return fmt.Errorf("lock managed Rsync verifying point: %w", err)
		}
		if point.State != string(backupasset.RecoveryPointVerifying) {
			return fmt.Errorf("%w: managed Rsync point is no longer verifying", backupasset.ErrConflict)
		}
		providerKind, lineage, consistency, err := publicationReconciliationFacts(point)
		if err != nil {
			return err
		}
		if providerKind != backupasset.ProviderRsync || consistency.Provider != backupasset.ProviderRsync ||
			!isLowerHex64(consistency.ProviderCommitDigest) || !isLowerHex64(consistency.RepositoryIdentityDigest) {
			return fmt.Errorf("%w: managed Rsync verifying point lacks durable provider evidence", backupasset.ErrConflict)
		}
		locator, err := decodeManagedRsyncPointLocator(point.EncryptedProviderLocator)
		if err != nil {
			return err
		}
		attempt, err := provider.DecodeRsyncTreeAttemptV1(locator.TaggedAttempt)
		if err != nil {
			return err
		}
		if err := validateRsyncPreparingClaim(point, lineage, attempt, locator.ChildFenceDigest); err != nil {
			return err
		}
		if locator.RepositoryID != point.RepositoryID || locator.CommitMarkerDigest == "" || locator.ManagedRootIdentityDigest != attempt.ManagedRootIdentityDigest {
			return fmt.Errorf("%w: managed Rsync verifying locator drift", backupasset.ErrConflict)
		}
		lease, err := service.acquireOrTakeoverPublicationLeaseTx(ctx, tx, point.ID, lineage.PointDeadlineAt, "managed Rsync manifest reconciliation")
		if err != nil {
			return err
		}
		now := service.now().UTC()
		consistency.PublicationRevision++
		consistency.AttemptCount++
		consistency.LastAttemptAt = &now
		encoded, err := backupasset.EncodePublicationConsistency(consistency)
		if err != nil {
			return err
		}
		point.ConsistencyJSON = encoded
		point.UpdatedAt = now
		if err := tx.Save(&point).Error; err != nil {
			return fmt.Errorf("record managed Rsync verification claim: %w", err)
		}
		claim = rsyncVerifyingClaim{point: point, lineage: lineage, consistency: consistency, locator: locator, lease: lease, attempt: attempt}
		return nil
	})
	return claim, err
}

func validateRsyncPreparingClaim(point model.RecoveryPoint, lineage backupasset.PublicationLineageV1, attempt provider.RsyncTreeAttemptV1, childFenceDigest string) error {
	if err := attempt.Validate(); err != nil {
		return err
	}
	expectedSemantics := backupasset.PointXirangManifest
	if attempt.ImportedBaseline {
		expectedSemantics = backupasset.PointImportedBaseline
	}
	if !isLowerHex64(childFenceDigest) || point.Semantics != string(expectedSemantics) || point.RepositoryID != attempt.RepositoryID || point.ProducingTaskID == nil || *point.ProducingTaskID != attempt.TaskID ||
		point.ProducingTaskRunID == nil || *point.ProducingTaskRunID != attempt.TaskRunID || lineage.TaskRepositoryLinkID != attempt.TaskRepositoryLinkID ||
		lineage.TaskID != attempt.TaskID || lineage.TaskRunID != attempt.TaskRunID || lineage.PublicationMode != string(attempt.PublicationMode) ||
		!lineage.PointDeadlineAt.Equal(attempt.PointDeadlineAt.UTC()) || point.ID != attempt.RecoveryPointID {
		return fmt.Errorf("%w: managed Rsync preparing attempt identity mismatch", backupasset.ErrConflict)
	}
	return nil
}

func (service *PublicationService) rsyncTreeReconcileInput(binding managedRsyncBindingDocumentV2, markerKey []byte, pointID, childFenceDigest string) (provider.RsyncTreeReconcileInput, error) {
	if len(markerKey) == 0 || validateManagedRsyncBindingDocumentV2(binding) != nil || backupasset.ValidateOpaqueID(pointID) != nil || !isLowerHex64(childFenceDigest) {
		return provider.RsyncTreeReconcileInput{}, fmt.Errorf("%w: managed Rsync reconciliation input is invalid", backupasset.ErrInvalidState)
	}
	config, err := service.foundation.PublicationConfig()
	if err != nil {
		return provider.RsyncTreeReconcileInput{}, err
	}
	maxBytes := config.ManifestMaxBytes
	if maxBytes > provider.MaxRsyncTreeMetadataBytes {
		maxBytes = provider.MaxRsyncTreeMetadataBytes
	}
	return provider.RsyncTreeReconcileInput{
		ManagedRoot: binding.ManagedRootLocator, MarkerKey: append([]byte(nil), markerKey...),
		SourceFingerprint: managedRsyncSourceFingerprint(markerKey, binding, pointID), ChildFenceDigest: childFenceDigest,
		ManifestLimits: provider.ManifestLimits{
			Timeout: config.ManifestTimeout, MaxBytes: maxBytes, MaxEntries: config.ManifestMaxEntries,
			MaxRecordBytes: config.ManifestMaxRecordBytes, MaxDepth: config.ManifestMaxDepth,
		},
	}, nil
}

func (service *PublicationService) recordReconciledRsyncProviderCommit(ctx context.Context, claim rsyncPreparingClaim, binding managedRsyncBindingDocumentV2, markerKey []byte, fact provider.RsyncTreeReconcileV1) (publication.Outcome, error) {
	if err := fact.Validate(); err != nil || fact.State != provider.RsyncTreeReconcileFinal || fact.Commit == nil {
		if err != nil {
			return publication.Outcome{}, err
		}
		return publication.Outcome{}, fmt.Errorf("%w: final managed Rsync reconciliation fact required", backupasset.ErrInvalidState)
	}
	evidence := *fact.Commit
	if err := validateReconciledRsyncCommitEvidence(claim.attempt, binding, markerKey, claim.childFenceDigest, evidence); err != nil {
		return publication.Outcome{}, err
	}
	var outcome publication.Outcome
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var point model.RecoveryPoint
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&point, "id = ?", claim.point.ID).Error; err != nil {
			return fmt.Errorf("lock reconciled managed Rsync point: %w", err)
		}
		if point.State != string(backupasset.RecoveryPointPreparing) || point.ConsistencyJSON != claim.point.ConsistencyJSON {
			return fmt.Errorf("%w: reconciled managed Rsync point changed", backupasset.ErrLeaseFenceLost)
		}
		if err := service.lease.ValidateFenceTx(ctx, tx, claim.lease.Fence); err != nil {
			return err
		}
		if err := validateRsyncPreparingClaim(point, claim.lineage, claim.attempt, claim.childFenceDigest); err != nil {
			return err
		}
		locatorPayload, err := encodeManagedRsyncPointLocatorForAttempt(claim.attempt, evidence)
		if err != nil {
			return err
		}
		commitDigest, err := canonicalRsyncProviderCommitDigest(claim.attempt, evidence)
		if err != nil {
			return err
		}
		fidelityPayload, err := encodeManagedRsyncFidelity(evidence.FidelityDigest)
		if err != nil {
			return err
		}
		consistency := claim.consistency
		consistency.Provider = backupasset.ProviderRsync
		consistency.RepositoryIdentityDigest = binding.ManagedRootIdentityDigest
		consistency.ProviderCommitDigest = commitDigest
		consistency.CapabilityRevision = point.CapabilityRevision
		encodedConsistency, err := backupasset.EncodePublicationConsistency(consistency)
		if err != nil {
			return err
		}
		if err := backupasset.ValidateRecoveryPointTransition(rsyncPointProfile(point, claim.attempt.PublicationMode), backupasset.RecoveryPointVerifying); err != nil {
			return err
		}
		capturedAt := evidence.ProviderCommittedAt.UTC()
		point.EncryptedProviderLocator = locatorPayload
		point.SourceFingerprint = evidence.SourceFingerprint
		point.ManifestDigestAlgorithm = evidence.ManifestDigestAlgorithm
		point.ManifestDigest = evidence.ManifestDigest
		point.EntryCount = int64(evidence.ManifestEntryCount)
		point.LogicalBytes = int64(evidence.LogicalBytes)
		point.FidelityJSON = fidelityPayload
		point.ConsistencyJSON = encodedConsistency
		point.CapturedAt = &capturedAt
		point.State = string(backupasset.RecoveryPointVerifying)
		point.UpdatedAt = service.now().UTC()
		if err := tx.Save(&point).Error; err != nil {
			if isPublicationManagedTreeSourceConflict(err) {
				return fmt.Errorf("%w: managed Rsync tree is already claimed", backupasset.ErrConflict)
			}
			return fmt.Errorf("save reconciled managed Rsync point: %w", err)
		}
		var repository model.BackupRepository
		if err := tx.First(&repository, "id = ?", point.RepositoryID).Error; err != nil {
			return fmt.Errorf("load reconciled managed Rsync repository: %w", err)
		}
		if err := upsertManagedRsyncHistoryLatchesTx(ctx, tx, repository, point, evidence.ProviderCommittedAt.UTC(), service.now().UTC()); err != nil {
			return err
		}
		if err := service.lease.ReleaseTx(ctx, tx, claim.lease.Fence); err != nil {
			return err
		}
		outcome = publication.Outcome{
			RepositoryID: point.RepositoryID, RecoveryPointID: point.ID, TaskID: claim.attempt.TaskID, TaskRunID: claim.attempt.TaskRunID,
			State: backupasset.RecoveryPointVerifying, CapturedAt: capturedAt, ProviderCommitRecorded: true,
		}
		return nil
	})
	return outcome, err
}

func validateReconciledRsyncCommitEvidence(attempt provider.RsyncTreeAttemptV1, binding managedRsyncBindingDocumentV2, markerKey []byte, childFenceDigest string, evidence provider.RsyncTreeCommitV1) error {
	if err := attempt.Validate(); err != nil {
		return err
	}
	if err := binding.validateForAttempt(attempt); err != nil {
		return err
	}
	if err := evidence.Validate(); err != nil {
		return err
	}
	if !isLowerHex64(childFenceDigest) || evidence.RepositoryID != attempt.RepositoryID || evidence.TaskRepositoryLinkID != attempt.TaskRepositoryLinkID ||
		evidence.RecoveryPointID != attempt.RecoveryPointID || evidence.AttemptID != attempt.AttemptID || evidence.PublicationMode != attempt.PublicationMode ||
		!evidence.PointDeadlineAt.Equal(attempt.PointDeadlineAt.UTC()) || !evidence.ProviderCommittedAt.UTC().Before(attempt.PointDeadlineAt.UTC()) ||
		evidence.SourceFingerprint != managedRsyncSourceFingerprint(markerKey, binding, attempt.RecoveryPointID) || evidence.ChildFenceDigest != childFenceDigest {
		return fmt.Errorf("%w: reconciled managed Rsync provider commit evidence mismatch", backupasset.ErrConflict)
	}
	if evidence.ManifestEntryCount > uint64(^uint64(0)>>1) || evidence.LogicalBytes > uint64(^uint64(0)>>1) {
		return fmt.Errorf("%w: reconciled managed Rsync provider commit exceeds model bounds", backupasset.ErrInvalidState)
	}
	if attempt.PublicationMode == backupasset.PublicationVersionedHardlink &&
		(evidence.ParentRecoveryPointID != attempt.ParentRecoveryPointID || evidence.ParentCommitDigest != attempt.ParentCommitDigest) {
		return fmt.Errorf("%w: reconciled managed Rsync provider commit parent mismatch", backupasset.ErrConflict)
	}
	return nil
}

func (service *PublicationService) commitReconciledRsyncManifest(ctx context.Context, claim rsyncVerifyingClaim, binding managedRsyncBindingDocumentV2, markerKey []byte, fact provider.RsyncTreeReconcileV1) (publication.Outcome, error) {
	if err := fact.Validate(); err != nil || fact.State != provider.RsyncTreeReconcileFinal || fact.Commit == nil || fact.Manifest == nil {
		if err != nil {
			return publication.Outcome{}, err
		}
		return publication.Outcome{}, fmt.Errorf("%w: final managed Rsync manifest fact required", backupasset.ErrInvalidState)
	}
	evidence := *fact.Commit
	if err := validateReconciledRsyncCommitEvidence(claim.attempt, binding, markerKey, claim.locator.ChildFenceDigest, evidence); err != nil {
		return publication.Outcome{}, err
	}
	if claim.locator.CommitMarkerDigest != evidence.CommitMarkerDigest {
		return publication.Outcome{}, fmt.Errorf("%w: managed Rsync verifying marker digest changed", backupasset.ErrConflict)
	}
	commitDigest, err := canonicalRsyncProviderCommitDigest(claim.attempt, evidence)
	if err != nil {
		return publication.Outcome{}, err
	}
	if claim.consistency.ProviderCommitDigest != commitDigest || claim.consistency.RepositoryIdentityDigest != binding.ManagedRootIdentityDigest {
		return publication.Outcome{}, fmt.Errorf("%w: managed Rsync verifying provider evidence drift", backupasset.ErrConflict)
	}
	fidelityPayload, err := encodeManagedRsyncFidelity(fact.Manifest.FidelityDigest)
	if err != nil {
		return publication.Outcome{}, err
	}
	commitEvidencePayload, err := json.Marshal(struct {
		Version int                        `json:"version"`
		Commit  provider.RsyncTreeCommitV1 `json:"commit"`
	}{Version: managedRsyncCommitEvidenceVersion, Commit: evidence})
	if err != nil {
		return publication.Outcome{}, fmt.Errorf("encode managed Rsync manifest commit evidence: %w", err)
	}
	manifestID, err := rsyncManifestID(claim.point.ID, claim.lease.Fence.AttemptID)
	if err != nil {
		return publication.Outcome{}, err
	}
	var outcome publication.Outcome
	err = service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var point model.RecoveryPoint
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&point, "id = ?", claim.point.ID).Error; err != nil {
			return fmt.Errorf("lock committed managed Rsync point: %w", err)
		}
		if point.State != string(backupasset.RecoveryPointVerifying) || point.ConsistencyJSON != claim.point.ConsistencyJSON {
			return fmt.Errorf("%w: managed Rsync verifying point changed", backupasset.ErrLeaseFenceLost)
		}
		if err := service.lease.ValidateFenceTx(ctx, tx, claim.lease.Fence); err != nil {
			return err
		}
		locator, err := decodeManagedRsyncPointLocator(point.EncryptedProviderLocator)
		if err != nil {
			return err
		}
		if locator != claim.locator || point.SourceFingerprint != evidence.SourceFingerprint || point.ManifestDigestAlgorithm != evidence.ManifestDigestAlgorithm ||
			point.ManifestDigest != evidence.ManifestDigest || point.EntryCount != int64(evidence.ManifestEntryCount) || point.LogicalBytes != int64(evidence.LogicalBytes) {
			return fmt.Errorf("%w: managed Rsync verifying point evidence changed", backupasset.ErrConflict)
		}
		if err := backupasset.ValidateRecoveryPointTransition(rsyncPointProfile(point, claim.attempt.PublicationMode), backupasset.RecoveryPointCommitted); err != nil {
			return err
		}
		var existing model.RecoveryPointManifest
		if err := tx.Where("id = ?", manifestID).Limit(1).Find(&existing).Error; err != nil {
			return fmt.Errorf("load managed Rsync manifest row: %w", err)
		}
		if existing.ID != "" {
			return fmt.Errorf("%w: managed Rsync manifest claim already exists", backupasset.ErrConflict)
		}
		var latestRevision int
		if err := tx.Model(&model.RecoveryPointManifest{}).Where("recovery_point_id = ?", point.ID).Select("COALESCE(MAX(revision), 0)").Scan(&latestRevision).Error; err != nil {
			return fmt.Errorf("load managed Rsync manifest revision: %w", err)
		}
		now := service.now().UTC()
		manifest := model.RecoveryPointManifest{
			ID: manifestID, RecoveryPointID: point.ID, Revision: latestRevision + 1, DigestAlgorithm: fact.Manifest.DigestAlgorithm, Digest: fact.Manifest.Digest,
			Generator: "xirang-rsync-tree", GeneratorVersion: "1", Completeness: string(backupasset.ManifestComplete),
			EntryCount: int64(fact.Manifest.EntryCount), LogicalBytes: int64(fact.Manifest.LogicalBytes), FidelityJSON: fidelityPayload,
			EncryptedCommitEvidence: string(commitEvidencePayload), IsActive: true, CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.Create(&manifest).Error; err != nil {
			return fmt.Errorf("create managed Rsync manifest: %w", err)
		}
		capturedAt := evidence.ProviderCommittedAt.UTC()
		point.FidelityJSON = fidelityPayload
		point.CapturedAt = &capturedAt
		point.CommittedAt = &now
		point.State = string(backupasset.RecoveryPointCommitted)
		point.UpdatedAt = now
		if err := tx.Save(&point).Error; err != nil {
			return fmt.Errorf("save committed managed Rsync point: %w", err)
		}
		if err := clearCommittedRsyncFullCopySeedTx(ctx, tx, binding, claim.attempt, now); err != nil {
			return err
		}
		if err := service.lease.ReleaseTx(ctx, tx, claim.lease.Fence); err != nil {
			return err
		}
		outcome = publication.Outcome{
			RepositoryID: point.RepositoryID, RecoveryPointID: point.ID, TaskID: claim.attempt.TaskID, TaskRunID: claim.attempt.TaskRunID,
			State: backupasset.RecoveryPointCommitted, CapturedAt: capturedAt, ProviderCommitRecorded: true,
		}
		return nil
	})
	return outcome, err
}

// clearCommittedRsyncFullCopySeedTx flips the long-term hardlink binding only
// after its specifically marked full-copy seed is durably committed. The
// attempt retains SeedFullCopy for restart/reconciliation validation after the
// binding moves on to ordinary hardlink attempts.
func clearCommittedRsyncFullCopySeedTx(ctx context.Context, tx *gorm.DB, binding managedRsyncBindingDocumentV2, attempt provider.RsyncTreeAttemptV1, now time.Time) error {
	if !attempt.SeedFullCopy {
		return nil
	}
	if tx == nil || binding.PublicationMode != backupasset.PublicationVersionedHardlink || !binding.SeedFullCopyRequired ||
		attempt.PublicationMode != backupasset.PublicationVersionedFullCopy || attempt.RepositoryID != binding.RepositoryID ||
		attempt.TaskRepositoryLinkID != binding.TaskRepositoryLinkID {
		return fmt.Errorf("%w: invalid managed Rsync full-copy seed commit", backupasset.ErrConflict)
	}
	var access model.RepositoryAccessBinding
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("repository_id = ? AND status = ?", binding.RepositoryID, bindingStatusActive).First(&access).Error; err != nil {
		return fmt.Errorf("lock managed Rsync seed binding: %w", err)
	}
	stored, err := decodeStoredBindingDocument(access.EncryptedConfig)
	if err != nil || stored.ManagedRsyncV2 == nil || stored.V1 != nil {
		if err != nil {
			return err
		}
		return fmt.Errorf("%w: managed Rsync seed binding changed", backupasset.ErrConflict)
	}
	document := *stored.ManagedRsyncV2
	if document != binding {
		return fmt.Errorf("%w: managed Rsync seed binding drift", backupasset.ErrConflict)
	}
	document.SeedFullCopyRequired = false
	payload, err := encodeManagedRsyncBindingDocumentV2(document)
	if err != nil {
		return err
	}
	access.EncryptedConfig = payload
	access.UpdatedAt = now.UTC()
	if err := tx.Save(&access).Error; err != nil {
		return fmt.Errorf("clear managed Rsync full-copy seed requirement: %w", err)
	}
	return nil
}

func rsyncManifestID(pointID, attemptID string) (string, error) {
	if backupasset.ValidateOpaqueID(pointID) != nil || backupasset.ValidateOpaqueID(attemptID) != nil {
		return "", fmt.Errorf("%w: invalid managed Rsync manifest identity", backupasset.ErrInvalidState)
	}
	sum := sha256.Sum256([]byte("xirang.rsync.tree.manifest.v1\x00" + pointID + "\x00" + attemptID))
	return hex.EncodeToString(sum[:16]), nil
}

func encodeManagedRsyncFidelity(digest string) (string, error) {
	if !isLowerHex64(digest) {
		return "", fmt.Errorf("%w: invalid managed Rsync fidelity digest", backupasset.ErrInvalidState)
	}
	payload, err := json.Marshal(struct {
		Version int    `json:"version"`
		Digest  string `json:"digest"`
	}{Version: managedRsyncCommitEvidenceVersion, Digest: digest})
	if err != nil {
		return "", fmt.Errorf("encode managed Rsync fidelity evidence: %w", err)
	}
	return string(payload), nil
}
