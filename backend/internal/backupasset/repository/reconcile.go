package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/catalog"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type repositoryRuntime struct {
	repository model.BackupRepository
	binding    model.RepositoryAccessBinding
	document   bindingDocument
	access     provider.AccessBinding
	task       model.Task
}

func (service *Service) Reconcile(ctx context.Context, repositoryID string, requestContext RequestContext) (ConnectResult, error) {
	if err := service.ensureEnabled(requestContext.CorrelationID); err != nil {
		return ConnectResult{}, err
	}
	if err := service.requireRuntime(); err != nil {
		return ConnectResult{}, err
	}
	return service.reconcileLoaded(ctx, repositoryID, requestContext, "", true)
}

// RefreshMutableObservation re-probes the exact live binding that produces a
// mutable point. It is deliberately narrower than the public Reconcile path:
// it cannot create a repository, select a replacement Task, or revive a
// disconnected repository.
func (service *Service) RefreshMutableObservation(ctx context.Context, request catalog.PointReadRequest) error {
	if service == nil || backupasset.ValidateOpaqueID(request.RepositoryID) != nil ||
		backupasset.ValidateOpaqueID(request.RecoveryPointID) != nil {
		return fmt.Errorf("%w: mutable observation refresh request", backupasset.ErrInvalidState)
	}
	if err := service.ensureEnabled(""); err != nil {
		return err
	}
	if err := service.requireRuntime(); err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	_, err := service.reconcileLoaded(ctx, request.RepositoryID, RequestContext{}, request.RecoveryPointID, false)
	return err
}

type backupSourceCompletionTarget struct {
	taskID       uint
	repositoryID string
	linkID       string
	pointID      string
}

