package model

import (
	"time"

	"gorm.io/gorm"
)

// BackupAssetRecoveryPlan stores the immutable selection, target, security,
// and preflight bindings used to create at most one durable recovery job.
type BackupAssetRecoveryPlan struct {
	ID                            string     `gorm:"primaryKey;size:32" json:"id"`
	RequesterID                   uint       `gorm:"not null;index" json:"-"`
	Endpoint                      string     `gorm:"size:64;not null" json:"-"`
	IdempotencyKeyDigest          string     `gorm:"size:64;not null" json:"-"`
	RepositoryID                  string     `gorm:"size:32;not null" json:"repository_id"`
	RecoveryPointID               string     `gorm:"size:32;not null" json:"recovery_point_id"`
	SourceRevisionDigest          string     `gorm:"size:64;not null" json:"-"`
	SourceRevisionKind            string     `gorm:"size:16;not null" json:"-"`
	ImmutableLocatorDigest        string     `gorm:"size:64;not null;default:''" json:"-"`
	ImmutableManifestDigest       string     `gorm:"size:64;not null;default:''" json:"-"`
	ObservationFingerprint        string     `gorm:"size:64;not null;default:''" json:"-"`
	CatalogGenerationID           string     `gorm:"size:32;not null;default:''" json:"-"`
	ObservedAt                    *time.Time `json:"-"`
	EncryptedSourceLocator        string     `gorm:"type:text;not null;default:''" json:"-"`
	TargetMode                    string     `gorm:"size:16;not null" json:"-"`
	TargetNodeID                  uint       `gorm:"not null" json:"target_node_id"`
	TargetRootID                  string     `gorm:"size:32;not null" json:"target_root_id"`
	EncryptedTargetRootLocator    string     `gorm:"type:text;not null;default:''" json:"-"`
	EncryptedTargetRelativePath   string     `gorm:"type:text;not null;default:''" json:"-"`
	RootLocatorDigest             string     `gorm:"size:64;not null" json:"-"`
	PathDigest                    string     `gorm:"size:64;not null" json:"-"`
	TargetBaseRevision            string     `gorm:"size:64;not null" json:"-"`
	CredentialScopeRevision       string     `gorm:"size:64;not null" json:"-"`
	RootRevision                  string     `gorm:"size:64;not null" json:"-"`
	FilesystemRevision            string     `gorm:"size:64;not null" json:"-"`
	SelectionDigest               string     `gorm:"size:64;not null" json:"-"`
	BindingDigest                 string     `gorm:"size:64;not null" json:"-"`
	CapabilityRevision            string     `gorm:"size:64;not null" json:"-"`
	ConflictPolicy                string     `gorm:"size:32;not null" json:"-"`
	OperationSetDigest            string     `gorm:"size:64;not null" json:"-"`
	DeleteSetDigest               string     `gorm:"size:64;not null" json:"-"`
	SecurityDecision              string     `gorm:"size:32;not null" json:"-"`
	SecurityDecisionDigest        string     `gorm:"size:64;not null" json:"-"`
	SecurityFindingSetDigest      string     `gorm:"size:64;not null" json:"-"`
	SecurityPolicyRevision        string     `gorm:"size:64;not null" json:"-"`
	SecurityOverrideBindingDigest string     `gorm:"size:64;not null;default:''" json:"-"`
	EncryptedOverrideReason       string     `gorm:"type:text;not null;default:''" json:"-"`
	PreflightRevision             string     `gorm:"size:64;not null" json:"-"`
	PreflightExpiresAt            time.Time  `gorm:"not null" json:"preflight_expires_at"`
	EstimatedItems                int64      `gorm:"not null;default:0" json:"estimated_items"`
	EstimatedBytes                int64      `gorm:"not null;default:0" json:"estimated_bytes"`
	State                         string     `gorm:"size:32;not null" json:"-"`
	TransitionRevision            uint64     `gorm:"not null;default:1" json:"-"`
	CreatedAt                     time.Time  `gorm:"not null" json:"created_at"`
	UpdatedAt                     time.Time  `gorm:"not null" json:"updated_at"`
}

func (BackupAssetRecoveryPlan) TableName() string { return "backup_asset_recovery_plans" }

