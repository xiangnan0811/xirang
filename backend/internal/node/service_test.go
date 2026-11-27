package node

import (
	"context"
	"testing"
	"time"

	"xirang/backend/internal/model"
	"xirang/backend/internal/repository"
	gormrepo "xirang/backend/internal/repository/gorm"
	"xirang/backend/internal/secure"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type probeDuringNodeUpdate struct {
	repository.NodeRepository
	db   *gorm.DB
	seen time.Time
}

func (r *probeDuringNodeUpdate) Update(ctx context.Context, node *model.Node, updateStatus bool) error {
	if err := r.db.WithContext(ctx).Model(&model.Node{}).Where("id = ?", node.ID).Updates(map[string]any{
		"status": "online", "connection_latency": 42, "last_seen_at": r.seen,
	}).Error; err != nil {
		return err
	}
	return r.NodeRepository.Update(ctx, node, updateStatus)
}

func TestNodeUpdatePreservesProbeAndOmittedDefaults(t *testing.T) {
	t.Setenv("DATA_ENCRYPTION_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
	db, err := gorm.Open(sqlite.Open("file:node_service_update?mode=memory&cache=shared"), &gorm.Config{})
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
	repo := gormrepo.NewNodeRepository(db)
	ctx := context.Background()
	existing := &model.Node{Name: "node", Host: "old-host", Port: 2222, Username: "root", AuthType: "password", Password: "secret", BackupDir: "node", BasePath: "/backup", Status: "offline"}
	if err := repo.Create(ctx, existing); err != nil {
		t.Fatal(err)
	}
	seen := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	service := NewNodeService(&probeDuringNodeUpdate{NodeRepository: repo, db: db, seen: seen})
	input := CreateNodeInput{Name: "node", Host: "new-host", Username: "root"}
	updated, _, err := service.Update(ctx, existing.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != "online" || updated.ConnectionLatency != 42 || updated.LastSeenAt == nil || !updated.LastSeenAt.Equal(seen) {
		t.Fatalf("probe result overwritten: %#v", updated.Sanitized())
	}
	if updated.Port != 22 || updated.AuthType != "password" || updated.Password != "secret" || updated.BasePath != "/backup" || updated.BackupDir != "node" {
		t.Fatalf("omitted defaults changed: %#v", updated.Sanitized())
	}
	input.Status = "maintenance"
	updated, _, err = service.Update(ctx, existing.ID, input)
	if err != nil || updated.Status != "maintenance" || updated.ConnectionLatency != 42 {
		t.Fatalf("explicit status failed: %v", err)
	}
}
