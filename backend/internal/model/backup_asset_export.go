package model

import "time"

// Export persistence is private aggregate state. API responses use closed
// domain DTOs and never serialize these models directly.
type BackupAssetExportJob struct {
	ID                       string     `gorm:"primaryKey;size:32" json:"-"`
	OwnerUserID              uint       `gorm:"not null" json:"-"`
	LifecycleEnqueueSequence int64      `gorm:"not null;default:1;uniqueIndex:idx_backup_asset_export_jobs_lifecycle_enqueue_sequence" json:"-"`
	SelectionDigest          string     `gorm:"size:64;not null" json:"-"`
	SelectionSchemaVersion   int        `gorm:"not null" json:"-"`
	ArchiveFormat            string     `gorm:"size:16;not null" json:"-"`
	ArchiveProfile           string     `gorm:"size:32;not null" json:"-"`
	LimitsSchemaVersion      int        `gorm:"not null" json:"-"`
	ChunkBytes               int64      `gorm:"not null" json:"-"`
	MaxItems                 int        `gorm:"not null" json:"-"`
	MaxSourcePoints          int        `gorm:"not null" json:"-"`
	MaxItemBytes             int64      `gorm:"not null" json:"-"`
	MaxLogicalBytes          int64      `gorm:"not null" json:"-"`
	MaxProviderBytes         int64      `gorm:"not null" json:"-"`
	MaxCiphertextBytes       int64      `gorm:"not null" json:"-"`
	MaxOpenReaders           int        `gorm:"not null" json:"-"`
	MaxDurationSeconds       int64      `gorm:"not null" json:"-"`
	MaxAttempts              int        `gorm:"not null" json:"-"`
	RetryBaseSeconds         int64      `gorm:"not null" json:"-"`
	RetryMaxDelaySeconds     int64      `gorm:"not null" json:"-"`
	LeaseTTLSeconds          int64      `gorm:"not null" json:"-"`
	LeaseRenewMarginSeconds  int64      `gorm:"not null" json:"-"`
	ReadyTTLSeconds          int64      `gorm:"not null" json:"-"`
	ExecutionState           string     `gorm:"size:32;not null" json:"-"`
	ResultKind               string     `gorm:"size:16;not null;default:''" json:"-"`
	CleanupState             string     `gorm:"size:16;not null" json:"-"`
	CurrentAttemptID         *string    `gorm:"size:32" json:"-"`
	CurrentFenceRevision     int64      `gorm:"not null;default:0" json:"-"`
	AbsoluteDeadline         time.Time  `gorm:"not null" json:"-"`
	ReadyAt                  *time.Time `json:"-"`
	ExpiresAt                *time.Time `json:"-"`
	ItemCount                int64      `gorm:"not null;default:0" json:"-"`
	PackedCount              int64      `gorm:"not null;default:0" json:"-"`
	SkippedCount             int64      `gorm:"not null;default:0" json:"-"`
	FailedCount              int64      `gorm:"not null;default:0" json:"-"`
	LogicalBytes             int64      `gorm:"not null;default:0" json:"-"`
	ProviderBytes            int64      `gorm:"not null;default:0" json:"-"`
	ArtifactBytes            int64      `gorm:"not null;default:0" json:"-"`
	ErrorCategory            string     `gorm:"size:64;not null;default:''" json:"-"`
	TransitionRevision       int64      `gorm:"not null;default:1" json:"-"`
	CreatedAt                time.Time  `gorm:"not null" json:"-"`
	UpdatedAt                time.Time  `gorm:"not null" json:"-"`
}

func (BackupAssetExportJob) TableName() string { return "backup_asset_export_jobs" }

type BackupAssetExportKey struct {
	ID            string     `gorm:"primaryKey;size:32" json:"-"`
	JobID         string     `gorm:"size:32;not null" json:"-"`
	State         string     `gorm:"size:16;not null" json:"-"`
	WrappedDEK    []byte     `gorm:"not null" json:"-"`
	EnvelopeNonce []byte     `gorm:"not null" json:"-"`
	KEKVersion    int        `gorm:"not null" json:"-"`
	WrapAlgorithm string     `gorm:"size:32;not null" json:"-"`
	KeyRevision   int64      `gorm:"not null;default:1" json:"-"`
	CreatedAt     time.Time  `gorm:"not null" json:"-"`
	RewrappedAt   *time.Time `json:"-"`
	DestroyedAt   *time.Time `json:"-"`
}

