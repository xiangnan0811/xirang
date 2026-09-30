package database

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4"
)

func TestRunMigrations091SchemaContractSQLite(t *testing.T) {
	testRunMigrations091SchemaContract(t, newSQLiteMigrationFixture(t))
}

func TestRunMigrations091SchemaContractPostgres(t *testing.T) {
	testRunMigrations091SchemaContract(t, newRequiredPostgresMigrationFixture(t))
}

func testRunMigrations091SchemaContract(t *testing.T, fixture migrationFixture) {
	t.Helper()

	t.Run("valid", func(t *testing.T) {
		migrator, db := fixture.openAt(t, latestMigrationVersion)
		if err := RunMigrations(fixture.recoveryWorkerGorm(t, db), fixture.engine); err != nil {
			t.Fatalf("valid %s 000091 schema rejected: %v", fixture.engine, err)
		}
		assertMigrationVersion(t, migrator, latestMigrationVersion)
	})

	t.Run("same-name no-op admission guard", func(t *testing.T) {
		migrator, db := fixture.openAt(t, latestMigrationVersion)
		if fixture.engine == "postgres" {
			fixture.mustExec(t, db, `CREATE OR REPLACE FUNCTION service_monitor_retirement_downgrade_admission()
				RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END; $$`)
		} else {
			fixture.mustExec(t, db, `DROP TRIGGER trg_service_monitor_retirement_downgrade_admission`)
			fixture.mustExec(t, db, `CREATE TRIGGER trg_service_monitor_retirement_downgrade_admission
				BEFORE INSERT ON schema_migrations
				WHEN NEW.version < 91
				BEGIN SELECT 1; END`)
		}
		assertRunMigrations091SchemaDrift(t, fixture, migrator, db, "invalid_service_monitor_retirement_admission_trigger")
	})

	if fixture.engine == "sqlite" {
		t.Run("additional SQLite predicate is rejected", func(t *testing.T) {
			migrator, db := fixture.openAt(t, latestMigrationVersion)
			fixture.mustExec(t, db, `DROP TRIGGER trg_service_monitor_retirement_downgrade_admission`)
			fixture.mustExec(t, db, `CREATE TRIGGER trg_service_monitor_retirement_downgrade_admission
				BEFORE INSERT ON schema_migrations
				WHEN NEW.version < 91 AND 0
				BEGIN
					SELECT RAISE(ABORT, '000091 downgrade blocked: service-monitor retirement is irreversible');
				END`)
			assertRunMigrations091SchemaDrift(t, fixture, migrator, db, "invalid_service_monitor_retirement_admission_trigger")
		})

		t.Run("missing update admission guard is rejected", func(t *testing.T) {
			migrator, db := fixture.openAt(t, latestMigrationVersion)
			fixture.mustExec(t, db, `DROP TRIGGER trg_service_monitor_retirement_downgrade_update_admission`)
			assertRunMigrations091SchemaDrift(t, fixture, migrator, db, "missing_service_monitor_retirement_admission_trigger")
		})
	} else {
		t.Run("early return before exception is rejected", func(t *testing.T) {
			migrator, db := fixture.openAt(t, latestMigrationVersion)
			fixture.mustExec(t, db, `CREATE OR REPLACE FUNCTION service_monitor_retirement_downgrade_admission()
				RETURNS trigger LANGUAGE plpgsql AS $$
				BEGIN
					IF NEW.version < 91 THEN
						RETURN NEW;
					END IF;
					RAISE EXCEPTION '000091 downgrade blocked: service-monitor retirement is irreversible';
				END;
				$$`)
			assertRunMigrations091SchemaDrift(t, fixture, migrator, db, "invalid_service_monitor_retirement_admission_trigger")
		})

		t.Run("statement-level trigger is rejected", func(t *testing.T) {
			migrator, db := fixture.openAt(t, latestMigrationVersion)
			fixture.mustExec(t, db, `DROP TRIGGER trg_service_monitor_retirement_downgrade_admission ON schema_migrations`)
			fixture.mustExec(t, db, `CREATE TRIGGER trg_service_monitor_retirement_downgrade_admission
				BEFORE INSERT OR UPDATE ON schema_migrations
				FOR EACH STATEMENT
				EXECUTE FUNCTION service_monitor_retirement_downgrade_admission()`)
			assertRunMigrations091SchemaDrift(t, fixture, migrator, db, "invalid_service_monitor_retirement_admission_trigger")
		})

		t.Run("disabled trigger is rejected", func(t *testing.T) {
			migrator, db := fixture.openAt(t, latestMigrationVersion)
			fixture.mustExec(t, db, `ALTER TABLE schema_migrations DISABLE TRIGGER trg_service_monitor_retirement_downgrade_admission`)
			assertRunMigrations091SchemaDrift(t, fixture, migrator, db, "invalid_service_monitor_retirement_admission_trigger")
		})
	}
}

func assertRunMigrations091SchemaDrift(
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
		t.Fatalf("clean %s 000091 drift returned %v, want %s", fixture.engine, err, reason)
	}

	afterVersion, afterDirty, err := migrator.Version()
	if err != nil {
		t.Fatalf("read postflight migration version: %v", err)
	}
	if afterVersion != beforeVersion || afterDirty != beforeDirty {
		t.Fatalf("schema drift changed migration metadata: before version=%d dirty=%v, after version=%d dirty=%v", beforeVersion, beforeDirty, afterVersion, afterDirty)
	}
}
