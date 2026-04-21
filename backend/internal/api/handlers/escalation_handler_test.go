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

func openEscalationDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(&model.EscalationPolicy{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func newEscalationRouter(t *testing.T, db *gorm.DB, role string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewEscalationHandler(db)
	inject := func(c *gin.Context) {
		c.Set("userID", uint(1))
		c.Set("role", role)
		c.Next()
	}
	g := r.Group("/api/v1", inject)
	g.GET("/escalation-policies", middleware.RBAC("escalation:read"), h.List)
	g.POST("/escalation-policies", middleware.RBAC("escalation:write"), h.Create)
	g.GET("/escalation-policies/:id", middleware.RBAC("escalation:read"), h.Get)
	g.PATCH("/escalation-policies/:id", middleware.RBAC("escalation:write"), h.Update)
	g.DELETE("/escalation-policies/:id", middleware.RBAC("escalation:write"), h.Delete)
	return r
}

func doEsc(r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
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

func validBody(name string) string {
	return fmt.Sprintf(`{"name":%q,"min_severity":"warning","enabled":true,"levels":[{"delay_seconds":0,"integration_ids":[1]},{"delay_seconds":300,"integration_ids":[2],"severity_override":"critical"}]}`, name)
}

func TestEscalationHandler_CreateAndGet(t *testing.T) {
	db := openEscalationDB(t)
	r := newEscalationRouter(t, db, "admin")
	w := doEsc(r, "POST", "/api/v1/escalation-policies", validBody("ops"))
	if w.Code != http.StatusOK {
		t.Fatalf("%d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct{ ID uint } `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Data.ID == 0 {
		t.Fatal("expected id")
	}
	w = doEsc(r, "GET", fmt.Sprintf("/api/v1/escalation-policies/%d", resp.Data.ID), "")
	if w.Code != http.StatusOK {
		t.Fatalf("get: %d", w.Code)
	}
}

func TestEscalationHandler_List(t *testing.T) {
	db := openEscalationDB(t)
	r := newEscalationRouter(t, db, "viewer")
	_ = doEsc(newEscalationRouter(t, db, "admin"), "POST", "/api/v1/escalation-policies", validBody("a"))
	w := doEsc(r, "GET", "/api/v1/escalation-policies", "")
	if w.Code != http.StatusOK {
		t.Fatalf("%d", w.Code)
	}
}

func TestEscalationHandler_ViewerCannotWrite(t *testing.T) {
	db := openEscalationDB(t)
	r := newEscalationRouter(t, db, "viewer")
	w := doEsc(r, "POST", "/api/v1/escalation-policies", validBody("v"))
	if w.Code != http.StatusForbidden {
		t.Fatalf("%d", w.Code)
	}
}

func TestEscalationHandler_OperatorCannotWrite(t *testing.T) {
	db := openEscalationDB(t)
	r := newEscalationRouter(t, db, "operator")
	w := doEsc(r, "POST", "/api/v1/escalation-policies", validBody("o"))
	if w.Code != http.StatusForbidden {
		t.Fatalf("%d", w.Code)
	}
}

func TestEscalationHandler_DuplicateName_409(t *testing.T) {
	db := openEscalationDB(t)
	r := newEscalationRouter(t, db, "admin")
	_ = doEsc(r, "POST", "/api/v1/escalation-policies", validBody("dup"))
	w := doEsc(r, "POST", "/api/v1/escalation-policies", validBody("dup"))
	if w.Code != http.StatusConflict {
		t.Fatalf("%d", w.Code)
	}
}

func TestEscalationHandler_InvalidLevels_400(t *testing.T) {
	db := openEscalationDB(t)
	r := newEscalationRouter(t, db, "admin")
	// first level delay must be 0
	body := `{"name":"bad","min_severity":"warning","enabled":true,"levels":[{"delay_seconds":30,"integration_ids":[1]}]}`
	w := doEsc(r, "POST", "/api/v1/escalation-policies", body)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("%d", w.Code)
	}
}

func TestEscalationHandler_Delete_404AfterDelete(t *testing.T) {
	db := openEscalationDB(t)
	r := newEscalationRouter(t, db, "admin")
	w := doEsc(r, "POST", "/api/v1/escalation-policies", validBody("x"))
	var resp struct {
		Data struct{ ID uint } `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	w = doEsc(r, "DELETE", fmt.Sprintf("/api/v1/escalation-policies/%d", resp.Data.ID), "")
	if w.Code != http.StatusOK {
		t.Fatalf("delete: %d", w.Code)
	}
	w = doEsc(r, "GET", fmt.Sprintf("/api/v1/escalation-policies/%d", resp.Data.ID), "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("after delete: %d", w.Code)
	}
}
