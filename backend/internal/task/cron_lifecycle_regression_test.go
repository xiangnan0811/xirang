package task

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/cronutil"
	"xirang/backend/internal/model"
	taskexec "xirang/backend/internal/task/executor"
	taskscheduler "xirang/backend/internal/task/scheduler"

	"gorm.io/gorm"
)

type failOnceBlockingRetryExecutor struct {
	calls        atomic.Int32
	retryStarted chan struct{}
	retryRelease chan struct{}
	retryOnce    sync.Once
	releaseOnce  sync.Once
}

func newFailOnceBlockingRetryExecutor() *failOnceBlockingRetryExecutor {
	return &failOnceBlockingRetryExecutor{
		retryStarted: make(chan struct{}),
		retryRelease: make(chan struct{}),
	}
}

func (e *failOnceBlockingRetryExecutor) Run(
	ctx context.Context,
	_ model.Task,
	_ taskexec.LogFunc,
	_ taskexec.ProgressFunc,
) (int, error) {
	switch e.calls.Add(1) {
	case 1:
		return 1, errors.New("deterministic first attempt failure")
	case 2:
		e.retryOnce.Do(func() { close(e.retryStarted) })
		select {
		case <-e.retryRelease:
			return 0, nil
		case <-ctx.Done():
			return 1, ctx.Err()
		}
	default:
		return 0, nil
	}
}
func (e *failOnceBlockingRetryExecutor) Release() {
	e.releaseOnce.Do(func() { close(e.retryRelease) })
}

func TestCronTriggerReleasesLocalOwnershipWhenDurableClaimRefused(t *testing.T) {
	db := openConcurrentManagerTestDB(t)
	exec := &successExecutor{}
	first := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, nil, 8, 90)
	second := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, first)
	shutdownManagerOnCleanup(t, second)

	taskEntity := seedTaskForManagerTest(t, db)
	scheduledAt := time.Date(2026, 9, 14, 2, 3, 4, 0, time.UTC)
	occurrence, err := first.ensureCronOccurrence(context.Background(), taskEntity.ID, scheduledAt)
	if err != nil {
		t.Fatalf("persist cron occurrence: %v", err)
	}
	claimed, err := second.claimCronOccurrence(context.Background(), occurrence.ID)
	if err != nil {
		t.Fatalf("claim cron occurrence from second manager: %v", err)
	}
	if !claimed {
		t.Fatal("second manager did not claim the cron occurrence")
	}

	if err := first.TriggerFromScheduler(taskEntity.ID, scheduledAt); err != nil {
		t.Fatalf("first manager trigger should be a no-op: %v", err)
	}
	if _, loaded := first.pendingRuns.Load(taskEntity.ID); loaded {
		t.Fatal("durable claim refusal leaked first manager pending ownership")
	}
	if _, loaded := first.chainRunner.Load(taskEntity.ID); loaded {
		t.Fatal("durable claim refusal leaked first manager chain ownership")
	}

	var stored model.TaskCronOccurrence
	if err := db.First(&stored, occurrence.ID).Error; err != nil {
		t.Fatalf("reload cron occurrence: %v", err)
	}
	if stored.DispatchOwnerID != second.cronOccurrenceOwner() {
		t.Fatalf("durable owner %q changed after refusal, want %q", stored.DispatchOwnerID, second.cronOccurrenceOwner())
	}
	if stored.State != model.TaskCronOccurrenceStateQueued || stored.TaskRunID != nil {
		t.Fatalf("refused occurrence state=%q run_id=%v, want queued without run", stored.State, stored.TaskRunID)
	}
}

func TestCronTriggerReleasesLocalOwnershipWhenDurableClaimErrors(t *testing.T) {
	db := openManagerTestDB(t)
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)

	taskEntity := seedTaskForManagerTest(t, db)
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Updates(map[string]interface{}{
		"status":    string(StatusSuccess),
		"enabled":   true,
		"cron_spec": "@every 1h",
	}).Error; err != nil {
		t.Fatalf("prepare claim-error task: %v", err)
	}
	scheduledAt := time.Date(2026, 9, 14, 3, 4, 5, 0, time.UTC)
	occurrence, err := manager.ensureCronOccurrence(context.Background(), taskEntity.ID, scheduledAt)
	if err != nil {
		t.Fatalf("persist cron occurrence: %v", err)
	}
	// Force the claim helper's authority error after triggerCore has acquired
	// local ownership. The defer must release local state on this path too.
	cronOwner := manager.cronOccurrenceOwnerID
	executionOwner := manager.executionOwnerID
	manager.cronOccurrenceOwnerID = ""
	manager.executionOwnerID = ""
	if err := manager.TriggerFromScheduler(taskEntity.ID, scheduledAt); err == nil ||
		!strings.Contains(err.Error(), "owner unavailable") {
		t.Fatalf("claim error = %v, want cron owner unavailable", err)
	}
	if _, loaded := manager.pendingRuns.Load(taskEntity.ID); loaded {
		t.Fatal("durable claim error leaked pending ownership")
	}
	if _, loaded := manager.chainRunner.Load(taskEntity.ID); loaded {
		t.Fatal("durable claim error leaked chain ownership")
	}

	var stored model.TaskCronOccurrence
	if err := db.First(&stored, occurrence.ID).Error; err != nil {
		t.Fatalf("reload cron occurrence: %v", err)
	}
	if stored.DispatchOwnerID != "" || stored.State != model.TaskCronOccurrenceStateQueued {
		t.Fatalf("claim error changed occurrence owner=%q state=%q", stored.DispatchOwnerID, stored.State)
	}

	// Restore the authority and retry the same durable occurrence. A failed
	// claim must not make a later manual/scheduler delivery permanently busy.
	manager.cronOccurrenceOwnerID = cronOwner
	manager.executionOwnerID = executionOwner
	runID, err := manager.triggerCore(taskEntity.ID, "cron", generateChainRunID(), nil, &scheduledAt)
	if err != nil || runID == 0 {
		t.Fatalf("retry after claim error run_id=%d err=%v", runID, err)
	}
	run := waitTaskRunTerminal(t, db, runID)
	manager.taskWG.Wait()
	if run.Status != model.TaskRunStatusSuccess {
		t.Fatalf("retry after claim error status=%q error=%q", run.Status, run.LastError)
	}
}

