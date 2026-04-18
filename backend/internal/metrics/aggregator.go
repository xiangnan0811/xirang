package metrics

import (
	"context"
	"fmt"
	"sync"
	"time"

	"gorm.io/gorm"
)

// Aggregator rolls raw node_metric_samples rows into hourly and daily tiers.
// Methods are safe for concurrent callers guarded by its internal mutex.
type Aggregator struct {
	db      *gorm.DB
	dialect string // "sqlite" or "postgres"
	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
}

// NewAggregator builds an Aggregator. dialect must be "sqlite" or "postgres".
func NewAggregator(db *gorm.DB, dialect string) *Aggregator {
	return &Aggregator{db: db, dialect: dialect, done: make(chan struct{})}
}

// bucketExpr returns dialect-specific SQL to truncate a time column to the given unit.
// unit: "hour" or "day". col: the source column name (e.g. "sampled_at", "bucket_start").
func (a *Aggregator) bucketExpr(unit, col string) string {
	if a.dialect == "postgres" {
		return fmt.Sprintf("date_trunc('%s', %s)", unit, col)
	}
	// SQLite strftime formatting.
	switch unit {
	case "hour":
		return fmt.Sprintf("datetime(strftime('%%Y-%%m-%%d %%H:00:00', %s))", col)
	case "day":
		return fmt.Sprintf("datetime(strftime('%%Y-%%m-%%d 00:00:00', %s))", col)
	}
	return col
}

// rollupHourly aggregates raw samples with sampled_at in [from, to) into the
// hourly tier. Idempotent: re-running the same window replaces aggregates via
// ON CONFLICT DO UPDATE. Returns the number of buckets written/updated.
//
// Acquires the Aggregator mutex to serialise backfill vs scheduled ticks —
// both paths call this method and must not interleave on the same window.
//
// MAX(disk_gb_total) is a deliberate approximation: disk capacity rarely
// changes within an hour, and if it does (resize), preferring the larger
// value matches the spec's "terminal value" semantic without tracking order.
func (a *Aggregator) rollupHourly(ctx context.Context, from, to time.Time) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	bucket := a.bucketExpr("hour", "sampled_at")
	query := fmt.Sprintf(`
        INSERT INTO node_metric_samples_hourly (
            node_id, bucket_start,
            cpu_pct_avg, cpu_pct_max,
            mem_pct_avg, mem_pct_max,
            disk_pct_avg, disk_pct_max,
            load1_avg, load1_max,
            latency_ms_avg, latency_ms_max,
            disk_gb_used_avg, disk_gb_total,
            probe_ok, probe_fail, sample_count, created_at
        )
        SELECT
            node_id,
            %[1]s AS bucket_start,
            AVG(cpu_pct), MAX(cpu_pct),
            AVG(mem_pct), MAX(mem_pct),
            AVG(disk_pct), MAX(disk_pct),
            AVG(load_1m), MAX(load_1m),
            AVG(latency_ms), MAX(latency_ms),
            AVG(disk_gb_used),
            MAX(disk_gb_total),
            SUM(CASE WHEN probe_ok THEN 1 ELSE 0 END),
            SUM(CASE WHEN probe_ok THEN 0 ELSE 1 END),
            COUNT(*),
            CURRENT_TIMESTAMP
        FROM node_metric_samples
        WHERE sampled_at >= ? AND sampled_at < ?
        GROUP BY node_id, %[1]s
        ON CONFLICT (node_id, bucket_start) DO UPDATE SET
            cpu_pct_avg      = excluded.cpu_pct_avg,
            cpu_pct_max      = excluded.cpu_pct_max,
            mem_pct_avg      = excluded.mem_pct_avg,
            mem_pct_max      = excluded.mem_pct_max,
            disk_pct_avg     = excluded.disk_pct_avg,
            disk_pct_max     = excluded.disk_pct_max,
            load1_avg        = excluded.load1_avg,
            load1_max        = excluded.load1_max,
            latency_ms_avg   = excluded.latency_ms_avg,
            latency_ms_max   = excluded.latency_ms_max,
            disk_gb_used_avg = excluded.disk_gb_used_avg,
            disk_gb_total    = excluded.disk_gb_total,
            probe_ok         = excluded.probe_ok,
            probe_fail       = excluded.probe_fail,
            sample_count     = excluded.sample_count
    `, bucket)

	result := a.db.WithContext(ctx).Exec(query, from, to)
	if result.Error != nil {
		return 0, result.Error
	}
	return int(result.RowsAffected), nil
}
