package alerting

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/cronbackup"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"golang.org/x/sys/unix"
	"gorm.io/gorm"
)

func newCronBackupWorkerFixture(t *testing.T, engine string) (*migrationRuntimeFixture, cronbackup.Config, time.Time) {
	t.Helper()
	fixture := newAlertingMigrationRuntimeFixture(t, engine)
	version, dirty, err := fixture.migrator.Version()
	if err != nil {
		t.Fatalf("read %s fixture migration version: %v", engine, err)
	}
	if dirty {
		t.Fatalf("%s fixture migration is dirty at version %d", engine, version)
	}
	if version < 92 {
		if err := fixture.migrator.Migrate(92); err != nil {
			t.Fatalf("migrate %s fixture to 92: %v", engine, err)
		}
	}
	stateDir := filepath.Join(t.TempDir(), "cron-db-state")
	cfg := cronbackup.Config{StateDirectory: stateDir, Engine: engine, MaxAge: time.Hour}
	anchor := time.Now().UTC().Truncate(time.Second)
	if err := cronbackup.Initialize(context.Background(), cfg, anchor); err != nil {
		t.Fatalf("initialize %s cron state: %v", engine, err)
	}
	return fixture, cfg, anchor
}
func (w *CronBackupWorker) tickAt(ctx context.Context, now time.Time) {
	fixed := func() time.Time { return now }
	w.tickWithClocks(ctx, fixed, fixed)
}

func cronBackupTestScript(t *testing.T, success bool) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "backup-test.sh")
	artifact := "xirang-sqlite-20261009-020000.db"
	body := "#!/bin/sh\nset -eu\nout=$1\nmkdir -p \"$out\"\n"
	if success {
		body += "printf 'cron worker test backup' > \"$out/" + artifact + "\"\nprintf '" + artifact + "\\n' >&3\n"
	} else {
		body += "exit 7\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func cronBackupCursor(t *testing.T, db *gorm.DB, sourceKey string) model.CronBackupHealth {
	t.Helper()
	var cursor model.CronBackupHealth
	if err := db.Where("source_key = ?", sourceKey).First(&cursor).Error; err != nil {
		t.Fatal(err)
	}
	return cursor
}

func cronBackupAlertCount(t *testing.T, db *gorm.DB, sourceKey string) int64 {
	t.Helper()
	var count int64
	if err := db.Model(&model.Alert{}).
		Where("error_code = ?", "XR-CRON-DB-BACKUP-"+sourceKey).
		Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	return count
}

