package slo

import (
	"testing"
	"time"

	"xirang/backend/internal/model"
)

func TestEvaluator_RaisesBreachWhenBurnRateOverTwo(t *testing.T) {
	db := openSLOTestDB(t)
	db.Create(&model.Node{ID: 1, Name: "n1", Tags: "prod"})
	now := time.Now().UTC().Truncate(time.Hour)
	// Window has enough samples to avoid insufficient_data (≥100 total).
	// Last 1h is all failures → burn rate very high.
	for h := 1; h < 28*24; h++ {
		db.Create(&model.NodeMetricSampleHourly{NodeID: 1, BucketStart: now.Add(-time.Duration(h) * time.Hour), ProbeOK: 10, ProbeFail: 0, SampleCount: 10})
	}
	db.Create(&model.NodeMetricSampleHourly{NodeID: 1, BucketStart: now.Add(-30 * time.Minute), ProbeOK: 0, ProbeFail: 100, SampleCount: 100})
	// BurnRate1h reads the raw table; seed 1h worth of failing probes so
	// the 1h burn clears the min-sample threshold and reports breach.
	for i := 0; i < 20; i++ {
		db.Create(&model.NodeMetricSample{NodeID: 1, SampledAt: now.Add(-time.Duration(i+1) * 3 * time.Minute), ProbeOK: false})
	}
	def := &model.SLODefinition{ID: 1, Name: "prod", MetricType: "availability", Threshold: 0.99, WindowDays: 28, Enabled: true, CreatedBy: 1}
	db.Create(def)

	var raised []uint
	w := NewEvaluator(db)
	w.raiseFn = func(_ any, slo *model.SLODefinition, c *Compliance) error {
		raised = append(raised, slo.ID)
		return nil
	}
	w.evaluateAll(now)
	if len(raised) != 1 || raised[0] != 1 {
		t.Fatalf("expected breach raised for SLO 1, got %v", raised)
	}
}

func TestEvaluator_SkipsHealthySLO(t *testing.T) {
	db := openSLOTestDB(t)
	db.Create(&model.Node{ID: 1, Tags: "prod"})
	now := time.Now().UTC().Truncate(time.Hour)
	for h := 0; h < 28*24; h++ {
		db.Create(&model.NodeMetricSampleHourly{NodeID: 1, BucketStart: now.Add(-time.Duration(h) * time.Hour), ProbeOK: 10, ProbeFail: 0, SampleCount: 10})
	}
	def := &model.SLODefinition{ID: 1, MetricType: "availability", Threshold: 0.99, WindowDays: 28, Enabled: true, CreatedBy: 1}
	db.Create(def)

	var raised []uint
	w := NewEvaluator(db)
	w.raiseFn = func(_ any, slo *model.SLODefinition, c *Compliance) error {
		raised = append(raised, slo.ID)
		return nil
	}
	w.evaluateAll(now)
	if len(raised) != 0 {
		t.Fatalf("healthy SLO must not raise breach, got %v", raised)
	}
}

func TestEvaluator_SkipsDisabled(t *testing.T) {
	db := openSLOTestDB(t)
	db.Create(&model.Node{ID: 1, Tags: "prod"})
	now := time.Now().UTC().Truncate(time.Hour)
	for h := 0; h < 100; h++ {
		db.Create(&model.NodeMetricSampleHourly{NodeID: 1, BucketStart: now.Add(-time.Duration(h) * time.Hour), ProbeOK: 0, ProbeFail: 10, SampleCount: 10})
	}
	// Use raw SQL to bypass GORM's zero-value skip + SQLite column default:true.
	db.Exec("INSERT INTO slo_definitions (id,name,metric_type,threshold,window_days,enabled,created_by,created_at,updated_at) VALUES (1,'disabled-slo','availability',0.99,28,0,1,datetime('now'),datetime('now'))")

	var raised []uint
	w := NewEvaluator(db)
	w.raiseFn = func(_ any, slo *model.SLODefinition, c *Compliance) error { raised = append(raised, slo.ID); return nil }
	w.evaluateAll(now)
	if len(raised) != 0 {
		t.Fatalf("disabled SLO must not raise breach, got %v", raised)
	}
}

func TestEvaluator_SkipsInsufficient(t *testing.T) {
	db := openSLOTestDB(t)
	db.Create(&model.Node{ID: 1, Tags: "prod"})
	now := time.Now().UTC().Truncate(time.Hour)
	// Only 5 samples — insufficient_data.
	db.Create(&model.NodeMetricSampleHourly{NodeID: 1, BucketStart: now.Add(-30 * time.Minute), ProbeOK: 0, ProbeFail: 5, SampleCount: 5})
	def := &model.SLODefinition{ID: 1, MetricType: "availability", Threshold: 0.99, WindowDays: 28, Enabled: true, CreatedBy: 1}
	db.Create(def)

	var raised []uint
	w := NewEvaluator(db)
	w.raiseFn = func(_ any, slo *model.SLODefinition, c *Compliance) error { raised = append(raised, slo.ID); return nil }
	w.evaluateAll(now)
	if len(raised) != 0 {
		t.Fatalf("insufficient_data must not raise, got %v", raised)
	}
}
