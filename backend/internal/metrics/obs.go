package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Use promauto to auto-register to the default registry (same pattern as
// backend/internal/middleware/metrics.go). All new metrics surface through
// the existing /metrics HTTP endpoint from P4.

var rollupDurationSeconds = promauto.NewHistogramVec(prometheus.HistogramOpts{
	Name:    "xirang_metric_rollup_duration_seconds",
	Help:    "Duration of one rollup tick per tier (hourly/daily).",
	Buckets: prometheus.DefBuckets,
}, []string{"tier"})

var rollupLagSeconds = promauto.NewGaugeVec(prometheus.GaugeOpts{
	Name: "xirang_metric_rollup_lag_seconds",
	Help: "Seconds between the newest aggregated bucket and now.",
}, []string{"tier"})

// SinkDropped is exported because RemoteWriteSink (Task 18) increments it.
// DBSink does not drop — only bounded-queue sinks do.
var SinkDropped = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "xirang_metric_sink_dropped_total",
	Help: "Samples dropped by a metric sink due to overflow or fatal failure.",
}, []string{"sink"})

// remoteWriteTotal counts Prometheus remote-write attempts by terminal
// status. Incremented from RemoteWriteSink.Write. Use rate of
// xirang_metrics_remote_write_total{status="failure"} to detect chronic
// remote-endpoint failure without burning the operator's attention.
var remoteWriteTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "xirang_metrics_remote_write_total",
	Help: "Total Prometheus remote-write attempts by status",
}, []string{"status"})
