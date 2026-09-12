package task

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"xirang/backend/internal/model"
	taskexec "xirang/backend/internal/task/executor"

	"gorm.io/gorm"
)

type sameTaskLongRunningCronExecutor struct {
	calls           atomic.Int32
	active          atomic.Int32
	maxActive       atomic.Int32
	firstStarted    chan struct{}
	releaseFirst    chan struct{}
	firstStartedMux sync.Once
}

func newSameTaskLongRunningCronExecutor() *sameTaskLongRunningCronExecutor {
	return &sameTaskLongRunningCronExecutor{
		firstStarted: make(chan struct{}),
		releaseFirst: make(chan struct{}),
	}
}

func (e *sameTaskLongRunningCronExecutor) Run(ctx context.Context, _ model.Task, _ taskexec.LogFunc, _ taskexec.ProgressFunc) (int, error) {
	call := e.calls.Add(1)
	active := e.active.Add(1)
	defer e.active.Add(-1)
	for {
		previous := e.maxActive.Load()
		if active <= previous || e.maxActive.CompareAndSwap(previous, active) {
			break
		}
	}
	if call == 1 {
		e.firstStartedMux.Do(func() { close(e.firstStarted) })
		select {
		case <-e.releaseFirst:
		case <-ctx.Done():
			return -1, ctx.Err()
		}
	}
	return 0, nil
}

func (e *sameTaskLongRunningCronExecutor) Calls() int {
	return int(e.calls.Load())
}

func (e *sameTaskLongRunningCronExecutor) MaxActive() int {
	return int(e.maxActive.Load())
}

func seedQuotaPolicyTask(t *testing.T, db *gorm.DB, policyID uint) model.Task {
	t.Helper()
	source := t.TempDir()
	target := t.TempDir()
	node := model.Node{
		Name:      fmt.Sprintf("node-quota-%s", filepath.Base(source)),
		Host:      "",
		Port:      22,
		Username:  "root",
		AuthType:  "key",
		BackupDir: filepath.Join(target, "backup"),
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("create quota node: %v", err)
	}
	taskEntity := model.Task{
		Name:         fmt.Sprintf("task-quota-%s", filepath.Base(source)),
		NodeID:       node.ID,
		PolicyID:     &policyID,
		ExecutorType: "rsync",
		CronSpec:     "@every 1h",
		Status:       string(StatusPending),
		Enabled:      true,
		RsyncSource:  source + "/",
		RsyncTarget:  target,
	}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatalf("create quota task: %v", err)
	}
	if err := db.Preload("Node").Preload("Policy").First(&taskEntity, taskEntity.ID).Error; err != nil {
		t.Fatalf("reload quota task: %v", err)
	}
	return taskEntity
}

func seedQuotaPolicy(t *testing.T, db *gorm.DB, maxConcurrent int) model.Policy {
	t.Helper()
	policy := model.Policy{Name: "quota-policy", MaxConcurrent: maxConcurrent}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatalf("create quota policy: %v", err)
	}
	if err := db.Model(&model.Policy{}).Where("id = ?", policy.ID).
		Updates(map[string]interface{}{"enabled": true, "max_concurrent": maxConcurrent}).Error; err != nil {
		t.Fatalf("persist quota policy limit: %v", err)
	}
	if err := db.First(&policy, policy.ID).Error; err != nil {
		t.Fatalf("reload quota policy: %v", err)
	}
	if !policy.Enabled {
		t.Fatalf("quota policy remained disabled after explicit enable")
	}
	return policy
}

func seedTargetClaimTask(t *testing.T, db *gorm.DB, nodeID uint, policyID *uint, name, target string) model.Task {
	t.Helper()
	source := t.TempDir()
	taskEntity := model.Task{
		Name:         name,
		NodeID:       nodeID,
		PolicyID:     policyID,
		ExecutorType: "rsync",
		Status:       string(StatusPending),
		RsyncSource:  source + "/",
		RsyncTarget:  target,
	}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatalf("create target claim task: %v", err)
	}
	if err := db.Preload("Node").Preload("Policy").First(&taskEntity, taskEntity.ID).Error; err != nil {
		t.Fatalf("reload target claim task: %v", err)
	}
	return taskEntity
}

