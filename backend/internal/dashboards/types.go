package dashboards

import (
	"context"
	"errors"
	"time"
)

// Sentinel errors mapped by handlers to HTTP status codes.
var (
	ErrNotFound           = errors.New("dashboard not found")
	ErrConflict           = errors.New("dashboard name already exists")
	ErrInvalidMetric      = errors.New("invalid metric")
	ErrInvalidAggregation = errors.New("aggregation not supported for metric")
	ErrInvalidFilters     = errors.New("filters not valid for metric")
	ErrInvalidTimeRange   = errors.New("invalid time range")
)

// MetricFamily discriminates providers.
type MetricFamily string

const (
	FamilyNode MetricFamily = "node"
	FamilyTask MetricFamily = "task"
)

// Point is one time-bucketed value.
type Point struct {
	Timestamp time.Time `json:"ts"`
	Value     float64   `json:"value"`
}

// Series is a labeled sequence of points.
type Series struct {
	Name   string  `json:"name"`
	Points []Point `json:"points"`
}

// QueryRequest is the canonical input for a panel query.
type QueryRequest struct {
	Metric      string    `json:"metric"`
	Filters     Filters   `json:"filters"`
	Aggregation string    `json:"aggregation"`
	Start       time.Time `json:"start"`
	End         time.Time `json:"end"`
}

// Filters mirrors model.PanelFilters but avoids an import cycle.
type Filters struct {
	NodeIDs []uint `json:"node_ids,omitempty"`
	TaskIDs []uint `json:"task_ids,omitempty"`
}

// QueryResponse carries series and the step actually used.
type QueryResponse struct {
	Series      []Series `json:"series"`
	StepSeconds int      `json:"step_seconds"`
}

// Provider queries a specific family of metrics.
type Provider interface {
	Family() MetricFamily
	Supports(metric string) bool
	SupportedAggregations(metric string) []string
	Query(ctx context.Context, req QueryRequest, stepSeconds int) (*QueryResponse, error)
}
