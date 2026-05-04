package metrics

import (
	"context"
	"testing"
	"time"

	"xirang/backend/internal/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newDBSinkTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?_loc=UTC"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.NodeMetricSample{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestDBSink_WriteInsertsRow(t *testing.T) {
	db := newDBSinkTestDB(t)
	sink := NewDBSink(db)
	cpu, mem, disk, load, lat := 12.5, 45.0, 78.3, 0.4, 15.0
	diskUsed, diskTotal := 312.5, 500.0
	sampledAt := time.Now().UTC()

	if err := sink.Write(context.Background(), Sample{
		NodeID:      7,
		SampledAt:   sampledAt,
		CPUPct:      &cpu,
		MemPct:      &mem,
		DiskPct:     &disk,
		Load1:       &load,
		LatencyMs:   &lat,
		DiskGBUsed:  &diskUsed,
		DiskGBTotal: &diskTotal,
		ProbeOK:     true,
	}); err != nil {
		t.Fatalf("write: %v", err)
	}

	var got model.NodeMetricSample
	if err := db.First(&got, "node_id = ?", 7).Error; err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.CpuPct != 12.5 || got.MemPct != 45.0 || got.DiskPct != 78.3 || got.Load1m != 0.4 {
		t.Fatalf("core floats wrong: %+v", got)
	}
	if !got.ProbeOK {
		t.Fatalf("probe_ok not persisted")
	}
	if got.LatencyMs == nil || *got.LatencyMs != 15 {
		t.Fatalf("latency_ms expected 15, got %v", got.LatencyMs)
	}
	if got.DiskGBUsed == nil || *got.DiskGBUsed != 312.5 {
		t.Fatalf("disk_gb_used expected 312.5, got %v", got.DiskGBUsed)
	}
	if got.DiskGBTotal == nil || *got.DiskGBTotal != 500.0 {
		t.Fatalf("disk_gb_total expected 500, got %v", got.DiskGBTotal)
	}
}

func TestDBSink_NilNumericsRemainZeroOrNil(t *testing.T) {
	db := newDBSinkTestDB(t)
	sink := NewDBSink(db)
	if err := sink.Write(context.Background(), Sample{
		NodeID:    8,
		SampledAt: time.Now().UTC(),
		ProbeOK:   false,
		// All numeric pointers intentionally nil
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	var got model.NodeMetricSample
	if err := db.First(&got, "node_id = ?", 8).Error; err != nil {
		t.Fatalf("read back: %v", err)
	}
	// Non-nullable columns should have default zero values.
	if got.CpuPct != 0 || got.MemPct != 0 || got.DiskPct != 0 || got.Load1m != 0 {
		t.Fatalf("expected zeros for nil pointer metrics: %+v", got)
	}
	// Nullable columns should remain nil pointers.
	if got.LatencyMs != nil || got.DiskGBUsed != nil || got.DiskGBTotal != nil {
		t.Fatalf("expected nil pointers for nullable columns: latency=%v used=%v total=%v", got.LatencyMs, got.DiskGBUsed, got.DiskGBTotal)
	}
	if got.ProbeOK {
		t.Fatalf("expected probe_ok false")
	}
}

func TestDBSink_Name(t *testing.T) {
	sink := NewDBSink(nil)
	if sink.Name() != "db" {
		t.Fatalf("expected Name() = \"db\", got %q", sink.Name())
	}
}
