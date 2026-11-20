package catalog

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/database"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type FileSourceComparisonState string

const (
	FileSourceComparisonEligible    FileSourceComparisonState = "eligible"
	FileSourceComparisonIndexing    FileSourceComparisonState = "indexing"
	FileSourceComparisonUnavailable FileSourceComparisonState = "unavailable"
	FileSourceComparisonIneligible  FileSourceComparisonState = "ineligible"
)

type FileSourceDiffRequest struct {
	BaseRecoveryPointID    string `json:"base_recovery_point_id" binding:"required"`
	CompareRecoveryPointID string `json:"compare_recovery_point_id" binding:"required"`
	Limit                  int    `json:"limit,omitempty"`
	Cursor                 string `json:"cursor,omitempty"`
}

type FileSourceDiffSide struct {
	RecoveryPointID      string                       `json:"recovery_point_id" binding:"required"`
	EntryID              string                       `json:"entry_id" binding:"required"`
	Name                 string                       `json:"name" binding:"required"`
	EntryType            backupasset.CatalogEntryType `json:"entry_type" binding:"required"`
	Size                 int64                        `json:"size" binding:"required"`
	ModifiedAt           *time.Time                   `json:"modified_at" binding:"required" extensions:"x-nullable"`
	FingerprintStrength  FingerprintStrength          `json:"fingerprint_strength" binding:"required"`
	Breadcrumb           []BreadcrumbDTO              `json:"breadcrumb" binding:"required"`
	BreadcrumbTruncated  bool                         `json:"breadcrumb_truncated,omitempty"`
	OmittedAncestorNames []string                     `json:"omitted_ancestor_names,omitempty"`
}

type FileSourceDiffItem struct {
	Kind            DiffChangeKind      `json:"kind" binding:"required"`
	Base            *FileSourceDiffSide `json:"base" binding:"required" extensions:"x-nullable"`
	Compare         *FileSourceDiffSide `json:"compare" binding:"required" extensions:"x-nullable"`
	ChangedFields   []string            `json:"changed_fields" binding:"required"`
	ContentEquality ContentEquality     `json:"content_equality" binding:"required" enums:"equal,different,unknown"`
}

type FileSourceDiffPage struct {
	BaseRecoveryPointID        string                 `json:"base_recovery_point_id" binding:"required"`
	CompareRecoveryPointID     string                 `json:"compare_recovery_point_id" binding:"required"`
	BaseContentAvailability    ContentAvailabilityDTO `json:"base_content_availability" binding:"required"`
	CompareContentAvailability ContentAvailabilityDTO `json:"compare_content_availability" binding:"required"`
	Items                      []FileSourceDiffItem   `json:"items" binding:"required"`
	NextCursor                 string                 `json:"next_cursor,omitempty"`
}

type fileSourceComparisonControl struct {
	state      FileSourceComparisonState
	generation model.CatalogGeneration
	manifest   model.RecoveryPointManifest
}

