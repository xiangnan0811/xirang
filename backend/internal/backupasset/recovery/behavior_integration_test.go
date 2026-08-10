package recovery_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	backupruntime "xirang/backend/internal/backupasset/runtime"
	"xirang/backend/internal/config"
	"xirang/backend/internal/database"
	"xirang/backend/internal/model"
	"xirang/backend/internal/task"
	taskexecutor "xirang/backend/internal/task/executor"

	"gorm.io/gorm"
)

var recoveryBehaviorSequence atomic.Uint64

func TestRecoveryBehaviorPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		if strings.TrimSpace(os.Getenv("REQUIRE_POSTGRES_RECOVERY_TEST")) == "1" {
			t.Fatal("TEST_POSTGRES_DSN is required when REQUIRE_POSTGRES_RECOVERY_TEST=1")
		}
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}

	fixture := openRecoveryBehaviorPostgres(t, dsn)

	t.Run("ActiveRecoveryLeaseBlocksTaskAdmission", func(t *testing.T) {
		node, taskEntity := seedRecoveryBehaviorNodeTask(t, fixture.db, "active-lease")
		if err := fixture.db.Create(newRecoveryBehaviorLease(node.ID)).Error; err != nil {
			t.Fatal(err)
		}

		err := reserveRecoveryBehaviorTask(context.Background(), fixture, taskEntity)
		if !errors.Is(err, task.ErrNodeWriteConflict) {
			t.Fatalf("task admission error=%v, want node-write conflict", err)
		}
		var runCount int64
		if err := fixture.db.Model(&model.TaskRun{}).Where("task_id = ?", taskEntity.ID).Count(&runCount).Error; err != nil {
			t.Fatal(err)
		}
		if runCount != 0 {
			t.Fatalf("conflicted task admission left %d durable TaskRun rows", runCount)
		}
	})

	t.Run("TaskRunStateMatrix", func(t *testing.T) {
		for _, testCase := range []struct {
			status  string
			blocked bool
		}{
			{status: "pending", blocked: true},
			{status: "running", blocked: true},
			{status: "success"},
			{status: "failed"},
			{status: "canceled"},
			{status: "warning"},
			{status: "skipped"},
		} {
			t.Run(testCase.status, func(t *testing.T) {
				node, taskEntity := seedRecoveryBehaviorNodeTask(t, fixture.db, "state-"+testCase.status)
				if err := fixture.db.Create(&model.TaskRun{
					TaskID: taskEntity.ID, TriggerType: "manual", Status: testCase.status,
				}).Error; err != nil {
					t.Fatal(err)
				}

				err := claimRecoveryBehaviorLease(context.Background(), fixture, node.ID)
				if testCase.blocked && !errors.Is(err, task.ErrNodeWriteConflict) {
					t.Fatalf("status %q recovery admission error=%v, want node-write conflict", testCase.status, err)
				}
				if !testCase.blocked && err != nil {
					t.Fatalf("terminal status %q blocked recovery admission: %v", testCase.status, err)
				}
				var leaseCount int64
				if err := fixture.db.Model(&model.BackupAssetRecoveryNodeLease{}).
					Where("node_id = ? AND state = ?", node.ID, "active").Count(&leaseCount).Error; err != nil {
					t.Fatal(err)
				}
				wantLeases := int64(1)
				if testCase.blocked {
					wantLeases = 0
				}
				if leaseCount != wantLeases {
					t.Fatalf("status %q durable active leases=%d, want %d", testCase.status, leaseCount, wantLeases)
				}
			})
		}
	})

	t.Run("TaskRunNodeSnapshotSurvivesTaskMigration", func(t *testing.T) {
		originalNode, taskEntity := seedRecoveryBehaviorNodeTask(t, fixture.db, "snapshot-original")
		migratedNode, _ := seedRecoveryBehaviorNodeTask(t, fixture.db, "snapshot-migrated")
		if err := fixture.db.Create(&model.TaskRun{
			TaskID: taskEntity.ID, TriggerType: "manual", Status: "running",
		}).Error; err != nil {
			t.Fatal(err)
		}
		if err := fixture.db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).
			Update("node_id", migratedNode.ID).Error; err != nil {
			t.Fatal(err)
		}

		err := fixture.db.Transaction(func(tx *gorm.DB) error {
			return fixture.coordinator.AdmitRecoveryTx(context.Background(), tx, originalNode.ID)
		})
		if !errors.Is(err, task.ErrNodeWriteConflict) {
			t.Fatalf("old-node recovery admission error=%v, want live-writer conflict", err)
		}
		err = fixture.db.Transaction(func(tx *gorm.DB) error {
			return fixture.coordinator.AdmitRecoveryTx(context.Background(), tx, migratedNode.ID)
		})
		if err != nil {
			t.Fatalf("new-node recovery admission inherited migrated Task writer: %v", err)
		}
	})

	t.Run("ConcurrentSameNodeHasOneDurableWinner", func(t *testing.T) {
		for attempt := 0; attempt < 10; attempt++ {
			node, taskEntity := seedRecoveryBehaviorNodeTask(t, fixture.db, fmt.Sprintf("concurrent-%d", attempt))
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			start := make(chan struct{})
			results := make(chan error, 2)
			go func() {
				<-start
				results <- reserveRecoveryBehaviorTask(ctx, fixture, taskEntity)
			}()
			go func() {
				<-start
				results <- claimRecoveryBehaviorLease(ctx, fixture, node.ID)
			}()
			close(start)

			successes := 0
			conflicts := 0
			for range 2 {
				err := <-results
				switch {
				case err == nil:
					successes++
				case errors.Is(err, task.ErrNodeWriteConflict):
					conflicts++
				default:
					cancel()
					t.Fatalf("attempt %d concurrent admission returned unstable error: %v", attempt+1, err)
				}
			}
			cancel()
			if successes != 1 || conflicts != 1 {
				t.Fatalf("attempt %d concurrent results successes=%d conflicts=%d, want 1/1",
					attempt+1, successes, conflicts)
			}

			var runCount, leaseCount int64
			if err := fixture.db.Model(&model.TaskRun{}).
				Where("task_id = ? AND status IN ?", taskEntity.ID, []string{"pending", "running"}).
				Count(&runCount).Error; err != nil {
				t.Fatal(err)
			}
			if err := fixture.db.Model(&model.BackupAssetRecoveryNodeLease{}).
				Where("node_id = ? AND state = ?", node.ID, "active").Count(&leaseCount).Error; err != nil {
				t.Fatal(err)
			}
			if runCount+leaseCount != 1 {
				t.Fatalf("attempt %d durable TaskRun/lease winners=%d/%d, want exactly one",
					attempt+1, runCount, leaseCount)
			}
		}
	})

	t.Run("ManagerCancelStartRecoveryRace", func(t *testing.T) {
		for _, testCase := range []struct {
			name    string
			restore bool
		}{
			{name: "ordinary"},
			{name: "legacy_restore", restore: true},
		} {
			t.Run(testCase.name, func(t *testing.T) {
				node, taskEntity := seedRecoveryBehaviorNodeTask(t, fixture.db, "manager-race-"+testCase.name)
				if err := fixture.db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).
					Update("status", "success").Error; err != nil {
					t.Fatal(err)
				}
				taskEntity.Status = "success"
				if err := fixture.db.Create(&model.TaskRun{
					TaskID: taskEntity.ID, TriggerType: "manual", Status: "success",
				}).Error; err != nil {
					t.Fatal(err)
				}

				executor := &recoveryBehaviorTrackingExecutor{}
				manager := task.NewManager(
					fixture.db,
					recoveryBehaviorExecutorFactory{executor: executor},
					nil,
					nil,
					nil,
					nil,
					8,
					90,
				)
				startEntered := make(chan struct{})
				startRelease := make(chan struct{})
				admission := &recoveryBehaviorStartBarrier{
					delegate: fixture.coordinator,
					entered:  startEntered,
					release:  startRelease,
				}
				manager.SetNodeWriteAdmission(admission)
				var releaseOnce sync.Once
				release := func() { releaseOnce.Do(func() { close(startRelease) }) }
				var (
					shutdownOnce sync.Once
					shutdownErr  error
				)
				shutdown := func() {
					shutdownOnce.Do(func() {
						ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
						defer cancel()
						shutdownErr = manager.Shutdown(ctx)
					})
				}
				defer func() {
					release()
					shutdown()
				}()

				var (
					runID      uint
					triggerErr error
				)
				if testCase.restore {
					runID, triggerErr = manager.TriggerRestore(taskEntity.ID, "/tmp/recovery-behavior-restore")
				} else {
					runID, triggerErr = manager.TriggerManual(taskEntity.ID)
				}
				if triggerErr != nil {
					t.Fatalf("trigger %s runner: %v", testCase.name, triggerErr)
				}
				select {
				case <-startEntered:
				case <-time.After(5 * time.Second):
					t.Fatalf("%s runner did not reach PostgreSQL start admission", testCase.name)
				}

				if err := claimRecoveryBehaviorLease(context.Background(), fixture, node.ID); !errors.Is(err, task.ErrNodeWriteConflict) {
					t.Fatalf("Recovery before %s cancellation error=%v, want live TaskRun conflict", testCase.name, err)
				}
				if err := manager.Cancel(taskEntity.ID); err != nil {
					t.Fatalf("cancel %s runner: %v", testCase.name, err)
				}
				if err := claimRecoveryBehaviorLease(context.Background(), fixture, node.ID); err != nil {
					t.Fatalf("Recovery after %s cancellation did not become the durable winner: %v", testCase.name, err)
				}

				release()
				shutdown()
				if shutdownErr != nil {
					t.Fatalf("shutdown %s manager: %v", testCase.name, shutdownErr)
				}
				var finalRun model.TaskRun
				if err := fixture.db.First(&finalRun, runID).Error; err != nil {
					t.Fatal(err)
				}
				if finalRun.Status != "canceled" {
					t.Fatalf("%s losing TaskRun status=%q, want canceled", testCase.name, finalRun.Status)
				}
				if got := executor.totalCalls(); got != 0 {
					t.Fatalf("%s losing runner performed %d executor call(s)", testCase.name, got)
				}
				var activeLeases int64
				if err := fixture.db.Model(&model.BackupAssetRecoveryNodeLease{}).
					Where("node_id = ? AND state = ?", node.ID, "active").Count(&activeLeases).Error; err != nil {
					t.Fatal(err)
				}
				if activeLeases != 1 {
					t.Fatalf("%s durable Recovery lease winners=%d, want exactly one", testCase.name, activeLeases)
				}
			})
		}
	})

	t.Run("ManagerCancelAfterEntryCommitPreservesPriorOutcome", func(t *testing.T) {
		node, taskEntity := seedRecoveryBehaviorNodeTask(t, fixture.db, "manager-commit-cancel")
		previousLastRunAt := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Microsecond)
		previousNextRunAt := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Microsecond)
		if err := fixture.db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Updates(map[string]interface{}{
			"status":      "success",
			"last_run_at": &previousLastRunAt,
			"next_run_at": &previousNextRunAt,
			"last_error":  "previous PostgreSQL outcome",
		}).Error; err != nil {
			t.Fatal(err)
		}
		if err := fixture.db.Create(&model.TaskRun{
			TaskID: taskEntity.ID, TriggerType: "manual", Status: "success",
		}).Error; err != nil {
			t.Fatal(err)
		}
		var previous model.Task
		if err := fixture.db.First(&previous, taskEntity.ID).Error; err != nil {
			t.Fatal(err)
		}

		sqlDB, err := fixture.db.DB()
		if err != nil {
			t.Fatal(err)
		}
		commitEntered := make(chan struct{})
		commitRelease := make(chan struct{})
		commitPool := &recoveryBehaviorCommitBarrierPool{
			DB: sqlDB, committed: commitEntered, release: commitRelease,
		}
		originalPool := fixture.db.ConnPool
		originalStatementPool := fixture.db.Statement.ConnPool
		fixture.db.ConnPool = commitPool
		fixture.db.Statement.ConnPool = commitPool
		defer func() {
			fixture.db.ConnPool = originalPool
			fixture.db.Statement.ConnPool = originalStatementPool
		}()

		startEntered := make(chan struct{})
		startRelease := make(chan struct{})
		executor := &recoveryBehaviorTrackingExecutor{}
		manager := task.NewManager(
			fixture.db,
			recoveryBehaviorExecutorFactory{executor: executor},
			nil,
			nil,
			nil,
			nil,
			8,
			90,
		)
		manager.SetNodeWriteAdmission(&recoveryBehaviorStartBarrier{
			delegate:  fixture.coordinator,
			entered:   startEntered,
			release:   startRelease,
			armCommit: commitPool.arm,
		})

		var blockCancelRead atomic.Bool
		cancelReadEntered := make(chan struct{})
		cancelReadRelease := make(chan struct{})
		var cancelReadOnce sync.Once
		callbackName := fmt.Sprintf("test:recovery-entry-cancel-read-%d", taskEntity.ID)
		if err := fixture.db.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
			if tx.Statement.Table != "tasks" || !blockCancelRead.CompareAndSwap(true, false) {
				return
			}
			cancelReadOnce.Do(func() { close(cancelReadEntered) })
			<-cancelReadRelease
		}); err != nil {
			t.Fatal(err)
		}
		var startReleaseOnce sync.Once
		releaseStart := func() { startReleaseOnce.Do(func() { close(startRelease) }) }
		var cancelReadReleaseOnce sync.Once
		releaseCancelRead := func() { cancelReadReleaseOnce.Do(func() { close(cancelReadRelease) }) }
		var commitReleaseOnce sync.Once
		releaseCommit := func() { commitReleaseOnce.Do(func() { close(commitRelease) }) }
		var shutdownOnce sync.Once
		var shutdownErr error
		shutdown := func() {
			shutdownOnce.Do(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				shutdownErr = manager.Shutdown(ctx)
			})
		}
		defer func() {
			releaseStart()
			releaseCancelRead()
			releaseCommit()
			shutdown()
			_ = fixture.db.Callback().Query().Remove(callbackName)
		}()

		runID, err := manager.TriggerManual(taskEntity.ID)
		if err != nil {
			t.Fatalf("trigger PostgreSQL ordinary task: %v", err)
		}
		select {
		case <-startEntered:
		case <-time.After(5 * time.Second):
			t.Fatal("runner did not reach PostgreSQL executor-entry transaction")
		}

		blockCancelRead.Store(true)
		cancelResult := make(chan error, 1)
		go func() { cancelResult <- manager.Cancel(taskEntity.ID) }()
		select {
		case <-cancelReadEntered:
		case <-time.After(5 * time.Second):
			t.Fatal("Cancel did not read the prior PostgreSQL Task outcome")
		}
		releaseStart()
		select {
		case <-commitEntered:
		case <-time.After(5 * time.Second):
			t.Fatal("PostgreSQL executor-entry transaction did not durably commit")
		}
		releaseCancelRead()
		select {
		case err := <-cancelResult:
			if err != nil {
				t.Fatalf("cancel PostgreSQL task after executor-entry commit: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("PostgreSQL Cancel did not return after executor-entry commit")
		}
		releaseCommit()
		shutdown()
		if shutdownErr != nil {
			t.Fatalf("shutdown PostgreSQL task manager: %v", shutdownErr)
		}
		if got := executor.totalCalls(); got != 0 {
			t.Fatalf("canceled PostgreSQL runner performed %d executor call(s)", got)
		}

		var finalRun model.TaskRun
		if err := fixture.db.First(&finalRun, runID).Error; err != nil {
			t.Fatal(err)
		}
		if finalRun.Status != "canceled" || finalRun.StartedAt != nil {
			t.Fatalf("PostgreSQL TaskRun status/start=%q/%v, want canceled/nil", finalRun.Status, finalRun.StartedAt)
		}
		var finalTask model.Task
		if err := fixture.db.First(&finalTask, taskEntity.ID).Error; err != nil {
			t.Fatal(err)
		}
		if finalTask.Status != previous.Status || finalTask.LastError != previous.LastError {
			t.Fatalf("PostgreSQL Task outcome=%q/%q, want preserved %q/%q",
				finalTask.Status, finalTask.LastError, previous.Status, previous.LastError)
		}
		if finalTask.LastRunAt == nil || previous.LastRunAt == nil || !finalTask.LastRunAt.Equal(*previous.LastRunAt) {
			t.Fatalf("PostgreSQL Task last_run_at=%v, want %v", finalTask.LastRunAt, previous.LastRunAt)
		}
		if finalTask.NextRunAt == nil || previous.NextRunAt == nil || !finalTask.NextRunAt.Equal(*previous.NextRunAt) {
			t.Fatalf("PostgreSQL Task next_run_at=%v, want %v", finalTask.NextRunAt, previous.NextRunAt)
		}
		if err := claimRecoveryBehaviorLease(context.Background(), fixture, node.ID); err != nil {
			t.Fatalf("Recovery did not become durable winner after canceled no-executor Task: %v", err)
		}
	})

	t.Run("ManagerStopAfterEntryCommitPreservesNoExecutorOutcome", func(t *testing.T) {
		for _, testCase := range []struct {
			name           string
			previousStatus string
			legacyRestore  bool
			shutdown       bool
		}{
			{name: "pending/shutdown", previousStatus: "pending", shutdown: true},
			{name: "pending/deadline", previousStatus: "pending"},
			{name: "retrying/shutdown", previousStatus: "retrying", shutdown: true},
			{name: "retrying/deadline", previousStatus: "retrying"},
			{name: "legacy_restore/shutdown", previousStatus: "success", legacyRestore: true, shutdown: true},
			{name: "legacy_restore/deadline", previousStatus: "success", legacyRestore: true},
		} {
			t.Run(testCase.name, func(t *testing.T) {
				testRecoveryBehaviorStopAfterEntryCommit(t, fixture, testCase.previousStatus, testCase.legacyRestore, testCase.shutdown)
			})
		}
	})
}