func (BackupAssetExportKey) TableName() string { return "backup_asset_export_keys" }

type BackupAssetExportItem struct {
	ID                         string     `gorm:"primaryKey;size:32" json:"-"`
	JobID                      string     `gorm:"size:32;not null" json:"-"`
	Ordinal                    int        `gorm:"not null" json:"-"`
	RecoveryPointID            string     `gorm:"size:32;not null" json:"-"`
	EntryID                    string     `gorm:"size:64;not null" json:"-"`
	CatalogGenerationID        string     `gorm:"size:32;not null" json:"-"`
	SourceFingerprint          string     `gorm:"size:128;not null" json:"-"`
	EntryFingerprint           string     `gorm:"size:128;not null" json:"-"`
	FingerprintStrength        string     `gorm:"size:16;not null" json:"-"`
	ProviderCapabilityRevision int64      `gorm:"not null" json:"-"`
	EntryType                  string     `gorm:"size:16;not null" json:"-"`
	LogicalSize                int64      `gorm:"not null" json:"-"`
	MediaType                  string     `gorm:"size:255;not null;default:''" json:"-"`
	RetentionUntil             *time.Time `json:"-"`
	SelectionRootOrdinal       int        `gorm:"not null" json:"-"`
	PathNonce                  []byte     `gorm:"not null" json:"-"`
	PathCiphertext             []byte     `gorm:"not null" json:"-"`
	State                      string     `gorm:"size:16;not null" json:"-"`
	CurrentAttemptID           *string    `gorm:"size:32" json:"-"`
	LogicalBytes               int64      `gorm:"not null;default:0" json:"-"`
	ProviderBytes              int64      `gorm:"not null;default:0" json:"-"`
	ErrorCategory              string     `gorm:"size:64;not null;default:''" json:"-"`
	CreatedAt                  time.Time  `gorm:"not null" json:"-"`
	UpdatedAt                  time.Time  `gorm:"not null" json:"-"`
}

func (BackupAssetExportItem) TableName() string { return "backup_asset_export_items" }

type BackupAssetExportAttempt struct {
	ID                      string     `gorm:"primaryKey;size:32" json:"-"`
	JobID                   string     `gorm:"size:32;not null" json:"-"`
	AttemptNumber           int        `gorm:"not null" json:"-"`
	WorkerOwner             string     `gorm:"size:128;not null" json:"-"`
	State                   string     `gorm:"size:16;not null" json:"-"`
	FenceToken              []byte     `gorm:"not null" json:"-"`
	FenceDigest             string     `gorm:"size:64;not null" json:"-"`
	NoncePrefix             []byte     `gorm:"not null" json:"-"`
	LeaseExpiresAt          time.Time  `gorm:"not null" json:"-"`
	CheckpointOrdinal       int        `gorm:"not null;default:0" json:"-"`
	CheckpointItemCount     int64      `gorm:"not null;default:0" json:"-"`
	CheckpointLogicalBytes  int64      `gorm:"not null;default:0" json:"-"`
	CheckpointProviderBytes int64      `gorm:"not null;default:0" json:"-"`
	StagingLocator          string     `gorm:"size:128;not null;default:''" json:"-"`
	FailureCategory         string     `gorm:"size:64;not null;default:''" json:"-"`
	IsCurrent               bool       `gorm:"not null;default:true" json:"-"`
	StartedAt               time.Time  `gorm:"not null" json:"-"`
	FinishedAt              *time.Time `json:"-"`
	CreatedAt               time.Time  `gorm:"not null" json:"-"`
	UpdatedAt               time.Time  `gorm:"not null" json:"-"`
}

func (BackupAssetExportAttempt) TableName() string { return "backup_asset_export_attempts" }

