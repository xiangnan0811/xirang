package search

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const searchBuildOwnerPrefix = "search:"

const maxSearchBuildTeardown = 30 * time.Second

type SearchLease interface {
	Acquire(context.Context, backupasset.AcquireLeaseRequest) (backupasset.Lease, error)
	Release(context.Context, backupasset.LeaseFence) error
	ReleaseTx(context.Context, *gorm.DB, backupasset.LeaseFence) error
	ValidateFenceTx(context.Context, *gorm.DB, backupasset.LeaseFence) error
}

type SearchKeySource interface {
	Active(context.Context, backupasset.KeyDomain) (backupasset.DomainKeyMaterial, error)
}

type IndexerConfig struct {
	BatchSize    int
	BuildTimeout time.Duration
	MaxDocuments int64
}

type IndexerDependencies struct {
	DB     *gorm.DB
	Lease  SearchLease
	Keys   SearchKeySource
	Now    func() time.Time
	Config IndexerConfig
}

type Indexer struct {
	db     *gorm.DB
	lease  SearchLease
	keys   SearchKeySource
	now    func() time.Time
	config IndexerConfig

	attemptsMu sync.Mutex
	attempts   map[string]activeSearchBuild
}

type activeSearchBuild struct {
	fence  backupasset.LeaseFence
	cancel context.CancelFunc
	done   chan struct{}
}

type BuildRequest struct {
	RepositoryID    string
	RecoveryPointID string
	CorrelationID   string
}

type BuildCandidate struct {
	RepositoryID        string
	RecoveryPointID     string
	CatalogGenerationID string
}

type frozenProjection struct {
	point   model.RecoveryPoint
	catalog model.CatalogGeneration
	key     backupasset.DomainKeyMaterial
}

func NewIndexer(dependencies IndexerDependencies) (*Indexer, error) {
	if dependencies.DB == nil || dependencies.Lease == nil || dependencies.Keys == nil ||
		dependencies.Config.BatchSize <= 0 || dependencies.Config.BatchSize > 100000 ||
		dependencies.Config.BuildTimeout <= 0 || dependencies.Config.MaxDocuments <= 0 {
		return nil, fmt.Errorf("%w: invalid Search indexer dependencies", backupasset.ErrInvalidState)
	}
	if dependencies.Now == nil {
		dependencies.Now = func() time.Time { return time.Now().UTC() }
	}
	return &Indexer{
		db: dependencies.DB, lease: dependencies.Lease, keys: dependencies.Keys,
		now: dependencies.Now, config: dependencies.Config,
		attempts: make(map[string]activeSearchBuild),
	}, nil
}

