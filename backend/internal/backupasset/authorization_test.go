package backupasset

import "testing"

func TestAuthorizationPermissionRegistryIsExactAndNonImplying(t *testing.T) {
	want := []string{
		"backup_assets:list",
		"backup_assets:preview",
		"backup_assets:download",
		"backup_assets:export",
		"backup_assets:recover",
		"backup_repositories:manage",
		"backup_repositories:purge",
	}
	if len(BackupAssetPermissions) != len(want) {
		t.Fatalf("permission count=%d, want %d", len(BackupAssetPermissions), len(want))
	}
	seen := make(map[string]bool, len(want))
	for _, permission := range BackupAssetPermissions {
		if seen[permission] {
			t.Fatalf("duplicate backup asset permission %q", permission)
		}
		seen[permission] = true
	}
	for _, permission := range want {
		if !seen[permission] {
			t.Fatalf("missing backup asset permission %q", permission)
		}
	}

	manageOnly := PermissionSet{PermissionBackupRepositoriesManage: true}
	if manageOnly.Has(PermissionBackupRepositoriesPurge) {
		t.Fatal("repository manage permission implied purge")
	}
	downloadOnly := PermissionSet{PermissionBackupAssetsDownload: true}
	if downloadOnly.Has(PermissionBackupAssetsRecover) {
		t.Fatal("asset download permission implied recover")
	}
}

func TestRecoveryResultAuthorizationRequiresRecoverOwnerAndExactAction(t *testing.T) {
	base := RecoveryResultAuthorization{
		Permissions:        PermissionSet{PermissionBackupAssetsRecover: true},
		RequesterUserID:    41,
		RecoveryJobOwnerID: 41,
		StepUpAction:       "recovery.result_download",
	}
	if !CanDeliverRecoveryResult(base) {
		t.Fatal("complete recovery-result authorization was rejected")
	}

	tests := []struct {
		name   string
		mutate func(*RecoveryResultAuthorization)
	}{
		{"download is not recover", func(input *RecoveryResultAuthorization) {
			input.Permissions = PermissionSet{PermissionBackupAssetsDownload: true}
		}},
		{"wrong owner", func(input *RecoveryResultAuthorization) { input.RequesterUserID++ }},
		{"wrong action", func(input *RecoveryResultAuthorization) { input.StepUpAction = "asset.download" }},
		{"missing requester", func(input *RecoveryResultAuthorization) { input.RequesterUserID = 0 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := base
			tt.mutate(&input)
			if CanDeliverRecoveryResult(input) {
				t.Fatalf("authorization unexpectedly passed: %+v", input)
			}
		})
	}
}
