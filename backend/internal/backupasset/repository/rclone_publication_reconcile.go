package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type rclonePreparingClaim struct {
	point            model.RecoveryPoint
	lineage          backupasset.PublicationLineageV1
	consistency      backupasset.PublicationConsistencyV1
	lease            backupasset.Lease
	attempt          provider.RcloneAttemptV1
	childFenceDigest string
}

type rcloneVerifyingClaim struct {
	point       model.RecoveryPoint
	lineage     backupasset.PublicationLineageV1
	consistency backupasset.PublicationConsistencyV1
	locator     managedRclonePointLocatorV1
	lease       backupasset.Lease
	attempt     provider.RcloneAttemptV1
	commit      provider.RcloneCommitV1
}

func (service *PublicationService) processRclonePreparingPoint(ctx context.Context, pointID string) (publication.Outcome, error) {
	token, err := service.admission.Acquire(ctx, publication.OperationReconcile)
	if err != nil {
		return publication.Outcome{}, err
	}
	defer func() { _ = token.Close() }()
	if token.Mode() != publication.AdmissionManaged {
		return publication.Outcome{}, fmt.Errorf("%w: managed Rclone reconciliation is not admitted", backupasset.ErrForbidden)
	}
	leaseConfig, err := service.foundation.LeaseConfig()
	if err != nil {
		return publication.Outcome{}, err
	}
	claim, err := service.claimRclonePreparingPoint(ctx, pointID)
	if err != nil {
		return publication.Outcome{}, err
	}
	runtime, err := service.loadExactManagedRclonePublicationRuntime(ctx, claim.lineage.TaskID)
	if err != nil {
		return publication.Outcome{}, err
	}
	if err := validateRcloneReconcileRuntime(runtime, claim.point, claim.attempt); err != nil {
		return publication.Outcome{}, err
	}
	markerKey, err := service.rcloneMarkerKey(ctx, runtime.repository.ID)
	if err != nil {
		return publication.Outcome{}, err
	}
	input, err := service.rcloneReconcileInput(ctx, runtime, claim.attempt, markerKey, leaseConfig, "", "")
	if err != nil {
		return publication.Outcome{}, err
	}
	strategy, err := service.registry.PublicationStrategy(backupasset.ProviderRclone)
	if err != nil {
		return publication.Outcome{}, err
	}
	workCtx, stopHeartbeat := service.startPublicationHeartbeat(ctx, claim.lease.Fence, leaseConfig)
	service.metrics.ObserveAttempt(backupasset.ProviderRclone, publication.StageReconciliation)
	result, reconcileErr := strategy.Reconcile(workCtx, provider.PublicationReconcileRequest{
		Attempt: provider.NewRclonePublicationAttempt(claim.attempt), RcloneInput: &input,
	})
	workCause := context.Cause(workCtx)
	stopHeartbeat()
	if workCause != nil && reconcileErr == nil {
		reconcileErr = workCause
	}
	if reconcileErr != nil {
		return publication.Outcome{}, reconcileErr
	}
	fact, err := result.RcloneResult()
	if err != nil {
		return publication.Outcome{}, err
	}
	switch fact.State {
	case provider.RcloneReconcileProviderCommitted:
		outcome, err := service.recordReconciledRcloneProviderCommit(ctx, claim, runtime.binding, markerKey, fact)
		if err == nil {
			service.metrics.ObserveOutcome(backupasset.ProviderRclone, publication.StageReconciliation, backupasset.PublicationOutcomeSuccess)
			_ = service.tryWake(outcome.RecoveryPointID)
		}
		return outcome, err
	case provider.RcloneReconcileAbsent:
		if err := service.releaseRcloneReconcileLease(ctx, claim.point.ID, claim.lease.Fence, backupasset.RecoveryPointPreparing); err != nil {
			return publication.Outcome{}, err
		}
		return publication.Outcome{
			RepositoryID: claim.point.RepositoryID, RecoveryPointID: claim.point.ID,
			TaskID: claim.attempt.TaskID, TaskRunID: claim.attempt.TaskRunID, State: backupasset.RecoveryPointPreparing,
		}, nil
	case provider.RcloneReconcileIncomplete:
		return service.failRcloneReconciliation(ctx, claim.point, claim.consistency, claim.lease.Fence, claim.attempt,
			backupasset.RecoveryPointPreparing, backupasset.FailureProviderCompletionUnproven)
	default:
		return publication.Outcome{}, fmt.Errorf("%w: unsupported managed Rclone reconciliation state", backupasset.ErrInvalidState)
	}
}

