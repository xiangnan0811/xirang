package alerting

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Sender 通知发送器接口
type Sender interface {
	Send(endpoint, secret string, body payload) error
}

// senderRegistry 按通道类型注册发送器
var senderRegistry = map[string]Sender{
	"webhook":  &webhookSender{},
	"slack":    &slackSender{},
	"telegram": &telegramSender{},
	"email":    &emailSender{},
	"feishu":   &feishuSender{},
	"dingtalk": &dingtalkSender{},
	"wecom":    &wecomSender{},
}

// --- webhook ---

type webhookSender struct{}

func (s *webhookSender) Send(endpoint, _ string, body payload) error {
	return postJSON(endpoint, body)
}

// --- slack ---

type slackSender struct{}

func (s *slackSender) Send(endpoint, _ string, body payload) error {
	return postJSON(endpoint, map[string]string{
		"text": fmt.Sprintf("[XiRang][%s] %s (%s)", strings.ToUpper(body.Severity), body.Message, body.ErrorCode),
	})
}

// --- telegram ---

type telegramSender struct{}

func (s *telegramSender) Send(endpoint, _ string, body payload) error {
	text := fmt.Sprintf("[XiRang][%s]\n节点: %s\n错误: %s\n说明: %s",
		strings.ToUpper(body.Severity), body.NodeName, body.ErrorCode, body.Message)
	return postTelegram(endpoint, text)
}

// --- email ---

type emailSender struct{}

func (s *emailSender) Send(endpoint, _ string, body payload) error {
	subject := fmt.Sprintf("[XiRang][%s] %s", strings.ToUpper(body.Severity), body.ErrorCode)
	content := fmt.Sprintf("节点: %s\n策略: %s\n错误码: %s\n详情: %s\n时间: %s\n",
		body.NodeName, body.PolicyName, body.ErrorCode, body.Message, body.Triggered.Format(time.RFC3339))
	return sendEmail(endpoint, subject, content)
}

// --- 飞书 ---

type feishuSender struct{}

// feishuSign 计算飞书签名：HMAC-SHA256(key=timestamp+"\n"+secret, data="")
func feishuSign(secret string, timestampSec int64) (string, error) {
	key := fmt.Sprintf("%d\n%s", timestampSec, secret)
	mac := hmac.New(sha256.New, []byte(key))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (s *feishuSender) Send(endpoint, secret string, body payload) error {
	text := fmt.Sprintf("[XiRang][%s]\n节点: %s\n错误码: %s\n说明: %s\n时间: %s",
		strings.ToUpper(body.Severity), body.NodeName, body.ErrorCode, body.Message,
		body.Triggered.Format("2006-01-02 15:04:05"))

	msg := map[string]interface{}{
		"msg_type": "text",
		"content": map[string]string{
			"text": text,
		},
	}

	if secret != "" {
		ts := time.Now().Unix()
		sign, err := feishuSign(secret, ts)
		if err != nil {
			return fmt.Errorf("飞书签名计算失败: %w", err)
		}
		msg["timestamp"] = fmt.Sprintf("%d", ts)
		msg["sign"] = sign
	}

	return postJSON(endpoint, msg)
}

// --- 钉钉 ---

type dingtalkSender struct{}

// dingtalkSign 计算钉钉签名：HMAC-SHA256(key=secret, data=timestamp+"\n"+secret)，结果 URL encode
func dingtalkSign(secret string, timestampMs int64) (string, error) {
	data := fmt.Sprintf("%d\n%s", timestampMs, secret)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(data))
	return url.QueryEscape(base64.StdEncoding.EncodeToString(mac.Sum(nil))), nil
}

func (s *dingtalkSender) Send(endpoint, secret string, body payload) error {
	endpointURL := endpoint
	if secret != "" {
		ts := time.Now().UnixMilli()
		sign, err := dingtalkSign(secret, ts)
		if err != nil {
			return fmt.Errorf("钉钉签名计算失败: %w", err)
		}
		endpointURL = fmt.Sprintf("%s&timestamp=%d&sign=%s", endpoint, ts, sign)
	}

	text := fmt.Sprintf("**[XiRang 告警][%s]**\n\n节点: %s\n错误码: %s\n说明: %s\n时间: %s",
		strings.ToUpper(body.Severity), body.NodeName, body.ErrorCode, body.Message,
		body.Triggered.Format("2006-01-02 15:04:05"))

	msg := map[string]interface{}{
		"msgtype": "markdown",
		"markdown": map[string]string{
			"title": fmt.Sprintf("XiRang 告警 [%s]", strings.ToUpper(body.Severity)),
			"text":  text,
		},
	}
	return postJSON(endpointURL, msg)
}

// --- 企业微信 ---

type wecomSender struct{}

func (s *wecomSender) Send(endpoint, _ string, body payload) error {
	text := fmt.Sprintf("> **[XiRang 告警][%s]**\n> 节点: %s\n> 错误码: <font color=\"warning\">%s</font>\n> 说明: %s\n> 时间: %s",
		strings.ToUpper(body.Severity), body.NodeName, body.ErrorCode, body.Message,
		body.Triggered.Format("2006-01-02 15:04:05"))

	msg := map[string]interface{}{
		"msgtype": "markdown",
		"markdown": map[string]string{
			"content": text,
		},
	}
	return postJSON(endpoint, msg)
}
