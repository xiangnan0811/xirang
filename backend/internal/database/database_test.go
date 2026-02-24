package database

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"xirang/backend/internal/config"
)

func TestOpenSQLiteEnablesWALPragmas(t *testing.T) {
	tempDir := t.TempDir()
	sqlitePath := filepath.Join(tempDir, "xirang-test.db")

	cfg := config.Config{
		DBType:     "sqlite",
		SQLitePath: sqlitePath,
	}
	db, err := Open(cfg)
	if err != nil {
		t.Fatalf("打开 sqlite 数据库失败: %v", err)
	}

	t.Cleanup(func() {
		sqlDB, dbErr := db.DB()
		if dbErr == nil {
			_ = sqlDB.Close()
		}
		_ = os.Remove(sqlitePath)
	})

	var journalMode string
	if err := db.Raw("PRAGMA journal_mode;").Scan(&journalMode).Error; err != nil {
		t.Fatalf("查询 journal_mode 失败: %v", err)
	}
	if strings.ToLower(strings.TrimSpace(journalMode)) != "wal" {
		t.Fatalf("期望 SQLite journal_mode=wal，实际: %s", journalMode)
	}

	var busyTimeout int
	if err := db.Raw("PRAGMA busy_timeout;").Scan(&busyTimeout).Error; err != nil {
		t.Fatalf("查询 busy_timeout 失败: %v", err)
	}
	if busyTimeout < 5000 {
		t.Fatalf("期望 busy_timeout >= 5000ms，实际: %d", busyTimeout)
	}

	var synchronous int
	if err := db.Raw("PRAGMA synchronous;").Scan(&synchronous).Error; err != nil {
		t.Fatalf("查询 synchronous 失败: %v", err)
	}
	if synchronous != 1 {
		t.Fatalf("期望 synchronous=NORMAL(1)，实际: %d", synchronous)
	}
}
