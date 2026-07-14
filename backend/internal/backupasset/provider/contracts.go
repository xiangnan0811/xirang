package provider

import (
	"context"
	"fmt"
	"io"
	"time"

	"xirang/backend/internal/backupasset"
)

const DefaultPageLimit = 100

const (
	defaultMetadataStderrBytes = int64(64 << 10)
	defaultMetadataRecordBytes = int64(1 << 20)
	defaultMetadataMaxItems    = 100_000
)

type IdentityClass string

const (
	IdentityNativeRepository   IdentityClass = "native_repository"
	IdentityTaskScopedEndpoint IdentityClass = "task_scoped_endpoint"
)

type AccessBinding struct {
	Provider      backupasset.ProviderKind `json:"-"`
	RepositoryID  string                   `json:"-"`
	TaskID        uint                     `json:"-"`
	NodeID        uint                     `json:"-"`
	IdentitySalt  []byte                   `json:"-"`
	EndpointFacts []string                 `json:"-"`
	Locator       string                   `json:"-"`
	Secret        []byte                   `json:"-"`
	Config        []byte                   `json:"-"`
	AdapterData   any                      `json:"-"`
}

type OperationLimits struct {
	Timeout          time.Duration
	MaxMetadataBytes int64
	MaxStderrBytes   int64
	MaxRecordBytes   int
	MaxItems         int
}

type OperationLimitsSource func() (OperationLimits, error)

func (limits OperationLimits) Validate() error {
	if limits.Timeout <= 0 || limits.MaxMetadataBytes <= 0 || limits.MaxStderrBytes <= 0 || limits.MaxRecordBytes <= 0 || limits.MaxItems <= 0 {
		return fmt.Errorf("%w: provider operation limits must be positive", backupasset.ErrInvalidState)
	}
	if int64(limits.MaxRecordBytes) > limits.MaxMetadataBytes {
		return fmt.Errorf("%w: provider record limit exceeds metadata limit", backupasset.ErrInvalidState)
	}
	return nil
}

func NewMetadataOperationLimits(timeout time.Duration, metadataBytes int64) (OperationLimits, error) {
	limits := OperationLimits{
		Timeout: timeout, MaxMetadataBytes: metadataBytes, MaxStderrBytes: defaultMetadataStderrBytes,
		MaxRecordBytes: int(min(metadataBytes, defaultMetadataRecordBytes)), MaxItems: defaultMetadataMaxItems,
	}
	if err := limits.Validate(); err != nil {
		return OperationLimits{}, err
	}
	return limits, nil
}

func resolveOperationLimits(source OperationLimitsSource) (OperationLimits, error) {
	if source == nil {
		return OperationLimits{}, fmt.Errorf("%w: provider operation limits source unavailable", backupasset.ErrInvalidState)
	}
	limits, err := source()
	if err != nil {
		return OperationLimits{}, err
	}
	if err := limits.Validate(); err != nil {
		return OperationLimits{}, err
	}
	return limits, nil
}

type RepositoryObservation struct {
	Provider              backupasset.ProviderKind
	IdentityClass         IdentityClass
	RepositoryIdentity    string `json:"-"`
	VersionMode           backupasset.VersionMode
	Capabilities          backupasset.CapabilitySet
	AdapterRevision       string
	SourceRevision        string `json:"-"`
	Availability          backupasset.PhysicalAvailability
	ObservedAt            time.Time
	ConfigFingerprint     string            `json:"-"`
	InternalProviderFacts map[string]string `json:"-"`
}

type ReadSnapshot struct {
	RepositoryID       string
	CapabilityRevision int
	SourceRevision     string
	Access             AccessBinding `json:"-"`
}

type PageRequest struct {
	Limit  int
	Cursor string
}

func (request PageRequest) Normalize(maximum int) (PageRequest, error) {
	if maximum <= 0 {
		return PageRequest{}, fmt.Errorf("%w: invalid maximum page size", backupasset.ErrInvalidState)
	}
	if request.Limit < 0 {
		return PageRequest{}, fmt.Errorf("%w: page limit cannot be negative", backupasset.ErrInvalidState)
	}
	if request.Limit == 0 {
		request.Limit = DefaultPageLimit
	}
	if request.Limit > maximum {
		request.Limit = maximum
	}
	return request, nil
}

type PointLocator struct {
	Native string `json:"-"`
}

type EntryLocator struct {
	Native string `json:"-"`
}

type NativePoint struct {
	OpaqueDigest   string
	CapturedAt     time.Time
	Semantics      backupasset.PointVersionSemantics
	SourceRevision string
	Locator        PointLocator `json:"-"`
}

type NativePointPage struct {
	Items      []NativePoint
	NextCursor string
}

type Entry struct {
	OpaqueDigest   string
	Name           string
	Type           backupasset.CatalogEntryType
	Size           int64
	ModTime        time.Time
	SourceRevision string
	Locator        EntryLocator `json:"-"`
}

type EntryPage struct {
	Items      []Entry
	NextCursor string
}

type ReadRequest struct {
	MaxBytes int64
}

func (request ReadRequest) Validate() error {
	if request.MaxBytes <= 0 {
		return fmt.Errorf("%w: sequential read byte limit must be positive", backupasset.ErrInvalidState)
	}
	return nil
}

type ByteRange struct {
	Offset int64
	Length int64
}

func (value ByteRange) Validate() error {
	if value.Offset < 0 || value.Length <= 0 {
		return fmt.Errorf("%w: invalid byte range", backupasset.ErrInvalidState)
	}
	return nil
}

