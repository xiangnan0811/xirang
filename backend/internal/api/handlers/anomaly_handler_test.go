package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openAnomalyTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(&model.Node{}, &model.AnomalyEvent{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func newAnomalyRouter(t *testing.T, db *gorm.DB, role string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewAnomalyHandler(db)
	inject := func(c *gin.Context) {
		c.Set("userID", uint(1))
		c.Set("role", role)
		c.Next()
	}
	g := r.Group("/api/v1", inject)
	g.GET("/anomaly-events", middleware.RBAC("nodes:read"), h.List)
	g.GET("/nodes/:id/anomaly-events", middleware.RBAC("nodes:read"), h.ListForNode)
	return r
}

func doAnomaly(r *gin.Engine, method, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, &bytes.Buffer{})
	r.ServeHTTP(w, req)
	return w
}

func seedAnomalyEvent(db *gorm.DB, nodeID uint, detector, metric, severity string, firedAt time.Time) {
	db.Create(&model.AnomalyEvent{
		NodeID: nodeID, Detector: detector, Metric: metric, Severity: severity,
		ObservedValue: 85, BaselineValue: 30, Details: "{}", FiredAt: firedAt,
	})
}

func TestAnomalyHandler_ListBasic(t *testing.T) {
	db := openAnomalyTestDB(t)
	db.Exec("INSERT INTO nodes (id, name, host, username, backup_dir) VALUES (1, 'n', 'h', 'u', '/b')")
	now := time.Now()
	seedAnomalyEvent(db, 1, "ewma", "cpu_pct", "warning", now.Add(-1*time.Hour))
	seedAnomalyEvent(db, 1, "ewma", "mem_pct", "critical", now)
	r := newAnomalyRouter(t, db, "viewer")
	w := doAnomaly(r, "GET", "/api/v1/anomaly-events")
	if w.Code != http.StatusOK {
		t.Fatalf("%d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Data  []model.AnomalyEvent `json:"data"`
			Total int64                `json:"total"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Data.Total != 2 {
		t.Fatalf("total=%d want 2", resp.Data.Total)
	}
	if resp.Data.Data[0].Metric != "mem_pct" {
		t.Fatalf("expected desc order; first=%s", resp.Data.Data[0].Metric)
	}
}

func TestAnomalyHandler_ListFilterBySeverity(t *testing.T) {
	db := openAnomalyTestDB(t)
	db.Exec("INSERT INTO nodes (id, name, host, username, backup_dir) VALUES (1, 'n', 'h', 'u', '/b')")
	seedAnomalyEvent(db, 1, "ewma", "cpu_pct", "warning", time.Now())
	seedAnomalyEvent(db, 1, "ewma", "cpu_pct", "critical", time.Now())
	r := newAnomalyRouter(t, db, "viewer")
	w := doAnomaly(r, "GET", "/api/v1/anomaly-events?severity=critical")
	if w.Code != http.StatusOK {
		t.Fatalf("%d", w.Code)
	}
	var resp struct {
		Data struct {
			Total int64 `json:"total"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Data.Total != 1 {
		t.Fatalf("total=%d want 1", resp.Data.Total)
	}
}

func TestAnomalyHandler_ListInvalidSeverity_400(t *testing.T) {
	db := openAnomalyTestDB(t)
	r := newAnomalyRouter(t, db, "viewer")
	w := doAnomaly(r, "GET", "/api/v1/anomaly-events?severity=bogus")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("%d", w.Code)
	}
}

func TestAnomalyHandler_ForNode(t *testing.T) {
	db := openAnomalyTestDB(t)
	db.Exec("INSERT INTO nodes (id, name, host, username, backup_dir) VALUES (1, 'n', 'h', 'u', '/b')")
	db.Exec("INSERT INTO nodes (id, name, host, username, backup_dir) VALUES (2, 'n2', 'h2', 'u', '/b2')")
	seedAnomalyEvent(db, 1, "ewma", "cpu_pct", "warning", time.Now())
	seedAnomalyEvent(db, 2, "ewma", "cpu_pct", "warning", time.Now())
	r := newAnomalyRouter(t, db, "viewer")
	w := doAnomaly(r, "GET", "/api/v1/nodes/1/anomaly-events")
	if w.Code != http.StatusOK {
		t.Fatalf("%d", w.Code)
	}
	var resp struct {
		Data []model.AnomalyEvent `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Data) != 1 {
		t.Fatalf("expected 1 event for node 1, got %d", len(resp.Data))
	}
	if resp.Data[0].NodeID != 1 {
		t.Fatalf("node_id=%d want 1", resp.Data[0].NodeID)
	}
}

func TestAnomalyHandler_ForNode_404(t *testing.T) {
	db := openAnomalyTestDB(t)
	r := newAnomalyRouter(t, db, "viewer")
	w := doAnomaly(r, "GET", "/api/v1/nodes/999/anomaly-events")
	if w.Code != http.StatusNotFound {
		t.Fatalf("%d", w.Code)
	}
}

func TestAnomalyHandler_Pagination(t *testing.T) {
	db := openAnomalyTestDB(t)
	db.Exec("INSERT INTO nodes (id, name, host, username, backup_dir) VALUES (1, 'n', 'h', 'u', '/b')")
	base := time.Now()
	for i := 0; i < 15; i++ {
		seedAnomalyEvent(db, 1, "ewma", "cpu_pct", "warning", base.Add(-time.Duration(i)*time.Minute))
	}
	r := newAnomalyRouter(t, db, "viewer")
	w := doAnomaly(r, "GET", "/api/v1/anomaly-events?page=1&page_size=10")
	if w.Code != http.StatusOK {
		t.Fatalf("%d", w.Code)
	}
	var resp struct {
		Data struct {
			Data    []model.AnomalyEvent `json:"data"`
			Total   int64                `json:"total"`
			HasMore bool                 `json:"has_more"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Data.Data) != 10 || resp.Data.Total != 15 || !resp.Data.HasMore {
		t.Fatalf("pagination broken: len=%d total=%d hasmore=%v",
			len(resp.Data.Data), resp.Data.Total, resp.Data.HasMore)
	}
}