func TestSyncScheduleQueuesOverdueIntentWithoutDowntimeSkip(t *testing.T) {
	db := openManagerTestDB(t)
	exec := &successExecutor{}
	cron := taskscheduler.NewCronScheduler()
	manager := NewManager(db, stubExecutorFactory{executor: exec}, nil, cron, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)

	taskEntity := seedTaskForManagerTest(t, db)
	scheduledAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Updates(map[string]interface{}{
		"status":      string(StatusSuccess),
		"enabled":     true,
		"cron_spec":   "@every 1h",
		"skip_next":   false,
		"next_run_at": scheduledAt,
	}).Error; err != nil {
		t.Fatalf("prepare scheduled task: %v", err)
	}

	if err := manager.SyncSchedule(taskEntity); err != nil {
		t.Fatalf("sync schedule: %v", err)
	}
	expectedNext := cronutil.NextAfter("@every 1h", scheduledAt)
	var scheduledTask model.Task
	if err := db.First(&scheduledTask, taskEntity.ID).Error; err != nil {
		t.Fatalf("reload schedule cursor after sync: %v", err)
	}
	if expectedNext == nil || scheduledTask.NextRunAt == nil ||
		!scheduledTask.NextRunAt.Equal(*expectedNext) {
		t.Fatalf("sync cursor=%v, want deadline anchored after %v", scheduledTask.NextRunAt, scheduledAt)
	}
	var occurrenceCount int64
	if err := db.Model(&model.TaskCronOccurrence{}).
		Where("task_id = ? AND scheduled_at = ?", taskEntity.ID, scheduledAt).
		Count(&occurrenceCount).Error; err != nil {
		t.Fatalf("count occurrences after sync: %v", err)
	}
	if occurrenceCount != 1 {
		t.Fatalf("sync persisted %d occurrence row(s), want one queued intent", occurrenceCount)
	}
	var queued model.TaskCronOccurrence
	if err := db.Where("task_id = ? AND scheduled_at = ?", taskEntity.ID, scheduledAt).
		First(&queued).Error; err != nil {
		t.Fatalf("reload queued occurrence: %v", err)
	}
	if queued.State != model.TaskCronOccurrenceStateQueued || queued.TaskRunID != nil {
		t.Fatalf("sync occurrence state=%q run_id=%v, want queued without run", queued.State, queued.TaskRunID)
	}

	runID, err := manager.triggerCore(taskEntity.ID, "cron", generateChainRunID(), nil, &scheduledAt)
	if err != nil {
		t.Fatalf("deliver scheduled callback after sync: %v", err)
	}
	if runID == 0 {
		t.Fatal("scheduled callback was discarded after reconciliation")
	}
	run := waitTaskRunTerminal(t, db, runID)
	manager.taskWG.Wait()
	if run.Status != model.TaskRunStatusSuccess {
		t.Fatalf("scheduled run status=%q error=%q, want success", run.Status, run.LastError)
	}
	if got := exec.Calls(); got != 1 {
		t.Fatalf("scheduled callback executor calls=%d, want 1", got)
	}
	var occurrence model.TaskCronOccurrence
	if err := db.Where("task_id = ? AND scheduled_at = ?", taskEntity.ID, scheduledAt).
		First(&occurrence).Error; err != nil {
		t.Fatalf("reload delivered occurrence: %v", err)
	}
	if occurrence.State != model.TaskCronOccurrenceStateDispatched || occurrence.TaskRunID == nil {
		t.Fatalf("delivered occurrence state=%q run_id=%v, want dispatched with run", occurrence.State, occurrence.TaskRunID)
	}
}

func TestRestartReconciliationQueuesDueNextRunWithoutCallback(t *testing.T) {
	db := openManagerTestDB(t)
	exec := &successExecutor{}
	cron := taskscheduler.NewCronScheduler()
	manager := NewManager(db, stubExecutorFactory{executor: exec}, nil, cron, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)

	taskEntity := seedTaskForManagerTest(t, db)
	scheduledAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Updates(map[string]interface{}{
		"status":      string(StatusSuccess),
		"enabled":     true,
		"cron_spec":   "@every 1h",
		"next_run_at": scheduledAt,
	}).Error; err != nil {
		t.Fatalf("prepare restart task: %v", err)
	}

	// This is the startup reconciliation phase. No robfig callback has run;
	// the durable deadline itself must become queued intent.
	if err := manager.reconcileSchedules(context.Background()); err != nil {
		t.Fatalf("reconcile schedules after restart: %v", err)
	}
	var occurrence model.TaskCronOccurrence
	if err := db.Where("task_id = ? AND scheduled_at = ?", taskEntity.ID, scheduledAt).
		First(&occurrence).Error; err != nil {
		t.Fatalf("load restart occurrence: %v", err)
	}
	if occurrence.State != model.TaskCronOccurrenceStateQueued || occurrence.TaskRunID != nil {
		t.Fatalf("restart occurrence state=%q run_id=%v, want queued without callback", occurrence.State, occurrence.TaskRunID)
	}

	if err := manager.drainCronOccurrences(context.Background()); err != nil {
		t.Fatalf("drain restart occurrence: %v", err)
	}
	if err := db.First(&occurrence, occurrence.ID).Error; err != nil {
		t.Fatalf("reload drained restart occurrence: %v", err)
	}
	if occurrence.State != model.TaskCronOccurrenceStateDispatched || occurrence.TaskRunID == nil {
		t.Fatalf("drained restart occurrence state=%q run_id=%v, want dispatched with run", occurrence.State, occurrence.TaskRunID)
	}
	run := waitTaskRunTerminal(t, db, *occurrence.TaskRunID)
	manager.taskWG.Wait()
	if run.Status != model.TaskRunStatusSuccess {
		t.Fatalf("restart recovered run status=%q error=%q, want success", run.Status, run.LastError)
	}
	if got := exec.Calls(); got != 1 {
		t.Fatalf("restart recovered executor calls=%d, want 1", got)
	}
}

