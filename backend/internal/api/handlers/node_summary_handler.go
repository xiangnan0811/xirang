package handlers

import (
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// NodeSummaryHandler serves the business counters needed by the node detail
// page. Resource samples and probe state intentionally do not belong here.
type NodeSummaryHandler struct {
	db *gorm.DB
}

func NewNodeSummaryHandler(db *gorm.DB) *NodeSummaryHandler {
	return &NodeSummaryHandler{db: db}
}

type nodeSummaryResponse struct {
	OpenAlerts   int64 `json:"open_alerts"`
	RunningTasks int64 `json:"running_tasks"`
}

// Get returns open alerts and authoritative running task runs for one node.
// The route owns authentication, nodes:read RBAC, and node ownership checks.
// TaskRun node snapshots are used instead of the mutable Task.node_id so the
// count remains tied to the run's immutable execution identity.
//
// @Summary      获取节点业务摘要
// @Description  返回节点的未解决告警和运行中任务数量，不包含资源探测数据
// @Tags         nodes
// @Security     Bearer
// @Produce      json
// @Param        id   path      int  true  "节点 ID"
// @Success      200  {object}  handlers.Response{data=nodeSummaryResponse}
// @Failure      400  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Failure      403  {object}  handlers.Response
// @Failure      500  {object}  handlers.Response
// @Router       /nodes/{id}/summary [get]
func (h *NodeSummaryHandler) Get(c *gin.Context) {
	nodeID, ok := parseID(c, "id")
	if !ok {
		return
	}

	var response nodeSummaryResponse
	db := h.db.WithContext(c.Request.Context())
	if err := db.Model(&model.Alert{}).
		Where("node_id = ? AND status = ?", nodeID, "open").
		Count(&response.OpenAlerts).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	if err := db.Table("task_runs").
		Where("node_id_snapshot = ? AND node_id_snapshot > ? AND status = ?",
			nodeID, model.TaskRunNodeIDLegacyUnknown, model.TaskRunStatusRunning).
		Count(&response.RunningTasks).Error; err != nil {
		respondInternalError(c, err)
		return
	}

	c.Header("Cache-Control", "private, no-store")
	respondOK(c, response)
}
