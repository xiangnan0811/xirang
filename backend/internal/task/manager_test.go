package task

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/alerting"
	"xirang/backend/internal/anomaly"
	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/credentialaudit"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"
	"xirang/backend/internal/sshutil"
	taskexec "xirang/backend/internal/task/executor"

	"github.com/mattn/go-sqlite3"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type stubExecutorFactory struct {
	executor taskexec.Executor
}

func (f stubExecutorFactory) Resolve(_ string) taskexec.Executor {
	return f.executor
}

type nodeWriteAdmissionFake struct {
	mu                  sync.Mutex
	calls               int
	nodeIDs             []uint
	taskRunCountsAtCall []int64
	errs                []error
	admitEntered        chan struct{}
	admitRelease        <-chan struct{}
	admitEnteredOnce    sync.Once
	startCalls          int
	startRunIDs         []uint
	startNodeIDs        []uint
	startErrs           []error
	startEntered        chan struct{}
	startRelease        <-chan struct{}
	startEnteredOnce    sync.Once
}

func (admission *nodeWriteAdmissionFake) AdmitTaskTx(ctx context.Context, tx *gorm.DB, nodeID uint) error {
	admission.mu.Lock()
	var taskRunCount int64
	if err := tx.Model(&model.TaskRun{}).Count(&taskRunCount).Error; err != nil {
		admission.mu.Unlock()
		return err
	}
	admission.calls++
	admission.nodeIDs = append(admission.nodeIDs, nodeID)
	admission.taskRunCountsAtCall = append(admission.taskRunCountsAtCall, taskRunCount)
	var admitErr error
	if len(admission.errs) > 0 {
		admitErr = admission.errs[0]
		admission.errs = admission.errs[1:]
	}
	admitEntered := admission.admitEntered
	admitRelease := admission.admitRelease
	admission.mu.Unlock()
	if admitEntered != nil {
		admission.admitEnteredOnce.Do(func() { close(admitEntered) })
	}
	if admitRelease != nil {
		<-admitRelease
	}
	if admitErr != nil {
		return admitErr
	}
	return ctx.Err()
}

func (admission *nodeWriteAdmissionFake) snapshot() (int, []uint, []int64) {
	admission.mu.Lock()
	defer admission.mu.Unlock()
	return admission.calls, append([]uint(nil), admission.nodeIDs...), append([]int64(nil), admission.taskRunCountsAtCall...)
}

func (admission *nodeWriteAdmissionFake) EnterTaskExecutionTx(
	ctx context.Context,
	tx *gorm.DB,
	runID uint,
	nodeID uint,
	startedAt time.Time,
) error {
	admission.mu.Lock()
	admission.startCalls++
	admission.startRunIDs = append(admission.startRunIDs, runID)
	admission.startNodeIDs = append(admission.startNodeIDs, nodeID)
	var startErr error
	if len(admission.startErrs) > 0 {
		startErr = admission.startErrs[0]
		admission.startErrs = admission.startErrs[1:]
	}
	startEntered := admission.startEntered
	startRelease := admission.startRelease
	admission.mu.Unlock()
	if startErr != nil {
		return startErr
	}
	if startEntered != nil {
		admission.startEnteredOnce.Do(func() { close(startEntered) })
	}
	if startRelease != nil {
		<-startRelease
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	result := tx.Model(&model.TaskRun{}).
		Where("id = ? AND node_id_snapshot = ? AND status = ?", runID, nodeID, "pending").
		Updates(map[string]interface{}{"status": "running", "started_at": &startedAt})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrNodeWriteStartLost
	}
	return nil
}

type taskEntryCommitBarrierPool struct {
	*sql.DB
	armed      atomic.Bool
	commitOnce sync.Once
	committed  chan struct{}
	release    <-chan struct{}
}

func (pool *taskEntryCommitBarrierPool) BeginTx(ctx context.Context, options *sql.TxOptions) (gorm.ConnPool, error) {
	tx, err := pool.DB.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return &taskEntryCommitBarrierTx{Tx: tx, pool: pool}, nil
}

type taskEntryCommitBarrierTx struct {
	*sql.Tx
	pool *taskEntryCommitBarrierPool
}

func (tx *taskEntryCommitBarrierTx) Commit() error {
	err := tx.Tx.Commit()
	if err == nil && tx.pool.armed.CompareAndSwap(true, false) {
		tx.pool.commitOnce.Do(func() { close(tx.pool.committed) })
		<-tx.pool.release
	}
	return err
}

// taskEntryDeadlineContext lets the no-executor timeout boundary be released
// only after the executor-entry transaction has durably committed.
type taskEntryDeadlineContext struct {
	done chan struct{}
	once sync.Once
}

func newTaskEntryDeadlineContext() *taskEntryDeadlineContext {
	return &taskEntryDeadlineContext{done: make(chan struct{})}
}

func (ctx *taskEntryDeadlineContext) Deadline() (time.Time, bool) {
	return time.Time{}, false
}

func (ctx *taskEntryDeadlineContext) Done() <-chan struct{} {
	return ctx.done
}

func (ctx *taskEntryDeadlineContext) Err() error {
	select {
	case <-ctx.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}

func (*taskEntryDeadlineContext) Value(any) any {
	return nil
}

func (ctx *taskEntryDeadlineContext) Expire() {
	ctx.once.Do(func() { close(ctx.done) })
}

func (admission *nodeWriteAdmissionFake) startSnapshot() (int, []uint, []uint) {
	admission.mu.Lock()
	defer admission.mu.Unlock()
	return admission.startCalls, append([]uint(nil), admission.startRunIDs...), append([]uint(nil), admission.startNodeIDs...)
}

type successExecutor struct {
	calls int32
}

func (e *successExecutor) Run(_ context.Context, _ model.Task, _ taskexec.LogFunc, _ taskexec.ProgressFunc) (int, error) {
	atomic.AddInt32(&e.calls, 1)
	return 0, nil
}

func (e *successExecutor) Calls() int {
	return int(atomic.LoadInt32(&e.calls))
}

type blockingExecutor struct {
	calls    int32
	started  chan struct{}
	release  chan struct{}
	startMux sync.Once
}

func newBlockingExecutor() *blockingExecutor {
	return &blockingExecutor{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (e *blockingExecutor) Run(ctx context.Context, _ model.Task, _ taskexec.LogFunc, _ taskexec.ProgressFunc) (int, error) {
	atomic.AddInt32(&e.calls, 1)
	e.startMux.Do(func() {
		close(e.started)
	})

	select {
	case <-e.release:
		return 0, nil
	case <-ctx.Done():
		return -1, ctx.Err()
	}
}

type sampleExecutor struct {
	samples []taskexec.ProgressSample
	called  int32
}

type contextAuditExecutor struct{}

func (e *contextAuditExecutor) Run(ctx context.Context, task model.Task, _ taskexec.LogFunc, _ taskexec.ProgressFunc) (int, error) {
	return 0, credentialaudit.WriteRuntime(ctx, credentialaudit.Event{
		Action:           "task.credential.use",
		Purpose:          sshutil.PurposeTaskBackup,
		CredentialKind:   "ssh_key",
		CredentialSource: "ssh_key_id=42",
		SSHKeyID:         credentialaudit.PtrUint(42),
		NodeID:           credentialaudit.PtrUint(task.NodeID),
		Outcome:          credentialaudit.OutcomeSuccess,
		Metadata: map[string]any{
			"stage":   "test_boundary",
			"command": "FAKE_COMMAND_FOR_TEST_ONLY",
		},
	})
}

func (e *sampleExecutor) Run(_ context.Context, _ model.Task, _ taskexec.LogFunc, progressf taskexec.ProgressFunc) (int, error) {
	atomic.AddInt32(&e.called, 1)
	for _, sample := range e.samples {
		progressf(sample)
	}
	return 0, nil
}

func (e *blockingExecutor) Calls() int {
	return int(atomic.LoadInt32(&e.calls))
}

func openManagerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	t.Setenv("DATA_ENCRYPTION_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
	// 关键：不用 cache=shared + 命名 file，原实现导致两个 flake：
	//   1) Manager 的后台 goroutine 与测试主线程并发写同一内存库 →
	//      SQLite 单写者锁默认立即返回 "database table is locked"，
	//      CI 上观察到 TestPreHookTimeout 偶发断言失败。
	//   2) 同一进程内 go test -count=N 重复跑同名测试时，命名 file 复用
	//      同一份内存库，残留数据触发 UNIQUE constraint。
	// 改用纯 ":memory:" + SetMaxOpenConns(1)：每次调用得到全新的私有库，
	// 单连接彻底串行化所有写入；_busy_timeout 作为兜底应对偶发竞争。
	db, err := gorm.Open(sqlite.Open("file::memory:?_busy_timeout=5000&_loc=UTC"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("获取底层连接失败: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&model.SSHKey{}, &model.Node{}, &model.Policy{}, &model.Task{}, &model.TaskRun{}, &model.RestoreDrillEvidence{}, &model.CredentialAuditEvent{}, &model.TaskLog{}, &model.Alert{}, &model.Integration{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}
	if err := db.AutoMigrate(&model.TaskTrafficSample{}); err != nil {
		t.Fatalf("初始化采样表失败: %v", err)
	}
	return db
}

func openConcurrentManagerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	t.Setenv("DATA_ENCRYPTION_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
	dsn := fmt.Sprintf("file:%s/manager.db?_busy_timeout=5000&_loc=UTC", t.TempDir())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开并发测试数据库失败: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("获取并发测试底层连接失败: %v", err)
	}
	sqlDB.SetMaxOpenConns(4)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&model.SSHKey{}, &model.Node{}, &model.Policy{}, &model.Task{}, &model.TaskRun{}, &model.RestoreDrillEvidence{}, &model.CredentialAuditEvent{}, &model.TaskLog{}, &model.Alert{}, &model.Integration{}, &model.TaskTrafficSample{}); err != nil {
		t.Fatalf("初始化并发测试数据表失败: %v", err)
	}
	return db
}

func createTestTaskRun(t *testing.T, db *gorm.DB, taskID uint, reason string) uint {
	t.Helper()
	run := model.TaskRun{
		TaskID:      taskID,
		TriggerType: reason,
		Status:      "pending",
	}
	if err := db.Create(&run).Error; err != nil {
		t.Fatalf("创建测试执行记录失败: %v", err)
	}
	return run.ID
}

func seedTaskForManagerTest(t *testing.T, db *gorm.DB) model.Task {
	t.Helper()
	node := model.Node{
		Name:     "node-manager-test",
		Host:     "127.0.0.1",
		Port:     22,
		Username: "root",
		AuthType: "key",
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}

	taskEntity := model.Task{
		Name:         "task-manager-test",
		NodeID:       node.ID,
		ExecutorType: "rsync",
		Status:       string(StatusPending),
		RsyncSource:  "/tmp/src",
		RsyncTarget:  "/tmp/dst",
	}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	return taskEntity
}

func TestRunTaskCleansUpLockEntries(t *testing.T) {
	db := openManagerTestDB(t)
	exec := &successExecutor{}
	m := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, nil, 8, 90)

	taskEntity := seedTaskForManagerTest(t, db)
	runID := createTestTaskRun(t, db, taskEntity.ID, "manual")
	m.runTask(taskEntity.ID, runID, "manual", generateChainRunID())

	if exec.Calls() != 1 {
		t.Fatalf("期望执行器调用 1 次，实际: %d", exec.Calls())
	}

	// 任务执行完毕后，taskID 级别的锁应被清理以防止 sync.Map 无限增长
	if _, ok := m.locks.Load(taskEntity.ID); ok {
		t.Fatalf("期望任务锁条目已清理，实际仍保留")
	}
	// strategyLocks 和 nodeLocks 按 nodeID/policyID 存储，数量有上界，无需清理
	strategyKey := buildStrategyKey(taskEntity.NodeID, taskEntity.PolicyID)
	if _, ok := m.strategyLocks.Load(strategyKey); !ok {
		t.Fatalf("期望策略锁条目保留，实际已删除")
	}
}

func TestTriggerRegistersCancelOwnerBeforeReturning(t *testing.T) {
	t.Run("ordinary", func(t *testing.T) {
		db := openConcurrentManagerTestDB(t)
		exec := newBlockingExecutor()
		manager := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, nil, 8, 90)
		taskEntity := seedTaskForManagerTest(t, db)

		previousProcs := runtime.GOMAXPROCS(1)
		runID, triggerErr := manager.TriggerManual(taskEntity.ID)
		_, registered := manager.chainRunner.Load(taskEntity.ID)
		runtime.GOMAXPROCS(previousProcs)

		if triggerErr != nil {
			t.Fatalf("trigger ordinary task: %v", triggerErr)
		}
		if cancelErr := manager.Cancel(taskEntity.ID); cancelErr != nil {
			t.Fatalf("cancel ordinary task after trigger: %v", cancelErr)
		}
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer shutdownCancel()
		if err := manager.Shutdown(shutdownCtx); err != nil {
			t.Fatalf("shutdown ordinary task manager: %v", err)
		}
		if runID == 0 {
			t.Fatal("ordinary trigger returned zero TaskRun ID")
		}
		if !registered {
			t.Fatal("ordinary TriggerManual returned before its cancel owner was registered")
		}
	})

	t.Run("legacy_restore", func(t *testing.T) {
		db := openConcurrentManagerTestDB(t)
		restoreExecutor := &trackingRestoreExecutor{}
		manager := NewManager(db, stubExecutorFactory{executor: restoreExecutor}, nil, nil, nil, nil, 8, 90)
		taskEntity := seedTaskForManagerTest(t, db)
		if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).
			Update("status", string(StatusSuccess)).Error; err != nil {
			t.Fatal(err)
		}
		taskEntity.Status = string(StatusSuccess)
		if err := db.Create(&model.TaskRun{
			TaskID: taskEntity.ID, TriggerType: "manual", Status: "success",
		}).Error; err != nil {
			t.Fatal(err)
		}

		previousProcs := runtime.GOMAXPROCS(1)
		runID, triggerErr := manager.TriggerRestore(taskEntity.ID, "/tmp/restore-test")
		_, registered := manager.chainRunner.Load(taskEntity.ID)
		runtime.GOMAXPROCS(previousProcs)

		if triggerErr != nil {
			t.Fatalf("trigger legacy restore: %v", triggerErr)
		}
		cancelErr := manager.Cancel(taskEntity.ID)
		if cancelErr != nil && !registered {
			deadline := time.After(3 * time.Second)
			for {
				if _, ok := manager.chainRunner.Load(taskEntity.ID); ok {
					cancelErr = manager.Cancel(taskEntity.ID)
					break
				}
				select {
				case <-deadline:
					t.Fatal("legacy restore runner never registered its cancel owner for cleanup")
				case <-time.After(time.Millisecond):
				}
			}
		}
		if cancelErr != nil {
			t.Fatalf("cancel legacy restore after trigger: %v", cancelErr)
		}
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer shutdownCancel()
		if err := manager.Shutdown(shutdownCtx); err != nil {
			t.Fatalf("shutdown legacy restore manager: %v", err)
		}
		if runID == 0 {
			t.Fatal("legacy restore trigger returned zero TaskRun ID")
		}
		if !registered {
			t.Fatal("legacy TriggerRestore returned before its cancel owner was registered")
		}
	})
}

func TestCancelAtPublicTriggerOwnerRegistrationPreventsScheduling(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		restore bool
	}{
		{name: "ordinary"},
		{name: "legacy_restore", restore: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db := openConcurrentManagerTestDB(t)
			ordinaryExecutor := &successExecutor{}
			restoreExecutor := &trackingRestoreExecutor{}
			factoryExecutor := taskexec.Executor(ordinaryExecutor)
			if testCase.restore {
				factoryExecutor = restoreExecutor
			}

			contextFactoryEntered := make(chan struct{})
			contextFactoryRelease := make(chan struct{})
			var factoryOnce sync.Once
			manager := NewManager(
				db,
				stubExecutorFactory{executor: factoryExecutor},
				nil,
				nil,
				nil,
				nil,
				8,
				90,
				WithRunContextFactory(func(parent context.Context, _ time.Duration) (context.Context, context.CancelFunc) {
					factoryOnce.Do(func() { close(contextFactoryEntered) })
					<-contextFactoryRelease
					return context.WithCancel(parent)
				}),
			)
			taskEntity := seedTaskForManagerTest(t, db)
			if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).
				Update("status", string(StatusSuccess)).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&model.TaskRun{
				TaskID: taskEntity.ID, TriggerType: "manual", Status: "success",
			}).Error; err != nil {
				t.Fatal(err)
			}
			admission := &nodeWriteAdmissionFake{}
			manager.SetNodeWriteAdmission(admission)
			var precheckCalls atomic.Int32
			manager.ensureRemoteTargetReadyFunc = func(context.Context, model.Node, string) error {
				precheckCalls.Add(1)
				return nil
			}
			for range cap(manager.semaphore) {
				manager.semaphore <- struct{}{}
			}

			type triggerResult struct {
				runID uint
				err   error
			}
			resultCh := make(chan triggerResult, 1)
			go func() {
				var result triggerResult
				if testCase.restore {
					result.runID, result.err = manager.TriggerRestore(taskEntity.ID, "/tmp/public-owner-registration")
				} else {
					result.runID, result.err = manager.TriggerManual(taskEntity.ID)
				}
				resultCh <- result
			}()

			select {
			case <-contextFactoryEntered:
			case <-time.After(3 * time.Second):
				t.Fatal("public trigger did not reach the owner-registration boundary")
			}
			cancelErr := manager.Cancel(taskEntity.ID)
			close(contextFactoryRelease)
			var result triggerResult
			select {
			case result = <-resultCh:
			case <-time.After(3 * time.Second):
				t.Fatal("public trigger did not return after releasing owner registration")
			}

			if result.err == nil {
				_ = manager.Cancel(taskEntity.ID)
			}
			for range cap(manager.semaphore) {
				<-manager.semaphore
			}
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer shutdownCancel()
			shutdownErr := manager.Shutdown(shutdownCtx)

			if cancelErr != nil {
				t.Fatalf("Cancel at public owner registration: %v", cancelErr)
			}
			if !errors.Is(result.err, context.Canceled) || result.runID != 0 {
				t.Fatalf("canceled public trigger result run=%d error=%v, want zero/context canceled", result.runID, result.err)
			}
			if shutdownErr != nil {
				t.Fatalf("shutdown manager: %v", shutdownErr)
			}
			calls, _, _ := admission.snapshot()
			if calls != 0 {
				t.Fatalf("canceled public trigger reached durable admission %d time(s)", calls)
			}
			if ordinaryExecutor.Calls() != 0 {
				t.Fatalf("canceled public ordinary trigger reached executor %d time(s)", ordinaryExecutor.Calls())
			}
			if got := precheckCalls.Load(); got != 0 {
				t.Fatalf("canceled public legacy restore reached precheck %d time(s)", got)
			}
			if got := atomic.LoadInt32(&restoreExecutor.calls); got != 0 {
				t.Fatalf("canceled public legacy restore reached executor %d time(s)", got)
			}
			var activeRuns int64
			if err := db.Model(&model.TaskRun{}).
				Where("task_id = ? AND status IN ?", taskEntity.ID, []string{"pending", "running"}).
				Count(&activeRuns).Error; err != nil {
				t.Fatal(err)
			}
			if activeRuns != 0 {
				t.Fatalf("canceled public trigger left %d active TaskRun(s)", activeRuns)
			}
			var finalTask model.Task
			if err := db.First(&finalTask, taskEntity.ID).Error; err != nil {
				t.Fatal(err)
			}
			if finalTask.Status != string(StatusSuccess) {
				t.Fatalf("canceled public trigger changed prior terminal Task status=%q", finalTask.Status)
			}
		})
	}
}

