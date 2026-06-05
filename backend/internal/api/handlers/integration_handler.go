package handlers

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"xirang/backend/internal/apperr"
	"xirang/backend/internal/integration"
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type IntegrationHandler struct {
	db  *gorm.DB
	svc *integration.IntegrationService
}

func NewIntegrationHandler(db *gorm.DB, svc *integration.IntegrationService) *IntegrationHandler {
	return &IntegrationHandler{db: db, svc: svc}
}

type integrationRequest struct {
	Type             string `json:"type" binding:"required"`
	Name             string `json:"name" binding:"required"`
	Endpoint         string `json:"endpoint"`
	Enabled          *bool  `json:"enabled"`
	FailThreshold    int    `json:"fail_threshold"`
	CooldownMinutes  int    `json:"cooldown_minutes"`
	Secret           string `json:"secret"`
	SkipEndpointHint bool   `json:"skip_endpoint_hint"`
	BotToken         string `json:"bot_token"`
	ChatID           string `json:"chat_id"`
	AccessToken      string `json:"access_token"`
	HookID           string `json:"hook_id"`
	WebhookKey       string `json:"webhook_key"`
	ProxyURL         string `json:"proxy_url"`
}

type integrationPatchRequest struct {
	Name             *string `json:"name"`
	Endpoint         *string `json:"endpoint"`
	Enabled          *bool   `json:"enabled"`
	FailThreshold    *int    `json:"fail_threshold"`
	CooldownMinutes  *int    `json:"cooldown_minutes"`
	Secret           *string `json:"secret"`
	SkipEndpointHint bool    `json:"skip_endpoint_hint"`
	BotToken         *string `json:"bot_token"`
	ChatID           *string `json:"chat_id"`
	AccessToken      *string `json:"access_token"`
	HookID           *string `json:"hook_id"`
	WebhookKey       *string `json:"webhook_key"`
	ProxyURL         *string `json:"proxy_url"`
}

