package task

import (
	"context"
	"encoding/json"
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
	manager.executionLeaseDuration = 45 * time.Millisecond
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
	case <-time.After(3 * time.Second):
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
	case <-time.After(3 * time.Second):
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
