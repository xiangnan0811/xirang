package handlers

import (
	"net/http"
	"path/filepath"
	"strings"

	"xirang/backend/internal/logger"
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

// ListSnapshots 列出 restic 任务的所有快照
func (h *SnapshotHandler) ListSnapshots(c *gin.Context) {
	taskID, ok := parseID(c, "id")
	if !ok {
		return
	}
	var task model.Task
	if err := h.db.Preload("Node").Preload("Node.SSHKey").First(&task, taskID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "任务不存在"})
		return
	}
	if task.ExecutorType != "restic" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "仅 restic 类型任务支持快照浏览"})
		return
	}

	exec := &executor.ResticExecutor{}
	snapshots, err := exec.ListSnapshots(c.Request.Context(), task)
	if err != nil {
		logger.Log.Error().Err(err).Msg("列出快照失败")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "列出快照失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": snapshots})
}

// ListFiles 列出指定快照中的文件
func (h *SnapshotHandler) ListFiles(c *gin.Context) {
	taskID, ok := parseID(c, "id")
	if !ok {
		return
	}
	snapshotID := c.Param("sid")
	if !snapshotIDPattern.MatchString(snapshotID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "快照 ID 格式无效"})
		return
	}
	path := filepath.Clean(c.DefaultQuery("path", "/"))
	if !strings.HasPrefix(path, "/") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "path 必须以 / 开头"})
		return
	}

	var task model.Task
	if err := h.db.Preload("Node").Preload("Node.SSHKey").First(&task, taskID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "任务不存在"})
		return
	}
	if task.ExecutorType != "restic" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "仅 restic 类型任务支持快照浏览"})
		return
	}

	exec := &executor.ResticExecutor{}
	entries, err := exec.ListFiles(c.Request.Context(), task, snapshotID, path)
	if err != nil {
		logger.Log.Error().Err(err).Msg("列出快照文件失败")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "列出快照文件失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": entries})
}

type restoreRequest struct {
	Includes   []string `json:"includes" binding:"required"`
	TargetPath string   `json:"targetPath" binding:"required"`
}

// Restore 从快照恢复指定文件
func (h *SnapshotHandler) Restore(c *gin.Context) {
	taskID, ok := parseID(c, "id")
	if !ok {
		return
	}
	snapshotID := c.Param("sid")
	if !snapshotIDPattern.MatchString(snapshotID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "快照 ID 格式无效"})
		return
	}

	var req restoreRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数无效"})
		return
	}

	if !validateRestoreTargetPath(req.TargetPath) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "恢复目标路径不安全，不允许恢复到系统目录"})
		return
	}

	var task model.Task
	if err := h.db.Preload("Node").Preload("Node.SSHKey").First(&task, taskID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "任务不存在"})
		return
	}
	if task.ExecutorType != "restic" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "仅 restic 类型任务支持快照恢复"})
		return
	}

	exec := &executor.ResticExecutor{}
	if err := exec.RestoreFiles(c.Request.Context(), task, snapshotID, req.Includes, req.TargetPath); err != nil {
		logger.Log.Error().Err(err).Msg("快照恢复失败")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "快照恢复失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"message": "恢复成功"}})
}
