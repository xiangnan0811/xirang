package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/automation"
	"xirang/backend/internal/model"
	taskexec "xirang/backend/internal/task/executor"
	taskscheduler "xirang/backend/internal/task/scheduler"

	"gorm.io/gorm"
)

type leaseCancellationExecutor struct {
	started    chan struct{}
	canceled   chan struct{}
	startOnce  sync.Once
	cancelOnce sync.Once
}

func (e *leaseCancellationExecutor) Run(ctx context.Context, _ model.Task, _ taskexec.LogFunc, _ taskexec.ProgressFunc) (int, error) {
	e.startOnce.Do(func() { close(e.started) })
	select {
	case <-ctx.Done():
		e.cancelOnce.Do(func() { close(e.canceled) })
		return -1, ctx.Err()
	case <-time.After(10 * time.Second):
		return 0, nil
	}
}

func bareEffectManager(db *gorm.DB, owner string) *Manager {
	manager := &Manager{
		db:                     db,
		stateMachine:           NewStateMachine(),
		executionOwnerID:       owner,
		executionLeaseDuration: time.Minute,
	}
	manager.shuttingDown.Store(true)
	return manager
}

func seedRetryEffect(t *testing.T, db *gorm.DB, owner string, taskStatus TaskStatus, nextRunAt time.Time, payload string, attempts int) (model.Task, model.TaskRun, model.TaskRunEffect) {
	t.Helper()
	node := model.Node{
		Name:     fmt.Sprintf("retry-regression-node-%d", time.Now().UnixNano()),
		Host:     "127.0.0.1",
		Port:     22,
		Username: "root",
		AuthType: "key",
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}
	task := model.Task{
		Name:         fmt.Sprintf("retry-regression-task-%d", time.Now().UnixNano()),
		NodeID:       node.ID,
		ExecutorType: "local",
		Status:       string(taskStatus),
		Enabled:      true,
		NextRunAt:    &nextRunAt,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}
	parent := model.TaskRun{
		TaskID: task.ID, NodeIDSnapshot: node.ID, TriggerType: "chain", Status: model.TaskRunStatusSuccess,
		ChainRunID: "parent-chain",
	}
	if err := db.Create(&parent).Error; err != nil {
		t.Fatalf("create parent run: %v", err)
	}
	upstreamID := parent.ID
	lease := time.Now().UTC().Add(time.Hour)
	source := model.TaskRun{
		TaskID: task.ID, NodeIDSnapshot: node.ID, TriggerType: "manual", Status: model.TaskRunStatusFailed,
		ChainRunID: "retry-chain", UpstreamTaskRunID: &upstreamID, ExecutionOwnerID: owner,
		ExecutionLeaseUntil: &lease,
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("create source run: %v", err)
	}
	if payload == "" {
		encoded, err := json.Marshal(retryTaskRunEffect{
			TaskID: task.ID, ChainRunID: source.ChainRunID, UpstreamTaskRunID: source.UpstreamTaskRunID,
			PredecessorRunID: source.ID,
		})
		if err != nil {
			t.Fatalf("encode retry effect: %v", err)
		}
		payload = string(encoded)
	}
	effect := model.TaskRunEffect{
		TaskRunID: source.ID, EffectKey: "retry", EffectType: model.TaskRunEffectTypeRetry,
		Payload: payload, Status: model.TaskRunEffectStatusPending, Attempts: attempts,
		NextAttemptAt: &nextRunAt,
	}
	if err := db.Create(&effect).Error; err != nil {
		t.Fatalf("create retry effect: %v", err)
	}
	return task, source, effect
}

func runLegacyRetryWithoutEffectRecovery(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.AutoMigrate(&model.RestoreDrillEvidence{}, &model.TaskLog{}, &model.TaskTrafficSample{}, &model.Alert{}); err != nil {
		t.Fatal(err)
	}
	owner := "legacy-retry-owner"
	retryDue := time.Now().UTC().Add(250 * time.Millisecond)
	task, source, effect := seedRetryEffect(t, db, owner, StatusRetrying, retryDue, "", 0)
	if err := db.Delete(&effect).Error; err != nil {
		t.Fatal(err)
	}
	var before int64
	if err := db.Model(&model.TaskRunEffect{}).Where("task_run_id = ?", source.ID).Count(&before).Error; err != nil {
		t.Fatal(err)
	}
	if before != 0 {
		t.Fatalf("legacy fixture retained %d retry effects before startup", before)
	}
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)
	if err := manager.LoadSchedules(context.Background()); err != nil {
		t.Fatalf("recover legacy retry effect: %v", err)
	}
	var recovered model.TaskRunEffect
	if err := db.Where("task_run_id = ? AND effect_type = ?", source.ID, model.TaskRunEffectTypeRetry).First(&recovered).Error; err != nil {
		t.Fatalf("load reconstructed retry effect: %v", err)
	}
	if recovered.NextAttemptAt == nil || recovered.NextAttemptAt.Before(retryDue.Add(-20*time.Millisecond)) {
		t.Fatalf("reconstructed retry deadline=%v, want %v", recovered.NextAttemptAt, retryDue)
	}
	var payload retryTaskRunEffect
	if err := json.Unmarshal([]byte(recovered.Payload), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.PredecessorRunID != source.ID || payload.ChainRunID != source.ChainRunID ||
		payload.UpstreamTaskRunID == nil || *payload.UpstreamTaskRunID != *source.UpstreamTaskRunID {
		t.Fatalf("reconstructed retry payload=%+v, want predecessor=%d chain=%q upstream=%d",
			payload, source.ID, source.ChainRunID, *source.UpstreamTaskRunID)
	}
	if err := manager.LoadSchedules(context.Background()); err != nil {
		t.Fatalf("repeat legacy retry reconciliation: %v", err)
	}
	var effectCount int64
	if err := db.Model(&model.TaskRunEffect{}).Where("task_run_id = ? AND effect_type = ?", source.ID, model.TaskRunEffectTypeRetry).Count(&effectCount).Error; err != nil {
		t.Fatal(err)
	}
	if effectCount != 1 {
		t.Fatalf("legacy retry reconciliation created %d effects, want one", effectCount)
	}
	time.Sleep(time.Until(retryDue) + 120*time.Millisecond)
	if err := manager.drainReadyTaskRunEffects(context.Background()); err != nil {
		t.Fatalf("deliver reconstructed retry: %v", err)
	}
	var retryCount int64
	if err := db.Model(&model.TaskRun{}).Where("task_id = ? AND trigger_type = ?", task.ID, "retry").Count(&retryCount).Error; err != nil {
		t.Fatal(err)
	}
	if retryCount != 1 {
		t.Fatalf("reconstructed retry runs=%d, want one", retryCount)
	}
}

func TestLegacyRetryWithoutEffectRecoversOnStartupSQLite(t *testing.T) {
	runLegacyRetryWithoutEffectRecovery(t, openManagerTestDB(t))
}

func TestLegacyRetryWithoutEffectRecoversOnStartupPostgres(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	runLegacyRetryWithoutEffectRecovery(t, openTaskTerminalPostgresDB(t, dsn))
}

func runRetryEffectCommittedDeadline(t *testing.T, db *gorm.DB) {
	t.Helper()
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)
	manager.shuttingDown.Store(true)

	now := time.Now().UTC()
	oldCronDeadline := now.Add(time.Hour)
	task, source, _ := seedRetryEffect(t, db, manager.executionOwnerID, StatusRunning, oldCronDeadline, "", 0)
	// The terminal transition itself is the effect reservation under test; the
	// helper's prebuilt effect is removed so its old deadline cannot win the
	// unique task-run/effect-key conflict.
	if err := db.Where("task_run_id = ?", source.ID).Delete(&model.TaskRunEffect{}).Error; err != nil {
		t.Fatal(err)
	}
	// seedRetryEffect creates a failed source; put the aggregate/source into the
	// running pair expected by terminalizeTaskRun before publishing retrying.
	if err := db.Model(&model.TaskRun{}).Where("id = ?", source.ID).Updates(map[string]interface{}{
		"status": model.TaskRunStatusRunning, "last_error": "attempt failed",
	}).Error; err != nil {
		t.Fatal(err)
	}
	retryDue := time.Now().UTC().Add(250 * time.Millisecond)
	if err := manager.terminalizeTaskRun(context.Background(), task.ID, source.ID,
		[]string{model.TaskRunStatusRunning}, taskStatusPtr(StatusRetrying),
		map[string]interface{}{"retry_count": 1, "next_run_at": &retryDue, "last_error": "attempt failed"},
		StatusFailed, map[string]interface{}{"last_error": "attempt failed"}); err != nil {
		t.Fatalf("terminalize retrying attempt: %v", err)
	}
	var effect model.TaskRunEffect
	if err := db.Where("task_run_id = ? AND effect_type = ?", source.ID, model.TaskRunEffectTypeRetry).First(&effect).Error; err != nil {
		t.Fatalf("load retry effect: %v", err)
	}
	if effect.NextAttemptAt == nil || effect.NextAttemptAt.Before(retryDue.Add(-20*time.Millisecond)) {
		t.Fatalf("retry effect deadline=%v, want committed retry deadline near %v", effect.NextAttemptAt, retryDue)
	}
	var retryCount int64
	if err := db.Model(&model.TaskRun{}).Where("task_id = ? AND trigger_type = ?", task.ID, "retry").Count(&retryCount).Error; err != nil {
		t.Fatal(err)
	}
	if retryCount != 0 {
		t.Fatalf("retry delivered before committed deadline: %d runs", retryCount)
	}

	time.Sleep(time.Until(retryDue) + 120*time.Millisecond)
	if err := manager.drainReadyTaskRunEffects(context.Background()); err != nil {
		t.Fatalf("deliver retry effect at committed deadline: %v", err)
	}

	if err := db.First(&effect, effect.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.TaskRun{}).Where("task_id = ? AND trigger_type = ?", task.ID, "retry").Count(&retryCount).Error; err != nil {
		t.Fatal(err)
	}
	if retryCount != 1 {
		t.Fatalf("retry runs=%d, want one delivery; effect=%+v", retryCount, effect)
	}
	var retryRun model.TaskRun
	if err := db.Where("task_id = ? AND trigger_type = ?", task.ID, "retry").First(&retryRun).Error; err != nil {
		t.Fatal(err)
	}
	if retryRun.ChainRunID != source.ChainRunID || retryRun.UpstreamTaskRunID != nil {
		t.Fatalf("retry lineage=%+v, want chain %q and no duplicate upstream edge", retryRun, source.ChainRunID)
	}
	var payload retryTaskRunEffect
	if err := json.Unmarshal([]byte(effect.Payload), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.UpstreamTaskRunID == nil || *payload.UpstreamTaskRunID != *source.UpstreamTaskRunID {
		t.Fatalf("retry payload upstream=%v, want %v", payload.UpstreamTaskRunID, source.UpstreamTaskRunID)
	}
}

