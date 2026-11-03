package database

import (
	"database/sql"
	"errors"
	"strings"
)

const catalogCandidateSweepSchemaVersion int64 = 96

const catalogCandidateSweepTable = "catalog_candidate_sweeps"
const catalogCandidateSweepIndex = "idx_recovery_points_created_at_id"

const catalogCandidateSweepBoundaryCheck = `
	(after_created_at IS NULL AND after_point_id IS NULL AND high_water_created_at IS NULL AND high_water_point_id IS NULL)
	OR
	(after_created_at IS NOT NULL AND after_point_id IS NOT NULL AND high_water_created_at IS NOT NULL AND high_water_point_id IS NOT NULL
		AND (after_created_at < high_water_created_at OR (after_created_at = high_water_created_at AND after_point_id <= high_water_point_id)))`

// These are independent of the embedded migrations, so a changed or partially
// restored migration cannot silently redefine the startup admission contract.
func catalogCandidateSweepChecks(dbType string) []string {
	switch dbType {
	case "sqlite":
		return []string{
			"id = 1",
			"typeof(revision) = 'integer' AND revision >= 1 AND revision <= 9223372036854775807",
			"after_point_id IS NULL OR (typeof(after_point_id) = 'text' AND length(after_point_id) = 32 AND length(CAST(after_point_id AS BLOB)) = 32 AND after_point_id NOT GLOB '*[^0-9a-f]*')",
			"high_water_point_id IS NULL OR (typeof(high_water_point_id) = 'text' AND length(high_water_point_id) = 32 AND length(CAST(high_water_point_id AS BLOB)) = 32 AND high_water_point_id NOT GLOB '*[^0-9a-f]*')",
			catalogCandidateSweepBoundaryCheck,
		}
	case "postgres":
		return []string{
			"id = 1",
			"revision >= 1 AND revision <= 9223372036854775807",
			"after_point_id IS NULL OR after_point_id ~ '^[0-9a-f]{32}$'",
			"high_water_point_id IS NULL OR high_water_point_id ~ '^[0-9a-f]{32}$'",
			catalogCandidateSweepBoundaryCheck,
		}
	default:
		return nil
	}
}

// validateCatalogCandidateSweepSchema only reads the schema and current state.
// In particular, a missing or damaged singleton is never recreated at startup.
func validateCatalogCandidateSweepSchema(db *sql.DB, dbType string) error {
	checks := catalogCandidateSweepChecks(dbType)
	if checks == nil {
		return errors.New("catalog_query_failed")
	}
	exists, err := migrationRelationExists(db, dbType, catalogCandidateSweepTable, "table")
	if err != nil {
		return errors.New("catalog_query_failed")
	}
	if !exists {
		return errors.New("missing_catalog_candidate_sweep_table")
	}

	count, err := migrationColumnCount(db, dbType, catalogCandidateSweepTable)
	if err != nil {
		return errors.New("catalog_query_failed")
	}
	if count != 6 {
		return errors.New("invalid_catalog_candidate_sweep_columns")
	}
	for _, want := range []struct {
		name         string
		sqliteType   string
		postgresType string
		maxLength    int64
		notNull      bool
	}{
		{"id", "integer", "integer", 0, true},
		{"revision", "integer", "bigint", 0, true},
		{"after_created_at", "datetime", "timestamp with time zone", 0, false},
		{"after_point_id", "text", "character varying", 32, false},
		{"high_water_created_at", "datetime", "timestamp with time zone", 0, false},
		{"high_water_point_id", "text", "character varying", 32, false},
	} {
		got, columnErr := migrationColumnContractOf(db, dbType, catalogCandidateSweepTable, want.name)
		if columnErr != nil {
			if errors.Is(columnErr, sql.ErrNoRows) {
				return errors.New("invalid_catalog_candidate_sweep_columns")
			}
			return errors.New("catalog_query_failed")
		}
		wantType := want.sqliteType
		if dbType == "postgres" {
			wantType = want.postgresType
			if got.maxLength != want.maxLength {
				return errors.New("invalid_catalog_candidate_sweep_columns")
			}
		}
		if got.dataType != wantType || got.notNull != want.notNull || got.defaultSQL != "" {
			return errors.New("invalid_catalog_candidate_sweep_columns")
		}
	}

	primaryKey, err := migrationPrimaryKeyColumns(db, dbType, catalogCandidateSweepTable)
	if err != nil {
		return errors.New("catalog_query_failed")
	}
	if !sameMigrationIndexColumns(primaryKey, []string{"id"}) {
		return errors.New("invalid_catalog_candidate_sweep_primary_key")
	}
	foreignKeys, err := migrationForeignKeyCount(db, dbType, catalogCandidateSweepTable)
	if err != nil {
		return errors.New("catalog_query_failed")
	}
	if foreignKeys != 0 {
		return errors.New("invalid_catalog_candidate_sweep_foreign_keys")
	}

	actualChecks, err := migrationTableCheckDefinitions(db, dbType, catalogCandidateSweepTable)
	if err != nil {
		return errors.New("catalog_query_failed")
	}
	if !catalogCandidateSweepChecksExact(dbType, actualChecks, checks) {
		return errors.New("invalid_catalog_candidate_sweep_checks")
	}

	index, err := migrationIndexContractOf(db, dbType, "recovery_points", catalogCandidateSweepIndex)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("invalid_catalog_candidate_sweep_index")
		}
		return errors.New("catalog_query_failed")
	}
	if !migrationIndexUsable(index) || index.unique || strings.TrimSpace(index.predicate) != "" ||
		!sameMigrationIndexColumns(index.columns, []string{"created_at", "id"}) {
		return errors.New("invalid_catalog_candidate_sweep_index")
	}
	if dbType == "postgres" {
		var keysOnly bool
		err := db.QueryRow(`SELECT index_row.indnatts = index_row.indnkeyatts
			FROM pg_catalog.pg_index AS index_row
			JOIN pg_catalog.pg_class AS index_relation ON index_relation.oid = index_row.indexrelid
			WHERE index_row.indrelid = pg_catalog.to_regclass($1) AND index_relation.relname = $2`,
			"recovery_points", catalogCandidateSweepIndex).Scan(&keysOnly)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return errors.New("invalid_catalog_candidate_sweep_index")
			}
			return errors.New("catalog_query_failed")
		}
		if !keysOnly {
			return errors.New("invalid_catalog_candidate_sweep_index")
		}
	}
	return validateCatalogCandidateSweepState(db, checks)
}

