package handlers

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"
	"xirang/backend/internal/reporting"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

var validScopeTypes = map[string]bool{"all": true, "tag": true, "node_ids": true}
var validPeriods = map[string]bool{"weekly": true, "monthly": true}

func validateReportConfigRequest(req *reportConfigRequest) string {
	scopeType := req.ScopeType
	if scopeType == "" {
		scopeType = "all"
	}
	if !validScopeTypes[scopeType] {
		return "scope_type 必须为 all、tag 或 node_ids"
	}
	period := req.Period
	if period == "" {
		period = "weekly"
	}
	if !validPeriods[period] {
		return "period 必须为 weekly 或 monthly"
	}
	parts := strings.Fields(req.Cron)
	if len(parts) != 5 {
		return "cron 格式无效，须为 5 段标准 cron 表达式（分 时 日 月 周）"
	}
	return ""
}

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
	if ownedIDs, needFilter := h.operatorOwnedNodeIDs(c); needFilter {
		configs = filterConfigsByOwnedNodes(configs, ownedIDs)
	}
	c.JSON(http.StatusOK, gin.H{"data": configs})
}

func (h *ReportHandler) CreateConfig(c *gin.Context) {
	var req reportConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if msg := validateReportConfigRequest(&req); msg != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": msg})
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
	c.JSON(http.StatusCreated, gin.H{"data": cfg})
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
	if msg := validateReportConfigRequest(&req); msg != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": msg})
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
	var cfg model.ReportConfig
	if err := h.db.First(&cfg, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "报告配置不存在"})
		return
	}
	if !h.checkConfigOwnership(c, cfg) {
		c.JSON(http.StatusForbidden, gin.H{"error": "无权访问该报告配置"})
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
	role := middleware.CurrentRole(c)
	if role != "admin" && role != "viewer" {
		if report.Config == nil || report.Config.ID == 0 {
			c.JSON(http.StatusForbidden, gin.H{"error": "无权访问该报告"})
			return
		}
		if !h.checkConfigOwnership(c, *report.Config) {
			c.JSON(http.StatusForbidden, gin.H{"error": "无权访问该报告"})
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"data": report})
}

// operatorOwnedNodeIDs 返回 operator 拥有的节点 ID 集合。
// admin/viewer 返回 nil, false（无需过滤）。
func (h *ReportHandler) operatorOwnedNodeIDs(c *gin.Context) (map[uint]struct{}, bool) {
	role := middleware.CurrentRole(c)
	if role == "admin" || role == "viewer" {
		return nil, false
	}
	userID := middleware.CurrentUserID(c)
	ids, err := middleware.OwnedNodeIDs(h.db, userID)
	if err != nil || len(ids) == 0 {
		return map[uint]struct{}{}, true
	}
	set := make(map[uint]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return set, true
}

// checkConfigOwnership 检查 operator 是否有权访问某报告配置。
func (h *ReportHandler) checkConfigOwnership(c *gin.Context, cfg model.ReportConfig) bool {
	role := middleware.CurrentRole(c)
	if role == "admin" || role == "viewer" {
		return true
	}
	// operator 仅可访问 scope_type=node_ids 且与自身节点有交集的配置
	if cfg.ScopeType != "node_ids" {
		return false
	}
	ownedIDs, needFilter := h.operatorOwnedNodeIDs(c)
	if !needFilter {
		return true
	}
	return configOverlapsOwnedNodes(cfg, ownedIDs)
}

// filterConfigsByOwnedNodes 过滤出与 operator 节点有交集的报告配置。
func filterConfigsByOwnedNodes(configs []model.ReportConfig, ownedIDs map[uint]struct{}) []model.ReportConfig {
	result := make([]model.ReportConfig, 0, len(configs))
	for _, cfg := range configs {
		if cfg.ScopeType != "node_ids" {
			continue
		}
		if configOverlapsOwnedNodes(cfg, ownedIDs) {
			result = append(result, cfg)
		}
	}
	return result
}

func configOverlapsOwnedNodes(cfg model.ReportConfig, ownedIDs map[uint]struct{}) bool {
	var nodeIDs []uint
	if err := json.Unmarshal([]byte(cfg.ScopeValue), &nodeIDs); err != nil || len(nodeIDs) == 0 {
		return false
	}
	for _, nid := range nodeIDs {
		if _, ok := ownedIDs[nid]; ok {
			return true
		}
	}
	return false
}
