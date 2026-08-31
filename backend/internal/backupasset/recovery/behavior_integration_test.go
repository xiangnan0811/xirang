package recovery_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
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

	t.Run("ActiveRecoveryLeaseBlocksDrillSourceAndSandbox", func(t *testing.T) {
		for _, blockedNode := range []string{"source", "sandbox"} {
			t.Run(blockedNode, func(t *testing.T) {
				source, sourceTask := seedRecoveryBehaviorNodeTask(t, fixture.db, "drill-lease-source-"+blockedNode)
				sandbox, _ := seedRecoveryBehaviorNodeTask(t, fixture.db, "drill-lease-sandbox-"+blockedNode)
				leaseNodeID := source.ID
				if blockedNode == "sandbox" {
					leaseNodeID = sandbox.ID
				}
				if err := fixture.db.Create(newRecoveryBehaviorLease(leaseNodeID)).Error; err != nil {
					t.Fatal(err)
				}

				err := reserveRecoveryBehaviorDrill(context.Background(), fixture, sourceTask, sandbox.ID)
				if !errors.Is(err, task.ErrNodeWriteConflict) {
					t.Fatalf("%s lease Drill admission error=%v, want conflict", blockedNode, err)
				}
				var runCount, evidenceCount int64
				if err := fixture.db.Model(&model.TaskRun{}).Where("task_id = ? AND trigger_type = ?", sourceTask.ID, "drill").Count(&runCount).Error; err != nil {
					t.Fatal(err)
				}
				if err := fixture.db.Model(&model.RestoreDrillEvidence{}).Where("task_id = ?", sourceTask.ID).Count(&evidenceCount).Error; err != nil {
					t.Fatal(err)
				}
				if runCount != 0 || evidenceCount != 0 {
					t.Fatalf("%s lease left Drill pair rows=%d/%d", blockedNode, runCount, evidenceCount)
				}
			})
		}
	})

	t.Run("ActiveDrillSandboxBlocksRecoveryAdmission", func(t *testing.T) {
		_, sourceTask := seedRecoveryBehaviorNodeTask(t, fixture.db, "drill-first-source")
		sandbox, _ := seedRecoveryBehaviorNodeTask(t, fixture.db, "drill-first-sandbox")
		if err := reserveRecoveryBehaviorDrill(context.Background(), fixture, sourceTask, sandbox.ID); err != nil {
			t.Fatalf("reserve Drill first: %v", err)
		}
		if err := claimRecoveryBehaviorLease(context.Background(), fixture, sandbox.ID); !errors.Is(err, task.ErrNodeWriteConflict) {
			t.Fatalf("sandbox Recovery after active Drill error=%v, want conflict", err)
		}
	})

	t.Run("SameNodeDrillReservationUsesOneBoundary", func(t *testing.T) {
		source, sourceTask := seedRecoveryBehaviorNodeTask(t, fixture.db, "drill-same-node")
		if err := reserveRecoveryBehaviorDrill(context.Background(), fixture, sourceTask, source.ID); err != nil {
			t.Fatalf("same-node Drill reservation: %v", err)
		}
		if err := claimRecoveryBehaviorLease(context.Background(), fixture, source.ID); !errors.Is(err, task.ErrNodeWriteConflict) {
			t.Fatalf("same-node Recovery after active Drill error=%v, want conflict", err)
		}
	})

	t.Run("DeterministicDrillRecoveryConcurrencyMatrix", func(t *testing.T) {
		matrix := []struct {
			phase  string
			node   string
			winner string
		}{
			{phase: "reservation", node: "source", winner: "recovery_first"},
			{phase: "reservation", node: "source", winner: "drill_first"},
			{phase: "reservation", node: "sandbox", winner: "recovery_first"},
			{phase: "reservation", node: "sandbox", winner: "drill_first"},
			{phase: "start", node: "source", winner: "recovery_first"},
			{phase: "start", node: "source", winner: "drill_first"},
			{phase: "start", node: "sandbox", winner: "recovery_first"},
			{phase: "start", node: "sandbox", winner: "drill_first"},
		}
		remaining := map[string]struct{}{
			"reservation/source/recovery_first":  {},
			"reservation/source/drill_first":     {},
			"reservation/sandbox/recovery_first": {},
			"reservation/sandbox/drill_first":    {},
			"start/source/recovery_first":        {},
			"start/source/drill_first":           {},
			"start/sandbox/recovery_first":       {},
			"start/sandbox/drill_first":          {},
		}
		for _, testCase := range matrix {
			signature := testCase.phase + "/" + testCase.node + "/" + testCase.winner
			if _, expected := remaining[signature]; !expected {
				t.Fatalf("unknown or duplicate PostgreSQL Drill/Recovery matrix case %q", signature)
			}
			delete(remaining, signature)
		}
		if len(remaining) != 0 {
			missing := make([]string, 0, len(remaining))
			for signature := range remaining {
				missing = append(missing, signature)
			}
			sort.Strings(missing)
			t.Fatalf("PostgreSQL Drill/Recovery matrix is missing cases: %s", strings.Join(missing, ", "))
		}

		for _, testCase := range matrix {
			for attempt := 1; attempt <= 3; attempt++ {
				t.Run(fmt.Sprintf("%s/%s/%s/attempt_%d", testCase.phase, testCase.node, testCase.winner, attempt), func(t *testing.T) {
					switch testCase.phase {
					case "reservation":
						testRecoveryBehaviorPostgresDrillReservationRace(t, fixture, testCase.node, testCase.winner, attempt)
					case "start":
						testRecoveryBehaviorPostgresDrillStartRace(t, fixture, testCase.node, testCase.winner, attempt)
					default:
						t.Fatalf("unknown PostgreSQL Drill/Recovery matrix phase %q", testCase.phase)
					}
				})
			}
		}
	})

	t.Run("DrillStartRechecksBothNodeLeasesAndRollsBackPair", func(t *testing.T) {
		for _, leaseNode := range []string{"source", "sandbox"} {
			t.Run(leaseNode, func(t *testing.T) {
				source, sourceTask := seedRecoveryBehaviorNodeTask(t, fixture.db, "drill-start-source-"+leaseNode)
				sandbox, _ := seedRecoveryBehaviorNodeTask(t, fixture.db, "drill-start-sandbox-"+leaseNode)
				if err := reserveRecoveryBehaviorDrill(context.Background(), fixture, sourceTask, sandbox.ID); err != nil {
					t.Fatal(err)
				}
				leaseNodeID := source.ID
				if leaseNode == "sandbox" {
					leaseNodeID = sandbox.ID
				}
				if err := fixture.db.Create(newRecoveryBehaviorLease(leaseNodeID)).Error; err != nil {
					t.Fatal(err)
				}
				var run model.TaskRun
				if err := fixture.db.Where("task_id = ? AND trigger_type = ?", sourceTask.ID, "drill").Take(&run).Error; err != nil {
					t.Fatal(err)
				}
				err := fixture.db.Transaction(func(tx *gorm.DB) error {
					return fixture.coordinator.EnterDrillExecutionTx(context.Background(), tx, run.ID, sandbox.ID, time.Now().UTC())
				})
				if !errors.Is(err, task.ErrNodeWriteConflict) {
					t.Fatalf("Drill start after %s lease error=%v, want conflict", leaseNode, err)
				}
				assertRecoveryBehaviorDrillPairStatus(t, fixture.db, run.ID, model.TaskRunStatusPending)
			})
		}

		t.Run("caller rollback", func(t *testing.T) {
			_, sourceTask := seedRecoveryBehaviorNodeTask(t, fixture.db, "drill-start-rollback-source")
			sandbox, _ := seedRecoveryBehaviorNodeTask(t, fixture.db, "drill-start-rollback-sandbox")
			if err := reserveRecoveryBehaviorDrill(context.Background(), fixture, sourceTask, sandbox.ID); err != nil {
				t.Fatal(err)
			}
			var run model.TaskRun
			if err := fixture.db.Where("task_id = ? AND trigger_type = ?", sourceTask.ID, "drill").Take(&run).Error; err != nil {
				t.Fatal(err)
			}
			injected := errors.New("INTERNAL_POSTGRES_DRILL_START_ROLLBACK_CANARY")
			err := fixture.db.Transaction(func(tx *gorm.DB) error {
				if err := fixture.coordinator.EnterDrillExecutionTx(context.Background(), tx, run.ID, sandbox.ID, time.Now().UTC()); err != nil {
					return err
				}
				if err := tx.Model(&model.RestoreDrillEvidence{}).Where("task_run_id = ?", run.ID).
					Update("status", model.TaskRunStatusRunning).Error; err != nil {
					return err
				}
				return injected
			})
			if !errors.Is(err, injected) {
				t.Fatalf("PostgreSQL Drill rollback error=%v, want injected", err)
			}
			assertRecoveryBehaviorDrillPairStatus(t, fixture.db, run.ID, model.TaskRunStatusPending)
		})
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

func testRecoveryBehaviorPostgresDrillReservationRace(
	t *testing.T,
	fixture recoveryBehaviorFixture,
	nodeRole string,
	winner string,
	attempt int,
) {
	t.Helper()
	source, sourceTask := seedRecoveryBehaviorNodeTask(
		t,
		fixture.db,
		fmt.Sprintf("drill-reservation-race-%s-%s-%d-source", nodeRole, winner, attempt),
	)
	sandbox, _ := seedRecoveryBehaviorNodeTask(
		t,
		fixture.db,
		fmt.Sprintf("drill-reservation-race-%s-%s-%d-sandbox", nodeRole, winner, attempt),
	)
	targetNodeID := recoveryBehaviorPostgresRaceNodeID(t, nodeRole, source.ID, sandbox.ID)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	holder := fixture.db.WithContext(ctx).Begin()
	if holder.Error != nil {
		t.Fatalf("begin PostgreSQL %s reservation holder: %v", winner, holder.Error)
	}
	holderOpen := true
	defer func() {
		if holderOpen {
			_ = holder.Rollback().Error
		}
	}()

	var (
		runID     uint
		contender recoveryBehaviorPostgresContender
	)
	switch winner {
	case "recovery_first":
		if err := claimRecoveryBehaviorLeaseTx(ctx, fixture, holder, targetNodeID); err != nil {
			t.Fatalf("hold PostgreSQL Recovery-first reservation: %v", err)
		}
		contender = startRecoveryBehaviorPostgresContender(ctx, fixture.db, func(tx *gorm.DB) error {
			_, err := reserveRecoveryBehaviorDrillTx(ctx, fixture, tx, sourceTask, sandbox.ID)
			return err
		})
	case "drill_first":
		var err error
		runID, err = reserveRecoveryBehaviorDrillTx(ctx, fixture, holder, sourceTask, sandbox.ID)
		if err != nil {
			t.Fatalf("hold PostgreSQL Drill-first reservation: %v", err)
		}
		contender = startRecoveryBehaviorPostgresContender(ctx, fixture.db, func(tx *gorm.DB) error {
			return claimRecoveryBehaviorLeaseTx(ctx, fixture, tx, targetNodeID)
		})
	default:
		t.Fatalf("unknown PostgreSQL reservation winner %q", winner)
	}

	holderPID := recoveryBehaviorPostgresBackendPID(t, holder)
	contenderPID := waitRecoveryBehaviorPostgresContenderStart(t, ctx, contender)
	waitForRecoveryBehaviorPostgresPIDBlockedBy(t, ctx, fixture.db, contender, contenderPID, holderPID)
	if err := holder.Commit().Error; err != nil {
		t.Fatalf("commit PostgreSQL %s reservation holder: %v", winner, err)
	}
	holderOpen = false
	loserErr := waitRecoveryBehaviorPostgresContenderResult(t, ctx, contender)
	if !errors.Is(loserErr, task.ErrNodeWriteConflict) {
		t.Fatalf("PostgreSQL %s reservation loser error=%v, want node-write conflict", winner, loserErr)
	}

	if winner == "recovery_first" {
		assertRecoveryBehaviorPostgresDrillRaceState(
			t, fixture.db, sourceTask.ID, 0, source.ID, sandbox.ID, "", targetNodeID,
		)
		return
	}
	assertRecoveryBehaviorPostgresDrillRaceState(
		t, fixture.db, sourceTask.ID, runID, source.ID, sandbox.ID, model.TaskRunStatusPending, 0,
	)
}

func testRecoveryBehaviorPostgresDrillStartRace(
	t *testing.T,
	fixture recoveryBehaviorFixture,
	nodeRole string,
	winner string,
	attempt int,
) {
	t.Helper()
	source, sourceTask := seedRecoveryBehaviorNodeTask(
		t,
		fixture.db,
		fmt.Sprintf("drill-start-race-%s-%s-%d-source", nodeRole, winner, attempt),
	)
	sandbox, _ := seedRecoveryBehaviorNodeTask(
		t,
		fixture.db,
		fmt.Sprintf("drill-start-race-%s-%s-%d-sandbox", nodeRole, winner, attempt),
	)
	if err := reserveRecoveryBehaviorDrill(context.Background(), fixture, sourceTask, sandbox.ID); err != nil {
		t.Fatalf("reserve PostgreSQL Drill before start race: %v", err)
	}
	var run model.TaskRun
	if err := fixture.db.Where("task_id = ? AND trigger_type = ?", sourceTask.ID, "drill").Take(&run).Error; err != nil {
		t.Fatalf("load PostgreSQL pending Drill before start race: %v", err)
	}
	targetNodeID := recoveryBehaviorPostgresRaceNodeID(t, nodeRole, source.ID, sandbox.ID)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	holder := fixture.db.WithContext(ctx).Begin()
	if holder.Error != nil {
		t.Fatalf("begin PostgreSQL %s start holder: %v", winner, holder.Error)
	}
	holderOpen := true
	defer func() {
		if holderOpen {
			_ = holder.Rollback().Error
		}
	}()

	var contender recoveryBehaviorPostgresContender
	switch winner {
	case "recovery_first":
		// A normal AdmitRecoveryTx cannot win after a pending Drill reservation.
		// This deliberately models the line-266 legacy/out-of-band lease window:
		// lock the canonical node boundary and publish the lease atomically, then
		// prove the start transaction waits and rolls its paired transition back.
		if err := claimOutOfBandRecoveryBehaviorLeaseTx(ctx, holder, targetNodeID); err != nil {
			t.Fatalf("hold PostgreSQL out-of-band Recovery-first start boundary: %v", err)
		}
		contender = startRecoveryBehaviorPostgresContender(ctx, fixture.db, func(tx *gorm.DB) error {
			return enterRecoveryBehaviorDrillTx(ctx, fixture, tx, run.ID, sandbox.ID, time.Now().UTC())
		})
	case "drill_first":
		if err := enterRecoveryBehaviorDrillTx(ctx, fixture, holder, run.ID, sandbox.ID, time.Now().UTC()); err != nil {
			t.Fatalf("hold PostgreSQL Drill-first start transition: %v", err)
		}
		contender = startRecoveryBehaviorPostgresContender(ctx, fixture.db, func(tx *gorm.DB) error {
			return claimRecoveryBehaviorLeaseTx(ctx, fixture, tx, targetNodeID)
		})
	default:
		t.Fatalf("unknown PostgreSQL start winner %q", winner)
	}

	holderPID := recoveryBehaviorPostgresBackendPID(t, holder)
	contenderPID := waitRecoveryBehaviorPostgresContenderStart(t, ctx, contender)
	waitForRecoveryBehaviorPostgresPIDBlockedBy(t, ctx, fixture.db, contender, contenderPID, holderPID)
	if err := holder.Commit().Error; err != nil {
		t.Fatalf("commit PostgreSQL %s start holder: %v", winner, err)
	}
	holderOpen = false
	loserErr := waitRecoveryBehaviorPostgresContenderResult(t, ctx, contender)
	if !errors.Is(loserErr, task.ErrNodeWriteConflict) {
		t.Fatalf("PostgreSQL %s start loser error=%v, want node-write conflict", winner, loserErr)
	}

	if winner == "recovery_first" {
		assertRecoveryBehaviorPostgresDrillRaceState(
			t, fixture.db, sourceTask.ID, run.ID, source.ID, sandbox.ID, model.TaskRunStatusPending, targetNodeID,
		)
		return
	}
	assertRecoveryBehaviorPostgresDrillRaceState(
		t, fixture.db, sourceTask.ID, run.ID, source.ID, sandbox.ID, model.TaskRunStatusRunning, 0,
	)
}

type recoveryBehaviorPostgresContenderStart struct {
	pid int
	err error
}

type recoveryBehaviorPostgresContender struct {
	started <-chan recoveryBehaviorPostgresContenderStart
	result  <-chan error
	done    <-chan struct{}
}

func startRecoveryBehaviorPostgresContender(
	ctx context.Context,
	db *gorm.DB,
	operation func(*gorm.DB) error,
) recoveryBehaviorPostgresContender {
	started := make(chan recoveryBehaviorPostgresContenderStart, 1)
	result := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		tx := db.WithContext(ctx).Begin()
		if tx.Error != nil {
			started <- recoveryBehaviorPostgresContenderStart{err: tx.Error}
			result <- tx.Error
			return
		}
		var pid int
		if err := tx.WithContext(ctx).Raw("SELECT pg_backend_pid()").Scan(&pid).Error; err != nil {
			_ = tx.Rollback().Error
			started <- recoveryBehaviorPostgresContenderStart{err: err}
			result <- err
			return
		}
		started <- recoveryBehaviorPostgresContenderStart{pid: pid}
		err := operation(tx)
		if err != nil {
			_ = tx.Rollback().Error
			result <- err
			return
		}
		result <- tx.Commit().Error
	}()
	return recoveryBehaviorPostgresContender{started: started, result: result, done: done}
}

func waitRecoveryBehaviorPostgresContenderStart(
	t *testing.T,
	ctx context.Context,
	contender recoveryBehaviorPostgresContender,
) int {
	t.Helper()
	select {
	case started := <-contender.started:
		if started.err != nil || started.pid <= 0 {
			t.Fatalf("start PostgreSQL Drill/Recovery contender pid=%d: %v", started.pid, started.err)
		}
		return started.pid
	case <-ctx.Done():
		t.Fatalf("wait for PostgreSQL Drill/Recovery contender backend PID: %v", ctx.Err())
		return 0
	}
}

func waitRecoveryBehaviorPostgresContenderResult(
	t *testing.T,
	ctx context.Context,
	contender recoveryBehaviorPostgresContender,
) error {
	t.Helper()
	select {
	case err := <-contender.result:
		return err
	case <-ctx.Done():
		t.Fatalf("wait for PostgreSQL Drill/Recovery contender result: %v", ctx.Err())
		return ctx.Err()
	}
}

func waitForRecoveryBehaviorPostgresPIDBlockedBy(
	t *testing.T,
	ctx context.Context,
	db *gorm.DB,
	contender recoveryBehaviorPostgresContender,
	blockedPID int,
	blockerPID int,
) {
	t.Helper()
	var last struct {
		WaitEventType   string
		WaitEvent       string
		BlockedByHolder bool
	}
	err := db.WithContext(ctx).Connection(func(observer *gorm.DB) error {
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-contender.done:
				return fmt.Errorf("contender completed before lock wait: %v", <-contender.result)
			default:
			}
			result := observer.WithContext(ctx).Raw(`SELECT
				COALESCE(wait_event_type, '') AS wait_event_type,
				COALESCE(wait_event, '') AS wait_event,
				COALESCE(? = ANY(pg_blocking_pids(pid)), false) AS blocked_by_holder
				FROM pg_stat_activity WHERE pid = ?`, blockerPID, blockedPID).Scan(&last)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 1 && last.WaitEventType == "Lock" && last.BlockedByHolder {
				return nil
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
			}
		}
	})
	if err != nil {
		t.Fatalf(
			"PostgreSQL contender pid %d was not lock-blocked by holder pid %d (last=%s/%s blocked=%t): %v",
			blockedPID,
			blockerPID,
			last.WaitEventType,
			last.WaitEvent,
			last.BlockedByHolder,
			err,
		)
	}
}

