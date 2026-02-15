package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

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
	delivery2 := model.AlertDelivery{AlertID: alert1.ID, IntegrationID: 12, Status: "failed", Error: "http 500"}
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
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
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
	if result.Data.Delivery.Error == "" {
		t.Fatalf("期望记录错误信息")
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
		{AlertID: alert.ID, IntegrationID: integrationA.ID, Status: "failed", Error: "http 500"},
		{AlertID: alert.ID, IntegrationID: integrationB.ID, Status: "failed", Error: "timeout"},
		{AlertID: alert.ID, IntegrationID: integrationA.ID, Status: "failed", Error: "duplicate"},
	}
	for _, record := range seedRecords {
		if err := db.Create(&record).Error; err != nil {
			t.Fatalf("创建初始失败投递失败: %v", err)
		}
	}

	r := gin.New()
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
		t.Fatalf("期望去重后 total_failed=2，实际: %d", result.Data.TotalFailed)
	}
	if result.Data.SuccessCount != 1 || result.Data.FailedCount != 1 {
		t.Fatalf("期望成功1失败1，实际 success=%d failed=%d", result.Data.SuccessCount, result.Data.FailedCount)
	}
	if result.Data.OK {
		t.Fatalf("存在失败投递时 OK 应为 false")
	}
	if len(result.Data.NewDeliveries) != 2 {
		t.Fatalf("期望新投递记录为 2 条，实际: %d", len(result.Data.NewDeliveries))
	}
}


func TestAlertDeliveryStats(t *testing.T) {
	db := openAlertHandlerTestDB(t)
	if err := db.AutoMigrate(&model.AlertDelivery{}, &model.Integration{}); err != nil {
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
			WindowHours int `json:"window_hours"`
			TotalSent int64 `json:"total_sent"`
			TotalFailed int64 `json:"total_failed"`
			SuccessRate float64 `json:"success_rate"`
			ByIntegration []struct {
				IntegrationID uint `json:"integration_id"`
				Name string `json:"name"`
				Sent int64 `json:"sent"`
				Failed int64 `json:"failed"`
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
