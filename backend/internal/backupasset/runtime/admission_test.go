package runtime

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/model"
	"xirang/backend/internal/task"
	taskexecutor "xirang/backend/internal/task/executor"

	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var allResticOperations = []publication.ResticOperation{
	publication.OperationLegacyBackup,
	publication.OperationLegacySnapshotList,
	publication.OperationLegacySnapshotFiles,
	publication.OperationLegacyIndex,
	publication.OperationLegacySearch,
	publication.OperationLegacyDiff,
	publication.OperationLegacySnapshotRestore,
	publication.OperationLegacyRestoreLatest,
	publication.OperationLegacyAnomaly,
	publication.OperationLegacyRetention,
	publication.OperationEvidenceBackup,
	publication.OperationManifest,
	publication.OperationReconcile,
}

func TestAdmissionTransitionDrainsEveryResticOperation(t *testing.T) {
	for _, operation := range allResticOperations {
		t.Run(string(operation), func(t *testing.T) {
			barrier := newTestAdmission(t, publication.AdmissionPristineLegacy)
			token, err := barrier.Acquire(context.Background(), operation)
			if err != nil {
				t.Fatal(err)
			}
			persisted := make(chan struct{})
			done := make(chan error, 1)
			go func() {
				done <- barrier.transition(context.Background(), publication.AdmissionManaged, func() error {
					close(persisted)
					return nil
				})
			}()
			select {
			case <-persisted:
				t.Fatal("transition persisted before admitted operation drained")
			case <-time.After(25 * time.Millisecond):
			}
			if err := token.Close(); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAdmissionFailedDrainPreservesPriorModeAndGeneration(t *testing.T) {
	barrier := newTestAdmission(t, publication.AdmissionPristineLegacy)
	token, err := barrier.Acquire(context.Background(), publication.OperationLegacyBackup)
	if err != nil {
		t.Fatal(err)
	}
	beforeMode, beforeGeneration := barrier.current()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := barrier.transition(ctx, publication.AdmissionManaged, func() error { return nil }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("transition error=%v, want deadline exceeded", err)
	}
	afterMode, afterGeneration := barrier.current()
	if afterMode != beforeMode || afterGeneration != beforeGeneration {
		t.Fatalf("failed drain changed state: before=%s/%d after=%s/%d", beforeMode, beforeGeneration, afterMode, afterGeneration)
	}
	if err := token.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAdmissionPersistFailureReopensPriorGeneration(t *testing.T) {
	barrier := newTestAdmission(t, publication.AdmissionPristineLegacy)
	beforeMode, beforeGeneration := barrier.current()
	persistErr := errors.New("persist failed")
	if err := barrier.transition(context.Background(), publication.AdmissionManaged, func() error { return persistErr }); !errors.Is(err, persistErr) {
		t.Fatalf("transition error=%v, want persist error", err)
	}
	afterMode, afterGeneration := barrier.current()
	if afterMode != beforeMode || afterGeneration != beforeGeneration {
		t.Fatalf("persist failure changed state: before=%s/%d after=%s/%d", beforeMode, beforeGeneration, afterMode, afterGeneration)
	}
	token, err := barrier.Acquire(context.Background(), publication.OperationLegacyBackup)
	if err != nil {
		t.Fatalf("prior generation did not reopen: %v", err)
	}
	defer func() { _ = token.Close() }()
}

func TestAdmissionTokenCloseIsIdempotentAndCannotUnderflow(t *testing.T) {
	barrier := newTestAdmission(t, publication.AdmissionPristineLegacy)
	token, err := barrier.Acquire(context.Background(), publication.OperationLegacyBackup)
	if err != nil {
		t.Fatal(err)
	}
	if err := token.Close(); err != nil {
		t.Fatal(err)
	}
	if err := token.Close(); err != nil {
		t.Fatal(err)
	}
	if active := barrier.activeCount(); active != 0 {
		t.Fatalf("active tokens=%d, want zero", active)
	}
}

func TestAdmissionTokenSnapshotsModeAndGenerationAcrossTransition(t *testing.T) {
	barrier := newTestAdmission(t, publication.AdmissionPristineLegacy)
	oldToken, err := barrier.Acquire(context.Background(), publication.OperationLegacyBackup)
	if err != nil {
		t.Fatal(err)
	}
	oldMode, oldGeneration := oldToken.Mode(), oldToken.Generation()
	done := make(chan error, 1)
	go func() {
		done <- barrier.transition(context.Background(), publication.AdmissionManaged, func() error { return nil })
	}()
	if err := oldToken.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if oldToken.Mode() != oldMode || oldToken.Generation() != oldGeneration {
		t.Fatalf("old token mutated across transition: mode=%s generation=%d", oldToken.Mode(), oldToken.Generation())
	}
	newToken, err := barrier.Acquire(context.Background(), publication.OperationEvidenceBackup)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = newToken.Close() }()
	if newToken.Mode() != publication.AdmissionManaged || newToken.Generation() != oldGeneration+1 {
		t.Fatalf("new token=%s/%d, want managed/%d", newToken.Mode(), newToken.Generation(), oldGeneration+1)
	}
}

func TestAdmissionStopRejectsNewTokensAndWaitsForCurrentTokens(t *testing.T) {
	barrier := newTestAdmission(t, publication.AdmissionPristineLegacy)
	token, err := barrier.Acquire(context.Background(), publication.OperationLegacyBackup)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- barrier.stop(context.Background()) }()
	deadline := time.Now().Add(time.Second)
	for !barrier.isStopping() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !barrier.isStopping() {
		t.Fatal("barrier did not enter stopping state")
	}
	if _, err := barrier.Acquire(context.Background(), publication.OperationLegacySnapshotList); err == nil {
		t.Fatal("stopping barrier admitted a new operation")
	}
	if err := token.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestAdmissionStopAcceptingRejectsNewTokensWithoutWaitingForCurrentTokens(t *testing.T) {
	barrier := newTestAdmission(t, publication.AdmissionPristineLegacy)
	token, err := barrier.Acquire(context.Background(), publication.OperationLegacySnapshotList)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		barrier.stopAccepting()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stop accepting waited for an active token")
	}
	if _, err := barrier.Acquire(context.Background(), publication.OperationLegacySnapshotList); !errors.Is(err, ErrAdmissionStopped) {
		t.Fatalf("new token after stop-accepting error=%v", err)
	}
	if err := token.Close(); err != nil {
		t.Fatal(err)
	}
	if err := barrier.stop(context.Background()); err != nil {
		t.Fatalf("full stop after active token closed: %v", err)
	}
}

func TestAdmissionDoesNotUpgradeAnOperationTokenIntoTransition(t *testing.T) {
	barrier := newTestAdmission(t, publication.AdmissionPristineLegacy)
	token, err := barrier.Acquire(context.Background(), publication.OperationLegacyBackup)
	if err != nil {
		t.Fatal(err)
	}
	if token.Operation() != publication.OperationLegacyBackup || token.Mode() != publication.AdmissionPristineLegacy {
		t.Fatalf("token unexpectedly carries transition authority: operation=%s mode=%s", token.Operation(), token.Mode())
	}
	if err := token.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAdmissionCanceledBeforeDrainReopensPriorGeneration(t *testing.T) {
	barrier := newTestAdmission(t, publication.AdmissionPristineLegacy)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := barrier.transition(ctx, publication.AdmissionManaged, func() error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled transition error=%v, want context canceled", err)
	}
	acquireCtx, acquireCancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer acquireCancel()
	token, err := barrier.Acquire(acquireCtx, publication.OperationLegacyBackup)
	if err != nil {
		t.Fatalf("canceled transition left prior generation closed: %v", err)
	}
	defer func() { _ = token.Close() }()
	if token.Mode() != publication.AdmissionPristineLegacy {
		t.Fatalf("canceled transition changed token mode to %s", token.Mode())
	}
}

func newTestAdmission(t *testing.T, mode publication.AdmissionMode) *admissionBarrier {
	t.Helper()
	barrier, err := newAdmissionBarrier(mode)
	if err != nil {
		t.Fatal(err)
	}
	return barrier
}

func TestAdmissionConcurrentClosesDoNotLoseDrainWakeup(t *testing.T) {
	barrier := newTestAdmission(t, publication.AdmissionPristineLegacy)
	tokens := make([]publication.AdmissionToken, 0, len(allResticOperations))
	for _, operation := range allResticOperations {
		token, err := barrier.Acquire(context.Background(), operation)
		if err != nil {
			t.Fatal(err)
		}
		tokens = append(tokens, token)
	}
	var persisted atomic.Bool
	done := make(chan error, 1)
	go func() {
		done <- barrier.transition(context.Background(), publication.AdmissionManaged, func() error { persisted.Store(true); return nil })
	}()
	for _, token := range tokens {
		go func(token publication.AdmissionToken) { _ = token.Close() }(token)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !persisted.Load() {
		t.Fatal("drained transition did not persist")
	}
}

var (
	nodeWriteTestDBSequence atomic.Uint64
	nodeWriteLeaseSequence  atomic.Uint64
)

func TestNodeWriteCoordinatorTaskAdmissionRejectsActiveRecoveryLease(t *testing.T) {
	db := openNodeWriteCoordinatorTestDB(t)
	node, _ := seedNodeWriteCoordinatorTask(t, db, "active-lease")
	coordinator, err := NewNodeWriteCoordinator(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Create(nodeWriteTestLease(node.ID)).Error; err != nil {
		t.Fatal(err)
	}

	err = db.Transaction(func(tx *gorm.DB) error {
		return coordinator.AdmitTaskTx(context.Background(), tx, node.ID)
	})
	if !errors.Is(err, task.ErrNodeWriteConflict) {
		t.Fatalf("task admission error=%v, want node-write conflict", err)
	}
}

func TestNodeWriteCoordinatorUnexpiredRecoveryLeaseBlocksTaskAndRecovery(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		admit func(context.Context, *gorm.DB, *NodeWriteCoordinator, uint) error
	}{
		{
			name: "task",
			admit: func(ctx context.Context, tx *gorm.DB, coordinator *NodeWriteCoordinator, nodeID uint) error {
				return coordinator.AdmitTaskTx(ctx, tx, nodeID)
			},
		},
		{
			name: "recovery",
			admit: func(ctx context.Context, tx *gorm.DB, coordinator *NodeWriteCoordinator, nodeID uint) error {
				return coordinator.AdmitRecoveryTx(ctx, tx, nodeID)
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db := openNodeWriteCoordinatorTestDB(t)
			node, _ := seedNodeWriteCoordinatorTask(t, db, "unexpired-"+testCase.name)
			now := time.Now().UTC().Truncate(time.Second)
			lease := nodeWriteTestLeaseAt(node.ID, now.Add(-time.Minute), now.Add(time.Minute))
			if err := db.Create(lease).Error; err != nil {
				t.Fatal(err)
			}
			coordinator := newNodeWriteCoordinatorAt(t, db, now)

			err := db.Transaction(func(tx *gorm.DB) error {
				return testCase.admit(context.Background(), tx, coordinator, node.ID)
			})
			if !errors.Is(err, task.ErrNodeWriteConflict) {
				t.Fatalf("unexpired lease admission error=%v, want node-write conflict", err)
			}
			assertNodeWriteLeaseState(t, db, lease.ID, "active", nil)
		})
	}
}

func TestNodeWriteCoordinatorExpiredRecoveryLeaseAdmitsTaskAndMarksLeaseExpired(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		expiresAt func(time.Time) time.Time
	}{
		{name: "past deadline", expiresAt: func(now time.Time) time.Time { return now.Add(-time.Second) }},
		{name: "exact deadline", expiresAt: func(now time.Time) time.Time { return now }},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db := openNodeWriteCoordinatorTestDB(t)
			node, _ := seedNodeWriteCoordinatorTask(t, db, "expired-task-"+strings.ReplaceAll(testCase.name, " ", "-"))
			now := time.Now().UTC().Truncate(time.Second)
			lease := nodeWriteTestLeaseAt(node.ID, now.Add(-time.Minute), testCase.expiresAt(now))
			if err := db.Create(lease).Error; err != nil {
				t.Fatal(err)
			}
			coordinator := newNodeWriteCoordinatorAt(t, db, now)

			if err := db.Transaction(func(tx *gorm.DB) error {
				return coordinator.AdmitTaskTx(context.Background(), tx, node.ID)
			}); err != nil {
				t.Fatalf("expired lease blocked task admission: %v", err)
			}
			assertNodeWriteLeaseState(t, db, lease.ID, "expired", &now)
		})
	}
}

func TestNodeWriteCoordinatorExpiredRecoveryLeaseAdmitsFreshRecoveryClaim(t *testing.T) {
	db := openNodeWriteCoordinatorTestDB(t)
	node, _ := seedNodeWriteCoordinatorTask(t, db, "expired-recovery")
	now := time.Now().UTC().Truncate(time.Second)
	expiredLease := nodeWriteTestLeaseAt(node.ID, now.Add(-time.Minute), now)
	if err := db.Create(expiredLease).Error; err != nil {
		t.Fatal(err)
	}
	coordinator := newNodeWriteCoordinatorAt(t, db, now)
	freshLease := nodeWriteTestLeaseAt(node.ID, now, now.Add(time.Minute))

	if err := claimNodeWriteRecoveryLease(context.Background(), db, coordinator, freshLease); err != nil {
		t.Fatalf("expired lease blocked fresh Recovery claimant: %v", err)
	}
	assertNodeWriteLeaseState(t, db, expiredLease.ID, "expired", &now)
	assertNodeWriteLeaseState(t, db, freshLease.ID, "active", nil)
}

func TestNodeWriteCoordinatorExpiredRecoveryLeaseConcurrentClaimHasOneDurableWinner(t *testing.T) {
	db := openNodeWriteCoordinatorTestDB(t)
	node, taskEntity := seedNodeWriteCoordinatorTask(t, db, "expired-race")
	now := time.Now().UTC().Truncate(time.Second)
	expiredLease := nodeWriteTestLeaseAt(node.ID, now.Add(-time.Minute), now.Add(-time.Second))
	if err := db.Create(expiredLease).Error; err != nil {
		t.Fatal(err)
	}
	coordinator := newNodeWriteCoordinatorAt(t, db, now)
	freshLease := nodeWriteTestLeaseAt(node.ID, now, now.Add(time.Minute))

	start := make(chan struct{})
	results := make(chan error, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() {
		<-start
		results <- reserveNodeWriteTask(ctx, db, coordinator, taskEntity)
	}()
	go func() {
		<-start
		results <- claimNodeWriteRecoveryLease(ctx, db, coordinator, freshLease)
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
			t.Fatalf("concurrent expired-lease claim returned unstable error: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent results successes=%d conflicts=%d, want 1/1", successes, conflicts)
	}
	assertNodeWriteLeaseState(t, db, expiredLease.ID, "expired", &now)

	var runCount, freshLeaseCount int64
	if err := db.Model(&model.TaskRun{}).Where("status IN ?", []string{"pending", "running"}).Count(&runCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.BackupAssetRecoveryNodeLease{}).
		Where("id = ? AND state = ?", freshLease.ID, "active").Count(&freshLeaseCount).Error; err != nil {
		t.Fatal(err)
	}
	if runCount+freshLeaseCount != 1 {
		t.Fatalf("durable TaskRun/fresh-lease winners=%d/%d, want exactly one", runCount, freshLeaseCount)
	}
}

func TestNodeWriteCoordinatorExpiredRecoveryLeaseRejectsStaleRenewAndRelease(t *testing.T) {
	db := openNodeWriteCoordinatorTestDB(t)
	node, _ := seedNodeWriteCoordinatorTask(t, db, "expired-stale-owner")
	now := time.Now().UTC().Truncate(time.Second)
	lease := nodeWriteTestLeaseAt(node.ID, now.Add(-time.Minute), now.Add(-time.Second))
	if err := db.Create(lease).Error; err != nil {
		t.Fatal(err)
	}
	coordinator := newNodeWriteCoordinatorAt(t, db, now)
	if err := db.Transaction(func(tx *gorm.DB) error {
		return coordinator.AdmitTaskTx(context.Background(), tx, node.ID)
	}); err != nil {
		t.Fatalf("expire old owner lease: %v", err)
	}

	staleRenew := db.Model(&model.BackupAssetRecoveryNodeLease{}).
		Where("id = ? AND owner_id = ? AND fence = ? AND state = ?", lease.ID, lease.OwnerID, lease.Fence, "active").
		Updates(map[string]interface{}{"lease_expires_at": now.Add(time.Hour), "updated_at": now})
	if staleRenew.Error != nil {
		t.Fatal(staleRenew.Error)
	}
	if staleRenew.RowsAffected != 0 {
		t.Fatalf("stale owner renewed expired lease: rows=%d", staleRenew.RowsAffected)
	}
	staleRelease := db.Model(&model.BackupAssetRecoveryNodeLease{}).
		Where("id = ? AND owner_id = ? AND fence = ? AND state = ?", lease.ID, lease.OwnerID, lease.Fence, "active").
		Updates(map[string]interface{}{"state": "released", "released_at": now, "updated_at": now})
	if staleRelease.Error != nil {
		t.Fatal(staleRelease.Error)
	}
	if staleRelease.RowsAffected != 0 {
		t.Fatalf("stale owner released expired lease: rows=%d", staleRelease.RowsAffected)
	}
	assertNodeWriteLeaseState(t, db, lease.ID, "expired", &now)
}

func TestNodeWriteCoordinatorExpiredRecoveryLeaseTransitionRollsBackWithCaller(t *testing.T) {
	db := openNodeWriteCoordinatorTestDB(t)
	node, _ := seedNodeWriteCoordinatorTask(t, db, "expired-rollback")
	now := time.Now().UTC().Truncate(time.Second)
	createdAt := now.Add(-time.Minute)
	lease := nodeWriteTestLeaseAt(node.ID, createdAt, now.Add(-time.Second))
	if err := db.Create(lease).Error; err != nil {
		t.Fatal(err)
	}
	coordinator := newNodeWriteCoordinatorAt(t, db, now)

	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	if err := coordinator.AdmitTaskTx(context.Background(), tx, node.ID); err != nil {
		_ = tx.Rollback().Error
		t.Fatalf("expired lease blocked admission inside rollback transaction: %v", err)
	}
	assertNodeWriteLeaseState(t, tx, lease.ID, "expired", &now)
	if err := tx.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	assertNodeWriteLeaseState(t, db, lease.ID, "active", nil)
}

func TestNodeWriteCoordinatorExpiredRecoveryLeaseAllowsExecutorEntry(t *testing.T) {
	db := openNodeWriteCoordinatorTestDB(t)
	node, taskEntity := seedNodeWriteCoordinatorTask(t, db, "expired-executor-entry")
	now := time.Now().UTC().Truncate(time.Second)
	lease := nodeWriteTestLeaseAt(node.ID, now.Add(-time.Minute), now.Add(-time.Second))
	if err := db.Create(lease).Error; err != nil {
		t.Fatal(err)
	}
	run := model.TaskRun{TaskID: taskEntity.ID, TriggerType: "manual", Status: "pending"}
	if err := db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	coordinator := newNodeWriteCoordinatorAt(t, db, now)

	if err := db.Transaction(func(tx *gorm.DB) error {
		return coordinator.EnterTaskExecutionTx(context.Background(), tx, run.ID, node.ID, now)
	}); err != nil {
		t.Fatalf("expired lease blocked executor entry: %v", err)
	}
	var storedRun model.TaskRun
	if err := db.First(&storedRun, run.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedRun.Status != "running" || storedRun.StartedAt == nil || !storedRun.StartedAt.Equal(now) {
		t.Fatalf("executor entry run status/started_at=%q/%v, want running/%s", storedRun.Status, storedRun.StartedAt, now)
	}
	assertNodeWriteLeaseState(t, db, lease.ID, "expired", &now)
}

type nodeWriteManagerExecutor struct {
	calls atomic.Int32
}

func (executor *nodeWriteManagerExecutor) Run(context.Context, model.Task, taskexecutor.LogFunc, taskexecutor.ProgressFunc) (int, error) {
	executor.calls.Add(1)
	return 0, nil
}

func (executor *nodeWriteManagerExecutor) RunRestore(context.Context, model.Task, taskexecutor.LogFunc, taskexecutor.ProgressFunc) (int, error) {
	executor.calls.Add(1)
	return 0, nil
}

type nodeWriteManagerExecutorFactory struct {
	executor taskexecutor.Executor
}

func (factory nodeWriteManagerExecutorFactory) Resolve(string) taskexecutor.Executor {
	return factory.executor
}

func TestNodeWriteCoordinatorActiveLeaseRejectsManagerTriggersWithoutResidualRuns(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		trigger func(*task.Manager, uint) (uint, error)
	}{
		{name: "ordinary", trigger: func(manager *task.Manager, taskID uint) (uint, error) {
			return manager.TriggerManual(taskID)
		}},
		{name: "legacy restore", trigger: func(manager *task.Manager, taskID uint) (uint, error) {
			return manager.TriggerRestore(taskID, "/tmp/node-write-manager-restore")
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db := openNodeWriteCoordinatorTestDB(t)
			node, taskEntity := seedNodeWriteCoordinatorTask(t, db, strings.ReplaceAll(testCase.name, " ", "-"))
			if err := db.Create(&model.TaskRun{TaskID: taskEntity.ID, TriggerType: "manual", Status: "success"}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(nodeWriteTestLease(node.ID)).Error; err != nil {
				t.Fatal(err)
			}
			coordinator, err := NewNodeWriteCoordinator(db)
			if err != nil {
				t.Fatal(err)
			}
			executor := &nodeWriteManagerExecutor{}
			manager := task.NewManager(db, nodeWriteManagerExecutorFactory{executor: executor}, nil, nil, nil, nil, 8, 90)
			manager.SetNodeWriteAdmission(coordinator)

			for attempt := 0; attempt < 2; attempt++ {
				runID, triggerErr := testCase.trigger(manager, taskEntity.ID)
				if !errors.Is(triggerErr, task.ErrNodeWriteConflict) {
					t.Fatalf("attempt %d trigger error=%v, want node-write conflict", attempt+1, triggerErr)
				}
				if runID != 0 {
					t.Fatalf("attempt %d conflicted run ID=%d, want zero", attempt+1, runID)
				}
			}
			var residualRuns int64
			query := db.Model(&model.TaskRun{}).Where("task_id = ? AND status IN ?", taskEntity.ID, []string{"pending", "running"})
			if testCase.name == "legacy restore" {
				query = db.Model(&model.TaskRun{}).Where("task_id = ? AND trigger_type = ?", taskEntity.ID, "restore")
			}
			if err := query.Count(&residualRuns).Error; err != nil {
				t.Fatal(err)
			}
			if residualRuns != 0 {
				t.Fatalf("conflicted trigger left %d residual TaskRun rows", residualRuns)
			}
			if executor.calls.Load() != 0 {
				t.Fatalf("conflicted trigger reached executor %d time(s)", executor.calls.Load())
			}
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := manager.Shutdown(shutdownCtx); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNodeWriteCoordinatorActiveSandboxLeaseRejectsManagerDrillWithoutResidualPair(t *testing.T) {
	db := openNodeWriteCoordinatorTestDB(t)
	source, taskEntity := seedNodeWriteCoordinatorTask(t, db, "manager-drill-source")
	sandbox, _ := seedNodeWriteCoordinatorTask(t, db, "manager-drill-sandbox")
	targetNodeID := sandbox.ID
	policy := model.Policy{
		Name: "manager-drill-policy", SourcePath: "/tmp/source", TargetPath: "/tmp/target",
		CronSpec: "@daily", DrillEnabled: true, DrillCron: "@every 5m",
		DrillTargetNodeID: &targetNodeID, DrillRestorePath: "/tmp/node-write-manager-drill",
	}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.PolicyNode{PolicyID: policy.ID, NodeID: source.ID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Updates(map[string]interface{}{
		"policy_id": policy.ID,
		"status":    "success",
	}).Error; err != nil {
		t.Fatal(err)
	}
	finishedAt := time.Now().UTC().Add(-time.Minute)
	if err := db.Create(&model.TaskRun{
		TaskID: taskEntity.ID, TriggerType: "manual", Status: model.TaskRunStatusSuccess, FinishedAt: &finishedAt,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(nodeWriteTestLease(sandbox.ID)).Error; err != nil {
		t.Fatal(err)
	}
	coordinator, err := NewNodeWriteCoordinator(db)
	if err != nil {
		t.Fatal(err)
	}
	manager := task.NewManager(
		db, nodeWriteManagerExecutorFactory{executor: &nodeWriteManagerExecutor{}}, nil, nil, nil, nil, 8, 90,
		task.WithDrillRestoreFunc(func(context.Context, model.Task, model.Node, string, func(string, string)) error {
			return nil
		}),
	)
	manager.SetNodeWriteAdmission(coordinator)
	t.Cleanup(func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer shutdownCancel()
		if err := manager.Shutdown(shutdownCtx); err != nil {
			t.Errorf("shutdown Drill manager: %v", err)
		}
	})
	if err := manager.LoadSchedules(context.Background()); err != nil {
		t.Fatalf("complete Drill recovery sweep: %v", err)
	}

	runID, triggerErr := manager.TriggerDrill(policy.ID, nil)
	if triggerErr == nil || runID != 0 {
		t.Fatalf("sandbox lease Drill trigger run=%d err=%v, want zero/non-nil", runID, triggerErr)
	}
	var runCount, evidenceCount int64
	if err := db.Model(&model.TaskRun{}).
		Where("task_id = ? AND trigger_type = ? AND status IN ?", taskEntity.ID, "drill", model.TaskRunActiveStatuses()).
		Count(&runCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.RestoreDrillEvidence{}).Where("task_id = ?", taskEntity.ID).Count(&evidenceCount).Error; err != nil {
		t.Fatal(err)
	}
	if runCount != 0 || evidenceCount != 0 {
		t.Fatalf("sandbox lease Drill trigger left TaskRun/Evidence=%d/%d", runCount, evidenceCount)
	}
}

func TestNodeWriteCoordinatorRecoveryAdmissionBlocksActiveTaskRuns(t *testing.T) {
	for _, testCase := range []struct {
		status  string
		blocked bool
	}{
		{status: model.TaskRunStatusPending, blocked: true},
		{status: model.TaskRunStatusRunning, blocked: true},
		{status: model.TaskRunStatusRetrying, blocked: true},
		{status: model.TaskRunStatusSuccess},
		{status: model.TaskRunStatusFailed},
		{status: model.TaskRunStatusCanceled},
		{status: model.TaskRunStatusWarning},
		{status: model.TaskRunStatusSkipped},
	} {
		t.Run(testCase.status, func(t *testing.T) {
			db := openNodeWriteCoordinatorTestDB(t)
			node, taskEntity := seedNodeWriteCoordinatorTask(t, db, testCase.status)
			if err := db.Create(&model.TaskRun{TaskID: taskEntity.ID, TriggerType: "manual", Status: testCase.status}).Error; err != nil {
				t.Fatal(err)
			}
			coordinator, err := NewNodeWriteCoordinator(db)
			if err != nil {
				t.Fatal(err)
			}
			err = db.Transaction(func(tx *gorm.DB) error {
				return coordinator.AdmitRecoveryTx(context.Background(), tx, node.ID)
			})
			if testCase.blocked && !errors.Is(err, task.ErrNodeWriteConflict) {
				t.Fatalf("status %q admission error=%v, want conflict", testCase.status, err)
			}
			if !testCase.blocked && err != nil {
				t.Fatalf("terminal status %q blocked recovery admission: %v", testCase.status, err)
			}
		})
	}
}

func TestNodeWriteCoordinatorRecoveryAdmissionBlocksActiveDrillSandbox(t *testing.T) {
	for _, testCase := range []struct {
		name           string
		runStatus      string
		evidenceStatus string
		blocked        bool
	}{
		{name: "pending pair", runStatus: model.TaskRunStatusPending, evidenceStatus: model.TaskRunStatusPending, blocked: true},
		{name: "running pair", runStatus: model.TaskRunStatusRunning, evidenceStatus: model.TaskRunStatusRunning, blocked: true},
		{name: "retrying pair", runStatus: model.TaskRunStatusRetrying, evidenceStatus: model.TaskRunStatusRetrying, blocked: true},
		{name: "active run split", runStatus: model.TaskRunStatusRunning, evidenceStatus: model.TaskRunStatusFailed, blocked: true},
		{name: "active evidence split", runStatus: model.TaskRunStatusFailed, evidenceStatus: model.TaskRunStatusRunning, blocked: true},
		{name: "terminal pair", runStatus: model.TaskRunStatusFailed, evidenceStatus: model.TaskRunStatusFailed},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db := openNodeWriteCoordinatorTestDB(t)
			_, sourceTask := seedNodeWriteCoordinatorTask(t, db, "drill-source-"+strings.ReplaceAll(testCase.name, " ", "-"))
			sandbox, _ := seedNodeWriteCoordinatorTask(t, db, "drill-sandbox-"+strings.ReplaceAll(testCase.name, " ", "-"))
			run := model.TaskRun{
				TaskID: sourceTask.ID, TriggerType: "drill", Status: testCase.runStatus,
			}
			if err := db.Create(&run).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&model.RestoreDrillEvidence{
				PolicyID: 1, TaskID: sourceTask.ID, TaskRunID: run.ID,
				SandboxNodeID: sandbox.ID, SandboxPath: "/tmp/node-write-drill-sandbox",
				Status: testCase.evidenceStatus,
			}).Error; err != nil {
				t.Fatal(err)
			}
			coordinator, err := NewNodeWriteCoordinator(db)
			if err != nil {
				t.Fatal(err)
			}

			err = db.Transaction(func(tx *gorm.DB) error {
				return coordinator.AdmitRecoveryTx(context.Background(), tx, sandbox.ID)
			})
			if testCase.blocked && !errors.Is(err, task.ErrNodeWriteConflict) {
				t.Fatalf("sandbox recovery admission error=%v, want active-drill conflict", err)
			}
			if !testCase.blocked && err != nil {
				t.Fatalf("terminal Drill pair blocked sandbox Recovery: %v", err)
			}
		})
	}
}

func TestNodeWriteCoordinatorDrillAdmissionRejectsRecoveryLeaseOnSourceAndSandbox(t *testing.T) {
	for _, blockedNode := range []string{"source", "sandbox"} {
		t.Run(blockedNode, func(t *testing.T) {
			db := openNodeWriteCoordinatorTestDB(t)
			source, _ := seedNodeWriteCoordinatorTask(t, db, "drill-lease-source-"+blockedNode)
			sandbox, _ := seedNodeWriteCoordinatorTask(t, db, "drill-lease-sandbox-"+blockedNode)
			leaseNodeID := source.ID
			if blockedNode == "sandbox" {
				leaseNodeID = sandbox.ID
			}
			if err := db.Create(nodeWriteTestLease(leaseNodeID)).Error; err != nil {
				t.Fatal(err)
			}
			coordinator, err := NewNodeWriteCoordinator(db)
			if err != nil {
				t.Fatal(err)
			}

			err = db.Transaction(func(tx *gorm.DB) error {
				return coordinator.AdmitDrillTx(context.Background(), tx, source.ID, sandbox.ID)
			})
			if !errors.Is(err, task.ErrNodeWriteConflict) {
				t.Fatalf("%s lease drill admission error=%v, want conflict", blockedNode, err)
			}
		})
	}
}

func TestNodeWriteCoordinatorDrillAdmissionDeduplicatesSameNode(t *testing.T) {
	db := openNodeWriteCoordinatorTestDB(t)
	node, _ := seedNodeWriteCoordinatorTask(t, db, "drill-same-node")
	coordinator, err := NewNodeWriteCoordinator(db)
	if err != nil {
		t.Fatal(err)
	}

	if err := db.Transaction(func(tx *gorm.DB) error {
		return coordinator.AdmitDrillTx(context.Background(), tx, node.ID, node.ID)
	}); err != nil {
		t.Fatalf("same-node drill admission failed: %v", err)
	}
}

func TestNodeWriteCoordinatorDrillLockOrderIsDeterministicAndDeduplicated(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		first      uint
		second     uint
		wantFirst  uint
		wantSecond uint
		wantLength int
	}{
		{name: "ascending", first: 7, second: 42, wantFirst: 7, wantSecond: 42, wantLength: 2},
		{name: "descending", first: 42, second: 7, wantFirst: 7, wantSecond: 42, wantLength: 2},
		{name: "same node", first: 7, second: 7, wantFirst: 7, wantLength: 1},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got := orderedDistinctNodeWriteIDs(testCase.first, testCase.second)
			if len(got) != testCase.wantLength || got[0] != testCase.wantFirst ||
				(testCase.wantLength == 2 && got[1] != testCase.wantSecond) {
				t.Fatalf("ordered node IDs=%v, want length=%d values=%d/%d",
					got, testCase.wantLength, testCase.wantFirst, testCase.wantSecond)
			}
		})
	}
}

func TestNodeWriteCoordinatorDrillAndRecoveryOrdersHaveOneDurableWinner(t *testing.T) {
	for _, testCase := range []struct {
		name          string
		recoveryFirst bool
	}{
		{name: "recovery first", recoveryFirst: true},
		{name: "drill first"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			for _, recoveryNode := range []string{"source", "sandbox"} {
				t.Run(recoveryNode, func(t *testing.T) {
					for attempt := 0; attempt < 10; attempt++ {
						db := openNodeWriteCoordinatorTestDB(t)
						suffix := strings.ReplaceAll(
							fmt.Sprintf("%s-%s-%d", testCase.name, recoveryNode, attempt),
							" ", "-",
						)
						source, sourceTask := seedNodeWriteCoordinatorTask(t, db, "ordered-source-"+suffix)
						sandbox, _ := seedNodeWriteCoordinatorTask(t, db, "ordered-sandbox-"+suffix)
						coordinator, err := NewNodeWriteCoordinator(db)
						if err != nil {
							t.Fatal(err)
						}
						recoveryNodeID := source.ID
						if recoveryNode == "sandbox" {
							recoveryNodeID = sandbox.ID
						}

						if testCase.recoveryFirst {
							if err := claimNodeWriteRecovery(context.Background(), db, coordinator, recoveryNodeID); err != nil {
								t.Fatalf("attempt %d claim Recovery first: %v", attempt+1, err)
							}
							err = reserveNodeWriteDrill(context.Background(), db, coordinator, sourceTask, sandbox.ID)
						} else {
							if err := reserveNodeWriteDrill(context.Background(), db, coordinator, sourceTask, sandbox.ID); err != nil {
								t.Fatalf("attempt %d reserve Drill first: %v", attempt+1, err)
							}
							err = claimNodeWriteRecovery(context.Background(), db, coordinator, recoveryNodeID)
						}
						if !errors.Is(err, task.ErrNodeWriteConflict) {
							t.Fatalf("attempt %d losing %s/%s admission error=%v, want conflict", attempt+1, testCase.name, recoveryNode, err)
						}
						assertNodeWriteDrillRecoveryWinner(t, db, testCase.recoveryFirst)
					}
				})
			}
		})
	}
}

type nodeWriteDrillRecoveryBarrierCase struct {
	phase        string
	recoveryNode string
	winner       string
}

var nodeWriteDrillRecoveryBarrierCases = []nodeWriteDrillRecoveryBarrierCase{
	{phase: "reservation", recoveryNode: "source", winner: "recovery"},
	{phase: "reservation", recoveryNode: "source", winner: "drill"},
	{phase: "reservation", recoveryNode: "sandbox", winner: "recovery"},
	{phase: "reservation", recoveryNode: "sandbox", winner: "drill"},
	{phase: "start", recoveryNode: "source", winner: "recovery"},
	{phase: "start", recoveryNode: "source", winner: "drill"},
	{phase: "start", recoveryNode: "sandbox", winner: "recovery"},
	{phase: "start", recoveryNode: "sandbox", winner: "drill"},
}

func TestNodeWriteCoordinatorDrillRecoveryBarrierMatrixIsComplete(t *testing.T) {
	want := map[nodeWriteDrillRecoveryBarrierCase]bool{}
	for _, phase := range []string{"reservation", "start"} {
		for _, recoveryNode := range []string{"source", "sandbox"} {
			for _, winner := range []string{"recovery", "drill"} {
				want[nodeWriteDrillRecoveryBarrierCase{phase: phase, recoveryNode: recoveryNode, winner: winner}] = true
			}
		}
	}
	seen := make(map[nodeWriteDrillRecoveryBarrierCase]bool, len(nodeWriteDrillRecoveryBarrierCases))
	for _, testCase := range nodeWriteDrillRecoveryBarrierCases {
		if seen[testCase] {
			t.Fatalf("Drill/Recovery barrier matrix contains duplicate case: %+v", testCase)
		}
		seen[testCase] = true
		delete(want, testCase)
	}
	if len(want) != 0 || len(seen) != 8 {
		t.Fatalf("Drill/Recovery barrier matrix is missing %d case(s): %v", len(want), want)
	}
}

func TestNodeWriteCoordinatorSQLiteDrillRecoveryBarrierMatrix(t *testing.T) {
	for _, testCase := range nodeWriteDrillRecoveryBarrierCases {
		winnerLabel := testCase.winner + "-first"
		if testCase.phase == "start" && testCase.winner == "recovery" {
			winnerLabel = "legacy-out-of-band-recovery-first"
		}
		t.Run(fmt.Sprintf("%s/%s/%s", testCase.phase, testCase.recoveryNode, winnerLabel), func(t *testing.T) {
			for attempt := 0; attempt < 3; attempt++ {
				t.Run(fmt.Sprintf("attempt-%d", attempt+1), func(t *testing.T) {
					testNodeWriteSQLiteDrillRecoveryBarrierCase(t, testCase, attempt)
				})
			}
		})
	}
}

func testNodeWriteSQLiteDrillRecoveryBarrierCase(
	t *testing.T,
	testCase nodeWriteDrillRecoveryBarrierCase,
	attempt int,
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	db := openNodeWriteCoordinatorTestDB(t)
	suffix := fmt.Sprintf("barrier-%s-%s-%s-%d", testCase.phase, testCase.recoveryNode, testCase.winner, attempt)
	source, sourceTask := seedNodeWriteCoordinatorTask(t, db, suffix+"-source")
	sandbox, _ := seedNodeWriteCoordinatorTask(t, db, suffix+"-sandbox")
	coordinator, err := NewNodeWriteCoordinator(db)
	if err != nil {
		t.Fatal(err)
	}
	recoveryNodeID := source.ID
	if testCase.recoveryNode == "sandbox" {
		recoveryNodeID = sandbox.ID
	}

	var runID uint
	if testCase.phase == "start" {
		runID, err = reserveNodeWriteDrillPair(ctx, db, coordinator, sourceTask, sandbox.ID)
		if err != nil {
			t.Fatalf("reserve pending Drill pair: %v", err)
		}
	}

	winnerTx := db.WithContext(ctx).Begin()
	if winnerTx.Error != nil {
		t.Fatal(winnerTx.Error)
	}
	winnerCommitted := false
	defer func() {
		if !winnerCommitted {
			_ = winnerTx.Rollback().Error
		}
	}()

	switch testCase.winner {
	case "recovery":
		lease := nodeWriteTestLease(recoveryNodeID)
		if testCase.phase == "reservation" {
			err = claimNodeWriteRecoveryLeaseTx(ctx, winnerTx, coordinator, lease)
		} else {
			// A normally admitted Recovery cannot win after the pending Drill pair
			// exists. This branch deterministically models the documented
			// legacy/out-of-band lease that appears between reservation and start.
			err = insertNodeWriteLegacyRecoveryLeaseTx(ctx, winnerTx, lease)
		}
	case "drill":
		if testCase.phase == "reservation" {
			runID, err = reserveNodeWriteDrillPairTx(ctx, winnerTx, coordinator, sourceTask, sandbox.ID)
		} else {
			err = startNodeWriteDrillPairTx(ctx, winnerTx, coordinator, runID, sandbox.ID, time.Now().UTC())
		}
	default:
		t.Fatalf("unsupported matrix winner %q", testCase.winner)
	}
	if err != nil {
		t.Fatalf("prepare uncommitted %s winner: %v", testCase.winner, err)
	}

	loserEntered := make(chan struct{})
	loserRelease := make(chan struct{})
	var releaseOnce sync.Once
	releaseLoser := func() { releaseOnce.Do(func() { close(loserRelease) }) }
	defer releaseLoser()
	loserDB := openNodeWriteSQLiteBoundaryBarrierDB(
		t,
		db,
		loserEntered,
		loserRelease,
		nodeWriteSQLiteLoserBoundaryMatch(testCase),
	)
	loserCoordinator, err := NewNodeWriteCoordinator(loserDB)
	if err != nil {
		t.Fatal(err)
	}
	lockContended := make(chan struct{})
	retryRelease := make(chan struct{})
	var contentionOnce sync.Once
	var retryReleaseOnce sync.Once
	releaseRetry := func() { retryReleaseOnce.Do(func() { close(retryRelease) }) }
	defer releaseRetry()
	loserCoordinator.retryWait = func(waitCtx context.Context, _ int) error {
		contentionOnce.Do(func() { close(lockContended) })
		select {
		case <-retryRelease:
			return nil
		case <-waitCtx.Done():
			return waitCtx.Err()
		}
	}
	loserResult := make(chan error, 1)
	go func() {
		if testCase.winner == "recovery" {
			if testCase.phase == "reservation" {
				_, loserErr := reserveNodeWriteDrillPair(ctx, loserDB, loserCoordinator, sourceTask, sandbox.ID)
				loserResult <- loserErr
				return
			}
			loserResult <- loserDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				return startNodeWriteDrillPairTx(ctx, tx, loserCoordinator, runID, sandbox.ID, time.Now().UTC())
			})
			return
		}
		loserResult <- claimNodeWriteRecoveryLease(ctx, loserDB, loserCoordinator, nodeWriteTestLease(recoveryNodeID))
	}()

	select {
	case <-loserEntered:
		// Both transactions now exist: the winner has completed admission and
		// its paired mutation, while the loser is paused immediately before its
		// admission/start recheck reaches the SQLite driver.
	case <-ctx.Done():
		t.Fatalf("loser did not reach deterministic SQLite boundary: %v", ctx.Err())
	}
	releaseLoser()
	select {
	case <-lockContended:
		// retryWait is reached only after the SQLite driver returns BUSY/LOCKED,
		// proving the loser actually contended with the still-uncommitted winner.
	case <-ctx.Done():
		t.Fatalf("loser did not encounter the uncommitted SQLite winner: %v", ctx.Err())
	}
	if err := winnerTx.Commit().Error; err != nil {
		t.Fatalf("commit %s winner: %v", testCase.winner, err)
	}
	winnerCommitted = true
	releaseRetry()

	select {
	case err = <-loserResult:
	case <-ctx.Done():
		t.Fatalf("loser did not finish after SQLite boundary release: %v", ctx.Err())
	}
	if !errors.Is(err, task.ErrNodeWriteConflict) {
		t.Fatalf("losing %s/%s/%s admission error=%v, want node-write conflict",
			testCase.phase, testCase.recoveryNode, testCase.winner, err)
	}
	assertNodeWriteDrillRecoveryBarrierState(
		t, db, sourceTask.ID, source.ID, sandbox.ID, runID, recoveryNodeID, testCase.phase, testCase.winner,
	)
}

func TestNodeWriteCoordinatorDrillStartRechecksBothNodesAndRollsBackPair(t *testing.T) {
	for _, leaseNode := range []string{"source", "sandbox"} {
		t.Run(leaseNode, func(t *testing.T) {
			db := openNodeWriteCoordinatorTestDB(t)
			source, sourceTask := seedNodeWriteCoordinatorTask(t, db, "start-source-"+leaseNode)
			sandbox, _ := seedNodeWriteCoordinatorTask(t, db, "start-sandbox-"+leaseNode)
			coordinator, err := NewNodeWriteCoordinator(db)
			if err != nil {
				t.Fatal(err)
			}
			if err := reserveNodeWriteDrill(context.Background(), db, coordinator, sourceTask, sandbox.ID); err != nil {
				t.Fatal(err)
			}
			leaseNodeID := source.ID
			if leaseNode == "sandbox" {
				leaseNodeID = sandbox.ID
			}
			// Simulate a legacy/out-of-band lease appearing between reservation and
			// execution. The start boundary must still fail closed.
			if err := db.Create(nodeWriteTestLease(leaseNodeID)).Error; err != nil {
				t.Fatal(err)
			}
			var run model.TaskRun
			if err := db.Where("task_id = ? AND trigger_type = ?", sourceTask.ID, "drill").Take(&run).Error; err != nil {
				t.Fatal(err)
			}

			err = db.Transaction(func(tx *gorm.DB) error {
				return coordinator.EnterDrillExecutionTx(context.Background(), tx, run.ID, sandbox.ID, time.Now().UTC())
			})
			if !errors.Is(err, task.ErrNodeWriteConflict) {
				t.Fatalf("%s lease start error=%v, want conflict", leaseNode, err)
			}
			assertNodeWriteDrillPairStatus(t, db, run.ID, model.TaskRunStatusPending)
		})
	}

	t.Run("caller rollback", func(t *testing.T) {
		db := openNodeWriteCoordinatorTestDB(t)
		_, sourceTask := seedNodeWriteCoordinatorTask(t, db, "start-rollback-source")
		sandbox, _ := seedNodeWriteCoordinatorTask(t, db, "start-rollback-sandbox")
		coordinator, err := NewNodeWriteCoordinator(db)
		if err != nil {
			t.Fatal(err)
		}
		if err := reserveNodeWriteDrill(context.Background(), db, coordinator, sourceTask, sandbox.ID); err != nil {
			t.Fatal(err)
		}
		var run model.TaskRun
		if err := db.Where("task_id = ? AND trigger_type = ?", sourceTask.ID, "drill").Take(&run).Error; err != nil {
			t.Fatal(err)
		}
		injected := errors.New("INTERNAL_DRILL_START_ROLLBACK_CANARY")
		err = db.Transaction(func(tx *gorm.DB) error {
			if err := coordinator.EnterDrillExecutionTx(context.Background(), tx, run.ID, sandbox.ID, time.Now().UTC()); err != nil {
				return err
			}
			if err := tx.Model(&model.RestoreDrillEvidence{}).Where("task_run_id = ?", run.ID).
				Update("status", model.TaskRunStatusRunning).Error; err != nil {
				return err
			}
			return injected
		})
		if !errors.Is(err, injected) {
			t.Fatalf("rollback transaction error=%v, want injected error", err)
		}
		assertNodeWriteDrillPairStatus(t, db, run.ID, model.TaskRunStatusPending)
	})

	t.Run("durable sandbox mismatch", func(t *testing.T) {
		db := openNodeWriteCoordinatorTestDB(t)
		_, sourceTask := seedNodeWriteCoordinatorTask(t, db, "start-mismatch-source")
		durableSandbox, _ := seedNodeWriteCoordinatorTask(t, db, "start-mismatch-durable-sandbox")
		wrongSandbox, _ := seedNodeWriteCoordinatorTask(t, db, "start-mismatch-wrong-sandbox")
		coordinator, err := NewNodeWriteCoordinator(db)
		if err != nil {
			t.Fatal(err)
		}
		if err := reserveNodeWriteDrill(context.Background(), db, coordinator, sourceTask, durableSandbox.ID); err != nil {
			t.Fatal(err)
		}
		var run model.TaskRun
		if err := db.Where("task_id = ? AND trigger_type = ?", sourceTask.ID, "drill").Take(&run).Error; err != nil {
			t.Fatal(err)
		}

		err = db.Transaction(func(tx *gorm.DB) error {
			return coordinator.EnterDrillExecutionTx(context.Background(), tx, run.ID, wrongSandbox.ID, time.Now().UTC())
		})
		if !errors.Is(err, task.ErrNodeWriteStartLost) {
			t.Fatalf("durable sandbox mismatch error=%v, want start-lost", err)
		}
		assertNodeWriteDrillPairStatus(t, db, run.ID, model.TaskRunStatusPending)
	})
}

func TestNodeWriteCoordinatorRecoveryAdmissionUsesImmutableRunNodeAfterTaskMigration(t *testing.T) {
	db := openNodeWriteCoordinatorTestDB(t)
	originalNode, taskEntity := seedNodeWriteCoordinatorTask(t, db, "snapshot-original")
	migratedNode, _ := seedNodeWriteCoordinatorTask(t, db, "snapshot-migrated")
	if err := db.Create(&model.TaskRun{
		TaskID: taskEntity.ID, TriggerType: "manual", Status: "running",
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Update("node_id", migratedNode.ID).Error; err != nil {
		t.Fatal(err)
	}
	coordinator, err := NewNodeWriteCoordinator(db)
	if err != nil {
		t.Fatal(err)
	}

	err = db.Transaction(func(tx *gorm.DB) error {
		return coordinator.AdmitRecoveryTx(context.Background(), tx, originalNode.ID)
	})
	if !errors.Is(err, task.ErrNodeWriteConflict) {
		t.Fatalf("old-node recovery admission error=%v, want live-writer conflict", err)
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		return coordinator.AdmitRecoveryTx(context.Background(), tx, migratedNode.ID)
	})
	if err != nil {
		t.Fatalf("new-node recovery admission inherited migrated Task writer: %v", err)
	}
}

func TestNodeWriteCoordinatorSameNodeConcurrentTaskAndRecoveryHaveOneDurableWinner(t *testing.T) {
	db := openNodeWriteCoordinatorTestDB(t)
	node, taskEntity := seedNodeWriteCoordinatorTask(t, db, "same-node")
	coordinator, err := NewNodeWriteCoordinator(db)
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() {
		<-start
		results <- reserveNodeWriteTask(ctx, db, coordinator, taskEntity)
	}()
	go func() {
		<-start
		results <- claimNodeWriteRecovery(ctx, db, coordinator, node.ID)
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
			t.Fatalf("concurrent admission returned unstable error: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent results successes=%d conflicts=%d, want 1/1", successes, conflicts)
	}
	var runCount, leaseCount int64
	if err := db.Model(&model.TaskRun{}).Where("status IN ?", []string{"pending", "running"}).Count(&runCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.BackupAssetRecoveryNodeLease{}).Where("state = ?", "active").Count(&leaseCount).Error; err != nil {
		t.Fatal(err)
	}
	if runCount+leaseCount != 1 {
		t.Fatalf("durable TaskRun/lease winners=%d/%d, want exactly one", runCount, leaseCount)
	}
}

func TestNodeWriteCoordinatorDifferentNodesDoNotConflict(t *testing.T) {
	db := openNodeWriteCoordinatorTestDB(t)
	nodeOne, taskOne := seedNodeWriteCoordinatorTask(t, db, "different-one")
	nodeTwo, _ := seedNodeWriteCoordinatorTask(t, db, "different-two")
	coordinator, err := NewNodeWriteCoordinator(db)
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() {
		<-start
		results <- reserveNodeWriteTask(ctx, db, coordinator, taskOne)
	}()
	go func() {
		<-start
		results <- claimNodeWriteRecovery(ctx, db, coordinator, nodeTwo.ID)
	}()
	close(start)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("different-node admission failed: %v", err)
		}
	}
	var runCount, leaseCount int64
	if err := db.Model(&model.TaskRun{}).Where("task_id = ?", taskOne.ID).Count(&runCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.BackupAssetRecoveryNodeLease{}).Where("node_id = ? AND state = ?", nodeTwo.ID, "active").Count(&leaseCount).Error; err != nil {
		t.Fatal(err)
	}
	if runCount != 1 || leaseCount != 1 {
		t.Fatalf("different-node reservations TaskRun/lease=%d/%d, want 1/1 (node one=%d)", runCount, leaseCount, nodeOne.ID)
	}
}