func (plan *BackupAssetRecoveryPlan) BeforeSave(_ *gorm.DB) error {
	if err := encryptBackupAssetField(&plan.EncryptedSourceLocator); err != nil {
		return err
	}
	if err := encryptBackupAssetField(&plan.EncryptedTargetRootLocator); err != nil {
		return err
	}
	if err := encryptBackupAssetField(&plan.EncryptedTargetRelativePath); err != nil {
		return err
	}
	return encryptBackupAssetField(&plan.EncryptedOverrideReason)
}

func (plan *BackupAssetRecoveryPlan) AfterFind(_ *gorm.DB) error {
	if err := decryptBackupAssetField(&plan.EncryptedSourceLocator); err != nil {
		return err
	}
	if err := decryptBackupAssetField(&plan.EncryptedTargetRootLocator); err != nil {
		return err
	}
	if err := decryptBackupAssetField(&plan.EncryptedTargetRelativePath); err != nil {
		return err
	}
	return decryptBackupAssetField(&plan.EncryptedOverrideReason)
}

type BackupAssetRecoveryPlanItem struct {
	ID                  string    `gorm:"primaryKey;size:32" json:"id"`
	PlanID              string    `gorm:"size:32;not null;index" json:"plan_id"`
	Ordinal             int       `gorm:"not null" json:"ordinal"`
	RecoveryPointID     string    `gorm:"size:32;not null" json:"recovery_point_id"`
	CatalogGenerationID string    `gorm:"size:32;not null" json:"catalog_generation_id"`
	EntryID             string    `gorm:"size:64;not null" json:"entry_id"`
	EntryType           string    `gorm:"size:16;not null" json:"-"`
	SourceFingerprint   string    `gorm:"size:64;not null;default:''" json:"-"`
	RelativePathDigest  string    `gorm:"size:64;not null" json:"-"`
	CreatedAt           time.Time `gorm:"not null" json:"created_at"`
}

func (BackupAssetRecoveryPlanItem) TableName() string { return "backup_asset_recovery_plan_items" }

type BackupAssetRecoveryPreflight struct {
	ID                              string    `gorm:"primaryKey;size:32" json:"id"`
	PlanID                          string    `gorm:"size:32;not null;index" json:"plan_id"`
	Revision                        string    `gorm:"size:64;not null" json:"-"`
	SourceRevisionDigest            string    `gorm:"size:64;not null" json:"-"`
	TargetNodeID                    uint      `gorm:"not null" json:"-"`
	NodeRevision                    string    `gorm:"size:64;not null" json:"-"`
	TargetRootID                    string    `gorm:"size:32;not null" json:"-"`
	RootLocatorDigest               string    `gorm:"size:64;not null" json:"-"`
	PathDigest                      string    `gorm:"size:64;not null" json:"-"`
	TargetRevision                  string    `gorm:"size:64;not null" json:"-"`
	CapabilityRevision              string    `gorm:"size:64;not null" json:"-"`
	PolicyRevision                  string    `gorm:"size:64;not null" json:"-"`
	FindingSetDigest                string    `gorm:"size:64;not null" json:"-"`
	SecurityOverrideCandidateDigest string    `gorm:"size:64;not null;default:''" json:"-"`
	SecurityOverrideCategories      string    `gorm:"size:96;not null;default:''" json:"-"`
	OperationSetDigest              string    `gorm:"size:64;not null" json:"-"`
	DeleteSetDigest                 string    `gorm:"size:64;not null" json:"-"`
	EncryptedOperationRows          string    `gorm:"type:text;not null" json:"-"`
	EstimatedItems                  int64     `gorm:"not null;default:0" json:"estimated_items"`
	EstimatedBytes                  int64     `gorm:"not null;default:0" json:"estimated_bytes"`
	ExpiresAt                       time.Time `gorm:"not null" json:"expires_at"`
	CreatedAt                       time.Time `gorm:"not null" json:"created_at"`
}

func (BackupAssetRecoveryPreflight) TableName() string { return "backup_asset_recovery_preflights" }

func (preflight *BackupAssetRecoveryPreflight) BeforeSave(_ *gorm.DB) error {
	return encryptBackupAssetField(&preflight.EncryptedOperationRows)
}

func (preflight *BackupAssetRecoveryPreflight) AfterFind(_ *gorm.DB) error {
	return decryptBackupAssetField(&preflight.EncryptedOperationRows)
}

