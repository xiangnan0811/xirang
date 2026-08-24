package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/catalog"
	"xirang/backend/internal/backupasset/content"
	assetexport "xirang/backend/internal/backupasset/export"
	"xirang/backend/internal/backupasset/overlay"
	"xirang/backend/internal/backupasset/processing"
	"xirang/backend/internal/backupasset/search"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var errContentDeliverySessionRevocation = errors.New("content delivery session revocation incomplete")

const (
	// exportRuntimeRecoveryShutdownTimeout matches the server graceful-shutdown budget.
	exportRuntimeRecoveryShutdownTimeout = 30 * time.Second

	// A bounded backfill page prevents a nonclaimable active prefix from consuming every queue pass.
	managedExportQueueBackfillPages = 2
)

type runtimeExportSelectionResolver struct {
	db          *gorm.DB
	ownership   *catalog.Ownership
	overlay     runtimeExportOverlay
	search      runtimeExportSearch
	queryLimits search.QueryLimits
}

type runtimeExportOverlay interface {
	UseSavedSearch(context.Context, overlay.Actor, string) (overlay.SavedSearch, error)
	ValidateSavedSearchForExportTx(context.Context, *gorm.DB, overlay.SavedSearchExportBinding) error
}

type runtimeExportSearch interface {
	Search(context.Context, search.SearchActor, search.SearchRequest) (search.SearchResponse, error)
}

func runtimeExportServiceConfig(config backupasset.ExportConfig) assetexport.ServiceConfig {
	return assetexport.ServiceConfig{
		Selection: assetexport.SelectionLimits{
			MaxItems: config.MaxItems, MaxSourcePoints: config.MaxSourcePoints, MaxLogicalBytes: config.MaxLogicalBytes,
		},
		Quota: assetexport.QuotaLimits{
			GlobalActiveJobs: int64(config.GlobalActiveJobs), UserActiveJobs: int64(config.UserActiveJobs),
			GlobalStoreBytes: config.Quota.GlobalStoreBytes, UserStoreBytes: config.Quota.UserStoreBytes,
		},
		ChunkBytes: config.ChunkBytes, MaxItemBytes: config.MaxItemBytes,
		MaxProviderBytes: config.MaxProviderBytes, MaxCiphertextBytes: config.MaxCiphertextBytes,
		MaxOpenReaders: config.MaxOpenReaders, MaxDuration: config.MaxDuration, MaxAttempts: config.MaxAttempts,
		RetryBase: config.RetryBase, RetryMaxDelay: config.RetryMaxDelay,
		LeaseTTL: config.LeaseTTL, LeaseRenewMargin: config.LeaseRenewMargin, ReadyTTL: config.ReadyTTL,
		IdempotencyTTL: config.IdempotencyTTL, IdempotencyKeyMaxBytes: config.IdempotencyKeyMaxBytes,
	}
}

func runtimeExportDeliveryConfig(config backupasset.ExportConfig) assetexport.DeliveryGatewayConfig {
	return assetexport.DeliveryGatewayConfig{
		TicketTTL: config.Ticket.TTL, MaxRequests: int64(config.Ticket.MaxRequests),
		MaxCumulativeBytes: config.Ticket.MaxCumulativeBytes, MaxInFlight: int64(config.Ticket.MaxInFlight),
	}
}

type runtimeExportSelectionRow struct {
	GenerationID                 string
	EntryID                      string
	RecoveryPointID              string
	ParentEntryID                *string
	NormalizedPath               string
	Name                         string
	EntryType                    string
	Size                         int64
	MimeType                     string
	Fingerprint                  string
	FingerprintStrength          string
	SecurityState                string
	GenerationState              string
	GenerationIsActive           bool
	GenerationSourceFingerprint  string
	PointSourceFingerprint       string
	PointCapabilityRevision      int64
	PointSemantics               string
	PointState                   string
	PointPhysicalAvailability    string
	PointRetentionUntil          *time.Time
	PointRetiredAt               *time.Time
	RepositoryStatus             string
	RepositoryProviderKind       string
	RepositoryCapabilityRevision int64
}

func (resolver *runtimeExportSelectionResolver) ResolveExplicit(
	ctx context.Context,
	actor assetexport.SelectionActor,
	refs []backupasset.AssetRef,
	limits assetexport.SelectionLimits,
) (assetexport.FrozenSelection, error) {
	if resolver == nil || resolver.db == nil || resolver.ownership == nil || actor.UserID == 0 || actor.Role != "admin" ||
		len(refs) == 0 || limits.MaxItems <= 0 || limits.MaxSourcePoints <= 0 || limits.MaxLogicalBytes <= 0 {
		return assetexport.FrozenSelection{}, assetexport.ErrInvalidSelection
	}
	ctx = nonNilExportRuntimeContext(ctx)
	pointIDs := make([]string, 0, len(refs))
	seenRefs := make(map[backupasset.AssetRef]struct{}, len(refs))
	for _, ref := range refs {
		if backupasset.ValidateAssetRef(ref) != nil {
			return assetexport.FrozenSelection{}, assetexport.ErrInvalidSelection
		}
		if _, duplicate := seenRefs[ref]; duplicate {
			continue
		}
		seenRefs[ref] = struct{}{}
		pointIDs = append(pointIDs, ref.RecoveryPointID)
	}
	authorized, err := resolver.ownership.AuthorizedPointIDs(
		ctx, catalog.AuthorizationScope{Role: actor.Role, UserID: actor.UserID}, pointIDs,
	)
	if err != nil || len(authorized) != uniqueStringCount(pointIDs) {
		return assetexport.FrozenSelection{}, assetexport.ErrInvalidSelection
	}

	orderedRefs := make([]backupasset.AssetRef, 0, len(seenRefs))
	for ref := range seenRefs {
		orderedRefs = append(orderedRefs, ref)
	}
	sort.Slice(orderedRefs, func(left, right int) bool {
		if orderedRefs[left].RecoveryPointID != orderedRefs[right].RecoveryPointID {
			return orderedRefs[left].RecoveryPointID < orderedRefs[right].RecoveryPointID
		}
		return orderedRefs[left].EntryID < orderedRefs[right].EntryID
	})

	items := make([]assetexport.FrozenItem, 0, min(len(orderedRefs), limits.MaxItems))
	seenItems := make(map[backupasset.AssetRef]struct{}, limits.MaxItems)
	for rootOrdinal, ref := range orderedRefs {
		root, err := resolver.loadSelectionRow(ctx, resolver.db, ref, "")
		if err != nil {
			return assetexport.FrozenSelection{}, err
		}
		queue := []runtimeExportSelectionRow{root}
		for len(queue) > 0 {
			row := queue[0]
			queue = queue[1:]
			rowRef := backupasset.AssetRef{RecoveryPointID: row.RecoveryPointID, EntryID: row.EntryID}
			if _, duplicate := seenItems[rowRef]; !duplicate {
				frozen, freezeErr := runtimeFrozenExportItem(row, rootOrdinal)
				if freezeErr != nil {
					return assetexport.FrozenSelection{}, freezeErr
				}
				items = append(items, frozen)
				seenItems[rowRef] = struct{}{}
				if len(items) > limits.MaxItems {
					return assetexport.FrozenSelection{}, assetexport.ErrSelectionLimit
				}
			}
			if row.EntryType != string(backupasset.CatalogEntryDirectory) {
				continue
			}
			children, childErr := resolver.loadSelectionChildren(ctx, row, limits.MaxItems-len(items)+1)
			if childErr != nil {
				return assetexport.FrozenSelection{}, childErr
			}
			queue = append(queue, children...)
		}
	}
	return assetexport.FreezeSelection(items, nil, limits)
}

func (resolver *runtimeExportSelectionResolver) ResolveSavedSearch(
	ctx context.Context,
	actor assetexport.SelectionActor,
	savedSearchID string,
	expectedVersion int64,
	limits assetexport.SelectionLimits,
) (assetexport.FrozenSelection, error) {
	version := int(expectedVersion)
	if resolver == nil || resolver.db == nil || resolver.overlay == nil || resolver.search == nil ||
		actor.UserID == 0 || actor.Role != "admin" || backupasset.ValidateOpaqueID(savedSearchID) != nil ||
		expectedVersion <= 0 || int64(version) != expectedVersion || resolver.queryLimits.MaxPageSize <= 0 ||
		limits.MaxItems <= 0 || limits.MaxSourcePoints <= 0 || limits.MaxLogicalBytes <= 0 {
		return assetexport.FrozenSelection{}, assetexport.ErrInvalidSelection
	}
	ctx = nonNilExportRuntimeContext(ctx)
	saved, err := resolver.overlay.UseSavedSearch(ctx, overlay.Actor{UserID: actor.UserID, Role: actor.Role}, savedSearchID)
	if err != nil || saved.ID != savedSearchID || saved.OwnerUserID != actor.UserID || saved.Version != version ||
		saved.State != overlay.SavedSearchActive {
		return assetexport.FrozenSelection{}, assetexport.ErrInvalidSelection
	}
	queryDigest, err := overlay.SavedSearchQueryDigest(saved.Query, resolver.queryLimits)
	if err != nil {
		return assetexport.FrozenSelection{}, assetexport.ErrInvalidSelection
	}
	request := saved.Query
	request.Cursor = ""
	request.Limit = min(resolver.queryLimits.MaxPageSize, limits.MaxItems)
	if request.Limit <= 0 {
		return assetexport.FrozenSelection{}, assetexport.ErrSelectionLimit
	}

	items := make([]assetexport.FrozenItem, 0, min(limits.MaxItems, request.Limit))
	seenRefs := make(map[backupasset.AssetRef]struct{}, limits.MaxItems)
	seenCursors := make(map[string]struct{})
	var queryGeneration string
	var generationDigest string
	var expectedTotal int64 = -1
	for {
		response, searchErr := resolver.search.Search(ctx, search.SearchActor{
			Authorization: catalog.AuthorizationScope{Role: actor.Role, UserID: actor.UserID},
		}, request)
		if searchErr != nil || !runtimeValidExportSearchPage(response, request.Limit) {
			return assetexport.FrozenSelection{}, assetexport.ErrInvalidSelection
		}
		pageDigest, indexes, digestErr := runtimeExportSearchGenerationDigest(response.Indexes)
		if digestErr != nil {
			return assetexport.FrozenSelection{}, digestErr
		}
		if queryGeneration == "" {
			queryGeneration = response.QueryGeneration
			generationDigest = pageDigest
			expectedTotal = *response.Total
		} else if response.QueryGeneration != queryGeneration || pageDigest != generationDigest || *response.Total != expectedTotal {
			return assetexport.FrozenSelection{}, assetexport.ErrArchiveSource
		}
		for _, hit := range response.Items {
			if _, duplicate := seenRefs[hit.Ref]; duplicate {
				return assetexport.FrozenSelection{}, assetexport.ErrInvalidSelection
			}
			index, exists := indexes[hit.Ref.RecoveryPointID]
			if !exists {
				return assetexport.FrozenSelection{}, assetexport.ErrArchiveSource
			}
			row, loadErr := resolver.loadSelectionRow(ctx, resolver.db, hit.Ref, index.CatalogGenerationID)
			if loadErr != nil {
				return assetexport.FrozenSelection{}, loadErr
			}
			item, freezeErr := runtimeFrozenExportItem(row, len(items))
			if freezeErr != nil {
				return assetexport.FrozenSelection{}, freezeErr
			}
			items = append(items, item)
			seenRefs[hit.Ref] = struct{}{}
			if len(items) > limits.MaxItems {
				return assetexport.FrozenSelection{}, assetexport.ErrSelectionLimit
			}
		}
		if response.NextCursor == "" {
			break
		}
		if _, duplicate := seenCursors[response.NextCursor]; duplicate || len(response.NextCursor) > 4096 {
			return assetexport.FrozenSelection{}, assetexport.ErrInvalidSelection
		}
		seenCursors[response.NextCursor] = struct{}{}
		request.Cursor = response.NextCursor
	}
	if expectedTotal <= 0 || int64(len(items)) != expectedTotal {
		return assetexport.FrozenSelection{}, assetexport.ErrInvalidSelection
	}
	return assetexport.FreezeSelection(items, &assetexport.SavedSearchCommitBindingV1{
		SavedSearchID: savedSearchID, ExpectedVersion: version,
		CanonicalQueryDigest: queryDigest, SearchGenerationDigest: generationDigest,
	}, limits)
}

func (resolver *runtimeExportSelectionResolver) RevalidateFrozenTx(
	ctx context.Context, tx *gorm.DB, actor assetexport.SelectionActor, frozen assetexport.FrozenSelection,
) error {
	if resolver == nil || tx == nil || actor.UserID == 0 || actor.Role != "admin" || len(frozen.Items) == 0 {
		return assetexport.ErrArchiveSource
	}
	if frozen.SavedSearch != nil {
		if resolver.overlay == nil {
			return assetexport.ErrArchiveSource
		}
		if err := resolver.overlay.ValidateSavedSearchForExportTx(ctx, tx, overlay.SavedSearchExportBinding{
			ID: frozen.SavedSearch.SavedSearchID, OwnerUserID: actor.UserID,
			ExpectedVersion:      frozen.SavedSearch.ExpectedVersion,
			CanonicalQueryDigest: frozen.SavedSearch.CanonicalQueryDigest,
		}); err != nil {
			return err
		}
		digest, err := runtimeExportFrozenSearchGenerationDigest(ctx, tx, frozen.Items)
		if err != nil || digest != frozen.SavedSearch.SearchGenerationDigest {
			return assetexport.ErrArchiveSource
		}
	}
	for _, item := range frozen.Items {
		if err := resolver.revalidateItemTx(ctx, tx, item); err != nil {
			return err
		}
	}
	return nil
}

func runtimeValidExportSearchPage(response search.SearchResponse, limit int) bool {
	return lowerHexExportRuntime(response.QueryGeneration, 64) && response.Coverage.Status == search.CoverageComplete &&
		response.Permissions.List && response.Total != nil && *response.Total >= 0 &&
		response.TotalRelation == search.TotalRelationExact && len(response.Items) <= limit && len(response.Indexes) > 0
}

func runtimeExportSearchGenerationDigest(
	statuses []search.SearchIndexStatus,
) (string, map[string]search.SearchIndexStatus, error) {
	ordered := append([]search.SearchIndexStatus(nil), statuses...)
	sort.Slice(ordered, func(left, right int) bool {
		return ordered[left].RecoveryPointID < ordered[right].RecoveryPointID
	})
	byPoint := make(map[string]search.SearchIndexStatus, len(ordered))
	for _, status := range ordered {
		if backupasset.ValidateOpaqueID(status.RecoveryPointID) != nil ||
			backupasset.ValidateOpaqueID(status.CatalogGenerationID) != nil ||
			backupasset.ValidateOpaqueID(status.SearchGenerationID) != nil || status.ProjectionRevision <= 0 ||
			status.Coverage != search.CoverageComplete || status.Staleness != search.StalenessFresh {
			return "", nil, assetexport.ErrArchiveSource
		}
		if _, duplicate := byPoint[status.RecoveryPointID]; duplicate {
			return "", nil, assetexport.ErrArchiveSource
		}
		byPoint[status.RecoveryPointID] = status
	}
	canonical, err := json.Marshal(ordered)
	if err != nil {
		return "", nil, assetexport.ErrArchiveSource
	}
	hash := sha256.Sum256(append([]byte("xirang.backup_asset.export.search_generations.v1\x00"), canonical...))
	return hex.EncodeToString(hash[:]), byPoint, nil
}

