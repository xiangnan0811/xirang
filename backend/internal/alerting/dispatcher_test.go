package alerting

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"xirang/backend/internal/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestRaiseTaskFailureDedupWindow(t *testing.T) {
	t.Setenv("ALERT_DEDUP_WINDOW", "15m")

	db := openAlertingTestDB(t)
	if err := db.AutoMigrate(&model.Alert{}, &model.AlertDelivery{}, &model.Integration{}); err != nil {
		t.Fatalf("初始化告警表失败: %v", err)
	}

	task := model.Task{
		ID:     1,
		NodeID: 2,
		Node: model.Node{
			Name: "node-a",
		},
	}

	if err := RaiseTaskFailure(db, task, "执行失败"); err != nil {
		t.Fatalf("首次创建告警失败: %v", err)
	}
	if err := RaiseTaskFailure(db, task, "执行失败-重复"); err != nil {
		t.Fatalf("重复创建告警失败: %v", err)
	}

	var count int64
	if err := db.Model(&model.Alert{}).Count(&count).Error; err != nil {
		t.Fatalf("统计告警数量失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("去重窗口内应仅保留1条告警，实际: %d", count)
	}
}

func TestSendProbeTelegramNormalizesEndpointAndUsesChatID(t *testing.T) {
	var called bool
	var gotPath string
	var gotChatID string
	var gotText string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		gotPath = r.URL.Path
		if err := r.ParseForm(); err != nil {
			t.Fatalf("解析表单失败: %v", err)
		}
		gotChatID = r.Form.Get("chat_id")
		gotText = r.Form.Get("text")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	channel := model.Integration{
		Type:     "telegram",
		Endpoint: server.URL + "/bot123456:ABC/getUpdates?chat_id=-100100",
	}
	if err := SendProbe(channel); err != nil {
		t.Fatalf("期望测试发送成功，实际错误: %v", err)
	}
	if !called {
		t.Fatalf("期望触发 Telegram 请求")
	}
	if gotPath != "/bot123456:ABC/sendMessage" {
		t.Fatalf("期望自动归一化到 sendMessage，实际路径: %s", gotPath)
	}
	if gotChatID != "-100100" {
		t.Fatalf("期望携带 chat_id，实际: %s", gotChatID)
	}
	if !strings.Contains(gotText, "XiRang") {
		t.Fatalf("期望消息正文包含 XiRang，实际: %s", gotText)
	}
}

func TestSendProbeTelegramRequiresChatID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	channel := model.Integration{
		Type:     "telegram",
		Endpoint: server.URL + "/bot123456:ABC/sendMessage",
	}
	err := SendProbe(channel)
	if err == nil {
		t.Fatalf("期望缺少 chat_id 时返回错误")
	}
	if !strings.Contains(err.Error(), "chat_id") {
		t.Fatalf("期望错误提示包含 chat_id，实际: %v", err)
	}
}

func TestSendProbeTelegramReturnsDescriptionOnHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"ok":false,"description":"Not Found"}`))
	}))
	defer server.Close()

	channel := model.Integration{
		Type:     "telegram",
		Endpoint: server.URL + "/bot123456:ABC/sendMessage?chat_id=123",
	}
	err := SendProbe(channel)
	if err == nil {
		t.Fatalf("期望 HTTP 404 时返回错误")
	}
	if !strings.Contains(err.Error(), "http 404") {
		t.Fatalf("期望错误提示包含 http 状态码，实际: %v", err)
	}
	if !strings.Contains(err.Error(), "Not Found") {
		t.Fatalf("期望错误提示包含响应描述，实际: %v", err)
	}
}

func TestSendProbeTelegramForwardsOptionalParams(t *testing.T) {
	var gotParseMode string
	var gotDisablePreview string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("解析表单失败: %v", err)
		}
		gotParseMode = r.Form.Get("parse_mode")
		gotDisablePreview = r.Form.Get("disable_web_page_preview")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	channel := model.Integration{
		Type:     "telegram",
		Endpoint: server.URL + "/bot123456:ABC/sendMessage?chat_id=-100&parse_mode=HTML&disable_web_page_preview=true",
	}
	if err := SendProbe(channel); err != nil {
		t.Fatalf("期望发送成功，实际错误: %v", err)
	}
	if gotParseMode != "HTML" {
		t.Fatalf("期望 parse_mode=HTML，实际: %s", gotParseMode)
	}
	if gotDisablePreview != "true" {
		t.Fatalf("期望 disable_web_page_preview=true，实际: %s", gotDisablePreview)
	}
}

func openAlertingTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	return db
}
