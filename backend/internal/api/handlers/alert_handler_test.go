package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/alerting"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestAlertBulkResolveByIDs(t *testing.T) {
	db := openAlertHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Alert{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	now := time.Now()
	openAlert := model.Alert{NodeID: 1, NodeName: "node-a", Severity: "critical", Status: "open", ErrorCode: "XR-OPEN", Message: "open", Retryable: true, TriggeredAt: now}
	ackedAlert := model.Alert{NodeID: 1, NodeName: "node-a", Severity: "warning", Status: "acked", ErrorCode: "XR-ACK", Message: "acked", Retryable: true, TriggeredAt: now}
	resolvedAlert := model.Alert{NodeID: 1, NodeName: "node-a", Severity: "warning", Status: "resolved", ErrorCode: "XR-RES", Message: "resolved", Retryable: false, TriggeredAt: now}
	if err := db.Create(&[]model.Alert{openAlert, ackedAlert, resolvedAlert}).Error; err != nil {
		t.Fatalf("创建告警失败: %v", err)
	}

	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "admin"); c.Next() })
	handler := NewAlertHandler(db)
	r.POST("/alerts/bulk-resolve", handler.BulkResolve)

	body := strings.NewReader(`{"alert_ids":[1,2,3,2]}`)
	req := httptest.NewRequest(http.MethodPost, "/alerts/bulk-resolve", body)
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d，body=%s", resp.Code, resp.Body.String())
	}

	var result struct {
		Data bulkResolveAlertsResponse `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if result.Data.ResolvedCount != 2 || result.Data.SkippedCount != 1 {
		t.Fatalf("批量处理统计错误: %+v", result.Data)
	}

	var rows []model.Alert
	if err := db.Order("id asc").Find(&rows).Error; err != nil {
		t.Fatalf("查询告警失败: %v", err)
	}
	for _, row := range rows {
		if row.Status != "resolved" {
			t.Fatalf("告警 %d 应为 resolved，实际: %s", row.ID, row.Status)
		}
		if row.ID != 3 && row.Retryable {
			t.Fatalf("告警 %d retryable 应为 false", row.ID)
		}
	}
}

func TestAlertBulkResolveByNode(t *testing.T) {
	db := openAlertHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Alert{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	now := time.Now()
	alerts := []model.Alert{
		{NodeID: 7, NodeName: "node-a", Severity: "critical", Status: "open", ErrorCode: "XR-1", Message: "open", Retryable: true, TriggeredAt: now},
		{NodeID: 7, NodeName: "node-a", Severity: "warning", Status: "acked", ErrorCode: "XR-2", Message: "acked", Retryable: true, TriggeredAt: now},
		{NodeID: 7, NodeName: "node-a", Severity: "warning", Status: "resolved", ErrorCode: "XR-3", Message: "resolved", Retryable: false, TriggeredAt: now},
		{NodeID: 8, NodeName: "node-b", Severity: "warning", Status: "open", ErrorCode: "XR-4", Message: "other", Retryable: true, TriggeredAt: now},
	}
	if err := db.Create(&alerts).Error; err != nil {
		t.Fatalf("创建告警失败: %v", err)
	}

	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "admin"); c.Next() })
	handler := NewAlertHandler(db)
	r.POST("/alerts/bulk-resolve", handler.BulkResolve)

	req := httptest.NewRequest(http.MethodPost, "/alerts/bulk-resolve", strings.NewReader(`{"node_id":7}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d，body=%s", resp.Code, resp.Body.String())
	}

	var result struct {
		Data bulkResolveAlertsResponse `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if result.Data.ResolvedCount != 2 || result.Data.SkippedCount != 0 {
		t.Fatalf("节点批量处理统计错误: %+v", result.Data)
	}

	var other model.Alert
	if err := db.First(&other, alerts[3].ID).Error; err != nil {
		t.Fatalf("查询其他节点告警失败: %v", err)
	}
	if other.Status != "open" || !other.Retryable {
		t.Fatalf("其他节点告警不应被修改: %+v", other)
	}
}

func TestAlertBulkResolveRejectsUnauthorizedAlertIDs(t *testing.T) {
	db := openAlertHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Alert{}, &model.NodeOwner{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	alert := model.Alert{NodeID: 9, NodeName: "node-denied", Severity: "critical", Status: "open", ErrorCode: "XR-DENY", Message: "denied", Retryable: true, TriggeredAt: time.Now()}
	if err := db.Create(&alert).Error; err != nil {
		t.Fatalf("创建告警失败: %v", err)
	}

	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.CtxRole, "operator")
		c.Set(middleware.CtxUserID, uint(1))
		c.Next()
	})
	handler := NewAlertHandler(db)
	r.POST("/alerts/bulk-resolve", handler.BulkResolve)

	body := strings.NewReader(fmt.Sprintf(`{"alert_ids":[%d]}`, alert.ID))
	req := httptest.NewRequest(http.MethodPost, "/alerts/bulk-resolve", body)
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusForbidden {
		t.Fatalf("期望状态码 403，实际: %d，body=%s", resp.Code, resp.Body.String())
	}

	var row model.Alert
	if err := db.First(&row, alert.ID).Error; err != nil {
		t.Fatalf("查询告警失败: %v", err)
	}
	if row.Status != "open" || !row.Retryable {
		t.Fatalf("无权告警不应被修改: %+v", row)
	}
}

func TestAlertBulkResolveRejectsUnauthorizedNode(t *testing.T) {
	db := openAlertHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Alert{}, &model.NodeOwner{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	alert := model.Alert{NodeID: 10, NodeName: "node-denied", Severity: "critical", Status: "open", ErrorCode: "XR-DENY", Message: "denied", Retryable: true, TriggeredAt: time.Now()}
	if err := db.Create(&alert).Error; err != nil {
		t.Fatalf("创建告警失败: %v", err)
	}

	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.CtxRole, "operator")
		c.Set(middleware.CtxUserID, uint(1))
		c.Next()
	})
	handler := NewAlertHandler(db)
	r.POST("/alerts/bulk-resolve", handler.BulkResolve)

	req := httptest.NewRequest(http.MethodPost, "/alerts/bulk-resolve", strings.NewReader(`{"node_id":10}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusForbidden {
		t.Fatalf("期望状态码 403，实际: %d，body=%s", resp.Code, resp.Body.String())
	}

	var row model.Alert
	if err := db.First(&row, alert.ID).Error; err != nil {
		t.Fatalf("查询告警失败: %v", err)
	}
	if row.Status != "open" || !row.Retryable {
		t.Fatalf("无权节点告警不应被修改: %+v", row)
	}
}

