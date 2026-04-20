package slo

import (
	"strconv"

	"xirang/backend/internal/model"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	sloObserved = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "xirang_slo_observed",
		Help: "Current SLO observed ratio (0–1)",
	}, []string{"slo_id", "metric_type"})

	sloBudgetRemaining = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "xirang_slo_budget_remaining_ratio",
		Help: "SLO error budget remaining ratio (0–1)",
	}, []string{"slo_id"})

	sloBurnRate = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "xirang_slo_burn_rate_1h",
		Help: "SLO 1-hour burn rate (0 = healthy, 1 = at pace, >2 = alerting)",
	}, []string{"slo_id"})
)

func recordPromMetrics(def *model.SLODefinition, c *Compliance) {
	idStr := strconv.FormatUint(uint64(def.ID), 10)
	sloObserved.WithLabelValues(idStr, def.MetricType).Set(c.Observed)
	sloBudgetRemaining.WithLabelValues(idStr).Set(c.ErrorBudgetRemainingPct / 100)
	sloBurnRate.WithLabelValues(idStr).Set(c.BurnRate1h)
}

// ClearPromMetricsForSLO removes all Prometheus series for the given SLO id.
// Called on delete to prevent unbounded cardinality from stale label sets.
func ClearPromMetricsForSLO(id uint) {
	idStr := strconv.FormatUint(uint64(id), 10)
	sloObserved.DeletePartialMatch(prometheus.Labels{"slo_id": idStr})
	sloBudgetRemaining.DeletePartialMatch(prometheus.Labels{"slo_id": idStr})
	sloBurnRate.DeletePartialMatch(prometheus.Labels{"slo_id": idStr})
}
