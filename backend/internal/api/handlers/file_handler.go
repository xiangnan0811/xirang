package handlers

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"
	"xirang/backend/internal/util"

	"github.com/gin-gonic/gin"
	"github.com/pkg/sftp"
	"gorm.io/gorm"
)

const (
	filePreviewMaxBytes = 1 * 1024 * 1024 // 1MB
	dirListMaxEntries   = 500
)

// FileEntry 表示一个文件或目录条目。
type FileEntry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	IsDir   bool   `json:"is_dir"`
	Size    int64  `json:"size"`
	Mode    string `json:"mode"`
	ModTime string `json:"mod_time"`
}

// FileListResponse 文件列表响应。
type FileListResponse struct {
	Path      string      `json:"path"`
	Entries   []FileEntry `json:"entries"`
	Truncated bool        `json:"truncated"`
}

// FileContentResponse 文件内容响应。
type FileContentResponse struct {
	Path      string `json:"path"`
	Content   string `json:"content"`
	Size      int64  `json:"size"`
	Truncated bool   `json:"truncated"`
}

// FileHandler 处理文件浏览请求。
type FileHandler struct {
	db *gorm.DB
}

func NewFileHandler(db *gorm.DB) *FileHandler {
	return &FileHandler{db: db}
}

// ListNodeFiles 通过 SFTP 列举远端节点目录（选项 A）。
// GET /nodes/:id/files?path=/var/backup
func (h *FileHandler) ListNodeFiles(c *gin.Context) {
	nodeID, ok := parseID(c, "id")
	if !ok {
		return
	}

	rawPath := strings.TrimSpace(c.Query("path"))
	if rawPath == "" {
		rawPath = "/"
	}

	var node model.Node
	if err := h.db.Preload("SSHKey").First(&node, nodeID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "节点不存在"})
		return
	}

	// 路径安全校验
	cleanPath, err := validateNodePath(rawPath, node, h.db)
	if err != nil {
		logger.Log.Warn().Err(err).Msg("节点路径校验拒绝")
		c.JSON(http.StatusForbidden, gin.H{"error": "路径不在允许的访问范围内"})
		return
	}

	client, sftpClient, err := dialSFTP(c.Request.Context(), node, h.db)
	if err != nil {
		logger.Log.Error().Err(err).Msg("SFTP 连接失败")
		c.JSON(http.StatusBadGateway, gin.H{"error": "SFTP 连接失败，请检查节点连接配置"})
		return
	}
	defer sftpClient.Close() //nolint:errcheck
	defer client.Close()    //nolint:errcheck

	entries, truncated, err := listSFTPDir(sftpClient, cleanPath)
	if err != nil {
		logger.Log.Error().Err(err).Msg("SFTP 读取目录失败")
		c.JSON(http.StatusBadGateway, gin.H{"error": "读取目录失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": FileListResponse{
		Path:      cleanPath,
		Entries:   entries,
		Truncated: truncated,
	}})
}

