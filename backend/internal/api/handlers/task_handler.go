package handlers

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"xirang/backend/internal/model"
	"xirang/backend/internal/task"
	"xirang/backend/internal/util"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type TaskRunner interface {
	TriggerManual(taskID uint) error
	SyncSchedule(task model.Task) error
	RemoveSchedule(taskID uint)
	Cancel(taskID uint) error
}

type TaskHandler struct {
	db     *gorm.DB
	runner TaskRunner
}

func NewTaskHandler(db *gorm.DB, runner TaskRunner) *TaskHandler {
	return &TaskHandler{db: db, runner: runner}
}

type taskRequest struct {
	Name         string `json:"name" binding:"required"`
	NodeID       uint   `json:"node_id" binding:"required"`
	PolicyID     *uint  `json:"policy_id"`
	Command      string `json:"command"`
	RsyncSource  string `json:"rsync_source"`
	RsyncTarget  string `json:"rsync_target"`
	ExecutorType string `json:"executor_type"`
	CronSpec     string `json:"cron_spec"`
}

func (h *TaskHandler) List(c *gin.Context) {
	query := h.db.Model(&model.Task{})

	status := strings.TrimSpace(c.Query("status"))
	if status != "" {
		query = query.Where("status = ?", status)
	}
	if rawNodeID := strings.TrimSpace(c.Query("node_id")); rawNodeID != "" {
		nodeID, err := strconv.ParseUint(rawNodeID, 10, 64)
		if err == nil {
			query = query.Where("node_id = ?", uint(nodeID))
		}
	}
	if rawPolicyID := strings.TrimSpace(c.Query("policy_id")); rawPolicyID != "" {
		policyID, err := strconv.ParseUint(rawPolicyID, 10, 64)
		if err == nil {
			query = query.Where("policy_id = ?", uint(policyID))
		}
	}
	if keyword := strings.TrimSpace(c.Query("keyword")); keyword != "" {
		fuzzyKeyword := "%" + keyword + "%"
		query = query.Where("name LIKE ? OR command LIKE ? OR rsync_source LIKE ? OR rsync_target LIKE ?", fuzzyKeyword, fuzzyKeyword, fuzzyKeyword, fuzzyKeyword)
	}

	if rawLimit := strings.TrimSpace(c.Query("limit")); rawLimit != "" {
		if parsed, err := strconv.Atoi(rawLimit); err == nil && parsed > 0 && parsed <= 500 {
			query = query.Limit(parsed)
		}
	}
	if rawOffset := strings.TrimSpace(c.Query("offset")); rawOffset != "" {
		if parsed, err := strconv.Atoi(rawOffset); err == nil && parsed >= 0 {
			query = query.Offset(parsed)
		}
	}

	var tasks []model.Task
	if err := query.Preload("Node").Preload("Policy").Order(parseTaskSort(c.Query("sort"))).Find(&tasks).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": tasks})
}

func (h *TaskHandler) Get(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var taskEntity model.Task
	if err := h.db.Preload("Node").Preload("Policy").First(&taskEntity, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "任务不存在"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": taskEntity})
}

func (h *TaskHandler) Create(c *gin.Context) {
	var req taskRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数不合法"})
		return
	}

	hydrateTaskDefaultsFromPolicy(h.db, &req)
	inferTaskExecutor(&req, "")
	trimTaskRequest(&req)

	if err := validateTaskRequest(req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	req.Command = ""

	taskEntity := model.Task{
		Name:         req.Name,
		NodeID:       req.NodeID,
		PolicyID:     req.PolicyID,
		Command:      req.Command,
		RsyncSource:  req.RsyncSource,
		RsyncTarget:  req.RsyncTarget,
		ExecutorType: req.ExecutorType,
		CronSpec:     req.CronSpec,
		Status:       string(task.StatusPending),
	}
	if err := h.db.Create(&taskEntity).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if h.runner != nil {
		if err := h.runner.SyncSchedule(taskEntity); err != nil {
			h.runner.RemoveSchedule(taskEntity.ID)
			if rollbackErr := h.db.Delete(&model.Task{}, taskEntity.ID).Error; rollbackErr != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("任务调度同步失败且补偿删除失败: %v", rollbackErr)})
				return
			}
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}
	c.JSON(http.StatusCreated, gin.H{"data": taskEntity})
}

