package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openOverviewTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	return db
}

func migrateOverviewTables(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.AutoMigrate(&model.Node{}, &model.Policy{}, &model.Task{}, &model.TaskTrafficSample{}); err != nil {
		t.Fatalf("初始化测试数据表失败: %v", err)
	}
}

func timePtr(t time.Time) *time.Time { return &t }

func overviewGet(t *testing.T, db *gorm.DB) *httptest.ResponseRecorder {
	t.Helper()
	r := gin.New()
	r.GET("/overview", NewOverviewHandler(db).Get)
	req := httptest.NewRequest(http.MethodGet, "/overview", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	return resp
}

type overviewBody struct {
	Data struct {
		TotalNodes            int     `json:"totalNodes"`
		HealthyNodes          int     `json:"healthyNodes"`
		ActivePolicies        int     `json:"activePolicies"`
		RunningTasks          int     `json:"runningTasks"`
		FailedTasks24h        int     `json:"failedTasks24h"`
		CurrentThroughputMbps float64 `json:"currentThroughputMbps"`
	} `json:"data"`
}

func parseOverview(t *testing.T, resp *httptest.ResponseRecorder) overviewBody {
	t.Helper()
	if resp.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，实际: %d", resp.Code)
	}
	var body overviewBody
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	return body
}

func TestOverviewReturnsZeroCounts(t *testing.T) {
	db := openOverviewTestDB(t)
	migrateOverviewTables(t, db)

	body := parseOverview(t, overviewGet(t, db))
	if body.Data.CurrentThroughputMbps != 0 {
		t.Errorf("无采样时吞吐应为 0，实际: %f", body.Data.CurrentThroughputMbps)
	}
}

func TestOverviewThroughputSumsRunningTasks(t *testing.T) {
	db := openOverviewTestDB(t)
	migrateOverviewTables(t, db)

	now := time.Now().UTC()
	run1Start := now.Add(-5 * time.Minute)
	run2Start := now.Add(-3 * time.Minute)

	task1 := model.Task{Name: "task1", Status: "running", NodeID: 1, LastRunAt: timePtr(run1Start)}
	task2 := model.Task{Name: "task2", Status: "running", NodeID: 1, LastRunAt: timePtr(run2Start)}
	db.Create(&task1)
	db.Create(&task2)

	samples := []model.TaskTrafficSample{
		{TaskID: task1.ID, NodeID: 1, RunStartedAt: run1Start, SampledAt: now.Add(-30 * time.Second), ThroughputMbps: 50.0},
		{TaskID: task1.ID, NodeID: 1, RunStartedAt: run1Start, SampledAt: now.Add(-10 * time.Second), ThroughputMbps: 80.5},
		{TaskID: task2.ID, NodeID: 1, RunStartedAt: run2Start, SampledAt: now.Add(-20 * time.Second), ThroughputMbps: 30.0},
		{TaskID: task2.ID, NodeID: 1, RunStartedAt: run2Start, SampledAt: now.Add(-5 * time.Second), ThroughputMbps: 19.5},
	}
	for i := range samples {
		db.Create(&samples[i])
	}

	body := parseOverview(t, overviewGet(t, db))

	// task1 最新 80.5 + task2 最新 19.5 = 100.0
	if body.Data.CurrentThroughputMbps != 100.0 {
		t.Errorf("期望吞吐 100.0，实际: %f", body.Data.CurrentThroughputMbps)
	}
	if body.Data.RunningTasks != 2 {
		t.Errorf("期望 runningTasks=2，实际: %d", body.Data.RunningTasks)
	}
}

func TestOverviewThroughputExcludesNonRunningTasks(t *testing.T) {
	db := openOverviewTestDB(t)
	migrateOverviewTables(t, db)

	now := time.Now().UTC()
	runStart := now.Add(-2 * time.Minute)

	runningTask := model.Task{Name: "running", Status: "running", NodeID: 1, LastRunAt: timePtr(runStart)}
	successTask := model.Task{Name: "done", Status: "success", NodeID: 1, LastRunAt: timePtr(now.Add(-5 * time.Minute))}
	db.Create(&runningTask)
	db.Create(&successTask)

	samples := []model.TaskTrafficSample{
		{TaskID: runningTask.ID, NodeID: 1, RunStartedAt: runStart, SampledAt: now.Add(-10 * time.Second), ThroughputMbps: 42.3},
		{TaskID: successTask.ID, NodeID: 1, RunStartedAt: now.Add(-5 * time.Minute), SampledAt: now.Add(-10 * time.Second), ThroughputMbps: 99.9},
	}
	for i := range samples {
		db.Create(&samples[i])
	}

	body := parseOverview(t, overviewGet(t, db))

	if body.Data.CurrentThroughputMbps != 42.3 {
		t.Errorf("期望吞吐 42.3，实际: %f", body.Data.CurrentThroughputMbps)
	}
}