// ObserveBackupSourceCompletion refreshes and invalidates the exact legacy
// mutable Rsync source owned by taskID. It never creates a Repository or picks
// a replacement link; disabled, foreign, managed, disconnected, and
// unconnected tasks are intentionally ignored.
func (service *Service) ObserveBackupSourceCompletion(ctx context.Context, taskID uint) error {
	if service == nil || taskID == 0 {
		return fmt.Errorf("%w: backup source completion observer", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if service.foundation == nil {
		return fmt.Errorf("%w: backup source completion foundation unavailable", backupasset.ErrInvalidState)
	}
	enabled, err := service.foundation.FeatureEnabled()
	if err != nil {
		return err
	}
	if !enabled {
		return nil
	}
	if err := service.requireRuntime(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	target, applicable, err := service.loadBackupSourceCompletionTarget(ctx, taskID)
	if err != nil || !applicable {
		return err
	}
	if err := service.RefreshMutableObservation(ctx, catalog.PointReadRequest{
		RepositoryID: target.repositoryID, RecoveryPointID: target.pointID,
	}); err != nil {
		return err
	}
	stillApplicable, err := service.invalidateBackupSourceCatalog(ctx, target)
	if err != nil {
		return err
	}
	if stillApplicable {
		service.requestCatalogWake()
	}
	return nil
}

// MutablePreviewPreparationResult reports whether the exact mutable source
// invalidation happened or the caller joined work already in progress. A
// preparation call never creates a binding or starts a second worker.
type MutablePreviewPreparationResult struct {
	Invalidated bool
	Joined      bool
}

// PrepareMutablePreviewSource performs the exact-generation CAS half of a
// preview-source preparation. The caller has already authorized and statted
// the requested active Catalog tuple. This method only admits an already
// connected legacy mutable Rsync binding, atomically invalidates the observed
// generation, and wakes the existing Catalog worker. The worker owns source
// re-observation before freezing a replacement generation.
func (service *Service) PrepareMutablePreviewSource(
	ctx context.Context,
	request catalog.PointReadRequest,
	expectedGenerationID string,
) (MutablePreviewPreparationResult, error) {
	var result MutablePreviewPreparationResult
	if service == nil || backupasset.ValidateOpaqueID(request.RepositoryID) != nil ||
		backupasset.ValidateOpaqueID(request.RecoveryPointID) != nil ||
		(expectedGenerationID != "" && backupasset.ValidateOpaqueID(expectedGenerationID) != nil) {
		return result, fmt.Errorf("%w: mutable preview preparation request", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := service.ensureEnabled(""); err != nil {
		return result, err
	}
	if err := service.requireRuntime(); err != nil {
		return result, err
	}
	target, applicable, err := service.loadMutablePreviewTarget(ctx, request)
	if err != nil || !applicable {
		return result, err
	}
	err = service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var taskEntity model.Task
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", target.taskID).First(&taskEntity).Error; err != nil {
			return fmt.Errorf("%w: mutable preview Task changed", backupasset.ErrConflict)
		}
		if taskEntity.ArchivedAt != nil || bindingProviderForTask(taskEntity) != backupasset.ProviderRsync {
			return fmt.Errorf("%w: mutable preview Task changed", backupasset.ErrConflict)
		}

		var link model.TaskRepositoryLink
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND task_id = ? AND repository_id = ? AND publication_mode = ? AND unlinked_at IS NULL",
				target.linkID, target.taskID, target.repositoryID, backupasset.PublicationLegacyMutable).
			First(&link).Error; err != nil {
			return fmt.Errorf("%w: mutable preview Task link changed", backupasset.ErrConflict)
		}

		var repository model.BackupRepository
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", target.repositoryID).First(&repository).Error; err != nil {
			return fmt.Errorf("%w: mutable preview repository changed", backupasset.ErrConflict)
		}
		if repository.ProviderKind != string(backupasset.ProviderRsync) ||
			repository.VersionMode != string(backupasset.VersionMutableHead) {
			return fmt.Errorf("%w: mutable preview repository contract changed", backupasset.ErrConflict)
		}
		switch backupasset.RepositoryStatus(repository.Status) {
		case backupasset.RepositoryOnline:
		case backupasset.RepositoryOffline:
			return capabilityError(backupasset.CapabilityRepositoryOffline, "")
		case backupasset.RepositoryDisconnected:
			return capabilityError(backupasset.CapabilityRepositoryDisconnected, "")
		default:
			return fmt.Errorf("%w: mutable preview repository status changed", backupasset.ErrConflict)
		}

		var point model.RecoveryPoint
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND repository_id = ? AND semantics = ? AND state = ? AND producing_task_id = ?",
				target.pointID, target.repositoryID, backupasset.PointMutableHead, backupasset.RecoveryPointObserved, target.taskID).
			First(&point).Error; err != nil {
			return fmt.Errorf("%w: mutable preview point changed", backupasset.ErrConflict)
		}
		if point.PhysicalAvailability != string(backupasset.PhysicalOnline) {
			return capabilityError(backupasset.CapabilityProviderUnavailable, "")
		}
		if err := backupasset.ValidateRecoveryPointWriteAdmissionTx(ctx, tx, point.ID); err != nil {
			return err
		}
		now := service.utcNow()
		liveCatalogBuild, err := service.reconcileExpiredCatalogBuildLeasesTx(ctx, tx, target.pointID, now)
		if err != nil {
			return err
		}

		var active model.CatalogGeneration
		activeResult := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("recovery_point_id = ? AND state = ? AND is_active = ?", target.pointID, catalog.GenerationComplete, true).
			Limit(1).Find(&active)
		if activeResult.Error != nil {
			return fmt.Errorf("load mutable preview active Catalog generation: %w", activeResult.Error)
		}
		if activeResult.RowsAffected != 1 {
			if expectedGenerationID != "" {
				result.Joined = true
				return nil
			}
			var latest model.CatalogGeneration
			latestResult := tx.WithContext(ctx).
				Where("recovery_point_id = ?", target.pointID).
				Order("generation DESC, id DESC").Limit(1).Find(&latest)
			if latestResult.Error != nil {
				return fmt.Errorf("load mutable preview latest Catalog generation: %w", latestResult.Error)
			}
			if latestResult.RowsAffected != 1 {
				result.Joined = true
				return nil
			}
			if latest.State == string(catalog.GenerationBuilding) {
				if liveCatalogBuild {
					result.Joined = true
					return nil
				}
				updated := tx.WithContext(ctx).Model(&model.CatalogGeneration{}).
					Where("id = ? AND recovery_point_id = ? AND state = ? AND is_active = ?",
						latest.ID, target.pointID, catalog.GenerationBuilding, false).
					Updates(map[string]any{
						"state": catalog.GenerationSuperseded, "is_active": false,
						"finished_at": gorm.Expr("COALESCE(finished_at, ?)", now), "updated_at": now,
					})
				if updated.Error != nil {
					return fmt.Errorf("rearm orphaned mutable preview Catalog generation: %w", updated.Error)
				}
				if updated.RowsAffected == 1 {
					result.Invalidated = true
				} else {
					result.Joined = true
				}
				return nil
			}
			if latest.State == string(catalog.GenerationPartial) || latest.State == string(catalog.GenerationFailed) {
				now := service.utcNow()
				updated := tx.WithContext(ctx).Model(&model.CatalogGeneration{}).
					Where("id = ? AND recovery_point_id = ? AND state IN ? AND is_active = ?",
						latest.ID, target.pointID,
						[]string{string(catalog.GenerationPartial), string(catalog.GenerationFailed)}, false).
					Updates(map[string]any{
						"state": catalog.GenerationSuperseded, "is_active": false,
						"finished_at": gorm.Expr("COALESCE(finished_at, ?)", now), "updated_at": now,
					})
				if updated.Error != nil {
					return fmt.Errorf("rearm mutable preview latest Catalog generation: %w", updated.Error)
				}
				if updated.RowsAffected == 1 {
					result.Invalidated = true
				} else {
					result.Joined = true
				}
				return nil
			}
			result.Joined = true
			return nil
		}
		if expectedGenerationID == "" || active.ID != expectedGenerationID {
			result.Joined = true
			return nil
		}

		var buildingCount int64
		if err := tx.WithContext(ctx).Model(&model.CatalogGeneration{}).
			Where("recovery_point_id = ? AND state = ?", target.pointID, catalog.GenerationBuilding).
			Count(&buildingCount).Error; err != nil {
			return fmt.Errorf("check mutable preview Catalog builds: %w", err)
		}
		if buildingCount != 0 {
			if !liveCatalogBuild {
				obsoleteStates := []string{
					string(catalog.GenerationBuilding), string(catalog.GenerationPartial), string(catalog.GenerationFailed),
				}
				updated := tx.WithContext(ctx).Model(&model.CatalogGeneration{}).
					Where("recovery_point_id = ? AND state IN ? AND is_active = ?", target.pointID, obsoleteStates, false).
					Updates(map[string]any{
						"state": catalog.GenerationSuperseded, "is_active": false,
						"finished_at": gorm.Expr("COALESCE(finished_at, ?)", now), "updated_at": now,
					})
				if updated.Error != nil {
					return fmt.Errorf("rearm orphaned mutable preview Catalog generations: %w", updated.Error)
				}
			}
			updated := tx.WithContext(ctx).Model(&model.CatalogGeneration{}).
				Where("id = ? AND recovery_point_id = ? AND state = ? AND is_active = ?",
					expectedGenerationID, target.pointID, catalog.GenerationComplete, true).
				Updates(map[string]any{"state": catalog.GenerationSuperseded, "is_active": false, "updated_at": now})
			if updated.Error != nil {
				return fmt.Errorf("invalidate mutable preview Catalog generation: %w", updated.Error)
			}
			if updated.RowsAffected != 1 {
				result.Joined = true
				return nil
			}
			result.Invalidated = true
			return nil
		}
		obsoleteStates := []string{
			string(catalog.GenerationPartial), string(catalog.GenerationFailed),
		}
		updated := tx.WithContext(ctx).Model(&model.CatalogGeneration{}).
			Where("recovery_point_id = ? AND state IN ? AND is_active = ?", target.pointID, obsoleteStates, false).
			Updates(map[string]any{
				"state": catalog.GenerationSuperseded, "is_active": false,
				"finished_at": gorm.Expr("COALESCE(finished_at, ?)", now), "updated_at": now,
			})
		if updated.Error != nil {
			return fmt.Errorf("supersede mutable preview Catalog generations: %w", updated.Error)
		}
		updated = tx.WithContext(ctx).Model(&model.CatalogGeneration{}).
			Where("id = ? AND recovery_point_id = ? AND state = ? AND is_active = ?",
				expectedGenerationID, target.pointID, catalog.GenerationComplete, true).
			Updates(map[string]any{"state": catalog.GenerationSuperseded, "is_active": false, "updated_at": now})
		if updated.Error != nil {
			return fmt.Errorf("invalidate mutable preview Catalog generation: %w", updated.Error)
		}
		if updated.RowsAffected != 1 {
			result.Joined = true
			return nil
		}
		result.Invalidated = true
		return nil
	})
	if err != nil {
		return result, err
	}
	if result.Invalidated || result.Joined {
		service.requestCatalogWake()
	}
	return result, nil
}

