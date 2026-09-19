package runtime

import (
	"context"
	"fmt"
	"strings"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/catalog"
	"xirang/backend/internal/backupasset/search"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

// Bounded Catalog generation reclamation. Retention here is a product
// invariant, not a tunable: the first version uses fixed constants so the
// protection set cannot drift with configuration.
const (
	// catalogGCMaxGenerationsPerScan deletes at most one generation per scan.
	// A single generation can own millions of batched rows, so reclaiming more
	// than one per scan would hold the database busier than the next scan can
	// wait for.
	catalogGCMaxGenerationsPerScan = 1
	// catalogGCRecentFailureEvidence keeps the most recent failed/partial
	// attempts per point, because retry backoff reads them as durable evidence.
	catalogGCRecentFailureEvidence = 2
	// catalogGCCandidateScan bounds how many generations one scan inspects while
	// searching for a reclaimable one.
	catalogGCCandidateScan = 50
	// catalogGCPayloadDocumentBatch bounds one Search payload delete to a single
	// document batch; every statement filters by document_id IN (?).
	catalogGCPayloadDocumentBatch = 1000
	// catalogGCPayloadBatchesPerScan bounds Search payload batches per scan so a
	// large generation is reclaimed over several scans, never in one long lock.
	catalogGCPayloadBatchesPerScan = 8
	// catalogGCEntryBatch bounds one catalog_entries delete by entry_id IN (?).
	catalogGCEntryBatch = 1000
	// catalogGCEntryBatchesPerScan bounds catalog_entries batches per scan.
	catalogGCEntryBatchesPerScan = 8
)

// CatalogGenerationCollector reclaims non-protected Catalog generations. It is a
// worker dependency so the Catalog worker owns when reclamation runs.
type CatalogGenerationCollector interface {
	Collect(context.Context) (CatalogGCResult, error)
}

// CatalogGCResult is one scan's reclamation outcome. It carries no recovery
// point, path, or locator identity.
type CatalogGCResult struct {
	DeletedGenerations int
	SkippedRestricted  int
}

// CatalogGenerationGC reclaims Catalog generations and the Search projections
// they own, in bounded batches. It deliberately lives outside
// backupasset/retention: the recovery-point purge path owns its own
// lease/admission protocol, while this collector runs from the Catalog worker so
// it still reclaims space while the backup-asset feature is disabled.
type CatalogGenerationGC struct {
	db  *gorm.DB
	now func() time.Time
}

func NewCatalogGenerationGC(db *gorm.DB, now func() time.Time) (*CatalogGenerationGC, error) {
	if db == nil {
		return nil, fmt.Errorf("%w: Catalog generation GC database unavailable", backupasset.ErrInvalidState)
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &CatalogGenerationGC{db: db, now: now}, nil
}

type catalogGCCandidate struct {
	ID              string
	RecoveryPointID string
}

// Collect reclaims at most one non-protected generation per scan, in the order
// Search payload, Search generation rows, catalog entries, then the generation
// row. A generation whose payload exceeds the per-scan budget keeps its row and
// is resumed by the next scan.
func (gc *CatalogGenerationGC) Collect(ctx context.Context) (CatalogGCResult, error) {
	var result CatalogGCResult
	if gc == nil || gc.db == nil {
		return result, fmt.Errorf("%w: Catalog generation GC unavailable", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	candidates, err := gc.reclaimableCandidates(ctx, true)
	if err != nil {
		return result, err
	}
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		// The query already excluded referenced generations; re-check under the
		// scan so a reference created in between skips this one instead of
		// tripping a foreign-key error.
		restricted, err := gc.restrictedReferences(ctx, candidate)
		if err != nil {
			return result, err
		}
		if restricted {
			result.SkippedRestricted++
			continue
		}
		deleted, err := gc.reclaimGeneration(ctx, candidate)
		if err != nil {
			return result, err
		}
		if deleted == 0 {
			// The per-scan budget ran out; the generation row survives and the
			// next scan resumes exactly here.
			return result, nil
		}
		result.DeletedGenerations += deleted
		if result.DeletedGenerations >= catalogGCMaxGenerationsPerScan {
			return result, nil
		}
	}
	// Nothing was reclaimable. Report the generations RESTRICT children still
	// pin, so an operator can see why reclamation is being held back.
	if err := gc.countRestrictedCandidates(ctx, &result); err != nil {
		return result, err
	}
	return result, nil
}

// countRestrictedCandidates records how many otherwise-reclaimable generations
// are retained only because a RESTRICT child still references them.
func (gc *CatalogGenerationGC) countRestrictedCandidates(ctx context.Context, result *CatalogGCResult) error {
	candidates, err := gc.reclaimableCandidates(ctx, false)
	if err != nil {
		return err
	}
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return err
		}
		restricted, err := gc.restrictedReferences(ctx, candidate)
		if err != nil {
			return err
		}
		if restricted {
			result.SkippedRestricted++
		}
	}
	return nil
}

// reclaimableCandidates lists generations outside the protection set, oldest
// first: not active, not building, on a point with no live build/index lease,
// not the latest generation, and not within the most recent failure evidence.
// When excludeRestricted is set it also omits generations a RESTRICT child still
// references, which is what lets reclamation always make progress.
func (gc *CatalogGenerationGC) reclaimableCandidates(ctx context.Context, excludeRestricted bool) ([]catalogGCCandidate, error) {
	now := gc.now().UTC()
	failureStates := []string{string(catalog.GenerationFailed), string(catalog.GenerationPartial)}
	query := gc.db.WithContext(ctx).Table("catalog_generations AS generations").
		Select("generations.id AS id, generations.recovery_point_id AS recovery_point_id").
		Where("generations.is_active = ?", false).
		Where("generations.state IN ?", []string{
			string(catalog.GenerationComplete), string(catalog.GenerationSuperseded),
			string(catalog.GenerationFailed), string(catalog.GenerationPartial),
		}).
		Where(`NOT EXISTS (
			SELECT 1 FROM recovery_point_leases AS leases
			WHERE leases.recovery_point_id = generations.recovery_point_id
			  AND leases.holder_type IN ?
			  AND leases.status = ? AND leases.lease_expires_at > ? AND leases.absolute_deadline > ?
		)`, []string{string(backupasset.LeaseHolderCatalogBuild), string(backupasset.LeaseHolderSearchIndex)},
			string(backupasset.LeaseActive), now, now).
		Where(`EXISTS (
			SELECT 1 FROM catalog_generations AS newer
			WHERE newer.recovery_point_id = generations.recovery_point_id
			  AND newer.generation > generations.generation
		)`).
		Where(`generations.state NOT IN ? OR (
			SELECT COUNT(*) FROM catalog_generations AS recent
			WHERE recent.recovery_point_id = generations.recovery_point_id
			  AND recent.state IN ? AND recent.generation > generations.generation
		) >= ?`, failureStates, failureStates, catalogGCRecentFailureEvidence)
	if excludeRestricted {
		query = query.Where(restrictedGuardSQL())
	}
	var candidates []catalogGCCandidate
	if err := query.
		Order("generations.updated_at ASC, generations.id ASC").
		Limit(catalogGCCandidateScan).
		Scan(&candidates).Error; err != nil {
		return nil, fmt.Errorf("list reclaimable Catalog generations: %w", err)
	}
	return candidates, nil
}

// restrictedTables are the children whose foreign key to catalog_entries is ON
// DELETE RESTRICT. Deleting a generation they reference would abort on a
// foreign-key error, so the generation is skipped instead. This is the complete
// set of tables that carry a catalog_generation_id and reference catalog_entries
// with RESTRICT; backup_asset_recovery_grants is deliberately absent because it
// has no catalog_generation_id column and cannot reference a generation.
func restrictedTables() []string {
	return []string{
		"backup_asset_delivery_grants",
		"backup_asset_processing_jobs",
		"backup_asset_derived_artifact_sets",
		"backup_asset_derived_blob_references",
		"backup_asset_recovery_plan_items",
	}
}

// restrictedGuardSQL excludes generations any RESTRICT child still references.
// It is applied to the reclamation query, not only checked afterwards, so a
// permanently referenced prefix of old generations cannot stall reclamation.
func restrictedGuardSQL() string {
	clauses := make([]string, 0, len(restrictedTables()))
	for _, table := range restrictedTables() {
		clauses = append(clauses,
			"NOT EXISTS (SELECT 1 FROM "+table+" AS restricted WHERE restricted.catalog_generation_id = generations.id)")
	}
	return strings.Join(clauses, " AND ")
}

func (gc *CatalogGenerationGC) restrictedReferences(ctx context.Context, candidate catalogGCCandidate) (bool, error) {
	for _, table := range restrictedTables() {
		var count int64
		if err := gc.db.WithContext(ctx).Table(table).
			Where("recovery_point_id = ? AND catalog_generation_id = ?", candidate.RecoveryPointID, candidate.ID).
			Count(&count).Error; err != nil {
			return false, fmt.Errorf("check restricted Catalog generation references: %w", err)
		}
		if count != 0 {
			return true, nil
		}
	}
	return false, nil
}

// reclaimGeneration clears one generation's Search payload and catalog entries
// within the per-scan budget and then deletes its row. It returns the number of
// generations deleted, which is zero when the budget ran out or the row is no
// longer reclaimable; either way the next scan re-evaluates it.
func (gc *CatalogGenerationGC) reclaimGeneration(ctx context.Context, candidate catalogGCCandidate) (int, error) {
	for batches := 0; ; batches++ {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		cleared, err := gc.reclaimOneSearchBatch(ctx, candidate.ID)
		if err != nil {
			return 0, err
		}
		if cleared {
			break
		}
		if batches+1 >= catalogGCPayloadBatchesPerScan {
			return 0, nil
		}
	}
	for batches := 0; ; batches++ {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		remaining, err := gc.deleteCatalogEntryBatch(ctx, candidate.ID)
		if err != nil {
			return 0, err
		}
		if !remaining {
			break
		}
		if batches+1 >= catalogGCEntryBatchesPerScan {
			return 0, nil
		}
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	deleted := gc.db.WithContext(ctx).Model(&model.CatalogGeneration{}).
		Where("id = ? AND recovery_point_id = ? AND is_active = ?", candidate.ID, candidate.RecoveryPointID, false).
		Delete(&model.CatalogGeneration{})
	if deleted.Error != nil {
		return 0, fmt.Errorf("delete Catalog generation: %w", deleted.Error)
	}
	return int(deleted.RowsAffected), nil
}

// reclaimOneSearchBatch deletes one bounded document batch for every Search
// generation owned by the Catalog generation, removing any Search generation
// whose payload is now empty. It reports true when no Search payload remains, so
// no Search generation row is ever deleted ahead of its postings.
func (gc *CatalogGenerationGC) reclaimOneSearchBatch(ctx context.Context, catalogGenerationID string) (bool, error) {
	var searchGenerationIDs []string
	if err := gc.db.WithContext(ctx).Model(&model.BackupAssetSearchGeneration{}).
		Where("catalog_generation_id = ?", catalogGenerationID).
		Order("id ASC").Pluck("id", &searchGenerationIDs).Error; err != nil {
		return false, fmt.Errorf("load Catalog generation Search projections: %w", err)
	}
	for _, searchGenerationID := range searchGenerationIDs {
		remaining := false
		err := gc.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var deleteErr error
			_, remaining, deleteErr = search.DeleteGenerationProjectionBatchTx(
				ctx, tx, searchGenerationID, catalogGCPayloadDocumentBatch,
			)
			if deleteErr != nil {
				return deleteErr
			}
			if remaining {
				return nil
			}
			return tx.WithContext(ctx).Model(&model.BackupAssetSearchGeneration{}).
				Where("id = ? AND catalog_generation_id = ?", searchGenerationID, catalogGenerationID).
				Delete(&model.BackupAssetSearchGeneration{}).Error
		})
		if err != nil {
			return false, err
		}
		if remaining {
			return false, nil
		}
	}
	return true, nil
}

// deleteCatalogEntryBatch removes one bounded entry batch and reports whether
// more entries remain.
func (gc *CatalogGenerationGC) deleteCatalogEntryBatch(ctx context.Context, catalogGenerationID string) (bool, error) {
	var entryIDs []string
	if err := gc.db.WithContext(ctx).Model(&model.CatalogEntry{}).
		Where("generation_id = ?", catalogGenerationID).
		Order("entry_id ASC").Limit(catalogGCEntryBatch).Pluck("entry_id", &entryIDs).Error; err != nil {
		return false, fmt.Errorf("load Catalog entry cleanup batch: %w", err)
	}
	if len(entryIDs) == 0 {
		return false, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	result := gc.db.WithContext(ctx).
		Where("generation_id = ? AND entry_id IN ?", catalogGenerationID, entryIDs).
		Delete(&model.CatalogEntry{})
	if result.Error != nil {
		return false, fmt.Errorf("delete Catalog entry batch: %w", result.Error)
	}
	if result.RowsAffected != int64(len(entryIDs)) {
		return false, fmt.Errorf("%w: Catalog entry cleanup evidence changed", backupasset.ErrConflict)
	}
	return len(entryIDs) == catalogGCEntryBatch, nil
}