func runtimeExportFrozenSearchGenerationDigest(
	ctx context.Context, tx *gorm.DB, items []assetexport.FrozenItem,
) (string, error) {
	type sourceBinding struct {
		pointID           string
		catalogID         string
		sourceFingerprint string
	}
	bindings := make(map[string]sourceBinding)
	for _, item := range items {
		binding := sourceBinding{
			pointID: item.Ref.RecoveryPointID, catalogID: item.CatalogGenerationID,
			sourceFingerprint: item.SourceFingerprint,
		}
		if previous, exists := bindings[binding.pointID]; exists && previous != binding {
			return "", assetexport.ErrArchiveSource
		}
		bindings[binding.pointID] = binding
	}
	statuses := make([]search.SearchIndexStatus, 0, len(bindings))
	for _, binding := range bindings {
		var rows []model.BackupAssetSearchGeneration
		result := tx.WithContext(nonNilExportRuntimeContext(ctx)).Clauses(clause.Locking{Strength: "UPDATE"}).
			Where(`recovery_point_id = ? AND catalog_generation_id = ? AND source_fingerprint = ?
				AND state = ? AND is_active = ?`, binding.pointID, binding.catalogID, binding.sourceFingerprint,
				search.SearchGenerationComplete, true).
			Limit(2).Find(&rows)
		if result.Error != nil || len(rows) != 1 || rows[0].ProjectionRevision <= 0 || rows[0].FinishedAt == nil ||
			rows[0].ExpectedDocumentCount != rows[0].WrittenDocumentCount {
			return "", assetexport.ErrArchiveSource
		}
		statuses = append(statuses, search.SearchIndexStatus{
			RecoveryPointID: binding.pointID, CatalogGenerationID: binding.catalogID,
			SearchGenerationID: rows[0].ID, ProjectionRevision: rows[0].ProjectionRevision,
			Coverage: search.CoverageComplete, Staleness: search.StalenessFresh,
		})
	}
	digest, _, err := runtimeExportSearchGenerationDigest(statuses)
	return digest, err
}

func lowerHexExportRuntime(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func (resolver *runtimeExportSelectionResolver) RevalidateMetadataTx(
	ctx context.Context, tx *gorm.DB, item assetexport.FrozenItem,
) error {
	return resolver.revalidateItemTx(ctx, tx, item)
}

func (resolver *runtimeExportSelectionResolver) RevalidateMetadata(
	ctx context.Context, item assetexport.FrozenItem,
) error {
	if resolver == nil || resolver.db == nil {
		return assetexport.ErrArchiveSource
	}
	return resolver.db.WithContext(nonNilExportRuntimeContext(ctx)).Transaction(func(tx *gorm.DB) error {
		return resolver.revalidateItemTx(ctx, tx, item)
	})
}

func (resolver *runtimeExportSelectionResolver) revalidateItemTx(
	ctx context.Context, tx *gorm.DB, item assetexport.FrozenItem,
) error {
	if resolver == nil || tx == nil || assetexport.ValidateFrozenItem(item) != nil {
		return assetexport.ErrArchiveSource
	}
	row, err := resolver.loadSelectionRow(ctx, tx.Clauses(clause.Locking{Strength: "UPDATE"}), item.Ref, item.CatalogGenerationID)
	if err != nil {
		return assetexport.ErrArchiveSource
	}
	current, err := runtimeFrozenExportItem(row, item.SelectionRootOrdinal)
	if err != nil || !runtimeFrozenExportItemsEqual(current, item) {
		return assetexport.ErrArchiveSource
	}
	return nil
}

func (resolver *runtimeExportSelectionResolver) loadSelectionChildren(
	ctx context.Context, parent runtimeExportSelectionRow, limit int,
) ([]runtimeExportSelectionRow, error) {
	if limit <= 0 {
		return nil, assetexport.ErrSelectionLimit
	}
	var children []runtimeExportSelectionRow
	err := runtimeExportSelectionQuery(resolver.db.WithContext(ctx)).
		Where("entries.recovery_point_id = ? AND entries.generation_id = ? AND entries.parent_entry_id = ?",
			parent.RecoveryPointID, parent.GenerationID, parent.EntryID).
		Order("entries.entry_id ASC").Limit(limit).Scan(&children).Error
	if err != nil {
		return nil, fmt.Errorf("load Export directory children: %w", err)
	}
	for _, child := range children {
		if runtimeValidateExportSelectionRow(child) != nil || child.GenerationID != parent.GenerationID ||
			child.RecoveryPointID != parent.RecoveryPointID || child.ParentEntryID == nil || *child.ParentEntryID != parent.EntryID {
			return nil, assetexport.ErrArchiveSource
		}
	}
	return children, nil
}

func (resolver *runtimeExportSelectionResolver) loadSelectionRow(
	ctx context.Context, db *gorm.DB, ref backupasset.AssetRef, expectedGeneration string,
) (runtimeExportSelectionRow, error) {
	if db == nil || backupasset.ValidateAssetRef(ref) != nil {
		return runtimeExportSelectionRow{}, assetexport.ErrArchiveSource
	}
	query := runtimeExportSelectionQuery(db.WithContext(nonNilExportRuntimeContext(ctx))).
		Where("entries.recovery_point_id = ? AND entries.entry_id = ?", ref.RecoveryPointID, ref.EntryID)
	if expectedGeneration != "" {
		query = query.Where("entries.generation_id = ?", expectedGeneration)
	}
	var rows []runtimeExportSelectionRow
	if err := query.Limit(2).Scan(&rows).Error; err != nil {
		return runtimeExportSelectionRow{}, fmt.Errorf("load Export selection item: %w", err)
	}
	if len(rows) != 1 || runtimeValidateExportSelectionRow(rows[0]) != nil {
		return runtimeExportSelectionRow{}, assetexport.ErrArchiveSource
	}
	return rows[0], nil
}

func runtimeExportSelectionQuery(db *gorm.DB) *gorm.DB {
	return db.Table("catalog_entries AS entries").
		Select(`entries.generation_id, entries.entry_id, entries.recovery_point_id, entries.parent_entry_id,
			entries.normalized_path, entries.name, entries.entry_type, entries.size, entries.mime_type,
			entries.fingerprint, entries.fingerprint_strength, entries.security_state,
			generations.state AS generation_state, generations.is_active AS generation_is_active,
			generations.source_fingerprint AS generation_source_fingerprint,
			points.source_fingerprint AS point_source_fingerprint, points.capability_revision AS point_capability_revision,
			points.semantics AS point_semantics, points.state AS point_state,
			points.physical_availability AS point_physical_availability, points.retention_until AS point_retention_until,
			points.retired_at AS point_retired_at, repositories.status AS repository_status,
			repositories.provider_kind AS repository_provider_kind, repositories.capability_revision AS repository_capability_revision`).
		Joins("JOIN catalog_generations AS generations ON generations.id = entries.generation_id AND generations.recovery_point_id = entries.recovery_point_id").
		Joins("JOIN recovery_points AS points ON points.id = entries.recovery_point_id").
		Joins("JOIN backup_repositories AS repositories ON repositories.id = points.repository_id").
		Where("generations.state = ? AND generations.is_active = ?", catalog.GenerationComplete, true)
}

func runtimeValidateExportSelectionRow(row runtimeExportSelectionRow) error {
	ref := backupasset.AssetRef{RecoveryPointID: row.RecoveryPointID, EntryID: row.EntryID}
	if backupasset.ValidateAssetRef(ref) != nil || backupasset.ValidateOpaqueID(row.GenerationID) != nil ||
		row.GenerationState != string(catalog.GenerationComplete) || !row.GenerationIsActive ||
		row.GenerationSourceFingerprint == "" || row.GenerationSourceFingerprint != row.PointSourceFingerprint ||
		row.PointCapabilityRevision <= 0 || row.PointCapabilityRevision != row.RepositoryCapabilityRevision ||
		row.RepositoryStatus != string(backupasset.RepositoryOnline) || row.PointPhysicalAvailability != string(backupasset.PhysicalOnline) ||
		row.PointRetiredAt != nil || !runtimeExportPointVisible(row.PointSemantics, row.PointState) ||
		row.SecurityState != "sealed" || row.Fingerprint == "" || row.Size < 0 ||
		(row.FingerprintStrength != string(catalog.FingerprintStrong) &&
			row.FingerprintStrength != string(catalog.FingerprintWeak) &&
			row.FingerprintStrength != string(catalog.FingerprintNone)) {
		return assetexport.ErrArchiveSource
	}
	switch backupasset.ProviderKind(row.RepositoryProviderKind) {
	case backupasset.ProviderRestic, backupasset.ProviderRsync, backupasset.ProviderRclone:
	default:
		return assetexport.ErrArchiveSource
	}
	switch backupasset.CatalogEntryType(row.EntryType) {
	case backupasset.CatalogEntryFile, backupasset.CatalogEntryDirectory, backupasset.CatalogEntrySymlink,
		backupasset.CatalogEntryHardlink, backupasset.CatalogEntrySpecial, backupasset.CatalogEntryUnknown:
	default:
		return assetexport.ErrArchiveSource
	}
	if row.PointRetentionUntil != nil && (row.PointRetentionUntil.IsZero() || row.PointRetentionUntil.Location() != time.UTC) {
		return assetexport.ErrArchiveSource
	}
	_, err := runtimeExportArchiveComponents(row.NormalizedPath, row.Name)
	return err
}

func runtimeExportPointVisible(semantics, state string) bool {
	switch backupasset.PointVersionSemantics(semantics) {
	case backupasset.PointMutableHead:
		return backupasset.RecoveryPointState(state) == backupasset.RecoveryPointObserved
	case backupasset.PointNativeSnapshot, backupasset.PointXirangManifest, backupasset.PointImportedBaseline:
		value := backupasset.RecoveryPointState(state)
		return value == backupasset.RecoveryPointCommitted || value == backupasset.RecoveryPointDegraded
	default:
		return false
	}
}

func runtimeFrozenExportItem(row runtimeExportSelectionRow, rootOrdinal int) (assetexport.FrozenItem, error) {
	components, err := runtimeExportArchiveComponents(row.NormalizedPath, row.Name)
	if err != nil {
		return assetexport.FrozenItem{}, err
	}
	return assetexport.FrozenItem{
		SchemaVersion:       1,
		Ref:                 backupasset.AssetRef{RecoveryPointID: row.RecoveryPointID, EntryID: row.EntryID},
		CatalogGenerationID: row.GenerationID, SourceFingerprint: row.PointSourceFingerprint,
		EntryFingerprint: row.Fingerprint, FingerprintStrength: row.FingerprintStrength,
		ProviderCapabilityRevision: row.PointCapabilityRevision, EntryType: backupasset.CatalogEntryType(row.EntryType),
		LogicalSize: row.Size, MediaType: row.MimeType, RetentionUntil: utcRuntimeTimePointer(row.PointRetentionUntil),
		SelectionRootOrdinal: rootOrdinal, ArchiveComponents: components,
	}, nil
}

func runtimeExportArchiveComponents(normalizedPath, name string) ([]string, error) {
	if normalizedPath == "" || strings.HasPrefix(normalizedPath, "/") || strings.HasSuffix(normalizedPath, "/") ||
		strings.Contains(normalizedPath, "\\") || strings.ContainsRune(normalizedPath, '\x00') {
		return nil, assetexport.ErrArchiveSource
	}
	components := strings.Split(normalizedPath, "/")
	for _, component := range components {
		if component == "" || component == "." || component == ".." {
			return nil, assetexport.ErrArchiveSource
		}
	}
	if strings.TrimSpace(name) == "" || components[len(components)-1] != name {
		return nil, assetexport.ErrArchiveSource
	}
	return components, nil
}

func runtimeFrozenExportItemsEqual(left, right assetexport.FrozenItem) bool {
	if left.SchemaVersion != right.SchemaVersion || left.Ref != right.Ref ||
		left.CatalogGenerationID != right.CatalogGenerationID || left.SourceFingerprint != right.SourceFingerprint ||
		left.EntryFingerprint != right.EntryFingerprint || left.FingerprintStrength != right.FingerprintStrength ||
		left.ProviderCapabilityRevision != right.ProviderCapabilityRevision || left.EntryType != right.EntryType ||
		left.LogicalSize != right.LogicalSize || left.MediaType != right.MediaType ||
		left.SelectionRootOrdinal != right.SelectionRootOrdinal || !sameRuntimeContentTime(left.RetentionUntil, right.RetentionUntil) ||
		len(left.ArchiveComponents) != len(right.ArchiveComponents) {
		return false
	}
	for index := range left.ArchiveComponents {
		if left.ArchiveComponents[index] != right.ArchiveComponents[index] {
			return false
		}
	}
	return true
}

func utcRuntimeTimePointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	result := value.UTC()
	return &result
}

func uniqueStringCount(values []string) int {
	unique := make(map[string]struct{}, len(values))
	for _, value := range values {
		unique[value] = struct{}{}
	}
	return len(unique)
}

type managedExportRuntimeDependencies struct {
	DB                *gorm.DB
	Foundation        *backupasset.FoundationService
	Keyring           *backupasset.Keyring
	ValidateRoot      func(context.Context, string) error
	Build             func(context.Context, backupasset.ExportConfig, *assetexport.Store) (*managedExportGraph, error)
	Publication       *managedExportPublication
	Service           *managedExportServiceFacade
	Delivery          *managedExportDeliveryFacade
	Archive           *managedArchiveMemberFacade
	TransitionTimeout time.Duration
}

type managedExportGraph struct {
	store         *assetexport.Store
	service       *assetexport.Service
	delivery      *assetexport.DeliveryGateway
	archiveMember *processing.ArchiveMemberService
	attempts      *assetexport.AttemptCoordinator
	worker        *assetexport.PersistentWorker
	lifecycle     *assetexport.Lifecycle
	runner        *managedExportWorker
	stopAccepting func()
	drain         func(context.Context) error
	run           func(context.Context)
	shutdown      func(context.Context) error
	terminalize   func(context.Context) error
	startup       func(context.Context) error
}

type managedExportPublication struct {
	mu     sync.Mutex
	graph  *managedExportGraph
	active int
	change chan struct{}
}

func newManagedExportPublication() *managedExportPublication {
	return &managedExportPublication{change: make(chan struct{})}
}

func (publication *managedExportPublication) acquire() (*managedExportGraph, func(), bool) {
	if publication == nil {
		return nil, func() {}, false
	}
	publication.mu.Lock()
	graph := publication.graph
	if graph == nil {
		publication.mu.Unlock()
		return nil, func() {}, false
	}
	publication.active++
	publication.mu.Unlock()
	var once sync.Once
	return graph, func() {
		once.Do(func() {
			publication.mu.Lock()
			publication.active--
			if publication.active == 0 {
				publication.signalLocked()
			}
			publication.mu.Unlock()
		})
	}, true
}

func (publication *managedExportPublication) publish(graph *managedExportGraph) {
	if publication == nil {
		return
	}
	publication.mu.Lock()
	publication.graph = graph
	publication.signalLocked()
	publication.mu.Unlock()
}

func (publication *managedExportPublication) unpublish() {
	publication.publish(nil)
}