func (indexer *Indexer) Build(ctx context.Context, request BuildRequest) (result model.BackupAssetSearchGeneration, buildErr error) {
	if indexer == nil || indexer.db == nil || backupasset.ValidateOpaqueID(request.RecoveryPointID) != nil ||
		(request.RepositoryID != "" && backupasset.ValidateOpaqueID(request.RepositoryID) != nil) ||
		len(request.CorrelationID) > 64 || strings.ContainsAny(request.CorrelationID, "\r\n\x00") {
		return result, fmt.Errorf("%w: invalid Search build request", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	frozen, err := indexer.loadFrozenProjection(ctx, request)
	if err != nil {
		return result, err
	}
	lease, err := indexer.lease.Acquire(ctx, backupasset.AcquireLeaseRequest{
		RecoveryPointID: frozen.point.ID, HolderType: backupasset.LeaseHolderSearchIndex,
		OwnerID: searchBuildOwnerPrefix + frozen.point.ID,
	})
	if err != nil {
		return result, err
	}
	registered := false
	leaseReleased := false
	generationSettled := false
	var buildCancel context.CancelFunc
	defer func() {
		if buildCancel != nil {
			buildCancel()
		}
		teardownCtx, cancelTeardown := indexer.newBuildTeardownContext(ctx)
		defer cancelTeardown()
		if registered && result.ID != "" && !generationSettled {
			if err := indexer.markFailed(teardownCtx, result.ID, classifySearchBuildError(buildErr)); err != nil {
				buildErr = errors.Join(buildErr, fmt.Errorf("%w: Search failure evidence unavailable", backupasset.ErrInvalidState))
			}
		}
		if !leaseReleased {
			if err := indexer.lease.Release(teardownCtx, lease.Fence); err != nil {
				buildErr = errors.Join(buildErr, fmt.Errorf("%w: Search lease release failed", backupasset.ErrInvalidState))
			}
		}
		if registered {
			indexer.unregisterActiveBuild(request.RecoveryPointID, lease.Fence)
		}
	}()
	deadline := indexer.utcNow().Add(indexer.config.BuildTimeout)
	if !lease.AbsoluteDeadline.IsZero() && lease.AbsoluteDeadline.Before(deadline) {
		deadline = lease.AbsoluteDeadline
	}
	remaining := deadline.Sub(indexer.utcNow())
	if remaining <= 0 {
		return result, backupasset.ErrLeaseDeadlineExceeded
	}
	buildCtx, cancel := context.WithTimeout(ctx, remaining)
	buildCancel = cancel
	if err := indexer.registerActiveBuild(request.RecoveryPointID, lease.Fence, cancel); err != nil {
		return result, err
	}
	registered = true
	result, err = indexer.beginGeneration(buildCtx, frozen, lease.Fence, request.CorrelationID)
	if err != nil {
		return result, err
	}
	if err := indexer.projectCatalog(buildCtx, frozen, lease.Fence, &result); err != nil {
		return result, err
	}
	activated, err := indexer.activate(buildCtx, frozen, lease.Fence, result.ID)
	if err != nil {
		return result, err
	}
	result = activated
	leaseReleased = true
	generationSettled = true
	return result, nil
}

func (indexer *Indexer) registerActiveBuild(
	pointID string,
	fence backupasset.LeaseFence,
	cancel context.CancelFunc,
) error {
	if indexer == nil || cancel == nil {
		return fmt.Errorf("%w: invalid Search active build", backupasset.ErrInvalidState)
	}
	indexer.attemptsMu.Lock()
	defer indexer.attemptsMu.Unlock()
	if indexer.attempts == nil {
		indexer.attempts = make(map[string]activeSearchBuild)
	}
	if _, exists := indexer.attempts[pointID]; exists {
		return fmt.Errorf("%w: Search point build already active", backupasset.ErrLeaseHeld)
	}
	indexer.attempts[pointID] = activeSearchBuild{fence: fence, cancel: cancel, done: make(chan struct{})}
	return nil
}

func (indexer *Indexer) unregisterActiveBuild(pointID string, fence backupasset.LeaseFence) {
	if indexer == nil {
		return
	}
	indexer.attemptsMu.Lock()
	defer indexer.attemptsMu.Unlock()
	attempt, exists := indexer.attempts[pointID]
	if !exists || attempt.fence.FenceToken != fence.FenceToken {
		return
	}
	delete(indexer.attempts, pointID)
	close(attempt.done)
}

func (indexer *Indexer) cancelAndJoinActiveBuild(ctx context.Context, pointID string) error {
	if indexer == nil {
		return fmt.Errorf("%w: Search Indexer is unavailable", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	indexer.attemptsMu.Lock()
	attempt, exists := indexer.attempts[pointID]
	indexer.attemptsMu.Unlock()
	if !exists {
		return nil
	}
	attempt.cancel()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-attempt.done:
		return nil
	}
}

func (indexer *Indexer) newBuildTeardownContext(parent context.Context) (context.Context, context.CancelFunc) {
	timeout := indexer.config.BuildTimeout
	if timeout > maxSearchBuildTeardown {
		timeout = maxSearchBuildTeardown
	}
	return context.WithTimeout(context.WithoutCancel(parent), timeout)
}

func (indexer *Indexer) activeBuildExists(pointID string) bool {
	if indexer == nil {
		return false
	}
	indexer.attemptsMu.Lock()
	defer indexer.attemptsMu.Unlock()
	_, exists := indexer.attempts[pointID]
	return exists
}

func (indexer *Indexer) ListCandidates(ctx context.Context, limit int) ([]BuildCandidate, error) {
	if indexer == nil || indexer.db == nil || limit <= 0 || limit > 100000 {
		return nil, fmt.Errorf("%w: invalid Search candidate limit", backupasset.ErrInvalidState)
	}
	key, err := indexer.activeSearchKey(ctx)
	if err != nil {
		return nil, err
	}
	var candidates []BuildCandidate
	err = indexer.db.WithContext(ctx).Table("catalog_generations AS catalogs").
		Select("points.repository_id, points.id AS recovery_point_id, catalogs.id AS catalog_generation_id").
		Joins("JOIN recovery_points AS points ON points.id = catalogs.recovery_point_id").
		Where("catalogs.is_active = ? AND catalogs.state = ?", true, "complete").
		Where(`NOT EXISTS (
			SELECT 1 FROM backup_asset_search_generations AS searches
			WHERE searches.recovery_point_id = points.id AND searches.is_active = ? AND searches.state = ?
			  AND searches.catalog_generation_id = catalogs.id
			  AND searches.source_fingerprint = points.source_fingerprint
			  AND searches.normalizer_version = ? AND searches.search_key_version = ?
		)`, true, SearchGenerationComplete, NormalizerVersion, key.Version).
		Order("points.repository_id ASC, points.id ASC").Limit(limit).Scan(&candidates).Error
	if err != nil {
		return nil, fmt.Errorf("list Search build candidates: %w", err)
	}
	return candidates, nil
}

func (indexer *Indexer) ReconcileAbandoned(ctx context.Context, cutoff time.Time, limit int) (int64, error) {
	if indexer == nil || indexer.db == nil || cutoff.IsZero() || limit <= 0 || limit > 100000 {
		return 0, fmt.Errorf("%w: invalid Search reconciliation request", backupasset.ErrInvalidState)
	}
	var ids []string
	if err := indexer.db.WithContext(ctx).Model(&model.BackupAssetSearchGeneration{}).
		Where("state = ? AND updated_at < ?", SearchGenerationBuilding, cutoff.UTC()).
		Order("updated_at ASC, id ASC").Limit(limit).Pluck("id", &ids).Error; err != nil {
		return 0, fmt.Errorf("list abandoned Search generations: %w", err)
	}
	if len(ids) == 0 {
		return 0, nil
	}
	now := indexer.utcNow()
	result := indexer.db.WithContext(ctx).Model(&model.BackupAssetSearchGeneration{}).
		Where("id IN ? AND state = ?", ids, SearchGenerationBuilding).
		Updates(map[string]any{"state": SearchGenerationFailed, "is_active": false, "error_code": SearchErrorBuildAbandoned, "finished_at": now, "updated_at": now})
	if result.Error != nil {
		return 0, fmt.Errorf("fail abandoned Search generations: %w", result.Error)
	}
	return result.RowsAffected, nil
}

func (indexer *Indexer) loadFrozenProjection(ctx context.Context, request BuildRequest) (frozenProjection, error) {
	var point model.RecoveryPoint
	if err := indexer.db.WithContext(ctx).Where("id = ?", request.RecoveryPointID).Take(&point).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return frozenProjection{}, backupasset.ErrNotFound
	} else if err != nil {
		return frozenProjection{}, fmt.Errorf("load Search recovery point: %w", err)
	}
	if request.RepositoryID != "" && point.RepositoryID != request.RepositoryID {
		return frozenProjection{}, backupasset.ErrNotFound
	}
	if !eligibleSearchPoint(point) {
		return frozenProjection{}, ErrSearchSourceChanged
	}
	var catalogGeneration model.CatalogGeneration
	if err := indexer.db.WithContext(ctx).
		Where("recovery_point_id = ? AND is_active = ? AND state = ?", point.ID, true, "complete").
		Take(&catalogGeneration).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return frozenProjection{}, ErrSearchCatalogChanged
	} else if err != nil {
		return frozenProjection{}, fmt.Errorf("load active Catalog for Search: %w", err)
	}
	if catalogGeneration.SourceFingerprint != point.SourceFingerprint || !catalogCountReadyForSearch(point, catalogGeneration) {
		return frozenProjection{}, ErrSearchCatalogChanged
	}
	key, err := indexer.activeSearchKey(ctx)
	if err != nil {
		return frozenProjection{}, err
	}
	return frozenProjection{point: point, catalog: catalogGeneration, key: key}, nil
}

func (indexer *Indexer) beginGeneration(
	ctx context.Context,
	frozen frozenProjection,
	fence backupasset.LeaseFence,
	correlationID string,
) (model.BackupAssetSearchGeneration, error) {
	var row model.BackupAssetSearchGeneration
	err := indexer.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := backupasset.ValidateRecoveryPointWriteAdmissionTx(ctx, tx, frozen.point.ID); err != nil {
			return err
		}
		if err := indexer.lease.ValidateFenceTx(ctx, tx, fence); err != nil {
			return err
		}
		var maxGeneration int
		if err := tx.Model(&model.BackupAssetSearchGeneration{}).Where("recovery_point_id = ?", frozen.point.ID).
			Select("COALESCE(MAX(generation), 0)").Scan(&maxGeneration).Error; err != nil {
			return err
		}
		id, err := backupasset.NewOpaqueID()
		if err != nil {
			return err
		}
		now := indexer.utcNow()
		fenceHash := sha256.Sum256([]byte(fence.FenceToken))
		row = model.BackupAssetSearchGeneration{
			ID: id, RecoveryPointID: frozen.point.ID, CatalogGenerationID: frozen.catalog.ID,
			Generation: maxGeneration + 1, State: string(SearchGenerationBuilding),
			SourceFingerprint: frozen.point.SourceFingerprint, NormalizerVersion: NormalizerVersion,
			SearchKeyVersion: frozen.key.Version, ProjectionRevision: 1, LeaseID: fence.LeaseID,
			BuildAttemptID: fence.AttemptID, FenceTokenHash: hex.EncodeToString(fenceHash[:]),
			ExpectedDocumentCount: frozen.catalog.WrittenEntryCount, CorrelationID: correlationID,
			StartedAt: now, CreatedAt: now, UpdatedAt: now,
		}
		return tx.Create(&row).Error
	})
	if err != nil {
		return row, fmt.Errorf("begin Search generation: %w", err)
	}
	return row, nil
}

