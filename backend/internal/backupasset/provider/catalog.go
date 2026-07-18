package provider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"xirang/backend/internal/backupasset"
)

var (
	ErrCatalogSessionIncomplete = errors.New("provider catalog session is incomplete")
	ErrCatalogSessionClosed     = errors.New("provider catalog session is closed")
	ErrCatalogProtocol          = errors.New("provider catalog protocol violation")
	ErrCatalogProofMismatch     = errors.New("provider catalog proof mismatch")
)

const (
	defaultCatalogSessionPageSize = 200
	maxCatalogSessionPageSize     = 1000
	maxCatalogPointLocatorBytes   = 64 << 10
	maxCatalogEntryPathBytes      = 8192
	catalogProjectionDomain       = "xirang.catalog.projection.v1"
	// ResticCatalogAdapterRevision lets the Repository-owned factory rebuild a
	// Catalog proof descriptor without persisting a new copy of adapter-private
	// runtime state in the RecoveryPoint.
	ResticCatalogAdapterRevision = resticAdapterRevision
)

type CatalogProofMode string

const (
	CatalogProofPublicationManifest CatalogProofMode = "publication_manifest"
	CatalogProofMutableObservation  CatalogProofMode = "mutable_observation"
)

type CatalogManifestProof struct {
	ManifestID      string
	Revision        int
	DigestAlgorithm string
	Digest          string
	EntryCount      int64
	Completeness    backupasset.ManifestCompleteness
	SourceRevision  string
}

type CatalogProjectionProof struct {
	DigestAlgorithm string
	Digest          string
	EntryCount      int64
	Complete        bool
}

type CatalogReadProof struct {
	Provider       backupasset.ProviderKind
	Mode           CatalogProofMode
	SourceRevision string
	Manifest       CatalogManifestProof
	Catalog        CatalogProjectionProof
}

// CatalogProjectionAccumulator is shared by the Provider session and the DB
// indexer so each independently proves the same safe Catalog projection. The
// publication manifest digest remains a separate Provider-specific domain.
type CatalogProjectionAccumulator struct {
	canonical *backupasset.CanonicalSHA256
	count     int64
	finished  bool
}

func NewCatalogProjectionAccumulator(kind backupasset.ProviderKind, repositoryID, recoveryPointID, sourceRevision string) (*CatalogProjectionAccumulator, error) {
	if !readableProvider(kind) || backupasset.ValidateOpaqueID(repositoryID) != nil ||
		backupasset.ValidateOpaqueID(recoveryPointID) != nil || strings.TrimSpace(sourceRevision) == "" ||
		strings.ContainsAny(sourceRevision, "\r\n\x00") {
		return nil, fmt.Errorf("%w: invalid Catalog projection scope", ErrCatalogProtocol)
	}
	canonical := backupasset.NewCanonicalSHA256()
	canonical.String(catalogProjectionDomain)
	canonical.String(string(kind))
	canonical.String(repositoryID)
	canonical.String(recoveryPointID)
	canonical.String(sourceRevision)
	return &CatalogProjectionAccumulator{canonical: canonical}, nil
}

func (accumulator *CatalogProjectionAccumulator) Write(record CatalogRecord) error {
	if accumulator == nil || accumulator.canonical == nil || accumulator.finished ||
		validateCatalogRecord(record) != nil {
		return fmt.Errorf("%w: invalid Catalog projection record", ErrCatalogProtocol)
	}
	accumulator.canonical.String(record.NormalizedPath)
	accumulator.canonical.String(record.ParentNormalizedPath)
	accumulator.canonical.String(record.Name)
	accumulator.canonical.String(string(record.Type))
	accumulator.canonical.Int64(record.Size)
	if record.ModifiedAt == nil {
		accumulator.canonical.Uint8(0)
	} else {
		accumulator.canonical.Uint8(1)
		accumulator.canonical.Int64(record.ModifiedAt.UTC().UnixNano())
	}
	accumulator.canonical.String(record.Mode)
	accumulator.canonical.String(record.Owner)
	accumulator.canonical.String(record.MIMEType)
	accumulator.canonical.String(record.Fingerprint)
	accumulator.canonical.String(record.FingerprintStrength)
	accumulator.count++
	return nil
}

func (accumulator *CatalogProjectionAccumulator) Finalize() (string, int64, error) {
	if accumulator == nil || accumulator.canonical == nil || accumulator.finished {
		return "", 0, fmt.Errorf("%w: Catalog projection already finalized", ErrCatalogProtocol)
	}
	accumulator.finished = true
	digest, err := accumulator.canonical.HexDigest()
	if err != nil {
		return "", 0, fmt.Errorf("%w: finalize Catalog projection", ErrCatalogProtocol)
	}
	return digest, accumulator.count, nil
}

type CatalogRecord struct {
	NormalizedPath        string
	ParentNormalizedPath  string
	Name                  string
	Type                  backupasset.CatalogEntryType
	Size                  int64
	ModifiedAt            *time.Time
	Mode                  string
	Owner                 string
	MIMEType              string
	Fingerprint           string
	FingerprintStrength   string
	ProviderLocator       EntryLocator `json:"-"`
	SealedProviderLocator string       `json:"-"`
}

type CatalogRecordPage struct {
	Items      []CatalogRecord
	NextCursor string
}

type CatalogReadRequest struct {
	Provider        backupasset.ProviderKind
	RecoveryPointID string
	Snapshot        ReadSnapshot
	Point           PointLocator
	Mode            CatalogProofMode
	Manifest        CatalogManifestProof
	ResticProof     *ResticCatalogProofInput `json:"-"`
	RcloneProof     *RcloneCatalogProofInput `json:"-"`
	MaxItems        int
}

type RcloneCatalogProofInput struct {
	Reconcile RcloneReconcileInput `json:"-"`
	Commit    RcloneCommitV1       `json:"-"`
}

type CatalogReadSession interface {
	SourceRevision() string
	ListCanonical(context.Context, PageRequest) (CatalogRecordPage, error)
	Finalize(context.Context) (CatalogReadProof, error)
	Close() error
}

type CatalogReader interface {
	OpenCatalogRead(context.Context, CatalogReadRequest) (CatalogReadSession, error)
}

type rcloneCatalogReader struct {
	mutable   CatalogReader
	immutable CatalogReader
}

