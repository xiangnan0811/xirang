package handlers

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"xirang/backend/internal/apperr"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ServiceMonitorChangeNotifier lets the scheduler wake and invalidate in-flight
// work immediately after a CRUD mutation.
type ServiceMonitorChangeNotifier interface {
	NotifyMonitorChange(uint)
}

// ServiceMonitorHandler handles CRUD for HTTP/TCP uptime monitors.
type ServiceMonitorHandler struct {
	db       *gorm.DB
	notifier ServiceMonitorChangeNotifier
}

// NewServiceMonitorHandler creates a new handler.
func NewServiceMonitorHandler(db *gorm.DB, notifier ServiceMonitorChangeNotifier) *ServiceMonitorHandler {
	return &ServiceMonitorHandler{db: db, notifier: notifier}
}

type serviceMonitorRequest struct {
	Name               string `json:"name" binding:"required"`
	Description        string `json:"description"`
	Type               string `json:"type" binding:"required"`
	Target             string `json:"target" binding:"required"`
	IntervalSeconds    *int   `json:"interval_seconds"`
	TimeoutSeconds     *int   `json:"timeout_seconds"`
	HTTPMethod         string `json:"http_method"`
	HTTPExpectedStatus *int   `json:"http_expected_status"`
	// HTTPHeaders is a JSON object encoded as a JSON string. Omit it on
	// updates to preserve existing credentials only when the monitor use
	// (type, target, and HTTP method) is unchanged; use "{}" to clear them.
	HTTPHeaders *string `json:"http_headers"`
	Enabled     *bool   `json:"enabled"`
}

const (
	serviceMonitorHeadersRetargetedCode    = "service_monitor_target_change_requires_headers"
	serviceMonitorHeadersRetargetedMessage = "监控目标、类型或请求方式已变化，请明确提供新的 http_headers，或使用 {} 清除现有请求头"
	serviceMonitorConcurrentUpdateCode     = "service_monitor_concurrent_update"
	serviceMonitorConcurrentUpdateMessage  = "服务监控配置已被其他请求更新，请重新加载后重试"
)

var (
	errServiceMonitorHeadersRetargeted = errors.New(serviceMonitorHeadersRetargetedCode)
	errServiceMonitorConcurrentUpdate  = errors.New(serviceMonitorConcurrentUpdateCode)
)

type serviceMonitorConflictReason struct {
	Code   string            `json:"code"`
	Params map[string]string `json:"params"`
}

type serviceMonitorConflictData struct {
	Reason serviceMonitorConflictReason `json:"reason"`
}
type serviceMonitorResponse struct {
	ID                    uint       `json:"id"`
	Name                  string     `json:"name"`
	Description           string     `json:"description"`
	Type                  string     `json:"type"`
	Target                string     `json:"target"`
	IntervalSeconds       int        `json:"interval_seconds"`
	TimeoutSeconds        int        `json:"timeout_seconds"`
	HTTPMethod            string     `json:"http_method"`
	HTTPExpectedStatus    int        `json:"http_expected_status"`
	HTTPHeaderNames       []string   `json:"http_header_names"`
	HTTPHeadersConfigured bool       `json:"http_headers_configured"`
	Enabled               bool       `json:"enabled"`
	LastStatus            string     `json:"last_status"`
	UptimePct             float64    `json:"uptime_pct"`
	LastCheckedAt         *time.Time `json:"last_checked_at"`
	CreatedAt             time.Time  `json:"created_at"`
	UpdatedAt             time.Time  `json:"updated_at"`
}

func newServiceMonitorResponse(item model.ServiceMonitor) serviceMonitorResponse {
	names := make([]string, 0)
	configured := false
	raw := strings.TrimSpace(item.HTTPHeaders)
	if raw != "" {
		if decrypted, err := secure.DecryptIfNeeded(raw); err == nil {
			raw = strings.TrimSpace(decrypted)
		}
	}
	if raw != "" && raw != "{}" {
		var values map[string]string
		if err := json.Unmarshal([]byte(raw), &values); err == nil {
			for name := range values {
				names = append(names, name)
			}
			sort.Strings(names)
			configured = len(names) > 0
		}
	}
	return serviceMonitorResponse{
		ID: item.ID, Name: item.Name, Description: item.Description, Type: item.Type,
		Target: item.Target, IntervalSeconds: item.IntervalSeconds, TimeoutSeconds: item.TimeoutSeconds,
		HTTPMethod: item.HTTPMethod, HTTPExpectedStatus: item.HTTPExpectedStatus,
		HTTPHeaderNames: names, HTTPHeadersConfigured: configured, Enabled: item.Enabled,
		LastStatus: item.LastStatus, UptimePct: item.UptimePct, LastCheckedAt: item.LastCheckedAt,
		CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
	}
}

