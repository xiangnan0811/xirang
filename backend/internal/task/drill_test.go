package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/credentialaudit"
	"xirang/backend/internal/model"

	"github.com/mattn/go-sqlite3"
	"github.com/robfig/cron/v3"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// openDrillTestDB 创建包含 PolicyNode 表迁移的测试数据库。
func openDrillTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := openManagerTestDB(t)
	if err := db.AutoMigrate(&model.PolicyNode{}); err != nil {
		t.Fatalf("PolicyNode 表迁移失败: %v", err)
	}
	return db
}

// seedDrillNodeWithBackupDir 创建带有唯一 BackupDir 的测试节点。
func seedDrillNodeWithBackupDir(t *testing.T, db *gorm.DB, name, host, backupDir string) model.Node {
	t.Helper()
	node := model.Node{
		Name:      name,
		Host:      host,
		Port:      22,
		Username:  "root",
		AuthType:  "key",
		BackupDir: backupDir,
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}
	return node
}

// TestValidateDrillConfigSandboxIsSourceNode 验证沙箱节点不能是备份源节点。
func TestValidateDrillConfigSandboxIsSourceNode(t *testing.T) {
	db := openDrillTestDB(t)
	m := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)

	node := seedDrillNodeWithBackupDir(t, db, "drill-src-node", "192.168.1.10", "drill-src-bd")

	targetNodeID := node.ID
	policy := model.Policy{
		Name:              "policy-drill-conflict",
		SourcePath:        "/tmp/src",
		TargetPath:        "/tmp/dst",
		CronSpec:          "@daily",
		DrillEnabled:      true,
		DrillCron:         "@every 5m",
		DrillTargetNodeID: &targetNodeID,
		DrillRestorePath:  "/tmp/xirang-drill",
	}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatalf("创建策略失败: %v", err)
	}
	if err := db.Create(&model.PolicyNode{PolicyID: policy.ID, NodeID: node.ID}).Error; err != nil {
		t.Fatalf("关联节点失败: %v", err)
	}

	var loaded model.Policy
	db.Preload("Nodes").First(&loaded, policy.ID)

	err := m.validateDrillConfig(&loaded, &node)
	if err == nil {
		t.Fatal("期望错误（沙箱=源节点），实际无错误")
	}
	if !strings.Contains(err.Error(), "沙箱节点不能是备份源节点") {
		t.Fatalf("错误信息应提及沙箱节点不能是源节点，实际: %v", err)
	}
}

// TestValidateDrillConfigInvalidRestorePath 验证无效恢复路径被拒绝。
func TestValidateDrillConfigInvalidRestorePath(t *testing.T) {
	db := openDrillTestDB(t)
	m := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)

	srcNode := seedDrillNodeWithBackupDir(t, db, "drill-src", "192.168.1.10", "drill-src-bd-2")
	sandbox := seedDrillNodeWithBackupDir(t, db, "drill-sandbox", "192.168.1.20", "drill-sb-bd-2")

	targetID := sandbox.ID
	policy := model.Policy{
		Name:              "policy-drill-badpath",
		SourcePath:        "/tmp/src",
		TargetPath:        "/tmp/dst",
		CronSpec:          "@daily",
		DrillEnabled:      true,
		DrillCron:         "@every 5m",
		DrillTargetNodeID: &targetID,
		DrillRestorePath:  "../etc/passwd",
	}
	db.Create(&policy)
	db.Create(&model.PolicyNode{PolicyID: policy.ID, NodeID: srcNode.ID})

	var loaded model.Policy
	db.Preload("Nodes").First(&loaded, policy.ID)

	err := m.validateDrillConfig(&loaded, &sandbox)
	if err == nil {
		t.Fatal("期望错误（无效恢复路径），实际无错误")
	}
	if !strings.Contains(err.Error(), "演习恢复路径无效") && !strings.Contains(err.Error(), "..") {
		t.Fatalf("错误信息应提及路径无效，实际: %v", err)
	}
}

// TestValidateDrillConfigSystemDirectory 验证禁止恢复到系统目录。
func TestValidateDrillConfigSystemDirectory(t *testing.T) {
	db := openDrillTestDB(t)
	m := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)

	srcNode := seedDrillNodeWithBackupDir(t, db, "drill-src-sys", "192.168.1.10", "drill-src-bd-sys")
	sandbox := seedDrillNodeWithBackupDir(t, db, "drill-sb-sys", "192.168.1.20", "drill-sb-bd-sys")

	targetID := sandbox.ID
	forbiddenPaths := []string{"/", "/etc", "/usr", "/bin", "/sbin", "/boot"}
	for _, path := range forbiddenPaths {
		policy := model.Policy{
			Name:              "policy-drill-sys-" + time.Now().Format("150405.000000"),
			SourcePath:        "/tmp/src",
			TargetPath:        "/tmp/dst",
			CronSpec:          "@daily",
			DrillEnabled:      true,
			DrillCron:         "@every 5m",
			DrillTargetNodeID: &targetID,
			DrillRestorePath:  path,
		}
		db.Create(&policy)
		db.Create(&model.PolicyNode{PolicyID: policy.ID, NodeID: srcNode.ID})

		var loaded model.Policy
		db.Preload("Nodes").First(&loaded, policy.ID)

		err := m.validateDrillConfig(&loaded, &sandbox)
		if err == nil {
			t.Fatalf("路径=%s: 期望被拒绝，实际通过", path)
		}
	}
}

// TestTransferFilesToSandboxBlocksCredentialSpreading 验证旧跨节点传输路径被阻断。
func TestTransferFilesToSandboxBlocksCredentialSpreading(t *testing.T) {
	db := openDrillTestDB(t)
	m := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	srcNode := model.Node{
		Name:       "drill-transfer-src",
		Host:       "192.168.3.10",
		Port:       22,
		Username:   "root",
		AuthType:   "key",
		PrivateKey: "FAKE_SOURCE_PRIVATE_KEY_FOR_TEST_ONLY",
	}
	dstNode := model.Node{Name: "drill-transfer-sandbox", Host: "192.168.3.20", Port: 22, Username: "root", AuthType: "key"}

	err := m.transferFilesToSandbox(context.Background(), srcNode, "/tmp/src", dstNode, "/tmp/xirang-drill-safe", func(string, string) {})
	if err == nil {
		t.Fatal("期望恢复演练跨节点传输被安全基线阻断")
	}
	message := err.Error()
	if !strings.Contains(message, "跨节点传输已禁用") {
		t.Fatalf("错误信息应说明跨节点传输被禁用，实际: %v", err)
	}
	if strings.Contains(message, "FAKE_SOURCE_PRIVATE_KEY_FOR_TEST_ONLY") || strings.Contains(message, "StrictHostKeyChecking=no") || strings.Contains(message, "UserKnownHostsFile=/dev/null") {
		t.Fatalf("错误信息不应包含源私钥或关闭主机密钥校验的命令片段，实际: %v", err)
	}
}

func TestRestoreBackupToSandboxBlocksBeforeRemoteMutation(t *testing.T) {
	db := openDrillTestDB(t)
	m := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	srcNode := model.Node{Name: "drill-restore-src", Host: "192.168.3.30", Port: 22, Username: "root", AuthType: "key"}
	dstNode := model.Node{Name: "drill-restore-sandbox", Host: "192.168.3.40", Port: 22, Username: "root", AuthType: "key"}
	srcTask := model.Task{
		Name:         "drill-restore-task",
		NodeID:       1,
		Node:         srcNode,
		ExecutorType: "rsync",
		RsyncSource:  "/data/src",
		RsyncTarget:  "/backup/dst",
	}
	calledRemote := false
	m.drillSSHScriptFunc = func(context.Context, model.Node, string) error {
		calledRemote = true
		return nil
	}

	err := m.restoreBackupToSandbox(context.Background(), srcTask, dstNode, "/tmp/xirang-drill-safe", func(string, string) {})
	if err == nil || !strings.Contains(err.Error(), "跨节点传输已禁用") {
		t.Fatalf("期望恢复前被安全基线阻断，实际: %v", err)
	}
	if calledRemote {
		t.Fatal("跨节点传输禁用时不应执行远端脚本或清理命令")
	}
}

func TestValidateDrillConfigSuccess(t *testing.T) {
	db := openDrillTestDB(t)
	m := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)

	srcNode := seedDrillNodeWithBackupDir(t, db, "drill-src-ok", "192.168.1.10", "drill-src-bd-ok")
	sandbox := seedDrillNodeWithBackupDir(t, db, "drill-sb-ok", "192.168.1.20", "drill-sb-bd-ok")

	targetID := sandbox.ID
	policy := model.Policy{
		Name:              "policy-drill-ok",
		SourcePath:        "/tmp/src",
		TargetPath:        "/tmp/dst",
		CronSpec:          "@daily",
		DrillEnabled:      true,
		DrillCron:         "@every 5m",
		DrillTargetNodeID: &targetID,
		DrillRestorePath:  "/tmp/xirang-drill",
	}
	db.Create(&policy)
	db.Create(&model.PolicyNode{PolicyID: policy.ID, NodeID: srcNode.ID})

	var loaded model.Policy
	db.Preload("Nodes").First(&loaded, policy.ID)

	if err := m.validateDrillConfig(&loaded, &sandbox); err != nil {
		t.Fatalf("合法配置应通过校验，实际错误: %v", err)
	}
}

// TestTriggerDrillPolicyNotFound 测试策略不存在的场景。
func TestTriggerDrillPolicyNotFound(t *testing.T) {
	db := openManagerTestDB(t)
	m := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)

	_, err := m.TriggerDrill(99999, nil)
	if err == nil {
		t.Fatal("期望错误（策略不存在），实际无错误")
	}
	if !strings.Contains(err.Error(), "策略不存在") {
		t.Fatalf("错误信息应提及策略不存在，实际: %v", err)
	}
}

// TestTriggerDrillNotEnabled 测试演习未启用的场景。
func TestTriggerDrillNotEnabled(t *testing.T) {
	db := openManagerTestDB(t)
	m := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)

	policy := model.Policy{
		Name:         "policy-drill-disabled",
		SourcePath:   "/tmp/src",
		TargetPath:   "/tmp/dst",
		CronSpec:     "@daily",
		DrillEnabled: false,
	}
	db.Create(&policy)

	_, err := m.TriggerDrill(policy.ID, nil)
	if err == nil {
		t.Fatal("期望错误（演习未启用），实际无错误")
	}
	if !strings.Contains(err.Error(), "未启用恢复演练") {
		t.Fatalf("错误信息应提及未启用，实际: %v", err)
	}
}

// TestTriggerDrillNoSandbox 测试未配置沙箱节点的场景。
func TestTriggerDrillNoSandbox(t *testing.T) {
	db := openManagerTestDB(t)
	m := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)

	policy := model.Policy{
		Name:              "policy-drill-nosandbox",
		SourcePath:        "/tmp/src",
		TargetPath:        "/tmp/dst",
		CronSpec:          "@daily",
		DrillEnabled:      true,
		DrillCron:         "@every 5m",
		DrillTargetNodeID: nil,
	}
	db.Create(&policy)

	_, err := m.TriggerDrill(policy.ID, nil)
	if err == nil {
		t.Fatal("期望错误（未配置沙箱），实际无错误")
	}
	if !strings.Contains(err.Error(), "未配置沙箱") {
		t.Fatalf("错误信息应提及未配置沙箱，实际: %v", err)
	}
}

// TestDrillCronMatching 验证 cron 匹配逻辑。
func TestDrillCronMatching(t *testing.T) {
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

	tests := []struct {
		name    string
		cron    string
		now     time.Time
		matches bool
	}{
		{
			name:    "每分钟触发",
			cron:    "* * * * *",
			now:     time.Date(2026, 5, 5, 10, 30, 0, 0, time.UTC),
			matches: true,
		},
		{
			name:    "每日凌晨3点",
			cron:    "0 3 * * *",
			now:     time.Date(2026, 5, 5, 3, 0, 0, 0, time.UTC),
			matches: true,
		},
		{
			name:    "每日凌晨3点—不匹配",
			cron:    "0 3 * * *",
			now:     time.Date(2026, 5, 5, 10, 0, 0, 0, time.UTC),
			matches: false,
		},
		{
			name:    "@daily 匹配",
			cron:    "@daily",
			now:     time.Date(2026, 5, 5, 0, 0, 0, 0, time.UTC),
			matches: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sched, err := parser.Parse(tt.cron)
			if err != nil {
				t.Fatalf("解析 cron 失败: %v", err)
			}
			nextRun := sched.Next(tt.now.Add(-61 * time.Second))
			got := nextRun.Before(tt.now) || nextRun.Equal(tt.now)
			if got != tt.matches {
				t.Fatalf("cron=%s now=%s: 期望 matches=%v, 实际=%v",
					tt.cron, tt.now.Format(time.RFC3339), tt.matches, got)
			}
		})
	}
}