func (publication *managedExportPublication) waitIdle(ctx context.Context) error {
	if publication == nil {
		return nil
	}
	ctx = nonNilExportRuntimeContext(ctx)
	for {
		publication.mu.Lock()
		if publication.active == 0 {
			publication.mu.Unlock()
			return nil
		}
		changed := publication.change
		publication.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}

func (publication *managedExportPublication) current() *managedExportGraph {
	if publication == nil {
		return nil
	}
	publication.mu.Lock()
	defer publication.mu.Unlock()
	return publication.graph
}

func (publication *managedExportPublication) signalLocked() {
	close(publication.change)
	publication.change = make(chan struct{})
}

type managedExportServiceFacade struct {
	publication *managedExportPublication
}

func (facade *managedExportServiceFacade) Create(
	ctx context.Context,
	request assetexport.CreateRequest,
) (assetexport.CreateResult, error) {
	graph, release, ok := facade.publication.acquire()
	if !ok || graph.service == nil {
		return assetexport.CreateResult{}, assetexport.ErrUnavailable
	}
	defer release()
	result, err := graph.service.Create(ctx, request)
	if err == nil && graph.runner != nil {
		graph.runner.NotifyWork()
	}
	return result, err
}

func (facade *managedExportServiceFacade) Status(
	ctx context.Context,
	request assetexport.StatusRequest,
) (assetexport.JobStatus, error) {
	graph, release, ok := facade.publication.acquire()
	if !ok || graph.service == nil {
		return assetexport.JobStatus{}, assetexport.ErrUnavailable
	}
	defer release()
	return graph.service.Status(ctx, request)
}

func (facade *managedExportServiceFacade) Cancel(
	ctx context.Context,
	actor assetexport.SelectionActor,
	jobID string,
) (assetexport.JobStatus, error) {
	graph, release, ok := facade.publication.acquire()
	if !ok || graph.service == nil {
		return assetexport.JobStatus{}, assetexport.ErrUnavailable
	}
	defer release()
	return graph.service.Cancel(ctx, actor, jobID)
}

type managedExportDeliveryFacade struct {
	publication *managedExportPublication
}

func (facade *managedExportDeliveryFacade) IssueExport(
	ctx context.Context,
	request assetexport.ExportDeliveryIssueRequest,
) (assetexport.IssuedDeliveryTicket, error) {
	graph, release, ok := facade.publication.acquire()
	if !ok || graph.delivery == nil {
		return assetexport.IssuedDeliveryTicket{}, assetexport.ErrUnavailable
	}
	defer release()
	return graph.delivery.IssueExport(ctx, request)
}

func (facade *managedExportDeliveryFacade) IssueArchiveMember(
	ctx context.Context,
	request assetexport.ArchiveMemberDeliveryIssueRequest,
) (assetexport.IssuedDeliveryTicket, error) {
	graph, release, ok := facade.publication.acquire()
	if !ok || graph.delivery == nil {
		return assetexport.IssuedDeliveryTicket{}, assetexport.ErrUnavailable
	}
	defer release()
	return graph.delivery.IssueArchiveMember(ctx, request)
}

func (facade *managedExportDeliveryFacade) MatchesDelivery(ctx context.Context, deliveryID string) (bool, error) {
	graph, release, ok := facade.publication.acquire()
	if !ok || graph.delivery == nil {
		return false, nil
	}
	defer release()
	return graph.delivery.MatchesDelivery(ctx, deliveryID)
}

func (facade *managedExportDeliveryFacade) Serve(
	ctx context.Context,
	request content.GatewayRequest,
	writer http.ResponseWriter,
) error {
	graph, release, ok := facade.publication.acquire()
	if !ok || graph.delivery == nil {
		return content.ErrContentNotFound
	}
	defer release()
	return graph.delivery.Serve(ctx, request, writer)
}

func (facade *managedExportDeliveryFacade) RevokeSession(ctx context.Context, sessionJTI, reason string) error {
	graph, release, ok := facade.publication.acquire()
	if !ok || graph.delivery == nil {
		return nil
	}
	defer release()
	return graph.delivery.RevokeSession(ctx, sessionJTI, reason)
}

func (facade *managedExportDeliveryFacade) RevokeArchiveMember(ctx context.Context, requestID, reason string) error {
	graph, release, ok := facade.publication.acquire()
	if !ok || graph.delivery == nil {
		return assetexport.ErrUnavailable
	}
	defer release()
	return graph.delivery.RevokeArchiveMember(ctx, requestID, reason)
}

type managedArchiveMemberFacade struct {
	publication *managedExportPublication
}

func (facade *managedArchiveMemberFacade) ListIndex(
	ctx context.Context,
	request processing.ArchiveMemberIndexLookup,
) (processing.ArchiveMemberIndexView, error) {
	graph, release, ok := facade.publication.acquire()
	if !ok || graph.archiveMember == nil {
		return processing.ArchiveMemberIndexView{}, processing.ErrArchiveMemberUnavailable
	}
	defer release()
	return graph.archiveMember.ListIndex(ctx, request)
}

func (facade *managedArchiveMemberFacade) Create(
	ctx context.Context,
	request processing.ArchiveMemberCreateRequest,
) (processing.ArchiveMemberCreateResult, error) {
	graph, release, ok := facade.publication.acquire()
	if !ok || graph.archiveMember == nil {
		return processing.ArchiveMemberCreateResult{}, processing.ErrArchiveMemberUnavailable
	}
	defer release()
	return graph.archiveMember.Create(ctx, request)
}

func (facade *managedArchiveMemberFacade) Reconcile(ctx context.Context, requestID string) error {
	graph, release, ok := facade.publication.acquire()
	if !ok || graph.archiveMember == nil {
		return processing.ErrArchiveMemberUnavailable
	}
	defer release()
	return graph.archiveMember.Reconcile(ctx, requestID)
}

func (facade *managedArchiveMemberFacade) Poll(
	ctx context.Context,
	request processing.ArchiveMemberLookup,
) (processing.ArchiveMemberStatusResult, error) {
	graph, release, ok := facade.publication.acquire()
	if !ok || graph.archiveMember == nil {
		return processing.ArchiveMemberStatusResult{}, processing.ErrArchiveMemberUnavailable
	}
	defer release()
	return graph.archiveMember.Poll(ctx, request)
}

func (facade *managedArchiveMemberFacade) Cancel(ctx context.Context, request processing.ArchiveMemberLookup) error {
	graph, release, ok := facade.publication.acquire()
	if !ok || graph.archiveMember == nil {
		return processing.ErrArchiveMemberUnavailable
	}
	defer release()
	return graph.archiveMember.Cancel(ctx, request)
}

func (facade *managedArchiveMemberFacade) AuthorizeReadyDelivery(
	ctx context.Context,
	request processing.ArchiveMemberLookup,
) (content.AuthorizedAsset, error) {
	graph, release, ok := facade.publication.acquire()
	if !ok || graph.archiveMember == nil {
		return content.AuthorizedAsset{}, processing.ErrArchiveMemberUnavailable
	}
	defer release()
	return graph.archiveMember.AuthorizeReadyDelivery(ctx, request)
}

type managedExportWorkerDependencies struct {
	DB                  *gorm.DB
	Attempts            managedExportAttemptController
	Worker              managedExportExecutionBackend
	Lifecycle           managedExportLifecycleBackend
	Delivery            managedExportDeliveryBackend
	Archive             managedArchiveMemberMaintenanceBackend
	Budget              managedExportAttemptBudgetBackend
	Cadence             time.Duration
	HeartbeatInterval   time.Duration
	SourceLeaseInterval time.Duration
	BatchSize           int
	WorkerConcurrency   int
	WorkerOwner         string
}

type managedExportAttemptController interface {
	Claim(context.Context, assetexport.AttemptClaimRequest) (assetexport.AttemptClaim, error)
	Heartbeat(context.Context, assetexport.AttemptHeartbeatRequest) (assetexport.AttemptHeartbeatResult, error)
	Fail(context.Context, assetexport.AttemptFailureRequest) (assetexport.AttemptFailureResult, error)
	MaintainSourceLeases(context.Context, assetexport.SourceLeaseMaintenanceRequest) (assetexport.SourceLeaseMaintenanceResult, error)
}

type managedExportAttemptCheckpointer interface {
	Checkpoint(context.Context, assetexport.AttemptCheckpoint) error
}

type managedExportExecutionBackend interface {
	SpoolItem(context.Context, assetexport.PersistentSpoolItemRequest) (assetexport.PersistentSpoolResult, error)
	SealArchive(context.Context, assetexport.PersistentSealRequest) (assetexport.PersistentSealResult, error)
	PublishReady(context.Context, assetexport.PersistentPublishRequest) (assetexport.PersistentPublishResult, error)
	DiscardAttempt(context.Context, assetexport.PersistentDiscardAttemptRequest) error
	ReconcileJob(context.Context, assetexport.PersistentReconcileRequest) (assetexport.PersistentReconcileResult, error)
	ReconcileOrphans(context.Context) (int, error)
}

type managedExportLifecycleBackend interface {
	Reconcile(context.Context, int) (int, error)
	FailSourceExpired(context.Context, string) error
	FailUnpublishable(context.Context, string, string) error
}

type managedExportDeliveryBackend interface {
	ReconcileDeliveries(context.Context) error
	MaintainDeliveries(context.Context) error
}

type managedArchiveMemberMaintenanceBackend interface {
	ReconcilePending(context.Context, int) (int, error)
}

type managedExportAttemptBudgetBackend interface {
	ReconcileExpiredAttemptReads(context.Context, int) (int, error)
}

type managedExportWorkerCapacityReconciler interface {
	ReconcileExpiredWorkerReservations(context.Context, int) (int, error)
}

type managedExportQueueCursor struct {
	updatedAt time.Time
	id        string
}

type managedExportQueueCandidate struct {
	ID               string
	UpdatedAt        time.Time
	CreatedAt        time.Time
	UpdatedAtMissing bool `gorm:"column:queue_updated_at_missing"`
}

func (candidate managedExportQueueCandidate) cursor() managedExportQueueCursor {
	updatedAt := candidate.UpdatedAt
	if candidate.UpdatedAtMissing {
		updatedAt = candidate.CreatedAt
	}
	return managedExportQueueCursor{updatedAt: updatedAt.UTC(), id: candidate.ID}
}

type managedExportWorker struct {
	db                  *gorm.DB
	attempts            managedExportAttemptController
	worker              managedExportExecutionBackend
	lifecycle           managedExportLifecycleBackend
	delivery            managedExportDeliveryBackend
	archive             managedArchiveMemberMaintenanceBackend
	budget              managedExportAttemptBudgetBackend
	cadence             time.Duration
	heartbeat           time.Duration
	sourceLeaseInterval time.Duration
	batchSize           int
	concurrency         int
	owner               string

	accepting             atomic.Bool
	admissionMu           sync.RWMutex
	mu                    sync.Mutex
	queueSweepMu          sync.Mutex
	queueSweepCursor      managedExportQueueCursor
	queueSweepHasCursor   bool
	sourceSweepMu         sync.Mutex
	sourceMaintenanceSeen map[string]struct{}
	cancel                context.CancelFunc
	done                  chan struct{}
	wake                  chan struct{}
}

const managedExportQueueWakeInterval = 100 * time.Millisecond

var managedExportSourceProtectedStates = [...]string{
	string(assetexport.ExecutionQueued),
	string(assetexport.ExecutionRunning),
	string(assetexport.ExecutionRetryWait),
	string(assetexport.ExecutionSealing),
	string(assetexport.ExecutionReady),
}

func newManagedExportWorker(dependencies managedExportWorkerDependencies) (*managedExportWorker, error) {
	if dependencies.HeartbeatInterval == 0 {
		dependencies.HeartbeatInterval = dependencies.Cadence
	}
	if dependencies.SourceLeaseInterval == 0 {
		dependencies.SourceLeaseInterval = dependencies.HeartbeatInterval
	}
	if dependencies.DB == nil || dependencies.Attempts == nil || dependencies.Worker == nil ||
		dependencies.Lifecycle == nil || dependencies.Delivery == nil || dependencies.Budget == nil || dependencies.Cadence <= 0 ||
		dependencies.HeartbeatInterval <= 0 || dependencies.SourceLeaseInterval <= 0 || dependencies.BatchSize <= 0 ||
		dependencies.WorkerConcurrency <= 0 || dependencies.WorkerConcurrency > 256 ||
		strings.TrimSpace(dependencies.WorkerOwner) == "" || len(dependencies.WorkerOwner) > 128 {
		return nil, fmt.Errorf("%w: managed Export Worker dependencies unavailable", backupasset.ErrInvalidState)
	}
	runner := &managedExportWorker{
		db: dependencies.DB, attempts: dependencies.Attempts, worker: dependencies.Worker,
		lifecycle: dependencies.Lifecycle, delivery: dependencies.Delivery, archive: dependencies.Archive, budget: dependencies.Budget,
		cadence: dependencies.Cadence, heartbeat: dependencies.HeartbeatInterval, sourceLeaseInterval: dependencies.SourceLeaseInterval,
		batchSize: dependencies.BatchSize, concurrency: dependencies.WorkerConcurrency, owner: dependencies.WorkerOwner,
		sourceMaintenanceSeen: make(map[string]struct{}),
		wake:                  make(chan struct{}, 1),
	}
	runner.accepting.Store(true)
	runner.wake <- struct{}{}
	return runner, nil
}

func (worker *managedExportWorker) Startup(ctx context.Context) error {
	if worker == nil {
		return fmt.Errorf("%w: managed Export Worker unavailable", backupasset.ErrInvalidState)
	}
	return worker.reconcileStartup(nonNilExportRuntimeContext(ctx))
}

func (worker *managedExportWorker) NotifyWork() {
	if worker == nil || worker.wake == nil {
		return
	}
	select {
	case worker.wake <- struct{}{}:
	default:
	}
}

func (worker *managedExportWorker) StopAccepting() {
	if worker == nil {
		return
	}
	worker.accepting.Store(false)
	worker.mu.Lock()
	cancel := worker.cancel
	worker.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	worker.admissionMu.Lock()
	//nolint:staticcheck // Intentional admission-drain barrier: after acceptance is disabled and cancellation is issued, wait for in-flight claims to release their read locks.
	worker.admissionMu.Unlock()
}

func (worker *managedExportWorker) Run(ctx context.Context) {
	if worker == nil {
		return
	}
	worker.mu.Lock()
	if worker.done != nil {
		worker.mu.Unlock()
		return
	}
	runCtx, cancel := context.WithCancel(nonNilExportRuntimeContext(ctx))
	done := make(chan struct{})
	worker.cancel = cancel
	worker.done = done
	worker.mu.Unlock()
	sourceMaintenanceDone := make(chan struct{})
	go func() {
		defer close(sourceMaintenanceDone)
		ticker := time.NewTicker(worker.sourceLeaseInterval)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				_ = worker.maintainSourceLeases(runCtx)
			}
		}
	}()
	defer func() {
		cancel()
		<-sourceMaintenanceDone
		close(done)
	}()

	ticker := time.NewTicker(worker.cadence)
	defer ticker.Stop()
	queueTimer := time.NewTimer(time.Hour)
	if !queueTimer.Stop() {
		<-queueTimer.C
	}
	defer queueTimer.Stop()
	for {
		select {
		case <-runCtx.Done():
			return
		case <-ticker.C:
			_ = worker.reconcileWithoutSourceMaintenance(runCtx)
		case <-worker.wake:
			_ = worker.executeQueued(runCtx)
			worker.scheduleQueueTimer(runCtx, queueTimer)
		case <-queueTimer.C:
			_ = worker.executeQueued(runCtx)
			worker.scheduleQueueTimer(runCtx, queueTimer)
		}
	}
}

func (worker *managedExportWorker) scheduleQueueTimer(ctx context.Context, timer *time.Timer) {
	if worker == nil || timer == nil {
		return
	}
	var candidate model.BackupAssetExportJob
	result := worker.db.WithContext(ctx).Model(&model.BackupAssetExportJob{}).Select("id").
		Where("execution_state IN ?", []string{
			string(assetexport.ExecutionQueued), string(assetexport.ExecutionRetryWait),
			string(assetexport.ExecutionRunning), string(assetexport.ExecutionSealing),
		}).Limit(1).Find(&candidate)
	if result.Error != nil || result.RowsAffected == 0 {
		stopManagedExportQueueTimer(timer)
		return
	}
	resetManagedExportQueueTimer(timer, managedExportQueueWakeInterval)
}

func stopManagedExportQueueTimer(timer *time.Timer) {
	if timer == nil || timer.Stop() {
		return
	}
	select {
	case <-timer.C:
	default:
	}
}

func resetManagedExportQueueTimer(timer *time.Timer, delay time.Duration) {
	if timer == nil {
		return
	}
	stopManagedExportQueueTimer(timer)
	timer.Reset(delay)
}

func (worker *managedExportWorker) Drain(ctx context.Context) error {
	if worker == nil {
		return nil
	}
	worker.StopAccepting()
	worker.mu.Lock()
	cancel, done := worker.cancel, worker.done
	worker.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-nonNilExportRuntimeContext(ctx).Done():
			return ctx.Err()
		}
	}
	cleanupParent := nonNilExportRuntimeContext(ctx)
	cleanupBase := context.WithoutCancel(cleanupParent)
	var cleanupCtx context.Context
	var cleanupCancel context.CancelFunc
	if deadline, ok := cleanupParent.Deadline(); ok {
		cleanupCtx, cleanupCancel = context.WithDeadline(cleanupBase, deadline)
	} else {
		cleanupCtx, cleanupCancel = context.WithTimeout(cleanupBase, exportRuntimeRecoveryShutdownTimeout)
	}
	defer cleanupCancel()
	return worker.reconcileForDrain(cleanupCtx)
}

