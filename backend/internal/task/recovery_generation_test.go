package task

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"
	"xirang/backend/internal/model"
	taskexec "xirang/backend/internal/task/executor"
)

func TestLegacyRsyncGenerationLineageRequiresVerifiedWriteAfterPartialFailure(t *testing.T) {
	runLegacyRsyncGenerationLineage(t, openManagerTestDB(t))
}

func TestLegacyRsyncGenerationLineageRequiresVerifiedWriteAfterPartialFailurePostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db := openTaskTerminalPostgresDB(t, dsn)
	if err := db.AutoMigrate(&model.RestoreDrillEvidence{}, &model.CredentialAuditEvent{}, &model.TaskLog{}, &model.Alert{}, &model.Integration{}, &model.TaskTrafficSample{}, &model.AlertDelivery{}); err != nil {
		t.Fatalf("migrate isolated PostgreSQL manager tables: %v", err)
	}
	runLegacyRsyncGenerationLineage(t, db)
}

func runLegacyRsyncGenerationLineage(t *testing.T, db *gorm.DB) {
	t.Helper()
	taskEntity := seedTaskForManagerTest(t, db)

	successManager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, successManager)
	firstRunID := createTestTaskRun(t, db, taskEntity.ID, "manual")
	successManager.runTask(taskEntity.ID, firstRunID, "manual", generateChainRunID())
	var firstRun model.TaskRun
	if err := db.First(&firstRun, firstRunID).Error; err != nil {
		t.Fatal(err)
	}
	if firstRun.Status != model.TaskRunStatusSuccess || firstRun.BackupGenerationState != model.TaskRunGenerationStateVerified {
		t.Fatalf("initial generation status=%q state=%q", firstRun.Status, firstRun.BackupGenerationState)
	}

	// A canceled/skipped attempt before the first possible write has no dirty
	// generation state and must not displace the verified source of restore.
	for _, status := range []string{model.TaskRunStatusCanceled, model.TaskRunStatusSkipped} {
		attempt := model.TaskRun{TaskID: taskEntity.ID, NodeIDSnapshot: taskEntity.NodeID, TriggerType: "manual", Status: status}
		if err := db.Create(&attempt).Error; err != nil {
			t.Fatal(err)
		}
	}
	if _, err := successManager.loadRestoreTaskWithProvenance(context.Background(), taskEntity.ID); err != nil {
		t.Fatalf("no-write canceled/skipped attempts displaced verified generation: %v", err)
	}

	failureManager := NewManager(db, stubExecutorFactory{executor: &sourceCompletionFailExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, failureManager)
	partialRunID := createTestTaskRun(t, db, taskEntity.ID, "manual")
	failureManager.runTask(taskEntity.ID, partialRunID, "manual", generateChainRunID())
	var partialRun model.TaskRun
	if err := db.First(&partialRun, partialRunID).Error; err != nil {
		t.Fatal(err)
	}
	if partialRun.BackupGenerationState != model.TaskRunGenerationStateDirty {
		t.Fatalf("partial write state=%q, want dirty", partialRun.BackupGenerationState)
	}
	if _, err := failureManager.loadRestoreTaskWithProvenance(context.Background(), taskEntity.ID); err == nil {
		t.Fatal("restore unexpectedly accepted latest dirty partial-write generation")
	}

	// A crash after reserving the write generation is represented by a dirty
	// terminal row and is equally ineligible until a fresh verified write.
	crashed := model.TaskRun{
		TaskID: taskEntity.ID, NodeIDSnapshot: taskEntity.NodeID, TriggerType: "manual",
		Status: model.TaskRunStatusFailed, BackupGenerationState: model.TaskRunGenerationStateDirty,
	}
	if err := db.Create(&crashed).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := failureManager.loadRestoreTaskWithProvenance(context.Background(), taskEntity.ID); err == nil {
		t.Fatal("restore unexpectedly accepted crash-dirty generation")
	}

	finalManager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, finalManager)
	finalRunID := createTestTaskRun(t, db, taskEntity.ID, "manual")
	finalManager.runTask(taskEntity.ID, finalRunID, "manual", generateChainRunID())
	if _, err := finalManager.loadRestoreTaskWithProvenance(context.Background(), taskEntity.ID); err != nil {
		t.Fatalf("fresh verified generation did not reauthorize restore: %v", err)
	}
}

