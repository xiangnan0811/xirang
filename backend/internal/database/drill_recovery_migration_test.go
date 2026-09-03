package database

import (
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const activeDrillMigrationIndex = "idx_task_runs_active_drill"

func TestDrillRecoveryMigrationPair(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		path  string
		fs    interface{ ReadFile(string) ([]byte, error) }
		wants []string
	}{
		{name: "sqlite up", path: "migrations/sqlite/000074_drill_durable_recovery.up.sql", fs: sqliteMigrationsFS, wants: []string{"CREATE UNIQUE INDEX idx_task_runs_active_drill", "'retrying'", "recovery_owner_id", "trg_drill_durable_recovery_downgrade_admission"}},
		{name: "sqlite down", path: "migrations/sqlite/000074_drill_durable_recovery.down.sql", fs: sqliteMigrationsFS, wants: []string{"DROP INDEX", "'retrying'", "recovery_owner_id", "DROP TRIGGER IF EXISTS trg_drill_durable_recovery_downgrade_admission"}},
		{name: "postgres up", path: "migrations/postgres/000074_drill_durable_recovery.up.sql", fs: postgresMigrationsFS, wants: []string{"BEGIN;", "COMMIT;", "CREATE UNIQUE INDEX idx_task_runs_active_drill", "'retrying'", "recovery_owner_id", "drill_durable_recovery_downgrade_admission"}},
		{name: "postgres down", path: "migrations/postgres/000074_drill_durable_recovery.down.sql", fs: postgresMigrationsFS, wants: []string{"BEGIN;", "COMMIT;", "DROP INDEX", "'retrying'", "recovery_owner_id", "DROP FUNCTION IF EXISTS drill_durable_recovery_downgrade_admission"}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			body, err := testCase.fs.ReadFile(testCase.path)
			if err != nil {
				t.Fatalf("read paired drill recovery migration: %v", err)
			}
			for _, want := range testCase.wants {
				if !strings.Contains(string(body), want) {
					t.Fatalf("%s missing %q", testCase.path, want)
				}
			}
		})
	}
}

