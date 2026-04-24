package providers

import (
	"context"
	"fmt"
	"sort"
	"time"

	"xirang/backend/internal/dashboards"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

// TaskStatsProvider queries task_runs and task_traffic_samples.
type TaskStatsProvider struct {
	db *gorm.DB
}

// NewTaskProvider returns a new provider bound to db.
func NewTaskProvider(db *gorm.DB) *TaskStatsProvider {
	return &TaskStatsProvider{db: db}
}

func (p *TaskStatsProvider) Family() dashboards.MetricFamily { return dashboards.FamilyTask }

func (p *TaskStatsProvider) Supports(metric string) bool {
	switch metric {
	case "task.success_rate", "task.throughput", "task.duration_p95":
		return true
	}
	return false
}

func (p *TaskStatsProvider) SupportedAggregations(metric string) []string {
	switch metric {
	case "task.success_rate":
		return []string{"avg"}
	case "task.throughput":
		return []string{"sum", "avg"}
	case "task.duration_p95":
		return []string{"p50", "p95", "p99"}
	}
	return nil
}

// Query routes by metric to the appropriate internal method.
func (p *TaskStatsProvider) Query(ctx context.Context, req dashboards.QueryRequest, stepSeconds int) (*dashboards.QueryResponse, error) {
	switch req.Metric {
	case "task.success_rate":
		return p.querySuccessRate(ctx, req, stepSeconds)
	case "task.throughput":
		return p.queryThroughput(ctx, req, stepSeconds)
	case "task.duration_p95":
		return p.queryDuration(ctx, req, stepSeconds)
	}
	return nil, dashboards.ErrInvalidMetric
}

// querySuccessRate reads task_runs finished in [start, end), groups by
// (task_id, bucket by finished_at), returns successes/total per bucket.
// When task_ids filter is empty, aggregates into a single "all" series.
func (p *TaskStatsProvider) querySuccessRate(ctx context.Context, req dashboards.QueryRequest, step int) (*dashboards.QueryResponse, error) {
	type row struct {
		TaskID     uint
		Status     string
		FinishedAt time.Time
	}
	var rows []row
	q := p.db.WithContext(ctx).Table("task_runs").
		Select("task_id, status, finished_at").
		Where("finished_at IS NOT NULL AND finished_at >= ? AND finished_at < ?", req.Start, req.End)
	if len(req.Filters.TaskIDs) > 0 {
		q = q.Where("task_id IN ?", req.Filters.TaskIDs)
	}
	if err := q.Order("task_id ASC, finished_at ASC").
		Limit(dashboards.MaxRowsPerQuery).
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("success_rate query: %w", err)
	}
	truncated := len(rows) >= dashboards.MaxRowsPerQuery

	grouped := make(map[uint]map[int64][2]int) // taskID -> bucket -> [successes, total]
	for _, r := range rows {
		b := r.FinishedAt.Unix() / int64(step) * int64(step)
		groupKey := r.TaskID
		if len(req.Filters.TaskIDs) == 0 {
			groupKey = 0 // aggregated series
		}
		m, ok := grouped[groupKey]
		if !ok {
			m = make(map[int64][2]int)
			grouped[groupKey] = m
		}
		v := m[b]
		v[1]++
		if r.Status == "success" {
			v[0]++
		}
		m[b] = v
	}

	taskNames := taskNameMap(ctx, p.db, keysOf(grouped))
	resp := buildSuccessSeries(grouped, taskNames, step)
	resp.Truncated = truncated
	return resp, nil
}

func (p *TaskStatsProvider) queryThroughput(ctx context.Context, req dashboards.QueryRequest, step int) (*dashboards.QueryResponse, error) {
	type row struct {
		TaskID         uint
		ThroughputMbps float64
		SampledAt      time.Time
	}
	var rows []row
	q := p.db.WithContext(ctx).Table("task_traffic_samples").
		Select("task_id, throughput_mbps, sampled_at").
		Where("sampled_at >= ? AND sampled_at < ?", req.Start, req.End)
	if len(req.Filters.TaskIDs) > 0 {
		q = q.Where("task_id IN ?", req.Filters.TaskIDs)
	}
	if err := q.Order("task_id ASC, sampled_at ASC").
		Limit(dashboards.MaxRowsPerQuery).
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("throughput query: %w", err)
	}
	truncated := len(rows) >= dashboards.MaxRowsPerQuery

	grouped := make(map[uint]map[int64][]float64)
	for _, r := range rows {
		b := r.SampledAt.Unix() / int64(step) * int64(step)
		groupKey := r.TaskID
		if len(req.Filters.TaskIDs) == 0 {
			groupKey = 0
		}
		m, ok := grouped[groupKey]
		if !ok {
			m = make(map[int64][]float64)
			grouped[groupKey] = m
		}
		m[b] = append(m[b], r.ThroughputMbps)
	}

	taskNames := taskNameMap(ctx, p.db, keysOfFloat(grouped))
	resp := buildReduceSeries(grouped, taskNames, step, req.Aggregation)
	resp.Truncated = truncated
	return resp, nil
}

