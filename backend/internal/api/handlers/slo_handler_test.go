package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"
	"xirang/backend/internal/slo"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// openSLOHandlerTestDB 返回使用内存 SQLite 的测试数据库，已完成 SLODefinition 表迁移。
func openSLOHandlerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.Node{}, &model.SLODefinition{}, &model.NodeMetricSample{}, &model.NodeMetricSampleHourly{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// newSLORouter 构建仅含 slos 路由的测试路由器，注入指定角色。
func newSLORouter(db *gorm.DB, role string, userID uint) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("role", role)
		c.Set("userID", userID)
		c.Next()
	})
	h := NewSLOHandler(db)
	r.GET("/api/v1/slos", middleware.RBAC("alerts:read"), h.List)
	r.GET("/api/v1/slos/compliance-summary", middleware.RBAC("alerts:read"), h.ComplianceSummary)
	r.POST("/api/v1/slos", middleware.RequireRole("admin"), h.Create)
	r.GET("/api/v1/slos/:id", middleware.RBAC("alerts:read"), h.Get)
	r.GET("/api/v1/slos/:id/compliance", middleware.RBAC("alerts:read"), h.Compliance)
	r.PATCH("/api/v1/slos/:id", middleware.RequireRole("admin"), h.Update)
	r.DELETE("/api/v1/slos/:id", middleware.RequireRole("admin"), h.Delete)
	return r
}

// doSLOJSON 发送带 JSON body 的 HTTP 请求并返回 ResponseRecorder。
func doSLOJSON(r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	var reqBody *bytes.Buffer
	if body != "" {
		reqBody = bytes.NewBufferString(body)
	} else {
		reqBody = bytes.NewBuffer(nil)
	}
	req := httptest.NewRequest(method, path, reqBody)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// sloIDStr 将 uint ID 转为路径字符串。
func sloIDStr(id uint) string { return strconv.FormatUint(uint64(id), 10) }

// TestCreateSLO_RequiresAdmin 验证非 admin 角色收到 403。
func TestCreateSLO_RequiresAdmin(t *testing.T) {
	db := openSLOHandlerTestDB(t)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("role", "viewer")
		c.Set("userID", uint(2))
		c.Next()
	})
	h := NewSLOHandler(db)
	r.POST("/api/v1/slos", middleware.RequireRole("admin"), h.Create)

	body := `{"name":"test-slo","metric_type":"availability","threshold":0.99,"window_days":28,"enabled":true}`
	w := doSLOJSON(r, "POST", "/api/v1/slos", body)
	if w.Code != http.StatusForbidden {
		t.Fatalf("期望 403 Forbidden，实际: %d — %s", w.Code, w.Body.String())
	}
}

// TestCreateSLO_Success 验证 admin 创建 SLO 后返回 201，字段正确持久化。
func TestCreateSLO_Success(t *testing.T) {
	db := openSLOHandlerTestDB(t)
	r := newSLORouter(db, "admin", 1)

	body := `{"name":"uptime-slo","metric_type":"availability","match_tags":["prod","web"],"threshold":0.995,"window_days":30,"enabled":true}`
	w := doSLOJSON(r, "POST", "/api/v1/slos", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("期望 201，实际: %d — %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data model.SLODefinition `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	out := resp.Data
	if out.Name != "uptime-slo" {
		t.Fatalf("name 不匹配，期望 uptime-slo，实际: %s", out.Name)
	}
	if out.MetricType != "availability" {
		t.Fatalf("metric_type 不匹配，期望 availability，实际: %s", out.MetricType)
	}
	if out.Threshold != 0.995 {
		t.Fatalf("threshold 不匹配，期望 0.995，实际: %f", out.Threshold)
	}
	if out.WindowDays != 30 {
		t.Fatalf("window_days 不匹配，期望 30，实际: %d", out.WindowDays)
	}
	if !strings.Contains(out.MatchTags, "prod") {
		t.Fatalf("match_tags JSON 未包含 prod: %q", out.MatchTags)
	}
	if out.CreatedBy != 1 {
		t.Fatalf("created_by 期望 1，实际: %d", out.CreatedBy)
	}
	if out.ID == 0 {
		t.Fatal("期望分配非零 ID")
	}
}

// TestCreateSLO_InvalidMetricType 验证非法 metric_type 返回 400。
func TestCreateSLO_InvalidMetricType(t *testing.T) {
	db := openSLOHandlerTestDB(t)
	r := newSLORouter(db, "admin", 1)

	body := `{"name":"bad-slo","metric_type":"latency","threshold":0.99}`
	w := doSLOJSON(r, "POST", "/api/v1/slos", body)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("期望 400 BadRequest，实际: %d — %s", w.Code, w.Body.String())
	}
}

// TestCreateSLO_InvalidThreshold 验证越界 threshold 返回 400。
func TestCreateSLO_InvalidThreshold(t *testing.T) {
	db := openSLOHandlerTestDB(t)
	r := newSLORouter(db, "admin", 1)

	cases := []string{
		`{"name":"bad-slo","metric_type":"availability","threshold":0}`,
		`{"name":"bad-slo","metric_type":"availability","threshold":1}`,
		`{"name":"bad-slo","metric_type":"availability","threshold":1.5}`,
	}
	for _, body := range cases {
		w := doSLOJSON(r, "POST", "/api/v1/slos", body)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("期望 400 BadRequest (body=%s)，实际: %d — %s", body, w.Code, w.Body.String())
		}
	}
}

