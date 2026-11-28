package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"xirang/backend/internal/model"
	"xirang/backend/internal/settings"
)

func TestConfigImportCommittedCacheVisibilityPostgres(t *testing.T) {
	for _, compensate := range []bool{false, true} {
		name := "success"
		if compensate {
			name = "compensation"
		}
		t.Run(name, func(t *testing.T) {
			setConfigHandlerTestEncryption(t)
			t.Setenv("BACKUP_ASSETS_CONTENT_PREVIEW_TTL", "")
			t.Setenv("LOGIN_RATE_LIMIT", "")
			db := openMigratedRetryTransactionDB(t, "postgres")
			const foundationKey = "backup_assets.content_preview_ttl"
			const ordinaryKey = "login.rate_limit"
			keys := []string{foundationKey, ordinaryKey}
			if err := db.Where("key IN ?", keys).Delete(&model.SystemSetting{}).Error; err != nil {
				t.Fatalf("clear setting fixtures: %v", err)
			}
			prior := model.SystemSetting{
				Key: foundationKey, Value: "120s",
				UpdatedAt: time.Date(2024, time.January, 2, 3, 4, 5, 0, time.UTC),
			}
			if err := db.Create(&prior).Error; err != nil {
				t.Fatalf("seed prior raw override: %v", err)
			}
			service := settings.NewService(db)
			if got := service.GetEffective(foundationKey); got != "120s" {
				t.Fatalf("prewarm prior foundation setting: got %q", got)
			}
			if got := service.GetEffective(ordinaryKey); got != "10" {
				t.Fatalf("prewarm absent ordinary setting: got %q", got)
			}

			body := []byte(`{"system_settings":[{"key":"backup_assets.content_preview_ttl","value":"3m"},{"key":"login.rate_limit","value":"11"}]}`)
			handler := NewConfigHandler(db, service)
			var runtime *configImportRollbackRuntime
			called := false
			var callbackErr error
			if compensate {
				runtime = &configImportRollbackRuntime{failure: errors.New("test runtime failure")}
				runtime.afterPersist = func() error {
					called = true
					for key, want := range map[string]string{foundationKey: "3m", ordinaryKey: "11"} {
						if got := service.GetEffective(key); got != want {
							callbackErr = fmt.Errorf("post-persist cached setting %s=%q, want %q", key, got, want)
							return nil
						}
						var stored model.SystemSetting
						if err := db.Where("key = ?", key).First(&stored).Error; err != nil {
							callbackErr = fmt.Errorf("load committed setting %s: %w", key, err)
							return nil
						}
						if stored.Value != want {
							callbackErr = fmt.Errorf("post-persist raw setting %s=%q, want %q", key, stored.Value, want)
							return nil
						}
					}
					return nil
				}
				handler = handler.WithBackupAssetTransitioner(runtime)
			} else {
				handler = handler.WithBackupAssetTransitioner(&settingsCanceledPersistenceRuntime{})
			}
			response, result, err := configNameMappingImportRequest(handler, "overwrite", body)
			if err != nil {
				t.Fatalf("import request: %v", err)
			}
			if compensate {
				if !called || callbackErr != nil || response.Code != http.StatusInternalServerError {
					t.Fatalf("post-persist callback called=%t error=%v status=%d body=%s", called, callbackErr, response.Code, response.Body.String())
				}
				if got := service.GetEffective(foundationKey); got != prior.Value {
					t.Fatalf("compensated cached foundation setting=%q, want %q", got, prior.Value)
				}
				if got := service.GetEffective(ordinaryKey); got != "10" {
					t.Fatalf("compensated cached absent setting=%q, want 10", got)
				}
				var restored model.SystemSetting
				if err := db.Where("key = ?", foundationKey).First(&restored).Error; err != nil {
					t.Fatalf("load restored raw override: %v", err)
				}
				if restored.Value != prior.Value || !restored.UpdatedAt.Equal(prior.UpdatedAt) {
					t.Fatalf("raw override not restored: got %+v, want %+v", restored, prior)
				}
				var count int64
				if err := db.Model(&model.SystemSetting{}).Where("key = ?", ordinaryKey).Count(&count).Error; err != nil || count != 0 {
					t.Fatalf("prior absence not restored: count=%d err=%v", count, err)
				}
			} else {
				if response.Code != http.StatusOK || result.SystemSettings != 2 {
					t.Fatalf("successful import status=%d result=%+v body=%s", response.Code, result, response.Body.String())
				}
				for key, want := range map[string]string{foundationKey: "3m", ordinaryKey: "11"} {
					if got := service.GetEffective(key); got != want {
						t.Fatalf("committed cached setting %s=%q, want %q", key, got, want)
					}
				}
			}
		})
	}
}
