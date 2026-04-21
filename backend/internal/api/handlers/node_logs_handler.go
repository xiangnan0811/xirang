package handlers

import (
	"strconv"
	"strings"
	"time"

	"xirang/backend/internal/model"
	"xirang/backend/internal/settings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type NodeLogsHandler struct {
	db       *gorm.DB
	settings *settings.Service
}

func NewNodeLogsHandler(db *gorm.DB, s *settings.Service) *NodeLogsHandler {
	return &NodeLogsHandler{db: db, settings: s}
}

type nodeLogsResponse struct {
	Data    []model.NodeLog `json:"data"`
	Total   int64           `json:"total"`
	HasMore bool            `json:"has_more"`
}

func (h *NodeLogsHandler) Query(c *gin.Context) {
	q := h.db.Model(&model.NodeLog{})

	if s := c.Query("node_ids"); s != "" {
		q = q.Where("node_id IN ?", splitInts(s))
	}
	if s := c.Query("source"); s != "" {
		q = q.Where("source IN ?", strings.Split(s, ","))
	}
	if s := c.Query("path"); s != "" {
		q = q.Where("path = ?", s)
	}
	if s := c.Query("priority"); s != "" {
		q = q.Where("priority IN ?", strings.Split(s, ","))
	}

	start, end := parseLogWindow(c.Query("start"), c.Query("end"))
	q = q.Where("timestamp >= ? AND timestamp < ?", start, end)

	if kw := c.Query("q"); kw != "" {
		if strings.HasPrefix(kw, "!") {
			q = q.Where("message NOT LIKE ?", "%"+kw[1:]+"%")
		} else {
			q = q.Where("message LIKE ?", "%"+kw+"%")
		}
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "200"))
	if pageSize <= 0 {
		pageSize = 200
	}
	if pageSize > 500 {
		pageSize = 500
	}

	var total int64
	q.Count(&total)
	var rows []model.NodeLog
	if err := q.Order("timestamp DESC").Limit(pageSize).Offset((page - 1) * pageSize).Find(&rows).Error; err != nil {
		respondInternalError(c, err)
		return
	}

	respondOK(c, nodeLogsResponse{
		Data:    rows,
		Total:   total,
		HasMore: int64(page*pageSize) < total,
	})
}

type alertLogsResponse struct {
	Data        []model.NodeLog `json:"data"`
	NodeID      uint            `json:"node_id"`
	WindowStart time.Time       `json:"window_start"`
	WindowEnd   time.Time       `json:"window_end"`
	Hint        string          `json:"hint,omitempty"`
	HasMore     bool            `json:"has_more"`
}

const alertLogsMaxRows = 500

func (h *NodeLogsHandler) AlertLogs(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var alert model.Alert
	if err := h.db.First(&alert, id).Error; err != nil {
		respondNotFound(c, "告警不存在")
		return
	}
	resp := alertLogsResponse{
		NodeID:      alert.NodeID,
		WindowStart: alert.TriggeredAt.Add(-5 * time.Minute),
		WindowEnd:   alert.TriggeredAt.Add(5 * time.Minute),
	}
	if alert.NodeID == 0 {
		resp.Hint = "平台级告警无关联节点，请切换到节点日志页按时间范围查询"
		resp.Data = []model.NodeLog{}
		respondOK(c, resp)
		return
	}
	var rows []model.NodeLog
	if err := h.db.Where("node_id = ? AND timestamp >= ? AND timestamp < ?",
		alert.NodeID, resp.WindowStart, resp.WindowEnd).
		Order("timestamp DESC").Limit(alertLogsMaxRows + 1).Find(&rows).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	if len(rows) > alertLogsMaxRows {
		resp.HasMore = true
		rows = rows[:alertLogsMaxRows]
	}
	resp.Data = rows
	respondOK(c, resp)
}

type logsSettingsResponse struct {
	DefaultRetentionDays int `json:"default_retention_days"`
}

type logsSettingsRequest struct {
	DefaultRetentionDays int `json:"default_retention_days"`
}

const logsRetentionKey = "logs.retention_days_default"

func (h *NodeLogsHandler) GetSettings(c *gin.Context) {
	v := h.settings.GetEffective(logsRetentionKey)
	n, _ := strconv.Atoi(v)
	if n <= 0 {
		n = 30
	}
	respondOK(c, logsSettingsResponse{DefaultRetentionDays: n})
}

func (h *NodeLogsHandler) PatchSettings(c *gin.Context) {
	var req logsSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, err.Error())
		return
	}
	if req.DefaultRetentionDays < 1 || req.DefaultRetentionDays > 365 {
		respondBadRequest(c, "default_retention_days 必须 1-365")
		return
	}
	if err := h.settings.Update(logsRetentionKey, strconv.Itoa(req.DefaultRetentionDays)); err != nil {
		respondInternalError(c, err)
		return
	}
	respondOK(c, logsSettingsResponse(req))
}

// Helpers

func splitInts(s string) []int {
	parts := strings.Split(s, ",")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err == nil {
			out = append(out, n)
		}
	}
	return out
}

func parseLogWindow(startStr, endStr string) (time.Time, time.Time) {
	now := time.Now().UTC()
	end, err := time.Parse(time.RFC3339, endStr)
	if err != nil || endStr == "" {
		end = now
	}
	start, err := time.Parse(time.RFC3339, startStr)
	if err != nil || startStr == "" {
		start = end.Add(-time.Hour)
	}
	if start.After(end) {
		start = end.Add(-time.Hour)
	}
	return start, end
}
