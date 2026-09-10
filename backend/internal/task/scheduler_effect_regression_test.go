package task

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
	"xirang/backend/internal/alerting"
	"xirang/backend/internal/model"
)

func waitTaskRunTerminal(t *testing.T, db *gorm.DB, runID uint) model.TaskRun {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var run model.TaskRun
		if err := db.First(&run, runID).Error; err == nil && model.IsTerminalTaskRunStatus(run.Status) {
			return run
		}
		time.Sleep(5 * time.Millisecond)
	}
	var run model.TaskRun
	if err := db.First(&run, runID).Error; err != nil {
		t.Fatalf("load task run %d: %v", runID, err)
	}
	t.Fatalf("task run %d remained %s", runID, run.Status)
	return run
}

func createDownstreamEffectFixture(t *testing.T, db *gorm.DB, enabled bool, archived bool) (model.Task, model.Task, model.TaskRun, string) {
	t.Helper()
	upstream := seedTaskForManagerTest(t, db)
	now := time.Now().UTC()
	if err := db.Model(&model.Task{}).Where("id = ?", upstream.ID).Updates(map[string]interface{}{
		"status": string(StatusSuccess), "enabled": true,
	}).Error; err != nil {
		t.Fatalf("update upstream: %v", err)
	}
	downstream := model.Task{
		Name: "chain-downstream", NodeID: upstream.NodeID, DependsOnTaskID: &upstream.ID,
		ExecutorType: "local", Status: string(StatusPending), Enabled: enabled,
		RsyncSource: "/tmp/src", RsyncTarget: "/tmp/dst",
	}
	if archived {
		downstream.ArchivedAt = &now
		downstream.Enabled = false
	}
	if err := db.Create(&downstream).Error; err != nil {
		t.Fatalf("create downstream: %v", err)
	}
	if !enabled || archived {
		updates := map[string]interface{}{"enabled": enabled}
		if archived {
			updates["enabled"] = false
			updates["archived_at"] = now
		}
		if err := db.Model(&model.Task{}).Where("id = ?", downstream.ID).Updates(updates).Error; err != nil {
			t.Fatalf("persist downstream state: %v", err)
		}
	}
	upstreamRun := model.TaskRun{
		TaskID: upstream.ID, NodeIDSnapshot: upstream.NodeID, TriggerType: "manual",
		Status: model.TaskRunStatusSuccess, ChainRunID: "chain-fixture",
	}
	if err := db.Create(&upstreamRun).Error; err != nil {
		t.Fatalf("create upstream run: %v", err)
	}
	payload, err := json.Marshal(downstreamTaskRunEffect{
		TaskID: downstream.ID, UpstreamRunID: upstreamRun.ID, ChainRunID: upstreamRun.ChainRunID,
	})
	if err != nil {
		t.Fatalf("encode effect: %v", err)
	}
	return upstream, downstream, upstreamRun, string(payload)
}

