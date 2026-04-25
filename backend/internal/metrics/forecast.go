package metrics

import "xirang/backend/internal/mathx"

// Confidence tiers for the disk-growth forecast.
type Confidence string

const (
	ConfidenceHigh         Confidence = "high"
	ConfidenceMedium       Confidence = "medium"
	ConfidenceLow          Confidence = "low"
	ConfidenceInsufficient Confidence = "insufficient"
)

// ForecastPoint is one (x, y) observation fed to DiskForecast. Day is the
// independent variable (typically day index or days-since-epoch); DiskGBUsed
// is the observed GB used on that day.
type ForecastPoint struct {
	Day        float64
	DiskGBUsed float64
}

// ForecastResult carries the fitted slope, an optional projection to full,
// and the confidence tier. DaysToFull is nil when the slope is ≤ 0
// (disk shrinking or flat — no projection to full). Confidence is
// "insufficient" when the point set is too small for any estimate.
type ForecastResult struct {
	DailyGrowthGB *float64
	DaysToFull    *float64
	Confidence    Confidence
	RSquared      float64
}

// DiskForecast runs an ordinary least-squares linear regression on the given
// points and returns slope, r², and (if slope > 0) projected days-to-full.
//
// Confidence tiers:
//
//	≥ 21 points AND r² ≥ 0.7 → high
//	≥ 14 points AND r² ≥ 0.3 → medium
//	≥  7 points            → low
//	<  7 points            → insufficient
//
// The OLS math itself lives in internal/mathx so the same primitive serves
// both the disk forecaster and the anomaly detector. This file keeps the
// domain layer (ForecastPoint shape, Confidence tiers, days-to-full
// projection) — only the inner regression call moved.
func DiskForecast(points []ForecastPoint, diskGBTotal float64) ForecastResult {
	n := len(points)
	if n < 7 {
		return ForecastResult{Confidence: ConfidenceInsufficient}
	}
	xs := make([]float64, n)
	ys := make([]float64, n)
	for i, p := range points {
		xs[i] = p.Day
		ys[i] = p.DiskGBUsed
	}
	slope, _, r2 := mathx.LinearRegression(xs, ys)
	// LinearRegression returns (0, mean(ys), 0) for zero variance in xs;
	// preserve the original "insufficient" semantic by treating that as
	// a degenerate fit. Any caller passing identical Day values gets the
	// same result either way.
	if slope == 0 && r2 == 0 {
		// Distinguish "all xs equal" (degenerate) from "true zero slope".
		// If every Day matches the first one, the OLS would have returned
		// (0, meanY, 0). Otherwise slope==0,r2==0 only when xs are
		// constant — same conclusion.
		allEqual := true
		for i := 1; i < n; i++ {
			if xs[i] != xs[0] {
				allEqual = false
				break
			}
		}
		if allEqual {
			return ForecastResult{Confidence: ConfidenceInsufficient}
		}
	}

	conf := ConfidenceLow
	if n >= 14 && r2 >= 0.3 {
		conf = ConfidenceMedium
	}
	if n >= 21 && r2 >= 0.7 {
		conf = ConfidenceHigh
	}

	result := ForecastResult{DailyGrowthGB: &slope, Confidence: conf, RSquared: r2}
	if slope > 0 {
		lastY := points[n-1].DiskGBUsed
		days := (diskGBTotal - lastY) / slope
		result.DaysToFull = &days
	}
	return result
}