func TestSQLiteDrillRecoveryMigrationActiveRunInvariant(t *testing.T) {
	up, err := sqliteMigrationsFS.ReadFile("migrations/sqlite/000074_drill_durable_recovery.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := sqliteMigrationsFS.ReadFile("migrations/sqlite/000074_drill_durable_recovery.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	openFixture := func(t *testing.T) *gorm.DB {
		t.Helper()
		db, err := gorm.Open(sqlite.Open(t.TempDir()+"/drill-migration.db"), &gorm.Config{})
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Exec(`CREATE TABLE task_runs (
				id INTEGER PRIMARY KEY,
				task_id INTEGER NOT NULL,
				trigger_type TEXT NOT NULL,
				status TEXT NOT NULL,
				started_at DATETIME,
				finished_at DATETIME,
				duration_ms INTEGER NOT NULL DEFAULT 0
			)`).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Exec(`CREATE TABLE restore_drill_evidences (
				id INTEGER PRIMARY KEY,
				task_run_id INTEGER NOT NULL,
				task_id INTEGER NOT NULL,
				status TEXT NOT NULL,
				started_at DATETIME,
				finished_at DATETIME,
				duration_ms INTEGER NOT NULL DEFAULT 0
			)`).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Exec(`CREATE TABLE schema_migrations (
				version INTEGER NOT NULL PRIMARY KEY,
				dirty BOOLEAN NOT NULL
			)`).Error; err != nil {
			t.Fatal(err)
		}
		return db
	}

	t.Run("single existing orphan upgrades and blocks duplicate", func(t *testing.T) {
		db := openFixture(t)
		if err := db.Exec("INSERT INTO task_runs(id, task_id, trigger_type, status) VALUES (1, 9, 'drill', 'running')").Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Exec(string(up)).Error; err != nil {
			t.Fatalf("upgrade single active orphan: %v", err)
		}
		if err := db.Exec("INSERT INTO task_runs(id, task_id, trigger_type, status) VALUES (2, 9, 'drill', 'retrying')").Error; err == nil {
			t.Fatal("000074 database invariant accepted a second active drill")
		}
		if err := db.Exec(string(down)).Error; err == nil {
			t.Fatal("000074 down silently removed recovery authority while a drill was active")
		}
		if err := db.Exec("UPDATE task_runs SET status = 'canceled' WHERE id = 1").Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Exec(string(down)).Error; err != nil {
			t.Fatalf("drop active drill invariant: %v", err)
		}
	})

	t.Run("duplicate existing active drills fail closed", func(t *testing.T) {
		db := openFixture(t)
		if err := db.Exec(`INSERT INTO task_runs(id, task_id, trigger_type, status, started_at) VALUES
				(1, 11, 'drill', 'running', '2026-08-30 00:00:00'),
				(2, 11, 'drill', 'retrying', '2026-08-30 00:00:01')`).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Exec(`INSERT INTO restore_drill_evidences(id, task_run_id, task_id, status, started_at) VALUES
				(1, 1, 11, 'running', '2026-08-30 00:00:00'),
				(2, 2, 11, 'retrying', '2026-08-30 00:00:01')`).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Exec(string(up)).Error; err == nil {
			t.Fatal("000074 silently accepted ambiguous duplicate active drills")
		}
		var count int64
		if err := db.Raw("SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?", activeDrillMigrationIndex).Scan(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatal("failed-closed duplicate migration left a partial index behind")
		}
		if err := db.Transaction(func(tx *gorm.DB) error {
			const pairedTerminal = "status = 'canceled', finished_at = '2026-08-30 00:01:01', duration_ms = 60000"
			if err := tx.Exec("UPDATE task_runs SET " + pairedTerminal + " WHERE id = 2").Error; err != nil {
				return err
			}
			return tx.Exec("UPDATE restore_drill_evidences SET " + pairedTerminal + " WHERE task_run_id = 2").Error
		}); err != nil {
			t.Fatalf("paired operator reconciliation: %v", err)
		}
		var pairedStatuses []string
		if err := db.Raw(`SELECT status FROM task_runs WHERE id = 2
				UNION ALL SELECT status FROM restore_drill_evidences WHERE task_run_id = 2`).Scan(&pairedStatuses).Error; err != nil {
			t.Fatal(err)
		}
		if len(pairedStatuses) != 2 || pairedStatuses[0] != "canceled" || pairedStatuses[1] != "canceled" {
			t.Fatalf("operator reconciliation did not terminalize the pair: %v", pairedStatuses)
		}
		if err := db.Exec(string(up)).Error; err != nil {
			t.Fatalf("000074 could not be retried after paired operator reconciliation: %v", err)
		}
		if err := db.Raw("SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?", activeDrillMigrationIndex).Scan(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatal("reconciled 000074 retry did not install the active drill index")
		}
	})
}

func TestRunMigrations074DuplicateActiveDrillFailsCleanAndRetriesSQLite(t *testing.T) {
	testRunMigrations074DuplicateActiveDrillFailsCleanAndRetries(t, newSQLiteMigrationFixture(t))
}

func TestRunMigrations074DuplicateActiveDrillFailsCleanAndRetriesPostgres(t *testing.T) {
	testRunMigrations074DuplicateActiveDrillFailsCleanAndRetries(t, newRequiredPostgresMigrationFixture(t))
}

func testRunMigrations074DuplicateActiveDrillFailsCleanAndRetries(t *testing.T, fixture migrationFixture) {
	t.Helper()
	migrator, db := fixture.openAt(t, backupAssetPlainTextContentVersion)
	now := time.Date(2026, 8, 30, 1, 2, 3, 0, time.UTC)
	insertDrillMigrationTaskAndRun(t, fixture, db, 74001, 74002, "running", now)
	fixture.mustExec(t, db, `INSERT INTO task_runs
		(id, task_id, node_id_snapshot, trigger_type, status, created_at, updated_at)
		VALUES (?, ?, 1, 'drill', 'retrying', ?, ?)`, int64(74003), int64(74001), now, now)
	insertDrillMigrationEvidence(t, fixture, db, 74004, 74001, 74002, "running", now)
	insertDrillMigrationEvidence(t, fixture, db, 74005, 74001, 74003, "retrying", now)

	err := RunMigrations(fixture.recoveryWorkerGorm(t, db), fixture.engine)
	if err == nil || !strings.Contains(err.Error(), "duplicate_active_drill") {
		t.Fatalf("000074 duplicate-active preflight returned %v, want sanitized preflight rejection", err)
	}
	dirty, version, checkErr := checkMigrationDirty(db, fixture.engine)
	if checkErr != nil {
		t.Fatalf("check metadata after 000074 preflight rejection: %v", checkErr)
	}
	if dirty || version != backupAssetPlainTextContentVersion {
		t.Fatalf("000074 preflight rejection mutated metadata: version=%d dirty=%v", version, dirty)
	}
	assertMigrationVersion(t, migrator, backupAssetPlainTextContentVersion)

	tx, txErr := db.Begin()
	if txErr != nil {
		t.Fatalf("begin paired operator reconciliation: %v", txErr)
	}
	if _, txErr = tx.Exec(
		fixture.bind(`UPDATE task_runs SET status = 'canceled', finished_at = ?, duration_ms = ? WHERE id = ?`),
		now.Add(time.Minute),
		int64(time.Minute/time.Millisecond),
		int64(74003),
	); txErr == nil {
		_, txErr = tx.Exec(
			fixture.bind(`UPDATE restore_drill_evidences SET status = 'canceled', finished_at = ?, duration_ms = ? WHERE task_run_id = ?`),
			now.Add(time.Minute),
			int64(time.Minute/time.Millisecond),
			int64(74003),
		)
	}
	if txErr != nil {
		_ = tx.Rollback()
		t.Fatalf("paired operator reconciliation: %v", txErr)
	}
	if txErr = tx.Commit(); txErr != nil {
		t.Fatalf("commit paired operator reconciliation: %v", txErr)
	}
	var terminalPairRows int
	if err := db.QueryRow(fixture.bind(`SELECT COUNT(*) FROM (
		SELECT status, duration_ms FROM task_runs WHERE id = ?
		UNION ALL
		SELECT status, duration_ms FROM restore_drill_evidences WHERE task_run_id = ?
	) AS terminal_pair WHERE status = 'canceled' AND duration_ms = ?`),
		int64(74003),
		int64(74003),
		int64(time.Minute/time.Millisecond),
	).Scan(&terminalPairRows); err != nil {
		t.Fatalf("verify paired operator reconciliation: %v", err)
	}
	if terminalPairRows != 2 {
		t.Fatalf("paired operator reconciliation updated %d of 2 rows", terminalPairRows)
	}
	if err := RunMigrations(fixture.recoveryWorkerGorm(t, db), fixture.engine); err != nil {
		t.Fatalf("retry 000074 after paired operator reconciliation: %v", err)
	}
	dirty, version, checkErr = checkMigrationDirty(db, fixture.engine)
	if checkErr != nil {
		t.Fatalf("check metadata after 000074 retry: %v", checkErr)
	}
	if dirty || version != latestMigrationVersion {
		t.Fatalf("migration retry metadata mismatch: version=%d dirty=%v", version, dirty)
	}
}

func TestMigration074StepsDownAdmissionPreservesCleanVersionSQLite(t *testing.T) {
	testMigration074StepsDownAdmissionPreservesCleanVersion(t, newSQLiteMigrationFixture(t))
}

func TestMigration074StepsDownAdmissionPreservesCleanVersionPostgres(t *testing.T) {
	testMigration074StepsDownAdmissionPreservesCleanVersion(t, newRequiredPostgresMigrationFixture(t))
}

func testMigration074StepsDownAdmissionPreservesCleanVersion(t *testing.T, fixture migrationFixture) {
	t.Helper()
	migrator, db := fixture.openAt(t, drillDurableRecoveryVersion)
	now := time.Date(2026, 8, 30, 2, 3, 4, 0, time.UTC)
	insertDrillMigrationTaskAndRun(t, fixture, db, 74101, 74102, "running", now)

	err := migrator.Steps(-1)
	if err == nil {
		t.Fatal("000074 downgrade admission accepted an active restore drill")
	}
	assertMigrationVersion(t, migrator, drillDurableRecoveryVersion)
	if err := validateMinimumRecoverySchema(db, fixture.engine, drillDurableRecoveryVersion); err != nil {
		t.Fatalf("failed downgrade changed clean 000074 schema: %v", err)
	}

	fixture.mustExec(t, db, `UPDATE task_runs SET status = 'canceled' WHERE id = ?`, int64(74102))
	if err := migrator.Steps(-1); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		t.Fatalf("downgrade 000074 after terminalizing active drill: %v", err)
	}
	assertMigrationVersion(t, migrator, backupAssetPlainTextContentVersion)
	for _, column := range []string{"recovery_owner_id", "recovery_lease_until"} {
		exists, existsErr := migrationColumnExists(db, fixture.engine, "restore_drill_evidences", column)
		if existsErr != nil {
			t.Fatalf("check downgraded column %s: %v", column, existsErr)
		}
		if exists {
			t.Fatalf("000074 downgrade left %s behind", column)
		}
	}
}