func testRecoveryBehaviorStopAfterEntryCommit(
	t *testing.T,
	fixture recoveryBehaviorFixture,
	previousStatus string,
	legacyRestore bool,
	shutdown bool,
) {
	t.Helper()
	node, taskEntity := seedRecoveryBehaviorNodeTask(t, fixture.db, "manager-stop-after-commit")
	previousLastRunAt := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Microsecond)
	previousNextRunAt := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Microsecond)
	if err := fixture.db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Updates(map[string]interface{}{
		"status":      previousStatus,
		"last_run_at": &previousLastRunAt,
		"next_run_at": &previousNextRunAt,
		"last_error":  "previous PostgreSQL no-executor outcome",
		"retry_count": 3,
		"skip_next":   true,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if legacyRestore {
		if err := fixture.db.Create(&model.TaskRun{
			TaskID: taskEntity.ID, TriggerType: "manual", Status: "success",
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	var previous model.Task
	if err := fixture.db.First(&previous, taskEntity.ID).Error; err != nil {
		t.Fatal(err)
	}

	t.Setenv("TASK_MAX_EXECUTION_SECONDS", "3600")

	sqlDB, err := fixture.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	commitEntered := make(chan struct{})
	commitRelease := make(chan struct{})
	commitPool := &recoveryBehaviorCommitBarrierPool{
		DB: sqlDB, committed: commitEntered, release: commitRelease,
	}
	originalPool := fixture.db.ConnPool
	originalStatementPool := fixture.db.Statement.ConnPool
	fixture.db.ConnPool = commitPool
	fixture.db.Statement.ConnPool = commitPool
	defer func() {
		fixture.db.ConnPool = originalPool
		fixture.db.Statement.ConnPool = originalStatementPool
	}()

	startEntered := make(chan struct{})
	startRelease := make(chan struct{})
	contextSeen := make(chan context.Context, 1)
	executor := &recoveryBehaviorTrackingExecutor{}
	runContextFactory := context.WithTimeout
	var deadlineContext *recoveryBehaviorDeadlineContext
	if !shutdown {
		deadlineContext = newRecoveryBehaviorDeadlineContext(context.Background())
		runContextFactory = func(context.Context, time.Duration) (context.Context, context.CancelFunc) {
			return deadlineContext, deadlineContext.Cancel
		}
	}
	manager := task.NewManager(
		fixture.db,
		recoveryBehaviorExecutorFactory{executor: executor},
		nil,
		nil,
		nil,
		nil,
		8,
		90,
		task.WithRunContextFactory(runContextFactory),
	)
	manager.SetNodeWriteAdmission(&recoveryBehaviorStartBarrier{
		delegate:    fixture.coordinator,
		entered:     startEntered,
		release:     startRelease,
		armCommit:   commitPool.arm,
		contextSeen: contextSeen,
	})

	var startReleaseOnce sync.Once
	releaseStart := func() { startReleaseOnce.Do(func() { close(startRelease) }) }
	var commitReleaseOnce sync.Once
	releaseCommit := func() { commitReleaseOnce.Do(func() { close(commitRelease) }) }
	var shutdownOnce sync.Once
	var shutdownErr error
	shutdownManager := func() {
		shutdownOnce.Do(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			shutdownErr = manager.Shutdown(ctx)
		})
	}
	defer func() {
		releaseStart()
		releaseCommit()
		shutdownManager()
	}()

	var runID uint
	if legacyRestore {
		runID, err = manager.TriggerRestore(taskEntity.ID, "/tmp/recovery-behavior-stop-after-commit")
	} else {
		runID, err = manager.TriggerManual(taskEntity.ID)
	}
	if err != nil {
		t.Fatalf("trigger PostgreSQL no-executor runner: %v", err)
	}
	select {
	case <-startEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("runner did not reach PostgreSQL executor-entry transaction")
	}
	var executionCtx context.Context
	select {
	case executionCtx = <-contextSeen:
	case <-time.After(5 * time.Second):
		t.Fatal("runner did not expose its PostgreSQL execution context")
	}
	releaseStart()
	select {
	case <-commitEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("PostgreSQL executor-entry transaction did not durably commit")
	}
	if err := executionCtx.Err(); err != nil {
		t.Fatalf("PostgreSQL execution context expired before durable entry commit release: %v", err)
	}

	var shutdownResult chan error
	if shutdown {
		shutdownResult = make(chan error, 1)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			shutdownResult <- manager.Shutdown(ctx)
		}()
	} else {
		deadlineContext.Expire()
	}
	select {
	case <-executionCtx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("PostgreSQL no-executor stop did not cancel the execution context")
	}
	if !shutdown && !errors.Is(executionCtx.Err(), context.DeadlineExceeded) {
		t.Fatalf("PostgreSQL execution context error=%v, want deadline exceeded", executionCtx.Err())
	}
	releaseCommit()
	if shutdown {
		select {
		case shutdownErr = <-shutdownResult:
			shutdownOnce.Do(func() {})
		case <-time.After(5 * time.Second):
			t.Fatal("PostgreSQL shutdown did not join the no-executor runner")
		}
	} else {
		shutdownManager()
	}
	if shutdownErr != nil {
		t.Fatalf("shutdown PostgreSQL no-executor manager: %v", shutdownErr)
	}
	if got := executor.totalCalls(); got != 0 {
		t.Fatalf("PostgreSQL no-executor runner performed %d executor/precheck-adjacent call(s)", got)
	}

	var finalRun model.TaskRun
	if err := fixture.db.First(&finalRun, runID).Error; err != nil {
		t.Fatal(err)
	}
	if finalRun.Status != "canceled" || finalRun.StartedAt != nil || finalRun.DurationMs != 0 {
		t.Fatalf("PostgreSQL TaskRun status/start/duration=%q/%v/%d, want canceled/nil/0",
			finalRun.Status, finalRun.StartedAt, finalRun.DurationMs)
	}
	var finalTask model.Task
	if err := fixture.db.First(&finalTask, taskEntity.ID).Error; err != nil {
		t.Fatal(err)
	}
	if finalTask.Status != previous.Status || finalTask.LastError != previous.LastError ||
		finalTask.RetryCount != previous.RetryCount || finalTask.SkipNext != previous.SkipNext {
		t.Fatalf("PostgreSQL Task outcome changed: got status=%q error=%q retry=%d skip=%v",
			finalTask.Status, finalTask.LastError, finalTask.RetryCount, finalTask.SkipNext)
	}
	if finalTask.LastRunAt == nil || previous.LastRunAt == nil || !finalTask.LastRunAt.Equal(*previous.LastRunAt) {
		t.Fatalf("PostgreSQL Task last_run_at=%v, want %v", finalTask.LastRunAt, previous.LastRunAt)
	}
	if finalTask.NextRunAt == nil || previous.NextRunAt == nil || !finalTask.NextRunAt.Equal(*previous.NextRunAt) {
		t.Fatalf("PostgreSQL Task next_run_at=%v, want %v", finalTask.NextRunAt, previous.NextRunAt)
	}
	if err := claimRecoveryBehaviorLease(context.Background(), fixture, node.ID); err != nil {
		t.Fatalf("Recovery did not become durable winner after PostgreSQL no-executor compensation: %v", err)
	}
}

type recoveryBehaviorExecutorFactory struct {
	executor taskexecutor.Executor
}

func (factory recoveryBehaviorExecutorFactory) Resolve(string) taskexecutor.Executor {
	return factory.executor
}

type recoveryBehaviorTrackingExecutor struct {
	ordinaryCalls atomic.Int32
	restoreCalls  atomic.Int32
}

type recoveryBehaviorDeadlineContext struct {
	parent context.Context
	done   chan struct{}
	once   sync.Once
	mu     sync.RWMutex
	err    error
}

func newRecoveryBehaviorDeadlineContext(parent context.Context) *recoveryBehaviorDeadlineContext {
	return &recoveryBehaviorDeadlineContext{parent: parent, done: make(chan struct{})}
}

func (*recoveryBehaviorDeadlineContext) Deadline() (time.Time, bool) {
	return time.Time{}, false
}

func (ctx *recoveryBehaviorDeadlineContext) Done() <-chan struct{} {
	return ctx.done
}

func (ctx *recoveryBehaviorDeadlineContext) Err() error {
	ctx.mu.RLock()
	defer ctx.mu.RUnlock()
	return ctx.err
}

func (ctx *recoveryBehaviorDeadlineContext) Value(key any) any {
	return ctx.parent.Value(key)
}

func (ctx *recoveryBehaviorDeadlineContext) Cancel() {
	ctx.finish(context.Canceled)
}

func (ctx *recoveryBehaviorDeadlineContext) Expire() {
	ctx.finish(context.DeadlineExceeded)
}

func (ctx *recoveryBehaviorDeadlineContext) finish(err error) {
	ctx.once.Do(func() {
		ctx.mu.Lock()
		ctx.err = err
		ctx.mu.Unlock()
		close(ctx.done)
	})
}

func (executor *recoveryBehaviorTrackingExecutor) Run(
	context.Context,
	model.Task,
	taskexecutor.LogFunc,
	taskexecutor.ProgressFunc,
) (int, error) {
	executor.ordinaryCalls.Add(1)
	return 0, nil
}

func (executor *recoveryBehaviorTrackingExecutor) RunRestore(
	context.Context,
	model.Task,
	taskexecutor.LogFunc,
	taskexecutor.ProgressFunc,
) (int, error) {
	executor.restoreCalls.Add(1)
	return 0, nil
}

func (executor *recoveryBehaviorTrackingExecutor) totalCalls() int32 {
	return executor.ordinaryCalls.Load() + executor.restoreCalls.Load()
}

type recoveryBehaviorStartBarrier struct {
	delegate    task.NodeWriteAdmission
	entered     chan struct{}
	release     <-chan struct{}
	armCommit   func(*gorm.DB)
	contextSeen chan<- context.Context
	once        sync.Once
}

func (barrier *recoveryBehaviorStartBarrier) AdmitTaskTx(
	ctx context.Context,
	tx *gorm.DB,
	nodeID uint,
) error {
	return barrier.delegate.AdmitTaskTx(ctx, tx, nodeID)
}

func (barrier *recoveryBehaviorStartBarrier) EnterTaskExecutionTx(
	ctx context.Context,
	tx *gorm.DB,
	runID uint,
	nodeID uint,
	startedAt time.Time,
) error {
	barrier.once.Do(func() {
		if barrier.contextSeen != nil {
			barrier.contextSeen <- ctx
		}
		close(barrier.entered)
	})
	<-barrier.release
	err := barrier.delegate.EnterTaskExecutionTx(ctx, tx, runID, nodeID, startedAt)
	if err == nil && barrier.armCommit != nil {
		barrier.armCommit(tx)
	}
	return err
}

type recoveryBehaviorCommitBarrierPool struct {
	*sql.DB
	commitOnce sync.Once
	committed  chan struct{}
	release    <-chan struct{}
}

func (pool *recoveryBehaviorCommitBarrierPool) BeginTx(ctx context.Context, options *sql.TxOptions) (gorm.ConnPool, error) {
	tx, err := pool.DB.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return &recoveryBehaviorCommitBarrierTx{Tx: tx, pool: pool}, nil
}

func (pool *recoveryBehaviorCommitBarrierPool) arm(tx *gorm.DB) {
	barrierTx, ok := tx.Statement.ConnPool.(*recoveryBehaviorCommitBarrierTx)
	if ok {
		barrierTx.armed.Store(true)
	}
}

type recoveryBehaviorCommitBarrierTx struct {
	*sql.Tx
	pool  *recoveryBehaviorCommitBarrierPool
	armed atomic.Bool
}

func (tx *recoveryBehaviorCommitBarrierTx) Commit() error {
	err := tx.Tx.Commit()
	if err == nil && tx.armed.CompareAndSwap(true, false) {
		tx.pool.commitOnce.Do(func() { close(tx.pool.committed) })
		<-tx.pool.release
	}
	return err
}

var _ taskexecutor.RestoreExecutor = (*recoveryBehaviorTrackingExecutor)(nil)
var _ task.NodeWriteAdmission = (*recoveryBehaviorStartBarrier)(nil)

type recoveryBehaviorFixture struct {
	db          *gorm.DB
	coordinator *backupruntime.NodeWriteCoordinator
}

func openRecoveryBehaviorPostgres(t *testing.T, dsn string) recoveryBehaviorFixture {
	t.Helper()
	parsed, err := url.Parse(dsn)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		t.Fatalf("TEST_POSTGRES_DSN must be a PostgreSQL URL: %v", err)
	}
	base, err := database.Open(config.Config{DBType: "postgres", PostgresDSN: dsn})
	if err != nil {
		t.Fatalf("open PostgreSQL Recovery behavior base: %v", err)
	}
	schema := fmt.Sprintf("xirang_recovery_%d", time.Now().UTC().UnixNano())
	if err := base.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		closeRecoveryBehaviorDB(t, base)
		t.Fatalf("create PostgreSQL Recovery behavior schema: %v", err)
	}

	var db *gorm.DB
	t.Cleanup(func() {
		if db != nil {
			closeRecoveryBehaviorDB(t, db)
		}
		if err := base.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE").Error; err != nil {
			t.Errorf("drop PostgreSQL Recovery behavior schema: %v", err)
		}
		closeRecoveryBehaviorDB(t, base)
	})

	query := parsed.Query()
	query.Set("search_path", schema)
	query.Set("timezone", "UTC")
	parsed.RawQuery = query.Encode()
	db, err = database.Open(config.Config{DBType: "postgres", PostgresDSN: parsed.String()})
	if err != nil {
		t.Fatalf("open scoped PostgreSQL Recovery behavior DB: %v", err)
	}
	if sqlDB, dbErr := db.DB(); dbErr != nil {
		t.Fatal(dbErr)
	} else {
		sqlDB.SetMaxOpenConns(16)
	}
	if err := db.AutoMigrate(
		&model.SSHKey{}, &model.Node{}, &model.Policy{}, &model.Task{}, &model.TaskRun{},
		&model.TaskLog{}, &model.TaskTrafficSample{}, &model.BackupAssetRecoveryNodeLease{},
	); err != nil {
		t.Fatalf("migrate PostgreSQL Recovery behavior schema: %v", err)
	}
	if err := db.Exec(`CREATE UNIQUE INDEX idx_backup_asset_recovery_node_leases_active_node
		ON backup_asset_recovery_node_leases(node_id) WHERE state = 'active'`).Error; err != nil {
		t.Fatalf("create PostgreSQL Recovery active-node lease index: %v", err)
	}
	coordinator, err := backupruntime.NewNodeWriteCoordinator(db)
	if err != nil {
		t.Fatal(err)
	}
	return recoveryBehaviorFixture{db: db, coordinator: coordinator}
}

