package handlers

import (
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"xirang/backend/internal/bootstrap"
	"xirang/backend/internal/logger"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// SystemHandler 处理系统级操作（数据库备份等）
type SystemHandler struct {
	db *gorm.DB
}

func NewSystemHandler(db *gorm.DB) *SystemHandler {
	return &SystemHandler{db: db}
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
		c.JSON(http.StatusNotImplemented, gin.H{"error": "当前仅支持 SQLite 数据库备份"})
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

	// 确保备份目录存在
	if err := os.MkdirAll(backupDir, 0750); err != nil {
		logger.Log.Error().Err(err).Msg("创建备份目录失败")
		respondInternalError(c, err)
		return
	}

	// 生成带时间戳的备份文件名
	timestamp := time.Now().Format("20060102-150405")
	backupFilename := fmt.Sprintf("xirang-%s.db", timestamp)
	backupPath := filepath.Join(backupDir, backupFilename)

	checksum, size, err := createSQLiteBackup(h.db, backupPath)
	if err != nil {
		logger.Log.Error().Err(err).Msg("备份数据库文件失败")
		respondInternalError(c, err)
		return
	}

	// 写入校验和文件
	checksumPath := backupPath + ".sha256"
	checksumContent := fmt.Sprintf("%s  %s\n", checksum, backupFilename)
	if err := os.WriteFile(checksumPath, []byte(checksumContent), 0640); err != nil {
		logger.Log.Error().Err(err).Msg("写入校验和文件失败")
		respondInternalError(c, err)
		return
	}

	// 清理旧备份
	maxBackups := 20
	if envMax := os.Getenv("DB_BACKUP_MAX_COUNT"); envMax != "" {
		if n, err := strconv.Atoi(envMax); err == nil && n > 0 {
			maxBackups = n
		}
	}
	if cleanEntries, err := os.ReadDir(backupDir); err == nil {
		var dbFiles []os.DirEntry
		for _, e := range cleanEntries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".db") {
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
		"size":     size,
		"sha256":   checksum,
	})
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
		c.JSON(http.StatusNotImplemented, gin.H{"error": "当前仅支持 SQLite 数据库备份"})
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
// @Summary      查询加密 V1 残留状态
// @Description  返回当前数据库中仍以 enc:v1: 前缀加密的字段总数。运维侧用于
// @Description  判断是否所有敏感字段都已迁移到 V2 (argon2id)，是后续退役 V1
// @Description  解密支持的前置条件。返回 0 表示可以安全裁掉 V1 兼容代码。
// @Tags         system
// @Security     Bearer
// @Produce      json
// @Success      200  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Failure      403  {object}  handlers.Response
// @Router       /system/encryption-status [get]
func (h *SystemHandler) EncryptionStatus(c *gin.Context) {
	count := bootstrap.CountV1EncryptedData(h.db)
	respondOK(c, gin.H{
		"v1_remaining_count": count,
		"healthy":            count == 0,
	})
}

func createSQLiteBackup(db *gorm.DB, backupPath string) (checksum string, size int64, err error) {
	escapedPath := strings.ReplaceAll(backupPath, "'", "''")
	if err := db.Exec(fmt.Sprintf("VACUUM INTO '%s'", escapedPath)).Error; err != nil {
		return "", 0, fmt.Errorf("执行 SQLite 一致性备份失败: %w", err)
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