// TestFindTaskForPolicyNoTasks 测试策略无关联任务。
func TestFindTaskForPolicyNoTasks(t *testing.T) {
	db := openManagerTestDB(t)
	m := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)

	policy := model.Policy{
		Name:       "policy-no-tasks",
		SourcePath: "/tmp/src",
		TargetPath: "/tmp/dst",
		CronSpec:   "@daily",
	}
	db.Create(&policy)

	_, err := m.findTaskForPolicy(policy.ID, nil)
	if err == nil {
		t.Fatal("期望错误（无关联任务），实际无错误")
	}
	if !strings.Contains(err.Error(), "没有关联的备份任务") {
		t.Fatalf("错误信息应提及无关联任务，实际: %v", err)
	}
}

// TestFindTaskForPolicyWithTasks 测试有任务但无成功记录。
func TestFindTaskForPolicyWithTasks(t *testing.T) {
	db := openManagerTestDB(t)
	m := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)

	node := seedDrillNodeWithBackupDir(t, db, "drill-node", "192.168.1.10", "drill-node-bd")

	policy := model.Policy{
		Name:       "policy-has-tasks",
		SourcePath: "/tmp/src",
		TargetPath: "/tmp/dst",
		CronSpec:   "@daily",
	}
	db.Create(&policy)

	task := model.Task{
		Name:         "drill-task",
		NodeID:       node.ID,
		PolicyID:     &policy.ID,
		ExecutorType: "rsync",
		Status:       string(StatusSuccess),
	}
	db.Create(&task)

	found, err := m.findTaskForPolicy(policy.ID, nil)
	if err != nil {
		t.Fatalf("不应报错，实际: %v", err)
	}
	if found.ID != task.ID {
		t.Fatalf("应返回第一个任务 (id=%d)，实际 (id=%d)", task.ID, found.ID)
	}
}

// TestFindTaskForPolicyPrefersSuccessful 测试优先返回有成功记录的任务。
func TestFindTaskForPolicyPrefersSuccessful(t *testing.T) {
	db := openManagerTestDB(t)
	m := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)

	node := seedDrillNodeWithBackupDir(t, db, "drill-node-pref", "192.168.1.10", "drill-node-pref-bd")

	policy := model.Policy{
		Name:       "policy-pref",
		SourcePath: "/tmp/src",
		TargetPath: "/tmp/dst",
		CronSpec:   "@daily",
	}
	db.Create(&policy)

	task1 := model.Task{
		Name:         "drill-task-1",
		NodeID:       node.ID,
		PolicyID:     &policy.ID,
		ExecutorType: "rsync",
		Status:       string(StatusSuccess),
	}
	db.Create(&task1)

	task2 := model.Task{
		Name:         "drill-task-2",
		NodeID:       node.ID,
		PolicyID:     &policy.ID,
		ExecutorType: "rsync",
		Status:       string(StatusSuccess),
	}
	db.Create(&task2)

	// task2 有成功记录
	db.Create(&model.TaskRun{TaskID: task2.ID, TriggerType: "manual", Status: "success"})

	found, err := m.findTaskForPolicy(policy.ID, nil)
	if err != nil {
		t.Fatalf("查找任务失败: %v", err)
	}
	if found.ID != task2.ID {
		t.Fatalf("应返回有成功记录的任务 (id=%d)，实际 (id=%d)", task2.ID, found.ID)
	}
}

func TestDrillSourceIgnoresNonAuthoritativeSuccessRuns(t *testing.T) {
	db := openManagerTestDB(t)
	m := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)

	nodeA := seedDrillNodeWithBackupDir(t, db, "drill-authority-a", "192.0.2.71", "drill-authority-a")
	nodeB := seedDrillNodeWithBackupDir(t, db, "drill-authority-b", "192.0.2.72", "drill-authority-b")
	policy := model.Policy{Name: "policy-drill-authority", SourcePath: "/safe/source", TargetPath: "/safe/target", CronSpec: "@daily"}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatal(err)
	}
	taskA := model.Task{Name: "drill-authority-task-a", NodeID: nodeA.ID, PolicyID: &policy.ID, ExecutorType: "rsync", Status: string(StatusSuccess)}
	taskB := model.Task{Name: "drill-authority-task-b", NodeID: nodeB.ID, PolicyID: &policy.ID, ExecutorType: "rsync", Status: string(StatusSuccess)}
	if err := db.Create(&taskA).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&taskB).Error; err != nil {
		t.Fatal(err)
	}
	legacy := model.TaskRun{TaskID: taskA.ID, TriggerType: "manual", Status: model.TaskRunStatusSuccess}
	mismatched := model.TaskRun{TaskID: taskA.ID, TriggerType: "manual", Status: model.TaskRunStatusSuccess}
	authoritative := model.TaskRun{TaskID: taskB.ID, TriggerType: "manual", Status: model.TaskRunStatusSuccess}
	for _, run := range []*model.TaskRun{&legacy, &mismatched, &authoritative} {
		if err := db.Create(run).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Model(&model.TaskRun{}).Where("id = ?", legacy.ID).
		UpdateColumn("node_id_snapshot", model.TaskRunNodeIDLegacyUnknown).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.TaskRun{}).Where("id = ?", mismatched.ID).
		UpdateColumn("node_id_snapshot", nodeB.ID).Error; err != nil {
		t.Fatal(err)
	}

	found, err := m.findTaskForPolicy(policy.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if found.ID != taskB.ID {
		t.Fatalf("drill source task=%d, want authoritative task %d", found.ID, taskB.ID)
	}
	if runID, err := m.latestSuccessfulRunID(taskA.ID, taskA.NodeID); err != nil || runID != nil {
		t.Fatalf("non-authoritative TaskRuns produced source run=%v err=%v", runID, err)
	}
	if runID, err := m.latestSuccessfulRunID(taskB.ID, taskB.NodeID); err != nil || runID == nil || *runID != authoritative.ID {
		t.Fatalf("authoritative TaskRun source=%v err=%v, want %d", runID, err, authoritative.ID)
	}
}

// TestTriggerDrillSandboxNotFound 测试沙箱节点不存在。
func TestTriggerDrillSandboxNotFound(t *testing.T) {
	db := openManagerTestDB(t)
	m := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)

	invalidNodeID := uint(99998)
	policy := model.Policy{
		Name:              "policy-drill-missingsb",
		SourcePath:        "/tmp/src",
		TargetPath:        "/tmp/dst",
		CronSpec:          "@daily",
		DrillEnabled:      true,
		DrillCron:         "@every 5m",
		DrillTargetNodeID: &invalidNodeID,
	}
	db.Create(&policy)

	_, err := m.TriggerDrill(policy.ID, nil)
	if err == nil {
		t.Fatal("期望错误（沙箱不存在），实际无错误")
	}
	if !strings.Contains(err.Error(), "沙箱节点不存在") {
		t.Fatalf("错误信息应提及沙箱不存在，实际: %v", err)
	}
}

// TestValidateDrillConfigMissingSandboxNode 测试沙箱节点为 nil。
func TestValidateDrillConfigMissingSandboxNode(t *testing.T) {
	db := openManagerTestDB(t)
	m := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)

	policy := model.Policy{
		Name:             "policy-no-sandbox-validate",
		SourcePath:       "/tmp/src",
		TargetPath:       "/tmp/dst",
		CronSpec:         "@daily",
		DrillRestorePath: "/tmp/xirang-drill",
	}

	err := m.validateDrillConfig(&policy, nil)
	if err == nil {
		t.Fatal("期望错误（沙箱节点为nil），实际无错误")
	}
	if !strings.Contains(err.Error(), "沙箱节点不存在") {
		t.Fatalf("错误信息应提及沙箱节点不存在，实际: %v", err)
	}
}

// TestValidateDrillConfigNoTargetNodeID 测试未配置 DrillTargetNodeID。
func TestValidateDrillConfigNoTargetNodeID(t *testing.T) {
	db := openDrillTestDB(t)
	m := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)

	sandbox := seedDrillNodeWithBackupDir(t, db, "drill-sb-notarget", "192.168.1.20", "drill-sb-notarget-bd")

	policy := model.Policy{
		Name:              "policy-no-target-id",
		SourcePath:        "/tmp/src",
		TargetPath:        "/tmp/dst",
		CronSpec:          "@daily",
		DrillEnabled:      true,
		DrillCron:         "@every 5m",
		DrillTargetNodeID: nil,
		DrillRestorePath:  "/tmp/xirang-drill",
	}

	err := m.validateDrillConfig(&policy, &sandbox)
	if err == nil {
		t.Fatal("期望错误（未配置沙箱节点ID），实际无错误")
	}
}

// TestTriggerDrillSuccessReturnsRunID 验证完整配置下 TriggerDrill 返回有效 run ID。
// 异步 goroutine 中的实际恢复会因为没有真实 SSH 而失败，但方法应立即返回成功。
func TestTriggerDrillSuccessReturnsRunID(t *testing.T) {
	db := openDrillTestDB(t)
	m := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90,
		WithDrillRestoreFunc(func(_ context.Context, _ model.Task, _ model.Node, _ string, _ func(string, string)) error {
			return nil
		}),
	)
	m.SetNodeWriteAdmission(&nodeWriteAdmissionFake{})
	shutdownManagerOnCleanup(t, m)

	srcNode := seedDrillNodeWithBackupDir(t, db, "drill-full-src", "192.168.1.100", "full-src-bd")
	sandbox := seedDrillNodeWithBackupDir(t, db, "drill-full-sb", "192.168.1.200", "full-sb-bd")

	targetID := sandbox.ID
	policy := model.Policy{
		Name:              "policy-full-drill",
		SourcePath:        "/tmp/src",
		TargetPath:        "/tmp/dst",
		CronSpec:          "@daily",
		DrillEnabled:      true,
		DrillCron:         "@every 5m",
		DrillTargetNodeID: &targetID,
		DrillRestorePath:  "/tmp/xirang-drill",
		DrillVerify:       "echo ok",
		DrillAutoCleanup:  true,
	}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatalf("创建策略失败: %v", err)
	}
	if err := db.Create(&model.PolicyNode{PolicyID: policy.ID, NodeID: srcNode.ID}).Error; err != nil {
		t.Fatalf("关联源节点失败: %v", err)
	}

	task := model.Task{
		Name:         "full-drill-task",
		NodeID:       srcNode.ID,
		PolicyID:     &policy.ID,
		ExecutorType: "rsync",
		RsyncSource:  "/data/src",
		RsyncTarget:  "/backup/dst",
		Status:       "success",
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	completeInitialDrillRecovery(t, m)

	runID, err := m.TriggerDrill(policy.ID, nil)
	if err != nil {
		t.Fatalf("TriggerDrill 不应报错: %v", err)
	}
	if runID == 0 {
		t.Fatal("应返回非零的 task_run_id")
	}

	// 验证 TaskRun 已创建且 trigger_type 为 "drill"
	var run model.TaskRun
	if err := db.First(&run, runID).Error; err != nil {
		t.Fatalf("查询 TaskRun 失败: %v", err)
	}
	if run.TriggerType != "drill" {
		t.Fatalf("期望 trigger_type=drill，实际: %s", run.TriggerType)
	}
	if run.TaskID != task.ID {
		t.Fatalf("期望 task_id=%d，实际: %d", task.ID, run.TaskID)
	}
	// executeDrill 异步运行，测试只验证 TriggerDrill 创建了合法状态的 drill TaskRun。
	validStatus := map[string]bool{
		"pending": true,
		"running": true,
		"success": true,
		"failed":  true,
	}
	if !validStatus[run.Status] {
		t.Fatalf("TaskRun status 不合法: %s", run.Status)
	}
}