type BackupAssetRecoveryGrant struct {
	ID                   string     `gorm:"primaryKey;size:32" json:"id"`
	PlanID               string     `gorm:"size:32;not null;index" json:"plan_id"`
	JobID                *string    `gorm:"size:32;index" json:"job_id,omitempty"`
	AuthorityCategory    string     `gorm:"size:32;not null" json:"-"`
	GrantHash            string     `gorm:"size:64;not null" json:"-"`
	ActorUserID          uint       `gorm:"not null" json:"actor_user_id"`
	ActorSessionID       string     `gorm:"size:64;not null" json:"-"`
	BindingDigest        string     `gorm:"size:64;not null" json:"-"`
	EncryptedReason      string     `gorm:"type:text;not null;default:''" json:"-"`
	DeleteCheckpointID   *string    `gorm:"size:32" json:"-"`
	DeleteSetDigest      string     `gorm:"size:64;not null;default:''" json:"-"`
	DeleteTargetRevision string     `gorm:"size:64;not null;default:''" json:"-"`
	DeleteAttemptID      *string    `gorm:"size:32" json:"-"`
	DeleteAttemptFence   uint64     `gorm:"not null;default:0" json:"-"`
	DeleteNodeFence      uint64     `gorm:"not null;default:0" json:"-"`
	ExpiresAt            time.Time  `gorm:"not null" json:"expires_at"`
	ConsumedAt           *time.Time `json:"consumed_at,omitempty"`
	RevokedAt            *time.Time `json:"revoked_at,omitempty"`
	CreatedAt            time.Time  `gorm:"not null" json:"created_at"`
	UpdatedAt            time.Time  `gorm:"not null" json:"updated_at"`
}

func (BackupAssetRecoveryGrant) TableName() string { return "backup_asset_recovery_grants" }

func (grant *BackupAssetRecoveryGrant) BeforeSave(_ *gorm.DB) error {
	return encryptBackupAssetField(&grant.EncryptedReason)
}

func (grant *BackupAssetRecoveryGrant) AfterFind(_ *gorm.DB) error {
	return decryptBackupAssetField(&grant.EncryptedReason)
}

type BackupAssetRecoveryJob struct {
	ID                                    string     `gorm:"primaryKey;size:32" json:"id"`
	PlanID                                string     `gorm:"size:32;not null;uniqueIndex:idx_backup_asset_recovery_jobs_plan" json:"-"`
	PlanBindingDigest                     string     `gorm:"size:64;not null" json:"-"`
	SelectionDigest                       string     `gorm:"size:64;not null" json:"-"`
	SourceRevisionDigest                  string     `gorm:"size:64;not null" json:"-"`
	PreflightID                           string     `gorm:"size:32;not null" json:"-"`
	PreflightRevision                     string     `gorm:"size:64;not null" json:"-"`
	PreflightExpiresAt                    time.Time  `gorm:"not null" json:"-"`
	PreflightTargetRevision               string     `gorm:"size:64;not null" json:"-"`
	PreflightNodeRevision                 string     `gorm:"size:64;not null" json:"-"`
	CapabilityRevision                    string     `gorm:"size:64;not null" json:"-"`
	OperationSetDigest                    string     `gorm:"size:64;not null" json:"-"`
	DeleteSetDigest                       string     `gorm:"size:64;not null" json:"-"`
	SecurityDecision                      string     `gorm:"size:32;not null" json:"-"`
	SecurityDecisionDigest                string     `gorm:"size:64;not null" json:"-"`
	SecurityFindingSetDigest              string     `gorm:"size:64;not null" json:"-"`
	SecurityPolicyRevision                string     `gorm:"size:64;not null" json:"-"`
	SecurityOverrideBindingDigest         string     `gorm:"size:64;not null;default:''" json:"-"`
	EstimatedItems                        int64      `gorm:"not null;default:0" json:"-"`
	EstimatedBytes                        int64      `gorm:"not null;default:0" json:"-"`
	AuthorityGrantID                      string     `gorm:"size:32;not null" json:"-"`
	AuthorityCategory                     string     `gorm:"size:32;not null" json:"-"`
	AuthorityBindingDigest                string     `gorm:"size:64;not null" json:"-"`
	AuthorityExpiresAt                    time.Time  `gorm:"not null" json:"-"`
	AuthorityConsumedAt                   time.Time  `gorm:"not null" json:"-"`
	State                                 string     `gorm:"size:32;not null" json:"-"`
	FailureCategory                       string     `gorm:"size:64;not null;default:''" json:"-"`
	TransitionRevision                    uint64     `gorm:"not null;default:1" json:"-"`
	WorkspacePhase                        string     `gorm:"size:32;not null" json:"-"`
	EncryptedWorkspaceRelativeLocator     string     `gorm:"type:text;not null;default:''" json:"-"`
	WorkspaceBindingDigest                string     `gorm:"size:64;not null;default:''" json:"-"`
	WorkspaceMarkerBindingDigest          string     `gorm:"size:64;not null;default:''" json:"-"`
	WorkspaceOwner                        string     `gorm:"size:64;not null;default:''" json:"-"`
	WorkspaceFence                        uint64     `gorm:"not null;default:0" json:"-"`
	WorkspaceMarkerValidationAttemptID    string     `gorm:"size:32;not null;default:''" json:"-"`
	WorkspaceMarkerValidationAttemptFence uint64     `gorm:"not null;default:0" json:"-"`
	WorkspaceMarkerValidationNodeFence    uint64     `gorm:"not null;default:0" json:"-"`
	WorkspaceCleanupPhase                 string     `gorm:"size:32;not null;default:claimed" json:"-"`
	WorkspaceCleanupOwner                 string     `gorm:"size:64;not null;default:''" json:"-"`
	WorkspaceCleanupLeaseExpiresAt        *time.Time `json:"-"`
	WorkspaceCleanupFence                 uint64     `gorm:"not null;default:0" json:"-"`
	WorkspaceCleanupNodeLeaseID           *string    `gorm:"size:32" json:"-"`
	WorkspaceCleanupNodeFence             uint64     `gorm:"not null;default:0" json:"-"`
	WorkspaceCleanupAttempt               uint64     `gorm:"not null;default:0" json:"-"`
	PlaintextDeadline                     *time.Time `json:"plaintext_deadline,omitempty"`
	TargetMode                            string     `gorm:"size:16;not null" json:"-"`
	TargetNodeID                          uint       `gorm:"not null" json:"target_node_id"`
	TargetRootID                          string     `gorm:"size:32;not null" json:"-"`
	RootLocatorDigest                     string     `gorm:"size:64;not null" json:"-"`
	PathDigest                            string     `gorm:"size:64;not null" json:"-"`
	TargetChainRevision                   string     `gorm:"size:64;not null;default:''" json:"-"`
	CreatedAt                             time.Time  `gorm:"not null" json:"created_at"`
	UpdatedAt                             time.Time  `gorm:"not null" json:"updated_at"`
}

