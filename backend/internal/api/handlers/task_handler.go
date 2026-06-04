package handlers

import (
	"fmt"
	"strconv"
	"strings"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/credentialaudit"
	"xirang/backend/internal/model"
	gormrepo "xirang/backend/internal/repository/gorm"
	"xirang/backend/internal/sshutil"
	"xirang/backend/internal/task"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type TaskRunner interface {
	TriggerManual(taskID uint) (uint, error)
	TriggerRestore(taskID uint, targetPath string) (uint, error)
	SyncSchedule(task model.Task) error
	RemoveSchedule(taskID uint)
	Cancel(taskID uint) error
	Pause(taskID uint, cancelRunning bool) error
	Resume(taskID uint) error
	SetSkipNext(taskID uint) error
}

type TaskHandler struct {
	db         *gorm.DB
	runner     TaskRunner
	svc        *task.TaskApiService
	jwtManager *auth.JWTManager
}

func NewTaskHandler(db *gorm.DB, runner TaskRunner) *TaskHandler {
	return &TaskHandler{db: db, runner: runner}
}

// WithTaskApiService injects a TaskApiService for business logic delegation.
func (h *TaskHandler) WithTaskApiService(svc *task.TaskApiService) *TaskHandler {
	h.svc = svc
	return h
}

// service returns the injected TaskApiService, or lazily creates one
// from the handler's db and runner (backward-compatible with tests).
func (h *TaskHandler) service() *task.TaskApiService {
	if h.svc != nil {
		return h.svc
	}
	nodeRepo := gormrepo.NewNodeRepository(h.db)
	policyRepo := gormrepo.NewPolicyRepository(h.db)
	taskRepo := gormrepo.NewTaskRepository(h.db)
	return task.NewTaskApiService(taskRepo, nodeRepo, policyRepo, h.runner)
}

func (h *TaskHandler) WithJWTManager(jwtManager *auth.JWTManager) *TaskHandler {
	h.jwtManager = jwtManager
	return h
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

func sanitizeTaskForResponse(taskEntity model.Task) model.Task {
	taskEntity.LastError = task.SanitizeRuntimeEvidenceForRead(taskEntity.LastError)
	if taskEntity.Policy != nil {
		policyCopy := *taskEntity.Policy
		policyCopy.PreHook = ""
		policyCopy.PostHook = ""
		taskEntity.Policy = &policyCopy
	}
	return taskEntity
}

func sanitizeTasksForResponse(tasks []model.Task) []model.Task {
	if len(tasks) == 0 {
		return tasks
	}
	sanitized := make([]model.Task, len(tasks))
	for i, taskEntity := range tasks {
		sanitized[i] = sanitizeTaskForResponse(taskEntity)
	}
	return sanitized
}

// List godoc
// @Summary      列出任务
// @Description  返回任务列表（分页），支持按状态、节点、策略、关键字过滤
// @Tags         tasks
// @Security     Bearer
// @Produce      json
// @Param        page       query     int     false  "页码（默认 1）"
// @Param        page_size  query     int     false  "每页条数（默认 20，最大 100）"
// @Param        status     query     string  false  "任务状态过滤"
// @Param        node_id    query     int     false  "节点 ID 过滤"
// @Param        policy_id  query     int     false  "策略 ID 过滤"
// @Param        keyword    query     string  false  "关键字模糊搜索"
// @Success      200  {object}  handlers.PaginatedResponse{data=[]model.Task}
// @Failure      401  {object}  handlers.Response
// @Router       /tasks [get]
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
		tasks[i].Node = tasks[i].Node.Sanitized()
	}
	// 为有 running TaskRun 的任务填充实时进度（覆盖备份和恢复场景）
	taskIDs := make([]uint, len(tasks))
	for i, t := range tasks {
		taskIDs[i] = t.ID
	}
	if len(taskIDs) > 0 {
		type taskProgress struct {
			TaskID   uint
			Progress int
		}
		var rows []taskProgress
		if err := h.db.Raw(
			"SELECT task_id, progress FROM task_runs WHERE id IN "+
				"(SELECT MAX(id) FROM task_runs WHERE task_id IN ? AND status = 'running' GROUP BY task_id)",
			taskIDs,
		).Scan(&rows).Error; err != nil {
			rows = nil
		}
		pm := make(map[uint]int, len(rows))
		for _, r := range rows {
			pm[r.TaskID] = r.Progress
		}
		for i := range tasks {
			if p, ok := pm[tasks[i].ID]; ok {
				v := p
				tasks[i].Progress = &v
			}
		}
	}
	respondPaginated(c, sanitizeTasksForResponse(tasks), total, pg.Page, pg.PageSize)
}