func TestOverviewThroughputExcludesOldSamples(t *testing.T) {
	db := openOverviewTestDB(t)
	migrateOverviewTables(t, db)

	now := time.Now().UTC()
	runStart := now.Add(-10 * time.Minute)

	task1 := model.Task{Name: "task1", Status: "running", NodeID: 1, LastRunAt: timePtr(runStart)}
	db.Create(&task1)

	samples := []model.TaskTrafficSample{
		// 超过 60 秒的旧采样，不应计入
		{TaskID: task1.ID, NodeID: 1, RunStartedAt: runStart, SampledAt: now.Add(-2 * time.Minute), ThroughputMbps: 200.0},
		// 60 秒内的新采样
		{TaskID: task1.ID, NodeID: 1, RunStartedAt: runStart, SampledAt: now.Add(-5 * time.Second), ThroughputMbps: 15.7},
	}
	for i := range samples {
		db.Create(&samples[i])
	}

	body := parseOverview(t, overviewGet(t, db))

	if body.Data.CurrentThroughputMbps != 15.7 {
		t.Errorf("期望吞吐 15.7，实际: %f", body.Data.CurrentThroughputMbps)
	}
}

func TestOverviewThroughputExcludesPreviousRunSamples(t *testing.T) {
	db := openOverviewTestDB(t)
	migrateOverviewTables(t, db)

	now := time.Now().UTC()
	oldRunStart := now.Add(-5 * time.Minute)
	newRunStart := now.Add(-3 * time.Second)

	// 任务当前是 running（第二轮运行），last_run_at 指向新轮
	task1 := model.Task{Name: "task1", Status: "running", NodeID: 1, LastRunAt: timePtr(newRunStart)}
	db.Create(&task1)

	samples := []model.TaskTrafficSample{
		// 上一轮运行的采样，sampled_at 在 60s 内，但 run_started_at 不等于 last_run_at
		{TaskID: task1.ID, NodeID: 1, RunStartedAt: oldRunStart, SampledAt: now.Add(-20 * time.Second), ThroughputMbps: 200.0},
		// 当前轮运行的采样，run_started_at 等于 last_run_at
		{TaskID: task1.ID, NodeID: 1, RunStartedAt: newRunStart, SampledAt: now.Add(-2 * time.Second), ThroughputMbps: 10.0},
	}
	for i := range samples {
		db.Create(&samples[i])
	}

	body := parseOverview(t, overviewGet(t, db))

	// 应只计当前轮的 10.0，不含上一轮的 200.0
	if body.Data.CurrentThroughputMbps != 10.0 {
		t.Errorf("期望吞吐 10.0（仅当前轮），实际: %f", body.Data.CurrentThroughputMbps)
	}
}

// 核心场景：任务刚重跑，新轮还没产出采样，上一轮残留采样仍在 60s 窗口内
func TestOverviewThroughputZeroWhenNewRunHasNoSamplesYet(t *testing.T) {
	db := openOverviewTestDB(t)
	migrateOverviewTables(t, db)

	now := time.Now().UTC()
	oldRunStart := now.Add(-1 * time.Minute)
	newRunStart := now.Add(-2 * time.Second)

	// 任务刚重启到 running，last_run_at 已更新为新轮
	task1 := model.Task{Name: "task1", Status: "running", NodeID: 1, LastRunAt: timePtr(newRunStart)}
	db.Create(&task1)

	// 上一轮的采样仍在 60s 窗口内（20 秒前），但 run_started_at 是旧轮
	sample := model.TaskTrafficSample{
		TaskID: task1.ID, NodeID: 1, RunStartedAt: oldRunStart,
		SampledAt: now.Add(-20 * time.Second), ThroughputMbps: 200.0,
	}
	db.Create(&sample)

	body := parseOverview(t, overviewGet(t, db))

	// 新轮没有采样，旧轮采样的 run_started_at 不等于 last_run_at，应为 0
	if body.Data.CurrentThroughputMbps != 0 {
		t.Errorf("期望吞吐 0（新轮尚无采样），实际: %f", body.Data.CurrentThroughputMbps)
	}
}

