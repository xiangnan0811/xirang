package export

import (
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type MetricErrorCategory string

const (
	MetricErrorNone             MetricErrorCategory = "none"
	MetricErrorSourceChanged    MetricErrorCategory = "source_changed"
	MetricErrorLinkMetadata     MetricErrorCategory = "link_metadata_unavailable"
	MetricErrorSpecialFile      MetricErrorCategory = "special_file_skipped"
	MetricErrorArtifactMissing  MetricErrorCategory = "artifact_missing"
	MetricErrorArtifactTampered MetricErrorCategory = "artifact_tampered"
	MetricErrorKeyUnavailable   MetricErrorCategory = "key_unavailable"
	MetricErrorQuotaExceeded    MetricErrorCategory = "quota_exceeded"
	MetricErrorDeadline         MetricErrorCategory = "deadline"
	MetricErrorCanceled         MetricErrorCategory = "canceled"
	MetricErrorInternal         MetricErrorCategory = "internal_failure"
)

type MetricByteKind string

const (
	MetricBytesLogical    MetricByteKind = "logical"
	MetricBytesProvider   MetricByteKind = "provider"
	MetricBytesCiphertext MetricByteKind = "ciphertext"
)

type MetricEvent string

const (
	MetricEventLeaseLoss       MetricEvent = "lease_loss"
	MetricEventTakeover        MetricEvent = "takeover"
	MetricEventQuotaSaturation MetricEvent = "quota_saturation"
	MetricEventTicketIssue     MetricEvent = "ticket_issue"
	MetricEventTicketReject    MetricEvent = "ticket_reject"
	MetricEventRange           MetricEvent = "range"
	MetricEventDecryptTamper   MetricEvent = "decrypt_tamper"
	MetricEventKeyLoss         MetricEvent = "key_loss"
	MetricEventGCFailure       MetricEvent = "gc_failure"
	MetricEventPurgeFailure    MetricEvent = "purge_failure"
	MetricEventAuditRetry      MetricEvent = "audit_retry"
)

type Metrics interface {
	SetQueue(ExecutionState, int64, time.Duration)
	ObserveJob(ArchiveFormat, ExecutionState, ResultKind, MetricErrorCategory, time.Duration)
	AddBytes(MetricByteKind, int64)
	ObserveEvent(MetricEvent)
}

type NoopMetrics struct{}

func (NoopMetrics) SetQueue(ExecutionState, int64, time.Duration) {}
func (NoopMetrics) ObserveJob(ArchiveFormat, ExecutionState, ResultKind, MetricErrorCategory, time.Duration) {
}
func (NoopMetrics) AddBytes(MetricByteKind, int64) {}
func (NoopMetrics) ObserveEvent(MetricEvent)       {}

type PrometheusMetrics struct {
	queue       *prometheus.GaugeVec
	queueAge    *prometheus.GaugeVec
	jobs        *prometheus.CounterVec
	jobDuration *prometheus.HistogramVec
	bytes       *prometheus.CounterVec
	events      *prometheus.CounterVec
}

func NewPrometheusMetrics(registerer prometheus.Registerer) (*PrometheusMetrics, error) {
	if registerer == nil {
		return nil, fmt.Errorf("%w: Export Prometheus registerer unavailable", ErrUnavailable)
	}
	metrics := &PrometheusMetrics{
		queue: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "xirang_backup_asset_export_queue",
			Help: "Current backup asset Export jobs by closed execution state.",
		}, []string{"state"}),
		queueAge: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "xirang_backup_asset_export_queue_oldest_age_seconds",
			Help: "Age of the oldest backup asset Export job by closed execution state.",
		}, []string{"state"}),
		jobs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "xirang_backup_asset_export_jobs_total",
			Help: "Total backup asset Export outcomes by closed format, state, result, and error category.",
		}, []string{"format", "state", "result", "error_category"}),
		jobDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "xirang_backup_asset_export_job_duration_seconds",
			Help: "Backup asset Export job duration by closed format and state.", Buckets: prometheus.DefBuckets,
		}, []string{"format", "state"}),
		bytes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "xirang_backup_asset_export_bytes_total",
			Help: "Total backup asset Export bytes by closed accounting kind.",
		}, []string{"kind"}),
		events: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "xirang_backup_asset_export_events_total",
			Help: "Total backup asset Export security and lifecycle events by closed category.",
		}, []string{"event"}),
	}
	for _, collector := range []prometheus.Collector{
		metrics.queue, metrics.queueAge, metrics.jobs, metrics.jobDuration, metrics.bytes, metrics.events,
	} {
		if err := registerer.Register(collector); err != nil {
			return nil, fmt.Errorf("register backup asset Export metric: %w", err)
		}
	}
	return metrics, nil
}