// loadFileSourceComparisonControls is shared by the selector and exact comparison.
// It reads only the supplied points and does not contact a provider.
func (service *Service) loadFileSourceComparisonControls(ctx context.Context, points []model.RecoveryPoint) (map[string]fileSourceComparisonControl, error) {
	result := make(map[string]fileSourceComparisonControl, len(points))
	if len(points) == 0 {
		return result, nil
	}
	ids := make([]string, 0, len(points))
	for _, point := range points {
		ids = append(ids, point.ID)
	}
	var active []model.CatalogGeneration
	if err := service.db.WithContext(ctx).Where("recovery_point_id IN ? AND is_active = ?", ids, true).Find(&active).Error; err != nil {
		return nil, fmt.Errorf("load comparison generations: %w", err)
	}
	generations := make(map[string]model.CatalogGeneration, len(active))
	for _, generation := range active {
		if _, exists := generations[generation.RecoveryPointID]; exists {
			return nil, fmt.Errorf("%w: duplicate active generation", ErrUnknownInternalState)
		}
		generations[generation.RecoveryPointID] = generation
	}
	var manifests []model.RecoveryPointManifest
	if err := service.db.WithContext(ctx).Model(&model.RecoveryPointManifest{}).Select("id", "recovery_point_id", "revision", "digest_algorithm", "digest", "completeness", "entry_count", "is_active").Where("recovery_point_id IN ? AND is_active = ?", ids, true).Scan(&manifests).Error; err != nil {
		return nil, fmt.Errorf("load comparison manifests: %w", err)
	}
	manifestByPoint := make(map[string]model.RecoveryPointManifest, len(manifests))
	for _, manifest := range manifests {
		if _, exists := manifestByPoint[manifest.RecoveryPointID]; exists {
			return nil, fmt.Errorf("%w: duplicate active manifest", ErrUnknownInternalState)
		}
		manifestByPoint[manifest.RecoveryPointID] = manifest
	}
	var attempts []struct{ RecoveryPointID string }
	if err := service.db.WithContext(ctx).Table("recovery_point_lifecycle_attempts").Select("DISTINCT recovery_point_id").
		Where("recovery_point_id IN ? AND phase <> 'complete'", ids).Scan(&attempts).Error; err != nil {
		return nil, fmt.Errorf("load comparison lifecycle admission: %w", err)
	}
	blocked := make(map[string]bool, len(attempts))
	for _, attempt := range attempts {
		blocked[attempt.RecoveryPointID] = true
	}
	for _, point := range points {
		control := fileSourceComparisonControl{state: FileSourceComparisonIneligible}
		semantics := backupasset.PointVersionSemantics(point.Semantics)
		if semantics != backupasset.PointNativeSnapshot && semantics != backupasset.PointXirangManifest && semantics != backupasset.PointImportedBaseline ||
			(point.State != string(backupasset.RecoveryPointCommitted) && point.State != string(backupasset.RecoveryPointDegraded)) ||
			point.PhysicalAvailability == string(backupasset.PhysicalMissing) || blocked[point.ID] {
			result[point.ID] = control
			continue
		}
		generation, exists := generations[point.ID]
		if !exists {
			control.state = FileSourceComparisonIndexing
		} else if manifest, exists := manifestByPoint[point.ID]; !exists || generation.State != string(GenerationComplete) ||
			point.SourceFingerprint == "" || generation.SourceFingerprint != point.SourceFingerprint || generation.Generation <= 0 ||
			generation.ManifestID == nil || *generation.ManifestID != manifest.ID || backupasset.ValidateOpaqueID(manifest.ID) != nil ||
			manifest.Completeness != string(backupasset.ManifestComplete) || manifest.DigestAlgorithm != "sha256" ||
			!validFileSourceDigest(point.ManifestDigest) || manifest.Digest != point.ManifestDigest || manifest.EntryCount != point.EntryCount ||
			generation.ExpectedEntryCount != point.EntryCount || generation.WrittenEntryCount != point.EntryCount ||
			generation.ExpectedDigest != point.ManifestDigest || !validFileSourceDigest(generation.WrittenDigest) ||
			backupasset.ValidateOpaqueID(generation.ID) != nil {
			control.state = FileSourceComparisonUnavailable
		} else {
			control.state, control.generation, control.manifest = FileSourceComparisonEligible, generation, manifest
		}
		result[point.ID] = control
	}
	return result, nil
}

