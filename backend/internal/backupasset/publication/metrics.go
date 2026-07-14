package publication

import (
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"xirang/backend/internal/backupasset"
)

type PrometheusMetrics struct {
	attempts         *prometheus.CounterVec
	outcomes         *prometheus.CounterVec
	backlogCount     *prometheus.GaugeVec
	backlogOldestAge *prometheus.GaugeVec
	reconcileMatches *prometheus.CounterVec
	fenceLost        *prometheus.CounterVec
	manifestDuration *prometheus.HistogramVec
	manifestEntries  *prometheus.HistogramVec
	manifestBytes    *prometheus.HistogramVec
	legacyBlocked    *prometheus.CounterVec
	auditFailures    *prometheus.CounterVec
}

func NewPrometheusMetrics(registerer prometheus.Registerer) (*PrometheusMetrics, error) {
	if registerer == nil {
		return nil, fmt.Errorf("%w: Prometheus registerer is unavailable", backupasset.ErrInvalidState)
	}
	metrics := &PrometheusMetrics{
		attempts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "xirang_backup_asset_publication_attempts_total",
			Help: "Total backup asset publication attempts.",
		}, []string{"provider", "stage"}),
		outcomes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "xirang_backup_asset_publication_outcomes_total",
			Help: "Total terminal backup asset publication outcomes.",
		}, []string{"provider", "stage", "code"}),
		backlogCount: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "xirang_backup_asset_publication_backlog_count",
			Help: "Current count of reconcilable publication points.",
		}, []string{"state"}),
		backlogOldestAge: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "xirang_backup_asset_publication_backlog_oldest_age_seconds",
			Help: "Age in seconds of the oldest reconcilable publication point.",
		}, []string{"state"}),
		reconcileMatches: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "xirang_backup_asset_publication_reconcile_matches_total",
			Help: "Total exact publication reconciliation match classifications.",
		}, []string{"class"}),
		fenceLost: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "xirang_backup_asset_publication_fence_lost_total",
			Help: "Total publication lease fence losses.",
		}, []string{"stage"}),
		manifestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "xirang_backup_asset_publication_manifest_duration_seconds",
			Help:    "Duration of an exact publication manifest build.",
			Buckets: prometheus.DefBuckets,
		}, []string{"completeness", "limit_class"}),
		manifestEntries: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "xirang_backup_asset_publication_manifest_entries",
			Help:    "Entries observed while building an exact publication manifest.",
			Buckets: prometheus.ExponentialBuckets(1, 10, 9),
		}, []string{"completeness"}),
		manifestBytes: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "xirang_backup_asset_publication_manifest_bytes",
			Help:    "Logical bytes observed while building an exact publication manifest.",
			Buckets: prometheus.ExponentialBuckets(1024, 8, 10),
		}, []string{"completeness"}),
		legacyBlocked: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "xirang_backup_asset_legacy_operation_blocked_total",
			Help: "Total legacy Restic operations blocked by exact lineage safety.",
		}, []string{"operation"}),
		auditFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "xirang_backup_asset_publication_audit_failures_total",
			Help: "Total publication audit write failures.",
		}, []string{"stage"}),
	}
	collectors := []prometheus.Collector{
		metrics.attempts,
		metrics.outcomes,
		metrics.backlogCount,
		metrics.backlogOldestAge,
		metrics.reconcileMatches,
		metrics.fenceLost,
		metrics.manifestDuration,
		metrics.manifestEntries,
		metrics.manifestBytes,
		metrics.legacyBlocked,
		metrics.auditFailures,
	}
	for _, collector := range collectors {
		if err := registerer.Register(collector); err != nil {
			return nil, fmt.Errorf("register backup asset publication metric: %w", err)
		}
	}
	return metrics, nil
}

func (metrics *PrometheusMetrics) ObserveAttempt(providerKind backupasset.ProviderKind, stage PublicationStage) {
	if metrics == nil {
		return
	}
	metrics.attempts.WithLabelValues(metricProvider(providerKind), metricStage(stage)).Inc()
}

func (metrics *PrometheusMetrics) ObserveOutcome(providerKind backupasset.ProviderKind, stage PublicationStage, outcome backupasset.PublicationOutcomeCode) {
	if metrics == nil {
		return
	}
	metrics.outcomes.WithLabelValues(metricProvider(providerKind), metricStage(stage), metricOutcome(outcome)).Inc()
}

