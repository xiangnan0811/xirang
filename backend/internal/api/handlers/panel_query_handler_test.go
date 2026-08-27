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
	db, err := gorm.Open(sqlite.Open("file:"+handlerTestDBName(t)+"?mode=memory&cache=shared&_loc=UTC"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.Node{}, &model.NodeOwner{}, &model.NodeMetricSample{}, &model.Task{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func seedPanelQueryOperatorOwner(t *testing.T, db *gorm.DB, userID, nodeID uint) {
	t.Helper()
	if err := db.Create(&model.NodeOwner{NodeID: nodeID, UserID: userID}).Error; err != nil {
		t.Fatalf("seed owner: %v", err)
	}
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
	dashboards.ResetRegistryForTest()
	dashboards.Register(providers.NewNodeProvider(db))
	t.Cleanup(dashboards.ResetRegistryForTest)
	db.Exec("INSERT INTO nodes (id, name, host, username, backup_dir) VALUES (1, 'n1', 'h', 'u', '/b1')")
	seedPanelQueryOperatorOwner(t, db, 1, 1)
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
			Series []struct {
				Points []struct{ Value float64 } `json:"points"`
			} `json:"series"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Data.Series) != 1 || resp.Data.Series[0].Points[0].Value != 60 {
		t.Fatalf("avg mismatch: %+v", resp.Data.Series)
	}
}

func TestPanelQuery_OperatorForbiddenNonOwnedNode(t *testing.T) {
	db := openPanelQueryDB(t)
	dashboards.ResetRegistryForTest()
	dashboards.Register(providers.NewNodeProvider(db))
	t.Cleanup(dashboards.ResetRegistryForTest)
	db.Exec("INSERT INTO nodes (id, name, host, username, backup_dir) VALUES (1, 'n1', 'h', 'u', '/b1')")
	db.Exec("INSERT INTO nodes (id, name, host, username, backup_dir) VALUES (2, 'n2', 'h2', 'u', '/b2')")
	seedPanelQueryOperatorOwner(t, db, 1, 1)

	base := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	r := newPanelQueryRouter(t, db, "operator")
	body := fmt.Sprintf(`{"metric":"node.cpu","filters":{"node_ids":[2]},"aggregation":"avg","start":"%s","end":"%s"}`,
		base.Format(time.RFC3339), base.Add(time.Hour).Format(time.RFC3339))
	w := doPanelQuery(r, "POST", "/api/v1/dashboards/panel-query", body)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-owned node, got %d %s", w.Code, w.Body.String())
	}
}

func TestPanelQuery_OperatorEmptyFilterOnlyOwned(t *testing.T) {
	db := openPanelQueryDB(t)
	dashboards.ResetRegistryForTest()
	dashboards.Register(providers.NewNodeProvider(db))
	t.Cleanup(dashboards.ResetRegistryForTest)
	db.Exec("INSERT INTO nodes (id, name, host, username, backup_dir) VALUES (1, 'n1', 'h', 'u', '/b1')")
	db.Exec("INSERT INTO nodes (id, name, host, username, backup_dir) VALUES (2, 'n2', 'h2', 'u', '/b2')")
	seedPanelQueryOperatorOwner(t, db, 1, 1)

	base := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	db.Create(&model.NodeMetricSample{NodeID: 1, CpuPct: 40, ProbeOK: true, SampledAt: base.Add(10 * time.Second)})
	db.Create(&model.NodeMetricSample{NodeID: 2, CpuPct: 90, ProbeOK: true, SampledAt: base.Add(10 * time.Second)})

	r := newPanelQueryRouter(t, db, "operator")
	body := fmt.Sprintf(`{"metric":"node.cpu","filters":{},"aggregation":"avg","start":"%s","end":"%s"}`,
		base.Format(time.RFC3339), base.Add(time.Hour).Format(time.RFC3339))
	w := doPanelQuery(r, "POST", "/api/v1/dashboards/panel-query", body)
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Series []struct {
				Name   string `json:"name"`
				Points []struct {
					Value float64 `json:"value"`
				} `json:"points"`
			} `json:"series"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Data.Series) != 1 {
		t.Fatalf("expected only owned node series, got %+v", resp.Data.Series)
	}
	if resp.Data.Series[0].Points[0].Value != 40 {
		t.Fatalf("expected owned node avg 40, got %+v", resp.Data.Series)
	}
}

func TestPanelQuery_AdminCanQueryAnyNode(t *testing.T) {
	db := openPanelQueryDB(t)
	dashboards.ResetRegistryForTest()
	dashboards.Register(providers.NewNodeProvider(db))
	t.Cleanup(dashboards.ResetRegistryForTest)
	db.Exec("INSERT INTO nodes (id, name, host, username, backup_dir) VALUES (2, 'n2', 'h2', 'u', '/b2')")
	base := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	db.Create(&model.NodeMetricSample{NodeID: 2, CpuPct: 55, ProbeOK: true, SampledAt: base.Add(10 * time.Second)})

	r := newPanelQueryRouter(t, db, "admin")
	body := fmt.Sprintf(`{"metric":"node.cpu","filters":{"node_ids":[2]},"aggregation":"avg","start":"%s","end":"%s"}`,
		base.Format(time.RFC3339), base.Add(time.Hour).Format(time.RFC3339))
	w := doPanelQuery(r, "POST", "/api/v1/dashboards/panel-query", body)
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}

func TestPanelQuery_OperatorForbiddenNonOwnedTask(t *testing.T) {
	db := openPanelQueryDB(t)
	dashboards.ResetRegistryForTest()
	// Task provider is registered globally in production; register a no-op isn't needed
	// for ownership denial which happens before Query.
	db.Exec("INSERT INTO nodes (id, name, host, username, backup_dir) VALUES (1, 'n1', 'h', 'u', '/b1')")
	db.Exec("INSERT INTO nodes (id, name, host, username, backup_dir) VALUES (2, 'n2', 'h2', 'u', '/b2')")
	seedPanelQueryOperatorOwner(t, db, 1, 1)
	// task 10 on owned node, task 20 on unowned node
	db.Exec("INSERT INTO tasks (id, name, node_id, status, executor_type) VALUES (10, 't-owned', 1, 'pending', 'rsync')")
	db.Exec("INSERT INTO tasks (id, name, node_id, status, executor_type) VALUES (20, 't-other', 2, 'pending', 'rsync')")

	base := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	r := newPanelQueryRouter(t, db, "operator")
	body := fmt.Sprintf(`{"metric":"task.success_rate","filters":{"task_ids":[20]},"aggregation":"avg","start":"%s","end":"%s"}`,
		base.Format(time.RFC3339), base.Add(time.Hour).Format(time.RFC3339))
	w := doPanelQuery(r, "POST", "/api/v1/dashboards/panel-query", body)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-owned task, got %d %s", w.Code, w.Body.String())
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
