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
	"xirang/backend/internal/nodelogs"
	"xirang/backend/internal/settings"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openNodeLogsTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+handlerTestDBName(t)+"?mode=memory&cache=shared&_loc=UTC"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.Node{}, &model.NodeOwner{}, &model.NodeLog{}, &model.Alert{}, &model.SystemSetting{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// seedOperatorOwnership wires user 1 (the test operator) as owner of the
// given nodes so ownershipNodeFilter returns them and the list query is
// not 403'd. Called from tests that use role="operator".
func seedOperatorOwnership(db *gorm.DB, nodeIDs ...uint) {
	for _, nid := range nodeIDs {
		db.Create(&model.NodeOwner{NodeID: nid, UserID: 1})
	}
}

func newNodeLogsRouter(t *testing.T, db *gorm.DB, role string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	svc := settings.NewService(db)
	h := NewNodeLogsHandler(db, svc)
	inject := func(c *gin.Context) {
		c.Set("userID", uint(1))
		c.Set("role", role)
		c.Next()
	}
	g := r.Group("/api/v1", inject)
	g.GET("/node-logs", middleware.RBAC("logs:read"), h.Query)
	g.GET("/alerts/:id/logs", middleware.RBAC("alerts:read"), h.AlertLogs)
	g.GET("/settings/logs", middleware.RequireRole("admin"), h.GetSettings)
	g.PATCH("/settings/logs", middleware.RequireRole("admin"), h.PatchSettings)
	return r
}

func doNodeLogs(r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	var rb *bytes.Buffer
	if body != "" {
		rb = bytes.NewBufferString(body)
	} else {
		rb = &bytes.Buffer{}
	}
	req := httptest.NewRequest(method, path, rb)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

func seedLog(db *gorm.DB, nodeID uint, ts time.Time, msg string) {
	db.Create(&model.NodeLog{
		NodeID: nodeID, Source: "journalctl", Path: "system.slice",
		Timestamp: ts, Priority: "info", Message: msg,
	})
}

func seedRawLog(db *gorm.DB, nodeID uint, ts time.Time, path, msg string) {
	db.Create(&model.NodeLog{
		NodeID: nodeID, Source: "journalctl", Path: path,
		Timestamp: ts, Priority: "info", Message: msg,
	})
}

func TestNodeLogs_FiltersNodeAndTime(t *testing.T) {
	db := openNodeLogsTestDB(t)
	db.Create(&model.Node{Name: "n1", Host: "h", Username: "u"})
	db.Create(&model.Node{Name: "n2", Host: "h2", Username: "u", BackupDir: "/b2"})
	seedOperatorOwnership(db, 1, 2)
	now := time.Now().UTC()
	seedLog(db, 1, now.Add(-30*time.Minute), "recent-n1")
	seedLog(db, 2, now.Add(-30*time.Minute), "recent-n2")
	seedLog(db, 1, now.Add(-4*time.Hour), "old-n1")

	r := newNodeLogsRouter(t, db, "operator")
	start := now.Add(-time.Hour).Format(time.RFC3339)
	end := now.Add(time.Minute).Format(time.RFC3339)
	url := fmt.Sprintf("/api/v1/node-logs?node_ids=1&start=%s&end=%s", start, end)
	w := doNodeLogs(r, "GET", url, "")
	if w.Code != http.StatusOK {
		t.Fatalf("%d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Data []struct {
				Message string `json:"message"`
			} `json:"data"`
			Total   int64 `json:"total"`
			HasMore bool  `json:"has_more"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Data.Data) != 1 {
		t.Fatalf("expected one row, got %+v", resp.Data.Data)
	}
	if strings.Contains(resp.Data.Data[0].Message, "recent-n1") {
		t.Fatalf("node log response leaked raw message: %+v", resp.Data.Data)
	}
}

func TestNodeLogs_SanitizesLegacyRawEvidenceInResponse(t *testing.T) {
	db := openNodeLogsTestDB(t)
	db.Create(&model.Node{Name: "n1", Host: "h", Username: "u"})
	seedOperatorOwnership(db, 1)
	now := time.Now().UTC()
	seedRawLog(db, 1, now.Add(-10*time.Minute), "/var/log/private/app.log", "token=FAKE_NODE_RESPONSE_TOKEN_FOR_TEST_ONLY host=logs.internal.example file=/srv/private/source")

	r := newNodeLogsRouter(t, db, "operator")
	start := now.Add(-time.Hour).Format(time.RFC3339)
	end := now.Add(time.Minute).Format(time.RFC3339)
	url := fmt.Sprintf("/api/v1/node-logs?start=%s&end=%s", start, end)
	w := doNodeLogs(r, "GET", url, "")
	if w.Code != http.StatusOK {
		t.Fatalf("%d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, forbidden := range []string{
		"/var/log/private/app.log",
		"FAKE_NODE_RESPONSE_TOKEN_FOR_TEST_ONLY",
		"logs.internal.example",
		"/srv/private/source",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("node log response leaked %q: %s", forbidden, body)
		}
	}
	for _, expected := range []string{"[日志路径:", "[日志内容已隐藏]"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("expected placeholder %q in %s", expected, body)
		}
	}
}

func TestNodeLogs_PathFilterUsesSanitizedPathKey(t *testing.T) {
	db := openNodeLogsTestDB(t)
	db.Create(&model.Node{Name: "n1", Host: "h", Username: "u"})
	seedOperatorOwnership(db, 1)
	now := time.Now().UTC()
	seedRawLog(db, 1, now.Add(-10*time.Minute), nodelogs.SanitizeLogPath("/var/log/private/app.log"), "matched")
	seedRawLog(db, 1, now.Add(-9*time.Minute), nodelogs.SanitizeLogPath("/var/log/private/other.log"), "other")

	r := newNodeLogsRouter(t, db, "operator")
	start := now.Add(-time.Hour).Format(time.RFC3339)
	end := now.Add(time.Minute).Format(time.RFC3339)
	url := fmt.Sprintf("/api/v1/node-logs?path=/var/log/private/app.log&start=%s&end=%s", start, end)
	w := doNodeLogs(r, "GET", url, "")
	if w.Code != http.StatusOK {
		t.Fatalf("%d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Data []model.NodeLog `json:"data"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Data.Data) != 1 {
		t.Fatalf("expected one row for sanitized path filter, got %+v", resp.Data.Data)
	}
}

func TestNodeLogs_PathFilterDoesNotQueryLegacyRawPath(t *testing.T) {
	db := openNodeLogsTestDB(t)
	db.Create(&model.Node{Name: "n1", Host: "h", Username: "u"})
	seedOperatorOwnership(db, 1)
	now := time.Now().UTC()
	seedRawLog(db, 1, now.Add(-10*time.Minute), "/var/log/private/legacy.log", "legacy")

	r := newNodeLogsRouter(t, db, "operator")
	start := now.Add(-time.Hour).Format(time.RFC3339)
	end := now.Add(time.Minute).Format(time.RFC3339)
	url := fmt.Sprintf("/api/v1/node-logs?path=/var/log/private/legacy.log&start=%s&end=%s", start, end)
	w := doNodeLogs(r, "GET", url, "")
	if w.Code != http.StatusOK {
		t.Fatalf("%d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if strings.Contains(body, "/var/log/private/legacy.log") || strings.Contains(body, "legacy") {
		t.Fatalf("legacy path filter response leaked raw evidence: %s", body)
	}
	var resp struct {
		Data struct {
			Data []model.NodeLog `json:"data"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Data.Data) != 0 {
		t.Fatalf("expected raw legacy path not to match sanitized path filter, got %+v", resp.Data.Data)
	}
}

func TestNodeLogs_SanitizesMalformedPathPlaceholderInResponse(t *testing.T) {
	db := openNodeLogsTestDB(t)
	db.Create(&model.Node{Name: "n1", Host: "h", Username: "u"})
	seedOperatorOwnership(db, 1)
	now := time.Now().UTC()
	seedRawLog(db, 1, now.Add(-10*time.Minute), "[日志路径:/var/log/private/malformed.log]", "masked")

	r := newNodeLogsRouter(t, db, "operator")
	start := now.Add(-time.Hour).Format(time.RFC3339)
	end := now.Add(time.Minute).Format(time.RFC3339)
	url := fmt.Sprintf("/api/v1/node-logs?start=%s&end=%s", start, end)
	w := doNodeLogs(r, "GET", url, "")
	if w.Code != http.StatusOK {
		t.Fatalf("%d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if strings.Contains(body, "/var/log/private/malformed.log") || strings.Contains(body, "[日志路径:/") {
		t.Fatalf("malformed placeholder leaked raw path: %s", body)
	}
}

func TestNodeLogs_Pagination(t *testing.T) {
	db := openNodeLogsTestDB(t)
	db.Create(&model.Node{Name: "n1", Host: "h", Username: "u"})
	seedOperatorOwnership(db, 1)
	base := time.Now().UTC()
	for i := 0; i < 250; i++ {
		seedLog(db, 1, base.Add(-time.Duration(i)*time.Second), fmt.Sprintf("m%d", i))
	}
	r := newNodeLogsRouter(t, db, "operator")
	start := base.Add(-time.Hour).Format(time.RFC3339)
	end := base.Add(time.Minute).Format(time.RFC3339)
	url := fmt.Sprintf("/api/v1/node-logs?page_size=100&start=%s&end=%s", start, end)
	w := doNodeLogs(r, "GET", url, "")
	if w.Code != http.StatusOK {
		t.Fatalf("%d", w.Code)
	}
	var resp struct {
		Data struct {
			Data    []model.NodeLog `json:"data"`
			Total   int64           `json:"total"`
			HasMore bool            `json:"has_more"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Data.Data) != 100 {
		t.Fatalf("got %d rows", len(resp.Data.Data))
	}
	if !resp.Data.HasMore {
		t.Fatal("expected has_more=true")
	}
	if resp.Data.Total != 250 {
		t.Fatalf("total=%d", resp.Data.Total)
	}
}

func TestNodeLogs_ReturnsInternalErrorWhenCountFails(t *testing.T) {
	db := openNodeLogsTestDB(t)
	if err := db.Migrator().DropTable(&model.NodeLog{}); err != nil {
		t.Fatalf("删除节点日志表失败: %v", err)
	}

	r := newNodeLogsRouter(t, db, "admin")
	w := doNodeLogs(r, "GET", "/api/v1/node-logs", "")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("日志计数失败期望 500，实际: %d，响应: %s", w.Code, w.Body.String())
	}
	var envelope Response
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if envelope.Code != http.StatusInternalServerError {
		t.Fatalf("期望标准错误信封，实际: %+v", envelope)
	}
}

func TestNodeLogs_KeywordExclusion(t *testing.T) {
	db := openNodeLogsTestDB(t)
	db.Create(&model.Node{Name: "n1", Host: "h", Username: "u"})
	seedOperatorOwnership(db, 1)
	now := time.Now().UTC()
	seedLog(db, 1, now.Add(-10*time.Minute), "allow traffic")
	seedLog(db, 1, now.Add(-5*time.Minute), "deny traffic")
	r := newNodeLogsRouter(t, db, "operator")
	start := now.Add(-time.Hour).Format(time.RFC3339)
	end := now.Add(time.Minute).Format(time.RFC3339)
	url := fmt.Sprintf("/api/v1/node-logs?q=!allow&start=%s&end=%s", start, end)
	w := doNodeLogs(r, "GET", url, "")
	if w.Code != http.StatusOK {
		t.Fatalf("%d", w.Code)
	}
	var resp struct {
		Data struct {
			Data []struct {
				Message string `json:"message"`
			} `json:"data"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Data.Data) != 1 {
		t.Fatalf("expected one non-excluded row, got %+v", resp.Data.Data)
	}
	if strings.Contains(resp.Data.Data[0].Message, "deny traffic") {
		t.Fatalf("node log response leaked raw message: %+v", resp.Data.Data)
	}
}

func TestAlertLogs_SanitizesLegacyRawEvidenceInResponse(t *testing.T) {
	db := openNodeLogsTestDB(t)
	db.Create(&model.Node{Name: "n1", Host: "h", Username: "u"})
	seedOperatorOwnership(db, 1)
	t0 := time.Now().UTC().Add(-30 * time.Minute)
	db.Create(&model.Alert{NodeID: 1, NodeName: "n1", Severity: "warning", Status: "open", ErrorCode: "X", Message: "m", TriggeredAt: t0})
	seedRawLog(db, 1, t0, "/var/log/private/alert.log", "password=FAKE_ALERT_LOG_PASSWORD_FOR_TEST_ONLY host=alert.internal.example file=/srv/private/alert")

	r := newNodeLogsRouter(t, db, "operator")
	w := doNodeLogs(r, "GET", "/api/v1/alerts/1/logs", "")
	if w.Code != http.StatusOK {
		t.Fatalf("%d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, forbidden := range []string{
		"/var/log/private/alert.log",
		"FAKE_ALERT_LOG_PASSWORD_FOR_TEST_ONLY",
		"alert.internal.example",
		"/srv/private/alert",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("alert log response leaked %q: %s", forbidden, body)
		}
	}
}

func TestAlertLogs_ReturnsWindow(t *testing.T) {
	db := openNodeLogsTestDB(t)
	db.Create(&model.Node{Name: "n1", Host: "h", Username: "u"})
	seedOperatorOwnership(db, 1)
	t0 := time.Now().UTC().Add(-30 * time.Minute)
	db.Create(&model.Alert{NodeID: 1, NodeName: "n1", Severity: "warning", Status: "open", ErrorCode: "X", Message: "m", TriggeredAt: t0})
	seedLog(db, 1, t0.Add(-3*time.Minute), "in-window")
	seedLog(db, 1, t0.Add(-10*time.Minute), "out-of-window")
	r := newNodeLogsRouter(t, db, "operator")
	w := doNodeLogs(r, "GET", "/api/v1/alerts/1/logs", "")
	if w.Code != http.StatusOK {
		t.Fatalf("%d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Data []struct {
				Message string `json:"message"`
			} `json:"data"`
			NodeID uint `json:"node_id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Data.Data) != 1 {
		t.Fatalf("expected one in-window row, got %+v", resp.Data.Data)
	}
	if strings.Contains(resp.Data.Data[0].Message, "in-window") {
		t.Fatalf("alert log response leaked raw message: %+v", resp.Data.Data)
	}
	if resp.Data.NodeID != 1 {
		t.Fatalf("node_id=%d", resp.Data.NodeID)
	}
}

func TestAlertLogs_PlatformAlertReturnsHint(t *testing.T) {
	db := openNodeLogsTestDB(t)
	db.Create(&model.Alert{NodeID: 0, Severity: "warning", Status: "open", ErrorCode: "X", Message: "m", TriggeredAt: time.Now().UTC()})
	r := newNodeLogsRouter(t, db, "operator")
	w := doNodeLogs(r, "GET", "/api/v1/alerts/1/logs", "")
	if w.Code != http.StatusOK {
		t.Fatalf("%d", w.Code)
	}
	var resp struct {
		Data struct {
			Data []model.NodeLog `json:"data"`
			Hint string          `json:"hint"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Data.Hint == "" {
		t.Fatal("expected hint for platform alert")
	}
	if len(resp.Data.Data) != 0 {
		t.Fatalf("expected empty data, got %d", len(resp.Data.Data))
	}
}

func TestLogsSettings_GetAndPatch(t *testing.T) {
	db := openNodeLogsTestDB(t)
	r := newNodeLogsRouter(t, db, "admin")
	w := doNodeLogs(r, "PATCH", "/api/v1/settings/logs", `{"default_retention_days":45}`)
	if w.Code != http.StatusOK {
		t.Fatalf("patch: %d: %s", w.Code, w.Body.String())
	}
	w = doNodeLogs(r, "GET", "/api/v1/settings/logs", "")
	if w.Code != http.StatusOK {
		t.Fatalf("get: %d", w.Code)
	}
	var resp struct {
		Data struct {
			DefaultRetentionDays int `json:"default_retention_days"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Data.DefaultRetentionDays != 45 {
		t.Fatalf("expected 45, got %d", resp.Data.DefaultRetentionDays)
	}
}

func TestLogsSettings_RejectsOutOfRange(t *testing.T) {
	db := openNodeLogsTestDB(t)
	r := newNodeLogsRouter(t, db, "admin")
	w := doNodeLogs(r, "PATCH", "/api/v1/settings/logs", `{"default_retention_days":999}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("%d", w.Code)
	}
}

func TestLogsSettings_RequiresAdmin(t *testing.T) {
	db := openNodeLogsTestDB(t)
	r := newNodeLogsRouter(t, db, "operator")
	w := doNodeLogs(r, "PATCH", "/api/v1/settings/logs", `{"default_retention_days":45}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("%d", w.Code)
	}
}