func TestRunMigrations074SchemaContractSQLite(t *testing.T) {
	testRunMigrations074SchemaContract(t, newSQLiteMigrationFixture(t))
}

func TestRunMigrations074SchemaContractPostgres(t *testing.T) {
	testRunMigrations074SchemaContract(t, newRequiredPostgresMigrationFixture(t))
}

func TestPostgresMigrationCISelectorIncludesDrillRecovery074(t *testing.T) {
	workflowPath := filepath.Join("..", "..", "..", ".github", "workflows", "ci.yml")
	workflow, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("read CI workflow: %v", err)
	}
	selector, ok := activeRequiredPostgresSelector(string(workflow), "./internal/database")
	if !ok {
		t.Fatal("CI workflow has no active reusable required-PostgreSQL runner for ./internal/database")
	}
	compiled, err := regexp.Compile(selector)
	if err != nil {
		t.Fatalf("compile PostgreSQL migration CI selector %q: %v", selector, err)
	}
	requiredTests := []string{
		"TestRunMigrations074DuplicateActiveDrillFailsCleanAndRetriesPostgres",
		"TestMigration074StepsDownAdmissionPreservesCleanVersionPostgres",
		"TestRunMigrations074SchemaContractPostgres",
	}
	for _, testName := range requiredTests {
		if !compiled.MatchString(testName) {
			t.Errorf("PostgreSQL migration CI selector %q omits %s", selector, testName)
		}
	}

	// Use Go's own listing boundary so this freshness check proves that the
	// checked-in workflow selector resolves to real tests, not only strings that
	// happen to satisfy a separately compiled regexp.
	cmd := exec.Command("go", "test", "./internal/database", "-list", selector, "-count=1") //nolint:gosec // fixed tool/package; selector is a checked-in workflow argument, never shell-expanded
	cmd.Dir = filepath.Join("..", "..")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("list required PostgreSQL migration tests with selector %q: %v\n%s", selector, err, output)
	}
	listedTests := make(map[string]struct{})
	for _, line := range strings.Split(string(output), "\n") {
		name := strings.TrimSpace(line)
		if strings.HasPrefix(name, "Test") {
			listedTests[name] = struct{}{}
		}
	}
	for _, testName := range requiredTests {
		if _, ok := listedTests[testName]; !ok {
			t.Errorf("go test -list with PostgreSQL migration CI selector %q omitted %s", selector, testName)
		}
	}
}