func TestCancelAfterTriggerDurablyTerminatesPendingRunBeforeExecutor(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		restore bool
	}{
		{name: "ordinary"},
		{name: "legacy_restore", restore: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db := openConcurrentManagerTestDB(t)
			ordinaryExecutor := &successExecutor{}
			restoreExecutor := &trackingRestoreExecutor{}
			factoryExecutor := taskexec.Executor(ordinaryExecutor)
			if testCase.restore {
				factoryExecutor = restoreExecutor
			}
			manager := NewManager(db, stubExecutorFactory{executor: factoryExecutor}, nil, nil, nil, nil, 8, 90)
			taskEntity := seedTaskForManagerTest(t, db)
			if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).
				Update("status", string(StatusSuccess)).Error; err != nil {
				t.Fatal(err)
			}
			taskEntity.Status = string(StatusSuccess)
			if err := db.Create(&model.TaskRun{
				TaskID: taskEntity.ID, TriggerType: "manual", Status: "success",
			}).Error; err != nil {
				t.Fatal(err)
			}

			startEntered := make(chan struct{})
			startRelease := make(chan struct{})
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(startRelease) }) }
			defer release()
			manager.SetNodeWriteAdmission(&nodeWriteAdmissionFake{
				startEntered: startEntered,
				startRelease: startRelease,
			})
			var precheckCalls atomic.Int32
			manager.ensureRemoteTargetReadyFunc = func(context.Context, model.Node, string) error {
				precheckCalls.Add(1)
				return nil
			}

			var (
				runID      uint
				triggerErr error
			)
			if testCase.restore {
				runID, triggerErr = manager.TriggerRestore(taskEntity.ID, "/tmp/restore-test")
			} else {
				runID, triggerErr = manager.TriggerManual(taskEntity.ID)
			}
			if triggerErr != nil {
				t.Fatalf("trigger task: %v", triggerErr)
			}
			select {
			case <-startEntered:
			case <-time.After(3 * time.Second):
				t.Fatal("runner did not reach its pending-to-running admission")
			}

			cancelErr := manager.Cancel(taskEntity.ID)
			var afterCancel model.TaskRun
			loadErr := db.First(&afterCancel, runID).Error
			release()
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer shutdownCancel()
			shutdownErr := manager.Shutdown(shutdownCtx)

			if cancelErr != nil {
				t.Fatalf("cancel pending run: %v", cancelErr)
			}
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if afterCancel.Status != "canceled" {
				t.Fatalf("Cancel returned with TaskRun status=%q, want durable canceled", afterCancel.Status)
			}
			if shutdownErr != nil {
				t.Fatalf("shutdown task manager: %v", shutdownErr)
			}
			if ordinaryExecutor.Calls() != 0 {
				t.Fatalf("canceled ordinary task entered executor %d time(s)", ordinaryExecutor.Calls())
			}
			if got := precheckCalls.Load(); got != 0 {
				t.Fatalf("canceled legacy restore entered precheck %d time(s)", got)
			}
			if got := atomic.LoadInt32(&restoreExecutor.calls); got != 0 {
				t.Fatalf("canceled legacy restore entered executor %d time(s)", got)
			}
		})
	}
}

func TestTriggerRestoreEarlyCancellationPreservesCommittedTerminalRun(t *testing.T) {
	db := openConcurrentManagerTestDB(t)
	restoreExecutor := &trackingRestoreExecutor{}
	manager := NewManager(db, stubExecutorFactory{executor: restoreExecutor}, nil, nil, nil, nil, 8, 90)
	taskEntity := seedTaskForManagerTest(t, db)
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).
		Update("status", string(StatusSuccess)).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.TaskRun{
		TaskID: taskEntity.ID, TriggerType: "manual", Status: "success",
	}).Error; err != nil {
		t.Fatal(err)
	}
	for range cap(manager.semaphore) {
		manager.semaphore <- struct{}{}
	}

	overwriteEntered := make(chan struct{})
	overwriteRelease := make(chan struct{})
	var enteredOnce sync.Once
	var releaseOnce sync.Once
	releaseOverwrite := func() { releaseOnce.Do(func() { close(overwriteRelease) }) }
	callbackName := fmt.Sprintf("test:block-restore-early-cancel-overwrite-%d", taskEntity.ID)
	if err := db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		updates, ok := tx.Statement.Dest.(map[string]interface{})
		if !ok || tx.Statement.Table != "task_runs" || updates["status"] != "canceled" ||
			updates["last_error"] != "恢复任务已取消" {
			return
		}
		enteredOnce.Do(func() { close(overwriteEntered) })
		<-overwriteRelease
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		releaseOverwrite()
		_ = db.Callback().Update().Remove(callbackName)
		for len(manager.semaphore) > 0 {
			<-manager.semaphore
		}
	})

	var precheckCalls atomic.Int32
	manager.ensureRemoteTargetReadyFunc = func(context.Context, model.Node, string) error {
		precheckCalls.Add(1)
		return nil
	}
	runID, err := manager.TriggerRestore(taskEntity.ID, "/tmp/restore-terminal-cancel")
	if err != nil {
		t.Fatalf("trigger legacy restore: %v", err)
	}
	if err := manager.Cancel(taskEntity.ID); err != nil {
		t.Fatalf("cancel pending legacy restore: %v", err)
	}

	select {
	case <-overwriteEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("legacy restore runner did not attempt its early cancellation update")
	}
	var committedCancel model.TaskRun
	if err := db.First(&committedCancel, runID).Error; err != nil {
		t.Fatal(err)
	}
	if committedCancel.Status != "canceled" || committedCancel.LastError != "任务已取消" || committedCancel.FinishedAt == nil {
		t.Fatalf("committed cancellation status/error/finished_at=%q/%q/%v", committedCancel.Status, committedCancel.LastError, committedCancel.FinishedAt)
	}

	releaseOverwrite()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer shutdownCancel()
	if err := manager.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("shutdown manager: %v", err)
	}
	var finalRun model.TaskRun
	if err := db.First(&finalRun, runID).Error; err != nil {
		t.Fatal(err)
	}
	if finalRun.Status != committedCancel.Status || finalRun.LastError != committedCancel.LastError ||
		finalRun.FinishedAt == nil || !finalRun.FinishedAt.Equal(*committedCancel.FinishedAt) {
		t.Fatalf("runner overwrote committed cancellation: before=%q/%q/%v after=%q/%q/%v",
			committedCancel.Status, committedCancel.LastError, committedCancel.FinishedAt,
			finalRun.Status, finalRun.LastError, finalRun.FinishedAt)
	}
	if got := precheckCalls.Load(); got != 0 {
		t.Fatalf("canceled legacy restore entered precheck %d time(s)", got)
	}
	if got := atomic.LoadInt32(&restoreExecutor.calls); got != 0 {
		t.Fatalf("canceled legacy restore entered executor %d time(s)", got)
	}
}

