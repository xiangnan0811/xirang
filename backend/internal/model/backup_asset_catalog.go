package model

import (
	"time"

	"gorm.io/gorm"
)

type CatalogGeneration struct {
	ID                 string     `gorm:"primaryKey;size:32" json:"id"`
	RecoveryPointID    string     `gorm:"size:32;not null" json:"recovery_point_id"`
	ManifestID         *string    `gorm:"size:32" json:"manifest_id,omitempty"`
	Generation         int        `gorm:"not null" json:"generation"`
	State              string     `gorm:"size:16;not null" json:"state"`
	IsActive           bool       `gorm:"not null;default:false" json:"is_active"`
	SourceFingerprint  string     `gorm:"size:128;not null;default:''" json:"source_fingerprint,omitempty"`
	ExpectedEntryCount int64      `gorm:"not null;default:0" json:"expected_entry_count"`
	WrittenEntryCount  int64      `gorm:"not null;default:0" json:"written_entry_count"`
	ExpectedDigest     string     `gorm:"size:128;not null;default:''" json:"expected_digest,omitempty"`
	WrittenDigest      string     `gorm:"size:128;not null;default:''" json:"written_digest,omitempty"`
	ErrorCode          string     `gorm:"size:64;not null;default:''" json:"error_code,omitempty"`
	CorrelationID      string     `gorm:"size:64;not null;default:''" json:"correlation_id,omitempty"`
	StartedAt          time.Time  `gorm:"not null" json:"started_at"`
	FinishedAt         *time.Time `json:"finished_at,omitempty"`
	CreatedAt          time.Time  `gorm:"not null" json:"created_at"`
	UpdatedAt          time.Time  `gorm:"not null" json:"updated_at"`
}

func (CatalogGeneration) TableName() string { return "catalog_generations" }

type CatalogEntry struct {
	GenerationID             string     `gorm:"primaryKey;size:32" json:"generation_id"`
	EntryID                  string     `gorm:"primaryKey;size:64" json:"entry_id"`
	RecoveryPointID          string     `gorm:"size:32;not null" json:"recovery_point_id"`
	ParentEntryID            *string    `gorm:"size:64" json:"parent_entry_id,omitempty"`
	NormalizedPath           string     `gorm:"type:text;not null" json:"-"`
	Name                     string     `gorm:"type:text;not null" json:"-"`
	EntryType                string     `gorm:"size:16;not null" json:"entry_type"`
	Size                     int64      `gorm:"not null;default:0" json:"size"`
	ModifiedAt               *time.Time `json:"modified_at,omitempty"`
	Mode                     string     `gorm:"size:32;not null;default:''" json:"mode,omitempty"`
	Owner                    string     `gorm:"size:255;not null;default:''" json:"owner,omitempty"`
	MimeType                 string     `gorm:"size:255;not null;default:''" json:"mime_type,omitempty"`
	Fingerprint              string     `gorm:"size:128;not null;default:''" json:"fingerprint,omitempty"`
	FingerprintStrength      string     `gorm:"size:32;not null;default:''" json:"fingerprint_strength,omitempty"`
	EncryptedProviderLocator string     `gorm:"type:text;not null;default:''" json:"-"`
	SecurityState            string     `gorm:"size:32;not null;default:''" json:"security_state,omitempty"`
	CreatedAt                time.Time  `gorm:"not null" json:"created_at"`
}

func (CatalogEntry) TableName() string { return "catalog_entries" }

func (entry *CatalogEntry) BeforeSave(_ *gorm.DB) error {
	return encryptBackupAssetField(&entry.EncryptedProviderLocator)
}

func (entry *CatalogEntry) AfterFind(_ *gorm.DB) error {
	return decryptBackupAssetField(&entry.EncryptedProviderLocator)
}
