package database

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

const (
	minimumRecoverySchemaVersion      int64 = 69
	taskRunCompatibilitySchemaVersion int64 = 72
	plainTextContentSchemaVersion     int64 = 73
)

const plainTextContentAdmissionTrigger = "trg_backup_asset_plain_text_content_downgrade_admission"

var plainTextContentSQLiteAdmissionFragments = []string{
	"before insert on schema_migrations",
	"when new.version < 73",
	"select 1 from backup_asset_delivery_grants",
	"where renderer = 'plain_text' or profile = 'text_v2'",
	"raise(abort, '000073 downgrade blocked: plain_text/text_v2 delivery grant exists')",
}

var plainTextContentPostgresAdmissionTriggerFragments = []string{
	"before insert on",
	"schema_migrations",
	"execute function",
	"backup_asset_plain_text_content_downgrade_admission()",
}

var plainTextContentPostgresAdmissionFunctionFragments = []string{
	"if new.version < 73",
	"select 1 from backup_asset_delivery_grants",
	"where renderer = 'plain_text' or profile = 'text_v2'",
	"raise exception '000073 downgrade blocked: plain_text/text_v2 delivery grant exists'",
}

var plainTextContentSQLiteCheckFragments = []string{
	"renderer in ('escaped_text', 'plain_text'",
	"profile in ('text_v1', 'text_v2'",
	"renderer = 'plain_text' and profile = 'text_v2' and range_policy = 'none'",
	"renderer in ('escaped_text', 'plain_text', 'metadata_hex')",
}

var plainTextContentPostgresConstraintFragments = map[string][]string{
	"backup_asset_delivery_grants_renderer_check":               {"plain_text"},
	"backup_asset_delivery_grants_profile_check":                {"text_v2"},
	"backup_asset_delivery_grants_renderer_product_check":       {"renderer", "plain_text", "profile", "text_v2", "range_policy", "none"},
	"backup_asset_delivery_grants_representation_product_check": {"renderer", "plain_text", "representation_source_bytes", "source_size"},
}

// ErrMigrationSchemaDrift means schema_migrations records a clean migration-69
// or newer database, but the minimum recovery schema is incomplete. The error is
// intentionally sanitized: callers can classify it with errors.Is without
// exposing SQL, DSNs, schema names, or business data.
var ErrMigrationSchemaDrift = errors.New("migration schema drift detected; startup refused")

var minimumRecoveryTables = []string{
	"backup_asset_recovery_plans",
	"backup_asset_recovery_plan_items",
	"backup_asset_recovery_preflights",
	"backup_asset_recovery_grants",
	"backup_asset_recovery_jobs",
	"backup_asset_recovery_job_items",
	"backup_asset_recovery_attempts",
	"backup_asset_recovery_checkpoints",
	"backup_asset_recovery_evidence",
	"backup_asset_recovery_result_sets",
	"backup_asset_recovery_results",
	"backup_asset_recovery_node_leases",
}

var minimumRecoveryTriggers = []struct {
	table string
	name  string
}{
	{table: "task_runs", name: "trg_backup_asset_recovery_task_runs_node_snapshot_insert"},
	{table: "task_runs", name: "trg_backup_asset_recovery_task_runs_node_snapshot_immutable"},
	{table: "schema_migrations", name: "trg_backup_asset_recovery_downgrade_admission"},
}

var taskRunCompatibilityTriggers = []struct {
	table string
	name  string
}{
	{table: "task_runs", name: "trg_backup_asset_task_runs_legacy_unknown_status_immutable"},
	{table: "schema_migrations", name: "trg_backup_asset_task_run_snapshot_compatibility_downgrade_admission"},
}

