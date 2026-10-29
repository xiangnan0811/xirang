package database

import (
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/model"
)

func TestAccountRecoveryMigration(t *testing.T) {
	testAccountRecoveryMigration(t, newSQLiteMigrationFixture(t))
}

func TestAccountRecoveryMigrationPostgres(t *testing.T) {
	testAccountRecoveryMigration(t, newRequiredPostgresMigrationFixture(t))
}

func testAccountRecoveryMigration(t *testing.T, fixture migrationFixture) {
	t.Helper()
	t.Run("additive migration preserves original identity and relationships", func(t *testing.T) {
		migrator, db := fixture.openAt(t, uint(accountRecoverySchemaVersion-1))
		now := time.Date(2026, 10, 16, 1, 2, 3, 0, time.UTC)
		fixture.mustExec(t, db, `INSERT INTO users
			(id, username, password_hash, role, totp_secret, totp_enabled, recovery_codes,
			token_version, totp_enrollment_id, totp_enrollment_expires_at, onboarded, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			41, "original-operator", "original-password-hash", "operator", "original-active-secret", true,
			"original-recovery-inventory", 7, "original-enrollment", now.Add(time.Minute), true, now, now)
		fixture.mustExec(t, db, `INSERT INTO nodes (id, name, host, username, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)`, 42, "original-node", "127.0.0.1", "fixture", now, now)
		fixture.mustExec(t, db, `INSERT INTO tasks
			(id, name, node_id, command, executor_type, status, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, 43, "original-task", 42, "true", "command", "pending", now, now)
		identityQuery := `SELECT id, username, password_hash, role, totp_secret, totp_enabled,
			recovery_codes, token_version, totp_enrollment_id, totp_enrollment_expires_at,
			onboarded, created_at, updated_at FROM users ORDER BY id`
		before := accountRecoveryMigrationSnapshot(t, db, identityQuery)
		business := accountRecoveryMigrationSnapshot(t, db, `SELECT * FROM tasks ORDER BY id`)
		if err := migrator.Steps(1); err != nil {
			t.Fatalf("apply account recovery migration on %s: %v", fixture.engine, err)
		}
		assertMigrationVersion(t, migrator, uint(accountRecoverySchemaVersion))
		if after := accountRecoveryMigrationSnapshot(t, db, identityQuery); !reflect.DeepEqual(before, after) {
			t.Fatal("migration changed pre-existing identity fields")
		}
		if after := accountRecoveryMigrationSnapshot(t, db, `SELECT * FROM tasks ORDER BY id`); !reflect.DeepEqual(business, after) {
			t.Fatal("migration changed pre-existing business relationships")
		}
		var required bool
		if err := db.QueryRow(`SELECT totp_reenrollment_required FROM users WHERE id = 41`).Scan(&required); err != nil || required {
			t.Fatalf("existing identity must default to unrestricted: required=%v error=%v", required, err)
		}
		if err := migrator.Up(); err != nil {
			t.Fatalf("upgrade preserved identity to current schema: %v", err)
		}
		if err := VerifyCurrentSchema(fixture.recoveryWorkerGorm(t, db), fixture.engine); err != nil {
			t.Fatalf("read-only current schema check on %s: %v", fixture.engine, err)
		}
		encoded, err := json.Marshal(model.User{TOTPReenrollmentRequired: true})
		if err != nil || strings.Contains(string(encoded), "reenrollment") {
			t.Fatal("internal required flag must not appear in user JSON")
		}
	})

	t.Run("unused down removes only its own schema and reapplies", func(t *testing.T) {
		migrator, db := fixture.openAt(t, uint(accountRecoverySchemaVersion))
		if err := migrator.Steps(-1); err != nil {
			t.Fatalf("unused recovery down on %s: %v", fixture.engine, err)
		}
		assertMigrationVersion(t, migrator, uint(accountRecoverySchemaVersion-1))
		if databaseTableExists(t, db, fixture.engine, "account_recovery_audits") {
			t.Fatal("unused down retained recovery audit table")
		}
		if exists, err := migrationColumnExists(db, fixture.engine, "users", "totp_reenrollment_required"); err != nil || exists {
			t.Fatalf("unused down retained required flag: exists=%v error=%v", exists, err)
		}
		for _, name := range []string{accountRecoveryAdmissionTrigger, accountRecoveryUpdateAdmissionTrigger} {
			if exists, err := migrationTriggerExists(db, fixture.engine, "schema_migrations", name); err != nil || exists {
				t.Fatalf("unused down retained guard: exists=%v error=%v", exists, err)
			}
		}
		if exists, err := migrationTriggerExists(db, fixture.engine, "schema_migrations", "trg_identity_security_downgrade_admission"); err != nil || !exists {
			t.Fatal("unused down removed the older independent identity guard")
		}
		if err := migrator.Steps(1); err != nil {
			t.Fatalf("reapply recovery migration: %v", err)
		}
		assertMigrationVersion(t, migrator, uint(accountRecoverySchemaVersion))
	})

	for _, used := range []string{"reset audit", "completed audit", "required flag"} {
		t.Run(used+" independently blocks real down and metadata lowering", func(t *testing.T) {
			migrator, db := fixture.openAt(t, uint(accountRecoverySchemaVersion))
			seedAccountRecoveryMigrationUsed(t, fixture, db, used)
			beforeUsers := accountRecoveryMigrationSnapshot(t, db, `SELECT * FROM users ORDER BY id`)
			beforeAudits := accountRecoveryMigrationSnapshot(t, db, `SELECT * FROM account_recovery_audits ORDER BY id`)
			beforeGuard, err := migrationTriggerDefinition(db, fixture.engine, "schema_migrations", accountRecoveryAdmissionTrigger)
			if err != nil {
				t.Fatal(err)
			}
			if err := migrator.Steps(-1); err == nil {
				t.Fatal("used recovery migration accepted destructive down")
			}
			assertMigrationVersion(t, migrator, uint(accountRecoverySchemaVersion))
			fixture.expectExecRejected(t, db, `UPDATE schema_migrations SET version = ?, dirty = ?`, accountRecoverySchemaVersion-1, false)
			assertMigrationVersion(t, migrator, uint(accountRecoverySchemaVersion))
			if !reflect.DeepEqual(beforeUsers, accountRecoveryMigrationSnapshot(t, db, `SELECT * FROM users ORDER BY id`)) ||
				!reflect.DeepEqual(beforeAudits, accountRecoveryMigrationSnapshot(t, db, `SELECT * FROM account_recovery_audits ORDER BY id`)) {
				t.Fatal("rejected downgrade changed identity or recovery evidence")
			}
			afterGuard, err := migrationTriggerDefinition(db, fixture.engine, "schema_migrations", accountRecoveryAdmissionTrigger)
			if err != nil || beforeGuard != afterGuard {
				t.Fatal("rejected downgrade changed guard definition")
			}
			if err := validateMinimumRecoverySchema(db, fixture.engine, accountRecoverySchemaVersion); err != nil {
				t.Fatalf("rejected downgrade damaged recovery schema: %v", err)
			}
			// Admission allows future versions and a return to the protected floor.
			fixture.mustExec(t, db, `UPDATE schema_migrations SET version = ?`, accountRecoverySchemaVersion+1)
			fixture.mustExec(t, db, `UPDATE schema_migrations SET version = ?`, accountRecoverySchemaVersion)
			assertMigrationVersion(t, migrator, uint(accountRecoverySchemaVersion))
		})
		t.Run(used+" independently blocks direct down without metadata guard", func(t *testing.T) {
			migrator, db := fixture.openAt(t, uint(accountRecoverySchemaVersion))
			seedAccountRecoveryMigrationUsed(t, fixture, db, used)
			dropAccountRecoveryMigrationGuards(t, fixture, db)
			source := sqliteMigrationsFS
			if fixture.engine == "postgres" {
				source = postgresMigrationsFS
			}
			down, err := source.ReadFile("migrations/" + fixture.engine + "/000094_account_recovery.down.sql")
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
				t.Fatal("direct down discarded used recovery state")
			}
			assertMigrationVersion(t, migrator, uint(accountRecoverySchemaVersion))
			if !databaseTableExists(t, db, fixture.engine, "account_recovery_audits") {
				t.Fatal("direct down removed recovery evidence table")
			}
		})
	}

	t.Run("audit constraints model UTC and history survive target deletion", func(t *testing.T) {
		_, db := fixture.openAt(t, uint(accountRecoverySchemaVersion))
		gormDB := fixture.recoveryWorkerGorm(t, db)
		now := time.Date(2026, 10, 16, 4, 5, 6, 0, time.UTC)
		row := model.AccountRecoveryAudit{
			OperationID: strings.Repeat("a", 32), TargetUserID: 51, TargetUsername: "deleted-account",
			Event: model.AccountRecoveryEventReset, ResetTOTP: true, Reason: "isolated fixture recovery",
			OldTokenVersion: 8, NewTokenVersion: 9, RevokedGrantCount: 2, CreatedAt: now,
		}
		fixture.mustExec(t, db, `INSERT INTO users (id, username, password_hash, role) VALUES (?, ?, ?, ?)`, 51, "deleted-account", "fixture-hash", "viewer")
		if err := gormDB.Create(&row).Error; err != nil {
			t.Fatalf("write recovery audit through model: %v", err)
		}
		var loaded model.AccountRecoveryAudit
		if err := gormDB.First(&loaded, row.ID).Error; err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(row, loaded) || !loaded.CreatedAt.Equal(now) || loaded.CreatedAt.Location() != time.UTC {
			t.Fatal("recovery audit model/UTC round-trip changed fields")
		}
		fixture.mustExec(t, db, `DELETE FROM users WHERE id = ?`, 51)
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM account_recovery_audits`).Scan(&count); err != nil || count != 1 {
			t.Fatal("target deletion removed recovery history")
		}
		insert := `INSERT INTO account_recovery_audits
			(operation_id, target_user_id, target_username, event, reset_password, reset_totp, reason,
			old_token_version, new_token_version, revoked_grant_count, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
		for _, invalid := range []struct {
			name     string
			op       string
			event    string
			password bool
			totp     bool
			reason   string
			old      int64
			new      int64
			grants   int64
		}{
			{"duplicate operation", strings.Repeat("a", 32), "reset", true, false, "valid reason", 0, 1, 0},
			{"uppercase operation", strings.Repeat("A", 32), "reset", true, false, "valid reason", 0, 1, 0},
			{"short operation", "abc", "reset", true, false, "valid reason", 0, 1, 0},
			{"invalid event", strings.Repeat("b", 32), "other", true, false, "valid reason", 0, 1, 0},
			{"empty mode", strings.Repeat("b", 32), "reset", false, false, "valid reason", 0, 1, 0},
			{"short reason", strings.Repeat("b", 32), "reset", true, false, "short", 0, 1, 0},
			{"reason byte limit", strings.Repeat("b", 32), "reset", true, false, strings.Repeat("界", 171), 0, 1, 0},
			{"completed reason", strings.Repeat("b", 32), "reenrollment_completed", false, false, "not empty", 0, 1, 0},
			{"negative old version", strings.Repeat("b", 32), "reset", true, false, "valid reason", -1, 0, 0},
			{"skipped version", strings.Repeat("b", 32), "reset", true, false, "valid reason", 0, 2, 0},
			{"negative grant count", strings.Repeat("b", 32), "reset", true, false, "valid reason", 0, 1, -1},
		} {
			t.Run(invalid.name, func(t *testing.T) {
				fixture.expectExecRejected(t, db, insert, invalid.op, 51, "deleted-account", invalid.event,
					invalid.password, invalid.totp, invalid.reason, invalid.old, invalid.new, invalid.grants, now)
			})
		}
		fixture.mustExec(t, db, insert, strings.Repeat("c", 32), 51, "deleted-account", "reenrollment_completed", false, false, "", 9, 10, 0, now)
	})

	t.Run("verifier rejects old dirty future and empty metadata without repair", func(t *testing.T) {
		migrator, db := fixture.openAt(t, uint(accountRecoverySchemaVersion-1))
		gormDB := fixture.recoveryWorkerGorm(t, db)
		if err := VerifyCurrentSchema(gormDB, fixture.engine); !errors.Is(err, ErrMigrationSchemaDrift) {
			t.Fatalf("old schema accepted: %v", err)
		}
		assertMigrationVersion(t, migrator, uint(accountRecoverySchemaVersion-1))
		if err := migrator.Steps(1); err != nil {
			t.Fatal(err)
		}
		fixture.mustExec(t, db, `UPDATE schema_migrations SET dirty = ?`, true)
		if err := VerifyCurrentSchema(gormDB, fixture.engine); !errors.Is(err, ErrMigrationDirty) {
			t.Fatalf("dirty schema accepted: %v", err)
		}
		if err := RunMigrations(gormDB, fixture.engine); !errors.Is(err, ErrMigrationDirty) {
			t.Fatalf("dirty startup accepted: %v", err)
		}
		dirty, version, err := checkMigrationDirty(db, fixture.engine)
		if err != nil || !dirty || version != accountRecoverySchemaVersion {
			t.Fatal("verifier or startup repaired dirty metadata")
		}
		fixture.mustExec(t, db, `UPDATE schema_migrations SET dirty = ?, version = ?`, false, latestMigrationVersion+1)
		if err := VerifyCurrentSchema(gormDB, fixture.engine); !errors.Is(err, ErrMigrationSchemaDrift) {
			t.Fatalf("future schema accepted: %v", err)
		}
		fixture.mustExec(t, db, `DELETE FROM schema_migrations`)
		if err := VerifyCurrentSchema(gormDB, fixture.engine); !errors.Is(err, ErrMigrationSchemaDrift) {
			t.Fatalf("empty metadata accepted: %v", err)
		}
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil || count != 0 {
			t.Fatal("verifier initialized empty migration metadata")
		}
	})
	t.Run("verifier never creates missing migration metadata", func(t *testing.T) {
		_, db := fixture.openAt(t, uint(accountRecoverySchemaVersion))
		fixture.mustExec(t, db, `DROP TABLE schema_migrations`)
		if err := VerifyCurrentSchema(fixture.recoveryWorkerGorm(t, db), fixture.engine); !errors.Is(err, ErrMigrationSchemaDrift) {
			t.Fatalf("missing metadata accepted: %v", err)
		}
		if databaseTableExists(t, db, fixture.engine, "schema_migrations") {
			t.Fatal("read-only verifier created migration metadata")
		}
	})
}

func TestAccountRecoveryMigrationSchemaDrift(t *testing.T) {
	testAccountRecoveryMigrationSchemaDrift(t, newSQLiteMigrationFixture(t))
}

func TestAccountRecoveryMigrationSchemaDriftPostgres(t *testing.T) {
	testAccountRecoveryMigrationSchemaDrift(t, newRequiredPostgresMigrationFixture(t))
}

func testAccountRecoveryMigrationSchemaDrift(t *testing.T, fixture migrationFixture) {
	t.Helper()
	cases := []struct {
		name   string
		mutate func(*testing.T, *sql.DB)
	}{
		{"missing flag", func(t *testing.T, db *sql.DB) {
			dropAccountRecoveryMigrationGuards(t, fixture, db)
			fixture.mustExec(t, db, `ALTER TABLE users DROP COLUMN totp_reenrollment_required`)
		}},
		{"missing audit table", func(t *testing.T, db *sql.DB) {
			dropAccountRecoveryMigrationGuards(t, fixture, db)
			fixture.mustExec(t, db, `DROP TABLE account_recovery_audits`)
		}},
		{"nonunique operation index", func(t *testing.T, db *sql.DB) {
			fixture.mustExec(t, db, `DROP INDEX idx_account_recovery_audits_operation_id`)
			fixture.mustExec(t, db, `CREATE INDEX idx_account_recovery_audits_operation_id ON account_recovery_audits(operation_id)`)
		}},
		{"wrong created time type", func(t *testing.T, db *sql.DB) {
			fixture.mustExec(t, db, `DROP INDEX idx_account_recovery_audits_created_at`)
			fixture.mustExec(t, db, `ALTER TABLE account_recovery_audits DROP COLUMN created_at`)
			fixture.mustExec(t, db, `ALTER TABLE account_recovery_audits ADD COLUMN created_at TEXT NOT NULL DEFAULT ''`)
		}},
		{"same name no-op guard", func(t *testing.T, db *sql.DB) {
			if fixture.engine == "postgres" {
				fixture.mustExec(t, db, `CREATE OR REPLACE FUNCTION account_recovery_downgrade_admission()
					RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END; $$`)
			} else {
				fixture.mustExec(t, db, `DROP TRIGGER trg_account_recovery_downgrade_admission`)
				fixture.mustExec(t, db, `CREATE TRIGGER trg_account_recovery_downgrade_admission
					BEFORE INSERT ON schema_migrations BEGIN SELECT 1; END`)
			}
		}},
		{"fragment preserving bypass guard", func(t *testing.T, db *sql.DB) {
			if fixture.engine == "postgres" {
				body := strings.Replace(accountRecoveryPostgresBody, "BEGIN", "BEGIN RETURN NEW;", 1)
				fixture.mustExec(t, db, `CREATE OR REPLACE FUNCTION account_recovery_downgrade_admission()
					RETURNS trigger LANGUAGE plpgsql AS $$ `+body+` $$`)
			} else {
				fixture.mustExec(t, db, `DROP TRIGGER trg_account_recovery_downgrade_admission`)
				fixture.mustExec(t, db, `CREATE TRIGGER trg_account_recovery_downgrade_admission
					BEFORE INSERT ON schema_migrations WHEN (`+accountRecoverySQLiteWhen+`) AND 0
					BEGIN `+accountRecoverySQLiteBody+` END`)
			}
		}},
	}
	if fixture.engine == "sqlite" {
		cases = append(cases, struct {
			name   string
			mutate func(*testing.T, *sql.DB)
		}{
			"missing update guard", func(t *testing.T, db *sql.DB) {
				fixture.mustExec(t, db, `DROP TRIGGER trg_account_recovery_downgrade_update_admission`)
			},
		})
	} else {
		cases = append(cases, struct {
			name   string
			mutate func(*testing.T, *sql.DB)
		}{
			"disabled guard", func(t *testing.T, db *sql.DB) {
				fixture.mustExec(t, db, `ALTER TABLE schema_migrations DISABLE TRIGGER trg_account_recovery_downgrade_admission`)
			},
		})
		cases = append(cases, struct {
			name   string
			mutate func(*testing.T, *sql.DB)
		}{
			"wrong flag default", func(t *testing.T, db *sql.DB) {
				fixture.mustExec(t, db, `ALTER TABLE users ALTER COLUMN totp_reenrollment_required SET DEFAULT TRUE`)
			},
		})
		cases = append(cases, struct {
			name   string
			mutate func(*testing.T, *sql.DB)
		}{
			"nullable required flag", func(t *testing.T, db *sql.DB) {
				fixture.mustExec(t, db, `ALTER TABLE users ALTER COLUMN totp_reenrollment_required DROP NOT NULL`)
			},
		})
		cases = append(cases, struct {
			name   string
			mutate func(*testing.T, *sql.DB)
		}{
			"missing canonical operation constraint", func(t *testing.T, db *sql.DB) {
				fixture.mustExec(t, db, `ALTER TABLE account_recovery_audits DROP CONSTRAINT account_recovery_audits_operation_id_check`)
			},
		})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			migrator, db := fixture.openAt(t, uint(latestMigrationVersion))
			tc.mutate(t, db)
			gormDB := fixture.recoveryWorkerGorm(t, db)
			if err := VerifyCurrentSchema(gormDB, fixture.engine); !errors.Is(err, ErrMigrationSchemaDrift) {
				t.Fatalf("read-only verifier accepted schema drift: %v", err)
			}
			if err := RunMigrations(gormDB, fixture.engine); !errors.Is(err, ErrMigrationSchemaDrift) {
				t.Fatalf("startup accepted schema drift: %v", err)
			}
			assertMigrationVersion(t, migrator, uint(latestMigrationVersion))
		})
	}
}

