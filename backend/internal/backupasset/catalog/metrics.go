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

// StorageObservation is the whole-database aggregate collected once per Catalog
// scan. It intentionally carries no recovery-point, path, or locator identity so
// it cannot multiply Prometheus label cardinality.
type StorageObservation struct {
	GenerationsByState     map[string]int64
	MaxGenerationsPerPoint int64
	EntryCount             int64
	SQLiteFileBytes        int64
}

type Metrics interface {
	ObserveBuild(MetricBuildOutcome, time.Duration)
	ObserveScan(MetricScanOutcome)
	SetActiveBuilds(int)
	AddReconciledAbandoned(int)
	ObserveStorage(StorageObservation)
	AddGCDeletedGenerations(int)
	AddGCSkippedRestricted(int)
}

type NoopMetrics struct{}

func (NoopMetrics) ObserveBuild(MetricBuildOutcome, time.Duration) {}
func (NoopMetrics) ObserveScan(MetricScanOutcome)                  {}
func (NoopMetrics) SetActiveBuilds(int)                            {}
func (NoopMetrics) AddReconciledAbandoned(int)                     {}
func (NoopMetrics) ObserveStorage(StorageObservation)              {}
func (NoopMetrics) AddGCDeletedGenerations(int)                    {}
func (NoopMetrics) AddGCSkippedRestricted(int)                     {}

// metricGenerationStates is the frozen Catalog generation-state label set
// (every persisted state, including building), so a stray database value can
// never become a Prometheus series.
func metricGenerationStates() []string {
	return []string{
		string(GenerationBuilding), string(GenerationComplete),
		string(GenerationPartial), string(GenerationFailed), string(GenerationSuperseded),
	}
}

type PrometheusMetrics struct {
	builds                 *prometheus.CounterVec
	buildDuration          *prometheus.HistogramVec
	scans                  *prometheus.CounterVec
	activeBuilds           prometheus.Gauge
	reconciledAbandoned    prometheus.Counter
	generations            *prometheus.GaugeVec
	generationsMaxPerPoint prometheus.Gauge
	entries                prometheus.Gauge
	sqliteFileBytes        prometheus.Gauge
	gcDeletedGenerations   prometheus.Counter
	gcSkippedRestricted    prometheus.Counter
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
		generations: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "xirang_backup_asset_catalog_generations",
			Help: "Current backup asset Catalog generations by frozen generation state.",
		}, []string{"state"}),
		generationsMaxPerPoint: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "xirang_backup_asset_catalog_generations_max_per_point",
			Help: "Largest number of Catalog generations retained by a single recovery point.",
		}),
		entries: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "xirang_backup_asset_catalog_entries",
			Help: "Current backup asset Catalog entry rows.",
		}),
		sqliteFileBytes: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "xirang_backup_asset_sqlite_file_bytes",
			Help: "On-disk bytes of the SQLite main database file; 0 on other backends.",
		}),
		gcDeletedGenerations: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "xirang_backup_asset_catalog_gc_deleted_generations_total",
			Help: "Total non-protected Catalog generations reclaimed with their Search payload.",
		}),
		gcSkippedRestricted: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "xirang_backup_asset_catalog_gc_skipped_restricted_total",
			Help: "Total Catalog generations retained because RESTRICT child rows still reference them.",
		}),
	}
	for _, collector := range []prometheus.Collector{
		metrics.builds, metrics.buildDuration, metrics.scans, metrics.activeBuilds, metrics.reconciledAbandoned,
		metrics.generations, metrics.generationsMaxPerPoint, metrics.entries, metrics.sqliteFileBytes,
		metrics.gcDeletedGenerations, metrics.gcSkippedRestricted,
	} {
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

// ObserveStorage publishes the scan-end storage aggregate. The generation gauge
// only ever uses the frozen generation-state label set; a state value outside
// that set is ignored rather than turned into a new series.
func (metrics *PrometheusMetrics) ObserveStorage(observation StorageObservation) {
	if metrics == nil {
		return
	}
	for _, state := range metricGenerationStates() {
		count := observation.GenerationsByState[state]
		if count < 0 {
			count = 0
		}
		metrics.generations.WithLabelValues(state).Set(float64(count))
	}
	metrics.generationsMaxPerPoint.Set(nonNegativeMetricValue(observation.MaxGenerationsPerPoint))
	metrics.entries.Set(nonNegativeMetricValue(observation.EntryCount))
	metrics.sqliteFileBytes.Set(nonNegativeMetricValue(observation.SQLiteFileBytes))
}

func nonNegativeMetricValue(value int64) float64 {
	if value < 0 {
		return 0
	}
	return float64(value)
}

// AddGCDeletedGenerations counts Catalog generations reclaimed with their Search
// payload. It carries no point, path, or locator label.
func (metrics *PrometheusMetrics) AddGCDeletedGenerations(count int) {
	if metrics != nil && count > 0 {
		metrics.gcDeletedGenerations.Add(float64(count))
	}
}

// AddGCSkippedRestricted counts Catalog generations retained because a RESTRICT
// child row still references them. It carries no point, path, or locator label.
func (metrics *PrometheusMetrics) AddGCSkippedRestricted(count int) {
	if metrics != nil && count > 0 {
		metrics.gcSkippedRestricted.Add(float64(count))
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