func (indexer *Indexer) projectCatalog(
	ctx context.Context,
	frozen frozenProjection,
	fence backupasset.LeaseFence,
	generation *model.BackupAssetSearchGeneration,
) error {
	lineage, err := indexerLineageIdentity(frozen.point)
	if err != nil {
		return err
	}
	lastEntryID := ""
	var written int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var entries []model.CatalogEntry
		query := indexer.db.WithContext(ctx).Where("generation_id = ?", frozen.catalog.ID)
		if lastEntryID != "" {
			query = query.Where("entry_id > ?", lastEntryID)
		}
		if err := query.Order("entry_id ASC").Limit(indexer.config.BatchSize).Find(&entries).Error; err != nil {
			return fmt.Errorf("read Catalog rows for Search: %w", err)
		}
		if len(entries) == 0 {
			break
		}
		if written+int64(len(entries)) > indexer.config.MaxDocuments {
			return ErrResourceLimit
		}
		if err := indexer.persistProjectionBatch(ctx, frozen, fence, generation.ID, lineage, entries); err != nil {
			return err
		}
		written += int64(len(entries))
		lastEntryID = entries[len(entries)-1].EntryID
		if err := indexer.db.WithContext(ctx).Model(&model.BackupAssetSearchGeneration{}).
			Where("id = ? AND state = ?", generation.ID, SearchGenerationBuilding).
			Updates(map[string]any{"written_document_count": written, "updated_at": indexer.utcNow()}).Error; err != nil {
			return fmt.Errorf("advance Search generation count: %w", err)
		}
	}
	generation.WrittenDocumentCount = written
	return nil
}