func (service *Service) DiffFileSource(ctx context.Context, setID string, scope AuthorizationScope, request FileSourceDiffRequest) (FileSourceDiffPage, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if backupasset.ValidateOpaqueID(request.BaseRecoveryPointID) != nil || backupasset.ValidateOpaqueID(request.CompareRecoveryPointID) != nil ||
		request.BaseRecoveryPointID == request.CompareRecoveryPointID || request.Limit < 0 || request.Limit > maxCatalogPageLimit ||
		len(request.Cursor) > maxCursorTokenBytes || request.Cursor != strings.TrimSpace(request.Cursor) {
		return FileSourceDiffPage{}, fmt.Errorf("%w: comparison request", ErrInvalidCursor)
	}
	if backupasset.ValidateOpaqueID(setID) != nil {
		return FileSourceDiffPage{}, fmt.Errorf("%w: file-source set", backupasset.ErrNotFound)
	}
	if err := service.ensureComparisonFeatureEnabled(ctx); err != nil {
		return FileSourceDiffPage{}, err
	}
	if service.db == nil || service.ownership == nil || service.cursor == nil || service.identityKeys == nil {
		return FileSourceDiffPage{}, backupasset.ErrInvalidState
	}
	if err := ValidateAuthorizationScope(scope); err != nil {
		return FileSourceDiffPage{}, err
	}
	limit, err := normalizeCatalogPageLimit(request.Limit)
	if err != nil {
		return FileSourceDiffPage{}, err
	}
	material, err := service.identityKeys.Active(ctx, backupasset.KeyDomainEntryIdentity)
	if err != nil {
		return FileSourceDiffPage{}, fmt.Errorf("%w: comparison identity key: %w", ErrCatalogUnavailable, err)
	}
	if material.Domain != backupasset.KeyDomainEntryIdentity || material.State != backupasset.DomainKeyActive || len(material.Key) < 32 {
		return FileSourceDiffPage{}, ErrCatalogUnavailable
	}
	var claims *CursorScope
	if request.Cursor != "" {
		decoded, decodeErr := service.cursor.decodeAuthenticatedMode(ctx, request.Cursor, true)
		if decodeErr != nil {
			return FileSourceDiffPage{}, decodeErr
		}
		if decoded.Endpoint != CursorEndpointFileSourceDiff || decoded.UserID != scope.UserID || decoded.Role != scope.Role || decoded.BackupSetID != setID || decoded.BaseRecoveryPointID != request.BaseRecoveryPointID || decoded.CompareRecoveryPointID != request.CompareRecoveryPointID {
			return FileSourceDiffPage{}, ErrStaleCursor
		}
		claims = &decoded
	}
	var nextScope *CursorScope
	var page FileSourceDiffPage
	err = database.WithReadonlySnapshot(ctx, service.db, func(tx *gorm.DB) error {
		snapshot := *service
		snapshot.db = tx.Session(&gorm.Session{Logger: logger.Discard})
		snapshot.ownership = &Ownership{db: snapshot.db}
		var err error
		page, err = snapshot.diffFileSourceSnapshot(ctx, setID, scope, request, limit, material.Key, claims, &nextScope)
		return err
	})
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return FileSourceDiffPage{}, ErrCatalogUnavailable
	}
	if err != nil {
		return FileSourceDiffPage{}, err
	}
	if nextScope != nil {
		page.NextCursor, err = service.cursor.encodeMode(ctx, *nextScope, true)
		if err != nil {
			return FileSourceDiffPage{}, err
		}
	}
	return page, nil
}