func seedAccountRecoveryMigrationUsed(t *testing.T, fixture migrationFixture, db *sql.DB, used string) {
	t.Helper()
	if used == "required flag" {
		fixture.mustExec(t, db, `INSERT INTO users (id, username, password_hash, role, totp_reenrollment_required)
			VALUES (?, ?, ?, ?, ?)`, 61, "required-account", "fixture-hash", "admin", true)
		return
	}
	event, reason, reset := "reset", "isolated fixture recovery", true
	if used == "completed audit" {
		event, reason, reset = "reenrollment_completed", "", false
	}
	fixture.mustExec(t, db, `INSERT INTO account_recovery_audits
		(operation_id, target_user_id, target_username, event, reset_password, reset_totp, reason,
		old_token_version, new_token_version, revoked_grant_count, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		strings.Repeat("d", 32), 62, "historical-account", event, false, reset, reason, 1, 2, 0,
		time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC))
}

func dropAccountRecoveryMigrationGuards(t *testing.T, fixture migrationFixture, db *sql.DB) {
	t.Helper()
	if fixture.engine == "postgres" {
		fixture.mustExec(t, db, `DROP TRIGGER trg_account_recovery_downgrade_admission ON schema_migrations`)
	} else {
		fixture.mustExec(t, db, `DROP TRIGGER trg_account_recovery_downgrade_admission`)
		fixture.mustExec(t, db, `DROP TRIGGER trg_account_recovery_downgrade_update_admission`)
	}
}

func accountRecoveryMigrationSnapshot(t *testing.T, db *sql.DB, query string) [][]any {
	t.Helper()
	rows, err := db.Query(query)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var result [][]any
	for rows.Next() {
		values, pointers := make([]any, len(columns)), make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			t.Fatal(err)
		}
		for i, value := range values {
			if bytes, ok := value.([]byte); ok {
				values[i] = string(bytes)
			}
		}
		result = append(result, values)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}
