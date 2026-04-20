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

func TestDispatch_FailedSendIsMarkedRetrying(t *testing.T) {
	t.Setenv("ALERT_DEDUP_WINDOW", "0")
	db := setupTestDB(t)

	// FailThreshold=1: 至少 1 个 open 告警才触发投递；Status 明确设为 "open"
	db.Create(&model.Integration{Name: "wh", Type: "webhook", Enabled: true, Endpoint: "http://127.0.0.1:1", FailThreshold: 1})

	alert := &model.Alert{
		NodeID:      1,
		NodeName:    "node-a",
		ErrorCode:   "probe_down",
		Severity:    "warn",
		Status:      "open",
		Message:     "probe failed",
		TriggeredAt: time.Now(),
	}
	if err := raiseAndDispatch(db, alert); err != nil {
		t.Fatal(err)
	}

	// raiseAndDispatch 内部以 wg.Wait() 同步，无需额外等待

	var d model.AlertDelivery
	if err := db.Where("alert_id = ?", alert.ID).First(&d).Error; err != nil {
		t.Fatalf("delivery row not found (alert_id=%d): %v", alert.ID, err)
	}
	if d.Status != "retrying" {
		t.Fatalf("expected retrying, got %q (last_error=%q)", d.Status, d.LastError)
	}
	if d.AttemptCount != 1 {
		t.Fatalf("expected attempts=1, got %d", d.AttemptCount)
	}
	if d.NextRetryAt == nil {
		t.Fatal("expected NextRetryAt set")
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

// setupTestDB 初始化包含静默规则所需全部表的测试数据库
func setupTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := openAlertingTestDB(t)
	if err := db.AutoMigrate(
		&model.Alert{},
		&model.AlertDelivery{},
		&model.Integration{},
		&model.Node{},
		&model.Silence{},
	); err != nil {
		t.Fatalf("初始化测试表失败: %v", err)
	}
	return db
}

func TestDispatch_SilencedAlertIsNotDelivered(t *testing.T) {
	t.Setenv("ALERT_DEDUP_WINDOW", "0")
	db := setupTestDB(t)
	now := time.Now()

	// 创建节点
	node := model.Node{ID: 1, Name: "node-a", Host: "1.2.3.4", Port: 22, Tags: "prod"}
	db.Create(&node)

	// 创建匹配 node 1 + error_code "probe_down" 的静默规则
	nodeID := uint(1)
	db.Create(&model.Silence{
		Name:          "maint",
		MatchNodeID:   &nodeID,
		MatchCategory: "probe_down",
		StartsAt:      now.Add(-time.Hour),
		EndsAt:        now.Add(time.Hour),
		CreatedBy:     1,
	})

	// 创建通道（使用本地 httptest server，快速响应）
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	db.Create(&model.Integration{Name: "wh", Type: "webhook", Enabled: true, Endpoint: srv.URL})

	// 直接调用内部函数（同包内测试可访问），不预先创建 alert
	alert := model.Alert{
		NodeID:      1,
		NodeName:    "node-a",
		ErrorCode:   "probe_down",
		Severity:    "warn",
		Status:      "open",
		Message:     "probe failed",
		TriggeredAt: now,
	}
	_ = raiseAndDispatch(db, &alert)

	// alert 由 raiseAndDispatch 写入，ID 已被填充
	var count int64
	db.Model(&model.AlertDelivery{}).Count(&count)
	if count != 0 {
		t.Fatalf("期望 0 条投递记录（已静默），实际 %d", count)
	}
}

func TestDispatch_SecondAlertInGroupWindowIsSuppressed(t *testing.T) {
	t.Setenv("ALERT_DEDUP_WINDOW", "0")
	db := setupTestDB(t)
	db.Create(&model.Node{ID: 1, Name: "node-a"})
	// FailThreshold=1：openCount 需 >= 1 才触发投递（与其他 dispatcher 测试保持一致）
	db.Create(&model.Integration{Name: "wh", Type: "webhook", Enabled: true, Endpoint: "http://127.0.0.1:1", FailThreshold: 1})

	// 重置包级分组状态，确保测试幂等
	SetSharedGroupingForTest(NewGrouping(5 * time.Minute))
	t.Cleanup(func() { SetSharedGroupingForTest(NewGrouping(5 * time.Minute)) })

	// Status 必须为 "open"，openCount 查询才能命中并满足 FailThreshold>=1
	alert1 := &model.Alert{NodeID: 1, NodeName: "node-a", ErrorCode: "probe_down", Severity: "warn", Status: "open", Message: "first", TriggeredAt: time.Now()}
	if err := raiseAndDispatch(db, alert1); err != nil {
		t.Fatal(err)
	}

	alert2 := &model.Alert{NodeID: 1, NodeName: "node-a", ErrorCode: "probe_down", Severity: "warn", Status: "open", Message: "second", TriggeredAt: time.Now()}
	if err := raiseAndDispatch(db, alert2); err != nil {
		t.Fatal(err)
	}

	var deliveries int64
	db.Model(&model.AlertDelivery{}).Count(&deliveries)
	if deliveries != 1 {
		t.Fatalf("expected 1 delivery (second grouped-out), got %d", deliveries)
	}
}

func TestDispatch_NonMatchingSilenceDoesNotBlock(t *testing.T) {
	t.Setenv("ALERT_DEDUP_WINDOW", "0")
	db := setupTestDB(t)
	now := time.Now()

	// 静默规则指向另一个节点（999），不应匹配 node 1
	otherNode := uint(999)
	db.Create(&model.Silence{
		Name:        "other",
		MatchNodeID: &otherNode,
		StartsAt:    now.Add(-time.Hour),
		EndsAt:      now.Add(time.Hour),
		CreatedBy:   1,
	})

	node := model.Node{ID: 1, Name: "node-a", Host: "1.2.3.4", Port: 22}
	db.Create(&node)

	// 使用本地 httptest server，确保 webhook 调用能快速完成
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	db.Create(&model.Integration{Name: "wh", Type: "webhook", Enabled: true, Endpoint: srv.URL})

	alert := model.Alert{
		NodeID:      1,
		ErrorCode:   "probe_down",
		Severity:    "warn",
		Status:      "open",
		TriggeredAt: now,
	}
	_ = raiseAndDispatch(db, &alert)

	var count int64
	db.Model(&model.AlertDelivery{}).Count(&count)
	if count == 0 {
		t.Fatal("期望至少 1 条投递记录（静默未命中，不应拦截）")
	}
}
