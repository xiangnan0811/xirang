package metrics

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"xirang/backend/internal/logger"

	"gorm.io/gorm"
)

// Retention windows for the aggregate tiers. Raw (node_metric_samples) is
// still pruned by prober.cleanupOldMetrics on a 7-day window. These two
// constants match the P5a design spec ("Raw 7d / Hourly 90d / Daily 2y").
const (
	hourlyRetentionDays = 90
	dailyRetentionDays  = 730
)

// Aggregator rolls raw node_metric_samples rows into hourly and daily tiers.
// Methods are safe for concurrent callers guarded by its internal mutex.
type Aggregator struct {
	db      *gorm.DB
	dialect string // "sqlite" or "postgres"
	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	// nowFn resolves "current time" for all rollup/cleanup windows. Overridable
	// for tests so backfill behavior doesn't depend on wall-clock alignment
	// (the 5-minute cushion in catchUpHourly is flaky when CI lands in the
	// first 5 minutes of an hour and the test's seeded samples straddle the
	// current hour boundary).
	nowFn func() time.Time
}

// NewAggregator builds an Aggregator. dialect must be "sqlite" or "postgres".
func NewAggregator(db *gorm.DB, dialect string) *Aggregator {
	return &Aggregator{db: db, dialect: dialect, done: make(chan struct{}), nowFn: func() time.Time { return time.Now().UTC() }}
}

// SetNowFn overrides the clock source (tests only). Not safe to call after Start.
func (a *Aggregator) SetNowFn(fn func() time.Time) { a.nowFn = fn }

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