func TestReplacedCronCallbackCannotCreateOccurrenceForNewSchedule(t *testing.T) {
	db := openManagerTestDB(t)
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)

	taskEntity := seedTaskForManagerTest(t, db)
	const oldSpec = "@every 1h"
	const newSpec = "@every 2h"
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Updates(map[string]interface{}{
		"status":    string(StatusSuccess),
		"enabled":   true,
		"cron_spec": newSpec,
	}).Error; err != nil {
		t.Fatalf("replace task cron spec: %v", err)
	}
	scheduledAt := time.Date(2026, 9, 14, 4, 5, 6, 0, time.UTC)
	if _, err := manager.triggerCoreWithExpectedCronSpec(
		taskEntity.ID, "cron", generateChainRunID(), nil, &scheduledAt, oldSpec,
	); err != nil {
		t.Fatalf("stale callback returned error: %v", err)
	}
	var occurrenceCount int64
	if err := db.Model(&model.TaskCronOccurrence{}).
		Where("task_id = ? AND scheduled_at = ?", taskEntity.ID, scheduledAt).
		Count(&occurrenceCount).Error; err != nil {
		t.Fatalf("count stale callback occurrence: %v", err)
	}
	if occurrenceCount != 0 {
		t.Fatalf("stale callback created %d occurrence row(s) after schedule replacement", occurrenceCount)
	}
}

// TestCronCallbackReconcileBarrierOrdering exercises the production callback
// and startup reconciliation against the same durable timestamp in both
// interleavings. The create callback is a database barrier, so neither path
// can rely on an in-memory scheduler marker to decide ownership.
func TestCronCallbackReconcileBarrierOrdering(t *testing.T) {
	t.Run("callback_then_reconcile", func(t *testing.T) {
		runCronCallbackReconcileBarrier(t, true)
	})
	t.Run("reconcile_then_callback", func(t *testing.T) {
		runCronCallbackReconcileBarrier(t, false)
	})
}

