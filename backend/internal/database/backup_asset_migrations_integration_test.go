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

	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"github.com/golang-migrate/migrate/v4"
	pgxmigrate "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	sqlitemigrate "github.com/golang-migrate/migrate/v4/database/sqlite3"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/mattn/go-sqlite3"
	"gorm.io/gorm/schema"
)

const (
	backupAssetMigrationVersion        = 62
	backupAssetPublicationVersion      = 63
	backupAssetRsyncPublicationVersion = 64
	backupAssetSearchVersion           = 65
	backupAssetContentVersion          = 66
)

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
