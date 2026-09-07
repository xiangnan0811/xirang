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

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/catalog"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/publication"
	backuprepository "xirang/backend/internal/backupasset/repository"
	"xirang/backend/internal/model"
	"xirang/backend/internal/settings"
	taskexec "xirang/backend/internal/task/executor"

	"gorm.io/gorm"
)

type sourceCompletionObserverFake struct {
	calls    atomic.Int32
	lastTask atomic.Uint32

	entered   chan struct{}
	done      chan struct{}
	enterOnce sync.Once
	doneOnce  sync.Once

	before   func(context.Context, uint)
	block    bool
	delegate func(context.Context, uint) error
	err      error
}

func (observer *sourceCompletionObserverFake) ObserveBackupSourceCompletion(ctx context.Context, taskID uint) error {
	observer.calls.Add(1)
	observer.lastTask.Store(uint32(taskID))
	if observer.entered != nil {
		observer.enterOnce.Do(func() { close(observer.entered) })
	}
	if observer.before != nil {
		observer.before(ctx, taskID)
	}
	var err error
	if observer.block {
		select {
		case <-ctx.Done():
			err = ctx.Err()
		case <-observer.done:
		}
	} else if observer.delegate != nil {
		err = observer.delegate(ctx, taskID)
	} else {
		err = observer.err
	}
	if observer.done != nil {
		observer.doneOnce.Do(func() { close(observer.done) })
	}
	return err
}

type sourceCompletionFailExecutor struct{}

func (*sourceCompletionFailExecutor) Run(context.Context, model.Task, taskexec.LogFunc, taskexec.ProgressFunc) (int, error) {
	return 1, errors.New("FAKE_RSYNC_TRANSFER_FAILURE_FOR_TEST_ONLY")
}

func completionPolicy(t *testing.T, db *gorm.DB, taskID uint, verify bool, postHook string) model.Policy {
	t.Helper()
	policy := model.Policy{
		Name:               fmt.Sprintf("source-completion-%s", strings.ReplaceAll(t.Name(), "/", "-")),
		SourcePath:         "/source",
		TargetPath:         "/target",
		VerifyEnabled:      verify,
		VerifySampleRate:   0,
		PostHook:           postHook,
		HookTimeoutSeconds: 2,
		MaxRetries:         0,
	}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatalf("create completion policy: %v", err)
	}
	if err := db.Model(&model.Policy{}).Where("id = ?", policy.ID).
		Updates(map[string]any{"verify_enabled": verify, "max_retries": 0}).Error; err != nil {
		t.Fatalf("persist completion policy controls: %v", err)
	}
	if err := db.Model(&model.Task{}).Where("id = ?", taskID).Update("policy_id", policy.ID).Error; err != nil {
		t.Fatalf("attach completion policy: %v", err)
	}
	return policy
}

func loadSourceCompletionState(t *testing.T, db *gorm.DB, taskID, runID uint) (model.Task, model.TaskRun) {
	t.Helper()
	var taskEntity model.Task
	if err := db.First(&taskEntity, taskID).Error; err != nil {
		t.Fatalf("load completion Task: %v", err)
	}
	var run model.TaskRun
	if err := db.First(&run, runID).Error; err != nil {
		t.Fatalf("load completion TaskRun: %v", err)
	}
	return taskEntity, run
}

