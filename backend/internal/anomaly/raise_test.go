package anomaly

import (
	"context"
	"testing"

	"xirang/backend/internal/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openRaiseDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(&model.Node{}, &model.Alert{}, &model.AnomalyEvent{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// Seed node 1 for FK satisfaction
	db.Exec("INSERT INTO nodes (id, name, host, username, backup_dir) VALUES (1, 'n', 'h', 'u', '/b')")
	return db
}

func TestRaise_NewFinding_WritesEventAndLinksAlert(t *testing.T) {
	db := openRaiseDB(t)
	raiser := func(_ *gorm.DB, _ uint, _, _, _ string) (uint, bool, error) {
		return 42, true, nil
	}
	fn := NewRaiseFn(db, raiser)
	sigma := 3.5
	f := Finding{NodeID: 1, Detector: "ewma", Metric: "cpu_pct", Severity: "warning",
		ObservedValue: 85, BaselineValue: 30, Sigma: &sigma,
		ErrorCode: "XR-ANOMALY-CPU-1", Message: "test",
		Details: map[string]any{"samples": 12},
	}
	if err := fn(context.Background(), f); err != nil {
		t.Fatalf("raise: %v", err)
	}
	var evt model.AnomalyEvent
	if err := db.First(&evt).Error; err != nil {
		t.Fatalf("no event: %v", err)
	}
	if evt.AlertID == nil || *evt.AlertID != 42 {
		t.Fatalf("alert_id: %+v", evt.AlertID)
	}
	if !evt.RaisedAlert {
		t.Fatal("expected RaisedAlert=true")
	}
	dm := evt.DecodedDetails()
	if dm["samples"] != float64(12) {
		t.Fatalf("details missing: %+v", dm)
	}
}

func TestRaise_DedupFinding_EventStillWritten_RaisedAlertFalse(t *testing.T) {
	db := openRaiseDB(t)
	raiser := func(_ *gorm.DB, _ uint, _, _, _ string) (uint, bool, error) {
		return 99, false, nil // deduped to existing alert 99
	}
	fn := NewRaiseFn(db, raiser)
	f := Finding{NodeID: 1, Detector: "ewma", Metric: "cpu_pct", Severity: "warning",
		ObservedValue: 85, BaselineValue: 30,
		ErrorCode: "XR-ANOMALY-CPU-1", Message: "test",
	}
	if err := fn(context.Background(), f); err != nil {
		t.Fatalf("raise: %v", err)
	}
	var evt model.AnomalyEvent
	db.First(&evt)
	if evt.RaisedAlert {
		t.Fatal("expected RaisedAlert=false")
	}
	if evt.AlertID == nil || *evt.AlertID != 99 {
		t.Fatalf("alert_id should be existing 99, got %+v", evt.AlertID)
	}
}

func TestRaise_AlertError_EventStillWritten(t *testing.T) {
	db := openRaiseDB(t)
	raiser := func(_ *gorm.DB, _ uint, _, _, _ string) (uint, bool, error) {
		return 0, false, gorm.ErrInvalidDB
	}
	fn := NewRaiseFn(db, raiser)
	f := Finding{NodeID: 1, Detector: "ewma", Metric: "cpu_pct", Severity: "warning",
		ErrorCode: "XR-ANOMALY-CPU-1", Message: "test",
	}
	err := fn(context.Background(), f)
	if err == nil {
		t.Fatal("expected propagated alert error")
	}
	var n int64
	db.Model(&model.AnomalyEvent{}).Count(&n)
	if n != 1 {
		t.Fatalf("event should still be written, count=%d", n)
	}
}