func (service *PublicationService) processRcloneVerifyingPoint(ctx context.Context, pointID string) (publication.Outcome, error) {
	token, err := service.admission.Acquire(ctx, publication.OperationManifest)
	if err != nil {
		return publication.Outcome{}, err
	}
	defer func() { _ = token.Close() }()
	if token.Mode() != publication.AdmissionManaged {
		return publication.Outcome{}, fmt.Errorf("%w: managed Rclone verification is not admitted", backupasset.ErrForbidden)
	}
	leaseConfig, err := service.foundation.LeaseConfig()
	if err != nil {
		return publication.Outcome{}, err
	}
	claim, err := service.claimRcloneVerifyingPoint(ctx, pointID)
	if err != nil {
		return publication.Outcome{}, err
	}
	runtime, err := service.loadExactManagedRclonePublicationRuntime(ctx, claim.lineage.TaskID)
	if err != nil {
		return publication.Outcome{}, err
	}
	if err := validateRcloneReconcileRuntime(runtime, claim.point, claim.attempt); err != nil {
		return publication.Outcome{}, err
	}
	markerKey, err := service.rcloneMarkerKey(ctx, runtime.repository.ID)
	if err != nil {
		return publication.Outcome{}, err
	}
	input, err := service.rcloneReconcileInput(
		ctx, runtime, claim.attempt, markerKey, leaseConfig,
		claim.locator.NativeCommitKey, claim.locator.NativeCommitVersionID,
	)
	if err != nil {
		return publication.Outcome{}, err
	}
	strategy, err := service.registry.PublicationStrategy(backupasset.ProviderRclone)
	if err != nil {
		return publication.Outcome{}, err
	}
	workCtx, stopHeartbeat := service.startPublicationHeartbeat(ctx, claim.lease.Fence, leaseConfig)
	service.metrics.ObserveAttempt(backupasset.ProviderRclone, publication.StageManifest)
	result, reconcileErr := strategy.Reconcile(workCtx, provider.PublicationReconcileRequest{
		Attempt: provider.NewRclonePublicationAttempt(claim.attempt), RcloneInput: &input,
	})
	workCause := context.Cause(workCtx)
	stopHeartbeat()
	if workCause != nil && reconcileErr == nil {
		reconcileErr = workCause
	}
	if reconcileErr != nil {
		return publication.Outcome{}, reconcileErr
	}
	fact, err := result.RcloneResult()
	if err != nil {
		return publication.Outcome{}, err
	}
	if fact.State != provider.RcloneReconcileProviderCommitted {
		return service.failRcloneReconciliation(ctx, claim.point, claim.consistency, claim.lease.Fence, claim.attempt,
			backupasset.RecoveryPointVerifying, backupasset.FailureManifestUnavailable)
	}
	outcome, err := service.commitReconciledRcloneManifest(ctx, claim, fact)
	if err == nil {
		service.metrics.ObserveOutcome(backupasset.ProviderRclone, publication.StageManifest, backupasset.PublicationOutcomeSuccess)
	}
	return outcome, err
}

func (service *PublicationService) claimRclonePreparingPoint(ctx context.Context, pointID string) (rclonePreparingClaim, error) {
	var claim rclonePreparingClaim
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var point model.RecoveryPoint
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&point, "id = ?", pointID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: managed Rclone publication point", backupasset.ErrNotFound)
			}
			return fmt.Errorf("lock managed Rclone preparing point: %w", err)
		}
		if point.State != string(backupasset.RecoveryPointPreparing) {
			return fmt.Errorf("%w: managed Rclone point is no longer preparing", backupasset.ErrConflict)
		}
		kind, lineage, consistency, err := publicationReconciliationFacts(point)
		if err != nil || kind != backupasset.ProviderRclone {
			return fmt.Errorf("%w: managed Rclone preparing point provider mismatch", backupasset.ErrInvalidState)
		}
		attempt, childFenceDigest, err := decodeManagedRclonePreparedAttemptRecord(point.EncryptedProviderLocator)
		if err != nil {
			return err
		}
		if err := validateRclonePreparingClaim(point, lineage, attempt, childFenceDigest); err != nil {
			return err
		}
		lease, err := service.acquireOrTakeoverPublicationLeaseTx(ctx, tx, point.ID, lineage.PointDeadlineAt, "managed Rclone preparing reconciliation")
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
			return fmt.Errorf("record managed Rclone reconciliation claim: %w", err)
		}
		claim = rclonePreparingClaim{
			point: point, lineage: lineage, consistency: consistency, lease: lease,
			attempt: attempt, childFenceDigest: childFenceDigest,
		}
		return nil
	})
	return claim, err
}

