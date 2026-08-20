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
					deleted, payloadRemaining, err := owner.deleteProjectionBatchTx(ctx, tx, generation.ID, payloadBudget)
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

type searchDocumentFieldDeleteKey struct {
	DocumentID string
	Field      string
}

type searchPostingDeleteKey struct {
	DocumentID string
	Field      string
	TokenKind  string
	KeyVersion int
	TokenHMAC  string
}

func (owner *SourceLifecycle) deleteProjectionBatchTx(
	ctx context.Context,
	tx *gorm.DB,
	generationID string,
	budget int,
) (int, bool, error) {
	if budget <= 0 {
		return 0, true, fmt.Errorf("%w: invalid Search cleanup payload budget", backupasset.ErrInvalidState)
	}
	deleted := 0
	fieldKeys := make([]searchDocumentFieldDeleteKey, 0, budget)
	if err := tx.WithContext(ctx).Model(&model.BackupAssetSearchDocumentField{}).
		Select("document_id", "field").Where("search_generation_id = ?", generationID).
		Order("document_id ASC, field ASC").Limit(budget).Find(&fieldKeys).Error; err != nil {
		return 0, true, fmt.Errorf("load Search source field cleanup batch: %w", err)
	}
	if len(fieldKeys) > 0 {
		query := tx.WithContext(ctx).Where(
			"search_generation_id = ? AND document_id = ? AND field = ?",
			generationID, fieldKeys[0].DocumentID, fieldKeys[0].Field,
		)
		for _, key := range fieldKeys[1:] {
			query = query.Or(
				"search_generation_id = ? AND document_id = ? AND field = ?",
				generationID, key.DocumentID, key.Field,
			)
		}
		result := query.Delete(&model.BackupAssetSearchDocumentField{})
		if result.Error != nil {
			return 0, true, fmt.Errorf("delete Search source field batch: %w", result.Error)
		}
		if result.RowsAffected != int64(len(fieldKeys)) {
			return 0, true, fmt.Errorf("%w: Search source field cleanup evidence changed", backupasset.ErrConflict)
		}
		deleted += len(fieldKeys)
		if deleted == budget {
			return deleted, true, nil
		}
	}

	remaining := budget - deleted
	postingKeys := make([]searchPostingDeleteKey, 0, remaining)
	if err := tx.WithContext(ctx).Model(&model.BackupAssetSearchPosting{}).
		Select("document_id", "field", "token_kind", "key_version", "token_hmac").
		Where("search_generation_id = ?", generationID).
		Order("document_id ASC, field ASC, token_kind ASC, key_version ASC, token_hmac ASC").
		Limit(remaining).Find(&postingKeys).Error; err != nil {
		return 0, true, fmt.Errorf("load Search source posting cleanup batch: %w", err)
	}
	if len(postingKeys) > 0 {
		query := tx.WithContext(ctx).Where(
			"search_generation_id = ? AND document_id = ? AND field = ? AND token_kind = ? AND key_version = ? AND token_hmac = ?",
			generationID, postingKeys[0].DocumentID, postingKeys[0].Field, postingKeys[0].TokenKind,
			postingKeys[0].KeyVersion, postingKeys[0].TokenHMAC,
		)
		for _, key := range postingKeys[1:] {
			query = query.Or(
				"search_generation_id = ? AND document_id = ? AND field = ? AND token_kind = ? AND key_version = ? AND token_hmac = ?",
				generationID, key.DocumentID, key.Field, key.TokenKind, key.KeyVersion, key.TokenHMAC,
			)
		}
		result := query.Delete(&model.BackupAssetSearchPosting{})
		if result.Error != nil {
			return 0, true, fmt.Errorf("delete Search source posting batch: %w", result.Error)
		}
		if result.RowsAffected != int64(len(postingKeys)) {
			return 0, true, fmt.Errorf("%w: Search source posting cleanup evidence changed", backupasset.ErrConflict)
		}
		deleted += len(postingKeys)
		if deleted == budget {
			return deleted, true, nil
		}
	}

	remaining = budget - deleted
	var documentIDs []string
	if err := tx.WithContext(ctx).Model(&model.BackupAssetSearchDocument{}).
		Where("search_generation_id = ?", generationID).Order("document_id ASC").
		Limit(remaining).Pluck("document_id", &documentIDs).Error; err != nil {
		return 0, true, fmt.Errorf("load Search source document cleanup batch: %w", err)
	}
	if len(documentIDs) == 0 {
		return deleted, false, nil
	}
	result := tx.WithContext(ctx).
		Where("search_generation_id = ? AND document_id IN ?", generationID, documentIDs).
		Delete(&model.BackupAssetSearchDocument{})
	if result.Error != nil {
		return 0, true, fmt.Errorf("delete Search source document batch: %w", result.Error)
	}
	if result.RowsAffected != int64(len(documentIDs)) {
		return 0, true, fmt.Errorf("%w: Search source document cleanup evidence changed", backupasset.ErrConflict)
	}
	deleted += len(documentIDs)
	return deleted, deleted == budget, nil
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
