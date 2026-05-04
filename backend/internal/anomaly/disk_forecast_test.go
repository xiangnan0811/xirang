package anomaly

import (
	"context"
	"testing"
	"time"

	"xirang/backend/internal/model"
	"xirang/backend/internal/settings"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openDiskForecastDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared&_loc=UTC"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(&model.Node{}, &model.NodeMetricSampleHourly{}, &model.SystemSetting{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func seedHourly(t *testing.T, db *gorm.DB, nodeID uint, bucket time.Time, diskAvg float64) {
	t.Helper()
	v := diskAvg
	if err := db.Create(&model.NodeMetricSampleHourly{
		NodeID:      nodeID,
		BucketStart: bucket,
		DiskPctAvg:  &v,
	}).Error; err != nil {
		t.Fatalf("seed hourly: %v", err)
	}
}

func newDiskDetector(t *testing.T, db *gorm.DB, now time.Time) *DiskForecastDetector {
	t.Helper()
	d := NewDiskForecastDetector(db, settings.NewService(db))
	d.SetNowFn(func() time.Time { return now })
	return d
}

func TestDisk_InsufficientHistory_Skip(t *testing.T) {
	db := openDiskForecastDB(t)
	seedNode(t, db, 1, "n1", "/b1")
	now := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	// Only 24 hours — below default 72
	for i := 0; i < 24; i++ {
		seedHourly(t, db, 1, now.Add(-time.Duration(24-i)*time.Hour), 40+float64(i)*0.1)
	}
	d := newDiskDetector(t, db, now)
	findings, _ := d.Evaluate(context.Background())
	if len(findings) != 0 {
		t.Fatalf("expected 0 findings, got %d", len(findings))
	}
}

func TestDisk_NonIncreasing_Skip(t *testing.T) {
	db := openDiskForecastDB(t)
	seedNode(t, db, 1, "n1", "/b1")
	now := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	// 100 hours of flat data (slope=0)
	for i := 0; i < 100; i++ {
		seedHourly(t, db, 1, now.Add(-time.Duration(100-i)*time.Hour), 50)
	}
	d := newDiskDetector(t, db, now)
	findings, _ := d.Evaluate(context.Background())
	if len(findings) != 0 {
		t.Fatalf("expected 0 findings for flat disk, got %d", len(findings))
	}
}

func TestDisk_NoisyDecline_Skip(t *testing.T) {
	db := openDiskForecastDB(t)
	seedNode(t, db, 1, "n1", "/b1")
	now := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	// 100 hours of declining disk (cleanup in progress)
	for i := 0; i < 100; i++ {
		seedHourly(t, db, 1, now.Add(-time.Duration(100-i)*time.Hour), 80-float64(i)*0.1)
	}
	d := newDiskDetector(t, db, now)
	findings, _ := d.Evaluate(context.Background())
	if len(findings) != 0 {
		t.Fatalf("expected 0 findings for declining disk, got %d", len(findings))
	}
}

func TestDisk_SlowGrowth_NoAlert(t *testing.T) {
	db := openDiskForecastDB(t)
	seedNode(t, db, 1, "n1", "/b1")
	now := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	// 14 days × 24h = 336 hourly points, growing 0.01%/hour → ~30 days to full (above threshold 7)
	for i := 0; i < 336; i++ {
		seedHourly(t, db, 1, now.Add(-time.Duration(336-i)*time.Hour), 50+float64(i)*0.01)
	}
	d := newDiskDetector(t, db, now)
	findings, _ := d.Evaluate(context.Background())
	if len(findings) != 0 {
		t.Fatalf("expected 0 findings for slow growth, got %d", len(findings))
	}
}

func TestDisk_FastGrowth_Warning(t *testing.T) {
	db := openDiskForecastDB(t)
	seedNode(t, db, 1, "n1", "/b1")
	now := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	// 100 hourly points, 0.15%/h growth starting at 70.
	// currentY ≈ 85%, rate=3.6%/day → ~4.2 days to full (within 7-day threshold → warning).
	for i := 0; i < 100; i++ {
		seedHourly(t, db, 1, now.Add(-time.Duration(100-i)*time.Hour), 70+float64(i)*0.15)
	}
	d := newDiskDetector(t, db, now)
	findings, _ := d.Evaluate(context.Background())
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	f := findings[0]
	if f.Detector != "disk_forecast" || f.Metric != "disk_pct" {
		t.Fatalf("unexpected detector/metric: %+v", f)
	}
	if f.ForecastDays == nil || *f.ForecastDays <= 0 {
		t.Fatalf("forecast_days missing or negative")
	}
	if f.Severity != "warning" && f.Severity != "critical" {
		t.Fatalf("severity=%s, expected warning/critical", f.Severity)
	}
	if f.ErrorCode != "XR-DISKFORECAST-1" {
		t.Fatalf("error code=%s", f.ErrorCode)
	}
}

func TestDisk_VeryFastGrowth_Critical(t *testing.T) {
	db := openDiskForecastDB(t)
	seedNode(t, db, 1, "n1", "/b1")
	now := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	// Growing ~17%/day → currentY ~92, slope = ~17%/day → ~0.5 days to 100
	for i := 0; i < 100; i++ {
		seedHourly(t, db, 1, now.Add(-time.Duration(100-i)*time.Hour), 20+float64(i)*0.75)
	}
	d := newDiskDetector(t, db, now)
	findings, _ := d.Evaluate(context.Background())
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	if findings[0].Severity != "critical" {
		t.Fatalf("severity=%s, expected critical", findings[0].Severity)
	}
}

func TestDisk_MultipleNodes_Independent(t *testing.T) {
	db := openDiskForecastDB(t)
	seedNode(t, db, 1, "n1", "/b1")
	seedNode(t, db, 2, "n2", "/b2")
	now := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	// n1 fast-growing: same as TestDisk_FastGrowth_Warning → expect 1 finding
	for i := 0; i < 100; i++ {
		seedHourly(t, db, 1, now.Add(-time.Duration(100-i)*time.Hour), 70+float64(i)*0.15)
	}
	// n2 flat — no finding
	for i := 0; i < 100; i++ {
		seedHourly(t, db, 2, now.Add(-time.Duration(100-i)*time.Hour), 50)
	}
	d := newDiskDetector(t, db, now)
	findings, _ := d.Evaluate(context.Background())
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding (only n1), got %d", len(findings))
	}
	if findings[0].NodeID != 1 {
		t.Fatalf("expected node 1, got %d", findings[0].NodeID)
	}
}
