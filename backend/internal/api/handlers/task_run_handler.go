package handlers

import (
	"errors"
	"strconv"

	"xirang/backend/internal/middleware"
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

func canReadOrphanedTaskRun(role string) bool {
	return role == "" || role == "admin"
}

// ListByTask godoc
// @Summary      列出任务执行历史
// @Description  返回某任务的执行历史，按 created_at DESC 分页
// @Tags         task-runs
// @Security     Bearer
// @Produce      json
// @Param        id         path      int     true   "任务 ID"
// @Param        page       query     int     false  "页码（默认 1）"
// @Param        page_size  query     int     false  "每页条数（默认 20，最大 100）"
// @Param        status     query     string  false  "状态过滤"
// @Success      200  {object}  handlers.PaginatedResponse{data=[]model.TaskRun}
// @Failure      401  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Router       /tasks/{id}/runs [get]
func (h *TaskRunHandler) ListByTask(c *gin.Context) {
	taskID, ok := parseID(c, "id")
	if !ok {
		return
	}

	pg := parsePagination(c, 20, "created_at", map[string]bool{
		"created_at": true, "status": true, "id": true,
	})
	if pg.PageSize > 100 {
		pg.PageSize = 100
	}

	query := h.db.Model(&model.TaskRun{}).Where("task_id = ?", taskID)
	if status := c.Query("status"); status != "" {
		query = query.Where("status = ?", status)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		respondInternalError(c, err)
		return
	}

	var runs []model.TaskRun
	if err := applyPagination(query, pg).Find(&runs).Error; err != nil {
		respondInternalError(c, err)
		return
	}

	respondPaginated(c, runs, total, pg.Page, pg.PageSize)
}

// Get godoc
// @Summary      获取执行记录详情
// @Description  返回单次任务执行详情，含关联的 Task 基本信息
// @Tags         task-runs
// @Security     Bearer
// @Produce      json
// @Param        id   path      int  true  "执行记录 ID"
// @Success      200  {object}  handlers.Response{data=model.TaskRun}
// @Failure      401  {object}  handlers.Response
// @Failure      403  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Router       /task-runs/{id} [get]
func (h *TaskRunHandler) Get(c *gin.Context) {
	runID, ok := parseID(c, "id")
	if !ok {
		return
	}
	role := middleware.CurrentRole(c)

	var run model.TaskRun
	if err := h.db.Preload("Task", func(db *gorm.DB) *gorm.DB {
		return db.Select("id", "name", "node_id", "rsync_source", "rsync_target", "executor_type")
	}).First(&run, runID).Error; err != nil {
		respondNotFound(c, "执行记录不存在")
		return
	}
	if run.Task.ID == 0 {
		if !canReadOrphanedTaskRun(role) {
			respondForbidden(c, "无权访问该执行记录")
			return
		}
	} else {
		allowed, err := authorizeNodeOwnership(c, h.db, run.Task.NodeID)
		if err != nil {
			respondInternalError(c, err)
			return
		}
		if !allowed {
			respondForbidden(c, "无权访问该执行记录")
			return
		}
	}

	respondOK(c, run)
}

// Logs godoc
// @Summary      获取执行记录日志
// @Description  返回单次任务执行的日志列表
// @Tags         task-runs
// @Security     Bearer
// @Produce      json
// @Param        id        path      int     true   "执行记录 ID"
// @Param        limit     query     int     false  "返回条数（默认 200，最大 1000）"
// @Param        before_id query     int     false  "游标：返回此 ID 之前的日志"
// @Param        level     query     string  false  "日志级别过滤"
// @Success      200  {object}  handlers.Response{data=[]model.TaskLog}
// @Failure      401  {object}  handlers.Response
// @Failure      403  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Router       /task-runs/{id}/logs [get]
func (h *TaskRunHandler) Logs(c *gin.Context) {
	runID, ok := parseID(c, "id")
	if !ok {
		return
	}
	role := middleware.CurrentRole(c)
	// ownership 校验：通过 task_run → task → node 链查
	var taskRun model.TaskRun
	if err := h.db.Select("id", "task_id").First(&taskRun, runID).Error; err != nil {
		respondNotFound(c, "执行记录不存在")
		return
	}
	var taskEntity model.Task
	if err := h.db.Select("id", "node_id").First(&taskEntity, taskRun.TaskID).Error; err == nil {
		allowed, authErr := authorizeNodeOwnership(c, h.db, taskEntity.NodeID)
		if authErr != nil {
			respondInternalError(c, authErr)
			return
		}
		if !allowed {
			respondForbidden(c, "无权访问该执行记录")
			return
		}
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		respondInternalError(c, err)
		return
	} else if !canReadOrphanedTaskRun(role) {
		respondForbidden(c, "无权访问该执行记录")
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
		respondInternalError(c, err)
		return
	}

	respondOK(c, logs)
}
