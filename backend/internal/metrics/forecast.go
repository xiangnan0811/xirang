package metrics

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
func DiskForecast(points []ForecastPoint, diskGBTotal float64) ForecastResult {
	n := len(points)
	if n < 7 {
		return ForecastResult{Confidence: ConfidenceInsufficient}
	}
	var sumX, sumY, sumXY, sumXX float64
	for _, p := range points {
		sumX += p.Day
		sumY += p.DiskGBUsed
		sumXY += p.Day * p.DiskGBUsed
		sumXX += p.Day * p.Day
	}
	fn := float64(n)
	denom := fn*sumXX - sumX*sumX
	if denom == 0 {
		return ForecastResult{Confidence: ConfidenceInsufficient}
	}
	slope := (fn*sumXY - sumX*sumY) / denom
	intercept := (sumY - slope*sumX) / fn

	// r² = 1 - SS_res / SS_tot
	var ssTot, ssRes float64
	meanY := sumY / fn
	for _, p := range points {
		pred := slope*p.Day + intercept
		ssRes += (p.DiskGBUsed - pred) * (p.DiskGBUsed - pred)
		ssTot += (p.DiskGBUsed - meanY) * (p.DiskGBUsed - meanY)
	}
	var r2 float64
	if ssTot > 0 {
		r2 = 1 - ssRes/ssTot
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
