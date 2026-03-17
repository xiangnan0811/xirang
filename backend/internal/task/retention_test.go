package task

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"xirang/backend/internal/model"
)

func TestShellEscape(t *testing.T) {
	cases := []struct {
		name   string
		input  string
		expect string
	}{
		{"空字符串", "", "''"},
		{"简单字符串", "hello", "'hello'"},
		{"包含单引号", "it's", "'it'\"'\"'s'"},
		{"包含空格", "a b", "'a b'"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shellEscape(tc.input)
			if got != tc.expect {
				t.Fatalf("shellEscape(%q): 期望 %q，实际 %q", tc.input, tc.expect, got)
			}
		})
	}
}

func TestExtractResticPassword(t *testing.T) {
	cases := []struct {
		name   string
		input  string
		expect string
	}{
		{"有效 JSON", `{"repository_password": "secret123"}`, "secret123"},
		{"无密码字段", `{"repo": "/backup"}`, ""},
		{"空密码", `{"repository_password": ""}`, ""},
		{"缺少结束引号", `{"repository_password": "broken`, ""},
		{"额外空白", `{"repository_password" : "secret"}`, "secret"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractResticPassword(tc.input)
			if got != tc.expect {
				t.Fatalf("extractResticPassword(%q): 期望 %q，实际 %q", tc.input, tc.expect, got)
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

	m := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, 8, 90)

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
