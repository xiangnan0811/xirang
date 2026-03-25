package handlers

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"xirang/backend/internal/alerting"
	"xirang/backend/internal/model"
	"xirang/backend/internal/util"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type IntegrationHandler struct {
	db *gorm.DB
}

func NewIntegrationHandler(db *gorm.DB) *IntegrationHandler {
	return &IntegrationHandler{db: db}
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

var knownIntegrationTypes = map[string]bool{
	"webhook": true, "slack": true, "telegram": true, "email": true,
	"feishu": true, "dingtalk": true, "wecom": true,
}

// channelDomainHints 各通道的预期域名提示
var channelDomainHints = map[string]string{
	"feishu":   "open.feishu.cn",
	"dingtalk": "oapi.dingtalk.com",
	"wecom":    "qyapi.weixin.qq.com",
	"slack":    "hooks.slack.com",
}

// checkChannelDomainHint 返回域名建议提示（非强制），仅对 URL 类型渠道有效
func checkChannelDomainHint(channelType, endpoint string) string {
	expected, ok := channelDomainHints[channelType]
	if !ok {
		return ""
	}
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || parsed == nil {
		return ""
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	if host == "" {
		return ""
	}
	if host != expected && !strings.HasSuffix(host, "."+expected) {
		return fmt.Sprintf("%s 通道通常使用 %s，当前地址 %s 不在此域名下，请确认地址是否正确", channelType, expected, host)
	}
	return ""
}

type integrationTestResponse struct {
	OK        bool   `json:"ok"`
	Message   string `json:"message"`
	LatencyMS int64  `json:"latency_ms"`
}

func validateIntegrationEndpoint(channelType, endpoint string) error {
	normalizedType := strings.ToLower(strings.TrimSpace(channelType))
	if normalizedType == "email" {
		return nil
	}

	parsedURL, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || parsedURL == nil {
		return fmt.Errorf("%s 通道 endpoint 必须是合法 URL", normalizedType)
	}

	scheme := strings.ToLower(strings.TrimSpace(parsedURL.Scheme))
	if (scheme != "http" && scheme != "https") || parsedURL.Host == "" {
		return fmt.Errorf("%s 通道仅允许 http/https URL", normalizedType)
	}
	if normalizedType == "telegram" {
		if err := validateTelegramEndpoint(parsedURL); err != nil {
			return err
		}
	}

	blockPrivate, err := util.ReadBoolEnv("INTEGRATION_BLOCK_PRIVATE_ENDPOINTS", true)
	if err != nil {
		return err
	}
	if !blockPrivate {
		return nil
	}

	hostName := strings.TrimSpace(parsedURL.Hostname())
	if hostName == "" {
		return fmt.Errorf("%s 通道 endpoint 缺少主机地址", normalizedType)
	}
	if err := validatePublicEndpointHost(hostName); err != nil {
		return fmt.Errorf("%s 通道 endpoint 不安全: %w", normalizedType, err)
	}
	return nil
}

func validateTelegramEndpoint(parsedURL *url.URL) error {
	_, err := util.ValidateTelegramEndpoint(parsedURL)
	return err
}

func validatePublicEndpointHost(host string) error {
	normalizedHost := strings.Trim(strings.ToLower(strings.TrimSpace(host)), "[]")
	if normalizedHost == "" {
		return fmt.Errorf("主机地址不能为空")
	}
	if normalizedHost == "localhost" || strings.HasSuffix(normalizedHost, ".localhost") {
		return fmt.Errorf("禁止使用本地回环地址")
	}

	if ip, err := netip.ParseAddr(normalizedHost); err == nil {
		if isPrivateOrLoopback(ip.Unmap()) {
			return fmt.Errorf("禁止使用内网或回环地址")
		}
		return nil
	}

	resolved, err := resolveHostAddrs(normalizedHost)
	if err != nil {
		return fmt.Errorf("无法解析主机地址，请检查域名是否正确")
	}
	if len(resolved) == 0 {
		return fmt.Errorf("主机地址无法解析，请检查域名是否正确")
	}
	for _, ip := range resolved {
		addr, ok := netip.AddrFromSlice(ip)
		if !ok {
			continue
		}
		if isPrivateOrLoopback(addr.Unmap()) {
			return fmt.Errorf("该地址指向内网或回环地址，不允许使用")
		}
	}
	return nil
}

func resolveHostAddrs(host string) ([]net.IP, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ipAddrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	addresses := make([]net.IP, 0, len(ipAddrs))
	for _, item := range ipAddrs {
		if item.IP != nil {
			addresses = append(addresses, item.IP)
		}
	}
	return addresses, nil
}

func isPrivateOrLoopback(addr netip.Addr) bool {
	return addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() || addr.IsUnspecified()
}

func (h *IntegrationHandler) List(c *gin.Context) {
	var items []model.Integration
	if err := h.db.Order("id asc").Find(&items).Error; err != nil {
		log.Printf("服务器内部错误: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "服务器内部错误"})
		return
	}
	for i := range items {
		maskIntegrationEndpoint(&items[i])
	}
	c.JSON(http.StatusOK, gin.H{"data": items})
}

func (h *IntegrationHandler) Get(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var item model.Integration
	if err := h.db.First(&item, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "通知通道不存在"})
		return
	}
	maskIntegrationEndpoint(&item)
	c.JSON(http.StatusOK, gin.H{"data": item})
}

func (h *IntegrationHandler) Create(c *gin.Context) {
	var req integrationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数不合法"})
		return
	}

	req.Type = strings.TrimSpace(strings.ToLower(req.Type))
	req.Name = strings.TrimSpace(req.Name)
	req.Endpoint = strings.TrimSpace(req.Endpoint)
	req.Secret = strings.TrimSpace(req.Secret)
	if req.Type == "" || req.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数不合法"})
		return
	}
	if !knownIntegrationTypes[req.Type] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "不支持的通知通道类型"})
		return
	}

	// 优先从结构化字段构建 endpoint
	if built, err := buildEndpointFromFields(req.Type, req.BotToken, req.ChatID, req.AccessToken, req.HookID, req.WebhookKey); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	} else if built != "" {
		req.Endpoint = built
	}

	if req.Endpoint == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "endpoint 或通道特定字段必填"})
		return
	}

	if err := validateIntegrationEndpoint(req.Type, req.Endpoint); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// 域名建议提示（非强制），用户可设置 skip_endpoint_hint=true 跳过
	if !req.SkipEndpointHint {
		if hint := checkChannelDomainHint(req.Type, req.Endpoint); hint != "" {
			c.JSON(http.StatusOK, gin.H{"hint": hint, "created": false})
			return
		}
	}

	// 验证代理 URL
	req.ProxyURL = strings.TrimSpace(req.ProxyURL)
	if req.ProxyURL != "" {
		if err := validateProxyURL(req.ProxyURL); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	if req.FailThreshold <= 0 {
		req.FailThreshold = 1
	}
	if req.CooldownMinutes <= 0 {
		req.CooldownMinutes = 5
	}

	item := model.Integration{
		Type:            req.Type,
		Name:            req.Name,
		Endpoint:        req.Endpoint,
		Secret:          req.Secret,
		Enabled:         enabled,
		FailThreshold:   req.FailThreshold,
		CooldownMinutes: req.CooldownMinutes,
		ProxyURL:        req.ProxyURL,
	}
	if err := h.db.Create(&item).Error; err != nil {
		log.Printf("创建通知通道失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "服务器内部错误"})
		return
	}
	item.HasSecret = req.Secret != ""
	maskIntegrationEndpoint(&item)
	c.JSON(http.StatusCreated, gin.H{"data": item})
}

func (h *IntegrationHandler) Update(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var req integrationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数不合法"})
		return
	}

	req.Type = strings.TrimSpace(strings.ToLower(req.Type))
	req.Name = strings.TrimSpace(req.Name)
	req.Endpoint = strings.TrimSpace(req.Endpoint)
	req.Secret = strings.TrimSpace(req.Secret)
	if req.Type == "" || req.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数不合法"})
		return
	}
	if !knownIntegrationTypes[req.Type] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "不支持的通知通道类型"})
		return
	}

	// 优先从结构化字段构建 endpoint
	if built, err := buildEndpointFromFields(req.Type, req.BotToken, req.ChatID, req.AccessToken, req.HookID, req.WebhookKey); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	} else if built != "" {
		req.Endpoint = built
	}

	if req.Endpoint == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "endpoint 或通道特定字段必填"})
		return
	}

	if err := validateIntegrationEndpoint(req.Type, req.Endpoint); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var item model.Integration
	if err := h.db.First(&item, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "通知通道不存在"})
		return
	}
	if req.FailThreshold <= 0 {
		req.FailThreshold = item.FailThreshold
	}
	if req.CooldownMinutes <= 0 {
		req.CooldownMinutes = item.CooldownMinutes
	}

	// 域名建议提示（非强制），用户可设置 skip_endpoint_hint=true 跳过
	if !req.SkipEndpointHint {
		if hint := checkChannelDomainHint(req.Type, req.Endpoint); hint != "" {
			c.JSON(http.StatusOK, gin.H{"hint": hint, "updated": false})
			return
		}
	}

	// 验证代理 URL
	req.ProxyURL = strings.TrimSpace(req.ProxyURL)
	if req.ProxyURL != "" {
		if err := validateProxyURL(req.ProxyURL); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}

	// 记录更新前是否有 secret（AfterFind 已解密）
	hadSecret := item.Secret != ""

	item.Type = req.Type
	item.Name = req.Name
	item.Endpoint = req.Endpoint
	if req.Enabled != nil {
		item.Enabled = *req.Enabled
	}
	item.FailThreshold = req.FailThreshold
	item.CooldownMinutes = req.CooldownMinutes
	if req.Secret != "" {
		item.Secret = req.Secret
	}
	// ProxyURL: 空字符串表示清除，未提供时保持不变（与 Secret 同模式不同，
	// 因为 proxy_url 是可选字段，客户端可能未知此字段。这里采用：非空则更新。
	// 如需显式清除，使用 PATCH + proxy_url=""）
	if req.ProxyURL != "" {
		item.ProxyURL = req.ProxyURL
	}

	if err := h.db.Save(&item).Error; err != nil {
		log.Printf("更新通知通道失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "服务器内部错误"})
		return
	}
	item.HasSecret = hadSecret || req.Secret != ""
	maskIntegrationEndpoint(&item)
	c.JSON(http.StatusOK, gin.H{"data": item})
}

