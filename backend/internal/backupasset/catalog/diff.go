package catalog

import (
	"context"
	"database/sql/driver"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"
)

type ProviderDiffStatus string

const (
	ProviderDiffSupported     ProviderDiffStatus = "supported"
	ProviderDiffUnavailable   ProviderDiffStatus = "unavailable"
	ProviderDiffNotApplicable ProviderDiffStatus = "not_applicable"
)

type ContentEquality string

const (
	ContentEqual     ContentEquality = "equal"
	ContentDifferent ContentEquality = "different"
	ContentUnknown   ContentEquality = "unknown"
)

type DiffRequest struct {
	BaseRecoveryPointID    string
	CompareRecoveryPointID string
	BaseParentEntryID      string
	CompareParentEntryID   string
	Sort                   DiffSort
	Limit                  int
	Cursor                 string
}

type DiffPage struct {
	Items            []DiffItemDTO           `json:"items"`
	NextCursor       string                  `json:"next_cursor,omitempty"`
	ProviderEvidence ProviderDiffEvidenceDTO `json:"provider_evidence"`
}

type ProviderDiffEvidenceDTO struct {
	Status ProviderDiffStatus            `json:"status"`
	Reason *backupasset.CapabilityReason `json:"reason"`
}

type DiffSideDTO struct {
	RecoveryPointID     string                       `json:"recovery_point_id"`
	EntryID             string                       `json:"entry_id"`
	Name                string                       `json:"name"`
	EntryType           backupasset.CatalogEntryType `json:"entry_type"`
	Size                int64                        `json:"size"`
	ModifiedAt          *time.Time                   `json:"modified_at"`
	Mode                string                       `json:"mode"`
	Owner               string                       `json:"owner"`
	MIMEType            string                       `json:"mime_type"`
	FingerprintStrength FingerprintStrength          `json:"fingerprint_strength"`
}

type DiffItemDTO struct {
	Kind            DiffChangeKind  `json:"kind"`
	Base            *DiffSideDTO    `json:"base"`
	Compare         *DiffSideDTO    `json:"compare"`
	ChangedFields   []string        `json:"changed_fields"`
	ContentEquality ContentEquality `json:"content_equality"`
}

type diffRow struct {
	RelativePath string
	ChangeKind   string
	ChangeRank   int

	BaseEntryID             *string
	BaseName                *string
	BaseEntryType           *string
	BaseSize                *int64
	BaseModifiedAt          nullableDiffTime
	BaseMode                *string
	BaseOwner               *string
	BaseMIMEType            *string
	BaseFingerprint         *string
	BaseFingerprintStrength *string

	CompareEntryID             *string
	CompareName                *string
	CompareEntryType           *string
	CompareSize                *int64
	CompareModifiedAt          nullableDiffTime
	CompareMode                *string
	CompareOwner               *string
	CompareMIMEType            *string
	CompareFingerprint         *string
	CompareFingerprintStrength *string
}

type diffAnchor struct {
	relativePath string
	rank         int
	baseID       string
	compareID    string
}

type nullableDiffTime struct {
	Time  time.Time
	Valid bool
}

func (value *nullableDiffTime) Scan(raw any) error {
	if value == nil {
		return fmt.Errorf("nil diff time scanner")
	}
	switch typed := raw.(type) {
	case nil:
		value.Time = time.Time{}
		value.Valid = false
		return nil
	case time.Time:
		value.Time = typed.UTC()
		value.Valid = true
		return nil
	case string:
		return value.scanText(typed)
	case []byte:
		return value.scanText(string(typed))
	default:
		return fmt.Errorf("unsupported diff time value %T", raw)
	}
}

func (value nullableDiffTime) Value() (driver.Value, error) {
	if !value.Valid {
		return nil, nil
	}
	return value.Time.UTC(), nil
}

func (value *nullableDiffTime) scanText(raw string) error {
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05-07:00",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
	} {
		parsed, err := time.Parse(layout, raw)
		if err == nil {
			value.Time = parsed.UTC()
			value.Valid = true
			return nil
		}
	}
	return fmt.Errorf("invalid diff time")
}