// reconcileExpiredCatalogBuildLeasesTx releases only Catalog worker leases
// whose own lease window or absolute deadline has elapsed. The caller holds
// the recovery-point row lock before taking these lease locks, preserving the
// point-before-lease order used by source preparation. The exact fence fields
// are part of every expiry update so a concurrent renew or takeover cannot be
// mistaken for the stale lease observed at the start of this transaction.
func (service *Service) reconcileExpiredCatalogBuildLeasesTx(
	ctx context.Context,
	tx *gorm.DB,
	pointID string,
	now time.Time,
) (bool, error) {
	if service == nil || tx == nil || backupasset.ValidateOpaqueID(pointID) != nil || now.IsZero() {
		return false, fmt.Errorf("%w: expired Catalog lease reconciliation request", backupasset.ErrInvalidState)
	}
	var leases []model.RecoveryPointLease
	loaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("recovery_point_id = ? AND holder_type = ? AND status = ?",
			pointID, backupasset.LeaseHolderCatalogBuild, backupasset.LeaseActive).
		Order("id").Find(&leases)
	if loaded.Error != nil {
		return false, fmt.Errorf("load mutable preview Catalog leases: %w", loaded.Error)
	}
	live := false
	for _, lease := range leases {
		if now.Before(lease.LeaseExpiresAt.UTC()) && now.Before(lease.AbsoluteDeadline.UTC()) {
			live = true
			continue
		}
		expired := tx.WithContext(ctx).Model(&model.RecoveryPointLease{}).
			Where(`id = ? AND recovery_point_id = ? AND holder_type = ? AND owner_id = ? AND attempt_id = ?
				AND fence_token = ? AND status = ? AND (lease_expires_at <= ? OR absolute_deadline <= ?)`,
				lease.ID, pointID, backupasset.LeaseHolderCatalogBuild, lease.OwnerID, lease.AttemptID,
				lease.FenceToken, backupasset.LeaseActive, now, now).
			Updates(map[string]any{
				"status": backupasset.LeaseExpired, "updated_at": now,
			})
		if expired.Error != nil {
			return false, fmt.Errorf("expire mutable preview Catalog lease: %w", expired.Error)
		}
		if expired.RowsAffected == 1 {
			continue
		}

		// A lease worker may renew without taking the point lock. Re-read
		// after a lost CAS and fail closed if that worker still owns a live
		// fence; a second stale active row is never stolen here.
		var current model.RecoveryPointLease
		reloaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", lease.ID).Limit(1).Find(&current)
		if reloaded.Error != nil {
			return false, fmt.Errorf("reload mutable preview Catalog lease: %w", reloaded.Error)
		}
		if reloaded.RowsAffected != 1 || backupasset.LeaseStatus(current.Status) != backupasset.LeaseActive {
			continue
		}
		if now.Before(current.LeaseExpiresAt.UTC()) && now.Before(current.AbsoluteDeadline.UTC()) {
			live = true
			continue
		}
		return false, fmt.Errorf("%w: mutable preview Catalog lease fence changed", backupasset.ErrConflict)
	}
	return live, nil
}

func (service *Service) loadMutablePreviewTarget(
	ctx context.Context,
	request catalog.PointReadRequest,
) (backupSourceCompletionTarget, bool, error) {
	var point model.RecoveryPoint
	result := service.db.WithContext(ctx).
		Where("id = ? AND repository_id = ?", request.RecoveryPointID, request.RepositoryID).
		First(&point)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return backupSourceCompletionTarget{}, false, fmt.Errorf("%w: mutable preview point", backupasset.ErrNotFound)
	}
	if result.Error != nil {
		return backupSourceCompletionTarget{}, false, fmt.Errorf("load mutable preview point: %w", result.Error)
	}
	if point.Semantics != string(backupasset.PointMutableHead) ||
		point.State != string(backupasset.RecoveryPointObserved) || point.ProducingTaskID == nil {
		return backupSourceCompletionTarget{}, false, fmt.Errorf("%w: mutable preview point contract changed", backupasset.ErrConflict)
	}
	var repository model.BackupRepository
	result = service.db.WithContext(ctx).Where("id = ?", request.RepositoryID).First(&repository)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return backupSourceCompletionTarget{}, false, fmt.Errorf("%w: mutable preview repository", backupasset.ErrNotFound)
	}
	if result.Error != nil {
		return backupSourceCompletionTarget{}, false, fmt.Errorf("load mutable preview repository: %w", result.Error)
	}
	if repository.ProviderKind != string(backupasset.ProviderRsync) ||
		repository.VersionMode != string(backupasset.VersionMutableHead) {
		return backupSourceCompletionTarget{}, false, fmt.Errorf("%w: mutable preview repository contract changed", backupasset.ErrConflict)
	}
	switch backupasset.RepositoryStatus(repository.Status) {
	case backupasset.RepositoryOnline:
	case backupasset.RepositoryOffline:
		return backupSourceCompletionTarget{}, false, capabilityError(backupasset.CapabilityRepositoryOffline, "")
	case backupasset.RepositoryDisconnected:
		return backupSourceCompletionTarget{}, false, capabilityError(backupasset.CapabilityRepositoryDisconnected, "")
	default:
		return backupSourceCompletionTarget{}, false, fmt.Errorf("%w: mutable preview repository status is invalid", backupasset.ErrConflict)
	}
	if point.PhysicalAvailability != string(backupasset.PhysicalOnline) {
		return backupSourceCompletionTarget{}, false, capabilityError(backupasset.CapabilityProviderUnavailable, "")
	}
	target, applicable, err := service.loadBackupSourceCompletionTarget(ctx, *point.ProducingTaskID)
	if err != nil || !applicable {
		if err != nil {
			return backupSourceCompletionTarget{}, false, err
		}
		return backupSourceCompletionTarget{}, false, fmt.Errorf("%w: mutable preview binding unavailable", backupasset.ErrConflict)
	}
	if target.repositoryID != request.RepositoryID || target.pointID != request.RecoveryPointID {
		return backupSourceCompletionTarget{}, false, fmt.Errorf("%w: mutable preview binding lineage changed", backupasset.ErrConflict)
	}
	return target, true, nil
}

