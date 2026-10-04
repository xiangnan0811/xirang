package handlers

import (
	"encoding/json"
	"errors"
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
	updateBody := `{"name":"FAKE_TEST_RULE_UPDATED_FOR_TEST_ONLY","description":"updated","event_type":"backup_failed","event_filter":"{\"node_id\":1}","action_type":"pause_policy","action_config":"{\"policy_id\":\"1\"}","enabled":false}`
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
	if updated.Data.EventType != "backup_failed" {
		t.Errorf("event_type 应为 backup_failed，实际 %q", updated.Data.EventType)
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
		{"retired node_offline event_type", `{"name":"r1","event_type":"node_offline","action_type":"send_notification"}`, http.StatusBadRequest},
		{"retired node_disk_high event_type", `{"name":"r1","event_type":"node_disk_high","action_type":"send_notification"}`, http.StatusBadRequest},
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
	body2 := `{"name":"FAKE_TEST_DUP_RULE_FOR_TEST_ONLY","event_type":"backup_succeeded","action_type":"pause_policy","action_config":"{}"}`

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

func TestAutomationRuleLogsSQLite(t *testing.T) {
	testAutomationRuleLogs(t, openR306DB(t, "sqlite", &model.AutomationRule{}, &model.AutomationRuleLog{}))
}

func TestAutomationRuleLogsPostgres(t *testing.T) {
	testAutomationRuleLogs(t, openR306DB(t, "postgres", &model.AutomationRule{}, &model.AutomationRuleLog{}))
}

func testAutomationRuleLogs(t *testing.T, db *gorm.DB) {
	t.Helper()
	r := gin.New()
	r.GET("/automation-rule-logs", NewAutomationRuleHandler(db).ListLogs)
	get := func(query string, status int) PaginatedResponse {
		t.Helper()
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/automation-rule-logs"+query, nil))
		if w.Code != status {
			t.Fatalf("%s: status=%d body=%s", query, w.Code, w.Body.String())
		}
		var result PaginatedResponse
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Code != status {
			t.Fatalf("envelope code=%d, HTTP=%d", result.Code, status)
		}
		if strings.Contains(w.Body.String(), "FAKE_") || strings.Contains(w.Body.String(), "automation_rule_logs") {
			t.Fatalf("unsafe response: %s", w.Body.String())
		}
		return result
	}
	rule := model.AutomationRule{Name: "FAKE_DELETED_RULE_FOR_TEST_ONLY", EventType: "backup_failed", ActionType: "trigger_task"}
	if err := db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 65; i++ {
		result := "success"
		if i%2 == 0 {
			result = "error"
		}
		row := model.AutomationRuleLog{
			RuleID: rule.ID, EventType: "backup_failed", ActionType: "trigger_task", Result: result,
			Error:   `{"password":"FAKE_PASSWORD_FOR_TEST_ONLY","token":"FAKE_TOKEN_FOR_TEST_ONLY","endpoint":"FAKE_ENDPOINT_FOR_TEST_ONLY"}`,
			Details: `{"task_id":12,"task_run_id":34,"message":"FAKE_MESSAGE_FOR_TEST_ONLY","config":"FAKE_CONFIG_FOR_TEST_ONLY","pem":"-----BEGIN PRIVATE KEY-----FAKE_PEM_FOR_TEST_ONLY"}`,
		}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Delete(&rule).Error; err != nil {
		t.Fatal(err)
	}
	first, second := get("", 200), get("?page=2", 200)
	for i, page := range []PaginatedResponse{first, second} {
		rows := page.Data.([]interface{})
		if page.Total != 65 || page.Page != i+1 || page.PageSize != 30 || len(rows) != 30 {
			t.Fatalf("page=%+v", page)
		}
		for j, value := range rows {
			row := value.(map[string]interface{})
			wantID := float64(65 - i*30 - j)
			if row["id"] != wantID || row["target_task_id"] != float64(12) || row["target_task_run_id"] != float64(34) {
				t.Fatalf("row=%v expected ID=%v", row, wantID)
			}
			if len(row) != 9 {
				t.Fatalf("DTO leaked unexpected fields: %v", row)
			}
			if row["result"] == "error" && row["error_code"] != "ACTION_FAILED" {
				t.Fatalf("missing safe error classification: %v", row)
			}
			if row["result"] == "success" && row["error_code"] != nil {
				t.Fatalf("success has error classification: %v", row)
			}
		}
	}
	filtered := get(fmt.Sprintf("?rule_id=%d&result=error&sort_order=asc", rule.ID), 200)
	if filtered.Total != 32 || filtered.Data.([]interface{})[0].(map[string]interface{})["id"] != float64(2) {
		t.Fatalf("deleted-rule/result filter: %+v", filtered)
	}
	for _, query := range []string{"?rule_id=999999", "?page=99"} {
		page := get(query, 200)
		if rows, ok := page.Data.([]interface{}); !ok || len(rows) != 0 {
			t.Fatalf("empty results must be []: %+v", page)
		}
	}
	for _, query := range []string{"?rule_id=0", "?rule_id=-1", "?rule_id=1.5", "?rule_id=abc", "?rule_id=18446744073709551616", "?result=unknown"} {
		get(query, 400)
	}
	for _, query := range []string{"?page=0&page_size=501", "?page=bad&page_size=-1"} {
		page := get(query, 200)
		if page.Page != 1 || page.PageSize != 30 {
			t.Fatalf("existing pagination defaults changed: %+v", page)
		}
	}
	if page := get("?page_size=500", 200); page.PageSize != 500 || len(page.Data.([]interface{})) != 65 {
		t.Fatalf("500 limit: %+v", page)
	}
	// The count succeeds, but the row query fails: neither failure may expose SQL.
	if err := db.Callback().Query().Before("gorm:query").Register("automation_logs_find_failure", func(tx *gorm.DB) {
		if _, ok := tx.Statement.Dest.(*[]model.AutomationRuleLog); ok {
			_ = tx.AddError(errors.New("FAKE_FIND_SQL_SECRET_FOR_TEST_ONLY"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	get("", 500)
	if err := db.Callback().Query().Remove("automation_logs_find_failure"); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropTable(&model.AutomationRuleLog{}); err != nil {
		t.Fatal(err)
	}
	get("", 500)
}

func TestAutomationRuleLogsSafeTargetsAndEnums(t *testing.T) {
	cases := []struct {
		name, details string
		want          bool
	}{
		{"valid", `{"task_id":1,"task_run_id":9007199254740991}`, true},
		{"missing pair", `{"task_id":1}`, false},
		{"zero", `{"task_id":0,"task_run_id":2}`, false},
		{"negative", `{"task_id":-1,"task_run_id":2}`, false},
		{"fraction", `{"task_id":1.5,"task_run_id":2}`, false},
		{"rounded fraction", `{"task_id":9007199254740991.1,"task_run_id":2}`, false},
		{"unsafe", `{"task_id":9007199254740992,"task_run_id":2}`, false},
		{"string", `{"task_id":"1","task_run_id":2}`, false},
		{"null", `{"task_id":null,"task_run_id":2}`, false},
		{"object", `{"task_id":{},"task_run_id":2}`, false},
		{"bad JSON", `{"task_id":1,`, false},
		{"trailing JSON", `{"task_id":1,"task_run_id":2} {}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mapAutomationRuleLog(model.AutomationRuleLog{ActionType: "trigger_task", Details: tc.details})
			if (got.TargetTaskID != nil) != tc.want || (got.TargetTaskRunID != nil) != tc.want {
				t.Fatalf("invalid target pair: %+v", got)
			}
			if tc.want && (*got.TargetTaskID != 1 || *got.TargetTaskRunID != 9007199254740991) {
				t.Fatalf("target precision lost: %+v", got)
			}
		})
	}
	got := mapAutomationRuleLog(model.AutomationRuleLog{ActionType: "FAKE_ACTION_FOR_TEST_ONLY", EventType: "FAKE_EVENT_FOR_TEST_ONLY", Result: "FAKE_RESULT_FOR_TEST_ONLY"})
	if got.ActionType != "unknown" || got.EventType != "unknown" || got.Result != "unknown" || got.ErrorCode != nil {
		t.Fatalf("untrusted enum exposed or treated as success: %+v", got)
	}
	got = mapAutomationRuleLog(model.AutomationRuleLog{ActionType: "send_notification", Details: `{"task_id":1,"task_run_id":2}`})
	if got.TargetTaskID != nil || got.TargetTaskRunID != nil {
		t.Fatal("non-trigger action exposed target IDs")
	}
}
