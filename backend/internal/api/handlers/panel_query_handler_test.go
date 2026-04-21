package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"xirang/backend/internal/dashboards"
	"xirang/backend/internal/dashboards/providers"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openPanelQueryDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.Node{}, &model.NodeMetricSample{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func newPanelQueryRouter(t *testing.T, db *gorm.DB, role string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewPanelQueryHandler(db)
	inject := func(c *gin.Context) {
		c.Set("userID", uint(1))
		c.Set("role", role)
		c.Next()
	}
	g := r.Group("/api/v1", inject)
	g.POST("/dashboards/panel-query", middleware.RBAC("dashboards:read"), h.Query)
	g.GET("/dashboards/metrics", middleware.RBAC("dashboards:read"), h.ListMetrics)
	return r
}

func doPanelQuery(r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

func TestPanelQuery_NormalNodeCPU(t *testing.T) {
	db := openPanelQueryDB(t)
	// Register node provider
	dashboards.Register(providers.NewNodeProvider(db))
	t.Cleanup(func() {
		// Clear provider registry by replacing; simplest is to accept cross-test leakage
		// since findProvider returns the first match. Tests here only query node.cpu.
	})
	db.Exec("INSERT INTO nodes (id, name, host, username, backup_dir) VALUES (1, 'n1', 'h', 'u', '/b1')")
	base := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	// 1h window → step=60s. Put both samples within the same bucket (base..base+60s).
	db.Create(&model.NodeMetricSample{NodeID: 1, CpuPct: 50, ProbeOK: true, SampledAt: base.Add(10 * time.Second)})
	db.Create(&model.NodeMetricSample{NodeID: 1, CpuPct: 70, ProbeOK: true, SampledAt: base.Add(30 * time.Second)})

	r := newPanelQueryRouter(t, db, "operator")
	body := fmt.Sprintf(`{"metric":"node.cpu","filters":{"node_ids":[1]},"aggregation":"avg","start":"%s","end":"%s"}`,
		base.Format(time.RFC3339), base.Add(time.Hour).Format(time.RFC3339))
	w := doPanelQuery(r, "POST", "/api/v1/dashboards/panel-query", body)
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Series []struct{ Points []struct{ Value float64 } `json:"points"` } `json:"series"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Data.Series) != 1 || resp.Data.Series[0].Points[0].Value != 60 {
		t.Fatalf("avg mismatch: %+v", resp.Data.Series)
	}
}

func TestPanelQuery_InvalidMetric_400(t *testing.T) {
	db := openPanelQueryDB(t)
	r := newPanelQueryRouter(t, db, "operator")
	w := doPanelQuery(r, "POST", "/api/v1/dashboards/panel-query",
		`{"metric":"bogus","aggregation":"avg","start":"2026-04-21T10:00:00Z","end":"2026-04-21T11:00:00Z"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestPanelQuery_InvalidTimeRange_400(t *testing.T) {
	db := openPanelQueryDB(t)
	r := newPanelQueryRouter(t, db, "operator")
	w := doPanelQuery(r, "POST", "/api/v1/dashboards/panel-query",
		`{"metric":"node.cpu","aggregation":"avg","start":"2026-04-21T11:00:00Z","end":"2026-04-21T10:00:00Z"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestPanelQuery_FiltersFamilyMismatch_400(t *testing.T) {
	db := openPanelQueryDB(t)
	r := newPanelQueryRouter(t, db, "operator")
	w := doPanelQuery(r, "POST", "/api/v1/dashboards/panel-query",
		`{"metric":"node.cpu","filters":{"task_ids":[1]},"aggregation":"avg","start":"2026-04-21T10:00:00Z","end":"2026-04-21T11:00:00Z"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestPanelQuery_ListMetrics(t *testing.T) {
	db := openPanelQueryDB(t)
	r := newPanelQueryRouter(t, db, "viewer")
	w := doPanelQuery(r, "GET", "/api/v1/dashboards/metrics", "")
	if w.Code != http.StatusOK {
		t.Fatalf("%d", w.Code)
	}
	var resp struct {
		Data []dashboards.MetricDescriptor `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Data) != 8 {
		t.Fatalf("expected 8 metrics, got %d", len(resp.Data))
	}
}