func (service *Service) loadBackupSourceCompletionTarget(
	ctx context.Context,
	taskID uint,
) (backupSourceCompletionTarget, bool, error) {
	var taskEntity model.Task
	result := service.db.WithContext(ctx).Where("id = ?", taskID).First(&taskEntity)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return backupSourceCompletionTarget{}, false, nil
	}
	if result.Error != nil {
		return backupSourceCompletionTarget{}, false, fmt.Errorf("load backup source completion Task: %w", result.Error)
	}
	if taskEntity.ArchivedAt != nil || bindingProviderForTask(taskEntity) != backupasset.ProviderRsync {
		return backupSourceCompletionTarget{}, false, nil
	}
	var links []model.TaskRepositoryLink
	if err := service.db.WithContext(ctx).
		Where("task_id = ? AND unlinked_at IS NULL AND publication_mode = ?", taskID, backupasset.PublicationLegacyMutable).
		Order("created_at ASC, id ASC").Find(&links).Error; err != nil {
		return backupSourceCompletionTarget{}, false, fmt.Errorf("load backup source completion Task links: %w", err)
	}
	if len(links) == 0 {
		return backupSourceCompletionTarget{}, false, nil
	}
	if len(links) != 1 {
		return backupSourceCompletionTarget{}, false, fmt.Errorf("%w: ambiguous active legacy Rsync Task links", backupasset.ErrConflict)
	}
	link := links[0]
	var repository model.BackupRepository
	result = service.db.WithContext(ctx).Where("id = ?", link.RepositoryID).First(&repository)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return backupSourceCompletionTarget{}, false, fmt.Errorf("%w: linked Rsync repository", backupasset.ErrConflict)
	}
	if result.Error != nil {
		return backupSourceCompletionTarget{}, false, fmt.Errorf("load backup source completion repository: %w", result.Error)
	}
	if repository.ProviderKind != string(backupasset.ProviderRsync) ||
		repository.VersionMode != string(backupasset.VersionMutableHead) {
		return backupSourceCompletionTarget{}, false, fmt.Errorf("%w: legacy Rsync repository contract changed", backupasset.ErrConflict)
	}
	status := backupasset.RepositoryStatus(repository.Status)
	if status == backupasset.RepositoryDisconnected {
		return backupSourceCompletionTarget{}, false, nil
	}
	if status != backupasset.RepositoryOnline && status != backupasset.RepositoryOffline {
		return backupSourceCompletionTarget{}, false, fmt.Errorf("%w: legacy Rsync repository status is invalid", backupasset.ErrConflict)
	}
	var points []model.RecoveryPoint
	if err := service.db.WithContext(ctx).
		Where("repository_id = ? AND semantics = ? AND state = ? AND producing_task_id = ?",
			repository.ID, backupasset.PointMutableHead, backupasset.RecoveryPointObserved, taskID).
		Order("created_at ASC, id ASC").Find(&points).Error; err != nil {
		return backupSourceCompletionTarget{}, false, fmt.Errorf("load backup source completion points: %w", err)
	}
	if len(points) == 0 {
		return backupSourceCompletionTarget{}, false, nil
	}
	if len(points) != 1 {
		return backupSourceCompletionTarget{}, false, fmt.Errorf("%w: ambiguous observed legacy Rsync points", backupasset.ErrConflict)
	}
	return backupSourceCompletionTarget{
		taskID: taskID, repositoryID: repository.ID, linkID: link.ID, pointID: points[0].ID,
	}, true, nil
}

func (service *Service) invalidateBackupSourceCatalog(
	ctx context.Context,
	target backupSourceCompletionTarget,
) (bool, error) {
	applicable := false
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var taskEntity model.Task
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", target.taskID).First(&taskEntity).Error; err != nil {
			return fmt.Errorf("revalidate backup source completion Task: %w", err)
		}
		if taskEntity.ArchivedAt != nil || bindingProviderForTask(taskEntity) != backupasset.ProviderRsync {
			return fmt.Errorf("%w: backup source completion Task changed", backupasset.ErrConflict)
		}
		var link model.TaskRepositoryLink
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND task_id = ? AND repository_id = ? AND publication_mode = ? AND unlinked_at IS NULL",
				target.linkID, target.taskID, target.repositoryID, backupasset.PublicationLegacyMutable).
			First(&link).Error; err != nil {
			return fmt.Errorf("%w: backup source completion Task link changed", backupasset.ErrConflict)
		}
		var repository model.BackupRepository
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", target.repositoryID).First(&repository).Error; err != nil {
			return fmt.Errorf("revalidate backup source completion repository: %w", err)
		}
		if repository.ProviderKind != string(backupasset.ProviderRsync) ||
			repository.VersionMode != string(backupasset.VersionMutableHead) {
			return fmt.Errorf("%w: backup source completion repository changed", backupasset.ErrConflict)
		}
		status := backupasset.RepositoryStatus(repository.Status)
		if status == backupasset.RepositoryDisconnected {
			return nil
		}
		if status != backupasset.RepositoryOnline && status != backupasset.RepositoryOffline {
			return fmt.Errorf("%w: backup source completion repository status changed", backupasset.ErrConflict)
		}
		var point model.RecoveryPoint
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND repository_id = ? AND semantics = ? AND state = ? AND producing_task_id = ?",
				target.pointID, target.repositoryID, backupasset.PointMutableHead, backupasset.RecoveryPointObserved, target.taskID).
			First(&point).Error; err != nil {
			return fmt.Errorf("%w: backup source completion point changed", backupasset.ErrConflict)
		}
		if err := backupasset.ValidateRecoveryPointWriteAdmissionTx(ctx, tx, point.ID); err != nil {
			return err
		}
		now := service.utcNow()
		obsoleteStates := []string{
			string(catalog.GenerationBuilding), string(catalog.GenerationPartial), string(catalog.GenerationFailed),
		}
		result := tx.WithContext(ctx).Model(&model.CatalogGeneration{}).
			Where("recovery_point_id = ? AND state IN ?", target.pointID, obsoleteStates).
			Updates(map[string]any{
				"state": catalog.GenerationSuperseded, "is_active": false,
				"finished_at": gorm.Expr("COALESCE(finished_at, ?)", now), "updated_at": now,
			})
		if result.Error != nil {
			return fmt.Errorf("supersede completion-obsolete Catalog generations: %w", result.Error)
		}
		result = tx.WithContext(ctx).Model(&model.CatalogGeneration{}).
			Where("recovery_point_id = ? AND state = ? AND is_active = ?", target.pointID, catalog.GenerationComplete, true).
			Updates(map[string]any{"state": catalog.GenerationSuperseded, "is_active": false, "updated_at": now})
		if result.Error != nil {
			return fmt.Errorf("invalidate active Catalog generation: %w", result.Error)
		}
		applicable = true
		return nil
	})
	return applicable, err
}

