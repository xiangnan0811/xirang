package handlers

import (
	"encoding/json"
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
	TriggerManual(taskID uint) (uint, error)
	TriggerRestore(taskID uint, targetPath string) (uint, error)
	SyncSchedule(task model.Task) error
	RemoveSchedule(taskID uint)
	Cancel(taskID uint) error
}

type TaskHandler struct {
	db     *gorm.DB
	runner TaskRunner
}

type taskRefValidationError struct {
	message string
}

func (e *taskRefValidationError) Error() string {
	return e.message
}

func NewTaskHandler(db *gorm.DB, runner TaskRunner) *TaskHandler {
	return &TaskHandler{db: db, runner: runner}
}

type taskRequest struct {
	Name            string `json:"name" binding:"required"`
	NodeID          uint   `json:"node_id" binding:"required"`
	PolicyID        *uint  `json:"policy_id"`
	DependsOnTaskID *uint  `json:"depends_on_task_id"`
	Command         string `json:"command"`
	RsyncSource     string `json:"rsync_source"`
	RsyncTarget     string `json:"rsync_target"`
	ExecutorType    string `json:"executor_type"`
	ExecutorConfig  string `json:"executor_config"`
	CronSpec        string `json:"cron_spec"`
}

func (h *TaskHandler) List(c *gin.Context) {
	query := h.db.Model(&model.Task{})

	if nodeIDs, needFilter, err := ownershipNodeFilter(c, h.db); err != nil {
		respondInternalError(c, err)
		return
	} else if needFilter {
		query = query.Where("node_id IN ?", nodeIDs)
	}

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
		escaped := strings.NewReplacer("%", "\\%", "_", "\\_").Replace(keyword)
		fuzzyKeyword := "%" + escaped + "%"
		query = query.Where("name LIKE ? ESCAPE '\\' OR command LIKE ? ESCAPE '\\' OR rsync_source LIKE ? ESCAPE '\\' OR rsync_target LIKE ? ESCAPE '\\'", fuzzyKeyword, fuzzyKeyword, fuzzyKeyword, fuzzyKeyword)
	}

	pg := parsePagination(c, 100, "created_at", map[string]bool{
		"id": true, "created_at": true, "status": true, "name": true, "node_id": true,
	})
	// 向后兼容旧 sort 参数（如 sort=-id）
	orderClause := parseTaskSort(c.Query("sort"))
	if c.Query("sort") == "" {
		orderClause = pg.SortBy + " " + pg.SortOrder + ", id desc"
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		respondInternalError(c, err)
		return
	}

	offset := (pg.Page - 1) * pg.PageSize
	var tasks []model.Task
	if err := query.Preload("Node").Preload("Policy").Order(orderClause).Offset(offset).Limit(pg.PageSize).Find(&tasks).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	for i := range tasks {
		tasks[i].Node = sanitizeNode(tasks[i].Node)
	}
	paginatedResponse(c, tasks, total, pg)
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
	taskEntity.Node = sanitizeNode(taskEntity.Node)
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
	if err := h.validateTaskRefs(req); err != nil {
		if isTaskRefValidationError(err) {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		} else {
			respondInternalError(c, err)
		}
		return
	}

	taskEntity := model.Task{
		Name:            req.Name,
		NodeID:          req.NodeID,
		PolicyID:        req.PolicyID,
		DependsOnTaskID: req.DependsOnTaskID,
		Command:         req.Command,
		RsyncSource:     req.RsyncSource,
		RsyncTarget:     req.RsyncTarget,
		ExecutorType:    req.ExecutorType,
		ExecutorConfig:  req.ExecutorConfig,
		CronSpec:        req.CronSpec,
		Status:          string(task.StatusPending),
	}
	if err := h.db.Create(&taskEntity).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	if h.runner != nil {
		if err := h.runner.SyncSchedule(taskEntity); err != nil {
			h.runner.RemoveSchedule(taskEntity.ID)
			if rollbackErr := h.db.Delete(&model.Task{}, taskEntity.ID).Error; rollbackErr != nil {
				respondInternalError(c, fmt.Errorf("任务调度同步失败且补偿删除失败: %v", rollbackErr))
				return
			}
			c.JSON(http.StatusBadRequest, gin.H{"error": "任务调度失败，请检查 Cron 表达式是否正确"})
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
	if err := h.validateTaskRefsWithID(req, id); err != nil {
		if isTaskRefValidationError(err) {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		} else {
			respondInternalError(c, err)
		}
		return
	}

	taskEntity.Name = req.Name
	taskEntity.NodeID = req.NodeID
	taskEntity.PolicyID = req.PolicyID
	taskEntity.DependsOnTaskID = req.DependsOnTaskID
	taskEntity.Command = req.Command
	taskEntity.RsyncSource = req.RsyncSource
	taskEntity.RsyncTarget = req.RsyncTarget
	taskEntity.ExecutorType = req.ExecutorType
	taskEntity.ExecutorConfig = req.ExecutorConfig
	taskEntity.CronSpec = req.CronSpec

	if err := h.db.Save(&taskEntity).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	if h.runner != nil {
		if err := h.runner.SyncSchedule(taskEntity); err != nil {
			h.runner.RemoveSchedule(taskEntity.ID)
			if restoreErr := h.db.Save(&previous).Error; restoreErr != nil {
				respondInternalError(c, fmt.Errorf("任务调度同步失败且补偿回滚失败: %v", restoreErr))
				return
			}
			if restoreScheduleErr := h.runner.SyncSchedule(previous); restoreScheduleErr != nil {
				h.runner.RemoveSchedule(taskEntity.ID)
				respondInternalError(c, fmt.Errorf("任务调度同步失败且补偿调度失败: %v", restoreScheduleErr))
				return
			}
			c.JSON(http.StatusBadRequest, gin.H{"error": "任务调度失败，请检查 Cron 表达式是否正确"})
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
	// 防止删除被其他任务依赖的任务
	var depCount int64
	if err := h.db.Model(&model.Task{}).Where("depends_on_task_id = ?", id).Count(&depCount).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	if depCount > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "该任务被其他任务依赖，请先解除依赖关系再删除"})
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
	runID, err := h.runner.TriggerManual(id)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"message": "triggered", "run_id": runID})
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