func TestReserveDrillRunUsesAtomicSourceAndSandboxAdmission(t *testing.T) {
	db := openDrillTestDB(t)
	fixture := setupDrillEvidenceFixture(t, db)
	admission := &nodeWriteAdmissionFake{drillErrs: []error{ErrNodeWriteConflict}}
	fixture.manager.SetNodeWriteAdmission(admission)
	evidence, err := fixture.manager.pendingDrillEvidence(
		context.Background(), fixture.policy, fixture.task, fixture.sandbox,
	)
	if err != nil {
		t.Fatal(err)
	}

	run, err := fixture.manager.reserveDrillRun(context.Background(), fixture.task, evidence)
	if !errors.Is(err, ErrNodeWriteConflict) || run.ID != 0 {
		t.Fatalf("conflicted Drill reservation run=%d err=%v, want zero/conflict", run.ID, err)
	}
	calls, sourceNodeIDs, sandboxNodeIDs := admission.drillSnapshot()
	if calls != 1 || len(sourceNodeIDs) != 1 || sourceNodeIDs[0] != fixture.task.NodeID ||
		len(sandboxNodeIDs) != 1 || sandboxNodeIDs[0] != fixture.sandbox.ID {
		t.Fatalf("Drill admission calls/source/sandbox=%d/%v/%v, want 1/[%d]/[%d]",
			calls, sourceNodeIDs, sandboxNodeIDs, fixture.task.NodeID, fixture.sandbox.ID)
	}
	var runCount, evidenceCount int64
	if err := db.Model(&model.TaskRun{}).
		Where("task_id = ? AND trigger_type = ? AND status IN ?", fixture.task.ID, "drill", model.TaskRunActiveStatuses()).
		Count(&runCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.RestoreDrillEvidence{}).Where("task_id = ?", fixture.task.ID).Count(&evidenceCount).Error; err != nil {
		t.Fatal(err)
	}
	if runCount != 0 || evidenceCount != 0 {
		t.Fatalf("conflicted Drill reservation left TaskRun/Evidence=%d/%d", runCount, evidenceCount)
	}
}

func TestStartDrillRunUsesAtomicSourceAndSandboxAdmission(t *testing.T) {
	db := openDrillTestDB(t)
	fixture := setupDrillEvidenceFixture(t, db)
	runID := createTestTaskRun(t, db, fixture.task.ID, "drill")
	evidence, err := fixture.manager.pendingDrillEvidence(
		context.Background(), fixture.policy, fixture.task, fixture.sandbox,
	)
	if err != nil {
		t.Fatal(err)
	}
	evidence.TaskRunID = runID
	if err := db.Create(&evidence).Error; err != nil {
		t.Fatal(err)
	}
	admission := &nodeWriteAdmissionFake{drillStartErrs: []error{ErrNodeWriteConflict}}
	fixture.manager.SetNodeWriteAdmission(admission)

	started, err := fixture.manager.startDrillRunOnce(runID, evidence, time.Now().UTC())
	if !errors.Is(err, ErrNodeWriteConflict) || started {
		t.Fatalf("conflicted Drill start started=%v err=%v, want false/conflict", started, err)
	}
	calls, runIDs, sandboxNodeIDs := admission.drillStartSnapshot()
	if calls != 1 || len(runIDs) != 1 || runIDs[0] != runID ||
		len(sandboxNodeIDs) != 1 || sandboxNodeIDs[0] != fixture.sandbox.ID {
		t.Fatalf("Drill start calls/run/sandbox=%d/%v/%v, want 1/[%d]/[%d]",
			calls, runIDs, sandboxNodeIDs, runID, fixture.sandbox.ID)
	}
	var run model.TaskRun
	var storedEvidence model.RestoreDrillEvidence
	if err := db.First(&run, runID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("task_run_id = ?", runID).Take(&storedEvidence).Error; err != nil {
		t.Fatal(err)
	}
	if run.Status != model.TaskRunStatusPending || storedEvidence.Status != model.TaskRunStatusPending {
		t.Fatalf("conflicted Drill start split pair TaskRun=%q Evidence=%q", run.Status, storedEvidence.Status)
	}
}

func TestTriggerDrillFailsClosedWithoutNodeWriteAdmission(t *testing.T) {
	db := openDrillTestDB(t)
	fixture := setupDrillEvidenceFixture(t, db)
	completeInitialDrillRecovery(t, fixture.manager)
	fixture.manager.SetNodeWriteAdmission(nil)

	runID, err := fixture.manager.TriggerDrill(fixture.policy.ID, nil)
	if !errors.Is(err, ErrDrillUnavailable) || runID != 0 {
		t.Fatalf("missing node-write admission trigger run=%d err=%v, want zero/ErrDrillUnavailable", runID, err)
	}
	var activeRuns int64
	if err := db.Model(&model.TaskRun{}).
		Where("task_id = ? AND trigger_type = ? AND status IN ?", fixture.task.ID, "drill", model.TaskRunActiveStatuses()).
		Count(&activeRuns).Error; err != nil {
		t.Fatal(err)
	}
	if activeRuns != 0 {
		t.Fatalf("missing node-write admission created %d active Drill rows", activeRuns)
	}
}

func TestTriggerDrillFailsClosedBeforeInitialRecoverySweep(t *testing.T) {
	db := openDrillTestDB(t)
	fixture := setupDrillEvidenceFixture(t, db)
	fixture.manager.drillSSHScriptFunc = func(context.Context, model.Node, string) error { return nil }

	if !fixture.manager.drillRecoveryBlocked.Load() {
		t.Fatal("new Manager admitted drills before its initial durable recovery sweep")
	}
	runID, err := fixture.manager.TriggerDrill(fixture.policy.ID, nil)
	if err == nil || !strings.Contains(err.Error(), "对账未完成") {
		t.Fatalf("pre-sweep trigger run_id=%d err=%v, want sanitized fail-closed recovery error", runID, err)
	}
	var activeRuns int64
	if err := db.Model(&model.TaskRun{}).
		Where("task_id = ? AND trigger_type = ? AND status IN ?", fixture.task.ID, "drill", model.TaskRunActiveStatuses()).
		Count(&activeRuns).Error; err != nil {
		t.Fatal(err)
	}
	if activeRuns != 0 {
		t.Fatalf("pre-sweep trigger created %d active durable drill rows", activeRuns)
	}
}

func TestTriggerDrillAcrossManagersAllowsOnlyOneDurableActiveRun(t *testing.T) {
	db := openConcurrentManagerTestDB(t)
	if err := db.AutoMigrate(&model.PolicyNode{}); err != nil {
		t.Fatalf("migrate concurrent drill fixture: %v", err)
	}
	fixture := setupDrillEvidenceFixture(t, db)
	restoreEntered := make(chan struct{})
	releaseRestore := make(chan struct{})
	var enteredOnce sync.Once
	blockRestore := func(ctx context.Context, _ model.Task, _ model.Node, _ string, _ func(string, string)) error {
		enteredOnce.Do(func() { close(restoreEntered) })
		select {
		case <-releaseRestore:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	fixture.manager.drillRestoreFunc = blockRestore
	fixture.manager.drillSSHScriptFunc = func(context.Context, model.Node, string) error { return nil }
	second := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90,
		WithDrillRestoreFunc(blockRestore),
	)
	second.SetNodeWriteAdmission(&nodeWriteAdmissionFake{})
	second.drillSSHScriptFunc = func(context.Context, model.Node, string) error { return nil }
	shutdownManagerOnCleanup(t, second)
	completeInitialDrillRecovery(t, fixture.manager)
	completeInitialDrillRecovery(t, second)
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseRestore) }) }
	t.Cleanup(release)

	type triggerResult struct {
		runID uint
		err   error
	}
	start := make(chan struct{})
	results := make(chan triggerResult, 2)
	for _, manager := range []*Manager{fixture.manager, second} {
		go func(manager *Manager) {
			<-start
			runID, err := manager.TriggerDrill(fixture.policy.ID, nil)
			results <- triggerResult{runID: runID, err: err}
		}(manager)
	}
	close(start)
	first, secondResult := <-results, <-results

	successes := 0
	for _, result := range []triggerResult{first, secondResult} {
		if result.err == nil {
			successes++
			if result.runID == 0 {
				t.Error("accepted durable drill reservation returned a zero run ID")
			}
			continue
		}
		if result.runID != 0 || !strings.Contains(result.err.Error(), "正在执行") {
			t.Errorf("rejected cross-manager trigger run_id=%d err=%v, want sanitized duplicate rejection", result.runID, result.err)
		}
	}
	if successes != 1 {
		t.Fatalf("cross-manager concurrent trigger successes=%d, want exactly one database-backed reservation", successes)
	}

	select {
	case <-restoreEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("accepted drill did not remain active at the restore boundary")
	}
	var activeRuns int64
	if err := db.Model(&model.TaskRun{}).
		Where("task_id = ? AND trigger_type = ? AND status IN ?", fixture.task.ID, "drill", model.TaskRunActiveStatuses()).
		Count(&activeRuns).Error; err != nil {
		t.Fatal(err)
	}
	if activeRuns != 1 {
		t.Fatalf("database active drill count=%d, want exactly one", activeRuns)
	}
	if !db.Migrator().HasIndex(&model.TaskRun{}, activeDrillRunIndex) {
		t.Fatalf("concurrent fixture is missing authoritative partial index %q", activeDrillRunIndex)
	}
	directDuplicate := db.Exec(`INSERT INTO task_runs
		(task_id, node_id_snapshot, trigger_type, status, duration_ms, verify_status, throughput_mbps, progress, created_at, updated_at)
		VALUES (?, ?, 'drill', 'retrying', 0, 'none', 0, 0, ?, ?)`,
		fixture.task.ID, fixture.task.NodeID, time.Now().UTC(), time.Now().UTC()).Error
	if directDuplicate == nil || !isActiveDrillReservationConflict(directDuplicate) {
		t.Fatalf("database partial index did not reject a raw duplicate active drill: %v", directDuplicate)
	}
	release()
}

