package handlers

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/credentialaudit"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const maxCommandLength = 4096

type BatchTaskRunner interface {
	TriggerManual(taskID uint) (uint, error)
	RemoveSchedule(taskID uint)
}

// BatchHandler 处理批量命令执行相关请求。
type BatchHandler struct {
	db         *gorm.DB
	manager    BatchTaskRunner
	jwtManager *auth.JWTManager
}

func NewBatchHandler(db *gorm.DB, manager BatchTaskRunner) *BatchHandler {
	return &BatchHandler{db: db, manager: manager}
}

func (h *BatchHandler) WithJWTManager(jwtManager *auth.JWTManager) *BatchHandler {
	h.jwtManager = jwtManager
	return h
}

// Create godoc
// @Summary      创建批量命令
// @Description  在多个节点上批量创建并触发命令执行任务
// @Tags         batch
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        Idempotency-Key header string true "请求者范围的幂等键；同一请求重试必须复用"
// @Param        body  body      batchCommandRequest  true  "批量命令请求"
// @Success      200  {object}  handlers.Response
// @Failure      400  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Failure      409  {object}  handlers.Response
// @Failure      403  {object}  handlers.Response
// @Router       /batch-commands [post]
func (h *BatchHandler) Create(c *gin.Context) {
	var req batchCommandRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, "请求参数错误")
		return
	}

	command := strings.TrimSpace(req.Command)
	if command == "" {
		respondBadRequest(c, "命令不能为空")
		return
	}
	if len(command) > maxCommandLength {
		respondBadRequest(c, fmt.Sprintf("命令长度不能超过 %d 字符", maxCommandLength))
		return
	}

	// 危险命令拦截
	if isDangerousCommand(command) {
		respondBadRequest(c, "该命令被安全策略拦截，禁止执行")
		return
	}

	req.Command = command
	req.Name = strings.TrimSpace(req.Name)
	nodes := make([]batchNode, 0, len(req.NodeIDs))
	allowedNodes, err := authorizeNodeOwnershipSet(c, h.db, req.NodeIDs)
	if err != nil {
		respondInternalError(c, err)
		return
	}
	// Batch query to avoid N+1
	var dbNodes []model.Node
	if err := h.db.Select("id", "name").Where("id IN ?", req.NodeIDs).Find(&dbNodes).Error; err != nil {
		respondInternalError(c, fmt.Errorf("查询节点失败: %w", err))
		return
	}
	dbNodeMap := make(map[uint]string, len(dbNodes))
	for _, n := range dbNodes {
		dbNodeMap[n.ID] = n.Name
	}
	for _, nodeID := range req.NodeIDs {
		nodeName, ok := dbNodeMap[nodeID]
		if !ok {
			respondBadRequest(c, fmt.Sprintf("节点 %d 不存在", nodeID))
			return
		}
		if _, ok := allowedNodes[nodeID]; !ok {
			respondForbidden(c, "无权访问该节点")
			return
		}
		nodes = append(nodes, batchNode{ID: nodeID, Name: nodeName})
	}
	if !EnforceStepUp(c, h.db, h.jwtManager, auth.StepUpActionBatchCommandCreate, sshutil.PurposeBatchCommand, "batch_run") {
		return
	}
	grantNodeIDs := make([]uint, 0, len(nodes))
	for _, node := range nodes {
		grantNodeIDs = append(grantNodeIDs, node.ID)
	}
	if !EnforceBatchCommandCredentialGrants(c, h.db, grantNodeIDs) {
		return
	}

	key, validKey := exactIdempotencyKey(c.Request)
	if !validKey {
		respondBadRequest(c, "需要有效的 Idempotency-Key")
		return
	}
	batch, dispatches, created, err := h.createBatchTasks(c.Request.Context(), middleware.CurrentUserID(c), key, req, nodes)
	if errors.Is(err, errBatchIdempotencyConflict) || errors.Is(err, errBatchDeleted) {
		respondConflict(c, "幂等键已用于不同请求或已删除的批次")
		return
	}
	if err != nil {
		respondInternalError(c, err)
		return
	}
	dispatchErr := h.dispatchBatch(c.Request.Context(), batch.ID, dispatches)
	taskIDs := make([]uint, 0, len(dispatches))
	runIDs := make([]uint, 0, len(dispatches))
	successCount, failureCount := 0, 0
	for _, dispatch := range dispatches {
		taskIDs = append(taskIDs, dispatch.TaskID)
		runIDs = append(runIDs, dispatch.RunID)
		if dispatch.Status == "accepted" {
			successCount++
		} else {
			failureCount++
		}
	}
	if created {
		writeCredentialAuditFromGin(c, h.db, credentialaudit.Event{
			Action: "batch_command.create", Purpose: sshutil.PurposeBatchCommand,
			Outcome:  credentialAuditOutcome(successCount, failureCount, 0),
			Metadata: map[string]any{"batch_id": batch.ID, "node_count": len(nodes), "task_count": len(taskIDs), "run_count": successCount, "success_count": successCount, "failure_count": failureCount, "retain": batch.Retain},
		})
	}
	// Creation has committed. Even if dispatch persistence is unavailable,
	// return its identity rather than hiding durable tasks behind an overall 500.
	respondOK(c, gin.H{"batch_id": batch.ID, "task_ids": taskIDs, "run_ids": runIDs, "retain": batch.Retain, "dispatches": dispatches, "dispatch_incomplete": dispatchErr != nil})
}