func activeRequiredPostgresSelector(workflow, packagePath string) (string, bool) {
	commandPattern := regexp.MustCompile(
		`^bash \.\./scripts/run-required-postgres-tests\.sh '[^']+' ` +
			regexp.QuoteMeta(packagePath) + ` '([^']+)'$`,
	)
	for _, line := range strings.Split(workflow, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "run:") {
			continue
		}
		matches := commandPattern.FindStringSubmatch(strings.TrimSpace(strings.TrimPrefix(trimmed, "run:")))
		if len(matches) == 2 {
			return matches[1], true
		}
	}
	return "", false
}

func TestNormalizeMigrationPredicatePostgresActiveDrill(t *testing.T) {
	definition := `(((trigger_type)::text = 'drill'::text) AND ((status)::text = ANY ((ARRAY[
		'pending'::character varying,
		'running'::character varying,
		'retrying'::character varying
	])::text[])))`
	want := "trigger_type='drill'andstatus=anyarray['pending','running','retrying']"
	if got := normalizeMigrationPredicate(definition); got != want {
		t.Fatalf("normalize PostgreSQL active-drill predicate got %q, want %q", got, want)
	}
}

func testRunMigrations074SchemaContract(t *testing.T, fixture migrationFixture) {
	t.Helper()
	t.Run("valid", func(t *testing.T) {
		_, db := fixture.openAt(t, drillDurableRecoveryVersion)
		if err := validateMinimumRecoverySchema(db, fixture.engine, drillDurableRecoverySchemaVersion); err != nil {
			t.Fatalf("valid %s 000074 schema rejected: %v", fixture.engine, err)
		}
	})

	t.Run("exact active index predicate", func(t *testing.T) {
		migrator, db := fixture.openAt(t, drillDurableRecoveryVersion)
		fixture.mustExec(t, db, `DROP INDEX idx_task_runs_active_drill`)
		fixture.mustExec(t, db, `CREATE UNIQUE INDEX idx_task_runs_active_drill
			ON task_runs(task_id)
			WHERE trigger_type = 'drill'
			  AND status IN ('pending', 'running', 'retrying')
			  AND task_id > 0`)
		assertRunMigrations074SchemaDrift(t, fixture, migrator, db, "invalid_active_drill_index")
	})

	t.Run("lease index columns", func(t *testing.T) {
		migrator, db := fixture.openAt(t, drillDurableRecoveryVersion)
		fixture.mustExec(t, db, `DROP INDEX idx_restore_drill_recovery_lease`)
		fixture.mustExec(t, db, `CREATE INDEX idx_restore_drill_recovery_lease
			ON restore_drill_evidences(task_run_id)`)
		assertRunMigrations074SchemaDrift(t, fixture, migrator, db, "invalid_drill_recovery_lease_index")
	})

	t.Run("same-name no-op admission", func(t *testing.T) {
		migrator, db := fixture.openAt(t, drillDurableRecoveryVersion)
		if fixture.engine == "postgres" {
			fixture.mustExec(t, db, `CREATE OR REPLACE FUNCTION drill_durable_recovery_downgrade_admission()
				RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END; $$`)
		} else {
			fixture.mustExec(t, db, `DROP TRIGGER trg_drill_durable_recovery_downgrade_admission`)
			fixture.mustExec(t, db, `CREATE TRIGGER trg_drill_durable_recovery_downgrade_admission
				BEFORE INSERT ON schema_migrations BEGIN SELECT 1; END`)
		}
		assertRunMigrations074SchemaDrift(t, fixture, migrator, db, "invalid_drill_recovery_admission_trigger")
	})

	t.Run("same-name incompatible columns", func(t *testing.T) {
		migrator, db := fixture.openAt(t, backupAssetPlainTextContentVersion)
		if err := migrator.Force(drillDurableRecoveryVersion); err != nil {
			t.Fatalf("force incomplete %s fixture to clean 000074: %v", fixture.engine, err)
		}
		fixture.mustExec(t, db, `ALTER TABLE restore_drill_evidences
			ADD COLUMN recovery_owner_id INTEGER NOT NULL DEFAULT 0`)
		fixture.mustExec(t, db, `ALTER TABLE restore_drill_evidences
			ADD COLUMN recovery_lease_until TEXT`)
		assertRunMigrations074SchemaDrift(t, fixture, migrator, db, "invalid_drill_recovery_owner_column")
	})
}