func runManualTargetOwnershipAdmission(t *testing.T, policyFirst bool) {
	t.Helper()
	db := openConcurrentManagerTestDB(t)
	node := model.Node{
		Name:     "target-ownership-node",
		Host:     "",
		Port:     22,
		Username: "root",
		AuthType: "key",
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("create target ownership node: %v", err)
	}
	var policyID *uint
	if policyFirst {
		policy := seedQuotaPolicy(t, db, 0)
		policyID = &policy.ID
	}
	target := filepath.Join(t.TempDir(), "shared-target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("create target ownership destination: %v", err)
	}
	first := seedTargetClaimTask(t, db, node.ID, policyID, "target-owner-first", target)
	executor := &successExecutor{}
	manager := NewManager(db, stubExecutorFactory{executor: executor}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)
	runID, err := manager.TriggerManual(first.ID)
	if err != nil || runID == 0 {
		t.Fatalf("first target ownership trigger run=%d err=%v", runID, err)
	}
	firstRun := waitTaskRunTerminal(t, db, runID)
	if firstRun.Status != model.TaskRunStatusSuccess {
		t.Fatalf("first target ownership run status=%q error=%q", firstRun.Status, firstRun.LastError)
	}
	second := seedTargetClaimTask(t, db, node.ID, nil, "target-owner-second", target)
	if duplicateRunID, duplicateErr := manager.TriggerManual(second.ID); duplicateErr == nil || duplicateRunID != 0 ||
		!strings.Contains(duplicateErr.Error(), "任务目标路径冲突") {
		t.Fatalf("duplicate target ownership trigger run=%d err=%v, want conflict and no run", duplicateRunID, duplicateErr)
	}
	if got := executor.Calls(); got != 1 {
		t.Fatalf("target ownership executor calls=%d, want 1", got)
	}
}

func TestManualTargetOwnershipAdmissionRejectsDuplicateRuntimeClaims(t *testing.T) {
	t.Run("manual_vs_manual", func(t *testing.T) {
		runManualTargetOwnershipAdmission(t, false)
	})
	t.Run("manual_vs_policy", func(t *testing.T) {
		runManualTargetOwnershipAdmission(t, true)
	})
}