func runDistinctDownstreamReservationRace(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.AutoMigrate(&model.TaskTrafficSample{}, &model.TaskLog{}, &model.Alert{}); err != nil {
		t.Fatalf("migrate downstream race support tables: %v", err)
	}
	exec := newBlockingExecutor()
	managerOne := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, nil, 8, 90)
	managerTwo := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, managerOne)
	shutdownManagerOnCleanup(t, managerTwo)

	upstream, downstream, firstSource, firstPayload := createDownstreamEffectFixture(t, db, true, false)
	secondSource := model.TaskRun{
		TaskID: upstream.ID, NodeIDSnapshot: upstream.NodeID, TriggerType: "manual",
		Status: model.TaskRunStatusSuccess, ChainRunID: "chain-fixture-second",
	}
	if err := db.Create(&secondSource).Error; err != nil {
		t.Fatalf("create second upstream run: %v", err)
	}
	secondPayload, err := json.Marshal(downstreamTaskRunEffect{
		TaskID: downstream.ID, UpstreamRunID: secondSource.ID, ChainRunID: secondSource.ChainRunID,
	})
	if err != nil {
		t.Fatalf("encode second downstream effect: %v", err)
	}
	now := time.Now().UTC()
	firstEffect := model.TaskRunEffect{
		TaskRunID: firstSource.ID, EffectKey: "downstream:first", EffectType: model.TaskRunEffectTypeDownstream,
		Payload: firstPayload, Status: model.TaskRunEffectStatusPending, NextAttemptAt: &now,
	}
	secondEffect := model.TaskRunEffect{
		TaskRunID: secondSource.ID, EffectKey: "downstream:second", EffectType: model.TaskRunEffectTypeDownstream,
		Payload: string(secondPayload), Status: model.TaskRunEffectStatusPending, NextAttemptAt: &now,
	}
	if err := db.Create(&firstEffect).Error; err != nil {
		t.Fatalf("create first downstream effect: %v", err)
	}
	if err := db.Create(&secondEffect).Error; err != nil {
		t.Fatalf("create second downstream effect: %v", err)
	}

	firstAdmitEntered := make(chan struct{})
	firstAdmitRelease := make(chan struct{})
	managerOne.SetNodeWriteAdmission(&nodeWriteAdmissionFake{
		admitEntered: firstAdmitEntered,
		admitRelease: firstAdmitRelease,
	})
	managerTwoAdmit := &nodeWriteAdmissionFake{}
	managerTwo.SetNodeWriteAdmission(managerTwoAdmit)

	firstErrCh := make(chan error, 1)
	go func() {
		firstErrCh <- managerOne.drainTaskRunEffects(context.Background(), firstSource.ID)
	}()
	select {
	case <-firstAdmitEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("first manager did not reach durable reservation")
	}

	secondErrCh := make(chan error, 1)
	go func() {
		secondErrCh <- managerTwo.drainTaskRunEffects(context.Background(), secondSource.ID)
	}()
	close(firstAdmitRelease)

	select {
	case err := <-firstErrCh:
		if err != nil {
			t.Fatalf("first downstream effect: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("first downstream effect did not finish reservation")
	}
	var secondErr error
	select {
	case secondErr = <-secondErrCh:
	case <-time.After(3 * time.Second):
		t.Fatal("second downstream effect did not classify durable busy reservation")
	}
	if secondErr == nil || !errors.Is(secondErr, errTaskChainBusy) {
		t.Fatalf("second downstream effect error=%v, want durable chain busy", secondErr)
	}

	var firstStored, secondStored model.TaskRunEffect
	if err := db.First(&firstStored, firstEffect.ID).Error; err != nil {
		t.Fatalf("reload first downstream effect: %v", err)
	}
	if err := db.First(&secondStored, secondEffect.ID).Error; err != nil {
		t.Fatalf("reload second downstream effect: %v", err)
	}
	if firstStored.Status != model.TaskRunEffectStatusSucceeded {
		t.Fatalf("first downstream effect status=%q, want succeeded", firstStored.Status)
	}
	if secondStored.Status != model.TaskRunEffectStatusFailed ||
		secondStored.Attempts != 1 || secondStored.NextAttemptAt == nil {
		t.Fatalf("second downstream effect=%+v, want retryable failed attempt", secondStored)
	}

	var child model.TaskRun
	if err := db.Where("task_id = ? AND upstream_task_run_id = ?", downstream.ID, firstSource.ID).
		First(&child).Error; err != nil {
		t.Fatalf("load admitted child: %v", err)
	}
	var childCount int64
	if err := db.Model(&model.TaskRun{}).
		Where("task_id = ? AND upstream_task_run_id IN ?", downstream.ID, []uint{firstSource.ID, secondSource.ID}).
		Count(&childCount).Error; err != nil {
		t.Fatalf("count distinct downstream children: %v", err)
	}
	if childCount != 1 {
		t.Fatalf("downstream children=%d, want one admitted child", childCount)
	}
	select {
	case <-exec.started:
	case <-time.After(3 * time.Second):
		t.Fatal("admitted child did not reach executor")
	}
	var runningTask model.Task
	if err := db.First(&runningTask, downstream.ID).Error; err != nil {
		t.Fatalf("reload downstream aggregate: %v", err)
	}
	if ParseStatus(runningTask.Status) != StatusRunning {
		t.Fatalf("downstream aggregate status=%q, want running while admitted child owns it", runningTask.Status)
	}
	close(exec.release)
	child = waitTaskRunTerminal(t, db, child.ID)
	if child.Status != model.TaskRunStatusSuccess {
		t.Fatalf("admitted child status=%q, want success", child.Status)
	}
}

func TestDistinctDownstreamEffectsDurablyReserveOneChildPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	runDistinctDownstreamReservationRace(t, openTaskTerminalPostgresDB(t, dsn))
}

