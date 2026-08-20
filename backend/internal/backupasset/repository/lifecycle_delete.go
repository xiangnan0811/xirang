package repository

import (
	"context"
	"path/filepath"
	"strings"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/model"
)

func lifecycleDeletionUnavailable() error {
	return &provider.CapabilityError{
		Reason: backupasset.CapabilityReason{Code: backupasset.CapabilityDeletionUnavailable},
	}
}

func lifecycleDeleteLocator(point model.RecoveryPoint) string {
	if strings.TrimSpace(point.EncryptedProviderLocator) != "" {
		return point.EncryptedProviderLocator
	}
	return strings.TrimSpace(point.EncryptedRollbackLocator)
}

func (service *Service) lifecycleDeleteAccess(
	ctx context.Context,
	repository model.BackupRepository,
	point model.RecoveryPoint,
	native string,
) (provider.AccessBinding, error) {
	switch backupasset.ProviderKind(repository.ProviderKind) {
	case backupasset.ProviderRestic:
		runtime, err := service.loadRepositoryRuntime(ctx, repository.ID)
		if err != nil {
			return provider.AccessBinding{}, lifecycleDeletionUnavailable()
		}
		return runtime.access, nil
	case backupasset.ProviderRsync:
		return service.rsyncLifecycleDeleteAccess(ctx, repository, point, native)
	case backupasset.ProviderRclone:
		return service.rcloneLifecycleDeleteAccess(ctx, repository, point, native)
	default:
		return provider.AccessBinding{}, lifecycleDeletionUnavailable()
	}
}

func (service *Service) rsyncLifecycleDeleteAccess(
	ctx context.Context,
	repository model.BackupRepository,
	point model.RecoveryPoint,
	native string,
) (provider.AccessBinding, error) {
	if service.publication == nil {
		return provider.AccessBinding{}, lifecycleDeletionUnavailable()
	}
	stored, err := service.loadActiveStoredBinding(ctx, repository.ID)
	if err != nil || stored.ManagedRsyncV2 == nil {
		return provider.AccessBinding{}, lifecycleDeletionUnavailable()
	}
	decoded, err := decodeManagedRsyncPointLocator(lifecycleDeleteLocator(point))
	if err != nil {
		return provider.AccessBinding{}, lifecycleDeletionUnavailable()
	}
	attempt, err := provider.DecodeRsyncTreeAttemptV1(decoded.TaggedAttempt)
	if err != nil || native != attempt.FinalComponent {
		return provider.AccessBinding{}, lifecycleDeletionUnavailable()
	}
	markerKey, err := service.publication.rsyncMarkerKey(ctx, repository.ID)
	if err != nil {
		return provider.AccessBinding{}, lifecycleDeletionUnavailable()
	}
	return provider.AccessBinding{
		Provider:     backupasset.ProviderRsync,
		RepositoryID: repository.ID,
		AdapterData: provider.RsyncPointDeletionAccess{
			ManagedRoot:        filepath.Clean(stored.ManagedRsyncV2.ManagedRootLocator),
			MarkerKey:          markerKey,
			Attempt:            attempt,
			CommitMarkerDigest: decoded.CommitMarkerDigest,
			SourceFingerprint:  point.SourceFingerprint,
		},
	}, nil
}

func (service *Service) rcloneLifecycleDeleteAccess(
	ctx context.Context,
	repository model.BackupRepository,
	point model.RecoveryPoint,
	native string,
) (provider.AccessBinding, error) {
	if service.publication == nil || point.ProducingTaskID == nil || *point.ProducingTaskID == 0 {
		return provider.AccessBinding{}, lifecycleDeletionUnavailable()
	}
	decoded, err := decodeManagedRclonePointLocator(lifecycleDeleteLocator(point))
	if err != nil {
		return provider.AccessBinding{}, lifecycleDeletionUnavailable()
	}
	switch decoded.PublicationMode {
	case backupasset.PublicationVersionedPrefix:
		return service.rclonePrefixLifecycleDeleteAccess(ctx, repository, point, native, decoded)
	case backupasset.PublicationNativeObjectVersions:
		return service.rcloneNativeLifecycleDeleteAccess(ctx, repository, point, native, decoded)
	default:
		return provider.AccessBinding{}, lifecycleDeletionUnavailable()
	}
}

