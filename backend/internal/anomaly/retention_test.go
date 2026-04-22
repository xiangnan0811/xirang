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

func openRetentionDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(&model.Node{}, &model.AnomalyEvent{}, &model.SystemSetting{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	db.Exec("INSERT INTO nodes (id, name, host, username, backup_dir) VALUES (1, 'n', 'h', 'u', '/b')")
	return db
}

func TestRetention_DeletesOld_KeepsRecent(t *testing.T) {
	db := openRetentionDB(t)
	now := time.Now().UTC()
	// Old event (40 days ago)
	db.Create(&model.AnomalyEvent{
		NodeID: 1, Detector: "ewma", Metric: "cpu_pct", Severity: "warning",
		ObservedValue: 85, BaselineValue: 30, Details: "{}",
		FiredAt: now.AddDate(0, 0, -40),
	})
	// Recent event (2 days ago)
	db.Create(&model.AnomalyEvent{
		NodeID: 1, Detector: "ewma", Metric: "mem_pct", Severity: "warning",
		ObservedValue: 85, BaselineValue: 30, Details: "{}",
		FiredAt: now.AddDate(0, 0, -2),
	})
	w := NewRetentionWorker(db, settings.NewService(db))
	w.Prune(context.Background())
	var count int64
	db.Model(&model.AnomalyEvent{}).Count(&count)
	if count != 1 {
		t.Fatalf("expected 1 row after prune (recent kept), got %d", count)
	}
	var kept model.AnomalyEvent
	db.First(&kept)
	if kept.Metric != "mem_pct" {
		t.Fatalf("wrong row kept: %+v", kept)
	}
}

func TestRetention_HonorsCustomDays(t *testing.T) {
	db := openRetentionDB(t)
	db.Create(&model.SystemSetting{Key: "anomaly.events_retention_days", Value: "7"})
	now := time.Now().UTC()
	db.Create(&model.AnomalyEvent{
		NodeID: 1, Detector: "ewma", Metric: "cpu_pct", Severity: "warning",
		ObservedValue: 85, BaselineValue: 30, Details: "{}",
		FiredAt: now.AddDate(0, 0, -10), // older than 7d
	})
	db.Create(&model.AnomalyEvent{
		NodeID: 1, Detector: "ewma", Metric: "cpu_pct", Severity: "warning",
		ObservedValue: 85, BaselineValue: 30, Details: "{}",
		FiredAt: now.AddDate(0, 0, -3), // newer than 7d
	})
	w := NewRetentionWorker(db, settings.NewService(db))
	w.Prune(context.Background())
	var count int64
	db.Model(&model.AnomalyEvent{}).Count(&count)
	if count != 1 {
		t.Fatalf("expected 1, got %d", count)
	}
}