func TestPolicyMaxConcurrentReservationsCountPending(t *testing.T) {
	db := openConcurrentManagerTestDB(t)
	policy := seedQuotaPolicy(t, db, 1)
	firstTask := seedQuotaPolicyTask(t, db, policy.ID)
	secondTask := seedQuotaPolicyTask(t, db, policy.ID)
	manager := &Manager{db: db, stateMachine: NewStateMachine(), executionOwnerID: "quota-reservation-test"}

	first, err := manager.reserveTaskRun(context.Background(), firstTask.NodeID, model.TaskRun{
		TaskID: firstTask.ID, TriggerType: "manual", Status: model.TaskRunStatusPending,
	})
	if err != nil {
		t.Fatalf("first reservation: %v", err)
	}
	if first.Status != model.TaskRunStatusPending {
		t.Fatalf("first reservation status=%q, want pending", first.Status)
	}
	if _, err := manager.reserveTaskRun(context.Background(), secondTask.NodeID, model.TaskRun{
		TaskID: secondTask.ID, TriggerType: "manual", Status: model.TaskRunStatusPending,
	}); !errors.Is(err, ErrPolicyConcurrencyLimit) {
		t.Fatalf("second reservation error=%v, want ErrPolicyConcurrencyLimit", err)
	}
	var activeCount int64
	if err := db.Model(&model.TaskRun{}).Where("status IN ?", []string{model.TaskRunStatusPending, model.TaskRunStatusRunning}).Count(&activeCount).Error; err != nil {
		t.Fatalf("count pending quota reservations: %v", err)
	}
	if activeCount != 1 {
		t.Fatalf("active reservation count=%d, want 1", activeCount)
	}
	if err := db.Model(&model.TaskRun{}).Where("id = ?", first.ID).Updates(map[string]interface{}{
		"status": model.TaskRunStatusCanceled, "finished_at": time.Now().UTC(),
	}).Error; err != nil {
		t.Fatalf("release first reservation: %v", err)
	}
	if _, err := manager.reserveTaskRun(context.Background(), secondTask.NodeID, model.TaskRun{
		TaskID: secondTask.ID, TriggerType: "manual", Status: model.TaskRunStatusPending,
	}); err != nil {
		t.Fatalf("reservation after slot release: %v", err)
	}
}
func TestCronQuotaRefusalKeepsDurableOccurrenceUntilSlotReleases(t *testing.T) {
	db := openConcurrentManagerTestDB(t)
	policy := seedQuotaPolicy(t, db, 1)
	firstTask := seedQuotaPolicyTask(t, db, policy.ID)
	secondTask := seedQuotaPolicyTask(t, db, policy.ID)
	thirdTask := seedQuotaPolicyTask(t, db, policy.ID)
	exec := newBlockingExecutor()
	manager := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)

	occurrence := time.Date(2026, 9, 12, 2, 0, 0, 0, time.UTC)
	if err := manager.TriggerFromScheduler(firstTask.ID, occurrence); err != nil {
		t.Fatalf("trigger first cron task: %v", err)
	}
	select {
	case <-exec.started:
	case <-time.After(3 * time.Second):
		var currentTask model.Task
		_ = db.First(&currentTask, firstTask.ID).Error
		var currentOccurrence model.TaskCronOccurrence
		_ = db.Where("task_id = ? AND scheduled_at = ?", firstTask.ID, occurrence).First(&currentOccurrence).Error
		var currentRun model.TaskRun
		_ = db.Where("task_id = ? AND trigger_type = ?", firstTask.ID, "cron").First(&currentRun).Error
		t.Fatalf("first cron task did not reach executor: task=%+v occurrence=%+v run=%+v executor_calls=%d", currentTask, currentOccurrence, currentRun, exec.Calls())
	}
	if err := manager.TriggerFromScheduler(secondTask.ID, occurrence); err != nil {
		t.Fatalf("queue second cron task: %v", err)
	}
	if err := manager.TriggerFromScheduler(thirdTask.ID, occurrence); err != nil {
		t.Fatalf("queue third cron task: %v", err)
	}

	var queuedCount int64
	if err := db.Model(&model.TaskCronOccurrence{}).
		Where("task_id IN ? AND state = ?", []uint{secondTask.ID, thirdTask.ID}, model.TaskCronOccurrenceStateQueued).
		Count(&queuedCount).Error; err != nil {
		t.Fatalf("count quota-blocked cron occurrences: %v", err)
	}
	if queuedCount != 2 {
		t.Fatalf("quota-blocked cron occurrences=%d, want 2", queuedCount)
	}
	var runCount int64
	if err := db.Model(&model.TaskRun{}).Where("trigger_type = ?", "cron").Count(&runCount).Error; err != nil {
		t.Fatalf("count cron reservations before slot release: %v", err)
	}
	if runCount != 1 {
		t.Fatalf("cron reservations before slot release=%d, want 1", runCount)
	}

	close(exec.release)
	firstRun := waitCronTaskRunByTask(t, db, firstTask.ID)
	if firstRun.Status != model.TaskRunStatusSuccess {
		t.Fatalf("first cron task status=%q error=%q, want success", firstRun.Status, firstRun.LastError)
	}

	for attempt := range 20 {
		if err := manager.drainCronOccurrences(context.Background()); err != nil {
			t.Fatalf("drain durable cron occurrences attempt %d: %v", attempt, err)
		}
		if err := db.Model(&model.TaskRun{}).Where("trigger_type = ? AND status IN ?",
			"cron", []string{model.TaskRunStatusPending, model.TaskRunStatusRunning, model.TaskRunStatusSuccess}).Count(&runCount).Error; err != nil {
			t.Fatalf("count cron deliveries attempt %d: %v", attempt, err)
		}
		if runCount == 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if runCount != 3 {
		t.Fatalf("cron reservations after slot release=%d, want 3", runCount)
	}
	for _, taskID := range []uint{secondTask.ID, thirdTask.ID} {
		run := waitCronTaskRunByTask(t, db, taskID)
		if run.Status != model.TaskRunStatusSuccess {
			t.Fatalf("queued cron task %d status=%q error=%q, want success", taskID, run.Status, run.LastError)
		}
	}
	var dispatchedCount int64
	if err := db.Model(&model.TaskCronOccurrence{}).
		Where("task_id IN ? AND state = ?", []uint{firstTask.ID, secondTask.ID, thirdTask.ID}, model.TaskCronOccurrenceStateDispatched).
		Count(&dispatchedCount).Error; err != nil {
		t.Fatalf("count dispatched cron occurrences: %v", err)
	}
	if dispatchedCount != 3 {
		t.Fatalf("dispatched cron occurrences=%d, want 3", dispatchedCount)
	}
}