func runCronCallbackReconcileBarrier(t *testing.T, callbackFirst bool) {
	t.Helper()
	db := openConcurrentManagerTestDB(t)
	exec := &successExecutor{}
	cron := taskscheduler.NewCronScheduler()
	manager := NewManager(db, stubExecutorFactory{executor: exec}, nil, cron, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)

	taskEntity := seedTaskForManagerTest(t, db)
	scheduledAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Updates(map[string]interface{}{
		"status":      string(StatusSuccess),
		"enabled":     true,
		"cron_spec":   "@every 1h",
		"skip_next":   false,
		"next_run_at": scheduledAt,
	}).Error; err != nil {
		t.Fatalf("prepare barrier task: %v", err)
	}

	createEntered := make(chan struct{})
	createRelease := make(chan struct{})
	var createCalls atomic.Int32
	createCallback := "test:cron-occurrence-barrier:" + t.Name()
	if err := db.Callback().Create().Before("gorm:create").Register(createCallback, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Schema == nil ||
			tx.Statement.Schema.Table != "task_cron_occurrences" ||
			createCalls.Add(1) != 1 {
			return
		}
		close(createEntered)
		<-createRelease
	}); err != nil {
		t.Fatalf("register occurrence barrier: %v", err)
	}
	t.Cleanup(func() { _ = db.Callback().Create().Remove(createCallback) })

	// Reconciliation's broad task scan has no Select list; triggerCore's
	// occurrence authority query selects only id. This gives the first ordering
	// a deterministic proof that reconciliation reached the barrier's
	// transaction before the callback is released.
	reconcileEntered := make(chan struct{})
	var reconcileCalls atomic.Int32
	queryCallback := "test:cron-reconcile-observer:" + t.Name()
	if err := db.Callback().Query().After("gorm:query").Register(queryCallback, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Schema == nil ||
			tx.Statement.Schema.Table != "tasks" || len(tx.Statement.Selects) != 0 ||
			reconcileCalls.Add(1) != 1 {
			return
		}
		close(reconcileEntered)
	}); err != nil {
		t.Fatalf("register reconciliation observer: %v", err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(queryCallback) })

	triggerDone := make(chan error, 1)
	reconcileDone := make(chan error, 1)
	if callbackFirst {
		go func() {
			triggerDone <- manager.TriggerFromScheduler(taskEntity.ID, scheduledAt)
		}()
		select {
		case <-createEntered:
		case <-time.After(3 * time.Second):
			t.Fatal("callback did not reach occurrence barrier")
		}
		go func() {
			reconcileDone <- manager.reconcileSchedules(context.Background())
		}()
		select {
		case <-reconcileEntered:
		case <-time.After(3 * time.Second):
			close(createRelease)
			t.Fatal("reconciliation did not reach durable task scan")
		}
		close(createRelease)
	} else {
		go func() {
			reconcileDone <- manager.reconcileSchedules(context.Background())
		}()
		select {
		case <-createEntered:
		case <-time.After(3 * time.Second):
			t.Fatal("reconciliation did not reach occurrence barrier")
		}
		// Enter the real public scheduler callback while reconciliation owns the
		// insertion barrier, then release both transactions to race their
		// unique occurrence claim.
		go func() {
			triggerDone <- manager.TriggerFromScheduler(taskEntity.ID, scheduledAt)
		}()
		close(createRelease)
	}

	select {
	case err := <-triggerDone:
		if err != nil {
			t.Fatalf("callback trigger: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("callback trigger did not finish after barrier release")
	}
	select {
	case err := <-reconcileDone:
		if err != nil {
			t.Fatalf("reconcile schedules: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reconciliation did not finish after barrier release")
	}
	manager.taskWG.Wait()

	var occurrences []model.TaskCronOccurrence
	if err := db.Where("task_id = ? AND scheduled_at = ?", taskEntity.ID, scheduledAt).
		Find(&occurrences).Error; err != nil {
		t.Fatalf("load barrier occurrences: %v", err)
	}
	if len(occurrences) != 1 {
		t.Fatalf("barrier interleaving persisted %d occurrences, want one", len(occurrences))
	}
	if occurrences[0].State != model.TaskCronOccurrenceStateDispatched ||
		occurrences[0].TaskRunID == nil {
		t.Fatalf("barrier occurrence state=%q run_id=%v, want dispatched with run",
			occurrences[0].State, occurrences[0].TaskRunID)
	}
	var runCount int64
	if err := db.Model(&model.TaskRun{}).
		Where("task_id = ? AND trigger_type = ?", taskEntity.ID, "cron").
		Count(&runCount).Error; err != nil {
		t.Fatalf("count barrier task runs: %v", err)
	}
	if runCount != 1 {
		t.Fatalf("barrier interleaving created %d task runs, want one", runCount)
	}
	if got := exec.Calls(); got != 1 {
		t.Fatalf("barrier interleaving executor calls=%d, want one", got)
	}
	if _, loaded := manager.pendingRuns.Load(taskEntity.ID); loaded {
		t.Fatal("barrier interleaving leaked pending ownership")
	}
	if _, loaded := manager.chainRunner.Load(taskEntity.ID); loaded {
		t.Fatal("barrier interleaving leaked chain ownership")
	}
}

func TestQueuedCronIntentSurvivesReconciliationAndRestart(t *testing.T) {
	db := openManagerTestDB(t)
	exec := &successExecutor{}
	firstScheduler := taskscheduler.NewCronScheduler()
	first := NewManager(db, stubExecutorFactory{executor: exec}, nil, firstScheduler, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, first)

	taskEntity := seedTaskForManagerTest(t, db)
	scheduledAt := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Microsecond)
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Updates(map[string]interface{}{
		"status":      string(StatusSuccess),
		"enabled":     true,
		"cron_spec":   "@every 1h",
		"next_run_at": scheduledAt,
	}).Error; err != nil {
		t.Fatalf("prepare cross-cycle task: %v", err)
	}
	if err := first.reconcileSchedules(context.Background()); err != nil {
		t.Fatalf("first reconciliation: %v", err)
	}
	if err := first.reconcileSchedules(context.Background()); err != nil {
		t.Fatalf("second reconciliation: %v", err)
	}
	var queued model.TaskCronOccurrence
	if err := db.Where("task_id = ? AND scheduled_at = ?", taskEntity.ID, scheduledAt).
		First(&queued).Error; err != nil {
		t.Fatalf("load queued cross-cycle occurrence: %v", err)
	}
	if queued.State != model.TaskCronOccurrenceStateQueued || queued.TaskRunID != nil {
		t.Fatalf("cross-cycle queued occurrence state=%q run_id=%v", queued.State, queued.TaskRunID)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	if err := first.Shutdown(shutdownCtx); err != nil {
		cancel()
		t.Fatalf("shutdown first manager: %v", err)
	}
	cancel()

	restarted := NewManager(db, stubExecutorFactory{executor: exec}, nil,
		taskscheduler.NewCronScheduler(), nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, restarted)
	if err := restarted.LoadSchedules(context.Background()); err != nil {
		t.Fatalf("restart reconciliation and drain: %v", err)
	}
	restarted.taskWG.Wait()
	if err := db.First(&queued, queued.ID).Error; err != nil {
		t.Fatalf("reload cross-cycle occurrence: %v", err)
	}
	if queued.State != model.TaskCronOccurrenceStateDispatched || queued.TaskRunID == nil {
		t.Fatalf("cross-cycle occurrence state=%q run_id=%v, want dispatched with run",
			queued.State, queued.TaskRunID)
	}
	run := waitTaskRunTerminal(t, db, *queued.TaskRunID)
	if run.Status != model.TaskRunStatusSuccess {
		t.Fatalf("cross-cycle recovered run status=%q error=%q, want success",
			run.Status, run.LastError)
	}
	if got := exec.Calls(); got != 1 {
		t.Fatalf("cross-cycle executor calls=%d, want one", got)
	}
}

func TestCronClaimCompetitionSQLite(t *testing.T) {
	runCronClaimCompetition(t, openConcurrentManagerTestDB(t))
}

func TestCronClaimCompetitionPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db := openTaskTerminalPostgresDB(t, dsn)
	if err := db.AutoMigrate(
		&model.TaskLog{}, &model.TaskTrafficSample{}, &model.Alert{},
		&model.AlertDelivery{}, &model.Integration{},
	); err != nil {
		t.Fatalf("migrate PostgreSQL cron competition support tables: %v", err)
	}
	runCronClaimCompetition(t, db)
}

func runCronClaimCompetition(t *testing.T, db *gorm.DB) {
	t.Helper()
	exec := &successExecutor{}
	first := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, nil, 8, 90)
	second := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, first)
	shutdownManagerOnCleanup(t, second)

	taskEntity := seedTaskForManagerTest(t, db)
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Updates(map[string]interface{}{
		"status":    string(StatusSuccess),
		"enabled":   true,
		"cron_spec": "@every 1h",
	}).Error; err != nil {
		t.Fatalf("prepare PostgreSQL competition task: %v", err)
	}
	scheduledAt := time.Date(2026, 9, 14, 5, 6, 7, 0, time.UTC)
	if _, err := first.ensureCronOccurrence(context.Background(), taskEntity.ID, scheduledAt); err != nil {
		t.Fatalf("persist competition occurrence: %v", err)
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	go func() {
		<-start
		results <- first.TriggerFromScheduler(taskEntity.ID, scheduledAt)
	}()
	go func() {
		<-start
		results <- second.TriggerFromScheduler(taskEntity.ID, scheduledAt)
	}()
	close(start)
	for range 2 {
		select {
		case err := <-results:
			if err != nil {
				t.Fatalf("concurrent PostgreSQL cron callback: %v", err)
			}
		case <-time.After(8 * time.Second):
			t.Fatal("concurrent PostgreSQL cron callbacks did not finish")
		}
	}
	first.taskWG.Wait()
	second.taskWG.Wait()

	var runCount int64
	if err := db.Model(&model.TaskRun{}).
		Where("task_id = ? AND trigger_type = ?", taskEntity.ID, "cron").
		Count(&runCount).Error; err != nil {
		t.Fatalf("count PostgreSQL competition runs: %v", err)
	}
	if runCount != 1 {
		t.Fatalf("PostgreSQL same-occurrence competition created %d runs, want one", runCount)
	}
	var occurrence model.TaskCronOccurrence
	if err := db.Where("task_id = ? AND scheduled_at = ?", taskEntity.ID, scheduledAt).
		First(&occurrence).Error; err != nil {
		t.Fatalf("load PostgreSQL competition occurrence: %v", err)
	}
	if occurrence.State != model.TaskCronOccurrenceStateDispatched || occurrence.TaskRunID == nil {
		t.Fatalf("PostgreSQL competition occurrence state=%q run_id=%v, want dispatched with run",
			occurrence.State, occurrence.TaskRunID)
	}
	run := waitTaskRunTerminal(t, db, *occurrence.TaskRunID)
	if run.Status != model.TaskRunStatusSuccess {
		t.Fatalf("PostgreSQL competition run status=%q error=%q, want success",
			run.Status, run.LastError)
	}
	if got := exec.Calls(); got != 1 {
		t.Fatalf("PostgreSQL competition executor calls=%d, want one", got)
	}
	for name, manager := range map[string]*Manager{"first": first, "second": second} {
		if _, loaded := manager.pendingRuns.Load(taskEntity.ID); loaded {
			t.Fatalf("%s manager leaked pending ownership", name)
		}
		if _, loaded := manager.chainRunner.Load(taskEntity.ID); loaded {
			t.Fatalf("%s manager leaked chain ownership", name)
		}
	}
}

