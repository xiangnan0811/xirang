package integration

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"xirang/backend/internal/alerting"
	"xirang/backend/internal/apperr"
	"xirang/backend/internal/model"
	"xirang/backend/internal/util"

	"gorm.io/gorm"
)

// KnownIntegrationTypes is the set of supported notification channel types.
var KnownIntegrationTypes = map[string]bool{
	"webhook": true, "slack": true, "telegram": true, "email": true,
	"feishu": true, "dingtalk": true, "wecom": true,
}

// channelDomainHints maps channel types to their expected domain hints.
var channelDomainHints = map[string]string{
	"feishu":   "open.feishu.cn",
	"dingtalk": "oapi.dingtalk.com",
	"wecom":    "qyapi.weixin.qq.com",
	"slack":    "hooks.slack.com",
}

// IntegrationService encapsulates business logic for integration CRUD and testing.
type IntegrationService struct {
	db              *gorm.DB
	alertDispatcher *alerting.Dispatcher
}

// NewIntegrationService creates a new IntegrationService.
func NewIntegrationService(db *gorm.DB) *IntegrationService {
	return &IntegrationService{db: db}
}

func (s *IntegrationService) WithAlertDispatcher(alertDispatcher *alerting.Dispatcher) *IntegrationService {
	s.alertDispatcher = alertDispatcher
	return s
}

func (s *IntegrationService) getAlertDispatcher() *alerting.Dispatcher {
	if s.alertDispatcher != nil {
		return s.alertDispatcher
	}
	return alerting.NewDispatcher(s.db, nil, nil)
}

// DB returns the underlying database instance for simple read operations.
func (s *IntegrationService) DB() *gorm.DB {
	return s.db
}

// CreateIntegrationInput is the input payload for creating or updating an integration.
type CreateIntegrationInput struct {
	Type            string
	Name            string
	Endpoint        string
	Secret          string
	Enabled         *bool
	FailThreshold   int
	CooldownMinutes int
	ProxyURL        string
	BotToken        string
	ChatID          string
	AccessToken     string
	HookID          string
	WebhookKey      string
}

// CreateIntegration validates the input, builds the endpoint from structured
// fields, and creates a new integration in the database. It returns the created
// integration (re-fetched so AfterFind has run) or a sentinel error.
func (s *IntegrationService) CreateIntegration(ctx context.Context, input CreateIntegrationInput) (*model.Integration, error) {
	// Validate type and name
	if input.Type == "" || input.Name == "" {
		return nil, validationError("请求参数不合法")
	}
	if !KnownIntegrationTypes[input.Type] {
		return nil, validationError("不支持的通知通道类型")
	}

	// Build endpoint from structured fields if applicable
	if built, err := BuildEndpointFromFields(input.Type, input.BotToken, input.ChatID, input.AccessToken, input.HookID, input.WebhookKey); err != nil {
		return nil, validationError(err.Error())
	} else if built != "" {
		input.Endpoint = built
	}

	// Reject masked placeholder endpoints in create
	if isMaskedURL(input.Endpoint) {
		return nil, validationError("endpoint 不能使用脱敏占位符")
	}

	if input.Endpoint == "" {
		return nil, validationError("endpoint 或通道特定字段必填")
	}

	// Validate endpoint URL (SSRF checks)
	if err := ValidateIntegrationEndpoint(input.Type, input.Endpoint); err != nil {
		return nil, validationError(err.Error())
	}

	// Validate proxy URL
	if input.ProxyURL != "" {
		if isMaskedURL(input.ProxyURL) {
			return nil, validationError("代理地址不能使用脱敏占位符")
		}
		if err := ValidateProxyURL(input.ProxyURL); err != nil {
			return nil, validationError(err.Error())
		}
	}

	// Set defaults
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	if input.FailThreshold <= 0 {
		input.FailThreshold = 1
	}
	if input.CooldownMinutes <= 0 {
		input.CooldownMinutes = 5
	}

	item := model.Integration{
		Type:            input.Type,
		Name:            input.Name,
		Endpoint:        input.Endpoint,
		Secret:          input.Secret,
		Enabled:         enabled,
		FailThreshold:   input.FailThreshold,
		CooldownMinutes: input.CooldownMinutes,
		ProxyURL:        input.ProxyURL,
	}

	if err := s.db.WithContext(ctx).Create(&item).Error; err != nil {
		return nil, apperr.WrapDBError(err)
	}
	if err := s.db.WithContext(ctx).First(&item, item.ID).Error; err != nil {
		return nil, apperr.WrapDBError(err)
	}
	return &item, nil
}

