package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
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
	IdentityNativeRepository        IdentityClass = "native_repository"
	IdentityTaskScopedEndpoint      IdentityClass = "task_scoped_endpoint"
	IdentityXirangManagedRepository IdentityClass = "xirang_managed_repository"
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

// ResticAttemptV1 is the provider-owned runtime attempt for a native Restic
// publication. It intentionally keeps access, audit, and fence material out
// of the tagged wire codec below; those values remain process-local and are
// revalidated by the Restic strategy before command execution.
type ResticAttemptV1 struct {
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

// ResticCommitV1 is the provider-owned native snapshot commit fact.
type ResticCommitV1 struct {
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
	ProviderCommit *ResticCommitV1
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

// ResticManifestV1 is the provider-owned manifest result for a native Restic
// snapshot.
type ResticManifestV1 struct {
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
	Backup(context.Context, ResticAttemptV1, ResticBackupInput, func(ResticBackupProgress)) (ResticBackupResult, error)
	LookupAttempt(context.Context, ResticAttemptV1) ([]ResticSnapshotObservation, error)
}

type ManifestBuilder interface {
	BuildManifest(context.Context, ResticAttemptV1, ResticCommitV1, ManifestLimits) (ResticManifestV1, error)
}

const (
	taggedPublicationSchemaV1        = 1
	maxTaggedPublicationPayloadBytes = 64 << 10
	maxTaggedPublicationJSONDepth    = 16
)

// TaggedPublicationAttempt is the only provider-neutral attempt value that a
// coordinator or registry strategy may accept. Exactly one provider payload is
// present, and the payload schema is selected by the explicit provider tag.
// It deliberately has no map, raw JSON, or interface-typed extension field.
type TaggedPublicationAttempt struct {
	Provider  backupasset.ProviderKind
	Version   int
	Restic    *ResticAttemptV1
	RsyncTree *RsyncTreeAttemptV1
	Rclone    *RcloneAttemptV1
}

// RsyncTreeAttemptV1 freezes the provider-visible identity for a future
// managed Rsync tree publication. It contains only opaque IDs, keyed digests,
// and relative component names; roots, locators, SSH arguments, and command
// output never cross the strategy boundary.
type RsyncTreeAttemptV1 struct {
	RepositoryID              string                          `json:"repository_id"`
	TaskRepositoryLinkID      string                          `json:"task_repository_link_id"`
	RecoveryPointID           string                          `json:"recovery_point_id"`
	AttemptID                 string                          `json:"attempt_id"`
	TaskID                    uint                            `json:"task_id"`
	TaskRunID                 uint                            `json:"task_run_id"`
	PublicationMode           backupasset.TaskPublicationMode `json:"publication_mode"`
	SeedFullCopy              bool                            `json:"seed_full_copy"`
	ImportedBaseline          bool                            `json:"imported_baseline"`
	PointDeadlineAt           time.Time                       `json:"point_deadline_at"`
	ExpectedTaskRevision      uint64                          `json:"expected_task_revision"`
	RepositoryMarkerDigest    string                          `json:"repository_marker_digest"`
	ManagedRootIdentityDigest string                          `json:"managed_root_identity_digest"`
	ParentRecoveryPointID     string                          `json:"parent_recovery_point_id,omitempty"`
	ParentCommitDigest        string                          `json:"parent_commit_digest,omitempty"`
	ParentManifestDigest      string                          `json:"parent_manifest_digest,omitempty"`
	StagingComponent          string                          `json:"staging_component"`
	FinalComponent            string                          `json:"final_component"`
	CommandProfileVersion     int                             `json:"command_profile_version"`
	PreflightID               string                          `json:"preflight_id"`
	PreflightDigest           string                          `json:"preflight_digest"`
}

// RsyncTreeCommitV1 is the exact provider commit fact for a managed tree. A
// later Rsync strategy will populate it only after manifest validation and a
// durable NOREPLACE rename; this V1 definition prevents a generic commit map
// from becoming an accidental extension point.
type RsyncTreeCommitV1 struct {
	LayoutVersion           int                                `json:"layout_version"`
	RepositoryID            string                             `json:"repository_id"`
	TaskRepositoryLinkID    string                             `json:"task_repository_link_id"`
	RecoveryPointID         string                             `json:"recovery_point_id"`
	AttemptID               string                             `json:"attempt_id"`
	PublicationMode         backupasset.TaskPublicationMode    `json:"publication_mode"`
	ParentRecoveryPointID   string                             `json:"parent_recovery_point_id,omitempty"`
	ParentCommitDigest      string                             `json:"parent_commit_digest,omitempty"`
	ManifestDigestAlgorithm string                             `json:"manifest_digest_algorithm"`
	ManifestDigest          string                             `json:"manifest_digest"`
	ManifestEntryCount      uint64                             `json:"manifest_entry_count"`
	LogicalBytes            uint64                             `json:"logical_bytes"`
	FidelityDigest          string                             `json:"fidelity_digest"`
	SourceFingerprint       string                             `json:"source_fingerprint"`
	ProviderCommittedAt     time.Time                          `json:"provider_committed_at"`
	CommitMarkerDigest      string                             `json:"commit_marker_digest"`
	ChildFenceDigest        string                             `json:"child_fence_digest"`
	PointDeadlineAt         time.Time                          `json:"point_deadline_at"`
	RenameVerified          bool                               `json:"rename_verified"`
	DirectoryFsyncVerified  bool                               `json:"directory_fsync_verified"`
	FailureCode             backupasset.PublicationFailureCode `json:"failure_code,omitempty"`
}

func NewResticPublicationAttempt(value ResticAttemptV1) TaggedPublicationAttempt {
	if value.Provider == "" {
		value.Provider = backupasset.ProviderRestic
	}
	return TaggedPublicationAttempt{Provider: backupasset.ProviderRestic, Version: taggedPublicationSchemaV1, Restic: &value}
}

func NewRsyncTreePublicationAttempt(value RsyncTreeAttemptV1) TaggedPublicationAttempt {
	return TaggedPublicationAttempt{Provider: backupasset.ProviderRsync, Version: taggedPublicationSchemaV1, RsyncTree: &value}
}

func NewRclonePublicationAttempt(value RcloneAttemptV1) TaggedPublicationAttempt {
	if value.Provider == "" {
		value.Provider = backupasset.ProviderRclone
	}
	return TaggedPublicationAttempt{Provider: backupasset.ProviderRclone, Version: taggedPublicationSchemaV1, Rclone: &value}
}

func (value TaggedPublicationAttempt) Validate() error {
	if value.Version != taggedPublicationSchemaV1 {
		return fmt.Errorf("%w: unsupported tagged publication attempt version", backupasset.ErrInvalidState)
	}
	switch value.Provider {
	case backupasset.ProviderRestic:
		if value.Restic == nil || value.RsyncTree != nil || value.Rclone != nil || value.Restic.Provider != backupasset.ProviderRestic {
			return fmt.Errorf("%w: invalid Restic tagged publication attempt", backupasset.ErrInvalidState)
		}
		return validateResticAttemptDescriptor(*value.Restic)
	case backupasset.ProviderRsync:
		if value.RsyncTree == nil || value.Restic != nil || value.Rclone != nil {
			return fmt.Errorf("%w: invalid Rsync tagged publication attempt", backupasset.ErrInvalidState)
		}
		return value.RsyncTree.Validate()
	case backupasset.ProviderRclone:
		if value.Rclone == nil || value.Restic != nil || value.RsyncTree != nil {
			return fmt.Errorf("%w: invalid Rclone tagged publication attempt", backupasset.ErrInvalidState)
		}
		return value.Rclone.Validate()
	default:
		return fmt.Errorf("%w: unsupported tagged publication provider", backupasset.ErrInvalidState)
	}
}

func (value TaggedPublicationAttempt) ResticAttempt() (ResticAttemptV1, error) {
	if err := value.Validate(); err != nil || value.Provider != backupasset.ProviderRestic || value.Restic == nil {
		if err != nil {
			return ResticAttemptV1{}, err
		}
		return ResticAttemptV1{}, fmt.Errorf("%w: Restic tagged publication attempt required", backupasset.ErrInvalidState)
	}
	return *value.Restic, nil
}

func (value TaggedPublicationAttempt) RsyncTreeAttempt() (RsyncTreeAttemptV1, error) {
	if err := value.Validate(); err != nil || value.Provider != backupasset.ProviderRsync || value.RsyncTree == nil {
		if err != nil {
			return RsyncTreeAttemptV1{}, err
		}
		return RsyncTreeAttemptV1{}, fmt.Errorf("%w: Rsync tagged publication attempt required", backupasset.ErrInvalidState)
	}
	return *value.RsyncTree, nil
}

func (value TaggedPublicationAttempt) RcloneAttempt() (RcloneAttemptV1, error) {
	if err := value.Validate(); err != nil || value.Provider != backupasset.ProviderRclone || value.Rclone == nil {
		if err != nil {
			return RcloneAttemptV1{}, err
		}
		return RcloneAttemptV1{}, fmt.Errorf("%w: Rclone tagged publication attempt required", backupasset.ErrInvalidState)
	}
	return *value.Rclone, nil
}

func (value RsyncTreeAttemptV1) Validate() error {
	if backupasset.ValidateOpaqueID(value.RepositoryID) != nil || backupasset.ValidateOpaqueID(value.TaskRepositoryLinkID) != nil ||
		backupasset.ValidateOpaqueID(value.RecoveryPointID) != nil || backupasset.ValidateOpaqueID(value.AttemptID) != nil ||
		backupasset.ValidateOpaqueID(value.PreflightID) != nil || value.TaskID == 0 || value.TaskRunID == 0 || value.ExpectedTaskRevision == 0 ||
		!validTaggedPublicationTime(value.PointDeadlineAt) || !validTaggedDigest(value.RepositoryMarkerDigest) ||
		!validTaggedDigest(value.ManagedRootIdentityDigest) || !validTaggedDigest(value.PreflightDigest) || value.CommandProfileVersion <= 0 ||
		!validManagedTreeComponent(value.StagingComponent) || !validManagedTreeComponent(value.FinalComponent) ||
		value.StagingComponent != value.RecoveryPointID+"."+value.AttemptID || value.FinalComponent != value.RecoveryPointID {
		return fmt.Errorf("%w: invalid Rsync tree publication attempt", backupasset.ErrInvalidState)
	}
	switch value.PublicationMode {
	case backupasset.PublicationVersionedHardlink:
		if value.SeedFullCopy || backupasset.ValidateOpaqueID(value.ParentRecoveryPointID) != nil || !validTaggedDigest(value.ParentCommitDigest) || !validTaggedDigest(value.ParentManifestDigest) {
			return fmt.Errorf("%w: invalid Rsync hardlink parent", backupasset.ErrInvalidState)
		}
	case backupasset.PublicationVersionedFullCopy:
		if value.ParentRecoveryPointID != "" || value.ParentCommitDigest != "" || value.ParentManifestDigest != "" {
			return fmt.Errorf("%w: full-copy Rsync attempt has a parent", backupasset.ErrInvalidState)
		}
	default:
		return fmt.Errorf("%w: invalid Rsync publication mode", backupasset.ErrInvalidState)
	}
	if value.ImportedBaseline && value.SeedFullCopy {
		return fmt.Errorf("%w: imported Rsync baseline cannot also be a seed", backupasset.ErrInvalidState)
	}
	return nil
}

func validateResticAttemptDescriptor(value ResticAttemptV1) error {
	if value.Provider != backupasset.ProviderRestic || backupasset.ValidateOpaqueID(value.RepositoryID) != nil ||
		backupasset.ValidateOpaqueID(value.TaskRepositoryLinkID) != nil || backupasset.ValidateOpaqueID(value.RecoveryPointID) != nil ||
		value.TaskID == 0 || value.TaskRunID == 0 || value.CapabilityRevision <= 0 || !validTaggedPublicationTime(value.PointDeadlineAt) ||
		!strings.HasPrefix(value.RepositoryIdentity, NativeResticIdentityPrefix) || !lowerHex(strings.TrimPrefix(value.RepositoryIdentity, NativeResticIdentityPrefix), 64) ||
		!validGeneratedResticTag(value.RequiredTags[0], 0) || !validGeneratedResticTag(value.RequiredTags[1], 1) || !validTaggedPublicationLabel(value.AdapterRevision) {
		return fmt.Errorf("%w: invalid Restic publication attempt descriptor", backupasset.ErrInvalidState)
	}
	return nil
}

func validTaggedPublicationTime(value time.Time) bool {
	return !value.IsZero() && value.Location() == time.UTC
}

func validTaggedDigest(value string) bool { return lowerHex(value, 64) }

func validTaggedPublicationLabel(value string) bool {
	if value == "" || len(value) > 64 || strings.ContainsRune(value, '\x00') {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || strings.ContainsRune("._-:", character) {
			continue
		}
		return false
	}
	return true
}

func validManagedTreeComponent(value string) bool {
	if value == "" || len(value) > 65 || value == "." || value == ".." || strings.ContainsRune(value, '\x00') {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || strings.ContainsRune("._-", character) {
			continue
		}
		return false
	}
	return true
}

// ProviderCommit is the provider-neutral, closed counterpart of the legacy
// Restic commit struct. A coordinator can inspect its provider tag but cannot
// attach arbitrary fields or reinterpret one provider's commit as another's.
type ProviderCommit struct {
	Provider  backupasset.ProviderKind
	Version   int
	Restic    *ResticCommitV1
	RsyncTree *RsyncTreeCommitV1
	Rclone    *RcloneCommitV1
}

func NewResticProviderCommit(value ResticCommitV1) ProviderCommit {
	if value.Provider == "" {
		value.Provider = backupasset.ProviderRestic
	}
	return ProviderCommit{Provider: backupasset.ProviderRestic, Version: taggedPublicationSchemaV1, Restic: &value}
}

func NewRsyncTreeProviderCommit(value RsyncTreeCommitV1) ProviderCommit {
	return ProviderCommit{Provider: backupasset.ProviderRsync, Version: taggedPublicationSchemaV1, RsyncTree: &value}
}

func NewRcloneProviderCommit(value RcloneCommitV1) ProviderCommit {
	return ProviderCommit{Provider: backupasset.ProviderRclone, Version: taggedPublicationSchemaV1, Rclone: &value}
}

func (value ProviderCommit) Validate() error {
	if value.Version != taggedPublicationSchemaV1 {
		return fmt.Errorf("%w: unsupported provider commit version", backupasset.ErrInvalidState)
	}
	switch value.Provider {
	case backupasset.ProviderRestic:
		if value.Restic == nil || value.RsyncTree != nil || value.Rclone != nil || value.Restic.Provider != backupasset.ProviderRestic || !validResticCommit(*value.Restic) {
			return fmt.Errorf("%w: invalid Restic provider commit", backupasset.ErrInvalidState)
		}
	case backupasset.ProviderRsync:
		if value.RsyncTree == nil || value.Restic != nil || value.Rclone != nil {
			return fmt.Errorf("%w: invalid Rsync provider commit", backupasset.ErrInvalidState)
		}
		if err := value.RsyncTree.Validate(); err != nil {
			return err
		}
	case backupasset.ProviderRclone:
		if value.Rclone == nil || value.Restic != nil || value.RsyncTree != nil {
			return fmt.Errorf("%w: invalid Rclone provider commit", backupasset.ErrInvalidState)
		}
		if err := value.Rclone.Validate(); err != nil {
			return err
		}
	default:
		return fmt.Errorf("%w: unsupported provider commit provider", backupasset.ErrInvalidState)
	}
	return nil
}

func (value ProviderCommit) ResticCommit() (ResticCommitV1, error) {
	if err := value.Validate(); err != nil || value.Provider != backupasset.ProviderRestic || value.Restic == nil {
		if err != nil {
			return ResticCommitV1{}, err
		}
		return ResticCommitV1{}, fmt.Errorf("%w: Restic provider commit required", backupasset.ErrInvalidState)
	}
	return *value.Restic, nil
}

func (value ProviderCommit) RsyncTreeCommit() (RsyncTreeCommitV1, error) {
	if err := value.Validate(); err != nil || value.Provider != backupasset.ProviderRsync || value.RsyncTree == nil {
		if err != nil {
			return RsyncTreeCommitV1{}, err
		}
		return RsyncTreeCommitV1{}, fmt.Errorf("%w: Rsync provider commit required", backupasset.ErrInvalidState)
	}
	return *value.RsyncTree, nil
}

func (value ProviderCommit) RcloneCommit() (RcloneCommitV1, error) {
	if err := value.Validate(); err != nil || value.Provider != backupasset.ProviderRclone || value.Rclone == nil {
		if err != nil {
			return RcloneCommitV1{}, err
		}
		return RcloneCommitV1{}, fmt.Errorf("%w: Rclone provider commit required", backupasset.ErrInvalidState)
	}
	return *value.Rclone, nil
}

func validResticCommit(value ResticCommitV1) bool {
	return strings.HasPrefix(value.RepositoryIdentity, NativeResticIdentityPrefix) &&
		lowerHex(strings.TrimPrefix(value.RepositoryIdentity, NativeResticIdentityPrefix), 64) && lowerHex(value.NativePointID, 64) &&
		validTaggedPublicationTime(value.CaptureStartedAt) && validTaggedPublicationTime(value.CaptureFinishedAt) &&
		!value.CaptureFinishedAt.Before(value.CaptureStartedAt)
}

func (value RsyncTreeCommitV1) Validate() error {
	if value.LayoutVersion != taggedPublicationSchemaV1 || backupasset.ValidateOpaqueID(value.RepositoryID) != nil ||
		backupasset.ValidateOpaqueID(value.TaskRepositoryLinkID) != nil || backupasset.ValidateOpaqueID(value.RecoveryPointID) != nil ||
		backupasset.ValidateOpaqueID(value.AttemptID) != nil || !validTaggedPublicationTime(value.PointDeadlineAt) {
		return fmt.Errorf("%w: invalid Rsync tree provider commit", backupasset.ErrInvalidState)
	}
	switch value.PublicationMode {
	case backupasset.PublicationVersionedHardlink:
		if backupasset.ValidateOpaqueID(value.ParentRecoveryPointID) != nil || !validTaggedDigest(value.ParentCommitDigest) {
			return fmt.Errorf("%w: invalid Rsync hardlink commit parent", backupasset.ErrInvalidState)
		}
	case backupasset.PublicationVersionedFullCopy:
		if value.ParentRecoveryPointID != "" || value.ParentCommitDigest != "" {
			return fmt.Errorf("%w: full-copy Rsync commit has a parent", backupasset.ErrInvalidState)
		}
	default:
		return fmt.Errorf("%w: invalid Rsync commit mode", backupasset.ErrInvalidState)
	}
	if value.FailureCode != "" {
		if backupasset.ValidatePublicationFailureCode(value.FailureCode) != nil || value.RenameVerified || value.DirectoryFsyncVerified ||
			!value.ProviderCommittedAt.IsZero() || value.ManifestDigestAlgorithm != "" || value.ManifestDigest != "" || value.ManifestEntryCount != 0 || value.LogicalBytes != 0 ||
			value.FidelityDigest != "" || value.SourceFingerprint != "" || value.CommitMarkerDigest != "" || value.ChildFenceDigest != "" {
			return fmt.Errorf("%w: invalid failed Rsync tree provider commit", backupasset.ErrInvalidState)
		}
		return nil
	}
	if value.ManifestDigestAlgorithm != "sha256" || !validTaggedDigest(value.ManifestDigest) || !validTaggedDigest(value.FidelityDigest) ||
		!validTaggedDigest(value.SourceFingerprint) || !validTaggedDigest(value.CommitMarkerDigest) || !validTaggedDigest(value.ChildFenceDigest) ||
		!validTaggedPublicationTime(value.ProviderCommittedAt) || !value.RenameVerified || !value.DirectoryFsyncVerified {
		return fmt.Errorf("%w: incomplete Rsync tree provider commit", backupasset.ErrInvalidState)
	}
	return nil
}

// RsyncTreeManifestV1 reserves a typed manifest result for the managed-tree
// strategy. It deliberately carries only digest-level facts at this boundary.
type RsyncTreeManifestV1 struct {
	DigestAlgorithm string                             `json:"digest_algorithm"`
	Digest          string                             `json:"digest"`
	EntryCount      uint64                             `json:"entry_count"`
	LogicalBytes    uint64                             `json:"logical_bytes"`
	FidelityDigest  string                             `json:"fidelity_digest"`
	FailureCode     backupasset.PublicationFailureCode `json:"failure_code,omitempty"`
}

// ManifestResult is a closed provider-tagged manifest outcome. Restic keeps
// its existing fidelity object internally while Rsync uses only a digest here.
type ManifestResult struct {
	Provider  backupasset.ProviderKind
	Version   int
	Restic    *ResticManifestV1
	RsyncTree *RsyncTreeManifestV1
	Rclone    *RcloneManifestV1
}

func (value ManifestResult) ResticManifest() (ResticManifestV1, error) {
	if value.Version != taggedPublicationSchemaV1 || value.Provider != backupasset.ProviderRestic || value.Restic == nil || value.RsyncTree != nil || value.Rclone != nil {
		return ResticManifestV1{}, fmt.Errorf("%w: Restic manifest result required", backupasset.ErrInvalidState)
	}
	return *value.Restic, nil
}

func (value ManifestResult) RsyncTreeManifest() (RsyncTreeManifestV1, error) {
	if value.Version != taggedPublicationSchemaV1 || value.Provider != backupasset.ProviderRsync || value.RsyncTree == nil || value.Restic != nil || value.Rclone != nil ||
		value.RsyncTree.DigestAlgorithm != "sha256" || !validTaggedDigest(value.RsyncTree.Digest) || !validTaggedDigest(value.RsyncTree.FidelityDigest) || value.RsyncTree.FailureCode != "" {
		return RsyncTreeManifestV1{}, fmt.Errorf("%w: Rsync tree manifest result required", backupasset.ErrInvalidState)
	}
	return *value.RsyncTree, nil
}

func (value ManifestResult) RcloneManifest() (RcloneManifestV1, error) {
	if value.Version != taggedPublicationSchemaV1 || value.Provider != backupasset.ProviderRclone || value.Rclone == nil || value.Restic != nil || value.RsyncTree != nil {
		return RcloneManifestV1{}, fmt.Errorf("%w: Rclone manifest result required", backupasset.ErrInvalidState)
	}
	if err := value.Rclone.Validate(); err != nil {
		return RcloneManifestV1{}, err
	}
	return *value.Rclone, nil
}

// PublicationPrepareRequest and its result carry a closed tagged attempt into
// a provider strategy. Provider-specific execution input is also typed: there
// is no arbitrary configuration map or raw JSON escape hatch.
type PublicationPrepareRequest struct {
	Attempt        TaggedPublicationAttempt
	ResticInput    *ResticBackupInput
	RsyncTreeInput *RsyncTreePublicationInput
	RcloneInput    *RclonePublicationInput
}

type PreparedPublication struct {
	Attempt        TaggedPublicationAttempt
	ResticInput    *ResticBackupInput
	RsyncTreeInput *RsyncTreePublicationInput
	RcloneInput    *RclonePublicationInput
	rsyncTree      *rsyncTreePreparedPublication
}

func (value PublicationPrepareRequest) Validate() error {
	if err := value.Attempt.Validate(); err != nil {
		return err
	}
	switch value.Attempt.Provider {
	case backupasset.ProviderRestic:
		if value.ResticInput == nil || value.RsyncTreeInput != nil || value.RcloneInput != nil {
			return fmt.Errorf("%w: invalid Restic publication prepare request", backupasset.ErrInvalidState)
		}
	case backupasset.ProviderRsync:
		if value.RsyncTreeInput == nil || value.ResticInput != nil || value.RcloneInput != nil {
			return fmt.Errorf("%w: invalid Rsync publication prepare request", backupasset.ErrInvalidState)
		}
	case backupasset.ProviderRclone:
		attempt, err := value.Attempt.RcloneAttempt()
		if err != nil || value.RcloneInput == nil || !value.RcloneInput.validateVariant(attempt.PublicationMode) ||
			value.ResticInput != nil || value.RsyncTreeInput != nil || value.RcloneInput.ManifestLimits.Timeout <= 0 ||
			value.RcloneInput.ManifestLimits.MaxBytes <= 0 || value.RcloneInput.ManifestLimits.MaxEntries <= 0 ||
			value.RcloneInput.ManifestLimits.MaxRecordBytes <= 0 || value.RcloneInput.ManifestLimits.MaxDepth <= 0 {
			return fmt.Errorf("%w: invalid Rclone publication prepare request", backupasset.ErrInvalidState)
		}
	default:
		return fmt.Errorf("%w: unsupported publication prepare provider", backupasset.ErrInvalidState)
	}
	return nil
}

type PublicationProgress struct {
	OnResticProgress func(ResticBackupProgress)
}

type ProviderExecutionResult struct {
	ExitCode       int
	Completion     backupasset.ProviderCompletionClass
	ProviderCommit *ProviderCommit
	EvidenceCode   backupasset.PublicationFailureCode
}

type PublicationReconcileRequest struct {
	Attempt        TaggedPublicationAttempt
	RsyncTreeInput *RsyncTreeReconcileInput
	RcloneInput    *RcloneReconcileInput
}

type PublicationReconcileResult struct {
	ResticObservations []ResticSnapshotObservation
	RsyncTree          *RsyncTreeReconcileV1
	Rclone             *RcloneReconcileV1
}

// RsyncTreeReconcileInput supplies only the repository-owned material needed
// to inspect one exact managed-tree attempt. It intentionally excludes a
// source, command profile, and arbitrary path components.
type RsyncTreeReconcileInput struct {
	ManagedRoot       string `json:"-"`
	MarkerKey         []byte `json:"-"`
	SourceFingerprint string `json:"-"`
	ChildFenceDigest  string `json:"-"`
	ManifestLimits    ManifestLimits
}

type RsyncTreeReconcileState string

const (
	RsyncTreeReconcileAbsent  RsyncTreeReconcileState = "absent"
	RsyncTreeReconcileStaging RsyncTreeReconcileState = "staging"
	RsyncTreeReconcileFinal   RsyncTreeReconcileState = "final"
)

// RsyncTreeReconcileV1 is an exact-fact result. Final carries both the
// authenticated commit marker and a freshly verified canonical manifest;
// absent and staging intentionally carry no inferred provider evidence.
type RsyncTreeReconcileV1 struct {
	State    RsyncTreeReconcileState
	Commit   *RsyncTreeCommitV1
	Manifest *RsyncTreeManifestV1
}

func (value RsyncTreeReconcileV1) Validate() error {
	switch value.State {
	case RsyncTreeReconcileAbsent, RsyncTreeReconcileStaging:
		if value.Commit != nil || value.Manifest != nil {
			return fmt.Errorf("%w: non-final Rsync reconciliation returned provider evidence", backupasset.ErrInvalidState)
		}
		return nil
	case RsyncTreeReconcileFinal:
		if value.Commit == nil || value.Manifest == nil {
			return fmt.Errorf("%w: final Rsync reconciliation evidence is incomplete", backupasset.ErrInvalidState)
		}
		if err := value.Commit.Validate(); err != nil {
			return err
		}
		manifest := value.Manifest
		if manifest.DigestAlgorithm != "sha256" || !validTaggedDigest(manifest.Digest) || !validTaggedDigest(manifest.FidelityDigest) ||
			manifest.FailureCode != "" || manifest.Digest != value.Commit.ManifestDigest || manifest.EntryCount != value.Commit.ManifestEntryCount ||
			manifest.LogicalBytes != value.Commit.LogicalBytes || manifest.FidelityDigest != value.Commit.FidelityDigest {
			return fmt.Errorf("%w: final Rsync reconciliation manifest mismatch", backupasset.ErrInvalidState)
		}
		return nil
	default:
		return fmt.Errorf("%w: unsupported Rsync reconciliation state", backupasset.ErrInvalidState)
	}
}

func (value PublicationReconcileResult) RsyncTreeResult() (RsyncTreeReconcileV1, error) {
	if value.RsyncTree == nil || value.Rclone != nil || len(value.ResticObservations) != 0 {
		return RsyncTreeReconcileV1{}, fmt.Errorf("%w: Rsync reconciliation result required", backupasset.ErrInvalidState)
	}
	if err := value.RsyncTree.Validate(); err != nil {
		return RsyncTreeReconcileV1{}, err
	}
	return *value.RsyncTree, nil
}

func (value PublicationReconcileResult) RcloneResult() (RcloneReconcileV1, error) {
	if value.Rclone == nil || value.RsyncTree != nil || len(value.ResticObservations) != 0 {
		return RcloneReconcileV1{}, fmt.Errorf("%w: Rclone reconciliation result required", backupasset.ErrInvalidState)
	}
	if err := value.Rclone.Validate(); err != nil {
		return RcloneReconcileV1{}, err
	}
	return *value.Rclone, nil
}

// PublicationStrategy is the provider-tagged boundary used by the shared
// coordinator. Implementations must reject every payload not owned by Kind().
// Rsync is intentionally not registered until its managed-tree implementation
// exists; callers therefore fail closed rather than falling back to Restic or
// a mutable executor.
type PublicationStrategy interface {
	Kind() backupasset.ProviderKind
	Prepare(context.Context, PublicationPrepareRequest) (PreparedPublication, error)
	Execute(context.Context, PreparedPublication, PublicationProgress) (ProviderExecutionResult, error)
	RecordCommit(context.Context, PreparedPublication, ProviderExecutionResult) (ProviderCommit, error)
	VerifyOrBuildManifest(context.Context, PreparedPublication, ProviderCommit, ManifestLimits) (ManifestResult, error)
	Reconcile(context.Context, PublicationReconcileRequest) (PublicationReconcileResult, error)
}

type resticAttemptWireV1 struct {
	RepositoryID         string    `json:"repository_id"`
	RepositoryIdentity   string    `json:"repository_identity"`
	TaskRepositoryLinkID string    `json:"task_repository_link_id"`
	RecoveryPointID      string    `json:"recovery_point_id"`
	TaskID               uint      `json:"task_id"`
	TaskRunID            uint      `json:"task_run_id"`
	RequiredTags         [2]string `json:"required_tags"`
	PointDeadlineAt      time.Time `json:"point_deadline_at"`
	CapabilityRevision   int       `json:"capability_revision"`
	AdapterRevision      string    `json:"adapter_revision"`
}

type taggedPublicationAttemptWireV1 struct {
	Provider  backupasset.ProviderKind `json:"provider"`
	Version   int                      `json:"version"`
	Restic    *resticAttemptWireV1     `json:"restic,omitempty"`
	RsyncTree *RsyncTreeAttemptV1      `json:"rsync_tree,omitempty"`
	Rclone    *rcloneAttemptWireV1     `json:"rclone,omitempty"`
}

func EncodePublicationAttempt(value TaggedPublicationAttempt) (string, error) {
	if err := value.Validate(); err != nil {
		return "", err
	}
	wire := taggedPublicationAttemptWireV1{Provider: value.Provider, Version: value.Version}
	if value.Restic != nil {
		wire.Restic = &resticAttemptWireV1{
			RepositoryID: value.Restic.RepositoryID, RepositoryIdentity: value.Restic.RepositoryIdentity,
			TaskRepositoryLinkID: value.Restic.TaskRepositoryLinkID, RecoveryPointID: value.Restic.RecoveryPointID,
			TaskID: value.Restic.TaskID, TaskRunID: value.Restic.TaskRunID, RequiredTags: value.Restic.RequiredTags,
			PointDeadlineAt: value.Restic.PointDeadlineAt.UTC(), CapabilityRevision: value.Restic.CapabilityRevision,
			AdapterRevision: value.Restic.AdapterRevision,
		}
	}
	if value.RsyncTree != nil {
		copy := *value.RsyncTree
		copy.PointDeadlineAt = copy.PointDeadlineAt.UTC()
		wire.RsyncTree = &copy
	}
	if value.Rclone != nil {
		wire.Rclone = rcloneAttemptToWire(*value.Rclone)
	}
	encoded, err := json.Marshal(wire)
	if err != nil {
		return "", fmt.Errorf("encode tagged publication attempt: %w", err)
	}
	return string(encoded), nil
}

func DecodePublicationAttempt(raw string) (TaggedPublicationAttempt, error) {
	if len(raw) == 0 || len(raw) > maxTaggedPublicationPayloadBytes {
		return TaggedPublicationAttempt{}, fmt.Errorf("%w: invalid tagged publication attempt payload size", backupasset.ErrInvalidState)
	}
	decoder, err := strictTaggedPayloadDecoder(raw)
	if err != nil {
		return TaggedPublicationAttempt{}, fmt.Errorf("%w: invalid tagged publication attempt payload", backupasset.ErrInvalidState)
	}
	var wire taggedPublicationAttemptWireV1
	if err := decoder.Decode(&wire); err != nil {
		return TaggedPublicationAttempt{}, fmt.Errorf("%w: invalid tagged publication attempt payload", backupasset.ErrInvalidState)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return TaggedPublicationAttempt{}, fmt.Errorf("%w: trailing tagged publication attempt payload", backupasset.ErrInvalidState)
	}
	value := TaggedPublicationAttempt{Provider: wire.Provider, Version: wire.Version}
	if wire.Restic != nil {
		value.Restic = &ResticAttemptV1{
			Provider: backupasset.ProviderRestic, RepositoryID: wire.Restic.RepositoryID, RepositoryIdentity: wire.Restic.RepositoryIdentity,
			TaskRepositoryLinkID: wire.Restic.TaskRepositoryLinkID, RecoveryPointID: wire.Restic.RecoveryPointID,
			TaskID: wire.Restic.TaskID, TaskRunID: wire.Restic.TaskRunID, RequiredTags: wire.Restic.RequiredTags,
			PointDeadlineAt: wire.Restic.PointDeadlineAt.UTC(), CapabilityRevision: wire.Restic.CapabilityRevision,
			AdapterRevision: wire.Restic.AdapterRevision,
		}
	}
	if wire.RsyncTree != nil {
		copy := *wire.RsyncTree
		copy.PointDeadlineAt = copy.PointDeadlineAt.UTC()
		value.RsyncTree = &copy
	}
	value.Rclone = rcloneAttemptFromWire(wire.Rclone)
	if err := value.Validate(); err != nil {
		return TaggedPublicationAttempt{}, err
	}
	return value, nil
}

func DecodeResticAttemptV1(raw string) (ResticAttemptV1, error) {
	value, err := DecodePublicationAttempt(raw)
	if err != nil {
		return ResticAttemptV1{}, err
	}
	return value.ResticAttempt()
}

func DecodeRsyncTreeAttemptV1(raw string) (RsyncTreeAttemptV1, error) {
	value, err := DecodePublicationAttempt(raw)
	if err != nil {
		return RsyncTreeAttemptV1{}, err
	}
	return value.RsyncTreeAttempt()
}

func DecodeRcloneAttemptV1(raw string) (RcloneAttemptV1, error) {
	value, err := DecodePublicationAttempt(raw)
	if err != nil {
		return RcloneAttemptV1{}, err
	}
	return value.RcloneAttempt()
}

type resticCommitWireV1 struct {
	RepositoryIdentity string    `json:"repository_identity"`
	NativePointID      string    `json:"native_point_id"`
	CaptureStartedAt   time.Time `json:"capture_started_at"`
	CaptureFinishedAt  time.Time `json:"capture_finished_at"`
	FilesProcessed     uint64    `json:"files_processed"`
	LogicalBytes       uint64    `json:"logical_bytes"`
}

type providerCommitWireV1 struct {
	Provider  backupasset.ProviderKind `json:"provider"`
	Version   int                      `json:"version"`
	Restic    *resticCommitWireV1      `json:"restic,omitempty"`
	RsyncTree *RsyncTreeCommitV1       `json:"rsync_tree,omitempty"`
	Rclone    *rcloneCommitWireV1      `json:"rclone,omitempty"`
}

func EncodeProviderCommit(value ProviderCommit) (string, error) {
	if err := value.Validate(); err != nil {
		return "", err
	}
	wire := providerCommitWireV1{Provider: value.Provider, Version: value.Version}
	if value.Restic != nil {
		wire.Restic = &resticCommitWireV1{RepositoryIdentity: value.Restic.RepositoryIdentity, NativePointID: value.Restic.NativePointID,
			CaptureStartedAt: value.Restic.CaptureStartedAt.UTC(), CaptureFinishedAt: value.Restic.CaptureFinishedAt.UTC(),
			FilesProcessed: value.Restic.FilesProcessed, LogicalBytes: value.Restic.LogicalBytes}
	}
	if value.RsyncTree != nil {
		copy := *value.RsyncTree
		copy.ProviderCommittedAt = copy.ProviderCommittedAt.UTC()
		copy.PointDeadlineAt = copy.PointDeadlineAt.UTC()
		wire.RsyncTree = &copy
	}
	if value.Rclone != nil {
		wire.Rclone = rcloneCommitToWire(*value.Rclone)
	}
	encoded, err := json.Marshal(wire)
	if err != nil {
		return "", fmt.Errorf("encode provider commit: %w", err)
	}
	return string(encoded), nil
}

func DecodeProviderCommit(raw string) (ProviderCommit, error) {
	if len(raw) == 0 || len(raw) > maxTaggedPublicationPayloadBytes {
		return ProviderCommit{}, fmt.Errorf("%w: invalid provider commit payload size", backupasset.ErrInvalidState)
	}
	decoder, err := strictTaggedPayloadDecoder(raw)
	if err != nil {
		return ProviderCommit{}, fmt.Errorf("%w: invalid provider commit payload", backupasset.ErrInvalidState)
	}
	var wire providerCommitWireV1
	if err := decoder.Decode(&wire); err != nil {
		return ProviderCommit{}, fmt.Errorf("%w: invalid provider commit payload", backupasset.ErrInvalidState)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ProviderCommit{}, fmt.Errorf("%w: trailing provider commit payload", backupasset.ErrInvalidState)
	}
	value := ProviderCommit{Provider: wire.Provider, Version: wire.Version}
	if wire.Restic != nil {
		value.Restic = &ResticCommitV1{Provider: backupasset.ProviderRestic, RepositoryIdentity: wire.Restic.RepositoryIdentity,
			NativePointID: wire.Restic.NativePointID, CaptureStartedAt: wire.Restic.CaptureStartedAt.UTC(),
			CaptureFinishedAt: wire.Restic.CaptureFinishedAt.UTC(), FilesProcessed: wire.Restic.FilesProcessed, LogicalBytes: wire.Restic.LogicalBytes}
	}
	if wire.RsyncTree != nil {
		copy := *wire.RsyncTree
		copy.ProviderCommittedAt = copy.ProviderCommittedAt.UTC()
		copy.PointDeadlineAt = copy.PointDeadlineAt.UTC()
		value.RsyncTree = &copy
	}
	value.Rclone = rcloneCommitFromWire(wire.Rclone)
	if err := value.Validate(); err != nil {
		return ProviderCommit{}, err
	}
	return value, nil
}

func DecodeRsyncTreeCommitV1(raw string) (RsyncTreeCommitV1, error) {
	value, err := DecodeProviderCommit(raw)
	if err != nil {
		return RsyncTreeCommitV1{}, err
	}
	return value.RsyncTreeCommit()
}

func DecodeRcloneCommitV1(raw string) (RcloneCommitV1, error) {
	value, err := DecodeProviderCommit(raw)
	if err != nil {
		return RcloneCommitV1{}, err
	}
	return value.RcloneCommit()
}

// strictTaggedPayloadDecoder rejects duplicate object members before typed
// decoding. encoding/json otherwise accepts a duplicate key and keeps the
// last value, which would make a signed or persisted provider envelope
// ambiguous across decoders.
func strictTaggedPayloadDecoder(raw string) (*json.Decoder, error) {
	if err := rejectDuplicateJSONMembers(raw); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	return decoder, nil
}

func rejectDuplicateJSONMembers(raw string) error {
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	if err := scanJSONValueForDuplicateMembers(decoder, 0); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("trailing JSON value")
		}
		return fmt.Errorf("trailing JSON token: %w", err)
	}
	return nil
}

func scanJSONValueForDuplicateMembers(decoder *json.Decoder, depth int) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, objectOrArray := token.(json.Delim)
	if !objectOrArray {
		return nil
	}
	if depth >= maxTaggedPublicationJSONDepth {
		return fmt.Errorf("tagged JSON exceeds nesting limit")
	}
	switch delimiter {
	case '{':
		members := make(map[string]struct{})
		for decoder.More() {
			nameToken, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := nameToken.(string)
			if !ok {
				return fmt.Errorf("JSON object member is not a string")
			}
			if _, exists := members[name]; exists {
				return fmt.Errorf("duplicate JSON object member")
			}
			members[name] = struct{}{}
			if err := scanJSONValueForDuplicateMembers(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			if err != nil {
				return err
			}
			return fmt.Errorf("invalid JSON object terminator")
		}
	case '[':
		for decoder.More() {
			if err := scanJSONValueForDuplicateMembers(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			if err != nil {
				return err
			}
			return fmt.Errorf("invalid JSON array terminator")
		}
	default:
		return fmt.Errorf("invalid JSON delimiter")
	}
	return nil
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
