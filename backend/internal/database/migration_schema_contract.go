package database

import (
	"database/sql"
	"errors"
	"fmt"
)

const (
	minimumRecoverySchemaVersion      int64 = 69
	taskRunCompatibilitySchemaVersion int64 = 72
)

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

func migrationConstraintExists(db *sql.DB, table, constraint string) (bool, error) {
	var count int
	err := db.QueryRow(`
		SELECT COUNT(*)
		FROM pg_catalog.pg_constraint
		WHERE conrelid = pg_catalog.to_regclass($1)
		  AND conname = $2`, table, constraint).Scan(&count)
	return count == 1, err
}
