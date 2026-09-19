package search

import (
	"context"
	"fmt"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SourceLifecycle owns point-scoped Search projections. Shared Search token
// keys are intentionally outside this owner's deletion boundary.
type SourceLifecycle struct {
	db        *gorm.DB
	indexer   *Indexer
	now       func() time.Time
	batchSize int
}

func NewSourceLifecycle(db *gorm.DB, indexer *Indexer, now func() time.Time, batchSize int) (*SourceLifecycle, error) {
	if db == nil || db.Config == nil || indexer == nil || indexer.db == nil || indexer.db.Config == nil || batchSize <= 0 || batchSize > 1000 {
		return nil, fmt.Errorf("%w: invalid Search source lifecycle dependencies", backupasset.ErrInvalidState)
	}
	ownerDB, ownerDBErr := db.DB()
	indexerDB, indexerDBErr := indexer.db.DB()
	if ownerDBErr != nil || indexerDBErr != nil || ownerDB == nil || ownerDB != indexerDB {
		return nil, fmt.Errorf("%w: invalid Search source lifecycle dependencies", backupasset.ErrInvalidState)
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &SourceLifecycle{db: db, indexer: indexer, now: now, batchSize: batchSize}, nil
}

func (owner *SourceLifecycle) RevokeRecoveryPoint(ctx context.Context, request backupasset.SourceLifecycleRequest) error {
	if owner == nil || owner.db == nil {
		return fmt.Errorf("%w: Search source lifecycle is unavailable", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := owner.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return backupasset.ValidateSourceLifecycleAttemptTx(ctx, tx, request)
	}); err != nil {
		return err
	}
	if err := owner.indexer.cancelAndJoinActiveBuild(ctx, request.RecoveryPointID); err != nil {
		return fmt.Errorf("cancel and join Search source builder: %w", err)
	}
	for {
		settled := false
		err := owner.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := backupasset.ValidateSourceLifecycleAttemptTx(ctx, tx, request); err != nil {
				return err
			}
			var generations []model.BackupAssetSearchGeneration
			query := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("recovery_point_id = ?", request.RecoveryPointID)
			if request.Stage == backupasset.SourceLifecyclePrepare {
				query = query.Where("state = ?", SearchGenerationBuilding)
			} else {
				query = query.Where(`state = ? OR is_active = ? OR
					EXISTS (SELECT 1 FROM backup_asset_search_documents WHERE backup_asset_search_documents.search_generation_id = backup_asset_search_generations.id) OR
					EXISTS (SELECT 1 FROM backup_asset_search_postings WHERE backup_asset_search_postings.search_generation_id = backup_asset_search_generations.id) OR
					EXISTS (SELECT 1 FROM backup_asset_search_document_fields WHERE backup_asset_search_document_fields.search_generation_id = backup_asset_search_generations.id)`,
					SearchGenerationBuilding, true)
			}
			if err := query.Order("id ASC").Limit(owner.batchSize).Find(&generations).Error; err != nil {
				return fmt.Errorf("load Search source generations: %w", err)
			}
			now := owner.now().UTC()
			if len(generations) == 0 {
				if err := owner.releaseSearchLeasesTx(ctx, tx, request.RecoveryPointID, now); err != nil {
					return err
				}
				settled = true
				return nil
			}
			payloadBudget := owner.batchSize
			for _, generation := range generations {
				if err := ctx.Err(); err != nil {
					return err
				}
				if generation.State == string(SearchGenerationBuilding) {
					if err := tx.WithContext(ctx).Model(&model.BackupAssetSearchGeneration{}).
						Where("id = ? AND recovery_point_id = ? AND state = ?", generation.ID, request.RecoveryPointID, SearchGenerationBuilding).
						Updates(map[string]any{"state": SearchGenerationFailed, "is_active": false, "error_code": SearchErrorBuildFailed, "finished_at": now, "updated_at": now}).Error; err != nil {
						return fmt.Errorf("fail Search source builder: %w", err)
					}
				}
				if request.Stage == backupasset.SourceLifecycleCleanup {
					deleted, payloadRemaining, err := DeleteGenerationProjectionBatchTx(ctx, tx, generation.ID, payloadBudget)
					if err != nil {
						return err
					}
					if payloadRemaining && deleted == 0 {
						return fmt.Errorf("%w: Search cleanup payload made no progress", backupasset.ErrInvalidState)
					}
					payloadBudget -= deleted
				}
				if request.Stage == backupasset.SourceLifecycleCleanup && generation.IsActive {
					if err := tx.WithContext(ctx).Model(&model.BackupAssetSearchGeneration{}).
						Where("id = ? AND recovery_point_id = ?", generation.ID, request.RecoveryPointID).
						Updates(map[string]any{"state": SearchGenerationSuperseded, "is_active": false, "updated_at": now}).Error; err != nil {
						return fmt.Errorf("supersede Search source generation: %w", err)
					}
				}
				if request.Stage == backupasset.SourceLifecycleCleanup && payloadBudget == 0 {
					break
				}
			}
			return owner.releaseSearchLeasesTx(ctx, tx, request.RecoveryPointID, now)
		})
		if err != nil {
			return err
		}
		if settled {
			break
		}
	}
	if request.Stage == backupasset.SourceLifecycleCleanup {
		return owner.ProveRecoveryPointRevoked(ctx, request)
	}
	if owner.indexer.activeBuildExists(request.RecoveryPointID) {
		return fmt.Errorf("%w: Search source builder remains active", backupasset.ErrConflict)
	}
	return nil
}