func (h *ServiceMonitorHandler) notifyChange(id uint) {
	if h.notifier != nil {
		h.notifier.NotifyMonitorChange(id)
	}
}
func parseHTTPHeaders(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = "{}"
	}
	var values map[string]string
	if err := json.Unmarshal([]byte(raw), &values); err != nil || values == nil {
		return "", errors.New("http_headers JSON 格式不合法")
	}
	return raw, nil
}
func normalizedServiceMonitorHTTPMethod(raw string) string {
	method := strings.ToUpper(strings.TrimSpace(raw))
	if method == "" {
		return "GET"
	}
	return method
}

func serviceMonitorUseChanged(current model.ServiceMonitor, req serviceMonitorRequest) bool {
	if current.Type != req.Type || current.Target != req.Target {
		return true
	}
	if current.Type != "http" || req.Type != "http" {
		return false
	}
	return normalizedServiceMonitorHTTPMethod(current.HTTPMethod) !=
		normalizedServiceMonitorHTTPMethod(req.HTTPMethod)
}

// serviceMonitorHeadersConfigured reports true for any non-empty or malformed
// persisted value. A malformed value is treated as configured so a target
// change cannot silently carry an unknown secret to a new destination.
func serviceMonitorHeadersConfigured(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" {
		return false
	}
	var values map[string]string
	if err := json.Unmarshal([]byte(raw), &values); err != nil || values == nil {
		return true
	}
	return len(values) > 0
}

func respondServiceMonitorHeadersRetargeted(c *gin.Context) {
	respondConflictData(c, serviceMonitorHeadersRetargetedMessage, serviceMonitorConflictData{
		Reason: serviceMonitorConflictReason{
			Code:   serviceMonitorHeadersRetargetedCode,
			Params: map[string]string{},
		},
	})
}

func respondServiceMonitorConcurrentUpdate(c *gin.Context) {
	respondConflictData(c, serviceMonitorConcurrentUpdateMessage, serviceMonitorConflictData{
		Reason: serviceMonitorConflictReason{
			Code:   serviceMonitorConcurrentUpdateCode,
			Params: map[string]string{},
		},
	})
}

// List godoc
// @Summary      列出服务监控
// @Description  返回所有服务监控列表（请求头仅返回名称和配置状态）
// @Tags         service-monitors
// @Security     Bearer
// @Produce      json
// @Success      200  {object}  handlers.Response{data=[]serviceMonitorResponse}
// @Failure      401  {object}  handlers.Response
// @Router       /service-monitors [get]
func (h *ServiceMonitorHandler) List(c *gin.Context) {
	var items []model.ServiceMonitor
	if err := h.db.Order("id asc").Find(&items).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	out := make([]serviceMonitorResponse, 0, len(items))
	for _, item := range items {
		out = append(out, newServiceMonitorResponse(item))
	}
	respondOK(c, out)
}

// Get godoc
// @Summary      获取服务监控详情
// @Description  返回单个服务监控（请求头仅返回名称和配置状态）
// @Tags         service-monitors
// @Security     Bearer
// @Produce      json
// @Param        id   path      int  true  "服务监控 ID"
// @Success      200  {object}  handlers.Response{data=serviceMonitorResponse}
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
	respondOK(c, newServiceMonitorResponse(item))
}

// Create godoc
// @Summary      创建服务监控
// @Description  创建新的 HTTP/TCP 服务监控
// @Tags         service-monitors
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        body  body      serviceMonitorRequest  true  "创建服务监控请求"
// @Success      201   {object}  handlers.Response{data=serviceMonitorResponse}
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

	httpHeaders := "{}"
	if req.HTTPHeaders != nil {
		var err error
		httpHeaders, err = parseHTTPHeaders(*req.HTTPHeaders)
		if err != nil {
			respondBadRequest(c, err.Error())
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
	// GORM's struct Create callback replaces selected zero values with the
	// model's defaults and also writes those defaults back into item. Keep the
	// model hook on the Create path (HTTP headers are encrypted there), then
	// restore any explicitly supplied zero value with an explicit-column update
	// before committing the transaction.
	if err := h.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&item).Error; err != nil {
			return err
		}
		if req.Enabled == nil {
			return nil
		}
		item.Enabled = enabled
		return tx.Model(&item).Select([]string{"enabled"}).Updates(&item).Error
	}); err != nil {
		err = apperr.WrapDBError(err)
		if errors.Is(err, apperr.ErrDuplicate) {
			respondConflict(c, "服务监控名称已存在")
			return
		}
		respondInternalError(c, err)
		return
	}
	h.notifyChange(item.ID)
	respondCreated(c, newServiceMonitorResponse(item))
}

