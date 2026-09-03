package model

import (
	"time"

	"gorm.io/gorm"
)

const (
	RecoveryPointRcloneNativeEvidenceRoleOwned     = "owned"
	RecoveryPointRcloneNativeEvidenceRoleReference = "reference"
)

// RecoveryPointRcloneNativeVersion stores one exact native-object version
// identity for a managed Rclone recovery point. PhysicalKey and VersionID are
// encrypted at rest; IdentityDigest is the marker-key HMAC used for joins and
// conflict checks without exposing either provider identity.
type RecoveryPointRcloneNativeVersion struct {
	RecoveryPointID      string    `gorm:"primaryKey;size:32;not null" json:"recovery_point_id"`
	EvidenceRole         string    `gorm:"primaryKey;size:16;not null" json:"evidence_role"`
	Ordinal              int64     `gorm:"primaryKey;not null" json:"ordinal"`
	RepositoryID         string    `gorm:"size:32;not null" json:"repository_id"`
	IdentityDigest       string    `gorm:"size:64;not null" json:"identity_digest"`
	EncryptedPhysicalKey string    `gorm:"type:text;not null" json:"-"`
	EncryptedVersionID   string    `gorm:"type:text;not null" json:"-"`
	CreatedAt            time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt            time.Time `gorm:"not null" json:"updated_at"`
}

func (version *RecoveryPointRcloneNativeVersion) BeforeSave(_ *gorm.DB) error {
	if err := encryptBackupAssetField(&version.EncryptedPhysicalKey); err != nil {
		return err
	}
	return encryptBackupAssetField(&version.EncryptedVersionID)
}

func (version *RecoveryPointRcloneNativeVersion) AfterFind(_ *gorm.DB) error {
	if err := decryptBackupAssetField(&version.EncryptedPhysicalKey); err != nil {
		return err
	}
	return decryptBackupAssetField(&version.EncryptedVersionID)
}
