package handlers

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"xirang/backend/internal/bootstrap"
	"xirang/backend/internal/logger"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// SystemHandler 处理系统级操作（数据库备份等）
type SystemHandler struct {
	db       *gorm.DB
	backupMu sync.Mutex
	now      func() time.Time
	backupID func() string
	chmod    func(string, os.FileMode) error
}

var backupSequence uint64

func NewSystemHandler(db *gorm.DB) *SystemHandler {
	return &SystemHandler{
		db:       db,
		now:      time.Now,
		backupID: newBackupID,
		chmod:    os.Chmod,
	}
}

func newBackupID() string {
	var randomID [8]byte
	if _, err := rand.Read(randomID[:]); err == nil {
		return hex.EncodeToString(randomID[:])
	}
	return fmt.Sprintf("%d-%d", time.Now().UnixNano(), atomic.AddUint64(&backupSequence, 1))
}

func sanitizeBackupID(id string) string {
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "backup"
	}
	return b.String()
}

func isManagedBackupFilename(name string) bool {
	const prefix = "xirang-"
	const suffix = ".db"
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
		return false
	}
	stem := strings.TrimSuffix(strings.TrimPrefix(name, prefix), suffix)
	if len(stem) < len("20060102-150405") || stem[8] != '-' {
		return false
	}
	for i, r := range stem[:15] {
		if i == 8 {
			continue
		}
		if r < '0' || r > '9' {
			return false
		}
	}
	return len(stem) == 15 || (len(stem) > 16 && stem[15] == '-')
}

func (h *SystemHandler) backupDestination(backupDir string, now time.Time) (string, string, error) {
	idGenerator := h.backupID
	if idGenerator == nil {
		idGenerator = newBackupID
	}
	baseID := sanitizeBackupID(idGenerator())
	timestamp := now.Format("20060102-150405")
	for attempt := range 1000 {
		id := baseID
		if attempt > 0 {
			id = fmt.Sprintf("%s-%d", baseID, attempt+1)
		}
		filename := fmt.Sprintf("xirang-%s-%s.db", timestamp, id)
		path := filepath.Join(backupDir, filename)
		_, err := os.Stat(path)
		switch {
		case err == nil:
			continue
		case os.IsNotExist(err):
			return filename, path, nil
		default:
			return "", "", fmt.Errorf("检查备份文件路径失败: %w", err)
		}
	}
	return "", "", fmt.Errorf("无法生成唯一的备份文件名")
}

func isSQLiteRuntime() bool {
	dbType := strings.TrimSpace(os.Getenv("DB_TYPE"))
	return dbType == "" || strings.EqualFold(dbType, "sqlite")
}

