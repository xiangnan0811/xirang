package anomaly

import (
	"math"
	"testing"
)

func approx(a, b, eps float64) bool {
	return math.Abs(a-b) <= eps
}

func TestEWMA_EmptyInput(t *testing.T) {
	mean, sd := EWMAMeanStddev(nil, 0.3)
	if mean != 0 || sd != 0 {
		t.Fatalf("expected (0,0), got (%v,%v)", mean, sd)
	}
}

func TestEWMA_SinglePoint(t *testing.T) {
	mean, sd := EWMAMeanStddev([]float64{42}, 0.3)
	if mean != 42 || sd != 0 {
		t.Fatalf("expected (42,0), got (%v,%v)", mean, sd)
	}
}

func TestEWMA_InvalidAlpha(t *testing.T) {
	for _, a := range []float64{0, -0.1, 1.5, 2} {
		mean, sd := EWMAMeanStddev([]float64{1, 2, 3}, a)
		if mean != 0 || sd != 0 {
			t.Fatalf("alpha=%v expected (0,0), got (%v,%v)", a, mean, sd)
		}
	}
}

func TestEWMA_ConstantSequence(t *testing.T) {
	mean, sd := EWMAMeanStddev([]float64{5, 5, 5, 5, 5}, 0.3)
	if !approx(mean, 5, 1e-9) || sd != 0 {
		t.Fatalf("const: expected (5,0), got (%v,%v)", mean, sd)
	}
}

func TestEWMA_SpikeAtEnd(t *testing.T) {
	// Plateau at 10 then jump to 50 — mean should stay near 10, stddev > 0
	mean, sd := EWMAMeanStddev([]float64{10, 10, 10, 10, 50}, 0.3)
	// Running mean after 4 flat observations at 10 is ~10. Last sample bumps mean toward 10 + 0.3*40 = 22.
	if mean < 10 || mean > 25 {
		t.Fatalf("spike: mean=%v out of expected [10,25]", mean)
	}
	if sd <= 0 {
		t.Fatalf("spike: stddev=%v should be > 0", sd)
	}
}

func TestLinearRegression_PerfectLine(t *testing.T) {
	// y = 2x + 3, r2 = 1
	xs := []float64{0, 1, 2, 3, 4}
	ys := []float64{3, 5, 7, 9, 11}
	slope, intercept, r2 := LinearRegression(xs, ys)
	if !approx(slope, 2, 1e-9) || !approx(intercept, 3, 1e-9) || !approx(r2, 1, 1e-9) {
		t.Fatalf("expected (2,3,1), got (%v,%v,%v)", slope, intercept, r2)
	}
}

func TestLinearRegression_FlatLine(t *testing.T) {
	// Horizontal: slope=0, r2=0
	xs := []float64{0, 1, 2, 3}
	ys := []float64{5, 5, 5, 5}
	slope, intercept, r2 := LinearRegression(xs, ys)
	if slope != 0 || !approx(intercept, 5, 1e-9) || r2 != 0 {
		t.Fatalf("flat: expected (0,5,0), got (%v,%v,%v)", slope, intercept, r2)
	}
}

func TestLinearRegression_InsufficientInput(t *testing.T) {
	s, i, r := LinearRegression([]float64{1}, []float64{2})
	if s != 0 || i != 0 || r != 0 {
		t.Fatalf("single point: expected (0,0,0), got (%v,%v,%v)", s, i, r)
	}
	s, i, r = LinearRegression([]float64{1, 2}, []float64{3}) // mismatched
	if s != 0 || i != 0 || r != 0 {
		t.Fatalf("mismatch: expected (0,0,0), got (%v,%v,%v)", s, i, r)
	}
	s, i, r = LinearRegression(nil, nil)
	if s != 0 || i != 0 || r != 0 {
		t.Fatalf("nil: expected (0,0,0), got (%v,%v,%v)", s, i, r)
	}
}

func TestLinearRegression_NoisyLine_R2Between0And1(t *testing.T) {
	// Slope ~1 with small noise
	xs := []float64{0, 1, 2, 3, 4, 5}
	ys := []float64{0.1, 1.2, 1.9, 3.1, 4.0, 5.2}
	slope, _, r2 := LinearRegression(xs, ys)
	if !approx(slope, 1, 0.2) {
		t.Fatalf("slope=%v, want ~1", slope)
	}
	if r2 < 0.95 {
		t.Fatalf("r2=%v want > 0.95", r2)
	}
}