// UpdateIntegration validates input, resolves masked URLs against the existing
// item, applies changes, and saves to the database. It returns the updated
// integration and whether the original item had a secret set (for response masking).
// PUT semantics: empty ProxyURL means "keep existing value"; non-empty ProxyURL
// is validated and applied.
func (s *IntegrationService) UpdateIntegration(ctx context.Context, id uint, input CreateIntegrationInput) (*model.Integration, bool, error) {
	// Validate type and name
	if input.Type == "" || input.Name == "" {
		return nil, false, validationError("请求参数不合法")
	}
	if !KnownIntegrationTypes[input.Type] {
		return nil, false, validationError("不支持的通知通道类型")
	}

	// Find existing item
	var item model.Integration
	if err := s.db.WithContext(ctx).First(&item, id).Error; err != nil {
		return nil, false, apperr.WrapDBError(err)
	}

	hadSecret := item.Secret != ""

	// Build endpoint from structured fields if applicable
	if built, err := BuildEndpointFromFields(input.Type, input.BotToken, input.ChatID, input.AccessToken, input.HookID, input.WebhookKey); err != nil {
		return nil, false, validationError(err.Error())
	} else if built != "" {
		input.Endpoint = built
	} else if isMaskedURL(input.Endpoint) {
		// Client sent back the sanitized placeholder — resolve to the real endpoint
		if !strings.EqualFold(input.Type, item.Type) || !maskedURLMatches(input.Endpoint, item.Endpoint) {
			return nil, false, validationError("endpoint 不能使用脱敏占位符")
		}
		input.Endpoint = item.Endpoint
	}

	if input.Endpoint == "" {
		return nil, false, validationError("endpoint 或通道特定字段必填")
	}

	// Validate endpoint URL (SSRF checks)
	if err := ValidateIntegrationEndpoint(input.Type, input.Endpoint); err != nil {
		return nil, false, validationError(err.Error())
	}

	// Set defaults for threshold fields
	if input.FailThreshold <= 0 {
		input.FailThreshold = item.FailThreshold
	}
	if input.CooldownMinutes <= 0 {
		input.CooldownMinutes = item.CooldownMinutes
	}

	// Resolve and validate proxy URL
	// PUT semantics: empty proxy URL = keep existing value
	if input.ProxyURL != "" {
		if isMaskedURL(input.ProxyURL) {
			// Client sent back the sanitized placeholder — resolve to existing
			if !maskedURLMatches(input.ProxyURL, item.ProxyURL) {
				return nil, false, validationError("代理地址不能使用脱敏占位符")
			}
			// Keep existing proxy — don't change
		} else {
			if err := ValidateProxyURL(input.ProxyURL); err != nil {
				return nil, false, validationError(err.Error())
			}
			item.ProxyURL = input.ProxyURL
		}
	}
	// If input.ProxyURL is empty, keep existing item.ProxyURL (PUT keep-existing semantics)

	// Apply changes
	item.Type = input.Type
	item.Name = input.Name
	item.Endpoint = input.Endpoint
	if input.Enabled != nil {
		item.Enabled = *input.Enabled
	}
	item.FailThreshold = input.FailThreshold
	item.CooldownMinutes = input.CooldownMinutes
	if input.Secret != "" {
		item.Secret = input.Secret
	}

	if err := s.db.WithContext(ctx).Save(&item).Error; err != nil {
		return nil, false, apperr.WrapDBError(err)
	}
	if err := s.db.WithContext(ctx).First(&item, item.ID).Error; err != nil {
		return nil, false, apperr.WrapDBError(err)
	}

	item.HasSecret = hadSecret || input.Secret != ""
	return &item, hadSecret, nil
}