func (service *Service) reconcileLoaded(
	ctx context.Context,
	repositoryID string,
	requestContext RequestContext,
	expectedPointID string,
	wakeCatalog bool,
) (ConnectResult, error) {
	runtime, err := service.loadRepositoryRuntime(ctx, repositoryID)
	if err != nil {
		return ConnectResult{}, err
	}
	linkSnapshot, err := service.connectTaskLinkSnapshot(ctx, runtime.task.ID)
	if err != nil {
		return ConnectResult{}, err
	}
	if linkSnapshot.active == nil || linkSnapshot.active.RepositoryID != repositoryID {
		return ConnectResult{}, fmt.Errorf("%w: binding Task link lineage changed", backupasset.ErrConflict)
	}
	if expectedPointID != "" {
		status := backupasset.RepositoryStatus(runtime.repository.Status)
		if status != backupasset.RepositoryOnline && status != backupasset.RepositoryOffline {
			return ConnectResult{}, fmt.Errorf("%w: mutable observation repository status cannot be refreshed", backupasset.ErrConflict)
		}
		if err := service.validateMutableRefreshTarget(ctx, runtime, expectedPointID); err != nil {
			return ConnectResult{}, err
		}
	}
	runtime.access = withRemoteAuditContext(runtime.access, requestContext, runtime.document.TaskID)
	prober, err := service.registry.Prober(runtime.access.Provider)
	if err != nil {
		return ConnectResult{}, err
	}
	limits, err := service.providerOperationLimits()
	if err != nil {
		return ConnectResult{}, err
	}
	observation, probeErr := prober.Probe(ctx, runtime.access, limits)
	if expectedPointID != "" {
		if cancellationErr := ctx.Err(); cancellationErr != nil {
			return ConnectResult{}, cancellationErr
		}
	}
	if probeErr != nil {
		reason := capabilityReasonForProviderError(probeErr)
		failureContext := context.WithoutCancel(ctx)
		if expectedPointID != "" {
			failureContext = ctx
		}
		if stateErr := service.recordReconcileFailure(failureContext, runtime, linkSnapshot, expectedPointID, reason); stateErr != nil {
			return ConnectResult{}, stateErr
		}
		if expectedPointID == "" {
			service.writeAudit(context.WithoutCancel(ctx), requestContext, backupasset.AuditActionRepositoryReconcile, backupasset.AuditOutcomeFailure, repositoryID, &runtime.document.TaskID, "probe", probeErr)
		}
		return ConnectResult{}, probeErr
	}
	if validationErr := validateObservation(runtime.access, observation); validationErr != nil {
		reason := backupasset.CapabilityReason{Code: backupasset.CapabilityProviderProtocolIncompatible}
		failureContext := context.WithoutCancel(ctx)
		if expectedPointID != "" {
			failureContext = ctx
		}
		if stateErr := service.recordReconcileFailure(failureContext, runtime, linkSnapshot, expectedPointID, reason); stateErr != nil {
			return ConnectResult{}, stateErr
		}
		if expectedPointID == "" {
			service.writeAudit(context.WithoutCancel(ctx), requestContext, backupasset.AuditActionRepositoryReconcile, backupasset.AuditOutcomeFailure, repositoryID, &runtime.document.TaskID, "validate", validationErr)
		}
		return ConnectResult{}, validationErr
	}
	if runtime.repository.RepositoryIdentity == nil || *runtime.repository.RepositoryIdentity != observation.RepositoryIdentity {
		return ConnectResult{}, fmt.Errorf("%w: repository identity mismatch", backupasset.ErrConflict)
	}

	var repository model.BackupRepository
	var mutablePoint *model.RecoveryPoint
	runTransaction := func() error {
		repository = model.BackupRepository{}
		mutablePoint = nil
		return service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			currentTask, currentRepository, binding, err := lockAndRevalidateReconcileAuthority(tx, runtime, linkSnapshot)
			if err != nil {
				return err
			}
			repository = currentRepository
			if repository.RepositoryIdentity == nil ||
				*repository.RepositoryIdentity != observation.RepositoryIdentity || repository.ProviderKind != string(observation.Provider) {
				return fmt.Errorf("%w: repository identity changed during reconcile", backupasset.ErrConflict)
			}
			document, err := decodeBindingDocument(binding.EncryptedConfig)
			if err != nil {
				return err
			}
			capabilitiesJSON, err := json.Marshal(observation.Capabilities)
			if err != nil {
				return fmt.Errorf("marshal reconciled capabilities: %w", err)
			}
			capabilitiesChanged, err := effectiveCapabilitiesChanged(repository.CapabilitiesJSON, observation.Capabilities)
			if err != nil {
				return err
			}
			if capabilitiesChanged || document.AdapterRevision != observation.AdapterRevision {
				repository.CapabilityRevision++
			}
			if err := backupasset.ValidateRepositoryTransition(backupasset.RepositoryStatus(repository.Status), backupasset.RepositoryOnline); err != nil {
				return err
			}
			now := service.utcNow()
			repository.Status = string(backupasset.RepositoryOnline)
			repository.CapabilitiesJSON = string(capabilitiesJSON)
			repository.LastSeenAt = &now
			repository.LastReconciledAt = &now
			repository.UpdatedAt = now
			if err := tx.Save(&repository).Error; err != nil {
				return fmt.Errorf("update reconciled repository: %w", err)
			}

			document.Backend = observation.InternalProviderFacts["backend"]
			document.RangeProven = observation.Capabilities.OpenRange
			document.AdapterRevision = observation.AdapterRevision
			if observation.Provider == backupasset.ProviderRestic {
				document.NativeRepositoryID = strings.TrimPrefix(observation.RepositoryIdentity, provider.NativeResticIdentityPrefix)
			}
			payload, err := encodeBindingDocument(document)
			if err != nil {
				return err
			}
			fingerprint := observation.ConfigFingerprint
			if !isLowerHex64(fingerprint) {
				salt, err := hexDecodeSalt(document.IdentitySalt)
				if err != nil {
					return err
				}
				fingerprint, err = provider.DeriveConfigFingerprint(salt, []byte(payload))
				if err != nil {
					return err
				}
			}
			binding.EncryptedConfig = payload
			binding.ConfigFingerprint = fingerprint
			binding.UpdatedAt = now
			if err := tx.Save(&binding).Error; err != nil {
				return fmt.Errorf("update reconciled repository binding: %w", err)
			}
			if observation.VersionMode == backupasset.VersionMutableHead {
				point, err := ensureMutablePointForExpectedID(tx, repository, currentTask, observation, now, expectedPointID)
				if err != nil {
					return err
				}
				mutablePoint = point
			}
			return nil
		})
	}
	err = runTransaction()
	if isConnectConstraintConflict(err) {
		err = runTransaction()
	}
	if err != nil {
		if expectedPointID == "" {
			service.writeAudit(ctx, requestContext, backupasset.AuditActionRepositoryReconcile, backupasset.AuditOutcomeBlocked, repositoryID, &runtime.document.TaskID, "commit", err)
		}
		return ConnectResult{}, err
	}
	if wakeCatalog && mutablePointCatalogWakeable(mutablePoint) {
		service.requestCatalogWake()
	}
	result, err := connectResultFromModels(repository, mutablePoint)
	if err != nil {
		return ConnectResult{}, err
	}
	if expectedPointID == "" {
		service.writeAudit(ctx, requestContext, backupasset.AuditActionRepositoryReconcile, backupasset.AuditOutcomeSuccess, repositoryID, &runtime.document.TaskID, "commit", nil)
	}
	return result, nil
}

