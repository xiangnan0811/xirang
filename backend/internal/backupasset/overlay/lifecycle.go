package overlay

import (
	"context"
	"fmt"
	"sort"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type SourceLifecycle struct {
	RecoveryPointID string
	Reason          SourceReason
}

type LifecycleResult struct {
	SavedSearches  int64
	Favorites      int64
	TagAssignments int64
	RecentsDeleted int64
}

func (service *Service) ReconcileSource(ctx context.Context, source SourceLifecycle, limit int) (LifecycleResult, error) {
	if err := service.ensureFeatureEnabled(); err != nil {
		return LifecycleResult{}, err
	}
	if ValidateOverlayRefForPoint(source.RecoveryPointID) != nil || limit <= 0 || limit > 100000 {
		return LifecycleResult{}, ErrInvalidOverlay
	}
	savedReason, tombstoneReason, err := lifecycleReasons(source.Reason)
	if err != nil {
		return LifecycleResult{}, err
	}
	var result LifecycleResult
	err = service.mutation(ctx, func(tx *gorm.DB) error {
		now := service.utcNow()
		var savedIDs []string
		if err := tx.Table("backup_asset_saved_searches AS saved").
			Select("saved.id").Joins("JOIN backup_asset_saved_search_scope_points AS points ON points.saved_search_id = saved.id").
			Where("points.recovery_point_id = ? AND saved.state = ?", source.RecoveryPointID, SavedSearchActive).
			Order("saved.id ASC").Limit(limit).Scan(&savedIDs).Error; err != nil {
			return err
		}
		if len(savedIDs) > 0 {
			updated := tx.Model(&model.BackupAssetSavedSearch{}).Where("id IN ? AND state = ?", savedIDs, SavedSearchActive).
				Updates(map[string]any{
					"state": SavedSearchBroken, "state_reason": savedReason, "broken_at": now,
					"version": gorm.Expr("version + 1"), "updated_at": now,
				})
			if updated.Error != nil {
				return updated.Error
			}
			result.SavedSearches = updated.RowsAffected
		}
		var favoriteIDs []string
		if err := tx.Model(&model.BackupAssetFavorite{}).
			Where("recovery_point_id = ? AND state = ?", source.RecoveryPointID, OverlayActive).
			Order("id ASC").Limit(limit).Pluck("id", &favoriteIDs).Error; err != nil {
			return err
		}
		if len(favoriteIDs) > 0 {
			favoriteUpdate := tx.Model(&model.BackupAssetFavorite{}).
				Where("id IN ? AND state = ?", favoriteIDs, OverlayActive).
				Updates(map[string]any{
					"state": OverlayTombstone, "tombstone_reason": tombstoneReason,
					"version": gorm.Expr("version + 1"), "updated_at": now,
				})
			if favoriteUpdate.Error != nil {
				return favoriteUpdate.Error
			}
			result.Favorites = favoriteUpdate.RowsAffected
		}
		var assignmentIDs []string
		if err := tx.Model(&model.BackupAssetTagAssignment{}).
			Where("recovery_point_id = ? AND state = ?", source.RecoveryPointID, OverlayActive).
			Order("id ASC").Limit(limit).Pluck("id", &assignmentIDs).Error; err != nil {
			return err
		}
		if len(assignmentIDs) > 0 {
			assignmentUpdate := tx.Model(&model.BackupAssetTagAssignment{}).
				Where("id IN ? AND state = ?", assignmentIDs, OverlayActive).
				Updates(map[string]any{
					"state": OverlayTombstone, "tombstone_reason": tombstoneReason,
					"version": gorm.Expr("version + 1"), "updated_at": now,
				})
			if assignmentUpdate.Error != nil {
				return assignmentUpdate.Error
			}
			result.TagAssignments = assignmentUpdate.RowsAffected
		}
		var recents []model.BackupAssetRecentAccess
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where("recovery_point_id = ?", source.RecoveryPointID).Order("id ASC").Limit(limit).Find(&recents).Error; err != nil {
			return err
		}
		byOwner := make(map[uint]int64)
		ids := make([]string, 0, len(recents))
		for _, recent := range recents {
			ids = append(ids, recent.ID)
			byOwner[recent.OwnerUserID]++
		}
		if len(ids) > 0 {
			deleted := tx.Where("id IN ?", ids).Delete(&model.BackupAssetRecentAccess{})
			if deleted.Error != nil {
				return deleted.Error
			}
			if deleted.RowsAffected != int64(len(ids)) {
				return backupasset.ErrConflict
			}
			result.RecentsDeleted = deleted.RowsAffected
			owners := make([]uint, 0, len(byOwner))
			for ownerID := range byOwner {
				owners = append(owners, ownerID)
			}
			sort.Slice(owners, func(left, right int) bool { return owners[left] < owners[right] })
			for _, ownerID := range owners {
				usage, err := service.lockUsage(tx, ownerID)
				if err != nil {
					return err
				}
				if err := service.updateUsage(tx, usage, map[string]any{"recent_count": max(usage.RecentCount-byOwner[ownerID], 0)}); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return LifecycleResult{}, err
	}
	service.auditLifecycle(ctx, source, result)
	return result, nil
}

func (service *Service) ReconcileInvalidSources(ctx context.Context, limit int) (int64, error) {
	if err := service.ensureFeatureEnabled(); err != nil {
		return 0, err
	}
	if limit <= 0 || limit > 100000 {
		return 0, ErrInvalidOverlay
	}
	type invalidSource struct {
		RecoveryPointID string
		State           string
	}
	var sources []invalidSource
	query := `
		SELECT sources.recovery_point_id, COALESCE(points.state, '') AS state
		FROM (
			SELECT scope.recovery_point_id
			FROM backup_asset_saved_search_scope_points AS scope
			JOIN backup_asset_saved_searches AS saved ON saved.id = scope.saved_search_id
			WHERE saved.state = ?
			UNION
			SELECT recovery_point_id FROM backup_asset_favorites WHERE state = ?
			UNION
			SELECT recovery_point_id FROM backup_asset_tag_assignments WHERE state = ?
			UNION
			SELECT recovery_point_id FROM backup_asset_recent_access
		) AS sources
		LEFT JOIN recovery_points AS points ON points.id = sources.recovery_point_id
		WHERE points.id IS NULL OR points.state IN (?, ?, ?, ?, ?)
		ORDER BY sources.recovery_point_id ASC
		LIMIT ?`
	if err := service.db.WithContext(ctx).Raw(query,
		SavedSearchActive, OverlayActive, OverlayActive,
		backupasset.RecoveryPointRetired, backupasset.RecoveryPointExpiring, backupasset.RecoveryPointExpired,
		backupasset.RecoveryPointFailed, backupasset.RecoveryPointPurgeBlocked, limit,
	).Scan(&sources).Error; err != nil {
		return 0, err
	}
	var reconciled int64
	for _, source := range sources {
		reason, err := sourceReasonForPointState(source.State)
		if err != nil {
			return reconciled, err
		}
		result, err := service.ReconcileSource(ctx, SourceLifecycle{RecoveryPointID: source.RecoveryPointID, Reason: reason}, limit)
		if err != nil {
			return reconciled, err
		}
		if result != (LifecycleResult{}) {
			reconciled++
		}
	}
	return reconciled, nil
}

func (service *Service) CleanupExpiredRecent(ctx context.Context, limit int) (int64, error) {
	if err := service.ensureFeatureEnabled(); err != nil {
		return 0, err
	}
	if limit <= 0 || limit > 100000 {
		return 0, ErrInvalidOverlay
	}
	var deleted int64
	err := service.mutation(ctx, func(tx *gorm.DB) error {
		var rows []model.BackupAssetRecentAccess
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where("expires_at <= ?", service.utcNow()).Order("expires_at ASC, id ASC").Limit(limit).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		ids := make([]string, 0, len(rows))
		byOwner := make(map[uint]int64)
		for _, row := range rows {
			ids = append(ids, row.ID)
			byOwner[row.OwnerUserID]++
		}
		result := tx.Where("id IN ?", ids).Delete(&model.BackupAssetRecentAccess{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != int64(len(ids)) {
			return backupasset.ErrConflict
		}
		owners := make([]uint, 0, len(byOwner))
		for ownerID := range byOwner {
			owners = append(owners, ownerID)
		}
		sort.Slice(owners, func(left, right int) bool { return owners[left] < owners[right] })
		for _, ownerID := range owners {
			usage, err := service.lockUsage(tx, ownerID)
			if err != nil {
				return err
			}
			if err := service.updateUsage(tx, usage, map[string]any{
				"recent_count": max(usage.RecentCount-byOwner[ownerID], 0),
			}); err != nil {
				return err
			}
		}
		deleted = result.RowsAffected
		return nil
	})
	if err != nil {
		return 0, err
	}
	if deleted > 0 {
		service.writeAudit(ctx, backupasset.AuditEventInput{
			Action: backupasset.AuditActionOverlayCleanup, Outcome: backupasset.AuditOutcomeSuccess, ItemCount: deleted,
		})
	}
	return deleted, nil
}

func (service *Service) auditLifecycle(ctx context.Context, source SourceLifecycle, result LifecycleResult) {
	for _, event := range []struct {
		action backupasset.AuditAction
		count  int64
	}{
		{backupasset.AuditActionSavedSearchBroken, result.SavedSearches},
		{backupasset.AuditActionFavoriteTombstone, result.Favorites},
		{backupasset.AuditActionTagAssignmentTombstone, result.TagAssignments},
	} {
		if event.count == 0 {
			continue
		}
		service.writeAudit(ctx, backupasset.AuditEventInput{
			Action: event.action, Outcome: backupasset.AuditOutcomeSuccess, RecoveryPointID: source.RecoveryPointID,
			ItemCount: event.count, Fields: map[backupasset.AuditField]any{backupasset.AuditFieldReasonCode: string(source.Reason)},
		})
	}
	total := result.SavedSearches + result.Favorites + result.TagAssignments + result.RecentsDeleted
	if total > 0 {
		service.writeAudit(ctx, backupasset.AuditEventInput{
			Action: backupasset.AuditActionOverlayCleanup, Outcome: backupasset.AuditOutcomeSuccess,
			RecoveryPointID: source.RecoveryPointID, ItemCount: total,
			Fields: map[backupasset.AuditField]any{backupasset.AuditFieldReasonCode: string(source.Reason)},
		})
	}
}

func sourceReasonForPointState(state string) (SourceReason, error) {
	switch backupasset.RecoveryPointState(state) {
	case backupasset.RecoveryPointRetired:
		return SourceRetired, nil
	case backupasset.RecoveryPointExpiring:
		return SourceExpiring, nil
	case backupasset.RecoveryPointExpired:
		return SourceExpired, nil
	case backupasset.RecoveryPointFailed:
		return SourceFailed, nil
	case backupasset.RecoveryPointPurgeBlocked:
		return SourcePurgeBlocked, nil
	case "":
		return SourceMissing, nil
	default:
		return "", fmt.Errorf("%w: lifecycle source state", ErrInvalidOverlay)
	}
}

func ValidateOverlayRefForPoint(pointID string) error {
	if len(pointID) != 32 {
		return ErrInvalidOverlay
	}
	for _, character := range pointID {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return ErrInvalidOverlay
		}
	}
	return nil
}

func lifecycleReasons(reason SourceReason) (SavedSearchReason, TombstoneReason, error) {
	switch reason {
	case SourceRetired:
		return SavedReasonPointRetired, TombstoneSourceRetired, nil
	case SourceExpiring:
		return SavedReasonPointExpiring, TombstoneSourceExpiring, nil
	case SourceExpired:
		return SavedReasonPointExpired, TombstoneSourceExpired, nil
	case SourceFailed:
		return SavedReasonPointFailed, TombstoneSourceFailed, nil
	case SourcePurgeBlocked:
		return SavedReasonPointPurgeBlocked, TombstoneSourcePurgeBlocked, nil
	case SourceMissing:
		return SavedReasonPointMissing, TombstoneSourceMissing, nil
	default:
		return "", "", fmt.Errorf("%w: lifecycle reason", ErrInvalidOverlay)
	}
}
