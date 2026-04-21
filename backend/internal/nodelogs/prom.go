package nodelogs

import (
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	logsIngested = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "xirang_node_logs_ingested_total",
		Help: "Log entries inserted",
	}, []string{"node_id", "source"})

	fetchDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "xirang_node_logs_fetch_duration_seconds",
		Help:    "Fetch latency",
		Buckets: prometheus.ExponentialBuckets(0.1, 2, 8),
	}, []string{"node_id"})

	fetchErrors = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "xirang_node_logs_fetch_errors_total",
		Help: "Fetch errors by reason",
	}, []string{"node_id", "reason"})

	queueDepth = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "xirang_node_logs_queue_depth",
		Help: "Current scheduler->worker queue depth",
	})

	retentionDeleted = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "xirang_node_logs_retention_deleted_total",
		Help: "Rows deleted by retention",
	}, []string{"node_id"})
)

func nodeIDLabel(id uint) string { return strconv.FormatUint(uint64(id), 10) }

// ClearPromMetricsForNode wipes a node's gauge/counter series on delete.
func ClearPromMetricsForNode(id uint) {
	l := prometheus.Labels{"node_id": nodeIDLabel(id)}
	logsIngested.DeletePartialMatch(l)
	fetchDuration.DeletePartialMatch(l)
	fetchErrors.DeletePartialMatch(l)
	retentionDeleted.DeletePartialMatch(l)
}
