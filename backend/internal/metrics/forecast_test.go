package metrics

import (
	"math"
	"testing"
)

func TestForecast_Insufficient(t *testing.T) {
	pts := []ForecastPoint{{1, 10}, {2, 11}, {3, 12}}
	f := DiskForecast(pts, 100)
	if f.Confidence != ConfidenceInsufficient {
		t.Fatalf("expected insufficient, got %v", f.Confidence)
	}
}

func TestForecast_StrongLinearGrowth(t *testing.T) {
	pts := make([]ForecastPoint, 21)
	for i := 0; i < 21; i++ {
		pts[i] = ForecastPoint{Day: float64(i), DiskGBUsed: 100 + float64(i)*2}
	}
	f := DiskForecast(pts, 200)
	if f.Confidence != ConfidenceHigh {
		t.Fatalf("expected high confidence, got %v", f.Confidence)
	}
	if f.DailyGrowthGB == nil || math.Abs(*f.DailyGrowthGB-2) > 1e-6 {
		t.Fatalf("expected growth ≈2, got %v", f.DailyGrowthGB)
	}
	if f.DaysToFull == nil || *f.DaysToFull <= 0 {
		t.Fatalf("expected positive days_to_full, got %v", f.DaysToFull)
	}
}

func TestForecast_NegativeSlope(t *testing.T) {
	pts := make([]ForecastPoint, 14)
	for i := 0; i < 14; i++ {
		pts[i] = ForecastPoint{Day: float64(i), DiskGBUsed: 200 - float64(i)*0.5}
	}
	f := DiskForecast(pts, 300)
	if f.DaysToFull != nil {
		t.Fatalf("expected nil days_to_full on negative slope, got %v", *f.DaysToFull)
	}
}

func TestForecast_NoisyLowConfidence(t *testing.T) {
	pts := []ForecastPoint{{1, 50}, {2, 52}, {3, 48}, {4, 51}, {5, 49}, {6, 53}, {7, 50}}
	f := DiskForecast(pts, 100)
	if f.Confidence != ConfidenceLow {
		t.Fatalf("expected low confidence for 7 noisy samples, got %v", f.Confidence)
	}
}
