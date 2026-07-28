package database

import (
	"database/sql"
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

	"xirang/backend/internal/config"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"github.com/golang-migrate/migrate/v4"
	pgxmigrate "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	sqlitemigrate "github.com/golang-migrate/migrate/v4/database/sqlite3"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/mattn/go-sqlite3"
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
)

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
	t.Setenv("ALLOW_DIRTY_STARTUP", "")
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
		JOIN pg_attribute AS attribute
		  ON attribute.attrelid = relation.oid
		 AND attribute.attnum = ANY(constraint_row.conkey)
		WHERE namespace.nspname = current_schema()
		  AND relation.relname = $1
		  AND attribute.attname = $2
		  AND constraint_row.contype = 'f'
		LIMIT 1`, table, column).Scan(&definition)
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
