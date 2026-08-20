package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

const (
	managedHistoryLatchScopeInstallation = "installation"
	managedHistoryLatchScopeRepository   = "repository"
)

type legacyFallbackBindingState uint8

const (
	legacyFallbackUnlinked legacyFallbackBindingState = iota
	legacyFallbackExactPristine
	legacyFallbackBlocked
)

// ManagedHistoryTombstoneSource is a narrow future port for lifecycle facts
// beyond the durable latch table, such as a later tombstone contract.
type ManagedHistoryTombstoneSource interface {
	HasRepositoryManagedHistory(context.Context, string) (bool, error)
	HasInstallationManagedHistory(context.Context) (bool, error)
}

type ManagedHistoryResolverDependencies struct {
	DB         *gorm.DB
	Tombstones ManagedHistoryTombstoneSource
}

// ManagedHistoryResolver owns only lower-level persisted safety queries. It
// intentionally has no admission, runtime, or publication-service dependency.
type ManagedHistoryResolver struct {
	db         *gorm.DB
	tombstones ManagedHistoryTombstoneSource
}

type lifecycleManagedHistoryTombstones struct {
	db *gorm.DB
}

func NewLifecycleManagedHistoryTombstones(db *gorm.DB) (ManagedHistoryTombstoneSource, error) {
	if db == nil {
		return nil, fmt.Errorf("%w: managed history tombstone database is unavailable", backupasset.ErrInvalidState)
	}
	return &lifecycleManagedHistoryTombstones{db: db}, nil
}

func (source *lifecycleManagedHistoryTombstones) HasRepositoryManagedHistory(ctx context.Context, repositoryID string) (bool, error) {
	if source == nil || source.db == nil || backupasset.ValidateOpaqueID(repositoryID) != nil {
		return false, fmt.Errorf("%w: invalid managed history tombstone query", backupasset.ErrInvalidState)
	}
	if !source.db.Migrator().HasTable(&model.RecoveryPointLifecycleTombstone{}) &&
		!source.db.Migrator().HasTable(&model.BackupAssetManagedHistoryLatch{}) {
		return false, nil
	}
	var count int64
	if source.db.Migrator().HasTable(&model.RecoveryPointLifecycleTombstone{}) {
		if err := source.db.WithContext(ctx).Model(&model.RecoveryPointLifecycleTombstone{}).
			Where("repository_id = ? AND managed_history = ?", repositoryID, true).
			Count(&count).Error; err != nil {
			return false, fmt.Errorf("query repository lifecycle tombstone: %w", err)
		}
		if count > 0 {
			return true, nil
		}
	}
	if source.db.Migrator().HasTable(&model.BackupAssetManagedHistoryLatch{}) {
		if err := source.db.WithContext(ctx).Model(&model.BackupAssetManagedHistoryLatch{}).
			Where("scope = ? AND repository_id = ?", managedHistoryLatchScopeRepository, repositoryID).
			Count(&count).Error; err != nil {
			return false, fmt.Errorf("query repository managed history latch tombstone: %w", err)
		}
		return count > 0, nil
	}
	return false, nil
}

func (source *lifecycleManagedHistoryTombstones) HasInstallationManagedHistory(ctx context.Context) (bool, error) {
	if source == nil || source.db == nil {
		return false, fmt.Errorf("%w: managed history tombstone database is unavailable", backupasset.ErrInvalidState)
	}
	if !source.db.Migrator().HasTable(&model.RecoveryPointLifecycleTombstone{}) &&
		!source.db.Migrator().HasTable(&model.BackupAssetManagedHistoryLatch{}) {
		return false, nil
	}
	var count int64
	if source.db.Migrator().HasTable(&model.RecoveryPointLifecycleTombstone{}) {
		if err := source.db.WithContext(ctx).Model(&model.RecoveryPointLifecycleTombstone{}).
			Where("managed_history = ?", true).Count(&count).Error; err != nil {
			return false, fmt.Errorf("query installation lifecycle tombstone: %w", err)
		}
		if count > 0 {
			return true, nil
		}
	}
	if source.db.Migrator().HasTable(&model.BackupAssetManagedHistoryLatch{}) {
		if err := source.db.WithContext(ctx).Model(&model.BackupAssetManagedHistoryLatch{}).
			Where("scope = ?", managedHistoryLatchScopeInstallation).
			Count(&count).Error; err != nil {
			return false, fmt.Errorf("query installation managed history latch tombstone: %w", err)
		}
		return count > 0, nil
	}
	return false, nil
}