func TestCronOccurrenceQueuesSameTaskLongRunAndDrainsExactlyOnce(t *testing.T) {
	db := openConcurrentManagerTestDB(t)
	policy := seedQuotaPolicy(t, db, 1)
	taskEntity := seedQuotaPolicyTask(t, db, policy.ID)
	exec := newSameTaskLongRunningCronExecutor()
	manager := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)

	firstOccurrence := time.Date(2026, 9, 12, 2, 0, 0, 0, time.UTC)
	if err := manager.TriggerFromScheduler(taskEntity.ID, firstOccurrence); err != nil {
		t.Fatalf("trigger first same-task cron occurrence: %v", err)
	}
	select {
	case <-exec.firstStarted:
	case <-time.After(3 * time.Second):
		t.Fatalf("first same-task cron occurrence did not reach the blocking executor; calls=%d", exec.Calls())
	}

	laterOccurrences := []time.Time{
		firstOccurrence.Add(time.Hour),
		firstOccurrence.Add(2 * time.Hour),
	}
	for _, occurrence := range laterOccurrences {
		if err := manager.TriggerFromScheduler(taskEntity.ID, occurrence); err != nil {
			t.Fatalf("queue same-task cron occurrence at %s: %v", occurrence, err)
		}
	}

	var occurrences []model.TaskCronOccurrence
	if err := db.Where("task_id = ?", taskEntity.ID).Order("scheduled_at ASC").Find(&occurrences).Error; err != nil {
		t.Fatalf("load queued same-task cron occurrences: %v", err)
	}
	if len(occurrences) != 3 {
		t.Fatalf("same-task cron occurrences before release=%d, want 3", len(occurrences))
	}
	for i, occurrence := range occurrences {
		if i == 0 {
			if occurrence.State != model.TaskCronOccurrenceStateDispatched || occurrence.TaskRunID == nil {
				t.Fatalf("first occurrence before release state=%q run_id=%v, want dispatched with run", occurrence.State, occurrence.TaskRunID)
			}
			continue
		}
		if occurrence.State != model.TaskCronOccurrenceStateQueued || occurrence.TaskRunID != nil {
			t.Fatalf("later occurrence %d before release state=%q run_id=%v, want queued without run", i, occurrence.State, occurrence.TaskRunID)
		}
	}
	var runCount int64
	if err := db.Model(&model.TaskRun{}).
		Where("task_id = ? AND trigger_type = ?", taskEntity.ID, "cron").
		Count(&runCount).Error; err != nil {
		t.Fatalf("count same-task cron runs before release: %v", err)
	}
	if runCount != 1 {
		t.Fatalf("same-task cron runs before release=%d, want 1", runCount)
	}

	close(exec.releaseFirst)
	firstRun := waitCronTaskRunByTask(t, db, taskEntity.ID)
	if firstRun.Status != model.TaskRunStatusSuccess {
		t.Fatalf("first same-task cron run status=%q error=%q, want success", firstRun.Status, firstRun.LastError)
	}
	manager.taskWG.Wait()

	for attempt := range 100 {
		if err := manager.drainCronOccurrences(context.Background()); err != nil {
			t.Fatalf("drain same-task cron occurrences attempt %d: %v", attempt, err)
		}
		manager.taskWG.Wait()
		if err := db.Model(&model.TaskRun{}).
			Where("task_id = ? AND trigger_type = ? AND status = ?",
				taskEntity.ID, "cron", model.TaskRunStatusSuccess).
			Count(&runCount).Error; err != nil {
			t.Fatalf("count completed same-task cron runs attempt %d: %v", attempt, err)
		}
		if runCount == 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if runCount != 3 {
		t.Fatalf("completed same-task cron runs=%d, want 3", runCount)
	}
	if exec.Calls() != 3 {
		t.Fatalf("same-task cron executor calls=%d, want 3", exec.Calls())
	}
	if exec.MaxActive() != 1 {
		t.Fatalf("same-task cron max concurrent executor entries=%d, want 1", exec.MaxActive())
	}

	if err := db.Where("task_id = ?", taskEntity.ID).Order("scheduled_at ASC").Find(&occurrences).Error; err != nil {
		t.Fatalf("reload same-task cron occurrences: %v", err)
	}
	if len(occurrences) != 3 {
		t.Fatalf("same-task cron occurrences after release=%d, want 3", len(occurrences))
	}
	seenRunIDs := make(map[uint]struct{}, len(occurrences))
	for i, occurrence := range occurrences {
		if occurrence.State != model.TaskCronOccurrenceStateDispatched || occurrence.TaskRunID == nil {
			t.Fatalf("occurrence %d after release state=%q run_id=%v, want dispatched with run", i, occurrence.State, occurrence.TaskRunID)
		}
		if _, duplicate := seenRunIDs[*occurrence.TaskRunID]; duplicate {
			t.Fatalf("same-task cron occurrence %d reused TaskRun %d", i, *occurrence.TaskRunID)
		}
		seenRunIDs[*occurrence.TaskRunID] = struct{}{}
	}
}

