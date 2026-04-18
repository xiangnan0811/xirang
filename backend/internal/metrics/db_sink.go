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
//
// Note: probe_ok is passed as an integer (0/1) via raw Exec to work around a
// GORM/SQLite quirk where Go false is serialised as the SQL literal "true" on
// numeric-affinity columns that carry DEFAULT true.
func (s *DBSink) Write(ctx context.Context, sample Sample) error {
	row := model.NodeMetricSample{
		NodeID:      sample.NodeID,
		SampledAt:   sample.SampledAt,
		DiskGBUsed:  sample.DiskGBUsed,
		DiskGBTotal: sample.DiskGBTotal,
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

	// Encode the bool as 0/1 so the SQLite driver cannot misrepresent false
	// as the SQL literal "true" (a known GORM/SQLite quirk on numeric-affinity
	// columns with DEFAULT true).
	probeOK := 0
	if sample.ProbeOK {
		probeOK = 1
	}

	return s.db.WithContext(ctx).Exec(
		`INSERT INTO node_metric_samples
		 (node_id, sampled_at, cpu_pct, mem_pct, disk_pct, load_1m,
		  latency_ms, disk_gb_used, disk_gb_total, probe_ok)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.NodeID, row.SampledAt,
		row.CpuPct, row.MemPct, row.DiskPct, row.Load1m,
		row.LatencyMs, row.DiskGBUsed, row.DiskGBTotal,
		probeOK,
	).Error
}