func TestSyncSchedulePreservesFutureCursorSQLite(t *testing.T) {
	runSyncSchedulePreservesFutureCursor(t, openManagerTestDB(t))
}

func TestSyncSchedulePreservesFutureCursorPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	runSyncSchedulePreservesFutureCursor(t, openTaskTerminalPostgresDB(t, dsn))
}

func runSyncSchedulePreservesFutureCursor(t *testing.T, db *gorm.DB) {
	t.Helper()
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil,
		taskscheduler.NewCronScheduler(), nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)
	taskEntity := seedTaskForManagerTest(t, db)
	nextRunAt := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Microsecond)
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Updates(map[string]interface{}{
		"status":      string(StatusSuccess),
		"enabled":     true,
		"cron_spec":   "@every 1h",
		"next_run_at": nextRunAt,
	}).Error; err != nil {
		t.Fatalf("prepare future-cursor task: %v", err)
	}
	for range 3 {
		if err := manager.SyncSchedule(taskEntity); err != nil {
			t.Fatalf("sync future-cursor schedule: %v", err)
		}
	}
	var persisted model.Task
	if err := db.First(&persisted, taskEntity.ID).Error; err != nil {
		t.Fatalf("reload future-cursor task: %v", err)
	}
	if persisted.NextRunAt == nil || !persisted.NextRunAt.Equal(nextRunAt) {
		t.Fatalf("future cursor changed after reconciliation: got %v, want %v",
			persisted.NextRunAt, nextRunAt)
	}
}

func TestCronTerminalizationPreservesFutureCursorSQLite(t *testing.T) {
	runCronTerminalizationPreservesFutureCursor(t, openManagerTestDB(t))
}

func TestCronTerminalizationPreservesFutureCursorPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	runCronTerminalizationPreservesFutureCursor(t, openTaskTerminalPostgresDB(t, dsn))
}

func runCronTerminalizationPreservesFutureCursor(t *testing.T, db *gorm.DB) {
	t.Helper()
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil,
		nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)
	taskEntity := seedTaskForManagerTest(t, db)
	nextRunAt := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Microsecond)
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Updates(map[string]interface{}{
		"status":      string(StatusRunning),
		"enabled":     true,
		"cron_spec":   "@every 1h",
		"next_run_at": nextRunAt,
	}).Error; err != nil {
		t.Fatalf("prepare terminal cursor task: %v", err)
	}
	leaseUntil := time.Now().UTC().Add(time.Minute)
	scheduledAt := nextRunAt.Add(-time.Hour)
	run := model.TaskRun{
		TaskID:              taskEntity.ID,
		NodeIDSnapshot:      taskEntity.NodeID,
		TriggerType:         "cron",
		CronScheduledAt:     &scheduledAt,
		Status:              model.TaskRunStatusRunning,
		ExecutionOwnerID:    manager.executionOwnerID,
		ExecutionLeaseUntil: &leaseUntil,
	}
	if err := db.Create(&run).Error; err != nil {
		t.Fatalf("create terminal cursor run: %v", err)
	}
	replacement := cronutil.Next("@every 1h")
	if replacement == nil {
		t.Fatal("cron replacement deadline is nil")
	}
	finishedAt := time.Now().UTC()
	if err := manager.terminalizeTaskRun(context.Background(), taskEntity.ID, run.ID,
		[]string{model.TaskRunStatusRunning}, taskStatusPtr(StatusSuccess),
		map[string]interface{}{"next_run_at": replacement, "last_error": ""},
		StatusSuccess, map[string]interface{}{"finished_at": &finishedAt}); err != nil {
		t.Fatalf("terminalize cron run: %v", err)
	}
	var persisted model.Task
	if err := db.First(&persisted, taskEntity.ID).Error; err != nil {
		t.Fatalf("reload terminal cursor task: %v", err)
	}
	if persisted.Status != string(StatusSuccess) || persisted.NextRunAt == nil ||
		!persisted.NextRunAt.Equal(nextRunAt) {
		t.Fatalf("terminal cron update reset cursor: status=%q next=%v, want success/%v",
			persisted.Status, persisted.NextRunAt, nextRunAt)
	}
}
func TestCronRetryKeepsEveryPhaseSQLite(t *testing.T) {
	runCronRetryKeepsEveryPhase(t, openManagerTestDB(t))
}

func TestCronRetryKeepsEveryPhasePostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db := openTaskTerminalPostgresDB(t, dsn)
	if err := db.AutoMigrate(
		&model.TaskLog{}, &model.TaskTrafficSample{}, &model.Alert{},
		&model.AlertDelivery{}, &model.Integration{}, &model.RestoreDrillEvidence{},
		&model.CredentialAuditEvent{},
	); err != nil {
		t.Fatalf("migrate PostgreSQL cron retry support tables: %v", err)
	}
	runCronRetryKeepsEveryPhase(t, db)
}