func (h *IntegrationHandler) Patch(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}

	var req integrationPatchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数不合法"})
		return
	}

	var item model.Integration
	if err := h.db.First(&item, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "通知通道不存在"})
		return
	}

	hadSecret := item.Secret != ""

	// 逐字段检查：仅非 nil 字段更新
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "名称不能为空"})
			return
		}
		item.Name = name
	}

	// 处理结构化字段重建 endpoint
	endpointChanged := false
	if req.BotToken != nil || req.ChatID != nil || req.AccessToken != nil || req.HookID != nil || req.WebhookKey != nil {
		built, err := buildEndpointFromPatch(item.Type, item.Endpoint, req)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
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
			c.JSON(http.StatusBadRequest, gin.H{"error": "endpoint 不能为空"})
			return
		}
		if err := validateIntegrationEndpoint(item.Type, endpoint); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		item.Endpoint = endpoint
		endpointChanged = true
	}

	// 域名建议提示（仅当 endpoint 变化且未跳过时）
	if endpointChanged && !req.SkipEndpointHint {
		if hint := checkChannelDomainHint(item.Type, item.Endpoint); hint != "" {
			c.JSON(http.StatusOK, gin.H{"hint": hint, "updated": false})
			return
		}
	}

	if req.Enabled != nil {
		item.Enabled = *req.Enabled
	}
	if req.FailThreshold != nil {
		if *req.FailThreshold <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "fail_threshold 必须大于 0"})
			return
		}
		item.FailThreshold = *req.FailThreshold
	}
	if req.CooldownMinutes != nil {
		if *req.CooldownMinutes <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "cooldown_minutes 必须大于 0"})
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
			if err := validateProxyURL(proxyURL); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
		}
		item.ProxyURL = proxyURL
	}


	if err := h.db.Save(&item).Error; err != nil {
		log.Printf("更新通知通道失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "服务器内部错误"})
		return
	}
	item.HasSecret = hadSecret || (req.Secret != nil && *req.Secret != "")
	maskIntegrationEndpoint(&item)
	c.JSON(http.StatusOK, gin.H{"data": item})
}

