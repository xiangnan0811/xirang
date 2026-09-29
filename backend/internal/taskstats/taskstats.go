package taskstats

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

var (
	ErrInvalidMetric      = errors.New("invalid metric")
	ErrInvalidAggregation = errors.New("aggregation not supported for metric")
	ErrInvalidTimeRange   = errors.New("invalid time range")
)

// MaxQueryDuration caps a single task statistics window.
const MaxQueryDuration = 30 * 24 * time.Hour

// MaxRowsPerQuery bounds the rows materialized by one statistics query. A
// response that reaches this cap is marked truncated rather than zero-filled.
const MaxRowsPerQuery = 500000

// Point is one time-bucketed task statistic.
type Point struct {
	Timestamp time.Time `json:"ts"`
	Value     float64   `json:"value"`
}

// Series is a labeled sequence of task statistic points.
type Series struct {
	Name   string  `json:"name"`
	Points []Point `json:"points"`
}

// QueryResponse carries the stable task statistics response shape.
type QueryResponse struct {
	Series      []Series `json:"series"`
	StepSeconds int      `json:"step_seconds"`
	Truncated   bool     `json:"truncated,omitempty"`
}

// Filters contains the task-only query filters.
type Filters struct {
	TaskIDs []uint `json:"task_ids,omitempty"`
}

// QueryRequest is the task-only statistics request. Ownership fields are
// server-only and never bind from JSON.
type QueryRequest struct {
	Metric      string    `json:"metric"`
	Filters     Filters   `json:"filters"`
	Aggregation string    `json:"aggregation"`
	Start       time.Time `json:"start"`
	End         time.Time `json:"end"`

	OwnershipScoped  bool   `json:"-"`
	OwnershipNodeIDs []uint `json:"-"`
}

// RestrictToNodeIDs applies a server-side node ownership scope. It is not part
// of the request JSON and cannot be supplied by a client.
func (r *QueryRequest) RestrictToNodeIDs(nodeIDs []uint) {
	if r == nil {
		return
	}
	r.OwnershipScoped = true
	r.OwnershipNodeIDs = append(r.OwnershipNodeIDs[:0], nodeIDs...)
}

// ComputeStepSeconds returns the bucket size for a window, targeting roughly
// one hundred points. The thresholds are kept from the former task provider.
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

// Query validates and computes task statistics for the requested window.
func Query(ctx context.Context, db *gorm.DB, req QueryRequest) (*QueryResponse, error) {
	if err := validate(req); err != nil {
		return nil, err
	}
	if db == nil {
		return nil, errors.New("task statistics database is unavailable")
	}

	step := ComputeStepSeconds(req.End.Sub(req.Start))
	switch req.Metric {
	case "task.success_rate":
		return querySuccessRate(ctx, db, req, step)
	case "task.throughput":
		return queryThroughput(ctx, db, req, step)
	case "task.duration":
		return queryDuration(ctx, db, req, step)
	default:
		// validate already handles this; keep the switch total if a new metric
		// is added without its query implementation.
		return nil, ErrInvalidMetric
	}
}

func validate(req QueryRequest) error {
	var supported []string
	switch req.Metric {
	case "task.success_rate":
		supported = []string{"avg"}
	case "task.throughput":
		supported = []string{"sum", "avg"}
	case "task.duration":
		supported = []string{"p50", "p95", "p99"}
	default:
		return ErrInvalidMetric
	}
	if !containsString(supported, req.Aggregation) {
		return ErrInvalidAggregation
	}
	if req.End.Before(req.Start) || req.End.Equal(req.Start) {
		return ErrInvalidTimeRange
	}
	if req.End.Sub(req.Start) > MaxQueryDuration {
		return ErrInvalidTimeRange
	}
	return nil
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func querySuccessRate(ctx context.Context, db *gorm.DB, req QueryRequest, step int) (*QueryResponse, error) {
	type row struct {
		TaskID     uint
		Status     string
		FinishedAt time.Time
	}
	var rows []row
	q := db.WithContext(ctx).Table("task_runs").
		Select("task_id, status, finished_at").
		Where("finished_at IS NOT NULL AND finished_at >= ? AND finished_at < ?", req.Start, req.End)
	q = applyTaskOwnershipScope(q, req)
	if len(req.Filters.TaskIDs) > 0 {
		q = q.Where("task_id IN ?", req.Filters.TaskIDs)
	}
	if err := q.Order("task_id ASC, finished_at ASC").Limit(MaxRowsPerQuery).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("success_rate query: %w", err)
	}
	truncated := len(rows) >= MaxRowsPerQuery

	grouped := make(map[uint]map[int64][2]int)
	for _, row := range rows {
		bucket := row.FinishedAt.Unix() / int64(step) * int64(step)
		groupKey := row.TaskID
		if len(req.Filters.TaskIDs) == 0 {
			groupKey = 0
		}
		buckets, ok := grouped[groupKey]
		if !ok {
			buckets = make(map[int64][2]int)
			grouped[groupKey] = buckets
		}
		counts := buckets[bucket]
		counts[1]++
		if row.Status == "success" {
			counts[0]++
		}
		buckets[bucket] = counts
	}

	taskNames, err := taskNameMap(ctx, db, keysOfSuccess(grouped))
	if err != nil {
		return nil, err
	}
	response := buildSuccessSeries(grouped, taskNames, step)
	response.Truncated = truncated
	return response, nil
}