func (BackupAssetRecoveryJob) TableName() string { return "backup_asset_recovery_jobs" }

func (job *BackupAssetRecoveryJob) BeforeSave(_ *gorm.DB) error {
	return encryptBackupAssetField(&job.EncryptedWorkspaceRelativeLocator)
}

func (job *BackupAssetRecoveryJob) AfterFind(_ *gorm.DB) error {
	return decryptBackupAssetField(&job.EncryptedWorkspaceRelativeLocator)
}

type BackupAssetRecoveryJobItem struct {
	ID                             string    `gorm:"primaryKey;size:32" json:"id"`
	PlanID                         string    `gorm:"size:32;not null;index" json:"plan_id"`
	JobID                          string    `gorm:"size:32;not null;index" json:"job_id"`
	PlanItemID                     *string   `gorm:"size:32" json:"-"`
	Ordinal                        int       `gorm:"not null" json:"ordinal"`
	OperationKind                  string    `gorm:"size:16;not null" json:"-"`
	TargetPathDigest               string    `gorm:"size:64;not null" json:"-"`
	SemanticTargetDigest           string    `gorm:"size:64;not null" json:"-"`
	TargetObjectDigest             string    `gorm:"size:64;not null" json:"-"`
	ExpectedPriorKind              string    `gorm:"size:16;not null" json:"-"`
	ExpectedPriorDigest            string    `gorm:"size:64;not null;default:''" json:"-"`
	ExpectedPostIdentityDigest     string    `gorm:"size:64;not null;default:''" json:"-"`
	ExpectedPostBytes              int64     `gorm:"not null;default:-1" json:"-"`
	ExpectedPriorBytes             int64     `gorm:"not null;default:-1" json:"-"`
	EncryptedTargetRelativeLocator string    `gorm:"type:text;not null;default:''" json:"-"`
	TargetLocatorKeyVersion        int       `gorm:"not null;default:0" json:"-"`
	TargetLocatorCipherVersion     int       `gorm:"not null;default:0" json:"-"`
	DisplayClass                   string    `gorm:"size:16;not null" json:"-"`
	EstimatedBytes                 int64     `gorm:"not null;default:0" json:"-"`
	Outcome                        string    `gorm:"size:32;not null;default:''" json:"-"`
	BytesWritten                   int64     `gorm:"not null;default:0" json:"bytes_written"`
	VerifiedSize                   int64     `gorm:"not null;default:0" json:"verified_size"`
	VerifiedDigest                 string    `gorm:"size:64;not null;default:''" json:"-"`
	FailureCategory                string    `gorm:"size:64;not null;default:''" json:"-"`
	CreatedAt                      time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt                      time.Time `gorm:"not null" json:"updated_at"`
}

