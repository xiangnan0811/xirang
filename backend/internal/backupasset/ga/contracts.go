package ga

import (
	"context"
	"errors"
	"time"

	"xirang/backend/internal/backupasset"

	"gorm.io/gorm"
)

var errInventoryMustNotMutateProvider = errors.New("inventory dry-run must not mutate provider bytes")

type ConflictKind string

const (
	ConflictSharedResticIdentity   ConflictKind = "shared_restic_identity"
	ConflictTaskRepositoryMismatch ConflictKind = "task_repository_mismatch"
	ConflictCapabilityGap          ConflictKind = "capability_gap"
	ConflictCommandUnsupported     ConflictKind = "command_unsupported"
)

const (
	ReasonSharedResticIdentity     = "backup_assets.ga.shared_restic_identity"
	ReasonCommandUnsupported       = "backup_assets.ga.command_unsupported"
	ReasonCapabilityGap            = "backup_assets.ga.capability_gap"
	ReasonTaskRepositoryMismatch   = "backup_assets.ga.task_repository_mismatch"
	InventoryRunRunning            = "running"
	InventoryRunComplete           = "complete"
	InventoryRunFailed             = "failed"
	InventoryErrorFailed           = "inventory_failed"
	InventoryErrorIncomplete       = "inventory_incomplete"
	InventoryErrorIdentityConflict = "identity_conflict"
	InventoryErrorCapabilityGap    = "capability_gap"
	InventoryErrorCommand          = "command_unsupported"
)

type InventorySource interface {
	LoadFacts(ctx context.Context) (InventoryFacts, error)
}

type InventoryStore interface {
	CurrentClass(ctx context.Context) (InstallationClass, error)
	PersistRun(ctx context.Context, document InventoryDocument) error
	PersistFailedRun(ctx context.Context, category string) error
}

type ProviderMutationSurface interface {
	OpenProvider(ctx context.Context, command string) error
	DiscoverImport(ctx context.Context) error
	Rebuild(ctx context.Context) error
	Purge(ctx context.Context) error
}

type InventoryDependencies struct {
	DB        *gorm.DB
	Source    InventorySource
	Store     InventoryStore
	Mutations ProviderMutationSurface
	Now       func() time.Time
}

type InventoryFacts struct {
	Tasks             []TaskFact
	Repositories      []RepositoryFact
	Links             []LinkFact
	SnapshotIndexes   []SnapshotIndexFact
	HasManagedHistory bool
}

type TaskFact struct {
	TaskID       uint
	ExecutorType string
	IdentityKey  string
	VersionMode  backupasset.VersionMode
}

type RepositoryFact struct {
	ID           string
	ProviderKind backupasset.ProviderKind
	IdentityKey  string
	VersionMode  backupasset.VersionMode
}

type LinkFact struct {
	TaskID       uint
	RepositoryID string
}

type SnapshotIndexFact struct {
	TaskID uint
	Path   string
}

type InventoryDocument struct {
	Class                InstallationClass
	Candidates           []InventoryCandidate
	Conflicts            []InventoryConflict
	Counts               InventoryCounts
	Digest               string
	TrustedSnapshotIndex bool
}

type InventoryCandidate struct {
	Provider         backupasset.ProviderKind
	VersionMode      backupasset.VersionMode
	IdentityKey      string
	RepositoryIDs    []string
	ProducingTaskIDs []uint
	OwnershipMerged  bool
}

type InventoryConflict struct {
	Kind             ConflictKind
	TaskIDs          []uint
	RepositoryID     string
	StableReasonCode string
}

type InventoryCounts struct {
	Candidates     int `json:"candidates"`
	Conflicts      int `json:"conflicts"`
	Unsupported    int `json:"unsupported"`
	CapabilityGaps int `json:"capability_gaps"`
}

type AdminReport struct {
	Snapshot  ReadinessSnapshot
	Inventory InventoryDocument
}
