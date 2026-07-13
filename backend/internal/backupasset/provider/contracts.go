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

type PublicationAttempt struct{}
type ProviderCommitEvidence struct{}
type RepositoryRef struct{}
type ManifestStream interface{ io.ReadCloser }
type FencingToken string

type PointPublisher interface {
	Publish(context.Context, PublicationAttempt) (ProviderCommitEvidence, error)
}

type ManifestBuilder interface {
	BuildManifest(context.Context, RepositoryRef, PointLocator) (ManifestStream, error)
}

type RepositoryReconciler interface {
	Reconcile(context.Context, RepositoryRef) (RepositoryObservation, error)
}

type PointDeleter interface {
	DeletePoint(context.Context, RepositoryRef, PointLocator, FencingToken) error
}
