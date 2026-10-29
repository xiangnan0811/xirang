package repository

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"

	"gorm.io/gorm"
)

func validRcloneRenewalAssociation(plan rcloneBindingWorkflowPlan) bool {
	document := plan.managedBinding
	if document == nil || plan.repository.CapabilityRevision <= 0 ||
		uint64(plan.repository.CapabilityRevision) != document.CapabilityRevision || plan.repository.RepositoryIdentity == nil ||
		plan.binding.BindingKind != "managed_rclone_v3" {
		return false
	}
	identity, err := managedRcloneRepositoryIdentity(*document)
	if err != nil || identity != *plan.repository.RepositoryIdentity {
		return false
	}
	salt, err := hexDecodeSalt(document.IdentitySalt)
	return err == nil && plan.binding.ConfigFingerprint == managedRcloneBindingDigest(salt, "active-binding", plan.binding.EncryptedConfig)
}

// Renewal candidates are reconstructed only from the admitted persisted binding.
// The final installation is guarded by the workflow lock; no database work is
// performed under that lock.
func (service *Service) prepareRcloneRenewalCandidate(ctx context.Context, request backupasset.RcloneVersioningPreflightRequest) error {
	kind := rcloneBindingSetupPortable
	if request.RequestedMode == backupasset.PublicationNativeObjectVersions {
		kind = rcloneBindingSetupNative
	}
	plan, err := service.loadRcloneBindingWorkflowPlan(ctx, request.TaskID, request.ExpectedTaskRevision, kind)
	if err != nil {
		return err
	}
	if plan.managedBinding == nil {
		return nil
	}
	if !validRcloneRenewalAssociation(plan) {
		return fmt.Errorf("%w: Rclone renewal association changed", backupasset.ErrConflict)
	}
	document := plan.managedBinding
	if document.RollbackPrepared || plan.task.Enabled {
		return fmt.Errorf("%w: Rclone renewal requires a paused Task without prepared rollback", backupasset.ErrConflict)
	}
	if err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		locked, err := lockRcloneWorkflowPlanTx(ctx, tx, plan, true)
		if err == nil && locked.task.Enabled {
			return fmt.Errorf("%w: Rclone renewal requires a paused Task", backupasset.ErrConflict)
		}
		return err
	}); err != nil {
		return err
	}
	config, err := service.foundation.PublicationConfig()
	if err != nil {
		return err
	}
	id, err := backupasset.NewOpaqueID()
	if err != nil {
		return err
	}
	candidate := rcloneBindingCandidate{
		purpose: backupasset.RclonePreflightPurposeRenewal, kind: kind, taskID: request.TaskID,
		expectedTaskRevision: request.ExpectedTaskRevision, bindingRevision: document.BindingRevision,
		configRevision: document.ConfigRevision, credentialRevision: document.CredentialRevision,
		setupID: id, expiresAt: service.utcNow().Add(config.Rclone.PreflightTTL), plan: plan,
	}
	if document.Portable != nil {
		salt, err := hexDecodeSalt(document.IdentitySalt)
		if err != nil {
			return err
		}
		bound, err := provider.ValidateRcloneBoundConfigV1744([]byte(document.Portable.BoundConfig), document.Portable.TargetRemote, salt, config.Rclone.BoundConfigMaxBytes)
		if err != nil || bound.KeyedDigest() != document.Portable.ConfigDigest {
			return fmt.Errorf("%w: Rclone renewal configuration changed", backupasset.ErrConflict)
		}
		candidate.portable = &rclonePortableBindingCandidate{targetRemote: document.Portable.TargetRemote, managedRootLocator: document.Portable.ManagedRootLocator, boundConfig: bound}
	} else if document.Native != nil {
		native := document.Native
		input := backupasset.RcloneNativeBindingRequest{TaskID: request.TaskID, ExpectedTaskRevision: request.ExpectedTaskRevision,
			ExpectedBindingRevision: document.BindingRevision, SetupID: id, Region: native.Region, Bucket: native.Bucket,
			ManagedPrefix: native.ManagedPrefix, RoleARN: native.RoleARN, KMSKeyARN: native.ActiveKMSKeyARN}
		switch native.Bootstrap.Mode {
		case managedRcloneBootstrapWorkloadChain:
			input.Bootstrap.Mode = backupasset.RcloneBootstrapWorkloadChain
		case managedRcloneBootstrapStaticSTS:
			input.Bootstrap = backupasset.RcloneNativeBootstrapInput{Mode: backupasset.RcloneBootstrapStaticSTS, AccessKeyID: native.Bootstrap.Static.AccessKeyID, SecretAccessKey: native.Bootstrap.Static.SecretAccessKey}
		default:
			return fmt.Errorf("%w: invalid Rclone renewal bootstrap", backupasset.ErrInvalidState)
		}
		switch native.EncryptionProfile {
		case provider.RcloneNativeSSES3V1:
			input.EncryptionProfile = backupasset.RcloneEncryptionSSES3
		case provider.RcloneNativeSSEKMSV1:
			input.EncryptionProfile = backupasset.RcloneEncryptionSSEKMS
		default:
			return fmt.Errorf("%w: invalid Rclone renewal encryption", backupasset.ErrInvalidState)
		}
		candidate.native = &rcloneNativeBindingCandidate{request: input, externalID: native.ExternalID}
	} else {
		return fmt.Errorf("%w: invalid Rclone renewal binding", backupasset.ErrInvalidState)
	}
	service.rcloneWorkflowMu.Lock()
	defer service.rcloneWorkflowMu.Unlock()
	if service.rcloneActivations[request.TaskID] != "" {
		return fmt.Errorf("%w: Rclone activation is in progress", backupasset.ErrConflict)
	}
	if current, ok := service.rcloneCandidates.get(request.TaskID); ok {
		if current.purpose != backupasset.RclonePreflightPurposeRenewal || current.expectedTaskRevision != request.ExpectedTaskRevision ||
			current.kind != kind || current.plan.bindingCiphertext != plan.bindingCiphertext {
			return fmt.Errorf("%w: another Rclone candidate exists", backupasset.ErrConflict)
		}
		return nil
	}
	service.rclonePreflights.removeForTask(request.TaskID)
	return service.rcloneCandidates.put(candidate)
}