func TestRunTaskObservesLegacyRsyncSourceAfterPostHookBeforeSuccess(t *testing.T) {
	db := openManagerTestDB(t)
	target := t.TempDir()
	node := model.Node{Name: "source-completion-node", Host: "example.invalid", Port: 22, Username: "reader", AuthType: "password", Password: "FAKE_NODE_PASSWORD_FOR_TEST_ONLY"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	taskEntity := model.Task{
		Name: "source-completion-task", NodeID: node.ID, ExecutorType: "rsync", Status: string(StatusPending), Enabled: true,
		RsyncSource: "/source", RsyncTarget: target,
	}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatal(err)
	}
	completionPolicy(t, db, taskEntity.ID, false, "write-completion-marker")

	markerPath := filepath.Join(target, "post-hook-complete")
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)
	manager.hookRunFunc = func(_ context.Context, _ model.Task, command string) error {
		if command != "write-completion-marker" {
			return fmt.Errorf("unexpected post-hook %q", command)
		}
		return os.WriteFile(markerPath, []byte("post-hook-final-content"), 0o600)
	}

	var observedTaskStatus atomic.Value
	var observedRunStatus atomic.Value
	observer := &sourceCompletionObserverFake{
		before: func(_ context.Context, observedTaskID uint) {
			if observedTaskID != taskEntity.ID {
				t.Errorf("observer task id=%d, want %d", observedTaskID, taskEntity.ID)
			}
			if _, err := os.Stat(markerPath); err != nil {
				t.Errorf("observer ran before post-hook marker: %v", err)
			}
			var currentTask model.Task
			if err := db.First(&currentTask, taskEntity.ID).Error; err != nil {
				t.Errorf("load Task from observer: %v", err)
			} else {
				observedTaskStatus.Store(currentTask.Status)
			}
			var currentRun model.TaskRun
			if err := db.Where("task_id = ?", taskEntity.ID).Order("id DESC").First(&currentRun).Error; err != nil {
				t.Errorf("load TaskRun from observer: %v", err)
			} else {
				observedRunStatus.Store(currentRun.Status)
			}
		},
	}
	manager.SetBackupSourceCompletionObserver(observer)

	runID := createTestTaskRun(t, db, taskEntity.ID, "manual")
	manager.runTask(taskEntity.ID, runID, "manual", generateChainRunID())
	storedTask, storedRun := loadSourceCompletionState(t, db, taskEntity.ID, runID)

	if storedTask.Status != string(StatusSuccess) || storedRun.Status != model.TaskRunStatusSuccess {
		t.Fatalf("successful legacy Rsync state Task=%q TaskRun=%q task_error=%q run_error=%q verify=%q", storedTask.Status, storedRun.Status, storedTask.LastError, storedRun.LastError, storedRun.VerifyStatus)
	}
	if got := observer.calls.Load(); got != 1 || observer.lastTask.Load() != uint32(taskEntity.ID) {
		t.Fatalf("observer calls=%d task=%d, want one exact call for %d", got, observer.lastTask.Load(), taskEntity.ID)
	}
	if got := observedTaskStatus.Load(); got != string(StatusRunning) {
		t.Fatalf("observer saw Task status=%v, want running before terminal success", got)
	}
	if got := observedRunStatus.Load(); got != model.TaskRunStatusRunning {
		t.Fatalf("observer saw TaskRun status=%v, want running before terminal success", got)
	}
	contents, err := os.ReadFile(markerPath)
	if err != nil || string(contents) != "post-hook-final-content" {
		t.Fatalf("post-hook marker contents=%q err=%v", string(contents), err)
	}
}

func TestRunTaskObservesBeforeVerificationWarning(t *testing.T) {
	db := openManagerTestDB(t)
	taskEntity := seedTaskForManagerTest(t, db)
	completionPolicy(t, db, taskEntity.ID, true, "")
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)

	var orderingViolation atomic.Bool
	observer := &sourceCompletionObserverFake{
		before: func(_ context.Context, observedTaskID uint) {
			var currentTask model.Task
			var currentRun model.TaskRun
			if err := db.First(&currentTask, observedTaskID).Error; err != nil {
				orderingViolation.Store(true)
				return
			}
			if err := db.Where("task_id = ?", observedTaskID).Order("id DESC").First(&currentRun).Error; err != nil {
				orderingViolation.Store(true)
				return
			}
			if currentTask.Status != string(StatusRunning) || currentRun.Status != model.TaskRunStatusRunning {
				orderingViolation.Store(true)
			}
		},
	}
	manager.SetBackupSourceCompletionObserver(observer)

	runID := createTestTaskRun(t, db, taskEntity.ID, "manual")
	manager.runTask(taskEntity.ID, runID, "manual", generateChainRunID())

	storedTask, storedRun := loadSourceCompletionState(t, db, taskEntity.ID, runID)
	if orderingViolation.Load() {
		t.Fatal("completion observer ran after the verification warning or another terminal state was advertised")
	}
	if observer.calls.Load() != 1 {
		t.Fatalf("verification-warning observer calls=%d, want 1", observer.calls.Load())
	}
	if storedTask.Status != string(StatusWarning) || storedRun.Status != model.TaskRunStatusWarning || storedRun.VerifyStatus != "warning" {
		t.Fatalf("verification-warning state Task=%q TaskRun=%q verify=%q", storedTask.Status, storedRun.Status, storedRun.VerifyStatus)
	}
}

