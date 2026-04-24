package handlers

import (
	"strconv"
	"strings"

	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type AnomalyHandler struct{ db *gorm.DB }

func NewAnomalyHandler(db *gorm.DB) *AnomalyHandler {
	return &AnomalyHandler{db: db}
}

type anomalyListResponse struct {
	Data    []model.AnomalyEvent `json:"data"`
	Total   int64                `json:"total"`
	HasMore bool                 `json:"has_more"`
}

// List returns paginated events with optional filters. For operator role the
// result is constrained to nodes the user owns (admin/viewer see all).
func (h *AnomalyHandler) List(c *gin.Context) {
	ownedIDs, needOwnerFilter, err := ownershipNodeFilter(c, h.db)
	if err != nil {
		respondInternalError(c, err)
		return
	}
	if needOwnerFilter && len(ownedIDs) == 0 {
		respondOK(c, anomalyListResponse{Data: []model.AnomalyEvent{}, Total: 0, HasMore: false})
		return
	}
	q := h.db.Model(&model.AnomalyEvent{})
	if needOwnerFilter {
		q = q.Where("node_id IN ?", ownedIDs)
	}
	if v := c.Query("detector"); v != "" {
		switch v {
		case "ewma", "disk_forecast":
		default:
			respondBadRequest(c, "detector: 仅支持 ewma / disk_forecast")
			return
		}
		q = q.Where("detector = ?", v)
	}
	if v := c.Query("metric"); v != "" {
		q = q.Where("metric = ?", v)
	}
	if v := c.Query("severity"); v != "" {
		switch v {
		case "warning", "critical":
		default:
			respondBadRequest(c, "severity: 仅支持 warning / critical")
			return
		}
		q = q.Where("severity = ?", v)
	}
	if v := c.Query("node_id"); v != "" {
		if id, err := strconv.ParseUint(v, 10, 64); err == nil && id > 0 {
			q = q.Where("node_id = ?", id)
		}
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "50"))
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > 500 {
		pageSize = 500
	}
	var total int64
	q.Count(&total)
	var rows []model.AnomalyEvent
	if err := q.Order("fired_at DESC").
		Limit(pageSize).Offset((page - 1) * pageSize).
		Find(&rows).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	respondOK(c, anomalyListResponse{
		Data:    rows,
		Total:   total,
		HasMore: int64(page*pageSize) < total,
	})
}

// ListForNode returns events for a specific node (ownership-enforced).
func (h *AnomalyHandler) ListForNode(c *gin.Context) {
	nodeID, ok := parseID(c, "id")
	if !ok {
		return
	}
	// Verify node exists (404 for missing node)
	var n model.Node
	if err := h.db.Select("id").First(&n, nodeID).Error; err != nil {
		respondNotFound(c, "节点不存在")
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	// Optional filters
	q := h.db.Model(&model.AnomalyEvent{}).Where("node_id = ?", nodeID)
	if v := strings.TrimSpace(c.Query("detector")); v != "" {
		q = q.Where("detector = ?", v)
	}
	var rows []model.AnomalyEvent
	if err := q.Order("fired_at DESC").Limit(limit).Find(&rows).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	respondOK(c, rows)
}
