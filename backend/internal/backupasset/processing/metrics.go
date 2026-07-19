package processing

import (
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type WorkerTrustClass string

const (
	WorkerTrustActive      WorkerTrustClass = "active"
	WorkerTrustQuarantined WorkerTrustClass = "quarantined"
	WorkerTrustRevoked     WorkerTrustClass = "revoked"
)

type WorkerHealthClass string

const (
	WorkerHealthReady    WorkerHealthClass = "ready"
	WorkerHealthDegraded WorkerHealthClass = "degraded"
	WorkerHealthDraining WorkerHealthClass = "draining"
)

type SlotMetricKind string

const (
	SlotMetricUsed  SlotMetricKind = "used"
	SlotMetricTotal SlotMetricKind = "total"
)

type DerivedMetricKind string

const (
	DerivedMetricLogicalBytes  DerivedMetricKind = "logical_bytes"
	DerivedMetricPhysicalBytes DerivedMetricKind = "physical_bytes"
	DerivedMetricOrphanBytes   DerivedMetricKind = "orphan_bytes"
	DerivedMetricQuotaBytes    DerivedMetricKind = "quota_bytes"
)

type DerivedMetricEvent string

const (
	DerivedEventTamper           DerivedMetricEvent = "tamper"
	DerivedEventRefcountRepaired DerivedMetricEvent = "refcount_repaired"
	DerivedEventRewrapped        DerivedMetricEvent = "rewrapped"
	DerivedEventPurged           DerivedMetricEvent = "purged"
	DerivedEventOrphanRemoved    DerivedMetricEvent = "orphan_removed"
	DerivedEventKeyLoss          DerivedMetricEvent = "key_loss"
	DerivedEventReconcileFailure DerivedMetricEvent = "reconcile_failure"
)

type Metrics interface {
	ObserveJob(PriorityClass, ProcessingState, ProcessingErrorCategory)
	ObserveLeaseLoss()
	ObserveJobDuration(PriorityClass, ProcessingState, time.Duration)
	SetWorkers(WorkerTrustClass, WorkerHealthClass, int64)
	SetSlots(SlotClass, SlotMetricKind, int64)
	SetQueue(PriorityClass, ProcessingState, int64, time.Duration)
	AddSinkBytes(int64)
	SetDerived(DerivedMetricKind, int64)
	ObserveDerived(DerivedMetricEvent)
}

type NoopMetrics struct{}

func (NoopMetrics) ObserveJob(PriorityClass, ProcessingState, ProcessingErrorCategory) {}
func (NoopMetrics) ObserveLeaseLoss()                                                  {}
func (NoopMetrics) ObserveJobDuration(PriorityClass, ProcessingState, time.Duration)   {}
func (NoopMetrics) SetWorkers(WorkerTrustClass, WorkerHealthClass, int64)              {}
func (NoopMetrics) SetSlots(SlotClass, SlotMetricKind, int64)                          {}
func (NoopMetrics) SetQueue(PriorityClass, ProcessingState, int64, time.Duration)      {}
func (NoopMetrics) AddSinkBytes(int64)                                                 {}
func (NoopMetrics) SetDerived(DerivedMetricKind, int64)                                {}
func (NoopMetrics) ObserveDerived(DerivedMetricEvent)                                  {}

type PrometheusMetrics struct {
	jobs          *prometheus.CounterVec
	leaseLoss     prometheus.Counter
	jobDuration   *prometheus.HistogramVec
	workers       *prometheus.GaugeVec
	slots         *prometheus.GaugeVec
	queue         *prometheus.GaugeVec
	queueAge      *prometheus.GaugeVec
	sinkBytes     prometheus.Counter
	derived       *prometheus.GaugeVec
	derivedEvents *prometheus.CounterVec
}

func NewPrometheusMetrics(registerer prometheus.Registerer) (*PrometheusMetrics, error) {
	if registerer == nil {
		return nil, fmt.Errorf("%w: Processing Prometheus registerer unavailable", ErrInvalidContract)
	}
	metrics := &PrometheusMetrics{
		jobs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "xirang_backup_asset_processing_jobs_total",
			Help: "Total backup asset Processing job outcomes by closed priority, state, and error category.",
		}, []string{"priority", "state", "error_category"}),
		leaseLoss: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "xirang_backup_asset_processing_lease_loss_total",
			Help: "Total Processing attempt lease losses.",
		}),
		jobDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "xirang_backup_asset_processing_job_duration_seconds",
			Help: "Processing job duration by closed priority and terminal state.", Buckets: prometheus.DefBuckets,
		}, []string{"priority", "state"}),
		workers: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "xirang_backup_asset_processing_workers",
			Help: "Current Worker identities by closed trust and health class.",
		}, []string{"trust", "health"}),
		slots: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "xirang_backup_asset_processing_slots",
			Help: "Current Processing slots by closed slot class and usage kind.",
		}, []string{"class", "kind"}),
		queue: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "xirang_backup_asset_processing_queue",
			Help: "Current Processing queue by closed priority and state.",
		}, []string{"priority", "state"}),
		queueAge: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "xirang_backup_asset_processing_queue_oldest_age_seconds",
			Help: "Age of the oldest Processing queue item by closed priority and state.",
		}, []string{"priority", "state"}),
		sinkBytes: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "xirang_backup_asset_processing_sink_bytes_total",
			Help: "Total plaintext bytes accepted through bounded Processing Sink sessions.",
		}),
		derived: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "xirang_backup_asset_processing_derived_bytes",
			Help: "Current Derived Store byte totals by closed kind.",
		}, []string{"kind"}),
		derivedEvents: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "xirang_backup_asset_processing_derived_events_total",
			Help: "Total Derived Store security and reconciliation events by closed kind.",
		}, []string{"event"}),
	}
	for _, collector := range []prometheus.Collector{
		metrics.jobs, metrics.leaseLoss, metrics.jobDuration, metrics.workers, metrics.slots,
		metrics.queue, metrics.queueAge, metrics.sinkBytes, metrics.derived, metrics.derivedEvents,
	} {
		if err := registerer.Register(collector); err != nil {
			return nil, fmt.Errorf("register backup asset Processing metric: %w", err)
		}
	}
	return metrics, nil
}

