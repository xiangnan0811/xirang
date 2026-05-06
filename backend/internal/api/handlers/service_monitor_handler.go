package handlers

import (
	"encoding/json"
	"strings"
	"time"

	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ServiceMonitorHandler handles CRUD for HTTP/TCP uptime monitors.
type ServiceMonitorHandler struct {
	db *gorm.DB
}

// NewServiceMonitorHandler creates a new handler.
func NewServiceMonitorHandler(db *gorm.DB) *ServiceMonitorHandler {
	return &ServiceMonitorHandler{db: db}
}

type serviceMonitorRequest struct {
	Name               string  `json:"name" binding:"required"`
	Description        string  `json:"description"`
	Type               string  `json:"type" binding:"required"`
	Target             string  `json:"target" binding:"required"`
	IntervalSeconds    *int    `json:"interval_seconds"`
	TimeoutSeconds     *int    `json:"timeout_seconds"`
	HTTPMethod         string  `json:"http_method"`
	HTTPExpectedStatus *int    `json:"http_expected_status"`
	HTTPHeaders        string  `json:"http_headers"`
	Enabled            *bool   `json:"enabled"`
}

// List godoc
// @Summary      列出服务监控
// @Description  返回所有服务监控列表
// @Tags         service-monitors
// @Security     Bearer
// @Produce      json
// @Success      200  {object}  handlers.Response{data=[]model.ServiceMonitor}
// @Failure      401  {object}  handlers.Response
// @Router       /service-monitors [get]
func (h *ServiceMonitorHandler) List(c *gin.Context) {
	var items []model.ServiceMonitor
	if err := h.db.Order("id asc").Find(&items).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	respondOK(c, items)
}

// Get godoc
// @Summary      获取服务监控详情
// @Description  返回单个服务监控
// @Tags         service-monitors
// @Security     Bearer
// @Produce      json
// @Param        id   path      int  true  "服务监控 ID"
// @Success      200  {object}  handlers.Response{data=model.ServiceMonitor}
// @Failure      401  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Router       /service-monitors/{id} [get]
func (h *ServiceMonitorHandler) Get(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var item model.ServiceMonitor
	if err := h.db.First(&item, id).Error; err != nil {
		respondNotFound(c, "服务监控不存在")
		return
	}
	respondOK(c, item)
}

// Create godoc
// @Summary      创建服务监控
// @Description  创建新的 HTTP/TCP 服务监控
// @Tags         service-monitors
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        body  body      serviceMonitorRequest  true  "创建服务监控请求"
// @Success      201   {object}  handlers.Response{data=model.ServiceMonitor}
// @Failure      400   {object}  handlers.Response
// @Failure      401   {object}  handlers.Response
// @Router       /service-monitors [post]
func (h *ServiceMonitorHandler) Create(c *gin.Context) {
	var req serviceMonitorRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.Type = strings.TrimSpace(strings.ToLower(req.Type))
	req.Target = strings.TrimSpace(req.Target)
	req.HTTPMethod = strings.TrimSpace(strings.ToUpper(req.HTTPMethod))

	if len(req.Name) == 0 {
		respondBadRequest(c, "名称不能为空")
		return
	}
	if len(req.Name) > 128 {
		respondBadRequest(c, "名称过长（最多 128 字符）")
		return
	}
	if req.Type != "http" && req.Type != "tcp" {
		respondBadRequest(c, "type 必须是 http 或 tcp")
		return
	}
	if req.Target == "" {
		respondBadRequest(c, "target 不能为空")
		return
	}
	if req.Type == "http" && req.HTTPMethod == "" {
		req.HTTPMethod = "GET"
	}
	if req.HTTPMethod != "" && req.HTTPMethod != "GET" && req.HTTPMethod != "POST" && req.HTTPMethod != "HEAD" {
		respondBadRequest(c, "http_method 必须是 GET、POST 或 HEAD")
		return
	}

	intervalSeconds := 60
	if req.IntervalSeconds != nil {
		if *req.IntervalSeconds < 5 || *req.IntervalSeconds > 3600 {
			respondBadRequest(c, "interval_seconds 必须在 5-3600 之间")
			return
		}
		intervalSeconds = *req.IntervalSeconds
	}
	timeoutSeconds := 10
	if req.TimeoutSeconds != nil {
		if *req.TimeoutSeconds < 1 || *req.TimeoutSeconds > 300 {
			respondBadRequest(c, "timeout_seconds 必须在 1-300 之间")
			return
		}
		timeoutSeconds = *req.TimeoutSeconds
	}
	httpExpectedStatus := 200
	if req.HTTPExpectedStatus != nil {
		httpExpectedStatus = *req.HTTPExpectedStatus
	}

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	httpHeaders := strings.TrimSpace(req.HTTPHeaders)
	if httpHeaders == "" {
		httpHeaders = "{}"
	}
	// Validate JSON syntax.
	if httpHeaders != "{}" {
		var headerMap map[string]string
		if err := json.Unmarshal([]byte(httpHeaders), &headerMap); err != nil {
			respondBadRequest(c, "http_headers JSON 格式不合法")
			return
		}
	}

	item := model.ServiceMonitor{
		Name:               req.Name,
		Description:        strings.TrimSpace(req.Description),
		Type:               req.Type,
		Target:             req.Target,
		IntervalSeconds:    intervalSeconds,
		TimeoutSeconds:     timeoutSeconds,
		HTTPMethod:         req.HTTPMethod,
		HTTPExpectedStatus: httpExpectedStatus,
		HTTPHeaders:        httpHeaders,
		Enabled:            enabled,
		LastStatus:         "unknown",
	}
	if err := h.db.Create(&item).Error; err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint") ||
			strings.Contains(err.Error(), "duplicate key") ||
			strings.Contains(err.Error(), "SQLSTATE 23505") {
			respondConflict(c, "服务监控名称已存在")
			return
		}
		respondInternalError(c, err)
		return
	}
	respondCreated(c, item)
}