func NewManagedHistoryResolver(dependencies ManagedHistoryResolverDependencies) (*ManagedHistoryResolver, error) {
	if dependencies.DB == nil {
		return nil, fmt.Errorf("%w: managed history database is unavailable", backupasset.ErrInvalidState)
	}
	return &ManagedHistoryResolver{db: dependencies.DB, tombstones: dependencies.Tombstones}, nil
}

func (resolver *ManagedHistoryResolver) HasRepositoryManagedHistory(ctx context.Context, repositoryID string) (bool, error) {
	if resolver == nil || resolver.db == nil || backupasset.ValidateOpaqueID(repositoryID) != nil {
		return false, fmt.Errorf("%w: invalid managed history repository query", backupasset.ErrInvalidState)
	}
	var count int64
	if err := resolver.db.WithContext(ctx).Model(&model.BackupAssetManagedHistoryLatch{}).
		Where("scope = ? AND repository_id = ?", managedHistoryLatchScopeRepository, repositoryID).
		Count(&count).Error; err != nil {
		return false, fmt.Errorf("query repository managed history latch: %w", err)
	}
	if count > 0 {
		return true, nil
	}
	if err := resolver.db.WithContext(ctx).Model(&model.RecoveryPoint{}).
		Where("repository_id = ? AND semantics IN ?", repositoryID, managedHistoryPointSemantics()).
		Count(&count).Error; err != nil {
		return false, fmt.Errorf("query repository managed history: %w", err)
	}
	if count > 0 {
		return true, nil
	}
	if err := resolver.db.WithContext(ctx).Model(&model.TaskRepositoryLink{}).
		Where("repository_id = ? AND unlinked_at IS NULL AND publication_mode IN ?", repositoryID, managedHistoryPublicationModes()).
		Count(&count).Error; err != nil {
		return false, fmt.Errorf("query repository managed history links: %w", err)
	}
	tombstoneHistory, err := resolver.repositoryTombstoneHistory(ctx, repositoryID)
	if err != nil {
		return false, err
	}
	return count > 0 || tombstoneHistory, nil
}

func (resolver *ManagedHistoryResolver) HasInstallationManagedHistory(ctx context.Context) (bool, error) {
	if resolver == nil || resolver.db == nil {
		return false, fmt.Errorf("%w: managed history database is unavailable", backupasset.ErrInvalidState)
	}
	var count int64
	if err := resolver.db.WithContext(ctx).Model(&model.BackupAssetManagedHistoryLatch{}).
		Where("scope = ?", managedHistoryLatchScopeInstallation).
		Count(&count).Error; err != nil {
		return false, fmt.Errorf("query installation managed history latch: %w", err)
	}
	if count > 0 {
		return true, nil
	}
	if err := resolver.db.WithContext(ctx).Model(&model.RecoveryPoint{}).
		Where("semantics IN ?", managedHistoryPointSemantics()).
		Count(&count).Error; err != nil {
		return false, fmt.Errorf("query installation managed history: %w", err)
	}
	if count > 0 {
		return true, nil
	}
	if err := resolver.db.WithContext(ctx).Model(&model.TaskRepositoryLink{}).
		Where("unlinked_at IS NULL AND publication_mode IN ?", managedHistoryPublicationModes()).
		Count(&count).Error; err != nil {
		return false, fmt.Errorf("query installation managed history links: %w", err)
	}
	tombstoneHistory, err := resolver.installationTombstoneHistory(ctx)
	if err != nil {
		return false, err
	}
	return count > 0 || tombstoneHistory, nil
}

