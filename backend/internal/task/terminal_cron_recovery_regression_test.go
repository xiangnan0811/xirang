package task

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

func prepareCronRecoveryTestDB(t *testing.T, db *gorm.DB) {
	t.Helper()
	// The task manager's asynchronous log/sample workers and terminal effect
	// path use these tables even when the fixture has no policy or downstream.
	if err := db.AutoMigrate(&model.TaskLog{}, &model.TaskTrafficSample{}, &model.Alert{}); err != nil {
		t.Fatalf("migrate cron recovery support tables: %v", err)
	}
}

func installCronOccurrenceIndex(t *testing.T, db *gorm.DB) {
	t.Helper()
	// Production migration 082 owns this identity boundary. The lightweight
	// test schemas are created with AutoMigrate, so install the same index here.
	if err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_task_runs_cron_occurrence
		ON task_runs(task_id, cron_scheduled_at)
		WHERE trigger_type = 'cron' AND cron_scheduled_at IS NOT NULL`).Error; err != nil {
		t.Fatalf("install cron occurrence uniqueness: %v", err)
	}
}

func seedCronRecoveryTask(t *testing.T, db *gorm.DB, status TaskStatus, skipNext bool) model.Task {
	t.Helper()
	source := t.TempDir()
	target := t.TempDir()
	node := model.Node{
		Name:      fmt.Sprintf("cron-recovery-node-%d", time.Now().UnixNano()),
		Host:      "",
		Port:      22,
		Username:  "root",
		AuthType:  "key",
		BackupDir: fmt.Sprintf("/tmp/cron-recovery-backup-%d", time.Now().UnixNano()),
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("create cron recovery node: %v", err)
	}
	taskEntity := model.Task{
		Name:         fmt.Sprintf("cron-recovery-task-%d", time.Now().UnixNano()),
		NodeID:       node.ID,
		ExecutorType: "rsync",
		Status:       string(status),
		CronSpec:     "@every 1h",
		Enabled:      true,
		SkipNext:     skipNext,
		RsyncSource:  source + "/",
		RsyncTarget:  target,
	}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatalf("create cron recovery task: %v", err)
	}
	return taskEntity
}

func TestPendingCronCrashRecoverySQLite(t *testing.T) {
	runPendingCronCrashRecovery(t, openManagerTestDB(t))
}

func TestPendingCronCrashRecoveryPostgres(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	runPendingCronCrashRecovery(t, openTaskTerminalPostgresDB(t, dsn))
}

func runPendingCronCrashRecovery(t *testing.T, db *gorm.DB) {
	t.Helper()
	prepareCronRecoveryTestDB(t, db)
	installCronOccurrenceIndex(t, db)

	t.Run("stored occurrence re-enters once after lease expiry", func(t *testing.T) {
		exec := &successExecutor{}
		manager := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, nil, 8, 90)
		shutdownManagerOnCleanup(t, manager)

		taskEntity := seedCronRecoveryTask(t, db, StatusSuccess, false)
		if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Updates(map[string]interface{}{
			"status": string(StatusSuccess), "enabled": true, "cron_spec": "@every 1h", "skip_next": false,
		}).Error; err != nil {
			t.Fatalf("prepare stored-occurrence task: %v", err)
		}
		now := time.Now().UTC()
		occurrence := now.Add(-time.Hour).Truncate(time.Microsecond)
		expiredLease := now.Add(-time.Minute)
		run := model.TaskRun{
			TaskID:              taskEntity.ID,
			NodeIDSnapshot:      taskEntity.NodeID,
			TriggerType:         "cron",
			CronScheduledAt:     &occurrence,
			Status:              model.TaskRunStatusPending,
			ChainRunID:          "cron-crash-chain",
			ExecutionOwnerID:    "prior-owner",
			ExecutionLeaseUntil: &expiredLease,
		}
		if err := db.Create(&run).Error; err != nil {
			t.Fatalf("seed pending cron crash row: %v", err)
		}

		if err := manager.reconcileExpiredOrdinaryRuns(context.Background()); err != nil {
			t.Fatalf("generic expiry reconciliation before cron recovery: %v", err)
		}
		var stillPending model.TaskRun
		if err := db.First(&stillPending, run.ID).Error; err != nil {
			t.Fatalf("reload pending cron before recovery: %v", err)
		}
		if stillPending.Status != model.TaskRunStatusPending {
			t.Fatalf("generic expiry terminalized recoverable cron status=%q", stillPending.Status)
		}

		if err := manager.reconcilePendingDurableRuns(context.Background()); err != nil {
			t.Fatalf("recover pending cron occurrence: %v", err)
		}
		recovered := waitTaskRunTerminal(t, db, run.ID)
		if recovered.Status != model.TaskRunStatusSuccess {
			t.Fatalf("recovered cron status=%q, want success", recovered.Status)
		}
		if recovered.CronScheduledAt == nil || !recovered.CronScheduledAt.Equal(occurrence) {
			t.Fatalf("recovered occurrence=%v, want %v", recovered.CronScheduledAt, occurrence)
		}
		if exec.Calls() != 1 {
			t.Fatalf("recovered cron executor calls=%d, want 1", exec.Calls())
		}

		// A second startup/tick pass sees the terminal row and cannot launch a
		// duplicate occurrence.
		if err := manager.reconcilePendingDurableRuns(context.Background()); err != nil {
			t.Fatalf("repeat pending cron recovery: %v", err)
		}
		var runCount int64
		if err := db.Model(&model.TaskRun{}).Where("task_id = ? AND trigger_type = ?", taskEntity.ID, "cron").Count(&runCount).Error; err != nil {
			t.Fatalf("count recovered cron rows: %v", err)
		}
		if runCount != 1 {
			t.Fatalf("recovered cron rows=%d, want 1", runCount)
		}
	})

	t.Run("skip-next consumes recovered occurrence and permits following one", func(t *testing.T) {
		exec := &successExecutor{}
		manager := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, nil, 8, 90)
		shutdownManagerOnCleanup(t, manager)

		taskEntity := seedCronRecoveryTask(t, db, StatusSuccess, true)
		if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Updates(map[string]interface{}{
			"status": string(StatusSuccess), "enabled": true, "cron_spec": "@every 1h", "skip_next": true,
		}).Error; err != nil {
			t.Fatalf("prepare skip-next task: %v", err)
		}
		now := time.Now().UTC()
		occurrence := now.Add(-time.Hour).Truncate(time.Microsecond)
		expiredLease := now.Add(-time.Minute)
		run := model.TaskRun{
			TaskID:              taskEntity.ID,
			NodeIDSnapshot:      taskEntity.NodeID,
			TriggerType:         "cron",
			CronScheduledAt:     &occurrence,
			Status:              model.TaskRunStatusPending,
			ChainRunID:          "cron-skip-crash-chain",
			ExecutionOwnerID:    "prior-owner",
			ExecutionLeaseUntil: &expiredLease,
		}
		if err := db.Create(&run).Error; err != nil {
			t.Fatalf("seed pending skip-next cron row: %v", err)
		}

		if err := manager.reconcilePendingDurableRuns(context.Background()); err != nil {
			t.Fatalf("recover skip-next cron occurrence: %v", err)
		}
		recovered := waitTaskRunTerminal(t, db, run.ID)
		// Durable terminalization precedes the runner's process-local owner
		// cleanup. Wait for the worker itself before reserving the following
		// occurrence, otherwise pendingRuns can still reject it as a duplicate.
		manager.taskWG.Wait()
		if recovered.Status != model.TaskRunStatusCanceled {
			t.Fatalf("recovered skip-next status=%q, want canceled", recovered.Status)
		}
		if exec.Calls() != 0 {
			t.Fatalf("skipped recovered cron executor calls=%d, want 0", exec.Calls())
		}
		var afterSkip model.Task
		if err := db.First(&afterSkip, taskEntity.ID).Error; err != nil {
			t.Fatalf("reload skip-next task: %v", err)
		}
		if afterSkip.SkipNext {
			t.Fatal("recovered skip-next occurrence did not consume skip_next")
		}

		nextOccurrence := occurrence.Add(time.Hour)
		nextRunID, err := manager.triggerCore(taskEntity.ID, "cron", "cron-following-chain", nil, &nextOccurrence)
		if err != nil {
			t.Fatalf("trigger following cron occurrence: %v", err)
		}
		nextRun := waitTaskRunTerminal(t, db, nextRunID)
		if nextRun.Status != model.TaskRunStatusSuccess {
			t.Fatalf("following cron status=%q, want success", nextRun.Status)
		}
		if nextRun.CronScheduledAt == nil || !nextRun.CronScheduledAt.Equal(nextOccurrence) {
			t.Fatalf("following occurrence=%v, want %v", nextRun.CronScheduledAt, nextOccurrence)
		}
		if exec.Calls() != 1 {
			t.Fatalf("following cron executor calls=%d, want 1", exec.Calls())
		}
		var canceledCount, successCount int64
		if err := db.Model(&model.TaskRun{}).Where("task_id = ? AND status = ?", taskEntity.ID, model.TaskRunStatusCanceled).Count(&canceledCount).Error; err != nil {
			t.Fatalf("count canceled cron rows: %v", err)
		}
		if err := db.Model(&model.TaskRun{}).Where("task_id = ? AND status = ?", taskEntity.ID, model.TaskRunStatusSuccess).Count(&successCount).Error; err != nil {
			t.Fatalf("count successful cron rows: %v", err)
		}
		if canceledCount != 1 || successCount != 1 {
			t.Fatalf("cron terminal counts canceled=%d success=%d, want 1/1", canceledCount, successCount)
		}
	})
}

func TestExpiredCronRecoveryNeverReplaysRunningOrUnknownSQLite(t *testing.T) {
	runExpiredCronRecoveryNeverReplays(t, openManagerTestDB(t))
}

func TestExpiredCronRecoveryNeverReplaysRunningOrUnknownPostgres(t *testing.T) {

	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	runExpiredCronRecoveryNeverReplays(t, openTaskTerminalPostgresDB(t, dsn))
}

func runExpiredCronRecoveryNeverReplays(t *testing.T, db *gorm.DB) {
	t.Helper()
	prepareCronRecoveryTestDB(t, db)
	installCronOccurrenceIndex(t, db)
	t.Run("live pending cron lease remains untouched", func(t *testing.T) {
		exec := &successExecutor{}
		manager := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, nil, 8, 90)
		shutdownManagerOnCleanup(t, manager)
		taskEntity := seedCronRecoveryTask(t, db, StatusSuccess, false)
		now := time.Now().UTC()
		occurrence := now.Add(-time.Minute).Truncate(time.Microsecond)
		liveLease := now.Add(time.Minute)
		run := model.TaskRun{
			TaskID:              taskEntity.ID,
			NodeIDSnapshot:      taskEntity.NodeID,
			TriggerType:         "cron",
			CronScheduledAt:     &occurrence,
			Status:              model.TaskRunStatusPending,
			ChainRunID:          "cron-live-chain",
			ExecutionOwnerID:    "live-owner",
			ExecutionLeaseUntil: &liveLease,
		}
		if err := db.Create(&run).Error; err != nil {
			t.Fatalf("seed live pending cron row: %v", err)
		}
		if err := manager.reconcilePendingDurableRuns(context.Background()); err != nil {
			t.Fatalf("scan live pending cron row: %v", err)
		}
		var untouched model.TaskRun
		if err := db.First(&untouched, run.ID).Error; err != nil {
			t.Fatalf("reload live pending cron row: %v", err)
		}
		if untouched.Status != model.TaskRunStatusPending ||
			untouched.ExecutionOwnerID != "live-owner" ||
			untouched.ExecutionLeaseUntil == nil ||
			!untouched.ExecutionLeaseUntil.After(now) ||
			exec.Calls() != 0 {
			t.Fatalf("live pending cron row changed or replayed: %+v executor_calls=%d", untouched, exec.Calls())
		}
	})
	t.Run("expired running occurrence is terminalized, not relaunched", func(t *testing.T) {
		exec := &successExecutor{}
		manager := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, nil, 8, 90)
		shutdownManagerOnCleanup(t, manager)
		taskEntity := seedCronRecoveryTask(t, db, StatusRunning, false)
		if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Updates(map[string]interface{}{
			"status": string(StatusRunning), "enabled": true, "cron_spec": "@every 1h",
		}).Error; err != nil {
			t.Fatalf("prepare running cron task: %v", err)
		}
		now := time.Now().UTC()
		occurrence := now.Add(-time.Hour).Truncate(time.Microsecond)
		expiredLease := now.Add(-time.Minute)
		run := model.TaskRun{
			TaskID:              taskEntity.ID,
			NodeIDSnapshot:      taskEntity.NodeID,
			TriggerType:         "cron",
			CronScheduledAt:     &occurrence,
			Status:              model.TaskRunStatusRunning,
			ChainRunID:          "cron-running-chain",
			ExecutionOwnerID:    "prior-owner",
			ExecutionLeaseUntil: &expiredLease,
		}
		if err := db.Create(&run).Error; err != nil {
			t.Fatalf("seed expired running cron row: %v", err)
		}
		if err := manager.reconcilePendingDurableRuns(context.Background()); err != nil {
			t.Fatalf("pending cron recovery scan: %v", err)
		}
		var untouched model.TaskRun
		if err := db.First(&untouched, run.ID).Error; err != nil {
			t.Fatalf("reload running cron row: %v", err)
		}
		if untouched.Status != model.TaskRunStatusRunning || exec.Calls() != 0 {
			t.Fatalf("pending recovery replayed running cron: status=%q executor_calls=%d", untouched.Status, exec.Calls())
		}
		if err := manager.reconcileExpiredOrdinaryRuns(context.Background()); err != nil {
			t.Fatalf("terminalize expired running cron row: %v", err)
		}
		recovered := waitTaskRunTerminal(t, db, run.ID)
		if recovered.Status != model.TaskRunStatusFailed {
			t.Fatalf("expired running cron status=%q, want failed", recovered.Status)
		}
		if exec.Calls() != 0 {
			t.Fatalf("expired running cron executor calls=%d, want 0", exec.Calls())
		}
	})

	t.Run("pending cron without stored occurrence is unknown and not relaunched", func(t *testing.T) {
		exec := &successExecutor{}
		manager := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, nil, 8, 90)
		shutdownManagerOnCleanup(t, manager)
		taskEntity := seedCronRecoveryTask(t, db, StatusSuccess, false)
		if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Updates(map[string]interface{}{
			"status": string(StatusSuccess), "enabled": true, "cron_spec": "@every 1h",
		}).Error; err != nil {
			t.Fatalf("prepare unknown cron task: %v", err)
		}
		expiredLease := time.Now().UTC().Add(-time.Minute)
		run := model.TaskRun{
			TaskID:              taskEntity.ID,
			NodeIDSnapshot:      taskEntity.NodeID,
			TriggerType:         "cron",
			Status:              model.TaskRunStatusPending,
			ChainRunID:          "cron-unknown-chain",
			ExecutionOwnerID:    "prior-owner",
			ExecutionLeaseUntil: &expiredLease,
		}
		if err := db.Create(&run).Error; err != nil {
			t.Fatalf("seed unknown pending cron row: %v", err)
		}
		if err := manager.reconcilePendingDurableRuns(context.Background()); err != nil {
			t.Fatalf("pending unknown cron recovery scan: %v", err)
		}
		var untouched model.TaskRun
		if err := db.First(&untouched, run.ID).Error; err != nil {
			t.Fatalf("reload unknown pending cron row: %v", err)
		}
		if untouched.Status != model.TaskRunStatusPending || exec.Calls() != 0 {
			t.Fatalf("pending recovery replayed unknown cron: status=%q executor_calls=%d", untouched.Status, exec.Calls())
		}
		if err := manager.reconcileExpiredOrdinaryRuns(context.Background()); err != nil {
			t.Fatalf("terminalize unknown pending cron row: %v", err)
		}
		recovered := waitTaskRunTerminal(t, db, run.ID)
		if recovered.Status != model.TaskRunStatusFailed {
			t.Fatalf("unknown pending cron status=%q, want failed", recovered.Status)
		}
		if exec.Calls() != 0 {
			t.Fatalf("unknown pending cron executor calls=%d, want 0", exec.Calls())
		}
	})
}

func TestLosingPendingRecoveryPreservesLiveTaskAggregateSQLite(t *testing.T) {
	runLosingPendingRecoveryPreservesAggregate(t, openManagerTestDB(t))
}

func TestLosingPendingRecoveryPreservesLiveTaskAggregatePostgres(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	runLosingPendingRecoveryPreservesAggregate(t, openTaskTerminalPostgresDB(t, dsn))
}

func runLosingPendingRecoveryPreservesAggregate(t *testing.T, db *gorm.DB) {
	t.Helper()
	prepareCronRecoveryTestDB(t, db)
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)
	taskEntity := seedCronRecoveryTask(t, db, StatusRunning, false)
	const previousError = "live-owner-error"
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Updates(map[string]interface{}{
		"status": string(StatusRunning), "last_error": previousError, "enabled": true,
	}).Error; err != nil {
		t.Fatalf("prepare live task aggregate: %v", err)
	}
	now := time.Now().UTC()
	winningLease := now.Add(time.Minute)
	winningRun := model.TaskRun{
		TaskID:              taskEntity.ID,
		NodeIDSnapshot:      taskEntity.NodeID,
		TriggerType:         "manual",
		Status:              model.TaskRunStatusRunning,
		ExecutionOwnerID:    "live-owner",
		ExecutionLeaseUntil: &winningLease,
	}
	if err := db.Create(&winningRun).Error; err != nil {
		t.Fatalf("seed live winning run: %v", err)
	}
	losingLease := now.Add(time.Minute)
	losingRun := model.TaskRun{
		TaskID:              taskEntity.ID,
		NodeIDSnapshot:      taskEntity.NodeID,
		TriggerType:         "chain",
		Status:              model.TaskRunStatusPending,
		ExecutionOwnerID:    manager.executionOwnerID,
		ExecutionLeaseUntil: &losingLease,
	}
	if err := db.Create(&losingRun).Error; err != nil {
		t.Fatalf("seed losing pending run: %v", err)
	}
	if err := manager.recoverTaskRunOnReturn(context.Background(), taskEntity.ID, losingRun.ID, "losing runner returned"); err != nil {
		t.Fatalf("recover losing pending run: %v", err)
	}
	var currentTask model.Task
	if err := db.First(&currentTask, taskEntity.ID).Error; err != nil {
		t.Fatalf("reload aggregate after losing recovery: %v", err)
	}
	if currentTask.Status != string(StatusRunning) || currentTask.LastError != previousError {
		t.Fatalf("losing recovery changed live aggregate: status=%q last_error=%q", currentTask.Status, currentTask.LastError)
	}
	var currentWinning model.TaskRun
	if err := db.First(&currentWinning, winningRun.ID).Error; err != nil {
		t.Fatalf("reload winning run: %v", err)
	}
	if currentWinning.Status != model.TaskRunStatusRunning || currentWinning.ExecutionOwnerID != "live-owner" || currentWinning.ExecutionLeaseUntil == nil {
		t.Fatalf("losing recovery changed live winning run: %+v", currentWinning)
	}
	var currentLosing model.TaskRun
	if err := db.First(&currentLosing, losingRun.ID).Error; err != nil {
		t.Fatalf("reload losing run: %v", err)
	}
	if currentLosing.Status != model.TaskRunStatusFailed || currentLosing.ExecutionOwnerID != "" || currentLosing.ExecutionLeaseUntil != nil {
		t.Fatalf("losing run did not terminalize independently: %+v", currentLosing)
	}
}
