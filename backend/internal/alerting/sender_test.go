package alerting

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestFeishuSign 验证飞书签名：key=timestamp+"\n"+secret，data="" (empty)
func TestFeishuSign(t *testing.T) {
	// 用固定时间戳和密钥验证签名稳定性
	ts := int64(1700000000)
	secret := "test-feishu-secret"

	sign1, err := feishuSign(secret, ts)
	if err != nil {
		t.Fatalf("feishuSign 失败: %v", err)
	}
	sign2, err := feishuSign(secret, ts)
	if err != nil {
		t.Fatalf("feishuSign 第二次调用失败: %v", err)
	}
	if sign1 != sign2 {
		t.Errorf("相同输入应产生相同签名，got %q vs %q", sign1, sign2)
	}
	if sign1 == "" {
		t.Error("签名不能为空")
	}

	// 不同时间戳应产生不同签名
	sign3, _ := feishuSign(secret, ts+1)
	if sign1 == sign3 {
		t.Error("不同时间戳应产生不同签名")
	}
}

// TestDingtalkSign 验证钉钉签名：key=secret，data=timestamp+"\n"+secret，结果 URL encode
func TestDingtalkSign(t *testing.T) {
	ts := int64(1700000000000) // 毫秒
	secret := "test-dingtalk-secret"

	sign1, err := dingtalkSign(secret, ts)
	if err != nil {
		t.Fatalf("dingtalkSign 失败: %v", err)
	}
	sign2, err := dingtalkSign(secret, ts)
	if err != nil {
		t.Fatalf("dingtalkSign 第二次调用失败: %v", err)
	}
	if sign1 != sign2 {
		t.Errorf("相同输入应产生相同签名，got %q vs %q", sign1, sign2)
	}
	if sign1 == "" {
		t.Error("签名不能为空")
	}

	// 飞书和钉钉签名算法不同，即使 secret 相同也应该不同
	feishuSign1, _ := feishuSign(secret, 1700000000) // 秒级
	if sign1 == feishuSign1 {
		t.Error("飞书和钉钉签名算法不同，不应相同")
	}
}

// TestFeishuSenderSendsCorrectPayload 验证飞书发送器请求格式
func TestFeishuSenderSendsCorrectPayload(t *testing.T) {
	var receivedBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&receivedBody); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sender := &feishuSender{}
	body := payload{
		Severity:  "critical",
		NodeName:  "node-x",
		ErrorCode: "XR-EXEC-1",
		Message:   "执行失败",
		Triggered: time.Now(),
	}

	if err := sender.Send(http.DefaultClient, srv.URL, "", body); err != nil {
		t.Fatalf("Send 失败: %v", err)
	}
	if receivedBody["msg_type"] != "text" {
		t.Errorf("msg_type 应为 text，got %v", receivedBody["msg_type"])
	}
	// 无 secret 时不应包含签名字段
	if _, ok := receivedBody["sign"]; ok {
		t.Error("无 secret 时不应包含 sign 字段")
	}
}

// TestFeishuSenderWithSecretAddsSign 验证飞书签名字段注入
func TestFeishuSenderWithSecretAddsSign(t *testing.T) {
	var receivedBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&receivedBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sender := &feishuSender{}
	body := payload{Severity: "warning", NodeName: "node-y", ErrorCode: "XR-NODE-1", Triggered: time.Now()}

	if err := sender.Send(http.DefaultClient, srv.URL, "my-secret", body); err != nil {
		t.Fatalf("Send with secret 失败: %v", err)
	}
	if _, ok := receivedBody["sign"]; !ok {
		t.Error("有 secret 时应包含 sign 字段")
	}
	if _, ok := receivedBody["timestamp"]; !ok {
		t.Error("有 secret 时应包含 timestamp 字段")
	}
}

// TestDingtalkSenderSendsMarkdown 验证钉钉发送器使用 markdown 格式
func TestDingtalkSenderSendsMarkdown(t *testing.T) {
	var receivedBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&receivedBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sender := &dingtalkSender{}
	body := payload{Severity: "critical", NodeName: "node-z", ErrorCode: "XR-EXEC-2", Triggered: time.Now()}

	if err := sender.Send(http.DefaultClient, srv.URL, "", body); err != nil {
		t.Fatalf("Send 失败: %v", err)
	}
	if receivedBody["msgtype"] != "markdown" {
		t.Errorf("msgtype 应为 markdown，got %v", receivedBody["msgtype"])
	}
}

// TestDingtalkSenderWithSecretAppendsToURL 验证签名参数追加到 URL
func TestDingtalkSenderWithSecretAppendsToURL(t *testing.T) {
	var requestURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestURL = r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sender := &dingtalkSender{}
	body := payload{Severity: "warning", NodeName: "node-a", ErrorCode: "XR-NODE-2", Triggered: time.Now()}

	if err := sender.Send(http.DefaultClient, srv.URL+"?access_token=abc", "my-dk-secret", body); err != nil {
		t.Fatalf("Send with secret 失败: %v", err)
	}
	if requestURL == "" {
		t.Fatal("未收到请求")
	}
	if !containsParam(requestURL, "timestamp") {
		t.Errorf("URL 应包含 timestamp 参数，got: %s", requestURL)
	}
	if !containsParam(requestURL, "sign") {
		t.Errorf("URL 应包含 sign 参数，got: %s", requestURL)
	}
}

// TestWecomSenderSendsMarkdown 验证企业微信发送器使用 markdown 格式
func TestWecomSenderSendsMarkdown(t *testing.T) {
	var receivedBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&receivedBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sender := &wecomSender{}
	body := payload{Severity: "warning", NodeName: "node-b", ErrorCode: "XR-NODE-3", Triggered: time.Now()}

	if err := sender.Send(http.DefaultClient, srv.URL, "", body); err != nil {
		t.Fatalf("Send 失败: %v", err)
	}
	if receivedBody["msgtype"] != "markdown" {
		t.Errorf("msgtype 应为 markdown，got %v", receivedBody["msgtype"])
	}
}

// TestSenderRegistryContainsAllTypes 验证注册表包含全部 7 种类型
func TestSenderRegistryContainsAllTypes(t *testing.T) {
	expected := []string{"webhook", "slack", "telegram", "email", "feishu", "dingtalk", "wecom"}
	for _, typ := range expected {
		if _, ok := senderRegistry[typ]; !ok {
			t.Errorf("senderRegistry 缺少类型: %s", typ)
		}
	}
}

func containsParam(query, key string) bool {
	return len(query) > 0 && (query == key ||
		len(query) >= len(key) &&
			(query[:len(key)] == key ||
				len(query) > len(key) && (query[len(query)-len(key)-1:] == "&"+key ||
					containsSubstring(query, "&"+key+"=") ||
					containsSubstring(query, key+"="))))
}

func containsSubstring(s, sub string) bool {
	if len(sub) > len(s) {
		return false
	}
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
