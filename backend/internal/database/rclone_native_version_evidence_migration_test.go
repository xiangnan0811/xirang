package database

import (
	"strings"
	"testing"
	"time"
)

func TestRcloneNativeVersionEvidenceMigrationContract(t *testing.T) {
	for _, testCase := range []struct {
		name string
		path string
		fs   interface{ ReadFile(string) ([]byte, error) }
		want []string
	}{
		{
			name: "sqlite up",
			path: "migrations/sqlite/000075_rclone_native_version_evidence.up.sql",
			fs:   sqliteMigrationsFS,
			want: []string{
				"CREATE TABLE recovery_point_rclone_native_versions",
				"evidence_role IN ('owned', 'reference')",
				"PRIMARY KEY (recovery_point_id, evidence_role, ordinal)",
				"encrypted_physical_key TEXT NOT NULL",
				"encrypted_version_id TEXT NOT NULL",
				"ON DELETE CASCADE",
				"idx_recovery_point_rclone_native_versions_repository_role_identity_point",
				"CREATE TRIGGER trg_rclone_native_version_evidence_downgrade_admission",
				"BEFORE INSERT ON schema_migrations",
			},
		},
		{
			name: "sqlite down",
			path: "migrations/sqlite/000075_rclone_native_version_evidence.down.sql",
			fs:   sqliteMigrationsFS,
			want: []string{
				"CREATE TEMP TABLE rclone_native_version_evidence_000075_down_guard",
				"DROP TABLE IF EXISTS recovery_point_rclone_native_versions",
			},
		},
		{
			name: "postgres up",
			path: "migrations/postgres/000075_rclone_native_version_evidence.up.sql",
			fs:   postgresMigrationsFS,
			want: []string{
				"BEGIN;", "COMMIT;", "CREATE TABLE recovery_point_rclone_native_versions",
				"evidence_role IN ('owned', 'reference')",
				"PRIMARY KEY (recovery_point_id, evidence_role, ordinal)",
				"encrypted_physical_key TEXT NOT NULL", "encrypted_version_id TEXT NOT NULL",
				"idx_recovery_point_rclone_native_versions_repository_role_identity_point",
				"CREATE OR REPLACE FUNCTION rclone_native_version_evidence_downgrade_admission",
				"CREATE TRIGGER trg_rclone_native_version_evidence_downgrade_admission",
			},
		},
		{
			name: "postgres down",
			path: "migrations/postgres/000075_rclone_native_version_evidence.down.sql",
			fs:   postgresMigrationsFS,
			want: []string{
				"BEGIN;",
				"DO $$",
				"IF EXISTS (",
				"SELECT 1 FROM recovery_point_rclone_native_versions",
				"RAISE EXCEPTION '000075 downgrade blocked: native Rclone version evidence exists';",
				"END",
				"DROP TRIGGER IF EXISTS trg_rclone_native_version_evidence_downgrade_admission ON schema_migrations;",
				"DROP FUNCTION IF EXISTS rclone_native_version_evidence_downgrade_admission();",
				"DROP TABLE IF EXISTS recovery_point_rclone_native_versions",
				"COMMIT;",
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			body, err := testCase.fs.ReadFile(testCase.path)
			if err != nil {
				t.Fatal(err)
			}
			for _, fragment := range testCase.want {
				if !strings.Contains(string(body), fragment) {
					t.Fatalf("%s missing %q", testCase.path, fragment)
				}
			}
		})
	}
}

func TestRcloneNativeVersionEvidenceSQLiteMigrationLifecycle(t *testing.T) {
	testRcloneNativeVersionEvidenceMigrationLifecycle(t, newSQLiteMigrationFixture(t))
}

func TestRcloneNativeVersionEvidenceMigrationPostgresLifecycle(t *testing.T) {
	testRcloneNativeVersionEvidenceMigrationLifecycle(t, newRequiredPostgresMigrationFixture(t))
}

