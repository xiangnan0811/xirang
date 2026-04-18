package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
)

func TestNodeMetricsHandler_Status(t *testing.T) {
	db := openNodeHandlerTestDB(t)
	if err := db.AutoMigrate(
		&model.Node{},
		&model.SSHKey{},
		&model.NodeMetricSample{},
		&model.NodeMetricSampleHourly{},
		&model.Alert{},
		&model.TaskRun{},
		&model.Task{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	node := model.Node{Name: "test-status-node", Host: "127.0.0.1", Port: 22, Username: "root", BackupDir: "backup-status"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("seed node: %v", err)
	}

	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		lat := int64(100 + i)
		if err := db.Create(&model.NodeMetricSample{
			NodeID:    node.ID,
			CpuPct:    10 + float64(i),
			MemPct:    50,
			DiskPct:   40,
			Load1m:    0.3,
			LatencyMs: &lat,
			ProbeOK:   true,
			SampledAt: now.Add(-time.Duration(i) * time.Minute),
		}).Error; err != nil {
			t.Fatalf("seed sample: %v", err)
		}
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewNodeMetricsHandler(db)
	r.GET("/api/v1/nodes/:id/status", h.Status)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/nodes/%d/status", node.ID), nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Online  bool `json:"online"`
		Current struct {
			CPUPct float64 `json:"cpu_pct"`
		} `json:"current"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Online {
		t.Fatalf("expected online")
	}
	if resp.Current.CPUPct < 10 {
		t.Fatalf("current cpu_pct too low: %f", resp.Current.CPUPct)
	}
}
