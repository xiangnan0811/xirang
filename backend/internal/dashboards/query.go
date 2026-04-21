package dashboards

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// MaxQueryDuration caps any single query window.
const MaxQueryDuration = 30 * 24 * time.Hour

// Query validates req, computes step, and dispatches to the matching provider.
func Query(ctx context.Context, _ *gorm.DB, req QueryRequest) (*QueryResponse, error) {
	desc := DescribeMetric(req.Metric)
	if desc == nil {
		return nil, ErrInvalidMetric
	}
	if !containsString(desc.SupportedAggregations, req.Aggregation) {
		return nil, ErrInvalidAggregation
	}
	if err := validateFilters(desc, req.Filters); err != nil {
		return nil, err
	}
	if req.End.Before(req.Start) || req.End.Equal(req.Start) {
		return nil, ErrInvalidTimeRange
	}
	if req.End.Sub(req.Start) > MaxQueryDuration {
		return nil, ErrInvalidTimeRange
	}

	provider, ok := findProvider(req.Metric)
	if !ok {
		return nil, ErrInvalidMetric
	}
	step := ComputeStepSeconds(req.End.Sub(req.Start))
	return provider.Query(ctx, req, step)
}

// ComputeStepSeconds returns the bucket size for a given window, targeting ~100 points.
func ComputeStepSeconds(d time.Duration) int {
	secs := int(d.Seconds())
	switch {
	case secs <= 480:
		return 5
	case secs <= 1500:
		return 15
	case secs <= 3000:
		return 30
	case secs <= 5400:
		return 60
	case secs <= 28800:
		return 300
	case secs <= 86400:
		return 900
	default:
		return 3600
	}
}

func validateFilters(desc *MetricDescriptor, f Filters) error {
	switch desc.Family {
	case FamilyNode:
		if len(f.TaskIDs) > 0 {
			return ErrInvalidFilters
		}
	case FamilyTask:
		if len(f.NodeIDs) > 0 {
			return ErrInvalidFilters
		}
	}
	return nil
}

func containsString(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}
