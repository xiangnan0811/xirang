package taskstats

import (
	"context"
	"errors"
	"testing"
	"time"

	"xirang/backend/internal/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openTaskStatsTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared&_loc=UTC"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(&model.Task{}, &model.TaskRun{}, &model.TaskTrafficSample{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func seedTaskStatsTask(t *testing.T, db *gorm.DB, id, nodeID uint, name string) {
	t.Helper()
	if err := db.Create(&model.Task{
		ID: id, Name: name, NodeID: nodeID, ExecutorType: "local", Status: "success",
	}).Error; err != nil {
		t.Fatalf("seed task: %v", err)
	}
}

func seedTaskStatsRun(t *testing.T, db *gorm.DB, taskID uint, status string, finishedAt *time.Time, durationMs int64) {
	t.Helper()
	if err := db.Create(&model.TaskRun{
		TaskID: taskID, Status: status, FinishedAt: finishedAt, DurationMs: durationMs,
	}).Error; err != nil {
		t.Fatalf("seed run: %v", err)
	}
}

func seedTaskStatsTraffic(t *testing.T, db *gorm.DB, taskID uint, sampledAt time.Time, mbps float64) {
	t.Helper()
	if err := db.Create(&model.TaskTrafficSample{
		TaskID: taskID, NodeID: 1, RunStartedAt: sampledAt, SampledAt: sampledAt, ThroughputMbps: mbps,
	}).Error; err != nil {
		t.Fatalf("seed traffic: %v", err)
	}
}

func taskStatsRequest(metric, aggregation string, start, end time.Time) QueryRequest {
	return QueryRequest{Metric: metric, Aggregation: aggregation, Start: start, End: end}
}

func TestComputeStepSeconds(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		want     int
	}{
		{name: "five minute boundary", duration: 8 * time.Minute, want: 5},
		{name: "fifteen minute boundary", duration: 25 * time.Minute, want: 15},
		{name: "thirty minute boundary", duration: 50 * time.Minute, want: 30},
		{name: "hour boundary", duration: 90 * time.Minute, want: 60},
		{name: "eight hour boundary", duration: 8 * time.Hour, want: 300},
		{name: "day boundary", duration: 24 * time.Hour, want: 900},
		{name: "long window", duration: 30 * 24 * time.Hour, want: 3600},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ComputeStepSeconds(test.duration); got != test.want {
				t.Fatalf("duration %v: got %d want %d", test.duration, got, test.want)
			}
		})
	}
}

func TestQueryValidationIsTaskOnly(t *testing.T) {
	base := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		req  QueryRequest
		want error
	}{
		{
			name: "unknown metric",
			req:  taskStatsRequest("node.cpu", "avg", base, base.Add(time.Hour)),
			want: ErrInvalidMetric,
		},
		{
			name: "old duration alias",
			req:  taskStatsRequest("task.duration_p95", "p95", base, base.Add(time.Hour)),
			want: ErrInvalidMetric,
		},
		{
			name: "unsupported aggregation",
			req:  taskStatsRequest("task.success_rate", "p95", base, base.Add(time.Hour)),
			want: ErrInvalidAggregation,
		},
		{
			name: "inverted range",
			req:  taskStatsRequest("task.success_rate", "avg", base.Add(time.Hour), base),
			want: ErrInvalidTimeRange,
		},
		{
			name: "thirty days plus one nanosecond",
			req:  taskStatsRequest("task.success_rate", "avg", base, base.Add(MaxQueryDuration+time.Nanosecond)),
			want: ErrInvalidTimeRange,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Query(context.Background(), nil, test.req)
			if !errors.Is(err, test.want) {
				t.Fatalf("got %v want %v", err, test.want)
			}
		})
	}
}