func (resolver *ManagedHistoryResolver) HasActivePublicationLease(ctx context.Context) (bool, error) {
	if resolver == nil || resolver.db == nil {
		return false, fmt.Errorf("%w: managed history database is unavailable", backupasset.ErrInvalidState)
	}
	var count int64
	if err := resolver.db.WithContext(ctx).Model(&model.RecoveryPointLease{}).
		Where("holder_type IN ? AND status = ?", managedHistoryLeaseHolderTypes(), backupasset.LeaseActive).
		Count(&count).Error; err != nil {
		return false, fmt.Errorf("query active publication lease: %w", err)
	}
	return count > 0, nil
}

// rcloneCleanRollbackAvailable deliberately ignores the active managed link:
// that link is the object a clean rollback will atomically replace. Every
// durable reservation, repository latch/tombstone, or repository-scoped
// publication lease closes the clean window permanently for this workflow.
func (resolver *ManagedHistoryResolver) rcloneCleanRollbackAvailable(ctx context.Context, repositoryID string) (bool, error) {
	if resolver == nil || resolver.db == nil || backupasset.ValidateOpaqueID(repositoryID) != nil {
		return false, fmt.Errorf("%w: invalid Rclone clean rollback query", backupasset.ErrInvalidState)
	}
	var count int64
	if err := resolver.db.WithContext(ctx).Model(&model.BackupAssetManagedHistoryLatch{}).
		Where("scope = ? AND repository_id = ?", managedHistoryLatchScopeRepository, repositoryID).
		Count(&count).Error; err != nil {
		return false, fmt.Errorf("query Rclone rollback repository latch: %w", err)
	}
	if count > 0 {
		return false, nil
	}
	if err := resolver.db.WithContext(ctx).Model(&model.RecoveryPoint{}).
		Where("repository_id = ? AND semantics IN ?", repositoryID, managedHistoryPointSemantics()).
		Count(&count).Error; err != nil {
		return false, fmt.Errorf("query Rclone rollback reservations: %w", err)
	}
	if count > 0 {
		return false, nil
	}
	if err := resolver.db.WithContext(ctx).Model(&model.RecoveryPointLease{}).
		Joins("JOIN recovery_points ON recovery_points.id = recovery_point_leases.recovery_point_id").
		Where("recovery_points.repository_id = ? AND recovery_point_leases.holder_type IN ? AND recovery_point_leases.status = ?",
			repositoryID, managedHistoryLeaseHolderTypes(), backupasset.LeaseActive).
		Count(&count).Error; err != nil {
		return false, fmt.Errorf("query Rclone rollback publication leases: %w", err)
	}
	if count > 0 {
		return false, nil
	}
	tombstone, err := resolver.repositoryTombstoneHistory(ctx, repositoryID)
	if err != nil {
		return false, err
	}
	return !tombstone, nil
}

// legacyFallbackAllowed is the common fail-closed answer for disabled-mode
// mutable paths. An installation latch blocks an unlinked or ambiguous Task,
// but it does not retroactively disable a separately verified pristine mutable
// binding. Any active publication lease remains globally unsafe.
func (resolver *ManagedHistoryResolver) legacyFallbackAllowed(ctx context.Context, taskEntity model.Task) (bool, error) {
	if resolver == nil || resolver.db == nil || taskEntity.ID == 0 {
		return false, fmt.Errorf("%w: invalid legacy fallback query", backupasset.ErrInvalidState)
	}
	installationHistory, err := resolver.HasInstallationManagedHistory(ctx)
	if err != nil {
		return false, err
	}
	activeLease, err := resolver.HasActivePublicationLease(ctx)
	if err != nil {
		return false, err
	}
	if activeLease {
		return false, nil
	}
	state, err := resolver.legacyFallbackBindingState(ctx, taskEntity)
	if err != nil {
		return false, err
	}
	switch state {
	case legacyFallbackExactPristine:
		return true, nil
	case legacyFallbackUnlinked:
		return !installationHistory, nil
	default:
		return false, nil
	}
}