// GetNodeFileContent 通过 SFTP 读取远端节点文件内容（选项 A）。
// GET /nodes/:id/files/content?path=/var/backup/log.txt
func (h *FileHandler) GetNodeFileContent(c *gin.Context) {
	nodeID, ok := parseID(c, "id")
	if !ok {
		return
	}

	rawPath := strings.TrimSpace(c.Query("path"))
	if rawPath == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请指定文件路径"})
		return
	}

	var node model.Node
	if err := h.db.Preload("SSHKey").First(&node, nodeID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "节点不存在"})
		return
	}

	cleanPath, err := validateNodePath(rawPath, node, h.db)
	if err != nil {
		logger.Log.Warn().Err(err).Msg("节点路径校验拒绝")
		c.JSON(http.StatusForbidden, gin.H{"error": "路径不在允许的访问范围内"})
		return
	}

	client, sftpClient, err := dialSFTP(c.Request.Context(), node, h.db)
	if err != nil {
		logger.Log.Error().Err(err).Msg("SFTP 连接失败")
		c.JSON(http.StatusBadGateway, gin.H{"error": "SFTP 连接失败，请检查节点连接配置"})
		return
	}
	defer sftpClient.Close() //nolint:errcheck
	defer client.Close()    //nolint:errcheck

	stat, err := sftpClient.Stat(cleanPath)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "文件不存在"})
		return
	}
	if stat.IsDir() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "目标路径是目录，无法预览"})
		return
	}

	f, err := sftpClient.Open(cleanPath)
	if err != nil {
		logger.Log.Error().Err(err).Msg("SFTP 打开文件失败")
		c.JSON(http.StatusBadGateway, gin.H{"error": "打开文件失败"})
		return
	}
	defer f.Close() //nolint:errcheck

	buf := make([]byte, filePreviewMaxBytes+1)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		logger.Log.Error().Err(err).Msg("SFTP 读取文件失败")
		c.JSON(http.StatusBadGateway, gin.H{"error": "读取文件失败"})
		return
	}

	truncated := n > filePreviewMaxBytes
	if truncated {
		n = filePreviewMaxBytes
	}

	c.JSON(http.StatusOK, gin.H{"data": FileContentResponse{
		Path:      cleanPath,
		Content:   string(buf[:n]),
		Size:      stat.Size(),
		Truncated: truncated,
	}})
}

// ListTaskBackupFiles 列举任务备份目标目录内容（选项 B，仅 admin）。
// GET /tasks/:id/backup-files?path=/
func (h *FileHandler) ListTaskBackupFiles(c *gin.Context) {
	taskID, ok := parseID(c, "id")
	if !ok {
		return
	}

	rawPath := strings.TrimSpace(c.Query("path"))
	if rawPath == "" {
		rawPath = "/"
	}

	var taskEntity model.Task
	if err := h.db.First(&taskEntity, taskID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "任务不存在"})
		return
	}

	base := strings.TrimSpace(taskEntity.RsyncTarget)
	if base == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "该任务未设置备份目标路径"})
		return
	}

	// 将请求路径拼接到 RsyncTarget 并做安全校验
	fullPath, err := validateLocalPath(rawPath, base)
	if err != nil {
		logger.Log.Warn().Err(err).Msg("本地路径校验拒绝")
		c.JSON(http.StatusForbidden, gin.H{"error": "路径不在允许的访问范围内"})
		return
	}

	entries, truncated, err := listLocalDir(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "目录不存在"})
		} else {
			logger.Log.Error().Err(err).Msg("读取本地目录失败")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "读取目录失败"})
		}
		return
	}

	// 响应中的 path 用相对于 RsyncTarget 的视角
	c.JSON(http.StatusOK, gin.H{"data": FileListResponse{
		Path:      fullPath,
		Entries:   entries,
		Truncated: truncated,
	}})
}

// --- 内部辅助函数 ---

// dialSFTP 建立 SSH+SFTP 会话。
func dialSFTP(ctx context.Context, node model.Node, db *gorm.DB) (interface{ Close() error }, *sftp.Client, error) {
	auth, err := sshutil.BuildSSHAuth(node, db)
	if err != nil {
		return nil, nil, err
	}
	hostKey, err := sshutil.ResolveSSHHostKeyCallback()
	if err != nil {
		return nil, nil, err
	}

	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	addr := fmt.Sprintf("%s:%d", node.Host, node.Port)
	sshClient, err := sshutil.DialSSH(dialCtx, addr, node.Username, auth, hostKey)
	if err != nil {
		return nil, nil, err
	}

	sftpClient, err := sftp.NewClient(sshClient)
	if err != nil {
		_ = sshClient.Close()
		return nil, nil, fmt.Errorf("SFTP 子系统初始化失败: %w", err)
	}
	return sshClient, sftpClient, nil
}

