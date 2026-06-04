package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
)

func TestAdminMetricsHandlerRollupStatusReturnsEnvelope(t *testing.T) {
	db := openNodeHandlerTestDB(t)
	if err := db.AutoMigrate(&model.NodeMetricSampleHourly{}, &model.NodeMetricSampleDaily{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/v1/admin/metrics/rollup-status", NewAdminMetricsHandler(db).RollupStatus)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/metrics/rollup-status", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}

	var envelope struct {
		Code int `json:"code"`
		Data struct {
			Hourly map[string]interface{} `json:"hourly"`
			Daily  map[string]interface{} `json:"daily"`
		} `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if envelope.Code != http.StatusOK {
		t.Fatalf("expected envelope code %d, got %d", http.StatusOK, envelope.Code)
	}
	if envelope.Data.Hourly == nil || envelope.Data.Daily == nil {
		t.Fatalf("expected hourly and daily rollup data, got %+v", envelope.Data)
	}
}
