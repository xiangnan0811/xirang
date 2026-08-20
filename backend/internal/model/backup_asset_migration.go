package model

import "time"

type BackupAssetInstallation struct {
	ID                    string     `gorm:"primaryKey;size:32" json:"id"`
	Slot                  int        `gorm:"not null" json:"-"`
	Class                 string     `gorm:"size:16;not null" json:"class"`
	Readiness             string     `gorm:"size:16;not null" json:"readiness"`
	InventoryDigest       string     `gorm:"size:64;not null" json:"inventory_digest"`
	AckActorID            *uint      `json:"ack_actor_id,omitempty"`
	AckAt                 *time.Time `json:"ack_at,omitempty"`
	EnablementSucceededAt *time.Time `json:"enablement_succeeded_at,omitempty"`
	CreatedAt             time.Time  `gorm:"not null" json:"created_at"`
	UpdatedAt             time.Time  `gorm:"not null" json:"updated_at"`
}

func (BackupAssetInstallation) TableName() string { return "backup_asset_installations" }

type BackupAssetInventoryRun struct {
	ID            string    `gorm:"primaryKey;size:32" json:"id"`
	Digest        string    `gorm:"size:64;not null" json:"digest"`
	Status        string    `gorm:"size:16;not null" json:"status"`
	CountsJSON    string    `gorm:"type:text;not null" json:"counts_json"`
	ErrorCategory string    `gorm:"size:32;not null;default:''" json:"error_category,omitempty"`
	CreatedAt     time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt     time.Time `gorm:"not null" json:"updated_at"`
}

func (BackupAssetInventoryRun) TableName() string { return "backup_asset_inventory_runs" }

type BackupAssetRepositoryConflict struct {
	ID               string    `gorm:"primaryKey;size:32" json:"id"`
	RunID            string    `gorm:"size:32;not null" json:"run_id"`
	Kind             string    `gorm:"size:32;not null" json:"kind"`
	TaskIDsJSON      string    `gorm:"type:text;not null" json:"task_ids_json"`
	RepositoryID     string    `gorm:"size:32;not null;default:''" json:"repository_id,omitempty"`
	StableReasonCode string    `gorm:"size:128;not null" json:"stable_reason_code"`
	CreatedAt        time.Time `gorm:"not null" json:"created_at"`
}

func (BackupAssetRepositoryConflict) TableName() string {
	return "backup_asset_repository_conflicts"
}
