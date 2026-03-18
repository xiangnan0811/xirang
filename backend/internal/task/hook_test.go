package task

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

// createPolicyForHookTest 创建策略。
// 注意：不设置 RsyncSource/RsyncTarget 可使校验器快速返回 passed，
// 因此测试中任务的 RsyncSource/RsyncTarget 留空以避免 SSH 校验。
func createPolicyForHookTest(t *testing.T, db *gorm.DB, policy model.Policy) model.Policy {
	t.Helper()
	if err := db.Create(&policy).Error; err != nil {
		t.Fatalf("创建策略失败: %v", err)
	}
	return policy
}

func TestPreHookSuccess(t *testing.T) {
	db := openManagerTestDB(t)
	exec := &successExecutor{}
	m := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, 8, 90)

	// 注入成功的 hook 函数
	var hookCalls int32
	m.hookRunFunc = func(_ context.Context, _ model.Task, command string) error {
		atomic.AddInt32(&hookCalls, 1)
		return nil
	}

	// 创建带 pre-hook 策略的任务
	node := model.Node{Name: "node-hook-pre-ok", Host: "127.0.0.1", Port: 22, Username: "root", AuthType: "key"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}
	policy := createPolicyForHookTest(t, db, model.Policy{
		Name:               "policy-hook-pre-ok",
		SourcePath:         "/tmp/src",
		TargetPath:         "/tmp/dst",
		CronSpec:           "",
		PreHook:            "echo pre",
		HookTimeoutSeconds: 60,
	})
	task := model.Task{
		Name:         "task-hook-pre-ok",
		NodeID:       node.ID,
		ExecutorType: "rsync",
		Status:       string(StatusPending),
		PolicyID:     &policy.ID,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	runID := createTestTaskRun(t, db, task.ID, "manual")
	m.runTask(task.ID, runID, "manual", generateChainRunID())

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = m.Shutdown(ctx)

	// pre-hook 应被调用一次
	if atomic.LoadInt32(&hookCalls) != 1 {
		t.Fatalf("pre-hook 应调用 1 次，实际: %d", atomic.LoadInt32(&hookCalls))
	}

	// 主执行器应执行成功
	if exec.Calls() != 1 {
		t.Fatalf("执行器应调用 1 次，实际: %d", exec.Calls())
	}

	// 任务状态应为 success
	var updated model.Task
	db.First(&updated, task.ID)
	if updated.Status != string(StatusSuccess) {
		t.Fatalf("任务状态应为 success，实际: %s", updated.Status)
	}

	// TaskRun 状态应为 success
	var run model.TaskRun
	db.First(&run, runID)
	if run.Status != "success" {
		t.Fatalf("TaskRun 状态应为 success，实际: %s", run.Status)
	}
}

func TestPreHookFailure(t *testing.T) {
	db := openManagerTestDB(t)
	exec := &successExecutor{}
	m := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, 8, 90)

	// 注入失败的 hook 函数
	m.hookRunFunc = func(_ context.Context, _ model.Task, command string) error {
		return fmt.Errorf("pre-hook 执行出错: exit status 1")
	}

	node := model.Node{Name: "node-hook-pre-fail", Host: "127.0.0.1", Port: 22, Username: "root", AuthType: "key"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}
	policy := createPolicyForHookTest(t, db, model.Policy{
		Name:               "policy-hook-pre-fail",
		SourcePath:         "/tmp/src",
		TargetPath:         "/tmp/dst",
		CronSpec:           "",
		PreHook:            "false",
		HookTimeoutSeconds: 60,
	})
	task := model.Task{
		Name:         "task-hook-pre-fail",
		NodeID:       node.ID,
		ExecutorType: "rsync",
		Status:       string(StatusPending),
		PolicyID:     &policy.ID,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	runID := createTestTaskRun(t, db, task.ID, "manual")
	m.runTask(task.ID, runID, "manual", generateChainRunID())

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = m.Shutdown(ctx)

	// 主执行器不应被调用（pre-hook 失败应中断流程）
	if exec.Calls() != 0 {
		t.Fatalf("pre-hook 失败后执行器不应被调用，实际: %d 次", exec.Calls())
	}

	// 任务状态应为 failed
	var updated model.Task
	db.First(&updated, task.ID)
	if updated.Status != string(StatusFailed) {
		t.Fatalf("任务状态应为 failed，实际: %s", updated.Status)
	}
	if updated.LastError == "" {
		t.Fatalf("任务 LastError 不应为空")
	}

	// TaskRun 状态应为 failed
	var run model.TaskRun
	db.First(&run, runID)
	if run.Status != "failed" {
		t.Fatalf("TaskRun 状态应为 failed，实际: %s", run.Status)
	}
	if run.LastError == "" {
		t.Fatalf("TaskRun.LastError 不应为空")
	}
	if run.FinishedAt == nil {
		t.Fatalf("TaskRun.FinishedAt 不应为空")
	}
}

