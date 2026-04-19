package metrics

import "time"

// Granularity identifies which metric storage tier to query.
type Granularity string

const (
	GranularityRaw    Granularity = "raw"
	GranularityHourly Granularity = "hourly"
	GranularityDaily  Granularity = "daily"
)

// SelectGranularity picks a tier based on the requested time span.
//
// Per the P5a spec's auto-selection table:
//
//	≤ 3 days   → raw     (optionally downsampled client-side)
//	≤ 90 days  → hourly
//	> 90 days  → daily
//
// The first two buckets (≤ 6h, 6h–3d) both map to raw; the handler applies
// server-side downsampling if the raw window is wide enough to exceed the
// 1500-points cap.
func SelectGranularity(span time.Duration) Granularity {
	day := 24 * time.Hour
	switch {
	case span <= 3*day:
		return GranularityRaw
	case span <= 90*day:
		return GranularityHourly
	default:
		return GranularityDaily
	}
}