// Restore 触发备份恢复，将备份数据反向同步回源路径或指定的自定义路径。
func (h *TaskHandler) Restore(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}

	var req struct {
		TargetPath string `json:"target_path"`
	}
	// 允许空 body（使用默认恢复路径）
	_ = c.ShouldBindJSON(&req)

	runID, err := h.runner.TriggerRestore(id, req.TargetPath)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "restore triggered",
		"run_id":  runID,
	})
}

// BatchTrigger 批量触发任务执行。
// POST /tasks/batch-trigger
func (h *TaskHandler) BatchTrigger(c *gin.Context) {
	var req struct {
		TaskIDs []uint `json:"task_ids" binding:"required,min=1"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数不合法"})
		return
	}

	type triggerResult struct {
		TaskID uint   `json:"task_id"`
		RunID  uint   `json:"run_id,omitempty"`
		Error  string `json:"error,omitempty"`
	}

	// ownership 校验：operator 仅允许触发自己拥有的节点上的任务
	nodeIDs, needFilter, err := ownershipNodeFilter(c, h.db)
	if err != nil {
		respondInternalError(c, err)
		return
	}
	var allowedNodeIDSet map[uint]struct{}
	if needFilter {
		allowedNodeIDSet = make(map[uint]struct{}, len(nodeIDs))
		for _, nid := range nodeIDs {
			allowedNodeIDSet[nid] = struct{}{}
		}
	}

	results := make([]triggerResult, 0, len(req.TaskIDs))
	successCount := 0
	for _, tid := range req.TaskIDs {
		if needFilter {
			var t model.Task
			if lookupErr := h.db.Select("id", "node_id").First(&t, tid).Error; lookupErr != nil {
				results = append(results, triggerResult{TaskID: tid, Error: "任务不存在"})
				continue
			}
			if _, ok := allowedNodeIDSet[t.NodeID]; !ok {
				results = append(results, triggerResult{TaskID: tid, Error: "无权操作该任务"})
				continue
			}
		}
		runID, err := h.runner.TriggerManual(tid)
		if err != nil {
			results = append(results, triggerResult{TaskID: tid, Error: err.Error()})
			continue
		}
		results = append(results, triggerResult{TaskID: tid, RunID: runID})
		successCount++
	}

	c.JSON(http.StatusOK, gin.H{
		"results":       results,
		"total":         len(req.TaskIDs),
		"success_count": successCount,
	})
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

func newTaskRefValidationError(message string) error {
	return &taskRefValidationError{message: message}
}

func isTaskRefValidationError(err error) bool {
	_, ok := err.(*taskRefValidationError)
	return ok
}

func (h *TaskHandler) validateTaskRefs(req taskRequest) error {
	return h.validateTaskRefsWithID(req, 0)
}

func (h *TaskHandler) validateTaskRefsWithID(req taskRequest, selfID uint) error {
	if req.NodeID != 0 {
		var count int64
		if err := h.db.Model(&model.Node{}).Where("id = ?", req.NodeID).Count(&count).Error; err != nil {
			return fmt.Errorf("校验节点失败: %w", err)
		}
		if count == 0 {
			return newTaskRefValidationError("所选节点不存在，请重新选择")
		}
	}
	if req.PolicyID != nil {
		var count int64
		if err := h.db.Model(&model.Policy{}).Where("id = ?", *req.PolicyID).Count(&count).Error; err != nil {
			return fmt.Errorf("校验策略失败: %w", err)
		}
		if count == 0 {
			return newTaskRefValidationError("所选策略不存在，请重新选择")
		}
	}
	if req.DependsOnTaskID != nil {
		// cron 与依赖链互斥：有前置任务的任务不能设置 cron
		if strings.TrimSpace(req.CronSpec) != "" {
			return newTaskRefValidationError("设置了前置任务的任务不能同时设置定时调度")
		}
		// 前置任务不能是自身
		if selfID != 0 && *req.DependsOnTaskID == selfID {
			return newTaskRefValidationError("任务不能依赖自身")
		}
		// 检查前置任务是否存在
		var count int64
		if err := h.db.Model(&model.Task{}).Where("id = ?", *req.DependsOnTaskID).Count(&count).Error; err != nil {
			return fmt.Errorf("校验前置任务失败: %w", err)
		}
		if count == 0 {
			return newTaskRefValidationError("所选前置任务不存在，请重新选择")
		}
		// 环路检测：从前置任务向上追溯，深度不超过 10
		if selfID != 0 {
			if err := h.detectDependencyCycle(selfID, *req.DependsOnTaskID, 10); err != nil {
				return err
			}
		}
	}
	return nil
}

// detectDependencyCycle 从 startID 开始沿 depends_on_task_id 链向上追溯，
// 若遍历到 selfID 则说明形成环路，maxDepth 为最大追溯深度。
func (h *TaskHandler) detectDependencyCycle(selfID, startID uint, maxDepth int) error {
	current := startID
	for i := 0; i < maxDepth; i++ {
		var t model.Task
		if err := h.db.Select("id", "depends_on_task_id").First(&t, current).Error; err != nil {
			return nil // 任务不存在，无法继续追溯
		}
		if t.DependsOnTaskID == nil {
			return nil
		}
		if *t.DependsOnTaskID == selfID {
			return newTaskRefValidationError("检测到循环依赖，请检查前置任务配置")
		}
		current = *t.DependsOnTaskID
	}
	return nil
}

func validateTaskRequest(req taskRequest) error {
	if req.Name == "" {
		return fmt.Errorf("任务名称不能为空")
	}
	if req.NodeID == 0 {
		return fmt.Errorf("请选择目标节点")
	}
	switch req.ExecutorType {
	case "rsync", "command", "restic", "rclone":
	default:
		return fmt.Errorf("不支持的执行器类型，仅允许 rsync / command / restic / rclone")
	}
	if req.CronSpec != "" {
		if err := validateCronSpec(req.CronSpec); err != nil {
			return err
		}
	}

	if req.ExecutorType == "command" {
		if strings.TrimSpace(req.Command) == "" {
			return fmt.Errorf("命令类型任务必须填写命令内容")
		}
	} else {
		if strings.TrimSpace(req.RsyncSource) == "" || strings.TrimSpace(req.RsyncTarget) == "" {
			return fmt.Errorf("同步任务必须填写源路径和目标路径")
		}
	}

	if cfg := strings.TrimSpace(req.ExecutorConfig); cfg != "" {
		if !json.Valid([]byte(cfg)) {
			return fmt.Errorf("executor_config 必须是合法的 JSON 格式")
		}
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
	const defaultOrder = "created_at desc, id desc"

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