// TestListSLOs_Success 验证 List 返回所有 SLO，包括 Enabled=false 的记录。
func TestListSLOs_Success(t *testing.T) {
	db := openSLOHandlerTestDB(t)

	// 正常创建 enabled=true 的记录（GORM Create 不会丢弃 true）
	s1 := model.SLODefinition{
		Name:       "slo-enabled",
		MetricType: "availability",
		MatchTags:  "[]",
		Threshold:  0.99,
		WindowDays: 28,
		Enabled:    true,
		CreatedBy:  1,
	}
	if err := db.Create(&s1).Error; err != nil {
		t.Fatalf("创建 slo-enabled 失败: %v", err)
	}

	// Enabled=false：使用原始 SQL 插入，绕过 GORM default:true 对零值 bool 的抑制行为。
	if err := db.Exec(
		"INSERT INTO slo_definitions (name, metric_type, match_tags, threshold, window_days, enabled, created_by, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, datetime('now'), datetime('now'))",
		"slo-disabled", "success_rate", "[]", 0.95, 14, false, 1,
	).Error; err != nil {
		t.Fatalf("插入 slo-disabled 失败: %v", err)
	}

	r := newSLORouter(db, "admin", 1)
	w := doSLOJSON(r, "GET", "/api/v1/slos", "")
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，实际: %d — %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data []model.SLODefinition `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("期望 2 条 SLO，实际: %d", len(resp.Data))
	}
	// 验证 disabled SLO 的 Enabled 字段确实为 false
	var foundDisabled bool
	for _, s := range resp.Data {
		if s.Name == "slo-disabled" {
			foundDisabled = true
			if s.Enabled {
				t.Fatalf("slo-disabled 的 Enabled 应为 false，实际为 true")
			}
		}
	}
	if !foundDisabled {
		t.Fatal("响应中未找到 slo-disabled")
	}
}

// TestGetSLO_NotFound 验证获取不存在的 SLO 返回 404。
func TestGetSLO_NotFound(t *testing.T) {
	db := openSLOHandlerTestDB(t)
	r := newSLORouter(db, "admin", 1)

	w := doSLOJSON(r, "GET", "/api/v1/slos/99999", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("期望 404 NotFound，实际: %d — %s", w.Code, w.Body.String())
	}
}

// TestUpdateSLO_Success 验证 PATCH 更新 SLO 字段并返回更新后的记录。
func TestUpdateSLO_Success(t *testing.T) {
	db := openSLOHandlerTestDB(t)

	s := model.SLODefinition{
		Name:       "original-name",
		MetricType: "availability",
		MatchTags:  `["prod"]`,
		Threshold:  0.99,
		WindowDays: 28,
		Enabled:    true,
		CreatedBy:  1,
	}
	if err := db.Create(&s).Error; err != nil {
		t.Fatalf("创建 SLO 失败: %v", err)
	}

	r := newSLORouter(db, "admin", 1)
	patchBody := `{"name":"updated-name","metric_type":"success_rate","match_tags":["staging"],"threshold":0.98,"window_days":14,"enabled":false}`
	w := doSLOJSON(r, "PATCH", "/api/v1/slos/"+sloIDStr(s.ID), patchBody)
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，实际: %d — %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data model.SLODefinition `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	out := resp.Data
	if out.Name != "updated-name" {
		t.Fatalf("name 期望 updated-name，实际: %s", out.Name)
	}
	if out.MetricType != "success_rate" {
		t.Fatalf("metric_type 期望 success_rate，实际: %s", out.MetricType)
	}
	if out.Threshold != 0.98 {
		t.Fatalf("threshold 期望 0.98，实际: %f", out.Threshold)
	}
	if out.WindowDays != 14 {
		t.Fatalf("window_days 期望 14，实际: %d", out.WindowDays)
	}
	if !strings.Contains(out.MatchTags, "staging") {
		t.Fatalf("match_tags 未包含 staging: %q", out.MatchTags)
	}
}

