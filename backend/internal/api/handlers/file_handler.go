package handlers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"xirang/backend/internal/credentialaudit"
	"xirang/backend/internal/fileaccess"
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
	dirListMaxScanned   = 10000
	dirListMaxNameBytes = 16 * 1024 * 1024
)

var errFileRootQuery = errors.New("file browser root query failed")

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

// ListNodeFiles godoc
// @Summary      列出节点文件
// @Description  通过 SFTP 列举远端节点目录内容
// @Tags         files
// @Security     Bearer
// @Produce      json
// @Param        id    path      int     true   "节点 ID"
// @Param        path  query     string  false  "目录路径（默认 /）"
// @Success      200  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Failure      403  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Failure      502  {object}  handlers.Response
// @Router       /nodes/{id}/files [get]
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
		respondNotFound(c, "节点不存在")
		return
	}
	roots, err := loadNodeFileRoots(c.Request.Context(), h.db, node)
	if err != nil {
		respondInternalError(c, err)
		return
	}

	client, sftpClient, credential, err := dialSFTP(c.Request.Context(), node, h.db)
	if err != nil {
		logFileHandlerFailure("error", node.ID, "dial", rawPath, "SFTP 连接失败")
		h.writeFileBrowserAudit(c, node, credential, "file_browser.list", credentialAuditSSHOutcome("dial", err), err, map[string]any{
			"stage":     "dial",
			"path_hash": safePathHash(rawPath),
		})
		respondBadGateway(c, "SFTP 连接失败，请检查节点连接配置")
		return
	}
	defer sftpClient.Close() //nolint:errcheck
	defer client.Close()     //nolint:errcheck

	accessRoot, locator, cleanPath, err := resolveNodeFileAccess(sftpClient, rawPath, roots)
	if err != nil {
		logFileHandlerFailure("warn", node.ID, "path_validate", rawPath, "节点路径校验拒绝")
		h.writeFileBrowserAudit(c, node, credential, "file_browser.list", credentialaudit.OutcomeBlocked, err, map[string]any{
			"stage":     "path_validate",
			"path_hash": safePathHash(rawPath),
		})
		respondForbidden(c, err.Error())
		return
	}

	tree := fileaccess.NewSFTPClientTree(sftpClient, fileaccess.NewSFTPCompatibilityEnumerator(sftpClient), func() error {
		_ = sftpClient.Close()
		return client.Close()
	})
	page, err := tree.List(c.Request.Context(), accessRoot, locator, fileaccess.LegacyPolicy, fileaccess.PageRequest{
		Limit: dirListMaxEntries, MaxItems: dirListMaxScanned, MaxBytes: dirListMaxNameBytes,
	})
	if err != nil {
		logFileHandlerFailure("error", node.ID, "read_dir", cleanPath, "SFTP 读取目录失败")
		h.writeFileBrowserAudit(c, node, credential, "file_browser.list", credentialaudit.OutcomeFailure, err, map[string]any{
			"stage":     "read_dir",
			"path_hash": safePathHash(cleanPath),
		})
		respondBadGateway(c, "读取目录失败")
		return
	}
	entries := fileEntriesFromAccess(accessRoot, page)
	truncated := page.HasMore

	h.writeFileBrowserAudit(c, node, credential, "file_browser.list", credentialaudit.OutcomeSuccess, nil, map[string]any{
		"stage":        "success",
		"kind":         "directory",
		"path_hash":    safePathHash(cleanPath),
		"count":        len(entries),
		"truncated":    truncated,
		"preview_size": 0,
	})

	respondOK(c, FileListResponse{
		Path:      cleanPath,
		Entries:   entries,
		Truncated: truncated,
	})
}