func (metrics *PrometheusMetrics) SetQueue(state ExecutionState, count int64, oldestAge time.Duration) {
	if metrics == nil {
		return
	}
	if count < 0 {
		count = 0
	}
	if oldestAge < 0 {
		oldestAge = 0
	}
	label := metricExecutionState(state)
	metrics.queue.WithLabelValues(label).Set(float64(count))
	metrics.queueAge.WithLabelValues(label).Set(oldestAge.Seconds())
}

func (metrics *PrometheusMetrics) ObserveJob(
	format ArchiveFormat,
	state ExecutionState,
	result ResultKind,
	category MetricErrorCategory,
	duration time.Duration,
) {
	if metrics == nil {
		return
	}
	if duration < 0 {
		duration = 0
	}
	formatLabel, stateLabel := metricArchiveFormat(format), metricExecutionState(state)
	metrics.jobs.WithLabelValues(formatLabel, stateLabel, metricResultKind(result), metricError(category)).Inc()
	metrics.jobDuration.WithLabelValues(formatLabel, stateLabel).Observe(duration.Seconds())
}

func (metrics *PrometheusMetrics) AddBytes(kind MetricByteKind, count int64) {
	if metrics == nil {
		return
	}
	if count < 0 {
		count = 0
	}
	metrics.bytes.WithLabelValues(metricByteKind(kind)).Add(float64(count))
}

func (metrics *PrometheusMetrics) ObserveEvent(event MetricEvent) {
	if metrics != nil {
		metrics.events.WithLabelValues(metricEvent(event)).Inc()
	}
}

func metricArchiveFormat(format ArchiveFormat) string {
	if format == ArchiveZIP || format == ArchiveTAR {
		return string(format)
	}
	return "unknown"
}

func metricExecutionState(state ExecutionState) string {
	if validExecutionStates[state] {
		return string(state)
	}
	return "unknown"
}

func metricResultKind(result ResultKind) string {
	if result == ResultComplete || result == ResultPartial {
		return string(result)
	}
	return "unknown"
}

func metricError(category MetricErrorCategory) string {
	switch category {
	case MetricErrorNone, MetricErrorSourceChanged, MetricErrorLinkMetadata, MetricErrorSpecialFile,
		MetricErrorArtifactMissing, MetricErrorArtifactTampered, MetricErrorKeyUnavailable,
		MetricErrorQuotaExceeded, MetricErrorDeadline, MetricErrorCanceled, MetricErrorInternal:
		return string(category)
	default:
		return "unknown"
	}
}

func metricByteKind(kind MetricByteKind) string {
	if kind == MetricBytesLogical || kind == MetricBytesProvider || kind == MetricBytesCiphertext {
		return string(kind)
	}
	return "unknown"
}

func metricEvent(event MetricEvent) string {
	switch event {
	case MetricEventLeaseLoss, MetricEventTakeover, MetricEventQuotaSaturation, MetricEventTicketIssue,
		MetricEventTicketReject, MetricEventRange, MetricEventDecryptTamper, MetricEventKeyLoss,
		MetricEventGCFailure, MetricEventPurgeFailure, MetricEventAuditRetry:
		return string(event)
	default:
		return "unknown"
	}
}

var _ Metrics = (*PrometheusMetrics)(nil)
var _ Metrics = NoopMetrics{}