func (worker *managedExportWorker) Shutdown(ctx context.Context) error {
	return worker.Drain(ctx)
}

func (worker *managedExportWorker) reconcile(ctx context.Context) error {
	return worker.reconcileRestart(ctx, true)
}

func (worker *managedExportWorker) reconcileStartup(ctx context.Context) error {
	if err := worker.delivery.ReconcileDeliveries(ctx); err != nil {
		return err
	}
	if err := worker.reconcileWithSourceMaintenance(ctx, true, true, false); err != nil {
		return err
	}
	// Startup may finish an already sealing attempt, but queued jobs wait until
	// Run has started and its listeners are installed.
	return worker.executeQueuedStates(ctx, []string{
		string(assetexport.ExecutionRunning), string(assetexport.ExecutionRetryWait), string(assetexport.ExecutionSealing),
	})
}

func (worker *managedExportWorker) reconcileWithoutSourceMaintenance(ctx context.Context) error {
	if err := worker.delivery.MaintainDeliveries(ctx); err != nil {
		return err
	}
	return worker.reconcileWithSourceMaintenance(ctx, false, true, true)
}

func (worker *managedExportWorker) reconcileForDrain(ctx context.Context) error {
	if err := worker.fenceJoinedActiveAttempts(ctx); err != nil {
		return err
	}
	return worker.reconcileRestart(ctx, false)
}

func (worker *managedExportWorker) reconcileRestart(ctx context.Context, maintainArchiveMembers bool) error {
	if err := worker.delivery.ReconcileDeliveries(ctx); err != nil {
		return err
	}
	return worker.reconcileWithSourceMaintenance(ctx, true, maintainArchiveMembers, true)
}

func (worker *managedExportWorker) reconcileWithSourceMaintenance(
	ctx context.Context,
	maintainSources bool,
	maintainArchiveMembers bool,
	executeQueued bool,
) error {
	if _, err := worker.budget.ReconcileExpiredAttemptReads(ctx, worker.batchSize); err != nil {
		return err
	}
	if reconciler, ok := worker.attempts.(managedExportWorkerCapacityReconciler); ok {
		if _, err := reconciler.ReconcileExpiredWorkerReservations(ctx, worker.batchSize); err != nil {
			return err
		}
	}
	if maintainSources {
		if err := worker.maintainSourceLeases(ctx); err != nil {
			return err
		}
	}
	if err := worker.retireClosedAttempts(ctx); err != nil {
		return err
	}
	if err := worker.reconcileSealing(ctx); err != nil {
		return err
	}
	if _, err := worker.lifecycle.Reconcile(ctx, worker.batchSize); err != nil {
		return err
	}
	if maintainArchiveMembers && worker.archive != nil {
		if _, err := worker.archive.ReconcilePending(ctx, worker.batchSize); err != nil &&
			!errors.Is(err, processing.ErrNotDeployed) && !errors.Is(err, processing.ErrArchiveMemberUnavailable) {
			return err
		}
	}
	if executeQueued && worker.accepting.Load() {
		if err := worker.executeQueued(ctx); err != nil {
			return err
		}
	}
	_, err := worker.worker.ReconcileOrphans(ctx)
	return err
}

func (worker *managedExportWorker) fenceJoinedActiveAttempts(ctx context.Context) error {
	type activeAttempt struct {
		JobID        string
		AttemptID    string
		FenceToken   []byte
		AttemptState string
	}
	var attempts []activeAttempt
	query := worker.db.WithContext(ctx).Table("backup_asset_export_jobs AS job").
		Select("job.id AS job_id, attempt.id AS attempt_id, attempt.fence_token, attempt.state AS attempt_state").
		Joins("JOIN backup_asset_export_attempts AS attempt ON attempt.id = job.current_attempt_id AND attempt.job_id = job.id").
		Where("job.execution_state IN ? AND attempt.is_current = ? AND attempt.state IN ?",
			[]string{string(assetexport.ExecutionRunning), string(assetexport.ExecutionSealing)}, true,
			[]string{string(assetexport.AttemptActive), string(assetexport.AttemptSealing)}).
		Order("job.id ASC")
	if err := query.Find(&attempts).Error; err != nil {
		return fmt.Errorf("load joined active Export attempts: %w", err)
	}
	var result error
	for _, attempt := range attempts {
		if _, err := worker.attempts.Fail(ctx, assetexport.AttemptFailureRequest{
			JobID: attempt.JobID, AttemptID: attempt.AttemptID, FenceToken: append([]byte(nil), attempt.FenceToken...),
			Category: "worker_unavailable", Retryable: true,
		}); err != nil && !errors.Is(err, assetexport.ErrAttemptFenceLost) {
			result = errors.Join(result, err)
		}
	}
	return result
}

func (worker *managedExportWorker) maintainSourceLeases(ctx context.Context) error {
	jobs, err := worker.nextSourceMaintenanceJobs(ctx)
	if err != nil {
		return err
	}
	var result error
	for _, job := range jobs {
		if err := worker.maintainSourceLeaseJob(ctx, job); err != nil {
			result = errors.Join(result, err)
		}
	}
	return result
}

func (worker *managedExportWorker) nextSourceMaintenanceJobs(ctx context.Context) ([]model.BackupAssetExportJob, error) {
	worker.sourceSweepMu.Lock()
	defer worker.sourceSweepMu.Unlock()
	if worker.sourceMaintenanceSeen == nil {
		worker.sourceMaintenanceSeen = make(map[string]struct{})
	}
	seenIDs := make([]string, 0, len(worker.sourceMaintenanceSeen))
	for jobID := range worker.sourceMaintenanceSeen {
		seenIDs = append(seenIDs, jobID)
	}
	sort.Strings(seenIDs)
	jobs, err := worker.loadSourceMaintenanceJobs(ctx, seenIDs)
	if err != nil {
		return nil, err
	}
	if len(jobs) == 0 && len(seenIDs) > 0 {
		clear(worker.sourceMaintenanceSeen)
		jobs, err = worker.loadSourceMaintenanceJobs(ctx, nil)
		if err != nil {
			return nil, err
		}
	}
	if len(jobs) == 0 {
		return nil, nil
	}
	for _, job := range jobs {
		worker.sourceMaintenanceSeen[job.ID] = struct{}{}
	}
	return jobs, nil
}

func (worker *managedExportWorker) loadSourceMaintenanceJobs(
	ctx context.Context,
	seenIDs []string,
) ([]model.BackupAssetExportJob, error) {
	// Renew the jobs nearest to losing source protection; IDs only break equal deadlines.
	query := worker.db.WithContext(ctx).Model(&model.BackupAssetExportJob{}).
		Select("backup_asset_export_jobs.id, backup_asset_export_jobs.execution_state").
		Joins("LEFT JOIN backup_asset_export_source_leases ON backup_asset_export_source_leases.job_id = backup_asset_export_jobs.id AND backup_asset_export_source_leases.state = ?", "active").
		Joins("LEFT JOIN recovery_point_leases ON recovery_point_leases.id = backup_asset_export_source_leases.lease_id").
		Where("backup_asset_export_jobs.execution_state IN ?", managedExportSourceProtectedStates[:]).
		Group("backup_asset_export_jobs.id, backup_asset_export_jobs.execution_state").
		Order("CASE WHEN MIN(recovery_point_leases.lease_expires_at) IS NULL THEN 1 ELSE 0 END ASC").
		Order("MIN(recovery_point_leases.lease_expires_at) ASC").
		Order("backup_asset_export_jobs.id ASC")
	if len(seenIDs) > 0 {
		query = query.Where("backup_asset_export_jobs.id NOT IN ?", seenIDs)
	}
	var jobs []model.BackupAssetExportJob
	if err := query.Limit(worker.batchSize).Find(&jobs).Error; err != nil {
		return nil, fmt.Errorf("load source-protected Export jobs: %w", err)
	}
	return jobs, nil
}