func TestCancelDuringTaskEntryCommitPreservesPriorOutcomeWithoutExecutor(t *testing.T) {
	for _, previousStatus := range []TaskStatus{StatusSuccess, StatusWarning} {
		t.Run(string(previousStatus), func(t *testing.T) {
			testCancelTaskEntryCommitPreservesPriorOutcomeWithoutExecutor(t, previousStatus, false)
		})
	}
}

func TestCancelAfterDurableTaskEntryPreservesPriorTerminalOutcomeWithoutExecutor(t *testing.T) {
	for _, previousStatus := range []TaskStatus{
		StatusSuccess,
		StatusWarning,
		StatusFailed,
		StatusCanceled,
		StatusSkipped,
	} {
		t.Run(string(previousStatus), func(t *testing.T) {
			testCancelTaskEntryCommitPreservesPriorOutcomeWithoutExecutor(t, previousStatus, true)
		})
	}
}

func testCancelTaskEntryCommitPreservesPriorOutcomeWithoutExecutor(
	t *testing.T,
	previousStatus TaskStatus,
	cancelAfterCommit bool,
) {
	t.Helper()
	db := openConcurrentManagerTestDB(t)
	executor := &successExecutor{}
	manager := NewManager(db, stubExecutorFactory{executor: executor}, nil, nil, nil, nil, 8, 90)
	taskEntity := seedTaskForManagerTest(t, db)
	previousLastRunAt := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Millisecond)
	previousNextRunAt := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Millisecond)
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Updates(map[string]interface{}{
		"status":      string(previousStatus),
		"last_run_at": &previousLastRunAt,
		"next_run_at": &previousNextRunAt,
		"last_error":  "previous durable outcome",
		"retry_count": 3,
		"skip_next":   true,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.TaskRun{
		TaskID: taskEntity.ID, TriggerType: "manual", Status: "success",
	}).Error; err != nil {
		t.Fatal(err)
	}
	var previous model.Task
	if err := db.First(&previous, taskEntity.ID).Error; err != nil {
		t.Fatal(err)
	}

	startEntered := make(chan struct{})
	startRelease := make(chan struct{})
	manager.SetNodeWriteAdmission(&nodeWriteAdmissionFake{
		startEntered: startEntered,
		startRelease: startRelease,
	})

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	commitEntered := make(chan struct{})
	commitRelease := make(chan struct{})
	commitPool := &taskEntryCommitBarrierPool{
		DB: sqlDB, committed: commitEntered, release: commitRelease,
	}
	db.ConnPool = commitPool
	db.Statement.ConnPool = commitPool

	var blockCancelRead atomic.Bool
	cancelReadEntered := make(chan struct{})
	cancelReadRelease := make(chan struct{})
	var cancelReadOnce sync.Once
	callbackName := fmt.Sprintf("test:task-entry-cancel-read-%d", taskEntity.ID)
	if err := db.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
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
	t.Cleanup(func() {
		releaseStart()
		releaseCancelRead()
		releaseCommit()
		_ = db.Callback().Query().Remove(callbackName)
	})

	runID, err := manager.TriggerManual(taskEntity.ID)
	if err != nil {
		t.Fatalf("trigger ordinary task: %v", err)
	}
	select {
	case <-startEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("runner did not reach the TaskRun start transaction")
	}

	commitPool.armed.Store(true)
	if !cancelAfterCommit {
		blockCancelRead.Store(true)
		cancelResult := make(chan error, 1)
		go func() { cancelResult <- manager.Cancel(taskEntity.ID) }()
		select {
		case <-cancelReadEntered:
		case <-time.After(3 * time.Second):
			t.Fatal("Cancel did not read the prior terminal Task outcome")
		}

		releaseStart()
		select {
		case <-commitEntered:
		case <-time.After(3 * time.Second):
			t.Fatal("executor-entry transaction did not durably commit")
		}
		releaseCancelRead()
		select {
		case err := <-cancelResult:
			if err != nil {
				t.Fatalf("cancel task after executor-entry commit: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("Cancel did not return after the executor-entry commit")
		}
	} else {
		releaseStart()
		select {
		case <-commitEntered:
		case <-time.After(3 * time.Second):
			t.Fatal("executor-entry transaction did not durably commit")
		}

		blockCancelRead.Store(true)
		cancelResult := make(chan error, 1)
		go func() { cancelResult <- manager.Cancel(taskEntity.ID) }()
		select {
		case <-cancelReadEntered:
		case <-time.After(3 * time.Second):
			t.Fatal("Cancel did not read durable running Task state")
		}
		releaseCancelRead()
		select {
		case err := <-cancelResult:
			if err != nil {
				t.Fatalf("cancel task after executor-entry commit: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("Cancel did not return after reading durable running Task state")
		}
	}
	releaseCommit()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer shutdownCancel()
	if err := manager.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("shutdown task manager: %v", err)
	}
	if executor.Calls() != 0 {
		t.Fatalf("canceled task entered executor %d time(s)", executor.Calls())
	}

	var finalRun model.TaskRun
	if err := db.First(&finalRun, runID).Error; err != nil {
		t.Fatal(err)
	}
	if finalRun.Status != "canceled" {
		t.Fatalf("TaskRun status=%q, want canceled", finalRun.Status)
	}
	if finalRun.StartedAt != nil {
		t.Fatalf("TaskRun started_at=%v, want nil because executor never began", finalRun.StartedAt)
	}
	var finalTask model.Task
	if err := db.First(&finalTask, taskEntity.ID).Error; err != nil {
		t.Fatal(err)
	}
	if finalTask.Status != previous.Status {
		t.Fatalf("Task status=%q, want preserved %q", finalTask.Status, previous.Status)
	}
	if finalTask.LastRunAt == nil || previous.LastRunAt == nil || !finalTask.LastRunAt.Equal(*previous.LastRunAt) {
		t.Fatalf("Task last_run_at=%v, want preserved %v", finalTask.LastRunAt, previous.LastRunAt)
	}
	if finalTask.NextRunAt == nil || previous.NextRunAt == nil || !finalTask.NextRunAt.Equal(*previous.NextRunAt) {
		t.Fatalf("Task next_run_at=%v, want preserved %v", finalTask.NextRunAt, previous.NextRunAt)
	}
	if finalTask.LastError != previous.LastError || finalTask.RetryCount != previous.RetryCount || finalTask.SkipNext != previous.SkipNext {
		t.Fatalf("Task prior outcome fields changed: got error=%q retry=%d skip=%v", finalTask.LastError, finalTask.RetryCount, finalTask.SkipNext)
	}
}

func TestNoExecutorCompensationAfterDurableEntryRestoresPendingAndRetryingTasks(t *testing.T) {
	for _, previousStatus := range []TaskStatus{StatusPending, StatusRetrying} {
		for _, stop := range []struct {
			name     string
			shutdown bool
		}{
			{name: "shutdown", shutdown: true},
			{name: "deadline"},
		} {
			t.Run(string(previousStatus)+"/"+stop.name, func(t *testing.T) {
				testNoExecutorCompensationAfterDurableEntry(t, previousStatus, stop.shutdown, false)
			})
		}
	}
}

func TestLegacyRestoreNoExecutorCompensationAfterDurableEntry(t *testing.T) {
	for _, stop := range []struct {
		name     string
		shutdown bool
	}{
		{name: "shutdown", shutdown: true},
		{name: "deadline"},
	} {
		t.Run(stop.name, func(t *testing.T) {
			testNoExecutorCompensationAfterDurableEntry(t, StatusSuccess, stop.shutdown, true)
		})
	}
}

func testNoExecutorCompensationAfterDurableEntry(
	t *testing.T,
	previousStatus TaskStatus,
	shutdown bool,
	legacyRestore bool,
) {
	t.Helper()
	db := openConcurrentManagerTestDB(t)
	ordinaryExecutor := &successExecutor{}
	restoreExecutor := &trackingRestoreExecutor{}
	factoryExecutor := taskexec.Executor(ordinaryExecutor)
	if legacyRestore {
		factoryExecutor = restoreExecutor
	}
	manager := NewManager(db, stubExecutorFactory{executor: factoryExecutor}, nil, nil, nil, nil, 8, 90)
	taskEntity := seedTaskForManagerTest(t, db)
	previousLastRunAt := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Millisecond)
	previousNextRunAt := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Millisecond)
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Updates(map[string]interface{}{
		"status":      string(previousStatus),
		"last_run_at": &previousLastRunAt,
		"next_run_at": &previousNextRunAt,
		"last_error":  "previous no-executor outcome",
		"retry_count": 3,
		"skip_next":   true,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if legacyRestore {
		taskEntity.ExecutorType = "rsync"
	}
	var previous model.Task
	if err := db.First(&previous, taskEntity.ID).Error; err != nil {
		t.Fatal(err)
	}

	runID := createTestTaskRun(t, db, taskEntity.ID, map[bool]string{true: "restore", false: "manual"}[legacyRestore])
	startEntered := make(chan struct{})
	startRelease := make(chan struct{})
	manager.SetNodeWriteAdmission(&nodeWriteAdmissionFake{
		startEntered: startEntered,
		startRelease: startRelease,
	})
	var precheckCalls atomic.Int32
	manager.ensureRemoteTargetReadyFunc = func(context.Context, model.Node, string) error {
		precheckCalls.Add(1)
		return nil
	}

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	commitEntered := make(chan struct{})
	commitRelease := make(chan struct{})
	commitPool := &taskEntryCommitBarrierPool{
		DB: sqlDB, committed: commitEntered, release: commitRelease,
	}
	originalPool := db.ConnPool
	originalStatementPool := db.Statement.ConnPool
	db.ConnPool = commitPool
	db.Statement.ConnPool = commitPool

	var releaseStartOnce sync.Once
	releaseStart := func() { releaseStartOnce.Do(func() { close(startRelease) }) }
	var releaseCommitOnce sync.Once
	releaseCommit := func() { releaseCommitOnce.Do(func() { close(commitRelease) }) }
	t.Cleanup(func() {
		releaseStart()
		releaseCommit()
		db.ConnPool = originalPool
		db.Statement.ConnPool = originalStatementPool
	})

	var runCtx context.Context
	var runCancel context.CancelFunc
	var deadlineCtx *taskEntryDeadlineContext
	if shutdown {
		runCtx, runCancel = context.WithCancel(context.Background())
		manager.chainRunner.Store(taskEntity.ID, runCancel)
	} else {
		deadlineCtx = newTaskEntryDeadlineContext()
		runCtx = deadlineCtx
		runCancel = func() {}
	}

	runDone := make(chan struct{})
	manager.taskWG.Add(1)
	go func() {
		defer manager.taskWG.Done()
		defer close(runDone)
		if legacyRestore {
			restoreTask := taskEntity
			restoreTask.RsyncSource = taskEntity.RsyncTarget
			restoreTask.RsyncTarget = "/tmp/no-executor-restore"
			manager.runRestoreTaskWithContext(taskEntity.ID, runID, restoreTask, runCtx, runCancel)
			return
		}
		manager.runTaskWithContext(taskEntity.ID, runID, "manual", generateChainRunID(), runCtx, runCancel)
	}()

	select {
	case <-startEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("runner did not reach executor-entry transaction")
	}
	commitPool.armed.Store(true)
	releaseStart()
	select {
	case <-commitEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("executor-entry transaction did not durably commit")
	}

	if shutdown {
		shutdownResult := make(chan error, 1)
		go func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			shutdownResult <- manager.Shutdown(shutdownCtx)
		}()
		select {
		case <-runCtx.Done():
		case <-time.After(3 * time.Second):
			t.Fatal("Shutdown did not cancel committed no-executor runner")
		}
		releaseCommit()
		select {
		case err := <-shutdownResult:
			if err != nil {
				t.Fatalf("shutdown task manager: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("Shutdown did not join no-executor runner")
		}
	} else {
		deadlineCtx.Expire()
		releaseCommit()
		select {
		case <-runDone:
		case <-time.After(3 * time.Second):
			t.Fatal("deadline-canceled runner did not return")
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := manager.Shutdown(shutdownCtx); err != nil {
			t.Fatalf("shutdown task manager after deadline: %v", err)
		}
	}

	if ordinaryExecutor.Calls() != 0 {
		t.Fatalf("ordinary no-executor runner entered executor %d time(s)", ordinaryExecutor.Calls())
	}
	if got := atomic.LoadInt32(&restoreExecutor.calls); got != 0 {
		t.Fatalf("legacy no-executor runner entered executor %d time(s)", got)
	}
	if got := precheckCalls.Load(); got != 0 {
		t.Fatalf("legacy no-executor runner entered precheck %d time(s)", got)
	}

	var finalRun model.TaskRun
	if err := db.First(&finalRun, runID).Error; err != nil {
		t.Fatal(err)
	}
	if finalRun.Status != "canceled" {
		t.Fatalf("TaskRun status=%q, want canceled", finalRun.Status)
	}
	if finalRun.StartedAt != nil {
		t.Fatalf("TaskRun started_at=%v, want nil because executor never began", finalRun.StartedAt)
	}

	var finalTask model.Task
	if err := db.First(&finalTask, taskEntity.ID).Error; err != nil {
		t.Fatal(err)
	}
	if finalTask.Status != previous.Status || finalTask.LastError != previous.LastError ||
		finalTask.RetryCount != previous.RetryCount || finalTask.SkipNext != previous.SkipNext {
		t.Fatalf("Task outcome changed: got status=%q error=%q retry=%d skip=%v",
			finalTask.Status, finalTask.LastError, finalTask.RetryCount, finalTask.SkipNext)
	}
	if finalTask.LastRunAt == nil || previous.LastRunAt == nil || !finalTask.LastRunAt.Equal(*previous.LastRunAt) {
		t.Fatalf("Task last_run_at=%v, want preserved %v", finalTask.LastRunAt, previous.LastRunAt)
	}
	if finalTask.NextRunAt == nil || previous.NextRunAt == nil || !finalTask.NextRunAt.Equal(*previous.NextRunAt) {
		t.Fatalf("Task next_run_at=%v, want preserved %v", finalTask.NextRunAt, previous.NextRunAt)
	}
}

func TestCancelWhileTriggerReservesRunPreventsDurableStart(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		restore bool
	}{
		{name: "ordinary"},
		{name: "legacy_restore", restore: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db := openConcurrentManagerTestDB(t)
			ordinaryExecutor := newBlockingExecutor()
			restoreExecutor := &trackingRestoreExecutor{}
			factoryExecutor := taskexec.Executor(ordinaryExecutor)
			if testCase.restore {
				factoryExecutor = restoreExecutor
			}
			manager := NewManager(db, stubExecutorFactory{executor: factoryExecutor}, nil, nil, nil, nil, 8, 90)
			taskEntity := seedTaskForManagerTest(t, db)
			if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).
				Update("status", string(StatusSuccess)).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&model.TaskRun{
				TaskID: taskEntity.ID, TriggerType: "manual", Status: "success",
			}).Error; err != nil {
				t.Fatal(err)
			}

			admitEntered := make(chan struct{})
			admitRelease := make(chan struct{})
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(admitRelease) }) }
			defer release()
			manager.SetNodeWriteAdmission(&nodeWriteAdmissionFake{
				admitEntered: admitEntered,
				admitRelease: admitRelease,
			})
			manager.ensureRemoteTargetReadyFunc = func(ctx context.Context, _ model.Node, _ string) error {
				<-ctx.Done()
				return ctx.Err()
			}

			type triggerResult struct {
				runID uint
				err   error
			}
			resultCh := make(chan triggerResult, 1)
			go func() {
				var result triggerResult
				if testCase.restore {
					result.runID, result.err = manager.TriggerRestore(taskEntity.ID, "/tmp/restore-test")
				} else {
					result.runID, result.err = manager.TriggerManual(taskEntity.ID)
				}
				resultCh <- result
			}()

			select {
			case <-admitEntered:
			case <-time.After(3 * time.Second):
				t.Fatal("trigger did not reach its durable TaskRun reservation")
			}
			cancelErr := manager.Cancel(taskEntity.ID)
			release()
			var result triggerResult
			select {
			case result = <-resultCh:
			case <-time.After(3 * time.Second):
				t.Fatal("trigger did not return after reservation release")
			}

			if cancelErr != nil && result.err == nil {
				deadline := time.After(3 * time.Second)
				for {
					if _, ok := manager.chainRunner.Load(taskEntity.ID); ok {
						_ = manager.Cancel(taskEntity.ID)
						break
					}
					select {
					case <-deadline:
						t.Fatal("scheduled runner never registered its cancel owner for cleanup")
					case <-time.After(time.Millisecond):
					}
				}
			}
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer shutdownCancel()
			shutdownErr := manager.Shutdown(shutdownCtx)

			if cancelErr != nil {
				t.Fatalf("Cancel could not reach the trigger reservation: %v", cancelErr)
			}
			if !errors.Is(result.err, context.Canceled) {
				t.Fatalf("canceled trigger error=%v, want context cancellation", result.err)
			}
			if result.runID != 0 {
				t.Fatalf("canceled trigger returned durable TaskRun ID %d", result.runID)
			}
			if shutdownErr != nil {
				t.Fatalf("shutdown task manager: %v", shutdownErr)
			}
			var activeRuns int64
			if err := db.Model(&model.TaskRun{}).
				Where("task_id = ? AND status IN ?", taskEntity.ID, []string{"pending", "running"}).
				Count(&activeRuns).Error; err != nil {
				t.Fatal(err)
			}
			if activeRuns != 0 {
				t.Fatalf("canceled trigger left %d active TaskRun(s)", activeRuns)
			}
			if ordinaryExecutor.Calls() != 0 {
				t.Fatalf("canceled ordinary trigger entered executor %d time(s)", ordinaryExecutor.Calls())
			}
			if got := atomic.LoadInt32(&restoreExecutor.calls); got != 0 {
				t.Fatalf("canceled legacy restore entered executor %d time(s)", got)
			}
		})
	}
}

