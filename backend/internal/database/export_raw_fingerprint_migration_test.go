package database

import (
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestExportRawFingerprintMigrationSQLite(t *testing.T) {
	testExportRawFingerprintMigration(t, newSQLiteMigrationFixture(t))
}

func TestExportRawFingerprintMigrationPostgres(t *testing.T) {
	testExportRawFingerprintMigration(t, newRequiredPostgresMigrationFixture(t))
}

func testExportRawFingerprintMigration(t *testing.T, fixture migrationFixture) {
	t.Helper()
	t.Run("upgrade_and_pristine_downgrade_preserve_rows", func(t *testing.T) {
		migrator, db := fixture.openAt(t, 96)
		seedExportRawFingerprintMigration(t, fixture, db)
		before := exportRawFingerprintRows(t, db)
		if err := migrator.Steps(1); err != nil {
			t.Fatalf("upgrade: %v", err)
		}
		assertMigrationVersion(t, migrator, 97)
		assertExportRawFingerprintRows(t, db, before)
		if err := VerifyCurrentSchema(fixture.recoveryWorkerGorm(t, db), fixture.engine); err != nil {
			t.Fatalf("verify upgraded schema: %v", err)
		}
		assertExportRawFingerprintReferences(t, fixture, db)
		if err := migrator.Steps(-1); err != nil {
			t.Fatalf("downgrade old-compatible rows: %v", err)
		}
		assertMigrationVersion(t, migrator, 96)
		assertExportRawFingerprintRows(t, db, before)
		assertExportRawFingerprintReferences(t, fixture, db)
		fixture.expectExecRejected(t, db, `UPDATE backup_asset_export_items SET entry_fingerprint = '', fingerprint_strength = 'none', entry_type = 'directory'`)
		if err := migrator.Steps(1); err != nil {
			t.Fatalf("reupgrade: %v", err)
		}
		assertExportRawFingerprintRows(t, db, before)
	})

	t.Run("admission_and_used_downgrade", func(t *testing.T) {
		migrator, db := fixture.openAt(t, 97)
		seedExportRawFingerprintMigration(t, fixture, db)
		for _, entryType := range []string{"directory", "symlink", "hardlink", "special", "unknown"} {
			fixture.mustExec(t, db, `UPDATE backup_asset_export_items SET entry_fingerprint = '', fingerprint_strength = 'none', entry_type = ?`, entryType)
			before := exportRawFingerprintRows(t, db)
			fixture.expectExecRejected(t, db, `UPDATE schema_migrations SET version = ?, dirty = ?`, 96, false)
			if err := migrator.Steps(-1); err == nil {
				t.Fatalf("down accepted used %s", entryType)
			}
			assertMigrationVersion(t, migrator, 97)
			assertExportRawFingerprintRows(t, db, before)
			fixture.mustExec(t, db, `UPDATE schema_migrations SET version = ?`, 98)
			fixture.mustExec(t, db, `UPDATE schema_migrations SET version = ?`, 97)
		}
		for _, entryType := range []string{"file", "directory", "symlink", "hardlink", "special", "unknown"} {
			for _, strength := range []string{"none", "weak", "strong"} {
				if entryType == "file" || strength != "none" {
					fixture.expectExecRejected(t, db, `UPDATE backup_asset_export_items SET entry_fingerprint = '', fingerprint_strength = ?, entry_type = ?`, strength, entryType)
				}
				fixture.mustExec(t, db, `UPDATE backup_asset_export_items SET entry_fingerprint = 'old-nonempty', fingerprint_strength = ?, entry_type = ?`, strength, entryType)
			}
		}
		fixture.expectExecRejected(t, db, `UPDATE backup_asset_export_items SET entry_fingerprint = ?`, strings.Repeat("x", 129))
		fixture.mustExec(t, db, `UPDATE backup_asset_export_items SET entry_fingerprint = 'old-nonempty', fingerprint_strength = 'strong', entry_type = 'directory'`)
		if err := migrator.Steps(-1); err != nil {
			t.Fatalf("down after all rows become old-compatible: %v", err)
		}
		assertMigrationVersion(t, migrator, 96)
	})

	t.Run("direct_down_guard", func(t *testing.T) {
		migrator, db := fixture.openAt(t, 97)
		seedExportRawFingerprintMigration(t, fixture, db)
		fixture.mustExec(t, db, `UPDATE backup_asset_export_items SET entry_fingerprint = '', fingerprint_strength = 'none', entry_type = 'symlink'`)
		before := exportRawFingerprintRows(t, db)
		if fixture.engine == "sqlite" {
			fixture.mustExec(t, db, `DROP TRIGGER trg_export_raw_fingerprint_downgrade_update_admission`)
			fixture.mustExec(t, db, `DROP TRIGGER trg_export_raw_fingerprint_downgrade_admission`)
		} else {
			fixture.mustExec(t, db, `DROP TRIGGER trg_export_raw_fingerprint_downgrade_admission ON schema_migrations`)
		}
		source := sqliteMigrationsFS
		if fixture.engine == "postgres" {
			source = postgresMigrationsFS
		}
		down, err := source.ReadFile("migrations/" + fixture.engine + "/000097_export_raw_fingerprint.down.sql")
		if err != nil {
			t.Fatal(err)
		}
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		_, downErr := tx.Exec(string(down))
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
		if downErr == nil {
			t.Fatal("direct down discarded new fingerprint state")
		}
		assertMigrationVersion(t, migrator, 97)
		assertExportRawFingerprintRows(t, db, before)
	})

	t.Run("schema_drift", func(t *testing.T) {
		migrator, db := fixture.openAt(t, 97)
		if fixture.engine == "sqlite" {
			fixture.mustExec(t, db, `DROP TRIGGER trg_export_raw_fingerprint_downgrade_admission`)
			fixture.mustExec(t, db, `CREATE TRIGGER trg_export_raw_fingerprint_downgrade_admission BEFORE INSERT ON schema_migrations BEGIN SELECT 1; END`)
		} else {
			fixture.mustExec(t, db, `CREATE OR REPLACE FUNCTION export_raw_fingerprint_downgrade_admission() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END; $$`)
		}
		gormDB := fixture.recoveryWorkerGorm(t, db)
		if err := VerifyCurrentSchema(gormDB, fixture.engine); !errors.Is(err, ErrMigrationSchemaDrift) || !strings.Contains(err.Error(), "invalid_export_raw_fingerprint_admission_trigger") {
			t.Fatalf("offline verification accepted broken guard: %v", err)
		}
		if err := RunMigrations(gormDB, fixture.engine); !errors.Is(err, ErrMigrationSchemaDrift) || !strings.Contains(err.Error(), "invalid_export_raw_fingerprint_admission_trigger") {
			t.Fatalf("startup accepted broken guard: %v", err)
		}
		assertMigrationVersion(t, migrator, 97)
	})
	t.Run("fingerprint_check_drift", func(t *testing.T) {
		migrator, db := fixture.openAt(t, 97)
		if fixture.engine == "sqlite" {
			fixture.mustExec(t, db, `ALTER TABLE backup_asset_export_items ADD COLUMN extra_guard TEXT CHECK (entry_fingerprint <> '')`)
		} else {
			fixture.mustExec(t, db, `ALTER TABLE backup_asset_export_items DROP CONSTRAINT backup_asset_export_items_entry_fingerprint_check`)
			fixture.mustExec(t, db, `ALTER TABLE backup_asset_export_items ADD CONSTRAINT backup_asset_export_items_entry_fingerprint_check CHECK (length(entry_fingerprint) <= 128)`)
		}
		gormDB := fixture.recoveryWorkerGorm(t, db)
		for _, verify := range []func() error{
			func() error { return VerifyCurrentSchema(gormDB, fixture.engine) },
			func() error { return RunMigrations(gormDB, fixture.engine) },
		} {
			if err := verify(); !errors.Is(err, ErrMigrationSchemaDrift) || !strings.Contains(err.Error(), "invalid_export_raw_fingerprint_check") {
				t.Fatalf("fingerprint constraint drift accepted: %v", err)
			}
		}
		assertMigrationVersion(t, migrator, 97)
	})
	if fixture.engine == "sqlite" {
		t.Run("extra_objects_refused_before_dirty", func(t *testing.T) {
			migrator, db := fixture.openAt(t, 96)
			seedExportRawFingerprintMigration(t, fixture, db)
			before := exportRawFingerprintRows(t, db)
			fixture.mustExec(t, db, `CREATE INDEX custom_export_item_index ON backup_asset_export_items(media_type)`)
			fixture.mustExec(t, db, `CREATE TRIGGER custom_export_attempt_trigger AFTER UPDATE ON backup_asset_export_item_attempts BEGIN SELECT 1; END`)
			err := RunMigrations(fixture.recoveryWorkerGorm(t, db), fixture.engine)
			if !errors.Is(err, ErrMigrationSchemaDrift) || !strings.Contains(err.Error(), "unexpected_export_raw_fingerprint_sqlite_object") {
				t.Fatalf("extra schema objects not rejected: %v", err)
			}
			assertMigrationVersion(t, migrator, 96)
			assertExportRawFingerprintRows(t, db, before)
			for _, name := range []string{"custom_export_item_index", "custom_export_attempt_trigger"} {
				var count int
				if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = ?`, name).Scan(&count); err != nil || count != 1 {
					t.Fatalf("extra schema object %s lost: count=%d error=%v", name, count, err)
				}
			}
		})
		t.Run("replaced_named_index_refused_before_dirty", func(t *testing.T) {
			migrator, db := fixture.openAt(t, 96)
			seedExportRawFingerprintMigration(t, fixture, db)
			before := exportRawFingerprintRows(t, db)
			fixture.mustExec(t, db, `DROP INDEX idx_backup_asset_export_items_job_state`)
			fixture.mustExec(t, db, `CREATE INDEX idx_backup_asset_export_items_job_state ON backup_asset_export_items(media_type)`)
			err := RunMigrations(fixture.recoveryWorkerGorm(t, db), fixture.engine)
			if !errors.Is(err, ErrMigrationSchemaDrift) || !strings.Contains(err.Error(), "unexpected_export_raw_fingerprint_sqlite_object") {
				t.Fatalf("replaced index not rejected: %v", err)
			}
			assertMigrationVersion(t, migrator, 96)
			assertExportRawFingerprintRows(t, db, before)
			index, err := migrationIndexContractOf(db, fixture.engine, "backup_asset_export_items", "idx_backup_asset_export_items_job_state")
			if err != nil || !sameMigrationIndexColumns(index.columns, []string{"media_type"}) {
				t.Fatalf("replaced index was not preserved: %+v, %v", index, err)
			}
		})
		t.Run("extra_objects_block_down_before_dirty", func(t *testing.T) {
			migrator, db := fixture.openAt(t, 97)
			seedExportRawFingerprintMigration(t, fixture, db)
			before := exportRawFingerprintRows(t, db)
			fixture.mustExec(t, db, `CREATE INDEX custom_export_attempt_index ON backup_asset_export_item_attempts(state)`)
			fixture.mustExec(t, db, `CREATE TRIGGER custom_export_item_trigger AFTER UPDATE ON backup_asset_export_items BEGIN SELECT 1; END`)
			if err := migrator.Steps(-1); err == nil {
				t.Fatal("down accepted custom objects")
			}
			assertMigrationVersion(t, migrator, 97)
			assertExportRawFingerprintRows(t, db, before)
			for _, name := range []string{"custom_export_attempt_index", "custom_export_item_trigger"} {
				var count int
				if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = ?`, name).Scan(&count); err != nil || count != 1 {
					t.Fatalf("extra schema object %s lost: count=%d error=%v", name, count, err)
				}
			}
		})
		for _, tc := range []struct {
			name    string
			version uint
		}{
			{"external_fk_blocks_upgrade", 96},
			{"external_fk_blocks_down", 97},
		} {
			t.Run(tc.name, func(t *testing.T) {
				migrator, db := fixture.openAt(t, tc.version)
				seedExportRawFingerprintMigration(t, fixture, db)
				before := exportRawFingerprintRows(t, db)
				fixture.mustExec(t, db, `CREATE TABLE custom_export_reference (id INTEGER PRIMARY KEY, item_attempt_id TEXT NOT NULL REFERENCES backup_asset_export_item_attempts(id) ON DELETE CASCADE)`)
				fixture.mustExec(t, db, `INSERT INTO custom_export_reference (id, item_attempt_id) VALUES (1, ?)`, strings.Repeat("7", 32))
				if tc.version == 96 {
					err := RunMigrations(fixture.recoveryWorkerGorm(t, db), fixture.engine)
					if !errors.Is(err, ErrMigrationSchemaDrift) || !strings.Contains(err.Error(), "unexpected_export_raw_fingerprint_sqlite_object") {
						t.Fatalf("incoming foreign key not rejected: %v", err)
					}
				} else if err := migrator.Steps(-1); err == nil {
					t.Fatal("down accepted incoming foreign key")
				}
				assertMigrationVersion(t, migrator, tc.version)
				assertExportRawFingerprintRows(t, db, before)
				var count int
				if err := db.QueryRow(`SELECT COUNT(*) FROM custom_export_reference WHERE id = 1`).Scan(&count); err != nil || count != 1 {
					t.Fatalf("referencing row lost: count=%d error=%v", count, err)
				}
			})
		}
	}
}

