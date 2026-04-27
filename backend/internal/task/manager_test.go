package task

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/model"
	taskexec "xirang/backend/internal/task/executor"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type stubExecutorFactory struct {
	executor taskexec.Executor
}

func (f stubExecutorFactory) Resolve(_ string) taskexec.Executor {
	return f.executor
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
	// 关键：不用 cache=shared + 命名 file，原实现导致两个 flake：
	//   1) Manager 的后台 goroutine 与测试主线程并发写同一内存库 →
	//      SQLite 单写者锁默认立即返回 "database table is locked"，
	//      CI 上观察到 TestPreHookTimeout 偶发断言失败。
	//   2) 同一进程内 go test -count=N 重复跑同名测试时，命名 file 复用
	//      同一份内存库，残留数据触发 UNIQUE constraint。
	// 改用纯 ":memory:" + SetMaxOpenConns(1)：每次调用得到全新的私有库，
	// 单连接彻底串行化所有写入；_busy_timeout 作为兜底应对偶发竞争。
	db, err := gorm.Open(sqlite.Open("file::memory:?_busy_timeout=5000"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("获取底层连接失败: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&model.SSHKey{}, &model.Node{}, &model.Policy{}, &model.Task{}, &model.TaskRun{}, &model.TaskLog{}, &model.Alert{}, &model.Integration{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}
	if err := db.AutoMigrate(&model.TaskTrafficSample{}); err != nil {
		t.Fatalf("初始化采样表失败: %v", err)
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
	m := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, 8, 90)

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

func TestTriggerManualRejectsConcurrentDuplicate(t *testing.T) {
	db := openManagerTestDB(t)
	exec := newBlockingExecutor()
	m := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, 8, 90)
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
	m := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, 8, 90)

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
	m := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, 8, 90)
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

func TestRunTaskDualWriteSuccess(t *testing.T) {
	db := openManagerTestDB(t)
	exec := &successExecutor{}
	m := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, 8, 90)
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
	m := NewManager(db, stubExecutorFactory{executor: failExec}, nil, nil, nil, 8, 90)
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

func TestCancelUpdatesTaskRunToCanceled(t *testing.T) {
	db := openManagerTestDB(t)
	exec := newBlockingExecutor()
	m := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, 8, 90)
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
	m := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, 8, 1) // 1 天保留

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
	m := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, 8, 90)

	taskEntity := seedTaskForManagerTest(t, db)
	runID := uint(42)

	// 直接调用 emitLog
	m.emitLog(taskEntity.ID, &runID, "info", "test message", "running")

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

type failingExecutor struct {
	err error
}

func (e *failingExecutor) Run(_ context.Context, _ model.Task, _ taskexec.LogFunc, _ taskexec.ProgressFunc) (int, error) {
	return 1, e.err
}

// TestRestoreBlockedByInFlightNormalTask 验证即使普通任务已经进入 runTask() 但尚未将
// 自身状态更新为 running，restore 触发仍会被节点级互斥阻塞（无 TOCTOU 竞态窗口）。
func TestRestoreBlockedByInFlightNormalTask(t *testing.T) {
	db := openManagerTestDB(t)
	exec := newBlockingExecutor()
	m := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, 8, 90)

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
	m := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, 8, 90)

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
	m := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, 8, 90)

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
	m := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, 8, 90)

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
