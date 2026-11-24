package task

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/model"
)

func TestManagedRcloneIntegrityBlocksLegacyCheckBeforeSSH(t *testing.T) {
	db := openManagerTestDB(t)
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)
	taskEntity := seedTaskForManagerTest(t, db)
	taskEntity.ExecutorType = "rclone"
	taskEntity.RsyncSource = "/srv/source"
	taskEntity.RsyncTarget = "backup:legacy"
	session := &legacyLineageSessionFake{mode: publication.LineageExact}
	guard := &legacyLineageGuardFake{session: session}
	recorder := &legacyBlockRecorderFake{}
	manager.SetLineageGuard(guard)
	manager.SetLegacyBlockRecorder(recorder)

	manager.checkRcloneIntegrity(model.Policy{ID: 24}, taskEntity)

	if guard.calls != 1 || guard.operation != publication.OperationLegacyIntegrity {
		t.Fatalf("guard calls=%d operation=%q", guard.calls, guard.operation)
	}
	if len(recorder.blocks) != 1 || recorder.blocks[0].Operation != publication.OperationLegacyIntegrity {
		t.Fatalf("managed Rclone integrity blocks=%+v", recorder.blocks)
	}
	if got := atomic.LoadInt32(&session.closed); got != 1 {
		t.Fatalf("session close count=%d, want 1", got)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = manager.Shutdown(shutdownCtx)
}

func TestCheckIntegritySkipsTemplatePolicies(t *testing.T) {
	db := openManagerTestDB(t)
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)

	node := model.Node{
		Name: "node-integrity-tmpl", Host: "127.0.0.1", Port: 22,
		Username: "root", AuthType: "key", BackupDir: "node-integrity-tmpl",
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}

	templatePolicy := model.Policy{
		Name: "tmpl-integrity", SourcePath: "/srv/src", TargetPath: "/backup",
		CronSpec: "@daily", Enabled: true, IsTemplate: true,
	}
	activePolicy := model.Policy{
		Name: "active-integrity", SourcePath: "/srv/src2", TargetPath: "/backup2",
		CronSpec: "@daily", Enabled: true, IsTemplate: false,
	}
	for _, p := range []*model.Policy{&templatePolicy, &activePolicy} {
		if err := model.CreatePolicyWithExplicitValues(db, p, "enabled", "is_template"); err != nil {
			t.Fatalf("创建策略失败: %v", err)
		}
	}

	tmplTask := model.Task{
		Name: "tmpl-rclone-task", NodeID: node.ID, PolicyID: &templatePolicy.ID,
		Source: "policy", ExecutorType: "rclone", RsyncSource: "/srv/src", RsyncTarget: "backup:tmpl",
		Status: string(StatusPending),
	}
	activeTask := model.Task{
		Name: "active-rclone-task", NodeID: node.ID, PolicyID: &activePolicy.ID,
		Source: "policy", ExecutorType: "rclone", RsyncSource: "/srv/src2", RsyncTarget: "backup:active",
		Status: string(StatusPending),
	}
	for _, taskRow := range []*model.Task{&tmplTask, &activeTask} {
		if err := db.Create(taskRow).Error; err != nil {
			t.Fatalf("创建任务失败: %v", err)
		}
	}

	session := &legacyLineageSessionFake{mode: publication.LineageExact}
	guard := &legacyLineageGuardFake{session: session}
	recorder := &legacyBlockRecorderFake{}
	manager.SetLineageGuard(guard)
	manager.SetLegacyBlockRecorder(recorder)

	manager.checkIntegrity()

	if guard.calls != 1 {
		t.Fatalf("期望仅非模板策略触发完整性检查 (1 次)，实际: %d", guard.calls)
	}
	if len(recorder.blocks) != 1 || recorder.blocks[0].TaskID != activeTask.ID {
		t.Fatalf("完整性检查记录不符合预期（模板策略不应被检查）: %+v", recorder.blocks)
	}
}