func (service *Service) diffFileSourceSnapshot(ctx context.Context, setID string, scope AuthorizationScope, request FileSourceDiffRequest, limit int, key []byte, claims *CursorScope, nextScope **CursorScope) (FileSourceDiffPage, error) {
	ids := []string{request.BaseRecoveryPointID, request.CompareRecoveryPointID}
	visible, err := service.ownership.AuthorizedPointIDs(ctx, scope, ids)
	if err != nil {
		return FileSourceDiffPage{}, err
	}
	if len(visible) != 2 {
		if request.Cursor != "" {
			var count int64
			if err := service.db.WithContext(ctx).Model(&model.RecoveryPoint{}).Where("id IN ?", ids).Count(&count).Error; err != nil {
				return FileSourceDiffPage{}, err
			}
			if count != 2 {
				return FileSourceDiffPage{}, ErrStaleCursor
			}
		}
		return FileSourceDiffPage{}, fmt.Errorf("%w: comparison points", backupasset.ErrNotFound)
	}
	var points []model.RecoveryPoint
	if err := service.db.WithContext(ctx).Model(&model.RecoveryPoint{}).Select("id", "repository_id", "producing_task_id", "producing_task_run_id", "producing_task_name_snapshot",
		"producing_node_id_snapshot", "producing_node_name_snapshot", "semantics", "state", "captured_at", "committed_at", "entry_count",
		"logical_bytes", "physical_availability", "created_at", "lineage_json", "consistency_json", "source_fingerprint", "capability_revision", "manifest_digest", "point_revision").
		Where("id IN ?", ids).Scan(&points).Error; err != nil {
		return FileSourceDiffPage{}, fmt.Errorf("load comparison points: %w", err)
	}
	if len(points) != 2 {
		return FileSourceDiffPage{}, fmt.Errorf("%w: comparison points", backupasset.ErrNotFound)
	}
	byID := make(map[string]model.RecoveryPoint, 2)
	for _, point := range points {
		byID[point.ID] = point
	}
	links, err := service.loadFileSourceLinks(ctx, points)
	if err != nil {
		return FileSourceDiffPage{}, err
	}
	repositories, err := service.loadFileSourceRepositories(ctx, points)
	if err != nil {
		return FileSourceDiffPage{}, err
	}
	for _, point := range points {
		repository, exists := repositories[point.RepositoryID]
		if !exists {
			return FileSourceDiffPage{}, fmt.Errorf("%w: comparison points", backupasset.ErrNotFound)
		}
		if !retainedFileSourcePoint(point) {
			if request.Cursor != "" {
				return FileSourceDiffPage{}, ErrStaleCursor
			}
			return FileSourceDiffPage{}, fmt.Errorf("%w: comparison points", backupasset.ErrNotFound)
		}
		lineage, err := validateFileSourcePoint(point, repository, links)
		if errors.Is(err, errFileSourceLineageUnproven) {
			return FileSourceDiffPage{}, fmt.Errorf("%w: comparison points", backupasset.ErrNotFound)
		}
		if err != nil {
			return FileSourceDiffPage{}, err
		}
		coordinates, _, err := fileSourceSetCoordinates(lineage)
		if err != nil || deriveBackupSetID(key, coordinates) != setID {
			return FileSourceDiffPage{}, fmt.Errorf("%w: comparison points", backupasset.ErrNotFound)
		}
	}
	controls, err := service.loadFileSourceComparisonControls(ctx, points)
	if err != nil {
		return FileSourceDiffPage{}, err
	}
	base, compare := byID[ids[0]], byID[ids[1]]
	baseControl, compareControl := controls[base.ID], controls[compare.ID]
	if baseControl.state != FileSourceComparisonEligible || compareControl.state != FileSourceComparisonEligible {
		if request.Cursor != "" || baseControl.state == FileSourceComparisonIneligible || compareControl.state == FileSourceComparisonIneligible {
			return FileSourceDiffPage{}, ErrStaleCursor
		}
		return FileSourceDiffPage{}, ErrCatalogUnavailable
	}
	baseAvailability, err := fileSourceComparisonAvailability(base, repositories[base.RepositoryID])
	if err != nil {
		return FileSourceDiffPage{}, err
	}
	compareAvailability, err := fileSourceComparisonAvailability(compare, repositories[compare.RepositoryID])
	if err != nil {
		return FileSourceDiffPage{}, err
	}
	digest, err := fileSourceProjectionDigest("comparison", struct {
		Base                                                             model.RecoveryPoint
		Compare                                                          model.RecoveryPoint
		BaseRepository                                                   model.BackupRepository
		CompareRepository                                                model.BackupRepository
		BaseGeneration                                                   model.CatalogGeneration
		CompareGeneration                                                model.CatalogGeneration
		BaseManifest, CompareManifest                                    model.RecoveryPointManifest
		BaseLineage, CompareLineage, BaseConsistency, CompareConsistency string
		BaseCapabilities, CompareCapabilities                            string
		BaseAvailability, CompareAvailability                            ContentAvailabilityDTO
	}{base, compare, repositories[base.RepositoryID], repositories[compare.RepositoryID], baseControl.generation, compareControl.generation, baseControl.manifest, compareControl.manifest,
		base.LineageJSON, compare.LineageJSON, base.ConsistencyJSON, compare.ConsistencyJSON,
		repositories[base.RepositoryID].CapabilitiesJSON, repositories[compare.RepositoryID].CapabilitiesJSON, baseAvailability, compareAvailability})
	if err != nil {
		return FileSourceDiffPage{}, err
	}
	cursorScope := CursorScope{Endpoint: CursorEndpointFileSourceDiff, Direction: CursorForward, UserID: scope.UserID, Role: scope.Role,
		Sort: string(DiffSortPathAsc), BackupSetID: setID, ProjectionDigest: digest, BaseRecoveryPointID: base.ID, CompareRecoveryPointID: compare.ID,
		BaseGenerationID: baseControl.generation.ID, CompareGenerationID: compareControl.generation.ID}
	var anchor *diffAnchor
	if claims != nil {
		expected, err := cursorClaimsFromScope(cursorScope, false)
		if err != nil {
			return FileSourceDiffPage{}, err
		}
		if !cursorClaimsMatch(cursorClaims(*claims), expected) {
			return FileSourceDiffPage{}, ErrStaleCursor
		}
		loaded, err := service.loadDiffAnchorMode(ctx, claims.Anchor, base, compare, baseControl.generation, compareControl.generation, "", "", true)
		if err != nil {
			return FileSourceDiffPage{}, err
		}
		anchor = &loaded
	}
	compatibleContent := comparisonSHA256Provider(base, repositories[base.RepositoryID]) && comparisonSHA256Provider(compare, repositories[compare.RepositoryID])
	rows, err := service.queryFileSourceDiffRows(ctx, base, compare, baseControl.generation, compareControl.generation, anchor, limit+1, compatibleContent)
	if err != nil {
		return FileSourceDiffPage{}, err
	}
	more := len(rows) > limit
	if more {
		rows = rows[:limit]
	}
	baseCrumbs, err := service.loadComparisonBreadcrumbs(ctx, base.ID, baseControl.generation.ID, rows, true)
	if err != nil {
		return FileSourceDiffPage{}, err
	}
	compareCrumbs, err := service.loadComparisonBreadcrumbs(ctx, compare.ID, compareControl.generation.ID, rows, false)
	if err != nil {
		return FileSourceDiffPage{}, err
	}
	page := FileSourceDiffPage{BaseRecoveryPointID: base.ID, CompareRecoveryPointID: compare.ID,
		BaseContentAvailability: baseAvailability, CompareContentAvailability: compareAvailability, Items: make([]FileSourceDiffItem, 0, len(rows))}
	for _, row := range rows {
		item, err := diffItemFromRow(base.ID, compare.ID, row)
		if err != nil {
			return FileSourceDiffPage{}, err
		}
		item.ContentEquality = ContentUnknown
		item.ChangedFields = comparisonChangedFields(row, compatibleContent)
		if compatibleContent && item.Base != nil && item.Compare != nil && item.Base.EntryType == backupasset.CatalogEntryFile && item.Compare.EntryType == backupasset.CatalogEntryFile &&
			item.Base.FingerprintStrength == FingerprintStrong && item.Compare.FingerprintStrength == FingerprintStrong &&
			validFileSourceDigest(pointerValue(row.BaseFingerprint)) && validFileSourceDigest(pointerValue(row.CompareFingerprint)) {
			if pointerValue(row.BaseFingerprint) == pointerValue(row.CompareFingerprint) {
				item.ContentEquality = ContentEqual
			} else {
				item.ContentEquality = ContentDifferent
			}
		}
		out := FileSourceDiffItem{Kind: item.Kind, ChangedFields: item.ChangedFields, ContentEquality: item.ContentEquality}
		if item.Base != nil {
			out.Base = comparisonSide(item.Base, baseCrumbs[item.Base.EntryID])
		}
		if item.Compare != nil {
			out.Compare = comparisonSide(item.Compare, compareCrumbs[item.Compare.EntryID])
		}
		page.Items = append(page.Items, out)
	}
	if more {
		last := rows[len(rows)-1]
		cursorScope.Anchor.BaseEntryID = pointerValue(last.BaseEntryID)
		cursorScope.Anchor.CompareEntryID = pointerValue(last.CompareEntryID)
		cursorScope.Anchor.ChangeKind = DiffChangeKind(last.ChangeKind)
		*nextScope = &cursorScope
	}
	return page, nil
}