func (h *TaskHandler) Update(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var req taskRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数不合法"})
		return
	}

	var taskEntity model.Task
	if err := h.db.First(&taskEntity, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "任务不存在"})
		return
	}
	// 值拷贝用于补偿回滚；安全前提：后续仅替换指针字段（如 PolicyID），
	// 不可通过 *previous.PolicyID = xxx 修改指向值，否则回滚数据会被污染。
	previous := taskEntity

	hydrateTaskDefaultsFromPolicy(h.db, &req)
	inferTaskExecutor(&req, taskEntity.ExecutorType)
	trimTaskRequest(&req)

	if req.Name == "" {
		req.Name = taskEntity.Name
	}
	if req.NodeID == 0 {
		req.NodeID = taskEntity.NodeID
	}
	if req.PolicyID == nil {
		req.PolicyID = taskEntity.PolicyID
	}
	if req.RsyncSource == "" {
		req.RsyncSource = taskEntity.RsyncSource
	}
	if req.RsyncTarget == "" {
		req.RsyncTarget = taskEntity.RsyncTarget
	}
	if req.CronSpec == "" {
		req.CronSpec = taskEntity.CronSpec
	}
	if req.ExecutorType == "" {
		req.ExecutorType = taskEntity.ExecutorType
	}

	if err := validateTaskRequest(req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	req.Command = ""

	taskEntity.Name = req.Name
	taskEntity.NodeID = req.NodeID
	taskEntity.PolicyID = req.PolicyID
	taskEntity.Command = req.Command
	taskEntity.RsyncSource = req.RsyncSource
	taskEntity.RsyncTarget = req.RsyncTarget
	taskEntity.ExecutorType = req.ExecutorType
	taskEntity.CronSpec = req.CronSpec

	if err := h.db.Save(&taskEntity).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if h.runner != nil {
		if err := h.runner.SyncSchedule(taskEntity); err != nil {
			h.runner.RemoveSchedule(taskEntity.ID)
			if restoreErr := h.db.Save(&previous).Error; restoreErr != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("任务调度同步失败且补偿回滚失败: %v", restoreErr)})
				return
			}
			if restoreScheduleErr := h.runner.SyncSchedule(previous); restoreScheduleErr != nil {
				h.runner.RemoveSchedule(taskEntity.ID)
				c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("任务调度同步失败且补偿调度失败: %v", restoreScheduleErr)})
				return
			}
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"data": taskEntity})
}

func (h *TaskHandler) Delete(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	if err := h.db.Delete(&model.Task{}, id).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	if h.runner != nil {
		h.runner.RemoveSchedule(id)
	}
	c.JSON(http.StatusOK, gin.H{"message": "deleted"})
}

func (h *TaskHandler) Trigger(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	if err := h.runner.TriggerManual(id); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"message": "triggered"})
}

func (h *TaskHandler) Cancel(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	if err := h.runner.Cancel(id); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "canceled"})
}

func (h *TaskHandler) Logs(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	query := h.db.Where("task_id = ?", id)

	if level := strings.TrimSpace(c.Query("level")); level != "" {
		query = query.Where("LOWER(level) = LOWER(?)", level)
	}
	if rawBeforeID := strings.TrimSpace(c.Query("before_id")); rawBeforeID != "" {
		if parsed, err := strconv.ParseUint(rawBeforeID, 10, 64); err == nil && parsed > 0 {
			query = query.Where("id < ?", parsed)
		}
	}

	limit := 200
	if rawLimit := strings.TrimSpace(c.Query("limit")); rawLimit != "" {
		if parsed, err := strconv.Atoi(rawLimit); err == nil && parsed > 0 && parsed <= 500 {
			limit = parsed
		}
	}

	var logs []model.TaskLog
	if err := query.Order("id desc").Limit(limit).Find(&logs).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": logs})
}

func trimTaskRequest(req *taskRequest) {
	req.Name = strings.TrimSpace(req.Name)
	req.Command = strings.TrimSpace(req.Command)
	req.RsyncSource = strings.TrimSpace(req.RsyncSource)
	req.RsyncTarget = strings.TrimSpace(req.RsyncTarget)
	req.ExecutorType = strings.TrimSpace(strings.ToLower(req.ExecutorType))
	req.CronSpec = strings.TrimSpace(req.CronSpec)
}