// TestIntegrationResult holds the result of a test probe to an integration.
type TestIntegrationResult struct {
	OK        bool
	Message   string
	LatencyMS int64
}

// TestIntegration finds the integration by ID and sends a test probe through it.
func (s *IntegrationService) TestIntegration(ctx context.Context, id uint) (*TestIntegrationResult, error) {
	var item model.Integration
	if err := s.db.WithContext(ctx).First(&item, id).Error; err != nil {
		return nil, apperr.WrapDBError(err)
	}

	startedAt := time.Now()
	err := s.getAlertDispatcher().SendProbe(item)
	latency := time.Since(startedAt).Milliseconds()

	if err != nil {
		return &TestIntegrationResult{
			OK:        false,
			Message:   "测试发送失败: " + util.SanitizeError(err),
			LatencyMS: latency,
		}, nil
	}

	return &TestIntegrationResult{
		OK:        true,
		Message:   "测试发送成功",
		LatencyMS: latency,
	}, nil
}

// --- exported validation / helper functions ---

// CheckChannelDomainHint returns a domain suggestion hint for URL-based channels
// when the endpoint does not match the expected domain pattern. Returns empty
// string if no hint is needed.
func CheckChannelDomainHint(channelType, endpoint string) string {
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

// ValidateIntegrationEndpoint validates the endpoint URL for a given channel type.
// It enforces HTTP/HTTPS scheme, performs Telegram-specific validation, and
// optionally blocks private/reserved IP endpoints (SSRF protection).
func ValidateIntegrationEndpoint(channelType, endpoint string) error {
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

// ValidateProxyURL validates the proxy URL format. Unlike endpoint validation,
// proxy URLs allow localhost/private addresses (proxies are typically local/VPC).
func ValidateProxyURL(proxyURL string) error {
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

// BuildEndpointFromFields constructs a full endpoint URL from channel-specific
// structured fields. Returns empty string if the channel type does not support
// field-based construction.
func BuildEndpointFromFields(channelType, botToken, chatID, accessToken, hookID, webhookKey string) (string, error) {
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
		for _, c := range hookID {
			if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' && c != '_' {
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

// --- masked URL helpers ---

const (
	// MaskedURLPlaceholder is the text placeholder for redacted integration URLs.
	MaskedURLPlaceholder = "[redacted]"
	// MaskedURLHost is the host used in redacted integration URL placeholders.
	MaskedURLHost = "redacted.invalid"
)

// MaskIntegrationURL returns a sanitized version of the raw endpoint/proxy URL
// suitable for API responses. The returned value is either a scheme+host placeholder
// or the MaskedURLPlaceholder constant.
func MaskIntegrationURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed == nil || parsed.Scheme == "" || parsed.Host == "" {
		return MaskedURLPlaceholder
	}
	return strings.ToLower(parsed.Scheme) + "://" + MaskedURLHost
}

// isMaskedURL reports whether raw is a redacted placeholder value.
func isMaskedURL(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	if trimmed == MaskedURLPlaceholder {
		return true
	}
	parsed, err := url.Parse(trimmed)
	return err == nil && parsed != nil && strings.EqualFold(parsed.Host, MaskedURLHost) && parsed.Path == "" && parsed.RawQuery == "" && parsed.Fragment == ""
}

// maskedURLMatches reports whether a masked placeholder matches the sanitized
// version of the original URL.
func maskedURLMatches(masked, original string) bool {
	if !isMaskedURL(masked) {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(masked), MaskIntegrationURL(original))
}

// --- private helpers ---

func validationError(msg string) error {
	return fmt.Errorf("%w: %s", apperr.ErrValidation, msg)
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