func TestPreHookTimeout(t *testing.T) {
	db := openManagerTestDB(t)
	exec := &successExecutor{}
	m := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, 8, 90)

	// 注入阻塞的 hook 函数（会被 context 超时取消）
	m.hookRunFunc = func(ctx context.Context, _ model.Task, command string) error {
		select {
		case <-ctx.Done():
			return fmt.Errorf("钩子执行失败: %w", ctx.Err())
		case <-time.After(30 * time.Second):
			return nil
		}
	}

	node := model.Node{Name: "node-hook-pre-timeout", Host: "127.0.0.1", Port: 22, Username: "root", AuthType: "key"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}
	policy := createPolicyForHookTest(t, db, model.Policy{
		Name:               "policy-hook-pre-timeout",
		SourcePath:         "/tmp/src",
		TargetPath:         "/tmp/dst",
		CronSpec:           "",
		PreHook:            "sleep 999",
		HookTimeoutSeconds: 1, // 1 秒超时
	})
	task := model.Task{
		Name:         "task-hook-pre-timeout",
		NodeID:       node.ID,
		ExecutorType: "rsync",
		Status:       string(StatusPending),
		PolicyID:     &policy.ID,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	runID := createTestTaskRun(t, db, task.ID, "manual")

	start := time.Now()
	m.runTask(task.ID, runID, "manual", generateChainRunID())
	elapsed := time.Since(start)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = m.Shutdown(ctx)

	// 应在超时时间附近完成（1 秒超时 + 合理余量），不应等 30 秒
	if elapsed > 5*time.Second {
		t.Fatalf("pre-hook 超时应在 ~1 秒内中断，实际耗时: %v", elapsed)
	}

	// 主执行器不应被调用
	if exec.Calls() != 0 {
		t.Fatalf("pre-hook 超时后执行器不应被调用，实际: %d 次", exec.Calls())
	}

	// 任务状态应为 failed
	var updated model.Task
	db.First(&updated, task.ID)
	if updated.Status != string(StatusFailed) {
		t.Fatalf("任务状态应为 failed，实际: %s", updated.Status)
	}

	// TaskRun 状态应为 failed
	var run model.TaskRun
	db.First(&run, runID)
	if run.Status != "failed" {
		t.Fatalf("TaskRun 状态应为 failed，实际: %s", run.Status)
	}
}