func (service *PublicationService) claimRcloneVerifyingPoint(ctx context.Context, pointID string) (rcloneVerifyingClaim, error) {
	var claim rcloneVerifyingClaim
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var point model.RecoveryPoint
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&point, "id = ?", pointID).Error; err != nil {
			return fmt.Errorf("lock managed Rclone verifying point: %w", err)
		}
		if point.State != string(backupasset.RecoveryPointVerifying) {
			return fmt.Errorf("%w: managed Rclone point is no longer verifying", backupasset.ErrConflict)
		}
		kind, lineage, consistency, err := publicationReconciliationFacts(point)
		if err != nil || kind != backupasset.ProviderRclone || consistency.Provider != backupasset.ProviderRclone ||
			!isLowerHex64(consistency.ProviderCommitDigest) {
			return fmt.Errorf("%w: managed Rclone verifying point lacks durable provider evidence", backupasset.ErrConflict)
		}
		locator, err := decodeManagedRclonePointLocator(point.EncryptedProviderLocator)
		if err != nil {
			return err
		}
		attempt, err := provider.DecodeRcloneAttemptV1(locator.TaggedAttempt)
		if err != nil {
			return err
		}
		commit, err := provider.DecodeRcloneCommitV1(locator.TaggedCommit)
		if err != nil {
			return err
		}
		if commit.Native != nil {
			commit.Native.CommitKey = locator.NativeCommitKey
			commit.Native.CommitVersionID = locator.NativeCommitVersionID
		}
		if err := validateRclonePreparingClaim(point, lineage, attempt, locator.ChildFenceDigest); err != nil {
			return err
		}
		if digest, err := canonicalRcloneProviderCommitDigest(commit); err != nil || digest != consistency.ProviderCommitDigest ||
			digest != locator.ProviderCommitDigest {
			return fmt.Errorf("%w: managed Rclone verifying commit digest drift", backupasset.ErrConflict)
		}
		lease, err := service.acquireOrTakeoverPublicationLeaseTx(ctx, tx, point.ID, lineage.PointDeadlineAt, "managed Rclone manifest reconciliation")
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
			return fmt.Errorf("record managed Rclone verification claim: %w", err)
		}
		claim = rcloneVerifyingClaim{
			point: point, lineage: lineage, consistency: consistency, locator: locator,
			lease: lease, attempt: attempt, commit: commit,
		}
		return nil
	})
	return claim, err
}

func validateRclonePreparingClaim(
	point model.RecoveryPoint,
	lineage backupasset.PublicationLineageV1,
	attempt provider.RcloneAttemptV1,
	childFenceDigest string,
) error {
	if err := attempt.Validate(); err != nil {
		return err
	}
	expectedSemantics := backupasset.PointXirangManifest
	if attempt.ImportedBaseline {
		expectedSemantics = backupasset.PointImportedBaseline
	}
	if !isLowerHex64(childFenceDigest) || childFenceDigest != attempt.ChildFenceDigest || point.Semantics != string(expectedSemantics) ||
		point.RepositoryID != attempt.RepositoryID || point.ProducingTaskID == nil || *point.ProducingTaskID != attempt.TaskID ||
		point.ProducingTaskRunID == nil || *point.ProducingTaskRunID != attempt.TaskRunID || point.ID != attempt.RecoveryPointID ||
		lineage.TaskRepositoryLinkID != attempt.TaskRepositoryLinkID || lineage.TaskID != attempt.TaskID || lineage.TaskRunID != attempt.TaskRunID ||
		lineage.PublicationMode != string(attempt.PublicationMode) || !lineage.PointDeadlineAt.Equal(attempt.PointDeadlineAt.UTC()) {
		return fmt.Errorf("%w: managed Rclone preparing attempt identity mismatch", backupasset.ErrConflict)
	}
	return nil
}