// GetNodeFileContent godoc
// @Summary      获取节点文件内容
// @Description  通过 SFTP 读取远端节点指定文件内容（最大 1MB，超出截断）
// @Tags         files
// @Security     Bearer
// @Produce      json
// @Param        id    path      int     true  "节点 ID"
// @Param        path  query     string  true  "文件路径"
// @Success      200  {object}  handlers.Response
// @Failure      400  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Failure      403  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Failure      502  {object}  handlers.Response
// @Router       /nodes/{id}/files/content [get]
func (h *FileHandler) GetNodeFileContent(c *gin.Context) {
	nodeID, ok := parseID(c, "id")
	if !ok {
		return
	}

	rawPath := strings.TrimSpace(c.Query("path"))
	if rawPath == "" {
		respondBadRequest(c, "请指定文件路径")
		return
	}

	var node model.Node
	if err := h.db.Preload("SSHKey").First(&node, nodeID).Error; err != nil {
		respondNotFound(c, "节点不存在")
		return
	}
	roots, err := loadNodeFileRoots(c.Request.Context(), h.db, node)
	if err != nil {
		respondInternalError(c, err)
		return
	}

	client, sftpClient, credential, err := dialSFTP(c.Request.Context(), node, h.db)
	if err != nil {
		logFileHandlerFailure("error", node.ID, "dial", rawPath, "SFTP 连接失败")
		h.writeFileBrowserAudit(c, node, credential, "file_browser.preview", credentialAuditSSHOutcome("dial", err), err, map[string]any{
			"stage":     "dial",
			"path_hash": safePathHash(rawPath),
		})
		respondBadGateway(c, "SFTP 连接失败，请检查节点连接配置")
		return
	}
	defer sftpClient.Close() //nolint:errcheck
	defer client.Close()     //nolint:errcheck

	accessRoot, locator, cleanPath, err := resolveNodeFileAccess(sftpClient, rawPath, roots)
	if err != nil {
		logFileHandlerFailure("warn", node.ID, "path_validate", rawPath, "节点路径校验拒绝")
		h.writeFileBrowserAudit(c, node, credential, "file_browser.preview", credentialaudit.OutcomeBlocked, err, map[string]any{
			"stage":     "path_validate",
			"path_hash": safePathHash(rawPath),
		})
		respondForbidden(c, err.Error())
		return
	}

	tree := fileaccess.NewSFTPClientTree(sftpClient, fileaccess.NewSFTPCompatibilityEnumerator(sftpClient), func() error {
		_ = sftpClient.Close()
		return client.Close()
	})
	handle, stat, err := tree.OpenRegular(c.Request.Context(), accessRoot, locator, fileaccess.LegacyPolicy)
	if err != nil {
		outcome := credentialaudit.OutcomeFailure
		if errors.Is(err, fileaccess.ErrNotRegular) || errors.Is(err, fileaccess.ErrSymlinkDenied) {
			outcome = credentialaudit.OutcomeBlocked
		}
		h.writeFileBrowserAudit(c, node, credential, "file_browser.preview", outcome, err, map[string]any{"stage": "open", "path_hash": safePathHash(cleanPath)})
		if errors.Is(err, fileaccess.ErrNotRegular) || errors.Is(err, fileaccess.ErrSymlinkDenied) {
			respondBadRequest(c, "目标路径不是可预览的普通文件")
		} else if errors.Is(err, os.ErrNotExist) {
			respondNotFound(c, "文件不存在")
		} else {
			respondBadGateway(c, "打开文件失败")
		}
		return
	}

	buf := make([]byte, filePreviewMaxBytes+1)
	n, err := io.ReadFull(handle, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		_ = handle.Close()
		logFileHandlerFailure("error", node.ID, "read", cleanPath, "SFTP 读取文件失败")
		h.writeFileBrowserAudit(c, node, credential, "file_browser.preview", credentialaudit.OutcomeFailure, err, map[string]any{
			"stage":     "read",
			"path_hash": safePathHash(cleanPath),
		})
		respondBadGateway(c, "读取文件失败")
		return
	}
	if closeErr := handle.Close(); closeErr != nil {
		h.writeFileBrowserAudit(c, node, credential, "file_browser.preview", credentialaudit.OutcomeFailure, closeErr, map[string]any{"stage": "post_check", "path_hash": safePathHash(cleanPath)})
		respondBadGateway(c, "文件在读取期间发生变化")
		return
	}

	truncated := n > filePreviewMaxBytes
	if truncated {
		n = filePreviewMaxBytes
	}

	h.writeFileBrowserAudit(c, node, credential, "file_browser.preview", credentialaudit.OutcomeSuccess, nil, map[string]any{
		"stage":         "success",
		"kind":          "file",
		"path_hash":     safePathHash(cleanPath),
		"size":          stat.Size,
		"preview_bytes": n,
		"truncated":     truncated,
	})

	respondOK(c, FileContentResponse{
		Path:      cleanPath,
		Content:   string(buf[:n]),
		Size:      stat.Size,
		Truncated: truncated,
	})
}