func (worker *managedExportWorker) maintainSourceLease(ctx context.Context, jobID string) error {
	if backupasset.ValidateOpaqueID(jobID) != nil {
		return assetexport.ErrAttemptFenceLost
	}
	var job model.BackupAssetExportJob
	result := worker.db.WithContext(ctx).Model(&model.BackupAssetExportJob{}).Select("id", "execution_state").
		Where("id = ? AND execution_state IN ?", jobID, managedExportSourceProtectedStates[:]).Limit(1).Find(&job)
	if result.Error != nil {
		return fmt.Errorf("load source-protected Export job: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return assetexport.ErrAttemptFenceLost
	}
	return worker.maintainSourceLeaseJob(ctx, job)
}

func (worker *managedExportWorker) maintainSourceLeaseJob(ctx context.Context, job model.BackupAssetExportJob) error {
	jobID := job.ID
	readyJob := assetexport.ExecutionState(job.ExecutionState) == assetexport.ExecutionReady
	var readyIntegrity *assetexport.ReadyIntegrityToken
	if readyJob {
		verification, verificationErr := worker.worker.ReconcileJob(
			ctx, assetexport.PersistentReconcileRequest{JobID: jobID},
		)
		if errors.Is(verificationErr, assetexport.ErrReadyExpired) {
			return nil
		}
		if verificationErr != nil {
			return verificationErr
		}
		if verification.Action == assetexport.PersistentReconcileRevoked {
			return nil
		}
		if verification.ReadyIntegrity == nil {
			return assetexport.ErrUnavailable
		}
		readyIntegrity = verification.ReadyIntegrity
	}
	_, maintenanceErr := worker.attempts.MaintainSourceLeases(
		ctx, assetexport.SourceLeaseMaintenanceRequest{JobID: jobID, ReadyIntegrity: readyIntegrity},
	)
	if maintenanceErr == nil {
		return nil
	}

	var current model.BackupAssetExportJob
	loaded := worker.db.WithContext(ctx).Select("execution_state").Where("id = ?", jobID).Limit(1).Find(&current)
	if loaded.Error != nil {
		return errors.Join(maintenanceErr, fmt.Errorf("reload Export job after source maintenance: %w", loaded.Error))
	}
	if loaded.RowsAffected == 1 && assetexport.ExecutionState(current.ExecutionState) == assetexport.ExecutionCancelRequested {
		if cleanupErr := worker.lifecycle.FailSourceExpired(ctx, jobID); cleanupErr != nil {
			return errors.Join(maintenanceErr, cleanupErr)
		}
		return nil
	}

	var cleanupErr error
	switch {
	case errors.Is(maintenanceErr, assetexport.ErrReadyExpired):
		return nil
	case errors.Is(maintenanceErr, assetexport.ErrSourceDeadlineReached):
		cleanupErr = worker.lifecycle.FailSourceExpired(ctx, jobID)
	case readyJob && errors.Is(maintenanceErr, assetexport.ErrStoreObjectAbsent):
		cleanupErr = worker.lifecycle.FailUnpublishable(ctx, jobID, "artifact_missing")
	case readyJob && (errors.Is(maintenanceErr, assetexport.ErrCipherTampered) ||
		errors.Is(maintenanceErr, assetexport.ErrInvalidStore)):
		cleanupErr = worker.lifecycle.FailUnpublishable(ctx, jobID, "artifact_tampered")
	case errors.Is(maintenanceErr, assetexport.ErrUnavailable):
		cleanupErr = worker.lifecycle.FailUnpublishable(ctx, jobID, "internal_failure")
	case readyJob && errors.Is(maintenanceErr, assetexport.ErrAttemptFenceLost):
		cleanupErr = worker.lifecycle.FailSourceExpired(ctx, jobID)
	case errors.Is(maintenanceErr, assetexport.ErrExecutionDeadlineReached):
		cleanupErr = worker.lifecycle.FailUnpublishable(ctx, jobID, "deadline")
	default:
		return maintenanceErr
	}
	if cleanupErr != nil {
		return errors.Join(maintenanceErr, cleanupErr)
	}
	return nil
}

func (worker *managedExportWorker) retireClosedAttempts(ctx context.Context) error {
	var attemptIDs []string
	closedStates := []string{
		string(assetexport.AttemptFailed), string(assetexport.AttemptCanceled), string(assetexport.AttemptSuperseded),
	}
	if err := worker.db.WithContext(ctx).Model(&model.BackupAssetExportAttempt{}).
		Where("is_current = ? AND state IN ?", false, closedStates).
		Where(`staging_locator <> ? OR EXISTS (
			SELECT 1 FROM backup_asset_export_item_attempts AS item_attempt
			WHERE item_attempt.attempt_id = backup_asset_export_attempts.id AND item_attempt.job_id = backup_asset_export_attempts.job_id
			AND item_attempt.spool_locator <> ?
		) OR EXISTS (
			SELECT 1 FROM backup_asset_export_artifacts AS artifact
			WHERE artifact.attempt_id = backup_asset_export_attempts.id AND artifact.job_id = backup_asset_export_attempts.job_id
			AND artifact.state IN ? AND artifact.expires_at IS NULL
		)`, "", "", []string{"staged", "sealed"}).
		Order("updated_at ASC, id ASC").Limit(worker.batchSize).Pluck("id", &attemptIDs).Error; err != nil {
		return fmt.Errorf("load retired Export attempts: %w", err)
	}
	var result error
	for _, attemptID := range attemptIDs {
		var attempt model.BackupAssetExportAttempt
		loaded := worker.db.WithContext(ctx).Select("job_id").Where("id = ?", attemptID).Limit(1).Find(&attempt)
		if loaded.Error != nil {
			result = errors.Join(result, fmt.Errorf("load retired Export attempt owner: %w", loaded.Error))
			continue
		}
		if loaded.RowsAffected != 1 {
			result = errors.Join(result, assetexport.ErrAttemptFenceLost)
			continue
		}
		if err := worker.worker.DiscardAttempt(ctx, assetexport.PersistentDiscardAttemptRequest{
			JobID: attempt.JobID, AttemptID: attemptID,
		}); err != nil {
			result = errors.Join(result, fmt.Errorf("retire Export attempt %s: %w", attemptID, err))
		}
	}
	return result
}

func (worker *managedExportWorker) executeQueued(ctx context.Context) error {
	return worker.executeQueuedStates(ctx, nil)
}

func (worker *managedExportWorker) executeQueuedStates(ctx context.Context, states []string) error {
	ctx = nonNilExportRuntimeContext(ctx)
	worker.queueSweepMu.Lock()
	defer worker.queueSweepMu.Unlock()

	// Rows inserted or touched during this pass belong to a later cadence. This keeps
	// the keyset finite even while claimed jobs advance their state timestamps.
	cutoff := time.Now().UTC()
	seen := make(map[string]struct{}, worker.batchSize)
	semaphore := make(chan struct{}, worker.concurrency)
	running := make([]<-chan error, 0, worker.batchSize)
	claimed := 0
	var result error
	queueCanceled := false
	exhausted := false

scan:
	for page := 0; page < managedExportQueueBackfillPages && claimed < worker.batchSize; page++ {
		candidates, err := worker.loadQueuedCandidates(ctx, cutoff, states)
		if err != nil {
			result = errors.Join(result, err)
			break
		}
		if len(candidates) == 0 {
			exhausted = true
			break
		}
		pageComplete := true
		for _, candidate := range candidates {
			if claimed >= worker.batchSize || !worker.accepting.Load() {
				break scan
			}
			if _, duplicate := seen[candidate.ID]; duplicate {
				worker.queueSweepCursor = candidate.cursor()
				worker.queueSweepHasCursor = true
				continue
			}
			seen[candidate.ID] = struct{}{}

			select {
			case semaphore <- struct{}{}:
			case <-ctx.Done():
				queueCanceled = true
				break scan
			}
			worker.queueSweepCursor = candidate.cursor()
			worker.queueSweepHasCursor = true
			claimResult := make(chan bool, 1)
			done := make(chan error, 1)
			go func(id string) {
				defer func() { <-semaphore }()
				reportedClaim := false
				err := worker.executeJobWithClaimNotification(ctx, id, func() {
					reportedClaim = true
					claimResult <- true
				})
				if !reportedClaim {
					claimResult <- false
				}
				done <- err
			}(candidate.ID)

			select {
			case claimSucceeded := <-claimResult:
				if claimSucceeded {
					claimed++
					running = append(running, done)
					continue
				}
				if err := <-done; err != nil && !errors.Is(err, assetexport.ErrAttemptNotClaimable) {
					result = errors.Join(result, err)
				}
			case <-ctx.Done():
				queueCanceled = true
				running = append(running, done)
				break scan
			}
		}
		if pageComplete && len(candidates) < worker.batchSize {
			exhausted = true
			break
		}
	}
	for _, done := range running {
		if err := <-done; err != nil && !errors.Is(err, assetexport.ErrAttemptNotClaimable) {
			result = errors.Join(result, err)
		}
	}
	if queueCanceled {
		result = errors.Join(result, ctx.Err())
	}
	if exhausted {
		worker.queueSweepCursor = managedExportQueueCursor{}
		worker.queueSweepHasCursor = false
	}
	return result
}

func (worker *managedExportWorker) loadQueuedCandidates(
	ctx context.Context,
	cutoff time.Time,
	states []string,
) ([]managedExportQueueCandidate, error) {
	// A NULL legacy timestamp falls back to created_at; a zero timestamp remains a stable key.
	const queueTimestamp = "COALESCE(backup_asset_export_jobs.updated_at, backup_asset_export_jobs.created_at)"
	query := worker.db.WithContext(ctx).Model(&model.BackupAssetExportJob{}).
		Select("backup_asset_export_jobs.id, backup_asset_export_jobs.updated_at, backup_asset_export_jobs.created_at, "+
			"backup_asset_export_jobs.updated_at IS NULL AS queue_updated_at_missing").
		Where("backup_asset_export_jobs.execution_state IN ?", func() []string {
			if len(states) != 0 {
				return states
			}
			return []string{
				string(assetexport.ExecutionQueued), string(assetexport.ExecutionRetryWait), string(assetexport.ExecutionRunning),
				string(assetexport.ExecutionSealing),
			}
		}()).
		Where(queueTimestamp+" <= ?", cutoff)
	if worker.queueSweepHasCursor {
		query = query.Where("("+queueTimestamp+" > ?) OR ("+queueTimestamp+" = ? AND backup_asset_export_jobs.id > ?)",
			worker.queueSweepCursor.updatedAt, worker.queueSweepCursor.updatedAt, worker.queueSweepCursor.id)
	}
	var candidates []managedExportQueueCandidate
	if err := query.Order(queueTimestamp + " ASC").Order("backup_asset_export_jobs.id ASC").
		Limit(worker.batchSize).Find(&candidates).Error; err != nil {
		return nil, fmt.Errorf("load queued Export jobs: %w", err)
	}
	return candidates, nil
}

func (worker *managedExportWorker) executeJob(ctx context.Context, jobID string) error {
	return worker.executeJobWithClaimNotification(ctx, jobID, nil)
}

func (worker *managedExportWorker) executeJobWithClaimNotification(
	ctx context.Context,
	jobID string,
	onClaimed func(),
) error {
	worker.admissionMu.RLock()
	if !worker.accepting.Load() {
		worker.admissionMu.RUnlock()
		return assetexport.ErrAttemptNotClaimable
	}
	claim, err := worker.attempts.Claim(ctx, assetexport.AttemptClaimRequest{JobID: jobID, WorkerOwner: worker.owner})
	worker.admissionMu.RUnlock()
	if err != nil {
		return err
	}
	if onClaimed != nil {
		onClaimed()
	}
	if claim.SupersededAttemptID != "" {
		if err := worker.worker.DiscardAttempt(ctx, assetexport.PersistentDiscardAttemptRequest{
			JobID: jobID, AttemptID: claim.SupersededAttemptID,
		}); err != nil {
			return fmt.Errorf("retire superseded Export attempt: %w", err)
		}
	}
	workCtx, cancelWork := context.WithCancel(nonNilExportRuntimeContext(ctx))
	defer cancelWork()
	heartbeatCtx, stopHeartbeat := context.WithCancel(workCtx)
	heartbeatDone := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(worker.heartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				if err := workCtx.Err(); err != nil {
					heartbeatDone <- err
				} else {
					heartbeatDone <- nil
				}
				return
			case <-ticker.C:
				if _, err := worker.attempts.Heartbeat(heartbeatCtx, assetexport.AttemptHeartbeatRequest{
					JobID: jobID, AttemptID: claim.AttemptID, FenceToken: append([]byte(nil), claim.FenceToken...),
				}); err != nil {
					cancelWork()
					heartbeatDone <- err
					return
				}
			}
		}
	}()
	var heartbeatStop sync.Once
	var heartbeatErr error
	stopAndJoinHeartbeat := func() error {
		heartbeatStop.Do(func() {
			stopHeartbeat()
			heartbeatErr = <-heartbeatDone
		})
		return heartbeatErr
	}
	failAttempt := func(cause error) error {
		heartbeatFailure := stopAndJoinHeartbeat()
		if ctx.Err() != nil {
			return errors.Join(cause, heartbeatFailure)
		}
		failureCtx := context.WithoutCancel(nonNilExportRuntimeContext(ctx))
		if errors.Is(cause, assetexport.ErrSourceDeadlineReached) ||
			errors.Is(heartbeatFailure, assetexport.ErrSourceDeadlineReached) {
			cleanupErr := worker.lifecycle.FailSourceExpired(failureCtx, jobID)
			return errors.Join(cause, heartbeatFailure, cleanupErr)
		}
		category := managedExportFailureCategory(cause, heartbeatFailure)
		if _, err := worker.attempts.Fail(failureCtx, assetexport.AttemptFailureRequest{
			JobID: jobID, AttemptID: claim.AttemptID, FenceToken: append([]byte(nil), claim.FenceToken...),
			Category: category, Retryable: category != "deadline",
		}); err != nil {
			return errors.Join(cause, heartbeatFailure, err)
		}
		discardErr := worker.worker.DiscardAttempt(failureCtx, assetexport.PersistentDiscardAttemptRequest{
			JobID: jobID, AttemptID: claim.AttemptID,
		})
		return errors.Join(cause, heartbeatFailure, discardErr)
	}
	var items []model.BackupAssetExportItem
	if err := worker.db.WithContext(workCtx).Select("id", "entry_type").Where("job_id = ?", jobID).
		Order("ordinal ASC").Find(&items).Error; err != nil {
		return failAttempt(fmt.Errorf("load claimed Export items: %w", err))
	}
	for _, item := range items {
		if item.EntryType == string(backupasset.CatalogEntryFile) {
			if _, err := worker.worker.SpoolItem(workCtx, assetexport.PersistentSpoolItemRequest{
				JobID: jobID, AttemptID: claim.AttemptID, FenceToken: append([]byte(nil), claim.FenceToken...), ItemID: item.ID,
			}); err != nil {
				if checkpointErr, handled := worker.checkpointPreHeaderSpoolFailure(workCtx, jobID, claim, item.ID, err, false); handled {
					if checkpointErr != nil {
						return failAttempt(errors.Join(err, checkpointErr))
					}
				} else {
					return failAttempt(err)
				}
			}
		}
		if _, err := worker.attempts.Heartbeat(workCtx, assetexport.AttemptHeartbeatRequest{
			JobID: jobID, AttemptID: claim.AttemptID, FenceToken: append([]byte(nil), claim.FenceToken...),
		}); err != nil {
			cancelWork()
			return failAttempt(err)
		}
	}
	var sealed assetexport.PersistentSealResult
	for {
		sealed, err = worker.worker.SealArchive(workCtx, assetexport.PersistentSealRequest{
			JobID: jobID, AttemptID: claim.AttemptID, FenceToken: append([]byte(nil), claim.FenceToken...),
		})
		if err == nil {
			break
		}
		var preHeaderFailure *assetexport.PreHeaderSpoolFailure
		var recoveredItem interface{ ItemID() string }
		if errors.As(err, &preHeaderFailure) && errors.As(err, &recoveredItem) &&
			backupasset.ValidateOpaqueID(recoveredItem.ItemID()) == nil {
			checkpointErr, handled := worker.checkpointPreHeaderSpoolFailure(
				workCtx, jobID, claim, recoveredItem.ItemID(), err, true,
			)
			if handled {
				if checkpointErr != nil {
					return failAttempt(errors.Join(err, checkpointErr))
				}
				continue
			}
		}
		return failAttempt(err)
	}
	_, err = worker.worker.PublishReady(workCtx, assetexport.PersistentPublishRequest{
		JobID: jobID, AttemptID: claim.AttemptID, FenceToken: append([]byte(nil), claim.FenceToken...),
		ArtifactID: sealed.ArtifactID,
	})
	if err != nil {
		return failAttempt(err)
	}
	if err := stopAndJoinHeartbeat(); err != nil {
		return failAttempt(err)
	}
	return worker.maintainSourceLease(workCtx, jobID)
}

func (worker *managedExportWorker) checkpointPreHeaderSpoolFailure(
	ctx context.Context, jobID string, claim assetexport.AttemptClaim, itemID string, cause error, recoveredReadSpool bool,
) (error, bool) {
	var failure *assetexport.PreHeaderSpoolFailure
	if !errors.As(cause, &failure) || !managedExportRecoverablePreHeaderSpoolError(cause) {
		return nil, false
	}
	checkpointer, ok := worker.attempts.(managedExportAttemptCheckpointer)
	if !ok {
		return assetexport.ErrUnavailable, true
	}
	var itemAttempt model.BackupAssetExportItemAttempt
	err := worker.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []model.BackupAssetExportItemAttempt
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where(
			"job_id = ? AND attempt_id = ? AND item_id = ?", jobID, claim.AttemptID, itemID,
		).Order("id ASC").Limit(2).Find(&rows)
		if result.Error != nil {
			return fmt.Errorf("load pre-header Export item attempt: %w", result.Error)
		}
		if len(rows) != 1 {
			return assetexport.ErrUnavailable
		}
		candidate := rows[0]
		if candidate.ProviderBytes < 0 || candidate.ErrorCategory != "" || candidate.PackedAt != nil || candidate.FinishedAt != nil {
			return assetexport.ErrUnavailable
		}
		if recoveredReadSpool {
			if candidate.State != string(assetexport.ItemRead) || candidate.SpoolDigest == "" || candidate.SpoolSize <= 0 ||
				candidate.SpoolLocator == "" || candidate.LogicalBytes < 0 || candidate.ReadAt == nil {
				return assetexport.ErrUnavailable
			}
		} else if candidate.State != string(assetexport.ItemPending) || candidate.SpoolDigest != "" || candidate.SpoolSize != 0 ||
			candidate.SpoolLocator != "" || candidate.LogicalBytes != 0 || candidate.ReadAt != nil {
			return assetexport.ErrUnavailable
		}
		itemAttempt = candidate
		return nil
	})
	if err != nil {
		if errors.Is(err, assetexport.ErrUnavailable) {
			return assetexport.ErrUnavailable, true
		}
		return errors.Join(assetexport.ErrUnavailable, err), true
	}
	return checkpointer.Checkpoint(ctx, assetexport.AttemptCheckpoint{
		JobID: jobID, AttemptID: claim.AttemptID, FenceToken: append([]byte(nil), claim.FenceToken...),
		ItemID: itemID, State: assetexport.ItemFailed, ProviderBytes: itemAttempt.ProviderBytes,
		ErrorCategory: managedExportPreHeaderFailureCategory(cause), PreHeaderSpoolRecovered: recoveredReadSpool,
	}), true
}

func managedExportRecoverablePreHeaderSpoolError(err error) bool {
	return err != nil &&
		!errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) &&
		!errors.Is(err, assetexport.ErrAttemptFenceLost) && !errors.Is(err, backupasset.ErrLeaseFenceLost) &&
		!errors.Is(err, assetexport.ErrExecutionDeadlineReached) && !errors.Is(err, backupasset.ErrLeaseDeadlineExceeded) &&
		!errors.Is(err, assetexport.ErrSourceDeadlineReached) && !errors.Is(err, assetexport.ErrArchiveLimit) &&
		!errors.Is(err, assetexport.ErrQuotaExceeded) && !errors.Is(err, content.ErrAttemptBudgetExceeded)
}

func managedExportPreHeaderFailureCategory(cause error) string {
	if errors.Is(cause, content.ErrAttemptSourceChanged) {
		return "source_changed"
	}
	return "internal_failure"
}

func managedExportFailureCategory(cause, heartbeatErr error) string {
	if errors.Is(cause, assetexport.ErrExecutionDeadlineReached) ||
		errors.Is(heartbeatErr, assetexport.ErrExecutionDeadlineReached) {
		return "deadline"
	}
	if heartbeatErr != nil && !errors.Is(heartbeatErr, context.Canceled) && !errors.Is(heartbeatErr, context.DeadlineExceeded) {
		return "heartbeat_lost"
	}
	if errors.Is(cause, content.ErrAttemptSourceChanged) {
		return "source_changed"
	}
	return "archive_failed"
}

func (worker *managedExportWorker) reconcileSealing(ctx context.Context) error {
	var jobIDs []string
	if err := worker.db.WithContext(ctx).Model(&model.BackupAssetExportJob{}).
		Where("execution_state = ?", assetexport.ExecutionSealing).
		Order("updated_at ASC, id ASC").Limit(worker.batchSize).Pluck("id", &jobIDs).Error; err != nil {
		return fmt.Errorf("load sealing Export jobs: %w", err)
	}
	var result error
	for _, jobID := range jobIDs {
		if _, err := worker.worker.ReconcileJob(ctx, assetexport.PersistentReconcileRequest{JobID: jobID}); err != nil {
			if errors.Is(err, assetexport.ErrAttemptLeaseExpired) {
				continue
			}
			result = errors.Join(result, err)
		}
	}
	return result
}

type managedExportRuntime struct {
	db                *gorm.DB
	foundation        *backupasset.FoundationService
	keyring           *backupasset.Keyring
	validateRoot      func(context.Context, string) error
	build             func(context.Context, backupasset.ExportConfig, *assetexport.Store) (*managedExportGraph, error)
	publication       *managedExportPublication
	service           *managedExportServiceFacade
	delivery          *managedExportDeliveryFacade
	archive           *managedArchiveMemberFacade
	transitionTimeout time.Duration
	// beforeTransitionLock is a test synchronization hook; production leaves it nil.
	beforeTransitionLock func()
	// beforePublish is a test synchronization hook; production leaves it nil.
	beforePublish func()

	// transitionMu owns a complete settings transition. It is deliberately
	// separate from shutdownMu because transition recovery calls helpers that
	// acquire shutdownMu after releasing any existing lifecycle ownership.
	transitionMu    sync.Mutex
	shutdownMu      sync.Mutex
	mu              sync.RWMutex
	graph           *managedExportGraph
	startup         *managedExportStartup
	config          backupasset.ExportConfig
	graphShutdown   bool
	startupRootPath string
	startupRootSet  bool
	change          chan struct{}
	ready           atomic.Bool
	accepting       atomic.Bool
	stopped         atomic.Bool
}

// managedExportStartup owns a graph while startup reconciliation is running but
// before it is available through the stable facades. shutdownMu protects its
// lifecycle; cleanupErr is published before done is closed.
type managedExportStartup struct {
	cancel     context.CancelFunc
	done       chan struct{}
	cleanupErr error
}

