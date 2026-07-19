package model

import "time"

// BackupAssetProcessingJob is private coordinator state. Protocol and public
// API responses must use sanitized domain DTOs instead of serializing models.
type BackupAssetProcessingJob struct {
	ID                         string     `gorm:"primaryKey;size:32" json:"-"`
	WorkKey                    string     `gorm:"size:64;not null" json:"-"`
	DescriptorSchemaVersion    int        `gorm:"not null" json:"-"`
	DescriptorCanonical        []byte     `gorm:"not null" json:"-"`
	RecoveryPointID            string     `gorm:"size:32;not null" json:"-"`
	CatalogGenerationID        string     `gorm:"size:32;not null" json:"-"`
	EntryID                    string     `gorm:"size:64;not null" json:"-"`
	SourceFingerprint          string     `gorm:"size:128;not null" json:"-"`
	EntryFingerprint           string     `gorm:"size:128;not null;default:''" json:"-"`
	ProviderCapabilityRevision int64      `gorm:"not null" json:"-"`
	Capability                 string     `gorm:"size:64;not null" json:"-"`
	CapabilitySchema           string     `gorm:"size:64;not null" json:"-"`
	PipelineFingerprint        string     `gorm:"size:128;not null" json:"-"`
	OutputProfile              string     `gorm:"size:64;not null" json:"-"`
	SecurityPolicyRevision     string     `gorm:"size:128;not null" json:"-"`
	PriorityClass              string     `gorm:"size:16;not null" json:"-"`
	EffectivePriority          int        `gorm:"not null" json:"-"`
	State                      string     `gorm:"size:32;not null" json:"-"`
	TransitionRevision         int64      `gorm:"not null;default:1" json:"-"`
	ErrorCode                  string     `gorm:"size:64;not null;default:''" json:"-"`
	RetryCount                 int        `gorm:"not null;default:0" json:"-"`
	RetryAt                    *time.Time `json:"-"`
	CancelReason               string     `gorm:"size:64;not null;default:''" json:"-"`
	SupersedeReason            string     `gorm:"size:64;not null;default:''" json:"-"`
	ExpiryReason               string     `gorm:"size:64;not null;default:''" json:"-"`
	CurrentAttemptID           *string    `gorm:"size:32" json:"-"`
	CurrentArtifactSetID       *string    `gorm:"size:32" json:"-"`
	IsCurrent                  bool       `gorm:"not null;default:true" json:"-"`
	QueuedAt                   time.Time  `gorm:"not null" json:"-"`
	StartedAt                  *time.Time `json:"-"`
	FinishedAt                 *time.Time `json:"-"`
	AbsoluteDeadline           time.Time  `gorm:"not null" json:"-"`
	CreatedAt                  time.Time  `gorm:"not null" json:"-"`
	UpdatedAt                  time.Time  `gorm:"not null" json:"-"`
	Version                    int64      `gorm:"not null;default:1" json:"-"`
}

func (BackupAssetProcessingJob) TableName() string { return "backup_asset_processing_jobs" }

type BackupAssetProcessingInterest struct {
	ID            string     `gorm:"primaryKey;size:32" json:"-"`
	JobID         string     `gorm:"size:32;not null" json:"-"`
	OwnerKind     string     `gorm:"size:16;not null" json:"-"`
	OwnerKey      string     `gorm:"size:128;not null" json:"-"`
	PriorityClass string     `gorm:"size:16;not null" json:"-"`
	Priority      int        `gorm:"not null" json:"-"`
	Active        bool       `gorm:"not null;default:true" json:"-"`
	RemovedReason string     `gorm:"size:32;not null;default:''" json:"-"`
	CreatedAt     time.Time  `gorm:"not null" json:"-"`
	UpdatedAt     time.Time  `gorm:"not null" json:"-"`
	RemovedAt     *time.Time `json:"-"`
}

func (BackupAssetProcessingInterest) TableName() string {
	return "backup_asset_processing_interests"
}

