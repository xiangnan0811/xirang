package verifier

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"xirang/backend/internal/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	return db
}

// TestVerifyBackupUsesLocalTarget 验证普通备份（isRestore=false）走本地目标端校验路径。
// 当 source 和 target 都不含 @ 或 : 且 Node.Host 非空时，以前的启发式会错误进入恢复模式。
// 现在改为通过 isRestore 参数显式标记，确保普通备份始终用本地校验。
func TestVerifyBackupUsesLocalTarget(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()

	// 在 src 和 dst 写入相同的文件
	if err := os.WriteFile(filepath.Join(srcDir, "test.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dstDir, "test.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	task := model.Task{
		RsyncSource: srcDir,
		RsyncTarget: dstDir,
		Node: model.Node{
			Host: "192.168.1.100",
		},
	}

	db := setupTestDB(t)
	var logs []string
	logf := func(level, msg string) {
		logs = append(logs, level+": "+msg)
	}

	// isRestore=false: 走普通备份校验路径
	// 由于没有真实 SSH 连接，dialSSHForTask 会失败。
	// 关键断言：不应进入 VerifyRemoteToRemote 路径。
	result := Verify(context.Background(), task, 0, db, logf, false)

	for _, log := range logs {
		if contains(log, "恢复校验") {
			t.Fatalf("普通备份不应进入恢复校验路径，日志: %v", logs)
		}
	}

	// 由于没有真实 SSH 且 Node.ID=0，应返回 warning（SSH 连接失败）
	// 关键点：它尝试的是普通备份校验路径而非恢复路径
	if result.Status == "passed" && result.Message == "无需校验：未配置同步路径" {
		t.Fatal("不应跳过校验")
	}
}

// TestVerifyRestoreUsesRemoteToRemote 验证恢复任务（isRestore=true）走远程到远程校验路径。
func TestVerifyRestoreUsesRemoteToRemote(t *testing.T) {
	task := model.Task{
		RsyncSource: "/backup/data",
		RsyncTarget: "/var/app/data",
		Node: model.Node{
			Host: "192.168.1.100",
		},
	}

	db := setupTestDB(t)
	var logs []string
	logf := func(level, msg string) {
		logs = append(logs, level+": "+msg)
	}

	// isRestore=true: 走恢复校验路径
	result := Verify(context.Background(), task, 0, db, logf, true)

	// 应进入 VerifyRemoteToRemote → 建立 SSH 连接会失败（Node.ID=0）
	// 关键断言：走的是恢复校验路径（日志包含"恢复校验"）
	foundRestoreLog := false
	for _, log := range logs {
		if contains(log, "恢复校验") {
			foundRestoreLog = true
			break
		}
	}
	if !foundRestoreLog {
		t.Fatalf("恢复任务应进入恢复校验路径，日志: %v", logs)
	}

	// 因为没有真实 SSH，应返回 warning
	if result.Status != "warning" {
		t.Fatalf("期望 warning（SSH 连接失败），实际: %s - %s", result.Status, result.Message)
	}
}

// TestVerifyBackupWithRemotePathNotMisidentified 验证普通备份任务不会因路径不含 @ 而被误判为恢复。
// 这是回归测试：以前的逻辑会将此场景错误地路由到 VerifyRemoteToRemote。
func TestVerifyBackupWithRemotePathNotMisidentified(t *testing.T) {
	task := model.Task{
		RsyncSource: "/var/data",
		RsyncTarget: "/nonexistent/backup/path",
		Node: model.Node{
			Host: "10.0.0.1",
		},
	}

	db := setupTestDB(t)
	var logs []string
	logf := func(level, msg string) {
		logs = append(logs, level+": "+msg)
	}

	// isRestore=false: 即使路径不含 @，也不应进入恢复路径
	Verify(context.Background(), task, 0, db, logf, false)

	for _, log := range logs {
		if contains(log, "恢复校验") {
			t.Fatalf("普通备份（目标路径本地不存在）不应进入恢复校验路径，日志: %v", logs)
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchSubstring(s, substr)
}

func searchSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
