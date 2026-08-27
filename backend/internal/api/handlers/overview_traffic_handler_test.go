package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openOverviewTrafficTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", handlerTestDBName(t))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	return db
}

func seedOverviewTrafficTasks(t *testing.T, db *gorm.DB, taskIDs ...uint) {
	t.Helper()
	for _, taskID := range taskIDs {
		task := model.Task{
			ID:           taskID,
			Name:         fmt.Sprintf("overview-traffic-task-%d", taskID),
			NodeID:       taskID,
			ExecutorType: "local",
			Status:       "success",
		}
		if err := db.Create(&task).Error; err != nil {
			t.Fatalf("写入 Task 失败: %v", err)
		}
	}
}

func trafficTestRouter(handler *OverviewTrafficHandler) *gin.Engine {
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.CtxRole, "admin")
		c.Set(middleware.CtxUserID, uint(1))
		c.Next()
	})
	r.GET("/overview/traffic", handler.Get)
	return r
}

func TestOverviewTrafficRejectsInvalidWindow(t *testing.T) {
	db := openOverviewTrafficTestDB(t)
	if err := db.AutoMigrate(&model.TaskTrafficSample{}); err != nil {
		t.Fatalf("初始化采样表失败: %v", err)
	}

	handler := NewOverviewTrafficHandler(db, func() time.Time {
		return time.Date(2026, 3, 7, 12, 0, 0, 0, time.UTC)
	})
	r := trafficTestRouter(handler)

	req := httptest.NewRequest(http.MethodGet, "/overview/traffic?window=30d", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("期望状态码 400，实际: %d", resp.Code)
	}
}