// @Summary      更新服务监控
// @Description  完整更新服务监控配置；省略 http_headers 仅在目标、类型和 HTTP 方法均未变化时保留现有请求头，修改监控用途时必须明确替换或清除；409 响应的 data.reason.code 为 service_monitor_target_change_requires_headers 或 service_monitor_concurrent_update
// @Tags         service-monitors
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        id    path      int                     true  "服务监控 ID"
// @Param        body  body      serviceMonitorRequest  true  "更新服务监控请求"
// @Success      200   {object}  handlers.Response{data=serviceMonitorResponse}
// @Failure      400   {object}  handlers.Response
// @Failure      401   {object}  handlers.Response
// @Failure      404   {object}  handlers.Response
// @Failure      409   {object}  handlers.Response
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
	if req.IntervalSeconds != nil && (*req.IntervalSeconds < 5 || *req.IntervalSeconds > 3600) {
		respondBadRequest(c, "interval_seconds 必须在 5-3600 之间")
		return
	}
	if req.TimeoutSeconds != nil && (*req.TimeoutSeconds < 1 || *req.TimeoutSeconds > 300) {
		respondBadRequest(c, "timeout_seconds 必须在 1-300 之间")
		return
	}

	var httpHeaders *string
	if req.HTTPHeaders != nil {
		normalized, err := parseHTTPHeaders(*req.HTTPHeaders)
		if err != nil {
			respondBadRequest(c, err.Error())
			return
		}
		httpHeaders = &normalized
	}

	var item model.ServiceMonitor
	err := h.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		// Lock the complete monitor row before comparing use identity or
		// selecting a retained header value. Credential rotation uses the same
		// Update path, so concurrent retargets cannot race a stale snapshot.
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&item, id).Error; err != nil {
			return err
		}
		originalUpdatedAt := item.UpdatedAt
		// Persisted timestamps are compared in UTC, matching the production
		// PostgreSQL timestamp contract and the repository's CAS convention.

		nextHTTPMethod := item.HTTPMethod
		if req.HTTPMethod != "" {
			nextHTTPMethod = req.HTTPMethod
		}
		if req.Type == "http" && nextHTTPMethod == "" {
			nextHTTPMethod = "GET"
		}
		effectiveRequest := req
		effectiveRequest.HTTPMethod = nextHTTPMethod
		if serviceMonitorUseChanged(item, effectiveRequest) &&
			httpHeaders == nil && serviceMonitorHeadersConfigured(item.HTTPHeaders) {
			return errServiceMonitorHeadersRetargeted
		}

		item.Name = req.Name
		item.Description = strings.TrimSpace(req.Description)
		item.Type = req.Type
		item.Target = req.Target
		item.HTTPMethod = nextHTTPMethod
		if req.IntervalSeconds != nil {
			item.IntervalSeconds = *req.IntervalSeconds
		}
		if req.TimeoutSeconds != nil {
			item.TimeoutSeconds = *req.TimeoutSeconds
		}
		if req.HTTPExpectedStatus != nil {
			item.HTTPExpectedStatus = *req.HTTPExpectedStatus
		}
		if req.Enabled != nil {
			item.Enabled = *req.Enabled
		}
		if httpHeaders != nil {
			item.HTTPHeaders = *httpHeaders
		}

		updateColumns := []string{
			"name", "description", "type", "target", "interval_seconds",
			"timeout_seconds", "http_method", "http_expected_status", "enabled", "updated_at",
		}
		if httpHeaders != nil {
			updateColumns = append(updateColumns, "http_headers")
		}
		result := tx.Model(&item).
			Where("id = ? AND updated_at = ?", id, originalUpdatedAt.UTC()).
			Select(updateColumns).Updates(&item)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errServiceMonitorConcurrentUpdate
		}
		return nil
	})
	if errors.Is(err, errServiceMonitorHeadersRetargeted) {
		respondServiceMonitorHeadersRetargeted(c)
		return
	}
	if errors.Is(err, errServiceMonitorConcurrentUpdate) {
		respondServiceMonitorConcurrentUpdate(c)
		return
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		respondNotFound(c, "服务监控不存在")
		return
	}
	if err != nil {
		err = apperr.WrapDBError(err)
		if errors.Is(err, apperr.ErrDuplicate) {
			respondConflict(c, "服务监控名称已存在")
			return
		}
		if errors.Is(err, apperr.ErrValidation) {
			respondBadRequest(c, err.Error())
			return
		}
		respondInternalError(c, err)
		return
	}
	h.notifyChange(item.ID)
	respondOK(c, newServiceMonitorResponse(item))
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
func (h *ServiceMonitorHandler) Delete(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	result := h.db.Delete(&model.ServiceMonitor{}, id)
	if result.Error != nil {
		respondInternalError(c, result.Error)
		return
	}
	if result.RowsAffected == 0 {
		respondNotFound(c, "服务监控不存在")
		return
	}
	h.notifyChange(id)
	respondMessage(c, "deleted")
}

// StatusPage returns a public (unauthenticated) summary of all enabled monitors.
// GET /api/v1/status-page
func (h *ServiceMonitorHandler) StatusPage(c *gin.Context) {
	type statusItem struct {
		Name          string     `json:"name"`
		Type          string     `json:"type"`
		Status        string     `json:"status"`
		UptimePct     float64    `json:"uptime_pct"`
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