// Get godoc
// @Summary      获取任务详情
// @Description  返回单个任务的详细信息
// @Tags         tasks
// @Security     Bearer
// @Produce      json
// @Param        id   path      int  true  "任务 ID"
// @Success      200  {object}  handlers.Response{data=model.Task}
// @Failure      401  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Router       /tasks/{id} [get]
func (h *TaskHandler) Get(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var taskEntity model.Task
	if err := h.db.Preload("Node").Preload("Policy").First(&taskEntity, id).Error; err != nil {
		respondNotFound(c, "任务不存在")
		return
	}
	taskEntity.Node = taskEntity.Node.Sanitized()
	// 查询最新 running TaskRun 的进度（覆盖备份和恢复场景）
	var runProgress []int
	if err := h.db.Model(&model.TaskRun{}).
		Where("task_id = ? AND status = ?", taskEntity.ID, "running").
		Order("id DESC").Limit(1).
		Pluck("progress", &runProgress).Error; err == nil && len(runProgress) > 0 {
		taskEntity.Progress = &runProgress[0]
	}
	respondOK(c, sanitizeTaskForResponse(taskEntity))
}

// Create godoc
// @Summary      创建任务
// @Description  创建新的运维任务
// @Tags         tasks
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        body  body      taskRequest  true  "创建任务请求"
// @Success      201   {object}  handlers.Response{data=model.Task}
// @Failure      400   {object}  handlers.Response
// @Failure      401   {object}  handlers.Response
// @Failure      403   {object}  handlers.Response
// @Router       /tasks [post]
func (h *TaskHandler) Create(c *gin.Context) {
	var req taskRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}

	if allowed, err := authorizeNodeOwnership(c, h.db, req.NodeID); err != nil {
		respondInternalError(c, err)
		return
	} else if !allowed {
		respondForbidden(c, "无权访问该节点")
		return
	}

	taskEntity, err := h.service().CreateTask(c.Request.Context(), task.CreateTaskInput{
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
	})
	if err != nil {
		if task.IsTaskValidationError(err) {
			respondBadRequest(c, err.Error())
		} else {
			respondInternalError(c, err)
		}
		return
	}
	respondCreated(c, taskEntity)
}

// Update godoc
// @Summary      更新任务
// @Description  更新任务配置
// @Tags         tasks
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        id    path      int          true  "任务 ID"
// @Param        body  body      taskRequest  true  "更新任务请求"
// @Success      200   {object}  handlers.Response{data=model.Task}
// @Failure      400   {object}  handlers.Response
// @Failure      401   {object}  handlers.Response
// @Failure      404   {object}  handlers.Response
// @Router       /tasks/{id} [put]
func (h *TaskHandler) Update(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var req taskRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}

	if allowed, err := authorizeNodeOwnership(c, h.db, req.NodeID); err != nil {
		respondInternalError(c, err)
		return
	} else if !allowed {
		respondForbidden(c, "无权访问该节点")
		return
	}

	taskEntity, err := h.service().UpdateTask(c.Request.Context(), id, task.CreateTaskInput{
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
	})
	if err != nil {
		if task.IsTaskValidationError(err) {
			respondBadRequest(c, err.Error())
		} else {
			respondInternalError(c, err)
		}
		return
	}
	respondOK(c, taskEntity)
}

// Delete godoc
// @Summary      删除任务
// @Description  删除指定任务（有依赖关系时拒绝）
// @Tags         tasks
// @Security     Bearer
// @Produce      json
// @Param        id   path      int  true  "任务 ID"
// @Success      200  {object}  handlers.Response
// @Failure      400  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Failure      409  {object}  handlers.Response
// @Router       /tasks/{id} [delete]
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
		respondConflict(c, "该任务被其他任务依赖，请先解除依赖关系再删除")
		return
	}
	if err := h.db.Delete(&model.Task{}, id).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	if h.runner != nil {
		h.runner.RemoveSchedule(id)
	}
	respondMessage(c, "deleted")
}

