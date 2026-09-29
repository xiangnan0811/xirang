package database

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4"
)

func TestRunMigrations090SchemaContractSQLite(t *testing.T) {
	testRunMigrations090SchemaContract(t, newSQLiteMigrationFixture(t))
}

func TestRunMigrations090SchemaContractPostgres(t *testing.T) {
	testRunMigrations090SchemaContract(t, newRequiredPostgresMigrationFixture(t))
}

func testRunMigrations090SchemaContract(t *testing.T, fixture migrationFixture) {
	t.Helper()

	t.Run("valid", func(t *testing.T) {
		migrator, db := fixture.openAt(t, latestMigrationVersion)
		if err := RunMigrations(fixture.recoveryWorkerGorm(t, db), fixture.engine); err != nil {
			t.Fatalf("valid %s 000090 schema rejected: %v", fixture.engine, err)
		}
		assertMigrationVersion(t, migrator, latestMigrationVersion)
	})

	t.Run("same-name no-op admission guard", func(t *testing.T) {
		migrator, db := fixture.openAt(t, latestMigrationVersion)
		if fixture.engine == "postgres" {
			fixture.mustExec(t, db, `CREATE OR REPLACE FUNCTION backup_focus_retirement_downgrade_admission()
				RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END; $$`)
		} else {
			fixture.mustExec(t, db, `DROP TRIGGER trg_backup_focus_retirement_downgrade_admission`)
			fixture.mustExec(t, db, `CREATE TRIGGER trg_backup_focus_retirement_downgrade_admission
				BEFORE INSERT ON schema_migrations
				BEGIN SELECT 1; END`)
		}
		assertRunMigrations090SchemaDrift(t, fixture, migrator, db, "invalid_backup_focus_retirement_admission_trigger")
	})

	if fixture.engine == "sqlite" {
		t.Run("additional SQLite predicate is rejected", func(t *testing.T) {
			migrator, db := fixture.openAt(t, latestMigrationVersion)
			fixture.mustExec(t, db, `DROP TRIGGER trg_backup_focus_retirement_downgrade_admission`)
			fixture.mustExec(t, db, `CREATE TRIGGER trg_backup_focus_retirement_downgrade_admission
				BEFORE INSERT ON schema_migrations
				WHEN NEW.version < 90 AND 0
				BEGIN
					SELECT RAISE(ABORT, '000090 downgrade blocked: backup-focus retirement is irreversible');
				END`)
			assertRunMigrations090SchemaDrift(t, fixture, migrator, db, "invalid_backup_focus_retirement_admission_trigger")
		})

		t.Run("same-name no-op update admission guard", func(t *testing.T) {
			migrator, db := fixture.openAt(t, latestMigrationVersion)
			fixture.mustExec(t, db, `DROP TRIGGER trg_backup_focus_retirement_downgrade_update_admission`)
			fixture.mustExec(t, db, `CREATE TRIGGER trg_backup_focus_retirement_downgrade_update_admission
				BEFORE UPDATE ON schema_migrations
				BEGIN SELECT 1; END`)
			assertRunMigrations090SchemaDrift(t, fixture, migrator, db, "invalid_backup_focus_retirement_admission_trigger")
		})
	}

	if fixture.engine == "postgres" {
		t.Run("early return before exception is rejected", func(t *testing.T) {
			migrator, db := fixture.openAt(t, latestMigrationVersion)
			fixture.mustExec(t, db, `CREATE OR REPLACE FUNCTION backup_focus_retirement_downgrade_admission()
				RETURNS trigger LANGUAGE plpgsql AS $$
				BEGIN
					IF NEW.version < 90 THEN
						RETURN NEW;
					END IF;
					RAISE EXCEPTION '000090 downgrade blocked: backup-focus retirement is irreversible';
				END;
				$$`)
			assertRunMigrations090SchemaDrift(t, fixture, migrator, db, "invalid_backup_focus_retirement_admission_trigger")
		})

		t.Run("statement-level trigger is rejected", func(t *testing.T) {
			migrator, db := fixture.openAt(t, latestMigrationVersion)
			fixture.mustExec(t, db, `DROP TRIGGER trg_backup_focus_retirement_downgrade_admission ON schema_migrations`)
			fixture.mustExec(t, db, `CREATE TRIGGER trg_backup_focus_retirement_downgrade_admission
				BEFORE INSERT OR UPDATE ON schema_migrations
				FOR EACH STATEMENT
				EXECUTE FUNCTION backup_focus_retirement_downgrade_admission()`)
			assertRunMigrations090SchemaDrift(t, fixture, migrator, db, "invalid_backup_focus_retirement_admission_trigger")
		})
	}
}

func assertRunMigrations090SchemaDrift(
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
		t.Fatalf("clean %s 000090 drift returned %v, want %s", fixture.engine, err, reason)
	}

	afterVersion, afterDirty, err := migrator.Version()
	if err != nil {
		t.Fatalf("read postflight migration version: %v", err)
	}
	if afterVersion != beforeVersion || afterDirty != beforeDirty {
		t.Fatalf("schema drift changed migration metadata: before version=%d dirty=%v, after version=%d dirty=%v", beforeVersion, beforeDirty, afterVersion, afterDirty)
	}
}
