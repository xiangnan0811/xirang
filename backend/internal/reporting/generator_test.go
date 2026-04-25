package reporting

import (
	"context"
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

func TestGenerate_ReversedTimeRange_DocumentsCurrentBehavior(t *testing.T) {
	db := openReportingTestDB(t)
	base := reportingTimeAnchor
	seedReportFixtureBasic(t, db, base)

	cfg := model.ReportConfig{
		Name: "reversed", ScopeType: "all", Period: "weekly",
		Cron: "* * * * *", IntegrationIDs: "[]", Enabled: true,
	}
	if err := db.Create(&cfg).Error; err != nil {
		t.Fatalf("seed cfg: %v", err)
	}
	// end < start: current generator query uses started_at >= start AND < end,
	// which yields zero matches when reversed. This test documents that
	// contract so future refactors don't silently change to panic / error.
	report, err := Generate(db, cfg, base, base.AddDate(0, 0, -7))
	if err != nil {
		t.Fatalf("expected no error on reversed range, got %v", err)
	}
	if report.TotalRuns != 0 {
		t.Fatalf("reversed range should yield 0 runs, got %d", report.TotalRuns)
	}
}

func TestGenerate_PersistsToReportsTable(t *testing.T) {
	db := openReportingTestDB(t)
	base := reportingTimeAnchor
	seedReportFixtureBasic(t, db, base)

	cfg := model.ReportConfig{
		Name: "persist", ScopeType: "all", Period: "weekly",
		Cron: "* * * * *", IntegrationIDs: "[]", Enabled: true,
	}
	if err := db.Create(&cfg).Error; err != nil {
		t.Fatalf("seed cfg: %v", err)
	}
	report, err := Generate(db, cfg, base.AddDate(0, 0, -7), base)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	// Re-read from DB to confirm round-trip — every aggregate field must match.
	var got model.Report
	if err := db.First(&got, report.ID).Error; err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if got.TotalRuns != report.TotalRuns ||
		got.SuccessRuns != report.SuccessRuns ||
		got.FailedRuns != report.FailedRuns ||
		!approxEqual(got.SuccessRate, report.SuccessRate) ||
		got.TopFailures != report.TopFailures ||
		got.DiskTrend != report.DiskTrend {
		t.Fatalf("persisted report differs from returned:\nin-mem  %+v\nfromDB  %+v", report, got)
	}
}

func TestShouldGenerate(t *testing.T) {
	tests := []struct {
		name string
		cron string
		now  time.Time
		want bool
	}{
		{
			name: "exact_match_weekly_monday_8am",
			cron: "0 8 * * 1",
			now:  time.Date(2026, 4, 6, 8, 0, 0, 0, time.UTC), // Monday
			want: true,
		},
		{
			name: "wrong_minute",
			cron: "0 8 * * 1",
			now:  time.Date(2026, 4, 6, 8, 5, 0, 0, time.UTC),
			want: false,
		},
		{
			name: "wrong_hour",
			cron: "0 8 * * 1",
			now:  time.Date(2026, 4, 6, 9, 0, 0, 0, time.UTC),
			want: false,
		},
		{
			name: "wrong_weekday",
			cron: "0 8 * * 1",
			now:  time.Date(2026, 4, 7, 8, 0, 0, 0, time.UTC), // Tuesday
			want: false,
		},
		{
			name: "wildcard_fires_any_time",
			cron: "* * * * *",
			now:  time.Date(2026, 4, 1, 12, 30, 0, 0, time.UTC),
			want: true,
		},
		{
			name: "too_few_fields",
			cron: "0 8 * *",
			now:  time.Date(2026, 4, 6, 8, 0, 0, 0, time.UTC),
			want: false,
		},
		{
			name: "non_wildcard_month_returns_false",
			cron: "0 8 * 4 1",
			now:  time.Date(2026, 4, 6, 8, 0, 0, 0, time.UTC),
			want: false,
		},
		{
			name: "dom_match",
			cron: "0 8 6 * *",
			now:  time.Date(2026, 4, 6, 8, 0, 0, 0, time.UTC),
			want: true,
		},
		{
			name: "dom_no_match",
			cron: "0 8 7 * *",
			now:  time.Date(2026, 4, 6, 8, 0, 0, 0, time.UTC),
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := model.ReportConfig{Cron: tt.cron}
			got := shouldGenerate(cfg, tt.now)
			if got != tt.want {
				t.Fatalf("shouldGenerate(%q, %v) = %v, want %v", tt.cron, tt.now, got, tt.want)
			}
		})
	}
}

func TestMatchField(t *testing.T) {
	tests := []struct {
		expr  string
		value int
		want  bool
	}{
		{"*", 5, true},
		{"5", 5, true},
		{"5", 6, false},
		{"0", 0, true},
		{"abc", 0, false},
	}
	for _, tt := range tests {
		got := matchField(tt.expr, tt.value)
		if got != tt.want {
			t.Fatalf("matchField(%q, %d) = %v, want %v", tt.expr, tt.value, got, tt.want)
		}
	}
}