// Trigger godoc
// @Summary      手动触发任务
// @Description  立即手动触发任务执行，返回新创建的 run_id
// @Tags         tasks
// @Security     Bearer
// @Produce      json
// @Param        id   path      int  true  "任务 ID"
// @Success      202  {object}  handlers.Response
// @Failure      400  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Router       /tasks/{id}/trigger [post]
func (h *TaskHandler) Trigger(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	auditTask, hasAuditTask := h.loadTaskCredentialAuditContext(id)
	auditPurpose := taskCredentialAuditPurpose(auditTask, hasAuditTask)
	auditMetadata := taskCredentialAuditMetadata("manual", auditTask, hasAuditTask)
	nodeID := taskCredentialAuditNodeID(auditTask, hasAuditTask)

	runID, err := h.service().TriggerTask(id)
	if err != nil {
		writeCredentialAuditFromGin(c, h.db, credentialaudit.Event{
			Action:       "task.manual_trigger",
			Purpose:      auditPurpose,
			NodeID:       nodeID,
			TaskID:       credentialaudit.PtrUint(id),
			Outcome:      credentialaudit.OutcomeFailure,
			ErrorMessage: err.Error(),
			Metadata:     auditMetadata,
		})
		respondBadRequest(c, err.Error())
		return
	}
	writeCredentialAuditFromGin(c, h.db, credentialaudit.Event{
		Action:    "task.manual_trigger",
		Purpose:   auditPurpose,
		NodeID:    nodeID,
		TaskID:    credentialaudit.PtrUint(id),
		TaskRunID: credentialaudit.PtrUint(runID),
		Outcome:   credentialaudit.OutcomeSuccess,
		Metadata:  auditMetadata,
	})
	respondAccepted(c, gin.H{"message": "triggered", "run_id": runID})
}

// Cancel godoc
// @Summary      取消任务
// @Description  取消正在运行的任务
// @Tags         tasks
// @Security     Bearer
// @Produce      json
// @Param        id   path      int  true  "任务 ID"
// @Success      200  {object}  handlers.Response
// @Failure      400  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Router       /tasks/{id}/cancel [post]
func (h *TaskHandler) Cancel(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	if err := h.runner.Cancel(id); err != nil {
		respondBadRequest(c, err.Error())
		return
	}
	respondMessage(c, "canceled")
}

// Pause 暂停任务调度。
// @Summary      暂停任务
// @Description  暂停任务的定时调度，可选是否取消当前运行
// @Tags         tasks
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        id    path      int  true  "任务 ID"
// @Param        body  body      object  false  "可选：cancel_running=true 取消当前运行"
// @Success      200   {object}  handlers.Response
// @Failure      400   {object}  handlers.Response
// @Failure      401   {object}  handlers.Response
// @Router       /tasks/{id}/pause [post]
func (h *TaskHandler) Pause(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var req struct {
		CancelRunning bool `json:"cancel_running"`
	}
	_ = c.ShouldBindJSON(&req) // optional body
	if err := h.runner.Pause(id, req.CancelRunning); err != nil {
		respondBadRequest(c, err.Error())
		return
	}
	respondMessage(c, "paused")
}

// Resume 恢复任务调度。
// @Summary      恢复任务
// @Description  恢复已暂停任务的定时调度
// @Tags         tasks
// @Security     Bearer
// @Produce      json
// @Param        id   path      int  true  "任务 ID"
// @Success      200  {object}  handlers.Response
// @Failure      400  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Router       /tasks/{id}/resume [post]
func (h *TaskHandler) Resume(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	if err := h.runner.Resume(id); err != nil {
		respondBadRequest(c, err.Error())
		return
	}
	respondMessage(c, "resumed")
}

