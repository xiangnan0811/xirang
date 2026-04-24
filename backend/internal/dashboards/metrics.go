package dashboards

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Prometheus metrics for the dashboards subsystem. Without these the subsystem
// is a production black box — panel queries can go slow or hit the row-cap
// truncation path and operators have no signal.

var (
	PanelQueryDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "xirang_dashboards_panel_query_duration_seconds",
		Help:    "Latency of one panel query, bucketed by metric family.",
		Buckets: prometheus.DefBuckets,
	}, []string{"family"})

	PanelQueryTruncated = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "xirang_dashboards_panel_query_truncated_total",
		Help: "Count of panel queries whose row fetch hit MaxRowsPerQuery and returned Truncated=true.",
	}, []string{"family"})

	PanelQueryErrors = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "xirang_dashboards_panel_query_errors_total",
		Help: "Count of panel query failures, labeled by family.",
	}, []string{"family"})
)
