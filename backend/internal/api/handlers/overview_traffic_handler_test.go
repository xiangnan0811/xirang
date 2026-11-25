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

func TestOverviewTrafficAggregates10sSlicesWithout6xInflation(t *testing.T) {
	db := openOverviewTrafficTestDB(t)
	if err := db.AutoMigrate(&model.TaskTrafficSample{}, &model.TaskLog{}, &model.Task{}, &model.TaskRun{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	now := time.Date(2026, 3, 7, 12, 0, 0, 0, time.UTC)
	minuteStart := now.Add(-10 * time.Minute)
	// 单任务按 10s 节流窗口在 1 分钟内写入 6 条 100 Mbps 样本，聚合后仍应为 ~100 Mbps（不能变成 600 Mbps）。
	for i := 0; i < 6; i++ {
		sampledAt := minuteStart.Add(time.Duration(i*10) * time.Second)
		sample := model.TaskTrafficSample{
			TaskID: 1, NodeID: 1, RunStartedAt: minuteStart, SampledAt: sampledAt, ThroughputMbps: 100,
		}
		if err := db.Create(&sample).Error; err != nil {
			t.Fatalf("写入采样失败: %v", err)
		}
	}

	// 另一桶内同一任务在同一 10s 切片出现两条样本（100 和 120），应取切片内平均 110 而非翻倍为 220。
	otherSlice := now.Add(-20 * time.Minute)
	for _, s := range []model.TaskTrafficSample{
		{TaskID: 1, NodeID: 1, RunStartedAt: otherSlice, SampledAt: otherSlice.Add(2 * time.Second), ThroughputMbps: 100},
		{TaskID: 1, NodeID: 1, RunStartedAt: otherSlice, SampledAt: otherSlice.Add(5 * time.Second), ThroughputMbps: 120},
	} {
		if err := db.Create(&s).Error; err != nil {
			t.Fatalf("写入同切片重复采样失败: %v", err)
		}
	}

	handler := NewOverviewTrafficHandler(db, func() time.Time { return now })
	r := trafficTestRouter(handler)
	req := httptest.NewRequest(http.MethodGet, "/overview/traffic?window=1h", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("期望 200，实际: %d body=%s", resp.Code, resp.Body.String())
	}

	var result struct {
		Data struct {
			Points []struct {
				TimestampMs     int64   `json:"timestamp_ms"`
				ThroughputMbps  float64 `json:"throughput_mbps"`
				SampleCount     int     `json:"sample_count"`
				ActiveTaskCount int     `json:"active_task_count"`
			} `json:"points"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}

	var found6Slices, foundDedupSlice bool
	for _, p := range result.Data.Points {
		if p.TimestampMs == minuteStart.UnixMilli() {
			found6Slices = true
			if p.ThroughputMbps != 100 || p.SampleCount != 1 || p.ActiveTaskCount != 1 {
				t.Fatalf("单任务 6 样本/分钟聚合错误: %+v (want throughput=100, sample_count=1, active_task_count=1)", p)
			}
		}
		if p.TimestampMs == otherSlice.UnixMilli() {
			foundDedupSlice = true
			if p.ThroughputMbps != 110 || p.SampleCount != 1 || p.ActiveTaskCount != 1 {
				t.Fatalf("同 10s 切片重复样本聚合错误: %+v (want throughput=110, sample_count=1, active_task_count=1)", p)
			}
		}
	}
	if !found6Slices || !foundDedupSlice {
		t.Fatalf("未命中目标时间桶: found6Slices=%v foundDedupSlice=%v", found6Slices, foundDedupSlice)
	}
}

func TestOverviewTrafficSumsConcurrentTasksAndSeparatesSequentialTasksPer10sSlice(t *testing.T) {
	db := openOverviewTrafficTestDB(t)
	if err := db.AutoMigrate(&model.TaskTrafficSample{}, &model.TaskLog{}, &model.Task{}, &model.TaskRun{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	now := time.Date(2026, 3, 7, 12, 0, 0, 0, time.UTC)
	concurrentBucket := now.Add(-15 * time.Minute)
	// 两个并发任务在同一分钟内各自按 10s 切片上报 100 Mbps -> 合计 ~200 Mbps，active_task_count=2。
	for i := 0; i < 6; i++ {
		ts := concurrentBucket.Add(time.Duration(i*10) * time.Second)
		for _, taskID := range []uint{1, 2} {
			s := model.TaskTrafficSample{
				TaskID: taskID, NodeID: 1, RunStartedAt: concurrentBucket, SampledAt: ts, ThroughputMbps: 100,
			}
			if err := db.Create(&s).Error; err != nil {
				t.Fatalf("写入并发采样失败: %v", err)
			}
		}
	}

	// 同分钟内串行不重叠的两个任务（任务 3 在 :00/:10/:20，任务 4 在 :30/:40/:50）-> 平均 ~100 Mbps，active_task_count=1。
	sequentialBucket := now.Add(-30 * time.Minute)
	for i := 0; i < 3; i++ {
		if err := db.Create(&model.TaskTrafficSample{
			TaskID: 3, NodeID: 1, RunStartedAt: sequentialBucket,
			SampledAt: sequentialBucket.Add(time.Duration(i*10) * time.Second), ThroughputMbps: 100,
		}).Error; err != nil {
			t.Fatalf("写入串行任务 3 采样失败: %v", err)
		}
		if err := db.Create(&model.TaskTrafficSample{
			TaskID: 4, NodeID: 1, RunStartedAt: sequentialBucket,
			SampledAt: sequentialBucket.Add(time.Duration((i+3)*10) * time.Second), ThroughputMbps: 100,
		}).Error; err != nil {
			t.Fatalf("写入串行任务 4 采样失败: %v", err)
		}
	}

	handler := NewOverviewTrafficHandler(db, func() time.Time { return now })
	r := trafficTestRouter(handler)
	req := httptest.NewRequest(http.MethodGet, "/overview/traffic?window=1h", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("期望 200，实际: %d", resp.Code)
	}

	var result struct {
		Data struct {
			Points []struct {
				TimestampMs     int64   `json:"timestamp_ms"`
				ThroughputMbps  float64 `json:"throughput_mbps"`
				SampleCount     int     `json:"sample_count"`
				ActiveTaskCount int     `json:"active_task_count"`
			} `json:"points"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}

	var foundConcurrent, foundSequential bool
	for _, p := range result.Data.Points {
		if p.TimestampMs == concurrentBucket.UnixMilli() {
			foundConcurrent = true
			if p.ThroughputMbps != 200 || p.SampleCount != 1 || p.ActiveTaskCount != 2 {
				t.Fatalf("并发任务聚合错误: %+v (want throughput=200, sample_count=1, active_task_count=2)", p)
			}
		}
		if p.TimestampMs == sequentialBucket.UnixMilli() {
			foundSequential = true
			if p.ThroughputMbps != 100 || p.SampleCount != 1 || p.ActiveTaskCount != 1 {
				t.Fatalf("串行任务聚合错误: %+v (want throughput=100, sample_count=1, active_task_count=1)", p)
			}
		}
	}
	if !foundConcurrent || !foundSequential {
		t.Fatalf("未命中测试桶: concurrent=%v sequential=%v", foundConcurrent, foundSequential)
	}
}

func TestOverviewTrafficIncludesTrailingPartialBucketAcrossWindows(t *testing.T) {
	db := openOverviewTrafficTestDB(t)
	if err := db.AutoMigrate(&model.User{}, &model.Node{}, &model.NodeOwner{}, &model.TaskTrafficSample{}, &model.TaskLog{}, &model.Task{}, &model.TaskRun{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}
	seedOverviewTrafficTasks(t, db, 1)

	now := time.Date(2026, 3, 7, 12, 17, 0, 0, time.UTC)
	// 1h: currentBucketStart=12:15 -> 12 个完整桶 (11:15..12:10) + 1 个部分桶 (12:15) = 13 个点
	// 24h: currentBucketStart=12:00 -> 48 个完整桶 + 1 个部分桶 (12:00) = 49 个点
	// 7d: currentBucketStart=12:00 -> 56 个完整桶 + 1 个部分桶 (12:00) = 57 个点
	sampleAt := time.Date(2026, 3, 7, 12, 16, 10, 0, time.UTC)
	futureSampleAt := time.Date(2026, 3, 7, 12, 18, 0, 0, time.UTC)
	startedAt := time.Date(2026, 3, 7, 12, 15, 30, 0, time.UTC)
	failedAt := now // exact now inside partial bucket
	for _, s := range []model.TaskTrafficSample{
		{TaskID: 1, NodeID: 1, RunStartedAt: startedAt, SampledAt: sampleAt, ThroughputMbps: 75},
		{TaskID: 1, NodeID: 1, RunStartedAt: startedAt, SampledAt: futureSampleAt, ThroughputMbps: 999},
	} {
		if err := db.Create(&s).Error; err != nil {
			t.Fatalf("写入采样失败: %v", err)
		}
	}
	run := model.TaskRun{TaskID: 1, Status: "failed", StartedAt: &startedAt, FinishedAt: &failedAt}
	if err := db.Create(&run).Error; err != nil {
		t.Fatalf("写入 TaskRun 失败: %v", err)
	}

	handler := NewOverviewTrafficHandler(db, func() time.Time { return now })
	r := trafficTestRouter(handler)

	for _, tc := range []struct {
		window           string
		wantCount        int
		wantPartialStart time.Time
	}{
		{window: "1h", wantCount: 13, wantPartialStart: time.Date(2026, 3, 7, 12, 15, 0, 0, time.UTC)},
		{window: "24h", wantCount: 49, wantPartialStart: time.Date(2026, 3, 7, 12, 0, 0, 0, time.UTC)},
		{window: "7d", wantCount: 57, wantPartialStart: time.Date(2026, 3, 7, 12, 0, 0, 0, time.UTC)},
	} {
		req := httptest.NewRequest(http.MethodGet, "/overview/traffic?window="+tc.window, nil)
		resp := httptest.NewRecorder()
		r.ServeHTTP(resp, req)
		if resp.Code != http.StatusOK {
			t.Fatalf("%s 期望 200，实际: %d body=%s", tc.window, resp.Code, resp.Body.String())
		}
		var result struct {
			Data struct {
				HasRealSamples bool `json:"has_real_samples"`
				Points         []struct {
					TimestampMs     int64   `json:"timestamp_ms"`
					ThroughputMbps  float64 `json:"throughput_mbps"`
					ActiveTaskCount int     `json:"active_task_count"`
					StartedCount    int     `json:"started_count"`
					FailedCount     int     `json:"failed_count"`
					Partial         bool    `json:"partial"`
				} `json:"points"`
			} `json:"data"`
		}
		if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
			t.Fatalf("%s 解析响应失败: %v", tc.window, err)
		}
		if !result.Data.HasRealSamples {
			t.Fatalf("%s 存在当前桶采样时 has_real_samples 应为 true", tc.window)
		}
		if len(result.Data.Points) != tc.wantCount {
			t.Fatalf("%s 期望 %d 个桶，实际: %d", tc.window, tc.wantCount, len(result.Data.Points))
		}
		for i, p := range result.Data.Points {
			isLast := i == len(result.Data.Points)-1
			if isLast {
				if !p.Partial || p.TimestampMs != tc.wantPartialStart.UnixMilli() {
					t.Fatalf("%s 末尾桶应标记 partial=true 且起始于 %v，实际: %+v", tc.window, tc.wantPartialStart, p)
				}
				if p.ThroughputMbps != 75 || p.ActiveTaskCount != 1 || p.StartedCount != 1 || p.FailedCount != 1 {
					t.Fatalf("%s 末尾桶应包含当前进行中的数据且排除未来样本: %+v", tc.window, p)
				}
			} else if p.Partial {
				t.Fatalf("%s 历史完整桶 [%d] 不应标记 partial=true", tc.window, i)
			}
		}
	}

	// 空拥有节点的 operator 在桶中间时间也应得到带 partial=true 的尾桶。
	op := model.User{Username: "traffic-empty-op", Role: "operator", PasswordHash: "x"}
	if err := db.Create(&op).Error; err != nil {
		t.Fatalf("创建 operator 失败: %v", err)
	}
	opRouter := gin.New()
	opRouter.Use(func(c *gin.Context) {
		c.Set(middleware.CtxRole, "operator")
		c.Set(middleware.CtxUserID, op.ID)
		c.Next()
	})
	opRouter.GET("/overview/traffic", handler.Get)
	opReq := httptest.NewRequest(http.MethodGet, "/overview/traffic?window=1h", nil)
	opResp := httptest.NewRecorder()
	opRouter.ServeHTTP(opResp, opReq)
	if opResp.Code != http.StatusOK {
		t.Fatalf("operator 空拥有节点期望 200，实际: %d", opResp.Code)
	}
	var opResult struct {
		Data struct {
			Points []struct {
				Partial bool `json:"partial"`
			} `json:"points"`
		} `json:"data"`
	}
	if err := json.Unmarshal(opResp.Body.Bytes(), &opResult); err != nil {
		t.Fatalf("operator 解析响应失败: %v", err)
	}
	if len(opResult.Data.Points) != 13 || !opResult.Data.Points[12].Partial || opResult.Data.Points[0].Partial {
		t.Fatalf("operator 空拥有节点部分桶状态错误: %+v", opResult.Data.Points)
	}
}

func TestOverviewTrafficScopesEventsByTaskRunNodeSnapshotAfterMigration(t *testing.T) {
	db := openOverviewTrafficTestDB(t)
	if err := db.AutoMigrate(&model.User{}, &model.Node{}, &model.NodeOwner{}, &model.TaskTrafficSample{}, &model.TaskLog{}, &model.Task{}, &model.TaskRun{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}

	opA := model.User{Username: "traffic-op-a", Role: "operator", PasswordHash: "x"}
	opB := model.User{Username: "traffic-op-b", Role: "operator", PasswordHash: "x"}
	nodeA := model.Node{Name: "node-a", Host: "10.0.20.1", Username: "root", AuthType: "key", BackupDir: "node-a"}
	nodeB := model.Node{Name: "node-b", Host: "10.0.20.2", Username: "root", AuthType: "key", BackupDir: "node-b"}
	for _, rec := range []any{&opA, &opB, &nodeA, &nodeB} {
		if err := db.Create(rec).Error; err != nil {
			t.Fatalf("创建夹具失败: %v", err)
		}
	}
	if err := db.Create(&model.NodeOwner{NodeID: nodeA.ID, UserID: opA.ID}).Error; err != nil {
		t.Fatalf("绑定 opA 节点失败: %v", err)
	}
	if err := db.Create(&model.NodeOwner{NodeID: nodeB.ID, UserID: opB.ID}).Error; err != nil {
		t.Fatalf("绑定 opB 节点失败: %v", err)
	}

	// 先在 nodeA 上创建任务并写入历史运行记录（node_id_snapshot = nodeA.ID），随后将任务迁移到 nodeB。
	migratedTask := model.Task{Name: "migrated-task", NodeID: nodeA.ID, Status: "failed"}
	if err := db.Create(&migratedTask).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	now := time.Date(2026, 3, 7, 12, 17, 0, 0, time.UTC)
	startedAt := time.Date(2026, 3, 7, 12, 15, 30, 0, time.UTC)
	failedAt := time.Date(2026, 3, 7, 12, 16, 0, 0, time.UTC)
	if err := db.Create(&model.TaskRun{
		TaskID:         migratedTask.ID,
		NodeIDSnapshot: nodeA.ID,
		Status:         "failed",
		StartedAt:      &startedAt,
		FinishedAt:     &failedAt,
	}).Error; err != nil {
		t.Fatalf("创建 TaskRun 失败: %v", err)
	}
	if err := db.Model(&migratedTask).Update("node_id", nodeB.ID).Error; err != nil {
		t.Fatalf("迁移任务节点失败: %v", err)
	}

	handler := NewOverviewTrafficHandler(db, func() time.Time { return now })
	queryForOp := func(userID uint) (int, int) {
		r := gin.New()
		r.Use(func(c *gin.Context) {
			c.Set(middleware.CtxRole, "operator")
			c.Set(middleware.CtxUserID, userID)
			c.Next()
		})
		r.GET("/overview/traffic", handler.Get)
		resp := httptest.NewRecorder()
		r.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/overview/traffic?window=1h", nil))
		if resp.Code != http.StatusOK {
			t.Fatalf("期望 200，实际: %d body=%s", resp.Code, resp.Body.String())
		}
		var result struct {
			Data struct {
				Points []struct {
					StartedCount int `json:"started_count"`
					FailedCount  int `json:"failed_count"`
				} `json:"points"`
			} `json:"data"`
		}
		if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
			t.Fatalf("解析响应失败: %v", err)
		}
		var started, failed int
		for _, p := range result.Data.Points {
			started += p.StartedCount
			failed += p.FailedCount
		}
		return started, failed
	}

	aStarted, aFailed := queryForOp(opA.ID)
	if aStarted != 1 || aFailed != 1 {
		t.Fatalf("nodeA 的 operator 应看到历史事件 (1,1)，实际 (%d,%d)", aStarted, aFailed)
	}
	bStarted, bFailed := queryForOp(opB.ID)
	if bStarted != 0 || bFailed != 0 {
		t.Fatalf("任务迁入 nodeB 后，nodeB 的 operator 不应看到发生在 nodeA 的事件，实际 (%d,%d)", bStarted, bFailed)
	}
}

func TestOverviewTrafficCapPreservesNewestBuckets(t *testing.T) {
	previousSampleCap, previousRunCap := trafficSampleCap, trafficTaskRunCap
	trafficSampleCap, trafficTaskRunCap = 2, 2
	t.Cleanup(func() { trafficSampleCap, trafficTaskRunCap = previousSampleCap, previousRunCap })

	now := time.Date(2026, 3, 7, 12, 17, 0, 0, time.UTC)
	currentBucket := now.Truncate(5 * time.Minute)
	tests := []struct {
		name      string
		times     []time.Duration
		retained  []time.Duration
		truncated bool
		cutoff    time.Duration
	}{
		{name: "exact limit", times: []time.Duration{-10 * time.Minute, 0}, retained: []time.Duration{-10 * time.Minute, 0}},
		{name: "usable trailing bucket", times: []time.Duration{-20 * time.Minute, -10 * time.Minute, 0}, retained: []time.Duration{-10 * time.Minute, 0}, truncated: true, cutoff: -20 * time.Minute},
		{name: "same timestamp cutoff", times: []time.Duration{-10 * time.Minute, -10 * time.Minute, 0}, retained: []time.Duration{-10 * time.Minute, 0}, truncated: true, cutoff: -10 * time.Minute},
		{name: "overflow in trailing bucket", times: []time.Duration{-time.Minute, -30 * time.Second, 0}, retained: []time.Duration{-30 * time.Second, 0}, truncated: true, cutoff: -time.Minute},
	}

	for _, engine := range []string{"sqlite", "postgres"} {
		for _, source := range []string{"sample", "started", "failed"} {
			for _, tc := range tests {
				t.Run(engine+"/"+source+"/"+tc.name, func(t *testing.T) {
					var db *gorm.DB
					if engine == "postgres" {
						db = openBackupHealthPostgresTestDB(t)
					} else {
						db = openOverviewTrafficTestDB(t)
					}
					if err := db.AutoMigrate(&model.TaskTrafficSample{}, &model.TaskLog{}, &model.Task{}, &model.TaskRun{}); err != nil {
						t.Fatalf("migrate traffic tables: %v", err)
					}
					node := model.Node{ID: 1, Name: "overview-traffic-node", Host: "10.1.1.1", Port: 22, Username: "root", BackupDir: "overview-traffic"}
					if err := db.Create(&node).Error; err != nil {
						t.Fatalf("insert traffic node: %v", err)
					}
					seedOverviewTrafficTasks(t, db, 1)
					addRow := func(at time.Time) {
						t.Helper()
						switch source {
						case "sample":
							row := model.TaskTrafficSample{TaskID: 1, NodeID: 1, RunStartedAt: now.Add(-2 * time.Hour), SampledAt: at, ThroughputMbps: 75}
							if err := db.Create(&row).Error; err != nil {
								t.Fatalf("insert sample: %v", err)
							}
						case "started":
							row := model.TaskRun{TaskID: 1, Status: "success", StartedAt: &at}
							if err := db.Create(&row).Error; err != nil {
								t.Fatalf("insert started run: %v", err)
							}
						case "failed":
							row := model.TaskRun{TaskID: 1, Status: "failed", FinishedAt: &at}
							if err := db.Create(&row).Error; err != nil {
								t.Fatalf("insert failed run: %v", err)
							}
						}
					}
					for _, offset := range tc.times {
						addRow(now.Add(offset))
					}
					// These rows must not consume the cap, even though the future row sorts first.
					addRow(now.Add(time.Minute))
					addRow(now.Add(-2 * time.Hour))

					router := trafficTestRouter(NewOverviewTrafficHandler(db, func() time.Time { return now }))
					response := httptest.NewRecorder()
					router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/overview/traffic?window=1h", nil))
					if response.Code != http.StatusOK {
						t.Fatalf("unexpected status %d: %s", response.Code, response.Body.String())
					}
					var result struct {
						Data struct {
							Truncated *bool `json:"truncated"`
							Points    []struct {
								TimestampMs       int64   `json:"timestamp_ms"`
								ThroughputMbps    float64 `json:"throughput_mbps"`
								SampleCount       int     `json:"sample_count"`
								ActiveTaskCount   int     `json:"active_task_count"`
								StartedCount      int     `json:"started_count"`
								FailedCount       int     `json:"failed_count"`
								Partial           *bool   `json:"partial"`
								Incomplete        *bool   `json:"incomplete"`
								SamplesIncomplete *bool   `json:"samples_incomplete"`
								StartedIncomplete *bool   `json:"started_incomplete"`
								FailedIncomplete  *bool   `json:"failed_incomplete"`
							} `json:"points"`
						} `json:"data"`
					}
					if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
						t.Fatalf("decode response: %v", err)
					}
					if (result.Data.Truncated != nil) != tc.truncated || (result.Data.Truncated != nil && !*result.Data.Truncated) {
						t.Fatalf("truncated = %v, want %v", result.Data.Truncated, tc.truncated)
					}
					if len(result.Data.Points) != 13 {
						t.Fatalf("got %d points, want 13", len(result.Data.Points))
					}
					retained := make(map[int64]int)
					sampleMinutes := make(map[int64]map[int64]bool)
					for _, offset := range tc.retained {
						at := now.Add(offset)
						bucket := at.Truncate(5 * time.Minute).UnixMilli()
						retained[bucket]++
						if sampleMinutes[bucket] == nil {
							sampleMinutes[bucket] = make(map[int64]bool)
						}
						sampleMinutes[bucket][at.Truncate(time.Minute).UnixMilli()] = true
					}
					cutoff := now.Add(tc.cutoff).Truncate(5 * time.Minute).UnixMilli()
					for i, point := range result.Data.Points {
						wantTime := currentBucket.Add(time.Duration(i-12) * 5 * time.Minute).UnixMilli()
						if point.TimestampMs != wantTime {
							t.Fatalf("point %d timestamp = %d, want %d", i, point.TimestampMs, wantTime)
						}
						wantIncomplete := tc.truncated && wantTime <= cutoff
						if (point.Incomplete != nil) != wantIncomplete || (point.Incomplete != nil && !*point.Incomplete) {
							t.Errorf("point %d incomplete = %v, want %v", i, point.Incomplete, wantIncomplete)
						}
						for _, flag := range []struct {
							name string
							got  *bool
							want bool
						}{
							{"samples_incomplete", point.SamplesIncomplete, source == "sample" && wantIncomplete},
							{"started_incomplete", point.StartedIncomplete, source == "started" && wantIncomplete},
							{"failed_incomplete", point.FailedIncomplete, source == "failed" && wantIncomplete},
						} {
							if (flag.got != nil) != flag.want || (flag.got != nil && !*flag.got) {
								t.Errorf("point %d %s = %v, want %v", i, flag.name, flag.got, flag.want)
							}
						}
						if (point.Partial != nil) != (i == 12) || (point.Partial != nil && !*point.Partial) {
							t.Errorf("point %d partial = %v", i, point.Partial)
						}
						count := retained[wantTime]
						if source == "sample" {
							wantThroughput, wantActive := 0.0, 0
							if count > 0 {
								wantThroughput, wantActive = 75, 1
							}
							if point.ThroughputMbps != wantThroughput || point.ActiveTaskCount != wantActive || point.SampleCount != len(sampleMinutes[wantTime]) {
								t.Errorf("point %d sample values = %+v", i, point)
							}
						} else if source == "started" && point.StartedCount != count {
							t.Errorf("point %d started = %d, want %d", i, point.StartedCount, count)
						} else if source == "failed" && point.FailedCount != count {
							t.Errorf("point %d failed = %d, want %d", i, point.FailedCount, count)
						}
					}
				})
			}
		}
	}
}

func TestOverviewTrafficUsesLatestExcludedBucketAcrossQueries(t *testing.T) {
	previousSampleCap, previousRunCap := trafficSampleCap, trafficTaskRunCap
	trafficSampleCap, trafficTaskRunCap = 1, 1
	t.Cleanup(func() { trafficSampleCap, trafficTaskRunCap = previousSampleCap, previousRunCap })
	db := openOverviewTrafficTestDB(t)
	if err := db.AutoMigrate(&model.TaskTrafficSample{}, &model.TaskLog{}, &model.Task{}, &model.TaskRun{}); err != nil {
		t.Fatalf("migrate traffic tables: %v", err)
	}
	seedOverviewTrafficTasks(t, db, 1)
	now := time.Date(2026, 3, 7, 12, 17, 0, 0, time.UTC)
	for _, offset := range []time.Duration{-10 * time.Minute, 0} {
		at := now.Add(offset)
		if err := db.Create(&model.TaskTrafficSample{TaskID: 1, NodeID: 1, RunStartedAt: now.Add(-time.Hour), SampledAt: at, ThroughputMbps: 42}).Error; err != nil {
			t.Fatalf("insert sample: %v", err)
		}
	}
	for _, offset := range []time.Duration{-20 * time.Minute, 0} {
		at := now.Add(offset)
		if err := db.Create(&model.TaskRun{TaskID: 1, Status: "success", StartedAt: &at}).Error; err != nil {
			t.Fatalf("insert started run: %v", err)
		}
	}
	for _, offset := range []time.Duration{-30 * time.Minute, 0} {
		at := now.Add(offset)
		if err := db.Create(&model.TaskRun{TaskID: 1, Status: "failed", FinishedAt: &at}).Error; err != nil {
			t.Fatalf("insert failed run: %v", err)
		}
	}
	response := httptest.NewRecorder()
	trafficTestRouter(NewOverviewTrafficHandler(db, func() time.Time { return now })).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/overview/traffic?window=1h", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", response.Code, response.Body.String())
	}
	var result struct {
		Data struct {
			Truncated bool `json:"truncated"`
			Points    []struct {
				TimestampMs       int64   `json:"timestamp_ms"`
				ThroughputMbps    float64 `json:"throughput_mbps"`
				StartedCount      int     `json:"started_count"`
				FailedCount       int     `json:"failed_count"`
				Incomplete        *bool   `json:"incomplete"`
				SamplesIncomplete *bool   `json:"samples_incomplete"`
				StartedIncomplete *bool   `json:"started_incomplete"`
				FailedIncomplete  *bool   `json:"failed_incomplete"`
				Partial           bool    `json:"partial"`
			} `json:"points"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !result.Data.Truncated || len(result.Data.Points) != 13 {
		t.Fatalf("unexpected truncated or point count: %+v", result.Data)
	}
	samplesCutoff := now.Add(-10 * time.Minute).Truncate(5 * time.Minute).UnixMilli()
	startedCutoff := now.Add(-20 * time.Minute).Truncate(5 * time.Minute).UnixMilli()
	failedCutoff := now.Add(-30 * time.Minute).Truncate(5 * time.Minute).UnixMilli()
	for _, point := range result.Data.Points {
		for _, flag := range []struct {
			name string
			got  *bool
			want bool
		}{
			{"incomplete", point.Incomplete, point.TimestampMs <= samplesCutoff},
			{"samples_incomplete", point.SamplesIncomplete, point.TimestampMs <= samplesCutoff},
			{"started_incomplete", point.StartedIncomplete, point.TimestampMs <= startedCutoff},
			{"failed_incomplete", point.FailedIncomplete, point.TimestampMs <= failedCutoff},
		} {
			if (flag.got != nil) != flag.want || (flag.got != nil && !*flag.got) {
				t.Errorf("point %d %s = %v, want %v", point.TimestampMs, flag.name, flag.got, flag.want)
			}
		}
	}
	last := result.Data.Points[len(result.Data.Points)-1]
	if !last.Partial || last.Incomplete != nil || last.ThroughputMbps != 42 || last.StartedCount != 1 || last.FailedCount != 1 {
		t.Fatalf("latest bucket lost retained rows: %+v", last)
	}
}

func TestOverviewTrafficCapFiltersOwnershipBeforeLimiting(t *testing.T) {
	previousSampleCap, previousRunCap := trafficSampleCap, trafficTaskRunCap
	trafficSampleCap, trafficTaskRunCap = 1, 1
	t.Cleanup(func() { trafficSampleCap, trafficTaskRunCap = previousSampleCap, previousRunCap })
	db := openOverviewTrafficTestDB(t)
	if err := db.AutoMigrate(&model.User{}, &model.Node{}, &model.NodeOwner{}, &model.TaskTrafficSample{}, &model.TaskLog{}, &model.Task{}, &model.TaskRun{}); err != nil {
		t.Fatalf("migrate traffic tables: %v", err)
	}
	op := model.User{Username: "traffic-cap-op", Role: "operator", PasswordHash: "x"}
	ownedNode := model.Node{Name: "owned", Host: "10.0.30.1", Username: "root", AuthType: "key", BackupDir: "owned"}
	otherNode := model.Node{Name: "other", Host: "10.0.30.2", Username: "root", AuthType: "key", BackupDir: "other"}
	for _, row := range []any{&op, &ownedNode, &otherNode} {
		if err := db.Create(row).Error; err != nil {
			t.Fatalf("insert ownership fixture: %v", err)
		}
	}
	if err := db.Create(&model.NodeOwner{NodeID: ownedNode.ID, UserID: op.ID}).Error; err != nil {
		t.Fatalf("assign node owner: %v", err)
	}
	now := time.Date(2026, 3, 7, 12, 17, 0, 0, time.UTC)
	for _, node := range []model.Node{ownedNode, otherNode} {
		task := model.Task{Name: "traffic-cap-task", NodeID: node.ID, ExecutorType: "local", Status: "success"}
		if err := db.Create(&task).Error; err != nil {
			t.Fatalf("insert task: %v", err)
		}
		at := now
		if node.ID == ownedNode.ID {
			at = now.Add(-10 * time.Minute)
		}
		if err := db.Create(&model.TaskTrafficSample{TaskID: task.ID, NodeID: node.ID, RunStartedAt: at, SampledAt: at, ThroughputMbps: 42}).Error; err != nil {
			t.Fatalf("insert sample: %v", err)
		}
		if err := db.Create(&model.TaskRun{TaskID: task.ID, Status: "failed", StartedAt: &at, FinishedAt: &at}).Error; err != nil {
			t.Fatalf("insert run: %v", err)
		}
	}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(middleware.CtxRole, "operator")
		c.Set(middleware.CtxUserID, op.ID)
		c.Next()
	})
	router.GET("/overview/traffic", NewOverviewTrafficHandler(db, func() time.Time { return now }).Get)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/overview/traffic?window=1h", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", response.Code, response.Body.String())
	}
	var result struct {
		Data struct {
			Truncated *bool `json:"truncated"`
			Points    []struct {
				TimestampMs    int64   `json:"timestamp_ms"`
				ThroughputMbps float64 `json:"throughput_mbps"`
				StartedCount   int     `json:"started_count"`
				FailedCount    int     `json:"failed_count"`
				Incomplete     *bool   `json:"incomplete"`
			} `json:"points"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result.Data.Truncated != nil {
		t.Fatal("other owner's rows must not trigger truncation")
	}
	ownedBucket := now.Add(-10 * time.Minute).Truncate(5 * time.Minute).UnixMilli()
	for _, point := range result.Data.Points {
		if point.Incomplete != nil {
			t.Fatal("untruncated points must omit incomplete")
		}
		if point.TimestampMs == ownedBucket {
			if point.ThroughputMbps != 42 || point.StartedCount != 1 || point.FailedCount != 1 {
				t.Fatalf("owned bucket lost its rows: %+v", point)
			}
		} else if point.ThroughputMbps != 0 || point.StartedCount != 0 || point.FailedCount != 0 {
			t.Fatalf("other owner's data appeared: %+v", point)
		}
	}
}