// validateMinimumRecoverySchema is read-only. It runs before any historical
// fixup and again after Up so a falsely-clean version can never authorize
// forward writes or a successful startup.
func validateMinimumRecoverySchema(db *sql.DB, dbType string, version int64) error {
	if version < minimumRecoverySchemaVersion {
		return nil
	}

	columnExists, err := migrationColumnExists(db, dbType, "task_runs", "node_id_snapshot")
	if err != nil {
		return migrationSchemaDriftError(version, "catalog_query_failed")
	}
	if !columnExists {
		return migrationSchemaDriftError(version, "missing_task_run_snapshot_column")
	}

	for _, table := range minimumRecoveryTables {
		exists, existsErr := migrationRelationExists(db, dbType, table, "table")
		if existsErr != nil {
			return migrationSchemaDriftError(version, "catalog_query_failed")
		}
		if !exists {
			return migrationSchemaDriftError(version, "missing_recovery_table")
		}
	}

	indexExists, err := migrationRelationExists(db, dbType, "idx_task_runs_node_snapshot_status", "index")
	if err != nil {
		return migrationSchemaDriftError(version, "catalog_query_failed")
	}
	if !indexExists {
		return migrationSchemaDriftError(version, "missing_task_run_snapshot_index")
	}

	for _, trigger := range minimumRecoveryTriggers {
		exists, existsErr := migrationTriggerExists(db, dbType, trigger.table, trigger.name)
		if existsErr != nil {
			return migrationSchemaDriftError(version, "catalog_query_failed")
		}
		if !exists {
			return migrationSchemaDriftError(version, "missing_recovery_trigger")
		}
	}
	if version < taskRunCompatibilitySchemaVersion {
		return nil
	}
	for _, trigger := range taskRunCompatibilityTriggers {
		exists, existsErr := migrationTriggerExists(db, dbType, trigger.table, trigger.name)
		if existsErr != nil {
			return migrationSchemaDriftError(version, "catalog_query_failed")
		}
		if !exists {
			return migrationSchemaDriftError(version, "missing_task_run_compatibility_trigger")
		}
	}
	if dbType == "postgres" {
		exists, existsErr := migrationConstraintExists(db, "task_runs", "task_runs_node_id_snapshot_compatibility")
		if existsErr != nil {
			return migrationSchemaDriftError(version, "catalog_query_failed")
		}
		if !exists {
			return migrationSchemaDriftError(version, "missing_task_run_compatibility_constraint")
		}
	}
	if version < plainTextContentSchemaVersion {
		return nil
	}

	if err := validatePlainTextContentChecks(db, dbType); err != nil {
		return migrationSchemaDriftError(version, err.Error())
	}
	if err := validatePlainTextContentAdmission(db, dbType); err != nil {
		return migrationSchemaDriftError(version, err.Error())
	}

	return nil
}

func validatePlainTextContentAdmission(db *sql.DB, dbType string) error {
	definition, err := migrationTriggerDefinition(db, dbType, "schema_migrations", plainTextContentAdmissionTrigger)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("missing_plain_text_content_admission_trigger")
		}
		return errors.New("catalog_query_failed")
	}

	fragments := plainTextContentSQLiteAdmissionFragments
	if dbType == "postgres" {
		fragments = plainTextContentPostgresAdmissionTriggerFragments
	}
	definition = normalizeMigrationDefinition(definition)
	for _, fragment := range fragments {
		if !strings.Contains(definition, fragment) {
			return errors.New("invalid_plain_text_content_admission_trigger")
		}
	}

	if dbType == "postgres" {
		functionDefinition, functionErr := migrationTriggerFunctionDefinition(
			db,
			"schema_migrations",
			plainTextContentAdmissionTrigger,
		)
		if functionErr != nil {
			if errors.Is(functionErr, sql.ErrNoRows) {
				return errors.New("invalid_plain_text_content_admission_trigger")
			}
			return errors.New("catalog_query_failed")
		}
		functionDefinition = normalizeMigrationDefinition(functionDefinition)
		for _, fragment := range plainTextContentPostgresAdmissionFunctionFragments {
			if !strings.Contains(functionDefinition, fragment) {
				return errors.New("invalid_plain_text_content_admission_trigger")
			}
		}
	}
	return nil
}

func normalizeMigrationDefinition(definition string) string {
	return strings.Join(strings.Fields(strings.ToLower(definition)), " ")
}

func validatePlainTextContentChecks(db *sql.DB, dbType string) error {
	switch dbType {
	case "sqlite":
		var definition string
		if err := db.QueryRow(
			"SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'backup_asset_delivery_grants'",
		).Scan(&definition); err != nil {
			return errors.New("catalog_query_failed")
		}
		definition = strings.ToLower(definition)
		for _, fragment := range plainTextContentSQLiteCheckFragments {
			if !strings.Contains(definition, fragment) {
				return errors.New("missing_plain_text_content_constraint")
			}
		}
	case "postgres":
		for constraint, fragments := range plainTextContentPostgresConstraintFragments {
			definition, err := migrationConstraintDefinition(db, "backup_asset_delivery_grants", constraint)
			if err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return errors.New("missing_plain_text_content_constraint")
				}
				return errors.New("catalog_query_failed")
			}
			definition = strings.ToLower(definition)
			for _, fragment := range fragments {
				if !strings.Contains(definition, fragment) {
					return errors.New("missing_plain_text_content_constraint")
				}
			}
		}
	default:
		return errors.New("catalog_query_failed")
	}
	return nil
}

