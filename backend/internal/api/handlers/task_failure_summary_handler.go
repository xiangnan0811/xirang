package handlers

import (
	"context"
	"errors"
	"time"

	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// TaskFailureSummaryHandler serves the fixed 24-hour retained task-run failure summary.
type TaskFailureSummaryHandler struct {
	db    *gorm.DB
	nowFn func() time.Time
}

func NewTaskFailureSummaryHandler(db *gorm.DB, nowFn func() time.Time) *TaskFailureSummaryHandler {
	if nowFn == nil {
		nowFn = time.Now
	}
	return &TaskFailureSummaryHandler{db: db, nowFn: nowFn}
}

type taskFailureSummaryResponse struct {
	FailedTasks int64 `json:"failed_tasks"`
	WindowHours int   `json:"window_hours"`
}

// Get returns the number of distinct tasks with a retained failed run finished
// in the fixed half-open UTC window [now-24h, now). Admins and viewers see all
// retained history, including legacy_unknown node snapshots. Operators see
// only runs whose immutable node snapshot is in their current owned node set.
// Retained history is intentionally queried directly without joining tasks.
//
// @Summary      查询通知页过去 24 小时失败任务摘要
// @Description  固定统计服务端 UTC 最近 24 小时内仍保留的失败 TaskRun，按 task_id 去重；admin/viewer 查看全局历史，operator 按运行时节点快照与当前 owned 节点相交，legacy_unknown 仅 admin/viewer 计入。清理或显式删除历史可能减少结果。
// @Tags         tasks
// @Produce      json
// @Security     Bearer
// @Success      200 {object} handlers.Response{data=handlers.taskFailureSummaryResponse}
// @Failure      401 {object} handlers.Response
// @Failure      403 {object} handlers.Response
// @Failure      500 {object} handlers.Response
// @Router       /tasks/failure-summary [get]
func (h *TaskFailureSummaryHandler) Get(c *gin.Context) {
	// This result is scoped by the authenticated user's role and owned nodes.
	// Never allow a shared cache to reuse it for another user.
	c.Header("Cache-Control", "private, no-store")

	db := h.db.WithContext(c.Request.Context())
	nodeIDs, needFilter, err := ownershipNodeFilter(c, db)
	if err != nil {
		respondTaskFailureSummaryError(c, err)
		return
	}

	response := taskFailureSummaryResponse{WindowHours: 24}
	end := h.nowFn().UTC()
	start := end.Add(-24 * time.Hour)
	query := db.Model(&model.TaskRun{}).
		Where("status = ? AND finished_at IS NOT NULL AND finished_at >= ? AND finished_at < ?",
			model.TaskRunStatusFailed, start, end)
	if needFilter && len(nodeIDs) == 0 {
		respondOK(c, response)
		return
	}
	if needFilter {
		query = query.Where("node_id_snapshot > 0 AND node_id_snapshot IN ?", nodeIDs)
	}
	if err := query.Distinct("task_id").Count(&response.FailedTasks).Error; err != nil {
		respondTaskFailureSummaryError(c, err)
		return
	}

	respondOK(c, response)
}

func respondTaskFailureSummaryError(c *gin.Context, err error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		c.AbortWithStatus(499)
		return
	}
	respondInternalError(c, err)
}