func newManagedExportRuntime(dependencies managedExportRuntimeDependencies) (*managedExportRuntime, error) {
	if dependencies.DB == nil || dependencies.Foundation == nil || dependencies.Keyring == nil ||
		dependencies.ValidateRoot == nil || dependencies.Build == nil {
		return nil, fmt.Errorf("%w: Export runtime dependencies unavailable", backupasset.ErrInvalidState)
	}
	publication := dependencies.Publication
	if publication == nil {
		publication = newManagedExportPublication()
	}
	service := dependencies.Service
	if service == nil {
		service = &managedExportServiceFacade{publication: publication}
	}
	delivery := dependencies.Delivery
	if delivery == nil {
		delivery = &managedExportDeliveryFacade{publication: publication}
	}
	archive := dependencies.Archive
	if archive == nil {
		archive = &managedArchiveMemberFacade{publication: publication}
	}
	if service.publication != publication || delivery.publication != publication || archive.publication != publication {
		return nil, fmt.Errorf("%w: Export runtime facades do not share one publication", backupasset.ErrInvalidState)
	}
	transitionTimeout := dependencies.TransitionTimeout
	if transitionTimeout <= 0 {
		transitionTimeout = exportRuntimeRecoveryShutdownTimeout
	}
	runtime := &managedExportRuntime{
		db: dependencies.DB, foundation: dependencies.Foundation, keyring: dependencies.Keyring,
		validateRoot: dependencies.ValidateRoot, build: dependencies.Build, publication: publication,
		service: service, delivery: delivery, archive: archive, transitionTimeout: transitionTimeout,
		change: make(chan struct{}),
	}
	runtime.accepting.Store(true)
	return runtime, nil
}

func (runtime *managedExportRuntime) Startup(ctx context.Context) error {
	if runtime == nil {
		return fmt.Errorf("%w: Export runtime unavailable", backupasset.ErrInvalidState)
	}
	runtime.transitionMu.Lock()
	defer runtime.transitionMu.Unlock()
	if runtime.stopped.Load() || !runtime.accepting.Load() {
		return fmt.Errorf("%w: Export runtime unavailable", backupasset.ErrInvalidState)
	}
	ctx = nonNilExportRuntimeContext(ctx)
	runtime.mu.Lock()
	alreadyStarted := runtime.graph != nil
	runtime.mu.Unlock()
	if alreadyStarted {
		return nil
	}
	foundationEnabled, err := runtime.foundation.FeatureEnabled()
	if err != nil {
		return err
	}
	config, err := runtime.foundation.ExportConfig()
	if err != nil {
		return err
	}
	config = runtime.pinStartupRoot(config)
	if !foundationEnabled || !config.Enabled {
		return runtime.startDisabledMaintenance(ctx, config)
	}
	return runtime.startWithConfig(ctx, config)
}

func (runtime *managedExportRuntime) startDisabledMaintenance(ctx context.Context, config backupasset.ExportConfig) error {
	runtime.shutdownMu.Lock()
	defer runtime.shutdownMu.Unlock()
	if runtime.stopped.Load() || !runtime.accepting.Load() {
		return fmt.Errorf("%w: Export runtime unavailable", backupasset.ErrInvalidState)
	}
	needed, err := runtime.disabledMaintenanceNeeded(ctx, config)
	if err != nil {
		return err
	}
	if !needed {
		runtime.ready.Store(false)
		return nil
	}
	if err := runtime.validateRoot(ctx, config.Root); err != nil {
		return fmt.Errorf("validate Export root for disabled maintenance: %w", err)
	}
	store, err := assetexport.OpenStore(assetexport.StoreConfig{Root: config.Root})
	if err != nil {
		return err
	}
	graph, err := runtime.build(ctx, config, store)
	if err != nil || graph == nil || graph.store != store || graph.terminalize == nil {
		if err == nil {
			err = backupasset.ErrInvalidState
		}
		return fmt.Errorf("build disabled Export maintenance: %w", errors.Join(err, store.Close()))
	}
	if graph.stopAccepting != nil {
		graph.stopAccepting()
	}
	terminalizeErr := graph.terminalize(ctx)
	closeErr := store.Close()
	runtime.ready.Store(false)
	return errors.Join(terminalizeErr, closeErr)
}

func (runtime *managedExportRuntime) disabledMaintenanceNeeded(
	ctx context.Context,
	config backupasset.ExportConfig,
) (bool, error) {
	if runtime == nil || runtime.db == nil {
		return false, backupasset.ErrInvalidState
	}
	if runtime.db.Migrator().HasTable(&model.BackupAssetExportJob{}) {
		var candidate model.BackupAssetExportJob
		result := runtime.db.WithContext(ctx).Model(&model.BackupAssetExportJob{}).
			Select("id").Where("cleanup_state <> ?", string(assetexport.CleanupPurged)).Limit(1).Find(&candidate)
		if result.Error != nil {
			return false, fmt.Errorf("find disabled Export lifecycle work: %w", result.Error)
		}
		if result.RowsAffected != 0 {
			return true, nil
		}
	}
	return assetexport.HasNonLockEntries(assetexport.StoreConfig{Root: config.Root})
}

func (runtime *managedExportRuntime) startWithConfig(ctx context.Context, config backupasset.ExportConfig) (resultErr error) {
	if runtime == nil || runtime.stopped.Load() || !runtime.accepting.Load() {
		return fmt.Errorf("%w: Export runtime unavailable", backupasset.ErrInvalidState)
	}
	ctx = nonNilExportRuntimeContext(ctx)
	runtime.mu.Lock()
	alreadyStarted := runtime.graph != nil
	runtime.mu.Unlock()
	if alreadyStarted {
		return nil
	}
	if err := runtime.validateRoot(ctx, config.Root); err != nil {
		return fmt.Errorf("validate Export root: %w", err)
	}
	if err := runtime.prepareExportStoreKeyring(ctx, config); err != nil {
		return err
	}
	store, err := assetexport.OpenStore(assetexport.StoreConfig{Root: config.Root})
	if err != nil {
		return err
	}
	graph, err := runtime.build(ctx, config, store)
	if err != nil || graph == nil || graph.store != store {
		if err == nil {
			err = backupasset.ErrInvalidState
		}
		return fmt.Errorf("build Export runtime: %w", errors.Join(err, cleanupUnownedExportGraphAndStore(ctx, graph, store)))
	}
	startupCtx, cancelStartup := context.WithCancel(ctx)
	startup := &managedExportStartup{cancel: cancelStartup, done: make(chan struct{})}
	runtime.shutdownMu.Lock()
	if runtime.stopped.Load() || !runtime.accepting.Load() {
		runtime.shutdownMu.Unlock()
		cancelStartup()
		return fmt.Errorf("start stopped Export runtime: %w", errors.Join(
			backupasset.ErrInvalidState, cleanupUnownedExportGraph(ctx, graph),
		))
	}
	runtime.mu.Lock()
	if runtime.graph != nil {
		runtime.mu.Unlock()
		runtime.shutdownMu.Unlock()
		cancelStartup()
		if err := cleanupUnownedExportGraph(ctx, graph); err != nil {
			return fmt.Errorf("shutdown competing Export runtime graph: %w", err)
		}
		return nil
	}
	runtime.graph = graph
	runtime.config = config
	runtime.graphShutdown = false
	runtime.startup = startup
	runtime.signalGraphChangedLocked()
	runtime.mu.Unlock()
	runtime.shutdownMu.Unlock()

	var startupErr error
	if graph.startup != nil {
		startupErr = graph.startup(startupCtx)
	}
	cancelStartup()
	runtime.shutdownMu.Lock()
	defer runtime.shutdownMu.Unlock()
	var startupCleanupErr error
	defer func() { runtime.finishStartingExportGraphLocked(startup, startupCleanupErr) }()
	runtime.mu.Lock()
	stopped := runtime.stopped.Load() || !runtime.accepting.Load()
	ownsGraph := runtime.graph == graph
	ownsStartup := runtime.startup == startup
	runtime.mu.Unlock()
	if stopped {
		if ownsGraph {
			startupCleanupErr = runtime.shutdownOwnedGraphLocked(ctx)
		} else {
			startupCleanupErr = cleanupUnownedExportGraph(ctx, graph)
		}
		return fmt.Errorf("start stopped Export runtime: %w", errors.Join(
			backupasset.ErrInvalidState, startupErr, startupCleanupErr,
		))
	}
	if !ownsGraph || !ownsStartup {
		startupCleanupErr = cleanupUnownedExportGraph(ctx, graph)
		if startupErr != nil {
			return fmt.Errorf("start Export runtime: %w", errors.Join(startupErr, startupCleanupErr))
		}
		if startupCleanupErr != nil {
			return fmt.Errorf("shutdown competing Export runtime graph: %w", startupCleanupErr)
		}
		return nil
	}
	if startupErr != nil {
		if graph.stopAccepting != nil {
			graph.stopAccepting()
		}
		startupCleanupErr = runtime.cleanupFailedStartingExportGraphLocked(ctx, graph)
		return fmt.Errorf("start Export runtime: %w", errors.Join(startupErr, startupCleanupErr))
	}
	if runtime.beforePublish != nil {
		runtime.shutdownMu.Unlock()
		runtime.beforePublish()
		runtime.shutdownMu.Lock()
		runtime.mu.Lock()
		stopped = runtime.stopped.Load() || !runtime.accepting.Load()
		ownsGraph = runtime.graph == graph
		ownsStartup = runtime.startup == startup
		runtime.mu.Unlock()
		if stopped {
			if ownsGraph {
				startupCleanupErr = runtime.shutdownOwnedGraphLocked(ctx)
			} else {
				startupCleanupErr = cleanupUnownedExportGraph(ctx, graph)
			}
			return fmt.Errorf("start stopped Export runtime: %w", errors.Join(
				backupasset.ErrInvalidState, startupCleanupErr,
			))
		}
		if !ownsGraph || !ownsStartup {
			startupCleanupErr = cleanupUnownedExportGraph(ctx, graph)
			if startupCleanupErr != nil {
				return fmt.Errorf("shutdown competing Export runtime graph: %w", startupCleanupErr)
			}
			return nil
		}
	}
	runtime.publication.publish(graph)
	runtime.ready.Store(true)
	runtime.mu.Lock()
	runtime.signalGraphChangedLocked()
	runtime.mu.Unlock()
	return nil
}

// finishStartingExportGraphLocked publishes the startup owner's cleanup result
// before waking a concurrent Shutdown. It requires runtime.shutdownMu.
func (runtime *managedExportRuntime) finishStartingExportGraphLocked(
	startup *managedExportStartup,
	cleanupErr error,
) {
	if runtime == nil || startup == nil {
		return
	}
	startup.cleanupErr = cleanupErr
	runtime.mu.Lock()
	if runtime.startup == startup {
		runtime.startup = nil
		runtime.signalGraphChangedLocked()
	}
	runtime.mu.Unlock()
	close(startup.done)
}

// cleanupFailedStartingExportGraphLocked tears down a graph whose own startup
// failed before publication. Unlike Shutdown, this leaves no retained graph so
// a later Startup may retry even when the best-effort cleanup reports an error.
// It requires runtime.shutdownMu and is called after graph.stopAccepting.
func (runtime *managedExportRuntime) cleanupFailedStartingExportGraphLocked(
	ctx context.Context,
	graph *managedExportGraph,
) error {
	if graph == nil {
		return backupasset.ErrInvalidState
	}
	var shutdownErr error
	if graph.shutdown != nil {
		shutdownErr = graph.shutdown(ctx)
	}
	var storeCloseErr error
	if graph.store != nil {
		storeCloseErr = graph.store.Close()
	}
	runtime.mu.Lock()
	if runtime.graph == graph {
		runtime.graph = nil
		runtime.graphShutdown = false
		runtime.signalGraphChangedLocked()
	}
	runtime.mu.Unlock()
	return errors.Join(shutdownErr, storeCloseErr)
}

func (runtime *managedExportRuntime) prepareExportStoreKeyring(
	ctx context.Context,
	config backupasset.ExportConfig,
) error {
	prepare := func() error {
		if _, err := runtime.keyring.RewrapDomains(ctx, backupasset.KeyDomainExportStore); err != nil {
			return fmt.Errorf("rewrap Export Store key: %w", err)
		}
		if _, err := runtime.keyring.Ensure(ctx, backupasset.KeyDomainExportStore); err != nil {
			return fmt.Errorf("ensure Export Store key: %w", err)
		}
		return nil
	}
	for attempt := 0; attempt < 2; attempt++ {
		preparationErr := prepare()
		if preparationErr == nil {
			return nil
		}
		if !errors.Is(preparationErr, backupasset.ErrKeyLost) && !errors.Is(preparationErr, backupasset.ErrKeyUnavailable) {
			return preparationErr
		}

		foundUnreadable, isolationErr := runtime.invalidateUnreadableExportKeys(ctx, config)
		if foundUnreadable && isolationErr != nil {
			return errors.Join(assetexport.ErrUnavailable, preparationErr, isolationErr)
		}
		if attempt == 1 {
			return errors.Join(assetexport.ErrUnavailable, preparationErr, isolationErr)
		}
		if foundUnreadable {
			continue
		}
		if _, activeErr := runtime.keyring.Active(ctx, backupasset.KeyDomainExportStore); activeErr != nil {
			return errors.Join(
				assetexport.ErrUnavailable,
				preparationErr,
				isolationErr,
				fmt.Errorf("verify active Export Store key after preparation failure: %w", activeErr),
			)
		}
	}
	return assetexport.ErrUnavailable
}

func cleanupUnownedExportGraph(ctx context.Context, graph *managedExportGraph) error {
	if graph == nil {
		return backupasset.ErrInvalidState
	}
	if graph.stopAccepting != nil {
		graph.stopAccepting()
	}
	var shutdownErr error
	if graph.shutdown != nil {
		shutdownErr = graph.shutdown(ctx)
	}
	var storeCloseErr error
	if graph.store != nil {
		storeCloseErr = graph.store.Close()
	}
	return errors.Join(shutdownErr, storeCloseErr)
}

func cleanupUnownedExportGraphAndStore(
	ctx context.Context,
	graph *managedExportGraph,
	store *assetexport.Store,
) error {
	if graph == nil {
		if store == nil {
			return nil
		}
		return store.Close()
	}
	graphStore := graph.store
	cleanupErr := cleanupUnownedExportGraph(ctx, graph)
	if graphStore != store && store != nil {
		cleanupErr = errors.Join(cleanupErr, store.Close())
	}
	return cleanupErr
}

// invalidateUnreadableExportKeys probes each eligible Export KEK through the
// typed keyring API, then revokes only the jobs bound to unreadable versions.
func (runtime *managedExportRuntime) invalidateUnreadableExportKeys(
	ctx context.Context,
	config backupasset.ExportConfig,
) (bool, error) {
	if runtime == nil || runtime.db == nil || runtime.keyring == nil || runtime.build == nil || config.ReconcileBatchSize <= 0 {
		return false, backupasset.ErrInvalidState
	}
	ctx = nonNilExportRuntimeContext(ctx)

	var keys []model.WrappedDomainKey
	if err := runtime.db.WithContext(ctx).
		Where("domain = ? AND state IN ?", backupasset.KeyDomainExportStore, []string{
			string(backupasset.DomainKeyActive), string(backupasset.DomainKeyVerifyOnly),
		}).
		Order("version ASC").Find(&keys).Error; err != nil {
		return false, fmt.Errorf("load unreadable Export key versions: %w", err)
	}
	if len(keys) == 0 {
		return false, nil
	}
	unreadableVersions := make([]int, 0, len(keys))
	for _, key := range keys {
		if _, err := runtime.keyring.ByVersion(ctx, backupasset.KeyDomainExportStore, key.Version); err == nil {
			continue
		} else if errors.Is(err, backupasset.ErrKeyLost) || errors.Is(err, backupasset.ErrKeyUnavailable) {
			unreadableVersions = append(unreadableVersions, key.Version)
		} else {
			return len(unreadableVersions) > 0, fmt.Errorf("verify Export key version %d: %w", key.Version, err)
		}
	}
	if len(unreadableVersions) == 0 {
		return false, nil
	}

	store, err := assetexport.OpenStore(assetexport.StoreConfig{Root: config.Root})
	if err != nil {
		return true, fmt.Errorf("open Export Store for key loss: %w", err)
	}

	var invalidationErr error
	graph, buildErr := runtime.build(ctx, config, store)
	if buildErr != nil || graph == nil || graph.store != store || graph.lifecycle == nil {
		if buildErr == nil {
			buildErr = backupasset.ErrInvalidState
		}
		invalidationErr = fmt.Errorf("build Export lifecycle for key loss: %w", buildErr)
	} else {
		for _, version := range unreadableVersions {
			if err := graph.lifecycle.MarkKeyVersionLost(ctx, runtime.keyring, version, config.ReconcileBatchSize); err != nil {
				invalidationErr = errors.Join(invalidationErr, fmt.Errorf("invalidate Export key version %d: %w", version, err))
			}
		}
	}

	graphCleanupErr := cleanupUnownedExportGraphAndStore(ctx, graph, store)
	if invalidationErr != nil || graphCleanupErr != nil {
		return true, errors.Join(invalidationErr, graphCleanupErr)
	}
	return true, nil
}

