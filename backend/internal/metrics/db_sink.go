package metrics

import (
	"context"

	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

// DBSink persists a Sample into the raw node_metric_samples table.
// It is the default always-on sink: every probe tick that produced a valid
// Sample flows through here, and P5a's aggregator later rolls these rows
// into the hourly/daily tiers.
type DBSink struct{ db *gorm.DB }

// NewDBSink builds a DBSink around an existing *gorm.DB.
func NewDBSink(db *gorm.DB) *DBSink { return &DBSink{db: db} }

// Name identifies this sink for error logs.
func (s *DBSink) Name() string { return "db" }

// Write converts a Sample into model.NodeMetricSample and inserts it.
// Pointer fields on Sample map to:
//   - nullable columns (LatencyMs *int64, DiskGBUsed *float64, DiskGBTotal
//     *float64) — left nil when Sample has nil
//   - non-nullable float columns (cpu/mem/disk pct, load_1m) — left as zero
//     when Sample has nil, because those columns are NOT NULL DEFAULT 0
func (s *DBSink) Write(ctx context.Context, sample Sample) error {
	row := model.NodeMetricSample{
		NodeID:      sample.NodeID,
		SampledAt:   sample.SampledAt,
		DiskGBUsed:  sample.DiskGBUsed,
		DiskGBTotal: sample.DiskGBTotal,
		ProbeOK:     sample.ProbeOK,
	}
	if sample.CPUPct != nil {
		row.CpuPct = *sample.CPUPct
	}
	if sample.MemPct != nil {
		row.MemPct = *sample.MemPct
	}
	if sample.DiskPct != nil {
		row.DiskPct = *sample.DiskPct
	}
	if sample.Load1 != nil {
		row.Load1m = *sample.Load1
	}
	if sample.LatencyMs != nil {
		v := int64(*sample.LatencyMs)
		row.LatencyMs = &v
	}
	return s.db.WithContext(ctx).Create(&row).Error
}