type BackupAssetExportItemAttempt struct {
	ID            string     `gorm:"primaryKey;size:32" json:"-"`
	JobID         string     `gorm:"size:32;not null" json:"-"`
	ItemID        string     `gorm:"size:32;not null" json:"-"`
	AttemptID     string     `gorm:"size:32;not null" json:"-"`
	State         string     `gorm:"size:16;not null" json:"-"`
	SpoolDigest   string     `gorm:"size:64;not null;default:''" json:"-"`
	SpoolSize     int64      `gorm:"not null;default:0" json:"-"`
	SpoolLocator  string     `gorm:"size:128;not null;default:''" json:"-"`
	LogicalBytes  int64      `gorm:"not null;default:0" json:"-"`
	ProviderBytes int64      `gorm:"not null;default:0" json:"-"`
	ErrorCategory string     `gorm:"size:64;not null;default:''" json:"-"`
	StartedAt     time.Time  `gorm:"not null" json:"-"`
	ReadAt        *time.Time `json:"-"`
	PackedAt      *time.Time `json:"-"`
	FinishedAt    *time.Time `json:"-"`
	CreatedAt     time.Time  `gorm:"not null" json:"-"`
}

func (BackupAssetExportItemAttempt) TableName() string {
	return "backup_asset_export_item_attempts"
}

type BackupAssetExportSourceLease struct {
	ID               string     `gorm:"primaryKey;size:32" json:"-"`
	JobID            string     `gorm:"size:32;not null" json:"-"`
	RecoveryPointID  string     `gorm:"size:32;not null" json:"-"`
	LeaseID          string     `gorm:"size:32;not null" json:"-"`
	LeaseAttemptID   string     `gorm:"size:32;not null" json:"-"`
	FenceHash        string     `gorm:"size:64;not null" json:"-"`
	AbsoluteDeadline time.Time  `gorm:"not null" json:"-"`
	RetentionUntil   *time.Time `json:"-"`
	State            string     `gorm:"size:16;not null" json:"-"`
	AcquiredAt       time.Time  `gorm:"not null" json:"-"`
	RenewedAt        time.Time  `gorm:"not null" json:"-"`
	ReleasedAt       *time.Time `json:"-"`
	CreatedAt        time.Time  `gorm:"not null" json:"-"`
	UpdatedAt        time.Time  `gorm:"not null" json:"-"`
}

func (BackupAssetExportSourceLease) TableName() string {
	return "backup_asset_export_source_leases"
}

type BackupAssetExportArtifact struct {
	ID               string     `gorm:"primaryKey;size:32" json:"-"`
	JobID            string     `gorm:"size:32;not null" json:"-"`
	AttemptID        string     `gorm:"size:32;not null" json:"-"`
	JobKeyID         string     `gorm:"size:32;not null" json:"-"`
	State            string     `gorm:"size:16;not null" json:"-"`
	Locator          string     `gorm:"size:128;not null" json:"-"`
	CipherVersion    int        `gorm:"not null" json:"-"`
	ChunkBytes       int64      `gorm:"not null" json:"-"`
	FormatVersion    int        `gorm:"not null" json:"-"`
	NoncePrefix      []byte     `gorm:"not null" json:"-"`
	ChunkCount       int64      `gorm:"not null;default:0" json:"-"`
	PlaintextDigest  string     `gorm:"size:64;not null;default:''" json:"-"`
	ArchiveDigest    string     `gorm:"size:64;not null;default:''" json:"-"`
	CiphertextDigest string     `gorm:"size:64;not null;default:''" json:"-"`
	PlaintextSize    int64      `gorm:"not null;default:0" json:"-"`
	CiphertextSize   int64      `gorm:"not null;default:0" json:"-"`
	SealedAt         *time.Time `json:"-"`
	ExpiresAt        *time.Time `json:"-"`
	PurgedAt         *time.Time `json:"-"`
	PurgeError       string     `gorm:"size:64;not null;default:''" json:"-"`
	CreatedAt        time.Time  `gorm:"not null" json:"-"`
	UpdatedAt        time.Time  `gorm:"not null" json:"-"`
}

func (BackupAssetExportArtifact) TableName() string { return "backup_asset_export_artifacts" }