func sameAlertID(a, b *uint) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func TestCronBackupWorkerDisabledWithoutStateDirectory(t *testing.T) {
	worker := NewCronBackupWorker(nil, nil, cronbackup.Config{Engine: "sqlite", MaxAge: time.Hour})
	if worker.enabled || worker.sourceKey != "" {
		t.Fatalf("disabled cron worker = %+v, want no enrollment identity", worker)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	worker.Run(ctx)
	if err := worker.Shutdown(context.Background()); err != nil {
		t.Fatalf("disabled worker shutdown: %v", err)
	}
}

func TestCronBackupWorkerFailureRecoveryAndDurableDelivery(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString([]byte("FAKE_CRON_KEY_1234_FOR_TEST_ONLY")))
	secure.ResetForTesting()
	fixture, cfg, anchor := newCronBackupWorkerFixture(t, "sqlite")
	dispatcher := NewDispatcher(fixture.db, nil, nil)
	worker := NewCronBackupWorker(fixture.db, dispatcher, cfg)

	// The exact first-grace boundary remains neutral; one nanosecond beyond it
	// is stale and creates one durable pending alert.
	worker.tickAt(context.Background(), anchor)
	worker.tickAt(context.Background(), anchor.Add(cfg.MaxAge))
	cursor := cronBackupCursor(t, fixture.db, worker.sourceKey)
	if cursor.FaultActive {
		t.Fatal("never-run state was faulted inside initial grace")
	}
	worker.tickAt(context.Background(), anchor.Add(cfg.MaxAge+time.Nanosecond))
	cursor = cronBackupCursor(t, fixture.db, worker.sourceKey)
	if !cursor.FaultActive || cursor.AlertID == nil {
		t.Fatalf("stale first-run state cursor = %+v, want active alert", cursor)
	}
	if got := cronBackupAlertCount(t, fixture.db, worker.sourceKey); got != 1 {
		t.Fatalf("stale first-run alert count=%d, want 1", got)
	}
	firstAlertID := *cursor.AlertID

	var requests atomic.Int64
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()
	integration := model.Integration{
		Name: "cron-worker-webhook", Type: "webhook", Endpoint: receiver.URL,
		Enabled: true, FailThreshold: 1, CooldownMinutes: 0,
	}
	if err := fixture.db.Create(&integration).Error; err != nil {
		t.Fatalf("create webhook integration: %v", err)
	}

	// A fresh RetryWorker represents a process restart. It consumes the alert's
	// pending decision and performs the only network call; a second fresh worker
	// must not duplicate the already-sent delivery.
	retry := NewRetryWorker(fixture.db)
	retry.dispatcher = dispatcher
	retry.sendFn = dispatcher.send
	retry.tick(context.Background(), time.Now().UTC())
	restartedRetry := NewRetryWorker(fixture.db)
	restartedRetry.dispatcher = dispatcher
	restartedRetry.sendFn = dispatcher.send
	restartedRetry.tick(context.Background(), time.Now().UTC())
	if got := requests.Load(); got != 1 {
		t.Fatalf("webhook requests after retry restart=%d, want 1", got)
	}
	var deliveries int64
	if err := fixture.db.Model(&model.AlertDelivery{}).Where("alert_id = ?", firstAlertID).Count(&deliveries).Error; err != nil {
		t.Fatal(err)
	}
	if deliveries != 1 {
		t.Fatalf("durable delivery rows=%d, want 1", deliveries)
	}

	// Manual resolve does not reopen or create a second alert while the same
	// fault cursor remains active.
	if err := fixture.db.Model(&model.Alert{}).Where("id = ?", firstAlertID).
		Update("status", "resolved").Error; err != nil {
		t.Fatal(err)
	}
	worker.tickAt(context.Background(), time.Now().UTC())
	if got := cronBackupAlertCount(t, fixture.db, worker.sourceKey); got != 1 {
		t.Fatalf("manual-resolved continuing fault alert count=%d, want 1", got)
	}

	// A valid fresh success clears only this cursor's alert. The already
	// resolved alert receives no synthetic recovery delivery.
	successScript := cronBackupTestScript(t, true)
	outputDir := filepath.Join(t.TempDir(), "backup-output")
	if err := cronbackup.Run(context.Background(), cfg, successScript, outputDir); err != nil {
		t.Fatalf("run successful backup: %v", err)
	}
	worker.tickAt(context.Background(), time.Now().UTC())
	cursor = cronBackupCursor(t, fixture.db, worker.sourceKey)
	if cursor.FaultActive {
		t.Fatal("fresh successful state did not clear active cursor")
	}
	var firstAlert model.Alert
	if err := fixture.db.First(&firstAlert, firstAlertID).Error; err != nil {
		t.Fatal(err)
	}
	if firstAlert.Status != "resolved" {
		t.Fatalf("first alert status=%q, want resolved", firstAlert.Status)
	}

	// The next failed attempt begins a new continuous cycle and gets one new
	// pending alert, without touching the prior resolved row.
	failedScript := cronBackupTestScript(t, false)
	if err := cronbackup.Run(context.Background(), cfg, failedScript, outputDir); err == nil {
		t.Fatal("failed backup returned nil")
	}
	worker.tickAt(context.Background(), time.Now().UTC())
	if got := cronBackupAlertCount(t, fixture.db, worker.sourceKey); got != 2 {
		t.Fatalf("new failure alert count=%d, want 2", got)
	}
	cursor = cronBackupCursor(t, fixture.db, worker.sourceKey)
	if !cursor.FaultActive || cursor.AlertID == nil || *cursor.AlertID == firstAlertID {
		t.Fatalf("new failure cursor=%+v, want a new active alert", cursor)
	}
}
func TestCronBackupWorkerSamplesClockAfterLockedPublication(t *testing.T) {
	fixture, cfg, _ := newCronBackupWorkerFixture(t, "sqlite")
	worker := NewCronBackupWorker(fixture.db, nil, cfg)
	before := time.Now().UTC().Add(-time.Hour)
	after := time.Now().UTC().Add(time.Hour)
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	worker.clock = func() time.Time { return after }
	worker.enrollmentClock = func() time.Time {
		if calls.Add(1) == 1 {
			close(started)
			<-release
			return before
		}
		return before
	}
	done := make(chan struct{})
	go func() {
		worker.tick(context.Background())
		close(done)
	}()
	<-started

	successScript := cronBackupTestScript(t, true)
	if err := cronbackup.Run(context.Background(), cfg, successScript, filepath.Join(t.TempDir(), "output")); err != nil {
		t.Fatalf("publish successful backup: %v", err)
	}
	close(release)
	<-done

	cursor := cronBackupCursor(t, fixture.db, worker.sourceKey)
	if cursor.FaultActive || cursor.AlertID != nil {
		t.Fatalf("published success became a fault: %+v", cursor)
	}
	if got := cronBackupAlertCount(t, fixture.db, worker.sourceKey); got != 0 {
		t.Fatalf("published success created %d alerts", got)
	}
}