// ListTaskBackupFiles godoc
// @Summary      列出任务备份文件
// @Description  列举任务备份目标目录内容（本地路径，仅 admin）
// @Tags         files
// @Security     Bearer
// @Produce      json
// @Param        id    path      int     true   "任务 ID"
// @Param        path  query     string  false  "子路径（默认 /）"
// @Success      200  {object}  handlers.Response
// @Failure      400  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Failure      403  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Router       /tasks/{id}/backup-files [get]
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
		respondNotFound(c, "任务不存在")
		return
	}

	base := strings.TrimSpace(taskEntity.RsyncTarget)
	if base == "" {
		respondBadRequest(c, "该任务未设置备份目标路径")
		return
	}

	locator, err := localLegacyLocator(rawPath)
	if err != nil {
		logLocalFileHandlerFailure("warn", taskEntity.ID, taskEntity.NodeID, "path_validate", rawPath, "本地路径校验拒绝")
		respondForbidden(c, "路径不在允许的访问范围内")
		return
	}
	accessRoot := fileaccess.Root{Path: filepath.Clean(base)}
	_, fullPath, err := fileaccess.Resolve(accessRoot, locator, fileaccess.LegacyPolicy)
	if err != nil {
		respondForbidden(c, "路径不在允许的访问范围内")
		return
	}
	page, err := fileaccess.NewLocalTree().List(c.Request.Context(), accessRoot, locator, fileaccess.LegacyPolicy, fileaccess.PageRequest{
		Limit: dirListMaxEntries, MaxItems: dirListMaxScanned, MaxBytes: dirListMaxNameBytes,
	})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			respondNotFound(c, "目录不存在")
		} else {
			logLocalFileHandlerFailure("error", taskEntity.ID, taskEntity.NodeID, "read_dir", fullPath, "读取本地目录失败")
			respondInternalError(c, nil)
		}
		return
	}
	entries := fileEntriesFromAccess(accessRoot, page)

	// 响应中的 path 用相对于 RsyncTarget 的视角
	respondOK(c, FileListResponse{
		Path:      fullPath,
		Entries:   entries,
		Truncated: page.HasMore,
	})
}

// --- 内部辅助函数 ---

func logFileHandlerFailure(level string, nodeID uint, stage, path, msg string) {
	log := logger.Module("file_handler")
	if level == "warn" {
		log.Warn().Uint("node_id", nodeID).Str("stage", stage).Str("path_hash", safePathHash(path)).Msg(msg)
		return
	}
	log.Error().Uint("node_id", nodeID).Str("stage", stage).Str("path_hash", safePathHash(path)).Msg(msg)
}

func logLocalFileHandlerFailure(level string, taskID, nodeID uint, stage, path, msg string) {
	log := logger.Module("file_handler")
	if level == "warn" {
		log.Warn().Uint("task_id", taskID).Uint("node_id", nodeID).Str("stage", stage).Str("path_hash", safePathHash(path)).Msg(msg)
		return
	}
	log.Error().Uint("task_id", taskID).Uint("node_id", nodeID).Str("stage", stage).Str("path_hash", safePathHash(path)).Msg(msg)
}

