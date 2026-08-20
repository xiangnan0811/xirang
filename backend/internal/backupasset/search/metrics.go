package search

import (
	"fmt"

	"xirang/backend/internal/backupasset"

	"github.com/prometheus/client_golang/prometheus"
)

type BuildOutcome string

const (
	BuildOutcomeSuccess  BuildOutcome = "success"
	BuildOutcomeFailure  BuildOutcome = "failure"
	BuildOutcomeCanceled BuildOutcome = "canceled"
	BuildOutcomeFenced   BuildOutcome = "fenced"
)

type ScanOutcome string

const (
	ScanOutcomeSuccess  ScanOutcome = "success"
	ScanOutcomeFailure  ScanOutcome = "failure"
	ScanOutcomeDisabled ScanOutcome = "disabled"
)

type Metrics interface {
	ObserveBuild(BuildOutcome)
	ObserveScan(ScanOutcome)
	SetActiveBuilds(int)
	AddReconciledAbandoned(int64)
	AddReconciledOverlays(int64)
}

type NoopMetrics struct{}

func (NoopMetrics) ObserveBuild(BuildOutcome)    {}
func (NoopMetrics) ObserveScan(ScanOutcome)      {}
func (NoopMetrics) SetActiveBuilds(int)          {}
func (NoopMetrics) AddReconciledAbandoned(int64) {}
func (NoopMetrics) AddReconciledOverlays(int64)  {}

type PrometheusMetrics struct {
	builds              *prometheus.CounterVec
	scans               *prometheus.CounterVec
	activeBuilds        prometheus.Gauge
	reconciledAbandoned prometheus.Counter
	reconciledOverlays  prometheus.Counter
}

func NewPrometheusMetrics(registerer prometheus.Registerer) (*PrometheusMetrics, error) {
	if registerer == nil {
		return nil, fmt.Errorf("%w: Search Prometheus registerer unavailable", backupasset.ErrInvalidState)
	}
	metrics := &PrometheusMetrics{
		builds: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "xirang_backup_asset_search_builds_total",
			Help: "Total terminal backup asset Search build outcomes.",
		}, []string{"outcome"}),
		scans: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "xirang_backup_asset_search_scans_total",
			Help: "Total backup asset Search scheduler scans.",
		}, []string{"outcome"}),
		activeBuilds: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "xirang_backup_asset_search_active_builds",
			Help: "Current active backup asset Search builds.",
		}),
		reconciledAbandoned: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "xirang_backup_asset_search_reconciled_abandoned_total",
			Help: "Total abandoned Search builds reconciled as failed.",
		}),
		reconciledOverlays: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "xirang_backup_asset_search_reconciled_overlays_total",
			Help: "Total Search overlay rows reconciled.",
		}),
	}
	for _, collector := range []prometheus.Collector{
		metrics.builds, metrics.scans, metrics.activeBuilds, metrics.reconciledAbandoned, metrics.reconciledOverlays,
	} {
		if err := registerer.Register(collector); err != nil {
			return nil, fmt.Errorf("register backup asset Search metric: %w", err)
		}
	}
	return metrics, nil
}

func (metrics *PrometheusMetrics) ObserveBuild(outcome BuildOutcome) {
	if metrics != nil {
		metrics.builds.WithLabelValues(metricBuildOutcome(outcome)).Inc()
	}
}

func (metrics *PrometheusMetrics) ObserveScan(outcome ScanOutcome) {
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

func (metrics *PrometheusMetrics) AddReconciledAbandoned(count int64) {
	if metrics != nil && count > 0 {
		metrics.reconciledAbandoned.Add(float64(count))
	}
}

func (metrics *PrometheusMetrics) AddReconciledOverlays(count int64) {
	if metrics != nil && count > 0 {
		metrics.reconciledOverlays.Add(float64(count))
	}
}

func metricBuildOutcome(outcome BuildOutcome) string {
	if validBuildOutcome(outcome) {
		return string(outcome)
	}
	return "unknown"
}

func metricScanOutcome(outcome ScanOutcome) string {
	if validScanOutcome(outcome) {
		return string(outcome)
	}
	return "unknown"
}

func validBuildOutcome(value BuildOutcome) bool {
	return value == BuildOutcomeSuccess || value == BuildOutcomeFailure || value == BuildOutcomeCanceled || value == BuildOutcomeFenced
}

func validScanOutcome(value ScanOutcome) bool {
	return value == ScanOutcomeSuccess || value == ScanOutcomeFailure || value == ScanOutcomeDisabled
}

var _ Metrics = (*PrometheusMetrics)(nil)
var _ Metrics = NoopMetrics{}
