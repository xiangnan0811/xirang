package dashboards

import (
	"context"
	"testing"
	"time"
)

func TestComputeStepSeconds(t *testing.T) {
	tests := []struct {
		duration time.Duration
		want     int
	}{
		{5 * time.Minute, 5},
		{10 * time.Minute, 15},
		{30 * time.Minute, 30},
		{time.Hour, 60},
		{6 * time.Hour, 300},
		{12 * time.Hour, 900},
		{7 * 24 * time.Hour, 3600},
	}
	for _, tt := range tests {
		if got := ComputeStepSeconds(tt.duration); got != tt.want {
			t.Fatalf("duration %v: got %d want %d", tt.duration, got, tt.want)
		}
	}
}

func TestQuery_UnknownMetric(t *testing.T) {
	resetForTest()
	t.Cleanup(resetForTest)
	_, err := Query(context.Background(), nil, QueryRequest{
		Metric: "bogus", Aggregation: "avg",
		Start: time.Now(), End: time.Now().Add(time.Hour),
	})
	if err != ErrInvalidMetric {
		t.Fatalf("expected ErrInvalidMetric, got %v", err)
	}
}

func TestQuery_InvalidAggregation(t *testing.T) {
	resetForTest()
	t.Cleanup(resetForTest)
	_, err := Query(context.Background(), nil, QueryRequest{
		Metric: "node.cpu", Aggregation: "p99", // cpu doesn't support p99
		Start: time.Now(), End: time.Now().Add(time.Hour),
	})
	if err != ErrInvalidAggregation {
		t.Fatalf("expected ErrInvalidAggregation, got %v", err)
	}
}

func TestQuery_FiltersFamilyMismatch(t *testing.T) {
	resetForTest()
	t.Cleanup(resetForTest)
	_, err := Query(context.Background(), nil, QueryRequest{
		Metric: "node.cpu", Aggregation: "avg",
		Filters: Filters{TaskIDs: []uint{1}}, // not valid for node metric
		Start:   time.Now(), End: time.Now().Add(time.Hour),
	})
	if err != ErrInvalidFilters {
		t.Fatalf("expected ErrInvalidFilters, got %v", err)
	}
}

func TestQuery_TimeRangeInverted(t *testing.T) {
	resetForTest()
	t.Cleanup(resetForTest)
	end := time.Now()
	start := end.Add(time.Hour)
	_, err := Query(context.Background(), nil, QueryRequest{
		Metric: "node.cpu", Aggregation: "avg",
		Start: start, End: end,
	})
	if err != ErrInvalidTimeRange {
		t.Fatalf("expected ErrInvalidTimeRange, got %v", err)
	}
}

func TestQuery_TimeRangeTooLong(t *testing.T) {
	resetForTest()
	t.Cleanup(resetForTest)
	start := time.Now()
	_, err := Query(context.Background(), nil, QueryRequest{
		Metric: "node.cpu", Aggregation: "avg",
		Start: start, End: start.Add(31 * 24 * time.Hour),
	})
	if err != ErrInvalidTimeRange {
		t.Fatalf("expected ErrInvalidTimeRange, got %v", err)
	}
}

func TestQuery_NoProviderRegistered(t *testing.T) {
	resetForTest()
	t.Cleanup(resetForTest)
	// Catalog has node.cpu but no provider is registered → ErrInvalidMetric
	_, err := Query(context.Background(), nil, QueryRequest{
		Metric: "node.cpu", Aggregation: "avg",
		Start: time.Now(), End: time.Now().Add(time.Hour),
	})
	if err != ErrInvalidMetric {
		t.Fatalf("expected ErrInvalidMetric, got %v", err)
	}
}
