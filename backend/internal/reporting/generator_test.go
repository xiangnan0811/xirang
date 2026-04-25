package reporting

import (
	"encoding/json"
	"fmt"
	"math"
	"testing"
	"time"

	"xirang/backend/internal/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// reportingTimeAnchor is the fixed reference "now" for every time-sensitive
// test in this package. Pinning to a literal date keeps the suite stable
// regardless of when CI fires (cf. metrics aggregator flake fix).
var reportingTimeAnchor = time.Date(2026, 4, 1, 12, 30, 0, 0, time.UTC)

func openReportingTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(
		&model.Node{},
		&model.Task{},
		&model.TaskRun{},
		&model.NodeMetricSample{},
		&model.Alert{},
		&model.ReportConfig{},
		&model.Report{},
		&model.Integration{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// approxEqual compares floats with the project's standard 1e-6 epsilon.
func approxEqual(got, want float64) bool {
	return math.Abs(got-want) < 1e-6
}

// seedReportFixtureBasic writes a stable multi-node, multi-task, mixed-status
// dataset anchored to base. Used by happy-path / scope / aggregation tests.
//   - 2 nodes (node "n1" tagged "prod", node "n2" tagged "dev")
//   - 5 tasks (3 on n1, 2 on n2)
//   - 30 TaskRuns spanning [base-7d, base-1d): 24 success, 6 failed
//   - 10 NodeMetricSamples (5 per node) for disk trend
//   - 3 Alerts (2 critical, 1 warning) for alertCount sanity
func seedReportFixtureBasic(t *testing.T, db *gorm.DB, base time.Time) {
	t.Helper()
	if err := db.Create(&model.Node{ID: 1, Name: "n1", Host: "h1", Username: "u", BackupDir: "/b1", Tags: "prod"}).Error; err != nil {
		t.Fatalf("seed node 1: %v", err)
	}
	if err := db.Create(&model.Node{ID: 2, Name: "n2", Host: "h2", Username: "u", BackupDir: "/b2", Tags: "dev"}).Error; err != nil {
		t.Fatalf("seed node 2: %v", err)
	}
	for i := 1; i <= 5; i++ {
		nodeID := uint(1)
		if i > 3 {
			nodeID = 2
		}
		if err := db.Create(&model.Task{ID: uint(i), Name: fmt.Sprintf("task-%d", i), NodeID: nodeID, Command: "echo " + fmt.Sprint(i)}).Error; err != nil {
			t.Fatalf("seed task %d: %v", i, err)
		}
	}
	// 30 TaskRuns: tasks 1-5, 6 runs each. First 24 success, last 6 failed.
	runIdx := 0
	for taskID := uint(1); taskID <= 5; taskID++ {
		for k := 0; k < 6; k++ {
			runIdx++
			status := "success"
			lastErr := ""
			if runIdx > 24 {
				status = "failed"
				lastErr = fmt.Sprintf("err-%d", runIdx)
			}
			started := base.AddDate(0, 0, -(runIdx%7 + 1))
			finished := started.Add(time.Minute)
			run := &model.TaskRun{
				TaskID: taskID, Status: status, LastError: lastErr,
				StartedAt: &started, FinishedAt: &finished, DurationMs: 60000,
			}
			if err := db.Create(run).Error; err != nil {
				t.Fatalf("seed run: %v", err)
			}
		}
	}
	// 10 disk samples
	for i := 0; i < 10; i++ {
		nodeID := uint(1)
		if i >= 5 {
			nodeID = 2
		}
		ts := base.AddDate(0, 0, -((i % 5) + 1))
		if err := db.Create(&model.NodeMetricSample{
			NodeID: nodeID, SampledAt: ts, DiskPct: 40 + float64(i),
			ProbeOK: true,
		}).Error; err != nil {
			t.Fatalf("seed metric: %v", err)
		}
	}
	// 3 alerts
	for i, sev := range []string{"critical", "critical", "warning"} {
		ts := base.AddDate(0, 0, -i-1)
		if err := db.Create(&model.Alert{
			NodeID: 1, NodeName: "n1", Severity: sev, Status: "open",
			ErrorCode: fmt.Sprintf("XR-X-%d", i), Message: "test", TriggeredAt: ts,
		}).Error; err != nil {
			t.Fatalf("seed alert: %v", err)
		}
	}
}

func TestGenerate_HappyPath_AllScope(t *testing.T) {
	db := openReportingTestDB(t)
	base := reportingTimeAnchor
	seedReportFixtureBasic(t, db, base)

	cfg := model.ReportConfig{
		Name: "weekly-all", ScopeType: "all", Period: "weekly",
		Cron: "0 8 * * 1", IntegrationIDs: "[]", Enabled: true,
	}
	if err := db.Create(&cfg).Error; err != nil {
		t.Fatalf("seed cfg: %v", err)
	}

	start := base.AddDate(0, 0, -7)
	end := base
	report, err := Generate(db, cfg, start, end)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if report.TotalRuns != 30 {
		t.Fatalf("TotalRuns: want 30, got %d", report.TotalRuns)
	}
	if report.SuccessRuns != 24 || report.FailedRuns != 6 {
		t.Fatalf("Success/Failed: want 24/6, got %d/%d", report.SuccessRuns, report.FailedRuns)
	}
	if !approxEqual(report.SuccessRate, 80.0) {
		t.Fatalf("SuccessRate: want 80.0, got %f", report.SuccessRate)
	}
	if report.AvgDurationMs != 60000 {
		t.Fatalf("AvgDurationMs: want 60000, got %d", report.AvgDurationMs)
	}
	if report.ConfigID != cfg.ID {
		t.Fatalf("ConfigID: want %d, got %d", cfg.ID, report.ConfigID)
	}
}

func TestGenerate_ScopeByTag(t *testing.T) {
	db := openReportingTestDB(t)
	base := reportingTimeAnchor
	seedReportFixtureBasic(t, db, base)

	cfg := model.ReportConfig{
		Name: "weekly-prod", ScopeType: "tag", ScopeValue: "prod",
		Period: "weekly", Cron: "0 8 * * 1", IntegrationIDs: "[]", Enabled: true,
	}
	if err := db.Create(&cfg).Error; err != nil {
		t.Fatalf("seed cfg: %v", err)
	}
	report, err := Generate(db, cfg, base.AddDate(0, 0, -7), base)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	// Only n1 (prod-tagged) tasks 1-3 → 18 runs (3 tasks × 6 runs).
	if report.TotalRuns != 18 {
		t.Fatalf("scope=tag should restrict to prod nodes; want 18 runs, got %d", report.TotalRuns)
	}
}

func TestGenerate_ScopeByNodeIDs(t *testing.T) {
	db := openReportingTestDB(t)
	base := reportingTimeAnchor
	seedReportFixtureBasic(t, db, base)

	cfg := model.ReportConfig{
		Name: "weekly-n2", ScopeType: "node_ids", ScopeValue: "[2]",
		Period: "weekly", Cron: "0 8 * * 1", IntegrationIDs: "[]", Enabled: true,
	}
	if err := db.Create(&cfg).Error; err != nil {
		t.Fatalf("seed cfg: %v", err)
	}
	report, err := Generate(db, cfg, base.AddDate(0, 0, -7), base)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	// Only node 2 → tasks 4 and 5 → 12 runs.
	if report.TotalRuns != 12 {
		t.Fatalf("scope=node_ids should restrict to listed nodes; want 12, got %d", report.TotalRuns)
	}
}

func TestGenerate_ScopeUnknown_ReturnsWrappedError(t *testing.T) {
	db := openReportingTestDB(t)
	base := reportingTimeAnchor

	cfg := model.ReportConfig{
		Name: "broken", ScopeType: "garbage", Period: "weekly",
		Cron: "* * * * *", IntegrationIDs: "[]", Enabled: true,
	}
	if err := db.Create(&cfg).Error; err != nil {
		t.Fatalf("seed cfg: %v", err)
	}
	report, err := Generate(db, cfg, base.AddDate(0, 0, -7), base)
	if err == nil {
		t.Fatal("expected error from unknown scope, got nil")
	}
	if report != nil {
		t.Fatalf("expected nil report on error, got %+v", report)
	}
	// Confirm the persisted reports table is empty — production code must
	// not write a row when scope resolution fails.
	var count int64
	db.Model(&model.Report{}).Count(&count)
	if count != 0 {
		t.Fatalf("reports table should be empty on scope failure, got %d rows", count)
	}
}

func TestGenerate_FailureTopN_TruncatesAndSorts(t *testing.T) {
	db := openReportingTestDB(t)
	base := reportingTimeAnchor
	// buildTopFailures hardcodes LIMIT 5 — seed 8 distinct failure groups so
	// truncation is exercised, with descending counts 1..8.
	seedReportFixtureFailureTopN(t, db, base, 5)

	cfg := model.ReportConfig{
		Name: "topn", ScopeType: "all", Period: "weekly",
		Cron: "* * * * *", IntegrationIDs: "[]", Enabled: true,
	}
	if err := db.Create(&cfg).Error; err != nil {
		t.Fatalf("seed cfg: %v", err)
	}
	report, err := Generate(db, cfg, base.AddDate(0, 0, -2), base)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	var failures []FailureEntry
	if err := json.Unmarshal([]byte(report.TopFailures), &failures); err != nil {
		t.Fatalf("unmarshal TopFailures: %v", err)
	}
	if len(failures) != 5 {
		t.Fatalf("Top N should truncate to 5, got %d entries", len(failures))
	}
	// Descending order: first entry has the highest Count.
	for i := 1; i < len(failures); i++ {
		if failures[i].Count > failures[i-1].Count {
			t.Fatalf("entry %d (count=%d) > entry %d (count=%d) — not sorted desc",
				i, failures[i].Count, i-1, failures[i-1].Count)
		}
	}
	// Highest two counts among 1..10 are 10 and 9 (10 nodes seeded: i+1 fails).
	if failures[0].Count != 10 {
		t.Fatalf("top entry Count: want 10, got %d", failures[0].Count)
	}
}

func TestGenerate_DiskTrend_DailyAggregation(t *testing.T) {
	db := openReportingTestDB(t)
	base := reportingTimeAnchor
	seedReportFixtureBasic(t, db, base)

	cfg := model.ReportConfig{
		Name: "trend", ScopeType: "all", Period: "weekly",
		Cron: "* * * * *", IntegrationIDs: "[]", Enabled: true,
	}
	if err := db.Create(&cfg).Error; err != nil {
		t.Fatalf("seed cfg: %v", err)
	}
	report, err := Generate(db, cfg, base.AddDate(0, 0, -7), base)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	var trend []DiskTrendEntry
	if err := json.Unmarshal([]byte(report.DiskTrend), &trend); err != nil {
		t.Fatalf("unmarshal DiskTrend: %v", err)
	}
	if len(trend) == 0 {
		t.Fatal("DiskTrend empty; expected ≥1 daily entry")
	}
	// Dates must be strictly ascending (production query uses ORDER BY date_label asc).
	for i := 1; i < len(trend); i++ {
		if trend[i].Date <= trend[i-1].Date {
			t.Fatalf("date order violated: %q <= %q at index %d", trend[i].Date, trend[i-1].Date, i)
		}
	}
	// AvgFree = 100 - disk_pct, so for the seeded values 40..49 expect entries in [51, 60].
	for _, e := range trend {
		if e.AvgFree < 50 || e.AvgFree > 61 {
			t.Fatalf("AvgFree out of expected band [50,61]: got %f for %s", e.AvgFree, e.Date)
		}
	}
}

func TestGenerate_EmptyData_NoPanic(t *testing.T) {
	db := openReportingTestDB(t)
	base := reportingTimeAnchor

	// One node so scope=all resolves non-empty, but ZERO TaskRuns / samples / alerts.
	if err := db.Create(&model.Node{ID: 1, Name: "lonely", Host: "h", Username: "u", BackupDir: "/x"}).Error; err != nil {
		t.Fatalf("seed node: %v", err)
	}
	cfg := model.ReportConfig{
		Name: "empty", ScopeType: "all", Period: "weekly",
		Cron: "* * * * *", IntegrationIDs: "[]", Enabled: true,
	}
	if err := db.Create(&cfg).Error; err != nil {
		t.Fatalf("seed cfg: %v", err)
	}
	report, err := Generate(db, cfg, base.AddDate(0, 0, -7), base)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if report.TotalRuns != 0 || report.SuccessRate != 0 || report.AvgDurationMs != 0 {
		t.Fatalf("empty-data report should be all zeros, got %+v", report)
	}
	if report.TopFailures == "" || report.DiskTrend == "" {
		t.Fatalf("JSON fields must round-trip as 'null' or '[]', got TopFailures=%q DiskTrend=%q",
			report.TopFailures, report.DiskTrend)
	}
}

// seedReportFixtureFailureTopN seeds n+5 failed TaskRuns distributed across
// (n+5) distinct (task,node) groups so the Top N truncation has something to
// truncate. Other tests do not use this — it is dedicated to test #5.
func seedReportFixtureFailureTopN(t *testing.T, db *gorm.DB, base time.Time, n int) {
	t.Helper()
	for i := 0; i < n+5; i++ {
		nodeID := uint(i + 100)
		if err := db.Create(&model.Node{
			ID: nodeID, Name: fmt.Sprintf("topn-node-%d", i),
			Host: "h", Username: "u", BackupDir: fmt.Sprintf("/topn-%d", i), Tags: "prod",
		}).Error; err != nil {
			t.Fatalf("seed topn node: %v", err)
		}
		taskID := uint(i + 100)
		if err := db.Create(&model.Task{
			ID: taskID, Name: fmt.Sprintf("topn-task-%d", i), NodeID: nodeID,
		}).Error; err != nil {
			t.Fatalf("seed topn task: %v", err)
		}
		// failure count = i+1 so node 0 has 1 failure, node n+4 has n+5 failures.
		// Sorting desc: largest count first; top N keeps highest n indices.
		for k := 0; k <= i; k++ {
			started := base.AddDate(0, 0, -1).Add(time.Duration(k) * time.Minute)
			finished := started.Add(10 * time.Second)
			if err := db.Create(&model.TaskRun{
				TaskID: taskID, Status: "failed", LastError: "boom",
				StartedAt: &started, FinishedAt: &finished, DurationMs: 10000,
			}).Error; err != nil {
				t.Fatalf("seed topn run: %v", err)
			}
		}
	}
}
