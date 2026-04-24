package providers

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"xirang/backend/internal/dashboards"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

// NodeMetricsProvider queries node_metric_samples.
type NodeMetricsProvider struct {
	db *gorm.DB
}

// NewNodeProvider returns a new provider bound to db.
func NewNodeProvider(db *gorm.DB) *NodeMetricsProvider {
	return &NodeMetricsProvider{db: db}
}

// metricToColumn maps dashboard metric keys to node_metric_samples columns.
var metricToColumn = map[string]string{
	"node.cpu":        "cpu_pct",
	"node.memory":     "mem_pct",
	"node.disk_pct":   "disk_pct",
	"node.load":       "load_1m",
	"node.latency_ms": "latency_ms",
}

func (p *NodeMetricsProvider) Family() dashboards.MetricFamily { return dashboards.FamilyNode }

func (p *NodeMetricsProvider) Supports(metric string) bool {
	_, ok := metricToColumn[metric]
	return ok
}

func (p *NodeMetricsProvider) SupportedAggregations(metric string) []string {
	if metric == "node.latency_ms" {
		return []string{"avg", "max", "min", "p50", "p95", "p99"}
	}
	if _, ok := metricToColumn[metric]; ok {
		return []string{"avg", "max", "min"}
	}
	return nil
}

// Query reads samples in [start, end), groups by (node_id, bucket) where
// bucket = floor(UNIX(sampled_at) / stepSeconds), applies aggregation,
// and returns one series per node.
func (p *NodeMetricsProvider) Query(ctx context.Context, req dashboards.QueryRequest, stepSeconds int) (*dashboards.QueryResponse, error) {
	col, ok := metricToColumn[req.Metric]
	if !ok {
		return nil, dashboards.ErrInvalidMetric
	}

	nodeIDs := req.Filters.NodeIDs
	aggSQL, needInMemoryPercentile := aggregationSQL(req.Aggregation, col)

	// We bucket by dividing unix seconds by stepSeconds. SQLite has strftime('%s'),
	// Postgres has EXTRACT(EPOCH FROM ...). Use a Go computation via CASE, which is
	// dialect-neutral: convert sampled_at to seconds via julianday or epoch.
	// For simplicity and portability, we do bucket computation in Go after fetching raw rows.

	type row struct {
		NodeID    uint
		Value     float64
		SampledAt time.Time
	}
	var rows []row
	q := p.db.WithContext(ctx).
		Table("node_metric_samples").
		Select("node_id, "+col+" AS value, sampled_at").
		Where("sampled_at >= ? AND sampled_at < ? AND probe_ok = ?", req.Start, req.End, true)
	if len(nodeIDs) > 0 {
		q = q.Where("node_id IN ?", nodeIDs)
	}
	if err := q.Order("node_id ASC, sampled_at ASC").
		Limit(dashboards.MaxRowsPerQuery).
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("query samples: %w", err)
	}
	truncated := len(rows) >= dashboards.MaxRowsPerQuery

	// If filter was empty, populate with all node IDs that appeared in rows
	// (so empty filter = "all nodes with data in window").

	// Bucket rows: map[nodeID]map[bucketStart]*[]float64
	buckets := make(map[uint]map[int64][]float64)
	for _, r := range rows {
		b := r.SampledAt.Unix() / int64(stepSeconds) * int64(stepSeconds)
		m, ok := buckets[r.NodeID]
		if !ok {
			m = make(map[int64][]float64)
			buckets[r.NodeID] = m
		}
		m[b] = append(m[b], r.Value)
	}

	// Resolve node names (best-effort; missing nodes keep numeric label).
	nameByID := make(map[uint]string)
	if len(buckets) > 0 {
		ids := make([]uint, 0, len(buckets))
		for id := range buckets {
			ids = append(ids, id)
		}
		var nodes []model.Node
		if err := p.db.WithContext(ctx).Select("id, name").Where("id IN ?", ids).Find(&nodes).Error; err == nil {
			for _, n := range nodes {
				nameByID[n.ID] = n.Name
			}
		}
	}

	// Build series in stable order.
	seriesIDs := make([]uint, 0, len(buckets))
	for id := range buckets {
		seriesIDs = append(seriesIDs, id)
	}
	sort.Slice(seriesIDs, func(i, j int) bool { return seriesIDs[i] < seriesIDs[j] })

	series := make([]dashboards.Series, 0, len(seriesIDs))
	for _, id := range seriesIDs {
		name := nameByID[id]
		if name == "" {
			name = fmt.Sprintf("node-%d", id)
		}
		pts := aggregateBuckets(buckets[id], aggSQL, req.Aggregation, needInMemoryPercentile)
		series = append(series, dashboards.Series{Name: name, Points: pts})
	}

	return &dashboards.QueryResponse{Series: series, StepSeconds: stepSeconds, Truncated: truncated}, nil
}

// aggregationSQL returns (SQL fragment, needInMemoryPercentile).
// We apply aggregations in Go (see aggregateBuckets). SQL fragment is unused
// except for documentation / future optimization.
func aggregationSQL(agg, col string) (string, bool) {
	switch agg {
	case "avg", "max", "min", "sum":
		return agg + "(" + col + ")", false
	case "p50", "p95", "p99":
		return "", true
	}
	return "", false
}

// aggregateBuckets reduces each bucket's slice of floats to a single Point.
func aggregateBuckets(m map[int64][]float64, _ string, agg string, _ bool) []dashboards.Point {
	keys := make([]int64, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	pts := make([]dashboards.Point, 0, len(keys))
	for _, k := range keys {
		v := reduce(m[k], agg)
		pts = append(pts, dashboards.Point{Timestamp: time.Unix(k, 0).UTC(), Value: v})
	}
	return pts
}

// reduce applies the aggregation to a non-empty slice.
func reduce(xs []float64, agg string) float64 {
	if len(xs) == 0 {
		return 0
	}
	switch agg {
	case "avg":
		s := 0.0
		for _, x := range xs {
			s += x
		}
		return s / float64(len(xs))
	case "sum":
		s := 0.0
		for _, x := range xs {
			s += x
		}
		return s
	case "max":
		m := xs[0]
		for _, x := range xs[1:] {
			if x > m {
				m = x
			}
		}
		return m
	case "min":
		m := xs[0]
		for _, x := range xs[1:] {
			if x < m {
				m = x
			}
		}
		return m
	case "p50":
		return percentile(xs, 0.50)
	case "p95":
		return percentile(xs, 0.95)
	case "p99":
		return percentile(xs, 0.99)
	}
	return 0
}

// percentile returns the nearest-rank percentile (1-based, rounded up).
func percentile(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	sorted := append([]float64(nil), xs...)
	sort.Float64s(sorted)
	// Nearest-rank percentile: rank = ceil(p * N), clamped to [1, N].
	rank := int(math.Ceil(float64(len(sorted)) * p))
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}