func TestQuerySuccessRatePerTaskAndHalfOpenWindow(t *testing.T) {
	db := openTaskStatsTestDB(t)
	seedTaskStatsTask(t, db, 1, 1, "alpha")
	base := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	for _, offset := range []time.Duration{10 * time.Second, 20 * time.Second, 30 * time.Second} {
		finishedAt := base.Add(offset)
		seedTaskStatsRun(t, db, 1, "success", &finishedAt, 100)
	}
	failedAt := base.Add(40 * time.Second)
	seedTaskStatsRun(t, db, 1, "failed", &failedAt, 100)
	secondSuccessAt := base.Add(70 * time.Second)
	seedTaskStatsRun(t, db, 1, "success", &secondSuccessAt, 100)
	secondFailedAt := base.Add(80 * time.Second)
	seedTaskStatsRun(t, db, 1, "failed", &secondFailedAt, 100)
	atEnd := base.Add(time.Hour)
	seedTaskStatsRun(t, db, 1, "success", &atEnd, 100)

	response, err := Query(context.Background(), db, QueryRequest{
		Metric: "task.success_rate", Filters: Filters{TaskIDs: []uint{1}}, Aggregation: "avg",
		Start: base, End: base.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if response.StepSeconds != 60 || len(response.Series) != 1 || len(response.Series[0].Points) != 2 {
		t.Fatalf("unexpected shape: %+v", response)
	}
	if response.Series[0].Points[0].Value != 0.75 {
		t.Fatalf("bucket 0: got %v want 0.75", response.Series[0].Points[0].Value)
	}
	if response.Series[0].Points[1].Value != 0.5 {
		t.Fatalf("bucket 1: got %v want 0.5", response.Series[0].Points[1].Value)
	}
}

func TestQuerySuccessRateCountsEveryFinishedStatus(t *testing.T) {
	db := openTaskStatsTestDB(t)
	seedTaskStatsTask(t, db, 1, 1, "alpha")
	base := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	for _, status := range []string{"success", "failed", "warning"} {
		finishedAt := base.Add(10 * time.Second)
		seedTaskStatsRun(t, db, 1, status, &finishedAt, 100)
	}
	seedTaskStatsRun(t, db, 1, "pending", nil, 100)

	response, err := Query(context.Background(), db, QueryRequest{
		Metric: "task.success_rate", Filters: Filters{TaskIDs: []uint{1}}, Aggregation: "avg",
		Start: base, End: base.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if got := response.Series[0].Points[0].Value; got != 1.0/3.0 {
		t.Fatalf("finished status denominator: got %v want %v", got, 1.0/3.0)
	}
}

func TestQueryEmptyFilterAggregatesAndOwnershipScope(t *testing.T) {
	db := openTaskStatsTestDB(t)
	seedTaskStatsTask(t, db, 1, 1, "alpha")
	seedTaskStatsTask(t, db, 2, 2, "beta")
	base := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	finishedAt := base.Add(10 * time.Second)
	seedTaskStatsRun(t, db, 1, "success", &finishedAt, 100)
	seedTaskStatsRun(t, db, 2, "failed", &finishedAt, 100)

	all, err := Query(context.Background(), db, taskStatsRequest("task.success_rate", "avg", base, base.Add(time.Hour)))
	if err != nil {
		t.Fatalf("all query: %v", err)
	}
	if got := all.Series[0].Points[0].Value; got != 0.5 {
		t.Fatalf("all-task rate: got %v want 0.5", got)
	}

	scopedRequest := taskStatsRequest("task.success_rate", "avg", base, base.Add(time.Hour))
	scopedRequest.RestrictToNodeIDs([]uint{1})
	scoped, err := Query(context.Background(), db, scopedRequest)
	if err != nil {
		t.Fatalf("scoped query: %v", err)
	}
	if got := scoped.Series[0].Points[0].Value; got != 1 {
		t.Fatalf("owned-task rate: got %v want 1", got)
	}
}

func TestQueryEmptyDataReturnsNoSeries(t *testing.T) {
	db := openTaskStatsTestDB(t)
	seedTaskStatsTask(t, db, 1, 1, "alpha")
	base := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)

	response, err := Query(context.Background(), db, QueryRequest{
		Metric: "task.throughput", Filters: Filters{TaskIDs: []uint{1}}, Aggregation: "avg",
		Start: base, End: base.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(response.Series) != 0 {
		t.Fatalf("empty query should return no series, got %+v", response.Series)
	}
	if response.Truncated {
		t.Fatal("empty query must not be marked truncated")
	}
}

func TestQueryTruncatesAtMaxRowsPerQuery(t *testing.T) {
	db := openTaskStatsTestDB(t)
	seedTaskStatsTask(t, db, 1, 1, "alpha")
	base := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)

	const insertRows = MaxRowsPerQuery + 1
	if err := db.Exec(`
		WITH RECURSIVE seq(n) AS (
			SELECT 1
			UNION ALL
			SELECT n + 1 FROM seq WHERE n < ?
		)
		INSERT INTO task_traffic_samples
			(task_id, node_id, run_started_at, sampled_at, throughput_mbps, created_at)
		SELECT 1, 1, ?, ?, 1.0, ? FROM seq
	`, insertRows, base, base, base).Error; err != nil {
		t.Fatalf("seed %d traffic samples: %v", insertRows, err)
	}

	response, err := Query(context.Background(), db, QueryRequest{
		Metric: "task.throughput", Filters: Filters{TaskIDs: []uint{1}}, Aggregation: "sum",
		Start: base, End: base.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !response.Truncated {
		t.Fatal("query reaching the row cap must be marked truncated")
	}
	if len(response.Series) != 1 || len(response.Series[0].Points) != 1 {
		t.Fatalf("unexpected truncated response shape: %+v", response)
	}
	if got := response.Series[0].Points[0].Value; got != float64(MaxRowsPerQuery) {
		t.Fatalf("query should materialize exactly the cap: got %v want %d", got, MaxRowsPerQuery)
	}
}

func TestQueryThroughputSumAndAverage(t *testing.T) {
	db := openTaskStatsTestDB(t)
	seedTaskStatsTask(t, db, 1, 1, "alpha")
	base := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	for _, mbps := range []float64{10, 20, 30} {
		seedTaskStatsTraffic(t, db, 1, base.Add(10*time.Second), mbps)
	}
	for _, aggregation := range []struct {
		name string
		want float64
	}{
		{name: "sum", want: 60},
		{name: "avg", want: 20},
	} {
		response, err := Query(context.Background(), db, QueryRequest{
			Metric: "task.throughput", Filters: Filters{TaskIDs: []uint{1}}, Aggregation: aggregation.name,
			Start: base, End: base.Add(time.Hour),
		})
		if err != nil {
			t.Fatalf("%s query: %v", aggregation.name, err)
		}
		if got := response.Series[0].Points[0].Value; got != aggregation.want {
			t.Fatalf("%s: got %v want %v", aggregation.name, got, aggregation.want)
		}
	}
}

func TestQueryDurationPercentiles(t *testing.T) {
	db := openTaskStatsTestDB(t)
	seedTaskStatsTask(t, db, 1, 1, "alpha")
	base := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	finishedAt := base.Add(10 * time.Second)
	for duration := int64(1); duration <= 100; duration++ {
		seedTaskStatsRun(t, db, 1, "success", &finishedAt, duration)
	}
	for _, aggregation := range []struct {
		name string
		want float64
	}{
		{name: "p50", want: 50},
		{name: "p95", want: 95},
		{name: "p99", want: 99},
	} {
		response, err := Query(context.Background(), db, QueryRequest{
			Metric: "task.duration", Filters: Filters{TaskIDs: []uint{1}}, Aggregation: aggregation.name,
			Start: base, End: base.Add(time.Hour),
		})
		if err != nil {
			t.Fatalf("%s query: %v", aggregation.name, err)
		}
		if got := response.Series[0].Points[0].Value; got != aggregation.want {
			t.Fatalf("%s: got %v want %v", aggregation.name, got, aggregation.want)
		}
	}
}

func TestQuerySkipsNonPositiveDurationAndDoesNotZeroFill(t *testing.T) {
	db := openTaskStatsTestDB(t)
	seedTaskStatsTask(t, db, 1, 1, "alpha")
	base := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	first := base.Add(10 * time.Second)
	seedTaskStatsRun(t, db, 1, "success", &first, 100)
	zero := base.Add(20 * time.Second)
	seedTaskStatsRun(t, db, 1, "success", &zero, 0)
	negative := base.Add(30 * time.Second)
	seedTaskStatsRun(t, db, 1, "success", &negative, -1)
	second := base.Add(130 * time.Second)
	seedTaskStatsRun(t, db, 1, "success", &second, 200)

	response, err := Query(context.Background(), db, QueryRequest{
		Metric: "task.duration", Filters: Filters{TaskIDs: []uint{1}}, Aggregation: "p95",
		Start: base, End: base.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(response.Series) != 1 || len(response.Series[0].Points) != 2 {
		t.Fatalf("expected two non-empty buckets, got %+v", response.Series)
	}
	if response.Series[0].Points[0].Value != 100 || response.Series[0].Points[1].Value != 200 {
		t.Fatalf("unexpected duration values: %+v", response.Series[0].Points)
	}
}
