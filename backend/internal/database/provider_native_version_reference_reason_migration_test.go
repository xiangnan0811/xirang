package database

import (
	"strings"
	"testing"
	"time"
)

const providerNativeVersionReferenceReasonMigrationVersion uint = 76

func TestProviderNativeVersionReferenceReasonSQLiteMigrationLifecycle(t *testing.T) {
	testProviderNativeVersionReferenceReasonMigrationLifecycle(t, newSQLiteMigrationFixture(t))
}

func TestProviderNativeVersionReferenceReasonMigrationPostgresLifecycle(t *testing.T) {
	testProviderNativeVersionReferenceReasonMigrationLifecycle(t, newRequiredPostgresMigrationFixture(t))
}

func testProviderNativeVersionReferenceReasonMigrationLifecycle(t *testing.T, fixture migrationFixture) {
	t.Helper()

	t.Run("pristine 75 to 76 to 75", func(t *testing.T) {
		migrator, db := fixture.openAt(t, rcloneNativeVersionEvidenceMigrationVersion)
		assertMigrationVersion(t, migrator, rcloneNativeVersionEvidenceMigrationVersion)
		lifecycleTriggerName := "trg_backup_asset_lifecycle_downgrade_admission"
		lifecycleTriggerBefore := fixture.recoveryTriggerDefinition(t, db, "schema_migrations", lifecycleTriggerName)
		providerTriggerName := "trg_provider_native_version_reference_reason_downgrade_admission"
		if fixture.recoveryTriggerExists(t, db, "schema_migrations", providerTriggerName) {
			t.Fatalf("%s pristine 000075 unexpectedly retained 000076 downgrade admission trigger", fixture.engine)
		}

		migrateToBackupAssetVersion(t, migrator, providerNativeVersionReferenceReasonMigrationVersion)
		assertMigrationVersion(t, migrator, providerNativeVersionReferenceReasonMigrationVersion)
		if !fixture.recoveryTriggerExists(t, db, "schema_migrations", lifecycleTriggerName) {
			t.Fatalf("%s 000076 upgrade dropped lifecycle downgrade admission trigger", fixture.engine)
		}
		if got := fixture.recoveryTriggerDefinition(t, db, "schema_migrations", lifecycleTriggerName); got != lifecycleTriggerBefore {
			t.Fatalf("%s 000076 upgrade changed lifecycle downgrade admission trigger\n got: %s\nwant: %s", fixture.engine, got, lifecycleTriggerBefore)
		}
		if !fixture.recoveryTriggerExists(t, db, "schema_migrations", providerTriggerName) {
			t.Fatalf("%s 000076 upgrade omitted provider native version reference reason admission trigger", fixture.engine)
		}

		if err := migrator.Steps(-1); err != nil {
			t.Fatalf("%s pristine 000076 down: %v", fixture.engine, err)
		}
		assertMigrationVersion(t, migrator, rcloneNativeVersionEvidenceMigrationVersion)
		if !fixture.recoveryTriggerExists(t, db, "schema_migrations", lifecycleTriggerName) {
			t.Fatalf("%s pristine 000076 down dropped lifecycle downgrade admission trigger", fixture.engine)
		}
		if got := fixture.recoveryTriggerDefinition(t, db, "schema_migrations", lifecycleTriggerName); got != lifecycleTriggerBefore {
			t.Fatalf("%s pristine 000076 down changed lifecycle downgrade admission trigger\n got: %s\nwant: %s", fixture.engine, got, lifecycleTriggerBefore)
		}
		if fixture.recoveryTriggerExists(t, db, "schema_migrations", providerTriggerName) {
			t.Fatalf("%s pristine 000076 down retained provider native version reference reason admission trigger", fixture.engine)
		}
	})

	t.Run("up accepts blocked native version reference reason", func(t *testing.T) {
		migrator, db := fixture.openAt(t, rcloneNativeVersionEvidenceMigrationVersion)
		now := time.Date(2026, 9, 2, 6, 7, 8, 0, time.UTC)
		repositoryID := strings.Repeat("1", 32)
		pointID := strings.Repeat("2", 32)
		attemptID := strings.Repeat("3", 32)
		fixture.insertRepository(t, db, repositoryID, "restic", now)
		fixture.mustInsertRecoveryPoint(t, db, publicationPointSeed{
			ID: pointID, RepositoryID: repositoryID, Semantics: "native_snapshot", State: "committed",
		})
		lifecycleTriggerName := "trg_backup_asset_lifecycle_downgrade_admission"
		lifecycleTriggerBefore := fixture.recoveryTriggerDefinition(t, db, "schema_migrations", lifecycleTriggerName)

		migrateToBackupAssetVersion(t, migrator, providerNativeVersionReferenceReasonMigrationVersion)
		assertMigrationVersion(t, migrator, providerNativeVersionReferenceReasonMigrationVersion)
		if got := fixture.recoveryTriggerDefinition(t, db, "schema_migrations", lifecycleTriggerName); got != lifecycleTriggerBefore {
			t.Fatalf("%s 000076 upgrade changed lifecycle downgrade admission trigger\n got: %s\nwant: %s", fixture.engine, got, lifecycleTriggerBefore)
		}
		providerTriggerName := "trg_provider_native_version_reference_reason_downgrade_admission"
		if !fixture.recoveryTriggerExists(t, db, "schema_migrations", providerTriggerName) {
			t.Fatalf("%s 000076 upgrade omitted provider native version reference reason admission trigger", fixture.engine)
		}
		fixture.mustExec(t, db, `INSERT INTO recovery_point_lifecycle_attempts
			(id, recovery_point_id, operation, phase, transition_revision, blocked_reason, created_at, updated_at)
			VALUES (?, ?, 'retention_expire', 'blocked', 1, 'provider_native_version_referenced', ?, ?)`,
			attemptID, pointID, now, now)

		var phase, blockedReason string
		if err := db.QueryRow(fixture.bind(`SELECT phase, blocked_reason
			FROM recovery_point_lifecycle_attempts WHERE id = ?`), attemptID).Scan(&phase, &blockedReason); err != nil {
			t.Fatalf("read 000076 blocked lifecycle attempt: %v", err)
		}
		if phase != "blocked" || blockedReason != "provider_native_version_referenced" {
			t.Fatalf("000076 blocked lifecycle attempt phase=%q reason=%q", phase, blockedReason)
		}
	})

	t.Run("used down fails closed and remains clean at 76", func(t *testing.T) {
		migrator, db := fixture.openAt(t, rcloneNativeVersionEvidenceMigrationVersion)
		now := time.Date(2026, 9, 2, 7, 8, 9, 0, time.UTC)
		repositoryID := strings.Repeat("4", 32)
		pointID := strings.Repeat("5", 32)
		attemptID := strings.Repeat("6", 32)
		fixture.insertRepository(t, db, repositoryID, "restic", now)
		fixture.mustInsertRecoveryPoint(t, db, publicationPointSeed{
			ID: pointID, RepositoryID: repositoryID, Semantics: "native_snapshot", State: "committed",
		})
		lifecycleTriggerName := "trg_backup_asset_lifecycle_downgrade_admission"
		lifecycleTriggerBefore := fixture.recoveryTriggerDefinition(t, db, "schema_migrations", lifecycleTriggerName)
		migrateToBackupAssetVersion(t, migrator, providerNativeVersionReferenceReasonMigrationVersion)
		assertMigrationVersion(t, migrator, providerNativeVersionReferenceReasonMigrationVersion)
		if got := fixture.recoveryTriggerDefinition(t, db, "schema_migrations", lifecycleTriggerName); got != lifecycleTriggerBefore {
			t.Fatalf("%s 000076 upgrade changed lifecycle downgrade admission trigger\n got: %s\nwant: %s", fixture.engine, got, lifecycleTriggerBefore)
		}
		providerTriggerName := "trg_provider_native_version_reference_reason_downgrade_admission"
		if !fixture.recoveryTriggerExists(t, db, "schema_migrations", providerTriggerName) {
			t.Fatalf("%s 000076 upgrade omitted provider native version reference reason admission trigger", fixture.engine)
		}
		providerTriggerBefore := fixture.recoveryTriggerDefinition(t, db, "schema_migrations", providerTriggerName)
		fixture.mustExec(t, db, `INSERT INTO recovery_point_lifecycle_attempts
			(id, recovery_point_id, operation, phase, transition_revision, blocked_reason, created_at, updated_at)
			VALUES (?, ?, 'retention_expire', 'blocked', 1, 'provider_native_version_referenced', ?, ?)`,
			attemptID, pointID, now, now)

		if err := migrator.Steps(-1); err == nil {
			t.Fatalf("%s used 000076 down unexpectedly succeeded", fixture.engine)
		}
		assertMigrationVersion(t, migrator, providerNativeVersionReferenceReasonMigrationVersion)
		var rowCount int
		if err := db.QueryRow(fixture.bind(`SELECT COUNT(*) FROM recovery_point_lifecycle_attempts
			WHERE id = ? AND phase = 'blocked' AND blocked_reason = 'provider_native_version_referenced'`), attemptID).Scan(&rowCount); err != nil {
			t.Fatalf("count blocked 000076 attempt after rejected down: %v", err)
		}
		if rowCount != 1 {
			t.Fatalf("blocked 000076 attempts after rejected down=%d, want 1", rowCount)
		}
		if !fixture.recoveryTriggerExists(t, db, "schema_migrations", providerTriggerName) {
			t.Fatalf("%s rejected 000076 down dropped provider native version reference reason admission trigger", fixture.engine)
		}
		if got := fixture.recoveryTriggerDefinition(t, db, "schema_migrations", providerTriggerName); got != providerTriggerBefore {
			t.Fatalf("%s rejected 000076 down changed provider native version reference reason admission trigger\n got: %s\nwant: %s",
				fixture.engine, got, providerTriggerBefore)
		}
		if !fixture.recoveryTriggerExists(t, db, "schema_migrations", lifecycleTriggerName) {
			t.Fatalf("%s rejected 000076 down dropped lifecycle downgrade admission trigger", fixture.engine)
		}
		if got := fixture.recoveryTriggerDefinition(t, db, "schema_migrations", lifecycleTriggerName); got != lifecycleTriggerBefore {
			t.Fatalf("%s rejected 000076 down changed lifecycle downgrade admission trigger\n got: %s\nwant: %s",
				fixture.engine, got, lifecycleTriggerBefore)
		}
	})
}