func recoveryBehaviorPostgresBackendPID(t *testing.T, tx *gorm.DB) int {
	t.Helper()
	var pid int
	if err := tx.Raw("SELECT pg_backend_pid()").Scan(&pid).Error; err != nil {
		t.Fatalf("load PostgreSQL holder backend PID: %v", err)
	}
	if pid <= 0 {
		t.Fatalf("PostgreSQL holder backend PID=%d", pid)
	}
	return pid
}

func recoveryBehaviorPostgresRaceNodeID(t *testing.T, nodeRole string, sourceNodeID, sandboxNodeID uint) uint {
	t.Helper()
	switch nodeRole {
	case "source":
		return sourceNodeID
	case "sandbox":
		return sandboxNodeID
	default:
		t.Fatalf("unknown PostgreSQL Drill/Recovery matrix node %q", nodeRole)
		return 0
	}
}

func claimOutOfBandRecoveryBehaviorLeaseTx(ctx context.Context, tx *gorm.DB, nodeID uint) error {
	var boundary struct {
		ID uint
	}
	result := tx.WithContext(ctx).Raw("SELECT id FROM nodes WHERE id = ? FOR UPDATE", nodeID).Scan(&boundary)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 || boundary.ID != nodeID {
		return fmt.Errorf("PostgreSQL out-of-band Recovery node boundary is unavailable")
	}
	return tx.WithContext(ctx).Create(newRecoveryBehaviorLease(nodeID)).Error
}