func (p *TaskStatsProvider) queryDuration(ctx context.Context, req dashboards.QueryRequest, step int) (*dashboards.QueryResponse, error) {
	type row struct {
		TaskID     uint
		DurationMs int64
		FinishedAt time.Time
	}
	var rows []row
	q := p.db.WithContext(ctx).Table("task_runs").
		Select("task_id, duration_ms, finished_at").
		Where("finished_at IS NOT NULL AND finished_at >= ? AND finished_at < ? AND duration_ms > 0", req.Start, req.End)
	if len(req.Filters.TaskIDs) > 0 {
		q = q.Where("task_id IN ?", req.Filters.TaskIDs)
	}
	if err := q.Order("task_id ASC, finished_at ASC").
		Limit(dashboards.MaxRowsPerQuery).
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("duration query: %w", err)
	}
	truncated := len(rows) >= dashboards.MaxRowsPerQuery

	grouped := make(map[uint]map[int64][]float64)
	for _, r := range rows {
		b := r.FinishedAt.Unix() / int64(step) * int64(step)
		groupKey := r.TaskID
		if len(req.Filters.TaskIDs) == 0 {
			groupKey = 0
		}
		m, ok := grouped[groupKey]
		if !ok {
			m = make(map[int64][]float64)
			grouped[groupKey] = m
		}
		m[b] = append(m[b], float64(r.DurationMs))
	}

	taskNames := taskNameMap(ctx, p.db, keysOfFloat(grouped))
	resp := buildReduceSeries(grouped, taskNames, step, req.Aggregation)
	resp.Truncated = truncated
	return resp, nil
}

func keysOf(m map[uint]map[int64][2]int) []uint {
	out := make([]uint, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func keysOfFloat(m map[uint]map[int64][]float64) []uint {
	out := make([]uint, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func taskNameMap(ctx context.Context, db *gorm.DB, ids []uint) map[uint]string {
	out := map[uint]string{}
	real := make([]uint, 0, len(ids))
	for _, id := range ids {
		if id != 0 {
			real = append(real, id)
		}
	}
	if len(real) == 0 {
		return out
	}
	var tasks []model.Task
	if err := db.WithContext(ctx).Select("id, name").Where("id IN ?", real).Find(&tasks).Error; err == nil {
		for _, t := range tasks {
			out[t.ID] = t.Name
		}
	}
	return out
}

func buildSuccessSeries(g map[uint]map[int64][2]int, names map[uint]string, step int) *dashboards.QueryResponse {
	ids := make([]uint, 0, len(g))
	for id := range g {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	series := make([]dashboards.Series, 0, len(ids))
	for _, id := range ids {
		name := names[id]
		if id == 0 {
			name = "全部任务"
		} else if name == "" {
			name = fmt.Sprintf("task-%d", id)
		}
		buckets := g[id]
		keys := make([]int64, 0, len(buckets))
		for k := range buckets {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
		pts := make([]dashboards.Point, 0, len(keys))
		for _, k := range keys {
			v := buckets[k]
			rate := 0.0
			if v[1] > 0 {
				rate = float64(v[0]) / float64(v[1])
			}
			pts = append(pts, dashboards.Point{Timestamp: time.Unix(k, 0).UTC(), Value: rate})
		}
		series = append(series, dashboards.Series{Name: name, Points: pts})
	}
	return &dashboards.QueryResponse{Series: series, StepSeconds: step}
}

func buildReduceSeries(g map[uint]map[int64][]float64, names map[uint]string, step int, agg string) *dashboards.QueryResponse {
	ids := keysOfFloat(g)
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	series := make([]dashboards.Series, 0, len(ids))
	for _, id := range ids {
		name := names[id]
		if id == 0 {
			name = "全部任务"
		} else if name == "" {
			name = fmt.Sprintf("task-%d", id)
		}
		buckets := g[id]
		keys := make([]int64, 0, len(buckets))
		for k := range buckets {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
		pts := make([]dashboards.Point, 0, len(keys))
		for _, k := range keys {
			pts = append(pts, dashboards.Point{Timestamp: time.Unix(k, 0).UTC(), Value: reduce(buckets[k], agg)})
		}
		series = append(series, dashboards.Series{Name: name, Points: pts})
	}
	return &dashboards.QueryResponse{Series: series, StepSeconds: step}
}
