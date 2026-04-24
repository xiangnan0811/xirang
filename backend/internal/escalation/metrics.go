package escalation

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Prometheus metrics for the escalation subsystem. Without these operators
// have no way to tell "did the engine fire anything last night" or "how long
// is the tick taking at scale" — both blind spots when an on-call goes wrong.

var (
	FiresTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "xirang_escalation_fires_total",
		Help: "Count of escalation level fires, labeled by severity_after and whether the level was silenced at fire time.",
	}, []string{"severity", "silenced"})

	TickDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "xirang_escalation_tick_duration_seconds",
		Help:    "Wall-clock latency of a single Engine.Tick pass over all open alerts.",
		Buckets: prometheus.DefBuckets,
	})

	OpenAlertsScanned = promauto.NewCounter(prometheus.CounterOpts{
		Name: "xirang_escalation_open_alerts_scanned_total",
		Help: "Running total of open alerts evaluated by Engine.Tick across all batches.",
	})
)