// Update godoc
// @Summary      更新服务监控
// @Description  完整更新服务监控配置
// @Tags         service-monitors
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        id    path      int                     true  "服务监控 ID"
// @Param        body  body      serviceMonitorRequest  true  "更新服务监控请求"
// @Success      200   {object}  handlers.Response{data=model.ServiceMonitor}
// @Failure      400   {object}  handlers.Response
// @Failure      401   {object}  handlers.Response
// @Failure      404   {object}  handlers.Response
// @Router       /service-monitors/{id} [put]
func (h *ServiceMonitorHandler) Update(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var req serviceMonitorRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.Type = strings.TrimSpace(strings.ToLower(req.Type))
	req.Target = strings.TrimSpace(req.Target)
	req.HTTPMethod = strings.TrimSpace(strings.ToUpper(req.HTTPMethod))

	if len(req.Name) == 0 {
		respondBadRequest(c, "名称不能为空")
		return
	}
	if req.Type != "http" && req.Type != "tcp" {
		respondBadRequest(c, "type 必须是 http 或 tcp")
		return
	}
	if req.Target == "" {
		respondBadRequest(c, "target 不能为空")
		return
	}
	if req.HTTPMethod != "" && req.HTTPMethod != "GET" && req.HTTPMethod != "POST" && req.HTTPMethod != "HEAD" {
		respondBadRequest(c, "http_method 必须是 GET、POST 或 HEAD")
		return
	}

	httpHeaders := strings.TrimSpace(req.HTTPHeaders)
	if httpHeaders == "" {
		httpHeaders = "{}"
	}
	if httpHeaders != "{}" {
		var headerMap map[string]string
		if err := json.Unmarshal([]byte(httpHeaders), &headerMap); err != nil {
			respondBadRequest(c, "http_headers JSON 格式不合法")
			return
		}
	}

	var item model.ServiceMonitor
	if err := h.db.First(&item, id).Error; err != nil {
		respondNotFound(c, "服务监控不存在")
		return
	}

	item.Name = req.Name
	item.Description = strings.TrimSpace(req.Description)
	item.Type = req.Type
	item.Target = req.Target
	if req.HTTPMethod != "" {
		item.HTTPMethod = req.HTTPMethod
	}
	if req.IntervalSeconds != nil {
		if *req.IntervalSeconds < 5 || *req.IntervalSeconds > 3600 {
			respondBadRequest(c, "interval_seconds 必须在 5-3600 之间")
			return
		}
		item.IntervalSeconds = *req.IntervalSeconds
	}
	if req.TimeoutSeconds != nil {
		if *req.TimeoutSeconds < 1 || *req.TimeoutSeconds > 300 {
			respondBadRequest(c, "timeout_seconds 必须在 1-300 之间")
			return
		}
		item.TimeoutSeconds = *req.TimeoutSeconds
	}
	if req.HTTPExpectedStatus != nil {
		item.HTTPExpectedStatus = *req.HTTPExpectedStatus
	}
	if req.Enabled != nil {
		item.Enabled = *req.Enabled
	}
	item.HTTPHeaders = httpHeaders

	if err := h.db.Save(&item).Error; err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint") ||
			strings.Contains(err.Error(), "duplicate key") ||
			strings.Contains(err.Error(), "SQLSTATE 23505") {
			respondConflict(c, "服务监控名称已存在")
			return
		}
		respondInternalError(c, err)
		return
	}
	respondOK(c, item)
}

// Delete godoc
// @Summary      删除服务监控
// @Description  删除指定服务监控
// @Tags         service-monitors
// @Security     Bearer
// @Produce      json
// @Param        id   path      int  true  "服务监控 ID"
// @Success      200  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Router       /service-monitors/{id} [delete]
func (h *ServiceMonitorHandler) Delete(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	if err := h.db.Delete(&model.ServiceMonitor{}, id).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	respondMessage(c, "deleted")
}

// StatusPage returns a public (unauthenticated) summary of all enabled monitors.
// GET /api/v1/status-page
func (h *ServiceMonitorHandler) StatusPage(c *gin.Context) {
	type statusItem struct {
		Name         string     `json:"name"`
		Type         string     `json:"type"`
		Status       string     `json:"status"`
		UptimePct    float64    `json:"uptime_pct"`
		LastCheckedAt *time.Time `json:"last_checked_at,omitempty"`
	}

	var monitors []model.ServiceMonitor
	if err := h.db.Where("enabled = ?", true).Order("id asc").Find(&monitors).Error; err != nil {
		respondInternalError(c, err)
		return
	}

	items := make([]statusItem, len(monitors))
	for i, m := range monitors {
		items[i] = statusItem{
			Name:          m.Name,
			Type:          m.Type,
			Status:        m.LastStatus,
			UptimePct:     m.UptimePct,
			LastCheckedAt: m.LastCheckedAt,
		}
	}
	respondOK(c, items)
}
