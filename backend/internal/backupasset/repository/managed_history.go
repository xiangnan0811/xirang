package repository

import (
	"context"
	"fmt"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

// ManagedHistoryTombstoneSource is deliberately a narrow future port. Child 3
// has no tombstone table, while the lifecycle child can later preserve this
// permanent safety latch after a native RecoveryPoint row is removed.
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
	if err := resolver.db.WithContext(ctx).Model(&model.RecoveryPoint{}).
		Where("repository_id = ? AND semantics = ?", repositoryID, backupasset.PointNativeSnapshot).
		Count(&count).Error; err != nil {
		return false, fmt.Errorf("query repository managed history: %w", err)
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
	if err := resolver.db.WithContext(ctx).Model(&model.RecoveryPoint{}).
		Where("semantics = ?", backupasset.PointNativeSnapshot).
		Count(&count).Error; err != nil {
		return false, fmt.Errorf("query installation managed history: %w", err)
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
		Where("holder_type = ? AND status = ?", backupasset.LeaseHolderPointPublication, backupasset.LeaseActive).
		Count(&count).Error; err != nil {
		return false, fmt.Errorf("query active publication lease: %w", err)
	}
	return count > 0, nil
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