func assertRunMigrations074SchemaDrift(
	t *testing.T,
	fixture migrationFixture,
	migrator *migrate.Migrate,
	db *sql.DB,
	reason string,
) {
	t.Helper()
	err := RunMigrations(fixture.recoveryWorkerGorm(t, db), fixture.engine)
	if !errors.Is(err, ErrMigrationSchemaDrift) || !strings.Contains(err.Error(), reason) {
		t.Fatalf("clean %s 000074 drift returned %v, want %s", fixture.engine, err, reason)
	}
	assertMigrationVersion(t, migrator, drillDurableRecoveryVersion)
}

func insertDrillMigrationTaskAndRun(
	t *testing.T,
	fixture migrationFixture,
	db *sql.DB,
	taskID, runID int64,
	status string,
	now time.Time,
) {
	t.Helper()
	fixture.mustExec(t, db, `INSERT INTO tasks
		(id, name, node_id, executor_type, status, created_at, updated_at)
		VALUES (?, ?, 1, 'local', 'idle', ?, ?)`, taskID, "drill-migration-task", now, now)
	fixture.mustExec(t, db, `INSERT INTO task_runs
		(id, task_id, node_id_snapshot, trigger_type, status, created_at, updated_at)
		VALUES (?, ?, 1, 'drill', ?, ?, ?)`, runID, taskID, status, now, now)
}

func insertDrillMigrationEvidence(
	t *testing.T,
	fixture migrationFixture,
	db *sql.DB,
	evidenceID, taskID, runID int64,
	status string,
	now time.Time,
) {
	t.Helper()
	fixture.mustExec(t, db, `INSERT INTO restore_drill_evidences
		(id, policy_id, task_id, task_run_id, sandbox_node_id, sandbox_path, status, started_at, created_at, updated_at)
		VALUES (?, 1, ?, ?, 1, '/tmp/drill-migration', ?, ?, ?, ?)`,
		evidenceID, taskID, runID, status, now, now, now)
}
