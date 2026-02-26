package util

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
)

func TestValidateTelegramEndpointValid(t *testing.T) {
	parsed, _ := url.Parse("https://api.telegram.org/bot123456:ABC-def/sendMessage?chat_id=-1001")
	info, err := ValidateTelegramEndpoint(parsed)
	if err != nil {
		t.Fatalf("期望合法 endpoint 校验通过，实际错误: %v", err)
	}
	if info.BotSegment != "bot123456:ABC-def" {
		t.Fatalf("期望 BotSegment=bot123456:ABC-def，实际: %s", info.BotSegment)
	}
	if info.ChatID != "-1001" {
		t.Fatalf("期望 ChatID=-1001，实际: %s", info.ChatID)
	}
}

func TestValidateTelegramEndpointMissingBotToken(t *testing.T) {
	parsed, _ := url.Parse("https://api.telegram.org/sendMessage?chat_id=1")
	_, err := ValidateTelegramEndpoint(parsed)
	if err == nil {
		t.Fatalf("期望缺少 bot token 时返回错误")
	}
}

func TestValidateTelegramEndpointInvalidBotTokenFormat(t *testing.T) {
	cases := []string{
		"https://api.telegram.org/bota/sendMessage?chat_id=1",
		"https://api.telegram.org/bot123/sendMessage?chat_id=1",
		"https://api.telegram.org/botxyz:abc/sendMessage?chat_id=1",
	}
	for _, raw := range cases {
		parsed, _ := url.Parse(raw)
		_, err := ValidateTelegramEndpoint(parsed)
		if err == nil {
			t.Fatalf("期望非法 bot token 格式 %q 返回错误", raw)
		}
	}
}

func TestValidateTelegramEndpointMissingChatID(t *testing.T) {
	parsed, _ := url.Parse("https://api.telegram.org/bot123456:ABC/sendMessage")
	_, err := ValidateTelegramEndpoint(parsed)
	if err == nil {
		t.Fatalf("期望缺少 chat_id 时返回错误")
	}
}

func TestValidateTelegramEndpointPreservesParams(t *testing.T) {
	parsed, _ := url.Parse("https://api.telegram.org/bot123456:ABC/sendMessage?chat_id=1&parse_mode=HTML")
	info, err := ValidateTelegramEndpoint(parsed)
	if err != nil {
		t.Fatalf("期望校验通过，实际错误: %v", err)
	}
	if info.Params.Get("parse_mode") != "HTML" {
		t.Fatalf("期望保留 parse_mode 参数，实际: %s", info.Params.Get("parse_mode"))
	}
}

func TestSanitizeTelegramErrorRemovesToken(t *testing.T) {
	err := fmt.Errorf(`Post "https://api.telegram.org/bot123456:ABCdef_123/sendMessage": dial tcp: lookup api.telegram.org: no such host`)
	result := SanitizeTelegramError(err)
	if result == "" {
		t.Fatalf("期望返回脱敏后的消息")
	}
	if strings.Contains(result, "ABCdef_123") {
		t.Fatalf("期望 token 被脱敏，实际: %s", result)
	}
	if !strings.Contains(result, "bot***:***") {
		t.Fatalf("期望包含脱敏占位符，实际: %s", result)
	}
}

func TestSanitizeTelegramErrorNilError(t *testing.T) {
	result := SanitizeTelegramError(nil)
	if result != "" {
		t.Fatalf("期望 nil error 返回空字符串，实际: %s", result)
	}
}

func TestSanitizeTelegramErrorNoToken(t *testing.T) {
	err := fmt.Errorf("通知发送失败: http 500")
	result := SanitizeTelegramError(err)
	if result != "通知发送失败: http 500" {
		t.Fatalf("期望无 token 时原样返回，实际: %s", result)
	}
}

func TestSanitizeDeliveryErrorTelegram(t *testing.T) {
	err := fmt.Errorf(`Post "https://api.telegram.org/bot999:XYZ/sendMessage": connection refused`)
	result := SanitizeDeliveryError("telegram", err)
	if strings.Contains(result, "XYZ") {
		t.Fatalf("期望 Telegram 类型错误被脱敏，实际: %s", result)
	}
}

func TestSanitizeDeliveryErrorNonTelegram(t *testing.T) {
	err := fmt.Errorf("webhook 发送失败")
	result := SanitizeDeliveryError("webhook", err)
	if result != "webhook 发送失败" {
		t.Fatalf("期望非 Telegram 类型原样返回，实际: %s", result)
	}
}

func TestSanitizeDeliveryErrorNilError(t *testing.T) {
	result := SanitizeDeliveryError("telegram", nil)
	if result != "" {
		t.Fatalf("期望 nil error 返回空字符串，实际: %s", result)
	}
}

func TestMaskBotToken(t *testing.T) {
	input := "https://api.telegram.org/bot123456:ABCdef/sendMessage?chat_id=1"
	result := MaskBotToken(input)
	if strings.Contains(result, "ABCdef") {
		t.Fatalf("期望 token 被掩码，实际: %s", result)
	}
	if !strings.Contains(result, "bot***:***") {
		t.Fatalf("期望包含掩码占位符，实际: %s", result)
	}
}

func TestMaskBotTokenNoToken(t *testing.T) {
	input := "https://example.com/webhook"
	result := MaskBotToken(input)
	if result != input {
		t.Fatalf("期望无 token 时原样返回，实际: %s", result)
	}
}
