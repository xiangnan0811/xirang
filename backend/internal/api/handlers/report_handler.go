package handlers

import (
	"encoding/json"
	"fmt"
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

// ListConfigs godoc
// @Summary      列出报告配置
// @Description  返回所有 SLA 报告配置（operator 仅返回关联自身节点的配置）
// @Tags         reports
// @Security     Bearer
// @Produce      json
// @Success      200  {object}  handlers.Response{data=[]model.ReportConfig}
// @Failure      401  {object}  handlers.Response
// @Router       /report-configs [get]
func (h *ReportHandler) ListConfigs(c *gin.Context) {
	var configs []model.ReportConfig
	if err := h.db.Order("id asc").Find(&configs).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	if ownedIDs, needFilter := h.operatorOwnedNodeIDs(c); needFilter {
		configs = filterConfigsByOwnedNodes(configs, ownedIDs)
	}
	respondOK(c, configs)
}

// CreateConfig godoc
// @Summary      创建报告配置
// @Description  创建新的 SLA 报告配置
// @Tags         reports
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        body  body      object  true  "报告配置"
// @Success      201  {object}  handlers.Response{data=model.ReportConfig}
// @Failure      400  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Router       /report-configs [post]
func (h *ReportHandler) CreateConfig(c *gin.Context) {
	var req reportConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, err.Error())
		return
	}
	if msg := validateReportConfigRequest(&req); msg != "" {
		respondBadRequest(c, msg)
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
		respondInternalError(c, err)
		return
	}
	respondCreated(c, cfg)
}

// UpdateConfig godoc
// @Summary      更新报告配置
// @Description  更新指定 SLA 报告配置
// @Tags         reports
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        id    path      int     true  "配置 ID"
// @Param        body  body      object  true  "报告配置"
// @Success      200  {object}  handlers.Response{data=model.ReportConfig}
// @Failure      400  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Router       /report-configs/{id} [put]
func (h *ReportHandler) UpdateConfig(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		respondBadRequest(c, "无效的 ID")
		return
	}
	var cfg model.ReportConfig
	if err := h.db.First(&cfg, id).Error; err != nil {
		respondNotFound(c, "报告配置不存在")
		return
	}

	var req reportConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, err.Error())
		return
	}
	if msg := validateReportConfigRequest(&req); msg != "" {
		respondBadRequest(c, msg)
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
		respondInternalError(c, err)
		return
	}
	respondOK(c, cfg)
}

// DeleteConfig godoc
// @Summary      删除报告配置
// @Description  删除指定 SLA 报告配置
// @Tags         reports
// @Security     Bearer
// @Produce      json
// @Param        id  path      int  true  "配置 ID"
// @Success      200  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Router       /report-configs/{id} [delete]
func (h *ReportHandler) DeleteConfig(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		respondBadRequest(c, "无效的 ID")
		return
	}
	if err := h.db.Delete(&model.ReportConfig{}, id).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	respondMessage(c, "已删除")
}

// GenerateNow godoc
// @Summary      立即生成报告
// @Description  立即为指定报告配置手动触发生成一份 SLA 报告
// @Tags         reports
// @Security     Bearer
// @Produce      json
// @Param        id  path      int  true  "配置 ID"
// @Success      200  {object}  handlers.Response{data=model.Report}
// @Failure      401  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Router       /report-configs/{id}/generate [post]
func (h *ReportHandler) GenerateNow(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		respondBadRequest(c, "无效的 ID")
		return
	}
	var cfg model.ReportConfig
	if err := h.db.First(&cfg, id).Error; err != nil {
		respondNotFound(c, "报告配置不存在")
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
		respondInternalError(c, fmt.Errorf("报告生成失败: %w", err))
		return
	}
	respondOK(c, report)
}

// ListReports godoc
// @Summary      列出报告
// @Description  列出指定报告配置下已生成的报告（最多 50 条）
// @Tags         reports
// @Security     Bearer
// @Produce      json
// @Param        id  path      int  true  "配置 ID"
// @Success      200  {object}  handlers.Response{data=[]model.Report}
// @Failure      401  {object}  handlers.Response
// @Failure      403  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Router       /report-configs/{id}/reports [get]
func (h *ReportHandler) ListReports(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		respondBadRequest(c, "无效的 ID")
		return
	}
	var cfg model.ReportConfig
	if err := h.db.First(&cfg, id).Error; err != nil {
		respondNotFound(c, "报告配置不存在")
		return
	}
	if !h.checkConfigOwnership(c, cfg) {
		respondForbidden(c, "无权访问该报告配置")
		return
	}
	var reports []model.Report
	if err := h.db.Where("config_id = ?", id).Order("period_start desc").Limit(50).Find(&reports).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	respondOK(c, reports)
}

// GetReport godoc
// @Summary      获取报告详情
// @Description  获取单份 SLA 报告的详细内容
// @Tags         reports
// @Security     Bearer
// @Produce      json
// @Param        id  path      int  true  "报告 ID"
// @Success      200  {object}  handlers.Response{data=model.Report}
// @Failure      401  {object}  handlers.Response
// @Failure      403  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Router       /reports/{id} [get]
func (h *ReportHandler) GetReport(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		respondBadRequest(c, "无效的 ID")
		return
	}
	var report model.Report
	if err := h.db.Preload("Config").First(&report, id).Error; err != nil {
		respondNotFound(c, "报告不存在")
		return
	}
	role := middleware.CurrentRole(c)
	if role != "admin" && role != "viewer" {
		if report.Config == nil || report.Config.ID == 0 {
			respondForbidden(c, "无权访问该报告")
			return
		}
		if !h.checkConfigOwnership(c, *report.Config) {
			respondForbidden(c, "无权访问该报告")
			return
		}
	}
	respondOK(c, report)
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