func comparisonChangedFields(row diffRow, compatibleContent bool) []string {
	if row.BaseEntryID == nil || row.CompareEntryID == nil {
		return []string{}
	}
	fields := changedDiffFields(row)
	result := make([]string, 0, len(fields))
	for _, field := range fields {
		if field == "fingerprint_strength" || field == "content" {
			continue
		}
		result = append(result, field)
	}
	if compatibleContent && pointerValue(row.BaseEntryType) == "file" && pointerValue(row.CompareEntryType) == "file" &&
		pointerValue(row.BaseFingerprintStrength) == "strong" && pointerValue(row.CompareFingerprintStrength) == "strong" &&
		validFileSourceDigest(pointerValue(row.BaseFingerprint)) && validFileSourceDigest(pointerValue(row.CompareFingerprint)) &&
		pointerValue(row.BaseFingerprint) != pointerValue(row.CompareFingerprint) {
		result = append(result, "content")
	}
	return result
}

func comparisonSHA256Provider(point model.RecoveryPoint, repository model.BackupRepository) bool {
	semantics := backupasset.PointVersionSemantics(point.Semantics)
	provider := backupasset.ProviderKind(repository.ProviderKind)
	return (semantics == backupasset.PointXirangManifest || semantics == backupasset.PointImportedBaseline) &&
		(provider == backupasset.ProviderRclone || provider == backupasset.ProviderRsync)
}

