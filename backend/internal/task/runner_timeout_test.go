package task

import (
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/model"
)

// withGlobalTimeout 临时覆盖 globalTaskTimeoutOverride，t.Cleanup 自动恢复。
// 避免污染同包其他测试。
func withGlobalTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	prev := globalTaskTimeoutOverride
	globalTaskTimeoutOverride = d
	t.Cleanup(func() {
		globalTaskTimeoutOverride = prev
	})
}

// TestRunTaskHonorsGlobalTimeout 验证全局超时（globalTaskTimeoutOverride 模拟
// TASK_MAX_EXECUTION_SECONDS 环境变量场景）能强制中止卡死的 executor，
// TaskRun 状态置为 failed 且 last_error 含"超时"字样。
func TestRunTaskHonorsGlobalTimeout(t *testing.T) {
	withGlobalTimeout(t, 150*time.Millisecond)

	db := openManagerTestDB(t)
	exec := newBlockingExecutor() // 永不 release，只能被 ctx 中断
	m := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, 8, 90)

	taskEntity := seedTaskForManagerTest(t, db)
	runID := createTestTaskRun(t, db, taskEntity.ID, "manual")

	start := time.Now()
	m.runTask(taskEntity.ID, runID, "manual", generateChainRunID())
	elapsed := time.Since(start)

	// 应在 ~150ms 后超时退出，加上一些调度余量
	if elapsed > 2*time.Second {
		t.Fatalf("超时未生效，耗时 %v 远超预期 150ms", elapsed)
	}
	if elapsed < 100*time.Millisecond {
		t.Fatalf("耗时 %v 比超时窗口还短，可能没真正调用 executor", elapsed)
	}

	var run model.TaskRun
	if err := db.First(&run, runID).Error; err != nil {
		t.Fatalf("读取 TaskRun 失败: %v", err)
	}
	if run.Status != "failed" {
		t.Fatalf("超时后 TaskRun.Status 期望 failed，实际 %q", run.Status)
	}
	if !strings.Contains(run.LastError, "超时") {
		t.Fatalf("TaskRun.LastError 期望含 \"超时\"，实际 %q", run.LastError)
	}
}

// TestRunTaskHonorsPolicyTimeout 验证 Policy.MaxExecutionSeconds 优先于全局超时。
// 全局设 10s（不会触发），Policy 设 1s（实测期望被触发）。
func TestRunTaskHonorsPolicyTimeout(t *testing.T) {
	withGlobalTimeout(t, 10*time.Second)

	db := openManagerTestDB(t)
	exec := newBlockingExecutor()
	m := NewManager(db, stubExecutorFactory{executor: exec}, nil, nil, nil, 8, 90)

	// Policy 直接绑定到 task；MaxExecutionSeconds 单位是秒，最小整数为 1
	policy := model.Policy{
		Name:                "policy-with-timeout",
		SourcePath:          "/tmp/src",
		TargetPath:          "/tmp/dst",
		CronSpec:            "* * * * *",
		MaxExecutionSeconds: 1,
	}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatalf("建 policy 失败: %v", err)
	}
	taskEntity := seedTaskForManagerTest(t, db)
	taskEntity.PolicyID = &policy.ID
	if err := db.Save(&taskEntity).Error; err != nil {
		t.Fatalf("绑定 policy 失败: %v", err)
	}

	runID := createTestTaskRun(t, db, taskEntity.ID, "manual")

	start := time.Now()
	m.runTask(taskEntity.ID, runID, "manual", generateChainRunID())
	elapsed := time.Since(start)

	// Policy 1s 超时应在 ~1s 后触发；远小于全局 10s
	if elapsed > 5*time.Second {
		t.Fatalf("Policy 超时未生效，耗时 %v", elapsed)
	}

	var run model.TaskRun
	if err := db.First(&run, runID).Error; err != nil {
		t.Fatalf("读取 TaskRun 失败: %v", err)
	}
	if run.Status != "failed" || !strings.Contains(run.LastError, "超时") {
		t.Fatalf("Policy 超时后期望 failed+超时，实际 status=%q error=%q", run.Status, run.LastError)
	}
}

// TestComputeExecTimeout_Defaults 验证优先级：Policy > globalOverride > env > 默认 24h
func TestComputeExecTimeout_Defaults(t *testing.T) {
	// Policy 优先
	taskWithPolicy := model.Task{Policy: &model.Policy{MaxExecutionSeconds: 7}}
	if got := computeExecTimeout(taskWithPolicy); got != 7*time.Second {
		t.Fatalf("Policy 优先失败：%v", got)
	}

	// Policy=0 时走 override（测试场景）
	withGlobalTimeout(t, 333*time.Millisecond)
	taskNoPolicy := model.Task{}
	if got := computeExecTimeout(taskNoPolicy); got != 333*time.Millisecond {
		t.Fatalf("globalOverride 未生效：%v", got)
	}
}
