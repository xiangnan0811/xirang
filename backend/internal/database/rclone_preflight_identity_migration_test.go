package database

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/secure"
)

const rclonePreflightIdentityMigrationVersion = 95

func TestRclonePreflightIdentityMigrationSQLite(t *testing.T) {
	testRclonePreflightIdentityMigration(t, newSQLiteMigrationFixture(t))
}

func TestRclonePreflightIdentityMigrationPostgres(t *testing.T) {
	testRclonePreflightIdentityMigration(t, newRequiredPostgresMigrationFixture(t))
}

func testRclonePreflightIdentityMigration(t *testing.T, fixture migrationFixture) {
	t.Helper()
	t.Run("upgrade preserves old keys and indexes", func(t *testing.T) {
		migrator, db := fixture.openAt(t, 94)
		for i, domain := range []string{"entry_identity", "cursor_signing", "audit_fingerprint", "recovery_cleanup_ownership", "search_token", "derived_store", "export_store"} {
			insertRcloneIdentityMigrationKey(t, fixture, db, fmt.Sprintf("%032x", i+1), domain, "active", 1)
		}
		before := accountRecoveryMigrationSnapshot(t, db, `SELECT * FROM wrapped_domain_keys ORDER BY domain, version`)
		if err := migrator.Steps(1); err != nil {
			t.Fatalf("upgrade: %v", err)
		}
		assertMigrationVersion(t, migrator, 95)
		if after := accountRecoveryMigrationSnapshot(t, db, `SELECT * FROM wrapped_domain_keys ORDER BY domain, version`); !reflect.DeepEqual(before, after) {
			t.Fatal("upgrade changed existing key rows")
		}
		for _, index := range []string{"idx_wrapped_domain_keys_active", "idx_wrapped_domain_keys_domain_state"} {
			var count int
			query := `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`
			if fixture.engine == "postgres" {
				query = `SELECT COUNT(*) FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ?`
			}
			if err := db.QueryRow(fixture.bind(query), index).Scan(&count); err != nil || count != 1 {
				t.Fatalf("index %s count=%d error=%v", index, count, err)
			}
		}
		insertRcloneIdentityMigrationKey(t, fixture, db, fmt.Sprintf("%032x", 8), "rclone_preflight_identity", "active", 1)
		insert := `INSERT INTO wrapped_domain_keys (id, domain, version, state, wrapped_key, wrap_algorithm, wrapping_key_fingerprint, activated_at, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
		now := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
		for _, candidate := range []struct {
			id, domain, state string
			version           int
		}{
			{fmt.Sprintf("%032x", 9), "unknown_domain", "active", 1},
			{fmt.Sprintf("%032x", 10), "rclone_preflight_identity", "active", 2},
			{fmt.Sprintf("%032x", 11), "rclone_preflight_identity", "retired", 1},
		} {
			fixture.expectExecRejected(t, db, insert, candidate.id, candidate.domain, candidate.version, candidate.state, "envelope", "algorithm", "fingerprint", now, now, now)
		}
	})

	t.Run("upgrade initializes stable key and rewraps it", func(t *testing.T) {
		t.Setenv("APP_ENV", "development")
		t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_MIGRATION_PREFLIGHT_OLD_KEY_FOR_TEST_ONLY")
		t.Setenv("DATA_ENCRYPTION_LEGACY_KEY", "")
		secure.ResetForTesting()
		t.Cleanup(secure.ResetForTesting)
		migrator, db := fixture.openAt(t, 94)
		if err := migrator.Steps(1); err != nil {
			t.Fatal(err)
		}
		ring := backupasset.NewKeyring(fixture.recoveryWorkerGorm(t, db), nil)
		ctx := context.Background()
		before, err := ring.Ensure(ctx, backupasset.KeyDomainRclonePreflightIdentity)
		if err != nil {
			t.Fatalf("initialize new domain: %v", err)
		}
		t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_MIGRATION_PREFLIGHT_NEW_KEY_FOR_TEST_ONLY")
		t.Setenv("DATA_ENCRYPTION_LEGACY_KEY", "FAKE_MIGRATION_PREFLIGHT_OLD_KEY_FOR_TEST_ONLY")
		secure.ResetForTesting()
		if _, err := ring.RewrapDomains(ctx, backupasset.KeyDomainRclonePreflightIdentity); err != nil {
			t.Fatal(err)
		}
		t.Setenv("DATA_ENCRYPTION_LEGACY_KEY", "")
		secure.ResetForTesting()
		after, err := ring.Ensure(ctx, backupasset.KeyDomainRclonePreflightIdentity)
		if err != nil || before.ID != after.ID || before.Version != after.Version || !bytes.Equal(before.Key, after.Key) {
			t.Fatalf("rewrap changed identity: %v", err)
		}
	})

	t.Run("pristine down and reapply", func(t *testing.T) {
		migrator, db := fixture.openAt(t, 95)
		if err := migrator.Steps(-1); err != nil {
			t.Fatalf("pristine down: %v", err)
		}
		assertMigrationVersion(t, migrator, 94)
		for _, name := range []string{"trg_rclone_preflight_identity_downgrade_admission", "trg_rclone_preflight_identity_downgrade_update_admission"} {
			if exists, err := migrationTriggerExists(db, fixture.engine, "schema_migrations", name); err != nil || exists {
				t.Fatalf("down retained %s: exists=%v error=%v", name, exists, err)
			}
		}
		if err := migrator.Steps(1); err != nil {
			t.Fatalf("reapply: %v", err)
		}
		assertMigrationVersion(t, migrator, 95)
	})

	for _, state := range []string{"active", "retired", "lost"} {
		t.Run(state+" blocks down before metadata becomes dirty", func(t *testing.T) {
			migrator, db := fixture.openAt(t, 95)
			insertRcloneIdentityMigrationKey(t, fixture, db, fmt.Sprintf("%032x", 12), "rclone_preflight_identity", state, 1)
			before := accountRecoveryMigrationSnapshot(t, db, `SELECT * FROM wrapped_domain_keys ORDER BY domain, version`)
			if err := migrator.Steps(-1); err == nil {
				t.Fatal("used key domain allowed down")
			}
			assertMigrationVersion(t, migrator, 95)
			fixture.expectExecRejected(t, db, `UPDATE schema_migrations SET version = ?, dirty = ?`, 94, false)
			assertMigrationVersion(t, migrator, 95)
			if after := accountRecoveryMigrationSnapshot(t, db, `SELECT * FROM wrapped_domain_keys ORDER BY domain, version`); !reflect.DeepEqual(before, after) {
				t.Fatal("rejected down changed key rows")
			}
			fixture.mustExec(t, db, `UPDATE schema_migrations SET version = ?`, 96)
			fixture.mustExec(t, db, `UPDATE schema_migrations SET version = ?`, 95)
			assertMigrationVersion(t, migrator, 95)
		})
		t.Run(state+" independently blocks direct down", func(t *testing.T) {
			migrator, db := fixture.openAt(t, 95)
			insertRcloneIdentityMigrationKey(t, fixture, db, fmt.Sprintf("%032x", 13), "rclone_preflight_identity", state, 1)
			if fixture.engine == "postgres" {
				fixture.mustExec(t, db, `DROP TRIGGER trg_rclone_preflight_identity_downgrade_admission ON schema_migrations`)
			} else {
				fixture.mustExec(t, db, `DROP TRIGGER trg_rclone_preflight_identity_downgrade_admission`)
				fixture.mustExec(t, db, `DROP TRIGGER trg_rclone_preflight_identity_downgrade_update_admission`)
			}
			source := sqliteMigrationsFS
			if fixture.engine == "postgres" {
				source = postgresMigrationsFS
			}
			down, err := source.ReadFile("migrations/" + fixture.engine + "/000095_rclone_preflight_identity.down.sql")
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
				t.Fatal("direct down discarded identity key")
			}
			assertMigrationVersion(t, migrator, 95)
		})
	}
}

func insertRcloneIdentityMigrationKey(t *testing.T, fixture migrationFixture, db *sql.DB, id, domain, state string, version int) {
	t.Helper()
	now := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	fixture.mustExec(t, db, `INSERT INTO wrapped_domain_keys (id, domain, version, state, wrapped_key, wrap_algorithm, wrapping_key_fingerprint, activated_at, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, domain, version, state, "envelope", "algorithm", "fingerprint", now, now, now)
}
