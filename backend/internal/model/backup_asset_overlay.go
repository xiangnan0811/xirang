package model

import (
	"time"

	"gorm.io/gorm"
)

type BackupAssetSavedSearch struct {
	ID            string     `gorm:"primaryKey;size:32" json:"id"`
	OwnerUserID   uint       `gorm:"not null" json:"-"`
	EncryptedAST  string     `gorm:"type:text;not null" json:"-"`
	SchemaVersion int        `gorm:"not null" json:"schema_version"`
	ScopeMode     string     `gorm:"size:16;not null" json:"scope_mode"`
	Version       int        `gorm:"not null;default:1" json:"version"`
	State         string     `gorm:"size:16;not null" json:"state"`
	StateReason   string     `gorm:"size:32;not null;default:''" json:"state_reason,omitempty"`
	BrokenAt      *time.Time `json:"broken_at,omitempty"`
	CreatedAt     time.Time  `gorm:"not null" json:"created_at"`
	UpdatedAt     time.Time  `gorm:"not null" json:"updated_at"`
}

func (BackupAssetSavedSearch) TableName() string {
	return "backup_asset_saved_searches"
}

func (search *BackupAssetSavedSearch) BeforeSave(_ *gorm.DB) error {
	return encryptBackupAssetField(&search.EncryptedAST)
}

func (search *BackupAssetSavedSearch) AfterFind(_ *gorm.DB) error {
	return decryptBackupAssetField(&search.EncryptedAST)
}

type BackupAssetSavedSearchScopePoint struct {
	SavedSearchID   string `gorm:"primaryKey;size:32" json:"-"`
	RecoveryPointID string `gorm:"primaryKey;size:32" json:"recovery_point_id"`
}

func (BackupAssetSavedSearchScopePoint) TableName() string {
	return "backup_asset_saved_search_scope_points"
}

type BackupAssetFavorite struct {
	ID              string    `gorm:"primaryKey;size:32" json:"id"`
	OwnerUserID     uint      `gorm:"not null" json:"-"`
	RecoveryPointID string    `gorm:"size:32;not null" json:"recovery_point_id"`
	EntryID         string    `gorm:"size:64;not null" json:"entry_id"`
	EncryptedLabel  string    `gorm:"type:text;not null;default:''" json:"-"`
	State           string    `gorm:"size:16;not null" json:"state"`
	TombstoneReason string    `gorm:"size:32;not null;default:''" json:"tombstone_reason,omitempty"`
	Version         int       `gorm:"not null;default:1" json:"version"`
	CreatedAt       time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt       time.Time `gorm:"not null" json:"updated_at"`
}

func (BackupAssetFavorite) TableName() string { return "backup_asset_favorites" }

func (favorite *BackupAssetFavorite) BeforeSave(_ *gorm.DB) error {
	return encryptBackupAssetField(&favorite.EncryptedLabel)
}

func (favorite *BackupAssetFavorite) AfterFind(_ *gorm.DB) error {
	return decryptBackupAssetField(&favorite.EncryptedLabel)
}

type BackupAssetTagDefinition struct {
	ID            string    `gorm:"primaryKey;size:32" json:"id"`
	OwnerUserID   uint      `gorm:"not null" json:"-"`
	EncryptedName string    `gorm:"type:text;not null" json:"-"`
	NameToken     string    `gorm:"size:64;not null" json:"-"`
	KeyVersion    int       `gorm:"not null" json:"-"`
	TokenState    string    `gorm:"size:16;not null" json:"token_state"`
	Version       int       `gorm:"not null;default:1" json:"version"`
	CreatedAt     time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt     time.Time `gorm:"not null" json:"updated_at"`
}

func (BackupAssetTagDefinition) TableName() string {
	return "backup_asset_tag_definitions"
}

func (tag *BackupAssetTagDefinition) BeforeSave(_ *gorm.DB) error {
	return encryptBackupAssetField(&tag.EncryptedName)
}

func (tag *BackupAssetTagDefinition) AfterFind(_ *gorm.DB) error {
	return decryptBackupAssetField(&tag.EncryptedName)
}