func validateRcloneReconcileRuntime(runtime managedRclonePublicationRuntime, point model.RecoveryPoint, attempt provider.RcloneAttemptV1) error {
	binding := runtime.binding
	if runtime.repository.ID != point.RepositoryID || runtime.link.ID != attempt.TaskRepositoryLinkID || binding.RepositoryID != attempt.RepositoryID ||
		binding.TaskRepositoryLinkID != attempt.TaskRepositoryLinkID || binding.TaskID != attempt.TaskID || binding.PublicationMode != attempt.PublicationMode ||
		binding.BindingRevision != attempt.BindingRevision || binding.ConfigRevision != attempt.ConfigRevision ||
		binding.CapabilityRevision != attempt.CapabilityRevision || binding.CredentialRevision != attempt.CredentialRevision ||
		binding.PreflightID != attempt.PreflightID || binding.PreflightRevision != attempt.PreflightRevision ||
		binding.PreflightDigest != attempt.PreflightDigest || binding.ManagedRootIdentityDigest != attempt.ManagedRootIdentityDigest {
		return fmt.Errorf("%w: managed Rclone reconciliation binding drift", backupasset.ErrConflict)
	}
	if attempt.PublicationMode == backupasset.PublicationVersionedPrefix {
		if binding.Portable == nil || attempt.Portable == nil || binding.Portable.ConfigDigest != attempt.ConfigDigest {
			return fmt.Errorf("%w: managed Rclone portable reconciliation binding drift", backupasset.ErrConflict)
		}
		return nil
	}
	if binding.Native == nil || attempt.Native == nil || binding.Native.ProfileCode != attempt.Native.ProfileCode ||
		binding.Native.VersioningDigest != attempt.Native.VersioningDigest || binding.Native.LifecycleDigest != attempt.Native.LifecycleDigest ||
		binding.Native.BucketEncryptionDigest != attempt.Native.BucketEncryptionDigest ||
		binding.Native.EncryptionProfile != attempt.Native.EncryptionProfile ||
		binding.Native.KMSCapabilityRevision != attempt.Native.KMSCapabilityRevision {
		return fmt.Errorf("%w: managed Rclone native reconciliation binding drift", backupasset.ErrConflict)
	}
	return nil
}

func (service *PublicationService) rcloneReconcileInput(
	ctx context.Context,
	runtime managedRclonePublicationRuntime,
	attempt provider.RcloneAttemptV1,
	markerKey []byte,
	leaseConfig backupasset.LeaseConfig,
	exactCommitKey string,
	exactCommitVersionID string,
) (provider.RcloneReconcileInput, error) {
	publicationConfig, err := service.foundation.PublicationConfig()
	if err != nil {
		return provider.RcloneReconcileInput{}, err
	}
	var nativeInput *managedRcloneNativeProcessInput
	if attempt.PublicationMode == backupasset.PublicationNativeObjectVersions {
		nativeInput, err = service.prepareRcloneNativeProcessInput(
			ctx, runtime.binding, markerKey, leaseConfig, publicationConfig, service.now().UTC(), attempt.PointDeadlineAt, false, nil,
		)
		if err != nil {
			return provider.RcloneReconcileInput{}, err
		}
	}
	execution := &rclonePublicationExecution{
		service: service, attempt: attempt, binding: runtime.binding, task: runtime.task,
		markerKey: append([]byte(nil), markerKey...), nativeInput: nativeInput, context: ctx,
	}
	input, err := execution.RclonePublicationInput()
	if err != nil {
		return provider.RcloneReconcileInput{}, err
	}
	if input.PortableRequest != nil {
		input.PortableRequest.Manifest = provider.RcloneManifestBundle{}
	}
	if input.NativeRequest != nil {
		input.NativeRequest.Manifest = provider.RcloneManifestBundle{}
		input.NativeRequest.Source = provider.RclonePrivateLocator{}
		input.NativeRequest.RcloneConfig = nil
		input.NativeRequest.Runtime = provider.RemoteCommandAccess{}
		input.NativeRequest.MaxVerifyBytes = 0
		input.NativeRequest.LowLevelRetries = 0
		input.NativeRequest.ExactCommitKey = exactCommitKey
		input.NativeRequest.ExactCommitVersionID = exactCommitVersionID
	}
	return provider.RcloneReconcileInput(input), nil
}

