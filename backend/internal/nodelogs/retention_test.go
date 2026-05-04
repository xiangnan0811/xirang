package nodelogs

import (
	"testing"
	"time"

	"xirang/backend/internal/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openRetentionTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared&_loc=UTC"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(&model.Node{}, &model.NodeLog{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestRetention_DeletesOldRowsPerNode(t *testing.T) {
	db := openRetentionTestDB(t)
	db.Create(&model.Node{ID: 1, Name: "n1", Host: "h", Username: "u", LogRetentionDays: 7, BackupDir: "/b1"})
	now := time.Now().UTC()
	db.Create(&model.NodeLog{NodeID: 1, Source: "file", Path: "/a", Timestamp: now, Message: "recent", CreatedAt: now})
	db.Create(&model.NodeLog{NodeID: 1, Source: "file", Path: "/a", Timestamp: now, Message: "old", CreatedAt: now.Add(-30 * 24 * time.Hour)})

	w := NewRetentionWorker(db)
	w.pruneNode(model.Node{ID: 1, LogRetentionDays: 7}, 30)

	var msgs []string
	db.Model(&model.NodeLog{}).Where("node_id = ?", 1).Pluck("message", &msgs)
	if len(msgs) != 1 || msgs[0] != "recent" {
		t.Fatalf("kept wrong rows: %+v", msgs)
	}
}

func TestRetention_FallsBackToGlobalDefault(t *testing.T) {
	db := openRetentionTestDB(t)
	db.Create(&model.Node{ID: 1, Name: "n1", Host: "h", Username: "u", LogRetentionDays: 0, BackupDir: "/b1"})
	now := time.Now().UTC()
	db.Create(&model.NodeLog{NodeID: 1, Source: "file", Path: "/a", Timestamp: now, Message: "recent", CreatedAt: now})
	db.Create(&model.NodeLog{NodeID: 1, Source: "file", Path: "/a", Timestamp: now, Message: "old", CreatedAt: now.Add(-60 * 24 * time.Hour)})

	w := NewRetentionWorker(db)
	w.pruneNode(model.Node{ID: 1, LogRetentionDays: 0}, 30)

	var count int64
	db.Model(&model.NodeLog{}).Where("node_id = ?", 1).Count(&count)
	if count != 1 {
		t.Fatalf("count=%d want 1", count)
	}
}

func TestRetention_KeepsFresh(t *testing.T) {
	db := openRetentionTestDB(t)
	db.Create(&model.Node{ID: 1, Name: "n1", Host: "h", Username: "u", LogRetentionDays: 30, BackupDir: "/b1"})
	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		db.Create(&model.NodeLog{NodeID: 1, Source: "file", Path: "/a", Timestamp: now, Message: "m", CreatedAt: now.Add(-time.Duration(i) * time.Hour)})
	}
	w := NewRetentionWorker(db)
	w.pruneNode(model.Node{ID: 1, LogRetentionDays: 30}, 30)

	var count int64
	db.Model(&model.NodeLog{}).Count(&count)
	if count != 3 {
		t.Fatalf("count=%d want 3", count)
	}
}