func TestDrillLeaseHeartbeatFailureStopsBeforeNextRemoteMutation(t *testing.T) {
	db := openDrillTestDB(t)
	fixture := setupDrillEvidenceFixture(t, db)
	fixture.manager.drillRecoveryLease = 30 * time.Millisecond
	completeInitialDrillRecovery(t, fixture.manager)
	heartbeatAttempted := make(chan struct{})
	var heartbeatOnce sync.Once
	callbackName := "test:fail-drill-lease-heartbeat"
	if err := db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		updates, ok := tx.Statement.Dest.(map[string]interface{})
		if !ok || tx.Statement.Table != "restore_drill_evidences" {
			return
		}
		leaseUntil, renewing := updates["recovery_lease_until"]
		if !renewing || leaseUntil == nil {
			return
		}
		heartbeatOnce.Do(func() { close(heartbeatAttempted) })
		_ = tx.AddError(errors.New("INTERNAL_DRILL_LEASE_HEARTBEAT_FAILURE_CANARY"))
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Update().Remove(callbackName) })

	precheckEntered := make(chan struct{})
	precheckCanceled := make(chan struct{})
	var precheckOnce sync.Once
	fixture.manager.drillSSHScriptFunc = func(ctx context.Context, _ model.Node, _ string) error {
		precheckOnce.Do(func() { close(precheckEntered) })
		select {
		case <-ctx.Done():
			close(precheckCanceled)
			return ctx.Err()
		case <-time.After(time.Second):
			return nil
		}
	}
	restoreCalled := make(chan struct{}, 1)
	fixture.manager.drillRestoreFunc = func(context.Context, model.Task, model.Node, string, func(string, string)) error {
		restoreCalled <- struct{}{}
		return nil
	}

	runID, err := fixture.manager.TriggerDrill(fixture.policy.ID, nil)
	if err != nil {
		t.Fatalf("trigger heartbeat-loss drill: %v", err)
	}
	select {
	case <-precheckEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("drill did not enter the first remote mutation")
	}
	select {
	case <-heartbeatAttempted:
	case <-time.After(3 * time.Second):
		t.Fatal("drill lease heartbeat was not attempted")
	}
	select {
	case <-precheckCanceled:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("runner context remained live after the durable lease heartbeat failed")
	}
	select {
	case <-restoreCalled:
		t.Fatal("runner continued to the restore mutation after losing its durable lease")
	case <-time.After(150 * time.Millisecond):
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		var run model.TaskRun
		if err := db.First(&run, runID).Error; err != nil {
			t.Fatal(err)
		}
		_, owned := fixture.manager.pendingRuns.Load(fixture.task.ID)
		if model.IsTerminalTaskRunStatus(run.Status) && !owned {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("lease-loss runner did not reach a durable terminal before cleanup: status=%q owned=%v", run.Status, owned)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestExpiredDrillLeaseRejectsStalePhaseBeforeRecoveryTakeover(t *testing.T) {
	db := openDrillTestDB(t)
	fixture := setupDrillEvidenceFixture(t, db)
	runID := createTestTaskRun(t, db, fixture.task.ID, "drill")
	futureLease := time.Now().UTC().Add(time.Minute)
	startedAt := time.Now().UTC().Add(-time.Second)
	started, err := fixture.manager.startDrillRun(runID, model.RestoreDrillEvidence{
		PolicyID: fixture.policy.ID, TaskID: fixture.task.ID, SandboxNodeID: fixture.sandbox.ID,
		SandboxPath: fixture.policy.DrillRestorePath, RestoreStatus: model.TaskRunStatusRunning,
		VerifyStatus: model.TaskRunStatusPending, PostVerifyStatus: model.TaskRunStatusSkipped,
		CleanupStatus: model.TaskRunStatusSkipped, RecoveryOwnerID: fixture.manager.drillOwnerID,
		RecoveryLeaseUntil: &futureLease,
	}, startedAt)
	if err != nil || !started {
		t.Fatalf("start leased drill fixture: started=%v err=%v", started, err)
	}
	expired := time.Now().UTC().Add(-time.Second)
	if err := db.Model(&model.RestoreDrillEvidence{}).Where("task_run_id = ?", runID).
		Update("recovery_lease_until", &expired).Error; err != nil {
		t.Fatal(err)
	}

	if err := fixture.manager.updateDrillEvidence(runID, map[string]interface{}{
		"restore_status": model.TaskRunStatusSuccess,
	}); err == nil {
		t.Fatal("stale runner mutated phase evidence after its durable lease expired")
	}
	takeover := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, takeover)
	if err := takeover.LoadSchedules(context.Background()); err != nil {
		t.Fatalf("expired lease recovery takeover: %v", err)
	}
	var run model.TaskRun
	var evidence model.RestoreDrillEvidence
	if err := db.First(&run, runID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&evidence, "task_run_id = ?", runID).Error; err != nil {
		t.Fatal(err)
	}
	if run.Status != model.TaskRunStatusCanceled || evidence.Status != model.TaskRunStatusCanceled ||
		evidence.RestoreStatus != model.TaskRunStatusCanceled {
		t.Fatalf("lease takeover did not atomically stop the stale phase: TaskRun=%q Evidence=%q restore=%q",
			run.Status, evidence.Status, evidence.RestoreStatus)
	}
}

func TestTriggerDrillDefaultTransportUnavailableBeforeTaskRun(t *testing.T) {
	db := openDrillTestDB(t)
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)

	srcNode := seedDrillNodeWithBackupDir(t, db, "drill-unavailable-src", "192.168.1.110", "unavailable-src-bd")
	sandbox := seedDrillNodeWithBackupDir(t, db, "drill-unavailable-sandbox", "192.168.1.210", "unavailable-sb-bd")
	targetID := sandbox.ID
	policy := model.Policy{
		Name:              "policy-drill-unavailable",
		SourcePath:        "/tmp/src",
		TargetPath:        "/tmp/dst",
		CronSpec:          "@daily",
		DrillEnabled:      true,
		DrillCron:         "@every 5m",
		DrillTargetNodeID: &targetID,
		DrillRestorePath:  "/tmp/xirang-drill-unavailable",
	}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatalf("创建策略失败: %v", err)
	}
	if err := db.Create(&model.PolicyNode{PolicyID: policy.ID, NodeID: srcNode.ID}).Error; err != nil {
		t.Fatalf("关联源节点失败: %v", err)
	}
	task := model.Task{
		Name:         "drill-unavailable-task",
		NodeID:       srcNode.ID,
		PolicyID:     &policy.ID,
		ExecutorType: "rsync",
		RsyncSource:  "/data/src",
		RsyncTarget:  "/backup/dst",
		Status:       "success",
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	runID, err := manager.TriggerDrill(policy.ID, nil)
	if !errors.Is(err, ErrDrillUnavailable) {
		t.Fatalf("默认恢复传输不可用时应返回 ErrDrillUnavailable，run_id=%d err=%v", runID, err)
	}
	if runID != 0 {
		t.Fatalf("恢复传输不可用时不应返回 TaskRun ID，实际 %d", runID)
	}
	var count int64
	if err := db.Model(&model.TaskRun{}).Where("task_id = ?", task.ID).Count(&count).Error; err != nil {
		t.Fatalf("统计 TaskRun 失败: %v", err)
	}
	if count != 0 {
		t.Fatalf("默认恢复传输不可用时不应创建 TaskRun，实际 %d", count)
	}
}

type drillEvidenceFixture struct {
	manager *Manager
	policy  model.Policy
	task    model.Task
	sandbox model.Node
}

func completeInitialDrillRecovery(t *testing.T, manager *Manager) {
	t.Helper()
	if err := manager.LoadSchedules(context.Background()); err != nil {
		t.Fatalf("complete initial drill recovery sweep: %v", err)
	}
}

func setupDrillEvidenceFixture(t *testing.T, db *gorm.DB) drillEvidenceFixture {
	t.Helper()
	srcNode := seedDrillNodeWithBackupDir(t, db, "drill-evidence-src", "192.168.2.10", "evidence-src")
	sandbox := seedDrillNodeWithBackupDir(t, db, "drill-evidence-sandbox", "192.168.2.20", "evidence-sandbox")
	targetID := sandbox.ID
	policy := model.Policy{
		Name:              "policy-drill-evidence",
		SourcePath:        "/data/src",
		TargetPath:        "/backup/dst",
		CronSpec:          "@daily",
		DrillEnabled:      true,
		DrillCron:         "@every 5m",
		DrillTargetNodeID: &targetID,
		DrillRestorePath:  "/tmp/xirang-drill-evidence",
		DrillVerify:       "verify-ok",
		DrillAutoCleanup:  true,
	}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatalf("创建策略失败: %v", err)
	}
	if err := db.Create(&model.PolicyNode{PolicyID: policy.ID, NodeID: srcNode.ID}).Error; err != nil {
		t.Fatalf("关联源节点失败: %v", err)
	}
	taskEntity := model.Task{
		Name:         "drill-evidence-task",
		NodeID:       srcNode.ID,
		Node:         srcNode,
		PolicyID:     &policy.ID,
		ExecutorType: "rsync",
		RsyncSource:  "/data/src",
		RsyncTarget:  "/backup/dst",
		Status:       "success",
	}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	finishedAt := time.Now().Add(-time.Minute)
	previousRun := model.TaskRun{TaskID: taskEntity.ID, TriggerType: "manual", Status: "success", FinishedAt: &finishedAt}
	if err := db.Create(&previousRun).Error; err != nil {
		t.Fatalf("创建成功执行记录失败: %v", err)
	}
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90,
		WithDrillRestoreFunc(func(_ context.Context, _ model.Task, _ model.Node, _ string, logf func(string, string)) error {
			logf("info", "restore-ok")
			return nil
		}),
	)
	manager.SetNodeWriteAdmission(&nodeWriteAdmissionFake{})
	shutdownManagerOnCleanup(t, manager)
	return drillEvidenceFixture{manager: manager, policy: policy, task: taskEntity, sandbox: sandbox}
}

func TestExecuteDrillRecordsSuccessfulEvidence(t *testing.T) {
	db := openDrillTestDB(t)
	fixture := setupDrillEvidenceFixture(t, db)
	var scripts []string
	fixture.manager.drillSSHScriptFunc = func(_ context.Context, _ model.Node, script string) error {
		scripts = append(scripts, script)
		return nil
	}
	runID := createTestTaskRun(t, db, fixture.task.ID, "drill")

	fixture.manager.executeDrill(&fixture.policy, fixture.task, fixture.sandbox, runID)

	var evidence model.RestoreDrillEvidence
	if err := db.First(&evidence, "task_run_id = ?", runID).Error; err != nil {
		t.Fatalf("查询恢复演练证据失败: %v", err)
	}
	if evidence.Status != "success" || !evidence.ConfidenceEligible {
		t.Fatalf("期望成功且可作为可信证据，实际 status=%s eligible=%v", evidence.Status, evidence.ConfidenceEligible)
	}
	if evidence.RestoreStatus != "success" || evidence.VerifyStatus != "success" || evidence.CleanupStatus != "success" {
		t.Fatalf("阶段状态不符合预期: restore=%s verify=%s cleanup=%s", evidence.RestoreStatus, evidence.VerifyStatus, evidence.CleanupStatus)
	}
	if evidence.FailedStep != "" {
		t.Fatalf("成功演练不应有失败步骤，实际: %q", evidence.FailedStep)
	}
	if evidence.SandboxNodeID != fixture.sandbox.ID || evidence.SandboxPath != fixture.policy.DrillRestorePath {
		t.Fatalf("沙箱证据不符合预期: node=%d path=%s", evidence.SandboxNodeID, evidence.SandboxPath)
	}
	if evidence.SourceTaskRunID == nil {
		t.Fatal("期望记录来源成功执行记录")
	}
	if evidence.SnapshotRef != "task_run:"+fmt.Sprint(*evidence.SourceTaskRunID) {
		t.Fatalf("期望记录快照引用，实际: %q", evidence.SnapshotRef)
	}
	if len(scripts) == 0 || scripts[len(scripts)-1] != "rm -rf '/tmp/xirang-drill-evidence'" {
		t.Fatalf("期望执行安全清理命令，实际脚本: %#v", scripts)
	}
}

func TestCancelRunningDrillFinalizesRunAndEvidenceWithSameDuration(t *testing.T) {
	db := openDrillTestDB(t)
	fixture := setupDrillEvidenceFixture(t, db)
	startedAt := time.Now().UTC().Add(-2 * time.Second)
	run := model.TaskRun{
		TaskID:      fixture.task.ID,
		TriggerType: "drill",
		Status:      model.TaskRunStatusRunning,
		StartedAt:   &startedAt,
	}
	if err := db.Create(&run).Error; err != nil {
		t.Fatalf("创建运行中演练记录失败: %v", err)
	}
	evidence := model.RestoreDrillEvidence{
		PolicyID:         fixture.policy.ID,
		TaskID:           fixture.task.ID,
		TaskRunID:        run.ID,
		SandboxNodeID:    fixture.sandbox.ID,
		SandboxNodeName:  fixture.sandbox.Name,
		SandboxPath:      fixture.policy.DrillRestorePath,
		Status:           model.TaskRunStatusRunning,
		StartedAt:        &startedAt,
		RestoreStatus:    model.TaskRunStatusRunning,
		VerifyStatus:     model.TaskRunStatusPending,
		PostVerifyStatus: model.TaskRunStatusSkipped,
		CleanupStatus:    model.TaskRunStatusSkipped,
	}
	if err := db.Create(&evidence).Error; err != nil {
		t.Fatalf("创建运行中演练证据失败: %v", err)
	}

	canceled, err := fixture.manager.cancelDrillTaskRuns(fixture.task.ID, "任务已取消")
	if err != nil {
		t.Fatalf("取消恢复演练失败: %v", err)
	}
	if canceled != 1 {
		t.Fatalf("取消记录数=%d，期望 1", canceled)
	}
	if err := db.First(&run, run.ID).Error; err != nil {
		t.Fatalf("重新读取 TaskRun 失败: %v", err)
	}
	if err := db.First(&evidence, evidence.ID).Error; err != nil {
		t.Fatalf("重新读取恢复演练证据失败: %v", err)
	}
	if run.Status != model.TaskRunStatusCanceled || evidence.Status != model.TaskRunStatusCanceled {
		t.Fatalf("取消终态不一致: TaskRun=%q Evidence=%q", run.Status, evidence.Status)
	}
	if run.DurationMs < 1000 || run.DurationMs != evidence.DurationMs {
		t.Fatalf("取消耗时不一致: TaskRun=%d Evidence=%d", run.DurationMs, evidence.DurationMs)
	}
	if run.FinishedAt == nil || evidence.FinishedAt == nil || !run.FinishedAt.Equal(*evidence.FinishedAt) {
		t.Fatalf("取消完成时间不一致: TaskRun=%v Evidence=%v", run.FinishedAt, evidence.FinishedAt)
	}
}