func TestRunTaskCanceledAfterInitialCheckCannotResurrectOrEnterExecutor(t *testing.T) {
	db := openManagerTestDB(t)
	exec := &successExecutor{}
	manager := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, nil, 8, 90)
	taskEntity := seedTaskForManagerTest(t, db)
	runID := createTestTaskRun(t, db, taskEntity.ID, "manual")

	strategyLock := manager.strategyLock(taskEntity.NodeID, taskEntity.PolicyID)
	strategyLock.Lock()
	lockReleased := false
	defer func() {
		if !lockReleased {
			strategyLock.Unlock()
		}
	}()

	observed := make(chan struct{})
	var observedOnce sync.Once
	callbackName := fmt.Sprintf("test:observe-task-run-status-%d", runID)
	if err := db.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == "task_runs" {
			observedOnce.Do(func() { close(observed) })
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callbackName) })

	done := make(chan struct{})
	go func() {
		manager.runTask(taskEntity.ID, runID, "manual", generateChainRunID())
		close(done)
	}()

	select {
	case <-observed:
	case <-time.After(3 * time.Second):
		t.Fatal("runTask did not complete its initial TaskRun status observation")
	}
	if err := manager.Cancel(taskEntity.ID); err != nil {
		t.Fatalf("cancel pending task: %v", err)
	}
	var canceled model.TaskRun
	if err := db.First(&canceled, runID).Error; err != nil {
		t.Fatal(err)
	}
	if canceled.Status != "canceled" {
		t.Fatalf("cancel committed TaskRun status=%q, want canceled", canceled.Status)
	}

	strategyLock.Unlock()
	lockReleased = true
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("runTask did not return after the cancellation barrier released")
	}

	if exec.Calls() != 0 {
		t.Fatalf("canceled pending TaskRun entered executor %d time(s)", exec.Calls())
	}
	var finalRun model.TaskRun
	if err := db.First(&finalRun, runID).Error; err != nil {
		t.Fatal(err)
	}
	if finalRun.Status != "canceled" {
		t.Fatalf("canceled TaskRun resurrected to %q", finalRun.Status)
	}
}

func TestRunRestoreTaskCanceledBeforeStartCannotEnterPrecheckOrExecutor(t *testing.T) {
	db := openConcurrentManagerTestDB(t)
	restoreExecutor := &trackingRestoreExecutor{}
	manager := NewManager(db, stubExecutorFactory{executor: restoreExecutor}, nil, nil, nil, nil, 8, 90)
	taskEntity := seedTaskForManagerTest(t, db)
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Update("status", string(StatusSuccess)).Error; err != nil {
		t.Fatal(err)
	}
	taskEntity.Status = string(StatusSuccess)
	runID := createTestTaskRun(t, db, taskEntity.ID, "restore")

	enteredRunningUpdate := make(chan struct{})
	releaseRunningUpdate := make(chan struct{})
	var enteredOnce sync.Once
	callbackName := fmt.Sprintf("test:block-restore-running-%d", runID)
	if err := db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		updates, ok := tx.Statement.Dest.(map[string]interface{})
		if !ok || tx.Statement.Table != "task_runs" || updates["status"] != "running" {
			return
		}
		enteredOnce.Do(func() { close(enteredRunningUpdate) })
		<-releaseRunningUpdate
	}); err != nil {
		t.Fatal(err)
	}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseRunningUpdate) }) }
	t.Cleanup(func() {
		release()
		_ = db.Callback().Update().Remove(callbackName)
	})

	var precheckCalls atomic.Int32
	manager.ensureRemoteTargetReadyFunc = func(ctx context.Context, _ model.Node, _ string) error {
		precheckCalls.Add(1)
		return ctx.Err()
	}
	done := make(chan struct{})
	go func() {
		manager.runRestoreTask(taskEntity.ID, runID, taskEntity)
		close(done)
	}()

	select {
	case <-enteredRunningUpdate:
	case <-time.After(3 * time.Second):
		t.Fatal("runRestoreTask did not reach its pending-to-running update")
	}
	if err := manager.Cancel(taskEntity.ID); err != nil {
		t.Fatalf("cancel queued restore: %v", err)
	}
	release()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("runRestoreTask did not return after cancellation")
	}

	if got := precheckCalls.Load(); got != 0 {
		t.Fatalf("canceled restore entered remote precheck %d time(s)", got)
	}
	if got := atomic.LoadInt32(&restoreExecutor.calls); got != 0 {
		t.Fatalf("canceled restore entered executor %d time(s)", got)
	}
	var finalRun model.TaskRun
	if err := db.First(&finalRun, runID).Error; err != nil {
		t.Fatal(err)
	}
	if finalRun.Status != "canceled" {
		t.Fatalf("canceled restore TaskRun ended in %q", finalRun.Status)
	}
}

func TestRunTaskExecutorEntryAdmissionFailureDoesNotEnterExecutor(t *testing.T) {
	db := openManagerTestDB(t)
	exec := &successExecutor{}
	manager := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, nil, 8, 90)
	taskEntity := seedTaskForManagerTest(t, db)
	runID := createTestTaskRun(t, db, taskEntity.ID, "manual")
	admission := &nodeWriteAdmissionFake{startErrs: []error{ErrNodeWriteConflict}}
	manager.SetNodeWriteAdmission(admission)

	manager.runTask(taskEntity.ID, runID, "manual", generateChainRunID())

	if exec.Calls() != 0 {
		t.Fatalf("executor-entry conflict reached executor %d time(s)", exec.Calls())
	}
	startCalls, runIDs, nodeIDs := admission.startSnapshot()
	if startCalls != 1 || len(runIDs) != 1 || runIDs[0] != runID || len(nodeIDs) != 1 || nodeIDs[0] != taskEntity.NodeID {
		t.Fatalf("executor-entry calls=%d run IDs=%v node IDs=%v", startCalls, runIDs, nodeIDs)
	}
	reservationCalls, _, _ := admission.snapshot()
	if reservationCalls != 0 {
		t.Fatalf("direct runner unexpectedly changed reservation calls=%d", reservationCalls)
	}
	var finalRun model.TaskRun
	if err := db.First(&finalRun, runID).Error; err != nil {
		t.Fatal(err)
	}
	if finalRun.Status != "failed" {
		t.Fatalf("executor-entry conflict left TaskRun status=%q, want failed", finalRun.Status)
	}
}

func TestRunRestoreTaskExecutorEntryAdmissionFailureDoesNotEnterPrecheckOrExecutor(t *testing.T) {
	db := openManagerTestDB(t)
	restoreExecutor := &trackingRestoreExecutor{}
	manager := NewManager(db, stubExecutorFactory{executor: restoreExecutor}, nil, nil, nil, nil, 8, 90)
	taskEntity := seedTaskForManagerTest(t, db)
	runID := createTestTaskRun(t, db, taskEntity.ID, "restore")
	admission := &nodeWriteAdmissionFake{startErrs: []error{ErrNodeWriteConflict}}
	manager.SetNodeWriteAdmission(admission)
	var precheckCalls atomic.Int32
	manager.ensureRemoteTargetReadyFunc = func(context.Context, model.Node, string) error {
		precheckCalls.Add(1)
		return nil
	}

	manager.runRestoreTask(taskEntity.ID, runID, taskEntity)

	if got := precheckCalls.Load(); got != 0 {
		t.Fatalf("executor-entry conflict reached restore precheck %d time(s)", got)
	}
	if got := atomic.LoadInt32(&restoreExecutor.calls); got != 0 {
		t.Fatalf("executor-entry conflict reached restore executor %d time(s)", got)
	}
	startCalls, runIDs, nodeIDs := admission.startSnapshot()
	if startCalls != 1 || len(runIDs) != 1 || runIDs[0] != runID || len(nodeIDs) != 1 || nodeIDs[0] != taskEntity.NodeID {
		t.Fatalf("restore executor-entry calls=%d run IDs=%v node IDs=%v", startCalls, runIDs, nodeIDs)
	}
	reservationCalls, _, _ := admission.snapshot()
	if reservationCalls != 0 {
		t.Fatalf("direct restore runner unexpectedly changed reservation calls=%d", reservationCalls)
	}
	var finalRun model.TaskRun
	if err := db.First(&finalRun, runID).Error; err != nil {
		t.Fatal(err)
	}
	if finalRun.Status != "failed" {
		t.Fatalf("restore executor-entry conflict left TaskRun status=%q, want failed", finalRun.Status)
	}
}