// validateNodePath 对选项 A 的路径做安全校验，允许 Node.BasePath 及该节点任务的 RsyncSource 作为白名单根。
func validateNodePath(rawPath string, node model.Node, db *gorm.DB) (string, error) {
	if util.GetEnvOrDefault("FILE_BROWSER_ALLOW_ALL", "") == "true" {
		if !util.IsDevelopmentEnv() {
			return "", fmt.Errorf("FILE_BROWSER_ALLOW_ALL 仅允许在开发环境中使用")
		}
		return filepath.Clean(rawPath), nil
	}

	clean := filepath.Clean(rawPath)

	// 收集白名单根路径
	roots := []string{}
	if base := strings.TrimSpace(node.BasePath); base != "" {
		roots = append(roots, filepath.Clean(base))
	}

	// 该节点所有任务的 RsyncSource
	var tasks []model.Task
	if err := db.Select("rsync_source").Where("node_id = ?", node.ID).Find(&tasks).Error; err == nil {
		for _, t := range tasks {
			if s := strings.TrimSpace(t.RsyncSource); s != "" {
				roots = append(roots, filepath.Clean(s))
			}
		}
	}

	for _, root := range roots {
		if root == "/" || strings.HasPrefix(clean, root+"/") || clean == root {
			return clean, nil
		}
	}

	return "", fmt.Errorf("路径超出允许范围，请在节点 BasePath 或任务源路径下浏览")
}

// validateLocalPath 对选项 B 的路径做安全校验，确保在 base（RsyncTarget）目录下。
func validateLocalPath(rawPath, base string) (string, error) {
	cleanBase := filepath.Clean(base)

	// rawPath 视为相对路径，拼接到 base 下
	joined := filepath.Join(cleanBase, rawPath)
	cleanJoined := filepath.Clean(joined)

	// 必须在 base 目录内
	if cleanJoined != cleanBase && !strings.HasPrefix(cleanJoined, cleanBase+string(filepath.Separator)) {
		return "", fmt.Errorf("路径超出备份目录范围")
	}

	// 解析符号链接后再次校验
	resolved, err := filepath.EvalSymlinks(cleanJoined)
	if err != nil {
		if os.IsNotExist(err) {
			return cleanJoined, nil // 路径不存在时跳过符号链接解析，留给调用者处理
		}
		return "", fmt.Errorf("路径解析失败")
	}
	resolvedBase, err := filepath.EvalSymlinks(cleanBase)
	if err != nil {
		resolvedBase = cleanBase
	}
	if resolved != resolvedBase && !strings.HasPrefix(resolved, resolvedBase+string(filepath.Separator)) {
		return "", fmt.Errorf("路径超出备份目录范围（符号链接穿越）")
	}

	return cleanJoined, nil
}

// listSFTPDir 通过 SFTP 列举目录内容。
func listSFTPDir(client *sftp.Client, path string) ([]FileEntry, bool, error) {
	infos, err := client.ReadDir(path)
	if err != nil {
		return nil, false, err
	}

	truncated := len(infos) > dirListMaxEntries
	if truncated {
		infos = infos[:dirListMaxEntries]
	}

	entries := make([]FileEntry, 0, len(infos))
	for _, info := range infos {
		entryPath := filepath.Join(path, info.Name())
		entries = append(entries, FileEntry{
			Name:    info.Name(),
			Path:    entryPath,
			IsDir:   info.IsDir(),
			Size:    info.Size(),
			Mode:    info.Mode().String(),
			ModTime: info.ModTime().Format(time.RFC3339),
		})
	}
	return entries, truncated, nil
}

// listLocalDir 列举本地目录内容。
func listLocalDir(path string) ([]FileEntry, bool, error) {
	des, err := os.ReadDir(path)
	if err != nil {
		return nil, false, err
	}

	truncated := len(des) > dirListMaxEntries
	if truncated {
		des = des[:dirListMaxEntries]
	}

	entries := make([]FileEntry, 0, len(des))
	for _, de := range des {
		info, err := de.Info()
		if err != nil {
			continue
		}
		entryPath := filepath.Join(path, de.Name())
		entries = append(entries, FileEntry{
			Name:    de.Name(),
			Path:    entryPath,
			IsDir:   de.IsDir(),
			Size:    info.Size(),
			Mode:    info.Mode().String(),
			ModTime: info.ModTime().Format(time.RFC3339),
		})
	}
	return entries, truncated, nil
}