func (service *Service) validateMutableRefreshTarget(ctx context.Context, runtime repositoryRuntime, pointID string) error {
	var point model.RecoveryPoint
	err := service.db.WithContext(ctx).
		Where("id = ? AND repository_id = ? AND semantics = ?", pointID, runtime.repository.ID, backupasset.PointMutableHead).
		First(&point).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("%w: mutable Catalog point", backupasset.ErrConflict)
	}
	if err != nil {
		return fmt.Errorf("load mutable Catalog point for refresh: %w", err)
	}
	if point.State != string(backupasset.RecoveryPointObserved) || point.ProducingTaskID == nil ||
		*point.ProducingTaskID != runtime.task.ID {
		return fmt.Errorf("%w: mutable Catalog point producer changed", backupasset.ErrConflict)
	}
	return nil
}

func sameReconcileRepositoryProbeLineage(snapshot, current model.BackupRepository) bool {
	if snapshot.ID != current.ID || snapshot.ProviderKind != current.ProviderKind || snapshot.VersionMode != current.VersionMode ||
		snapshot.Status != current.Status || snapshot.CapabilityRevision != current.CapabilityRevision ||
		snapshot.CapabilitiesJSON != current.CapabilitiesJSON || snapshot.ImmutabilityLevel != current.ImmutabilityLevel ||
		!sameConnectOptionalTime(snapshot.LastSeenAt, current.LastSeenAt) ||
		!sameConnectOptionalTime(snapshot.LastReconciledAt, current.LastReconciledAt) {
		return false
	}
	if snapshot.RepositoryIdentity == nil || current.RepositoryIdentity == nil {
		return snapshot.RepositoryIdentity == nil && current.RepositoryIdentity == nil
	}
	return *snapshot.RepositoryIdentity == *current.RepositoryIdentity
}

func sameReconcileBindingProbeLineage(snapshot, current model.RepositoryAccessBinding) bool {
	return snapshot.ID == current.ID && snapshot.RepositoryID == current.RepositoryID && snapshot.BindingKind == current.BindingKind &&
		snapshot.Status == current.Status && snapshot.EncryptedConfig == current.EncryptedConfig &&
		snapshot.ConfigFingerprint == current.ConfigFingerprint && sameConnectOptionalTime(snapshot.RevokedAt, current.RevokedAt) &&
		snapshot.CreatedAt.Equal(current.CreatedAt) && snapshot.UpdatedAt.Equal(current.UpdatedAt)
}

func lockAndRevalidateReconcileRepository(
	tx *gorm.DB,
	runtime repositoryRuntime,
) (model.BackupRepository, error) {
	var repository model.BackupRepository
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&repository, "id = ?", runtime.repository.ID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.BackupRepository{}, fmt.Errorf("%w: repository changed during reconcile probe", backupasset.ErrConflict)
		}
		return model.BackupRepository{}, fmt.Errorf("reload repository after reconcile probe: %w", err)
	}
	if !sameReconcileRepositoryProbeLineage(runtime.repository, repository) {
		return model.BackupRepository{}, fmt.Errorf("%w: repository changed during reconcile probe", backupasset.ErrConflict)
	}
	return repository, nil
}

func lockAndRevalidateReconcileAuthority(
	tx *gorm.DB,
	runtime repositoryRuntime,
	linkSnapshot connectTaskLinkSnapshot,
) (model.Task, model.BackupRepository, model.RepositoryAccessBinding, error) {
	resticRepository := runtime.repository.ProviderKind == string(backupasset.ProviderRestic)
	var repository model.BackupRepository
	var err error
	if resticRepository {
		repository, err = lockAndRevalidateReconcileRepository(tx, runtime)
		if err != nil {
			return model.Task{}, model.BackupRepository{}, model.RepositoryAccessBinding{}, err
		}
	}

	currentTask, err := lockAndRevalidateConnectTask(tx, runtime.task)
	if err != nil {
		return model.Task{}, model.BackupRepository{}, model.RepositoryAccessBinding{}, err
	}
	if err := lockAndRevalidateConnectTaskLink(tx, currentTask.ID, linkSnapshot); err != nil {
		return model.Task{}, model.BackupRepository{}, model.RepositoryAccessBinding{}, err
	}
	if !resticRepository {
		repository, err = lockAndRevalidateReconcileRepository(tx, runtime)
		if err != nil {
			return model.Task{}, model.BackupRepository{}, model.RepositoryAccessBinding{}, err
		}
	}
	var binding model.RepositoryAccessBinding
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND repository_id = ? AND status = ?", runtime.binding.ID, repository.ID, bindingStatusActive).
		First(&binding).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.Task{}, model.BackupRepository{}, model.RepositoryAccessBinding{}, fmt.Errorf("%w: active repository binding changed during reconcile probe", backupasset.ErrConflict)
		}
		return model.Task{}, model.BackupRepository{}, model.RepositoryAccessBinding{}, fmt.Errorf("reload repository binding after reconcile probe: %w", err)
	}
	if !sameReconcileBindingProbeLineage(runtime.binding, binding) {
		return model.Task{}, model.BackupRepository{}, model.RepositoryAccessBinding{}, fmt.Errorf("%w: active repository binding changed during reconcile probe", backupasset.ErrConflict)
	}
	return currentTask, repository, binding, nil
}