func TestRunTaskObserverFailureDoesNotRewriteSuccessfulBackup(t *testing.T) {
	db := openManagerTestDB(t)
	taskEntity := seedTaskForManagerTest(t, db)
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)
	observer := &sourceCompletionObserverFake{err: errors.New("FAKE_CATALOG_OBSERVER_FAILURE_FOR_TEST_ONLY")}
	manager.SetBackupSourceCompletionObserver(observer)

	runID := createTestTaskRun(t, db, taskEntity.ID, "manual")
	manager.runTask(taskEntity.ID, runID, "manual", generateChainRunID())

	storedTask, storedRun := loadSourceCompletionState(t, db, taskEntity.ID, runID)
	if observer.calls.Load() != 1 {
		t.Fatalf("observer failure calls=%d, want 1", observer.calls.Load())
	}
	if storedTask.Status != string(StatusSuccess) || storedRun.Status != model.TaskRunStatusSuccess {
		t.Fatalf("observer failure rewrote successful result Task=%q TaskRun=%q", storedTask.Status, storedRun.Status)
	}
	if storedTask.LastError != "" || storedRun.LastError != "" {
		t.Fatalf("observer failure leaked into successful backup errors Task=%q TaskRun=%q", storedTask.LastError, storedRun.LastError)
	}
}

func TestRunTaskDoesNotObserveNonRsyncManagedOrFailedExecutions(t *testing.T) {
	t.Run("non-rsync", func(t *testing.T) {
		db := openManagerTestDB(t)
		taskEntity := seedTaskForManagerTest(t, db)
		if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Update("executor_type", "command").Error; err != nil {
			t.Fatal(err)
		}
		manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
		shutdownManagerOnCleanup(t, manager)
		observer := &sourceCompletionObserverFake{}
		manager.SetBackupSourceCompletionObserver(observer)
		runID := createTestTaskRun(t, db, taskEntity.ID, "manual")
		manager.runTask(taskEntity.ID, runID, "manual", generateChainRunID())
		storedTask, storedRun := loadSourceCompletionState(t, db, taskEntity.ID, runID)
		if observer.calls.Load() != 0 {
			t.Fatalf("non-Rsync observer calls=%d, want 0", observer.calls.Load())
		}
		if storedTask.Status != string(StatusSuccess) || storedRun.Status != model.TaskRunStatusSuccess {
			t.Fatalf("non-Rsync state Task=%q TaskRun=%q", storedTask.Status, storedRun.Status)
		}
	})

	t.Run("managed-rsync", func(t *testing.T) {
		db := openManagerTestDB(t)
		taskEntity := seedTaskForManagerTest(t, db)
		runID := createTestTaskRun(t, db, taskEntity.ID, "manual")
		attempt := publicationRsyncAttempt(taskEntity.ID, runID)
		commit := provider.NewRsyncTreeProviderCommit(provider.RsyncTreeCommitV1{
			LayoutVersion: 1, RepositoryID: attempt.RsyncTree.RepositoryID, TaskRepositoryLinkID: attempt.RsyncTree.TaskRepositoryLinkID,
			RecoveryPointID: attempt.RsyncTree.RecoveryPointID, AttemptID: attempt.RsyncTree.AttemptID, PublicationMode: attempt.RsyncTree.PublicationMode,
			ManifestDigestAlgorithm: "sha256", ManifestDigest: strings.Repeat("a", 64), ManifestEntryCount: 1, LogicalBytes: 1,
			FidelityDigest: strings.Repeat("b", 64), SourceFingerprint: strings.Repeat("c", 64), ProviderCommittedAt: time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC),
			CommitMarkerDigest: strings.Repeat("d", 64), ChildFenceDigest: strings.Repeat("e", 64), PointDeadlineAt: attempt.RsyncTree.PointDeadlineAt,
			RenameVerified: true, DirectoryFsyncVerified: true,
		})
		session := &publicationExecutionFake{
			mode: publication.ModeEvidence, attempt: attempt,
			rsyncInput: &provider.RsyncTreePublicationInput{
				ManagedRoot: t.TempDir(), MarkerKey: []byte("FAKE_RSYNC_MARKER_KEY_FOR_COMPLETION_TEST"),
				SourceFingerprint: strings.Repeat("c", 64), ChildFenceDigest: strings.Repeat("e", 64),
				ManifestLimits: provider.ManifestLimits{Timeout: time.Minute, MaxBytes: 1 << 20, MaxEntries: 10, MaxRecordBytes: 1024, MaxDepth: 8}, MaxCommandOutputBytes: 1 << 20,
			},
		}
		evidence := &evidenceExecutorFake{result: taskexec.PublicationExecutionResult{ExitCode: 0, Completion: backupasset.CompletionKnownExitZero, ProviderCommit: &commit}}
		manager := NewManager(db, executorFactoryFake{executor: evidence}, nil, nil, nil, nil, 8, 90)
		manager.publicationCoordinator = &publicationCoordinatorFake{execution: session}
		shutdownManagerOnCleanup(t, manager)
		observer := &sourceCompletionObserverFake{}
		manager.SetBackupSourceCompletionObserver(observer)
		manager.runTask(taskEntity.ID, runID, "manual", generateChainRunID())
		storedTask, storedRun := loadSourceCompletionState(t, db, taskEntity.ID, runID)
		if observer.calls.Load() != 0 {
			t.Fatalf("managed Rsync observer calls=%d, want 0", observer.calls.Load())
		}
		if storedTask.Status != string(StatusSuccess) || storedRun.Status != model.TaskRunStatusSuccess {
			t.Fatalf("managed Rsync state Task=%q TaskRun=%q", storedTask.Status, storedRun.Status)
		}
	})

	t.Run("transfer-failure", func(t *testing.T) {
		db := openManagerTestDB(t)
		taskEntity := seedTaskForManagerTest(t, db)
		policy := completionPolicy(t, db, taskEntity.ID, false, "")
		if err := db.Model(&model.Policy{}).Where("id = ?", policy.ID).UpdateColumn("max_retries", 1).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).UpdateColumn("retry_count", 1).Error; err != nil {
			t.Fatal(err)
		}
		manager := NewManager(db, stubExecutorFactory{executor: &sourceCompletionFailExecutor{}}, nil, nil, nil, nil, 8, 90)
		shutdownManagerOnCleanup(t, manager)
		observer := &sourceCompletionObserverFake{}
		manager.SetBackupSourceCompletionObserver(observer)
		runID := createTestTaskRun(t, db, taskEntity.ID, "manual")
		manager.runTask(taskEntity.ID, runID, "manual", generateChainRunID())
		storedTask, storedRun := loadSourceCompletionState(t, db, taskEntity.ID, runID)
		if observer.calls.Load() != 0 {
			t.Fatalf("failed transfer observer calls=%d, want 0", observer.calls.Load())
		}
		if storedTask.Status != string(StatusFailed) || storedRun.Status != model.TaskRunStatusFailed {
			t.Fatalf("failed transfer state Task=%q TaskRun=%q task_error=%q run_error=%q retries=%d", storedTask.Status, storedRun.Status, storedTask.LastError, storedRun.LastError, storedTask.RetryCount)
		}
	})
}