// rollupDaily aggregates hourly buckets with bucket_start in [from, to) into
// the daily tier. Same idempotency guarantee and mutex discipline as
// rollupHourly. probe_ok / probe_fail / sample_count are summed (they are
// already counts from the hourly layer). Floats use AVG(avg)/MAX(max).
func (a *Aggregator) rollupDaily(ctx context.Context, from, to time.Time) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	bucket := a.bucketExpr("day", "bucket_start")
	query := fmt.Sprintf(`
        INSERT INTO node_metric_samples_daily (
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
            AVG(cpu_pct_avg), MAX(cpu_pct_max),
            AVG(mem_pct_avg), MAX(mem_pct_max),
            AVG(disk_pct_avg), MAX(disk_pct_max),
            AVG(load1_avg),    MAX(load1_max),
            AVG(latency_ms_avg), MAX(latency_ms_max),
            AVG(disk_gb_used_avg),
            MAX(disk_gb_total),
            SUM(probe_ok), SUM(probe_fail), SUM(sample_count),
            CURRENT_TIMESTAMP
        FROM node_metric_samples_hourly
        WHERE bucket_start >= ? AND bucket_start < ?
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

// backfill walks forward from the oldest uncovered window, synchronously
// filling hourly then daily tiers up to (now - 5min) and (now - 1d)
// respectively. Called once at Start before the tickers arm. Safe to call
// on a cold boot (empty aggregate tables → backfill from oldest raw sample).
func (a *Aggregator) backfill(ctx context.Context) error {
	if err := a.catchUpHourly(ctx); err != nil {
		return fmt.Errorf("hourly backfill: %w", err)
	}
	return a.catchUpDaily(ctx)
}

// scanNullableTime scans a MAX/MIN aggregate (Postgres native timestamptz or
// SQLite TEXT affinity) into a time.Time.
//
//   - (zero, nil)   — column is NULL (empty table)
//   - (value, nil)  — parsed successfully
//   - (zero, err)   — DB query failed OR SQLite returned an unparseable string
//
// Treating a query error as "empty table" silently stalls rollup (lag metric
// goes to 0 and no new buckets are filled), so callers must distinguish.
func (a *Aggregator) scanNullableTime(db *gorm.DB, query string, args ...interface{}) (time.Time, error) {
	if a.dialect == "postgres" {
		var t sql.NullTime
		if err := db.Raw(query, args...).Scan(&t).Error; err != nil {
			return time.Time{}, err
		}
		if t.Valid {
			return t.Time.UTC(), nil
		}
		return time.Time{}, nil
	}
	var raw *string
	if err := db.Raw(query, args...).Scan(&raw).Error; err != nil {
		return time.Time{}, err
	}
	if raw == nil || *raw == "" {
		return time.Time{}, nil
	}
	// SQLite emits datetimes in several formats depending on how they were stored.
	formats := []string{
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05Z",
		time.RFC3339Nano,
		time.RFC3339,
	}
	for _, f := range formats {
		if t, err := time.Parse(f, *raw); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unparseable datetime from SQLite: %q", *raw)
}

// catchUpHourly walks every uncovered hourly bucket from MAX(bucket_start)+1h
// (or oldest raw sample, if the table is empty) up to now-5min, calling
// rollupHourly on each. The 5-minute cushion avoids picking up an in-flight
// hour. Does NOT acquire a.mu — rollupHourly locks each call individually.
func (a *Aggregator) catchUpHourly(ctx context.Context) error {
	lastBucket, err := a.scanNullableTime(a.db.WithContext(ctx),
		"SELECT MAX(bucket_start) FROM node_metric_samples_hourly")
	if err != nil {
		return fmt.Errorf("catchUpHourly: read last bucket: %w", err)
	}
	oldestSample, err := a.scanNullableTime(a.db.WithContext(ctx),
		"SELECT MIN(sampled_at) FROM node_metric_samples")
	if err != nil {
		return fmt.Errorf("catchUpHourly: read oldest sample: %w", err)
	}

	var from time.Time
	if lastBucket.IsZero() {
		if oldestSample.IsZero() {
			rollupLagSeconds.WithLabelValues("hourly").Set(0)
			return nil
		}
		from = oldestSample.Truncate(time.Hour)
	} else {
		from = lastBucket.Add(time.Hour)
	}
	end := a.nowFn().Add(-5 * time.Minute).Truncate(time.Hour)
	for from.Before(end) {
		to := from.Add(time.Hour)
		if _, err := a.rollupHourly(ctx, from, to); err != nil {
			return err
		}
		from = to
	}
	rollupLagSeconds.WithLabelValues("hourly").Set(time.Since(end).Seconds())
	return nil
}

// catchUpDaily does the same for daily buckets, sourcing from hourly.
func (a *Aggregator) catchUpDaily(ctx context.Context) error {
	lastBucket, err := a.scanNullableTime(a.db.WithContext(ctx),
		"SELECT MAX(bucket_start) FROM node_metric_samples_daily")
	if err != nil {
		return fmt.Errorf("catchUpDaily: read last bucket: %w", err)
	}
	oldestHourly, err := a.scanNullableTime(a.db.WithContext(ctx),
		"SELECT MIN(bucket_start) FROM node_metric_samples_hourly")
	if err != nil {
		return fmt.Errorf("catchUpDaily: read oldest hourly bucket: %w", err)
	}

	var from time.Time
	if lastBucket.IsZero() {
		if oldestHourly.IsZero() {
			rollupLagSeconds.WithLabelValues("daily").Set(0)
			return nil
		}
		from = oldestHourly.Truncate(24 * time.Hour)
	} else {
		from = lastBucket.Add(24 * time.Hour)
	}
	end := a.nowFn().Truncate(24 * time.Hour)
	for from.Before(end) {
		to := from.Add(24 * time.Hour)
		if _, err := a.rollupDaily(ctx, from, to); err != nil {
			return err
		}
		from = to
	}
	rollupLagSeconds.WithLabelValues("daily").Set(time.Since(end).Seconds())
	return nil
}

// cleanupAggregates deletes hourly buckets older than hourlyRetentionDays and
// daily buckets older than dailyRetentionDays. Takes the mutex to avoid
// racing with concurrent rollup upserts on the same tables. Safe to call
// repeatedly — DELETE is idempotent when the window is empty.
func (a *Aggregator) cleanupAggregates(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	now := a.nowFn()
	hourlyCutoff := now.Add(-time.Duration(hourlyRetentionDays) * 24 * time.Hour)
	dailyCutoff := now.Add(-time.Duration(dailyRetentionDays) * 24 * time.Hour)

	hourlyResult := a.db.WithContext(ctx).Exec(
		"DELETE FROM node_metric_samples_hourly WHERE bucket_start < ?", hourlyCutoff,
	)
	if hourlyResult.Error != nil {
		return fmt.Errorf("hourly cleanup: %w", hourlyResult.Error)
	}
	dailyResult := a.db.WithContext(ctx).Exec(
		"DELETE FROM node_metric_samples_daily WHERE bucket_start < ?", dailyCutoff,
	)
	if dailyResult.Error != nil {
		return fmt.Errorf("daily cleanup: %w", dailyResult.Error)
	}
	if hourlyResult.RowsAffected > 0 || dailyResult.RowsAffected > 0 {
		logger.Module("metrics").Info().
			Int64("hourly_deleted", hourlyResult.RowsAffected).
			Int64("daily_deleted", dailyResult.RowsAffected).
			Msg("aggregate retention cleanup")
	}
	return nil
}

// Start performs backfill synchronously, runs one immediate retention pass,
// then arms the periodic tickers. Blocks on backfill; returns once the
// background loop is running.
func (a *Aggregator) Start(ctx context.Context) error {
	if err := a.backfill(ctx); err != nil {
		return err
	}
	// Enforce retention immediately at startup — a fresh deploy should not
	// wait 24h for the first cleanup tick to prune stale data from the DB.
	if err := a.cleanupAggregates(ctx); err != nil {
		logger.Module("metrics").Warn().Err(err).Msg("initial aggregate cleanup failed")
	}
	aggCtx, cancel := context.WithCancel(ctx)
	a.cancel = cancel
	go a.loop(aggCtx)
	return nil
}

// loop runs the scheduled ticks until ctx is cancelled.
func (a *Aggregator) loop(ctx context.Context) {
	defer close(a.done)
	hourly := time.NewTicker(1 * time.Minute)
	daily := time.NewTicker(10 * time.Minute)
	retention := time.NewTicker(24 * time.Hour)
	defer hourly.Stop()
	defer daily.Stop()
	defer retention.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-hourly.C:
			a.measureRollup(ctx, "hourly", a.catchUpHourly)
		case <-daily.C:
			a.measureRollup(ctx, "daily", a.catchUpDaily)
		case <-retention.C:
			if err := a.cleanupAggregates(ctx); err != nil {
				logger.Module("metrics").Warn().Err(err).Msg("aggregate cleanup failed")
			}
		}
	}
}

// measureRollup wraps a catch-up call with duration observation and error
// logging. Errors never escalate — they're logged and the next tick retries.
func (a *Aggregator) measureRollup(ctx context.Context, tier string, fn func(context.Context) error) {
	start := time.Now()
	if err := fn(ctx); err != nil {
		logger.Module("metrics").Warn().Str("tier", tier).Err(err).Msg("rollup failed")
	}
	rollupDurationSeconds.WithLabelValues(tier).Observe(time.Since(start).Seconds())
}

// Stop signals the background loop to exit and waits for completion or ctx timeout.
func (a *Aggregator) Stop(ctx context.Context) error {
	if a.cancel != nil {
		a.cancel()
	}
	select {
	case <-a.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