func TestExecuteDrillDoesNotCreateEvidenceAfterPendingCancellationWins(t *testing.T) {
	db := openDrillTestDB(t)
	fixture := setupDrillEvidenceFixture(t, db)
	runID := createTestTaskRun(t, db, fixture.task.ID, "drill")
	if canceled, err := fixture.manager.cancelDrillTaskRuns(fixture.task.ID, "任务已取消"); err != nil || canceled != 1 {
		t.Fatalf("预先取消 pending 演练失败: canceled=%d err=%v", canceled, err)
	}

	fixture.manager.executeDrillWithContext(
		context.Background(), &fixture.policy, fixture.task, fixture.sandbox, runID, nil, nil,
	)

	var evidenceCount int64
	if err := db.Model(&model.RestoreDrillEvidence{}).Where("task_run_id = ?", runID).Count(&evidenceCount).Error; err != nil {
		t.Fatalf("统计恢复演练证据失败: %v", err)
	}
	if evidenceCount != 0 {
		t.Fatalf("pending 取消先赢后仍创建了 %d 条恢复演练证据", evidenceCount)
	}
	var run model.TaskRun
	if err := db.First(&run, runID).Error; err != nil {
		t.Fatalf("读取 pending 取消后的 TaskRun 失败: %v", err)
	}
	if run.Status != model.TaskRunStatusCanceled || run.StartedAt != nil || run.DurationMs != 0 {
		t.Fatalf("pending 取消字段错误: status=%q started_at=%v duration_ms=%d", run.Status, run.StartedAt, run.DurationMs)
	}
}

func TestExecuteDrillLateContextCancellationCannotSplitSuccessfulTerminalState(t *testing.T) {
	db := openDrillTestDB(t)
	fixture := setupDrillEvidenceFixture(t, db)
	fixture.manager.drillSSHScriptFunc = func(_ context.Context, _ model.Node, _ string) error { return nil }
	runID := createTestTaskRun(t, db, fixture.task.ID, "drill")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	lateCancel := make(chan struct{})
	var cancelOnce sync.Once
	callbackName := fmt.Sprintf("test:cancel-after-drill-success-%d", runID)
	if err := db.Callback().Update().After("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		updates, ok := tx.Statement.Dest.(map[string]interface{})
		if !ok || tx.Statement.Table != "task_runs" || updates["status"] != model.TaskRunStatusSuccess {
			return
		}
		cancelOnce.Do(func() {
			canceled := make(chan struct{})
			go func() {
				cancel()
				close(canceled)
			}()
			<-canceled
			close(lateCancel)
		})
	}); err != nil {
		t.Fatalf("注册终态取消屏障失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Callback().Update().Remove(callbackName) })

	fixture.manager.executeDrillWithContext(ctx, &fixture.policy, fixture.task, fixture.sandbox, runID, nil, nil)
	select {
	case <-lateCancel:
	default:
		t.Fatal("未在 TaskRun success 写入窗口触发取消")
	}

	var run model.TaskRun
	var evidence model.RestoreDrillEvidence
	if err := db.First(&run, runID).Error; err != nil {
		t.Fatalf("读取 TaskRun 失败: %v", err)
	}
	if err := db.First(&evidence, "task_run_id = ?", runID).Error; err != nil {
		t.Fatalf("读取恢复演练证据失败: %v", err)
	}
	if run.Status != model.TaskRunStatusSuccess || evidence.Status != model.TaskRunStatusSuccess {
		t.Fatalf("后到取消制造了终态分裂: TaskRun=%q Evidence=%q", run.Status, evidence.Status)
	}
}

func TestDrillTerminalCASFirstCommitWinsWithoutSplit(t *testing.T) {
	for _, winner := range []string{model.TaskRunStatusSuccess, model.TaskRunStatusCanceled} {
		t.Run(winner+"_wins", func(t *testing.T) {
			db := openDrillTestDB(t)
			fixture := setupDrillEvidenceFixture(t, db)
			startedAt := time.Now().UTC().Add(-time.Second)
			run := model.TaskRun{
				TaskID: fixture.task.ID, TriggerType: "drill", Status: model.TaskRunStatusRunning, StartedAt: &startedAt,
			}
			if err := db.Create(&run).Error; err != nil {
				t.Fatalf("创建运行中 TaskRun 失败: %v", err)
			}
			evidence := model.RestoreDrillEvidence{
				PolicyID: fixture.policy.ID, TaskID: fixture.task.ID, TaskRunID: run.ID,
				SandboxNodeID: fixture.sandbox.ID, SandboxPath: fixture.policy.DrillRestorePath,
				Status: model.TaskRunStatusRunning, StartedAt: &startedAt,
				RestoreStatus: model.TaskRunStatusSuccess, VerifyStatus: model.TaskRunStatusSuccess,
				PostVerifyStatus: model.TaskRunStatusSkipped, CleanupStatus: model.TaskRunStatusSkipped,
			}
			if err := db.Create(&evidence).Error; err != nil {
				t.Fatalf("创建运行中 Evidence 失败: %v", err)
			}

			winnerEntered := make(chan struct{})
			winnerRelease := make(chan struct{})
			var barrierOnce sync.Once
			var releaseOnce sync.Once
			callbackName := fmt.Sprintf("test:drill-terminal-cas-%s-%d", winner, run.ID)
			if err := db.Callback().Update().After("gorm:update").Register(callbackName, func(tx *gorm.DB) {
				updates, ok := tx.Statement.Dest.(map[string]interface{})
				if !ok || tx.Statement.Table != "task_runs" || updates["status"] != winner {
					return
				}
				barrierOnce.Do(func() {
					close(winnerEntered)
					<-winnerRelease
				})
			}); err != nil {
				t.Fatalf("注册终态事务屏障失败: %v", err)
			}
			t.Cleanup(func() {
				releaseOnce.Do(func() { close(winnerRelease) })
				_ = db.Callback().Update().Remove(callbackName)
			})

			type terminalResult struct {
				won bool
				err error
			}
			winnerResult := make(chan terminalResult, 1)
			loserStarted := make(chan struct{})
			loserResult := make(chan terminalResult, 1)
			finishedAt := time.Now().UTC()
			if winner == model.TaskRunStatusSuccess {
				go func() {
					won, _, err := fixture.manager.finalizeDrillRun(run.ID, model.TaskRunStatusSuccess, "", finishedAt, map[string]interface{}{
						"failed_step": "", "confidence_eligible": true,
					})
					winnerResult <- terminalResult{won: won, err: err}
				}()
			} else {
				go func() {
					won, err := fixture.manager.cancelOneDrillRun(fixture.task.ID, run.ID, "任务已取消", finishedAt)
					winnerResult <- terminalResult{won: won, err: err}
				}()
			}
			select {
			case <-winnerEntered:
			case <-time.After(3 * time.Second):
				t.Fatal("第一终态事务未进入提交屏障")
			}

			if winner == model.TaskRunStatusSuccess {
				go func() {
					close(loserStarted)
					won, err := fixture.manager.cancelOneDrillRun(fixture.task.ID, run.ID, "任务已取消", finishedAt.Add(time.Millisecond))
					loserResult <- terminalResult{won: won, err: err}
				}()
			} else {
				go func() {
					close(loserStarted)
					won, _, err := fixture.manager.finalizeDrillRun(run.ID, model.TaskRunStatusSuccess, "", finishedAt.Add(time.Millisecond), map[string]interface{}{
						"failed_step": "", "confidence_eligible": true,
					})
					loserResult <- terminalResult{won: won, err: err}
				}()
			}
			<-loserStarted
			releaseOnce.Do(func() { close(winnerRelease) })

			if result := <-winnerResult; result.err != nil || !result.won {
				t.Fatalf("第一终态事务未赢得 CAS: won=%v err=%v", result.won, result.err)
			}
			if result := <-loserResult; result.err != nil || result.won {
				t.Fatalf("后到终态事务错误地赢得 CAS: won=%v err=%v", result.won, result.err)
			}

			if err := db.First(&run, run.ID).Error; err != nil {
				t.Fatalf("读取终态 TaskRun 失败: %v", err)
			}
			if err := db.First(&evidence, evidence.ID).Error; err != nil {
				t.Fatalf("读取终态 Evidence 失败: %v", err)
			}
			if run.Status != winner || evidence.Status != winner || run.DurationMs != evidence.DurationMs {
				t.Fatalf("首提交终态不一致: want=%q TaskRun=%q/%d Evidence=%q/%d",
					winner, run.Status, run.DurationMs, evidence.Status, evidence.DurationMs)
			}
		})
	}
}

func TestExecuteDrillTerminalSuccessPersistenceFailureRecoversBeforeOwnerRelease(t *testing.T) {
	db := openDrillTestDB(t)
	fixture := setupDrillEvidenceFixture(t, db)
	fixture.manager.drillSSHScriptFunc = func(_ context.Context, _ model.Node, _ string) error { return nil }
	runID := createTestTaskRun(t, db, fixture.task.ID, "drill")
	ownership := &pendingRunOwnership{}
	fixture.manager.pendingRuns.Store(fixture.task.ID, ownership)
	t.Cleanup(func() { fixture.manager.pendingRuns.CompareAndDelete(fixture.task.ID, ownership) })

	injected := errors.New("INTERNAL_TERMINAL_EVIDENCE_FAILURE_CANARY")
	injectedReached := false
	recoverySawOwner := false
	callbackName := fmt.Sprintf("test:fail-drill-evidence-terminal-%d", runID)
	if err := db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		updates, ok := tx.Statement.Dest.(map[string]interface{})
		if !ok || tx.Statement.Table != "restore_drill_evidences" {
			return
		}
		switch updates["status"] {
		case model.TaskRunStatusSuccess:
			injectedReached = true
			_ = tx.AddError(injected)
		case model.TaskRunStatusFailed:
			value, owned := fixture.manager.pendingRuns.Load(fixture.task.ID)
			recoverySawOwner = owned && value == ownership
		}
	}); err != nil {
		t.Fatalf("注册终态证据失败注入失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Callback().Update().Remove(callbackName) })

	fixture.manager.executeDrillWithContext(
		context.Background(), &fixture.policy, fixture.task, fixture.sandbox, runID, ownership, nil,
	)
	if !injectedReached {
		t.Fatal("未触发恢复演练证据终态失败注入")
	}
	if !recoverySawOwner {
		t.Fatal("终态持久化失败后未在释放 runner owner 前执行安全恢复")
	}
	if _, owned := fixture.manager.pendingRuns.Load(fixture.task.ID); owned {
		t.Fatal("恢复演练返回后仍残留 runner owner")
	}

	var run model.TaskRun
	var evidence model.RestoreDrillEvidence
	if err := db.First(&run, runID).Error; err != nil {
		t.Fatalf("读取 TaskRun 失败: %v", err)
	}
	if err := db.First(&evidence, "task_run_id = ?", runID).Error; err != nil {
		t.Fatalf("读取恢复演练证据失败: %v", err)
	}
	if run.Status != model.TaskRunStatusFailed || evidence.Status != model.TaskRunStatusFailed {
		t.Fatalf("终态持久化失败未恢复为配对 failed: TaskRun=%q Evidence=%q", run.Status, evidence.Status)
	}
	if run.FinishedAt == nil || evidence.FinishedAt == nil || !run.FinishedAt.Equal(*evidence.FinishedAt) {
		t.Fatalf("恢复后的 finished_at 不一致: TaskRun=%v Evidence=%v", run.FinishedAt, evidence.FinishedAt)
	}
	if run.DurationMs != evidence.DurationMs {
		t.Fatalf("恢复后的 duration_ms 不一致: TaskRun=%d Evidence=%d", run.DurationMs, evidence.DurationMs)
	}
	if evidence.FailedStep != "finalize" || evidence.ConfidenceEligible {
		t.Fatalf("恢复后的 Evidence 终态字段错误: failed_step=%q eligible=%v", evidence.FailedStep, evidence.ConfidenceEligible)
	}
	if strings.Contains(run.LastError, injected.Error()) {
		t.Fatalf("TaskRun last_error 泄漏内部持久化错误: %q", run.LastError)
	}
}

func TestExecuteDrillPersistentTerminalFailureRetainsOwnership(t *testing.T) {
	db := openDrillTestDB(t)
	fixture := setupDrillEvidenceFixture(t, db)
	fixture.manager.drillRecoveryLease = 50 * time.Millisecond
	fixture.manager.drillRecoveryInterval = 10 * time.Millisecond
	completeInitialDrillRecovery(t, fixture.manager)
	startManagerRecoveryWorker(t, fixture.manager)
	fixture.manager.drillSSHScriptFunc = func(context.Context, model.Node, string) error { return nil }
	runID := createTestTaskRun(t, db, fixture.task.ID, "drill")
	ownership := &pendingRunOwnership{}
	fixture.manager.pendingRuns.Store(fixture.task.ID, ownership)
	fixture.manager.chainRunner.Store(fixture.task.ID, ownership.cancel)
	t.Cleanup(func() {
		fixture.manager.chainRunner.Delete(fixture.task.ID)
		fixture.manager.pendingRuns.CompareAndDelete(fixture.task.ID, ownership)
	})

	injected := errors.New("INTERNAL_PERSISTENT_DRILL_TERMINAL_FAILURE_CANARY")
	callbackName := fmt.Sprintf("test:persistent-drill-terminal-%d", runID)
	if err := db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == "restore_drill_evidences" {
			_ = tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	callbackInstalled := true
	t.Cleanup(func() {
		if callbackInstalled {
			_ = db.Callback().Update().Remove(callbackName)
		}
	})

	fixture.manager.executeDrillWithContext(
		context.Background(), &fixture.policy, fixture.task, fixture.sandbox, runID, ownership, nil,
	)
	value, owned := fixture.manager.pendingRuns.Load(fixture.task.ID)
	if !owned || value != ownership {
		t.Fatal("persistent terminal transaction failure released process ownership while durable drill rows remained active")
	}
	if _, cancelOwned := fixture.manager.chainRunner.Load(fixture.task.ID); !cancelOwned {
		t.Fatal("persistent terminal transaction failure released the runner cancellation owner")
	}
	var run model.TaskRun
	var evidence model.RestoreDrillEvidence
	if err := db.First(&run, runID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&evidence, "task_run_id = ?", runID).Error; err != nil {
		t.Fatal(err)
	}
	if !model.IsActiveTaskRunStatus(run.Status) || !model.IsActiveTaskRunStatus(evidence.Status) {
		t.Fatalf("fault injection did not leave the expected recovery handoff: TaskRun=%q Evidence=%q", run.Status, evidence.Status)
	}

	if err := db.Callback().Update().Remove(callbackName); err != nil {
		t.Fatal(err)
	}
	callbackInstalled = false
	deadline := time.Now().Add(3 * time.Second)
	for {
		if err := db.First(&run, runID).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.First(&evidence, "task_run_id = ?", runID).Error; err != nil {
			t.Fatal(err)
		}
		if model.IsTerminalTaskRunStatus(run.Status) && model.IsTerminalTaskRunStatus(evidence.Status) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Manager.Run lifecycle sweep did not recover persistent terminal failure without manual Cancel: TaskRun=%q Evidence=%q",
				run.Status, evidence.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if run.Status != model.TaskRunStatusCanceled || evidence.Status != model.TaskRunStatusCanceled ||
		run.FinishedAt == nil || evidence.FinishedAt == nil || !run.FinishedAt.Equal(*evidence.FinishedAt) ||
		run.DurationMs != evidence.DurationMs || evidence.ConfidenceEligible {
		t.Fatalf("lifecycle recovery did not atomically terminalize the pair: TaskRun=%+v Evidence=%+v", run, evidence)
	}
	if _, owned := fixture.manager.pendingRuns.Load(fixture.task.ID); owned {
		t.Fatal("successful lifecycle recovery retained process-local drill ownership")
	}
}

func TestFinalizeDrillRunRetriesTransientPersistenceFailureWithBoundedAttempts(t *testing.T) {
	for _, testCase := range []struct {
		name         string
		failures     int
		wantWon      bool
		wantErr      bool
		wantAttempts int
	}{
		{name: "recovers", failures: 2, wantWon: true, wantAttempts: 3},
		{name: "stops_at_limit", failures: 4, wantErr: true, wantAttempts: 3},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db := openDrillTestDB(t)
			fixture := setupDrillEvidenceFixture(t, db)
			runID := createTestTaskRun(t, db, fixture.task.ID, "drill")
			startedAt := time.Now().UTC().Add(-time.Second)
			started, err := fixture.manager.startDrillRun(runID, model.RestoreDrillEvidence{
				PolicyID: fixture.policy.ID, TaskID: fixture.task.ID, SandboxNodeID: fixture.sandbox.ID,
				SandboxPath: fixture.policy.DrillRestorePath, RestoreStatus: model.TaskRunStatusSuccess,
				VerifyStatus: model.TaskRunStatusSuccess, PostVerifyStatus: model.TaskRunStatusSkipped,
				CleanupStatus: model.TaskRunStatusSuccess,
			}, startedAt)
			if err != nil || !started {
				t.Fatalf("start drill pair: started=%v err=%v", started, err)
			}

			attempts := 0
			callbackName := fmt.Sprintf("test:transient-drill-terminal-%s-%d", testCase.name, runID)
			if err := db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
				updates, ok := tx.Statement.Dest.(map[string]interface{})
				if !ok || tx.Statement.Table != "restore_drill_evidences" || updates["status"] != model.TaskRunStatusSuccess {
					return
				}
				attempts++
				if attempts <= testCase.failures {
					_ = tx.AddError(sqlite3.Error{Code: sqlite3.ErrBusy})
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Callback().Update().Remove(callbackName) })

			won, _, err := fixture.manager.finalizeDrillRun(runID, model.TaskRunStatusSuccess, "", time.Now().UTC(), map[string]interface{}{
				"failed_step": "", "confidence_eligible": true,
			})
			if won != testCase.wantWon || (err != nil) != testCase.wantErr {
				t.Fatalf("finalize result won=%v err=%v, want won=%v err=%v", won, err, testCase.wantWon, testCase.wantErr)
			}
			if attempts != testCase.wantAttempts {
				t.Fatalf("terminal persistence attempts=%d, want bounded %d", attempts, testCase.wantAttempts)
			}
		})
	}
}