func TestExportFingerprintBooleanExpressionPreservesGrouping(t *testing.T) {
	correct := `length(entry_fingerprint) <= 128 AND (length(entry_fingerprint) > 0 OR (fingerprint_strength = 'none' AND entry_type = 'directory'))`
	regrouped := `(length(entry_fingerprint) <= 128 AND length(entry_fingerprint) > 0) OR (fingerprint_strength = 'none' AND entry_type = 'directory')`
	if exportFingerprintBooleanExpression(correct) == exportFingerprintBooleanExpression(regrouped) {
		t.Fatal("regrouped fingerprint check was treated as equivalent")
	}
}

func seedExportRawFingerprintMigration(t *testing.T, fixture migrationFixture, db *sql.DB) {
	t.Helper()
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	fixture.seed068CryptographicRows(t, db, now)
	fixture.mustExec(t, db, `INSERT INTO backup_asset_export_attempts
		(id, job_id, attempt_number, worker_owner, state, fence_token, fence_digest, nonce_prefix,
		 lease_expires_at, is_current, started_at, created_at, updated_at)
		VALUES (?, ?, 1, 'worker', 'active', ?, ?, ?, ?, ?, ?, ?, ?)`,
		strings.Repeat("4", 32), strings.Repeat("1", 32), []byte(strings.Repeat("f", 32)),
		strings.Repeat("b", 64), []byte("12345678"), now.Add(time.Hour), true, now, now, now)
	fixture.mustExec(t, db, `INSERT INTO backup_asset_export_item_attempts
		(id, job_id, item_id, attempt_id, state, started_at, created_at)
		VALUES (?, ?, ?, ?, 'pending', ?, ?)`, strings.Repeat("7", 32), strings.Repeat("1", 32),
		strings.Repeat("3", 32), strings.Repeat("4", 32), now, now)
}

