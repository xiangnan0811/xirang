package model

import (
	"strings"
	"time"

	"xirang/backend/internal/secure"

	"gorm.io/gorm"
)

type BackupRepository struct {
	ID                 string     `gorm:"primaryKey;size:32" json:"id"`
	ProviderKind       string     `gorm:"size:32;not null" json:"provider_kind"`
	RepositoryIdentity *string    `gorm:"type:text" json:"-"`
	DisplayName        string     `gorm:"size:255;not null" json:"display_name"`
	Description        string     `gorm:"type:text;not null;default:''" json:"description"`
	VersionMode        string     `gorm:"size:32;not null" json:"version_mode"`
	Status             string     `gorm:"size:32;not null" json:"status"`
	CapabilityRevision int        `gorm:"not null;default:1" json:"capability_revision"`
	CapabilitiesJSON   string     `gorm:"type:text;not null;default:'{}'" json:"-"`
	ImmutabilityLevel  string     `gorm:"size:32;not null" json:"immutability_level"`
	LastSeenAt         *time.Time `json:"last_seen_at,omitempty"`
	LastReconciledAt   *time.Time `json:"last_reconciled_at,omitempty"`
	CreatedAt          time.Time  `gorm:"not null" json:"created_at"`
	UpdatedAt          time.Time  `gorm:"not null" json:"updated_at"`
}

func (BackupRepository) TableName() string { return "backup_repositories" }

type RepositoryAccessBinding struct {
	ID                string     `gorm:"primaryKey;size:32" json:"id"`
	RepositoryID      string     `gorm:"size:32;not null" json:"repository_id"`
	BindingKind       string     `gorm:"size:32;not null" json:"binding_kind"`
	EncryptedConfig   string     `gorm:"type:text;not null" json:"-"`
	ConfigFingerprint string     `gorm:"size:64;not null" json:"config_fingerprint"`
	Status            string     `gorm:"size:16;not null" json:"status"`
	RevokedAt         *time.Time `json:"revoked_at,omitempty"`
	CreatedAt         time.Time  `gorm:"not null" json:"created_at"`
	UpdatedAt         time.Time  `gorm:"not null" json:"updated_at"`
}

func (RepositoryAccessBinding) TableName() string { return "repository_access_bindings" }

func (binding *RepositoryAccessBinding) BeforeSave(_ *gorm.DB) error {
	return encryptBackupAssetField(&binding.EncryptedConfig)
}

func (binding *RepositoryAccessBinding) AfterFind(_ *gorm.DB) error {
	return decryptBackupAssetField(&binding.EncryptedConfig)
}

type TaskRepositoryLink struct {
	ID                     string     `gorm:"primaryKey;size:32" json:"id"`
	TaskID                 *uint      `json:"task_id,omitempty"`
	RepositoryID           string     `gorm:"size:32;not null" json:"repository_id"`
	TaskNameSnapshot       string     `gorm:"size:255;not null;default:''" json:"task_name_snapshot"`
	NodeIDSnapshot         uint       `gorm:"not null;default:0" json:"node_id_snapshot"`
	NodeNameSnapshot       string     `gorm:"size:255;not null;default:''" json:"node_name_snapshot"`
	PublicationMode        string     `gorm:"size:32;not null" json:"publication_mode"`
	EncryptedLegacyLocator string     `gorm:"type:text;not null;default:''" json:"-"`
	LinkedAt               time.Time  `gorm:"not null" json:"linked_at"`
	UnlinkedAt             *time.Time `json:"unlinked_at,omitempty"`
	CreatedAt              time.Time  `gorm:"not null" json:"created_at"`
	UpdatedAt              time.Time  `gorm:"not null" json:"updated_at"`
}

func (TaskRepositoryLink) TableName() string { return "task_repository_links" }

func (link *TaskRepositoryLink) BeforeSave(_ *gorm.DB) error {
	return encryptBackupAssetField(&link.EncryptedLegacyLocator)
}

func (link *TaskRepositoryLink) AfterFind(_ *gorm.DB) error {
	return decryptBackupAssetField(&link.EncryptedLegacyLocator)
}