type BackupAssetProcessingAttempt struct {
	ID                     string     `gorm:"primaryKey;size:32" json:"-"`
	JobID                  string     `gorm:"size:32;not null" json:"-"`
	AttemptNumber          int        `gorm:"not null" json:"-"`
	WorkerID               string     `gorm:"size:32;not null" json:"-"`
	SlotClass              string     `gorm:"size:32;not null" json:"-"`
	State                  string     `gorm:"size:16;not null" json:"-"`
	WorkerLeaseExpiresAt   time.Time  `gorm:"not null" json:"-"`
	LastHeartbeatAt        time.Time  `gorm:"not null" json:"-"`
	RecoveryPointLeaseID   string     `gorm:"size:32;not null" json:"-"`
	RecoveryPointAttemptID string     `gorm:"size:32;not null" json:"-"`
	RecoveryPointFenceHash string     `gorm:"size:64;not null" json:"-"`
	AbsoluteDeadline       time.Time  `gorm:"not null" json:"-"`
	OutcomeCode            string     `gorm:"size:64;not null;default:''" json:"-"`
	IsCurrent              bool       `gorm:"not null;default:true" json:"-"`
	StartedAt              time.Time  `gorm:"not null" json:"-"`
	FinishedAt             *time.Time `json:"-"`
	CreatedAt              time.Time  `gorm:"not null" json:"-"`
	UpdatedAt              time.Time  `gorm:"not null" json:"-"`
}

func (BackupAssetProcessingAttempt) TableName() string {
	return "backup_asset_processing_attempts"
}

type BackupAssetProcessingGrant struct {
	ID                   string     `gorm:"primaryKey;size:32" json:"-"`
	JobID                string     `gorm:"size:32;not null" json:"-"`
	AttemptID            string     `gorm:"size:32;not null" json:"-"`
	WorkerID             string     `gorm:"size:32;not null" json:"-"`
	Kind                 string     `gorm:"size:16;not null" json:"-"`
	ActivationSecretHash string     `gorm:"size:64;not null" json:"-"`
	FenceHash            string     `gorm:"size:64;not null" json:"-"`
	State                string     `gorm:"size:16;not null" json:"-"`
	MaxRequests          int64      `gorm:"not null" json:"-"`
	MaxBytesPerRequest   int64      `gorm:"not null" json:"-"`
	MaxCumulativeBytes   int64      `gorm:"not null" json:"-"`
	MaxInFlight          int64      `gorm:"not null" json:"-"`
	RequestCount         int64      `gorm:"not null;default:0" json:"-"`
	ReservedBytes        int64      `gorm:"not null;default:0" json:"-"`
	ConsumedBytes        int64      `gorm:"not null;default:0" json:"-"`
	InFlight             int64      `gorm:"not null;default:0" json:"-"`
	ExpiresAt            time.Time  `gorm:"not null" json:"-"`
	ActivatedAt          *time.Time `json:"-"`
	RevokedAt            *time.Time `json:"-"`
	RevocationReason     string     `gorm:"size:32;not null;default:''" json:"-"`
	CreatedAt            time.Time  `gorm:"not null" json:"-"`
	UpdatedAt            time.Time  `gorm:"not null" json:"-"`
	Version              int64      `gorm:"not null;default:1" json:"-"`
}

func (BackupAssetProcessingGrant) TableName() string { return "backup_asset_processing_grants" }

type BackupAssetProcessingGrantRequest struct {
	ID            string     `gorm:"primaryKey;size:32" json:"-"`
	GrantID       string     `gorm:"size:32;not null" json:"-"`
	RequestKind   string     `gorm:"size:16;not null" json:"-"`
	RangeOffset   *int64     `json:"-"`
	RangeLength   *int64     `json:"-"`
	State         string     `gorm:"size:16;not null" json:"-"`
	ReservedBytes int64      `gorm:"not null;default:0" json:"-"`
	ProviderBytes int64      `gorm:"not null;default:0" json:"-"`
	StoredBytes   int64      `gorm:"not null;default:0" json:"-"`
	FailureCode   string     `gorm:"size:64;not null;default:''" json:"-"`
	StartedAt     time.Time  `gorm:"not null" json:"-"`
	FinishedAt    *time.Time `json:"-"`
	CreatedAt     time.Time  `gorm:"not null" json:"-"`
	UpdatedAt     time.Time  `gorm:"not null" json:"-"`
}

func (BackupAssetProcessingGrantRequest) TableName() string {
	return "backup_asset_processing_grant_requests"
}

type BackupAssetProcessingUpload struct {
	ID                string     `gorm:"primaryKey;size:32" json:"-"`
	JobID             string     `gorm:"size:32;not null" json:"-"`
	AttemptID         string     `gorm:"size:32;not null" json:"-"`
	GrantID           string     `gorm:"size:32;not null" json:"-"`
	Ordinal           int        `gorm:"not null" json:"-"`
	Role              string     `gorm:"size:16;not null" json:"-"`
	MediaType         string     `gorm:"size:128;not null" json:"-"`
	DeclaredSize      int64      `gorm:"not null" json:"-"`
	DeclaredDigest    string     `gorm:"size:64;not null" json:"-"`
	ActualSize        int64      `gorm:"not null;default:0" json:"-"`
	ActualDigest      string     `gorm:"size:64;not null;default:''" json:"-"`
	Completeness      string     `gorm:"size:16;not null" json:"-"`
	CoverageCanonical []byte     `gorm:"not null" json:"-"`
	StagingID         string     `gorm:"size:32;not null" json:"-"`
	State             string     `gorm:"size:16;not null" json:"-"`
	FailureCode       string     `gorm:"size:64;not null;default:''" json:"-"`
	CreatedAt         time.Time  `gorm:"not null" json:"-"`
	UpdatedAt         time.Time  `gorm:"not null" json:"-"`
	FinishedAt        *time.Time `json:"-"`
}