func TestLegacyRsyncCaptureEvidenceFailureRunsProviderButDeniesRestore(t *testing.T) {
	db := openManagerTestDB(t)
	taskEntity := seedTaskForManagerTest(t, db)
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Update("rsync_source", filepath.Join(t.TempDir(), "missing-source")).Error; err != nil {
		t.Fatal(err)
	}
	exec := &successExecutor{}
	manager := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)
	runID := createTestTaskRun(t, db, taskEntity.ID, "manual")
	manager.runTask(taskEntity.ID, runID, "manual", generateChainRunID())
	var run model.TaskRun
	if err := db.First(&run, runID).Error; err != nil {
		t.Fatal(err)
	}
	if run.Status != model.TaskRunStatusWarning {
		t.Fatalf("capture evidence failure status=%q, want warning", run.Status)
	}
	if exec.Calls() != 1 {
		t.Fatalf("provider calls=%d, want one transfer attempt", exec.Calls())
	}
	if run.BackupGenerationState != model.TaskRunGenerationStateDirty {
		t.Fatalf("capture evidence failure state=%q, want dirty", run.BackupGenerationState)
	}
	if _, err := manager.loadRestoreTaskWithProvenance(context.Background(), taskEntity.ID); err == nil {
		t.Fatal("restore unexpectedly accepted warning run without capture evidence")
	}
}

type writeThenBlockExecutor struct {
	started chan struct{}
	once    sync.Once
}

func (e *writeThenBlockExecutor) Run(ctx context.Context, task model.Task, _ taskexec.LogFunc, _ taskexec.ProgressFunc) (int, error) {
	if err := os.WriteFile(filepath.Join(task.RsyncTarget, "write-started"), []byte("partial"), 0o600); err != nil {
		return -1, err
	}
	e.once.Do(func() { close(e.started) })
	<-ctx.Done()
	return -1, ctx.Err()
}

func TestLegacyRsyncCancellationAfterWriteStartedLeavesGenerationDirty(t *testing.T) {
	runLegacyRsyncCancellationAfterWriteStarted(t, openManagerTestDB(t))
}

func TestLegacyRsyncCancellationAfterWriteStartedLeavesGenerationDirtyPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db := openTaskTerminalPostgresDB(t, dsn)
	if err := db.AutoMigrate(&model.RestoreDrillEvidence{}, &model.CredentialAuditEvent{}, &model.TaskLog{}, &model.Alert{}, &model.Integration{}, &model.TaskTrafficSample{}, &model.AlertDelivery{}); err != nil {
		t.Fatalf("migrate isolated PostgreSQL manager tables: %v", err)
	}
	runLegacyRsyncCancellationAfterWriteStarted(t, db)
}

func runLegacyRsyncCancellationAfterWriteStarted(t *testing.T, db *gorm.DB) {
	t.Helper()
	taskEntity := seedTaskForManagerTest(t, db)
	exec := &writeThenBlockExecutor{started: make(chan struct{})}
	manager := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)
	runID := createTestTaskRun(t, db, taskEntity.ID, "manual")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		manager.runTaskWithContext(taskEntity.ID, runID, "manual", generateChainRunID(), ctx, nil, cancel)
		close(done)
	}()
	select {
	case <-exec.started:
	case <-time.After(3 * time.Second):
		t.Fatal("executor did not reach write-started point")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("canceled write did not terminate")
	}
	var run model.TaskRun
	if err := db.First(&run, runID).Error; err != nil {
		t.Fatal(err)
	}
	if run.BackupGenerationState != model.TaskRunGenerationStateDirty {
		t.Fatalf("post-write cancellation state=%q, want dirty", run.BackupGenerationState)
	}
	if _, err := manager.loadRestoreTaskWithProvenance(context.Background(), taskEntity.ID); err == nil {
		t.Fatal("restore unexpectedly accepted post-write canceled generation")
	}
}

func TestLegacyRsyncCanceledBeforeCaptureDoesNotDirtyGeneration(t *testing.T) {
	db := openManagerTestDB(t)
	taskEntity := seedTaskForManagerTest(t, db)
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)
	runID := createTestTaskRun(t, db, taskEntity.ID, "manual")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runCancel := func() {}
	manager.runTaskWithContext(taskEntity.ID, runID, "manual", generateChainRunID(), ctx, nil, runCancel)
	var run model.TaskRun
	if err := db.First(&run, runID).Error; err != nil {
		t.Fatal(err)
	}
	if run.Status != model.TaskRunStatusCanceled || run.BackupGenerationState != "" {
		t.Fatalf("pre-capture cancellation status=%q state=%q", run.Status, run.BackupGenerationState)
	}
}