func assertRecoveryBehaviorPostgresDrillRaceState(
	t *testing.T,
	db *gorm.DB,
	taskID uint,
	expectedRunID uint,
	sourceNodeID uint,
	sandboxNodeID uint,
	wantPairStatus string,
	wantLeaseNodeID uint,
) {
	t.Helper()
	var runs []model.TaskRun
	if err := db.Where("task_id = ? AND trigger_type = ?", taskID, "drill").Order("id ASC").Find(&runs).Error; err != nil {
		t.Fatalf("load PostgreSQL Drill race TaskRuns: %v", err)
	}
	var evidences []model.RestoreDrillEvidence
	if err := db.Where("task_id = ?", taskID).Order("id ASC").Find(&evidences).Error; err != nil {
		t.Fatalf("load PostgreSQL Drill race Evidence rows: %v", err)
	}
	if wantPairStatus == "" {
		if len(runs) != 0 || len(evidences) != 0 {
			t.Fatalf("PostgreSQL Recovery winner left Drill TaskRun/Evidence rows=%d/%d", len(runs), len(evidences))
		}
	} else {
		if len(runs) != 1 || len(evidences) != 1 {
			t.Fatalf("PostgreSQL Drill pair rows=%d/%d, want exactly 1/1", len(runs), len(evidences))
		}
		run := runs[0]
		evidence := evidences[0]
		if run.ID != expectedRunID || evidence.TaskRunID != run.ID || run.NodeIDSnapshot != sourceNodeID ||
			evidence.SandboxNodeID != sandboxNodeID || run.Status != wantPairStatus || evidence.Status != wantPairStatus {
			t.Fatalf(
				"PostgreSQL Drill pair mismatch run=%+v evidence_run/status/sandbox=%d/%q/%d, want id/source/status/sandbox=%d/%d/%q/%d",
				run,
				evidence.TaskRunID,
				evidence.Status,
				evidence.SandboxNodeID,
				expectedRunID,
				sourceNodeID,
				wantPairStatus,
				sandboxNodeID,
			)
		}
		switch wantPairStatus {
		case model.TaskRunStatusPending:
			if run.StartedAt != nil || evidence.StartedAt != nil {
				t.Fatalf("PostgreSQL pending Drill pair has started timestamps run/evidence=%v/%v", run.StartedAt, evidence.StartedAt)
			}
		case model.TaskRunStatusRunning:
			if run.StartedAt == nil || evidence.StartedAt == nil || !run.StartedAt.Equal(*evidence.StartedAt) {
				t.Fatalf("PostgreSQL running Drill pair timestamps run/evidence=%v/%v, want matching non-nil", run.StartedAt, evidence.StartedAt)
			}
		default:
			t.Fatalf("unsupported PostgreSQL Drill race pair status %q", wantPairStatus)
		}
	}

	var leases []model.BackupAssetRecoveryNodeLease
	if err := db.Where("node_id IN ? AND state = ?", []uint{sourceNodeID, sandboxNodeID}, "active").
		Order("node_id ASC, id ASC").Find(&leases).Error; err != nil {
		t.Fatalf("load PostgreSQL Drill race Recovery leases: %v", err)
	}
	if wantLeaseNodeID == 0 {
		if len(leases) != 0 {
			t.Fatalf("PostgreSQL Drill winner left active Recovery leases=%+v", leases)
		}
		return
	}
	if len(leases) != 1 || leases[0].NodeID != wantLeaseNodeID {
		t.Fatalf("PostgreSQL Recovery winner leases=%+v, want exactly one on node %d", leases, wantLeaseNodeID)
	}
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

func (barrier *recoveryBehaviorStartBarrier) AdmitDrillTx(
	ctx context.Context,
	tx *gorm.DB,
	sourceNodeID uint,
	sandboxNodeID uint,
) error {
	return barrier.delegate.AdmitDrillTx(ctx, tx, sourceNodeID, sandboxNodeID)
}

func (barrier *recoveryBehaviorStartBarrier) EnterDrillExecutionTx(
	ctx context.Context,
	tx *gorm.DB,
	runID uint,
	sandboxNodeID uint,
	startedAt time.Time,
) error {
	return barrier.delegate.EnterDrillExecutionTx(ctx, tx, runID, sandboxNodeID, startedAt)
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
		&model.TaskLog{}, &model.TaskTrafficSample{}, &model.RestoreDrillEvidence{},
		&model.BackupAssetRecoveryNodeLease{},
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

func reserveRecoveryBehaviorDrill(
	ctx context.Context,
	fixture recoveryBehaviorFixture,
	taskEntity model.Task,
	sandboxNodeID uint,
) error {
	return fixture.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		_, err := reserveRecoveryBehaviorDrillTx(ctx, fixture, tx, taskEntity, sandboxNodeID)
		return err
	})
}

