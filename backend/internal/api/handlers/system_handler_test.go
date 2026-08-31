package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openSystemHandlerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+handlerTestDBName(t)+"?mode=memory&cache=shared&_loc=UTC"), &gorm.Config{})
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
	var resp Response
	if err := json.NewDecoder(recorder.Body).Decode(&resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if resp.Code != http.StatusNotImplemented || resp.Message != "当前仅支持 SQLite 数据库备份" {
		t.Fatalf("期望标准 501 响应，实际: %+v", resp)
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
	var resp Response
	if err := json.NewDecoder(recorder.Body).Decode(&resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if resp.Code != http.StatusNotImplemented || resp.Message != "当前仅支持 SQLite 数据库备份" {
		t.Fatalf("期望标准 501 响应，实际: %+v", resp)
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
	if err := os.MkdirAll(backupDir, 0777); err != nil {
		t.Fatalf("创建宽权限备份目录失败: %v", err)
	}
	if err := os.Chmod(backupDir, 0777); err != nil {
		t.Fatalf("设置宽权限备份目录失败: %v", err)
	}

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
	var resp struct {
		Code int `json:"code"`
		Data struct {
			Filename string `json:"filename"`
			Path     string `json:"path"`
			Size     int64  `json:"size"`
			SHA256   string `json:"sha256"`
		} `json:"data"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if resp.Code != http.StatusOK || resp.Data.Filename == "" || resp.Data.Path == "" || resp.Data.Size <= 0 || resp.Data.SHA256 == "" {
		t.Fatalf("备份响应缺少必要字段: %+v", resp)
	}

	entries, err := os.ReadDir(backupDir)
	if err != nil {
		t.Fatalf("读取备份目录失败: %v", err)
	}

	var hasDB, hasChecksum bool
	var backupDBPath, checksumPath string
	for _, entry := range entries {
		switch filepath.Ext(entry.Name()) {
		case ".db":
			hasDB = true
			backupDBPath = filepath.Join(backupDir, entry.Name())
		case ".sha256":
			hasChecksum = true
			checksumPath = filepath.Join(backupDir, entry.Name())
		}
	}
	if !hasDB || !hasChecksum {
		t.Fatalf("期望生成 .db 和 .sha256 备份文件，实际 hasDB=%v hasChecksum=%v", hasDB, hasChecksum)
	}
	for path, expected := range map[string]os.FileMode{
		backupDir:    0700,
		backupDBPath: 0600,
		checksumPath: 0600,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("读取权限失败 %s: %v", path, err)
		}
		if actual := info.Mode().Perm(); actual != expected {
			t.Fatalf("期望 %s 权限 %04o，实际 %04o", path, expected, actual)
		}
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

func TestSystemHandlerBackupDBPrivatePermissionsIgnoreCallerUmask(t *testing.T) {
	const helperEnv = "XR_SYSTEM_BACKUP_UMASK_HELPER"
	if maskText := os.Getenv(helperEnv); maskText != "" {
		mask, err := strconv.ParseUint(maskText, 8, 32)
		if err != nil {
			t.Fatalf("解析 helper umask 失败: %v", err)
		}
		previous := syscall.Umask(int(mask))
		defer syscall.Umask(previous)

		t.Setenv("DB_TYPE", "sqlite")
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "xirang.db")
		backupDir := filepath.Join(tmpDir, "backups")
		t.Setenv("SQLITE_PATH", dbPath)
		t.Setenv("DB_BACKUP_DIR", backupDir)
		gin.SetMode(gin.TestMode)

		db := openSystemHandlerSQLiteFileDB(t, dbPath)
		sqlDB, err := db.DB()
		if err != nil {
			t.Fatalf("获取底层 sqlite 连接失败: %v", err)
		}
		defer sqlDB.Close() //nolint:errcheck
		if err := db.Exec("CREATE TABLE permission_probe (id INTEGER PRIMARY KEY)").Error; err != nil {
			t.Fatalf("初始化权限测试表失败: %v", err)
		}

		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/system/backup-db", nil)
		NewSystemHandler(db).BackupDB(ctx)
		if recorder.Code != http.StatusOK {
			t.Fatalf("备份应成功，状态码=%d body=%s", recorder.Code, recorder.Body.String())
		}
		var response struct {
			Data struct {
				Path string `json:"path"`
			} `json:"data"`
		}
		if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
			t.Fatalf("解析备份响应失败: %v", err)
		}
		for path, expected := range map[string]os.FileMode{
			backupDir:                      0700,
			response.Data.Path:             0600,
			response.Data.Path + ".sha256": 0600,
		} {
			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("读取权限失败 %s: %v", path, err)
			}
			if actual := info.Mode().Perm(); actual != expected {
				t.Fatalf("caller umask %s: 期望 %s 权限 %04o，实际 %04o", maskText, path, expected, actual)
			}
		}
		return
	}

	for _, mask := range []string{"000", "022"} {
		t.Run("umask_"+mask, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestSystemHandlerBackupDBPrivatePermissionsIgnoreCallerUmask$", "-test.count=1")
			cmd.Env = append(os.Environ(), helperEnv+"="+mask)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("helper failed for umask %s: %v\n%s", mask, err, output)
			}
		})
	}
}

func TestSystemHandlerBackupDBPermissionHardeningFailureRemovesArtifacts(t *testing.T) {
	t.Setenv("DB_TYPE", "sqlite")
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "xirang.db")
	backupDir := filepath.Join(tmpDir, "backups")
	t.Setenv("SQLITE_PATH", dbPath)
	t.Setenv("DB_BACKUP_DIR", backupDir)
	gin.SetMode(gin.TestMode)

	db := openSystemHandlerSQLiteFileDB(t, dbPath)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("获取底层 sqlite 连接失败: %v", err)
	}
	defer sqlDB.Close() //nolint:errcheck
	if err := db.Exec("CREATE TABLE permission_failure_probe (id INTEGER PRIMARY KEY)").Error; err != nil {
		t.Fatalf("初始化权限失败测试表失败: %v", err)
	}

	handler := NewSystemHandler(db)
	handler.now = func() time.Time {
		return time.Date(2025, time.January, 2, 3, 4, 5, 0, time.UTC)
	}
	handler.backupID = func() string { return "permission-failure" }
	handler.chmod = func(path string, mode os.FileMode) error {
		if path != backupDir {
			return os.ErrPermission
		}
		return os.Chmod(path, mode)
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/system/backup-db", nil)
	handler.BackupDB(ctx)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("权限加固失败应 fail closed，状态码=%d body=%s", recorder.Code, recorder.Body.String())
	}
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		t.Fatalf("读取备份目录失败: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("权限加固失败不应留下备份产物，实际: %v", entries)
	}
}

func TestSystemHandlerBackupDBSameSecondConcurrentCallsUseUniqueNames(t *testing.T) {
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
	if err := db.Exec("CREATE TABLE IF NOT EXISTS backup_test (id INTEGER PRIMARY KEY, value TEXT)").Error; err != nil {
		t.Fatalf("初始化测试表失败: %v", err)
	}

	handler := NewSystemHandler(db)
	handler.now = func() time.Time {
		return time.Date(2025, time.January, 2, 3, 4, 5, 0, time.UTC)
	}
	handler.backupID = func() string { return "fixed" }

	type result struct {
		status int
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/system/backup-db", nil)
			handler.BackupDB(ctx)
			results <- result{status: recorder.Code}
		}()
	}
	wg.Wait()
	close(results)

	for got := range results {
		if got.status != http.StatusOK {
			t.Fatalf("并发备份应成功，实际状态码 %d", got.status)
		}
	}

	entries, err := os.ReadDir(backupDir)
	if err != nil {
		t.Fatalf("读取备份目录失败: %v", err)
	}
	names := make(map[string]struct{})
	for _, entry := range entries {
		if isManagedBackupFilename(entry.Name()) {
			names[entry.Name()] = struct{}{}
		}
	}
	if len(names) != 2 {
		t.Fatalf("同一秒并发备份应生成两个唯一文件，实际 %d 个: %v", len(names), names)
	}
}

func TestSystemHandlerBackupDBCleanupPreservesUnmanagedDatabase(t *testing.T) {
	t.Setenv("DB_TYPE", "")
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "xirang.db")
	t.Setenv("SQLITE_PATH", dbPath)
	backupDir := filepath.Join(tmpDir, "backups")
	t.Setenv("DB_BACKUP_DIR", backupDir)
	t.Setenv("DB_BACKUP_MAX_COUNT", "1")
	gin.SetMode(gin.TestMode)

	if err := os.MkdirAll(backupDir, 0750); err != nil {
		t.Fatalf("创建备份目录失败: %v", err)
	}
	unmanagedPath := filepath.Join(backupDir, "unmanaged.db")
	if err := os.WriteFile(unmanagedPath, []byte("keep"), 0640); err != nil {
		t.Fatalf("创建非托管数据库文件失败: %v", err)
	}

	db := openSystemHandlerSQLiteFileDB(t, dbPath)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("获取底层 sqlite 连接失败: %v", err)
	}
	defer sqlDB.Close() //nolint:errcheck
	if err := db.Exec("CREATE TABLE IF NOT EXISTS backup_test (id INTEGER PRIMARY KEY)").Error; err != nil {
		t.Fatalf("初始化测试表失败: %v", err)
	}

	handler := NewSystemHandler(db)
	handler.now = func() time.Time {
		return time.Date(2025, time.January, 2, 3, 4, 5, 0, time.UTC)
	}
	handler.backupID = func() string { return "managed" }
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/system/backup-db", nil)
	handler.BackupDB(ctx)
	if recorder.Code != http.StatusOK {
		t.Fatalf("备份应成功，实际状态码 %d", recorder.Code)
	}
	if _, err := os.Stat(unmanagedPath); err != nil {
		t.Fatalf("清理旧备份不应删除非托管文件: %v", err)
	}
}