func NewRcloneCatalogReader(mutable, immutable CatalogReader) (CatalogReader, error) {
	if interfaceNil(mutable) || interfaceNil(immutable) {
		return nil, fmt.Errorf("%w: incomplete Rclone Catalog reader", backupasset.ErrInvalidState)
	}
	return &rcloneCatalogReader{mutable: mutable, immutable: immutable}, nil
}

func (reader *rcloneCatalogReader) OpenCatalogRead(ctx context.Context, request CatalogReadRequest) (CatalogReadSession, error) {
	if reader == nil || request.Provider != backupasset.ProviderRclone {
		return nil, fmt.Errorf("%w: invalid Rclone Catalog reader request", ErrCatalogProtocol)
	}
	switch request.Mode {
	case CatalogProofMutableObservation:
		return reader.mutable.OpenCatalogRead(ctx, request)
	case CatalogProofPublicationManifest:
		return reader.immutable.OpenCatalogRead(ctx, request)
	default:
		return nil, fmt.Errorf("%w: invalid Rclone Catalog proof mode", ErrCatalogProtocol)
	}
}

// CatalogManifestProver is deliberately separate from EntryLister. A reader
// may enumerate metadata without being able to prove that it still matches a
// committed publication manifest; such a reader cannot publish a complete
// immutable Catalog generation.
type CatalogManifestProver interface {
	ProveCatalogManifest(context.Context, CatalogReadRequest) (CatalogManifestProof, error)
}

type catalogDirectory struct {
	path    string
	locator EntryLocator
}

type catalogReadSession struct {
	reader  EntryLister
	prover  CatalogManifestProver
	request CatalogReadRequest

	mu              sync.Mutex
	closed          bool
	failed          error
	complete        bool
	directories     []catalogDirectory
	current         *catalogDirectory
	providerCursor  string
	lastSiblingName string
	lastSiblingID   string
	paths           map[string]struct{}
	nextCursor      string
	pageSequence    int
	entryCount      int64
	projection      *CatalogProjectionAccumulator
	digest          string
}

func NewCatalogReadSession(reader EntryLister, request CatalogReadRequest) (CatalogReadSession, error) {
	if interfaceNil(reader) {
		return nil, fmt.Errorf("%w: Catalog entry reader unavailable", ErrCatalogProtocol)
	}
	if err := validateCatalogReadRequest(request); err != nil {
		return nil, err
	}
	var prover CatalogManifestProver
	if request.Mode == CatalogProofPublicationManifest {
		var ok bool
		prover, ok = reader.(CatalogManifestProver)
		if !ok || interfaceNil(prover) {
			return nil, fmt.Errorf("%w: publication Catalog reader has no manifest proof", ErrCatalogProtocol)
		}
	}
	projection, err := NewCatalogProjectionAccumulator(
		request.Provider, request.Snapshot.RepositoryID, request.RecoveryPointID, request.Snapshot.SourceRevision,
	)
	if err != nil {
		return nil, err
	}
	return &catalogReadSession{
		reader: reader, prover: prover, request: request, directories: []catalogDirectory{{}},
		paths: make(map[string]struct{}), projection: projection,
	}, nil
}

func (session *catalogReadSession) SourceRevision() string {
	if session == nil {
		return ""
	}
	return session.request.Snapshot.SourceRevision
}

func (session *catalogReadSession) ListCanonical(ctx context.Context, request PageRequest) (CatalogRecordPage, error) {
	if session == nil {
		return CatalogRecordPage{}, fmt.Errorf("%w: Catalog session unavailable", ErrCatalogSessionClosed)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return CatalogRecordPage{}, err
	}
	request, err := normalizeCatalogPageRequest(request)
	if err != nil {
		return CatalogRecordPage{}, err
	}

	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed {
		return CatalogRecordPage{}, fmt.Errorf("%w", ErrCatalogSessionClosed)
	}
	if session.failed != nil {
		return CatalogRecordPage{}, session.failed
	}
	if request.Cursor != session.nextCursor {
		return CatalogRecordPage{}, fmt.Errorf("%w: Catalog continuation changed", ErrCatalogProtocol)
	}
	if session.complete {
		return CatalogRecordPage{}, nil
	}

	page := CatalogRecordPage{Items: make([]CatalogRecord, 0, request.Limit)}
	for len(page.Items) < request.Limit && !session.complete {
		if err := ctx.Err(); err != nil {
			return CatalogRecordPage{}, err
		}
		if session.current == nil {
			if len(session.directories) == 0 {
				if err := session.finish(); err != nil {
					session.failed = err
					return CatalogRecordPage{}, err
				}
				break
			}
			directory := session.directories[0]
			session.directories = session.directories[1:]
			session.current = &directory
			session.providerCursor = ""
			session.lastSiblingName = ""
			session.lastSiblingID = ""
		}

		providerPage, err := session.reader.ListEntries(ctx, session.request.Snapshot, session.request.Point, session.current.locator, PageRequest{
			Limit: min(request.Limit-len(page.Items), defaultCatalogSessionPageSize), Cursor: session.providerCursor,
		})
		if err != nil {
			if ctx.Err() != nil {
				return CatalogRecordPage{}, ctx.Err()
			}
			session.failed = err
			return CatalogRecordPage{}, err
		}
		if len(providerPage.Items) == 0 && providerPage.NextCursor != "" {
			err := fmt.Errorf("%w: empty Provider page has a continuation", ErrCatalogProtocol)
			session.failed = err
			return CatalogRecordPage{}, err
		}
		for _, entry := range providerPage.Items {
			record, err := session.acceptEntry(*session.current, entry)
			if err != nil {
				session.failed = err
				return CatalogRecordPage{}, err
			}
			page.Items = append(page.Items, record)
			if len(page.Items) == request.Limit {
				break
			}
		}
		session.providerCursor = providerPage.NextCursor
		if providerPage.NextCursor == "" {
			session.current = nil
		}
	}

	if !session.complete {
		session.pageSequence++
		session.nextCursor = strconv.Itoa(session.pageSequence)
		page.NextCursor = session.nextCursor
	} else {
		session.nextCursor = ""
	}
	return page, nil
}

