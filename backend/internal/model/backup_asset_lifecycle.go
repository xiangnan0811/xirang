package model

import (
	"time"

	"gorm.io/gorm"
)

type BackupRetentionPolicy struct {
	ID        string     `gorm:"primaryKey;size:32" json:"id"`
	ScopeKind string     `gorm:"size:16;not null" json:"scope_kind"`
	ScopeID   string     `gorm:"size:32;not null" json:"scope_id"`
	Revision  int64      `gorm:"not null" json:"revision"`
	RulesJSON string     `gorm:"type:text;not null" json:"rules_json"`
	Status    string     `gorm:"size:16;not null" json:"status"`
	CreatedBy uint       `gorm:"not null" json:"created_by"`
	UpdatedBy uint       `gorm:"not null" json:"updated_by"`
	CreatedAt time.Time  `gorm:"not null" json:"created_at"`
	UpdatedAt time.Time  `gorm:"not null" json:"updated_at"`
	DeletedAt *time.Time `json:"deleted_at,omitempty"`
}

func (BackupRetentionPolicy) TableName() string { return "backup_retention_policies" }

type RecoveryPointHold struct {
	ID                     string     `gorm:"primaryKey;size:32" json:"id"`
	RecoveryPointID        string     `gorm:"size:32;not null" json:"recovery_point_id"`
	HoldType               string     `gorm:"size:16;not null" json:"hold_type"`
	State                  string     `gorm:"size:16;not null" json:"state"`
	EncryptedReason        string     `gorm:"type:text;not null" json:"-"`
	CreatedBy              uint       `gorm:"not null" json:"created_by"`
	ExpiresAt              *time.Time `json:"expires_at,omitempty"`
	ReleasedBy             *uint      `json:"released_by,omitempty"`
	ReleasedAt             *time.Time `json:"released_at,omitempty"`
	EncryptedReleaseReason string     `gorm:"type:text;not null;default:''" json:"-"`
	CreatedAt              time.Time  `gorm:"not null" json:"created_at"`
	UpdatedAt              time.Time  `gorm:"not null" json:"updated_at"`
}

func (RecoveryPointHold) TableName() string { return "recovery_point_holds" }

func (hold *RecoveryPointHold) BeforeSave(_ *gorm.DB) error {
	if err := encryptBackupAssetField(&hold.EncryptedReason); err != nil {
		return err
	}
	return encryptBackupAssetField(&hold.EncryptedReleaseReason)
}

func (hold *RecoveryPointHold) AfterFind(_ *gorm.DB) error {
	if err := decryptBackupAssetField(&hold.EncryptedReason); err != nil {
		return err
	}
	return decryptBackupAssetField(&hold.EncryptedReleaseReason)
}

type RecoveryPointLifecycleAttempt struct {
	ID                  string     `gorm:"primaryKey;size:32" json:"id"`
	RecoveryPointID     string     `gorm:"size:32;not null" json:"recovery_point_id"`
	Operation           string     `gorm:"size:24;not null" json:"operation"`
	Phase               string     `gorm:"size:24;not null" json:"phase"`
	TransitionRevision  int64      `gorm:"not null;default:1" json:"transition_revision"`
	PolicyID            *string    `gorm:"size:32" json:"policy_id,omitempty"`
	PolicyRevision      *int64     `json:"policy_revision,omitempty"`
	PolicyRuleDigest    *string    `gorm:"size:64" json:"policy_rule_digest,omitempty"`
	EvaluationTime      *time.Time `json:"evaluation_time,omitempty"`
	PurgePlanID         *string    `gorm:"size:32" json:"purge_plan_id,omitempty"`
	PurgePlanRevision   *int64     `json:"purge_plan_revision,omitempty"`
	PurgeActorID        *uint      `json:"purge_actor_id,omitempty"`
	LeaseID             *string    `gorm:"size:32" json:"-"`
	LeaseAttemptID      *string    `gorm:"size:32" json:"-"`
	LeaseFenceTokenHash *string    `gorm:"size:64" json:"-"`
	BlockedReason       string     `gorm:"size:48;not null;default:''" json:"blocked_reason,omitempty"`
	ClaimedAt           *time.Time `json:"claimed_at,omitempty"`
	HeartbeatAt         *time.Time `json:"heartbeat_at,omitempty"`
	RetryAt             *time.Time `json:"retry_at,omitempty"`
	CompletedAt         *time.Time `json:"completed_at,omitempty"`
	CreatedAt           time.Time  `gorm:"not null" json:"created_at"`
	UpdatedAt           time.Time  `gorm:"not null" json:"updated_at"`
}

func (RecoveryPointLifecycleAttempt) TableName() string {
	return "recovery_point_lifecycle_attempts"
}

type RecoveryPointLifecycleTombstone struct {
	RecoveryPointID       string     `gorm:"primaryKey;size:32" json:"recovery_point_id"`
	RepositoryID          string     `gorm:"size:32;not null" json:"repository_id"`
	OriginalSemantics     string     `gorm:"size:32;not null" json:"original_semantics"`
	TerminalOperation     string     `gorm:"primaryKey;size:24" json:"terminal_operation"`
	TerminalState         string     `gorm:"size:16;not null" json:"terminal_state"`
	ManagedHistory        bool       `gorm:"not null;default:true" json:"managed_history"`
	DeletionReceiptDigest *string    `gorm:"size:64" json:"deletion_receipt_digest,omitempty"`
	RetiredAt             *time.Time `json:"retired_at,omitempty"`
	PurgedAt              *time.Time `json:"purged_at,omitempty"`
	ResultCode            string     `gorm:"size:32;not null" json:"result_code"`
	CreatedAt             time.Time  `gorm:"not null" json:"created_at"`
}

