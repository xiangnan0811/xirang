package settings

import (
	"context"
	"testing"

	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

func TestBackupAssetSettingsSnapshotTxUsesOneTransactionAndResolutionPrecedence(t *testing.T) {
	for _, definition := range NewService(nil).Registry() {
		if IsBackupAssetFoundationSetting(definition.Key) {
			t.Setenv(definition.EnvVar, "")
		}
	}
	t.Setenv("BACKUP_ASSETS_RCLONE_LOW_LEVEL_RETRIES", "7")
	t.Setenv("BACKUP_ASSETS_MANIFEST_MAX_ENTRIES", "1234")
	db := setupTestDB(t)
	service := NewService(db)
	queries := 0
	const callback = "test:rclone_settings_snapshot_count"
	if err := db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "system_settings" {
			queries++
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&model.SystemSetting{Key: "backup_assets.rclone_low_level_retries", Value: "4"}).Error; err != nil {
			return err
		}
		values, err := service.BackupAssetSettingsSnapshotTx(context.Background(), tx)
		if err != nil {
			return err
		}
		if values["backup_assets.rclone_low_level_retries"] != "4" || values["backup_assets.manifest_max_entries"] != "1234" || values["backup_assets.rclone_preflight_ttl"] != "30m" {
			t.Fatal("transaction snapshot did not preserve DB > environment > default precedence")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if queries != 1 {
		t.Fatalf("transaction snapshot performed %d settings queries, want one", queries)
	}
}
