package provider

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/fileaccess"
)

const rsyncAdapterRevision = "rsync-tree-reader:v1"

type RsyncRuntimeAccess struct {
	Tree        fileaccess.Tree `json:"-"`
	Root        fileaccess.Root `json:"-"`
	RangeProven bool            `json:"-"`
}

type RsyncAdapter struct {
	cursors      *CursorCodec
	limitsSource OperationLimitsSource
	maxPageSize  int
	now          func() time.Time
}

func NewRsyncAdapter(cursors *CursorCodec, limits OperationLimits, maxPageSize int, now func() time.Time) (*RsyncAdapter, error) {
	return NewRsyncAdapterWithLimitsSource(cursors, func() (OperationLimits, error) { return limits, nil }, maxPageSize, now)
}

func NewRsyncAdapterWithLimitsSource(cursors *CursorCodec, limitsSource OperationLimitsSource, maxPageSize int, now func() time.Time) (*RsyncAdapter, error) {
	if cursors == nil || maxPageSize <= 0 {
		return nil, fmt.Errorf("%w: invalid Rsync adapter dependencies", backupasset.ErrInvalidState)
	}
	if _, err := resolveOperationLimits(limitsSource); err != nil {
		return nil, fmt.Errorf("%w: invalid Rsync adapter limits", backupasset.ErrInvalidState)
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &RsyncAdapter{cursors: cursors, limitsSource: limitsSource, maxPageSize: maxPageSize, now: now}, nil
}

func (adapter *RsyncAdapter) Probe(ctx context.Context, binding AccessBinding, limits OperationLimits) (RepositoryObservation, error) {
	runtimeAccess, err := adapter.validateBinding(binding)
	if err != nil {
		return RepositoryObservation{}, err
	}
	if err := limits.Validate(); err != nil {
		return RepositoryObservation{}, err
	}
	rootEntry, err := runtimeAccess.Tree.Lstat(ctx, runtimeAccess.Root, fileaccess.RootLocator(), fileaccess.ProviderPolicy)
	if err != nil {
		return RepositoryObservation{}, mapTreeError(ctx, err)
	}
	if rootEntry.Type != fileaccess.EntryDirectory || rootEntry.SourceRevision == "" {
		return RepositoryObservation{}, protocolCapabilityError()
	}
	identity, err := DeriveScopedIdentity(binding.IdentitySalt, ScopedIdentityDocument{
		Provider: backupasset.ProviderRsync, TaskID: binding.TaskID, NodeID: binding.NodeID, EndpointFacts: binding.EndpointFacts,
	})
	if err != nil {
		return RepositoryObservation{}, newCapabilityError(backupasset.CapabilityRepositoryIdentityUnavailable)
	}
	rangeProven := proveTreeRange(ctx, runtimeAccess, limits)
	capabilities := backupasset.CapabilitySet{List: true, SearchPath: true, OpenSequential: true, OpenRange: rangeProven, Download: true, Restore: true}
	return RepositoryObservation{
		Provider: backupasset.ProviderRsync, IdentityClass: IdentityTaskScopedEndpoint, RepositoryIdentity: identity,
		VersionMode: backupasset.VersionMutableHead, Capabilities: capabilities, AdapterRevision: rsyncAdapterRevision,
		SourceRevision: rootEntry.SourceRevision, Availability: backupasset.PhysicalOnline, ObservedAt: adapter.now().UTC(),
	}, nil
}

func (adapter *RsyncAdapter) ListPoints(ctx context.Context, snapshot ReadSnapshot, request PageRequest) (NativePointPage, error) {
	_, err := adapter.validateOperation(ctx, snapshot, rsyncPointLocator(snapshot.SourceRevision))
	if err != nil {
		return NativePointPage{}, err
	}
	request, err = request.Normalize(adapter.maxPageSize)
	if err != nil {
		return NativePointPage{}, err
	}
	point := NativePoint{
		OpaqueDigest: stableDigest("rsync-mutable-point", snapshot.RepositoryID), CapturedAt: adapter.now().UTC(),
		Semantics: backupasset.PointMutableHead, SourceRevision: snapshot.SourceRevision, Locator: rsyncPointLocator(snapshot.SourceRevision),
	}
	if request.Cursor == "" {
		return NativePointPage{Items: []NativePoint{point}}, nil
	}
	expected := CursorScope{Provider: backupasset.ProviderRsync, RepositoryID: snapshot.RepositoryID, CapabilityRevision: snapshot.CapabilityRevision, SourceRevision: revisionDigest(snapshot.SourceRevision), Direction: CursorForward}
	decoded, err := adapter.cursors.Decode(ctx, request.Cursor, expected)
	if err != nil {
		return NativePointPage{}, err
	}
	if decoded.LastItemDigest != point.OpaqueDigest {
		return NativePointPage{}, ErrStaleCursor
	}
	return NativePointPage{}, nil
}

func (adapter *RsyncAdapter) ListEntries(ctx context.Context, snapshot ReadSnapshot, point PointLocator, parent EntryLocator, request PageRequest) (EntryPage, error) {
	runtimeAccess, err := adapter.validateOperation(ctx, snapshot, point)
	if err != nil {
		return EntryPage{}, err
	}
	parentLocator, parentScope, err := rsyncFileLocator(parent.Native, true)
	if err != nil {
		return EntryPage{}, err
	}
	limits, err := adapter.operationLimits()
	if err != nil {
		return EntryPage{}, err
	}
	page, err := runtimeAccess.Tree.List(ctx, runtimeAccess.Root, parentLocator, fileaccess.ProviderPolicy, fileaccess.PageRequest{
		Limit: limits.MaxItems, MaxItems: limits.MaxItems, MaxBytes: limits.MaxMetadataBytes,
	})
	if err != nil {
		return EntryPage{}, mapTreeError(ctx, err)
	}
	if page.HasMore {
		return EntryPage{}, newCapabilityError(backupasset.CapabilityProviderResourceLimit)
	}
	if err := adapter.verifyRoot(ctx, runtimeAccess, snapshot.SourceRevision); err != nil {
		return EntryPage{}, err
	}
	entries := make([]Entry, 0, len(page.Items))
	for _, item := range page.Items {
		entryType, ok := catalogTypeFromFileAccess(item.Type)
		if !ok {
			return EntryPage{}, protocolCapabilityError()
		}
		entries = append(entries, Entry{
			OpaqueDigest: item.OpaqueDigest, Name: item.Name, Type: entryType, Size: item.Size, ModTime: item.ModTime,
			SourceRevision: item.SourceRevision, Locator: EntryLocator{Native: item.Locator.Path},
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Name == entries[j].Name {
			return entries[i].OpaqueDigest < entries[j].OpaqueDigest
		}
		return entries[i].Name < entries[j].Name
	})
	return adapter.pageEntries(ctx, snapshot, point, parentScope, entries, request)
}

func (adapter *RsyncAdapter) StatEntry(ctx context.Context, snapshot ReadSnapshot, point PointLocator, locator EntryLocator) (Entry, error) {
	runtimeAccess, err := adapter.validateOperation(ctx, snapshot, point)
	if err != nil {
		return Entry{}, err
	}
	fileLocator, _, err := rsyncFileLocator(locator.Native, false)
	if err != nil {
		return Entry{}, err
	}
	item, err := runtimeAccess.Tree.Lstat(ctx, runtimeAccess.Root, fileLocator, fileaccess.ProviderPolicy)
	if err != nil {
		return Entry{}, mapTreeError(ctx, err)
	}
	if err := adapter.verifyRoot(ctx, runtimeAccess, snapshot.SourceRevision); err != nil {
		return Entry{}, err
	}
	entryType, ok := catalogTypeFromFileAccess(item.Type)
	if !ok {
		return Entry{}, protocolCapabilityError()
	}
	return Entry{OpaqueDigest: item.OpaqueDigest, Name: item.Name, Type: entryType, Size: item.Size, ModTime: item.ModTime, SourceRevision: item.SourceRevision, Locator: EntryLocator{Native: item.Locator.Path}}, nil
}

func (adapter *RsyncAdapter) OpenSequential(ctx context.Context, snapshot ReadSnapshot, point PointLocator, locator EntryLocator, request ReadRequest) (ReadHandle, ContentStat, error) {
	if err := request.Validate(); err != nil {
		return nil, ContentStat{}, err
	}
	runtimeAccess, err := adapter.validateOperation(ctx, snapshot, point)
	if err != nil {
		return nil, ContentStat{}, err
	}
	fileLocator, _, err := rsyncFileLocator(locator.Native, false)
	if err != nil {
		return nil, ContentStat{}, err
	}
	handle, stat, err := runtimeAccess.Tree.OpenRegular(ctx, runtimeAccess.Root, fileLocator, fileaccess.ProviderPolicy)
	if err != nil {
		return nil, ContentStat{}, mapTreeError(ctx, err)
	}
	checked := &treeInvariantHandle{underlying: handle, verify: func() error { return adapter.verifyRoot(ctx, runtimeAccess, snapshot.SourceRevision) }}
	return newBoundedReadHandle(checked, request.MaxBytes), providerContentStat(stat), nil
}

func (adapter *RsyncAdapter) OpenRange(ctx context.Context, snapshot ReadSnapshot, point PointLocator, locator EntryLocator, byteRange ByteRange) (ReadHandle, ContentStat, error) {
	if err := byteRange.Validate(); err != nil {
		return nil, ContentStat{}, err
	}
	runtimeAccess, err := adapter.validateOperation(ctx, snapshot, point)
	if err != nil {
		return nil, ContentStat{}, err
	}
	if !runtimeAccess.RangeProven {
		return nil, ContentStat{}, newCapabilityError(backupasset.CapabilityRangeUnavailable)
	}
	fileLocator, _, err := rsyncFileLocator(locator.Native, false)
	if err != nil {
		return nil, ContentStat{}, err
	}
	handle, stat, err := runtimeAccess.Tree.OpenRange(ctx, runtimeAccess.Root, fileLocator, fileaccess.ProviderPolicy, fileaccess.ByteRange{Offset: byteRange.Offset, Length: byteRange.Length})
	if err != nil {
		return nil, ContentStat{}, mapTreeError(ctx, err)
	}
	checked := &treeInvariantHandle{underlying: handle, verify: func() error { return adapter.verifyRoot(ctx, runtimeAccess, snapshot.SourceRevision) }}
	return checked, providerContentStat(stat), nil
}

func (adapter *RsyncAdapter) validateBinding(binding AccessBinding) (RsyncRuntimeAccess, error) {
	if adapter == nil || binding.Provider != backupasset.ProviderRsync || binding.TaskID == 0 || binding.NodeID == 0 || len(binding.IdentitySalt) != IdentitySaltBytes || len(binding.EndpointFacts) == 0 {
		return RsyncRuntimeAccess{}, fmt.Errorf("%w: invalid Rsync access binding", backupasset.ErrInvalidState)
	}
	runtimeAccess, ok := binding.AdapterData.(RsyncRuntimeAccess)
	if !ok || runtimeAccess.Tree == nil || strings.TrimSpace(runtimeAccess.Root.Path) == "" {
		return RsyncRuntimeAccess{}, fmt.Errorf("%w: Rsync tree access unavailable", backupasset.ErrInvalidState)
	}
	return runtimeAccess, nil
}

func (adapter *RsyncAdapter) operationLimits() (OperationLimits, error) {
	if adapter == nil {
		return OperationLimits{}, fmt.Errorf("%w: Rsync adapter unavailable", backupasset.ErrInvalidState)
	}
	return resolveOperationLimits(adapter.limitsSource)
}

func (adapter *RsyncAdapter) validateOperation(ctx context.Context, snapshot ReadSnapshot, point PointLocator) (RsyncRuntimeAccess, error) {
	runtimeAccess, err := adapter.validateBinding(snapshot.Access)
	if err != nil {
		return RsyncRuntimeAccess{}, err
	}
	if backupasset.ValidateOpaqueID(snapshot.RepositoryID) != nil || snapshot.RepositoryID != snapshot.Access.RepositoryID || snapshot.CapabilityRevision <= 0 || snapshot.SourceRevision == "" || point.Native != rsyncPointLocator(snapshot.SourceRevision).Native {
		return RsyncRuntimeAccess{}, fmt.Errorf("%w: invalid Rsync read snapshot", backupasset.ErrInvalidState)
	}
	if err := adapter.verifyRoot(ctx, runtimeAccess, snapshot.SourceRevision); err != nil {
		return RsyncRuntimeAccess{}, err
	}
	return runtimeAccess, nil
}

func (adapter *RsyncAdapter) verifyRoot(ctx context.Context, runtimeAccess RsyncRuntimeAccess, expected string) error {
	entry, err := runtimeAccess.Tree.Lstat(ctx, runtimeAccess.Root, fileaccess.RootLocator(), fileaccess.ProviderPolicy)
	if err != nil {
		return mapTreeError(ctx, err)
	}
	if entry.Type != fileaccess.EntryDirectory || entry.SourceRevision != expected {
		return newCapabilityError(backupasset.CapabilityMutableSourceChanged)
	}
	return nil
}

func proveTreeRange(ctx context.Context, runtimeAccess RsyncRuntimeAccess, limits OperationLimits) bool {
	page, err := runtimeAccess.Tree.List(ctx, runtimeAccess.Root, fileaccess.RootLocator(), fileaccess.ProviderPolicy, fileaccess.PageRequest{
		Limit: min(32, limits.MaxItems), MaxItems: limits.MaxItems, MaxBytes: limits.MaxMetadataBytes,
	})
	if err != nil {
		return false
	}
	for _, item := range page.Items {
		if item.Type != fileaccess.EntryFile || item.Size <= 0 {
			continue
		}
		length := min(int64(16), item.Size)
		sequential, _, err := runtimeAccess.Tree.OpenRegular(ctx, runtimeAccess.Root, item.Locator, fileaccess.ProviderPolicy)
		if err != nil {
			return false
		}
		want, readErr := io.ReadAll(io.LimitReader(sequential, length))
		closeErr := sequential.Close()
		if readErr != nil || closeErr != nil || int64(len(want)) != length {
			return false
		}
		ranged, _, err := runtimeAccess.Tree.OpenRange(ctx, runtimeAccess.Root, item.Locator, fileaccess.ProviderPolicy, fileaccess.ByteRange{Offset: 0, Length: length})
		if err != nil {
			return false
		}
		got, readErr := io.ReadAll(ranged)
		closeErr = ranged.Close()
		return readErr == nil && closeErr == nil && bytes.Equal(got, want)
	}
	return false
}

func rsyncPointLocator(sourceRevision string) PointLocator {
	return PointLocator{Native: "mutable:" + revisionDigest(sourceRevision)}
}

func rsyncFileLocator(value string, allowRoot bool) (fileaccess.Locator, string, error) {
	if value == "" && allowRoot {
		return fileaccess.RootLocator(), "/", nil
	}
	locator, err := fileaccess.ParseLocator(value, fileaccess.ProviderPolicy)
	if err != nil {
		return fileaccess.Locator{}, "", fmt.Errorf("%w: invalid Rsync entry locator", backupasset.ErrInvalidState)
	}
	return locator, filepathScope(value), nil
}

func filepathScope(value string) string {
	return strings.ReplaceAll(value, "\\", "/")
}

func catalogTypeFromFileAccess(value fileaccess.EntryType) (backupasset.CatalogEntryType, bool) {
	switch value {
	case fileaccess.EntryFile:
		return backupasset.CatalogEntryFile, true
	case fileaccess.EntryDirectory:
		return backupasset.CatalogEntryDirectory, true
	case fileaccess.EntrySymlink:
		return backupasset.CatalogEntrySymlink, true
	case fileaccess.EntrySpecial:
		return backupasset.CatalogEntrySpecial, true
	default:
		return "", false
	}
}

func providerContentStat(stat fileaccess.ContentStat) ContentStat {
	return ContentStat{Size: stat.Size, ModTime: stat.ModTime, SourceRevision: stat.SourceRevision}
}

func mapTreeError(ctx context.Context, err error) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	switch {
	case errors.Is(err, fileaccess.ErrSourceChanged):
		return newCapabilityError(backupasset.CapabilityMutableSourceChanged)
	case errors.Is(err, fileaccess.ErrResourceLimit):
		return newCapabilityError(backupasset.CapabilityProviderResourceLimit)
	case errors.Is(err, fileaccess.ErrStrictUnavailable):
		return newCapabilityError(backupasset.CapabilityProviderUnavailable)
	default:
		return newCapabilityError(backupasset.CapabilityRepositoryOffline)
	}
}

type treeInvariantHandle struct {
	underlying ReadHandle
	verify     func() error
}

func (handle *treeInvariantHandle) Read(buffer []byte) (int, error) {
	return handle.underlying.Read(buffer)
}
func (handle *treeInvariantHandle) Close() error {
	closeErr := handle.underlying.Close()
	verifyErr := handle.verify()
	if closeErr != nil {
		return mapTreeError(context.Background(), closeErr)
	}
	return verifyErr
}

func (adapter *RsyncAdapter) pageEntries(ctx context.Context, snapshot ReadSnapshot, point PointLocator, parent string, items []Entry, request PageRequest) (EntryPage, error) {
	request, err := request.Normalize(adapter.maxPageSize)
	if err != nil {
		return EntryPage{}, err
	}
	listRevision := entryListRevision(snapshot.SourceRevision, items)
	start := 0
	if request.Cursor != "" {
		expected := CursorScope{Provider: backupasset.ProviderRsync, RepositoryID: snapshot.RepositoryID, PointScopeDigest: stableDigest("rsync-point-scope", point.Native), ParentScopeDigest: stableDigest("rsync-parent-scope", parent), CapabilityRevision: snapshot.CapabilityRevision, SourceRevision: listRevision, Direction: CursorForward}
		decoded, decodeErr := adapter.cursors.Decode(ctx, request.Cursor, expected)
		if decodeErr != nil {
			return EntryPage{}, decodeErr
		}
		start = indexAfterEntry(items, decoded.LastItemDigest)
		if start == 0 {
			return EntryPage{}, ErrStaleCursor
		}
	}
	end := min(start+request.Limit, len(items))
	page := EntryPage{Items: append([]Entry(nil), items[start:end]...)}
	if end < len(items) && len(page.Items) > 0 {
		scope := CursorScope{Provider: backupasset.ProviderRsync, RepositoryID: snapshot.RepositoryID, PointScopeDigest: stableDigest("rsync-point-scope", point.Native), ParentScopeDigest: stableDigest("rsync-parent-scope", parent), CapabilityRevision: snapshot.CapabilityRevision, SourceRevision: listRevision, LastItemDigest: page.Items[len(page.Items)-1].OpaqueDigest, Direction: CursorForward}
		page.NextCursor, err = adapter.cursors.Encode(ctx, scope)
	}
	return page, err
}

var (
	_ RepositoryProber = (*RsyncAdapter)(nil)
	_ PointLister      = (*RsyncAdapter)(nil)
	_ EntryLister      = (*RsyncAdapter)(nil)
	_ EntryStatter     = (*RsyncAdapter)(nil)
	_ SequentialReader = (*RsyncAdapter)(nil)
	_ RangeReader      = (*RsyncAdapter)(nil)
)