func TestExecuteDrillRetriesPreStartCancellationPersistenceBeforeOwnerRelease(t *testing.T) {
	db := openDrillTestDB(t)
	fixture := setupDrillEvidenceFixture(t, db)
	runID := createTestTaskRun(t, db, fixture.task.ID, "drill")
	ownership := &pendingRunOwnership{}
	fixture.manager.pendingRuns.Store(fixture.task.ID, ownership)
	t.Cleanup(func() { fixture.manager.pendingRuns.CompareAndDelete(fixture.task.ID, ownership) })

	attempts := 0
	retryWaits := 0
	recoverySawOwner := false
	fixture.manager.nodeWriteRetryWait = func(context.Context, int) error {
		retryWaits++
		return nil
	}
	callbackName := fmt.Sprintf("test:drill-prestart-cancel-retry-%d", runID)
	if err := db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		updates, ok := tx.Statement.Dest.(map[string]interface{})
		if !ok || tx.Statement.Table != "task_runs" || updates["status"] != model.TaskRunStatusCanceled {
			return
		}
		attempts++
		if attempts == 1 {
			_ = tx.AddError(sqlite3.Error{Code: sqlite3.ErrBusy})
			return
		}
		value, owned := fixture.manager.pendingRuns.Load(fixture.task.ID)
		recoverySawOwner = owned && value == ownership
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Update().Remove(callbackName) })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fixture.manager.executeDrillWithContext(ctx, &fixture.policy, fixture.task, fixture.sandbox, runID, ownership, nil)

	if attempts != 2 || retryWaits != 1 {
		t.Fatalf("pre-start cancel persistence attempts=%d waits=%d, want 2/1", attempts, retryWaits)
	}
	if !recoverySawOwner {
		t.Fatal("pre-start cancellation persistence recovered only after runner ownership was released")
	}
	if _, owned := fixture.manager.pendingRuns.Load(fixture.task.ID); owned {
		t.Fatal("durably canceled pre-start drill retained runner ownership")
	}
	var run model.TaskRun
	if err := db.First(&run, runID).Error; err != nil {
		t.Fatal(err)
	}
	if run.Status != model.TaskRunStatusCanceled || run.StartedAt != nil || run.FinishedAt == nil || run.DurationMs != 0 {
		t.Fatalf("pre-start cancellation was not durable: status=%q started=%v finished=%v duration=%d",
			run.Status, run.StartedAt, run.FinishedAt, run.DurationMs)
	}
}

func TestExecuteDrillStartPersistenceFailureUsesBoundedRecoveryBeforeOwnerRelease(t *testing.T) {
	for _, testCase := range []struct {
		name         string
		failures     int
		wantAttempts int
		wantTerminal bool
	}{
		{name: "transient_start_recovers", failures: 1, wantAttempts: 2},
		{name: "retry_limit_compensates", failures: 4, wantAttempts: 3, wantTerminal: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db := openDrillTestDB(t)
			fixture := setupDrillEvidenceFixture(t, db)
			fixture.manager.drillSSHScriptFunc = func(context.Context, model.Node, string) error { return nil }
			fixture.manager.nodeWriteRetryWait = func(context.Context, int) error { return nil }
			runID := createTestTaskRun(t, db, fixture.task.ID, "drill")
			ownership := &pendingRunOwnership{}
			fixture.manager.pendingRuns.Store(fixture.task.ID, ownership)
			t.Cleanup(func() { fixture.manager.pendingRuns.CompareAndDelete(fixture.task.ID, ownership) })

			attempts := 0
			terminalSawOwner := false
			callbackName := fmt.Sprintf("test:drill-start-retry-%s-%d", testCase.name, runID)
			if err := db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
				updates, ok := tx.Statement.Dest.(map[string]interface{})
				if !ok || tx.Statement.Table != "task_runs" {
					return
				}
				if updates["status"] == model.TaskRunStatusRunning {
					attempts++
					if attempts <= testCase.failures {
						_ = tx.AddError(sqlite3.Error{Code: sqlite3.ErrBusy})
					}
					return
				}
				if model.IsTerminalTaskRunStatus(fmt.Sprint(updates["status"])) {
					value, owned := fixture.manager.pendingRuns.Load(fixture.task.ID)
					terminalSawOwner = owned && value == ownership
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Callback().Update().Remove(callbackName) })

			fixture.manager.executeDrillWithContext(
				context.Background(), &fixture.policy, fixture.task, fixture.sandbox, runID, ownership, nil,
			)
			if attempts != testCase.wantAttempts {
				t.Fatalf("start persistence attempts=%d, want bounded %d", attempts, testCase.wantAttempts)
			}
			if _, owned := fixture.manager.pendingRuns.Load(fixture.task.ID); owned {
				t.Fatal("completed start recovery retained runner ownership")
			}
			var run model.TaskRun
			if err := db.First(&run, runID).Error; err != nil {
				t.Fatal(err)
			}
			if testCase.wantTerminal {
				if !model.IsTerminalTaskRunStatus(run.Status) || !terminalSawOwner {
					t.Fatalf("exhausted start recovery released owner without durable terminal: status=%q saw_owner=%v", run.Status, terminalSawOwner)
				}
				return
			}
			if run.Status != model.TaskRunStatusSuccess {
				t.Fatalf("transient start persistence did not recover to successful drill: status=%q", run.Status)
			}
		})
	}
}