func (service *PublicationService) recordReconciledRcloneProviderCommit(
	ctx context.Context,
	claim rclonePreparingClaim,
	binding managedRcloneBindingDocumentV3,
	markerKey []byte,
	fact provider.RcloneReconcileV1,
) (publication.Outcome, error) {
	if err := fact.Validate(); err != nil || fact.State != provider.RcloneReconcileProviderCommitted || fact.Commit == nil {
		return publication.Outcome{}, fmt.Errorf("%w: committed managed Rclone reconciliation evidence required", backupasset.ErrInvalidState)
	}
	evidence := *fact.Commit
	if err := validateReconciledRcloneCommitEvidence(claim.attempt, binding, evidence); err != nil {
		return publication.Outcome{}, err
	}
	locatorPayload, locator, err := encodeManagedRclonePointLocator(claim.attempt, binding, markerKey, evidence)
	if err != nil {
		return publication.Outcome{}, err
	}
	var outcome publication.Outcome
	err = service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var point model.RecoveryPoint
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&point, "id = ?", claim.point.ID).Error; err != nil {
			return fmt.Errorf("lock reconciled managed Rclone point: %w", err)
		}
		if point.State != string(backupasset.RecoveryPointPreparing) || point.ConsistencyJSON != claim.point.ConsistencyJSON {
			return fmt.Errorf("%w: reconciled managed Rclone point changed", backupasset.ErrLeaseFenceLost)
		}
		if err := service.lease.ValidateFenceTx(ctx, tx, claim.lease.Fence); err != nil {
			return err
		}
		if err := validateRclonePreparingClaim(point, claim.lineage, claim.attempt, claim.childFenceDigest); err != nil {
			return err
		}
		if evidence.ManifestEntryCount > math.MaxInt64 || evidence.LogicalBytes > math.MaxInt64 {
			return fmt.Errorf("%w: reconciled managed Rclone provider commit exceeds model bounds", backupasset.ErrInvalidState)
		}
		fidelity, err := encodeManagedRcloneFidelity(evidence.FidelityEvidenceDigest)
		if err != nil {
			return err
		}
		consistency := claim.consistency
		consistency.Provider = backupasset.ProviderRclone
		consistency.RepositoryIdentityDigest = claim.attempt.RepositoryIdentityDigest
		consistency.ProviderCommitDigest = locator.ProviderCommitDigest
		consistency.CapabilityRevision = point.CapabilityRevision
		encodedConsistency, err := backupasset.EncodePublicationConsistency(consistency)
		if err != nil {
			return err
		}
		if err := backupasset.ValidateRecoveryPointTransition(rclonePointProfile(point, claim.attempt.PublicationMode), backupasset.RecoveryPointVerifying); err != nil {
			return err
		}
		capturedAt := evidence.ProviderCommittedAt.UTC()
		point.EncryptedProviderLocator = locatorPayload
		point.SourceFingerprint = locator.PhysicalIdentityDigest
		point.ManifestDigestAlgorithm = "sha256"
		point.ManifestDigest = evidence.ManifestIndexDigest
		point.EntryCount = int64(evidence.ManifestEntryCount)
		point.LogicalBytes = int64(evidence.LogicalBytes)
		point.FidelityJSON = fidelity
		point.ConsistencyJSON = encodedConsistency
		point.CapturedAt = &capturedAt
		point.State = string(backupasset.RecoveryPointVerifying)
		point.UpdatedAt = service.now().UTC()
		if err := tx.Save(&point).Error; err != nil {
			return fmt.Errorf("save reconciled managed Rclone point: %w", err)
		}
		var repository model.BackupRepository
		if err := tx.First(&repository, "id = ?", point.RepositoryID).Error; err != nil {
			return err
		}
		if err := upsertManagedRcloneHistoryLatchesTx(ctx, tx, repository, point, capturedAt, service.now().UTC()); err != nil {
			return err
		}
		if err := service.lease.ReleaseTx(ctx, tx, claim.lease.Fence); err != nil {
			return err
		}
		outcome = publication.Outcome{
			RepositoryID: point.RepositoryID, RecoveryPointID: point.ID, TaskID: claim.attempt.TaskID,
			TaskRunID: claim.attempt.TaskRunID, State: backupasset.RecoveryPointVerifying,
			CapturedAt: capturedAt, ProviderCommitRecorded: true,
		}
		return nil
	})
	return outcome, err
}