func (BackupAssetRecoveryJobItem) TableName() string { return "backup_asset_recovery_job_items" }

type BackupAssetRecoveryAttempt struct {
	ID             string     `gorm:"primaryKey;size:32" json:"id"`
	JobID          string     `gorm:"size:32;not null;index" json:"job_id"`
	OwnerID        string     `gorm:"size:64;not null" json:"-"`
	Fence          uint64     `gorm:"not null" json:"-"`
	State          string     `gorm:"size:32;not null" json:"-"`
	MutationArmed  bool       `gorm:"not null;default:false" json:"-"`
	LeaseExpiresAt *time.Time `json:"lease_expires_at,omitempty"`
	HeartbeatAt    *time.Time `json:"heartbeat_at,omitempty"`
	ClosedAt       *time.Time `json:"closed_at,omitempty"`
	CreatedAt      time.Time  `gorm:"not null" json:"created_at"`
	UpdatedAt      time.Time  `gorm:"not null" json:"updated_at"`
}

func (BackupAssetRecoveryAttempt) TableName() string { return "backup_asset_recovery_attempts" }

type BackupAssetRecoveryCheckpoint struct {
	ID                        string     `gorm:"primaryKey;size:32" json:"id"`
	JobID                     string     `gorm:"size:32;not null;index" json:"job_id"`
	JobItemID                 string     `gorm:"size:32;not null;default:''" json:"-"`
	AttemptID                 string     `gorm:"size:32;not null" json:"attempt_id"`
	Sequence                  int        `gorm:"not null" json:"sequence"`
	Phase                     string     `gorm:"size:32;not null" json:"-"`
	AuthorityCategory         string     `gorm:"size:32;not null;default:''" json:"-"`
	OperationDigest           string     `gorm:"size:64;not null;default:''" json:"-"`
	PriorTargetRevision       string     `gorm:"size:64;not null;default:''" json:"-"`
	NextTargetRevision        string     `gorm:"size:64;not null;default:''" json:"-"`
	UnresolvedCategory        string     `gorm:"size:32;not null;default:''" json:"-"`
	WriteResultDigest         string     `gorm:"size:64;not null;default:''" json:"-"`
	WriteTargetRevision       string     `gorm:"size:64;not null;default:''" json:"-"`
	ObservationDigest         string     `gorm:"size:64;not null;default:''" json:"-"`
	ObservedTargetRevision    string     `gorm:"size:64;not null;default:''" json:"-"`
	ObservedPresence          string     `gorm:"size:16;not null;default:''" json:"-"`
	SourceRevalidationOutcome string     `gorm:"size:16;not null;default:''" json:"-"`
	NodeFence                 uint64     `gorm:"not null;default:0" json:"-"`
	AttemptFence              uint64     `gorm:"not null;default:0" json:"-"`
	PlanBindingDigest         string     `gorm:"size:64;not null" json:"-"`
	SourceRevisionDigest      string     `gorm:"size:64;not null" json:"-"`
	PreflightID               string     `gorm:"size:32;not null" json:"-"`
	PreflightRevision         string     `gorm:"size:64;not null" json:"-"`
	PreflightExpiresAt        time.Time  `gorm:"not null" json:"-"`
	SecurityDecision          string     `gorm:"size:32;not null" json:"-"`
	SecurityDecisionDigest    string     `gorm:"size:64;not null" json:"-"`
	SecurityFindingSetDigest  string     `gorm:"size:64;not null" json:"-"`
	SecurityPolicyRevision    string     `gorm:"size:64;not null" json:"-"`
	AuthorityGrantID          string     `gorm:"size:32;not null" json:"-"`
	JobAuthorityCategory      string     `gorm:"size:32;not null" json:"-"`
	AuthorityBindingDigest    string     `gorm:"size:64;not null" json:"-"`
	AuthorityExpiresAt        time.Time  `gorm:"not null" json:"-"`
	DeleteNodeRevision        string     `gorm:"size:64;not null;default:''" json:"-"`
	DeleteRootRevision        string     `gorm:"size:64;not null;default:''" json:"-"`
	DeleteAuthorityExpiresAt  *time.Time `json:"-"`
	DeleteGrantID             string     `gorm:"size:32;not null;default:''" json:"-"`
	DeleteGrantBindingDigest  string     `gorm:"size:64;not null;default:''" json:"-"`
	DeleteGrantExpiresAt      *time.Time `json:"-"`
	DeleteGrantConsumedAt     *time.Time `json:"-"`
	CreatedAt                 time.Time  `gorm:"not null" json:"created_at"`
}