type BackupAssetExportIdempotency struct {
	ID                  string    `gorm:"primaryKey;size:32" json:"-"`
	OwnerUserID         uint      `gorm:"not null;uniqueIndex:idx_backup_asset_export_idempotency_slot" json:"-"`
	Endpoint            string    `gorm:"size:64;not null;uniqueIndex:idx_backup_asset_export_idempotency_slot" json:"-"`
	KeyDigest           string    `gorm:"size:64;not null;uniqueIndex:idx_backup_asset_export_idempotency_slot" json:"-"`
	RequestIntentDigest string    `gorm:"size:64;not null" json:"-"`
	State               string    `gorm:"size:16;not null" json:"-"`
	ResultJobID         *string   `gorm:"size:32" json:"-"`
	ExpiresAt           time.Time `gorm:"not null" json:"-"`
	CreatedAt           time.Time `gorm:"not null" json:"-"`
	UpdatedAt           time.Time `gorm:"not null" json:"-"`
}

func (BackupAssetExportIdempotency) TableName() string {
	return "backup_asset_export_idempotency"
}

type BackupAssetExportQuotaBucket struct {
	ID                           string     `gorm:"primaryKey;size:32" json:"-"`
	Scope                        string     `gorm:"size:16;not null;uniqueIndex:idx_backup_asset_export_quota_scope" json:"-"`
	Subject                      string     `gorm:"size:64;not null;uniqueIndex:idx_backup_asset_export_quota_scope" json:"-"`
	TransitionRevision           int64      `gorm:"not null;default:1" json:"-"`
	ActiveJobs                   int64      `gorm:"not null;default:0" json:"-"`
	ActiveWorkers                int64      `gorm:"not null;default:0" json:"-"`
	ActiveReaders                int64      `gorm:"not null;default:0" json:"-"`
	ReservedStoreBytes           int64      `gorm:"not null;default:0" json:"-"`
	UsedStoreBytes               int64      `gorm:"not null;default:0" json:"-"`
	LifecycleNextSequence        int64      `gorm:"not null;default:1" json:"-"`
	LifecycleSweepCursor         int64      `gorm:"not null;default:0" json:"-"`
	LifecycleSweepHighWater      int64      `gorm:"not null;default:0" json:"-"`
	LifecycleSweepRevision       int64      `gorm:"not null;default:0" json:"-"`
	LifecycleSweepLeaseExpiresAt *time.Time `json:"-"`
	ReaderNextSequence           int64      `gorm:"not null;default:1" json:"-"`
	ReaderSweepCursor            int64      `gorm:"not null;default:0" json:"-"`
	ReaderSweepHighWater         int64      `gorm:"not null;default:0" json:"-"`
	ReaderSweepRevision          int64      `gorm:"not null;default:0" json:"-"`
	ReaderSweepLeaseExpiresAt    *time.Time `json:"-"`
	CreatedAt                    time.Time  `gorm:"not null" json:"-"`
	UpdatedAt                    time.Time  `gorm:"not null" json:"-"`
}

func (BackupAssetExportQuotaBucket) TableName() string {
	return "backup_asset_export_quota_buckets"
}

type BackupAssetExportReservation struct {
	ID                    string     `gorm:"primaryKey;size:32" json:"-"`
	BucketID              string     `gorm:"size:32;not null" json:"-"`
	JobID                 *string    `gorm:"size:32" json:"-"`
	AttemptID             *string    `gorm:"size:32" json:"-"`
	Kind                  string     `gorm:"size:16;not null" json:"-"`
	ReaderEnqueueSequence int64      `gorm:"not null;default:0" json:"-"`
	ReservedSlots         int64      `gorm:"not null;default:0" json:"-"`
	ReservedLogicalBytes  int64      `gorm:"not null;default:0" json:"-"`
	ReservedProviderBytes int64      `gorm:"not null;default:0" json:"-"`
	ReservedCipherBytes   int64      `gorm:"not null;default:0" json:"-"`
	ReservedStoreBytes    int64      `gorm:"not null;default:0" json:"-"`
	LeaseOwner            string     `gorm:"size:128;not null" json:"-"`
	LeaseExpiresAt        time.Time  `gorm:"not null" json:"-"`
	State                 string     `gorm:"size:16;not null" json:"-"`
	CreatedAt             time.Time  `gorm:"not null" json:"-"`
	UpdatedAt             time.Time  `gorm:"not null" json:"-"`
	ReleasedAt            *time.Time `json:"-"`
}

func (BackupAssetExportReservation) TableName() string {
	return "backup_asset_export_reservations"
}