// SkipNext 跳过 cron 任务的下一次执行。
// @Summary      跳过下次执行
// @Description  标记跳过 cron 任务的下一次定时执行
// @Tags         tasks
// @Security     Bearer
// @Produce      json
// @Param        id   path      int  true  "任务 ID"
// @Success      200  {object}  handlers.Response
// @Failure      400  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Router       /tasks/{id}/skip-next [post]
func (h *TaskHandler) SkipNext(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	if err := h.runner.SetSkipNext(id); err != nil {
		respondBadRequest(c, err.Error())
		return
	}
	respondMessage(c, "skip_next set")
}

// Restore 触发备份恢复，将备份数据反向同步回源路径或指定的自定义路径。
// @Summary      触发备份恢复
// @Description  将备份数据反向同步回源路径或指定的自定义路径
// @Tags         tasks
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        id    path      int     true   "任务 ID"
// @Param        body  body      object  false  "可选：target_path 自定义恢复路径"
// @Success      200   {object}  handlers.Response
// @Failure      400   {object}  handlers.Response
// @Failure      401   {object}  handlers.Response
// @Router       /tasks/{id}/restore [post]
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
		writeCredentialAuditFromGin(c, h.db, credentialaudit.Event{
			Action:       "task.restore_trigger",
			Purpose:      sshutil.PurposeTaskRestore,
			TaskID:       credentialaudit.PtrUint(id),
			Outcome:      credentialaudit.OutcomeFailure,
			ErrorMessage: err.Error(),
			Metadata: map[string]any{
				"custom_target": strings.TrimSpace(req.TargetPath) != "",
			},
		})
		respondBadRequest(c, err.Error())
		return
	}

	writeCredentialAuditFromGin(c, h.db, credentialaudit.Event{
		Action:    "task.restore_trigger",
		Purpose:   sshutil.PurposeTaskRestore,
		TaskID:    credentialaudit.PtrUint(id),
		TaskRunID: credentialaudit.PtrUint(runID),
		Outcome:   credentialaudit.OutcomeSuccess,
		Metadata: map[string]any{
			"custom_target": strings.TrimSpace(req.TargetPath) != "",
		},
	})

	respondOK(c, gin.H{
		"message": "restore triggered",
		"run_id":  runID,
	})
}

