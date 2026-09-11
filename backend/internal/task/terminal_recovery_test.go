package task

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

// TestInterruptedOrdinaryTaskReconstructsAndDrainsDurableEffects exercises the
// real manager recovery path: an expired ordinary execution is terminalized,
// its downstream effect claim is interrupted after the transaction commits,
// and a reconstructed manager drains that effect exactly once.
func TestInterruptedOrdinaryTaskReconstructsAndDrainsDurableEffects(t *testing.T) {
	db := openManagerTestDB(t)
	if err := db.AutoMigrate(&model.AlertDelivery{}); err != nil {
		t.Fatalf("migrate terminal recovery alert deliveries: %v", err)
	}
	upstream := seedTaskForManagerTest(t, db)
	downstream := model.Task{
		Name:            "task-manager-downstream-recovery",
		NodeID:          upstream.NodeID,
		DependsOnTaskID: &upstream.ID,
		ExecutorType:    "rsync",
		Status:          string(StatusPending),
		RsyncSource:     "/tmp/downstream-src",
		RsyncTarget:     "/tmp/downstream-dst",
	}
	if err := db.Create(&downstream).Error; err != nil {
		t.Fatalf("create downstream task: %v", err)
	}
	startedAt := time.Now().UTC().Add(-2 * time.Minute).Truncate(time.Millisecond)
	expiredLease := time.Now().UTC().Add(-time.Minute)
	if err := db.Model(&model.Task{}).Where("id = ?", upstream.ID).Updates(map[string]interface{}{
		"status":      string(StatusRunning),
		"last_run_at": &startedAt,
	}).Error; err != nil {
		t.Fatalf("mark upstream task running: %v", err)
	}
	run := model.TaskRun{
		TaskID: upstream.ID, NodeIDSnapshot: upstream.NodeID, TriggerType: "manual", Status: model.TaskRunStatusRunning,
		ExecutionOwnerID: "lost-process-owner", ExecutionLeaseUntil: &expiredLease, StartedAt: &startedAt,
	}
	if err := db.Create(&run).Error; err != nil {
		t.Fatalf("create interrupted task run: %v", err)
	}

	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)
	injected := atomic.Bool{}
	callbackName := fmt.Sprintf("test:ordinary-effect-claim-interruption-%d", run.ID)
	injectedErr := errors.New("INTERNAL_ORDINARY_EFFECT_CLAIM_INTERRUPTION")
	if err := db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == "task_run_effects" && injected.CompareAndSwap(false, true) {
			_ = tx.AddError(injectedErr)
		}
	}); err != nil {
		t.Fatalf("register effect interruption: %v", err)
	}
	if err := manager.reconcileExpiredOrdinaryRuns(context.Background()); err != nil {
		t.Fatalf("reconcile interrupted ordinary run: %v", err)
	}
	if !injected.Load() {
		t.Fatal("effect claim interruption was not injected")
	}
	if err := db.Callback().Update().Remove(callbackName); err != nil {
		t.Fatalf("remove effect interruption: %v", err)
	}

	var recoveredTask model.Task
	var recoveredRun model.TaskRun
	if err := db.First(&recoveredTask, upstream.ID).Error; err != nil {
		t.Fatalf("reload recovered task: %v", err)
	}
	if err := db.First(&recoveredRun, run.ID).Error; err != nil {
		t.Fatalf("reload recovered run: %v", err)
	}
	if recoveredTask.Status != string(StatusFailed) || recoveredRun.Status != model.TaskRunStatusFailed {
		t.Fatalf("interrupted pair not terminalized coherently: Task=%q TaskRun=%q", recoveredTask.Status, recoveredRun.Status)
	}
	if recoveredRun.ExecutionOwnerID != "" || recoveredRun.ExecutionLeaseUntil != nil {
		t.Fatalf("recovered run retained stale owner/lease: owner=%q lease=%v", recoveredRun.ExecutionOwnerID, recoveredRun.ExecutionLeaseUntil)
	}
	var effect model.TaskRunEffect
	if err := db.Where("task_run_id = ?", run.ID).First(&effect).Error; err != nil {
		t.Fatalf("load committed pending effect: %v", err)
	}
	if effect.Status != model.TaskRunEffectStatusPending {
		t.Fatalf("interrupted effect status=%q, want pending", effect.Status)
	}
	var downstreamRuns int64
	if err := db.Model(&model.TaskRun{}).Where("task_id = ? AND upstream_task_run_id = ?", downstream.ID, run.ID).Count(&downstreamRuns).Error; err != nil {
		t.Fatalf("count downstream runs before recovery: %v", err)
	}
	if downstreamRuns != 0 {
		t.Fatalf("interrupted effect unexpectedly created %d downstream runs", downstreamRuns)
	}

	restarted := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, restarted)
	if err := restarted.drainReadyTaskRunEffects(context.Background()); err != nil {
		t.Fatalf("drain committed effect after manager reconstruction: %v", err)
	}
	if err := restarted.drainReadyTaskRunEffects(context.Background()); err != nil {
		t.Fatalf("repeat durable effect drain: %v", err)
	}
	if err := db.First(&effect, effect.ID).Error; err != nil {
		t.Fatalf("reload drained effect: %v", err)
	}
	if effect.Status != model.TaskRunEffectStatusSucceeded {
		t.Fatalf("reconstructed manager left effect status=%q", effect.Status)
	}
	if err := db.Model(&model.TaskRun{}).Where("task_id = ? AND upstream_task_run_id = ?", downstream.ID, run.ID).Count(&downstreamRuns).Error; err != nil {
		t.Fatalf("count downstream runs after recovery: %v", err)
	}
	if downstreamRuns != 1 {
		t.Fatalf("durable downstream effect created %d runs, want exactly one", downstreamRuns)
	}
	var recoveredDownstream model.Task
	if err := db.First(&recoveredDownstream, downstream.ID).Error; err != nil {
		t.Fatalf("reload downstream task: %v", err)
	}
	if recoveredDownstream.Status != string(StatusSkipped) {
		t.Fatalf("downstream task status=%q, want skipped", recoveredDownstream.Status)
	}
}
