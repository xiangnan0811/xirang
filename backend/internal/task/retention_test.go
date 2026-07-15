package task

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/model"
	"xirang/backend/internal/task/executor"
)

func TestManagedResticRetentionBlocksForgetPruneBeforeCredentialAndSSH(t *testing.T) {
	db := openManagerTestDB(t)
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	taskEntity := seedTaskForManagerTest(t, db)
	taskEntity.ExecutorType = "restic"
	policy := model.Policy{ID: 17, RetentionDays: 7}
	session := &legacyLineageSessionFake{mode: publication.LineageExact}
	guard := &legacyLineageGuardFake{session: session}
	recorder := &legacyBlockRecorderFake{}
	manager.SetLineageGuard(guard)
	manager.SetLegacyBlockRecorder(recorder)
	var legacyCalls int32
	manager.resticRetentionFunc = func(context.Context, model.Policy, model.Task) {
		atomic.AddInt32(&legacyCalls, 1)
	}

	manager.enforceResticRetention(policy, taskEntity)

	if guard.calls != 1 || guard.operation != publication.OperationLegacyRetention {
		t.Fatalf("guard calls=%d operation=%q", guard.calls, guard.operation)
	}
	if got := atomic.LoadInt32(&legacyCalls); got != 0 {
		t.Fatalf("managed retention reached legacy credential/SSH path %d time(s)", got)
	}
	if len(recorder.blocks) != 1 || recorder.blocks[0].Operation != publication.OperationLegacyRetention || recorder.blocks[0].TaskID != taskEntity.ID || recorder.blocks[0].TaskRunID != nil {
		t.Fatalf("unexpected retention blocks: %+v", recorder.blocks)
	}
	if got := atomic.LoadInt32(&session.closed); got != 1 {
		t.Fatalf("session close count=%d, want 1", got)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = manager.Shutdown(shutdownCtx)
}

func TestManagedRsyncRetentionBlocksLegacyDirectoryDeletion(t *testing.T) {
	db := openManagerTestDB(t)
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	taskEntity := seedTaskForManagerTest(t, db)
	taskEntity.ExecutorType = "rsync"
	target := t.TempDir()
	stale := filepath.Join(target, "stale")
	if err := os.Mkdir(stale, 0o700); err != nil {
		t.Fatal(err)
	}
	staleAt := time.Now().AddDate(0, 0, -30)
	if err := os.Chtimes(stale, staleAt, staleAt); err != nil {
		t.Fatal(err)
	}
	session := &legacyLineageSessionFake{mode: publication.LineageExact}
	guard := &legacyLineageGuardFake{session: session}
	recorder := &legacyBlockRecorderFake{}
	manager.SetLineageGuard(guard)
	manager.SetLegacyBlockRecorder(recorder)

	manager.enforceRsyncRetention(model.Policy{ID: 22, TargetPath: target, RetentionDays: 7}, taskEntity, time.Now().AddDate(0, 0, -7))

	if guard.calls != 1 || guard.operation != publication.OperationLegacyRetention {
		t.Fatalf("guard calls=%d operation=%q", guard.calls, guard.operation)
	}
	if _, err := os.Stat(stale); err != nil {
		t.Fatalf("managed Rsync retention deleted legacy directory: %v", err)
	}
	if len(recorder.blocks) != 1 || recorder.blocks[0].Operation != publication.OperationLegacyRetention {
		t.Fatalf("managed Rsync retention blocks=%+v", recorder.blocks)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = manager.Shutdown(shutdownCtx)
}

func TestRollbackSafeDisabledRetentionRemainsBlocked(t *testing.T) {
	db := openManagerTestDB(t)
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	taskEntity := seedTaskForManagerTest(t, db)
	taskEntity.ExecutorType = "restic"
	policy := model.Policy{ID: 18, RetentionDays: 7}
	session := &legacyLineageSessionFake{mode: publication.LineageExact}
	guard := &legacyLineageGuardFake{session: session}
	manager.SetLineageGuard(guard)
	manager.resticRetentionFunc = func(context.Context, model.Policy, model.Task) {
		t.Fatal("rollback-safe retention must not reach legacy path")
	}

	manager.enforceResticRetention(policy, taskEntity)

	if guard.calls != 1 || guard.operation != publication.OperationLegacyRetention {
		t.Fatalf("guard calls=%d operation=%q", guard.calls, guard.operation)
	}
	if got := atomic.LoadInt32(&session.closed); got != 1 {
		t.Fatalf("session close count=%d, want 1", got)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = manager.Shutdown(shutdownCtx)
}

func TestPristineResticRetentionRetainsCompatibility(t *testing.T) {
	db := openManagerTestDB(t)
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	taskEntity := seedTaskForManagerTest(t, db)
	taskEntity.ExecutorType = "restic"
	policy := model.Policy{ID: 19, RetentionDays: 7}
	session := &legacyLineageSessionFake{mode: publication.LineageCompatibility}
	guard := &legacyLineageGuardFake{session: session}
	manager.SetLineageGuard(guard)
	var legacyCalls int32
	manager.resticRetentionFunc = func(context.Context, model.Policy, model.Task) {
		atomic.AddInt32(&legacyCalls, 1)
	}

	manager.enforceResticRetention(policy, taskEntity)

	if guard.calls != 1 || guard.operation != publication.OperationLegacyRetention {
		t.Fatalf("guard calls=%d operation=%q", guard.calls, guard.operation)
	}
	if got := atomic.LoadInt32(&legacyCalls); got != 1 {
		t.Fatalf("pristine retention legacy calls=%d, want 1", got)
	}
	if got := atomic.LoadInt32(&session.closed); got != 1 {
		t.Fatalf("session close count=%d, want 1", got)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = manager.Shutdown(shutdownCtx)
}

func TestResticRetentionAdmissionDrainsThroughCommandAndConnectionClose(t *testing.T) {
	db := openManagerTestDB(t)
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	taskEntity := seedTaskForManagerTest(t, db)
	taskEntity.ExecutorType = "restic"
	policy := model.Policy{ID: 20, RetentionDays: 7}
	session := &legacyLineageSessionFake{mode: publication.LineageCompatibility}
	manager.SetLineageGuard(&legacyLineageGuardFake{session: session})
	started := make(chan struct{})
	release := make(chan struct{})
	manager.resticRetentionFunc = func(context.Context, model.Policy, model.Task) {
		close(started)
		<-release
	}
	finished := make(chan struct{})
	go func() {
		manager.enforceResticRetention(policy, taskEntity)
		close(finished)
	}()

	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("legacy retention path did not begin")
	}
	if got := atomic.LoadInt32(&session.closed); got != 0 {
		t.Fatalf("admission closed before command/connection returned: %d", got)
	}
	close(release)
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("legacy retention path did not finish")
	}
	if got := atomic.LoadInt32(&session.closed); got != 1 {
		t.Fatalf("admission close count=%d, want 1", got)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = manager.Shutdown(shutdownCtx)
}

func TestManagedRestoreAndRetentionRecordTypedLegacyBlockAuditAndMetric(t *testing.T) {
	db := openManagerTestDB(t)
	restoreExecutor := &trackingRestoreExecutor{err: errors.New("must remain unreachable")}
	manager := NewManager(db, stubExecutorFactory{executor: restoreExecutor}, nil, nil, nil, nil, 8, 90)
	taskEntity := seedTaskForManagerTest(t, db)
	taskEntity.ExecutorType = "restic"
	restoreRunID := createTestTaskRun(t, db, taskEntity.ID, "restore")
	recorder := &legacyBlockRecorderFake{}
	manager.SetLegacyBlockRecorder(recorder)
	manager.SetLineageGuard(&legacyLineageGuardFake{session: &legacyLineageSessionFake{mode: publication.LineageExact}})
	manager.ensureRemoteTargetReadyFunc = func(context.Context, model.Node, string) error {
		t.Fatal("managed restore must not precheck")
		return nil
	}
	manager.resticRetentionFunc = func(context.Context, model.Policy, model.Task) {
		t.Fatal("managed retention must not touch its legacy path")
	}

	manager.runRestoreTask(taskEntity.ID, restoreRunID, taskEntity)
	manager.enforceResticRetention(model.Policy{ID: 21, RetentionDays: 7}, taskEntity)

	if len(recorder.blocks) != 2 {
		t.Fatalf("legacy block count=%d, want 2", len(recorder.blocks))
	}
	if restore := recorder.blocks[0]; restore.Operation != publication.OperationLegacyRestoreLatest || restore.TaskRunID == nil || *restore.TaskRunID != restoreRunID {
		t.Fatalf("restore block=%+v", restore)
	}
	if retention := recorder.blocks[1]; retention.Operation != publication.OperationLegacyRetention || retention.TaskRunID != nil {
		t.Fatalf("retention block=%+v", retention)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = manager.Shutdown(shutdownCtx)
}

func TestShellEscape(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"空字符串", ""},
		{"简单字符串", "hello"},
		{"包含单引号", "it's"},
		{"包含空格", "a b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shellEscape(tc.input)
			out, err := exec.Command("sh", "-c", "printf %s "+got).Output()
			if err != nil {
				t.Fatalf("shellEscape(%q) 生成的 shell 表达式执行失败: %v", tc.input, err)
			}
			if string(out) != tc.input {
				t.Fatalf("shellEscape(%q) 回放结果错误，实际 %q", tc.input, string(out))
			}
		})
	}
}

func TestResolveResticRepositoryAccessForRetention(t *testing.T) {
	cases := []struct {
		name   string
		input  string
		expect string
	}{
		{"有效 JSON", `{"repository_password": "FAKE_RETENTION_RESTIC_PASSWORD_FOR_TEST_ONLY"}`, "FAKE_RETENTION_RESTIC_PASSWORD_FOR_TEST_ONLY"},
		{"无访问口令字段", `{"repo": "/backup"}`, ""},
		{"空访问口令", `{"repository_password": ""}`, ""},
		{"额外空白", `{"repository_password" : "FAKE_RETENTION_RESTIC_PASSWORD_WITH_SPACE_FOR_TEST_ONLY"}`, "FAKE_RETENTION_RESTIC_PASSWORD_WITH_SPACE_FOR_TEST_ONLY"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			access := executor.ResolveResticRepositoryAccessOrEmpty(tc.input)
			if access.Password() != tc.expect {
				t.Fatalf("ResolveResticRepositoryAccessOrEmpty(%q): 期望 %q，实际 %q", tc.input, tc.expect, access.Password())
			}
		})
	}
}

