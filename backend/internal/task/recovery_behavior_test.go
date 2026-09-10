package task

import (
	"testing"

	"xirang/backend/internal/model"
)

func TestRunTaskPolicyMaxRetriesZeroDoesNotRetry(t *testing.T) {
	db := openManagerTestDB(t)
	taskEntity := seedTaskForManagerTest(t, db)
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Update("executor_type", "command").Error; err != nil {
		t.Fatalf("set command executor: %v", err)
	}
	completionPolicy(t, db, taskEntity.ID, false, "")
	manager := NewManager(db, stubExecutorFactory{executor: &sourceCompletionFailExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)
	runID := createTestTaskRun(t, db, taskEntity.ID, "manual")
	manager.runTask(taskEntity.ID, runID, "manual", generateChainRunID())

	var storedTask model.Task
	if err := db.First(&storedTask, taskEntity.ID).Error; err != nil {
		t.Fatalf("load task after failed run: %v", err)
	}
	var storedRun model.TaskRun
	if err := db.First(&storedRun, runID).Error; err != nil {
		t.Fatalf("load run after failed run: %v", err)
	}
	if storedTask.Status != string(StatusFailed) || storedRun.Status != model.TaskRunStatusFailed {
		t.Fatalf("zero-retry failure statuses task=%q run=%q", storedTask.Status, storedRun.Status)
	}
	if storedTask.RetryCount != 0 {
		t.Fatalf("zero-retry failure scheduled a retry: retry_count=%d", storedTask.RetryCount)
	}
	var runCount int64
	if err := db.Model(&model.TaskRun{}).Where("task_id = ?", taskEntity.ID).Count(&runCount).Error; err != nil {
		t.Fatalf("count task runs: %v", err)
	}
	if runCount != 1 {
		t.Fatalf("zero-retry failure created %d task runs, want one", runCount)
	}
}
