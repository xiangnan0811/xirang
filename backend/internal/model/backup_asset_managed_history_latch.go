package model

import "time"

// BackupAssetManagedHistoryLatch is a durable, internal-only record that keeps
// managed-history admission fail-closed even after related repository rows are
// unlinked or removed by a future lifecycle workflow.
type BackupAssetManagedHistoryLatch struct {
	ID                       string    `gorm:"primaryKey;size:96" json:"-"`
	Scope                    string    `gorm:"size:16;not null" json:"-"`
	RepositoryID             *string   `gorm:"size:32" json:"-"`
	RepositoryIdentityDigest string    `gorm:"size:64;not null;default:''" json:"-"`
	FirstSemantics           string    `gorm:"size:32;not null" json:"-"`
	FirstOrigin              string    `gorm:"size:64;not null" json:"-"`
	FirstSeenAt              time.Time `gorm:"not null" json:"-"`
	CreatedAt                time.Time `gorm:"not null" json:"-"`
	UpdatedAt                time.Time `gorm:"not null" json:"-"`
}

func (BackupAssetManagedHistoryLatch) TableName() string {
	return "backup_asset_managed_history_latches"
}