func reserveRecoveryBehaviorDrillTx(
	ctx context.Context,
	fixture recoveryBehaviorFixture,
	tx *gorm.DB,
	taskEntity model.Task,
	sandboxNodeID uint,
) (uint, error) {
	if err := fixture.coordinator.AdmitDrillTx(ctx, tx, taskEntity.NodeID, sandboxNodeID); err != nil {
		return 0, err
	}
	run := model.TaskRun{TaskID: taskEntity.ID, TriggerType: "drill", Status: model.TaskRunStatusPending}
	if err := tx.WithContext(ctx).Create(&run).Error; err != nil {
		return 0, err
	}
	if err := tx.WithContext(ctx).Create(&model.RestoreDrillEvidence{
		PolicyID: 1, TaskID: taskEntity.ID, TaskRunID: run.ID,
		SandboxNodeID: sandboxNodeID, SandboxPath: "/tmp/recovery-behavior-drill",
		Status: model.TaskRunStatusPending,
	}).Error; err != nil {
		return 0, err
	}
	return run.ID, nil
}

func enterRecoveryBehaviorDrillTx(
	ctx context.Context,
	fixture recoveryBehaviorFixture,
	tx *gorm.DB,
	runID uint,
	sandboxNodeID uint,
	startedAt time.Time,
) error {
	if err := fixture.coordinator.EnterDrillExecutionTx(ctx, tx, runID, sandboxNodeID, startedAt); err != nil {
		return err
	}
	result := tx.WithContext(ctx).Model(&model.RestoreDrillEvidence{}).
		Where("task_run_id = ? AND sandbox_node_id = ? AND status = ?",
			runID, sandboxNodeID, model.TaskRunStatusPending).
		Updates(map[string]interface{}{"status": model.TaskRunStatusRunning, "started_at": &startedAt})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return task.ErrNodeWriteStartLost
	}
	return nil
}

func assertRecoveryBehaviorDrillPairStatus(t *testing.T, db *gorm.DB, runID uint, want string) {
	t.Helper()
	var run model.TaskRun
	var evidence model.RestoreDrillEvidence
	if err := db.First(&run, runID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("task_run_id = ?", runID).Take(&evidence).Error; err != nil {
		t.Fatal(err)
	}
	if run.Status != want || evidence.Status != want {
		t.Fatalf("PostgreSQL Drill pair TaskRun/Evidence=%q/%q, want %q/%q", run.Status, evidence.Status, want, want)
	}
}

func claimRecoveryBehaviorLease(ctx context.Context, fixture recoveryBehaviorFixture, nodeID uint) error {
	return fixture.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return claimRecoveryBehaviorLeaseTx(ctx, fixture, tx, nodeID)
	})
}

func claimRecoveryBehaviorLeaseTx(
	ctx context.Context,
	fixture recoveryBehaviorFixture,
	tx *gorm.DB,
	nodeID uint,
) error {
	if err := fixture.coordinator.AdmitRecoveryTx(ctx, tx, nodeID); err != nil {
		return err
	}
	return tx.WithContext(ctx).Create(newRecoveryBehaviorLease(nodeID)).Error
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
