package handlers

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"
	"xirang/backend/internal/settings"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openSettingsAnomalySmokeDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(&model.SystemSetting{}, &model.AnomalyEvent{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func newSettingsAnomalySmokeRouter(t *testing.T, db *gorm.DB) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	settingsSvc := settings.NewService(db)
	settingsHandler := NewSettingsHandler(db, settingsSvc)
	anomalyHandler := NewAnomalyHandler(db)
	inject := func(c *gin.Context) {
		c.Set(middleware.CtxUserID, uint(1))
		c.Set("role", "admin")
		c.Next()
	}
	g := r.Group("/api/v1", inject)
	g.PUT("/settings", middleware.RequireRole("admin"), settingsHandler.BatchUpdate)
	g.GET("/anomaly-events", middleware.RBAC("nodes:read"), anomalyHandler.List)
	return r
}

func doSettingsAnomalySmoke(r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

func TestSettingsUpdateAnomalyEnabledKeepsAnomalyEventsEndpointAvailable(t *testing.T) {
	db := openSettingsAnomalySmokeDB(t)
	r := newSettingsAnomalySmokeRouter(t, db)

	w := doSettingsAnomalySmoke(r, "PUT", "/api/v1/settings", `{"anomaly.enabled":"true"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("settings update status=%d body=%s", w.Code, w.Body.String())
	}

	w = doSettingsAnomalySmoke(r, "GET", "/api/v1/anomaly-events", "")
	if w.Code != http.StatusOK {
		t.Fatalf("anomaly events status=%d body=%s", w.Code, w.Body.String())
	}
}
