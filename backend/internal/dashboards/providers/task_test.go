package providers

import (
	"context"
	"testing"
	"time"

	"xirang/backend/internal/dashboards"
	"xirang/backend/internal/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openTaskTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(&model.Task{}, &model.TaskRun{}, &model.TaskTrafficSample{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func seedTask(db *gorm.DB, id uint, name string) {
	db.Exec("INSERT INTO tasks (id, name, node_id) VALUES (?, ?, 1)", id, name)
}

func seedRun(db *gorm.DB, taskID uint, status string, finishedAt time.Time, durationMs int64) {
	fa := finishedAt
	r := model.TaskRun{TaskID: taskID, Status: status, FinishedAt: &fa, DurationMs: durationMs}
	db.Create(&r)
}

func seedTraffic(db *gorm.DB, taskID uint, ts time.Time, mbps float64) {
	db.Create(&model.TaskTrafficSample{
		TaskID: taskID, NodeID: 1, RunStartedAt: ts, SampledAt: ts, ThroughputMbps: mbps,
	})
}

func TestTaskProvider_Supports(t *testing.T) {
	p := NewTaskProvider(nil)
	for _, m := range []string{"task.success_rate", "task.throughput", "task.duration_p95"} {
		if !p.Supports(m) {
			t.Fatalf("should support %s", m)
		}
	}
	if p.Supports("node.cpu") {
		t.Fatal("should not support node metric")
	}
}

func TestTaskProvider_SuccessRate_PerTask(t *testing.T) {
	db := openTaskTestDB(t)
	seedTask(db, 1, "alpha")
	base := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	// Bucket 0 (60s): 3 success + 1 failed = 0.75
	seedRun(db, 1, "success", base.Add(10*time.Second), 100)
	seedRun(db, 1, "success", base.Add(20*time.Second), 100)
	seedRun(db, 1, "success", base.Add(30*time.Second), 100)
	seedRun(db, 1, "failed", base.Add(40*time.Second), 100)
	// Bucket 1: 1 success + 1 failed = 0.5
	seedRun(db, 1, "success", base.Add(70*time.Second), 100)
	seedRun(db, 1, "failed", base.Add(80*time.Second), 100)

	p := NewTaskProvider(db)
	resp, err := p.Query(context.Background(), dashboards.QueryRequest{
		Metric: "task.success_rate", Filters: dashboards.Filters{TaskIDs: []uint{1}},
		Aggregation: "avg", Start: base, End: base.Add(2 * time.Minute),
	}, 60)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(resp.Series) != 1 || len(resp.Series[0].Points) != 2 {
		t.Fatalf("unexpected shape: %+v", resp.Series)
	}
	if resp.Series[0].Points[0].Value != 0.75 {
		t.Fatalf("bucket 0: got %v want 0.75", resp.Series[0].Points[0].Value)
	}
	if resp.Series[0].Points[1].Value != 0.5 {
		t.Fatalf("bucket 1: got %v want 0.5", resp.Series[0].Points[1].Value)
	}
}

func TestTaskProvider_SuccessRate_EmptyFilterAggregates(t *testing.T) {
	db := openTaskTestDB(t)
	seedTask(db, 1, "alpha")
	seedTask(db, 2, "beta")
	base := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	seedRun(db, 1, "success", base.Add(10*time.Second), 100)
	seedRun(db, 2, "failed", base.Add(20*time.Second), 100)

	p := NewTaskProvider(db)
	resp, _ := p.Query(context.Background(), dashboards.QueryRequest{
		Metric: "task.success_rate", Aggregation: "avg",
		Start: base, End: base.Add(time.Minute),
	}, 60)
	if len(resp.Series) != 1 || resp.Series[0].Name != "全部任务" {
		t.Fatalf("expected single '全部任务' series, got %+v", resp.Series)
	}
	if resp.Series[0].Points[0].Value != 0.5 {
		t.Fatalf("aggregated rate: got %v want 0.5", resp.Series[0].Points[0].Value)
	}
}

func TestTaskProvider_Throughput_Sum(t *testing.T) {
	db := openTaskTestDB(t)
	seedTask(db, 1, "alpha")
	base := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	seedTraffic(db, 1, base.Add(10*time.Second), 10)
	seedTraffic(db, 1, base.Add(20*time.Second), 20)
	seedTraffic(db, 1, base.Add(30*time.Second), 30)

	p := NewTaskProvider(db)
	resp, _ := p.Query(context.Background(), dashboards.QueryRequest{
		Metric: "task.throughput", Filters: dashboards.Filters{TaskIDs: []uint{1}},
		Aggregation: "sum", Start: base, End: base.Add(time.Minute),
	}, 60)
	if resp.Series[0].Points[0].Value != 60 {
		t.Fatalf("sum: got %v want 60", resp.Series[0].Points[0].Value)
	}
}

func TestTaskProvider_DurationP95(t *testing.T) {
	db := openTaskTestDB(t)
	seedTask(db, 1, "alpha")
	base := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	// 10 runs with durations 100, 200, ..., 1000 ms. p95 nearest-rank = 1000.
	for i := 1; i <= 10; i++ {
		seedRun(db, 1, "success", base.Add(time.Duration(i*5)*time.Second), int64(i*100))
	}
	p := NewTaskProvider(db)
	resp, _ := p.Query(context.Background(), dashboards.QueryRequest{
		Metric: "task.duration_p95", Filters: dashboards.Filters{TaskIDs: []uint{1}},
		Aggregation: "p95", Start: base, End: base.Add(time.Minute),
	}, 60)
	if resp.Series[0].Points[0].Value != 1000 {
		t.Fatalf("p95: got %v want 1000", resp.Series[0].Points[0].Value)
	}
}

func TestTaskProvider_SkipsNilFinishedAt(t *testing.T) {
	db := openTaskTestDB(t)
	seedTask(db, 1, "alpha")
	// Run without finished_at (still pending) — should be ignored
	db.Create(&model.TaskRun{TaskID: 1, Status: "pending"})
	base := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	seedRun(db, 1, "success", base.Add(10*time.Second), 100)

	p := NewTaskProvider(db)
	resp, _ := p.Query(context.Background(), dashboards.QueryRequest{
		Metric: "task.success_rate", Filters: dashboards.Filters{TaskIDs: []uint{1}},
		Aggregation: "avg", Start: base, End: base.Add(time.Minute),
	}, 60)
	if resp.Series[0].Points[0].Value != 1.0 {
		t.Fatalf("pending run should be ignored: got %v want 1.0", resp.Series[0].Points[0].Value)
	}
}