func migrationSchemaDriftError(version int64, reason string) error {
	return fmt.Errorf("%w (version=%d, reason=%s); restore a verified backup or perform an audited offline repair", ErrMigrationSchemaDrift, version, reason)
}

func migrationColumnExists(db *sql.DB, dbType, table, column string) (bool, error) {
	var count int
	var err error
	switch dbType {
	case "sqlite":
		err = db.QueryRow("SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?", table, column).Scan(&count)
	case "postgres":
		err = db.QueryRow(`
			SELECT COUNT(*)
			FROM pg_catalog.pg_attribute
			WHERE attrelid = pg_catalog.to_regclass($1)
			  AND attname = $2
			  AND NOT attisdropped`, table, column).Scan(&count)
	default:
		return false, fmt.Errorf("unsupported database type")
	}
	return count == 1, err
}

func migrationRelationExists(db *sql.DB, dbType, name, kind string) (bool, error) {
	var count int
	var err error
	switch dbType {
	case "sqlite":
		err = db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = ? AND name = ?", kind, name).Scan(&count)
	case "postgres":
		var expectedKind string
		switch kind {
		case "table":
			expectedKind = "r"
		case "index":
			expectedKind = "i"
		default:
			return false, fmt.Errorf("unsupported relation kind")
		}
		err = db.QueryRow(`
			SELECT COUNT(*)
			FROM pg_catalog.pg_class
			WHERE oid = pg_catalog.to_regclass($1)
			  AND relkind = $2`, name, expectedKind).Scan(&count)
	default:
		return false, fmt.Errorf("unsupported database type")
	}
	return count == 1, err
}

func migrationTriggerExists(db *sql.DB, dbType, table, trigger string) (bool, error) {
	var count int
	var err error
	switch dbType {
	case "sqlite":
		err = db.QueryRow(
			"SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger' AND tbl_name = ? AND name = ?",
			table,
			trigger,
		).Scan(&count)
	case "postgres":
		err = db.QueryRow(`
			SELECT COUNT(*)
			FROM pg_catalog.pg_trigger
			WHERE tgrelid = pg_catalog.to_regclass($1)
			  AND tgname = $2
			  AND NOT tgisinternal`, table, trigger).Scan(&count)
	default:
		return false, fmt.Errorf("unsupported database type")
	}
	return count == 1, err
}

func migrationTriggerDefinition(db *sql.DB, dbType, table, trigger string) (string, error) {
	var definition string
	var err error
	switch dbType {
	case "sqlite":
		err = db.QueryRow(
			"SELECT sql FROM sqlite_master WHERE type = 'trigger' AND tbl_name = ? AND name = ?",
			table,
			trigger,
		).Scan(&definition)
	case "postgres":
		err = db.QueryRow(`
			SELECT pg_catalog.pg_get_triggerdef(oid)
			FROM pg_catalog.pg_trigger
			WHERE tgrelid = pg_catalog.to_regclass($1)
			  AND tgname = $2
			  AND NOT tgisinternal`, table, trigger).Scan(&definition)
	default:
		return "", fmt.Errorf("unsupported database type")
	}
	return definition, err
}

func migrationTriggerFunctionDefinition(db *sql.DB, table, trigger string) (string, error) {
	var definition string
	err := db.QueryRow(`
		SELECT pg_catalog.pg_get_functiondef(t.tgfoid)
		FROM pg_catalog.pg_trigger AS t
		WHERE t.tgrelid = pg_catalog.to_regclass($1)
		  AND t.tgname = $2
		  AND NOT t.tgisinternal`, table, trigger).Scan(&definition)
	return definition, err
}

func migrationConstraintExists(db *sql.DB, table, constraint string) (bool, error) {
	var count int
	err := db.QueryRow(`
		SELECT COUNT(*)
		FROM pg_catalog.pg_constraint
		WHERE conrelid = pg_catalog.to_regclass($1)
		  AND conname = $2`, table, constraint).Scan(&count)
	return count == 1, err
}

func migrationConstraintDefinition(db *sql.DB, table, constraint string) (string, error) {
	var definition string
	err := db.QueryRow(`
		SELECT pg_catalog.pg_get_constraintdef(oid)
		FROM pg_catalog.pg_constraint
		WHERE conrelid = pg_catalog.to_regclass($1)
		  AND conname = $2`, table, constraint).Scan(&definition)
	return definition, err
}