func TestCronBackupWorkerStateLockUnavailablePreservesCursor(t *testing.T) {
	fixture, cfg, anchor := newCronBackupWorkerFixture(t, "sqlite")
	worker := NewCronBackupWorker(fixture.db, nil, cfg)
	worker.tickAt(context.Background(), anchor)
	before := cronBackupCursor(t, fixture.db, worker.sourceKey)

	lockPath := filepath.Join(cfg.StateDirectory, cfg.Engine, "state.lock")
	lockFile, err := os.OpenFile(lockPath, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lockFile.Close() }()
	if err := unix.Flock(int(lockFile.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	worker.tickAt(ctx, anchor.Add(2*time.Hour))
	cancel()

	after := cronBackupCursor(t, fixture.db, worker.sourceKey)
	if after.FaultActive != before.FaultActive || !sameAlertID(after.AlertID, before.AlertID) || after.HighestRevision != before.HighestRevision || !after.EnrolledAt.Equal(before.EnrolledAt) {
		t.Fatalf("state-lock failure changed cursor: before=%+v after=%+v", before, after)
	}
	if got := cronBackupAlertCount(t, fixture.db, worker.sourceKey); got != 0 {
		t.Fatalf("state-lock failure created %d alerts", got)
	}
}

func TestCronBackupWorkerDatabaseFailureAfterAlertInsertionRollsBack(t *testing.T) {
	fixture, cfg, anchor := newCronBackupWorkerFixture(t, "sqlite")
	worker := NewCronBackupWorker(fixture.db, nil, cfg)
	worker.tickAt(context.Background(), anchor)
	before := cronBackupCursor(t, fixture.db, worker.sourceKey)

	if err := fixture.db.Exec(`
		CREATE TRIGGER trg_test_cron_backup_health_failure
		BEFORE UPDATE ON cron_backup_health
		WHEN NEW.fault_active = 1 AND OLD.fault_active = 0
		BEGIN
			SELECT RAISE(ABORT, 'forced cron backup health failure after alert insertion');
		END`).Error; err != nil {
		t.Fatal(err)
	}
	defer fixture.db.Exec("DROP TRIGGER trg_test_cron_backup_health_failure")

	worker.tickAt(context.Background(), anchor.Add(cfg.MaxAge+time.Nanosecond))
	after := cronBackupCursor(t, fixture.db, worker.sourceKey)
	if after.FaultActive || after.AlertID != nil || after.HighestRevision != before.HighestRevision || !after.EnrolledAt.Equal(before.EnrolledAt) {
		t.Fatalf("failed cursor transaction changed state: before=%+v after=%+v", before, after)
	}
	if got := cronBackupAlertCount(t, fixture.db, worker.sourceKey); got != 0 {
		t.Fatalf("failed cursor transaction created %d alerts", got)
	}
}

func TestCronBackupWorkerSourceIdentityAndRevisionFence(t *testing.T) {
	fixture, cfg, _ := newCronBackupWorkerFixture(t, "sqlite")
	worker := NewCronBackupWorker(fixture.db, nil, cfg)
	script := cronBackupTestScript(t, true)
	if err := cronbackup.Run(context.Background(), cfg, script, filepath.Join(t.TempDir(), "output")); err != nil {
		t.Fatal(err)
	}
	worker.tickAt(context.Background(), time.Now().UTC())
	before := cronBackupCursor(t, fixture.db, worker.sourceKey)
	if before.SourceID == "" || before.HighestRevision <= 0 {
		t.Fatalf("successful observation did not enroll identity/high-water: %+v", before)
	}

	statePath := filepath.Join(cfg.StateDirectory, cfg.Engine, "state.json")
	stateBytes, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]interface{}
	if err := json.Unmarshal(stateBytes, &state); err != nil {
		t.Fatal(err)
	}
	state["revision"] = float64(before.HighestRevision - 1)
	lowered, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, lowered, 0o600); err != nil {
		t.Fatal(err)
	}
	worker.tickAt(context.Background(), time.Now().UTC())
	cursor := cronBackupCursor(t, fixture.db, worker.sourceKey)
	if !cursor.FaultActive || cursor.HighestRevision != before.HighestRevision {
		t.Fatalf("revision rollback was not fenced: before=%+v after=%+v", before, cursor)
	}
	if got := cronBackupAlertCount(t, fixture.db, worker.sourceKey); got != 1 {
		t.Fatalf("revision rollback alert count=%d, want 1", got)
	}

	state["revision"] = float64(before.HighestRevision + 1)
	state["source_id"] = "ffffffffffffffffffffffffffffffff"
	tampered, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	worker.tickAt(context.Background(), time.Now().UTC())
	if got := cronBackupAlertCount(t, fixture.db, worker.sourceKey); got != 1 {
		t.Fatalf("identity tamper created a second alert: %d", got)
	}
}

