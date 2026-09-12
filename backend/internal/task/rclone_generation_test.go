package task

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/model"
	taskexec "xirang/backend/internal/task/executor"
	"xirang/backend/internal/task/testutil"

	"gorm.io/gorm"
)

func seedLegacyRcloneTask(t *testing.T, db *gorm.DB) model.Task {
	t.Helper()
	taskEntity := seedTaskForManagerTest(t, db)
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Updates(map[string]interface{}{
		"executor_type":   "rclone",
		"executor_config": "",
	}).Error; err != nil {
		t.Fatalf("configure legacy Rclone task: %v", err)
	}
	if err := db.Preload("Node").Preload("Policy").First(&taskEntity, taskEntity.ID).Error; err != nil {
		t.Fatalf("reload legacy Rclone task: %v", err)
	}
	return taskEntity
}

func seedRcloneGeneration(t *testing.T, db *gorm.DB, taskEntity model.Task, status, state, lastError string, createdAt time.Time) model.TaskRun {
	t.Helper()
	finishedAt := createdAt.Add(time.Minute)
	run := model.TaskRun{
		TaskID:                  taskEntity.ID,
		NodeIDSnapshot:          taskEntity.NodeID,
		TriggerType:             "manual",
		Status:                  status,
		BackupConfigFingerprint: model.TaskRunBackupConfigFingerprint(taskEntity),
		BackupGenerationState:   state,
		LastError:               lastError,
		CreatedAt:               createdAt,
		UpdatedAt:               createdAt,
		FinishedAt:              &finishedAt,
	}
	if err := db.Create(&run).Error; err != nil {
		t.Fatalf("create Rclone generation: %v", err)
	}
	return run
}

type unknownRcloneExecutor struct {
	calls int
}

func (e *unknownRcloneExecutor) Run(context.Context, model.Task, taskexec.LogFunc, taskexec.ProgressFunc) (int, error) {
	e.calls++
	return -1, &taskexec.RemoteExecutionUnknownError{Err: errors.New("RCLONE_REMOTE_RESULT_UNKNOWN_FOR_TEST_ONLY")}
}

type noStartDeadlineRcloneExecutor struct{}

func (*noStartDeadlineRcloneExecutor) Run(context.Context, model.Task, taskexec.LogFunc, taskexec.ProgressFunc) (int, error) {
	return -1, &taskexec.NoProcessStartError{Err: context.DeadlineExceeded}
}

type preChildCancelRcloneExecutor struct {
	calls atomic.Int32
}

func (e *preChildCancelRcloneExecutor) Run(ctx context.Context, _ model.Task, _ taskexec.LogFunc, _ taskexec.ProgressFunc) (int, error) {
	e.calls.Add(1)
	return -1, &taskexec.NoProcessStartError{Err: ctx.Err()}
}

func configureLegacyRclonePolicy(t *testing.T, db *gorm.DB, taskEntity model.Task, maxRetries int) model.Task {
	t.Helper()
	policy := model.Policy{
		Name:          "legacy-rclone-generation-policy-" + taskEntity.Name,
		MaxRetries:    maxRetries,
		VerifyEnabled: false,
	}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatalf("create Rclone generation policy: %v", err)
	}
	if err := db.Model(&model.Policy{}).Where("id = ?", policy.ID).
		Updates(map[string]interface{}{"max_retries": maxRetries, "verify_enabled": false}).Error; err != nil {
		t.Fatalf("persist Rclone generation policy: %v", err)
	}
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Update("policy_id", policy.ID).Error; err != nil {
		t.Fatalf("attach Rclone generation policy: %v", err)
	}
	if err := db.Preload("Node").Preload("Policy").First(&taskEntity, taskEntity.ID).Error; err != nil {
		t.Fatalf("reload Rclone generation task: %v", err)
	}
	return taskEntity
}
func runLegacyRcloneQueuedCancellation(t *testing.T, db *gorm.DB) {
	t.Helper()
	taskEntity := configureLegacyRclonePolicy(t, db, seedLegacyRcloneTask(t, db), 0)
	verified := seedRcloneGeneration(t, db, taskEntity, model.TaskRunStatusSuccess, model.TaskRunGenerationStateVerified, "", time.Now().UTC().Add(-time.Minute))
	queued := model.TaskRun{
		TaskID:                  taskEntity.ID,
		NodeIDSnapshot:          taskEntity.NodeID,
		TriggerType:             "manual",
		Status:                  model.TaskRunStatusPending,
		BackupConfigFingerprint: model.TaskRunBackupConfigFingerprint(taskEntity),
		CreatedAt:               time.Now().UTC(),
		UpdatedAt:               time.Now().UTC(),
	}
	if err := db.Create(&queued).Error; err != nil {
		t.Fatalf("create queued Rclone run: %v", err)
	}
	manager := &Manager{db: db, executionOwnerID: "queued-rclone-no-start-test"}
	canceledCount, err := manager.cancelPendingTaskRuns(taskEntity.ID, "queued Rclone cancellation")
	if err != nil {
		t.Fatalf("cancel queued Rclone run: %v", err)
	}
	if canceledCount != 1 {
		t.Fatalf("queued Rclone cancellation count=%d, want 1", canceledCount)
	}
	var canceled model.TaskRun
	if err := db.First(&canceled, queued.ID).Error; err != nil {
		t.Fatalf("reload canceled queued Rclone run: %v", err)
	}
	if canceled.Status != model.TaskRunStatusCanceled || canceled.BackupGenerationState != model.TaskRunGenerationStateNoStart {
		t.Fatalf("queued Rclone cancellation status=%q state=%q error=%q, want canceled/no_start",
			canceled.Status, canceled.BackupGenerationState, canceled.LastError)
	}
	loaded, err := manager.loadRestoreTaskWithProvenance(context.Background(), taskEntity.ID)
	if err != nil {
		t.Fatalf("restore after queued Rclone cancellation: %v", err)
	}
	if loaded.RsyncCaptureGenerationID != verified.ID {
		t.Fatalf("restore generation=%d after queued cancellation, want prior verified generation %d",
			loaded.RsyncCaptureGenerationID, verified.ID)
	}
}