func validateReconciledRcloneCommitEvidence(
	attempt provider.RcloneAttemptV1,
	binding managedRcloneBindingDocumentV3,
	evidence provider.RcloneCommitV1,
) error {
	if err := attempt.Validate(); err != nil {
		return err
	}
	if err := validateManagedRcloneBindingDocumentV3(binding); err != nil {
		return err
	}
	if err := evidence.Validate(); err != nil {
		return err
	}
	if binding.RepositoryID != attempt.RepositoryID || binding.TaskRepositoryLinkID != attempt.TaskRepositoryLinkID ||
		binding.TaskID != attempt.TaskID || binding.PublicationMode != attempt.PublicationMode ||
		evidence.RepositoryID != attempt.RepositoryID || evidence.TaskRepositoryLinkID != attempt.TaskRepositoryLinkID ||
		evidence.RecoveryPointID != attempt.RecoveryPointID || evidence.AttemptID != attempt.AttemptID ||
		evidence.PublicationMode != attempt.PublicationMode || !evidence.PointDeadlineAt.Equal(attempt.PointDeadlineAt.UTC()) ||
		evidence.ProviderCommittedAt.After(attempt.PointDeadlineAt) || evidence.ChildFenceDigest != attempt.ChildFenceDigest ||
		evidence.CapabilityEvidenceDigest != attempt.PreflightDigest {
		return fmt.Errorf("%w: reconciled managed Rclone provider commit evidence mismatch", backupasset.ErrConflict)
	}
	return nil
}

func (service *PublicationService) commitReconciledRcloneManifest(
	ctx context.Context,
	claim rcloneVerifyingClaim,
	fact provider.RcloneReconcileV1,
) (publication.Outcome, error) {
	if err := fact.Validate(); err != nil || fact.State != provider.RcloneReconcileProviderCommitted || fact.Commit == nil || fact.Manifest == nil {
		return publication.Outcome{}, fmt.Errorf("%w: complete managed Rclone manifest evidence required", backupasset.ErrInvalidState)
	}
	encodedCommit, err := provider.EncodeProviderCommit(provider.NewRcloneProviderCommit(*fact.Commit))
	if err != nil || encodedCommit != claim.locator.TaggedCommit ||
		(fact.Commit.Native != nil && (fact.Commit.Native.CommitKey != claim.locator.NativeCommitKey ||
			fact.Commit.Native.CommitVersionID != claim.locator.NativeCommitVersionID)) {
		return publication.Outcome{}, fmt.Errorf("%w: managed Rclone verifying provider evidence drift", backupasset.ErrConflict)
	}
	manifestID, err := rcloneManifestID(claim.point.ID, claim.lease.Fence.AttemptID)
	if err != nil {
		return publication.Outcome{}, err
	}
	var outcome publication.Outcome
	err = service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var point model.RecoveryPoint
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&point, "id = ?", claim.point.ID).Error; err != nil {
			return fmt.Errorf("lock committed managed Rclone point: %w", err)
		}
		if point.State != string(backupasset.RecoveryPointVerifying) || point.ConsistencyJSON != claim.point.ConsistencyJSON {
			return fmt.Errorf("%w: managed Rclone verifying point changed", backupasset.ErrLeaseFenceLost)
		}
		if err := service.lease.ValidateFenceTx(ctx, tx, claim.lease.Fence); err != nil {
			return err
		}
		locator, err := decodeManagedRclonePointLocator(point.EncryptedProviderLocator)
		if err != nil || locator != claim.locator || point.ManifestDigest != fact.Commit.ManifestIndexDigest ||
			point.EntryCount != int64(fact.Commit.ManifestEntryCount) || point.LogicalBytes != int64(fact.Commit.LogicalBytes) {
			return fmt.Errorf("%w: managed Rclone verifying point evidence changed", backupasset.ErrConflict)
		}
		if err := backupasset.ValidateRecoveryPointTransition(rclonePointProfile(point, claim.attempt.PublicationMode), backupasset.RecoveryPointCommitted); err != nil {
			return err
		}
		var existing model.RecoveryPointManifest
		if err := tx.Where("id = ?", manifestID).Limit(1).Find(&existing).Error; err != nil {
			return err
		}
		if existing.ID != "" {
			return fmt.Errorf("%w: managed Rclone manifest already exists", backupasset.ErrConflict)
		}
		now := service.now().UTC()
		manifest := model.RecoveryPointManifest{
			ID: manifestID, RecoveryPointID: point.ID, Revision: 1, DigestAlgorithm: "sha256", Digest: fact.Manifest.ManifestIndexDigest,
			Generator: "xirang-rclone", GeneratorVersion: "1", Completeness: string(backupasset.ManifestComplete),
			EntryCount: int64(fact.Manifest.EntryCount), LogicalBytes: int64(fact.Manifest.LogicalBytes),
			FidelityJSON: point.FidelityJSON, EncryptedCommitEvidence: encodedCommit, IsActive: true, CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.Create(&manifest).Error; err != nil {
			return fmt.Errorf("create managed Rclone manifest: %w", err)
		}
		capturedAt := fact.Commit.ProviderCommittedAt.UTC()
		point.CapturedAt = &capturedAt
		point.CommittedAt = &now
		point.State = string(backupasset.RecoveryPointCommitted)
		point.UpdatedAt = now
		if err := tx.Save(&point).Error; err != nil {
			return fmt.Errorf("save committed managed Rclone point: %w", err)
		}
		if err := service.lease.ReleaseTx(ctx, tx, claim.lease.Fence); err != nil {
			return err
		}
		outcome = publication.Outcome{
			RepositoryID: point.RepositoryID, RecoveryPointID: point.ID, TaskID: claim.attempt.TaskID,
			TaskRunID: claim.attempt.TaskRunID, State: backupasset.RecoveryPointCommitted,
			CapturedAt: capturedAt, ProviderCommitRecorded: true,
		}
		return nil
	})
	return outcome, err
}