// Get godoc
// @Summary      获取批次状态
// @Description  查询指定批次的所有任务状态和统计信息
// @Tags         batch
// @Security     Bearer
// @Produce      json
// @Param        batch_id  path      string  true  "批次 ID"
// @Success      200  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Failure      403  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Router       /batch-commands/{batch_id} [get]
func (h *BatchHandler) Get(c *gin.Context) {
	batchID := c.Param("batch_id")
	if batchID == "" {
		respondBadRequest(c, "batch_id 不能为空")
		return
	}

	var tasks []model.Task
	if err := h.db.Preload("Node").Where("batch_id = ?", batchID).Find(&tasks).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	if len(tasks) == 0 {
		respondNotFound(c, "批次不存在")
		return
	}
	nodeIDs := make([]uint, 0, len(tasks))
	seen := make(map[uint]struct{}, len(tasks))
	for _, taskEntity := range tasks {
		if _, ok := seen[taskEntity.NodeID]; ok {
			continue
		}
		seen[taskEntity.NodeID] = struct{}{}
		nodeIDs = append(nodeIDs, taskEntity.NodeID)
	}
	allowedNodes, err := authorizeNodeOwnershipSet(c, h.db, nodeIDs)
	if err != nil {
		respondInternalError(c, err)
		return
	}
	for _, taskEntity := range tasks {
		if _, ok := allowedNodes[taskEntity.NodeID]; !ok {
			respondForbidden(c, "无权访问该节点")
			return
		}
	}

	// 聚合各状态计数
	statusCounts := map[string]int{}
	for _, t := range tasks {
		statusCounts[t.Status]++
	}

	// 清理节点敏感字段
	for i := range tasks {
		tasks[i].Node = tasks[i].Node.Sanitized()
	}

	var dispatches []model.BatchCommandDispatch
	if err := h.db.WithContext(c.Request.Context()).Where("batch_id = ?", batchID).Order("task_id").Find(&dispatches).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	respondOK(c, gin.H{
		"dispatches":    dispatches,
		"batch_id":      batchID,
		"tasks":         tasks,
		"total":         len(tasks),
		"status_counts": statusCounts,
	})
}