type RecoveryPoint struct {
	ID                        string     `gorm:"primaryKey;size:32" json:"id"`
	RepositoryID              string     `gorm:"size:32;not null" json:"repository_id"`
	ProducingTaskID           *uint      `json:"producing_task_id,omitempty"`
	ProducingTaskRunID        *uint      `json:"producing_task_run_id,omitempty"`
	ProducingTaskNameSnapshot string     `gorm:"size:255;not null;default:''" json:"producing_task_name_snapshot"`
	ProducingNodeIDSnapshot   uint       `gorm:"not null;default:0" json:"producing_node_id_snapshot"`
	ProducingNodeNameSnapshot string     `gorm:"size:255;not null;default:''" json:"producing_node_name_snapshot"`
	LineageJSON               string     `gorm:"type:text;not null;default:'{}'" json:"-"`
	EncryptedProviderLocator  string     `gorm:"type:text;not null;default:''" json:"-"`
	EncryptedRollbackLocator  string     `gorm:"type:text;not null;default:''" json:"-"`
	Semantics                 string     `gorm:"size:32;not null" json:"semantics"`
	State                     string     `gorm:"size:32;not null" json:"state"`
	CapturedAt                *time.Time `json:"captured_at,omitempty"`
	CommittedAt               *time.Time `json:"committed_at,omitempty"`
	ObservedAt                *time.Time `json:"observed_at,omitempty"`
	SourceFingerprint         string     `gorm:"size:128;not null;default:''" json:"source_fingerprint,omitempty"`
	ManifestDigestAlgorithm   string     `gorm:"size:32;not null;default:sha256" json:"manifest_digest_algorithm"`
	ManifestDigest            string     `gorm:"size:128;not null;default:''" json:"manifest_digest,omitempty"`
	EntryCount                int64      `gorm:"not null;default:0" json:"entry_count"`
	LogicalBytes              int64      `gorm:"not null;default:0" json:"logical_bytes"`
	ConsistencyJSON           string     `gorm:"type:text;not null;default:'{}'" json:"-"`
	FidelityJSON              string     `gorm:"type:text;not null;default:'{}'" json:"-"`
	CapabilityRevision        int        `gorm:"not null;default:1" json:"capability_revision"`
	CapabilitiesJSON          string     `gorm:"type:text;not null;default:'{}'" json:"-"`
	ImmutabilityLevel         string     `gorm:"size:32;not null" json:"immutability_level"`
	PhysicalAvailability      string     `gorm:"size:16;not null" json:"physical_availability"`
	HoldState                 string     `gorm:"size:16;not null" json:"hold_state"`
	HoldUntil                 *time.Time `json:"hold_until,omitempty"`
	RetentionUntil            *time.Time `json:"retention_until,omitempty"`
	RetirementReason          *string    `gorm:"size:16" json:"retirement_reason,omitempty"`
	RetiredAt                 *time.Time `json:"retired_at,omitempty"`
	CreatedAt                 time.Time  `gorm:"not null" json:"created_at"`
	UpdatedAt                 time.Time  `gorm:"not null" json:"updated_at"`
}

func (RecoveryPoint) TableName() string { return "recovery_points" }

func (point *RecoveryPoint) BeforeSave(_ *gorm.DB) error {
	if err := encryptBackupAssetField(&point.EncryptedProviderLocator); err != nil {
		return err
	}
	return encryptBackupAssetField(&point.EncryptedRollbackLocator)
}

func (point *RecoveryPoint) AfterFind(_ *gorm.DB) error {
	if err := decryptBackupAssetField(&point.EncryptedProviderLocator); err != nil {
		return err
	}
	return decryptBackupAssetField(&point.EncryptedRollbackLocator)
}

type RecoveryPointManifest struct {
	ID                      string    `gorm:"primaryKey;size:32" json:"id"`
	RecoveryPointID         string    `gorm:"size:32;not null" json:"recovery_point_id"`
	Revision                int       `gorm:"not null" json:"revision"`
	DigestAlgorithm         string    `gorm:"size:32;not null" json:"digest_algorithm"`
	Digest                  string    `gorm:"size:128;not null" json:"digest"`
	Generator               string    `gorm:"size:64;not null" json:"generator"`
	GeneratorVersion        string    `gorm:"size:64;not null" json:"generator_version"`
	Completeness            string    `gorm:"size:16;not null" json:"completeness"`
	EntryCount              int64     `gorm:"not null;default:0" json:"entry_count"`
	LogicalBytes            int64     `gorm:"not null;default:0" json:"logical_bytes"`
	FidelityJSON            string    `gorm:"type:text;not null;default:'{}'" json:"-"`
	EncryptedCommitEvidence string    `gorm:"type:text;not null;default:''" json:"-"`
	IsActive                bool      `gorm:"not null;default:false" json:"is_active"`
	CreatedAt               time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt               time.Time `gorm:"not null" json:"updated_at"`
}

func (RecoveryPointManifest) TableName() string { return "recovery_point_manifests" }

func (manifest *RecoveryPointManifest) BeforeSave(_ *gorm.DB) error {
	return encryptBackupAssetField(&manifest.EncryptedCommitEvidence)
}

func (manifest *RecoveryPointManifest) AfterFind(_ *gorm.DB) error {
	return decryptBackupAssetField(&manifest.EncryptedCommitEvidence)
}

type WrappedDomainKey struct {
	ID                     string     `gorm:"primaryKey;size:32" json:"id"`
	Domain                 string     `gorm:"size:32;not null" json:"domain"`
	Version                int        `gorm:"not null" json:"version"`
	State                  string     `gorm:"size:16;not null" json:"state"`
	WrappedKey             string     `gorm:"type:text;not null" json:"-"`
	WrapAlgorithm          string     `gorm:"size:32;not null" json:"-"`
	WrappingKeyFingerprint string     `gorm:"size:64;not null" json:"-"`
	ActivatedAt            time.Time  `gorm:"not null" json:"activated_at"`
	VerifyUntil            *time.Time `json:"verify_until,omitempty"`
	LostAt                 *time.Time `json:"lost_at,omitempty"`
	CreatedAt              time.Time  `gorm:"not null" json:"created_at"`
	UpdatedAt              time.Time  `gorm:"not null" json:"updated_at"`
}

func (WrappedDomainKey) TableName() string { return "wrapped_domain_keys" }

func encryptBackupAssetField(value *string) error {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil
	}
	encrypted, err := secure.EncryptIfNeeded(*value)
	if err != nil {
		return err
	}
	*value = encrypted
	return nil
}

func decryptBackupAssetField(value *string) error {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil
	}
	decrypted, err := secure.DecryptIfNeeded(*value)
	if err != nil {
		return err
	}
	*value = decrypted
	return nil
}
