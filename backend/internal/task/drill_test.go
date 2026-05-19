package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/credentialaudit"
	"xirang/backend/internal/model"

	"github.com/robfig/cron/v3"
	"gorm.io/gorm"
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
	m := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, 8, 90)

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
	m := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, 8, 90)

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
	m := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, 8, 90)

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
	m := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, 8, 90)
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
	m := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, 8, 90)
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
	m := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, 8, 90)

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
	m := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, 8, 90)

	_, err := m.TriggerDrill(99999)
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
	m := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, 8, 90)

	policy := model.Policy{
		Name:         "policy-drill-disabled",
		SourcePath:   "/tmp/src",
		TargetPath:   "/tmp/dst",
		CronSpec:     "@daily",
		DrillEnabled: false,
	}
	db.Create(&policy)

	_, err := m.TriggerDrill(policy.ID)
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
	m := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, 8, 90)

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

	_, err := m.TriggerDrill(policy.ID)
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
	m := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, 8, 90)

	policy := model.Policy{
		Name:       "policy-no-tasks",
		SourcePath: "/tmp/src",
		TargetPath: "/tmp/dst",
		CronSpec:   "@daily",
	}
	db.Create(&policy)

	_, err := m.findTaskForPolicy(policy.ID)
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
	m := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, 8, 90)

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

	found, err := m.findTaskForPolicy(policy.ID)
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
	m := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, 8, 90)

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

	found, err := m.findTaskForPolicy(policy.ID)
	if err != nil {
		t.Fatalf("查找任务失败: %v", err)
	}
	if found.ID != task2.ID {
		t.Fatalf("应返回有成功记录的任务 (id=%d)，实际 (id=%d)", task2.ID, found.ID)
	}
}

// TestTriggerDrillSandboxNotFound 测试沙箱节点不存在。
func TestTriggerDrillSandboxNotFound(t *testing.T) {
	db := openManagerTestDB(t)
	m := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, 8, 90)

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

	_, err := m.TriggerDrill(policy.ID)
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
	m := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, 8, 90)

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
	m := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, 8, 90)

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
	m := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, 8, 90)

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

	runID, err := m.TriggerDrill(policy.ID)
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

type drillEvidenceFixture struct {
	manager *Manager
	policy  model.Policy
	task    model.Task
	sandbox model.Node
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
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, 8, 90)
	manager.drillRestoreFunc = func(_ context.Context, _ model.Task, _ model.Node, _ string, logf func(string, string)) error {
		logf("info", "restore-ok")
		return nil
	}
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
		if strings.Contains(logEntry.Message, "FAKE_PRE_VERIFY_TOKEN_FOR_TEST_ONLY") ||
			strings.Contains(logEntry.Message, "FAKE_VERIFY_TOKEN_FOR_TEST_ONLY") ||
			strings.Contains(logEntry.Message, "FAKE_POST_VERIFY_TOKEN_FOR_TEST_ONLY") {
			t.Fatalf("日志不应包含脚本原文或敏感 token，实际日志: %q", logEntry.Message)
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
	m := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, 8, 90)

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
	_, err := m.TriggerDrill(policy.ID)
	if err == nil {
		t.Fatal("期望错误（无关联任务），实际无错误")
	}
	if !strings.Contains(err.Error(), "没有关联的备份任务") {
		t.Fatalf("错误信息应提及无关联任务，实际: %v", err)
	}
}