// Delete godoc
// @Summary      删除批次
// @Description  删除整个批次的任务及所有关联记录
// @Tags         batch
// @Security     Bearer
// @Produce      json
// @Param        batch_id  path      string  true  "批次 ID"
// @Success      200  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Failure      403  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Router       /batch-commands/{batch_id} [delete]
func (h *BatchHandler) Delete(c *gin.Context) {
	batchID := c.Param("batch_id")
	if batchID == "" {
		respondBadRequest(c, "batch_id 不能为空")
		return
	}

	// 查询该批次下的所有任务 ID
	var taskIDs []uint
	if err := h.db.Model(&model.Task{}).Where("batch_id = ?", batchID).Pluck("id", &taskIDs).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	if len(taskIDs) == 0 {
		respondNotFound(c, "批次不存在")
		return
	}
	var tasks []model.Task
	if err := h.db.Select("id", "node_id").Where("batch_id = ?", batchID).Find(&tasks).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	nodeIDs := make([]uint, 0, len(tasks))
	seen := make(map[uint]struct{}, len(tasks))
	for _, taskEntity := range tasks {
		if _, ok := seen[taskEntity.NodeID]; ok {
			continue
		}
		seen[taskEntity.NodeID] = struct{}{}
		nodeIDs = append(nodeIDs, taskEntity.NodeID)
	}
	allowedNodes, err := authorizeNodeOwnershipSet(c, h.db, nodeIDs)
	if err != nil {
		respondInternalError(c, err)
		return
	}
	for _, taskEntity := range tasks {
		if _, ok := allowedNodes[taskEntity.NodeID]; !ok {
			respondForbidden(c, "无权访问该节点")
			return
		}
	}

	// 事务删除关联记录及任务本身
	var deleted int64
	err = h.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var batch model.BatchCommand
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", batchID).Limit(1).Find(&batch).Error; err != nil {
			return err
		}
		var lockedTaskIDs []uint
		if err := tx.Model(&model.Task{}).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id IN ?", taskIDs).Order("id").Pluck("id", &lockedTaskIDs).Error; err != nil {
			return err
		}
		var active int64
		if err := tx.Model(&model.BatchCommandDispatch{}).Where("batch_id = ? AND status = ?", batchID, "dispatching").Count(&active).Error; err != nil {
			return err
		}
		if active > 0 {
			return errBatchActive
		}
		if err := tx.Model(&model.TaskRun{}).Where("task_id IN ? AND status IN ?", taskIDs, model.TaskRunActiveStatuses()).Count(&active).Error; err != nil {
			return err
		}
		if active > 0 {
			return errBatchActive
		}
		if err := tx.Table("task_run_effects").Joins("JOIN task_runs ON task_runs.id = task_run_effects.task_run_id").Where("task_runs.task_id IN ? AND task_run_effects.status <> ?", taskIDs, "succeeded").Count(&active).Error; err != nil {
			return err
		}
		if active > 0 {
			return errBatchActive
		}
		if err := tx.Model(&model.BatchCommand{}).Where("id = ?", batchID).Update("deleted_at", time.Now().UTC()).Error; err != nil {
			return err
		}
		if err := tx.Where("task_id IN ?", taskIDs).Delete(&model.TaskLog{}).Error; err != nil {
			return err
		}
		if err := tx.Where("task_id IN ?", taskIDs).Delete(&model.TaskRun{}).Error; err != nil {
			return err
		}
		if err := tx.Where("task_id IN ?", taskIDs).Delete(&model.TaskTrafficSample{}).Error; err != nil {
			return err
		}
		if err := tx.Where("task_id IN ?", taskIDs).Delete(&model.Alert{}).Error; err != nil {
			return err
		}

		result := tx.Where("batch_id = ?", batchID).Delete(&model.Task{})
		deleted = result.RowsAffected
		return result.Error
	})
	if errors.Is(err, errBatchActive) {
		respondConflict(c, "批次仍有活动任务、未确认派发或未完成收尾，暂不能删除")
		return
	}
	if err != nil {
		respondInternalError(c, err)
		return
	}

	// 移除调度器中的定时计划
	if h.manager != nil {
		for _, tid := range taskIDs {
			h.manager.RemoveSchedule(tid)
		}
	}

	respondOK(c, gin.H{"deleted": deleted})
}

// generateBatchID uses a collision-resistant identity with no fixed fallback.
func generateBatchID() string { return rand.Text() }

// dangerousPatterns 预编译的危险命令正则表达式。
// 注意：这是安全辅助拦截（safety net），不是安全边界——用户已有 SSH 权限。
var dangerousPatterns = []*regexp.Regexp{
	// rm -rf / 及变体（rm -r /、rm --recursive --force /、带路径 /boot /etc 等关键目录）
	regexp.MustCompile(`(?i)\brm\s+.*-[^\s]*r[^\s]*\s+/(\s|$|boot|etc|usr|var|home|root|sys|proc|dev)`),
	regexp.MustCompile(`(?i)\brm\s+--recursive\b`),
	regexp.MustCompile(`(?i)\bmkfs\b`),
	regexp.MustCompile(`(?i)\bdd\s+.*\bof\s*=\s*/dev/`),
	regexp.MustCompile(`(?i)\bshutdown\b`),
	regexp.MustCompile(`(?i)\breboot\b`),
	regexp.MustCompile(`(?i)\binit\s+0\b`),
	regexp.MustCompile(`(?i)\bhalt\b`),
	regexp.MustCompile(`(?i)\bpoweroff\b`),
	// 管道写入关键设备或清空磁盘
	regexp.MustCompile(`(?i)>\s*/dev/[sh]d`),
	regexp.MustCompile(`(?i)\bwipefs\b`),
}

// isDangerousCommand 判断命令是否匹配危险命令规则。
func isDangerousCommand(cmd string) bool {
	// 检查环境变量中的自定义黑名单
	if blacklist := strings.TrimSpace(os.Getenv("BATCH_COMMAND_BLACKLIST")); blacklist != "" {
		for _, pattern := range strings.Split(blacklist, ",") {
			pattern = strings.TrimSpace(pattern)
			if pattern != "" && strings.Contains(cmd, pattern) {
				return true
			}
		}
	}

	for _, pattern := range dangerousPatterns {
		if pattern.MatchString(cmd) {
			return true
		}
	}
	return false
}
