package database

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
)

func TestCronBackupHealthMigration(t *testing.T) {
	testCronBackupHealthMigration(t, newSQLiteMigrationFixture(t))
}

func TestCronBackupHealthMigrationPostgres(t *testing.T) {
	testCronBackupHealthMigration(t, newRequiredPostgresMigrationFixture(t))
}

func testCronBackupHealthMigration(t *testing.T, fixture migrationFixture) {
	t.Helper()

	t.Run("apply validates constraints and defaults", func(t *testing.T) {
		migrator, db := fixture.openAt(t, uint(serviceMonitorRetirementSchemaVersion))
		if err := migrator.Steps(1); err != nil {
			t.Fatalf("apply 000092 on %s: %v", fixture.engine, err)
		}
		assertMigrationVersion(t, migrator, uint(cronBackupHealthSchemaVersion))
		if err := validateMinimumRecoverySchema(db, fixture.engine, cronBackupHealthSchemaVersion); err != nil {
			t.Fatalf("validate 000092 on %s: %v", fixture.engine, err)
		}

		now := time.Date(2026, 10, 9, 8, 7, 6, 0, time.UTC)
		longErrorCode := "XR-CRON-DB-BACKUP-" + strings.Repeat("a", 64)
		fixture.mustExec(t, db, `INSERT INTO alerts
			(id, node_id, node_name, severity, status, error_code, message, retryable, created_at, updated_at)
			VALUES (9201, 0, 'cron-health-fixture', 'warning', 'open',
				?, 'cron health fixture', FALSE, ?, ?)`, longErrorCode, now, now)
		key := strings.Repeat("a", 64)
		fixture.mustExec(t, db, `INSERT INTO cron_backup_health
			(source_key, enrolled_at, updated_at) VALUES (?, ?, ?)`, key, now, now)

		var sourceID string
		var highestRevision int64
		var faultActive bool
		if err := db.QueryRow(fixture.bind(`SELECT source_id, highest_revision, fault_active
			FROM cron_backup_health WHERE source_key = ?`), key).
			Scan(&sourceID, &highestRevision, &faultActive); err != nil {
			t.Fatalf("read cron health defaults on %s: %v", fixture.engine, err)
		}
		if sourceID != "" || highestRevision != 0 || faultActive {
			t.Fatalf("cron health defaults on %s = source_id=%q highest_revision=%d fault_active=%v",
				fixture.engine, sourceID, highestRevision, faultActive)
		}

		fixture.mustExec(t, db, `INSERT INTO cron_backup_health
			(source_key, source_id, highest_revision, enrolled_at, fault_active, alert_id, updated_at)
			VALUES (?, ?, ?, ?, TRUE, ?, ?)`, strings.Repeat("c", 64), strings.Repeat("b", 32), 7, now, 9201, now)
		fixture.expectExecRejected(t, db, `INSERT INTO cron_backup_health
			(source_key, enrolled_at, updated_at) VALUES (?, ?, ?)`, strings.Repeat("d", 63), now, now)
		fixture.expectExecRejected(t, db, `INSERT INTO cron_backup_health
			(source_key, source_id, enrolled_at, updated_at) VALUES (?, ?, ?, ?)`, strings.Repeat("e", 64), strings.Repeat("g", 32), now, now)
		fixture.expectExecRejected(t, db, `INSERT INTO cron_backup_health
			(source_key, highest_revision, enrolled_at, updated_at) VALUES (?, ?, ?, ?)`, strings.Repeat("f", 64), -1, now, now)
		fixture.expectExecRejected(t, db, `INSERT INTO cron_backup_health
			(source_key, fault_active, enrolled_at, updated_at) VALUES (?, TRUE, ?, ?)`, strings.Repeat("1", 64), now, now)
		fixture.expectExecRejected(t, db, `INSERT INTO cron_backup_health
			(source_key, alert_id, enrolled_at, updated_at) VALUES (?, ?, ?, ?)`, strings.Repeat("2", 64), 929999, now, now)
		fixture.expectExecRejected(t, db, `INSERT INTO cron_backup_health_usage(id) VALUES (2)`)
		fixture.mustExec(t, db, `INSERT INTO cron_backup_health_usage(id) VALUES (1)`)
		fixture.expectExecRejected(t, db, `INSERT INTO cron_backup_health_usage(id) VALUES (1)`)
	})

	t.Run("unused down removes schema and guards", func(t *testing.T) {
		migrator, db := fixture.openAt(t, uint(serviceMonitorRetirementSchemaVersion))
		if err := migrator.Steps(1); err != nil {
			t.Fatalf("apply unused 000092 on %s: %v", fixture.engine, err)
		}
		if err := migrator.Steps(-1); err != nil {
			t.Fatalf("unused down 000092 on %s: %v", fixture.engine, err)
		}
		assertMigrationVersion(t, migrator, uint(serviceMonitorRetirementSchemaVersion))
		for _, table := range []string{"cron_backup_health", "cron_backup_health_usage"} {
			if databaseTableExists(t, db, fixture.engine, table) {
				t.Fatalf("unused 000092 down on %s retained %s", fixture.engine, table)
			}
		}
		if fixture.recoveryTriggerExists(t, db, "schema_migrations", cronBackupHealthAdmissionTrigger) {
			t.Fatalf("unused 000092 down on %s retained insert/update guard", fixture.engine)
		}
		if fixture.engine == "sqlite" && fixture.recoveryTriggerExists(t, db, "schema_migrations", cronBackupHealthUpdateAdmissionTrigger) {
			t.Fatalf("unused 000092 down on SQLite retained update guard")
		}
	})

	t.Run("used marker survives cursor deletion and rejects both metadata paths", func(t *testing.T) {
		migrator, db := fixture.openAt(t, uint(serviceMonitorRetirementSchemaVersion))
		if err := migrator.Steps(1); err != nil {
			t.Fatalf("apply used 000092 on %s: %v", fixture.engine, err)
		}
		now := time.Date(2026, 10, 9, 9, 8, 7, 0, time.UTC)
		fixture.mustExec(t, db, `INSERT INTO cron_backup_health_usage(id) VALUES (1)`)
		fixture.mustExec(t, db, `INSERT INTO cron_backup_health
			(source_key, enrolled_at, updated_at) VALUES (?, ?, ?)`, strings.Repeat("a", 64), now, now)
		fixture.mustExec(t, db, `DELETE FROM cron_backup_health`)

		fixture.expectExecRejected(t, db, `UPDATE schema_migrations
			SET version = ?, dirty = TRUE WHERE version = ?`, serviceMonitorRetirementSchemaVersion, cronBackupHealthSchemaVersion)
		if err := migrator.Steps(-1); err == nil {
			t.Fatalf("used 000092 down on %s unexpectedly succeeded", fixture.engine)
		}
		assertMigrationVersion(t, migrator, uint(cronBackupHealthSchemaVersion))
		for _, table := range []string{"cron_backup_health", "cron_backup_health_usage"} {
			if !databaseTableExists(t, db, fixture.engine, table) {
				t.Fatalf("used 000092 down on %s removed %s", fixture.engine, table)
			}
		}
		var markerCount int
		if err := db.QueryRow(`SELECT COUNT(*) FROM cron_backup_health_usage`).Scan(&markerCount); err != nil {
			t.Fatalf("read marker after rejected down on %s: %v", fixture.engine, err)
		}
		if markerCount != 1 {
			t.Fatalf("marker count after rejected down on %s = %d, want 1", fixture.engine, markerCount)
		}
	})

	t.Run("long alert code blocks downgrade without marker", func(t *testing.T) {
		migrator, db := fixture.openAt(t, uint(serviceMonitorRetirementSchemaVersion))
		if err := migrator.Steps(1); err != nil {
			t.Fatalf("apply long-code 000092 on %s: %v", fixture.engine, err)
		}
		longErrorCode := "XR-CRON-DB-BACKUP-" + strings.Repeat("b", 64)
		now := time.Date(2026, 10, 9, 10, 9, 8, 0, time.UTC)
		fixture.mustExec(t, db, `INSERT INTO alerts
			(id, node_id, node_name, severity, status, error_code, message, retryable, created_at, updated_at)
			VALUES (9202, 0, 'cron-health-long-code', 'warning', 'open',
				?, 'cron health long code', FALSE, ?, ?)`, longErrorCode, now, now)
		fixture.expectExecRejected(t, db, `UPDATE schema_migrations
			SET version = ?, dirty = TRUE WHERE version = ?`,
			serviceMonitorRetirementSchemaVersion, cronBackupHealthSchemaVersion)
		if err := migrator.Steps(-1); err == nil {
			t.Fatalf("long alert code down on %s unexpectedly succeeded", fixture.engine)
		}
		assertMigrationVersion(t, migrator, uint(cronBackupHealthSchemaVersion))
		for _, table := range []string{"cron_backup_health", "cron_backup_health_usage"} {
			if !databaseTableExists(t, db, fixture.engine, table) {
				t.Fatalf("long alert code down on %s removed %s", fixture.engine, table)
			}
		}
		var markerCount int
		if err := db.QueryRow(`SELECT COUNT(*) FROM cron_backup_health_usage`).Scan(&markerCount); err != nil {
			t.Fatalf("read unused marker after long-code down on %s: %v", fixture.engine, err)
		}
		if markerCount != 0 {
			t.Fatalf("long-code down on %s unexpectedly created usage marker", fixture.engine)
		}
	})

	t.Run("alert code capacity drift is schema drift", func(t *testing.T) {
		if fixture.engine != "postgres" {
			return
		}
		migrator, db := fixture.openAt(t, uint(cronBackupHealthSchemaVersion))
		fixture.mustExec(t, db, `ALTER TABLE alerts ALTER COLUMN error_code TYPE VARCHAR(64)`)
		assertRunMigrationsCronBackupHealthSchemaDrift(
			t, fixture, migrator, db, "invalid_cron_backup_alert_error_code_column",
		)
	})

	t.Run("missing foreign key is schema drift", func(t *testing.T) {
		migrator, db := fixture.openAt(t, uint(cronBackupHealthSchemaVersion))
		if fixture.engine == "postgres" {
			fixture.mustExec(t, db, `ALTER TABLE cron_backup_health DROP CONSTRAINT cron_backup_health_alert_fk`)
		} else {
			fixture.mustExec(t, db, `PRAGMA foreign_keys = OFF`)
			fixture.mustExec(t, db, `ALTER TABLE cron_backup_health RENAME TO cron_backup_health_original`)
			fixture.mustExec(t, db, `CREATE TABLE cron_backup_health (
				source_key TEXT NOT NULL PRIMARY KEY
					CHECK (length(source_key) = 64 AND source_key NOT GLOB '*[^0-9a-f]*'),
				source_id TEXT NOT NULL DEFAULT ''
					CHECK (source_id = '' OR (length(source_id) = 32 AND source_id NOT GLOB '*[^0-9a-f]*')),
				highest_revision INTEGER NOT NULL DEFAULT 0 CHECK (highest_revision >= 0),
				enrolled_at DATETIME NOT NULL,
				fault_active BOOLEAN NOT NULL DEFAULT 0,
				alert_id INTEGER,
				updated_at DATETIME NOT NULL,
				CHECK (fault_active = 0 OR alert_id IS NOT NULL)
			)`)
			fixture.mustExec(t, db, `INSERT INTO cron_backup_health
				(source_key, source_id, highest_revision, enrolled_at, fault_active, alert_id, updated_at)
				SELECT source_key, source_id, highest_revision, enrolled_at, fault_active, alert_id, updated_at
				FROM cron_backup_health_original`)
			fixture.mustExec(t, db, `DROP TABLE cron_backup_health_original`)
			fixture.mustExec(t, db, `PRAGMA foreign_keys = ON`)
		}
		assertRunMigrationsCronBackupHealthSchemaDrift(t, fixture, migrator, db, "invalid_cron_backup_health_foreign_key")
	})

	t.Run("no-op admission guard is schema drift", func(t *testing.T) {
		migrator, db := fixture.openAt(t, uint(cronBackupHealthSchemaVersion))
		if fixture.engine == "postgres" {
			fixture.mustExec(t, db, `CREATE OR REPLACE FUNCTION cron_backup_health_downgrade_admission()
				RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END; $$`)
		} else {
			fixture.mustExec(t, db, `DROP TRIGGER trg_cron_backup_health_downgrade_admission`)
			fixture.mustExec(t, db, `CREATE TRIGGER trg_cron_backup_health_downgrade_admission
				BEFORE INSERT ON schema_migrations BEGIN SELECT 1; END`)
		}
		assertRunMigrationsCronBackupHealthSchemaDrift(t, fixture, migrator, db, "invalid_cron_backup_health_admission_trigger")
	})
}

func assertRunMigrationsCronBackupHealthSchemaDrift(
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
	if beforeVersion != uint(cronBackupHealthSchemaVersion) || beforeDirty {
		t.Fatalf("preflight migration state got version=%d dirty=%v, want version=%d clean",
			beforeVersion, beforeDirty, cronBackupHealthSchemaVersion)
	}

	err = RunMigrations(fixture.recoveryWorkerGorm(t, db), fixture.engine)
	if !errors.Is(err, ErrMigrationSchemaDrift) || !strings.Contains(err.Error(), reason) {
		t.Fatalf("clean %s 000092 drift returned %v, want %s", fixture.engine, err, reason)
	}
	afterVersion, afterDirty, err := migrator.Version()
	if err != nil {
		t.Fatalf("read postflight migration version: %v", err)
	}
	if afterVersion != beforeVersion || afterDirty != beforeDirty {
		t.Fatalf("schema drift changed migration metadata: before version=%d dirty=%v, after version=%d dirty=%v",
			beforeVersion, beforeDirty, afterVersion, afterDirty)
	}
}