func (value nullableDiffTime) pointer() *time.Time {
	if !value.Valid {
		return nil
	}
	result := value.Time.UTC()
	return &result
}

func (service *Service) Diff(ctx context.Context, scope AuthorizationScope, request DiffRequest) (DiffPage, error) {
	if request.BaseRecoveryPointID == request.CompareRecoveryPointID ||
		backupasset.ValidateOpaqueID(request.BaseRecoveryPointID) != nil ||
		backupasset.ValidateOpaqueID(request.CompareRecoveryPointID) != nil {
		return DiffPage{}, fmt.Errorf("%w: exact diff requires two distinct points", ErrInvalidCatalogContract)
	}
	if request.Sort == "" {
		request.Sort = DiffSortPathAsc
	}
	if request.Sort != DiffSortPathAsc {
		return DiffPage{}, fmt.Errorf("%w: diff sort", ErrInvalidCatalogContract)
	}
	limit, err := normalizeCatalogPageLimit(request.Limit)
	if err != nil {
		return DiffPage{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	basePoint, baseRepository, baseGeneration, err := service.authorizedActivePoint(ctx, request.BaseRecoveryPointID, scope)
	if err != nil {
		return DiffPage{}, err
	}
	comparePoint, compareRepository, compareGeneration, err := service.authorizedActivePoint(ctx, request.CompareRecoveryPointID, scope)
	if err != nil {
		return DiffPage{}, err
	}
	if baseRepository.ID != compareRepository.ID {
		return DiffPage{}, fmt.Errorf("%w: diff points belong to different repositories", ErrInvalidCatalogContract)
	}
	basePrefix, err := service.diffSubtreePrefix(ctx, baseGeneration, basePoint, request.BaseParentEntryID)
	if err != nil {
		return DiffPage{}, err
	}
	comparePrefix, err := service.diffSubtreePrefix(ctx, compareGeneration, comparePoint, request.CompareParentEntryID)
	if err != nil {
		return DiffPage{}, err
	}
	cursorScope := CursorScope{
		Endpoint: CursorEndpointDiff, Direction: CursorForward, UserID: scope.UserID, Role: scope.Role,
		Sort: string(request.Sort), RepositoryID: baseRepository.ID,
		BaseRecoveryPointID: basePoint.ID, CompareRecoveryPointID: comparePoint.ID,
		BaseGenerationID: baseGeneration.ID, CompareGenerationID: compareGeneration.ID,
		BaseParentEntryID: request.BaseParentEntryID, CompareParentEntryID: request.CompareParentEntryID,
	}
	var anchor *diffAnchor
	if strings.TrimSpace(request.Cursor) != "" {
		decoded, decodeErr := service.cursor.Decode(ctx, request.Cursor, cursorScope)
		if decodeErr != nil {
			return DiffPage{}, decodeErr
		}
		loaded, loadErr := service.loadDiffAnchor(ctx, decoded.Anchor, basePoint, comparePoint, baseGeneration, compareGeneration, basePrefix, comparePrefix)
		if loadErr != nil {
			return DiffPage{}, loadErr
		}
		anchor = &loaded
	}
	rows, err := service.queryDiffRows(ctx, basePoint, comparePoint, baseGeneration, compareGeneration, basePrefix, comparePrefix, anchor, limit+1)
	if err != nil {
		return DiffPage{}, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	page := DiffPage{
		Items:            make([]DiffItemDTO, 0, len(rows)),
		ProviderEvidence: providerDiffEvidence(basePoint, comparePoint, baseRepository),
	}
	for _, row := range rows {
		item, mapErr := diffItemFromRow(basePoint.ID, comparePoint.ID, row)
		if mapErr != nil {
			return DiffPage{}, mapErr
		}
		page.Items = append(page.Items, item)
	}
	if hasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		if last.BaseEntryID != nil {
			cursorScope.Anchor.BaseEntryID = *last.BaseEntryID
		}
		if last.CompareEntryID != nil {
			cursorScope.Anchor.CompareEntryID = *last.CompareEntryID
		}
		kind, parseErr := ParseDiffChangeKind(last.ChangeKind)
		if parseErr != nil {
			return DiffPage{}, parseErr
		}
		cursorScope.Anchor.ChangeKind = kind
		page.NextCursor, err = service.cursor.Encode(ctx, cursorScope)
		if err != nil {
			return DiffPage{}, err
		}
	}
	return page, nil
}

func (service *Service) diffSubtreePrefix(
	ctx context.Context,
	generation model.CatalogGeneration,
	point model.RecoveryPoint,
	parentEntryID string,
) (string, error) {
	if parentEntryID == "" {
		return "", nil
	}
	if backupasset.ValidateAssetRef(backupasset.AssetRef{RecoveryPointID: point.ID, EntryID: parentEntryID}) != nil {
		return "", fmt.Errorf("%w: diff subtree", backupasset.ErrNotFound)
	}
	entry, err := service.loadCatalogEntry(ctx, generation.ID, point.ID, parentEntryID)
	if err != nil || entry.EntryType != string(backupasset.CatalogEntryDirectory) {
		return "", fmt.Errorf("%w: diff subtree", backupasset.ErrNotFound)
	}
	return entry.NormalizedPath, nil
}

func (service *Service) queryDiffRows(
	ctx context.Context,
	basePoint, comparePoint model.RecoveryPoint,
	baseGeneration, compareGeneration model.CatalogGeneration,
	basePrefix, comparePrefix string,
	anchor *diffAnchor,
	limit int,
) ([]diffRow, error) {
	baseSQL, baseArgs := diffSideCTE("base", baseGeneration.ID, basePoint.ID, basePrefix)
	compareSQL, compareArgs := diffSideCTE("compare", compareGeneration.ID, comparePoint.ID, comparePrefix)
	metadataChanged := `b.entry_type <> c.entry_type OR b.name <> c.name OR b.size <> c.size OR
		(b.modified_at IS NULL) <> (c.modified_at IS NULL) OR b.modified_at <> c.modified_at OR
		b.mode <> c.mode OR b.owner <> c.owner OR b.mime_type <> c.mime_type OR
		b.fingerprint <> c.fingerprint OR b.fingerprint_strength <> c.fingerprint_strength`
	query := fmt.Sprintf(`WITH base_entries AS (%s), compare_entries AS (%s), changes AS (
		SELECT b.relative_path,
			b.entry_id AS base_entry_id, b.name AS base_name, b.entry_type AS base_entry_type,
			b.size AS base_size, b.modified_at AS base_modified_at, b.mode AS base_mode, b.owner AS base_owner,
			b.mime_type AS base_mime_type, b.fingerprint AS base_fingerprint,
			b.fingerprint_strength AS base_fingerprint_strength,
			c.entry_id AS compare_entry_id, c.name AS compare_name, c.entry_type AS compare_entry_type,
			c.size AS compare_size, c.modified_at AS compare_modified_at, c.mode AS compare_mode, c.owner AS compare_owner,
			c.mime_type AS compare_mime_type, c.fingerprint AS compare_fingerprint,
			c.fingerprint_strength AS compare_fingerprint_strength
		FROM base_entries b LEFT JOIN compare_entries c ON c.relative_path = b.relative_path
		WHERE c.entry_id IS NULL OR (%s)
		UNION ALL
		SELECT c.relative_path,
			NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL,
			c.entry_id, c.name, c.entry_type, c.size, c.modified_at, c.mode, c.owner,
			c.mime_type, c.fingerprint, c.fingerprint_strength
		FROM compare_entries c LEFT JOIN base_entries b ON b.relative_path = c.relative_path
		WHERE b.entry_id IS NULL
	), ranked_changes AS (
		SELECT changes.*,
			CASE WHEN base_entry_id IS NULL THEN 'added' WHEN compare_entry_id IS NULL THEN 'removed'
				WHEN base_entry_type <> compare_entry_type THEN 'type_changed' ELSE 'modified' END AS change_kind,
			CASE WHEN base_entry_id IS NULL THEN 2 WHEN compare_entry_id IS NULL THEN 1
				WHEN base_entry_type <> compare_entry_type THEN 4 ELSE 3 END AS change_rank
		FROM changes
	)
	SELECT * FROM ranked_changes`, baseSQL, compareSQL, metadataChanged)
	args := append(baseArgs, compareArgs...)
	collation := "BINARY"
	if service.db.Name() == "postgres" {
		collation = `"C"`
	}
	if anchor != nil {
		query += fmt.Sprintf(` WHERE (relative_path COLLATE %s > ?) OR
			(relative_path COLLATE %s = ? AND (change_rank > ? OR
			(change_rank = ? AND (COALESCE(base_entry_id, '') > ? OR
			(COALESCE(base_entry_id, '') = ? AND COALESCE(compare_entry_id, '') > ?)))))`, collation, collation)
		args = append(args, anchor.relativePath, anchor.relativePath, anchor.rank, anchor.rank, anchor.baseID, anchor.baseID, anchor.compareID)
	}
	query += fmt.Sprintf(` ORDER BY relative_path COLLATE %s ASC, change_rank ASC,
		COALESCE(base_entry_id, '') ASC, COALESCE(compare_entry_id, '') ASC LIMIT ?`, collation)
	args = append(args, limit)
	var rows []diffRow
	if err := service.db.WithContext(ctx).Raw(query, args...).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("query exact Catalog diff: %w", err)
	}
	return rows, nil
}

func diffSideCTE(_ string, generationID, pointID, prefix string) (string, []any) {
	columns := `entry_id, name, entry_type, size, modified_at, mode, owner, mime_type, fingerprint, fingerprint_strength`
	if prefix == "" {
		return fmt.Sprintf(`SELECT %s, normalized_path AS relative_path FROM catalog_entries
			WHERE generation_id = ? AND recovery_point_id = ?`, columns), []any{generationID, pointID}
	}
	start := utf8.RuneCountInString(prefix) + 2
	pattern := escapeSQLLike(prefix) + "/%"
	return fmt.Sprintf(`SELECT %s, SUBSTR(normalized_path, ?) AS relative_path FROM catalog_entries
		WHERE generation_id = ? AND recovery_point_id = ? AND normalized_path LIKE ? ESCAPE '\'`, columns),
		[]any{start, generationID, pointID, pattern}
}

func escapeSQLLike(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(value)
}

func (service *Service) loadDiffAnchor(
	ctx context.Context,
	claims CursorAnchor,
	basePoint, comparePoint model.RecoveryPoint,
	baseGeneration, compareGeneration model.CatalogGeneration,
	basePrefix, comparePrefix string,
) (diffAnchor, error) {
	anchor := diffAnchor{rank: diffKindRank(claims.ChangeKind), baseID: claims.BaseEntryID, compareID: claims.CompareEntryID}
	if anchor.rank == 0 || (anchor.baseID == "" && anchor.compareID == "") {
		return diffAnchor{}, fmt.Errorf("%w: diff cursor anchor", ErrStaleCursor)
	}
	var baseEntry *model.CatalogEntry
	var compareEntry *model.CatalogEntry
	if anchor.baseID != "" {
		entry, err := service.loadCatalogEntry(ctx, baseGeneration.ID, basePoint.ID, anchor.baseID)
		if err != nil {
			return diffAnchor{}, fmt.Errorf("%w: diff base cursor anchor", ErrStaleCursor)
		}
		baseEntry = &entry
		anchor.relativePath, err = relativeDiffPath(entry.NormalizedPath, basePrefix)
		if err != nil {
			return diffAnchor{}, err
		}
	}
	if anchor.compareID != "" {
		entry, err := service.loadCatalogEntry(ctx, compareGeneration.ID, comparePoint.ID, anchor.compareID)
		if err != nil {
			return diffAnchor{}, fmt.Errorf("%w: diff compare cursor anchor", ErrStaleCursor)
		}
		compareEntry = &entry
		relative, err := relativeDiffPath(entry.NormalizedPath, comparePrefix)
		if err != nil {
			return diffAnchor{}, err
		}
		if anchor.relativePath != "" && anchor.relativePath != relative {
			return diffAnchor{}, fmt.Errorf("%w: diff cursor paths changed", ErrStaleCursor)
		}
		anchor.relativePath = relative
	}
	if diffKindForEntries(baseEntry, compareEntry) != claims.ChangeKind {
		return diffAnchor{}, fmt.Errorf("%w: diff cursor kind changed", ErrStaleCursor)
	}
	return anchor, nil
}

func relativeDiffPath(normalizedPath, prefix string) (string, error) {
	if prefix == "" {
		if normalizedPath == "" {
			return "", fmt.Errorf("%w: empty diff path", ErrStaleCursor)
		}
		return normalizedPath, nil
	}
	required := prefix + "/"
	if !strings.HasPrefix(normalizedPath, required) || len(normalizedPath) == len(required) {
		return "", fmt.Errorf("%w: diff cursor left subtree", ErrStaleCursor)
	}
	return strings.TrimPrefix(normalizedPath, required), nil
}

func diffItemFromRow(basePointID, comparePointID string, row diffRow) (DiffItemDTO, error) {
	kind, err := ParseDiffChangeKind(row.ChangeKind)
	if err != nil {
		return DiffItemDTO{}, err
	}
	base, err := diffSideFromRow(basePointID, row.BaseEntryID, row.BaseName, row.BaseEntryType, row.BaseSize,
		row.BaseModifiedAt, row.BaseMode, row.BaseOwner, row.BaseMIMEType, row.BaseFingerprintStrength)
	if err != nil {
		return DiffItemDTO{}, err
	}
	compare, err := diffSideFromRow(comparePointID, row.CompareEntryID, row.CompareName, row.CompareEntryType, row.CompareSize,
		row.CompareModifiedAt, row.CompareMode, row.CompareOwner, row.CompareMIMEType, row.CompareFingerprintStrength)
	if err != nil {
		return DiffItemDTO{}, err
	}
	item := DiffItemDTO{Kind: kind, Base: base, Compare: compare, ChangedFields: []string{}, ContentEquality: ContentUnknown}
	if base != nil && compare != nil {
		item.ChangedFields = changedDiffFields(row)
		if base.FingerprintStrength == FingerprintStrong && compare.FingerprintStrength == FingerprintStrong &&
			row.BaseFingerprint != nil && row.CompareFingerprint != nil && *row.BaseFingerprint != "" && *row.CompareFingerprint != "" {
			if *row.BaseFingerprint == *row.CompareFingerprint {
				item.ContentEquality = ContentEqual
			} else {
				item.ContentEquality = ContentDifferent
			}
		}
	}
	return item, nil
}

func diffSideFromRow(
	pointID string,
	entryID, name, entryType *string,
	size *int64,
	modifiedAt nullableDiffTime,
	mode, owner, mimeType, strength *string,
) (*DiffSideDTO, error) {
	if entryID == nil {
		return nil, nil
	}
	if name == nil || entryType == nil || size == nil || mode == nil || owner == nil || mimeType == nil || strength == nil ||
		backupasset.ValidateAssetRef(backupasset.AssetRef{RecoveryPointID: pointID, EntryID: *entryID}) != nil {
		return nil, fmt.Errorf("%w: incomplete diff side", ErrUnknownInternalState)
	}
	typeValue := backupasset.CatalogEntryType(*entryType)
	if !validCatalogEntryType(typeValue) {
		return nil, fmt.Errorf("%w: diff entry type", ErrUnknownInternalState)
	}
	strengthValue, err := ParseFingerprintStrength(*strength)
	if err != nil {
		return nil, err
	}
	return &DiffSideDTO{
		RecoveryPointID: pointID, EntryID: *entryID, Name: *name, EntryType: typeValue, Size: *size,
		ModifiedAt: modifiedAt.pointer(), Mode: *mode, Owner: *owner, MIMEType: *mimeType, FingerprintStrength: strengthValue,
	}, nil
}

func changedDiffFields(row diffRow) []string {
	fields := make([]string, 0, 8)
	if pointerValue(row.BaseEntryType) != pointerValue(row.CompareEntryType) {
		fields = append(fields, "entry_type")
	}
	if pointerValue(row.BaseName) != pointerValue(row.CompareName) {
		fields = append(fields, "name")
	}
	if int64PointerValue(row.BaseSize) != int64PointerValue(row.CompareSize) {
		fields = append(fields, "size")
	}
	if !optionalTimesEqual(row.BaseModifiedAt, row.CompareModifiedAt) {
		fields = append(fields, "modified_at")
	}
	if pointerValue(row.BaseMode) != pointerValue(row.CompareMode) {
		fields = append(fields, "mode")
	}
	if pointerValue(row.BaseOwner) != pointerValue(row.CompareOwner) {
		fields = append(fields, "owner")
	}
	if pointerValue(row.BaseMIMEType) != pointerValue(row.CompareMIMEType) {
		fields = append(fields, "mime_type")
	}
	if pointerValue(row.BaseFingerprintStrength) != pointerValue(row.CompareFingerprintStrength) {
		fields = append(fields, "fingerprint_strength")
	}
	if pointerValue(row.BaseFingerprint) != pointerValue(row.CompareFingerprint) {
		fields = append(fields, "content")
	}
	return fields
}

func providerDiffEvidence(base, compare model.RecoveryPoint, repository model.BackupRepository) ProviderDiffEvidenceDTO {
	baseAvailability := contentAvailability(base, repository)
	compareAvailability := contentAvailability(compare, repository)
	if !baseAvailability.Available {
		return ProviderDiffEvidenceDTO{Status: ProviderDiffUnavailable, Reason: baseAvailability.Reason}
	}
	if !compareAvailability.Available {
		return ProviderDiffEvidenceDTO{Status: ProviderDiffUnavailable, Reason: compareAvailability.Reason}
	}
	baseDTO, baseErr := backupasset.ToRecoveryPointDTO(base, backupasset.VersionMode(repository.VersionMode))
	compareDTO, compareErr := backupasset.ToRecoveryPointDTO(compare, backupasset.VersionMode(repository.VersionMode))
	if baseErr != nil || compareErr != nil || !baseDTO.Capabilities.Diff || !compareDTO.Capabilities.Diff {
		return ProviderDiffEvidenceDTO{
			Status: ProviderDiffNotApplicable,
			Reason: &backupasset.CapabilityReason{Code: backupasset.CapabilityDiffUnavailable},
		}
	}
	return ProviderDiffEvidenceDTO{Status: ProviderDiffSupported}
}

func diffKindForEntries(base, compare *model.CatalogEntry) DiffChangeKind {
	switch {
	case base == nil && compare != nil:
		return DiffAdded
	case base != nil && compare == nil:
		return DiffRemoved
	case base == nil || compare == nil:
		return ""
	case base.EntryType != compare.EntryType:
		return DiffTypeChanged
	default:
		return DiffModified
	}
}

func diffKindRank(kind DiffChangeKind) int {
	switch kind {
	case DiffRemoved:
		return 1
	case DiffAdded:
		return 2
	case DiffModified:
		return 3
	case DiffTypeChanged:
		return 4
	default:
		return 0
	}
}

func pointerValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func int64PointerValue(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func optionalTimesEqual(left, right nullableDiffTime) bool {
	if !left.Valid || !right.Valid {
		return left.Valid == right.Valid
	}
	return left.Time.UTC().Equal(right.Time.UTC())
}