func (service *PublicationService) failRcloneReconciliation(
	ctx context.Context,
	claimPoint model.RecoveryPoint,
	claimConsistency backupasset.PublicationConsistencyV1,
	fence backupasset.LeaseFence,
	attempt provider.RcloneAttemptV1,
	expectedState backupasset.RecoveryPointState,
	code backupasset.PublicationFailureCode,
) (publication.Outcome, error) {
	var outcome publication.Outcome
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var point model.RecoveryPoint
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&point, "id = ?", claimPoint.ID).Error; err != nil {
			return err
		}
		if point.State != string(expectedState) || point.ConsistencyJSON != claimPoint.ConsistencyJSON {
			return fmt.Errorf("%w: managed Rclone reconciliation point changed", backupasset.ErrLeaseFenceLost)
		}
		if err := service.lease.ValidateFenceTx(ctx, tx, fence); err != nil {
			return err
		}
		consistency := claimConsistency
		consistency.Code = code
		encoded, err := backupasset.EncodePublicationConsistency(consistency)
		if err != nil {
			return err
		}
		point.ConsistencyJSON = encoded
		point.State = string(backupasset.RecoveryPointFailed)
		point.UpdatedAt = service.now().UTC()
		if err := tx.Save(&point).Error; err != nil {
			return err
		}
		if err := service.lease.ReleaseTx(ctx, tx, fence); err != nil {
			return err
		}
		outcome = publication.Outcome{
			RepositoryID: point.RepositoryID, RecoveryPointID: point.ID, TaskID: attempt.TaskID, TaskRunID: attempt.TaskRunID,
			State: backupasset.RecoveryPointFailed, ProviderCommitRecorded: expectedState == backupasset.RecoveryPointVerifying, Code: code,
		}
		return nil
	})
	return outcome, err
}

func (service *PublicationService) releaseRcloneReconcileLease(
	ctx context.Context,
	pointID string,
	fence backupasset.LeaseFence,
	expectedState backupasset.RecoveryPointState,
) error {
	return service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var point model.RecoveryPoint
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&point, "id = ?", pointID).Error; err != nil {
			return err
		}
		if point.State != string(expectedState) {
			return fmt.Errorf("%w: managed Rclone reconciliation point changed", backupasset.ErrLeaseFenceLost)
		}
		return service.lease.ReleaseTx(ctx, tx, fence)
	})
}

func rcloneManifestID(pointID, attemptID string) (string, error) {
	if backupasset.ValidateOpaqueID(pointID) != nil || backupasset.ValidateOpaqueID(attemptID) != nil {
		return "", fmt.Errorf("%w: invalid managed Rclone manifest identity", backupasset.ErrInvalidState)
	}
	sum := sha256.Sum256([]byte("xirang.rclone.manifest.v1\x00" + pointID + "\x00" + attemptID))
	return hex.EncodeToString(sum[:16]), nil
}

func encodeManagedRcloneFidelity(digest string) (string, error) {
	if !isLowerHex64(digest) {
		return "", fmt.Errorf("%w: invalid managed Rclone fidelity digest", backupasset.ErrInvalidState)
	}
	return fmt.Sprintf(`{"version":1,"digest":%q}`, digest), nil
}