func (service *Service) rclonePrefixLifecycleDeleteAccess(
	ctx context.Context,
	repository model.BackupRepository,
	point model.RecoveryPoint,
	native string,
	locator managedRclonePointLocatorV1,
) (provider.AccessBinding, error) {
	if native != locator.PortableAttemptRoot {
		return provider.AccessBinding{}, lifecycleDeletionUnavailable()
	}
	stored, err := service.loadActiveStoredBinding(ctx, repository.ID)
	if err != nil || stored.ManagedRcloneV3 == nil || stored.ManagedRcloneV3.Portable == nil {
		return provider.AccessBinding{}, lifecycleDeletionUnavailable()
	}
	prefix, err := provider.NewRclonePrivateLocator(locator.PortableAttemptRoot)
	if err != nil {
		return provider.AccessBinding{}, lifecycleDeletionUnavailable()
	}
	attempt, err := provider.DecodeRcloneAttemptV1(locator.TaggedAttempt)
	if err != nil || attempt.ManagedRootIdentityDigest == "" || stored.ManagedRcloneV3.Portable.ConfigDigest == "" {
		return provider.AccessBinding{}, lifecycleDeletionUnavailable()
	}
	runtime, err := service.publication.loadExactManagedRclonePublicationRuntime(ctx, *point.ProducingTaskID)
	if err != nil || runtime.task.Node.ID == 0 {
		return provider.AccessBinding{}, lifecycleDeletionUnavailable()
	}
	return provider.AccessBinding{
		Provider:     backupasset.ProviderRclone,
		RepositoryID: repository.ID,
		TaskID:       runtime.task.ID,
		NodeID:       runtime.task.NodeID,
		Secret:       []byte(stored.ManagedRcloneV3.Portable.BoundConfig),
		AdapterData: provider.RclonePrefixDeletionAccess{
			Prefix:               prefix,
			MarkerDigest:         point.SourceFingerprint,
			ExpectedBackend:      stored.ManagedRcloneV3.Portable.Backend,
			ExpectedRootIdentity: attempt.ManagedRootIdentityDigest,
			ConfigDigest:         stored.ManagedRcloneV3.Portable.ConfigDigest,
			Command:              &provider.RemoteCommandAccess{Node: runtime.task.Node},
		},
	}, nil
}

func (service *Service) rcloneNativeLifecycleDeleteAccess(
	ctx context.Context,
	repository model.BackupRepository,
	point model.RecoveryPoint,
	native string,
	locator managedRclonePointLocatorV1,
) (provider.AccessBinding, error) {
	if native != locator.NativeCommitKey || locator.NativeCommitKey == "" || locator.NativeCommitVersionID == "" {
		return provider.AccessBinding{}, lifecycleDeletionUnavailable()
	}
	versions, err := rcloneNativeFrozenDeletionVersions(locator)
	if err != nil {
		return provider.AccessBinding{}, lifecycleDeletionUnavailable()
	}
	input, err := service.rcloneNativeReconcileInput(ctx, *point.ProducingTaskID, repository, point, locator)
	if err != nil || input.NativeRequest == nil || input.NativeRequest.ClientFactory == nil {
		return provider.AccessBinding{}, lifecycleDeletionUnavailable()
	}
	s3, err := input.NativeRequest.ClientFactory.S3(
		input.NativeRequest.Session, input.NativeRequest.Profile, input.NativeRequest.KMSKeyBindings,
	)
	if err != nil || s3 == nil {
		return provider.AccessBinding{}, lifecycleDeletionUnavailable()
	}
	client, ok := provider.RcloneNativeExactVersionDeleterFromS3(s3)
	if !ok {
		return provider.AccessBinding{}, lifecycleDeletionUnavailable()
	}
	return provider.AccessBinding{
		Provider:     backupasset.ProviderRclone,
		RepositoryID: repository.ID,
		AdapterData: provider.RcloneNativeDeletionAccess{
			Versions: versions,
			Client:   client,
		},
	}, nil
}

