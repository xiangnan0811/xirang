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
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
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