func TestRunTaskCancellationDuringSourceCompletionCancelsTask(t *testing.T) {
	db := openManagerTestDB(t)
	taskEntity := seedTaskForManagerTest(t, db)
	completionPolicy(t, db, taskEntity.ID, false, "")
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)
	entered := make(chan struct{})
	observerDone := make(chan struct{})
	observer := &sourceCompletionObserverFake{entered: entered, done: observerDone, block: true}
	manager.SetBackupSourceCompletionObserver(observer)
	runID := createTestTaskRun(t, db, taskEntity.ID, "manual")
	runDone := make(chan struct{})
	go func() {
		manager.runTask(taskEntity.ID, runID, "manual", generateChainRunID())
		close(runDone)
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("source completion observer did not start")
	}
	if err := manager.Cancel(taskEntity.ID); err != nil {
		t.Fatalf("cancel task during source completion: %v", err)
	}
	select {
	case <-observerDone:
	case <-time.After(3 * time.Second):
		t.Fatal("source completion observer did not honor cancellation")
	}
	select {
	case <-runDone:
	case <-time.After(3 * time.Second):
		t.Fatal("task runner remained blocked after source completion cancellation")
	}
	storedTask, storedRun := loadSourceCompletionState(t, db, taskEntity.ID, runID)
	if storedTask.Status != string(StatusCanceled) || storedRun.Status != model.TaskRunStatusCanceled {
		t.Fatalf("canceled source completion state Task=%q TaskRun=%q", storedTask.Status, storedRun.Status)
	}
	if storedRun.LastError != "任务已取消" || storedTask.LastError != "任务已取消" {
		t.Fatalf("canceled source completion errors Task=%q TaskRun=%q", storedTask.LastError, storedRun.LastError)
	}
}

