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
		&model.SLODefinition{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
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
