package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"

	"xirang/backend/internal/model"
	"xirang/backend/internal/task"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// BatchHandler 处理批量命令执行相关请求。
type BatchHandler struct {
	db      *gorm.DB
	manager *task.Manager
}

func NewBatchHandler(db *gorm.DB, manager *task.Manager) *BatchHandler {
	return &BatchHandler{db: db, manager: manager}
}

// Create 批量创建命令任务并触发执行。
// POST /batch-commands
func (h *BatchHandler) Create(c *gin.Context) {
	var req struct {
		NodeIDs []uint `json:"node_ids" binding:"required,min=1"`
		Command string `json:"command" binding:"required"`
		Name    string `json:"name"`
		Retain  *bool  `json:"retain"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数错误"})
		return
	}

	command := strings.TrimSpace(req.Command)
	if command == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "命令不能为空"})
		return
	}
	if len(command) > 4096 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "命令长度不能超过 4096 字符"})
		return
	}

	// 危险命令拦截
	if isDangerousCommand(command) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "该命令被安全策略拦截，禁止执行"})
		return
	}

	batchID := generateBatchID()
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = fmt.Sprintf("批量命令 %s", batchID)
	}

	var taskIDs []uint
	for _, nodeID := range req.NodeIDs {
		// 校验节点是否存在
		var node model.Node
		if err := h.db.First(&node, nodeID).Error; err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("节点 %d 不存在", nodeID)})
			return
		}

		t := model.Task{
			Name:         fmt.Sprintf("%s [%s]", name, node.Name),
			NodeID:       nodeID,
			ExecutorType: "command",
			Command:      command,
			Source:       "batch",
			Status:       "pending",
			BatchID:      batchID,
		}
		if err := h.db.Create(&t).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "创建任务失败"})
			return
		}
		taskIDs = append(taskIDs, t.ID)
	}

	// 逐个触发任务执行
	runIDs := make([]uint, 0, len(taskIDs))
	for _, tid := range taskIDs {
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

	c.JSON(http.StatusOK, gin.H{
		"batch_id": batchID,
		"task_ids": taskIDs,
		"run_ids":  runIDs,
		"retain":   retain,
	})
}

// Get 查询批次状态。
// GET /batch-commands/:batch_id
func (h *BatchHandler) Get(c *gin.Context) {
	batchID := c.Param("batch_id")
	if batchID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "batch_id 不能为空"})
		return
	}

	var tasks []model.Task
	if err := h.db.Preload("Node").Where("batch_id = ?", batchID).Find(&tasks).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询失败"})
		return
	}
	if len(tasks) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "批次不存在"})
		return
	}

	// 聚合各状态计数
	statusCounts := map[string]int{}
	for _, t := range tasks {
		statusCounts[t.Status]++
	}

	// 清理节点敏感字段
	for i := range tasks {
		tasks[i].Node = sanitizeNode(tasks[i].Node)
	}

	c.JSON(http.StatusOK, gin.H{
		"batch_id":      batchID,
		"tasks":         tasks,
		"total":         len(tasks),
		"status_counts": statusCounts,
	})
}

// Delete 删除整个批次的任务及关联记录。
// DELETE /batch-commands/:batch_id
func (h *BatchHandler) Delete(c *gin.Context) {
	batchID := c.Param("batch_id")
	if batchID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "batch_id 不能为空"})
		return
	}

	// 查询该批次下的所有任务 ID
	var taskIDs []uint
	if err := h.db.Model(&model.Task{}).Where("batch_id = ?", batchID).Pluck("id", &taskIDs).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	if len(taskIDs) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "批次不存在"})
		return
	}

	// 事务删除关联记录及任务本身
	var deleted int64
	err := h.db.Transaction(func(tx *gorm.DB) error {
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
	for _, tid := range taskIDs {
		h.manager.RemoveSchedule(tid)
	}

	c.JSON(http.StatusOK, gin.H{"deleted": deleted})
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