type BackupAssetExportDeliveryGrant struct {
	ID                     string     `gorm:"primaryKey;size:32" json:"-"`
	DeliveryID             string     `gorm:"size:32;not null" json:"-"`
	ResourceKind           string     `gorm:"size:32;not null" json:"-"`
	ExportJobID            *string    `gorm:"size:32" json:"-"`
	ExportArtifactID       *string    `gorm:"size:32" json:"-"`
	ExportAttemptID        *string    `gorm:"size:32" json:"-"`
	ExportFenceDigest      string     `gorm:"size:64;not null;default:''" json:"-"`
	SelectionDigest        string     `gorm:"size:64;not null;default:''" json:"-"`
	ArtifactDigest         string     `gorm:"size:64;not null;default:''" json:"-"`
	PlaintextSize          int64      `gorm:"not null;default:0" json:"-"`
	CiphertextSize         int64      `gorm:"not null;default:0" json:"-"`
	FormatVersion          int        `gorm:"not null;default:0" json:"-"`
	ChunkBytes             int64      `gorm:"not null;default:0" json:"-"`
	JobKeyID               *string    `gorm:"size:32" json:"-"`
	JobKeyVersion          int        `gorm:"not null;default:0" json:"-"`
	MemberRequestID        *string    `gorm:"size:32" json:"-"`
	OuterRecoveryPointID   string     `gorm:"size:32;not null;default:''" json:"-"`
	OuterEntryID           string     `gorm:"size:64;not null;default:''" json:"-"`
	OuterSourceFingerprint string     `gorm:"size:128;not null;default:''" json:"-"`
	OuterEntryFingerprint  string     `gorm:"size:128;not null;default:''" json:"-"`
	MemberChainDigest      string     `gorm:"size:64;not null;default:''" json:"-"`
	ProcessingJobID        *string    `gorm:"size:32" json:"-"`
	ProcessingAttemptID    *string    `gorm:"size:32" json:"-"`
	DerivedArtifactSetID   *string    `gorm:"size:32" json:"-"`
	DerivedArtifactID      *string    `gorm:"size:32" json:"-"`
	DerivedBlobID          *string    `gorm:"size:32" json:"-"`
	DerivedDigest          string     `gorm:"size:64;not null;default:''" json:"-"`
	DerivedSize            int64      `gorm:"not null;default:0" json:"-"`
	OwnerUserID            uint       `gorm:"not null" json:"-"`
	SessionJTI             string     `gorm:"size:128;not null" json:"-"`
	TokenVersion           int64      `gorm:"not null" json:"-"`
	RoleRevision           int64      `gorm:"not null" json:"-"`
	ProofAction            string     `gorm:"size:64;not null" json:"-"`
	ProofID                string     `gorm:"size:64;not null" json:"-"`
	ProofExpiresAt         time.Time  `gorm:"not null" json:"-"`
	CookieSecretHash       string     `gorm:"size:64;not null" json:"-"`
	Action                 string     `gorm:"size:32;not null" json:"-"`
	CanonicalPath          string     `gorm:"size:255;not null" json:"-"`
	MethodPolicy           string     `gorm:"size:16;not null" json:"-"`
	RangePolicy            string     `gorm:"size:16;not null" json:"-"`
	State                  string     `gorm:"size:16;not null" json:"-"`
	RevokeReason           string     `gorm:"size:64;not null;default:''" json:"-"`
	IdleExpiresAt          time.Time  `gorm:"not null" json:"-"`
	AbsoluteExpiresAt      time.Time  `gorm:"not null" json:"-"`
	MaxRequests            int64      `gorm:"not null" json:"-"`
	MaxCumulativeBytes     int64      `gorm:"not null" json:"-"`
	MaxInFlight            int64      `gorm:"not null" json:"-"`
	RequestCount           int64      `gorm:"not null;default:0" json:"-"`
	ReservedBytes          int64      `gorm:"not null;default:0" json:"-"`
	ConsumedBytes          int64      `gorm:"not null;default:0" json:"-"`
	InFlight               int64      `gorm:"not null;default:0" json:"-"`
	AuditState             string     `gorm:"size:16;not null;default:none" json:"-"`
	AuditRangeCount        int64      `gorm:"not null;default:0" json:"-"`
	AuditRangeBytes        int64      `gorm:"not null;default:0" json:"-"`
	AuditRequestCount      int64      `gorm:"not null;default:0" json:"-"`
	AuditSuccessCount      int64      `gorm:"not null;default:0" json:"-"`
	AuditBlockedCount      int64      `gorm:"not null;default:0" json:"-"`
	AuditFailureCount      int64      `gorm:"not null;default:0" json:"-"`
	AuditFailureCode       string     `gorm:"size:64;not null;default:''" json:"-"`
	AuditAttemptCount      int64      `gorm:"not null;default:0" json:"-"`
	AuditNextAttemptAt     *time.Time `json:"-"`
	IssuedAt               time.Time  `gorm:"not null" json:"-"`
	LastAccessAt           *time.Time `json:"-"`
	RevokedAt              *time.Time `json:"-"`
	CreatedAt              time.Time  `gorm:"not null" json:"-"`
	UpdatedAt              time.Time  `gorm:"not null" json:"-"`
	Version                int64      `gorm:"not null;default:1" json:"-"`
}