func (BackupAssetProcessingUpload) TableName() string { return "backup_asset_processing_uploads" }

type BackupAssetWorkerIdentity struct {
	ID                   string    `gorm:"primaryKey;size:32" json:"-"`
	TransportKind        string    `gorm:"size:16;not null" json:"-"`
	TransportFingerprint string    `gorm:"size:64;not null" json:"-"`
	InstanceID           string    `gorm:"size:32;not null" json:"-"`
	IdentityRevision     int64     `gorm:"not null;default:1" json:"-"`
	ProtocolVersion      int       `gorm:"not null" json:"-"`
	TrustState           string    `gorm:"size:16;not null" json:"-"`
	HealthState          string    `gorm:"size:16;not null" json:"-"`
	InteractiveSlots     int       `gorm:"not null;default:0" json:"-"`
	BackgroundSlots      int       `gorm:"not null;default:0" json:"-"`
	QuarantineCode       string    `gorm:"size:64;not null;default:''" json:"-"`
	LastSeenAt           time.Time `gorm:"not null" json:"-"`
	CreatedAt            time.Time `gorm:"not null" json:"-"`
	UpdatedAt            time.Time `gorm:"not null" json:"-"`
}

func (BackupAssetWorkerIdentity) TableName() string { return "backup_asset_worker_identities" }

type BackupAssetWorkerCapability struct {
	ID                  string    `gorm:"primaryKey;size:32" json:"-"`
	WorkerID            string    `gorm:"size:32;not null" json:"-"`
	Capability          string    `gorm:"size:64;not null" json:"-"`
	CapabilitySchema    string    `gorm:"size:64;not null" json:"-"`
	PipelineFingerprint string    `gorm:"size:128;not null" json:"-"`
	OutputProfile       string    `gorm:"size:64;not null" json:"-"`
	InputModes          string    `gorm:"size:128;not null" json:"-"`
	LimitsCanonical     []byte    `gorm:"not null" json:"-"`
	AdvertisementDigest string    `gorm:"size:64;not null" json:"-"`
	HealthState         string    `gorm:"size:16;not null" json:"-"`
	CreatedAt           time.Time `gorm:"not null" json:"-"`
	UpdatedAt           time.Time `gorm:"not null" json:"-"`
}

func (BackupAssetWorkerCapability) TableName() string {
	return "backup_asset_worker_capabilities"
}

type BackupAssetDerivedBlob struct {
	ID                  string     `gorm:"primaryKey;size:32" json:"-"`
	PlaintextDigest     string     `gorm:"size:64;not null" json:"-"`
	PlaintextSize       int64      `gorm:"not null" json:"-"`
	PhysicalSize        int64      `gorm:"not null" json:"-"`
	CipherFormatVersion int        `gorm:"not null" json:"-"`
	ChunkSize           int64      `gorm:"not null" json:"-"`
	ChunkCount          int64      `gorm:"not null" json:"-"`
	NoncePrefix         []byte     `gorm:"not null" json:"-"`
	OpaqueLocator       string     `gorm:"size:128;not null" json:"-"`
	WrappedDEK          []byte     `gorm:"column:wrapped_dek;not null" json:"-"`
	EnvelopeNonce       []byte     `gorm:"not null" json:"-"`
	DerivedKEKVersion   int        `gorm:"column:derived_kek_version;not null" json:"-"`
	State               string     `gorm:"size:16;not null" json:"-"`
	RefCount            int64      `gorm:"not null;default:0" json:"-"`
	CreatedAt           time.Time  `gorm:"not null" json:"-"`
	UpdatedAt           time.Time  `gorm:"not null" json:"-"`
	UnavailableAt       *time.Time `json:"-"`
}

func (BackupAssetDerivedBlob) TableName() string { return "backup_asset_derived_blobs" }

