package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"strings"

	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type BatchTaskRunner interface {
	TriggerManual(taskID uint) (uint, error)
	RemoveSchedule(taskID uint)
}

// BatchHandler 处理批量命令执行相关请求。
type BatchHandler struct {
	db      *gorm.DB
	manager BatchTaskRunner
}

func NewBatchHandler(db *gorm.DB, manager BatchTaskRunner) *BatchHandler {
	return &BatchHandler{db: db, manager: manager}
}

// Create godoc
// @Summary      创建批量命令
// @Description  在多个节点上批量创建并触发命令执行任务
// @Tags         batch
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        body  body      object  true  "批量命令请求"
// @Success      200  {object}  handlers.Response
// @Failure      400  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Failure      403  {object}  handlers.Response
// @Router       /batch-commands [post]
func (h *BatchHandler) Create(c *gin.Context) {
	var req struct {
		NodeIDs []uint `json:"node_ids" binding:"required,min=1"`
		Command string `json:"command" binding:"required"`
		Name    string `json:"name"`
		Retain  *bool  `json:"retain"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, "请求参数错误")
		return
	}

	command := strings.TrimSpace(req.Command)
	if command == "" {
		respondBadRequest(c, "命令不能为空")
		return
	}
	if len(command) > 4096 {
		respondBadRequest(c, "命令长度不能超过 4096 字符")
		return
	}

	// 危险命令拦截
	if isDangerousCommand(command) {
		respondBadRequest(c, "该命令被安全策略拦截，禁止执行")
		return
	}

	batchID := generateBatchID()
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = fmt.Sprintf("批量命令 %s", batchID)
	}

	type batchNode struct {
		ID   uint
		Name string
	}
	nodes := make([]batchNode, 0, len(req.NodeIDs))
	allowedNodes, err := authorizeNodeOwnershipSet(c, h.db, req.NodeIDs)
	if err != nil {
		respondInternalError(c, err)
		return
	}
	for _, nodeID := range req.NodeIDs {
		var node model.Node
		if err := h.db.First(&node, nodeID).Error; err != nil {
			respondBadRequest(c, fmt.Sprintf("节点 %d 不存在", nodeID))
			return
		}
		if _, ok := allowedNodes[nodeID]; !ok {
			respondForbidden(c, "无权访问该节点")
			return
		}
		nodes = append(nodes, batchNode{ID: node.ID, Name: node.Name})
	}

	var taskIDs []uint
	for _, node := range nodes {
		t := model.Task{
			Name:         fmt.Sprintf("%s [%s]", name, node.Name),
			NodeID:       node.ID,
			ExecutorType: "command",
			Command:      command,
			Source:       "batch",
			Status:       "pending",
			BatchID:      batchID,
		}
		if err := h.db.Create(&t).Error; err != nil {
			respondInternalError(c, err)
			return
		}
		taskIDs = append(taskIDs, t.ID)
	}

	// 逐个触发任务执行
	runIDs := make([]uint, 0, len(taskIDs))
	for _, tid := range taskIDs {
		if h.manager == nil {
			runIDs = append(runIDs, 0)
			continue
		}
		runID, err := h.manager.TriggerManual(tid)
		if err != nil {
			// 记录失败但不中断整体流程——任务已创建，仅触发失败
			runIDs = append(runIDs, 0)
			continue
		}
		runIDs = append(runIDs, runID)
	}

	retain := false
	if req.Retain != nil {
		retain = *req.Retain
	}

	respondOK(c, gin.H{
		"batch_id": batchID,
		"task_ids": taskIDs,
		"run_ids":  runIDs,
		"retain":   retain,
	})
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

	respondOK(c, gin.H{
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
	err = h.db.Transaction(func(tx *gorm.DB) error {
		tx.Where("task_id IN ?", taskIDs).Delete(&model.TaskLog{})
		tx.Where("task_id IN ?", taskIDs).Delete(&model.TaskRun{})
		tx.Where("task_id IN ?", taskIDs).Delete(&model.TaskTrafficSample{})
		tx.Where("task_id IN ?", taskIDs).Delete(&model.Alert{})

		result := tx.Where("batch_id = ?", batchID).Delete(&model.Task{})
		deleted = result.RowsAffected
		return result.Error
	})
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

// generateBatchID 生成 8 字符的随机批次 ID。
func generateBatchID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		// 极端情况下回退到固定前缀 + 时间戳
		return fmt.Sprintf("b%d", b[0])
	}
	return hex.EncodeToString(b)
}

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