func TestPolicyDriftBetweenReservationAndEntryRejectsExecutor(t *testing.T) {
	db := openConcurrentManagerTestDB(t)
	exec := &successExecutor{}
	manager := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)
	taskEntity := seedTaskForManagerTest(t, db)
	policy := model.Policy{
		Name:         "policy-reservation-entry-drift",
		ExcludeRules: "keep-before",
		PreHook:      "pre-before",
		PostHook:     "post-before",
		AppProfile:   "profile-before",
	}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatalf("create policy: %v", err)
	}
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Updates(map[string]interface{}{
		"policy_id": policy.ID, "executor_type": "local",
	}).Error; err != nil {
		t.Fatalf("bind policy task: %v", err)
	}

	for range cap(manager.semaphore) {
		manager.semaphore <- struct{}{}
	}
	runID, err := manager.TriggerManual(taskEntity.ID)
	if err != nil {
		t.Fatalf("reserve task run: %v", err)
	}
	if err := db.Model(&model.Policy{}).Where("id = ?", policy.ID).UpdateColumn(
		"exclude_rules", "keep-after",
	).Error; err != nil {
		t.Fatalf("mutate policy after reservation: %v", err)
	}
	for range cap(manager.semaphore) {
		<-manager.semaphore
	}

	run := waitTaskRunTerminal(t, db, runID)
	if run.Status != model.TaskRunStatusFailed {
		t.Fatalf("policy-drift TaskRun status=%q, want failed before executor", run.Status)
	}
	if exec.Calls() != 0 {
		t.Fatalf("policy-drift executor calls=%d, want zero", exec.Calls())
	}
}

func TestCronSkipNextConsumedOnlyAtExecutionEntry(t *testing.T) {
	db := openManagerTestDB(t)
	exec := &successExecutor{}
	manager := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)
	taskEntity := seedTaskForManagerTest(t, db)
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Updates(map[string]interface{}{
		"status": string(StatusSuccess), "enabled": true, "cron_spec": "@every 1h", "skip_next": true,
	}).Error; err != nil {
		t.Fatalf("prepare cron task: %v", err)
	}

	manager.runContextFactory = func(parent context.Context, _ time.Duration) (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancel(parent)
		cancel()
		return ctx, func() {}
	}
	firstOccurrence := time.Now().UTC().Add(time.Minute)
	if _, err := manager.triggerCore(taskEntity.ID, "cron", generateChainRunID(), nil, &firstOccurrence); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cron cancellation error=%v, want context canceled", err)
	}
	var afterPreCron model.Task
	if err := db.First(&afterPreCron, taskEntity.ID).Error; err != nil {
		t.Fatalf("reload pre-cron task: %v", err)
	}
	if !afterPreCron.SkipNext {
		t.Fatal("pre-cron trigger consumed skip_next before execution entry")
	}

	manager.runContextFactory = context.WithTimeout
	secondOccurrence := firstOccurrence.Add(time.Hour)
	runID, err := manager.triggerCore(taskEntity.ID, "cron", generateChainRunID(), nil, &secondOccurrence)
	if err != nil {
		t.Fatalf("trigger skipped cron run: %v", err)
	}
	run := waitTaskRunTerminal(t, db, runID)
	// Durable terminalization precedes the runner's process-local owner
	// cleanup. Join the worker before reserving the independent occurrence.
	manager.taskWG.Wait()
	if run.Status != model.TaskRunStatusCanceled {
		t.Fatalf("skipped cron run status=%q, want canceled", run.Status)
	}
	var afterSkip model.Task
	if err := db.First(&afterSkip, taskEntity.ID).Error; err != nil {
		t.Fatalf("reload skipped task: %v", err)
	}
	if afterSkip.SkipNext {
		t.Fatal("execution entry did not consume skip_next")
	}
	if exec.Calls() != 0 {
		t.Fatalf("skipped cron invoked executor %d time(s)", exec.Calls())
	}

	thirdOccurrence := secondOccurrence.Add(time.Hour)
	nextRunID, err := manager.triggerCore(taskEntity.ID, "cron", generateChainRunID(), nil, &thirdOccurrence)
	if err != nil {
		t.Fatalf("trigger next cron run: %v", err)
	}
	nextRun := waitTaskRunTerminal(t, db, nextRunID)
	if nextRun.Status != model.TaskRunStatusSuccess || exec.Calls() != 1 {
		t.Fatalf("next cron run status=%q calls=%d, want success/1", nextRun.Status, exec.Calls())
	}
}

