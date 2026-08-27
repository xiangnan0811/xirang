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

func openAutomationTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", handlerTestDBName(t))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.AutomationRule{}, &model.AutomationRuleLog{}); err != nil {
		t.Fatalf("迁移测试表失败: %v", err)
	}
	return db
}

func TestAutomationRuleList_Empty(t *testing.T) {
	db := openAutomationTestDB(t)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "admin"); c.Next() })
	handler := NewAutomationRuleHandler(db)
	r.GET("/automation-rules", handler.List)

	req := httptest.NewRequest(http.MethodGet, "/automation-rules", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", resp.Code)
	}

	var result struct {
		Data []model.AutomationRule `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if len(result.Data) != 0 {
		t.Errorf("期望空列表，实际 %d 条", len(result.Data))
	}
}

func TestAutomationRuleCRUD(t *testing.T) {
	db := openAutomationTestDB(t)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "admin"); c.Next() })
	handler := NewAutomationRuleHandler(db)
	r.GET("/automation-rules", handler.List)
	r.POST("/automation-rules", handler.Create)
	r.GET("/automation-rules/:id", handler.Get)
	r.PUT("/automation-rules/:id", handler.Update)
	r.DELETE("/automation-rules/:id", handler.Delete)

	// Create
	body := `{"name":"FAKE_TEST_RULE_CREATE_FOR_TEST_ONLY","description":"test rule","event_type":"backup_failed","event_filter":"{}","action_type":"send_notification","action_config":"{}","enabled":true}`
	req := httptest.NewRequest(http.MethodPost, "/automation-rules", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusCreated {
		t.Fatalf("创建期望 201，实际 %d: %s", resp.Code, resp.Body.String())
	}

	var created struct {
		Data model.AutomationRule `json:"data"`
	}
	_ = json.Unmarshal(resp.Body.Bytes(), &created)
	if created.Data.ID == 0 {
		t.Fatal("创建后 ID 不应为 0")
	}
	ruleID := created.Data.ID

	// Get
	req = httptest.NewRequest(http.MethodGet, fmt.Sprintf("/automation-rules/%d", ruleID), nil)
	resp = httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("获取期望 200，实际 %d", resp.Code)
	}

	// Update
	updateBody := `{"name":"FAKE_TEST_RULE_UPDATED_FOR_TEST_ONLY","description":"updated","event_type":"node_offline","event_filter":"{\"node_id\":1}","action_type":"pause_policy","action_config":"{\"policy_id\":\"1\"}","enabled":false}`
	req = httptest.NewRequest(http.MethodPut, fmt.Sprintf("/automation-rules/%d", ruleID), strings.NewReader(updateBody))
	req.Header.Set("Content-Type", "application/json")
	resp = httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("更新期望 200，实际 %d: %s", resp.Code, resp.Body.String())
	}

	var updated struct {
		Data model.AutomationRule `json:"data"`
	}
	_ = json.Unmarshal(resp.Body.Bytes(), &updated)
	if updated.Data.Name != "FAKE_TEST_RULE_UPDATED_FOR_TEST_ONLY" {
		t.Errorf("名称应为 'FAKE_TEST_RULE_UPDATED_FOR_TEST_ONLY'，实际 %q", updated.Data.Name)
	}
	if updated.Data.EventType != "node_offline" {
		t.Errorf("event_type 应为 node_offline，实际 %q", updated.Data.EventType)
	}
	if updated.Data.ActionType != "pause_policy" {
		t.Errorf("action_type 应为 pause_policy，实际 %q", updated.Data.ActionType)
	}
	if updated.Data.Enabled {
		t.Error("enabled 应为 false")
	}

	// Delete
	req = httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/automation-rules/%d", ruleID), nil)
	resp = httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("删除期望 200，实际 %d", resp.Code)
	}

	// Get after delete
	req = httptest.NewRequest(http.MethodGet, fmt.Sprintf("/automation-rules/%d", ruleID), nil)
	resp = httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	if resp.Code != http.StatusNotFound {
		t.Errorf("删除后获取期望 404，实际 %d", resp.Code)
	}
}

func TestAutomationRuleCreateValidation(t *testing.T) {
	db := openAutomationTestDB(t)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "admin"); c.Next() })
	handler := NewAutomationRuleHandler(db)
	r.POST("/automation-rules", handler.Create)

	tests := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{"empty name", `{"event_type":"backup_failed","action_type":"send_notification"}`, http.StatusBadRequest},
		{"missing event_type", `{"name":"r1","action_type":"send_notification"}`, http.StatusBadRequest},
		{"missing action_type", `{"name":"r1","event_type":"backup_failed"}`, http.StatusBadRequest},
		{"invalid event_type", `{"name":"r1","event_type":"INVALID","action_type":"send_notification"}`, http.StatusBadRequest},
		{"invalid action_type", `{"name":"r1","event_type":"backup_failed","action_type":"INVALID"}`, http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/automation-rules", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			resp := httptest.NewRecorder()
			r.ServeHTTP(resp, req)

			if resp.Code != tt.wantStatus {
				t.Errorf("期望 %d，实际 %d: %s", tt.wantStatus, resp.Code, resp.Body.String())
			}
		})
	}
}

func TestAutomationRuleDuplicateName(t *testing.T) {
	db := openAutomationTestDB(t)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "admin"); c.Next() })
	handler := NewAutomationRuleHandler(db)
	r.POST("/automation-rules", handler.Create)

	body1 := `{"name":"FAKE_TEST_DUP_RULE_FOR_TEST_ONLY","event_type":"backup_failed","action_type":"send_notification"}`
	body2 := `{"name":"FAKE_TEST_DUP_RULE_FOR_TEST_ONLY","event_type":"node_offline","action_type":"pause_policy","action_config":"{}"}`

	req := httptest.NewRequest(http.MethodPost, "/automation-rules", strings.NewReader(body1))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	if resp.Code != http.StatusCreated {
		t.Fatalf("首次创建期望 201，实际 %d: %s", resp.Code, resp.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/automation-rules", strings.NewReader(body2))
	req.Header.Set("Content-Type", "application/json")
	resp = httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	if resp.Code != http.StatusConflict {
		t.Errorf("重复名称期望 409，实际 %d: %s", resp.Code, resp.Body.String())
	}
}

func TestAutomationRuleGetNotFound(t *testing.T) {
	db := openAutomationTestDB(t)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "admin"); c.Next() })
	handler := NewAutomationRuleHandler(db)
	r.GET("/automation-rules/:id", handler.Get)

	req := httptest.NewRequest(http.MethodGet, "/automation-rules/99999", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusNotFound {
		t.Errorf("不存在的规则期望 404，实际 %d", resp.Code)
	}
}

func TestAutomationRuleUpdateNotFound(t *testing.T) {
	db := openAutomationTestDB(t)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "admin"); c.Next() })
	handler := NewAutomationRuleHandler(db)
	r.PUT("/automation-rules/:id", handler.Update)

	body := `{"name":"r","event_type":"backup_failed","action_type":"send_notification"}`
	req := httptest.NewRequest(http.MethodPut, "/automation-rules/99999", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusNotFound {
		t.Errorf("更新不存在的规则期望 404，实际 %d", resp.Code)
	}
}
