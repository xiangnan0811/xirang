package slo

// burnRate returns the current 1-hour error budget consumption rate relative to
// the target pace. A burn rate of 1.0 means "exactly at target pace"; 2.0 means
// "twice as fast as target"; 0 means "no errors in this window" or "threshold
// is infeasible (>=1.0)".
func burnRate(observed1h, threshold float64) float64 {
	if observed1h >= 1.0 {
		return 0
	}
	if threshold >= 1.0 {
		return 0
	}
	errorBudgetRate := 1.0 - threshold
	observedErrorRate := 1.0 - observed1h
	return observedErrorRate / errorBudgetRate
}