func (runtime *managedExportRuntime) TransitionSettings(
	ctx context.Context,
	globalEnabled bool,
	config backupasset.ExportConfig,
	persist func() error,
) error {
	return runtime.TransitionSettingsWithRestore(ctx, globalEnabled, config, persist, func() error { return nil })
}

func (runtime *managedExportRuntime) TransitionSettingsWithRestore(
	ctx context.Context,
	globalEnabled bool,
	config backupasset.ExportConfig,
	persist func() error,
	restorePersisted func() error,
) error {
	if persist == nil || restorePersisted == nil {
		return fmt.Errorf("%w: Export settings transition unavailable", backupasset.ErrInvalidState)
	}
	return runtime.TransitionSettingsContextWithRestore(
		ctx,
		globalEnabled,
		config,
		func(context.Context) error { return persist() },
		func(context.Context) error { return restorePersisted() },
	)
}

func (runtime *managedExportRuntime) TransitionSettingsContextWithRestore(
	ctx context.Context,
	globalEnabled bool,
	config backupasset.ExportConfig,
	persist func(context.Context) error,
	restorePersisted func(context.Context) error,
) error {
	if runtime == nil || persist == nil || restorePersisted == nil {
		return fmt.Errorf("%w: Export settings transition unavailable", backupasset.ErrInvalidState)
	}
	if runtime.beforeTransitionLock != nil {
		runtime.beforeTransitionLock()
	}
	runtime.transitionMu.Lock()
	defer runtime.transitionMu.Unlock()
	if runtime.stopped.Load() {
		return fmt.Errorf("%w: Export runtime unavailable", backupasset.ErrInvalidState)
	}
	ctx = nonNilExportRuntimeContext(ctx)
	transitionCtx, cancelTransition := runtime.boundedTransitionDrainContext(ctx)
	defer cancelTransition()
	runtime.shutdownMu.Lock()
	if runtime.stopped.Load() {
		runtime.shutdownMu.Unlock()
		return fmt.Errorf("%w: Export runtime unavailable", backupasset.ErrInvalidState)
	}
	runtime.mu.Lock()
	graph := runtime.graph
	previousConfig := runtime.config
	if runtime.startupRootSet {
		config.Root = runtime.startupRootPath
	}
	runtime.mu.Unlock()
	if graph == nil {
		if err := persist(transitionCtx); err != nil {
			recoveryCtx, cancelRecovery := runtime.boundedDetachedRecoveryContext(transitionCtx)
			defer cancelRecovery()
			runtime.shutdownMu.Unlock()
			return errors.Join(err, runtime.restoreExportPersisted(recoveryCtx, restorePersisted))
		}
		runtime.shutdownMu.Unlock()
		var startErr error
		if !globalEnabled || !config.Enabled {
			startErr = runtime.startDisabledMaintenance(ctx, config)
		} else {
			startErr = runtime.startWithConfig(ctx, config)
		}
		if startErr != nil {
			recoveryCtx, cancelRecovery := runtime.boundedDetachedRecoveryContext(transitionCtx)
			defer cancelRecovery()
			return errors.Join(startErr, runtime.restoreExportPersisted(recoveryCtx, restorePersisted))
		}
		return nil
	}

	runtime.mu.Lock()
	graphShutdown := runtime.graph == graph && runtime.graphShutdown
	runtime.mu.Unlock()
	runtime.ready.Store(false)
	runtime.publication.unpublish()
	// stopAccepting makes this graph non-reusable, so recover it through a fresh prior-config graph.
	restoreStoppedTransition := func(transitionErr error) error {
		recoveryCtx, cancelRecovery := runtime.boundedDetachedRecoveryContext(transitionCtx)
		defer cancelRecovery()
		cleanupErr := runtime.shutdownStoppedTransitionGraph(recoveryCtx, graph)
		runtime.shutdownMu.Unlock()
		if cleanupErr != nil {
			return errors.Join(transitionErr, cleanupErr)
		}
		return errors.Join(transitionErr, runtime.startWithConfig(recoveryCtx, previousConfig))
	}
	if !graphShutdown {
		if err := runtime.publication.waitIdle(transitionCtx); err != nil {
			runtime.publication.publish(graph)
			runtime.ready.Store(true)
			runtime.shutdownMu.Unlock()
			return err
		}
		if graph.stopAccepting != nil {
			graph.stopAccepting()
		}
		if graph.drain != nil {
			if err := graph.drain(transitionCtx); err != nil {
				return restoreStoppedTransition(err)
			}
		}
		if graph.shutdown != nil {
			if err := graph.shutdown(transitionCtx); err != nil {
				return restoreStoppedTransition(err)
			}
		}
		runtime.mu.Lock()
		if runtime.graph == graph {
			runtime.graphShutdown = true
		}
		runtime.mu.Unlock()
	}
	if err := graph.store.Close(); err != nil {
		runtime.shutdownMu.Unlock()
		return err
	}
	runtime.mu.Lock()
	if runtime.graph == graph {
		runtime.graph = nil
		runtime.graphShutdown = false
		runtime.signalGraphChangedLocked()
	}
	runtime.mu.Unlock()
	if err := persist(transitionCtx); err != nil {
		recoveryCtx, cancelRecovery := runtime.boundedDetachedRecoveryContext(transitionCtx)
		defer cancelRecovery()
		runtime.shutdownMu.Unlock()
		return errors.Join(err, runtime.restoreExportPersisted(recoveryCtx, restorePersisted), runtime.startWithConfig(recoveryCtx, previousConfig))
	}
	runtime.shutdownMu.Unlock()
	var startErr error
	if !globalEnabled || !config.Enabled {
		startErr = runtime.startDisabledMaintenance(ctx, config)
	} else {
		startErr = runtime.startWithConfig(ctx, config)
	}
	if startErr == nil {
		return nil
	}
	recoveryCtx, cancelRecovery := runtime.boundedDetachedRecoveryContext(transitionCtx)
	defer cancelRecovery()
	return errors.Join(startErr, runtime.restoreExportPersisted(recoveryCtx, restorePersisted), runtime.startWithConfig(recoveryCtx, previousConfig))
}

func (runtime *managedExportRuntime) restoreExportPersisted(ctx context.Context, restore func(context.Context) error) error {
	err := restore(ctx)
	if err == nil {
		return nil
	}
	runtime.ready.Store(false)
	runtime.accepting.Store(false)
	if runtime.publication != nil {
		runtime.publication.unpublish()
	}
	return err
}

func (runtime *managedExportRuntime) shutdownStoppedTransitionGraph(ctx context.Context, graph *managedExportGraph) error {
	if graph.shutdown != nil {
		if err := graph.shutdown(ctx); err != nil {
			return err
		}
	}
	runtime.mu.Lock()
	if runtime.graph == graph {
		runtime.graphShutdown = true
	}
	runtime.mu.Unlock()
	if err := graph.store.Close(); err != nil {
		return err
	}
	runtime.mu.Lock()
	if runtime.graph == graph {
		runtime.graph = nil
		runtime.graphShutdown = false
		runtime.signalGraphChangedLocked()
	}
	runtime.mu.Unlock()
	return nil
}

func (runtime *managedExportRuntime) Ready() bool {
	return runtime != nil && runtime.ready.Load() && runtime.accepting.Load() && !runtime.stopped.Load()
}

func (runtime *managedExportRuntime) Service() *managedExportServiceFacade {
	if runtime == nil {
		return nil
	}
	return runtime.service
}

func (runtime *managedExportRuntime) Delivery() *managedExportDeliveryFacade {
	if runtime == nil {
		return nil
	}
	return runtime.delivery

}

func (runtime *managedExportRuntime) ArchiveMember() *managedArchiveMemberFacade {
	if runtime == nil {
		return nil
	}
	return runtime.archive
}

func (runtime *managedExportRuntime) StopAccepting() {
	if runtime == nil {
		return
	}
	runtime.shutdownMu.Lock()
	defer runtime.shutdownMu.Unlock()
	runtime.stopAcceptingLocked()
}

// stopAcceptingLocked requires runtime.shutdownMu to be held.
func (runtime *managedExportRuntime) stopAcceptingLocked() {
	wasAccepting := runtime.accepting.Swap(false)
	runtime.ready.Store(false)
	runtime.publication.unpublish()
	runtime.mu.Lock()
	graph := runtime.graph
	startup := runtime.startup
	runtime.mu.Unlock()
	if startup != nil && startup.cancel != nil {
		startup.cancel()
	}
	if wasAccepting && graph != nil && graph.stopAccepting != nil {
		graph.stopAccepting()
	}
}

func (runtime *managedExportRuntime) Run(ctx context.Context) {
	if runtime == nil || runtime.stopped.Load() {
		return
	}
	ctx = nonNilExportRuntimeContext(ctx)
	for {
		if runtime.stopped.Load() {
			return
		}
		runtime.mu.Lock()
		graph, startup, changed := runtime.graph, runtime.startup, runtime.change
		runnable := graph != nil && startup == nil && runtime.ready.Load() && runtime.accepting.Load()
		runtime.mu.Unlock()
		if runtime.stopped.Load() {
			return
		}
		if !runnable || graph.run == nil {
			select {
			case <-ctx.Done():
				return
			case <-changed:
				continue
			}
		}
		runCtx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() {
			defer close(done)
			graph.run(runCtx)
		}()
		select {
		case <-ctx.Done():
			cancel()
			<-done
			return
		case <-changed:
			cancel()
			<-done
			continue
		case <-done:
			cancel()
			select {
			case <-ctx.Done():
				return
			case <-changed:
				continue
			}
		}
	}
}

