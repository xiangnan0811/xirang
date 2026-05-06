package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"xirang/backend/internal/config"
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openPolicyHandlerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.Policy{}, &model.Node{}, &model.PolicyNode{}, &model.Task{}); err != nil {
		t.Fatalf("迁移测试表失败: %v", err)
	}
	return db
}

// TestPolicyUpdateWarningUsesEnvelope reproduces the regression where toggling a
// policy whose stored target_path differs from /backup returned a raw
// {"data": ..., "warning": ...} payload instead of the standard
// {"code", "message", "data"} envelope. The frontend's auto-unwrap relies on
// the "code" field, so the raw shape leaked the wrapper into PolicyResponse,
// surfacing as `Cannot read properties of undefined (reading 'trim')` inside
// describeCron when naturalLanguage was rebuilt.
func TestPolicyUpdateWarningUsesEnvelope(t *testing.T) {
	db := openPolicyHandlerTestDB(t)

	legacy := model.Policy{
		Name:       "legacy-policy",
		SourcePath: "/srv/data",
		TargetPath: "/legacy/backup",
		CronSpec:   "0 */2 * * *",
		Enabled:    true,
	}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatalf("创建测试策略失败: %v", err)
	}

	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "admin"); c.Next() })
	handler := NewPolicyHandler(db, nil)
	r.PUT("/policies/:id", handler.Update)

	body := map[string]any{
		"name":        legacy.Name,
		"source_path": legacy.SourcePath,
		"cron_spec":   legacy.CronSpec,
		"enabled":     false,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("编码请求体失败: %v", err)
	}

	req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/policies/%d", legacy.ID), bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d, body=%s", resp.Code, resp.Body.String())
	}

	// 顶层结构必须遵循统一信封 {code, message, data}；前端 request() 依赖顶层
	// code 字段决定是否自动解包。任何顶层 warning 字段都意味着旧的非信封格式。
	var top map[string]json.RawMessage
	if err := json.Unmarshal(resp.Body.Bytes(), &top); err != nil {
		t.Fatalf("解析顶层响应失败: %v, body=%s", err, resp.Body.String())
	}
	if _, ok := top["code"]; !ok {
		t.Fatalf("响应缺少顶层 'code' 字段，违反统一信封约定，body=%s", resp.Body.String())
	}
	if _, ok := top["warning"]; ok {
		t.Fatalf("响应不应在顶层暴露 'warning'，应该放进 envelope.message，body=%s", resp.Body.String())
	}

	var envelope struct {
		Code    int            `json:"code"`
		Message string         `json:"message"`
		Data    map[string]any `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("解析响应失败: %v, body=%s", err, resp.Body.String())
	}

	if envelope.Code != 0 {
		t.Fatalf("期望 envelope code=0，实际: %d", envelope.Code)
	}
	if envelope.Data == nil {
		t.Fatalf("envelope.data 不应为空，body=%s", resp.Body.String())
	}
	if cron, ok := envelope.Data["cron_spec"].(string); !ok || cron != legacy.CronSpec {
		t.Fatalf("期望 data.cron_spec=%q，实际: %v", legacy.CronSpec, envelope.Data["cron_spec"])
	}
	if target, ok := envelope.Data["target_path"].(string); !ok || target != config.BackupRoot {
		t.Fatalf("期望 data.target_path=%q，实际: %v", config.BackupRoot, envelope.Data["target_path"])
	}

	// 警告信息必须保留，建议放进 envelope.message，便于前端用 toast 提示。
	if !strings.Contains(envelope.Message, "/legacy/backup") {
		t.Fatalf("期望 envelope.message 包含旧路径 '/legacy/backup'，实际: %q", envelope.Message)
	}
}

// DrillConfig 测试辅助函数：创建 Gin 路由并设置 admin 角色
func setupDrillTestRouter(handler *PolicyHandler) *gin.Engine {
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "admin"); c.Next() })
	r.POST("/policies", handler.Create)
	r.GET("/policies/:id", handler.Get)
	r.PUT("/policies/:id", handler.Update)
	return r
}

// TestDrillCreateValid 测试创建启用 drill 的有效策略
func TestDrillCreateValid(t *testing.T) {
	db := openPolicyHandlerTestDB(t)

	sandbox := model.Node{Name: "sandbox-node", Host: "10.0.0.100", BackupDir: "/backup/sandbox-node"}
	db.Create(&sandbox)
	source := model.Node{Name: "source-node", Host: "10.0.0.1", BackupDir: "/backup/source-node"}
	db.Create(&source)

	r := setupDrillTestRouter(NewPolicyHandler(db, nil))

	body := map[string]any{
		"name":                 "drill-policy-valid",
		"source_path":          "/data",
		"cron_spec":            "0 2 * * *",
		"drill_enabled":        true,
		"drill_cron":           "0 3 * * *",
		"drill_target_node_id": sandbox.ID,
		"drill_restore_path":   "/tmp/drill-test",
		"drill_verify":         "echo ok",
		"node_ids":             []uint{source.ID},
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/policies", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", resp.Code, resp.Body.String())
	}

	var envelope struct {
		Code int            `json:"code"`
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal(resp.Body.Bytes(), &envelope)
	if envelope.Data["drill_enabled"] != true {
		t.Error("expected drill_enabled=true")
	}
	if envelope.Data["drill_cron"] != "0 3 * * *" {
		t.Errorf("expected drill_cron='0 3 * * *', got %v", envelope.Data["drill_cron"])
	}
	if envelope.Data["drill_target_node_id"] != float64(sandbox.ID) {
		t.Errorf("expected drill_target_node_id=%d", sandbox.ID)
	}
}

// TestDrillCreateMissingCron 测试启用 drill 但未设置 cron 时报错
func TestDrillCreateMissingCron(t *testing.T) {
	db := openPolicyHandlerTestDB(t)

	sandbox := model.Node{Name: "sandbox-node", Host: "10.0.0.100", BackupDir: "/backup/sandbox-cron"}
	db.Create(&sandbox)
	source := model.Node{Name: "source-node", Host: "10.0.0.1", BackupDir: "/backup/source-cron"}
	db.Create(&source)

	r := setupDrillTestRouter(NewPolicyHandler(db, nil))

	body := map[string]any{
		"name":                 "drill-no-cron",
		"source_path":          "/data",
		"cron_spec":            "0 2 * * *",
		"drill_enabled":        true,
		"drill_target_node_id": sandbox.ID,
		"drill_restore_path":   "/tmp/drill-test",
		"node_ids":             []uint{source.ID},
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/policies", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", resp.Code, resp.Body.String())
	}
}

// TestDrillCreateSandboxEqualsSource 测试沙箱节点与源节点相同时报错
func TestDrillCreateSandboxEqualsSource(t *testing.T) {
	db := openPolicyHandlerTestDB(t)

	node := model.Node{Name: "dual-role-node", Host: "10.0.0.1", BackupDir: "/backup/dual-role"}
	db.Create(&node)

	r := setupDrillTestRouter(NewPolicyHandler(db, nil))

	body := map[string]any{
		"name":                 "drill-same-node",
		"source_path":          "/data",
		"cron_spec":            "0 2 * * *",
		"drill_enabled":        true,
		"drill_cron":           "0 3 * * *",
		"drill_target_node_id": node.ID,
		"drill_restore_path":   "/tmp/drill-test",
		"node_ids":             []uint{node.ID},
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/policies", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", resp.Code, resp.Body.String())
	}
}

// TestDrillCreateDisabled 测试 drill_enabled=false 时不校验 drill 字段
func TestDrillCreateDisabled(t *testing.T) {
	db := openPolicyHandlerTestDB(t)

	// drill_enabled=false, drill_cron 留空 → 应该成功
	r := setupDrillTestRouter(NewPolicyHandler(db, nil))

	body := map[string]any{
		"name":          "drill-disabled",
		"source_path":   "/data",
		"cron_spec":     "0 2 * * *",
		"drill_enabled": false,
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/policies", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", resp.Code, resp.Body.String())
	}
}

// TestDrillUpdateAddConfig 测试更新策略添加 drill 配置
func TestDrillUpdateAddConfig(t *testing.T) {
	db := openPolicyHandlerTestDB(t)

	sandbox := model.Node{Name: "sandbox-node", Host: "10.0.0.100", BackupDir: "/backup/sandbox-update"}
	db.Create(&sandbox)
	source := model.Node{Name: "source-node", Host: "10.0.0.1", BackupDir: "/backup/source-update"}
	db.Create(&source)

	// 先创建一个不含 drill 的策略
	p := model.Policy{
		Name:       "no-drill-initially",
		SourcePath: "/data",
		CronSpec:   "0 2 * * *",
		Enabled:    true,
	}
	db.Create(&p)

	r := setupDrillTestRouter(NewPolicyHandler(db, nil))

	// 更新策略添加 drill 配置
	body := map[string]any{
		"name":                 "no-drill-initially",
		"source_path":          "/data",
		"cron_spec":            "0 2 * * *",
		"drill_enabled":        true,
		"drill_cron":           "0 4 * * *",
		"drill_target_node_id": sandbox.ID,
		"drill_restore_path":   "/tmp/drill-update",
		"drill_verify":         "echo updated",
		"drill_auto_cleanup":   false,
		"node_ids":             []uint{source.ID},
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest("PUT", fmt.Sprintf("/policies/%d", p.ID), bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.Code, resp.Body.String())
	}

	var envelope struct {
		Code int            `json:"code"`
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal(resp.Body.Bytes(), &envelope)
	if envelope.Data["drill_enabled"] != true {
		t.Error("expected drill_enabled=true after update")
	}
	if envelope.Data["drill_auto_cleanup"] != false {
		t.Error("expected drill_auto_cleanup=false after update")
	}
}

// TestDrillGetIncludesFields 测试获取策略时响应包含 drill 字段
func TestDrillGetIncludesFields(t *testing.T) {
	db := openPolicyHandlerTestDB(t)

	p := model.Policy{
		Name:             "drill-inspect",
		SourcePath:       "/data",
		CronSpec:         "0 2 * * *",
		Enabled:          true,
		DrillEnabled:     true,
		DrillCron:        "0 5 * * *",
		DrillRestorePath: "/tmp/drill-inspect",
		DrillVerify:      "echo inspect",
	}
	db.Create(&p)

	r := setupDrillTestRouter(NewPolicyHandler(db, nil))

	req := httptest.NewRequest("GET", fmt.Sprintf("/policies/%d", p.ID), nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.Code, resp.Body.String())
	}

	var envelope struct {
		Code int            `json:"code"`
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal(resp.Body.Bytes(), &envelope)

	// 检查 drill 字段存在
	if _, ok := envelope.Data["drill_enabled"]; !ok {
		t.Error("response missing drill_enabled")
	}
	if _, ok := envelope.Data["drill_cron"]; !ok {
		t.Error("response missing drill_cron")
	}
	if _, ok := envelope.Data["drill_target_node_id"]; !ok {
		t.Error("response missing drill_target_node_id")
	}
	if _, ok := envelope.Data["drill_restore_path"]; !ok {
		t.Error("response missing drill_restore_path")
	}
	if _, ok := envelope.Data["drill_pre_verify"]; !ok {
		t.Error("response missing drill_pre_verify")
	}
	if _, ok := envelope.Data["drill_verify"]; !ok {
		t.Error("response missing drill_verify")
	}
	if _, ok := envelope.Data["drill_post_verify"]; !ok {
		t.Error("response missing drill_post_verify")
	}
	if _, ok := envelope.Data["drill_auto_cleanup"]; !ok {
		t.Error("response missing drill_auto_cleanup")
	}
}

// TestGFSCreateValid 测试创建 GFS 保留模式的策略
func TestGFSCreateValid(t *testing.T) {
	db := openPolicyHandlerTestDB(t)

	r := setupDrillTestRouter(NewPolicyHandler(db, nil))

	body := map[string]any{
		"name":           "gfs-policy-valid",
		"source_path":    "/data",
		"cron_spec":      "0 2 * * *",
		"retention_mode": "gfs",
		"keep_daily":     7,
		"keep_weekly":    4,
		"keep_monthly":   12,
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/policies", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", resp.Code, resp.Body.String())
	}

	var envelope struct {
		Code int            `json:"code"`
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal(resp.Body.Bytes(), &envelope)
	if envelope.Data["retention_mode"] != "gfs" {
		t.Errorf("expected retention_mode='gfs', got %v", envelope.Data["retention_mode"])
	}
	if envelope.Data["keep_daily"] != float64(7) {
		t.Errorf("expected keep_daily=7, got %v", envelope.Data["keep_daily"])
	}
	if envelope.Data["keep_weekly"] != float64(4) {
		t.Errorf("expected keep_weekly=4, got %v", envelope.Data["keep_weekly"])
	}
}

// TestGFSCreateNoKeepValues 测试 GFS 模式但未设置任何 keep 值 → 400
func TestGFSCreateNoKeepValues(t *testing.T) {
	db := openPolicyHandlerTestDB(t)

	r := setupDrillTestRouter(NewPolicyHandler(db, nil))

	body := map[string]any{
		"name":           "gfs-no-keep",
		"source_path":    "/data",
		"cron_spec":      "0 2 * * *",
		"retention_mode": "gfs",
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/policies", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for GFS without keep values, got %d: %s", resp.Code, resp.Body.String())
	}
}

// TestRPORTOFieldsInResponse 测试创建带 RPO/RTO 目标的策略 → 响应包含这些字段
func TestRPORTOFieldsInResponse(t *testing.T) {
	db := openPolicyHandlerTestDB(t)

	r := setupDrillTestRouter(NewPolicyHandler(db, nil))

	body := map[string]any{
		"name":        "rpo-rto-policy",
		"source_path": "/data",
		"cron_spec":   "0 2 * * *",
		"rpo_minutes": 60,
		"rto_minutes": 120,
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/policies", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", resp.Code, resp.Body.String())
	}

	var envelope struct {
		Code int            `json:"code"`
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal(resp.Body.Bytes(), &envelope)
	if envelope.Data["rpo_minutes"] != float64(60) {
		t.Errorf("expected rpo_minutes=60, got %v", envelope.Data["rpo_minutes"])
	}
	if envelope.Data["rto_minutes"] != float64(120) {
		t.Errorf("expected rto_minutes=120, got %v", envelope.Data["rto_minutes"])
	}
	if envelope.Data["retention_mode"] != "simple" {
		t.Errorf("expected default retention_mode='simple', got %v", envelope.Data["retention_mode"])
	}
}

// TestGFSUpdateAddKeepValues 测试更新策略添加 GFS 保留配置
func TestGFSUpdateAddKeepValues(t *testing.T) {
	db := openPolicyHandlerTestDB(t)

	p := model.Policy{
		Name:       "gfs-update-test",
		SourcePath: "/data",
		CronSpec:   "0 2 * * *",
		Enabled:    true,
	}
	db.Create(&p)

	r := setupDrillTestRouter(NewPolicyHandler(db, nil))

	body := map[string]any{
		"name":           "gfs-update-test",
		"source_path":    "/data",
		"cron_spec":      "0 2 * * *",
		"retention_mode": "gfs",
		"keep_daily":     14,
		"keep_weekly":    8,
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest("PUT", fmt.Sprintf("/policies/%d", p.ID), bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.Code, resp.Body.String())
	}

	var envelope struct {
		Code int            `json:"code"`
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal(resp.Body.Bytes(), &envelope)
	if envelope.Data["retention_mode"] != "gfs" {
		t.Errorf("expected retention_mode='gfs' after update, got %v", envelope.Data["retention_mode"])
	}
	if envelope.Data["keep_daily"] != float64(14) {
		t.Errorf("expected keep_daily=14, got %v", envelope.Data["keep_daily"])
	}
}

// TestInvalidRetentionMode 测试非法 retention_mode → 400
func TestInvalidRetentionMode(t *testing.T) {
	db := openPolicyHandlerTestDB(t)

	r := setupDrillTestRouter(NewPolicyHandler(db, nil))

	body := map[string]any{
		"name":           "invalid-mode",
		"source_path":    "/data",
		"cron_spec":      "0 2 * * *",
		"retention_mode": "invalid",
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/policies", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid retention_mode, got %d: %s", resp.Code, resp.Body.String())
	}
}
