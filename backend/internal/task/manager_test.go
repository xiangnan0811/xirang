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
	if err := db.AutoMigrate(&model.SSHKey{}, &model.Node{}, &model.Policy{}, &model.Task{}, &model.TaskLog{}, &model.Alert{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}
	if err := db.AutoMigrate(&model.TaskTrafficSample{}); err != nil {
		t.Fatalf("初始化采样表失败: %v", err)
	}
	return db
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
	m := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, 8)

	taskEntity := seedTaskForManagerTest(t, db)
	m.runTask(taskEntity.ID, "manual")

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
	m := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, 8)
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
			resultCh <- m.TriggerManual(taskEntity.ID)
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
	m := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, 8)

	m.runTask(taskEntity.ID, "manual")

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
