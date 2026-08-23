package database

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/backupasset/recovery"
	"xirang/backend/internal/config"
	"xirang/backend/internal/model"
	gormrepo "xirang/backend/internal/repository/gorm"
	"xirang/backend/internal/secure"
	"xirang/backend/internal/settings"

	"github.com/golang-migrate/migrate/v4"
	pgxmigrate "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	sqlitemigrate "github.com/golang-migrate/migrate/v4/database/sqlite3"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/mattn/go-sqlite3"
	postgresgorm "gorm.io/driver/postgres"
	sqlitegorm "gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

const (
	backupAssetMigrationVersion        = 62
	backupAssetPublicationVersion      = 63
	backupAssetRsyncPublicationVersion = 64
	backupAssetSearchVersion           = 65
	backupAssetContentVersion          = 66
	backupAssetProcessingVersion       = 67
	backupAssetExportVersion           = 68
	backupAssetRecoveryVersion         = 69
	backupAssetLifecycleVersion        = 70
	backupAssetGAVersion               = 71
	backupAssetTaskRunCompatVersion    = 72
	recoveryEmptyDeleteSetDigest       = "3f5a5d5213612b170da6ce2f2f90775a31d4e40269bb785042589af64011b7cf"
	recoveryClaimSchedulerRowID        = "0000000000000000000000000000006a"
	recoveryTakeoverSchedulerRowID     = "0000000000000000000000000000006b"
)

type recoveryMigrationSourceResolver struct{}

func (recoveryMigrationSourceResolver) ResolveRsyncRestoreSource(
	context.Context,
	provider.RsyncRestoreSourceRef,
) (provider.RsyncRestoreSource, error) {
	return nil, errors.New("migration fixture source is unavailable")
}

var backupAssetLifecycleTables = []string{
	"backup_retention_policies",
	"recovery_point_holds",
	"recovery_point_lifecycle_attempts",
	"recovery_point_lifecycle_tombstones",
	"backup_repository_import_candidates",
	"backup_asset_purge_plans",
	"backup_asset_purge_plan_items",
	"backup_asset_config_import_refs",
}

var backupAssetGATables = []string{
	"backup_asset_installations",
	"backup_asset_inventory_runs",
	"backup_asset_repository_conflicts",
}

var backupAssetRecoveryTables = []string{
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

type backupAssetRecoveryOwnedTrigger struct {
	name          string
	table         string
	downWithTable bool
}

var backupAssetRecoveryOwnedTriggers = []backupAssetRecoveryOwnedTrigger{
	{name: "trg_backup_asset_recovery_task_runs_node_snapshot_insert", table: "task_runs"},
	{name: "trg_backup_asset_recovery_task_runs_node_snapshot_immutable", table: "task_runs"},
	{name: "trg_backup_asset_recovery_attempts_mutation_arm_monotonic", table: "backup_asset_recovery_attempts"},
	{name: "trg_backup_asset_recovery_attempts_integrity", table: "backup_asset_recovery_attempts"},
	{name: "trg_backup_asset_recovery_attempts_terminal_delete", table: "backup_asset_recovery_attempts"},
	{name: "trg_backup_asset_recovery_attempts_terminal_replay", table: "backup_asset_recovery_attempts"},
	{name: "trg_backup_asset_recovery_attempts_terminal_job_barrier", table: "backup_asset_recovery_attempts"},
	{name: "trg_backup_asset_recovery_evidence_latch_update", table: "backup_asset_recovery_evidence"},
	{name: "trg_backup_asset_recovery_evidence_latch_delete", table: "backup_asset_recovery_evidence"},
	{name: "trg_backup_asset_recovery_evidence_scheduler_update", table: "backup_asset_recovery_evidence"},
	{name: "trg_backup_asset_recovery_evidence_scheduler_delete", table: "backup_asset_recovery_evidence"},
	{name: "trg_backup_asset_recovery_evidence_receipt_insert", table: "backup_asset_recovery_evidence"},
	{name: "trg_backup_asset_recovery_evidence_receipt_update", table: "backup_asset_recovery_evidence"},
	{name: "trg_backup_asset_recovery_evidence_receipt_delete", table: "backup_asset_recovery_evidence"},
	{
		name: "trg_backup_asset_recovery_evidence_worker_insert", table: "backup_asset_recovery_evidence",
		downWithTable: true,
	},
	{name: "trg_backup_asset_recovery_grants_terminal", table: "backup_asset_recovery_grants"},
	{name: "trg_backup_asset_recovery_grants_terminal_delete", table: "backup_asset_recovery_grants"},
	{name: "trg_backup_asset_recovery_grants_terminal_replay", table: "backup_asset_recovery_grants"},
	{name: "trg_backup_asset_recovery_grants_delete_binding_insert", table: "backup_asset_recovery_grants"},
	{name: "trg_backup_asset_recovery_plans_binding_frozen", table: "backup_asset_recovery_plans"},
	{name: "trg_backup_asset_recovery_preflights_immutable", table: "backup_asset_recovery_preflights"},
	{name: "trg_backup_asset_recovery_jobs_authority_insert", table: "backup_asset_recovery_jobs"},
	{name: "trg_backup_asset_recovery_jobs_binding_immutable", table: "backup_asset_recovery_jobs"},
	{name: "trg_backup_asset_recovery_job_items_insert_binding", table: "backup_asset_recovery_job_items"},
	{name: "trg_backup_asset_recovery_job_items_binding_immutable", table: "backup_asset_recovery_job_items"},
	{name: "trg_backup_asset_recovery_job_items_projection", table: "backup_asset_recovery_job_items"},
	{name: "trg_backup_asset_recovery_checkpoints_authority_insert", table: "backup_asset_recovery_checkpoints"},
	{name: "trg_backup_asset_recovery_checkpoints_immutable", table: "backup_asset_recovery_checkpoints"},
	{name: "trg_backup_asset_recovery_checkpoints_consumed_delete", table: "backup_asset_recovery_checkpoints"},
	{name: "trg_backup_asset_recovery_checkpoints_consumed_replay", table: "backup_asset_recovery_checkpoints"},
	{name: "trg_backup_asset_recovery_jobs_workspace_cleanup_insert", table: "backup_asset_recovery_jobs"},
	{name: "trg_backup_asset_recovery_jobs_workspace_cleanup_transition", table: "backup_asset_recovery_jobs"},
	{name: "trg_backup_asset_recovery_jobs_publication_integrity", table: "backup_asset_recovery_jobs"},
	{name: "trg_backup_asset_recovery_jobs_state_transition", table: "backup_asset_recovery_jobs"},
	{name: "trg_backup_asset_recovery_attempts_publication_integrity", table: "backup_asset_recovery_attempts"},
	{name: "trg_backup_asset_recovery_result_sets_publish", table: "backup_asset_recovery_result_sets"},
	{name: "trg_backup_asset_recovery_result_sets_deadline_integrity", table: "backup_asset_recovery_result_sets"},
	{name: "trg_backup_asset_recovery_result_sets_state_transition", table: "backup_asset_recovery_result_sets"},
	{name: "trg_backup_asset_recovery_result_sets_terminal_delete", table: "backup_asset_recovery_result_sets"},
	{name: "trg_backup_asset_recovery_results_publish", table: "backup_asset_recovery_results"},
	{name: "trg_backup_asset_recovery_results_classification_immutable", table: "backup_asset_recovery_results"},
	{name: "trg_backup_asset_recovery_content_authorization_insert", table: "backup_asset_delivery_grants"},
	{name: "trg_backup_asset_recovery_content_authorization_update", table: "backup_asset_delivery_grants"},
	{name: "trg_backup_asset_recovery_content_binding_immutable", table: "backup_asset_delivery_grants"},
	{name: "trg_backup_asset_recovery_downgrade_admission", table: "schema_migrations"},
}

var backupAssetRecoverySQLiteOwnedTriggers = []backupAssetRecoveryOwnedTrigger{
	{name: "trg_backup_asset_recovery_result_sets_terminal_replay", table: "backup_asset_recovery_result_sets"},
}

func backupAssetRecoveryOwnedTriggersForEngine(engine string) []backupAssetRecoveryOwnedTrigger {
	owned := append([]backupAssetRecoveryOwnedTrigger(nil), backupAssetRecoveryOwnedTriggers...)
	if engine == "sqlite" {
		owned = append(owned, backupAssetRecoverySQLiteOwnedTriggers...)
	}
	return owned
}

var backupAssetRecoveryOwnedPostgresFunctions = []string{
	"backup_asset_recovery_task_run_node_snapshot_guard",
	"backup_asset_recovery_attempt_mutation_arm_monotonic",
	"backup_asset_recovery_attempt_integrity_guard",
	"backup_asset_recovery_attempt_terminal_delete_guard",
	"backup_asset_recovery_attempt_terminal_job_barrier_guard",
	"backup_asset_recovery_latch_immutable",
	"backup_asset_recovery_scheduler_state_guard",
	"backup_asset_recovery_receipt_insert_guard",
	"backup_asset_recovery_receipt_immutable",
	"backup_asset_recovery_grant_terminal_guard",
	"backup_asset_recovery_grant_delete_binding_guard",
	"backup_asset_recovery_frozen_product_guard",
	"backup_asset_recovery_plan_binding_guard",
	"backup_asset_recovery_job_authority_insert_guard",
	"backup_asset_recovery_checkpoint_authority_insert_guard",
	"backup_asset_recovery_checkpoint_consumed_replay_guard",
	"backup_asset_recovery_job_workspace_cleanup_insert_guard",
	"backup_asset_recovery_job_workspace_cleanup_transition_guard",
	"backup_asset_recovery_job_publication_integrity_guard",
	"backup_asset_recovery_job_state_transition_guard",
	"backup_asset_recovery_job_item_insert_binding_guard",
	"backup_asset_recovery_job_item_projection_guard",
	"backup_asset_recovery_attempt_publication_integrity_guard",
	"backup_asset_recovery_result_set_publish_guard",
	"backup_asset_recovery_result_set_deadline_integrity_guard",
	"backup_asset_recovery_result_set_state_transition_guard",
	"backup_asset_recovery_result_set_terminal_delete_guard",
	"backup_asset_recovery_result_publish_guard",
	"backup_asset_recovery_result_classification_guard",
	"backup_asset_recovery_content_authorization_guard",
	"backup_asset_recovery_content_binding_guard",
	"backup_asset_recovery_downgrade_admission",
}

var backupAssetExportTables = []string{
	"backup_asset_export_jobs",
	"backup_asset_export_keys",
	"backup_asset_export_items",
	"backup_asset_export_attempts",
	"backup_asset_export_item_attempts",
	"backup_asset_export_source_leases",
	"backup_asset_export_artifacts",
	"backup_asset_export_idempotency",
	"backup_asset_export_quota_buckets",
	"backup_asset_export_reservations",
	"backup_asset_export_delivery_grants",
	"backup_asset_export_delivery_requests",
	"backup_asset_archive_member_requests",
}

var backupAssetProcessingTables = []string{
	"backup_asset_processing_jobs",
	"backup_asset_processing_interests",
	"backup_asset_processing_attempts",
	"backup_asset_processing_grants",
	"backup_asset_processing_grant_requests",
	"backup_asset_processing_uploads",
	"backup_asset_worker_identities",
	"backup_asset_worker_capabilities",
	"backup_asset_derived_artifact_sets",
	"backup_asset_derived_artifacts",
	"backup_asset_derived_blobs",
	"backup_asset_derived_blob_references",
	"backup_asset_updater_metadata",
}

var backupAssetContentTables = []string{
	"backup_asset_delivery_grants",
	"backup_asset_delivery_requests",
	"backup_asset_delivery_usage",
}

var backupAssetSearchTables = []string{
	"backup_asset_search_generations",
	"backup_asset_search_documents",
	"backup_asset_search_postings",
	"backup_asset_search_document_fields",
	"backup_asset_saved_searches",
	"backup_asset_saved_search_scope_points",
	"backup_asset_favorites",
	"backup_asset_tag_definitions",
	"backup_asset_tag_assignments",
	"backup_asset_recent_access",
	"backup_asset_overlay_usage",
	"backup_asset_overlay_idempotency",
}

var backupAssetFoundationTables = []string{
	"backup_repositories",
	"repository_access_bindings",
	"task_repository_links",
	"recovery_points",
	"recovery_point_manifests",
	"catalog_generations",
	"catalog_entries",
	"wrapped_domain_keys",
	"recovery_point_leases",
	"backup_asset_audit_checkpoints",
	"backup_asset_audit_events",
}

var backupAssetFoundationIndexNames = []string{
	"idx_backup_repositories_provider_identity",
	"idx_backup_repositories_provider_kind",
	"idx_backup_repositories_status",
	"idx_repository_access_bindings_active",
	"idx_repository_access_bindings_repository_id",
	"idx_task_repository_links_active_task",
	"idx_task_repository_links_repository_id",
	"idx_recovery_points_mutable_head",
	"idx_recovery_points_repository_state",
	"idx_recovery_points_producing_task_id",
	"idx_recovery_points_producing_task_run_id",
	"idx_recovery_points_retention_until",
	"idx_recovery_point_manifests_active",
	"idx_recovery_point_manifests_recovery_point_id",
	"idx_catalog_generations_active",
	"idx_catalog_generations_recovery_point_id",
	"idx_catalog_generations_manifest_id",
	"idx_catalog_entries_listing",
	"idx_catalog_entries_entry_id",
	"idx_wrapped_domain_keys_active",
	"idx_wrapped_domain_keys_domain_state",
	"idx_recovery_point_leases_active_owner_slot",
	"idx_recovery_point_leases_recovery_status_expiry",
	"idx_recovery_point_leases_absolute_deadline",
	"idx_backup_asset_audit_checkpoints_status",
	"idx_backup_asset_audit_events_action_created_at",
	"idx_backup_asset_audit_events_repository_created_at",
	"idx_backup_asset_audit_events_recovery_point_created_at",
}

var backupAssetFoundationCheckFragments = map[string][]string{
	"backup_repositories":            {"connecting", "native_snapshot", "storage_worm"},
	"repository_access_bindings":     {"revoked"},
	"task_repository_links":          {"versioned_full_copy"},
	"recovery_points":                {"imported_baseline", "purge_blocked", "released"},
	"recovery_point_manifests":       {"unavailable"},
	"catalog_generations":            {"superseded"},
	"catalog_entries":                {"hardlink", "special"},
	"wrapped_domain_keys":            {"recovery_cleanup_ownership", "verify_only"},
	"recovery_point_leases":          {"content_session", "recovery_job", "expired"},
	"backup_asset_audit_checkpoints": {"details_purged"},
	"backup_asset_audit_events":      {"success", "failure", "blocked"},
}

var backupAssetFoundationPartialUniqueIndexFragments = map[string][]string{
	"idx_backup_repositories_provider_identity":   {"provider_kind", "repository_identity", "where", "is not null"},
	"idx_repository_access_bindings_active":       {"repository_id", "where", "status", "active"},
	"idx_task_repository_links_active_task":       {"task_id", "where", "unlinked_at", "is null"},
	"idx_recovery_points_mutable_head":            {"repository_id", "where", "semantics", "mutable_head"},
	"idx_recovery_point_manifests_active":         {"recovery_point_id", "where", "is_active"},
	"idx_catalog_generations_active":              {"recovery_point_id", "where", "is_active"},
	"idx_wrapped_domain_keys_active":              {"domain", "where", "state", "active"},
	"idx_recovery_point_leases_active_owner_slot": {"recovery_point_id", "holder_type", "owner_id", "where", "status", "active"},
}

func TestBackupAssetMigration062SQLiteApplyDown(t *testing.T) {
	migrator, db := newSQLiteBackupAssetMigrator(t)
	migrateToBackupAssetFoundation(t, migrator)
	assertMigrationVersion(t, migrator, backupAssetMigrationVersion)
	assertFoundationSchemaPresent(t, db, "sqlite")
	assertFoundationIndexesPresent(t, db, "sqlite")
	assertFoundationPartialUniqueIndexes(t, db, "sqlite")
	assertFoundationChecksPresent(t, db, "sqlite")

	if err := migrator.Steps(-1); err != nil {
		t.Fatalf("step SQLite migration down to 000061: %v", err)
	}
	assertMigrationVersion(t, migrator, backupAssetMigrationVersion-1)
	assertFoundationSchemaAbsent(t, db, "sqlite")
}

func TestBackupAssetMigration062PostgresApplyDown(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		if strings.TrimSpace(os.Getenv("REQUIRE_POSTGRES_MIGRATION_TEST")) == "1" {
			t.Fatal("TEST_POSTGRES_DSN is required when REQUIRE_POSTGRES_MIGRATION_TEST=1")
		}
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}

	migrator, db := newPostgresBackupAssetMigrator(t, dsn)
	migrateToBackupAssetFoundation(t, migrator)
	assertMigrationVersion(t, migrator, backupAssetMigrationVersion)
	assertFoundationSchemaPresent(t, db, "postgres")
	assertFoundationIndexesPresent(t, db, "postgres")
	assertFoundationPartialUniqueIndexes(t, db, "postgres")
	assertFoundationChecksPresent(t, db, "postgres")
	assertPostgresForeignKeyAction(t, db, "task_repository_links", "task_id", "tasks", "SET NULL")
	assertPostgresForeignKeyAction(t, db, "recovery_points", "producing_task_id", "tasks", "SET NULL")
	assertPostgresForeignKeyAction(t, db, "recovery_points", "producing_task_run_id", "task_runs", "SET NULL")
	assertFoundationModelParity(t, db, "postgres")
	assertPostgresUTCRoundTripAndNoTimeDefaults(t, db)

	if err := migrator.Steps(-1); err != nil {
		t.Fatalf("step PostgreSQL migration down to 000061: %v", err)
	}
	assertMigrationVersion(t, migrator, backupAssetMigrationVersion-1)
	assertFoundationSchemaAbsent(t, db, "postgres")
}

func TestRunMigrationsPostgresDirtyCheckUsesSearchPath(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		if strings.TrimSpace(os.Getenv("REQUIRE_POSTGRES_MIGRATION_TEST")) == "1" {
			t.Fatal("TEST_POSTGRES_DSN is required when REQUIRE_POSTGRES_MIGRATION_TEST=1")
		}
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.Scheme == "" {
		t.Fatalf("TEST_POSTGRES_DSN must be a PostgreSQL URL: %v", err)
	}

	baseDB, err := openPostgresSQLDB(dsn)
	if err != nil {
		t.Fatalf("open PostgreSQL dirty-check base: %v", err)
	}
	if err := baseDB.Ping(); err != nil {
		_ = baseDB.Close()
		t.Fatalf("ping PostgreSQL dirty-check base: %v", err)
	}
	t.Cleanup(func() { _ = baseDB.Close() })

	suffix := time.Now().UTC().Format("20060102150405.000000000")
	freshSchema := "xirang_dirty_fresh_" + strings.ReplaceAll(suffix, ".", "")
	firstSchema := "xirang_dirty_first_" + strings.ReplaceAll(suffix, ".", "")
	siblingSchema := "xirang_dirty_sibling_" + strings.ReplaceAll(suffix, ".", "")
	createdSchemas := make([]string, 0, 3)
	t.Cleanup(func() {
		for index := len(createdSchemas) - 1; index >= 0; index-- {
			if _, err := baseDB.Exec("DROP SCHEMA IF EXISTS " + createdSchemas[index] + " CASCADE"); err != nil {
				t.Errorf("drop PostgreSQL dirty-check schema %s: %v", createdSchemas[index], err)
			}
		}
	})
	for _, schema := range []string{freshSchema, firstSchema, siblingSchema} {
		if _, err := baseDB.Exec("CREATE SCHEMA " + schema); err != nil {
			t.Fatalf("create PostgreSQL dirty-check schema %s: %v", schema, err)
		}
		createdSchemas = append(createdSchemas, schema)
	}

	if _, err := baseDB.Exec("CREATE TABLE " + siblingSchema + ".schema_migrations (version BIGINT NOT NULL PRIMARY KEY, dirty BOOLEAN NOT NULL)"); err != nil {
		t.Fatalf("create sibling schema_migrations: %v", err)
	}
	if _, err := baseDB.Exec("INSERT INTO " + siblingSchema + ".schema_migrations (version, dirty) VALUES (68, true)"); err != nil {
		t.Fatalf("seed sibling dirty migration: %v", err)
	}

	openScoped := func(searchPath string) (*gorm.DB, *sql.DB) {
		t.Helper()
		scoped := *parsed
		query := scoped.Query()
		query.Set("search_path", searchPath)
		query.Set("timezone", "UTC")
		scoped.RawQuery = query.Encode()
		gdb, err := Open(config.Config{DBType: "postgres", PostgresDSN: scoped.String()})
		if err != nil {
			t.Fatalf("open PostgreSQL dirty-check scope %s: %v", searchPath, err)
		}
		sqlDB, err := gdb.DB()
		if err != nil {
			t.Fatalf("get PostgreSQL dirty-check scope %s DB: %v", searchPath, err)
		}
		t.Cleanup(func() { _ = sqlDB.Close() })
		return gdb, sqlDB
	}

	freshDB, _ := openScoped(freshSchema)
	if err := RunMigrations(freshDB, "postgres"); err != nil {
		t.Fatalf("fresh scoped schema must ignore sibling dirty row: %v", err)
	}

	firstDB, _ := openScoped(firstSchema + "," + siblingSchema)
	err = RunMigrations(firstDB, "postgres")
	if !errors.Is(err, ErrMigrationDirty) {
		t.Fatalf("search-path-visible sibling dirty row must fail closed, got %v", err)
	}
}

func TestRunMigrationsPostgresSchemaDriftCheckUsesSearchPath(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		if strings.TrimSpace(os.Getenv("REQUIRE_POSTGRES_MIGRATION_TEST")) == "1" {
			t.Fatal("TEST_POSTGRES_DSN is required when REQUIRE_POSTGRES_MIGRATION_TEST=1")
		}
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.Scheme == "" {
		t.Fatalf("TEST_POSTGRES_DSN must be a PostgreSQL URL: %v", err)
	}

	baseDB, err := openPostgresSQLDB(dsn)
	if err != nil {
		t.Fatalf("open PostgreSQL schema-drift base: %v", err)
	}
	if err := baseDB.Ping(); err != nil {
		_ = baseDB.Close()
		t.Fatalf("ping PostgreSQL schema-drift base: %v", err)
	}
	t.Cleanup(func() { _ = baseDB.Close() })

	suffix := strings.ReplaceAll(time.Now().UTC().Format("20060102150405.000000000"), ".", "")
	targetSchema := "xirang_schema_drift_" + suffix
	if _, err := baseDB.Exec("CREATE SCHEMA " + targetSchema); err != nil {
		t.Fatalf("create PostgreSQL schema-drift schema: %v", err)
	}
	t.Cleanup(func() {
		if _, err := baseDB.Exec("DROP SCHEMA IF EXISTS " + targetSchema + " CASCADE"); err != nil {
			t.Errorf("drop PostgreSQL schema-drift schema: %v", err)
		}
	})
	if _, err := baseDB.Exec("CREATE TABLE " + targetSchema + ".schema_migrations (version BIGINT NOT NULL PRIMARY KEY, dirty BOOLEAN NOT NULL)"); err != nil {
		t.Fatalf("create schema-drift schema_migrations: %v", err)
	}
	if _, err := baseDB.Exec("INSERT INTO " + targetSchema + ".schema_migrations (version, dirty) VALUES (71, false)"); err != nil {
		t.Fatalf("seed clean schema-drift migration: %v", err)
	}
	if _, err := baseDB.Exec("CREATE TABLE " + targetSchema + ".policies (id BIGINT PRIMARY KEY, bw_limit BIGINT NOT NULL)"); err != nil {
		t.Fatalf("create schema-drift policies fixture: %v", err)
	}
	if _, err := baseDB.Exec("INSERT INTO " + targetSchema + ".policies (id, bw_limit) VALUES (1, 23)"); err != nil {
		t.Fatalf("seed schema-drift policies fixture: %v", err)
	}

	scoped := *parsed
	query := scoped.Query()
	query.Set("search_path", targetSchema)
	query.Set("timezone", "UTC")
	scoped.RawQuery = query.Encode()
	gdb, err := Open(config.Config{DBType: "postgres", PostgresDSN: scoped.String()})
	if err != nil {
		t.Fatalf("open PostgreSQL schema-drift scope: %v", err)
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		t.Fatalf("get PostgreSQL schema-drift scope DB: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	err = RunMigrations(gdb, "postgres")
	if !errors.Is(err, ErrMigrationSchemaDrift) {
		t.Fatalf("clean version 71 without minimum schema returned %v, want ErrMigrationSchemaDrift", err)
	}
	dirty, version, checkErr := checkMigrationDirty(sqlDB, "postgres")
	if checkErr != nil {
		t.Fatalf("check PostgreSQL migration metadata after schema-drift rejection: %v", checkErr)
	}
	if dirty || version != 71 {
		t.Fatalf("PostgreSQL schema-drift rejection mutated migration metadata: version=%d dirty=%v", version, dirty)
	}
	oldExists, oldErr := migrationColumnExists(sqlDB, "postgres", "policies", "bw_limit")
	newExists, newErr := migrationColumnExists(sqlDB, "postgres", "policies", "bwlimit")
	if oldErr != nil || newErr != nil {
		t.Fatalf("inspect PostgreSQL policies columns after rejection: old=%v new=%v", oldErr, newErr)
	}
	if !oldExists || newExists {
		t.Fatalf("PostgreSQL schema-drift rejection mutated legacy policies schema: old=%v new=%v", oldExists, newExists)
	}
	var bwLimit int
	if scanErr := sqlDB.QueryRow(`SELECT bw_limit FROM policies WHERE id = 1`).Scan(&bwLimit); scanErr != nil {
		t.Fatalf("read PostgreSQL policies data after rejection: %v", scanErr)
	}
	if bwLimit != 23 {
		t.Fatalf("PostgreSQL schema-drift rejection mutated policies data: got %d want 23", bwLimit)
	}
}

func TestRunMigrationsClean071Applies072SQLite(t *testing.T) {
	fixture := newSQLiteMigrationFixture(t)
	_, sqlDB := fixture.openAt(t, backupAssetGAVersion)
	gdb, err := gorm.Open(sqlitegorm.New(sqlitegorm.Config{Conn: sqlDB}), &gorm.Config{})
	if err != nil {
		t.Fatalf("wrap clean 071 SQLite fixture: %v", err)
	}

	if err := RunMigrations(gdb, "sqlite"); err != nil {
		t.Fatalf("run migration 072 from clean complete 071: %v", err)
	}
	dirty, version, err := checkMigrationDirty(sqlDB, "sqlite")
	if err != nil {
		t.Fatalf("check migration 072 metadata: %v", err)
	}
	if dirty || version != backupAssetTaskRunCompatVersion {
		t.Fatalf("migration 072 metadata mismatch: version=%d dirty=%v", version, dirty)
	}
	if err := validateMinimumRecoverySchema(sqlDB, "sqlite", version); err != nil {
		t.Fatalf("validate migration 072 minimum recovery schema: %v", err)
	}
}

func TestBackupAssetMigration062ForeignKeysSetNull(t *testing.T) {
	migrator, db := newSQLiteBackupAssetMigrator(t)
	migrateToBackupAssetFoundation(t, migrator)

	assertSQLiteForeignKeyAction(t, db, "task_repository_links", "task_id", "tasks", "SET NULL")
	assertSQLiteForeignKeyAction(t, db, "recovery_points", "producing_task_id", "tasks", "SET NULL")
	assertSQLiteForeignKeyAction(t, db, "recovery_points", "producing_task_run_id", "task_runs", "SET NULL")

	now := "2026-07-13 03:04:05+00:00"
	mustExec(t, db, `INSERT INTO tasks (id, name, node_id, executor_type, status, created_at, updated_at) VALUES (9001, 'foundation-task', 1, 'local', 'idle', ?, ?)`, now, now)
	mustExec(t, db, `INSERT INTO task_runs (id, task_id, trigger_type, status, created_at, updated_at) VALUES (9002, 9001, 'manual', 'success', ?, ?)`, now, now)
	insertFoundationRepository(t, db, strings.Repeat("a", 32), now)
	mustExec(t, db, `INSERT INTO task_repository_links
		(id, task_id, repository_id, task_name_snapshot, node_id_snapshot, node_name_snapshot, publication_mode, encrypted_legacy_locator, linked_at, created_at, updated_at)
		VALUES (?, 9001, ?, 'foundation-task', 1, 'node-a', 'versioned_full_copy', '', ?, ?, ?)`, strings.Repeat("b", 32), strings.Repeat("a", 32), now, now, now)
	mustExec(t, db, `INSERT INTO recovery_points
		(id, repository_id, producing_task_id, producing_task_run_id, producing_task_name_snapshot, producing_node_id_snapshot, producing_node_name_snapshot,
		lineage_json, encrypted_provider_locator, encrypted_rollback_locator, semantics, state, manifest_digest_algorithm, manifest_digest,
		entry_count, logical_bytes, consistency_json, fidelity_json, capability_revision, capabilities_json, immutability_level,
		physical_availability, hold_state, created_at, updated_at)
		VALUES (?, ?, 9001, 9002, 'foundation-task', 1, 'node-a', '{}', '', '', 'xirang_manifest', 'committed', 'sha256', '', 0, 0, '{}', '{}', 1, '{}', 'xirang_managed', 'online', 'none', ?, ?)`, strings.Repeat("c", 32), strings.Repeat("a", 32), now, now)

	mustExec(t, db, `DELETE FROM task_runs WHERE id = 9002`)
	mustExec(t, db, `DELETE FROM tasks WHERE id = 9001`)

	var linkTaskID sql.NullInt64
	if err := db.QueryRow(`SELECT task_id FROM task_repository_links WHERE id = ?`, strings.Repeat("b", 32)).Scan(&linkTaskID); err != nil {
		t.Fatalf("load task link after task delete: %v", err)
	}
	var pointTaskID, pointRunID sql.NullInt64
	if err := db.QueryRow(`SELECT producing_task_id, producing_task_run_id FROM recovery_points WHERE id = ?`, strings.Repeat("c", 32)).Scan(&pointTaskID, &pointRunID); err != nil {
		t.Fatalf("load recovery point after task/run delete: %v", err)
	}
	if linkTaskID.Valid || pointTaskID.Valid || pointRunID.Valid {
		t.Fatalf("SET NULL contract failed: link=%v point_task=%v point_run=%v", linkTaskID, pointTaskID, pointRunID)
	}
}

func TestBackupAssetMigration062MutableHeadAndActiveGenerationUniqueness(t *testing.T) {
	migrator, db := newSQLiteBackupAssetMigrator(t)
	migrateToBackupAssetFoundation(t, migrator)

	now := "2026-07-13 03:04:05+00:00"
	repositoryID := strings.Repeat("d", 32)
	insertFoundationRepository(t, db, repositoryID, now)
	insertRecoveryPoint := func(id string, semantics string) error {
		_, err := db.Exec(`INSERT INTO recovery_points
			(id, repository_id, producing_task_name_snapshot, producing_node_id_snapshot, producing_node_name_snapshot,
			lineage_json, encrypted_provider_locator, encrypted_rollback_locator, semantics, state, observed_at, manifest_digest_algorithm,
			manifest_digest, entry_count, logical_bytes, consistency_json, fidelity_json, capability_revision, capabilities_json,
			immutability_level, physical_availability, hold_state, created_at, updated_at)
			VALUES (?, ?, '', 0, '', '{}', '', '', ?, 'observed', ?, 'sha256', '', 0, 0, '{}', '{}', 1, '{}', 'mutable', 'online', 'none', ?, ?)`, id, repositoryID, semantics, now, now, now)
		return err
	}
	if err := insertRecoveryPoint(strings.Repeat("e", 32), "mutable_head"); err != nil {
		t.Fatalf("insert first mutable head: %v", err)
	}
	if err := insertRecoveryPoint(strings.Repeat("f", 32), "mutable_head"); err == nil {
		t.Fatal("second mutable head for one repository unexpectedly succeeded")
	}

	pointID := strings.Repeat("1", 32)
	mustExec(t, db, `INSERT INTO recovery_points
		(id, repository_id, producing_task_name_snapshot, producing_node_id_snapshot, producing_node_name_snapshot,
		lineage_json, encrypted_provider_locator, encrypted_rollback_locator, semantics, state, manifest_digest_algorithm,
		manifest_digest, entry_count, logical_bytes, consistency_json, fidelity_json, capability_revision, capabilities_json,
		immutability_level, physical_availability, hold_state, created_at, updated_at)
		VALUES (?, ?, '', 0, '', '{}', '', '', 'xirang_manifest', 'preparing', 'sha256', '', 0, 0, '{}', '{}', 1, '{}', 'xirang_managed', 'online', 'none', ?, ?)`, pointID, repositoryID, now, now)
	insertGeneration := func(id string, generation int) error {
		_, err := db.Exec(`INSERT INTO catalog_generations
			(id, recovery_point_id, generation, state, is_active, source_fingerprint, expected_entry_count, written_entry_count,
			expected_digest, written_digest, error_code, correlation_id, started_at, created_at, updated_at)
			VALUES (?, ?, ?, 'building', 1, '', 0, 0, '', '', '', '', ?, ?, ?)`, id, pointID, generation, now, now, now)
		return err
	}
	if err := insertGeneration(strings.Repeat("2", 32), 1); err != nil {
		t.Fatalf("insert first active generation: %v", err)
	}
	if err := insertGeneration(strings.Repeat("3", 32), 2); err == nil {
		t.Fatal("second active generation for one recovery point unexpectedly succeeded")
	}
}

func TestBackupAssetMigration062UTCAndModelParity(t *testing.T) {
	migrator, db := newSQLiteBackupAssetMigrator(t)
	migrateToBackupAssetFoundation(t, migrator)

	assertFoundationModelParity(t, db, "sqlite")
	if !containsString(gormColumnNames(t, model.Task{}), "archived_at") {
		t.Fatal("model.Task is missing archived_at")
	}

	now := "2026-07-13 03:04:05+00:00"
	insertFoundationRepository(t, db, strings.Repeat("9", 32), now)
	var createdAt time.Time
	if err := db.QueryRow(`SELECT created_at FROM backup_repositories WHERE id = ?`, strings.Repeat("9", 32)).Scan(&createdAt); err != nil {
		t.Fatalf("scan UTC created_at: %v", err)
	}
	if createdAt.Location() != time.UTC || createdAt.Format(time.RFC3339) != "2026-07-13T03:04:05Z" {
		t.Fatalf("UTC timestamp drifted: %s (%s)", createdAt.Format(time.RFC3339), createdAt.Location())
	}

	for _, table := range backupAssetFoundationTables {
		assertSQLiteTimeColumnsHaveNoDefault(t, db, table)
	}
}

func TestBackupAssetMigration063SQLite(t *testing.T) {
	runBackupAssetMigration063Contract(t, newSQLiteMigrationFixture(t))
}

func TestBackupAssetMigration063Postgres(t *testing.T) {
	runBackupAssetMigration063Contract(t, newRequiredPostgresMigrationFixture(t))
}

func TestBackupAssetMigration064SQLite(t *testing.T) {
	runBackupAssetMigration064Contract(t, newSQLiteMigrationFixture(t))
}

func TestBackupAssetMigration064Postgres(t *testing.T) {
	runBackupAssetMigration064Contract(t, newRequiredPostgresMigrationFixture(t))
}

func TestBackupAssetMigration065SQLite(t *testing.T) {
	runBackupAssetMigration065Contract(t, newSQLiteMigrationFixture(t))
}

func TestBackupAssetMigration065Postgres(t *testing.T) {
	runBackupAssetMigration065Contract(t, newRequiredPostgresMigrationFixture(t))
}

func TestBackupAssetMigration066SQLite(t *testing.T) {
	runBackupAssetMigration066Contract(t, newSQLiteMigrationFixture(t))
}

func TestBackupAssetMigration066Postgres(t *testing.T) {
	runBackupAssetMigration066Contract(t, newRequiredPostgresMigrationFixture(t))
}

func TestBackupAssetMigration067SQLite(t *testing.T) {
	runBackupAssetMigration067Contract(t, newSQLiteMigrationFixture(t))
}

func TestBackupAssetMigration067Postgres(t *testing.T) {
	runBackupAssetMigration067Contract(t, newRequiredPostgresMigrationFixture(t))
}

func TestBackupAssetMigration068SQLite(t *testing.T) {
	runBackupAssetMigration068Contract(t, newSQLiteMigrationFixture(t))
}

func TestBackupAssetMigration068Postgres(t *testing.T) {
	runBackupAssetMigration068Contract(t, newRequiredPostgresMigrationFixture(t))
}

func TestBackupAssetMigration069SQLite(t *testing.T) {
	runBackupAssetMigration069Contract(t, newSQLiteMigrationFixture(t))
}

func TestBackupAssetMigration069WholeTask6ClosureSQLite(t *testing.T) {
	newSQLiteMigrationFixture(t).test069WholeTask6Closure(t)
}

func TestBackupAssetMigration069WorkspaceCleanupOwnershipSQLite(t *testing.T) {
	newSQLiteMigrationFixture(t).test069WorkspaceCleanupOwnership(t)
}

func TestBackupAssetMigration069WorkspaceCleanupOwnershipPostgres(t *testing.T) {
	newRequiredPostgresRecoveryMigrationFixture(t).test069WorkspaceCleanupOwnership(t)
}

func TestBackupAssetMigration069AuthorizationReceiptSQLite(t *testing.T) {
	fixture := newSQLiteMigrationFixture(t)
	_, db := fixture.openAt(t, backupAssetRecoveryVersion)

	for _, column := range []string{
		"plan_id",
		"checkpoint_id",
		"grant_id",
		"attempt_id",
		"source_lease_id",
		"node_lease_id",
		"requester_id",
		"operation",
		"category",
		"endpoint",
		"idempotency_key_digest",
		"intent_digest",
		"step_up_jti_digest",
		"presenting_session_digest",
		"presenting_session_user_id",
		"presenting_session_role",
		"presenting_session_token_version",
		"proof_expires_at",
		"presenting_session_expires_at",
		"replay_expires_at",
		"expected_plan_transition_revision",
		"result_plan_transition_revision",
		"grant_binding_digest",
		"source_lease_binding_digest",
		"node_lease_fence",
	} {
		if !databaseColumnExists(t, db, fixture.engine, "backup_asset_recovery_evidence", column) {
			t.Fatalf("SQLite 000069 evidence omits authorization-receipt column %s", column)
		}
	}

	definition := fixture.tableDefinition(t, db, "backup_asset_recovery_evidence")
	for _, fragment := range []string{
		"authorization_receipt",
		"proof_expires_at <= replay_expires_at",
		"replay_expires_at <= presenting_session_expires_at",
		"foreign key (source_lease_id)",
	} {
		if !strings.Contains(strings.ToLower(definition), fragment) {
			t.Fatalf("SQLite 000069 authorization-receipt evidence omits %q: %s", fragment, definition)
		}
	}

	for _, index := range []string{
		"idx_backup_asset_recovery_evidence_authorization_idempotency",
		"idx_backup_asset_recovery_evidence_authorization_proof",
		"idx_backup_asset_recovery_evidence_authorization_reaper",
		"idx_recovery_point_leases_recovery_job_owner",
	} {
		if indexDefinition := fixture.indexDefinition(t, db, index); indexDefinition == "" {
			t.Fatalf("SQLite 000069 omits authorization-receipt index %s", index)
		}
	}

	for _, trigger := range []string{
		"trg_backup_asset_recovery_evidence_receipt_update",
		"trg_backup_asset_recovery_evidence_receipt_delete",
		"trg_backup_asset_recovery_evidence_receipt_insert",
	} {
		if !fixture.recoveryTriggerExists(t, db, "backup_asset_recovery_evidence", trigger) {
			t.Fatalf("SQLite 000069 omits authorization-receipt trigger %s", trigger)
		}
	}
}

func TestBackupAssetMigration069SecurityOverrideCandidateSQLite(t *testing.T) {
	newSQLiteMigrationFixture(t).test069SecurityOverrideCandidate(t)
}

func TestBackupAssetMigration069SecurityOverrideCandidatePostgres(t *testing.T) {
	newRequiredPostgresMigrationFixture(t).test069SecurityOverrideCandidate(t)
}

func TestBackupAssetMigration069SecurityOverrideBindingFreezeSQLite(t *testing.T) {
	newSQLiteMigrationFixture(t).test069SecurityOverrideBindingFreeze(t)
}

func TestBackupAssetMigration069SecurityOverrideBindingFreezePostgres(t *testing.T) {
	newRequiredPostgresMigrationFixture(t).test069SecurityOverrideBindingFreeze(t)
}

func (fixture migrationFixture) test069SecurityOverrideCandidate(t *testing.T) {
	t.Helper()
	_, db := fixture.openAt(t, backupAssetRecoveryVersion)
	for _, column := range []string{
		"security_override_candidate_digest",
		"security_override_categories",
	} {
		if !databaseColumnExists(t, db, fixture.engine, "backup_asset_recovery_preflights", column) {
			t.Fatalf("%s 000069 preflight omits persisted security override candidate column %s", fixture.engine, column)
		}
	}
	definition := strings.ToLower(fixture.tableDefinition(t, db, "backup_asset_recovery_preflights"))
	for _, fragment := range []string{
		"security_override_candidate_digest",
		"security_override_categories",
		"malware,suspicious,test_signature",
	} {
		if !strings.Contains(definition, fragment) {
			t.Fatalf("%s 000069 preflight candidate contract omits %q: %s", fixture.engine, fragment, definition)
		}
	}
}

func TestRecoveryAuthorizationReceiptDirectSQLSQLite(t *testing.T) {
	newSQLiteMigrationFixture(t).test069AuthorizationReceiptDirectSQL(t)
}

func TestRecoveryAuthorizationReceiptDirectSQLPostgres(t *testing.T) {
	if strings.TrimSpace(os.Getenv("REQUIRE_POSTGRES_RECOVERY_TEST")) == "1" {
		t.Setenv("REQUIRE_POSTGRES_MIGRATION_TEST", "1")
	}
	newRequiredPostgresMigrationFixture(t).test069AuthorizationReceiptDirectSQL(t)
}

type recoveryAuthorizationReceiptSeed struct {
	ID                             string
	PlanID                         string
	JobID                          any
	CheckpointID                   any
	GrantID                        any
	AttemptID                      any
	SourceLeaseID                  any
	NodeLeaseID                    any
	RequesterID                    int64
	Operation                      string
	Category                       string
	Endpoint                       string
	IdempotencyKeyDigest           string
	IntentDigest                   string
	StepUpJTIDigest                string
	PresentingSessionDigest        string
	PresentingSessionUserID        int64
	PresentingSessionRole          string
	PresentingSessionTokenVersion  int64
	ProofExpiresAt                 time.Time
	PresentingSessionExpiresAt     time.Time
	ReplayExpiresAt                time.Time
	ExpectedPlanTransitionRevision int64
	ResultPlanTransitionRevision   int64
	GrantBindingDigest             string
	SourceLeaseBindingDigest       string
	NodeLeaseFence                 int64
	CreatedAt                      time.Time
}

func (fixture migrationFixture) insertRecoveryAuthorizationReceipt(
	t *testing.T,
	db *sql.DB,
	receipt recoveryAuthorizationReceiptSeed,
) error {
	t.Helper()
	_, err := db.Exec(fixture.bind(`INSERT INTO backup_asset_recovery_evidence (
		id, job_id, kind, outcome, summary_digest, difference_count, verified_at,
		plan_id, checkpoint_id, grant_id, attempt_id, source_lease_id, node_lease_id,
		requester_id, operation, category, endpoint, idempotency_key_digest,
		intent_digest, step_up_jti_digest, presenting_session_digest,
		presenting_session_user_id, presenting_session_role,
		presenting_session_token_version, proof_expires_at,
		presenting_session_expires_at, replay_expires_at,
		expected_plan_transition_revision, result_plan_transition_revision,
		grant_binding_digest, source_lease_binding_digest, node_lease_fence,
		created_at, updated_at
	) VALUES (?, ?, 'authorization_receipt', '', '', 0, NULL,
		?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		receipt.ID, receipt.JobID, receipt.PlanID, receipt.CheckpointID, receipt.GrantID,
		receipt.AttemptID, receipt.SourceLeaseID, receipt.NodeLeaseID, receipt.RequesterID,
		receipt.Operation, receipt.Category, receipt.Endpoint, receipt.IdempotencyKeyDigest,
		receipt.IntentDigest, receipt.StepUpJTIDigest, receipt.PresentingSessionDigest,
		receipt.PresentingSessionUserID, receipt.PresentingSessionRole,
		receipt.PresentingSessionTokenVersion, receipt.ProofExpiresAt,
		receipt.PresentingSessionExpiresAt, receipt.ReplayExpiresAt,
		receipt.ExpectedPlanTransitionRevision, receipt.ResultPlanTransitionRevision,
		receipt.GrantBindingDigest, receipt.SourceLeaseBindingDigest, receipt.NodeLeaseFence,
		receipt.CreatedAt, receipt.CreatedAt)
	return err
}

func (fixture migrationFixture) test069SecurityOverrideBindingFreeze(t *testing.T) {
	t.Helper()
	_, db := fixture.openAt(t, backupAssetRecoveryVersion)
	now := time.Now().UTC().Truncate(time.Second)
	aggregate := fixture.seedRecoveryMigrationPurgeablePlan(t, db, "override-freeze", 207, now.Add(-4*time.Hour))

	fixture.mustExec(t, db, `UPDATE backup_asset_recovery_plans
		SET security_decision = 'admin_override', security_decision_digest = ?,
			security_override_binding_digest = ?, encrypted_override_reason = 'enc:v2:override-reason',
			binding_digest = ?, transition_revision = 2, updated_at = ?
		WHERE id = ?`,
		recoveryMigrationDigest(790070), recoveryMigrationDigest(790071), recoveryMigrationDigest(790072), now, aggregate.PlanID)

	expiredReceipt := recoveryAuthorizationReceiptSeed{
		ID:                             recoveryMigrationOpaqueID(790073),
		PlanID:                         aggregate.PlanID,
		RequesterID:                    aggregate.UserID,
		Operation:                      "security_override",
		Category:                       "security_override",
		Endpoint:                       "/api/v1/recovery-plans/:id/security-overrides",
		IdempotencyKeyDigest:           recoveryMigrationDigest(790074),
		IntentDigest:                   recoveryMigrationDigest(790075),
		StepUpJTIDigest:                recoveryMigrationDigest(790076),
		PresentingSessionDigest:        recoveryMigrationDigest(790077),
		PresentingSessionUserID:        aggregate.UserID,
		PresentingSessionRole:          "admin",
		PresentingSessionTokenVersion:  1,
		ProofExpiresAt:                 now.Add(-3 * time.Hour),
		ReplayExpiresAt:                now.Add(-2 * time.Hour),
		PresentingSessionExpiresAt:     now.Add(-time.Hour),
		ExpectedPlanTransitionRevision: 1,
		ResultPlanTransitionRevision:   2,
		CreatedAt:                      now.Add(-4 * time.Hour),
	}
	if err := fixture.insertRecoveryAuthorizationReceipt(t, db, expiredReceipt); err != nil {
		t.Fatalf("insert expired %s security override receipt: %v", fixture.engine, err)
	}
	fixture.mustExec(t, db, `DELETE FROM backup_asset_recovery_evidence
		WHERE id = ? AND kind = 'authorization_receipt' AND replay_expires_at <= ?`, expiredReceipt.ID, now)

	var receiptCount int
	if err := db.QueryRow(fixture.bind(`SELECT COUNT(*) FROM backup_asset_recovery_evidence WHERE id = ?`), expiredReceipt.ID).Scan(&receiptCount); err != nil {
		t.Fatalf("count reaped %s security override receipt: %v", fixture.engine, err)
	}
	if receiptCount != 0 {
		t.Fatalf("%s expired security override receipt remains after reaping", fixture.engine)
	}

	fixture.expectExecRejectedInRollback(t, db,
		`UPDATE backup_asset_recovery_plans SET binding_digest = ? WHERE id = ?`,
		recoveryMigrationDigest(790078), aggregate.PlanID)
}

func (fixture migrationFixture) test069AuthorizationReceiptDirectSQL(t *testing.T) {
	migrator, db := fixture.openAt(t, backupAssetRecoveryVersion)
	now := time.Now().UTC().Truncate(time.Second)
	securityAggregate := fixture.seedRecoveryMigrationAggregate(t, db, "1", 201, now, true)
	writeAggregate := fixture.seedRecoveryMigrationAggregate(t, db, "2", 202, now.Add(time.Second), true)
	executeAggregate := fixture.seedRecoveryMigrationAggregate(t, db, "3", 203, now.Add(2*time.Second), true)
	deleteAggregate := fixture.seedRecoveryMigrationExactMirrorAggregate(t, db, "4", 204, now.Add(3*time.Second))

	// The generic active-aggregate fixture carries terminal verification/source
	// evidence for tests that need it. This receipt matrix owns its exact singleton
	// active source lease, so remove those unrelated terminal fixture rows first.
	fixture.mustExec(t, db, `DELETE FROM backup_asset_recovery_evidence WHERE job_id = ?`, executeAggregate.JobID)
	fixture.mustExec(t, db, `DELETE FROM recovery_point_leases
		WHERE holder_type = 'recovery_job' AND owner_id = ? AND attempt_id = ?`,
		executeAggregate.JobID, executeAggregate.AttemptID)
	sourceLeaseID := recoveryMigrationOpaqueID(790001)
	fixture.mustExec(t, db, `INSERT INTO recovery_point_leases (
		id, recovery_point_id, holder_type, owner_id, attempt_id, fence_token, status,
		lease_expires_at, absolute_deadline, last_heartbeat_at, created_at, updated_at
	) VALUES (?, ?, 'recovery_job', ?, ?, ?, 'active', ?, ?, ?, ?, ?)`,
		sourceLeaseID, executeAggregate.PointID, executeAggregate.JobID, executeAggregate.AttemptID,
		recoveryMigrationDigest(790001), now.Add(20*time.Minute), now.Add(30*time.Minute), now, now, now)

	deleteCheckpointID := recoveryMigrationOpaqueID(790002)
	deleteGrantID := recoveryMigrationOpaqueID(790003)
	deleteBindingDigest := recoveryMigrationDigest(790003)
	deleteGrantExpiresAt := now.Add(12 * time.Minute)
	deleteAuthorityExpiresAt := now.Add(15 * time.Minute)
	fixture.mustExec(t, db, `INSERT INTO backup_asset_recovery_checkpoints (
		id, job_id, attempt_id, sequence, phase, authority_category, operation_digest,
		prior_target_revision, next_target_revision, node_fence, attempt_fence,
		plan_binding_digest, source_revision_digest, preflight_id, preflight_revision,
		preflight_expires_at, security_decision, security_decision_digest,
		security_finding_set_digest, security_policy_revision, authority_grant_id,
		job_authority_category, authority_binding_digest, authority_expires_at,
		delete_node_revision, delete_root_revision, delete_authority_expires_at, created_at
	)
	SELECT ?, checkpoint.job_id, checkpoint.attempt_id, checkpoint.sequence + 1,
		'delete_authority_required', 'exact_mirror_delete', job.delete_set_digest,
		job.target_chain_revision, '', 1, 1, checkpoint.plan_binding_digest,
		checkpoint.source_revision_digest, checkpoint.preflight_id,
		checkpoint.preflight_revision, checkpoint.preflight_expires_at,
		checkpoint.security_decision, checkpoint.security_decision_digest,
		checkpoint.security_finding_set_digest, checkpoint.security_policy_revision,
		checkpoint.authority_grant_id, checkpoint.job_authority_category,
		checkpoint.authority_binding_digest, checkpoint.authority_expires_at,
		'delete-node-revision', 'root-v1', ?, ?
	FROM backup_asset_recovery_checkpoints AS checkpoint
	JOIN backup_asset_recovery_jobs AS job ON job.id = checkpoint.job_id
	WHERE checkpoint.id = ?`, deleteCheckpointID, deleteAuthorityExpiresAt, now.Add(4*time.Second),
		deleteAggregate.CheckpointID)
	fixture.mustExec(t, db, `INSERT INTO backup_asset_recovery_grants (
		id, plan_id, job_id, authority_category, grant_hash, actor_user_id,
		actor_session_id, binding_digest, encrypted_reason, delete_checkpoint_id,
		delete_set_digest, delete_target_revision, delete_attempt_id,
		delete_attempt_fence, delete_node_fence, expires_at, created_at, updated_at
	)
	SELECT ?, job.plan_id, job.id, 'exact_mirror_delete', ?, ?, 'receipt-delete-session',
		?, 'enc:v2:delete-reason', ?, job.delete_set_digest, job.target_chain_revision,
		attempt.id, attempt.fence, lease.fence, ?, ?, ?
	FROM backup_asset_recovery_jobs AS job
	JOIN backup_asset_recovery_attempts AS attempt ON attempt.job_id = job.id
	JOIN backup_asset_recovery_node_leases AS lease ON lease.job_id = job.id
	WHERE job.id = ?`, deleteGrantID, recoveryMigrationDigest(790004), deleteAggregate.UserID,
		deleteBindingDigest, deleteCheckpointID, deleteGrantExpiresAt, now.Add(5*time.Second),
		now.Add(5*time.Second), deleteAggregate.JobID)

	var writeGrantBinding, executeGrantBinding string
	if err := db.QueryRow(fixture.bind(`SELECT binding_digest FROM backup_asset_recovery_grants WHERE id = ?`),
		writeAggregate.GrantID).Scan(&writeGrantBinding); err != nil {
		t.Fatalf("load %s write receipt grant binding: %v", fixture.engine, err)
	}
	if err := db.QueryRow(fixture.bind(`SELECT binding_digest FROM backup_asset_recovery_grants WHERE id = ?`),
		executeAggregate.WriteGrantID).Scan(&executeGrantBinding); err != nil {
		t.Fatalf("load %s execute receipt grant binding: %v", fixture.engine, err)
	}

	newReceipt := func(idValue int, aggregate recoveryMigrationAggregate, operation, category, endpoint string) recoveryAuthorizationReceiptSeed {
		return recoveryAuthorizationReceiptSeed{
			ID: recoveryMigrationOpaqueID(idValue), PlanID: aggregate.PlanID,
			RequesterID: aggregate.UserID, Operation: operation, Category: category, Endpoint: endpoint,
			IdempotencyKeyDigest:    recoveryMigrationDigest(idValue + 10),
			IntentDigest:            recoveryMigrationDigest(idValue + 20),
			StepUpJTIDigest:         recoveryMigrationDigest(idValue + 30),
			PresentingSessionDigest: recoveryMigrationDigest(idValue + 40),
			PresentingSessionUserID: aggregate.UserID, PresentingSessionRole: "admin",
			PresentingSessionTokenVersion: 1, ProofExpiresAt: now.Add(10 * time.Minute),
			ReplayExpiresAt: now.Add(40 * time.Minute), PresentingSessionExpiresAt: now.Add(50 * time.Minute),
			ExpectedPlanTransitionRevision: 1, ResultPlanTransitionRevision: 2,
			CreatedAt: now.Add(6 * time.Second),
		}
	}

	securityReceipt := newReceipt(790010, securityAggregate, "security_override", "security_override",
		"/api/v1/recovery-plans/:id/security-overrides")
	writeReceipt := newReceipt(790011, writeAggregate, "write_authorize", "write",
		"/api/v1/recovery-plans/:id/write-authorizations")
	writeReceipt.GrantID = writeAggregate.GrantID
	writeReceipt.GrantBindingDigest = writeGrantBinding
	deleteReceipt := newReceipt(790012, deleteAggregate, "exact_mirror_delete_authorize", "exact_mirror_delete",
		"/api/v1/recovery-jobs/:id/exact-mirror-delete-authorizations")
	deleteReceipt.JobID = deleteAggregate.JobID
	deleteReceipt.CheckpointID = deleteCheckpointID
	deleteReceipt.GrantID = deleteGrantID
	deleteReceipt.AttemptID = deleteAggregate.AttemptID
	deleteReceipt.GrantBindingDigest = deleteBindingDigest
	deleteReceipt.ResultPlanTransitionRevision = deleteReceipt.ExpectedPlanTransitionRevision
	executeReceipt := newReceipt(790013, executeAggregate, "execute", "execute",
		"/api/v1/recovery-plans/:id/execute")
	executeReceipt.JobID = executeAggregate.JobID
	executeReceipt.GrantID = executeAggregate.WriteGrantID
	executeReceipt.AttemptID = executeAggregate.AttemptID
	executeReceipt.SourceLeaseID = sourceLeaseID
	executeReceipt.NodeLeaseID = executeAggregate.NodeLeaseID
	executeReceipt.GrantBindingDigest = executeGrantBinding
	executeReceipt.SourceLeaseBindingDigest = recoveryMigrationDigest(790013)
	executeReceipt.NodeLeaseFence = 1

	for _, receipt := range []recoveryAuthorizationReceiptSeed{
		securityReceipt, writeReceipt, deleteReceipt, executeReceipt,
	} {
		if err := fixture.insertRecoveryAuthorizationReceipt(t, db, receipt); err != nil {
			t.Fatalf("insert valid %s %s authorization receipt: %v", fixture.engine, receipt.Operation, err)
		}
	}

	t.Run("RequesterEndpointKeyIsUnique", func(t *testing.T) {
		duplicate := securityReceipt
		duplicate.ID = recoveryMigrationOpaqueID(790020)
		duplicate.StepUpJTIDigest = recoveryMigrationDigest(790020)
		if err := fixture.insertRecoveryAuthorizationReceipt(t, db, duplicate); err == nil {
			t.Fatalf("%s accepted duplicate requester/endpoint/key receipt", fixture.engine)
		}
	})
	t.Run("ProofDigestIsGloballyUnique", func(t *testing.T) {
		duplicate := securityReceipt
		duplicate.ID = recoveryMigrationOpaqueID(790021)
		duplicate.Endpoint = "/api/v1/recovery-plans/:id/write-authorizations"
		duplicate.IdempotencyKeyDigest = recoveryMigrationDigest(790021)
		if err := fixture.insertRecoveryAuthorizationReceipt(t, db, duplicate); err == nil {
			t.Fatalf("%s accepted globally reused step-up proof digest", fixture.engine)
		}
	})
	t.Run("OperationCategoryAndEffectAreClosed", func(t *testing.T) {
		invalid := writeReceipt
		invalid.ID = recoveryMigrationOpaqueID(790022)
		invalid.IdempotencyKeyDigest = recoveryMigrationDigest(790022)
		invalid.StepUpJTIDigest = recoveryMigrationDigest(790122)
		invalid.Category = "execute"
		if err := fixture.insertRecoveryAuthorizationReceipt(t, db, invalid); err == nil {
			t.Fatalf("%s accepted mismatched receipt operation/category/effect", fixture.engine)
		}
	})
	t.Run("DeadlinesAreOrdered", func(t *testing.T) {
		for index, mutate := range []func(*recoveryAuthorizationReceiptSeed){
			func(receipt *recoveryAuthorizationReceiptSeed) {
				receipt.ProofExpiresAt = receipt.ReplayExpiresAt.Add(time.Second)
			},
			func(receipt *recoveryAuthorizationReceiptSeed) {
				receipt.ReplayExpiresAt = receipt.PresentingSessionExpiresAt.Add(time.Second)
			},
		} {
			invalid := securityReceipt
			invalid.ID = recoveryMigrationOpaqueID(790030 + index)
			invalid.IdempotencyKeyDigest = recoveryMigrationDigest(790030 + index)
			invalid.StepUpJTIDigest = recoveryMigrationDigest(790130 + index)
			mutate(&invalid)
			if err := fixture.insertRecoveryAuthorizationReceipt(t, db, invalid); err == nil {
				t.Fatalf("%s accepted invalid receipt deadline ordering probe %d", fixture.engine, index+1)
			}
		}
	})
	t.Run("GrantExpiryDoesNotOutliveReplay", func(t *testing.T) {
		for index, receipt := range []recoveryAuthorizationReceiptSeed{writeReceipt, deleteReceipt} {
			var grantExpiresAt time.Time
			if err := db.QueryRow(fixture.bind(`SELECT expires_at
				FROM backup_asset_recovery_grants WHERE id = ?`), receipt.GrantID).
				Scan(&grantExpiresAt); err != nil {
				t.Fatalf("load %s %s grant expiry: %v", fixture.engine, receipt.Operation, err)
			}
			invalid := receipt
			invalid.ID = recoveryMigrationOpaqueID(790035 + index)
			invalid.IdempotencyKeyDigest = recoveryMigrationDigest(790035 + index)
			invalid.StepUpJTIDigest = recoveryMigrationDigest(790135 + index)
			invalid.ReplayExpiresAt = grantExpiresAt.Add(-time.Second)
			invalid.ProofExpiresAt = invalid.ReplayExpiresAt.Add(-time.Minute)
			if err := fixture.insertRecoveryAuthorizationReceipt(t, db, invalid); err == nil {
				t.Fatalf("%s accepted %s receipt whose grant outlives replay", fixture.engine, receipt.Operation)
			}
		}
	})
	t.Run("ExecuteRequiresExactSingletonSourceLease", func(t *testing.T) {
		for index, mutate := range []func(*recoveryAuthorizationReceiptSeed){
			func(receipt *recoveryAuthorizationReceiptSeed) { receipt.SourceLeaseID = nil },
			func(receipt *recoveryAuthorizationReceiptSeed) {
				receipt.SourceLeaseID = recoveryMigrationOpaqueID(799999)
			},
			func(receipt *recoveryAuthorizationReceiptSeed) { receipt.AttemptID = securityAggregate.AttemptID },
		} {
			invalid := executeReceipt
			invalid.ID = recoveryMigrationOpaqueID(790040 + index)
			invalid.IdempotencyKeyDigest = recoveryMigrationDigest(790040 + index)
			invalid.StepUpJTIDigest = recoveryMigrationDigest(790140 + index)
			mutate(&invalid)
			if err := fixture.insertRecoveryAuthorizationReceipt(t, db, invalid); err == nil {
				t.Fatalf("%s accepted invalid execute source-lease probe %d", fixture.engine, index+1)
			}
		}

		extraSourceLeaseID := recoveryMigrationOpaqueID(790045)
		fixture.mustExec(t, db, `INSERT INTO recovery_point_leases (
			id, recovery_point_id, holder_type, owner_id, attempt_id, fence_token, status,
			lease_expires_at, absolute_deadline, last_heartbeat_at, created_at, updated_at
		) VALUES (?, ?, 'recovery_job', ?, ?, ?, 'active', ?, ?, ?, ?, ?)`,
			extraSourceLeaseID, securityAggregate.PointID, executeAggregate.JobID,
			executeAggregate.AttemptID, recoveryMigrationDigest(790045),
			now.Add(20*time.Minute), now.Add(30*time.Minute), now, now, now)
		invalid := executeReceipt
		invalid.ID = recoveryMigrationOpaqueID(790046)
		invalid.IdempotencyKeyDigest = recoveryMigrationDigest(790046)
		invalid.StepUpJTIDigest = recoveryMigrationDigest(790146)
		if err := fixture.insertRecoveryAuthorizationReceipt(t, db, invalid); err == nil {
			t.Fatalf("%s accepted execute receipt with an additional real source lease", fixture.engine)
		}
		fixture.mustExec(t, db, `DELETE FROM recovery_point_leases WHERE id = ?`, extraSourceLeaseID)
	})
	t.Run("ReceiptIsImmutableAndProtectedBeforeReplayExpiry", func(t *testing.T) {
		fixture.expectExecRejectedInRollback(t, db,
			`UPDATE backup_asset_recovery_evidence SET intent_digest = ? WHERE id = ?`,
			recoveryMigrationDigest(790050), securityReceipt.ID)
		fixture.expectExecRejectedInRollback(t, db,
			`DELETE FROM backup_asset_recovery_evidence WHERE id = ?`, securityReceipt.ID)
	})

	t.Run("ParentCascadeAndDownAreProtectedUntilReaped", func(t *testing.T) {
		purgeable := fixture.seedRecoveryMigrationPurgeablePlan(t, db, "5", 205, now.Add(-4*time.Hour))
		expired := newReceipt(790060, purgeable, "security_override", "security_override",
			"/api/v1/recovery-plans/:id/security-overrides")
		expired.CreatedAt = now.Add(-3 * time.Hour)
		expired.ProofExpiresAt = now.Add(-2 * time.Hour)
		expired.ReplayExpiresAt = now.Add(-time.Hour)
		expired.PresentingSessionExpiresAt = now.Add(-30 * time.Minute)
		if err := fixture.insertRecoveryAuthorizationReceipt(t, db, expired); err != nil {
			t.Fatalf("insert expired %s authorization receipt: %v", fixture.engine, err)
		}

		livePlan := fixture.seedRecoveryMigrationPurgeablePlan(t, db, "6", 206, now)
		live := newReceipt(790061, livePlan, "security_override", "security_override",
			"/api/v1/recovery-plans/:id/security-overrides")
		if err := fixture.insertRecoveryAuthorizationReceipt(t, db, live); err != nil {
			t.Fatalf("insert live %s authorization receipt: %v", fixture.engine, err)
		}
		fixture.expectExecRejectedInRollback(t, db,
			`DELETE FROM backup_asset_recovery_plans WHERE id = ?`, livePlan.PlanID)
		if err := migrator.Steps(-1); err == nil {
			t.Fatalf("%s 000069 down accepted live authorization receipts", fixture.engine)
		}
		assertMigrationVersion(t, migrator, backupAssetRecoveryVersion)

		fixture.mustExec(t, db,
			`DELETE FROM backup_asset_recovery_evidence WHERE id = ? AND kind = 'authorization_receipt' AND replay_expires_at <= ?`,
			expired.ID, now)
		var expiredCount int
		if err := db.QueryRow(fixture.bind(`SELECT COUNT(*) FROM backup_asset_recovery_evidence WHERE id = ?`),
			expired.ID).Scan(&expiredCount); err != nil {
			t.Fatalf("count reaped %s receipt: %v", fixture.engine, err)
		}
		if expiredCount != 0 {
			t.Fatalf("%s expired receipt was not reaped", fixture.engine)
		}
	})

	t.Run("SchemaUseLatchCannotBeDeleted", func(t *testing.T) {
		fixture.insertRecoveryMigrationUseLatch(t, db, now)
		fixture.expectExecRejectedInRollback(t, db,
			`DELETE FROM backup_asset_recovery_evidence
				WHERE id = '00000000000000000000000000000069'`)
		var latchCount int
		if err := db.QueryRow(`SELECT COUNT(*) FROM backup_asset_recovery_evidence
			WHERE id = '00000000000000000000000000000069'
			  AND kind = 'schema_use_latch'`).Scan(&latchCount); err != nil {
			t.Fatalf("count %s permanent schema-use latch: %v", fixture.engine, err)
		}
		if latchCount != 1 {
			t.Fatalf("%s schema-use latch count=%d, want 1 after refused delete", fixture.engine, latchCount)
		}
	})

	t.Run("ExpiredReceiptCanReapThenPristineDown", func(t *testing.T) {
		pristineMigrator, pristineDB := fixture.openAt(t, backupAssetRecoveryVersion)
		pristineNow := now
		aggregate := fixture.seedRecoveryMigrationPurgeablePlan(t, pristineDB, "post-reap", 208, pristineNow.Add(-4*time.Hour))
		receipt := recoveryAuthorizationReceiptSeed{
			ID: recoveryMigrationOpaqueID(790080), PlanID: aggregate.PlanID,
			RequesterID: aggregate.UserID, Operation: "security_override", Category: "security_override",
			Endpoint:             "/api/v1/recovery-plans/:id/security-overrides",
			IdempotencyKeyDigest: recoveryMigrationDigest(790081),
			IntentDigest:         recoveryMigrationDigest(790082), StepUpJTIDigest: recoveryMigrationDigest(790083),
			PresentingSessionDigest: recoveryMigrationDigest(790084),
			PresentingSessionUserID: aggregate.UserID, PresentingSessionRole: "admin",
			PresentingSessionTokenVersion: 1,
			CreatedAt:                     pristineNow.Add(-4 * time.Hour), ProofExpiresAt: pristineNow.Add(-3 * time.Hour),
			ReplayExpiresAt:                pristineNow.Add(-2 * time.Hour),
			PresentingSessionExpiresAt:     pristineNow.Add(-time.Hour),
			ExpectedPlanTransitionRevision: 1, ResultPlanTransitionRevision: 2,
		}
		if err := fixture.insertRecoveryAuthorizationReceipt(t, pristineDB, receipt); err != nil {
			t.Fatalf("insert expired %s receipt for pristine down: %v", fixture.engine, err)
		}
		fixture.mustExec(t, pristineDB, `DELETE FROM backup_asset_recovery_evidence
			WHERE id = ? AND kind = 'authorization_receipt' AND replay_expires_at <= ?`,
			receipt.ID, pristineNow)
		fixture.purgeRecoveryMigrationOrdinaryRows(t, pristineDB)
		if err := pristineMigrator.Steps(-1); err != nil {
			t.Fatalf("step %s 000069 down after safe receipt reap: %v", fixture.engine, err)
		}
		assertMigrationVersion(t, pristineMigrator, backupAssetExportVersion)
		for _, table := range backupAssetRecoveryTables {
			if databaseTableExists(t, pristineDB, fixture.engine, table) {
				t.Fatalf("%s recovery table %s remains after post-reap pristine down", fixture.engine, table)
			}
		}
	})
}

// Post-GREEN regression coverage: the original Task 1 latch RED was not observed.
func TestRecoveryReviewF6UseLatchSQLite(t *testing.T) {
	newSQLiteMigrationFixture(t).test069UseLatchImmutabilityAndOrdinaryEvidenceUpdates(t)
}

func TestRecoveryReviewF6UseLatchPostgres(t *testing.T) {
	newRequiredPostgresMigrationFixture(t).test069UseLatchImmutabilityAndOrdinaryEvidenceUpdates(t)
}

// Post-GREEN regression coverage: the original Task 1 used-down RED was not observed.
func TestRecoveryReviewF6UsedDownAtomicRefusal(t *testing.T) {
	newSQLiteMigrationFixture(t).test069UsedDownIsRejectedAtomically(t)
}

func TestRecoveryGrantTerminalTransitionBindingsSQLite(t *testing.T) {
	newSQLiteMigrationFixture(t).test069GrantTerminalTransitionBindings(t)
}

func TestRecoveryGrantTerminalTransitionBindingsPostgres(t *testing.T) {
	newRequiredPostgresMigrationFixture(t).test069GrantTerminalTransitionBindings(t)
}

func TestBackupAssetMigration069PlanTargetNodeForeignKeySQLite(t *testing.T) {
	newSQLiteMigrationFixture(t).test069PlanTargetNodeForeignKey(t)
}

func TestBackupAssetMigration069PlanTargetNodeForeignKeyPostgres(t *testing.T) {
	newRequiredPostgresMigrationFixture(t).test069PlanTargetNodeForeignKey(t)
}

func TestBackupAssetMigration069TaskRunNodeSnapshotSQLite(t *testing.T) {
	newSQLiteMigrationFixture(t).test069TaskRunNodeSnapshot(t)
}

func TestBackupAssetMigration069TaskRunNodeSnapshotPostgres(t *testing.T) {
	newRequiredPostgresMigrationFixture(t).test069TaskRunNodeSnapshot(t)
}

func TestBackupAssetMigration069ActiveGrantBindingImmutabilitySQLite(t *testing.T) {
	newSQLiteMigrationFixture(t).test069ActiveGrantBindingImmutability(t)
}

func TestBackupAssetMigration069ActiveGrantBindingImmutabilityPostgres(t *testing.T) {
	newRequiredPostgresMigrationFixture(t).test069ActiveGrantBindingImmutability(t)
}

func TestBackupAssetMigration069PlanRecoveryPointOwnershipSQLite(t *testing.T) {
	newSQLiteMigrationFixture(t).test069PlanRecoveryPointOwnership(t)
}

func TestBackupAssetMigration069PlanRecoveryPointOwnershipPostgres(t *testing.T) {
	newRequiredPostgresMigrationFixture(t).test069PlanRecoveryPointOwnership(t)
}

func TestBackupAssetMigration069NodeLeaseMatchesJobTargetSQLite(t *testing.T) {
	newSQLiteMigrationFixture(t).test069NodeLeaseMatchesJobTarget(t)
}

func TestBackupAssetMigration069NodeLeaseMatchesJobTargetPostgres(t *testing.T) {
	newRequiredPostgresMigrationFixture(t).test069NodeLeaseMatchesJobTarget(t)
}

func TestBackupAssetMigration069TerminalWorkspacePhasesSQLite(t *testing.T) {
	newSQLiteMigrationFixture(t).test069TerminalWorkspacePhases(t)
}

func TestBackupAssetMigration069TerminalWorkspacePhasesPostgres(t *testing.T) {
	newRequiredPostgresMigrationFixture(t).test069TerminalWorkspacePhases(t)
}

func TestBackupAssetMigration069FrozenAuthorityBindingsSQLite(t *testing.T) {
	newSQLiteMigrationFixture(t).test069FrozenAuthorityBindings(t)
}

func TestBackupAssetMigration069FrozenAuthorityBindingsPostgres(t *testing.T) {
	newRequiredPostgresMigrationFixture(t).test069FrozenAuthorityBindings(t)
}

func TestBackupAssetMigration069TargetBindingCopiesSQLite(t *testing.T) {
	newSQLiteMigrationFixture(t).test069TargetBindingCopies(t)
}

func TestBackupAssetMigration069TargetBindingCopiesPostgres(t *testing.T) {
	newRequiredPostgresMigrationFixture(t).test069TargetBindingCopies(t)
}

func TestBackupAssetMigration069PlanJobItemOrdinalParitySQLite(t *testing.T) {
	newSQLiteMigrationFixture(t).test069PlanJobItemOrdinalParity(t)
}

func TestBackupAssetMigration069PlanJobItemOrdinalParityPostgres(t *testing.T) {
	newRequiredPostgresMigrationFixture(t).test069PlanJobItemOrdinalParity(t)
}

func TestBackupAssetMigration069SQLiteOperationSnapshot(t *testing.T) {
	newSQLiteMigrationFixture(t).test069OperationSnapshot(t)
}

func TestBackupAssetMigration069PostgresOperationSnapshot(t *testing.T) {
	newRequiredPostgresMigrationFixture(t).test069OperationSnapshot(t)
}

func TestBackupAssetMigration069JobItemOperationAndLocatorBindingsSQLite(t *testing.T) {
	newSQLiteMigrationFixture(t).test069JobItemOperationAndLocatorBindings(t)
}

func TestBackupAssetMigration069JobItemOperationAndLocatorBindingsPostgres(t *testing.T) {
	newRequiredPostgresMigrationFixture(t).test069JobItemOperationAndLocatorBindings(t)
}

func TestBackupAssetMigration069RecoveryLocatorProductSQLite(t *testing.T) {
	newSQLiteMigrationFixture(t).test069RecoveryLocatorProduct(t)
}

func TestBackupAssetMigration069RecoveryLocatorProductPostgres(t *testing.T) {
	newRequiredPostgresMigrationFixture(t).test069RecoveryLocatorProduct(t)
}

func (fixture migrationFixture) test069RecoveryLocatorProduct(t *testing.T) {
	t.Helper()
	_, db := fixture.openAt(t, backupAssetRecoveryVersion)
	for table, columns := range map[string][]string{
		"backup_asset_recovery_jobs": {
			"workspace_binding_digest",
		},
		"backup_asset_recovery_job_items": {
			"semantic_target_digest",
			"target_object_digest",
			"encrypted_target_relative_locator",
			"target_locator_key_version",
			"target_locator_cipher_version",
		},
	} {
		for _, column := range columns {
			if !databaseColumnExists(t, db, fixture.engine, table, column) {
				t.Fatalf("%s 000069 %s omits locator-product column %s", fixture.engine, table, column)
			}
		}
	}
	for _, forbidden := range []string{"verified_modified_at", "fidelity_state"} {
		if databaseColumnExists(t, db, fixture.engine, "backup_asset_recovery_job_items", forbidden) {
			t.Fatalf("%s 000069 retains unsupported recovery fidelity column %s", fixture.engine, forbidden)
		}
	}

	jobDefinition := fixture.tableDefinition(t, db, "backup_asset_recovery_jobs")
	itemDefinition := fixture.tableDefinition(t, db, "backup_asset_recovery_job_items")
	for _, fragment := range []string{"workspace_binding_digest", "workspace_phase"} {
		if !strings.Contains(jobDefinition, fragment) {
			t.Fatalf("%s job workspace product omits %q: %s", fixture.engine, fragment, jobDefinition)
		}
	}
	for _, fragment := range []string{"semantic_target_digest", "target_object_digest", "target_locator_key_version", "target_locator_cipher_version"} {
		if !strings.Contains(itemDefinition, fragment) {
			t.Fatalf("%s item locator product omits %q: %s", fixture.engine, fragment, itemDefinition)
		}
	}

	// Retain the existing operation matrix as part of the amended paired
	// product; the new columns cannot weaken its prior/post sentinels.
	fixture.test069JobItemOperationAndLocatorBindings(t)
	fixture.test069RecoveryImmutableLocatorProduct(t)
}

func (fixture migrationFixture) test069RecoveryImmutableLocatorProduct(t *testing.T) {
	t.Helper()
	_, db := fixture.openAt(t, backupAssetRecoveryVersion)
	now := time.Date(2026, time.August, 2, 12, 0, 0, 0, time.UTC)
	aggregate := fixture.seedRecoveryMigrationAggregateWithOptions(
		t, db, "b1e3-immutable", 219, now, recoveryMigrationSeedOptions{claimableAttempt: true},
	)

	var workspacePhase, encryptedWorkspace, workspaceBinding, markerBinding, workspaceOwner string
	var workspaceFence int64
	var plaintextDeadline sql.NullTime
	if err := db.QueryRow(fixture.bind(`SELECT workspace_phase,
		encrypted_workspace_relative_locator, workspace_binding_digest,
		workspace_marker_binding_digest, workspace_owner, workspace_fence, plaintext_deadline
		FROM backup_asset_recovery_jobs WHERE id = ?`), aggregate.JobID).Scan(
		&workspacePhase, &encryptedWorkspace, &workspaceBinding, &markerBinding,
		&workspaceOwner, &workspaceFence, &plaintextDeadline,
	); err != nil {
		t.Fatalf("load %s isolated none workspace product: %v", fixture.engine, err)
	}
	if workspacePhase != "none" || encryptedWorkspace == "" || len(workspaceBinding) != 64 ||
		markerBinding != "" || workspaceOwner != "" || workspaceFence != 0 || plaintextDeadline.Valid {
		t.Fatalf("%s isolated none workspace product is incomplete: phase=%q locator=%q binding=%q marker=%q owner=%q fence=%d deadline=%v",
			fixture.engine, workspacePhase, encryptedWorkspace, workspaceBinding, markerBinding,
			workspaceOwner, workspaceFence, plaintextDeadline)
	}

	for _, rewrite := range []struct {
		name string
		set  string
		arg  any
	}{
		{name: "workspace ciphertext", set: "encrypted_workspace_relative_locator = ?", arg: "enc:v2:rewritten-workspace"},
		{name: "workspace binding", set: "workspace_binding_digest = ?", arg: recoveryMigrationDigest(712001)},
		{name: "target root", set: "target_root_id = ?", arg: "rewritten-root"},
	} {
		t.Run("job immutable/"+rewrite.name, func(t *testing.T) {
			fixture.expectExecRejectedInRollback(t, db,
				"UPDATE backup_asset_recovery_jobs SET "+rewrite.set+" WHERE id = ?", rewrite.arg, aggregate.JobID)
		})
	}
	fixture.expectExecRejectedInRollback(t, db, `UPDATE backup_asset_recovery_jobs
		SET workspace_phase = 'reserved', updated_at = ? WHERE id = ?`, now.Add(time.Second), aggregate.JobID)

	for _, rewrite := range []struct {
		name string
		set  string
		arg  any
	}{
		{name: "semantic digest", set: "semantic_target_digest = ?", arg: recoveryMigrationDigest(712010)},
		{name: "final-object digest", set: "target_object_digest = ?", arg: recoveryMigrationDigest(712011)},
		{name: "locator ciphertext", set: "encrypted_target_relative_locator = ?", arg: "recovery:aead:v1:rewritten"},
		{name: "locator key version", set: "target_locator_key_version = ?", arg: 2},
		{name: "locator cipher version", set: "target_locator_cipher_version = ?", arg: 2},
		{name: "expected post digest", set: "expected_post_identity_digest = ?", arg: recoveryMigrationDigest(712012)},
		{name: "expected post bytes", set: "expected_post_bytes = ?", arg: int64(2)},
	} {
		t.Run("item immutable/"+rewrite.name, func(t *testing.T) {
			fixture.expectExecRejectedInRollback(t, db,
				"UPDATE backup_asset_recovery_job_items SET "+rewrite.set+`,
					outcome = 'failed', failure_category = 'b1e3_immutable_probe', updated_at = ?
					WHERE id = ?`,
				rewrite.arg, now.Add(time.Second), aggregate.JobItemID)
		})
	}

	fixture.mustExec(t, db, `UPDATE backup_asset_recovery_job_items
		SET outcome = 'failed', failure_category = 'b1e3_terminal', updated_at = ? WHERE id = ?`,
		now.Add(2*time.Second), aggregate.JobItemID)
	fixture.expectExecRejectedInRollback(t, db, `UPDATE backup_asset_recovery_job_items
		SET failure_category = 'b1e3_rewritten', updated_at = ? WHERE id = ?`,
		now.Add(3*time.Second), aggregate.JobItemID)

	deleteInsert := `INSERT INTO backup_asset_recovery_job_items
		(id, plan_id, job_id, plan_item_id, ordinal, operation_kind, target_path_digest,
		 semantic_target_digest, target_object_digest,
		 expected_prior_kind, expected_prior_digest, expected_post_identity_digest,
		 expected_post_bytes, expected_prior_bytes, encrypted_target_relative_locator,
		 target_locator_key_version, target_locator_cipher_version,
		 display_class, estimated_bytes, created_at, updated_at)
		VALUES (?, ?, ?, NULL, 1, 'delete', ?, ?, ?, 'present', ?, '', -1, -1, ?, 1, 1,
			'directory', 0, ?, ?)`
	fixture.expectExecRejectedInRollback(t, db, deleteInsert,
		recoveryMigrationOpaqueID(712020), aggregate.PlanID, aggregate.JobID,
		recoveryMigrationDigest(712021), recoveryMigrationDigest(712022), recoveryMigrationDigest(712023),
		recoveryMigrationDigest(712024), "recovery:aead:v1:isolated-delete", now, now)

	t.Run("unique semantic and final object digests plus in-place none", func(t *testing.T) {
		_, exactDB := fixture.openAt(t, backupAssetRecoveryVersion)
		exact := fixture.seedRecoveryMigrationExactMirrorAggregate(t, exactDB, "b1e3-unique", 220, now)
		var existingSemantic, existingObject string
		if err := exactDB.QueryRow(fixture.bind(`SELECT semantic_target_digest, target_object_digest
			FROM backup_asset_recovery_job_items WHERE id = ?`), exact.JobItemID).
			Scan(&existingSemantic, &existingObject); err != nil {
			t.Fatalf("load %s existing locator digests: %v", fixture.engine, err)
		}
		var exactPhase, exactLocator, exactBinding string
		if err := exactDB.QueryRow(fixture.bind(`SELECT workspace_phase,
			encrypted_workspace_relative_locator, workspace_binding_digest
			FROM backup_asset_recovery_jobs WHERE id = ?`), exact.JobID).
			Scan(&exactPhase, &exactLocator, &exactBinding); err != nil {
			t.Fatalf("load %s in-place none workspace product: %v", fixture.engine, err)
		}
		if exactPhase != "none" || exactLocator != "" || exactBinding != "" {
			t.Fatalf("%s in-place none retained workspace identity: phase=%q locator=%q binding=%q",
				fixture.engine, exactPhase, exactLocator, exactBinding)
		}

		insertArgs := func(id, targetPath, semantic, object, ciphertext string) []any {
			return []any{
				id, exact.PlanID, exact.JobID, targetPath, semantic, object,
				recoveryMigrationDigest(712030), ciphertext, now, now,
			}
		}
		fixture.expectExecAcceptedInRollback(t, exactDB, deleteInsert, insertArgs(
			recoveryMigrationOpaqueID(712031), recoveryMigrationDigest(712032),
			recoveryMigrationDigest(712033), recoveryMigrationDigest(712034),
			"recovery:aead:v1:unique-delete",
		)...)
		fixture.expectExecRejectedInRollback(t, exactDB, deleteInsert, insertArgs(
			recoveryMigrationOpaqueID(712035), recoveryMigrationDigest(712036),
			existingSemantic, recoveryMigrationDigest(712037),
			"recovery:aead:v1:duplicate-semantic",
		)...)
		fixture.expectExecRejectedInRollback(t, exactDB, deleteInsert, insertArgs(
			recoveryMigrationOpaqueID(712038), recoveryMigrationDigest(712039),
			recoveryMigrationDigest(712040), existingObject,
			"recovery:aead:v1:duplicate-object",
		)...)
	})
}

func (fixture migrationFixture) test069JobItemOperationAndLocatorBindings(t *testing.T) {
	t.Helper()
	_, db := fixture.openAt(t, backupAssetRecoveryVersion)
	for _, column := range []string{
		"expected_post_identity_digest",
		"expected_post_bytes",
		"expected_prior_bytes",
		"encrypted_target_relative_locator",
		"target_locator_key_version",
		"target_locator_cipher_version",
	} {
		if !databaseColumnExists(t, db, fixture.engine, "backup_asset_recovery_job_items", column) {
			t.Fatalf("%s 000069 job item omits immutable operation/locator column %s", fixture.engine, column)
		}
	}

	now := time.Date(2026, time.July, 31, 9, 0, 0, 0, time.UTC)
	aggregate := fixture.seedRecoveryMigrationAggregate(t, db, "job-item-binding", 218, now, true)
	if _, err := db.Exec(fixture.bind(`UPDATE backup_asset_recovery_job_items
		SET expected_post_bytes = ?, updated_at = ? WHERE id = ?`), 2, now.Add(time.Second), aggregate.JobItemID); err == nil {
		t.Fatalf("%s 000069 allowed immutable job-item operation binding update", fixture.engine)
	}

	type jobItemProduct struct {
		name                   string
		operationKind          string
		planItem               bool
		expectedPriorKind      string
		expectedPriorDigest    string
		expectedPostDigest     string
		expectedPostBytes      int64
		expectedPriorBytes     int64
		encryptedTargetLocator string
		keyVersion             int
		cipherVersion          int
		wantValid              bool
	}
	priorDigest := recoveryMigrationDigest(400)
	postDigest := recoveryMigrationDigest(401)
	products := []jobItemProduct{
		{
			name: "valid create", operationKind: "create", planItem: true,
			expectedPriorKind: "absent", expectedPostDigest: postDigest,
			expectedPostBytes: 1, expectedPriorBytes: -1,
			encryptedTargetLocator: "enc:v2:create", keyVersion: 1, cipherVersion: 1, wantValid: true,
		},
		{
			name: "valid overwrite", operationKind: "overwrite", planItem: true,
			expectedPriorKind: "present", expectedPriorDigest: priorDigest, expectedPostDigest: postDigest,
			expectedPostBytes: 2, expectedPriorBytes: 1,
			encryptedTargetLocator: "enc:v2:overwrite", keyVersion: 1, cipherVersion: 1, wantValid: true,
		},
		{
			name: "valid skip", operationKind: "skip", planItem: true,
			expectedPriorKind: "present", expectedPriorDigest: priorDigest, expectedPostDigest: priorDigest,
			expectedPostBytes: -1, expectedPriorBytes: 1,
			encryptedTargetLocator: "enc:v2:skip", keyVersion: 1, cipherVersion: 1, wantValid: true,
		},
		{
			name: "valid delete", operationKind: "delete", planItem: false,
			expectedPriorKind: "present", expectedPriorDigest: priorDigest,
			expectedPostBytes: -1, expectedPriorBytes: -1,
			encryptedTargetLocator: "enc:v2:delete", keyVersion: 1, cipherVersion: 1, wantValid: true,
		},
		{
			name: "create empty post digest", operationKind: "create", planItem: true,
			expectedPriorKind: "absent", expectedPostBytes: 1, expectedPriorBytes: -1,
			encryptedTargetLocator: "enc:v2:create-empty", keyVersion: 1, cipherVersion: 1,
		},
		{
			name: "create missing post bytes", operationKind: "create", planItem: true,
			expectedPriorKind: "absent", expectedPostDigest: postDigest,
			expectedPostBytes: -1, expectedPriorBytes: -1,
			encryptedTargetLocator: "enc:v2:create-post-bytes", keyVersion: 1, cipherVersion: 1,
		},
		{
			name: "create carries prior bytes", operationKind: "create", planItem: true,
			expectedPriorKind: "absent", expectedPostDigest: postDigest,
			expectedPostBytes: 1, expectedPriorBytes: 0,
			encryptedTargetLocator: "enc:v2:create-prior-bytes", keyVersion: 1, cipherVersion: 1,
		},
		{
			name: "overwrite empty post digest", operationKind: "overwrite", planItem: true,
			expectedPriorKind: "present", expectedPriorDigest: priorDigest,
			expectedPostBytes: 1, expectedPriorBytes: 1,
			encryptedTargetLocator: "enc:v2:overwrite-empty", keyVersion: 1, cipherVersion: 1,
		},
		{
			name: "overwrite missing post bytes", operationKind: "overwrite", planItem: true,
			expectedPriorKind: "present", expectedPriorDigest: priorDigest, expectedPostDigest: postDigest,
			expectedPostBytes: -1, expectedPriorBytes: 1,
			encryptedTargetLocator: "enc:v2:overwrite-post-bytes", keyVersion: 1, cipherVersion: 1,
		},
		{
			name: "overwrite missing prior bytes", operationKind: "overwrite", planItem: true,
			expectedPriorKind: "present", expectedPriorDigest: priorDigest, expectedPostDigest: postDigest,
			expectedPostBytes: 1, expectedPriorBytes: -1,
			encryptedTargetLocator: "enc:v2:overwrite-prior-bytes", keyVersion: 1, cipherVersion: 1,
		},
		{
			name: "skip changes post digest", operationKind: "skip", planItem: true,
			expectedPriorKind: "present", expectedPriorDigest: priorDigest, expectedPostDigest: postDigest,
			expectedPostBytes: -1, expectedPriorBytes: 1,
			encryptedTargetLocator: "enc:v2:skip-post", keyVersion: 1, cipherVersion: 1,
		},
		{
			name: "skip carries post bytes", operationKind: "skip", planItem: true,
			expectedPriorKind: "present", expectedPriorDigest: priorDigest, expectedPostDigest: priorDigest,
			expectedPostBytes: 0, expectedPriorBytes: 1,
			encryptedTargetLocator: "enc:v2:skip-post-bytes", keyVersion: 1, cipherVersion: 1,
		},
		{
			name: "skip missing prior bytes", operationKind: "skip", planItem: true,
			expectedPriorKind: "present", expectedPriorDigest: priorDigest, expectedPostDigest: priorDigest,
			expectedPostBytes: -1, expectedPriorBytes: -1,
			encryptedTargetLocator: "enc:v2:skip-prior-bytes", keyVersion: 1, cipherVersion: 1,
		},
		{
			name: "delete carries post digest", operationKind: "delete", planItem: false,
			expectedPriorKind: "present", expectedPriorDigest: priorDigest, expectedPostDigest: postDigest,
			expectedPostBytes: -1, expectedPriorBytes: -1,
			encryptedTargetLocator: "enc:v2:delete-post", keyVersion: 1, cipherVersion: 1,
		},
		{
			name: "delete carries post bytes", operationKind: "delete", planItem: false,
			expectedPriorKind: "present", expectedPriorDigest: priorDigest,
			expectedPostBytes: 0, expectedPriorBytes: -1,
			encryptedTargetLocator: "enc:v2:delete-post-bytes", keyVersion: 1, cipherVersion: 1,
		},
		{
			name: "delete carries prior bytes", operationKind: "delete", planItem: false,
			expectedPriorKind: "present", expectedPriorDigest: priorDigest,
			expectedPostBytes: -1, expectedPriorBytes: 0,
			encryptedTargetLocator: "enc:v2:delete-prior-bytes", keyVersion: 1, cipherVersion: 1,
		},
		{
			name: "empty encrypted locator", operationKind: "create", planItem: true,
			expectedPriorKind: "absent", expectedPostDigest: postDigest,
			expectedPostBytes: 1, expectedPriorBytes: -1, keyVersion: 1, cipherVersion: 1,
		},
		{
			name: "zero locator key version", operationKind: "create", planItem: true,
			expectedPriorKind: "absent", expectedPostDigest: postDigest,
			expectedPostBytes: 1, expectedPriorBytes: -1,
			encryptedTargetLocator: "enc:v2:key-zero", cipherVersion: 1,
		},
		{
			name: "zero locator cipher version", operationKind: "create", planItem: true,
			expectedPriorKind: "absent", expectedPostDigest: postDigest,
			expectedPostBytes: 1, expectedPriorBytes: -1,
			encryptedTargetLocator: "enc:v2:cipher-zero", keyVersion: 1,
		},
	}
	matrixAggregate := fixture.seedRecoveryMigrationExactMirrorAggregate(t, db, "job-item-product", 219, now.Add(2*time.Second))
	for index, product := range products {
		t.Run(product.name, func(t *testing.T) {
			fixture.mustExec(t, db, `DELETE FROM backup_asset_recovery_job_items WHERE id = ?`, matrixAggregate.JobItemID)
			var planItemID any
			if product.planItem {
				planItemID = matrixAggregate.PlanItemID
			}
			_, err := db.Exec(fixture.bind(`INSERT INTO backup_asset_recovery_job_items
				(id, plan_id, job_id, plan_item_id, ordinal, operation_kind, target_path_digest,
				 semantic_target_digest, target_object_digest,
				 expected_prior_kind, expected_prior_digest, expected_post_identity_digest,
				 expected_post_bytes, expected_prior_bytes, encrypted_target_relative_locator,
				 target_locator_key_version, target_locator_cipher_version,
				 display_class, estimated_bytes, created_at, updated_at)
				VALUES (?, ?, ?, ?, 0, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'regular', 1, ?, ?)`),
				matrixAggregate.JobItemID, matrixAggregate.PlanID, matrixAggregate.JobID, planItemID,
				product.operationKind, recoveryMigrationDigest(500+index),
				recoveryMigrationDigest(700+index), recoveryMigrationDigest(800+index), product.expectedPriorKind,
				product.expectedPriorDigest, product.expectedPostDigest, product.expectedPostBytes,
				product.expectedPriorBytes, product.encryptedTargetLocator, product.keyVersion,
				product.cipherVersion, now, now)
			if err == nil {
				fixture.mustExec(t, db, `DELETE FROM backup_asset_recovery_job_items WHERE id = ?`, matrixAggregate.JobItemID)
			}
			if product.wantValid && err != nil {
				t.Fatalf("%s 000069 rejected valid %s job-item product: %v", fixture.engine, product.name, err)
			}
			if !product.wantValid && err == nil {
				t.Fatalf("%s 000069 accepted invalid %s job-item product", fixture.engine, product.name)
			}
		})
	}

	t.Run("one-way terminal projection", func(t *testing.T) {
		projection := fixture.seedRecoveryMigrationAggregate(t, db, "job-item-projection", 220, now.Add(3*time.Second), true)
		verifiedAt := now.Add(4 * time.Second)
		if _, err := db.Exec(fixture.bind(`UPDATE backup_asset_recovery_job_items
			SET outcome = 'succeeded', updated_at = ? WHERE id = ?`), verifiedAt, projection.JobItemID); err == nil {
			t.Fatalf("%s 000069 accepted incomplete succeeded job-item projection", fixture.engine)
		}
		if _, err := db.Exec(fixture.bind(`UPDATE backup_asset_recovery_job_items
			SET outcome = 'succeeded', bytes_written = 1, verified_size = 1,
			    verified_digest = expected_post_identity_digest, updated_at = ?
			WHERE id = ?`), verifiedAt, projection.JobItemID); err != nil {
			t.Fatalf("%s 000069 rejected pending-to-terminal job-item projection: %v", fixture.engine, err)
		}
		if _, err := db.Exec(fixture.bind(`UPDATE backup_asset_recovery_job_items
			SET updated_at = ? WHERE id = ?`), verifiedAt.Add(time.Second), projection.JobItemID); err == nil {
			t.Fatalf("%s 000069 allowed terminal job-item rewrite", fixture.engine)
		}
	})

	t.Run("operation-specific terminal outcome", func(t *testing.T) {
		skip := fixture.seedRecoveryMigrationAggregate(t, db, "job-item-skip-projection", 221, now.Add(5*time.Second), true)
		fixture.mustExec(t, db, `DELETE FROM backup_asset_recovery_job_items WHERE id = ?`, skip.JobItemID)
		prior := recoveryMigrationDigest(600)
		fixture.mustExec(t, db, `INSERT INTO backup_asset_recovery_job_items
			(id, plan_id, job_id, plan_item_id, ordinal, operation_kind, target_path_digest,
			 semantic_target_digest, target_object_digest,
			 expected_prior_kind, expected_prior_digest, expected_post_identity_digest,
			 expected_post_bytes, expected_prior_bytes, encrypted_target_relative_locator,
			 target_locator_key_version, target_locator_cipher_version,
			 display_class, estimated_bytes, created_at, updated_at)
			VALUES (?, ?, ?, ?, 0, 'skip', ?, ?, ?, 'present', ?, ?, -1, 1, ?, 1, 1, 'regular', 1, ?, ?)`,
			skip.JobItemID, skip.PlanID, skip.JobID, skip.PlanItemID, recoveryMigrationDigest(601),
			recoveryMigrationDigest(602), recoveryMigrationDigest(603),
			prior, prior, "enc:v2:skip-projection", now, now)
		if _, err := db.Exec(fixture.bind(`UPDATE backup_asset_recovery_job_items
			SET outcome = 'succeeded', updated_at = ? WHERE id = ?`), now.Add(6*time.Second), skip.JobItemID); err == nil {
			t.Fatalf("%s 000069 allowed skip job item to project succeeded", fixture.engine)
		}
		if _, err := db.Exec(fixture.bind(`UPDATE backup_asset_recovery_job_items
			SET outcome = 'skipped', verified_size = 1, verified_digest = ?,
			    updated_at = ? WHERE id = ?`), prior, now.Add(6*time.Second), skip.JobItemID); err != nil {
			t.Fatalf("%s 000069 rejected valid skip terminal projection: %v", fixture.engine, err)
		}
	})
}

func TestBackupAssetMigration069TerminalAttemptIntegritySQLite(t *testing.T) {
	newSQLiteMigrationFixture(t).test069TerminalAttemptIntegrity(t)
}

func TestBackupAssetMigration069TerminalAttemptIntegrityPostgres(t *testing.T) {
	newRequiredPostgresMigrationFixture(t).test069TerminalAttemptIntegrity(t)
}

func TestBackupAssetMigration069InitialWorkerClaimHandoffSQLite(t *testing.T) {
	newSQLiteMigrationFixture(t).test069InitialWorkerClaimHandoff(t)
}

func TestBackupAssetMigration069InitialWorkerClaimHandoffPostgres(t *testing.T) {
	newRequiredPostgresMigrationFixture(t).test069InitialWorkerClaimHandoff(t)
}

func TestBackupAssetRecoveryWorkerBehaviorSQLite(t *testing.T) {
	newSQLiteMigrationFixture(t).test069WorkerClaimHeartbeatAndTakeover(t)
}

func TestBackupAssetRecoveryWorkerBehaviorPostgres(t *testing.T) {
	newRequiredPostgresRecoveryMigrationFixture(t).test069WorkerClaimHeartbeatAndTakeover(t)
}

func TestBackupAssetRecoveryWorkerFirstWriteSQLite(t *testing.T) {
	newSQLiteMigrationFixture(t).test069WorkerFirstWrite(t)
}

func TestBackupAssetRecoveryWorkerFirstWritePostgres(t *testing.T) {
	newRequiredPostgresRecoveryMigrationFixture(t).test069WorkerFirstWrite(t)
}

func TestBackupAssetMigration069WorkerEvidenceBindingsSQLite(t *testing.T) {
	newSQLiteMigrationFixture(t).test069WorkerEvidenceBindings(t)
}

func TestBackupAssetMigration069WorkerEvidenceBindingsPostgres(t *testing.T) {
	newRequiredPostgresRecoveryMigrationFixture(t).test069WorkerEvidenceBindings(t)
}

func TestBackupAssetMigration069UnresolvedOperationOutcomeSQLite(t *testing.T) {
	newSQLiteMigrationFixture(t).test069UnresolvedOperationOutcome(t)
}

func TestBackupAssetMigration069UnresolvedOperationOutcomePostgres(t *testing.T) {
	newRequiredPostgresRecoveryMigrationFixture(t).test069UnresolvedOperationOutcome(t)
}

func (fixture migrationFixture) test069UnresolvedOperationOutcome(t *testing.T) {
	t.Helper()
	_, db := fixture.openAt(t, backupAssetRecoveryVersion)
	for _, column := range []string{
		"job_item_id",
		"unresolved_category",
		"write_result_digest",
		"write_target_revision",
		"observation_digest",
		"observed_target_revision",
		"observed_presence",
		"source_revalidation_outcome",
	} {
		if !databaseColumnExists(t, db, fixture.engine, "backup_asset_recovery_checkpoints", column) {
			t.Fatalf("%s 000069 recovery checkpoint omits unresolved-outcome column %s", fixture.engine, column)
		}
	}

	checkpointDefinition := strings.ToLower(
		fixture.tableDefinition(t, db, "backup_asset_recovery_checkpoints"),
	)
	for _, fragment := range []string{
		"operation_unresolved",
		"revision_disagreement",
		"verification_mismatch",
		"write_result_invalid",
		"observation_invalid",
		"matched",
		"drifted",
		"failed",
	} {
		if !strings.Contains(checkpointDefinition, fragment) {
			t.Fatalf("%s 000069 unresolved checkpoint contract omits %q: %s",
				fixture.engine, fragment, checkpointDefinition)
		}
	}

	jobDefinition := strings.ToLower(fixture.tableDefinition(t, db, "backup_asset_recovery_jobs"))
	itemDefinition := strings.ToLower(fixture.tableDefinition(t, db, "backup_asset_recovery_job_items"))
	for table, definition := range map[string]string{
		"backup_asset_recovery_jobs":      jobDefinition,
		"backup_asset_recovery_job_items": itemDefinition,
	} {
		if !strings.Contains(definition, "remote_outcome_unresolved") {
			t.Fatalf("%s 000069 %s omits remote_outcome_unresolved failure category: %s",
				fixture.engine, table, definition)
		}
	}

	fixture.test069ConsumedDeleteUnresolvedHistoryMatrix(t, db, 210)
}

func (fixture migrationFixture) test069WholeTask6Closure(t *testing.T) {
	t.Helper()
	t.Run("post-arm and adoption unresolved products", fixture.test069WholeTask6UnresolvedProducts)
	t.Run("item-bound operation and source failure", fixture.test069WholeTask6OperationEvidence)
	t.Run("workspace one-way phases", fixture.test069WholeTask6WorkspacePhases)
}

func (fixture migrationFixture) test069WorkspaceCleanupOwnership(t *testing.T) {
	t.Helper()
	_, contractDB := fixture.openAt(t, backupAssetRecoveryVersion)
	for _, column := range []string{
		"workspace_cleanup_phase",
		"workspace_cleanup_owner",
		"workspace_cleanup_lease_expires_at",
		"workspace_cleanup_fence",
		"workspace_cleanup_node_lease_id",
		"workspace_cleanup_node_fence",
		"workspace_cleanup_attempt",
	} {
		if !databaseColumnExists(t, contractDB, fixture.engine, "backup_asset_recovery_jobs", column) {
			t.Fatalf("%s 000069 recovery jobs omit workspace-cleanup column %s", fixture.engine, column)
		}
	}
	jobDefinition := strings.ToLower(fixture.tableDefinition(t, contractDB, "backup_asset_recovery_jobs"))
	for _, fragment := range []string{
		"workspace_cleanup_phase",
		"workspace_cleanup_node_lease_id",
		"workspace_cleanup_attempt",
		"tombstoned",
	} {
		if !strings.Contains(jobDefinition, fragment) {
			t.Fatalf("%s workspace-cleanup job definition omits %q: %s", fixture.engine, fragment, jobDefinition)
		}
	}
	if err := contractDB.Close(); err != nil {
		t.Fatalf("close %s workspace-cleanup contract database: %v", fixture.engine, err)
	}

	seedCleanupDue := func(t *testing.T, sequence int) (*sql.DB, recoveryMigrationAggregate, time.Time) {
		t.Helper()
		_, db := fixture.openAt(t, backupAssetRecoveryVersion)
		now := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
		aggregate := fixture.seedRecoveryMigrationAggregateWithOptions(
			t, db, fmt.Sprintf("%x", sequence%16), sequence, now,
			recoveryMigrationSeedOptions{activeAttempt: true, workspacePhase: "cleanup_due"},
		)
		closedAt := now.Add(time.Second)
		fixture.mustExec(t, db, `UPDATE backup_asset_recovery_attempts
			SET state = 'failed', closed_at = ?, updated_at = ? WHERE id = ?`,
			closedAt, closedAt, aggregate.AttemptID)
		fixture.mustExec(t, db, `UPDATE backup_asset_recovery_node_leases
			SET state = 'released', released_at = ?, updated_at = ? WHERE id = ?`,
			closedAt, closedAt, aggregate.NodeLeaseID)
		fixture.mustExec(t, db, `UPDATE backup_asset_recovery_jobs
			SET state = 'failed', failure_category = 'cleanup_required',
			    transition_revision = transition_revision + 1, updated_at = ? WHERE id = ?`,
			now.Add(2*time.Second), aggregate.JobID)
		return db, aggregate, now
	}
	createCleanupLease := func(
		t *testing.T,
		db *sql.DB,
		aggregate recoveryMigrationAggregate,
		id, owner string,
		fence int64,
		createdAt, expiresAt time.Time,
	) {
		t.Helper()
		fixture.mustExec(t, db, `INSERT INTO backup_asset_recovery_node_leases
			(id, node_id, holder_kind, job_id, attempt_id, owner_id, fence, state,
			 lease_expires_at, created_at, updated_at)
			VALUES (?, ?, 'recovery_cleanup', ?, NULL, ?, ?, 'active', ?, ?, ?)`,
			id, aggregate.NodeID, aggregate.JobID, owner, fence, expiresAt, createdAt, createdAt)
	}

	t.Run("neutral claim failure retry and tombstone shapes", func(t *testing.T) {
		db, aggregate, now := seedCleanupDue(t, 260)
		var phase, owner string
		var leaseExpiresAt sql.NullTime
		var cleanupFence, nodeFence, attempt int64
		var nodeLeaseID sql.NullString
		if err := db.QueryRow(fixture.bind(`SELECT workspace_cleanup_phase,
			workspace_cleanup_owner, workspace_cleanup_lease_expires_at,
			workspace_cleanup_fence, workspace_cleanup_node_lease_id,
			workspace_cleanup_node_fence, workspace_cleanup_attempt
			FROM backup_asset_recovery_jobs WHERE id = ?`), aggregate.JobID).Scan(
			&phase, &owner, &leaseExpiresAt, &cleanupFence, &nodeLeaseID, &nodeFence, &attempt,
		); err != nil {
			t.Fatalf("load %s neutral workspace cleanup tuple: %v", fixture.engine, err)
		}
		if phase != "claimed" || owner != "" || leaseExpiresAt.Valid || cleanupFence != 0 ||
			nodeLeaseID.Valid || nodeFence != 0 || attempt != 0 {
			t.Fatalf("%s neutral workspace cleanup tuple is invalid: phase=%q owner=%q lease=%v cleanup_fence=%d node=%v node_fence=%d attempt=%d",
				fixture.engine, phase, owner, leaseExpiresAt, cleanupFence, nodeLeaseID, nodeFence, attempt)
		}

		firstLeaseID := recoveryMigrationOpaqueID(899920)
		firstOwner := "workspace-cleanup-owner-a"
		firstExpiry := now.Add(10 * time.Minute)
		createCleanupLease(t, db, aggregate, firstLeaseID, firstOwner, 2, now.Add(3*time.Second), firstExpiry)
		fixture.mustExec(t, db, `UPDATE backup_asset_recovery_jobs
			SET workspace_cleanup_owner = ?, workspace_cleanup_lease_expires_at = ?,
			    workspace_cleanup_fence = 1, workspace_cleanup_node_lease_id = ?,
			    workspace_cleanup_node_fence = 2, workspace_cleanup_attempt = 1,
			    updated_at = ? WHERE id = ?`,
			firstOwner, firstExpiry, firstLeaseID, now.Add(3*time.Second), aggregate.JobID)
		renewedExpiry := firstExpiry.Add(time.Minute)
		fixture.mustExec(t, db, `UPDATE backup_asset_recovery_node_leases
			SET lease_expires_at = ?, updated_at = ? WHERE id = ?`,
			renewedExpiry, now.Add(4*time.Second), firstLeaseID)
		fixture.mustExec(t, db, `UPDATE backup_asset_recovery_jobs
			SET workspace_cleanup_lease_expires_at = ?, updated_at = ? WHERE id = ?`,
			renewedExpiry, now.Add(4*time.Second), aggregate.JobID)

		fixture.expectExecRejectedInRollback(t, db, `UPDATE backup_asset_recovery_jobs
			SET workspace_cleanup_owner = 'workspace-cleanup-owner-b', updated_at = ? WHERE id = ?`,
			now.Add(4*time.Second), aggregate.JobID)
		fixture.expectExecRejectedInRollback(t, db, `UPDATE backup_asset_recovery_jobs
			SET workspace_owner = 'rewritten-execution-owner', updated_at = ? WHERE id = ?`,
			now.Add(4*time.Second), aggregate.JobID)
		fixture.expectExecRejectedInRollback(t, db, `UPDATE backup_asset_recovery_jobs
			SET workspace_cleanup_phase = 'validated', updated_at = ? WHERE id = ?`,
			now.Add(4*time.Second), aggregate.JobID)

		fixture.mustExec(t, db, `UPDATE backup_asset_recovery_jobs
			SET workspace_cleanup_phase = 'revoked', updated_at = ? WHERE id = ?`,
			now.Add(4*time.Second), aggregate.JobID)
		fixture.mustExec(t, db, `UPDATE backup_asset_recovery_jobs
			SET workspace_cleanup_phase = 'drained', updated_at = ? WHERE id = ?`,
			now.Add(5*time.Second), aggregate.JobID)
		fixture.mustExec(t, db, `UPDATE backup_asset_recovery_node_leases
			SET state = 'released', released_at = ?, updated_at = ? WHERE id = ?`,
			now.Add(6*time.Second), now.Add(6*time.Second), firstLeaseID)
		fixture.mustExec(t, db, `UPDATE backup_asset_recovery_jobs
			SET workspace_cleanup_owner = '', workspace_cleanup_lease_expires_at = NULL,
			    workspace_cleanup_node_lease_id = NULL, workspace_cleanup_node_fence = 0,
			    updated_at = ? WHERE id = ?`, now.Add(6*time.Second), aggregate.JobID)

		secondLeaseID := recoveryMigrationOpaqueID(899921)
		secondOwner := "workspace-cleanup-owner-b"
		secondExpiry := now.Add(20 * time.Minute)
		createCleanupLease(t, db, aggregate, secondLeaseID, secondOwner, 3, now.Add(7*time.Second), secondExpiry)
		for _, rejectedPhase := range []string{"claimed", "revoked", "validated", "delete_started", "deleted"} {
			fixture.expectExecRejectedInRollback(t, db, `UPDATE backup_asset_recovery_jobs
				SET workspace_cleanup_phase = ?, workspace_cleanup_owner = ?,
				    workspace_cleanup_lease_expires_at = ?, workspace_cleanup_fence = 2,
				    workspace_cleanup_node_lease_id = ?, workspace_cleanup_node_fence = 3,
				    workspace_cleanup_attempt = 2, updated_at = ? WHERE id = ?`,
				rejectedPhase, secondOwner, secondExpiry, secondLeaseID, now.Add(7*time.Second), aggregate.JobID)
		}
		fixture.mustExec(t, db, `UPDATE backup_asset_recovery_jobs
			SET workspace_cleanup_phase = 'drained', workspace_cleanup_owner = ?,
			    workspace_cleanup_lease_expires_at = ?, workspace_cleanup_fence = 2,
			    workspace_cleanup_node_lease_id = ?, workspace_cleanup_node_fence = 3,
			    workspace_cleanup_attempt = 2, updated_at = ? WHERE id = ?`,
			secondOwner, secondExpiry, secondLeaseID, now.Add(7*time.Second), aggregate.JobID)
		for index, nextPhase := range []string{"validated", "delete_started", "deleted"} {
			fixture.mustExec(t, db, `UPDATE backup_asset_recovery_jobs
				SET workspace_cleanup_phase = ?, updated_at = ? WHERE id = ?`,
				nextPhase, now.Add(time.Duration(8+index)*time.Second), aggregate.JobID)
		}
		fixture.mustExec(t, db, `UPDATE backup_asset_recovery_node_leases
			SET state = 'released', released_at = ?, updated_at = ? WHERE id = ?`,
			now.Add(12*time.Second), now.Add(12*time.Second), secondLeaseID)
		fixture.mustExec(t, db, `UPDATE backup_asset_recovery_jobs
			SET workspace_phase = 'workspace_cleaned', workspace_cleanup_phase = 'tombstoned',
			    workspace_cleanup_owner = '', workspace_cleanup_lease_expires_at = NULL,
			    workspace_cleanup_node_lease_id = NULL, workspace_cleanup_node_fence = 0,
			    updated_at = ? WHERE id = ?`, now.Add(12*time.Second), aggregate.JobID)
		fixture.expectExecRejectedInRollback(t, db, `UPDATE backup_asset_recovery_jobs
			SET workspace_phase = 'cleanup_due', workspace_cleanup_phase = 'deleted',
			    updated_at = ? WHERE id = ?`, now.Add(14*time.Second), aggregate.JobID)
	})

	t.Run("expired takeover preserves durable phase and advances fences", func(t *testing.T) {
		db, aggregate, now := seedCleanupDue(t, 261)
		oldLeaseID := recoveryMigrationOpaqueID(899930)
		oldOwner := "workspace-cleanup-expired-owner"
		oldCreatedAt := now.Add(time.Second)
		oldExpiry := now.Add(30 * time.Second)
		createCleanupLease(t, db, aggregate, oldLeaseID, oldOwner, 2, oldCreatedAt, oldExpiry)
		fixture.mustExec(t, db, `UPDATE backup_asset_recovery_jobs
			SET workspace_cleanup_owner = ?, workspace_cleanup_lease_expires_at = ?,
			    workspace_cleanup_fence = 1, workspace_cleanup_node_lease_id = ?,
			    workspace_cleanup_node_fence = 2, workspace_cleanup_attempt = 1,
			    updated_at = ? WHERE id = ?`,
			oldOwner, oldExpiry, oldLeaseID, now.Add(3*time.Second), aggregate.JobID)
		for index, nextPhase := range []string{"revoked", "drained"} {
			fixture.mustExec(t, db, `UPDATE backup_asset_recovery_jobs
				SET workspace_cleanup_phase = ?, updated_at = ? WHERE id = ?`,
				nextPhase, now.Add(time.Duration(4+index)*time.Second), aggregate.JobID)
		}
		fixture.mustExec(t, db, `UPDATE backup_asset_recovery_node_leases
			SET state = 'expired', released_at = ?, updated_at = ? WHERE id = ?`,
			now.Add(5*time.Second), now.Add(5*time.Second), oldLeaseID)

		newLeaseID := recoveryMigrationOpaqueID(899931)
		newOwner := "workspace-cleanup-takeover-owner"
		newExpiry := now.Add(15 * time.Minute)
		createCleanupLease(t, db, aggregate, newLeaseID, newOwner, 3, now.Add(5*time.Second), newExpiry)
		fixture.expectExecRejectedInRollback(t, db, `UPDATE backup_asset_recovery_jobs
			SET workspace_cleanup_phase = 'claimed', workspace_cleanup_owner = ?,
			    workspace_cleanup_lease_expires_at = ?, workspace_cleanup_fence = 2,
			    workspace_cleanup_node_lease_id = ?, workspace_cleanup_node_fence = 3,
			    workspace_cleanup_attempt = 2, updated_at = ? WHERE id = ?`,
			newOwner, newExpiry, newLeaseID, now.Add(5*time.Second), aggregate.JobID)
		fixture.expectExecRejectedInRollback(t, db, `UPDATE backup_asset_recovery_jobs
			SET workspace_cleanup_owner = ?, workspace_cleanup_lease_expires_at = ?,
			    workspace_cleanup_node_lease_id = ?, workspace_cleanup_node_fence = 3,
			    workspace_cleanup_attempt = 2, updated_at = ? WHERE id = ?`,
			newOwner, newExpiry, newLeaseID, now.Add(5*time.Second), aggregate.JobID)
		fixture.mustExec(t, db, `UPDATE backup_asset_recovery_jobs
			SET workspace_cleanup_owner = ?, workspace_cleanup_lease_expires_at = ?,
			    workspace_cleanup_fence = 2, workspace_cleanup_node_lease_id = ?,
			    workspace_cleanup_node_fence = 3, workspace_cleanup_attempt = 2,
			    updated_at = ? WHERE id = ?`,
			newOwner, newExpiry, newLeaseID, now.Add(5*time.Second), aggregate.JobID)
	})

	t.Run("node lease binding rejects non-cleanup provenance", func(t *testing.T) {
		db, aggregate, now := seedCleanupDue(t, 262)
		fixture.expectExecRejectedInRollback(t, db, `UPDATE backup_asset_recovery_jobs
			SET workspace_cleanup_owner = 'recovery-worker',
			    workspace_cleanup_lease_expires_at = ?, workspace_cleanup_fence = 1,
			    workspace_cleanup_node_lease_id = ?, workspace_cleanup_node_fence = 1,
			    workspace_cleanup_attempt = 1, updated_at = ? WHERE id = ?`,
			now.Add(10*time.Minute), aggregate.NodeLeaseID, now.Add(3*time.Second), aggregate.JobID)
	})
}

func (fixture migrationFixture) test069WholeTask6UnresolvedProducts(t *testing.T) {
	t.Helper()
	_, db := fixture.openAt(t, backupAssetRecoveryVersion)
	now := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	aggregate := fixture.seedRecoveryMigrationAggregate(t, db, "5", 225, now, true)
	fixture.mustExec(t, db, `UPDATE backup_asset_recovery_attempts
		SET mutation_armed = ?, updated_at = ? WHERE id = ?`, true, now.Add(time.Second), aggregate.AttemptID)

	insert := `INSERT INTO backup_asset_recovery_checkpoints
		(id, job_id, job_item_id, attempt_id, sequence, phase, authority_category,
		 operation_digest, prior_target_revision, next_target_revision,
		 unresolved_category, write_result_digest, write_target_revision,
		 observation_digest, observed_target_revision, observed_presence,
		 source_revalidation_outcome, node_fence, attempt_fence,
		 plan_binding_digest, source_revision_digest, preflight_id, preflight_revision,
		 preflight_expires_at, security_decision, security_decision_digest,
		 security_finding_set_digest, security_policy_revision, authority_grant_id,
		 job_authority_category, authority_binding_digest, authority_expires_at, created_at)
	SELECT ?, job.id, item.id, checkpoint.attempt_id, 1, 'operation_unresolved', 'write',
	       ?, job.target_chain_revision, '', ?, ?, ?, ?, ?, ?, 'matched', 1, 1,
	       checkpoint.plan_binding_digest, checkpoint.source_revision_digest,
	       checkpoint.preflight_id, checkpoint.preflight_revision, checkpoint.preflight_expires_at,
	       checkpoint.security_decision, checkpoint.security_decision_digest,
	       checkpoint.security_finding_set_digest, checkpoint.security_policy_revision,
	       checkpoint.authority_grant_id, checkpoint.job_authority_category,
	       checkpoint.authority_binding_digest, checkpoint.authority_expires_at, ?
	FROM backup_asset_recovery_jobs AS job
	JOIN backup_asset_recovery_job_items AS item ON item.job_id = job.id
	JOIN backup_asset_recovery_checkpoints AS checkpoint
	  ON checkpoint.id = ? AND checkpoint.job_id = job.id
	WHERE job.id = ? AND item.id = ?`
	type product struct {
		name                                               string
		category, writeDigest, writeRevision               string
		observationDigest, observedRevision, observedState string
	}
	products := []product{
		{name: "write call failed without product", category: "write_result_invalid"},
		{
			name: "verify call failed after write", category: "observation_invalid",
			writeDigest: recoveryMigrationDigest(899501), writeRevision: "target-revision-write",
		},
		{name: "adoption verify call failed without product", category: "observation_invalid"},
		{
			name: "adoption mismatch without write product", category: "verification_mismatch",
			observationDigest: recoveryMigrationDigest(899502), observedRevision: "target-revision-adopted",
			observedState: "present",
		},
	}
	for index, product := range products {
		t.Run(product.name, func(t *testing.T) {
			fixture.expectExecAcceptedInRollback(
				t, db, insert,
				recoveryMigrationOpaqueID(899510+index), recoveryMigrationDigest(899520+index),
				product.category, product.writeDigest, product.writeRevision,
				product.observationDigest, product.observedRevision, product.observedState,
				now.Add(2*time.Second), aggregate.CheckpointID, aggregate.JobID, aggregate.JobItemID,
			)
		})
	}
	fixture.expectExecRejectedInRollback(
		t, db, insert,
		recoveryMigrationOpaqueID(899530), recoveryMigrationDigest(899531),
		"write_result_invalid", "", "", recoveryMigrationDigest(899532), "", "",
		now.Add(2*time.Second), aggregate.CheckpointID, aggregate.JobID, aggregate.JobItemID,
	)
}

func (fixture migrationFixture) test069WholeTask6OperationEvidence(t *testing.T) {
	t.Helper()
	t.Run("mutating operation and source failure", func(t *testing.T) {
		_, db := fixture.openAt(t, backupAssetRecoveryVersion)
		now := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
		aggregate := fixture.seedRecoveryMigrationAggregate(t, db, "6", 226, now, true)
		fixture.mustExec(t, db, `UPDATE backup_asset_recovery_attempts
			SET mutation_armed = ?, updated_at = ? WHERE id = ?`, true, now.Add(time.Second), aggregate.AttemptID)
		operationDigest := recoveryMigrationDigest(899601)
		nextRevision := recoveryMigrationDigest(899602)
		insertOperation := wholeTask6OperationCheckpointInsert()

		fixture.expectExecRejectedInRollback(
			t, db, insertOperation,
			recoveryMigrationOpaqueID(899603), recoveryMigrationOpaqueID(899604),
			operationDigest, nextRevision, now.Add(2*time.Second), aggregate.CheckpointID, aggregate.JobID,
		)
		fixture.expectExecRejectedInRollback(
			t, db, insertOperation,
			recoveryMigrationOpaqueID(899605), aggregate.JobItemID,
			operationDigest, recoveryMigrationDigest(690000+226*100+21), now.Add(2*time.Second),
			aggregate.CheckpointID, aggregate.JobID,
		)
		fixture.mustExec(
			t, db, insertOperation,
			recoveryMigrationOpaqueID(899606), aggregate.JobItemID,
			operationDigest, nextRevision, now.Add(2*time.Second), aggregate.CheckpointID, aggregate.JobID,
		)
		fixture.mustExec(t, db, `UPDATE backup_asset_recovery_job_items
			SET outcome = 'succeeded', bytes_written = expected_post_bytes,
			    verified_size = expected_post_bytes, verified_digest = expected_post_identity_digest,
			    updated_at = ? WHERE id = ?`, now.Add(3*time.Second), aggregate.JobItemID)
		fixture.mustExec(t, db, `UPDATE backup_asset_recovery_jobs
			SET target_chain_revision = ?, workspace_phase = 'writing', updated_at = ? WHERE id = ?`,
			nextRevision, now.Add(3*time.Second), aggregate.JobID)
		sourceLeaseID := recoveryMigrationOpaqueID(899607)
		fixture.mustExec(t, db, `INSERT INTO recovery_point_leases
			(id, recovery_point_id, holder_type, owner_id, attempt_id, fence_token, status,
			 lease_expires_at, absolute_deadline, last_heartbeat_at, created_at, updated_at)
			VALUES (?, ?, 'recovery_job', ?, ?, ?, 'active', ?, ?, ?, ?, ?)`,
			sourceLeaseID, aggregate.PointID, aggregate.JobID, aggregate.AttemptID,
			recoveryMigrationDigest(899608), now.Add(time.Hour), now.Add(2*time.Hour), now, now, now,
		)
		failureInsert := wholeTask6SourceFailureEvidenceInsert()
		failureArgs := func(id, checkpointID, leaseID string, nodeFence int64) []any {
			return []any{
				id, aggregate.JobID, recoveryMigrationDigest(899609), now.Add(4 * time.Second),
				aggregate.PlanID, checkpointID, aggregate.WriteGrantID, aggregate.AttemptID,
				leaseID, aggregate.NodeLeaseID, nodeFence, now.Add(4 * time.Second), now.Add(4 * time.Second),
			}
		}
		fixture.expectExecRejectedInRollback(
			t, db, failureInsert,
			failureArgs(recoveryMigrationOpaqueID(899610), recoveryMigrationOpaqueID(899611), sourceLeaseID, 1)...,
		)
		fixture.expectExecRejectedInRollback(
			t, db, failureInsert,
			failureArgs(recoveryMigrationOpaqueID(899612), recoveryMigrationOpaqueID(899606), sourceLeaseID, 2)...,
		)
		fixture.expectExecRejectedInRollback(
			t, db, failureInsert,
			failureArgs(recoveryMigrationOpaqueID(899613), recoveryMigrationOpaqueID(899606),
				recoveryMigrationOpaqueID(899614), 1)...,
		)
		fixture.expectExecAcceptedInRollback(
			t, db, failureInsert,
			failureArgs(recoveryMigrationOpaqueID(899615), recoveryMigrationOpaqueID(899606), sourceLeaseID, 1)...,
		)

		tx, err := db.Begin()
		if err != nil {
			t.Fatalf("begin %s source-failure wrong-chain probe: %v", fixture.engine, err)
		}
		if _, err = tx.Exec(fixture.bind(`UPDATE backup_asset_recovery_jobs
			SET target_chain_revision = ? WHERE id = ?`), recoveryMigrationDigest(899616), aggregate.JobID); err == nil {
			_, err = tx.Exec(fixture.bind(failureInsert), failureArgs(
				recoveryMigrationOpaqueID(899617), recoveryMigrationOpaqueID(899606), sourceLeaseID, 1,
			)...)
		}
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			t.Fatalf("rollback %s source-failure wrong-chain probe: %v", fixture.engine, rollbackErr)
		}
		if err == nil {
			t.Fatalf("%s source-failure evidence accepted a wrong target chain", fixture.engine)
		}
	})

	t.Run("skip operation retains the chain", func(t *testing.T) {
		_, db := fixture.openAt(t, backupAssetRecoveryVersion)
		now := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
		aggregate := fixture.seedRecoveryMigrationAggregate(t, db, "7", 227, now, true)
		fixture.mustExec(t, db, `UPDATE backup_asset_recovery_attempts
			SET mutation_armed = ?, updated_at = ? WHERE id = ?`, true, now.Add(time.Second), aggregate.AttemptID)
		fixture.mustExec(t, db, `DELETE FROM backup_asset_recovery_job_items WHERE id = ?`, aggregate.JobItemID)
		priorDigest := recoveryMigrationDigest(899701)
		fixture.mustExec(t, db, `INSERT INTO backup_asset_recovery_job_items
			(id, plan_id, job_id, plan_item_id, ordinal, operation_kind, target_path_digest,
			 semantic_target_digest, target_object_digest, expected_prior_kind, expected_prior_digest,
			 expected_post_identity_digest, expected_post_bytes, expected_prior_bytes,
			 encrypted_target_relative_locator, target_locator_key_version, target_locator_cipher_version,
			 display_class, estimated_bytes, created_at, updated_at)
			VALUES (?, ?, ?, ?, 0, 'skip', ?, ?, ?, 'present', ?, ?, -1, 1,
			        'enc:v2:whole-task6-skip', 1, 1, 'regular', 1, ?, ?)`,
			aggregate.JobItemID, aggregate.PlanID, aggregate.JobID, aggregate.PlanItemID,
			recoveryMigrationDigest(899702), recoveryMigrationDigest(899703), recoveryMigrationDigest(899704),
			priorDigest, priorDigest, now, now,
		)
		var chain string
		if err := db.QueryRow(fixture.bind(`SELECT target_chain_revision
			FROM backup_asset_recovery_jobs WHERE id = ?`), aggregate.JobID).Scan(&chain); err != nil {
			t.Fatal(err)
		}
		insertOperation := wholeTask6OperationCheckpointInsert()
		fixture.expectExecRejectedInRollback(
			t, db, insertOperation,
			recoveryMigrationOpaqueID(899705), aggregate.JobItemID, recoveryMigrationDigest(899706),
			recoveryMigrationDigest(899707), now.Add(2*time.Second), aggregate.CheckpointID, aggregate.JobID,
		)
		fixture.expectExecAcceptedInRollback(
			t, db, insertOperation,
			recoveryMigrationOpaqueID(899708), aggregate.JobItemID, recoveryMigrationDigest(899709),
			chain, now.Add(2*time.Second), aggregate.CheckpointID, aggregate.JobID,
		)
	})
}

func wholeTask6OperationCheckpointInsert() string {
	return `INSERT INTO backup_asset_recovery_checkpoints
		(id, job_id, job_item_id, attempt_id, sequence, phase, authority_category,
		 operation_digest, prior_target_revision, next_target_revision, node_fence, attempt_fence,
		 plan_binding_digest, source_revision_digest, preflight_id, preflight_revision,
		 preflight_expires_at, security_decision, security_decision_digest,
		 security_finding_set_digest, security_policy_revision, authority_grant_id,
		 job_authority_category, authority_binding_digest, authority_expires_at, created_at)
	SELECT ?, job.id, ?, checkpoint.attempt_id, 1, 'operation', 'write', ?,
	       job.target_chain_revision, ?, 1, 1,
	       checkpoint.plan_binding_digest, checkpoint.source_revision_digest,
	       checkpoint.preflight_id, checkpoint.preflight_revision, checkpoint.preflight_expires_at,
	       checkpoint.security_decision, checkpoint.security_decision_digest,
	       checkpoint.security_finding_set_digest, checkpoint.security_policy_revision,
	       checkpoint.authority_grant_id, checkpoint.job_authority_category,
	       checkpoint.authority_binding_digest, checkpoint.authority_expires_at, ?
	FROM backup_asset_recovery_jobs AS job
	JOIN backup_asset_recovery_checkpoints AS checkpoint
	  ON checkpoint.id = ? AND checkpoint.job_id = job.id
	WHERE job.id = ?`
}

func wholeTask6SourceFailureEvidenceInsert() string {
	return `INSERT INTO backup_asset_recovery_evidence
		(id, job_id, kind, outcome, summary_digest, difference_count, verified_at,
		 plan_id, checkpoint_id, grant_id, attempt_id, source_lease_id, node_lease_id,
		 node_lease_fence, created_at, updated_at)
	VALUES (?, ?, 'failure', 'needs_attention', ?, 0, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
}

func (fixture migrationFixture) test069WholeTask6WorkspacePhases(t *testing.T) {
	t.Helper()
	_, contractDB := fixture.openAt(t, backupAssetRecoveryVersion)
	for _, column := range []string{
		"workspace_marker_validation_attempt_id",
		"workspace_marker_validation_attempt_fence",
		"workspace_marker_validation_node_fence",
	} {
		if !databaseColumnExists(t, contractDB, fixture.engine, "backup_asset_recovery_jobs", column) {
			t.Fatalf("%s 000069 recovery jobs omit marker-validation column %s", fixture.engine, column)
		}
	}
	if err := contractDB.Close(); err != nil {
		t.Fatalf("close %s marker-validation contract database: %v", fixture.engine, err)
	}

	seedAt := func(t *testing.T, phase string, sequence int) (*sql.DB, recoveryMigrationAggregate, time.Time) {
		t.Helper()
		_, db := fixture.openAt(t, backupAssetRecoveryVersion)
		now := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
		aggregate := fixture.seedRecoveryMigrationAggregateWithOptions(
			t, db, fmt.Sprintf("%x", sequence%16), sequence, now,
			recoveryMigrationSeedOptions{activeAttempt: true, workspacePhase: phase},
		)
		return db, aggregate, now
	}

	for index, transition := range []struct {
		from string
		to   string
	}{
		{from: "reserved", to: "marker_created"},
		{from: "marker_created", to: "writing"},
		{from: "writing", to: "sealed"},
		{from: "reserved", to: "cleanup_due"},
		{from: "marker_created", to: "cleanup_due"},
		{from: "writing", to: "cleanup_due"},
		{from: "sealed", to: "cleanup_due"},
	} {
		transition := transition
		t.Run("allows "+transition.from+" to "+transition.to, func(t *testing.T) {
			db, aggregate, now := seedAt(t, transition.from, 230+index)
			if transition.from == "reserved" && transition.to == "marker_created" {
				fixture.mustExec(t, db, `UPDATE backup_asset_recovery_attempts
					SET mutation_armed = ?, updated_at = ? WHERE id = ?`,
					true, now.Add(time.Second), aggregate.AttemptID)
				fixture.expectExecAcceptedInRollback(t, db, `UPDATE backup_asset_recovery_jobs
					SET workspace_phase = ?, workspace_marker_validation_attempt_id = ?,
					    workspace_marker_validation_attempt_fence = 1,
					    workspace_marker_validation_node_fence = 1,
					    updated_at = ? WHERE id = ?`,
					transition.to, aggregate.AttemptID, now.Add(time.Second), aggregate.JobID)
				return
			}
			fixture.expectExecAcceptedInRollback(t, db, `UPDATE backup_asset_recovery_jobs
				SET workspace_phase = ?, updated_at = ? WHERE id = ?`,
				transition.to, now.Add(time.Second), aggregate.JobID)
		})
	}

	t.Run("allows none to reserved with the frozen identity", func(t *testing.T) {
		_, db := fixture.openAt(t, backupAssetRecoveryVersion)
		now := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
		aggregate := fixture.seedRecoveryMigrationAggregateWithOptions(
			t, db, "e", 238, now, recoveryMigrationSeedOptions{claimableAttempt: true},
		)
		fixture.expectExecAcceptedInRollback(t, db, `UPDATE backup_asset_recovery_jobs
			SET workspace_phase = 'reserved', workspace_marker_binding_digest = ?,
			    workspace_owner = 'whole-task6-worker', workspace_fence = 1,
			    plaintext_deadline = ?, updated_at = ? WHERE id = ?`,
			recoveryMigrationDigest(899801), now.Add(2*time.Hour), now.Add(time.Second), aggregate.JobID)
	})

	t.Run("freezes marker provenance after reservation", func(t *testing.T) {
		db, aggregate, now := seedAt(t, "reserved", 239)
		fixture.expectExecRejectedInRollback(t, db, `UPDATE backup_asset_recovery_jobs
			SET workspace_owner = 'whole-task6-rewritten-worker', updated_at = ? WHERE id = ?`,
			now.Add(time.Second), aggregate.JobID)
		fixture.expectExecRejectedInRollback(t, db, `UPDATE backup_asset_recovery_jobs
			SET workspace_fence = workspace_fence + 1, updated_at = ? WHERE id = ?`,
			now.Add(time.Second), aggregate.JobID)
	})

	t.Run("requires an atomic complete marker validation product", func(t *testing.T) {
		db, aggregate, now := seedAt(t, "reserved", 257)
		fixture.expectExecRejectedInRollback(t, db, `UPDATE backup_asset_recovery_jobs
			SET workspace_marker_validation_attempt_id = ?, updated_at = ? WHERE id = ?`,
			aggregate.AttemptID, now.Add(time.Second), aggregate.JobID)
		fixture.expectExecRejectedInRollback(t, db, `UPDATE backup_asset_recovery_jobs
			SET workspace_phase = 'marker_created', updated_at = ? WHERE id = ?`,
			now.Add(time.Second), aggregate.JobID)
	})

	t.Run("freezes marker validation provenance after marker creation", func(t *testing.T) {
		db, aggregate, now := seedAt(t, "reserved", 258)
		fixture.mustExec(t, db, `UPDATE backup_asset_recovery_attempts
			SET mutation_armed = ?, updated_at = ? WHERE id = ?`, true, now.Add(time.Second), aggregate.AttemptID)
		fixture.mustExec(t, db, `UPDATE backup_asset_recovery_jobs
			SET workspace_phase = 'marker_created', workspace_marker_validation_attempt_id = ?,
			    workspace_marker_validation_attempt_fence = 1,
			    workspace_marker_validation_node_fence = 1,
			    updated_at = ? WHERE id = ?`, aggregate.AttemptID, now.Add(2*time.Second), aggregate.JobID)
		fixture.expectExecRejectedInRollback(t, db, `UPDATE backup_asset_recovery_jobs
			SET workspace_marker_validation_attempt_id = ?, updated_at = ? WHERE id = ?`,
			recoveryMigrationOpaqueID(899901), now.Add(3*time.Second), aggregate.JobID)
		fixture.expectExecRejectedInRollback(t, db, `UPDATE backup_asset_recovery_jobs
			SET workspace_marker_validation_attempt_fence = 2,
			    workspace_marker_validation_node_fence = 2,
			    updated_at = ? WHERE id = ?`, now.Add(3*time.Second), aggregate.JobID)
	})

	t.Run("allows sealed to published after terminal success", func(t *testing.T) {
		db, aggregate, now := seedAt(t, "sealed", 240)
		fixture.mustExec(t, db, `UPDATE backup_asset_recovery_attempts
			SET state = 'completed', mutation_armed = ?, closed_at = ?, updated_at = ? WHERE id = ?`,
			true, now.Add(time.Second), now.Add(time.Second), aggregate.AttemptID)
		fixture.mustExec(t, db, `UPDATE backup_asset_recovery_node_leases
			SET state = 'released', released_at = ?, updated_at = ? WHERE id = ?`,
			now.Add(time.Second), now.Add(time.Second), aggregate.NodeLeaseID)
		fixture.mustExec(t, db, `UPDATE backup_asset_recovery_jobs
			SET state = 'verifying', transition_revision = transition_revision + 1,
			    updated_at = ? WHERE id = ?`, now.Add(2*time.Second), aggregate.JobID)
		fixture.mustExec(t, db, `UPDATE backup_asset_recovery_jobs
			SET state = 'succeeded', transition_revision = transition_revision + 1,
			    updated_at = ? WHERE id = ?`, now.Add(3*time.Second), aggregate.JobID)
		fixture.expectExecAcceptedInRollback(t, db, `UPDATE backup_asset_recovery_jobs
			SET workspace_phase = 'published', updated_at = ? WHERE id = ?`,
			now.Add(4*time.Second), aggregate.JobID)
	})

	for index, phase := range []string{
		"reserved", "marker_created", "writing", "sealed", "cleanup_due",
	} {
		phase := phase
		t.Run("allows same "+phase, func(t *testing.T) {
			db, aggregate, now := seedAt(t, phase, 241+index)
			fixture.expectExecAcceptedInRollback(t, db, `UPDATE backup_asset_recovery_jobs
				SET workspace_phase = ?, updated_at = ? WHERE id = ?`,
				phase, now.Add(time.Second), aggregate.JobID)
		})
	}

	for index, transition := range []struct {
		from string
		to   string
	}{
		{from: "reserved", to: "writing"},
		{from: "reserved", to: "sealed"},
		{from: "marker_created", to: "reserved"},
		{from: "marker_created", to: "sealed"},
		{from: "writing", to: "reserved"},
		{from: "writing", to: "marker_created"},
		{from: "sealed", to: "marker_created"},
		{from: "cleanup_due", to: "reserved"},
	} {
		transition := transition
		t.Run("rejects "+transition.from+" to "+transition.to, func(t *testing.T) {
			db, aggregate, now := seedAt(t, transition.from, 247+index)
			fixture.expectExecRejectedInRollback(t, db, `UPDATE backup_asset_recovery_jobs
				SET workspace_phase = ?, updated_at = ? WHERE id = ?`,
				transition.to, now.Add(time.Second), aggregate.JobID)
		})
	}
}

func (fixture migrationFixture) test069ConsumedDeleteUnresolvedHistoryMatrix(
	t *testing.T,
	db *sql.DB,
	sequence int,
) {
	t.Helper()
	now := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	aggregate := fixture.seedRecoveryMigrationExactMirrorOperationHistory(
		t, db, fmt.Sprintf("%x", sequence%16), sequence, now,
	)
	base := 690000 + sequence*100
	type neutralDeleteCandidate struct {
		itemID            string
		operationDigest   string
		observationDigest string
		checkpointID      string
		jobID             string
	}
	seedCandidate := func(
		aggregate recoveryMigrationAggregate,
		candidateBase int,
		ordinal int,
		createdAt time.Time,
		locator string,
	) neutralDeleteCandidate {
		t.Helper()
		candidate := neutralDeleteCandidate{
			itemID:            recoveryMigrationOpaqueID(candidateBase + 40),
			operationDigest:   recoveryMigrationDigest(candidateBase + 41),
			observationDigest: recoveryMigrationDigest(candidateBase + 50),
			checkpointID:      aggregate.CheckpointID,
			jobID:             aggregate.JobID,
		}
		fixture.mustExec(t, db, `INSERT INTO backup_asset_recovery_job_items
			(id, plan_id, job_id, plan_item_id, ordinal, operation_kind, target_path_digest,
			 semantic_target_digest, target_object_digest, expected_prior_kind, expected_prior_digest,
			 expected_post_identity_digest, expected_post_bytes, expected_prior_bytes,
			 encrypted_target_relative_locator, target_locator_key_version, target_locator_cipher_version,
			 display_class, estimated_bytes, created_at, updated_at)
			VALUES (?, ?, ?, NULL, ?, 'delete', ?, ?, ?, 'present', ?, '', -1, -1,
				?, 1, 1, 'regular', 0, ?, ?)`,
			candidate.itemID, aggregate.PlanID, aggregate.JobID, ordinal,
			recoveryMigrationDigest(candidateBase+42), recoveryMigrationDigest(candidateBase+43),
			recoveryMigrationDigest(candidateBase+44), recoveryMigrationDigest(candidateBase+45),
			locator, createdAt, createdAt,
		)
		return candidate
	}
	mainCandidate := seedCandidate(
		aggregate, base, 1, now, "enc:v2:consumed-delete-unresolved",
	)

	insertOperation := func(
		aggregate recoveryMigrationAggregate,
		operationBase int,
		id string,
		jobItemID string,
		checkpointSequence int,
		nextRevision string,
		createdAt time.Time,
	) {
		t.Helper()
		fixture.mustExec(t, db, `INSERT INTO backup_asset_recovery_checkpoints
			(id, job_id, job_item_id, attempt_id, sequence, phase, authority_category, operation_digest,
			 prior_target_revision, next_target_revision, node_fence, attempt_fence,
			 plan_binding_digest, source_revision_digest, preflight_id, preflight_revision,
			 preflight_expires_at, security_decision, security_decision_digest,
			 security_finding_set_digest, security_policy_revision, authority_grant_id,
			 job_authority_category, authority_binding_digest, authority_expires_at, created_at)
			SELECT ?, checkpoint.job_id, ?, checkpoint.attempt_id, ?, 'operation', 'write', ?,
		       job.target_chain_revision, ?, 1, 1,
		       checkpoint.plan_binding_digest, checkpoint.source_revision_digest,
		       checkpoint.preflight_id, checkpoint.preflight_revision, checkpoint.preflight_expires_at,
		       checkpoint.security_decision, checkpoint.security_decision_digest,
		       checkpoint.security_finding_set_digest, checkpoint.security_policy_revision,
		       checkpoint.authority_grant_id, checkpoint.job_authority_category,
		       checkpoint.authority_binding_digest, checkpoint.authority_expires_at, ?
		FROM backup_asset_recovery_checkpoints AS checkpoint
		JOIN backup_asset_recovery_jobs AS job ON job.id = checkpoint.job_id
		WHERE checkpoint.id = ?`,
			id, jobItemID, checkpointSequence, recoveryMigrationDigest(operationBase+46+checkpointSequence),
			nextRevision, createdAt, aggregate.CheckpointID,
		)
		fixture.mustExec(t, db, `UPDATE backup_asset_recovery_job_items
			SET outcome = 'succeeded', updated_at = ? WHERE id = ?`, createdAt, jobItemID)
		fixture.mustExec(t, db, `UPDATE backup_asset_recovery_jobs
			SET target_chain_revision = ?, updated_at = ? WHERE id = ?`,
			nextRevision, createdAt, aggregate.JobID,
		)
	}

	unresolvedInsert := `INSERT INTO backup_asset_recovery_checkpoints
		(id, job_id, job_item_id, attempt_id, sequence, phase, authority_category,
		 operation_digest, prior_target_revision, next_target_revision,
		 unresolved_category, write_result_digest, write_target_revision,
		 observation_digest, observed_target_revision, observed_presence,
		 source_revalidation_outcome, node_fence, attempt_fence,
		 plan_binding_digest, source_revision_digest, preflight_id, preflight_revision,
		 preflight_expires_at, security_decision, security_decision_digest,
		 security_finding_set_digest, security_policy_revision, authority_grant_id,
		 job_authority_category, authority_binding_digest, authority_expires_at, created_at)
		SELECT ?, job.id, ?, checkpoint.attempt_id, ?, 'operation_unresolved', 'write', ?,
	       job.target_chain_revision, '', ?, ?, ?, ?, ?, ?, 'matched', ?, ?,
	       checkpoint.plan_binding_digest, checkpoint.source_revision_digest,
	       checkpoint.preflight_id, checkpoint.preflight_revision, checkpoint.preflight_expires_at,
	       checkpoint.security_decision, checkpoint.security_decision_digest,
	       checkpoint.security_finding_set_digest, checkpoint.security_policy_revision,
	       checkpoint.authority_grant_id, checkpoint.job_authority_category,
	       checkpoint.authority_binding_digest, checkpoint.authority_expires_at, ?
	FROM backup_asset_recovery_jobs AS job
	JOIN backup_asset_recovery_checkpoints AS checkpoint
	  ON checkpoint.id = ? AND checkpoint.job_id = job.id
	WHERE job.id = ?`
	unresolvedArgs := func(
		candidate neutralDeleteCandidate,
		id string,
		checkpointSequence int,
		writeDigest string,
		writeRevision string,
		nodeFence int64,
		attemptFence int64,
		createdAt time.Time,
	) []any {
		t.Helper()
		return []any{
			id, candidate.itemID, checkpointSequence, candidate.operationDigest,
			"observation_invalid", writeDigest, writeRevision, candidate.observationDigest, "", "",
			nodeFence, attemptFence,
			createdAt, candidate.checkpointID, candidate.jobID,
		}
	}
	verificationMismatchArgs := func(
		candidate neutralDeleteCandidate,
		id string,
		checkpointSequence int,
		nodeFence int64,
		attemptFence int64,
		createdAt time.Time,
	) []any {
		t.Helper()
		return []any{
			id, candidate.itemID, checkpointSequence, candidate.operationDigest,
			"verification_mismatch", "", "", candidate.observationDigest,
			"target-revision-delete-still-present", "present", nodeFence, attemptFence,
			createdAt, candidate.checkpointID, candidate.jobID,
		}
	}

	fixture.expectExecRejectedInRollback(
		t, db, unresolvedInsert,
		unresolvedArgs(
			mainCandidate, recoveryMigrationOpaqueID(base+52), 1, "", "", 1, 1, now.Add(2*time.Second),
		)...,
	)
	fixture.expectExecRejectedInRollback(
		t, db, unresolvedInsert,
		verificationMismatchArgs(
			mainCandidate, recoveryMigrationOpaqueID(base+63), 1, 1, 1, now.Add(2*time.Second),
		)...,
	)
	fixture.expectExecAcceptedInRollback(
		t, db, unresolvedInsert,
		unresolvedArgs(
			mainCandidate, recoveryMigrationOpaqueID(base+53), 1, recoveryMigrationDigest(base+53),
			"target-revision-delete", 1, 1, now.Add(2*time.Second),
		)...,
	)

	requiredCheckpointID := recoveryMigrationOpaqueID(base + 54)
	requiredAt := now.Add(3 * time.Second)
	deleteAuthorityExpiresAt := now.Add(20 * time.Minute)
	fixture.mustExec(t, db, `INSERT INTO backup_asset_recovery_checkpoints
		(id, job_id, attempt_id, sequence, phase, authority_category, operation_digest,
		 prior_target_revision, next_target_revision, node_fence, attempt_fence,
		 plan_binding_digest, source_revision_digest, preflight_id, preflight_revision,
		 preflight_expires_at, security_decision, security_decision_digest,
		 security_finding_set_digest, security_policy_revision, authority_grant_id,
		 job_authority_category, authority_binding_digest, authority_expires_at,
		 delete_node_revision, delete_root_revision, delete_authority_expires_at, created_at)
		SELECT ?, checkpoint.job_id, checkpoint.attempt_id, 1, 'delete_authority_required',
	       'exact_mirror_delete', job.delete_set_digest, job.target_chain_revision, '', 1, 1,
	       checkpoint.plan_binding_digest, checkpoint.source_revision_digest,
	       checkpoint.preflight_id, checkpoint.preflight_revision, checkpoint.preflight_expires_at,
	       checkpoint.security_decision, checkpoint.security_decision_digest,
	       checkpoint.security_finding_set_digest, checkpoint.security_policy_revision,
	       checkpoint.authority_grant_id, checkpoint.job_authority_category,
	       checkpoint.authority_binding_digest, checkpoint.authority_expires_at,
	       job.preflight_node_revision, plan.root_revision, ?, ?
	FROM backup_asset_recovery_checkpoints AS checkpoint
	JOIN backup_asset_recovery_jobs AS job ON job.id = checkpoint.job_id
	JOIN backup_asset_recovery_plans AS plan ON plan.id = job.plan_id
	WHERE checkpoint.id = ?`,
		requiredCheckpointID, deleteAuthorityExpiresAt, requiredAt, aggregate.CheckpointID,
	)

	deleteGrantID := recoveryMigrationOpaqueID(base + 55)
	deleteGrantBindingDigest := recoveryMigrationDigest(base + 55)
	deleteGrantExpiresAt := now.Add(10 * time.Minute)
	deleteGrantCreatedAt := now.Add(4 * time.Second)
	consumeAt := now.Add(5 * time.Second)
	fixture.mustExec(t, db, `INSERT INTO backup_asset_recovery_grants
		(id, plan_id, job_id, authority_category, grant_hash, actor_user_id,
		 actor_session_id, binding_digest, encrypted_reason, delete_checkpoint_id,
		 delete_set_digest, delete_target_revision, delete_attempt_id,
		 delete_attempt_fence, delete_node_fence, expires_at, created_at, updated_at)
	SELECT ?, job.plan_id, job.id, 'exact_mirror_delete', ?, ?, 'consumed-delete-session',
	       ?, 'enc:v2:consumed-delete-reason', required.id, required.operation_digest,
	       required.prior_target_revision, required.attempt_id, required.attempt_fence,
	       required.node_fence, ?, ?, ?
	FROM backup_asset_recovery_jobs AS job
	JOIN backup_asset_recovery_checkpoints AS required
	  ON required.id = ? AND required.job_id = job.id
	WHERE job.id = ?`,
		deleteGrantID, recoveryMigrationDigest(base+56), aggregate.UserID,
		deleteGrantBindingDigest, deleteGrantExpiresAt, deleteGrantCreatedAt,
		deleteGrantCreatedAt, requiredCheckpointID, aggregate.JobID,
	)
	fixture.mustExec(t, db, `UPDATE backup_asset_recovery_grants
		SET consumed_at = ?, updated_at = ? WHERE id = ?`, consumeAt, consumeAt, deleteGrantID)

	consumedCheckpointID := recoveryMigrationOpaqueID(base + 57)
	fixture.mustExec(t, db, `INSERT INTO backup_asset_recovery_checkpoints
		(id, job_id, attempt_id, sequence, phase, authority_category, operation_digest,
		 prior_target_revision, next_target_revision, node_fence, attempt_fence,
		 plan_binding_digest, source_revision_digest, preflight_id, preflight_revision,
		 preflight_expires_at, security_decision, security_decision_digest,
		 security_finding_set_digest, security_policy_revision, authority_grant_id,
		 job_authority_category, authority_binding_digest, authority_expires_at,
		 delete_node_revision, delete_root_revision, delete_authority_expires_at,
		 delete_grant_id, delete_grant_binding_digest, delete_grant_expires_at,
		 delete_grant_consumed_at, created_at)
		SELECT ?, required.job_id, required.attempt_id, 2, 'delete_authority_consumed',
	       'exact_mirror_delete', required.operation_digest, required.prior_target_revision,
	       required.prior_target_revision, required.node_fence, required.attempt_fence,
	       required.plan_binding_digest, required.source_revision_digest,
	       required.preflight_id, required.preflight_revision, required.preflight_expires_at,
	       required.security_decision, required.security_decision_digest,
	       required.security_finding_set_digest, required.security_policy_revision,
	       required.authority_grant_id, required.job_authority_category,
	       required.authority_binding_digest, required.authority_expires_at,
	       required.delete_node_revision, required.delete_root_revision,
	       required.delete_authority_expires_at, grant_row.id, grant_row.binding_digest,
	       grant_row.expires_at, grant_row.consumed_at, ?
	FROM backup_asset_recovery_checkpoints AS required
	JOIN backup_asset_recovery_grants AS grant_row ON grant_row.id = ?
	WHERE required.id = ?`,
		consumedCheckpointID, consumeAt, deleteGrantID, requiredCheckpointID,
	)

	interveningCandidate := seedCandidate(
		aggregate, base+40, 2, now.Add(6*time.Second), "enc:v2:consumed-delete-intervening",
	)
	secondOperationRevision := recoveryMigrationDigest(base + 58)
	insertOperation(
		aggregate, base, recoveryMigrationOpaqueID(base+58), interveningCandidate.itemID, 3,
		secondOperationRevision, now.Add(6*time.Second),
	)
	fixture.expectExecAcceptedInRollback(
		t, db, unresolvedInsert,
		unresolvedArgs(
			mainCandidate, recoveryMigrationOpaqueID(base+59), 4, "", "", 1, 1, now.Add(7*time.Second),
		)...,
	)
	fixture.expectExecAcceptedInRollback(
		t, db, unresolvedInsert,
		verificationMismatchArgs(
			mainCandidate, recoveryMigrationOpaqueID(base+64), 4, 1, 1, now.Add(7*time.Second),
		)...,
	)

	crossJobSequence := sequence + 1
	crossJobBase := 690000 + crossJobSequence*100
	crossJobAggregate := fixture.seedRecoveryMigrationExactMirrorOperationHistory(
		t, db, fmt.Sprintf("%x", crossJobSequence%16), crossJobSequence, now.Add(8*time.Second),
	)
	crossJobCandidate := seedCandidate(
		crossJobAggregate, crossJobBase, 1, now.Add(8*time.Second),
		"enc:v2:cross-job-consumed-delete-unresolved",
	)

	type consumedHistorySnapshot struct {
		checkpointRows int64
		unresolvedRows int64
		attemptRows    int64
		grantRows      int64

		mainJobDeleteSet       string
		mainJobState           string
		mainJobFailure         string
		mainJobTransition      int64
		mainJobTargetRevision  string
		mainItemOutcome        string
		mainItemFailure        string
		crossJobDeleteSet      string
		crossJobState          string
		crossJobFailure        string
		crossJobTransition     int64
		crossJobTargetRevision string
		crossItemOutcome       string
		crossItemFailure       string

		attemptFence int64
		attemptState string
		nodeFence    int64
		nodeState    string

		requiredJob          string
		requiredAttempt      string
		requiredDeleteSet    string
		requiredAttemptFence int64
		requiredNodeFence    int64
		consumedJob          string
		consumedAttempt      string
		consumedDeleteSet    string
		consumedAttemptFence int64
		consumedNodeFence    int64
		consumedGrantID      string
		consumedGrantBinding string

		grantJob          string
		grantCheckpoint   string
		grantDeleteSet    string
		grantAttempt      string
		grantAttemptFence int64
		grantNodeFence    int64
		grantBinding      string
		grantConsumedAt   sql.NullTime
		grantRevokedAt    sql.NullTime

		triggerDefinitions []string
	}
	captureHistory := func(t *testing.T) consumedHistorySnapshot {
		t.Helper()
		var snapshot consumedHistorySnapshot
		if err := db.QueryRow(`SELECT COUNT(*),
			COALESCE(SUM(CASE WHEN phase = 'operation_unresolved' THEN 1 ELSE 0 END), 0)
			FROM backup_asset_recovery_checkpoints`).Scan(
			&snapshot.checkpointRows, &snapshot.unresolvedRows,
		); err != nil {
			t.Fatalf("capture %s consumed-history checkpoint counts: %v", fixture.engine, err)
		}
		if err := db.QueryRow(`SELECT COUNT(*) FROM backup_asset_recovery_attempts`).Scan(&snapshot.attemptRows); err != nil {
			t.Fatalf("capture %s consumed-history attempt count: %v", fixture.engine, err)
		}
		if err := db.QueryRow(`SELECT COUNT(*) FROM backup_asset_recovery_grants`).Scan(&snapshot.grantRows); err != nil {
			t.Fatalf("capture %s consumed-history grant count: %v", fixture.engine, err)
		}
		if err := db.QueryRow(fixture.bind(`SELECT delete_set_digest, state, failure_category,
			transition_revision, target_chain_revision
			FROM backup_asset_recovery_jobs WHERE id = ?`), aggregate.JobID).Scan(
			&snapshot.mainJobDeleteSet, &snapshot.mainJobState, &snapshot.mainJobFailure,
			&snapshot.mainJobTransition, &snapshot.mainJobTargetRevision,
		); err != nil {
			t.Fatalf("capture %s consumed-history main job: %v", fixture.engine, err)
		}
		if err := db.QueryRow(fixture.bind(`SELECT outcome, failure_category
			FROM backup_asset_recovery_job_items WHERE id = ?`), mainCandidate.itemID).Scan(
			&snapshot.mainItemOutcome, &snapshot.mainItemFailure,
		); err != nil {
			t.Fatalf("capture %s consumed-history main item: %v", fixture.engine, err)
		}
		if err := db.QueryRow(fixture.bind(`SELECT delete_set_digest, state, failure_category,
			transition_revision, target_chain_revision
			FROM backup_asset_recovery_jobs WHERE id = ?`), crossJobAggregate.JobID).Scan(
			&snapshot.crossJobDeleteSet, &snapshot.crossJobState, &snapshot.crossJobFailure,
			&snapshot.crossJobTransition, &snapshot.crossJobTargetRevision,
		); err != nil {
			t.Fatalf("capture %s consumed-history cross-job target: %v", fixture.engine, err)
		}
		if err := db.QueryRow(fixture.bind(`SELECT outcome, failure_category
			FROM backup_asset_recovery_job_items WHERE id = ?`), crossJobCandidate.itemID).Scan(
			&snapshot.crossItemOutcome, &snapshot.crossItemFailure,
		); err != nil {
			t.Fatalf("capture %s consumed-history cross-job item: %v", fixture.engine, err)
		}
		if err := db.QueryRow(fixture.bind(`SELECT fence, state
			FROM backup_asset_recovery_attempts WHERE id = ?`), aggregate.AttemptID).Scan(
			&snapshot.attemptFence, &snapshot.attemptState,
		); err != nil {
			t.Fatalf("capture %s consumed-history attempt: %v", fixture.engine, err)
		}
		if err := db.QueryRow(fixture.bind(`SELECT fence, state
			FROM backup_asset_recovery_node_leases WHERE id = ?`), aggregate.NodeLeaseID).Scan(
			&snapshot.nodeFence, &snapshot.nodeState,
		); err != nil {
			t.Fatalf("capture %s consumed-history node lease: %v", fixture.engine, err)
		}
		if err := db.QueryRow(fixture.bind(`SELECT job_id, attempt_id, operation_digest,
			attempt_fence, node_fence FROM backup_asset_recovery_checkpoints WHERE id = ?`),
			requiredCheckpointID,
		).Scan(
			&snapshot.requiredJob, &snapshot.requiredAttempt, &snapshot.requiredDeleteSet,
			&snapshot.requiredAttemptFence, &snapshot.requiredNodeFence,
		); err != nil {
			t.Fatalf("capture %s required delete history: %v", fixture.engine, err)
		}
		if err := db.QueryRow(fixture.bind(`SELECT job_id, attempt_id, operation_digest,
			attempt_fence, node_fence, delete_grant_id, delete_grant_binding_digest
			FROM backup_asset_recovery_checkpoints WHERE id = ?`), consumedCheckpointID).Scan(
			&snapshot.consumedJob, &snapshot.consumedAttempt, &snapshot.consumedDeleteSet,
			&snapshot.consumedAttemptFence, &snapshot.consumedNodeFence,
			&snapshot.consumedGrantID, &snapshot.consumedGrantBinding,
		); err != nil {
			t.Fatalf("capture %s consumed delete history: %v", fixture.engine, err)
		}
		if err := db.QueryRow(fixture.bind(`SELECT job_id, delete_checkpoint_id, delete_set_digest,
			delete_attempt_id, delete_attempt_fence, delete_node_fence, binding_digest,
			consumed_at, revoked_at FROM backup_asset_recovery_grants WHERE id = ?`), deleteGrantID).Scan(
			&snapshot.grantJob, &snapshot.grantCheckpoint, &snapshot.grantDeleteSet,
			&snapshot.grantAttempt, &snapshot.grantAttemptFence, &snapshot.grantNodeFence,
			&snapshot.grantBinding, &snapshot.grantConsumedAt, &snapshot.grantRevokedAt,
		); err != nil {
			t.Fatalf("capture %s consumed delete grant: %v", fixture.engine, err)
		}
		for _, trigger := range []struct {
			table string
			name  string
		}{
			{table: "backup_asset_recovery_jobs", name: "trg_backup_asset_recovery_jobs_binding_immutable"},
			{table: "backup_asset_recovery_attempts", name: "trg_backup_asset_recovery_attempts_integrity"},
			{table: "backup_asset_recovery_checkpoints", name: "trg_backup_asset_recovery_checkpoints_immutable"},
			{table: "backup_asset_recovery_grants", name: "trg_backup_asset_recovery_grants_terminal"},
		} {
			snapshot.triggerDefinitions = append(snapshot.triggerDefinitions,
				fixture.recoveryTriggerDefinition(t, db, trigger.table, trigger.name))
		}
		return snapshot
	}

	dropTrigger := func(tx *sql.Tx, table, name string) error {
		query := "DROP TRIGGER " + name
		if fixture.engine == "postgres" {
			query += " ON " + table
		}
		_, err := tx.Exec(query)
		return err
	}
	execOne := func(tx *sql.Tx, query string, args ...any) error {
		result, err := tx.Exec(fixture.bind(query), args...)
		if err != nil {
			return err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if rows != 1 {
			return fmt.Errorf("rows affected=%d, want 1", rows)
		}
		return nil
	}

	type candidateProbe struct {
		candidate    neutralDeleteCandidate
		id           string
		sequence     int
		nodeFence    int64
		attemptFence int64
		createdAt    time.Time
	}
	runMismatch := func(
		name string,
		positive candidateProbe,
		negative candidateProbe,
		mutate func(*sql.Tx) error,
	) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			before := captureHistory(t)
			fixture.expectExecAcceptedInRollback(
				t, db, unresolvedInsert,
				unresolvedArgs(
					positive.candidate, positive.id, positive.sequence, "", "",
					positive.nodeFence, positive.attemptFence, positive.createdAt,
				)...,
			)

			tx, err := db.Begin()
			if err != nil {
				t.Fatalf("begin %s %s mismatch transaction: %v", fixture.engine, name, err)
			}
			finished := false
			defer func() {
				if !finished {
					_ = tx.Rollback()
				}
			}()
			if mutate != nil {
				if err := mutate(tx); err != nil {
					_ = tx.Rollback()
					finished = true
					t.Fatalf("construct %s %s mismatch: %v", fixture.engine, name, err)
				}
			}
			_, insertErr := tx.Exec(
				fixture.bind(unresolvedInsert),
				unresolvedArgs(
					negative.candidate, negative.id, negative.sequence, "", "",
					negative.nodeFence, negative.attemptFence, negative.createdAt,
				)...,
			)
			if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
				t.Fatalf("rollback %s %s mismatch transaction: %v", fixture.engine, name, rollbackErr)
			}
			finished = true
			if insertErr == nil {
				t.Fatalf("%s unresolved neutral write accepted %s mismatch", fixture.engine, name)
			}
			if after := captureHistory(t); !reflect.DeepEqual(after, before) {
				t.Fatalf("%s %s rejection persisted a row, transition, binding, or trigger change:\nbefore=%+v\nafter=%+v",
					fixture.engine, name, before, after)
			}
			var candidateRows int64
			if err := db.QueryRow(fixture.bind(`SELECT COUNT(*) FROM backup_asset_recovery_checkpoints
				WHERE id = ?`), negative.id).Scan(&candidateRows); err != nil {
				t.Fatalf("verify %s %s candidate rollback: %v", fixture.engine, name, err)
			}
			if candidateRows != 0 {
				t.Fatalf("%s %s rejection persisted candidate %s", fixture.engine, name, negative.id)
			}
		})
	}

	mainProbe := func(index int, nodeFence, attemptFence int64) candidateProbe {
		return candidateProbe{
			candidate: mainCandidate, id: recoveryMigrationOpaqueID(base + 70 + index), sequence: 4,
			nodeFence: nodeFence, attemptFence: attemptFence, createdAt: now.Add(7 * time.Second),
		}
	}
	runMismatch(
		"consumed delete history has no cross-job inheritance",
		mainProbe(0, 1, 1),
		candidateProbe{
			candidate: crossJobCandidate, id: recoveryMigrationOpaqueID(base + 70), sequence: 1,
			nodeFence: 1, attemptFence: 1, createdAt: now.Add(10 * time.Second),
		},
		nil,
	)
	runMismatch(
		"consumed delete history rejects delete set mismatch",
		mainProbe(1, 1, 1), mainProbe(1, 1, 1),
		func(tx *sql.Tx) error {
			if err := dropTrigger(
				tx, "backup_asset_recovery_jobs", "trg_backup_asset_recovery_jobs_binding_immutable",
			); err != nil {
				return err
			}
			return execOne(tx, `UPDATE backup_asset_recovery_jobs SET delete_set_digest = ? WHERE id = ?`,
				recoveryMigrationDigest(base+60), aggregate.JobID)
		},
	)
	competingAttemptID := recoveryMigrationOpaqueID(base + 61)
	runMismatch(
		"consumed delete history rejects attempt mismatch",
		mainProbe(2, 1, 1), mainProbe(2, 1, 1),
		func(tx *sql.Tx) error {
			terminalAt := now.Add(6 * time.Second)
			if err := execOne(tx, `INSERT INTO backup_asset_recovery_attempts
				(id, job_id, owner_id, fence, state, mutation_armed, lease_expires_at,
				 heartbeat_at, closed_at, created_at, updated_at)
				VALUES (?, ?, 'consumed-history-companion', 2, 'failed', ?, NULL, NULL, ?, ?, ?)`,
				competingAttemptID, aggregate.JobID, false, terminalAt, terminalAt, terminalAt); err != nil {
				return err
			}
			if err := dropTrigger(
				tx, "backup_asset_recovery_checkpoints", "trg_backup_asset_recovery_checkpoints_immutable",
			); err != nil {
				return err
			}
			if err := dropTrigger(
				tx, "backup_asset_recovery_grants", "trg_backup_asset_recovery_grants_terminal",
			); err != nil {
				return err
			}
			result, err := tx.Exec(fixture.bind(`UPDATE backup_asset_recovery_checkpoints
				SET attempt_id = ? WHERE id IN (?, ?)`),
				competingAttemptID, requiredCheckpointID, consumedCheckpointID)
			if err != nil {
				return err
			}
			rows, err := result.RowsAffected()
			if err != nil || rows != 2 {
				return fmt.Errorf("update required/consumed attempt rows=%d: %w", rows, err)
			}
			return execOne(tx, `UPDATE backup_asset_recovery_grants SET delete_attempt_id = ? WHERE id = ?`,
				competingAttemptID, deleteGrantID)
		},
	)
	runMismatch(
		"consumed delete history rejects attempt fence mismatch",
		mainProbe(3, 1, 1), mainProbe(3, 1, 2),
		func(tx *sql.Tx) error {
			if err := dropTrigger(
				tx, "backup_asset_recovery_attempts", "trg_backup_asset_recovery_attempts_integrity",
			); err != nil {
				return err
			}
			return execOne(tx, `UPDATE backup_asset_recovery_attempts SET fence = 2 WHERE id = ?`,
				aggregate.AttemptID)
		},
	)
	runMismatch(
		"consumed delete history rejects node fence mismatch",
		mainProbe(4, 1, 1), mainProbe(4, 2, 1),
		func(tx *sql.Tx) error {
			return execOne(tx, `UPDATE backup_asset_recovery_node_leases SET fence = 2 WHERE id = ?`,
				aggregate.NodeLeaseID)
		},
	)
	runMismatch(
		"consumed delete history rejects validated grant mismatch",
		mainProbe(5, 1, 1), mainProbe(5, 1, 1),
		func(tx *sql.Tx) error {
			if err := dropTrigger(
				tx, "backup_asset_recovery_checkpoints", "trg_backup_asset_recovery_checkpoints_immutable",
			); err != nil {
				return err
			}
			return execOne(tx, `UPDATE backup_asset_recovery_checkpoints
				SET delete_grant_binding_digest = ? WHERE id = ?`,
				recoveryMigrationDigest(base+62), consumedCheckpointID)
		},
	)
}

func TestBackupAssetMigration069WorkerCancelQueuedSQLite(t *testing.T) {
	newSQLiteMigrationFixture(t).test069WorkerCancelQueued(t)
}

func TestBackupAssetMigration069WorkerCancelQueuedPostgres(t *testing.T) {
	newRequiredPostgresRecoveryMigrationFixture(t).test069WorkerCancelQueued(t)
}

func TestBackupAssetMigration069WorkerPreWriteSourceDriftSQLite(t *testing.T) {
	newSQLiteMigrationFixture(t).test069WorkerPreWriteSourceDrift(t)
}

func TestBackupAssetMigration069WorkerPreWriteSourceDriftPostgres(t *testing.T) {
	newRequiredPostgresRecoveryMigrationFixture(t).test069WorkerPreWriteSourceDrift(t)
}

func TestRecoveryReviewF3PreWriteDriftTransactionPostgres(t *testing.T) {
	newRequiredPostgresRecoveryMigrationFixture(t).test069WorkerPreWriteAuthorityDrift(t)
}

func TestBackupAssetMigration069PublicationAndDeadlineIntegritySQLite(t *testing.T) {
	newSQLiteMigrationFixture(t).test069PublicationAndDeadlineIntegrity(t)
}

func TestBackupAssetMigration069PublicationAndDeadlineIntegrityPostgres(t *testing.T) {
	newRequiredPostgresMigrationFixture(t).test069PublicationAndDeadlineIntegrity(t)
}

func TestBackupAssetMigration069ResultClassificationSQLite(t *testing.T) {
	newSQLiteMigrationFixture(t).test069ResultClassification(t)
}

func TestBackupAssetMigration069ResultClassificationPostgres(t *testing.T) {
	newRequiredPostgresMigrationFixture(t).test069ResultClassification(t)
}

func TestBackupAssetMigration069CleanedResultSetTombstoneSQLite(t *testing.T) {
	newSQLiteMigrationFixture(t).test069CleanedResultSetTombstonePermanence(t)
}

func TestBackupAssetMigration069CleanedResultSetTombstonePostgres(t *testing.T) {
	newRequiredPostgresMigrationFixture(t).test069CleanedResultSetTombstonePermanence(t)
}

func TestBackupAssetMigration069CleanedResultSetReplaceSQLite(t *testing.T) {
	newSQLiteMigrationFixture(t).test069CleanedResultSetSQLiteReplacementBarrier(t)
}

func TestBackupAssetMigration069RecoveryResultContentAuthorizationSQLite(t *testing.T) {
	newSQLiteMigrationFixture(t).test069RecoveryResultContentAuthorization(t)
}

func TestBackupAssetMigration069RecoveryResultContentAuthorizationPostgres(t *testing.T) {
	newRequiredPostgresMigrationFixture(t).test069RecoveryResultContentAuthorization(t)
}

func TestBackupAssetMigration069StrictOpaqueDigestAndTemporalContractsSQLite(t *testing.T) {
	newSQLiteMigrationFixture(t).test069StrictOpaqueDigestAndTemporalContracts(t)
}

func TestBackupAssetMigration069StrictOpaqueDigestAndTemporalContractsPostgres(t *testing.T) {
	newRequiredPostgresMigrationFixture(t).test069StrictOpaqueDigestAndTemporalContracts(t)
}

func TestBackupAssetMigration069AdmissionExpiryEqualitySQLite(t *testing.T) {
	newSQLiteMigrationFixture(t).test069AdmissionExpiryEquality(t)
}

func TestBackupAssetMigration069AdmissionExpiryEqualityPostgres(t *testing.T) {
	newRequiredPostgresMigrationFixture(t).test069AdmissionExpiryEquality(t)
}

func TestBackupAssetMigration069Postgres(t *testing.T) {
	runBackupAssetMigration069Contract(t, newRequiredPostgresMigrationFixture(t))
}

func TestBackupAssetMigration070SQLite(t *testing.T) {
	runBackupAssetMigration070Contract(t, newSQLiteMigrationFixture(t))
}

func TestBackupAssetMigration070PointRevisionSQLite(t *testing.T) {
	assertBackupAssetLifecyclePointRevision(t, newSQLiteMigrationFixture(t))
}

func TestBackupAssetMigration070PolicyRevisionSnapshotSQLite(t *testing.T) {
	fixture := newSQLiteMigrationFixture(t)
	_, db := fixture.openAt(t, backupAssetLifecycleVersion)
	assertBackupAssetLifecyclePolicyRevisionSnapshot(t, fixture, db)
}

func TestBackupAssetMigration070TombstoneTerminalFactsSQLite(t *testing.T) {
	fixture := newSQLiteMigrationFixture(t)
	_, db := fixture.openAt(t, backupAssetLifecycleVersion)
	assertBackupAssetLifecycleTombstoneTerminalFacts(t, fixture, db)
}

func TestBackupAssetMigration070TombstoneCompositeHistorySQLite(t *testing.T) {
	fixture := newSQLiteMigrationFixture(t)
	_, db := fixture.openAt(t, backupAssetLifecycleVersion)
	assertBackupAssetLifecycleTombstoneCompositeHistory(t, fixture, db)
}

func TestBackupAssetMigration070TombstoneCompositeHistoryPostgres(t *testing.T) {
	fixture := newRequiredPostgresMigrationFixture(t)
	_, db := fixture.openAt(t, backupAssetLifecycleVersion)
	assertBackupAssetLifecycleTombstoneCompositeHistory(t, fixture, db)
}

func TestBackupAssetMigration070SharedContractRegistersTombstoneTerminalFacts(t *testing.T) {
	for _, check := range backupAssetMigration070SharedChecks() {
		if check.name == "TombstoneTerminalFacts" {
			if check.run == nil {
				t.Fatal("TombstoneTerminalFacts shared contract check has no executable runner")
			}
			return
		}
	}
	t.Fatal("shared 000070 contract does not register TombstoneTerminalFacts")
}

func TestBackupAssetMigration070SharedContractRegistersTombstoneCompositeHistory(t *testing.T) {
	for _, check := range backupAssetMigration070SharedChecks() {
		if check.name == "TombstoneCompositeHistory" {
			if check.run == nil {
				t.Fatal("TombstoneCompositeHistory shared contract check has no executable runner")
			}
			return
		}
	}
	t.Fatal("shared 000070 contract does not register TombstoneCompositeHistory")
}

func TestBackupAssetMigration070PairedFiles(t *testing.T) {
	testCases := []struct {
		name string
		fs   interface {
			ReadFile(string) ([]byte, error)
		}
		path string
	}{
		{name: "SQLiteUp", fs: sqliteMigrationsFS, path: "migrations/sqlite/000070_backup_asset_lifecycle.up.sql"},
		{name: "SQLiteDown", fs: sqliteMigrationsFS, path: "migrations/sqlite/000070_backup_asset_lifecycle.down.sql"},
		{name: "PostgresUp", fs: postgresMigrationsFS, path: "migrations/postgres/000070_backup_asset_lifecycle.up.sql"},
		{name: "PostgresDown", fs: postgresMigrationsFS, path: "migrations/postgres/000070_backup_asset_lifecycle.down.sql"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			script, err := testCase.fs.ReadFile(testCase.path)
			if err != nil {
				t.Fatalf("read paired 000070 migration: %v", err)
			}
			text := strings.ToLower(string(script))
			for _, fragment := range append(append([]string(nil), backupAssetLifecycleTables...),
				"retention_worker", "point_revision", "trg_recovery_points_point_revision",
				"schema_migrations", "trg_backup_asset_lifecycle_downgrade_admission") {
				if !strings.Contains(text, fragment) {
					t.Fatalf("%s is missing lifecycle contract %q", testCase.path, fragment)
				}
			}
			if strings.HasSuffix(testCase.path, ".up.sql") {
				for _, fragment := range []string{
					"repository", "task_link", "operational", "legal", "retention_expire", "explicit_purge",
					"mutable_retire", "provider_delete", "tombstoning", "managed_history", "encrypted_reason",
					"encrypted_release_reason", "encrypted_provider_locator", "encrypted_evidence",
				} {
					if !strings.Contains(text, fragment) {
						t.Fatalf("%s is missing closed lifecycle value %q", testCase.path, fragment)
					}
				}
				attemptStart := strings.Index(text, "create table recovery_point_lifecycle_attempts")
				if attemptStart < 0 {
					t.Fatalf("%s is missing the lifecycle attempt definition", testCase.path)
				}
				attemptEnd := strings.Index(text[attemptStart:], "\n);")
				if attemptEnd < 0 {
					t.Fatalf("%s lifecycle attempt definition is incomplete", testCase.path)
				}
				attemptDefinition := text[attemptStart : attemptStart+attemptEnd]
				if strings.Contains(attemptDefinition, "\n    fence_token ") {
					t.Fatalf("%s persists a raw lifecycle fence token", testCase.path)
				}
			}
		})
	}
}

func TestBackupAssetMigration070UsedDownAdmissionSQLite(t *testing.T) {
	runBackupAssetMigration070UsedDownAdmission(t, newSQLiteMigrationFixture(t))
}

func TestBackupAssetMigration070Postgres(t *testing.T) {
	runBackupAssetMigration070Contract(t, newRequiredPostgresMigrationFixture(t))
}

func TestBackupAssetMigration070UsedDownAdmissionPostgres(t *testing.T) {
	runBackupAssetMigration070UsedDownAdmission(t, newRequiredPostgresMigrationFixture(t))
}

func TestBackupAssetMigration071SQLite(t *testing.T) {
	runBackupAssetMigration071Contract(t, newSQLiteMigrationFixture(t))
}

func TestBackupAssetMigration071Postgres(t *testing.T) {
	runBackupAssetMigration071Contract(t, newRequiredPostgresMigrationFixture(t))
}

func TestBackupAssetMigration071UsedDownAdmissionSQLite(t *testing.T) {
	runBackupAssetMigration071UsedDownAdmission(t, newSQLiteMigrationFixture(t))
}

func TestBackupAssetMigration071UsedDownAdmissionPostgres(t *testing.T) {
	runBackupAssetMigration071UsedDownAdmission(t, newRequiredPostgresMigrationFixture(t))
}

func TestBackupAssetMigration071PairedFiles(t *testing.T) {
	testCases := []struct {
		name string
		fs   interface {
			ReadFile(string) ([]byte, error)
		}
		path string
	}{
		{name: "SQLiteUp", fs: sqliteMigrationsFS, path: "migrations/sqlite/000071_backup_asset_ga.up.sql"},
		{name: "SQLiteDown", fs: sqliteMigrationsFS, path: "migrations/sqlite/000071_backup_asset_ga.down.sql"},
		{name: "PostgresUp", fs: postgresMigrationsFS, path: "migrations/postgres/000071_backup_asset_ga.up.sql"},
		{name: "PostgresDown", fs: postgresMigrationsFS, path: "migrations/postgres/000071_backup_asset_ga.down.sql"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			script, err := testCase.fs.ReadFile(testCase.path)
			if err != nil {
				t.Fatalf("read paired 000071 migration: %v", err)
			}
			text := strings.ToLower(string(script))
			for _, fragment := range append(append([]string(nil), backupAssetGATables...),
				"schema_migrations", "trg_backup_asset_ga_downgrade_admission",
				"fresh", "existing", "unknown", "blocked", "ready", "acknowledged",
				"shared_restic_identity", "task_repository_mismatch", "capability_gap", "command_unsupported") {
				if !strings.Contains(text, fragment) {
					t.Fatalf("%s is missing GA contract %q", testCase.path, fragment)
				}
			}
		})
	}
}

func TestBackupAssetMigration072SQLite(t *testing.T) {
	runBackupAssetMigration072Contract(t, newSQLiteMigrationFixture(t))
}

func TestBackupAssetMigration072Postgres(t *testing.T) {
	runBackupAssetMigration072Contract(t, newRequiredPostgresMigrationFixture(t))
}

func TestBackupAssetMigration072PairedFiles(t *testing.T) {
	testCases := []struct {
		name string
		fs   interface {
			ReadFile(string) ([]byte, error)
		}
		path string
	}{
		{name: "SQLiteUp", fs: sqliteMigrationsFS, path: "migrations/sqlite/000072_task_run_snapshot_compatibility.up.sql"},
		{name: "SQLiteDown", fs: sqliteMigrationsFS, path: "migrations/sqlite/000072_task_run_snapshot_compatibility.down.sql"},
		{name: "PostgresUp", fs: postgresMigrationsFS, path: "migrations/postgres/000072_task_run_snapshot_compatibility.up.sql"},
		{name: "PostgresDown", fs: postgresMigrationsFS, path: "migrations/postgres/000072_task_run_snapshot_compatibility.down.sql"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			script, err := testCase.fs.ReadFile(testCase.path)
			if err != nil {
				t.Fatalf("read paired 000072 migration: %v", err)
			}
			text := strings.ToLower(string(script))
			for _, fragment := range []string{
				"task_runs", "node_id_snapshot", "success", "failed", "canceled", "warning", "skipped",
				"trg_backup_asset_recovery_task_runs_node_snapshot_insert",
				"trg_backup_asset_recovery_task_runs_node_snapshot_immutable",
				"trg_backup_asset_task_runs_legacy_unknown_status_immutable",
				"trg_backup_asset_task_run_snapshot_compatibility_downgrade_admission",
			} {
				if !strings.Contains(text, fragment) {
					t.Fatalf("%s is missing TaskRun compatibility contract %q", testCase.path, fragment)
				}
			}
		})
	}
}

func runBackupAssetMigration072Contract(t *testing.T, fixture migrationFixture) {
	t.Helper()
	t.Run("ConvergesFromPre69AndOriginalClean71", fixture.test072ConvergesUpgradePaths)
	t.Run("LegacyUnknownActiveDriftIsRejectedAtomically", fixture.test072RejectsLegacyUnknownActiveDrift)
	t.Run("OrdinaryWritesAndLegacyStatusAreClosed", fixture.test072TaskRunWriteContract)
	t.Run("CleanVersionMissingFinalContractIsRejected", fixture.test072SchemaDriftIsRejected)
	t.Run("SingleNodeDeletePreservesPre69TerminalRun", fixture.testNodeDeletePreservesPre69TerminalRun)
	t.Run("BatchNodeDeletePreservesPositiveSnapshots", fixture.testBatchNodeDeletePreservesPositiveSnapshots)
	t.Run("UsedDownWithLegacyUnknownIsRejectedAtomically", fixture.test072UsedDownWithLegacyUnknownIsAtomic)
	t.Run("PristineDownRestores071", fixture.test072PristineDown)
}

type taskRunCompatibilitySchema struct {
	tableDefinition   string
	indexDefinition   string
	insertTrigger     string
	immutableTrigger  string
	statusTrigger     string
	admissionTrigger  string
	guardFunction     string
	statusFunction    string
	admissionFunction string
}

func (fixture migrationFixture) captureTaskRunCompatibilitySchema(t *testing.T, db *sql.DB) taskRunCompatibilitySchema {
	t.Helper()
	normalize := func(definition string) string { return definition }
	if fixture.engine == "postgres" {
		var schema string
		if err := db.QueryRow(`SELECT current_schema()`).Scan(&schema); err != nil {
			t.Fatalf("load PostgreSQL schema for TaskRun compatibility comparison: %v", err)
		}
		schemaPrefix := strings.ToLower(schema) + "."
		normalize = func(definition string) string {
			return strings.ReplaceAll(definition, schemaPrefix, "")
		}
	}
	statusTrigger := ""
	if fixture.recoveryTriggerExists(t, db, "task_runs", "trg_backup_asset_task_runs_legacy_unknown_status_immutable") {
		statusTrigger = fixture.recoveryTriggerDefinition(t, db, "task_runs", "trg_backup_asset_task_runs_legacy_unknown_status_immutable")
	}
	return taskRunCompatibilitySchema{
		tableDefinition:   normalize(fixture.tableDefinition(t, db, "task_runs")),
		indexDefinition:   normalize(fixture.indexDefinition(t, db, "idx_task_runs_node_snapshot_status")),
		insertTrigger:     normalize(fixture.recoveryTriggerDefinition(t, db, "task_runs", "trg_backup_asset_recovery_task_runs_node_snapshot_insert")),
		immutableTrigger:  normalize(fixture.recoveryTriggerDefinition(t, db, "task_runs", "trg_backup_asset_recovery_task_runs_node_snapshot_immutable")),
		statusTrigger:     normalize(statusTrigger),
		admissionTrigger:  normalize(fixture.recoveryTriggerDefinition(t, db, "schema_migrations", "trg_backup_asset_task_run_snapshot_compatibility_downgrade_admission")),
		guardFunction:     normalize(fixture.recoveryFunctionDefinition(t, db, "backup_asset_recovery_task_run_node_snapshot_guard")),
		statusFunction:    normalize(fixture.recoveryFunctionDefinition(t, db, "backup_asset_task_run_legacy_unknown_status_guard")),
		admissionFunction: normalize(fixture.recoveryFunctionDefinition(t, db, "backup_asset_task_run_snapshot_compatibility_downgrade_admission")),
	}
}

func (fixture migrationFixture) seedTerminalOrphanBefore069(t *testing.T, db *sql.DB, runID, taskID int64) {
	t.Helper()
	now := time.Date(2026, 8, 23, 6, 7, 8, 0, time.UTC)
	fixture.mustExec(t, db, `INSERT INTO task_runs
		(id, task_id, trigger_type, status, created_at, updated_at)
		VALUES (?, ?, 'scheduled', 'success', ?, ?)`, runID, taskID, now, now)
}

func (fixture migrationFixture) test072ConvergesUpgradePaths(t *testing.T) {
	pre69Migrator, pre69DB := fixture.openAt(t, backupAssetExportVersion)
	fixture.seedTerminalOrphanBefore069(t, pre69DB, 772001, 772099)
	migrateToBackupAssetVersion(t, pre69Migrator, backupAssetTaskRunCompatVersion)
	assertMigrationVersion(t, pre69Migrator, backupAssetTaskRunCompatVersion)
	var snapshot int64
	if err := pre69DB.QueryRow(fixture.bind(`SELECT node_id_snapshot FROM task_runs WHERE id = ?`), int64(772001)).Scan(&snapshot); err != nil {
		t.Fatalf("load %s pre-69 terminal orphan after 000072: %v", fixture.engine, err)
	}
	if snapshot != 0 {
		t.Fatalf("%s pre-69 terminal orphan snapshot=%d, want legacy_unknown 0", fixture.engine, snapshot)
	}
	pre69Schema := fixture.captureTaskRunCompatibilitySchema(t, pre69DB)

	clean71Migrator, clean71DB := fixture.openAt(t, backupAssetGAVersion)
	if fixture.engine == "postgres" {
		fixture.mustExec(t, clean71DB, `ALTER TABLE task_runs DROP CONSTRAINT task_runs_node_id_snapshot_positive`)
		fixture.mustExec(t, clean71DB, `ALTER TABLE task_runs ADD CONSTRAINT task_runs_node_id_snapshot_positive CHECK (node_id_snapshot > 0)`)
	}
	migrateToBackupAssetVersion(t, clean71Migrator, backupAssetTaskRunCompatVersion)
	assertMigrationVersion(t, clean71Migrator, backupAssetTaskRunCompatVersion)
	clean71Schema := fixture.captureTaskRunCompatibilitySchema(t, clean71DB)
	if !reflect.DeepEqual(pre69Schema, clean71Schema) {
		t.Fatalf("%s 000072 did not converge pre-69 and original-clean-71 schemas\npre-69: %#v\nclean-71: %#v",
			fixture.engine, pre69Schema, clean71Schema)
	}
}

func (fixture migrationFixture) test072RejectsLegacyUnknownActiveDrift(t *testing.T) {
	migrator, db := fixture.openAt(t, backupAssetExportVersion)
	fixture.seedTerminalOrphanBefore069(t, db, 772051, 772059)
	migrateToBackupAssetVersion(t, migrator, backupAssetGAVersion)
	if fixture.engine == "postgres" {
		fixture.mustExec(t, db, `ALTER TABLE task_runs DROP CONSTRAINT task_runs_node_id_snapshot_positive`)
	}
	fixture.mustExec(t, db, `UPDATE task_runs SET status = 'running' WHERE id = ?`, int64(772051))

	if err := migrator.Steps(1); err == nil {
		t.Fatalf("%s 000072 unexpectedly accepted active legacy_unknown TaskRun drift", fixture.engine)
	}
	_, dirty, err := migrator.Version()
	if err != nil {
		t.Fatalf("read %s migration version after rejected 000072: %v", fixture.engine, err)
	}
	if !dirty {
		t.Fatalf("%s rejected 000072 did not leave a dirty fail-closed version", fixture.engine)
	}
	if fixture.recoveryTriggerExists(t, db, "task_runs", "trg_backup_asset_task_runs_legacy_unknown_status_immutable") {
		t.Fatalf("%s rejected 000072 left a partial legacy_unknown status trigger", fixture.engine)
	}
	var count int
	if err := db.QueryRow(fixture.bind(`SELECT COUNT(*) FROM task_runs
		WHERE id = ? AND node_id_snapshot = 0 AND status = 'running'`), int64(772051)).Scan(&count); err != nil {
		t.Fatalf("count %s active legacy_unknown row after rejected 000072: %v", fixture.engine, err)
	}
	if count != 1 {
		t.Fatalf("%s rejected 000072 changed active legacy_unknown row count=%d, want 1", fixture.engine, count)
	}
}

func (fixture migrationFixture) test072TaskRunWriteContract(t *testing.T) {
	migrator, db := fixture.openAt(t, backupAssetExportVersion)
	fixture.seedTerminalOrphanBefore069(t, db, 772101, 772199)
	now := time.Date(2026, 8, 23, 7, 8, 9, 0, time.UTC)
	fixture.mustExec(t, db, `INSERT INTO nodes
		(id, name, host, username, backup_dir, created_at, updated_at)
		VALUES (?, 'compat-node', '127.0.0.1', 'root', '/tmp/compat-node', ?, ?)`, int64(772102), now, now)
	fixture.mustExec(t, db, `INSERT INTO tasks
		(id, name, node_id, executor_type, status, created_at, updated_at)
		VALUES (?, 'compat-task', ?, 'rsync', 'running', ?, ?)`, int64(772103), int64(772102), now, now)
	migrateToBackupAssetVersion(t, migrator, backupAssetTaskRunCompatVersion)

	fixture.expectExecRejectedInRollback(t, db, `INSERT INTO task_runs
		(id, task_id, node_id_snapshot, trigger_type, status, created_at, updated_at)
		VALUES (?, ?, 0, 'manual', 'success', ?, ?)`, int64(772104), int64(772199), now, now)
	fixture.expectExecRejectedInRollback(t, db, `INSERT INTO task_runs
		(id, task_id, node_id_snapshot, trigger_type, status, created_at, updated_at)
		VALUES (?, ?, ?, 'manual', 'running', ?, ?)`, int64(772105), int64(772103), int64(772102+1), now, now)
	fixture.mustExec(t, db, `INSERT INTO task_runs
		(id, task_id, node_id_snapshot, trigger_type, status, created_at, updated_at)
		VALUES (?, ?, ?, 'manual', 'running', ?, ?)`, int64(772106), int64(772103), int64(772102), now, now)
	fixture.expectExecRejectedInRollback(t, db, `UPDATE task_runs SET task_id = ? WHERE id = ?`, int64(772199), int64(772106))
	fixture.expectExecRejectedInRollback(t, db, `UPDATE task_runs SET node_id_snapshot = ? WHERE id = ?`, int64(772102+1), int64(772106))
	fixture.expectExecRejectedInRollback(t, db, `UPDATE task_runs SET status = 'running' WHERE id = ?`, int64(772101))
}

func (fixture migrationFixture) test072SchemaDriftIsRejected(t *testing.T) {
	migrator, db := fixture.openAt(t, backupAssetTaskRunCompatVersion)
	if fixture.engine == "postgres" {
		fixture.mustExec(t, db, `DROP TRIGGER trg_backup_asset_task_runs_legacy_unknown_status_immutable ON task_runs`)
	} else {
		fixture.mustExec(t, db, `DROP TRIGGER trg_backup_asset_task_runs_legacy_unknown_status_immutable`)
	}
	before := fixture.captureTaskRunCompatibilitySchema(t, db)
	gdb := fixture.recoveryWorkerGorm(t, db)
	err := RunMigrations(gdb, fixture.engine)
	if !errors.Is(err, ErrMigrationSchemaDrift) {
		t.Fatalf("clean %s version 72 missing final contract returned %v, want ErrMigrationSchemaDrift", fixture.engine, err)
	}
	assertMigrationVersion(t, migrator, backupAssetTaskRunCompatVersion)
	after := fixture.captureTaskRunCompatibilitySchema(t, db)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("%s final-schema drift rejection mutated schema\nbefore: %#v\nafter: %#v", fixture.engine, before, after)
	}
}

func (fixture migrationFixture) testNodeDeletePreservesPre69TerminalRun(t *testing.T) {
	migrator, db := fixture.openAt(t, backupAssetExportVersion)
	now := time.Date(2026, 8, 23, 9, 10, 11, 0, time.UTC)
	const nodeID, taskID, runID int64 = 772301, 772302, 772303
	fixture.mustExec(t, db, `INSERT INTO nodes
		(id, name, host, username, backup_dir, created_at, updated_at)
		VALUES (?, 'pre69-delete-node', '192.0.2.31', 'root', '/srv/pre69-delete', ?, ?)`, nodeID, now, now)
	fixture.mustExec(t, db, `INSERT INTO tasks
		(id, name, node_id, executor_type, status, created_at, updated_at)
		VALUES (?, 'pre69-delete-task', ?, 'rsync', 'success', ?, ?)`, taskID, nodeID, now, now)
	fixture.mustExec(t, db, `INSERT INTO task_runs
		(id, task_id, trigger_type, status, created_at, updated_at)
		VALUES (?, ?, 'scheduled', 'success', ?, ?)`, runID, taskID, now, now)

	repository := gormrepo.NewNodeRepository(fixture.recoveryWorkerGorm(t, db))
	if err := repository.DeleteWithAssociations(context.Background(), uint(nodeID)); err != nil {
		t.Fatalf("delete %s pre-69 node with associations: %v", fixture.engine, err)
	}
	fixture.assertTaskDeletedRunRetained(t, db, taskID, runID)

	migrateToBackupAssetVersion(t, migrator, backupAssetTaskRunCompatVersion)
	var snapshot int64
	if err := db.QueryRow(fixture.bind(`SELECT node_id_snapshot FROM task_runs WHERE id = ?`), runID).Scan(&snapshot); err != nil {
		t.Fatalf("load %s retained pre-69 TaskRun after upgrade: %v", fixture.engine, err)
	}
	if snapshot != int64(model.TaskRunNodeIDLegacyUnknown) {
		t.Fatalf("%s retained pre-69 TaskRun snapshot=%d, want legacy_unknown 0", fixture.engine, snapshot)
	}
}

func (fixture migrationFixture) testBatchNodeDeletePreservesPositiveSnapshots(t *testing.T) {
	_, db := fixture.openAt(t, backupAssetTaskRunCompatVersion)
	now := time.Date(2026, 8, 23, 10, 11, 12, 0, time.UTC)
	for offset := int64(0); offset < 2; offset++ {
		nodeID := int64(772401) + offset
		taskID := int64(772411) + offset
		runID := int64(772421) + offset
		fixture.mustExec(t, db, `INSERT INTO nodes
			(id, name, host, username, backup_dir, created_at, updated_at)
			VALUES (?, ?, ?, 'root', ?, ?, ?)`, nodeID, fmt.Sprintf("post69-delete-node-%d", offset), fmt.Sprintf("192.0.2.%d", 41+offset), fmt.Sprintf("/srv/post69-delete-%d", offset), now, now)
		fixture.mustExec(t, db, `INSERT INTO tasks
			(id, name, node_id, executor_type, status, created_at, updated_at)
			VALUES (?, ?, ?, 'rsync', 'success', ?, ?)`, taskID, fmt.Sprintf("post69-delete-task-%d", offset), nodeID, now, now)
		fixture.mustExec(t, db, `INSERT INTO task_runs
			(id, task_id, node_id_snapshot, trigger_type, status, created_at, updated_at)
			VALUES (?, ?, ?, 'scheduled', 'success', ?, ?)`, runID, taskID, nodeID, now, now)
	}

	repository := gormrepo.NewNodeRepository(fixture.recoveryWorkerGorm(t, db))
	deleted, notFound, err := repository.BatchDeleteWithAssociations(context.Background(), []uint{772401, 772402, 772499})
	if err != nil {
		t.Fatalf("batch delete %s nodes with associations: %v", fixture.engine, err)
	}
	if deleted != 2 || !reflect.DeepEqual(notFound, []uint{772499}) {
		t.Fatalf("%s batch node delete result deleted=%d notFound=%v", fixture.engine, deleted, notFound)
	}
	for offset := int64(0); offset < 2; offset++ {
		taskID := int64(772411) + offset
		runID := int64(772421) + offset
		fixture.assertTaskDeletedRunRetained(t, db, taskID, runID)
		var snapshot int64
		if err := db.QueryRow(fixture.bind(`SELECT node_id_snapshot FROM task_runs WHERE id = ?`), runID).Scan(&snapshot); err != nil {
			t.Fatalf("load %s retained post-69 TaskRun: %v", fixture.engine, err)
		}
		if snapshot != int64(772401)+offset {
			t.Fatalf("%s retained post-69 TaskRun snapshot=%d, want %d", fixture.engine, snapshot, int64(772401)+offset)
		}
	}
}

func (fixture migrationFixture) assertTaskDeletedRunRetained(t *testing.T, db *sql.DB, taskID, runID int64) {
	t.Helper()
	var taskCount, runCount int
	if err := db.QueryRow(fixture.bind(`SELECT COUNT(*) FROM tasks WHERE id = ?`), taskID).Scan(&taskCount); err != nil {
		t.Fatalf("count %s task after node delete: %v", fixture.engine, err)
	}
	if err := db.QueryRow(fixture.bind(`SELECT COUNT(*) FROM task_runs WHERE id = ?`), runID).Scan(&runCount); err != nil {
		t.Fatalf("count %s TaskRun after node delete: %v", fixture.engine, err)
	}
	if taskCount != 0 || runCount != 1 {
		t.Fatalf("%s node delete task count=%d TaskRun count=%d, want 0 and 1", fixture.engine, taskCount, runCount)
	}
}

func (fixture migrationFixture) test072UsedDownWithLegacyUnknownIsAtomic(t *testing.T) {
	migrator, db := fixture.openAt(t, backupAssetExportVersion)
	fixture.seedTerminalOrphanBefore069(t, db, 772201, 772299)
	migrateToBackupAssetVersion(t, migrator, backupAssetTaskRunCompatVersion)
	before := fixture.captureTaskRunCompatibilitySchema(t, db)
	if err := migrator.Steps(-1); err == nil {
		t.Fatalf("%s 000072 down unexpectedly accepted legacy_unknown TaskRun", fixture.engine)
	}
	assertMigrationVersion(t, migrator, backupAssetTaskRunCompatVersion)
	after := fixture.captureTaskRunCompatibilitySchema(t, db)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("%s rejected 000072 down changed compatibility schema\nbefore: %#v\nafter: %#v", fixture.engine, before, after)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM task_runs WHERE node_id_snapshot = 0`).Scan(&count); err != nil {
		t.Fatalf("count %s legacy_unknown TaskRuns after rejected 000072 down: %v", fixture.engine, err)
	}
	if count != 1 {
		t.Fatalf("%s rejected 000072 down changed legacy_unknown count=%d, want 1", fixture.engine, count)
	}
}

func (fixture migrationFixture) test072PristineDown(t *testing.T) {
	migrator, db := fixture.openAt(t, backupAssetTaskRunCompatVersion)
	if err := migrator.Steps(-1); err != nil {
		t.Fatalf("step %s pristine 000072 down: %v", fixture.engine, err)
	}
	assertMigrationVersion(t, migrator, backupAssetGAVersion)
	for _, trigger := range []struct{ table, name string }{
		{table: "task_runs", name: "trg_backup_asset_task_runs_legacy_unknown_status_immutable"},
		{table: "schema_migrations", name: "trg_backup_asset_task_run_snapshot_compatibility_downgrade_admission"},
	} {
		if fixture.recoveryTriggerExists(t, db, trigger.table, trigger.name) {
			t.Fatalf("%s pristine 000072 down left trigger %s", fixture.engine, trigger.name)
		}
	}
	for _, trigger := range []string{
		"trg_backup_asset_recovery_task_runs_node_snapshot_insert",
		"trg_backup_asset_recovery_task_runs_node_snapshot_immutable",
	} {
		if !fixture.recoveryTriggerExists(t, db, "task_runs", trigger) {
			t.Fatalf("%s pristine 000072 down removed 000069 trigger %s", fixture.engine, trigger)
		}
	}
	if fixture.engine == "postgres" {
		definition := fixture.tableDefinition(t, db, "task_runs")
		if !strings.Contains(definition, "node_id_snapshot > 0") {
			t.Fatalf("PostgreSQL pristine 000072 down did not restore positive snapshot constraint: %s", definition)
		}
	}
}

func runBackupAssetMigration071Contract(t *testing.T, fixture migrationFixture) {
	t.Helper()
	t.Run("ApplyAndModelParity", func(t *testing.T) {
		migrator, db := fixture.openAt(t, backupAssetGAVersion)
		assertMigrationVersion(t, migrator, backupAssetGAVersion)
		assertBackupAssetGASchema071(t, fixture, db)
		if fixture.engine == "sqlite" {
			assertSQLiteForeignKeyCheck(t, db)
		}
	})
	t.Run("UTCRoundTrip", func(t *testing.T) {
		_, db := fixture.openAt(t, backupAssetGAVersion)
		now := "2026-08-20 02:03:04+00:00"
		installationID := strings.Repeat("a", 32)
		fixture.mustExec(t, db, `INSERT INTO backup_asset_installations
			(id, class, readiness, inventory_digest, created_at, updated_at)
			VALUES (?, 'fresh', 'unknown', '', ?, ?)`, installationID, now, now)
		var createdAt time.Time
		if err := db.QueryRow(fixture.bind(`SELECT created_at FROM backup_asset_installations WHERE id = ?`), installationID).Scan(&createdAt); err != nil {
			t.Fatalf("scan GA UTC created_at: %v", err)
		}
		if createdAt.Location() != time.UTC || createdAt.Format(time.RFC3339) != "2026-08-20T02:03:04Z" {
			t.Fatalf("GA UTC timestamp drifted: %s (%s)", createdAt.Format(time.RFC3339), createdAt.Location())
		}
	})
	t.Run("ClosedConstraints", func(t *testing.T) {
		_, db := fixture.openAt(t, backupAssetGAVersion)
		now := time.Date(2026, 8, 20, 4, 0, 0, 0, time.UTC)
		fixture.expectExecRejected(t, db, `INSERT INTO backup_asset_installations
			(id, class, readiness, inventory_digest, created_at, updated_at)
			VALUES (?, 'upgrade', 'unknown', '', ?, ?)`, strings.Repeat("b", 32), now, now)
		fixture.expectExecRejected(t, db, `INSERT INTO backup_asset_installations
			(id, class, readiness, inventory_digest, ack_actor_id, ack_at, created_at, updated_at)
			VALUES (?, 'fresh', 'acknowledged', ?, 1, ?, ?, ?)`,
			strings.Repeat("c", 32), strings.Repeat("d", 64), now, now, now)
		fixture.expectExecRejected(t, db, `INSERT INTO backup_asset_inventory_runs
			(id, digest, status, counts_json, error_category, created_at, updated_at)
			VALUES (?, ?, 'complete', '{}', 'raw_locator', ?, ?)`,
			strings.Repeat("e", 32), strings.Repeat("f", 64), now, now)
		fixture.expectExecRejected(t, db, `INSERT INTO backup_asset_repository_conflicts
			(id, run_id, kind, task_ids_json, repository_id, stable_reason_code, created_at)
			VALUES (?, ?, 'owned_merge', '[]', '', 'backup_assets.ga.invalid', ?)`,
			strings.Repeat("1", 32), strings.Repeat("2", 32), now)
	})
	t.Run("PreservesExisting070Facts", func(t *testing.T) {
		migrator, db := fixture.openAt(t, backupAssetLifecycleVersion)
		seed := fixture.seedLifecycleMigrationBase(t, db, "7", 7101)
		seedLifecycleRetentionPolicy(t, fixture, db, seed)
		migrateToBackupAssetVersion(t, migrator, backupAssetGAVersion)
		assertMigrationVersion(t, migrator, backupAssetGAVersion)
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM backup_retention_policies`).Scan(&count); err != nil {
			t.Fatalf("count preserved 000070 policy: %v", err)
		}
		if count != 1 {
			t.Fatalf("%s 000071 migration lost existing 000070 retention policy", fixture.engine)
		}
	})
	t.Run("PristineDownDropsGATables", func(t *testing.T) {
		migrator, db := fixture.openAt(t, backupAssetGAVersion)
		if err := migrator.Steps(-1); err != nil {
			t.Fatalf("step %s pristine 000071 down: %v", fixture.engine, err)
		}
		assertMigrationVersion(t, migrator, backupAssetLifecycleVersion)
		for _, table := range backupAssetGATables {
			if databaseTableExists(t, db, fixture.engine, table) {
				t.Fatalf("%s GA table %s remains after pristine down", fixture.engine, table)
			}
		}
		if fixture.recoveryTriggerExists(t, db, "schema_migrations", "trg_backup_asset_ga_downgrade_admission") {
			t.Fatal("GA downgrade admission trigger remains after pristine down")
		}
		if fixture.engine == "sqlite" {
			assertSQLiteForeignKeyCheck(t, db)
		}
	})
}

func runBackupAssetMigration071UsedDownAdmission(t *testing.T, fixture migrationFixture) {
	t.Helper()
	testCases := []struct {
		name string
		seed func(*testing.T, migrationFixture, *sql.DB)
	}{
		{name: "ReadyInstallation", seed: seedGAReadyInstallation},
		{name: "AcknowledgedInstallation", seed: seedGAAcknowledgedInstallation},
		{name: "RepositoryConflict", seed: seedGARepositoryConflict},
		{name: "SuccessfulEnablement", seed: seedGASuccessfulEnablement},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			migrator, db := fixture.openAt(t, backupAssetGAVersion)
			testCase.seed(t, fixture, db)
			admissionBefore := fixture.recoveryTriggerDefinition(t, db, "schema_migrations", "trg_backup_asset_ga_downgrade_admission")
			if err := migrator.Steps(-1); err == nil {
				t.Fatalf("%s 000071 down unexpectedly succeeded with %s state", fixture.engine, testCase.name)
			}
			assertMigrationVersion(t, migrator, backupAssetGAVersion)
			if got := fixture.recoveryTriggerDefinition(t, db, "schema_migrations", "trg_backup_asset_ga_downgrade_admission"); got != admissionBefore {
				t.Fatalf("rejected down changed GA admission trigger\n got: %s\nwant: %s", got, admissionBefore)
			}
			for _, table := range backupAssetGATables {
				if !databaseTableExists(t, db, fixture.engine, table) {
					t.Fatalf("rejected down dropped GA table %s", table)
				}
			}
		})
	}
}

func assertBackupAssetGASchema071(t *testing.T, fixture migrationFixture, db *sql.DB) {
	t.Helper()
	for table, persistentModel := range backupAssetGAModels() {
		if !databaseTableExists(t, db, fixture.engine, table) {
			t.Fatalf("%s GA migration table %s is missing", fixture.engine, table)
		}
		want := gormColumnNames(t, persistentModel)
		var got []string
		if fixture.engine == "sqlite" {
			got = sqliteColumnNames(t, db, table)
			assertSQLiteTimeColumnsHaveNoDefault(t, db, table)
		} else {
			got = postgresColumnNames(t, db, table)
			assertPostgresTableTimeColumnsHaveNoDefault(t, db, table)
		}
		sort.Strings(got)
		sort.Strings(want)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("%s %s columns mismatch\n got: %v\nwant: %v", fixture.engine, table, got, want)
		}
	}
	if !fixture.recoveryTriggerExists(t, db, "schema_migrations", "trg_backup_asset_ga_downgrade_admission") {
		t.Fatal("GA downgrade admission trigger is missing")
	}
}

func backupAssetGAModels() map[string]any {
	return map[string]any{
		"backup_asset_installations":        model.BackupAssetInstallation{},
		"backup_asset_inventory_runs":       model.BackupAssetInventoryRun{},
		"backup_asset_repository_conflicts": model.BackupAssetRepositoryConflict{},
	}
}

func seedGAReadyInstallation(t *testing.T, fixture migrationFixture, db *sql.DB) {
	t.Helper()
	now := time.Date(2026, 8, 20, 5, 0, 0, 0, time.UTC)
	fixture.mustExec(t, db, `INSERT INTO backup_asset_installations
		(id, class, readiness, inventory_digest, created_at, updated_at)
		VALUES (?, 'fresh', 'ready', ?, ?, ?)`,
		strings.Repeat("a", 32), strings.Repeat("b", 64), now, now)
}

func seedGAAcknowledgedInstallation(t *testing.T, fixture migrationFixture, db *sql.DB) {
	t.Helper()
	now := time.Date(2026, 8, 20, 5, 1, 0, 0, time.UTC)
	digest := strings.Repeat("c", 64)
	fixture.mustExec(t, db, `INSERT INTO backup_asset_installations
		(id, class, readiness, inventory_digest, ack_actor_id, ack_at, created_at, updated_at)
		VALUES (?, 'existing', 'acknowledged', ?, 1, ?, ?, ?)`,
		strings.Repeat("d", 32), digest, now, now, now)
}

func seedGARepositoryConflict(t *testing.T, fixture migrationFixture, db *sql.DB) {
	t.Helper()
	now := time.Date(2026, 8, 20, 5, 2, 0, 0, time.UTC)
	runID := strings.Repeat("e", 32)
	fixture.mustExec(t, db, `INSERT INTO backup_asset_inventory_runs
		(id, digest, status, counts_json, error_category, created_at, updated_at)
		VALUES (?, ?, 'complete', ?, '', ?, ?)`,
		runID, strings.Repeat("f", 64), `{"candidates":1,"conflicts":1,"unsupported":0,"capability_gaps":0}`, now, now)
	fixture.mustExec(t, db, `INSERT INTO backup_asset_repository_conflicts
		(id, run_id, kind, task_ids_json, repository_id, stable_reason_code, created_at)
		VALUES (?, ?, 'shared_restic_identity', ?, '', 'backup_assets.ga.shared_restic_identity', ?)`,
		strings.Repeat("1", 32), runID, `[11,12]`, now)
}

func seedGASuccessfulEnablement(t *testing.T, fixture migrationFixture, db *sql.DB) {
	t.Helper()
	now := time.Date(2026, 8, 20, 5, 3, 0, 0, time.UTC)
	fixture.mustExec(t, db, `INSERT INTO backup_asset_installations
		(id, class, readiness, inventory_digest, enablement_succeeded_at, created_at, updated_at)
		VALUES (?, 'fresh', 'unknown', '', ?, ?, ?)`,
		strings.Repeat("2", 32), now, now, now)
}

func TestBackupAssetMigration069WholeTask6ClosurePostgres(t *testing.T) {
	newRequiredPostgresRecoveryMigrationFixture(t).test069WholeTask6Closure(t)
}

func TestBackupAssetMigration069PairedFiles(t *testing.T) {
	testCases := []struct {
		name string
		fs   interface {
			ReadFile(string) ([]byte, error)
		}
		path string
		up   bool
	}{
		{name: "SQLiteUp", fs: sqliteMigrationsFS, path: "migrations/sqlite/000069_backup_asset_recovery.up.sql", up: true},
		{name: "SQLiteDown", fs: sqliteMigrationsFS, path: "migrations/sqlite/000069_backup_asset_recovery.down.sql"},
		{name: "PostgresUp", fs: postgresMigrationsFS, path: "migrations/postgres/000069_backup_asset_recovery.up.sql", up: true},
		{name: "PostgresDown", fs: postgresMigrationsFS, path: "migrations/postgres/000069_backup_asset_recovery.down.sql"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			script, err := testCase.fs.ReadFile(testCase.path)
			if err != nil {
				t.Fatalf("read paired 000069 migration: %v", err)
			}
			text := strings.ToLower(string(script))
			normalized := strings.Join(strings.Fields(text), " ")
			if !strings.Contains(text, "node_id_snapshot") || !strings.Contains(text, "idx_task_runs_node_snapshot_status") {
				t.Fatalf("%s is missing the TaskRun node snapshot column/index contract", testCase.path)
			}
			engine := "postgres"
			if strings.HasPrefix(testCase.name, "SQLite") {
				engine = "sqlite"
			}
			for _, ownedTrigger := range backupAssetRecoveryOwnedTriggersForEngine(engine) {
				verb := "create trigger "
				if !testCase.up {
					if ownedTrigger.downWithTable && strings.Contains(normalized, "drop table "+ownedTrigger.table) {
						continue
					}
					verb = "drop trigger "
				}
				if !strings.Contains(normalized, verb+ownedTrigger.name) {
					t.Fatalf("%s does not own recovery trigger %q through %q", testCase.path, ownedTrigger.name, verb)
				}
			}
			if testCase.name == "PostgresUp" || testCase.name == "PostgresDown" {
				for _, function := range backupAssetRecoveryOwnedPostgresFunctions {
					verb := "create or replace function "
					if !testCase.up {
						verb = "drop function "
					}
					if !strings.Contains(normalized, verb+function) {
						t.Fatalf("%s does not own recovery function %q through %q", testCase.path, function, verb)
					}
				}
			}
			for _, table := range backupAssetRecoveryTables {
				if !strings.Contains(text, table) {
					t.Fatalf("%s is missing recovery table/guard %q", testCase.path, table)
				}
			}
			if !testCase.up {
				for _, table := range backupAssetRecoveryTables {
					if table == "backup_asset_recovery_evidence" {
						for _, fragment := range []string{
							"select 1 from backup_asset_recovery_evidence",
							"where not (",
							"kind = 'scheduler_state'",
							"id = '" + recoveryClaimSchedulerRowID + "' and scheduler_scope = 'claim'",
							"id = '" + recoveryTakeoverSchedulerRowID + "' and scheduler_scope = 'takeover'",
						} {
							if !strings.Contains(normalized, fragment) {
								t.Fatalf("%s does not preserve the scheduler-aware evidence down guard %q", testCase.path, fragment)
							}
						}
						continue
					}
					if !strings.Contains(normalized, "exists (select 1 from "+table+")") {
						t.Fatalf("%s does not guard used family %q before down", testCase.path, table)
					}
				}
				for _, fragment := range []string{
					"trg_backup_asset_recovery_downgrade_admission",
					"trg_backup_asset_recovery_attempts_mutation_arm_monotonic",
					"exists (select 1 from backup_asset_delivery_grants where resource_kind = 'recovery_result')",
					"join backup_asset_delivery_grants as grant_row on grant_row.id = request_row.grant_id",
					"where grant_row.resource_kind = 'recovery_result'",
				} {
					if !strings.Contains(text, fragment) {
						t.Fatalf("%s is missing recovery Content downgrade admission %q", testCase.path, fragment)
					}
				}
				if testCase.name == "PostgresDown" && !strings.Contains(text, "backup_asset_recovery_attempt_mutation_arm_monotonic") {
					t.Fatalf("%s does not explicitly drop the recovery attempt mutation-arm function", testCase.path)
				}
				return
			}

			for _, fragment := range []string{
				"isolated", "in_place", "immutable", "observation",
				"allow_clean", "block", "admin_override",
				"write", "exact_mirror_delete",
				"schema_use_latch", "selection_digest", "binding_digest",
				"operation_set_digest", "delete_set_digest",
				"reserved", "marker_created", "cleanup_due", "workspace_cleaned",
				"ready", "revoking", "cleaned", "cleanup_failed",
				"claimed", "delete_started", "tombstoned",
				"recovery_result", "recovery_job_id", "recovery_result_id",
				"trg_backup_asset_recovery_evidence_latch_update",
				"trg_backup_asset_recovery_evidence_latch_delete",
				"trg_backup_asset_recovery_attempts_mutation_arm_monotonic",
				"schema_migrations",
				"trg_backup_asset_recovery_downgrade_admission",
			} {
				if !strings.Contains(text, fragment) {
					t.Fatalf("%s is missing closed recovery contract %q", testCase.path, fragment)
				}
			}
			if testCase.name == "PostgresUp" && !strings.Contains(text, "backup_asset_recovery_attempt_mutation_arm_monotonic") {
				t.Fatalf("%s is missing the recovery attempt mutation-arm function", testCase.path)
			}
			if strings.Contains(text, "create table backup_asset_recovery_latch") ||
				strings.Contains(text, "create table backup_asset_recovery_delivery") {
				t.Fatalf("%s creates a forbidden thirteenth latch/delivery table", testCase.path)
			}
		})
	}

}

func TestBackupAssetMigration067PairedFiles(t *testing.T) {
	testCases := []struct {
		name string
		fs   interface {
			ReadFile(string) ([]byte, error)
		}
		path string
	}{
		{name: "SQLiteUp", fs: sqliteMigrationsFS, path: "migrations/sqlite/000067_backup_asset_processing.up.sql"},
		{name: "SQLiteDown", fs: sqliteMigrationsFS, path: "migrations/sqlite/000067_backup_asset_processing.down.sql"},
		{name: "PostgresUp", fs: postgresMigrationsFS, path: "migrations/postgres/000067_backup_asset_processing.up.sql"},
		{name: "PostgresDown", fs: postgresMigrationsFS, path: "migrations/postgres/000067_backup_asset_processing.down.sql"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			script, err := testCase.fs.ReadFile(testCase.path)
			if err != nil {
				t.Fatalf("read paired 000067 migration: %v", err)
			}
			text := string(script)
			for _, fragment := range []string{"backup_asset_processing_jobs", "backup_asset_derived_blobs", "backup_asset_updater_metadata", "derived_store"} {
				if !strings.Contains(text, fragment) {
					t.Fatalf("%s is missing required fragment %q", testCase.path, fragment)
				}
			}
		})
	}
}

func TestBackupAssetMigration068PairedFiles(t *testing.T) {
	tableFragments := []string{
		"backup_asset_export_jobs",
		"backup_asset_export_keys",
		"backup_asset_export_items",
		"backup_asset_export_attempts",
		"backup_asset_export_item_attempts",
		"backup_asset_export_source_leases",
		"backup_asset_export_artifacts",
		"backup_asset_export_idempotency",
		"backup_asset_export_quota_buckets",
		"backup_asset_export_reservations",
		"backup_asset_export_delivery_grants",
		"backup_asset_export_delivery_requests",
		"backup_asset_archive_member_requests",
	}
	testCases := []struct {
		name string
		fs   interface {
			ReadFile(string) ([]byte, error)
		}
		path string
	}{
		{name: "SQLiteUp", fs: sqliteMigrationsFS, path: "migrations/sqlite/000068_backup_asset_export.up.sql"},
		{name: "SQLiteDown", fs: sqliteMigrationsFS, path: "migrations/sqlite/000068_backup_asset_export.down.sql"},
		{name: "PostgresUp", fs: postgresMigrationsFS, path: "migrations/postgres/000068_backup_asset_export.up.sql"},
		{name: "PostgresDown", fs: postgresMigrationsFS, path: "migrations/postgres/000068_backup_asset_export.down.sql"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			script, err := testCase.fs.ReadFile(testCase.path)
			if err != nil {
				t.Fatalf("read paired 000068 migration: %v", err)
			}
			text := string(script)
			for _, fragment := range append(tableFragments, "export_store") {
				if !strings.Contains(text, fragment) {
					t.Fatalf("%s is missing required fragment %q", testCase.path, fragment)
				}
			}
			if strings.HasSuffix(testCase.path, ".up.sql") {
				const readyProductCheck = "CHECK (execution_state NOT IN ('ready', 'expiring', 'expired') OR (result_kind IN ('complete', 'partial') AND packed_count > 0))"
				if !strings.Contains(text, readyProductCheck) {
					t.Fatalf("%s is missing the closed ready-product CHECK", testCase.path)
				}
				if testCase.name == "SQLiteUp" && strings.Count(text, "id TEXT NOT NULL PRIMARY KEY CHECK") != len(tableFragments) {
					t.Fatalf("%s does not declare every Export primary key NOT NULL", testCase.path)
				}
			}
		})
	}
}

func TestBackupAssetSearchModelSensitiveFieldsEncryptAtRest(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_BACKUP_ASSET_SEARCH_MODEL_DATA_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()

	testCases := []struct {
		name       string
		beforeSave func() (string, error)
		afterFind  func(string) (string, error)
	}{
		{
			name: "SavedSearchAST",
			beforeSave: func() (string, error) {
				value := model.BackupAssetSavedSearch{EncryptedAST: `{"schema_version":1}`}
				err := value.BeforeSave(nil)
				return value.EncryptedAST, err
			},
			afterFind: func(ciphertext string) (string, error) {
				value := model.BackupAssetSavedSearch{EncryptedAST: ciphertext}
				err := value.AfterFind(nil)
				return value.EncryptedAST, err
			},
		},
		{
			name: "FavoriteLabel",
			beforeSave: func() (string, error) {
				value := model.BackupAssetFavorite{EncryptedLabel: "private label"}
				err := value.BeforeSave(nil)
				return value.EncryptedLabel, err
			},
			afterFind: func(ciphertext string) (string, error) {
				value := model.BackupAssetFavorite{EncryptedLabel: ciphertext}
				err := value.AfterFind(nil)
				return value.EncryptedLabel, err
			},
		},
		{
			name: "TagName",
			beforeSave: func() (string, error) {
				value := model.BackupAssetTagDefinition{EncryptedName: "private tag"}
				err := value.BeforeSave(nil)
				return value.EncryptedName, err
			},
			afterFind: func(ciphertext string) (string, error) {
				value := model.BackupAssetTagDefinition{EncryptedName: ciphertext}
				err := value.AfterFind(nil)
				return value.EncryptedName, err
			},
		},
		{
			name: "IdempotencyFingerprint",
			beforeSave: func() (string, error) {
				value := model.BackupAssetOverlayIdempotency{EncryptedRequestFingerprint: "request fingerprint"}
				err := value.BeforeSave(nil)
				return value.EncryptedRequestFingerprint, err
			},
			afterFind: func(ciphertext string) (string, error) {
				value := model.BackupAssetOverlayIdempotency{EncryptedRequestFingerprint: ciphertext}
				err := value.AfterFind(nil)
				return value.EncryptedRequestFingerprint, err
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			ciphertext, err := testCase.beforeSave()
			if err != nil {
				t.Fatalf("encrypt model field: %v", err)
			}
			if !strings.HasPrefix(ciphertext, "enc:") || strings.Contains(ciphertext, "private") || strings.Contains(ciphertext, "schema_version") {
				t.Fatalf("sensitive model field is not opaque ciphertext: %q", ciphertext)
			}
			plaintext, err := testCase.afterFind(ciphertext)
			if err != nil {
				t.Fatalf("decrypt model field: %v", err)
			}
			if plaintext == ciphertext || plaintext == "" {
				t.Fatalf("model hook did not restore plaintext: ciphertext=%q plaintext=%q", ciphertext, plaintext)
			}
		})
	}
}

type backupAssetMigration070SharedCheck struct {
	name string
	run  func(*testing.T, migrationFixture, *sql.DB)
}

func backupAssetMigration070SharedChecks() []backupAssetMigration070SharedCheck {
	return []backupAssetMigration070SharedCheck{
		{name: "PolicyRevisionSnapshotDoesNotPinCurrentPolicy", run: assertBackupAssetLifecyclePolicyRevisionSnapshot},
		{name: "TombstoneTerminalFacts", run: assertBackupAssetLifecycleTombstoneTerminalFacts},
		{name: "TombstoneCompositeHistory", run: assertBackupAssetLifecycleTombstoneCompositeHistory},
	}
}

func runBackupAssetMigration070Contract(t *testing.T, fixture migrationFixture) {
	t.Helper()
	t.Run("PointRevision", func(t *testing.T) {
		assertBackupAssetLifecyclePointRevision(t, fixture)
	})
	t.Run("ApplyAndModelParity", func(t *testing.T) {
		migrator, db := fixture.openAt(t, backupAssetLifecycleVersion)
		assertMigrationVersion(t, migrator, backupAssetLifecycleVersion)
		assertBackupAssetLifecycleSchema070(t, fixture, db)
		if fixture.engine == "sqlite" {
			assertSQLiteForeignKeyCheck(t, db)
		}
	})
	t.Run("ClosedConstraintsAndPermanentTombstone", func(t *testing.T) {
		_, db := fixture.openAt(t, backupAssetLifecycleVersion)
		seed := fixture.seedLifecycleMigrationBase(t, db, "a", 7001)
		fixture.assertLifecycleClosedConstraints(t, db, seed)
	})
	for _, check := range backupAssetMigration070SharedChecks() {
		t.Run(check.name, func(t *testing.T) {
			_, db := fixture.openAt(t, backupAssetLifecycleVersion)
			check.run(t, fixture, db)
		})
	}
	t.Run("PreservesExisting069FactsAndLeaseReferences", func(t *testing.T) {
		migrator, db := fixture.openAt(t, backupAssetRecoveryVersion)
		now := time.Date(2026, 8, 17, 7, 0, 0, 0, time.UTC)
		aggregate := fixture.seedRecoveryMigrationAggregate(t, db, "7", 70, now)
		migrateToBackupAssetVersion(t, migrator, backupAssetLifecycleVersion)
		assertMigrationVersion(t, migrator, backupAssetLifecycleVersion)
		for table, id := range map[string]string{
			"backup_asset_recovery_jobs":     aggregate.JobID,
			"backup_asset_recovery_evidence": aggregate.EvidenceID,
			"recovery_point_leases":          aggregate.SourceLeaseID,
		} {
			var count int
			if err := db.QueryRow(fixture.bind(`SELECT COUNT(*) FROM `+table+` WHERE id = ?`), id).Scan(&count); err != nil {
				t.Fatalf("count preserved %s row: %v", table, err)
			}
			if count != 1 {
				t.Fatalf("%s 000070 migration lost existing 000069 %s row", fixture.engine, table)
			}
		}
		if fixture.engine == "sqlite" {
			assertSQLiteForeignKeyCheck(t, db)
		}
	})
	t.Run("PristineDownRestores069", func(t *testing.T) {
		migrator, db := fixture.openAt(t, backupAssetRecoveryVersion)
		pointDefinitionBefore := fixture.tableDefinition(t, db, "recovery_points")
		leaseDefinitionBefore := fixture.tableDefinition(t, db, "recovery_point_leases")
		recoveryLeaseIndexBefore := fixture.indexDefinition(t, db, "idx_recovery_point_leases_recovery_job_owner")
		migrateToBackupAssetVersion(t, migrator, backupAssetLifecycleVersion)
		if err := migrator.Steps(-1); err != nil {
			t.Fatalf("step %s pristine 000070 down to 000069: %v", fixture.engine, err)
		}
		assertMigrationVersion(t, migrator, backupAssetRecoveryVersion)
		for _, table := range backupAssetLifecycleTables {
			if databaseTableExists(t, db, fixture.engine, table) {
				t.Fatalf("%s lifecycle table %s remains after pristine down", fixture.engine, table)
			}
		}
		if fixture.recoveryTriggerExists(t, db, "schema_migrations", "trg_backup_asset_lifecycle_downgrade_admission") {
			t.Fatal("lifecycle downgrade admission trigger remains after pristine down")
		}
		for _, trigger := range backupAssetPointRevisionTriggers(fixture.engine) {
			if fixture.recoveryTriggerExists(t, db, "recovery_points", trigger) {
				t.Fatalf("point revision trigger %s remains after pristine down", trigger)
			}
		}
		if fixture.engine == "postgres" && fixture.recoveryFunctionDefinition(t, db, "recovery_point_revision_advance") != "" {
			t.Fatal("point revision function remains after pristine down")
		}
		if got := fixture.tableDefinition(t, db, "recovery_points"); got != pointDefinitionBefore {
			t.Fatalf("%s pristine down did not restore the exact 000069 recovery point definition\n got: %s\nwant: %s", fixture.engine, got, pointDefinitionBefore)
		}
		var pointColumns []string
		if fixture.engine == "sqlite" {
			pointColumns = sqliteColumnNames(t, db, "recovery_points")
		} else {
			pointColumns = postgresColumnNames(t, db, "recovery_points")
		}
		if containsString(pointColumns, "point_revision") {
			t.Fatal("point_revision remains after pristine down")
		}
		if got := fixture.tableDefinition(t, db, "recovery_point_leases"); got != leaseDefinitionBefore {
			t.Fatalf("%s pristine down did not restore the exact 000069 lease definition\n got: %s\nwant: %s", fixture.engine, got, leaseDefinitionBefore)
		}
		if got := fixture.indexDefinition(t, db, "idx_recovery_point_leases_recovery_job_owner"); got != recoveryLeaseIndexBefore {
			t.Fatalf("%s pristine down did not restore the 000069 recovery lease index\n got: %s\nwant: %s", fixture.engine, got, recoveryLeaseIndexBefore)
		}
		if strings.Contains(fixture.tableDefinition(t, db, "recovery_point_leases"), "retention_worker") {
			t.Fatal("retention_worker remains in the restored 000069 lease definition")
		}
		if fixture.engine == "sqlite" {
			assertSQLiteForeignKeyCheck(t, db)
		}
	})
}

func assertBackupAssetLifecyclePolicyRevisionSnapshot(t *testing.T, fixture migrationFixture, db *sql.DB) {
	t.Helper()
	seed := fixture.seedLifecycleMigrationBase(t, db, "d", 7004)
	seedLifecycleRetentionPolicy(t, fixture, db, seed)
	policyID := recoveryMigrationOpaqueID(700001)
	attemptID := recoveryMigrationOpaqueID(702001)
	fixture.mustExec(t, db, `INSERT INTO recovery_point_lifecycle_attempts
		(id, recovery_point_id, operation, phase, transition_revision, policy_id, policy_revision,
		 policy_rule_digest, evaluation_time, blocked_reason, created_at, updated_at)
		VALUES (?, ?, 'retention_expire', 'selected', 1, ?, 1, ?, ?, '', ?, ?)`,
		attemptID, seed.PointID, policyID, recoveryMigrationDigest(702001), seed.Now, seed.Now, seed.Now)

	fixture.mustExec(t, db, `UPDATE backup_retention_policies
		SET revision = 2, rules_json = '{"version":1,"min_age_days":30}', updated_at = ?
		WHERE id = ? AND revision = 1`, seed.Now.Add(time.Minute), policyID)

	var currentRevision, snapshotRevision int64
	if err := db.QueryRow(fixture.bind(`SELECT revision FROM backup_retention_policies WHERE id = ?`), policyID).Scan(&currentRevision); err != nil {
		t.Fatalf("read current retention policy revision: %v", err)
	}
	if err := db.QueryRow(fixture.bind(`SELECT policy_revision FROM recovery_point_lifecycle_attempts WHERE id = ?`), attemptID).Scan(&snapshotRevision); err != nil {
		t.Fatalf("read lifecycle attempt policy snapshot: %v", err)
	}
	if currentRevision != 2 || snapshotRevision != 1 {
		t.Fatalf("policy/current snapshot revisions = %d/%d, want 2/1", currentRevision, snapshotRevision)
	}
}

func assertBackupAssetLifecycleTombstoneTerminalFacts(t *testing.T, fixture migrationFixture, db *sql.DB) {
	t.Helper()
	seed := fixture.seedLifecycleMigrationBase(t, db, "e", 7005)
	semantics := []string{"native_snapshot", "xirang_manifest", "imported_baseline", "mutable_head"}
	operations := []string{"retention_expire", "explicit_purge", "mutable_retire"}
	states := []string{"retired", "expired"}
	times := []any{nil, seed.Now}
	digests := []any{nil, recoveryMigrationDigest(703001)}
	resultCodes := []string{"mutable_retired", "provider_deleted", "provider_already_absent"}

	for _, originalSemantics := range semantics {
		for _, operation := range operations {
			for _, state := range states {
				for _, retiredAt := range times {
					for _, purgedAt := range times {
						for _, digest := range digests {
							for _, resultCode := range resultCodes {
								validMutableRetire := operation == "mutable_retire" && originalSemantics == "mutable_head" &&
									state == "retired" && retiredAt != nil && purgedAt == nil && digest == nil && resultCode == "mutable_retired"
								validProviderDelete := (operation == "retention_expire" || operation == "explicit_purge") &&
									state == "expired" && retiredAt == nil && purgedAt != nil && digest != nil &&
									(resultCode == "provider_deleted" || resultCode == "provider_already_absent")
								if validMutableRetire || validProviderDelete {
									continue
								}
								fixture.expectExecRejected(t, db, `INSERT INTO recovery_point_lifecycle_tombstones
									(recovery_point_id, repository_id, original_semantics, terminal_operation, terminal_state,
									 managed_history, deletion_receipt_digest, retired_at, purged_at, result_code, created_at)
									VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
									seed.PointID, seed.RepositoryID, originalSemantics, operation, state, true, digest,
									retiredAt, purgedAt, resultCode, seed.Now)
							}
						}
					}
				}
			}
		}
	}
}

func assertBackupAssetLifecycleTombstoneCompositeHistory(t *testing.T, fixture migrationFixture, db *sql.DB) {
	t.Helper()
	wantPrimaryKey := []string{"recovery_point_id", "terminal_operation"}
	t.Run("ModelPrimaryKeyOrder", func(t *testing.T) {
		parsed, err := schema.Parse(model.RecoveryPointLifecycleTombstone{}, &sync.Map{}, schema.NamingStrategy{})
		if err != nil {
			t.Fatalf("parse tombstone model: %v", err)
		}
		got := make([]string, 0, len(parsed.PrimaryFields))
		for _, field := range parsed.PrimaryFields {
			got = append(got, field.DBName)
		}
		if strings.Join(got, ",") != strings.Join(wantPrimaryKey, ",") {
			t.Fatalf("tombstone model primary key = %v, want %v", got, wantPrimaryKey)
		}
	})
	t.Run("DatabasePrimaryKeyOrder", func(t *testing.T) {
		var rows *sql.Rows
		var err error
		if fixture.engine == "sqlite" {
			rows, err = db.Query(`PRAGMA table_info(recovery_point_lifecycle_tombstones)`)
		} else {
			rows, err = db.Query(`SELECT key_column_usage.column_name
				FROM information_schema.table_constraints AS table_constraint
				JOIN information_schema.key_column_usage AS key_column_usage
				  ON key_column_usage.constraint_name = table_constraint.constraint_name
				 AND key_column_usage.constraint_schema = table_constraint.constraint_schema
				WHERE table_constraint.table_schema = current_schema()
				  AND table_constraint.table_name = 'recovery_point_lifecycle_tombstones'
				  AND table_constraint.constraint_type = 'PRIMARY KEY'
				ORDER BY key_column_usage.ordinal_position`)
		}
		if err != nil {
			t.Fatalf("query %s tombstone primary key: %v", fixture.engine, err)
		}
		defer func() {
			if err := rows.Close(); err != nil {
				t.Errorf("close %s tombstone primary key rows: %v", fixture.engine, err)
			}
		}()
		got := make([]string, 0, len(wantPrimaryKey))
		if fixture.engine == "sqlite" {
			type primaryColumn struct {
				name    string
				ordinal int
			}
			columns := make([]primaryColumn, 0, len(wantPrimaryKey))
			for rows.Next() {
				var cid, notNull, primaryKey int
				var name, columnType string
				var defaultValue any
				if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
					t.Fatalf("scan SQLite tombstone primary key: %v", err)
				}
				if primaryKey > 0 {
					columns = append(columns, primaryColumn{name: name, ordinal: primaryKey})
				}
			}
			sort.Slice(columns, func(i, j int) bool { return columns[i].ordinal < columns[j].ordinal })
			for _, column := range columns {
				got = append(got, column.name)
			}
		} else {
			for rows.Next() {
				var name string
				if err := rows.Scan(&name); err != nil {
					t.Fatalf("scan PostgreSQL tombstone primary key: %v", err)
				}
				got = append(got, name)
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("iterate %s tombstone primary key: %v", fixture.engine, err)
		}
		if strings.Join(got, ",") != strings.Join(wantPrimaryKey, ",") {
			t.Fatalf("%s tombstone primary key = %v, want %v", fixture.engine, got, wantPrimaryKey)
		}
	})
	t.Run("MutableRetireThenExplicitPurge", func(t *testing.T) {
		seed := fixture.seedLifecycleMigrationBase(t, db, "6", 7006)
		fixture.mustExec(t, db, `INSERT INTO recovery_point_lifecycle_tombstones
			(recovery_point_id, repository_id, original_semantics, terminal_operation, terminal_state,
			 managed_history, retired_at, result_code, created_at)
			VALUES (?, ?, 'mutable_head', 'mutable_retire', 'retired', ?, ?, 'mutable_retired', ?)`,
			seed.PointID, seed.RepositoryID, true, seed.Now, seed.Now)
		fixture.mustExec(t, db, `INSERT INTO recovery_point_lifecycle_tombstones
			(recovery_point_id, repository_id, original_semantics, terminal_operation, terminal_state,
			 managed_history, deletion_receipt_digest, purged_at, result_code, created_at)
			VALUES (?, ?, 'mutable_head', 'explicit_purge', 'expired', ?, ?, ?, 'provider_deleted', ?)`,
			seed.PointID, seed.RepositoryID, true, recoveryMigrationDigest(704001), seed.Now.Add(time.Minute), seed.Now.Add(time.Minute))
		var count int
		if err := db.QueryRow(fixture.bind(`SELECT COUNT(*) FROM recovery_point_lifecycle_tombstones WHERE recovery_point_id = ?`), seed.PointID).Scan(&count); err != nil {
			t.Fatalf("count tombstone history: %v", err)
		}
		if count != 2 {
			t.Fatalf("%s tombstone history rows = %d, want 2", fixture.engine, count)
		}
		fixture.expectExecRejected(t, db, `INSERT INTO recovery_point_lifecycle_tombstones
			(recovery_point_id, repository_id, original_semantics, terminal_operation, terminal_state,
			 managed_history, retired_at, result_code, created_at)
			VALUES (?, ?, 'mutable_head', 'mutable_retire', 'retired', ?, ?, 'mutable_retired', ?)`,
			seed.PointID, seed.RepositoryID, true, seed.Now.Add(2*time.Minute), seed.Now.Add(2*time.Minute))
	})
}

func runBackupAssetMigration070UsedDownAdmission(t *testing.T, fixture migrationFixture) {
	t.Helper()
	testCases := []struct {
		name  string
		table string
		seed  func(*testing.T, migrationFixture, *sql.DB, lifecycleMigrationSeed)
	}{
		{name: "RetentionPolicy", table: "backup_retention_policies", seed: seedLifecycleRetentionPolicy},
		{name: "RecoveryPointHold", table: "recovery_point_holds", seed: seedLifecycleHold},
		{name: "LifecycleAttempt", table: "recovery_point_lifecycle_attempts", seed: seedLifecycleAttempt},
		{name: "PermanentTombstone", table: "recovery_point_lifecycle_tombstones", seed: seedLifecycleTombstone},
		{name: "ImportCandidate", table: "backup_repository_import_candidates", seed: seedLifecycleImportCandidate},
		{name: "PurgePlan", table: "backup_asset_purge_plans", seed: seedLifecyclePurgePlan},
		{name: "PurgePlanItem", table: "backup_asset_purge_plan_items", seed: seedLifecyclePurgePlanItem},
		{name: "ConfigImportRef", table: "backup_asset_config_import_refs", seed: seedLifecycleConfigImportRef},
		{name: "RetentionWorkerLease", table: "recovery_point_leases", seed: seedLifecycleRetentionWorkerLease},
	}
	for index, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			migrator, db := fixture.openAt(t, backupAssetLifecycleVersion)
			seed := fixture.seedLifecycleMigrationBase(t, db, fmt.Sprintf("%x", index+1), int64(7100+index))
			testCase.seed(t, fixture, db, seed)

			leaseDefinitionBefore := fixture.tableDefinition(t, db, "recovery_point_leases")
			activeAttemptIndexBefore := fixture.indexDefinition(t, db, "idx_recovery_point_lifecycle_attempts_active")
			admissionBefore := fixture.recoveryTriggerDefinition(t, db, "schema_migrations", "trg_backup_asset_lifecycle_downgrade_admission")
			var rowsBefore int
			if err := db.QueryRow(`SELECT COUNT(*) FROM ` + testCase.table).Scan(&rowsBefore); err != nil {
				t.Fatalf("count %s before rejected down: %v", testCase.table, err)
			}
			if rowsBefore == 0 {
				t.Fatalf("%s seed did not create a used lifecycle fact", testCase.name)
			}

			if err := migrator.Steps(-1); err == nil {
				t.Fatalf("%s 000070 down unexpectedly succeeded with %s state", fixture.engine, testCase.name)
			}
			assertMigrationVersion(t, migrator, backupAssetLifecycleVersion)
			var rowsAfter int
			if err := db.QueryRow(`SELECT COUNT(*) FROM ` + testCase.table).Scan(&rowsAfter); err != nil {
				t.Fatalf("count %s after rejected down: %v", testCase.table, err)
			}
			if rowsAfter != rowsBefore {
				t.Fatalf("rejected down changed %s rows: before=%d after=%d", testCase.table, rowsBefore, rowsAfter)
			}
			if got := fixture.tableDefinition(t, db, "recovery_point_leases"); got != leaseDefinitionBefore {
				t.Fatalf("rejected down changed lease definition\n got: %s\nwant: %s", got, leaseDefinitionBefore)
			}
			if got := fixture.indexDefinition(t, db, "idx_recovery_point_lifecycle_attempts_active"); got != activeAttemptIndexBefore {
				t.Fatalf("rejected down changed active-attempt index\n got: %s\nwant: %s", got, activeAttemptIndexBefore)
			}
			if got := fixture.recoveryTriggerDefinition(t, db, "schema_migrations", "trg_backup_asset_lifecycle_downgrade_admission"); got != admissionBefore {
				t.Fatalf("rejected down changed lifecycle admission trigger\n got: %s\nwant: %s", got, admissionBefore)
			}
		})
	}
	t.Run("PointRevision", func(t *testing.T) {
		migrator, db := fixture.openAt(t, backupAssetLifecycleVersion)
		seed := fixture.seedLifecycleMigrationBase(t, db, "f", 7199)
		fixture.mustExec(t, db, `UPDATE recovery_points SET state = state WHERE id = ?`, seed.PointID)

		var revision int64
		if err := db.QueryRow(fixture.bind(`SELECT point_revision FROM recovery_points WHERE id = ?`), seed.PointID).Scan(&revision); err != nil {
			t.Fatalf("read used point revision: %v", err)
		}
		if revision != 2 {
			t.Fatalf("used point revision=%d, want 2", revision)
		}
		pointDefinitionBefore := fixture.tableDefinition(t, db, "recovery_points")
		triggerDefinitionsBefore := make(map[string]string)
		for _, trigger := range backupAssetPointRevisionTriggers(fixture.engine) {
			triggerDefinitionsBefore[trigger] = fixture.recoveryTriggerDefinition(t, db, "recovery_points", trigger)
		}

		if err := migrator.Steps(-1); err == nil {
			t.Fatalf("%s 000070 down unexpectedly succeeded with advanced point revision", fixture.engine)
		}
		assertMigrationVersion(t, migrator, backupAssetLifecycleVersion)
		if got := fixture.tableDefinition(t, db, "recovery_points"); got != pointDefinitionBefore {
			t.Fatalf("rejected down changed recovery point definition\n got: %s\nwant: %s", got, pointDefinitionBefore)
		}
		for trigger, before := range triggerDefinitionsBefore {
			if got := fixture.recoveryTriggerDefinition(t, db, "recovery_points", trigger); got != before {
				t.Fatalf("rejected down changed point revision trigger %s\n got: %s\nwant: %s", trigger, got, before)
			}
		}
		var after int64
		if err := db.QueryRow(fixture.bind(`SELECT point_revision FROM recovery_points WHERE id = ?`), seed.PointID).Scan(&after); err != nil {
			t.Fatalf("read point revision after rejected down: %v", err)
		}
		if after != revision {
			t.Fatalf("rejected down changed point revision: before=%d after=%d", revision, after)
		}
	})
}

func assertBackupAssetLifecyclePointRevision(t *testing.T, fixture migrationFixture) {
	t.Helper()
	migrator, db := fixture.openAt(t, backupAssetRecoveryVersion)
	seed := fixture.seedLifecycleMigrationBase(t, db, "f", 7006)
	migrateToBackupAssetVersion(t, migrator, backupAssetLifecycleVersion)

	var columns []string
	if fixture.engine == "sqlite" {
		columns = sqliteColumnNames(t, db, "recovery_points")
	} else {
		columns = postgresColumnNames(t, db, "recovery_points")
	}
	if !containsString(columns, "point_revision") {
		t.Fatalf("%s 000070 recovery_points lacks point_revision", fixture.engine)
	}
	if !containsString(gormColumnNames(t, model.RecoveryPoint{}), "point_revision") {
		t.Fatal("RecoveryPoint model lacks point_revision")
	}
	wantColumns := gormColumnNames(t, model.RecoveryPoint{})
	sort.Strings(columns)
	sort.Strings(wantColumns)
	if !reflect.DeepEqual(columns, wantColumns) {
		t.Fatalf("%s recovery_points model columns mismatch\n got: %v\nwant: %v", fixture.engine, columns, wantColumns)
	}
	assertPointRevisionColumnContract(t, fixture, db)
	for _, trigger := range backupAssetPointRevisionTriggers(fixture.engine) {
		if !fixture.recoveryTriggerExists(t, db, "recovery_points", trigger) {
			t.Fatalf("%s point revision trigger %s is missing", fixture.engine, trigger)
		}
	}
	if fixture.engine == "postgres" && fixture.recoveryFunctionDefinition(t, db, "recovery_point_revision_advance") == "" {
		t.Fatal("PostgreSQL point revision function is missing")
	}

	readRevisions := func() (int64, int) {
		t.Helper()
		var pointRevision int64
		var capabilityRevision int
		if err := db.QueryRow(fixture.bind(`SELECT point_revision, capability_revision FROM recovery_points WHERE id = ?`), seed.PointID).
			Scan(&pointRevision, &capabilityRevision); err != nil {
			t.Fatalf("read recovery point revisions: %v", err)
		}
		return pointRevision, capabilityRevision
	}
	assertRevisions := func(wantPoint int64, wantCapability int) {
		t.Helper()
		gotPoint, gotCapability := readRevisions()
		if gotPoint != wantPoint || gotCapability != wantCapability {
			t.Fatalf("point/capability revisions=%d/%d, want %d/%d", gotPoint, gotCapability, wantPoint, wantCapability)
		}
	}

	assertRevisions(1, 1)
	fixture.mustExec(t, db, `UPDATE recovery_points SET state = state WHERE id = ?`, seed.PointID)
	assertRevisions(2, 1)
	fixture.mustExec(t, db, `UPDATE recovery_points SET point_revision = ? WHERE id = ?`, 3, seed.PointID)
	assertRevisions(3, 1)
	fixture.expectExecRejected(t, db, `UPDATE recovery_points SET point_revision = ? WHERE id = ?`, 5, seed.PointID)
	fixture.expectExecRejected(t, db, `UPDATE recovery_points SET point_revision = ? WHERE id = ?`, 2, seed.PointID)
	fixture.expectExecRejected(t, db, `UPDATE recovery_points SET point_revision = ? WHERE id = ?`, 0, seed.PointID)
	assertRevisions(3, 1)
	fixture.mustExec(t, db, `UPDATE recovery_points SET capability_revision = capability_revision + 1 WHERE id = ?`, seed.PointID)
	assertRevisions(4, 2)
}

func assertPointRevisionColumnContract(t *testing.T, fixture migrationFixture, db *sql.DB) {
	t.Helper()
	if fixture.engine == "sqlite" {
		var columnType string
		var notNull int
		var defaultValue sql.NullString
		if err := db.QueryRow(`SELECT type, "notnull", dflt_value FROM pragma_table_info('recovery_points') WHERE name = 'point_revision'`).
			Scan(&columnType, &notNull, &defaultValue); err != nil {
			t.Fatalf("load SQLite point revision column contract: %v", err)
		}
		if !strings.EqualFold(columnType, "INTEGER") || notNull != 1 || !defaultValue.Valid || defaultValue.String != "1" {
			t.Fatalf("SQLite point revision column=%q notnull=%d default=%q, want INTEGER/1/1", columnType, notNull, defaultValue.String)
		}
		return
	}
	var dataType, nullable, defaultValue string
	if err := db.QueryRow(`SELECT data_type, is_nullable, COALESCE(column_default, '')
		FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'recovery_points' AND column_name = 'point_revision'`).
		Scan(&dataType, &nullable, &defaultValue); err != nil {
		t.Fatalf("load PostgreSQL point revision column contract: %v", err)
	}
	if dataType != "bigint" || nullable != "NO" || !strings.HasPrefix(defaultValue, "1") {
		t.Fatalf("PostgreSQL point revision column=%q nullable=%q default=%q, want bigint/NO/1", dataType, nullable, defaultValue)
	}
}

func backupAssetPointRevisionTriggers(engine string) []string {
	if engine == "sqlite" {
		return []string{"trg_recovery_points_point_revision_guard", "trg_recovery_points_point_revision_advance"}
	}
	return []string{"trg_recovery_points_point_revision"}
}

func backupAssetLifecycleModels() map[string]any {
	return map[string]any{
		"backup_retention_policies":           model.BackupRetentionPolicy{},
		"recovery_point_holds":                model.RecoveryPointHold{},
		"recovery_point_lifecycle_attempts":   model.RecoveryPointLifecycleAttempt{},
		"recovery_point_lifecycle_tombstones": model.RecoveryPointLifecycleTombstone{},
		"backup_repository_import_candidates": model.BackupRepositoryImportCandidate{},
		"backup_asset_purge_plans":            model.BackupAssetPurgePlan{},
		"backup_asset_purge_plan_items":       model.BackupAssetPurgePlanItem{},
		"backup_asset_config_import_refs":     model.BackupAssetConfigImportRef{},
	}
}

func assertBackupAssetLifecycleSchema070(t *testing.T, fixture migrationFixture, db *sql.DB) {
	t.Helper()
	for table, persistentModel := range backupAssetLifecycleModels() {
		if !databaseTableExists(t, db, fixture.engine, table) {
			t.Fatalf("%s lifecycle migration table %s is missing", fixture.engine, table)
		}
		want := gormColumnNames(t, persistentModel)
		var got []string
		if fixture.engine == "sqlite" {
			got = sqliteColumnNames(t, db, table)
			assertSQLiteTimeColumnsHaveNoDefault(t, db, table)
		} else {
			got = postgresColumnNames(t, db, table)
			assertPostgresTableTimeColumnsHaveNoDefault(t, db, table)
		}
		sort.Strings(got)
		sort.Strings(want)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("%s %s columns mismatch\n got: %v\nwant: %v", fixture.engine, table, got, want)
		}
	}
	for index, fragments := range map[string][]string{
		"idx_backup_retention_policies_active_scope":     {"unique", "scope_kind", "scope_id", "where", "active"},
		"idx_recovery_point_holds_active_type":           {"unique", "recovery_point_id", "hold_type", "where", "active"},
		"idx_recovery_point_lifecycle_attempts_active":   {"unique", "recovery_point_id", "where", "complete"},
		"idx_backup_repository_import_candidates_source": {"unique", "repository_id", "source_fingerprint"},
		"idx_backup_asset_purge_plan_items_plan_ordinal": {"unique", "plan_id", "ordinal"},
		"idx_backup_asset_config_import_refs_source":     {"unique", "source_document_id", "source_reference", "entity_kind"},
		"idx_backup_asset_config_import_refs_local":      {"unique", "source_document_id", "entity_kind", "local_entity_id"},
	} {
		definition := fixture.indexDefinition(t, db, index)
		if definition == "" {
			t.Fatalf("%s lifecycle index %s is missing", fixture.engine, index)
		}
		for _, fragment := range fragments {
			if !strings.Contains(definition, fragment) {
				t.Fatalf("%s lifecycle index %s omits %q: %s", fixture.engine, index, fragment, definition)
			}
		}
	}
	for table, fragments := range map[string][]string{
		"backup_retention_policies":           {"repository", "task_link", "active", "deleted"},
		"recovery_point_holds":                {"operational", "legal", "released"},
		"recovery_point_lifecycle_attempts":   {"retention_expire", "explicit_purge", "mutable_retire", "provider_delete", "complete"},
		"recovery_point_lifecycle_tombstones": {"managed_history", "provider_deleted", "provider_already_absent"},
		"backup_repository_import_candidates": {"native_snapshot", "xirang_manifest", "pending", "accepted", "rejected"},
		"backup_asset_purge_plans":            {"ready", "bound", "executing", "consumed", "invalidated"},
		"backup_asset_config_import_refs":     {"repository", "task_link", "retention_policy", "hold"},
		"recovery_point_leases":               {"retention_worker"},
	} {
		definition := fixture.tableDefinition(t, db, table)
		for _, fragment := range fragments {
			if !strings.Contains(definition, fragment) {
				t.Fatalf("%s %s definition omits %q: %s", fixture.engine, table, fragment, definition)
			}
		}
	}
	for _, trigger := range []struct{ table, name string }{
		{table: "schema_migrations", name: "trg_backup_asset_lifecycle_downgrade_admission"},
		{table: "recovery_point_holds", name: "trg_recovery_point_holds_release_one_way"},
		{table: "recovery_point_lifecycle_tombstones", name: "trg_recovery_point_lifecycle_tombstones_immutable_update"},
		{table: "recovery_point_lifecycle_tombstones", name: "trg_recovery_point_lifecycle_tombstones_immutable_delete"},
	} {
		if !fixture.recoveryTriggerExists(t, db, trigger.table, trigger.name) {
			t.Fatalf("%s lifecycle trigger %s is missing", fixture.engine, trigger.name)
		}
	}
}

type lifecycleMigrationSeed struct {
	Now          time.Time
	UserID       int64
	RepositoryID string
	PointID      string
}

func (fixture migrationFixture) seedLifecycleMigrationBase(t *testing.T, db *sql.DB, marker string, userID int64) lifecycleMigrationSeed {
	t.Helper()
	now := time.Date(2026, 8, 17, 5, 0, 0, 0, time.UTC).Add(time.Duration(userID) * time.Second)
	repositoryID := strings.Repeat(marker, 32)
	pointID := strings.Repeat(fmt.Sprintf("%x", (userID%14)+1), 32)
	fixture.insertSearchMigrationUser(t, db, userID, fmt.Sprintf("lifecycle-user-%d", userID), now)
	fixture.insertRepository(t, db, repositoryID, "restic", now)
	fixture.mustInsertRecoveryPoint(t, db, publicationPointSeed{
		ID: pointID, RepositoryID: repositoryID, Semantics: "native_snapshot", State: "committed",
		SourceFingerprint: "lifecycle-source-" + marker,
	})
	return lifecycleMigrationSeed{Now: now, UserID: userID, RepositoryID: repositoryID, PointID: pointID}
}

func seedLifecycleRetentionPolicy(t *testing.T, fixture migrationFixture, db *sql.DB, seed lifecycleMigrationSeed) {
	t.Helper()
	fixture.mustExec(t, db, `INSERT INTO backup_retention_policies
		(id, scope_kind, scope_id, revision, rules_json, status, created_by, updated_by, created_at, updated_at)
		VALUES (?, 'repository', ?, 1, '{"version":1}', 'active', ?, ?, ?, ?)`,
		recoveryMigrationOpaqueID(700001), seed.RepositoryID, seed.UserID, seed.UserID, seed.Now, seed.Now)
}

func seedLifecycleHold(t *testing.T, fixture migrationFixture, db *sql.DB, seed lifecycleMigrationSeed) {
	t.Helper()
	fixture.mustExec(t, db, `INSERT INTO recovery_point_holds
		(id, recovery_point_id, hold_type, state, encrypted_reason, created_by, expires_at, encrypted_release_reason, created_at, updated_at)
		VALUES (?, ?, 'operational', 'active', 'enc:v2:fixture', ?, ?, '', ?, ?)`,
		recoveryMigrationOpaqueID(700002), seed.PointID, seed.UserID, seed.Now.Add(time.Hour), seed.Now, seed.Now)
}

func seedLifecycleAttempt(t *testing.T, fixture migrationFixture, db *sql.DB, seed lifecycleMigrationSeed) {
	t.Helper()
	fixture.mustExec(t, db, `INSERT INTO recovery_point_lifecycle_attempts
		(id, recovery_point_id, operation, phase, transition_revision, blocked_reason, created_at, updated_at)
		VALUES (?, ?, 'retention_expire', 'selected', 1, '', ?, ?)`,
		recoveryMigrationOpaqueID(700003), seed.PointID, seed.Now, seed.Now)
}

func seedLifecycleTombstone(t *testing.T, fixture migrationFixture, db *sql.DB, seed lifecycleMigrationSeed) {
	t.Helper()
	fixture.mustExec(t, db, `INSERT INTO recovery_point_lifecycle_tombstones
		(recovery_point_id, repository_id, original_semantics, terminal_operation, terminal_state,
		 managed_history, deletion_receipt_digest, purged_at, result_code, created_at)
		VALUES (?, ?, 'native_snapshot', 'explicit_purge', 'expired', ?, ?, ?, 'provider_deleted', ?)`,
		seed.PointID, seed.RepositoryID, true, recoveryMigrationDigest(700004), seed.Now, seed.Now)
}

func seedLifecycleImportCandidate(t *testing.T, fixture migrationFixture, db *sql.DB, seed lifecycleMigrationSeed) {
	t.Helper()
	fixture.mustExec(t, db, `INSERT INTO backup_repository_import_candidates
		(id, repository_id, candidate_kind, source_fingerprint, encrypted_provider_locator,
		 encrypted_evidence, review_state, created_at, updated_at)
		VALUES (?, ?, 'native_snapshot', ?, 'enc:v2:locator', 'enc:v2:evidence', 'pending', ?, ?)`,
		recoveryMigrationOpaqueID(700005), seed.RepositoryID, recoveryMigrationDigest(700005), seed.Now, seed.Now)
}

func seedLifecyclePurgePlan(t *testing.T, fixture migrationFixture, db *sql.DB, seed lifecycleMigrationSeed) {
	t.Helper()
	fixture.mustExec(t, db, `INSERT INTO backup_asset_purge_plans
		(id, repository_id, requester_id, revision, impact_revision, expires_at, hold_count, lease_count,
		 worm_count, status, execute_proof_digest, execute_reason_digest, created_at, updated_at)
		VALUES (?, ?, ?, 1, 1, ?, 0, 0, 0, 'ready', '', '', ?, ?)`,
		recoveryMigrationOpaqueID(700006), seed.RepositoryID, seed.UserID, seed.Now.Add(time.Hour), seed.Now, seed.Now)
}

func seedLifecyclePurgePlanItem(t *testing.T, fixture migrationFixture, db *sql.DB, seed lifecycleMigrationSeed) {
	t.Helper()
	seedLifecyclePurgePlan(t, fixture, db, seed)
	fixture.mustExec(t, db, `INSERT INTO backup_asset_purge_plan_items
		(id, plan_id, ordinal, recovery_point_id, expected_point_revision, expected_capability_revision, created_at)
		VALUES (?, ?, 1, ?, 1, 1, ?)`,
		recoveryMigrationOpaqueID(700007), recoveryMigrationOpaqueID(700006), seed.PointID, seed.Now)
}

func seedLifecycleConfigImportRef(t *testing.T, fixture migrationFixture, db *sql.DB, seed lifecycleMigrationSeed) {
	t.Helper()
	fixture.mustExec(t, db, `INSERT INTO backup_asset_config_import_refs
		(id, source_document_id, source_reference, entity_kind, local_entity_id, created_at)
		VALUES (?, ?, 'repository:primary', 'repository', ?, ?)`,
		recoveryMigrationOpaqueID(700008), recoveryMigrationOpaqueID(700009), seed.RepositoryID, seed.Now)
}

func seedLifecycleRetentionWorkerLease(t *testing.T, fixture migrationFixture, db *sql.DB, seed lifecycleMigrationSeed) {
	t.Helper()
	fixture.insertPublicationLease(t, db, recoveryMigrationOpaqueID(700010), seed.PointID, "retention_worker", "released", seed.Now)
}

func (fixture migrationFixture) assertLifecycleClosedConstraints(t *testing.T, db *sql.DB, seed lifecycleMigrationSeed) {
	t.Helper()
	seedLifecycleRetentionPolicy(t, fixture, db, seed)
	fixture.expectExecRejected(t, db, `INSERT INTO backup_retention_policies
		(id, scope_kind, scope_id, revision, rules_json, status, created_by, updated_by, created_at, updated_at)
		VALUES (?, 'repository', ?, 1, '{}', 'active', ?, ?, ?, ?)`,
		recoveryMigrationOpaqueID(701001), seed.RepositoryID, seed.UserID, seed.UserID, seed.Now, seed.Now)
	fixture.expectExecRejected(t, db, `INSERT INTO recovery_point_holds
		(id, recovery_point_id, hold_type, state, encrypted_reason, created_by, encrypted_release_reason, created_at, updated_at)
		VALUES (?, ?, 'future_hold', 'active', 'enc:v2:reason', ?, '', ?, ?)`,
		recoveryMigrationOpaqueID(701002), seed.PointID, seed.UserID, seed.Now, seed.Now)
	seedLifecycleHold(t, fixture, db, seed)
	fixture.mustExec(t, db, `UPDATE recovery_point_holds
		SET state = 'released', released_by = ?, released_at = ?, encrypted_release_reason = 'enc:v2:release', updated_at = ?
		WHERE id = ?`, seed.UserID, seed.Now.Add(time.Minute), seed.Now.Add(time.Minute), recoveryMigrationOpaqueID(700002))
	fixture.expectExecRejected(t, db, `UPDATE recovery_point_holds SET state = 'active', released_by = NULL,
		released_at = NULL, encrypted_release_reason = '', updated_at = ? WHERE id = ?`,
		seed.Now.Add(2*time.Minute), recoveryMigrationOpaqueID(700002))

	seedLifecycleTombstone(t, fixture, db, seed)
	fixture.expectExecRejected(t, db, `UPDATE recovery_point_lifecycle_tombstones SET managed_history = ? WHERE recovery_point_id = ?`, false, seed.PointID)
	fixture.expectExecRejected(t, db, `DELETE FROM recovery_point_lifecycle_tombstones WHERE recovery_point_id = ?`, seed.PointID)
	fixture.expectExecRejected(t, db, `INSERT INTO recovery_point_lifecycle_attempts
		(id, recovery_point_id, operation, phase, transition_revision, lease_id, lease_attempt_id,
		 lease_fence_token_hash, blocked_reason, created_at, updated_at)
		VALUES (?, ?, 'retention_expire', 'selected', 1, ?, NULL, NULL, '', ?, ?)`,
		recoveryMigrationOpaqueID(701003), seed.PointID, recoveryMigrationOpaqueID(701004), seed.Now, seed.Now)
	fixture.expectExecRejected(t, db, `INSERT INTO backup_repository_import_candidates
		(id, repository_id, candidate_kind, source_fingerprint, encrypted_provider_locator,
		 encrypted_evidence, review_state, reviewed_by, reviewed_at, accepted_recovery_point_id, created_at, updated_at)
		VALUES (?, ?, 'native_snapshot', ?, 'enc:v2:locator', 'enc:v2:evidence', 'accepted', ?, ?, NULL, ?, ?)`,
		recoveryMigrationOpaqueID(701005), seed.RepositoryID, recoveryMigrationDigest(701005), seed.UserID, seed.Now, seed.Now, seed.Now)
}

type migrationFixture struct {
	engine string
	open   func(*testing.T) (*migrate.Migrate, *sql.DB)
}

func newSQLiteMigrationFixture(t *testing.T) migrationFixture {
	t.Helper()
	return migrationFixture{engine: "sqlite", open: newSQLiteBackupAssetMigrator}
}

func newRequiredPostgresMigrationFixture(t *testing.T) migrationFixture {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		if strings.TrimSpace(os.Getenv("REQUIRE_POSTGRES_MIGRATION_TEST")) == "1" {
			t.Fatal("TEST_POSTGRES_DSN is required when REQUIRE_POSTGRES_MIGRATION_TEST=1")
		}
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	return migrationFixture{
		engine: "postgres",
		open: func(test *testing.T) (*migrate.Migrate, *sql.DB) {
			return newPostgresBackupAssetMigrator(test, dsn)
		},
	}
}

func newRequiredPostgresRecoveryMigrationFixture(t *testing.T) migrationFixture {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		if strings.TrimSpace(os.Getenv("REQUIRE_POSTGRES_RECOVERY_TEST")) == "1" {
			t.Fatal("TEST_POSTGRES_DSN is required when REQUIRE_POSTGRES_RECOVERY_TEST=1")
		}
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	return migrationFixture{
		engine: "postgres",
		open: func(test *testing.T) (*migrate.Migrate, *sql.DB) {
			return newPostgresBackupAssetMigrator(test, dsn)
		},
	}
}

func runBackupAssetMigration063Contract(t *testing.T, fixture migrationFixture) {
	t.Helper()
	t.Run("ApplyDown", fixture.testApplyDown)
	t.Run("ConvertsOnlyResticLinks", fixture.testConvertsOnlyResticLinks)
	t.Run("UniqueProducingRunAcrossSemantics", fixture.testUniqueProducingRunAcrossSemantics)
	t.Run("UniqueNativeSourcePerRepository", fixture.testUniqueNativeSourcePerRepository)
	t.Run("DownRejectsActivePublicationLease", fixture.testDownRejectsActivePublicationLease)
	t.Run("DownRejectsEveryNativePointStateAndNullableLineage", fixture.testDownRejectsEveryNativePointStateAndNullableLineage)
	t.Run("RejectedDownLeavesVersionSchemaAndDataUnchanged", fixture.testRejectedDownLeavesVersionSchemaAndDataUnchanged)
	t.Run("UTCAndModelParity", fixture.testUTCAndModelParity)
}

func TestBackupAssetMigration068DeliveryAuditReceiptStatesArePaired(t *testing.T) {
	testCases := []struct {
		name string
		fs   interface {
			ReadFile(string) ([]byte, error)
		}
		path string
	}{
		{name: "SQLite", fs: sqliteMigrationsFS, path: "migrations/sqlite/000068_backup_asset_export.up.sql"},
		{name: "Postgres", fs: postgresMigrationsFS, path: "migrations/postgres/000068_backup_asset_export.up.sql"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			script, err := testCase.fs.ReadFile(testCase.path)
			if err != nil {
				t.Fatal(err)
			}
			text := string(script)
			for _, fragment := range []string{
				"range_requested",
				"audit_state IN ('none', 'pending', 'emitted', 'retry_wait', 'failed')",
				"audit_state IN ('retry_wait', 'failed') AND audit_failure_code IN ('audit_write_failed', 'reconciliation_failed')",
			} {
				if !strings.Contains(text, fragment) {
					t.Fatalf("%s is missing delivery audit contract %q", testCase.path, fragment)
				}
			}
		})
	}
}

func runBackupAssetMigration064Contract(t *testing.T, fixture migrationFixture) {
	t.Helper()
	t.Run("ApplyAndParity", fixture.test064ApplyAndParity)
	t.Run("BackfillsEveryResticManagedState", fixture.test064BackfillsEveryResticManagedState)
	t.Run("AllowsPristineMutableHead", fixture.test064AllowsPristineMutableHead)
	t.Run("ManagedTreeSourceFingerprintIsRepositoryScoped", fixture.test064ManagedTreeSourceUnique)
	t.Run("DownRejectsLatch", fixture.test064DownRejectsLatch)
	t.Run("DownRejectsManagedPointAndVersionedLink", fixture.test064DownRejectsManagedHistory)
	t.Run("DownAllowsUnlinkedRcloneManagedLinkWithoutHistory", fixture.test064DownAllowsUnlinkedRcloneManagedLink)
	t.Run("DownRejectsPublicationAndParentLease", fixture.test064DownRejectsPublicationAndParentLease)
	t.Run("RejectedDownLeavesSchemaAndRowsUntouched", fixture.test064DownIsAtomic)
}

func runBackupAssetMigration065Contract(t *testing.T, fixture migrationFixture) {
	t.Helper()
	t.Run("ApplyCreatesChild7Tables", func(t *testing.T) {
		migrator, db := fixture.openAt(t, backupAssetSearchVersion)
		assertMigrationVersion(t, migrator, backupAssetSearchVersion)
		for _, table := range backupAssetSearchTables {
			if !databaseTableExists(t, db, fixture.engine, table) {
				t.Fatalf("%s search migration table %s is missing", fixture.engine, table)
			}
		}
	})
	t.Run("PristineDownRestores064", fixture.test065PristineDown)
	t.Run("PreservesLegacyRowsAndExtendsClosedSets", fixture.test065PreservesLegacyRows)
	t.Run("RejectsInvalidRowsAndCrossPointReferences", fixture.test065RejectsInvalidRows)
	t.Run("UsedDownIsRejectedAtomically", fixture.test065UsedDownIsAtomic)
	t.Run("ModelParity", func(t *testing.T) {
		_, db := fixture.openAt(t, backupAssetSearchVersion)
		for table, persistentModel := range backupAssetSearchModels() {
			want := gormColumnNames(t, persistentModel)
			var got []string
			if fixture.engine == "sqlite" {
				got = sqliteColumnNames(t, db, table)
			} else {
				got = postgresColumnNames(t, db, table)
			}
			sort.Strings(got)
			sort.Strings(want)
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Fatalf("%s %s columns mismatch\n got: %v\nwant: %v", fixture.engine, table, got, want)
			}
		}
	})
}

func runBackupAssetMigration066Contract(t *testing.T, fixture migrationFixture) {
	t.Helper()
	t.Run("ApplyAndParity", func(t *testing.T) {
		migrator, db := fixture.openAt(t, backupAssetContentVersion)
		assertMigrationVersion(t, migrator, backupAssetContentVersion)
		for _, table := range backupAssetContentTables {
			if !databaseTableExists(t, db, fixture.engine, table) {
				t.Fatalf("%s content migration table %s is missing", fixture.engine, table)
			}
			want := gormColumnNames(t, backupAssetContentModels()[table])
			var got []string
			if fixture.engine == "sqlite" {
				got = sqliteColumnNames(t, db, table)
				assertSQLiteTimeColumnsHaveNoDefault(t, db, table)
			} else {
				got = postgresColumnNames(t, db, table)
				assertPostgresTableTimeColumnsHaveNoDefault(t, db, table)
			}
			sort.Strings(got)
			sort.Strings(want)
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Fatalf("%s %s columns mismatch\n got: %v\nwant: %v", fixture.engine, table, got, want)
			}
			if table == "backup_asset_delivery_grants" {
				for _, required := range []string{"representation_source_bytes", "representation_size", "representation_truncated"} {
					found := false
					for _, column := range got {
						found = found || column == required
					}
					if !found {
						t.Fatalf("%s delivery grant representation column %s is missing", fixture.engine, required)
					}
				}
			}
		}
		for _, index := range []string{
			"idx_backup_asset_delivery_grants_delivery_state",
			"idx_backup_asset_delivery_grants_session_state",
			"idx_backup_asset_delivery_grants_resource_state",
			"idx_backup_asset_delivery_grants_expiry",
			"idx_backup_asset_delivery_grants_audit",
			"idx_backup_asset_delivery_requests_grant_state",
			"idx_backup_asset_delivery_requests_reconcile",
			"idx_backup_asset_delivery_usage_window",
			"idx_backup_asset_audit_events_content_grant_action",
		} {
			if definition := fixture.indexDefinition(t, db, index); definition == "" {
				t.Fatalf("%s content index %s is missing", fixture.engine, index)
			}
		}
		if fixture.engine == "sqlite" {
			assertSQLiteForeignKeyCheck(t, db)
		}
	})
	t.Run("PristineDownRestores065", fixture.test066PristineDown)
	t.Run("ValidRowsAndInvalidProducts", fixture.test066ValidAndInvalidRows)
	t.Run("UsedDownIsRejectedAtomically", fixture.test066UsedDownIsAtomic)
	t.Run("ExplicitSafeDrainAllowsDown", fixture.test066SafeDrain)
	t.Run("Preserves065UsedDownDefense", fixture.test066Preserves065Defense)
}

func runBackupAssetMigration067Contract(t *testing.T, fixture migrationFixture) {
	t.Helper()
	t.Run("ApplyAndModelParity", func(t *testing.T) {
		migrator, db := fixture.openAt(t, backupAssetProcessingVersion)
		assertMigrationVersion(t, migrator, backupAssetProcessingVersion)
		for _, table := range backupAssetProcessingTables {
			if !databaseTableExists(t, db, fixture.engine, table) {
				t.Fatalf("%s processing migration table %s is missing", fixture.engine, table)
			}
			want := gormColumnNames(t, backupAssetProcessingModels()[table])
			var got []string
			if fixture.engine == "sqlite" {
				got = sqliteColumnNames(t, db, table)
				assertSQLiteTimeColumnsHaveNoDefault(t, db, table)
			} else {
				got = postgresColumnNames(t, db, table)
				assertPostgresTableTimeColumnsHaveNoDefault(t, db, table)
			}
			sort.Strings(got)
			sort.Strings(want)
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Fatalf("%s %s columns mismatch\n got: %v\nwant: %v", fixture.engine, table, got, want)
			}
		}
		if definition := fixture.tableDefinition(t, db, "wrapped_domain_keys"); !strings.Contains(definition, "derived_store") {
			t.Fatalf("%s wrapped key CHECK does not permit derived_store: %s", fixture.engine, definition)
		}
	})
	t.Run("PristineDownRestores066", fixture.test067PristineDown)
	t.Run("RejectsInvalidStateErrorProducts", fixture.test067RejectsInvalidStateErrorProducts)
	t.Run("UsedDownIsRejectedAtomically", fixture.test067UsedDownIsAtomic)
	t.Run("DownRejectsUnrevokedSearchProjection", fixture.test067DownRejectsUnrevokedSearchProjection)
}

func backupAssetProcessingModels() map[string]any {
	return map[string]any{
		"backup_asset_processing_jobs":           model.BackupAssetProcessingJob{},
		"backup_asset_processing_interests":      model.BackupAssetProcessingInterest{},
		"backup_asset_processing_attempts":       model.BackupAssetProcessingAttempt{},
		"backup_asset_processing_grants":         model.BackupAssetProcessingGrant{},
		"backup_asset_processing_grant_requests": model.BackupAssetProcessingGrantRequest{},
		"backup_asset_processing_uploads":        model.BackupAssetProcessingUpload{},
		"backup_asset_worker_identities":         model.BackupAssetWorkerIdentity{},
		"backup_asset_worker_capabilities":       model.BackupAssetWorkerCapability{},
		"backup_asset_derived_artifact_sets":     model.BackupAssetDerivedArtifactSet{},
		"backup_asset_derived_artifacts":         model.BackupAssetDerivedArtifact{},
		"backup_asset_derived_blobs":             model.BackupAssetDerivedBlob{},
		"backup_asset_derived_blob_references":   model.BackupAssetDerivedBlobReference{},
		"backup_asset_updater_metadata":          model.BackupAssetUpdaterMetadata{},
	}
}

func runBackupAssetMigration069Contract(t *testing.T, fixture migrationFixture) {
	t.Helper()
	t.Run("ApplyExactAggregateAndPristineDown", func(t *testing.T) {
		migrator, db := fixture.openAt(t, backupAssetRecoveryVersion)
		assertMigrationVersion(t, migrator, backupAssetRecoveryVersion)
		assertBackupAssetRecoverySchema069(t, fixture, db)

		if err := migrator.Steps(-1); err != nil {
			t.Fatalf("step %s migration down to 000068: %v", fixture.engine, err)
		}
		assertMigrationVersion(t, migrator, backupAssetExportVersion)
		for _, table := range backupAssetRecoveryTables {
			if databaseTableExists(t, db, fixture.engine, table) {
				t.Fatalf("%s recovery table %s remains after pristine down", fixture.engine, table)
			}
		}
		for _, ownedTrigger := range backupAssetRecoveryOwnedTriggersForEngine(fixture.engine) {
			if fixture.recoveryTriggerExists(t, db, ownedTrigger.table, ownedTrigger.name) {
				t.Fatalf("%s pristine down left recovery trigger %q installed", fixture.engine, ownedTrigger.name)
			}
		}
		if fixture.engine == "postgres" {
			for _, function := range backupAssetRecoveryOwnedPostgresFunctions {
				if definition := fixture.recoveryFunctionDefinition(t, db, function); definition != "" {
					t.Fatalf("PostgreSQL pristine down left recovery function %q installed: %s", function, definition)
				}
			}
		}
		contentDefinition := fixture.tableDefinition(t, db, "backup_asset_delivery_grants")
		if strings.Contains(contentDefinition, "resource_kind = 'recovery_result'") {
			t.Fatalf("%s pristine down left the RecoveryResult Content arm active: %s", fixture.engine, contentDefinition)
		}
		if definition := fixture.indexDefinition(t, db, "idx_recovery_points_repository_id_id"); definition != "" {
			t.Fatalf("%s pristine down left the recovery-point ownership anchor installed: %s", fixture.engine, definition)
		}
	})
	t.Run("ValidAggregate", fixture.test069ValidAggregate)
	t.Run("SchedulerStateContract", fixture.test069SchedulerStateContract)
	t.Run("GrantTerminalTransitions", fixture.test069GrantTerminalTransitions)
	t.Run("ActiveGrantBindingImmutability", fixture.test069ActiveGrantBindingImmutability)
	t.Run("PlanIDUsesExactlyOneUniqueStructure", fixture.test069PlanIDUsesExactlyOneUniqueStructure)
	t.Run("PlanTargetNodeForeignKey", fixture.test069PlanTargetNodeForeignKey)
	t.Run("PlanRecoveryPointOwnership", fixture.test069PlanRecoveryPointOwnership)
	t.Run("NodeLeaseMatchesJobTarget", fixture.test069NodeLeaseMatchesJobTarget)
	t.Run("TerminalWorkspacePhases", fixture.test069TerminalWorkspacePhases)
	t.Run("FrozenAuthorityBindings", fixture.test069FrozenAuthorityBindings)
	t.Run("ExactMirrorDeleteGrantConsumption", fixture.test069ExactMirrorDeleteGrantConsumption)
	t.Run("JobAndResultSetTransitionMatrix", fixture.test069JobAndResultSetTransitionMatrix)
	t.Run("TerminalJobAttemptBarrier", fixture.test069TerminalJobAttemptBarrier)
	t.Run("TerminalArmedAttemptCannotBeDeletedOrRebuilt", fixture.test069TerminalArmedAttemptCannotBeDeletedOrRebuilt)
	t.Run("PlanJobItemOrdinalParity", fixture.test069PlanJobItemOrdinalParity)
	t.Run("ClosedProductsAndContentRecoveryResultArm", fixture.test069ClosedProductsAndContentRecoveryResultArm)
	t.Run("ContentAuthorizationRequiresExactStepUp", fixture.test069ContentAuthorizationRequiresExactStepUp)
	t.Run("ResultSetCleanupFenceInvariant", fixture.test069ResultSetCleanupFenceInvariant)
	t.Run("AttemptMutationArmTerminalAndTakeoverInvariant", fixture.test069AttemptMutationArmTerminalAndTakeoverInvariant)
	t.Run("TerminalAttemptIntegrity", fixture.test069TerminalAttemptIntegrity)
	t.Run("PublicationAndDeadlineIntegrity", fixture.test069PublicationAndDeadlineIntegrity)
	t.Run("ResultClassification", fixture.test069ResultClassification)
	t.Run("RecoveryResultContentAuthorization", fixture.test069RecoveryResultContentAuthorization)
	t.Run("StrictOpaqueDigestAndTemporalContracts", fixture.test069StrictOpaqueDigestAndTemporalContracts)
	t.Run("UseLatchImmutabilityAndOrdinaryEvidenceUpdates", fixture.test069UseLatchImmutabilityAndOrdinaryEvidenceUpdates)
	t.Run("PreservesExisting068ContentAndExportArms", fixture.test069PreservesExisting068ContentAndExportArms)
	t.Run("UsedDownIsRejectedAtomically", fixture.test069UsedDownIsRejectedAtomically)
	t.Run("RejectedDownSnapshotCoversMutationArm", fixture.test069RejectedDownSnapshotCoversMutationArm)
}

func (fixture migrationFixture) test069TaskRunNodeSnapshot(t *testing.T) {
	fixture.test069TaskRunNodeSnapshotPreservesTerminalOrphan(t)
	fixture.test069TaskRunNodeSnapshotRejectsUnbackfillableLegacyRows(t)

	migrator, db := fixture.openAt(t, backupAssetExportVersion)
	now := time.Now().UTC().Truncate(time.Second)
	const (
		originalNodeID = int64(769001)
		migratedNodeID = int64(769002)
		taskID         = int64(769003)
		runID          = int64(769004)
	)
	fixture.mustExec(t, db, `INSERT INTO nodes
		(id, name, host, username, backup_dir, created_at, updated_at)
		VALUES (?, 'recovery-taskrun-original', '127.0.0.1', 'root', '/tmp/recovery-taskrun-original', ?, ?)`,
		originalNodeID, now, now)
	fixture.mustExec(t, db, `INSERT INTO nodes
		(id, name, host, username, backup_dir, created_at, updated_at)
		VALUES (?, 'recovery-taskrun-migrated', '127.0.0.2', 'root', '/tmp/recovery-taskrun-migrated', ?, ?)`,
		migratedNodeID, now, now)
	fixture.mustExec(t, db, `INSERT INTO tasks
		(id, name, node_id, executor_type, status, created_at, updated_at)
		VALUES (?, 'recovery-taskrun-task', ?, 'rsync', 'running', ?, ?)`,
		taskID, originalNodeID, now, now)
	fixture.mustExec(t, db, `INSERT INTO task_runs
		(id, task_id, trigger_type, status, created_at, updated_at)
		VALUES (?, ?, 'manual', 'running', ?, ?)`, runID, taskID, now, now)

	if err := migrator.Steps(1); err != nil {
		t.Fatalf("apply %s 000069 for TaskRun snapshot: %v", fixture.engine, err)
	}
	if !databaseColumnExists(t, db, fixture.engine, "task_runs", "node_id_snapshot") {
		t.Fatalf("%s 000069 omitted task_runs.node_id_snapshot", fixture.engine)
	}
	var snapshot int64
	if err := db.QueryRow(fixture.bind(`SELECT node_id_snapshot FROM task_runs WHERE id = ?`), runID).Scan(&snapshot); err != nil {
		t.Fatalf("load %s TaskRun node snapshot: %v", fixture.engine, err)
	}
	if snapshot != originalNodeID {
		t.Fatalf("%s backfilled TaskRun node snapshot=%d, want %d", fixture.engine, snapshot, originalNodeID)
	}

	fixture.mustExec(t, db, `UPDATE tasks SET node_id = ?, updated_at = ? WHERE id = ?`, migratedNodeID, now.Add(time.Second), taskID)
	if err := db.QueryRow(fixture.bind(`SELECT node_id_snapshot FROM task_runs WHERE id = ?`), runID).Scan(&snapshot); err != nil {
		t.Fatalf("reload %s TaskRun node snapshot after Task migration: %v", fixture.engine, err)
	}
	if snapshot != originalNodeID {
		t.Fatalf("%s Task migration rewrote TaskRun node snapshot=%d, want %d", fixture.engine, snapshot, originalNodeID)
	}
	fixture.expectExecRejectedInRollback(t, db,
		`UPDATE task_runs SET node_id_snapshot = ? WHERE id = ?`, migratedNodeID, runID)
	fixture.expectExecRejectedInRollback(t, db, `INSERT INTO task_runs
		(id, task_id, trigger_type, status, created_at, updated_at)
		VALUES (?, ?, 'manual', 'running', ?, ?)`, runID+1, taskID, now, now)
	fixture.expectExecRejectedInRollback(t, db, `INSERT INTO task_runs
		(id, task_id, node_id_snapshot, trigger_type, status, created_at, updated_at)
		VALUES (?, ?, ?, 'manual', 'running', ?, ?)`, runID+2, taskID, originalNodeID, now, now)
	fixture.mustExec(t, db, `INSERT INTO task_runs
		(id, task_id, node_id_snapshot, trigger_type, status, created_at, updated_at)
		VALUES (?, ?, ?, 'manual', 'running', ?, ?)`, runID+3, taskID, migratedNodeID, now, now)

	if err := migrator.Steps(-1); err != nil {
		t.Fatalf("pristine %s 000069 down with TaskRun snapshots: %v", fixture.engine, err)
	}
	if databaseColumnExists(t, db, fixture.engine, "task_runs", "node_id_snapshot") {
		t.Fatalf("%s pristine 000069 down left task_runs.node_id_snapshot", fixture.engine)
	}
	var runCount int
	if err := db.QueryRow(fixture.bind(`SELECT COUNT(*) FROM task_runs WHERE task_id = ?`), taskID).Scan(&runCount); err != nil {
		t.Fatalf("count %s TaskRuns after pristine 000069 down: %v", fixture.engine, err)
	}
	if runCount != 2 {
		t.Fatalf("%s pristine 000069 down changed TaskRun count=%d, want 2", fixture.engine, runCount)
	}
}

func (fixture migrationFixture) test069TaskRunNodeSnapshotPreservesTerminalOrphan(t *testing.T) {
	t.Run("TerminalOrphanPreservedAsLegacyUnknown", func(t *testing.T) {
		migrator, db := fixture.openAt(t, backupAssetExportVersion)
		now := time.Date(2026, 8, 23, 4, 5, 6, 0, time.UTC)
		const (
			orphanTaskID = int64(769090)
			runID        = int64(769091)
		)
		fixture.mustExec(t, db, `INSERT INTO task_runs
			(id, task_id, trigger_type, status, created_at, updated_at)
			VALUES (?, ?, 'scheduled', 'success', ?, ?)`, runID, orphanTaskID, now, now)

		if err := migrator.Steps(1); err != nil {
			t.Fatalf("apply %s 000069 for terminal orphan TaskRun: %v", fixture.engine, err)
		}

		var (
			gotTaskID   int64
			gotSnapshot int64
			gotStatus   string
		)
		if err := db.QueryRow(fixture.bind(`SELECT task_id, node_id_snapshot, status
			FROM task_runs WHERE id = ?`), runID).Scan(&gotTaskID, &gotSnapshot, &gotStatus); err != nil {
			t.Fatalf("load %s terminal orphan TaskRun after 000069: %v", fixture.engine, err)
		}
		if gotTaskID != orphanTaskID || gotSnapshot != 0 || gotStatus != "success" {
			t.Fatalf("%s 000069 changed or misclassified terminal orphan TaskRun: "+
				"task_id=%d snapshot=%d status=%q", fixture.engine, gotTaskID, gotSnapshot, gotStatus)
		}
	})
}

func (fixture migrationFixture) test069TaskRunNodeSnapshotRejectsUnbackfillableLegacyRows(t *testing.T) {
	for _, testCase := range []struct {
		name string
		seed func(*testing.T, migrationFixture, *sql.DB, time.Time) int64
	}{
		{
			name: "active orphan",
			seed: func(t *testing.T, fixture migrationFixture, db *sql.DB, now time.Time) int64 {
				t.Helper()
				const runID = int64(769101)
				fixture.mustExec(t, db, `INSERT INTO task_runs
						(id, task_id, trigger_type, status, created_at, updated_at)
						VALUES (?, ?, 'manual', 'running', ?, ?)`, runID, int64(769199), now, now)
				return runID
			},
		},
		{
			name: "unknown-state orphan",
			seed: func(t *testing.T, fixture migrationFixture, db *sql.DB, now time.Time) int64 {
				t.Helper()
				const runID = int64(769104)
				fixture.mustExec(t, db, `INSERT INTO task_runs
						(id, task_id, trigger_type, status, created_at, updated_at)
						VALUES (?, ?, 'manual', 'state_outside_closed_contract', ?, ?)`,
					runID, int64(769198), now, now)
				return runID
			},
		},
		{
			name: "nonpositive task node",
			seed: func(t *testing.T, fixture migrationFixture, db *sql.DB, now time.Time) int64 {
				t.Helper()
				fixture.mustExec(t, db, `INSERT INTO tasks
						(id, name, node_id, executor_type, status, created_at, updated_at)
						VALUES (?, 'recovery-taskrun-invalid-node', 0, 'rsync', 'running', ?, ?)`, int64(769102), now, now)
				const runID = int64(769103)
				fixture.mustExec(t, db, `INSERT INTO task_runs
						(id, task_id, trigger_type, status, created_at, updated_at)
						VALUES (?, ?, 'manual', 'running', ?, ?)`, runID, int64(769102), now, now)
				return runID
			},
		},
	} {
		t.Run("FailClosed/"+testCase.name, func(t *testing.T) {
			migrator, db := fixture.openAt(t, backupAssetExportVersion)
			now := time.Now().UTC().Truncate(time.Second)
			runID := testCase.seed(t, fixture, db, now)

			if err := migrator.Steps(1); err == nil {
				t.Fatalf("%s 000069 unexpectedly accepted an unbackfillable legacy TaskRun (%s)", fixture.engine, testCase.name)
			}
			_, dirty, err := migrator.Version()
			if err != nil {
				t.Fatalf("read %s migration version after rejected 000069: %v", fixture.engine, err)
			}
			if !dirty {
				t.Fatalf("%s rejected 000069 did not leave startup fail-closed with a dirty migration version", fixture.engine)
			}
			if databaseColumnExists(t, db, fixture.engine, "task_runs", "node_id_snapshot") {
				t.Fatalf("%s rejected 000069 left a partial task_runs.node_id_snapshot column", fixture.engine)
			}
			if fixture.recoveryTriggerExists(t, db, "task_runs", "trg_backup_asset_recovery_task_runs_node_snapshot_insert") {
				t.Fatalf("%s rejected 000069 left a partial TaskRun snapshot trigger", fixture.engine)
			}
			for _, table := range backupAssetRecoveryTables {
				if databaseTableExists(t, db, fixture.engine, table) {
					t.Fatalf("%s rejected 000069 left partial recovery table %s", fixture.engine, table)
				}
			}
			var runCount int
			if err := db.QueryRow(fixture.bind(`SELECT COUNT(*) FROM task_runs WHERE id = ?`), runID).Scan(&runCount); err != nil {
				t.Fatalf("count %s rejected orphan TaskRun after 000069: %v", fixture.engine, err)
			}
			if runCount != 1 {
				t.Fatalf("%s rejected 000069 changed orphan TaskRun row count=%d, want 1", fixture.engine, runCount)
			}
		})
	}
}

func (fixture migrationFixture) test069SchedulerStateContract(t *testing.T) {
	migrator, db := fixture.openAt(t, backupAssetRecoveryVersion)
	type schedulerRow struct {
		id        string
		scope     string
		revision  int64
		createdAt time.Time
		updatedAt time.Time
	}

	rows, err := db.Query(`SELECT id, scheduler_scope, scheduler_revision, created_at, updated_at
		FROM backup_asset_recovery_evidence WHERE kind = 'scheduler_state' ORDER BY id`)
	if err != nil {
		t.Fatalf("list %s recovery scheduler rows: %v", fixture.engine, err)
	}
	var schedulers []schedulerRow
	for rows.Next() {
		var row schedulerRow
		if err := rows.Scan(&row.id, &row.scope, &row.revision, &row.createdAt, &row.updatedAt); err != nil {
			t.Fatalf("scan %s recovery scheduler row: %v", fixture.engine, err)
		}
		schedulers = append(schedulers, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate %s recovery scheduler rows: %v", fixture.engine, err)
	}
	closeMigrationRows(t, rows)
	wantSchedulers := []struct {
		id    string
		scope string
	}{
		{id: recoveryClaimSchedulerRowID, scope: "claim"},
		{id: recoveryTakeoverSchedulerRowID, scope: "takeover"},
	}
	if len(schedulers) != len(wantSchedulers) {
		t.Fatalf("%s recovery scheduler rows=%d, want %d", fixture.engine, len(schedulers), len(wantSchedulers))
	}
	for index, want := range wantSchedulers {
		got := schedulers[index]
		if got.id != want.id || got.scope != want.scope || got.revision != 1 || got.createdAt.IsZero() || got.updatedAt.IsZero() {
			t.Fatalf("%s recovery scheduler row[%d]=%+v, want id=%s scope=%s revision=1 with timestamps",
				fixture.engine, index, got, want.id, want.scope)
		}
	}

	claim := schedulers[0]
	fixture.expectExecRejectedInRollback(t, db, `UPDATE backup_asset_recovery_evidence
		SET id = ?, scheduler_revision = scheduler_revision + 1 WHERE id = ?`,
		recoveryMigrationOpaqueID(699961), claim.id)
	fixture.expectExecRejectedInRollback(t, db, `UPDATE backup_asset_recovery_evidence
		SET scheduler_scope = 'takeover', scheduler_revision = scheduler_revision + 1 WHERE id = ?`, claim.id)
	fixture.expectExecRejectedInRollback(t, db, `UPDATE backup_asset_recovery_evidence
		SET created_at = ?, scheduler_revision = scheduler_revision + 1 WHERE id = ?`,
		claim.createdAt.Add(-time.Second), claim.id)
	fixture.expectExecRejectedInRollback(t, db, `UPDATE backup_asset_recovery_evidence
		SET scheduler_revision = scheduler_revision + 2, updated_at = ? WHERE id = ?`,
		claim.updatedAt.Add(time.Second), claim.id)
	fixture.expectExecRejectedInRollback(t, db,
		`DELETE FROM backup_asset_recovery_evidence WHERE id = ?`, claim.id)

	cursorAt := claim.updatedAt.Add(time.Minute)
	highWaterAt := cursorAt.Add(time.Minute)
	cursorID := recoveryMigrationOpaqueID(699962)
	highWaterID := recoveryMigrationOpaqueID(699963)
	result, err := db.Exec(fixture.bind(`UPDATE backup_asset_recovery_evidence
		SET scheduler_cursor_at = ?, scheduler_cursor_id = ?,
			scheduler_high_water_at = ?, scheduler_high_water_id = ?,
			scheduler_revision = scheduler_revision + 1, updated_at = ?
		WHERE id = ? AND scheduler_revision = 1`),
		cursorAt, cursorID, highWaterAt, highWaterID, cursorAt, claim.id)
	if err != nil {
		t.Fatalf("advance %s recovery scheduler cursor: %v", fixture.engine, err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		t.Fatalf("advance %s recovery scheduler cursor rows=%d err=%v, want 1 nil", fixture.engine, affected, err)
	}
	var gotCursorAt, gotHighWaterAt time.Time
	var gotCursorID, gotHighWaterID string
	var gotRevision int64
	if err := db.QueryRow(fixture.bind(`SELECT scheduler_cursor_at, scheduler_cursor_id,
		scheduler_high_water_at, scheduler_high_water_id, scheduler_revision
		FROM backup_asset_recovery_evidence WHERE id = ?`), claim.id).
		Scan(&gotCursorAt, &gotCursorID, &gotHighWaterAt, &gotHighWaterID, &gotRevision); err != nil {
		t.Fatalf("load advanced %s recovery scheduler cursor: %v", fixture.engine, err)
	}
	if !gotCursorAt.Equal(cursorAt) || gotCursorID != cursorID || !gotHighWaterAt.Equal(highWaterAt) ||
		gotHighWaterID != highWaterID || gotRevision != 2 {
		t.Fatalf("%s advanced recovery scheduler cursor=%s/%s high-water=%s/%s revision=%d",
			fixture.engine, gotCursorAt, gotCursorID, gotHighWaterAt, gotHighWaterID, gotRevision)
	}

	if err := migrator.Steps(-1); err != nil {
		t.Fatalf("step %s 000069 down with scheduler metadata only: %v", fixture.engine, err)
	}
	assertMigrationVersion(t, migrator, backupAssetExportVersion)
}

func (fixture migrationFixture) test069ValidAggregate(t *testing.T) {
	_, db := fixture.openAt(t, backupAssetRecoveryVersion)
	now := time.Now().UTC().Truncate(time.Second)
	aggregate := fixture.seedRecoveryMigrationAggregate(t, db, "a", 1, now)
	var resultCount int
	if err := db.QueryRow(fixture.bind(`SELECT COUNT(*) FROM backup_asset_recovery_results
		WHERE id = ? AND job_id = ?`), aggregate.ResultID, aggregate.JobID).Scan(&resultCount); err != nil {
		t.Fatalf("load %s valid recovery result: %v", fixture.engine, err)
	}
	if resultCount != 1 {
		t.Fatalf("%s valid recovery result count=%d want 1", fixture.engine, resultCount)
	}
	if fixture.engine == "sqlite" {
		assertSQLiteForeignKeyCheck(t, db)
	}
}

func (fixture migrationFixture) test069TargetBindingCopies(t *testing.T) {
	_, db := fixture.openAt(t, backupAssetRecoveryVersion)
	for _, table := range []string{"backup_asset_recovery_preflights", "backup_asset_recovery_jobs"} {
		for _, column := range []string{"target_root_id", "root_locator_digest", "path_digest"} {
			if !databaseColumnExists(t, db, fixture.engine, table, column) {
				t.Fatalf("%s %s.%s target-binding copy is missing", fixture.engine, table, column)
			}
		}
	}
	assertBackupAssetRecoveryModelParity(t, fixture, db)

	now := time.Now().UTC().Truncate(time.Second)
	aggregate := fixture.seedRecoveryMigrationAggregate(t, db, "t", 69, now)
	var matchingRows int
	if err := db.QueryRow(fixture.bind(`SELECT COUNT(*)
		FROM backup_asset_recovery_plans AS plan
		JOIN backup_asset_recovery_preflights AS preflight ON preflight.plan_id = plan.id
		JOIN backup_asset_recovery_jobs AS job ON job.plan_id = plan.id AND job.preflight_id = preflight.id
		WHERE plan.id = ?
		  AND preflight.target_root_id = plan.target_root_id
		  AND preflight.root_locator_digest = plan.root_locator_digest
		  AND preflight.path_digest = plan.path_digest
		  AND job.target_root_id = preflight.target_root_id
		  AND job.root_locator_digest = preflight.root_locator_digest
		  AND job.path_digest = preflight.path_digest`), aggregate.PlanID).Scan(&matchingRows); err != nil {
		t.Fatalf("load %s durable target-binding parity: %v", fixture.engine, err)
	}
	if matchingRows != 1 {
		t.Fatalf("%s durable target-binding parity rows=%d want 1", fixture.engine, matchingRows)
	}
}

func (fixture migrationFixture) test069GrantTerminalTransitions(t *testing.T) {
	_, db := fixture.openAt(t, backupAssetRecoveryVersion)
	now := time.Now().UTC().Truncate(time.Second)
	consumedGrant := fixture.seedRecoveryMigrationAggregate(t, db, "a", 11, now)
	consumedAt := now.Add(time.Minute)
	fixture.mustExec(t, db, `UPDATE backup_asset_recovery_grants
		SET consumed_at = ?, updated_at = ? WHERE id = ?`, consumedAt, consumedAt, consumedGrant.GrantID)

	var gotConsumedAt, gotRevokedAt sql.NullTime
	if err := db.QueryRow(fixture.bind(`SELECT consumed_at, revoked_at FROM backup_asset_recovery_grants WHERE id = ?`), consumedGrant.GrantID).Scan(
		&gotConsumedAt,
		&gotRevokedAt,
	); err != nil {
		t.Fatalf("read %s legally consumed recovery grant: %v", fixture.engine, err)
	}
	if !gotConsumedAt.Valid || !gotConsumedAt.Time.Equal(consumedAt) || gotRevokedAt.Valid {
		t.Fatalf("%s active -> consumed grant state=%v/%v, want consumed timestamp and no revocation", fixture.engine, gotConsumedAt, gotRevokedAt)
	}

	revokedGrant := fixture.seedRecoveryMigrationAggregate(t, db, "b", 12, now.Add(time.Second))
	revokedAt := now.Add(2 * time.Minute)
	fixture.mustExec(t, db, `UPDATE backup_asset_recovery_grants
		SET revoked_at = ?, updated_at = ? WHERE id = ?`, revokedAt, revokedAt, revokedGrant.GrantID)
	if err := db.QueryRow(fixture.bind(`SELECT consumed_at, revoked_at FROM backup_asset_recovery_grants WHERE id = ?`), revokedGrant.GrantID).Scan(
		&gotConsumedAt,
		&gotRevokedAt,
	); err != nil {
		t.Fatalf("read %s legally revoked recovery grant: %v", fixture.engine, err)
	}
	if gotConsumedAt.Valid || !gotRevokedAt.Valid || !gotRevokedAt.Time.Equal(revokedAt) {
		t.Fatalf("%s active -> revoked grant state=%v/%v, want no consumption and revocation timestamp", fixture.engine, gotConsumedAt, gotRevokedAt)
	}

	activeGrant := fixture.seedRecoveryMigrationAggregate(t, db, "c", 13, now.Add(2*time.Second))
	alternateGrant := fixture.seedRecoveryMigrationAggregate(t, db, "d", 14, now.Add(3*time.Second))
	var unexpectedlyAllowed []string
	assertRejected := func(name, query string, args ...any) {
		t.Helper()
		tx, err := db.Begin()
		if err != nil {
			t.Fatalf("begin %s %s terminal-transition probe: %v", fixture.engine, name, err)
		}
		defer func() {
			if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
				t.Fatalf("rollback %s %s terminal-transition probe: %v", fixture.engine, name, err)
			}
		}()
		if _, err := tx.Exec(fixture.bind(query), args...); err == nil {
			unexpectedlyAllowed = append(unexpectedlyAllowed, name)
		}
	}

	assertRejected("consumed_at to NULL", `UPDATE backup_asset_recovery_grants
		SET consumed_at = NULL, updated_at = ? WHERE id = ?`, now.Add(3*time.Minute), consumedGrant.GrantID)
	assertRejected("consumed_at timestamp rewrite", `UPDATE backup_asset_recovery_grants
		SET consumed_at = ?, updated_at = ? WHERE id = ?`, now.Add(4*time.Minute), now.Add(4*time.Minute), consumedGrant.GrantID)
	assertRejected("consumed to revoked", `UPDATE backup_asset_recovery_grants
		SET consumed_at = NULL, revoked_at = ?, updated_at = ? WHERE id = ?`, now.Add(5*time.Minute), now.Add(5*time.Minute), consumedGrant.GrantID)
	assertRejected("revoked_at to NULL", `UPDATE backup_asset_recovery_grants
		SET revoked_at = NULL, updated_at = ? WHERE id = ?`, now.Add(3*time.Minute), revokedGrant.GrantID)
	assertRejected("revoked_at timestamp rewrite", `UPDATE backup_asset_recovery_grants
		SET revoked_at = ?, updated_at = ? WHERE id = ?`, now.Add(4*time.Minute), now.Add(4*time.Minute), revokedGrant.GrantID)
	assertRejected("revoked to consumed", `UPDATE backup_asset_recovery_grants
		SET revoked_at = NULL, consumed_at = ?, updated_at = ? WHERE id = ?`, now.Add(5*time.Minute), now.Add(5*time.Minute), revokedGrant.GrantID)
	assertRejected("active grant with both terminal timestamps", `UPDATE backup_asset_recovery_grants
		SET consumed_at = ?, revoked_at = ?, updated_at = ? WHERE id = ?`, now.Add(time.Minute), now.Add(time.Minute), now.Add(time.Minute), activeGrant.GrantID)
	assertRejected("consumed grant id rewrite", `UPDATE backup_asset_recovery_grants
		SET id = ? WHERE id = ?`, recoveryMigrationOpaqueID(699996), consumedGrant.GrantID)
	assertRejected("consumed grant plan/job binding rewrite", `UPDATE backup_asset_recovery_grants
		SET plan_id = ?, job_id = ? WHERE id = ?`, alternateGrant.PlanID, alternateGrant.JobID, consumedGrant.GrantID)
	assertRejected("consumed grant authority category rewrite", `UPDATE backup_asset_recovery_grants
		SET authority_category = 'write', job_id = NULL WHERE id = ?`, consumedGrant.GrantID)
	assertRejected("consumed grant hash rewrite", `UPDATE backup_asset_recovery_grants
		SET grant_hash = ? WHERE id = ?`, recoveryMigrationDigest(699997), consumedGrant.GrantID)
	assertRejected("consumed grant actor rewrite", `UPDATE backup_asset_recovery_grants
		SET actor_user_id = ? WHERE id = ?`, alternateGrant.UserID, consumedGrant.GrantID)
	assertRejected("consumed grant session rewrite", `UPDATE backup_asset_recovery_grants
		SET actor_session_id = 'rewritten-session' WHERE id = ?`, consumedGrant.GrantID)
	assertRejected("consumed grant binding digest rewrite", `UPDATE backup_asset_recovery_grants
		SET binding_digest = ? WHERE id = ?`, recoveryMigrationDigest(699998), consumedGrant.GrantID)
	assertRejected("consumed grant reason rewrite", `UPDATE backup_asset_recovery_grants
		SET encrypted_reason = 'enc:v2:rewritten-reason' WHERE id = ?`, consumedGrant.GrantID)
	assertRejected("consumed grant expiry rewrite", `UPDATE backup_asset_recovery_grants
		SET expires_at = ? WHERE id = ?`, now.Add(20*time.Minute), consumedGrant.GrantID)
	assertRejected("consumed grant created timestamp rewrite", `UPDATE backup_asset_recovery_grants
		SET created_at = ? WHERE id = ?`, now.Add(-time.Minute), consumedGrant.GrantID)
	assertRejected("consumed grant updated timestamp rewrite", `UPDATE backup_asset_recovery_grants
		SET updated_at = ? WHERE id = ?`, now.Add(2*time.Minute), consumedGrant.GrantID)

	if len(unexpectedlyAllowed) != 0 {
		t.Fatalf("%s recovery grant terminal transitions unexpectedly allowed: %s", fixture.engine, strings.Join(unexpectedlyAllowed, ", "))
	}
	if !fixture.recoveryTriggerExists(t, db, "backup_asset_recovery_grants", "trg_backup_asset_recovery_grants_terminal") {
		t.Fatalf("%s recovery grant terminal trigger is missing", fixture.engine)
	}
}

func (fixture migrationFixture) test069GrantTerminalTransitionBindings(t *testing.T) {
	_, db := fixture.openAt(t, backupAssetRecoveryVersion)
	now := time.Now().UTC().Truncate(time.Second)

	for _, terminalTransition := range []struct {
		name   string
		column string
	}{
		{name: "consume", column: "consumed_at"},
		{name: "revoke", column: "revoked_at"},
	} {
		t.Run("clean "+terminalTransition.name, func(t *testing.T) {
			sequence := 80
			marker := "a"
			if terminalTransition.column == "revoked_at" {
				sequence = 81
				marker = "b"
			}
			grant := fixture.seedRecoveryMigrationAggregate(t, db, marker, sequence, now)
			terminalAt := now.Add(time.Minute)
			fixture.mustExec(t, db, fmt.Sprintf(`UPDATE backup_asset_recovery_grants
				SET %s = ?, updated_at = ? WHERE id = ?`, terminalTransition.column),
				terminalAt, terminalAt, grant.GrantID)

			var consumedAt, revokedAt sql.NullTime
			var updatedAt time.Time
			if err := db.QueryRow(fixture.bind(`SELECT consumed_at, revoked_at, updated_at
				FROM backup_asset_recovery_grants WHERE id = ?`), grant.GrantID).Scan(
				&consumedAt,
				&revokedAt,
				&updatedAt,
			); err != nil {
				t.Fatalf("read %s clean %s terminal transition: %v", fixture.engine, terminalTransition.name, err)
			}
			if !updatedAt.Equal(terminalAt) {
				t.Fatalf("%s clean %s terminal transition updated_at=%s, want %s",
					fixture.engine, terminalTransition.name, updatedAt, terminalAt)
			}
			if terminalTransition.column == "consumed_at" {
				if !consumedAt.Valid || !consumedAt.Time.Equal(terminalAt) || revokedAt.Valid {
					t.Fatalf("%s clean consume state=%v/%v, want consumed timestamp and no revocation",
						fixture.engine, consumedAt, revokedAt)
				}
			} else if consumedAt.Valid || !revokedAt.Valid || !revokedAt.Time.Equal(terminalAt) {
				t.Fatalf("%s clean revoke state=%v/%v, want no consumption and revocation timestamp",
					fixture.engine, consumedAt, revokedAt)
			}
		})
	}

	activeGrant := fixture.seedRecoveryMigrationAggregate(t, db, "c", 82, now)
	alternateGrant := fixture.seedRecoveryMigrationAggregate(t, db, "d", 83, now)
	type authorityBindingRewrite struct {
		name string
		set  string
		args func() []any
	}
	rewrites := []authorityBindingRewrite{
		{
			name: "grant identity",
			set:  "id = ?",
			args: func() []any { return []any{recoveryMigrationOpaqueID(699996)} },
		},
		{
			name: "plan/job binding",
			set:  "plan_id = ?, job_id = ?",
			args: func() []any { return []any{alternateGrant.PlanID, alternateGrant.JobID} },
		},
		{
			name: "authority category/job shape",
			set:  "authority_category = 'exact_mirror_delete', job_id = ?",
			args: func() []any { return []any{activeGrant.JobID} },
		},
		{
			name: "grant hash",
			set:  "grant_hash = ?",
			args: func() []any { return []any{recoveryMigrationDigest(699997)} },
		},
		{
			name: "actor user",
			set:  "actor_user_id = ?",
			args: func() []any { return []any{alternateGrant.UserID} },
		},
		{
			name: "actor session",
			set:  "actor_session_id = ?",
			args: func() []any { return []any{"rewritten-session"} },
		},
		{
			name: "binding digest",
			set:  "binding_digest = ?",
			args: func() []any { return []any{recoveryMigrationDigest(699998)} },
		},
		{
			name: "encrypted reason",
			set:  "encrypted_reason = ?",
			args: func() []any { return []any{"enc:v2:rewritten-reason"} },
		},
		{
			name: "expiry",
			set:  "expires_at = ?",
			args: func() []any { return []any{now.Add(20 * time.Minute)} },
		},
		{
			name: "created timestamp",
			set:  "created_at = ?",
			args: func() []any { return []any{now.Add(-time.Minute)} },
		},
	}

	var accepted []string
	for _, terminalTransition := range []struct {
		name   string
		column string
	}{
		{name: "consume", column: "consumed_at"},
		{name: "revoke", column: "revoked_at"},
	} {
		for _, rewrite := range rewrites {
			terminalAt := now.Add(time.Minute)
			query := fmt.Sprintf(`UPDATE backup_asset_recovery_grants
				SET %s, %s = ?, updated_at = ? WHERE id = ?`, rewrite.set, terminalTransition.column)
			args := append(rewrite.args(), terminalAt, terminalAt, activeGrant.GrantID)
			tx, err := db.Begin()
			if err != nil {
				t.Fatalf("begin %s combined %s %s rewrite probe: %v", fixture.engine, terminalTransition.name, rewrite.name, err)
			}
			_, execErr := tx.Exec(fixture.bind(query), args...)
			if rollbackErr := tx.Rollback(); rollbackErr != nil {
				t.Fatalf("rollback %s combined %s %s rewrite probe: %v", fixture.engine, terminalTransition.name, rewrite.name, rollbackErr)
			}
			if execErr == nil {
				accepted = append(accepted, terminalTransition.name+"/"+rewrite.name)
			}
		}
	}
	if len(accepted) != 0 {
		t.Fatalf("%s active-to-terminal recovery grant updates accepted authority-binding rewrites: %s",
			fixture.engine, strings.Join(accepted, ", "))
	}
}

func (fixture migrationFixture) test069PlanTargetNodeForeignKey(t *testing.T) {
	_, db := fixture.openAt(t, backupAssetRecoveryVersion)
	now := time.Now().UTC().Truncate(time.Second)
	aggregate := fixture.seedRecoveryMigrationAggregate(t, db, "e", 84, now, true)

	// Isolate the plan with a nonterminal, unarmed job graph so the terminal
	// attempt identity contract remains intact during this foreign-key probe.
	fixture.removeRecoveryMigrationActiveJobGraph(t, db, aggregate)

	fixture.expectExecRejectedInRollback(t, db, `UPDATE backup_asset_recovery_plans
		SET target_node_id = ? WHERE id = ?`, aggregate.NodeID+1_000_000, aggregate.PlanID)

	if fixture.engine == "sqlite" {
		assertSQLiteForeignKeyAction(t, db, "backup_asset_recovery_plans", "target_node_id", "nodes", "RESTRICT")
	} else {
		assertPostgresForeignKeyAction(t, db, "backup_asset_recovery_plans", "target_node_id", "nodes", "RESTRICT")
	}
}

func (fixture migrationFixture) test069PlanRecoveryPointOwnership(t *testing.T) {
	_, db := fixture.openAt(t, backupAssetRecoveryVersion)
	now := time.Now().UTC().Truncate(time.Second)
	plan := fixture.seedRecoveryMigrationAggregate(t, db, "j", 89, now, true)
	alternate := fixture.seedRecoveryMigrationAggregate(t, db, "k", 90, now.Add(time.Second))
	alternateRepositoryID := alternate.RepositoryID
	clonePlanQuery := `INSERT INTO backup_asset_recovery_plans (
		id, requester_id, endpoint, idempotency_key_digest, repository_id, recovery_point_id,
		source_revision_digest, source_revision_kind, immutable_locator_digest, immutable_manifest_digest,
		observation_fingerprint, catalog_generation_id, observed_at, encrypted_source_locator,
		target_mode, target_node_id, target_root_id, encrypted_target_root_locator,
		encrypted_target_relative_path, root_locator_digest, path_digest, target_base_revision,
		credential_scope_revision, root_revision, filesystem_revision, selection_digest,
		binding_digest, capability_revision, conflict_policy, operation_set_digest, delete_set_digest,
		security_decision, security_decision_digest, security_finding_set_digest, security_policy_revision,
		security_override_binding_digest, encrypted_override_reason, preflight_revision,
		preflight_expires_at, estimated_items, estimated_bytes, state, transition_revision,
		created_at, updated_at)
		SELECT ?, requester_id, endpoint, ?, ?, recovery_point_id,
			source_revision_digest, source_revision_kind, immutable_locator_digest, immutable_manifest_digest,
			observation_fingerprint, catalog_generation_id, observed_at, encrypted_source_locator,
			target_mode, target_node_id, target_root_id, encrypted_target_root_locator,
			encrypted_target_relative_path, root_locator_digest, path_digest, target_base_revision,
			credential_scope_revision, root_revision, filesystem_revision, selection_digest,
			binding_digest, capability_revision, conflict_policy, operation_set_digest, delete_set_digest,
			security_decision, security_decision_digest, security_finding_set_digest, security_policy_revision,
			security_override_binding_digest, encrypted_override_reason, preflight_revision,
			preflight_expires_at, estimated_items, estimated_bytes, state, transition_revision,
			created_at, updated_at
		FROM backup_asset_recovery_plans WHERE id = ?`

	t.Run("insert matching ownership control", func(t *testing.T) {
		fixture.expectExecAcceptedInRollback(t, db, clonePlanQuery,
			recoveryMigrationOpaqueID(699990), recoveryMigrationDigest(699990), plan.RepositoryID, plan.PlanID)
	})

	t.Run("insert mismatch", func(t *testing.T) {
		fixture.expectExecRejectedInRollback(t, db, clonePlanQuery,
			recoveryMigrationOpaqueID(699991), recoveryMigrationDigest(699991), alternateRepositoryID, plan.PlanID)
	})

	t.Run("update mismatch", func(t *testing.T) {
		fixture.removeRecoveryMigrationActiveJobGraph(t, db, plan)
		fixture.expectExecAcceptedInRollback(t, db, `UPDATE backup_asset_recovery_plans
			SET repository_id = ? WHERE id = ?`, plan.RepositoryID, plan.PlanID)
		fixture.expectExecRejectedInRollback(t, db, `UPDATE backup_asset_recovery_plans
			SET repository_id = ? WHERE id = ?`, alternateRepositoryID, plan.PlanID)
	})
}

func (fixture migrationFixture) test069NodeLeaseMatchesJobTarget(t *testing.T) {
	_, db := fixture.openAt(t, backupAssetRecoveryVersion)
	now := time.Now().UTC().Truncate(time.Second)
	job := fixture.seedRecoveryMigrationAggregate(t, db, "l", 91, now)
	alternate := fixture.seedRecoveryMigrationAggregate(t, db, "m", 92, now.Add(time.Second))
	fixture.mustExec(t, db, `DELETE FROM backup_asset_recovery_evidence WHERE job_id = ?`, alternate.JobID)
	fixture.mustExec(t, db, `DELETE FROM backup_asset_recovery_node_leases WHERE id = ?`, alternate.NodeLeaseID)

	t.Run("insert mismatch", func(t *testing.T) {
		fixture.expectExecRejectedInRollback(t, db, `INSERT INTO backup_asset_recovery_node_leases
			(id, node_id, holder_kind, job_id, attempt_id, owner_id, fence, state,
			 lease_expires_at, released_at, created_at, updated_at)
			VALUES (?, ?, 'recovery_cleanup', ?, NULL, 'cleanup-worker', 1, 'released', ?, ?, ?, ?)`,
			recoveryMigrationOpaqueID(699992), alternate.NodeID, job.JobID,
			now.Add(time.Hour), now.Add(30*time.Minute), now, now.Add(30*time.Minute))
	})

	t.Run("update mismatch", func(t *testing.T) {
		fixture.expectExecRejectedInRollback(t, db, `UPDATE backup_asset_recovery_node_leases
			SET node_id = ? WHERE id = ?`, alternate.NodeID, job.NodeLeaseID)
	})
}

func (fixture migrationFixture) test069TerminalWorkspacePhases(t *testing.T) {
	_, db := fixture.openAt(t, backupAssetRecoveryVersion)
	now := time.Now().UTC().Truncate(time.Second)
	aggregate := fixture.seedRecoveryMigrationAggregate(t, db, "n", 93, now, true)

	fixture.mustExec(t, db, `DELETE FROM backup_asset_recovery_results WHERE result_set_id = ?`, aggregate.ResultSetID)
	fixture.mustExec(t, db, `DELETE FROM backup_asset_recovery_result_sets WHERE id = ?`, aggregate.ResultSetID)
	closedAt := now.Add(time.Minute)
	fixture.mustExec(t, db, `UPDATE backup_asset_recovery_attempts
		SET state = 'completed', mutation_armed = ?, closed_at = ?, updated_at = ? WHERE id = ?`,
		false, closedAt, closedAt, aggregate.AttemptID)
	for _, state := range []string{"failed", "canceled", "needs_attention"} {
		for _, phase := range []string{"reserved", "marker_created", "writing", "sealed"} {
			t.Run(state+"/"+phase, func(t *testing.T) {
				fixture.expectExecRejectedInRollback(t, db, `UPDATE backup_asset_recovery_jobs
					SET state = ?, workspace_phase = ? WHERE id = ?`, state, phase, aggregate.JobID)
			})
		}
	}
	for _, state := range []string{"succeeded", "degraded"} {
		for _, phase := range []string{"reserved", "marker_created", "writing"} {
			t.Run(state+"/"+phase, func(t *testing.T) {
				fixture.expectExecRejectedInRollback(t, db, `UPDATE backup_asset_recovery_jobs
					SET state = ?, workspace_phase = ? WHERE id = ?`, state, phase, aggregate.JobID)
			})
		}
	}
	legalPhases := []struct {
		name  string
		query string
		args  []any
	}{
		{
			name:  "cleanup_due",
			query: `UPDATE backup_asset_recovery_jobs SET state = 'failed', workspace_phase = 'cleanup_due' WHERE id = ?`,
			args:  []any{aggregate.JobID},
		},
	}
	for _, legalPhase := range legalPhases {
		t.Run("allows/"+legalPhase.name, func(t *testing.T) {
			tx, err := db.Begin()
			if err != nil {
				t.Fatalf("begin %s legal terminal workspace phase %s: %v", fixture.engine, legalPhase.name, err)
			}
			_, execErr := tx.Exec(fixture.bind(legalPhase.query), legalPhase.args...)
			if rollbackErr := tx.Rollback(); rollbackErr != nil {
				t.Fatalf("rollback %s legal terminal workspace phase %s: %v", fixture.engine, legalPhase.name, rollbackErr)
			}
			if execErr != nil {
				t.Fatalf("%s legal terminal workspace phase %s rejected: %v", fixture.engine, legalPhase.name, execErr)
			}
		})
	}
}

func (fixture migrationFixture) test069FrozenAuthorityBindings(t *testing.T) {
	_, db := fixture.openAt(t, backupAssetRecoveryVersion)
	now := time.Now().UTC().Truncate(time.Second)

	requiredColumns := map[string][]string{
		"task_runs": {"node_id_snapshot"},
		"backup_asset_recovery_plans": {
			"source_revision_digest",
			"security_decision_digest",
		},
		"backup_asset_recovery_jobs": {
			"plan_binding_digest",
			"selection_digest",
			"source_revision_digest",
			"preflight_id",
			"preflight_revision",
			"preflight_expires_at",
			"preflight_target_revision",
			"capability_revision",
			"operation_set_digest",
			"delete_set_digest",
			"security_decision",
			"security_decision_digest",
			"security_finding_set_digest",
			"security_policy_revision",
			"security_override_binding_digest",
			"estimated_items",
			"estimated_bytes",
			"authority_grant_id",
			"authority_category",
			"authority_binding_digest",
			"authority_expires_at",
			"authority_consumed_at",
		},
		"backup_asset_recovery_checkpoints": {
			"plan_binding_digest",
			"source_revision_digest",
			"preflight_id",
			"preflight_revision",
			"preflight_expires_at",
			"security_decision",
			"security_decision_digest",
			"security_finding_set_digest",
			"security_policy_revision",
			"authority_grant_id",
			"job_authority_category",
			"authority_binding_digest",
			"authority_expires_at",
		},
	}
	for table, columns := range requiredColumns {
		for _, column := range columns {
			if !databaseColumnExists(t, db, fixture.engine, table, column) {
				t.Fatalf("%s frozen recovery authority is missing %s.%s", fixture.engine, table, column)
			}
		}
	}

	aggregate := fixture.seedRecoveryMigrationAggregate(t, db, "a", 96, now)
	var parityCount int
	if err := db.QueryRow(fixture.bind(`SELECT COUNT(*)
		FROM backup_asset_recovery_jobs AS job
		JOIN backup_asset_recovery_plans AS plan ON plan.id = job.plan_id
		JOIN backup_asset_recovery_preflights AS preflight
		  ON preflight.id = job.preflight_id AND preflight.plan_id = job.plan_id
		JOIN backup_asset_recovery_grants AS authority ON authority.id = job.authority_grant_id
		WHERE job.id = ?
		  AND job.plan_binding_digest = plan.binding_digest
		  AND job.selection_digest = plan.selection_digest
		  AND job.source_revision_digest = plan.source_revision_digest
		  AND job.source_revision_digest = preflight.source_revision_digest
		  AND job.preflight_revision = plan.preflight_revision
		  AND job.preflight_revision = preflight.revision
		  AND job.preflight_expires_at = plan.preflight_expires_at
		  AND job.preflight_expires_at = preflight.expires_at
		  AND job.preflight_target_revision = preflight.target_revision
		  AND job.capability_revision = plan.capability_revision
		  AND job.capability_revision = preflight.capability_revision
		  AND job.operation_set_digest = plan.operation_set_digest
		  AND job.operation_set_digest = preflight.operation_set_digest
		  AND job.delete_set_digest = plan.delete_set_digest
		  AND job.delete_set_digest = preflight.delete_set_digest
		  AND job.security_decision = plan.security_decision
		  AND job.security_decision_digest = plan.security_decision_digest
		  AND job.security_finding_set_digest = plan.security_finding_set_digest
		  AND job.security_finding_set_digest = preflight.finding_set_digest
		  AND job.security_policy_revision = plan.security_policy_revision
		  AND job.security_policy_revision = preflight.policy_revision
		  AND job.security_override_binding_digest = plan.security_override_binding_digest
		  AND job.estimated_items = plan.estimated_items
		  AND job.estimated_items = preflight.estimated_items
		  AND job.estimated_bytes = plan.estimated_bytes
		  AND job.estimated_bytes = preflight.estimated_bytes
		  AND job.authority_category = 'write'
		  AND authority.plan_id = job.plan_id
		  AND authority.authority_category = job.authority_category
		  AND authority.binding_digest = job.authority_binding_digest
		  AND authority.expires_at = job.authority_expires_at
		  AND authority.consumed_at = job.authority_consumed_at
		  AND authority.consumed_at IS NOT NULL
		  AND authority.revoked_at IS NULL`), aggregate.JobID).Scan(&parityCount); err != nil {
		t.Fatalf("read %s frozen recovery job authority parity: %v", fixture.engine, err)
	}
	if parityCount != 1 {
		t.Fatalf("%s frozen recovery job authority parity count=%d want 1", fixture.engine, parityCount)
	}

	for _, rewrite := range []struct {
		name string
		set  string
		args []any
	}{
		{name: "selection digest", set: "selection_digest = ?", args: []any{recoveryMigrationDigest(699901)}},
		{name: "source revision digest", set: "source_revision_digest = ?", args: []any{recoveryMigrationDigest(699902)}},
		{name: "target root binding", set: "root_locator_digest = ?", args: []any{recoveryMigrationDigest(699903)}},
		{name: "security decision digest", set: "security_decision_digest = ?", args: []any{recoveryMigrationDigest(699904)}},
		{name: "preflight expiry", set: "preflight_expires_at = ?", args: []any{now.Add(2 * time.Hour)}},
	} {
		query := fmt.Sprintf(`UPDATE backup_asset_recovery_plans SET %s WHERE id = ?`, rewrite.set)
		args := append(append([]any(nil), rewrite.args...), aggregate.PlanID)
		fixture.expectExecRejectedInRollback(t, db, query, args...)
	}

	t.Run("authorized plan binding is immutable before job creation", func(t *testing.T) {
		_, authorizedDB := fixture.openAt(t, backupAssetRecoveryVersion)
		authorizedAggregate := fixture.seedRecoveryMigrationAggregate(t, authorizedDB, "f", 129, now, true)
		fixture.removeRecoveryMigrationActiveJobGraph(t, authorizedDB, authorizedAggregate)
		fixture.mustExec(t, authorizedDB, `UPDATE backup_asset_recovery_plans
			SET state = 'authorized', transition_revision = transition_revision + 1, updated_at = ?
			WHERE id = ?`, now.Add(time.Minute), authorizedAggregate.PlanID)

		fixture.expectExecRejectedInRollback(t, authorizedDB, `UPDATE backup_asset_recovery_plans
			SET operation_set_digest = ? WHERE id = ?`, recoveryMigrationDigest(699913), authorizedAggregate.PlanID)
		fixture.mustExec(t, authorizedDB, `UPDATE backup_asset_recovery_plans
			SET state = 'draft', transition_revision = transition_revision + 1, updated_at = ?
			WHERE id = ?`, now.Add(2*time.Minute), authorizedAggregate.PlanID)
		fixture.expectExecRejectedInRollback(t, authorizedDB, `UPDATE backup_asset_recovery_plans
			SET operation_set_digest = ? WHERE id = ?`, recoveryMigrationDigest(699914), authorizedAggregate.PlanID)
	})

	for _, rewrite := range []struct {
		name string
		set  string
		args []any
	}{
		{name: "revision", set: "revision = ?", args: []any{"preflight-rewritten"}},
		{name: "source revision", set: "source_revision_digest = ?", args: []any{recoveryMigrationDigest(699905)}},
		{name: "expiry", set: "expires_at = ?", args: []any{now.Add(2 * time.Hour)}},
	} {
		query := fmt.Sprintf(`UPDATE backup_asset_recovery_preflights SET %s WHERE id = ?`, rewrite.set)
		args := append(append([]any(nil), rewrite.args...), aggregate.PreflightID)
		fixture.expectExecRejectedInRollback(t, db, query, args...)
	}

	for _, rewrite := range []struct {
		name string
		set  string
		args []any
	}{
		{name: "plan binding", set: "plan_binding_digest = ?", args: []any{recoveryMigrationDigest(699906)}},
		{name: "source revision", set: "source_revision_digest = ?", args: []any{recoveryMigrationDigest(699907)}},
		{name: "preflight revision", set: "preflight_revision = ?", args: []any{"preflight-rewritten"}},
		{name: "security decision", set: "security_decision = 'block'"},
		{name: "authority category", set: "authority_category = 'exact_mirror_delete'"},
		{name: "authority expiry", set: "authority_expires_at = ?", args: []any{now.Add(2 * time.Hour)}},
	} {
		query := fmt.Sprintf(`UPDATE backup_asset_recovery_jobs SET %s WHERE id = ?`, rewrite.set)
		args := append(append([]any(nil), rewrite.args...), aggregate.JobID)
		fixture.expectExecRejectedInRollback(t, db, query, args...)
	}

	for _, rewrite := range []struct {
		name string
		set  string
		args []any
	}{
		{name: "plan binding", set: "plan_binding_digest = ?", args: []any{recoveryMigrationDigest(699908)}},
		{name: "source revision", set: "source_revision_digest = ?", args: []any{recoveryMigrationDigest(699909)}},
		{name: "preflight expiry", set: "preflight_expires_at = ?", args: []any{now.Add(2 * time.Hour)}},
		{name: "security decision digest", set: "security_decision_digest = ?", args: []any{recoveryMigrationDigest(699910)}},
		{name: "authority binding", set: "authority_binding_digest = ?", args: []any{recoveryMigrationDigest(699911)}},
	} {
		query := fmt.Sprintf(`UPDATE backup_asset_recovery_checkpoints SET %s WHERE id = ?`, rewrite.set)
		args := append(append([]any(nil), rewrite.args...), aggregate.CheckpointID)
		fixture.expectExecRejectedInRollback(t, db, query, args...)
	}

	fixture.expectExecRejectedInRollback(t, db, `INSERT INTO backup_asset_recovery_checkpoints (
		id, job_id, attempt_id, sequence, phase, authority_category, operation_digest,
		prior_target_revision, next_target_revision, node_fence, attempt_fence,
		plan_binding_digest, source_revision_digest, preflight_id, preflight_revision,
		preflight_expires_at, security_decision, security_decision_digest,
		security_finding_set_digest, security_policy_revision, authority_grant_id,
		job_authority_category, authority_binding_digest, authority_expires_at, created_at)
		SELECT ?, job_id, attempt_id, sequence + 1, phase, authority_category, operation_digest,
			prior_target_revision, next_target_revision, node_fence, attempt_fence,
			?, source_revision_digest, preflight_id, preflight_revision,
			preflight_expires_at, security_decision, security_decision_digest,
			security_finding_set_digest, security_policy_revision, authority_grant_id,
			job_authority_category, authority_binding_digest, authority_expires_at, created_at
		FROM backup_asset_recovery_checkpoints WHERE id = ?`,
		recoveryMigrationOpaqueID(699912), recoveryMigrationDigest(699912), aggregate.CheckpointID)

	updatedAt := now.Add(time.Minute)
	fixture.mustExec(t, db, `UPDATE backup_asset_recovery_plans
		SET state = 'superseded', transition_revision = transition_revision + 1, updated_at = ?
		WHERE id = ?`, updatedAt, aggregate.PlanID)
}

func (fixture migrationFixture) test069ExactMirrorDeleteGrantConsumption(t *testing.T) {
	_, db := fixture.openAt(t, backupAssetRecoveryVersion)
	now := time.Now().UTC().Truncate(time.Second)
	aggregate := fixture.seedRecoveryMigrationExactMirrorAggregate(t, db, "d", 142, now)
	alternate := fixture.seedRecoveryMigrationExactMirrorAggregate(t, db, "e", 143, now.Add(time.Second))
	requiredCheckpointID := recoveryMigrationOpaqueID(714201)
	requiredAt := now.Add(time.Minute)
	consumeAt := now.Add(2 * time.Minute)
	authorizationExpiresAt := now.Add(20 * time.Minute)
	grantExpiresAt := now.Add(10 * time.Minute)
	deleteNodeRevision := "delete-node-revision-1"
	deleteRootRevision := "root-v1"

	var deleteSetDigest, targetRevision string
	if err := db.QueryRow(fixture.bind(`SELECT delete_set_digest, target_chain_revision
		FROM backup_asset_recovery_jobs WHERE id = ?`), aggregate.JobID).Scan(&deleteSetDigest, &targetRevision); err != nil {
		t.Fatalf("load %s exact-mirror job delete binding: %v", fixture.engine, err)
	}

	fixture.mustExec(t, db, `INSERT INTO backup_asset_recovery_checkpoints (
		id, job_id, attempt_id, sequence, phase, authority_category, operation_digest,
		prior_target_revision, next_target_revision, node_fence, attempt_fence,
		plan_binding_digest, source_revision_digest, preflight_id, preflight_revision,
		preflight_expires_at, security_decision, security_decision_digest,
		security_finding_set_digest, security_policy_revision, authority_grant_id,
		job_authority_category, authority_binding_digest, authority_expires_at,
		delete_node_revision, delete_root_revision, delete_authority_expires_at, created_at
	)
	SELECT ?, checkpoint.job_id, checkpoint.attempt_id, checkpoint.sequence + 1,
		'delete_authority_required', 'exact_mirror_delete', job.delete_set_digest,
		job.target_chain_revision, '', 1, 1,
		checkpoint.plan_binding_digest, checkpoint.source_revision_digest, checkpoint.preflight_id,
		checkpoint.preflight_revision, checkpoint.preflight_expires_at, checkpoint.security_decision,
		checkpoint.security_decision_digest, checkpoint.security_finding_set_digest,
		checkpoint.security_policy_revision, checkpoint.authority_grant_id,
		checkpoint.job_authority_category, checkpoint.authority_binding_digest,
		checkpoint.authority_expires_at, ?, ?, ?, ?
	FROM backup_asset_recovery_checkpoints AS checkpoint
	JOIN backup_asset_recovery_jobs AS job ON job.id = checkpoint.job_id
	WHERE checkpoint.id = ?`, requiredCheckpointID, deleteNodeRevision, deleteRootRevision,
		authorizationExpiresAt, requiredAt, aggregate.CheckpointID)

	// A consumed delete checkpoint must not be admitted with only the job's
	// original write authority. This probe uses the pre-fix row shape so a
	// passing insert is the genuine missing-delete-grant RED.
	fixture.expectExecRejectedInRollback(t, db, `INSERT INTO backup_asset_recovery_checkpoints (
		id, job_id, attempt_id, sequence, phase, authority_category, operation_digest,
		prior_target_revision, next_target_revision, node_fence, attempt_fence,
		plan_binding_digest, source_revision_digest, preflight_id, preflight_revision,
		preflight_expires_at, security_decision, security_decision_digest,
		security_finding_set_digest, security_policy_revision, authority_grant_id,
		job_authority_category, authority_binding_digest, authority_expires_at, created_at
	)
	SELECT ?, job_id, attempt_id, sequence + 1, 'delete_authority_consumed',
		'exact_mirror_delete', operation_digest, prior_target_revision, ?, node_fence,
		attempt_fence, plan_binding_digest, source_revision_digest, preflight_id,
		preflight_revision, preflight_expires_at, security_decision,
		security_decision_digest, security_finding_set_digest, security_policy_revision,
		authority_grant_id, job_authority_category, authority_binding_digest,
		authority_expires_at, ?
	FROM backup_asset_recovery_checkpoints WHERE id = ?`,
		recoveryMigrationOpaqueID(714202), recoveryMigrationDigest(714202), consumeAt, requiredCheckpointID)

	for _, column := range []struct {
		table  string
		column string
	}{
		{"backup_asset_recovery_grants", "delete_checkpoint_id"},
		{"backup_asset_recovery_grants", "delete_set_digest"},
		{"backup_asset_recovery_grants", "delete_target_revision"},
		{"backup_asset_recovery_grants", "delete_attempt_id"},
		{"backup_asset_recovery_grants", "delete_attempt_fence"},
		{"backup_asset_recovery_grants", "delete_node_fence"},
		{"backup_asset_recovery_checkpoints", "delete_node_revision"},
		{"backup_asset_recovery_checkpoints", "delete_root_revision"},
		{"backup_asset_recovery_checkpoints", "delete_authority_expires_at"},
		{"backup_asset_recovery_checkpoints", "delete_grant_id"},
		{"backup_asset_recovery_checkpoints", "delete_grant_binding_digest"},
		{"backup_asset_recovery_checkpoints", "delete_grant_expires_at"},
		{"backup_asset_recovery_checkpoints", "delete_grant_consumed_at"},
	} {
		if !databaseColumnExists(t, db, fixture.engine, column.table, column.column) {
			t.Errorf("%s exact-mirror consumption is missing %s.%s", fixture.engine, column.table, column.column)
		}
	}

	type deleteGrantProduct struct {
		id              string
		category        string
		jobID           string
		checkpointID    string
		deleteSetDigest string
		targetRevision  string
		attemptID       string
		attemptFence    int64
		nodeFence       int64
		bindingDigest   string
		createdAt       time.Time
		expiresAt       time.Time
	}
	type consumedCheckpointProduct struct {
		id                     string
		jobID                  string
		attemptID              string
		sequence               int
		deleteSetDigest        string
		targetRevision         string
		nextTargetRevision     string
		attemptFence           int64
		nodeFence              int64
		nodeRevision           string
		rootRevision           string
		authorizationExpiresAt time.Time
		grantID                string
		grantBindingDigest     string
		grantExpiresAt         time.Time
		grantConsumedAt        time.Time
		createdAt              time.Time
	}
	type deleteConsumptionProbe struct {
		grant       deleteGrantProduct
		checkpoint  consumedCheckpointProduct
		insertGrant bool
		consume     bool
		revoke      bool
	}

	baseGrantID := recoveryMigrationOpaqueID(714210)
	base := deleteConsumptionProbe{
		grant: deleteGrantProduct{
			id:              baseGrantID,
			category:        "exact_mirror_delete",
			jobID:           aggregate.JobID,
			checkpointID:    requiredCheckpointID,
			deleteSetDigest: deleteSetDigest,
			targetRevision:  targetRevision,
			attemptID:       aggregate.AttemptID,
			attemptFence:    1,
			nodeFence:       1,
			bindingDigest:   recoveryMigrationDigest(714210),
			createdAt:       requiredAt.Add(time.Second),
			expiresAt:       grantExpiresAt,
		},
		checkpoint: consumedCheckpointProduct{
			id:                     recoveryMigrationOpaqueID(714211),
			jobID:                  aggregate.JobID,
			attemptID:              aggregate.AttemptID,
			sequence:               2,
			deleteSetDigest:        deleteSetDigest,
			targetRevision:         targetRevision,
			nextTargetRevision:     targetRevision,
			attemptFence:           1,
			nodeFence:              1,
			nodeRevision:           deleteNodeRevision,
			rootRevision:           deleteRootRevision,
			authorizationExpiresAt: authorizationExpiresAt,
			grantID:                baseGrantID,
			grantBindingDigest:     recoveryMigrationDigest(714210),
			grantExpiresAt:         grantExpiresAt,
			grantConsumedAt:        consumeAt,
			createdAt:              consumeAt,
		},
		insertGrant: true,
		consume:     true,
	}

	insertGrant := func(tx *sql.Tx, grant deleteGrantProduct) error {
		_, err := tx.Exec(fixture.bind(`INSERT INTO backup_asset_recovery_grants (
			id, plan_id, job_id, authority_category, grant_hash, actor_user_id,
			actor_session_id, binding_digest, encrypted_reason,
			delete_checkpoint_id, delete_set_digest, delete_target_revision,
			delete_attempt_id, delete_attempt_fence, delete_node_fence,
			expires_at, created_at, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, 'delete-authority-session', ?, 'enc:v2:delete-reason',
			?, ?, ?, ?, ?, ?, ?, ?, ?)`),
			grant.id, aggregate.PlanID, grant.jobID, grant.category,
			recoveryMigrationDigest(714220), aggregate.UserID, grant.bindingDigest,
			grant.checkpointID, grant.deleteSetDigest, grant.targetRevision,
			grant.attemptID, grant.attemptFence, grant.nodeFence,
			grant.expiresAt, grant.createdAt, grant.createdAt)
		return err
	}
	insertConsumedCheckpoint := func(tx *sql.Tx, checkpoint consumedCheckpointProduct) error {
		_, err := tx.Exec(fixture.bind(`INSERT INTO backup_asset_recovery_checkpoints (
			id, job_id, attempt_id, sequence, phase, authority_category, operation_digest,
			prior_target_revision, next_target_revision, node_fence, attempt_fence,
			plan_binding_digest, source_revision_digest, preflight_id, preflight_revision,
			preflight_expires_at, security_decision, security_decision_digest,
			security_finding_set_digest, security_policy_revision, authority_grant_id,
			job_authority_category, authority_binding_digest, authority_expires_at,
			delete_node_revision, delete_root_revision, delete_authority_expires_at,
			delete_grant_id, delete_grant_binding_digest, delete_grant_expires_at,
			delete_grant_consumed_at, created_at
		)
		SELECT ?, ?, ?, ?, 'delete_authority_consumed', 'exact_mirror_delete', ?,
			?, ?, ?, ?, required.plan_binding_digest, required.source_revision_digest,
			required.preflight_id, required.preflight_revision, required.preflight_expires_at,
			required.security_decision, required.security_decision_digest,
			required.security_finding_set_digest, required.security_policy_revision,
			required.authority_grant_id, required.job_authority_category,
			required.authority_binding_digest, required.authority_expires_at,
			?, ?, ?, ?, ?, ?, ?, ?
		FROM backup_asset_recovery_checkpoints AS required WHERE required.id = ?`),
			checkpoint.id, checkpoint.jobID, checkpoint.attemptID, checkpoint.sequence,
			checkpoint.deleteSetDigest, checkpoint.targetRevision, checkpoint.nextTargetRevision,
			checkpoint.nodeFence, checkpoint.attemptFence, checkpoint.nodeRevision,
			checkpoint.rootRevision, checkpoint.authorizationExpiresAt, checkpoint.grantID,
			checkpoint.grantBindingDigest, checkpoint.grantExpiresAt,
			checkpoint.grantConsumedAt, checkpoint.createdAt, requiredCheckpointID)
		return err
	}
	runProbe := func(probe deleteConsumptionProbe, commit bool) error {
		tx, err := db.Begin()
		if err != nil {
			t.Fatalf("begin %s exact-mirror consumption probe: %v", fixture.engine, err)
		}
		finished := false
		defer func() {
			if !finished {
				_ = tx.Rollback()
			}
		}()
		if probe.insertGrant {
			err = insertGrant(tx, probe.grant)
		}
		if err == nil && probe.revoke {
			_, err = tx.Exec(fixture.bind(`UPDATE backup_asset_recovery_grants
				SET revoked_at = ?, updated_at = ? WHERE id = ?`),
				probe.checkpoint.grantConsumedAt, probe.checkpoint.grantConsumedAt, probe.grant.id)
		}
		if err == nil && probe.consume {
			_, err = tx.Exec(fixture.bind(`UPDATE backup_asset_recovery_grants
				SET consumed_at = ?, updated_at = ? WHERE id = ?`),
				probe.checkpoint.grantConsumedAt, probe.checkpoint.grantConsumedAt, probe.grant.id)
		}
		if err == nil {
			err = insertConsumedCheckpoint(tx, probe.checkpoint)
		}
		if err != nil {
			_ = tx.Rollback()
			finished = true
			return err
		}
		if commit {
			err = tx.Commit()
		} else {
			err = tx.Rollback()
		}
		finished = true
		return err
	}
	assertRejected := func(name string, probe deleteConsumptionProbe) {
		t.Helper()
		if err := runProbe(probe, false); err == nil {
			t.Fatalf("%s exact-mirror delete authority accepted %s", fixture.engine, name)
		}
	}

	missing := base
	missing.insertGrant = false
	missing.consume = false
	assertRejected("missing grant", missing)

	for index, category := range []string{"write", "security_override", "download", "cleanup"} {
		probe := base
		probe.grant.id = recoveryMigrationOpaqueID(714230 + index)
		probe.grant.category = category
		probe.checkpoint.id = recoveryMigrationOpaqueID(714240 + index)
		probe.checkpoint.grantID = probe.grant.id
		assertRejected("wrong "+category+" category", probe)
	}

	bindingMismatches := []struct {
		name   string
		mutate func(*deleteConsumptionProbe)
	}{
		{name: "wrong job", mutate: func(probe *deleteConsumptionProbe) {
			probe.grant.jobID = alternate.JobID
		}},
		{name: "wrong required checkpoint", mutate: func(probe *deleteConsumptionProbe) {
			probe.grant.checkpointID = alternate.CheckpointID
		}},
		{name: "wrong delete set", mutate: func(probe *deleteConsumptionProbe) {
			probe.grant.deleteSetDigest = recoveryMigrationDigest(714250)
		}},
		{name: "wrong attempt", mutate: func(probe *deleteConsumptionProbe) {
			probe.grant.attemptID = alternate.AttemptID
		}},
		{name: "wrong attempt fence", mutate: func(probe *deleteConsumptionProbe) {
			probe.grant.attemptFence++
		}},
		{name: "wrong node fence", mutate: func(probe *deleteConsumptionProbe) {
			probe.grant.nodeFence++
		}},
		{name: "wrong current target", mutate: func(probe *deleteConsumptionProbe) {
			probe.grant.targetRevision = recoveryMigrationDigest(714251)
		}},
		{name: "grant beyond checkpoint deadline", mutate: func(probe *deleteConsumptionProbe) {
			probe.grant.expiresAt = authorizationExpiresAt.Add(time.Second)
			probe.checkpoint.grantExpiresAt = probe.grant.expiresAt
		}},
		{name: "consumed checkpoint delete set mismatch", mutate: func(probe *deleteConsumptionProbe) {
			probe.checkpoint.deleteSetDigest = recoveryMigrationDigest(714252)
		}},
		{name: "consumed checkpoint target mismatch", mutate: func(probe *deleteConsumptionProbe) {
			probe.checkpoint.targetRevision = recoveryMigrationDigest(714253)
			probe.checkpoint.nextTargetRevision = probe.checkpoint.targetRevision
		}},
		{name: "consumed checkpoint attempt fence mismatch", mutate: func(probe *deleteConsumptionProbe) {
			probe.checkpoint.attemptFence++
		}},
		{name: "consumed checkpoint node fence mismatch", mutate: func(probe *deleteConsumptionProbe) {
			probe.checkpoint.nodeFence++
		}},
		{name: "consumed checkpoint node revision mismatch", mutate: func(probe *deleteConsumptionProbe) {
			probe.checkpoint.nodeRevision = "delete-node-revision-2"
		}},
		{name: "consumed checkpoint root revision mismatch", mutate: func(probe *deleteConsumptionProbe) {
			probe.checkpoint.rootRevision = "delete-root-revision-2"
		}},
		{name: "consumed checkpoint deadline mismatch", mutate: func(probe *deleteConsumptionProbe) {
			probe.checkpoint.authorizationExpiresAt = authorizationExpiresAt.Add(time.Second)
		}},
		{name: "consumed checkpoint grant binding mismatch", mutate: func(probe *deleteConsumptionProbe) {
			probe.checkpoint.grantBindingDigest = recoveryMigrationDigest(714254)
		}},
		{name: "consumed checkpoint grant expiry mismatch", mutate: func(probe *deleteConsumptionProbe) {
			probe.checkpoint.grantExpiresAt = probe.checkpoint.grantExpiresAt.Add(time.Second)
		}},
		{name: "consumed checkpoint grant consumption mismatch", mutate: func(probe *deleteConsumptionProbe) {
			probe.checkpoint.grantConsumedAt = probe.checkpoint.grantConsumedAt.Add(time.Second)
		}},
	}
	for _, testCase := range bindingMismatches {
		probe := base
		testCase.mutate(&probe)
		assertRejected(testCase.name, probe)
	}

	expired := base
	expired.grant.expiresAt = consumeAt.Add(-time.Second)
	expired.checkpoint.grantExpiresAt = expired.grant.expiresAt
	assertRejected("expired grant", expired)

	revoked := base
	revoked.consume = false
	revoked.revoke = true
	assertRejected("revoked grant", revoked)

	unconsumed := base
	unconsumed.consume = false
	assertRejected("unconsumed grant", unconsumed)

	if err := runProbe(base, true); err != nil {
		t.Fatalf("%s valid fresh exact-mirror delete grant was rejected: %v", fixture.engine, err)
	}

	var gotGrantID, gotBindingDigest string
	var gotGrantExpiresAt, gotGrantConsumedAt time.Time
	if err := db.QueryRow(fixture.bind(`SELECT delete_grant_id, delete_grant_binding_digest,
		delete_grant_expires_at, delete_grant_consumed_at
		FROM backup_asset_recovery_checkpoints WHERE id = ?`), base.checkpoint.id).Scan(
		&gotGrantID, &gotBindingDigest, &gotGrantExpiresAt, &gotGrantConsumedAt,
	); err != nil {
		t.Fatalf("read %s consumed exact-mirror delete checkpoint: %v", fixture.engine, err)
	}
	if gotGrantID != base.grant.id || gotBindingDigest != base.grant.bindingDigest ||
		!gotGrantExpiresAt.Equal(base.grant.expiresAt) || !gotGrantConsumedAt.Equal(base.checkpoint.grantConsumedAt) {
		t.Fatalf("%s consumed exact-mirror delete checkpoint lost grant product: id=%q binding=%q expires=%s consumed=%s",
			fixture.engine, gotGrantID, gotBindingDigest, gotGrantExpiresAt, gotGrantConsumedAt)
	}

	reused := base.checkpoint
	reused.id = recoveryMigrationOpaqueID(714299)
	reused.sequence++
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin %s reused delete-grant probe: %v", fixture.engine, err)
	}
	reuseErr := insertConsumedCheckpoint(tx, reused)
	if rollbackErr := tx.Rollback(); rollbackErr != nil {
		t.Fatalf("rollback %s reused delete-grant probe: %v", fixture.engine, rollbackErr)
	}
	if reuseErr == nil {
		t.Fatalf("%s consumed exact-mirror delete grant was reused", fixture.engine)
	}
	if definition := fixture.indexDefinition(t, db, "idx_backup_asset_recovery_checkpoints_delete_grant"); !strings.Contains(definition, "unique index") {
		t.Fatalf("%s exact-mirror delete grant one-use index is missing or not unique: %s", fixture.engine, definition)
	}

	t.Run("terminal destructive grant cannot be deleted", func(t *testing.T) {
		fixture.expectExecRejectedInRollback(t, db, `DELETE FROM backup_asset_recovery_grants WHERE id = ?`, base.grant.id)
	})
	t.Run("consumed delete checkpoint cannot be deleted", func(t *testing.T) {
		fixture.expectExecRejectedInRollback(t, db, `DELETE FROM backup_asset_recovery_checkpoints WHERE id = ?`, base.checkpoint.id)
	})
	assertDeleteReplayRejected := func(t *testing.T, name, table, id string, restore func(*sql.Tx) error) {
		t.Helper()
		tx, err := db.Begin()
		if err != nil {
			t.Fatalf("begin %s %s delete/replay probe: %v", fixture.engine, name, err)
		}
		_, replayErr := tx.Exec(fixture.bind(fmt.Sprintf(`DELETE FROM %s WHERE id = ?`, table)), id)
		if replayErr == nil {
			replayErr = restore(tx)
		}
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			t.Fatalf("rollback %s %s delete/replay probe: %v", fixture.engine, name, rollbackErr)
		}
		if replayErr == nil {
			t.Fatalf("%s terminal %s was deleted and replayed", fixture.engine, name)
		}
	}
	t.Run("same secret and ID terminal grant replay is rejected", func(t *testing.T) {
		assertDeleteReplayRejected(t, "grant", "backup_asset_recovery_grants", base.grant.id, func(tx *sql.Tx) error {
			return insertGrant(tx, base.grant)
		})
	})
	t.Run("same ID consumed checkpoint replay is rejected", func(t *testing.T) {
		assertDeleteReplayRejected(t, "consumed checkpoint", "backup_asset_recovery_checkpoints", base.checkpoint.id, func(tx *sql.Tx) error {
			return insertConsumedCheckpoint(tx, base.checkpoint)
		})
	})
	t.Run("revoked destructive grant cannot be deleted", func(t *testing.T) {
		revokedGrant := base.grant
		revokedGrant.id = recoveryMigrationOpaqueID(714212)
		revokedGrant.bindingDigest = recoveryMigrationDigest(714212)
		revokedGrant.createdAt = base.grant.createdAt.Add(time.Second)
		tx, err := db.Begin()
		if err != nil {
			t.Fatalf("begin %s revoked destructive-grant delete probe: %v", fixture.engine, err)
		}
		probeErr := insertGrant(tx, revokedGrant)
		if probeErr == nil {
			_, probeErr = tx.Exec(fixture.bind(`UPDATE backup_asset_recovery_grants
				SET revoked_at = ?, updated_at = ? WHERE id = ?`), revokedGrant.createdAt, revokedGrant.createdAt, revokedGrant.id)
		}
		if probeErr == nil {
			_, probeErr = tx.Exec(fixture.bind(`DELETE FROM backup_asset_recovery_grants WHERE id = ?`), revokedGrant.id)
		}
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			t.Fatalf("rollback %s revoked destructive-grant delete probe: %v", fixture.engine, rollbackErr)
		}
		if probeErr == nil {
			t.Fatalf("%s revoked destructive grant was deleted", fixture.engine)
		}
	})
	if fixture.engine == "sqlite" {
		t.Run("INSERT OR REPLACE cannot rebuild terminal grant", func(t *testing.T) {
			fixture.expectExecRejectedInRollback(t, db, `INSERT OR REPLACE INTO backup_asset_recovery_grants (
				id, plan_id, job_id, authority_category, grant_hash, actor_user_id, actor_session_id,
				binding_digest, encrypted_reason, delete_checkpoint_id, delete_set_digest,
				delete_target_revision, delete_attempt_id, delete_attempt_fence, delete_node_fence,
				expires_at, consumed_at, revoked_at, created_at, updated_at
			)
			SELECT id, plan_id, job_id, authority_category, grant_hash, actor_user_id, actor_session_id,
				binding_digest, encrypted_reason, delete_checkpoint_id, delete_set_digest,
				delete_target_revision, delete_attempt_id, delete_attempt_fence, delete_node_fence,
				expires_at, consumed_at, revoked_at, created_at, updated_at
			FROM backup_asset_recovery_grants WHERE id = ?`, base.grant.id)
		})
		t.Run("INSERT OR REPLACE cannot rebuild consumed checkpoint", func(t *testing.T) {
			fixture.expectExecRejectedInRollback(t, db, `INSERT OR REPLACE INTO backup_asset_recovery_checkpoints (
				id, job_id, attempt_id, sequence, phase, authority_category, operation_digest,
				prior_target_revision, next_target_revision, node_fence, attempt_fence,
				plan_binding_digest, source_revision_digest, preflight_id, preflight_revision,
				preflight_expires_at, security_decision, security_decision_digest,
				security_finding_set_digest, security_policy_revision, authority_grant_id,
				job_authority_category, authority_binding_digest, authority_expires_at,
				delete_node_revision, delete_root_revision, delete_authority_expires_at,
				delete_grant_id, delete_grant_binding_digest, delete_grant_expires_at,
				delete_grant_consumed_at, created_at
			)
			SELECT id, job_id, attempt_id, sequence, phase, authority_category, operation_digest,
				prior_target_revision, next_target_revision, node_fence, attempt_fence,
				plan_binding_digest, source_revision_digest, preflight_id, preflight_revision,
				preflight_expires_at, security_decision, security_decision_digest,
				security_finding_set_digest, security_policy_revision, authority_grant_id,
				job_authority_category, authority_binding_digest, authority_expires_at,
				delete_node_revision, delete_root_revision, delete_authority_expires_at,
				delete_grant_id, delete_grant_binding_digest, delete_grant_expires_at,
				delete_grant_consumed_at, created_at
			FROM backup_asset_recovery_checkpoints WHERE id = ?`, base.checkpoint.id)
		})
	}
}

func (fixture migrationFixture) test069JobAndResultSetTransitionMatrix(t *testing.T) {
	t.Run("ready cannot skip revoking", func(t *testing.T) {
		_, db := fixture.openAt(t, backupAssetRecoveryVersion)
		now := time.Now().UTC().Truncate(time.Second)
		aggregate := fixture.seedRecoveryMigrationAggregate(t, db, "e", 143, now)
		fixture.expectExecRejectedInRollback(t, db, `UPDATE backup_asset_recovery_result_sets
			SET state = 'cleaned', cleanup_phase = 'tombstoned', cleanup_owner = '',
				cleanup_lease_expires_at = NULL, cleanup_fence = 1, node_lease_id = NULL,
				node_fence = 0, cleanup_attempt = 1, updated_at = ? WHERE id = ?`,
			now.Add(time.Minute), aggregate.ResultSetID)
	})

	t.Run("terminal job cannot reopen without a result set", func(t *testing.T) {
		_, db := fixture.openAt(t, backupAssetRecoveryVersion)
		now := time.Now().UTC().Truncate(time.Second)
		aggregate := fixture.seedRecoveryMigrationAggregate(t, db, "f", 144, now, true)
		closedAt := now.Add(time.Minute)
		fixture.mustExec(t, db, `UPDATE backup_asset_recovery_attempts
			SET state = 'completed', mutation_armed = ?, closed_at = ?, updated_at = ? WHERE id = ?`,
			false, closedAt, closedAt, aggregate.AttemptID)
		fixture.mustExec(t, db, `UPDATE backup_asset_recovery_jobs
			SET state = 'verifying', workspace_phase = 'sealed', transition_revision = transition_revision + 1,
				updated_at = ? WHERE id = ?`, now.Add(2*time.Minute), aggregate.JobID)
		fixture.mustExec(t, db, `UPDATE backup_asset_recovery_jobs
			SET state = 'succeeded', transition_revision = transition_revision + 1, updated_at = ? WHERE id = ?`,
			now.Add(3*time.Minute), aggregate.JobID)
		fixture.expectExecRejectedInRollback(t, db, `UPDATE backup_asset_recovery_jobs
			SET state = 'running', workspace_phase = 'writing', transition_revision = transition_revision + 1,
				updated_at = ? WHERE id = ?`, now.Add(4*time.Minute), aggregate.JobID)
	})

	t.Run("legal ready revoking cleaned path and no-op timestamps", func(t *testing.T) {
		_, db := fixture.openAt(t, backupAssetRecoveryVersion)
		now := time.Now().UTC().Truncate(time.Second)
		aggregate := fixture.seedRecoveryMigrationAggregate(t, db, "a", 145, now)
		fixture.mustExec(t, db, `UPDATE backup_asset_recovery_result_sets SET updated_at = ? WHERE id = ?`, now.Add(time.Second), aggregate.ResultSetID)
		fixture.mustExec(t, db, `UPDATE backup_asset_recovery_result_sets
			SET state = 'revoking', cleanup_owner = 'cleanup-worker', cleanup_lease_expires_at = ?,
				cleanup_fence = 1, node_lease_id = ?, node_fence = 1, cleanup_attempt = 1,
				updated_at = ? WHERE id = ?`, now.Add(30*time.Minute), aggregate.NodeLeaseID,
			now.Add(2*time.Minute), aggregate.ResultSetID)
		fixture.mustExec(t, db, `UPDATE backup_asset_recovery_result_sets
			SET state = 'cleaned', cleanup_phase = 'tombstoned', cleanup_owner = '',
				cleanup_lease_expires_at = NULL, node_lease_id = NULL, node_fence = 0,
				updated_at = ? WHERE id = ?`, now.Add(3*time.Minute), aggregate.ResultSetID)
	})
}

func (fixture migrationFixture) test069TerminalJobAttemptBarrier(t *testing.T) {
	terminalize := func(t *testing.T, db *sql.DB, now time.Time, aggregate recoveryMigrationAggregate, terminalState string, reject bool) {
		t.Helper()
		workspacePhase := "cleanup_due"
		switch terminalState {
		case "succeeded", "degraded":
			workspacePhase = "sealed"
			fixture.mustExec(t, db, `UPDATE backup_asset_recovery_jobs
				SET state = 'verifying', workspace_phase = ?, transition_revision = transition_revision + 1,
					updated_at = ? WHERE id = ?`, workspacePhase, now.Add(time.Minute), aggregate.JobID)
		case "canceled":
			fixture.mustExec(t, db, `UPDATE backup_asset_recovery_jobs
				SET state = 'cancel_requested', workspace_phase = ?, transition_revision = transition_revision + 1,
					updated_at = ? WHERE id = ?`, workspacePhase, now.Add(time.Minute), aggregate.JobID)
		}

		query := `UPDATE backup_asset_recovery_jobs
			SET state = ?, workspace_phase = ?, transition_revision = transition_revision + 1,
				updated_at = ? WHERE id = ?`
		if reject {
			fixture.expectExecRejectedInRollback(t, db, query, terminalState, workspacePhase, now.Add(2*time.Minute), aggregate.JobID)
			return
		}
		fixture.mustExec(t, db, query, terminalState, workspacePhase, now.Add(2*time.Minute), aggregate.JobID)
	}

	terminalStates := []string{"succeeded", "degraded", "failed", "needs_attention", "canceled"}
	for index, terminalState := range terminalStates {
		t.Run("active attempt blocks terminal job/"+terminalState, func(t *testing.T) {
			_, db := fixture.openAt(t, backupAssetRecoveryVersion)
			now := time.Now().UTC().Truncate(time.Second)
			aggregate := fixture.seedRecoveryMigrationAggregate(t, db, fmt.Sprintf("%x", index+1), 146+index, now, true)
			terminalize(t, db, now, aggregate, terminalState, true)
		})

		t.Run("terminal job blocks fresh active attempts/"+terminalState, func(t *testing.T) {
			_, db := fixture.openAt(t, backupAssetRecoveryVersion)
			now := time.Now().UTC().Truncate(time.Second)
			aggregate := fixture.seedRecoveryMigrationAggregate(t, db, fmt.Sprintf("%x", index+9), 151+index, now, true)
			closedAt := now.Add(30 * time.Second)
			fixture.mustExec(t, db, `UPDATE backup_asset_recovery_attempts
				SET state = 'completed', mutation_armed = ?, closed_at = ?, updated_at = ? WHERE id = ?`,
				false, closedAt, closedAt, aggregate.AttemptID)
			terminalize(t, db, now, aggregate, terminalState, false)

			for attemptIndex, attemptState := range []string{"claimed", "running"} {
				fixture.expectExecRejectedInRollback(t, db, `INSERT INTO backup_asset_recovery_attempts
					(id, job_id, owner_id, fence, state, mutation_armed, lease_expires_at, heartbeat_at,
					 created_at, updated_at)
					VALUES (?, ?, 'late-worker', 2, ?, ?, ?, ?, ?, ?)`,
					recoveryMigrationOpaqueID(714300+index*10+attemptIndex), aggregate.JobID, attemptState, false,
					now.Add(30*time.Minute), now, now, now)
			}
		})
	}
}

func (fixture migrationFixture) test069InitialWorkerClaimHandoff(t *testing.T) {
	_, db := fixture.openAt(t, backupAssetRecoveryVersion)
	now := time.Now().UTC().Truncate(time.Second)
	aggregate := fixture.seedRecoveryMigrationAggregateWithOptions(
		t,
		db,
		"6",
		156,
		now,
		recoveryMigrationSeedOptions{claimableAttempt: true},
	)

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin %s initial recovery worker claim: %v", fixture.engine, err)
	}
	if _, err := tx.Exec(fixture.bind(`UPDATE backup_asset_recovery_node_leases
		SET owner_id = 'recovery-worker-a', updated_at = ?
		WHERE id = ? AND owner_id = 'recovery-authorization' AND state = 'active'`),
		now.Add(time.Second), aggregate.NodeLeaseID); err != nil {
		_ = tx.Rollback()
		t.Fatalf("bind %s initial node-lease owner: %v", fixture.engine, err)
	}
	if _, err := tx.Exec(fixture.bind(`UPDATE backup_asset_recovery_attempts
		SET owner_id = 'recovery-worker-a', state = 'running', heartbeat_at = ?, updated_at = ?
		WHERE id = ? AND owner_id = 'recovery-authorization' AND state = 'claimed'`),
		now.Add(time.Second), now.Add(time.Second), aggregate.AttemptID); err != nil {
		_ = tx.Rollback()
		t.Fatalf("bind %s initial attempt owner: %v", fixture.engine, err)
	}
	if _, err := tx.Exec(fixture.bind(`UPDATE backup_asset_recovery_jobs
		SET state = 'running', transition_revision = transition_revision + 1, updated_at = ?
		WHERE id = ? AND state = 'queued' AND transition_revision = 1`),
		now.Add(time.Second), aggregate.JobID); err != nil {
		_ = tx.Rollback()
		t.Fatalf("start %s initially claimed job: %v", fixture.engine, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit %s initial recovery worker claim: %v", fixture.engine, err)
	}

	var attemptOwner, attemptState, nodeOwner, jobState string
	var revision uint64
	if err := db.QueryRow(fixture.bind(`SELECT owner_id, state FROM backup_asset_recovery_attempts WHERE id = ?`),
		aggregate.AttemptID).Scan(&attemptOwner, &attemptState); err != nil {
		t.Fatalf("read %s claimed attempt: %v", fixture.engine, err)
	}
	if err := db.QueryRow(fixture.bind(`SELECT owner_id FROM backup_asset_recovery_node_leases WHERE id = ?`),
		aggregate.NodeLeaseID).Scan(&nodeOwner); err != nil {
		t.Fatalf("read %s claimed node lease: %v", fixture.engine, err)
	}
	if err := db.QueryRow(fixture.bind(`SELECT state, transition_revision FROM backup_asset_recovery_jobs WHERE id = ?`),
		aggregate.JobID).Scan(&jobState, &revision); err != nil {
		t.Fatalf("read %s claimed job: %v", fixture.engine, err)
	}
	if attemptOwner != "recovery-worker-a" || attemptState != "running" || nodeOwner != attemptOwner ||
		jobState != "running" || revision != 2 {
		t.Fatalf("%s initial claim owner/state mismatch: attempt=%q/%q node=%q job=%q revision=%d",
			fixture.engine, attemptOwner, attemptState, nodeOwner, jobState, revision)
	}

	fixture.expectExecRejectedInRollback(t, db, `UPDATE backup_asset_recovery_attempts
		SET owner_id = 'recovery-worker-b', updated_at = ? WHERE id = ?`, now.Add(2*time.Second), aggregate.AttemptID)
}

func (fixture migrationFixture) test069WorkerClaimHeartbeatAndTakeover(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_MIGRATION_RECOVERY_WORKER_STATE_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)

	_, sqlDB := fixture.openAt(t, backupAssetRecoveryVersion)
	db := fixture.recoveryWorkerGorm(t, sqlDB)
	now := time.Now().UTC().Truncate(time.Second)
	aggregate := fixture.seedRecoveryMigrationAggregateWithOptions(
		t,
		sqlDB,
		"d",
		170,
		now,
		recoveryMigrationSeedOptions{claimableAttempt: true, decryptableWorkspace: true},
	)
	sourceLeaseID := recoveryMigrationOpaqueID(890001)
	fixture.mustExec(t, sqlDB, `INSERT INTO recovery_point_leases
		(id, recovery_point_id, holder_type, owner_id, attempt_id, fence_token, status,
		 lease_expires_at, absolute_deadline, last_heartbeat_at, created_at, updated_at)
		VALUES (?, ?, 'recovery_job', ?, ?, ?, 'active', ?, ?, ?, ?, ?)`,
		sourceLeaseID,
		aggregate.PointID,
		aggregate.JobID,
		aggregate.AttemptID,
		recoveryMigrationDigest(890002),
		now.Add(30*time.Minute),
		now.Add(2*time.Hour),
		now,
		now,
		now,
	)

	clock := now.Add(time.Minute)
	sourceLeases, err := backupasset.NewLeaseService(
		db,
		func() time.Time { return clock },
		backupasset.LeaseConfig{Duration: 10 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: 2 * time.Hour},
	)
	if err != nil {
		t.Fatalf("create %s real-schema source-lease service: %v", fixture.engine, err)
	}
	coordinator, err := recovery.NewWorkerCoordinator(recovery.WorkerCoordinatorDependencies{
		DB: db, SourceLeases: sourceLeases, Now: func() time.Time { return clock },
		SourceResolver: recoveryMigrationSourceResolver{}, LeaseTTL: 10 * time.Minute, ScanLimit: 8,
	})
	if err != nil {
		t.Fatalf("create %s real-schema worker coordinator: %v", fixture.engine, err)
	}

	claim, found, err := coordinator.ClaimNext(context.Background(), "recovery-worker-a")
	if err != nil || !found {
		t.Fatalf("claim %s real-schema recovery job: found=%t err=%v", fixture.engine, found, err)
	}
	if claim.JobID != aggregate.JobID || claim.AttemptID != aggregate.AttemptID ||
		claim.NodeLeaseID != aggregate.NodeLeaseID || claim.WorkerID != "recovery-worker-a" ||
		claim.AttemptFence != 1 || claim.NodeFence != 1 || claim.TransitionRevision != 2 ||
		claim.SourceFence.LeaseID != sourceLeaseID {
		t.Fatalf("unexpected %s real-schema worker claim: %+v", fixture.engine, claim)
	}
	if second, secondFound, secondErr := coordinator.ClaimNext(context.Background(), "recovery-worker-b"); secondErr != nil || secondFound || second.JobID != "" {
		t.Fatalf("second %s worker acquired claimed job: claim=%+v found=%t err=%v",
			fixture.engine, second, secondFound, secondErr)
	}
	if premature, prematureFound, prematureErr := coordinator.TakeoverExpired(context.Background(), "recovery-worker-b"); prematureErr != nil || prematureFound || premature.JobID != "" {
		t.Fatalf("%s active worker was taken over: claim=%+v found=%t err=%v",
			fixture.engine, premature, prematureFound, prematureErr)
	}

	clock = clock.Add(time.Minute)
	heartbeat, err := coordinator.Heartbeat(context.Background(), claim)
	if err != nil {
		t.Fatalf("heartbeat %s real-schema worker: %v", fixture.engine, err)
	}
	if !heartbeat.LeaseExpiresAt.After(claim.LeaseExpiresAt) || heartbeat.SourceFence != claim.SourceFence {
		t.Fatalf("unexpected %s heartbeat renewal: before=%+v after=%+v", fixture.engine, claim, heartbeat)
	}

	clock = heartbeat.LeaseExpiresAt.Add(time.Second)
	takeover, takeoverFound, takeoverErr := coordinator.TakeoverExpired(context.Background(), "recovery-worker-b")
	if takeoverErr != nil || !takeoverFound {
		t.Fatalf("take over expired %s real-schema recovery job: found=%t err=%v",
			fixture.engine, takeoverFound, takeoverErr)
	}
	if takeover.JobID != claim.JobID || takeover.AttemptID == claim.AttemptID ||
		takeover.AttemptFence != claim.AttemptFence+1 || takeover.NodeFence != claim.NodeFence+1 ||
		takeover.WorkerID != "recovery-worker-b" || takeover.TransitionRevision != heartbeat.TransitionRevision+1 ||
		takeover.SourceFence.LeaseID != claim.SourceFence.LeaseID ||
		takeover.SourceFence.FenceToken == claim.SourceFence.FenceToken {
		t.Fatalf("unexpected %s real-schema takeover: first=%+v takeover=%+v", fixture.engine, heartbeat, takeover)
	}
	if _, err := coordinator.Heartbeat(context.Background(), heartbeat); !errors.Is(err, recovery.ErrRecoveryWorkerFenceLost) {
		t.Fatalf("old %s worker heartbeat after takeover error=%v", fixture.engine, err)
	}

	var oldAttempt, newAttempt model.BackupAssetRecoveryAttempt
	if err := db.Where("id = ?", claim.AttemptID).Take(&oldAttempt).Error; err != nil {
		t.Fatalf("load %s old worker attempt: %v", fixture.engine, err)
	}
	if err := db.Where("id = ?", takeover.AttemptID).Take(&newAttempt).Error; err != nil {
		t.Fatalf("load %s takeover attempt: %v", fixture.engine, err)
	}
	if oldAttempt.State != string(recovery.AttemptStateLost) || oldAttempt.ClosedAt == nil || oldAttempt.MutationArmed ||
		newAttempt.State != string(recovery.AttemptStateRunning) || newAttempt.OwnerID != takeover.WorkerID ||
		newAttempt.Fence != takeover.AttemptFence || newAttempt.MutationArmed {
		t.Fatalf("%s real-schema attempt handoff mismatch: old=%+v new=%+v", fixture.engine, oldAttempt, newAttempt)
	}
	var nodeLease model.BackupAssetRecoveryNodeLease
	if err := db.Where("id = ?", aggregate.NodeLeaseID).Take(&nodeLease).Error; err != nil {
		t.Fatalf("load %s takeover node lease: %v", fixture.engine, err)
	}
	if nodeLease.AttemptID == nil || *nodeLease.AttemptID != takeover.AttemptID ||
		nodeLease.OwnerID != takeover.WorkerID || nodeLease.Fence != takeover.NodeFence || nodeLease.State != "active" {
		t.Fatalf("%s real-schema node takeover mismatch: %+v", fixture.engine, nodeLease)
	}
	var sourceLease model.RecoveryPointLease
	if err := db.Where("id = ?", sourceLeaseID).Take(&sourceLease).Error; err != nil {
		t.Fatalf("load %s takeover source lease: %v", fixture.engine, err)
	}
	if sourceLease.AttemptID != takeover.AttemptID || sourceLease.FenceToken != takeover.SourceFence.FenceToken ||
		sourceLease.Status != string(backupasset.LeaseActive) {
		t.Fatalf("%s real-schema source takeover mismatch: %+v", fixture.engine, sourceLease)
	}
}

func (fixture migrationFixture) recoveryWorkerGorm(t *testing.T, sqlDB *sql.DB) *gorm.DB {
	t.Helper()
	var dialector gorm.Dialector
	if fixture.engine == "sqlite" {
		dialector = sqlitegorm.New(sqlitegorm.Config{Conn: sqlDB})
	} else {
		dialector = postgresgorm.New(postgresgorm.Config{Conn: sqlDB})
	}
	db, err := gorm.Open(dialector, &gorm.Config{NowFunc: func() time.Time { return time.Now().UTC() }})
	if err != nil {
		t.Fatalf("open %s real-schema recovery worker GORM DB: %v", fixture.engine, err)
	}
	return db
}

type recoveryMigrationFirstWriteLiveRevalidator struct{}

func (recoveryMigrationFirstWriteLiveRevalidator) ObserveRecoveryAuthority(
	context.Context,
	recovery.RecoveryAuthorityBinding,
) (recovery.RecoveryAuthorityObservation, error) {
	return recovery.RecoveryAuthorityObservation{}, nil
}

func (recoveryMigrationFirstWriteLiveRevalidator) RevalidateRecoveryAuthorityTx(
	context.Context,
	*gorm.DB,
	recovery.RecoveryAuthorityBinding,
	recovery.RecoveryAuthorityObservation,
) error {
	return nil
}

type recoveryMigrationFirstWriteAuthorityDriftRevalidator struct{}

func (recoveryMigrationFirstWriteAuthorityDriftRevalidator) ObserveRecoveryAuthority(
	context.Context,
	recovery.RecoveryAuthorityBinding,
) (recovery.RecoveryAuthorityObservation, error) {
	return recovery.RecoveryAuthorityObservation{}, nil
}

func (recoveryMigrationFirstWriteAuthorityDriftRevalidator) RevalidateRecoveryAuthorityTx(
	context.Context,
	*gorm.DB,
	recovery.RecoveryAuthorityBinding,
	recovery.RecoveryAuthorityObservation,
) error {
	return recovery.ErrAuthorizationDenied
}

type recoveryMigrationFirstWriteWorkspaceKeySource struct{}

func (recoveryMigrationFirstWriteWorkspaceKeySource) Active(
	_ context.Context,
	domain backupasset.KeyDomain,
) (backupasset.DomainKeyMaterial, error) {
	if domain != backupasset.KeyDomainRecoveryCleanupOwnership {
		return backupasset.DomainKeyMaterial{}, fmt.Errorf("unexpected recovery workspace key domain %q", domain)
	}
	return backupasset.DomainKeyMaterial{
		Domain: domain, State: backupasset.DomainKeyActive, Version: 1,
		Key: []byte(strings.Repeat("w", 32)),
	}, nil
}

func (recoveryMigrationFirstWriteWorkspaceKeySource) ByVersion(
	ctx context.Context,
	domain backupasset.KeyDomain,
	version int,
) (backupasset.DomainKeyMaterial, error) {
	material, err := (recoveryMigrationFirstWriteWorkspaceKeySource{}).Active(ctx, domain)
	if err != nil || material.Version != version {
		return backupasset.DomainKeyMaterial{}, backupasset.ErrKeyUnavailable
	}
	return material, nil
}

func (fixture migrationFixture) test069WorkerFirstWrite(t *testing.T) {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_MIGRATION_RECOVERY_FIRST_WRITE_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)

	_, sqlDB := fixture.openAt(t, backupAssetRecoveryVersion)
	db := fixture.recoveryWorkerGorm(t, sqlDB)
	now := time.Now().UTC().Truncate(time.Second)
	aggregate := fixture.seedRecoveryMigrationAggregateWithOptions(
		t,
		sqlDB,
		"e",
		174,
		now,
		recoveryMigrationSeedOptions{claimableAttempt: true, firstWrite: true},
	)
	sourceLeaseID := recoveryMigrationOpaqueID(899001)
	fixture.mustExec(t, sqlDB, `INSERT INTO recovery_point_leases
		(id, recovery_point_id, holder_type, owner_id, attempt_id, fence_token, status,
		 lease_expires_at, absolute_deadline, last_heartbeat_at, created_at, updated_at)
		VALUES (?, ?, 'recovery_job', ?, ?, ?, 'active', ?, ?, ?, ?, ?)`,
		sourceLeaseID,
		aggregate.PointID,
		aggregate.JobID,
		aggregate.AttemptID,
		recoveryMigrationDigest(899002),
		now.Add(30*time.Minute),
		now.Add(2*time.Hour),
		now,
		now,
		now,
	)

	clock := now.Add(time.Minute)
	sourceLeases, err := backupasset.NewLeaseService(
		db,
		func() time.Time { return clock },
		backupasset.LeaseConfig{Duration: 10 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: 2 * time.Hour},
	)
	if err != nil {
		t.Fatalf("create %s first-write source lease service: %v", fixture.engine, err)
	}
	coordinator, err := recovery.NewWorkerCoordinator(recovery.WorkerCoordinatorDependencies{
		DB:              db,
		SourceLeases:    sourceLeases,
		LiveRevalidator: recoveryMigrationFirstWriteLiveRevalidator{},
		WorkspaceKeys:   recoveryMigrationFirstWriteWorkspaceKeySource{},
		SourceResolver:  recoveryMigrationSourceResolver{},
		Now:             func() time.Time { return clock },
		LeaseTTL:        10 * time.Minute,
		ScanLimit:       8,
	})
	if err != nil {
		t.Fatalf("create %s first-write worker coordinator: %v", fixture.engine, err)
	}

	claim, found, err := coordinator.ClaimNext(context.Background(), "recovery-worker-a")
	if err != nil || !found {
		t.Fatalf("claim %s first-write recovery job: found=%t err=%v", fixture.engine, found, err)
	}
	permit, err := coordinator.PrepareFirstWrite(context.Background(), claim)
	if err != nil {
		t.Fatalf("prepare %s first-write boundary: %v", fixture.engine, err)
	}
	if err := permit.ValidateAt(clock); err != nil {
		t.Fatalf("validate %s first-write permit: %v", fixture.engine, err)
	}
	replay, err := coordinator.PrepareFirstWrite(context.Background(), claim)
	if err != nil {
		t.Fatalf("replay %s first-write boundary: %v", fixture.engine, err)
	}
	if err := replay.ValidateAt(clock); err != nil {
		t.Fatalf("validate replayed %s first-write permit: %v", fixture.engine, err)
	}

	const latchID = "00000000000000000000000000000069"
	var latchCount, checkpointCount int
	if err := sqlDB.QueryRow(fixture.bind(`SELECT COUNT(*) FROM backup_asset_recovery_evidence
		WHERE id = ? AND kind = ?`), latchID, recovery.RecoverySchemaUseLatchID).Scan(&latchCount); err != nil {
		t.Fatalf("count %s first-write latch: %v", fixture.engine, err)
	}
	if err := sqlDB.QueryRow(fixture.bind(`SELECT COUNT(*) FROM backup_asset_recovery_checkpoints
		WHERE job_id = ? AND sequence = 0 AND phase = 'workspace_reserved'`), aggregate.JobID).Scan(&checkpointCount); err != nil {
		t.Fatalf("count %s first-write checkpoint: %v", fixture.engine, err)
	}
	if latchCount != 1 || checkpointCount != 1 {
		t.Fatalf("%s first-write boundary left latch/checkpoints=%d/%d, want 1/1", fixture.engine, latchCount, checkpointCount)
	}

	var job model.BackupAssetRecoveryJob
	if err := db.Where("id = ?", aggregate.JobID).Take(&job).Error; err != nil {
		t.Fatalf("load %s first-write job: %v", fixture.engine, err)
	}
	if job.WorkspacePhase != string(recovery.WorkspacePhaseReserved) || job.EncryptedWorkspaceRelativeLocator == "" ||
		job.WorkspaceMarkerBindingDigest == "" || job.WorkspaceOwner != claim.WorkerID ||
		job.WorkspaceFence != claim.AttemptFence || job.PlaintextDeadline == nil || !job.PlaintextDeadline.After(clock) {
		t.Fatalf("%s first-write reservation is incomplete: %+v", fixture.engine, job)
	}
	var storedWorkspaceLocator string
	if err := sqlDB.QueryRow(fixture.bind(`SELECT encrypted_workspace_relative_locator
		FROM backup_asset_recovery_jobs WHERE id = ?`), aggregate.JobID).Scan(&storedWorkspaceLocator); err != nil {
		t.Fatalf("load %s stored workspace locator: %v", fixture.engine, err)
	}
	if !secure.IsEncrypted(storedWorkspaceLocator) || storedWorkspaceLocator == job.EncryptedWorkspaceRelativeLocator {
		t.Fatalf("%s workspace locator is not encrypted at rest: %q", fixture.engine, storedWorkspaceLocator)
	}
	fixture.expectExecRejectedInRollback(t, sqlDB, `UPDATE backup_asset_recovery_jobs
		SET plaintext_deadline = ? WHERE id = ?`, job.PlaintextDeadline.Add(time.Minute), aggregate.JobID)

	var attempt model.BackupAssetRecoveryAttempt
	if err := db.Where("id = ?", claim.AttemptID).Take(&attempt).Error; err != nil {
		t.Fatalf("load %s first-write attempt: %v", fixture.engine, err)
	}
	if !attempt.MutationArmed || attempt.State != string(recovery.AttemptStateRunning) {
		t.Fatalf("%s first-write attempt was not durably armed: %+v", fixture.engine, attempt)
	}

	fixture.mustExec(t, sqlDB, `UPDATE recovery_point_leases
		SET status = 'released', released_at = ?, updated_at = ? WHERE id = ?`, clock, clock, sourceLeaseID)
	if err := permit.ValidateAt(clock); !errors.Is(err, recovery.ErrInvalidTargetPermit) {
		t.Fatalf("%s first-write permit survived source-fence loss: %v", fixture.engine, err)
	}
}

func (fixture migrationFixture) test069WorkerEvidenceBindings(t *testing.T) {
	_, db := fixture.openAt(t, backupAssetRecoveryVersion)
	now := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	verifiedAt := now.Add(time.Second)
	aggregate := fixture.seedRecoveryMigrationAggregate(t, db, "f", 175, now, true)
	sourceLeaseID := recoveryMigrationOpaqueID(899101)
	fixture.mustExec(t, db, `INSERT INTO recovery_point_leases
		(id, recovery_point_id, holder_type, owner_id, attempt_id, fence_token, status,
		 lease_expires_at, absolute_deadline, last_heartbeat_at, created_at, updated_at)
		VALUES (?, ?, 'recovery_job', ?, ?, ?, 'active', ?, ?, ?, ?, ?)`,
		sourceLeaseID, aggregate.PointID, aggregate.JobID, aggregate.AttemptID,
		recoveryMigrationDigest(899102), now.Add(time.Hour), now.Add(2*time.Hour), now, now, now,
	)
	for index, evidence := range []struct {
		kind, outcome string
		differences   int64
	}{
		{kind: "verification", outcome: "succeeded"},
		{kind: "difference", outcome: "needs_attention", differences: 1},
	} {
		fixture.mustExec(t, db, `INSERT INTO backup_asset_recovery_evidence
			(id, job_id, kind, outcome, summary_digest, difference_count, verified_at,
			 plan_id, checkpoint_id, grant_id, attempt_id, source_lease_id, node_lease_id,
			 node_lease_fence, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)`,
			recoveryMigrationOpaqueID(899110+index), aggregate.JobID, evidence.kind, evidence.outcome,
			recoveryMigrationDigest(899120+index), evidence.differences, verifiedAt,
			aggregate.PlanID, aggregate.CheckpointID, aggregate.WriteGrantID, aggregate.AttemptID,
			sourceLeaseID, aggregate.NodeLeaseID, now, now,
		)
	}

	failureEvidenceInsert := `INSERT INTO backup_asset_recovery_evidence
		(id, job_id, kind, outcome, summary_digest, difference_count, verified_at,
		 plan_id, checkpoint_id, grant_id, attempt_id, source_lease_id, node_lease_id,
		 node_lease_fence, created_at, updated_at)
		VALUES (?, ?, 'failure', 'needs_attention', ?, 0, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	failureEvidenceArgs := func(id, checkpointID, boundSourceLeaseID string, fence int64, createdAt time.Time) []any {
		t.Helper()
		return []any{
			id, aggregate.JobID, recoveryMigrationDigest(899131), verifiedAt,
			aggregate.PlanID, checkpointID, aggregate.WriteGrantID, aggregate.AttemptID,
			boundSourceLeaseID, aggregate.NodeLeaseID, fence, createdAt, verifiedAt,
		}
	}
	fixture.expectExecRejectedInRollback(
		t, db, failureEvidenceInsert, failureEvidenceArgs(
			recoveryMigrationOpaqueID(899130), aggregate.CheckpointID, sourceLeaseID, 1, now,
		)...,
	)

	fixture.mustExec(t, db, `UPDATE backup_asset_recovery_attempts
		SET mutation_armed = ?, updated_at = ? WHERE id = ?`, true, verifiedAt, aggregate.AttemptID)
	unresolvedInsert := `INSERT INTO backup_asset_recovery_checkpoints
		(id, job_id, job_item_id, attempt_id, sequence, phase, authority_category,
		 operation_digest, prior_target_revision, next_target_revision,
		 unresolved_category, write_result_digest, write_target_revision,
		 observation_digest, observed_target_revision, observed_presence,
		 source_revalidation_outcome, node_fence, attempt_fence,
		 plan_binding_digest, source_revision_digest, preflight_id, preflight_revision,
		 preflight_expires_at, security_decision, security_decision_digest,
		 security_finding_set_digest, security_policy_revision, authority_grant_id,
		 job_authority_category, authority_binding_digest, authority_expires_at, created_at)
	SELECT ?, checkpoint.job_id, ?, checkpoint.attempt_id, 1, 'operation_unresolved', 'write',
	       ?, job.target_chain_revision, '', ?, ?, ?, ?, ?, ?, ?, 1, 1,
	       checkpoint.plan_binding_digest, checkpoint.source_revision_digest,
	       checkpoint.preflight_id, checkpoint.preflight_revision, checkpoint.preflight_expires_at,
	       checkpoint.security_decision, checkpoint.security_decision_digest,
	       checkpoint.security_finding_set_digest, checkpoint.security_policy_revision,
	       checkpoint.authority_grant_id, checkpoint.job_authority_category,
	       checkpoint.authority_binding_digest, checkpoint.authority_expires_at, ?
	FROM backup_asset_recovery_checkpoints AS checkpoint
	JOIN backup_asset_recovery_jobs AS job ON job.id = checkpoint.job_id
	WHERE checkpoint.id = ?`
	unresolvedArgs := func(
		id, itemID, category, writeDigest, writeRevision,
		observationDigest, observedRevision, observedPresence string,
	) []any {
		t.Helper()
		return []any{
			id, itemID, recoveryMigrationDigest(899143), category,
			writeDigest, writeRevision, observationDigest, observedRevision, observedPresence,
			"matched", verifiedAt, aggregate.CheckpointID,
		}
	}
	unresolvedID := recoveryMigrationOpaqueID(899140)
	fixture.expectExecRejectedInRollback(
		t, db, unresolvedInsert, unresolvedArgs(
			recoveryMigrationOpaqueID(899141), recoveryMigrationOpaqueID(899142),
			"verification_mismatch", recoveryMigrationDigest(899144), "target-revision-write",
			recoveryMigrationDigest(899145), "target-revision-write", "present",
		)...,
	)

	for index, product := range []struct {
		category, writeDigest, writeRevision          string
		observationDigest, observedRevision, presence string
	}{
		{
			category: "write_result_invalid", writeDigest: recoveryMigrationDigest(899160),
			writeRevision: "untrusted-write-revision",
		},
		{
			category: "observation_invalid", writeDigest: recoveryMigrationDigest(899161),
			writeRevision: "target-revision-write", observationDigest: recoveryMigrationDigest(899162),
			observedRevision: "untrusted-observation-revision", presence: "present",
		},
		{
			category: "revision_disagreement", writeDigest: recoveryMigrationDigest(899163),
			writeRevision: "same-target-revision", observationDigest: recoveryMigrationDigest(899164),
			observedRevision: "same-target-revision", presence: "present",
		},
		{
			category: "verification_mismatch", writeDigest: recoveryMigrationDigest(899165),
			writeRevision: "write-target-revision", observationDigest: recoveryMigrationDigest(899166),
			observedRevision: "different-target-revision", presence: "present",
		},
		{
			category: "verification_mismatch", writeDigest: recoveryMigrationDigest(899167),
			writeRevision: "target-revision-write",
		},
	} {
		fixture.expectExecRejectedInRollback(
			t, db, unresolvedInsert, unresolvedArgs(
				recoveryMigrationOpaqueID(899170+index), aggregate.JobItemID,
				product.category, product.writeDigest, product.writeRevision,
				product.observationDigest, product.observedRevision, product.presence,
			)...,
		)
	}

	for index, product := range []struct {
		category, writeDigest, writeRevision          string
		observationDigest, observedRevision, presence string
	}{
		{
			category: "write_result_invalid", writeDigest: recoveryMigrationDigest(899180),
		},
		{
			category: "observation_invalid", writeDigest: recoveryMigrationDigest(899181),
			writeRevision: "target-revision-write", observationDigest: recoveryMigrationDigest(899182),
		},
		{
			category: "revision_disagreement", writeDigest: recoveryMigrationDigest(899183),
			writeRevision: "write-target-revision", observationDigest: recoveryMigrationDigest(899184),
			observedRevision: "observed-target-revision", presence: "present",
		},
		{
			category: "verification_mismatch", writeDigest: recoveryMigrationDigest(899185),
			writeRevision: "target-revision-write", observationDigest: recoveryMigrationDigest(899186),
			observedRevision: "target-revision-write", presence: "present",
		},
	} {
		fixture.expectExecAcceptedInRollback(
			t, db, unresolvedInsert, unresolvedArgs(
				recoveryMigrationOpaqueID(899190+index), aggregate.JobItemID,
				product.category, product.writeDigest, product.writeRevision,
				product.observationDigest, product.observedRevision, product.presence,
			)...,
		)
	}
	fixture.mustExec(
		t, db, unresolvedInsert, unresolvedArgs(
			unresolvedID, aggregate.JobItemID,
			"verification_mismatch", recoveryMigrationDigest(899144), "target-revision-write",
			recoveryMigrationDigest(899145), "target-revision-write", "present",
		)...,
	)
	fixture.expectExecRejectedInRollback(t, db, `INSERT INTO backup_asset_recovery_checkpoints
		(id, job_id, attempt_id, sequence, phase,
		 plan_binding_digest, source_revision_digest, preflight_id, preflight_revision,
		 preflight_expires_at, security_decision, security_decision_digest,
		 security_finding_set_digest, security_policy_revision, authority_grant_id,
		 job_authority_category, authority_binding_digest, authority_expires_at, created_at)
	SELECT ?, checkpoint.job_id, checkpoint.attempt_id, checkpoint.sequence + 1, 'verification',
	       checkpoint.plan_binding_digest, checkpoint.source_revision_digest,
	       checkpoint.preflight_id, checkpoint.preflight_revision, checkpoint.preflight_expires_at,
	       checkpoint.security_decision, checkpoint.security_decision_digest,
	       checkpoint.security_finding_set_digest, checkpoint.security_policy_revision,
	       checkpoint.authority_grant_id, checkpoint.job_authority_category,
	       checkpoint.authority_binding_digest, checkpoint.authority_expires_at, ?
	FROM backup_asset_recovery_checkpoints AS checkpoint
	WHERE checkpoint.id = ?`,
		recoveryMigrationOpaqueID(899198), verifiedAt, unresolvedID,
	)
	fixture.mustExec(t, db, `UPDATE backup_asset_recovery_job_items
		SET outcome = 'failed', failure_category = 'remote_outcome_unresolved', updated_at = ?
		WHERE id = ?`, verifiedAt, aggregate.JobItemID)

	for index, expiredLease := range []struct {
		query string
		id    string
	}{
		{query: `UPDATE backup_asset_recovery_attempts SET lease_expires_at = ? WHERE id = ?`, id: aggregate.AttemptID},
		{query: `UPDATE recovery_point_leases SET lease_expires_at = ? WHERE id = ?`, id: sourceLeaseID},
		{query: `UPDATE backup_asset_recovery_node_leases SET lease_expires_at = ? WHERE id = ?`, id: aggregate.NodeLeaseID},
	} {
		tx, err := db.Begin()
		if err != nil {
			t.Fatalf("begin %s expired worker lease transaction: %v", fixture.engine, err)
		}
		if _, err := tx.Exec(fixture.bind(expiredLease.query), now, expiredLease.id); err != nil {
			_ = tx.Rollback()
			t.Fatalf("expire %s worker lease: %v", fixture.engine, err)
		}
		_, insertErr := tx.Exec(fixture.bind(failureEvidenceInsert), failureEvidenceArgs(
			recoveryMigrationOpaqueID(899200+index), unresolvedID, sourceLeaseID, 1, now.Add(-time.Second),
		)...)
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			t.Fatalf("rollback %s expired worker lease transaction: %v", fixture.engine, rollbackErr)
		}
		if insertErr == nil {
			t.Fatalf("%s failure evidence accepted a backdated expired worker lease", fixture.engine)
		}
	}

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin %s unresolved target-chain transaction: %v", fixture.engine, err)
	}
	if _, err := tx.Exec(fixture.bind(`UPDATE backup_asset_recovery_jobs
		SET target_chain_revision = ?, updated_at = ? WHERE id = ?`),
		recoveryMigrationDigest(899210), verifiedAt, aggregate.JobID); err != nil {
		_ = tx.Rollback()
		t.Fatalf("change %s unresolved target chain: %v", fixture.engine, err)
	}
	_, insertErr := tx.Exec(fixture.bind(failureEvidenceInsert), failureEvidenceArgs(
		recoveryMigrationOpaqueID(899211), unresolvedID, sourceLeaseID, 1, now,
	)...)
	if rollbackErr := tx.Rollback(); rollbackErr != nil {
		t.Fatalf("rollback %s unresolved target-chain transaction: %v", fixture.engine, rollbackErr)
	}
	if insertErr == nil {
		t.Fatalf("%s failure evidence accepted an advanced unresolved target chain", fixture.engine)
	}

	fixture.mustExec(
		t, db, failureEvidenceInsert, failureEvidenceArgs(
			recoveryMigrationOpaqueID(899150), unresolvedID, sourceLeaseID, 1, now,
		)...,
	)
	fixture.test069ConsumedDeleteUnresolvedHistoryMatrix(t, db, 220)
}

func (fixture migrationFixture) test069WorkerCancelQueued(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_MIGRATION_RECOVERY_WORKER_STATE_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)

	_, sqlDB := fixture.openAt(t, backupAssetRecoveryVersion)
	db := fixture.recoveryWorkerGorm(t, sqlDB)
	now := time.Now().UTC().Truncate(time.Second)
	aggregate := fixture.seedRecoveryMigrationAggregateWithOptions(
		t, sqlDB, "g", 176, now,
		recoveryMigrationSeedOptions{claimableAttempt: true, decryptableWorkspace: true},
	)
	sourceLeaseID := recoveryMigrationOpaqueID(899201)
	fixture.mustExec(t, sqlDB, `INSERT INTO recovery_point_leases
		(id, recovery_point_id, holder_type, owner_id, attempt_id, fence_token, status,
		 lease_expires_at, absolute_deadline, last_heartbeat_at, created_at, updated_at)
		VALUES (?, ?, 'recovery_job', ?, ?, ?, 'active', ?, ?, ?, ?, ?)`,
		sourceLeaseID, aggregate.PointID, aggregate.JobID, aggregate.AttemptID,
		recoveryMigrationDigest(899202), now.Add(time.Hour), now.Add(2*time.Hour), now, now, now,
	)
	sourceLeases, err := backupasset.NewLeaseService(
		db, func() time.Time { return now },
		backupasset.LeaseConfig{Duration: 10 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: 2 * time.Hour},
	)
	if err != nil {
		t.Fatalf("create %s queued-cancellation source lease service: %v", fixture.engine, err)
	}
	coordinator, err := recovery.NewWorkerCoordinator(recovery.WorkerCoordinatorDependencies{
		DB: db, SourceLeases: sourceLeases, SourceResolver: recoveryMigrationSourceResolver{},
		Now: func() time.Time { return now }, LeaseTTL: 10 * time.Minute, ScanLimit: 8,
	})
	if err != nil {
		t.Fatalf("create %s queued-cancellation worker coordinator: %v", fixture.engine, err)
	}

	if err := coordinator.CancelJob(context.Background(), aggregate.JobID); err != nil {
		t.Fatalf("cancel %s queued recovery job: %v", fixture.engine, err)
	}
	if err := coordinator.CancelJob(context.Background(), aggregate.JobID); err != nil {
		t.Fatalf("repeat cancel %s queued recovery job: %v", fixture.engine, err)
	}

	var job model.BackupAssetRecoveryJob
	if err := db.Where("id = ?", aggregate.JobID).Take(&job).Error; err != nil {
		t.Fatalf("load %s canceled queued job: %v", fixture.engine, err)
	}
	wantWorkspace := "jobs/" + job.ID
	wantWorkspaceBinding := recoveryMigrationWorkspaceBindingDigest(
		job.ID,
		job.PlanID,
		job.PlanBindingDigest,
		int64(job.TargetNodeID),
		job.TargetRootID,
		job.RootLocatorDigest,
		wantWorkspace,
	)
	if job.State != string(recovery.JobStateCanceled) || job.TransitionRevision != 3 ||
		job.WorkspacePhase != string(recovery.WorkspacePhaseNone) ||
		job.EncryptedWorkspaceRelativeLocator != wantWorkspace || job.WorkspaceBindingDigest != wantWorkspaceBinding ||
		job.WorkspaceMarkerBindingDigest != "" || job.PlaintextDeadline != nil {
		t.Fatalf("%s queued cancellation job state=%+v", fixture.engine, job)
	}
	var attempt model.BackupAssetRecoveryAttempt
	if err := db.Where("id = ?", aggregate.AttemptID).Take(&attempt).Error; err != nil {
		t.Fatalf("load %s canceled queued attempt: %v", fixture.engine, err)
	}
	if attempt.State != string(recovery.AttemptStateCanceled) || attempt.ClosedAt == nil || attempt.MutationArmed {
		t.Fatalf("%s queued cancellation attempt=%+v", fixture.engine, attempt)
	}
	var source model.RecoveryPointLease
	if err := db.Where("id = ?", sourceLeaseID).Take(&source).Error; err != nil {
		t.Fatalf("load %s canceled queued source lease: %v", fixture.engine, err)
	}
	if source.Status != string(backupasset.LeaseReleased) || source.ReleasedAt == nil {
		t.Fatalf("%s queued cancellation source lease=%+v", fixture.engine, source)
	}
	var node model.BackupAssetRecoveryNodeLease
	if err := db.Where("id = ?", aggregate.NodeLeaseID).Take(&node).Error; err != nil {
		t.Fatalf("load %s canceled queued node lease: %v", fixture.engine, err)
	}
	if node.State != "released" || node.ReleasedAt == nil || node.Fence != 1 {
		t.Fatalf("%s queued cancellation node lease=%+v", fixture.engine, node)
	}
	var checkpointCount, latchCount int64
	if err := db.Model(&model.BackupAssetRecoveryCheckpoint{}).Where("job_id = ?", aggregate.JobID).Count(&checkpointCount).Error; err != nil {
		t.Fatalf("count %s queued cancellation checkpoints: %v", fixture.engine, err)
	}
	if err := db.Model(&model.BackupAssetRecoveryEvidence{}).Where("id = ?", recovery.RecoverySchemaUseLatchID).Count(&latchCount).Error; err != nil {
		t.Fatalf("count %s queued cancellation latches: %v", fixture.engine, err)
	}
	if checkpointCount != 0 || latchCount != 0 {
		t.Fatalf("%s queued cancellation created target state: checkpoints=%d latches=%d", fixture.engine, checkpointCount, latchCount)
	}
}

func (fixture migrationFixture) test069WorkerPreWriteSourceDrift(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_MIGRATION_RECOVERY_SOURCE_DRIFT_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)

	_, sqlDB := fixture.openAt(t, backupAssetRecoveryVersion)
	db := fixture.recoveryWorkerGorm(t, sqlDB)
	now := time.Now().UTC().Truncate(time.Second)
	aggregate := fixture.seedRecoveryMigrationAggregateWithOptions(
		t, sqlDB, "h", 177, now, recoveryMigrationSeedOptions{claimableAttempt: true, firstWrite: true},
	)
	sourceLeaseID := recoveryMigrationOpaqueID(899301)
	fixture.mustExec(t, sqlDB, `INSERT INTO recovery_point_leases
		(id, recovery_point_id, holder_type, owner_id, attempt_id, fence_token, status,
		 lease_expires_at, absolute_deadline, last_heartbeat_at, created_at, updated_at)
		VALUES (?, ?, 'recovery_job', ?, ?, ?, 'active', ?, ?, ?, ?, ?)`,
		sourceLeaseID, aggregate.PointID, aggregate.JobID, aggregate.AttemptID,
		recoveryMigrationDigest(899302), now.Add(time.Hour), now.Add(2*time.Hour), now, now, now,
	)
	clock := now.Add(time.Minute)
	sourceLeases, err := backupasset.NewLeaseService(
		db, func() time.Time { return clock },
		backupasset.LeaseConfig{Duration: 10 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: 2 * time.Hour},
	)
	if err != nil {
		t.Fatalf("create %s source-drift source lease service: %v", fixture.engine, err)
	}
	coordinator, err := recovery.NewWorkerCoordinator(recovery.WorkerCoordinatorDependencies{
		DB:              db,
		SourceLeases:    sourceLeases,
		LiveRevalidator: recoveryMigrationFirstWriteLiveRevalidator{},
		WorkspaceKeys:   recoveryMigrationFirstWriteWorkspaceKeySource{},
		SourceResolver:  recoveryMigrationSourceResolver{},
		Now:             func() time.Time { return clock },
		LeaseTTL:        10 * time.Minute,
		ScanLimit:       8,
	})
	if err != nil {
		t.Fatalf("create %s source-drift worker coordinator: %v", fixture.engine, err)
	}
	claim, found, err := coordinator.ClaimNext(context.Background(), "recovery-worker-a")
	if err != nil || !found {
		t.Fatalf("claim %s source-drift recovery job: found=%t err=%v", fixture.engine, found, err)
	}
	var planItem model.BackupAssetRecoveryPlanItem
	if err := db.Where("id = ? AND plan_id = ?", aggregate.PlanItemID, aggregate.PlanID).Take(&planItem).Error; err != nil {
		t.Fatalf("load %s source-drift plan item: %v", fixture.engine, err)
	}
	if err := db.Model(&model.CatalogEntry{}).
		Where("generation_id = ? AND recovery_point_id = ? AND entry_id = ?",
			planItem.CatalogGenerationID, planItem.RecoveryPointID, planItem.EntryID).
		Update("normalized_path", "/migrated-schema-source-drift").Error; err != nil {
		t.Fatalf("introduce %s source drift: %v", fixture.engine, err)
	}

	if _, err := coordinator.PrepareFirstWrite(context.Background(), claim); !errors.Is(err, recovery.ErrRecoverySourceChanged) {
		t.Fatalf("prepare %s first write after source drift error=%v, want ErrRecoverySourceChanged", fixture.engine, err)
	}

	var plan model.BackupAssetRecoveryPlan
	if err := db.Where("id = ?", aggregate.PlanID).Take(&plan).Error; err != nil {
		t.Fatalf("load %s source-drift plan: %v", fixture.engine, err)
	}
	if plan.State != string(recovery.PlanStateSuperseded) {
		t.Fatalf("%s source-drift plan=%+v", fixture.engine, plan)
	}
	var job model.BackupAssetRecoveryJob
	if err := db.Where("id = ?", aggregate.JobID).Take(&job).Error; err != nil {
		t.Fatalf("load %s source-drift job: %v", fixture.engine, err)
	}
	if job.State != string(recovery.JobStateFailed) || job.FailureCategory != "pre_write_drift" ||
		job.TransitionRevision != claim.TransitionRevision+1 || job.WorkspacePhase != string(recovery.WorkspacePhaseNone) {
		t.Fatalf("%s source-drift job=%+v", fixture.engine, job)
	}
	var attempt model.BackupAssetRecoveryAttempt
	if err := db.Where("id = ?", aggregate.AttemptID).Take(&attempt).Error; err != nil {
		t.Fatalf("load %s source-drift attempt: %v", fixture.engine, err)
	}
	if attempt.State != string(recovery.AttemptStateSuperseded) || attempt.MutationArmed || attempt.ClosedAt == nil {
		t.Fatalf("%s source-drift attempt=%+v", fixture.engine, attempt)
	}
	var source model.RecoveryPointLease
	if err := db.Where("id = ?", sourceLeaseID).Take(&source).Error; err != nil {
		t.Fatalf("load %s source-drift source lease: %v", fixture.engine, err)
	}
	if source.Status != string(backupasset.LeaseReleased) || source.ReleasedAt == nil {
		t.Fatalf("%s source-drift source lease=%+v", fixture.engine, source)
	}
	var node model.BackupAssetRecoveryNodeLease
	if err := db.Where("id = ?", aggregate.NodeLeaseID).Take(&node).Error; err != nil {
		t.Fatalf("load %s source-drift node lease: %v", fixture.engine, err)
	}
	if node.State != "released" || node.ReleasedAt == nil || node.Fence != claim.NodeFence {
		t.Fatalf("%s source-drift node lease=%+v", fixture.engine, node)
	}
	var unusedGrantRevokedAt sql.NullTime
	if err := sqlDB.QueryRow(fixture.bind(`SELECT revoked_at FROM backup_asset_recovery_grants WHERE id = ?`), aggregate.GrantID).
		Scan(&unusedGrantRevokedAt); err != nil {
		t.Fatalf("load %s source-drift unused grant revocation: %v", fixture.engine, err)
	}
	if !unusedGrantRevokedAt.Valid {
		t.Fatalf("%s source drift did not revoke unused authority", fixture.engine)
	}
	var checkpointCount, latchCount int64
	if err := db.Model(&model.BackupAssetRecoveryCheckpoint{}).Where("job_id = ?", aggregate.JobID).Count(&checkpointCount).Error; err != nil {
		t.Fatalf("count %s source-drift checkpoints: %v", fixture.engine, err)
	}
	if err := db.Model(&model.BackupAssetRecoveryEvidence{}).Where("id = ?", recovery.RecoverySchemaUseLatchID).Count(&latchCount).Error; err != nil {
		t.Fatalf("count %s source-drift latches: %v", fixture.engine, err)
	}
	if checkpointCount != 0 || latchCount != 0 {
		t.Fatalf("%s source drift wrote target state: checkpoints=%d latches=%d", fixture.engine, checkpointCount, latchCount)
	}
}

func (fixture migrationFixture) test069WorkerPreWriteAuthorityDrift(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_MIGRATION_RECOVERY_AUTHORITY_DRIFT_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)

	_, sqlDB := fixture.openAt(t, backupAssetRecoveryVersion)
	db := fixture.recoveryWorkerGorm(t, sqlDB)
	now := time.Now().UTC().Truncate(time.Second)
	aggregate := fixture.seedRecoveryMigrationAggregateWithOptions(
		t, sqlDB, "8", 198, now, recoveryMigrationSeedOptions{claimableAttempt: true, firstWrite: true},
	)
	sourceLeaseID := recoveryMigrationOpaqueID(899601)
	fixture.mustExec(t, sqlDB, `INSERT INTO recovery_point_leases
		(id, recovery_point_id, holder_type, owner_id, attempt_id, fence_token, status,
		 lease_expires_at, absolute_deadline, last_heartbeat_at, created_at, updated_at)
		VALUES (?, ?, 'recovery_job', ?, ?, ?, 'active', ?, ?, ?, ?, ?)`,
		sourceLeaseID, aggregate.PointID, aggregate.JobID, aggregate.AttemptID,
		recoveryMigrationDigest(899602), now.Add(time.Hour), now.Add(2*time.Hour), now, now, now,
	)
	clock := now.Add(time.Minute)
	sourceLeases, err := backupasset.NewLeaseService(
		db, func() time.Time { return clock },
		backupasset.LeaseConfig{Duration: 10 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: 2 * time.Hour},
	)
	if err != nil {
		t.Fatalf("create %s authority-drift source lease service: %v", fixture.engine, err)
	}
	coordinator, err := recovery.NewWorkerCoordinator(recovery.WorkerCoordinatorDependencies{
		DB:              db,
		SourceLeases:    sourceLeases,
		LiveRevalidator: recoveryMigrationFirstWriteAuthorityDriftRevalidator{},
		WorkspaceKeys:   recoveryMigrationFirstWriteWorkspaceKeySource{},
		SourceResolver:  recoveryMigrationSourceResolver{},
		Now:             func() time.Time { return clock },
		LeaseTTL:        10 * time.Minute,
		ScanLimit:       8,
	})
	if err != nil {
		t.Fatalf("create %s authority-drift worker coordinator: %v", fixture.engine, err)
	}
	claim, found, err := coordinator.ClaimNext(context.Background(), "recovery-f3-postgres-worker")
	if err != nil || !found {
		t.Fatalf("claim %s authority-drift recovery job: found=%t err=%v", fixture.engine, found, err)
	}
	if _, err := coordinator.PrepareFirstWrite(context.Background(), claim); !errors.Is(err, recovery.ErrRecoverySourceChanged) {
		t.Fatalf("prepare %s first write after authority drift error=%v, want ErrRecoverySourceChanged", fixture.engine, err)
	}

	var plan model.BackupAssetRecoveryPlan
	if err := db.Where("id = ?", aggregate.PlanID).Take(&plan).Error; err != nil {
		t.Fatalf("load %s authority-drift plan: %v", fixture.engine, err)
	}
	var job model.BackupAssetRecoveryJob
	if err := db.Where("id = ?", aggregate.JobID).Take(&job).Error; err != nil {
		t.Fatalf("load %s authority-drift job: %v", fixture.engine, err)
	}
	var attempt model.BackupAssetRecoveryAttempt
	if err := db.Where("id = ?", aggregate.AttemptID).Take(&attempt).Error; err != nil {
		t.Fatalf("load %s authority-drift attempt: %v", fixture.engine, err)
	}
	var source model.RecoveryPointLease
	if err := db.Where("id = ?", sourceLeaseID).Take(&source).Error; err != nil {
		t.Fatalf("load %s authority-drift source lease: %v", fixture.engine, err)
	}
	var node model.BackupAssetRecoveryNodeLease
	if err := db.Where("id = ?", aggregate.NodeLeaseID).Take(&node).Error; err != nil {
		t.Fatalf("load %s authority-drift node lease: %v", fixture.engine, err)
	}
	if plan.State != string(recovery.PlanStateSuperseded) ||
		job.State != string(recovery.JobStateFailed) || job.FailureCategory != "pre_write_drift" ||
		job.TransitionRevision != claim.TransitionRevision+1 ||
		attempt.State != string(recovery.AttemptStateSuperseded) || attempt.MutationArmed || attempt.ClosedAt == nil ||
		source.Status != string(backupasset.LeaseReleased) || source.ReleasedAt == nil ||
		node.State != "released" || node.ReleasedAt == nil {
		t.Fatalf("%s authority drift was not one terminal transaction: plan=%+v job=%+v attempt=%+v source=%+v node=%+v",
			fixture.engine, plan, job, attempt, source, node)
	}
	var checkpointCount, latchCount int64
	if err := db.Model(&model.BackupAssetRecoveryCheckpoint{}).
		Where("job_id = ?", aggregate.JobID).Count(&checkpointCount).Error; err != nil {
		t.Fatalf("count %s authority-drift checkpoints: %v", fixture.engine, err)
	}
	if err := db.Model(&model.BackupAssetRecoveryEvidence{}).
		Where("id = ?", recovery.RecoverySchemaUseLatchID).Count(&latchCount).Error; err != nil {
		t.Fatalf("count %s authority-drift latches: %v", fixture.engine, err)
	}
	if checkpointCount != 0 || latchCount != 0 {
		t.Fatalf("%s authority drift crossed first-write barrier: checkpoints=%d latches=%d",
			fixture.engine, checkpointCount, latchCount)
	}
	var schedulerCount int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM backup_asset_recovery_evidence
		WHERE (id = '0000000000000000000000000000006a' AND kind = 'scheduler_state' AND scheduler_scope = 'claim')
		   OR (id = '0000000000000000000000000000006b' AND kind = 'scheduler_state' AND scheduler_scope = 'takeover')`).
		Scan(&schedulerCount); err != nil {
		t.Fatalf("load %s F3 scheduler rows: %v", fixture.engine, err)
	}
	if schedulerCount != 2 {
		t.Fatalf("%s F3 scheduler rows=%d, want 2", fixture.engine, schedulerCount)
	}
}

func (fixture migrationFixture) test069TerminalArmedAttemptCannotBeDeletedOrRebuilt(t *testing.T) {
	for _, testCase := range []struct {
		name          string
		mutationArmed bool
	}{
		{name: "armed", mutationArmed: true},
		{name: "unarmed", mutationArmed: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, db := fixture.openAt(t, backupAssetRecoveryVersion)
			now := time.Now().UTC().Truncate(time.Second)
			aggregate := fixture.seedRecoveryMigrationAggregate(t, db, "4", 149, now, true)
			closedAt := now.Add(time.Minute)
			fixture.mustExec(t, db, `UPDATE backup_asset_recovery_attempts
				SET state = 'lost', mutation_armed = ?, closed_at = ?, updated_at = ? WHERE id = ?`,
				testCase.mutationArmed, closedAt, closedAt, aggregate.AttemptID)

			tx, err := db.Begin()
			if err != nil {
				t.Fatalf("begin %s terminal-attempt delete/rebuild probe: %v", fixture.engine, err)
			}
			defer func() {
				if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
					t.Fatalf("rollback %s terminal-attempt delete/rebuild probe: %v", fixture.engine, rollbackErr)
				}
			}()
			if _, err := tx.Exec(fixture.bind(`DELETE FROM backup_asset_recovery_evidence WHERE job_id = ?`), aggregate.JobID); err != nil {
				t.Fatalf("remove %s terminal-attempt evidence dependency: %v", fixture.engine, err)
			}
			if _, err := tx.Exec(fixture.bind(`DELETE FROM backup_asset_recovery_node_leases WHERE job_id = ?`), aggregate.JobID); err != nil {
				t.Fatalf("remove %s terminal-attempt node-lease dependency: %v", fixture.engine, err)
			}
			deleteErr := func() error {
				if _, err := tx.Exec(fixture.bind(`DELETE FROM backup_asset_recovery_attempts WHERE id = ?`), aggregate.AttemptID); err != nil {
					return err
				}
				_, err := tx.Exec(fixture.bind(`INSERT INTO backup_asset_recovery_attempts
					(id, job_id, owner_id, fence, state, mutation_armed, lease_expires_at, heartbeat_at,
					 created_at, updated_at)
					VALUES (?, ?, 'replacement-worker', 2, 'claimed', ?, ?, ?, ?, ?)`),
					aggregate.AttemptID, aggregate.JobID, false, now.Add(30*time.Minute), now, now, now)
				return err
			}()
			if deleteErr == nil {
				t.Fatalf("%s terminal %s attempt was deleted and rebuilt with a fresh owner/fence", fixture.engine, testCase.name)
			}
		})
	}

	if fixture.engine != "sqlite" {
		return
	}
	for _, testCase := range []struct {
		name     string
		terminal bool
	}{
		{name: "terminal", terminal: true},
		{name: "mutation armed"},
	} {
		t.Run("INSERT OR REPLACE cannot rebuild "+testCase.name+" attempt", func(t *testing.T) {
			_, db := fixture.openAt(t, backupAssetRecoveryVersion)
			now := time.Now().UTC().Truncate(time.Second)
			aggregate := fixture.seedRecoveryMigrationAggregate(t, db, "5", 150, now, true)
			if testCase.terminal {
				closedAt := now.Add(time.Minute)
				fixture.mustExec(t, db, `UPDATE backup_asset_recovery_attempts
					SET state = 'lost', mutation_armed = ?, closed_at = ?, updated_at = ? WHERE id = ?`,
					false, closedAt, closedAt, aggregate.AttemptID)
			} else {
				fixture.mustExec(t, db, `UPDATE backup_asset_recovery_attempts
					SET mutation_armed = ?, updated_at = ? WHERE id = ?`, true, now.Add(time.Minute), aggregate.AttemptID)
			}
			fixture.mustExec(t, db, `DELETE FROM backup_asset_recovery_evidence WHERE job_id = ?`, aggregate.JobID)
			fixture.mustExec(t, db, `DELETE FROM backup_asset_recovery_node_leases WHERE job_id = ?`, aggregate.JobID)
			fixture.expectExecRejectedInRollback(t, db, `INSERT OR REPLACE INTO backup_asset_recovery_attempts
				(id, job_id, owner_id, fence, state, mutation_armed, lease_expires_at, heartbeat_at,
				 created_at, updated_at)
				VALUES (?, ?, 'replacement-worker', 2, 'claimed', ?, ?, ?, ?, ?)`,
				aggregate.AttemptID, aggregate.JobID, false, now.Add(30*time.Minute), now, now, now)
		})
	}
}

func (fixture migrationFixture) test069PlanJobItemOrdinalParity(t *testing.T) {
	_, db := fixture.openAt(t, backupAssetRecoveryVersion)
	now := time.Now().UTC().Truncate(time.Second)
	aggregate := fixture.seedRecoveryMigrationAggregate(t, db, "b", 97, now)

	t.Run("insert mismatch", func(t *testing.T) {
		tx, err := db.Begin()
		if err != nil {
			t.Fatalf("begin %s job-item ordinal insert probe: %v", fixture.engine, err)
		}
		defer func() {
			if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
				t.Fatalf("rollback %s job-item ordinal insert probe: %v", fixture.engine, rollbackErr)
			}
		}()
		if _, err := tx.Exec(fixture.bind(`DELETE FROM backup_asset_recovery_job_items WHERE id = ?`), aggregate.JobItemID); err != nil {
			t.Fatalf("delete %s seeded job item for ordinal probe: %v", fixture.engine, err)
		}
		if _, err := tx.Exec(fixture.bind(`INSERT INTO backup_asset_recovery_job_items
				(id, plan_id, job_id, plan_item_id, ordinal, operation_kind, target_path_digest,
				 semantic_target_digest, target_object_digest,
				 expected_prior_kind, expected_prior_digest, expected_post_identity_digest,
				 expected_post_bytes, expected_prior_bytes, encrypted_target_relative_locator,
				 target_locator_key_version, target_locator_cipher_version,
				 display_class, estimated_bytes, created_at, updated_at)
				VALUES (?, ?, ?, ?, 1, 'create', ?, ?, ?, 'absent', '', ?, 1, -1, ?, 1, 1, 'regular', 1, ?, ?)`),
			recoveryMigrationOpaqueID(699913), aggregate.PlanID, aggregate.JobID, aggregate.PlanItemID,
			recoveryMigrationDigest(699914), recoveryMigrationDigest(699916), recoveryMigrationDigest(699917),
			recoveryMigrationDigest(699915), "enc:v2:ordinal-probe", now, now); err != nil {
			t.Fatalf("%s job-item insert rejected exact plan item at independent operation ordinal: %v", fixture.engine, err)
		}
	})
}

func (fixture migrationFixture) test069OperationSnapshot(t *testing.T) {
	_, db := fixture.openAt(t, backupAssetRecoveryVersion)
	preflightDefinition := fixture.tableDefinition(t, db, "backup_asset_recovery_preflights")
	jobItemDefinition := fixture.tableDefinition(t, db, "backup_asset_recovery_job_items")
	for _, fragment := range []string{"encrypted_operation_rows"} {
		if !strings.Contains(preflightDefinition, fragment) {
			t.Fatalf("%s preflight operation snapshot definition omits %q: %s", fixture.engine, fragment, preflightDefinition)
		}
	}
	if !fixture.columnIsNotNull(t, db, "backup_asset_recovery_preflights", "encrypted_operation_rows") {
		t.Fatalf("%s preflight encrypted operation snapshot column is nullable", fixture.engine)
	}
	for _, fragment := range []string{
		"target_path_digest",
		"expected_prior_kind",
		"expected_prior_digest",
		"display_class",
		"estimated_bytes",
	} {
		if !strings.Contains(jobItemDefinition, fragment) {
			t.Fatalf("%s job-item operation snapshot definition omits %q: %s", fixture.engine, fragment, jobItemDefinition)
		}
	}

	now := time.Now().UTC().Truncate(time.Second)
	aggregate := fixture.seedRecoveryMigrationExactMirrorAggregate(t, db, "o", 197, now)
	var encryptedSnapshot string
	if err := db.QueryRow(fixture.bind(`SELECT encrypted_operation_rows
		FROM backup_asset_recovery_preflights WHERE id = ?`), aggregate.PreflightID).Scan(&encryptedSnapshot); err != nil {
		t.Fatalf("load %s encrypted operation snapshot: %v", fixture.engine, err)
	}
	if encryptedSnapshot == "" {
		t.Fatalf("%s preflight persisted an empty operation snapshot", fixture.engine)
	}

	targetPathDigest := recoveryMigrationDigest(699971)
	expectedPriorDigest := recoveryMigrationDigest(699972)
	validDelete := `INSERT INTO backup_asset_recovery_job_items
		(id, plan_id, job_id, plan_item_id, ordinal, operation_kind, target_path_digest,
		 semantic_target_digest, target_object_digest,
		 expected_prior_kind, expected_prior_digest, expected_post_identity_digest,
		 expected_post_bytes, expected_prior_bytes, encrypted_target_relative_locator,
		 target_locator_key_version, target_locator_cipher_version,
		 display_class, estimated_bytes, created_at, updated_at)
		VALUES (?, ?, ?, NULL, 1, 'delete', ?, ?, ?, 'present', ?, '', -1, -1, ?, 1, 1, 'directory', 0, ?, ?)`
	fixture.expectExecAcceptedInRollback(t, db, validDelete, recoveryMigrationOpaqueID(699973),
		aggregate.PlanID, aggregate.JobID, targetPathDigest, recoveryMigrationDigest(699981),
		recoveryMigrationDigest(699982), expectedPriorDigest, "enc:v2:valid-delete", now, now)

	invalidRows := []struct {
		name  string
		query string
		args  []any
	}{
		{
			name: "delete references a plan item",
			query: `INSERT INTO backup_asset_recovery_job_items
				(id, plan_id, job_id, plan_item_id, ordinal, operation_kind, target_path_digest,
				 semantic_target_digest, target_object_digest,
				 expected_prior_kind, expected_prior_digest, expected_post_identity_digest,
				 expected_post_bytes, expected_prior_bytes, encrypted_target_relative_locator,
				 target_locator_key_version, target_locator_cipher_version,
				 display_class, estimated_bytes, created_at, updated_at)
				VALUES (?, ?, ?, ?, 1, 'delete', ?, ?, ?, 'present', ?, '', -1, -1, ?, 1, 1, 'directory', 0, ?, ?)`,
			args: []any{recoveryMigrationOpaqueID(699974), aggregate.PlanID, aggregate.JobID, aggregate.PlanItemID,
				recoveryMigrationDigest(699975), recoveryMigrationDigest(699983), recoveryMigrationDigest(699984),
				expectedPriorDigest, "enc:v2:delete-plan-item", now, now},
		},
		{
			name: "create omits its plan item",
			query: `INSERT INTO backup_asset_recovery_job_items
				(id, plan_id, job_id, plan_item_id, ordinal, operation_kind, target_path_digest,
				 semantic_target_digest, target_object_digest,
				 expected_prior_kind, expected_prior_digest, expected_post_identity_digest,
				 expected_post_bytes, expected_prior_bytes, encrypted_target_relative_locator,
				 target_locator_key_version, target_locator_cipher_version,
				 display_class, estimated_bytes, created_at, updated_at)
				VALUES (?, ?, ?, NULL, 1, 'create', ?, ?, ?, 'absent', '', ?, 1, -1, ?, 1, 1, 'regular', 1, ?, ?)`,
			args: []any{recoveryMigrationOpaqueID(699976), aggregate.PlanID, aggregate.JobID,
				recoveryMigrationDigest(699977), recoveryMigrationDigest(699985), recoveryMigrationDigest(699986),
				recoveryMigrationDigest(699976), "enc:v2:create-no-plan-item", now, now},
		},
		{
			name: "create claims a present target",
			query: `INSERT INTO backup_asset_recovery_job_items
				(id, plan_id, job_id, plan_item_id, ordinal, operation_kind, target_path_digest,
				 semantic_target_digest, target_object_digest,
				 expected_prior_kind, expected_prior_digest, expected_post_identity_digest,
				 expected_post_bytes, expected_prior_bytes, encrypted_target_relative_locator,
				 target_locator_key_version, target_locator_cipher_version,
				 display_class, estimated_bytes, created_at, updated_at)
				VALUES (?, ?, ?, ?, 0, 'create', ?, ?, ?, 'present', ?, ?, 1, -1, ?, 1, 1, 'regular', 1, ?, ?)`,
			args: []any{recoveryMigrationOpaqueID(699978), aggregate.PlanID, aggregate.JobID, aggregate.PlanItemID,
				recoveryMigrationDigest(699979), recoveryMigrationDigest(699987), recoveryMigrationDigest(699988),
				expectedPriorDigest, recoveryMigrationDigest(699978),
				"enc:v2:create-present", now, now},
		},
		{
			name: "duplicate target path",
			query: `INSERT INTO backup_asset_recovery_job_items
				(id, plan_id, job_id, plan_item_id, ordinal, operation_kind, target_path_digest,
				 semantic_target_digest, target_object_digest,
				 expected_prior_kind, expected_prior_digest, expected_post_identity_digest,
				 expected_post_bytes, expected_prior_bytes, encrypted_target_relative_locator,
				 target_locator_key_version, target_locator_cipher_version,
				 display_class, estimated_bytes, created_at, updated_at)
				SELECT ?, plan_id, job_id, NULL, 1, 'delete', target_path_digest, ?, ?,
				 'present', ?, '', -1, -1, ?, 1, 1, 'directory', 0, ?, ?
				FROM backup_asset_recovery_job_items WHERE id = ?`,
			args: []any{recoveryMigrationOpaqueID(699980), recoveryMigrationDigest(699989), recoveryMigrationDigest(699990),
				expectedPriorDigest, "enc:v2:duplicate-target", now, now, aggregate.JobItemID},
		},
	}
	for _, testCase := range invalidRows {
		t.Run(testCase.name, func(t *testing.T) {
			fixture.expectExecRejectedInRollback(t, db, testCase.query, testCase.args...)
		})
	}
}

func (fixture migrationFixture) test069ActiveGrantBindingImmutability(t *testing.T) {
	_, db := fixture.openAt(t, backupAssetRecoveryVersion)
	now := time.Now().UTC().Truncate(time.Second)
	activeGrant := fixture.seedRecoveryMigrationAggregate(t, db, "f", 85, now)
	alternateGrant := fixture.seedRecoveryMigrationAggregate(t, db, "g", 86, now.Add(time.Second))

	type authorityBindingRewrite struct {
		name string
		set  string
		args []any
	}
	rewrites := []authorityBindingRewrite{
		{name: "grant identity", set: "id = ?", args: []any{recoveryMigrationOpaqueID(699995)}},
		{name: "plan and job binding", set: "plan_id = ?, job_id = ?", args: []any{alternateGrant.PlanID, alternateGrant.JobID}},
		{name: "authority category and job shape", set: "authority_category = 'exact_mirror_delete', job_id = ?", args: []any{activeGrant.JobID}},
		{name: "grant hash", set: "grant_hash = ?", args: []any{recoveryMigrationDigest(699996)}},
		{name: "actor user", set: "actor_user_id = ?", args: []any{alternateGrant.UserID}},
		{name: "actor session", set: "actor_session_id = ?", args: []any{"rewritten-session"}},
		{name: "binding digest", set: "binding_digest = ?", args: []any{recoveryMigrationDigest(699997)}},
		{name: "encrypted reason", set: "encrypted_reason = ?", args: []any{"enc:v2:rewritten-reason"}},
		{name: "expiry", set: "expires_at = ?", args: []any{now.Add(20 * time.Minute)}},
		{name: "created timestamp", set: "created_at = ?", args: []any{now.Add(-time.Minute)}},
	}

	var accepted []string
	for _, rewrite := range rewrites {
		tx, err := db.Begin()
		if err != nil {
			t.Fatalf("begin %s active-grant %s rewrite: %v", fixture.engine, rewrite.name, err)
		}
		query := fmt.Sprintf(`UPDATE backup_asset_recovery_grants SET %s WHERE id = ?`, rewrite.set)
		args := append(append([]any(nil), rewrite.args...), activeGrant.GrantID)
		_, execErr := tx.Exec(fixture.bind(query), args...)
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			t.Fatalf("rollback %s active-grant %s rewrite: %v", fixture.engine, rewrite.name, rollbackErr)
		}
		if execErr == nil {
			accepted = append(accepted, rewrite.name)
		}
	}

	var grantID, planID, grantHash, sessionID, bindingDigest, encryptedReason string
	var jobID sql.NullString
	var actorUserID int64
	var authorityCategory string
	var expiresAt, createdAt time.Time
	if err := db.QueryRow(fixture.bind(`SELECT id, plan_id, job_id, authority_category, grant_hash,
		actor_user_id, actor_session_id, binding_digest, encrypted_reason, expires_at, created_at
		FROM backup_asset_recovery_grants WHERE id = ?`), activeGrant.GrantID).Scan(
		&grantID, &planID, &jobID, &authorityCategory, &grantHash, &actorUserID, &sessionID,
		&bindingDigest, &encryptedReason, &expiresAt, &createdAt,
	); err != nil {
		t.Fatalf("read %s active recovery grant after rollback probes: %v", fixture.engine, err)
	}
	if grantID != activeGrant.GrantID || planID != activeGrant.PlanID || jobID.Valid ||
		authorityCategory != "write" || grantHash != recoveryMigrationDigest(698525) ||
		actorUserID != activeGrant.UserID || sessionID != "recovery-session" ||
		bindingDigest != recoveryMigrationDigest(698526) || encryptedReason != "enc:v2:reason" ||
		!expiresAt.Equal(now.Add(30*time.Minute)) || !createdAt.Equal(now) {
		t.Fatalf("%s active-grant rewrite rollback leaked authority state: id=%q plan=%q job=%v category=%q hash=%q actor=%d session=%q binding=%q reason=%q expires=%s created=%s",
			fixture.engine, grantID, planID, jobID, authorityCategory, grantHash, actorUserID, sessionID,
			bindingDigest, encryptedReason, expiresAt, createdAt)
	}

	updatedAt := now.Add(30 * time.Second)
	fixture.mustExec(t, db, `UPDATE backup_asset_recovery_grants SET updated_at = ? WHERE id = ?`, updatedAt, activeGrant.GrantID)
	var gotUpdatedAt time.Time
	if err := db.QueryRow(fixture.bind(`SELECT updated_at FROM backup_asset_recovery_grants WHERE id = ?`), activeGrant.GrantID).Scan(&gotUpdatedAt); err != nil {
		t.Fatalf("read %s active grant updated_at: %v", fixture.engine, err)
	}
	if !gotUpdatedAt.Equal(updatedAt) {
		t.Fatalf("%s active grant updated_at=%s, want %s", fixture.engine, gotUpdatedAt, updatedAt)
	}

	for index, terminalTransition := range []struct {
		name   string
		column string
	}{
		{name: "consume", column: "consumed_at"},
		{name: "revoke", column: "revoked_at"},
	} {
		grant := fixture.seedRecoveryMigrationAggregate(t, db, string(rune('h'+index)), 87+index, now.Add(time.Duration(index+2)*time.Second))
		terminalAt := now.Add(time.Duration(index+2) * time.Minute)
		fixture.mustExec(t, db, fmt.Sprintf(`UPDATE backup_asset_recovery_grants
			SET %s = ?, updated_at = ? WHERE id = ?`, terminalTransition.column), terminalAt, terminalAt, grant.GrantID)

		var consumedAt, revokedAt sql.NullTime
		var terminalUpdatedAt time.Time
		if err := db.QueryRow(fixture.bind(`SELECT consumed_at, revoked_at, updated_at FROM backup_asset_recovery_grants WHERE id = ?`), grant.GrantID).Scan(&consumedAt, &revokedAt, &terminalUpdatedAt); err != nil {
			t.Fatalf("read %s clean %s recovery grant: %v", fixture.engine, terminalTransition.name, err)
		}
		if !terminalUpdatedAt.Equal(terminalAt) {
			t.Fatalf("%s clean %s transition updated_at=%s, want %s", fixture.engine, terminalTransition.name, terminalUpdatedAt, terminalAt)
		}
		if terminalTransition.column == "consumed_at" {
			if !consumedAt.Valid || !consumedAt.Time.Equal(terminalAt) || revokedAt.Valid {
				t.Fatalf("%s clean consume transition state=%v/%v", fixture.engine, consumedAt, revokedAt)
			}
		} else if consumedAt.Valid || !revokedAt.Valid || !revokedAt.Time.Equal(terminalAt) {
			t.Fatalf("%s clean revoke transition state=%v/%v", fixture.engine, consumedAt, revokedAt)
		}
	}

	if len(accepted) != 0 {
		t.Fatalf("%s active recovery grant accepted authority-binding rewrites before terminalization: %s",
			fixture.engine, strings.Join(accepted, ", "))
	}
}

func (fixture migrationFixture) test069PlanIDUsesExactlyOneUniqueStructure(t *testing.T) {
	_, db := fixture.openAt(t, backupAssetRecoveryVersion)
	if got := fixture.recoveryJobPlanIDUniqueStructureCount(t, db); got != 1 {
		t.Fatalf("%s recovery jobs has %d unique plan_id structures, want exactly one named index", fixture.engine, got)
	}
	if definition := fixture.indexDefinition(t, db, "idx_backup_asset_recovery_jobs_plan"); !strings.Contains(definition, "unique index") || !strings.Contains(definition, "plan_id") {
		t.Fatalf("%s named recovery job plan index lost uniqueness: %s", fixture.engine, definition)
	}

	now := time.Now().UTC().Truncate(time.Second)
	aggregate := fixture.seedRecoveryMigrationAggregate(t, db, "d", 14, now)
	tryDuplicate := func(dropUniqueIndex bool) error {
		t.Helper()
		tx, err := db.Begin()
		if err != nil {
			t.Fatalf("begin %s duplicate recovery-job plan probe: %v", fixture.engine, err)
		}
		probeTable := "recovery_job_plan_unique_enforced_probe"
		if dropUniqueIndex {
			probeTable = "recovery_job_plan_unique_control_probe"
		}
		if _, err = tx.Exec(fixture.bind(`CREATE TEMP TABLE `+probeTable+`
			AS SELECT * FROM backup_asset_recovery_jobs WHERE id = ?`), aggregate.JobID); err == nil {
			_, err = tx.Exec(fixture.bind(`UPDATE `+probeTable+` SET id = ?`), recoveryMigrationOpaqueID(699996))
		}
		if err == nil && dropUniqueIndex {
			if _, err = tx.Exec(`DROP INDEX idx_backup_asset_recovery_jobs_plan`); err != nil {
				_ = tx.Rollback()
				t.Fatalf("drop %s recovery-job plan unique index in probe: %v", fixture.engine, err)
			}
		}
		execErr := err
		if execErr == nil {
			_, execErr = tx.Exec(`INSERT INTO backup_asset_recovery_jobs SELECT * FROM ` + probeTable)
		}
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			t.Fatalf("rollback %s duplicate recovery-job plan probe: %v", fixture.engine, rollbackErr)
		}
		return execErr
	}
	if err := tryDuplicate(true); err != nil {
		t.Fatalf("%s duplicate recovery-job control failed without the plan unique index: %v", fixture.engine, err)
	}
	if err := tryDuplicate(false); err == nil {
		t.Fatalf("%s duplicate recovery job unexpectedly bypassed the sole plan unique index", fixture.engine)
	}
}

func (fixture migrationFixture) recoveryJobPlanIDUniqueStructureCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	if fixture.engine == "postgres" {
		var count int
		err := db.QueryRow(`SELECT COUNT(*)
			FROM pg_index AS index_row
			JOIN pg_class AS relation ON relation.oid = index_row.indrelid
			JOIN pg_namespace AS namespace ON namespace.oid = relation.relnamespace
			WHERE namespace.nspname = current_schema()
			  AND relation.relname = 'backup_asset_recovery_jobs'
			  AND index_row.indisunique
			  AND index_row.indnkeyatts = 1
			  AND (
				SELECT string_agg(attribute.attname, ',' ORDER BY key_column.ordinality)
				FROM unnest(index_row.indkey::smallint[]) WITH ORDINALITY AS key_column(attnum, ordinality)
				JOIN pg_attribute AS attribute
				  ON attribute.attrelid = relation.oid AND attribute.attnum = key_column.attnum
				WHERE key_column.ordinality <= index_row.indnkeyatts
			  ) = 'plan_id'`).Scan(&count)
		if err != nil {
			t.Fatalf("count PostgreSQL recovery job plan unique structures: %v", err)
		}
		return count
	}

	rows, err := db.Query(`PRAGMA index_list('backup_asset_recovery_jobs')`)
	if err != nil {
		t.Fatalf("list SQLite recovery job indexes: %v", err)
	}
	var uniqueIndexes []string
	for rows.Next() {
		var sequence, isUnique, partial int
		var name, origin string
		if err := rows.Scan(&sequence, &name, &isUnique, &origin, &partial); err != nil {
			_ = rows.Close()
			t.Fatalf("scan SQLite recovery job index: %v", err)
		}
		if isUnique != 0 {
			uniqueIndexes = append(uniqueIndexes, name)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		t.Fatalf("iterate SQLite recovery job indexes: %v", err)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close SQLite recovery job indexes: %v", err)
	}

	count := 0
	for _, index := range uniqueIndexes {
		columnRows, err := db.Query(fmt.Sprintf("PRAGMA index_info(%q)", index))
		if err != nil {
			t.Fatalf("list SQLite recovery job index %s columns: %v", index, err)
		}
		var columns []string
		for columnRows.Next() {
			var sequence, columnNumber int
			var column string
			if err := columnRows.Scan(&sequence, &columnNumber, &column); err != nil {
				_ = columnRows.Close()
				t.Fatalf("scan SQLite recovery job index %s column: %v", index, err)
			}
			columns = append(columns, column)
		}
		if err := columnRows.Err(); err != nil {
			_ = columnRows.Close()
			t.Fatalf("iterate SQLite recovery job index %s columns: %v", index, err)
		}
		if err := columnRows.Close(); err != nil {
			t.Fatalf("close SQLite recovery job index %s columns: %v", index, err)
		}
		if len(columns) == 1 && columns[0] == "plan_id" {
			count++
		}
	}
	return count
}

func (fixture migrationFixture) test069ClosedProductsAndContentRecoveryResultArm(t *testing.T) {
	_, db := fixture.openAt(t, backupAssetRecoveryVersion)
	now := time.Now().UTC().Truncate(time.Second)
	aggregate := fixture.seedRecoveryMigrationAggregate(t, db, "a", 1, now)

	fixture.expectExecRejected(t, db, `UPDATE backup_asset_recovery_jobs
		SET state = 'queued' WHERE id = ?`, aggregate.JobID)
	fixture.expectExecRejected(t, db, `UPDATE backup_asset_recovery_result_sets
		SET hard_deadline = created_at WHERE id = ?`, aggregate.ResultSetID)
	fixture.expectExecRejected(t, db, `UPDATE backup_asset_recovery_job_items
		SET plan_id = ? WHERE id = ?`, recoveryMigrationOpaqueID(699999), aggregate.JobItemID)
	fixture.expectExecRejected(t, db, `INSERT INTO backup_asset_recovery_node_leases
		(id, node_id, holder_kind, job_id, attempt_id, owner_id, fence, state,
		 lease_expires_at, created_at, updated_at)
		VALUES (?, ?, 'recovery_job', ?, ?, 'second-worker', 2, 'active', ?, ?, ?)`,
		recoveryMigrationOpaqueID(699998), aggregate.NodeID, aggregate.JobID, aggregate.AttemptID,
		now.Add(30*time.Minute), now, now)

	contentGrantID := fixture.insertRecoveryMigrationContentGrant(t, db, aggregate, 1, now)
	var resourceKind, contentJobID, contentResultID string
	if err := db.QueryRow(fixture.bind(`SELECT resource_kind, recovery_job_id, recovery_result_id
		FROM backup_asset_delivery_grants WHERE id = ?`), contentGrantID).Scan(
		&resourceKind, &contentJobID, &contentResultID,
	); err != nil {
		t.Fatalf("read %s valid RecoveryResult Content grant: %v", fixture.engine, err)
	}
	if resourceKind != "recovery_result" || contentJobID != aggregate.JobID || contentResultID != aggregate.ResultID {
		t.Fatalf("%s RecoveryResult Content grant=%q/%q/%q, want recovery_result/%q/%q",
			fixture.engine, resourceKind, contentJobID, contentResultID, aggregate.JobID, aggregate.ResultID)
	}
	fixture.expectExecAcceptedWithoutRecoveryContentBindingTriggerInRollback(t, db, `UPDATE backup_asset_delivery_grants
		SET step_up_proof_id = ? WHERE id = ?`, strings.Repeat("b", 32), contentGrantID)
	fixture.expectExecRejectedWithoutRecoveryContentBindingTriggerInRollback(t, db, `UPDATE backup_asset_delivery_grants
		SET recovery_result_id = NULL WHERE id = ?`, contentGrantID)
	fixture.expectExecRejectedWithoutRecoveryContentBindingTriggerInRollback(t, db, `UPDATE backup_asset_delivery_grants
		SET recovery_point_id = ? WHERE id = ?`, aggregate.PointID, contentGrantID)
	other := fixture.seedRecoveryMigrationAggregate(t, db, "b", 2, now.Add(time.Second))
	fixture.expectExecRejectedWithoutRecoveryContentBindingTriggerInRollback(t, db, `UPDATE backup_asset_delivery_grants
		SET recovery_job_id = ? WHERE id = ?`, other.JobID, contentGrantID)
	if fixture.engine == "sqlite" {
		assertSQLiteForeignKeyCheck(t, db)
	}

	// Use a separate active graph for plan-only probes. The published terminal
	// graph above remains durable so the test never bypasses terminal identity.
	planAggregate := fixture.seedRecoveryMigrationAggregate(t, db, "c", 3, now.Add(2*time.Second), true)
	fixture.removeRecoveryMigrationActiveJobGraph(t, db, planAggregate)
	fixture.expectExecAcceptedInRollback(t, db, `UPDATE backup_asset_recovery_plans
		SET source_revision_kind = source_revision_kind WHERE id = ?`, planAggregate.PlanID)
	fixture.expectExecRejected(t, db, `UPDATE backup_asset_recovery_plans
		SET source_revision_kind = 'observation' WHERE id = ?`, planAggregate.PlanID)
	fixture.expectExecRejected(t, db, `UPDATE backup_asset_recovery_plans
		SET operation_set_digest = ? WHERE id = ?`, strings.Repeat("a", 63), planAggregate.PlanID)
	fixture.expectExecRejected(t, db, `UPDATE backup_asset_recovery_plans
		SET delete_set_digest = ? WHERE id = ?`, strings.Repeat("a", 64), planAggregate.PlanID)
}

func (fixture migrationFixture) test069ContentAuthorizationRequiresExactStepUp(t *testing.T) {
	_, db := fixture.openAt(t, backupAssetRecoveryVersion)
	now := time.Now().UTC().Truncate(time.Second)
	aggregate := fixture.seedRecoveryMigrationAggregate(t, db, "e", 15, now)
	contentGrantID := fixture.insertRecoveryMigrationContentGrant(t, db, aggregate, 15, now)
	assetLeaseID := recoveryMigrationOpaqueID(697160)
	assetGrantID := recoveryMigrationOpaqueID(697161)
	fixture.insertSearchMigrationLease(t, db, assetLeaseID, aggregate.PointID, "content_session", now)
	fixture.insertContentMigrationGrant(t, db, aggregate.UserID, assetGrantID, recoveryMigrationOpaqueID(697162),
		aggregate.PointID, aggregate.CatalogID, aggregate.EntryID, assetLeaseID, now)
	fixture.mustExec(t, db, `UPDATE backup_asset_delivery_grants
		SET action = 'download', range_policy = 'single', renderer = 'attachment', profile = 'original_v1',
			classification = 'non_secret', step_up_action = 'asset.download', step_up_proof_id = ?,
			step_up_expires_at = ?
		WHERE id = ?`, strings.Repeat("a", 32), now.Add(20*time.Minute), assetGrantID)
	fixture.expectExecAcceptedWithoutRecoveryContentBindingTriggerInRollback(t, db, `UPDATE backup_asset_delivery_grants
		SET step_up_proof_id = ? WHERE id = ?`, strings.Repeat("b", 32), contentGrantID)

	var unexpectedlyAllowed []string
	assertRejected := func(name string, dropRecoveryBindingTrigger bool, query string, args ...any) {
		t.Helper()
		if dropRecoveryBindingTrigger {
			if err := fixture.execWithoutRecoveryContentBindingTriggerInRollback(t, db, query, args...); err == nil {
				unexpectedlyAllowed = append(unexpectedlyAllowed, name)
			}
			return
		}
		tx, err := db.Begin()
		if err != nil {
			t.Fatalf("begin %s %s Content authorization probe: %v", fixture.engine, name, err)
		}
		defer func() {
			if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
				t.Fatalf("rollback %s %s Content authorization probe: %v", fixture.engine, name, err)
			}
		}()
		if _, err := tx.Exec(fixture.bind(query), args...); err == nil {
			unexpectedlyAllowed = append(unexpectedlyAllowed, name)
		}
	}

	testCases := []struct {
		name  string
		query string
		args  []any
	}{
		{name: "step-up action NULL", query: `UPDATE backup_asset_delivery_grants
			SET step_up_action = NULL WHERE id = ?`},
		{name: "short proof id", query: `UPDATE backup_asset_delivery_grants
			SET step_up_proof_id = ? WHERE id = ?`, args: []any{strings.Repeat("a", 31)}},
		{name: "non-hex proof id", query: `UPDATE backup_asset_delivery_grants
			SET step_up_proof_id = ? WHERE id = ?`, args: []any{strings.Repeat("g", 32)}},
		{name: "uppercase proof id", query: `UPDATE backup_asset_delivery_grants
			SET step_up_proof_id = ? WHERE id = ?`, args: []any{strings.Repeat("A", 32)}},
	}
	for _, arm := range []struct {
		name, grantID, wrongStepUpAction string
		dropRecoveryBindingTrigger       bool
	}{
		{name: "RecoveryResult", grantID: contentGrantID, wrongStepUpAction: "asset.download", dropRecoveryBindingTrigger: true},
		{name: "existing backup-asset", grantID: assetGrantID, wrongStepUpAction: "recovery.result_download"},
	} {
		assertRejected(arm.name+" wrong step-up action", arm.dropRecoveryBindingTrigger, `UPDATE backup_asset_delivery_grants
			SET step_up_action = ? WHERE id = ?`, arm.wrongStepUpAction, arm.grantID)
		for _, testCase := range testCases {
			args := append(append([]any(nil), testCase.args...), arm.grantID)
			assertRejected(arm.name+" "+testCase.name, arm.dropRecoveryBindingTrigger, testCase.query, args...)
		}
	}

	if len(unexpectedlyAllowed) != 0 {
		t.Fatalf("%s RecoveryResult Content authorization unexpectedly allowed: %s", fixture.engine, strings.Join(unexpectedlyAllowed, ", "))
	}
}

func (fixture migrationFixture) test069ResultSetCleanupFenceInvariant(t *testing.T) {
	t.Run("CleanedResultSetCannotLoseAllocatedCleanupFence", func(t *testing.T) {
		_, db := fixture.openAt(t, backupAssetRecoveryVersion)
		now := time.Now().UTC().Truncate(time.Second)
		aggregate := fixture.seedRecoveryMigrationAggregate(t, db, "d", 40, now)
		cleanedAt := now.Add(time.Minute)

		fixture.mustExec(t, db, `UPDATE backup_asset_recovery_result_sets
			SET state = 'revoking', cleanup_owner = 'cleanup-worker', cleanup_lease_expires_at = ?,
				cleanup_fence = 1, node_lease_id = ?, node_fence = 1, cleanup_attempt = 1,
				updated_at = ? WHERE id = ?`, now.Add(30*time.Minute), aggregate.NodeLeaseID,
			now.Add(30*time.Second), aggregate.ResultSetID)
		fixture.mustExec(t, db, `UPDATE backup_asset_recovery_result_sets
			SET state = 'cleaned', cleanup_phase = 'tombstoned', cleanup_owner = '',
				cleanup_lease_expires_at = NULL, cleanup_fence = 1, node_lease_id = NULL,
				node_fence = 0, cleanup_attempt = 1, updated_at = ?
			WHERE id = ?`, cleanedAt, aggregate.ResultSetID)
		fixture.expectExecRejectedInRollback(t, db, `UPDATE backup_asset_recovery_result_sets
			SET cleanup_fence = 0, updated_at = ? WHERE id = ?`,
			cleanedAt.Add(time.Second), aggregate.ResultSetID)
	})

	t.Run("ReadyResultSetCannotTransitionDirectlyToZeroFenceTombstone", func(t *testing.T) {
		_, db := fixture.openAt(t, backupAssetRecoveryVersion)
		now := time.Now().UTC().Truncate(time.Second)
		aggregate := fixture.seedRecoveryMigrationAggregate(t, db, "e", 41, now)

		fixture.expectExecRejectedInRollback(t, db, `UPDATE backup_asset_recovery_result_sets
			SET state = 'cleaned', cleanup_phase = 'tombstoned', cleanup_owner = '',
				cleanup_lease_expires_at = NULL, cleanup_fence = 0, node_lease_id = NULL,
				node_fence = 0, cleanup_attempt = 1, updated_at = ?
			WHERE id = ? AND state = 'ready'`, now.Add(time.Minute), aggregate.ResultSetID)
	})

	t.Run("CleanedResultSetWithAllocatedFenceRemainsValid", func(t *testing.T) {
		_, db := fixture.openAt(t, backupAssetRecoveryVersion)
		now := time.Now().UTC().Truncate(time.Second)
		aggregate := fixture.seedRecoveryMigrationAggregate(t, db, "f", 42, now)
		cleanedAt := now.Add(time.Minute)

		fixture.mustExec(t, db, `UPDATE backup_asset_recovery_result_sets
			SET state = 'revoking', cleanup_owner = 'cleanup-worker', cleanup_lease_expires_at = ?,
				cleanup_fence = 1, node_lease_id = ?, node_fence = 1, cleanup_attempt = 1,
				updated_at = ? WHERE id = ?`, now.Add(30*time.Minute), aggregate.NodeLeaseID,
			now.Add(30*time.Second), aggregate.ResultSetID)
		fixture.mustExec(t, db, `UPDATE backup_asset_recovery_result_sets
			SET state = 'cleaned', cleanup_phase = 'tombstoned', cleanup_owner = '',
				cleanup_lease_expires_at = NULL, cleanup_fence = 1, node_lease_id = NULL,
				node_fence = 0, cleanup_attempt = 1, updated_at = ?
			WHERE id = ?`, cleanedAt, aggregate.ResultSetID)
		var cleanupFence int64
		if err := db.QueryRow(fixture.bind(`SELECT cleanup_fence
			FROM backup_asset_recovery_result_sets WHERE id = ?`), aggregate.ResultSetID).Scan(&cleanupFence); err != nil {
			t.Fatalf("read %s cleaned result-set cleanup fence: %v", fixture.engine, err)
		}
		if cleanupFence != 1 {
			t.Fatalf("%s cleaned result-set cleanup fence=%d, want 1", fixture.engine, cleanupFence)
		}
	})

	t.Run("CleanupFailedRetriesNeedFreshFence", func(t *testing.T) {
		_, db := fixture.openAt(t, backupAssetRecoveryVersion)
		now := time.Now().UTC().Truncate(time.Second)
		aggregate := fixture.seedRecoveryMigrationAggregate(t, db, "a", 43, now)
		fixture.mustExec(t, db, `UPDATE backup_asset_recovery_result_sets
			SET state = 'revoking', cleanup_owner = 'cleanup-worker', cleanup_lease_expires_at = ?,
				cleanup_fence = 1, node_lease_id = ?, node_fence = 1, cleanup_attempt = 1,
				updated_at = ? WHERE id = ?`, now.Add(30*time.Minute), aggregate.NodeLeaseID,
			now.Add(time.Minute), aggregate.ResultSetID)
		fixture.mustExec(t, db, `UPDATE backup_asset_recovery_result_sets
			SET state = 'cleanup_failed', cleanup_owner = '', cleanup_lease_expires_at = NULL,
				node_lease_id = NULL, node_fence = 0, updated_at = ? WHERE id = ?`,
			now.Add(2*time.Minute), aggregate.ResultSetID)
		fixture.expectExecRejectedInRollback(t, db, `UPDATE backup_asset_recovery_result_sets
			SET state = 'revoking', cleanup_owner = 'retry-worker', cleanup_lease_expires_at = ?,
				node_lease_id = ?, node_fence = 2, updated_at = ? WHERE id = ?`,
			now.Add(30*time.Minute), aggregate.NodeLeaseID, now.Add(3*time.Minute), aggregate.ResultSetID)
		fixture.mustExec(t, db, `UPDATE backup_asset_recovery_result_sets
			SET state = 'revoking', cleanup_owner = 'retry-worker', cleanup_lease_expires_at = ?,
				cleanup_fence = 2, node_lease_id = ?, node_fence = 2, cleanup_attempt = 2,
				updated_at = ? WHERE id = ?`, now.Add(30*time.Minute), aggregate.NodeLeaseID,
			now.Add(4*time.Minute), aggregate.ResultSetID)
	})
}

func (fixture migrationFixture) test069CleanedResultSetTombstonePermanence(t *testing.T) {
	t.Run("legal ready revoking cleaned path remains available", func(t *testing.T) {
		_, db := fixture.openAt(t, backupAssetRecoveryVersion)
		now := time.Now().UTC().Truncate(time.Second)
		aggregate := fixture.seedRecoveryMigrationAggregate(t, db, "8", 157, now)
		fixture.transitionRecoveryResultSetToCleaned(t, db, aggregate, now)

		var state string
		if err := db.QueryRow(fixture.bind(`SELECT state FROM backup_asset_recovery_result_sets WHERE id = ?`),
			aggregate.ResultSetID).Scan(&state); err != nil {
			t.Fatalf("read %s cleaned result-set state: %v", fixture.engine, err)
		}
		if state != "cleaned" {
			t.Fatalf("%s result-set state=%q, want cleaned", fixture.engine, state)
		}
	})

	t.Run("cleaned tombstone cannot be deleted", func(t *testing.T) {
		_, db := fixture.openAt(t, backupAssetRecoveryVersion)
		now := time.Now().UTC().Truncate(time.Second)
		aggregate := fixture.seedRecoveryMigrationAggregate(t, db, "9", 158, now)
		fixture.transitionRecoveryResultSetToCleaned(t, db, aggregate, now)
		fixture.expectExecRejectedInRollback(t, db,
			`DELETE FROM backup_asset_recovery_result_sets WHERE id = ?`, aggregate.ResultSetID)
	})

	t.Run("cleaned plaintext deadline cannot be extended", func(t *testing.T) {
		_, db := fixture.openAt(t, backupAssetRecoveryVersion)
		now := time.Now().UTC().Truncate(time.Second)
		aggregate := fixture.seedRecoveryMigrationAggregate(t, db, "a", 159, now)
		fixture.transitionRecoveryResultSetToCleaned(t, db, aggregate, now)
		fixture.expectExecRejectedInRollback(t, db, `UPDATE backup_asset_recovery_result_sets
			SET plaintext_deadline = ?, updated_at = ? WHERE id = ?`,
			now.Add(150*time.Minute), now.Add(3*time.Minute), aggregate.ResultSetID)
	})

	t.Run("cleaned hard deadline remains immutable", func(t *testing.T) {
		_, db := fixture.openAt(t, backupAssetRecoveryVersion)
		now := time.Now().UTC().Truncate(time.Second)
		aggregate := fixture.seedRecoveryMigrationAggregate(t, db, "b", 160, now)
		fixture.transitionRecoveryResultSetToCleaned(t, db, aggregate, now)
		fixture.expectExecRejectedInRollback(t, db, `UPDATE backup_asset_recovery_result_sets
			SET hard_deadline = ?, updated_at = ? WHERE id = ?`,
			now.Add(4*time.Hour), now.Add(3*time.Minute), aggregate.ResultSetID)
	})

	for index, testCase := range []struct {
		name      string
		marker    string
		sequence  int
		rebuildID func(recoveryMigrationAggregate) string
	}{
		{
			name:     "cleaned tombstone cannot be deleted and rebuilt with the same ID",
			marker:   "c",
			sequence: 161,
			rebuildID: func(aggregate recoveryMigrationAggregate) string {
				return aggregate.ResultSetID
			},
		},
		{
			name:     "cleaned tombstone cannot be deleted and rebuilt for the same job",
			marker:   "d",
			sequence: 162,
			rebuildID: func(recoveryMigrationAggregate) string {
				return recoveryMigrationOpaqueID(719900)
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, db := fixture.openAt(t, backupAssetRecoveryVersion)
			now := time.Now().UTC().Truncate(time.Second)
			aggregate := fixture.seedRecoveryMigrationAggregate(t, db, testCase.marker, testCase.sequence, now)
			fixture.transitionRecoveryResultSetToCleaned(t, db, aggregate, now)
			fixture.expectCleanedResultSetDeleteAndRebuildRejected(
				t, db, aggregate, testCase.rebuildID(aggregate), now.Add(time.Duration(index+4)*time.Hour), now,
			)
		})
	}
}

func (fixture migrationFixture) test069CleanedResultSetSQLiteReplacementBarrier(t *testing.T) {
	if fixture.engine != "sqlite" {
		t.Fatal("cleaned ResultSet replacement barrier is SQLite-specific")
	}
	for _, testCase := range []struct {
		name      string
		marker    string
		sequence  int
		replaceID func(recoveryMigrationAggregate) string
	}{
		{
			name:     "same ID",
			marker:   "e",
			sequence: 163,
			replaceID: func(aggregate recoveryMigrationAggregate) string {
				return aggregate.ResultSetID
			},
		},
		{
			name:     "same job",
			marker:   "f",
			sequence: 164,
			replaceID: func(recoveryMigrationAggregate) string {
				return recoveryMigrationOpaqueID(719901)
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, db := fixture.openAt(t, backupAssetRecoveryVersion)
			now := time.Now().UTC().Truncate(time.Second)
			aggregate := fixture.seedRecoveryMigrationAggregate(t, db, testCase.marker, testCase.sequence, now)
			fixture.transitionRecoveryResultSetToCleaned(t, db, aggregate, now)
			fixture.expectExecRejectedInRollback(t, db, `INSERT OR REPLACE INTO backup_asset_recovery_result_sets
				(id, job_id, state, marker_binding_digest, plaintext_deadline, hard_deadline,
				 cleanup_phase, created_at, updated_at)
				SELECT ?, id, 'ready', workspace_marker_binding_digest, plaintext_deadline, ?,
				 'claimed', ?, ? FROM backup_asset_recovery_jobs WHERE id = ?`,
				testCase.replaceID(aggregate), now.Add(4*time.Hour), now.Add(time.Minute),
				now.Add(time.Minute), aggregate.JobID)
		})
	}
}

func (fixture migrationFixture) transitionRecoveryResultSetToCleaned(
	t *testing.T,
	db *sql.DB,
	aggregate recoveryMigrationAggregate,
	now time.Time,
) {
	t.Helper()
	fixture.mustExec(t, db, `UPDATE backup_asset_recovery_result_sets
		SET state = 'revoking', cleanup_owner = 'cleanup-worker', cleanup_lease_expires_at = ?,
			cleanup_fence = 1, node_lease_id = ?, node_fence = 1, cleanup_attempt = 1,
			updated_at = ? WHERE id = ?`,
		now.Add(30*time.Minute), aggregate.NodeLeaseID, now.Add(time.Minute), aggregate.ResultSetID)
	fixture.mustExec(t, db, `UPDATE backup_asset_recovery_result_sets
		SET state = 'cleaned', cleanup_phase = 'tombstoned', cleanup_owner = '',
			cleanup_lease_expires_at = NULL, node_lease_id = NULL, node_fence = 0,
			updated_at = ? WHERE id = ?`, now.Add(2*time.Minute), aggregate.ResultSetID)
}

func (fixture migrationFixture) expectCleanedResultSetDeleteAndRebuildRejected(
	t *testing.T,
	db *sql.DB,
	aggregate recoveryMigrationAggregate,
	rebuildID string,
	hardDeadline time.Time,
	now time.Time,
) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin %s cleaned result-set delete/rebuild probe: %v", fixture.engine, err)
	}
	if _, err = tx.Exec(fixture.bind(`DELETE FROM backup_asset_recovery_result_sets WHERE id = ?`),
		aggregate.ResultSetID); err == nil {
		_, err = tx.Exec(fixture.bind(`INSERT INTO backup_asset_recovery_result_sets
			(id, job_id, state, marker_binding_digest, plaintext_deadline, hard_deadline,
			 cleanup_phase, created_at, updated_at)
			SELECT ?, id, 'ready', workspace_marker_binding_digest, plaintext_deadline, ?,
			 'claimed', ?, ? FROM backup_asset_recovery_jobs WHERE id = ?`),
			rebuildID, hardDeadline, now.Add(time.Minute), now.Add(time.Minute), aggregate.JobID)
	}
	if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
		t.Fatalf("rollback %s cleaned result-set delete/rebuild probe: %v", fixture.engine, rollbackErr)
	}
	if err == nil {
		t.Fatalf("%s cleaned result-set tombstone was deleted and rebuilt as ready", fixture.engine)
	}
}

func (fixture migrationFixture) test069AttemptMutationArmTerminalAndTakeoverInvariant(t *testing.T) {
	t.Run("ArmedAttemptsCanReachTerminalStates", func(t *testing.T) {
		terminalStates := []struct {
			state  string
			marker string
		}{
			{state: "lost", marker: "a"},
			{state: "failed", marker: "b"},
			{state: "completed", marker: "c"},
			{state: "canceled", marker: "d"},
		}
		for sequence, testCase := range terminalStates {
			t.Run(testCase.state, func(t *testing.T) {
				_, db := fixture.openAt(t, backupAssetRecoveryVersion)
				now := time.Now().UTC().Truncate(time.Second)
				aggregate := fixture.seedRecoveryMigrationAggregate(t, db, testCase.marker, 50+sequence, now, true)
				closedAt := now.Add(time.Duration(sequence+1) * time.Minute)
				fixture.mustExec(t, db, `UPDATE backup_asset_recovery_attempts
					SET state = ?, mutation_armed = ?, closed_at = ?, updated_at = ?
					WHERE id = ?`, testCase.state, true, closedAt, closedAt, aggregate.AttemptID)

				var gotState string
				var mutationArmed bool
				if err := db.QueryRow(fixture.bind(`SELECT state, mutation_armed
					FROM backup_asset_recovery_attempts WHERE id = ?`), aggregate.AttemptID).Scan(&gotState, &mutationArmed); err != nil {
					t.Fatalf("read %s armed terminal attempt: %v", fixture.engine, err)
				}
				if gotState != testCase.state || !mutationArmed {
					t.Fatalf("%s terminal attempt=%q armed=%t, want %q/true", fixture.engine, gotState, mutationArmed, testCase.state)
				}
			})
		}
	})

	t.Run("ClaimedAttemptCannotArm", func(t *testing.T) {
		_, db := fixture.openAt(t, backupAssetRecoveryVersion)
		now := time.Now().UTC().Truncate(time.Second)
		aggregate := fixture.seedRecoveryMigrationAggregate(t, db, "e", 60, now, true)
		fixture.expectExecRejectedInRollback(t, db, `UPDATE backup_asset_recovery_attempts
			SET state = 'claimed', mutation_armed = ?, closed_at = NULL, updated_at = ?
			WHERE id = ?`, true, now.Add(time.Minute), aggregate.AttemptID)
	})

	t.Run("MutationArmCannotBeCleared", func(t *testing.T) {
		_, db := fixture.openAt(t, backupAssetRecoveryVersion)
		now := time.Now().UTC().Truncate(time.Second)
		aggregate := fixture.seedRecoveryMigrationAggregate(t, db, "f", 61, now, true)
		fixture.mustExec(t, db, `UPDATE backup_asset_recovery_attempts
			SET mutation_armed = ?, updated_at = ? WHERE id = ?`, true, now.Add(time.Minute), aggregate.AttemptID)
		fixture.expectExecRejectedInRollback(t, db, `UPDATE backup_asset_recovery_attempts
			SET mutation_armed = ?, updated_at = ? WHERE id = ?`, false, now.Add(2*time.Minute), aggregate.AttemptID)
	})

	t.Run("TerminalTakeoverCannotEraseArm", func(t *testing.T) {
		_, db := fixture.openAt(t, backupAssetRecoveryVersion)
		now := time.Now().UTC().Truncate(time.Second)
		aggregate := fixture.seedRecoveryMigrationAggregate(t, db, "a", 62, now, true)
		fixture.mustExec(t, db, `UPDATE backup_asset_recovery_attempts
			SET mutation_armed = ?, updated_at = ? WHERE id = ?`, true, now.Add(time.Minute), aggregate.AttemptID)

		tx, err := db.Begin()
		if err != nil {
			t.Fatalf("begin %s armed takeover transition: %v", fixture.engine, err)
		}
		closedAt := now.Add(2 * time.Minute)
		if _, err = tx.Exec(fixture.bind(`UPDATE backup_asset_recovery_attempts
			SET state = 'lost', mutation_armed = ?, closed_at = ?, updated_at = ?
			WHERE id = ?`), false, closedAt, closedAt, aggregate.AttemptID); err == nil {
			_, err = tx.Exec(fixture.bind(`INSERT INTO backup_asset_recovery_attempts
				(id, job_id, owner_id, fence, state, mutation_armed, lease_expires_at, heartbeat_at, created_at, updated_at)
				VALUES (?, ?, 'takeover-worker', 2, 'claimed', ?, ?, ?, ?, ?)`),
				recoveryMigrationOpaqueID(706201), aggregate.JobID, false, now.Add(30*time.Minute), now, now, now)
		}
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			t.Fatalf("rollback %s armed takeover transition: %v", fixture.engine, rollbackErr)
		}
		if err == nil {
			t.Fatalf("%s terminal takeover erased a durable mutation arm", fixture.engine)
		}
	})

	t.Run("ArmedTerminalAttemptStillDeniesPreWriteSupersedeAfterTakeover", func(t *testing.T) {
		_, db := fixture.openAt(t, backupAssetRecoveryVersion)
		now := time.Now().UTC().Truncate(time.Second)
		aggregate := fixture.seedRecoveryMigrationAggregate(t, db, "b", 63, now, true)
		closedAt := now.Add(time.Minute)
		fixture.mustExec(t, db, `UPDATE backup_asset_recovery_attempts
			SET state = 'lost', mutation_armed = ?, closed_at = ?, updated_at = ?
			WHERE id = ?`, true, closedAt, closedAt, aggregate.AttemptID)

		takeoverID := recoveryMigrationOpaqueID(706301)
		fixture.mustExec(t, db, `INSERT INTO backup_asset_recovery_attempts
			(id, job_id, owner_id, fence, state, mutation_armed, lease_expires_at, heartbeat_at, created_at, updated_at)
			VALUES (?, ?, 'takeover-worker', 2, 'claimed', ?, ?, ?, ?, ?)`,
			takeoverID, aggregate.JobID, false, now.Add(30*time.Minute), now, now, now)

		var earlierArm bool
		if err := db.QueryRow(fixture.bind(`SELECT mutation_armed FROM backup_asset_recovery_attempts
			WHERE id = ?`), aggregate.AttemptID).Scan(&earlierArm); err != nil {
			t.Fatalf("read %s terminal armed attempt: %v", fixture.engine, err)
		}
		if !earlierArm {
			t.Fatalf("%s terminal attempt lost its durable mutation arm", fixture.engine)
		}
		var takeoverState string
		if err := db.QueryRow(fixture.bind(`SELECT state FROM backup_asset_recovery_attempts
			WHERE id = ?`), takeoverID).Scan(&takeoverState); err != nil {
			t.Fatalf("read %s takeover attempt: %v", fixture.engine, err)
		}
		if takeoverState != "claimed" {
			t.Fatalf("%s takeover state=%q, want claimed", fixture.engine, takeoverState)
		}
		if recovery.PlanStateExecuted.CanTransitionTo(recovery.PlanStateSuperseded, recovery.PlanTransitionGuard{
			HasDurableJob:        true,
			HasCurrentFence:      true,
			MutationArmed:        earlierArm,
			TargetAtBaseRevision: true,
		}) {
			t.Fatalf("%s armed terminal attempt allowed executed -> superseded after takeover", fixture.engine)
		}
	})
}

func (fixture migrationFixture) test069TerminalAttemptIntegrity(t *testing.T) {
	for index, terminalState := range []string{"completed", "failed", "canceled", "superseded", "lost"} {
		t.Run(terminalState+" identity", func(t *testing.T) {
			_, db := fixture.openAt(t, backupAssetRecoveryVersion)
			now := time.Now().UTC().Truncate(time.Second)
			aggregate := fixture.seedRecoveryMigrationAggregate(t, db, fmt.Sprintf("%x", index+1), 100+index, now, true)
			closedAt := now.Add(time.Minute)
			mutationArmed := terminalState != "superseded"
			fixture.mustExec(t, db, `UPDATE backup_asset_recovery_attempts
				SET state = ?, mutation_armed = ?, closed_at = ?, updated_at = ?
				WHERE id = ?`, terminalState, mutationArmed, closedAt, closedAt, aggregate.AttemptID)

			fixture.expectExecRejectedInRollback(t, db, `UPDATE backup_asset_recovery_attempts
				SET owner_id = 'replacement-worker' WHERE id = ?`, aggregate.AttemptID)
			fixture.expectExecRejectedInRollback(t, db, `UPDATE backup_asset_recovery_attempts
				SET fence = fence + 1 WHERE id = ?`, aggregate.AttemptID)
		})
	}

	t.Run("lost cannot resume", func(t *testing.T) {
		_, db := fixture.openAt(t, backupAssetRecoveryVersion)
		now := time.Now().UTC().Truncate(time.Second)
		aggregate := fixture.seedRecoveryMigrationAggregate(t, db, "6", 106, now, true)
		closedAt := now.Add(time.Minute)
		fixture.mustExec(t, db, `UPDATE backup_asset_recovery_attempts
			SET state = 'lost', mutation_armed = ?, closed_at = ?, updated_at = ?
			WHERE id = ?`, true, closedAt, closedAt, aggregate.AttemptID)
		fixture.expectExecRejectedInRollback(t, db, `UPDATE backup_asset_recovery_attempts
			SET state = 'running', closed_at = NULL, updated_at = ? WHERE id = ?`,
			closedAt.Add(time.Second), aggregate.AttemptID)
	})

	t.Run("terminal attempt cannot retroactively arm", func(t *testing.T) {
		_, db := fixture.openAt(t, backupAssetRecoveryVersion)
		now := time.Now().UTC().Truncate(time.Second)
		aggregate := fixture.seedRecoveryMigrationAggregate(t, db, "7", 131, now, true)
		closedAt := now.Add(time.Minute)
		fixture.mustExec(t, db, `UPDATE backup_asset_recovery_attempts
			SET state = 'lost', mutation_armed = ?, closed_at = ?, updated_at = ?
			WHERE id = ?`, false, closedAt, closedAt, aggregate.AttemptID)
		fixture.expectExecRejectedInRollback(t, db, `UPDATE backup_asset_recovery_attempts
			SET mutation_armed = ?, updated_at = ? WHERE id = ?`, true, closedAt.Add(time.Second), aggregate.AttemptID)
	})
}

func (fixture migrationFixture) test069PublicationAndDeadlineIntegrity(t *testing.T) {
	t.Run("publication marker parity", func(t *testing.T) {
		_, db := fixture.openAt(t, backupAssetRecoveryVersion)
		now := time.Now().UTC().Truncate(time.Second)
		aggregate := fixture.seedRecoveryMigrationAggregate(t, db, "7", 107, now)

		fixture.expectExecRejectedInRollback(t, db, `UPDATE backup_asset_recovery_jobs
			SET workspace_marker_binding_digest = ? WHERE id = ?`, recoveryMigrationDigest(699913), aggregate.JobID)
		fixture.expectExecRejectedInRollback(t, db, `UPDATE backup_asset_recovery_result_sets
			SET marker_binding_digest = ? WHERE id = ?`, recoveryMigrationDigest(699914), aggregate.ResultSetID)

		fixture.mustExec(t, db, `DELETE FROM backup_asset_recovery_results WHERE result_set_id = ?`, aggregate.ResultSetID)
		fixture.mustExec(t, db, `DELETE FROM backup_asset_recovery_result_sets WHERE id = ?`, aggregate.ResultSetID)
		fixture.expectExecRejectedInRollback(t, db, `INSERT INTO backup_asset_recovery_result_sets
			(id, job_id, state, marker_binding_digest, plaintext_deadline, hard_deadline,
			 cleanup_phase, created_at, updated_at)
			SELECT ?, id, 'ready', ?, plaintext_deadline, ?, 'claimed', ?, ?
			FROM backup_asset_recovery_jobs WHERE id = ?`,
			recoveryMigrationOpaqueID(699915), recoveryMigrationDigest(699915), now.Add(3*time.Hour),
			now, now, aggregate.JobID)
	})

	t.Run("active attempt blocks publication", func(t *testing.T) {
		_, db := fixture.openAt(t, backupAssetRecoveryVersion)
		now := time.Now().UTC().Truncate(time.Second)
		aggregate := fixture.seedRecoveryMigrationAggregate(t, db, "8", 108, now, true)
		fixture.mustExec(t, db, `UPDATE backup_asset_recovery_jobs
			SET state = 'verifying', workspace_phase = 'sealed' WHERE id = ?`, aggregate.JobID)

		fixture.expectExecRejectedInRollback(t, db, `UPDATE backup_asset_recovery_jobs
			SET state = 'succeeded', workspace_phase = 'published' WHERE id = ?`, aggregate.JobID)
	})

	t.Run("published job blocks active attempt creation", func(t *testing.T) {
		_, db := fixture.openAt(t, backupAssetRecoveryVersion)
		now := time.Now().UTC().Truncate(time.Second)
		aggregate := fixture.seedRecoveryMigrationAggregate(t, db, "b", 111, now)
		fixture.expectExecRejectedInRollback(t, db, `INSERT INTO backup_asset_recovery_attempts
			(id, job_id, owner_id, fence, state, mutation_armed, lease_expires_at, heartbeat_at,
			 created_at, updated_at)
			VALUES (?, ?, 'late-worker', 2, 'claimed', ?, ?, ?, ?, ?)`,
			recoveryMigrationOpaqueID(699917), aggregate.JobID, false,
			now.Add(30*time.Minute), now, now, now)
	})

	t.Run("ready result job cannot reopen", func(t *testing.T) {
		_, db := fixture.openAt(t, backupAssetRecoveryVersion)
		now := time.Now().UTC().Truncate(time.Second)
		aggregate := fixture.seedRecoveryMigrationAggregate(t, db, "9", 109, now)
		fixture.expectExecRejectedInRollback(t, db, `UPDATE backup_asset_recovery_jobs
			SET state = 'running', workspace_phase = 'writing' WHERE id = ?`, aggregate.JobID)
	})

	t.Run("ready result job outcome and workspace are immutable", func(t *testing.T) {
		_, db := fixture.openAt(t, backupAssetRecoveryVersion)
		now := time.Now().UTC().Truncate(time.Second)
		aggregate := fixture.seedRecoveryMigrationAggregate(t, db, "c", 130, now)
		fixture.expectExecRejectedInRollback(t, db, `UPDATE backup_asset_recovery_jobs
			SET state = 'degraded', failure_category = 'verification_mismatch',
				transition_revision = transition_revision + 1
			WHERE id = ?`, aggregate.JobID)
		fixture.expectExecRejectedInRollback(t, db, `UPDATE backup_asset_recovery_jobs
			SET target_chain_revision = ? WHERE id = ?`, recoveryMigrationDigest(699918), aggregate.JobID)
	})

	t.Run("workspace and hard deadlines are immutable", func(t *testing.T) {
		_, db := fixture.openAt(t, backupAssetRecoveryVersion)
		now := time.Now().UTC().Truncate(time.Second)
		aggregate := fixture.seedRecoveryMigrationAggregate(t, db, "a", 110, now)
		fixture.expectExecRejectedInRollback(t, db, `UPDATE backup_asset_recovery_jobs
			SET plaintext_deadline = ? WHERE id = ?`, now.Add(2*time.Hour+time.Minute), aggregate.JobID)
		fixture.expectExecRejectedInRollback(t, db, `UPDATE backup_asset_recovery_result_sets
			SET hard_deadline = ? WHERE id = ?`, now.Add(4*time.Hour), aggregate.ResultSetID)

		retainedUntil := now.Add(150 * time.Minute)
		fixture.mustExec(t, db, `UPDATE backup_asset_recovery_result_sets
			SET plaintext_deadline = ?, updated_at = ? WHERE id = ?`, retainedUntil, now.Add(time.Minute), aggregate.ResultSetID)
		var gotRetainedUntil time.Time
		if err := db.QueryRow(fixture.bind(`SELECT plaintext_deadline
			FROM backup_asset_recovery_result_sets WHERE id = ?`), aggregate.ResultSetID).Scan(&gotRetainedUntil); err != nil {
			t.Fatalf("read %s retained result deadline: %v", fixture.engine, err)
		}
		if !gotRetainedUntil.Equal(retainedUntil) {
			t.Fatalf("%s retained result deadline=%s want %s", fixture.engine, gotRetainedUntil, retainedUntil)
		}
	})
}

func (fixture migrationFixture) test069ResultClassification(t *testing.T) {
	_, db := fixture.openAt(t, backupAssetRecoveryVersion)
	for _, column := range []string{
		"classification",
		"classification_revision",
		"classification_source_revision",
	} {
		if !databaseColumnExists(t, db, fixture.engine, "backup_asset_recovery_results", column) {
			t.Fatalf("%s recovery result is missing frozen classification column %s", fixture.engine, column)
		}
	}
}

func (fixture migrationFixture) test069RecoveryResultContentAuthorization(t *testing.T) {
	for index, classification := range []string{"non_secret", "secret", "unknown"} {
		t.Run("matching "+classification, func(t *testing.T) {
			_, db := fixture.openAt(t, backupAssetRecoveryVersion)
			now := time.Now().UTC().Truncate(time.Second)
			aggregate := fixture.seedRecoveryMigrationAggregate(t, db, fmt.Sprintf("%x", index+1), 120+index, now)
			aggregate.ResultID = fixture.insertRecoveryMigrationResult(t, db, aggregate, 120+index, classification, 2, 3, now)
			grantID := fixture.insertRecoveryMigrationContentGrantWithClassification(
				t, db, aggregate, 120+index, classification, 2, 3, now,
			)
			var matches int
			if err := db.QueryRow(fixture.bind(`SELECT COUNT(*)
				FROM backup_asset_delivery_grants AS content_grant
				JOIN backup_asset_recovery_results AS result
				  ON result.id = content_grant.recovery_result_id
				 AND result.job_id = content_grant.recovery_job_id
				WHERE content_grant.id = ?
				  AND content_grant.classification = result.classification
				  AND content_grant.classification_revision = result.classification_revision
				  AND content_grant.classification_source_revision = result.classification_source_revision`), grantID).Scan(&matches); err != nil {
				t.Fatalf("read %s matching %s RecoveryResult Content grant: %v", fixture.engine, classification, err)
			}
			if matches != 1 {
				t.Fatalf("%s matching %s RecoveryResult Content grant count=%d want 1", fixture.engine, classification, matches)
			}
		})
	}

	t.Run("published binding is immutable", func(t *testing.T) {
		_, db := fixture.openAt(t, backupAssetRecoveryVersion)
		now := time.Now().UTC().Truncate(time.Second)
		aggregate := fixture.seedRecoveryMigrationAggregate(t, db, "d", 123, now)
		grantID := fixture.insertRecoveryMigrationContentGrant(t, db, aggregate, 123, now)
		otherResultID := fixture.insertRecoveryMigrationResult(t, db, aggregate, 124, "non_secret", 1, 1, now)
		otherUserID := aggregate.UserID + 1_000_000
		fixture.insertSearchMigrationUser(t, db, otherUserID, "recovery-other-owner", now)

		for _, probe := range []struct {
			name  string
			query string
			args  []any
		}{
			{name: "classification revision", query: `UPDATE backup_asset_delivery_grants SET classification_revision = 2 WHERE id = ?`, args: []any{grantID}},
			{name: "classification source revision", query: `UPDATE backup_asset_delivery_grants SET classification_source_revision = 2 WHERE id = ?`, args: []any{grantID}},
			{name: "operator session", query: `UPDATE backup_asset_delivery_grants SET session_role = 'operator' WHERE id = ?`, args: []any{grantID}},
			{name: "owner", query: `UPDATE backup_asset_delivery_grants SET owner_user_id = ? WHERE id = ?`, args: []any{otherUserID, grantID}},
			{name: "result identity", query: `UPDATE backup_asset_delivery_grants SET recovery_result_id = ? WHERE id = ?`, args: []any{otherResultID, grantID}},
		} {
			t.Run(probe.name, func(t *testing.T) {
				fixture.expectExecRejectedInRollback(t, db, probe.query, probe.args...)
			})
		}
		fixture.expectExecRejectedInRollback(t, db, `UPDATE backup_asset_recovery_results
			SET classification = 'secret' WHERE id = ?`, aggregate.ResultID)
	})

	t.Run("insert validates classification revisions", func(t *testing.T) {
		_, db := fixture.openAt(t, backupAssetRecoveryVersion)
		now := time.Now().UTC().Truncate(time.Second)
		aggregate := fixture.seedRecoveryMigrationAggregate(t, db, "e", 125, now)
		grantID := fixture.insertRecoveryMigrationContentGrant(t, db, aggregate, 125, now)
		tx, err := db.Begin()
		if err != nil {
			t.Fatalf("begin %s RecoveryResult Content insert probe: %v", fixture.engine, err)
		}
		if _, err = tx.Exec(fixture.bind(`CREATE TEMP TABLE recovery_content_insert_probe AS
			SELECT * FROM backup_asset_delivery_grants WHERE id = ?`), grantID); err == nil {
			_, err = tx.Exec(`UPDATE recovery_content_insert_probe
				SET classification_revision = classification_revision + 1`)
		}
		if err == nil {
			_, err = tx.Exec(fixture.bind(`DELETE FROM backup_asset_delivery_grants WHERE id = ?`), grantID)
		}
		if err == nil {
			_, err = tx.Exec(`INSERT INTO backup_asset_delivery_grants SELECT * FROM recovery_content_insert_probe`)
		}
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			t.Fatalf("rollback %s RecoveryResult Content insert probe: %v", fixture.engine, rollbackErr)
		}
		if err == nil {
			t.Fatalf("%s RecoveryResult Content INSERT accepted a mismatched classification revision", fixture.engine)
		}
	})

	t.Run("nonready result set rejects a new binding", func(t *testing.T) {
		_, db := fixture.openAt(t, backupAssetRecoveryVersion)
		now := time.Now().UTC().Truncate(time.Second)
		aggregate := fixture.seedRecoveryMigrationAggregate(t, db, "f", 126, now)
		fixture.mustExec(t, db, `UPDATE backup_asset_recovery_result_sets
			SET state = 'revoking', cleanup_owner = 'cleanup-worker', cleanup_lease_expires_at = ?,
				cleanup_fence = 1, node_lease_id = ?, node_fence = 1, cleanup_attempt = 1,
				updated_at = ? WHERE id = ?`, now.Add(30*time.Minute), aggregate.NodeLeaseID,
			now.Add(30*time.Second), aggregate.ResultSetID)
		fixture.mustExec(t, db, `UPDATE backup_asset_recovery_result_sets
			SET state = 'cleaned', cleanup_phase = 'tombstoned', cleanup_owner = '',
				cleanup_lease_expires_at = NULL, cleanup_fence = 1, node_lease_id = NULL,
				node_fence = 0, cleanup_attempt = 1, updated_at = ? WHERE id = ?`,
			now.Add(time.Minute), aggregate.ResultSetID)

		base := 698900
		leaseID := recoveryMigrationOpaqueID(base + 1)
		grantID := recoveryMigrationOpaqueID(base + 2)
		fixture.insertSearchMigrationLease(t, db, leaseID, aggregate.PointID, "content_session", now)
		fixture.insertContentMigrationGrant(t, db, aggregate.UserID, grantID, recoveryMigrationOpaqueID(base+3),
			aggregate.PointID, aggregate.CatalogID, aggregate.EntryID, leaseID, now)
		fixture.expectExecRejectedInRollback(t, db, `UPDATE backup_asset_delivery_grants
			SET resource_kind = 'recovery_result',
				recovery_point_id = NULL, catalog_generation_id = NULL, entry_id = NULL,
				recovery_job_id = ?, recovery_result_id = ?, session_role = 'admin',
				action = 'download', range_policy = 'single', renderer = 'attachment', profile = 'original_v1',
				classification = 'non_secret', classification_revision = 1,
				classification_source_revision = 1, step_up_action = 'recovery.result_download',
				step_up_proof_id = ?, step_up_expires_at = ?
			WHERE id = ?`, aggregate.JobID, aggregate.ResultID,
			recoveryMigrationOpaqueID(base+4), now.Add(20*time.Minute), grantID)
	})
}

func (fixture migrationFixture) test069StrictOpaqueDigestAndTemporalContracts(t *testing.T) {
	_, db := fixture.openAt(t, backupAssetRecoveryVersion)

	type lowerHexColumn struct {
		table  string
		column string
		size   int
	}
	opaqueColumns := []lowerHexColumn{
		{table: "backup_asset_recovery_plans", column: "id", size: 32},
		{table: "backup_asset_recovery_plans", column: "repository_id", size: 32},
		{table: "backup_asset_recovery_plans", column: "recovery_point_id", size: 32},
		{table: "backup_asset_recovery_plans", column: "catalog_generation_id", size: 32},
		{table: "backup_asset_recovery_plan_items", column: "id", size: 32},
		{table: "backup_asset_recovery_plan_items", column: "plan_id", size: 32},
		{table: "backup_asset_recovery_plan_items", column: "recovery_point_id", size: 32},
		{table: "backup_asset_recovery_plan_items", column: "catalog_generation_id", size: 32},
		{table: "backup_asset_recovery_plan_items", column: "entry_id", size: 64},
		{table: "backup_asset_recovery_preflights", column: "id", size: 32},
		{table: "backup_asset_recovery_preflights", column: "plan_id", size: 32},
		{table: "backup_asset_recovery_grants", column: "id", size: 32},
		{table: "backup_asset_recovery_grants", column: "plan_id", size: 32},
		{table: "backup_asset_recovery_grants", column: "job_id", size: 32},
		{table: "backup_asset_recovery_grants", column: "delete_checkpoint_id", size: 32},
		{table: "backup_asset_recovery_grants", column: "delete_attempt_id", size: 32},
		{table: "backup_asset_recovery_jobs", column: "id", size: 32},
		{table: "backup_asset_recovery_jobs", column: "plan_id", size: 32},
		{table: "backup_asset_recovery_jobs", column: "preflight_id", size: 32},
		{table: "backup_asset_recovery_jobs", column: "authority_grant_id", size: 32},
		{table: "backup_asset_recovery_job_items", column: "id", size: 32},
		{table: "backup_asset_recovery_job_items", column: "plan_id", size: 32},
		{table: "backup_asset_recovery_job_items", column: "job_id", size: 32},
		{table: "backup_asset_recovery_job_items", column: "plan_item_id", size: 32},
		{table: "backup_asset_recovery_attempts", column: "id", size: 32},
		{table: "backup_asset_recovery_attempts", column: "job_id", size: 32},
		{table: "backup_asset_recovery_checkpoints", column: "id", size: 32},
		{table: "backup_asset_recovery_checkpoints", column: "job_id", size: 32},
		{table: "backup_asset_recovery_checkpoints", column: "attempt_id", size: 32},
		{table: "backup_asset_recovery_checkpoints", column: "preflight_id", size: 32},
		{table: "backup_asset_recovery_checkpoints", column: "authority_grant_id", size: 32},
		{table: "backup_asset_recovery_checkpoints", column: "delete_grant_id", size: 32},
		{table: "backup_asset_recovery_evidence", column: "id", size: 32},
		{table: "backup_asset_recovery_evidence", column: "job_id", size: 32},
		{table: "backup_asset_recovery_node_leases", column: "id", size: 32},
		{table: "backup_asset_recovery_node_leases", column: "job_id", size: 32},
		{table: "backup_asset_recovery_node_leases", column: "attempt_id", size: 32},
		{table: "backup_asset_recovery_result_sets", column: "id", size: 32},
		{table: "backup_asset_recovery_result_sets", column: "job_id", size: 32},
		{table: "backup_asset_recovery_result_sets", column: "node_lease_id", size: 32},
		{table: "backup_asset_recovery_results", column: "id", size: 32},
		{table: "backup_asset_recovery_results", column: "result_set_id", size: 32},
		{table: "backup_asset_recovery_results", column: "job_id", size: 32},
		{table: "backup_asset_delivery_grants", column: "recovery_job_id", size: 32},
		{table: "backup_asset_delivery_grants", column: "recovery_result_id", size: 32},
	}
	for _, column := range opaqueColumns {
		column := column
		t.Run("opaque/"+column.table+"/"+column.column, func(t *testing.T) {
			fixture.assertRecoveryLowerHexColumn(t, db, column.table, column.column, column.size)
		})
	}

	digestColumns := []lowerHexColumn{
		{table: "backup_asset_recovery_plans", column: "idempotency_key_digest", size: 64},
		{table: "backup_asset_recovery_plans", column: "source_revision_digest", size: 64},
		{table: "backup_asset_recovery_plans", column: "immutable_locator_digest", size: 64},
		{table: "backup_asset_recovery_plans", column: "immutable_manifest_digest", size: 64},
		{table: "backup_asset_recovery_plans", column: "observation_fingerprint", size: 64},
		{table: "backup_asset_recovery_plans", column: "root_locator_digest", size: 64},
		{table: "backup_asset_recovery_plans", column: "path_digest", size: 64},
		{table: "backup_asset_recovery_plans", column: "selection_digest", size: 64},
		{table: "backup_asset_recovery_plans", column: "binding_digest", size: 64},
		{table: "backup_asset_recovery_plans", column: "operation_set_digest", size: 64},
		{table: "backup_asset_recovery_plans", column: "delete_set_digest", size: 64},
		{table: "backup_asset_recovery_plans", column: "security_decision_digest", size: 64},
		{table: "backup_asset_recovery_plans", column: "security_finding_set_digest", size: 64},
		{table: "backup_asset_recovery_plans", column: "security_override_binding_digest", size: 64},
		{table: "backup_asset_recovery_plan_items", column: "source_fingerprint", size: 64},
		{table: "backup_asset_recovery_plan_items", column: "relative_path_digest", size: 64},
		{table: "backup_asset_recovery_preflights", column: "source_revision_digest", size: 64},
		{table: "backup_asset_recovery_preflights", column: "root_locator_digest", size: 64},
		{table: "backup_asset_recovery_preflights", column: "path_digest", size: 64},
		{table: "backup_asset_recovery_preflights", column: "finding_set_digest", size: 64},
		{table: "backup_asset_recovery_preflights", column: "operation_set_digest", size: 64},
		{table: "backup_asset_recovery_preflights", column: "delete_set_digest", size: 64},
		{table: "backup_asset_recovery_jobs", column: "plan_binding_digest", size: 64},
		{table: "backup_asset_recovery_jobs", column: "selection_digest", size: 64},
		{table: "backup_asset_recovery_jobs", column: "source_revision_digest", size: 64},
		{table: "backup_asset_recovery_jobs", column: "root_locator_digest", size: 64},
		{table: "backup_asset_recovery_jobs", column: "path_digest", size: 64},
		{table: "backup_asset_recovery_jobs", column: "operation_set_digest", size: 64},
		{table: "backup_asset_recovery_jobs", column: "delete_set_digest", size: 64},
		{table: "backup_asset_recovery_jobs", column: "security_decision_digest", size: 64},
		{table: "backup_asset_recovery_jobs", column: "security_finding_set_digest", size: 64},
		{table: "backup_asset_recovery_jobs", column: "security_override_binding_digest", size: 64},
		{table: "backup_asset_recovery_jobs", column: "authority_binding_digest", size: 64},
		{table: "backup_asset_recovery_jobs", column: "workspace_marker_binding_digest", size: 64},
		{table: "backup_asset_recovery_job_items", column: "verified_digest", size: 64},
		{table: "backup_asset_recovery_checkpoints", column: "operation_digest", size: 64},
		{table: "backup_asset_recovery_checkpoints", column: "plan_binding_digest", size: 64},
		{table: "backup_asset_recovery_checkpoints", column: "source_revision_digest", size: 64},
		{table: "backup_asset_recovery_checkpoints", column: "security_decision_digest", size: 64},
		{table: "backup_asset_recovery_checkpoints", column: "security_finding_set_digest", size: 64},
		{table: "backup_asset_recovery_checkpoints", column: "authority_binding_digest", size: 64},
		{table: "backup_asset_recovery_checkpoints", column: "delete_grant_binding_digest", size: 64},
		{table: "backup_asset_recovery_evidence", column: "summary_digest", size: 64},
		{table: "backup_asset_recovery_result_sets", column: "marker_binding_digest", size: 64},
		{table: "backup_asset_recovery_results", column: "locator_digest", size: 64},
		{table: "backup_asset_recovery_results", column: "content_digest", size: 64},
		{table: "backup_asset_recovery_grants", column: "grant_hash", size: 64},
		{table: "backup_asset_recovery_grants", column: "binding_digest", size: 64},
		{table: "backup_asset_recovery_grants", column: "delete_set_digest", size: 64},
	}
	for _, column := range digestColumns {
		column := column
		t.Run("digest/"+column.table+"/"+column.column, func(t *testing.T) {
			fixture.assertRecoveryLowerHexColumn(t, db, column.table, column.column, column.size)
		})
	}

	t.Run("bounded opaque preflight revisions", func(t *testing.T) {
		now := time.Now().UTC().Truncate(time.Second)
		aggregate := fixture.seedRecoveryMigrationAggregate(t, db, "b", 127, now)
		fixture.mustExec(t, db, `INSERT INTO backup_asset_recovery_preflights
			(id, plan_id, revision, source_revision_digest, target_node_id, node_revision,
			 target_root_id, root_locator_digest, path_digest, target_revision, capability_revision,
			 policy_revision, finding_set_digest, operation_set_digest, delete_set_digest,
			 encrypted_operation_rows, estimated_items, estimated_bytes, expires_at, created_at)
			SELECT ?, plan_id, 'preflight-v2', source_revision_digest, target_node_id, node_revision,
				target_root_id, root_locator_digest, path_digest, 'target-v2', 'capability-v2',
				'policy-v2', finding_set_digest, operation_set_digest, delete_set_digest,
				encrypted_operation_rows, estimated_items, estimated_bytes, ?, ?
			FROM backup_asset_recovery_preflights WHERE id = ?`,
			recoveryMigrationOpaqueID(699701), now.Add(time.Hour), now, aggregate.PreflightID)
	})

	for _, testCase := range []struct {
		name             string
		sequence         int
		observedAt       func(time.Time) time.Time
		preflightExpires func(time.Time) time.Time
		createdAt        func(time.Time) time.Time
	}{
		{
			name:             "future observation",
			sequence:         128,
			observedAt:       func(now time.Time) time.Time { return now.Add(time.Minute) },
			preflightExpires: func(now time.Time) time.Time { return now.Add(time.Hour) },
			createdAt:        func(now time.Time) time.Time { return now },
		},
		{
			name:             "observation after preflight expiry",
			sequence:         129,
			observedAt:       func(now time.Time) time.Time { return now.Add(-15 * time.Minute) },
			preflightExpires: func(now time.Time) time.Time { return now.Add(-30 * time.Minute) },
			createdAt:        func(now time.Time) time.Time { return now.Add(-2 * time.Hour) },
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			now := time.Now().UTC().Truncate(time.Second)
			marker := fmt.Sprintf("%x", testCase.sequence%14+1)
			aggregate := fixture.seedRecoveryMigrationAggregate(t, db, marker, testCase.sequence, now)
			fixture.expectRecoveryObservationPlanCloneRejected(
				t, db, aggregate.PlanID, testCase.sequence,
				testCase.observedAt(now), testCase.preflightExpires(now), testCase.createdAt(now),
			)
		})
	}

}

func (fixture migrationFixture) test069AdmissionExpiryEquality(t *testing.T) {
	injectedAdmissionAt := time.Unix(2_000_000_000, 0).UTC()
	testCases := []struct {
		name               string
		sequence           int
		marker             string
		preflightExpiresAt time.Time
		authorityExpiresAt time.Time
	}{
		{
			name:               "preflight expiry equals admission timestamp",
			sequence:           132,
			marker:             "d",
			preflightExpiresAt: injectedAdmissionAt,
			authorityExpiresAt: injectedAdmissionAt.Add(time.Hour),
		},
		{
			name:               "authority expiry equals admission timestamp",
			sequence:           133,
			marker:             "e",
			preflightExpiresAt: injectedAdmissionAt.Add(time.Hour),
			authorityExpiresAt: injectedAdmissionAt,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			_, db := fixture.openAt(t, backupAssetRecoveryVersion)
			fixture.expectRecoveryJobAdmissionRejectedAtExpiryEquality(
				t,
				db,
				testCase.marker,
				testCase.sequence,
				injectedAdmissionAt,
				testCase.preflightExpiresAt,
				testCase.authorityExpiresAt,
			)
		})
	}
}

func (fixture migrationFixture) expectRecoveryJobAdmissionRejectedAtExpiryEquality(
	t *testing.T,
	db *sql.DB,
	marker string,
	sequence int,
	admissionAt time.Time,
	preflightExpiresAt time.Time,
	authorityExpiresAt time.Time,
) {
	t.Helper()
	createdAt := admissionAt.Add(-2 * time.Hour)
	authorityConsumedAt := admissionAt.Add(-time.Minute)
	base := 715000 + sequence*100
	userID := int64(base)
	nodeID := int64(base)
	repositoryID := strings.Repeat(marker, 32)
	pointID, catalogID, _ := fixture.insertSearchMigrationCatalog(t, db, marker, createdAt)
	fixture.insertSearchMigrationUser(t, db, userID, "recovery-expiry-user-"+marker, createdAt)
	fixture.mustExec(t, db, `INSERT INTO nodes
		(id, name, host, username, backup_dir, created_at, updated_at)
		VALUES (?, ?, ?, 'recovery-user', ?, ?, ?)`,
		nodeID, "recovery-expiry-node-"+marker, "recovery-expiry-host-"+marker,
		"/var/lib/xirang/recovery-expiry-"+marker, createdAt, createdAt)

	planID := recoveryMigrationOpaqueID(base + 1)
	preflightID := recoveryMigrationOpaqueID(base + 2)
	grantID := recoveryMigrationOpaqueID(base + 3)
	jobID := recoveryMigrationOpaqueID(base + 4)
	sourceRevisionDigest := recoveryMigrationDigest(base + 5)
	nodeRevision := recoveryMigrationDigest(base + 6)
	targetRevision := recoveryMigrationDigest(base + 21)
	capabilityRevision := recoveryMigrationDigest(base + 7)
	securityPolicyRevision := recoveryMigrationDigest(base + 8)
	findingSetDigest := recoveryMigrationDigest(base + 9)
	operationSetDigest := recoveryMigrationDigest(base + 10)
	securityDecisionDigest := recoveryMigrationDigest(base + 11)
	planBindingDigest := recoveryMigrationDigest(base + 12)
	selectionDigest := recoveryMigrationDigest(base + 13)
	authorityBindingDigest := recoveryMigrationDigest(base + 14)
	targetRootID := "root-expiry-" + marker
	rootLocatorDigest := recoveryMigrationDigest(base + 18)
	pathDigest := recoveryMigrationDigest(base + 19)

	fixture.mustExec(t, db, `INSERT INTO backup_asset_recovery_plans
		(id, requester_id, endpoint, idempotency_key_digest, repository_id, recovery_point_id,
		 source_revision_digest, source_revision_kind, immutable_locator_digest, immutable_manifest_digest,
		 observation_fingerprint, catalog_generation_id, observed_at, encrypted_source_locator,
		 target_mode, target_node_id, target_root_id, encrypted_target_root_locator,
		 encrypted_target_relative_path, root_locator_digest, path_digest, target_base_revision,
		 credential_scope_revision, root_revision, filesystem_revision, selection_digest,
		 binding_digest, capability_revision, conflict_policy, operation_set_digest, delete_set_digest,
		 security_decision, security_decision_digest, security_finding_set_digest, security_policy_revision,
		 security_override_binding_digest, encrypted_override_reason, preflight_revision,
		 preflight_expires_at, estimated_items, estimated_bytes, state, transition_revision, created_at, updated_at)
		VALUES (?, ?, 'recovery_plan_create', ?, ?, ?, ?, 'immutable', ?, ?, '', ?, NULL,
		 'enc:v2:source', 'isolated', ?, ?, 'enc:v2:root', 'enc:v2:path', ?, ?, ?,
		 'credential-v1', 'root-v1', 'filesystem-v1', ?, ?, ?, 'fail_on_conflict', ?, ?,
		 'allow_clean', ?, ?, ?, '', '', 'preflight-v1', ?, 1, 1, 'executed', 1, ?, ?)`,
		planID, userID, recoveryMigrationDigest(base+15), repositoryID, pointID,
		sourceRevisionDigest, recoveryMigrationDigest(base+16), recoveryMigrationDigest(base+17),
		catalogID, nodeID, targetRootID, rootLocatorDigest,
		pathDigest, nodeRevision, selectionDigest, planBindingDigest,
		capabilityRevision, operationSetDigest, recoveryEmptyDeleteSetDigest,
		securityDecisionDigest, findingSetDigest, securityPolicyRevision, preflightExpiresAt,
		createdAt, createdAt)
	fixture.mustExec(t, db, `INSERT INTO backup_asset_recovery_preflights
		(id, plan_id, revision, source_revision_digest, target_node_id, node_revision,
		 target_root_id, root_locator_digest, path_digest, target_revision, capability_revision,
		 policy_revision, finding_set_digest, operation_set_digest, delete_set_digest,
		 encrypted_operation_rows, estimated_items, estimated_bytes, expires_at, created_at)
		VALUES (?, ?, 'preflight-v1', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'enc:v2:operation-rows', 1, 1, ?, ?)`,
		preflightID, planID, sourceRevisionDigest, nodeID, nodeRevision,
		targetRootID, rootLocatorDigest, pathDigest, targetRevision, capabilityRevision,
		securityPolicyRevision, findingSetDigest, operationSetDigest, recoveryEmptyDeleteSetDigest,
		preflightExpiresAt, createdAt)
	fixture.mustExec(t, db, `INSERT INTO backup_asset_recovery_grants
		(id, plan_id, job_id, authority_category, grant_hash, actor_user_id, actor_session_id,
		 binding_digest, encrypted_reason, expires_at, consumed_at, created_at, updated_at)
		VALUES (?, ?, NULL, 'write', ?, ?, 'recovery-session', ?, 'enc:v2:write-reason', ?, ?, ?, ?)`,
		grantID, planID, recoveryMigrationDigest(base+20), userID, authorityBindingDigest,
		authorityExpiresAt, authorityConsumedAt, createdAt, authorityConsumedAt)

	fixture.expectExecRejectedInRollback(t, db, `INSERT INTO backup_asset_recovery_jobs
		(id, plan_id, plan_binding_digest, selection_digest, source_revision_digest,
		 preflight_id, preflight_revision, preflight_expires_at, preflight_target_revision,
		 preflight_node_revision, capability_revision, operation_set_digest, delete_set_digest, security_decision,
		 security_decision_digest, security_finding_set_digest, security_policy_revision,
		 security_override_binding_digest, estimated_items, estimated_bytes, authority_grant_id,
		 authority_category, authority_binding_digest, authority_expires_at, authority_consumed_at,
		 state, workspace_phase, target_mode, target_node_id, target_root_id,
		 root_locator_digest, path_digest, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 'preflight-v1', ?, ?, ?, ?, ?, ?, 'allow_clean', ?, ?, ?,
			'', 1, 1, ?, 'write', ?, ?, ?, 'queued', 'none', 'isolated', ?, ?, ?, ?, ?, ?)`,
		jobID, planID, planBindingDigest, selectionDigest, sourceRevisionDigest,
		preflightID, preflightExpiresAt, targetRevision, nodeRevision, capabilityRevision, operationSetDigest,
		recoveryEmptyDeleteSetDigest, securityDecisionDigest, findingSetDigest,
		securityPolicyRevision, grantID, authorityBindingDigest, authorityExpiresAt,
		authorityConsumedAt, nodeID, targetRootID, rootLocatorDigest, pathDigest, admissionAt, admissionAt)
}

func (fixture migrationFixture) assertRecoveryLowerHexColumn(
	t *testing.T,
	db *sql.DB,
	table string,
	column string,
	size int,
) {
	t.Helper()
	definition := fixture.recoveryColumnCheckDefinition(t, db, table, column)
	compact := strings.ReplaceAll(definition, " ", "")
	if fixture.engine == "sqlite" {
		lengthCheck := fmt.Sprintf("length(%s)=%d", column, size)
		lowerHexCheck := column + "notglob'*[^0-9a-f]*'"
		if !strings.Contains(compact, lengthCheck) || !strings.Contains(compact, lowerHexCheck) {
			t.Fatalf("SQLite %s.%s lacks an exact %d-character lowercase-hex check: %s",
				table, column, size, definition)
		}
		return
	}
	wantPattern := fmt.Sprintf("^[0-9a-f]{%d}$", size)
	if !strings.Contains(definition, wantPattern) {
		t.Fatalf("PostgreSQL %s.%s lacks an exact %d-character lowercase-hex check: %s",
			table, column, size, definition)
	}
}

func (fixture migrationFixture) recoveryColumnCheckDefinition(
	t *testing.T,
	db *sql.DB,
	table string,
	column string,
) string {
	t.Helper()
	if fixture.engine == "sqlite" {
		var definition string
		if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&definition); err != nil {
			t.Fatalf("load SQLite definition for %s.%s: %v", table, column, err)
		}
		prefix := column + " "
		lowerDefinition := strings.ToLower(definition)
		for _, line := range strings.Split(lowerDefinition, "\n") {
			normalized := strings.Join(strings.Fields(strings.TrimSpace(line)), " ")
			if strings.HasPrefix(normalized, prefix) {
				return strings.Join(strings.Fields(lowerDefinition), " ")
			}
		}
		t.Fatalf("SQLite definition for %s has no column %s", table, column)
	}

	rows, err := db.Query(`SELECT pg_get_constraintdef(constraint_row.oid)
		FROM pg_constraint AS constraint_row
		JOIN pg_class AS relation ON relation.oid = constraint_row.conrelid
		JOIN pg_namespace AS namespace ON namespace.oid = relation.relnamespace
		WHERE namespace.nspname = current_schema()
		  AND relation.relname = $1
		  AND constraint_row.contype = 'c'
		ORDER BY constraint_row.conname`, table)
	if err != nil {
		t.Fatalf("load PostgreSQL checks for %s.%s: %v", table, column, err)
	}
	defer closeMigrationRows(t, rows)
	columnCast := "(" + column + ")::text"
	var definitions []string
	for rows.Next() {
		var definition string
		if err := rows.Scan(&definition); err != nil {
			t.Fatalf("scan PostgreSQL check for %s.%s: %v", table, column, err)
		}
		normalized := strings.Join(strings.Fields(strings.ToLower(definition)), " ")
		if strings.Contains(normalized, columnCast) || strings.Contains(normalized, "length("+column+")") ||
			strings.Contains(normalized, " "+column+" ~") {
			definitions = append(definitions, normalized)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate PostgreSQL checks for %s.%s: %v", table, column, err)
	}
	if len(definitions) == 0 {
		t.Fatalf("PostgreSQL definition for %s has no check on %s", table, column)
	}
	return strings.Join(definitions, " ")
}

func (fixture migrationFixture) expectRecoveryObservationPlanCloneRejected(
	t *testing.T,
	db *sql.DB,
	sourcePlanID string,
	sequence int,
	observedAt time.Time,
	preflightExpiresAt time.Time,
	createdAt time.Time,
) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin %s recovery observation plan probe: %v", fixture.engine, err)
	}
	if _, err = tx.Exec(fixture.bind(`CREATE TEMP TABLE recovery_observation_plan_probe AS
		SELECT * FROM backup_asset_recovery_plans WHERE id = ?`), sourcePlanID); err == nil {
		_, err = tx.Exec(fixture.bind(`UPDATE recovery_observation_plan_probe
			SET id = ?, idempotency_key_digest = ?, source_revision_kind = 'observation',
				immutable_locator_digest = '', immutable_manifest_digest = '',
				observation_fingerprint = ?, observed_at = ?, preflight_expires_at = ?,
				state = 'draft', transition_revision = 1, created_at = ?, updated_at = ?`),
			recoveryMigrationOpaqueID(699800+sequence), recoveryMigrationDigest(699800+sequence),
			recoveryMigrationDigest(699900+sequence), observedAt, preflightExpiresAt, createdAt, createdAt)
	}
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("prepare %s recovery observation plan probe: %v", fixture.engine, err)
	}
	_, insertErr := tx.Exec(`INSERT INTO backup_asset_recovery_plans SELECT * FROM recovery_observation_plan_probe`)
	if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
		t.Fatalf("rollback %s recovery observation plan probe: %v", fixture.engine, rollbackErr)
	}
	if insertErr == nil {
		t.Fatalf("%s accepted invalid recovery observation time observed=%s created=%s preflight_expires=%s",
			fixture.engine, observedAt, createdAt, preflightExpiresAt)
	}
}

func (fixture migrationFixture) test069UseLatchImmutabilityAndOrdinaryEvidenceUpdates(t *testing.T) {
	_, db := fixture.openAt(t, backupAssetRecoveryVersion)
	now := time.Now().UTC().Truncate(time.Second)
	aggregate := fixture.seedRecoveryMigrationAggregate(t, db, "c", 3, now)
	const latchID = "00000000000000000000000000000069"
	ordinaryUpdatedAt := now.Add(time.Second)
	fixture.mustExec(t, db, `UPDATE backup_asset_recovery_evidence
		SET outcome = 'degraded', updated_at = ? WHERE id = ?`, ordinaryUpdatedAt, aggregate.EvidenceID)
	var ordinaryOutcome string
	var gotOrdinaryUpdatedAt time.Time
	if err := db.QueryRow(fixture.bind(`SELECT outcome, updated_at FROM backup_asset_recovery_evidence WHERE id = ?`), aggregate.EvidenceID).Scan(
		&ordinaryOutcome, &gotOrdinaryUpdatedAt,
	); err != nil {
		t.Fatalf("read %s updated ordinary recovery evidence: %v", fixture.engine, err)
	}
	if ordinaryOutcome != "degraded" || !gotOrdinaryUpdatedAt.Equal(ordinaryUpdatedAt) {
		t.Fatalf("%s ordinary recovery evidence update did not persist: outcome=%q updated_at=%s, want degraded/%s",
			fixture.engine, ordinaryOutcome, gotOrdinaryUpdatedAt.Format(time.RFC3339), ordinaryUpdatedAt.Format(time.RFC3339))
	}
	fixture.mustExec(t, db, `INSERT INTO backup_asset_recovery_evidence
		(id, job_id, kind, outcome, summary_digest, difference_count, verified_at, created_at, updated_at)
		VALUES (?, NULL, 'schema_use_latch', '', '', 0, NULL, ?, ?)`, latchID, now, now)
	fixture.expectExecRejected(t, db, `INSERT INTO backup_asset_recovery_evidence
		(id, job_id, kind, outcome, created_at, updated_at)
		VALUES (?, ?, 'schema_use_latch', '', ?, ?)`, latchID, aggregate.JobID, now, now)
	fixture.expectExecRejected(t, db, `INSERT INTO backup_asset_recovery_evidence
		(id, job_id, kind, outcome, created_at, updated_at)
		VALUES (?, NULL, 'verification', 'succeeded', ?, ?)`, recoveryMigrationOpaqueID(699997), now, now)

	fixture.mustExec(t, db, `DELETE FROM backup_asset_recovery_evidence
		WHERE kind IN ('verification', 'difference', 'failure')`)
	var latchCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM backup_asset_recovery_evidence
		WHERE id = '00000000000000000000000000000069' AND kind = 'schema_use_latch'`).Scan(&latchCount); err != nil {
		t.Fatalf("count %s immutable schema-use latch: %v", fixture.engine, err)
	}
	if latchCount != 1 {
		t.Fatalf("%s ordinary evidence cleanup removed schema-use latch", fixture.engine)
	}
	fixture.expectExecRejected(t, db, `UPDATE backup_asset_recovery_evidence
		SET updated_at = ? WHERE id = ?`, now.Add(time.Second), latchID)
	fixture.expectExecRejected(t, db, `DELETE FROM backup_asset_recovery_evidence WHERE id = ?`, latchID)
}

func (fixture migrationFixture) test069PreservesExisting068ContentAndExportArms(t *testing.T) {
	migrator, db := fixture.openAt(t, backupAssetExportVersion)
	contentDefinitionBefore := fixture.recoveryContentDefinitionForParity(t, db)
	now := time.Now().UTC().Truncate(time.Second)
	pointID, catalogID, entryID := fixture.insertSearchMigrationCatalog(t, db, "d", now)
	fixture.insertSearchMigrationUser(t, db, 6901, "recovery-content-preserve", now)
	leaseID := recoveryMigrationOpaqueID(698001)
	fixture.insertSearchMigrationLease(t, db, leaseID, pointID, "catalog_build", now)
	grantID := recoveryMigrationOpaqueID(698002)
	fixture.insertContentMigrationGrant(t, db, 6901, grantID, recoveryMigrationOpaqueID(698003),
		pointID, catalogID, entryID, leaseID, now)

	migrateToBackupAssetVersion(t, migrator, backupAssetRecoveryVersion)
	var resourceKind string
	if err := db.QueryRow(fixture.bind(`SELECT resource_kind FROM backup_asset_delivery_grants WHERE id = ?`), grantID).Scan(&resourceKind); err != nil {
		t.Fatalf("load %s pre-000069 Content grant after up: %v", fixture.engine, err)
	}
	if resourceKind != "backup_asset" {
		t.Fatalf("%s pre-000069 Content grant arm=%q want backup_asset", fixture.engine, resourceKind)
	}
	if !databaseTableExists(t, db, fixture.engine, "backup_asset_export_jobs") ||
		fixture.indexDefinition(t, db, "idx_backup_asset_export_jobs_claim") == "" {
		t.Fatalf("%s 000069 changed the existing 000068 export arm", fixture.engine)
	}
	if err := migrator.Steps(-1); err != nil {
		t.Fatalf("step %s 000069 down while only existing asset Content state remains: %v", fixture.engine, err)
	}
	assertMigrationVersion(t, migrator, backupAssetExportVersion)
	if err := db.QueryRow(fixture.bind(`SELECT resource_kind FROM backup_asset_delivery_grants WHERE id = ?`), grantID).Scan(&resourceKind); err != nil {
		t.Fatalf("load %s preserved Content grant after down: %v", fixture.engine, err)
	}
	if resourceKind != "backup_asset" {
		t.Fatalf("%s down changed preserved Content grant arm=%q", fixture.engine, resourceKind)
	}
	if contentDefinitionAfter := fixture.recoveryContentDefinitionForParity(t, db); contentDefinitionAfter != contentDefinitionBefore {
		t.Fatalf("%s pristine 000069 down did not restore the exact pre-000069 Content definition\n got: %s\nwant: %s",
			fixture.engine, contentDefinitionAfter, contentDefinitionBefore)
	}
}

func (fixture migrationFixture) recoveryContentDefinitionForParity(t *testing.T, db *sql.DB) string {
	t.Helper()
	definition := fixture.tableDefinition(t, db, "backup_asset_delivery_grants")
	definition = strings.Join(strings.Fields(definition), "")
	if fixture.engine == "sqlite" {
		definition = strings.ReplaceAll(definition,
			`createtable"backup_asset_delivery_grants"`,
			"createtablebackup_asset_delivery_grants",
		)
	}
	return definition
}

func (fixture migrationFixture) test069UsedDownIsRejectedAtomically(t *testing.T) {
	t.Run("EveryRecoveryFamily", func(t *testing.T) {
		migrator, db := fixture.openAt(t, backupAssetRecoveryVersion)
		now := time.Now().UTC().Truncate(time.Second)
		fixture.seedRecoveryMigrationAggregate(t, db, "e", 4, now)
		for _, table := range backupAssetRecoveryTables {
			var count int
			if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
				t.Fatalf("count %s used recovery family %s: %v", fixture.engine, table, err)
			}
			if count == 0 {
				t.Fatalf("%s used aggregate did not exercise recovery family %s", fixture.engine, table)
			}
		}
		fixture.assertRecoveryDownRejectedUnchanged(t, migrator, db)
	})

	t.Run("LatchOnlyCrashAfterCommit", func(t *testing.T) {
		migrator, db := fixture.openAt(t, backupAssetRecoveryVersion)
		now := time.Now().UTC().Truncate(time.Second)
		fixture.insertRecoveryMigrationUseLatch(t, db, now)
		fixture.assertRecoveryDownRejectedUnchanged(t, migrator, db)
	})

	t.Run("PurgeToEmptyEvidenceStillLeavesPermanentLatch", func(t *testing.T) {
		migrator, db := fixture.openAt(t, backupAssetRecoveryVersion)
		now := time.Now().UTC().Truncate(time.Second)
		fixture.seedRecoveryMigrationPurgeablePlan(t, db, "f", 5, now)
		fixture.insertRecoveryMigrationUseLatch(t, db, now)
		fixture.purgeRecoveryMigrationOrdinaryRows(t, db)
		for _, table := range backupAssetRecoveryTables {
			var count int
			if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
				t.Fatalf("count %s purged recovery family %s: %v", fixture.engine, table, err)
			}
			if table == "backup_asset_recovery_evidence" {
				if count != 3 {
					t.Fatalf("%s purge left %d evidence rows, want the permanent latch and two scheduler rows", fixture.engine, count)
				}
				continue
			}
			if count != 0 {
				t.Fatalf("%s purge left %d rows in %s", fixture.engine, count, table)
			}
		}
		fixture.assertRecoveryDownRejectedUnchanged(t, migrator, db)
	})

	t.Run("RecoveryContentGrantOnly", func(t *testing.T) {
		migrator, db := fixture.openAt(t, backupAssetRecoveryVersion)
		now := time.Now().UTC().Truncate(time.Second)
		grantID := fixture.insertIsolatedRecoveryContentGrant(t, migrator, db, 6, 0, now)
		fixture.assertIsolatedRecoveryContentState(t, db, grantID, 0)
		fixture.assertRecoveryDownRejectedUnchanged(t, migrator, db)
	})

	t.Run("RecoveryContentRequest", func(t *testing.T) {
		migrator, db := fixture.openAt(t, backupAssetRecoveryVersion)
		now := time.Now().UTC().Truncate(time.Second)
		grantID := fixture.insertIsolatedRecoveryContentGrant(t, migrator, db, 7, 1, now)
		fixture.insertContentMigrationRequest(t, db, recoveryMigrationOpaqueID(697099), grantID, now)
		fixture.assertIsolatedRecoveryContentState(t, db, grantID, 1)
		fixture.assertRecoveryDownRejectedUnchanged(t, migrator, db)
	})

	t.Run("SharedContentUsage", func(t *testing.T) {
		migrator, db := fixture.openAt(t, backupAssetRecoveryVersion)
		now := time.Now().UTC().Truncate(time.Second)
		fixture.mustExec(t, db, `INSERT INTO backup_asset_delivery_usage
			(scope_kind, scope_id, window_started_at, window_expires_at, request_count,
			 reserved_bytes, delivered_bytes, in_flight, version, updated_at)
			VALUES ('global', 'global', ?, ?, 0, 0, 0, 0, 1, ?)`, now, now.Add(time.Minute), now)
		fixture.assertRecoveryDownRejectedUnchanged(t, migrator, db)
	})

	t.Run("ContentSessionLease", func(t *testing.T) {
		migrator, db := fixture.openAt(t, backupAssetRecoveryVersion)
		now := time.Now().UTC().Truncate(time.Second)
		pointID, _, _ := fixture.insertSearchMigrationCatalog(t, db, "b", now)
		fixture.insertSearchMigrationLease(t, db, recoveryMigrationOpaqueID(698101), pointID, "content_session", now)
		fixture.assertRecoveryDownRejectedUnchanged(t, migrator, db)
	})

	t.Run("ActiveRecoveryJobLease", func(t *testing.T) {
		migrator, db := fixture.openAt(t, backupAssetRecoveryVersion)
		now := time.Now().UTC().Truncate(time.Second)
		pointID, _, _ := fixture.insertSearchMigrationCatalog(t, db, "c", now)
		fixture.insertPublicationLease(t, db, recoveryMigrationOpaqueID(698201), pointID, "recovery_job", "active", now)
		fixture.assertRecoveryDownRejectedUnchanged(t, migrator, db)
	})
}

func (fixture migrationFixture) test069RejectedDownSnapshotCoversMutationArm(t *testing.T) {
	migrator, db := fixture.openAt(t, backupAssetRecoveryVersion)
	before := fixture.captureRecoveryMigrationSnapshot(t, migrator, db)
	const trigger = "trg_backup_asset_recovery_attempts_mutation_arm_monotonic"
	const function = "backup_asset_recovery_attempt_mutation_arm_monotonic"
	for _, ownedTrigger := range backupAssetRecoveryOwnedTriggersForEngine(fixture.engine) {
		if definition, ok := before.triggers[ownedTrigger.name]; !ok || definition == "" {
			t.Fatalf("%s recovery down snapshot is missing owned trigger %q", fixture.engine, ownedTrigger.name)
		}
	}
	if fixture.engine == "postgres" {
		for _, ownedFunction := range backupAssetRecoveryOwnedPostgresFunctions {
			if definition, ok := before.functions[ownedFunction]; !ok || definition == "" {
				t.Fatalf("PostgreSQL recovery down snapshot is missing owned function %q", ownedFunction)
			}
		}
	}

	var beforeFunction string
	if fixture.engine == "postgres" {
		beforeFunction = recoveryMigrationSnapshotFunctionDefinition(t, before, function)
		if beforeFunction == "" {
			t.Fatalf("PostgreSQL recovery down snapshot is missing mutation-arm function %q", function)
		}
	}
	if before.triggers[trigger] == "" {
		t.Fatalf("%s recovery down snapshot is missing mutation-arm trigger %q", fixture.engine, trigger)
	}

	dropTrigger := "DROP TRIGGER " + trigger
	if fixture.engine == "postgres" {
		dropTrigger += " ON backup_asset_recovery_attempts"
	}
	fixture.mustExec(t, db, dropTrigger)
	if fixture.engine == "postgres" {
		fixture.mustExec(t, db, `CREATE OR REPLACE FUNCTION backup_asset_recovery_attempt_mutation_arm_monotonic()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RETURN NEW;
END;
$$`)
		fixture.mustExec(t, db, `CREATE TRIGGER trg_backup_asset_recovery_attempts_mutation_arm_monotonic
BEFORE UPDATE ON backup_asset_recovery_attempts
FOR EACH ROW EXECUTE FUNCTION backup_asset_recovery_attempt_mutation_arm_monotonic()`)
	} else {
		fixture.mustExec(t, db, `CREATE TRIGGER trg_backup_asset_recovery_attempts_mutation_arm_monotonic
BEFORE UPDATE ON backup_asset_recovery_attempts
BEGIN
    SELECT RAISE(ABORT, 'replacement mutation-arm trigger');
END`)
	}
	after := fixture.captureRecoveryMigrationSnapshot(t, migrator, db)
	if after.triggers[trigger] == before.triggers[trigger] {
		t.Fatalf("%s recovery down snapshot did not observe mutation-arm trigger replacement", fixture.engine)
	}
	if fixture.engine == "postgres" && recoveryMigrationSnapshotFunctionDefinition(t, after, function) == beforeFunction {
		t.Fatal("PostgreSQL recovery down snapshot did not observe mutation-arm function replacement")
	}
}

func recoveryMigrationSnapshotFunctionDefinition(t *testing.T, snapshot recoveryMigrationSnapshot, name string) string {
	t.Helper()
	definition, ok := snapshot.functions[name]
	if !ok {
		t.Fatalf("recovery down snapshot does not collect PostgreSQL function %q", name)
	}
	return definition
}

func (fixture migrationFixture) insertRecoveryMigrationUseLatch(t *testing.T, db *sql.DB, now time.Time) {
	t.Helper()
	fixture.mustExec(t, db, `INSERT INTO backup_asset_recovery_evidence
		(id, job_id, kind, outcome, summary_digest, difference_count, verified_at, created_at, updated_at)
		VALUES ('00000000000000000000000000000069', NULL, 'schema_use_latch', '', '', 0, NULL, ?, ?)`, now, now)
}

func (fixture migrationFixture) insertIsolatedRecoveryContentGrant(
	t *testing.T,
	migrator *migrate.Migrate,
	db *sql.DB,
	sequence int,
	requestCount int,
	now time.Time,
) string {
	t.Helper()
	base := 697800 + sequence*10
	grantID := recoveryMigrationOpaqueID(base + 1)
	pointID, _, _ := fixture.insertSearchMigrationCatalog(t, db, fmt.Sprintf("%x", sequence), now)
	fixture.insertSearchMigrationUser(t, db, int64(base+5), fmt.Sprintf("recovery-content-owner-%d", sequence), now)
	fixture.insertSearchMigrationLease(t, db, recoveryMigrationOpaqueID(base+9), pointID, "catalog_build", now)
	query := `INSERT INTO backup_asset_delivery_grants
		(id, delivery_id, resource_kind, recovery_point_id, catalog_generation_id, entry_id,
		 recovery_job_id, recovery_result_id, owner_user_id, session_jti, session_token_version,
		 session_role, session_expires_at, action, method_policy, range_policy, renderer, profile,
		 classification, classification_revision, classification_source_revision, step_up_action,
		 step_up_proof_id, step_up_expires_at, provider_kind, source_fingerprint, entry_fingerprint,
		 fingerprint_strength, representation_etag, source_size, source_modified_at, detected_media_type,
		 representation_source_bytes, representation_size, representation_truncated, cookie_secret_hash,
		 state, revocation_reason, lease_id, lease_attempt_id, lease_fence_token_hash,
		 absolute_expires_at, idle_expires_at, idle_ttl_seconds, last_activity_at,
		 max_bytes_per_request, max_cumulative_bytes, max_requests, max_in_flight,
		 reserved_bytes, delivered_bytes, request_count, in_flight, version, audit_state,
		 audit_range_count, audit_range_bytes, audit_request_count, audit_success_count,
		 audit_blocked_count, audit_failure_count, audit_failure_code, audit_attempt_count,
		 created_at, updated_at)
		VALUES (?, ?, 'recovery_result', NULL, NULL, NULL,
		 ?, ?, ?, ?, 0, 'admin', ?, 'download', 'get_head', 'single', 'attachment', 'original_v1',
		 'non_secret', 1, 1, 'recovery.result_download', ?, ?, 'rsync', 'recovery-content-source-v1',
		 'recovery-result-v1', 'strong', '"recovery-content-etag"', 1, ?, 'application/octet-stream',
		 1, 1, ?, ?, 'active', '', ?, ?, ?, ?, ?, 60, ?,
		 64, 256, 10, 2, 0, 0, ?, 0, 1, 'none',
		 0, 0, 0, 0, 0, 0, '', 0, ?, ?)`
	args := []any{
		grantID,
		recoveryMigrationOpaqueID(base + 2),
		recoveryMigrationOpaqueID(base + 3),
		recoveryMigrationOpaqueID(base + 4),
		int64(base + 5),
		recoveryMigrationOpaqueID(base + 6),
		now.Add(time.Hour),
		recoveryMigrationOpaqueID(base + 7),
		now.Add(20 * time.Minute),
		now,
		false,
		recoveryMigrationDigest(base + 8),
		recoveryMigrationOpaqueID(base + 9),
		recoveryMigrationOpaqueID(base + 10),
		recoveryMigrationDigest(base + 11),
		now.Add(10 * time.Minute),
		now.Add(time.Minute),
		now,
		requestCount,
		now,
		now,
	}

	before := fixture.captureRecoveryMigrationSnapshot(t, migrator, db)
	postgresForeignKeys := map[string]string(nil)
	if fixture.engine == "postgres" {
		postgresForeignKeys = fixture.dropRecoveryContentGrantForeignKeys(t, db)
	}
	authorizationTriggers := fixture.dropRecoveryContentAuthorizationTriggers(t, db)
	authorizationTriggersRestored := false
	restoreAuthorizationTriggers := func() {
		if authorizationTriggersRestored {
			return
		}
		fixture.restoreRecoveryContentAuthorizationTriggers(t, db, authorizationTriggers)
		authorizationTriggersRestored = true
	}
	t.Cleanup(func() {
		restoreAuthorizationTriggers()
		fixture.mustExec(t, db, `DELETE FROM backup_asset_delivery_requests WHERE grant_id = ?`, grantID)
		fixture.mustExec(t, db, `DELETE FROM backup_asset_delivery_grants WHERE id = ?`, grantID)
		if fixture.engine == "postgres" {
			fixture.restoreRecoveryContentGrantForeignKeys(t, db, postgresForeignKeys)
		}
		after := fixture.captureRecoveryMigrationSnapshot(t, migrator, db)
		assertRecoveryMigrationSnapshotEqual(t, before, after, "isolated Recovery Content fixture cleanup")
	})

	if fixture.engine == "sqlite" {
		fixture.mustExec(t, db, `PRAGMA foreign_keys = OFF`)
		fixture.mustExec(t, db, query, args...)
		fixture.mustExec(t, db, `PRAGMA foreign_keys = ON`)
		restoreAuthorizationTriggers()
		return grantID
	}

	fixture.mustExec(t, db, query, args...)
	restoreAuthorizationTriggers()
	return grantID
}

func (fixture migrationFixture) dropRecoveryContentAuthorizationTriggers(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	definitions := make(map[string]string, 2)
	for _, trigger := range []string{
		"trg_backup_asset_recovery_content_authorization_insert",
		"trg_backup_asset_recovery_content_authorization_update",
	} {
		var definition string
		var err error
		if fixture.engine == "sqlite" {
			err = db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'trigger' AND name = ?`, trigger).Scan(&definition)
		} else {
			err = db.QueryRow(`SELECT pg_get_triggerdef(trigger_row.oid)
				FROM pg_trigger AS trigger_row
				JOIN pg_class AS relation ON relation.oid = trigger_row.tgrelid
				JOIN pg_namespace AS namespace ON namespace.oid = relation.relnamespace
				WHERE namespace.nspname = current_schema()
				  AND relation.relname = 'backup_asset_delivery_grants'
				  AND trigger_row.tgname = $1
				  AND NOT trigger_row.tgisinternal`, trigger).Scan(&definition)
		}
		if err != nil {
			t.Fatalf("load %s Recovery Content authorization trigger %s: %v", fixture.engine, trigger, err)
		}
		definitions[trigger] = definition
		if fixture.engine == "sqlite" {
			fixture.mustExec(t, db, `DROP TRIGGER `+trigger)
		} else {
			fixture.mustExec(t, db, `DROP TRIGGER `+trigger+` ON backup_asset_delivery_grants`)
		}
	}
	return definitions
}

func (fixture migrationFixture) restoreRecoveryContentAuthorizationTriggers(
	t *testing.T,
	db *sql.DB,
	definitions map[string]string,
) {
	t.Helper()
	for _, trigger := range []string{
		"trg_backup_asset_recovery_content_authorization_insert",
		"trg_backup_asset_recovery_content_authorization_update",
	} {
		definition := definitions[trigger]
		if definition == "" {
			t.Fatalf("%s Recovery Content authorization trigger %s has no saved definition", fixture.engine, trigger)
		}
		fixture.mustExec(t, db, definition)
		got := fixture.recoveryTriggerDefinition(t, db, "backup_asset_delivery_grants", trigger)
		want := strings.Join(strings.Fields(strings.ToLower(definition)), " ")
		if got != want {
			t.Fatalf("%s Recovery Content authorization trigger %s was not restored exactly\n got: %s\nwant: %s",
				fixture.engine, trigger, got, want)
		}
	}
}

func (fixture migrationFixture) dropRecoveryContentGrantForeignKeys(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	definitions := fixture.recoveryContentGrantForeignKeyDefinitions(t, db)
	for _, constraint := range []string{
		"backup_asset_delivery_grants_recovery_job_fk",
		"backup_asset_delivery_grants_recovery_result_fk",
	} {
		if definitions[constraint] == "" {
			t.Fatalf("%s recovery Content foreign key %s is missing", fixture.engine, constraint)
		}
		fixture.mustExec(t, db, `ALTER TABLE backup_asset_delivery_grants DROP CONSTRAINT `+constraint)
	}
	return definitions
}

func (fixture migrationFixture) restoreRecoveryContentGrantForeignKeys(t *testing.T, db *sql.DB, definitions map[string]string) {
	t.Helper()
	for _, constraint := range []string{
		"backup_asset_delivery_grants_recovery_job_fk",
		"backup_asset_delivery_grants_recovery_result_fk",
	} {
		definition := definitions[constraint]
		if definition == "" {
			t.Fatalf("%s recovery Content foreign key %s has no saved definition", fixture.engine, constraint)
		}
		fixture.mustExec(t, db, `ALTER TABLE backup_asset_delivery_grants ADD CONSTRAINT `+constraint+` `+definition)
	}
}

func (fixture migrationFixture) recoveryContentGrantForeignKeyDefinitions(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	definitions := make(map[string]string)
	if fixture.engine == "sqlite" {
		rows, err := db.Query(`PRAGMA foreign_key_list('backup_asset_delivery_grants')`)
		if err != nil {
			t.Fatalf("list SQLite Recovery Content foreign keys: %v", err)
		}
		defer closeMigrationRows(t, rows)
		for rows.Next() {
			var id, sequence int
			var table, from, to, onUpdate, onDelete, match string
			if err := rows.Scan(&id, &sequence, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
				t.Fatalf("scan SQLite Recovery Content foreign key: %v", err)
			}
			if table == "backup_asset_recovery_jobs" || table == "backup_asset_recovery_results" {
				definitions[fmt.Sprintf("%d:%d", id, sequence)] = strings.Join([]string{table, from, to, onUpdate, onDelete, match}, "|")
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("iterate SQLite Recovery Content foreign keys: %v", err)
		}
		return definitions
	}

	rows, err := db.Query(`SELECT constraint_row.conname, pg_get_constraintdef(constraint_row.oid)
		FROM pg_constraint AS constraint_row
		JOIN pg_class AS relation ON relation.oid = constraint_row.conrelid
		JOIN pg_namespace AS namespace ON namespace.oid = relation.relnamespace
		WHERE namespace.nspname = current_schema()
		  AND relation.relname = 'backup_asset_delivery_grants'
		  AND constraint_row.contype = 'f'
		  AND constraint_row.conname IN (
			'backup_asset_delivery_grants_recovery_job_fk',
			'backup_asset_delivery_grants_recovery_result_fk'
		  )
		ORDER BY constraint_row.conname`)
	if err != nil {
		t.Fatalf("list PostgreSQL Recovery Content foreign keys: %v", err)
	}
	defer closeMigrationRows(t, rows)
	for rows.Next() {
		var name, definition string
		if err := rows.Scan(&name, &definition); err != nil {
			t.Fatalf("scan PostgreSQL Recovery Content foreign key: %v", err)
		}
		definitions[name] = strings.Join(strings.Fields(strings.ToLower(definition)), " ")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate PostgreSQL Recovery Content foreign keys: %v", err)
	}
	return definitions
}

func (fixture migrationFixture) assertIsolatedRecoveryContentState(
	t *testing.T,
	db *sql.DB,
	grantID string,
	wantRequests int,
) {
	t.Helper()
	for _, table := range backupAssetRecoveryTables {
		var count int
		query := `SELECT COUNT(*) FROM ` + table
		var args []any
		if table == "backup_asset_recovery_evidence" {
			query += ` WHERE NOT (kind = 'scheduler_state' AND
				((id = ? AND scheduler_scope = 'claim') OR (id = ? AND scheduler_scope = 'takeover')))`
			query = fixture.bind(query)
			args = []any{recoveryClaimSchedulerRowID, recoveryTakeoverSchedulerRowID}
		}
		if err := db.QueryRow(query, args...).Scan(&count); err != nil {
			t.Fatalf("count %s isolated recovery Content blocker rows in %s: %v", fixture.engine, table, err)
		}
		if count != 0 {
			t.Fatalf("%s isolated recovery Content blocker seeded %d unrelated rows in %s", fixture.engine, count, table)
		}
	}

	var grantCount, requestCount, usageCount, leaseCount int
	if err := db.QueryRow(fixture.bind(`SELECT COUNT(*) FROM backup_asset_delivery_grants
		WHERE id = ? AND resource_kind = 'recovery_result'`), grantID).Scan(&grantCount); err != nil {
		t.Fatalf("count %s isolated recovery Content grant: %v", fixture.engine, err)
	}
	if err := db.QueryRow(fixture.bind(`SELECT COUNT(*) FROM backup_asset_delivery_requests
		WHERE grant_id = ?`), grantID).Scan(&requestCount); err != nil {
		t.Fatalf("count %s isolated recovery Content requests: %v", fixture.engine, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM backup_asset_delivery_usage`).Scan(&usageCount); err != nil {
		t.Fatalf("count %s isolated recovery Content usage: %v", fixture.engine, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM recovery_point_leases
		WHERE holder_type IN ('content_session', 'recovery_job')`).Scan(&leaseCount); err != nil {
		t.Fatalf("count %s isolated recovery Content leases: %v", fixture.engine, err)
	}
	if grantCount != 1 || requestCount != wantRequests || usageCount != 0 || leaseCount != 0 {
		t.Fatalf("%s isolated recovery Content state grant=%d requests=%d usage=%d leases=%d, want 1/%d/0/0",
			fixture.engine, grantCount, requestCount, usageCount, leaseCount, wantRequests)
	}
}

func (fixture migrationFixture) purgeRecoveryMigrationOrdinaryRows(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, statement := range []string{
		`DELETE FROM backup_asset_recovery_results`,
		`DELETE FROM backup_asset_recovery_result_sets`,
		`DELETE FROM backup_asset_recovery_evidence
			WHERE id NOT IN ('00000000000000000000000000000069',
				'0000000000000000000000000000006a', '0000000000000000000000000000006b')`,
		`DELETE FROM backup_asset_recovery_node_leases`,
		`DELETE FROM backup_asset_recovery_checkpoints`,
		`DELETE FROM backup_asset_recovery_attempts`,
		`DELETE FROM backup_asset_recovery_job_items`,
		`DELETE FROM backup_asset_recovery_grants`,
		`DELETE FROM backup_asset_recovery_jobs`,
		`DELETE FROM backup_asset_recovery_preflights`,
		`DELETE FROM backup_asset_recovery_plan_items`,
		`DELETE FROM backup_asset_recovery_plans`,
	} {
		fixture.mustExec(t, db, statement)
	}
}

// removeRecoveryMigrationActiveJobGraph isolates plan-level assertions without
// weakening the permanent identity of a terminal recovery attempt.
func (fixture migrationFixture) removeRecoveryMigrationActiveJobGraph(
	t *testing.T,
	db *sql.DB,
	aggregate recoveryMigrationAggregate,
) {
	t.Helper()
	var state string
	var mutationArmed bool
	if err := db.QueryRow(fixture.bind(`SELECT state, mutation_armed
		FROM backup_asset_recovery_attempts WHERE id = ?`), aggregate.AttemptID).Scan(&state, &mutationArmed); err != nil {
		t.Fatalf("read %s active recovery attempt for isolated graph removal: %v", fixture.engine, err)
	}
	if state != "claimed" && state != "running" || mutationArmed {
		t.Fatalf("refuse to remove %s terminal or mutation-armed recovery attempt state=%q armed=%t", fixture.engine, state, mutationArmed)
	}
	for _, statement := range []string{
		`DELETE FROM backup_asset_recovery_results WHERE job_id = ?`,
		`DELETE FROM backup_asset_recovery_result_sets WHERE job_id = ?`,
		`DELETE FROM backup_asset_recovery_evidence WHERE job_id = ?`,
		`DELETE FROM backup_asset_recovery_node_leases WHERE job_id = ?`,
		`DELETE FROM backup_asset_recovery_checkpoints WHERE job_id = ?`,
		`DELETE FROM backup_asset_recovery_job_items WHERE job_id = ?`,
		`DELETE FROM backup_asset_recovery_grants WHERE job_id = ?`,
		`DELETE FROM backup_asset_recovery_attempts WHERE job_id = ?`,
		`DELETE FROM backup_asset_recovery_jobs WHERE id = ?`,
	} {
		fixture.mustExec(t, db, statement, aggregate.JobID)
	}
}

func (fixture migrationFixture) insertRecoveryMigrationContentGrant(
	t *testing.T,
	db *sql.DB,
	aggregate recoveryMigrationAggregate,
	sequence int,
	now time.Time,
) string {
	t.Helper()
	return fixture.insertRecoveryMigrationContentGrantWithClassification(
		t, db, aggregate, sequence, "non_secret", 1, 1, now,
	)
}

func (fixture migrationFixture) insertRecoveryMigrationContentGrantWithClassification(
	t *testing.T,
	db *sql.DB,
	aggregate recoveryMigrationAggregate,
	sequence int,
	classification string,
	classificationRevision int,
	classificationSourceRevision int64,
	now time.Time,
) string {
	t.Helper()
	base := 697000 + sequence*10
	leaseID := recoveryMigrationOpaqueID(base + 1)
	grantID := recoveryMigrationOpaqueID(base + 2)
	fixture.insertSearchMigrationLease(t, db, leaseID, aggregate.PointID, "content_session", now)
	fixture.insertContentMigrationGrant(t, db, aggregate.UserID, grantID, recoveryMigrationOpaqueID(base+3),
		aggregate.PointID, aggregate.CatalogID, aggregate.EntryID, leaseID, now)
	fixture.mustExec(t, db, `UPDATE backup_asset_delivery_grants
		SET resource_kind = 'recovery_result',
			recovery_point_id = NULL, catalog_generation_id = NULL, entry_id = NULL,
			recovery_job_id = ?, recovery_result_id = ?, session_role = 'admin',
			action = 'download', range_policy = 'single', renderer = 'attachment', profile = 'original_v1',
			classification = ?, classification_revision = ?, classification_source_revision = ?,
			step_up_action = 'recovery.result_download',
			step_up_proof_id = ?, step_up_expires_at = ?
		WHERE id = ?`,
		aggregate.JobID, aggregate.ResultID, classification, classificationRevision,
		classificationSourceRevision, recoveryMigrationOpaqueID(base+4), now.Add(20*time.Minute), grantID)
	return grantID
}

func (fixture migrationFixture) insertRecoveryMigrationResult(
	t *testing.T,
	db *sql.DB,
	aggregate recoveryMigrationAggregate,
	sequence int,
	classification string,
	classificationRevision int,
	classificationSourceRevision int64,
	now time.Time,
) string {
	t.Helper()
	base := 698000 + sequence*10
	resultID := recoveryMigrationOpaqueID(base + 1)
	fixture.mustExec(t, db, `INSERT INTO backup_asset_recovery_results
		(id, result_set_id, job_id, result_kind, classification, classification_revision,
		 classification_source_revision, encrypted_relative_locator, locator_digest,
		 size, content_digest, created_at)
		VALUES (?, ?, ?, 'regular_file', ?, ?, ?, 'enc:v2:result', ?, 1, ?, ?)`,
		resultID, aggregate.ResultSetID, aggregate.JobID, classification, classificationRevision,
		classificationSourceRevision, recoveryMigrationDigest(base+2), recoveryMigrationDigest(base+3), now)
	return resultID
}

func assertBackupAssetRecoverySchema069(t *testing.T, fixture migrationFixture, db *sql.DB) {
	t.Helper()
	var query string
	if fixture.engine == "sqlite" {
		query = `SELECT name FROM sqlite_master
			WHERE type = 'table' AND name LIKE 'backup_asset_recovery_%' ORDER BY name`
	} else {
		query = `SELECT table_name FROM information_schema.tables
			WHERE table_schema = current_schema()
			  AND table_name LIKE 'backup_asset_recovery_%' ORDER BY table_name`
	}
	gotTables := fixture.rowStrings(t, db, query)
	wantTables := append([]string(nil), backupAssetRecoveryTables...)
	sort.Strings(wantTables)
	if !reflect.DeepEqual(gotTables, wantTables) {
		t.Fatalf("%s recovery tables mismatch\n got: %v\nwant: %v", fixture.engine, gotTables, wantTables)
	}

	requiredColumns := map[string][]string{
		"backup_asset_recovery_plans": {
			"id", "source_revision_kind", "target_mode", "selection_digest", "binding_digest",
			"operation_set_digest", "delete_set_digest", "security_decision", "state",
		},
		"backup_asset_recovery_plan_items":  {"id", "plan_id", "recovery_point_id", "catalog_generation_id", "entry_id"},
		"backup_asset_recovery_preflights":  {"id", "plan_id", "revision", "expires_at"},
		"backup_asset_recovery_grants":      {"id", "plan_id", "authority_category", "binding_digest", "encrypted_reason", "consumed_at"},
		"backup_asset_recovery_jobs":        {"id", "plan_id", "state", "workspace_phase", "plaintext_deadline"},
		"backup_asset_recovery_job_items":   {"id", "plan_id", "job_id", "plan_item_id"},
		"backup_asset_recovery_attempts":    {"id", "job_id", "state", "mutation_armed"},
		"backup_asset_recovery_checkpoints": {"id", "job_id", "attempt_id", "phase"},
		"backup_asset_recovery_evidence": {
			"id", "job_id", "scheduler_scope", "scheduler_cursor_at", "scheduler_cursor_id",
			"scheduler_high_water_at", "scheduler_high_water_id", "scheduler_revision",
		},
		"backup_asset_recovery_result_sets": {"id", "job_id", "state", "cleanup_phase", "plaintext_deadline"},
		"backup_asset_recovery_results":     {"id", "job_id", "result_set_id", "encrypted_relative_locator"},
		"backup_asset_recovery_node_leases": {"id", "node_id", "job_id", "state", "lease_expires_at"},
	}
	for table, columns := range requiredColumns {
		for _, column := range columns {
			if !databaseColumnExists(t, db, fixture.engine, table, column) {
				t.Fatalf("%s recovery schema is missing %s.%s", fixture.engine, table, column)
			}
		}
	}

	definitionFragments := map[string][]string{
		"backup_asset_recovery_plans": {
			"isolated", "in_place", "immutable", "observation", "allow_clean", "block", "admin_override",
		},
		"backup_asset_recovery_grants": {"write", "exact_mirror_delete"},
		"backup_asset_recovery_jobs": {
			"queued", "running", "verifying", "succeeded", "degraded", "needs_attention",
			"reserved", "marker_created", "writing", "sealed", "published", "cleanup_due", "workspace_cleaned",
		},
		"backup_asset_recovery_evidence": {"schema_use_latch"},
		"backup_asset_recovery_result_sets": {
			"ready", "revoking", "cleaned", "cleanup_failed", "claimed", "delete_started", "tombstoned",
		},
	}
	for table, fragments := range definitionFragments {
		definition := fixture.tableDefinition(t, db, table)
		for _, fragment := range fragments {
			if !strings.Contains(definition, fragment) {
				t.Fatalf("%s %s definition omits closed value %q: %s", fixture.engine, table, fragment, definition)
			}
		}
	}

	contentDefinition := fixture.tableDefinition(t, db, "backup_asset_delivery_grants")
	for _, fragment := range []string{"recovery_result", "recovery_job_id", "recovery_result_id"} {
		if !strings.Contains(contentDefinition, fragment) {
			t.Fatalf("%s Content RecoveryResult arm omits %q: %s", fixture.engine, fragment, contentDefinition)
		}
	}
	if fixture.engine == "sqlite" {
		assertSQLiteForeignKeyCheck(t, db)
	}
	assertBackupAssetRecoveryModelParity(t, fixture, db)
	assertBackupAssetRecoveryIndexesForeignKeysAndUTCTypes(t, fixture, db)
}

func assertBackupAssetRecoveryIndexesForeignKeysAndUTCTypes(t *testing.T, fixture migrationFixture, db *sql.DB) {
	t.Helper()
	for _, index := range []string{
		"idx_task_runs_node_snapshot_status",
		"idx_recovery_points_repository_id_id",
		"idx_backup_asset_recovery_plans_id_generation_point",
		"idx_backup_asset_recovery_plans_id_target",
		"idx_backup_asset_recovery_plans_id_target_binding",
		"idx_backup_asset_recovery_plans_requester_state",
		"idx_backup_asset_recovery_plans_preflight_expiry",
		"idx_backup_asset_recovery_plan_items_plan_ordinal",
		"idx_backup_asset_recovery_preflights_plan_expiry",
		"idx_backup_asset_recovery_jobs_plan",
		"idx_backup_asset_recovery_jobs_id_target_node",
		"idx_backup_asset_recovery_jobs_claim",
		"idx_backup_asset_recovery_job_items_job_outcome",
		"idx_backup_asset_recovery_attempts_current",
		"idx_backup_asset_recovery_attempts_expiry",
		"idx_backup_asset_recovery_checkpoints_job_sequence",
		"idx_backup_asset_recovery_checkpoints_delete_grant",
		"idx_backup_asset_recovery_evidence_job_created",
		"idx_backup_asset_recovery_node_leases_active_node",
		"idx_backup_asset_recovery_node_leases_claim",
		"idx_backup_asset_recovery_result_sets_expiry",
		"idx_backup_asset_recovery_result_sets_cleanup",
		"idx_backup_asset_recovery_results_job",
		"idx_backup_asset_recovery_grants_plan_category_expiry",
		"idx_backup_asset_recovery_grants_job_category",
		"idx_backup_asset_delivery_grants_recovery_result_state",
	} {
		definition := fixture.indexDefinition(t, db, index)
		if definition == "" {
			t.Fatalf("%s recovery index %s is missing", fixture.engine, index)
		}
	}
	for _, index := range []string{
		"idx_recovery_points_repository_id_id",
		"idx_backup_asset_recovery_plans_id_target_binding",
		"idx_backup_asset_recovery_jobs_plan",
		"idx_backup_asset_recovery_jobs_id_target_node",
		"idx_backup_asset_recovery_attempts_current",
		"idx_backup_asset_recovery_checkpoints_delete_grant",
		"idx_backup_asset_recovery_node_leases_active_node",
	} {
		if definition := fixture.indexDefinition(t, db, index); !strings.Contains(definition, "unique index") {
			t.Fatalf("%s recovery index %s is not unique: %s", fixture.engine, index, definition)
		}
	}

	for _, foreignKey := range []struct {
		table, column, target, onDelete string
	}{
		{"backup_asset_recovery_plan_items", "plan_id", "backup_asset_recovery_plans", "cascade"},
		{"backup_asset_recovery_jobs", "plan_id", "backup_asset_recovery_plans", "restrict"},
		{"backup_asset_recovery_job_items", "job_id", "backup_asset_recovery_jobs", "cascade"},
		{"backup_asset_recovery_attempts", "job_id", "backup_asset_recovery_jobs", "cascade"},
		{"backup_asset_recovery_checkpoints", "attempt_id", "backup_asset_recovery_attempts", "cascade"},
		{"backup_asset_recovery_grants", "delete_checkpoint_id", "backup_asset_recovery_checkpoints", "restrict"},
		{"backup_asset_recovery_grants", "delete_attempt_id", "backup_asset_recovery_attempts", "restrict"},
		{"backup_asset_recovery_evidence", "job_id", "backup_asset_recovery_jobs", "cascade"},
		{"backup_asset_recovery_results", "result_set_id", "backup_asset_recovery_result_sets", "cascade"},
		{"backup_asset_delivery_grants", "recovery_result_id", "backup_asset_recovery_results", "restrict"},
	} {
		if fixture.engine == "sqlite" {
			assertSQLiteForeignKeyAction(t, db, foreignKey.table, foreignKey.column, foreignKey.target, foreignKey.onDelete)
		} else {
			assertPostgresForeignKeyAction(t, db, foreignKey.table, foreignKey.column, foreignKey.target, foreignKey.onDelete)
		}
	}

	for table, columns := range map[string][]string{
		"backup_asset_recovery_plans":       {"observed_at", "preflight_expires_at", "created_at", "updated_at"},
		"backup_asset_recovery_preflights":  {"expires_at", "created_at"},
		"backup_asset_recovery_grants":      {"expires_at", "consumed_at", "revoked_at", "created_at", "updated_at"},
		"backup_asset_recovery_checkpoints": {"preflight_expires_at", "authority_expires_at", "delete_authority_expires_at", "delete_grant_expires_at", "delete_grant_consumed_at", "created_at"},
		"backup_asset_recovery_jobs":        {"plaintext_deadline", "created_at", "updated_at"},
		"backup_asset_recovery_attempts":    {"lease_expires_at", "heartbeat_at", "closed_at", "created_at", "updated_at"},
		"backup_asset_recovery_evidence": {
			"verified_at", "scheduler_cursor_at", "scheduler_high_water_at", "created_at", "updated_at",
		},
		"backup_asset_recovery_node_leases": {"lease_expires_at", "released_at", "created_at", "updated_at"},
		"backup_asset_recovery_result_sets": {"plaintext_deadline", "hard_deadline", "cleanup_lease_expires_at", "created_at", "updated_at"},
		"backup_asset_recovery_results":     {"modified_at", "created_at"},
	} {
		for _, column := range columns {
			if fixture.engine == "sqlite" {
				var columnType string
				if err := db.QueryRow(`SELECT type FROM pragma_table_info(?) WHERE name = ?`, table, column).Scan(&columnType); err != nil {
					t.Fatalf("load SQLite %s.%s type: %v", table, column, err)
				}
				if !strings.EqualFold(columnType, "DATETIME") {
					t.Fatalf("SQLite %s.%s type=%q want DATETIME", table, column, columnType)
				}
				continue
			}
			var dataType string
			if err := db.QueryRow(`SELECT data_type FROM information_schema.columns
				WHERE table_schema = current_schema() AND table_name = $1 AND column_name = $2`, table, column).Scan(&dataType); err != nil {
				t.Fatalf("load PostgreSQL %s.%s type: %v", table, column, err)
			}
			if dataType != "timestamp with time zone" {
				t.Fatalf("PostgreSQL %s.%s type=%q want TIMESTAMPTZ", table, column, dataType)
			}
		}
	}
}

func backupAssetRecoveryModels() map[string]any {
	return map[string]any{
		"backup_asset_recovery_plans":       model.BackupAssetRecoveryPlan{},
		"backup_asset_recovery_plan_items":  model.BackupAssetRecoveryPlanItem{},
		"backup_asset_recovery_preflights":  model.BackupAssetRecoveryPreflight{},
		"backup_asset_recovery_grants":      model.BackupAssetRecoveryGrant{},
		"backup_asset_recovery_jobs":        model.BackupAssetRecoveryJob{},
		"backup_asset_recovery_job_items":   model.BackupAssetRecoveryJobItem{},
		"backup_asset_recovery_attempts":    model.BackupAssetRecoveryAttempt{},
		"backup_asset_recovery_checkpoints": model.BackupAssetRecoveryCheckpoint{},
		"backup_asset_recovery_evidence":    model.BackupAssetRecoveryEvidence{},
		"backup_asset_recovery_result_sets": model.BackupAssetRecoveryResultSet{},
		"backup_asset_recovery_results":     model.BackupAssetRecoveryResult{},
		"backup_asset_recovery_node_leases": model.BackupAssetRecoveryNodeLease{},
	}
}

func assertBackupAssetRecoveryModelParity(t *testing.T, fixture migrationFixture, db *sql.DB) {
	t.Helper()
	for table, persistentModel := range backupAssetRecoveryModels() {
		want := gormColumnNames(t, persistentModel)
		var got []string
		if fixture.engine == "sqlite" {
			got = sqliteColumnNames(t, db, table)
			assertSQLiteTimeColumnsHaveNoDefault(t, db, table)
		} else {
			got = postgresColumnNames(t, db, table)
			assertPostgresTableTimeColumnsHaveNoDefault(t, db, table)
		}
		sort.Strings(got)
		sort.Strings(want)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("%s %s columns mismatch\n got: %v\nwant: %v", fixture.engine, table, got, want)
		}
	}
}

type recoveryMigrationAggregate struct {
	UserID        int64
	NodeID        int64
	RepositoryID  string
	PlanID        string
	PlanItemID    string
	PreflightID   string
	JobID         string
	JobItemID     string
	AttemptID     string
	CheckpointID  string
	EvidenceID    string
	SourceLeaseID string
	NodeLeaseID   string
	ResultSetID   string
	ResultID      string
	WriteGrantID  string
	GrantID       string
	PointID       string
	CatalogID     string
	EntryID       string
}

func recoveryMigrationOpaqueID(value int) string {
	return fmt.Sprintf("%032x", value)
}

func recoveryMigrationDigest(value int) string {
	return fmt.Sprintf("%064x", value)
}

func recoveryMigrationWorkspaceBindingDigest(
	jobID,
	planID,
	planBindingDigest string,
	nodeID int64,
	rootID,
	rootLocatorDigest,
	workspaceLocator string,
) string {
	hash := sha256.New()
	writeUint64 := func(value uint64) {
		var encoded [8]byte
		binary.BigEndian.PutUint64(encoded[:], value)
		_, _ = hash.Write(encoded[:])
	}
	writeString := func(value string) {
		writeUint64(uint64(len(value)))
		_, _ = hash.Write([]byte(value))
	}
	writeString("xirang/recovery/workspace-binding/v1")
	writeUint64(8)
	for _, value := range []string{
		jobID,
		planID,
		planBindingDigest,
		string(recovery.TargetModeIsolated),
		strconv.FormatInt(nodeID, 10),
		rootID,
		rootLocatorDigest,
		workspaceLocator,
	} {
		writeString(value)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

type recoveryMigrationSeedOptions struct {
	activeAttempt           bool
	claimableAttempt        bool
	exactMirror             bool
	initialOperationHistory bool
	purgeablePlan           bool
	firstWrite              bool
	decryptableWorkspace    bool
	workspacePhase          string
}

func (fixture migrationFixture) seedRecoveryMigrationAggregate(
	t *testing.T,
	db *sql.DB,
	marker string,
	sequence int,
	now time.Time,
	activeAttempt ...bool,
) recoveryMigrationAggregate {
	t.Helper()
	return fixture.seedRecoveryMigrationAggregateWithOptions(t, db, marker, sequence, now, recoveryMigrationSeedOptions{
		activeAttempt: len(activeAttempt) != 0 && activeAttempt[0],
	})
}

func (fixture migrationFixture) seedRecoveryMigrationExactMirrorAggregate(
	t *testing.T,
	db *sql.DB,
	marker string,
	sequence int,
	now time.Time,
) recoveryMigrationAggregate {
	t.Helper()
	return fixture.seedRecoveryMigrationAggregateWithOptions(t, db, marker, sequence, now, recoveryMigrationSeedOptions{
		activeAttempt: true,
		exactMirror:   true,
	})
}

func (fixture migrationFixture) seedRecoveryMigrationExactMirrorOperationHistory(
	t *testing.T,
	db *sql.DB,
	marker string,
	sequence int,
	now time.Time,
) recoveryMigrationAggregate {
	t.Helper()
	return fixture.seedRecoveryMigrationAggregateWithOptions(t, db, marker, sequence, now, recoveryMigrationSeedOptions{
		activeAttempt:           true,
		exactMirror:             true,
		initialOperationHistory: true,
	})
}

func (fixture migrationFixture) seedRecoveryMigrationPurgeablePlan(
	t *testing.T,
	db *sql.DB,
	marker string,
	sequence int,
	now time.Time,
) recoveryMigrationAggregate {
	t.Helper()
	return fixture.seedRecoveryMigrationAggregateWithOptions(t, db, marker, sequence, now, recoveryMigrationSeedOptions{
		purgeablePlan: true,
	})
}

func (fixture migrationFixture) seedRecoveryMigrationAggregateWithOptions(
	t *testing.T,
	db *sql.DB,
	marker string,
	sequence int,
	now time.Time,
	options recoveryMigrationSeedOptions,
) recoveryMigrationAggregate {
	t.Helper()
	seedActiveAttempt := options.activeAttempt
	base := 690000 + sequence*100
	catalogMarker := fmt.Sprintf("%x", sequence%16)
	catalogRepositoryID := strings.Repeat(catalogMarker, 32)
	aggregate := recoveryMigrationAggregate{
		UserID:       int64(base),
		NodeID:       int64(base),
		RepositoryID: catalogRepositoryID,
		PlanID:       recoveryMigrationOpaqueID(base + 1),
		PlanItemID:   recoveryMigrationOpaqueID(base + 2),
		PreflightID:  recoveryMigrationOpaqueID(base + 3),
		JobID:        recoveryMigrationOpaqueID(base + 4),
		JobItemID:    recoveryMigrationOpaqueID(base + 5),
		AttemptID:    recoveryMigrationOpaqueID(base + 6),
		CheckpointID: recoveryMigrationOpaqueID(base + 7),
		EvidenceID:   recoveryMigrationOpaqueID(base + 8),
		NodeLeaseID:  recoveryMigrationOpaqueID(base + 9),
		ResultSetID:  recoveryMigrationOpaqueID(base + 10),
		ResultID:     recoveryMigrationOpaqueID(base + 11),
		WriteGrantID: recoveryMigrationOpaqueID(base + 27),
		GrantID:      recoveryMigrationOpaqueID(base + 12),
	}
	var existingCatalogRepository int
	if err := db.QueryRow(fixture.bind(`SELECT COUNT(*) FROM backup_repositories WHERE id = ?`), catalogRepositoryID).Scan(&existingCatalogRepository); err != nil {
		t.Fatalf("check %s recovery catalog marker collision for sequence %d: %v", fixture.engine, sequence, err)
	}
	if existingCatalogRepository != 0 {
		t.Fatalf("%s recovery catalog marker %q collides for sequence %d", fixture.engine, catalogMarker, sequence)
	}
	aggregate.PointID, aggregate.CatalogID, aggregate.EntryID = fixture.insertSearchMigrationCatalog(t, db, catalogMarker, now)
	fixture.insertSearchMigrationUser(t, db, aggregate.UserID, "recovery-user-"+marker, now)
	fixture.mustExec(t, db, `INSERT INTO nodes
		(id, name, host, username, backup_dir, created_at, updated_at)
		VALUES (?, ?, ?, 'recovery-user', ?, ?, ?)`,
		aggregate.NodeID, "recovery-node-"+marker, "recovery-host-"+marker,
		"/var/lib/xirang/recovery-"+marker, now, now)
	firstWriteSeed := recoveryMigrationFirstWriteSeed{}
	if options.firstWrite {
		firstWriteSeed = fixture.prepareRecoveryMigrationFirstWriteSeed(t, db, aggregate, base, now)
	}

	preflightExpiry := now.Add(time.Hour)
	plaintextDeadline := now.Add(2 * time.Hour)
	hardDeadline := now.Add(3 * time.Hour)
	sourceRevisionDigest := recoveryMigrationDigest(base + 12)
	sourceRevisionKind := "immutable"
	immutableLocatorDigest := recoveryMigrationDigest(base + 2)
	immutableManifestDigest := recoveryMigrationDigest(base + 3)
	observationFingerprint := ""
	var observedAt any
	encryptedSourceLocator := "enc:v2:source"
	encryptedTargetRootLocator := "enc:v2:root"
	encryptedTargetRelativePath := "enc:v2:path"
	selectionDigest := recoveryMigrationDigest(base + 6)
	planBindingDigest := recoveryMigrationDigest(base + 7)
	planItemSourceFingerprint := ""
	planItemRelativePathDigest := recoveryMigrationDigest(base + 11)
	encryptedOperationRows := "enc:v2:operation-rows"
	encryptedWriteReason := "enc:v2:write-reason"
	if options.firstWrite {
		sourceRevisionDigest = firstWriteSeed.Selection.SourceRevisionDigest
		sourceRevisionKind = string(firstWriteSeed.Selection.SourceRevision.Kind)
		immutableLocatorDigest = ""
		immutableManifestDigest = ""
		observationFingerprint = firstWriteSeed.Selection.SourceRevision.MutableObservation.SourceFingerprint
		observedAt = firstWriteSeed.Selection.SourceRevision.MutableObservation.ObservedAt
		encryptedSourceLocator = firstWriteSeed.EncryptedSourceLocator
		encryptedTargetRootLocator = firstWriteSeed.EncryptedTargetRootLocator
		encryptedTargetRelativePath = firstWriteSeed.EncryptedTargetRelativePath
		selectionDigest = firstWriteSeed.Selection.SelectionDigest
		planItemSourceFingerprint = observationFingerprint
		planItemRelativePathDigest = firstWriteSeed.PlanItemPathDigest
		encryptedOperationRows = firstWriteSeed.EncryptedOperationRows
		encryptedWriteReason = firstWriteSeed.EncryptedWriteReason
	}
	targetRevision := recoveryMigrationDigest(base + 13)
	targetChainRevision := recoveryMigrationDigest(base + 21)
	if options.firstWrite {
		targetChainRevision = targetRevision
	}
	nodeRevision := recoveryMigrationDigest(base + 30)
	capabilityRevision := recoveryMigrationDigest(base + 14)
	securityPolicyRevision := recoveryMigrationDigest(base + 15)
	findingSetDigest := recoveryMigrationDigest(base + 16)
	operationSetDigest := recoveryMigrationDigest(base + 17)
	deleteSetDigest := recoveryEmptyDeleteSetDigest
	securityDecisionDigest := recoveryMigrationDigest(base + 19)
	workspaceMarkerBindingDigest := recoveryMigrationDigest(base + 20)
	writeAuthorityBindingDigest := recoveryMigrationDigest(base + 29)
	targetRootID := "root-" + marker
	rootLocatorDigest := recoveryMigrationDigest(base + 4)
	if options.firstWrite {
		rootLocator := "/var/lib/xirang/test-recovery-root/" + aggregate.JobID
		digest, digestErr := settings.RecoveryTargetRootLocatorDigest(uint(aggregate.NodeID), targetRootID, rootLocator)
		if digestErr != nil {
			t.Fatalf("derive %s first-write target root locator digest: %v", fixture.engine, digestErr)
		}
		rootLocatorDigest = digest
	}
	pathDigest := recoveryMigrationDigest(base + 5)
	writeAuthorityExpiresAt := now.Add(30 * time.Minute)
	targetMode := "isolated"
	conflictPolicy := "fail_on_conflict"
	workspaceRelativeLocator := "jobs/" + aggregate.JobID
	workspaceLocator := "enc:v2:workspace"
	if options.firstWrite || options.decryptableWorkspace {
		encryptedWorkspaceLocator, encryptErr := secure.EncryptString(workspaceRelativeLocator)
		if encryptErr != nil {
			t.Fatalf("encrypt %s recovery workspace locator: %v", fixture.engine, encryptErr)
		}
		workspaceLocator = encryptedWorkspaceLocator
	}
	workspaceBindingDigest := recoveryMigrationWorkspaceBindingDigest(
		aggregate.JobID,
		aggregate.PlanID,
		planBindingDigest,
		aggregate.NodeID,
		targetRootID,
		rootLocatorDigest,
		workspaceRelativeLocator,
	)
	workspaceOwner := "recovery-worker"
	workspaceFence := int64(1)
	markerValidationAttemptID := aggregate.AttemptID
	markerValidationAttemptFence := int64(1)
	markerValidationNodeFence := int64(1)
	var persistedPlaintextDeadline any = plaintextDeadline
	jobState := "succeeded"
	workspacePhase := "published"
	initialCheckpointPhase := "workspace_reserved"
	planState := "executed"
	if options.purgeablePlan {
		planState = "preflight_ready"
	}
	if options.exactMirror {
		deleteSetDigest = recoveryMigrationDigest(base + 18)
		targetMode = "in_place"
		conflictPolicy = "exact_mirror"
		workspaceRelativeLocator = ""
		workspaceLocator = ""
		workspaceBindingDigest = ""
		workspaceMarkerBindingDigest = ""
		workspaceOwner = ""
		workspaceFence = 0
		markerValidationAttemptID = ""
		markerValidationAttemptFence = 0
		markerValidationNodeFence = 0
		persistedPlaintextDeadline = nil
		jobState = "running"
		workspacePhase = "none"
		initialCheckpointPhase = "verification"
	}
	if seedActiveAttempt {
		jobState = "running"
		if !options.exactMirror {
			workspacePhase = "writing"
		}
	}
	if options.claimableAttempt {
		jobState = "queued"
		workspacePhase = "none"
		workspaceMarkerBindingDigest = ""
		workspaceOwner = ""
		workspaceFence = 0
		markerValidationAttemptID = ""
		markerValidationAttemptFence = 0
		markerValidationNodeFence = 0
		persistedPlaintextDeadline = nil
	}
	if options.workspacePhase != "" {
		workspacePhase = options.workspacePhase
	}
	if workspacePhase == "none" || workspacePhase == "reserved" {
		markerValidationAttemptID = ""
		markerValidationAttemptFence = 0
		markerValidationNodeFence = 0
	}
	itemRelativeLocator := "items/item-0000"
	semanticTargetDigest, err := recovery.SemanticTargetDigest(
		recovery.TargetMode(targetMode), targetRootID, rootLocatorDigest, itemRelativeLocator,
	)
	if err != nil {
		t.Fatalf("derive %s recovery semantic target digest: %v", fixture.engine, err)
	}
	finalItemLocator := itemRelativeLocator
	if targetMode == string(recovery.TargetModeIsolated) {
		finalItemLocator = workspaceRelativeLocator + "/" + itemRelativeLocator
	}
	targetObjectDigest, err := recovery.TargetObjectDigest(targetRootID, rootLocatorDigest, finalItemLocator)
	if err != nil {
		t.Fatalf("derive %s recovery target object digest: %v", fixture.engine, err)
	}
	fixture.mustExec(t, db, `INSERT INTO backup_asset_recovery_plans
		(id, requester_id, endpoint, idempotency_key_digest, repository_id, recovery_point_id,
		 source_revision_digest, source_revision_kind, immutable_locator_digest, immutable_manifest_digest,
		 observation_fingerprint, catalog_generation_id, observed_at, encrypted_source_locator,
		 target_mode, target_node_id, target_root_id, encrypted_target_root_locator,
		 encrypted_target_relative_path, root_locator_digest, path_digest, target_base_revision,
		 credential_scope_revision, root_revision, filesystem_revision, selection_digest,
		 binding_digest, capability_revision, conflict_policy, operation_set_digest, delete_set_digest,
		 security_decision, security_decision_digest, security_finding_set_digest, security_policy_revision,
		 security_override_binding_digest, encrypted_override_reason, preflight_revision,
		 preflight_expires_at, estimated_items, estimated_bytes, state, transition_revision, created_at, updated_at)
		VALUES (?, ?, 'recovery_plan_create', ?, ?, ?, ?,
			 ?, ?, ?, ?, ?, ?, ?,
			 ?, ?, ?, ?, ?, ?, ?, ?,
			 'credential-v1', 'root-v1', 'filesystem-v1', ?, ?, ?, ?, ?, ?,
			'allow_clean', ?, ?, ?, '', '', 'preflight-v1', ?, 1, 1, ?, 1, ?, ?)`,
		aggregate.PlanID, aggregate.UserID, recoveryMigrationDigest(base+1),
		aggregate.RepositoryID, aggregate.PointID, sourceRevisionDigest,
		sourceRevisionKind, immutableLocatorDigest, immutableManifestDigest, observationFingerprint, aggregate.CatalogID, observedAt, encryptedSourceLocator,
		targetMode, aggregate.NodeID, targetRootID, encryptedTargetRootLocator, encryptedTargetRelativePath,
		rootLocatorDigest, pathDigest, nodeRevision,
		selectionDigest, planBindingDigest, capabilityRevision,
		conflictPolicy, operationSetDigest, deleteSetDigest, securityDecisionDigest, findingSetDigest,
		securityPolicyRevision,
		preflightExpiry, planState, now, now)
	if options.purgeablePlan {
		return aggregate
	}
	fixture.mustExec(t, db, `INSERT INTO backup_asset_recovery_plan_items
		(id, plan_id, ordinal, recovery_point_id, catalog_generation_id, entry_id, entry_type,
		 source_fingerprint, relative_path_digest, created_at)
		VALUES (?, ?, 0, ?, ?, ?, 'file', ?, ?, ?)`,
		aggregate.PlanItemID, aggregate.PlanID, aggregate.PointID, aggregate.CatalogID, aggregate.EntryID,
		planItemSourceFingerprint, planItemRelativePathDigest, now)
	fixture.mustExec(t, db, `INSERT INTO backup_asset_recovery_preflights
		(id, plan_id, revision, source_revision_digest, target_node_id, node_revision,
		 target_root_id, root_locator_digest, path_digest, target_revision, capability_revision,
		 policy_revision, finding_set_digest, operation_set_digest, delete_set_digest,
		 encrypted_operation_rows, estimated_items, estimated_bytes, expires_at, created_at)
		VALUES (?, ?, 'preflight-v1', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, 1, ?, ?)`,
		aggregate.PreflightID, aggregate.PlanID, sourceRevisionDigest,
		aggregate.NodeID, nodeRevision, targetRootID, rootLocatorDigest, pathDigest,
		targetRevision, capabilityRevision, securityPolicyRevision,
		findingSetDigest, operationSetDigest, deleteSetDigest, encryptedOperationRows,
		preflightExpiry, now)
	fixture.mustExec(t, db, `INSERT INTO backup_asset_recovery_grants
		(id, plan_id, job_id, authority_category, grant_hash, actor_user_id, actor_session_id,
		 binding_digest, encrypted_reason, expires_at, consumed_at, created_at, updated_at)
		VALUES (?, ?, NULL, 'write', ?, ?, 'recovery-session', ?, ?, ?, ?, ?, ?)`,
		aggregate.WriteGrantID, aggregate.PlanID, recoveryMigrationDigest(base+28), aggregate.UserID,
		writeAuthorityBindingDigest, encryptedWriteReason, writeAuthorityExpiresAt, now, now, now)
	fixture.mustExec(t, db, `INSERT INTO backup_asset_recovery_jobs
		(id, plan_id, plan_binding_digest, selection_digest, source_revision_digest,
		 preflight_id, preflight_revision, preflight_expires_at, preflight_target_revision,
		 preflight_node_revision, capability_revision, operation_set_digest, delete_set_digest, security_decision,
		 security_decision_digest, security_finding_set_digest, security_policy_revision,
		 security_override_binding_digest, estimated_items, estimated_bytes, authority_grant_id,
		 authority_category, authority_binding_digest, authority_expires_at, authority_consumed_at,
		 state, failure_category, transition_revision, workspace_phase,
			 encrypted_workspace_relative_locator, workspace_binding_digest,
			 workspace_marker_binding_digest, workspace_owner,
			 workspace_fence, workspace_marker_validation_attempt_id,
			 workspace_marker_validation_attempt_fence, workspace_marker_validation_node_fence,
			 plaintext_deadline, target_mode, target_node_id, target_root_id,
		 root_locator_digest, path_digest, target_chain_revision,
		 created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 'preflight-v1', ?, ?, ?, ?, ?, ?, 'allow_clean', ?, ?, ?,
			'', 1, 1, ?, 'write', ?, ?, ?,
			?, '', 1, ?, ?, ?, ?, ?,
			?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		aggregate.JobID, aggregate.PlanID, planBindingDigest, selectionDigest,
		sourceRevisionDigest, aggregate.PreflightID, preflightExpiry, targetRevision,
		nodeRevision, capabilityRevision, operationSetDigest, deleteSetDigest, securityDecisionDigest,
		findingSetDigest, securityPolicyRevision, aggregate.WriteGrantID, writeAuthorityBindingDigest,
		writeAuthorityExpiresAt, now, jobState, workspacePhase, workspaceLocator, workspaceBindingDigest,
		workspaceMarkerBindingDigest, workspaceOwner, workspaceFence,
		markerValidationAttemptID, markerValidationAttemptFence, markerValidationNodeFence, persistedPlaintextDeadline,
		targetMode, aggregate.NodeID, targetRootID, rootLocatorDigest, pathDigest,
		targetChainRevision, now, now)
	fixture.mustExec(t, db, `INSERT INTO backup_asset_recovery_job_items
		(id, plan_id, job_id, plan_item_id, ordinal, operation_kind, target_path_digest,
		 semantic_target_digest, target_object_digest,
		 expected_prior_kind, expected_prior_digest, expected_post_identity_digest,
		 expected_post_bytes, expected_prior_bytes, encrypted_target_relative_locator,
		 target_locator_key_version, target_locator_cipher_version,
		 display_class, estimated_bytes, created_at, updated_at)
		VALUES (?, ?, ?, ?, 0, 'create', ?, ?, ?, 'absent', '', ?, 1, -1, ?, 1, 1, 'regular', 1, ?, ?)`,
		aggregate.JobItemID, aggregate.PlanID, aggregate.JobID, aggregate.PlanItemID,
		recoveryMigrationDigest(base+31), semanticTargetDigest, targetObjectDigest,
		recoveryMigrationDigest(base+32),
		"enc:v2:item-target-locator-"+marker, now, now)
	if options.claimableAttempt {
		fixture.mustExec(t, db, `INSERT INTO backup_asset_recovery_attempts
			(id, job_id, owner_id, fence, state, mutation_armed, lease_expires_at, heartbeat_at,
			 created_at, updated_at)
			VALUES (?, ?, 'recovery-authorization', 1, 'claimed', ?, ?, ?, ?, ?)`,
			aggregate.AttemptID, aggregate.JobID, false, now.Add(30*time.Minute), now, now, now)
	} else if seedActiveAttempt {
		fixture.mustExec(t, db, `INSERT INTO backup_asset_recovery_attempts
			(id, job_id, owner_id, fence, state, mutation_armed, lease_expires_at, heartbeat_at,
			 created_at, updated_at)
			VALUES (?, ?, 'recovery-worker', 1, 'running', ?, ?, ?, ?, ?)`,
			aggregate.AttemptID, aggregate.JobID, options.initialOperationHistory,
			now.Add(30*time.Minute), now, now, now)
	} else {
		fixture.mustExec(t, db, `INSERT INTO backup_asset_recovery_attempts
			(id, job_id, owner_id, fence, state, mutation_armed, closed_at, created_at, updated_at)
			VALUES (?, ?, 'recovery-worker', 1, 'completed', ?, ?, ?, ?)`,
			aggregate.AttemptID, aggregate.JobID, true, now, now, now)
	}
	if !options.claimableAttempt && !options.initialOperationHistory {
		fixture.mustExec(t, db, `INSERT INTO backup_asset_recovery_checkpoints
		(id, job_id, attempt_id, sequence, phase, plan_binding_digest, source_revision_digest,
		 preflight_id, preflight_revision, preflight_expires_at, security_decision,
		 security_decision_digest, security_finding_set_digest, security_policy_revision,
		 authority_grant_id, job_authority_category, authority_binding_digest,
		 authority_expires_at, created_at)
		VALUES (?, ?, ?, 0, ?, ?, ?, ?, 'preflight-v1', ?, 'allow_clean',
		 ?, ?, ?, ?, 'write', ?, ?, ?)`,
			aggregate.CheckpointID, aggregate.JobID, aggregate.AttemptID, initialCheckpointPhase,
			recoveryMigrationDigest(base+7), sourceRevisionDigest, aggregate.PreflightID, preflightExpiry,
			securityDecisionDigest, findingSetDigest, securityPolicyRevision, aggregate.WriteGrantID,
			writeAuthorityBindingDigest, writeAuthorityExpiresAt, now)
	}
	nodeLeaseOwner := "recovery-worker"
	if options.claimableAttempt {
		nodeLeaseOwner = "recovery-authorization"
	}
	fixture.mustExec(t, db, `INSERT INTO backup_asset_recovery_node_leases
		(id, node_id, holder_kind, job_id, attempt_id, owner_id, fence, state,
		 lease_expires_at, created_at, updated_at)
		VALUES (?, ?, 'recovery_job', ?, ?, ?, 1, 'active', ?, ?, ?)`,
		aggregate.NodeLeaseID, aggregate.NodeID, aggregate.JobID, aggregate.AttemptID, nodeLeaseOwner,
		now.Add(30*time.Minute), now, now)
	if options.initialOperationHistory {
		initialNextRevision := recoveryMigrationDigest(base + 33)
		fixture.mustExec(t, db, `INSERT INTO backup_asset_recovery_checkpoints
			(id, job_id, job_item_id, attempt_id, sequence, phase, authority_category,
			 operation_digest, prior_target_revision, next_target_revision, node_fence, attempt_fence,
			 plan_binding_digest, source_revision_digest, preflight_id, preflight_revision,
			 preflight_expires_at, security_decision, security_decision_digest,
			 security_finding_set_digest, security_policy_revision, authority_grant_id,
			 job_authority_category, authority_binding_digest, authority_expires_at, created_at)
			SELECT ?, job.id, item.id, attempt.id, 0, 'operation', 'write', ?,
			       job.target_chain_revision, ?, node_lease.fence, attempt.fence,
			       job.plan_binding_digest, job.source_revision_digest, job.preflight_id,
			       job.preflight_revision, job.preflight_expires_at, job.security_decision,
			       job.security_decision_digest, job.security_finding_set_digest,
			       job.security_policy_revision, job.authority_grant_id, job.authority_category,
			       job.authority_binding_digest, job.authority_expires_at, ?
			FROM backup_asset_recovery_jobs AS job
			JOIN backup_asset_recovery_job_items AS item
			  ON item.id = ? AND item.job_id = job.id AND item.plan_id = job.plan_id
			JOIN backup_asset_recovery_attempts AS attempt
			  ON attempt.id = ? AND attempt.job_id = job.id
			JOIN backup_asset_recovery_node_leases AS node_lease
			  ON node_lease.id = ? AND node_lease.job_id = job.id AND node_lease.attempt_id = attempt.id
			WHERE job.id = ?`,
			aggregate.CheckpointID, recoveryMigrationDigest(base+34), initialNextRevision, now,
			aggregate.JobItemID, aggregate.AttemptID, aggregate.NodeLeaseID, aggregate.JobID)
		fixture.mustExec(t, db, `UPDATE backup_asset_recovery_job_items
			SET outcome = 'succeeded', bytes_written = expected_post_bytes,
			    verified_size = expected_post_bytes, verified_digest = expected_post_identity_digest,
			    updated_at = ? WHERE id = ?`, now, aggregate.JobItemID)
		fixture.mustExec(t, db, `UPDATE backup_asset_recovery_jobs
			SET target_chain_revision = ?, updated_at = ? WHERE id = ?`,
			initialNextRevision, now, aggregate.JobID)
	}
	if !options.claimableAttempt && !options.initialOperationHistory {
		sourceLeaseID := recoveryMigrationOpaqueID(base + 29)
		aggregate.SourceLeaseID = sourceLeaseID
		fixture.mustExec(t, db, `INSERT INTO recovery_point_leases
			(id, recovery_point_id, holder_type, owner_id, attempt_id, fence_token, status,
			 lease_expires_at, absolute_deadline, last_heartbeat_at, created_at, updated_at)
			VALUES (?, ?, 'recovery_job', ?, ?, ?, 'released', ?, ?, ?, ?, ?)`,
			sourceLeaseID, aggregate.PointID, aggregate.JobID, aggregate.AttemptID, recoveryMigrationDigest(base+30),
			now.Add(time.Hour), now.Add(2*time.Hour), now, now, now)
		fixture.mustExec(t, db, `INSERT INTO backup_asset_recovery_evidence
			(id, job_id, kind, outcome, summary_digest, difference_count, verified_at,
			 plan_id, checkpoint_id, grant_id, attempt_id, source_lease_id, node_lease_id,
			 node_lease_fence, created_at, updated_at)
			VALUES (?, ?, 'verification', 'succeeded', ?, 0, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)`,
			aggregate.EvidenceID, aggregate.JobID, recoveryMigrationDigest(base+31), now,
			aggregate.PlanID, aggregate.CheckpointID, aggregate.WriteGrantID, aggregate.AttemptID,
			sourceLeaseID, aggregate.NodeLeaseID, now, now)
	}
	if !seedActiveAttempt && !options.claimableAttempt && !options.exactMirror {
		fixture.mustExec(t, db, `INSERT INTO backup_asset_recovery_result_sets
			(id, job_id, state, marker_binding_digest, plaintext_deadline, hard_deadline, cleanup_phase,
			 created_at, updated_at)
			VALUES (?, ?, 'ready', ?, ?, ?, 'claimed', ?, ?)`,
			aggregate.ResultSetID, aggregate.JobID, workspaceMarkerBindingDigest, plaintextDeadline, hardDeadline, now, now)
		fixture.mustExec(t, db, `INSERT INTO backup_asset_recovery_results
			(id, result_set_id, job_id, result_kind, classification, classification_revision,
			 classification_source_revision, encrypted_relative_locator, locator_digest,
			 size, content_digest, created_at)
			VALUES (?, ?, ?, 'regular_file', 'non_secret', 1, 1, 'enc:v2:result', ?, 1, ?, ?)`,
			aggregate.ResultID, aggregate.ResultSetID, aggregate.JobID, recoveryMigrationDigest(base+23),
			recoveryMigrationDigest(base+24), now)
	}
	fixture.mustExec(t, db, `INSERT INTO backup_asset_recovery_grants
		(id, plan_id, job_id, authority_category, grant_hash, actor_user_id, actor_session_id,
		 binding_digest, encrypted_reason, expires_at, created_at, updated_at)
		VALUES (?, ?, NULL, 'write', ?, ?, 'recovery-session', ?, 'enc:v2:reason', ?, ?, ?)`,
		aggregate.GrantID, aggregate.PlanID, recoveryMigrationDigest(base+25), aggregate.UserID,
		recoveryMigrationDigest(base+26), now.Add(30*time.Minute), now, now)
	return aggregate
}

type recoveryMigrationFirstWriteSeed struct {
	Selection                   recovery.ExactSelection
	EncryptedSourceLocator      string
	EncryptedTargetRootLocator  string
	EncryptedTargetRelativePath string
	EncryptedOperationRows      string
	EncryptedWriteReason        string
	PlanItemPathDigest          string
}

func (fixture migrationFixture) prepareRecoveryMigrationFirstWriteSeed(
	t *testing.T,
	db *sql.DB,
	aggregate recoveryMigrationAggregate,
	base int,
	now time.Time,
) recoveryMigrationFirstWriteSeed {
	t.Helper()
	sourceLocator := "/var/lib/xirang/test-source/" + aggregate.JobID
	targetRootLocator := "/var/lib/xirang/test-recovery-root/" + aggregate.JobID
	targetRelativePath := "plans/" + aggregate.PlanID
	sourceFingerprint := recoveryMigrationDigest(base + 90)

	encrypt := func(label, value string) string {
		t.Helper()
		ciphertext, err := secure.EncryptString(value)
		if err != nil {
			t.Fatalf("encrypt %s %s fixture value: %v", fixture.engine, label, err)
		}
		return ciphertext
	}
	encryptedSourceLocator := encrypt("source locator", sourceLocator)
	encryptedTargetRootLocator := encrypt("target root locator", targetRootLocator)
	encryptedTargetRelativePath := encrypt("target relative path", targetRelativePath)
	encryptedOperationRows := encrypt("operation rows", `{"schema_version":1}`)
	encryptedWriteReason := encrypt("write reason", "migration first-write test")

	fixture.mustExec(t, db, `UPDATE backup_repositories
		SET version_mode = 'mutable_head', updated_at = ? WHERE id = ?`, now, aggregate.RepositoryID)
	fixture.mustExec(t, db, `UPDATE recovery_points
		SET encrypted_provider_locator = ?, source_fingerprint = ?, observed_at = ?, updated_at = ?
		WHERE id = ? AND repository_id = ?`,
		encryptedSourceLocator, sourceFingerprint, now, now, aggregate.PointID, aggregate.RepositoryID)
	fixture.mustExec(t, db, `UPDATE catalog_generations
		SET source_fingerprint = ?, updated_at = ? WHERE id = ? AND recovery_point_id = ?`,
		sourceFingerprint, now, aggregate.CatalogID, aggregate.PointID)

	validator, err := recovery.NewSourceValidator(fixture.recoveryWorkerGorm(t, db))
	if err != nil {
		t.Fatalf("create %s first-write source validator: %v", fixture.engine, err)
	}
	selection, err := validator.FreezeSelection(context.Background(), recovery.SourceSelectionRequest{
		RepositoryID: aggregate.RepositoryID, RecoveryPointID: aggregate.PointID,
		CatalogGenerationID: aggregate.CatalogID,
		AssetRefs:           []backupasset.AssetRef{{RecoveryPointID: aggregate.PointID, EntryID: aggregate.EntryID}},
		MaxItems:            1,
	})
	if err != nil {
		t.Fatalf("freeze %s first-write source selection: %v", fixture.engine, err)
	}
	if selection.SourceRevision.Kind != recovery.SourceRevisionObservation || selection.SourceRevision.MutableObservation == nil ||
		selection.SourceRevision.MutableObservation.SourceFingerprint != sourceFingerprint {
		t.Fatalf("unexpected %s first-write source selection: %+v", fixture.engine, selection.SourceRevision)
	}

	var normalizedPath string
	if err := db.QueryRow(fixture.bind(`SELECT normalized_path FROM catalog_entries
		WHERE generation_id = ? AND recovery_point_id = ? AND entry_id = ?`),
		aggregate.CatalogID, aggregate.PointID, aggregate.EntryID).Scan(&normalizedPath); err != nil {
		t.Fatalf("load %s first-write catalog path: %v", fixture.engine, err)
	}
	return recoveryMigrationFirstWriteSeed{
		Selection:                   selection,
		EncryptedSourceLocator:      encryptedSourceLocator,
		EncryptedTargetRootLocator:  encryptedTargetRootLocator,
		EncryptedTargetRelativePath: encryptedTargetRelativePath,
		EncryptedOperationRows:      encryptedOperationRows,
		EncryptedWriteReason:        encryptedWriteReason,
		PlanItemPathDigest: publication.RecoveryPlanItemPathDigest(
			aggregate.RepositoryID, aggregate.PointID, aggregate.CatalogID, aggregate.EntryID, normalizedPath,
		),
	}
}

func runBackupAssetMigration068Contract(t *testing.T, fixture migrationFixture) {
	t.Helper()
	t.Run("ApplyAndModelParity", func(t *testing.T) {
		migrator, db := fixture.openAt(t, backupAssetExportVersion)
		assertMigrationVersion(t, migrator, backupAssetExportVersion)
		for _, table := range backupAssetExportTables {
			if !databaseTableExists(t, db, fixture.engine, table) {
				t.Fatalf("%s export migration table %s is missing", fixture.engine, table)
			}
			want := gormColumnNames(t, backupAssetExportModels()[table])
			var got []string
			if fixture.engine == "sqlite" {
				got = sqliteColumnNames(t, db, table)
				assertSQLiteTimeColumnsHaveNoDefault(t, db, table)
			} else {
				got = postgresColumnNames(t, db, table)
				assertPostgresTableTimeColumnsHaveNoDefault(t, db, table)
			}
			sort.Strings(got)
			sort.Strings(want)
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Fatalf("%s %s columns mismatch\n got: %v\nwant: %v", fixture.engine, table, got, want)
			}
		}
		definition := fixture.tableDefinition(t, db, "wrapped_domain_keys")
		if !strings.Contains(definition, "derived_store") || !strings.Contains(definition, "export_store") {
			t.Fatalf("%s wrapped key CHECK does not preserve derived_store and add export_store: %s", fixture.engine, definition)
		}
	})
	t.Run("PostgresTimestampScanLocations", func(t *testing.T) {
		if fixture.engine != "postgres" {
			return
		}
		t.Setenv("TZ", "Asia/Shanghai")
		_, db := fixture.openAt(t, backupAssetExportVersion)

		var timestampAt time.Time
		var timestamptzAt time.Time
		if err := db.QueryRow(`SELECT
			TIMESTAMP '2026-07-27 03:04:05',
			TIMESTAMPTZ '2026-07-27 03:04:05+00'`).Scan(&timestampAt, &timestamptzAt); err != nil {
			t.Fatalf("scan PostgreSQL timestamp pair: %v", err)
		}
		for name, got := range map[string]time.Time{
			"timestamp":   timestampAt,
			"timestamptz": timestamptzAt,
		} {
			if got.Location() != time.UTC || got.Format(time.RFC3339) != "2026-07-27T03:04:05Z" {
				t.Fatalf("%s scan=%s (%s), want configured UTC ScanLocation", name, got.Format(time.RFC3339), got.Location())
			}
		}
	})
	t.Run("LifecycleSchedulerColumnsIndexesDefaultsAndChecks", func(t *testing.T) {
		_, db := fixture.openAt(t, backupAssetExportVersion)
		var bucketCount int
		if err := db.QueryRow(`SELECT COUNT(*) FROM backup_asset_export_quota_buckets`).Scan(&bucketCount); err != nil {
			t.Fatalf("count pristine %s lifecycle scheduler buckets: %v", fixture.engine, err)
		}
		if bucketCount != 0 {
			t.Fatalf("pristine %s schema must not pre-create the global lifecycle latch, got %d bucket(s)", fixture.engine, bucketCount)
		}
		jobType := reflect.TypeOf(model.BackupAssetExportJob{})
		sequenceField, found := jobType.FieldByName("LifecycleEnqueueSequence")
		if !found || !strings.Contains(sequenceField.Tag.Get("gorm"),
			"uniqueIndex:idx_backup_asset_export_jobs_lifecycle_enqueue_sequence") {
			t.Fatalf("Export job model does not bind the immutable lifecycle sequence index: %+v", sequenceField)
		}
		indexDefinition := fixture.indexDefinition(t, db, "idx_backup_asset_export_jobs_lifecycle_enqueue_sequence")
		for _, fragment := range []string{"unique index", "lifecycle_enqueue_sequence"} {
			if !strings.Contains(indexDefinition, fragment) {
				t.Fatalf("%s lifecycle sequence index omits %q: %s", fixture.engine, fragment, indexDefinition)
			}
		}

		now := time.Now().UTC().Truncate(time.Second)
		globalID := strings.Repeat("d", 32)
		userID := strings.Repeat("e", 32)
		fixture.mustExec(t, db, `INSERT INTO backup_asset_export_quota_buckets
			(id, scope, subject, transition_revision, active_jobs, active_workers, active_readers,
			 reserved_store_bytes, used_store_bytes, created_at, updated_at)
			VALUES (?, 'global', 'global', 1, 0, 0, 0, 0, 0, ?, ?)`, globalID, now, now)
		fixture.mustExec(t, db, `INSERT INTO backup_asset_export_quota_buckets
			(id, scope, subject, transition_revision, active_jobs, active_workers, active_readers,
			 reserved_store_bytes, used_store_bytes, created_at, updated_at)
			VALUES (?, 'user', '42', 1, 0, 0, 0, 0, 0, ?, ?)`, userID, now, now)
		assertSchedulerDefaults := func(t *testing.T, bucketID string) {
			t.Helper()
			var next, cursor, highWater, revision int64
			var lease any
			if err := db.QueryRow(fixture.bind(`SELECT lifecycle_next_sequence, lifecycle_sweep_cursor,
				lifecycle_sweep_high_water, lifecycle_sweep_revision, lifecycle_sweep_lease_expires_at
				FROM backup_asset_export_quota_buckets WHERE id = ?`), bucketID).
				Scan(&next, &cursor, &highWater, &revision, &lease); err != nil {
				t.Fatalf("load %s lifecycle scheduler defaults: %v", fixture.engine, err)
			}
			if next != 1 || cursor != 0 || highWater != 0 || revision != 0 || lease != nil {
				t.Fatalf("%s lifecycle scheduler defaults are not inert: next=%d cursor=%d high_water=%d revision=%d lease=%v",
					fixture.engine, next, cursor, highWater, revision, lease)
			}
		}
		t.Run("global defaults", func(t *testing.T) { assertSchedulerDefaults(t, globalID) })
		t.Run("user defaults", func(t *testing.T) { assertSchedulerDefaults(t, userID) })

		for _, testCase := range []struct {
			name  string
			query string
			args  []any
		}{
			{
				name: "global cursor exceeds high water",
				query: `UPDATE backup_asset_export_quota_buckets
					SET lifecycle_sweep_cursor = 1 WHERE id = ?`,
				args: []any{globalID},
			},
			{
				name: "global high water reaches next sequence",
				query: `UPDATE backup_asset_export_quota_buckets
					SET lifecycle_sweep_high_water = lifecycle_next_sequence WHERE id = ?`,
				args: []any{globalID},
			},
			{
				name: "global lease without acquired revision",
				query: `UPDATE backup_asset_export_quota_buckets
					SET lifecycle_sweep_lease_expires_at = ? WHERE id = ?`,
				args: []any{now.Add(time.Minute), globalID},
			},
			{
				name: "global high water without a scheduler revision",
				query: `UPDATE backup_asset_export_quota_buckets
					SET lifecycle_next_sequence = 2, lifecycle_sweep_high_water = 1 WHERE id = ?`,
				args: []any{globalID},
			},
			{
				name: "global scheduler revision without high water",
				query: `UPDATE backup_asset_export_quota_buckets
					SET lifecycle_sweep_revision = 1 WHERE id = ?`,
				args: []any{globalID},
			},
			{
				name: "user next sequence is mutable",
				query: `UPDATE backup_asset_export_quota_buckets
					SET lifecycle_next_sequence = 2 WHERE id = ?`,
				args: []any{userID},
			},
			{
				name: "user cursor is mutable",
				query: `UPDATE backup_asset_export_quota_buckets
					SET lifecycle_sweep_cursor = 1, lifecycle_sweep_high_water = 1,
						lifecycle_next_sequence = 2 WHERE id = ?`,
				args: []any{userID},
			},
			{
				name: "user revision is mutable",
				query: `UPDATE backup_asset_export_quota_buckets
					SET lifecycle_sweep_revision = 1 WHERE id = ?`,
				args: []any{userID},
			},
			{
				name: "user lease is mutable",
				query: `UPDATE backup_asset_export_quota_buckets
					SET lifecycle_sweep_lease_expires_at = ? WHERE id = ?`,
				args: []any{now.Add(time.Minute), userID},
			},
		} {
			t.Run(testCase.name, func(t *testing.T) {
				fixture.expectExecRejectedInRollback(t, db, testCase.query, testCase.args...)
			})
		}

		type quotaAccountingState struct {
			transitionRevision int64
			activeJobs         int64
			activeWorkers      int64
			activeReaders      int64
			reservedStoreBytes int64
			usedStoreBytes     int64
			updatedAt          time.Time
		}
		loadQuotaAccountingState := func(t *testing.T) quotaAccountingState {
			t.Helper()
			var state quotaAccountingState
			if err := db.QueryRow(fixture.bind(`SELECT transition_revision, active_jobs, active_workers,
				active_readers, reserved_store_bytes, used_store_bytes, updated_at
				FROM backup_asset_export_quota_buckets WHERE id = ?`), globalID).Scan(
				&state.transitionRevision, &state.activeJobs, &state.activeWorkers, &state.activeReaders,
				&state.reservedStoreBytes, &state.usedStoreBytes, &state.updatedAt,
			); err != nil {
				t.Fatalf("load %s quota accounting state: %v", fixture.engine, err)
			}
			return state
		}
		beforeSchedulerCAS := loadQuotaAccountingState(t)
		schedulerLease := now.Add(time.Minute)
		result, err := db.Exec(fixture.bind(`UPDATE backup_asset_export_quota_buckets
			SET lifecycle_next_sequence = 2, lifecycle_sweep_cursor = 0, lifecycle_sweep_high_water = 1,
				lifecycle_sweep_revision = 1, lifecycle_sweep_lease_expires_at = ?
			WHERE id = ? AND lifecycle_sweep_revision = 0`), schedulerLease, globalID)
		if err != nil {
			t.Fatalf("persist %s scheduler-only CAS: %v", fixture.engine, err)
		}
		if rows, err := result.RowsAffected(); err != nil || rows != 1 {
			t.Fatalf("persist %s scheduler-only CAS rows=%d err=%v, want 1 nil", fixture.engine, rows, err)
		}
		afterSchedulerCAS := loadQuotaAccountingState(t)
		if afterSchedulerCAS.transitionRevision != beforeSchedulerCAS.transitionRevision ||
			afterSchedulerCAS.activeJobs != beforeSchedulerCAS.activeJobs ||
			afterSchedulerCAS.activeWorkers != beforeSchedulerCAS.activeWorkers ||
			afterSchedulerCAS.activeReaders != beforeSchedulerCAS.activeReaders ||
			afterSchedulerCAS.reservedStoreBytes != beforeSchedulerCAS.reservedStoreBytes ||
			afterSchedulerCAS.usedStoreBytes != beforeSchedulerCAS.usedStoreBytes ||
			!afterSchedulerCAS.updatedAt.Equal(beforeSchedulerCAS.updatedAt) {
			t.Fatalf("%s scheduler-only CAS mutated quota accounting: before=%+v after=%+v",
				fixture.engine, beforeSchedulerCAS, afterSchedulerCAS)
		}
		loser, err := db.Exec(fixture.bind(`UPDATE backup_asset_export_quota_buckets
			SET lifecycle_sweep_lease_expires_at = ?
			WHERE id = ? AND lifecycle_sweep_revision = 0`), schedulerLease.Add(time.Minute), globalID)
		if err != nil {
			t.Fatalf("persist %s stale scheduler CAS: %v", fixture.engine, err)
		}
		if rows, err := loser.RowsAffected(); err != nil || rows != 0 {
			t.Fatalf("stale %s scheduler CAS rows=%d err=%v, want 0 nil", fixture.engine, rows, err)
		}
	})
	t.Run("ReaderSchedulerColumnsIndexesDefaultsAndChecks", func(t *testing.T) {
		_, db := fixture.openAt(t, backupAssetExportVersion)
		now := time.Now().UTC().Truncate(time.Second)
		globalID := strings.Repeat("c", 32)
		userID := strings.Repeat("d", 32)
		fixture.mustExec(t, db, `INSERT INTO backup_asset_export_quota_buckets
				(id, scope, subject, transition_revision, active_jobs, active_workers, active_readers,
				 reserved_store_bytes, used_store_bytes, created_at, updated_at)
				VALUES (?, 'global', 'global', 1, 0, 0, 0, 0, 0, ?, ?)`, globalID, now, now)
		fixture.mustExec(t, db, `INSERT INTO backup_asset_export_quota_buckets
				(id, scope, subject, transition_revision, active_jobs, active_workers, active_readers,
				 reserved_store_bytes, used_store_bytes, created_at, updated_at)
				VALUES (?, 'user', '42', 1, 0, 0, 0, 0, 0, ?, ?)`, userID, now, now)

		assertDefaults := func(t *testing.T, bucketID string) {
			t.Helper()
			var next, cursor, highWater, revision int64
			var lease any
			if err := db.QueryRow(fixture.bind(`SELECT reader_next_sequence, reader_sweep_cursor,
					reader_sweep_high_water, reader_sweep_revision, reader_sweep_lease_expires_at
					FROM backup_asset_export_quota_buckets WHERE id = ?`), bucketID).
				Scan(&next, &cursor, &highWater, &revision, &lease); err != nil {
				t.Fatalf("load %s reader scheduler defaults: %v", fixture.engine, err)
			}
			if next != 1 || cursor != 0 || highWater != 0 || revision != 0 || lease != nil {
				t.Fatalf("%s reader scheduler defaults are not inert: next=%d cursor=%d high_water=%d revision=%d lease=%v",
					fixture.engine, next, cursor, highWater, revision, lease)
			}
		}
		assertDefaults(t, globalID)
		assertDefaults(t, userID)
		t.Run("physical column types", func(t *testing.T) {
			if fixture.engine == "sqlite" {
				loadTypes := func(table string) map[string]string {
					t.Helper()
					rows, err := db.Query(`PRAGMA table_info('` + table + `')`)
					if err != nil {
						t.Fatal(err)
					}
					defer closeMigrationRows(t, rows)
					types := make(map[string]string)
					for rows.Next() {
						var cid, notNull, primaryKey int
						var name, columnType string
						var defaultValue sql.NullString
						if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
							t.Fatal(err)
						}
						types[name] = strings.ToUpper(columnType)
					}
					if err := rows.Err(); err != nil {
						t.Fatal(err)
					}
					return types
				}
				bucketTypes := loadTypes("backup_asset_export_quota_buckets")
				for _, column := range []string{"reader_next_sequence", "reader_sweep_cursor", "reader_sweep_high_water", "reader_sweep_revision"} {
					if bucketTypes[column] != "INTEGER" {
						t.Fatalf("SQLite %s type=%q want INTEGER", column, bucketTypes[column])
					}
				}
				if bucketTypes["reader_sweep_lease_expires_at"] != "DATETIME" {
					t.Fatalf("SQLite reader sweep lease type=%q want DATETIME", bucketTypes["reader_sweep_lease_expires_at"])
				}
				reservationTypes := loadTypes("backup_asset_export_reservations")
				if reservationTypes["reader_enqueue_sequence"] != "INTEGER" {
					t.Fatalf("SQLite reader enqueue sequence type=%q want INTEGER", reservationTypes["reader_enqueue_sequence"])
				}
				return
			}

			rows, err := db.Query(fixture.bind(`SELECT table_name, column_name, data_type
					FROM information_schema.columns
					WHERE table_schema = current_schema()
					  AND ((table_name = ? AND column_name IN (?, ?, ?, ?, ?))
					       OR (table_name = ? AND column_name = ?))`),
				"backup_asset_export_quota_buckets", "reader_next_sequence", "reader_sweep_cursor",
				"reader_sweep_high_water", "reader_sweep_revision", "reader_sweep_lease_expires_at",
				"backup_asset_export_reservations", "reader_enqueue_sequence")
			if err != nil {
				t.Fatal(err)
			}
			defer closeMigrationRows(t, rows)
			types := make(map[string]string)
			for rows.Next() {
				var table, column, columnType string
				if err := rows.Scan(&table, &column, &columnType); err != nil {
					t.Fatal(err)
				}
				types[table+"."+column] = columnType
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			for _, column := range []string{"reader_next_sequence", "reader_sweep_cursor", "reader_sweep_high_water", "reader_sweep_revision"} {
				if types["backup_asset_export_quota_buckets."+column] != "bigint" {
					t.Fatalf("PostgreSQL %s type=%q want bigint", column, types["backup_asset_export_quota_buckets."+column])
				}
			}
			if types["backup_asset_export_quota_buckets.reader_sweep_lease_expires_at"] != "timestamp with time zone" {
				t.Fatalf("PostgreSQL reader sweep lease type=%q want TIMESTAMPTZ", types["backup_asset_export_quota_buckets.reader_sweep_lease_expires_at"])
			}
			if types["backup_asset_export_reservations.reader_enqueue_sequence"] != "bigint" {
				t.Fatalf("PostgreSQL reader enqueue sequence type=%q want bigint", types["backup_asset_export_reservations.reader_enqueue_sequence"])
			}
		})

		for _, testCase := range []struct {
			name  string
			query string
			args  []any
		}{
			{name: "global cursor exceeds high water", query: `UPDATE backup_asset_export_quota_buckets
					SET reader_sweep_cursor = 1 WHERE id = ?`, args: []any{globalID}},
			{name: "global high water reaches next sequence", query: `UPDATE backup_asset_export_quota_buckets
					SET reader_sweep_revision = 1, reader_sweep_high_water = reader_next_sequence WHERE id = ?`, args: []any{globalID}},
			{name: "global lease without revision", query: `UPDATE backup_asset_export_quota_buckets
					SET reader_sweep_lease_expires_at = ? WHERE id = ?`, args: []any{now.Add(time.Minute), globalID}},
			{name: "global high water without revision", query: `UPDATE backup_asset_export_quota_buckets
					SET reader_next_sequence = 2, reader_sweep_high_water = 1 WHERE id = ?`, args: []any{globalID}},
			{name: "global revision without high water", query: `UPDATE backup_asset_export_quota_buckets
					SET reader_sweep_revision = 1 WHERE id = ?`, args: []any{globalID}},
			{name: "user next sequence is mutable", query: `UPDATE backup_asset_export_quota_buckets
					SET reader_next_sequence = 2 WHERE id = ?`, args: []any{userID}},
			{name: "user cursor is mutable", query: `UPDATE backup_asset_export_quota_buckets
					SET reader_next_sequence = 2, reader_sweep_cursor = 1, reader_sweep_high_water = 1,
					reader_sweep_revision = 1 WHERE id = ?`, args: []any{userID}},
			{name: "user revision is mutable", query: `UPDATE backup_asset_export_quota_buckets
					SET reader_sweep_revision = 1 WHERE id = ?`, args: []any{userID}},
			{name: "user lease is mutable", query: `UPDATE backup_asset_export_quota_buckets
					SET reader_sweep_lease_expires_at = ? WHERE id = ?`, args: []any{now.Add(time.Minute), userID}},
		} {
			t.Run(testCase.name, func(t *testing.T) {
				fixture.expectExecRejectedInRollback(t, db, testCase.query, testCase.args...)
			})
		}

		type quotaAccountingState struct {
			transitionRevision int64
			activeJobs         int64
			activeWorkers      int64
			activeReaders      int64
			reservedStoreBytes int64
			usedStoreBytes     int64
			updatedAt          time.Time
		}
		loadAccounting := func(t *testing.T) quotaAccountingState {
			t.Helper()
			var state quotaAccountingState
			if err := db.QueryRow(fixture.bind(`SELECT transition_revision, active_jobs, active_workers,
					active_readers, reserved_store_bytes, used_store_bytes, updated_at
					FROM backup_asset_export_quota_buckets WHERE id = ?`), globalID).Scan(
				&state.transitionRevision, &state.activeJobs, &state.activeWorkers, &state.activeReaders,
				&state.reservedStoreBytes, &state.usedStoreBytes, &state.updatedAt,
			); err != nil {
				t.Fatalf("load %s reader scheduler accounting state: %v", fixture.engine, err)
			}
			return state
		}
		beforeSchedulerCAS := loadAccounting(t)
		schedulerLease := now.Add(time.Minute)
		result, err := db.Exec(fixture.bind(`UPDATE backup_asset_export_quota_buckets
				SET reader_next_sequence = 2, reader_sweep_cursor = 0, reader_sweep_high_water = 1,
					reader_sweep_revision = 1, reader_sweep_lease_expires_at = ?
				WHERE id = ? AND reader_sweep_revision = 0`), schedulerLease, globalID)
		if err != nil {
			t.Fatalf("persist %s reader scheduler-only CAS: %v", fixture.engine, err)
		}
		if rows, err := result.RowsAffected(); err != nil || rows != 1 {
			t.Fatalf("persist %s reader scheduler-only CAS rows=%d err=%v, want 1 nil", fixture.engine, rows, err)
		}
		afterSchedulerCAS := loadAccounting(t)
		if afterSchedulerCAS != beforeSchedulerCAS {
			t.Fatalf("%s reader scheduler-only CAS mutated quota accounting: before=%+v after=%+v",
				fixture.engine, beforeSchedulerCAS, afterSchedulerCAS)
		}
		loser, err := db.Exec(fixture.bind(`UPDATE backup_asset_export_quota_buckets
				SET reader_sweep_lease_expires_at = ?
				WHERE id = ? AND reader_sweep_revision = 0`), schedulerLease.Add(time.Minute), globalID)
		if err != nil {
			t.Fatalf("persist %s stale reader scheduler CAS: %v", fixture.engine, err)
		}
		if rows, err := loser.RowsAffected(); err != nil || rows != 0 {
			t.Fatalf("stale %s reader scheduler CAS rows=%d err=%v, want 0 nil", fixture.engine, rows, err)
		}

		for _, index := range []struct {
			name      string
			fragments []string
		}{
			{name: "idx_backup_asset_export_reservations_reader_enqueue_sequence", fragments: []string{
				"unique index", "bucket_id", "reader_enqueue_sequence", "where", "kind", "reader",
			}},
			{name: "idx_backup_asset_export_reservations_reader_sweep", fragments: []string{
				"bucket_id", "reader_enqueue_sequence", "lease_expires_at", "where", "kind", "reader", "state", "active",
			}},
		} {
			definition := fixture.indexDefinition(t, db, index.name)
			for _, fragment := range index.fragments {
				if !strings.Contains(definition, fragment) {
					t.Fatalf("%s reader index %s omits %q: %s", fixture.engine, index.name, fragment, definition)
				}
			}
		}

		insertReader := `INSERT INTO backup_asset_export_reservations
				(id, bucket_id, kind, reader_enqueue_sequence, reserved_slots, lease_owner, lease_expires_at, state, created_at, updated_at)
				VALUES (?, ?, 'reader', ?, 1, ?, ?, 'active', ?, ?)`
		fixture.mustExec(t, db, insertReader, strings.Repeat("1", 32), globalID, 1, "reader-sequence-one", now.Add(time.Hour), now, now)
		fixture.expectExecRejectedInRollback(t, db, insertReader,
			strings.Repeat("2", 32), globalID, 1, "reader-sequence-duplicate", now.Add(time.Hour), now, now)
		fixture.expectExecRejectedInRollback(t, db, insertReader,
			strings.Repeat("3", 32), globalID, 0, "reader-sequence-zero", now.Add(time.Hour), now, now)
		fixture.expectExecRejectedInRollback(t, db, `INSERT INTO backup_asset_export_reservations
				(id, bucket_id, kind, reader_enqueue_sequence, reserved_slots, lease_owner, lease_expires_at, state, created_at, updated_at)
				VALUES (?, ?, 'job', 1, 1, 'non-reader-sequence', ?, 'active', ?, ?)`,
			strings.Repeat("4", 32), globalID, now.Add(time.Hour), now, now)
	})
	t.Run("FrozenJobLimitSnapshotChecksAreClosed", func(t *testing.T) {
		_, db := fixture.openAt(t, backupAssetExportVersion)
		now := time.Now().UTC().Truncate(time.Second)
		fixture.seed068CryptographicRows(t, db, now)
		jobID := strings.Repeat("1", 32)
		const exactMinimumCiphertextBytes = int64(10485760 + 10*1024 + 67108864)
		fixture.mustExec(t, db, `UPDATE backup_asset_export_jobs SET max_ciphertext_bytes = ? WHERE id = ?`,
			exactMinimumCiphertextBytes, jobID)
		for _, testCase := range []struct {
			name  string
			query string
			args  []any
		}{
			{
				name:  "unknown selection schema",
				query: `UPDATE backup_asset_export_jobs SET selection_schema_version = 2 WHERE id = ?`,
				args:  []any{jobID},
			},
			{
				name:  "unknown limits schema",
				query: `UPDATE backup_asset_export_jobs SET limits_schema_version = 2 WHERE id = ?`,
				args:  []any{jobID},
			},
			{
				name:  "source points exceed items",
				query: `UPDATE backup_asset_export_jobs SET max_source_points = max_items + 1 WHERE id = ?`,
				args:  []any{jobID},
			},
			{
				name:  "item bytes exceed logical bytes",
				query: `UPDATE backup_asset_export_jobs SET max_item_bytes = max_logical_bytes + 1 WHERE id = ?`,
				args:  []any{jobID},
			},
			{
				name:  "provider bytes below logical bytes",
				query: `UPDATE backup_asset_export_jobs SET max_provider_bytes = max_logical_bytes - 1 WHERE id = ?`,
				args:  []any{jobID},
			},
			{
				name:  "ciphertext omits fixed and per-item overhead",
				query: `UPDATE backup_asset_export_jobs SET max_ciphertext_bytes = ? WHERE id = ?`,
				args:  []any{exactMinimumCiphertextBytes - 1, jobID},
			},
			{
				name: "renew margin reaches half lease TTL",
				query: `UPDATE backup_asset_export_jobs
					SET lease_ttl_seconds = 120, lease_renew_margin_seconds = 60 WHERE id = ?`,
				args: []any{jobID},
			},
		} {
			t.Run(testCase.name, func(t *testing.T) {
				fixture.expectExecRejectedInRollback(t, db, testCase.query, testCase.args...)
			})
		}
	})
	t.Run("LifecycleMaintenanceIndexesCoverBoundedQueries", func(t *testing.T) {
		_, db := fixture.openAt(t, backupAssetExportVersion)
		for indexName, fragments := range map[string][]string{
			"idx_backup_asset_archive_member_requests_state_id": {
				"backup_asset_archive_member_requests", "state", "id",
			},
			"idx_backup_asset_export_delivery_grants_member_state": {
				"backup_asset_export_delivery_grants", "member_request_id", "resource_kind", "state", "id",
			},
			"idx_backup_asset_export_delivery_grants_export_job": {
				"backup_asset_export_delivery_grants", "export_job_id", "resource_kind", "id",
			},
		} {
			indexName := indexName
			fragments := fragments
			t.Run(indexName, func(t *testing.T) {
				definition := fixture.indexDefinition(t, db, indexName)
				if definition == "" {
					t.Fatalf("%s maintenance index %s is missing", fixture.engine, indexName)
				}
				for _, fragment := range fragments {
					if !strings.Contains(definition, fragment) {
						t.Fatalf("%s maintenance index %s omits %q: %s",
							fixture.engine, indexName, fragment, definition)
					}
				}
			})
		}
	})
	t.Run("SQLiteOpaquePrimaryKeysAreExplicitlyNotNull", func(t *testing.T) {
		if fixture.engine != "sqlite" {
			return
		}
		_, db := fixture.openAt(t, backupAssetExportVersion)
		for _, table := range backupAssetExportTables {
			var notNull int
			if err := db.QueryRow(`SELECT "notnull" FROM pragma_table_info(?) WHERE name = 'id'`, table).Scan(&notNull); err != nil {
				t.Fatalf("inspect SQLite opaque primary key %s.id: %v", table, err)
			}
			if notNull != 1 {
				t.Fatalf("SQLite opaque primary key %s.id is nullable", table)
			}
		}
	})
	t.Run("ArchiveFormatProfilePairIsClosed", func(t *testing.T) {
		_, db := fixture.openAt(t, backupAssetExportVersion)
		now := time.Now().UTC().Truncate(time.Second)
		fixture.insertSearchMigrationUser(t, db, 6683, "export-archive-pair-user", now)
		const insertJob = `INSERT INTO backup_asset_export_jobs
			(id, owner_user_id, lifecycle_enqueue_sequence, selection_digest, selection_schema_version, archive_format, archive_profile,
			 limits_schema_version, chunk_bytes, max_items, max_source_points, max_item_bytes, max_logical_bytes,
			 max_provider_bytes, max_ciphertext_bytes, max_open_readers, max_duration_seconds, max_attempts,
			 retry_base_seconds, retry_max_delay_seconds, lease_ttl_seconds, lease_renew_margin_seconds,
			 ready_ttl_seconds, execution_state, result_kind, cleanup_state, absolute_deadline,
			 item_count, packed_count, skipped_count, failed_count, transition_revision, created_at, updated_at)
			VALUES (?, 6683, ?, ?, 1, ?, ?, 1, 65536, 10, 2, 1048576, 10485760,
			 10485760, 77604864, 2, 300, 3, 1, 10, 120, 30, 86400, 'queued', '',
			 'none', ?, 0, 0, 0, 0, 1, ?, ?)`
		for index, pair := range []struct {
			format  any
			profile any
		}{
			{format: "zip", profile: "zip_deflate_v1"},
			{format: "tar", profile: "tar_none_v1"},
			{format: "tar", profile: "tar_gzip_v1"},
		} {
			fixture.mustExec(t, db, insertJob,
				strings.Repeat(strconv.Itoa(index+1), 32), index+1, strings.Repeat("a", 64),
				pair.format, pair.profile, now.Add(time.Hour), now, now)
		}
		for _, testCase := range []struct {
			name    string
			format  any
			profile any
		}{
			{name: "missing format", profile: "zip_deflate_v1"},
			{name: "missing profile", format: "zip"},
			{name: "unknown format", format: "rar", profile: "zip_deflate_v1"},
			{name: "unknown profile", format: "zip", profile: "future_v2"},
			{name: "zip crossed with tar none", format: "zip", profile: "tar_none_v1"},
			{name: "zip crossed with tar gzip", format: "zip", profile: "tar_gzip_v1"},
			{name: "tar crossed with zip", format: "tar", profile: "zip_deflate_v1"},
		} {
			t.Run(testCase.name, func(t *testing.T) {
				fixture.expectExecRejectedInRollback(t, db, insertJob,
					strings.Repeat("f", 32), 100, strings.Repeat("b", 64), testCase.format, testCase.profile,
					now.Add(time.Hour), now, now)
			})
		}
	})
	t.Run("ReadyLifecycleTimestampProductIsClosed", func(t *testing.T) {
		_, db := fixture.openAt(t, backupAssetExportVersion)
		now := time.Now().UTC().Truncate(time.Second)
		fixture.insertSearchMigrationUser(t, db, 6684, "export-ready-timestamp-user", now)
		const insertJob = `INSERT INTO backup_asset_export_jobs
			(id, owner_user_id, lifecycle_enqueue_sequence, selection_digest, selection_schema_version, archive_format, archive_profile,
			 limits_schema_version, chunk_bytes, max_items, max_source_points, max_item_bytes, max_logical_bytes,
			 max_provider_bytes, max_ciphertext_bytes, max_open_readers, max_duration_seconds, max_attempts,
			 retry_base_seconds, retry_max_delay_seconds, lease_ttl_seconds, lease_renew_margin_seconds,
			 ready_ttl_seconds, execution_state, result_kind, cleanup_state, absolute_deadline, ready_at, expires_at,
			 item_count, packed_count, skipped_count, failed_count, transition_revision, created_at, updated_at)
			VALUES (?, 6684, ?, ?, 1, 'zip', 'zip_deflate_v1', 1, 65536, 10, 2, 1048576, 10485760,
			 10485760, 77604864, 2, 300, 3, 1, 10, 120, 30, 86400, ?, ?, 'none', ?, ?, ?,
			 ?, ?, 0, 0, 1, ?, ?)`
		absoluteDeadline := now.Add(5 * time.Minute)
		readyAt := now.Add(4 * time.Minute)
		expiresAt := now.Add(15 * time.Minute)
		idCharacters := "0123456789abcdef"
		insert := func(idCharacter byte, state, resultKind string, ready, expires any, itemCount, packedCount int, updatedAt time.Time) {
			sequence := strings.IndexByte(idCharacters, idCharacter) + 1
			fixture.mustExec(t, db, insertJob,
				strings.Repeat(string(idCharacter), 32), sequence, strings.Repeat(string(idCharacter), 64),
				state, resultKind, absoluteDeadline, ready, expires, itemCount, packedCount, now, updatedAt)
		}

		for index, state := range []string{
			"queued", "running", "retry_wait", "sealing", "cancel_requested", "failed", "source_expired", "canceled",
		} {
			t.Run(state+" permits no ready timestamps", func(t *testing.T) {
				insert(idCharacters[index], state, "", nil, nil, 0, 0, now)
			})
		}
		for index, state := range []string{"ready", "expiring", "expired"} {
			t.Run(state+" requires an ordered timestamp pair independent of execution deadline", func(t *testing.T) {
				insert(idCharacters[index+8], state, "complete", readyAt, expiresAt, 1, 1, expiresAt)
			})
		}
		for index, state := range []string{"cancel_requested", "canceled"} {
			t.Run(state+" preserves ready timestamp history", func(t *testing.T) {
				insert(idCharacters[index+11], state, "complete", readyAt, expiresAt, 1, 1, expiresAt)
			})
		}

		for _, testCase := range []struct {
			name      string
			state     string
			readyAt   any
			expiresAt any
		}{
			{name: "ready missing both timestamps", state: "ready"},
			{name: "expiring missing both timestamps", state: "expiring"},
			{name: "expired missing both timestamps", state: "expired"},
			{name: "ready missing ready timestamp", state: "ready", expiresAt: expiresAt},
			{name: "expiring missing ready timestamp", state: "expiring", expiresAt: expiresAt},
			{name: "expired missing ready timestamp", state: "expired", expiresAt: expiresAt},
			{name: "ready missing expiry timestamp", state: "ready", readyAt: readyAt},
			{name: "expiring missing expiry timestamp", state: "expiring", readyAt: readyAt},
			{name: "expired missing expiry timestamp", state: "expired", readyAt: readyAt},
			{name: "ready has zero duration", state: "ready", readyAt: readyAt, expiresAt: readyAt},
			{name: "expiring has zero duration", state: "expiring", readyAt: readyAt, expiresAt: readyAt},
			{name: "expired has zero duration", state: "expired", readyAt: readyAt, expiresAt: readyAt},
		} {
			t.Run(testCase.name, func(t *testing.T) {
				fixture.expectExecRejectedInRollback(t, db, insertJob,
					strings.Repeat("f", 32), 16, strings.Repeat("f", 64), testCase.state, "complete",
					absoluteDeadline, testCase.readyAt, testCase.expiresAt, 1, 1, now, expiresAt)
			})
		}
		for _, testCase := range []struct {
			name       string
			state      string
			resultKind string
			packed     int
		}{
			{name: "ready missing result", state: "ready", packed: 1},
			{name: "expiring missing result", state: "expiring", packed: 1},
			{name: "expired missing result", state: "expired", packed: 1},
			{name: "ready has no packed items", state: "ready", resultKind: "complete"},
			{name: "expiring has no packed items", state: "expiring", resultKind: "complete"},
			{name: "expired has no packed items", state: "expired", resultKind: "complete"},
		} {
			t.Run(testCase.name, func(t *testing.T) {
				fixture.expectExecRejectedInRollback(t, db, insertJob,
					strings.Repeat("e", 32), 15, strings.Repeat("e", 64), testCase.state, testCase.resultKind,
					absoluteDeadline, readyAt, expiresAt, 1, testCase.packed, now, expiresAt)
			})
		}
	})
	t.Run("IdempotencyReceiptCreatedAtPrecedesExpiry", func(t *testing.T) {
		_, db := fixture.openAt(t, backupAssetExportVersion)
		now := time.Now().UTC().Truncate(time.Second)
		fixture.seed068ParityRows(t, db, now)
		fixture.expectExecRejectedInRollback(t, db,
			`UPDATE backup_asset_export_idempotency SET expires_at = ? WHERE id = ?`,
			now.Add(-time.Second), strings.Repeat("a", 32),
		)
	})
	t.Run("PristineDownRestores067", func(t *testing.T) {
		migrator, db := fixture.openAt(t, backupAssetExportVersion)
		if err := migrator.Steps(-1); err != nil {
			t.Fatalf("step %s migration down to 000067: %v", fixture.engine, err)
		}
		assertMigrationVersion(t, migrator, backupAssetProcessingVersion)
		for _, table := range backupAssetExportTables {
			if databaseTableExists(t, db, fixture.engine, table) {
				t.Fatalf("%s export table %s remains after pristine down", fixture.engine, table)
			}
		}
		definition := fixture.tableDefinition(t, db, "wrapped_domain_keys")
		if !strings.Contains(definition, "derived_store") || strings.Contains(definition, "export_store") {
			t.Fatalf("%s wrapped key CHECK was not restored to 000067: %s", fixture.engine, definition)
		}
		if !databaseTableExists(t, db, fixture.engine, "backup_asset_processing_jobs") {
			t.Fatalf("%s 000067 processing schema was removed by 000068 down", fixture.engine)
		}
	})
	t.Run("GlobalQuotaBucketPermanentlyBlocksUsedDown", func(t *testing.T) {
		migrator, db := fixture.openAt(t, backupAssetExportVersion)
		now := time.Now().UTC().Truncate(time.Second)
		fixture.mustExec(t, db, `INSERT INTO backup_asset_export_quota_buckets
			(id, scope, subject, transition_revision, active_jobs, active_workers, active_readers,
			 reserved_store_bytes, used_store_bytes, created_at, updated_at)
			 VALUES (?, 'global', 'global', 1, 0, 0, 0, 0, 0, ?, ?)`, strings.Repeat("e", 32), now, now)
		if err := fixture.executeExportDown(db); err == nil {
			t.Fatalf("%s used 000068 down unexpectedly succeeded", fixture.engine)
		}
		assertMigrationVersion(t, migrator, backupAssetExportVersion)
		if !databaseTableExists(t, db, fixture.engine, "backup_asset_export_quota_buckets") {
			t.Fatalf("%s used-down removed the durable use latch", fixture.engine)
		}
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM backup_asset_export_quota_buckets`).Scan(&count); err != nil {
			t.Fatalf("count %s durable use latch rows: %v", fixture.engine, err)
		}
		if count != 1 {
			t.Fatalf("%s used-down changed durable use latch count: got=%d want=1", fixture.engine, count)
		}
	})
	t.Run("ArchiveMemberTerminalCleanupMayAdvanceAfterAbsoluteExpiry", func(t *testing.T) {
		_, db := fixture.openAt(t, backupAssetExportVersion)
		now := time.Now().UTC().Truncate(time.Second)
		fixture.insertSearchMigrationUser(t, db, 6681, "archive-member-expiry-user", now)
		expiresAt := now.Add(time.Minute)
		idempotencyExpiresAt := now.Add(30 * time.Minute)
		fixture.mustExec(t, db, `INSERT INTO backup_asset_archive_member_requests
			(id, owner_user_id, endpoint, key_digest, request_intent_digest, recovery_point_id, entry_id,
			 catalog_generation_id, source_fingerprint, entry_fingerprint, index_artifact_id, index_revision,
			 member_chain_digest, resolved_ordinal, state, error_category, idempotency_expires_at, absolute_expires_at,
			 created_at, updated_at, version)
			VALUES (?, 6681, 'archive_member', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 'queued', '', ?, ?, ?, ?, 1)`,
			strings.Repeat("1", 32), strings.Repeat("2", 64), strings.Repeat("3", 64),
			strings.Repeat("4", 32), strings.Repeat("5", 64), strings.Repeat("6", 32),
			strings.Repeat("7", 64), strings.Repeat("8", 64), strings.Repeat("9", 32),
			strings.Repeat("a", 64), strings.Repeat("b", 64), idempotencyExpiresAt, expiresAt, now, now)

		cleanupAt := expiresAt.Add(time.Minute)
		fixture.mustExec(t, db, `UPDATE backup_asset_archive_member_requests
			SET state = 'expired', finished_at = ?, updated_at = ?, version = version + 1
			WHERE id = ?`, cleanupAt, cleanupAt, strings.Repeat("1", 32))
	})
	t.Run("CleanupMayAdvanceAfterExecutionDeadlineAndRetentionCapsLease", func(t *testing.T) {
		_, db := fixture.openAt(t, backupAssetExportVersion)
		now := time.Now().UTC().Truncate(time.Second)
		fixture.insertSearchMigrationUser(t, db, 6680, "export-cleanup-user", now)

		jobID := strings.Repeat("8", 32)
		jobDeadline := now.Add(5 * time.Minute)
		readyAt := now.Add(4 * time.Minute)
		expiresAt := now.Add(15 * time.Minute)
		fixture.mustExec(t, db, `INSERT INTO backup_asset_export_jobs
			(id, owner_user_id, lifecycle_enqueue_sequence, selection_digest, selection_schema_version, archive_format, archive_profile,
			 limits_schema_version, chunk_bytes, max_items, max_source_points, max_item_bytes, max_logical_bytes,
			 max_provider_bytes, max_ciphertext_bytes, max_open_readers, max_duration_seconds, max_attempts,
			 retry_base_seconds, retry_max_delay_seconds, lease_ttl_seconds, lease_renew_margin_seconds,
			 ready_ttl_seconds, execution_state, result_kind, cleanup_state, absolute_deadline, ready_at, expires_at,
			 item_count, packed_count, skipped_count, failed_count, transition_revision, created_at, updated_at)
			VALUES (?, 6680, 1, ?, 1, 'zip', 'zip_deflate_v1', 1, 1048576, 10, 2, 1048576, 10485760,
			 10485760, 77604864, 2, 300, 3, 1, 10, 120, 30, 86400, 'ready', 'complete',
			 'none', ?, ?, ?, 1, 1, 0, 0, 1, ?, ?)`,
			jobID, strings.Repeat("a", 64), jobDeadline, readyAt, expiresAt, now, readyAt)

		leaseDeadline := now.Add(45 * time.Minute)
		retentionUntil := now.Add(20 * time.Minute)
		fixture.mustExec(t, db, `INSERT INTO backup_asset_export_source_leases
			(id, job_id, recovery_point_id, lease_id, lease_attempt_id, fence_hash, absolute_deadline,
			 retention_until, state, acquired_at, renewed_at, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'active', ?, ?, ?, ?)`,
			strings.Repeat("9", 32), jobID, strings.Repeat("7", 32), strings.Repeat("6", 32),
			strings.Repeat("5", 32), strings.Repeat("b", 64), leaseDeadline, retentionUntil,
			now, now, now, now)

		cleanupAt := jobDeadline.Add(time.Minute)
		fixture.mustExec(t, db, `UPDATE backup_asset_export_jobs
			SET cleanup_state = 'revoking', updated_at = ?, transition_revision = transition_revision + 1
			WHERE id = ?`, cleanupAt, jobID)
	})
	t.Run("CryptographicTombstonesAreCompletePairs", func(t *testing.T) {
		t.Run("Key", func(t *testing.T) {
			_, db := fixture.openAt(t, backupAssetExportVersion)
			now := time.Now().UTC().Truncate(time.Second)
			fixture.seed068CryptographicRows(t, db, now)
			fixture.mustExec(t, db, `UPDATE backup_asset_export_keys
				SET state = 'destroyed', wrapped_dek = ?, envelope_nonce = ?, destroyed_at = ?
				WHERE id = ?`, []byte{}, []byte{}, now.Add(time.Second), strings.Repeat("2", 32))
		})
		t.Run("LostKey", func(t *testing.T) {
			_, db := fixture.openAt(t, backupAssetExportVersion)
			now := time.Now().UTC().Truncate(time.Second)
			fixture.seed068CryptographicRows(t, db, now)
			fixture.mustExec(t, db, `UPDATE backup_asset_export_keys
				SET state = 'lost', wrapped_dek = ?, envelope_nonce = ?, destroyed_at = ?
				WHERE id = ?`, []byte{}, []byte{}, now.Add(time.Second), strings.Repeat("2", 32))
		})
		t.Run("Item", func(t *testing.T) {
			_, db := fixture.openAt(t, backupAssetExportVersion)
			now := time.Now().UTC().Truncate(time.Second)
			fixture.seed068CryptographicRows(t, db, now)
			fixture.mustExec(t, db, `UPDATE backup_asset_export_items
				SET path_nonce = ?, path_ciphertext = ? WHERE id = ?`,
				[]byte{}, []byte{}, strings.Repeat("3", 32))
		})
		t.Run("RejectsHalfWipes", func(t *testing.T) {
			_, db := fixture.openAt(t, backupAssetExportVersion)
			now := time.Now().UTC().Truncate(time.Second)
			fixture.seed068CryptographicRows(t, db, now)
			for _, testCase := range []struct {
				name  string
				query string
				args  []any
			}{
				{name: "key wrapped only", query: `UPDATE backup_asset_export_keys SET wrapped_dek = ?, envelope_nonce = ? WHERE id = ?`, args: []any{[]byte("wrapped"), []byte{}, strings.Repeat("2", 32)}},
				{name: "key nonce only", query: `UPDATE backup_asset_export_keys SET wrapped_dek = ?, envelope_nonce = ? WHERE id = ?`, args: []any{[]byte{}, []byte("123456789012"), strings.Repeat("2", 32)}},
				{name: "item ciphertext only", query: `UPDATE backup_asset_export_items SET path_nonce = ?, path_ciphertext = ? WHERE id = ?`, args: []any{[]byte{}, []byte("ciphertext"), strings.Repeat("3", 32)}},
				{name: "item nonce only", query: `UPDATE backup_asset_export_items SET path_nonce = ?, path_ciphertext = ? WHERE id = ?`, args: []any{[]byte("123456789012"), []byte{}, strings.Repeat("3", 32)}},
			} {
				t.Run(testCase.name, func(t *testing.T) {
					fixture.expectExecRejectedInRollback(t, db, testCase.query, testCase.args...)
				})
			}
		})
	})
	t.Run("ReservationLiveSlotIsUnique", func(t *testing.T) {
		_, db := fixture.openAt(t, backupAssetExportVersion)
		now := time.Now().UTC().Truncate(time.Second)
		bucketID := strings.Repeat("b", 32)
		fixture.mustExec(t, db, `INSERT INTO backup_asset_export_quota_buckets
			(id, scope, subject, transition_revision, active_jobs, active_workers, active_readers,
			 reserved_store_bytes, used_store_bytes, created_at, updated_at)
			 VALUES (?, 'global', 'global', 1, 0, 0, 0, 0, 0, ?, ?)`, bucketID, now, now)
		fixture.mustExec(t, db, `INSERT INTO backup_asset_export_reservations
			(id, bucket_id, kind, reserved_slots, lease_owner, lease_expires_at, state, created_at, updated_at)
			VALUES (?, ?, 'job', 1, 'same-owner', ?, 'active', ?, ?)`,
			strings.Repeat("c", 32), bucketID, now.Add(time.Hour), now, now)
		for index, state := range []string{"active", "purge_pending"} {
			t.Run(state, func(t *testing.T) {
				fixture.expectExecRejectedInRollback(t, db, `INSERT INTO backup_asset_export_reservations
					(id, bucket_id, kind, reserved_slots, lease_owner, lease_expires_at, state, created_at, updated_at)
					VALUES (?, ?, 'job', 1, 'same-owner', ?, ?, ?, ?)`,
					strings.Repeat(strconv.Itoa(index+1), 32), bucketID, now.Add(time.Hour), state, now, now)
			})
		}
		for index, state := range []string{"released", "expired"} {
			t.Run(state+" history permits later active", func(t *testing.T) {
				owner := "history-" + state
				fixture.mustExec(t, db, `INSERT INTO backup_asset_export_reservations
					(id, bucket_id, kind, reserved_slots, lease_owner, lease_expires_at, state, created_at, updated_at)
					VALUES (?, ?, 'job', 1, ?, ?, ?, ?, ?)`,
					strings.Repeat([]string{"d", "e"}[index], 32), bucketID, owner,
					now.Add(time.Hour), state, now, now)
				fixture.mustExec(t, db, `INSERT INTO backup_asset_export_reservations
					(id, bucket_id, kind, reserved_slots, lease_owner, lease_expires_at, state, created_at, updated_at)
					VALUES (?, ?, 'job', 1, ?, ?, 'active', ?, ?)`,
					strings.Repeat([]string{"f", "0"}[index], 32), bucketID, owner,
					now.Add(time.Hour), now, now)
			})
		}
	})
	t.Run("DeliveryActionAndRangeMatchResourceArm", func(t *testing.T) {
		_, db := fixture.openAt(t, backupAssetExportVersion)
		now := time.Now().UTC().Truncate(time.Second)
		fixture.seed068DeliveryRows(t, db, now)
		for _, testCase := range []struct {
			name  string
			query string
			args  []any
		}{
			{name: "export action", query: `UPDATE backup_asset_export_delivery_grants SET action = 'archive_member_download' WHERE id = ?`, args: []any{strings.Repeat("6", 32)}},
			{name: "export range", query: `UPDATE backup_asset_export_delivery_grants SET range_policy = 'none' WHERE id = ?`, args: []any{strings.Repeat("6", 32)}},
			{name: "member action", query: `UPDATE backup_asset_export_delivery_grants SET action = 'export_download' WHERE id = ?`, args: []any{strings.Repeat("7", 32)}},
			{name: "member range", query: `UPDATE backup_asset_export_delivery_grants SET range_policy = 'single' WHERE id = ?`, args: []any{strings.Repeat("7", 32)}},
		} {
			t.Run(testCase.name, func(t *testing.T) {
				fixture.expectExecRejectedInRollback(t, db, testCase.query, testCase.args...)
			})
		}
	})
	t.Run("KeyStatePayloadAndTimestampProductIsClosed", func(t *testing.T) {
		_, db := fixture.openAt(t, backupAssetExportVersion)
		now := time.Now().UTC().Truncate(time.Second)
		fixture.seed068CryptographicRows(t, db, now)
		for _, testCase := range []struct {
			name        string
			state       string
			wrappedDEK  []byte
			nonce       []byte
			destroyedAt any
		}{
			{name: "active tombstone", state: "active", wrappedDEK: []byte{}, nonce: []byte{}},
			{name: "active with destruction timestamp", state: "active", wrappedDEK: []byte("wrapped-dek"), nonce: []byte("123456789012"), destroyedAt: now},
			{name: "destroyed with live payload", state: "destroyed", wrappedDEK: []byte("wrapped-dek"), nonce: []byte("123456789012"), destroyedAt: now},
			{name: "destroyed without timestamp", state: "destroyed", wrappedDEK: []byte{}, nonce: []byte{}},
			{name: "lost with live payload", state: "lost", wrappedDEK: []byte("wrapped-dek"), nonce: []byte("123456789012"), destroyedAt: now},
			{name: "lost without timestamp", state: "lost", wrappedDEK: []byte{}, nonce: []byte{}},
			{name: "unknown state", state: "unknown", wrappedDEK: []byte("wrapped-dek"), nonce: []byte("123456789012")},
		} {
			t.Run(testCase.name, func(t *testing.T) {
				fixture.expectExecRejectedInRollback(t, db, `UPDATE backup_asset_export_keys
					SET state = ?, wrapped_dek = ?, envelope_nonce = ?, destroyed_at = ? WHERE id = ?`,
					testCase.state, testCase.wrappedDEK, testCase.nonce, testCase.destroyedAt, strings.Repeat("2", 32))
			})
		}
	})
	t.Run("RequiredStringsRejectEmptyAcrossEngines", func(t *testing.T) {
		_, db := fixture.openAt(t, backupAssetExportVersion)
		now := time.Now().UTC().Truncate(time.Second)
		fixture.seed068ParityRows(t, db, now)
		for _, testCase := range []struct {
			name   string
			table  string
			column string
			id     string
		}{
			{name: "jobs archive_profile", table: "backup_asset_export_jobs", column: "archive_profile", id: strings.Repeat("1", 32)},
			{name: "jobs current_attempt_id", table: "backup_asset_export_jobs", column: "current_attempt_id", id: strings.Repeat("1", 32)},
			{name: "keys wrap_algorithm", table: "backup_asset_export_keys", column: "wrap_algorithm", id: strings.Repeat("2", 32)},
			{name: "items recovery_point_id", table: "backup_asset_export_items", column: "recovery_point_id", id: strings.Repeat("3", 32)},
			{name: "items entry_id", table: "backup_asset_export_items", column: "entry_id", id: strings.Repeat("3", 32)},
			{name: "items catalog_generation_id", table: "backup_asset_export_items", column: "catalog_generation_id", id: strings.Repeat("3", 32)},
			{name: "items source_fingerprint", table: "backup_asset_export_items", column: "source_fingerprint", id: strings.Repeat("3", 32)},
			{name: "items entry_fingerprint", table: "backup_asset_export_items", column: "entry_fingerprint", id: strings.Repeat("3", 32)},
			{name: "items current_attempt_id", table: "backup_asset_export_items", column: "current_attempt_id", id: strings.Repeat("3", 32)},
			{name: "attempts worker_owner", table: "backup_asset_export_attempts", column: "worker_owner", id: strings.Repeat("4", 32)},
			{name: "source leases recovery_point_id", table: "backup_asset_export_source_leases", column: "recovery_point_id", id: strings.Repeat("9", 32)},
			{name: "source leases lease_id", table: "backup_asset_export_source_leases", column: "lease_id", id: strings.Repeat("9", 32)},
			{name: "source leases lease_attempt_id", table: "backup_asset_export_source_leases", column: "lease_attempt_id", id: strings.Repeat("9", 32)},
			{name: "artifacts locator", table: "backup_asset_export_artifacts", column: "locator", id: strings.Repeat("5", 32)},
			{name: "idempotency endpoint", table: "backup_asset_export_idempotency", column: "endpoint", id: strings.Repeat("a", 32)},
			{name: "idempotency committed result_job_id", table: "backup_asset_export_idempotency", column: "result_job_id", id: strings.Repeat("a", 32)},
			{name: "quota user subject", table: "backup_asset_export_quota_buckets", column: "subject", id: strings.Repeat("f", 32)},
			{name: "reservations lease_owner", table: "backup_asset_export_reservations", column: "lease_owner", id: strings.Repeat("e", 32)},
			{name: "archive member endpoint", table: "backup_asset_archive_member_requests", column: "endpoint", id: strings.Repeat("8", 32)},
			{name: "archive member recovery_point_id", table: "backup_asset_archive_member_requests", column: "recovery_point_id", id: strings.Repeat("8", 32)},
			{name: "archive member entry_id", table: "backup_asset_archive_member_requests", column: "entry_id", id: strings.Repeat("8", 32)},
			{name: "archive member catalog_generation_id", table: "backup_asset_archive_member_requests", column: "catalog_generation_id", id: strings.Repeat("8", 32)},
			{name: "archive member source_fingerprint", table: "backup_asset_archive_member_requests", column: "source_fingerprint", id: strings.Repeat("8", 32)},
			{name: "archive member entry_fingerprint", table: "backup_asset_archive_member_requests", column: "entry_fingerprint", id: strings.Repeat("8", 32)},
			{name: "archive member index_artifact_id", table: "backup_asset_archive_member_requests", column: "index_artifact_id", id: strings.Repeat("8", 32)},
			{name: "archive member index_revision", table: "backup_asset_archive_member_requests", column: "index_revision", id: strings.Repeat("8", 32)},
			{name: "archive member processing_interest_id", table: "backup_asset_archive_member_requests", column: "processing_interest_id", id: strings.Repeat("8", 32)},
			{name: "archive member processing_job_id", table: "backup_asset_archive_member_requests", column: "processing_job_id", id: strings.Repeat("8", 32)},
			{name: "delivery session_jti", table: "backup_asset_export_delivery_grants", column: "session_jti", id: strings.Repeat("7", 32)},
			{name: "delivery proof_id", table: "backup_asset_export_delivery_grants", column: "proof_id", id: strings.Repeat("7", 32)},
			{name: "delivery canonical_path", table: "backup_asset_export_delivery_grants", column: "canonical_path", id: strings.Repeat("7", 32)},
		} {
			t.Run(testCase.name, func(t *testing.T) {
				query := fmt.Sprintf("UPDATE %s SET %s = '' WHERE id = ?", testCase.table, testCase.column)
				fixture.expectExecRejectedInRollback(t, db, query, testCase.id)
			})
		}
	})
	t.Run("ArchiveMemberOpaqueIDsAreCanonical", func(t *testing.T) {
		_, db := fixture.openAt(t, backupAssetExportVersion)
		now := time.Now().UTC().Truncate(time.Second)
		fixture.seed068DeliveryRows(t, db, now)
		for _, column := range []string{
			"processing_job_id", "processing_attempt_id", "derived_artifact_set_id", "derived_artifact_id", "derived_blob_id",
		} {
			for _, invalid := range []struct {
				name  string
				value string
			}{
				{name: "empty", value: ""},
				{name: "wrong length", value: strings.Repeat("a", 31)},
				{name: "uppercase", value: strings.Repeat("A", 32)},
				{name: "nonhex", value: strings.Repeat("g", 32)},
			} {
				t.Run(column+" "+invalid.name, func(t *testing.T) {
					query := fmt.Sprintf("UPDATE backup_asset_export_delivery_grants SET %s = ? WHERE id = ?", column)
					fixture.expectExecRejectedInRollback(t, db, query, invalid.value, strings.Repeat("7", 32))
				})
			}
		}
	})
	t.Run("FixedWidthFieldsRejectAdjacentLengths", func(t *testing.T) {
		_, db := fixture.openAt(t, backupAssetExportVersion)
		now := time.Now().UTC().Truncate(time.Second)
		fixture.seed068ParityRows(t, db, now)
		for _, testCase := range []struct {
			name   string
			table  string
			column string
			id     string
			width  int
		}{
			{name: "jobs current_attempt_id", table: "backup_asset_export_jobs", column: "current_attempt_id", id: strings.Repeat("1", 32), width: 32},
			{name: "keys job_id", table: "backup_asset_export_keys", column: "job_id", id: strings.Repeat("2", 32), width: 32},
			{name: "items job_id", table: "backup_asset_export_items", column: "job_id", id: strings.Repeat("3", 32), width: 32},
			{name: "items recovery_point_id", table: "backup_asset_export_items", column: "recovery_point_id", id: strings.Repeat("3", 32), width: 32},
			{name: "items entry_id", table: "backup_asset_export_items", column: "entry_id", id: strings.Repeat("3", 32), width: 64},
			{name: "items catalog_generation_id", table: "backup_asset_export_items", column: "catalog_generation_id", id: strings.Repeat("3", 32), width: 32},
			{name: "items current_attempt_id", table: "backup_asset_export_items", column: "current_attempt_id", id: strings.Repeat("3", 32), width: 32},
			{name: "attempts job_id", table: "backup_asset_export_attempts", column: "job_id", id: strings.Repeat("4", 32), width: 32},
			{name: "attempts fence_digest", table: "backup_asset_export_attempts", column: "fence_digest", id: strings.Repeat("4", 32), width: 64},
			{name: "source leases job_id", table: "backup_asset_export_source_leases", column: "job_id", id: strings.Repeat("9", 32), width: 32},
			{name: "source leases recovery_point_id", table: "backup_asset_export_source_leases", column: "recovery_point_id", id: strings.Repeat("9", 32), width: 32},
			{name: "source leases lease_id", table: "backup_asset_export_source_leases", column: "lease_id", id: strings.Repeat("9", 32), width: 32},
			{name: "source leases lease_attempt_id", table: "backup_asset_export_source_leases", column: "lease_attempt_id", id: strings.Repeat("9", 32), width: 32},
			{name: "source leases fence_hash", table: "backup_asset_export_source_leases", column: "fence_hash", id: strings.Repeat("9", 32), width: 64},
			{name: "artifacts job_id", table: "backup_asset_export_artifacts", column: "job_id", id: strings.Repeat("5", 32), width: 32},
			{name: "artifacts attempt_id", table: "backup_asset_export_artifacts", column: "attempt_id", id: strings.Repeat("5", 32), width: 32},
			{name: "artifacts job_key_id", table: "backup_asset_export_artifacts", column: "job_key_id", id: strings.Repeat("5", 32), width: 32},
			{name: "artifacts plaintext_digest", table: "backup_asset_export_artifacts", column: "plaintext_digest", id: strings.Repeat("5", 32), width: 64},
			{name: "artifacts archive_digest", table: "backup_asset_export_artifacts", column: "archive_digest", id: strings.Repeat("5", 32), width: 64},
			{name: "artifacts ciphertext_digest", table: "backup_asset_export_artifacts", column: "ciphertext_digest", id: strings.Repeat("5", 32), width: 64},
			{name: "idempotency key_digest", table: "backup_asset_export_idempotency", column: "key_digest", id: strings.Repeat("a", 32), width: 64},
			{name: "idempotency request_intent_digest", table: "backup_asset_export_idempotency", column: "request_intent_digest", id: strings.Repeat("a", 32), width: 64},
			{name: "idempotency result_job_id", table: "backup_asset_export_idempotency", column: "result_job_id", id: strings.Repeat("a", 32), width: 32},
			{name: "reservations bucket_id", table: "backup_asset_export_reservations", column: "bucket_id", id: strings.Repeat("e", 32), width: 32},
			{name: "reservations job_id", table: "backup_asset_export_reservations", column: "job_id", id: strings.Repeat("e", 32), width: 32},
			{name: "reservations attempt_id", table: "backup_asset_export_reservations", column: "attempt_id", id: strings.Repeat("e", 32), width: 32},
			{name: "archive member key_digest", table: "backup_asset_archive_member_requests", column: "key_digest", id: strings.Repeat("8", 32), width: 64},
			{name: "archive member request_intent_digest", table: "backup_asset_archive_member_requests", column: "request_intent_digest", id: strings.Repeat("8", 32), width: 64},
			{name: "archive member recovery_point_id", table: "backup_asset_archive_member_requests", column: "recovery_point_id", id: strings.Repeat("8", 32), width: 32},
			{name: "archive member entry_id", table: "backup_asset_archive_member_requests", column: "entry_id", id: strings.Repeat("8", 32), width: 64},
			{name: "archive member catalog_generation_id", table: "backup_asset_archive_member_requests", column: "catalog_generation_id", id: strings.Repeat("8", 32), width: 32},
			{name: "archive member index_artifact_id", table: "backup_asset_archive_member_requests", column: "index_artifact_id", id: strings.Repeat("8", 32), width: 32},
			{name: "archive member index_revision", table: "backup_asset_archive_member_requests", column: "index_revision", id: strings.Repeat("8", 32), width: 64},
			{name: "archive member member_chain_digest", table: "backup_asset_archive_member_requests", column: "member_chain_digest", id: strings.Repeat("8", 32), width: 64},
			{name: "archive member processing_interest_id", table: "backup_asset_archive_member_requests", column: "processing_interest_id", id: strings.Repeat("8", 32), width: 32},
			{name: "archive member processing_job_id", table: "backup_asset_archive_member_requests", column: "processing_job_id", id: strings.Repeat("8", 32), width: 32},
			{name: "export grant fence digest", table: "backup_asset_export_delivery_grants", column: "export_fence_digest", id: strings.Repeat("6", 32), width: 64},
			{name: "export grant selection digest", table: "backup_asset_export_delivery_grants", column: "selection_digest", id: strings.Repeat("6", 32), width: 64},
			{name: "export grant artifact digest", table: "backup_asset_export_delivery_grants", column: "artifact_digest", id: strings.Repeat("6", 32), width: 64},
			{name: "member grant outer recovery point", table: "backup_asset_export_delivery_grants", column: "outer_recovery_point_id", id: strings.Repeat("7", 32), width: 32},
			{name: "member grant outer entry", table: "backup_asset_export_delivery_grants", column: "outer_entry_id", id: strings.Repeat("7", 32), width: 64},
			{name: "member grant chain digest", table: "backup_asset_export_delivery_grants", column: "member_chain_digest", id: strings.Repeat("7", 32), width: 64},
			{name: "member grant processing job", table: "backup_asset_export_delivery_grants", column: "processing_job_id", id: strings.Repeat("7", 32), width: 32},
			{name: "member grant processing attempt", table: "backup_asset_export_delivery_grants", column: "processing_attempt_id", id: strings.Repeat("7", 32), width: 32},
			{name: "member grant derived artifact set", table: "backup_asset_export_delivery_grants", column: "derived_artifact_set_id", id: strings.Repeat("7", 32), width: 32},
			{name: "member grant derived artifact", table: "backup_asset_export_delivery_grants", column: "derived_artifact_id", id: strings.Repeat("7", 32), width: 32},
			{name: "member grant derived blob", table: "backup_asset_export_delivery_grants", column: "derived_blob_id", id: strings.Repeat("7", 32), width: 32},
			{name: "member grant derived digest", table: "backup_asset_export_delivery_grants", column: "derived_digest", id: strings.Repeat("7", 32), width: 64},
		} {
			for _, invalidWidth := range []int{testCase.width - 1, testCase.width + 1} {
				t.Run(fmt.Sprintf("%s/%d", testCase.name, invalidWidth), func(t *testing.T) {
					query := fmt.Sprintf("UPDATE %s SET %s = ? WHERE id = ?", testCase.table, testCase.column)
					fixture.expectExecRejectedInRollback(t, db, query, strings.Repeat("a", invalidWidth), testCase.id)
				})
			}
		}
	})
}

func (fixture migrationFixture) seed068CryptographicRows(t *testing.T, db *sql.DB, now time.Time) {
	t.Helper()
	fixture.insertSearchMigrationUser(t, db, 6682, "export-contract-user", now)
	jobID := strings.Repeat("1", 32)
	fixture.mustExec(t, db, `INSERT INTO backup_asset_export_jobs
		(id, owner_user_id, lifecycle_enqueue_sequence, selection_digest, selection_schema_version, archive_format, archive_profile,
		 limits_schema_version, chunk_bytes, max_items, max_source_points, max_item_bytes, max_logical_bytes,
		 max_provider_bytes, max_ciphertext_bytes, max_open_readers, max_duration_seconds, max_attempts,
		 retry_base_seconds, retry_max_delay_seconds, lease_ttl_seconds, lease_renew_margin_seconds,
		 ready_ttl_seconds, execution_state, result_kind, cleanup_state, absolute_deadline,
		 item_count, packed_count, skipped_count, failed_count, transition_revision, created_at, updated_at)
		VALUES (?, 6682, 1, ?, 1, 'zip', 'zip_deflate_v1', 1, 1048576, 10, 2, 1048576, 10485760,
		 10485760, 77604864, 2, 300, 3, 1, 10, 120, 30, 86400, 'queued', '',
		 'none', ?, 1, 0, 0, 0, 1, ?, ?)`,
		jobID, strings.Repeat("a", 64), now.Add(time.Hour), now, now)
	fixture.mustExec(t, db, `INSERT INTO backup_asset_export_keys
		(id, job_id, state, wrapped_dek, envelope_nonce, kek_version, wrap_algorithm,
		 key_revision, created_at)
		VALUES (?, ?, 'active', ?, ?, 1, 'aes-256-gcm', 1, ?)`,
		strings.Repeat("2", 32), jobID, []byte("wrapped-dek"), []byte("123456789012"), now)
	fixture.mustExec(t, db, `INSERT INTO backup_asset_export_items
		(id, job_id, ordinal, recovery_point_id, entry_id, catalog_generation_id,
		 source_fingerprint, entry_fingerprint, fingerprint_strength, provider_capability_revision,
		 entry_type, logical_size, selection_root_ordinal, path_nonce, path_ciphertext, state,
		 created_at, updated_at)
		VALUES (?, ?, 0, ?, ?, ?, 'source-fingerprint', 'entry-fingerprint', 'strong', 1,
		 'file', 10, 0, ?, ?, 'pending', ?, ?)`,
		strings.Repeat("3", 32), jobID, strings.Repeat("4", 32), strings.Repeat("5", 64),
		strings.Repeat("6", 32), []byte("123456789012"), []byte("path-ciphertext"), now, now)
}

func (fixture migrationFixture) seed068DeliveryRows(t *testing.T, db *sql.DB, now time.Time) {
	t.Helper()
	fixture.seed068CryptographicRows(t, db, now)
	jobID := strings.Repeat("1", 32)
	keyID := strings.Repeat("2", 32)
	attemptID := strings.Repeat("4", 32)
	artifactID := strings.Repeat("5", 32)
	fixture.mustExec(t, db, `INSERT INTO backup_asset_export_attempts
		(id, job_id, attempt_number, worker_owner, state, fence_token, fence_digest, nonce_prefix,
		 lease_expires_at, is_current, started_at, finished_at, created_at, updated_at)
		VALUES (?, ?, 1, 'worker', 'sealed', ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		attemptID, jobID, []byte(strings.Repeat("f", 32)), strings.Repeat("b", 64), []byte("12345678"),
		now.Add(time.Hour), false, now, now, now, now)
	fixture.mustExec(t, db, `INSERT INTO backup_asset_export_artifacts
		(id, job_id, attempt_id, job_key_id, state, locator, cipher_version, chunk_bytes,
		 format_version, nonce_prefix, chunk_count, plaintext_digest, archive_digest, ciphertext_digest,
		 plaintext_size, ciphertext_size, sealed_at, expires_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'sealed', 'final.xre', 1, 65536, 1, ?, 1, ?, ?, ?, 10, 20, ?, ?, ?, ?)`,
		artifactID, jobID, attemptID, keyID, []byte("12345678"), strings.Repeat("c", 64),
		strings.Repeat("d", 64), strings.Repeat("e", 64), now, now.Add(time.Hour), now, now)
	memberRequestID := strings.Repeat("8", 32)
	fixture.mustExec(t, db, `INSERT INTO backup_asset_archive_member_requests
		(id, owner_user_id, endpoint, key_digest, request_intent_digest, recovery_point_id, entry_id,
		 catalog_generation_id, source_fingerprint, entry_fingerprint, index_artifact_id, index_revision,
		 member_chain_digest, resolved_ordinal, processing_interest_id, processing_job_id, state,
		 idempotency_expires_at, absolute_expires_at, created_at, updated_at, version)
		VALUES (?, 6682, 'archive_member', ?, ?, ?, ?, ?, 'source-fingerprint', 'entry-fingerprint', ?, ?, ?, 0,
		 ?, ?, 'ready', ?, ?, ?, ?, 1)`, memberRequestID, strings.Repeat("1", 64), strings.Repeat("2", 64),
		strings.Repeat("3", 32), strings.Repeat("4", 64), strings.Repeat("5", 32), strings.Repeat("6", 32),
		strings.Repeat("7", 64), strings.Repeat("9", 64), strings.Repeat("a", 32), strings.Repeat("b", 32),
		now.Add(2*time.Hour), now.Add(time.Hour), now, now)
	fixture.mustExec(t, db, `INSERT INTO backup_asset_export_delivery_grants
		(id, delivery_id, resource_kind, export_job_id, export_artifact_id, export_attempt_id,
		 export_fence_digest, selection_digest, artifact_digest, plaintext_size, ciphertext_size,
		 format_version, chunk_bytes, job_key_id, job_key_version, owner_user_id, session_jti,
		 token_version, role_revision, proof_action, proof_id, proof_expires_at, cookie_secret_hash,
		 action, canonical_path, method_policy, range_policy, state, idle_expires_at, absolute_expires_at,
		 max_requests, max_cumulative_bytes, max_in_flight, issued_at, created_at, updated_at)
		VALUES (?, ?, 'export_archive', ?, ?, ?, ?, ?, ?, 10, 20, 1, 65536, ?, 1, 6682, 'session',
		 1, 1, 'asset.export_download', 'proof', ?, ?, 'export_download', '/api/v1/asset-content/export',
		 'get_head', 'single', 'issued', ?, ?, 10, 100, 1, ?, ?, ?)`,
		strings.Repeat("6", 32), strings.Repeat("c", 32), jobID, artifactID, attemptID,
		strings.Repeat("b", 64), strings.Repeat("a", 64), strings.Repeat("e", 64), keyID,
		now.Add(2*time.Hour), strings.Repeat("f", 64), now.Add(30*time.Minute), now.Add(time.Hour), now, now, now)
	fixture.mustExec(t, db, `INSERT INTO backup_asset_export_delivery_grants
		(id, delivery_id, resource_kind, member_request_id, outer_recovery_point_id, outer_entry_id,
		 outer_source_fingerprint, outer_entry_fingerprint, member_chain_digest, processing_job_id,
		 processing_attempt_id, derived_artifact_set_id, derived_artifact_id, derived_blob_id,
		 derived_digest, derived_size, owner_user_id, session_jti, token_version, role_revision,
		 proof_action, proof_id, proof_expires_at, cookie_secret_hash, action, canonical_path,
		 method_policy, range_policy, state, idle_expires_at, absolute_expires_at, max_requests,
		 max_cumulative_bytes, max_in_flight, issued_at, created_at, updated_at)
		VALUES (?, ?, 'archive_member', ?, ?, ?, 'source-fingerprint', 'entry-fingerprint', ?, ?, ?, ?, ?, ?, ?, 10,
		 6682, 'session', 1, 1, 'asset.download', 'proof', ?, ?, 'archive_member_download',
		 '/api/v1/asset-content/member', 'get_head', 'none', 'issued', ?, ?, 10, 100, 1, ?, ?, ?)`,
		strings.Repeat("7", 32), strings.Repeat("d", 32), memberRequestID, strings.Repeat("3", 32),
		strings.Repeat("4", 64), strings.Repeat("9", 64), strings.Repeat("a", 32), strings.Repeat("b", 32),
		strings.Repeat("c", 32), strings.Repeat("d", 32), strings.Repeat("e", 32),
		strings.Repeat("1", 64), now.Add(2*time.Hour), strings.Repeat("2", 64), now.Add(30*time.Minute),
		now.Add(time.Hour), now, now, now)
}

func (fixture migrationFixture) seed068ParityRows(t *testing.T, db *sql.DB, now time.Time) {
	t.Helper()
	fixture.seed068DeliveryRows(t, db, now)
	jobID := strings.Repeat("1", 32)
	fixture.mustExec(t, db, `INSERT INTO backup_asset_export_source_leases
		(id, job_id, recovery_point_id, lease_id, lease_attempt_id, fence_hash, absolute_deadline,
		 state, acquired_at, renewed_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'active', ?, ?, ?, ?)`,
		strings.Repeat("9", 32), jobID, strings.Repeat("7", 32), strings.Repeat("8", 32),
		strings.Repeat("6", 32), strings.Repeat("a", 64), now.Add(time.Hour), now, now, now, now)
	fixture.mustExec(t, db, `INSERT INTO backup_asset_export_idempotency
		(id, owner_user_id, endpoint, key_digest, request_intent_digest, state, result_job_id,
		 expires_at, created_at, updated_at)
		VALUES (?, 6682, 'asset_export_create', ?, ?, 'committed', ?, ?, ?, ?)`,
		strings.Repeat("a", 32), strings.Repeat("b", 64), strings.Repeat("c", 64), jobID,
		now.Add(time.Hour), now, now)
	userBucketID := strings.Repeat("f", 32)
	fixture.mustExec(t, db, `INSERT INTO backup_asset_export_quota_buckets
		(id, scope, subject, transition_revision, active_jobs, active_workers, active_readers,
		 reserved_store_bytes, used_store_bytes, created_at, updated_at)
		VALUES (?, 'user', '6682', 1, 0, 0, 0, 0, 0, ?, ?)`, userBucketID, now, now)
	fixture.mustExec(t, db, `INSERT INTO backup_asset_export_reservations
		(id, bucket_id, kind, reserved_slots, lease_owner, lease_expires_at, state, created_at, updated_at)
		VALUES (?, ?, 'job', 1, 'reservation-owner', ?, 'released', ?, ?)`,
		strings.Repeat("e", 32), userBucketID, now.Add(time.Hour), now, now)
}

func (fixture migrationFixture) expectExecRejectedInRollback(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin %s rejected-row transaction: %v", fixture.engine, err)
	}
	_, execErr := tx.Exec(fixture.bind(query), args...)
	if rollbackErr := tx.Rollback(); rollbackErr != nil {
		t.Fatalf("rollback %s rejected-row transaction: %v", fixture.engine, rollbackErr)
	}
	if execErr == nil {
		t.Fatalf("%s invalid migration row unexpectedly succeeded", fixture.engine)
	}
}

func (fixture migrationFixture) expectExecAcceptedInRollback(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin %s accepted-row transaction: %v", fixture.engine, err)
	}
	_, execErr := tx.Exec(fixture.bind(query), args...)
	if rollbackErr := tx.Rollback(); rollbackErr != nil {
		t.Fatalf("rollback %s accepted-row transaction: %v", fixture.engine, rollbackErr)
	}
	if execErr != nil {
		t.Fatalf("%s valid migration row was rejected: %v", fixture.engine, execErr)
	}
}

func (fixture migrationFixture) expectExecRejectedWithoutRecoveryContentBindingTriggerInRollback(
	t *testing.T,
	db *sql.DB,
	query string,
	args ...any,
) {
	t.Helper()
	if err := fixture.execWithoutRecoveryContentBindingTriggerInRollback(t, db, query, args...); err == nil {
		t.Fatalf("%s invalid RecoveryResult Content row unexpectedly succeeded without the binding trigger", fixture.engine)
	}
}

func (fixture migrationFixture) expectExecAcceptedWithoutRecoveryContentBindingTriggerInRollback(
	t *testing.T,
	db *sql.DB,
	query string,
	args ...any,
) {
	t.Helper()
	if err := fixture.execWithoutRecoveryContentBindingTriggerInRollback(t, db, query, args...); err != nil {
		t.Fatalf("%s valid RecoveryResult Content row was rejected without the binding trigger: %v", fixture.engine, err)
	}
}

func (fixture migrationFixture) execWithoutRecoveryContentBindingTriggerInRollback(
	t *testing.T,
	db *sql.DB,
	query string,
	args ...any,
) error {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin %s RecoveryResult Content constraint probe: %v", fixture.engine, err)
	}
	dropTrigger := "DROP TRIGGER trg_backup_asset_recovery_content_binding_immutable"
	if fixture.engine == "postgres" {
		dropTrigger += " ON backup_asset_delivery_grants"
	}
	if _, err := tx.Exec(dropTrigger); err != nil {
		_ = tx.Rollback()
		t.Fatalf("drop %s RecoveryResult Content binding trigger in probe: %v", fixture.engine, err)
	}
	_, execErr := tx.Exec(fixture.bind(query), args...)
	if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
		t.Fatalf("rollback %s RecoveryResult Content constraint probe: %v", fixture.engine, rollbackErr)
	}
	return execErr
}

func (fixture migrationFixture) executeExportDown(db *sql.DB) error {
	path := "migrations/sqlite/000068_backup_asset_export.down.sql"
	migrationFS := sqliteMigrationsFS
	if fixture.engine == "postgres" {
		path = "migrations/postgres/000068_backup_asset_export.down.sql"
		migrationFS = postgresMigrationsFS
	}
	script, err := migrationFS.ReadFile(path)
	if err != nil {
		return err
	}
	_, err = db.Exec(string(script))
	if err != nil {
		_, _ = db.Exec("ROLLBACK")
	}
	return err
}

func backupAssetExportModels() map[string]any {
	return map[string]any{
		"backup_asset_export_jobs":              model.BackupAssetExportJob{},
		"backup_asset_export_keys":              model.BackupAssetExportKey{},
		"backup_asset_export_items":             model.BackupAssetExportItem{},
		"backup_asset_export_attempts":          model.BackupAssetExportAttempt{},
		"backup_asset_export_item_attempts":     model.BackupAssetExportItemAttempt{},
		"backup_asset_export_source_leases":     model.BackupAssetExportSourceLease{},
		"backup_asset_export_artifacts":         model.BackupAssetExportArtifact{},
		"backup_asset_export_idempotency":       model.BackupAssetExportIdempotency{},
		"backup_asset_export_quota_buckets":     model.BackupAssetExportQuotaBucket{},
		"backup_asset_export_reservations":      model.BackupAssetExportReservation{},
		"backup_asset_export_delivery_grants":   model.BackupAssetExportDeliveryGrant{},
		"backup_asset_export_delivery_requests": model.BackupAssetExportDeliveryRequest{},
		"backup_asset_archive_member_requests":  model.BackupAssetArchiveMemberRequest{},
	}
}

func (fixture migrationFixture) test067PristineDown(t *testing.T) {
	migrator, db := fixture.openAt(t, backupAssetProcessingVersion)
	if err := migrator.Steps(-1); err != nil {
		t.Fatalf("step %s migration down to 000066: %v", fixture.engine, err)
	}
	assertMigrationVersion(t, migrator, backupAssetContentVersion)
	for _, table := range backupAssetProcessingTables {
		if databaseTableExists(t, db, fixture.engine, table) {
			t.Fatalf("%s processing table %s remains after pristine down", fixture.engine, table)
		}
	}
	if definition := fixture.tableDefinition(t, db, "wrapped_domain_keys"); strings.Contains(definition, "derived_store") {
		t.Fatalf("%s wrapped key CHECK still permits derived_store after down: %s", fixture.engine, definition)
	}
	if !databaseTableExists(t, db, fixture.engine, "backup_asset_delivery_grants") {
		t.Fatalf("%s 000066 content schema was removed by 000067 down", fixture.engine)
	}
}

func (fixture migrationFixture) test067RejectsInvalidStateErrorProducts(t *testing.T) {
	t.Run("JobErrorProduct", func(t *testing.T) {
		_, db := fixture.openAt(t, backupAssetProcessingVersion)
		now := time.Date(2026, 7, 19, 8, 9, 10, 0, time.UTC)
		pointID, catalogID, entryID := fixture.insertSearchMigrationCatalog(t, db, "7", now)
		jobID := strings.Repeat("8", 32)
		fixture.insertProcessingMigrationJob(t, db, jobID, pointID, catalogID, entryID, now)

		fixture.expectExecRejected(t, db,
			`UPDATE backup_asset_processing_jobs SET error_code = 'invalid_output' WHERE id = ?`, jobID)
		fixture.expectExecRejected(t, db,
			`UPDATE backup_asset_processing_jobs
			 SET state = 'failed', is_current = ?, finished_at = ?, error_code = '' WHERE id = ?`,
			false, now.Add(time.Minute), jobID)
	})

	t.Run("AttemptOutcomeProduct", func(t *testing.T) {
		_, db := fixture.openAt(t, backupAssetProcessingVersion)
		now := time.Date(2026, 7, 19, 8, 9, 11, 0, time.UTC)
		pointID, catalogID, entryID := fixture.insertSearchMigrationCatalog(t, db, "9", now)
		jobID := strings.Repeat("a", 32)
		workerID := strings.Repeat("b", 32)
		leaseID := strings.Repeat("c", 32)
		attemptID := strings.Repeat("d", 32)
		fixture.insertProcessingMigrationJob(t, db, jobID, pointID, catalogID, entryID, now)
		fixture.insertProcessingMigrationWorker(t, db, workerID, now)
		fixture.insertSearchMigrationLease(t, db, leaseID, pointID, "processing_job", now)
		fixture.insertProcessingMigrationAttempt(t, db, attemptID, jobID, workerID, leaseID, now)

		fixture.expectExecRejected(t, db,
			`UPDATE backup_asset_processing_attempts
			 SET state = 'succeeded', is_current = ?, finished_at = ?, outcome_code = 'invalid_output'
			 WHERE id = ?`, false, now.Add(time.Minute), attemptID)
		fixture.expectExecRejected(t, db,
			`UPDATE backup_asset_processing_attempts
			 SET state = 'failed', is_current = ?, finished_at = ?, outcome_code = ''
			 WHERE id = ?`, false, now.Add(time.Minute), attemptID)

		fixture.mustExec(t, db,
			`UPDATE backup_asset_processing_attempts
			 SET state = 'expired', is_current = ?, finished_at = ?, outcome_code = 'lease_lost'
			 WHERE id = ?`, false, now.Add(time.Minute), attemptID)
	})

	t.Run("DerivedSetRevocationProduct", func(t *testing.T) {
		_, db := fixture.openAt(t, backupAssetProcessingVersion)
		now := time.Date(2026, 7, 19, 8, 9, 12, 0, time.UTC)
		pointID, catalogID, entryID := fixture.insertSearchMigrationCatalog(t, db, "c", now)
		jobID := strings.Repeat("e", 32)
		workerID := strings.Repeat("f", 32)
		leaseID := strings.Repeat("1", 32)
		attemptID := strings.Repeat("2", 32)
		setID := strings.Repeat("3", 32)
		fixture.insertProcessingMigrationJob(t, db, jobID, pointID, catalogID, entryID, now)
		fixture.insertProcessingMigrationWorker(t, db, workerID, now)
		fixture.insertSearchMigrationLease(t, db, leaseID, pointID, "processing_job", now)
		fixture.insertProcessingMigrationAttempt(t, db, attemptID, jobID, workerID, leaseID, now)
		fixture.mustExec(t, db, `INSERT INTO backup_asset_derived_artifact_sets
			(id, job_id, attempt_id, work_key, recovery_point_id, catalog_generation_id, entry_id,
				 source_fingerprint, security_policy_revision, manifest_digest, state, revocation_reason,
				 completeness, artifact_count, total_plaintext_bytes, projection_required,
				 projection_published, projection_revision, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, 'source-v1', 'policy-v1', ?, 'active', '',
				 'complete', 1, 0, ?, ?, 0, ?, ?)`,
			setID, jobID, attemptID, strings.Repeat("4", 64), pointID, catalogID, entryID,
			strings.Repeat("5", 64), false, false, now, now)

		fixture.expectExecRejected(t, db,
			`UPDATE backup_asset_derived_artifact_sets
			 SET state = 'superseded', revoked_at = ?, revocation_reason = '' WHERE id = ?`, now, setID)
		fixture.expectExecRejected(t, db,
			`UPDATE backup_asset_derived_artifact_sets
			 SET state = 'superseded', revoked_at = ?, revocation_reason = 'not-a-reason' WHERE id = ?`, now, setID)
	})
}

func (fixture migrationFixture) test067UsedDownIsAtomic(t *testing.T) {
	testCases := []struct {
		name string
		seed func(*testing.T, *sql.DB, time.Time)
	}{
		{
			name: "ProcessingJob",
			seed: func(t *testing.T, db *sql.DB, now time.Time) {
				pointID, catalogID, entryID := fixture.insertSearchMigrationCatalog(t, db, "1", now)
				fixture.insertProcessingMigrationJob(t, db, strings.Repeat("2", 32), pointID, catalogID, entryID, now)
			},
		},
		{
			name: "WorkerIdentity",
			seed: func(t *testing.T, db *sql.DB, now time.Time) {
				fixture.insertProcessingMigrationWorker(t, db, strings.Repeat("3", 32), now)
			},
		},
		{
			name: "DerivedBlob",
			seed: func(t *testing.T, db *sql.DB, now time.Time) {
				fixture.mustExec(t, db, `INSERT INTO backup_asset_derived_blobs
					(id, plaintext_digest, plaintext_size, physical_size, cipher_format_version,
					 chunk_size, chunk_count, nonce_prefix, opaque_locator, wrapped_dek,
					 envelope_nonce, derived_kek_version, state, ref_count, created_at, updated_at)
					VALUES (?, ?, 1, 32, 1, 65536, 1, ?, 'blob-067-test', ?, ?, 1,
					 'staged', 0, ?, ?)`, strings.Repeat("4", 32), strings.Repeat("5", 64),
					[]byte("12345678"), []byte("wrapped-dek"), []byte("123456789012"), now, now)
			},
		},
		{
			name: "UpdaterMetadata",
			seed: func(t *testing.T, db *sql.DB, now time.Time) {
				fixture.mustExec(t, db, `INSERT INTO backup_asset_updater_metadata
					(id, source_kind, source_id, version, manifest_digest, signing_key_fingerprint,
					 bundle_fingerprint, state, failure_code, created_at, updated_at)
					VALUES (?, 'builtin', 'noop-fixture', '1.0.0', ?, ?, ?, 'registered', '', ?, ?)`,
					strings.Repeat("6", 32), strings.Repeat("7", 64), strings.Repeat("8", 64),
					strings.Repeat("9", 64), now, now)
			},
		},
		{
			name: "ProcessingRecoveryPointLease",
			seed: func(t *testing.T, db *sql.DB, now time.Time) {
				pointID, _, _ := fixture.insertSearchMigrationCatalog(t, db, "a", now)
				fixture.insertSearchMigrationLease(t, db, strings.Repeat("b", 32), pointID, "processing_job", now)
			},
		},
		{
			name: "DerivedDomainKey",
			seed: func(t *testing.T, db *sql.DB, now time.Time) {
				fixture.mustExec(t, db, `INSERT INTO wrapped_domain_keys
					(id, domain, version, state, wrapped_key, wrap_algorithm,
					 wrapping_key_fingerprint, activated_at, created_at, updated_at)
					VALUES (?, 'derived_store', 1, 'active', 'wrapped-key', 'aes-256-gcm', ?, ?, ?, ?)`,
					"derived-key-067-test", strings.Repeat("c", 64), now, now, now)
			},
		},
	}

	for index, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			migrator, db := fixture.openAt(t, backupAssetProcessingVersion)
			now := time.Date(2026, 7, 19, 10, 11, index+1, 0, time.UTC)
			testCase.seed(t, db, now)
			fixture.assertProcessingDownRejectedUnchanged(t, migrator, db)
		})
	}
}

func (fixture migrationFixture) test067DownRejectsUnrevokedSearchProjection(t *testing.T) {
	testCases := []struct {
		name string
		seed func(*testing.T, *sql.DB, string, string, time.Time)
	}{
		{
			name: "ContentPostingWithoutExcerpt",
			seed: func(t *testing.T, db *sql.DB, searchID, documentID string, _ time.Time) {
				fixture.mustExec(t, db, `INSERT INTO backup_asset_search_postings
					(search_generation_id, document_id, field, token_kind, key_version, token_hmac, term_frequency)
					VALUES (?, ?, 'content', 'exact', 1, ?, 1)`,
					searchID, documentID, strings.Repeat("d", 64))
			},
		},
		{
			name: "OCRCoverageStillAvailable",
			seed: func(t *testing.T, db *sql.DB, searchID, documentID string, now time.Time) {
				fixture.mustExec(t, db, `INSERT INTO backup_asset_search_document_fields
					(search_generation_id, document_id, field, state, coverage_revision,
					 classification_revision, pipeline_revision, index_revision, source_fingerprint,
					 excerpt_ref, updated_at)
					VALUES (?, ?, 'ocr', 'complete', 1, 1, 1, 1, 'source-v1', NULL, ?)`,
					searchID, documentID, now)
			},
		},
		{
			name: "ContentExcerptStillReferenced",
			seed: func(t *testing.T, db *sql.DB, searchID, documentID string, now time.Time) {
				fixture.mustExec(t, db, `INSERT INTO backup_asset_search_document_fields
					(search_generation_id, document_id, field, state, coverage_revision,
					 classification_revision, pipeline_revision, index_revision, source_fingerprint,
					 excerpt_ref, updated_at)
					VALUES (?, ?, 'content', 'unavailable', 1, 1, 1, 1, 'source-v1', ?, ?)`,
					searchID, documentID, strings.Repeat("f", 32), now)
			},
		},
	}

	for index, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			migrator, db := fixture.openAt(t, backupAssetProcessingVersion)
			now := time.Date(2026, 7, 19, 9, 10, index+1, 0, time.UTC)
			marker := strconv.Itoa(index + 1)
			pointID, catalogID, entryID := fixture.insertSearchMigrationCatalog(t, db, marker, now)
			searchID := strings.Repeat(string(rune('a'+index)), 32)
			documentID := strings.Repeat(string(rune('e'+index)), 64)
			fixture.insertSearchMigrationGeneration(t, db, searchID, pointID, catalogID, 1, now)
			fixture.insertSearchMigrationDocument(t, db, searchID, pointID, catalogID, entryID, documentID, now)
			testCase.seed(t, db, searchID, documentID, now)

			fixture.assertProcessingDownRejectedUnchanged(t, migrator, db)
		})
	}
}

func (fixture migrationFixture) insertProcessingMigrationJob(
	t *testing.T,
	db *sql.DB,
	jobID, pointID, catalogID, entryID string,
	now time.Time,
) {
	t.Helper()
	fixture.mustExec(t, db, `INSERT INTO backup_asset_processing_jobs
		(id, work_key, descriptor_schema_version, descriptor_canonical,
		 recovery_point_id, catalog_generation_id, entry_id, source_fingerprint,
		 entry_fingerprint, provider_capability_revision, capability, capability_schema,
		 pipeline_fingerprint, output_profile, security_policy_revision, priority_class,
		 effective_priority, state, transition_revision, error_code, retry_count,
		 cancel_reason, supersede_reason, expiry_reason, is_current, queued_at,
		 absolute_deadline, created_at, updated_at, version)
		VALUES (?, ?, 1, ?, ?, ?, ?, 'source-v1', 'entry-v1', 1, 'noop', 'noop-v1',
		 'pipeline-v1', 'noop-v1', 'security-policy-v1', 'interactive', 100,
		 'queued', 1, '', 0, '', '', '', ?, ?, ?, ?, ?, 1)`,
		jobID, strings.Repeat("c", 64), []byte{1}, pointID, catalogID, entryID,
		true, now, now.Add(time.Hour), now, now)
}

func (fixture migrationFixture) insertProcessingMigrationWorker(t *testing.T, db *sql.DB, workerID string, now time.Time) {
	t.Helper()
	fixture.mustExec(t, db, `INSERT INTO backup_asset_worker_identities
		(id, transport_kind, transport_fingerprint, instance_id, identity_revision,
		 protocol_version, trust_state, health_state, interactive_slots, background_slots,
		 quarantine_code, last_seen_at, created_at, updated_at)
		VALUES (?, 'local', ?, ?, 1, 1, 'active', 'ready', 1, 1, '', ?, ?, ?)`,
		workerID, strings.Repeat("e", 64), strings.Repeat("f", 32), now, now, now)
}

func (fixture migrationFixture) insertProcessingMigrationAttempt(
	t *testing.T,
	db *sql.DB,
	attemptID, jobID, workerID, leaseID string,
	now time.Time,
) {
	t.Helper()
	fixture.mustExec(t, db, `INSERT INTO backup_asset_processing_attempts
		(id, job_id, attempt_number, worker_id, slot_class, state,
		 worker_lease_expires_at, last_heartbeat_at, recovery_point_lease_id,
		 recovery_point_attempt_id, recovery_point_fence_hash, absolute_deadline,
		 outcome_code, is_current, started_at, created_at, updated_at)
		VALUES (?, ?, 1, ?, 'interactive', 'active', ?, ?, ?, ?, ?, ?, '', ?, ?, ?, ?)`,
		attemptID, jobID, workerID, now.Add(time.Minute), now, leaseID,
		strings.Repeat("1", 32), strings.Repeat("2", 64), now.Add(time.Hour), true, now, now, now)
}

type processingMigrationSnapshot struct {
	version     uint
	dirty       bool
	tables      map[string]bool
	definitions map[string]string
	indexes     map[string]string
	rowCounts   map[string]int
}

func (fixture migrationFixture) captureProcessingMigrationSnapshot(
	t *testing.T,
	migrator *migrate.Migrate,
	db *sql.DB,
) processingMigrationSnapshot {
	t.Helper()
	version, dirty, err := migrator.Version()
	if err != nil {
		t.Fatalf("read %s processing migration version: %v", fixture.engine, err)
	}
	tables := append([]string{
		"wrapped_domain_keys",
		"recovery_point_leases",
		"backup_asset_search_postings",
		"backup_asset_search_document_fields",
	}, backupAssetProcessingTables...)
	snapshot := processingMigrationSnapshot{
		version:     version,
		dirty:       dirty,
		tables:      make(map[string]bool, len(tables)),
		definitions: make(map[string]string, len(tables)),
		indexes: map[string]string{
			"idx_wrapped_domain_keys_active":                fixture.indexDefinition(t, db, "idx_wrapped_domain_keys_active"),
			"idx_backup_asset_processing_jobs_current_work": fixture.indexDefinition(t, db, "idx_backup_asset_processing_jobs_current_work"),
			"idx_backup_asset_derived_refs_blob_state":      fixture.indexDefinition(t, db, "idx_backup_asset_derived_refs_blob_state"),
		},
		rowCounts: make(map[string]int, len(tables)),
	}
	for _, table := range tables {
		exists := databaseTableExists(t, db, fixture.engine, table)
		snapshot.tables[table] = exists
		if !exists {
			continue
		}
		snapshot.definitions[table] = fixture.tableDefinition(t, db, table)
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatalf("count %s rows in %s: %v", fixture.engine, table, err)
		}
		snapshot.rowCounts[table] = count
	}
	return snapshot
}

func (fixture migrationFixture) executeProcessingDown(db *sql.DB) error {
	path := "migrations/sqlite/000067_backup_asset_processing.down.sql"
	migrationFS := sqliteMigrationsFS
	if fixture.engine == "postgres" {
		path = "migrations/postgres/000067_backup_asset_processing.down.sql"
		migrationFS = postgresMigrationsFS
	}
	script, err := migrationFS.ReadFile(path)
	if err != nil {
		return err
	}
	_, err = db.Exec(string(script))
	if err != nil {
		_, _ = db.Exec("ROLLBACK")
	}
	return err
}

func (fixture migrationFixture) assertProcessingDownRejectedUnchanged(
	t *testing.T,
	migrator *migrate.Migrate,
	db *sql.DB,
) {
	t.Helper()
	before := fixture.captureProcessingMigrationSnapshot(t, migrator, db)
	if err := fixture.executeProcessingDown(db); err == nil {
		t.Fatal("000067 down unexpectedly succeeded while Child 10 state remains")
	}
	after := fixture.captureProcessingMigrationSnapshot(t, migrator, db)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("rejected 000067 down changed migration state\nbefore=%+v\nafter=%+v", before, after)
	}
}

func backupAssetContentModels() map[string]any {
	return map[string]any{
		"backup_asset_delivery_grants":   model.BackupAssetDeliveryGrant{},
		"backup_asset_delivery_requests": model.BackupAssetDeliveryRequest{},
		"backup_asset_delivery_usage":    model.BackupAssetDeliveryUsage{},
	}
}

func (fixture migrationFixture) test066PristineDown(t *testing.T) {
	migrator, db := fixture.openAt(t, backupAssetContentVersion)
	if err := migrator.Steps(-1); err != nil {
		t.Fatalf("step %s migration down to 000065: %v", fixture.engine, err)
	}
	assertMigrationVersion(t, migrator, backupAssetSearchVersion)
	for _, table := range backupAssetContentTables {
		if databaseTableExists(t, db, fixture.engine, table) {
			t.Fatalf("%s content table %s remains after pristine down", fixture.engine, table)
		}
	}
	if definition := fixture.indexDefinition(t, db, "idx_backup_asset_audit_events_content_grant_action"); definition != "" {
		t.Fatalf("%s content audit index remains after down: %s", fixture.engine, definition)
	}
	if !databaseTableExists(t, db, fixture.engine, "backup_asset_search_generations") {
		t.Fatalf("%s 000065 search schema was removed by 000066 down", fixture.engine)
	}
}

func (fixture migrationFixture) test066ValidAndInvalidRows(t *testing.T) {
	_, db := fixture.openAt(t, backupAssetContentVersion)
	now := time.Date(2026, 7, 18, 4, 5, 6, 0, time.UTC)
	pointA, catalogA, entryA := fixture.insertSearchMigrationCatalog(t, db, "4", now)
	pointB, _, _ := fixture.insertSearchMigrationCatalog(t, db, "5", now)
	fixture.insertSearchMigrationUser(t, db, 6601, "content-user", now)
	leaseID := strings.Repeat("6", 32)
	fixture.insertSearchMigrationLease(t, db, leaseID, pointA, "catalog_build", now)
	grantID := strings.Repeat("7", 32)
	fixture.insertContentMigrationGrant(t, db, 6601, grantID, strings.Repeat("8", 32), pointA, catalogA, entryA, leaseID, now)
	fixture.insertContentMigrationRequest(t, db, strings.Repeat("9", 32), grantID, now)
	fixture.mustExec(t, db, `INSERT INTO backup_asset_delivery_usage
		(scope_kind, scope_id, window_started_at, window_expires_at, request_count,
		 reserved_bytes, delivered_bytes, in_flight, version, updated_at)
		VALUES ('global', 'global', ?, ?, 1, 0, 0, 0, 1, ?)`, now, now.Add(time.Minute), now)

	fixture.expectExecRejected(t, db, `UPDATE backup_asset_delivery_grants SET resource_kind = 'recovery_result',
		recovery_point_id = NULL, catalog_generation_id = NULL, entry_id = NULL,
		recovery_job_id = ?, recovery_result_id = ? WHERE id = ?`, strings.Repeat("a", 32), strings.Repeat("b", 32), grantID)
	fixture.expectExecRejected(t, db, `UPDATE backup_asset_delivery_grants SET recovery_job_id = ? WHERE id = ?`, strings.Repeat("a", 32), grantID)
	fixture.expectExecRejected(t, db, `UPDATE backup_asset_delivery_grants SET resource_kind = 'future' WHERE id = ?`, grantID)
	fixture.expectExecRejected(t, db, `UPDATE backup_asset_delivery_grants SET recovery_point_id = ? WHERE id = ?`, pointB, grantID)
	fixture.expectExecRejected(t, db, `UPDATE backup_asset_delivery_grants SET step_up_action = 'asset.download', step_up_proof_id = ?, step_up_expires_at = ? WHERE id = ?`, strings.Repeat("c", 32), now.Add(time.Minute), grantID)
	fixture.expectExecRejected(t, db, `UPDATE backup_asset_delivery_grants SET classification = 'secret', step_up_action = NULL, step_up_proof_id = ?, step_up_expires_at = ? WHERE id = ?`, strings.Repeat("c", 32), now.Add(10*time.Minute), grantID)
	fixture.expectExecRejected(t, db, `UPDATE backup_asset_delivery_grants SET action = 'download', renderer = 'attachment', profile = 'original_v1', step_up_action = NULL, step_up_proof_id = ?, step_up_expires_at = ? WHERE id = ?`, strings.Repeat("c", 32), now.Add(10*time.Minute), grantID)
	fixture.expectExecRejected(t, db, `UPDATE backup_asset_delivery_grants SET renderer = 'native_video' WHERE id = ?`, grantID)
	fixture.expectExecRejected(t, db, `UPDATE backup_asset_delivery_grants SET representation_truncated = ? WHERE id = ?`, true, grantID)
	fixture.expectExecRejected(t, db, `UPDATE backup_asset_delivery_grants SET representation_source_bytes = 0, representation_truncated = ? WHERE id = ?`, false, grantID)
	fixture.expectExecRejected(t, db, `UPDATE backup_asset_delivery_grants SET renderer = 'safe_raster', profile = 'raster_v1', representation_size = 2 WHERE id = ?`, grantID)
	fixture.expectExecRejected(t, db, `UPDATE backup_asset_delivery_grants SET audit_request_count = 1 WHERE id = ?`, grantID)
	fixture.expectExecRejected(t, db, `UPDATE backup_asset_delivery_grants SET audit_state = 'pending' WHERE id = ?`, grantID)
	fixture.expectExecRejected(t, db, `UPDATE backup_asset_delivery_grants SET audit_state = 'pending',
		audit_request_count = 1, audit_success_count = 1 WHERE id = ?`, grantID)
	fixture.expectExecRejected(t, db, `UPDATE backup_asset_delivery_grants SET audit_state = 'retry_wait',
		audit_request_count = 1, audit_failure_count = 1, audit_failure_code = 'audit_write_failed',
		audit_attempt_count = 1 WHERE id = ?`, grantID)
	fixture.expectExecRejected(t, db, `UPDATE backup_asset_delivery_grants SET request_count = 1,
		delivered_bytes = 1, audit_state = 'pending', audit_request_count = 1,
		audit_success_count = 1, audit_range_bytes = 1 WHERE id = ?`, grantID)
	fixture.expectExecRejected(t, db, `UPDATE backup_asset_delivery_grants SET request_count = 1,
		audit_state = 'pending', audit_request_count = 1, audit_success_count = 1,
		audit_range_count = 1, audit_range_bytes = 1 WHERE id = ?`, grantID)
	fixture.expectExecRejected(t, db, `UPDATE backup_asset_delivery_grants SET cookie_secret_hash = 'ABC' WHERE id = ?`, grantID)
	fixture.expectExecRejected(t, db, `UPDATE backup_asset_delivery_grants SET lease_attempt_id = 'ABC' WHERE id = ?`, grantID)
	fixture.expectExecRejected(t, db, `UPDATE backup_asset_delivery_grants SET idle_expires_at = ? WHERE id = ?`, now.Add(20*time.Minute), grantID)
	fixture.expectExecRejected(t, db, `UPDATE backup_asset_delivery_grants SET reserved_bytes = max_cumulative_bytes + 1 WHERE id = ?`, grantID)
	fixture.expectExecRejected(t, db, `UPDATE backup_asset_delivery_requests SET range_kind = 'normal' WHERE id = ?`, strings.Repeat("9", 32))
	fixture.expectExecRejected(t, db, `INSERT INTO backup_asset_delivery_usage
		(scope_kind, scope_id, window_started_at, window_expires_at, request_count,
		 reserved_bytes, delivered_bytes, in_flight, version, updated_at)
		VALUES ('user', '01', ?, ?, 0, 0, 0, 0, 1, ?)`, now, now.Add(time.Minute), now)
	fixture.expectExecRejected(t, db, `INSERT INTO backup_asset_delivery_usage
		(scope_kind, scope_id, window_started_at, window_expires_at, request_count,
		 reserved_bytes, delivered_bytes, in_flight, version, updated_at)
		VALUES ('provider', 'command', ?, ?, 0, 0, 0, 0, 1, ?)`, now, now.Add(time.Minute), now)
	fixture.mustExec(t, db, `UPDATE backup_asset_delivery_grants SET request_count = 1, audit_state = 'pending',
		audit_request_count = 1, audit_success_count = 1 WHERE id = ?`, grantID)
	fixture.mustExec(t, db, `UPDATE backup_asset_delivery_grants SET audit_state = 'retry_wait',
		audit_failure_code = 'audit_write_failed', audit_attempt_count = 1,
		audit_next_attempt_at = ? WHERE id = ?`, now.Add(time.Minute), grantID)
	fixture.mustExec(t, db, `UPDATE backup_asset_delivery_grants SET audit_state = 'emitted',
		audit_failure_code = '', audit_next_attempt_at = NULL WHERE id = ?`, grantID)
}

func (fixture migrationFixture) test066UsedDownIsAtomic(t *testing.T) {
	testCases := []struct {
		name string
		seed func(*testing.T, *sql.DB)
	}{
		{name: "Grant", seed: func(t *testing.T, db *sql.DB) {
			now := time.Date(2026, 7, 18, 5, 6, 7, 0, time.UTC)
			pointID, catalogID, entryID := fixture.insertSearchMigrationCatalog(t, db, "a", now)
			fixture.insertSearchMigrationUser(t, db, 6610, "grant-user", now)
			leaseID := strings.Repeat("b", 32)
			fixture.insertSearchMigrationLease(t, db, leaseID, pointID, "catalog_build", now)
			fixture.insertContentMigrationGrant(t, db, 6610, strings.Repeat("c", 32), strings.Repeat("d", 32), pointID, catalogID, entryID, leaseID, now)
		}},
		{name: "Request", seed: func(t *testing.T, db *sql.DB) {
			now := time.Date(2026, 7, 18, 5, 6, 7, 0, time.UTC)
			pointID, catalogID, entryID := fixture.insertSearchMigrationCatalog(t, db, "e", now)
			fixture.insertSearchMigrationUser(t, db, 6611, "request-user", now)
			leaseID := strings.Repeat("f", 32)
			fixture.insertSearchMigrationLease(t, db, leaseID, pointID, "catalog_build", now)
			grantID := strings.Repeat("1", 32)
			fixture.insertContentMigrationGrant(t, db, 6611, grantID, strings.Repeat("2", 32), pointID, catalogID, entryID, leaseID, now)
			fixture.insertContentMigrationRequest(t, db, strings.Repeat("3", 32), grantID, now)
		}},
		{name: "Usage", seed: func(t *testing.T, db *sql.DB) {
			now := time.Date(2026, 7, 18, 5, 6, 7, 0, time.UTC)
			fixture.mustExec(t, db, `INSERT INTO backup_asset_delivery_usage
				(scope_kind, scope_id, window_started_at, window_expires_at, request_count,
				 reserved_bytes, delivered_bytes, in_flight, version, updated_at)
				VALUES ('provider', 'rsync', ?, ?, 0, 0, 0, 0, 1, ?)`, now, now.Add(time.Minute), now)
		}},
		{name: "ContentLease", seed: func(t *testing.T, db *sql.DB) {
			now := time.Date(2026, 7, 18, 5, 6, 7, 0, time.UTC)
			pointID, _, _ := fixture.insertSearchMigrationCatalog(t, db, "4", now)
			fixture.insertSearchMigrationLease(t, db, strings.Repeat("5", 32), pointID, "content_session", now)
		}},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			migrator, db := fixture.openAt(t, backupAssetContentVersion)
			testCase.seed(t, db)
			before := fixture.captureContentMigrationSnapshot(t, migrator, db)
			if err := fixture.executeContentDown(db); err == nil {
				t.Fatal("000066 down unexpectedly succeeded while Child 8 state remains")
			}
			after := fixture.captureContentMigrationSnapshot(t, migrator, db)
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("rejected 000066 down changed migration state\nbefore=%+v\nafter=%+v", before, after)
			}
		})
	}
}

func (fixture migrationFixture) test066SafeDrain(t *testing.T) {
	migrator, db := fixture.openAt(t, backupAssetContentVersion)
	now := time.Date(2026, 7, 18, 6, 7, 8, 0, time.UTC)
	pointID, catalogID, entryID := fixture.insertSearchMigrationCatalog(t, db, "6", now)
	fixture.insertSearchMigrationUser(t, db, 6620, "drain-user", now)
	leaseID := strings.Repeat("7", 32)
	fixture.insertSearchMigrationLease(t, db, leaseID, pointID, "content_session", now)
	grantID := strings.Repeat("8", 32)
	fixture.insertContentMigrationGrant(t, db, 6620, grantID, strings.Repeat("9", 32), pointID, catalogID, entryID, leaseID, now)
	fixture.insertContentMigrationRequest(t, db, strings.Repeat("a", 32), grantID, now)
	fixture.mustExec(t, db, `INSERT INTO backup_asset_delivery_usage
		(scope_kind, scope_id, window_started_at, window_expires_at, request_count,
		 reserved_bytes, delivered_bytes, in_flight, version, updated_at)
		VALUES ('user', '6620', ?, ?, 1, 0, 0, 0, 1, ?)`, now, now.Add(time.Minute), now)

	fixture.mustExec(t, db, `DELETE FROM backup_asset_delivery_requests`)
	fixture.mustExec(t, db, `DELETE FROM backup_asset_delivery_grants`)
	fixture.mustExec(t, db, `DELETE FROM backup_asset_delivery_usage`)
	fixture.mustExec(t, db, `DELETE FROM recovery_point_leases WHERE holder_type = 'content_session'`)
	if err := migrator.Steps(-1); err != nil {
		t.Fatalf("step %s drained 000066 down: %v", fixture.engine, err)
	}
	assertMigrationVersion(t, migrator, backupAssetSearchVersion)
}

func (fixture migrationFixture) test066Preserves065Defense(t *testing.T) {
	migrator, db := fixture.openAt(t, backupAssetSearchVersion)
	now := time.Date(2026, 7, 18, 7, 8, 9, 0, time.UTC)
	pointID, catalogID, _ := fixture.insertSearchMigrationCatalog(t, db, "b", now)
	fixture.insertSearchMigrationGeneration(t, db, strings.Repeat("c", 32), pointID, catalogID, 1, now)
	migrateToBackupAssetVersion(t, migrator, backupAssetContentVersion)
	if err := migrator.Steps(-1); err != nil {
		t.Fatalf("step %s 000066 down with Child 7 state: %v", fixture.engine, err)
	}
	assertMigrationVersion(t, migrator, backupAssetSearchVersion)
	before := fixture.captureSearchMigrationSnapshot(t, migrator, db)
	if err := fixture.executeSearchDown(db); err == nil {
		t.Fatal("000065 down unexpectedly succeeded while Child 7 state remains after 000066")
	}
	after := fixture.captureSearchMigrationSnapshot(t, migrator, db)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("rejected 000065 down changed state after 000066\nbefore=%+v\nafter=%+v", before, after)
	}
}

func (fixture migrationFixture) test065PristineDown(t *testing.T) {
	migrator, db := fixture.openAt(t, backupAssetSearchVersion)
	if err := migrator.Steps(-1); err != nil {
		t.Fatalf("step %s migration down to 000064: %v", fixture.engine, err)
	}
	assertMigrationVersion(t, migrator, backupAssetRsyncPublicationVersion)
	for _, table := range backupAssetSearchTables {
		if databaseTableExists(t, db, fixture.engine, table) {
			t.Fatalf("%s search table %s remains after pristine down", fixture.engine, table)
		}
	}
	if definition := fixture.tableDefinition(t, db, "wrapped_domain_keys"); strings.Contains(definition, "search_token") {
		t.Fatalf("%s wrapped key CHECK still permits search_token after down: %s", fixture.engine, definition)
	}
	if definition := fixture.tableDefinition(t, db, "recovery_point_leases"); strings.Contains(definition, "search_index") {
		t.Fatalf("%s lease CHECK still permits search_index after down: %s", fixture.engine, definition)
	}
	for _, index := range []string{
		"idx_catalog_generations_id_recovery_point",
		"idx_catalog_entries_generation_entry_recovery_point",
	} {
		if definition := fixture.indexDefinition(t, db, index); definition != "" {
			t.Fatalf("%s parent identity index %s remains after down: %s", fixture.engine, index, definition)
		}
	}
}

func (fixture migrationFixture) test065PreservesLegacyRows(t *testing.T) {
	migrator, db := fixture.openAt(t, backupAssetRsyncPublicationVersion)
	now := time.Date(2026, 7, 18, 2, 3, 4, 0, time.UTC)
	pointID, _, _ := fixture.insertSearchMigrationCatalog(t, db, "1", now)

	legacyDomains := []string{"entry_identity", "cursor_signing", "audit_fingerprint", "recovery_cleanup_ownership"}
	for index, domain := range legacyDomains {
		fixture.mustExec(t, db, `INSERT INTO wrapped_domain_keys
			(id, domain, version, state, wrapped_key, wrap_algorithm, wrapping_key_fingerprint,
			 activated_at, created_at, updated_at)
			VALUES (?, ?, 1, 'retired', ?, 'aes-256-gcm', ?, ?, ?, ?)`,
			fmt.Sprintf("legacy-key-%020d", index), domain, "wrapped-"+domain,
			strings.Repeat(strconv.Itoa(index+1), 64), now, now, now)
	}
	legacyHolders := []string{"rsync_parent", "catalog_build", "content_session", "processing_job", "export_job", "recovery_job", "point_publication"}
	for index, holder := range legacyHolders {
		fixture.insertSearchMigrationLease(t, db, fmt.Sprintf("legacy-lease-%018d", index), pointID, holder, now)
	}

	migrateToBackupAssetVersion(t, migrator, backupAssetSearchVersion)
	assertMigrationVersion(t, migrator, backupAssetSearchVersion)
	var domainCount, holderCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM wrapped_domain_keys WHERE domain <> 'search_token'`).Scan(&domainCount); err != nil {
		t.Fatalf("count %s preserved key domains: %v", fixture.engine, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM recovery_point_leases WHERE holder_type <> 'search_index'`).Scan(&holderCount); err != nil {
		t.Fatalf("count %s preserved lease holders: %v", fixture.engine, err)
	}
	if domainCount != len(legacyDomains) || holderCount != len(legacyHolders) {
		t.Fatalf("%s legacy rows changed: domains=%d/%d holders=%d/%d", fixture.engine, domainCount, len(legacyDomains), holderCount, len(legacyHolders))
	}

	fixture.mustExec(t, db, `INSERT INTO wrapped_domain_keys
		(id, domain, version, state, wrapped_key, wrap_algorithm, wrapping_key_fingerprint,
		 activated_at, created_at, updated_at)
		VALUES (?, 'search_token', 1, 'active', 'wrapped-search', 'aes-256-gcm', ?, ?, ?, ?)`,
		"search-key-00000000000000000000", strings.Repeat("a", 64), now, now, now)
	fixture.insertSearchMigrationLease(t, db, "search-lease-000000000000000", pointID, "search_index", now)
	fixture.expectExecRejected(t, db, `INSERT INTO wrapped_domain_keys
		(id, domain, version, state, wrapped_key, wrap_algorithm, wrapping_key_fingerprint,
		 activated_at, created_at, updated_at)
		VALUES (?, 'future_domain', 1, 'retired', '', '', '', ?, ?, ?)`, "invalid-key-0000000000000000000", now, now, now)
	fixture.expectExecRejected(t, db, `INSERT INTO recovery_point_leases
		(id, recovery_point_id, holder_type, owner_id, attempt_id, fence_token, status,
		 lease_expires_at, absolute_deadline, last_heartbeat_at, created_at, updated_at)
		VALUES (?, ?, 'future_holder', 'owner', 'attempt', 'fence', 'released', ?, ?, ?, ?, ?)`,
		"invalid-lease-000000000000000", pointID, now.Add(time.Minute), now.Add(time.Hour), now, now, now)
}

func (fixture migrationFixture) test065RejectsInvalidRows(t *testing.T) {
	_, db := fixture.openAt(t, backupAssetSearchVersion)
	now := time.Date(2026, 7, 18, 2, 3, 4, 0, time.UTC)
	pointA, catalogA, entryA := fixture.insertSearchMigrationCatalog(t, db, "2", now)
	pointB, catalogB, entryB := fixture.insertSearchMigrationCatalog(t, db, "3", now)
	searchA := strings.Repeat("4", 32)
	fixture.insertSearchMigrationGeneration(t, db, searchA, pointA, catalogA, 1, now)

	fixture.expectExecRejected(t, db, `INSERT INTO backup_asset_search_generations
		(id, recovery_point_id, catalog_generation_id, generation, state, is_active,
		 source_fingerprint, normalizer_version, search_key_version, projection_revision,
		 lease_id, build_attempt_id, fence_token_hash, expected_document_count,
		 written_document_count, error_code, correlation_id, started_at, created_at, updated_at)
		VALUES (?, ?, ?, 2, 'future', 0, '', 1, 1, 1, 'lease', 'attempt', ?, 0, 0, '', '', ?, ?, ?)`,
		strings.Repeat("5", 32), pointA, catalogA, strings.Repeat("a", 64), now, now, now)
	fixture.expectExecRejected(t, db, `INSERT INTO backup_asset_search_generations
		(id, recovery_point_id, catalog_generation_id, generation, state, is_active,
		 source_fingerprint, normalizer_version, search_key_version, projection_revision,
		 lease_id, build_attempt_id, fence_token_hash, expected_document_count,
		 written_document_count, error_code, correlation_id, started_at, created_at, updated_at)
		VALUES (?, ?, ?, 2, 'building', 0, '', 1, 1, 1, 'lease', 'attempt', ?, -1, 0, '', '', ?, ?, ?)`,
		strings.Repeat("6", 32), pointA, catalogA, strings.Repeat("a", 64), now, now, now)
	fixture.expectExecRejected(t, db, `INSERT INTO backup_asset_search_generations
		(id, recovery_point_id, catalog_generation_id, generation, state, is_active,
		 source_fingerprint, normalizer_version, search_key_version, projection_revision,
		 lease_id, build_attempt_id, fence_token_hash, expected_document_count,
		 written_document_count, error_code, correlation_id, started_at, created_at, updated_at)
		VALUES (?, ?, ?, 2, 'building', 0, '', 1, 1, 1, 'lease', 'attempt', ?, 0, 0, '', '', ?, ?, ?)`,
		strings.Repeat("7", 32), pointA, catalogB, strings.Repeat("a", 64), now, now, now)
	fixture.expectExecRejected(t, db, `INSERT INTO backup_asset_search_generations
		(id, recovery_point_id, catalog_generation_id, generation, state, is_active,
		 source_fingerprint, normalizer_version, search_key_version, projection_revision,
		 lease_id, build_attempt_id, fence_token_hash, expected_document_count,
		 written_document_count, error_code, correlation_id, started_at, created_at, updated_at)
		VALUES (?, ?, ?, 1, 'building', 0, '', 1, 1, 1, 'lease', 'attempt', ?, 0, 0, '', '', ?, ?, ?)`,
		strings.Repeat("8", 32), pointA, catalogA, strings.Repeat("a", 64), now, now, now)

	fixture.insertSearchMigrationDocument(t, db, searchA, pointA, catalogA, entryA, strings.Repeat("9", 64), now)
	fixture.expectExecRejected(t, db, `INSERT INTO backup_asset_search_documents
		(search_generation_id, document_id, recovery_point_id, catalog_generation_id, entry_id,
		 sensitivity, classification_revision, metadata_revision, entry_type, lineage_token,
		 path_group_token, path_sort_key, name_sort_key, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 'unknown', 1, 1, 'file', ?, ?, '', '', ?, ?)`,
		searchA, strings.Repeat("a", 64), pointB, catalogB, entryB,
		strings.Repeat("b", 64), strings.Repeat("c", 64), now, now)
	fixture.expectExecRejected(t, db, `INSERT INTO backup_asset_search_postings
		(search_generation_id, document_id, field, token_kind, key_version, token_hmac, term_frequency)
		VALUES (?, ?, 'future', 'exact', 1, ?, 1)`, searchA, strings.Repeat("9", 64), strings.Repeat("d", 64))

	fixture.insertSearchMigrationUser(t, db, 6501, "search-user-a", now)
	fixture.expectExecRejected(t, db, `INSERT INTO backup_asset_overlay_usage
		(owner_user_id, saved_search_count, favorite_count, tag_definition_count,
		 tag_assignment_count, recent_count, recent_rate_window_started_at,
		 recent_rate_window_write_count, version, updated_at)
		VALUES (?, -1, 0, 0, 0, 0, ?, 0, 1, ?)`, 6501, now, now)
	fixture.expectExecRejected(t, db, `INSERT INTO backup_asset_favorites
		(id, owner_user_id, recovery_point_id, entry_id, encrypted_label, state,
		 tombstone_reason, version, created_at, updated_at)
		VALUES (?, 999999, ?, ?, '', 'active', '', 1, ?, ?)`, strings.Repeat("b", 32), pointA, entryA, now, now)
}

func (fixture migrationFixture) test065UsedDownIsAtomic(t *testing.T) {
	testCases := []struct {
		name string
		seed func(*testing.T, *sql.DB)
	}{
		{name: "SearchKey", seed: func(t *testing.T, db *sql.DB) {
			now := time.Date(2026, 7, 18, 3, 4, 5, 0, time.UTC)
			fixture.mustExec(t, db, `INSERT INTO wrapped_domain_keys
				(id, domain, version, state, wrapped_key, wrap_algorithm, wrapping_key_fingerprint,
				 activated_at, created_at, updated_at)
				VALUES (?, 'search_token', 1, 'active', '', '', ?, ?, ?, ?)`,
				"used-search-key-000000000000000", strings.Repeat("a", 64), now, now, now)
		}},
		{name: "SearchLease", seed: func(t *testing.T, db *sql.DB) {
			now := time.Date(2026, 7, 18, 3, 4, 5, 0, time.UTC)
			pointID, _, _ := fixture.insertSearchMigrationCatalog(t, db, "5", now)
			fixture.insertSearchMigrationLease(t, db, "used-search-lease-00000000000", pointID, "search_index", now)
		}},
		{name: "Projection", seed: func(t *testing.T, db *sql.DB) {
			now := time.Date(2026, 7, 18, 3, 4, 5, 0, time.UTC)
			pointID, catalogID, _ := fixture.insertSearchMigrationCatalog(t, db, "6", now)
			fixture.insertSearchMigrationGeneration(t, db, strings.Repeat("7", 32), pointID, catalogID, 1, now)
		}},
		{name: "SavedSearch", seed: func(t *testing.T, db *sql.DB) {
			now := time.Date(2026, 7, 18, 3, 4, 5, 0, time.UTC)
			fixture.insertSearchMigrationUser(t, db, 6502, "saved-user", now)
			fixture.mustExec(t, db, `INSERT INTO backup_asset_saved_searches
				(id, owner_user_id, encrypted_ast, schema_version, scope_mode, version,
				 state, state_reason, created_at, updated_at)
				VALUES (?, 6502, 'enc:v2:test', 1, 'current', 1, 'active', '', ?, ?)`, strings.Repeat("8", 32), now, now)
		}},
		{name: "Favorite", seed: func(t *testing.T, db *sql.DB) {
			now := time.Date(2026, 7, 18, 3, 4, 5, 0, time.UTC)
			fixture.insertSearchMigrationUser(t, db, 6503, "favorite-user", now)
			fixture.mustExec(t, db, `INSERT INTO backup_asset_favorites
				(id, owner_user_id, recovery_point_id, entry_id, encrypted_label, state,
				 tombstone_reason, version, created_at, updated_at)
				VALUES (?, 6503, ?, ?, '', 'active', '', 1, ?, ?)`, strings.Repeat("9", 32), strings.Repeat("a", 32), strings.Repeat("b", 64), now, now)
		}},
		{name: "Tag", seed: func(t *testing.T, db *sql.DB) {
			now := time.Date(2026, 7, 18, 3, 4, 5, 0, time.UTC)
			fixture.insertSearchMigrationUser(t, db, 6504, "tag-user", now)
			fixture.mustExec(t, db, `INSERT INTO backup_asset_tag_definitions
				(id, owner_user_id, encrypted_name, name_token, key_version, token_state,
				 version, created_at, updated_at)
				VALUES (?, 6504, 'enc:v2:test', ?, 1, 'active', 1, ?, ?)`, strings.Repeat("c", 32), strings.Repeat("d", 64), now, now)
		}},
		{name: "Recent", seed: func(t *testing.T, db *sql.DB) {
			now := time.Date(2026, 7, 18, 3, 4, 5, 0, time.UTC)
			fixture.insertSearchMigrationUser(t, db, 6505, "recent-user", now)
			fixture.mustExec(t, db, `INSERT INTO backup_asset_recent_access
				(id, owner_user_id, recovery_point_id, entry_id, access_count, last_accessed_at,
				 expires_at, version, created_at, updated_at)
				VALUES (?, 6505, ?, ?, 1, ?, ?, 1, ?, ?)`, strings.Repeat("e", 32), strings.Repeat("f", 32), strings.Repeat("0", 64), now, now.Add(time.Hour), now, now)
		}},
		{name: "Usage", seed: func(t *testing.T, db *sql.DB) {
			now := time.Date(2026, 7, 18, 3, 4, 5, 0, time.UTC)
			fixture.insertSearchMigrationUser(t, db, 6506, "usage-user", now)
			fixture.mustExec(t, db, `INSERT INTO backup_asset_overlay_usage
				(owner_user_id, saved_search_count, favorite_count, tag_definition_count,
				 tag_assignment_count, recent_count, recent_rate_window_started_at,
				 recent_rate_window_write_count, version, updated_at)
				VALUES (6506, 0, 0, 0, 0, 0, ?, 0, 1, ?)`, now, now)
		}},
		{name: "Idempotency", seed: func(t *testing.T, db *sql.DB) {
			now := time.Date(2026, 7, 18, 3, 4, 5, 0, time.UTC)
			fixture.insertSearchMigrationUser(t, db, 6507, "idempotency-user", now)
			fixture.mustExec(t, db, `INSERT INTO backup_asset_overlay_idempotency
				(id, owner_user_id, action, key_hash, encrypted_request_fingerprint,
				 result_resource_type, result_resource_id, result_version, created_at, expires_at)
				VALUES (?, 6507, 'recent_clear', ?, 'enc:v2:test', 'none', '', 0, ?, ?)`,
				strings.Repeat("1", 32), strings.Repeat("2", 64), now, now.Add(time.Hour))
		}},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			migrator, db := fixture.openAt(t, backupAssetSearchVersion)
			testCase.seed(t, db)
			before := fixture.captureSearchMigrationSnapshot(t, migrator, db)
			if err := fixture.executeSearchDown(db); err == nil {
				t.Fatal("000065 down unexpectedly succeeded while Child 7 state remains")
			}
			after := fixture.captureSearchMigrationSnapshot(t, migrator, db)
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("rejected 000065 down changed migration state\nbefore=%+v\nafter=%+v", before, after)
			}
		})
	}
}

func backupAssetSearchModels() map[string]any {
	return map[string]any{
		"backup_asset_search_generations":        model.BackupAssetSearchGeneration{},
		"backup_asset_search_documents":          model.BackupAssetSearchDocument{},
		"backup_asset_search_postings":           model.BackupAssetSearchPosting{},
		"backup_asset_search_document_fields":    model.BackupAssetSearchDocumentField{},
		"backup_asset_saved_searches":            model.BackupAssetSavedSearch{},
		"backup_asset_saved_search_scope_points": model.BackupAssetSavedSearchScopePoint{},
		"backup_asset_favorites":                 model.BackupAssetFavorite{},
		"backup_asset_tag_definitions":           model.BackupAssetTagDefinition{},
		"backup_asset_tag_assignments":           model.BackupAssetTagAssignment{},
		"backup_asset_recent_access":             model.BackupAssetRecentAccess{},
		"backup_asset_overlay_usage":             model.BackupAssetOverlayUsage{},
		"backup_asset_overlay_idempotency":       model.BackupAssetOverlayIdempotency{},
	}
}

func (fixture migrationFixture) insertSearchMigrationCatalog(t *testing.T, db *sql.DB, marker string, now time.Time) (string, string, string) {
	t.Helper()
	repositoryID := strings.Repeat(marker, 32)
	pointID := strings.Repeat(marker, 32)
	catalogID := strings.Repeat(marker, 32)
	entryID := strings.Repeat(marker, 64)
	fixture.insertRepository(t, db, repositoryID, "rsync", now)
	if err := fixture.insertRecoveryPoint(db, publicationPointSeed{
		ID: pointID, RepositoryID: repositoryID, Semantics: "mutable_head",
		State: "observed", SourceFingerprint: "search-source-" + marker,
	}, now); err != nil {
		t.Fatalf("insert %s search migration recovery point: %v", fixture.engine, err)
	}
	fixture.mustExec(t, db, `INSERT INTO catalog_generations
		(id, recovery_point_id, generation, state, is_active, source_fingerprint,
		 expected_entry_count, written_entry_count, expected_digest, written_digest,
		 error_code, correlation_id, started_at, finished_at, created_at, updated_at)
		VALUES (?, ?, 1, 'complete', ?, ?, 1, 1, '', '', '', '', ?, ?, ?, ?)`,
		catalogID, pointID, true, "search-source-"+marker, now, now, now, now)
	fixture.mustExec(t, db, `INSERT INTO catalog_entries
		(generation_id, entry_id, recovery_point_id, normalized_path, name, entry_type,
		 size, mode, owner, mime_type, fingerprint, fingerprint_strength,
		 encrypted_provider_locator, security_state, created_at)
		VALUES (?, ?, ?, ?, ?, 'file', 1, '', '', '', '', '', '', '', ?)`,
		catalogID, entryID, pointID, "/search/"+marker+".txt", marker+".txt", now)
	return pointID, catalogID, entryID
}

func (fixture migrationFixture) insertSearchMigrationGeneration(
	t *testing.T,
	db *sql.DB,
	id, pointID, catalogID string,
	generation int,
	now time.Time,
) {
	t.Helper()
	fixture.mustExec(t, db, `INSERT INTO backup_asset_search_generations
		(id, recovery_point_id, catalog_generation_id, generation, state, is_active,
		 source_fingerprint, normalizer_version, search_key_version, projection_revision,
		 lease_id, build_attempt_id, fence_token_hash, expected_document_count,
		 written_document_count, error_code, correlation_id, started_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'building', ?, '', 1, 1, 1, 'lease', 'attempt', ?, 1, 0, '', '', ?, ?, ?)`,
		id, pointID, catalogID, generation, false, strings.Repeat("a", 64), now, now, now)
}

func (fixture migrationFixture) insertSearchMigrationDocument(
	t *testing.T,
	db *sql.DB,
	searchID, pointID, catalogID, entryID, documentID string,
	now time.Time,
) {
	t.Helper()
	fixture.mustExec(t, db, `INSERT INTO backup_asset_search_documents
		(search_generation_id, document_id, recovery_point_id, catalog_generation_id,
		 entry_id, sensitivity, classification_revision, metadata_revision, entry_type,
		 lineage_token, path_group_token, path_sort_key, name_sort_key, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 'unknown', 1, 1, 'file', ?, ?, '2f736561726368', '312e747874', ?, ?)`,
		searchID, documentID, pointID, catalogID, entryID,
		strings.Repeat("b", 64), strings.Repeat("c", 64), now, now)
}

func (fixture migrationFixture) insertSearchMigrationLease(t *testing.T, db *sql.DB, id, pointID, holder string, now time.Time) {
	t.Helper()
	fixture.mustExec(t, db, `INSERT INTO recovery_point_leases
		(id, recovery_point_id, holder_type, owner_id, attempt_id, fence_token, status,
		 lease_expires_at, absolute_deadline, last_heartbeat_at, released_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'attempt', 'fence', 'released', ?, ?, ?, ?, ?, ?)`,
		id, pointID, holder, "owner-"+holder, now.Add(time.Minute), now.Add(time.Hour), now, now, now, now)
}

func (fixture migrationFixture) insertSearchMigrationUser(t *testing.T, db *sql.DB, id int64, username string, now time.Time) {
	t.Helper()
	fixture.mustExec(t, db, `INSERT INTO users
		(id, username, password_hash, role, totp_secret, totp_enabled, recovery_codes,
		 token_version, onboarded, created_at, updated_at)
		VALUES (?, ?, 'hash', 'operator', '', ?, '', 0, ?, ?, ?)`, id, username, false, true, now, now)
}

func (fixture migrationFixture) insertContentMigrationGrant(
	t *testing.T,
	db *sql.DB,
	ownerUserID int64,
	grantID, deliveryID, pointID, catalogID, entryID, leaseID string,
	now time.Time,
) {
	t.Helper()
	fixture.mustExec(t, db, `INSERT INTO backup_asset_delivery_grants
		(id, delivery_id, resource_kind, recovery_point_id, catalog_generation_id, entry_id,
		 owner_user_id, session_jti, session_token_version, session_role, session_expires_at,
		 action, method_policy, range_policy, renderer, profile, classification,
		 classification_revision, classification_source_revision, provider_kind,
			 source_fingerprint, entry_fingerprint, fingerprint_strength, representation_etag,
			 source_size, source_modified_at, detected_media_type, representation_source_bytes,
			 representation_size, representation_truncated, cookie_secret_hash,
		 state, revocation_reason, lease_id, lease_attempt_id, lease_fence_token_hash,
		 absolute_expires_at, idle_expires_at, idle_ttl_seconds, last_activity_at,
		 max_bytes_per_request, max_cumulative_bytes, max_requests, max_in_flight,
		 reserved_bytes, delivered_bytes, request_count, in_flight, version, audit_state,
		 audit_range_count, audit_range_bytes, audit_request_count, audit_success_count,
		 audit_blocked_count, audit_failure_count, audit_failure_code, audit_attempt_count,
		 created_at, updated_at)
		VALUES (?, ?, 'backup_asset', ?, ?, ?, ?, ?, 0, 'operator', ?,
		 'preview', 'get_head', 'none', 'escaped_text', 'text_v1', 'non_secret',
		 1, 1, 'rsync', 'content-source-v1', 'entry-v1', 'strong', '"content-etag"',
			 1, ?, 'text/plain', 1, 1, ?, ?, 'active', '', ?, ?, ?, ?, ?, 60, ?,
		 64, 256, 10, 2, 0, 0, 0, 0, 1, 'none', 0, 0, 0, 0, 0, 0, '', 0, ?, ?)`,
		grantID, deliveryID, pointID, catalogID, entryID, ownerUserID, strings.Repeat("a", 32), now.Add(time.Hour),
		now, false, strings.Repeat("b", 64), leaseID, strings.Repeat("c", 32), strings.Repeat("d", 64),
		now.Add(10*time.Minute), now.Add(time.Minute), now, now, now)
}

func (fixture migrationFixture) insertContentMigrationRequest(t *testing.T, db *sql.DB, requestID, grantID string, now time.Time) {
	t.Helper()
	fixture.mustExec(t, db, `INSERT INTO backup_asset_delivery_requests
		(id, grant_id, method, range_kind, state, reserved_bytes, provider_bytes,
		 response_bytes, http_status, failure_code, started_at, last_progress_at,
		 finished_at, created_at, updated_at, version)
		VALUES (?, ?, 'HEAD', 'full', 'succeeded', 0, 0, 0, 200, '', ?, ?, ?, ?, ?, 1)`,
		requestID, grantID, now, now, now, now, now)
}

type contentMigrationSnapshot struct {
	version     uint
	dirty       bool
	tables      map[string]bool
	definitions map[string]string
	indexes     map[string]string
	rowCounts   map[string]int
}

func (fixture migrationFixture) captureContentMigrationSnapshot(t *testing.T, migrator *migrate.Migrate, db *sql.DB) contentMigrationSnapshot {
	t.Helper()
	version, dirty, err := migrator.Version()
	if err != nil {
		t.Fatalf("read %s content migration version: %v", fixture.engine, err)
	}
	tables := append([]string{"recovery_point_leases", "backup_asset_audit_events"}, backupAssetContentTables...)
	snapshot := contentMigrationSnapshot{
		version: version, dirty: dirty,
		tables: make(map[string]bool, len(tables)), definitions: make(map[string]string, len(tables)),
		indexes: map[string]string{
			"idx_backup_asset_delivery_grants_delivery_state":    fixture.indexDefinition(t, db, "idx_backup_asset_delivery_grants_delivery_state"),
			"idx_backup_asset_delivery_requests_grant_state":     fixture.indexDefinition(t, db, "idx_backup_asset_delivery_requests_grant_state"),
			"idx_backup_asset_delivery_usage_window":             fixture.indexDefinition(t, db, "idx_backup_asset_delivery_usage_window"),
			"idx_backup_asset_audit_events_content_grant_action": fixture.indexDefinition(t, db, "idx_backup_asset_audit_events_content_grant_action"),
		},
		rowCounts: make(map[string]int, len(tables)),
	}
	for _, table := range tables {
		exists := databaseTableExists(t, db, fixture.engine, table)
		snapshot.tables[table] = exists
		if !exists {
			continue
		}
		snapshot.definitions[table] = fixture.tableDefinition(t, db, table)
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatalf("count %s rows in %s: %v", fixture.engine, table, err)
		}
		snapshot.rowCounts[table] = count
	}
	return snapshot
}

func (fixture migrationFixture) executeContentDown(db *sql.DB) error {
	path := "migrations/sqlite/000066_backup_asset_content.down.sql"
	migrationFS := sqliteMigrationsFS
	if fixture.engine == "postgres" {
		path = "migrations/postgres/000066_backup_asset_content.down.sql"
		migrationFS = postgresMigrationsFS
	}
	script, err := migrationFS.ReadFile(path)
	if err != nil {
		return err
	}
	_, err = db.Exec(string(script))
	if err != nil {
		_, _ = db.Exec("ROLLBACK")
	}
	return err
}

type recoveryMigrationSnapshot struct {
	version     uint
	dirty       bool
	tables      map[string]bool
	definitions map[string]string
	indexes     map[string]string
	triggers    map[string]string
	functions   map[string]string
	foreignKeys map[string]string
	rowCounts   map[string]int
}

func (fixture migrationFixture) captureRecoveryMigrationSnapshot(
	t *testing.T,
	migrator *migrate.Migrate,
	db *sql.DB,
) recoveryMigrationSnapshot {
	t.Helper()
	version, dirty, err := migrator.Version()
	if err != nil {
		t.Fatalf("read %s recovery migration version: %v", fixture.engine, err)
	}
	tables := append([]string{
		"task_runs",
		"backup_asset_delivery_grants",
		"backup_asset_delivery_requests",
		"backup_asset_delivery_usage",
		"recovery_point_leases",
	}, backupAssetRecoveryTables...)
	snapshot := recoveryMigrationSnapshot{
		version:     version,
		dirty:       dirty,
		tables:      make(map[string]bool, len(tables)),
		definitions: make(map[string]string, len(tables)),
		indexes: map[string]string{
			"idx_task_runs_node_snapshot_status":                     fixture.indexDefinition(t, db, "idx_task_runs_node_snapshot_status"),
			"idx_recovery_points_repository_id_id":                   fixture.indexDefinition(t, db, "idx_recovery_points_repository_id_id"),
			"idx_backup_asset_recovery_plans_id_generation_point":    fixture.indexDefinition(t, db, "idx_backup_asset_recovery_plans_id_generation_point"),
			"idx_backup_asset_recovery_plans_id_target":              fixture.indexDefinition(t, db, "idx_backup_asset_recovery_plans_id_target"),
			"idx_backup_asset_recovery_plans_id_target_binding":      fixture.indexDefinition(t, db, "idx_backup_asset_recovery_plans_id_target_binding"),
			"idx_backup_asset_recovery_plans_requester_state":        fixture.indexDefinition(t, db, "idx_backup_asset_recovery_plans_requester_state"),
			"idx_backup_asset_recovery_plans_preflight_expiry":       fixture.indexDefinition(t, db, "idx_backup_asset_recovery_plans_preflight_expiry"),
			"idx_backup_asset_recovery_plan_items_plan_ordinal":      fixture.indexDefinition(t, db, "idx_backup_asset_recovery_plan_items_plan_ordinal"),
			"idx_backup_asset_recovery_preflights_plan_expiry":       fixture.indexDefinition(t, db, "idx_backup_asset_recovery_preflights_plan_expiry"),
			"idx_backup_asset_recovery_jobs_plan":                    fixture.indexDefinition(t, db, "idx_backup_asset_recovery_jobs_plan"),
			"idx_backup_asset_recovery_jobs_id_target_node":          fixture.indexDefinition(t, db, "idx_backup_asset_recovery_jobs_id_target_node"),
			"idx_backup_asset_recovery_jobs_claim":                   fixture.indexDefinition(t, db, "idx_backup_asset_recovery_jobs_claim"),
			"idx_backup_asset_recovery_job_items_job_outcome":        fixture.indexDefinition(t, db, "idx_backup_asset_recovery_job_items_job_outcome"),
			"idx_backup_asset_recovery_attempts_current":             fixture.indexDefinition(t, db, "idx_backup_asset_recovery_attempts_current"),
			"idx_backup_asset_recovery_attempts_expiry":              fixture.indexDefinition(t, db, "idx_backup_asset_recovery_attempts_expiry"),
			"idx_backup_asset_recovery_checkpoints_job_sequence":     fixture.indexDefinition(t, db, "idx_backup_asset_recovery_checkpoints_job_sequence"),
			"idx_backup_asset_recovery_checkpoints_delete_grant":     fixture.indexDefinition(t, db, "idx_backup_asset_recovery_checkpoints_delete_grant"),
			"idx_backup_asset_recovery_evidence_job_created":         fixture.indexDefinition(t, db, "idx_backup_asset_recovery_evidence_job_created"),
			"idx_backup_asset_recovery_node_leases_active_node":      fixture.indexDefinition(t, db, "idx_backup_asset_recovery_node_leases_active_node"),
			"idx_backup_asset_recovery_node_leases_claim":            fixture.indexDefinition(t, db, "idx_backup_asset_recovery_node_leases_claim"),
			"idx_backup_asset_recovery_result_sets_expiry":           fixture.indexDefinition(t, db, "idx_backup_asset_recovery_result_sets_expiry"),
			"idx_backup_asset_recovery_result_sets_cleanup":          fixture.indexDefinition(t, db, "idx_backup_asset_recovery_result_sets_cleanup"),
			"idx_backup_asset_recovery_results_job":                  fixture.indexDefinition(t, db, "idx_backup_asset_recovery_results_job"),
			"idx_backup_asset_recovery_grants_plan_category_expiry":  fixture.indexDefinition(t, db, "idx_backup_asset_recovery_grants_plan_category_expiry"),
			"idx_backup_asset_recovery_grants_job_category":          fixture.indexDefinition(t, db, "idx_backup_asset_recovery_grants_job_category"),
			"idx_backup_asset_delivery_grants_delivery_state":        fixture.indexDefinition(t, db, "idx_backup_asset_delivery_grants_delivery_state"),
			"idx_backup_asset_delivery_grants_session_state":         fixture.indexDefinition(t, db, "idx_backup_asset_delivery_grants_session_state"),
			"idx_backup_asset_delivery_grants_resource_state":        fixture.indexDefinition(t, db, "idx_backup_asset_delivery_grants_resource_state"),
			"idx_backup_asset_delivery_grants_expiry":                fixture.indexDefinition(t, db, "idx_backup_asset_delivery_grants_expiry"),
			"idx_backup_asset_delivery_grants_audit":                 fixture.indexDefinition(t, db, "idx_backup_asset_delivery_grants_audit"),
			"idx_backup_asset_delivery_grants_recovery_result_state": fixture.indexDefinition(t, db, "idx_backup_asset_delivery_grants_recovery_result_state"),
			"idx_backup_asset_delivery_requests_grant_state":         fixture.indexDefinition(t, db, "idx_backup_asset_delivery_requests_grant_state"),
			"idx_backup_asset_delivery_requests_reconcile":           fixture.indexDefinition(t, db, "idx_backup_asset_delivery_requests_reconcile"),
		},
		triggers:    make(map[string]string, len(backupAssetRecoveryOwnedTriggersForEngine(fixture.engine))),
		functions:   make(map[string]string, len(backupAssetRecoveryOwnedPostgresFunctions)),
		foreignKeys: fixture.recoveryContentGrantForeignKeyDefinitions(t, db),
		rowCounts:   make(map[string]int, len(tables)),
	}
	for _, ownedTrigger := range backupAssetRecoveryOwnedTriggersForEngine(fixture.engine) {
		snapshot.triggers[ownedTrigger.name] = fixture.recoveryTriggerDefinition(
			t, db, ownedTrigger.table, ownedTrigger.name,
		)
	}
	if fixture.engine == "postgres" {
		for _, function := range backupAssetRecoveryOwnedPostgresFunctions {
			snapshot.functions[function] = fixture.recoveryFunctionDefinition(t, db, function)
		}
	}
	for _, table := range tables {
		exists := databaseTableExists(t, db, fixture.engine, table)
		snapshot.tables[table] = exists
		if !exists {
			continue
		}
		snapshot.definitions[table] = fixture.tableDefinition(t, db, table)
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatalf("count %s rows in %s: %v", fixture.engine, table, err)
		}
		snapshot.rowCounts[table] = count
	}
	return snapshot
}

func (fixture migrationFixture) recoveryTriggerDefinition(t *testing.T, db *sql.DB, table, trigger string) string {
	t.Helper()
	var definition string
	var err error
	if fixture.engine == "sqlite" {
		err = db.QueryRow(`SELECT COALESCE(sql, '') FROM sqlite_master WHERE type = 'trigger' AND name = ?`, trigger).Scan(&definition)
	} else {
		err = db.QueryRow(`SELECT COALESCE(pg_get_triggerdef(trigger_row.oid), '')
			FROM pg_trigger AS trigger_row
			JOIN pg_class AS relation ON relation.oid = trigger_row.tgrelid
			JOIN pg_namespace AS namespace ON namespace.oid = relation.relnamespace
			WHERE namespace.nspname = current_schema()
			  AND relation.relname = $1
			  AND trigger_row.tgname = $2
			  AND NOT trigger_row.tgisinternal`, table, trigger).Scan(&definition)
	}
	if err != nil {
		t.Fatalf("load %s trigger %s: %v", fixture.engine, trigger, err)
	}
	return strings.Join(strings.Fields(strings.ToLower(definition)), " ")
}

func (fixture migrationFixture) recoveryFunctionDefinition(t *testing.T, db *sql.DB, function string) string {
	t.Helper()
	if fixture.engine != "postgres" {
		return ""
	}
	var definition string
	err := db.QueryRow(fixture.bind(`SELECT pg_get_functiondef(function_row.oid)
		FROM pg_proc AS function_row
		JOIN pg_namespace AS namespace ON namespace.oid = function_row.pronamespace
		WHERE namespace.nspname = current_schema()
		  AND function_row.proname = ?
		ORDER BY function_row.oid
		LIMIT 1`), function).Scan(&definition)
	if errors.Is(err, sql.ErrNoRows) {
		return ""
	}
	if err != nil {
		t.Fatalf("load %s function %s: %v", fixture.engine, function, err)
	}
	return strings.Join(strings.Fields(strings.ToLower(definition)), " ")
}

func (fixture migrationFixture) recoveryTriggerExists(t *testing.T, db *sql.DB, table, trigger string) bool {
	t.Helper()
	var count int
	var err error
	if fixture.engine == "sqlite" {
		err = db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger' AND name = ?`, trigger).Scan(&count)
	} else {
		err = db.QueryRow(`SELECT COUNT(*)
			FROM pg_trigger AS trigger_row
			JOIN pg_class AS relation ON relation.oid = trigger_row.tgrelid
			JOIN pg_namespace AS namespace ON namespace.oid = relation.relnamespace
			WHERE namespace.nspname = current_schema()
			  AND relation.relname = $1
			  AND trigger_row.tgname = $2
			  AND NOT trigger_row.tgisinternal`, table, trigger).Scan(&count)
	}
	if err != nil {
		t.Fatalf("count %s trigger %s: %v", fixture.engine, trigger, err)
	}
	return count != 0
}

func (fixture migrationFixture) assertRecoveryDownRejectedUnchanged(
	t *testing.T,
	migrator *migrate.Migrate,
	db *sql.DB,
) {
	t.Helper()
	before := fixture.captureRecoveryMigrationSnapshot(t, migrator, db)
	if err := migrator.Steps(-1); err == nil {
		t.Fatal("000069 down unexpectedly succeeded while recovery state remains")
	}
	after := fixture.captureRecoveryMigrationSnapshot(t, migrator, db)
	assertRecoveryMigrationSnapshotEqual(t, before, after, "rejected 000069 down")
}

func assertRecoveryMigrationSnapshotEqual(t *testing.T, before, after recoveryMigrationSnapshot, context string) {
	t.Helper()
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("%s changed migration snapshot: version=%d dirty=%t -> version=%d dirty=%t; tables_changed=%t definitions_changed=%t indexes_changed=%t triggers_changed=%t functions_changed=%t foreign_keys_changed=%t rows_changed=%t",
			context,
			before.version,
			before.dirty,
			after.version,
			after.dirty,
			!reflect.DeepEqual(before.tables, after.tables),
			!reflect.DeepEqual(before.definitions, after.definitions),
			!reflect.DeepEqual(before.indexes, after.indexes),
			!reflect.DeepEqual(before.triggers, after.triggers),
			!reflect.DeepEqual(before.functions, after.functions),
			!reflect.DeepEqual(before.foreignKeys, after.foreignKeys),
			!reflect.DeepEqual(before.rowCounts, after.rowCounts),
		)
	}
}

func (fixture migrationFixture) expectExecRejected(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(fixture.bind(query), args...); err == nil {
		t.Fatalf("%s invalid migration row unexpectedly succeeded", fixture.engine)
	}
}

type searchMigrationSnapshot struct {
	version     uint
	dirty       bool
	tables      map[string]bool
	definitions map[string]string
	indexes     map[string]string
	rowCounts   map[string]int
}

func (fixture migrationFixture) captureSearchMigrationSnapshot(t *testing.T, migrator *migrate.Migrate, db *sql.DB) searchMigrationSnapshot {
	t.Helper()
	version, dirty, err := migrator.Version()
	if err != nil {
		t.Fatalf("read %s search migration version: %v", fixture.engine, err)
	}
	tables := append([]string{"wrapped_domain_keys", "recovery_point_leases"}, backupAssetSearchTables...)
	snapshot := searchMigrationSnapshot{
		version: version, dirty: dirty, tables: make(map[string]bool, len(tables)),
		definitions: make(map[string]string, len(tables)),
		indexes: map[string]string{
			"idx_catalog_generations_id_recovery_point":           fixture.indexDefinition(t, db, "idx_catalog_generations_id_recovery_point"),
			"idx_catalog_entries_generation_entry_recovery_point": fixture.indexDefinition(t, db, "idx_catalog_entries_generation_entry_recovery_point"),
			"idx_backup_asset_search_generations_active":          fixture.indexDefinition(t, db, "idx_backup_asset_search_generations_active"),
			"idx_backup_asset_overlay_idempotency_expiry":         fixture.indexDefinition(t, db, "idx_backup_asset_overlay_idempotency_expiry"),
		},
		rowCounts: make(map[string]int, len(tables)),
	}
	for _, table := range tables {
		exists := databaseTableExists(t, db, fixture.engine, table)
		snapshot.tables[table] = exists
		if !exists {
			continue
		}
		snapshot.definitions[table] = fixture.tableDefinition(t, db, table)
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatalf("count %s rows in %s: %v", fixture.engine, table, err)
		}
		snapshot.rowCounts[table] = count
	}
	return snapshot
}

func (fixture migrationFixture) executeSearchDown(db *sql.DB) error {
	path := "migrations/sqlite/000065_backup_asset_search.down.sql"
	migrationFS := sqliteMigrationsFS
	if fixture.engine == "postgres" {
		path = "migrations/postgres/000065_backup_asset_search.down.sql"
		migrationFS = postgresMigrationsFS
	}
	script, err := migrationFS.ReadFile(path)
	if err != nil {
		return err
	}
	_, err = db.Exec(string(script))
	if err != nil {
		_, _ = db.Exec("ROLLBACK")
	}
	return err
}

func (fixture migrationFixture) openAt(t *testing.T, version uint) (*migrate.Migrate, *sql.DB) {
	t.Helper()
	migrator, db := fixture.open(t)
	migrateToBackupAssetVersion(t, migrator, version)
	return migrator, db
}

func (fixture migrationFixture) testApplyDown(t *testing.T) {
	migrator, db := fixture.openAt(t, backupAssetMigrationVersion)
	migrateToBackupAssetVersion(t, migrator, backupAssetPublicationVersion)
	assertMigrationVersion(t, migrator, backupAssetPublicationVersion)
	fixture.assertPublicationContractSchema(t, db)
	if fixture.engine == "sqlite" {
		assertSQLiteForeignKeyCheck(t, db)
	}

	now := time.Date(2026, 7, 14, 3, 4, 5, 0, time.UTC)
	repositoryID := strings.Repeat("a", 32)
	linkID := strings.Repeat("b", 32)
	pointID := strings.Repeat("c", 32)
	leaseID := strings.Repeat("d", 32)
	taskID := int64(9301)
	fixture.insertRepository(t, db, repositoryID, "restic", now)
	fixture.insertTaskAndRun(t, db, taskID, 9302, now)
	fixture.insertTaskLink(t, db, linkID, taskID, repositoryID, "native_snapshot", now)
	fixture.mustInsertRecoveryPoint(t, db, publicationPointSeed{
		ID: pointID, RepositoryID: repositoryID, Semantics: "native_snapshot", State: "preparing",
	})
	fixture.insertPublicationLease(t, db, leaseID, pointID, "point_publication", "released", now)

	fixture.mustExec(t, db, `DELETE FROM recovery_point_leases WHERE id = ?`, leaseID)
	fixture.mustExec(t, db, `DELETE FROM recovery_points WHERE id = ?`, pointID)
	if err := migrator.Steps(-1); err != nil {
		t.Fatalf("step %s migration down to 000062: %v", fixture.engine, err)
	}
	assertMigrationVersion(t, migrator, backupAssetMigrationVersion)
	fixture.assertPublicationContractAbsent(t, db)
	if fixture.engine == "sqlite" {
		assertSQLiteForeignKeyCheck(t, db)
	}
	var publicationMode string
	if err := db.QueryRow(fixture.bind(`SELECT publication_mode FROM task_repository_links WHERE id = ?`), linkID).Scan(&publicationMode); err != nil {
		t.Fatalf("read Restic link after down: %v", err)
	}
	if publicationMode != "native_object_versions" {
		t.Fatalf("Restic link mode after down=%q, want native_object_versions", publicationMode)
	}
}

func (fixture migrationFixture) testConvertsOnlyResticLinks(t *testing.T) {
	migrator, db := fixture.openAt(t, backupAssetMigrationVersion)
	now := time.Date(2026, 7, 14, 3, 4, 5, 0, time.UTC)
	resticRepositoryID := strings.Repeat("1", 32)
	rcloneRepositoryID := strings.Repeat("2", 32)
	resticTaskID := int64(9401)
	rcloneTaskID := int64(9403)
	fixture.insertRepository(t, db, resticRepositoryID, "restic", now)
	fixture.insertRepository(t, db, rcloneRepositoryID, "rclone", now)
	fixture.insertTaskAndRun(t, db, resticTaskID, 9402, now)
	fixture.insertTaskAndRun(t, db, rcloneTaskID, 9404, now)
	fixture.insertTaskLink(t, db, strings.Repeat("3", 32), resticTaskID, resticRepositoryID, "native_object_versions", now)
	fixture.insertTaskLink(t, db, strings.Repeat("4", 32), rcloneTaskID, rcloneRepositoryID, "native_object_versions", now)

	migrateToBackupAssetVersion(t, migrator, backupAssetPublicationVersion)
	fixture.assertPublicationContractSchema(t, db)
	for _, expected := range []struct {
		repositoryID string
		mode         string
	}{
		{resticRepositoryID, "native_snapshot"},
		{rcloneRepositoryID, "native_object_versions"},
	} {
		var got string
		if err := db.QueryRow(fixture.bind(`SELECT publication_mode FROM task_repository_links WHERE repository_id = ?`), expected.repositoryID).Scan(&got); err != nil {
			t.Fatalf("read converted link for repository %s: %v", expected.repositoryID, err)
		}
		if got != expected.mode {
			t.Fatalf("repository %s publication mode=%q, want %q", expected.repositoryID, got, expected.mode)
		}
	}
}

func (fixture migrationFixture) testUniqueProducingRunAcrossSemantics(t *testing.T) {
	_, db := fixture.openAt(t, backupAssetPublicationVersion)
	now := time.Date(2026, 7, 14, 3, 4, 5, 0, time.UTC)
	repositoryID := strings.Repeat("5", 32)
	taskID := int64(9501)
	runID := int64(9502)
	fixture.insertRepository(t, db, repositoryID, "restic", now)
	fixture.insertTaskAndRun(t, db, taskID, runID, now)
	fixture.mustInsertRecoveryPoint(t, db, publicationPointSeed{
		ID: strings.Repeat("6", 32), RepositoryID: repositoryID, TaskID: &taskID, TaskRunID: &runID,
		Semantics: "native_snapshot", State: "preparing", SourceFingerprint: "first-native-point",
	})
	for _, candidate := range []publicationPointSeed{
		{ID: strings.Repeat("7", 32), RepositoryID: repositoryID, TaskID: &taskID, TaskRunID: &runID, Semantics: "mutable_head", State: "observed"},
		{ID: strings.Repeat("8", 32), RepositoryID: repositoryID, TaskID: &taskID, TaskRunID: &runID, Semantics: "xirang_manifest", State: "preparing"},
		{ID: strings.Repeat("9", 32), RepositoryID: repositoryID, TaskID: &taskID, TaskRunID: &runID, Semantics: "native_snapshot", State: "preparing", SourceFingerprint: "second-native-point"},
	} {
		if err := fixture.insertRecoveryPoint(db, candidate, now); err == nil {
			t.Fatalf("second %s point with producing TaskRun %d unexpectedly succeeded", candidate.Semantics, runID)
		}
	}
	fixture.mustInsertRecoveryPoint(t, db, publicationPointSeed{
		ID: strings.Repeat("a", 31) + "b", RepositoryID: repositoryID, Semantics: "mutable_head", State: "observed",
	})
}

func (fixture migrationFixture) testUniqueNativeSourcePerRepository(t *testing.T) {
	_, db := fixture.openAt(t, backupAssetPublicationVersion)
	now := time.Date(2026, 7, 14, 3, 4, 5, 0, time.UTC)
	firstRepositoryID := strings.Repeat("a", 32)
	secondRepositoryID := strings.Repeat("b", 32)
	fingerprint := "same-native-source"
	fixture.insertRepository(t, db, firstRepositoryID, "restic", now)
	fixture.insertRepository(t, db, secondRepositoryID, "restic", now)
	fixture.mustInsertRecoveryPoint(t, db, publicationPointSeed{
		ID: strings.Repeat("c", 32), RepositoryID: firstRepositoryID, Semantics: "native_snapshot", State: "preparing", SourceFingerprint: fingerprint,
	})
	if err := fixture.insertRecoveryPoint(db, publicationPointSeed{
		ID: strings.Repeat("d", 32), RepositoryID: firstRepositoryID, Semantics: "native_snapshot", State: "preparing", SourceFingerprint: fingerprint,
	}, now); err == nil {
		t.Fatal("same Repository/native snapshot source fingerprint unexpectedly succeeded")
	}
	fixture.mustInsertRecoveryPoint(t, db, publicationPointSeed{
		ID: strings.Repeat("e", 32), RepositoryID: secondRepositoryID, Semantics: "native_snapshot", State: "preparing", SourceFingerprint: fingerprint,
	})
	fixture.mustInsertRecoveryPoint(t, db, publicationPointSeed{
		ID: strings.Repeat("f", 32), RepositoryID: firstRepositoryID, Semantics: "xirang_manifest", State: "preparing", SourceFingerprint: fingerprint,
	})
}

func (fixture migrationFixture) testDownRejectsActivePublicationLease(t *testing.T) {
	migrator, db := fixture.openAt(t, backupAssetPublicationVersion)
	now := time.Date(2026, 7, 14, 3, 4, 5, 0, time.UTC)
	repositoryID := strings.Repeat("a", 32)
	pointID := strings.Repeat("b", 32)
	fixture.insertRepository(t, db, repositoryID, "restic", now)
	fixture.mustInsertRecoveryPoint(t, db, publicationPointSeed{
		ID: pointID, RepositoryID: repositoryID, Semantics: "native_snapshot", State: "preparing",
	})
	fixture.insertPublicationLease(t, db, strings.Repeat("c", 32), pointID, "point_publication", "active", now)
	fixture.assertPublicationDownRejectedUnchanged(t, migrator, db)
}

func (fixture migrationFixture) testDownRejectsEveryNativePointStateAndNullableLineage(t *testing.T) {
	states := []string{
		"preparing", "verifying", "committed", "degraded", "expiring",
		"expired", "failed", "purge_blocked",
	}
	for index, state := range states {
		state := state
		t.Run(state, func(t *testing.T) {
			migrator, db := fixture.openAt(t, backupAssetPublicationVersion)
			now := time.Date(2026, 7, 14, 3, 4, 5, 0, time.UTC)
			repositoryID := fmt.Sprintf("%032x", index+101)
			pointID := fmt.Sprintf("%032x", index+1)
			fixture.insertRepository(t, db, repositoryID, "restic", now)
			fixture.mustInsertRecoveryPoint(t, db, publicationPointSeed{
				ID: pointID, RepositoryID: repositoryID, Semantics: "native_snapshot", State: state,
			})
			fixture.assertPublicationDownRejectedUnchanged(t, migrator, db)
		})
	}
}

func (fixture migrationFixture) testRejectedDownLeavesVersionSchemaAndDataUnchanged(t *testing.T) {
	migrator, db := fixture.openAt(t, backupAssetPublicationVersion)
	now := time.Date(2026, 7, 14, 3, 4, 5, 0, time.UTC)
	repositoryID := strings.Repeat("a", 32)
	taskID := int64(9601)
	runID := int64(9602)
	fixture.insertRepository(t, db, repositoryID, "restic", now)
	fixture.insertTaskAndRun(t, db, taskID, runID, now)
	fixture.insertTaskLink(t, db, strings.Repeat("b", 32), taskID, repositoryID, "native_snapshot", now)
	fixture.mustInsertRecoveryPoint(t, db, publicationPointSeed{
		ID: strings.Repeat("c", 32), RepositoryID: repositoryID, TaskID: &taskID, TaskRunID: &runID,
		Semantics: "native_snapshot", State: "failed", SourceFingerprint: "retained-native-history",
	})
	fixture.assertPublicationDownRejectedUnchanged(t, migrator, db)
}

func (fixture migrationFixture) testUTCAndModelParity(t *testing.T) {
	_, db := fixture.openAt(t, backupAssetPublicationVersion)
	fixture.assertPublicationContractSchema(t, db)
	assertFoundationModelParity(t, db, fixture.engine)
	if fixture.engine == "sqlite" {
		for _, table := range backupAssetFoundationTables {
			assertSQLiteTimeColumnsHaveNoDefault(t, db, table)
		}
		return
	}
	assertPostgresUTCRoundTripAndNoTimeDefaults(t, db)
}

func (fixture migrationFixture) test064ApplyAndParity(t *testing.T) {
	migrator, db := fixture.openAt(t, backupAssetPublicationVersion)
	migrateToBackupAssetVersion(t, migrator, backupAssetRsyncPublicationVersion)
	assertMigrationVersion(t, migrator, backupAssetRsyncPublicationVersion)
	fixture.assertRsyncPublicationContractSchema(t, db)
	assertRsyncPublicationModelParity(t, db, fixture.engine)
	if fixture.engine == "sqlite" {
		assertSQLiteTimeColumnsHaveNoDefault(t, db, "backup_asset_managed_history_latches")
	}

	if err := migrator.Steps(-1); err != nil {
		t.Fatalf("step %s migration down to 000063: %v", fixture.engine, err)
	}
	assertMigrationVersion(t, migrator, backupAssetPublicationVersion)
	fixture.assertRsyncPublicationContractAbsent(t, db)
}

func (fixture migrationFixture) test064BackfillsEveryResticManagedState(t *testing.T) {
	migrator, db := fixture.openAt(t, backupAssetPublicationVersion)
	now := time.Date(2026, 7, 15, 3, 4, 5, 0, time.UTC)
	firstRepositoryID := strings.Repeat("1", 32)
	secondRepositoryID := strings.Repeat("2", 32)
	fixture.insertRepository(t, db, firstRepositoryID, "restic", now)
	fixture.insertRepository(t, db, secondRepositoryID, "restic", now.Add(time.Hour))
	states := []string{
		"preparing", "verifying", "committed", "degraded", "expiring",
		"expired", "failed", "purge_blocked",
	}
	for index, state := range states {
		if err := fixture.insertRecoveryPoint(db, publicationPointSeed{
			ID: fmt.Sprintf("%032x", index+1), RepositoryID: firstRepositoryID,
			Semantics: "native_snapshot", State: state,
		}, now.Add(time.Duration(index)*time.Second)); err != nil {
			t.Fatalf("insert first repository native point in %s: %v", state, err)
		}
	}
	if err := fixture.insertRecoveryPoint(db, publicationPointSeed{
		ID: strings.Repeat("f", 32), RepositoryID: secondRepositoryID,
		Semantics: "native_snapshot", State: "committed",
	}, now.Add(time.Hour)); err != nil {
		t.Fatalf("insert second repository native point: %v", err)
	}

	migrateToBackupAssetVersion(t, migrator, backupAssetRsyncPublicationVersion)
	fixture.assertManagedHistoryLatch(t, db, "installation", "", now)
	fixture.assertManagedHistoryLatch(t, db, "repository", firstRepositoryID, now)
	fixture.assertManagedHistoryLatch(t, db, "repository", secondRepositoryID, now.Add(time.Hour))
	if got := fixture.managedHistoryLatchCount(t, db); got != 3 {
		t.Fatalf("managed history latch count=%d, want 3", got)
	}
}

func (fixture migrationFixture) test064AllowsPristineMutableHead(t *testing.T) {
	migrator, db := fixture.openAt(t, backupAssetPublicationVersion)
	now := time.Date(2026, 7, 15, 3, 4, 5, 0, time.UTC)
	repositoryID := strings.Repeat("3", 32)
	fixture.insertRepository(t, db, repositoryID, "rsync", now)
	if err := fixture.insertRecoveryPoint(db, publicationPointSeed{
		ID: strings.Repeat("4", 32), RepositoryID: repositoryID,
		Semantics: "mutable_head", State: "observed",
	}, now); err != nil {
		t.Fatalf("insert mutable head: %v", err)
	}

	migrateToBackupAssetVersion(t, migrator, backupAssetRsyncPublicationVersion)
	if got := fixture.managedHistoryLatchCount(t, db); got != 0 {
		t.Fatalf("pristine mutable head created %d managed history latches", got)
	}
}

func (fixture migrationFixture) test064ManagedTreeSourceUnique(t *testing.T) {
	_, db := fixture.openAt(t, backupAssetRsyncPublicationVersion)
	now := time.Date(2026, 7, 15, 3, 4, 5, 0, time.UTC)
	firstRepositoryID := strings.Repeat("5", 32)
	secondRepositoryID := strings.Repeat("6", 32)
	fingerprint := "managed-tree-source-fingerprint"
	fixture.insertRepository(t, db, firstRepositoryID, "rsync", now)
	fixture.insertRepository(t, db, secondRepositoryID, "rsync", now)
	fixture.mustInsertRecoveryPoint(t, db, publicationPointSeed{
		ID: strings.Repeat("7", 32), RepositoryID: firstRepositoryID,
		Semantics: "xirang_manifest", State: "preparing", SourceFingerprint: fingerprint,
	})
	if err := fixture.insertRecoveryPoint(db, publicationPointSeed{
		ID: strings.Repeat("8", 32), RepositoryID: firstRepositoryID,
		Semantics: "imported_baseline", State: "preparing", SourceFingerprint: fingerprint,
	}, now); err == nil {
		t.Fatal("same repository managed-tree source fingerprint unexpectedly succeeded")
	}
	fixture.mustInsertRecoveryPoint(t, db, publicationPointSeed{
		ID: strings.Repeat("9", 32), RepositoryID: secondRepositoryID,
		Semantics: "imported_baseline", State: "preparing", SourceFingerprint: fingerprint,
	})
	fixture.mustInsertRecoveryPoint(t, db, publicationPointSeed{
		ID: strings.Repeat("a", 32), RepositoryID: firstRepositoryID,
		Semantics: "xirang_manifest", State: "preparing",
	})
	fixture.mustInsertRecoveryPoint(t, db, publicationPointSeed{
		ID: strings.Repeat("b", 32), RepositoryID: firstRepositoryID,
		Semantics: "imported_baseline", State: "preparing",
	})
}

func (fixture migrationFixture) test064DownRejectsLatch(t *testing.T) {
	migrator, db := fixture.openAt(t, backupAssetRsyncPublicationVersion)
	now := time.Date(2026, 7, 15, 3, 4, 5, 0, time.UTC)
	fixture.insertManagedHistoryLatch(t, db, "installation", "", now)
	fixture.assertRsyncPublicationDownRejectedUnchanged(t, migrator, db)
}

func (fixture migrationFixture) test064DownRejectsManagedHistory(t *testing.T) {
	states := []string{
		"preparing", "verifying", "committed", "degraded", "expiring",
		"expired", "failed", "purge_blocked",
	}
	for index, state := range states {
		state := state
		t.Run("xirang_manifest_"+state, func(t *testing.T) {
			migrator, db := fixture.openAt(t, backupAssetRsyncPublicationVersion)
			now := time.Date(2026, 7, 15, 3, 4, 5, 0, time.UTC)
			repositoryID := fmt.Sprintf("%032x", index+101)
			fixture.insertRepository(t, db, repositoryID, "rsync", now)
			fixture.mustInsertRecoveryPoint(t, db, publicationPointSeed{
				ID: fmt.Sprintf("%032x", index+1), RepositoryID: repositoryID,
				Semantics: "xirang_manifest", State: state,
			})
			fixture.assertRsyncPublicationDownRejectedUnchanged(t, migrator, db)
		})
	}

	for index, publicationMode := range []string{"native_snapshot", "versioned_hardlink", "versioned_full_copy", "versioned_prefix", "native_object_versions"} {
		publicationMode := publicationMode
		t.Run("link_"+publicationMode, func(t *testing.T) {
			migrator, db := fixture.openAt(t, backupAssetRsyncPublicationVersion)
			now := time.Date(2026, 7, 15, 3, 4, 5, 0, time.UTC)
			repositoryID := fmt.Sprintf("%032x", index+201)
			taskID := int64(9800 + index*2)
			providerKind := "rsync"
			if publicationMode == "versioned_prefix" || publicationMode == "native_object_versions" {
				providerKind = "rclone"
			}
			fixture.insertRepository(t, db, repositoryID, providerKind, now)
			fixture.insertTaskAndRun(t, db, taskID, taskID+1, now)
			fixture.insertTaskLink(t, db, fmt.Sprintf("%032x", index+301), taskID, repositoryID, publicationMode, now)
			fixture.assertRsyncPublicationDownRejectedUnchanged(t, migrator, db)
		})
	}
}

func (fixture migrationFixture) test064DownRejectsPublicationAndParentLease(t *testing.T) {
	for index, holderType := range []string{"point_publication", "rsync_parent"} {
		holderType := holderType
		t.Run(holderType, func(t *testing.T) {
			migrator, db := fixture.openAt(t, backupAssetRsyncPublicationVersion)
			now := time.Date(2026, 7, 15, 3, 4, 5, 0, time.UTC)
			repositoryID := fmt.Sprintf("%032x", index+401)
			pointID := fmt.Sprintf("%032x", index+501)
			fixture.insertRepository(t, db, repositoryID, "rsync", now)
			fixture.mustInsertRecoveryPoint(t, db, publicationPointSeed{
				ID: pointID, RepositoryID: repositoryID, Semantics: "mutable_head", State: "observed",
			})
			fixture.insertPublicationLease(t, db, fmt.Sprintf("%032x", index+601), pointID, holderType, "active", now)
			fixture.assertRsyncPublicationDownRejectedUnchanged(t, migrator, db)
		})
	}
}

func (fixture migrationFixture) test064DownAllowsUnlinkedRcloneManagedLink(t *testing.T) {
	for index, publicationMode := range []string{"versioned_prefix", "native_object_versions"} {
		publicationMode := publicationMode
		t.Run(publicationMode, func(t *testing.T) {
			migrator, db := fixture.openAt(t, backupAssetRsyncPublicationVersion)
			now := time.Date(2026, 7, 16, 9, 0, 0, 0, time.UTC)
			repositoryID := fmt.Sprintf("%032x", index+701)
			linkID := fmt.Sprintf("%032x", index+801)
			taskID := int64(9900 + index*2)
			fixture.insertRepository(t, db, repositoryID, "rclone", now)
			fixture.insertTaskAndRun(t, db, taskID, taskID+1, now)
			fixture.insertTaskLink(t, db, linkID, taskID, repositoryID, publicationMode, now)
			fixture.mustExec(t, db, `UPDATE task_repository_links SET unlinked_at = ? WHERE id = ?`, now.Add(time.Minute), linkID)
			if err := migrator.Steps(-1); err != nil {
				t.Fatalf("000064 down rejected clean unlinked %s link: %v", publicationMode, err)
			}
			assertMigrationVersion(t, migrator, backupAssetPublicationVersion)
			fixture.assertRsyncPublicationContractAbsent(t, db)
		})
	}
}

func (fixture migrationFixture) test064DownIsAtomic(t *testing.T) {
	migrator, db := fixture.openAt(t, backupAssetRsyncPublicationVersion)
	now := time.Date(2026, 7, 15, 3, 4, 5, 0, time.UTC)
	repositoryID := strings.Repeat("c", 32)
	fixture.insertRepository(t, db, repositoryID, "rsync", now)
	fixture.mustInsertRecoveryPoint(t, db, publicationPointSeed{
		ID: strings.Repeat("d", 32), RepositoryID: repositoryID,
		Semantics: "imported_baseline", State: "failed", SourceFingerprint: "retained-managed-tree",
	})
	fixture.assertRsyncPublicationDownRejectedUnchanged(t, migrator, db)
}

type publicationPointSeed struct {
	ID                string
	RepositoryID      string
	TaskID            *int64
	TaskRunID         *int64
	Semantics         string
	State             string
	SourceFingerprint string
}

func (fixture migrationFixture) insertRepository(t *testing.T, db *sql.DB, id, providerKind string, now time.Time) {
	t.Helper()
	versionMode := "hardlink_tree"
	immutability := "xirang_managed"
	switch providerKind {
	case "restic":
		versionMode = "native_snapshot"
		immutability = "backend_versioned"
	case "rclone":
		versionMode = "versioned_prefix"
	}
	fixture.mustExec(t, db, `INSERT INTO backup_repositories
		(id, provider_kind, repository_identity, display_name, description, version_mode, status, capability_revision, capabilities_json, immutability_level, created_at, updated_at)
		VALUES (?, ?, ?, 'publication-repository', '', ?, 'online', 1, '{}', ?, ?, ?)`,
		id, providerKind, providerKind+"-identity-"+id, versionMode, immutability, now, now)
}

func (fixture migrationFixture) insertTaskAndRun(t *testing.T, db *sql.DB, taskID, runID int64, now time.Time) {
	t.Helper()
	fixture.mustExec(t, db, `INSERT INTO tasks (id, name, node_id, executor_type, status, created_at, updated_at)
		VALUES (?, ?, 1, 'local', 'idle', ?, ?)`, taskID, fmt.Sprintf("publication-task-%d", taskID), now, now)
	fixture.mustExec(t, db, `INSERT INTO task_runs (id, task_id, trigger_type, status, created_at, updated_at)
		VALUES (?, ?, 'manual', 'success', ?, ?)`, runID, taskID, now, now)
}

func (fixture migrationFixture) insertTaskLink(t *testing.T, db *sql.DB, id string, taskID int64, repositoryID, publicationMode string, now time.Time) {
	t.Helper()
	fixture.mustExec(t, db, `INSERT INTO task_repository_links
		(id, task_id, repository_id, task_name_snapshot, node_id_snapshot, node_name_snapshot, publication_mode, encrypted_legacy_locator, linked_at, created_at, updated_at)
		VALUES (?, ?, ?, 'publication-task', 1, 'node-a', ?, '', ?, ?, ?)`, id, taskID, repositoryID, publicationMode, now, now, now)
}

func (fixture migrationFixture) mustInsertRecoveryPoint(t *testing.T, db *sql.DB, seed publicationPointSeed) {
	t.Helper()
	if err := fixture.insertRecoveryPoint(db, seed, time.Date(2026, 7, 14, 3, 4, 5, 0, time.UTC)); err != nil {
		t.Fatalf("insert recovery point %+v: %v", seed, err)
	}
}

func (fixture migrationFixture) insertRecoveryPoint(db *sql.DB, seed publicationPointSeed, now time.Time) error {
	var taskID any
	if seed.TaskID != nil {
		taskID = *seed.TaskID
	}
	var taskRunID any
	if seed.TaskRunID != nil {
		taskRunID = *seed.TaskRunID
	}
	var observedAt any
	immutability := "backend_versioned"
	if seed.Semantics == "mutable_head" {
		observedAt = now
		immutability = "mutable"
	}
	_, err := db.Exec(fixture.bind(`INSERT INTO recovery_points
		(id, repository_id, producing_task_id, producing_task_run_id, producing_task_name_snapshot, producing_node_id_snapshot, producing_node_name_snapshot,
		lineage_json, encrypted_provider_locator, encrypted_rollback_locator, semantics, state, observed_at, source_fingerprint,
		manifest_digest_algorithm, manifest_digest, entry_count, logical_bytes, consistency_json, fidelity_json, capability_revision, capabilities_json,
		immutability_level, physical_availability, hold_state, created_at, updated_at)
		VALUES (?, ?, ?, ?, '', 0, '', '{}', '', '', ?, ?, ?, ?, 'sha256', '', 0, 0, '{}', '{}', 1, '{}', ?, 'online', 'none', ?, ?)`),
		seed.ID, seed.RepositoryID, taskID, taskRunID, seed.Semantics, seed.State, observedAt, seed.SourceFingerprint, immutability, now, now)
	return err
}

func (fixture migrationFixture) insertPublicationLease(t *testing.T, db *sql.DB, id, recoveryPointID, holderType, status string, now time.Time) {
	t.Helper()
	fixture.mustExec(t, db, `INSERT INTO recovery_point_leases
		(id, recovery_point_id, holder_type, owner_id, attempt_id, fence_token, status, lease_expires_at, absolute_deadline, last_heartbeat_at, created_at, updated_at)
		VALUES (?, ?, ?, 'publication-owner', 'publication-attempt', 'publication-fence', ?, ?, ?, ?, ?, ?)`,
		id, recoveryPointID, holderType, status, now.Add(time.Minute), now.Add(time.Hour), now, now, now)
}

func (fixture migrationFixture) mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(fixture.bind(query), args...); err != nil {
		t.Fatalf("execute %s migration assertion query: %v", fixture.engine, err)
	}
}

func (fixture migrationFixture) bind(query string) string {
	if fixture.engine != "postgres" {
		return query
	}
	var builder strings.Builder
	placeholder := 1
	for _, character := range query {
		if character == '?' {
			builder.WriteString("$")
			builder.WriteString(strconv.Itoa(placeholder))
			placeholder++
			continue
		}
		builder.WriteRune(character)
	}
	return builder.String()
}

type publicationMigrationSnapshot struct {
	version                      uint
	dirty                        bool
	taskRepositoryLinkDefinition string
	leaseDefinition              string
	producingRunIndexDefinition  string
	nativeSourceIndexDefinition  string
	linkRows                     []string
	pointRows                    []string
	leaseRows                    []string
}

type rsyncPublicationMigrationSnapshot struct {
	version                          uint
	dirty                            bool
	latchDefinition                  string
	installationLatchIndexDefinition string
	repositoryLatchIndexDefinition   string
	managedTreeIndexDefinition       string
	latchRows                        []string
	linkRows                         []string
	pointRows                        []string
	leaseRows                        []string
}

func (fixture migrationFixture) assertPublicationDownRejectedUnchanged(t *testing.T, migrator *migrate.Migrate, db *sql.DB) {
	t.Helper()
	before := fixture.capturePublicationMigrationSnapshot(t, migrator, db)
	if err := fixture.executePublicationDown(db); err == nil {
		t.Fatal("000063 down unexpectedly succeeded while publication contract data remains")
	}
	after := fixture.capturePublicationMigrationSnapshot(t, migrator, db)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("rejected 000063 down changed migration state\nbefore=%+v\nafter=%+v", before, after)
	}
}

func (fixture migrationFixture) assertRsyncPublicationDownRejectedUnchanged(t *testing.T, migrator *migrate.Migrate, db *sql.DB) {
	t.Helper()
	before := fixture.captureRsyncPublicationMigrationSnapshot(t, migrator, db)
	if err := fixture.executeRsyncPublicationDown(db); err == nil {
		t.Fatal("000064 down unexpectedly succeeded while managed history remains")
	}
	after := fixture.captureRsyncPublicationMigrationSnapshot(t, migrator, db)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("rejected 000064 down changed migration state\nbefore=%+v\nafter=%+v", before, after)
	}
}

func (fixture migrationFixture) executePublicationDown(db *sql.DB) error {
	path := "migrations/sqlite/000063_backup_asset_publication_contract.down.sql"
	migrationFS := sqliteMigrationsFS
	if fixture.engine == "postgres" {
		path = "migrations/postgres/000063_backup_asset_publication_contract.down.sql"
		migrationFS = postgresMigrationsFS
	}
	script, err := migrationFS.ReadFile(path)
	if err != nil {
		return err
	}
	_, err = db.Exec(string(script))
	if err != nil {
		_, _ = db.Exec("ROLLBACK")
	}
	return err
}

func (fixture migrationFixture) executeRsyncPublicationDown(db *sql.DB) error {
	path := "migrations/sqlite/000064_backup_asset_rsync_publication_contract.down.sql"
	migrationFS := sqliteMigrationsFS
	if fixture.engine == "postgres" {
		path = "migrations/postgres/000064_backup_asset_rsync_publication_contract.down.sql"
		migrationFS = postgresMigrationsFS
	}
	script, err := migrationFS.ReadFile(path)
	if err != nil {
		return err
	}
	_, err = db.Exec(string(script))
	if err != nil {
		_, _ = db.Exec("ROLLBACK")
	}
	return err
}

func (fixture migrationFixture) capturePublicationMigrationSnapshot(t *testing.T, migrator *migrate.Migrate, db *sql.DB) publicationMigrationSnapshot {
	t.Helper()
	version, dirty, err := migrator.Version()
	if err != nil {
		t.Fatalf("read %s migration version: %v", fixture.engine, err)
	}
	return publicationMigrationSnapshot{
		version:                      version,
		dirty:                        dirty,
		taskRepositoryLinkDefinition: fixture.tableDefinition(t, db, "task_repository_links"),
		leaseDefinition:              fixture.tableDefinition(t, db, "recovery_point_leases"),
		producingRunIndexDefinition:  fixture.indexDefinition(t, db, "idx_recovery_points_producing_task_run_unique"),
		nativeSourceIndexDefinition:  fixture.indexDefinition(t, db, "idx_recovery_points_native_source_unique"),
		linkRows: fixture.rowStrings(t, db, `SELECT id || '|' || repository_id || '|' || publication_mode
			FROM task_repository_links ORDER BY id`),
		pointRows: fixture.rowStrings(t, db, `SELECT id || '|' || repository_id || '|' || semantics || '|' || state || '|' ||
			COALESCE(CAST(producing_task_run_id AS TEXT), '<null>') || '|' || source_fingerprint
			FROM recovery_points ORDER BY id`),
		leaseRows: fixture.rowStrings(t, db, `SELECT id || '|' || recovery_point_id || '|' || holder_type || '|' || status
			FROM recovery_point_leases ORDER BY id`),
	}
}

func (fixture migrationFixture) captureRsyncPublicationMigrationSnapshot(t *testing.T, migrator *migrate.Migrate, db *sql.DB) rsyncPublicationMigrationSnapshot {
	t.Helper()
	version, dirty, err := migrator.Version()
	if err != nil {
		t.Fatalf("read %s migration version: %v", fixture.engine, err)
	}
	return rsyncPublicationMigrationSnapshot{
		version:                          version,
		dirty:                            dirty,
		latchDefinition:                  fixture.tableDefinition(t, db, "backup_asset_managed_history_latches"),
		installationLatchIndexDefinition: fixture.indexDefinition(t, db, "idx_backup_asset_managed_history_latches_installation_unique"),
		repositoryLatchIndexDefinition:   fixture.indexDefinition(t, db, "idx_backup_asset_managed_history_latches_repository_unique"),
		managedTreeIndexDefinition:       fixture.indexDefinition(t, db, "idx_recovery_points_managed_tree_source_unique"),
		latchRows: fixture.rowStrings(t, db, `SELECT id || '|' || scope || '|' || COALESCE(repository_id, '<null>') || '|' ||
			repository_identity_digest || '|' || first_semantics || '|' || first_origin || '|' || CAST(first_seen_at AS TEXT)
			FROM backup_asset_managed_history_latches ORDER BY id`),
		linkRows: fixture.rowStrings(t, db, `SELECT id || '|' || repository_id || '|' || publication_mode
			FROM task_repository_links ORDER BY id`),
		pointRows: fixture.rowStrings(t, db, `SELECT id || '|' || repository_id || '|' || semantics || '|' || state || '|' ||
			COALESCE(CAST(producing_task_run_id AS TEXT), '<null>') || '|' || source_fingerprint
			FROM recovery_points ORDER BY id`),
		leaseRows: fixture.rowStrings(t, db, `SELECT id || '|' || recovery_point_id || '|' || holder_type || '|' || status
			FROM recovery_point_leases ORDER BY id`),
	}
}

func (fixture migrationFixture) tableDefinition(t *testing.T, db *sql.DB, table string) string {
	t.Helper()
	var definition string
	var err error
	if fixture.engine == "sqlite" {
		err = db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&definition)
	} else {
		query := `SELECT COALESCE(string_agg(pg_get_constraintdef(constraint_row.oid), ' ' ORDER BY constraint_row.conname), '')
			FROM pg_constraint AS constraint_row
			JOIN pg_class AS relation ON relation.oid = constraint_row.conrelid
			JOIN pg_namespace AS namespace ON namespace.oid = relation.relnamespace
			WHERE namespace.nspname = current_schema()
			  AND relation.relname = ?
			  AND constraint_row.contype = 'c'`
		err = db.QueryRow(fixture.bind(query), table).Scan(&definition)
	}
	if err != nil {
		t.Fatalf("load %s table definition for %s: %v", fixture.engine, table, err)
	}
	return strings.Join(strings.Fields(strings.ToLower(definition)), " ")
}

func (fixture migrationFixture) columnIsNotNull(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()
	if fixture.engine == "sqlite" {
		var notNull int
		if err := db.QueryRow(`SELECT "notnull" FROM pragma_table_info(?) WHERE name = ?`, table, column).Scan(&notNull); err != nil {
			t.Fatalf("load SQLite nullability for %s.%s: %v", table, column, err)
		}
		return notNull == 1
	}

	var notNull bool
	query := `SELECT is_nullable = 'NO'
		FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = ? AND column_name = ?`
	if err := db.QueryRow(fixture.bind(query), table, column).Scan(&notNull); err != nil {
		t.Fatalf("load PostgreSQL nullability for %s.%s: %v", table, column, err)
	}
	return notNull
}

func (fixture migrationFixture) indexDefinition(t *testing.T, db *sql.DB, index string) string {
	t.Helper()
	var definition string
	var err error
	if fixture.engine == "sqlite" {
		err = db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'index' AND name = ?`, index).Scan(&definition)
	} else {
		query := `SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ?`
		err = db.QueryRow(fixture.bind(query), index).Scan(&definition)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ""
	}
	if err != nil {
		t.Fatalf("load %s index definition for %s: %v", fixture.engine, index, err)
	}
	return strings.Join(strings.Fields(strings.ToLower(definition)), " ")
}

func (fixture migrationFixture) rowStrings(t *testing.T, db *sql.DB, query string) []string {
	t.Helper()
	rows, err := db.Query(query)
	if err != nil {
		t.Fatalf("query %s migration snapshot rows: %v", fixture.engine, err)
	}
	defer closeMigrationRows(t, rows)
	var values []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			t.Fatalf("scan %s migration snapshot row: %v", fixture.engine, err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate %s migration snapshot rows: %v", fixture.engine, err)
	}
	return values
}

func (fixture migrationFixture) assertPublicationContractSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	if definition := fixture.tableDefinition(t, db, "task_repository_links"); !strings.Contains(definition, "native_snapshot") {
		t.Fatalf("%s task_repository_links check omits native_snapshot: %s", fixture.engine, definition)
	}
	if definition := fixture.tableDefinition(t, db, "recovery_point_leases"); !strings.Contains(definition, "point_publication") {
		t.Fatalf("%s recovery_point_leases check omits point_publication: %s", fixture.engine, definition)
	}
	for index, fragments := range map[string][]string{
		"idx_recovery_points_producing_task_run_unique": {"unique", "producing_task_run_id", "where", "is not null"},
		"idx_recovery_points_native_source_unique":      {"unique", "repository_id", "source_fingerprint", "native_snapshot"},
	} {
		definition := fixture.indexDefinition(t, db, index)
		if definition == "" {
			t.Fatalf("%s publication index %s is missing", fixture.engine, index)
		}
		for _, fragment := range fragments {
			if !strings.Contains(definition, fragment) {
				t.Fatalf("%s publication index %s omits %q: %s", fixture.engine, index, fragment, definition)
			}
		}
	}
}

func (fixture migrationFixture) assertRsyncPublicationContractSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	if !databaseTableExists(t, db, fixture.engine, "backup_asset_managed_history_latches") {
		t.Fatalf("%s managed-history latch table is missing", fixture.engine)
	}
	for _, column := range []string{
		"id", "scope", "repository_id", "repository_identity_digest", "first_semantics", "first_origin",
		"first_seen_at", "created_at", "updated_at",
	} {
		if !databaseColumnExists(t, db, fixture.engine, "backup_asset_managed_history_latches", column) {
			t.Fatalf("%s managed-history latch column %s is missing", fixture.engine, column)
		}
	}
	definition := fixture.tableDefinition(t, db, "backup_asset_managed_history_latches")
	for _, fragment := range []string{"check", "installation", "repository", "repository_id"} {
		if !strings.Contains(definition, fragment) {
			t.Fatalf("%s managed-history latch constraint omits %q: %s", fixture.engine, fragment, definition)
		}
	}
	fixture.assertManagedHistoryLatchHasNoRepositoryForeignKey(t, db)
	for index, fragments := range map[string][]string{
		"idx_backup_asset_managed_history_latches_installation_unique": {"unique", "scope", "where", "installation"},
		"idx_backup_asset_managed_history_latches_repository_unique":   {"unique", "repository_id", "where", "repository"},
		"idx_recovery_points_managed_tree_source_unique":               {"unique", "repository_id", "source_fingerprint", "where", "xirang_manifest", "imported_baseline"},
	} {
		indexDefinition := fixture.indexDefinition(t, db, index)
		if indexDefinition == "" {
			t.Fatalf("%s Rsync publication index %s is missing", fixture.engine, index)
		}
		for _, fragment := range fragments {
			if !strings.Contains(indexDefinition, fragment) {
				t.Fatalf("%s Rsync publication index %s omits %q: %s", fixture.engine, index, fragment, indexDefinition)
			}
		}
	}
}

func (fixture migrationFixture) assertRsyncPublicationContractAbsent(t *testing.T, db *sql.DB) {
	t.Helper()
	if databaseTableExists(t, db, fixture.engine, "backup_asset_managed_history_latches") {
		t.Fatalf("%s managed-history latch table remains after 000064 down", fixture.engine)
	}
	for _, index := range []string{
		"idx_backup_asset_managed_history_latches_installation_unique",
		"idx_backup_asset_managed_history_latches_repository_unique",
		"idx_recovery_points_managed_tree_source_unique",
	} {
		if definition := fixture.indexDefinition(t, db, index); definition != "" {
			t.Fatalf("%s Rsync publication index %s remains after down: %s", fixture.engine, index, definition)
		}
	}
}

func (fixture migrationFixture) assertManagedHistoryLatchHasNoRepositoryForeignKey(t *testing.T, db *sql.DB) {
	t.Helper()
	var count int
	var err error
	if fixture.engine == "sqlite" {
		rows, queryErr := db.Query(`PRAGMA foreign_key_list('backup_asset_managed_history_latches')`)
		if queryErr != nil {
			t.Fatalf("inspect SQLite managed-history latch foreign keys: %v", queryErr)
		}
		defer closeMigrationRows(t, rows)
		for rows.Next() {
			var id, seq int
			var table, from, to, onUpdate, onDelete, match string
			if scanErr := rows.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match); scanErr != nil {
				t.Fatalf("scan SQLite managed-history latch foreign key: %v", scanErr)
			}
			if from == "repository_id" {
				count++
			}
		}
		if rowErr := rows.Err(); rowErr != nil {
			t.Fatalf("iterate SQLite managed-history latch foreign keys: %v", rowErr)
		}
	} else {
		err = db.QueryRow(`SELECT COUNT(*)
			FROM pg_constraint AS constraint_row
			JOIN pg_class AS relation ON relation.oid = constraint_row.conrelid
			JOIN pg_namespace AS namespace ON namespace.oid = relation.relnamespace
			WHERE namespace.nspname = current_schema()
			  AND relation.relname = 'backup_asset_managed_history_latches'
			  AND constraint_row.contype = 'f'`).Scan(&count)
		if err != nil {
			t.Fatalf("inspect PostgreSQL managed-history latch foreign keys: %v", err)
		}
	}
	if count != 0 {
		t.Fatalf("%s managed-history latch has %d repository foreign keys, want none", fixture.engine, count)
	}
}

func (fixture migrationFixture) managedHistoryLatchCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM backup_asset_managed_history_latches`).Scan(&count); err != nil {
		t.Fatalf("count %s managed-history latches: %v", fixture.engine, err)
	}
	return count
}

func (fixture migrationFixture) assertManagedHistoryLatch(t *testing.T, db *sql.DB, scope, repositoryID string, wantFirstSeenAt time.Time) {
	t.Helper()
	var gotScope, identityDigest, firstSemantics, firstOrigin string
	var gotRepositoryID sql.NullString
	var firstSeenAt time.Time
	query := `SELECT scope, repository_id, repository_identity_digest, first_semantics, first_origin, first_seen_at
		FROM backup_asset_managed_history_latches
		WHERE scope = ? AND ((? = '' AND repository_id IS NULL) OR repository_id = ?)`
	if err := db.QueryRow(fixture.bind(query), scope, repositoryID, repositoryID).Scan(
		&gotScope, &gotRepositoryID, &identityDigest, &firstSemantics, &firstOrigin, &firstSeenAt,
	); err != nil {
		t.Fatalf("read %s %s managed-history latch for repository %q: %v", fixture.engine, scope, repositoryID, err)
	}
	if gotScope != scope || firstSemantics != "native_snapshot" || firstOrigin != "migration_backfill" {
		t.Fatalf("managed-history latch got scope=%q semantics=%q origin=%q, want %q/native_snapshot/migration_backfill", gotScope, firstSemantics, firstOrigin, scope)
	}
	if scope == "installation" {
		if gotRepositoryID.Valid || identityDigest != "" {
			t.Fatalf("installation latch has repository_id=%q valid=%t identity_digest=%q", gotRepositoryID.String, gotRepositoryID.Valid, identityDigest)
		}
	} else if !gotRepositoryID.Valid || gotRepositoryID.String != repositoryID || len(identityDigest) != 64 {
		t.Fatalf("repository latch got repository_id=%q valid=%t identity_digest=%q, want %q and a 64-char opaque digest", gotRepositoryID.String, gotRepositoryID.Valid, identityDigest, repositoryID)
	}
	if !firstSeenAt.Equal(wantFirstSeenAt) || firstSeenAt.Location() != time.UTC {
		t.Fatalf("managed-history latch first_seen_at=%s (%s), want %s UTC", firstSeenAt.Format(time.RFC3339), firstSeenAt.Location(), wantFirstSeenAt.Format(time.RFC3339))
	}
}

func (fixture migrationFixture) insertManagedHistoryLatch(t *testing.T, db *sql.DB, scope, repositoryID string, now time.Time) {
	t.Helper()
	id := "managed-history-installation"
	var repositoryIDValue any
	identityDigest := ""
	if scope == "repository" {
		id = "managed-history-repository-" + repositoryID
		repositoryIDValue = repositoryID
		identityDigest = strings.Repeat("0", 32) + repositoryID
	}
	fixture.mustExec(t, db, `INSERT INTO backup_asset_managed_history_latches
		(id, scope, repository_id, repository_identity_digest, first_semantics, first_origin, first_seen_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'xirang_manifest', 'provider_commit', ?, ?, ?)`,
		id, scope, repositoryIDValue, identityDigest, now, now, now)
}

func (fixture migrationFixture) assertPublicationContractAbsent(t *testing.T, db *sql.DB) {
	t.Helper()
	if definition := fixture.tableDefinition(t, db, "task_repository_links"); strings.Contains(definition, "native_snapshot") {
		t.Fatalf("%s task_repository_links still permits native_snapshot after down: %s", fixture.engine, definition)
	}
	if definition := fixture.tableDefinition(t, db, "recovery_point_leases"); strings.Contains(definition, "point_publication") {
		t.Fatalf("%s recovery_point_leases still permits point_publication after down: %s", fixture.engine, definition)
	}
	for _, index := range []string{
		"idx_recovery_points_producing_task_run_unique",
		"idx_recovery_points_native_source_unique",
	} {
		if definition := fixture.indexDefinition(t, db, index); definition != "" {
			t.Fatalf("%s publication index %s remains after down: %s", fixture.engine, index, definition)
		}
	}
}

func foundationModels() map[string]any {
	return map[string]any{
		"backup_repositories":            model.BackupRepository{},
		"repository_access_bindings":     model.RepositoryAccessBinding{},
		"task_repository_links":          model.TaskRepositoryLink{},
		"recovery_points":                model.RecoveryPoint{},
		"recovery_point_manifests":       model.RecoveryPointManifest{},
		"catalog_generations":            model.CatalogGeneration{},
		"catalog_entries":                model.CatalogEntry{},
		"wrapped_domain_keys":            model.WrappedDomainKey{},
		"recovery_point_leases":          model.RecoveryPointLease{},
		"backup_asset_audit_checkpoints": model.BackupAssetAuditCheckpoint{},
		"backup_asset_audit_events":      model.BackupAssetAuditEvent{},
	}
}

func assertFoundationModelParity(t *testing.T, db *sql.DB, engine string) {
	t.Helper()
	for table, persistentModel := range foundationModels() {
		want := gormColumnNames(t, persistentModel)
		if table == "recovery_points" {
			want = filterStrings(want, "point_revision")
		}
		var got []string
		if engine == "sqlite" {
			got = sqliteColumnNames(t, db, table)
		} else {
			got = postgresColumnNames(t, db, table)
		}
		sort.Strings(got)
		sort.Strings(want)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("%s %s columns mismatch\n got: %v\nwant: %v", engine, table, got, want)
		}
	}
}

func filterStrings(values []string, excluded string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != excluded {
			result = append(result, value)
		}
	}
	return result
}

func assertRsyncPublicationModelParity(t *testing.T, db *sql.DB, engine string) {
	t.Helper()
	want := gormColumnNames(t, model.BackupAssetManagedHistoryLatch{})
	var got []string
	if engine == "sqlite" {
		got = sqliteColumnNames(t, db, "backup_asset_managed_history_latches")
	} else {
		got = postgresColumnNames(t, db, "backup_asset_managed_history_latches")
	}
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("%s backup_asset_managed_history_latches columns mismatch\n got: %v\nwant: %v", engine, got, want)
	}
}

func gormColumnNames(t *testing.T, value any) []string {
	t.Helper()
	parsed, err := schema.Parse(value, &sync.Map{}, schema.NamingStrategy{})
	if err != nil {
		t.Fatalf("parse GORM model %T: %v", value, err)
	}
	columns := make([]string, 0, len(parsed.Fields))
	for _, field := range parsed.Fields {
		if field.DBName != "" {
			columns = append(columns, field.DBName)
		}
	}
	return columns
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func newSQLiteBackupAssetMigrator(t *testing.T) (*migrate.Migrate, *sql.DB) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "backup-asset-migration.db")
	db, err := sql.Open("sqlite3", buildSQLiteDSN(dbPath))
	if err != nil {
		t.Fatalf("open SQLite migration database: %v", err)
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		t.Fatalf("ping SQLite migration database: %v", err)
	}

	source, err := iofs.New(sqliteMigrationsFS, "migrations/sqlite")
	if err != nil {
		t.Fatalf("open embedded SQLite migrations: %v", err)
	}
	driver, err := sqlitemigrate.WithInstance(db, &sqlitemigrate.Config{})
	if err != nil {
		t.Fatalf("create SQLite migration driver: %v", err)
	}
	migrator, err := migrate.NewWithInstance("iofs", source, "sqlite3", driver)
	if err != nil {
		t.Fatalf("create SQLite migrator: %v", err)
	}
	t.Cleanup(func() {
		_, _ = migrator.Close()
	})
	return migrator, db
}

func newPostgresBackupAssetMigrator(t *testing.T, dsn string) (*migrate.Migrate, *sql.DB) {
	t.Helper()
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.Scheme == "" {
		t.Fatalf("TEST_POSTGRES_DSN must be a PostgreSQL URL: %v", err)
	}
	baseDB, err := openPostgresSQLDB(dsn)
	if err != nil {
		t.Fatalf("open base PostgreSQL database: %v", err)
	}
	if err := baseDB.Ping(); err != nil {
		t.Fatalf("ping base PostgreSQL database: %v", err)
	}
	schema := fmt.Sprintf("xirang_backup_asset_%d", time.Now().UnixNano())
	if _, err := baseDB.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatalf("create PostgreSQL test schema: %v", err)
	}

	query := parsed.Query()
	query.Set("search_path", schema)
	query.Set("timezone", "UTC")
	parsed.RawQuery = query.Encode()
	db, err := openPostgresSQLDB(parsed.String())
	if err != nil {
		t.Fatalf("open scoped PostgreSQL database: %v", err)
	}
	if err := db.Ping(); err != nil {
		t.Fatalf("ping scoped PostgreSQL database: %v", err)
	}
	source, err := iofs.New(postgresMigrationsFS, "migrations/postgres")
	if err != nil {
		t.Fatalf("open embedded PostgreSQL migrations: %v", err)
	}
	driver, err := pgxmigrate.WithInstance(db, &pgxmigrate.Config{})
	if err != nil {
		t.Fatalf("create PostgreSQL migration driver: %v", err)
	}
	migrator, err := migrate.NewWithInstance("iofs", source, "pgx5", driver)
	if err != nil {
		t.Fatalf("create PostgreSQL migrator: %v", err)
	}
	t.Cleanup(func() {
		_, _ = migrator.Close()
		_, _ = baseDB.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`)
		_ = baseDB.Close()
	})
	return migrator, db
}

func migrateToBackupAssetFoundation(t *testing.T, migrator *migrate.Migrate) {
	t.Helper()
	migrateToBackupAssetVersion(t, migrator, backupAssetMigrationVersion)
}

func migrateToBackupAssetVersion(t *testing.T, migrator *migrate.Migrate, version uint) {
	t.Helper()
	if err := migrator.Migrate(version); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		t.Fatalf("migrate to %05d: %v", version, err)
	}
}

func assertMigrationVersion(t *testing.T, migrator *migrate.Migrate, want uint) {
	t.Helper()
	got, dirty, err := migrator.Version()
	if err != nil {
		t.Fatalf("read migration version: %v", err)
	}
	if got != want || dirty {
		t.Fatalf("migration version got=%d dirty=%v, want=%d clean", got, dirty, want)
	}
}

func assertFoundationSchemaPresent(t *testing.T, db *sql.DB, engine string) {
	t.Helper()
	for _, table := range backupAssetFoundationTables {
		if !databaseTableExists(t, db, engine, table) {
			t.Fatalf("%s table %s is missing after 000062", engine, table)
		}
	}
	if !databaseColumnExists(t, db, engine, "tasks", "archived_at") {
		t.Fatalf("%s tasks.archived_at is missing after 000062", engine)
	}
}

func assertFoundationSchemaAbsent(t *testing.T, db *sql.DB, engine string) {
	t.Helper()
	for _, table := range backupAssetFoundationTables {
		if databaseTableExists(t, db, engine, table) {
			t.Fatalf("%s table %s remains after down to 000061", engine, table)
		}
	}
	if databaseColumnExists(t, db, engine, "tasks", "archived_at") {
		t.Fatalf("%s tasks.archived_at remains after down to 000061", engine)
	}
}

func assertFoundationIndexesPresent(t *testing.T, db *sql.DB, engine string) {
	t.Helper()
	for _, index := range backupAssetFoundationIndexNames {
		var count int
		var err error
		if engine == "sqlite" {
			err = db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, index).Scan(&count)
		} else {
			err = db.QueryRow(`SELECT COUNT(*) FROM pg_indexes WHERE schemaname = current_schema() AND indexname = $1`, index).Scan(&count)
		}
		if err != nil {
			t.Fatalf("check %s index %s: %v", engine, index, err)
		}
		if count != 1 {
			t.Fatalf("%s index %s count=%d, want 1", engine, index, count)
		}
	}
}

func assertFoundationPartialUniqueIndexes(t *testing.T, db *sql.DB, engine string) {
	t.Helper()
	for index, fragments := range backupAssetFoundationPartialUniqueIndexFragments {
		var definition string
		var err error
		if engine == "sqlite" {
			err = db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'index' AND name = ?`, index).Scan(&definition)
		} else {
			err = db.QueryRow(`SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND indexname = $1`, index).Scan(&definition)
		}
		if err != nil {
			t.Fatalf("load %s partial unique index %s: %v", engine, index, err)
		}
		lower := strings.ToLower(definition)
		if !strings.Contains(lower, "unique index") {
			t.Fatalf("%s index %s is not unique: %s", engine, index, definition)
		}
		for _, fragment := range fragments {
			if !strings.Contains(lower, strings.ToLower(fragment)) {
				t.Fatalf("%s index %s omits %q: %s", engine, index, fragment, definition)
			}
		}
	}
}

func assertFoundationChecksPresent(t *testing.T, db *sql.DB, engine string) {
	t.Helper()
	for table, fragments := range backupAssetFoundationCheckFragments {
		var definition string
		var err error
		if engine == "sqlite" {
			err = db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&definition)
		} else {
			err = db.QueryRow(`
			SELECT COALESCE(string_agg(pg_get_constraintdef(constraint_row.oid), ' '), '')
			FROM pg_constraint AS constraint_row
			JOIN pg_class AS relation ON relation.oid = constraint_row.conrelid
			JOIN pg_namespace AS namespace ON namespace.oid = relation.relnamespace
			WHERE namespace.nspname = current_schema()
			  AND relation.relname = $1
			  AND constraint_row.contype = 'c'`, table).Scan(&definition)
		}
		if err != nil {
			t.Fatalf("load %s CHECK definitions for %s: %v", engine, table, err)
		}
		lower := strings.ToLower(definition)
		if !strings.Contains(lower, "check") {
			t.Fatalf("%s table %s has no CHECK definition: %s", engine, table, definition)
		}
		for _, fragment := range fragments {
			if !strings.Contains(lower, strings.ToLower(fragment)) {
				t.Fatalf("%s table %s CHECK definitions omit %q: %s", engine, table, fragment, definition)
			}
		}
	}
}

func assertPostgresForeignKeyAction(t *testing.T, db *sql.DB, table, column, targetTable, wantDelete string) {
	t.Helper()
	var definition string
	err := db.QueryRow(`
		SELECT pg_get_constraintdef(constraint_row.oid)
		FROM pg_constraint AS constraint_row
		JOIN pg_class AS relation ON relation.oid = constraint_row.conrelid
		JOIN pg_namespace AS namespace ON namespace.oid = relation.relnamespace
		JOIN pg_class AS target_relation ON target_relation.oid = constraint_row.confrelid
		JOIN pg_attribute AS attribute
		  ON attribute.attrelid = relation.oid
		 AND attribute.attnum = ANY(constraint_row.conkey)
		WHERE namespace.nspname = current_schema()
		  AND relation.relname = $1
		  AND attribute.attname = $2
		  AND target_relation.relname = $3
		  AND constraint_row.contype = 'f'
		LIMIT 1`, table, column, targetTable).Scan(&definition)
	if err != nil {
		t.Fatalf("load PostgreSQL foreign key for %s.%s: %v", table, column, err)
	}
	lower := strings.ToLower(definition)
	if !strings.Contains(lower, "references "+strings.ToLower(targetTable)+"(") ||
		!strings.Contains(lower, "on delete "+strings.ToLower(wantDelete)) {
		t.Fatalf("PostgreSQL %s.%s FK definition=%q, want target=%s on_delete=%s", table, column, definition, targetTable, wantDelete)
	}
}

func assertPostgresUTCRoundTripAndNoTimeDefaults(t *testing.T, db *sql.DB) {
	t.Helper()
	var sessionTimezone string
	if err := db.QueryRow(`SHOW TIME ZONE`).Scan(&sessionTimezone); err != nil {
		t.Fatalf("read PostgreSQL session timezone: %v", err)
	}
	if !strings.EqualFold(sessionTimezone, "UTC") && !strings.EqualFold(sessionTimezone, "Etc/UTC") {
		t.Fatalf("PostgreSQL session timezone=%q, want UTC", sessionTimezone)
	}
	now := time.Date(2026, 7, 13, 3, 4, 5, 0, time.UTC)
	repositoryID := strings.Repeat("8", 32)
	if _, err := db.Exec(`INSERT INTO backup_repositories
		(id, provider_kind, display_name, description, version_mode, status, capability_revision, capabilities_json, immutability_level, created_at, updated_at)
		VALUES ($1, 'rsync', 'foundation-repository', '', 'hardlink_tree', 'online', 1, '{}', 'xirang_managed', $2, $2)`, repositoryID, now); err != nil {
		t.Fatalf("insert PostgreSQL UTC repository: %v", err)
	}
	var createdAt time.Time
	if err := db.QueryRow(`SELECT created_at FROM backup_repositories WHERE id = $1`, repositoryID).Scan(&createdAt); err != nil {
		t.Fatalf("scan PostgreSQL UTC created_at: %v", err)
	}
	if !createdAt.Equal(now) || createdAt.UTC().Format(time.RFC3339) != "2026-07-13T03:04:05Z" {
		t.Fatalf("PostgreSQL UTC timestamp drifted: %s (%s)", createdAt.Format(time.RFC3339), createdAt.Location())
	}

	for _, table := range backupAssetFoundationTables {
		rows, err := db.Query(`
			SELECT column_name, column_default
			FROM information_schema.columns
			WHERE table_schema = current_schema()
			  AND table_name = $1
			  AND data_type = 'timestamp with time zone'`, table)
		if err != nil {
			t.Fatalf("inspect PostgreSQL time columns for %s: %v", table, err)
		}
		for rows.Next() {
			var column string
			var defaultValue sql.NullString
			if err := rows.Scan(&column, &defaultValue); err != nil {
				_ = rows.Close()
				t.Fatalf("scan PostgreSQL time column for %s: %v", table, err)
			}
			if defaultValue.Valid {
				_ = rows.Close()
				t.Fatalf("PostgreSQL %s.%s has DB-side time default %q", table, column, defaultValue.String)
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			t.Fatalf("iterate PostgreSQL time columns for %s: %v", table, err)
		}
		if err := rows.Close(); err != nil {
			t.Fatalf("close PostgreSQL time columns for %s: %v", table, err)
		}
	}
	var archivedDefault sql.NullString
	if err := db.QueryRow(`
		SELECT column_default
		FROM information_schema.columns
		WHERE table_schema = current_schema()
		  AND table_name = 'tasks'
		  AND column_name = 'archived_at'`).Scan(&archivedDefault); err != nil {
		t.Fatalf("inspect PostgreSQL tasks.archived_at default: %v", err)
	}
	if archivedDefault.Valid {
		t.Fatalf("PostgreSQL tasks.archived_at has DB-side time default %q", archivedDefault.String)
	}
}

func databaseTableExists(t *testing.T, db *sql.DB, engine, table string) bool {
	t.Helper()
	var count int
	var err error
	if engine == "sqlite" {
		err = db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count)
	} else {
		err = db.QueryRow(`SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = $1`, table).Scan(&count)
	}
	if err != nil {
		t.Fatalf("check %s table %s: %v", engine, table, err)
	}
	return count == 1
}

func databaseColumnExists(t *testing.T, db *sql.DB, engine, table, column string) bool {
	t.Helper()
	var count int
	var err error
	if engine == "sqlite" {
		err = db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, table, column).Scan(&count)
	} else {
		err = db.QueryRow(`SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = $1 AND column_name = $2`, table, column).Scan(&count)
	}
	if err != nil {
		t.Fatalf("check %s column %s.%s: %v", engine, table, column, err)
	}
	return count == 1
}

func assertSQLiteForeignKeyAction(t *testing.T, db *sql.DB, table, column, targetTable, wantDelete string) {
	t.Helper()
	rows, err := db.Query(`PRAGMA foreign_key_list('` + table + `')`)
	if err != nil {
		t.Fatalf("inspect foreign keys for %s: %v", table, err)
	}
	defer closeMigrationRows(t, rows)
	for rows.Next() {
		var id, seq int
		var target, from, to, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &seq, &target, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			t.Fatalf("scan foreign key for %s: %v", table, err)
		}
		if from == column {
			if target != targetTable || !strings.EqualFold(onDelete, wantDelete) {
				t.Fatalf("%s.%s FK got target=%s on_delete=%s, want %s %s", table, column, target, onDelete, targetTable, wantDelete)
			}
			return
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate foreign keys for %s: %v", table, err)
	}
	t.Fatalf("foreign key for %s.%s is missing", table, column)
}

func assertSQLiteForeignKeyCheck(t *testing.T, db *sql.DB) {
	t.Helper()
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatalf("run SQLite foreign_key_check: %v", err)
	}
	defer closeMigrationRows(t, rows)
	if rows.Next() {
		var table, parent string
		var rowID, foreignKeyID int64
		if err := rows.Scan(&table, &rowID, &parent, &foreignKeyID); err != nil {
			t.Fatalf("scan SQLite foreign_key_check violation: %v", err)
		}
		t.Fatalf("SQLite foreign_key_check violation: table=%s rowid=%d parent=%s fkid=%d", table, rowID, parent, foreignKeyID)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate SQLite foreign_key_check: %v", err)
	}
}

func sqliteColumnNames(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.Query(`PRAGMA table_info('` + table + `')`)
	if err != nil {
		t.Fatalf("inspect columns for %s: %v", table, err)
	}
	defer closeMigrationRows(t, rows)
	var names []string
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatalf("scan columns for %s: %v", table, err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate columns for %s: %v", table, err)
	}
	return names
}

func postgresColumnNames(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.Query(`
		SELECT column_name
		FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = $1
		ORDER BY ordinal_position`, table)
	if err != nil {
		t.Fatalf("inspect PostgreSQL columns for %s: %v", table, err)
	}
	defer closeMigrationRows(t, rows)
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan PostgreSQL columns for %s: %v", table, err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate PostgreSQL columns for %s: %v", table, err)
	}
	return names
}

func assertSQLiteTimeColumnsHaveNoDefault(t *testing.T, db *sql.DB, table string) {
	t.Helper()
	rows, err := db.Query(`PRAGMA table_info('` + table + `')`)
	if err != nil {
		t.Fatalf("inspect time columns for %s: %v", table, err)
	}
	defer closeMigrationRows(t, rows)
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatalf("scan time columns for %s: %v", table, err)
		}
		if strings.Contains(strings.ToUpper(columnType), "DATE") && defaultValue.Valid {
			t.Fatalf("%s.%s has DB-side time default %q", table, name, defaultValue.String)
		}
	}
}

func assertPostgresTableTimeColumnsHaveNoDefault(t *testing.T, db *sql.DB, table string) {
	t.Helper()
	rows, err := db.Query(`
		SELECT column_name, column_default
		FROM information_schema.columns
		WHERE table_schema = current_schema()
		  AND table_name = $1
		  AND data_type IN ('timestamp with time zone', 'timestamp without time zone')`, table)
	if err != nil {
		t.Fatalf("inspect PostgreSQL time columns for %s: %v", table, err)
	}
	defer closeMigrationRows(t, rows)
	for rows.Next() {
		var column string
		var defaultValue sql.NullString
		if err := rows.Scan(&column, &defaultValue); err != nil {
			t.Fatalf("scan PostgreSQL time column for %s: %v", table, err)
		}
		if defaultValue.Valid {
			t.Fatalf("PostgreSQL %s.%s has DB-side time default %q", table, column, defaultValue.String)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate PostgreSQL time columns for %s: %v", table, err)
	}
}

func insertFoundationRepository(t *testing.T, db *sql.DB, id, now string) {
	t.Helper()
	mustExec(t, db, `INSERT INTO backup_repositories
		(id, provider_kind, display_name, description, version_mode, status, capability_revision, capabilities_json, immutability_level, created_at, updated_at)
		VALUES (?, 'rsync', 'foundation-repository', '', 'hardlink_tree', 'online', 1, '{}', 'xirang_managed', ?, ?)`, id, now, now)
}

func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("execute migration assertion query: %v", err)
	}
}

func closeMigrationRows(t *testing.T, rows *sql.Rows) {
	t.Helper()
	if err := rows.Close(); err != nil {
		t.Errorf("close migration assertion rows: %v", err)
	}
}
