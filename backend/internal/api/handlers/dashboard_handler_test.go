package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openDashboardTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared&_loc=UTC"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.Dashboard{}, &model.DashboardPanel{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func newDashboardRouter(t *testing.T, db *gorm.DB, userID uint, role string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewDashboardHandler(db)
	inject := func(c *gin.Context) {
		c.Set("userID", userID)
		c.Set("role", role)
		c.Next()
	}
	g := r.Group("/api/v1", inject)
	g.GET("/dashboards", middleware.RBAC("dashboards:read"), h.List)
	g.POST("/dashboards", middleware.RBAC("dashboards:write"), h.Create)
	g.GET("/dashboards/:id", middleware.RBAC("dashboards:read"), h.Get)
	g.PATCH("/dashboards/:id", middleware.RBAC("dashboards:write"), h.Update)
	g.DELETE("/dashboards/:id", middleware.RBAC("dashboards:write"), h.Delete)
	g.POST("/dashboards/:id/panels", middleware.RBAC("dashboards:write"), h.AddPanel)
	g.PATCH("/dashboards/:id/panels/:pid", middleware.RBAC("dashboards:write"), h.UpdatePanel)
	g.DELETE("/dashboards/:id/panels/:pid", middleware.RBAC("dashboards:write"), h.DeletePanel)
	g.PUT("/dashboards/:id/panels/layout", middleware.RBAC("dashboards:write"), h.UpdateLayout)
	return r
}

func doDashboard(r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
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

func TestDashboardHandler_CreateAndGet(t *testing.T) {
	db := openDashboardTestDB(t)
	r := newDashboardRouter(t, db, 1, "operator")
	w := doDashboard(r, "POST", "/api/v1/dashboards", `{"name":"ops","time_range":"1h","auto_refresh_seconds":30}`)
	if w.Code != http.StatusOK {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			ID uint `json:"id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Data.ID == 0 {
		t.Fatal("expected id")
	}
	w = doDashboard(r, "GET", fmt.Sprintf("/api/v1/dashboards/%d", resp.Data.ID), "")
	if w.Code != http.StatusOK {
		t.Fatalf("get: %d", w.Code)
	}
}

func TestDashboardHandler_List_OnlyOwn(t *testing.T) {
	db := openDashboardTestDB(t)
	r1 := newDashboardRouter(t, db, 1, "operator")
	_ = doDashboard(r1, "POST", "/api/v1/dashboards", `{"name":"a","time_range":"1h","auto_refresh_seconds":30}`)
	r2 := newDashboardRouter(t, db, 2, "operator")
	_ = doDashboard(r2, "POST", "/api/v1/dashboards", `{"name":"b","time_range":"1h","auto_refresh_seconds":30}`)
	w := doDashboard(r1, "GET", "/api/v1/dashboards", "")
	var resp struct {
		Data []model.Dashboard `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Data) != 1 || resp.Data[0].Name != "a" {
		t.Fatalf("user 1 should see only own: %+v", resp.Data)
	}
}

func TestDashboardHandler_CrossUser_404(t *testing.T) {
	db := openDashboardTestDB(t)
	r1 := newDashboardRouter(t, db, 1, "operator")
	w := doDashboard(r1, "POST", "/api/v1/dashboards", `{"name":"a","time_range":"1h","auto_refresh_seconds":30}`)
	var resp struct {
		Data struct{ ID uint } `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	r2 := newDashboardRouter(t, db, 2, "operator")
	w = doDashboard(r2, "GET", fmt.Sprintf("/api/v1/dashboards/%d", resp.Data.ID), "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestDashboardHandler_Viewer_CannotWrite(t *testing.T) {
	db := openDashboardTestDB(t)
	r := newDashboardRouter(t, db, 1, "viewer")
	w := doDashboard(r, "POST", "/api/v1/dashboards", `{"name":"a","time_range":"1h","auto_refresh_seconds":30}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}
}

func TestDashboardHandler_DuplicateName_409(t *testing.T) {
	db := openDashboardTestDB(t)
	r := newDashboardRouter(t, db, 1, "operator")
	_ = doDashboard(r, "POST", "/api/v1/dashboards", `{"name":"dup","time_range":"1h","auto_refresh_seconds":30}`)
	w := doDashboard(r, "POST", "/api/v1/dashboards", `{"name":"dup","time_range":"1h","auto_refresh_seconds":30}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", w.Code)
	}
}

func TestDashboardHandler_PanelCRUD(t *testing.T) {
	db := openDashboardTestDB(t)
	r := newDashboardRouter(t, db, 1, "operator")
	w := doDashboard(r, "POST", "/api/v1/dashboards", `{"name":"a","time_range":"1h","auto_refresh_seconds":30}`)
	var resp struct {
		Data struct{ ID uint } `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	did := resp.Data.ID
	// Add
	w = doDashboard(r, "POST", fmt.Sprintf("/api/v1/dashboards/%d/panels", did),
		`{"title":"cpu","chart_type":"line","metric":"node.cpu","filters":{"node_ids":[1]},"aggregation":"avg","layout_x":0,"layout_y":0,"layout_w":6,"layout_h":4}`)
	if w.Code != http.StatusOK {
		t.Fatalf("addpanel: %d %s", w.Code, w.Body.String())
	}
	var p struct {
		Data struct{ ID uint } `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &p)
	// Layout update
	w = doDashboard(r, "PUT", fmt.Sprintf("/api/v1/dashboards/%d/panels/layout", did),
		fmt.Sprintf(`{"items":[{"id":%d,"layout_x":0,"layout_y":0,"layout_w":12,"layout_h":8}]}`, p.Data.ID))
	if w.Code != http.StatusOK {
		t.Fatalf("layout: %d", w.Code)
	}
	// Delete panel
	w = doDashboard(r, "DELETE", fmt.Sprintf("/api/v1/dashboards/%d/panels/%d", did, p.Data.ID), "")
	if w.Code != http.StatusOK {
		t.Fatalf("delete panel: %d", w.Code)
	}
}

func TestDashboardHandler_PanelInvalidMetric_400(t *testing.T) {
	db := openDashboardTestDB(t)
	r := newDashboardRouter(t, db, 1, "operator")
	w := doDashboard(r, "POST", "/api/v1/dashboards", `{"name":"a","time_range":"1h","auto_refresh_seconds":30}`)
	var resp struct{ Data struct{ ID uint } `json:"data"` }
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	w = doDashboard(r, "POST", fmt.Sprintf("/api/v1/dashboards/%d/panels", resp.Data.ID),
		`{"title":"x","chart_type":"line","metric":"bogus","aggregation":"avg","layout_w":6,"layout_h":4}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}