func validateRcloneRenewalEvidence(document *managedRcloneBindingDocumentV3, evidence RcloneVersioningPreflightEvidence) error {
	if document == nil || document.RollbackPrepared || evidence.Mode != document.PublicationMode ||
		evidence.CapabilityRevision != document.CapabilityRevision || evidence.ManagedRootIdentityDigest != document.ManagedRootIdentityDigest {
		return fmt.Errorf("%w: Rclone renewal identity changed", backupasset.ErrConflict)
	}
	if document.Native != nil {
		old, observed := document.Native, evidence.Native
		if observed == nil || observed.VersioningDigest != old.VersioningDigest || observed.LifecycleDigest != old.LifecycleDigest ||
			observed.BucketEncryptionDigest != old.BucketEncryptionDigest || observed.BucketKeyEnabled != old.BucketKeyEnabled ||
			observed.ActiveKMSKeyDigest != old.ActiveKMSKeyDigest || observed.KMSCapabilityRevision != old.KMSCapabilityRevision ||
			!slices.Equal(observed.RetainedReadKeys, old.RetainedReadKeys) {
			return fmt.Errorf("%w: Rclone renewal capability changed", backupasset.ErrConflict)
		}
	}
	return nil
}

func (service *Service) renewRcloneBindingTx(ctx context.Context, tx *gorm.DB, record rcloneVersioningPreflightRecord) error {
	locked, err := lockRcloneWorkflowPlanTx(ctx, tx, record.candidate.plan, true)
	if err != nil {
		return err
	}
	document := locked.managedBinding
	if locked.task.Enabled || document == nil || document.RollbackPrepared ||
		!record.expiresAt.After(document.PreflightExpiresAt) || !record.expiresAt.After(service.utcNow()) ||
		!record.candidate.expiresAt.After(service.utcNow()) {
		return fmt.Errorf("%w: Rclone renewal is no longer eligible", backupasset.ErrConflict)
	}
	if err := validateRcloneRenewalEvidence(document, record.evidence); err != nil {
		return err
	}
	updated := *document
	updated.PreflightExpiresAt = record.expiresAt
	encoded, err := encodeManagedRcloneBindingDocumentV3(updated)
	if err != nil {
		return err
	}
	salt, err := hexDecodeSalt(document.IdentitySalt)
	if err != nil {
		return err
	}
	now := service.utcNow()
	if !now.After(locked.task.UpdatedAt.UTC()) {
		now = locked.task.UpdatedAt.UTC().Add(time.Microsecond)
	}
	binding := locked.binding
	binding.EncryptedConfig = encoded
	binding.ConfigFingerprint = managedRcloneBindingDigest(salt, "active-binding", encoded)
	binding.UpdatedAt = now
	if err := tx.Save(&binding).Error; err != nil {
		return fmt.Errorf("renew Rclone binding: %w", err)
	}
	// Use an explicit update to preserve every Task field except the CAS time.
	if err := tx.Model(&locked.task).Update("updated_at", now).Error; err != nil {
		return fmt.Errorf("advance renewed Rclone Task revision: %w", err)
	}
	current, err := service.foundation.PublicationConfigTx(ctx, tx)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(current, record.publicationConfig) || !record.expiresAt.After(service.utcNow()) || !record.candidate.expiresAt.After(service.utcNow()) {
		return fmt.Errorf("%w: Rclone renewal configuration or ticket expired", backupasset.ErrConflict)
	}
	return nil
}
