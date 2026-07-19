package content

import (
	"fmt"
	"time"

	"xirang/backend/internal/backupasset"

	"github.com/prometheus/client_golang/prometheus"
)

type MetricOutcome string

const (
	MetricOutcomeSuccess MetricOutcome = "success"
	MetricOutcomeBlocked MetricOutcome = "blocked"
	MetricOutcomeFailure MetricOutcome = "failure"
)

type MetricByteKind string

const (
	MetricBytesReserved MetricByteKind = "reserved"
	MetricBytesCharged  MetricByteKind = "charged"
)

type MetricReason string

const (
	MetricReasonLeaseLost         MetricReason = "lease_lost"
	MetricReasonSourceChanged     MetricReason = "source_changed"
	MetricReasonClientCanceled    MetricReason = "client_canceled"
	MetricReasonSessionRevoked    MetricReason = "session_revoked"
	MetricReasonPermissionChanged MetricReason = "permission_changed"
	MetricReasonFeatureDisabled   MetricReason = "feature_disabled"
	MetricReasonShutdown          MetricReason = "shutdown"
	MetricReasonReconciledCrash   MetricReason = "reconciled_crash"
	MetricReasonInternal          MetricReason = "internal_failure"
)

type MetricCacheOutcome string

const (
	MetricCacheHit      MetricCacheOutcome = "hit"
	MetricCacheMiss     MetricCacheOutcome = "miss"
	MetricCacheFull     MetricCacheOutcome = "full"
	MetricCacheTamper   MetricCacheOutcome = "tamper"
	MetricCacheOrphan   MetricCacheOutcome = "orphan"
	MetricCacheKeyLoss  MetricCacheOutcome = "key_loss"
	MetricCacheDisabled MetricCacheOutcome = "disabled"
	MetricCacheFailure  MetricCacheOutcome = "failure"
)

type Metrics interface {
	ObserveTicket(DeliveryAction, MetricOutcome)
	ObserveRead(DeliveryAction, MetricOutcome)
	SetInFlight(backupasset.ProviderKind, int)
	AddBytes(MetricByteKind, int64)
	ObserveReason(MetricReason)
	ObserveCache(MetricCacheOutcome)
	SetAuditBacklog(int)
	ObserveAuditRetry()
	SetReconciliationAge(time.Duration)
}

type NoopMetrics struct{}

func (NoopMetrics) ObserveTicket(DeliveryAction, MetricOutcome) {}
func (NoopMetrics) ObserveRead(DeliveryAction, MetricOutcome)   {}
func (NoopMetrics) SetInFlight(backupasset.ProviderKind, int)   {}
func (NoopMetrics) AddBytes(MetricByteKind, int64)              {}
func (NoopMetrics) ObserveReason(MetricReason)                  {}
func (NoopMetrics) ObserveCache(MetricCacheOutcome)             {}
func (NoopMetrics) SetAuditBacklog(int)                         {}
func (NoopMetrics) ObserveAuditRetry()                          {}
func (NoopMetrics) SetReconciliationAge(time.Duration)          {}

type PrometheusMetrics struct {
	tickets           *prometheus.CounterVec
	reads             *prometheus.CounterVec
	inFlight          *prometheus.GaugeVec
	bytes             *prometheus.CounterVec
	reasons           *prometheus.CounterVec
	cache             *prometheus.CounterVec
	auditBacklog      prometheus.Gauge
	auditRetries      prometheus.Counter
	reconciliationAge prometheus.Gauge
}

func NewPrometheusMetrics(registerer prometheus.Registerer) (*PrometheusMetrics, error) {
	if registerer == nil {
		return nil, ErrInvalidBrokerRequest
	}
	metrics := &PrometheusMetrics{
		tickets: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "xirang_backup_asset_content_tickets_total",
			Help: "Total backup asset content ticket outcomes.",
		}, []string{"action", "outcome"}),
		reads: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "xirang_backup_asset_content_reads_total",
			Help: "Total backup asset content read outcomes.",
		}, []string{"action", "outcome"}),
		inFlight: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "xirang_backup_asset_content_in_flight",
			Help: "Current backup asset content reads by closed Provider kind.",
		}, []string{"provider"}),
		bytes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "xirang_backup_asset_content_bytes_total",
			Help: "Total reserved or conservatively charged content bytes.",
		}, []string{"kind"}),
		reasons: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "xirang_backup_asset_content_reasons_total",
			Help: "Total terminal content security and lifecycle reasons.",
		}, []string{"reason"}),
		cache: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "xirang_backup_asset_content_cache_total",
			Help: "Total authenticated content cache outcomes.",
		}, []string{"outcome"}),
		auditBacklog: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "xirang_backup_asset_content_audit_backlog",
			Help: "Current pending aggregate content audit grants.",
		}),
		auditRetries: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "xirang_backup_asset_content_audit_retries_total",
			Help: "Total aggregate content audit retries.",
		}),
		reconciliationAge: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "xirang_backup_asset_content_reconciliation_age_seconds",
			Help: "Age of the oldest content state awaiting reconciliation.",
		}),
	}
	collectors := []prometheus.Collector{
		metrics.tickets, metrics.reads, metrics.inFlight, metrics.bytes, metrics.reasons,
		metrics.cache, metrics.auditBacklog, metrics.auditRetries, metrics.reconciliationAge,
	}
	for _, collector := range collectors {
		if err := registerer.Register(collector); err != nil {
			return nil, fmt.Errorf("register backup asset content metric: %w", err)
		}
	}
	return metrics, nil
}