func (resolver *ManagedHistoryResolver) legacyFallbackBindingState(ctx context.Context, taskEntity model.Task) (legacyFallbackBindingState, error) {
	var links []model.TaskRepositoryLink
	if err := resolver.db.WithContext(ctx).Where("task_id = ? AND unlinked_at IS NULL", taskEntity.ID).Find(&links).Error; err != nil {
		return legacyFallbackBlocked, fmt.Errorf("load legacy fallback Task links: %w", err)
	}
	if len(links) == 0 {
		return legacyFallbackUnlinked, nil
	}
	if len(links) != 1 {
		return legacyFallbackBlocked, nil
	}
	link := links[0]
	if link.TaskID == nil || *link.TaskID != taskEntity.ID || backupasset.ValidateOpaqueID(link.ID) != nil ||
		backupasset.ValidateOpaqueID(link.RepositoryID) != nil || link.PublicationMode != string(backupasset.PublicationLegacyMutable) ||
		strings.TrimSpace(link.EncryptedLegacyLocator) == "" {
		return legacyFallbackBlocked, nil
	}

	var repository model.BackupRepository
	if err := resolver.db.WithContext(ctx).First(&repository, "id = ?", link.RepositoryID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return legacyFallbackBlocked, nil
		}
		return legacyFallbackBlocked, fmt.Errorf("load legacy fallback repository: %w", err)
	}
	if repository.ProviderKind != string(bindingProviderForTask(taskEntity)) || repository.VersionMode != string(backupasset.VersionMutableHead) {
		return legacyFallbackBlocked, nil
	}
	repositoryHistory, err := resolver.HasRepositoryManagedHistory(ctx, repository.ID)
	if err != nil {
		return legacyFallbackBlocked, err
	}
	if repositoryHistory {
		return legacyFallbackBlocked, nil
	}

	var binding model.RepositoryAccessBinding
	if err := resolver.db.WithContext(ctx).Where("repository_id = ? AND status = ?", repository.ID, bindingStatusActive).First(&binding).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return legacyFallbackBlocked, nil
		}
		return legacyFallbackBlocked, fmt.Errorf("load legacy fallback binding: %w", err)
	}
	document, err := decodeBindingDocument(binding.EncryptedConfig)
	if err != nil {
		return legacyFallbackBlocked, nil
	}
	if document.Provider != bindingProviderForTask(taskEntity) || document.TaskID != taskEntity.ID || document.NodeID != taskEntity.NodeID ||
		document.Locator != taskEntity.RsyncTarget || document.Locator != link.EncryptedLegacyLocator {
		return legacyFallbackBlocked, nil
	}
	return legacyFallbackExactPristine, nil
}

func managedHistoryPointSemantics() []string {
	return []string{
		string(backupasset.PointNativeSnapshot),
		string(backupasset.PointXirangManifest),
		string(backupasset.PointImportedBaseline),
	}
}

func managedHistoryLeaseHolderTypes() []string {
	return []string{
		string(backupasset.LeaseHolderPointPublication),
		string(backupasset.LeaseHolderRsyncParent),
	}
}

func managedHistoryPublicationModes() []string {
	return []string{
		string(backupasset.PublicationNativeSnapshot),
		string(backupasset.PublicationVersionedHardlink),
		string(backupasset.PublicationVersionedFullCopy),
		string(backupasset.PublicationVersionedPrefix),
		string(backupasset.PublicationNativeObjectVersions),
	}
}

func (resolver *ManagedHistoryResolver) repositoryTombstoneHistory(ctx context.Context, repositoryID string) (bool, error) {
	if resolver.tombstones == nil {
		return false, nil
	}
	found, err := resolver.tombstones.HasRepositoryManagedHistory(ctx, repositoryID)
	if err != nil {
		return false, fmt.Errorf("query repository managed history tombstone: %w", err)
	}
	return found, nil
}

func (resolver *ManagedHistoryResolver) installationTombstoneHistory(ctx context.Context) (bool, error) {
	if resolver.tombstones == nil {
		return false, nil
	}
	found, err := resolver.tombstones.HasInstallationManagedHistory(ctx)
	if err != nil {
		return false, fmt.Errorf("query installation managed history tombstone: %w", err)
	}
	return found, nil
}