func TestLegacyRcloneQueuedCancellationPersistsNoStart(t *testing.T) {
	runLegacyRcloneQueuedCancellation(t, openManagerTestDB(t))
}

func TestLegacyRcloneCancellationAfterDurableArmPreservesVerifiedGeneration(t *testing.T) {
	db := openManagerTestDB(t)
	taskEntity := configureLegacyRclonePolicy(t, db, seedLegacyRcloneTask(t, db), 0)
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)

	firstRunID := createTestTaskRun(t, db, taskEntity.ID, "manual")
	manager.runTask(taskEntity.ID, firstRunID, "manual", generateChainRunID())
	firstRun := waitTaskRunTerminal(t, db, firstRunID)
	if firstRun.Status != model.TaskRunStatusSuccess || firstRun.BackupGenerationState != model.TaskRunGenerationStateVerified {
		t.Fatalf("initial Rclone status=%q state=%q error=%q", firstRun.Status, firstRun.BackupGenerationState, firstRun.LastError)
	}

	invocation := &preChildCancelRcloneExecutor{}
	manager.executorFactory = stubExecutorFactory{executor: invocation}
	cancelCtx, cancel := context.WithCancel(context.Background())
	manager.afterLegacyRsyncGenerationArm = cancel
	secondRunID := createTestTaskRun(t, db, taskEntity.ID, "manual")
	manager.runTaskWithContext(taskEntity.ID, secondRunID, "manual", generateChainRunID(), cancelCtx, nil, cancel)
	secondRun := waitTaskRunTerminal(t, db, secondRunID)
	if invocation.calls.Load() != 0 {
		t.Fatalf("Rclone pre-child cancellation invoked executor %d times", invocation.calls.Load())
	}
	if secondRun.Status != model.TaskRunStatusCanceled || secondRun.BackupGenerationState != model.TaskRunGenerationStateNoStart {
		t.Fatalf("Rclone post-arm cancellation status=%q state=%q error=%q, want canceled/no_start",
			secondRun.Status, secondRun.BackupGenerationState, secondRun.LastError)
	}
	loaded, err := manager.loadRestoreTaskWithProvenance(context.Background(), taskEntity.ID)
	if err != nil {
		t.Fatalf("restore after Rclone post-arm cancellation: %v", err)
	}
	if loaded.RsyncCaptureGenerationID != firstRunID {
		t.Fatalf("restore generation=%d after Rclone post-arm cancellation, want prior verified generation %d",
			loaded.RsyncCaptureGenerationID, firstRunID)
	}
}

func TestLegacyRcloneCancellationBeforeProviderAfterDurableEntryPreservesVerifiedGeneration(t *testing.T) {
	runLegacyRcloneCancellationBeforeProviderAfterDurableEntry(t, openManagerTestDB(t))
}

func TestLegacyRcloneCancellationBeforeProviderAfterDurableEntryPreservesVerifiedGenerationPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	runLegacyRcloneCancellationBeforeProviderAfterDurableEntry(t, openTaskTerminalPostgresDB(t, dsn))
}

func runLegacyRcloneCancellationBeforeProviderAfterDurableEntry(t *testing.T, db *gorm.DB) {
	t.Helper()
	taskEntity := configureLegacyRclonePolicy(t, db, seedLegacyRcloneTask(t, db), 0)
	if err := db.Model(&model.Policy{}).Where("id = ?", *taskEntity.PolicyID).
		Update("pre_hook", "cancel-before-provider").Error; err != nil {
		t.Fatalf("configure Rclone cancellation pre-hook: %v", err)
	}
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)
	manager.hookRunFunc = func(_ context.Context, _ model.Task, _ string) error {
		return nil
	}

	firstRunID := createTestTaskRun(t, db, taskEntity.ID, "manual")
	manager.runTask(taskEntity.ID, firstRunID, "manual", generateChainRunID())
	firstRun := waitTaskRunTerminal(t, db, firstRunID)
	if firstRun.Status != model.TaskRunStatusSuccess || firstRun.BackupGenerationState != model.TaskRunGenerationStateVerified {
		t.Fatalf("initial Rclone status=%q state=%q error=%q", firstRun.Status, firstRun.BackupGenerationState, firstRun.LastError)
	}

	invocation := &preChildCancelRcloneExecutor{}
	manager.executorFactory = stubExecutorFactory{executor: invocation}
	cancelCtx, cancel := context.WithCancel(context.Background())
	manager.hookRunFunc = func(_ context.Context, _ model.Task, _ string) error {
		cancel()
		return nil
	}
	secondRunID := createTestTaskRun(t, db, taskEntity.ID, "manual")
	manager.runTaskWithContext(taskEntity.ID, secondRunID, "manual", generateChainRunID(), cancelCtx, nil, cancel)
	secondRun := waitTaskRunTerminal(t, db, secondRunID)
	if invocation.calls.Load() != 0 {
		t.Fatalf("Rclone pre-provider cancellation invoked executor %d times", invocation.calls.Load())
	}
	if secondRun.Status != model.TaskRunStatusCanceled || secondRun.BackupGenerationState != model.TaskRunGenerationStateNoStart {
		t.Fatalf("Rclone pre-provider cancellation status=%q state=%q error=%q, want canceled/no_start",
			secondRun.Status, secondRun.BackupGenerationState, secondRun.LastError)
	}
	loaded, err := manager.loadRestoreTaskWithProvenance(context.Background(), taskEntity.ID)
	if err != nil {
		t.Fatalf("restore after Rclone pre-provider cancellation: %v", err)
	}
	if loaded.RsyncCaptureGenerationID != firstRunID {
		t.Fatalf("restore generation=%d after Rclone pre-provider cancellation, want prior verified generation %d",
			loaded.RsyncCaptureGenerationID, firstRunID)
	}
}