func exportRawFingerprintRows(t *testing.T, db *sql.DB) map[string][][]any {
	t.Helper()
	rows := make(map[string][][]any)
	for _, table := range []string{"backup_asset_export_jobs", "backup_asset_export_items", "backup_asset_export_attempts", "backup_asset_export_item_attempts"} {
		rows[table] = accountRecoveryMigrationSnapshot(t, db, `SELECT * FROM `+table+` ORDER BY id`)
		if len(rows[table]) != 1 {
			t.Fatalf("missing seeded %s row", table)
		}
	}
	return rows
}

func assertExportRawFingerprintRows(t *testing.T, db *sql.DB, want map[string][][]any) {
	t.Helper()
	if got := exportRawFingerprintRows(t, db); !reflect.DeepEqual(got, want) {
		t.Fatalf("export evidence changed across migration: got %#v, want %#v", got, want)
	}
}

func assertExportRawFingerprintReferences(t *testing.T, fixture migrationFixture, db *sql.DB) {
	t.Helper()
	index, err := migrationIndexContractOf(db, fixture.engine, "backup_asset_export_items", "idx_backup_asset_export_items_job_state")
	if err != nil || !migrationIndexUsable(index) || index.unique || !sameMigrationIndexColumns(index.columns, []string{"job_id", "state", "ordinal"}) {
		t.Fatalf("item index lost: %+v, %v", index, err)
	}
	fixture.expectExecRejected(t, db, `UPDATE backup_asset_export_item_attempts SET item_id = ?`, strings.Repeat("8", 32))
	fixture.expectExecRejected(t, db, `UPDATE backup_asset_export_items SET job_id = ?`, strings.Repeat("8", 32))
}
