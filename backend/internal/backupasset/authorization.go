package backupasset

const (
	PermissionBackupAssetsList         = "backup_assets:list"
	PermissionBackupAssetsPreview      = "backup_assets:preview"
	PermissionBackupAssetsDownload     = "backup_assets:download"
	PermissionBackupAssetsExport       = "backup_assets:export"
	PermissionBackupAssetsRecover      = "backup_assets:recover"
	PermissionBackupRepositoriesManage = "backup_repositories:manage"
	PermissionBackupRepositoriesPurge  = "backup_repositories:purge"
)

var BackupAssetPermissions = [...]string{
	PermissionBackupAssetsList,
	PermissionBackupAssetsPreview,
	PermissionBackupAssetsDownload,
	PermissionBackupAssetsExport,
	PermissionBackupAssetsRecover,
	PermissionBackupRepositoriesManage,
	PermissionBackupRepositoriesPurge,
}

type PermissionSet map[string]bool

func (permissions PermissionSet) Has(permission string) bool {
	return permissions != nil && permissions[permission]
}

type RecoveryResultAuthorization struct {
	Permissions        PermissionSet
	RequesterUserID    uint
	RecoveryJobOwnerID uint
	StepUpAction       string
}

func CanDeliverRecoveryResult(input RecoveryResultAuthorization) bool {
	return input.RequesterUserID != 0 &&
		input.RequesterUserID == input.RecoveryJobOwnerID &&
		input.Permissions.Has(PermissionBackupAssetsRecover) &&
		input.StepUpAction == "recovery.result_download"
}