func (indexer *Indexer) persistProjectionBatch(
	ctx context.Context,
	frozen frozenProjection,
	fence backupasset.LeaseFence,
	searchGenerationID, lineage string,
	entries []model.CatalogEntry,
) error {
	return indexer.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := backupasset.ValidateRecoveryPointWriteAdmissionTx(ctx, tx, frozen.point.ID); err != nil {
			return err
		}
		if err := indexer.lease.ValidateFenceTx(ctx, tx, fence); err != nil {
			return err
		}
		for _, entry := range entries {
			document, postings, fields, err := projectCatalogEntry(frozen, searchGenerationID, lineage, entry, indexer.utcNow())
			if err != nil {
				return err
			}
			if err := tx.Create(&document).Error; err != nil {
				return fmt.Errorf("create Search document: %w", err)
			}
			if len(postings) > 0 {
				if err := tx.Create(&postings).Error; err != nil {
					return fmt.Errorf("create Search postings: %w", err)
				}
			}
			if err := tx.Create(&fields).Error; err != nil {
				return fmt.Errorf("create Search field coverage: %w", err)
			}
		}
		return nil
	})
}

func projectCatalogEntry(
	frozen frozenProjection,
	searchGenerationID, lineage string,
	entry model.CatalogEntry,
	now time.Time,
) (model.BackupAssetSearchDocument, []model.BackupAssetSearchPosting, []model.BackupAssetSearchDocumentField, error) {
	if entry.GenerationID != frozen.catalog.ID || entry.RecoveryPointID != frozen.point.ID ||
		backupasset.ValidateAssetRef(backupasset.AssetRef{RecoveryPointID: entry.RecoveryPointID, EntryID: entry.EntryID}) != nil {
		return model.BackupAssetSearchDocument{}, nil, nil, ErrSearchCatalogChanged
	}
	sensitivity, err := mapCatalogSensitivity(entry.SecurityState)
	if err != nil {
		return model.BackupAssetSearchDocument{}, nil, nil, err
	}
	pathValue, err := NormalizeFieldV1(SearchFieldPath, entry.NormalizedPath, DefaultNormalizerLimits())
	if err != nil {
		return model.BackupAssetSearchDocument{}, nil, nil, ErrSearchCatalogChanged
	}
	nameValue, err := NormalizeFieldV1(SearchFieldName, entry.Name, DefaultNormalizerLimits())
	if err != nil {
		return model.BackupAssetSearchDocument{}, nil, nil, ErrSearchCatalogChanged
	}
	pathSort, err := PortableSortKey(pathValue.Canonical)
	if err != nil {
		return model.BackupAssetSearchDocument{}, nil, nil, err
	}
	nameSort, err := PortableSortKey(nameValue.Canonical)
	if err != nil {
		return model.BackupAssetSearchDocument{}, nil, nil, err
	}
	lineageToken, err := LineageToken(frozen.key.Key, frozen.key.Version, lineage)
	if err != nil {
		return model.BackupAssetSearchDocument{}, nil, nil, err
	}
	pathGroup, err := PathGroupToken(frozen.key.Key, frozen.key.Version, lineage, entry.NormalizedPath)
	if err != nil {
		return model.BackupAssetSearchDocument{}, nil, nil, err
	}
	document := model.BackupAssetSearchDocument{
		SearchGenerationID: searchGenerationID, DocumentID: entry.EntryID, RecoveryPointID: entry.RecoveryPointID,
		CatalogGenerationID: entry.GenerationID, EntryID: entry.EntryID, Sensitivity: string(sensitivity),
		ClassificationRevision: 1, MetadataRevision: 1, EntryType: entry.EntryType, ModifiedAt: utcPointer(entry.ModifiedAt),
		LineageToken: lineageToken, PathGroupToken: pathGroup, PathSortKey: pathSort, NameSortKey: nameSort,
		CreatedAt: now, UpdatedAt: now,
	}
	postings := make([]model.BackupAssetSearchPosting, 0, len(pathValue.Tokens)+len(nameValue.Tokens)+8)
	appendTokens := func(field SearchField, tokens []NormalizedToken) error {
		for _, token := range tokens {
			digest, err := TokenHMAC(frozen.key.Key, frozen.key.Version, NormalizerVersion, field, token.Kind, token.Value)
			if err != nil {
				return err
			}
			postings = append(postings, model.BackupAssetSearchPosting{
				SearchGenerationID: searchGenerationID, DocumentID: entry.EntryID, Field: string(field),
				TokenKind: string(token.Kind), KeyVersion: frozen.key.Version, TokenHMAC: digest, TermFrequency: token.Frequency,
			})
		}
		return nil
	}
	if err := appendTokens(SearchFieldName, nameValue.Tokens); err != nil {
		return model.BackupAssetSearchDocument{}, nil, nil, err
	}
	if err := appendTokens(SearchFieldPath, pathValue.Tokens); err != nil {
		return model.BackupAssetSearchDocument{}, nil, nil, err
	}
	if nameValue.Extension != "" {
		if err := appendTokens(SearchFieldExtension, []NormalizedToken{{Value: nameValue.Extension, Kind: TokenKindExact, Frequency: 1}}); err != nil {
			return model.BackupAssetSearchDocument{}, nil, nil, err
		}
	}
	if err := appendTokens(SearchFieldType, []NormalizedToken{{Value: entry.EntryType, Kind: TokenKindExact, Frequency: 1}}); err != nil {
		return model.BackupAssetSearchDocument{}, nil, nil, err
	}
	if entry.ModifiedAt != nil {
		if err := appendTokens(SearchFieldModifiedTime, NormalizeModifiedTimeV1(*entry.ModifiedAt)); err != nil {
			return model.BackupAssetSearchDocument{}, nil, nil, err
		}
	}
	fields := make([]model.BackupAssetSearchDocumentField, 0, 7)
	for _, field := range []SearchField{SearchFieldName, SearchFieldPath, SearchFieldExtension, SearchFieldType, SearchFieldModifiedTime, SearchFieldContent, SearchFieldOCR} {
		state := FieldCoverageComplete
		if field == SearchFieldContent || field == SearchFieldOCR {
			state = FieldCoverageUnavailable
		}
		fields = append(fields, model.BackupAssetSearchDocumentField{
			SearchGenerationID: searchGenerationID, DocumentID: entry.EntryID, Field: string(field), State: string(state),
			CoverageRevision: 1, ClassificationRevision: 1, PipelineRevision: 1, IndexRevision: 1,
			SourceFingerprint: frozen.point.SourceFingerprint, UpdatedAt: now,
		})
	}
	return document, postings, fields, nil
}