func (h *FileHandler) writeFileBrowserAudit(c *gin.Context, node model.Node, credential sshutil.ResolvedCredential, action, outcome string, err error, metadata map[string]any) {
	fallbackKind, fallbackSource, fallbackKeyID := nodeCredentialFallback(node)
	kind, source, keyID := eventCredentialFields(credential, fallbackKind, fallbackSource)
	if keyID == nil {
		keyID = fallbackKeyID
	}
	event := credentialaudit.Event{
		Action:           action,
		Purpose:          sshutil.PurposeFileBrowser,
		CredentialKind:   kind,
		CredentialSource: source,
		SSHKeyID:         keyID,
		NodeID:           credentialaudit.PtrUint(node.ID),
		Outcome:          outcome,
		Metadata:         metadata,
	}
	if err != nil {
		event.ErrorMessage = credentialAuditSafeError(fmt.Sprint(metadata["stage"]), err)
	}
	writeCredentialAuditFromGin(c, h.db, event)
}

// dialSFTP 建立 SSH+SFTP 会话。
func dialSFTP(ctx context.Context, node model.Node, db *gorm.DB) (interface{ Close() error }, *sftp.Client, sshutil.ResolvedCredential, error) {
	auth, credential, err := sshutil.BuildSSHAuthForPurpose(node, db, sshutil.PurposeFileBrowser)
	if err != nil {
		return nil, nil, credential, err
	}
	hostKey, err := sshutil.ResolveSSHHostKeyCallback()
	if err != nil {
		return nil, nil, credential, err
	}

	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	addr := fmt.Sprintf("%s:%d", node.Host, node.Port)
	sshClient, err := sshutil.DialSSH(dialCtx, addr, node.Username, auth, hostKey)
	if err != nil {
		return nil, nil, credential, err
	}

	sftpClient, err := sftp.NewClient(sshClient)
	if err != nil {
		_ = sshClient.Close()
		return nil, nil, credential, fmt.Errorf("SFTP 子系统初始化失败: %w", err)
	}
	return sshClient, sftpClient, credential, nil
}

// realPathResolver 抽象出"把任意路径解析为节点端真实绝对路径"的能力。
// *sftp.Client 直接满足；测试时可注入伪实现以模拟符号链接解析。
type realPathResolver interface {
	RealPath(p string) (string, error)
}

// validateNodePath 校验请求路径是否落在节点的允许根目录（Node.BasePath 及该节点任务的 RsyncSource）内。
//
// 实现要点（B-2 加固）：
//  1. 通过 sftp.RealPath 让节点端解析输入路径与每个白名单根，获得解析符号链接后的绝对路径；
//  2. 在解析后的字符串上做严格前缀比对，封堵 "ln -s /etc /backup/sneaky" 类逃逸；
//  3. 返回值为节点端真实路径，可直接用于后续 SFTP 调用，避免再次解析。
//
// 性能影响：每次请求新增 (1 + len(roots)) 次 SFTP RTT；典型 LAN 节点 ~10ms 量级，可接受。
// 开发旁路：FILE_BROWSER_ALLOW_ALL=true 且处于开发环境时跳过解析，仅做语法 Clean。
func validateNodePath(ctx context.Context, resolver realPathResolver, rawPath string, node model.Node, db *gorm.DB) (string, error) {
	if util.GetEnvOrDefault("FILE_BROWSER_ALLOW_ALL", "") == "true" {
		if !util.IsDevelopmentEnv() {
			return "", fmt.Errorf("FILE_BROWSER_ALLOW_ALL 仅允许在开发环境中使用")
		}
		return filepath.Clean(rawPath), nil
	}

	if resolver == nil {
		return "", fmt.Errorf("路径解析失败：缺少 SFTP 会话")
	}
	roots, err := loadNodeFileRoots(ctx, db, node)
	if err != nil {
		return "", err
	}
	_, _, resolved, err := resolveNodeFileAccess(resolver, rawPath, roots)
	return resolved, err
}