func (BackupAssetExportDeliveryGrant) TableName() string {
	return "backup_asset_export_delivery_grants"
}

type BackupAssetExportDeliveryRequest struct {
	ID              string     `gorm:"primaryKey;size:32" json:"-"`
	GrantID         string     `gorm:"size:32;not null" json:"-"`
	Method          string     `gorm:"size:8;not null" json:"-"`
	RangeRequested  bool       `gorm:"not null;default:false" json:"-"`
	RangeOffset     *int64     `json:"-"`
	RangeLength     *int64     `json:"-"`
	State           string     `gorm:"size:16;not null" json:"-"`
	ReservedBytes   int64      `gorm:"not null;default:0" json:"-"`
	PlaintextBytes  int64      `gorm:"not null;default:0" json:"-"`
	CiphertextBytes int64      `gorm:"not null;default:0" json:"-"`
	FailureCode     string     `gorm:"size:64;not null;default:''" json:"-"`
	StartedAt       time.Time  `gorm:"not null" json:"-"`
	FinishedAt      *time.Time `json:"-"`
	CreatedAt       time.Time  `gorm:"not null" json:"-"`
	UpdatedAt       time.Time  `gorm:"not null" json:"-"`
}

func (BackupAssetExportDeliveryRequest) TableName() string {
	return "backup_asset_export_delivery_requests"
}

type BackupAssetArchiveMemberRequest struct {
	ID                   string     `gorm:"primaryKey;size:32" json:"-"`
	OwnerUserID          uint       `gorm:"not null" json:"-"`
	Endpoint             string     `gorm:"size:64;not null" json:"-"`
	KeyDigest            string     `gorm:"size:64;not null" json:"-"`
	RequestIntentDigest  string     `gorm:"size:64;not null" json:"-"`
	RecoveryPointID      string     `gorm:"size:32;not null" json:"-"`
	EntryID              string     `gorm:"size:64;not null" json:"-"`
	CatalogGenerationID  string     `gorm:"size:32;not null" json:"-"`
	SourceFingerprint    string     `gorm:"size:128;not null" json:"-"`
	EntryFingerprint     string     `gorm:"size:128;not null" json:"-"`
	IndexArtifactID      string     `gorm:"size:32;not null" json:"-"`
	IndexRevision        string     `gorm:"size:64;not null" json:"-"`
	MemberChainDigest    string     `gorm:"size:64;not null" json:"-"`
	ResolvedOrdinal      int        `gorm:"not null" json:"-"`
	ProcessingInterestID *string    `gorm:"size:32" json:"-"`
	ProcessingJobID      *string    `gorm:"size:32" json:"-"`
	State                string     `gorm:"size:16;not null" json:"-"`
	ErrorCategory        string     `gorm:"size:64;not null;default:''" json:"-"`
	IdempotencyExpiresAt time.Time  `gorm:"not null" json:"-"`
	AbsoluteExpiresAt    time.Time  `gorm:"not null" json:"-"`
	CreatedAt            time.Time  `gorm:"not null" json:"-"`
	UpdatedAt            time.Time  `gorm:"not null" json:"-"`
	FinishedAt           *time.Time `json:"-"`
	Version              int64      `gorm:"not null;default:1" json:"-"`
}

func (BackupAssetArchiveMemberRequest) TableName() string {
	return "backup_asset_archive_member_requests"
}
