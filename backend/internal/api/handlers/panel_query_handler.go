package handlers

import (
	"context"
	"errors"
	"time"

	"xirang/backend/internal/dashboards"
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type PanelQueryHandler struct {
	db *gorm.DB
}

func NewPanelQueryHandler(db *gorm.DB) *PanelQueryHandler {
	return &PanelQueryHandler{db: db}
}

type panelQueryPayload struct {
	Metric      string             `json:"metric"`
	Filters     dashboards.Filters `json:"filters"`
	Aggregation string             `json:"aggregation"`
	Start       time.Time          `json:"start"`
	End         time.Time          `json:"end"`
}

func (h *PanelQueryHandler) Query(c *gin.Context) {
	var req panelQueryPayload
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, err.Error())
		return
	}

	filters, ownershipScoped, ownershipNodeIDs, denied, err := h.applyPanelQueryOwnership(c, req.Metric, req.Filters)
	if err != nil {
		respondInternalError(c, err)
		return
	}
	if denied {
		respondForbidden(c, "无权查询未授权节点的指标")
		return
	}
	req.Filters = filters

	resp, err := dashboards.Query(c.Request.Context(), h.db, dashboards.QueryRequest{
		Metric: req.Metric, Filters: req.Filters, Aggregation: req.Aggregation,
		Start: req.Start.UTC(), End: req.End.UTC(),
		OwnershipScoped:  ownershipScoped,
		OwnershipNodeIDs: ownershipNodeIDs,
	})
	if err != nil {
		switch {
		case errors.Is(err, dashboards.ErrInvalidMetric),
			errors.Is(err, dashboards.ErrInvalidAggregation),
			errors.Is(err, dashboards.ErrInvalidFilters),
			errors.Is(err, dashboards.ErrInvalidTimeRange):
			respondBadRequest(c, err.Error())
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			c.AbortWithStatus(499)
		default:
			respondInternalError(c, err)
		}
		return
	}
	respondOK(c, resp)
}

// applyPanelQueryOwnership constrains panel-query filters for operators.
//
// Rules (admin/viewer unchanged):
//   - node metrics: empty node_ids → owned set; any non-owned id → forbidden
//   - task metrics: empty task_ids keeps aggregate semantics and scopes via
//     OwnershipNodeIDs subquery (never expands all task IDs into IN lists);
//     explicit task_ids must all belong to owned nodes or → forbidden
func (h *PanelQueryHandler) applyPanelQueryOwnership(c *gin.Context, metric string, filters dashboards.Filters) (dashboards.Filters, bool, []uint, bool, error) {
	ownedIDs, needFilter, err := ownershipNodeFilter(c, h.db)
	if err != nil {
		return filters, false, nil, false, err
	}
	if !needFilter {
		return filters, false, nil, false, nil
	}

	desc := dashboards.DescribeMetric(metric)
	if desc == nil {
		return filters, false, nil, false, nil
	}

	ownedSet := make(map[uint]struct{}, len(ownedIDs))
	for _, id := range ownedIDs {
		ownedSet[id] = struct{}{}
	}

	switch desc.Family {
	case dashboards.FamilyNode:
		if len(filters.NodeIDs) == 0 {
			filters.NodeIDs = append([]uint(nil), ownedIDs...)
			if len(filters.NodeIDs) == 0 {
				filters.NodeIDs = []uint{0}
			}
			return filters, false, nil, false, nil
		}
		for _, id := range filters.NodeIDs {
			if _, ok := ownedSet[id]; !ok {
				return filters, false, nil, true, nil
			}
		}
		return filters, false, nil, false, nil

	case dashboards.FamilyTask:
		// Empty task_ids: keep aggregate semantics; scope via OwnershipNodeIDs.
		if len(filters.TaskIDs) == 0 {
			return filters, true, append([]uint(nil), ownedIDs...), false, nil
		}
		var tasks []model.Task
		if err := h.db.Select("id", "node_id").
			Where("id IN ?", filters.TaskIDs).
			Find(&tasks).Error; err != nil {
			return filters, false, nil, false, err
		}
		found := make(map[uint]uint, len(tasks))
		for _, t := range tasks {
			found[t.ID] = t.NodeID
		}
		for _, id := range filters.TaskIDs {
			nodeID, ok := found[id]
			if !ok {
				return filters, false, nil, true, nil
			}
			if _, ok := ownedSet[nodeID]; !ok {
				return filters, false, nil, true, nil
			}
		}
		// Explicit task list already ownership-checked; no extra scope needed.
		return filters, false, nil, false, nil
	}

	return filters, false, nil, false, nil
}

func (h *PanelQueryHandler) ListMetrics(c *gin.Context) {
	respondOK(c, dashboards.ListMetrics())
}