func loadNodeFileRoots(ctx context.Context, db *gorm.DB, node model.Node) ([]fileaccess.Root, error) {
	if db == nil {
		return nil, fmt.Errorf("%w: database unavailable", errFileRootQuery)
	}
	values := make([]string, 0, 4)
	if base := strings.TrimSpace(node.BasePath); base != "" {
		values = append(values, base)
	}
	var tasks []model.Task
	if err := db.WithContext(ctx).Select("rsync_source").Where("node_id = ?", node.ID).Find(&tasks).Error; err != nil {
		return nil, fmt.Errorf("%w", errFileRootQuery)
	}
	for _, taskEntity := range tasks {
		if source := strings.TrimSpace(taskEntity.RsyncSource); source != "" {
			values = append(values, source)
		}
	}
	seen := make(map[string]struct{}, len(values))
	roots := make([]fileaccess.Root, 0, len(values))
	for _, value := range values {
		clean := filepath.Clean(value)
		if _, exists := seen[clean]; exists {
			continue
		}
		seen[clean] = struct{}{}
		roots = append(roots, fileaccess.Root{Path: clean})
	}
	return roots, nil
}

func resolveNodeFileAccess(resolver realPathResolver, rawPath string, roots []fileaccess.Root) (fileaccess.Root, fileaccess.Locator, string, error) {
	if util.GetEnvOrDefault("FILE_BROWSER_ALLOW_ALL", "") == "true" {
		if !util.IsDevelopmentEnv() {
			return fileaccess.Root{}, fileaccess.Locator{}, "", fmt.Errorf("FILE_BROWSER_ALLOW_ALL 仅允许在开发环境中使用")
		}
		clean := filepath.Clean(rawPath)
		return fileaccess.Root{Path: string(filepath.Separator)}, fileaccess.Locator{Path: clean}, clean, nil
	}
	if resolver == nil {
		return fileaccess.Root{}, fileaccess.Locator{}, "", fmt.Errorf("路径解析失败：缺少 SFTP 会话")
	}
	resolved, err := resolver.RealPath(filepath.Clean(rawPath))
	if err != nil {
		return fileaccess.Root{}, fileaccess.Locator{}, "", fmt.Errorf("路径不存在或不可访问")
	}
	resolved = filepath.Clean(resolved)
	for _, root := range roots {
		resolvedRoot := string(filepath.Separator)
		if filepath.Clean(root.Path) != string(filepath.Separator) {
			resolvedRoot, err = resolver.RealPath(filepath.Clean(root.Path))
			if err != nil {
				continue
			}
			resolvedRoot = filepath.Clean(resolvedRoot)
		}
		if fileaccess.Contains(resolvedRoot, resolved) {
			return fileaccess.Root{Path: resolvedRoot}, fileaccess.Locator{Path: resolved}, resolved, nil
		}
	}
	return fileaccess.Root{}, fileaccess.Locator{}, "", fmt.Errorf("路径超出允许范围，请在节点 BasePath 或任务源路径下浏览")
}

func localLegacyLocator(rawPath string) (fileaccess.Locator, error) {
	clean := filepath.Clean(rawPath)
	if clean == string(filepath.Separator) || clean == "." {
		return fileaccess.RootLocator(), nil
	}
	if filepath.IsAbs(clean) {
		clean = strings.TrimPrefix(clean, filepath.VolumeName(clean))
		clean = strings.TrimLeft(clean, "/\\")
	}
	return fileaccess.ParseLocator(clean, fileaccess.LegacyPolicy)
}

func fileEntriesFromAccess(root fileaccess.Root, page fileaccess.EntryPage) []FileEntry {
	entries := make([]FileEntry, 0, len(page.Items))
	for _, entry := range page.Items {
		entryPath := entry.Locator.Path
		if !filepath.IsAbs(entryPath) {
			entryPath = filepath.Join(root.Path, entryPath)
		}
		entries = append(entries, FileEntry{
			Name: entry.Name, Path: entryPath, IsDir: entry.Type == fileaccess.EntryDirectory,
			Size: entry.Size, Mode: entry.Mode, ModTime: entry.ModTime.Format(time.RFC3339),
		})
	}
	return entries
}
