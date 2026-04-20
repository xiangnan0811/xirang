package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"xirang/backend/internal/logger"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"
	"xirang/backend/internal/slo"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type SLOHandler struct {
	db *gorm.DB
}

func NewSLOHandler(db *gorm.DB) *SLOHandler { return &SLOHandler{db: db} }

type sloCreateRequest struct {
	Name       string   `json:"name" binding:"required"`
	MetricType string   `json:"metric_type" binding:"required"`
	MatchTags  []string `json:"match_tags"`
	Threshold  float64  `json:"threshold" binding:"required"`
	WindowDays int      `json:"window_days"`
	Enabled    bool     `json:"enabled"`
}

func (req *sloCreateRequest) validate() error {
	if len(req.Name) == 0 || len(req.Name) > 128 {
		return errors.New("name must be 1-128 characters")
	}
	switch req.MetricType {
	case "availability", "success_rate":
	default:
		return errors.New("metric_type must be availability or success_rate")
	}
	if req.Threshold <= 0 || req.Threshold >= 1 {
		return errors.New("threshold must be in (0, 1)")
	}
	if req.WindowDays < 0 || req.WindowDays > 365 {
		return errors.New("window_days must be 0-365")
	}
	if req.WindowDays == 0 {
		req.WindowDays = 28
	}
	return nil
}

// List 列出所有 SLO 定义（alerts:read）。
func (h *SLOHandler) List(c *gin.Context) {
	var rows []model.SLODefinition
	if err := h.db.Order("id ASC").Find(&rows).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	respondOK(c, rows)
}

// Get 获取单条 SLO 定义（alerts:read）。
func (h *SLOHandler) Get(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var s model.SLODefinition
	if err := h.db.First(&s, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			respondNotFound(c, "SLO 定义不存在")
			return
		}
		respondInternalError(c, err)
		return
	}
	respondOK(c, s)
}

// Create 创建 SLO 定义（admin only）。
func (h *SLOHandler) Create(c *gin.Context) {
	var req sloCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, err.Error())
		return
	}
	if err := req.validate(); err != nil {
		respondBadRequest(c, err.Error())
		return
	}
	tagsJSON, _ := json.Marshal(req.MatchTags)
	uid := middleware.CurrentUserID(c)
	s := model.SLODefinition{
		Name:       req.Name,
		MetricType: req.MetricType,
		MatchTags:  string(tagsJSON),
		Threshold:  req.Threshold,
		WindowDays: req.WindowDays,
		Enabled:    req.Enabled,
		CreatedBy:  uid,
	}
	if err := h.db.Create(&s).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	respondCreated(c, s)
}

// Update 更新 SLO 定义（admin only）。
func (h *SLOHandler) Update(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var existing model.SLODefinition
	if err := h.db.First(&existing, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			respondNotFound(c, "SLO 定义不存在")
			return
		}
		respondInternalError(c, err)
		return
	}
	var req sloCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, err.Error())
		return
	}
	if err := req.validate(); err != nil {
		respondBadRequest(c, err.Error())
		return
	}
	tagsJSON, _ := json.Marshal(req.MatchTags)
	updates := map[string]any{
		"name":        req.Name,
		"metric_type": req.MetricType,
		"match_tags":  string(tagsJSON),
		"threshold":   req.Threshold,
		"window_days": req.WindowDays,
		"enabled":     req.Enabled,
	}
	if err := h.db.Model(&model.SLODefinition{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	var updated model.SLODefinition
	if err := h.db.First(&updated, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			respondNotFound(c, "SLO 定义不存在")
			return
		}
		respondInternalError(c, err)
		return
	}
	respondOK(c, updated)
}

// Delete 硬删除 SLO 定义（admin only）。
func (h *SLOHandler) Delete(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	res := h.db.Delete(&model.SLODefinition{}, id)
	if res.Error != nil {
		respondInternalError(c, res.Error)
		return
	}
	if res.RowsAffected == 0 {
		respondNotFound(c, "SLO 定义不存在")
		return
	}
	slo.ClearPromMetricsForSLO(uint(id))
	c.Status(http.StatusNoContent)
}

// Compliance 计算单条 SLO 的合规状态（alerts:read）。
func (h *SLOHandler) Compliance(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var s model.SLODefinition
	if err := h.db.First(&s, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			respondNotFound(c, "SLO 定义不存在")
			return
		}
		respondInternalError(c, err)
		return
	}
	result, err := slo.Compute(h.db, &s, time.Now().UTC())
	if err != nil {
		respondInternalError(c, err)
		return
	}
	respondOK(c, result)
}

type sloSummaryResponse struct {
	Total        int `json:"total"`
	Healthy      int `json:"healthy"`
	Warning      int `json:"warning"`
	Breached     int `json:"breached"`
	Insufficient int `json:"insufficient"`
}

// ComplianceSummary 汇总所有已启用 SLO 的合规状态计数（alerts:read）。
func (h *SLOHandler) ComplianceSummary(c *gin.Context) {
	var defs []model.SLODefinition
	if err := h.db.Where("enabled = ?", true).Find(&defs).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	now := time.Now().UTC()
	summary := sloSummaryResponse{}
	for i := range defs {
		result, err := slo.Compute(h.db, &defs[i], now)
		if err != nil {
			logger.Module("slo").Warn().
				Uint("slo_id", defs[i].ID).
				Err(err).
				Msg("SLO compliance summary: compute failed, row skipped")
			continue
		}
		switch result.Status {
		case slo.StatusHealthy:
			summary.Total++
			summary.Healthy++
		case slo.StatusWarning:
			summary.Total++
			summary.Warning++
		case slo.StatusBreached:
			summary.Total++
			summary.Breached++
		case slo.StatusInsufficient:
			summary.Insufficient++
		}
	}
	respondOK(c, summary)
}
