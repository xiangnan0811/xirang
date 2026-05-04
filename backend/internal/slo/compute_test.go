package slo

import (
	"testing"
	"time"

	"xirang/backend/internal/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openSLOTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared&_loc=UTC"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(
		&model.Node{},
		&model.Task{},
		&model.TaskRun{},
		&model.NodeMetricSample{},
		&model.NodeMetricSampleHourly{},
		&model.SLODefinition{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// seedRawSamples fills node_metric_samples for the 1h burn-rate query.
// computeAvailability reads the most recent 60 minutes from the raw table
// (not the hourly rollup) because the rollup has a 5-minute cushion, so
// SLO tests that only populate hourly would fail with "no such table" or
// bogus BurnRate1h values.
func seedRawSamples(db *gorm.DB, nodeID uint, now time.Time, probeOK bool, count int) {
	step := time.Hour / time.Duration(count)
	for i := 0; i < count; i++ {
		db.Create(&model.NodeMetricSample{
			NodeID:    nodeID,
			SampledAt: now.Add(-time.Duration(i+1) * step),
			ProbeOK:   probeOK,
		})
	}
}

func TestComputeAvailability_AllOK(t *testing.T) {
	db := openSLOTestDB(t)
	db.Create(&model.Node{ID: 1, Name: "n1", Tags: "prod"})
	now := time.Now().UTC().Truncate(time.Hour)
	for h := 0; h < 28*24; h++ {
		db.Create(&model.NodeMetricSampleHourly{
			NodeID:      1,
			BucketStart: now.Add(-time.Duration(h) * time.Hour),
			ProbeOK:     10,
			ProbeFail:   0,
			SampleCount: 10,
		})
	}
	seedRawSamples(db, 1, now, true, 10)
	def := &model.SLODefinition{ID: 1, Name: "prod availability", MetricType: "availability", Threshold: 0.99, WindowDays: 28}
	c, err := Compute(db, def, now)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if c.Observed < 0.999 {
		t.Fatalf("expected Observed ≈ 1.0, got %f", c.Observed)
	}
	if c.Status != StatusHealthy {
		t.Fatalf("expected Healthy, got %q", c.Status)
	}
	if c.BurnRate1h != 0 {
		t.Fatalf("expected BurnRate1h=0, got %f", c.BurnRate1h)
	}
}

func TestComputeAvailability_BelowThreshold(t *testing.T) {
	db := openSLOTestDB(t)
	db.Create(&model.Node{ID: 1, Name: "n1", Tags: "prod"})
	now := time.Now().UTC().Truncate(time.Hour)
	for h := 0; h < 28*24; h++ {
		db.Create(&model.NodeMetricSampleHourly{
			NodeID:      1,
			BucketStart: now.Add(-time.Duration(h) * time.Hour),
			ProbeOK:     9,
			ProbeFail:   1,
			SampleCount: 10,
		})
	}
	// 1h raw samples fail at 9/10 so BurnRate1h matches the SLO violation.
	for i := 0; i < 9; i++ {
		db.Create(&model.NodeMetricSample{NodeID: 1, SampledAt: now.Add(-time.Duration(i+1) * 6 * time.Minute), ProbeOK: true})
	}
	db.Create(&model.NodeMetricSample{NodeID: 1, SampledAt: now.Add(-54 * time.Minute), ProbeOK: false})
	def := &model.SLODefinition{ID: 1, MetricType: "availability", Threshold: 0.99, WindowDays: 28}
	c, err := Compute(db, def, now)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if c.Observed > 0.92 || c.Observed < 0.88 {
		t.Fatalf("expected Observed ≈ 0.9, got %f", c.Observed)
	}
	if c.Status != StatusBreached {
		t.Fatalf("expected Breached, got %q", c.Status)
	}
}

func TestComputeAvailability_InsufficientData(t *testing.T) {
	db := openSLOTestDB(t)
	db.Create(&model.Node{ID: 1, Name: "n1", Tags: "prod"})
	now := time.Now().UTC().Truncate(time.Hour)
	for h := 0; h < 5; h++ {
		db.Create(&model.NodeMetricSampleHourly{
			NodeID:      1,
			BucketStart: now.Add(-time.Duration(h) * time.Hour),
			ProbeOK:     1,
			ProbeFail:   0,
			SampleCount: 1,
		})
	}
	def := &model.SLODefinition{ID: 1, MetricType: "availability", Threshold: 0.99, WindowDays: 28}
	c, _ := Compute(db, def, now)
	if c.Status != StatusInsufficient {
		t.Fatalf("expected Insufficient, got %q", c.Status)
	}
}

func TestComputeAvailability_TagFilter(t *testing.T) {
	db := openSLOTestDB(t)
	db.Create(&model.Node{ID: 1, Name: "n1", Tags: "prod,web", BackupDir: "/b1"})
	db.Create(&model.Node{ID: 2, Name: "n2", Tags: "staging", BackupDir: "/b2"})
	now := time.Now().UTC().Truncate(time.Hour)
	for h := 0; h < 28*24; h++ {
		db.Create(&model.NodeMetricSampleHourly{NodeID: 1, BucketStart: now.Add(-time.Duration(h) * time.Hour), ProbeOK: 10, ProbeFail: 0, SampleCount: 10})
		db.Create(&model.NodeMetricSampleHourly{NodeID: 2, BucketStart: now.Add(-time.Duration(h) * time.Hour), ProbeOK: 0, ProbeFail: 10, SampleCount: 10})
	}
	def := &model.SLODefinition{ID: 1, MetricType: "availability", MatchTags: `["prod"]`, Threshold: 0.99, WindowDays: 28}
	c, _ := Compute(db, def, now)
	if c.Status != StatusHealthy {
		t.Fatalf("expected Healthy (prod only is healthy), got %q Observed=%f", c.Status, c.Observed)
	}
}

// TestComputeAvailability_Warning verifies StatusWarning fires when observed >= threshold
// but error budget remaining < 20%.
//
// Seed: 672 buckets × 30 samples = 20160 total. Every 4th bucket has 1 fail → 168 fails.
// observed = 19992/20160 ≈ 0.9917. budget=1%, consumed≈83%, remaining≈17% < 20% → Warning.
func TestComputeAvailability_Warning(t *testing.T) {
	db := openSLOTestDB(t)
	db.Create(&model.Node{ID: 1, Name: "n1", Tags: "prod", BackupDir: "/bw"})
	now := time.Now().UTC().Truncate(time.Hour)
	for h := 0; h < 28*24; h++ {
		fails := int64(0)
		if h%4 == 0 {
			fails = 1
		}
		ok := int64(30) - fails
		db.Create(&model.NodeMetricSampleHourly{
			NodeID:      1,
			BucketStart: now.Add(-time.Duration(h) * time.Hour),
			ProbeOK:     ok,
			ProbeFail:   fails,
			SampleCount: 30,
		})
	}
	def := &model.SLODefinition{ID: 1, Name: "prod availability warning", MetricType: "availability", Threshold: 0.99, WindowDays: 28}
	c, err := Compute(db, def, now)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	// observed should be above threshold (healthy range) but budget running low
	if c.Observed < 0.99 {
		t.Fatalf("expected Observed >= 0.99 (above threshold), got %f", c.Observed)
	}
	if c.Observed >= 0.995 {
		t.Fatalf("expected Observed < 0.995 (budget nearly consumed), got %f", c.Observed)
	}
	if c.Status != StatusWarning {
		t.Fatalf("expected StatusWarning (budget remaining < 20%%), got %q (observed=%f, budgetRemaining=%f%%)", c.Status, c.Observed, c.ErrorBudgetRemainingPct)
	}
}

func TestComputeSuccessRate_AllOK(t *testing.T) {
	db := openSLOTestDB(t)
	db.Create(&model.Node{ID: 1, Name: "n1", Tags: "prod"})
	db.Create(&model.Task{ID: 1, Name: "t1", NodeID: 1, Status: "success"})
	now := time.Now().UTC()
	for i := 0; i < 200; i++ {
		db.Create(&model.TaskRun{TaskID: 1, Status: "success", CreatedAt: now.Add(-time.Duration(i) * time.Minute)})
	}
	def := &model.SLODefinition{ID: 1, MetricType: "success_rate", Threshold: 0.99, WindowDays: 28}
	c, _ := Compute(db, def, now)
	if c.Status != StatusHealthy {
		t.Fatalf("expected Healthy, got %q Observed=%f", c.Status, c.Observed)
	}
}

func TestComputeSuccessRate_BelowThreshold(t *testing.T) {
	db := openSLOTestDB(t)
	db.Create(&model.Node{ID: 1, Name: "n1", Tags: "prod"})
	db.Create(&model.Task{ID: 1, NodeID: 1, Status: "failed"})
	now := time.Now().UTC()
	for i := 0; i < 180; i++ {
		db.Create(&model.TaskRun{TaskID: 1, Status: "success", CreatedAt: now.Add(-time.Duration(i) * time.Minute)})
	}
	for i := 0; i < 20; i++ {
		db.Create(&model.TaskRun{TaskID: 1, Status: "failed", CreatedAt: now.Add(-time.Duration(i+200) * time.Minute)})
	}
	def := &model.SLODefinition{ID: 1, MetricType: "success_rate", Threshold: 0.99, WindowDays: 28}
	c, _ := Compute(db, def, now)
	if c.Status != StatusBreached {
		t.Fatalf("expected Breached, got %q Observed=%f", c.Status, c.Observed)
	}
}
