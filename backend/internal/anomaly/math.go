package anomaly

import "xirang/backend/internal/mathx"

// Thin adapters so existing callers keep their anomaly.X import paths while
// the actual math lives in internal/mathx (shared with metrics/forecast.go
// and available for future callers without circular imports).

// EWMAMeanStddev is re-exported; see mathx.EWMAMeanStddev.
func EWMAMeanStddev(xs []float64, alpha float64) (mean, stddev float64) {
	return mathx.EWMAMeanStddev(xs, alpha)
}

// LinearRegression is re-exported; see mathx.LinearRegression.
func LinearRegression(xs, ys []float64) (slope, intercept, r2 float64) {
	return mathx.LinearRegression(xs, ys)
}
