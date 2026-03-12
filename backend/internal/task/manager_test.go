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
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.SSHKey{}, &model.Node{}, &model.Policy{}, &model.Task{}, &model.TaskRun{}, &model.TaskLog{}, &model.Alert{}); err != nil {
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

func TestRunTaskKeepsLockEntries(t *testing.T) {
	db := openManagerTestDB(t)
	exec := &successExecutor{}
	m := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, 8, 90)

	taskEntity := seedTaskForManagerTest(t, db)
	runID := createTestTaskRun(t, db, taskEntity.ID, "manual")
	m.runTask(taskEntity.ID, runID, "manual")

	if exec.Calls() != 1 {
		t.Fatalf("期望执行器调用 1 次，实际: %d", exec.Calls())
	}

	if _, ok := m.locks.Load(taskEntity.ID); !ok {
		t.Fatalf("期望任务锁条目保留，实际已删除")
	}
	strategyKey := buildStrategyKey(taskEntity.NodeID, taskEntity.PolicyID)
	if _, ok := m.strategyLocks.Load(strategyKey); !ok {
		t.Fatalf("期望策略锁条目保留，实际已删除")
	}
}

func TestTriggerManualRejectsConcurrentDuplicate(t *testing.T) {
	db := openManagerTestDB(t)
	exec := newBlockingExecutor()
	m := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, 8, 90)
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
	m := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, 8, 90)

	runID := createTestTaskRun(t, db, taskEntity.ID, "manual")
	m.runTask(taskEntity.ID, runID, "manual")

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
	m := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, 8, 90)
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
	m := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, 8, 90)
	taskEntity := seedTaskForManagerTest(t, db)

	runID := createTestTaskRun(t, db, taskEntity.ID, "manual")
	m.runTask(taskEntity.ID, runID, "manual")

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
	m := NewManager(db, stubExecutorFactory{executor: failExec}, nil, nil, 8, 90)
	taskEntity := seedTaskForManagerTest(t, db)

	runID := createTestTaskRun(t, db, taskEntity.ID, "manual")
	m.runTask(taskEntity.ID, runID, "manual")

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
	m := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, 8, 90)
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
	m := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, 8, 1) // 1 天保留

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
	m := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, 8, 90)

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