func TestLegacyRclonePrepareNoStartPreservesVerifiedGeneration(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		err    error
		status string
	}{
		{name: "prepare_failure", err: errors.New("RCLONE_PREPARE_FAILURE_FOR_TEST_ONLY"), status: model.TaskRunStatusFailed},
		{name: "prepare_cancel", err: context.Canceled, status: model.TaskRunStatusCanceled},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db := openManagerTestDB(t)
			taskEntity := configureLegacyRclonePolicy(t, db, seedLegacyRcloneTask(t, db), 0)
			manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
			shutdownManagerOnCleanup(t, manager)
			firstRunID := createTestTaskRun(t, db, taskEntity.ID, "manual")
			manager.runTask(taskEntity.ID, firstRunID, "manual", generateChainRunID())
			firstRun := waitTaskRunTerminal(t, db, firstRunID)
			if firstRun.Status != model.TaskRunStatusSuccess || firstRun.BackupGenerationState != model.TaskRunGenerationStateVerified {
				t.Fatalf("initial Rclone status=%q state=%q error=%q", firstRun.Status, firstRun.BackupGenerationState, firstRun.LastError)
			}

			invocation := &preChildCancelRcloneExecutor{}
			manager.executorFactory = stubExecutorFactory{executor: invocation}
			manager.publicationCoordinator = &publicationCoordinatorFake{err: testCase.err}
			secondRunID := createTestTaskRun(t, db, taskEntity.ID, "manual")
			manager.runTask(taskEntity.ID, secondRunID, "manual", generateChainRunID())
			secondRun := waitTaskRunTerminal(t, db, secondRunID)
			if invocation.calls.Load() != 0 {
				t.Fatalf("Rclone Prepare no-start invoked executor %d times", invocation.calls.Load())
			}
			if secondRun.Status != testCase.status || secondRun.BackupGenerationState != model.TaskRunGenerationStateNoStart {
				t.Fatalf("Rclone Prepare no-start status=%q state=%q error=%q, want %q/no_start",
					secondRun.Status, secondRun.BackupGenerationState, secondRun.LastError, testCase.status)
			}
			loaded, err := manager.loadRestoreTaskWithProvenance(context.Background(), taskEntity.ID)
			if err != nil {
				t.Fatalf("restore after Rclone Prepare no-start: %v", err)
			}
			if loaded.RsyncCaptureGenerationID != firstRunID {
				t.Fatalf("restore generation=%d after Rclone Prepare no-start, want prior verified generation %d",
					loaded.RsyncCaptureGenerationID, firstRunID)
			}
		})
	}
}

func TestLegacyRclonePreProviderFailuresPreserveVerifiedGeneration(t *testing.T) {
	for _, testCase := range []struct {
		name          string
		before        func(t *testing.T, db *gorm.DB, taskEntity model.Task)
		after         func(t *testing.T, db *gorm.DB, taskEntity model.Task)
		configureHook func(manager *Manager)
		wantStatus    string
	}{
		{
			name: "maintenance",
			after: func(t *testing.T, db *gorm.DB, taskEntity model.Task) {
				t.Helper()
				start := time.Now().UTC().Add(-time.Minute)
				end := time.Now().UTC().Add(time.Minute)
				if err := db.Model(&model.Node{}).Where("id = ?", taskEntity.NodeID).
					Updates(map[string]interface{}{"maintenance_start": start, "maintenance_end": end}).Error; err != nil {
					t.Fatalf("configure maintenance window: %v", err)
				}
			},
			wantStatus: model.TaskRunStatusCanceled,
		},
		{
			name: "pre_hook",
			before: func(t *testing.T, db *gorm.DB, taskEntity model.Task) {
				t.Helper()
				if err := db.Model(&model.Policy{}).Where("id = ?", *taskEntity.PolicyID).
					Update("pre_hook", "rclone-pre-hook-failure").Error; err != nil {
					t.Fatalf("configure pre-hook failure: %v", err)
				}
			},
			configureHook: func(manager *Manager) {
				var calls atomic.Int32
				manager.hookRunFunc = func(context.Context, model.Task, string) error {
					if calls.Add(1) == 1 {
						return nil
					}
					return errors.New("RCLONE_PRE_HOOK_FAILURE_FOR_TEST_ONLY")
				}
			},
			wantStatus: model.TaskRunStatusFailed,
		},
		{
			name: "app_profile_render",
			before: func(t *testing.T, db *gorm.DB, taskEntity model.Task) {
				t.Helper()
				if err := db.AutoMigrate(&model.AppCredential{}); err != nil {
					t.Fatalf("migrate application credential table: %v", err)
				}
				credential := model.AppCredential{
					Name:   "rclone-app-profile-credential",
					Type:   "mysql",
					Config: `{"host":"127.0.0.1","user":"root"}`,
				}
				if err := db.Create(&credential).Error; err != nil {
					t.Fatalf("create application credential: %v", err)
				}
				if err := db.Model(&model.Policy{}).Where("id = ?", *taskEntity.PolicyID).
					Updates(map[string]interface{}{
						"app_profile":       "mysql",
						"app_credential_id": credential.ID,
					}).Error; err != nil {
					t.Fatalf("configure application profile: %v", err)
				}
			},
			after: func(t *testing.T, db *gorm.DB, _ model.Task) {
				t.Helper()
				if err := db.Model(&model.AppCredential{}).Where("name = ?", "rclone-app-profile-credential").
					Update("config", `{"host":`).Error; err != nil {
					t.Fatalf("corrupt application profile credential: %v", err)
				}
			},
			configureHook: func(manager *Manager) {
				manager.hookRunFunc = func(context.Context, model.Task, string) error {
					return nil
				}
			},
			wantStatus: model.TaskRunStatusFailed,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db := openManagerTestDB(t)
			taskEntity := configureLegacyRclonePolicy(t, db, seedLegacyRcloneTask(t, db), 0)
			if testCase.before != nil {
				testCase.before(t, db, taskEntity)
			}
			manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
			shutdownManagerOnCleanup(t, manager)
			if testCase.configureHook != nil {
				testCase.configureHook(manager)
			}
			firstRunID := createTestTaskRun(t, db, taskEntity.ID, "manual")
			manager.runTask(taskEntity.ID, firstRunID, "manual", generateChainRunID())
			firstRun := waitTaskRunTerminal(t, db, firstRunID)
			if firstRun.Status != model.TaskRunStatusSuccess || firstRun.BackupGenerationState != model.TaskRunGenerationStateVerified {
				t.Fatalf("initial Rclone status=%q state=%q error=%q", firstRun.Status, firstRun.BackupGenerationState, firstRun.LastError)
			}

			if testCase.after != nil {
				testCase.after(t, db, taskEntity)
			}
			invocation := &preChildCancelRcloneExecutor{}
			manager.executorFactory = stubExecutorFactory{executor: invocation}
			secondRunID := createTestTaskRun(t, db, taskEntity.ID, "manual")
			manager.runTask(taskEntity.ID, secondRunID, "manual", generateChainRunID())
			secondRun := waitTaskRunTerminal(t, db, secondRunID)
			if invocation.calls.Load() != 0 {
				t.Fatalf("Rclone %s invoked executor %d time(s)", testCase.name, invocation.calls.Load())
			}
			if secondRun.Status != testCase.wantStatus || secondRun.BackupGenerationState != model.TaskRunGenerationStateNoStart {
				t.Fatalf("Rclone %s status=%q state=%q error=%q, want %q/no_start",
					testCase.name, secondRun.Status, secondRun.BackupGenerationState, secondRun.LastError, testCase.wantStatus)
			}
			loaded, err := manager.loadRestoreTaskWithProvenance(context.Background(), taskEntity.ID)
			if err != nil {
				t.Fatalf("restore after Rclone %s: %v", testCase.name, err)
			}
			if loaded.RsyncCaptureGenerationID != firstRunID {
				t.Fatalf("restore generation=%d after Rclone %s, want prior verified generation %d",
					loaded.RsyncCaptureGenerationID, testCase.name, firstRunID)
			}
		})
	}
}