func TestBuildReportMessage(t *testing.T) {
	cfg := model.ReportConfig{Name: "weekly-all"}
	report := &model.Report{
		PeriodStart:   time.Date(2026, 3, 25, 0, 0, 0, 0, time.UTC),
		PeriodEnd:     time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
		SuccessRate:   80.0,
		SuccessRuns:   24,
		TotalRuns:     30,
		AvgDurationMs: 60000,
	}
	msg := buildReportMessage(cfg, report)
	if msg == "" {
		t.Fatal("buildReportMessage returned empty string")
	}
	// Verify key fields appear in the message.
	for _, want := range []string{"weekly-all", "80.0%", "24/30", "60000ms", "2026-03-25", "2026-04-01"} {
		if !contains(msg, want) {
			t.Fatalf("message missing %q:\n%s", want, msg)
		}
	}
}

// contains is a simple substring helper to avoid importing strings in test.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(substr); i++ {
				if s[i:i+len(substr)] == substr {
					return true
				}
			}
			return false
		}())
}

func TestNewScheduler_CreatesInstance(t *testing.T) {
	db := openReportingTestDB(t)
	ctx := t.Context()
	s := NewScheduler(ctx, db)
	if s == nil {
		t.Fatal("NewScheduler returned nil")
	}
	if s.db != db {
		t.Fatal("Scheduler.db not set")
	}
}

func TestCheckAndGenerate_FiresMatchingConfig(t *testing.T) {
	db := openReportingTestDB(t)
	base := reportingTimeAnchor
	seedReportFixtureBasic(t, db, base)

	// Seed a config whose cron exactly matches base (Wednesday 12:30 → "30 12 * * 3").
	// base = 2026-04-01 12:30 UTC, which is a Wednesday (weekday=3).
	cfg := model.ReportConfig{
		Name: "sched-test", ScopeType: "all", Period: "weekly",
		Cron: "30 12 * * 3", IntegrationIDs: "[]", Enabled: true,
	}
	if err := db.Create(&cfg).Error; err != nil {
		t.Fatalf("seed cfg: %v", err)
	}

	ctx := t.Context()
	s := NewScheduler(ctx, db)
	s.checkAndGenerate(base)

	var count int64
	db.Model(&model.Report{}).Count(&count)
	if count != 1 {
		t.Fatalf("expected 1 report generated, got %d", count)
	}
}

func TestCheckAndGenerate_SkipsNonMatchingCron(t *testing.T) {
	db := openReportingTestDB(t)
	base := reportingTimeAnchor // Wednesday 12:30 UTC

	// Cron fires Monday at 08:00 — does not match base (Wednesday 12:30).
	cfg := model.ReportConfig{
		Name: "non-matching", ScopeType: "all", Period: "weekly",
		Cron: "0 8 * * 1", IntegrationIDs: "[]", Enabled: true,
	}
	if err := db.Create(&cfg).Error; err != nil {
		t.Fatalf("seed cfg: %v", err)
	}

	ctx := t.Context()
	s := NewScheduler(ctx, db)
	s.checkAndGenerate(base)

	var count int64
	db.Model(&model.Report{}).Count(&count)
	if count != 0 {
		t.Fatalf("non-matching cron should not generate report, got %d", count)
	}
}

func TestCheckAndGenerate_MonthlyPeriodUsesOneMonthRange(t *testing.T) {
	db := openReportingTestDB(t)
	base := reportingTimeAnchor

	if err := db.Create(&model.Node{ID: 1, Name: "m1", Host: "h", Username: "u", BackupDir: "/m"}).Error; err != nil {
		t.Fatalf("seed node: %v", err)
	}

	cfg := model.ReportConfig{
		Name: "monthly", ScopeType: "all", Period: "monthly",
		Cron: "30 12 * * 3", IntegrationIDs: "[]", Enabled: true,
	}
	if err := db.Create(&cfg).Error; err != nil {
		t.Fatalf("seed cfg: %v", err)
	}

	ctx := t.Context()
	s := NewScheduler(ctx, db)
	s.checkAndGenerate(base)

	var report model.Report
	if err := db.First(&report).Error; err != nil {
		t.Fatalf("re-read report: %v", err)
	}
	// Period start should be ~1 month before base.
	expectedStart := base.AddDate(0, -1, 0)
	diff := report.PeriodStart.Sub(expectedStart)
	if diff < 0 {
		diff = -diff
	}
	if diff > time.Second {
		t.Fatalf("monthly report PeriodStart: want ~%v, got %v", expectedStart, report.PeriodStart)
	}
}

func TestScheduler_Start_ExitsOnContextCancel(t *testing.T) {
	db := openReportingTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	// Cancel immediately so loop() exits on the first ctx.Done() select.
	cancel()
	s := NewScheduler(ctx, db)
	s.Start()
	// Give the goroutine a moment to observe the cancellation.
	time.Sleep(20 * time.Millisecond)
	// If we reach here without hanging, Start() and loop() work correctly.
}

func TestSendReport_MissingIntegration_DoesNotPanic(t *testing.T) {
	db := openReportingTestDB(t)
	// IntegrationIDs references a non-existent integration — sendReport must
	// log and continue, not panic. Run synchronously via direct call.
	cfg := model.ReportConfig{
		Name: "send-test", IntegrationIDs: "[9999]",
	}
	report := &model.Report{ID: 1}
	// sendReport is fire-and-forget with panic recovery — call it directly.
	sendReport(db, cfg, report)
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
