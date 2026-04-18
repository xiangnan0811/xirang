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
