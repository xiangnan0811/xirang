package database

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/model"

	"github.com/golang-migrate/migrate/v4"
	pgxmigrate "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	sqlitemigrate "github.com/golang-migrate/migrate/v4/database/sqlite3"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/mattn/go-sqlite3"
	"gorm.io/gorm/schema"
)

const backupAssetMigrationVersion = 62

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
	baseDB, err := sql.Open("pgx", dsn)
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
	db, err := sql.Open("pgx", parsed.String())
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
	if err := migrator.Migrate(backupAssetMigrationVersion); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		t.Fatalf("migrate to 000062: %v", err)
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