func (runtime *managedExportRuntime) Shutdown(ctx context.Context) error {
	if runtime == nil {
		return nil
	}
	ctx = nonNilExportRuntimeContext(ctx)
	runtime.shutdownMu.Lock()
	runtime.stopAcceptingLocked()
	runtime.stopped.Store(true)
	runtime.mu.Lock()
	startup := runtime.startup
	runtime.signalGraphChangedLocked()
	runtime.mu.Unlock()
	runtime.shutdownMu.Unlock()

	if startup != nil {
		select {
		case <-startup.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	runtime.shutdownMu.Lock()
	defer runtime.shutdownMu.Unlock()
	if startup != nil && startup.cleanupErr != nil {
		return startup.cleanupErr
	}
	return runtime.shutdownOwnedGraphLocked(ctx)
}

// shutdownOwnedGraphLocked requires runtime.shutdownMu to be held.
func (runtime *managedExportRuntime) shutdownOwnedGraphLocked(ctx context.Context) error {
	runtime.stopAcceptingLocked()
	runtime.stopped.Store(true)
	runtime.mu.Lock()
	graph := runtime.graph
	runtime.signalGraphChangedLocked()
	runtime.mu.Unlock()
	if err := runtime.publication.waitIdle(ctx); err != nil {
		return err
	}
	if graph == nil {
		return nil
	}
	runtime.mu.Lock()
	graphShutdown := runtime.graphShutdown
	runtime.mu.Unlock()
	if !graphShutdown {
		if graph.shutdown != nil {
			if err := graph.shutdown(ctx); err != nil {
				return err
			}
		}
		runtime.mu.Lock()
		if runtime.graph == graph {
			runtime.graphShutdown = true
		}
		runtime.mu.Unlock()
	}
	if err := graph.store.Close(); err != nil {
		return err
	}
	runtime.mu.Lock()
	if runtime.graph == graph {
		runtime.graph = nil
		runtime.graphShutdown = false
		runtime.signalGraphChangedLocked()
	}
	runtime.mu.Unlock()
	return nil
}

func (runtime *managedExportRuntime) PrepareSchemaDown(ctx context.Context, down func() error) error {
	if runtime == nil || down == nil {
		return fmt.Errorf("%w: Export schema down unavailable", backupasset.ErrInvalidState)
	}
	ctx = nonNilExportRuntimeContext(ctx)
	runtime.StopAccepting()
	if err := runtime.publication.waitIdle(ctx); err != nil {
		return err
	}
	runtime.mu.Lock()
	graph := runtime.graph
	runtime.mu.Unlock()
	if graph != nil {
		if graph.drain != nil {
			if err := graph.drain(ctx); err != nil {
				return err
			}
		}
		return graph.store.PrepareSchemaDown(down)
	}
	root, pinned := runtime.startupRoot()
	if !pinned {
		config, err := runtime.foundation.ExportConfig()
		if err != nil {
			return err
		}
		root = config.Root
	}
	if _, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
		return assetexport.ErrInvalidStore
	} else if err != nil {
		return assetexport.ErrInvalidStore
	}
	if err := runtime.validateRoot(ctx, root); err != nil {
		return fmt.Errorf("validate Export root for schema down: %w", err)
	}
	store, err := assetexport.OpenStore(assetexport.StoreConfig{Root: root})
	if err != nil {
		return err
	}
	return errors.Join(store.PrepareSchemaDown(down), store.Close())
}

func (runtime *managedExportRuntime) pinStartupRoot(config backupasset.ExportConfig) backupasset.ExportConfig {
	if runtime == nil {
		return config
	}
	runtime.mu.Lock()
	if !runtime.startupRootSet {
		runtime.startupRootPath = config.Root
		runtime.startupRootSet = true
	}
	config.Root = runtime.startupRootPath
	runtime.mu.Unlock()
	return config
}

func (runtime *managedExportRuntime) startupRoot() (string, bool) {
	if runtime == nil {
		return "", false
	}
	runtime.mu.RLock()
	defer runtime.mu.RUnlock()
	return runtime.startupRootPath, runtime.startupRootSet
}

func (runtime *managedExportRuntime) signalGraphChangedLocked() {
	close(runtime.change)
	runtime.change = make(chan struct{})
}

type runtimeArchiveMemberCoordinator struct {
	runtime *managedProcessingRuntime
}

func (adapter runtimeArchiveMemberCoordinator) RequestWork(
	ctx context.Context,
	request processing.WorkRequest,
) (processing.WorkResult, error) {
	coordinator := adapter.current()
	if coordinator == nil {
		return processing.WorkResult{}, processing.ErrNotDeployed
	}
	return coordinator.RequestWork(ctx, request)
}

func (adapter runtimeArchiveMemberCoordinator) RemoveInterest(
	ctx context.Context,
	jobID string,
	ownerKind processing.InterestOwnerKind,
	ownerKey string,
	reason processing.InterestRemovedReason,
) error {
	coordinator := adapter.current()
	if coordinator == nil {
		return processing.ErrNotDeployed
	}
	return coordinator.RemoveInterest(ctx, jobID, ownerKind, ownerKey, reason)
}

func (adapter runtimeArchiveMemberCoordinator) current() *processing.Coordinator {
	return adapter.runtime.archiveMemberCoordinator()
}

type runtimeArchiveMemberIndexResolver struct {
	db       *gorm.DB
	resolver *content.DerivedRepresentationResolver
}

type runtimeArchiveMemberIndexRow struct {
	ArtifactSetID              string
	JobID                      string
	RecoveryPointID            string
	CatalogGenerationID        string
	EntryID                    string
	SourceFingerprint          string
	SecurityPolicyRevision     string
	PipelineFingerprint        string
	ProviderCapabilityRevision int64
	AbsoluteDeadline           time.Time
}

func (adapter runtimeArchiveMemberIndexResolver) Resolve(
	ctx context.Context,
	asset content.AuthorizedAsset,
	expectedRevision string,
) (processing.ArchiveMemberIndexBinding, error) {
	if adapter.db == nil || adapter.resolver == nil {
		return processing.ArchiveMemberIndexBinding{}, processing.ErrArchiveMemberUnavailable
	}
	ctx = nonNilExportRuntimeContext(ctx)
	index, err := adapter.resolver.ResolveArchiveIndex(ctx, content.ArchiveIndexRequest{
		Asset: asset, SecurityPolicyRevision: processingSecurityPolicyRevision,
		ExpectedRevision: expectedRevision,
	})
	if err != nil {
		return processing.ArchiveMemberIndexBinding{}, err
	}
	var row runtimeArchiveMemberIndexRow
	result := adapter.db.WithContext(ctx).Table("backup_asset_derived_artifacts AS artifacts").
		Select(`sets.id AS artifact_set_id, jobs.id AS job_id,
			sets.recovery_point_id, sets.catalog_generation_id, sets.entry_id,
			sets.source_fingerprint, sets.security_policy_revision,
			jobs.pipeline_fingerprint, jobs.provider_capability_revision,
			jobs.absolute_deadline`).
		Joins("JOIN backup_asset_derived_artifact_sets AS sets ON sets.id = artifacts.artifact_set_id").
		Joins("JOIN backup_asset_processing_jobs AS jobs ON jobs.id = sets.job_id").
		Where(`artifacts.id = ? AND artifacts.plaintext_digest = ?
			AND sets.state = ? AND sets.completeness = ?
			AND jobs.state = ? AND jobs.is_current = ?
			AND jobs.current_artifact_set_id = sets.id`,
			index.ArtifactID(), index.IndexRevision, "active", "complete", processing.ProcessingSucceeded, false).
		Limit(1).Scan(&row)
	if result.Error != nil {
		return processing.ArchiveMemberIndexBinding{}, fmt.Errorf("resolve archive index deadline: %w", result.Error)
	}
	if result.RowsAffected != 1 || backupasset.ValidateOpaqueID(row.ArtifactSetID) != nil ||
		backupasset.ValidateOpaqueID(row.JobID) != nil || row.RecoveryPointID != asset.Ref.RecoveryPointID ||
		row.CatalogGenerationID != asset.CatalogGenerationID || row.EntryID != asset.Ref.EntryID ||
		row.SourceFingerprint != asset.SourceFingerprint || row.SecurityPolicyRevision != index.SecurityPolicyRevision() ||
		row.PipelineFingerprint != index.PipelineFingerprint() ||
		row.AbsoluteDeadline.Location() != time.UTC {
		return processing.ArchiveMemberIndexBinding{}, processing.ErrArchiveMemberUnavailable
	}
	if row.ProviderCapabilityRevision != asset.ProviderCapabilityRevision {
		return processing.ArchiveMemberIndexBinding{}, backupasset.ErrNotFound
	}
	members := make([]processing.ArchiveMemberIndexEntry, len(index.Entries))
	for ordinal, entry := range index.Entries {
		members[ordinal] = processing.ArchiveMemberIndexEntry{
			OpaqueID: entry.ID, Ordinal: ordinal, ParentID: entry.ParentID,
			DisplayName: entry.DisplayName, Size: entry.Size, MediaType: entry.MediaType,
		}
	}
	return processing.ArchiveMemberIndexBinding{
		ArtifactID: index.ArtifactID(), Revision: index.IndexRevision,
		PipelineFingerprint:    index.PipelineFingerprint(),
		SecurityPolicyRevision: index.SecurityPolicyRevision(),
		AbsoluteExpiresAt:      row.AbsoluteDeadline.UTC(), Members: members,
	}, nil
}

type runtimeArchiveMemberAuthorityResolver struct {
	db        *gorm.DB
	authorize processingAssetAuthorizer
}

type runtimeArchiveMemberOwnerRole struct {
	Role string
}

func (adapter runtimeArchiveMemberAuthorityResolver) Resolve(
	ctx context.Context,
	request model.BackupAssetArchiveMemberRequest,
) (processing.ArchiveMemberProcessingAuthority, error) {
	asset, err := adapter.resolveAsset(ctx, request)
	if err != nil {
		return processing.ArchiveMemberProcessingAuthority{}, err
	}
	return processing.ArchiveMemberProcessingAuthority{
		ProviderCapabilityRevision: asset.ProviderCapabilityRevision,
		SecurityPolicyRevision:     processingSecurityPolicyRevision,
	}, nil
}

func (adapter runtimeArchiveMemberAuthorityResolver) resolveAsset(
	ctx context.Context,
	request model.BackupAssetArchiveMemberRequest,
) (content.AuthorizedAsset, error) {
	if adapter.db == nil || adapter.authorize == nil || request.OwnerUserID == 0 {
		return content.AuthorizedAsset{}, processing.ErrArchiveMemberUnavailable
	}
	ctx = nonNilExportRuntimeContext(ctx)
	var owner runtimeArchiveMemberOwnerRole
	result := adapter.db.WithContext(ctx).Table("users").Select("role").Where("id = ?", request.OwnerUserID).Limit(1).Scan(&owner)
	if result.Error != nil {
		return content.AuthorizedAsset{}, fmt.Errorf("resolve archive member owner: %w", result.Error)
	}
	if result.RowsAffected != 1 || (owner.Role != "admin" && owner.Role != "operator") {
		return content.AuthorizedAsset{}, backupasset.ErrForbidden
	}
	ref := backupasset.AssetRef{RecoveryPointID: request.RecoveryPointID, EntryID: request.EntryID}
	asset, err := adapter.authorize.Authorize(
		ctx, content.DeliveryActor{UserID: request.OwnerUserID, Role: owner.Role}, ref, content.DeliveryPreview,
	)
	if err != nil {
		return content.AuthorizedAsset{}, err
	}
	if asset.Ref != ref || asset.CatalogGenerationID != request.CatalogGenerationID ||
		asset.SourceFingerprint != request.SourceFingerprint || asset.EntryFingerprint != request.EntryFingerprint ||
		asset.ProviderCapabilityRevision <= 0 {
		return content.AuthorizedAsset{}, backupasset.ErrNotFound
	}
	return asset, nil
}

type runtimeArchiveMemberDeliveryRevoker interface {
	RevokeArchiveMember(context.Context, string, string) error
}

func newRuntimeArchiveMemberService(
	db *gorm.DB,
	now func() time.Time,
	config backupasset.ExportConfig,
	processingRuntime *managedProcessingRuntime,
	authorize processingAssetAuthorizer,
	derived *content.DerivedRepresentationResolver,
	delivery runtimeArchiveMemberDeliveryRevoker,
) (*processing.ArchiveMemberService, error) {
	if db == nil || processingRuntime == nil || authorize == nil || derived == nil || delivery == nil {
		return nil, processing.ErrArchiveMemberUnavailable
	}
	coordinator := runtimeArchiveMemberCoordinator{runtime: processingRuntime}
	indexResolver := runtimeArchiveMemberIndexResolver{db: db, resolver: derived}
	authorityResolver := runtimeArchiveMemberAuthorityResolver{db: db, authorize: authorize}
	return processing.NewArchiveMemberService(processing.ArchiveMemberServiceDependencies{
		DB: db, Coordinator: coordinator, Authorize: authorize,
		ResolveIndex: indexResolver.Resolve,
		RevalidateIndex: func(ctx context.Context, request model.BackupAssetArchiveMemberRequest) (processing.ArchiveMemberIndexBinding, error) {
			asset, err := authorityResolver.resolveAsset(ctx, request)
			if err != nil {
				return processing.ArchiveMemberIndexBinding{}, err
			}
			return indexResolver.Resolve(ctx, asset, request.IndexRevision)
		},
		ResolveAuthority:    authorityResolver.Resolve,
		ResolveRuntimeAsset: authorityResolver.resolveAsset,
		ResolveExtractCapability: func(ctx context.Context) (processing.CapabilityAdvertisement, error) {
			service := processingRuntime.currentCapabilityService()
			if service == nil {
				return processing.CapabilityAdvertisement{}, processing.ErrNotDeployed
			}
			return service.ArchiveExtractCapability(ctx)
		},
		ResolveOutput:      derived.ValidateArchiveMemberOutput,
		ResolveReadyOutput: derived.ResolveArchiveMember,
		RevokeDeliveries: func(ctx context.Context, requestID, reason string) error {
			return delivery.RevokeArchiveMember(ctx, requestID, reason)
		},
		RevokeOutput: func(ctx context.Context, artifactSetID string, reason processing.DerivedRevokeReason) error {
			processingRuntime.mu.RLock()
			lifecycle := processingRuntime.lifecycle
			processingRuntime.mu.RUnlock()
			if lifecycle == nil || processingRuntime.stopped.Load() {
				return processing.ErrArchiveMemberUnavailable
			}
			return lifecycle.RevokeSet(ctx, artifactSetID, reason)
		},
		Now: now, IdempotencyTTL: config.IdempotencyTTL, IdempotencyKeyMaxBytes: config.IdempotencyKeyMaxBytes,
	})
}

type contentTicketIssuer interface {
	Issue(context.Context, content.IssueRequest) (content.IssuedTicket, error)
}

type typedContentDeliveryBranch interface {
	MatchesDelivery(context.Context, string) (bool, error)
	Serve(context.Context, content.GatewayRequest, http.ResponseWriter) error
	RevokeSession(context.Context, string, string) error
}

type contentDeliveryServer interface {
	Serve(context.Context, content.GatewayRequest, http.ResponseWriter) error
	RevokeSession(context.Context, string, string) error
}

type contentBrokerDeliveryBranch struct {
	db     *gorm.DB
	server contentDeliveryServer
}

func newContentBrokerDeliveryBranch(
	db *gorm.DB,
	server contentDeliveryServer,
) (*contentBrokerDeliveryBranch, error) {
	if db == nil || server == nil {
		return nil, content.ErrInvalidBrokerRequest
	}
	return &contentBrokerDeliveryBranch{db: db, server: server}, nil
}

func (branch *contentBrokerDeliveryBranch) MatchesDelivery(ctx context.Context, deliveryID string) (bool, error) {
	if branch == nil || branch.db == nil || backupasset.ValidateOpaqueID(deliveryID) != nil {
		return false, nil
	}
	var count int64
	err := branch.db.WithContext(nonNilExportRuntimeContext(ctx)).Model(&model.BackupAssetDeliveryGrant{}).
		Where("delivery_id = ?", deliveryID).Limit(1).Count(&count).Error
	return count == 1, err
}

func (branch *contentBrokerDeliveryBranch) Serve(
	ctx context.Context,
	request content.GatewayRequest,
	writer http.ResponseWriter,
) error {
	if branch == nil || branch.server == nil {
		return content.ErrContentNotFound
	}
	return branch.server.Serve(ctx, request, writer)
}

func (branch *contentBrokerDeliveryBranch) RevokeSession(ctx context.Context, sessionJTI, reason string) error {
	if branch == nil || branch.server == nil {
		return content.ErrInvalidBrokerRequest
	}
	return branch.server.RevokeSession(ctx, sessionJTI, reason)
}

type optionalTypedDeliveryBranch struct {
	mu     sync.RWMutex
	branch typedContentDeliveryBranch
}

func newOptionalTypedDeliveryBranch() *optionalTypedDeliveryBranch {
	return &optionalTypedDeliveryBranch{}
}

func (branch *optionalTypedDeliveryBranch) Install(value typedContentDeliveryBranch) error {
	if branch == nil || value == nil {
		return content.ErrInvalidBrokerRequest
	}
	branch.mu.Lock()
	defer branch.mu.Unlock()
	branch.branch = value
	return nil
}

func (branch *optionalTypedDeliveryBranch) Unpublish() {
	if branch == nil {
		return
	}
	branch.mu.Lock()
	branch.branch = nil
	branch.mu.Unlock()
}

func (branch *optionalTypedDeliveryBranch) current() typedContentDeliveryBranch {
	if branch == nil {
		return nil
	}
	branch.mu.RLock()
	defer branch.mu.RUnlock()
	return branch.branch
}

func (branch *optionalTypedDeliveryBranch) MatchesDelivery(ctx context.Context, deliveryID string) (bool, error) {
	current := branch.current()
	if current == nil {
		return false, nil
	}
	return current.MatchesDelivery(ctx, deliveryID)
}

func (branch *optionalTypedDeliveryBranch) Serve(
	ctx context.Context,
	request content.GatewayRequest,
	writer http.ResponseWriter,
) error {
	current := branch.current()
	if current == nil {
		return content.ErrContentNotFound
	}
	return current.Serve(ctx, request, writer)
}

func (branch *optionalTypedDeliveryBranch) RevokeSession(ctx context.Context, sessionJTI, reason string) error {
	current := branch.current()
	if current == nil {
		return nil
	}
	return current.RevokeSession(ctx, sessionJTI, reason)
}

type contentDeliveryMux struct {
	issuer        contentTicketIssuer
	contentBranch typedContentDeliveryBranch
	exportBranch  typedContentDeliveryBranch
}

func newContentDeliveryMux(
	issuer contentTicketIssuer,
	contentBranch typedContentDeliveryBranch,
	exportBranch typedContentDeliveryBranch,
) (*contentDeliveryMux, error) {
	if issuer == nil || contentBranch == nil || exportBranch == nil {
		return nil, content.ErrInvalidBrokerRequest
	}
	return &contentDeliveryMux{issuer: issuer, contentBranch: contentBranch, exportBranch: exportBranch}, nil
}

func (mux *contentDeliveryMux) Issue(
	ctx context.Context,
	request content.IssueRequest,
) (content.IssuedTicket, error) {
	if mux == nil || mux.issuer == nil {
		return content.IssuedTicket{}, content.ErrInvalidBrokerRequest
	}
	return mux.issuer.Issue(ctx, request)
}

func (mux *contentDeliveryMux) Serve(
	ctx context.Context,
	request content.GatewayRequest,
	writer http.ResponseWriter,
) error {
	if mux == nil || mux.contentBranch == nil || mux.exportBranch == nil {
		return content.ErrContentNotFound
	}
	contentMatch, contentErr := mux.contentBranch.MatchesDelivery(ctx, request.DeliveryID)
	exportMatch, exportErr := mux.exportBranch.MatchesDelivery(ctx, request.DeliveryID)
	if contentErr != nil || exportErr != nil {
		return content.ErrContentUnavailable
	}
	if contentMatch == exportMatch {
		return content.ErrContentNotFound
	}
	if contentMatch {
		return mux.contentBranch.Serve(ctx, request, writer)
	}
	return mux.exportBranch.Serve(ctx, request, writer)
}

func (mux *contentDeliveryMux) RevokeSession(ctx context.Context, sessionJTI, reason string) error {
	if mux == nil || mux.contentBranch == nil || mux.exportBranch == nil ||
		backupasset.ValidateOpaqueID(sessionJTI) != nil ||
		(reason != "logout" && reason != "session_revoked") {
		return errContentDeliverySessionRevocation
	}
	contentErr := mux.contentBranch.RevokeSession(ctx, sessionJTI, reason)
	exportErr := mux.exportBranch.RevokeSession(ctx, sessionJTI, reason)
	if contentErr != nil || exportErr != nil {
		return errContentDeliverySessionRevocation
	}
	return nil
}

func nonNilExportRuntimeContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func (runtime *managedExportRuntime) boundedTransitionDrainContext(parent context.Context) (context.Context, context.CancelFunc) {
	parent = nonNilExportRuntimeContext(parent)
	return context.WithTimeout(parent, runtime.transitionTimeout)
}

func (runtime *managedExportRuntime) boundedDetachedRecoveryContext(parent context.Context) (context.Context, context.CancelFunc) {
	parent = nonNilExportRuntimeContext(parent)
	if budget, _ := parent.Value(featureTransitionBudgetContextKey{}).(*featureTransitionBudget); budget != nil {
		return newFeatureTransitionCleanupContext(parent)
	}
	deadline := time.Now().Add(runtime.transitionTimeout)
	if parentDeadline, ok := parent.Deadline(); ok && parentDeadline.Before(deadline) {
		deadline = parentDeadline
	}
	return context.WithDeadline(context.WithoutCancel(parent), deadline)
}