func runCronRetryKeepsEveryPhase(t *testing.T, db *gorm.DB) {
	t.Helper()
	const cronSpec = "@every 1h"
	exec := newFailOnceBlockingRetryExecutor()
	manager := NewManager(db, stubExecutorFactory{executor: exec}, nil,
		taskscheduler.NewCronScheduler(), nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)
	t.Cleanup(exec.Release)

	taskEntity := seedTaskForManagerTest(t, db)
	scheduledAt := time.Now().UTC().Add(10 * time.Minute).Truncate(time.Microsecond)
	nextAfterFirst := cronutil.NextAfter(cronSpec, scheduledAt)
	if nextAfterFirst == nil {
		t.Fatal("first @every cursor is nil")
	}
	nextAfterSecond := cronutil.NextAfter(cronSpec, *nextAfterFirst)
	if nextAfterSecond == nil {
		t.Fatal("second @every cursor is nil")
	}
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Updates(map[string]interface{}{
		"executor_type": "local",
		"status":        string(StatusSuccess),
		"enabled":       true,
		"cron_spec":     cronSpec,
		"next_run_at":   scheduledAt,
	}).Error; err != nil {
		t.Fatalf("prepare retry phase task: %v", err)
	}

	// Publish the live schedule before the failure. This callback is intentionally
	// not started: all dispatches below are explicit callback invocations.
	if err := manager.SyncSchedule(taskEntity); err != nil {
		t.Fatalf("initial schedule reconciliation: %v", err)
	}
	firstRunID, err := manager.triggerCore(taskEntity.ID, "cron", generateChainRunID(), nil, &scheduledAt)
	if err != nil || firstRunID == 0 {
		t.Fatalf("trigger first cron attempt run=%d err=%v", firstRunID, err)
	}
	firstRun := waitTaskRunTerminal(t, db, firstRunID)
	manager.taskWG.Wait()
	if firstRun.Status != model.TaskRunStatusFailed {
		t.Fatalf("first cron attempt status=%q error=%q, want failed", firstRun.Status, firstRun.LastError)
	}

	var retryEffect model.TaskRunEffect
	if err := db.Where("task_run_id = ? AND effect_type = ?", firstRunID, model.TaskRunEffectTypeRetry).
		First(&retryEffect).Error; err != nil {
		t.Fatalf("load retry effect: %v", err)
	}
	cursorMode, modeErr := model.ParseTaskRunEffectCronCursorMode(retryEffect.Payload)
	if modeErr != nil || cursorMode != model.TaskRunCronCursorModeRegularV1 {
		t.Fatalf("new cron retry effect omitted regular-cursor provenance: mode=%q err=%v payload=%s", cursorMode, modeErr, retryEffect.Payload)
	}
	if retryEffect.NextAttemptAt == nil {
		t.Fatal("retry effect has no durable attempt deadline")
	}

	var retryingTask model.Task
	if err := db.First(&retryingTask, taskEntity.ID).Error; err != nil {
		t.Fatalf("load retrying task: %v", err)
	}
	if retryingTask.Status != string(StatusRetrying) || retryingTask.NextRunAt == nil ||
		!retryingTask.NextRunAt.Equal(*nextAfterFirst) {
		t.Fatalf("retry entry status=%q cursor=%v, want retrying/%v",
			retryingTask.Status, retryingTask.NextRunAt, *nextAfterFirst)
	}

	// Reconciliation while the retry reservation is pending must preserve the
	// same regular cursor and the already-published live schedule.
	if err := manager.SyncSchedule(taskEntity); err != nil {
		t.Fatalf("reconcile while retrying: %v", err)
	}
	var reconciledTask model.Task
	if err := db.First(&reconciledTask, taskEntity.ID).Error; err != nil {
		t.Fatalf("reload task after retry reconciliation: %v", err)
	}
	if reconciledTask.NextRunAt == nil || !reconciledTask.NextRunAt.Equal(*nextAfterFirst) {
		t.Fatalf("retry reconciliation moved cursor to %v, want %v",
			reconciledTask.NextRunAt, *nextAfterFirst)
	}

	past := time.Now().UTC().Add(-time.Minute)
	if err := db.Model(&model.TaskRunEffect{}).Where("id = ?", retryEffect.ID).
		Update("next_attempt_at", past).Error; err != nil {
		t.Fatalf("make retry effect due: %v", err)
	}
	if err := manager.drainReadyTaskRunEffects(context.Background()); err != nil {
		t.Fatalf("drain due retry effect: %v", err)
	}
	select {
	case <-exec.retryStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("retry attempt did not reach executor")
	}

	var retryRun model.TaskRun
	if err := db.Where("task_id = ? AND trigger_type = ?", taskEntity.ID, "retry").
		Order("id ASC").First(&retryRun).Error; err != nil {
		t.Fatalf("load launched retry attempt: %v", err)
	}
	if retryRun.Status != model.TaskRunStatusRunning {
		t.Fatalf("retry attempt status=%q, want running", retryRun.Status)
	}

	// The live callback arrives while retry is executing. It must queue the
	// exact regular occurrence and advance only that regular cursor.
	if err := manager.SyncSchedule(taskEntity); err != nil {
		t.Fatalf("reconcile while retry running: %v", err)
	}
	if err := manager.TriggerFromScheduler(taskEntity.ID, *nextAfterFirst); err != nil {
		t.Fatalf("live callback during retry: %v", err)
	}
	var queuedOccurrence model.TaskCronOccurrence
	if err := db.Where("task_id = ? AND scheduled_at = ?", taskEntity.ID, *nextAfterFirst).
		First(&queuedOccurrence).Error; err != nil {
		t.Fatalf("load callback occurrence during retry: %v", err)
	}
	if queuedOccurrence.State != model.TaskCronOccurrenceStateQueued || queuedOccurrence.TaskRunID != nil {
		t.Fatalf("callback occurrence state=%q run_id=%v, want queued without run",
			queuedOccurrence.State, queuedOccurrence.TaskRunID)
	}
	var runningTask model.Task
	if err := db.First(&runningTask, taskEntity.ID).Error; err != nil {
		t.Fatalf("load task after live retry callback: %v", err)
	}
	if runningTask.NextRunAt == nil || !runningTask.NextRunAt.Equal(*nextAfterSecond) {
		t.Fatalf("live callback cursor=%v, want %v", runningTask.NextRunAt, *nextAfterSecond)
	}

	exec.Release()
	manager.taskWG.Wait()
	retryRun = waitTaskRunTerminal(t, db, retryRun.ID)
	if retryRun.Status != model.TaskRunStatusSuccess {
		t.Fatalf("retry attempt status=%q error=%q, want success", retryRun.Status, retryRun.LastError)
	}
	var afterRetry model.Task
	if err := db.First(&afterRetry, taskEntity.ID).Error; err != nil {
		t.Fatalf("load task after retry success: %v", err)
	}
	if afterRetry.Status != string(StatusSuccess) || afterRetry.NextRunAt == nil ||
		!afterRetry.NextRunAt.Equal(*nextAfterSecond) {
		t.Fatalf("retry success status=%q cursor=%v, want success/%v",
			afterRetry.Status, afterRetry.NextRunAt, *nextAfterSecond)
	}

	// Restart recovery drains the queued regular occurrence. It must not
	// re-anchor the schedule from the retry completion time.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := manager.Shutdown(shutdownCtx); err != nil {
		cancel()
		t.Fatalf("shutdown pre-restart manager: %v", err)
	}
	cancel()
	restarted := NewManager(db, stubExecutorFactory{executor: exec}, nil,
		taskscheduler.NewCronScheduler(), nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, restarted)
	if err := restarted.LoadSchedules(context.Background()); err != nil {
		t.Fatalf("restart reconciliation: %v", err)
	}
	restarted.taskWG.Wait()

	if err := db.First(&queuedOccurrence, queuedOccurrence.ID).Error; err != nil {
		t.Fatalf("reload queued occurrence after restart: %v", err)
	}
	if queuedOccurrence.State != model.TaskCronOccurrenceStateDispatched ||
		queuedOccurrence.TaskRunID == nil {
		t.Fatalf("restart occurrence state=%q run_id=%v, want dispatched with run",
			queuedOccurrence.State, queuedOccurrence.TaskRunID)
	}
	queuedRun := waitTaskRunTerminal(t, db, *queuedOccurrence.TaskRunID)
	if queuedRun.Status != model.TaskRunStatusSuccess {
		t.Fatalf("restart queued cron status=%q error=%q, want success",
			queuedRun.Status, queuedRun.LastError)
	}

	var occurrences []model.TaskCronOccurrence
	if err := db.Where("task_id = ?", taskEntity.ID).Order("scheduled_at ASC").Find(&occurrences).Error; err != nil {
		t.Fatalf("load all cron occurrences: %v", err)
	}
	if len(occurrences) != 2 ||
		!occurrences[0].ScheduledAt.Equal(scheduledAt) ||
		!occurrences[1].ScheduledAt.Equal(*nextAfterFirst) {
		t.Fatalf("cron occurrence phase split: %+v, want [%v %v]",
			occurrences, scheduledAt, *nextAfterFirst)
	}

	var runs []model.TaskRun
	if err := db.Where("task_id = ?", taskEntity.ID).Order("id ASC").Find(&runs).Error; err != nil {
		t.Fatalf("load retry phase task runs: %v", err)
	}
	if len(runs) != 3 {
		t.Fatalf("retry phase created %d task runs, want first cron/retry/queued cron", len(runs))
	}
	var cronTimes []time.Time
	for _, run := range runs {
		switch run.TriggerType {
		case "cron":
			if run.CronScheduledAt == nil {
				t.Fatalf("cron run %d has no scheduled timestamp", run.ID)
			}
			cronTimes = append(cronTimes, run.CronScheduledAt.UTC())
		case "retry":
			if run.CronScheduledAt != nil {
				t.Fatalf("retry run %d carried cron timestamp %v", run.ID, run.CronScheduledAt)
			}
		default:
			t.Fatalf("unexpected trigger type %q in retry phase run %d", run.TriggerType, run.ID)
		}
	}
	if len(cronTimes) != 2 || !cronTimes[0].Equal(scheduledAt) || !cronTimes[1].Equal(*nextAfterFirst) {
		t.Fatalf("cron run phases=%v, want [%v %v]", cronTimes, scheduledAt, *nextAfterFirst)
	}
	if got := exec.calls.Load(); got != 3 {
		t.Fatalf("executor calls=%d, want failed cron/retry/queued cron", got)
	}
	var finalTask model.Task
	if err := db.First(&finalTask, taskEntity.ID).Error; err != nil {
		t.Fatalf("load final retry phase task: %v", err)
	}
	if finalTask.Status != string(StatusSuccess) || finalTask.NextRunAt == nil ||
		!finalTask.NextRunAt.Equal(*nextAfterSecond) {
		t.Fatalf("final task status=%q cursor=%v, want success/%v",
			finalTask.Status, finalTask.NextRunAt, *nextAfterSecond)
	}
}
func TestLegacyCronRetryReanchorsLiveScheduleSQLite(t *testing.T) {
	runLegacyCronRetryReanchorsLiveSchedule(t, openManagerTestDB(t))
}

