package model

import "testing"

func TestBackupAssetGAModelsUseExactThreeTables(t *testing.T) {
	models := []interface{ TableName() string }{
		BackupAssetInstallation{},
		BackupAssetInventoryRun{},
		BackupAssetRepositoryConflict{},
	}
	want := []string{
		"backup_asset_installations",
		"backup_asset_inventory_runs",
		"backup_asset_repository_conflicts",
	}
	if len(models) != len(want) {
		t.Fatalf("GA model count=%d want=%d", len(models), len(want))
	}
	for index, persistentModel := range models {
		if got := persistentModel.TableName(); got != want[index] {
			t.Fatalf("GA model %d table=%q want=%q", index, got, want[index])
		}
	}
}