func TestLegacyRcloneQueuedCancellationPersistsNoStartPostgres(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not configured")
	}
	runLegacyRcloneQueuedCancellation(t, openTaskTerminalPostgresDB(t, dsn))
}

func TestLegacyRcloneDirtyGenerationBlocksFallback(t *testing.T) {
	db := openManagerTestDB(t)
	taskEntity := seedLegacyRcloneTask(t, db)
	policy := model.Policy{Name: "legacy-rclone-generation-policy", MaxRetries: 0, VerifyEnabled: false}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatalf("create Rclone policy: %v", err)
	}
	if err := db.Model(&model.Policy{}).Where("id = ?", policy.ID).
		Updates(map[string]interface{}{"max_retries": 0, "verify_enabled": false}).Error; err != nil {
		t.Fatalf("persist Rclone policy limits: %v", err)
	}
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Update("policy_id", policy.ID).Error; err != nil {
		t.Fatalf("attach Rclone policy: %v", err)
	}
	if err := db.Preload("Node").Preload("Policy").First(&taskEntity, taskEntity.ID).Error; err != nil {
		t.Fatalf("reload Rclone policy task: %v", err)
	}

	exec := &successExecutor{}
	manager := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)
	firstRunID := createTestTaskRun(t, db, taskEntity.ID, "manual")
	manager.runTask(taskEntity.ID, firstRunID, "manual", generateChainRunID())
	firstRun := waitTaskRunTerminal(t, db, firstRunID)
	if firstRun.Status != model.TaskRunStatusSuccess || firstRun.BackupGenerationState != model.TaskRunGenerationStateVerified {
		t.Fatalf("Rclone success status=%q state=%q error=%q", firstRun.Status, firstRun.BackupGenerationState, firstRun.LastError)
	}

	manager.executorFactory = stubExecutorFactory{executor: &failingExecutor{err: errors.New("RCLONE_PARTIAL_WRITE_FOR_TEST_ONLY")}}
	secondRunID := createTestTaskRun(t, db, taskEntity.ID, "manual")
	manager.runTask(taskEntity.ID, secondRunID, "manual", generateChainRunID())
	secondRun := waitTaskRunTerminal(t, db, secondRunID)
	if secondRun.Status != model.TaskRunStatusFailed || secondRun.BackupGenerationState != model.TaskRunGenerationStateDirty {
		t.Fatalf("Rclone failed status=%q state=%q error=%q", secondRun.Status, secondRun.BackupGenerationState, secondRun.LastError)
	}
	if _, err := manager.loadRestoreTaskWithProvenance(context.Background(), taskEntity.ID); !errors.Is(err, ErrRestoreRequiresNewBackup) {
		t.Fatalf("dirty Rclone generation restore error=%v, want ErrRestoreRequiresNewBackup", err)
	}
}

func TestLegacyRcloneKnownFailureAllowsNextWrite(t *testing.T) {
	db := openManagerTestDB(t)
	taskEntity := configureLegacyRclonePolicy(t, db, seedLegacyRcloneTask(t, db), 0)
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)

	firstRunID := createTestTaskRun(t, db, taskEntity.ID, "manual")
	manager.runTask(taskEntity.ID, firstRunID, "manual", generateChainRunID())
	firstRun := waitTaskRunTerminal(t, db, firstRunID)
	if firstRun.Status != model.TaskRunStatusSuccess || firstRun.BackupGenerationState != model.TaskRunGenerationStateVerified {
		t.Fatalf("Rclone initial status=%q state=%q error=%q", firstRun.Status, firstRun.BackupGenerationState, firstRun.LastError)
	}

	manager.executorFactory = stubExecutorFactory{executor: &failingExecutor{err: errors.New("RCLONE_KNOWN_FAILURE_FOR_TEST_ONLY")}}
	failedRunID := createTestTaskRun(t, db, taskEntity.ID, "manual")
	manager.runTask(taskEntity.ID, failedRunID, "manual", generateChainRunID())
	failedRun := waitTaskRunTerminal(t, db, failedRunID)
	if failedRun.Status != model.TaskRunStatusFailed || failedRun.BackupGenerationState != model.TaskRunGenerationStateDirty {
		t.Fatalf("Rclone known failure status=%q state=%q error=%q", failedRun.Status, failedRun.BackupGenerationState, failedRun.LastError)
	}

	success := &successExecutor{}
	manager.executorFactory = stubExecutorFactory{executor: success}
	nextRunID, err := manager.TriggerManual(taskEntity.ID)
	if err != nil || nextRunID == 0 {
		t.Fatalf("Rclone write after known failure run=%d err=%v", nextRunID, err)
	}
	nextRun := waitTaskRunTerminal(t, db, nextRunID)
	if nextRun.Status != model.TaskRunStatusSuccess || nextRun.BackupGenerationState != model.TaskRunGenerationStateVerified {
		t.Fatalf("Rclone next write status=%q state=%q error=%q", nextRun.Status, nextRun.BackupGenerationState, nextRun.LastError)
	}
	if success.Calls() != 1 {
		t.Fatalf("Rclone next write executor calls=%d, want 1", success.Calls())
	}
}

