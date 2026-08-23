package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
		&model.SSHKey{}, &model.Node{}, &model.Policy{}, &model.Task{}, &model.TaskRun{},
		&model.BackupAssetRecoveryNodeLease{},
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
		if err := coordinator.AdmitRecoveryTx(ctx, tx, lease.NodeID); err != nil {
			return err
		}
		return tx.Create(lease).Error
	})
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