func TestRetryEffectUsesCommittedNextRunAtAndPreservesLineageSQLite(t *testing.T) {
	runRetryEffectCommittedDeadline(t, openManagerTestDB(t))
}

func TestRetryEffectUsesCommittedNextRunAtAndPreservesLineagePostgres(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	runRetryEffectCommittedDeadline(t, openTaskTerminalPostgresDB(t, dsn))
}

func taskStatusPtr(status TaskStatus) *TaskStatus { return &status }

func runRetryEffectExhaustion(t *testing.T, db *gorm.DB) {
	t.Helper()
	manager := bareEffectManager(db, "retry-exhaustion-owner")
	task, _, effect := seedRetryEffect(t, db, manager.executionOwnerID, StatusRetrying, time.Now().UTC(), "{malformed", taskRunEffectMaxAttempts-1)
	if err := manager.drainReadyTaskRunEffects(context.Background()); err == nil {
		t.Fatal("malformed retry effect should report delivery failure")
	}
	var current model.TaskRunEffect
	if err := db.First(&current, effect.ID).Error; err != nil {
		t.Fatal(err)
	}
	if current.Status != model.TaskRunEffectStatusFailed || current.Attempts != taskRunEffectMaxAttempts || current.NextAttemptAt != nil {
		t.Fatalf("exhausted effect=%+v, want failed at bounded attempt with no next deadline", current)
	}
	var settled model.Task
	if err := db.First(&settled, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if settled.Status != string(StatusFailed) {
		t.Fatalf("aggregate status=%q, want failed", settled.Status)
	}
}

func TestRetryEffectExhaustionSettlesAggregateSQLite(t *testing.T) {
	runRetryEffectExhaustion(t, openManagerTestDB(t))
}

func TestRetryEffectExhaustionSettlesAggregatePostgres(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	runRetryEffectExhaustion(t, openTaskTerminalPostgresDB(t, dsn))
}

func runRetryEffectSuperseded(t *testing.T, db *gorm.DB) {
	t.Helper()
	manager := bareEffectManager(db, "retry-supersession-owner")
	task, source, effect := seedRetryEffect(t, db, manager.executionOwnerID, StatusRetrying, time.Now().UTC(), "", 0)
	newer := model.TaskRun{
		TaskID: task.ID, NodeIDSnapshot: task.NodeID, TriggerType: "manual",
		Status: model.TaskRunStatusFailed, ChainRunID: "newer-chain",
	}
	if err := db.Create(&newer).Error; err != nil {
		t.Fatal(err)
	}
	if err := manager.drainReadyTaskRunEffects(context.Background()); err != nil {
		t.Fatalf("drain superseded retry effect: %v", err)
	}
	var current model.TaskRunEffect
	if err := db.First(&current, effect.ID).Error; err != nil {
		t.Fatal(err)
	}
	if current.Status != model.TaskRunEffectStatusSucceeded {
		t.Fatalf("superseded effect status=%q, want succeeded", current.Status)
	}
	var retryCount int64
	if err := db.Model(&model.TaskRun{}).Where("task_id = ? AND trigger_type = ?", task.ID, "retry").Count(&retryCount).Error; err != nil {
		t.Fatal(err)
	}
	if retryCount != 0 {
		t.Fatalf("superseded predecessor %d created retry runs; source=%d newer=%d", retryCount, source.ID, newer.ID)
	}
	var currentTask model.Task
	if err := db.First(&currentTask, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if currentTask.Status != string(StatusRetrying) {
		t.Fatalf("superseded retry changed aggregate status=%q", currentTask.Status)
	}
}

func TestRetryEffectSupersededAttemptDoesNotReviveOldChainSQLite(t *testing.T) {
	runRetryEffectSuperseded(t, openManagerTestDB(t))
}

func TestRetryEffectSupersededAttemptDoesNotReviveOldChainPostgres(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	runRetryEffectSuperseded(t, openTaskTerminalPostgresDB(t, dsn))
}

func runExhaustedSupersededRetryEffect(t *testing.T, db *gorm.DB) {
	t.Helper()
	manager := bareEffectManager(db, "retry-stale-exhaustion-owner")
	task, source, effect := seedRetryEffect(t, db, manager.executionOwnerID, StatusRetrying, time.Now().UTC(), "{malformed", taskRunEffectMaxAttempts-1)
	newer := model.TaskRun{
		TaskID: task.ID, NodeIDSnapshot: task.NodeID, TriggerType: "manual",
		Status: model.TaskRunStatusFailed, ChainRunID: "newer-chain",
	}
	if err := db.Create(&newer).Error; err != nil {
		t.Fatal(err)
	}
	if err := manager.drainReadyTaskRunEffects(context.Background()); err == nil {
		t.Fatal("malformed superseded effect should report delivery failure")
	}
	var current model.TaskRunEffect
	if err := db.First(&current, effect.ID).Error; err != nil {
		t.Fatal(err)
	}
	if current.Status != model.TaskRunEffectStatusFailed || current.Attempts != taskRunEffectMaxAttempts {
		t.Fatalf("stale exhausted effect=%+v, want bounded failure", current)
	}
	var currentTask model.Task
	if err := db.First(&currentTask, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if currentTask.Status != string(StatusRetrying) {
		t.Fatalf("stale exhausted effect failed newer aggregate cycle: status=%q source=%d newer=%d", currentTask.Status, source.ID, newer.ID)
	}
}

func TestExhaustedSupersededRetryEffectDoesNotFailNewCycleSQLite(t *testing.T) {
	runExhaustedSupersededRetryEffect(t, openManagerTestDB(t))
}

func TestExhaustedSupersededRetryEffectDoesNotFailNewCyclePostgres(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	runExhaustedSupersededRetryEffect(t, openTaskTerminalPostgresDB(t, dsn))
}

func runCompetingRetryManagers(t *testing.T, db *gorm.DB) {
	t.Helper()
	managerA := bareEffectManager(db, "retry-competitor-a")
	managerB := bareEffectManager(db, "retry-competitor-b")
	task, _, effect := seedRetryEffect(t, db, managerA.executionOwnerID, StatusRetrying, time.Now().UTC(), "", 0)
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, manager := range []*Manager{managerA, managerB} {
		wg.Add(1)
		go func(worker *Manager) {
			defer wg.Done()
			<-start
			var err error
			for range 20 {
				err = worker.drainReadyTaskRunEffects(context.Background())
				if err == nil {
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
			errs <- err
		}(manager)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("competing manager never converged: %v", err)
	}
	var current model.TaskRunEffect
	if err := db.First(&current, effect.ID).Error; err != nil {
		t.Fatal(err)
	}
	if current.Status != model.TaskRunEffectStatusSucceeded {
		t.Fatalf("effect status=%q, want succeeded", current.Status)
	}
	var retryCount int64
	if err := db.Model(&model.TaskRun{}).Where("task_id = ? AND trigger_type = ?", task.ID, "retry").Count(&retryCount).Error; err != nil {
		t.Fatal(err)
	}
	if retryCount != 1 {
		t.Fatalf("competing managers created %d retries, want one", retryCount)
	}
}

func TestRetryEffectCompetingManagersSQLite(t *testing.T) {
	runCompetingRetryManagers(t, openConcurrentManagerTestDB(t))
}

func TestRetryEffectCompetingManagersPostgres(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	runCompetingRetryManagers(t, openTaskTerminalPostgresDB(t, dsn))
}

func runPendingRetryReservationRecovery(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.AutoMigrate(&model.TaskLog{}, &model.TaskTrafficSample{}, &model.Alert{}); err != nil {
		t.Fatal(err)
	}
	first := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, first)
	first.shuttingDown.Store(true)
	node := model.Node{Name: fmt.Sprintf("pending-retry-node-%d", time.Now().UnixNano()), Host: "127.0.0.1", Port: 22, Username: "root", AuthType: "key"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	task := model.Task{Name: fmt.Sprintf("pending-retry-task-%d", time.Now().UnixNano()), NodeID: node.ID, ExecutorType: "local", Status: string(StatusRetrying), Enabled: true}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	run := model.TaskRun{TaskID: task.ID, NodeIDSnapshot: node.ID, TriggerType: "retry", Status: model.TaskRunStatusPending, ChainRunID: "restart-chain"}
	if err := db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	if err := first.reconcilePendingDurableRuns(context.Background()); err != nil {
		t.Fatal(err)
	}
	restarted := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, restarted)
	if err := restarted.reconcilePendingDurableRuns(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		var current model.TaskRun
		if err := db.First(&current, run.ID).Error; err != nil {
			t.Fatal(err)
		}
		if current.Status == model.TaskRunStatusSuccess {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("restarted manager left pending retry run status=%q", current.Status)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestPendingRetryReservationRecoversAfterManagerRestartSQLite(t *testing.T) {
	runPendingRetryReservationRecovery(t, openManagerTestDB(t))
}

func TestPendingRetryReservationRecoversAfterManagerRestartPostgres(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	runPendingRetryReservationRecovery(t, openTaskTerminalPostgresDB(t, dsn))
}

func runTransientDownstreamEffectRecovery(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.AutoMigrate(&model.TaskLog{}, &model.TaskTrafficSample{}, &model.Alert{}); err != nil {
		t.Fatal(err)
	}
	admission := &nodeWriteAdmissionFake{errs: make([]error, nodeWriteReservationAttempts)}
	for i := range admission.errs {
		admission.errs[i] = ErrNodeWriteUnavailable
	}
	executor := &successExecutor{}
	manager := NewManager(db, stubExecutorFactory{executor: executor}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)
	manager.SetNodeWriteAdmission(admission)
	upstream := seedTaskForManagerTest(t, db)
	if err := db.Model(&model.Task{}).Where("id = ?", upstream.ID).Update("status", string(StatusSuccess)).Error; err != nil {
		t.Fatal(err)
	}
	source := model.TaskRun{
		TaskID: upstream.ID, NodeIDSnapshot: upstream.NodeID, TriggerType: "manual",
		Status: model.TaskRunStatusSuccess, ChainRunID: "downstream-retry-chain",
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	downstream := model.Task{
		Name:   fmt.Sprintf("downstream-retry-task-%d", time.Now().UnixNano()),
		NodeID: upstream.NodeID, ExecutorType: "local", Status: string(StatusPending), Enabled: true,
	}
	if err := db.Create(&downstream).Error; err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(downstreamTaskRunEffect{
		TaskID: downstream.ID, UpstreamRunID: source.ID, ChainRunID: source.ChainRunID,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	effect := model.TaskRunEffect{
		TaskRunID: source.ID, EffectKey: "downstream:transient", EffectType: model.TaskRunEffectTypeDownstream,
		Payload: string(payload), Status: model.TaskRunEffectStatusPending, NextAttemptAt: &now,
	}
	if err := db.Create(&effect).Error; err != nil {
		t.Fatal(err)
	}
	if err := manager.drainReadyTaskRunEffects(context.Background()); err == nil {
		t.Fatal("first downstream delivery should expose admission outage")
	}
	if err := db.Model(&model.TaskRunEffect{}).Where("id = ?", effect.ID).Update("next_attempt_at", time.Now().UTC()).Error; err != nil {
		t.Fatal(err)
	}
	if err := manager.drainReadyTaskRunEffects(context.Background()); err != nil {
		t.Fatalf("downstream delivery after admission recovery: %v", err)
	}
	var downstreamRuns int64
	if err := db.Model(&model.TaskRun{}).Where("task_id = ? AND upstream_task_run_id = ?", downstream.ID, source.ID).Count(&downstreamRuns).Error; err != nil {
		t.Fatal(err)
	}
	if downstreamRuns != 1 {
		t.Fatalf("downstream runs=%d, want one after transient retry", downstreamRuns)
	}
}

func TestTransientDownstreamEffectRetriesAfterAdmissionRecoverySQLite(t *testing.T) {
	runTransientDownstreamEffectRecovery(t, openManagerTestDB(t))
}

func TestTransientDownstreamEffectRetriesAfterAdmissionRecoveryPostgres(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	runTransientDownstreamEffectRecovery(t, openTaskTerminalPostgresDB(t, dsn))
}

func runOrdinaryHeartbeatLossFencesTerminalWrite(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.AutoMigrate(&model.TaskLog{}, &model.TaskTrafficSample{}, &model.Alert{}); err != nil {
		t.Fatal(err)
	}
	executor := &leaseCancellationExecutor{started: make(chan struct{}), canceled: make(chan struct{})}
	manager := NewManager(db, stubExecutorFactory{executor: executor}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)
	// Ownership is lost by the explicit foreign-owner write, not startup expiry.
	manager.executionLeaseDuration = 3 * time.Second
	task := seedTaskForManagerTest(t, db)
	if err := db.Model(&model.Task{}).Where("id = ?", task.ID).Update("executor_type", "local").Error; err != nil {
		t.Fatal(err)
	}
	runID, err := manager.TriggerManual(task.ID)
	if err != nil {
		t.Fatalf("trigger task: %v", err)
	}
	select {
	case <-executor.started:
	case <-time.After(5 * time.Second):
		t.Fatal("executor did not start")
	}
	foreignLease := time.Now().UTC().Add(time.Second)
	if err := db.Model(&model.TaskRun{}).Where("id = ?", runID).Updates(map[string]interface{}{
		"execution_owner_id": "foreign-owner", "execution_lease_until": &foreignLease,
	}).Error; err != nil {
		t.Fatal(err)
	}
	select {
	case <-executor.canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("heartbeat ownership loss did not cancel executor context")
	}
	var current model.TaskRun
	if err := db.First(&current, runID).Error; err != nil {
		t.Fatal(err)
	}
	if current.Status != model.TaskRunStatusRunning || current.ExecutionOwnerID != "foreign-owner" {
		t.Fatalf("stale runner overwrote foreign-owned run: %+v", current)
	}
	if err := db.Model(&model.TaskRun{}).Where("id = ?", runID).Updates(map[string]interface{}{
		"status": model.TaskRunStatusFailed, "execution_owner_id": "", "execution_lease_until": nil,
		"finished_at": time.Now().UTC(),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.Task{}).Where("id = ?", task.ID).Update("status", string(StatusFailed)).Error; err != nil {
		t.Fatal(err)
	}
}

func TestOrdinaryHeartbeatLossCancelsExecutorAndFencesTerminalWriteSQLite(t *testing.T) {
	runOrdinaryHeartbeatLossFencesTerminalWrite(t, openManagerTestDB(t))
}

func TestOrdinaryHeartbeatLossCancelsExecutorAndFencesTerminalWritePostgres(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	runOrdinaryHeartbeatLossFencesTerminalWrite(t, openTaskTerminalPostgresDB(t, dsn))
}
func runPendingAutomationReservationRecovery(t *testing.T, db *gorm.DB) {
	if err := db.AutoMigrate(&model.TaskLog{}, &model.TaskTrafficSample{}, &model.Alert{}); err != nil {
		t.Fatal(err)
	}
	first := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, first)
	first.shuttingDown.Store(true)
	task := seedTaskForManagerTest(t, db)
	if err := db.Model(&model.Task{}).Where("id = ?", task.ID).Update("executor_type", "local").Error; err != nil {
		t.Fatal(err)
	}
	var runID uint
	if err := db.Transaction(func(tx *gorm.DB) error {
		var err error
		runID, err = first.ReserveAutomationRunTx(context.Background(), tx, task.ID)
		return err
	}); err != nil {
		t.Fatalf("reserve automation run: %v", err)
	}
	if runID == 0 {
		t.Fatal("automation reservation returned no run ID")
	}
	restarted := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, restarted)
	if err := restarted.reconcilePendingDurableRuns(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		var run model.TaskRun
		if err := db.First(&run, runID).Error; err != nil {
			t.Fatal(err)
		}
		if run.Status == model.TaskRunStatusSuccess {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("restarted manager left pending automation run status=%q", run.Status)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestPendingAutomationReservationRecoversAfterManagerRestartSQLite(t *testing.T) {
	runPendingAutomationReservationRecovery(t, openManagerTestDB(t))
}

func TestPendingAutomationReservationRecoversAfterManagerRestartPostgres(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	runPendingAutomationReservationRecovery(t, openTaskTerminalPostgresDB(t, dsn))
}

func runDurableAutomationTriggerRecovery(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.AutoMigrate(
		&model.TaskLog{}, &model.TaskTrafficSample{}, &model.Alert{},
		&model.AutomationRule{}, &model.AutomationRuleLog{},
	); err != nil {
		t.Fatal(err)
	}
	first := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, first)
	first.shuttingDown.Store(true)
	task := seedTaskForManagerTest(t, db)
	if err := db.Model(&model.Task{}).Where("id = ?", task.ID).Update("executor_type", "local").Error; err != nil {
		t.Fatal(err)
	}
	source := model.TaskRun{
		TaskID: task.ID, NodeIDSnapshot: task.NodeID, TriggerType: "manual", Status: model.TaskRunStatusSuccess,
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	rule := model.AutomationRule{
		Name: fmt.Sprintf("trigger-rule-%d", time.Now().UnixNano()), EventType: automation.EventBackupFailed,
		EventFilter: "{}", ActionType: automation.ActionTriggerTask,
		ActionConfig: fmt.Sprintf(`{"task_id":"%d"}`, task.ID), Enabled: true,
	}
	if err := db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(policyActionEffectPayload{
		Rule: rule, Event: automation.Event{Type: automation.EventBackupFailed, Context: map[string]interface{}{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	lease := time.Now().UTC().Add(time.Hour)
	effect := model.TaskRunEffect{
		TaskRunID: source.ID, EffectKey: "automation-rule:trigger",
		EffectType: model.TaskRunEffectTypeAutomationRule, Payload: string(payload),
		Status: model.TaskRunEffectStatusRunning, ClaimedBy: first.executionOwnerID, ClaimLeaseUntil: &lease,
	}
	if err := db.Create(&effect).Error; err != nil {
		t.Fatal(err)
	}
	dispatcher := automation.NewDispatcher(db)
	dispatcher.SetTaskTriggerer(first)
	if err := dispatcher.DispatchTaskRunEffect(context.Background(), automation.Event{}, effect); err != nil {
		t.Fatalf("durable trigger action: %v", err)
	}
	var currentEffect model.TaskRunEffect
	if err := db.First(&currentEffect, effect.ID).Error; err != nil {
		t.Fatal(err)
	}
	if currentEffect.Status != model.TaskRunEffectStatusSucceeded {
		t.Fatalf("trigger effect status=%q, want succeeded", currentEffect.Status)
	}
	var pending model.TaskRun
	if err := db.Where("task_id = ? AND trigger_type = ?", task.ID, "auto").First(&pending).Error; err != nil {
		t.Fatalf("load reserved automation run: %v", err)
	}
	if pending.Status != model.TaskRunStatusPending {
		t.Fatalf("reserved automation run status=%q, want pending before restart", pending.Status)
	}
	var logs []model.AutomationRuleLog
	if err := db.Find(&logs).Error; err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 || logs[0].Result != automation.ResultSuccess {
		t.Fatalf("trigger action logs=%+v, want one success", logs)
	}

	restarted := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, restarted)
	if err := restarted.reconcilePendingDurableRuns(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if err := db.First(&pending, pending.ID).Error; err != nil {
			t.Fatal(err)
		}
		if pending.Status == model.TaskRunStatusSuccess {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("restarted manager left triggered run status=%q", pending.Status)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestDurableAutomationTriggerReservationRecoversSQLite(t *testing.T) {
	runDurableAutomationTriggerRecovery(t, openManagerTestDB(t))
}

func TestDurableAutomationTriggerReservationRecoversPostgres(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	runDurableAutomationTriggerRecovery(t, openTaskTerminalPostgresDB(t, dsn))
}

type policyActionEffectPayload struct {
	Rule  model.AutomationRule `json:"rule"`
	Event automation.Event     `json:"event"`
}

type rollbackPolicyController struct{}

func (rollbackPolicyController) PausePolicyNext(context.Context, uint) error {
	return fmt.Errorf("non-transactional policy control is not used")
}

func (rollbackPolicyController) DisablePolicy(context.Context, uint) error {
	return fmt.Errorf("non-transactional policy control is not used")
}

func (rollbackPolicyController) PausePolicyNextTx(context.Context, *gorm.DB, uint) error {
	return fmt.Errorf("pause policy is not used")
}

func (rollbackPolicyController) DisablePolicyTx(_ context.Context, tx *gorm.DB, policyID uint) ([]uint, error) {
	var taskIDs []uint
	if err := tx.Model(&model.Task{}).Where("policy_id = ? AND source = ?", policyID, "policy").Pluck("id", &taskIDs).Error; err != nil {
		return nil, err
	}
	if err := tx.Model(&model.Policy{}).Where("id = ?", policyID).Update("enabled", false).Error; err != nil {
		return nil, err
	}
	if err := tx.Model(&model.Task{}).Where("policy_id = ? AND source = ?", policyID, "policy").Update("cron_spec", "").Error; err != nil {
		return nil, err
	}
	return taskIDs, fmt.Errorf("injected policy transaction failure")
}

func seedScheduledPolicyEffect(t *testing.T, db *gorm.DB, owner string) (model.Policy, model.Task, model.TaskRun, model.TaskRunEffect) {
	t.Helper()
	if err := db.AutoMigrate(&model.AutomationRule{}, &model.AutomationRuleLog{}); err != nil {
		t.Fatal(err)
	}
	node := model.Node{
		Name: fmt.Sprintf("policy-action-node-%d", time.Now().UnixNano()),
		Host: "127.0.0.1", Port: 22, Username: "root", AuthType: "key",
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	policy := model.Policy{
		Name:       fmt.Sprintf("policy-action-policy-%d", time.Now().UnixNano()),
		SourcePath: "/source", TargetPath: "/target", CronSpec: "@every 1h", Enabled: true,
	}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatal(err)
	}
	task := model.Task{
		Name:   fmt.Sprintf("policy-action-task-%d", time.Now().UnixNano()),
		NodeID: node.ID, PolicyID: &policy.ID, Source: "policy",
		ExecutorType: "local", CronSpec: "@every 1h", Status: string(StatusPending), Enabled: true,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	source := model.TaskRun{
		TaskID: task.ID, NodeIDSnapshot: node.ID, TriggerType: "manual", Status: model.TaskRunStatusSuccess,
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	rule := model.AutomationRule{
		Name:      fmt.Sprintf("policy-action-rule-%d", time.Now().UnixNano()),
		EventType: automation.EventBackupFailed, EventFilter: "{}", ActionType: automation.ActionDisablePolicy,
		ActionConfig: fmt.Sprintf(`{"policy_id":"%d"}`, policy.ID), Enabled: true,
	}
	if err := db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(policyActionEffectPayload{
		Rule: rule, Event: automation.Event{Type: automation.EventBackupFailed, Context: map[string]interface{}{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	lease := time.Now().UTC().Add(time.Hour)
	effect := model.TaskRunEffect{
		TaskRunID: source.ID, EffectKey: "automation-rule:policy-action",
		EffectType: model.TaskRunEffectTypeAutomationRule, Payload: string(payload),
		Status: model.TaskRunEffectStatusRunning, ClaimedBy: owner, ClaimLeaseUntil: &lease,
	}
	if err := db.Create(&effect).Error; err != nil {
		t.Fatal(err)
	}
	return policy, task, source, effect
}

func runPolicyDisableActionCommit(t *testing.T, db *gorm.DB) {
	sched := taskscheduler.NewCronScheduler()
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, sched, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)
	manager.shuttingDown.Store(true)
	policy, task, _, effect := seedScheduledPolicyEffect(t, db, manager.executionOwnerID)
	if err := manager.SyncSchedule(task); err != nil {
		t.Fatal(err)
	}
	if !sched.HasTask(task.ID) {
		t.Fatal("policy task was not registered before disable")
	}
	dispatcher := automation.NewDispatcher(db)
	dispatcher.SetPolicyController(manager)
	if err := dispatcher.DispatchTaskRunEffect(context.Background(), automation.Event{}, effect); err != nil {
		t.Fatalf("disable policy action: %v", err)
	}
	var currentPolicy model.Policy
	if err := db.First(&currentPolicy, policy.ID).Error; err != nil {
		t.Fatal(err)
	}
	var currentTask model.Task
	if err := db.First(&currentTask, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if currentPolicy.Enabled || currentTask.CronSpec != "" || !currentTask.Enabled {
		t.Fatalf("committed disable state policy=%+v task=%+v", currentPolicy, currentTask)
	}
	if sched.HasTask(task.ID) {
		t.Fatal("committed disable left a live scheduler entry")
	}
	var currentEffect model.TaskRunEffect
	if err := db.First(&currentEffect, effect.ID).Error; err != nil {
		t.Fatal(err)
	}
	if currentEffect.Status != model.TaskRunEffectStatusSucceeded {
		t.Fatalf("effect status=%q, want succeeded", currentEffect.Status)
	}
	var logs int64
	if err := db.Model(&model.AutomationRuleLog{}).Count(&logs).Error; err != nil {
		t.Fatal(err)
	}
	if logs != 1 {
		t.Fatalf("automation logs=%d, want one success log", logs)
	}
}

func TestPolicyDisableActionRemovesCommittedScheduleSQLite(t *testing.T) {
	runPolicyDisableActionCommit(t, openManagerTestDB(t))
}

func TestPolicyDisableActionRemovesCommittedSchedulePostgres(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db := openTaskTerminalPostgresDB(t, dsn)
	if err := db.AutoMigrate(&model.AutomationRule{}, &model.AutomationRuleLog{}); err != nil {
		t.Fatal(err)
	}
	runPolicyDisableActionCommit(t, db)
}

func runPolicyDisableActionRollback(t *testing.T, db *gorm.DB) {
	t.Helper()
	sched := taskscheduler.NewCronScheduler()
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, sched, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)
	manager.shuttingDown.Store(true)
	policy, task, _, effect := seedScheduledPolicyEffect(t, db, manager.executionOwnerID)
	if err := manager.SyncSchedule(task); err != nil {
		t.Fatal(err)
	}
	dispatcher := automation.NewDispatcher(db)
	dispatcher.SetPolicyController(rollbackPolicyController{})
	if err := dispatcher.DispatchTaskRunEffect(context.Background(), automation.Event{}, effect); err == nil {
		t.Fatal("faulted policy transaction should be returned")
	}
	var currentPolicy model.Policy
	if err := db.First(&currentPolicy, policy.ID).Error; err != nil {
		t.Fatal(err)
	}
	var currentTask model.Task
	if err := db.First(&currentTask, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !currentPolicy.Enabled || currentTask.CronSpec != "@every 1h" || !currentTask.Enabled {
		t.Fatalf("rolled-back disable changed durable state policy=%+v task=%+v", currentPolicy, currentTask)
	}
	if !sched.HasTask(task.ID) {
		t.Fatal("rolled-back disable removed the live scheduler entry")
	}
	var currentEffect model.TaskRunEffect
	if err := db.First(&currentEffect, effect.ID).Error; err != nil {
		t.Fatal(err)
	}
	if currentEffect.Status != model.TaskRunEffectStatusRunning {
		t.Fatalf("rolled-back effect status=%q, want running", currentEffect.Status)
	}
}

func TestPolicyDisableActionRollbackPreservesScheduleSQLite(t *testing.T) {
	runPolicyDisableActionRollback(t, openManagerTestDB(t))
}

func TestPolicyDisableActionRollbackPreservesSchedulePostgres(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db := openTaskTerminalPostgresDB(t, dsn)
	if err := db.AutoMigrate(&model.AutomationRule{}, &model.AutomationRuleLog{}); err != nil {
		t.Fatal(err)
	}
	runPolicyDisableActionRollback(t, db)
}

func TestSyncScheduleReReadsCurrentDurableStateSQLite(t *testing.T) {
	db := openManagerTestDB(t)
	sched := taskscheduler.NewCronScheduler()
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, sched, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)
	manager.shuttingDown.Store(true)
	task := seedTaskForManagerTest(t, db)
	if err := db.Model(&model.Task{}).Where("id = ?", task.ID).Updates(map[string]interface{}{
		"enabled": true, "cron_spec": "@every 1h", "status": string(StatusPending),
	}).Error; err != nil {
		t.Fatal(err)
	}
	stale := task
	stale.Enabled = true
	stale.CronSpec = "@every 1h"
	if err := manager.SyncSchedule(stale); err != nil {
		t.Fatal(err)
	}
	if !sched.HasTask(task.ID) {
		t.Fatal("current enabled task was not registered")
	}
	if err := db.Model(&model.Task{}).Where("id = ?", task.ID).Updates(map[string]interface{}{
		"enabled": false, "cron_spec": "",
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := manager.SyncSchedule(stale); err != nil {
		t.Fatal(err)
	}
	if sched.HasTask(task.ID) {
		t.Fatal("stale task snapshot resurrected a disabled schedule")
	}
}
func seedRetryRecoveryBatch(t *testing.T, db *gorm.DB, count, handled int) {
	t.Helper()
	if handled > count {
		t.Fatalf("handled retry rows=%d exceeds count=%d", handled, count)
	}
	nextRunAt := time.Now().UTC().Add(time.Hour)
	for i := range count {
		node := model.Node{
			Name:      fmt.Sprintf("retry-batch-node-%d-%d", time.Now().UnixNano(), i),
			Host:      "127.0.0.1",
			Port:      22,
			Username:  "root",
			AuthType:  "key",
			BackupDir: fmt.Sprintf("/tmp/xirang-retry-batch-%d-%d", time.Now().UnixNano(), i),
		}
		if err := db.Create(&node).Error; err != nil {
			t.Fatalf("create retry batch node %d: %v", i, err)
		}
		task := model.Task{
			Name:         fmt.Sprintf("retry-batch-task-%d-%d", time.Now().UnixNano(), i),
			NodeID:       node.ID,
			ExecutorType: "local",
			Status:       string(StatusRetrying),
			Enabled:      true,
			NextRunAt:    &nextRunAt,
		}
		if err := db.Create(&task).Error; err != nil {
			t.Fatalf("create retry batch task %d: %v", i, err)
		}
		source := model.TaskRun{
			TaskID:         task.ID,
			NodeIDSnapshot: node.ID,
			TriggerType:    "manual",
			Status:         model.TaskRunStatusFailed,
			ChainRunID:     fmt.Sprintf("retry-batch-chain-%d", i),
			LastError:      "RETRY_BATCH_SOURCE_FAILURE_FOR_TEST_ONLY",
		}
		if err := db.Create(&source).Error; err != nil {
			t.Fatalf("create retry batch source %d: %v", i, err)
		}
		if i >= handled {
			continue
		}
		effect := model.TaskRunEffect{
			TaskRunID:     source.ID,
			EffectKey:     "retry",
			EffectType:    model.TaskRunEffectTypeRetry,
			Payload:       "{}",
			Status:        model.TaskRunEffectStatusPending,
			NextAttemptAt: &nextRunAt,
		}
		if err := db.Create(&effect).Error; err != nil {
			t.Fatalf("create handled retry effect %d: %v", i, err)
		}
	}
}

func runRetryRecoveryBatchBoundaries(t *testing.T, openDB func(*testing.T) *gorm.DB) {
	t.Helper()
	for _, count := range []int{taskRunRecoveryBatchSize - 1, taskRunRecoveryBatchSize, taskRunRecoveryBatchSize + 1} {
		t.Run(fmt.Sprintf("count-%d", count), func(t *testing.T) {
			db := openDB(t)
			if err := db.AutoMigrate(&model.RestoreDrillEvidence{}, &model.TaskLog{}, &model.TaskTrafficSample{}, &model.Alert{}); err != nil {
				t.Fatalf("migrate retry recovery support tables: %v", err)
			}
			seedRetryRecoveryBatch(t, db, count, 0)
			manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
			shutdownManagerOnCleanup(t, manager)
			manager.shuttingDown.Store(true)

			if err := manager.LoadSchedules(context.Background()); err != nil {
				t.Fatalf("first bounded retry recovery pass count=%d: %v", count, err)
			}
			var recovered int64
			if err := db.Model(&model.TaskRunEffect{}).
				Where("effect_type = ?", model.TaskRunEffectTypeRetry).
				Count(&recovered).Error; err != nil {
				t.Fatalf("count first recovered retry effects count=%d: %v", count, err)
			}
			want := int64(count)
			if count > taskRunRecoveryBatchSize {
				want = int64(taskRunRecoveryBatchSize)
			}
			if recovered != want {
				t.Fatalf("first retry recovery effect count=%d, want %d", recovered, want)
			}
			if count > taskRunRecoveryBatchSize {
				if err := manager.LoadSchedules(context.Background()); err != nil {
					t.Fatalf("repeat bounded retry recovery pass count=%d: %v", count, err)
				}
				if err := db.Model(&model.TaskRunEffect{}).
					Where("effect_type = ?", model.TaskRunEffectTypeRetry).
					Count(&recovered).Error; err != nil {
					t.Fatalf("count repeated recovered retry effects count=%d: %v", count, err)
				}
				if recovered != int64(count) {
					t.Fatalf("repeated retry recovery effect count=%d, want %d", recovered, count)
				}
			}
		})
	}

	t.Run("handled-rows-do-not-fill-page", func(t *testing.T) {
		db := openDB(t)
		if err := db.AutoMigrate(&model.RestoreDrillEvidence{}, &model.TaskLog{}, &model.TaskTrafficSample{}, &model.Alert{}); err != nil {
			t.Fatalf("migrate retry recovery support tables: %v", err)
		}
		seedRetryRecoveryBatch(t, db, taskRunRecoveryBatchSize, taskRunRecoveryBatchSize-1)
		manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
		shutdownManagerOnCleanup(t, manager)
		manager.shuttingDown.Store(true)
		if err := manager.LoadSchedules(context.Background()); err != nil {
			t.Fatalf("retry recovery with handled rows: %v", err)
		}
		var recovered int64
		if err := db.Model(&model.TaskRunEffect{}).
			Where("effect_type = ?", model.TaskRunEffectTypeRetry).
			Count(&recovered).Error; err != nil {
			t.Fatalf("count retry effects with handled rows: %v", err)
		}
		if recovered != taskRunRecoveryBatchSize {
			t.Fatalf("retry effect count with handled rows=%d, want %d", recovered, taskRunRecoveryBatchSize)
		}
	})

	t.Run("newer-terminal-run-suppresses-older-failure", func(t *testing.T) {
		db := openDB(t)
		if err := db.AutoMigrate(&model.RestoreDrillEvidence{}, &model.TaskLog{}, &model.TaskTrafficSample{}, &model.Alert{}); err != nil {
			t.Fatalf("migrate latest predecessor support tables: %v", err)
		}
		node := model.Node{
			Name:      fmt.Sprintf("retry-latest-node-%d", time.Now().UnixNano()),
			Host:      "127.0.0.1",
			Port:      22,
			Username:  "root",
			AuthType:  "key",
			BackupDir: fmt.Sprintf("/tmp/xirang-retry-latest-%d", time.Now().UnixNano()),
		}
		if err := db.Create(&node).Error; err != nil {
			t.Fatalf("create latest predecessor node: %v", err)
		}
		nextRunAt := time.Now().UTC().Add(time.Hour)
		task := model.Task{
			Name:         fmt.Sprintf("retry-latest-task-%d", time.Now().UnixNano()),
			NodeID:       node.ID,
			ExecutorType: "local",
			Status:       string(StatusRetrying),
			Enabled:      true,
			NextRunAt:    &nextRunAt,
		}
		if err := db.Create(&task).Error; err != nil {
			t.Fatalf("create latest predecessor task: %v", err)
		}
		oldFailure := model.TaskRun{
			TaskID:         task.ID,
			NodeIDSnapshot: node.ID,
			TriggerType:    "manual",
			Status:         model.TaskRunStatusFailed,
			ChainRunID:     "retry-latest-old",
			LastError:      "RETRY_LATEST_OLD_FAILURE_FOR_TEST_ONLY",
		}
		if err := db.Create(&oldFailure).Error; err != nil {
			t.Fatalf("create older failed predecessor: %v", err)
		}
		newerTerminal := model.TaskRun{
			TaskID:         task.ID,
			NodeIDSnapshot: node.ID,
			TriggerType:    "manual",
			Status:         model.TaskRunStatusSuccess,
			ChainRunID:     "retry-latest-new",
		}
		if err := db.Create(&newerTerminal).Error; err != nil {
			t.Fatalf("create newer terminal predecessor: %v", err)
		}
		manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
		shutdownManagerOnCleanup(t, manager)
		manager.shuttingDown.Store(true)
		if err := manager.LoadSchedules(context.Background()); err != nil {
			t.Fatalf("reconcile latest predecessor: %v", err)
		}
		var retryEffects int64
		if err := db.Model(&model.TaskRunEffect{}).
			Where("task_run_id IN ? AND effect_type = ?", []uint{oldFailure.ID, newerTerminal.ID}, model.TaskRunEffectTypeRetry).
			Count(&retryEffects).Error; err != nil {
			t.Fatalf("count retry effects for latest predecessor: %v", err)
		}
		if retryEffects != 0 {
			t.Fatalf("retry effects for superseded predecessor=%d, want 0", retryEffects)
		}
	})

	t.Run("later-nonordinary-terminal-does-not-suppress-ordinary-failure", func(t *testing.T) {
		for _, trigger := range []string{"drill", "restore"} {
			t.Run(trigger, func(t *testing.T) {
				db := openDB(t)
				if err := db.AutoMigrate(&model.RestoreDrillEvidence{}, &model.TaskLog{}, &model.TaskTrafficSample{}, &model.Alert{}); err != nil {
					t.Fatalf("migrate nonordinary predecessor support tables: %v", err)
				}
				node := model.Node{
					Name:      fmt.Sprintf("retry-nonordinary-%s-node-%d", trigger, time.Now().UnixNano()),
					Host:      "127.0.0.1",
					Port:      22,
					Username:  "root",
					AuthType:  "key",
					BackupDir: fmt.Sprintf("/tmp/xirang-retry-nonordinary-%s-%d", trigger, time.Now().UnixNano()),
				}
				if err := db.Create(&node).Error; err != nil {
					t.Fatalf("create nonordinary predecessor node: %v", err)
				}
				nextRunAt := time.Now().UTC().Add(time.Hour)
				task := model.Task{
					Name:         fmt.Sprintf("retry-nonordinary-%s-task-%d", trigger, time.Now().UnixNano()),
					NodeID:       node.ID,
					ExecutorType: "local",
					Status:       string(StatusRetrying),
					Enabled:      true,
					NextRunAt:    &nextRunAt,
				}
				if err := db.Create(&task).Error; err != nil {
					t.Fatalf("create nonordinary predecessor task: %v", err)
				}
				oldFailure := model.TaskRun{
					TaskID:         task.ID,
					NodeIDSnapshot: node.ID,
					TriggerType:    "manual",
					Status:         model.TaskRunStatusFailed,
					ChainRunID:     "retry-nonordinary-old",
					LastError:      "RETRY_NONORDINARY_OLD_FAILURE_FOR_TEST_ONLY",
				}
				if err := db.Create(&oldFailure).Error; err != nil {
					t.Fatalf("create older ordinary failed predecessor: %v", err)
				}
				newerNonOrdinary := model.TaskRun{
					TaskID:         task.ID,
					NodeIDSnapshot: node.ID,
					TriggerType:    trigger,
					Status:         model.TaskRunStatusSuccess,
					ChainRunID:     "retry-nonordinary-new",
				}
				if err := db.Create(&newerNonOrdinary).Error; err != nil {
					t.Fatalf("create newer %s predecessor: %v", trigger, err)
				}
				manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
				shutdownManagerOnCleanup(t, manager)
				manager.shuttingDown.Store(true)
				if err := manager.LoadSchedules(context.Background()); err != nil {
					t.Fatalf("reconcile %s predecessor: %v", trigger, err)
				}
				var retryEffects int64
				if err := db.Model(&model.TaskRunEffect{}).
					Where("task_run_id = ? AND effect_type = ?", oldFailure.ID, model.TaskRunEffectTypeRetry).
					Count(&retryEffects).Error; err != nil {
					t.Fatalf("count retry effect for %s predecessor: %v", trigger, err)
				}
				if retryEffects != 1 {
					t.Fatalf("retry effects for ordinary predecessor before later %s=%d, want 1", trigger, retryEffects)
				}
			})
		}
	})
}

func TestRetryRecoveryBatchBoundariesSQLite(t *testing.T) {
	runRetryRecoveryBatchBoundaries(t, openManagerTestDB)
}

func TestRetryRecoveryBatchBoundariesPostgres(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	runRetryRecoveryBatchBoundaries(t, func(t *testing.T) *gorm.DB {
		return openTaskTerminalPostgresDB(t, dsn)
	})
}

func seedReadyAlertEffects(t *testing.T, db *gorm.DB, count int, status string, nextAttemptAt, claimLeaseUntil *time.Time, claimedBy string) []uint {
	t.Helper()
	node := model.Node{
		Name:      fmt.Sprintf("effect-batch-node-%d", time.Now().UnixNano()),
		Host:      "127.0.0.1",
		Port:      22,
		Username:  "root",
		AuthType:  "key",
		BackupDir: fmt.Sprintf("/tmp/xirang-effect-batch-%d", time.Now().UnixNano()),
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("create effect batch node: %v", err)
	}
	task := model.Task{
		Name:         fmt.Sprintf("effect-batch-task-%d", time.Now().UnixNano()),
		NodeID:       node.ID,
		ExecutorType: "local",
		Status:       string(StatusFailed),
		Enabled:      true,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("create effect batch task: %v", err)
	}
	run := model.TaskRun{
		TaskID:         task.ID,
		NodeIDSnapshot: node.ID,
		TriggerType:    "manual",
		Status:         model.TaskRunStatusFailed,
		ChainRunID:     "effect-batch-chain",
	}
	if err := db.Create(&run).Error; err != nil {
		t.Fatalf("create effect batch source: %v", err)
	}
	ids := make([]uint, 0, count)
	for i := range count {
		effect := model.TaskRunEffect{
			TaskRunID:       run.ID,
			EffectKey:       fmt.Sprintf("alert:%d", i),
			EffectType:      model.TaskRunEffectTypeAlert,
			Payload:         "{}",
			Status:          status,
			NextAttemptAt:   nextAttemptAt,
			ClaimedBy:       claimedBy,
			ClaimLeaseUntil: claimLeaseUntil,
		}
		if err := db.Create(&effect).Error; err != nil {
			t.Fatalf("create effect batch row %d: %v", i, err)
		}
		ids = append(ids, effect.ID)
	}
	return ids
}

func runReadyEffectBatchBoundaries(t *testing.T, openDB func(*testing.T) *gorm.DB) {
	t.Helper()
	for _, count := range []int{taskRunEffectRecoveryBatchSize() - 1, taskRunEffectRecoveryBatchSize(), taskRunEffectRecoveryBatchSize() + 1} {
		t.Run(fmt.Sprintf("ready-count-%d", count), func(t *testing.T) {
			db := openDB(t)
			if err := db.AutoMigrate(&model.RestoreDrillEvidence{}, &model.TaskLog{}, &model.TaskTrafficSample{}, &model.Alert{}); err != nil {
				t.Fatalf("migrate effect recovery support tables: %v", err)
			}
			seedReadyAlertEffects(t, db, count, model.TaskRunEffectStatusPending, nil, nil, "")
			manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
			shutdownManagerOnCleanup(t, manager)
			manager.shuttingDown.Store(true)
			for pass := range 4 {
				if err := manager.LoadSchedules(context.Background()); err != nil {
					t.Fatalf("ready effect recovery pass=%d count=%d: %v", pass, count, err)
				}
			}
			var succeeded int64
			if err := db.Model(&model.TaskRunEffect{}).
				Where("status = ?", model.TaskRunEffectStatusSucceeded).
				Count(&succeeded).Error; err != nil {
				t.Fatalf("count succeeded effects count=%d: %v", count, err)
			}
			if succeeded != int64(count) {
				t.Fatalf("succeeded effect count=%d, want %d", succeeded, count)
			}
		})
	}

	t.Run("future-effects-are-not-ready", func(t *testing.T) {
		db := openDB(t)
		if err := db.AutoMigrate(&model.RestoreDrillEvidence{}, &model.TaskLog{}, &model.TaskTrafficSample{}, &model.Alert{}); err != nil {
			t.Fatalf("migrate future effect support tables: %v", err)
		}
		future := time.Now().UTC().Add(time.Hour)
		seedReadyAlertEffects(t, db, taskRunEffectRecoveryBatchSize(), model.TaskRunEffectStatusPending, &future, nil, "")
		manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
		shutdownManagerOnCleanup(t, manager)
		manager.shuttingDown.Store(true)
		if err := manager.LoadSchedules(context.Background()); err != nil {
			t.Fatalf("future effect recovery: %v", err)
		}
		var pending int64
		if err := db.Model(&model.TaskRunEffect{}).
			Where("status = ?", model.TaskRunEffectStatusPending).
			Count(&pending).Error; err != nil {
			t.Fatalf("count future pending effects: %v", err)
		}
		if pending != int64(taskRunEffectRecoveryBatchSize()) {
			t.Fatalf("future pending effect count=%d, want %d", pending, taskRunEffectRecoveryBatchSize())
		}
	})

	t.Run("foreign-live-claims-are-not-ready", func(t *testing.T) {
		db := openDB(t)
		if err := db.AutoMigrate(&model.RestoreDrillEvidence{}, &model.TaskLog{}, &model.TaskTrafficSample{}, &model.Alert{}); err != nil {
			t.Fatalf("migrate foreign effect support tables: %v", err)
		}
		future := time.Now().UTC().Add(time.Hour)
		ids := seedReadyAlertEffects(t, db, taskRunEffectRecoveryBatchSize(), model.TaskRunEffectStatusRunning, nil, &future, "foreign-effect-owner")
		manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
		shutdownManagerOnCleanup(t, manager)
		manager.shuttingDown.Store(true)
		if err := manager.LoadSchedules(context.Background()); err != nil {
			t.Fatalf("foreign effect recovery: %v", err)
		}
		var running int64
		if err := db.Model(&model.TaskRunEffect{}).
			Where("status = ? AND claimed_by = ?", model.TaskRunEffectStatusRunning, "foreign-effect-owner").
			Count(&running).Error; err != nil {
			t.Fatalf("count foreign running effects: %v", err)
		}
		if running != int64(len(ids)) {
			t.Fatalf("foreign running effect count=%d, want %d", running, len(ids))
		}
	})
}

func TestReadyEffectBatchBoundariesSQLite(t *testing.T) {
	runReadyEffectBatchBoundaries(t, openManagerTestDB)
}

func TestReadyEffectBatchBoundariesPostgres(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	runReadyEffectBatchBoundaries(t, func(t *testing.T) *gorm.DB {
		return openTaskTerminalPostgresDB(t, dsn)
	})
}

func seedPendingDurableRuns(t *testing.T, db *gorm.DB, count, foreign int) []model.TaskRun {
	t.Helper()
	if foreign > count {
		t.Fatalf("foreign pending rows=%d exceeds count=%d", foreign, count)
	}
	runs := make([]model.TaskRun, 0, count)
	future := time.Now().UTC().Add(time.Hour)
	for i := range count {
		node := model.Node{
			Name:      fmt.Sprintf("pending-batch-node-%d-%d", time.Now().UnixNano(), i),
			Host:      "127.0.0.1",
			Port:      22,
			Username:  "root",
			AuthType:  "key",
			BackupDir: fmt.Sprintf("/tmp/xirang-pending-batch-%d-%d", time.Now().UnixNano(), i),
		}
		if err := db.Create(&node).Error; err != nil {
			t.Fatalf("create pending batch node %d: %v", i, err)
		}
		task := model.Task{
			Name:         fmt.Sprintf("pending-batch-task-%d-%d", time.Now().UnixNano(), i),
			NodeID:       node.ID,
			ExecutorType: "local",
			Status:       string(StatusPending),
			Enabled:      true,
		}
		if err := db.Create(&task).Error; err != nil {
			t.Fatalf("create pending batch task %d: %v", i, err)
		}
		run := model.TaskRun{
			TaskID:         task.ID,
			NodeIDSnapshot: node.ID,
			TriggerType:    "auto",
			Status:         model.TaskRunStatusPending,
		}
		if i < foreign {
			run.ExecutionOwnerID = "foreign-pending-owner"
			run.ExecutionLeaseUntil = &future
		}
		if err := db.Create(&run).Error; err != nil {
			t.Fatalf("create pending batch run %d: %v", i, err)
		}
		runs = append(runs, run)
	}
	return runs
}

func seedMissingPendingDurableRun(t *testing.T, db *gorm.DB, taskID, nodeID uint, owner string, lease time.Time) model.TaskRun {
	t.Helper()
	if db.Name() == "postgres" {
		if err := db.Exec("ALTER TABLE task_runs DROP CONSTRAINT IF EXISTS fk_task_runs_task").Error; err != nil {
			t.Fatalf("drop task-run authority constraint for legacy fixture: %v", err)
		}
	}
	now := time.Now().UTC()
	if err := db.Exec(`INSERT INTO task_runs
		(task_id, node_id_snapshot, trigger_type, status, execution_owner_id, execution_lease_until, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		taskID, nodeID, "auto", model.TaskRunStatusPending, owner, lease, now, now).Error; err != nil {
		t.Fatalf("create missing-task pending run: %v", err)
	}
	var run model.TaskRun
	if err := db.Where("task_id = ? AND execution_owner_id = ? AND status = ?",
		taskID, owner, model.TaskRunStatusPending).Order("id DESC").First(&run).Error; err != nil {
		t.Fatalf("load missing-task pending run: %v", err)
	}
	return run
}

func waitForTaskRunsTerminal(t *testing.T, db *gorm.DB, runs []model.TaskRun) {
	t.Helper()
	ids := make([]uint, 0, len(runs))
	for _, run := range runs {
		ids = append(ids, run.ID)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		var active int64
		if err := db.Model(&model.TaskRun{}).
			Where("id IN ? AND status IN ?", ids, model.TaskRunActiveStatuses()).
			Count(&active).Error; err != nil {
			t.Fatalf("count pending batch active runs: %v", err)
		}
		if active == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("pending batch still has %d active runs", active)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func runPendingDurableBatchBoundaries(t *testing.T, openDB func(*testing.T) *gorm.DB) {
	t.Helper()
	for _, count := range []int{taskRunRecoveryBatchSize - 1, taskRunRecoveryBatchSize, taskRunRecoveryBatchSize + 1} {
		t.Run(fmt.Sprintf("pending-count-%d", count), func(t *testing.T) {
			db := openDB(t)
			if err := db.AutoMigrate(&model.RestoreDrillEvidence{}, &model.TaskLog{}, &model.TaskTrafficSample{}, &model.Alert{}); err != nil {
				t.Fatalf("migrate pending recovery support tables: %v", err)
			}
			runs := seedPendingDurableRuns(t, db, count, 0)
			manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
			shutdownManagerOnCleanup(t, manager)
			if err := manager.LoadSchedules(context.Background()); err != nil {
				t.Fatalf("first pending recovery pass count=%d: %v", count, err)
			}
			if count > taskRunRecoveryBatchSize {
				if err := manager.LoadSchedules(context.Background()); err != nil {
					t.Fatalf("repeat pending recovery pass count=%d: %v", count, err)
				}
			}
			waitForTaskRunsTerminal(t, db, runs)
			var succeeded int64
			if err := db.Model(&model.TaskRun{}).
				Where("id IN ? AND status = ?", idsForTaskRuns(runs), model.TaskRunStatusSuccess).
				Count(&succeeded).Error; err != nil {
				t.Fatalf("count completed pending runs count=%d: %v", count, err)
			}
			if succeeded != int64(count) {
				t.Fatalf("completed pending runs=%d, want %d", succeeded, count)
			}
		})
	}

	t.Run("foreign-live-claims-do-not-starve-eligible-row", func(t *testing.T) {
		db := openDB(t)
		if err := db.AutoMigrate(&model.RestoreDrillEvidence{}, &model.TaskLog{}, &model.TaskTrafficSample{}, &model.Alert{}); err != nil {
			t.Fatalf("migrate foreign pending support tables: %v", err)
		}
		runs := seedPendingDurableRuns(t, db, taskRunRecoveryBatchSize+1, taskRunRecoveryBatchSize)
		manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
		shutdownManagerOnCleanup(t, manager)
		if err := manager.LoadSchedules(context.Background()); err != nil {
			t.Fatalf("foreign pending recovery: %v", err)
		}
		waitForTaskRunsTerminal(t, db, runs[taskRunRecoveryBatchSize:])
		var current model.TaskRun
		if err := db.First(&current, runs[taskRunRecoveryBatchSize].ID).Error; err != nil {
			t.Fatalf("load eligible trailing pending run: %v", err)
		}
		if current.Status != model.TaskRunStatusSuccess {
			t.Fatalf("eligible trailing pending run status=%q, want success", current.Status)
		}
		var foreignActive int64
		if err := db.Model(&model.TaskRun{}).
			Where("id IN ? AND status = ? AND execution_owner_id = ?", idsForTaskRuns(runs[:taskRunRecoveryBatchSize]), model.TaskRunStatusPending, "foreign-pending-owner").
			Count(&foreignActive).Error; err != nil {
			t.Fatalf("count foreign pending runs: %v", err)
		}
		if foreignActive != taskRunRecoveryBatchSize {
			t.Fatalf("foreign pending runs=%d, want %d", foreignActive, taskRunRecoveryBatchSize)
		}
	})

	t.Run("self-owned-live-claims-are-not-relaunched", func(t *testing.T) {
		db := openDB(t)
		if err := db.AutoMigrate(&model.RestoreDrillEvidence{}, &model.TaskLog{}, &model.TaskTrafficSample{}, &model.Alert{}); err != nil {
			t.Fatalf("migrate self-owned pending support tables: %v", err)
		}
		runs := seedPendingDurableRuns(t, db, taskRunRecoveryBatchSize+1, 0)
		exec := &successExecutor{}
		manager := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, nil, 8, 90)
		shutdownManagerOnCleanup(t, manager)
		future := time.Now().UTC().Add(time.Hour)
		if err := db.Model(&model.TaskRun{}).
			Where("id IN ?", idsForTaskRuns(runs[:taskRunRecoveryBatchSize])).
			Updates(map[string]any{
				"execution_owner_id":    manager.executionOwnerID,
				"execution_lease_until": future,
			}).Error; err != nil {
			t.Fatalf("mark self-owned pending runs: %v", err)
		}
		if err := manager.LoadSchedules(context.Background()); err != nil {
			t.Fatalf("self-owned pending recovery: %v", err)
		}
		trailing := runs[taskRunRecoveryBatchSize:]
		waitForTaskRunsTerminal(t, db, trailing)
		var current model.TaskRun
		if err := db.First(&current, trailing[0].ID).Error; err != nil {
			t.Fatalf("load self-owned trailing pending run: %v", err)
		}
		if current.Status != model.TaskRunStatusSuccess {
			t.Fatalf("self-owned trailing pending run status=%q, want success", current.Status)
		}
		if exec.Calls() != 1 {
			t.Fatalf("self-owned pending run executor calls=%d, want 1", exec.Calls())
		}
		var selfActive int64
		if err := db.Model(&model.TaskRun{}).
			Where("id IN ? AND status = ? AND execution_owner_id = ?",
				idsForTaskRuns(runs[:taskRunRecoveryBatchSize]), model.TaskRunStatusPending, manager.executionOwnerID).
			Count(&selfActive).Error; err != nil {
			t.Fatalf("count self-owned pending runs: %v", err)
		}
		if selfActive != taskRunRecoveryBatchSize {
			t.Fatalf("self-owned pending runs=%d, want %d", selfActive, taskRunRecoveryBatchSize)
		}
	})

	t.Run("expired-foreign-disabled-archived-missing-cancel-and-progress", func(t *testing.T) {
		db := openDB(t)
		if err := db.AutoMigrate(&model.RestoreDrillEvidence{}, &model.TaskLog{}, &model.TaskTrafficSample{}, &model.Alert{}); err != nil {
			t.Fatalf("migrate expired foreign pending support tables: %v", err)
		}
		expired := time.Now().UTC().Add(-time.Hour)
		blocked := seedPendingDurableRuns(t, db, 2, 2)
		if err := db.Model(&model.TaskRun{}).
			Where("id IN ?", idsForTaskRuns(blocked)).
			Update("execution_lease_until", expired).Error; err != nil {
			t.Fatalf("expire foreign pending leases: %v", err)
		}
		if err := db.Model(&model.Task{}).Where("id = ?", blocked[0].TaskID).Update("enabled", false).Error; err != nil {
			t.Fatalf("disable pending task: %v", err)
		}
		archivedAt := time.Now().UTC()
		if err := db.Model(&model.Task{}).Where("id = ?", blocked[1].TaskID).Update("archived_at", archivedAt).Error; err != nil {
			t.Fatalf("archive pending task: %v", err)
		}
		var maxTaskID uint
		if err := db.Model(&model.Task{}).Select("COALESCE(MAX(id), 0)").Scan(&maxTaskID).Error; err != nil {
			t.Fatalf("find missing-task id: %v", err)
		}
		missing := seedMissingPendingDurableRun(t, db, maxTaskID+1000, blocked[1].NodeIDSnapshot,
			"foreign-pending-owner", expired)
		trailing := seedPendingDurableRuns(t, db, 1, 0)[0]
		exec := &successExecutor{}
		manager := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, nil, 8, 90)
		shutdownManagerOnCleanup(t, manager)
		if err := manager.LoadSchedules(context.Background()); err != nil {
			t.Fatalf("expired foreign pending recovery: %v", err)
		}
		waitForTaskRunsTerminal(t, db, []model.TaskRun{trailing})
		for _, run := range []model.TaskRun{blocked[0], blocked[1], missing} {
			var canceled model.TaskRun
			if err := db.First(&canceled, run.ID).Error; err != nil {
				t.Fatalf("reload expired foreign run %d: %v", run.ID, err)
			}
			if canceled.Status != model.TaskRunStatusCanceled || canceled.ExecutionOwnerID != "" ||
				canceled.ExecutionLeaseUntil != nil {
				t.Fatalf("expired foreign run %d not canceled/cleared: %+v", run.ID, canceled)
			}
		}
		var completed model.TaskRun
		if err := db.First(&completed, trailing.ID).Error; err != nil {
			t.Fatalf("reload eligible trailing run: %v", err)
		}
		if completed.Status != model.TaskRunStatusSuccess {
			t.Fatalf("eligible trailing run status=%q, want success", completed.Status)
		}
		if exec.Calls() != 1 {
			t.Fatalf("eligible trailing run executor calls=%d, want 1", exec.Calls())
		}
	})
	t.Run("already-handled-reservations-do-not-starve-eligible-row", func(t *testing.T) {
		db := openDB(t)
		if err := db.AutoMigrate(&model.RestoreDrillEvidence{}, &model.TaskLog{}, &model.TaskTrafficSample{}, &model.Alert{}); err != nil {
			t.Fatalf("migrate handled pending support tables: %v", err)
		}
		handledCount := taskRunRecoveryBatchSize*2 + 1
		runs := seedPendingDurableRuns(t, db, handledCount+1, 0)
		exec := &successExecutor{}
		manager := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, nil, 8, 90)
		shutdownManagerOnCleanup(t, manager)
		for _, run := range runs[:handledCount] {
			ownership := &pendingRunOwnership{}
			taskID := run.TaskID
			manager.pendingRuns.Store(taskID, ownership)
			t.Cleanup(func() { manager.pendingRuns.CompareAndDelete(taskID, ownership) })
		}
		for pass := range 3 {
			if err := manager.LoadSchedules(context.Background()); err != nil {
				t.Fatalf("handled pending recovery pass %d: %v", pass+1, err)
			}
		}
		trailing := runs[handledCount:]
		waitForTaskRunsTerminal(t, db, trailing)
		var current model.TaskRun
		if err := db.First(&current, trailing[0].ID).Error; err != nil {
			t.Fatalf("load trailing handled pending run: %v", err)
		}
		if current.Status != model.TaskRunStatusSuccess {
			t.Fatalf("trailing handled pending run status=%q, want success", current.Status)
		}
		if exec.Calls() != 1 {
			t.Fatalf("trailing handled pending run executor calls=%d, want 1", exec.Calls())
		}
		var stillPending int64
		if err := db.Model(&model.TaskRun{}).
			Where("id IN ? AND status = ?", idsForTaskRuns(runs[:handledCount]), model.TaskRunStatusPending).
			Count(&stillPending).Error; err != nil {
			t.Fatalf("count handled pending runs: %v", err)
		}
		if stillPending != int64(handledCount) {
			t.Fatalf("handled pending runs=%d, want %d", stillPending, handledCount)
		}
	})
}

func idsForTaskRuns(runs []model.TaskRun) []uint {
	ids := make([]uint, 0, len(runs))
	for _, run := range runs {
		ids = append(ids, run.ID)
	}
	return ids
}

func TestPendingDurableBatchBoundariesSQLite(t *testing.T) {
	runPendingDurableBatchBoundaries(t, openManagerTestDB)
}

func TestPendingDurableBatchBoundariesPostgres(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	runPendingDurableBatchBoundaries(t, func(t *testing.T) *gorm.DB {
		return openTaskTerminalPostgresDB(t, dsn)
	})
}

func seedUniqueCancellationTask(t *testing.T, db *gorm.DB, status TaskStatus) model.Task {
	t.Helper()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	node := model.Node{
		Name:      "cancellation-node-" + suffix,
		Host:      "127.0.0.1",
		Port:      22,
		Username:  "root",
		AuthType:  "key",
		BackupDir: "/tmp/xirang-cancellation-" + suffix,
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("create cancellation node: %v", err)
	}
	task := model.Task{
		Name:         "cancellation-task-" + suffix,
		NodeID:       node.ID,
		ExecutorType: "local",
		Status:       string(status),
		Enabled:      true,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("create cancellation task: %v", err)
	}
	return task
}

func runCancellationOwnerFence(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.AutoMigrate(&model.RestoreDrillEvidence{}, &model.TaskLog{}, &model.TaskTrafficSample{}, &model.Alert{}); err != nil {
		t.Fatalf("migrate cancellation support tables: %v", err)
	}
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)
	manager.executionOwnerID = "owner-a"

	orphanFuture := time.Now().UTC().Add(time.Hour)

	orphanTask := seedUniqueCancellationTask(t, db, StatusRunning)
	orphanRun := model.TaskRun{
		TaskID:              orphanTask.ID,
		NodeIDSnapshot:      orphanTask.NodeID,
		TriggerType:         "manual",
		Status:              model.TaskRunStatusRunning,
		ExecutionOwnerID:    "owner-b",
		ExecutionLeaseUntil: &orphanFuture,
	}
	if err := db.Create(&orphanRun).Error; err != nil {
		t.Fatalf("create foreign-owned orphan run: %v", err)
	}
	if err := manager.Cancel(orphanTask.ID); !errors.Is(err, errTaskCancelConflict) {
		t.Fatalf("foreign-owned orphan cancellation error=%v, want %v", err, errTaskCancelConflict)
	}
	var orphanUnchanged model.TaskRun
	if err := db.First(&orphanUnchanged, orphanRun.ID).Error; err != nil {
		t.Fatalf("reload foreign-owned orphan run: %v", err)
	}
	if orphanUnchanged.Status != model.TaskRunStatusRunning || orphanUnchanged.ExecutionOwnerID != "owner-b" ||
		orphanUnchanged.ExecutionLeaseUntil == nil {
		t.Fatalf("foreign-owned orphan run changed: %+v", orphanUnchanged)
	}
	var orphanTaskUnchanged model.Task
	if err := db.First(&orphanTaskUnchanged, orphanTask.ID).Error; err != nil {
		t.Fatalf("reload foreign-owned orphan task: %v", err)
	}
	if ParseStatus(orphanTaskUnchanged.Status) != StatusRunning {
		t.Fatalf("foreign-owned orphan task status=%q, want %q", orphanTaskUnchanged.Status, StatusRunning)
	}
	task := seedUniqueCancellationTask(t, db, StatusPending)
	future := time.Now().UTC().Add(time.Hour)
	run := model.TaskRun{
		TaskID:              task.ID,
		NodeIDSnapshot:      task.NodeID,
		TriggerType:         "manual",
		Status:              model.TaskRunStatusPending,
		ExecutionOwnerID:    "owner-b",
		ExecutionLeaseUntil: &future,
	}
	if err := db.Create(&run).Error; err != nil {
		t.Fatalf("create foreign-owned pending run: %v", err)
	}
	if err := manager.cancelTaskRunBeforeExecutor(task.ID, run.ID, "STALE_OWNER_CANCEL_FOR_TEST_ONLY"); !errors.Is(err, errTaskRunNotOwner) {
		t.Fatalf("stale owner cancellation error=%v, want %v", err, errTaskRunNotOwner)
	}
	var unchanged model.TaskRun
	if err := db.First(&unchanged, run.ID).Error; err != nil {
		t.Fatalf("reload foreign-owned pending run: %v", err)
	}
	if unchanged.Status != model.TaskRunStatusPending || unchanged.ExecutionOwnerID != "owner-b" ||
		unchanged.ExecutionLeaseUntil == nil {
		t.Fatalf("stale owner mutated pending run: %+v", unchanged)
	}

	taskRunning := seedUniqueCancellationTask(t, db, StatusRunning)
	running := model.TaskRun{
		TaskID:              taskRunning.ID,
		NodeIDSnapshot:      taskRunning.NodeID,
		TriggerType:         "manual",
		Status:              model.TaskRunStatusRunning,
		ExecutionOwnerID:    "owner-b",
		ExecutionLeaseUntil: &future,
	}
	if err := db.Create(&running).Error; err != nil {
		t.Fatalf("create foreign-owned running run: %v", err)
	}
	if err := manager.cancelTaskExecutionBeforeExecutor(running.ID, taskRunning.ID, taskRunning.NodeID, nil, "STALE_RUNNING_OWNER_CANCEL_FOR_TEST_ONLY"); !errors.Is(err, errTaskRunNotOwner) {
		t.Fatalf("stale running owner cancellation error=%v, want %v", err, errTaskRunNotOwner)
	}
	var runningUnchanged model.TaskRun
	if err := db.First(&runningUnchanged, running.ID).Error; err != nil {
		t.Fatalf("reload foreign-owned running run: %v", err)
	}
	if runningUnchanged.Status != model.TaskRunStatusRunning || runningUnchanged.ExecutionOwnerID != "owner-b" ||
		runningUnchanged.ExecutionLeaseUntil == nil {
		t.Fatalf("stale running owner mutated run: %+v", runningUnchanged)
	}

	if err := db.Model(&model.TaskRun{}).Where("id = ?", run.ID).Updates(map[string]interface{}{
		"execution_owner_id": manager.executionOwnerID,
	}).Error; err != nil {
		t.Fatalf("transfer pending run ownership for valid cancellation: %v", err)
	}
	if err := manager.cancelTaskRunBeforeExecutor(task.ID, run.ID, "VALID_OWNER_CANCEL_FOR_TEST_ONLY"); err != nil {
		t.Fatalf("valid owner cancellation: %v", err)
	}
	var canceled model.TaskRun
	if err := db.First(&canceled, run.ID).Error; err != nil {
		t.Fatalf("reload valid canceled run: %v", err)
	}
	if canceled.Status != model.TaskRunStatusCanceled || canceled.ExecutionOwnerID != "" || canceled.ExecutionLeaseUntil != nil {
		t.Fatalf("valid owner cancellation did not clear lease: %+v", canceled)
	}

	var directTask model.Task
	if err := db.First(&directTask, task.ID).Error; err != nil {
		t.Fatalf("reload direct manual-cancel task: %v", err)
	}
	if ParseStatus(directTask.Status) != StatusPending {
		t.Fatalf("direct manual-cancel task status=%q, want %q", directTask.Status, StatusPending)
	}

	terminalTask := seedUniqueCancellationTask(t, db, StatusSuccess)
	terminalRun := model.TaskRun{
		TaskID:              terminalTask.ID,
		NodeIDSnapshot:      terminalTask.NodeID,
		TriggerType:         "manual",
		Status:              model.TaskRunStatusPending,
		ExecutionOwnerID:    manager.executionOwnerID,
		ExecutionLeaseUntil: &future,
	}
	if err := db.Create(&terminalRun).Error; err != nil {
		t.Fatalf("create terminal manual-cancel run: %v", err)
	}
	if err := manager.cancelPendingDurableRun(context.Background(), terminalRun.ID, terminalTask.ID, "MANUAL_TERMINAL_CANCEL_FOR_TEST_ONLY"); err != nil {
		t.Fatalf("terminal manual cancellation: %v", err)
	}
	var preservedTerminalTask model.Task
	if err := db.First(&preservedTerminalTask, terminalTask.ID).Error; err != nil {
		t.Fatalf("reload terminal manual-cancel task: %v", err)
	}
	if ParseStatus(preservedTerminalTask.Status) != StatusSuccess {
		t.Fatalf("terminal manual-cancel task status=%q, want %q", preservedTerminalTask.Status, StatusSuccess)
	}

	retryTask := seedUniqueCancellationTask(t, db, StatusRetrying)
	if err := db.Model(&model.Task{}).Where("id = ?", retryTask.ID).Update("next_run_at", future).Error; err != nil {
		t.Fatalf("set queued retry next run: %v", err)
	}
	retrySource := model.TaskRun{
		TaskID:         retryTask.ID,
		NodeIDSnapshot: retryTask.NodeID,
		TriggerType:    "manual",
		Status:         model.TaskRunStatusFailed,
		ChainRunID:     "retry-cancel-source",
		LastError:      "RETRY_CANCEL_SOURCE_FAILURE_FOR_TEST_ONLY",
	}
	if err := db.Create(&retrySource).Error; err != nil {
		t.Fatalf("create queued retry predecessor: %v", err)
	}
	retryEffect := model.TaskRunEffect{
		TaskRunID:  retrySource.ID,
		EffectKey:  "retry",
		EffectType: model.TaskRunEffectTypeRetry,
		Payload:    "{}",
		Status:     model.TaskRunEffectStatusSucceeded,
	}
	if err := db.Create(&retryEffect).Error; err != nil {
		t.Fatalf("create succeeded queued retry effect: %v", err)
	}
	queuedRetry := model.TaskRun{
		TaskID:              retryTask.ID,
		NodeIDSnapshot:      retryTask.NodeID,
		TriggerType:         "retry",
		Status:              model.TaskRunStatusPending,
		ExecutionOwnerID:    manager.executionOwnerID,
		ExecutionLeaseUntil: &future,
	}
	if err := db.Create(&queuedRetry).Error; err != nil {
		t.Fatalf("create queued retry run: %v", err)
	}
	if err := manager.cancelPendingDurableRun(context.Background(), queuedRetry.ID, retryTask.ID, "QUEUED_RETRY_CANCEL_FOR_TEST_ONLY"); err != nil {
		t.Fatalf("queued retry cancellation: %v", err)
	}
	var canceledRetry model.TaskRun
	if err := db.First(&canceledRetry, queuedRetry.ID).Error; err != nil {
		t.Fatalf("reload canceled queued retry run: %v", err)
	}
	if canceledRetry.Status != model.TaskRunStatusCanceled || canceledRetry.ExecutionOwnerID != "" ||
		canceledRetry.ExecutionLeaseUntil != nil {
		t.Fatalf("queued retry cancellation did not clear lease: %+v", canceledRetry)
	}
	var canceledRetryTask model.Task
	if err := db.First(&canceledRetryTask, retryTask.ID).Error; err != nil {
		t.Fatalf("reload canceled queued retry task: %v", err)
	}
	if ParseStatus(canceledRetryTask.Status) != StatusCanceled || canceledRetryTask.NextRunAt != nil {
		t.Fatalf("queued retry cancellation aggregate=%+v, want canceled without next run", canceledRetryTask)
	}
	var activeRetryRuns int64
	if err := db.Model(&model.TaskRun{}).
		Where("task_id = ? AND status IN ?", retryTask.ID, model.TaskRunActiveStatuses()).
		Count(&activeRetryRuns).Error; err != nil {
		t.Fatalf("count active queued retry runs: %v", err)
	}
	if activeRetryRuns != 0 {
		t.Fatalf("active queued retry runs=%d, want 0", activeRetryRuns)
	}
	var pendingRetryEffects int64
	if err := db.Model(&model.TaskRunEffect{}).
		Where("task_run_id = ? AND effect_type = ? AND status <> ?",
			retrySource.ID, model.TaskRunEffectTypeRetry, model.TaskRunEffectStatusSucceeded).
		Count(&pendingRetryEffects).Error; err != nil {
		t.Fatalf("count queued retry intents: %v", err)
	}
	if pendingRetryEffects != 0 {
		t.Fatalf("queued retry intents=%d, want 0", pendingRetryEffects)
	}

	taskByUser := seedUniqueCancellationTask(t, db, StatusPending)
	userRun := model.TaskRun{
		TaskID:              taskByUser.ID,
		NodeIDSnapshot:      taskByUser.NodeID,
		TriggerType:         "manual",
		Status:              model.TaskRunStatusPending,
		ExecutionOwnerID:    manager.executionOwnerID,
		ExecutionLeaseUntil: &future,
	}
	if err := db.Create(&userRun).Error; err != nil {
		t.Fatalf("create user-cancel pending run: %v", err)
	}
	if err := manager.Cancel(taskByUser.ID); err != nil {
		t.Fatalf("user cancellation: %v", err)
	}
	var userCanceled model.TaskRun
	if err := db.First(&userCanceled, userRun.ID).Error; err != nil {
		t.Fatalf("reload user-canceled run: %v", err)
	}
	if userCanceled.Status != model.TaskRunStatusCanceled || userCanceled.ExecutionOwnerID != "" || userCanceled.ExecutionLeaseUntil != nil {
		t.Fatalf("user cancellation did not clear lease: %+v", userCanceled)
	}
	var canceledTask model.Task
	if err := db.First(&canceledTask, taskByUser.ID).Error; err != nil {
		t.Fatalf("reload user-canceled task: %v", err)
	}
	if ParseStatus(canceledTask.Status) != StatusCanceled {
		t.Fatalf("user-canceled task status=%q, want %q", canceledTask.Status, StatusCanceled)
	}
}

func TestCancellationOwnerFenceAndLeaseClearSQLite(t *testing.T) {
	runCancellationOwnerFence(t, openManagerTestDB(t))
}

func TestCancellationOwnerFenceAndLeaseClearPostgres(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	runCancellationOwnerFence(t, openTaskTerminalPostgresDB(t, dsn))
}
