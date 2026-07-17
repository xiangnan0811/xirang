package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type managedRcloneNativeHealthRuntime struct {
	managedRclonePublicationRuntime
	points []model.RecoveryPoint
}

func (service *PublicationService) ListRcloneNativeHealthCandidates(ctx context.Context, limit int) ([]string, error) {
	if service == nil || service.db == nil || limit <= 0 {
		return nil, fmt.Errorf("%w: invalid native Rclone health scan", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var candidates []string
	err := service.db.WithContext(ctx).
		Table("backup_repositories AS repositories").
		Distinct("repositories.id").
		Joins("JOIN task_repository_links AS links ON links.repository_id = repositories.id AND links.unlinked_at IS NULL").
		Joins("JOIN repository_access_bindings AS bindings ON bindings.repository_id = repositories.id AND bindings.status = ?", bindingStatusActive).
		Joins("JOIN recovery_points AS points ON points.repository_id = repositories.id").
		Where("repositories.provider_kind = ? AND repositories.version_mode = ? AND repositories.immutability_level = ?",
			backupasset.ProviderRclone, backupasset.VersionNativeObjectVersions, backupasset.ImmutabilityBackendVersioned).
		Where("links.publication_mode = ?", backupasset.PublicationNativeObjectVersions).
		Where("points.semantics IN ? AND points.state IN ?", []string{
			string(backupasset.PointXirangManifest), string(backupasset.PointImportedBaseline),
		}, []string{string(backupasset.RecoveryPointCommitted), string(backupasset.RecoveryPointDegraded)}).
		Order("repositories.id ASC").Limit(limit).Pluck("repositories.id", &candidates).Error
	if err != nil {
		return nil, fmt.Errorf("list native Rclone health candidates: %w", err)
	}
	return candidates, nil
}

func (service *PublicationService) CheckRcloneNativeHealth(ctx context.Context, repositoryID string) (provider.RcloneNativeHealthResult, error) {
	if service == nil || service.db == nil || service.rcloneNativeHealthCheck == nil || backupasset.ValidateOpaqueID(repositoryID) != nil {
		return provider.RcloneNativeHealthResult{}, fmt.Errorf("%w: invalid native Rclone health target", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	result, healthErr := service.rcloneNativeHealthCheck(ctx, repositoryID)
	if healthErr != nil && result.Reason == "" {
		result.Reason = provider.RcloneNativeFailureReason(healthErr)
	}
	if persistErr := service.persistRcloneNativeHealth(context.WithoutCancel(ctx), repositoryID, result, healthErr); persistErr != nil {
		return provider.RcloneNativeHealthResult{}, persistErr
	}
	return result, healthErr
}

func (service *PublicationService) checkRcloneNativeProviderHealth(ctx context.Context, repositoryID string) (provider.RcloneNativeHealthResult, error) {
	runtime, err := service.loadManagedRcloneNativeHealthRuntime(ctx, repositoryID)
	if err != nil {
		return provider.RcloneNativeHealthResult{}, err
	}
	leaseConfig, err := service.foundation.LeaseConfig()
	if err != nil {
		return provider.RcloneNativeHealthResult{}, err
	}
	publicationConfig, err := service.foundation.PublicationConfig()
	if err != nil {
		return provider.RcloneNativeHealthResult{}, err
	}
	markerKey, err := service.rcloneMarkerKey(ctx, runtime.repository.ID)
	if err != nil {
		return provider.RcloneNativeHealthResult{}, err
	}
	checkedAt := service.now().UTC()
	healthDeadline := checkedAt.Add(publicationConfig.Rclone.NativeDeadline)
	nativeInput, err := service.prepareRcloneNativeProcessInput(
		ctx, runtime.binding, markerKey, leaseConfig, publicationConfig, checkedAt, healthDeadline, false, nil,
	)
	if err != nil {
		return provider.RcloneNativeHealthResult{Reason: provider.RcloneNativeFailureReason(err)}, err
	}
	s3, err := nativeInput.factory.S3(nativeInput.session, nativeInput.profile, nativeInput.keyBindings)
	if err != nil || s3 == nil {
		if err == nil {
			err = backupasset.ErrCapabilityUnavailable
		}
		return provider.RcloneNativeHealthResult{Reason: provider.RcloneNativeFailureReason(err)}, err
	}

	maxReferences := publicationConfig.Rclone.KMSReadKeyMaxCount + 1
	selected := make(map[string]provider.RcloneNativeHealthReference, maxReferences)
	var selectedSSES3 *provider.RcloneNativeHealthReference
	for _, point := range runtime.points {
		locator, decodeErr := decodeManagedRclonePointLocator(point.EncryptedProviderLocator)
		if decodeErr != nil || locator.PublicationMode != backupasset.PublicationNativeObjectVersions {
			return provider.RcloneNativeHealthResult{Reason: backupasset.RcloneReasonManifestMismatch}, fmt.Errorf("%w: invalid native Rclone health locator", backupasset.ErrConflict)
		}
		attempt, decodeErr := provider.DecodeRcloneAttemptV1(locator.TaggedAttempt)
		if decodeErr != nil || attempt.RepositoryID != runtime.repository.ID || attempt.RecoveryPointID != point.ID || attempt.Native == nil {
			return provider.RcloneNativeHealthResult{Reason: backupasset.RcloneReasonManifestMismatch}, fmt.Errorf("%w: invalid native Rclone health attempt", backupasset.ErrConflict)
		}
		commit, decodeErr := provider.DecodeRcloneCommitV1(locator.TaggedCommit)
		if decodeErr != nil || commit.Native == nil {
			return provider.RcloneNativeHealthResult{Reason: backupasset.RcloneReasonManifestMismatch}, fmt.Errorf("%w: invalid native Rclone health commit", backupasset.ErrConflict)
		}
		commit.Native.CommitKey = locator.NativeCommitKey
		commit.Native.CommitVersionID = locator.NativeCommitVersionID
		request := provider.RcloneNativePublicationRequest{
			Attempt: attempt, Profile: nativeInput.profile, Session: nativeInput.session, ClientFactory: nativeInput.factory,
			ObservationLimits: nativeInput.observationLimits, Encryption: nativeInput.encryption,
			EncryptionEvidence: nativeInput.encryptionEvidence,
			KMSKeyBindings:     append([]provider.RcloneNativeKMSKeyDigestBinding(nil), nativeInput.keyBindings...),
			MarkerKey:          append([]byte(nil), markerKey...), CapabilityEvidenceDigest: commit.CapabilityEvidenceDigest,
			CostEvidenceDigest: commit.CostEvidenceDigest, ControlPayloadMaxBytes: uint64(publicationConfig.Rclone.ControlPayloadMaxBytes),
		}
		references, loadErr := provider.LoadRcloneNativeHealthReferences(ctx, s3, request, commit, maxReferences)
		if loadErr != nil {
			return provider.RcloneNativeHealthResult{Reason: provider.RcloneNativeFailureReason(loadErr)}, loadErr
		}
		for _, reference := range references {
			if nativeInput.encryption.Profile == provider.RcloneNativeSSES3V1 {
				if selectedSSES3 == nil {
					copy := reference
					selectedSSES3 = &copy
				}
				continue
			}
			if _, exists := selected[reference.KMSKeyDigest]; !exists {
				selected[reference.KMSKeyDigest] = reference
			}
		}
	}
	references := make([]provider.RcloneNativeHealthReference, 0, maxReferences)
	if selectedSSES3 != nil {
		references = append(references, *selectedSSES3)
	}
	keys := make([]string, 0, len(selected))
	for digest := range selected {
		keys = append(keys, digest)
	}
	sort.Strings(keys)
	for _, digest := range keys {
		references = append(references, selected[digest])
	}
	if len(references) == 0 || len(references) > maxReferences {
		return provider.RcloneNativeHealthResult{Reason: backupasset.RcloneReasonKMSKeyUnavailable}, fmt.Errorf("%w: native Rclone health references unavailable", backupasset.ErrConflict)
	}
	accountID, ok := managedRcloneAWSRoleAccount(runtime.binding.Native.RoleARN)
	if !ok {
		return provider.RcloneNativeHealthResult{Reason: backupasset.RcloneReasonIdentityMismatch}, fmt.Errorf("%w: native Rclone role identity", backupasset.ErrConflict)
	}
	var kms provider.KMSKeyInspector
	if nativeInput.encryption.Profile == provider.RcloneNativeSSEKMSV1 {
		kms, err = nativeInput.factory.KMS(nativeInput.session, nativeInput.profile.Region)
		if err != nil || kms == nil {
			if err == nil {
				err = backupasset.ErrCapabilityUnavailable
			}
			return provider.RcloneNativeHealthResult{Reason: provider.RcloneNativeFailureReason(err)}, err
		}
	}
	result, err := provider.CheckRcloneNativeHealth(ctx, provider.RcloneNativeHealthDependencies{S3: s3, KMS: kms}, provider.RcloneNativeHealthRequest{
		Profile: nativeInput.profile, ExpectedAccountID: accountID,
		StableObservedAt: runtime.binding.Native.CapabilityStableObservedAt, CheckedAt: checkedAt,
		VersioningDigest: runtime.binding.Native.VersioningDigest, LifecycleDigest: runtime.binding.Native.LifecycleDigest,
		BucketEncryptionDigest: runtime.binding.Native.BucketEncryptionDigest,
		Encryption:             nativeInput.encryption, ExpectedEncryption: nativeInput.encryptionEvidence,
		KMSLimits: provider.RcloneNativeKMSLimits{
			MaxReadKeys: publicationConfig.Rclone.KMSReadKeyMaxCount, MaxSerializedBytes: managedRcloneRetainedReadKeyBytesMaximum,
		},
		References: references, MaxVerifyBytes: uint64(publicationConfig.Rclone.FullVerifyMaxBytes),
	})
	if err != nil && result.Reason == "" {
		result.Reason = provider.RcloneNativeFailureReason(err)
	}
	return result, err
}

func (service *PublicationService) loadManagedRcloneNativeHealthRuntime(ctx context.Context, repositoryID string) (managedRcloneNativeHealthRuntime, error) {
	config, err := service.foundation.PublicationConfig()
	if err != nil {
		return managedRcloneNativeHealthRuntime{}, err
	}
	var repository model.BackupRepository
	if err := service.db.WithContext(ctx).First(&repository, "id = ?", repositoryID).Error; err != nil {
		return managedRcloneNativeHealthRuntime{}, fmt.Errorf("load native Rclone health repository: %w", err)
	}
	if repository.ProviderKind != string(backupasset.ProviderRclone) || repository.VersionMode != string(backupasset.VersionNativeObjectVersions) ||
		repository.ImmutabilityLevel != string(backupasset.ImmutabilityXirangManaged) || repository.RepositoryIdentity == nil {
		return managedRcloneNativeHealthRuntime{}, fmt.Errorf("%w: native Rclone health repository contract", backupasset.ErrConflict)
	}
	var link model.TaskRepositoryLink
	if err := service.db.WithContext(ctx).Where("repository_id = ? AND publication_mode = ? AND unlinked_at IS NULL", repositoryID, backupasset.PublicationNativeObjectVersions).First(&link).Error; err != nil {
		return managedRcloneNativeHealthRuntime{}, fmt.Errorf("load native Rclone health link: %w", err)
	}
	if link.TaskID == nil {
		return managedRcloneNativeHealthRuntime{}, fmt.Errorf("%w: native Rclone health link has no Task", backupasset.ErrConflict)
	}
	var taskEntity model.Task
	if err := service.db.WithContext(ctx).Preload("Node.SSHKey").Where("archived_at IS NULL").First(&taskEntity, *link.TaskID).Error; err != nil {
		return managedRcloneNativeHealthRuntime{}, fmt.Errorf("load native Rclone health Task: %w", err)
	}
	var access model.RepositoryAccessBinding
	if err := service.db.WithContext(ctx).Where("repository_id = ? AND status = ?", repositoryID, bindingStatusActive).First(&access).Error; err != nil {
		return managedRcloneNativeHealthRuntime{}, fmt.Errorf("load native Rclone health binding: %w", err)
	}
	stored, err := decodeStoredBindingDocument(access.EncryptedConfig)
	if err != nil || stored.ManagedRcloneV3 == nil || stored.ManagedRcloneV3.Native == nil {
		return managedRcloneNativeHealthRuntime{}, fmt.Errorf("%w: native Rclone V3 health binding required", backupasset.ErrConflict)
	}
	binding := *stored.ManagedRcloneV3
	if err := validateManagedRcloneBindingAssociation(binding, managedRcloneBindingAssociation{Task: taskEntity, Link: link, Repository: repository}); err != nil {
		return managedRcloneNativeHealthRuntime{}, err
	}
	var points []model.RecoveryPoint
	if err := service.db.WithContext(ctx).
		Where("repository_id = ? AND semantics IN ? AND state IN ?", repositoryID, []string{
			string(backupasset.PointXirangManifest), string(backupasset.PointImportedBaseline),
		}, []string{string(backupasset.RecoveryPointCommitted), string(backupasset.RecoveryPointDegraded)}).
		Order("committed_at DESC, id ASC").Limit(config.Rclone.HealthBatchSize).Find(&points).Error; err != nil {
		return managedRcloneNativeHealthRuntime{}, fmt.Errorf("load native Rclone health points: %w", err)
	}
	if len(points) == 0 {
		return managedRcloneNativeHealthRuntime{}, fmt.Errorf("%w: native Rclone health points unavailable", backupasset.ErrNotFound)
	}
	return managedRcloneNativeHealthRuntime{
		managedRclonePublicationRuntime: managedRclonePublicationRuntime{repository: repository, task: taskEntity, link: link, binding: binding},
		points:                          points,
	}, nil
}

func (service *PublicationService) persistRcloneNativeHealth(
	ctx context.Context,
	repositoryID string,
	result provider.RcloneNativeHealthResult,
	healthErr error,
) error {
	reason := result.Reason
	if healthErr != nil && reason == "" {
		reason = provider.RcloneNativeFailureReason(healthErr)
	}
	targetStatus := backupasset.RepositoryOnline
	targetAvailability := backupasset.PhysicalOnline
	degradePoints := false
	if healthErr != nil {
		switch reason {
		case backupasset.RcloneReasonProviderUnavailable, backupasset.RcloneReasonProviderTimeout, backupasset.RcloneReasonRepositoryOffline:
			targetStatus = backupasset.RepositoryOffline
			targetAvailability = backupasset.PhysicalOffline
		default:
			targetStatus = backupasset.RepositoryDegraded
			targetAvailability = backupasset.PhysicalUnknown
			degradePoints = true
		}
	} else if reason != backupasset.RcloneReasonReady || result.EvidenceDigest == "" {
		return fmt.Errorf("%w: invalid successful native Rclone health evidence", backupasset.ErrInvalidState)
	}
	return service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var repository model.BackupRepository
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&repository, "id = ?", repositoryID).Error; err != nil {
			return fmt.Errorf("lock native Rclone health repository: %w", err)
		}
		if repository.ProviderKind != string(backupasset.ProviderRclone) || repository.VersionMode != string(backupasset.VersionNativeObjectVersions) {
			return fmt.Errorf("%w: native Rclone health repository changed", backupasset.ErrConflict)
		}
		var activeLinks int64
		if err := tx.Model(&model.TaskRepositoryLink{}).Where("repository_id = ? AND publication_mode = ? AND unlinked_at IS NULL", repositoryID, backupasset.PublicationNativeObjectVersions).Count(&activeLinks).Error; err != nil {
			return fmt.Errorf("verify native Rclone health link: %w", err)
		}
		if activeLinks != 1 {
			return fmt.Errorf("%w: native Rclone health link changed", backupasset.ErrConflict)
		}
		if err := backupasset.ValidateRepositoryTransition(backupasset.RepositoryStatus(repository.Status), targetStatus); err != nil {
			return err
		}
		capabilities, err := rcloneHealthCapabilities(repository.CapabilitiesJSON, reason, healthErr)
		if err != nil {
			return err
		}
		now := service.now().UTC()
		repository.Status = string(targetStatus)
		repository.CapabilitiesJSON = capabilities
		repository.LastReconciledAt = &now
		if healthErr == nil {
			repository.LastSeenAt = &now
		}
		repository.UpdatedAt = now
		if err := tx.Save(&repository).Error; err != nil {
			return fmt.Errorf("persist native Rclone repository health: %w", err)
		}
		query := tx.Model(&model.RecoveryPoint{}).Where("repository_id = ? AND semantics IN ? AND state IN ?", repositoryID, []string{
			string(backupasset.PointXirangManifest), string(backupasset.PointImportedBaseline),
		}, []string{string(backupasset.RecoveryPointCommitted), string(backupasset.RecoveryPointDegraded)})
		updates := map[string]any{"physical_availability": string(targetAvailability), "capabilities_json": capabilities, "updated_at": now}
		if degradePoints {
			updates["state"] = string(backupasset.RecoveryPointDegraded)
		}
		if err := query.Updates(updates).Error; err != nil {
			return fmt.Errorf("persist native Rclone point health: %w", err)
		}
		if healthErr == nil {
			if err := tx.Model(&model.RecoveryPoint{}).
				Where("repository_id = ? AND semantics IN ? AND state = ?", repositoryID, []string{
					string(backupasset.PointXirangManifest), string(backupasset.PointImportedBaseline),
				}, backupasset.RecoveryPointDegraded).
				Updates(map[string]any{"state": string(backupasset.RecoveryPointCommitted), "updated_at": now}).Error; err != nil {
				return fmt.Errorf("restore native Rclone point health: %w", err)
			}
		}
		return nil
	})
}

func rcloneHealthCapabilities(raw string, reason backupasset.RcloneVersioningReasonCode, healthErr error) (string, error) {
	if healthErr != nil {
		code := backupasset.CapabilityProviderProtocolIncompatible
		switch reason {
		case backupasset.RcloneReasonProviderTimeout:
			code = backupasset.CapabilityProviderOperationTimeout
		case backupasset.RcloneReasonProviderUnavailable, backupasset.RcloneReasonRepositoryOffline:
			code = backupasset.CapabilityProviderUnavailable
		}
		return capabilitiesWithReason(raw, code)
	}
	capabilities, err := decodeRepositoryCapabilities(raw)
	if err != nil {
		return "", err
	}
	capabilities.Reason = nil
	payload, err := json.Marshal(capabilities)
	if err != nil {
		return "", fmt.Errorf("marshal native Rclone health capabilities: %w", err)
	}
	return string(payload), nil
}