func TestLegacyRcloneUnknownGenerationBlocksRecoveryAndFutureWrites(t *testing.T) {
	db := openManagerTestDB(t)
	taskEntity := configureLegacyRclonePolicy(t, db, seedLegacyRcloneTask(t, db), 3)
	unknown := &unknownRcloneExecutor{}
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)

	firstRunID := createTestTaskRun(t, db, taskEntity.ID, "manual")
	manager.runTask(taskEntity.ID, firstRunID, "manual", generateChainRunID())
	firstRun := waitTaskRunTerminal(t, db, firstRunID)
	if firstRun.Status != model.TaskRunStatusSuccess || firstRun.BackupGenerationState != model.TaskRunGenerationStateVerified {
		t.Fatalf("Rclone initial status=%q state=%q error=%q", firstRun.Status, firstRun.BackupGenerationState, firstRun.LastError)
	}

	manager.executorFactory = stubExecutorFactory{executor: unknown}
	unknownRunID := createTestTaskRun(t, db, taskEntity.ID, "manual")
	manager.runTask(taskEntity.ID, unknownRunID, "manual", generateChainRunID())
	unknownRun := waitTaskRunTerminal(t, db, unknownRunID)
	if unknownRun.Status != model.TaskRunStatusFailed || unknownRun.BackupGenerationState != model.TaskRunGenerationStateUnknown {
		t.Fatalf("Rclone unknown status=%q state=%q error=%q", unknownRun.Status, unknownRun.BackupGenerationState, unknownRun.LastError)
	}
	if _, err := manager.loadRestoreTaskWithProvenance(context.Background(), taskEntity.ID); !errors.Is(err, ErrRestoreRequiresNewBackup) {
		t.Fatalf("unknown Rclone restore error=%v, want ErrRestoreRequiresNewBackup", err)
	}
	var taskAfterUnknown model.Task
	if err := db.First(&taskAfterUnknown, taskEntity.ID).Error; err != nil {
		t.Fatalf("load Rclone task after unknown: %v", err)
	}
	if taskAfterUnknown.Status != string(StatusFailed) {
		t.Fatalf("Rclone unknown aggregate status=%q, want failed (no retry)", taskAfterUnknown.Status)
	}
	if unknown.calls != 1 {
		t.Fatalf("Rclone unknown executor calls=%d, want 1", unknown.calls)
	}

	var beforeCount int64
	if err := db.Model(&model.TaskRun{}).Where("task_id = ?", taskEntity.ID).Count(&beforeCount).Error; err != nil {
		t.Fatalf("count Rclone runs before blocked triggers: %v", err)
	}
	if _, err := manager.TriggerManual(taskEntity.ID); !errors.Is(err, ErrMutableGenerationUnresolved) {
		t.Fatalf("manual write after unknown error=%v, want ErrMutableGenerationUnresolved", err)
	}
	if err := manager.TriggerFromScheduler(taskEntity.ID, time.Now().UTC()); !errors.Is(err, ErrMutableGenerationUnresolved) {
		t.Fatalf("cron write after unknown error=%v, want ErrMutableGenerationUnresolved", err)
	}
	if err := db.Model(&model.TaskRun{}).Where("task_id = ?", taskEntity.ID).Count(&beforeCount).Error; err != nil {
		t.Fatalf("count Rclone runs after blocked triggers: %v", err)
	}
	if beforeCount != 2 {
		t.Fatalf("blocked manual/cron triggers created %d runs, want 2 historical runs", beforeCount)
	}
	if unknown.calls != 1 {
		t.Fatalf("blocked Rclone triggers entered executor calls=%d, want 1", unknown.calls)
	}

	// A config/target edit must not erase the old unresolved evidence or open a
	// bypass that could mix the old mutable remote with a new Task identity.
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Update("rsync_target", filepath.Join(t.TempDir(), "new-target")).Error; err != nil {
		t.Fatalf("change Rclone target identity: %v", err)
	}
	if _, err := manager.TriggerManual(taskEntity.ID); !errors.Is(err, ErrMutableGenerationUnresolved) {
		t.Fatalf("manual write after target edit error=%v, want ErrMutableGenerationUnresolved", err)
	}
	var preserved model.TaskRun
	if err := db.First(&preserved, unknownRunID).Error; err != nil {
		t.Fatalf("reload unresolved Rclone evidence: %v", err)
	}
	if preserved.BackupGenerationState != model.TaskRunGenerationStateUnknown {
		t.Fatalf("target edit erased unresolved state=%q", preserved.BackupGenerationState)
	}
}

func TestLegacyRcloneArmCrashRecoveryKeepsWriteHold(t *testing.T) {
	db := openManagerTestDB(t)
	taskEntity := seedLegacyRcloneTask(t, db)
	exec := &successExecutor{}
	manager := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)

	startedAt := time.Now().UTC().Add(-3 * time.Minute)
	expiredLease := time.Now().UTC().Add(-time.Minute)
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Updates(map[string]interface{}{
		"status": string(StatusRunning), "last_run_at": &startedAt,
	}).Error; err != nil {
		t.Fatalf("mark Rclone crash task running: %v", err)
	}
	crashed := model.TaskRun{
		TaskID:                  taskEntity.ID,
		NodeIDSnapshot:          taskEntity.NodeID,
		TriggerType:             "manual",
		Status:                  model.TaskRunStatusRunning,
		BackupConfigFingerprint: model.TaskRunBackupConfigFingerprint(taskEntity),
		BackupGenerationState:   model.TaskRunGenerationStateWriting,
		ExecutionOwnerID:        "lost-rclone-process",
		ExecutionLeaseUntil:     &expiredLease,
		StartedAt:               &startedAt,
	}
	if err := db.Create(&crashed).Error; err != nil {
		t.Fatalf("create crashed Rclone run: %v", err)
	}
	if err := manager.reconcileExpiredOrdinaryRuns(context.Background()); err != nil {
		t.Fatalf("recover crashed Rclone run: %v", err)
	}
	var recovered model.TaskRun
	if err := db.First(&recovered, crashed.ID).Error; err != nil {
		t.Fatalf("reload recovered Rclone run: %v", err)
	}
	if recovered.Status != model.TaskRunStatusFailed || recovered.BackupGenerationState != model.TaskRunGenerationStateWriting {
		t.Fatalf("recovered Rclone status=%q state=%q, want failed/writing", recovered.Status, recovered.BackupGenerationState)
	}

	if _, err := manager.TriggerManual(taskEntity.ID); !errors.Is(err, ErrMutableGenerationUnresolved) {
		t.Fatalf("write after crash recovery error=%v, want ErrMutableGenerationUnresolved", err)
	}
	if exec.Calls() != 0 {
		t.Fatalf("write after crash recovery entered executor calls=%d, want 0", exec.Calls())
	}
}

