package handlers

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"time"

	"xirang/backend/internal/model"
	"xirang/backend/internal/reporting"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type ReportHandler struct {
	db *gorm.DB
}

func NewReportHandler(db *gorm.DB) *ReportHandler {
	return &ReportHandler{db: db}
}

type reportConfigRequest struct {
	Name           string `json:"name" binding:"required"`
	ScopeType      string `json:"scope_type"`
	ScopeValue     string `json:"scope_value"`
	Period         string `json:"period"`
	Cron           string `json:"cron" binding:"required"`
	IntegrationIDs []uint `json:"integration_ids"`
	Enabled        *bool  `json:"enabled"`
}

func (h *ReportHandler) ListConfigs(c *gin.Context) {
	var configs []model.ReportConfig
	if err := h.db.Order("id asc").Find(&configs).Error; err != nil {
		log.Printf("报告配置查询失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "服务器内部错误"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": configs})
}

func (h *ReportHandler) CreateConfig(c *gin.Context) {
	var req reportConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	scopeType := req.ScopeType
	if scopeType == "" {
		scopeType = "all"
	}
	period := req.Period
	if period == "" {
		period = "weekly"
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	integrationIDsJSON, _ := json.Marshal(req.IntegrationIDs)

	cfg := model.ReportConfig{
		Name:           req.Name,
		ScopeType:      scopeType,
		ScopeValue:     req.ScopeValue,
		Period:         period,
		Cron:           req.Cron,
		IntegrationIDs: string(integrationIDsJSON),
		Enabled:        enabled,
	}
	if err := h.db.Create(&cfg).Error; err != nil {
		log.Printf("报告配置创建失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "服务器内部错误"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": cfg})
}

func (h *ReportHandler) UpdateConfig(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
		return
	}
	var cfg model.ReportConfig
	if err := h.db.First(&cfg, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "报告配置不存在"})
		return
	}

	var req reportConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	integrationIDsJSON, _ := json.Marshal(req.IntegrationIDs)
	cfg.Name = req.Name
	if req.ScopeType != "" {
		cfg.ScopeType = req.ScopeType
	}
	cfg.ScopeValue = req.ScopeValue
	if req.Period != "" {
		cfg.Period = req.Period
	}
	cfg.Cron = req.Cron
	cfg.IntegrationIDs = string(integrationIDsJSON)
	if req.Enabled != nil {
		cfg.Enabled = *req.Enabled
	}

	if err := h.db.Save(&cfg).Error; err != nil {
		log.Printf("报告配置更新失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "服务器内部错误"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": cfg})
}

func (h *ReportHandler) DeleteConfig(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
		return
	}
	if err := h.db.Delete(&model.ReportConfig{}, id).Error; err != nil {
		log.Printf("报告配置删除失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "服务器内部错误"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

// GenerateNow 立即为指定配置生成一份报告（手动触发）。
func (h *ReportHandler) GenerateNow(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
		return
	}
	var cfg model.ReportConfig
	if err := h.db.First(&cfg, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "报告配置不存在"})
		return
	}

	now := time.Now()
	var start time.Time
	if cfg.Period == "monthly" {
		start = now.AddDate(0, -1, 0)
	} else {
		start = now.AddDate(0, 0, -7)
	}

	report, err := reporting.Generate(h.db, cfg, start, now)
	if err != nil {
		log.Printf("报告生成失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "报告生成失败: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": report})
}

// ListReports 列出某配置下已生成的报告。
func (h *ReportHandler) ListReports(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
		return
	}
	var reports []model.Report
	if err := h.db.Where("config_id = ?", id).Order("period_start desc").Limit(50).Find(&reports).Error; err != nil {
		log.Printf("报告查询失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "服务器内部错误"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": reports})
}

// GetReport 获取单份报告详情。
func (h *ReportHandler) GetReport(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
		return
	}
	var report model.Report
	if err := h.db.Preload("Config").First(&report, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "报告不存在"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": report})
}
