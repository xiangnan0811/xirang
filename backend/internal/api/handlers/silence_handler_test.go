package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// openSilenceTestDB 返回使用内存 SQLite 的测试数据库，已完成 Silence 表迁移。
func openSilenceTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.Silence{}); err != nil {
		t.Fatalf("迁移 silences 表失败: %v", err)
	}
	return db
}

// newSilenceRouter 构建仅含 silences 路由的测试路由器，注入指定角色。
func newSilenceRouter(db *gorm.DB, role string, userID uint) *gin.Engine {
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("role", role)
		c.Set("userID", userID)
		c.Next()
	})
	h := NewSilenceHandler(db)
	r.GET("/api/v1/silences", h.List)
	r.POST("/api/v1/silences", h.Create)
	r.GET("/api/v1/silences/:id", h.Get)
	r.PATCH("/api/v1/silences/:id", h.Patch)
	r.DELETE("/api/v1/silences/:id", h.Delete)
	return r
}

// doSilenceJSON 发送带 JSON body 的 HTTP 请求并返回 ResponseRecorder。
func doSilenceJSON(r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	var reqBody *bytes.Buffer
	if body != "" {
		reqBody = bytes.NewBufferString(body)
	} else {
		reqBody = bytes.NewBuffer(nil)
	}
	req := httptest.NewRequest(method, path, reqBody)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// TestCreateSilence_RequiresAdmin 验证非 admin 角色收到 403（使用真实 middleware.RequireRole）。
func TestCreateSilence_RequiresAdmin(t *testing.T) {
	db := openSilenceTestDB(t)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("role", "viewer") // 非 admin
		c.Set("userID", uint(2))
		c.Next()
	})
	h := NewSilenceHandler(db)
	r.POST("/api/v1/silences", middleware.RequireRole("admin"), h.Create)

	body := `{"name":"maint","starts_at":"2026-04-19T00:00:00Z","ends_at":"2026-04-19T02:00:00Z"}`
	w := doSilenceJSON(r, "POST", "/api/v1/silences", body)
	if w.Code != http.StatusForbidden {
		t.Fatalf("期望 403 Forbidden，实际: %d — %s", w.Code, w.Body.String())
	}
}