func closeRecoveryBehaviorDB(t *testing.T, db *gorm.DB) {
	t.Helper()
	sqlDB, err := db.DB()
	if err != nil {
		t.Errorf("load PostgreSQL Recovery behavior SQL DB: %v", err)
		return
	}
	if err := sqlDB.Close(); err != nil {
		t.Errorf("close PostgreSQL Recovery behavior DB: %v", err)
	}
}

func seedRecoveryBehaviorNodeTask(t *testing.T, db *gorm.DB, suffix string) (model.Node, model.Task) {
	t.Helper()
	sequence := recoveryBehaviorSequence.Add(1)
	node := model.Node{
		Name:      fmt.Sprintf("recovery-behavior-%s-%d", suffix, sequence),
		Host:      "127.0.0.1",
		Port:      22,
		Username:  "root",
		AuthType:  "key",
		BasePath:  fmt.Sprintf("/tmp/recovery-behavior-base-%d", sequence),
		BackupDir: fmt.Sprintf("/tmp/recovery-behavior-backup-%d", sequence),
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	taskEntity := model.Task{
		Name:         fmt.Sprintf("recovery-behavior-task-%d", sequence),
		NodeID:       node.ID,
		ExecutorType: "rsync",
		Status:       "pending",
		Enabled:      true,
		RsyncSource:  "/tmp/source",
		RsyncTarget:  "/tmp/target",
	}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatal(err)
	}
	return node, taskEntity
}

