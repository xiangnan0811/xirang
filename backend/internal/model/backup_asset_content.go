package model

import "time"

// BackupAssetDeliveryGrant stores private, server-authorizing delivery state.
// API responses must use content-domain DTOs instead of serializing this model.
type BackupAssetDeliveryGrant struct {
	ID                           string     `gorm:"primaryKey;size:32" json:"-"`
	DeliveryID                   string     `gorm:"size:32;not null;uniqueIndex" json:"-"`
	ResourceKind                 string     `gorm:"size:32;not null" json:"-"`
	RecoveryPointID              *string    `gorm:"size:32" json:"-"`
	CatalogGenerationID          *string    `gorm:"size:32" json:"-"`
	EntryID                      *string    `gorm:"size:64" json:"-"`
	RecoveryJobID                *string    `gorm:"size:32" json:"-"`
	RecoveryResultID             *string    `gorm:"size:32" json:"-"`
	OwnerUserID                  uint       `gorm:"not null" json:"-"`
	SessionJTI                   string     `gorm:"size:32;not null" json:"-"`
	SessionTokenVersion          uint       `gorm:"not null" json:"-"`
	SessionRole                  string     `gorm:"size:16;not null" json:"-"`
	SessionExpiresAt             time.Time  `gorm:"not null" json:"-"`
	Action                       string     `gorm:"size:16;not null" json:"-"`
	MethodPolicy                 string     `gorm:"size:16;not null" json:"-"`
	RangePolicy                  string     `gorm:"size:16;not null" json:"-"`
	Renderer                     string     `gorm:"size:32;not null" json:"-"`
	Profile                      string     `gorm:"size:32;not null" json:"-"`
	Classification               string     `gorm:"size:16;not null" json:"-"`
	ClassificationRevision       int        `gorm:"not null" json:"-"`
	ClassificationSourceRevision int64      `gorm:"not null" json:"-"`
	StepUpAction                 *string    `gorm:"size:64" json:"-"`
	StepUpProofID                *string    `gorm:"size:32" json:"-"`
	StepUpExpiresAt              *time.Time `json:"-"`
	ProviderKind                 string     `gorm:"size:16;not null" json:"-"`
	SourceFingerprint            string     `gorm:"size:128;not null" json:"-"`
	EntryFingerprint             string     `gorm:"size:128;not null;default:''" json:"-"`
	FingerprintStrength          string     `gorm:"size:16;not null" json:"-"`
	RepresentationETag           string     `gorm:"column:representation_etag;size:160;not null" json:"-"`
	SourceSize                   int64      `gorm:"not null" json:"-"`
	SourceModifiedAt             *time.Time `json:"-"`
	DetectedMediaType            string     `gorm:"size:128;not null" json:"-"`
	RepresentationSourceBytes    int64      `gorm:"not null" json:"-"`
	RepresentationSize           int64      `gorm:"not null" json:"-"`
	RepresentationTruncated      bool       `gorm:"not null" json:"-"`
	CookieSecretHash             string     `gorm:"size:64;not null" json:"-"`
	State                        string     `gorm:"size:16;not null" json:"-"`
	RevocationReason             string     `gorm:"size:64;not null;default:''" json:"-"`
	RevokedAt                    *time.Time `json:"-"`
	LeaseID                      string     `gorm:"size:32;not null;uniqueIndex" json:"-"`
	LeaseAttemptID               string     `gorm:"size:32;not null" json:"-"`
	LeaseFenceTokenHash          string     `gorm:"size:64;not null" json:"-"`
	AbsoluteExpiresAt            time.Time  `gorm:"not null" json:"-"`
	IdleExpiresAt                time.Time  `gorm:"not null" json:"-"`
	IdleTTLSeconds               int64      `gorm:"not null" json:"-"`
	LastActivityAt               time.Time  `gorm:"not null" json:"-"`
	MaxBytesPerRequest           int64      `gorm:"not null" json:"-"`
	MaxCumulativeBytes           int64      `gorm:"not null" json:"-"`
	MaxRequests                  int64      `gorm:"not null" json:"-"`
	MaxInFlight                  int64      `gorm:"not null" json:"-"`
	ReservedBytes                int64      `gorm:"not null;default:0" json:"-"`
	DeliveredBytes               int64      `gorm:"not null;default:0" json:"-"`
	RequestCount                 int64      `gorm:"not null;default:0" json:"-"`
	InFlight                     int64      `gorm:"not null;default:0" json:"-"`
	Version                      int64      `gorm:"not null;default:1" json:"-"`
	AuditState                   string     `gorm:"size:16;not null;default:none" json:"-"`
	AuditRangeCount              int64      `gorm:"not null;default:0" json:"-"`
	AuditRangeBytes              int64      `gorm:"not null;default:0" json:"-"`
	AuditRequestCount            int64      `gorm:"not null;default:0" json:"-"`
	AuditSuccessCount            int64      `gorm:"not null;default:0" json:"-"`
	AuditBlockedCount            int64      `gorm:"not null;default:0" json:"-"`
	AuditFailureCount            int64      `gorm:"not null;default:0" json:"-"`
	AuditFailureCode             string     `gorm:"size:64;not null;default:''" json:"-"`
	AuditAttemptCount            int64      `gorm:"not null;default:0" json:"-"`
	AuditNextAttemptAt           *time.Time `json:"-"`
	CreatedAt                    time.Time  `gorm:"not null" json:"-"`
	UpdatedAt                    time.Time  `gorm:"not null" json:"-"`
}