func TestExecuteDrillStartupRecoveryHandoffCanBeReconciledAfterDatabaseRecovers(t *testing.T) {
	db := openDrillTestDB(t)
	fixture := setupDrillEvidenceFixture(t, db)
	fixture.manager.nodeWriteRetryWait = func(context.Context, int) error { return nil }
	runID := createTestTaskRun(t, db, fixture.task.ID, "drill")
	ownership := &pendingRunOwnership{}
	fixture.manager.pendingRuns.Store(fixture.task.ID, ownership)
	fixture.manager.chainRunner.Store(fixture.task.ID, ownership.cancel)
	t.Cleanup(func() {
		fixture.manager.chainRunner.Delete(fixture.task.ID)
		fixture.manager.pendingRuns.CompareAndDelete(fixture.task.ID, ownership)
	})

	attempts := 0
	callbackName := fmt.Sprintf("test:drill-startup-recovery-handoff-%d", runID)
	if err := db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		updates, ok := tx.Statement.Dest.(map[string]interface{})
		if !ok || tx.Statement.Table != "task_runs" {
			return
		}
		status := fmt.Sprint(updates["status"])
		if status != model.TaskRunStatusRunning && status != model.TaskRunStatusCanceled {
			return
		}
		attempts++
		_ = tx.AddError(sqlite3.Error{Code: sqlite3.ErrBusy})
	}); err != nil {
		t.Fatal(err)
	}
	callbackInstalled := true
	t.Cleanup(func() {
		if callbackInstalled {
			_ = db.Callback().Update().Remove(callbackName)
		}
	})

	fixture.manager.executeDrillWithContext(
		context.Background(), &fixture.policy, fixture.task, fixture.sandbox, runID, ownership, nil,
	)
	if attempts != 2*drillTerminalTransitionAttempts {
		t.Fatalf("startup plus compensation attempts=%d, want bounded %d",
			attempts, 2*drillTerminalTransitionAttempts)
	}
	if value, owned := fixture.manager.pendingRuns.Load(fixture.task.ID); !owned || value != ownership {
		t.Fatal("exhausted startup recovery released ownership before a durable terminal transition")
	}
	var pending model.TaskRun
	if err := db.First(&pending, runID).Error; err != nil {
		t.Fatal(err)
	}
	if pending.Status != model.TaskRunStatusPending {
		t.Fatalf("fault-injected startup run status=%q, want pending recovery handoff", pending.Status)
	}

	if err := db.Callback().Update().Remove(callbackName); err != nil {
		t.Fatal(err)
	}
	callbackInstalled = false
	if err := fixture.manager.Cancel(fixture.task.ID); err != nil {
		t.Fatalf("explicit startup recovery after database became available: %v", err)
	}

	var recovered model.TaskRun
	if err := db.First(&recovered, runID).Error; err != nil {
		t.Fatal(err)
	}
	if recovered.Status != model.TaskRunStatusCanceled || recovered.StartedAt != nil || recovered.FinishedAt == nil || recovered.DurationMs != 0 {
		t.Fatalf("startup recovery did not durably cancel pending run: status=%q started=%v finished=%v duration=%d",
			recovered.Status, recovered.StartedAt, recovered.FinishedAt, recovered.DurationMs)
	}
	if _, owned := fixture.manager.pendingRuns.Load(fixture.task.ID); owned {
		t.Fatal("successful explicit startup recovery retained process-local ownership")
	}
	if _, owned := fixture.manager.chainRunner.Load(fixture.task.ID); owned {
		t.Fatal("successful explicit startup recovery retained chain runner cancellation state")
	}
	var evidenceCount int64
	if err := db.Model(&model.RestoreDrillEvidence{}).Where("task_run_id = ?", runID).Count(&evidenceCount).Error; err != nil {
		t.Fatal(err)
	}
	if evidenceCount != 0 {
		t.Fatalf("rolled-back startup unexpectedly retained %d Evidence rows", evidenceCount)
	}
}

func TestExecuteDrillRecoveryHandoffLinearizesWithConcurrentCancel(t *testing.T) {
	db := openConcurrentManagerTestDB(t)
	if err := db.AutoMigrate(&model.PolicyNode{}); err != nil {
		t.Fatalf("migrate concurrent drill fixture: %v", err)
	}
	fixture := setupDrillEvidenceFixture(t, db)
	fixture.manager.nodeWriteRetryWait = func(context.Context, int) error { return nil }
	runID := createTestTaskRun(t, db, fixture.task.ID, "drill")
	ownership := &pendingRunOwnership{}
	fixture.manager.pendingRuns.Store(fixture.task.ID, ownership)
	fixture.manager.chainRunner.Store(fixture.task.ID, ownership.cancel)
	t.Cleanup(func() {
		fixture.manager.chainRunner.Delete(fixture.task.ID)
		fixture.manager.pendingRuns.CompareAndDelete(fixture.task.ID, ownership)
	})

	updateAttempts := 0
	updateCallback := fmt.Sprintf("test:drill-handoff-update-failure-%d", runID)
	if err := db.Callback().Update().Before("gorm:update").Register(updateCallback, func(tx *gorm.DB) {
		updates, ok := tx.Statement.Dest.(map[string]interface{})
		if !ok || tx.Statement.Table != "task_runs" {
			return
		}
		status := fmt.Sprint(updates["status"])
		if status != model.TaskRunStatusRunning && status != model.TaskRunStatusCanceled {
			return
		}
		updateAttempts++
		_ = tx.AddError(sqlite3.Error{Code: sqlite3.ErrBusy})
	}); err != nil {
		t.Fatal(err)
	}
	updateCallbackInstalled := true
	t.Cleanup(func() {
		if updateCallbackInstalled {
			_ = db.Callback().Update().Remove(updateCallback)
		}
	})

	pendingRead := make(chan struct{})
	releasePendingRead := make(chan struct{})
	var releaseReadOnce sync.Once
	releaseRead := func() { releaseReadOnce.Do(func() { close(releasePendingRead) }) }
	t.Cleanup(releaseRead)
	var blocked atomic.Bool
	queryCallback := fmt.Sprintf("test:block-drill-handoff-terminal-read-%d", runID)
	if err := db.Callback().Query().After("gorm:after_query").Register(queryCallback, func(tx *gorm.DB) {
		if updateAttempts != 2*drillTerminalTransitionAttempts || tx.Statement.Table != "task_runs" {
			return
		}
		run, ok := tx.Statement.Dest.(*model.TaskRun)
		if !ok || run.ID != runID || run.Status != model.TaskRunStatusPending || !blocked.CompareAndSwap(false, true) {
			return
		}
		close(pendingRead)
		<-releasePendingRead
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(queryCallback) })

	runnerDone := make(chan struct{})
	go func() {
		defer close(runnerDone)
		fixture.manager.executeDrillWithContext(
			context.Background(), &fixture.policy, fixture.task, fixture.sandbox, runID, ownership, nil,
		)
	}()
	select {
	case <-pendingRead:
	case <-time.After(3 * time.Second):
		t.Fatal("runner recovery did not pause after observing the pending TaskRun")
	}

	if err := db.Callback().Update().Remove(updateCallback); err != nil {
		t.Fatal(err)
	}
	updateCallbackInstalled = false
	if err := fixture.manager.Cancel(fixture.task.ID); err != nil {
		t.Fatalf("concurrent Cancel after database recovery: %v", err)
	}
	releaseRead()
	select {
	case <-runnerDone:
	case <-time.After(3 * time.Second):
		t.Fatal("runner recovery did not return after releasing its terminal read")
	}

	var recovered model.TaskRun
	if err := db.First(&recovered, runID).Error; err != nil {
		t.Fatal(err)
	}
	if recovered.Status != model.TaskRunStatusCanceled {
		t.Fatalf("concurrent recovery TaskRun status=%q, want canceled", recovered.Status)
	}
	if _, owned := fixture.manager.pendingRuns.Load(fixture.task.ID); owned {
		t.Fatal("runner/Cancel recovery handoff interleaving leaked process-local ownership")
	}
	if _, owned := fixture.manager.chainRunner.Load(fixture.task.ID); owned {
		t.Fatal("runner/Cancel recovery handoff interleaving leaked chain runner state")
	}
}

func TestExecuteDrillPhaseEvidencePersistenceFailureCannotCommitSuccess(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		field      string
		value      string
		failedStep string
		zeroRows   bool
		configure  func(*drillEvidenceFixture)
	}{
		{name: "restore_update_error", field: "restore_status", value: model.TaskRunStatusSuccess, failedStep: "restore"},
		{name: "verify_update_error", field: "verify_status", value: model.TaskRunStatusSuccess, failedStep: "verify"},
		{
			name: "post_verify_update_error", field: "post_verify_status", value: model.TaskRunStatusSuccess, failedStep: "post_verify",
			configure: func(fixture *drillEvidenceFixture) { fixture.policy.DrillPostVerify = "post-verify-ok" },
		},
		{name: "cleanup_update_error", field: "cleanup_status", value: model.TaskRunStatusSuccess, failedStep: "cleanup"},
		{name: "verify_update_zero_rows", field: "verify_status", value: model.TaskRunStatusSuccess, failedStep: "verify", zeroRows: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db := openDrillTestDB(t)
			fixture := setupDrillEvidenceFixture(t, db)
			if testCase.configure != nil {
				testCase.configure(&fixture)
			}
			fixture.manager.drillSSHScriptFunc = func(context.Context, model.Node, string) error { return nil }
			runID := createTestTaskRun(t, db, fixture.task.ID, "drill")

			injected := false
			callbackName := fmt.Sprintf("test:drill-phase-persistence-%s-%d", testCase.name, runID)
			if err := db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
				updates, ok := tx.Statement.Dest.(map[string]interface{})
				if !ok || tx.Statement.Table != "restore_drill_evidences" || updates[testCase.field] != testCase.value || injected {
					return
				}
				injected = true
				if testCase.zeroRows {
					tx.Statement.AddClause(clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: "1 = 0"}}})
					return
				}
				_ = tx.AddError(errors.New("INTERNAL_DRILL_PHASE_PERSISTENCE_CANARY"))
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Callback().Update().Remove(callbackName) })

			fixture.manager.executeDrill(&fixture.policy, fixture.task, fixture.sandbox, runID)
			if !injected {
				t.Fatal("phase evidence persistence fault was not injected")
			}
			var run model.TaskRun
			var evidence model.RestoreDrillEvidence
			if err := db.First(&run, runID).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.First(&evidence, "task_run_id = ?", runID).Error; err != nil {
				t.Fatal(err)
			}
			if run.Status != model.TaskRunStatusFailed || evidence.Status != model.TaskRunStatusFailed || evidence.ConfidenceEligible {
				t.Fatalf("phase persistence failure committed positive evidence: TaskRun=%q Evidence=%q eligible=%v",
					run.Status, evidence.Status, evidence.ConfidenceEligible)
			}
			if evidence.FailedStep != testCase.failedStep {
				t.Fatalf("phase persistence failure step=%q, want %q", evidence.FailedStep, testCase.failedStep)
			}
			if strings.Contains(run.LastError, "INTERNAL_DRILL_PHASE_PERSISTENCE_CANARY") {
				t.Fatalf("phase persistence failure leaked internal detail: %q", run.LastError)
			}
		})
	}
}

func TestFinalizeDrillRunRejectsSuccessWhenEvidencePhasesAreIncomplete(t *testing.T) {
	db := openDrillTestDB(t)
	fixture := setupDrillEvidenceFixture(t, db)
	runID := createTestTaskRun(t, db, fixture.task.ID, "drill")
	started, err := fixture.manager.startDrillRun(runID, model.RestoreDrillEvidence{
		PolicyID: fixture.policy.ID, TaskID: fixture.task.ID, SandboxNodeID: fixture.sandbox.ID,
		SandboxPath: fixture.policy.DrillRestorePath, RestoreStatus: model.TaskRunStatusRunning,
		VerifyStatus: model.TaskRunStatusPending, PostVerifyStatus: model.TaskRunStatusSkipped,
		CleanupStatus: model.TaskRunStatusSkipped,
	}, time.Now().UTC().Add(-time.Second))
	if err != nil || !started {
		t.Fatalf("start incomplete drill pair: started=%v err=%v", started, err)
	}

	won, _, err := fixture.manager.finalizeDrillRun(
		runID, model.TaskRunStatusSuccess, "", time.Now().UTC(),
		map[string]interface{}{"failed_step": "", "confidence_eligible": true},
	)
	if err == nil || won {
		t.Fatalf("incomplete Evidence phases committed success: won=%v err=%v", won, err)
	}
	var run model.TaskRun
	var evidence model.RestoreDrillEvidence
	if err := db.First(&run, runID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&evidence, "task_run_id = ?", runID).Error; err != nil {
		t.Fatal(err)
	}
	if run.Status != model.TaskRunStatusRunning || evidence.Status != model.TaskRunStatusRunning || evidence.ConfidenceEligible {
		t.Fatalf("rejected success mutated active pair: TaskRun=%q Evidence=%q eligible=%v",
			run.Status, evidence.Status, evidence.ConfidenceEligible)
	}
}