type completionSettings map[string]string

func (settings completionSettings) GetEffective(key string) string {
	return settings[key]
}

func completionFoundationSettings() completionSettings {
	values := completionSettings{}
	for _, definition := range settings.NewService(nil).Registry() {
		if strings.HasPrefix(definition.Key, "backup_assets.") {
			values[definition.Key] = definition.CodeDefault
		}
	}
	values["backup_assets.enabled"] = "true"
	return values
}

type completionProbe struct {
	markerPath    string
	revision      string
	requireMarker atomic.Bool
	calls         atomic.Int32
	sawMarker     atomic.Bool
}

func (probe *completionProbe) Probe(_ context.Context, binding provider.AccessBinding, _ provider.OperationLimits) (provider.RepositoryObservation, error) {
	probe.calls.Add(1)
	if probe.requireMarker.Load() {
		contents, err := os.ReadFile(probe.markerPath)
		if err != nil {
			return provider.RepositoryObservation{}, fmt.Errorf("post-hook marker unavailable: %w", err)
		}
		if string(contents) != "post-hook-final-content" {
			return provider.RepositoryObservation{}, errors.New("post-hook marker content was not final")
		}
		probe.sawMarker.Store(true)
	}
	identity, err := provider.DeriveScopedIdentity(binding.IdentitySalt, provider.ScopedIdentityDocument{
		Provider: backupasset.ProviderRsync, TaskID: binding.TaskID, NodeID: binding.NodeID, EndpointFacts: binding.EndpointFacts,
	})
	if err != nil {
		return provider.RepositoryObservation{}, err
	}
	return provider.RepositoryObservation{
		Provider: backupasset.ProviderRsync, IdentityClass: provider.IdentityTaskScopedEndpoint, RepositoryIdentity: identity,
		VersionMode:     backupasset.VersionMutableHead,
		Capabilities:    backupasset.CapabilitySet{List: true, OpenSequential: true, OpenRange: true},
		AdapterRevision: "completion-test:v1", SourceRevision: probe.revision, Availability: backupasset.PhysicalOnline,
		ObservedAt: time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC), ConfigFingerprint: strings.Repeat("c", 64),
	}, nil
}

type completionWake struct{ calls atomic.Int32 }

func (wake *completionWake) TryWake() bool {
	wake.calls.Add(1)
	return true
}