func TestNodeWriteCoordinatorCallerRollbackLeavesNoReservationOrLease(t *testing.T) {
	db := openNodeWriteCoordinatorTestDB(t)
	node, taskEntity := seedNodeWriteCoordinatorTask(t, db, "rollback")
	coordinator, err := NewNodeWriteCoordinator(db)
	if err != nil {
		t.Fatal(err)
	}

	taskTx := db.Begin()
	if taskTx.Error != nil {
		t.Fatal(taskTx.Error)
	}
	if err := coordinator.AdmitTaskTx(context.Background(), taskTx, node.ID); err != nil {
		t.Fatal(err)
	}
	if err := taskTx.Create(&model.TaskRun{TaskID: taskEntity.ID, TriggerType: "manual", Status: "pending"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := taskTx.Rollback().Error; err != nil {
		t.Fatal(err)
	}

	recoveryTx := db.Begin()
	if recoveryTx.Error != nil {
		t.Fatal(recoveryTx.Error)
	}
	if err := coordinator.AdmitRecoveryTx(context.Background(), recoveryTx, node.ID); err != nil {
		t.Fatal(err)
	}
	if err := recoveryTx.Create(nodeWriteTestLease(node.ID)).Error; err != nil {
		t.Fatal(err)
	}
	if err := recoveryTx.Rollback().Error; err != nil {
		t.Fatal(err)
	}

	var runCount, leaseCount int64
	if err := db.Model(&model.TaskRun{}).Count(&runCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.BackupAssetRecoveryNodeLease{}).Count(&leaseCount).Error; err != nil {
		t.Fatal(err)
	}
	if runCount != 0 || leaseCount != 0 {
		t.Fatalf("rollback residue TaskRun/lease=%d/%d, want 0/0", runCount, leaseCount)
	}
}

func TestNodeWriteCoordinatorSQLiteBusyRetriesAndTranslatesExhaustion(t *testing.T) {
	t.Run("retry succeeds after lock release", func(t *testing.T) {
		db := openNodeWriteCoordinatorTestDB(t)
		node, _ := seedNodeWriteCoordinatorTask(t, db, "busy-release")
		coordinator, err := NewNodeWriteCoordinator(db)
		if err != nil {
			t.Fatal(err)
		}
		locker := db.Begin()
		if locker.Error != nil {
			t.Fatal(locker.Error)
		}
		if err := locker.Exec("UPDATE nodes SET name = name WHERE id = ?", node.ID).Error; err != nil {
			t.Fatal(err)
		}
		var waits atomic.Int32
		coordinator.retryWait = func(ctx context.Context, _ int) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if waits.Add(1) == 1 {
				return locker.Commit().Error
			}
			return nil
		}
		contender := db.Begin()
		if contender.Error != nil {
			t.Fatal(contender.Error)
		}
		if err := coordinator.AdmitTaskTx(context.Background(), contender, node.ID); err != nil {
			_ = contender.Rollback().Error
			t.Fatalf("busy retry did not recover: %v", err)
		}
		if err := contender.Rollback().Error; err != nil {
			t.Fatal(err)
		}
		if waits.Load() == 0 {
			t.Fatal("SQLite lock did not exercise retry wait")
		}
	})

	t.Run("persistent lock is stable unavailable", func(t *testing.T) {
		db := openNodeWriteCoordinatorTestDB(t)
		node, _ := seedNodeWriteCoordinatorTask(t, db, "busy-exhaust")
		coordinator, err := NewNodeWriteCoordinator(db)
		if err != nil {
			t.Fatal(err)
		}
		coordinator.retryAttempts = 2
		coordinator.retryWait = func(ctx context.Context, _ int) error { return ctx.Err() }
		locker := db.Begin()
		if locker.Error != nil {
			t.Fatal(locker.Error)
		}
		if err := locker.Exec("UPDATE nodes SET name = name WHERE id = ?", node.ID).Error; err != nil {
			t.Fatal(err)
		}
		contender := db.Begin()
		if contender.Error != nil {
			t.Fatal(contender.Error)
		}
		err = coordinator.AdmitTaskTx(context.Background(), contender, node.ID)
		_ = contender.Rollback().Error
		_ = locker.Rollback().Error
		if !errors.Is(err, task.ErrNodeWriteUnavailable) {
			t.Fatalf("persistent busy error=%v, want stable unavailable", err)
		}
		message := strings.ToLower(err.Error())
		if strings.Contains(message, "database is locked") || strings.Contains(message, "database table is locked") {
			t.Fatalf("raw SQLite lock leaked through stable error: %v", err)
		}
	})
}

func TestNodeWriteCoordinatorPostgresLocksSharedNodeRowForUpdate(t *testing.T) {
	db, err := gorm.Open(postgres.Open("host=127.0.0.1 user=xirang dbname=xirang sslmode=disable"), &gorm.Config{
		DryRun: true, DisableAutomaticPing: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var statements []string
	if err := db.Callback().Query().After("gorm:query").Register("test:capture-node-write-sql", func(tx *gorm.DB) {
		statements = append(statements, tx.Statement.SQL.String())
	}); err != nil {
		t.Fatal(err)
	}
	coordinator, err := NewNodeWriteCoordinator(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.AdmitTaskTx(context.Background(), db, 42); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(statements, "\n")
	if !strings.Contains(joined, `FROM "nodes"`) || !strings.Contains(joined, "FOR UPDATE") {
		t.Fatalf("PostgreSQL admission omitted shared nodes row FOR UPDATE:\n%s", joined)
	}
}

type nodeWriteSQLiteBoundaryBarrierPool struct {
	*sql.DB
	entered chan struct{}
	release <-chan struct{}
	match   func(string) bool
	once    sync.Once
}

func (pool *nodeWriteSQLiteBoundaryBarrierPool) BeginTx(
	ctx context.Context,
	options *sql.TxOptions,
) (gorm.ConnPool, error) {
	tx, err := pool.DB.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return &nodeWriteSQLiteBoundaryBarrierTx{Tx: tx, pool: pool}, nil
}

type nodeWriteSQLiteBoundaryBarrierTx struct {
	*sql.Tx
	pool *nodeWriteSQLiteBoundaryBarrierPool
}

func (tx *nodeWriteSQLiteBoundaryBarrierTx) ExecContext(
	ctx context.Context,
	query string,
	args ...any,
) (sql.Result, error) {
	if err := tx.pool.wait(ctx, query); err != nil {
		return nil, err
	}
	return tx.Tx.ExecContext(ctx, query, args...)
}

func (tx *nodeWriteSQLiteBoundaryBarrierTx) QueryContext(
	ctx context.Context,
	query string,
	args ...any,
) (*sql.Rows, error) {
	if err := tx.pool.wait(ctx, query); err != nil {
		return nil, err
	}
	return tx.Tx.QueryContext(ctx, query, args...)
}

func (pool *nodeWriteSQLiteBoundaryBarrierPool) wait(ctx context.Context, query string) error {
	if pool.match == nil || !pool.match(query) {
		return nil
	}
	var waitErr error
	pool.once.Do(func() {
		close(pool.entered)
		select {
		case <-pool.release:
		case <-ctx.Done():
			waitErr = ctx.Err()
		}
	})
	return waitErr
}

func openNodeWriteSQLiteBoundaryBarrierDB(
	t *testing.T,
	db *gorm.DB,
	entered chan struct{},
	release <-chan struct{},
	match func(string) bool,
) *gorm.DB {
	t.Helper()
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	pool := &nodeWriteSQLiteBoundaryBarrierPool{
		DB: sqlDB, entered: entered, release: release, match: match,
	}
	barrierDB, err := gorm.Open(sqlite.New(sqlite.Config{Conn: pool}), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open SQLite boundary barrier DB: %v", err)
	}
	return barrierDB
}

func nodeWriteSQLiteLoserBoundaryMatch(testCase nodeWriteDrillRecoveryBarrierCase) func(string) bool {
	return func(query string) bool {
		normalized := strings.ToLower(strings.Join(strings.Fields(query), " "))
		if testCase.phase == "start" && testCase.winner == "recovery" {
			// The Drill start reads its immutable pair before locking either node.
			// Pause at that first start boundary; after release, retryWait still has
			// to prove the same transaction reached and contended on the node write.
			return strings.Contains(normalized, "select") &&
				strings.Contains(normalized, "task_runs") &&
				strings.Contains(normalized, "node_id_snapshot") &&
				strings.Contains(normalized, "trigger_type")
		}
		return strings.Contains(normalized, "update nodes set id = id where id = ?")
	}
}

func openNodeWriteCoordinatorTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:node-write-%d?mode=memory&cache=shared&_busy_timeout=1&_loc=UTC", nodeWriteTestDBSequence.Add(1))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(8)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(
		&model.SSHKey{}, &model.Node{}, &model.Policy{}, &model.PolicyNode{}, &model.Task{}, &model.TaskRun{},
		&model.RestoreDrillEvidence{}, &model.BackupAssetRecoveryNodeLease{},
	); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE UNIQUE INDEX idx_node_write_test_active_lease
		ON backup_asset_recovery_node_leases(node_id) WHERE state = 'active'`).Error; err != nil {
		t.Fatal(err)
	}
	return db
}

func seedNodeWriteCoordinatorTask(t *testing.T, db *gorm.DB, suffix string) (model.Node, model.Task) {
	t.Helper()
	node := model.Node{
		Name: "node-write-" + suffix, Host: "127.0.0.1", Port: 22, Username: "root", AuthType: "key",
		BasePath: "/tmp/node-write-base-" + suffix, BackupDir: "/tmp/node-write-backup-" + suffix,
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	taskEntity := model.Task{
		Name: "task-write-" + suffix, NodeID: node.ID, ExecutorType: "rsync", Status: "pending", Enabled: true,
		RsyncSource: "/tmp/source", RsyncTarget: "/tmp/target",
	}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatal(err)
	}
	return node, taskEntity
}

func reserveNodeWriteTask(ctx context.Context, db *gorm.DB, coordinator *NodeWriteCoordinator, taskEntity model.Task) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := coordinator.AdmitTaskTx(ctx, tx, taskEntity.NodeID); err != nil {
			return err
		}
		return tx.Create(&model.TaskRun{TaskID: taskEntity.ID, TriggerType: "manual", Status: "pending"}).Error
	})
}

func reserveNodeWriteDrill(
	ctx context.Context,
	db *gorm.DB,
	coordinator *NodeWriteCoordinator,
	taskEntity model.Task,
	sandboxNodeID uint,
) error {
	_, err := reserveNodeWriteDrillPair(ctx, db, coordinator, taskEntity, sandboxNodeID)
	return err
}

func reserveNodeWriteDrillPair(
	ctx context.Context,
	db *gorm.DB,
	coordinator *NodeWriteCoordinator,
	taskEntity model.Task,
	sandboxNodeID uint,
) (uint, error) {
	var runID uint
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		runID, err = reserveNodeWriteDrillPairTx(ctx, tx, coordinator, taskEntity, sandboxNodeID)
		return err
	})
	return runID, err
}

func reserveNodeWriteDrillPairTx(
	ctx context.Context,
	tx *gorm.DB,
	coordinator *NodeWriteCoordinator,
	taskEntity model.Task,
	sandboxNodeID uint,
) (uint, error) {
	if err := coordinator.AdmitDrillTx(ctx, tx, taskEntity.NodeID, sandboxNodeID); err != nil {
		return 0, err
	}
	run := model.TaskRun{TaskID: taskEntity.ID, TriggerType: "drill", Status: model.TaskRunStatusPending}
	if err := tx.WithContext(ctx).Create(&run).Error; err != nil {
		return 0, err
	}
	err := tx.WithContext(ctx).Create(&model.RestoreDrillEvidence{
		PolicyID: 1, TaskID: taskEntity.ID, TaskRunID: run.ID,
		SandboxNodeID: sandboxNodeID, SandboxPath: "/tmp/node-write-drill-sandbox",
		Status: model.TaskRunStatusPending,
	}).Error
	return run.ID, err
}

func startNodeWriteDrillPairTx(
	ctx context.Context,
	tx *gorm.DB,
	coordinator *NodeWriteCoordinator,
	runID uint,
	sandboxNodeID uint,
	startedAt time.Time,
) error {
	if err := coordinator.EnterDrillExecutionTx(ctx, tx, runID, sandboxNodeID, startedAt); err != nil {
		return err
	}
	result := tx.WithContext(ctx).Model(&model.RestoreDrillEvidence{}).
		Where("task_run_id = ? AND status = ?", runID, model.TaskRunStatusPending).
		Updates(map[string]interface{}{"status": model.TaskRunStatusRunning, "started_at": &startedAt})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return task.ErrNodeWriteStartLost
	}
	return nil
}

func assertNodeWriteDrillRecoveryWinner(t *testing.T, db *gorm.DB, wantRecovery bool) {
	t.Helper()
	var runCount, evidenceCount, leaseCount int64
	if err := db.Model(&model.TaskRun{}).Where("trigger_type = ? AND status IN ?", "drill", model.TaskRunActiveStatuses()).Count(&runCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.RestoreDrillEvidence{}).Where("status IN ?", model.TaskRunActiveStatuses()).Count(&evidenceCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.BackupAssetRecoveryNodeLease{}).Where("state = ?", "active").Count(&leaseCount).Error; err != nil {
		t.Fatal(err)
	}
	if wantRecovery {
		if runCount != 0 || evidenceCount != 0 || leaseCount != 1 {
			t.Fatalf("Recovery-first durable rows run/evidence/lease=%d/%d/%d, want 0/0/1", runCount, evidenceCount, leaseCount)
		}
		return
	}
	if runCount != 1 || evidenceCount != 1 || leaseCount != 0 {
		t.Fatalf("Drill-first durable rows run/evidence/lease=%d/%d/%d, want 1/1/0", runCount, evidenceCount, leaseCount)
	}
}

func assertNodeWriteDrillPairStatus(t *testing.T, db *gorm.DB, runID uint, want string) {
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
		t.Fatalf("drill pair status TaskRun=%q Evidence=%q, want %q/%q", run.Status, evidence.Status, want, want)
	}
}

func assertNodeWriteDrillRecoveryBarrierState(
	t *testing.T,
	db *gorm.DB,
	taskID uint,
	sourceNodeID uint,
	sandboxNodeID uint,
	runID uint,
	recoveryNodeID uint,
	phase string,
	winner string,
) {
	t.Helper()
	var runs []model.TaskRun
	if err := db.Where("task_id = ? AND trigger_type = ?", taskID, "drill").Find(&runs).Error; err != nil {
		t.Fatal(err)
	}
	var evidences []model.RestoreDrillEvidence
	if err := db.Where("task_id = ?", taskID).Find(&evidences).Error; err != nil {
		t.Fatal(err)
	}
	var leases []model.BackupAssetRecoveryNodeLease
	if err := db.Where("node_id = ? AND state = ?", recoveryNodeID, "active").Find(&leases).Error; err != nil {
		t.Fatal(err)
	}

	// A legacy/out-of-band Recovery winner at start does not delete the already
	// reserved pair: exact fail-closed state is one pending pair plus one lease.
	wantPair := phase == "start" || winner == "drill"
	wantLease := winner == "recovery"
	if len(runs) != boolCount(wantPair) || len(evidences) != boolCount(wantPair) || len(leases) != boolCount(wantLease) {
		t.Fatalf("durable matrix state TaskRun/Evidence/lease=%d/%d/%d, want %d/%d/%d",
			len(runs), len(evidences), len(leases),
			boolCount(wantPair), boolCount(wantPair), boolCount(wantLease))
	}
	if !wantPair {
		return
	}
	if runs[0].ID != runID || evidences[0].TaskRunID != runID {
		t.Fatalf("split Drill pair TaskRun ID/Evidence TaskRun ID=%d/%d, want %d/%d",
			runs[0].ID, evidences[0].TaskRunID, runID, runID)
	}
	if runs[0].NodeIDSnapshot != sourceNodeID || evidences[0].SandboxNodeID != sandboxNodeID {
		t.Fatalf("Drill pair node snapshots source/sandbox=%d/%d, want %d/%d",
			runs[0].NodeIDSnapshot, evidences[0].SandboxNodeID, sourceNodeID, sandboxNodeID)
	}
	wantStatus := model.TaskRunStatusPending
	if phase == "start" && winner == "drill" {
		wantStatus = model.TaskRunStatusRunning
	}
	if runs[0].Status != wantStatus || evidences[0].Status != wantStatus {
		t.Fatalf("Drill pair status TaskRun/Evidence=%q/%q, want %q/%q",
			runs[0].Status, evidences[0].Status, wantStatus, wantStatus)
	}
	if wantStatus == model.TaskRunStatusRunning {
		if runs[0].StartedAt == nil || evidences[0].StartedAt == nil {
			t.Fatalf("running Drill pair missing started_at TaskRun/Evidence=%v/%v",
				runs[0].StartedAt, evidences[0].StartedAt)
		}
		if !runs[0].StartedAt.Equal(*evidences[0].StartedAt) {
			t.Fatalf("running Drill pair started_at differs TaskRun/Evidence=%s/%s",
				runs[0].StartedAt.UTC(), evidences[0].StartedAt.UTC())
		}
		return
	}
	if runs[0].StartedAt != nil || evidences[0].StartedAt != nil {
		t.Fatalf("pending Drill pair has started_at TaskRun/Evidence=%v/%v",
			runs[0].StartedAt, evidences[0].StartedAt)
	}
}

func boolCount(value bool) int {
	if value {
		return 1
	}
	return 0
}

func claimNodeWriteRecovery(ctx context.Context, db *gorm.DB, coordinator *NodeWriteCoordinator, nodeID uint) error {
	return claimNodeWriteRecoveryLease(ctx, db, coordinator, nodeWriteTestLease(nodeID))
}

func claimNodeWriteRecoveryLease(
	ctx context.Context,
	db *gorm.DB,
	coordinator *NodeWriteCoordinator,
	lease *model.BackupAssetRecoveryNodeLease,
) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return claimNodeWriteRecoveryLeaseTx(ctx, tx, coordinator, lease)
	})
}

func claimNodeWriteRecoveryLeaseTx(
	ctx context.Context,
	tx *gorm.DB,
	coordinator *NodeWriteCoordinator,
	lease *model.BackupAssetRecoveryNodeLease,
) error {
	if err := coordinator.AdmitRecoveryTx(ctx, tx, lease.NodeID); err != nil {
		return err
	}
	return tx.WithContext(ctx).Create(lease).Error
}

func insertNodeWriteLegacyRecoveryLeaseTx(
	ctx context.Context,
	tx *gorm.DB,
	lease *model.BackupAssetRecoveryNodeLease,
) error {
	if err := lockNodeBoundaryOnce(ctx, tx, lease.NodeID); err != nil {
		return err
	}
	return tx.WithContext(ctx).Create(lease).Error
}

func nodeWriteTestLease(nodeID uint) *model.BackupAssetRecoveryNodeLease {
	now := time.Now().UTC()
	return nodeWriteTestLeaseAt(nodeID, now, now.Add(time.Hour))
}

func nodeWriteTestLeaseAt(nodeID uint, createdAt, expiresAt time.Time) *model.BackupAssetRecoveryNodeLease {
	sequence := nodeWriteLeaseSequence.Add(1)
	return &model.BackupAssetRecoveryNodeLease{
		ID: fmt.Sprintf("%032x", sequence), NodeID: nodeID, HolderKind: "recovery_cleanup",
		JobID: fmt.Sprintf("%032x", sequence+1_000_000), OwnerID: "node-write-test", Fence: sequence,
		State: "active", LeaseExpiresAt: expiresAt, CreatedAt: createdAt, UpdatedAt: createdAt,
	}
}

func newNodeWriteCoordinatorAt(t *testing.T, db *gorm.DB, now time.Time) *NodeWriteCoordinator {
	t.Helper()
	coordinator, err := NewNodeWriteCoordinator(db)
	if err != nil {
		t.Fatal(err)
	}
	coordinator.now = func() time.Time { return now }
	return coordinator
}

func assertNodeWriteLeaseState(
	t *testing.T,
	db *gorm.DB,
	leaseID string,
	wantState string,
	wantReleasedAt *time.Time,
) {
	t.Helper()
	var lease model.BackupAssetRecoveryNodeLease
	if err := db.Where("id = ?", leaseID).Take(&lease).Error; err != nil {
		t.Fatal(err)
	}
	if lease.State != wantState {
		t.Fatalf("lease %s state=%q, want %q", leaseID, lease.State, wantState)
	}
	if wantReleasedAt == nil {
		if lease.ReleasedAt != nil {
			t.Fatalf("lease %s released_at=%s, want nil", leaseID, lease.ReleasedAt)
		}
		return
	}
	if lease.ReleasedAt == nil || !lease.ReleasedAt.Equal(*wantReleasedAt) {
		t.Fatalf("lease %s released_at=%v, want %s", leaseID, lease.ReleasedAt, wantReleasedAt.UTC())
	}
	if !lease.UpdatedAt.Equal(*wantReleasedAt) {
		t.Fatalf("lease %s updated_at=%s, want %s", leaseID, lease.UpdatedAt, wantReleasedAt.UTC())
	}
}

var _ task.NodeWriteAdmission = (*NodeWriteCoordinator)(nil)