func TestEnforceRsyncRetention(t *testing.T) {
	// 1. 创建临时目录作为策略目标路径
	targetDir := t.TempDir()

	freshDir := filepath.Join(targetDir, "fresh-dir")
	staleDir := filepath.Join(targetDir, "stale-dir")
	if err := os.Mkdir(freshDir, 0o755); err != nil {
		t.Fatalf("创建 fresh-dir 失败: %v", err)
	}
	if err := os.Mkdir(staleDir, 0o755); err != nil {
		t.Fatalf("创建 stale-dir 失败: %v", err)
	}

	// 将 stale-dir 的修改时间设为 30 天前
	staleTime := time.Now().AddDate(0, 0, -30)
	if err := os.Chtimes(staleDir, staleTime, staleTime); err != nil {
		t.Fatalf("设置 stale-dir 修改时间失败: %v", err)
	}

	// 2. 创建 Manager 并初始化测试数据
	db := openManagerTestDB(t)

	node := model.Node{
		Name:     "node-retention-test",
		Host:     "127.0.0.1",
		Port:     22,
		Username: "root",
		AuthType: "key",
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}

	policy := model.Policy{
		Name:          "policy-retention-test",
		SourcePath:    "/tmp/src",
		TargetPath:    targetDir,
		CronSpec:      "@daily",
		RetentionDays: 7,
	}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatalf("创建策略失败: %v", err)
	}

	task := model.Task{
		Name:         "task-retention-test",
		NodeID:       node.ID,
		ExecutorType: "rsync",
		Status:       string(StatusPending),
		RsyncSource:  "/tmp/src",
		RsyncTarget:  targetDir,
		PolicyID:     &policy.ID,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	// Preload Node 以避免 enforceRsyncRetention 内部访问空 Node
	db.Preload("Node").First(&task, task.ID)

	m := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)

	// 3. 调用 enforceRsyncRetention，cutoff 设为 7 天前
	cutoff := time.Now().AddDate(0, 0, -7)
	m.enforceRsyncRetention(policy, task, cutoff)

	// 4. 断言：stale-dir 应被删除，fresh-dir 应保留
	if _, err := os.Stat(staleDir); !os.IsNotExist(err) {
		t.Fatalf("期望 stale-dir 已被删除，但仍存在")
	}
	if _, err := os.Stat(freshDir); err != nil {
		t.Fatalf("期望 fresh-dir 仍存在，但访问失败: %v", err)
	}
}