func hydrateTaskDefaultsFromPolicy(db *gorm.DB, req *taskRequest) {
	if req.PolicyID == nil {
		return
	}
	var policy model.Policy
	if err := db.First(&policy, *req.PolicyID).Error; err != nil {
		return
	}
	if strings.TrimSpace(req.RsyncSource) == "" {
		req.RsyncSource = policy.SourcePath
	}
	if strings.TrimSpace(req.RsyncTarget) == "" {
		req.RsyncTarget = policy.TargetPath
	}
	if strings.TrimSpace(req.CronSpec) == "" {
		req.CronSpec = policy.CronSpec
	}
}

func inferTaskExecutor(req *taskRequest, _ string) {
	if strings.TrimSpace(req.ExecutorType) != "" {
		req.ExecutorType = strings.TrimSpace(strings.ToLower(req.ExecutorType))
		return
	}
	req.ExecutorType = "rsync"
}

func validateTaskRequest(req taskRequest) error {
	if req.Name == "" {
		return fmt.Errorf("任务名称不能为空")
	}
	if req.NodeID == 0 {
		return fmt.Errorf("node_id 不能为空")
	}
	if req.ExecutorType != "rsync" {
		return fmt.Errorf("仅支持 rsync executor_type")
	}
	if strings.TrimSpace(req.Command) != "" {
		return fmt.Errorf("command 执行已禁用，请使用 rsync_source 与 rsync_target")
	}
	if req.CronSpec != "" {
		if err := validateCronSpec(req.CronSpec); err != nil {
			return err
		}
	}

	if strings.TrimSpace(req.RsyncSource) == "" || strings.TrimSpace(req.RsyncTarget) == "" {
		return fmt.Errorf("rsync 任务必须提供 rsync_source 和 rsync_target")
	}

	sourceAllowList := parseCSVEnvList("RSYNC_ALLOWED_SOURCE_PREFIXES")
	targetAllowList := parseCSVEnvList("RSYNC_ALLOWED_TARGET_PREFIXES")

	if !util.IsRemotePathSpec(req.RsyncSource) {
		if err := validatePathByPrefix(req.RsyncSource, sourceAllowList, "rsync_source"); err != nil {
			return err
		}
	}
	if !util.IsRemotePathSpec(req.RsyncTarget) {
		if err := validatePathByPrefix(req.RsyncTarget, targetAllowList, "rsync_target"); err != nil {
			return err
		}
	}

	return nil
}

func parseTaskSort(raw string) string {
	const defaultOrder = "id asc"

	field := strings.ToLower(strings.TrimSpace(raw))
	if field == "" {
		return defaultOrder
	}

	direction := "asc"
	if strings.HasPrefix(field, "-") {
		direction = "desc"
		field = strings.TrimPrefix(field, "-")
	}

	switch {
	case strings.Contains(field, ":"):
		parts := strings.SplitN(field, ":", 2)
		field = strings.TrimSpace(parts[0])
		direction = normalizeSortDirection(parts[1], direction)
	case strings.Contains(field, " "):
		parts := strings.Fields(field)
		if len(parts) > 0 {
			field = parts[0]
		}
		if len(parts) > 1 {
			direction = normalizeSortDirection(parts[1], direction)
		}
	case strings.HasSuffix(field, "_desc"):
		field = strings.TrimSuffix(field, "_desc")
		direction = "desc"
	case strings.HasSuffix(field, "_asc"):
		field = strings.TrimSuffix(field, "_asc")
		direction = "asc"
	}

	allowedFields := map[string]string{
		"id":          "id",
		"name":        "name",
		"status":      "status",
		"node_id":     "node_id",
		"policy_id":   "policy_id",
		"created_at":  "created_at",
		"updated_at":  "updated_at",
		"last_run_at": "last_run_at",
		"next_run_at": "next_run_at",
	}
	column, ok := allowedFields[field]
	if !ok {
		return defaultOrder
	}
	if direction != "desc" {
		direction = "asc"
	}
	return fmt.Sprintf("%s %s", column, direction)
}

func normalizeSortDirection(raw string, fallback string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "asc", "ascending":
		return "asc"
	case "desc", "descending":
		return "desc"
	default:
		return fallback
	}
}
