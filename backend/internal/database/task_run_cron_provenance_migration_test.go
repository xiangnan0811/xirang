package database

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
)

func TestTaskRunCronProvenanceMigrationSQLite(t *testing.T) {
	testTaskRunCronProvenanceMigration(t, newSQLiteMigrationFixture(t))
}

func TestTaskRunCronProvenanceMigrationPostgres(t *testing.T) {
	testTaskRunCronProvenanceMigration(t, newRequiredPostgresMigrationFixture(t))
}

func testTaskRunCronProvenanceMigration(t *testing.T, fixture migrationFixture) {
	t.Helper()
	migrator, db := fixture.openAt(t, latestMigrationVersion-1)
	if err := migrator.Steps(1); err != nil {
		t.Fatalf("apply 000082 %s: %v", fixture.engine, err)
	}
	assertMigrationVersion(t, migrator, latestMigrationVersion)

	now := time.Date(2026, 9, 9, 1, 2, 3, 0, time.UTC)
	taskID := int64(82001)
	firstRunID := int64(82002)
	fixture.mustExec(t, db, `INSERT INTO tasks
		(id, name, node_id, executor_type, status, created_at, updated_at)
		VALUES (?, ?, 1, 'rsync', 'idle', ?, ?)`, taskID, "cron-provenance-task", now, now)
	occurrence := now.Add(time.Hour)
	fingerprint := strings.Repeat("a", 64)
	fixture.mustExec(t, db, `INSERT INTO task_runs
		(id, task_id, node_id_snapshot, trigger_type, status, cron_scheduled_at,
		 backup_config_fingerprint, created_at, updated_at)
		VALUES (?, ?, 1, 'cron', 'pending', ?, ?, ?, ?)`,
		firstRunID, taskID, occurrence, "", now, now)

	// The canonical occurrence identity is unique for a task, while another
	// task may legitimately use the same wall-clock occurrence.
	fixture.expectExecRejected(t, db, `INSERT INTO task_runs
		(id, task_id, node_id_snapshot, trigger_type, status, cron_scheduled_at,
		 backup_config_fingerprint, created_at, updated_at)
		VALUES (?, ?, 1, 'cron', 'pending', ?, ?, ?, ?)`,
		firstRunID+1, taskID, occurrence, strings.Repeat("b", 64), now, now)
	fixture.mustExec(t, db, `INSERT INTO tasks
		(id, name, node_id, executor_type, status, created_at, updated_at)
		VALUES (?, ?, 1, 'rsync', 'idle', ?, ?)`, taskID+10, "cron-provenance-other-task", now, now)
	fixture.mustExec(t, db, `INSERT INTO task_runs
		(id, task_id, node_id_snapshot, trigger_type, status, cron_scheduled_at,
		 backup_config_fingerprint, created_at, updated_at)
		VALUES (?, ?, 1, 'cron', 'pending', ?, ?, ?, ?)`,
		firstRunID+10, taskID+10, occurrence, strings.Repeat("b", 64), now, now)

	fixture.expectExecRejected(t, db,
		`UPDATE task_runs SET trigger_type = 'manual' WHERE id = ?`, firstRunID)
	fixture.expectExecRejected(t, db,
		`UPDATE task_runs SET cron_scheduled_at = ? WHERE id = ?`, occurrence.Add(time.Hour), firstRunID)
	fixture.mustExec(t, db,
		`UPDATE task_runs SET backup_config_fingerprint = ? WHERE id = ?`, fingerprint, firstRunID)
	var storedFingerprint string
	if err := db.QueryRow(fixture.bind(`SELECT backup_config_fingerprint FROM task_runs WHERE id = ?`), firstRunID).Scan(&storedFingerprint); err != nil {
		t.Fatalf("read captured %s backup fingerprint: %v", fixture.engine, err)
	}
	if storedFingerprint != fingerprint {
		t.Fatalf("captured %s backup fingerprint=%q, want %q", fixture.engine, storedFingerprint, fingerprint)
	}
	fixture.expectExecRejected(t, db,
		`UPDATE task_runs SET backup_config_fingerprint = ? WHERE id = ?`, strings.Repeat("c", 64), firstRunID)
	fixture.mustExec(t, db,
		`UPDATE task_runs SET status = 'success' WHERE id = ?`, firstRunID)
	fixture.expectExecRejected(t, db,
		`UPDATE task_runs SET backup_config_fingerprint = ? WHERE id = ?`, strings.Repeat("d", 64), firstRunID)

	// A used v82 schema cannot be downgraded. The metadata admission trigger
	// rejects the same write golang-migrate would use before any down body runs.
	fixture.expectExecRejected(t, db,
		`INSERT INTO schema_migrations (version, dirty) VALUES (?, ?)`, latestMigrationVersion-1, true)
	if err := migrator.Steps(-1); err == nil {
		t.Fatalf("used 000082 %s downgrade unexpectedly succeeded", fixture.engine)
	}
	assertMigrationVersion(t, migrator, latestMigrationVersion)
	var cronAt time.Time
	if err := db.QueryRow(fixture.bind(`SELECT cron_scheduled_at FROM task_runs WHERE id = ?`), firstRunID).Scan(&cronAt); err != nil {
		t.Fatalf("read preserved cron occurrence after rejected %s downgrade: %v", fixture.engine, err)
	}
	if !cronAt.Equal(occurrence) {
		t.Fatalf("cron occurrence changed after rejected %s downgrade: got %v want %v", fixture.engine, cronAt, occurrence)
	}

	// A pristine v82 schema remains downgradeable, proving the admission guard
	// protects evidence rather than making every rollback impossible.
	pristineMigrator, pristineDB := fixture.openAt(t, latestMigrationVersion)
	if err := pristineMigrator.Steps(-1); err != nil {
		t.Fatalf("pristine 000082 %s downgrade: %v", fixture.engine, err)
	}
	assertMigrationVersion(t, pristineMigrator, latestMigrationVersion-1)
	for _, column := range []string{"cron_scheduled_at", "backup_config_fingerprint"} {
		exists, err := migrationColumnExists(pristineDB, fixture.engine, "task_runs", column)
		if err != nil {
			t.Fatalf("check pristine %s column %s after downgrade: %v", fixture.engine, column, err)
		}
		if exists {
			t.Fatalf("pristine %s downgrade retained task_runs.%s", fixture.engine, column)
		}
	}
	for _, testCase := range []struct {
		name        string
		triggerType string
		cronAt      any
		fingerprint string
	}{
		{name: "cron occurrence only", triggerType: "cron", cronAt: occurrence},
		{name: "backup fingerprint only", triggerType: "manual", fingerprint: strings.Repeat("e", 64)},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			identityMigrator, identityDB := fixture.openAt(t, latestMigrationVersion)
			identityTaskID := taskID + 100
			identityRunID := firstRunID + 100
			fixture.mustExec(t, identityDB, `INSERT INTO tasks
				(id, name, node_id, executor_type, status, created_at, updated_at)
				VALUES (?, ?, 1, 'rsync', 'idle', ?, ?)`,
				identityTaskID, "cron-provenance-identity-task", now, now)
			fixture.mustExec(t, identityDB, `INSERT INTO task_runs
				(id, task_id, node_id_snapshot, trigger_type, status, cron_scheduled_at,
				 backup_config_fingerprint, created_at, updated_at)
				VALUES (?, ?, 1, ?, 'pending', ?, ?, ?, ?)`,
				identityRunID, identityTaskID, testCase.triggerType, testCase.cronAt, testCase.fingerprint, now, now)
			if err := identityMigrator.Steps(-1); err == nil {
				t.Fatalf("used %s identity %s downgrade unexpectedly succeeded", fixture.engine, testCase.name)
			}
			assertMigrationVersion(t, identityMigrator, latestMigrationVersion)
		})
	}
}

