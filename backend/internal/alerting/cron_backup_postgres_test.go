package alerting

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"xirang/backend/internal/cronbackup"
)

// TestCronBackupWorkerPostgres exercises the same versioned v92 fixture and
// cursor transaction on PostgreSQL. The migration fixture isolates the test in
// a temporary schema; no production tables are AutoMigrated.
func TestCronBackupWorkerPostgres(t *testing.T) {
	requireCronBackupPostgres(t)
	fixture, cfg, anchor := newCronBackupWorkerFixture(t, "postgres")
	worker := NewCronBackupWorker(fixture.db, nil, cfg)
	worker.tickAt(context.Background(), anchor.Add(cfg.MaxAge+time.Nanosecond))
	cursor := cronBackupCursor(t, fixture.db, worker.sourceKey)
	if !cursor.FaultActive || cursor.AlertID == nil {
		t.Fatalf("postgres stale cursor=%+v, want one active alert", cursor)
	}
	if got := cronBackupAlertCount(t, fixture.db, worker.sourceKey); got != 1 {
		t.Fatalf("postgres alert count=%d, want 1", got)
	}
}
func requireCronBackupPostgres(t *testing.T) {
	t.Helper()
	if os.Getenv("TEST_POSTGRES_DSN") == "" {
		if os.Getenv("REQUIRE_POSTGRES_MIGRATION_TEST") == "1" {
			t.Fatal("TEST_POSTGRES_DSN is required for cron backup PostgreSQL verification")
		}
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
}

func cronBackupPostgresSuccessScript(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "backup-postgres.sh")
	body := "#!/bin/sh\nset -eu\nmkdir -p \"$1\"\nprintf 'postgres cron backup' > \"$1/xirang-postgres-20261009-020000.dump\"\nprintf 'xirang-postgres-20261009-020000.dump\\n' >&3\n"
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}
func waitCronBackupPostgresBlock(t *testing.T, db *sql.DB, blockerPID int) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		var waiterPID int
		err := db.QueryRowContext(ctx, `
			SELECT COALESCE(MIN(pid), 0)
			FROM pg_stat_activity
			WHERE pid <> pg_backend_pid()
			  AND wait_event_type = 'Lock'
			  AND $1 = ANY(pg_blocking_pids(pid))
			  AND query ILIKE '%cron_backup_health%'
			  AND query ILIKE '%for update%'`, blockerPID).Scan(&waiterPID)
		if err != nil {
			t.Fatalf("inspect PostgreSQL cron backup lock contention: %v", err)
		}
		if waiterPID != 0 {
			return waiterPID
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for PostgreSQL cron backup reconciliation blocked by PID %d", blockerPID)
		default:
			runtime.Gosched()
		}
	}
}