func TestChainTriggerReusesExactExistingChildAtReservation(t *testing.T) {
	db := openManagerTestDB(t)
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)
	_, downstream, upstreamRun, _ := createDownstreamEffectFixture(t, db, true, false)
	existing := model.TaskRun{
		TaskID: downstream.ID, NodeIDSnapshot: downstream.NodeID, TriggerType: "chain",
		Status: model.TaskRunStatusPending, ChainRunID: upstreamRun.ChainRunID,
		UpstreamTaskRunID: &upstreamRun.ID,
	}
	if err := db.Create(&existing).Error; err != nil {
		t.Fatalf("create existing exact child: %v", err)
	}
	runID, err := manager.triggerCore(
		downstream.ID, "chain", upstreamRun.ChainRunID, &upstreamRun.ID, nil,
	)
	if err != nil {
		t.Fatalf("reuse exact existing child: %v", err)
	}
	if runID != existing.ID {
		t.Fatalf("reused child ID=%d, want %d", runID, existing.ID)
	}
	var childCount int64
	if err := db.Model(&model.TaskRun{}).
		Where("task_id = ? AND upstream_task_run_id = ?", downstream.ID, upstreamRun.ID).
		Count(&childCount).Error; err != nil {
		t.Fatalf("count exact children: %v", err)
	}
	if childCount != 1 {
		t.Fatalf("exact child count=%d, want one", childCount)
	}
}

func TestChainDisabledAndArchivedCreateExplicitSkippedRuns(t *testing.T) {
	for _, tc := range []struct {
		name     string
		enabled  bool
		archived bool
		wantText string
	}{
		{name: "disabled", enabled: false, wantText: "暂停"},
		{name: "archived", enabled: false, archived: true, wantText: "归档"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openManagerTestDB(t)
			manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
			shutdownManagerOnCleanup(t, manager)
			upstream, downstream, upstreamRun, payload := createDownstreamEffectFixture(t, db, tc.enabled, tc.archived)
			if tc.archived {
				if err := db.Model(&model.Task{}).Where("id = ?", downstream.ID).Update("status", string(StatusSuccess)).Error; err != nil {
					t.Fatalf("set archived aggregate status: %v", err)
				}
			}
			effect := model.TaskRunEffect{TaskRunID: upstreamRun.ID, EffectType: model.TaskRunEffectTypeDownstream, Payload: payload}
			if err := manager.executeTaskRunEffect(context.Background(), effect); err != nil {
				t.Fatalf("execute chain effect: %v", err)
			}
			var child model.TaskRun
			if err := db.Where("task_id = ? AND upstream_task_run_id = ?", downstream.ID, upstreamRun.ID).First(&child).Error; err != nil {
				t.Fatalf("load explicit skipped child: %v", err)
			}
			if child.Status != model.TaskRunStatusSkipped || !strings.Contains(child.SkipReason, tc.wantText) {
				t.Fatalf("child status=%q reason=%q, want skipped/%q", child.Status, child.SkipReason, tc.wantText)
			}
			_ = upstream
		})
	}
}

func TestChainBusyEffectExhaustionRemainsFailed(t *testing.T) {
	db := openManagerTestDB(t)
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)
	upstream, downstream, upstreamRun, payload := createDownstreamEffectFixture(t, db, true, false)
	effect := model.TaskRunEffect{
		TaskRunID: upstreamRun.ID, EffectKey: "downstream:busy", EffectType: model.TaskRunEffectTypeDownstream,
		Payload: payload, Status: model.TaskRunEffectStatusPending, Attempts: taskRunEffectMaxAttempts - 1,
		NextAttemptAt: new(time.Now().UTC()),
	}
	if err := db.Create(&effect).Error; err != nil {
		t.Fatalf("create busy effect: %v", err)
	}
	manager.pendingRuns.Store(downstream.ID, struct{}{})
	defer manager.pendingRuns.Delete(downstream.ID)
	if err := manager.drainTaskRunEffects(context.Background(), upstreamRun.ID); err == nil {
		t.Fatal("busy effect exhaustion returned nil")
	}
	var stored model.TaskRunEffect
	if err := db.First(&stored, effect.ID).Error; err != nil {
		t.Fatalf("reload busy effect: %v", err)
	}
	if stored.Status != model.TaskRunEffectStatusFailed || stored.Attempts != taskRunEffectMaxAttempts || stored.NextAttemptAt != nil || stored.LastError == "" {
		t.Fatalf("busy effect=%+v, want exhausted failed state", stored)
	}
	var childCount int64
	if err := db.Model(&model.TaskRun{}).Where("task_id = ? AND upstream_task_run_id = ?", downstream.ID, upstreamRun.ID).Count(&childCount).Error; err != nil {
		t.Fatalf("count busy children: %v", err)
	}
	if childCount != 0 {
		t.Fatalf("busy effect created %d child runs", childCount)
	}
	_ = upstream
}