func reserveRecoveryBehaviorTask(
	ctx context.Context,
	fixture recoveryBehaviorFixture,
	taskEntity model.Task,
) error {
	return fixture.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := fixture.coordinator.AdmitTaskTx(ctx, tx, taskEntity.NodeID); err != nil {
			return err
		}
		return tx.Create(&model.TaskRun{
			TaskID: taskEntity.ID, TriggerType: "manual", Status: "pending",
		}).Error
	})
}

func claimRecoveryBehaviorLease(ctx context.Context, fixture recoveryBehaviorFixture, nodeID uint) error {
	return fixture.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := fixture.coordinator.AdmitRecoveryTx(ctx, tx, nodeID); err != nil {
			return err
		}
		return tx.Create(newRecoveryBehaviorLease(nodeID)).Error
	})
}

func newRecoveryBehaviorLease(nodeID uint) *model.BackupAssetRecoveryNodeLease {
	sequence := recoveryBehaviorSequence.Add(1)
	now := time.Now().UTC().Truncate(time.Microsecond)
	return &model.BackupAssetRecoveryNodeLease{
		ID:             fmt.Sprintf("%032x", sequence),
		NodeID:         nodeID,
		HolderKind:     "recovery_job",
		JobID:          fmt.Sprintf("%032x", sequence+1_000_000),
		OwnerID:        "recovery-behavior-worker",
		Fence:          sequence,
		State:          "active",
		LeaseExpiresAt: now.Add(time.Hour),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
}
