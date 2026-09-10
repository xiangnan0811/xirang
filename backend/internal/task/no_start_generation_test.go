package task

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"gorm.io/gorm"
	"xirang/backend/internal/model"
	taskexec "xirang/backend/internal/task/executor"
)

type noStartCompatibilityExecutor struct {
	err error
}

func (e *noStartCompatibilityExecutor) Run(context.Context, model.Task, taskexec.LogFunc, taskexec.ProgressFunc) (int, error) {
	return -1, &taskexec.NoProcessStartError{Err: e.err}
}

func (e *noStartCompatibilityExecutor) RsyncBinary() string {
	return "rsync"
}

func TestLegacyRsyncNoStartFailurePreservesVerifiedGeneration(t *testing.T) {
	rsyncBinary, err := exec.LookPath("rsync")
	if err != nil {
		t.Skip("rsync is not installed")
	}
	runLegacyRsyncNoStartFailure(t, openManagerTestDB(t), rsyncBinary)
}

func TestLegacyRsyncNoStartFailurePreservesVerifiedGenerationPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	rsyncBinary, err := exec.LookPath("rsync")
	if err != nil {
		t.Skip("rsync is not installed")
	}
	db := openTaskTerminalPostgresDB(t, dsn)
	if err := db.AutoMigrate(&model.RestoreDrillEvidence{}, &model.CredentialAuditEvent{}, &model.TaskLog{}, &model.Alert{}, &model.Integration{}, &model.TaskTrafficSample{}, &model.AlertDelivery{}); err != nil {
		t.Fatalf("migrate isolated PostgreSQL manager tables: %v", err)
	}
	runLegacyRsyncNoStartFailure(t, db, rsyncBinary)
}

func runLegacyRsyncNoStartFailure(t *testing.T, db *gorm.DB, rsyncBinary string) {
	t.Helper()
	taskEntity := seedTaskForManagerTest(t, db)
	if err := os.WriteFile(taskEntity.RsyncSource+"payload", []byte("verified-generation-payload"), 0o644); err != nil {
		t.Fatalf("seed source payload: %v", err)
	}
	policy := model.Policy{
		Name:       "no-start-generation-policy",
		SourcePath: taskEntity.RsyncSource,
		TargetPath: taskEntity.RsyncTarget,
		CronSpec:   "",
	}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatalf("create no-start policy: %v", err)
	}
	if err := db.Model(&model.Policy{}).Where("id = ?", policy.ID).Update("max_retries", 0).Error; err != nil {
		t.Fatalf("persist max_retries=0: %v", err)
	}
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Update("policy_id", policy.ID).Error; err != nil {
		t.Fatalf("attach no-start policy: %v", err)
	}

	manager := NewManager(db, taskexec.NewFactory(rsyncBinary), nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)
	firstRunID := createTestTaskRun(t, db, taskEntity.ID, "manual")
	manager.runTask(taskEntity.ID, firstRunID, "manual", generateChainRunID())
	firstRun := waitTaskRunTerminal(t, db, firstRunID)
	if firstRun.Status != model.TaskRunStatusSuccess || firstRun.BackupGenerationState != model.TaskRunGenerationStateVerified {
		t.Fatalf("initial generation status=%q state=%q error=%q", firstRun.Status, firstRun.BackupGenerationState, firstRun.LastError)
	}

	manager.executorFactory = stubExecutorFactory{executor: &noStartCompatibilityExecutor{err: errors.New("FAKE_RSYNC_START_FAILURE_FOR_TEST_ONLY")}}
	secondRunID := createTestTaskRun(t, db, taskEntity.ID, "manual")
	manager.runTask(taskEntity.ID, secondRunID, "manual", generateChainRunID())
	secondRun := waitTaskRunTerminal(t, db, secondRunID)
	if secondRun.Status != model.TaskRunStatusFailed || secondRun.BackupGenerationState != "" {
		t.Fatalf("no-start run status=%q state=%q error=%q", secondRun.Status, secondRun.BackupGenerationState, secondRun.LastError)
	}
	if _, err := manager.loadRestoreTaskWithProvenance(context.Background(), taskEntity.ID); err != nil {
		t.Fatalf("authoritative verified generation was displaced by no-start failure: %v", err)
	}
}

func TestLegacyRsyncCancellationAfterCaptureBeforeStartPreservesVerifiedGeneration(t *testing.T) {
	rsyncBinary, err := exec.LookPath("rsync")
	if err != nil {
		t.Skip("rsync is not installed")
	}
	runLegacyRsyncCancellationAfterCaptureBeforeStart(t, openManagerTestDB(t), rsyncBinary)
}

func TestLegacyRsyncCancellationAfterCaptureBeforeStartPreservesVerifiedGenerationPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	rsyncBinary, err := exec.LookPath("rsync")
	if err != nil {
		t.Skip("rsync is not installed")
	}
	db := openTaskTerminalPostgresDB(t, dsn)
	if err := db.AutoMigrate(&model.RestoreDrillEvidence{}, &model.CredentialAuditEvent{}, &model.TaskLog{}, &model.Alert{}, &model.Integration{}, &model.TaskTrafficSample{}, &model.AlertDelivery{}); err != nil {
		t.Fatalf("migrate isolated PostgreSQL manager tables: %v", err)
	}
	runLegacyRsyncCancellationAfterCaptureBeforeStart(t, db, rsyncBinary)
}

func runLegacyRsyncCancellationAfterCaptureBeforeStart(t *testing.T, db *gorm.DB, rsyncBinary string) {
	t.Helper()
	taskEntity := seedTaskForManagerTest(t, db)
	if err := os.WriteFile(taskEntity.RsyncSource+"payload", []byte("verified-generation-payload"), 0o644); err != nil {
		t.Fatalf("seed source payload: %v", err)
	}
	manager := NewManager(db, taskexec.NewFactory(rsyncBinary), nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)
	firstRunID := createTestTaskRun(t, db, taskEntity.ID, "manual")
	manager.runTask(taskEntity.ID, firstRunID, "manual", generateChainRunID())
	firstRun := waitTaskRunTerminal(t, db, firstRunID)
	if firstRun.Status != model.TaskRunStatusSuccess || firstRun.BackupGenerationState != model.TaskRunGenerationStateVerified {
		t.Fatalf("initial generation status=%q state=%q error=%q", firstRun.Status, firstRun.BackupGenerationState, firstRun.LastError)
	}

	cancelCtx, cancel := context.WithCancel(context.Background())
	manager.executorFactory = stubExecutorFactory{executor: &noStartCompatibilityExecutor{err: context.Canceled}}
	manager.afterLegacyRsyncGenerationArm = cancel
	secondRunID := createTestTaskRun(t, db, taskEntity.ID, "manual")
	manager.runTaskWithContext(taskEntity.ID, secondRunID, "manual", generateChainRunID(), cancelCtx, nil, cancel)
	secondRun := waitTaskRunTerminal(t, db, secondRunID)
	if secondRun.Status != model.TaskRunStatusCanceled || secondRun.BackupGenerationState != "" {
		t.Fatalf("capture-boundary cancellation status=%q state=%q error=%q", secondRun.Status, secondRun.BackupGenerationState, secondRun.LastError)
	}
	if _, err := manager.loadRestoreTaskWithProvenance(context.Background(), taskEntity.ID); err != nil {
		t.Fatalf("capture-boundary cancellation displaced verified generation: %v", err)
	}
}