func TestAlertEscalationEventsRejectsUnauthorizedAlert(t *testing.T) {
	db := openAlertHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Alert{}, &model.AlertEscalationEvent{}, &model.NodeOwner{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	alert := model.Alert{NodeID: 9, NodeName: "node-denied", Severity: "critical", Status: "open", ErrorCode: "XR-DENY", Message: "denied", TriggeredAt: time.Now()}
	if err := db.Create(&alert).Error; err != nil {
		t.Fatalf("创建告警失败: %v", err)
	}
	if err := db.Create(&model.AlertEscalationEvent{AlertID: alert.ID, LevelIndex: 0, SeverityBefore: "critical", SeverityAfter: "critical", FiredAt: time.Now()}).Error; err != nil {
		t.Fatalf("创建升级事件失败: %v", err)
	}

	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.CtxRole, "operator")
		c.Set(middleware.CtxUserID, uint(1))
		c.Next()
	})
	handler := NewAlertHandler(db)
	r.GET("/alerts/:id/escalation-events", handler.EscalationEvents)

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/alerts/%d/escalation-events", alert.ID), nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusForbidden {
		t.Fatalf("期望状态码 403，实际: %d，body=%s", resp.Code, resp.Body.String())
	}
}

func TestAlertDeliveries(t *testing.T) {
	db := openAlertHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Alert{}, &model.AlertDelivery{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	alert1 := model.Alert{
		NodeID:      1,
		NodeName:    "node-a",
		Severity:    "critical",
		Status:      "open",
		ErrorCode:   "XR-001",
		Message:     "backup failed",
		TriggeredAt: time.Now(),
	}
	alert2 := model.Alert{
		NodeID:      2,
		NodeName:    "node-b",
		Severity:    "warning",
		Status:      "open",
		ErrorCode:   "XR-002",
		Message:     "probe failed",
		TriggeredAt: time.Now(),
	}
	if err := db.Create(&alert1).Error; err != nil {
		t.Fatalf("创建告警1失败: %v", err)
	}
	if err := db.Create(&alert2).Error; err != nil {
		t.Fatalf("创建告警2失败: %v", err)
	}

	delivery1 := model.AlertDelivery{AlertID: alert1.ID, IntegrationID: 11, Status: "sent"}
	delivery2 := model.AlertDelivery{AlertID: alert1.ID, IntegrationID: 12, Status: "failed", LastError: "http 500"}
	delivery3 := model.AlertDelivery{AlertID: alert2.ID, IntegrationID: 13, Status: "sent"}
	if err := db.Create(&delivery1).Error; err != nil {
		t.Fatalf("创建投递1失败: %v", err)
	}
	if err := db.Create(&delivery2).Error; err != nil {
		t.Fatalf("创建投递2失败: %v", err)
	}
	if err := db.Create(&delivery3).Error; err != nil {
		t.Fatalf("创建投递3失败: %v", err)
	}

	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "admin"); c.Next() })
	handler := NewAlertHandler(db)
	r.GET("/alerts/:id/deliveries", handler.Deliveries)

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/alerts/%d/deliveries", alert1.ID), nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d", resp.Code)
	}

	var result struct {
		Data []model.AlertDelivery `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if len(result.Data) != 2 {
		t.Fatalf("投递记录数量错误，期望 2，实际: %d", len(result.Data))
	}
	if result.Data[0].ID != delivery2.ID || result.Data[1].ID != delivery1.ID {
		t.Fatalf("投递记录排序错误，实际 id 顺序: %d, %d", result.Data[0].ID, result.Data[1].ID)
	}
}

func TestAlertDeliveriesNotFound(t *testing.T) {
	db := openAlertHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Alert{}, &model.AlertDelivery{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "admin"); c.Next() })
	handler := NewAlertHandler(db)
	r.GET("/alerts/:id/deliveries", handler.Deliveries)

	req := httptest.NewRequest(http.MethodGet, "/alerts/999/deliveries", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusNotFound {
		t.Fatalf("期望状态码 404，实际: %d", resp.Code)
	}
	if !strings.Contains(resp.Body.String(), "告警不存在") {
		t.Fatalf("期望返回告警不存在，实际: %s", resp.Body.String())
	}
}

func openAlertHandlerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	secure.ResetForTesting()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", handlerTestDBName(t))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.AlertEscalationEvent{}); err != nil {
		t.Fatalf("初始化告警升级事件表失败: %v", err)
	}
	return db
}

func TestAlertRetryDeliverySuccess(t *testing.T) {
	db := openAlertHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Alert{}, &model.AlertDelivery{}, &model.Integration{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	alert := model.Alert{
		NodeID:      1,
		NodeName:    "node-a",
		Severity:    "critical",
		Status:      "open",
		ErrorCode:   "XR-001",
		Message:     "backup failed",
		TriggeredAt: time.Now(),
	}
	if err := db.Create(&alert).Error; err != nil {
		t.Fatalf("创建告警失败: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	integration := model.Integration{
		Type:            "webhook",
		Name:            "webhook-a",
		Endpoint:        server.URL,
		Enabled:         true,
		FailThreshold:   1,
		CooldownMinutes: 1,
	}
	if err := db.Create(&integration).Error; err != nil {
		t.Fatalf("创建通知通道失败: %v", err)
	}

	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "admin"); c.Next() })
	handler := NewAlertHandler(db)
	r.POST("/alerts/:id/retry-delivery", handler.RetryDelivery)

	body := strings.NewReader(fmt.Sprintf(`{"integration_id":%d}`, integration.ID))
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/alerts/%d/retry-delivery", alert.ID), body)
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d", resp.Code)
	}

	var result struct {
		Data struct {
			OK       bool                `json:"ok"`
			Message  string              `json:"message"`
			Delivery model.AlertDelivery `json:"delivery"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if !result.Data.OK {
		t.Fatalf("期望重发成功，响应: %s", resp.Body.String())
	}
	if result.Data.Delivery.Status != "sent" {
		t.Fatalf("期望 delivery 状态 sent，实际: %s", result.Data.Delivery.Status)
	}
}