func TestRunMigrations082SchemaContractSQLite(t *testing.T) {
	testRunMigrations082SchemaContract(t, newSQLiteMigrationFixture(t))
}

func TestRunMigrations082SchemaContractPostgres(t *testing.T) {
	testRunMigrations082SchemaContract(t, newRequiredPostgresMigrationFixture(t))
}

func testRunMigrations082SchemaContract(t *testing.T, fixture migrationFixture) {
	t.Helper()
	t.Run("valid", func(t *testing.T) {
		migrator, db := fixture.openAt(t, latestMigrationVersion)
		if err := RunMigrations(fixture.recoveryWorkerGorm(t, db), fixture.engine); err != nil {
			t.Fatalf("valid %s 000082 schema rejected: %v", fixture.engine, err)
		}
		assertMigrationVersion(t, migrator, latestMigrationVersion)
	})

	t.Run("same-name no-op immutable trigger", func(t *testing.T) {
		migrator, db := fixture.openAt(t, latestMigrationVersion)
		if fixture.engine == "postgres" {
			fixture.mustExec(t, db, `CREATE OR REPLACE FUNCTION task_runs_cron_provenance_immutable_guard()
				RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END; $$`)
		} else {
			fixture.mustExec(t, db, `DROP TRIGGER trg_task_runs_cron_provenance_immutable`)
			fixture.mustExec(t, db, `CREATE TRIGGER trg_task_runs_cron_provenance_immutable
				BEFORE UPDATE OF trigger_type, cron_scheduled_at, backup_config_fingerprint ON task_runs
				BEGIN SELECT 1; END`)
		}
		assertRunMigrations082SchemaDrift(t, fixture, migrator, db, "invalid_task_run_cron_provenance_trigger")
	})

	t.Run("same-name no-op admission trigger", func(t *testing.T) {
		migrator, db := fixture.openAt(t, latestMigrationVersion)
		if fixture.engine == "postgres" {
			fixture.mustExec(t, db, `CREATE OR REPLACE FUNCTION task_runs_cron_provenance_downgrade_admission()
				RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END; $$`)
		} else {
			fixture.mustExec(t, db, `DROP TRIGGER trg_task_runs_cron_provenance_downgrade_admission`)
			fixture.mustExec(t, db, `CREATE TRIGGER trg_task_runs_cron_provenance_downgrade_admission
				BEFORE INSERT ON schema_migrations
				BEGIN SELECT 1; END`)
		}
		assertRunMigrations082SchemaDrift(t, fixture, migrator, db, "invalid_task_run_cron_provenance_trigger")
	})
}

func assertRunMigrations082SchemaDrift(
	t *testing.T,
	fixture migrationFixture,
	migrator *migrate.Migrate,
	db *sql.DB,
	reason string,
) {
	t.Helper()
	err := RunMigrations(fixture.recoveryWorkerGorm(t, db), fixture.engine)
	if !errors.Is(err, ErrMigrationSchemaDrift) || !strings.Contains(err.Error(), reason) {
		t.Fatalf("clean %s 000082 drift returned %v, want %s", fixture.engine, err, reason)
	}
	assertMigrationVersion(t, migrator, latestMigrationVersion)
}