type BackupAssetDerivedArtifactSet struct {
	ID                     string     `gorm:"primaryKey;size:32" json:"-"`
	JobID                  string     `gorm:"size:32;not null" json:"-"`
	AttemptID              string     `gorm:"size:32;not null" json:"-"`
	WorkKey                string     `gorm:"size:64;not null" json:"-"`
	RecoveryPointID        string     `gorm:"size:32;not null" json:"-"`
	CatalogGenerationID    string     `gorm:"size:32;not null" json:"-"`
	EntryID                string     `gorm:"size:64;not null" json:"-"`
	SourceFingerprint      string     `gorm:"size:128;not null" json:"-"`
	SecurityPolicyRevision string     `gorm:"size:128;not null" json:"-"`
	ManifestDigest         string     `gorm:"size:64;not null" json:"-"`
	State                  string     `gorm:"size:16;not null" json:"-"`
	RevocationReason       string     `gorm:"size:32;not null;default:''" json:"-"`
	Completeness           string     `gorm:"size:16;not null" json:"-"`
	ArtifactCount          int        `gorm:"not null" json:"-"`
	TotalPlaintextBytes    int64      `gorm:"not null" json:"-"`
	ProjectionRequired     bool       `gorm:"not null;default:false" json:"-"`
	ProjectionPublished    bool       `gorm:"not null;default:false" json:"-"`
	ProjectionRevision     int64      `gorm:"not null;default:0" json:"-"`
	CreatedAt              time.Time  `gorm:"not null" json:"-"`
	UpdatedAt              time.Time  `gorm:"not null" json:"-"`
	RevokedAt              *time.Time `json:"-"`
}

func (BackupAssetDerivedArtifactSet) TableName() string {
	return "backup_asset_derived_artifact_sets"
}

type BackupAssetDerivedArtifact struct {
	ID                string    `gorm:"primaryKey;size:32" json:"-"`
	ArtifactSetID     string    `gorm:"size:32;not null" json:"-"`
	Ordinal           int       `gorm:"not null" json:"-"`
	Role              string    `gorm:"size:16;not null" json:"-"`
	MediaType         string    `gorm:"size:128;not null" json:"-"`
	PlaintextSize     int64     `gorm:"not null" json:"-"`
	PlaintextDigest   string    `gorm:"size:64;not null" json:"-"`
	Completeness      string    `gorm:"size:16;not null" json:"-"`
	CoverageCanonical []byte    `gorm:"not null" json:"-"`
	BlobID            string    `gorm:"size:32;not null" json:"-"`
	ExcerptRef        string    `gorm:"size:128;not null;default:''" json:"-"`
	CreatedAt         time.Time `gorm:"not null" json:"-"`
}

func (BackupAssetDerivedArtifact) TableName() string { return "backup_asset_derived_artifacts" }

type BackupAssetDerivedBlobReference struct {
	ID                  string     `gorm:"primaryKey;size:32" json:"-"`
	BlobID              string     `gorm:"size:32;not null" json:"-"`
	ArtifactID          string     `gorm:"size:32;not null" json:"-"`
	RecoveryPointID     string     `gorm:"size:32;not null" json:"-"`
	CatalogGenerationID string     `gorm:"size:32;not null" json:"-"`
	EntryID             string     `gorm:"size:64;not null" json:"-"`
	SourceFingerprint   string     `gorm:"size:128;not null" json:"-"`
	State               string     `gorm:"size:16;not null" json:"-"`
	CreatedAt           time.Time  `gorm:"not null" json:"-"`
	UpdatedAt           time.Time  `gorm:"not null" json:"-"`
	RevokedAt           *time.Time `json:"-"`
}

func (BackupAssetDerivedBlobReference) TableName() string {
	return "backup_asset_derived_blob_references"
}

type BackupAssetUpdaterMetadata struct {
	ID                    string     `gorm:"primaryKey;size:32" json:"-"`
	SourceKind            string     `gorm:"size:32;not null" json:"-"`
	SourceID              string     `gorm:"size:128;not null" json:"-"`
	Version               string     `gorm:"size:64;not null" json:"-"`
	ManifestDigest        string     `gorm:"size:64;not null" json:"-"`
	SigningKeyFingerprint string     `gorm:"size:64;not null" json:"-"`
	BundleFingerprint     string     `gorm:"size:64;not null" json:"-"`
	State                 string     `gorm:"size:16;not null" json:"-"`
	FailureCode           string     `gorm:"size:64;not null;default:''" json:"-"`
	VerifiedAt            *time.Time `json:"-"`
	ActivatedAt           *time.Time `json:"-"`
	CreatedAt             time.Time  `gorm:"not null" json:"-"`
	UpdatedAt             time.Time  `gorm:"not null" json:"-"`
}

func (BackupAssetUpdaterMetadata) TableName() string { return "backup_asset_updater_metadata" }