func TestAlertRetryDeliveryFailed(t *testing.T) {
	db := openAlertHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Alert{}, &model.AlertDelivery{}, &model.Integration{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	alert := model.Alert{
		NodeID:      1,
		NodeName:    "node-a",
		Severity:    "critical",
		Status:      "open",
		ErrorCode:   "XR-001",
		Message:     "backup failed",
		TriggeredAt: time.Now(),
	}
	if err := db.Create(&alert).Error; err != nil {
		t.Fatalf("创建告警失败: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"ok":false}`))
	}))
	defer server.Close()

	integration := model.Integration{
		Type:            "webhook",
		Name:            "webhook-b",
		Endpoint:        server.URL,
		Enabled:         true,
		FailThreshold:   1,
		CooldownMinutes: 1,
	}
	if err := db.Create(&integration).Error; err != nil {
		t.Fatalf("创建通知通道失败: %v", err)
	}

	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "admin"); c.Next() })
	handler := NewAlertHandler(db)
	r.POST("/alerts/:id/retry-delivery", handler.RetryDelivery)

	body := strings.NewReader(fmt.Sprintf(`{"integration_id":%d}`, integration.ID))
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/alerts/%d/retry-delivery", alert.ID), body)
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d", resp.Code)
	}

	var result struct {
		Data struct {
			OK       bool                `json:"ok"`
			Message  string              `json:"message"`
			Delivery model.AlertDelivery `json:"delivery"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if result.Data.OK {
		t.Fatalf("期望重发失败，响应: %s", resp.Body.String())
	}
	if result.Data.Delivery.Status != "failed" {
		t.Fatalf("期望 delivery 状态 failed，实际: %s", result.Data.Delivery.Status)
	}
	if result.Data.Delivery.LastError == "" {
		t.Fatalf("期望记录错误信息")
	}
}

func TestAlertRetryDeliveryEscalatedWithoutIntentDoesNotCreateDirect(t *testing.T) {
	db := openAlertHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Alert{}, &model.AlertDelivery{}, &model.Integration{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	var sends int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&sends, 1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	alert := model.Alert{
		NodeID: 1, NodeName: "node-escalated-no-intent", Severity: "critical", Status: "open",
		ErrorCode: "XR-ESCALATED-NO-INTENT", Message: "waiting for escalation", TriggeredAt: time.Now(),
		DeliveryDecision: model.AlertDeliveryDecisionEscalated,
		DeliveryReason:   model.AlertDeliveryReasonEscalation,
	}
	if err := db.Create(&alert).Error; err != nil {
		t.Fatalf("创建升级告警失败: %v", err)
	}
	integration := model.Integration{
		Type: "webhook", Name: "webhook-escalated-no-intent", Endpoint: server.URL,
		Enabled: true, FailThreshold: 1, CooldownMinutes: 0,
	}
	if err := db.Create(&integration).Error; err != nil {
		t.Fatalf("创建通知通道失败: %v", err)
	}

	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "admin"); c.Next() })
	handler := NewAlertHandler(db)
	r.POST("/alerts/:id/retry-delivery", handler.RetryDelivery)
	body := strings.NewReader(fmt.Sprintf(`{"integration_id":%d}`, integration.ID))
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/alerts/%d/retry-delivery", alert.ID), body)
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusNotFound {
		t.Fatalf("无升级投递意图时应返回 404，实际: %d，body=%s", resp.Code, resp.Body.String())
	}
	if atomic.LoadInt64(&sends) != 0 {
		t.Fatalf("无升级投递意图时发送数=%d，want 0", atomic.LoadInt64(&sends))
	}
	var intentCount int64
	if err := db.Model(&model.AlertDelivery{}).Where("alert_id = ?", alert.ID).Count(&intentCount).Error; err != nil {
		t.Fatalf("查询升级投递意图失败: %v", err)
	}
	if intentCount != 0 {
		t.Fatalf("无升级投递意图时创建了 %d 条 direct 投递", intentCount)
	}
}

func TestAlertRetryFailedDeliveriesMixedResult(t *testing.T) {
	db := openAlertHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Alert{}, &model.AlertDelivery{}, &model.Integration{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	alert := model.Alert{
		NodeID:      1,
		NodeName:    "node-c",
		Severity:    "critical",
		Status:      "open",
		ErrorCode:   "XR-999",
		Message:     "backup failed",
		TriggeredAt: time.Now(),
	}
	if err := db.Create(&alert).Error; err != nil {
		t.Fatalf("创建告警失败: %v", err)
	}

	successServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer successServer.Close()

	failedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"ok":false}`))
	}))
	defer failedServer.Close()

	integrationA := model.Integration{
		Type:            "webhook",
		Name:            "webhook-success",
		Endpoint:        successServer.URL,
		Enabled:         true,
		FailThreshold:   1,
		CooldownMinutes: 1,
	}
	integrationB := model.Integration{
		Type:            "webhook",
		Name:            "webhook-failed",
		Endpoint:        failedServer.URL,
		Enabled:         true,
		FailThreshold:   1,
		CooldownMinutes: 1,
	}
	if err := db.Create(&integrationA).Error; err != nil {
		t.Fatalf("创建通知通道A失败: %v", err)
	}
	if err := db.Create(&integrationB).Error; err != nil {
		t.Fatalf("创建通知通道B失败: %v", err)
	}

	seedRecords := []model.AlertDelivery{
		{AlertID: alert.ID, IntegrationID: integrationA.ID, Status: "failed", LastError: "http 500"},
		{AlertID: alert.ID, IntegrationID: integrationB.ID, Status: "failed", LastError: "timeout"},
		{AlertID: alert.ID, IntegrationID: integrationA.ID, Status: "failed", LastError: "duplicate"},
	}
	for _, record := range seedRecords {
		if err := db.Create(&record).Error; err != nil {
			t.Fatalf("创建初始失败投递失败: %v", err)
		}
	}

	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "admin"); c.Next() })
	handler := NewAlertHandler(db)
	r.POST("/alerts/:id/retry-failed-deliveries", handler.RetryFailedDeliveries)

	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/alerts/%d/retry-failed-deliveries", alert.ID), nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d", resp.Code)
	}

	var result struct {
		Data struct {
			OK            bool                  `json:"ok"`
			TotalFailed   int                   `json:"total_failed"`
			SuccessCount  int                   `json:"success_count"`
			FailedCount   int                   `json:"failed_count"`
			NewDeliveries []model.AlertDelivery `json:"new_deliveries"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}

	if result.Data.TotalFailed != 2 {
		t.Fatalf("期望按逻辑投递 total_failed=2，实际: %d", result.Data.TotalFailed)
	}
	if result.Data.SuccessCount != 1 || result.Data.FailedCount != 1 {
		t.Fatalf("期望成功1失败1，实际 success=%d failed=%d", result.Data.SuccessCount, result.Data.FailedCount)
	}
	if result.Data.OK {
		t.Fatalf("存在失败投递时 OK 应为 false")
	}
	if len(result.Data.NewDeliveries) != 2 {
		t.Fatalf("期望每个逻辑投递记录均返回，实际: %d", len(result.Data.NewDeliveries))
	}
}

func TestAlertRetryFailedDeliveriesPreservesEventScopedRows(t *testing.T) {
	db := openAlertHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Alert{}, &model.AlertDelivery{}, &model.Integration{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	var sends int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&sends, 1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	alert := model.Alert{
		NodeID: 1, NodeName: "node-escalated", Severity: "critical", Status: "open",
		ErrorCode: "XR-ESCALATED", Message: "escalated", TriggeredAt: time.Now(),
	}
	if err := db.Create(&alert).Error; err != nil {
		t.Fatalf("创建告警失败: %v", err)
	}
	integration := model.Integration{
		Type: "webhook", Name: "webhook-escalated", Endpoint: server.URL,
		Enabled: true, FailThreshold: 1, CooldownMinutes: 0,
	}
	if err := db.Create(&integration).Error; err != nil {
		t.Fatalf("创建通知通道失败: %v", err)
	}
	legacyRecords := []model.AlertDelivery{
		{
			AlertID: alert.ID, IntegrationID: integration.ID, Status: model.AlertDeliveryStatusFailed,
			Decision: "deliver", AttemptCount: 1, LastError: "old legacy attempt",
		},
		{
			AlertID: alert.ID, IntegrationID: integration.ID, Status: model.AlertDeliveryStatusFailed,
			Decision: "deliver", AttemptCount: 1, LastError: "current legacy attempt",
		},
	}
	if err := db.Create(&legacyRecords).Error; err != nil {
		t.Fatalf("创建历史重复投递记录失败: %v", err)
	}
	records := []model.AlertDelivery{
		{
			AlertID: alert.ID, IntegrationID: integration.ID, Status: model.AlertDeliveryStatusFailed,
			Decision: "deliver", DeliveryKey: fmt.Sprintf("%d:%d:%d", alert.ID, 101, integration.ID),
			AttemptCount: 1, LastError: "level one failed",
		},
		{
			AlertID: alert.ID, IntegrationID: integration.ID, Status: model.AlertDeliveryStatusFailed,
			Decision: "deliver", DeliveryKey: fmt.Sprintf("%d:%d:%d", alert.ID, 102, integration.ID),
			AttemptCount: 1, LastError: "level two failed",
		},
	}
	if err := db.Create(&records).Error; err != nil {
		t.Fatalf("创建事件投递记录失败: %v", err)
	}

	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "admin"); c.Next() })
	handler := NewAlertHandler(db)
	r.POST("/alerts/:id/retry-failed-deliveries", handler.RetryFailedDeliveries)
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/alerts/%d/retry-failed-deliveries", alert.ID), nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d，body=%s", resp.Code, resp.Body.String())
	}
	var result struct {
		Data retryFailedDeliveriesResponse `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if result.Data.TotalFailed != 3 || result.Data.SuccessCount != 3 || result.Data.FailedCount != 0 || !result.Data.OK {
		t.Fatalf("批量重发逻辑投递统计错误: %+v", result.Data)
	}
	if len(result.Data.NewDeliveries) != 3 || atomic.LoadInt64(&sends) != 3 {
		t.Fatalf("实际逻辑投递数=%d，网络发送数=%d，want 3/3", len(result.Data.NewDeliveries), atomic.LoadInt64(&sends))
	}

	var persisted []model.AlertDelivery
	if err := db.Where("alert_id = ?", alert.ID).Order("id ASC").Find(&persisted).Error; err != nil {
		t.Fatalf("查询事件投递记录失败: %v", err)
	}
	if len(persisted) != 4 {
		t.Fatalf("事件投递记录数=%d，want 4（含历史重复），禁止创建额外 direct 记录", len(persisted))
	}
	if persisted[0].Status != model.AlertDeliveryStatusFailed || persisted[0].AttemptCount != 1 || persisted[0].DeliveryKey != "" {
		t.Fatalf("旧历史重复记录不应被重试: %+v", persisted[0])
	}
	if persisted[1].Status != model.AlertDeliveryStatusSent || persisted[1].AttemptCount != 2 ||
		persisted[1].DeliveryKey != fmt.Sprintf("%d:%d", alert.ID, integration.ID) {
		t.Fatalf("当前 legacy direct 逻辑投递错误: %+v", persisted[1])
	}
	for i, row := range persisted[2:] {
		if row.ID != records[i].ID || row.DeliveryKey != records[i].DeliveryKey {
			t.Fatalf("事件投递记录 %d 被替换或合并: got=%+v want=%+v", i, row, records[i])
		}
		if row.Status != model.AlertDeliveryStatusSent || row.AttemptCount != 2 {
			t.Fatalf("事件投递记录 %d=%+v，want sent/attempt=2", i, row)
		}
	}

	// The old blank-key duplicate remains failed, but the canonical direct
	// head is now sent and must prevent fallback to that historical attempt.
	req = httptest.NewRequest(http.MethodPost, fmt.Sprintf("/alerts/%d/retry-failed-deliveries", alert.ID), nil)
	resp = httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK || atomic.LoadInt64(&sends) != 3 {
		t.Fatalf("重复批量重发不应发送历史旧记录: status=%d sends=%d", resp.Code, atomic.LoadInt64(&sends))
	}
}

func TestAlertRetryDeliveryTargetsMostRecentExistingEventAndKeepsSentTerminal(t *testing.T) {
	db := openAlertHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Alert{}, &model.AlertDelivery{}, &model.Integration{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	var sends int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&sends, 1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	alert := model.Alert{
		NodeID: 1, NodeName: "node-escalated-single", Severity: "critical", Status: "open",
		ErrorCode: "XR-ESCALATED-SINGLE", Message: "escalated", TriggeredAt: time.Now(),
	}
	if err := db.Create(&alert).Error; err != nil {
		t.Fatalf("创建告警失败: %v", err)
	}
	integration := model.Integration{
		Type: "webhook", Name: "webhook-escalated-single", Endpoint: server.URL,
		Enabled: true, FailThreshold: 1, CooldownMinutes: 0,
	}
	if err := db.Create(&integration).Error; err != nil {
		t.Fatalf("创建通知通道失败: %v", err)
	}
	older := model.AlertDelivery{
		AlertID: alert.ID, IntegrationID: integration.ID, Status: model.AlertDeliveryStatusFailed,
		Decision: "deliver", DeliveryKey: fmt.Sprintf("%d:%d:%d", alert.ID, 201, integration.ID),
		AttemptCount: 1, LastError: "older level failed",
	}
	newer := model.AlertDelivery{
		AlertID: alert.ID, IntegrationID: integration.ID, Status: model.AlertDeliveryStatusFailed,
		Decision: "deliver", DeliveryKey: fmt.Sprintf("%d:%d:%d", alert.ID, 202, integration.ID),
		AttemptCount: 1, LastError: "newer level failed",
	}
	if err := db.Create(&older).Error; err != nil {
		t.Fatalf("创建旧事件投递记录失败: %v", err)
	}
	if err := db.Create(&newer).Error; err != nil {
		t.Fatalf("创建新事件投递记录失败: %v", err)
	}

	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "admin"); c.Next() })
	handler := NewAlertHandler(db)
	r.POST("/alerts/:id/retry-delivery", handler.RetryDelivery)
	body := strings.NewReader(fmt.Sprintf(`{"integration_id":%d}`, integration.ID))
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/alerts/%d/retry-delivery", alert.ID), body)
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("首次重发状态码=%d，body=%s", resp.Code, resp.Body.String())
	}
	var result struct {
		Data retryDeliveryResponse `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
		t.Fatalf("解析首次重发响应失败: %v", err)
	}
	if !result.Data.OK || result.Data.Delivery.ID != newer.ID || result.Data.Delivery.Status != model.AlertDeliveryStatusSent {
		t.Fatalf("首次重发未选择最新事件投递: %+v", result.Data)
	}
	if atomic.LoadInt64(&sends) != 1 {
		t.Fatalf("首次重发网络发送数=%d，want 1", atomic.LoadInt64(&sends))
	}

	req = httptest.NewRequest(http.MethodPost, fmt.Sprintf("/alerts/%d/retry-delivery", alert.ID), strings.NewReader(fmt.Sprintf(`{"integration_id":%d}`, integration.ID)))
	req.Header.Set("Content-Type", "application/json")
	resp = httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("sent 投递再次请求状态码=%d，body=%s", resp.Code, resp.Body.String())
	}
	if atomic.LoadInt64(&sends) != 1 {
		t.Fatalf("sent 投递被重复发送，网络发送数=%d", atomic.LoadInt64(&sends))
	}
	var persisted []model.AlertDelivery
	if err := db.Where("alert_id = ?", alert.ID).Order("id ASC").Find(&persisted).Error; err != nil {
		t.Fatalf("查询单通道投递记录失败: %v", err)
	}
	if len(persisted) != 2 || persisted[0].Status != model.AlertDeliveryStatusFailed ||
		persisted[1].Status != model.AlertDeliveryStatusSent || persisted[1].AttemptCount != 2 {
		t.Fatalf("单通道事件投递状态错误: %+v", persisted)
	}
}

func TestAlertDeliveryStats(t *testing.T) {
	db := openAlertHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Alert{}, &model.AlertDelivery{}, &model.Integration{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	integrationA := model.Integration{Type: "webhook", Name: "int-a", Endpoint: "https://example.com/a", Enabled: true, FailThreshold: 1, CooldownMinutes: 1}
	integrationB := model.Integration{Type: "slack", Name: "int-b", Endpoint: "https://example.com/b", Enabled: true, FailThreshold: 1, CooldownMinutes: 1}
	if err := db.Create(&integrationA).Error; err != nil {
		t.Fatalf("创建 integrationA 失败: %v", err)
	}
	if err := db.Create(&integrationB).Error; err != nil {
		t.Fatalf("创建 integrationB 失败: %v", err)
	}

	now := time.Now()
	records := []model.AlertDelivery{
		{AlertID: 1, IntegrationID: integrationA.ID, Status: "sent", CreatedAt: now.Add(-1 * time.Hour)},
		{AlertID: 1, IntegrationID: integrationA.ID, Status: "failed", CreatedAt: now.Add(-30 * time.Minute)},
		{AlertID: 1, IntegrationID: integrationB.ID, Status: "sent", CreatedAt: now.Add(-20 * time.Minute)},
		{AlertID: 1, IntegrationID: integrationB.ID, Status: "failed", CreatedAt: now.Add(-72 * time.Hour)},
	}
	for _, row := range records {
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("写入投递记录失败: %v", err)
		}
	}

	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "admin"); c.Next() })
	handler := NewAlertHandler(db)
	r.GET("/alerts/delivery-stats", handler.DeliveryStats)

	req := httptest.NewRequest(http.MethodGet, "/alerts/delivery-stats?hours=24", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d", resp.Code)
	}

	var result struct {
		Data struct {
			WindowHours   int     `json:"window_hours"`
			TotalSent     int64   `json:"total_sent"`
			TotalFailed   int64   `json:"total_failed"`
			SuccessRate   float64 `json:"success_rate"`
			ByIntegration []struct {
				IntegrationID uint   `json:"integration_id"`
				Name          string `json:"name"`
				Sent          int64  `json:"sent"`
				Failed        int64  `json:"failed"`
			} `json:"by_integration"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if result.Data.WindowHours != 24 {
		t.Fatalf("window_hours 应为 24，实际: %d", result.Data.WindowHours)
	}
	if result.Data.TotalSent != 2 || result.Data.TotalFailed != 1 {
		t.Fatalf("统计总数不正确，sent=%d failed=%d", result.Data.TotalSent, result.Data.TotalFailed)
	}
	if result.Data.SuccessRate != 66.7 {
		t.Fatalf("成功率应为 66.7，实际: %.1f", result.Data.SuccessRate)
	}
	if len(result.Data.ByIntegration) != 2 {
		t.Fatalf("按通道统计数量错误，实际: %d", len(result.Data.ByIntegration))
	}
}

