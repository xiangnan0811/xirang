package metrics

import (
	"context"
	"testing"
	"time"

	"xirang/backend/internal/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newAggTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(
		&model.NodeMetricSample{},
		&model.NodeMetricSampleHourly{},
		&model.NodeMetricSampleDaily{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestRollupHourly_FillsBucket(t *testing.T) {
	db := newAggTestDB(t)
	base := time.Date(2026, 4, 17, 10, 0, 0, 0, time.UTC)
	// 3 raw samples in the 10:00 bucket with cpu 10, 20, 30
	for i := 0; i < 3; i++ {
		lat := int64(100 + i*10)
		if err := db.Create(&model.NodeMetricSample{
			NodeID:    1,
			CpuPct:    10.0 + float64(i)*10, // 10, 20, 30
			MemPct:    50,
			DiskPct:   40,
			Load1m:    0.5,
			LatencyMs: &lat,
			ProbeOK:   true,
			SampledAt: base.Add(time.Duration(i) * 10 * time.Minute),
		}).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	agg := &Aggregator{db: db, dialect: "sqlite"}

	n, err := agg.rollupHourly(context.Background(), base, base.Add(time.Hour))
	if err != nil {
		t.Fatalf("rollup: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 bucket written, got %d", n)
	}
	var got model.NodeMetricSampleHourly
	if err := db.First(&got, "node_id = ?", 1).Error; err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.CpuPctAvg == nil || *got.CpuPctAvg != 20 {
		t.Fatalf("expected cpu_pct_avg=20, got %v", got.CpuPctAvg)
	}
	if got.CpuPctMax == nil || *got.CpuPctMax != 30 {
		t.Fatalf("expected cpu_pct_max=30, got %v", got.CpuPctMax)
	}
	if got.ProbeOK != 3 || got.ProbeFail != 0 || got.SampleCount != 3 {
		t.Fatalf("bad counts: ok=%d fail=%d total=%d", got.ProbeOK, got.ProbeFail, got.SampleCount)
	}
}

func TestRollupHourly_Idempotent(t *testing.T) {
	db := newAggTestDB(t)
	base := time.Date(2026, 4, 17, 11, 0, 0, 0, time.UTC)
	if err := db.Create(&model.NodeMetricSample{
		NodeID:    1,
		CpuPct:    50,
		ProbeOK:   true,
		SampledAt: base.Add(15 * time.Minute),
	}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	agg := &Aggregator{db: db, dialect: "sqlite"}
	if _, err := agg.rollupHourly(context.Background(), base, base.Add(time.Hour)); err != nil {
		t.Fatalf("first rollup: %v", err)
	}
	// Add another raw sample, rerun — ON CONFLICT DO UPDATE should refresh aggregates.
	if err := db.Create(&model.NodeMetricSample{
		NodeID:    1,
		CpuPct:    150,
		ProbeOK:   true,
		SampledAt: base.Add(30 * time.Minute),
	}).Error; err != nil {
		t.Fatalf("seed 2: %v", err)
	}
	if _, err := agg.rollupHourly(context.Background(), base, base.Add(time.Hour)); err != nil {
		t.Fatalf("second rollup: %v", err)
	}
	var got model.NodeMetricSampleHourly
	db.First(&got, "node_id = ?", 1)
	if got.CpuPctMax == nil || *got.CpuPctMax != 150 {
		t.Fatalf("expected max to update to 150 after re-rollup, got %v", got.CpuPctMax)
	}
}

func TestRollupDaily_FromHourly(t *testing.T) {
	db := newAggTestDB(t)
	day := time.Date(2026, 4, 17, 0, 0, 0, 0, time.UTC)
	// Seed 24 hourly buckets in the day, cpu_pct_avg = h, cpu_pct_max = h+5.
	for h := 0; h < 24; h++ {
		cpuAvg := float64(h)
		cpuMax := float64(h) + 5
		if err := db.Create(&model.NodeMetricSampleHourly{
			NodeID:      1,
			BucketStart: day.Add(time.Duration(h) * time.Hour),
			CpuPctAvg:   &cpuAvg,
			CpuPctMax:   &cpuMax,
			ProbeOK:     10,
			ProbeFail:   0,
			SampleCount: 10,
		}).Error; err != nil {
			t.Fatalf("seed hour %d: %v", h, err)
		}
	}
	agg := &Aggregator{db: db, dialect: "sqlite"}

	n, err := agg.rollupDaily(context.Background(), day, day.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("rollup: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 day bucket, got %d", n)
	}
	var got model.NodeMetricSampleDaily
	if err := db.First(&got, "node_id = ?", 1).Error; err != nil {
		t.Fatalf("read back: %v", err)
	}
	// AVG of 0..23 = 11.5
	if got.CpuPctAvg == nil || *got.CpuPctAvg != 11.5 {
		t.Fatalf("expected cpu_pct_avg=11.5, got %v", got.CpuPctAvg)
	}
	// MAX of (h+5 for h=0..23) = 28
	if got.CpuPctMax == nil || *got.CpuPctMax != 28 {
		t.Fatalf("expected cpu_pct_max=28, got %v", got.CpuPctMax)
	}
	// SUM of probe_ok=10 over 24 buckets = 240
	if got.ProbeOK != 240 {
		t.Fatalf("expected probe_ok=240, got %d", got.ProbeOK)
	}
	if got.SampleCount != 240 {
		t.Fatalf("expected sample_count=240, got %d", got.SampleCount)
	}
}

func TestAggregator_BackfillsHourlyFromRaw(t *testing.T) {
	db := newAggTestDB(t)
	now := time.Now().UTC().Truncate(time.Hour)
	// Seed 3 raw samples at h-3, h-2, h-1 relative to now.
	for h := -3; h <= -1; h++ {
		if err := db.Create(&model.NodeMetricSample{
			NodeID:    1,
			CpuPct:    float64(h) + 50,
			ProbeOK:   true,
			SampledAt: now.Add(time.Duration(h) * time.Hour).Add(15 * time.Minute),
		}).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	agg := NewAggregator(db, "sqlite")
	if err := agg.backfill(context.Background()); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	var count int64
	db.Model(&model.NodeMetricSampleHourly{}).Count(&count)
	if count != 3 {
		t.Fatalf("expected 3 hourly buckets after backfill, got %d", count)
	}
}

func TestAggregator_CleanupAggregatesDropsOldBuckets(t *testing.T) {
	db := newAggTestDB(t)
	now := time.Now().UTC().Truncate(time.Hour)

	// Hourly: seed one fresh bucket (within retention) + one ancient bucket
	// (91 days old — just outside the 90-day window).
	freshHourly := now.Add(-1 * time.Hour)
	staleHourly := now.Add(-time.Duration(hourlyRetentionDays+1) * 24 * time.Hour)
	for _, ts := range []time.Time{freshHourly, staleHourly} {
		if err := db.Create(&model.NodeMetricSampleHourly{
			NodeID: 1, BucketStart: ts, SampleCount: 1,
		}).Error; err != nil {
			t.Fatalf("seed hourly %v: %v", ts, err)
		}
	}

	// Daily: one fresh, one ancient (731 days old — just outside 2y).
	freshDaily := now.Truncate(24 * time.Hour)
	staleDaily := now.Add(-time.Duration(dailyRetentionDays+1) * 24 * time.Hour).Truncate(24 * time.Hour)
	for _, ts := range []time.Time{freshDaily, staleDaily} {
		if err := db.Create(&model.NodeMetricSampleDaily{
			NodeID: 1, BucketStart: ts, SampleCount: 1,
		}).Error; err != nil {
			t.Fatalf("seed daily %v: %v", ts, err)
		}
	}

	agg := NewAggregator(db, "sqlite")
	if err := agg.cleanupAggregates(context.Background()); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	var hourlyCount int64
	db.Model(&model.NodeMetricSampleHourly{}).Count(&hourlyCount)
	if hourlyCount != 1 {
		t.Fatalf("expected 1 hourly row after cleanup, got %d", hourlyCount)
	}

	var dailyCount int64
	db.Model(&model.NodeMetricSampleDaily{}).Count(&dailyCount)
	if dailyCount != 1 {
		t.Fatalf("expected 1 daily row after cleanup, got %d", dailyCount)
	}

	// Idempotent: running again should not fail and should not delete any more.
	if err := agg.cleanupAggregates(context.Background()); err != nil {
		t.Fatalf("cleanup 2: %v", err)
	}
	db.Model(&model.NodeMetricSampleHourly{}).Count(&hourlyCount)
	db.Model(&model.NodeMetricSampleDaily{}).Count(&dailyCount)
	if hourlyCount != 1 || dailyCount != 1 {
		t.Fatalf("expected counts unchanged on second cleanup, got hourly=%d daily=%d", hourlyCount, dailyCount)
	}
}