// validateProxyURL 验证代理 URL 格式
// 注意：代理地址允许 localhost/内网地址（代理通常部署在本机或 VPC 内），
// 不复用通知 endpoint 的 SSRF 校验。该字段受 RBAC("integrations:write") 保护。
func validateProxyURL(proxyURL string) error {
	parsed, err := url.Parse(proxyURL)
	if err != nil {
		return fmt.Errorf("代理地址格式不合法")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" && scheme != "socks5" && scheme != "socks5h" {
		return fmt.Errorf("代理地址仅支持 http/https/socks5 协议")
	}
	if parsed.Host == "" {
		return fmt.Errorf("代理地址缺少主机信息")
	}
	return nil
}

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
		return buildEndpointFromFields(normalizedType, botToken, chatID, "", "", "")

	case "dingtalk":
		existingToken := parseDingtalkAccessToken(existingEndpoint)
		accessToken := existingToken
		if req.AccessToken != nil {
			accessToken = strings.TrimSpace(*req.AccessToken)
		}
		if accessToken == "" {
			return "", fmt.Errorf("dingtalk 通道需要 access_token")
		}
		return buildEndpointFromFields(normalizedType, "", "", accessToken, "", "")

	case "feishu":
		existingHookID := parseFeishuHookID(existingEndpoint)
		hookID := existingHookID
		if req.HookID != nil {
			hookID = strings.TrimSpace(*req.HookID)
		}
		if hookID == "" {
			return "", fmt.Errorf("feishu 通道需要 hook_id")
		}
		return buildEndpointFromFields(normalizedType, "", "", "", hookID, "")

	case "wecom":
		existingKey := parseWecomWebhookKey(existingEndpoint)
		webhookKey := existingKey
		if req.WebhookKey != nil {
			webhookKey = strings.TrimSpace(*req.WebhookKey)
		}
		if webhookKey == "" {
			return "", fmt.Errorf("wecom 通道需要 webhook_key")
		}
		return buildEndpointFromFields(normalizedType, "", "", "", "", webhookKey)

	default:
		return "", nil
	}
}