func TestLegacyCronRetryReanchorsLiveSchedulePostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db := openTaskTerminalPostgresDB(t, dsn)
	if err := db.AutoMigrate(
		&model.TaskLog{}, &model.TaskTrafficSample{}, &model.Alert{},
		&model.AlertDelivery{}, &model.Integration{}, &model.RestoreDrillEvidence{},
		&model.CredentialAuditEvent{},
	); err != nil {
		t.Fatalf("migrate PostgreSQL legacy retry support tables: %v", err)
	}
	runLegacyCronRetryReanchorsLiveSchedule(t, db)
}

func runLegacyCronRetryReanchorsLiveSchedule(t *testing.T, db *gorm.DB) {
	t.Helper()
	const cronSpec = "@every 1h"
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil,
		taskscheduler.NewCronScheduler(), nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)

	taskEntity := seedTaskForManagerTest(t, db)
	retryDue := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Updates(map[string]interface{}{
		"executor_type": "local",
		"status":        string(StatusRetrying),
		"enabled":       true,
		"cron_spec":     cronSpec,
		"next_run_at":   retryDue,
	}).Error; err != nil {
		t.Fatalf("prepare legacy retry task: %v", err)
	}
	var storedTask model.Task
	if err := db.First(&storedTask, taskEntity.ID).Error; err != nil {
		t.Fatalf("load legacy retry task: %v", err)
	}
	source := model.TaskRun{
		TaskID: taskEntity.ID, NodeIDSnapshot: taskEntity.NodeID,
		TriggerType: "manual", Status: model.TaskRunStatusFailed,
		ChainRunID: "legacy-retry-chain",
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("create legacy retry source: %v", err)
	}
	payload, err := json.Marshal(retryTaskRunEffect{
		TaskID: storedTask.ID, ChainRunID: source.ChainRunID,
		PredecessorRunID: source.ID,
	})
	if err != nil {
		t.Fatalf("encode legacy retry effect: %v", err)
	}
	effect := model.TaskRunEffect{
		TaskRunID: source.ID, EffectKey: "retry", EffectType: model.TaskRunEffectTypeRetry,
		Payload: string(payload), Status: model.TaskRunEffectStatusPending,
		NextAttemptAt: &retryDue,
	}
	if err := db.Create(&effect).Error; err != nil {
		t.Fatalf("create legacy retry effect: %v", err)
	}
	cursorMode, modeErr := model.ParseTaskRunEffectCronCursorMode(effect.Payload)
	if modeErr != nil {
		t.Fatalf("parse legacy retry effect mode: %v", modeErr)
	}
	if cursorMode == model.TaskRunCronCursorModeRegularV1 {
		t.Fatal("legacy retry effect unexpectedly carries current cursor provenance")
	}

	// The old Task.NextRunAt is only a retry deadline. While it is pending,
	// reconciliation must not publish that value as a regular live phase.
	if err := manager.SyncSchedule(storedTask); err != nil {
		t.Fatalf("reconcile legacy retry reservation: %v", err)
	}
	if manager.scheduler.HasTask(taskEntity.ID) {
		t.Fatal("legacy retry reservation published a live schedule before terminalization")
	}

	if err := manager.drainReadyTaskRunEffects(context.Background()); err != nil {
		t.Fatalf("drain legacy retry effect: %v", err)
	}
	var retryRun model.TaskRun
	if err := db.Where("task_id = ? AND trigger_type = ?", taskEntity.ID, "retry").
		Order("id ASC").First(&retryRun).Error; err != nil {
		t.Fatalf("load legacy retry run: %v", err)
	}
	manager.taskWG.Wait()
	retryRun = waitTaskRunTerminal(t, db, retryRun.ID)
	if retryRun.Status != model.TaskRunStatusSuccess {
		t.Fatalf("legacy retry run status=%q error=%q, want success",
			retryRun.Status, retryRun.LastError)
	}

	var reanchored model.Task
	if err := db.First(&reanchored, taskEntity.ID).Error; err != nil {
		t.Fatalf("load reanchored legacy task: %v", err)
	}
	if reanchored.Status != string(StatusSuccess) || reanchored.NextRunAt == nil ||
		!reanchored.NextRunAt.After(time.Now().UTC()) {
		t.Fatalf("legacy retry did not publish a future regular cursor: status=%q next=%v",
			reanchored.Status, reanchored.NextRunAt)
	}
	regularNext := reanchored.NextRunAt.UTC()
	if manager.scheduler == nil || !manager.scheduler.HasTask(taskEntity.ID) {
		t.Fatal("legacy retry terminalization did not reanchor the live schedule")
	}
	if err := manager.SyncSchedule(reanchored); err != nil {
		t.Fatalf("repeat reanchor reconciliation: %v", err)
	}
	var stable model.Task
	if err := db.First(&stable, taskEntity.ID).Error; err != nil {
		t.Fatalf("reload stable legacy cursor: %v", err)
	}
	if stable.NextRunAt == nil || !stable.NextRunAt.Equal(regularNext) {
		t.Fatalf("legacy reanchor reconciliation changed cursor=%v, want %v",
			stable.NextRunAt, regularNext)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := manager.Shutdown(shutdownCtx); err != nil {
		cancel()
		t.Fatalf("shutdown legacy retry manager: %v", err)
	}
	cancel()
	restarted := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil,
		taskscheduler.NewCronScheduler(), nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, restarted)
	if err := restarted.LoadSchedules(context.Background()); err != nil {
		t.Fatalf("restart legacy retry schedule: %v", err)
	}
	var afterRestart model.Task
	if err := db.First(&afterRestart, taskEntity.ID).Error; err != nil {
		t.Fatalf("load legacy task after restart: %v", err)
	}
	if afterRestart.NextRunAt == nil || !afterRestart.NextRunAt.Equal(regularNext) {
		t.Fatalf("restart changed legacy regular cursor=%v, want %v",
			afterRestart.NextRunAt, regularNext)
	}
	var oldOccurrenceCount int64
	if err := db.Model(&model.TaskCronOccurrence{}).
		Where("task_id = ? AND scheduled_at = ?", taskEntity.ID, retryDue).
		Count(&oldOccurrenceCount).Error; err != nil {
		t.Fatalf("count legacy retry-deadline occurrence: %v", err)
	}
	if oldOccurrenceCount != 0 {
		t.Fatalf("legacy retry deadline became cron occurrence %d time(s)", oldOccurrenceCount)
	}

	if err := restarted.TriggerFromScheduler(taskEntity.ID, regularNext); err != nil {
		t.Fatalf("trigger reanchored legacy callback: %v", err)
	}
	var occurrence model.TaskCronOccurrence
	if err := db.Where("task_id = ? AND scheduled_at = ?", taskEntity.ID, regularNext).
		First(&occurrence).Error; err != nil {
		t.Fatalf("load reanchored regular occurrence: %v", err)
	}
	restarted.taskWG.Wait()
	if occurrence.State != model.TaskCronOccurrenceStateDispatched || occurrence.TaskRunID == nil {
		t.Fatalf("reanchored occurrence state=%q run_id=%v, want dispatched with run",
			occurrence.State, occurrence.TaskRunID)
	}
	regularRun := waitTaskRunTerminal(t, db, *occurrence.TaskRunID)
	if regularRun.Status != model.TaskRunStatusSuccess {
		t.Fatalf("reanchored regular run status=%q error=%q, want success",
			regularRun.Status, regularRun.LastError)
	}
	var final model.Task
	if err := db.First(&final, taskEntity.ID).Error; err != nil {
		t.Fatalf("load final legacy task: %v", err)
	}
	expectedFollowing := cronutil.NextAfter(cronSpec, regularNext)
	if expectedFollowing == nil || final.NextRunAt == nil || !final.NextRunAt.Equal(*expectedFollowing) {
		t.Fatalf("legacy regular callback cursor=%v, want %v", final.NextRunAt, expectedFollowing)
	}
}