// BatchTrigger 批量触发任务执行。
// @Summary      批量触发任务
// @Description  批量手动触发多个任务执行
// @Tags         tasks
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        body  body      object  true  "task_ids 数组"
// @Success      200   {object}  handlers.Response
// @Failure      400   {object}  handlers.Response
// @Failure      401   {object}  handlers.Response
// @Router       /tasks/batch-trigger [post]
func (h *TaskHandler) BatchTrigger(c *gin.Context) {
	var req struct {
		TaskIDs []uint `json:"task_ids" binding:"required,min=1"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, "请求参数不合法")
		return
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

	results := make([]task.BulkTriggerResult, 0, len(req.TaskIDs))
	tasksToTrigger := make([]uint, 0, len(req.TaskIDs))
	successCount := 0
	failureCount := 0
	blockedCount := 0

	// Batch query to avoid N+1
	var tasks []model.Task
	if lookupErr := h.db.Select("id", "node_id").Where("id IN ?", req.TaskIDs).Find(&tasks).Error; lookupErr != nil {
		respondInternalError(c, lookupErr)
		return
	}
	taskMap := make(map[uint]model.Task, len(tasks))
	for _, t := range tasks {
		taskMap[t.ID] = t
	}
	for _, tid := range req.TaskIDs {
		t, found := taskMap[tid]
		if !found {
			failureCount++
			results = append(results, task.BulkTriggerResult{TaskID: tid, Error: "任务不存在"})
			continue
		}
		if needFilter {
			if _, ok := allowedNodeIDSet[t.NodeID]; !ok {
				blockedCount++
				results = append(results, task.BulkTriggerResult{TaskID: tid, Error: "无权操作该任务"})
				continue
			}
		}
		tasksToTrigger = append(tasksToTrigger, tid)
	}

	if len(tasksToTrigger) > 0 && !EnforceStepUp(c, h.db, h.jwtManager, CredentialGrantActionTaskBatchTrigger, sshutil.PurposeTaskCommand, "task_bulk_run") {
		return
	}
	if len(tasksToTrigger) > 0 && !EnforceTaskBatchTriggerCredentialGrants(c, h.db, tasksToTrigger) {
		return
	}

	if len(tasksToTrigger) > 0 {
		svcResults := h.service().BulkTriggerTasks(c.Request.Context(), tasksToTrigger)
		for _, r := range svcResults {
			if r.Error != "" {
				failureCount++
			} else {
				successCount++
			}
			results = append(results, r)
		}
	}

	if len(tasksToTrigger) == 0 {
		writeCredentialAuditFromGin(c, h.db, credentialaudit.Event{
			Action:  "task.batch_trigger",
			Purpose: sshutil.PurposeTaskCommand,
			Outcome: credentialAuditNoExecutionOutcome(failureCount, blockedCount),
			Metadata: map[string]any{
				"stage":           "no_op",
				"requested_count": len(req.TaskIDs),
				"eligible_count":  0,
				"executed_count":  0,
				"failure_count":   failureCount,
				"blocked_count":   blockedCount,
				"no_op":           true,
			},
		})
		respondOK(c, gin.H{
			"results":       results,
			"total":         len(req.TaskIDs),
			"success_count": successCount,
		})
		return
	}

	writeCredentialAuditFromGin(c, h.db, credentialaudit.Event{
		Action:  "task.batch_trigger",
		Purpose: sshutil.PurposeTaskCommand,
		Outcome: credentialAuditOutcome(successCount, failureCount, blockedCount),
		Metadata: map[string]any{
			"task_count":    len(req.TaskIDs),
			"success_count": successCount,
			"failure_count": failureCount,
			"blocked_count": blockedCount,
			"purpose":       "mixed_task_trigger",
		},
	})

	respondOK(c, gin.H{
		"results":       results,
		"total":         len(req.TaskIDs),
		"success_count": successCount,
	})
}

// Logs godoc
// @Summary      获取任务日志
// @Description  返回任务的执行日志列表
// @Tags         tasks
// @Security     Bearer
// @Produce      json
// @Param        id        path      int     true   "任务 ID"
// @Param        level     query     string  false  "日志级别过滤"
// @Param        before_id query     int     false  "游标：返回此 ID 之前的日志"
// @Param        limit     query     int     false  "返回条数（默认 200，最大 500）"
// @Success      200  {object}  handlers.Response{data=[]model.TaskLog}
// @Failure      401  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Router       /tasks/{id}/logs [get]
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
	respondOK(c, sanitizeTaskLogsForResponse(logs))
}

func (h *TaskHandler) loadTaskCredentialAuditContext(taskID uint) (model.Task, bool) {
	if h == nil || h.db == nil || taskID == 0 {
		return model.Task{}, false
	}
	var task model.Task
	if err := h.db.Select("id", "node_id", "policy_id", "executor_type", "source").First(&task, taskID).Error; err != nil {
		return model.Task{}, false
	}
	return task, true
}

func taskCredentialAuditNodeID(task model.Task, ok bool) *uint {
	if !ok {
		return nil
	}
	return credentialaudit.PtrUint(task.NodeID)
}

func taskCredentialAuditPurpose(task model.Task, ok bool) string {
	if !ok {
		return sshutil.PurposeTaskCommand
	}
	switch strings.TrimSpace(strings.ToLower(task.ExecutorType)) {
	case "command":
		if strings.TrimSpace(task.Source) == "batch" {
			return sshutil.PurposeBatchCommand
		}
		return sshutil.PurposeTaskCommand
	case "rsync", "restic", "rclone":
		return sshutil.PurposeTaskBackup
	default:
		return sshutil.PurposeTaskCommand
	}
}

func taskCredentialAuditMetadata(triggerType string, task model.Task, ok bool) map[string]any {
	metadata := map[string]any{
		"trigger_type": triggerType,
	}
	if !ok {
		return metadata
	}
	metadata["executor_type"] = strings.TrimSpace(strings.ToLower(task.ExecutorType))
	if strings.TrimSpace(task.Source) != "" {
		metadata["source"] = strings.TrimSpace(task.Source)
	}
	if task.PolicyID != nil && *task.PolicyID != 0 {
		metadata["policy_id"] = *task.PolicyID
	}
	return metadata
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