func TestOverviewThroughputZeroWhenLastRunAtIsNil(t *testing.T) {
	db := openOverviewTestDB(t)
	migrateOverviewTables(t, db)

	now := time.Now().UTC()

	// running 任务但 last_run_at 为 nil（不应发生但需防御）
	task1 := model.Task{Name: "task1", Status: "running", NodeID: 1}
	db.Create(&task1)

	sample := model.TaskTrafficSample{
		TaskID: task1.ID, NodeID: 1, RunStartedAt: now.Add(-1 * time.Minute),
		SampledAt: now.Add(-5 * time.Second), ThroughputMbps: 50.0,
	}
	db.Create(&sample)

	body := parseOverview(t, overviewGet(t, db))

	// last_run_at IS NULL → 不参与聚合 → 0
	if body.Data.CurrentThroughputMbps != 0 {
		t.Errorf("期望吞吐 0（last_run_at 为空），实际: %f", body.Data.CurrentThroughputMbps)
	}
}

// 验证修复前的时区不一致场景：SQLite 将不同时区表示存为不同字符串，
// 等值连接会失败。runner 已修复为统一 UTC（runner.go:232），此测试记录该行为限制。
func TestOverviewThroughputZeroWhenTimezoneRepresentationDiffers(t *testing.T) {
	db := openOverviewTestDB(t)
	migrateOverviewTables(t, db)

	now := time.Now().UTC()
	cst := time.FixedZone("CST", 8*3600)
	// 同一时间点，不同时区表示 — SQLite 存为不同字符串
	lastRunLocal := now.In(cst) // e.g. 2026-03-26T13:48:25+08:00
	runStartUTC := now          // e.g. 2026-03-26T05:48:25Z

	task1 := model.Task{Name: "task1", Status: "running", NodeID: 1, LastRunAt: timePtr(lastRunLocal)}
	db.Create(&task1)

	sample := model.TaskTrafficSample{
		TaskID: task1.ID, NodeID: 1, RunStartedAt: runStartUTC,
		SampledAt: now.Add(-5 * time.Second), ThroughputMbps: 75.0,
	}
	db.Create(&sample)

	body := parseOverview(t, overviewGet(t, db))

	// SQLite 文本比较失败 → 吞吐为 0。这正是 runner 必须统一 UTC 的原因。
	if body.Data.CurrentThroughputMbps != 0 {
		t.Errorf("时区不一致时 SQLite 应无法匹配（返回 0），实际: %f", body.Data.CurrentThroughputMbps)
	}
}

// 验证修复后的正确路径：runner 统一 UTC，last_run_at 和 run_started_at 表示一致
func TestOverviewThroughputMatchesWhenBothUTC(t *testing.T) {
	db := openOverviewTestDB(t)
	migrateOverviewTables(t, db)

	now := time.Now().UTC()

	task1 := model.Task{Name: "task1", Status: "running", NodeID: 1, LastRunAt: timePtr(now)}
	db.Create(&task1)

	sample := model.TaskTrafficSample{
		TaskID: task1.ID, NodeID: 1, RunStartedAt: now,
		SampledAt: now.Add(-5 * time.Second), ThroughputMbps: 75.0,
	}
	db.Create(&sample)

	body := parseOverview(t, overviewGet(t, db))

	if body.Data.CurrentThroughputMbps != 75.0 {
		t.Errorf("期望吞吐 75.0（统一 UTC），实际: %f", body.Data.CurrentThroughputMbps)
	}
}

func TestOverviewThroughputRoundsToOneDecimal(t *testing.T) {
	db := openOverviewTestDB(t)
	migrateOverviewTables(t, db)

	now := time.Now().UTC()
	runStart := now.Add(-1 * time.Minute)

	task1 := model.Task{Name: "task1", Status: "running", NodeID: 1, LastRunAt: timePtr(runStart)}
	db.Create(&task1)

	sample := model.TaskTrafficSample{
		TaskID: task1.ID, NodeID: 1, RunStartedAt: runStart,
		SampledAt: now.Add(-5 * time.Second), ThroughputMbps: 33.3567,
	}
	db.Create(&sample)

	body := parseOverview(t, overviewGet(t, db))

	// 33.3567 → 四舍五入到一位小数 → 33.4
	if body.Data.CurrentThroughputMbps != 33.4 {
		t.Errorf("期望吞吐 33.4，实际: %f", body.Data.CurrentThroughputMbps)
	}
}
