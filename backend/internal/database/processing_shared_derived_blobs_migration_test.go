package database

import (
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestProcessingSharedDerivedBlobsMigrationSQLite(t *testing.T) {
	testProcessingSharedDerivedBlobsMigration(t, newSQLiteMigrationFixture(t))
}

func TestProcessingSharedDerivedBlobsMigrationPostgres(t *testing.T) {
	testProcessingSharedDerivedBlobsMigration(t, newRequiredPostgresMigrationFixture(t))
}

func testProcessingSharedDerivedBlobsMigration(t *testing.T, fixture migrationFixture) {
	t.Helper()
	t.Run("upgrade_and_pristine_downgrade_preserve_rows", func(t *testing.T) {
		migrator, db := fixture.openAt(t, 97)
		seedProcessingSharedDerivedBlobs(t, fixture, db)
		before := processingSharedDerivedBlobsRows(t, db)
		if err := migrator.Steps(1); err != nil {
			t.Fatalf("upgrade: %v", err)
		}
		assertMigrationVersion(t, migrator, 98)
		if err := validateMinimumRecoverySchema(db, fixture.engine, 98); err != nil {
			t.Fatalf("verify: %v", err)
		}
		assertProcessingSharedDerivedBlobsRows(t, db, before)
		if err := migrator.Steps(-1); err != nil {
			t.Fatalf("downgrade: %v", err)
		}
		assertMigrationVersion(t, migrator, 97)
		assertProcessingSharedDerivedBlobsRows(t, db, before)
		fixture.expectExecRejected(t, db, `UPDATE backup_asset_processing_uploads SET staging_id = ? WHERE id = ?`, strings.Repeat("1", 32), strings.Repeat("2", 32))
		fixture.expectExecRejected(t, db, `UPDATE backup_asset_derived_artifact_sets SET manifest_digest = ? WHERE id = ?`, strings.Repeat("3", 64), strings.Repeat("4", 32))
		if err := migrator.Steps(1); err != nil {
			t.Fatalf("reupgrade: %v", err)
		}
		assertProcessingSharedDerivedBlobsRows(t, db, before)
	})
	t.Run("admission_and_used_downgrade", func(t *testing.T) {
		migrator, db := fixture.openAt(t, 98)
		seedProcessingSharedDerivedBlobs(t, fixture, db)
		for _, tc := range []struct{ table, column, id, shared, unique string }{
			{"backup_asset_processing_uploads", "staging_id", strings.Repeat("2", 32), strings.Repeat("1", 32), strings.Repeat("2", 32)},
			{"backup_asset_derived_artifact_sets", "manifest_digest", strings.Repeat("4", 32), strings.Repeat("3", 64), strings.Repeat("4", 64)},
		} {
			fixture.mustExec(t, db, `UPDATE `+tc.table+` SET `+tc.column+` = ? WHERE id = ?`, tc.shared, tc.id)
			before := processingSharedDerivedBlobsRows(t, db)
			fixture.expectExecRejected(t, db, `UPDATE schema_migrations SET version = ?`, 97)
			if err := migrator.Steps(-1); err == nil {
				t.Fatal("downgrade accepted shared values")
			}
			assertMigrationVersion(t, migrator, 98)
			assertProcessingSharedDerivedBlobsRows(t, db, before)
			fixture.mustExec(t, db, `UPDATE schema_migrations SET version = ?`, 99)
			fixture.mustExec(t, db, `UPDATE schema_migrations SET version = ?`, 98)
			fixture.mustExec(t, db, `UPDATE `+tc.table+` SET `+tc.column+` = ? WHERE id = ?`, tc.unique, tc.id)
		}
		if err := migrator.Steps(-1); err != nil {
			t.Fatalf("downgrade after resolving duplicates: %v", err)
		}
		assertMigrationVersion(t, migrator, 97)
	})
	t.Run("direct_down_guard", func(t *testing.T) {
		for _, tc := range []struct{ name, table, column, id, value string }{
			{"staging", "backup_asset_processing_uploads", "staging_id", strings.Repeat("2", 32), strings.Repeat("1", 32)},
			{"manifest", "backup_asset_derived_artifact_sets", "manifest_digest", strings.Repeat("4", 32), strings.Repeat("3", 64)},
		} {
			t.Run(tc.name, func(t *testing.T) {
				migrator, db := fixture.openAt(t, 98)
				seedProcessingSharedDerivedBlobs(t, fixture, db)
				fixture.mustExec(t, db, `UPDATE `+tc.table+` SET `+tc.column+` = ? WHERE id = ?`, tc.value, tc.id)
				before := processingSharedDerivedBlobsRows(t, db)
				if fixture.engine == "sqlite" {
					fixture.mustExec(t, db, `DROP TRIGGER trg_processing_shared_derived_blobs_downgrade_admission`)
					fixture.mustExec(t, db, `DROP TRIGGER trg_processing_shared_derived_blobs_downgrade_update_admission`)
				} else {
					fixture.mustExec(t, db, `DROP TRIGGER trg_processing_shared_derived_blobs_downgrade_admission ON schema_migrations`)
				}
				source := sqliteMigrationsFS
				if fixture.engine == "postgres" {
					source = postgresMigrationsFS
				}
				down, err := source.ReadFile("migrations/" + fixture.engine + "/000098_processing_shared_derived_blobs.down.sql")
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
					t.Fatal("direct down accepted shared values")
				}
				assertMigrationVersion(t, migrator, 98)
				assertProcessingSharedDerivedBlobsRows(t, db, before)
			})
		}
	})
	t.Run("schema_drift", func(t *testing.T) {
		for _, tc := range []struct{ name, sqlite, postgres string }{
			{"trigger", `DROP TRIGGER trg_processing_shared_derived_blobs_downgrade_admission; CREATE TRIGGER trg_processing_shared_derived_blobs_downgrade_admission BEFORE INSERT ON schema_migrations BEGIN SELECT 1; END`, `CREATE OR REPLACE FUNCTION processing_shared_derived_blobs_downgrade_admission() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END; $$`},
			{"index", `DROP INDEX idx_backup_asset_processing_uploads_staging_id`, `DROP INDEX idx_backup_asset_processing_uploads_staging_id`},
			{"staging_unique", `CREATE UNIQUE INDEX drift_upload_unique ON backup_asset_processing_uploads(staging_id)`, `CREATE UNIQUE INDEX drift_upload_unique ON backup_asset_processing_uploads(staging_id)`},
			{"manifest_unique", `CREATE UNIQUE INDEX drift_manifest_unique ON backup_asset_derived_artifact_sets(manifest_digest)`, `CREATE UNIQUE INDEX drift_manifest_unique ON backup_asset_derived_artifact_sets(manifest_digest)`},
		} {
			t.Run(tc.name, func(t *testing.T) {
				migrator, db := fixture.openAt(t, uint(latestMigrationVersion))
				query := tc.sqlite
				if fixture.engine == "postgres" {
					query = tc.postgres
				}
				fixture.mustExec(t, db, query)
				gormDB := fixture.recoveryWorkerGorm(t, db)
				for _, verify := range []func() error{func() error { return VerifyCurrentSchema(gormDB, fixture.engine) }, func() error { return RunMigrations(gormDB, fixture.engine) }} {
					if err := verify(); !errors.Is(err, ErrMigrationSchemaDrift) {
						t.Fatalf("schema drift accepted: %v", err)
					}
				}
				assertMigrationVersion(t, migrator, uint(latestMigrationVersion))
			})
		}
	})
	if fixture.engine == "sqlite" {
		for _, tc := range []struct {
			name    string
			version uint
			setup   string
		}{
			{"extra_objects_refused_before_dirty", 97, `CREATE INDEX custom_processing_index ON backup_asset_processing_uploads(ordinal); CREATE TRIGGER custom_processing_guard AFTER UPDATE ON backup_asset_derived_artifacts BEGIN SELECT 1; END`},
			{"replaced_named_index_refused_before_dirty", 97, `DROP INDEX idx_backup_asset_processing_uploads_staging; CREATE INDEX idx_backup_asset_processing_uploads_staging ON backup_asset_processing_uploads(ordinal)`},
			{"extra_objects_block_down_before_dirty", 98, `CREATE INDEX custom_processing_index ON backup_asset_derived_artifacts(ordinal); CREATE TRIGGER custom_processing_guard AFTER UPDATE ON backup_asset_processing_uploads BEGIN SELECT 1; END`},
			{"external_fk_blocks_upgrade", 97, `CREATE TABLE custom_processing_reference (id INTEGER PRIMARY KEY, artifact_id TEXT REFERENCES backup_asset_derived_artifacts(id))`},
			{"external_fk_blocks_down", 98, `CREATE TABLE custom_processing_reference (id INTEGER PRIMARY KEY, artifact_id TEXT REFERENCES backup_asset_derived_artifacts(id))`},
		} {
			t.Run(tc.name, func(t *testing.T) {
				migrator, db := fixture.openAt(t, tc.version)
				seedProcessingSharedDerivedBlobs(t, fixture, db)
				before := processingSharedDerivedBlobsRows(t, db)
				fixture.mustExec(t, db, tc.setup)
				if strings.HasPrefix(tc.name, "external_fk_") {
					fixture.mustExec(t, db, `INSERT INTO custom_processing_reference (id, artifact_id) VALUES (1, ?)`, strings.Repeat("5", 32))
				}
				if tc.version == 97 {
					if err := RunMigrations(fixture.recoveryWorkerGorm(t, db), fixture.engine); !errors.Is(err, ErrMigrationPrecondition) {
						t.Fatalf("upgrade accepted extra object: %v", err)
					}
				} else if err := migrator.Steps(-1); err == nil {
					t.Fatal("downgrade accepted extra object")
				}
				assertMigrationVersion(t, migrator, tc.version)
				assertProcessingSharedDerivedBlobsRows(t, db, before)
				if strings.HasPrefix(tc.name, "external_fk_") {
					var count int
					if err := db.QueryRow(`SELECT COUNT(*) FROM custom_processing_reference WHERE id = 1`).Scan(&count); err != nil || count != 1 {
						t.Fatalf("external reference lost: %d, %v", count, err)
					}
				}
				if tc.name == "extra_objects_refused_before_dirty" || tc.name == "extra_objects_block_down_before_dirty" {
					var count int
					if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name IN ('custom_processing_index', 'custom_processing_guard')`).Scan(&count); err != nil || count != 2 {
						t.Fatalf("extra objects lost: %d, %v", count, err)
					}
				}
			})
		}
	}
}

func processingSharedDerivedBlobsRows(t *testing.T, db *sql.DB) map[string][][]any {
	t.Helper()
	result := make(map[string][][]any)
	for _, table := range []string{"backup_asset_processing_uploads", "backup_asset_derived_blobs", "backup_asset_derived_artifact_sets", "backup_asset_derived_artifacts", "backup_asset_derived_blob_references"} {
		result[table] = accountRecoveryMigrationSnapshot(t, db, `SELECT * FROM `+table+` ORDER BY id`)
		if len(result[table]) == 0 {
			t.Fatalf("missing seeded row in %s", table)
		}
	}
	return result
}

func assertProcessingSharedDerivedBlobsRows(t *testing.T, db *sql.DB, want map[string][][]any) {
	t.Helper()
	if got := processingSharedDerivedBlobsRows(t, db); !reflect.DeepEqual(got, want) {
		t.Fatalf("migration changed rows: got %#v, want %#v", got, want)
	}
}

func seedProcessingSharedDerivedBlobs(t *testing.T, fixture migrationFixture, db *sql.DB) {
	t.Helper()
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	point, catalog, entry := fixture.insertSearchMigrationCatalog(t, db, "9", now)
	job1, job2 := strings.Repeat("a", 32), strings.Repeat("b", 32)
	worker, lease := strings.Repeat("d", 32), strings.Repeat("e", 32)
	attempt1, attempt2 := strings.Repeat("f", 32), strings.Repeat("0", 32)
	grant := strings.Repeat("a", 32)
	fixture.insertProcessingMigrationJob(t, db, job1, point, catalog, entry, now)
	fixture.mustExec(t, db, `UPDATE backup_asset_processing_jobs SET work_key = ? WHERE id = ?`, strings.Repeat("b", 64), job1)
	fixture.insertProcessingMigrationJob(t, db, job2, point, catalog, entry, now)
	fixture.insertProcessingMigrationWorker(t, db, worker, now)
	fixture.insertSearchMigrationLease(t, db, lease, point, "processing_job", now)
	fixture.insertProcessingMigrationAttempt(t, db, attempt1, job1, worker, lease, now)
	fixture.insertProcessingMigrationAttempt(t, db, attempt2, job2, worker, lease, now)
	fixture.mustExec(t, db, `INSERT INTO backup_asset_processing_grants
		(id, job_id, attempt_id, worker_id, kind, activation_secret_hash, fence_hash, state,
		 max_requests, max_bytes_per_request, max_cumulative_bytes, max_in_flight, expires_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'sink', ?, ?, 'issued', 10, 10, 100, 2, ?, ?, ?)`,
		grant, job1, attempt1, worker, strings.Repeat("a", 64), strings.Repeat("b", 64), now.Add(time.Hour), now, now)
	for _, marker := range []string{"1", "2"} {
		ordinal := 0
		if marker == "2" {
			ordinal = 1
		}
		fixture.mustExec(t, db, `INSERT INTO backup_asset_processing_uploads
			(id, job_id, attempt_id, grant_id, ordinal, role, media_type, declared_size, declared_digest,
			 completeness, coverage_canonical, staging_id, state, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, 'content', 'text/plain', 1, ?, 'complete', ?, ?, 'reserved', ?, ?)`,
			strings.Repeat(marker, 32), job1, attempt1, grant, ordinal, strings.Repeat("5", 64), []byte{1}, strings.Repeat(marker, 32), now, now)
	}
	fixture.mustExec(t, db, `INSERT INTO backup_asset_derived_blobs
		(id, plaintext_digest, plaintext_size, physical_size, cipher_format_version, chunk_size, chunk_count,
		 nonce_prefix, opaque_locator, wrapped_dek, envelope_nonce, derived_kek_version, state, ref_count, created_at, updated_at)
		VALUES (?, ?, 1, 32, 1, 65536, 1, ?, 'blob-098-test', ?, ?, 1, 'active', 1, ?, ?)`,
		strings.Repeat("1", 32), strings.Repeat("5", 64), []byte("12345678"), []byte("wrapped"), []byte("123456789012"), now, now)
	for _, set := range []struct{ marker, job, attempt string }{{"3", job1, attempt1}, {"4", job2, attempt2}} {
		fixture.mustExec(t, db, `INSERT INTO backup_asset_derived_artifact_sets
			(id, job_id, attempt_id, work_key, recovery_point_id, catalog_generation_id, entry_id,
			 source_fingerprint, security_policy_revision, manifest_digest, state, completeness, artifact_count,
			 total_plaintext_bytes, projection_required, projection_published, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, 'source-v1', 'policy-v1', ?, 'active', 'complete', 1, 1, ?, ?, ?, ?)`,
			strings.Repeat(set.marker, 32), set.job, set.attempt, strings.Repeat(set.marker, 64), point, catalog, entry,
			strings.Repeat(set.marker, 64), false, false, now, now)
	}
	fixture.mustExec(t, db, `INSERT INTO backup_asset_derived_artifacts
		(id, artifact_set_id, ordinal, role, media_type, plaintext_size, plaintext_digest, completeness, coverage_canonical, blob_id, created_at)
		VALUES (?, ?, 0, 'content', 'text/plain', 1, ?, 'complete', ?, ?, ?)`,
		strings.Repeat("5", 32), strings.Repeat("3", 32), strings.Repeat("5", 64), []byte{1}, strings.Repeat("1", 32), now)
	fixture.mustExec(t, db, `INSERT INTO backup_asset_derived_blob_references
		(id, blob_id, artifact_id, recovery_point_id, catalog_generation_id, entry_id, source_fingerprint, state, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 'source-v1', 'active', ?, ?)`,
		strings.Repeat("6", 32), strings.Repeat("1", 32), strings.Repeat("5", 32), point, catalog, entry, now, now)
}