func rcloneNativeFrozenDeletionVersions(locator managedRclonePointLocatorV1) ([]provider.RcloneNativeExactVersion, error) {
	if len(locator.FrozenNativeVersions) < 2 {
		return nil, lifecycleDeletionUnavailable()
	}
	versions := make([]provider.RcloneNativeExactVersion, 0, len(locator.FrozenNativeVersions))
	for _, version := range locator.FrozenNativeVersions {
		versions = append(versions, provider.RcloneNativeExactVersion{
			PhysicalKey: version.PhysicalKey,
			VersionID:   version.VersionID,
		})
	}
	return validateRcloneNativeFrozenDeletionVersions(locator, versions)
}

func validateRcloneNativeFrozenDeletionVersions(
	locator managedRclonePointLocatorV1,
	versions []provider.RcloneNativeExactVersion,
) ([]provider.RcloneNativeExactVersion, error) {
	if len(versions) < 2 {
		return nil, lifecycleDeletionUnavailable()
	}
	seen := make(map[string]bool, len(versions))
	foundCommit := false
	for _, version := range versions {
		if strings.TrimSpace(version.PhysicalKey) == "" || strings.TrimSpace(version.VersionID) == "" {
			return nil, lifecycleDeletionUnavailable()
		}
		identity := version.PhysicalKey + "\x00" + version.VersionID
		if seen[identity] {
			return nil, lifecycleDeletionUnavailable()
		}
		seen[identity] = true
		if version.PhysicalKey == locator.NativeCommitKey && version.VersionID == locator.NativeCommitVersionID {
			foundCommit = true
		}
	}
	if !foundCommit {
		return nil, lifecycleDeletionUnavailable()
	}
	return versions, nil
}

func (service *Service) rcloneNativeReconcileInput(
	ctx context.Context,
	taskID uint,
	repository model.BackupRepository,
	point model.RecoveryPoint,
	locator managedRclonePointLocatorV1,
) (provider.RcloneReconcileInput, error) {
	if service.publication == nil || service.foundation == nil {
		return provider.RcloneReconcileInput{}, lifecycleDeletionUnavailable()
	}
	runtime, err := service.publication.loadExactManagedRclonePublicationRuntime(ctx, taskID)
	if err != nil {
		return provider.RcloneReconcileInput{}, lifecycleDeletionUnavailable()
	}
	attempt, err := provider.DecodeRcloneAttemptV1(locator.TaggedAttempt)
	if err != nil {
		return provider.RcloneReconcileInput{}, lifecycleDeletionUnavailable()
	}
	if err := validateRcloneReconcileRuntime(runtime, point, attempt); err != nil {
		return provider.RcloneReconcileInput{}, lifecycleDeletionUnavailable()
	}
	markerKey, err := service.publication.rcloneMarkerKey(ctx, repository.ID)
	if err != nil {
		return provider.RcloneReconcileInput{}, lifecycleDeletionUnavailable()
	}
	leaseConfig, err := service.foundation.LeaseConfig()
	if err != nil {
		return provider.RcloneReconcileInput{}, lifecycleDeletionUnavailable()
	}
	input, err := service.publication.rcloneReconcileInput(
		ctx, runtime, attempt, markerKey, leaseConfig, locator.NativeCommitKey, locator.NativeCommitVersionID,
	)
	if err != nil || input.NativeRequest == nil {
		return provider.RcloneReconcileInput{}, lifecycleDeletionUnavailable()
	}
	return input, nil
}

func (service *Service) loadActiveStoredBinding(ctx context.Context, repositoryID string) (storedBindingDocument, error) {
	var binding model.RepositoryAccessBinding
	if err := service.db.WithContext(ctx).
		Where("repository_id = ? AND status = ?", repositoryID, bindingStatusActive).
		First(&binding).Error; err != nil {
		return storedBindingDocument{}, err
	}
	return decodeStoredBindingDocument(binding.EncryptedConfig)
}