func TestExecuteDrillWritesSecretSafePhaseCredentialAudit(t *testing.T) {
	db := openDrillTestDB(t)
	fixture := setupDrillEvidenceFixture(t, db)
	fixture.policy.DrillPreVerify = "pre-check-token=FAKE_PRE_VERIFY_TOKEN_FOR_TEST_ONLY"
	fixture.policy.DrillPostVerify = "post-check-token=FAKE_POST_VERIFY_TOKEN_FOR_TEST_ONLY"
	fixture.manager.drillSSHScriptFunc = func(_ context.Context, _ model.Node, _ string) error {
		return nil
	}
	runID := createTestTaskRun(t, db, fixture.task.ID, "drill")

	fixture.manager.executeDrill(&fixture.policy, fixture.task, fixture.sandbox, runID)

	var events []model.CredentialAuditEvent
	if err := db.Where("action = ?", "drill.phase").Order("id asc").Find(&events).Error; err != nil {
		t.Fatalf("查询恢复演练凭据审计事件失败: %v", err)
	}
	if len(events) < 6 {
		t.Fatalf("期望写入多个演练阶段凭据审计事件，实际 %d 条: %#v", len(events), events)
	}
	phases := map[string]bool{}
	for _, event := range events {
		if event.TaskID == nil || *event.TaskID != fixture.task.ID || event.TaskRunID == nil || *event.TaskRunID != runID || event.NodeID == nil || *event.NodeID != fixture.sandbox.ID {
			t.Fatalf("演练阶段审计缺少任务/执行/节点上下文: %+v", event)
		}
		if event.PolicyID == nil || *event.PolicyID != fixture.policy.ID {
			t.Fatalf("演练阶段审计缺少策略上下文: %+v", event)
		}
		if event.Purpose != "drill" || event.Outcome != credentialaudit.OutcomeSuccess {
			t.Fatalf("演练阶段审计 purpose/outcome 不符合预期: %+v", event)
		}
		if strings.Contains(event.Metadata, "FAKE_PRE_VERIFY_TOKEN_FOR_TEST_ONLY") || strings.Contains(event.Metadata, "FAKE_POST_VERIFY_TOKEN_FOR_TEST_ONLY") || strings.Contains(event.Metadata, "pre-check-token") || strings.Contains(event.Metadata, "post-check-token") || strings.Contains(event.Metadata, "rm -rf") {
			t.Fatalf("演练阶段审计 metadata 不应包含脚本或命令文本: %s", event.Metadata)
		}
		var metadata map[string]any
		if err := json.Unmarshal([]byte(event.Metadata), &metadata); err != nil {
			t.Fatalf("解析 metadata 失败: %v", err)
		}
		if phase, ok := metadata["phase"].(string); ok {
			phases[phase] = true
		}
	}
	for _, phase := range []string{"sandbox_precheck", "restore", "pre_verify", "verify", "post_verify", "cleanup"} {
		if !phases[phase] {
			t.Fatalf("缺少演练阶段审计事件 phase=%s，已有: %#v", phase, phases)
		}
	}
}

func TestExecuteDrillSanitizesPhaseCredentialAuditErrors(t *testing.T) {
	db := openDrillTestDB(t)
	fixture := setupDrillEvidenceFixture(t, db)
	fixture.manager.drillSSHScriptFunc = func(_ context.Context, _ model.Node, script string) error {
		if script == fixture.policy.DrillVerify {
			return errors.New(`verify failed at /tmp/xirang-drill-evidence on drill-audit.internal.example output=/tmp/raw-output token=FAKE_DRILL_AUDIT_TOKEN_FOR_TEST_ONLY`)
		}
		return nil
	}
	runID := createTestTaskRun(t, db, fixture.task.ID, "drill")

	fixture.manager.executeDrill(&fixture.policy, fixture.task, fixture.sandbox, runID)

	var events []model.CredentialAuditEvent
	if err := db.Where("action = ? AND outcome = ?", "drill.phase", credentialaudit.OutcomeFailure).Find(&events).Error; err != nil {
		t.Fatalf("查询恢复演练失败凭据审计事件失败: %v", err)
	}
	if len(events) == 0 {
		t.Fatalf("期望写入失败阶段凭据审计事件")
	}
	for _, event := range events {
		assertTaskRuntimeTextSanitized(t, event.ErrorMessage, []string{"/tmp/xirang-drill-evidence", "drill-audit.internal.example", "/tmp/raw-output", "FAKE_DRILL_AUDIT_TOKEN_FOR_TEST_ONLY"})
	}
}

func TestExecuteDrillRecordsFailureEvidence(t *testing.T) {
	db := openDrillTestDB(t)
	fixture := setupDrillEvidenceFixture(t, db)
	fixture.manager.drillSSHScriptFunc = func(_ context.Context, _ model.Node, script string) error {
		if script == fixture.policy.DrillVerify {
			return errors.New("verify mismatch token=FAKE_DRILL_TOKEN_FOR_TEST_ONLY")
		}
		return nil
	}
	runID := createTestTaskRun(t, db, fixture.task.ID, "drill")

	fixture.manager.executeDrill(&fixture.policy, fixture.task, fixture.sandbox, runID)

	var evidence model.RestoreDrillEvidence
	if err := db.First(&evidence, "task_run_id = ?", runID).Error; err != nil {
		t.Fatalf("查询恢复演练证据失败: %v", err)
	}
	if evidence.Status != "failed" || evidence.ConfidenceEligible {
		t.Fatalf("失败演练不能作为正向证据，实际 status=%s eligible=%v", evidence.Status, evidence.ConfidenceEligible)
	}
	if evidence.FailedStep != "verify" || evidence.VerifyStatus != "failed" {
		t.Fatalf("期望 verify 失败，实际 failed_step=%s verify=%s", evidence.FailedStep, evidence.VerifyStatus)
	}
	if !strings.Contains(evidence.VerifyError, "token=***") {
		t.Fatalf("期望错误信息被脱敏，实际: %q", evidence.VerifyError)
	}
	var run model.TaskRun
	if err := db.First(&run, runID).Error; err != nil {
		t.Fatalf("查询 TaskRun 失败: %v", err)
	}
	if run.Status != "failed" {
		t.Fatalf("期望 TaskRun 失败，实际: %s", run.Status)
	}
}

func TestExecuteDrillPostVerifyFailureIsNotConfidenceEvidence(t *testing.T) {
	db := openDrillTestDB(t)
	fixture := setupDrillEvidenceFixture(t, db)
	fixture.policy.DrillPostVerify = "post-cleanup-check"
	fixture.manager.drillSSHScriptFunc = func(_ context.Context, _ model.Node, script string) error {
		if script == fixture.policy.DrillPostVerify {
			return errors.New("post verify token=FAKE_POST_VERIFY_TOKEN_FOR_TEST_ONLY")
		}
		return nil
	}
	runID := createTestTaskRun(t, db, fixture.task.ID, "drill")

	fixture.manager.executeDrill(&fixture.policy, fixture.task, fixture.sandbox, runID)

	var evidence model.RestoreDrillEvidence
	if err := db.First(&evidence, "task_run_id = ?", runID).Error; err != nil {
		t.Fatalf("查询恢复演练证据失败: %v", err)
	}
	if evidence.Status != "failed" || evidence.ConfidenceEligible {
		t.Fatalf("post_verify 失败不能作为正向证据，实际 status=%s eligible=%v", evidence.Status, evidence.ConfidenceEligible)
	}
	if evidence.FailedStep != "post_verify" || evidence.PostVerifyStatus != "failed" {
		t.Fatalf("期望 post_verify 失败，实际 failed_step=%s post_verify=%s", evidence.FailedStep, evidence.PostVerifyStatus)
	}
	if !strings.Contains(evidence.PostVerifyError, "token=***") {
		t.Fatalf("期望 post_verify 错误被脱敏，实际: %q", evidence.PostVerifyError)
	}
	if evidence.RestoreStatus != "success" || evidence.VerifyStatus != "success" || evidence.CleanupStatus != "success" {
		t.Fatalf("post_verify 失败仍应保留其他阶段结果，实际 restore=%s verify=%s cleanup=%s", evidence.RestoreStatus, evidence.VerifyStatus, evidence.CleanupStatus)
	}
	var run model.TaskRun
	if err := db.First(&run, runID).Error; err != nil {
		t.Fatalf("查询 TaskRun 失败: %v", err)
	}
	if run.Status != "failed" || !strings.Contains(run.LastError, "token=***") {
		t.Fatalf("期望 TaskRun 失败且错误脱敏，实际 status=%s last_error=%q", run.Status, run.LastError)
	}
}

func TestExecuteDrillDoesNotLogVerifyScriptContent(t *testing.T) {
	db := openDrillTestDB(t)
	fixture := setupDrillEvidenceFixture(t, db)
	fixture.policy.DrillPreVerify = "echo token=FAKE_PRE_VERIFY_TOKEN_FOR_TEST_ONLY"
	fixture.policy.DrillVerify = "echo token=FAKE_VERIFY_TOKEN_FOR_TEST_ONLY"
	fixture.policy.DrillPostVerify = "echo token=FAKE_POST_VERIFY_TOKEN_FOR_TEST_ONLY"
	fixture.manager.drillSSHScriptFunc = func(_ context.Context, _ model.Node, _ string) error {
		return nil
	}
	runID := createTestTaskRun(t, db, fixture.task.ID, "drill")

	fixture.manager.executeDrill(&fixture.policy, fixture.task, fixture.sandbox, runID)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := fixture.manager.Shutdown(ctx); err != nil {
		t.Fatalf("关闭 manager 失败: %v", err)
	}

	var logs []model.TaskLog
	if err := db.Where("task_run_id = ?", runID).Find(&logs).Error; err != nil {
		t.Fatalf("查询 TaskLog 失败: %v", err)
	}
	for _, logEntry := range logs {
		for _, forbidden := range []string{
			"FAKE_PRE_VERIFY_TOKEN_FOR_TEST_ONLY",
			"FAKE_VERIFY_TOKEN_FOR_TEST_ONLY",
			"FAKE_POST_VERIFY_TOKEN_FOR_TEST_ONLY",
			fixture.policy.DrillRestorePath,
			fixture.sandbox.Host,
			fixture.sandbox.Name,
			fixture.policy.Name,
		} {
			if strings.Contains(logEntry.Message, forbidden) {
				t.Fatalf("日志不应包含脚本原文或恢复演练运行时敏感片段 %q，实际日志: %q", forbidden, logEntry.Message)
			}
		}
	}
}

func TestExecuteDrillRejectsUnsafeCleanupBoundary(t *testing.T) {
	db := openDrillTestDB(t)
	fixture := setupDrillEvidenceFixture(t, db)
	fixture.policy.DrillRestorePath = "/etc/xirang-drill"
	var scripts []string
	fixture.manager.drillSSHScriptFunc = func(_ context.Context, _ model.Node, script string) error {
		scripts = append(scripts, script)
		return nil
	}
	runID := createTestTaskRun(t, db, fixture.task.ID, "drill")

	fixture.manager.executeDrill(&fixture.policy, fixture.task, fixture.sandbox, runID)

	var evidence model.RestoreDrillEvidence
	if err := db.First(&evidence, "task_run_id = ?", runID).Error; err != nil {
		t.Fatalf("查询恢复演练证据失败: %v", err)
	}
	if evidence.Status != "failed" || evidence.FailedStep != "restore_path" || evidence.ConfidenceEligible {
		t.Fatalf("期望非法沙箱路径失败且不可作为证据，实际 status=%s step=%s eligible=%v", evidence.Status, evidence.FailedStep, evidence.ConfidenceEligible)
	}
	for _, script := range scripts {
		if strings.HasPrefix(script, "rm -rf ") {
			t.Fatalf("不应对不安全路径执行清理命令，实际脚本: %#v", scripts)
		}
	}
}

// TestTriggerDrillNoAssociatedTask 验证策略无关联备份任务时返回错误。
func TestTriggerDrillNoAssociatedTask(t *testing.T) {
	db := openDrillTestDB(t)
	m := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)

	srcNode := seedDrillNodeWithBackupDir(t, db, "drill-notask-src", "192.168.1.110", "notask-src-bd")
	sandbox := seedDrillNodeWithBackupDir(t, db, "drill-notask-sb", "192.168.1.210", "notask-sb-bd")

	targetID := sandbox.ID
	policy := model.Policy{
		Name:              "policy-no-task",
		SourcePath:        "/tmp/src",
		TargetPath:        "/tmp/dst",
		CronSpec:          "@daily",
		DrillEnabled:      true,
		DrillCron:         "@every 5m",
		DrillTargetNodeID: &targetID,
		DrillRestorePath:  "/tmp/xirang-drill",
	}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatalf("创建策略失败: %v", err)
	}
	if err := db.Create(&model.PolicyNode{PolicyID: policy.ID, NodeID: srcNode.ID}).Error; err != nil {
		t.Fatalf("关联源节点失败: %v", err)
	}

	// 未创建关联任务
	_, err := m.TriggerDrill(policy.ID, nil)
	if err == nil {
		t.Fatal("期望错误（无关联任务），实际无错误")
	}
	if !strings.Contains(err.Error(), "没有关联的备份任务") {
		t.Fatalf("错误信息应提及无关联任务，实际: %v", err)
	}
}
