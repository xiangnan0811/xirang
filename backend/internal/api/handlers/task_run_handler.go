package handlers

import (
	"net/http"
	"strconv"

	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type TaskRunHandler struct {
	db *gorm.DB
}

func NewTaskRunHandler(db *gorm.DB) *TaskRunHandler {
	return &TaskRunHandler{db: db}
}

// ListByTask 返回某任务的执行历史，按 created_at DESC 分页
// GET /tasks/:id/runs?limit=20&offset=0&status=success
func (h *TaskRunHandler) ListByTask(c *gin.Context) {
	taskID, ok := parseID(c, "id")
	if !ok {
		return
	}

	limit := 20
	if raw := c.Query("limit"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			limit = v
		}
	}
	if limit > 100 {
		limit = 100
	}

	offset := 0
	if raw := c.Query("offset"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v >= 0 {
			offset = v
		}
	}

	query := h.db.Model(&model.TaskRun{}).Where("task_id = ?", taskID)
	if status := c.Query("status"); status != "" {
		query = query.Where("status = ?", status)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询执行记录总数失败"})
		return
	}

	var runs []model.TaskRun
	if err := query.Order("created_at desc").Offset(offset).Limit(limit).Find(&runs).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询执行记录失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"items": runs,
		"total": total,
	})
}

// Get 返回单次执行详情，含关联的 Task 基本信息
// GET /task-runs/:id
func (h *TaskRunHandler) Get(c *gin.Context) {
	runID, ok := parseID(c, "id")
	if !ok {
		return
	}

	var run model.TaskRun
	if err := h.db.Preload("Task", func(db *gorm.DB) *gorm.DB {
		return db.Select("id", "name", "node_id", "rsync_source", "rsync_target", "executor_type")
	}).First(&run, runID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "执行记录不存在"})
		return
	}

	c.JSON(http.StatusOK, run)
}

// Logs 返回单次执行的日志
// GET /task-runs/:id/logs?limit=200&before_id=0&level=error
func (h *TaskRunHandler) Logs(c *gin.Context) {
	runID, ok := parseID(c, "id")
	if !ok {
		return
	}

	limit := 200
	if raw := c.Query("limit"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			limit = v
		}
	}
	if limit > 1000 {
		limit = 1000
	}

	query := h.db.Model(&model.TaskLog{}).Where("task_run_id = ?", runID)

	if level := c.Query("level"); level != "" {
		query = query.Where("level = ?", level)
	}
	if raw := c.Query("before_id"); raw != "" {
		if beforeID, err := strconv.ParseUint(raw, 10, 64); err == nil && beforeID > 0 {
			query = query.Where("id < ?", beforeID)
		}
	}

	var logs []model.TaskLog
	if err := query.Order("id desc").Limit(limit).Find(&logs).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询执行日志失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": logs})
}