func (indexer *Indexer) activate(
	ctx context.Context,
	frozen frozenProjection,
	fence backupasset.LeaseFence,
	generationID string,
) (model.BackupAssetSearchGeneration, error) {
	var activated model.BackupAssetSearchGeneration
	err := indexer.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := backupasset.ValidateRecoveryPointWriteAdmissionTx(ctx, tx, frozen.point.ID); err != nil {
			return err
		}
		if err := indexer.lease.ValidateFenceTx(ctx, tx, fence); err != nil {
			return err
		}
		var point model.RecoveryPoint
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", frozen.point.ID).Take(&point).Error; err != nil {
			return ErrSearchSourceChanged
		}
		if point.SourceFingerprint != frozen.point.SourceFingerprint || point.Semantics != frozen.point.Semantics ||
			point.State != frozen.point.State || !eligibleSearchPoint(point) {
			return ErrSearchSourceChanged
		}
		var catalogGeneration model.CatalogGeneration
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", frozen.catalog.ID).Take(&catalogGeneration).Error; err != nil ||
			catalogGeneration.RecoveryPointID != frozen.point.ID || !catalogGeneration.IsActive || catalogGeneration.State != "complete" ||
			catalogGeneration.SourceFingerprint != frozen.point.SourceFingerprint || catalogGeneration.WrittenEntryCount != frozen.catalog.WrittenEntryCount ||
			catalogGeneration.ExpectedEntryCount != frozen.catalog.ExpectedEntryCount ||
			(catalogGeneration.ManifestID == nil) != (frozen.catalog.ManifestID == nil) ||
			(catalogGeneration.ManifestID != nil && *catalogGeneration.ManifestID != *frozen.catalog.ManifestID) ||
			!catalogCountReadyForSearch(point, catalogGeneration) {
			return ErrSearchCatalogChanged
		}
		var keyRow model.WrappedDomainKey
		if err := tx.Where("domain = ? AND state = ?", backupasset.KeyDomainSearchToken, backupasset.DomainKeyActive).Take(&keyRow).Error; err != nil || keyRow.Version != frozen.key.Version {
			return ErrSearchKeyUnavailable
		}
		var generation model.BackupAssetSearchGeneration
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND state = ?", generationID, SearchGenerationBuilding).Take(&generation).Error; err != nil {
			return ErrSearchCatalogChanged
		}
		var documentCount int64
		if err := tx.Model(&model.BackupAssetSearchDocument{}).Where("search_generation_id = ?", generationID).Count(&documentCount).Error; err != nil {
			return err
		}
		if documentCount != generation.WrittenDocumentCount || generation.WrittenDocumentCount != generation.ExpectedDocumentCount {
			return fmt.Errorf("%w: Search document count", ErrSearchCatalogChanged)
		}
		now := indexer.utcNow()
		if err := tx.Model(&model.BackupAssetSearchGeneration{}).
			Where("recovery_point_id = ? AND is_active = ? AND id <> ?", frozen.point.ID, true, generationID).
			Updates(map[string]any{"state": SearchGenerationSuperseded, "is_active": false, "updated_at": now}).Error; err != nil {
			return err
		}
		result := tx.Model(&model.BackupAssetSearchGeneration{}).Where("id = ? AND state = ?", generationID, SearchGenerationBuilding).
			Updates(map[string]any{"state": SearchGenerationComplete, "is_active": true, "finished_at": now, "updated_at": now})
		if result.Error != nil || result.RowsAffected != 1 {
			return fmt.Errorf("activate Search generation: %w", result.Error)
		}
		if err := indexer.lease.ReleaseTx(ctx, tx, fence); err != nil {
			return err
		}
		return tx.Where("id = ?", generationID).Take(&activated).Error
	})
	if err != nil {
		return model.BackupAssetSearchGeneration{}, err
	}
	return activated, nil
}

