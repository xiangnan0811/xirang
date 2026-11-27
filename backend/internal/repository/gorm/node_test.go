package gorm

import (
	"context"
	"errors"
	"testing"
	"time"

	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestNodeUpdatePreservesConcurrentHealthAndDoesNotResurrect(t *testing.T) {
	t.Setenv("DATA_ENCRYPTION_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
	db, err := gorm.Open(sqlite.Open("file:node_update_scoped?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&model.SSHKey{}, &model.Node{}); err != nil {
		t.Fatal(err)
	}
	repo := NewNodeRepository(db)
	ctx := context.Background()
	original := &model.Node{Name: "node", Host: "old-host", Username: "root", BackupDir: "node", AuthType: "password", Password: "secret", Status: "offline"}
	if err := repo.Create(ctx, original); err != nil {
		t.Fatal(err)
	}
	stale, err := repo.FindByID(ctx, original.ID)
	if err != nil {
		t.Fatal(err)
	}
	seen := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := db.Model(&model.Node{}).Where("id = ?", original.ID).Updates(map[string]any{"status": "online", "connection_latency": 25, "last_seen_at": seen}).Error; err != nil {
		t.Fatal(err)
	}
	stale.Host = "new-host"
	if err := repo.Update(ctx, stale, false); err != nil {
		t.Fatal(err)
	}
	updated, err := repo.FindByID(ctx, original.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Host != "new-host" || updated.Status != "online" || updated.ConnectionLatency != 25 || updated.LastSeenAt == nil || !updated.LastSeenAt.Equal(seen) || updated.Password != "secret" {
		t.Fatalf("unexpected updated node: %#v", updated.Sanitized())
	}
	var stored string
	if err := db.Table("nodes").Select("password").Where("id = ?", original.ID).Scan(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored == "secret" || stored == "" {
		t.Fatal("update failed to encrypt password")
	}
	updated.Status = "maintenance"
	if err := repo.Update(ctx, updated, true); err != nil {
		t.Fatal(err)
	}
	updated, err = repo.FindByID(ctx, original.ID)
	if err != nil || updated.Status != "maintenance" {
		t.Fatalf("explicit status not saved: %v", err)
	}
	if err := db.Delete(&model.Node{}, original.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := repo.Update(ctx, updated, false); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("deleted update returned %v", err)
	}
	var count int64
	if err := db.Model(&model.Node{}).Where("id = ?", original.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("deleted node was recreated: %d %v", count, err)
	}
}
