package retention

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"

	"xirang/backend/internal/backupasset"
)

type MetricOutcome string

const (
	MetricSelected MetricOutcome = "selected"
	MetricRetired  MetricOutcome = "retired"
	MetricExpired  MetricOutcome = "expired"
	MetricBlocked  MetricOutcome = "blocked"
	MetricRetried  MetricOutcome = "retried"
)

type Metrics interface {
	Observe(MetricOutcome)
}

type NoopMetrics struct{}

func (NoopMetrics) Observe(MetricOutcome) {}

type PrometheusMetrics struct {
	outcomes *prometheus.CounterVec
}

func NewPrometheusMetrics(registerer prometheus.Registerer) (*PrometheusMetrics, error) {
	if registerer == nil {
		return nil, fmt.Errorf("%w: Prometheus registerer is unavailable", backupasset.ErrInvalidState)
	}
	metrics := &PrometheusMetrics{
		outcomes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "xirang_backup_asset_retention_outcomes_total",
			Help: "Total backup asset retention outcomes.",
		}, []string{"outcome"}),
	}
	if err := registerer.Register(metrics.outcomes); err != nil {
		return nil, fmt.Errorf("register backup asset retention metric: %w", err)
	}
	return metrics, nil
}

func (metrics *PrometheusMetrics) Observe(outcome MetricOutcome) {
	if metrics == nil || metrics.outcomes == nil {
		return
	}
	metrics.outcomes.WithLabelValues(metricOutcomeLabel(outcome)).Inc()
}

func metricOutcomeLabel(outcome MetricOutcome) string {
	switch outcome {
	case MetricSelected, MetricRetired, MetricExpired, MetricBlocked, MetricRetried:
		return string(outcome)
	default:
		return "unknown"
	}
}
