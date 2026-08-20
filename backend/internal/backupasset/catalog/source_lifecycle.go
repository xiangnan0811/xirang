package catalog

import (
	"context"
	"fmt"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SourceLifecycle owns Catalog builders and projections for one source point.
type SourceLifecycle struct {
	db        *gorm.DB
	indexer   *Indexer
	now       func() time.Time
	batchSize int
}

func NewSourceLifecycle(db *gorm.DB, indexer *Indexer, now func() time.Time, batchSize int) (*SourceLifecycle, error) {
	if db == nil || db.Config == nil || indexer == nil || indexer.db == nil || indexer.db.Config == nil || batchSize <= 0 || batchSize > 1000 {
		return nil, fmt.Errorf("%w: invalid Catalog source lifecycle dependencies", backupasset.ErrInvalidState)
	}
	ownerDB, ownerDBErr := db.DB()
	indexerDB, indexerDBErr := indexer.db.DB()
	if ownerDBErr != nil || indexerDBErr != nil || ownerDB == nil || ownerDB != indexerDB {
		return nil, fmt.Errorf("%w: invalid Catalog source lifecycle dependencies", backupasset.ErrInvalidState)
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &SourceLifecycle{db: db, indexer: indexer, now: now, batchSize: batchSize}, nil
}

func (owner *SourceLifecycle) RetireRecoveryPoint(ctx context.Context, request backupasset.SourceLifecycleRequest) error {
	if owner == nil || owner.db == nil {
		return fmt.Errorf("%w: Catalog source lifecycle is unavailable", backupasset.ErrInvalidState)
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
		return fmt.Errorf("cancel and join Catalog source builder: %w", err)
	}
	for {
		settled := false
		err := owner.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := backupasset.ValidateSourceLifecycleAttemptTx(ctx, tx, request); err != nil {
				return err
			}
			var generations []model.CatalogGeneration
			query := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("recovery_point_id = ?", request.RecoveryPointID)
			if request.Stage == backupasset.SourceLifecyclePrepare {
				query = query.Where("state = ?", GenerationBuilding)
			} else {
				query = query.Where("state = ? OR is_active = ?", GenerationBuilding, true)
			}
			if err := query.Order("id ASC").Limit(owner.batchSize).Find(&generations).Error; err != nil {
				return fmt.Errorf("load Catalog source generations: %w", err)
			}
			now := owner.now().UTC()
			if len(generations) == 0 {
				if err := owner.releaseCatalogLeasesTx(ctx, tx, request.RecoveryPointID, now); err != nil {
					return err
				}
				settled = true
				return nil
			}
			for _, generation := range generations {
				if err := ctx.Err(); err != nil {
					return err
				}
				updates := map[string]any{"is_active": false, "updated_at": now}
				if generation.State == string(GenerationBuilding) {
					updates["state"] = GenerationFailed
					updates["error_code"] = GenerationErrorBuildAbandoned
					updates["finished_at"] = now
				} else if request.Stage == backupasset.SourceLifecycleCleanup && generation.IsActive {
					updates["state"] = GenerationSuperseded
				}
				if err := tx.WithContext(ctx).Model(&model.CatalogGeneration{}).Where("id = ? AND recovery_point_id = ?", generation.ID, request.RecoveryPointID).Updates(updates).Error; err != nil {
					return fmt.Errorf("retire Catalog source generation: %w", err)
				}
			}
			return owner.releaseCatalogLeasesTx(ctx, tx, request.RecoveryPointID, now)
		})
		if err != nil {
			return err
		}
		if settled {
			break
		}
	}
	if owner.activeBuildExists(request.RecoveryPointID) {
		return fmt.Errorf("%w: Catalog source builder has not joined", backupasset.ErrConflict)
	}
	return nil
}

func (owner *SourceLifecycle) releaseCatalogLeasesTx(ctx context.Context, tx *gorm.DB, pointID string, now time.Time) error {
	result := tx.WithContext(ctx).Model(&model.RecoveryPointLease{}).
		Where("recovery_point_id = ? AND holder_type = ? AND owner_id = ? AND status = ?", pointID, backupasset.LeaseHolderCatalogBuild, catalogBuildOwnerPrefix+pointID, backupasset.LeaseActive).
		Updates(map[string]any{"status": backupasset.LeaseReleased, "released_at": now, "updated_at": now})
	if result.Error != nil {
		return fmt.Errorf("release Catalog source lease: %w", result.Error)
	}
	return nil
}

func (owner *SourceLifecycle) activeBuildExists(pointID string) bool {
	if owner.indexer == nil {
		return false
	}
	owner.indexer.attemptsMu.Lock()
	defer owner.indexer.attemptsMu.Unlock()
	_, ok := owner.indexer.attempts[pointID]
	return ok
}