func validateCatalogCandidateSweepState(db *sql.DB, checks []string) error {
	var count, invalid int64
	// The predicates use the engine's comparison and storage semantics, including
	// SQLite's existing DATETIME text ordering. Never round-trip boundaries in Go.
	query := `SELECT COUNT(*), COALESCE(SUM(CASE WHEN (` + strings.Join(checks, `) AND (`) +
		`) THEN 0 ELSE 1 END), 0) FROM catalog_candidate_sweeps`
	if err := db.QueryRow(query).Scan(&count, &invalid); err != nil {
		return errors.New("catalog_query_failed")
	}
	if count != 1 || invalid != 0 {
		return errors.New("invalid_catalog_candidate_sweep_state")
	}
	return nil
}

func catalogCandidateSweepChecksExact(dbType string, got []migrationCheckDefinition, want []string) bool {
	// PostgreSQL may deparse a large int8 constant as a quoted bigint literal.
	matches := migrationCheckMultisetExact(dbType, got, want)
	if !matches && dbType == "postgres" {
		alternate := append([]string(nil), want...)
		alternate[1] = strings.ReplaceAll(alternate[1], "9223372036854775807", "'9223372036854775807'::bigint")
		matches = migrationCheckMultisetExact(dbType, got, alternate)
	}
	if !matches {
		return false
	}

	// The shared comparison removes all parentheses and folds string literals.
	// Preserve boolean grouping and literal case as well: otherwise a regrouped
	// boundary check or weakened hexadecimal pattern could pass admission.
	expected := make(map[string]int, len(want))
	for _, expression := range want {
		expected[catalogCandidateSweepLogicalCheck(expression)]++
	}
	for _, check := range got {
		key := catalogCandidateSweepLogicalCheck(check.expression)
		if expected[key] == 0 {
			return false
		}
		expected[key]--
	}
	return true
}

// catalogCandidateSweepLogicalCheck canonicalizes only the small boolean
// vocabulary used by these checks; it retains AND/OR grouping and SQL literals.
func catalogCandidateSweepLogicalCheck(expression string) string {
	expression = strings.TrimSpace(expression)
	for strings.HasPrefix(expression, "(") {
		end, ok := migrationMatchingParenthesis(expression, 0)
		if !ok || end != len(expression)-1 {
			break
		}
		expression = strings.TrimSpace(expression[1:end])
	}
	for _, operator := range []string{"or", "and"} {
		if parts := catalogCandidateSweepSplitLogical(expression, operator); len(parts) > 1 {
			for index := range parts {
				parts[index] = catalogCandidateSweepLogicalCheck(parts[index])
			}
			return operator + "(" + strings.Join(parts, ",") + ")"
		}
	}
	return catalogCandidateSweepNormalizeAtom(expression)
}