func (session *catalogReadSession) Finalize(ctx context.Context) (CatalogReadProof, error) {
	if session == nil {
		return CatalogReadProof{}, fmt.Errorf("%w: Catalog session unavailable", ErrCatalogSessionClosed)
	}
	if ctx != nil && ctx.Err() != nil {
		return CatalogReadProof{}, ctx.Err()
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed {
		return CatalogReadProof{}, fmt.Errorf("%w", ErrCatalogSessionClosed)
	}
	if session.failed != nil {
		return CatalogReadProof{}, session.failed
	}
	if !session.complete || session.digest == "" {
		return CatalogReadProof{}, fmt.Errorf("%w", ErrCatalogSessionIncomplete)
	}
	manifest := CatalogManifestProof{}
	if session.request.Mode == CatalogProofPublicationManifest {
		if session.prover == nil {
			return CatalogReadProof{}, fmt.Errorf("%w: publication proof unavailable", ErrCatalogProtocol)
		}
		proved, err := session.prover.ProveCatalogManifest(ctx, session.request)
		if err != nil {
			return CatalogReadProof{}, err
		}
		if proved != session.request.Manifest {
			return CatalogReadProof{}, fmt.Errorf("%w: Provider manifest proof changed", ErrCatalogProofMismatch)
		}
		if proved.EntryCount != session.entryCount {
			return CatalogReadProof{}, fmt.Errorf("%w: manifest count=%d Catalog count=%d", ErrCatalogProofMismatch, proved.EntryCount, session.entryCount)
		}
		manifest = proved
	}
	return CatalogReadProof{
		Provider: session.request.Provider, Mode: session.request.Mode,
		SourceRevision: session.request.Snapshot.SourceRevision, Manifest: manifest,
		Catalog: CatalogProjectionProof{DigestAlgorithm: "sha256", Digest: session.digest, EntryCount: session.entryCount, Complete: true},
	}, nil
}

func (session *catalogReadSession) Close() error {
	if session == nil {
		return nil
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed {
		return nil
	}
	session.closed = true
	session.directories = nil
	session.current = nil
	session.paths = nil
	return nil
}

func (session *catalogReadSession) acceptEntry(parent catalogDirectory, entry Entry) (CatalogRecord, error) {
	if err := validateCatalogEntry(entry); err != nil {
		return CatalogRecord{}, err
	}
	if session.lastSiblingName != "" && (entry.Name < session.lastSiblingName ||
		(entry.Name == session.lastSiblingName && entry.OpaqueDigest <= session.lastSiblingID)) {
		return CatalogRecord{}, fmt.Errorf("%w: Provider records are not strictly ordered", ErrCatalogProtocol)
	}
	if entry.Name == session.lastSiblingName {
		return CatalogRecord{}, fmt.Errorf("%w: duplicate sibling name", ErrCatalogProtocol)
	}
	session.lastSiblingName = entry.Name
	session.lastSiblingID = entry.OpaqueDigest

	normalizedPath := entry.Name
	if parent.path != "" {
		normalizedPath = parent.path + "/" + entry.Name
	}
	if len(normalizedPath) > maxCatalogEntryPathBytes {
		return CatalogRecord{}, fmt.Errorf("%w: Catalog path exceeds limit", ErrCatalogProtocol)
	}
	if _, exists := session.paths[normalizedPath]; exists {
		return CatalogRecord{}, fmt.Errorf("%w: duplicate canonical path", ErrCatalogProtocol)
	}
	session.paths[normalizedPath] = struct{}{}
	session.entryCount++
	if session.entryCount > int64(session.request.MaxItems) {
		return CatalogRecord{}, newCapabilityError(backupasset.CapabilityProviderResourceLimit)
	}

	var modifiedAt *time.Time
	if !entry.ModTime.IsZero() {
		utc := entry.ModTime.UTC()
		modifiedAt = &utc
	}
	record := CatalogRecord{
		NormalizedPath: normalizedPath, ParentNormalizedPath: parent.path, Name: entry.Name,
		Type: entry.Type, Size: entry.Size, ModifiedAt: modifiedAt, ProviderLocator: entry.Locator,
	}
	session.writeRecord(record)
	if entry.Type == backupasset.CatalogEntryDirectory {
		session.directories = append(session.directories, catalogDirectory{path: normalizedPath, locator: entry.Locator})
	}
	return record, nil
}

func (session *catalogReadSession) writeRecord(record CatalogRecord) {
	_ = session.projection.Write(record)
}

func (session *catalogReadSession) finish() error {
	digest, count, err := session.projection.Finalize()
	if err != nil {
		return fmt.Errorf("%w: finalize Catalog digest", ErrCatalogProtocol)
	}
	if count != session.entryCount {
		return fmt.Errorf("%w: Catalog projection count changed", ErrCatalogProtocol)
	}
	session.digest = digest
	session.complete = true
	return nil
}

func normalizeCatalogPageRequest(request PageRequest) (PageRequest, error) {
	if request.Limit < 0 {
		return PageRequest{}, fmt.Errorf("%w: negative Catalog page size", ErrCatalogProtocol)
	}
	if request.Limit == 0 {
		request.Limit = DefaultPageLimit
	}
	if request.Limit > maxCatalogSessionPageSize {
		request.Limit = maxCatalogSessionPageSize
	}
	if len(request.Cursor) > 64 || strings.ContainsAny(request.Cursor, "\r\n\x00") {
		return PageRequest{}, fmt.Errorf("%w: invalid Catalog continuation", ErrCatalogProtocol)
	}
	return request, nil
}

func validateCatalogReadRequest(request CatalogReadRequest) error {
	if !readableProvider(request.Provider) || backupasset.ValidateOpaqueID(request.RecoveryPointID) != nil ||
		backupasset.ValidateOpaqueID(request.Snapshot.RepositoryID) != nil || request.Snapshot.Access.Provider != request.Provider ||
		request.Snapshot.Access.RepositoryID != request.Snapshot.RepositoryID || request.Snapshot.CapabilityRevision <= 0 ||
		strings.TrimSpace(request.Snapshot.SourceRevision) == "" || strings.ContainsAny(request.Snapshot.SourceRevision, "\r\n\x00") ||
		strings.TrimSpace(request.Point.Native) == "" || len(request.Point.Native) > maxCatalogPointLocatorBytes || strings.ContainsRune(request.Point.Native, '\x00') ||
		request.MaxItems <= 0 {
		return fmt.Errorf("%w: invalid Catalog read request", ErrCatalogProtocol)
	}
	switch request.Mode {
	case CatalogProofPublicationManifest:
		proof := request.Manifest
		if backupasset.ValidateOpaqueID(proof.ManifestID) != nil || proof.Revision <= 0 || proof.DigestAlgorithm != "sha256" ||
			!lowerHex(proof.Digest, 64) || proof.EntryCount < 0 || proof.Completeness != backupasset.ManifestComplete ||
			proof.SourceRevision != request.Snapshot.SourceRevision {
			return fmt.Errorf("%w: invalid publication manifest proof", ErrCatalogProtocol)
		}
	case CatalogProofMutableObservation:
		if request.Manifest != (CatalogManifestProof{}) {
			return fmt.Errorf("%w: mutable Catalog request cannot carry a manifest proof", ErrCatalogProtocol)
		}
	default:
		return fmt.Errorf("%w: invalid Catalog proof mode", ErrCatalogProtocol)
	}
	return nil
}

func validateCatalogEntry(entry Entry) error {
	if !lowerHex(entry.OpaqueDigest, 64) || entry.Name == "" || entry.Name == "." || entry.Name == ".." || len(entry.Name) > 4096 ||
		strings.ContainsAny(entry.Name, "/\\\r\n\x00") || entry.Size < 0 || strings.TrimSpace(entry.Locator.Native) == "" ||
		len(entry.Locator.Native) > maxCatalogPointLocatorBytes || strings.ContainsRune(entry.Locator.Native, '\x00') {
		return fmt.Errorf("%w: invalid Catalog record", ErrCatalogProtocol)
	}
	switch entry.Type {
	case backupasset.CatalogEntryFile, backupasset.CatalogEntryDirectory, backupasset.CatalogEntrySymlink,
		backupasset.CatalogEntryHardlink, backupasset.CatalogEntrySpecial, backupasset.CatalogEntryUnknown:
		return nil
	default:
		return fmt.Errorf("%w: invalid Catalog entry type", ErrCatalogProtocol)
	}
}

func validateCatalogRecord(record CatalogRecord) error {
	if record.NormalizedPath == "" || len(record.NormalizedPath) > maxCatalogEntryPathBytes ||
		strings.HasPrefix(record.NormalizedPath, "/") || strings.HasSuffix(record.NormalizedPath, "/") ||
		strings.ContainsAny(record.NormalizedPath, "\\\r\n\x00") || record.Name == "" ||
		strings.ContainsAny(record.Name, "/\\\r\n\x00") || record.Size < 0 {
		return fmt.Errorf("%w: invalid Catalog record", ErrCatalogProtocol)
	}
	components := strings.Split(record.NormalizedPath, "/")
	for _, component := range components {
		if component == "" || component == "." || component == ".." {
			return fmt.Errorf("%w: invalid Catalog record path", ErrCatalogProtocol)
		}
	}
	if components[len(components)-1] != record.Name {
		return fmt.Errorf("%w: Catalog record name mismatch", ErrCatalogProtocol)
	}
	wantParent := strings.Join(components[:len(components)-1], "/")
	if record.ParentNormalizedPath != wantParent {
		return fmt.Errorf("%w: Catalog record parent mismatch", ErrCatalogProtocol)
	}
	switch record.Type {
	case backupasset.CatalogEntryFile, backupasset.CatalogEntryDirectory, backupasset.CatalogEntrySymlink,
		backupasset.CatalogEntryHardlink, backupasset.CatalogEntrySpecial, backupasset.CatalogEntryUnknown:
	default:
		return fmt.Errorf("%w: invalid Catalog record type", ErrCatalogProtocol)
	}
	if record.ModifiedAt != nil && record.ModifiedAt.Location() != time.UTC {
		return fmt.Errorf("%w: Catalog record time is not UTC", ErrCatalogProtocol)
	}
	return nil
}

func (adapter *ResticAdapter) OpenCatalogRead(_ context.Context, request CatalogReadRequest) (CatalogReadSession, error) {
	if request.Provider != backupasset.ProviderRestic || request.Mode != CatalogProofPublicationManifest ||
		request.ResticProof == nil || request.Point.Native != request.ResticProof.Commit.NativePointID {
		return nil, fmt.Errorf("%w: Restic Catalog provider mismatch", ErrCatalogProtocol)
	}
	return NewCatalogReadSession(adapter, request)
}

func (adapter *ResticAdapter) ProveCatalogManifest(ctx context.Context, request CatalogReadRequest) (CatalogManifestProof, error) {
	if adapter == nil || request.Provider != backupasset.ProviderRestic || request.Mode != CatalogProofPublicationManifest ||
		request.ResticProof == nil || request.Point.Native != request.ResticProof.Commit.NativePointID {
		return CatalogManifestProof{}, fmt.Errorf("%w: Restic Catalog proof request", ErrCatalogProtocol)
	}
	manifest, err := adapter.BuildCatalogManifest(ctx, *request.ResticProof)
	if err != nil {
		return CatalogManifestProof{}, err
	}
	if manifest.Completeness != backupasset.ManifestComplete || manifest.DigestAlgorithm != "sha256" ||
		manifest.Digest == "" || manifest.EntryCount < 0 {
		return CatalogManifestProof{}, fmt.Errorf("%w: Restic manifest is not complete", ErrCatalogProofMismatch)
	}
	return CatalogManifestProof{
		ManifestID: request.Manifest.ManifestID, Revision: request.Manifest.Revision,
		DigestAlgorithm: manifest.DigestAlgorithm, Digest: manifest.Digest, EntryCount: manifest.EntryCount,
		Completeness: manifest.Completeness, SourceRevision: request.Snapshot.SourceRevision,
	}, nil
}

func (adapter *RsyncAdapter) OpenCatalogRead(_ context.Context, request CatalogReadRequest) (CatalogReadSession, error) {
	if request.Provider != backupasset.ProviderRsync || request.Mode != CatalogProofMutableObservation {
		return nil, fmt.Errorf("%w: Rsync Catalog provider mismatch", ErrCatalogProtocol)
	}
	return NewCatalogReadSession(adapter, request)
}

func (adapter *RsyncCommittedPointAdapter) OpenCatalogRead(_ context.Context, request CatalogReadRequest) (CatalogReadSession, error) {
	if request.Provider != backupasset.ProviderRsync || request.Mode != CatalogProofPublicationManifest {
		return nil, fmt.Errorf("%w: committed Rsync Catalog provider mismatch", ErrCatalogProtocol)
	}
	return NewCatalogReadSession(adapter, request)
}

func (adapter *RsyncCommittedPointAdapter) ProveCatalogManifest(ctx context.Context, request CatalogReadRequest) (CatalogManifestProof, error) {
	if adapter == nil || request.Provider != backupasset.ProviderRsync || request.Mode != CatalogProofPublicationManifest {
		return CatalogManifestProof{}, fmt.Errorf("%w: committed Rsync Catalog proof request", ErrCatalogProtocol)
	}
	runtimeAccess, ok := request.Snapshot.Access.AdapterData.(RsyncCommittedPointRuntimeAccess)
	if !ok || request.Snapshot.SourceRevision != runtimeAccess.SourceRevision ||
		request.Point.Native != rsyncCommittedPointLocator(runtimeAccess.request).Native {
		return CatalogManifestProof{}, fmt.Errorf("%w: committed Rsync Catalog runtime facts", ErrCatalogProtocol)
	}
	if _, err := validateRsyncCommittedPointTree(ctx, runtimeAccess.request); err != nil {
		return CatalogManifestProof{}, err
	}
	return CatalogManifestProof{
		ManifestID: request.Manifest.ManifestID, Revision: request.Manifest.Revision,
		DigestAlgorithm: "sha256", Digest: runtimeAccess.request.ManifestDigest,
		EntryCount: int64(runtimeAccess.request.ManifestEntryCount), Completeness: backupasset.ManifestComplete,
		SourceRevision: runtimeAccess.SourceRevision,
	}, nil
}

func (adapter *RcloneAdapter) OpenCatalogRead(_ context.Context, request CatalogReadRequest) (CatalogReadSession, error) {
	if request.Provider != backupasset.ProviderRclone || request.Mode != CatalogProofMutableObservation {
		return nil, fmt.Errorf("%w: Rclone Catalog provider mismatch", ErrCatalogProtocol)
	}
	return NewCatalogReadSession(adapter, request)
}

func (strategy *RclonePublicationStrategy) OpenCatalogRead(ctx context.Context, request CatalogReadRequest) (CatalogReadSession, error) {
	if strategy == nil || strategy.portable == nil || strategy.native == nil || request.Provider != backupasset.ProviderRclone ||
		request.Mode != CatalogProofPublicationManifest || request.RcloneProof == nil {
		return nil, fmt.Errorf("%w: immutable Rclone Catalog provider mismatch", ErrCatalogProtocol)
	}
	if err := validateCatalogReadRequest(request); err != nil {
		return nil, err
	}
	input := request.RcloneProof
	attempt := input.Commit.PublicationMode
	if !input.Reconcile.validateVariant(attempt) || input.Commit.RepositoryID != request.Snapshot.RepositoryID ||
		input.Commit.RecoveryPointID != request.RecoveryPointID || input.Commit.ManifestIndexDigest != request.Manifest.Digest ||
		input.Commit.ManifestEntryCount > uint64(request.MaxItems) || input.Commit.ManifestEntryCount != uint64(request.Manifest.EntryCount) {
		return nil, fmt.Errorf("%w: immutable Rclone Catalog proof mismatch", ErrCatalogProtocol)
	}
	var (
		actual       RcloneCommitV1
		entries      []rcloneCanonicalManifestEntry
		nativeStates map[string]rcloneNativeManifestVersionStateV1
		err          error
	)
	switch attempt {
	case backupasset.PublicationVersionedPrefix:
		portable := *input.Reconcile.PortableRequest
		actual, err = strategy.portable.Reconcile(ctx, portable)
		if err == nil {
			var manifest RcloneManifestBundle
			manifest, err = readRclonePortableCatalogManifest(ctx, strategy.portable.remote, portable, actual)
			if err == nil {
				entries, err = decodeRcloneNativeSourceManifest(manifest)
			}
		}
	case backupasset.PublicationNativeObjectVersions:
		native := *input.Reconcile.NativeRequest
		actual, entries, nativeStates, err = strategy.readRcloneNativeCatalog(ctx, native)
	default:
		return nil, fmt.Errorf("%w: immutable Rclone Catalog mode mismatch", ErrCatalogProtocol)
	}
	if err != nil {
		return nil, err
	}
	if !equalRcloneCatalogCommit(actual, input.Commit) {
		return nil, fmt.Errorf("%w: immutable Rclone commit changed", ErrCatalogProofMismatch)
	}
	records, err := rcloneCatalogRecords(entries, nativeStates)
	if err != nil {
		return nil, err
	}
	return newMaterializedCatalogReadSession(request, records)
}

func (strategy *RclonePublicationStrategy) readRcloneNativeCatalog(
	ctx context.Context,
	request RcloneNativePublicationRequest,
) (RcloneCommitV1, []rcloneCanonicalManifestEntry, map[string]rcloneNativeManifestVersionStateV1, error) {
	if strategy == nil || strategy.native == nil || strategy.native.now == nil || request.ExactCommitKey == "" ||
		request.ExactCommitVersionID == "" {
		return RcloneCommitV1{}, nil, nil, fmt.Errorf("%w: exact native Rclone Catalog source unavailable", ErrCatalogProtocol)
	}
	commit, err := strategy.native.Reconcile(ctx, request)
	if err != nil {
		return RcloneCommitV1{}, nil, nil, err
	}
	if commit.Native == nil || commit.Native.CommitKey != request.ExactCommitKey || commit.Native.CommitVersionID != request.ExactCommitVersionID {
		return RcloneCommitV1{}, nil, nil, fmt.Errorf("%w: exact native Rclone commit changed", ErrCatalogProofMismatch)
	}
	s3, err := request.ClientFactory.S3(request.Session, request.Profile, request.KMSKeyBindings)
	if err != nil || s3 == nil {
		return RcloneCommitV1{}, nil, nil, rcloneNativeError(backupasset.RcloneReasonAdmissionBlocked, err)
	}
	request.s3 = s3
	head, err := s3.HeadVersion(ctx, RcloneNativeExactReadRequest{PhysicalKey: request.ExactCommitKey, VersionID: request.ExactCommitVersionID})
	if err != nil || head.PhysicalKey != request.ExactCommitKey || head.VersionID != request.ExactCommitVersionID ||
		head.Size == 0 || head.Size > request.ControlPayloadMaxBytes {
		return RcloneCommitV1{}, nil, nil, rcloneNativeError(backupasset.RcloneReasonMarkerMismatch, err)
	}
	candidate := RcloneNativeVersionRecord{
		PhysicalKey: head.PhysicalKey, VersionID: head.VersionID, Kind: RcloneNativeObjectVersion, Size: head.Size,
		EncryptionProfile: head.EncryptionProfile, KMSKeyDigest: head.KMSKeyDigest, BucketKeyEnabled: head.BucketKeyEnabled,
	}
	commitPayload, commitVersion, err := readRcloneNativeCommitCandidate(ctx, request, candidate)
	if err != nil {
		return RcloneCommitV1{}, nil, nil, err
	}
	marker, err := decodeRcloneNativeCommitMarker(commitPayload, request.MarkerKey)
	controlPrefix := rcloneNativeAttemptControlPrefix(request)
	if err != nil || validateRcloneNativeCommitMarker(request, controlPrefix, marker) != nil {
		return RcloneCommitV1{}, nil, nil, rcloneNativeError(backupasset.RcloneReasonMarkerMismatch, err)
	}
	manifestPayloads, index, err := reopenRcloneNativeCatalogPayloads(ctx, request, marker, commitVersion)
	if err != nil {
		return RcloneCommitV1{}, nil, nil, err
	}
	nativeStates := make(map[string]rcloneNativeManifestVersionStateV1)
	entries, err := decodeRcloneNativeCatalogEntries(ctx, request, marker, index, manifestPayloads, nativeStates)
	if err != nil {
		return RcloneCommitV1{}, nil, nil, err
	}
	return commit, entries, nativeStates, nil
}

func reopenRcloneNativeCatalogPayloads(
	ctx context.Context,
	request RcloneNativePublicationRequest,
	marker rcloneNativeCommitMarkerV1,
	commitVersion RcloneNativeControlObjectVersion,
) ([][]byte, rcloneNativeManifestIndexV1, error) {
	graph := RcloneNativeControlCommitGraph{
		ManifestVersions: make([]RcloneNativeControlObjectVersion, len(marker.ManifestVersions)), CommitVersion: commitVersion,
	}
	payloads := make([][]byte, len(marker.ManifestVersions))
	for position, reference := range marker.ManifestVersions {
		payload, version, err := reopenRcloneNativeControlVersion(ctx, request, reference)
		if err != nil {
			return nil, rcloneNativeManifestIndexV1{}, err
		}
		payloads[position] = payload
		graph.ManifestVersions[position] = version
	}
	indexPayload, indexVersion, err := reopenRcloneNativeControlVersion(ctx, request, marker.IndexVersion)
	if err != nil {
		return nil, rcloneNativeManifestIndexV1{}, err
	}
	graph.IndexVersion = indexVersion
	graph.Digest, err = digestRcloneNativeControlGraph(graph)
	if err != nil || graph.Digest == "" {
		return nil, rcloneNativeManifestIndexV1{}, rcloneNativeError(backupasset.RcloneReasonMarkerMismatch, err)
	}
	index, _, err := decodeAndValidateRcloneNativeManifestIndex(request, marker, indexPayload, payloads)
	if err != nil {
		return nil, rcloneNativeManifestIndexV1{}, rcloneNativeError(backupasset.RcloneReasonManifestMismatch, err)
	}
	return payloads, index, nil
}

func decodeRcloneNativeCatalogEntries(
	ctx context.Context,
	request RcloneNativePublicationRequest,
	marker rcloneNativeCommitMarkerV1,
	index rcloneNativeManifestIndexV1,
	payloads [][]byte,
	nativeStates map[string]rcloneNativeManifestVersionStateV1,
) ([]rcloneCanonicalManifestEntry, error) {
	if nativeStates == nil {
		return nil, rcloneNativeError(backupasset.RcloneReasonManifestMismatch, nil)
	}
	entries := make([]rcloneCanonicalManifestEntry, 0, index.EntryCount)
	seen := make(map[string]struct{})
	headerSeen := false
	for _, payload := range payloads {
		for _, line := range bytes.Split(payload, []byte{'\n'}) {
			if len(line) == 0 {
				continue
			}
			if rejectDuplicateJSONMembers(string(line)) != nil {
				return nil, rcloneNativeError(backupasset.RcloneReasonManifestMismatch, nil)
			}
			decoder := json.NewDecoder(bytes.NewReader(line))
			decoder.DisallowUnknownFields()
			var record rcloneNativeManifestRecordV1
			if err := decoder.Decode(&record); err != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) || record.Version != 1 {
				return nil, rcloneNativeError(backupasset.RcloneReasonManifestMismatch, err)
			}
			switch record.RecordKind {
			case "header":
				if headerSeen || record.Header == nil || record.Source != nil || record.State != nil ||
					record.Header.SourceManifestIndexDigest != marker.SourceManifestIndexDigest ||
					record.Header.SourceObservationDigest != marker.SourceObservationDigest ||
					record.Header.PointViewDigest != marker.PointViewDigest || record.Header.MutationLedgerDigest != marker.MutationLedgerDigest ||
					record.Header.B0VersionGraphDigest != marker.B0VersionGraphDigest || record.Header.B1VersionGraphDigest != marker.B1VersionGraphDigest ||
					record.Header.ExactReadProofDigest != marker.ExactReadProofDigest {
					return nil, rcloneNativeError(backupasset.RcloneReasonManifestMismatch, nil)
				}
				headerSeen = true
			case "entry":
				if record.Source == nil || record.Header != nil || !validRcloneLogicalPath(record.Source.Path, 4096) ||
					!validRcloneLogicalPath(record.Source.PhysicalPath, 4096) {
					return nil, rcloneNativeError(backupasset.RcloneReasonManifestMismatch, nil)
				}
				if _, duplicate := seen[record.Source.Path]; duplicate {
					return nil, rcloneNativeError(backupasset.RcloneReasonManifestMismatch, nil)
				}
				seen[record.Source.Path] = struct{}{}
				if record.Source.Kind == "directory" {
					if record.State != nil {
						return nil, rcloneNativeError(backupasset.RcloneReasonManifestMismatch, nil)
					}
				} else {
					if record.State == nil || record.State.Kind != RcloneNativeObjectVersion ||
						!validRcloneNativeVersionIdentity(record.State.PhysicalKey, record.State.VersionID, record.State.Kind) ||
						record.State.Size != record.Source.Size {
						return nil, rcloneNativeError(backupasset.RcloneReasonManifestMismatch, nil)
					}
					encoded, err := EncodeRcloneV1744S3Path(record.Source.PhysicalPath)
					if err != nil || record.State.PhysicalKey != request.Profile.ManagedPrefix+"data/"+encoded {
						return nil, rcloneNativeError(backupasset.RcloneReasonManifestMismatch, err)
					}
					head, err := request.s3.HeadVersion(ctx, RcloneNativeExactReadRequest{
						PhysicalKey: record.State.PhysicalKey, VersionID: record.State.VersionID,
					})
					if err != nil || head.PhysicalKey != record.State.PhysicalKey || head.VersionID != record.State.VersionID ||
						head.Size != record.State.Size || head.EncryptionProfile != record.State.EncryptionProfile ||
						head.KMSKeyDigest != record.State.KMSKeyDigest || head.BucketKeyEnabled != record.State.BucketKeyEnabled {
						return nil, rcloneNativeError(backupasset.RcloneReasonIdentityMismatch, err)
					}
					nativeStates[record.Source.Path] = *record.State
				}
				entries = append(entries, *record.Source)
			case "delete_state", "mutation":
				if record.State == nil || record.Header != nil || record.Source != nil {
					return nil, rcloneNativeError(backupasset.RcloneReasonManifestMismatch, nil)
				}
			default:
				return nil, rcloneNativeError(backupasset.RcloneReasonManifestMismatch, nil)
			}
		}
	}
	if !headerSeen || uint64(len(entries)) != index.EntryCount {
		return nil, rcloneNativeError(backupasset.RcloneReasonManifestMismatch, nil)
	}
	return entries, nil
}

type rcloneCatalogManifestIndexV1 struct {
	Version          int                      `json:"version"`
	Generator        string                   `json:"generator"`
	GeneratorVersion string                   `json:"generator_version"`
	EntryCount       uint64                   `json:"entry_count"`
	LogicalBytes     uint64                   `json:"logical_bytes"`
	Chunks           []rcloneCatalogChunkV1   `json:"chunks"`
	Fidelity         RcloneManifestFidelityV1 `json:"fidelity"`
}

type rcloneCatalogChunkV1 struct {
	Ordinal    int    `json:"ordinal"`
	Digest     string `json:"digest"`
	Size       int64  `json:"size"`
	EntryCount uint64 `json:"entry_count"`
}

func readRclonePortableCatalogManifest(
	ctx context.Context,
	remote RclonePortableRemote,
	request RclonePortablePublicationRequest,
	commit RcloneCommitV1,
) (RcloneManifestBundle, error) {
	if remote == nil || commit.Portable == nil || commit.PublicationMode != backupasset.PublicationVersionedPrefix ||
		request.ControlPayloadMaxBytes <= 0 {
		return RcloneManifestBundle{}, fmt.Errorf("%w: portable Catalog proof unavailable", ErrCatalogProtocol)
	}
	indexPayload, err := remote.ReadControl(ctx, request, "manifest-index.json", request.ControlPayloadMaxBytes)
	if err != nil {
		return RcloneManifestBundle{}, err
	}
	if sha256Hex(indexPayload) != commit.ManifestIndexDigest || rejectDuplicateJSONMembers(string(indexPayload)) != nil {
		return RcloneManifestBundle{}, fmt.Errorf("%w: portable Catalog manifest index changed", ErrCatalogProofMismatch)
	}
	decoder := json.NewDecoder(bytes.NewReader(indexPayload))
	decoder.DisallowUnknownFields()
	var index rcloneCatalogManifestIndexV1
	if err := decoder.Decode(&index); err != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) || index.Version != 1 ||
		index.Generator != "xirang-rclone-manifest" || index.GeneratorVersion != "v1" ||
		index.EntryCount != commit.ManifestEntryCount || index.LogicalBytes != commit.LogicalBytes ||
		len(index.Chunks) != len(commit.ManifestChunkDigests) {
		return RcloneManifestBundle{}, fmt.Errorf("%w: invalid portable Catalog manifest index", ErrCatalogProofMismatch)
	}
	bundle := RcloneManifestBundle{
		Version: 1, Chunks: make([]RcloneManifestChunk, len(index.Chunks)), IndexEncoded: append([]byte(nil), indexPayload...),
		IndexDigest: commit.ManifestIndexDigest, EntryCount: index.EntryCount, LogicalBytes: index.LogicalBytes, Fidelity: index.Fidelity,
	}
	hash := sha256.New()
	var count uint64
	for position, reference := range index.Chunks {
		if reference.Ordinal != position || reference.Size <= 0 || reference.EntryCount == 0 ||
			reference.Digest != commit.ManifestChunkDigests[position] {
			return RcloneManifestBundle{}, fmt.Errorf("%w: invalid portable Catalog manifest chunk", ErrCatalogProofMismatch)
		}
		name := fmt.Sprintf("manifest-%06d.jsonl", position)
		payload, err := remote.ReadControl(ctx, request, name, request.ControlPayloadMaxBytes)
		if err != nil {
			return RcloneManifestBundle{}, err
		}
		if int64(len(payload)) != reference.Size || sha256Hex(payload) != reference.Digest {
			return RcloneManifestBundle{}, fmt.Errorf("%w: portable Catalog manifest chunk changed", ErrCatalogProofMismatch)
		}
		_, _ = hash.Write(payload)
		count += reference.EntryCount
		bundle.Chunks[position] = RcloneManifestChunk{
			Ordinal: position, Encoded: append([]byte(nil), payload...), Digest: reference.Digest,
			Size: reference.Size, EntryCount: reference.EntryCount,
		}
	}
	if count != index.EntryCount {
		return RcloneManifestBundle{}, fmt.Errorf("%w: portable Catalog manifest count changed", ErrCatalogProofMismatch)
	}
	_, _ = io.WriteString(hash, fmt.Sprintf("entries:%d\nbytes:%d\n", index.EntryCount, index.LogicalBytes))
	bundle.ObservationDigest = hex.EncodeToString(hash.Sum(nil))
	return bundle, nil
}

