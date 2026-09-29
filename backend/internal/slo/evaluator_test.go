package slo

import (
	"testing"
	"time"

	"xirang/backend/internal/model"
)

// recordingSink captures SLO IDs whose RaiseSLOBreach was invoked.
type recordingSink struct {
	raised []uint
}

func (r *recordingSink) RaiseSLOBreach(def *model.SLODefinition, _ *Compliance) error {
	r.raised = append(r.raised, def.ID)
	return nil
}

func TestEvaluator_RaisesBreachWhenSuccessRateBurnRateOverTwo(t *testing.T) {
	db := openSLOTestDB(t)
	db.Create(&model.Node{ID: 1, Name: "n1", Tags: "prod"})
	db.Create(&model.Task{ID: 1, Name: "backup", NodeID: 1})
	now := time.Now().UTC().Truncate(time.Hour)
	for i := range 200 {
		db.Create(&model.TaskRun{TaskID: 1, Status: "failed", CreatedAt: now.Add(-time.Duration(i+1) * time.Minute)})
	}
	def := &model.SLODefinition{ID: 1, Name: "prod", MetricType: "success_rate", Threshold: 0.99, WindowDays: 28, Enabled: true, CreatedBy: 1}
	db.Create(def)

	sink := &recordingSink{}
	w := NewEvaluator(db, sink)
	w.evaluateAll(now)
	if len(sink.raised) != 1 || sink.raised[0] != 1 {
		t.Fatalf("expected breach raised for SLO 1, got %v", sink.raised)
	}
}

func TestEvaluator_SkipsHealthySuccessRateSLO(t *testing.T) {
	db := openSLOTestDB(t)
	db.Create(&model.Node{ID: 1, Tags: "prod"})
	db.Create(&model.Task{ID: 1, Name: "backup", NodeID: 1})
	now := time.Now().UTC().Truncate(time.Hour)
	for i := range 200 {
		db.Create(&model.TaskRun{TaskID: 1, Status: "success", CreatedAt: now.Add(-time.Duration(i+1) * time.Minute)})
	}
	def := &model.SLODefinition{ID: 1, MetricType: "success_rate", Threshold: 0.99, WindowDays: 28, Enabled: true, CreatedBy: 1}
	db.Create(def)

	sink := &recordingSink{}
	w := NewEvaluator(db, sink)
	w.evaluateAll(now)
	if len(sink.raised) != 0 {
		t.Fatalf("healthy SLO must not raise breach, got %v", sink.raised)
	}
}

func TestEvaluator_SkipsDisabled(t *testing.T) {
	db := openSLOTestDB(t)
	db.Create(&model.Node{ID: 1, Tags: "prod"})
	db.Create(&model.Task{ID: 1, Name: "backup", NodeID: 1})
	now := time.Now().UTC().Truncate(time.Hour)
	for i := range 100 {
		db.Create(&model.TaskRun{TaskID: 1, Status: "failed", CreatedAt: now.Add(-time.Duration(i+1) * time.Minute)})
	}
	// Use raw SQL to bypass GORM's zero-value skip + SQLite column default:true.
	db.Exec("INSERT INTO slo_definitions (id,name,metric_type,threshold,window_days,enabled,created_by,created_at,updated_at) VALUES (1,'disabled-slo','success_rate',0.99,28,0,1,datetime('now'),datetime('now'))")

	sink := &recordingSink{}
	w := NewEvaluator(db, sink)
	w.evaluateAll(now)
	if len(sink.raised) != 0 {
		t.Fatalf("disabled SLO must not raise breach, got %v", sink.raised)
	}
}

func TestEvaluator_SkipsInsufficient(t *testing.T) {
	db := openSLOTestDB(t)
	db.Create(&model.Node{ID: 1, Tags: "prod"})
	db.Create(&model.Task{ID: 1, Name: "backup", NodeID: 1})
	now := time.Now().UTC().Truncate(time.Hour)
	for i := range 5 {
		db.Create(&model.TaskRun{TaskID: 1, Status: "failed", CreatedAt: now.Add(-time.Duration(i+1) * time.Minute)})
	}
	def := &model.SLODefinition{ID: 1, MetricType: "success_rate", Threshold: 0.99, WindowDays: 28, Enabled: true, CreatedBy: 1}
	db.Create(def)

	sink := &recordingSink{}
	w := NewEvaluator(db, sink)
	w.evaluateAll(now)
	if len(sink.raised) != 0 {
		t.Fatalf("insufficient_data must not raise, got %v", sink.raised)
	}
}
