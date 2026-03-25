package util

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// botTokenPattern 匹配 Telegram bot token 格式: bot<数字>:<字母数字串>（完整匹配）
var botTokenPattern = regexp.MustCompile(`^bot\d+:[A-Za-z0-9_-]+$`)

// botTokenSanitizer 匹配文本中出现的 bot token（不带锚点，用于脱敏）
var botTokenSanitizer = regexp.MustCompile(`bot\d+:[A-Za-z0-9_-]+`)

// TelegramEndpointInfo 包含解析后的 Telegram endpoint 信息
type TelegramEndpointInfo struct {
	// BotSegment 是 URL 路径中的 bot<token> 段
	BotSegment string
	// ChatID 是 query 参数中的 chat_id
	ChatID string
	// Params 是完整的 query 参数（包含 chat_id、parse_mode 等）
	Params url.Values
}

// ValidateTelegramEndpoint 校验 Telegram endpoint URL 的 bot token 路径和 chat_id 参数。
// 返回解析后的信息供调用方使用。
func ValidateTelegramEndpoint(parsedURL *url.URL) (*TelegramEndpointInfo, error) {
	segments := strings.Split(strings.Trim(parsedURL.Path, "/"), "/")
	botSegment := ""
	for _, segment := range segments {
		if botTokenPattern.MatchString(segment) {
			botSegment = segment
			break
		}
	}
	if botSegment == "" {
		return nil, fmt.Errorf("telegram 通道 endpoint 必须包含 /bot<token> 路径")
	}

	params := parsedURL.Query()
	if strings.TrimSpace(params.Get("chat_id")) == "" {
		return nil, fmt.Errorf("telegram 通道 endpoint 缺少 chat_id 查询参数")
	}

	return &TelegramEndpointInfo{
		BotSegment: botSegment,
		ChatID:     params.Get("chat_id"),
		Params:     params,
	}, nil
}

// BotTokenPattern 返回 bot token 正则（完整匹配，带 bot 前缀）
func BotTokenPattern() *regexp.Regexp {
	return botTokenPattern
}

// SanitizeTelegramError 对错误消息中可能包含的 bot token 进行脱敏
func SanitizeTelegramError(err error) string {
	if err == nil {
		return ""
	}
	return botTokenSanitizer.ReplaceAllString(err.Error(), "bot***:***")
}

// SanitizeDeliveryError 根据通道类型对投递错误进行脱敏
func SanitizeDeliveryError(channelType string, err error) string {
	if err == nil {
		return ""
	}
	if strings.ToLower(strings.TrimSpace(channelType)) == "telegram" {
		return SanitizeTelegramError(err)
	}
	return err.Error()
}

// MaskBotToken 对字符串中出现的 bot token 进行掩码替换
func MaskBotToken(s string) string {
	return botTokenSanitizer.ReplaceAllString(s, "bot***:***")
}