func equalRcloneCatalogCommit(left, right RcloneCommitV1) bool {
	leftEncoded, leftErr := EncodeProviderCommit(NewRcloneProviderCommit(left))
	rightEncoded, rightErr := EncodeProviderCommit(NewRcloneProviderCommit(right))
	if leftErr != nil || rightErr != nil || leftEncoded != rightEncoded {
		return false
	}
	if left.Native == nil || right.Native == nil {
		return left.Native == nil && right.Native == nil
	}
	return left.Native.CommitKey == right.Native.CommitKey && left.Native.CommitVersionID == right.Native.CommitVersionID
}

func rcloneCatalogRecords(
	entries []rcloneCanonicalManifestEntry,
	nativeStates map[string]rcloneNativeManifestVersionStateV1,
) ([]CatalogRecord, error) {
	sort.Slice(entries, func(left, right int) bool { return entries[left].Path < entries[right].Path })
	records := make([]CatalogRecord, 0, len(entries))
	directories := make(map[string]struct{})
	for _, entry := range entries {
		if entry.Version != 1 || !validRcloneLogicalPath(entry.Path, 4096) || !validRcloneLogicalPath(entry.PhysicalPath, 4096) || entry.Size > uint64(^uint64(0)>>1) {
			return nil, fmt.Errorf("%w: invalid Rclone Catalog manifest record", ErrCatalogProtocol)
		}
		parent := path.Dir(entry.Path)
		if parent == "." {
			parent = ""
		}
		if parent != "" {
			if _, ok := directories[parent]; !ok {
				return nil, fmt.Errorf("%w: Rclone Catalog parent is missing", ErrCatalogProtocol)
			}
		}
		var entryType backupasset.CatalogEntryType
		switch entry.Kind {
		case "file":
			entryType = backupasset.CatalogEntryFile
		case "directory":
			entryType = backupasset.CatalogEntryDirectory
			directories[entry.Path] = struct{}{}
		case "symlink":
			entryType = backupasset.CatalogEntrySymlink
		default:
			return nil, fmt.Errorf("%w: invalid Rclone Catalog entry kind", ErrCatalogProtocol)
		}
		modifiedAt, err := time.Parse(time.RFC3339Nano, entry.ModTime)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid Rclone Catalog modification time", ErrCatalogProtocol)
		}
		locator := "portable:" + entry.PhysicalPath
		if state, ok := nativeStates[entry.Path]; ok {
			locator = "native:" + state.PhysicalKey + "\x00" + state.VersionID
		}
		strength := "none"
		if entry.SHA256 != "" {
			strength = "strong"
		}
		records = append(records, CatalogRecord{
			NormalizedPath: entry.Path, ParentNormalizedPath: parent, Name: path.Base(entry.Path), Type: entryType,
			Size: int64(entry.Size), ModifiedAt: timePointerUTC(modifiedAt), Mode: entry.Metadata["mode"],
			Owner: strings.Trim(entry.Metadata["uid"]+":"+entry.Metadata["gid"], ":"), Fingerprint: entry.SHA256,
			FingerprintStrength: strength, ProviderLocator: EntryLocator{Native: locator},
		})
	}
	return records, nil
}

