package slo

import "testing"

func TestBurnRate_HealthyHour(t *testing.T) {
	// observed1h=1.0 means no errors in past hour → burn rate 0
	if got := burnRate(1.0, 0.99); got != 0 {
		t.Fatalf("expected 0, got %f", got)
	}
}

func TestBurnRate_AtTargetPace(t *testing.T) {
	// threshold=0.99 means allowed error rate = 0.01.
	// If observed1h = 0.99 (matches threshold), burn rate is exactly 1.0.
	if got := burnRate(0.99, 0.99); got < 0.99 || got > 1.01 {
		t.Fatalf("expected ≈ 1.0, got %f", got)
	}
}

func TestBurnRate_FastBurn(t *testing.T) {
	// Threshold 0.99 (1% error budget). Observed 1h = 0.98 = 2% error.
	// Burn rate = 2% / 1% = 2.0 — right at alert boundary.
	if got := burnRate(0.98, 0.99); got < 1.95 || got > 2.05 {
		t.Fatalf("expected ≈ 2.0, got %f", got)
	}
}

func TestBurnRate_InfeasibleThreshold(t *testing.T) {
	// threshold=1.0 means "no errors allowed". Observed 0.99 → error budget is 0,
	// so burn rate is undefined. Return 0 (don't alert on infeasible config).
	if got := burnRate(0.99, 1.0); got != 0 {
		t.Fatalf("expected 0 for infeasible threshold, got %f", got)
	}
}

func TestBurnRate_NegativeObserved(t *testing.T) {
	// Should not happen, but protect: if observed > 1, no burn.
	if got := burnRate(1.5, 0.99); got != 0 {
		t.Fatalf("expected 0 for observed > 1, got %f", got)
	}
}