func TestOverviewTrafficFormatsTimestampInServiceLocalTimezone(t *testing.T) {
	db := openOverviewTrafficTestDB(t)
	if err := db.AutoMigrate(&model.TaskTrafficSample{}, &model.TaskLog{}, &model.Task{}, &model.TaskRun{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	loc := time.FixedZone("CST", 8*3600)
	// nowFn stays in service-local TZ for response formatting; samples are stored
	// as UTC wall times (GORM/SQLite datetime has no offset — matching production writers).
	nowLocal := time.Date(2026, 3, 8, 20, 0, 0, 0, loc)
	sampledAtUTC := nowLocal.Add(-40 * time.Minute).UTC()
	sample := model.TaskTrafficSample{
		TaskID: 1, NodeID: 1,
		RunStartedAt:   sampledAtUTC,
		SampledAt:      sampledAtUTC,
		ThroughputMbps: 100,
	}
	if err := db.Create(&sample).Error; err != nil {
		t.Fatalf("写入采样失败: %v", err)
	}

	handler := NewOverviewTrafficHandler(db, func() time.Time { return nowLocal })
	r := trafficTestRouter(handler)

	req := httptest.NewRequest(http.MethodGet, "/overview/traffic?window=1h", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}

	var result struct {
		Data struct {
			GeneratedAt string `json:"generated_at"`
			Points      []struct {
				Timestamp      string  `json:"timestamp"`
				Label          string  `json:"label"`
				ThroughputMbps float64 `json:"throughput_mbps"`
			} `json:"points"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if !strings.HasSuffix(result.Data.GeneratedAt, "+08:00") {
		t.Fatalf("generated_at 应使用本地时区，实际: %s", result.Data.GeneratedAt)
	}
	for _, point := range result.Data.Points {
		if point.ThroughputMbps > 0 {
			if !strings.HasSuffix(point.Timestamp, "+08:00") {
				t.Fatalf("timestamp 应使用本地时区，实际: %s", point.Timestamp)
			}
			if point.Label != "19:20" {
				t.Fatalf("label 应为本地时间 19:20，实际: %s", point.Label)
			}
			return
		}
	}
	t.Fatalf("未找到非零 bucket")
}

func TestOverviewTrafficReturnsZeroFilledSeriesWhenEmpty(t *testing.T) {
	db := openOverviewTrafficTestDB(t)
	if err := db.AutoMigrate(&model.TaskTrafficSample{}, &model.TaskLog{}, &model.Task{}, &model.TaskRun{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	now := time.Date(2026, 3, 7, 12, 0, 0, 0, time.UTC)
	handler := NewOverviewTrafficHandler(db, func() time.Time { return now })
	r := trafficTestRouter(handler)

	req := httptest.NewRequest(http.MethodGet, "/overview/traffic?window=1h", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d", resp.Code)
	}

	var result struct {
		Data struct {
			Window         string `json:"window"`
			BucketMinutes  int    `json:"bucket_minutes"`
			HasRealSamples bool   `json:"has_real_samples"`
			Points         []struct {
				Timestamp       string  `json:"timestamp"`
				TimestampMs     int64   `json:"timestamp_ms"`
				ThroughputMbps  float64 `json:"throughput_mbps"`
				SampleCount     int     `json:"sample_count"`
				ActiveTaskCount int     `json:"active_task_count"`
				StartedCount    int     `json:"started_count"`
				FailedCount     int     `json:"failed_count"`
			} `json:"points"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if result.Data.Window != "1h" {
		t.Fatalf("window 应为 1h，实际: %s", result.Data.Window)
	}
	if result.Data.BucketMinutes != 5 {
		t.Fatalf("bucket_minutes 应为 5，实际: %d", result.Data.BucketMinutes)
	}
	if result.Data.HasRealSamples {
		t.Fatalf("空数据时 has_real_samples 应为 false")
	}
	if len(result.Data.Points) != 12 {
		t.Fatalf("1h 应返回 12 个 bucket，实际: %d", len(result.Data.Points))
	}
	for _, point := range result.Data.Points {
		if point.ThroughputMbps != 0 || point.SampleCount != 0 || point.ActiveTaskCount != 0 || point.StartedCount != 0 || point.FailedCount != 0 {
			t.Fatalf("空数据点应全部为 0，实际: %+v", point)
		}
		if point.TimestampMs <= 0 {
			t.Fatalf("timestamp_ms 应存在，实际: %d", point.TimestampMs)
		}
	}
}

func TestOverviewTrafficAveragesWithinBucketAcrossMinutes(t *testing.T) {
	db := openOverviewTrafficTestDB(t)
	if err := db.AutoMigrate(&model.TaskTrafficSample{}, &model.TaskLog{}, &model.Task{}, &model.TaskRun{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	now := time.Date(2026, 3, 7, 12, 0, 0, 0, time.UTC)
	samples := []model.TaskTrafficSample{
		{TaskID: 1, NodeID: 1, RunStartedAt: now.Add(-50 * time.Minute), SampledAt: now.Add(-50 * time.Minute), ThroughputMbps: 100},
		{TaskID: 1, NodeID: 1, RunStartedAt: now.Add(-48 * time.Minute), SampledAt: now.Add(-48 * time.Minute), ThroughputMbps: 140},
	}
	for _, sample := range samples {
		if err := db.Create(&sample).Error; err != nil {
			t.Fatalf("写入采样失败: %v", err)
		}
	}

	handler := NewOverviewTrafficHandler(db, func() time.Time { return now })
	r := trafficTestRouter(handler)

	req := httptest.NewRequest(http.MethodGet, "/overview/traffic?window=1h", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	var result struct {
		Data struct {
			Points []struct {
				ThroughputMbps float64 `json:"throughput_mbps"`
				SampleCount    int     `json:"sample_count"`
			} `json:"points"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	for _, point := range result.Data.Points {
		if point.ThroughputMbps > 0 {
			if point.SampleCount != 2 {
				t.Fatalf("期望 sample_count 为 2，实际: %d", point.SampleCount)
			}
			if point.ThroughputMbps != 120 {
				t.Fatalf("期望 bucket 平均总吞吐为 120，实际: %v", point.ThroughputMbps)
			}
			return
		}
	}
	t.Fatalf("未找到非零 bucket")
}

func TestOverviewTrafficUsesTaskRunForStartedCount(t *testing.T) {
	db := openOverviewTrafficTestDB(t)
	if err := db.AutoMigrate(&model.TaskTrafficSample{}, &model.TaskLog{}, &model.Task{}, &model.TaskRun{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}
	seedOverviewTrafficTasks(t, db, 1)

	now := time.Date(2026, 3, 7, 12, 0, 0, 0, time.UTC)
	startedAt := now.Add(-6 * time.Minute)
	taskRun := model.TaskRun{
		TaskID:    1,
		Status:    "success",
		StartedAt: &startedAt,
	}
	if err := db.Create(&taskRun).Error; err != nil {
		t.Fatalf("写入 TaskRun 失败: %v", err)
	}

	handler := NewOverviewTrafficHandler(db, func() time.Time { return now })
	r := trafficTestRouter(handler)

	req := httptest.NewRequest(http.MethodGet, "/overview/traffic?window=1h", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	var result struct {
		Data struct {
			Points []struct {
				StartedCount int `json:"started_count"`
			} `json:"points"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	for _, point := range result.Data.Points {
		if point.StartedCount == 1 {
			return
		}
	}
	t.Fatalf("期望至少存在一个 started_count = 1 的 bucket")
}

func TestOverviewTrafficIncludesActivityAndEventCounts(t *testing.T) {
	db := openOverviewTrafficTestDB(t)
	if err := db.AutoMigrate(&model.TaskTrafficSample{}, &model.TaskLog{}, &model.Task{}, &model.TaskRun{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}
	seedOverviewTrafficTasks(t, db, 1, 2, 3)

	now := time.Date(2026, 3, 7, 12, 0, 0, 0, time.UTC)
	samples := []model.TaskTrafficSample{
		{TaskID: 1, NodeID: 1, RunStartedAt: now.Add(-50 * time.Minute), SampledAt: now.Add(-50 * time.Minute), ThroughputMbps: 100},
		{TaskID: 2, NodeID: 1, RunStartedAt: now.Add(-50 * time.Minute), SampledAt: now.Add(-50 * time.Minute), ThroughputMbps: 140},
		{TaskID: 3, NodeID: 1, RunStartedAt: now.Add(-6 * time.Minute), SampledAt: now.Add(-6 * time.Minute), ThroughputMbps: 80},
	}
	for _, sample := range samples {
		if err := db.Create(&sample).Error; err != nil {
			t.Fatalf("写入采样失败: %v", err)
		}
	}
	started1 := now.Add(-50 * time.Minute)
	started2 := now.Add(-50 * time.Minute)
	started3 := now.Add(-6 * time.Minute)
	finished3 := now.Add(-6 * time.Minute)
	taskRuns := []model.TaskRun{
		{TaskID: 1, Status: "success", StartedAt: &started1},
		{TaskID: 2, Status: "success", StartedAt: &started2},
		{TaskID: 3, Status: "failed", StartedAt: &started3, FinishedAt: &finished3},
	}
	for _, run := range taskRuns {
		if err := db.Create(&run).Error; err != nil {
			t.Fatalf("写入 TaskRun 失败: %v", err)
		}
	}

	handler := NewOverviewTrafficHandler(db, func() time.Time { return now })
	r := trafficTestRouter(handler)

	req := httptest.NewRequest(http.MethodGet, "/overview/traffic?window=1h", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	var result struct {
		Data struct {
			Points []struct {
				ActiveTaskCount int `json:"active_task_count"`
				StartedCount    int `json:"started_count"`
				FailedCount     int `json:"failed_count"`
			} `json:"points"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	hasFirst := false
	hasLast := false
	for _, point := range result.Data.Points {
		if point.ActiveTaskCount == 2 && point.StartedCount == 2 {
			hasFirst = true
		}
		if point.ActiveTaskCount == 1 && point.StartedCount == 1 && point.FailedCount == 1 {
			hasLast = true
		}
	}
	if !hasFirst || !hasLast {
		t.Fatalf("期望命中活动与失败事件 bucket，实际未满足：hasFirst=%v hasLast=%v", hasFirst, hasLast)
	}
}

func TestOverviewTrafficAggregatesSamplesByWindowBucket(t *testing.T) {
	db := openOverviewTrafficTestDB(t)
	if err := db.AutoMigrate(&model.TaskTrafficSample{}, &model.TaskLog{}, &model.Task{}, &model.TaskRun{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	now := time.Date(2026, 3, 7, 12, 0, 0, 0, time.UTC)
	samples := []model.TaskTrafficSample{
		{TaskID: 1, NodeID: 1, RunStartedAt: now.Add(-50 * time.Minute), SampledAt: now.Add(-50 * time.Minute), ThroughputMbps: 100},
		{TaskID: 2, NodeID: 1, RunStartedAt: now.Add(-50 * time.Minute), SampledAt: now.Add(-50 * time.Minute), ThroughputMbps: 140},
		{TaskID: 3, NodeID: 1, RunStartedAt: now.Add(-6 * time.Minute), SampledAt: now.Add(-6 * time.Minute), ThroughputMbps: 80},
	}
	for _, sample := range samples {
		if err := db.Create(&sample).Error; err != nil {
			t.Fatalf("写入采样失败: %v", err)
		}
	}

	handler := NewOverviewTrafficHandler(db, func() time.Time { return now })
	r := trafficTestRouter(handler)

	req := httptest.NewRequest(http.MethodGet, "/overview/traffic?window=1h", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d", resp.Code)
	}

	var result struct {
		Data struct {
			HasRealSamples bool `json:"has_real_samples"`
			Points         []struct {
				ThroughputMbps float64 `json:"throughput_mbps"`
				SampleCount    int     `json:"sample_count"`
			} `json:"points"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if !result.Data.HasRealSamples {
		t.Fatalf("存在采样时 has_real_samples 应为 true")
	}

	has240 := false
	has80 := false
	for _, point := range result.Data.Points {
		if point.SampleCount == 1 && point.ThroughputMbps == 240 {
			has240 = true
		}
		if point.SampleCount == 1 && point.ThroughputMbps == 80 {
			has80 = true
		}
	}
	if !has240 || !has80 {
		t.Fatalf("聚合结果不正确：has240=%v has80=%v points=%+v", has240, has80, result.Data.Points)
	}
}

func TestOverviewTraffic24hReturns48Buckets(t *testing.T) {
	db := openOverviewTrafficTestDB(t)
	if err := db.AutoMigrate(&model.TaskTrafficSample{}, &model.TaskLog{}, &model.Task{}, &model.TaskRun{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	now := time.Date(2026, 3, 7, 12, 0, 0, 0, time.UTC)
	handler := NewOverviewTrafficHandler(db, func() time.Time { return now })
	r := trafficTestRouter(handler)

	req := httptest.NewRequest(http.MethodGet, "/overview/traffic?window=24h", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	var result struct {
		Data struct {
			BucketMinutes int        `json:"bucket_minutes"`
			Points        []struct{} `json:"points"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if result.Data.BucketMinutes != 30 {
		t.Fatalf("24h bucket_minutes 应为 30，实际: %d", result.Data.BucketMinutes)
	}
	if len(result.Data.Points) != 48 {
		t.Fatalf("24h 应返回 48 个 bucket，实际: %d", len(result.Data.Points))
	}
}

func TestOverviewTraffic7dReturns56Buckets(t *testing.T) {
	db := openOverviewTrafficTestDB(t)
	if err := db.AutoMigrate(&model.TaskTrafficSample{}, &model.TaskLog{}, &model.Task{}, &model.TaskRun{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	now := time.Date(2026, 3, 7, 12, 0, 0, 0, time.UTC)
	handler := NewOverviewTrafficHandler(db, func() time.Time { return now })
	r := trafficTestRouter(handler)

	req := httptest.NewRequest(http.MethodGet, "/overview/traffic?window=7d", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	var result struct {
		Data struct {
			BucketMinutes int        `json:"bucket_minutes"`
			Points        []struct{} `json:"points"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if result.Data.BucketMinutes != 180 {
		t.Fatalf("7d bucket_minutes 应为 180，实际: %d", result.Data.BucketMinutes)
	}
	if len(result.Data.Points) != 56 {
		t.Fatalf("7d 应返回 56 个 bucket，实际: %d", len(result.Data.Points))
	}
}

func TestOverviewTrafficSetsTruncatedWhenSampleCapHit(t *testing.T) {
	db := openOverviewTrafficTestDB(t)
	if err := db.AutoMigrate(&model.TaskTrafficSample{}, &model.TaskLog{}, &model.Task{}, &model.TaskRun{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	prevCap := trafficSampleCap
	trafficSampleCap = 2
	t.Cleanup(func() { trafficSampleCap = prevCap })

	now := time.Date(2026, 3, 7, 12, 0, 0, 0, time.UTC)
	// Three samples in-window → cap=2 triggers Truncated.
	for i := 0; i < 3; i++ {
		sample := model.TaskTrafficSample{
			TaskID:         uint(i + 1),
			NodeID:         1,
			RunStartedAt:   now.Add(-30 * time.Minute),
			SampledAt:      now.Add(-time.Duration(10+i) * time.Minute),
			ThroughputMbps: 50,
		}
		if err := db.Create(&sample).Error; err != nil {
			t.Fatalf("写入采样失败: %v", err)
		}
	}

	handler := NewOverviewTrafficHandler(db, func() time.Time { return now })
	r := trafficTestRouter(handler)
	req := httptest.NewRequest(http.MethodGet, "/overview/traffic?window=1h", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("期望 200，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}

	var result struct {
		Data struct {
			HasRealSamples bool `json:"has_real_samples"`
			Truncated      bool `json:"truncated"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if !result.Data.HasRealSamples {
		t.Fatal("截断场景仍应 has_real_samples=true")
	}
	if !result.Data.Truncated {
		t.Fatal("超过 trafficSampleCap 时应 truncated=true，避免缺失桶被伪装为零")
	}
}

func TestOverviewTrafficSetsTruncatedWhenTaskRunCapHit(t *testing.T) {
	db := openOverviewTrafficTestDB(t)
	if err := db.AutoMigrate(&model.TaskTrafficSample{}, &model.TaskLog{}, &model.Task{}, &model.TaskRun{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}
	seedOverviewTrafficTasks(t, db, 1, 2, 3)

	prevCap := trafficTaskRunCap
	trafficTaskRunCap = 2
	t.Cleanup(func() { trafficTaskRunCap = prevCap })

	now := time.Date(2026, 3, 7, 12, 0, 0, 0, time.UTC)
	// Three started runs in-window → cap=2 triggers Truncated.
	for i := 0; i < 3; i++ {
		started := now.Add(-time.Duration(10+i) * time.Minute)
		run := model.TaskRun{TaskID: uint(i + 1), Status: "success", StartedAt: &started}
		if err := db.Create(&run).Error; err != nil {
			t.Fatalf("写入 TaskRun 失败: %v", err)
		}
	}

	handler := NewOverviewTrafficHandler(db, func() time.Time { return now })
	r := trafficTestRouter(handler)
	req := httptest.NewRequest(http.MethodGet, "/overview/traffic?window=1h", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("期望 200，实际: %d，响应: %s", resp.Code, resp.Body.String())
	}

	var result struct {
		Data struct {
			Truncated bool `json:"truncated"`
			Points    []struct {
				StartedCount int `json:"started_count"`
			} `json:"points"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if !result.Data.Truncated {
		t.Fatal("超过 trafficTaskRunCap 时应 truncated=true")
	}
	totalStarted := 0
	for _, p := range result.Data.Points {
		totalStarted += p.StartedCount
	}
	if totalStarted != 2 {
		t.Fatalf("截断后 started 合计应为 2，实际 %d", totalStarted)
	}
}