func TestPostHookFailureDoesNotAffectTaskStatus(t *testing.T) {
	db := openManagerTestDB(t)
	exec := &successExecutor{}
	m := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, 8, 90)

	// 注入 post-hook 失败的函数
	var hookCalls int32
	m.hookRunFunc = func(_ context.Context, _ model.Task, command string) error {
		atomic.AddInt32(&hookCalls, 1)
		return fmt.Errorf("post-hook 执行出错: exit status 2")
	}

	node := model.Node{Name: "node-hook-post-fail", Host: "127.0.0.1", Port: 22, Username: "root", AuthType: "key"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}
	policy := createPolicyForHookTest(t, db, model.Policy{
		Name:               "policy-hook-post-fail",
		SourcePath:         "/tmp/src",
		TargetPath:         "/tmp/dst",
		CronSpec:           "",
		PostHook:           "false",
		HookTimeoutSeconds: 60,
	})
	task := model.Task{
		Name:         "task-hook-post-fail",
		NodeID:       node.ID,
		ExecutorType: "rsync",
		Status:       string(StatusPending),
		PolicyID:     &policy.ID,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	runID := createTestTaskRun(t, db, task.ID, "manual")
	m.runTask(task.ID, runID, "manual", generateChainRunID())

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = m.Shutdown(ctx)

	// 主执行器应被调用
	if exec.Calls() != 1 {
		t.Fatalf("执行器应调用 1 次，实际: %d", exec.Calls())
	}

	// post-hook 应被调用
	if atomic.LoadInt32(&hookCalls) != 1 {
		t.Fatalf("post-hook 应调用 1 次，实际: %d", atomic.LoadInt32(&hookCalls))
	}

	// 任务状态应为 success（post-hook 失败不影响）
	var updated model.Task
	db.First(&updated, task.ID)
	if updated.Status != string(StatusSuccess) {
		t.Fatalf("post-hook 失败不应影响任务状态，期望 success，实际: %s", updated.Status)
	}

	// TaskRun 状态应为 success
	var run model.TaskRun
	db.First(&run, runID)
	if run.Status != "success" {
		t.Fatalf("TaskRun 状态应为 success，实际: %s", run.Status)
	}
}

func TestEmptyHookIsNoOp(t *testing.T) {
	db := openManagerTestDB(t)
	exec := &successExecutor{}
	m := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, 8, 90)

	// 注入 hook 函数——不应被调用
	var hookCalls int32
	m.hookRunFunc = func(_ context.Context, _ model.Task, command string) error {
		atomic.AddInt32(&hookCalls, 1)
		return fmt.Errorf("不应到达此处")
	}

	node := model.Node{Name: "node-hook-empty", Host: "127.0.0.1", Port: 22, Username: "root", AuthType: "key"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}
	policy := createPolicyForHookTest(t, db, model.Policy{
		Name:               "policy-hook-empty",
		SourcePath:         "/tmp/src",
		TargetPath:         "/tmp/dst",
		CronSpec:           "",
		PreHook:            "",
		PostHook:           "",
		HookTimeoutSeconds: 60,
	})
	task := model.Task{
		Name:         "task-hook-empty",
		NodeID:       node.ID,
		ExecutorType: "rsync",
		Status:       string(StatusPending),
		PolicyID:     &policy.ID,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	runID := createTestTaskRun(t, db, task.ID, "manual")
	m.runTask(task.ID, runID, "manual", generateChainRunID())

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = m.Shutdown(ctx)

	// hook 不应被调用
	if atomic.LoadInt32(&hookCalls) != 0 {
		t.Fatalf("空钩子不应触发 hookRunFunc，实际调用: %d 次", atomic.LoadInt32(&hookCalls))
	}

	// 主执行器应正常执行
	if exec.Calls() != 1 {
		t.Fatalf("执行器应调用 1 次，实际: %d", exec.Calls())
	}

	// 任务状态应为 success
	var updated model.Task
	db.First(&updated, task.ID)
	if updated.Status != string(StatusSuccess) {
		t.Fatalf("任务状态应为 success，实际: %s", updated.Status)
	}
}

func TestNoPolicySkipsHooks(t *testing.T) {
	db := openManagerTestDB(t)
	exec := &successExecutor{}
	m := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, 8, 90)

	// 注入 hook 函数——无策略时不应被调用
	var hookCalls int32
	m.hookRunFunc = func(_ context.Context, _ model.Task, command string) error {
		atomic.AddInt32(&hookCalls, 1)
		return fmt.Errorf("不应到达此处")
	}

	// 使用无策略的任务（seedTaskForManagerTest 创建的任务没有 PolicyID）
	taskEntity := seedTaskForManagerTest(t, db)

	runID := createTestTaskRun(t, db, taskEntity.ID, "manual")
	m.runTask(taskEntity.ID, runID, "manual", generateChainRunID())

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = m.Shutdown(ctx)

	// hook 不应被调用
	if atomic.LoadInt32(&hookCalls) != 0 {
		t.Fatalf("无策略时不应触发 hookRunFunc，实际调用: %d 次", atomic.LoadInt32(&hookCalls))
	}

	// 任务正常完成
	if exec.Calls() != 1 {
		t.Fatalf("执行器应调用 1 次，实际: %d", exec.Calls())
	}
}