// BackupDB godoc
// @Summary      备份数据库
// @Description  创建 SQLite 数据库的时间戳一致性备份（仅 SQLite）
// @Tags         system
// @Security     Bearer
// @Produce      json
// @Success      200  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Failure      501  {object}  handlers.Response
// @Router       /system/backup-db [post]
func (h *SystemHandler) BackupDB(c *gin.Context) {
	if !isSQLiteRuntime() {
		respondNotImplemented(c, "当前仅支持 SQLite 数据库备份")
		return
	}

	h.backupMu.Lock()
	defer h.backupMu.Unlock()

	dbPath := os.Getenv("SQLITE_PATH")
	if dbPath == "" {
		dbPath = "./xirang.db"
	}

	backupDir := os.Getenv("DB_BACKUP_DIR")
	if backupDir == "" {
		backupDir = filepath.Join(filepath.Dir(dbPath), "backups")
	}

	// 备份目录可能已在更宽松的调用方 umask 下创建，因此创建后仍需显式收紧并验证。
	if err := os.MkdirAll(backupDir, 0700); err != nil {
		logger.Log.Error().Err(err).Msg("创建备份目录失败")
		respondInternalError(c, err)
		return
	}
	if err := h.ensureExactMode(backupDir, 0700); err != nil {
		logger.Log.Error().Err(err).Msg("收紧备份目录权限失败")
		respondInternalError(c, err)
		return
	}

	now := time.Now
	if h.now != nil {
		now = h.now
	}
	backupFilename, backupPath, err := h.backupDestination(backupDir, now())
	if err != nil {
		logger.Log.Error().Err(err).Msg("生成备份文件路径失败")
		respondInternalError(c, err)
		return
	}

	checksum, size, err := h.createSQLiteBackup(backupPath)
	if err != nil {
		removeBackupArtifacts(backupPath)
		logger.Log.Error().Err(err).Msg("备份数据库文件失败")
		respondInternalError(c, err)
		return
	}

	// 写入校验和文件；失败时不能留下无法验证的数据库文件。
	checksumPath := backupPath + ".sha256"
	checksumContent := fmt.Sprintf("%s  %s\n", checksum, backupFilename)
	if err := os.WriteFile(checksumPath, []byte(checksumContent), 0600); err != nil {
		removeBackupArtifacts(backupPath)
		logger.Log.Error().Err(err).Msg("写入校验和文件失败")
		respondInternalError(c, err)
		return
	}
	if err := h.ensureExactMode(checksumPath, 0600); err != nil {
		removeBackupArtifacts(backupPath)
		logger.Log.Error().Err(err).Msg("收紧校验和文件权限失败")
		respondInternalError(c, err)
		return
	}

	// 清理旧备份。仅管理符合 xirang-YYYYMMDD-HHMMSS[-id].db
	// 命名约定的文件，避免删除备份目录中的其他数据库文件。
	maxBackups := 20
	if envMax := os.Getenv("DB_BACKUP_MAX_COUNT"); envMax != "" {
		if n, err := strconv.Atoi(envMax); err == nil && n > 0 {
			maxBackups = n
		}
	}
	if cleanEntries, err := os.ReadDir(backupDir); err == nil {
		var dbFiles []os.DirEntry
		for _, e := range cleanEntries {
			if !e.IsDir() && isManagedBackupFilename(e.Name()) {
				dbFiles = append(dbFiles, e)
			}
		}
		if len(dbFiles) > maxBackups {
			sort.Slice(dbFiles, func(i, j int) bool {
				return dbFiles[i].Name() < dbFiles[j].Name()
			})
			for _, f := range dbFiles[:len(dbFiles)-maxBackups] {
				os.Remove(filepath.Join(backupDir, f.Name()))           //nolint:errcheck
				os.Remove(filepath.Join(backupDir, f.Name()+".sha256")) //nolint:errcheck
			}
		}
	}

	respondOK(c, gin.H{
		"filename": backupFilename,
		"path":     backupPath,
		"size":     size,
		"sha256":   checksum,
	})
}

func removeBackupArtifacts(backupPath string) {
	_ = os.Remove(backupPath)
	_ = os.Remove(backupPath + ".sha256")
}

