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

// BackupDB 创建 SQLite 数据库的时间戳备份
func (h *SystemHandler) BackupDB(c *gin.Context) {
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
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("创建备份目录失败: %v", err)})
		return
	}

	// 执行 WAL checkpoint，确保所有数据写入主数据库文件
	if err := h.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)").Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("执行 WAL checkpoint 失败: %v", err)})
		return
	}

	// 生成带时间戳的备份文件名
	timestamp := time.Now().Format("20060102-150405")
	backupFilename := fmt.Sprintf("xirang-%s.db", timestamp)
	backupPath := filepath.Join(backupDir, backupFilename)

	// 复制数据库文件
	checksum, size, err := copyFileWithChecksum(dbPath, backupPath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("备份数据库文件失败: %v", err)})
		return
	}

	// 写入校验和文件
	checksumPath := backupPath + ".sha256"
	checksumContent := fmt.Sprintf("%s  %s\n", checksum, backupFilename)
	if err := os.WriteFile(checksumPath, []byte(checksumContent), 0640); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("写入校验和文件失败: %v", err)})
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
				os.Remove(filepath.Join(backupDir, f.Name()))
				os.Remove(filepath.Join(backupDir, f.Name()+".sha256"))
			}
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"data": gin.H{
			"filename": backupFilename,
			"size":     size,
			"sha256":   checksum,
		},
	})
}

// ListBackups 列出已有的数据库备份文件
func (h *SystemHandler) ListBackups(c *gin.Context) {
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
			c.JSON(http.StatusOK, gin.H{"data": []gin.H{}})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("读取备份目录失败: %v", err)})
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

	c.JSON(http.StatusOK, gin.H{"data": backups})
}

// copyFileWithChecksum 复制文件并同时计算 SHA-256 校验和
func copyFileWithChecksum(src, dst string) (checksum string, size int64, err error) {
	srcFile, err := os.Open(src)
	if err != nil {
		return "", 0, fmt.Errorf("打开源文件失败: %w", err)
	}
	defer srcFile.Close()

	dstFile, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0640)
	if err != nil {
		return "", 0, fmt.Errorf("创建目标文件失败: %w", err)
	}
	defer dstFile.Close()

	hasher := sha256.New()
	writer := io.MultiWriter(dstFile, hasher)

	written, err := io.Copy(writer, srcFile)
	if err != nil {
		return "", 0, fmt.Errorf("复制文件失败: %w", err)
	}

	return fmt.Sprintf("%x", hasher.Sum(nil)), written, nil
}