func (metrics *PrometheusMetrics) ObserveJob(priority PriorityClass, state ProcessingState, category ProcessingErrorCategory) {
	if metrics != nil {
		metrics.jobs.WithLabelValues(metricPriority(priority), metricProcessingState(state), metricErrorCategory(category)).Inc()
	}
}

func (metrics *PrometheusMetrics) ObserveLeaseLoss() {
	if metrics != nil {
		metrics.leaseLoss.Inc()
	}
}

func (metrics *PrometheusMetrics) ObserveJobDuration(priority PriorityClass, state ProcessingState, duration time.Duration) {
	if metrics == nil {
		return
	}
	if duration < 0 {
		duration = 0
	}
	metrics.jobDuration.WithLabelValues(metricPriority(priority), metricProcessingState(state)).Observe(duration.Seconds())
}

func (metrics *PrometheusMetrics) SetWorkers(trust WorkerTrustClass, health WorkerHealthClass, count int64) {
	if metrics == nil {
		return
	}
	metrics.workers.WithLabelValues(metricWorkerTrust(trust), metricWorkerHealth(health)).Set(nonnegativeMetric(count))
}

func (metrics *PrometheusMetrics) SetSlots(class SlotClass, kind SlotMetricKind, count int64) {
	if metrics == nil {
		return
	}
	metrics.slots.WithLabelValues(metricSlotClass(class), metricSlotKind(kind)).Set(nonnegativeMetric(count))
}

func (metrics *PrometheusMetrics) SetQueue(priority PriorityClass, state ProcessingState, count int64, oldestAge time.Duration) {
	if metrics == nil {
		return
	}
	if oldestAge < 0 {
		oldestAge = 0
	}
	labels := []string{metricPriority(priority), metricProcessingState(state)}
	metrics.queue.WithLabelValues(labels...).Set(nonnegativeMetric(count))
	metrics.queueAge.WithLabelValues(labels...).Set(oldestAge.Seconds())
}

func (metrics *PrometheusMetrics) AddSinkBytes(count int64) {
	if metrics != nil {
		metrics.sinkBytes.Add(nonnegativeMetric(count))
	}
}

func (metrics *PrometheusMetrics) SetDerived(kind DerivedMetricKind, count int64) {
	if metrics != nil {
		metrics.derived.WithLabelValues(metricDerivedKind(kind)).Set(nonnegativeMetric(count))
	}
}

func (metrics *PrometheusMetrics) ObserveDerived(event DerivedMetricEvent) {
	if metrics != nil {
		metrics.derivedEvents.WithLabelValues(metricDerivedEvent(event)).Inc()
	}
}

func metricPriority(value PriorityClass) string {
	if value == PriorityInteractive || value == PriorityBackground {
		return string(value)
	}
	return "unknown"
}

func metricProcessingState(value ProcessingState) string {
	if value.Valid() {
		return string(value)
	}
	return "unknown"
}

func metricErrorCategory(value ProcessingErrorCategory) string {
	switch value {
	case "":
		return "none"
	case PermanentError, TransientError, ContractSecurityError:
		return string(value)
	default:
		return "unknown"
	}
}

func metricWorkerTrust(value WorkerTrustClass) string {
	switch value {
	case WorkerTrustActive, WorkerTrustQuarantined, WorkerTrustRevoked:
		return string(value)
	default:
		return "unknown"
	}
}

func metricWorkerHealth(value WorkerHealthClass) string {
	switch value {
	case WorkerHealthReady, WorkerHealthDegraded, WorkerHealthDraining:
		return string(value)
	default:
		return "unknown"
	}
}

func metricSlotClass(value SlotClass) string {
	switch value {
	case SlotInteractive, SlotBackground, SlotBackgroundBorrowed:
		return string(value)
	default:
		return "unknown"
	}
}

func metricSlotKind(value SlotMetricKind) string {
	if value == SlotMetricUsed || value == SlotMetricTotal {
		return string(value)
	}
	return "unknown"
}

func metricDerivedKind(value DerivedMetricKind) string {
	switch value {
	case DerivedMetricLogicalBytes, DerivedMetricPhysicalBytes, DerivedMetricOrphanBytes, DerivedMetricQuotaBytes:
		return string(value)
	default:
		return "unknown"
	}
}

func metricDerivedEvent(value DerivedMetricEvent) string {
	switch value {
	case DerivedEventTamper, DerivedEventRefcountRepaired, DerivedEventRewrapped, DerivedEventPurged,
		DerivedEventOrphanRemoved, DerivedEventKeyLoss, DerivedEventReconcileFailure:
		return string(value)
	default:
		return "unknown"
	}
}

func nonnegativeMetric(value int64) float64 {
	if value < 0 {
		return 0
	}
	return float64(value)
}

var _ Metrics = (*PrometheusMetrics)(nil)
var _ Metrics = NoopMetrics{}
