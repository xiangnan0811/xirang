package anomaly

import "math"

// EWMAMeanStddev computes an exponentially-weighted moving average mean and
// stddev of xs (assumed sorted by time ASC). alpha ∈ (0,1) is the smoothing
// factor: larger alpha = more weight on recent observations.
//
// Returns (0, 0) for empty input or alpha outside (0,1]. For length 1 returns
// (xs[0], 0) — a single observation has no spread.
//
// The stddev uses an EWMA of squared deviations from the running mean, which
// approximates the variance well enough for anomaly detection while staying
// stateless per call.
func EWMAMeanStddev(xs []float64, alpha float64) (mean, stddev float64) {
	if len(xs) == 0 {
		return 0, 0
	}
	if alpha <= 0 || alpha > 1 {
		return 0, 0
	}
	mean = xs[0]
	var variance float64
	for i := 1; i < len(xs); i++ {
		diff := xs[i] - mean
		mean = mean + alpha*diff
		variance = (1-alpha)*(variance + alpha*diff*diff)
	}
	if variance < 0 {
		variance = 0
	}
	return mean, math.Sqrt(variance)
}

// LinearRegression performs simple linear regression y = intercept + slope*x
// on the provided points. Returns (slope, intercept, r2). len(xs) != len(ys)
// or < 2 returns (0, 0, 0). If variance of xs is 0, returns (0, mean(ys), 0).
func LinearRegression(xs, ys []float64) (slope, intercept, r2 float64) {
	if len(xs) != len(ys) || len(xs) < 2 {
		return 0, 0, 0
	}
	n := float64(len(xs))
	var sumX, sumY, sumXY, sumXX, sumYY float64
	for i := range xs {
		sumX += xs[i]
		sumY += ys[i]
		sumXY += xs[i] * ys[i]
		sumXX += xs[i] * xs[i]
		sumYY += ys[i] * ys[i]
	}
	meanX := sumX / n
	meanY := sumY / n
	varX := sumXX/n - meanX*meanX
	if varX <= 0 {
		return 0, meanY, 0
	}
	covXY := sumXY/n - meanX*meanY
	slope = covXY / varX
	intercept = meanY - slope*meanX

	// R² = 1 - SS_res/SS_tot
	varY := sumYY/n - meanY*meanY
	if varY <= 0 {
		return slope, intercept, 0
	}
	ssTot := varY * n
	var ssRes float64
	for i := range xs {
		pred := intercept + slope*xs[i]
		d := ys[i] - pred
		ssRes += d * d
	}
	r2 = 1 - ssRes/ssTot
	if r2 < 0 {
		r2 = 0
	}
	return slope, intercept, r2
}