// TestCreateSilence_Success 验证 admin 创建静默规则后返回 201，tags 以 JSON 存储。
func TestCreateSilence_Success(t *testing.T) {
	db := openSilenceTestDB(t)
	r := newSilenceRouter(db, "admin", 1)

	nodeID := uint(1)
	reqBody := map[string]any{
		"name":            "maint",
		"match_node_id":   nodeID,
		"match_category":  "probe_down",
		"match_tags":      []string{"prod"},
		"starts_at":       "2026-04-19T00:00:00Z",
		"ends_at":         "2026-04-19T02:00:00Z",
		"note":            "cluster migrate",
	}
	bodyBytes, _ := json.Marshal(reqBody)

	w := doSilenceJSON(r, "POST", "/api/v1/silences", string(bodyBytes))
	if w.Code != http.StatusCreated {
		t.Fatalf("期望 201，实际: %d — %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data model.Silence `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	out := resp.Data
	if out.Name != "maint" {
		t.Fatalf("name 不匹配，期望 maint，实际: %s", out.Name)
	}
	if out.MatchNodeID == nil || *out.MatchNodeID != 1 {
		t.Fatalf("match_node_id 不匹配，期望 1，实际: %v", out.MatchNodeID)
	}
	if !strings.Contains(out.MatchTags, "prod") {
		t.Fatalf("match_tags JSON 未包含 prod: %q", out.MatchTags)
	}
	if out.CreatedBy != 1 {
		t.Fatalf("created_by 期望 1，实际: %d", out.CreatedBy)
	}
}

// TestListSilences_FiltersActive 验证 ?active=true 只返回当前生效的静默规则。
func TestListSilences_FiltersActive(t *testing.T) {
	db := openSilenceTestDB(t)

	now := time.Now()
	active := model.Silence{
		Name:      "active-silence",
		StartsAt:  now.Add(-1 * time.Hour),
		EndsAt:    now.Add(1 * time.Hour),
		CreatedBy: 1,
		MatchTags: "[]",
	}
	expired := model.Silence{
		Name:      "expired-silence",
		StartsAt:  now.Add(-3 * time.Hour),
		EndsAt:    now.Add(-1 * time.Hour),
		CreatedBy: 1,
		MatchTags: "[]",
	}
	if err := db.Create(&active).Error; err != nil {
		t.Fatalf("创建生效规则失败: %v", err)
	}
	if err := db.Create(&expired).Error; err != nil {
		t.Fatalf("创建过期规则失败: %v", err)
	}

	r := newSilenceRouter(db, "admin", 1)
	w := doSilenceJSON(r, "GET", "/api/v1/silences?active=true", "")
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，实际: %d — %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data []model.Silence `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("期望 1 条生效规则，实际: %d", len(resp.Data))
	}
	if resp.Data[0].Name != "active-silence" {
		t.Fatalf("返回的规则名称不匹配，期望 active-silence，实际: %s", resp.Data[0].Name)
	}
}

// TestPatchSilence_ExtendsEndsAt 验证 PATCH 可以更新 ends_at。
func TestPatchSilence_ExtendsEndsAt(t *testing.T) {
	db := openSilenceTestDB(t)

	now := time.Now()
	s := model.Silence{
		Name:      "original",
		StartsAt:  now.Add(-1 * time.Hour),
		EndsAt:    now.Add(1 * time.Hour),
		CreatedBy: 1,
		MatchTags: "[]",
	}
	if err := db.Create(&s).Error; err != nil {
		t.Fatalf("创建静默规则失败: %v", err)
	}

	newEndsAt := now.Add(4 * time.Hour)
	patchBody := map[string]any{
		"name":      "original",
		"starts_at": s.StartsAt.Format(time.RFC3339),
		"ends_at":   newEndsAt.Format(time.RFC3339),
		"note":      "extended",
	}
	bodyBytes, _ := json.Marshal(patchBody)

	r := newSilenceRouter(db, "admin", 1)
	w := doSilenceJSON(r, "PATCH", fmt.Sprintf("/api/v1/silences/%d", s.ID), string(bodyBytes))
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，实际: %d — %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data model.Silence `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if resp.Data.Note != "extended" {
		t.Fatalf("note 期望 extended，实际: %s", resp.Data.Note)
	}
	// EndsAt 应在新时间附近（允许 5 秒误差）
	diff := resp.Data.EndsAt.Sub(newEndsAt)
	if diff < -5*time.Second || diff > 5*time.Second {
		t.Fatalf("ends_at 未正确更新，期望约 %v，实际: %v", newEndsAt, resp.Data.EndsAt)
	}
}

// TestDeleteSilence_MarksEnded 验证 DELETE 将 ends_at 设置为过去时间（软删除）。
func TestDeleteSilence_MarksEnded(t *testing.T) {
	db := openSilenceTestDB(t)

	now := time.Now()
	s := model.Silence{
		Name:      "to-delete",
		StartsAt:  now.Add(-1 * time.Hour),
		EndsAt:    now.Add(2 * time.Hour),
		CreatedBy: 1,
		MatchTags: "[]",
	}
	if err := db.Create(&s).Error; err != nil {
		t.Fatalf("创建静默规则失败: %v", err)
	}

	r := newSilenceRouter(db, "admin", 1)
	w := doSilenceJSON(r, "DELETE", fmt.Sprintf("/api/v1/silences/%d", s.ID), "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("期望 204，实际: %d — %s", w.Code, w.Body.String())
	}

	// 直接从数据库重新读取，验证 ends_at ≤ now
	var updated model.Silence
	if err := db.First(&updated, s.ID).Error; err != nil {
		t.Fatalf("重新读取规则失败: %v", err)
	}
	if updated.EndsAt.After(time.Now()) {
		t.Fatalf("期望 ends_at ≤ now（软删除），实际 ends_at: %v", updated.EndsAt)
	}
}

// TestCreateSilence_InvalidTimeWindow 验证 ends_at <= starts_at 时返回 400。
func TestCreateSilence_InvalidTimeWindow(t *testing.T) {
	db := openSilenceTestDB(t)
	r := newSilenceRouter(db, "admin", 1)

	// ends_at == starts_at（不合法）
	body := `{"name":"bad-window","starts_at":"2026-04-19T02:00:00Z","ends_at":"2026-04-19T02:00:00Z"}`
	w := doSilenceJSON(r, "POST", "/api/v1/silences", body)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("期望 400 BadRequest，实际: %d — %s", w.Code, w.Body.String())
	}

	// ends_at < starts_at（不合法）
	body2 := `{"name":"bad-window2","starts_at":"2026-04-19T02:00:00Z","ends_at":"2026-04-19T01:00:00Z"}`
	w2 := doSilenceJSON(r, "POST", "/api/v1/silences", body2)
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("期望 400 BadRequest（ends_at < starts_at），实际: %d — %s", w2.Code, w2.Body.String())
	}
}