func timePointerUTC(value time.Time) *time.Time {
	utc := value.UTC()
	return &utc
}

type materializedCatalogReadSession struct {
	mu         sync.Mutex
	request    CatalogReadRequest
	records    []CatalogRecord
	projection *CatalogProjectionAccumulator
	offset     int
	nextCursor string
	digest     string
	closed     bool
}

func newMaterializedCatalogReadSession(request CatalogReadRequest, records []CatalogRecord) (CatalogReadSession, error) {
	if len(records) > request.MaxItems || int64(len(records)) != request.Manifest.EntryCount {
		return nil, fmt.Errorf("%w: materialized Catalog count mismatch", ErrCatalogProofMismatch)
	}
	projection, err := NewCatalogProjectionAccumulator(request.Provider, request.Snapshot.RepositoryID, request.RecoveryPointID, request.Snapshot.SourceRevision)
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		if validateCatalogRecord(record) != nil || record.ProviderLocator.Native == "" || record.SealedProviderLocator != "" {
			return nil, fmt.Errorf("%w: invalid materialized Catalog record", ErrCatalogProtocol)
		}
	}
	return &materializedCatalogReadSession{request: request, records: records, projection: projection}, nil
}

func (session *materializedCatalogReadSession) SourceRevision() string {
	if session == nil {
		return ""
	}
	return session.request.Snapshot.SourceRevision
}