func (owner *SourceLifecycle) ProveRecoveryPointRevoked(ctx context.Context, request backupasset.SourceLifecycleRequest) error {
	if owner == nil || owner.db == nil || request.Stage != backupasset.SourceLifecycleCleanup {
		return fmt.Errorf("%w: invalid Search cleanup proof request", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return owner.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := backupasset.ValidateSourceLifecycleAttemptTx(ctx, tx, request); err != nil {
			return err
		}
		var activeGenerations, documents, postings, fields, liveLeases int64
		if err := tx.Model(&model.BackupAssetSearchGeneration{}).
			Where("recovery_point_id = ? AND (state = ? OR is_active = ?)", request.RecoveryPointID, SearchGenerationBuilding, true).
			Count(&activeGenerations).Error; err != nil {
			return fmt.Errorf("prove Search generations revoked: %w", err)
		}
		if err := tx.Model(&model.BackupAssetSearchDocument{}).Where("recovery_point_id = ?", request.RecoveryPointID).Count(&documents).Error; err != nil {
			return fmt.Errorf("prove Search documents removed: %w", err)
		}
		if err := tx.Table("backup_asset_search_postings AS postings").
			Joins("JOIN backup_asset_search_generations AS generations ON generations.id = postings.search_generation_id").
			Where("generations.recovery_point_id = ?", request.RecoveryPointID).Count(&postings).Error; err != nil {
			return fmt.Errorf("prove Search postings removed: %w", err)
		}
		if err := tx.Table("backup_asset_search_document_fields AS fields").
			Joins("JOIN backup_asset_search_generations AS generations ON generations.id = fields.search_generation_id").
			Where("generations.recovery_point_id = ?", request.RecoveryPointID).Count(&fields).Error; err != nil {
			return fmt.Errorf("prove Search fields removed: %w", err)
		}
		if err := tx.Model(&model.RecoveryPointLease{}).
			Where("recovery_point_id = ? AND holder_type = ? AND status = ?", request.RecoveryPointID, backupasset.LeaseHolderSearchIndex, backupasset.LeaseActive).
			Count(&liveLeases).Error; err != nil {
			return fmt.Errorf("prove Search leases released: %w", err)
		}
		if activeGenerations != 0 || documents != 0 || postings != 0 || fields != 0 || liveLeases != 0 {
			return fmt.Errorf("%w: Search source revocation is incomplete", backupasset.ErrConflict)
		}
		return nil
	})
}

// maxProjectionDeleteBatchSize bounds one Search projection delete statement.
// Every statement filters by document_id IN (?), so this is also the bind
// parameter count and stays far below SQLite's variable limit.
const maxProjectionDeleteBatchSize = 1000

// DeleteGenerationProjectionBatchTx deletes one bounded batch of the Search
// payload owned by a single Search generation and reports how many documents it
// removed. Recovery-point cleanup and Catalog generation GC share this exact
// path so neither can rely on a single CASCADE over millions of posting rows:
// fields and postings are removed explicitly for the selected document_ids
// before their documents. A full batch means more payload may remain; an empty
// batch means the generation's projection is gone.
func DeleteGenerationProjectionBatchTx(
	ctx context.Context,
	tx *gorm.DB,
	generationID string,
	budget int,
) (int, bool, error) {
	if tx == nil || generationID == "" || budget <= 0 || budget > maxProjectionDeleteBatchSize {
		return 0, false, fmt.Errorf("%w: invalid Search projection delete request", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return 0, true, err
	}
	var documentIDs []string
	if err := tx.WithContext(ctx).Model(&model.BackupAssetSearchDocument{}).
		Where("search_generation_id = ?", generationID).
		Order("document_id ASC").Limit(budget).Pluck("document_id", &documentIDs).Error; err != nil {
		return 0, true, fmt.Errorf("load Search projection cleanup batch: %w", err)
	}
	if len(documentIDs) == 0 {
		return 0, false, nil
	}
	if err := ctx.Err(); err != nil {
		return 0, true, err
	}
	if err := tx.WithContext(ctx).
		Where("search_generation_id = ? AND document_id IN ?", generationID, documentIDs).
		Delete(&model.BackupAssetSearchDocumentField{}).Error; err != nil {
		return 0, true, fmt.Errorf("delete Search projection field batch: %w", err)
	}
	if err := tx.WithContext(ctx).
		Where("search_generation_id = ? AND document_id IN ?", generationID, documentIDs).
		Delete(&model.BackupAssetSearchPosting{}).Error; err != nil {
		return 0, true, fmt.Errorf("delete Search projection posting batch: %w", err)
	}
	result := tx.WithContext(ctx).
		Where("search_generation_id = ? AND document_id IN ?", generationID, documentIDs).
		Delete(&model.BackupAssetSearchDocument{})
	if result.Error != nil {
		return 0, true, fmt.Errorf("delete Search projection document batch: %w", result.Error)
	}
	if result.RowsAffected != int64(len(documentIDs)) {
		return 0, true, fmt.Errorf("%w: Search projection cleanup evidence changed", backupasset.ErrConflict)
	}
	return len(documentIDs), len(documentIDs) == budget, nil
}

func (owner *SourceLifecycle) releaseSearchLeasesTx(ctx context.Context, tx *gorm.DB, pointID string, now time.Time) error {
	result := tx.WithContext(ctx).Model(&model.RecoveryPointLease{}).
		Where("recovery_point_id = ? AND holder_type = ? AND owner_id = ? AND status = ?", pointID, backupasset.LeaseHolderSearchIndex, searchBuildOwnerPrefix+pointID, backupasset.LeaseActive).
		Updates(map[string]any{"status": backupasset.LeaseReleased, "released_at": now, "updated_at": now})
	if result.Error != nil {
		return fmt.Errorf("release Search source lease: %w", result.Error)
	}
	return nil
}