func waitCronTaskRunByTask(t *testing.T, db *gorm.DB, taskID uint) model.TaskRun {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var run model.TaskRun
		if err := db.Where("task_id = ? AND trigger_type = ?", taskID, "cron").Order("id ASC").First(&run).Error; err == nil {
			if model.IsTerminalTaskRunStatus(run.Status) {
				return run
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	var run model.TaskRun
	if err := db.Where("task_id = ? AND trigger_type = ?", taskID, "cron").Order("id ASC").First(&run).Error; err != nil {
		t.Fatalf("load cron TaskRun for task %d: %v", taskID, err)
	}
	t.Fatalf("cron TaskRun for task %d remained %s", taskID, run.Status)
	return run
}

func TestPolicyMaxConcurrentAdmissionRechecksRunningAcrossTasks(t *testing.T) {
	db := openConcurrentManagerTestDB(t)
	policy := seedQuotaPolicy(t, db, 1)
	firstTask := seedQuotaPolicyTask(t, db, policy.ID)
	secondTask := seedQuotaPolicyTask(t, db, policy.ID)
	firstRunID := createTestTaskRun(t, db, firstTask.ID, "manual")
	secondRunID := createTestTaskRun(t, db, secondTask.ID, "manual")
	manager := &Manager{db: db, stateMachine: NewStateMachine(), executionOwnerID: "quota-admission-test"}

	var loadedFirst, loadedSecond model.Task
	if err := db.First(&loadedFirst, firstTask.ID).Error; err != nil {
		t.Fatalf("load first quota task: %v", err)
	}
	if err := db.First(&loadedSecond, secondTask.ID).Error; err != nil {
		t.Fatalf("load second quota task: %v", err)
	}
	startedAt := time.Now().UTC()
	if err := manager.enterTaskExecutionForReason(context.Background(), firstRunID, loadedFirst.NodeID, startedAt, &loadedFirst, "manual"); err != nil {
		t.Fatalf("first execution admission: %v", err)
	}
	var firstRunning model.TaskRun
	if err := db.First(&firstRunning, firstRunID).Error; err != nil {
		t.Fatalf("load first running quota task: %v", err)
	}
	if firstRunning.Status != model.TaskRunStatusRunning {
		t.Fatalf("first run status=%q, want running", firstRunning.Status)
	}
	if err := manager.enterTaskExecutionForReason(context.Background(), secondRunID, loadedSecond.NodeID, startedAt, &loadedSecond, "manual"); !errors.Is(err, ErrPolicyConcurrencyLimit) {
		t.Fatalf("second execution admission error=%v, want ErrPolicyConcurrencyLimit", err)
	}
	var secondPending model.TaskRun
	if err := db.First(&secondPending, secondRunID).Error; err != nil {
		t.Fatalf("load second pending quota task: %v", err)
	}
	if secondPending.Status != model.TaskRunStatusPending {
		t.Fatalf("second run status=%q, want pending after busy refusal", secondPending.Status)
	}
	if err := db.Model(&model.TaskRun{}).Where("id = ?", firstRunID).Update("status", model.TaskRunStatusSuccess).Error; err != nil {
		t.Fatalf("release first running slot: %v", err)
	}
	if err := db.Model(&model.Task{}).Where("id = ?", firstTask.ID).Update("status", string(StatusSuccess)).Error; err != nil {
		t.Fatalf("release first task aggregate: %v", err)
	}
	if err := manager.enterTaskExecutionForReason(context.Background(), secondRunID, loadedSecond.NodeID, startedAt.Add(time.Second), &loadedSecond, "manual"); err != nil {
		t.Fatalf("second execution admission after release: %v", err)
	}
}

func TestPolicyDisableCancelsPendingQuotaReservations(t *testing.T) {
	db := openConcurrentManagerTestDB(t)
	policy := seedQuotaPolicy(t, db, 1)
	taskEntity := seedQuotaPolicyTask(t, db, policy.ID)
	manager := &Manager{db: db, stateMachine: NewStateMachine(), executionOwnerID: "quota-disable-test"}
	run, err := manager.reserveTaskRun(context.Background(), taskEntity.NodeID, model.TaskRun{
		TaskID: taskEntity.ID, TriggerType: "auto", Status: model.TaskRunStatusPending,
	})
	if err != nil {
		t.Fatalf("reserve pending automation run: %v", err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		_, err := manager.DisablePolicyTx(context.Background(), tx, policy.ID)
		return err
	}); err != nil {
		t.Fatalf("disable quota policy: %v", err)
	}
	var canceled model.TaskRun
	if err := db.First(&canceled, run.ID).Error; err != nil {
		t.Fatalf("load canceled quota run: %v", err)
	}
	if canceled.Status != model.TaskRunStatusCanceled {
		t.Fatalf("disabled pending run status=%q, want canceled", canceled.Status)
	}
}
func TestPolicyMaxConcurrentCapsActualExecutorEntries(t *testing.T) {
	for _, maxConcurrent := range []int{1, 2} {
		t.Run(fmt.Sprintf("max_%d", maxConcurrent), func(t *testing.T) {
			db := openConcurrentManagerTestDB(t)
			policy := seedQuotaPolicy(t, db, maxConcurrent)
			tasks := make([]model.Task, maxConcurrent+1)
			for i := range tasks {
				tasks[i] = seedQuotaPolicyTask(t, db, policy.ID)
			}
			blocking := newBlockingExecutor()
			manager := NewManager(db, stubExecutorFactory{executor: blocking}, nil, nil, nil, nil, 8, 90)
			shutdownManagerOnCleanup(t, manager)

			for i := range maxConcurrent {
				if runID, err := manager.TriggerManual(tasks[i].ID); err != nil || runID == 0 {
					t.Fatalf("trigger admitted task %d run=%d err=%v", i, runID, err)
				}
			}
			waitForBlockingExecutorCalls(t, blocking, maxConcurrent)

			if runID, err := manager.TriggerManual(tasks[maxConcurrent].ID); !errors.Is(err, ErrPolicyConcurrencyLimit) || runID != 0 {
				t.Fatalf("over-quota trigger run=%d err=%v, want zero and ErrPolicyConcurrencyLimit", runID, err)
			}
			if got := blocking.Calls(); got != maxConcurrent {
				t.Fatalf("executor calls=%d after over-quota trigger, want %d", got, maxConcurrent)
			}

			close(blocking.release)
			manager.taskWG.Wait()
		})
	}
}
func TestPolicyMaxConcurrentCapsActualExecutorEntriesPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	for _, maxConcurrent := range []int{1, 2} {
		t.Run(fmt.Sprintf("max_%d", maxConcurrent), func(t *testing.T) {
			db := openTaskTerminalPostgresDB(t, dsn)
			if err := db.AutoMigrate(
				&model.SSHKey{}, &model.Node{}, &model.Policy{}, &model.Task{}, &model.TaskRun{},
				&model.TaskRunEffect{}, &model.RestoreDrillEvidence{}, &model.CredentialAuditEvent{},
				&model.TaskLog{}, &model.Alert{}, &model.Integration{}, &model.TaskTrafficSample{},
				&model.AlertDelivery{},
			); err != nil {
				t.Fatalf("migrate PostgreSQL quota fixtures: %v", err)
			}
			runPolicyMaxConcurrentCapsActualExecutorEntriesAcrossManagers(t, db, maxConcurrent)
		})
	}
}

func runPolicyMaxConcurrentCapsActualExecutorEntriesAcrossManagers(t *testing.T, db *gorm.DB, maxConcurrent int) {
	t.Helper()
	policy := seedQuotaPolicy(t, db, maxConcurrent)
	tasks := make([]model.Task, maxConcurrent+1)
	for i := range tasks {
		tasks[i] = seedQuotaPolicyTask(t, db, policy.ID)
	}
	blocking := newBlockingExecutor()
	managerA := NewManager(db, stubExecutorFactory{executor: blocking}, nil, nil, nil, nil, 8, 90)
	managerB := NewManager(db, stubExecutorFactory{executor: blocking}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, managerA)
	shutdownManagerOnCleanup(t, managerB)

	for i := range maxConcurrent {
		manager := managerA
		if i%2 == 1 {
			manager = managerB
		}
		if runID, err := manager.TriggerManual(tasks[i].ID); err != nil || runID == 0 {
			t.Fatalf("trigger admitted PostgreSQL task %d run=%d err=%v", i, runID, err)
		}
	}
	waitForBlockingExecutorCalls(t, blocking, maxConcurrent)

	if runID, err := managerB.TriggerManual(tasks[maxConcurrent].ID); !errors.Is(err, ErrPolicyConcurrencyLimit) || runID != 0 {
		t.Fatalf("PostgreSQL over-quota trigger run=%d err=%v, want zero and ErrPolicyConcurrencyLimit", runID, err)
	}
	if got := blocking.Calls(); got != maxConcurrent {
		t.Fatalf("PostgreSQL executor calls=%d after over-quota trigger, want %d", got, maxConcurrent)
	}

	close(blocking.release)
	managerA.taskWG.Wait()
	managerB.taskWG.Wait()
}

func waitForBlockingExecutorCalls(t *testing.T, executor *blockingExecutor, want int) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if got := executor.Calls(); got >= want {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("executor calls=%d, want at least %d", executor.Calls(), want)
		case <-ticker.C:
		}
	}
}