func TestLegacyRcloneNoStartPreservesVerifiedGeneration(t *testing.T) {
	db := openManagerTestDB(t)
	taskEntity := seedLegacyRcloneTask(t, db)
	policy := model.Policy{Name: "legacy-rclone-no-start-policy", MaxRetries: 0, VerifyEnabled: false}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatalf("create Rclone no-start policy: %v", err)
	}
	if err := db.Model(&model.Policy{}).Where("id = ?", policy.ID).
		Updates(map[string]interface{}{"max_retries": 0, "verify_enabled": false}).Error; err != nil {
		t.Fatalf("persist Rclone no-start policy limits: %v", err)
	}
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Update("policy_id", policy.ID).Error; err != nil {
		t.Fatalf("attach Rclone no-start policy: %v", err)
	}
	if err := db.Preload("Node").Preload("Policy").First(&taskEntity, taskEntity.ID).Error; err != nil {
		t.Fatalf("reload Rclone no-start task: %v", err)
	}

	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)
	firstRunID := createTestTaskRun(t, db, taskEntity.ID, "manual")
	manager.runTask(taskEntity.ID, firstRunID, "manual", generateChainRunID())
	firstRun := waitTaskRunTerminal(t, db, firstRunID)
	if firstRun.Status != model.TaskRunStatusSuccess || firstRun.BackupGenerationState != model.TaskRunGenerationStateVerified {
		t.Fatalf("Rclone initial status=%q state=%q error=%q", firstRun.Status, firstRun.BackupGenerationState, firstRun.LastError)
	}

	manager.executorFactory = stubExecutorFactory{executor: &noStartCompatibilityExecutor{err: errors.New("RCLONE_NO_START_FOR_TEST_ONLY")}}
	secondRunID := createTestTaskRun(t, db, taskEntity.ID, "manual")
	manager.runTask(taskEntity.ID, secondRunID, "manual", generateChainRunID())
	secondRun := waitTaskRunTerminal(t, db, secondRunID)
	if secondRun.Status != model.TaskRunStatusFailed || secondRun.BackupGenerationState != model.TaskRunGenerationStateNoStart {
		t.Fatalf("Rclone no-start status=%q state=%q error=%q", secondRun.Status, secondRun.BackupGenerationState, secondRun.LastError)
	}
	loaded, err := manager.loadRestoreTaskWithProvenance(context.Background(), taskEntity.ID)
	if err != nil {
		t.Fatalf("verified Rclone generation displaced by no-start: %v", err)
	}
	if loaded.RsyncCaptureGenerationID != firstRunID {
		t.Fatalf("Rclone restore generation=%d, want %d", loaded.RsyncCaptureGenerationID, firstRunID)
	}
}

func TestLegacyRcloneCanceledEmptyHeadBlocksFallback(t *testing.T) {
	db := openManagerTestDB(t)
	taskEntity := configureLegacyRclonePolicy(t, db, seedLegacyRcloneTask(t, db), 0)
	old := time.Now().UTC().Add(-2 * time.Hour)
	seedRcloneGeneration(t, db, taskEntity, model.TaskRunStatusSuccess, model.TaskRunGenerationStateVerified, "", old)
	seedRcloneGeneration(t, db, taskEntity, model.TaskRunStatusCanceled, "", "stale wording", old.Add(time.Minute))

	if _, err := (&Manager{db: db}).loadRestoreTaskWithProvenance(context.Background(), taskEntity.ID); !errors.Is(err, ErrRestoreRequiresNewBackup) {
		t.Fatalf("canceled empty Rclone head error=%v, want ErrRestoreRequiresNewBackup", err)
	}
}

func TestLegacyRcloneNoStartDoesNotSkipAmbiguousEmptyHead(t *testing.T) {
	db := openManagerTestDB(t)
	taskEntity := configureLegacyRclonePolicy(t, db, seedLegacyRcloneTask(t, db), 0)
	old := time.Now().UTC().Add(-3 * time.Hour)
	seedRcloneGeneration(t, db, taskEntity, model.TaskRunStatusSuccess, model.TaskRunGenerationStateVerified, "", old)
	ambiguous := seedRcloneGeneration(t, db, taskEntity, model.TaskRunStatusFailed, "", "ambiguous partial write", old.Add(time.Minute))
	seedRcloneGeneration(t, db, taskEntity, model.TaskRunStatusFailed, model.TaskRunGenerationStateNoStart, "", old.Add(2*time.Minute))

	selection, err := latestRcloneMutableGeneration(db, taskEntity.ID, taskEntity.NodeID)
	if err != nil {
		t.Fatalf("select Rclone generation after no-start: %v", err)
	}
	if selection.run.ID != ambiguous.ID || selection.allowEmpty {
		t.Fatalf("Rclone no-start skipped ambiguous head: selected=%d allowEmpty=%v, want %d/false", selection.run.ID, selection.allowEmpty, ambiguous.ID)
	}
	if _, err := (&Manager{db: db}).loadRestoreTaskWithProvenance(context.Background(), taskEntity.ID); !errors.Is(err, ErrRestoreRequiresNewBackup) {
		t.Fatalf("Rclone no-start after ambiguous head error=%v, want ErrRestoreRequiresNewBackup", err)
	}
}