func queryThroughput(ctx context.Context, db *gorm.DB, req QueryRequest, step int) (*QueryResponse, error) {
	type row struct {
		TaskID         uint
		ThroughputMbps float64
		SampledAt      time.Time
	}
	var rows []row
	q := db.WithContext(ctx).Table("task_traffic_samples").
		Select("task_id, throughput_mbps, sampled_at").
		Where("sampled_at >= ? AND sampled_at < ?", req.Start, req.End)
	q = applyTaskOwnershipScope(q, req)
	if len(req.Filters.TaskIDs) > 0 {
		q = q.Where("task_id IN ?", req.Filters.TaskIDs)
	}
	if err := q.Order("task_id ASC, sampled_at ASC").Limit(MaxRowsPerQuery).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("throughput query: %w", err)
	}
	truncated := len(rows) >= MaxRowsPerQuery

	grouped := make(map[uint]map[int64][]float64)
	for _, row := range rows {
		bucket := row.SampledAt.Unix() / int64(step) * int64(step)
		groupKey := row.TaskID
		if len(req.Filters.TaskIDs) == 0 {
			groupKey = 0
		}
		buckets, ok := grouped[groupKey]
		if !ok {
			buckets = make(map[int64][]float64)
			grouped[groupKey] = buckets
		}
		buckets[bucket] = append(buckets[bucket], row.ThroughputMbps)
	}

	taskNames, err := taskNameMap(ctx, db, keysOfFloat(grouped))
	if err != nil {
		return nil, err
	}
	response := buildReduceSeries(grouped, taskNames, step, req.Aggregation)
	response.Truncated = truncated
	return response, nil
}

func queryDuration(ctx context.Context, db *gorm.DB, req QueryRequest, step int) (*QueryResponse, error) {
	type row struct {
		TaskID     uint
		DurationMs int64
		FinishedAt time.Time
	}
	var rows []row
	q := db.WithContext(ctx).Table("task_runs").
		Select("task_id, duration_ms, finished_at").
		Where("finished_at IS NOT NULL AND finished_at >= ? AND finished_at < ? AND duration_ms > 0", req.Start, req.End)
	q = applyTaskOwnershipScope(q, req)
	if len(req.Filters.TaskIDs) > 0 {
		q = q.Where("task_id IN ?", req.Filters.TaskIDs)
	}
	if err := q.Order("task_id ASC, finished_at ASC").Limit(MaxRowsPerQuery).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("duration query: %w", err)
	}
	truncated := len(rows) >= MaxRowsPerQuery

	grouped := make(map[uint]map[int64][]float64)
	for _, row := range rows {
		bucket := row.FinishedAt.Unix() / int64(step) * int64(step)
		groupKey := row.TaskID
		if len(req.Filters.TaskIDs) == 0 {
			groupKey = 0
		}
		buckets, ok := grouped[groupKey]
		if !ok {
			buckets = make(map[int64][]float64)
			grouped[groupKey] = buckets
		}
		buckets[bucket] = append(buckets[bucket], float64(row.DurationMs))
	}

	taskNames, err := taskNameMap(ctx, db, keysOfFloat(grouped))
	if err != nil {
		return nil, err
	}
	response := buildReduceSeries(grouped, taskNames, step, req.Aggregation)
	response.Truncated = truncated
	return response, nil
}

