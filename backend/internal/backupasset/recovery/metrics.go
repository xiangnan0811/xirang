package recovery

import (
	"context"
	"errors"
	"fmt"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"github.com/prometheus/client_golang/prometheus"
	"gorm.io/gorm"
)

type MetricOutcome string

const (
	MetricOutcomeSuccess MetricOutcome = "success"
	MetricOutcomeBlocked MetricOutcome = "blocked"
	MetricOutcomeFailure MetricOutcome = "failure"
)

type Metrics interface {
	ObserveState(backupasset.ProviderKind, JobState)
	ObserveOutcome(backupasset.ProviderKind, JobState, MetricOutcome)
	ObserveCategory(AuthorizationReceiptCategory, MetricOutcome)
}

type NoopMetrics struct{}

func (NoopMetrics) ObserveState(backupasset.ProviderKind, JobState)                  {}
func (NoopMetrics) ObserveOutcome(backupasset.ProviderKind, JobState, MetricOutcome) {}
func (NoopMetrics) ObserveCategory(AuthorizationReceiptCategory, MetricOutcome)      {}

type PrometheusMetrics struct {
	states     *prometheus.CounterVec
	outcomes   *prometheus.CounterVec
	categories *prometheus.CounterVec
}

func NewPrometheusMetrics(registerer prometheus.Registerer) (*PrometheusMetrics, error) {
	if registerer == nil {
		return nil, fmt.Errorf("%w: Recovery Prometheus registerer unavailable", backupasset.ErrInvalidState)
	}
	metrics := &PrometheusMetrics{
		states: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "xirang_backup_asset_recovery_states_total",
			Help: "Total Recovery job state observations.",
		}, []string{"provider", "state"}),
		outcomes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "xirang_backup_asset_recovery_outcomes_total",
			Help: "Total terminal Recovery outcomes.",
		}, []string{"provider", "state", "outcome"}),
		categories: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "xirang_backup_asset_recovery_categories_total",
			Help: "Total Recovery authorization category outcomes.",
		}, []string{"category", "outcome"}),
	}
	for _, collector := range []prometheus.Collector{metrics.states, metrics.outcomes, metrics.categories} {
		if err := registerer.Register(collector); err != nil {
			return nil, fmt.Errorf("register Recovery metric: %w", err)
		}
	}
	return metrics, nil
}

func (metrics *PrometheusMetrics) ObserveState(provider backupasset.ProviderKind, state JobState) {
	if metrics != nil {
		metrics.states.WithLabelValues(recoveryMetricProvider(provider), recoveryMetricState(state)).Inc()
	}
}

func (metrics *PrometheusMetrics) ObserveOutcome(
	provider backupasset.ProviderKind,
	state JobState,
	outcome MetricOutcome,
) {
	if metrics != nil {
		metrics.outcomes.WithLabelValues(
			recoveryMetricProvider(provider), recoveryMetricState(state), recoveryMetricOutcome(outcome),
		).Inc()
	}
}

func (metrics *PrometheusMetrics) ObserveCategory(category AuthorizationReceiptCategory, outcome MetricOutcome) {
	if metrics != nil {
		metrics.categories.WithLabelValues(recoveryMetricCategory(category), recoveryMetricOutcome(outcome)).Inc()
	}
}

func recoveryMetricProvider(provider backupasset.ProviderKind) string {
	switch provider {
	case backupasset.ProviderRestic, backupasset.ProviderRsync, backupasset.ProviderRclone,
		backupasset.ProviderVerifiedImport:
		return string(provider)
	default:
		return "unknown"
	}
}

func recoveryMetricState(state JobState) string {
	if !state.Valid() {
		return "unknown"
	}
	return string(state)
}

func recoveryMetricOutcome(outcome MetricOutcome) string {
	switch outcome {
	case MetricOutcomeSuccess, MetricOutcomeBlocked, MetricOutcomeFailure:
		return string(outcome)
	default:
		return "unknown"
	}
}

func recoveryMetricCategory(category AuthorizationReceiptCategory) string {
	switch category {
	case AuthorizationReceiptCategorySecurityOverride, AuthorizationReceiptCategoryWrite,
		AuthorizationReceiptCategoryExactMirrorDelete, AuthorizationReceiptCategoryExecute:
		return string(category)
	default:
		return "unknown"
	}
}

func recoveryAuthorizationMetricOutcome(err error) MetricOutcome {
	if err == nil {
		return MetricOutcomeSuccess
	}
	if isAuthorizationPublicError(err) {
		return MetricOutcomeBlocked
	}
	return MetricOutcomeFailure
}

func recoveryTerminalMetricOutcome(state JobState) MetricOutcome {
	switch state {
	case JobStateSucceeded:
		return MetricOutcomeSuccess
	case JobStateCanceled:
		return MetricOutcomeBlocked
	case JobStateDegraded, JobStateFailed, JobStateNeedsAttention:
		return MetricOutcomeFailure
	default:
		return MetricOutcome("unknown")
	}
}

func recoveryJobProvider(ctx context.Context, db *gorm.DB, jobID string) (backupasset.ProviderKind, error) {
	if ctx == nil || db == nil || !validOpaqueID(jobID) {
		return "", ErrAuthorizationUnavailable
	}
	var repository struct {
		ProviderKind string `gorm:"column:provider_kind"`
	}
	loaded := db.WithContext(ctx).Table((model.BackupRepository{}).TableName()).
		Select("backup_repositories.provider_kind").
		Joins("JOIN backup_asset_recovery_plans ON backup_asset_recovery_plans.repository_id = backup_repositories.id").
		Joins("JOIN backup_asset_recovery_jobs ON backup_asset_recovery_jobs.plan_id = backup_asset_recovery_plans.id").
		Where("backup_asset_recovery_jobs.id = ?", jobID).Limit(1).Find(&repository)
	provider := backupasset.ProviderKind(repository.ProviderKind)
	if loaded.Error != nil {
		return "", loaded.Error
	}
	if loaded.RowsAffected != 1 || !validRecoveryProvider(provider) {
		return "", errors.New("recovery metric provider unavailable")
	}
	return provider, nil
}

func (coordinator *WorkerCoordinator) observeJobState(ctx context.Context, jobID string, state JobState) {
	if coordinator == nil || coordinator.metrics == nil {
		return
	}
	provider, _ := recoveryJobProvider(recoveryMetricContext(ctx), coordinator.db, jobID)
	coordinator.metrics.ObserveState(provider, state)
}

func (coordinator *WorkerCoordinator) observeJobOutcome(ctx context.Context, jobID string, state JobState) {
	if coordinator == nil || coordinator.metrics == nil {
		return
	}
	provider, _ := recoveryJobProvider(recoveryMetricContext(ctx), coordinator.db, jobID)
	coordinator.metrics.ObserveOutcome(provider, state, recoveryTerminalMetricOutcome(state))
}

func recoveryMetricContext(ctx context.Context) context.Context {
	if ctx == nil || ctx.Err() != nil {
		return context.Background()
	}
	return context.WithoutCancel(ctx)
}

var _ Metrics = (*PrometheusMetrics)(nil)