func TestTriggerManualRejectsConcurrentDuplicate(t *testing.T) {
	db := openManagerTestDB(t)
	exec := newBlockingExecutor()
	m := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, nil, 8, 90)
	taskEntity := seedTaskForManagerTest(t, db)

	for i := 0; i < cap(m.semaphore); i++ {
		m.semaphore <- struct{}{}
	}

	const attempts = 64
	start := make(chan struct{})
	resultCh := make(chan error, attempts)
	for i := 0; i < attempts; i++ {
		go func() {
			<-start
			_, err := m.TriggerManual(taskEntity.ID)
			resultCh <- err
		}()
	}
	close(start)

	successCount := 0
	for i := 0; i < attempts; i++ {
		err := <-resultCh
		if err == nil {
			successCount++
		}
	}

	if successCount != 1 {
		t.Fatalf("期望并发触发仅 1 次成功，实际成功: %d", successCount)
	}

	for i := 0; i < cap(m.semaphore); i++ {
		<-m.semaphore
	}

	select {
	case <-exec.started:
	case <-time.After(2 * time.Second):
		t.Fatal("等待任务开始执行超时")
	}
	close(exec.release)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := m.Shutdown(shutdownCtx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("关闭 manager 失败: %v", err)
	}

	if exec.Calls() != 1 {
		t.Fatalf("期望执行器仅执行 1 次，实际: %d", exec.Calls())
	}
}

func TestRunTaskPersistsTrafficSamplesWithMinuteThrottle(t *testing.T) {
	db := openManagerTestDB(t)
	taskEntity := seedTaskForManagerTest(t, db)
	now := time.Date(2026, 3, 8, 0, 10, 0, 0, time.UTC)
	exec := &sampleExecutor{samples: []taskexec.ProgressSample{
		{ObservedAt: now, ThroughputMbps: 100},
		{ObservedAt: now.Add(20 * time.Second), ThroughputMbps: 120},
		{ObservedAt: now.Add(65 * time.Second), ThroughputMbps: 80},
	}}
	m := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, nil, 8, 90)

	runID := createTestTaskRun(t, db, taskEntity.ID, "manual")
	m.runTask(taskEntity.ID, runID, "manual", generateChainRunID())

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := m.Shutdown(ctx); err != nil {
		t.Fatalf("关闭 manager 失败: %v", err)
	}

	var samples []model.TaskTrafficSample
	if err := db.Order("sampled_at asc").Find(&samples).Error; err != nil {
		t.Fatalf("查询采样失败: %v", err)
	}
	if len(samples) != 3 {
		t.Fatalf("期望 10 秒节流后落 3 条样本，实际: %d", len(samples))
	}
	if samples[0].ThroughputMbps != 100 {
		t.Fatalf("首条样本吞吐应为 100，实际: %v", samples[0].ThroughputMbps)
	}
	if samples[1].ThroughputMbps != 120 {
		t.Fatalf("第二条样本吞吐应为 120，实际: %v", samples[1].ThroughputMbps)
	}
	if samples[2].ThroughputMbps != 80 {
		t.Fatalf("第三条样本吞吐应为 80，实际: %v", samples[2].ThroughputMbps)
	}
	if samples[0].RunStartedAt.IsZero() || samples[1].RunStartedAt.IsZero() {
		t.Fatalf("期望记录 run_started_at")
	}
}

func TestTriggerCreatesTaskRun(t *testing.T) {
	db := openManagerTestDB(t)
	exec := &successExecutor{}
	m := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, nil, 8, 90)
	taskEntity := seedTaskForManagerTest(t, db)

	runID, err := m.TriggerManual(taskEntity.ID)
	if err != nil {
		t.Fatalf("触发任务失败: %v", err)
	}
	if runID == 0 {
		t.Fatalf("期望返回非零 runID")
	}

	// 等待任务完成
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := m.Shutdown(ctx); err != nil {
		t.Fatalf("关闭 manager 失败: %v", err)
	}

	var run model.TaskRun
	if err := db.First(&run, runID).Error; err != nil {
		t.Fatalf("查询 TaskRun 失败: %v", err)
	}
	if run.TaskID != taskEntity.ID {
		t.Fatalf("TaskRun.TaskID 期望 %d，实际 %d", taskEntity.ID, run.TaskID)
	}
	if run.TriggerType != "manual" {
		t.Fatalf("TaskRun.TriggerType 期望 manual，实际 %s", run.TriggerType)
	}
}

func TestTriggerManualNodeWriteConflictLeavesNoReservationOrMarker(t *testing.T) {
	db := openManagerTestDB(t)
	exec := &successExecutor{}
	manager := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, nil, 8, 90)
	taskEntity := seedTaskForManagerTest(t, db)
	admission := &nodeWriteAdmissionFake{errs: []error{ErrNodeWriteConflict}}
	manager.SetNodeWriteAdmission(admission)

	runID, err := manager.TriggerManual(taskEntity.ID)
	if !errors.Is(err, ErrNodeWriteConflict) {
		t.Fatalf("TriggerManual error=%v, want node-write conflict", err)
	}
	if runID != 0 {
		t.Fatalf("conflicted TriggerManual run ID=%d, want zero", runID)
	}
	calls, nodeIDs, counts := admission.snapshot()
	if calls != 1 || len(nodeIDs) != 1 || nodeIDs[0] != taskEntity.NodeID {
		t.Fatalf("admission calls=%d node IDs=%v", calls, nodeIDs)
	}
	if len(counts) != 1 || counts[0] != 0 {
		t.Fatalf("TaskRun counts at admission=%v, want [0]", counts)
	}
	var runCount int64
	if err := db.Model(&model.TaskRun{}).Count(&runCount).Error; err != nil {
		t.Fatal(err)
	}
	if runCount != 0 {
		t.Fatalf("conflicted TriggerManual left %d TaskRun rows", runCount)
	}
	if _, ok := manager.pendingRuns.Load(taskEntity.ID); ok {
		t.Fatal("conflicted TriggerManual leaked pendingRuns marker")
	}
	if manager.isNodeRestoring(taskEntity.NodeID) {
		t.Fatal("conflicted TriggerManual leaked restoreNodes marker")
	}
	if exec.Calls() != 0 {
		t.Fatalf("conflicted TriggerManual reached executor %d time(s)", exec.Calls())
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := manager.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
}

func TestTriggerManualRetriesRawSQLiteBusyAroundWholeReservationTransaction(t *testing.T) {
	db := openManagerTestDB(t)
	exec := &successExecutor{}
	manager := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, nil, 8, 90)
	taskEntity := seedTaskForManagerTest(t, db)
	admission := &nodeWriteAdmissionFake{errs: []error{sqlite3.Error{Code: sqlite3.ErrBusy}, nil}}
	manager.SetNodeWriteAdmission(admission)
	var retryWaits atomic.Int32
	manager.nodeWriteRetryWait = func(ctx context.Context, _ int) error {
		retryWaits.Add(1)
		return ctx.Err()
	}
	for range cap(manager.semaphore) {
		manager.semaphore <- struct{}{}
	}

	runID, err := manager.TriggerManual(taskEntity.ID)
	if err != nil {
		t.Fatalf("TriggerManual after SQLite busy retry: %v", err)
	}
	if runID == 0 {
		t.Fatal("TriggerManual returned zero run ID after retry")
	}
	calls, _, counts := admission.snapshot()
	if calls != 2 || len(counts) != 2 || counts[0] != 0 || counts[1] != 0 {
		t.Fatalf("admission calls=%d TaskRun counts=%v, want two pre-insert attempts", calls, counts)
	}
	if retryWaits.Load() != 1 {
		t.Fatalf("reservation retry waits=%d, want one", retryWaits.Load())
	}
	var run model.TaskRun
	if err := db.First(&run, runID).Error; err != nil {
		t.Fatal(err)
	}
	if run.Status != "pending" {
		t.Fatalf("reserved run status=%q, want pending before goroutine starts", run.Status)
	}
	var runCount int64
	if err := db.Model(&model.TaskRun{}).Where("task_id = ?", taskEntity.ID).Count(&runCount).Error; err != nil {
		t.Fatal(err)
	}
	if runCount != 1 {
		t.Fatalf("SQLite retry created %d TaskRun rows, want one", runCount)
	}

	for range cap(manager.semaphore) {
		<-manager.semaphore
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := manager.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
}

func TestTriggerRestoreNodeWriteConflictLeavesNoRunMarkerPrecheckOrExecutor(t *testing.T) {
	db := openManagerTestDB(t)
	restoreExecutor := &trackingRestoreExecutor{}
	manager := NewManager(db, stubExecutorFactory{executor: restoreExecutor}, nil, nil, nil, nil, 8, 90)
	taskEntity := seedTaskForManagerTest(t, db)
	if err := db.Create(&model.TaskRun{TaskID: taskEntity.ID, TriggerType: "manual", Status: "success"}).Error; err != nil {
		t.Fatal(err)
	}
	admission := &nodeWriteAdmissionFake{errs: []error{ErrNodeWriteConflict}}
	manager.SetNodeWriteAdmission(admission)
	var precheckCalls atomic.Int32
	manager.ensureRemoteTargetReadyFunc = func(context.Context, model.Node, string) error {
		precheckCalls.Add(1)
		return nil
	}

	runID, err := manager.TriggerRestore(taskEntity.ID, "/tmp/node-write-conflict")
	if !errors.Is(err, ErrNodeWriteConflict) {
		t.Fatalf("TriggerRestore error=%v, want node-write conflict", err)
	}
	if runID != 0 {
		t.Fatalf("conflicted TriggerRestore run ID=%d, want zero", runID)
	}
	calls, nodeIDs, counts := admission.snapshot()
	if calls != 1 || len(nodeIDs) != 1 || nodeIDs[0] != taskEntity.NodeID {
		t.Fatalf("admission calls=%d node IDs=%v", calls, nodeIDs)
	}
	if len(counts) != 1 || counts[0] != 1 {
		t.Fatalf("TaskRun counts at restore admission=%v, want only the successful prerequisite", counts)
	}
	var restoreRuns int64
	if err := db.Model(&model.TaskRun{}).Where("trigger_type = ?", "restore").Count(&restoreRuns).Error; err != nil {
		t.Fatal(err)
	}
	if restoreRuns != 0 {
		t.Fatalf("conflicted TriggerRestore left %d restore TaskRun rows", restoreRuns)
	}
	if _, ok := manager.pendingRuns.Load(taskEntity.ID); ok {
		t.Fatal("conflicted TriggerRestore leaked pendingRuns marker")
	}
	if manager.isNodeRestoring(taskEntity.NodeID) {
		t.Fatal("conflicted TriggerRestore leaked restoreNodes marker")
	}
	if got := precheckCalls.Load(); got != 0 {
		t.Fatalf("conflicted TriggerRestore reached precheck %d time(s)", got)
	}
	if got := atomic.LoadInt32(&restoreExecutor.calls); got != 0 {
		t.Fatalf("conflicted TriggerRestore reached executor %d time(s)", got)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := manager.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
}

func TestRunTaskAttachesCredentialAuditRuntimeContext(t *testing.T) {
	db := openManagerTestDB(t)
	exec := &contextAuditExecutor{}
	m := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, nil, 8, 90)
	taskEntity := seedTaskForManagerTest(t, db)

	runID := createTestTaskRun(t, db, taskEntity.ID, "manual")
	m.runTask(taskEntity.ID, runID, "manual", generateChainRunID())

	var event model.CredentialAuditEvent
	if err := db.Where("action = ?", "task.credential.use").First(&event).Error; err != nil {
		t.Fatalf("期望写入运行时凭据审计事件: %v", err)
	}
	if event.TaskID == nil || *event.TaskID != taskEntity.ID || event.TaskRunID == nil || *event.TaskRunID != runID || event.NodeID == nil || *event.NodeID != taskEntity.NodeID {
		t.Fatalf("审计上下文未包含任务/执行/节点标识: %+v", event)
	}
	if event.Purpose != sshutil.PurposeTaskBackup || event.CredentialSource != "ssh_key_id=42" || event.Outcome != credentialaudit.OutcomeSuccess {
		t.Fatalf("审计事件字段不符合预期: %+v", event)
	}
	if strings.Contains(event.Metadata, "FAKE_COMMAND_FOR_TEST_ONLY") || strings.Contains(event.Metadata, "command") {
		t.Fatalf("审计 metadata 不应保存命令文本或 command 键: %s", event.Metadata)
	}
	if !strings.Contains(event.Metadata, "test_boundary") || !strings.Contains(event.Metadata, "manual") {
		t.Fatalf("审计 metadata 应保留安全上下文字段: %s", event.Metadata)
	}
}