func TestCronBackupWorkerPostgresCompetingWorkersAndRecovery(t *testing.T) {
	requireCronBackupPostgres(t)
	fixture, cfg, anchor := newCronBackupWorkerFixture(t, "postgres")
	first := NewCronBackupWorker(fixture.db, nil, cfg)
	second := NewCronBackupWorker(fixture.db, nil, cfg)
	staleAt := anchor.Add(cfg.MaxAge + time.Nanosecond)

	// Enrollment is deliberately completed before the external holder starts.
	// The holder must therefore contend with reconciliation's SELECT ... FOR
	// UPDATE, not with first-use enrollment or migration setup.
	if err := first.enroll(context.Background(), anchor); err != nil {
		t.Fatalf("enroll first PostgreSQL worker: %v", err)
	}
	if err := second.enroll(context.Background(), anchor); err != nil {
		t.Fatalf("enroll second PostgreSQL worker: %v", err)
	}

	workerCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	holder := fixture.db.WithContext(workerCtx).Begin()
	if holder.Error != nil {
		t.Fatalf("begin external PostgreSQL holder transaction: %v", holder.Error)
	}
	holderReleased := false
	t.Cleanup(func() {
		if !holderReleased {
			_ = holder.Rollback().Error
		}
	})
	var blockerPID int
	if err := holder.Raw("SELECT pg_backend_pid()").Scan(&blockerPID).Error; err != nil {
		t.Fatalf("read PostgreSQL holder PID: %v", err)
	}
	var lockedSource string
	if err := holder.Raw(`
		SELECT source_key
		FROM cron_backup_health
		WHERE source_key = ?
		FOR UPDATE`, first.sourceKey).Scan(&lockedSource).Error; err != nil {
		t.Fatalf("hold PostgreSQL cron backup cursor row: %v", err)
	}
	if lockedSource != first.sourceKey {
		t.Fatalf("holder locked source=%q, want %q", lockedSource, first.sourceKey)
	}

	start := make(chan struct{})
	started := make(chan struct{}, 2)
	done := make(chan struct{}, 2)
	for _, worker := range []*CronBackupWorker{first, second} {
		go func(worker *CronBackupWorker) {
			<-start
			started <- struct{}{}
			worker.tickAt(workerCtx, staleAt)
			done <- struct{}{}
		}(worker)
	}
	close(start)
	for range 2 {
		select {
		case <-started:
		case <-workerCtx.Done():
			t.Fatalf("PostgreSQL competing worker did not start: %v", workerCtx.Err())
		}
	}

	// This is the contender barrier: do not release the row until PostgreSQL
	// reports a real worker reconciliation waiting on the holder's exact PID.
	waiterPID := waitCronBackupPostgresBlock(t, fixture.sqlDB, blockerPID)
	if waiterPID == blockerPID {
		t.Fatalf("PostgreSQL waiter PID=%d is the external holder PID", waiterPID)
	}

	if err := holder.Commit().Error; err != nil {
		t.Fatalf("release external PostgreSQL holder transaction: %v", err)
	}
	holderReleased = true
	for range 2 {
		select {
		case <-done:
		case <-workerCtx.Done():
			t.Fatalf("PostgreSQL competing worker did not finish after holder release: %v", workerCtx.Err())
		}
	}

	cursor := cronBackupCursor(t, fixture.db, first.sourceKey)
	if !cursor.FaultActive || cursor.AlertID == nil {
		t.Fatalf("competing PostgreSQL workers cursor=%+v, want active cycle", cursor)
	}
	if got := cronBackupAlertCount(t, fixture.db, first.sourceKey); got != 1 {
		t.Fatalf("competing PostgreSQL workers created %d alerts, want one", got)
	}
	alertID := *cursor.AlertID

	successScript := cronBackupPostgresSuccessScript(t)
	if err := cronbackup.Run(context.Background(), cfg, successScript, filepath.Join(t.TempDir(), "output")); err != nil {
		t.Fatalf("postgres success run: %v", err)
	}
	first.tickAt(context.Background(), time.Now().UTC().Add(time.Minute))
	cursor = cronBackupCursor(t, fixture.db, first.sourceKey)
	if cursor.FaultActive || cursor.AlertID == nil || *cursor.AlertID != alertID {
		t.Fatalf("successful PostgreSQL recovery cursor=%+v", cursor)
	}
	var alert struct {
		Status string
	}
	if err := fixture.db.Table("alerts").Select("status").Where("id = ?", alertID).Scan(&alert).Error; err != nil {
		t.Fatal(err)
	}
	if alert.Status != "resolved" {
		t.Fatalf("recovered PostgreSQL alert status=%q, want resolved", alert.Status)
	}
}

func TestCronBackupWorkerPostgresRollsBackAlertAfterInsertion(t *testing.T) {
	requireCronBackupPostgres(t)
	fixture, cfg, anchor := newCronBackupWorkerFixture(t, "postgres")
	worker := NewCronBackupWorker(fixture.db, nil, cfg)
	if err := fixture.db.Exec(`
		CREATE OR REPLACE FUNCTION test_cron_backup_abort_after_alert()
		RETURNS trigger
		LANGUAGE plpgsql
		AS $$
		BEGIN
			IF NEW.fault_active AND NOT OLD.fault_active THEN
				RAISE EXCEPTION 'forced cron backup cursor failure after alert insertion';
			END IF;
			RETURN NEW;
		END;
		$$`).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Exec(`
		CREATE TRIGGER trg_test_cron_backup_after_alert
		BEFORE UPDATE ON cron_backup_health
		FOR EACH ROW EXECUTE FUNCTION test_cron_backup_abort_after_alert()
	`).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = fixture.db.Exec("DROP TRIGGER IF EXISTS trg_test_cron_backup_after_alert ON cron_backup_health").Error
		_ = fixture.db.Exec("DROP FUNCTION IF EXISTS test_cron_backup_abort_after_alert()").Error
	})

	worker.tickAt(context.Background(), anchor.Add(cfg.MaxAge+time.Nanosecond))
	cursor := cronBackupCursor(t, fixture.db, worker.sourceKey)
	if cursor.FaultActive || cursor.AlertID != nil {
		t.Fatalf("rolled-back PostgreSQL cursor=%+v, want clean", cursor)
	}
	if got := cronBackupAlertCount(t, fixture.db, worker.sourceKey); got != 0 {
		t.Fatalf("alert insertion survived rollback: count=%d", got)
	}
}