func TestCronBackupWorkerConcurrentTicksCreateOneCycle(t *testing.T) {
	fixture, cfg, anchor := newCronBackupWorkerFixture(t, "sqlite")
	first := NewCronBackupWorker(fixture.db, nil, cfg)
	second := NewCronBackupWorker(fixture.db, nil, cfg)
	at := anchor.Add(cfg.MaxAge + time.Nanosecond)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); first.tickAt(context.Background(), at) }()
	go func() { defer wg.Done(); second.tickAt(context.Background(), at) }()
	wg.Wait()
	if got := cronBackupAlertCount(t, fixture.db, first.sourceKey); got != 1 {
		t.Fatalf("concurrent ticks created %d alerts, want one", got)
	}
	cursor := cronBackupCursor(t, fixture.db, first.sourceKey)
	if !cursor.FaultActive || cursor.AlertID == nil {
		t.Fatalf("concurrent cursor=%+v, want active cycle", cursor)
	}
}

func TestCronBackupWorkerRunAndShutdownLifecycle(t *testing.T) {
	fixture, cfg, _ := newCronBackupWorkerFixture(t, "sqlite")
	worker := NewCronBackupWorker(fixture.db, nil, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	go worker.Run(ctx)
	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer shutdownCancel()
	if err := worker.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}