func TestRunTaskDualWriteSuccess(t *testing.T) {
	db := openManagerTestDB(t)
	exec := &successExecutor{}
	m := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, nil, 8, 90)
	taskEntity := seedTaskForManagerTest(t, db)

	runID := createTestTaskRun(t, db, taskEntity.ID, "manual")
	m.runTask(taskEntity.ID, runID, "manual", generateChainRunID())

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = m.Shutdown(ctx)

	// 验证 Task 状态
	var task model.Task
	db.First(&task, taskEntity.ID)
	if task.Status != string(StatusSuccess) {
		t.Fatalf("Task 状态期望 success，实际 %s", task.Status)
	}

	// 验证 TaskRun 状态
	var run model.TaskRun
	db.First(&run, runID)
	if run.Status != "success" {
		t.Fatalf("TaskRun 状态期望 success，实际 %s", run.Status)
	}
	if run.StartedAt == nil {
		t.Fatalf("TaskRun.StartedAt 不应为空")
	}
	if run.FinishedAt == nil {
		t.Fatalf("TaskRun.FinishedAt 不应为空")
	}
	if run.DurationMs < 0 {
		t.Fatalf("TaskRun.DurationMs 不应为负数: %d", run.DurationMs)
	}
}

func TestRunTaskDualWriteFailed(t *testing.T) {
	db := openManagerTestDB(t)
	failExec := &failingExecutor{err: fmt.Errorf("模拟执行失败")}
	m := NewManager(db, stubExecutorFactory{executor: failExec}, nil, nil, nil, nil, 8, 90)
	taskEntity := seedTaskForManagerTest(t, db)

	runID := createTestTaskRun(t, db, taskEntity.ID, "manual")
	m.runTask(taskEntity.ID, runID, "manual", generateChainRunID())

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = m.Shutdown(ctx)

	// TaskRun 应标记为 failed
	var run model.TaskRun
	db.First(&run, runID)
	if run.Status != "failed" {
		t.Fatalf("TaskRun 状态期望 failed，实际 %s", run.Status)
	}
	if run.LastError == "" {
		t.Fatalf("TaskRun.LastError 不应为空")
	}
	if run.FinishedAt == nil {
		t.Fatalf("TaskRun.FinishedAt 不应为空")
	}
}

func TestCancelBeforeRunStartsDoesNotExecute(t *testing.T) {
	db := openManagerTestDB(t)
	exec := &successExecutor{}
	m := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, nil, 8, 90)
	taskEntity := seedTaskForManagerTest(t, db)

	for i := 0; i < cap(m.semaphore); i++ {
		m.semaphore <- struct{}{}
	}
	runID, err := m.TriggerManual(taskEntity.ID)
	if err != nil {
		t.Fatalf("触发任务失败: %v", err)
	}
	if err := m.Cancel(taskEntity.ID); err != nil {
		t.Fatalf("启动前取消任务失败: %v", err)
	}
	for i := 0; i < cap(m.semaphore); i++ {
		<-m.semaphore
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := m.Shutdown(ctx); err != nil {
		t.Fatalf("关闭 manager 失败: %v", err)
	}
	if exec.Calls() != 0 {
		t.Fatalf("启动前取消后执行器不应被调用，实际: %d", exec.Calls())
	}
	var run model.TaskRun
	if err := db.First(&run, runID).Error; err != nil {
		t.Fatalf("查询 TaskRun 失败: %v", err)
	}
	if run.Status != "canceled" {
		t.Fatalf("TaskRun 状态期望 canceled，实际 %s", run.Status)
	}
}

func TestCancelUpdatesTaskRunToCanceled(t *testing.T) {
	db := openManagerTestDB(t)
	exec := newBlockingExecutor()
	m := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, nil, 8, 90)
	taskEntity := seedTaskForManagerTest(t, db)

	runID, err := m.TriggerManual(taskEntity.ID)
	if err != nil {
		t.Fatalf("触发任务失败: %v", err)
	}

	// 等待执行器开始
	select {
	case <-exec.started:
	case <-time.After(3 * time.Second):
		t.Fatal("等待执行器开始超时")
	}

	// 取消任务
	if err := m.Cancel(taskEntity.ID); err != nil {
		t.Fatalf("取消任务失败: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = m.Shutdown(ctx)

	// TaskRun 应标记为 canceled
	var run model.TaskRun
	db.First(&run, runID)
	if run.Status != "canceled" {
		t.Fatalf("TaskRun 状态期望 canceled，实际 %s", run.Status)
	}
}

func TestCleanupExpiredTaskRuns(t *testing.T) {
	db := openManagerTestDB(t)
	m := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 1) // 1 天保留

	taskEntity := seedTaskForManagerTest(t, db)

	// 创建过期的 TaskRun
	oldTime := time.Now().AddDate(0, 0, -3)
	oldRun := model.TaskRun{
		TaskID:      taskEntity.ID,
		TriggerType: "manual",
		Status:      "success",
		CreatedAt:   oldTime,
	}
	db.Create(&oldRun)

	// 创建关联的 TaskLog
	oldLog := model.TaskLog{
		TaskID:    taskEntity.ID,
		TaskRunID: &oldRun.ID,
		Level:     "info",
		Message:   "test log",
	}
	db.Create(&oldLog)

	// 创建关联的 Alert
	oldAlert := model.Alert{
		NodeID:      1,
		NodeName:    "test",
		TaskRunID:   &oldRun.ID,
		Severity:    "warning",
		Status:      "resolved",
		ErrorCode:   "XR-TEST",
		Message:     "test",
		TriggeredAt: oldTime,
	}
	db.Create(&oldAlert)

	// 创建新的 TaskRun（不应被清理）
	newRun := model.TaskRun{
		TaskID:      taskEntity.ID,
		TriggerType: "manual",
		Status:      "success",
	}
	db.Create(&newRun)

	// 重置清理时间以允许执行
	m.lastTaskRunCleanupAt = time.Time{}

	m.cleanupExpiredTaskRuns()

	// 旧 TaskRun 应被删除
	var runCount int64
	db.Model(&model.TaskRun{}).Where("id = ?", oldRun.ID).Count(&runCount)
	if runCount != 0 {
		t.Fatalf("过期 TaskRun 应被删除")
	}

	// 关联 TaskLog 应被删除
	var logCount int64
	db.Model(&model.TaskLog{}).Where("task_run_id = ?", oldRun.ID).Count(&logCount)
	if logCount != 0 {
		t.Fatalf("过期 TaskRun 关联的 TaskLog 应被删除")
	}

	// 关联 Alert 的 task_run_id 应被清空
	var alert model.Alert
	db.First(&alert, oldAlert.ID)
	if alert.TaskRunID != nil {
		t.Fatalf("过期 TaskRun 关联的 Alert.TaskRunID 应被清空")
	}

	// 新 TaskRun 应保留
	var newRunCount int64
	db.Model(&model.TaskRun{}).Where("id = ?", newRun.ID).Count(&newRunCount)
	if newRunCount != 1 {
		t.Fatalf("新 TaskRun 不应被删除")
	}
}

func TestEmitLogWritesTaskRunID(t *testing.T) {
	db := openManagerTestDB(t)
	m := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)

	taskEntity := seedTaskForManagerTest(t, db)
	runID := uint(42)

	// 直接调用 emitLog
	m.logDispatcher.Dispatch(taskEntity.ID, &runID, "info", "test message", "running")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = m.Shutdown(ctx)

	var logs []model.TaskLog
	db.Where("task_id = ?", taskEntity.ID).Find(&logs)
	if len(logs) == 0 {
		t.Fatalf("期望写入至少一条日志")
	}
	if logs[0].TaskRunID == nil || *logs[0].TaskRunID != runID {
		t.Fatalf("TaskLog.TaskRunID 期望 %d，实际 %v", runID, logs[0].TaskRunID)
	}
}

func TestEmitLogSanitizesTaskRuntimeEvidence(t *testing.T) {
	db := openManagerTestDB(t)
	m := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)

	taskEntity := seedTaskForManagerTest(t, db)
	runID := uint(43)
	message := `执行命令: curl https://hooks.example.test/services/FAKE_TASK_LOG_TOKEN_FOR_TEST_ONLY?secret=FAKE_QUERY_FOR_TEST_ONLY && rsync /srv/private/source root@db.internal.example:/backup/tenant-a`

	m.logDispatcher.Dispatch(taskEntity.ID, &runID, "info", message, "running")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = m.Shutdown(ctx)

	var log model.TaskLog
	if err := db.Where("task_id = ?", taskEntity.ID).First(&log).Error; err != nil {
		t.Fatalf("查询任务日志失败: %v", err)
	}
	for _, forbidden := range []string{"curl", "rsync", "hooks.example.test", "FAKE_TASK_LOG_TOKEN_FOR_TEST_ONLY", "FAKE_QUERY_FOR_TEST_ONLY", "/srv/private/source", "db.internal.example", "/backup/tenant-a"} {
		if strings.Contains(log.Message, forbidden) {
			t.Fatalf("TaskLog.Message 泄露敏感片段 %q: %s", forbidden, log.Message)
		}
	}
	if !strings.Contains(log.Message, "[命令已隐藏]") {
		t.Fatalf("TaskLog.Message 缺少命令脱敏占位: %s", log.Message)
	}
}

type failingExecutor struct {
	err error
}

func (e *failingExecutor) Run(_ context.Context, _ model.Task, _ taskexec.LogFunc, _ taskexec.ProgressFunc) (int, error) {
	return 1, e.err
}

type failingRestoreExecutor struct {
	err   error
	calls int32
}

type legacyLineageSessionFake struct {
	publication.LineageSession
	mode   publication.LineageMode
	closed int32
}

func (session *legacyLineageSessionFake) Mode() publication.LineageMode {
	return session.mode
}

func (session *legacyLineageSessionFake) Close() error {
	atomic.AddInt32(&session.closed, 1)
	return nil
}

type legacyLineageGuardFake struct {
	session   publication.LineageSession
	err       error
	calls     int
	operation publication.ResticOperation
}

func (guard *legacyLineageGuardFake) Begin(_ context.Context, _ uint, operation publication.ResticOperation) (publication.LineageSession, error) {
	guard.calls++
	guard.operation = operation
	return guard.session, guard.err
}

type legacyBlockRecorderFake struct {
	blocks []publication.LegacyBlock
	err    error
}

func (recorder *legacyBlockRecorderFake) RecordLegacyBlock(_ context.Context, block publication.LegacyBlock) error {
	recorder.blocks = append(recorder.blocks, block)
	return recorder.err
}

type trackingRestoreExecutor struct {
	calls int32
	err   error
}

func (executor *trackingRestoreExecutor) Run(context.Context, model.Task, taskexec.LogFunc, taskexec.ProgressFunc) (int, error) {
	return 0, nil
}

func (executor *trackingRestoreExecutor) RunRestore(context.Context, model.Task, taskexec.LogFunc, taskexec.ProgressFunc) (int, error) {
	atomic.AddInt32(&executor.calls, 1)
	return 1, executor.err
}

var _ publication.LineageGuard = (*legacyLineageGuardFake)(nil)
var _ publication.LegacyBlockRecorder = (*legacyBlockRecorderFake)(nil)
var _ taskexec.RestoreExecutor = (*trackingRestoreExecutor)(nil)