func TestLegacyRcloneNoStartDeadlinePersistsProof(t *testing.T) {
	db := openManagerTestDB(t)
	taskEntity := configureLegacyRclonePolicy(t, db, seedLegacyRcloneTask(t, db), 0)
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)

	firstRunID := createTestTaskRun(t, db, taskEntity.ID, "manual")
	manager.runTask(taskEntity.ID, firstRunID, "manual", generateChainRunID())
	firstRun := waitTaskRunTerminal(t, db, firstRunID)
	if firstRun.Status != model.TaskRunStatusSuccess || firstRun.BackupGenerationState != model.TaskRunGenerationStateVerified {
		t.Fatalf("Rclone initial status=%q state=%q error=%q", firstRun.Status, firstRun.BackupGenerationState, firstRun.LastError)
	}

	manager.executorFactory = stubExecutorFactory{executor: &noStartDeadlineRcloneExecutor{}}
	noStartRunID := createTestTaskRun(t, db, taskEntity.ID, "manual")
	manager.runTask(taskEntity.ID, noStartRunID, "manual", generateChainRunID())
	noStartRun := waitTaskRunTerminal(t, db, noStartRunID)
	if noStartRun.Status != model.TaskRunStatusFailed || noStartRun.BackupGenerationState != model.TaskRunGenerationStateNoStart {
		t.Fatalf("Rclone deadline no-start status=%q state=%q error=%q", noStartRun.Status, noStartRun.BackupGenerationState, noStartRun.LastError)
	}
	if _, err := manager.loadRestoreTaskWithProvenance(context.Background(), taskEntity.ID); err != nil {
		t.Fatalf("durable Rclone no-start proof displaced verified generation: %v", err)
	}
}

func TestLegacyRcloneNoStartPreservesPreUpgradeSuccess(t *testing.T) {
	db := openManagerTestDB(t)
	taskEntity := configureLegacyRclonePolicy(t, db, seedLegacyRcloneTask(t, db), 0)
	old := time.Now().UTC().Add(-2 * time.Hour)
	legacy := seedRcloneGeneration(t, db, taskEntity, model.TaskRunStatusSuccess, "", "", old)
	seedRcloneGeneration(t, db, taskEntity, model.TaskRunStatusFailed, model.TaskRunGenerationStateNoStart, "", old.Add(time.Minute))

	loaded, err := (&Manager{db: db}).loadRestoreTaskWithProvenance(context.Background(), taskEntity.ID)
	if err != nil || loaded.RsyncCaptureGenerationID != legacy.ID {
		t.Fatalf("Rclone no-start legacy restore generation=%d err=%v, want %d", loaded.RsyncCaptureGenerationID, err, legacy.ID)
	}
}

func TestLegacyRclonePreUpgradeFailedHeadBlocksFallback(t *testing.T) {
	db := openManagerTestDB(t)
	taskEntity := seedLegacyRcloneTask(t, db)
	policy := model.Policy{Name: "legacy-rclone-pre-upgrade-policy", MaxRetries: 0, VerifyEnabled: false}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatalf("create pre-upgrade Rclone policy: %v", err)
	}
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Update("policy_id", policy.ID).Error; err != nil {
		t.Fatalf("attach pre-upgrade Rclone policy: %v", err)
	}
	if err := db.Preload("Node").Preload("Policy").First(&taskEntity, taskEntity.ID).Error; err != nil {
		t.Fatalf("reload pre-upgrade Rclone task: %v", err)
	}
	old := time.Now().UTC().Add(-2 * time.Hour)
	seedRcloneGeneration(t, db, taskEntity, model.TaskRunStatusSuccess, "", "", old)
	seedRcloneGeneration(t, db, taskEntity, model.TaskRunStatusFailed, "", "untracked partial write", old.Add(time.Minute))

	manager := &Manager{db: db}
	if _, err := manager.loadRestoreTaskWithProvenance(context.Background(), taskEntity.ID); !errors.Is(err, ErrRestoreRequiresNewBackup) {
		t.Fatalf("pre-upgrade failed Rclone head error=%v, want ErrRestoreRequiresNewBackup", err)
	}
}

func TestLegacyRcloneRestoreReservationRevalidatesLatestHead(t *testing.T) {
	db := openManagerTestDB(t)
	taskEntity := seedLegacyRcloneTask(t, db)
	policy := model.Policy{Name: "legacy-rclone-reservation-policy", MaxRetries: 0, VerifyEnabled: false}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatalf("create reservation Rclone policy: %v", err)
	}
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Update("policy_id", policy.ID).Error; err != nil {
		t.Fatalf("attach reservation Rclone policy: %v", err)
	}
	if err := db.Preload("Node").Preload("Policy").First(&taskEntity, taskEntity.ID).Error; err != nil {
		t.Fatalf("reload reservation Rclone task: %v", err)
	}
	old := time.Now().UTC().Add(-2 * time.Hour)
	first := seedRcloneGeneration(t, db, taskEntity, model.TaskRunStatusSuccess, model.TaskRunGenerationStateVerified, "", old)
	loaded, err := (&Manager{db: db}).loadRestoreTaskWithProvenance(context.Background(), taskEntity.ID)
	if err != nil || loaded.RsyncCaptureGenerationID != first.ID {
		t.Fatalf("initial Rclone restore provenance id=%d err=%v, want %d", loaded.RsyncCaptureGenerationID, err, first.ID)
	}
	seedRcloneGeneration(t, db, taskEntity, model.TaskRunStatusSuccess, model.TaskRunGenerationStateVerified, "", old.Add(time.Minute))

	_, err = (&Manager{db: db}).reserveTaskRun(context.Background(), taskEntity.NodeID, model.TaskRun{
		TaskID:            taskEntity.ID,
		TriggerType:       "restore",
		Status:            model.TaskRunStatusPending,
		BackupSourceRunID: first.ID,
	})
	if !errors.Is(err, ErrRestoreRequiresNewBackup) {
		t.Fatalf("stale Rclone restore reservation error=%v, want ErrRestoreRequiresNewBackup", err)
	}
}

