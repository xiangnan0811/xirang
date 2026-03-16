package alerting

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

	if err := RaiseTaskFailure(db, task, nil, "执行失败"); err != nil {
		t.Fatalf("首次创建告警失败: %v", err)
	}
	if err := RaiseTaskFailure(db, task, nil, "执行失败-重复"); err != nil {
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

func TestCooldownPreventsRedelivery(t *testing.T) {
	db := openAlertingTestDB(t)
	if err := db.AutoMigrate(&model.Alert{}, &model.AlertDelivery{}, &model.Integration{}); err != nil {
		t.Fatalf("初始化告警表失败: %v", err)
	}

	// 创建通道（cooldown 5 分钟）
	channel := model.Integration{
		Type:            "webhook",
		Name:            "test-cooldown",
		Endpoint:        "http://localhost:9999/webhook",
		Enabled:         true,
		FailThreshold:   1,
		CooldownMinutes: 5,
	}
	db.Create(&channel)

	// 模拟最近一次成功投递
	delivery := model.AlertDelivery{
		AlertID:       1,
		IntegrationID: channel.ID,
		Status:        "sent",
	}
	db.Create(&delivery)

	// 验证冷却期内
	now := time.Now()
	if !inCooldown(db, channel.ID, channel.CooldownMinutes, now) {
		t.Fatalf("刚投递后应处于冷却期")
	}

	// 冷却期过后
	later := now.Add(6 * time.Minute)
	if inCooldown(db, channel.ID, channel.CooldownMinutes, later) {
		t.Fatalf("冷却期过后不应拦截")
	}
}

func TestRaiseTaskFailureStoresRunID(t *testing.T) {
	t.Setenv("ALERT_DEDUP_WINDOW", "0")

	db := openAlertingTestDB(t)
	if err := db.AutoMigrate(&model.Alert{}, &model.AlertDelivery{}, &model.Integration{}); err != nil {
		t.Fatalf("初始化告警表失败: %v", err)
	}

	task := model.Task{
		ID:     10,
		NodeID: 1,
		Node:   model.Node{Name: "node-run-id"},
	}
	runID := uint(42)

	if err := RaiseTaskFailure(db, task, &runID, "测试关联 run_id"); err != nil {
		t.Fatalf("创建告警失败: %v", err)
	}

	var alert model.Alert
	if err := db.First(&alert).Error; err != nil {
		t.Fatalf("查询告警失败: %v", err)
	}
	if alert.TaskRunID == nil || *alert.TaskRunID != runID {
		t.Fatalf("Alert.TaskRunID 期望 %d，实际 %v", runID, alert.TaskRunID)
	}
}

func TestResolveTaskAlertsOnlyAffectsOpenAndAcked(t *testing.T) {
	db := openAlertingTestDB(t)
	if err := db.AutoMigrate(&model.Alert{}, &model.AlertDelivery{}, &model.Integration{}); err != nil {
		t.Fatalf("初始化告警表失败: %v", err)
	}

	taskID := uint(5)
	// open 状态
	openAlert := model.Alert{NodeID: 1, NodeName: "n", TaskID: &taskID, Severity: "critical", Status: "open", ErrorCode: "XR-1", Message: "open", TriggeredAt: time.Now()}
	db.Create(&openAlert)
	// acked 状态
	ackedAlert := model.Alert{NodeID: 1, NodeName: "n", TaskID: &taskID, Severity: "critical", Status: "acked", ErrorCode: "XR-2", Message: "acked", TriggeredAt: time.Now()}
	db.Create(&ackedAlert)
	// resolved 状态（不应被修改）
	resolvedAlert := model.Alert{NodeID: 1, NodeName: "n", TaskID: &taskID, Severity: "critical", Status: "resolved", ErrorCode: "XR-3", Message: "already-resolved", TriggeredAt: time.Now()}
	db.Create(&resolvedAlert)

	if err := ResolveTaskAlerts(db, taskID, "已恢复"); err != nil {
		t.Fatalf("ResolveTaskAlerts 失败: %v", err)
	}

	var alerts []model.Alert
	db.Order("id asc").Find(&alerts)

	if alerts[0].Status != "resolved" {
		t.Fatalf("open 告警应变为 resolved，实际 %s", alerts[0].Status)
	}
	if alerts[1].Status != "resolved" {
		t.Fatalf("acked 告警应变为 resolved，实际 %s", alerts[1].Status)
	}
	if alerts[2].Message != "already-resolved" {
		t.Fatalf("已 resolved 的告警 message 不应被修改，实际 %s", alerts[2].Message)
	}
}

func TestRaiseVerificationFailureStoresRunID(t *testing.T) {
	t.Setenv("ALERT_DEDUP_WINDOW", "0")

	db := openAlertingTestDB(t)
	if err := db.AutoMigrate(&model.Alert{}, &model.AlertDelivery{}, &model.Integration{}); err != nil {
		t.Fatalf("初始化告警表失败: %v", err)
	}

	task := model.Task{
		ID:     20,
		NodeID: 1,
		Node:   model.Node{Name: "node-verify"},
	}
	runID := uint(99)

	if err := RaiseVerificationFailure(db, task, &runID, "校验失败"); err != nil {
		t.Fatalf("创建校验告警失败: %v", err)
	}

	var alert model.Alert
	db.First(&alert)
	if alert.Severity != "warning" {
		t.Fatalf("校验告警 severity 期望 warning，实际 %s", alert.Severity)
	}
	if alert.TaskRunID == nil || *alert.TaskRunID != runID {
		t.Fatalf("Alert.TaskRunID 期望 %d，实际 %v", runID, alert.TaskRunID)
	}
}

func openAlertingTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	return db
}