func (BackupAssetDeliveryGrant) TableName() string { return "backup_asset_delivery_grants" }

type BackupAssetDeliveryRequest struct {
	ID                string     `gorm:"primaryKey;size:32" json:"-"`
	GrantID           string     `gorm:"size:32;not null" json:"-"`
	Method            string     `gorm:"size:8;not null" json:"-"`
	RangeKind         string     `gorm:"size:16;not null" json:"-"`
	RangeStart        *int64     `json:"-"`
	RangeEndExclusive *int64     `json:"-"`
	SuffixLength      *int64     `json:"-"`
	State             string     `gorm:"size:16;not null" json:"-"`
	ReservedBytes     int64      `gorm:"not null;default:0" json:"-"`
	ProviderBytes     int64      `gorm:"not null;default:0" json:"-"`
	ResponseBytes     int64      `gorm:"not null;default:0" json:"-"`
	HTTPStatus        int        `gorm:"not null;default:0" json:"-"`
	FailureCode       string     `gorm:"size:64;not null;default:''" json:"-"`
	StartedAt         time.Time  `gorm:"not null" json:"-"`
	LastProgressAt    time.Time  `gorm:"not null" json:"-"`
	FinishedAt        *time.Time `json:"-"`
	CreatedAt         time.Time  `gorm:"not null" json:"-"`
	UpdatedAt         time.Time  `gorm:"not null" json:"-"`
	Version           int64      `gorm:"not null;default:1" json:"-"`
}

func (BackupAssetDeliveryRequest) TableName() string { return "backup_asset_delivery_requests" }

type BackupAssetDeliveryUsage struct {
	ScopeKind       string    `gorm:"primaryKey;size:16" json:"-"`
	ScopeID         string    `gorm:"primaryKey;size:32" json:"-"`
	WindowStartedAt time.Time `gorm:"not null" json:"-"`
	WindowExpiresAt time.Time `gorm:"not null" json:"-"`
	RequestCount    int64     `gorm:"not null;default:0" json:"-"`
	ReservedBytes   int64     `gorm:"not null;default:0" json:"-"`
	DeliveredBytes  int64     `gorm:"not null;default:0" json:"-"`
	InFlight        int64     `gorm:"not null;default:0" json:"-"`
	Version         int64     `gorm:"not null;default:1" json:"-"`
	UpdatedAt       time.Time `gorm:"not null" json:"-"`
}

func (BackupAssetDeliveryUsage) TableName() string { return "backup_asset_delivery_usage" }