func (RecoveryPointLifecycleTombstone) TableName() string {
	return "recovery_point_lifecycle_tombstones"
}

type BackupRepositoryImportCandidate struct {
	ID                       string     `gorm:"primaryKey;size:32" json:"id"`
	RepositoryID             string     `gorm:"size:32;not null" json:"repository_id"`
	CandidateKind            string     `gorm:"size:24;not null" json:"candidate_kind"`
	SourceFingerprint        string     `gorm:"size:64;not null" json:"-"`
	EncryptedProviderLocator string     `gorm:"type:text;not null" json:"-"`
	EncryptedEvidence        string     `gorm:"type:text;not null" json:"-"`
	ReviewState              string     `gorm:"size:16;not null" json:"review_state"`
	ReviewedBy               *uint      `json:"reviewed_by,omitempty"`
	ReviewedAt               *time.Time `json:"reviewed_at,omitempty"`
	AcceptedRecoveryPointID  *string    `gorm:"size:32" json:"accepted_recovery_point_id,omitempty"`
	CreatedAt                time.Time  `gorm:"not null" json:"created_at"`
	UpdatedAt                time.Time  `gorm:"not null" json:"updated_at"`
}

func (BackupRepositoryImportCandidate) TableName() string {
	return "backup_repository_import_candidates"
}

func (candidate *BackupRepositoryImportCandidate) BeforeSave(_ *gorm.DB) error {
	if err := encryptBackupAssetField(&candidate.EncryptedProviderLocator); err != nil {
		return err
	}
	return encryptBackupAssetField(&candidate.EncryptedEvidence)
}

func (candidate *BackupRepositoryImportCandidate) AfterFind(_ *gorm.DB) error {
	if err := decryptBackupAssetField(&candidate.EncryptedProviderLocator); err != nil {
		return err
	}
	return decryptBackupAssetField(&candidate.EncryptedEvidence)
}

type BackupAssetPurgePlan struct {
	ID                  string     `gorm:"primaryKey;size:32" json:"id"`
	RepositoryID        string     `gorm:"size:32;not null" json:"repository_id"`
	RequesterID         uint       `gorm:"not null" json:"requester_id"`
	Revision            int64      `gorm:"not null;default:1" json:"revision"`
	ImpactRevision      int64      `gorm:"not null" json:"impact_revision"`
	ExpiresAt           time.Time  `gorm:"not null" json:"expires_at"`
	HoldCount           int64      `gorm:"not null;default:0" json:"hold_count"`
	LeaseCount          int64      `gorm:"not null;default:0" json:"lease_count"`
	WORMCount           int64      `gorm:"not null;default:0" json:"worm_count"`
	Status              string     `gorm:"size:16;not null" json:"status"`
	ExecuteActorID      *uint      `json:"execute_actor_id,omitempty"`
	ExecuteProofDigest  string     `gorm:"size:64;not null;default:''" json:"-"`
	ExecuteReasonDigest string     `gorm:"size:64;not null;default:''" json:"-"`
	ExecuteBoundAt      *time.Time `json:"execute_bound_at,omitempty"`
	ConsumedAt          *time.Time `json:"consumed_at,omitempty"`
	CreatedAt           time.Time  `gorm:"not null" json:"created_at"`
	UpdatedAt           time.Time  `gorm:"not null" json:"updated_at"`
}

func (BackupAssetPurgePlan) TableName() string { return "backup_asset_purge_plans" }

type BackupAssetPurgePlanItem struct {
	ID                         string    `gorm:"primaryKey;size:32" json:"id"`
	PlanID                     string    `gorm:"size:32;not null" json:"plan_id"`
	Ordinal                    int       `gorm:"not null" json:"ordinal"`
	RecoveryPointID            string    `gorm:"size:32;not null" json:"recovery_point_id"`
	ExpectedPointRevision      int64     `gorm:"not null" json:"expected_point_revision"`
	ExpectedCapabilityRevision int       `gorm:"not null" json:"expected_capability_revision"`
	CreatedAt                  time.Time `gorm:"not null" json:"created_at"`
}

func (BackupAssetPurgePlanItem) TableName() string { return "backup_asset_purge_plan_items" }

type BackupAssetConfigImportRef struct {
	ID               string    `gorm:"primaryKey;size:32" json:"id"`
	SourceDocumentID string    `gorm:"size:32;not null" json:"source_document_id"`
	SourceReference  string    `gorm:"size:128;not null" json:"source_reference"`
	EntityKind       string    `gorm:"size:24;not null" json:"entity_kind"`
	LocalEntityID    string    `gorm:"size:32;not null" json:"local_entity_id"`
	CreatedAt        time.Time `gorm:"not null" json:"created_at"`
}

func (BackupAssetConfigImportRef) TableName() string {
	return "backup_asset_config_import_refs"
}