func TestChainEffectRestartDoesNotDuplicateChild(t *testing.T) {
	db := openManagerTestDB(t)
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)
	_, downstream, upstreamRun, payload := createDownstreamEffectFixture(t, db, true, false)
	existing := model.TaskRun{
		TaskID: downstream.ID, NodeIDSnapshot: downstream.NodeID, TriggerType: "chain",
		Status: model.TaskRunStatusPending, ChainRunID: upstreamRun.ChainRunID, UpstreamTaskRunID: &upstreamRun.ID,
	}
	if err := db.Create(&existing).Error; err != nil {
		t.Fatalf("create existing child: %v", err)
	}
	effect := model.TaskRunEffect{TaskRunID: upstreamRun.ID, EffectType: model.TaskRunEffectTypeDownstream, Payload: payload}
	if err := manager.executeTaskRunEffect(context.Background(), effect); err != nil {
		t.Fatalf("replay downstream effect: %v", err)
	}
	var childCount int64
	if err := db.Model(&model.TaskRun{}).Where("task_id = ? AND upstream_task_run_id = ?", downstream.ID, upstreamRun.ID).Count(&childCount).Error; err != nil {
		t.Fatalf("count replay children: %v", err)
	}
	if childCount != 1 {
		t.Fatalf("replay created %d child runs, want one", childCount)
	}
}

func TestChainShutdownEffectIsRetryableAndCreatesNoRun(t *testing.T) {
	db := openManagerTestDB(t)
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)
	_, downstream, upstreamRun, payload := createDownstreamEffectFixture(t, db, true, false)
	manager.shuttingDown.Store(true)
	if err := manager.executeTaskRunEffect(context.Background(), model.TaskRunEffect{TaskRunID: upstreamRun.ID, EffectType: model.TaskRunEffectTypeDownstream, Payload: payload}); err == nil {
		t.Fatal("shutdown chain effect returned nil")
	}
	var childCount int64
	if err := db.Model(&model.TaskRun{}).Where("task_id = ? AND upstream_task_run_id = ?", downstream.ID, upstreamRun.ID).Count(&childCount).Error; err != nil {
		t.Fatalf("count shutdown children: %v", err)
	}
	if childCount != 0 {
		t.Fatalf("shutdown chain effect created %d child runs", childCount)
	}
}

func TestDelayedVerificationEffectDoesNotRaiseAfterNewerSuccess(t *testing.T) {
	db := openManagerTestDB(t)
	task := seedTaskForManagerTest(t, db)
	warningRun := model.TaskRun{
		TaskID: task.ID, NodeIDSnapshot: task.NodeID, TriggerType: "manual",
		Status: model.TaskRunStatusWarning, FinishedAt: new(time.Now().UTC()),
	}
	if err := db.Create(&warningRun).Error; err != nil {
		t.Fatalf("create warning run: %v", err)
	}
	successRun := model.TaskRun{
		TaskID: task.ID, NodeIDSnapshot: task.NodeID, TriggerType: "manual",
		Status: model.TaskRunStatusSuccess, FinishedAt: new(time.Now().UTC()),
	}
	if err := db.Create(&successRun).Error; err != nil {
		t.Fatalf("create success run: %v", err)
	}
	payload, err := json.Marshal(alertTaskRunEffect{
		TaskID: task.ID, RunID: warningRun.ID, Action: "verification_failure", Message: "stale verification",
	})
	if err != nil {
		t.Fatalf("encode verification effect: %v", err)
	}
	manager := &Manager{db: db, alertDispatcher: alerting.NewDispatcher(db, nil, nil)}
	if err := manager.executeTaskRunEffect(context.Background(), model.TaskRunEffect{
		TaskRunID: warningRun.ID, EffectType: model.TaskRunEffectTypeAlert, Payload: string(payload),
	}); err != nil {
		t.Fatalf("execute delayed verification effect: %v", err)
	}
	var alertCount int64
	if err := db.Model(&model.Alert{}).Where("task_id = ?", task.ID).Count(&alertCount).Error; err != nil {
		t.Fatalf("count delayed verification alerts: %v", err)
	}
	if alertCount != 0 {
		t.Fatalf("delayed verification effect created %d alert(s) after newer success", alertCount)
	}
}
