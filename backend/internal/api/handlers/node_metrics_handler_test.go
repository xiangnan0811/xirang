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

func TestNodeMetricsHandler_Metrics_AutoPicksHourlyFor7d(t *testing.T) {
	db := openNodeHandlerTestDB(t)
	if err := db.AutoMigrate(
		&model.Node{},
		&model.SSHKey{},
		&model.NodeMetricSample{},
		&model.NodeMetricSampleHourly{},
		&model.NodeMetricSampleDaily{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	node := model.Node{Name: "test-series-node", Host: "127.0.0.1", Port: 22, Username: "root", BackupDir: "backup-series"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Seed 48 hourly buckets covering the requested 7d window.
	end := time.Now().UTC().Truncate(time.Hour)
	for i := 0; i < 48; i++ {
		avg := 20.0 + float64(i)
		max := avg + 5
		if err := db.Create(&model.NodeMetricSampleHourly{
			NodeID:      node.ID,
			BucketStart: end.Add(-time.Duration(i) * time.Hour),
			CpuPctAvg:   &avg,
			CpuPctMax:   &max,
			ProbeOK:     10,
			SampleCount: 10,
		}).Error; err != nil {
			t.Fatalf("seed hour: %v", err)
		}
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewNodeMetricsHandler(db)
	r.GET("/api/v1/nodes/:id/metric-series", h.Metrics)

	from := end.Add(-7 * 24 * time.Hour).Format(time.RFC3339)
	to := end.Format(time.RFC3339)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet,
		fmt.Sprintf("/api/v1/nodes/%d/metric-series?from=%s&to=%s&fields=cpu_pct", node.ID, from, to),
		nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		Granularity   string `json:"granularity"`
		BucketSeconds int    `json:"bucket_seconds"`
		Series        []struct {
			Metric string `json:"metric"`
			Unit   string `json:"unit"`
		} `json:"series"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Granularity != "hourly" {
		t.Fatalf("expected hourly, got %s", resp.Granularity)
	}
	if resp.BucketSeconds != 3600 {
		t.Fatalf("expected bucket_seconds=3600, got %d", resp.BucketSeconds)
	}
	if len(resp.Series) != 1 || resp.Series[0].Metric != "cpu_pct" {
		t.Fatalf("expected one cpu_pct series, got %+v", resp.Series)
	}
}

func TestNodeMetricsHandler_Metrics_BadTimeRange(t *testing.T) {
	db := openNodeHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Node{}, &model.SSHKey{}, &model.NodeMetricSample{}, &model.NodeMetricSampleHourly{}, &model.NodeMetricSampleDaily{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewNodeMetricsHandler(db)
	r.GET("/api/v1/nodes/:id/metric-series", h.Metrics)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/nodes/1/metric-series?from=2026-01-01T00:00:00Z&to=2025-12-31T00:00:00Z", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestNodeMetricsHandler_DiskForecast_HighConfidence(t *testing.T) {
	db := openNodeHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Node{}, &model.SSHKey{}, &model.NodeMetricSampleDaily{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	node := model.Node{Name: "test-forecast-node", Host: "127.0.0.1", Port: 22, Username: "root", BackupDir: "backup-forecast"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("seed node: %v", err)
	}
	// 25 days of linear growth (100 → 148)
	base := time.Now().UTC().Add(-25 * 24 * time.Hour).Truncate(24 * time.Hour)
	for d := 0; d < 25; d++ {
		used := 100 + 2*float64(d)
		total := 200.0
		if err := db.Create(&model.NodeMetricSampleDaily{
			NodeID:        node.ID,
			BucketStart:   base.Add(time.Duration(d) * 24 * time.Hour),
			DiskGBUsedAvg: &used,
			DiskGBTotal:   &total,
			ProbeOK:       10,
			SampleCount:   10,
		}).Error; err != nil {
			t.Fatalf("seed day %d: %v", d, err)
		}
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewNodeMetricsHandler(db)
	r.GET("/api/v1/nodes/:id/disk-forecast", h.DiskForecast)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/nodes/%d/disk-forecast", node.ID), nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Forecast struct {
			Confidence string   `json:"confidence"`
			DaysToFull *float64 `json:"days_to_full"`
		} `json:"forecast"`
		DailyGrowthGB *float64 `json:"daily_growth_gb"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Forecast.Confidence != "high" {
		t.Fatalf("expected confidence=high, got %s", resp.Forecast.Confidence)
	}
	if resp.DailyGrowthGB == nil || *resp.DailyGrowthGB < 1.5 || *resp.DailyGrowthGB > 2.5 {
		t.Fatalf("expected daily_growth_gb ≈ 2, got %v", resp.DailyGrowthGB)
	}
	if resp.Forecast.DaysToFull == nil || *resp.Forecast.DaysToFull <= 0 {
		t.Fatalf("expected positive days_to_full, got %v", resp.Forecast.DaysToFull)
	}
}

func TestNodeMetricsHandler_DiskForecast_Insufficient(t *testing.T) {
	db := openNodeHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Node{}, &model.SSHKey{}, &model.NodeMetricSampleDaily{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	node := model.Node{Name: "test-forecast-insufficient", Host: "127.0.0.1", Port: 22, Username: "root", BackupDir: "backup-forecast-insuf"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewNodeMetricsHandler(db)
	r.GET("/api/v1/nodes/:id/disk-forecast", h.DiskForecast)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/nodes/%d/disk-forecast", node.ID), nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp struct {
		Forecast struct {
			Confidence string `json:"confidence"`
		} `json:"forecast"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Forecast.Confidence != "insufficient" {
		t.Fatalf("expected insufficient, got %s", resp.Forecast.Confidence)
	}
}

func TestDownsample_UnderMaxReturnsInput(t *testing.T) {
	pts := []metricPoint{{T: time.Unix(0, 0)}, {T: time.Unix(1, 0)}, {T: time.Unix(2, 0)}}
	got := downsample(pts, 10)
	if len(got) != 3 {
		t.Fatalf("expected 3 points, got %d", len(got))
	}
}

func TestDownsample_CapsAndKeepsLastPoint(t *testing.T) {
	pts := make([]metricPoint, 100)
	for i := range pts {
		pts[i] = metricPoint{T: time.Unix(int64(i), 0)}
	}
	got := downsample(pts, 10)
	if len(got) > 11 {
		t.Fatalf("expected at most 11 points, got %d", len(got))
	}
	if got[len(got)-1].T != pts[len(pts)-1].T {
		t.Fatalf("last point must be preserved")
	}
}

func TestFillTrend_WeightedBySampleCount(t *testing.T) {
	db := openNodeHandlerTestDB(t)
	if err := db.AutoMigrate(&model.NodeMetricSampleHourly{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Hour)
	// Bucket A: 3 samples, cpu_avg = 10  — short hour, few samples
	// Bucket B: 120 samples, cpu_avg = 50 — normal hour, dense samples
	// Un-weighted average would be 30; weighted (by sample_count) is
	// (10*3 + 50*120) / (3+120) = 6030/123 ≈ 49.02
	a, b := 10.0, 50.0
	rows := []model.NodeMetricSampleHourly{
		{NodeID: 1, BucketStart: now.Add(-2 * time.Hour), CpuPctAvg: &a, SampleCount: 3, ProbeOK: 3},
		{NodeID: 1, BucketStart: now.Add(-1 * time.Hour), CpuPctAvg: &b, SampleCount: 120, ProbeOK: 120},
	}
	for _, r := range rows {
		if err := db.Create(&r).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	h := NewNodeMetricsHandler(db)
	dst := map[string]float64{}
	h.fillTrend(1, now.Add(-3*time.Hour), now, dst)

	expected := (10*3.0 + 50*120.0) / 123.0
	got := dst["cpu_pct_avg"]
	if diff := got - expected; diff > 0.01 || diff < -0.01 {
		t.Fatalf("expected weighted cpu_pct_avg ≈ %.2f, got %.4f", expected, got)
	}
	// Un-weighted would have been 30 — make sure we're not silently using that.
	if got < 40 {
		t.Fatalf("weight appears not applied: %.2f too close to un-weighted 30", got)
	}
}