// applyTaskOwnershipScope restricts task rows to tasks on the server-selected
// node set. An empty set deliberately yields no rows rather than all rows.
func applyTaskOwnershipScope(q *gorm.DB, req QueryRequest) *gorm.DB {
	if !req.OwnershipScoped {
		return q
	}
	if len(req.OwnershipNodeIDs) == 0 {
		return q.Where("1 = 0")
	}
	return q.Where("task_id IN (SELECT id FROM tasks WHERE node_id IN ?)", req.OwnershipNodeIDs)
}

func keysOfSuccess(groups map[uint]map[int64][2]int) []uint {
	keys := make([]uint, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	return keys
}

func keysOfFloat(groups map[uint]map[int64][]float64) []uint {
	keys := make([]uint, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	return keys
}

func taskNameMap(ctx context.Context, db *gorm.DB, ids []uint) (map[uint]string, error) {
	names := map[uint]string{}
	realIDs := make([]uint, 0, len(ids))
	for _, id := range ids {
		if id != 0 {
			realIDs = append(realIDs, id)
		}
	}
	if len(realIDs) == 0 {
		return names, nil
	}

	var tasks []model.Task
	if err := db.WithContext(ctx).Select("id, name").Where("id IN ?", realIDs).Find(&tasks).Error; err != nil {
		return nil, fmt.Errorf("task names query: %w", err)
	}
	for _, task := range tasks {
		names[task.ID] = task.Name
	}
	return names, nil
}

func buildSuccessSeries(groups map[uint]map[int64][2]int, names map[uint]string, step int) *QueryResponse {
	ids := keysOfSuccess(groups)
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	series := make([]Series, 0, len(ids))
	for _, id := range ids {
		name := names[id]
		if id == 0 {
			name = "全部任务"
		} else if name == "" {
			name = fmt.Sprintf("task-%d", id)
		}
		buckets := groups[id]
		bucketKeys := make([]int64, 0, len(buckets))
		for key := range buckets {
			bucketKeys = append(bucketKeys, key)
		}
		sort.Slice(bucketKeys, func(i, j int) bool { return bucketKeys[i] < bucketKeys[j] })
		points := make([]Point, 0, len(bucketKeys))
		for _, key := range bucketKeys {
			counts := buckets[key]
			rate := 0.0
			if counts[1] > 0 {
				rate = float64(counts[0]) / float64(counts[1])
			}
			points = append(points, Point{Timestamp: time.Unix(key, 0).UTC(), Value: rate})
		}
		series = append(series, Series{Name: name, Points: points})
	}
	return &QueryResponse{Series: series, StepSeconds: step}
}

func buildReduceSeries(groups map[uint]map[int64][]float64, names map[uint]string, step int, aggregation string) *QueryResponse {
	ids := keysOfFloat(groups)
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	series := make([]Series, 0, len(ids))
	for _, id := range ids {
		name := names[id]
		if id == 0 {
			name = "全部任务"
		} else if name == "" {
			name = fmt.Sprintf("task-%d", id)
		}
		buckets := groups[id]
		bucketKeys := make([]int64, 0, len(buckets))
		for key := range buckets {
			bucketKeys = append(bucketKeys, key)
		}
		sort.Slice(bucketKeys, func(i, j int) bool { return bucketKeys[i] < bucketKeys[j] })
		points := make([]Point, 0, len(bucketKeys))
		for _, key := range bucketKeys {
			points = append(points, Point{
				Timestamp: time.Unix(key, 0).UTC(),
				Value:     reduce(buckets[key], aggregation),
			})
		}
		series = append(series, Series{Name: name, Points: points})
	}
	return &QueryResponse{Series: series, StepSeconds: step}
}

// reduce applies an aggregation to a non-empty bucket.
func reduce(values []float64, aggregation string) float64 {
	if len(values) == 0 {
		return 0
	}
	switch aggregation {
	case "avg":
		total := 0.0
		for _, value := range values {
			total += value
		}
		return total / float64(len(values))
	case "sum":
		total := 0.0
		for _, value := range values {
			total += value
		}
		return total
	case "p50":
		return percentile(values, 0.50)
	case "p95":
		return percentile(values, 0.95)
	case "p99":
		return percentile(values, 0.99)
	default:
		return 0
	}
}

// percentile uses nearest-rank semantics (1-based, rounded up).
func percentile(values []float64, p float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	rank := int(math.Ceil(float64(len(sorted)) * p))
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}
