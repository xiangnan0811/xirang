package settings

import (
	"context"
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"

	"xirang/backend/internal/model"
)

func TestUpdateManyContextCanceledBeforePersistenceMutatesNothing(t *testing.T) {
	service := NewService(setupRecoveryTargetRootTestDB(t))
	before := service.GetEffective("backup_assets.enabled")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := service.UpdateManyContext(ctx, map[string]string{"backup_assets.enabled": "true"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("UpdateManyContext error=%v, want context.Canceled", err)
	}
	if got := service.GetEffective("backup_assets.enabled"); got != before {
		t.Fatalf("canceled context changed backup_assets.enabled from %q to %q", before, got)
	}
}

func TestUpdateContextCanceledBeforePersistenceMutatesNothing(t *testing.T) {
	service := NewService(setupRecoveryTargetRootTestDB(t))
	before := service.GetEffective("backup_assets.enabled")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := service.UpdateContext(ctx, "backup_assets.enabled", "true")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("UpdateContext error=%v, want context.Canceled", err)
	}
	if got := service.GetEffective("backup_assets.enabled"); got != before {
		t.Fatalf("canceled context changed backup_assets.enabled from %q to %q", before, got)
	}
}

func TestUpdateWithTxContextCanceledBeforePersistenceMutatesNothing(t *testing.T) {
	db := setupRecoveryTargetRootTestDB(t)
	service := NewService(db)
	const key = "backup_assets.content_preview_ttl"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := db.Transaction(func(tx *gorm.DB) error {
		return service.UpdateWithTxContext(ctx, tx, key, "3m")
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("UpdateWithTxContext error=%v, want context.Canceled", err)
	}
	var count int64
	if err := db.Model(&model.SystemSetting{}).Where("key = ?", key).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("canceled UpdateWithTxContext row count=%d", count)
	}
}

func TestDeleteWithTxContextCanceledBeforePersistencePreservesOverride(t *testing.T) {
	db := setupRecoveryTargetRootTestDB(t)
	service := NewService(db)
	const key = "backup_assets.content_preview_ttl"
	if err := service.Update(key, "3m"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := db.Transaction(func(tx *gorm.DB) error {
		return service.DeleteWithTxContext(ctx, tx, key)
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("DeleteWithTxContext error=%v, want context.Canceled", err)
	}
	var row model.SystemSetting
	if err := db.Where("key = ?", key).Take(&row).Error; err != nil {
		t.Fatalf("canceled DeleteWithTxContext removed override: %v", err)
	}
	if row.Value != "3m" {
		t.Fatalf("canceled DeleteWithTxContext changed override to %q", row.Value)
	}
}

func TestBackupAssetOverrideSnapshotRestoresRawRowAbsenceAndInvalidatesCache(t *testing.T) {
	db := setupRecoveryTargetRootTestDB(t)
	service := NewService(db)
	const (
		absentKey  = "backup_assets.content_allow_insecure_private_network"
		presentKey = "backup_assets.export.worker_concurrency"
	)
	priorUpdatedAt := time.Date(2026, 8, 24, 9, 30, 0, 123456000, time.UTC)
	if err := db.Create(&model.SystemSetting{
		Key: presentKey, Value: "2", UpdatedAt: priorUpdatedAt,
	}).Error; err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.CaptureBackupAssetOverridesContext(
		context.Background(), []string{presentKey, absentKey},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.UpdateManyContext(context.Background(), map[string]string{
		absentKey: "true", presentKey: "3",
	}); err != nil {
		t.Fatal(err)
	}
	if got := service.GetEffective(absentKey); got != "true" {
		t.Fatalf("prospective absent-key value=%q, want true", got)
	}
	if got := service.GetEffective(presentKey); got != "3" {
		t.Fatalf("prospective present-key value=%q, want 3", got)
	}
	if err := service.RestoreBackupAssetOverridesContext(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	var absent model.SystemSetting
	if err := db.Where("key = ?", absentKey).Take(&absent).Error; !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("restored absent override error=%v row=%+v", err, absent)
	}
	var present model.SystemSetting
	if err := db.Where("key = ?", presentKey).Take(&present).Error; err != nil {
		t.Fatal(err)
	}
	if present.Value != "2" || !present.UpdatedAt.Equal(priorUpdatedAt) {
		t.Fatalf("restored raw row=%+v, want value 2 updated_at %s", present, priorUpdatedAt)
	}
	if got := service.GetEffective(absentKey); got != "false" {
		t.Fatalf("restored absent-key effective value=%q, want default false", got)
	}
	if got := service.GetEffective(presentKey); got != "2" {
		t.Fatalf("restored present-key effective value=%q, want 2", got)
	}
}

func TestBackupAssetOverrideSnapshotFailureRollsBackAtomicallyAndKeepsCache(t *testing.T) {
	db := setupRecoveryTargetRootTestDB(t)
	service := NewService(db)
	const (
		absentKey  = "backup_assets.content_allow_insecure_private_network"
		presentKey = "backup_assets.export.worker_concurrency"
	)
	if err := service.UpdateContext(context.Background(), presentKey, "2"); err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.CaptureBackupAssetOverridesContext(
		context.Background(), []string{absentKey, presentKey},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.UpdateManyContext(context.Background(), map[string]string{
		absentKey: "true", presentKey: "3",
	}); err != nil {
		t.Fatal(err)
	}
	if service.GetEffective(absentKey) != "true" || service.GetEffective(presentKey) != "3" {
		t.Fatal("failed to warm prospective settings cache")
	}

	restoreErr := errors.New("injected override restore failure")
	callbackName := "test:backup-asset-override-restore-failure"
	if err := db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		row, ok := tx.Statement.Dest.(*model.SystemSetting)
		if ok && row.Key == presentKey {
			_ = tx.AddError(restoreErr)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Create().Remove(callbackName) })
	if err := service.RestoreBackupAssetOverridesContext(context.Background(), snapshot); !errors.Is(err, restoreErr) {
		t.Fatalf("restore error=%v, want injected transaction failure", err)
	}

	for key, want := range map[string]string{absentKey: "true", presentKey: "3"} {
		var row model.SystemSetting
		if err := db.Where("key = ?", key).Take(&row).Error; err != nil {
			t.Fatalf("load rolled-back %s: %v", key, err)
		}
		if row.Value != want {
			t.Fatalf("rolled-back %s row=%q, want %q", key, row.Value, want)
		}
		service.mu.RLock()
		cached, cachedOK := service.cache[key]
		service.mu.RUnlock()
		if !cachedOK || cached.value != want {
			t.Fatalf("failed restore changed cache %s=%+v present=%t, want %q", key, cached, cachedOK, want)
		}
	}
}