// ListBackups godoc
// @Summary      列出数据库备份
// @Description  列出已有的 SQLite 数据库备份文件（仅 SQLite）
// @Tags         system
// @Security     Bearer
// @Produce      json
// @Success      200  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Failure      501  {object}  handlers.Response
// @Router       /system/backups [get]
func (h *SystemHandler) ListBackups(c *gin.Context) {
	if !isSQLiteRuntime() {
		respondNotImplemented(c, "当前仅支持 SQLite 数据库备份")
		return
	}

	dbPath := os.Getenv("SQLITE_PATH")
	if dbPath == "" {
		dbPath = "./xirang.db"
	}

	backupDir := os.Getenv("DB_BACKUP_DIR")
	if backupDir == "" {
		backupDir = filepath.Join(filepath.Dir(dbPath), "backups")
	}

	entries, err := os.ReadDir(backupDir)
	if err != nil {
		if os.IsNotExist(err) {
			respondOK(c, []gin.H{})
			return
		}
		logger.Log.Error().Err(err).Msg("读取备份目录失败")
		respondInternalError(c, err)
		return
	}

	backups := make([]gin.H, 0)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".db") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}

		item := gin.H{
			"filename":   entry.Name(),
			"size":       info.Size(),
			"created_at": info.ModTime().Format(time.RFC3339),
		}

		// 尝试读取对应的 .sha256 文件
		checksumPath := filepath.Join(backupDir, entry.Name()+".sha256")
		if data, err := os.ReadFile(checksumPath); err == nil {
			parts := strings.Fields(string(data))
			if len(parts) > 0 {
				item["sha256"] = parts[0]
			}
		}

		backups = append(backups, item)
	}

	// 按文件名降序排列（最新的在前）
	sort.Slice(backups, func(i, j int) bool {
		return backups[i]["filename"].(string) > backups[j]["filename"].(string)
	})

	respondOK(c, backups)
}

// EncryptionStatus godoc
// @Summary      查询加密迁移健康状态
// @Description  返回 enc:v1: 残留数量与策略演练脚本明文残留数量。运维侧用于
// @Description  判断敏感字段是否均已迁移到 V2，以及历史明文 drill 脚本是否已密封。
// @Description  healthy=true 表示两者均为 0。
// @Tags         system
// @Security     Bearer
// @Produce      json
// @Success      200  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Failure      403  {object}  handlers.Response
// @Router       /system/encryption-status [get]
func (h *SystemHandler) EncryptionStatus(c *gin.Context) {
	v1Count, err := bootstrap.CountV1EncryptedData(h.db)
	if err != nil {
		// Never report healthy when residual counts cannot be verified.
		respondInternalError(c, err)
		return
	}
	plainDrill, err := bootstrap.CountPlaintextPolicyDrillScripts(h.db)
	if err != nil {
		respondInternalError(c, err)
		return
	}
	respondOK(c, gin.H{
		"v1_remaining_count":                 v1Count,
		"plaintext_drill_script_field_count": plainDrill,
		"healthy":                            v1Count == 0 && plainDrill == 0,
	})
}

func (h *SystemHandler) ensureExactMode(path string, expected os.FileMode) error {
	chmod := h.chmod
	if chmod == nil {
		chmod = os.Chmod
	}
	if err := chmod(path, expected); err != nil {
		return fmt.Errorf("设置 %s 权限为 %04o 失败: %w", path, expected.Perm(), err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("验证 %s 权限失败: %w", path, err)
	}
	if actual := info.Mode().Perm(); actual != expected.Perm() {
		return fmt.Errorf("%s 权限验证失败: 期望 %04o，实际 %04o", path, expected.Perm(), actual)
	}
	return nil
}

func (h *SystemHandler) createSQLiteBackup(backupPath string) (checksum string, size int64, err error) {
	escapedPath := strings.ReplaceAll(backupPath, "'", "''")
	if err := h.db.Exec(fmt.Sprintf("VACUUM INTO '%s'", escapedPath)).Error; err != nil {
		return "", 0, fmt.Errorf("执行 SQLite 一致性备份失败: %w", err)
	}
	if err := h.ensureExactMode(backupPath, 0600); err != nil {
		return "", 0, err
	}
	return checksumFile(backupPath)
}

func checksumFile(path string) (checksum string, size int64, err error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", 0, fmt.Errorf("读取备份文件大小失败: %w", err)
	}
	size = info.Size()

	file, err := os.Open(path)
	if err != nil {
		return "", 0, fmt.Errorf("打开备份文件失败: %w", err)
	}
	defer file.Close() //nolint:errcheck

	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", 0, fmt.Errorf("计算备份文件校验和失败: %w", err)
	}

	return fmt.Sprintf("%x", hasher.Sum(nil)), size, nil
}
