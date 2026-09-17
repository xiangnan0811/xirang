package database

import (
	"errors"
	"testing"
	"time"
)

func TestTaskRunEffectsMigrationDuplicateHistorySQLite(t *testing.T) {
	testTaskRunEffectsMigrationDuplicateHistory(t, newSQLiteMigrationFixture(t))
}

func TestTaskRunEffectsMigrationDuplicateHistoryPostgres(t *testing.T) {
	testTaskRunEffectsMigrationDuplicateHistory(t, newRequiredPostgresMigrationFixture(t))
}

func testTaskRunEffectsMigrationDuplicateHistory(t *testing.T, fixture migrationFixture) {
	t.Helper()
	_, db := fixture.openAt(t, 78)
	now := time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)
	insertDrillMigrationTaskAndRun(t, fixture, db, 79001, 79002, "success", now)
	insertDrillMigrationTaskAndRun(t, fixture, db, 79005, 79006, "success", now)
	for _, runID := range []int64{79003, 79004} {
		fixture.mustExec(t, db, `INSERT INTO task_runs
			(id, task_id, node_id_snapshot, trigger_type, status, upstream_task_run_id, started_at, finished_at, created_at, updated_at)
			VALUES (?, 79005, 1, 'chain', 'success', 79002, ?, ?, ?, ?)`, runID, now, now, now, now)
	}

	err := RunMigrations(fixture.recoveryWorkerGorm(t, db), fixture.engine)
	if !errors.Is(err, ErrMigrationPrecondition) {
		t.Fatalf("ambiguous historical executions must reject before migration: %v", err)
	}
	dirty, version, err := checkMigrationDirty(db, fixture.engine)
	if err != nil || dirty || version != 78 {
		t.Fatalf("preflight rejection changed clean metadata: version=%d dirty=%v err=%v", version, dirty, err)
	}
	var retained int
	if err := db.QueryRow(`SELECT COUNT(*) FROM task_runs WHERE task_id = 79005 AND upstream_task_run_id = 79002`).Scan(&retained); err != nil {
		t.Fatal(err)
	}
	if retained != 2 {
		t.Fatalf("migration must not guess away execution evidence: retained=%d", retained)
	}
}