// TestDeleteSLO_HardDelete 验证 DELETE 硬删除 SLO，之后查询返回 404。
func TestDeleteSLO_HardDelete(t *testing.T) {
	db := openSLOHandlerTestDB(t)

	s := model.SLODefinition{
		Name:       "to-delete",
		MetricType: "availability",
		MatchTags:  "[]",
		Threshold:  0.99,
		WindowDays: 28,
		Enabled:    true,
		CreatedBy:  1,
	}
	if err := db.Create(&s).Error; err != nil {
		t.Fatalf("创建 SLO 失败: %v", err)
	}

	r := newSLORouter(db, "admin", 1)

	// 执行删除
	w := doSLOJSON(r, "DELETE", "/api/v1/slos/"+sloIDStr(s.ID), "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("期望 204 NoContent，实际: %d — %s", w.Code, w.Body.String())
	}

	// 验证记录已被硬删除，再次 GET 应返回 404
	w2 := doSLOJSON(r, "GET", "/api/v1/slos/"+sloIDStr(s.ID), "")
	if w2.Code != http.StatusNotFound {
		t.Fatalf("删除后期望 404，实际: %d — %s", w2.Code, w2.Body.String())
	}

	// 直接查数据库确认记录不存在
	var count int64
	db.Model(&model.SLODefinition{}).Where("id = ?", s.ID).Count(&count)
	if count != 0 {
		t.Fatalf("期望记录已从数据库删除，实际 count=%d", count)
	}
}

// TestSLOCompliance_ReturnsStructure 验证单条 SLO 合规端点返回正确结构与 healthy 状态。
func TestSLOCompliance_ReturnsStructure(t *testing.T) {
	db := openSLOHandlerTestDB(t)
	db.Create(&model.Node{ID: 1, Name: "n1", Tags: "prod"})
	s := model.SLODefinition{Name: "prod avail", MetricType: "availability", MatchTags: `["prod"]`, Threshold: 0.99, WindowDays: 28, Enabled: true, CreatedBy: 1}
	db.Create(&s)
	now := time.Now().UTC().Truncate(time.Hour)
	for h := 0; h < 28*24; h++ {
		db.Create(&model.NodeMetricSampleHourly{NodeID: 1, BucketStart: now.Add(-time.Duration(h) * time.Hour), ProbeOK: 10, ProbeFail: 0, SampleCount: 10})
	}
	r := newSLORouter(db, "viewer", 2)
	w := doSLOJSON(r, "GET", "/api/v1/slos/"+sloIDStr(s.ID)+"/compliance", "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data slo.Compliance `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Data.Status != slo.StatusHealthy {
		t.Fatalf("expected healthy, got %q", resp.Data.Status)
	}
}

// TestSLOComplianceSummary_ReturnsCounts 验证汇总端点只计入已启用 SLO，且计数正确。
func TestSLOComplianceSummary_ReturnsCounts(t *testing.T) {
	db := openSLOHandlerTestDB(t)
	db.Create(&model.Node{ID: 1, Name: "n1", Tags: "prod"})
	now := time.Now().UTC().Truncate(time.Hour)
	// seed enough samples to get healthy status
	for h := 0; h < 28*24; h++ {
		db.Create(&model.NodeMetricSampleHourly{NodeID: 1, BucketStart: now.Add(-time.Duration(h) * time.Hour), ProbeOK: 10, ProbeFail: 0, SampleCount: 10})
	}
	// one enabled
	db.Create(&model.SLODefinition{Name: "a", MetricType: "availability", MatchTags: `["prod"]`, Threshold: 0.99, WindowDays: 28, Enabled: true, CreatedBy: 1})
	// one disabled — use raw SQL to bypass GORM default:true for zero bool
	db.Exec("INSERT INTO slo_definitions (name, metric_type, match_tags, threshold, window_days, enabled, created_by, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, datetime('now'), datetime('now'))",
		"b", "availability", `["prod"]`, 0.99, 28, 0, 1)

	r := newSLORouter(db, "viewer", 2)
	w := doSLOJSON(r, "GET", "/api/v1/slos/compliance-summary", "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Total, Healthy, Warning, Breached, Insufficient int
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Data.Total != 1 {
		t.Fatalf("expected Total=1 (only enabled healthy), got %d; body=%s", resp.Data.Total, w.Body.String())
	}
	if resp.Data.Healthy != 1 {
		t.Fatalf("expected Healthy=1, got %d", resp.Data.Healthy)
	}
}