func (metrics *PrometheusMetrics) ObserveTicket(action DeliveryAction, outcome MetricOutcome) {
	if metrics != nil {
		metrics.tickets.WithLabelValues(metricAction(action), metricOutcome(outcome)).Inc()
	}
}

func (metrics *PrometheusMetrics) ObserveRead(action DeliveryAction, outcome MetricOutcome) {
	if metrics != nil {
		metrics.reads.WithLabelValues(metricAction(action), metricOutcome(outcome)).Inc()
	}
}

func (metrics *PrometheusMetrics) SetInFlight(provider backupasset.ProviderKind, count int) {
	if metrics == nil {
		return
	}
	if count < 0 {
		count = 0
	}
	metrics.inFlight.WithLabelValues(metricProvider(provider)).Set(float64(count))
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

func (metrics *PrometheusMetrics) ObserveReason(reason MetricReason) {
	if metrics != nil {
		metrics.reasons.WithLabelValues(metricReason(reason)).Inc()
	}
}

func (metrics *PrometheusMetrics) ObserveCache(outcome MetricCacheOutcome) {
	if metrics != nil {
		metrics.cache.WithLabelValues(metricCacheOutcome(outcome)).Inc()
	}
}

func (metrics *PrometheusMetrics) SetAuditBacklog(count int) {
	if metrics == nil {
		return
	}
	if count < 0 {
		count = 0
	}
	metrics.auditBacklog.Set(float64(count))
}

func (metrics *PrometheusMetrics) ObserveAuditRetry() {
	if metrics != nil {
		metrics.auditRetries.Inc()
	}
}

func (metrics *PrometheusMetrics) SetReconciliationAge(age time.Duration) {
	if metrics == nil {
		return
	}
	if age < 0 {
		age = 0
	}
	metrics.reconciliationAge.Set(age.Seconds())
}

func metricAction(action DeliveryAction) string {
	if action == DeliveryPreview || action == DeliveryDownload {
		return string(action)
	}
	return "unknown"
}

func metricOutcome(outcome MetricOutcome) string {
	if outcome == MetricOutcomeSuccess || outcome == MetricOutcomeBlocked || outcome == MetricOutcomeFailure {
		return string(outcome)
	}
	return "unknown"
}

func metricProvider(provider backupasset.ProviderKind) string {
	switch provider {
	case backupasset.ProviderRestic, backupasset.ProviderRsync, backupasset.ProviderRclone:
		return string(provider)
	default:
		return "unknown"
	}
}

func metricByteKind(kind MetricByteKind) string {
	if kind == MetricBytesReserved || kind == MetricBytesCharged {
		return string(kind)
	}
	return "unknown"
}

func metricReason(reason MetricReason) string {
	switch reason {
	case MetricReasonLeaseLost, MetricReasonSourceChanged, MetricReasonClientCanceled,
		MetricReasonSessionRevoked, MetricReasonPermissionChanged, MetricReasonFeatureDisabled,
		MetricReasonShutdown, MetricReasonReconciledCrash, MetricReasonInternal:
		return string(reason)
	default:
		return "unknown"
	}
}

func metricCacheOutcome(outcome MetricCacheOutcome) string {
	switch outcome {
	case MetricCacheHit, MetricCacheMiss, MetricCacheFull, MetricCacheTamper,
		MetricCacheOrphan, MetricCacheKeyLoss, MetricCacheDisabled, MetricCacheFailure:
		return string(outcome)
	default:
		return "unknown"
	}
}

var _ Metrics = (*PrometheusMetrics)(nil)
var _ Metrics = NoopMetrics{}