func fileSourceComparisonAvailability(point model.RecoveryPoint, repository model.BackupRepository) (ContentAvailabilityDTO, error) {
	availability := fileSourceContentAvailability(point, repository)
	if availability.Available {
		dto, err := backupasset.ToRepositoryDTO(repository)
		if err != nil {
			return ContentAvailabilityDTO{}, fmt.Errorf("%w: file-source repository capabilities", ErrUnknownInternalState)
		}
		if !dto.Capabilities.OpenSequential {
			return ContentAvailabilityDTO{Reason: &backupasset.CapabilityReason{Code: backupasset.CapabilitySequentialReadUnavailable}}, nil
		}
	}
	return availability, nil
}

type comparisonBreadcrumb struct {
	items        []BreadcrumbDTO
	truncated    bool
	omittedNames []string
}

func comparisonSide(side *DiffSideDTO, breadcrumb comparisonBreadcrumb) *FileSourceDiffSide {
	return &FileSourceDiffSide{RecoveryPointID: side.RecoveryPointID, EntryID: side.EntryID, Name: side.Name,
		EntryType: side.EntryType, Size: side.Size, ModifiedAt: side.ModifiedAt,
		FingerprintStrength: side.FingerprintStrength, Breadcrumb: breadcrumb.items, BreadcrumbTruncated: breadcrumb.truncated,
		OmittedAncestorNames: breadcrumb.omittedNames}
}