func (session *materializedCatalogReadSession) ListCanonical(ctx context.Context, request PageRequest) (CatalogRecordPage, error) {
	if session == nil {
		return CatalogRecordPage{}, ErrCatalogSessionClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return CatalogRecordPage{}, err
	}
	request, err := normalizeCatalogPageRequest(request)
	if err != nil {
		return CatalogRecordPage{}, err
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed {
		return CatalogRecordPage{}, ErrCatalogSessionClosed
	}
	if request.Cursor != session.nextCursor {
		return CatalogRecordPage{}, fmt.Errorf("%w: materialized Catalog continuation changed", ErrCatalogProtocol)
	}
	if session.offset == len(session.records) {
		if session.digest == "" {
			session.digest, _, err = session.projection.Finalize()
		}
		return CatalogRecordPage{}, err
	}
	end := min(session.offset+request.Limit, len(session.records))
	items := append([]CatalogRecord(nil), session.records[session.offset:end]...)
	for _, record := range items {
		if err := session.projection.Write(record); err != nil {
			return CatalogRecordPage{}, err
		}
	}
	session.offset = end
	page := CatalogRecordPage{Items: items}
	if session.offset < len(session.records) {
		session.nextCursor = strconv.Itoa(session.offset)
		page.NextCursor = session.nextCursor
	} else {
		session.nextCursor = ""
		session.digest, _, err = session.projection.Finalize()
	}
	return page, err
}

func (session *materializedCatalogReadSession) Finalize(ctx context.Context) (CatalogReadProof, error) {
	if ctx != nil && ctx.Err() != nil {
		return CatalogReadProof{}, ctx.Err()
	}
	if session == nil {
		return CatalogReadProof{}, ErrCatalogSessionClosed
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed {
		return CatalogReadProof{}, ErrCatalogSessionClosed
	}
	if session.offset != len(session.records) || session.digest == "" {
		return CatalogReadProof{}, ErrCatalogSessionIncomplete
	}
	return CatalogReadProof{
		Provider: session.request.Provider, Mode: session.request.Mode, SourceRevision: session.request.Snapshot.SourceRevision,
		Manifest: session.request.Manifest,
		Catalog:  CatalogProjectionProof{DigestAlgorithm: "sha256", Digest: session.digest, EntryCount: int64(len(session.records)), Complete: true},
	}, nil
}

func (session *materializedCatalogReadSession) Close() error {
	if session == nil {
		return nil
	}
	session.mu.Lock()
	session.closed = true
	session.records = nil
	session.mu.Unlock()
	return nil
}

var (
	_ CatalogReader = (*ResticAdapter)(nil)
	_ CatalogReader = (*RsyncAdapter)(nil)
	_ CatalogReader = (*RsyncCommittedPointAdapter)(nil)
	_ CatalogReader = (*RcloneAdapter)(nil)
	_ CatalogReader = (*RclonePublicationStrategy)(nil)
)