func TestRunTaskRealRepositoryObserverInvalidatesCatalogAfterPostHook(t *testing.T) {
	db := openManagerTestDB(t)
	if err := db.AutoMigrate(
		&model.BackupRepository{}, &model.RepositoryAccessBinding{}, &model.TaskRepositoryLink{}, &model.RecoveryPoint{},
		&model.RecoveryPointLifecycleAttempt{}, &model.RecoveryPointManifest{}, &model.CatalogGeneration{}, &model.CatalogEntry{},
	); err != nil {
		t.Fatalf("migrate source completion repository tables: %v", err)
	}
	target := t.TempDir()
	node := model.Node{Name: "source-completion-real-node", Host: "example.invalid", Port: 22, Username: "reader", AuthType: "password", Password: "FAKE_NODE_PASSWORD_FOR_TEST_ONLY"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	taskEntity := model.Task{
		Name: "source-completion-real-task", NodeID: node.ID, ExecutorType: "rsync", Status: string(StatusPending), Enabled: true,
		RsyncSource: "/source", RsyncTarget: target,
	}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatal(err)
	}
	markerPath := filepath.Join(target, "post-hook-complete")
	completionPolicy(t, db, taskEntity.ID, false, "write-completion-marker")

	probe := &completionProbe{markerPath: markerPath, revision: strings.Repeat("a", 64)}
	registry := provider.NewRegistry()
	if err := registry.Register(backupasset.ProviderRsync, provider.Registration{Prober: probe}); err != nil {
		t.Fatal(err)
	}
	repositoryService, err := backuprepository.NewService(backuprepository.Dependencies{
		DB: db, Foundation: backupasset.NewFoundationService(completionFoundationSettings()), Registry: registry,
		Now: func() time.Time { return time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("create repository service: %v", err)
	}
	wake := &completionWake{}
	if err := repositoryService.SetCatalogWake(wake); err != nil {
		t.Fatalf("set Catalog wake: %v", err)
	}
	connected, err := repositoryService.Connect(context.Background(), backuprepository.ConnectRequest{TaskID: taskEntity.ID}, backuprepository.RequestContext{})
	if err != nil || connected.MutablePoint == nil {
		t.Fatalf("connect legacy Rsync source: result=%+v err=%v", connected, err)
	}
	var point model.RecoveryPoint
	if err := db.Where("id = ?", connected.MutablePoint.ID).First(&point).Error; err != nil {
		t.Fatal(err)
	}
	finished := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	generation := model.CatalogGeneration{
		ID: strings.Repeat("d", 32), RecoveryPointID: point.ID, Generation: 1, State: string(catalog.GenerationComplete), IsActive: true,
		SourceFingerprint: point.SourceFingerprint, StartedAt: finished.Add(-time.Minute), FinishedAt: &finished, CreatedAt: finished.Add(-time.Minute), UpdatedAt: finished,
	}
	if err := db.Create(&generation).Error; err != nil {
		t.Fatalf("seed active Catalog generation: %v", err)
	}
	wake.calls.Store(0)

	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)
	manager.hookRunFunc = func(_ context.Context, _ model.Task, command string) error {
		if command != "write-completion-marker" {
			return fmt.Errorf("unexpected post-hook %q", command)
		}
		return os.WriteFile(markerPath, []byte("post-hook-final-content"), 0o600)
	}
	observer := &sourceCompletionObserverFake{
		delegate: repositoryService.ObserveBackupSourceCompletion,
		before: func(_ context.Context, observedTaskID uint) {
			if observedTaskID != taskEntity.ID {
				t.Errorf("real observer task id=%d, want %d", observedTaskID, taskEntity.ID)
			}
			contents, readErr := os.ReadFile(markerPath)
			if readErr != nil || string(contents) != "post-hook-final-content" {
				t.Errorf("real observer saw post-hook contents=%q err=%v", string(contents), readErr)
			}
		},
	}
	manager.SetBackupSourceCompletionObserver(observer)
	probe.requireMarker.Store(true)

	runID := createTestTaskRun(t, db, taskEntity.ID, "manual")
	manager.runTask(taskEntity.ID, runID, "manual", generateChainRunID())

	storedTask, storedRun := loadSourceCompletionState(t, db, taskEntity.ID, runID)
	if storedTask.Status != string(StatusSuccess) || storedRun.Status != model.TaskRunStatusSuccess {
		t.Fatalf("real observer successful state Task=%q TaskRun=%q", storedTask.Status, storedRun.Status)
	}
	if observer.calls.Load() != 1 || probe.calls.Load() != 2 || !probe.sawMarker.Load() {
		t.Fatalf("real observer calls=%d probe calls=%d saw post-hook=%v", observer.calls.Load(), probe.calls.Load(), probe.sawMarker.Load())
	}
	var refreshedPoint model.RecoveryPoint
	if err := db.Where("id = ?", point.ID).First(&refreshedPoint).Error; err != nil {
		t.Fatal(err)
	}
	if refreshedPoint.SourceFingerprint != strings.Repeat("a", 64) || refreshedPoint.ObservedAt == nil {
		t.Fatalf("refreshed mutable point=%+v", refreshedPoint)
	}
	var invalidated model.CatalogGeneration
	if err := db.Where("id = ?", generation.ID).First(&invalidated).Error; err != nil {
		t.Fatal(err)
	}
	if invalidated.State != string(catalog.GenerationSuperseded) || invalidated.IsActive {
		t.Fatalf("active Catalog generation was not invalidated: %+v", invalidated)
	}
	if wake.calls.Load() != 1 {
		t.Fatalf("Catalog wake calls=%d, want 1", wake.calls.Load())
	}
	var link model.TaskRepositoryLink
	if err := db.Where("task_id = ? AND unlinked_at IS NULL", taskEntity.ID).First(&link).Error; err != nil {
		t.Fatal(err)
	}
	if link.PublicationMode != string(backupasset.PublicationLegacyMutable) {
		t.Fatalf("completion observer changed Task link mode=%q", link.PublicationMode)
	}
}