func TestAlertGet_ReturnsPlainAlert_NoGroupInfo(t *testing.T) {
	db := openAlertHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Alert{}, &model.Node{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}
	a := model.Alert{
		NodeID:      1,
		NodeName:    "node-a",
		Severity:    "warning",
		Status:      "open",
		ErrorCode:   "XR-001",
		Message:     "boom",
		TriggeredAt: time.Now(),
		Tags:        "[]",
	}
	if err := db.Create(&a).Error; err != nil {
		t.Fatalf("创建告警失败: %v", err)
	}

	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "admin"); c.Next() })
	handler := NewAlertHandler(db)
	r.GET("/alerts/:id", handler.Get)

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/alerts/%d", a.ID), nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d，body=%s", resp.Code, resp.Body.String())
	}

	var result struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if _, has := result.Data["group_info"]; has {
		t.Fatalf("Get 响应不应再包含 group_info 字段，实际: %s", resp.Body.String())
	}
	if _, has := result.Data["error_code"]; !has {
		t.Fatalf("Get 响应应包含 Alert 字段，实际: %s", resp.Body.String())
	}
}

func TestAlertGroupInfo_HappyPath(t *testing.T) {
	db := openAlertHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Alert{}, &model.Node{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}
	a := model.Alert{
		NodeID:      1,
		NodeName:    "node-a",
		Severity:    "warning",
		Status:      "open",
		ErrorCode:   "XR-GRP-1",
		Message:     "grp",
		TriggeredAt: time.Now(),
		Tags:        "[]",
	}
	if err := db.Create(&a).Error; err != nil {
		t.Fatalf("创建告警失败: %v", err)
	}

	// Bump the in-memory grouping counter so we get a non-zero count.
	key := alerting.GroupKey(a.ErrorCode, a.NodeID, []string{})
	for i := 0; i < 3; i++ {
		alerting.GetSharedGrouping().ShouldSend(key, a.ID)
	}

	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "admin"); c.Next() })
	handler := NewAlertHandler(db)
	r.GET("/alerts/:id/group-info", handler.GroupInfo)

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/alerts/%d/group-info", a.ID), nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d，body=%s", resp.Code, resp.Body.String())
	}

	var result struct {
		Data AlertGroupInfo `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if result.Data.Count < 1 {
		t.Fatalf("期望 count >=1，实际: %d", result.Data.Count)
	}
}

func TestAlertGroupInfo_NotFound(t *testing.T) {
	db := openAlertHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Alert{}, &model.Node{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "admin"); c.Next() })
	handler := NewAlertHandler(db)
	r.GET("/alerts/:id/group-info", handler.GroupInfo)

	req := httptest.NewRequest(http.MethodGet, "/alerts/9999/group-info", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusNotFound {
		t.Fatalf("期望状态码 404，实际: %d", resp.Code)
	}
}
