package nodelogs

import (
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	journalRecoveries = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "xirang_node_logs_journal_recoveries_total",
		Help: "Persisted journal recovery boundary resets; not a count of skipped entries",
	}, []string{"reason"})
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

	jobsDeduplicated = promauto.NewCounter(prometheus.CounterOpts{
		Name: "xirang_node_logs_jobs_deduplicated_total",
		Help: "Collection jobs skipped because the node is already queued or running",
	})
	queueRejected = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "xirang_node_logs_queue_rejected_total",
		Help: "Collection jobs rejected by the scheduler",
	}, []string{"reason"})
	inFlight = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "xirang_node_logs_in_flight",
		Help: "Collection jobs currently running",
	})
	shutdownTimeouts = promauto.NewCounter(prometheus.CounterOpts{
		Name: "xirang_node_logs_shutdown_timeouts_total",
		Help: "Scheduler shutdown calls whose deadline expired before workers joined",
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
