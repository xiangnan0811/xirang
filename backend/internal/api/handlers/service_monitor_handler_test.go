package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openServiceMonitorTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", handlerTestDBName(t))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.ServiceMonitor{}); err != nil {
		t.Fatalf("迁移测试表失败: %v", err)
	}
	return db
}

func TestServiceMonitorList_Empty(t *testing.T) {
	db := openServiceMonitorTestDB(t)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "admin"); c.Next() })
	handler := NewServiceMonitorHandler(db)
	r.GET("/service-monitors", handler.List)

	req := httptest.NewRequest(http.MethodGet, "/service-monitors", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", resp.Code)
	}

	var result struct {
		Data []model.ServiceMonitor `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if len(result.Data) != 0 {
		t.Errorf("期望空列表，实际 %d 条", len(result.Data))
	}
}

func TestServiceMonitorCRUD(t *testing.T) {
	db := openServiceMonitorTestDB(t)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "admin"); c.Next() })
	handler := NewServiceMonitorHandler(db)
	r.GET("/service-monitors", handler.List)
	r.POST("/service-monitors", handler.Create)
	r.GET("/service-monitors/:id", handler.Get)
	r.PUT("/service-monitors/:id", handler.Update)
	r.DELETE("/service-monitors/:id", handler.Delete)

	// Create HTTP monitor.
	body := `{"name":"FAKE_TEST_HTTP_FOR_TEST_ONLY","type":"http","target":"https://example.com","interval_seconds":30,"timeout_seconds":5,"http_method":"GET","http_expected_status":200,"enabled":true}`
	req := httptest.NewRequest(http.MethodPost, "/service-monitors", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusCreated {
		t.Fatalf("创建期望 201，实际 %d: %s", resp.Code, resp.Body.String())
	}

	var created struct {
		Data model.ServiceMonitor `json:"data"`
	}
	_ = json.Unmarshal(resp.Body.Bytes(), &created)
	if created.Data.ID == 0 {
		t.Fatal("创建后 ID 不应为 0")
	}
	if created.Data.Type != "http" {
		t.Errorf("期望 type=http，实际 %q", created.Data.Type)
	}
	if created.Data.LastStatus != "unknown" {
		t.Errorf("期望初始 last_status=unknown，实际 %q", created.Data.LastStatus)
	}
	monitorID := created.Data.ID

	// Get.
	req = httptest.NewRequest(http.MethodGet, fmt.Sprintf("/service-monitors/%d", monitorID), nil)
	resp = httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("获取期望 200，实际 %d", resp.Code)
	}

	// Update.
	updateBody := `{"name":"FAKE_TEST_TCP_UPDATED_FOR_TEST_ONLY","type":"tcp","target":"192.168.1.1:443","interval_seconds":120,"timeout_seconds":15,"enabled":false}`
	req = httptest.NewRequest(http.MethodPut, fmt.Sprintf("/service-monitors/%d", monitorID), strings.NewReader(updateBody))
	req.Header.Set("Content-Type", "application/json")
	resp = httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("更新期望 200，实际 %d: %s", resp.Code, resp.Body.String())
	}

	var updated struct {
		Data model.ServiceMonitor `json:"data"`
	}
	_ = json.Unmarshal(resp.Body.Bytes(), &updated)
	if updated.Data.Name != "FAKE_TEST_TCP_UPDATED_FOR_TEST_ONLY" {
		t.Errorf("名称应为 'FAKE_TEST_TCP_UPDATED_FOR_TEST_ONLY'，实际 %q", updated.Data.Name)
	}
	if updated.Data.Type != "tcp" {
		t.Errorf("type 应为 tcp，实际 %q", updated.Data.Type)
	}
	if updated.Data.Target != "192.168.1.1:443" {
		t.Errorf("target 应为 '192.168.1.1:443'，实际 %q", updated.Data.Target)
	}
	if updated.Data.IntervalSeconds != 120 {
		t.Errorf("interval_seconds 应为 120，实际 %d", updated.Data.IntervalSeconds)
	}
	if updated.Data.Enabled {
		t.Error("enabled 应为 false")
	}

	// Delete.
	req = httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/service-monitors/%d", monitorID), nil)
	resp = httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("删除期望 200，实际 %d", resp.Code)
	}

	// Get after delete.
	req = httptest.NewRequest(http.MethodGet, fmt.Sprintf("/service-monitors/%d", monitorID), nil)
	resp = httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	if resp.Code != http.StatusNotFound {
		t.Errorf("删除后获取期望 404，实际 %d", resp.Code)
	}
}

func TestServiceMonitorCreateValidation(t *testing.T) {
	db := openServiceMonitorTestDB(t)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "admin"); c.Next() })
	handler := NewServiceMonitorHandler(db)
	r.POST("/service-monitors", handler.Create)

	tests := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{"empty name", `{"type":"http","target":"https://example.com"}`, http.StatusBadRequest},
		{"missing type", `{"name":"svc1","target":"https://example.com"}`, http.StatusBadRequest},
		{"missing target", `{"name":"svc1","type":"http"}`, http.StatusBadRequest},
		{"invalid type", `{"name":"svc1","type":"invalid","target":"https://example.com"}`, http.StatusBadRequest},
		{"invalid http_method", `{"name":"svc1","type":"http","target":"https://example.com","http_method":"DELETE"}`, http.StatusBadRequest},
		{"invalid json headers", `{"name":"svc1","type":"http","target":"https://example.com","http_headers":"not-json"}`, http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/service-monitors", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			resp := httptest.NewRecorder()
			r.ServeHTTP(resp, req)

			if resp.Code != tt.wantStatus {
				t.Errorf("期望 %d，实际 %d: %s", tt.wantStatus, resp.Code, resp.Body.String())
			}
		})
	}
}

func TestServiceMonitorDuplicateName(t *testing.T) {
	db := openServiceMonitorTestDB(t)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "admin"); c.Next() })
	handler := NewServiceMonitorHandler(db)
	r.POST("/service-monitors", handler.Create)

	body1 := `{"name":"FAKE_TEST_DUP_MON_FOR_TEST_ONLY","type":"http","target":"https://example.com"}`
	body2 := `{"name":"FAKE_TEST_DUP_MON_FOR_TEST_ONLY","type":"tcp","target":"192.168.1.1:80"}`

	req := httptest.NewRequest(http.MethodPost, "/service-monitors", strings.NewReader(body1))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	if resp.Code != http.StatusCreated {
		t.Fatalf("首次创建期望 201，实际 %d: %s", resp.Code, resp.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/service-monitors", strings.NewReader(body2))
	req.Header.Set("Content-Type", "application/json")
	resp = httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	if resp.Code != http.StatusConflict {
		t.Errorf("重复名称期望 409，实际 %d: %s", resp.Code, resp.Body.String())
	}
}

func TestServiceMonitorGetNotFound(t *testing.T) {
	db := openServiceMonitorTestDB(t)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "admin"); c.Next() })
	handler := NewServiceMonitorHandler(db)
	r.GET("/service-monitors/:id", handler.Get)

	req := httptest.NewRequest(http.MethodGet, "/service-monitors/99999", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusNotFound {
		t.Errorf("不存在的监控期望 404，实际 %d", resp.Code)
	}
}

func TestServiceMonitorUpdateNotFound(t *testing.T) {
	db := openServiceMonitorTestDB(t)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "admin"); c.Next() })
	handler := NewServiceMonitorHandler(db)
	r.PUT("/service-monitors/:id", handler.Update)

	body := `{"name":"r","type":"http","target":"https://example.com"}`
	req := httptest.NewRequest(http.MethodPut, "/service-monitors/99999", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusNotFound {
		t.Errorf("更新不存在的监控期望 404，实际 %d", resp.Code)
	}
}

func TestServiceMonitorStatusPage(t *testing.T) {
	db := openServiceMonitorTestDB(t)
	r := gin.New()
	handler := NewServiceMonitorHandler(db)

	// Seed monitors.
	db.Create(&model.ServiceMonitor{
		Name: "enabled-http", Type: "http", Target: "https://example.com", Enabled: true,
		LastStatus: "up", UptimePct: 99.5,
	})
	db.Create(&model.ServiceMonitor{
		Name: "enabled-tcp", Type: "tcp", Target: "192.168.1.1:443", Enabled: true,
		LastStatus: "down", UptimePct: 50.0,
	})
	db.Create(&model.ServiceMonitor{
		Name: "disabled", Type: "http", Target: "https://disabled.local", Enabled: false,
		LastStatus: "unknown", UptimePct: 0,
	})
	// GORM omits zero-value bool on INSERT, so explicitly set enabled false.
	db.Model(&model.ServiceMonitor{}).Where("name = ?", "disabled").Update("enabled", false)

	r.GET("/api/v1/status-page", handler.StatusPage)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/status-page", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status-page 期望 200，实际 %d: %s", resp.Code, resp.Body.String())
	}

	var result struct {
		Data []struct {
			Name      string  `json:"name"`
			Type      string  `json:"type"`
			Status    string  `json:"status"`
			UptimePct float64 `json:"uptime_pct"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}

	if len(result.Data) != 2 {
		t.Fatalf("status-page 应只返回 enabled 的 monitor，期望 2 条，实际 %d", len(result.Data))
	}
	if result.Data[0].Name != "enabled-http" {
		t.Errorf("第 1 条期望 enabled-http，实际 %q", result.Data[0].Name)
	}
	if result.Data[1].Name != "enabled-tcp" {
		t.Errorf("第 2 条期望 enabled-tcp，实际 %q", result.Data[1].Name)
	}
}
