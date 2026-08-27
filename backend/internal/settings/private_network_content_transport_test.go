package settings

import (
	"testing"

	"xirang/backend/internal/model"
)

func TestPrivateNetworkContentTransportSettingPrecedenceAndValidation(t *testing.T) {
	const (
		key    = "backup_assets.content_allow_insecure_private_network"
		envVar = "BACKUP_ASSETS_CONTENT_ALLOW_INSECURE_PRIVATE_NETWORK"
	)

	t.Setenv(envVar, "")
	service := NewService(setupTestDB(t))
	assertResolved := func(wantValue, wantSource string) {
		t.Helper()
		values, err := service.GetAll()
		if err != nil {
			t.Fatal(err)
		}
		resolved, ok := values[key]
		if !ok || resolved.Value != wantValue || resolved.Source != wantSource {
			t.Fatalf("resolved setting=%+v present=%t, want value=%q source=%q", resolved, ok, wantValue, wantSource)
		}
		snapshot, err := service.BackupAssetSettingsSnapshot()
		if err != nil {
			t.Fatal(err)
		}
		if got := snapshot[key]; got != wantValue {
			t.Fatalf("Foundation snapshot %s=%q, want %q", key, got, wantValue)
		}
	}

	assertResolved("false", "default")
	for _, value := range []string{"true", "false"} {
		if err := service.Validate(key, value); err != nil {
			t.Fatalf("valid bool %q rejected: %v", value, err)
		}
	}
	for _, value := range []string{"", "1", "enabled"} {
		if err := service.Validate(key, value); err == nil {
			t.Fatalf("invalid bool %q accepted", value)
		}
	}

	t.Setenv(envVar, "true")
	assertResolved("true", "env")
	if err := service.Update(key, "false"); err != nil {
		t.Fatal(err)
	}
	assertResolved("false", "db")
	if err := service.Update(key, "true"); err != nil {
		t.Fatal(err)
	}
	assertResolved("true", "db")
	if err := service.Delete(key); err != nil {
		t.Fatal(err)
	}
	assertResolved("true", "env")

	var count int64
	if err := service.db.Model(&model.SystemSetting{}).Where("key = ?", key).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("reset retained DB override count=%d", count)
	}
}