func TestPreHookDefaultTimeout(t *testing.T) {
	db := openManagerTestDB(t)
	exec := &successExecutor{}
	m := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, 8, 90)

	// 注入成功 hook（验证 HookTimeoutSeconds=0 时使用默认 5 分钟超时，
	// 此处只验证 hook 被调用且正常通过）
	var hookCalls int32
	m.hookRunFunc = func(_ context.Context, _ model.Task, command string) error {
		atomic.AddInt32(&hookCalls, 1)
		return nil
	}

	node := model.Node{Name: "node-hook-default-timeout", Host: "127.0.0.1", Port: 22, Username: "root", AuthType: "key"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}
	policy := createPolicyForHookTest(t, db, model.Policy{
		Name:               "policy-hook-default-timeout",
		SourcePath:         "/tmp/src",
		TargetPath:         "/tmp/dst",
		CronSpec:           "",
		PreHook:            "echo check-timeout",
		HookTimeoutSeconds: 0, // 触发默认超时逻辑
	})
	task := model.Task{
		Name:         "task-hook-default-timeout",
		NodeID:       node.ID,
		ExecutorType: "rsync",
		Status:       string(StatusPending),
		PolicyID:     &policy.ID,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	runID := createTestTaskRun(t, db, task.ID, "manual")
	m.runTask(task.ID, runID, "manual", generateChainRunID())

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = m.Shutdown(ctx)

	// hook 应正常调用（HookTimeoutSeconds=0 使用默认 5 分钟，不会超时）
	if atomic.LoadInt32(&hookCalls) != 1 {
		t.Fatalf("HookTimeoutSeconds=0 时 hook 仍应被调用，实际: %d", atomic.LoadInt32(&hookCalls))
	}

	var updated model.Task
	db.First(&updated, task.ID)
	if updated.Status != string(StatusSuccess) {
		t.Fatalf("任务状态应为 success，实际: %s", updated.Status)
	}
}

func TestBothPreAndPostHooksExecuted(t *testing.T) {
	db := openManagerTestDB(t)
	exec := &successExecutor{}
	m := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, 8, 90)

	// 记录 hook 调用顺序
	var hookCommands []string
	m.hookRunFunc = func(_ context.Context, _ model.Task, command string) error {
		hookCommands = append(hookCommands, command)
		return nil
	}

	node := model.Node{Name: "node-hook-both", Host: "127.0.0.1", Port: 22, Username: "root", AuthType: "key"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}
	policy := createPolicyForHookTest(t, db, model.Policy{
		Name:               "policy-hook-both",
		SourcePath:         "/tmp/src",
		TargetPath:         "/tmp/dst",
		CronSpec:           "",
		PreHook:            "echo pre-hook",
		PostHook:           "echo post-hook",
		HookTimeoutSeconds: 60,
	})
	task := model.Task{
		Name:         "task-hook-both",
		NodeID:       node.ID,
		ExecutorType: "rsync",
		Status:       string(StatusPending),
		PolicyID:     &policy.ID,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	runID := createTestTaskRun(t, db, task.ID, "manual")
	m.runTask(task.ID, runID, "manual", generateChainRunID())

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = m.Shutdown(ctx)

	// 应先执行 pre-hook 再执行 post-hook
	if len(hookCommands) != 2 {
		t.Fatalf("应调用 2 次 hook，实际: %d", len(hookCommands))
	}
	if hookCommands[0] != "echo pre-hook" {
		t.Fatalf("第一个 hook 应为 pre-hook 命令，实际: %s", hookCommands[0])
	}
	if hookCommands[1] != "echo post-hook" {
		t.Fatalf("第二个 hook 应为 post-hook 命令，实际: %s", hookCommands[1])
	}

	// 任务应成功完成
	var updated model.Task
	db.First(&updated, task.ID)
	if updated.Status != string(StatusSuccess) {
		t.Fatalf("任务状态应为 success，实际: %s", updated.Status)
	}
}