type integrationResponse struct {
	ID              uint      `json:"id"`
	Type            string    `json:"type"`
	Name            string    `json:"name"`
	Endpoint        string    `json:"endpoint"`
	HasSecret       bool      `json:"has_secret"`
	Enabled         bool      `json:"enabled"`
	FailThreshold   int       `json:"fail_threshold"`
	CooldownMinutes int       `json:"cooldown_minutes"`
	ProxyURL        string    `json:"proxy_url"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type integrationTestResponse struct {
	OK        bool   `json:"ok"`
	Message   string `json:"message"`
	LatencyMS int64  `json:"latency_ms"`
}

// List godoc
// @Summary      列出通知通道
// @Description  返回所有通知通道列表
// @Tags         integrations
// @Security     Bearer
// @Produce      json
// @Success      200  {object}  handlers.Response{data=[]integrationResponse}
// @Failure      401  {object}  handlers.Response
// @Router       /integrations [get]
func (h *IntegrationHandler) List(c *gin.Context) {
	var items []model.Integration
	if err := h.db.Order("id asc").Find(&items).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	out := make([]integrationResponse, 0, len(items))
	for i := range items {
		out = append(out, sanitizeIntegration(&items[i]))
	}
	respondOK(c, out)
}

// Get godoc
// @Summary      获取通知通道详情
// @Description  返回单个通知通道的详细信息
// @Tags         integrations
// @Security     Bearer
// @Produce      json
// @Param        id   path      int  true  "通知通道 ID"
// @Success      200  {object}  handlers.Response{data=integrationResponse}
// @Failure      401  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Router       /integrations/{id} [get]
func (h *IntegrationHandler) Get(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var item model.Integration
	if err := h.db.First(&item, id).Error; err != nil {
		respondNotFound(c, "通知通道不存在")
		return
	}
	respondOK(c, sanitizeIntegration(&item))
}

// Create godoc
// @Summary      创建通知通道
// @Description  创建新的通知通道（Webhook/Slack/Telegram/Email/飞书/钉钉/企业微信）
// @Tags         integrations
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        body  body      integrationRequest  true  "创建通知通道请求"
// @Success      201   {object}  handlers.Response{data=integrationResponse}
// @Failure      400   {object}  handlers.Response
// @Failure      401   {object}  handlers.Response
// @Router       /integrations [post]
func (h *IntegrationHandler) Create(c *gin.Context) {
	var req integrationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}

	req.Type = strings.TrimSpace(strings.ToLower(req.Type))
	req.Name = strings.TrimSpace(req.Name)
	req.Endpoint = strings.TrimSpace(req.Endpoint)
	req.Secret = strings.TrimSpace(req.Secret)
	req.ProxyURL = strings.TrimSpace(req.ProxyURL)

	// Domain hint check (handler concern — affects response code path)
	if !req.SkipEndpointHint {
		endpointForHint := req.Endpoint
		if built, err := integration.BuildEndpointFromFields(req.Type, req.BotToken, req.ChatID, req.AccessToken, req.HookID, req.WebhookKey); err == nil && built != "" {
			endpointForHint = built
		}
		if endpointForHint != "" {
			if hint := integration.CheckChannelDomainHint(req.Type, endpointForHint); hint != "" {
				respondOK(c, gin.H{"hint": hint, "created": false})
				return
			}
		}
	}

	item, err := h.svc.CreateIntegration(c.Request.Context(), integration.CreateIntegrationInput{
		Type:            req.Type,
		Name:            req.Name,
		Endpoint:        req.Endpoint,
		Secret:          req.Secret,
		Enabled:         req.Enabled,
		FailThreshold:   req.FailThreshold,
		CooldownMinutes: req.CooldownMinutes,
		ProxyURL:        req.ProxyURL,
		BotToken:        req.BotToken,
		ChatID:          req.ChatID,
		AccessToken:     req.AccessToken,
		HookID:          req.HookID,
		WebhookKey:      req.WebhookKey,
	})
	if err != nil {
		handleIntegrationServiceError(c, err)
		return
	}

	respondCreated(c, sanitizeIntegration(item))
}

// Update godoc
// @Summary      更新通知通道
// @Description  完整更新通知通道配置
// @Tags         integrations
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        id    path      int                 true  "通知通道 ID"
// @Param        body  body      integrationRequest  true  "更新通知通道请求"
// @Success      200   {object}  handlers.Response{data=integrationResponse}
// @Failure      400   {object}  handlers.Response
// @Failure      401   {object}  handlers.Response
// @Failure      404   {object}  handlers.Response
// @Router       /integrations/{id} [put]
func (h *IntegrationHandler) Update(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}

	var req integrationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}

	req.Type = strings.TrimSpace(strings.ToLower(req.Type))
	req.Name = strings.TrimSpace(req.Name)
	req.Endpoint = strings.TrimSpace(req.Endpoint)
	req.Secret = strings.TrimSpace(req.Secret)
	req.ProxyURL = strings.TrimSpace(req.ProxyURL)

	// Domain hint check (handler concern — affects response code path)
	if !req.SkipEndpointHint {
		endpointForHint := req.Endpoint
		if built, err := integration.BuildEndpointFromFields(req.Type, req.BotToken, req.ChatID, req.AccessToken, req.HookID, req.WebhookKey); err == nil && built != "" {
			endpointForHint = built
		}
		if endpointForHint != "" && !isMaskedIntegrationURL(endpointForHint) {
			if hint := integration.CheckChannelDomainHint(req.Type, endpointForHint); hint != "" {
				respondOK(c, gin.H{"hint": hint, "updated": false})
				return
			}
		}
	}

	updated, hadSecret, err := h.svc.UpdateIntegration(c.Request.Context(), id, integration.CreateIntegrationInput{
		Type:            req.Type,
		Name:            req.Name,
		Endpoint:        req.Endpoint,
		Secret:          req.Secret,
		Enabled:         req.Enabled,
		FailThreshold:   req.FailThreshold,
		CooldownMinutes: req.CooldownMinutes,
		ProxyURL:        req.ProxyURL,
		BotToken:        req.BotToken,
		ChatID:          req.ChatID,
		AccessToken:     req.AccessToken,
		HookID:          req.HookID,
		WebhookKey:      req.WebhookKey,
	})
	if err != nil {
		handleIntegrationServiceError(c, err)
		return
	}

	// Restore HasSecret for response (service sets it internally; we align with original logic)
	if !updated.HasSecret {
		updated.HasSecret = hadSecret || req.Secret != ""
	}
	respondOK(c, sanitizeIntegration(updated))
}

// Patch godoc
// @Summary      部分更新通知通道
// @Description  部分更新通知通道配置（仅更新提供的字段）
// @Tags         integrations
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        id    path      int                      true  "通知通道 ID"
// @Param        body  body      integrationPatchRequest  true  "部分更新请求"
// @Success      200   {object}  handlers.Response{data=integrationResponse}
// @Failure      400   {object}  handlers.Response
// @Failure      401   {object}  handlers.Response
// @Failure      404   {object}  handlers.Response
// @Router       /integrations/{id} [patch]
func (h *IntegrationHandler) Patch(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}

	var req integrationPatchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}

	var item model.Integration
	if err := h.db.First(&item, id).Error; err != nil {
		respondNotFound(c, "通知通道不存在")
		return
	}

	hadSecret := item.Secret != ""

	// 逐字段检查：仅非 nil 字段更新
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			respondBadRequest(c, "名称不能为空")
			return
		}
		item.Name = name
	}

	// 处理结构化字段重建 endpoint
	endpointChanged := false
	if req.BotToken != nil || req.ChatID != nil || req.AccessToken != nil || req.HookID != nil || req.WebhookKey != nil {
		built, err := buildEndpointFromPatch(item.Type, item.Endpoint, req)
		if err != nil {
			respondBadRequest(c, err.Error())
			return
		}
		if built != "" {
			item.Endpoint = built
			endpointChanged = true
		}
	}

	if req.Endpoint != nil {
		endpoint := strings.TrimSpace(*req.Endpoint)
		if endpoint == "" {
			respondBadRequest(c, "endpoint 不能为空")
			return
		}
		if isMaskedIntegrationURL(endpoint) {
			if !maskedIntegrationURLMatches(endpoint, item.Endpoint) {
				respondBadRequest(c, "endpoint 不能使用脱敏占位符")
				return
			}
		} else {
			if err := integration.ValidateIntegrationEndpoint(item.Type, endpoint); err != nil {
				respondBadRequest(c, err.Error())
				return
			}
			item.Endpoint = endpoint
			endpointChanged = true
		}
	}

	// 域名建议提示（仅当 endpoint 变化且未跳过时）
	if endpointChanged && !req.SkipEndpointHint {
		if hint := integration.CheckChannelDomainHint(item.Type, item.Endpoint); hint != "" {
			respondOK(c, gin.H{"hint": hint, "updated": false})
			return
		}
	}

	if req.Enabled != nil {
		item.Enabled = *req.Enabled
	}
	if req.FailThreshold != nil {
		if *req.FailThreshold <= 0 {
			respondBadRequest(c, "fail_threshold 必须大于 0")
			return
		}
		item.FailThreshold = *req.FailThreshold
	}
	if req.CooldownMinutes != nil {
		if *req.CooldownMinutes <= 0 {
			respondBadRequest(c, "cooldown_minutes 必须大于 0")
			return
		}
		item.CooldownMinutes = *req.CooldownMinutes
	}
	if req.Secret != nil && *req.Secret != "" {
		item.Secret = strings.TrimSpace(*req.Secret)
	}
	if req.ProxyURL != nil {
		proxyURL := strings.TrimSpace(*req.ProxyURL)
		if proxyURL != "" {
			if isMaskedIntegrationURL(proxyURL) {
				if !maskedIntegrationURLMatches(proxyURL, item.ProxyURL) {
					respondBadRequest(c, "代理地址不能使用脱敏占位符")
					return
				}
			} else {
				if err := integration.ValidateProxyURL(proxyURL); err != nil {
					respondBadRequest(c, err.Error())
					return
				}
				item.ProxyURL = proxyURL
			}
		} else {
			item.ProxyURL = ""
		}
	}

	if err := h.db.Save(&item).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	if err := h.db.First(&item, item.ID).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	item.HasSecret = hadSecret || (req.Secret != nil && *req.Secret != "")
	respondOK(c, sanitizeIntegration(&item))
}

// Delete godoc
// @Summary      删除通知通道
// @Description  删除指定通知通道
// @Tags         integrations
// @Security     Bearer
// @Produce      json
// @Param        id   path      int  true  "通知通道 ID"
// @Success      200  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Router       /integrations/{id} [delete]
func (h *IntegrationHandler) Delete(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	if err := h.db.Delete(&model.Integration{}, id).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	respondMessage(c, "deleted")
}

// Test godoc
// @Summary      测试通知通道
// @Description  向通知通道发送测试消息，验证配置是否正确
// @Tags         integrations
// @Security     Bearer
// @Produce      json
// @Param        id   path      int  true  "通知通道 ID"
// @Success      200  {object}  handlers.Response{data=integrationTestResponse}
// @Failure      401  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Router       /integrations/{id}/test [post]
func (h *IntegrationHandler) Test(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}

	result, err := h.svc.TestIntegration(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, apperr.ErrNotFound) {
			respondNotFound(c, "通知通道不存在")
			return
		}
		respondInternalError(c, err)
		return
	}

	respondOK(c, integrationTestResponse{
		OK:        result.OK,
		Message:   result.Message,
		LatencyMS: result.LatencyMS,
	})
}

// handleIntegrationServiceError maps service-layer sentinel errors to HTTP responses.
func handleIntegrationServiceError(c *gin.Context, err error) {
	if errors.Is(err, apperr.ErrValidation) {
		msg := err.Error()
		prefix := apperr.ErrValidation.Error() + ": "
		if after, ok := strings.CutPrefix(msg, prefix); ok {
			msg = after
		}
		respondBadRequest(c, msg)
		return
	}
	if errors.Is(err, apperr.ErrNotFound) {
		respondNotFound(c, "通知通道不存在")
		return
	}
	if errors.Is(err, apperr.ErrDuplicate) {
		respondConflict(c, "资源已存在")
		return
	}
	respondInternalError(c, err)
}

// --- response / sanitization helpers ---

func sanitizeIntegration(item *model.Integration) integrationResponse {
	return integrationResponse{
		ID:              item.ID,
		Type:            item.Type,
		Name:            item.Name,
		Endpoint:        maskIntegrationURL(item.Endpoint),
		HasSecret:       item.HasSecret,
		Enabled:         item.Enabled,
		FailThreshold:   item.FailThreshold,
		CooldownMinutes: item.CooldownMinutes,
		ProxyURL:        maskIntegrationURL(item.ProxyURL),
		CreatedAt:       item.CreatedAt,
		UpdatedAt:       item.UpdatedAt,
	}
}

func maskIntegrationURL(raw string) string {
	return integration.MaskIntegrationURL(raw)
}

func isMaskedIntegrationURL(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	if trimmed == integration.MaskedURLPlaceholder {
		return true
	}
	parsed, err := url.Parse(trimmed)
	return err == nil && parsed != nil && strings.EqualFold(parsed.Host, integration.MaskedURLHost) && parsed.Path == "" && parsed.RawQuery == "" && parsed.Fragment == ""
}

func maskedIntegrationURLMatches(masked, original string) bool {
	if !isMaskedIntegrationURL(masked) {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(masked), maskIntegrationURL(original))
}

// --- endpoint parsing helpers (Patch-specific) ---

// buildEndpointFromPatch 根据 PATCH 请求中的结构化字段和现有 endpoint 重建完整 endpoint
func buildEndpointFromPatch(channelType, existingEndpoint string, req integrationPatchRequest) (string, error) {
	normalizedType := strings.ToLower(strings.TrimSpace(channelType))

	switch normalizedType {
	case "telegram":
		existingBotToken, existingChatID := parseTelegramEndpointParts(existingEndpoint)
		botToken := existingBotToken
		chatID := existingChatID
		if req.BotToken != nil {
			botToken = strings.TrimSpace(*req.BotToken)
		}
		if req.ChatID != nil {
			chatID = strings.TrimSpace(*req.ChatID)
		}
		if botToken == "" || chatID == "" {
			return "", fmt.Errorf("telegram 通道需要 bot_token 和 chat_id")
		}
		return integration.BuildEndpointFromFields(normalizedType, botToken, chatID, "", "", "")

	case "dingtalk":
		existingToken := parseDingtalkAccessToken(existingEndpoint)
		accessToken := existingToken
		if req.AccessToken != nil {
			accessToken = strings.TrimSpace(*req.AccessToken)
		}
		if accessToken == "" {
			return "", fmt.Errorf("dingtalk 通道需要 access_token")
		}
		return integration.BuildEndpointFromFields(normalizedType, "", "", accessToken, "", "")

	case "feishu":
		existingHookID := parseFeishuHookID(existingEndpoint)
		hookID := existingHookID
		if req.HookID != nil {
			hookID = strings.TrimSpace(*req.HookID)
		}
		if hookID == "" {
			return "", fmt.Errorf("feishu 通道需要 hook_id")
		}
		return integration.BuildEndpointFromFields(normalizedType, "", "", "", hookID, "")

	case "wecom":
		existingKey := parseWecomWebhookKey(existingEndpoint)
		webhookKey := existingKey
		if req.WebhookKey != nil {
			webhookKey = strings.TrimSpace(*req.WebhookKey)
		}
		if webhookKey == "" {
			return "", fmt.Errorf("wecom 通道需要 webhook_key")
		}
		return integration.BuildEndpointFromFields(normalizedType, "", "", "", "", webhookKey)

	default:
		return "", nil
	}
}

// parseTelegramEndpointParts 从已有完整 endpoint 中提取 bot_token 和 chat_id
func parseTelegramEndpointParts(endpoint string) (botToken, chatID string) {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || parsed == nil {
		return "", ""
	}
	// 从路径中提取 bot token: /bot<token>/sendMessage
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	for _, seg := range segments {
		if strings.HasPrefix(seg, "bot") && len(seg) > 3 {
			botToken = strings.TrimPrefix(seg, "bot")
			break
		}
	}
	chatID = parsed.Query().Get("chat_id")
	return botToken, chatID
}

// parseDingtalkAccessToken 从钉钉 endpoint 中提取 access_token
func parseDingtalkAccessToken(endpoint string) string {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || parsed == nil {
		return ""
	}
	return parsed.Query().Get("access_token")
}

// parseFeishuHookID 从飞书 endpoint 中提取 hook ID
func parseFeishuHookID(endpoint string) string {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || parsed == nil {
		return ""
	}
	// URL 格式: https://open.feishu.cn/open-apis/bot/v2/hook/{id}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(segments) > 0 {
		return segments[len(segments)-1]
	}
	return ""
}

// parseWecomWebhookKey 从企业微信 endpoint 中提取 webhook key
func parseWecomWebhookKey(endpoint string) string {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || parsed == nil {
		return ""
	}
	return parsed.Query().Get("key")
}