func catalogCandidateSweepSplitLogical(expression, operator string) []string {
	lower := catalogCandidateSweepLowerSQL(expression)
	depth, start := 0, 0
	var parts []string
	for index := 0; index < len(expression); index++ {
		switch expression[index] {
		case '\'', '"':
			quote := expression[index]
			for index++; index < len(expression); index++ {
				if expression[index] == quote {
					if index+1 < len(expression) && expression[index+1] == quote {
						index++
						continue
					}
					break
				}
			}
		case '(':
			depth++
		case ')':
			depth--
		default:
			end := index + len(operator)
			if depth == 0 && end <= len(expression) && lower[index:end] == operator &&
				(index == 0 || !migrationSQLWordCharacter(lower[index-1])) &&
				(end == len(expression) || !migrationSQLWordCharacter(lower[end])) {
				parts = append(parts, expression[start:index])
				start = end
				index = end - 1
			}
		}
	}
	if len(parts) == 0 {
		return nil
	}
	return append(parts, expression[start:])
}

func catalogCandidateSweepNormalizeAtom(expression string) string {
	lower := catalogCandidateSweepLowerSQL(expression)
	var normalized strings.Builder
	for index := 0; index < len(expression); index++ {
		character := expression[index]
		if character == '\'' {
			start := index
			for index++; index < len(expression); index++ {
				if expression[index] == '\'' {
					if index+1 < len(expression) && expression[index+1] == '\'' {
						index++
						continue
					}
					break
				}
			}
			literal := expression[start : index+1]
			if literal == "'9223372036854775807'" {
				literal = "9223372036854775807"
			}
			normalized.WriteString(literal)
			continue
		}
		switch character {
		case ' ', '\t', '\n', '\r', '"':
			continue
		case '(':
			// PostgreSQL parenthesizes operands before adding casts. Keep
			// function-call parentheses, whose placement is meaningful.
			end, ok := migrationMatchingParenthesis(expression, index)
			if ok && (index == 0 || !migrationSQLWordCharacter(lower[index-1])) {
				inner := strings.TrimSpace(expression[index+1 : end])
				if inner != "" && strings.IndexFunc(inner, func(r rune) bool {
					return (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_'
				}) < 0 {
					normalized.WriteString(strings.ToLower(inner))
					index = end
					continue
				}
			}
		}
		if character == ':' && index+1 < len(expression) && expression[index+1] == ':' {
			rest := lower[index:]
			matchedCast := false
			for _, cast := range []string{"::character varying", "::bigint", "::integer", "::text"} {
				if strings.HasPrefix(rest, cast) {
					prefix := normalized.String()
					operandStart := len(prefix)
					for operandStart > 0 && migrationSQLWordCharacter(prefix[operandStart-1]) {
						operandStart--
					}
					operand := prefix[operandStart:]
					// PostgreSQL renders varchar comparisons as text casts and
					// may annotate numeric constants. A narrowing cast on a
					// column changes the allowed state and must not disappear.
					allowed := false
					switch cast {
					case "::text", "::character varying":
						allowed = operand == "after_point_id" ||
							operand == "high_water_point_id" ||
							strings.HasSuffix(prefix, "'^[0-9a-f]{32}$'")
					case "::bigint":
						allowed = operand == "revision" || operand == "1" || operand == "9223372036854775807"
					case "::integer":
						allowed = operand == "id" || operand == "1" || operand == "32"
					}
					if !allowed {
						return "invalid-cast:" + expression
					}
					index += len(cast) - 1
					matchedCast = true
					break
				}
			}
			if matchedCast {
				continue
			}
		}
		normalized.WriteByte(lower[index])
	}
	return normalized.String()
}

// SQL keywords and the identifiers in this contract are ASCII. Preserving all
// other bytes keeps scanner offsets valid even for a drifted Unicode literal.
func catalogCandidateSweepLowerSQL(value string) string {
	var lower strings.Builder
	lower.Grow(len(value))
	for index := range len(value) {
		character := value[index]
		if character >= 'A' && character <= 'Z' {
			character += 'a' - 'A'
		}
		lower.WriteByte(character)
	}
	return lower.String()
}