type ContentStat struct {
	Size           int64
	ModTime        time.Time
	SourceRevision string
	MediaType      string
}

type ReadHandle interface {
	io.Reader
	Close() error
}

type RepositoryProber interface {
	Probe(context.Context, AccessBinding, OperationLimits) (RepositoryObservation, error)
}

type PointLister interface {
	ListPoints(context.Context, ReadSnapshot, PageRequest) (NativePointPage, error)
}

type EntryLister interface {
	ListEntries(context.Context, ReadSnapshot, PointLocator, EntryLocator, PageRequest) (EntryPage, error)
}

type EntryStatter interface {
	StatEntry(context.Context, ReadSnapshot, PointLocator, EntryLocator) (Entry, error)
}

type SequentialReader interface {
	OpenSequential(context.Context, ReadSnapshot, PointLocator, EntryLocator, ReadRequest) (ReadHandle, ContentStat, error)
}

type RangeReader interface {
	OpenRange(context.Context, ReadSnapshot, PointLocator, EntryLocator, ByteRange) (ReadHandle, ContentStat, error)
}

type PublicationAttempt struct {
	Provider             backupasset.ProviderKind
	RepositoryID         string
	RepositoryIdentity   string `json:"-"`
	TaskRepositoryLinkID string
	RecoveryPointID      string
	TaskID               uint
	TaskRunID            uint
	RequiredTags         [2]string `json:"-"`
	PointDeadlineAt      time.Time
	CapabilityRevision   int
	AdapterRevision      string
	Audit                backupasset.PublicationAuditContext `json:"-"`
	Access               AccessBinding                       `json:"-"`
	Fence                backupasset.LeaseFence              `json:"-"`
}

type ProviderCommitEvidence struct {
	Provider           backupasset.ProviderKind
	RepositoryIdentity string `json:"-"`
	NativePointID      string `json:"-"`
	CaptureStartedAt   time.Time
	CaptureFinishedAt  time.Time
	FilesProcessed     uint64
	LogicalBytes       uint64
}

type ResticBackupInput struct {
	Source   string   `json:"-"`
	Excludes []string `json:"-"`
}

type ResticBackupProgress struct {
	ObservedAt     time.Time
	Percent        int
	ThroughputMbps float64
	FilesDone      uint64
}

type ResticBackupResult struct {
	ExitCode       int
	Completion     backupasset.ProviderCompletionClass
	ProviderCommit *ProviderCommitEvidence
	EvidenceCode   backupasset.PublicationFailureCode
}

const UnknownProviderExitCode = -1

type ResticStoredSummary struct {
	BackupStartedAt  time.Time
	BackupFinishedAt time.Time
	FilesProcessed   uint64
	LogicalBytes     uint64
}

type ResticSnapshotObservation struct {
	RepositoryIdentity string `json:"-"`
	NativePointID      string `json:"-"`
	SnapshotTime       time.Time
	Tags               []string `json:"-"`
	OriginalPresent    bool
	Original           *string `json:"-"`
	Summary            *ResticStoredSummary
}

type ManifestLimits struct {
	Timeout        time.Duration
	MaxBytes       int64
	MaxEntries     int64
	MaxRecordBytes int
	MaxDepth       int
}

type ResticManifestFidelity struct {
	Version     int       `json:"version"`
	Profile     string    `json:"profile"`
	Included    [7]string `json:"included"`
	CommitBound [3]string `json:"commit_bound"`
	NotExposed  [7]string `json:"not_exposed"`
}

func ResticManifestFidelityV1() ResticManifestFidelity {
	return ResticManifestFidelity{
		Version: 1,
		Profile: "restic_ls_json_v1",
		Included: [7]string{
			"path_name", "native_type", "regular_file_size", "mode", "uid_gid", "mtime_atime_ctime", "inode",
		},
		CommitBound: [3]string{"repository_identity", "full_snapshot_id", "required_tags"},
		NotExposed: [7]string{
			"link_target", "xattrs", "generic_attributes", "device_link_counts", "content_blob_ids", "subtree_ids", "acl_security_descriptors",
		},
	}
}

type ManifestEvidence struct {
	DigestAlgorithm   string
	Digest            string
	Generator         string
	GeneratorVersion  string
	Completeness      backupasset.ManifestCompleteness
	EntryCount        int64
	LogicalBytes      int64
	Fidelity          ResticManifestFidelity
	HeaderCapturedAt  time.Time
	ObservedTagDigest string
	FailureCode       backupasset.PublicationFailureCode
}

type ResticPublisher interface {
	Backup(context.Context, PublicationAttempt, ResticBackupInput, func(ResticBackupProgress)) (ResticBackupResult, error)
	LookupAttempt(context.Context, PublicationAttempt) ([]ResticSnapshotObservation, error)
}

type ManifestBuilder interface {
	BuildManifest(context.Context, PublicationAttempt, ProviderCommitEvidence, ManifestLimits) (ManifestEvidence, error)
}

type CommandCompletion struct {
	ExitCode        int
	ExitCodeKnown   bool
	Stderr          []byte `json:"-"`
	StderrTruncated bool
}

type CommandExecution interface {
	io.Reader
	Join() (CommandCompletion, error)
	Cancel() error
}

type CommandStreamTransport interface {
	OpenExecution(context.Context, CommandInvocation, OperationLimits, int64) (CommandExecution, error)
}

type PublicationConfigSource func() (backupasset.PublicationConfig, error)
