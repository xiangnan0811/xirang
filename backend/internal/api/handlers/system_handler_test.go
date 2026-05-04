package handlers

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openSystemHandlerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared&_loc=UTC"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	return db
}

func openSystemHandlerSQLiteFileDB(t *testing.T, dbPath string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(dbPath+"?_loc=UTC"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开 SQLite 文件数据库失败: %v", err)
	}
	return db
}

func TestSystemHandlerBackupDBRejectsNonSQLite(t *testing.T) {
	t.Setenv("DB_TYPE", "postgres")
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/system/backup-db", nil)

	handler := NewSystemHandler(openSystemHandlerTestDB(t))
	handler.BackupDB(ctx)

	if recorder.Code != http.StatusNotImplemented {
		t.Fatalf("期望状态码 %d，实际 %d", http.StatusNotImplemented, recorder.Code)
	}
}

func TestSystemHandlerListBackupsRejectsNonSQLite(t *testing.T) {
	t.Setenv("DB_TYPE", "postgres")
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/v1/system/backups", nil)

	handler := NewSystemHandler(openSystemHandlerTestDB(t))
	handler.ListBackups(ctx)

	if recorder.Code != http.StatusNotImplemented {
		t.Fatalf("期望状态码 %d，实际 %d", http.StatusNotImplemented, recorder.Code)
	}
}

func TestSystemHandlerBackupDBUsesSQLiteByDefault(t *testing.T) {
	t.Setenv("DB_TYPE", "")
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "xirang.db")
	t.Setenv("SQLITE_PATH", dbPath)
	backupDir := filepath.Join(tmpDir, "backups")
	t.Setenv("DB_BACKUP_DIR", backupDir)
	gin.SetMode(gin.TestMode)

	db := openSystemHandlerSQLiteFileDB(t, dbPath)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("获取底层 sqlite 连接失败: %v", err)
	}
	defer sqlDB.Close() //nolint:errcheck
	if err := db.Exec("PRAGMA journal_mode=WAL").Error; err != nil {
		t.Fatalf("启用 WAL 模式失败: %v", err)
	}
	if err := db.Exec("CREATE TABLE IF NOT EXISTS backup_test (id INTEGER PRIMARY KEY, value TEXT)").Error; err != nil {
		t.Fatalf("初始化测试表失败: %v", err)
	}
	if err := db.Exec("INSERT INTO backup_test (value) VALUES (?)", "sqlite-backup-test").Error; err != nil {
		t.Fatalf("写入测试数据失败: %v", err)
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/system/backup-db", nil)

	handler := NewSystemHandler(db)
	handler.BackupDB(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("期望状态码 %d，实际 %d", http.StatusOK, recorder.Code)
	}

	entries, err := os.ReadDir(backupDir)
	if err != nil {
		t.Fatalf("读取备份目录失败: %v", err)
	}

	var hasDB, hasChecksum bool
	var backupDBPath string
	for _, entry := range entries {
		switch filepath.Ext(entry.Name()) {
		case ".db":
			hasDB = true
			backupDBPath = filepath.Join(backupDir, entry.Name())
		case ".sha256":
			hasChecksum = true
		}
	}
	if !hasDB || !hasChecksum {
		t.Fatalf("期望生成 .db 和 .sha256 备份文件，实际 hasDB=%v hasChecksum=%v", hasDB, hasChecksum)
	}

	backupDB := openSystemHandlerSQLiteFileDB(t, backupDBPath)
	backupSQLDB, err := backupDB.DB()
	if err != nil {
		t.Fatalf("获取备份 sqlite 连接失败: %v", err)
	}
	defer backupSQLDB.Close() //nolint:errcheck

	var value string
	if err := backupDB.Raw("SELECT value FROM backup_test LIMIT 1").Scan(&value).Error; err != nil {
		t.Fatalf("读取备份数据失败: %v", err)
	}
	if value != "sqlite-backup-test" {
		t.Fatalf("备份内容不符合预期，实际: %q", value)
	}
}