func (BackupAssetRecoveryCheckpoint) TableName() string { return "backup_asset_recovery_checkpoints" }

// BackupAssetRecoveryEvidence also stores the distinguished schema-use latch
// and recovery-worker scheduler rows. They are closed row kinds, not delivery
// evidence or separate tables.
type BackupAssetRecoveryEvidence struct {
	ID                             string     `gorm:"primaryKey;size:32" json:"id"`
	JobID                          *string    `gorm:"size:32;index" json:"job_id,omitempty"`
	Kind                           string     `gorm:"size:32;not null" json:"-"`
	Outcome                        string     `gorm:"size:32;not null;default:''" json:"-"`
	SummaryDigest                  string     `gorm:"size:64;not null;default:''" json:"-"`
	DifferenceCount                int64      `gorm:"not null;default:0" json:"difference_count"`
	VerifiedAt                     *time.Time `json:"verified_at,omitempty"`
	PlanID                         *string    `gorm:"size:32;index" json:"-"`
	CheckpointID                   *string    `gorm:"size:32" json:"-"`
	GrantID                        *string    `gorm:"size:32" json:"-"`
	AttemptID                      *string    `gorm:"size:32" json:"-"`
	SourceLeaseID                  *string    `gorm:"size:32" json:"-"`
	NodeLeaseID                    *string    `gorm:"size:32" json:"-"`
	RequesterID                    *uint      `gorm:"index" json:"-"`
	Operation                      string     `gorm:"size:48;not null;default:''" json:"-"`
	Category                       string     `gorm:"size:32;not null;default:''" json:"-"`
	Endpoint                       string     `gorm:"size:96;not null;default:''" json:"-"`
	IdempotencyKeyDigest           string     `gorm:"size:64;not null;default:''" json:"-"`
	IntentDigest                   string     `gorm:"size:64;not null;default:''" json:"-"`
	StepUpJTIDigest                string     `gorm:"column:step_up_jti_digest;size:64;not null;default:''" json:"-"`
	PresentingSessionDigest        string     `gorm:"size:64;not null;default:''" json:"-"`
	PresentingSessionUserID        *uint      `json:"-"`
	PresentingSessionRole          string     `gorm:"size:32;not null;default:''" json:"-"`
	PresentingSessionTokenVersion  uint       `gorm:"not null;default:0" json:"-"`
	ProofExpiresAt                 *time.Time `json:"-"`
	PresentingSessionExpiresAt     *time.Time `json:"-"`
	ReplayExpiresAt                *time.Time `json:"-"`
	ExpectedPlanTransitionRevision uint64     `gorm:"not null;default:0" json:"-"`
	ResultPlanTransitionRevision   uint64     `gorm:"not null;default:0" json:"-"`
	GrantBindingDigest             string     `gorm:"size:64;not null;default:''" json:"-"`
	SourceLeaseBindingDigest       string     `gorm:"size:64;not null;default:''" json:"-"`
	NodeLeaseFence                 uint64     `gorm:"not null;default:0" json:"-"`
	SchedulerScope                 string     `gorm:"size:16;not null;default:''" json:"-"`
	SchedulerCursorAt              *time.Time `json:"-"`
	SchedulerCursorID              string     `gorm:"size:32;not null;default:''" json:"-"`
	SchedulerHighWaterAt           *time.Time `json:"-"`
	SchedulerHighWaterID           string     `gorm:"size:32;not null;default:''" json:"-"`
	SchedulerRevision              uint64     `gorm:"not null;default:0" json:"-"`
	CreatedAt                      time.Time  `gorm:"not null" json:"created_at"`
	UpdatedAt                      time.Time  `gorm:"not null" json:"updated_at"`
}

