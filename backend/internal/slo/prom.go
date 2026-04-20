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
	}, []string{"slo_id", "name", "metric_type"})

	sloBudgetRemaining = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "xirang_slo_budget_remaining_ratio",
		Help: "SLO error budget remaining ratio (0–1)",
	}, []string{"slo_id", "name"})

	sloBurnRate = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "xirang_slo_burn_rate_1h",
		Help: "SLO 1-hour burn rate (0 = healthy, 1 = at pace, >2 = alerting)",
	}, []string{"slo_id", "name"})
)

func recordPromMetrics(def *model.SLODefinition, c *Compliance) {
	idStr := strconv.FormatUint(uint64(def.ID), 10)
	sloObserved.WithLabelValues(idStr, def.Name, def.MetricType).Set(c.Observed)
	sloBudgetRemaining.WithLabelValues(idStr, def.Name).Set(c.ErrorBudgetRemainingPct / 100)
	sloBurnRate.WithLabelValues(idStr, def.Name).Set(c.BurnRate1h)
}