func (metrics *PrometheusMetrics) SetBacklog(state backupasset.RecoveryPointState, count int, oldestAge time.Duration) {
	if metrics == nil {
		return
	}
	if count < 0 {
		count = 0
	}
	if oldestAge < 0 {
		oldestAge = 0
	}
	label := metricRecoveryPointState(state)
	metrics.backlogCount.WithLabelValues(label).Set(float64(count))
	metrics.backlogOldestAge.WithLabelValues(label).Set(oldestAge.Seconds())
}

func (metrics *PrometheusMetrics) ObserveReconcileMatch(class ReconcileMatchClass) {
	if metrics == nil {
		return
	}
	metrics.reconcileMatches.WithLabelValues(metricReconcileMatch(class)).Inc()
}

func (metrics *PrometheusMetrics) ObserveFenceLoss(stage PublicationStage) {
	if metrics == nil {
		return
	}
	metrics.fenceLost.WithLabelValues(metricStage(stage)).Inc()
}

func (metrics *PrometheusMetrics) ObserveManifest(duration time.Duration, entries, bytes int64, completeness backupasset.ManifestCompleteness, limit ManifestLimitClass) {
	if metrics == nil {
		return
	}
	if duration < 0 {
		duration = 0
	}
	if entries < 0 {
		entries = 0
	}
	if bytes < 0 {
		bytes = 0
	}
	completenessLabel := metricManifestCompleteness(completeness)
	metrics.manifestDuration.WithLabelValues(completenessLabel, metricManifestLimit(limit)).Observe(duration.Seconds())
	metrics.manifestEntries.WithLabelValues(completenessLabel).Observe(float64(entries))
	metrics.manifestBytes.WithLabelValues(completenessLabel).Observe(float64(bytes))
}

func (metrics *PrometheusMetrics) ObserveLegacyBlocked(operation ResticOperation) {
	if metrics == nil {
		return
	}
	metrics.legacyBlocked.WithLabelValues(metricOperation(operation)).Inc()
}

func (metrics *PrometheusMetrics) ObserveAuditFailure(stage PublicationStage) {
	if metrics == nil {
		return
	}
	metrics.auditFailures.WithLabelValues(metricStage(stage)).Inc()
}

func metricProvider(value backupasset.ProviderKind) string {
	switch value {
	case backupasset.ProviderRestic, backupasset.ProviderRsync, backupasset.ProviderRclone, backupasset.ProviderCommand, backupasset.ProviderVerifiedImport:
		return string(value)
	default:
		return "unknown"
	}
}

func metricStage(value PublicationStage) string {
	if ValidatePublicationStage(value) != nil {
		return "unknown"
	}
	return string(value)
}

func metricOutcome(value backupasset.PublicationOutcomeCode) string {
	if backupasset.ValidatePublicationOutcomeCode(value) != nil {
		return "unknown"
	}
	return string(value)
}

func metricRecoveryPointState(value backupasset.RecoveryPointState) string {
	switch value {
	case backupasset.RecoveryPointObserved, backupasset.RecoveryPointRetired, backupasset.RecoveryPointPreparing,
		backupasset.RecoveryPointVerifying, backupasset.RecoveryPointCommitted, backupasset.RecoveryPointDegraded,
		backupasset.RecoveryPointExpiring, backupasset.RecoveryPointExpired, backupasset.RecoveryPointFailed,
		backupasset.RecoveryPointPurgeBlocked:
		return string(value)
	default:
		return "unknown"
	}
}

func metricReconcileMatch(value ReconcileMatchClass) string {
	if ValidateReconcileMatchClass(value) != nil {
		return "unknown"
	}
	return string(value)
}

func metricManifestCompleteness(value backupasset.ManifestCompleteness) string {
	switch value {
	case backupasset.ManifestComplete, backupasset.ManifestPartial, backupasset.ManifestUnavailable:
		return string(value)
	default:
		return "unknown"
	}
}

func metricManifestLimit(value ManifestLimitClass) string {
	if ValidateManifestLimitClass(value) != nil {
		return "unknown"
	}
	return string(value)
}

func metricOperation(value ResticOperation) string {
	if ValidateResticOperation(value) != nil {
		return "unknown"
	}
	return string(value)
}

var _ Metrics = (*PrometheusMetrics)(nil)