// TestPatchSilence_InvalidTimeWindow 验证 PATCH 时 ends_at <= starts_at 返回 400。
func TestPatchSilence_InvalidTimeWindow(t *testing.T) {
	db := openSilenceTestDB(t)

	now := time.Now()
	s := model.Silence{
		Name:      "patch-window-test",
		StartsAt:  now.Add(-1 * time.Hour),
		EndsAt:    now.Add(1 * time.Hour),
		CreatedBy: 1,
		MatchTags: "[]",
	}
	if err := db.Create(&s).Error; err != nil {
		t.Fatalf("创建静默规则失败: %v", err)
	}

	r := newSilenceRouter(db, "admin", 1)

	// ends_at == starts_at（不合法）
	patchBody := map[string]any{
		"name":      "patch-window-test",
		"starts_at": now.Add(-1 * time.Hour).Format(time.RFC3339),
		"ends_at":   now.Add(-1 * time.Hour).Format(time.RFC3339),
	}
	bodyBytes, _ := json.Marshal(patchBody)
	w := doSilenceJSON(r, "PATCH", fmt.Sprintf("/api/v1/silences/%d", s.ID), string(bodyBytes))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("期望 400 BadRequest，实际: %d — %s", w.Code, w.Body.String())
	}
}

// TestGetSilence_NotFound 验证获取不存在的静默规则返回 404。
func TestGetSilence_NotFound(t *testing.T) {
	db := openSilenceTestDB(t)
	r := newSilenceRouter(db, "admin", 1)

	w := doSilenceJSON(r, "GET", "/api/v1/silences/99999", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("期望 404 NotFound，实际: %d — %s", w.Code, w.Body.String())
	}
}

// TestPatchSilence_DoesNotChangeMatchFields 验证 PATCH 请求中携带 match_node_id /
// match_category 等匹配字段时，这些字段不会被修改（silencePatchRequest 不含匹配字段，
// Go JSON 解码器会静默丢弃未知字段）。
func TestPatchSilence_DoesNotChangeMatchFields(t *testing.T) {
	db := openSilenceTestDB(t)

	now := time.Now()
	origNodeID := uint(1)
	s := model.Silence{
		Name:          "immutable-match",
		MatchNodeID:   &origNodeID,
		MatchCategory: "XR-NODE-5",
		StartsAt:      now.Add(-1 * time.Hour),
		EndsAt:        now.Add(1 * time.Hour),
		CreatedBy:     1,
		MatchTags:     "[]",
	}
	if err := db.Create(&s).Error; err != nil {
		t.Fatalf("创建静默规则失败: %v", err)
	}

	newEndsAt := now.Add(4 * time.Hour)
	// 故意在 PATCH body 中携带 match_node_id=99 和 match_category="XR-OTHER"
	// 期望它们被静默丢弃，原值不变。
	patchBody := map[string]any{
		"name":            "new-name",
		"starts_at":       s.StartsAt.Format(time.RFC3339),
		"ends_at":         newEndsAt.Format(time.RFC3339),
		"match_node_id":   uint(99),
		"match_category":  "XR-OTHER",
	}
	bodyBytes, _ := json.Marshal(patchBody)

	r := newSilenceRouter(db, "admin", 1)
	w := doSilenceJSON(r, "PATCH", fmt.Sprintf("/api/v1/silences/%d", s.ID), string(bodyBytes))
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，实际: %d — %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data model.Silence `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	out := resp.Data

	// name 应已更新
	if out.Name != "new-name" {
		t.Fatalf("name 期望 new-name，实际: %s", out.Name)
	}
	// match_node_id 必须保持原值 1，不能变成 99
	if out.MatchNodeID == nil || *out.MatchNodeID != origNodeID {
		t.Fatalf("match_node_id 不应被 PATCH 修改，期望 %d，实际: %v", origNodeID, out.MatchNodeID)
	}
	// match_category 必须保持原值
	if out.MatchCategory != "XR-NODE-5" {
		t.Fatalf("match_category 不应被 PATCH 修改，期望 XR-NODE-5，实际: %s", out.MatchCategory)
	}
}