func TestLegacyRcloneActualPartialWriteHead(t *testing.T) {
	officialBinary := os.Getenv("RCLONE_TEST_BINARY")
	if officialBinary == "" {
		var lookupErr error
		officialBinary, lookupErr = exec.LookPath("rclone")
		if lookupErr != nil {
			t.Skipf("official Rclone binary is unavailable: %v", lookupErr)
		}
	}
	if _, err := os.Stat(officialBinary); err != nil {
		t.Skipf("official Rclone binary is unavailable: %v", err)
	}
	if os.Geteuid() == 0 {
		t.Skip("partial unreadable-source fixture requires a non-root test user")
	}
	sshdBinary, err := exec.LookPath("sshd")
	if err != nil {
		t.Skip("sshd is not installed")
	}
	t.Setenv("SSH_STRICT_HOST_KEY_CHECKING", "false")
	t.Setenv("SSH_AUTO_ACCEPT_NEW_HOSTS", "false")

	node := testutil.StartRsyncSSHServer(t, sshdBinary)
	db := openManagerTestDB(t)
	node.Name = "legacy-rclone-real-node"
	node.BackupDir = t.TempDir()
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("create Rclone SSH node: %v", err)
	}
	source := filepath.Join(t.TempDir(), "source")
	target := filepath.Join(t.TempDir(), "target")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("create Rclone source: %v", err)
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("create Rclone target: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "z-unreadable.txt"), []byte("b-v1"), 0o644); err != nil {
		t.Fatalf("seed Rclone source unreadable file: %v", err)
	}
	taskEntity := model.Task{
		Name:           "legacy-rclone-real",
		NodeID:         node.ID,
		ExecutorType:   "rclone",
		ExecutorConfig: `{"version":1,"transfers":1}`,
		RsyncSource:    source,
		RsyncTarget:    target,
		Status:         string(StatusPending),
	}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatalf("create real Rclone task: %v", err)
	}
	policy := model.Policy{Name: "legacy-rclone-real-policy", MaxRetries: 0, VerifyEnabled: false}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatalf("create real Rclone policy: %v", err)
	}
	if err := db.Model(&model.Policy{}).Where("id = ?", policy.ID).
		Updates(map[string]interface{}{"max_retries": 0, "verify_enabled": false}).Error; err != nil {
		t.Fatalf("persist real Rclone policy limits: %v", err)
	}
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Update("policy_id", policy.ID).Error; err != nil {
		t.Fatalf("attach real Rclone policy: %v", err)
	}
	if err := db.Preload("Node").Preload("Policy").First(&taskEntity, taskEntity.ID).Error; err != nil {
		t.Fatalf("reload real Rclone task: %v", err)
	}

	t.Setenv("RCLONE_BINARY", officialBinary)

	manager := NewManager(db, taskexec.NewFactory("rsync"), nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)
	firstRunID := createTestTaskRun(t, db, taskEntity.ID, "manual")
	manager.runTask(taskEntity.ID, firstRunID, "manual", generateChainRunID())
	firstRun := waitTaskRunTerminal(t, db, firstRunID)
	if firstRun.Status != model.TaskRunStatusSuccess || firstRun.BackupGenerationState != model.TaskRunGenerationStateVerified {
		t.Fatalf("real Rclone initial status=%q state=%q error=%q", firstRun.Status, firstRun.BackupGenerationState, firstRun.LastError)
	}

	if err := os.WriteFile(filepath.Join(source, "a.txt"), []byte("a-v2"), 0o644); err != nil {
		t.Fatalf("mutate Rclone source a: %v", err)
	}
	unreadableSource := filepath.Join(source, "z-unreadable.txt")
	if err := os.WriteFile(unreadableSource, []byte("b-v2-expanded"), 0o644); err != nil {
		t.Fatalf("mutate Rclone unreadable source file: %v", err)
	}
	if err := os.Chmod(unreadableSource, 0); err != nil {
		t.Fatalf("make Rclone source file unreadable: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(unreadableSource, 0o644) })
	secondRunID := createTestTaskRun(t, db, taskEntity.ID, "manual")
	manager.runTask(taskEntity.ID, secondRunID, "manual", generateChainRunID())
	secondRun := waitTaskRunTerminal(t, db, secondRunID)
	if secondRun.Status != model.TaskRunStatusFailed || secondRun.BackupGenerationState != model.TaskRunGenerationStateDirty {
		t.Fatalf("real Rclone partial status=%q state=%q error=%q", secondRun.Status, secondRun.BackupGenerationState, secondRun.LastError)
	}
	if data, err := os.ReadFile(filepath.Join(target, "a.txt")); err != nil || string(data) != "a-v2" {
		t.Fatalf("partial Rclone write a=%q err=%v, want a-v2", string(data), err)
	}
	if data, err := os.ReadFile(filepath.Join(target, "z-unreadable.txt")); err != nil || string(data) != "b-v1" {
		t.Fatalf("partial Rclone write unreadable file=%q err=%v, want b-v1", string(data), err)
	}
	if _, err := manager.loadRestoreTaskWithProvenance(context.Background(), taskEntity.ID); !errors.Is(err, ErrRestoreRequiresNewBackup) {
		t.Fatalf("real Rclone dirty restore error=%v, want ErrRestoreRequiresNewBackup", err)
	}
	if err := os.Chmod(unreadableSource, 0o644); err != nil {
		t.Fatalf("restore Rclone source permissions: %v", err)
	}

	thirdRunID := createTestTaskRun(t, db, taskEntity.ID, "manual")
	manager.runTask(taskEntity.ID, thirdRunID, "manual", generateChainRunID())
	thirdRun := waitTaskRunTerminal(t, db, thirdRunID)
	if thirdRun.Status != model.TaskRunStatusSuccess || thirdRun.BackupGenerationState != model.TaskRunGenerationStateVerified {
		t.Fatalf("real Rclone complete status=%q state=%q error=%q", thirdRun.Status, thirdRun.BackupGenerationState, thirdRun.LastError)
	}
	loaded, err := manager.loadRestoreTaskWithProvenance(context.Background(), taskEntity.ID)
	if err != nil {
		t.Fatalf("real Rclone complete restore provenance: %v", err)
	}
	if loaded.RsyncCaptureGenerationID != thirdRunID {
		t.Fatalf("real Rclone restore generation=%d, want %d", loaded.RsyncCaptureGenerationID, thirdRunID)
	}
}

var _ taskexec.Executor = (*noStartCompatibilityExecutor)(nil)
