package catalog

import (
	"fmt"
	"time"

	"xirang/backend/internal/backupasset"

	"github.com/prometheus/client_golang/prometheus"
)

type MetricBuildOutcome string

const (
	MetricBuildComplete MetricBuildOutcome = "complete"
	MetricBuildPartial  MetricBuildOutcome = "partial"
	MetricBuildFailed   MetricBuildOutcome = "failed"
	MetricBuildCanceled MetricBuildOutcome = "canceled"
	MetricBuildSkipped  MetricBuildOutcome = "skipped"
)

type MetricScanOutcome string

const (
	MetricScanSuccess  MetricScanOutcome = "success"
	MetricScanFailure  MetricScanOutcome = "failure"
	MetricScanDisabled MetricScanOutcome = "disabled"
	MetricScanSkipped  MetricScanOutcome = "skipped"
)

type Metrics interface {
	ObserveBuild(MetricBuildOutcome, time.Duration)
	ObserveScan(MetricScanOutcome)
	SetActiveBuilds(int)
	AddReconciledAbandoned(int)
}

type NoopMetrics struct{}

func (NoopMetrics) ObserveBuild(MetricBuildOutcome, time.Duration) {}
func (NoopMetrics) ObserveScan(MetricScanOutcome)                  {}
func (NoopMetrics) SetActiveBuilds(int)                            {}
func (NoopMetrics) AddReconciledAbandoned(int)                     {}

type PrometheusMetrics struct {
	builds              *prometheus.CounterVec
	buildDuration       *prometheus.HistogramVec
	scans               *prometheus.CounterVec
	activeBuilds        prometheus.Gauge
	reconciledAbandoned prometheus.Counter
}

func NewPrometheusMetrics(registerer prometheus.Registerer) (*PrometheusMetrics, error) {
	if registerer == nil {
		return nil, fmt.Errorf("%w: Catalog Prometheus registerer unavailable", backupasset.ErrInvalidState)
	}
	metrics := &PrometheusMetrics{
		builds: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "xirang_backup_asset_catalog_builds_total",
			Help: "Total terminal backup asset Catalog build outcomes.",
		}, []string{"outcome"}),
		buildDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "xirang_backup_asset_catalog_build_duration_seconds",
			Help: "Duration of terminal backup asset Catalog builds.", Buckets: prometheus.DefBuckets,
		}, []string{"outcome"}),
		scans: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "xirang_backup_asset_catalog_scans_total",
			Help: "Total backup asset Catalog scheduler scans.",
		}, []string{"outcome"}),
		activeBuilds: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "xirang_backup_asset_catalog_active_builds",
			Help: "Current active backup asset Catalog builds.",
		}),
		reconciledAbandoned: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "xirang_backup_asset_catalog_reconciled_abandoned_total",
			Help: "Total abandoned Catalog generations reconciled as failed.",
		}),
	}
	for _, collector := range []prometheus.Collector{metrics.builds, metrics.buildDuration, metrics.scans, metrics.activeBuilds, metrics.reconciledAbandoned} {
		if err := registerer.Register(collector); err != nil {
			return nil, fmt.Errorf("register backup asset Catalog metric: %w", err)
		}
	}
	return metrics, nil
}

func (metrics *PrometheusMetrics) ObserveBuild(outcome MetricBuildOutcome, duration time.Duration) {
	if metrics == nil {
		return
	}
	if duration < 0 {
		duration = 0
	}
	label := metricBuildOutcome(outcome)
	metrics.builds.WithLabelValues(label).Inc()
	metrics.buildDuration.WithLabelValues(label).Observe(duration.Seconds())
}

func (metrics *PrometheusMetrics) ObserveScan(outcome MetricScanOutcome) {
	if metrics != nil {
		metrics.scans.WithLabelValues(metricScanOutcome(outcome)).Inc()
	}
}

func (metrics *PrometheusMetrics) SetActiveBuilds(count int) {
	if metrics == nil {
		return
	}
	if count < 0 {
		count = 0
	}
	metrics.activeBuilds.Set(float64(count))
}

func (metrics *PrometheusMetrics) AddReconciledAbandoned(count int) {
	if metrics != nil && count > 0 {
		metrics.reconciledAbandoned.Add(float64(count))
	}
}

func metricBuildOutcome(outcome MetricBuildOutcome) string {
	switch outcome {
	case MetricBuildComplete, MetricBuildPartial, MetricBuildFailed, MetricBuildCanceled, MetricBuildSkipped:
		return string(outcome)
	default:
		return "unknown"
	}
}

func metricScanOutcome(outcome MetricScanOutcome) string {
	switch outcome {
	case MetricScanSuccess, MetricScanFailure, MetricScanDisabled, MetricScanSkipped:
		return string(outcome)
	default:
		return "unknown"
	}
}

var _ Metrics = (*PrometheusMetrics)(nil)
var _ Metrics = NoopMetrics{}