func (service *Service) Disconnect(ctx context.Context, repositoryID string, requestContext RequestContext) (ConnectResult, error) {
	if err := service.ensureEnabled(requestContext.CorrelationID); err != nil {
		return ConnectResult{}, err
	}
	if service.db == nil || backupasset.ValidateOpaqueID(repositoryID) != nil {
		return ConnectResult{}, fmt.Errorf("%w: repository", backupasset.ErrNotFound)
	}
	var repository model.BackupRepository
	var mutablePoint *model.RecoveryPoint
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&repository, "id = ?", repositoryID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: repository", backupasset.ErrNotFound)
			}
			return fmt.Errorf("load repository for disconnect: %w", err)
		}
		now := service.utcNow()
		var binding model.RepositoryAccessBinding
		bindingErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("repository_id = ? AND status = ?", repositoryID, bindingStatusActive).First(&binding).Error
		if bindingErr == nil {
			if err := tx.Model(&model.RepositoryAccessBinding{}).Where("id = ? AND status = ?", binding.ID, bindingStatusActive).Updates(map[string]any{
				"status": bindingStatusRevoked, "revoked_at": now, "updated_at": now,
			}).Error; err != nil {
				return fmt.Errorf("revoke repository binding: %w", err)
			}
		} else if !errors.Is(bindingErr, gorm.ErrRecordNotFound) {
			return fmt.Errorf("load repository binding for disconnect: %w", bindingErr)
		} else if backupasset.RepositoryStatus(repository.Status) != backupasset.RepositoryDisconnected {
			return fmt.Errorf("%w: active repository binding missing", backupasset.ErrConflict)
		}
		if err := backupasset.ValidateRepositoryTransition(backupasset.RepositoryStatus(repository.Status), backupasset.RepositoryDisconnected); err != nil {
			return err
		}
		capabilities, err := capabilitiesWithReason(repository.CapabilitiesJSON, backupasset.CapabilityRepositoryDisconnected)
		if err != nil {
			return err
		}
		repository.Status = string(backupasset.RepositoryDisconnected)
		repository.CapabilitiesJSON = capabilities
		repository.LastReconciledAt = &now
		repository.UpdatedAt = now
		if err := tx.Save(&repository).Error; err != nil {
			return fmt.Errorf("update disconnected repository: %w", err)
		}
		var point model.RecoveryPoint
		pointErr := tx.Where("repository_id = ? AND semantics = ?", repositoryID, backupasset.PointMutableHead).First(&point).Error
		if pointErr == nil {
			if err := tx.Model(&model.RecoveryPoint{}).Where("id = ?", point.ID).Updates(map[string]any{
				"physical_availability": string(backupasset.PhysicalOffline), "capabilities_json": capabilities, "updated_at": now,
			}).Error; err != nil {
				return fmt.Errorf("mark disconnected mutable point offline: %w", err)
			}
			point.PhysicalAvailability = string(backupasset.PhysicalOffline)
			point.CapabilitiesJSON = capabilities
			point.UpdatedAt = now
			mutablePoint = &point
		} else if !errors.Is(pointErr, gorm.ErrRecordNotFound) {
			return fmt.Errorf("load mutable point for disconnect: %w", pointErr)
		}
		return nil
	})
	if err != nil {
		service.writeAudit(ctx, requestContext, backupasset.AuditActionRepositoryDisconnect, backupasset.AuditOutcomeBlocked, repositoryID, nil, "commit", err)
		return ConnectResult{}, err
	}
	result, err := connectResultFromModels(repository, mutablePoint)
	if err != nil {
		return ConnectResult{}, err
	}
	service.writeAudit(ctx, requestContext, backupasset.AuditActionRepositoryDisconnect, backupasset.AuditOutcomeSuccess, repositoryID, nil, "commit", nil)
	return result, nil
}

func (service *Service) loadRepositoryRuntime(ctx context.Context, repositoryID string) (repositoryRuntime, error) {
	return service.loadRepositoryRuntimeTx(ctx, service.db, repositoryID)
}

func (service *Service) loadRepositoryRuntimeTx(
	ctx context.Context,
	tx *gorm.DB,
	repositoryID string,
) (repositoryRuntime, error) {
	if service == nil || tx == nil || backupasset.ValidateOpaqueID(repositoryID) != nil {
		return repositoryRuntime{}, fmt.Errorf("%w: repository", backupasset.ErrNotFound)
	}
	var repository model.BackupRepository
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).First(&repository, "id = ?", repositoryID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return repositoryRuntime{}, fmt.Errorf("%w: repository", backupasset.ErrNotFound)
		}
		return repositoryRuntime{}, fmt.Errorf("load repository runtime: %w", err)
	}
	var binding model.RepositoryAccessBinding
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("repository_id = ? AND status = ?", repositoryID, bindingStatusActive).First(&binding).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return repositoryRuntime{}, fmt.Errorf("%w: active repository binding", backupasset.ErrConflict)
		}
		return repositoryRuntime{}, fmt.Errorf("load active repository binding: %w", err)
	}
	document, err := decodeBindingDocument(binding.EncryptedConfig)
	if err != nil {
		return repositoryRuntime{}, err
	}
	var taskEntity model.Task
	if err := tx.WithContext(ctx).Preload("Node.SSHKey").Clauses(clause.Locking{Strength: "UPDATE"}).First(&taskEntity, document.TaskID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return repositoryRuntime{}, fmt.Errorf("%w: binding Task unavailable", backupasset.ErrConflict)
		}
		return repositoryRuntime{}, fmt.Errorf("load binding Task: %w", err)
	}
	if taskEntity.NodeID != document.NodeID || taskEntity.ArchivedAt != nil {
		return repositoryRuntime{}, fmt.Errorf("%w: binding Task lineage changed", backupasset.ErrConflict)
	}
	if bindingProviderForTask(taskEntity) != document.Provider {
		return repositoryRuntime{}, fmt.Errorf("%w: binding Task Provider changed", backupasset.ErrConflict)
	}
	access, err := accessFromBindingDocument(document, taskEntity.Node)
	if err != nil {
		return repositoryRuntime{}, err
	}
	access.RepositoryID = repositoryID
	if string(access.Provider) != repository.ProviderKind {
		return repositoryRuntime{}, fmt.Errorf("%w: binding Provider mismatch", backupasset.ErrConflict)
	}
	return repositoryRuntime{repository: repository, binding: binding, document: document, access: access, task: taskEntity}, nil
}