func testRcloneNativeVersionEvidenceMigrationLifecycle(t *testing.T, fixture migrationFixture) {
	t.Helper()
	t.Run("upgrade to clean version 75 and pristine down", func(t *testing.T) {
		migrator, db := fixture.openAt(t, drillDurableRecoveryVersion)
		now := time.Date(2026, 9, 2, 3, 4, 5, 0, time.UTC)
		repositoryID := strings.Repeat("a", 32)
		pointID := strings.Repeat("b", 32)
		fixture.insertRepository(t, db, repositoryID, "rclone", now)
		fixture.mustInsertRecoveryPoint(t, db, publicationPointSeed{
			ID: pointID, RepositoryID: repositoryID, Semantics: "xirang_manifest", State: "committed",
		})
		migrateToBackupAssetVersion(t, migrator, rcloneNativeVersionEvidenceMigrationVersion)
		assertMigrationVersion(t, migrator, rcloneNativeVersionEvidenceMigrationVersion)
		if !databaseTableExists(t, db, fixture.engine, "recovery_point_rclone_native_versions") {
			t.Fatalf("%s clean 000075 migration omitted native evidence table", fixture.engine)
		}
		if !fixture.recoveryTriggerExists(t, db, "schema_migrations", "trg_rclone_native_version_evidence_downgrade_admission") {
			t.Fatalf("%s clean 000075 migration omitted downgrade admission trigger", fixture.engine)
		}
		if err := migrator.Steps(-1); err != nil {
			t.Fatalf("%s pristine 000075 down: %v", fixture.engine, err)
		}
		assertMigrationVersion(t, migrator, drillDurableRecoveryVersion)
		if databaseTableExists(t, db, fixture.engine, "recovery_point_rclone_native_versions") {
			t.Fatalf("%s pristine 000075 down retained native evidence table", fixture.engine)
		}
		if fixture.recoveryTriggerExists(t, db, "schema_migrations", "trg_rclone_native_version_evidence_downgrade_admission") {
			t.Fatalf("%s pristine 000075 down retained downgrade admission trigger", fixture.engine)
		}
	})

	t.Run("used down is rejected atomically and retains evidence", func(t *testing.T) {
		migrator, db := fixture.openAt(t, drillDurableRecoveryVersion)
		now := time.Date(2026, 9, 2, 4, 5, 6, 0, time.UTC)
		repositoryID := strings.Repeat("c", 32)
		pointID := strings.Repeat("d", 32)
		fixture.insertRepository(t, db, repositoryID, "rclone", now)
		fixture.mustInsertRecoveryPoint(t, db, publicationPointSeed{
			ID: pointID, RepositoryID: repositoryID, Semantics: "xirang_manifest", State: "committed",
		})
		migrateToBackupAssetVersion(t, migrator, rcloneNativeVersionEvidenceMigrationVersion)
		assertMigrationVersion(t, migrator, rcloneNativeVersionEvidenceMigrationVersion)
		if !databaseTableExists(t, db, fixture.engine, "recovery_point_rclone_native_versions") {
			t.Fatalf("%s clean 000075 migration omitted native evidence table", fixture.engine)
		}
		triggerName := "trg_rclone_native_version_evidence_downgrade_admission"
		if !fixture.recoveryTriggerExists(t, db, "schema_migrations", triggerName) {
			t.Fatalf("%s clean 000075 migration omitted downgrade admission trigger", fixture.engine)
		}
		triggerBefore := fixture.recoveryTriggerDefinition(t, db, "schema_migrations", triggerName)
		tableBefore := fixture.tableDefinition(t, db, "recovery_point_rclone_native_versions")
		fixture.mustExec(t, db, `INSERT INTO recovery_point_rclone_native_versions (
			recovery_point_id, evidence_role, ordinal, repository_id, identity_digest,
			encrypted_physical_key, encrypted_version_id, created_at, updated_at
		) VALUES (?, 'reference', 0, ?, ?, ?, ?, ?, ?)`,
			pointID, repositoryID, strings.Repeat("e", 64),
			"encrypted-key", "encrypted-version", now, now)
		if err := migrator.Steps(-1); err == nil {
			t.Fatalf("%s used 000075 down unexpectedly succeeded", fixture.engine)
		}
		assertMigrationVersion(t, migrator, rcloneNativeVersionEvidenceMigrationVersion)
		if !databaseTableExists(t, db, fixture.engine, "recovery_point_rclone_native_versions") {
			t.Fatalf("%s rejected 000075 down dropped native evidence table", fixture.engine)
		}
		var rowCount int
		if err := db.QueryRow(fixture.bind(`SELECT COUNT(*) FROM recovery_point_rclone_native_versions`)).Scan(&rowCount); err != nil {
			t.Fatalf("count %s evidence rows after rejected down: %v", fixture.engine, err)
		}
		if rowCount != 1 {
			t.Fatalf("%s evidence rows after rejected down=%d, want 1", fixture.engine, rowCount)
		}
		if !fixture.recoveryTriggerExists(t, db, "schema_migrations", triggerName) {
			t.Fatalf("%s rejected 000075 down dropped downgrade admission trigger", fixture.engine)
		}
		if got := fixture.recoveryTriggerDefinition(t, db, "schema_migrations", triggerName); got != triggerBefore {
			t.Fatalf("%s rejected 000075 down changed downgrade admission trigger\n got: %s\nwant: %s",
				fixture.engine, got, triggerBefore)
		}
		if got := fixture.tableDefinition(t, db, "recovery_point_rclone_native_versions"); got != tableBefore {
			t.Fatalf("%s rejected 000075 down changed native evidence table definition\n got: %s\nwant: %s",
				fixture.engine, got, tableBefore)
		}
	})
}