func (indexer *Indexer) markFailed(ctx context.Context, generationID string, code SearchGenerationError) error {
	now := indexer.utcNow()
	result := indexer.db.WithContext(ctx).Model(&model.BackupAssetSearchGeneration{}).
		Where("id = ? AND state = ?", generationID, SearchGenerationBuilding).
		Updates(map[string]any{"state": SearchGenerationFailed, "is_active": false, "error_code": code, "finished_at": now, "updated_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("%w: Search failure evidence was not persisted", backupasset.ErrInvalidState)
	}
	return nil
}

func (indexer *Indexer) activeSearchKey(ctx context.Context) (backupasset.DomainKeyMaterial, error) {
	key, err := indexer.keys.Active(ctx, backupasset.KeyDomainSearchToken)
	if err != nil || key.Domain != backupasset.KeyDomainSearchToken || key.State != backupasset.DomainKeyActive || len(key.Key) != 32 || key.Version <= 0 {
		return backupasset.DomainKeyMaterial{}, fmt.Errorf("%w: active Search Token key", ErrSearchKeyUnavailable)
	}
	return key, nil
}

func mapCatalogSensitivity(value string) (Sensitivity, error) {
	switch value {
	case "":
		return SensitivityUnknown, nil
	case string(SensitivityNonSecret):
		return SensitivityNonSecret, nil
	case string(SensitivitySecret):
		return SensitivitySecret, nil
	case string(SensitivityUnknown):
		return SensitivityUnknown, nil
	case "sealed":
		return SensitivityUnknown, nil
	default:
		return "", ErrInvalidSecurityState
	}
}

func eligibleSearchPoint(point model.RecoveryPoint) bool {
	semantics := backupasset.PointVersionSemantics(point.Semantics)
	state := backupasset.RecoveryPointState(point.State)
	return (semantics == backupasset.PointMutableHead && state == backupasset.RecoveryPointObserved) ||
		((semantics == backupasset.PointNativeSnapshot || semantics == backupasset.PointXirangManifest || semantics == backupasset.PointImportedBaseline) &&
			(state == backupasset.RecoveryPointCommitted || state == backupasset.RecoveryPointDegraded))
}

func catalogCountReadyForSearch(point model.RecoveryPoint, generation model.CatalogGeneration) bool {
	if generation.ExpectedEntryCount < 0 || generation.WrittenEntryCount < 0 {
		return false
	}
	if backupasset.PointVersionSemantics(point.Semantics) == backupasset.PointMutableHead && generation.ManifestID == nil {
		return generation.ExpectedEntryCount == 0
	}
	return generation.WrittenEntryCount == generation.ExpectedEntryCount
}

func indexerLineageIdentity(point model.RecoveryPoint) (string, error) {
	control := scopePointControl{
		ID: point.ID, RepositoryID: point.RepositoryID, ProducingTaskID: point.ProducingTaskID,
		ProducingTaskRunID: point.ProducingTaskRunID, Semantics: point.Semantics, State: point.State,
		LineageJSON: point.LineageJSON, CapturedAt: point.CapturedAt, CommittedAt: point.CommittedAt,
		ObservedAt: point.ObservedAt, CreatedAt: point.CreatedAt,
	}
	selected, err := selectedPointFromControl(control, "admin")
	if err != nil {
		return "", err
	}
	return selected.Lineage, nil
}

func classifySearchBuildError(err error) SearchGenerationError {
	switch {
	case err == nil:
		return SearchErrorBuildFailed
	case errors.Is(err, context.DeadlineExceeded):
		return SearchErrorBuildTimeout
	case errors.Is(err, ErrResourceLimit):
		return SearchErrorBuildLimit
	case errors.Is(err, ErrInvalidSecurityState):
		return SearchErrorInvalidSecurityState
	case errors.Is(err, ErrSearchSourceChanged):
		return SearchErrorSourceChanged
	case errors.Is(err, ErrSearchCatalogChanged):
		return SearchErrorCatalogChanged
	case errors.Is(err, ErrSearchKeyUnavailable), errors.Is(err, backupasset.ErrKeyLost):
		return SearchErrorKeyUnavailable
	case errors.Is(err, backupasset.ErrLeaseFenceLost), errors.Is(err, backupasset.ErrLeaseDeadlineExceeded):
		return SearchErrorFenceLost
	default:
		return SearchErrorBuildFailed
	}
}

func (indexer *Indexer) utcNow() time.Time { return indexer.now().UTC() }