func (BackupAssetRecoveryEvidence) TableName() string { return "backup_asset_recovery_evidence" }

type BackupAssetRecoveryResultSet struct {
	ID                    string     `gorm:"primaryKey;size:32" json:"id"`
	JobID                 string     `gorm:"size:32;not null;index" json:"job_id"`
	State                 string     `gorm:"size:32;not null" json:"-"`
	MarkerBindingDigest   string     `gorm:"size:64;not null" json:"-"`
	PlaintextDeadline     time.Time  `gorm:"not null" json:"plaintext_deadline"`
	HardDeadline          time.Time  `gorm:"not null" json:"hard_deadline"`
	CleanupPhase          string     `gorm:"size:32;not null" json:"-"`
	CleanupOwner          string     `gorm:"size:64;not null;default:''" json:"-"`
	CleanupLeaseExpiresAt *time.Time `json:"cleanup_lease_expires_at,omitempty"`
	CleanupFence          uint64     `gorm:"not null;default:0" json:"-"`
	NodeLeaseID           *string    `gorm:"size:32" json:"-"`
	NodeFence             uint64     `gorm:"not null;default:0" json:"-"`
	CleanupAttempt        uint64     `gorm:"not null;default:0" json:"-"`
	CreatedAt             time.Time  `gorm:"not null" json:"created_at"`
	UpdatedAt             time.Time  `gorm:"not null" json:"updated_at"`
}

func (BackupAssetRecoveryResultSet) TableName() string { return "backup_asset_recovery_result_sets" }

type BackupAssetRecoveryResult struct {
	ID                           string     `gorm:"primaryKey;size:32" json:"id"`
	ResultSetID                  string     `gorm:"size:32;not null;index" json:"result_set_id"`
	JobID                        string     `gorm:"size:32;not null;index" json:"job_id"`
	ResultKind                   string     `gorm:"size:16;not null" json:"-"`
	Classification               string     `gorm:"size:16;not null" json:"-"`
	ClassificationRevision       int        `gorm:"not null" json:"-"`
	ClassificationSourceRevision int64      `gorm:"not null" json:"-"`
	EncryptedRelativeLocator     string     `gorm:"type:text;not null;default:''" json:"-"`
	LocatorDigest                string     `gorm:"size:64;not null" json:"-"`
	Size                         int64      `gorm:"not null;default:0" json:"size"`
	ContentDigest                string     `gorm:"size:64;not null;default:''" json:"-"`
	ModifiedAt                   *time.Time `json:"modified_at,omitempty"`
	CreatedAt                    time.Time  `gorm:"not null" json:"created_at"`
}

func (BackupAssetRecoveryResult) TableName() string { return "backup_asset_recovery_results" }

func (result *BackupAssetRecoveryResult) BeforeSave(_ *gorm.DB) error {
	return encryptBackupAssetField(&result.EncryptedRelativeLocator)
}

func (result *BackupAssetRecoveryResult) AfterFind(_ *gorm.DB) error {
	return decryptBackupAssetField(&result.EncryptedRelativeLocator)
}

type BackupAssetRecoveryNodeLease struct {
	ID             string     `gorm:"primaryKey;size:32" json:"id"`
	NodeID         uint       `gorm:"not null;index" json:"node_id"`
	HolderKind     string     `gorm:"size:32;not null" json:"-"`
	JobID          string     `gorm:"size:32;not null;index" json:"job_id"`
	AttemptID      *string    `gorm:"size:32;index" json:"attempt_id,omitempty"`
	OwnerID        string     `gorm:"size:64;not null" json:"-"`
	Fence          uint64     `gorm:"not null" json:"-"`
	State          string     `gorm:"size:32;not null" json:"-"`
	LeaseExpiresAt time.Time  `gorm:"column:lease_expires_at;not null" json:"lease_expires_at"`
	ReleasedAt     *time.Time `json:"released_at,omitempty"`
	CreatedAt      time.Time  `gorm:"not null" json:"created_at"`
	UpdatedAt      time.Time  `gorm:"not null" json:"updated_at"`
}

func (BackupAssetRecoveryNodeLease) TableName() string { return "backup_asset_recovery_node_leases" }
