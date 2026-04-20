package slo

import "time"

// Status classifies an SLO's current state.
type Status string

const (
	StatusHealthy      Status = "healthy"
	StatusWarning      Status = "warning"
	StatusBreached     Status = "breached"
	StatusInsufficient Status = "insufficient_data"
)

// Compliance is the result of a single SLO evaluation.
type Compliance struct {
	SLOID                   uint      `json:"slo_id"`
	Name                    string    `json:"name"`
	MetricType              string    `json:"metric_type"`
	WindowStart             time.Time `json:"window_start"`
	WindowEnd               time.Time `json:"window_end"`
	Threshold               float64   `json:"threshold"`
	Observed                float64   `json:"observed"`
	SampleCount             int       `json:"sample_count"`
	ErrorBudgetRemainingPct float64   `json:"error_budget_remaining_pct"`
	BurnRate1h              float64   `json:"burn_rate_1h"`
	Status                  Status    `json:"status"`
}

// insufficientSampleThreshold is the minimum total sample count needed
// to consider a compliance value meaningful. Below this, Compute returns
// StatusInsufficient and evaluator skips alerting.
const insufficientSampleThreshold = 100
