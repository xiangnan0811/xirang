package backupasset

import (
	"context"
	"reflect"
	"testing"

	"xirang/backend/internal/model"
	"xirang/backend/internal/settings"

	"gorm.io/gorm"
)

func TestPublicationConfigFromValuesMatchesEffectiveConfiguration(t *testing.T) {
	values := cloneFoundationTestValues(staticFoundationDefaults)
	values["backup_assets.rclone_low_level_retries"] = "4"
	before := cloneFoundationTestValues(values)
	actual, err := PublicationConfigFromValues(values)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := NewFoundationService(staticSettingsReader(values)).PublicationConfig()
	if err != nil || !reflect.DeepEqual(actual, expected) || !reflect.DeepEqual(values, before) {
		t.Fatalf("publication snapshot mismatch: %v", err)
	}
	delete(values, "backup_assets.rclone_preflight_ttl")
	if _, err := PublicationConfigFromValues(values); err == nil {
		t.Fatal("incomplete snapshot accepted")
	}
}

func TestPublicationConfigTxObservesUncommittedOverride(t *testing.T) {
	clearFoundationSettingEnvironment(t)
	db := openFoundationEffectiveSettingsTestDB(t)
	foundation := NewFoundationService(settings.NewService(db))
	before, err := foundation.PublicationConfig()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&model.SystemSetting{Key: "backup_assets.rclone_low_level_retries", Value: "4"}).Error; err != nil {
			return err
		}
		current, err := foundation.PublicationConfigTx(context.Background(), tx)
		if err != nil {
			return err
		}
		if before.Rclone.LowLevelRetries == current.Rclone.LowLevelRetries || current.Rclone.LowLevelRetries != 4 {
			t.Fatal("transaction snapshot did not see its uncommitted override")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestProcessingBackfillConfigTxObservesUncommittedOverride(t *testing.T) {
	clearFoundationSettingEnvironment(t)
	db := openFoundationEffectiveSettingsTestDB(t)
	foundation := NewFoundationService(settings.NewService(db))
	before, err := foundation.ProcessingConfig()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&model.SystemSetting{Key: "backup_assets.processing_backfill_bytes_per_hour", Value: "65536"}).Error; err != nil {
			return err
		}
		current, err := foundation.ProcessingBackfillConfigTx(context.Background(), tx)
		if err != nil {
			return err
		}
		if current.BytesPerHour != 65536 || current.RecentWindow != before.Backfill.RecentWindow || current.HistoryAgingStep != before.Backfill.HistoryAgingStep {
			t.Fatalf("transactional backfill config=%+v, before=%+v", current, before.Backfill)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFoundationService(staticSettingsReader{}).ProcessingBackfillConfigTx(context.Background(), db); err == nil {
		t.Fatal("missing transaction snapshot reader accepted")
	}
}
