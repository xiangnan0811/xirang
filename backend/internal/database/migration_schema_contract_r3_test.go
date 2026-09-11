package database

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4"
)

func TestRunMigrations083And084SchemaContractSQLite(t *testing.T) {
	testRunMigrations083And084SchemaContract(t, newSQLiteMigrationFixture(t))
}

func TestRunMigrations083And084SchemaContractPostgres(t *testing.T) {
	testRunMigrations083And084SchemaContract(t, newRequiredPostgresMigrationFixture(t))
}

func testRunMigrations083And084SchemaContract(t *testing.T, fixture migrationFixture) {
	t.Helper()

	t.Run("valid", func(t *testing.T) {
		migrator, db := fixture.openAt(t, latestMigrationVersion)
		if err := RunMigrations(fixture.recoveryWorkerGorm(t, db), fixture.engine); err != nil {
			t.Fatalf("valid %s 000083/000084 schema rejected: %v", fixture.engine, err)
		}
		assertMigrationVersion(t, migrator, latestMigrationVersion)
	})
	t.Run("v83 schema upgrades to v84", func(t *testing.T) {
		migrator, db := fixture.openAt(t, uint(taskRunRecoveryCaptureSchemaVersion))
		if err := RunMigrations(fixture.recoveryWorkerGorm(t, db), fixture.engine); err != nil {
			t.Fatalf("upgrade %s from 000083 to 000084 rejected: %v", fixture.engine, err)
		}
		assertMigrationVersion(t, migrator, latestMigrationVersion)
	})

	t.Run("missing recovery capture column", func(t *testing.T) {
		migrator, db := fixture.openAt(t, latestMigrationVersion)
		if fixture.engine == "postgres" {
			fixture.mustExec(t, db, `DROP TRIGGER trg_task_runs_recovery_capture_downgrade_admission ON schema_migrations`)
		} else {
			fixture.mustExec(t, db, `DROP TRIGGER trg_task_runs_recovery_capture_downgrade_admission`)
		}
		fixture.mustExec(t, db, `ALTER TABLE task_runs DROP COLUMN backup_capture_manifest`)
		assertRunMigrations083And084SchemaDrift(t, fixture, migrator, db, "missing_task_run_recovery_capture_column")
	})

	t.Run("missing recovery capture admission guard", func(t *testing.T) {
		migrator, db := fixture.openAt(t, latestMigrationVersion)
		if fixture.engine == "postgres" {
			fixture.mustExec(t, db, `DROP TRIGGER trg_task_runs_recovery_capture_downgrade_admission ON schema_migrations`)
		} else {
			fixture.mustExec(t, db, `DROP TRIGGER trg_task_runs_recovery_capture_downgrade_admission`)
		}
		assertRunMigrations083And084SchemaDrift(t, fixture, migrator, db, "missing_task_run_recovery_capture_admission_trigger")
	})

	t.Run("no-op recovery capture admission guard", func(t *testing.T) {
		migrator, db := fixture.openAt(t, latestMigrationVersion)
		if fixture.engine == "postgres" {
			fixture.mustExec(t, db, `CREATE OR REPLACE FUNCTION task_runs_recovery_capture_downgrade_admission()
				RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END; $$`)
		} else {
			fixture.mustExec(t, db, `DROP TRIGGER trg_task_runs_recovery_capture_downgrade_admission`)
			fixture.mustExec(t, db, `CREATE TRIGGER trg_task_runs_recovery_capture_downgrade_admission
				BEFORE INSERT ON schema_migrations
				BEGIN SELECT 1; END`)
		}
		assertRunMigrations083And084SchemaDrift(t, fixture, migrator, db, "invalid_task_run_recovery_capture_admission_trigger")
	})

	t.Run("missing alert delivery column", func(t *testing.T) {
		migrator, db := fixture.openAt(t, latestMigrationVersion)
		fixture.mustExec(t, db, `ALTER TABLE alerts DROP COLUMN delivery_reason`)
		assertRunMigrations083And084SchemaDrift(t, fixture, migrator, db, "missing_alert_delivery_column")
	})

	t.Run("missing alert delivery key index", func(t *testing.T) {
		migrator, db := fixture.openAt(t, latestMigrationVersion)
		fixture.mustExec(t, db, `DROP INDEX idx_alert_deliveries_delivery_key`)
		assertRunMigrations083And084SchemaDrift(t, fixture, migrator, db, "missing_alert_delivery_key_index")
	})
	t.Run("invalid alert delivery key index", func(t *testing.T) {
		migrator, db := fixture.openAt(t, latestMigrationVersion)
		fixture.mustExec(t, db, `DROP INDEX idx_alert_deliveries_delivery_key`)
		fixture.mustExec(t, db, `CREATE UNIQUE INDEX idx_alert_deliveries_delivery_key ON alert_deliveries(delivery_key)`)
		assertRunMigrations083And084SchemaDrift(t, fixture, migrator, db, "invalid_alert_delivery_key_index")
	})

	t.Run("missing alert delivery admission guard", func(t *testing.T) {
		migrator, db := fixture.openAt(t, latestMigrationVersion)
		if fixture.engine == "postgres" {
			fixture.mustExec(t, db, `DROP TRIGGER trg_alert_delivery_intents_downgrade_admission ON schema_migrations`)
		} else {
			fixture.mustExec(t, db, `DROP TRIGGER trg_alert_delivery_intents_downgrade_admission`)
		}
		assertRunMigrations083And084SchemaDrift(t, fixture, migrator, db, "missing_alert_delivery_admission_trigger")
	})

	t.Run("no-op alert delivery admission guard", func(t *testing.T) {
		migrator, db := fixture.openAt(t, latestMigrationVersion)
		if fixture.engine == "postgres" {
			fixture.mustExec(t, db, `CREATE OR REPLACE FUNCTION alert_delivery_intents_downgrade_admission()
				RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END; $$`)
		} else {
			fixture.mustExec(t, db, `DROP TRIGGER trg_alert_delivery_intents_downgrade_admission`)
			fixture.mustExec(t, db, `CREATE TRIGGER trg_alert_delivery_intents_downgrade_admission
				BEFORE INSERT ON schema_migrations
				BEGIN SELECT 1; END`)
		}
		assertRunMigrations083And084SchemaDrift(t, fixture, migrator, db, "invalid_alert_delivery_admission_trigger")
	})
}

func assertRunMigrations083And084SchemaDrift(
	t *testing.T,
	fixture migrationFixture,
	migrator *migrate.Migrate,
	db *sql.DB,
	reason string,
) {
	t.Helper()
	beforeVersion, beforeDirty, err := migrator.Version()
	if err != nil {
		t.Fatalf("read preflight migration version: %v", err)
	}
	if beforeVersion != latestMigrationVersion || beforeDirty {
		t.Fatalf("preflight migration state got version=%d dirty=%v, want version=%d clean", beforeVersion, beforeDirty, latestMigrationVersion)
	}

	err = RunMigrations(fixture.recoveryWorkerGorm(t, db), fixture.engine)
	if !errors.Is(err, ErrMigrationSchemaDrift) || !strings.Contains(err.Error(), reason) {
		t.Fatalf("clean %s 000083/000084 drift returned %v, want %s", fixture.engine, err, reason)
	}

	afterVersion, afterDirty, err := migrator.Version()
	if err != nil {
		t.Fatalf("read postflight migration version: %v", err)
	}
	if afterVersion != beforeVersion || afterDirty != beforeDirty {
		t.Fatalf("schema drift changed migration metadata: before version=%d dirty=%v, after version=%d dirty=%v", beforeVersion, beforeDirty, afterVersion, afterDirty)
	}
}