func (service *Service) providerOperationLimits() (provider.OperationLimits, error) {
	config, err := service.foundation.ProviderConfig()
	if err != nil {
		return provider.OperationLimits{}, err
	}
	return provider.NewMetadataOperationLimits(config.OperationTimeout, config.MetadataLimitBytes)
}

func (service *Service) recordReconcileFailure(
	ctx context.Context,
	runtime repositoryRuntime,
	linkSnapshot connectTaskLinkSnapshot,
	expectedPointID string,
	reason backupasset.CapabilityReason,
) error {
	if err := backupasset.ValidateCapabilityReason(reason); err != nil {
		return err
	}
	return service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		_, repository, _, err := lockAndRevalidateReconcileAuthority(tx, runtime, linkSnapshot)
		if err != nil {
			return err
		}
		pointQuery := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("repository_id = ? AND semantics = ?", repository.ID, backupasset.PointMutableHead)
		if expectedPointID != "" {
			pointQuery = pointQuery.Where("id = ?", expectedPointID)
		}
		var mutablePoint model.RecoveryPoint
		pointErr := pointQuery.First(&mutablePoint).Error
		if pointErr != nil && !errors.Is(pointErr, gorm.ErrRecordNotFound) {
			return fmt.Errorf("lock mutable point for failed reconcile: %w", pointErr)
		}
		if expectedPointID != "" && errors.Is(pointErr, gorm.ErrRecordNotFound) {
			return fmt.Errorf("%w: mutable Catalog point changed during reconcile probe", backupasset.ErrConflict)
		}
		if err := backupasset.ValidateRepositoryTransition(backupasset.RepositoryStatus(repository.Status), backupasset.RepositoryOffline); err != nil {
			return err
		}
		capabilities, err := capabilitiesWithReason(repository.CapabilitiesJSON, reason.Code)
		if err != nil {
			return err
		}
		now := service.utcNow()
		repository.Status = string(backupasset.RepositoryOffline)
		repository.CapabilitiesJSON = capabilities
		repository.LastReconciledAt = &now
		repository.UpdatedAt = now
		if err := tx.Save(&repository).Error; err != nil {
			return fmt.Errorf("update failed repository observation: %w", err)
		}
		if pointErr == nil {
			if err := tx.Model(&model.RecoveryPoint{}).Where("id = ?", mutablePoint.ID).Updates(map[string]any{
				"physical_availability": string(backupasset.PhysicalOffline),
				"capabilities_json":     capabilities,
				"updated_at":            now,
			}).Error; err != nil {
				return fmt.Errorf("update failed mutable observation: %w", err)
			}
		}
		return nil
	})
}

func effectiveCapabilitiesChanged(raw string, observed backupasset.CapabilitySet) (bool, error) {
	current, err := decodeRepositoryCapabilities(raw)
	if err != nil {
		return false, err
	}
	current.Reason = nil
	observed.Reason = nil
	return current != observed, nil
}

func capabilitiesWithReason(raw string, code backupasset.CapabilityCode) (string, error) {
	capabilities, err := decodeRepositoryCapabilities(raw)
	if err != nil {
		return "", err
	}
	reason := backupasset.CapabilityReason{Code: code}
	if err := backupasset.ValidateCapabilityReason(reason); err != nil {
		return "", err
	}
	capabilities.Reason = &reason
	payload, err := json.Marshal(capabilities)
	if err != nil {
		return "", fmt.Errorf("marshal repository capability reason: %w", err)
	}
	return string(payload), nil
}

func decodeRepositoryCapabilities(raw string) (backupasset.CapabilitySet, error) {
	if strings.TrimSpace(raw) == "" {
		raw = "{}"
	}
	var capabilities backupasset.CapabilitySet
	if err := json.Unmarshal([]byte(raw), &capabilities); err != nil {
		return backupasset.CapabilitySet{}, fmt.Errorf("%w: invalid repository capabilities", backupasset.ErrInvalidState)
	}
	if capabilities.Reason != nil {
		if err := backupasset.ValidateCapabilityReason(*capabilities.Reason); err != nil {
			return backupasset.CapabilitySet{}, err
		}
	}
	return capabilities, nil
}

func capabilityReasonForProviderError(err error) backupasset.CapabilityReason {
	var capabilityErr *provider.CapabilityError
	if errors.As(err, &capabilityErr) && backupasset.ValidateCapabilityReason(capabilityErr.Reason) == nil {
		return capabilityErr.Reason
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return backupasset.CapabilityReason{Code: backupasset.CapabilityProviderOperationTimeout}
	}
	return backupasset.CapabilityReason{Code: backupasset.CapabilityProviderUnavailable}
}

func connectResultFromModels(repository model.BackupRepository, mutablePoint *model.RecoveryPoint) (ConnectResult, error) {
	repositoryDTO, err := backupasset.ToRepositoryDTO(repository)
	if err != nil {
		return ConnectResult{}, err
	}
	result := ConnectResult{Repository: repositoryDTO}
	if mutablePoint != nil {
		pointDTO, err := backupasset.ToRecoveryPointDTO(*mutablePoint, backupasset.VersionMode(repository.VersionMode))
		if err != nil {
			return ConnectResult{}, err
		}
		result.MutablePoint = &pointDTO
	}
	return result, nil
}
