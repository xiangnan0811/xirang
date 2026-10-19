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
			assertAutomationRuleLogResponseFields(t, row)
			if row["metadata_version"] != nil || row["execution_stage"] != "unknown" || row["reason_code"] != nil {
				t.Fatalf("legacy log invented execution metadata: %v", row)
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
	t.Run("safe execution boundaries", func(t *testing.T) {
		testAutomationRuleLogsSafeExecution(t, db)
	})
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

func assertAutomationRuleLogResponseFields(t *testing.T, row map[string]interface{}) {
	t.Helper()
	allowed := map[string]bool{
		"id": true, "rule_id": true, "event_type": true, "action_type": true,
		"result": true, "created_at": true, "error_code": true,
		"target_task_id": true, "target_task_run_id": true,
		"metadata_version": true, "execution_stage": true, "reason_code": true,
		"source_node_id": true, "source_policy_id": true,
		"source_task_id": true, "source_task_run_id": true, "target_policy_id": true,
	}
	for key := range row {
		if !allowed[key] {
			t.Fatalf("DTO exposed non-allowlisted field %q", key)
		}
	}
}

// Both engine entrypoints exercise the HTTP projection of persisted, untrusted
// Details. No raw diagnostic or arbitrary nested field may reach the response.
func testAutomationRuleLogsSafeExecution(t *testing.T, db *gorm.DB) {
	t.Helper()
	rule := model.AutomationRule{Name: "safe execution boundary fixtures", EventType: "backup_failed", ActionType: "trigger_task"}
	if err := db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	r.GET("/automation-rule-logs", NewAutomationRuleHandler(db).ListLogs)
	wrap := func(object string) string {
		return `{"task_id":41,"task_run_id":42,"message":"FAKE_NOTIFICATION_MESSAGE","url":"https://FAKE_PRIVATE_URL.invalid/?token=FAKE_URL_TOKEN","safe_execution":` + object + `}`
	}
	metadata := func(stage, extra string) string {
		return `{"version":1,"stage":"` + stage + `"` + extra + `}`
	}
	sources := `,"source_node_id":11,"source_policy_id":12,"source_task_id":13,"source_task_run_id":14,"target_policy_id":15`
	backupIDs := map[string]string{
		"source_node_id": "11", "source_policy_id": "12",
		"source_task_id": "13", "source_task_run_id": "14",
	}
	type scenario struct {
		name, event, action, result, details string
		version                              bool
		stage, reason                        string
		ids                                  map[string]string
		noTargets                            bool
	}
	cases := []scenario{
		{name: "legacy top-level targets do not invent sources", details: `{"task_id":41,"task_run_id":42,"source_task_id":13,"source_task_run_id":14}`},
		{name: "valid distinct source and action target", details: wrap(metadata("task_run_reserved", sources)), version: true, stage: "task_run_reserved", ids: backupIDs},
		{name: "unknown version", details: wrap(`{"version":2,"stage":"task_run_reserved"` + sources + `}`)},
		{name: "missing version", details: wrap(`{"stage":"task_run_reserved"` + sources + `}`)},
		{name: "string version", details: wrap(`{"version":"1","stage":"task_run_reserved"}`)},
		{name: "fractional version", details: wrap(`{"version":1.0,"stage":"task_run_reserved"}`)},
		{name: "exponent version", details: wrap(`{"version":1e0,"stage":"task_run_reserved"}`)},
		{name: "null object", details: wrap(`null`)},
		{name: "array object", details: wrap(`[]`)},
		{name: "string object", details: wrap(`"FAKE_METADATA_SECRET"`)},
		{name: "numeric object", details: wrap(`1`)},
		{name: "malformed JSON", details: `{"safe_execution":{"version":1`, noTargets: true},
		{name: "trailing JSON", details: wrap(metadata("task_run_reserved", sources)) + `{}`, noTargets: true},
		{name: "non-object details", details: `[]`, noTargets: true},
		{name: "unknown stage retains safe sources", details: wrap(metadata("FAKE_PRIVATE_STAGE", sources)), version: true, ids: backupIDs},
		{name: "non-string stage retains safe sources", details: wrap(`{"version":1,"stage":{} ` + sources + `}`), version: true, ids: backupIDs},
		{name: "missing stage retains safe sources", details: wrap(`{"version":1` + sources + `}`), version: true, ids: backupIDs},
		{name: "successful reason rejects boundary", details: wrap(metadata("task_run_reserved", `,"reason_code":"ACTION_FAILED"`+sources)), version: true, ids: backupIDs},
		{name: "non-string reason rejects boundary", details: wrap(metadata("task_run_reserved", `,"reason_code":{}`+sources)), version: true, ids: backupIDs},
		{name: "null reason rejects boundary", details: wrap(metadata("task_run_reserved", `,"reason_code":null`+sources)), version: true, ids: backupIDs},
		{name: "unknown reason rejects boundary", result: "error", details: wrap(metadata("action_execution", `,"reason_code":"FAKE_PRIVATE_REASON"`+sources)), version: true, ids: backupIDs},
		{name: "failure without reason", result: "error", details: wrap(metadata("action_execution", sources)), version: true, ids: backupIDs},
		{name: "failure with success stage", result: "error", details: wrap(metadata("task_run_reserved", `,"reason_code":"ACTION_FAILED"`)), version: true},
		{name: "unknown result", result: "FAKE_RESULT", details: wrap(metadata("task_run_reserved", sources)), version: true, ids: backupIDs},
		{name: "validation reason on execution stage", result: "error", details: wrap(metadata("action_execution", `,"reason_code":"INVALID_TARGET"`)), version: true},
		{name: "execution reason on validation stage", result: "error", details: wrap(metadata("action_validation", `,"reason_code":"ACTION_FAILED"`)), version: true},
		{name: "unsupported reason on known action", result: "error", details: wrap(metadata("action_validation", `,"reason_code":"UNSUPPORTED_ACTION"`)), version: true},
		{name: "unsupported unknown action", action: "FAKE_ACTION", result: "error", details: wrap(metadata("action_validation", `,"reason_code":"UNSUPPORTED_ACTION"`)), version: true, stage: "action_validation", reason: "UNSUPPORTED_ACTION"},
		{name: "execution on notification rejected", action: "send_notification", result: "error", details: wrap(metadata("action_execution", `,"reason_code":"ACTION_FAILED"`)), version: true},
		{name: "execution on unknown action rejected", action: "FAKE_ACTION", result: "error", details: wrap(metadata("action_execution", `,"reason_code":"ACTION_FAILED"`)), version: true},
		{name: "policy stage on trigger rejected", details: wrap(metadata("policy_updated", sources)), version: true, ids: backupIDs},
		{name: "reservation on policy rejected but target independently safe", action: "pause_policy", details: wrap(metadata("task_run_reserved", sources)), version: true, ids: map[string]string{"source_node_id": "11", "source_policy_id": "12", "source_task_id": "13", "source_task_run_id": "14", "target_policy_id": "15"}},
		{name: "notification stage on trigger rejected", details: wrap(metadata("notification_recorded", sources)), version: true, ids: backupIDs},
		{name: "source task missing run", details: wrap(metadata("task_run_reserved", `,"source_task_id":13,"source_node_id":11`)), version: true, stage: "task_run_reserved", ids: map[string]string{"source_node_id": "11"}},
		{name: "source run missing task", details: wrap(metadata("task_run_reserved", `,"source_task_run_id":14,"source_policy_id":12`)), version: true, stage: "task_run_reserved", ids: map[string]string{"source_policy_id": "12"}},
		{name: "anomaly node only", event: "anomaly_detected", details: wrap(metadata("task_run_reserved", sources)), version: true, stage: "task_run_reserved", ids: map[string]string{"source_node_id": "11"}},
		{name: "drill policy only", event: "drill_failed", details: wrap(metadata("task_run_reserved", sources)), version: true, stage: "task_run_reserved", ids: map[string]string{"source_policy_id": "12"}},
		{name: "unknown event no source", event: "FAKE_EVENT", details: wrap(metadata("task_run_reserved", sources)), version: true, stage: "task_run_reserved"},
		{name: "failed policy no target", action: "disable_policy", result: "error", details: wrap(metadata("action_execution", `,"reason_code":"ACTION_FAILED"`+sources)), version: true, stage: "action_execution", reason: "ACTION_FAILED", ids: backupIDs},
		{name: "recorded notification no target", action: "send_notification", details: wrap(metadata("notification_recorded", sources)), version: true, stage: "notification_recorded", ids: backupIDs},
		{name: "legacy trigger acceptance", details: wrap(metadata("task_trigger_accepted", sources)), version: true, stage: "task_trigger_accepted", ids: backupIDs},
	}
	for _, action := range []string{"pause_policy", "disable_policy"} {
		cases = append(cases, scenario{
			name: action + " target differs from source", action: action,
			details: wrap(metadata("policy_updated", sources)), version: true, stage: "policy_updated",
			ids: map[string]string{"source_node_id": "11", "source_policy_id": "12", "source_task_id": "13", "source_task_run_id": "14", "target_policy_id": "15"},
		})
	}
	for _, event := range []string{"backup_succeeded", "backup_failed"} {
		cases = append(cases, scenario{
			name: event + " source pair", event: event, details: wrap(metadata("task_run_reserved", sources)),
			version: true, stage: "task_run_reserved", ids: backupIDs,
		})
	}
	for _, boundary := range []struct {
		stage   string
		reasons []string
	}{
		{"action_validation", []string{"INVALID_TARGET", "EXECUTOR_UNAVAILABLE", "TRANSACTIONAL_EXECUTOR_UNAVAILABLE", "POLICY_CONTROLLER_UNAVAILABLE"}},
		{"action_execution", []string{"CONTEXT_CANCELED", "DEADLINE_EXCEEDED", "TARGET_NOT_FOUND", "ACTION_FAILED"}},
	} {
		for _, reason := range boundary.reasons {
			cases = append(cases, scenario{
				name: "safe failure " + reason, result: "error",
				details: wrap(metadata(boundary.stage, `,"reason_code":"`+reason+`"`+sources)),
				version: true, stage: boundary.stage, reason: reason, ids: backupIDs,
			})
		}
	}
	// Validate each association independently without ever converting through
	// float64, including fractions that would otherwise round to safe integers.
	for _, number := range []string{"0", "-1", "1.5", "1e1", `"11"`, "9007199254740992", "9007199254740991.1", "18446744073709551616", "null", "{}", "[]", "true"} {
		for _, field := range []string{"source_node_id", "source_policy_id", "source_task_id", "source_task_run_id", "target_policy_id"} {
			extra := `,"source_node_id":11,"source_policy_id":12,"source_task_id":13,"source_task_run_id":14,"target_policy_id":15`
			old := map[string]string{"source_node_id": "11", "source_policy_id": "12", "source_task_id": "13", "source_task_run_id": "14", "target_policy_id": "15"}
			extra = strings.Replace(extra, `"`+field+`":`+old[field], `"`+field+`":`+number, 1)
			delete(old, field)
			if field == "source_task_id" || field == "source_task_run_id" {
				delete(old, "source_task_id")
				delete(old, "source_task_run_id")
			}
			cases = append(cases, scenario{
				name: field + " rejects " + number, action: "pause_policy", details: wrap(metadata("policy_updated", extra)),
				version: true, stage: "policy_updated", ids: old,
			})
		}
	}
	for _, field := range []string{"source_node_id", "source_policy_id", "source_task_id", "source_task_run_id", "target_policy_id"} {
		extra := strings.Replace(sources, `"`+field+`":`+map[string]string{"source_node_id": "11", "source_policy_id": "12", "source_task_id": "13", "source_task_run_id": "14", "target_policy_id": "15"}[field], `"`+field+`":9007199254740991`, 1)
		ids := map[string]string{"source_node_id": "11", "source_policy_id": "12", "source_task_id": "13", "source_task_run_id": "14", "target_policy_id": "15"}
		ids[field] = "9007199254740991"
		cases = append(cases, scenario{name: field + " maximum exact integer", action: "pause_policy", details: wrap(metadata("policy_updated", extra)), version: true, stage: "policy_updated", ids: ids})
	}
	bounded := metadata("task_run_reserved", sources+`,"private":"FAKE_NESTED_SECRET"`)
	for _, size := range []int{1024, 1025} {
		object := bounded[:len(bounded)-1] + `,"padding":"` + strings.Repeat("x", size-len(bounded)-13) + `"}`
		if len(object) != size {
			t.Fatalf("invalid size fixture: got %d want %d", len(object), size)
		}
		tc := scenario{name: fmt.Sprintf("nested object size %d", size), details: wrap(object)}
		if size == 1024 {
			tc.version, tc.stage, tc.ids = true, "task_run_reserved", backupIDs
		}
		cases = append(cases, tc)
	}
	cases = append(cases, scenario{
		name:    "large private message does not invalidate bounded metadata",
		details: `{"task_id":41,"task_run_id":42,"message":"FAKE_LARGE_MESSAGE` + strings.Repeat("x", 2048) + `","safe_execution":` + bounded + `}`,
		version: true, stage: "task_run_reserved", ids: backupIDs,
	})

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.event == "" {
				tc.event = "backup_failed"
			}
			if tc.action == "" {
				tc.action = "trigger_task"
			}
			if tc.result == "" {
				tc.result = "success"
			}
			entry := model.AutomationRuleLog{
				RuleID: rule.ID, EventType: tc.event, ActionType: tc.action, Result: tc.result, Details: tc.details,
				Error: "-----BEGIN PRIVATE KEY-----FAKE_PRIVATE_KEY\nFAKE_ERROR_TOKEN https://FAKE_ERROR_URL.invalid/",
			}
			if err := db.Create(&entry).Error; err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/automation-rule-logs?rule_id=%d&page_size=1&sort_order=desc", rule.ID), nil))
			if w.Code != http.StatusOK {
				t.Fatalf("HTTP %d: %s", w.Code, w.Body.String())
			}
			for _, forbidden := range []string{"FAKE_", "BEGIN PRIVATE KEY", "https://", `"safe_execution"`, `"details"`, `"error":`, `"padding"`, `"private"`} {
				if strings.Contains(w.Body.String(), forbidden) {
					t.Fatalf("HTTP JSON exposed private data %q: %s", forbidden, w.Body.String())
				}
			}
			var response struct {
				Data []map[string]json.RawMessage `json:"data"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || len(response.Data) != 1 {
				t.Fatalf("invalid HTTP envelope: %s, err=%v", w.Body.String(), err)
			}
			got := response.Data[0]
			public := make(map[string]interface{}, len(got))
			for field := range got {
				public[field] = nil
			}
			assertAutomationRuleLogResponseFields(t, public)
			want := map[string]string{
				"id": fmt.Sprint(entry.ID), "metadata_version": "null", "execution_stage": `"unknown"`, "reason_code": "null",
				"source_node_id": "null", "source_policy_id": "null", "source_task_id": "null", "source_task_run_id": "null", "target_policy_id": "null",
				"target_task_id": "null", "target_task_run_id": "null", "error_code": "null",
			}
			if tc.version {
				want["metadata_version"] = "1"
			}
			if tc.stage != "" {
				want["execution_stage"] = `"` + tc.stage + `"`
			}
			if tc.reason != "" {
				want["reason_code"] = `"` + tc.reason + `"`
			}
			for field, value := range tc.ids {
				want[field] = value
			}
			if tc.action == "trigger_task" && !tc.noTargets {
				want["target_task_id"], want["target_task_run_id"] = "41", "42"
			}
			if tc.result == "error" {
				want["error_code"] = `"ACTION_FAILED"`
			}
			for field, value := range want {
				if string(got[field]) != value {
					t.Errorf("%s=%s want %s", field, got[field], value)
				}
			}
		})
	}
}