func TestManagedResticRestoreLatestBlockedBeforeCredentialAndSSH(t *testing.T) {
	db := openManagerTestDB(t)
	restoreExecutor := &trackingRestoreExecutor{err: errors.New("restore must remain unreachable")}
	manager := NewManager(db, stubExecutorFactory{executor: restoreExecutor}, nil, nil, nil, nil, 8, 90)
	taskEntity := seedTaskForManagerTest(t, db)
	taskEntity.ExecutorType = "restic"
	runID := createTestTaskRun(t, db, taskEntity.ID, "restore")

	session := &legacyLineageSessionFake{mode: publication.LineageExact}
	guard := &legacyLineageGuardFake{session: session}
	recorder := &legacyBlockRecorderFake{}
	manager.SetLineageGuard(guard)
	manager.SetLegacyBlockRecorder(recorder)
	var precheckCalls int32
	manager.ensureRemoteTargetReadyFunc = func(context.Context, model.Node, string) error {
		atomic.AddInt32(&precheckCalls, 1)
		return nil
	}

	manager.runRestoreTask(taskEntity.ID, runID, taskEntity)

	if guard.calls != 1 || guard.operation != publication.OperationLegacyRestoreLatest {
		t.Fatalf("guard calls=%d operation=%q", guard.calls, guard.operation)
	}
	if got := atomic.LoadInt32(&precheckCalls); got != 0 {
		t.Fatalf("managed restore reached remote precheck %d time(s)", got)
	}
	if got := atomic.LoadInt32(&restoreExecutor.calls); got != 0 {
		t.Fatalf("managed restore reached executor %d time(s)", got)
	}
	if len(recorder.blocks) != 1 {
		t.Fatalf("legacy blocks=%d, want 1", len(recorder.blocks))
	}
	block := recorder.blocks[0]
	if block.TaskID != taskEntity.ID || block.TaskRunID == nil || *block.TaskRunID != runID || block.Operation != publication.OperationLegacyRestoreLatest {
		t.Fatalf("unexpected legacy block: %+v", block)
	}
	if got := atomic.LoadInt32(&session.closed); got != 1 {
		t.Fatalf("session close count=%d, want 1", got)
	}

	var run model.TaskRun
	if err := db.First(&run, runID).Error; err != nil {
		t.Fatalf("load restore run: %v", err)
	}
	if run.Status != "failed" || run.LastError != string("legacy_operation_blocked") {
		t.Fatalf("managed restore run status=%q last_error=%q", run.Status, run.LastError)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = manager.Shutdown(shutdownCtx)
}

func TestManagedRsyncRestoreLatestBlockedBeforePrecheckAndExecutor(t *testing.T) {
	db := openManagerTestDB(t)
	restoreExecutor := &trackingRestoreExecutor{err: errors.New("restore must remain unreachable")}
	manager := NewManager(db, stubExecutorFactory{executor: restoreExecutor}, nil, nil, nil, nil, 8, 90)
	taskEntity := seedTaskForManagerTest(t, db)
	taskEntity.ExecutorType = "rsync"
	runID := createTestTaskRun(t, db, taskEntity.ID, "restore")

	session := &legacyLineageSessionFake{mode: publication.LineageExact}
	guard := &legacyLineageGuardFake{session: session}
	recorder := &legacyBlockRecorderFake{}
	manager.SetLineageGuard(guard)
	manager.SetLegacyBlockRecorder(recorder)
	var precheckCalls int32
	manager.ensureRemoteTargetReadyFunc = func(context.Context, model.Node, string) error {
		atomic.AddInt32(&precheckCalls, 1)
		return nil
	}

	manager.runRestoreTask(taskEntity.ID, runID, taskEntity)

	if guard.calls != 1 || guard.operation != publication.OperationLegacyRestoreLatest {
		t.Fatalf("guard calls=%d operation=%q", guard.calls, guard.operation)
	}
	if got := atomic.LoadInt32(&precheckCalls); got != 0 {
		t.Fatalf("managed Rsync restore reached remote precheck %d time(s)", got)
	}
	if got := atomic.LoadInt32(&restoreExecutor.calls); got != 0 {
		t.Fatalf("managed Rsync restore reached executor %d time(s)", got)
	}
	if len(recorder.blocks) != 1 || recorder.blocks[0].Operation != publication.OperationLegacyRestoreLatest {
		t.Fatalf("managed Rsync restore blocks=%+v", recorder.blocks)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = manager.Shutdown(shutdownCtx)
}

func TestPristineResticRestoreLatestRetainsCompatibility(t *testing.T) {
	db := openManagerTestDB(t)
	restoreExecutor := &trackingRestoreExecutor{err: errors.New("expected compatibility restore failure")}
	manager := NewManager(db, stubExecutorFactory{executor: restoreExecutor}, nil, nil, nil, nil, 8, 90)
	taskEntity := seedTaskForManagerTest(t, db)
	taskEntity.ExecutorType = "restic"
	runID := createTestTaskRun(t, db, taskEntity.ID, "restore")

	session := &legacyLineageSessionFake{mode: publication.LineageCompatibility}
	guard := &legacyLineageGuardFake{session: session}
	recorder := &legacyBlockRecorderFake{}
	manager.SetLineageGuard(guard)
	manager.SetLegacyBlockRecorder(recorder)
	var precheckCalls int32
	manager.ensureRemoteTargetReadyFunc = func(context.Context, model.Node, string) error {
		atomic.AddInt32(&precheckCalls, 1)
		return nil
	}

	manager.runRestoreTask(taskEntity.ID, runID, taskEntity)

	if guard.calls != 1 || guard.operation != publication.OperationLegacyRestoreLatest {
		t.Fatalf("guard calls=%d operation=%q", guard.calls, guard.operation)
	}
	if got := atomic.LoadInt32(&precheckCalls); got != 1 {
		t.Fatalf("pristine restore precheck calls=%d, want 1", got)
	}
	if got := atomic.LoadInt32(&restoreExecutor.calls); got != 1 {
		t.Fatalf("pristine restore executor calls=%d, want 1", got)
	}
	if len(recorder.blocks) != 0 {
		t.Fatalf("pristine restore recorded %d legacy blocks", len(recorder.blocks))
	}
	if got := atomic.LoadInt32(&session.closed); got != 1 {
		t.Fatalf("session close count=%d, want 1", got)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = manager.Shutdown(shutdownCtx)
}

func TestManagedLegacyRestoreBlockCleansReservationMarkers(t *testing.T) {
	db := openManagerTestDB(t)
	restoreExecutor := &trackingRestoreExecutor{err: errors.New("restore must remain unreachable")}
	manager := NewManager(db, stubExecutorFactory{executor: restoreExecutor}, nil, nil, nil, nil, 8, 90)
	taskEntity := seedTaskForManagerTest(t, db)
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Update("executor_type", "restic").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.TaskRun{TaskID: taskEntity.ID, TriggerType: "manual", Status: "success"}).Error; err != nil {
		t.Fatal(err)
	}
	manager.SetNodeWriteAdmission(&nodeWriteAdmissionFake{})
	session := &legacyLineageSessionFake{mode: publication.LineageExact}
	manager.SetLineageGuard(&legacyLineageGuardFake{session: session})
	manager.SetLegacyBlockRecorder(&legacyBlockRecorderFake{})
	var precheckCalls atomic.Int32
	manager.ensureRemoteTargetReadyFunc = func(context.Context, model.Node, string) error {
		precheckCalls.Add(1)
		return nil
	}

	runID, err := manager.TriggerRestore(taskEntity.ID, "/tmp/managed-legacy-block")
	if err != nil {
		t.Fatalf("TriggerRestore: %v", err)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := manager.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}

	if _, ok := manager.pendingRuns.Load(taskEntity.ID); ok {
		t.Fatal("managed legacy restore block leaked pendingRuns marker")
	}
	if manager.isNodeRestoring(taskEntity.NodeID) {
		t.Fatal("managed legacy restore block leaked restoreNodes marker")
	}
	if got := precheckCalls.Load(); got != 0 {
		t.Fatalf("managed legacy restore block reached precheck %d time(s)", got)
	}
	if got := atomic.LoadInt32(&restoreExecutor.calls); got != 0 {
		t.Fatalf("managed legacy restore block reached executor %d time(s)", got)
	}
	var run model.TaskRun
	if err := db.First(&run, runID).Error; err != nil {
		t.Fatal(err)
	}
	if run.Status != "failed" || run.LastError != "legacy_operation_blocked" {
		t.Fatalf("managed legacy restore run status=%q last_error=%q", run.Status, run.LastError)
	}
}

type exactAnomalySinkFake struct {
	findings []anomaly.Finding
}

func (sink *exactAnomalySinkFake) Raise(_ context.Context, finding anomaly.Finding) error {
	sink.findings = append(sink.findings, finding)
	return nil
}

func TestManagerObserveCommittedDispatchesExactAnomalyBestEffort(t *testing.T) {
	db := openManagerTestDB(t)
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	taskEntity := seedTaskForManagerTest(t, db)
	taskEntity.ExecutorType = "restic"
	if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Update("executor_type", "restic").Error; err != nil {
		t.Fatal(err)
	}
	currentID := strings.Repeat("a", 64)
	previousID := strings.Repeat("b", 64)
	var calls int
	manager.SetExactAnomalyAnalyzer(func(_ context.Context, task model.Task, runID uint, current, previous string) ([]anomaly.Finding, error) {
		calls++
		if task.ID != taskEntity.ID || runID != 17 || current != currentID || previous != previousID {
			t.Fatalf("exact anomaly request task=%d run=%d current=%q previous=%q", task.ID, runID, current, previous)
		}
		return []anomaly.Finding{{NodeID: task.NodeID, Detector: "snapshot_diff", Metric: "snapshot_churn"}}, nil
	})
	sink := &exactAnomalySinkFake{}
	manager.SetAnomalySink(sink)

	manager.ObserveCommitted(context.Background(), publication.Outcome{
		TaskID: taskEntity.ID, TaskRunID: 17, State: backupasset.RecoveryPointCommitted,
		NativePointID: currentID, PreviousNativePointID: previousID,
	})

	if calls != 1 || len(sink.findings) != 1 {
		t.Fatalf("exact analyzer calls=%d findings=%+v", calls, sink.findings)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = manager.Shutdown(shutdownCtx)
}

func (e *failingRestoreExecutor) Run(_ context.Context, _ model.Task, _ taskexec.LogFunc, _ taskexec.ProgressFunc) (int, error) {
	return 0, nil
}

func (e *failingRestoreExecutor) RunRestore(_ context.Context, _ model.Task, _ taskexec.LogFunc, _ taskexec.ProgressFunc) (int, error) {
	atomic.AddInt32(&e.calls, 1)
	return 1, e.err
}

func TestRunTaskSanitizesExecutorFailureLastError(t *testing.T) {
	db := openManagerTestDB(t)
	execErr := errors.New(`backup failed for /srv/private/source to root@backup.internal.example:/repo/tenant-a via https://backup.internal.example/api?token=FAKE_EXECUTOR_TOKEN_FOR_TEST_ONLY`)
	m := NewManager(db, stubExecutorFactory{executor: &failingExecutor{err: execErr}}, nil, nil, nil, nil, 8, 90)
	taskEntity := seedTaskForManagerTest(t, db)
	runID := createTestTaskRun(t, db, taskEntity.ID, "manual")

	m.runTask(taskEntity.ID, runID, "manual", generateChainRunID())

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = m.Shutdown(ctx)

	var run model.TaskRun
	if err := db.First(&run, runID).Error; err != nil {
		t.Fatalf("查询 TaskRun 失败: %v", err)
	}
	if run.Status != "failed" {
		t.Fatalf("期望 TaskRun failed，实际 %q", run.Status)
	}
	assertTaskRuntimeTextSanitized(t, run.LastError, []string{"/srv/private/source", "backup.internal.example", "/repo/tenant-a", "FAKE_EXECUTOR_TOKEN_FOR_TEST_ONLY"})

	var updated model.Task
	if err := db.First(&updated, taskEntity.ID).Error; err != nil {
		t.Fatalf("查询 Task 失败: %v", err)
	}
	assertTaskRuntimeTextSanitized(t, updated.LastError, []string{"/srv/private/source", "backup.internal.example", "/repo/tenant-a", "FAKE_EXECUTOR_TOKEN_FOR_TEST_ONLY"})
}

func TestRunRestoreTaskSanitizesPrecheckFailureLastError(t *testing.T) {
	db := openManagerTestDB(t)
	precheckErr := errors.New(`target /srv/private/restore unavailable on restore-precheck.internal.example output=/tmp/precheck-output token=FAKE_RESTORE_PRECHECK_TOKEN_FOR_TEST_ONLY`)
	restoreExec := &failingRestoreExecutor{err: errors.New("restore executor should not run")}
	m := NewManager(db, stubExecutorFactory{executor: restoreExec}, nil, nil, nil, nil, 8, 90)
	m.ensureRemoteTargetReadyFunc = func(context.Context, model.Node, string) error {
		return precheckErr
	}
	taskEntity := seedTaskForManagerTest(t, db)
	taskEntity.RsyncSource = "/backup/private/source"
	taskEntity.RsyncTarget = "/srv/private/restore"
	runID := createTestTaskRun(t, db, taskEntity.ID, "restore")

	m.runRestoreTask(taskEntity.ID, runID, taskEntity)
	if atomic.LoadInt32(&restoreExec.calls) != 0 {
		t.Fatalf("恢复前检查失败时不应调用恢复执行器，实际调用 %d 次", atomic.LoadInt32(&restoreExec.calls))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = m.Shutdown(ctx)

	var run model.TaskRun
	if err := db.First(&run, runID).Error; err != nil {
		t.Fatalf("查询 TaskRun 失败: %v", err)
	}
	if run.Status != "failed" {
		t.Fatalf("期望恢复前检查 TaskRun failed，实际 %q", run.Status)
	}
	assertTaskRuntimeTextSanitized(t, run.LastError, []string{"/srv/private/restore", "/tmp/precheck-output", "restore-precheck.internal.example", "FAKE_RESTORE_PRECHECK_TOKEN_FOR_TEST_ONLY"})

	var logs []model.TaskLog
	if err := db.Where("task_run_id = ?", runID).Find(&logs).Error; err != nil {
		t.Fatalf("查询恢复日志失败: %v", err)
	}
	if len(logs) == 0 {
		t.Fatalf("期望恢复前检查失败写入任务日志")
	}
	for _, logEntry := range logs {
		assertTaskRuntimeTextSanitized(t, logEntry.Message, []string{"/srv/private/restore", "/tmp/precheck-output", "restore-precheck.internal.example", "FAKE_RESTORE_PRECHECK_TOKEN_FOR_TEST_ONLY"})
	}
}

func TestRunRestoreTaskSanitizesRestoreFailureLastError(t *testing.T) {
	db := openManagerTestDB(t)
	restoreErr := errors.New(`restore failed from /backup/private/source to /srv/private/restore on restore.internal.example via https://restore.internal.example/api?token=FAKE_RESTORE_TOKEN_FOR_TEST_ONLY`)
	restoreExec := &failingRestoreExecutor{err: restoreErr}
	m := NewManager(db, stubExecutorFactory{executor: restoreExec}, nil, nil, nil, nil, 8, 90)
	m.ensureRemoteTargetReadyFunc = func(context.Context, model.Node, string) error {
		return nil
	}
	taskEntity := seedTaskForManagerTest(t, db)
	taskEntity.RsyncSource = "/backup/private/source"
	taskEntity.RsyncTarget = "/srv/private/restore"
	runID := createTestTaskRun(t, db, taskEntity.ID, "restore")

	m.runRestoreTask(taskEntity.ID, runID, taskEntity)
	if atomic.LoadInt32(&restoreExec.calls) != 1 {
		t.Fatalf("期望执行恢复执行器失败路径，实际调用 %d 次", atomic.LoadInt32(&restoreExec.calls))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = m.Shutdown(ctx)

	var run model.TaskRun
	if err := db.First(&run, runID).Error; err != nil {
		t.Fatalf("查询 TaskRun 失败: %v", err)
	}
	if run.Status != "failed" {
		t.Fatalf("期望恢复 TaskRun failed，实际 %q", run.Status)
	}
	assertTaskRuntimeTextSanitized(t, run.LastError, []string{"/backup/private/source", "/srv/private/restore", "restore.internal.example", "FAKE_RESTORE_TOKEN_FOR_TEST_ONLY"})

	var logs []model.TaskLog
	if err := db.Where("task_run_id = ?", runID).Find(&logs).Error; err != nil {
		t.Fatalf("查询恢复日志失败: %v", err)
	}
	if len(logs) == 0 {
		t.Fatalf("期望恢复失败写入任务日志")
	}
	for _, logEntry := range logs {
		assertTaskRuntimeTextSanitized(t, logEntry.Message, []string{"/backup/private/source", "/srv/private/restore", "restore.internal.example", "FAKE_RESTORE_TOKEN_FOR_TEST_ONLY"})
	}
}

func TestMaintenanceMessagesSanitizeRuntimeEvidence(t *testing.T) {
	db := openManagerTestDB(t)
	m := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)

	retentionErr := sanitizeTaskLastError(`restic 保留清理失败: remove /srv/private/repo on backup.internal.example token=FAKE_RETENTION_ALERT_TOKEN_FOR_TEST_ONLY, 输出: /tmp/raw-output`)
	m.logDispatcher.Dispatch(0, nil, "error", retentionErr, "")
	_ = alerting.RaiseRetentionFailure(db, 7, "policy-runtime", "node-runtime", 9, retentionErr)

	integrityErr := sanitizeTaskLastError(`rclone 完整性检查失败: compare /srv/private/source to s3:tenant/private on integrity.internal.example token=FAKE_INTEGRITY_ALERT_TOKEN_FOR_TEST_ONLY, 输出: /tmp/raw-output`)
	m.logDispatcher.Dispatch(0, nil, "error", integrityErr, "")
	_ = alerting.RaiseIntegrityCheckFailure(db, 8, "policy-runtime", "node-runtime", 10, integrityErr)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = m.Shutdown(ctx)

	var logs []model.TaskLog
	if err := db.Find(&logs).Error; err != nil {
		t.Fatalf("查询维护日志失败: %v", err)
	}
	if len(logs) != 2 {
		t.Fatalf("期望写入 2 条维护日志，实际 %d", len(logs))
	}
	for _, logEntry := range logs {
		assertTaskRuntimeTextSanitized(t, logEntry.Message, []string{"/srv/private/repo", "/srv/private/source", "s3:tenant/private", "backup.internal.example", "integrity.internal.example", "FAKE_RETENTION_ALERT_TOKEN_FOR_TEST_ONLY", "FAKE_INTEGRITY_ALERT_TOKEN_FOR_TEST_ONLY", "/tmp/raw-output"})
	}

	var alerts []model.Alert
	if err := db.Find(&alerts).Error; err != nil {
		t.Fatalf("查询维护告警失败: %v", err)
	}
	if len(alerts) != 2 {
		t.Fatalf("期望写入 2 条维护告警，实际 %d", len(alerts))
	}
	for _, alert := range alerts {
		assertTaskRuntimeTextSanitized(t, alert.Message, []string{"/srv/private/repo", "/srv/private/source", "s3:tenant/private", "backup.internal.example", "integrity.internal.example", "FAKE_RETENTION_ALERT_TOKEN_FOR_TEST_ONLY", "FAKE_INTEGRITY_ALERT_TOKEN_FOR_TEST_ONLY", "/tmp/raw-output"})
	}
}

// TestRestoreBlockedByInFlightNormalTask 验证即使普通任务已经进入 runTask() 但尚未将
// 自身状态更新为 running，restore 触发仍会被节点级互斥阻塞（无 TOCTOU 竞态窗口）。
func TestRestoreBlockedByInFlightNormalTask(t *testing.T) {
	db := openManagerTestDB(t)
	exec := newBlockingExecutor()
	m := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, nil, 8, 90)

	t1, t2 := seedTwoTasksSameNode(t, db)

	// task2 需要有成功记录才能触发恢复
	db.Create(&model.TaskRun{TaskID: t2.ID, TriggerType: "manual", Status: "success"})

	// 触发 task1（普通任务），等待它进入 executor（此时 Task.Status 已更新为 running）
	_, err := m.TriggerManual(t1.ID)
	if err != nil {
		t.Fatalf("触发普通任务失败: %v", err)
	}
	select {
	case <-exec.started:
	case <-time.After(3 * time.Second):
		t.Fatal("等待普通任务开始执行超时")
	}

	// 普通任务正在运行，尝试对同节点另一个任务触发恢复 — 应被 DB 冲突查询阻塞
	_, err = m.TriggerRestore(t2.ID, "")
	if err == nil {
		t.Fatal("同节点有普通任务运行时，恢复应被阻塞")
	}
	if !strings.Contains(err.Error(), "任务正在运行") {
		t.Fatalf("错误信息应提及任务正在运行，实际: %v", err)
	}

	// 释放普通任务
	close(exec.release)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = m.Shutdown(ctx)
}

// seedTwoTasksSameNode 创建同节点、不同策略的两个 rsync 任务，用于互斥测试。
func seedTwoTasksSameNode(t *testing.T, db *gorm.DB) (model.Task, model.Task) {
	t.Helper()
	node := model.Node{Name: "node-mutex-test", Host: "127.0.0.1", Port: 22, Username: "root", AuthType: "key"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}
	p1 := model.Policy{Name: "policy-mutex-1", SourcePath: "/src1", TargetPath: "/dst1", CronSpec: "@daily"}
	p2 := model.Policy{Name: "policy-mutex-2", SourcePath: "/src2", TargetPath: "/dst2", CronSpec: "@daily"}
	db.Create(&p1)
	db.Create(&p2)

	t1 := model.Task{Name: "t-mutex-1", NodeID: node.ID, ExecutorType: "rsync", Status: string(StatusPending), RsyncSource: "/src1", RsyncTarget: "/dst1", PolicyID: &p1.ID}
	t2 := model.Task{Name: "t-mutex-2", NodeID: node.ID, ExecutorType: "rsync", Status: string(StatusPending), RsyncSource: "/src2", RsyncTarget: "/dst2", PolicyID: &p2.ID}
	db.Create(&t1)
	db.Create(&t2)
	return t1, t2
}

// TestRestoreNodeMutexBlocksNormalTask 验证恢复期间同节点的普通任务被阻塞。
func TestRestoreNodeMutexBlocksNormalTask(t *testing.T) {
	db := openManagerTestDB(t)
	exec := &successExecutor{}
	m := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, nil, 8, 90)

	t1, t2 := seedTwoTasksSameNode(t, db)

	// 模拟 task1 有恢复任务正在运行
	m.restoreNodes.Store(t1.NodeID, t1.ID)

	// 触发 task2（同节点不同策略）应被阻塞
	_, err := m.TriggerManual(t2.ID)
	if err == nil {
		t.Fatal("同节点有恢复任务时，普通任务应被阻塞")
	}
	if !strings.Contains(err.Error(), "恢复任务正在运行") {
		t.Fatalf("错误信息应提及恢复任务，实际: %v", err)
	}

	// 恢复完成，解除节点互斥
	m.restoreNodes.Delete(t1.NodeID)

	// 现在触发应成功
	runID, err := m.TriggerManual(t2.ID)
	if err != nil {
		t.Fatalf("恢复完成后触发应成功: %v", err)
	}
	if runID == 0 {
		t.Fatal("期望返回非零 runID")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = m.Shutdown(ctx)
}

// TestRestoreNodeMutexBlocksConcurrentRestore 验证同节点不允许并发恢复。
func TestRestoreNodeMutexBlocksConcurrentRestore(t *testing.T) {
	db := openManagerTestDB(t)
	exec := &successExecutor{}
	m := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, nil, 8, 90)

	t1, t2 := seedTwoTasksSameNode(t, db)

	// task1 和 task2 都需要有成功记录才能触发恢复
	db.Create(&model.TaskRun{TaskID: t1.ID, TriggerType: "manual", Status: "success"})
	db.Create(&model.TaskRun{TaskID: t2.ID, TriggerType: "manual", Status: "success"})

	// 模拟 task1 有恢复正在运行
	m.restoreNodes.Store(t1.NodeID, t1.ID)
	m.pendingRuns.Store(t1.ID, struct{}{}) // 标记 task1 正在执行

	// 尝试对 task2 触发恢复 — 应因节点互斥被拒绝
	_, err := m.TriggerRestore(t2.ID, "")
	if err == nil {
		t.Fatal("同节点已有恢复时，另一个恢复应被阻塞")
	}
	if !strings.Contains(err.Error(), "恢复任务正在运行") {
		t.Fatalf("错误信息应提及恢复任务，实际: %v", err)
	}

	m.restoreNodes.Delete(t1.NodeID)
	m.pendingRuns.Delete(t1.ID)
}

// TestRestoreNodeMutexRegisteredSynchronously 验证节点互斥在 TriggerRestore 同步返回时即已生效，
// 不存在触发到 goroutine 启动之间的竞态窗口。
func TestRestoreNodeMutexRegisteredSynchronously(t *testing.T) {
	db := openManagerTestDB(t)
	exec := &successExecutor{}
	m := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, nil, 8, 90)

	t1, t2 := seedTwoTasksSameNode(t, db)

	// task1 需要有成功记录才能触发恢复
	db.Create(&model.TaskRun{TaskID: t1.ID, TriggerType: "manual", Status: "success"})

	// 填满 semaphore，使 restore goroutine 阻塞在排队阶段
	for i := 0; i < cap(m.semaphore); i++ {
		m.semaphore <- struct{}{}
	}

	// 触发恢复 — goroutine 会阻塞在 semaphore，但 restoreNodes 已在同步路径中注册
	_, err := m.TriggerRestore(t1.ID, "/tmp/restore-test")
	if err != nil {
		t.Fatalf("触发恢复失败: %v", err)
	}

	// TriggerRestore 同步返回后立即断言（无 sleep），节点已标记为正在恢复
	if !m.isNodeRestoring(t1.NodeID) {
		t.Fatal("TriggerRestore 返回后，节点应立即标记为正在恢复（无竞态窗口）")
	}

	// 同节点普通任务应被阻塞
	_, err = m.TriggerManual(t2.ID)
	if err == nil {
		t.Fatal("恢复排队期间，同节点普通任务应被阻塞")
	}
	if !strings.Contains(err.Error(), "恢复任务正在运行") {
		t.Fatalf("错误信息应提及恢复任务，实际: %v", err)
	}

	// 取消恢复任务并释放 semaphore
	_ = m.Cancel(t1.ID)
	for i := 0; i < cap(m.semaphore); i++ {
		<-m.semaphore
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = m.Shutdown(ctx)

	// 恢复取消后，节点应不再标记
	if m.isNodeRestoring(t1.NodeID) {
		t.Fatal("恢复取消后，节点不应再标记为正在恢复")
	}
}