func (h *IntegrationHandler) Delete(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	if err := h.db.Delete(&model.Integration{}, id).Error; err != nil {
		log.Printf("服务器内部错误: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "服务器内部错误"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "deleted"})
}

func (h *IntegrationHandler) Test(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}

	var item model.Integration
	if err := h.db.First(&item, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "通知通道不存在"})
		return
	}

	startedAt := time.Now()
	err := alerting.SendProbe(item)
	latency := time.Since(startedAt).Milliseconds()
	if err != nil {
		errMsg := err.Error()
		if strings.ToLower(strings.TrimSpace(item.Type)) == "telegram" {
			errMsg = util.SanitizeTelegramError(err)
		}
		c.JSON(http.StatusOK, gin.H{"data": integrationTestResponse{
			OK:        false,
			Message:   "测试发送失败: " + errMsg,
			LatencyMS: latency,
		}})
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": integrationTestResponse{
		OK:        true,
		Message:   "测试发送成功",
		LatencyMS: latency,
	}})
}

// buildEndpointFromFields 根据通道类型和结构化字段构建完整 endpoint URL
func buildEndpointFromFields(channelType, botToken, chatID, accessToken, hookID, webhookKey string) (string, error) {
	switch channelType {
	case "telegram":
		botToken = strings.TrimSpace(botToken)
		chatID = strings.TrimSpace(chatID)
		if botToken == "" || chatID == "" {
			return "", fmt.Errorf("telegram 通道需要 bot_token 和 chat_id")
		}
		if !util.BotTokenPattern().MatchString("bot" + botToken) {
			return "", fmt.Errorf("bot_token 格式不正确，应为 数字:字母数字串（如 123456:ABC-DEF）")
		}
		return fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage?chat_id=%s", botToken, url.QueryEscape(chatID)), nil

	case "dingtalk":
		accessToken = strings.TrimSpace(accessToken)
		if accessToken == "" {
			return "", fmt.Errorf("dingtalk 通道需要 access_token")
		}
		return fmt.Sprintf("https://oapi.dingtalk.com/robot/send?access_token=%s", url.QueryEscape(accessToken)), nil

	case "feishu":
		hookID = strings.TrimSpace(hookID)
		if hookID == "" {
			return "", fmt.Errorf("feishu 通道需要 hook_id")
		}
		// 校验 hookID 仅含字母数字和连字符（UUID 格式）
		for _, c := range hookID {
			if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
				return "", fmt.Errorf("feishu hook_id 格式不正确，仅允许字母、数字、连字符")
			}
		}
		return fmt.Sprintf("https://open.feishu.cn/open-apis/bot/v2/hook/%s", url.PathEscape(hookID)), nil

	case "wecom":
		webhookKey = strings.TrimSpace(webhookKey)
		if webhookKey == "" {
			return "", fmt.Errorf("wecom 通道需要 webhook_key")
		}
		return fmt.Sprintf("https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=%s", url.QueryEscape(webhookKey)), nil

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

// maskIntegrationEndpoint 对 Telegram 类型通道的 endpoint 中 bot token 进行脱敏
func maskIntegrationEndpoint(item *model.Integration) {
	if strings.ToLower(strings.TrimSpace(item.Type)) == "telegram" {
		item.Endpoint = util.MaskBotToken(item.Endpoint)
	}
}