type BackupAssetTagAssignment struct {
	ID              string    `gorm:"primaryKey;size:32" json:"id"`
	OwnerUserID     uint      `gorm:"not null" json:"-"`
	TagID           string    `gorm:"size:32;not null" json:"tag_id"`
	RecoveryPointID string    `gorm:"size:32;not null" json:"recovery_point_id"`
	EntryID         string    `gorm:"size:64;not null" json:"entry_id"`
	State           string    `gorm:"size:16;not null" json:"state"`
	TombstoneReason string    `gorm:"size:32;not null;default:''" json:"tombstone_reason,omitempty"`
	Version         int       `gorm:"not null;default:1" json:"version"`
	CreatedAt       time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt       time.Time `gorm:"not null" json:"updated_at"`
}

func (BackupAssetTagAssignment) TableName() string {
	return "backup_asset_tag_assignments"
}

type BackupAssetRecentAccess struct {
	ID              string    `gorm:"primaryKey;size:32" json:"id"`
	OwnerUserID     uint      `gorm:"not null" json:"-"`
	RecoveryPointID string    `gorm:"size:32;not null" json:"recovery_point_id"`
	EntryID         string    `gorm:"size:64;not null" json:"entry_id"`
	AccessCount     int64     `gorm:"not null" json:"access_count"`
	LastAccessedAt  time.Time `gorm:"not null" json:"last_accessed_at"`
	ExpiresAt       time.Time `gorm:"not null" json:"expires_at"`
	Version         int       `gorm:"not null;default:1" json:"version"`
	CreatedAt       time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt       time.Time `gorm:"not null" json:"updated_at"`
}

func (BackupAssetRecentAccess) TableName() string {
	return "backup_asset_recent_access"
}

type BackupAssetOverlayUsage struct {
	OwnerUserID                uint      `gorm:"primaryKey" json:"-"`
	SavedSearchCount           int64     `gorm:"not null;default:0" json:"saved_search_count"`
	FavoriteCount              int64     `gorm:"not null;default:0" json:"favorite_count"`
	TagDefinitionCount         int64     `gorm:"not null;default:0" json:"tag_definition_count"`
	TagAssignmentCount         int64     `gorm:"not null;default:0" json:"tag_assignment_count"`
	RecentCount                int64     `gorm:"not null;default:0" json:"recent_count"`
	RecentRateWindowStartedAt  time.Time `gorm:"not null" json:"-"`
	RecentRateWindowWriteCount int64     `gorm:"not null;default:0" json:"-"`
	Version                    int       `gorm:"not null;default:1" json:"version"`
	UpdatedAt                  time.Time `gorm:"not null" json:"updated_at"`
}

func (BackupAssetOverlayUsage) TableName() string {
	return "backup_asset_overlay_usage"
}

type BackupAssetOverlayIdempotency struct {
	ID                          string    `gorm:"primaryKey;size:32" json:"-"`
	OwnerUserID                 uint      `gorm:"not null" json:"-"`
	Action                      string    `gorm:"size:32;not null" json:"-"`
	KeyHash                     string    `gorm:"size:64;not null" json:"-"`
	EncryptedRequestFingerprint string    `gorm:"type:text;not null" json:"-"`
	ResultResourceType          string    `gorm:"size:32;not null" json:"-"`
	ResultResourceID            string    `gorm:"size:64;not null;default:''" json:"-"`
	ResultVersion               int       `gorm:"not null;default:0" json:"-"`
	CreatedAt                   time.Time `gorm:"not null" json:"-"`
	ExpiresAt                   time.Time `gorm:"not null" json:"-"`
}

func (BackupAssetOverlayIdempotency) TableName() string {
	return "backup_asset_overlay_idempotency"
}

func (receipt *BackupAssetOverlayIdempotency) BeforeSave(_ *gorm.DB) error {
	return encryptBackupAssetField(&receipt.EncryptedRequestFingerprint)
}

func (receipt *BackupAssetOverlayIdempotency) AfterFind(_ *gorm.DB) error {
	return decryptBackupAssetField(&receipt.EncryptedRequestFingerprint)
}
