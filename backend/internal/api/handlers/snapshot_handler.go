package handlers

import (
	"path/filepath"
	"strings"

	"xirang/backend/internal/model"
	"xirang/backend/internal/task/executor"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// dangerousRestorePaths 禁止恢复到的系统目录
var dangerousRestorePaths = []string{
	"/bin", "/sbin", "/usr", "/lib", "/lib64",
	"/boot", "/dev", "/proc", "/sys", "/run",
	"/etc", "/var/run",
}

// validateRestoreTargetPath 校验恢复目标路径安全性
func validateRestoreTargetPath(targetPath string) bool {
	cleaned := filepath.Clean(targetPath)
	if !filepath.IsAbs(cleaned) {
		return false
	}
	if cleaned == "/" {
		return false
	}
	for _, prefix := range dangerousRestorePaths {
		if cleaned == prefix || strings.HasPrefix(cleaned, prefix+"/") {
			return false
		}
	}
	return true
}

// SnapshotHandler 处理 restic 快照浏览和恢复
type SnapshotHandler struct {
	db *gorm.DB
}

func NewSnapshotHandler(db *gorm.DB) *SnapshotHandler {
	return &SnapshotHandler{db: db}
}

// ListSnapshots godoc
// @Summary      列出快照
// @Description  列出 restic 类型任务的所有备份快照
// @Tags         snapshots
// @Security     Bearer
// @Produce      json
// @Param        id  path      int  true  "任务 ID"
// @Success      200  {object}  handlers.Response
// @Failure      400  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Router       /tasks/{id}/snapshots [get]
func (h *SnapshotHandler) ListSnapshots(c *gin.Context) {
	taskID, ok := parseID(c, "id")
	if !ok {
		return
	}
	var task model.Task
	if err := h.db.Preload("Node").Preload("Node.SSHKey").First(&task, taskID).Error; err != nil {
		respondNotFound(c, "任务不存在")
		return
	}
	if task.ExecutorType != "restic" {
		respondBadRequest(c, "仅 restic 类型任务支持快照浏览")
		return
	}

	exec := &executor.ResticExecutor{}
	snapshots, err := exec.ListSnapshots(c.Request.Context(), task)
	if err != nil {
		respondInternalError(c, err)
		return
	}
	respondOK(c, snapshots)
}

// ListFiles godoc
// @Summary      列出快照文件
// @Description  列出指定 restic 快照中的文件和目录
// @Tags         snapshots
// @Security     Bearer
// @Produce      json
// @Param        id    path      int     true   "任务 ID"
// @Param        sid   path      string  true   "快照 ID"
// @Param        path  query     string  false  "目录路径（默认 /）"
// @Success      200  {object}  handlers.Response
// @Failure      400  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Router       /tasks/{id}/snapshots/{sid}/files [get]
func (h *SnapshotHandler) ListFiles(c *gin.Context) {
	taskID, ok := parseID(c, "id")
	if !ok {
		return
	}
	snapshotID := c.Param("sid")
	if !snapshotIDPattern.MatchString(snapshotID) {
		respondBadRequest(c, "快照 ID 格式无效")
		return
	}
	path := filepath.Clean(c.DefaultQuery("path", "/"))
	if !strings.HasPrefix(path, "/") {
		respondBadRequest(c, "path 必须以 / 开头")
		return
	}

	var task model.Task
	if err := h.db.Preload("Node").Preload("Node.SSHKey").First(&task, taskID).Error; err != nil {
		respondNotFound(c, "任务不存在")
		return
	}
	if task.ExecutorType != "restic" {
		respondBadRequest(c, "仅 restic 类型任务支持快照浏览")
		return
	}

	exec := &executor.ResticExecutor{}
	entries, err := exec.ListFiles(c.Request.Context(), task, snapshotID, path)
	if err != nil {
		respondInternalError(c, err)
		return
	}
	respondOK(c, entries)
}

type restoreRequest struct {
	Includes   []string `json:"includes" binding:"required"`
	TargetPath string   `json:"targetPath" binding:"required"`
}

// Restore godoc
// @Summary      恢复快照文件
// @Description  从指定 restic 快照恢复选定的文件到目标路径
// @Tags         snapshots
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        id    path      int     true  "任务 ID"
// @Param        sid   path      string  true  "快照 ID"
// @Param        body  body      object  true  "恢复请求（includes 列表 + targetPath）"
// @Success      200  {object}  handlers.Response
// @Failure      400  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Router       /tasks/{id}/snapshots/{sid}/restore [post]
func (h *SnapshotHandler) Restore(c *gin.Context) {
	taskID, ok := parseID(c, "id")
	if !ok {
		return
	}
	snapshotID := c.Param("sid")
	if !snapshotIDPattern.MatchString(snapshotID) {
		respondBadRequest(c, "快照 ID 格式无效")
		return
	}

	var req restoreRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, "请求参数无效")
		return
	}

	if !validateRestoreTargetPath(req.TargetPath) {
		respondBadRequest(c, "恢复目标路径不安全，不允许恢复到系统目录")
		return
	}

	var task model.Task
	if err := h.db.Preload("Node").Preload("Node.SSHKey").First(&task, taskID).Error; err != nil {
		respondNotFound(c, "任务不存在")
		return
	}
	if task.ExecutorType != "restic" {
		respondBadRequest(c, "仅 restic 类型任务支持快照恢复")
		return
	}

	exec := &executor.ResticExecutor{}
	if err := exec.RestoreFiles(c.Request.Context(), task, snapshotID, req.Includes, req.TargetPath); err != nil {
		respondInternalError(c, err)
		return
	}
	respondMessage(c, "恢复成功")
}
