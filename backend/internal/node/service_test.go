package node

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/apperr"
	"xirang/backend/internal/model"
	"xirang/backend/internal/nodeprobe"
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

func (r *probeDuringNodeUpdate) Update(ctx context.Context, node *model.Node, options repository.NodeUpdateOptions) error {
	if err := r.db.WithContext(ctx).Model(&model.Node{}).Where("id = ?", node.ID).Updates(map[string]any{
		"status": "online", "connection_latency": 42, "last_seen_at": r.seen,
	}).Error; err != nil {
		return err
	}
	return r.NodeRepository.Update(ctx, node, options)
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
	input.Status = "warning"
	updated, _, err = service.Update(ctx, existing.ID, input)
	if err != nil || updated.Status != "warning" || updated.ConnectionLatency != 42 {
		t.Fatalf("explicit status failed: %v", err)
	}
}

type interveningTimeUpdate struct {
	repository.NodeRepository
	db *gorm.DB
}

func (r *interveningTimeUpdate) Update(ctx context.Context, node *model.Node, options repository.NodeUpdateOptions) error {
	start := time.Date(2026, 2, 1, 10, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	if err := r.db.WithContext(ctx).Model(&model.Node{}).Where("id = ?", node.ID).Updates(map[string]any{
		"maintenance_start": start, "maintenance_end": end,
	}).Error; err != nil {
		return err
	}
	return r.NodeRepository.Update(ctx, node, options)
}

func TestNodeOptionalTimesMergeAndValidation(t *testing.T) {
	t.Setenv("DATA_ENCRYPTION_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
	db, err := gorm.Open(sqlite.Open("file:node_optional_times?mode=memory&cache=shared"), &gorm.Config{})
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
	ctx := context.Background()
	repo := gormrepo.NewNodeRepository(db)
	service := NewNodeService(repo)
	start, end, expiry := "2026-02-01T14:00:00+02:00", "2026-02-01T15:00:00+02:00", "2020-01-01T00:00:00.123456789Z"
	input := CreateNodeInput{Name: "times", Host: "node.example", Username: "root", AuthType: "password", Password: "secret", MaintenanceStart: &start, MaintenanceEnd: &end, ExpiryDate: &expiry}
	created, err := service.Create(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if created.MaintenanceStart == nil || !created.MaintenanceStart.Equal(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)) || created.ExpiryDate == nil || created.ExpiryDate.Nanosecond() != 123456000 {
		t.Fatalf("unexpected normalized times: %#v", created.Sanitized())
	}
	bad := "private-invalid-value"
	input.ExpiryDate = &bad
	if _, err := service.Create(ctx, input); !errors.Is(err, apperr.ErrValidation) || !strings.Contains(err.Error(), "expiry_date") || strings.Contains(err.Error(), bad) {
		t.Fatalf("invalid expiry returned %v", err)
	}
	input.MaintenanceStart = nil
	input.MaintenanceEnd = nil
	input.ExpiryDate = nil
	input.Host = "new.example"
	newEnd := "2026-02-01T10:30:00Z"
	input.MaintenanceEnd = &newEnd
	service = NewNodeService(&interveningTimeUpdate{NodeRepository: repo, db: db})
	updated, _, err := service.Update(ctx, created.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if updated.MaintenanceStart == nil || !updated.MaintenanceStart.Equal(time.Date(2026, 2, 1, 10, 0, 0, 0, time.UTC)) || updated.MaintenanceEnd == nil || !updated.MaintenanceEnd.Equal(time.Date(2026, 2, 1, 10, 30, 0, 0, time.UTC)) || updated.ExpiryDate == nil || !updated.ExpiryDate.Equal(*created.ExpiryDate) {
		t.Fatalf("update failed to merge current times: %#v", updated.Sanitized())
	}
	service = NewNodeService(repo)
	invalidEnd := "2026-02-01T09:00:00Z"
	input.MaintenanceEnd = &invalidEnd
	input.Host = "should-not-save.example"
	if _, _, err := service.Update(ctx, created.ID, input); !errors.Is(err, apperr.ErrValidation) {
		t.Fatalf("invalid pair returned %v", err)
	}
	stored, err := repo.FindByID(ctx, created.ID)
	if err != nil || stored.Host != "new.example" || stored.MaintenanceEnd == nil || !stored.MaintenanceEnd.Equal(*updated.MaintenanceEnd) {
		t.Fatalf("invalid update changed stored node: %#v, %v", stored, err)
	}
}

func TestNodeOptionalTimesLegacyRepairAndYearBoundary(t *testing.T) {
	t.Setenv("DATA_ENCRYPTION_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
	db, err := gorm.Open(sqlite.Open("file:node_legacy_times?mode=memory&cache=shared"), &gorm.Config{})
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
	ctx := context.Background()
	repo := gormrepo.NewNodeRepository(db)
	legacyStart := time.Date(2026, 3, 1, 12, 0, 0, 123456789, time.UTC)
	node := &model.Node{Name: "legacy", Host: "node.example", Username: "root", BackupDir: "legacy", AuthType: "password", Password: "secret", MaintenanceStart: &legacyStart}
	if err := repo.Create(ctx, node); err != nil {
		t.Fatal(err)
	}
	service := NewNodeService(repo)
	repeat := "2026-03-01T13:00:00.123456999+01:00"
	input := CreateNodeInput{Name: node.Name, Host: node.Host, Username: node.Username, MaintenanceStart: &repeat}
	updated, _, err := service.Update(ctx, node.ID, input)
	if err != nil || updated.MaintenanceStart == nil || updated.MaintenanceStart.Nanosecond() != legacyStart.Nanosecond() || updated.MaintenanceEnd != nil {
		t.Fatalf("legacy repeat was not preserved: %#v, %v", updated, err)
	}
	clear := ""
	input.MaintenanceStart = &clear
	updated, _, err = service.Update(ctx, node.ID, input)
	if err != nil || updated.MaintenanceStart != nil || updated.MaintenanceEnd != nil {
		t.Fatalf("legacy partial pair was not cleared: %#v, %v", updated, err)
	}
	for _, boundary := range []string{"0000-01-01T00:00:00+01:00", "9999-12-31T23:59:59-01:00"} {
		input.MaintenanceStart = nil
		input.ExpiryDate = &boundary
		if _, _, err := service.Update(ctx, node.ID, input); !errors.Is(err, apperr.ErrValidation) || !strings.Contains(err.Error(), "expiry_date") {
			t.Fatalf("out-of-range UTC year returned %v", err)
		}
	}
}

type repairBasePathDuringUpdate struct {
	repository.NodeRepository
	db *gorm.DB
}

func (r *repairBasePathDuringUpdate) Update(ctx context.Context, node *model.Node, options repository.NodeUpdateOptions) error {
	if err := r.db.WithContext(ctx).Model(&model.Node{}).Where("id = ?", node.ID).Update("base_path", "/repaired").Error; err != nil {
		return err
	}
	return r.NodeRepository.Update(ctx, node, options)
}

func TestNodeBasePathValidationAndLegacyRetention(t *testing.T) {
	t.Setenv("DATA_ENCRYPTION_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
	db, err := gorm.Open(sqlite.Open("file:node_base_path_service?mode=memory&cache=shared"), &gorm.Config{})
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
	ctx := context.Background()
	repo := gormrepo.NewNodeRepository(db)
	service := NewNodeService(repo)
	input := CreateNodeInput{Name: "path-node", Host: "node.example", Username: "root", AuthType: "password", Password: "secret", BasePath: "/a//./b/"}
	created, err := service.Create(ctx, input)
	if err != nil || created.BasePath != input.BasePath {
		t.Fatalf("create did not preserve path: %#v, %v", created, err)
	}
	input.BasePath = "/a/../invalid"
	if _, err := service.Create(ctx, input); !errors.Is(err, apperr.ErrValidation) || strings.Contains(err.Error(), input.BasePath) {
		t.Fatalf("invalid create returned %v", err)
	}
	if _, _, err := service.Update(ctx, created.ID, input); !errors.Is(err, apperr.ErrValidation) {
		t.Fatalf("invalid update returned %v", err)
	}
	input.BasePath = "/fixed//path/"
	updated, _, err := service.Update(ctx, created.ID, input)
	if err != nil || updated.BasePath != input.BasePath {
		t.Fatalf("valid update did not preserve path: %#v, %v", updated, err)
	}
	legacy := " relative/legacy "
	for _, echoed := range []bool{false, true} {
		if err := db.Model(&model.Node{}).Where("id = ?", created.ID).Update("base_path", legacy).Error; err != nil {
			t.Fatal(err)
		}
		input.BasePath = ""
		if echoed {
			input.BasePath = legacy
		}
		updated, _, err = service.Update(ctx, created.ID, input)
		if err != nil || updated.BasePath != legacy {
			t.Fatalf("legacy path retention (echo=%v): %#v, %v", echoed, updated, err)
		}
		concurrent := NewNodeService(&repairBasePathDuringUpdate{NodeRepository: repo, db: db})
		updated, _, err = concurrent.Update(ctx, created.ID, input)
		if err != nil || updated.BasePath != "/repaired" {
			t.Fatalf("concurrent path repair (echo=%v): %#v, %v", echoed, updated, err)
		}
	}
}

type changeNodeBeforeLockedUpdate struct {
	repository.NodeRepository
	db *gorm.DB
}

func (r *changeNodeBeforeLockedUpdate) Update(ctx context.Context, node *model.Node, options repository.NodeUpdateOptions) error {
	if err := r.db.WithContext(ctx).Table("nodes").Where("id = ?", node.ID).Updates(map[string]any{
		"backup_dir": "new-backup", "archived": true, "use_sudo": true,
		"config_revision": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}).Error; err != nil {
		return err
	}
	return r.NodeRepository.Update(ctx, node, options)
}

func TestNodeUpdateRevisionAndLockedDefaults(t *testing.T) {
	t.Setenv("DATA_ENCRYPTION_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
	db, err := gorm.Open(sqlite.Open("file:node_revision_service?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&model.SSHKey{}, &model.Node{}, &nodeprobe.State{}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repo := gormrepo.NewNodeRepository(db)
	original := &model.Node{Name: "node", Host: "node.example", Username: "root", AuthType: "password", Password: "secret", BackupDir: "old-backup"}
	if err := repo.Create(ctx, original); err != nil {
		t.Fatal(err)
	}
	service := NewNodeService(&changeNodeBeforeLockedUpdate{NodeRepository: repo, db: db})
	stale := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	input := CreateNodeInput{Name: "node", Host: "invalid/host", Username: "root", ExpectedRevision: &stale}
	if _, _, err := service.Update(ctx, original.ID, input); !errors.Is(err, repository.ErrNodeRevisionConflict) {
		t.Fatalf("stale revision did not precede validation: %v", err)
	}
	input.ExpectedRevision = nil
	input.Host = "node.example"
	updated, oldBackup, err := service.Update(ctx, original.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if oldBackup != "new-backup" || updated.BackupDir != "new-backup" || !updated.Archived || !updated.UseSudo || updated.Password != "secret" {
		t.Fatalf("locked defaults were not retained: old=%q node=%#v", oldBackup, updated.Sanitized())
	}
	plainService := NewNodeService(repo)
	if _, _, err := plainService.Update(ctx, original.ID+100, CreateNodeInput{Status: "invalid"}); !errors.Is(err, apperr.ErrValidation) {
		t.Fatalf("invalid status on missing node: %v", err)
	}
	zero := uint(0)
	keyInput := CreateNodeInput{Name: "key-node", Host: "node.example", Username: "root", AuthType: "key", SSHKeyID: &zero}
	if _, err := plainService.Create(ctx, keyInput); !errors.Is(err, apperr.ErrValidation) {
		t.Fatalf("create accepted zero SSH key ID: %v", err)
	}
	keyInput.Name = "node"
	if _, _, err := plainService.Update(ctx, original.ID, keyInput); !errors.Is(err, apperr.ErrValidation) {
		t.Fatalf("update accepted zero SSH key ID: %v", err)
	}
}

func TestValidateExpectedRevision(t *testing.T) {
	valid := "0123456789abcdef0123456789abcdef"
	if err := ValidateExpectedRevision(nil); err != nil {
		t.Fatal(err)
	}
	if err := ValidateExpectedRevision(&valid); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"", "0123456789ABCDEF0123456789ABCDEF", "0123456789abcdef0123456789abcdeg"} {
		if err := ValidateExpectedRevision(&invalid); !errors.Is(err, apperr.ErrValidation) {
			t.Fatalf("invalid revision %q: %v", invalid, err)
		}
	}
}
