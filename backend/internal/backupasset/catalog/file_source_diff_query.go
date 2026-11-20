package catalog

import (
	"context"
	"fmt"
	"strings"

	"xirang/backend/internal/model"
)

// queryFileSourceDiffRows groups the two indexed generation scans before
// comparing metadata. The cursor range is applied to both scans.
func (service *Service) queryFileSourceDiffRows(ctx context.Context, basePoint, comparePoint model.RecoveryPoint, baseGeneration, compareGeneration model.CatalogGeneration, anchor *diffAnchor, limit int, compatibleContent bool) ([]diffRow, error) {
	query, args := service.fileSourceDiffRowsQuery(basePoint, comparePoint, baseGeneration, compareGeneration, anchor, limit, compatibleContent)
	var grouped []struct {
		DiffRow      diffRow `gorm:"embedded"`
		BaseCount    int
		CompareCount int
	}
	if err := service.db.WithContext(ctx).Raw(query, args...).Scan(&grouped).Error; err != nil {
		return nil, fmt.Errorf("query file-source Catalog diff: %w", err)
	}
	rows := make([]diffRow, 0, len(grouped))
	for _, row := range grouped {
		if row.BaseCount > 1 || row.CompareCount > 1 {
			return nil, fmt.Errorf("%w: duplicate comparison path", ErrInvalidCatalogContract)
		}
		rows = append(rows, row.DiffRow)
	}
	return rows, nil
}

func (service *Service) fileSourceDiffRowsQuery(basePoint, comparePoint model.RecoveryPoint, baseGeneration, compareGeneration model.CatalogGeneration, anchor *diffAnchor, limit int, compatibleContent bool) (string, []any) {
	collation := "BINARY"
	if service.db.Name() == "postgres" {
		collation = `"C"`
	}
	columns := []string{"entry_id", "name", "entry_type", "size", "modified_at", "mode", "owner", "mime_type", "fingerprint", "fingerprint_strength"}
	aggregates := make([]string, 0, len(columns)*2)
	for _, side := range []struct {
		value  int
		prefix string
	}{{0, "base"}, {1, "compare"}} {
		for _, column := range columns {
			aggregates = append(aggregates, fmt.Sprintf("MAX(%s) FILTER (WHERE side = %d) AS %s_%s", column, side.value, side.prefix, column))
		}
	}
	input := `SELECT %d AS side, normalized_path, entry_id, name, entry_type, size, modified_at, mode, owner, mime_type, fingerprint, fingerprint_strength FROM catalog_entries WHERE generation_id = ? AND recovery_point_id = ?`
	args := []any{baseGeneration.ID, basePoint.ID}
	baseSQL := fmt.Sprintf(input, 0)
	compareSQL := fmt.Sprintf(input, 1)
	if anchor != nil {
		baseSQL += fmt.Sprintf(" AND normalized_path COLLATE %s > ?", collation)
		args = append(args, anchor.relativePath)
	}
	args = append(args, compareGeneration.ID, comparePoint.ID)
	if anchor != nil {
		compareSQL += fmt.Sprintf(" AND normalized_path COLLATE %s > ?", collation)
		args = append(args, anchor.relativePath)
	}
	changed := `base_entry_id IS NULL OR compare_entry_id IS NULL OR base_entry_type <> compare_entry_type OR base_name <> compare_name OR base_size <> compare_size OR (base_modified_at IS NULL) <> (compare_modified_at IS NULL) OR base_modified_at <> compare_modified_at OR base_mode <> compare_mode OR base_owner <> compare_owner OR base_mime_type <> compare_mime_type`
	if compatibleContent {
		valid := func(column string) string {
			if service.db.Name() == "postgres" {
				return column + ` ~ '^[0-9a-f]{64}$'`
			}
			return `length(` + column + `) = 64 AND ` + column + ` NOT GLOB '*[^0-9a-f]*'`
		}
		changed += ` OR (base_entry_type = 'file' AND compare_entry_type = 'file' AND base_fingerprint_strength = 'strong' AND compare_fingerprint_strength = 'strong' AND ` + valid("base_fingerprint") + ` AND ` + valid("compare_fingerprint") + ` AND base_fingerprint <> compare_fingerprint)`
	}
	query := fmt.Sprintf(`WITH inputs AS (%s UNION ALL %s), pairs AS (
		SELECT normalized_path COLLATE %s AS relative_path, %s,
		COUNT(*) FILTER (WHERE side = 0) AS base_count, COUNT(*) FILTER (WHERE side = 1) AS compare_count
		FROM inputs GROUP BY normalized_path COLLATE %s
	), changes AS (
		SELECT pairs.*, CASE WHEN base_entry_id IS NULL THEN 'added' WHEN compare_entry_id IS NULL THEN 'removed' WHEN base_entry_type <> compare_entry_type THEN 'type_changed' ELSE 'modified' END AS change_kind,
		CASE WHEN base_entry_id IS NULL THEN 2 WHEN compare_entry_id IS NULL THEN 1 WHEN base_entry_type <> compare_entry_type THEN 4 ELSE 3 END AS change_rank
		FROM pairs WHERE base_count > 1 OR compare_count > 1 OR (%s)
	) SELECT * FROM changes ORDER BY relative_path COLLATE %s ASC LIMIT ?`, baseSQL, compareSQL, collation, strings.Join(aggregates, ", "), collation, changed, collation)
	return query, append(args, limit)
}