// Load the selected entries and at most 257 parents. The final parent is a
// sentinel proving truncation; only the nearest 256 parents carry references.
func (service *Service) loadComparisonBreadcrumbs(ctx context.Context, pointID, generationID string, rows []diffRow, base bool) (map[string]comparisonBreadcrumb, error) {
	ids := make([]string, 0, len(rows))
	componentsByID := make(map[string][]string, len(rows))
	for _, row := range rows {
		id := row.CompareEntryID
		if base {
			id = row.BaseEntryID
		}
		if id != nil {
			components, err := validateNormalizedEntryPath(row.RelativePath)
			if err != nil || !utf8.ValidString(row.RelativePath) {
				return nil, fmt.Errorf("%w: comparison ancestry path", ErrInvalidCatalogContract)
			}
			ids = append(ids, *id)
			componentsByID[*id] = components
		}
	}
	result := make(map[string]comparisonBreadcrumb, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	// Keep paths out of the recursive CTE and result rows. The database checks
	// each parent/child path relationship using the indexed entry references.
	collation := "BINARY"
	if service.db.Name() == "postgres" {
		collation = `"C"`
	}
	query := fmt.Sprintf(`WITH RECURSIVE ancestry(item_id, entry_id, parent_entry_id, depth, path_matches) AS (
		SELECT entry_id, entry_id, parent_entry_id, 0, TRUE FROM catalog_entries
		WHERE generation_id = ? AND recovery_point_id = ? AND entry_id IN ?
		UNION ALL
		SELECT ancestry.item_id, parent.entry_id, parent.parent_entry_id, ancestry.depth + 1,
			(child.normalized_path COLLATE %s = (parent.normalized_path || '/' || child.name) COLLATE %s)
		FROM ancestry JOIN catalog_entries child ON child.entry_id = ancestry.entry_id
		AND child.generation_id = ? AND child.recovery_point_id = ?
		JOIN catalog_entries parent ON parent.entry_id = ancestry.parent_entry_id
		AND parent.generation_id = ? AND parent.recovery_point_id = ? WHERE ancestry.depth < ?
	) SELECT ancestry.item_id, ancestry.entry_id, entry.name, entry.entry_type, ancestry.parent_entry_id, ancestry.depth, ancestry.path_matches
	FROM ancestry JOIN catalog_entries entry ON entry.entry_id = ancestry.entry_id
	AND entry.generation_id = ? AND entry.recovery_point_id = ? ORDER BY ancestry.item_id, ancestry.depth ASC`, collation, collation)
	ancestorRows, err := service.db.WithContext(ctx).Raw(query, generationID, pointID, ids, generationID, pointID,
		generationID, pointID, maxBreadcrumbDepth+1, generationID, pointID).Rows()
	if err != nil {
		return nil, fmt.Errorf("load comparison ancestors: %w", err)
	}
	defer ancestorRows.Close()
	expectedID := make(map[string]string, len(ids))
	expectedDepth := make(map[string]int, len(ids))
	for _, id := range ids {
		expectedID[id] = id
	}
	for ancestorRows.Next() {
		var itemID, entryID, name, entryType string
		var parentID *string
		var depth int
		var pathMatches bool
		if err := ancestorRows.Scan(&itemID, &entryID, &name, &entryType, &parentID, &depth, &pathMatches); err != nil {
			return nil, fmt.Errorf("scan comparison ancestor: %w", err)
		}
		components := componentsByID[itemID]
		componentIndex := len(components) - 1 - depth
		if componentIndex < 0 || expectedDepth[itemID] != depth || expectedID[itemID] != entryID || !pathMatches ||
			backupasset.ValidateAssetRef(backupasset.AssetRef{RecoveryPointID: pointID, EntryID: entryID}) != nil || components[componentIndex] != name ||
			(depth > 0 && entryType != string(backupasset.CatalogEntryDirectory)) || !validCatalogEntryType(backupasset.CatalogEntryType(entryType)) ||
			(componentIndex == 0) != (parentID == nil) {
			return nil, fmt.Errorf("%w: comparison ancestry", ErrInvalidCatalogContract)
		}
		expectedDepth[itemID]++
		expectedID[itemID] = ""
		if parentID != nil {
			expectedID[itemID] = *parentID
		}
		crumb, exists := result[itemID]
		if !exists {
			crumb.items = []BreadcrumbDTO{}
		}
		if depth == maxBreadcrumbDepth+1 {
			crumb.truncated = true
			crumb.omittedNames = append([]string(nil), components[:len(components)-1-maxBreadcrumbDepth]...)
		} else if depth > 0 {
			crumb.items = append(crumb.items, BreadcrumbDTO{RecoveryPointID: pointID, EntryID: entryID, Name: name})
		}
		result[itemID] = crumb
	}
	if err := ancestorRows.Err(); err != nil {
		return nil, fmt.Errorf("read comparison ancestors: %w", err)
	}
	for _, id := range ids {
		crumb, ok := result[id]
		if !ok || (!crumb.truncated && expectedID[id] != "") {
			return nil, fmt.Errorf("%w: comparison missing ancestor", ErrInvalidCatalogContract)
		}
		for i, j := 0, len(crumb.items)-1; i < j; i, j = i+1, j-1 {
			crumb.items[i], crumb.items[j] = crumb.items[j], crumb.items[i]
		}
		result[id] = crumb
	}
	return result, nil
}
