package provider

import (
	"bufio"
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
	"strings"
	"sync"
	"time"

	"xirang/backend/internal/backupasset"
)

const resticAdapterRevision = "restic-reader:v1"

type ResticRuntimeAccess struct {
	NativeRepositoryID string               `json:"-"`
	Command            *RemoteCommandAccess `json:"-"`
}

type ResticAdapter struct {
	transport    CommandTransport
	cursors      *CursorCodec
	limitsSource OperationLimitsSource
	maxPageSize  int
	now          func() time.Time
}

func NewResticAdapter(transport CommandTransport, cursors *CursorCodec, limits OperationLimits, maxPageSize int, now func() time.Time) (*ResticAdapter, error) {
	return NewResticAdapterWithLimitsSource(transport, cursors, func() (OperationLimits, error) { return limits, nil }, maxPageSize, now)
}

func NewResticAdapterWithLimitsSource(transport CommandTransport, cursors *CursorCodec, limitsSource OperationLimitsSource, maxPageSize int, now func() time.Time) (*ResticAdapter, error) {
	if transport == nil || cursors == nil || maxPageSize <= 0 {
		return nil, fmt.Errorf("%w: invalid Restic adapter dependencies", backupasset.ErrInvalidState)
	}
	if _, err := resolveOperationLimits(limitsSource); err != nil {
		return nil, fmt.Errorf("%w: invalid Restic adapter limits", backupasset.ErrInvalidState)
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &ResticAdapter{transport: transport, cursors: cursors, limitsSource: limitsSource, maxPageSize: maxPageSize, now: now}, nil
}

func (adapter *ResticAdapter) Probe(ctx context.Context, binding AccessBinding, limits OperationLimits) (RepositoryObservation, error) {
	if err := adapter.validateBinding(binding); err != nil {
		return RepositoryObservation{}, err
	}
	if err := limits.Validate(); err != nil {
		return RepositoryObservation{}, err
	}
	version, err := adapter.run(ctx, adapter.repositoryInvocation(binding, OperationResticVersion, []string{"version"}, CommandPurposeProbe), limits)
	if err != nil {
		return RepositoryObservation{}, adapter.operationError(ctx, err)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(version.Stdout)), "restic ") {
		return RepositoryObservation{}, protocolCapabilityError()
	}
	configOutput, err := adapter.run(ctx, adapter.repositoryInvocation(binding, OperationResticConfig, []string{"--password-file", "/dev/stdin", "cat", "config"}, CommandPurposeProbe), limits)
	if err != nil {
		return RepositoryObservation{}, adapter.operationError(ctx, err)
	}
	var config struct {
		Version int    `json:"version"`
		ID      string `json:"id"`
	}
	if err := decodeSingleJSON(configOutput.Stdout, &config); err != nil || config.Version <= 0 {
		return RepositoryObservation{}, protocolCapabilityError()
	}
	identity, err := NativeRepositoryIdentity(backupasset.ProviderRestic, config.ID)
	if err != nil {
		return RepositoryObservation{}, newCapabilityError(backupasset.CapabilityRepositoryIdentityUnavailable)
	}
	capabilities := backupasset.CapabilitySet{
		List: true, SearchPath: true, OpenSequential: true, Download: true, Restore: true,
		Diff: true, NativeHistory: true,
	}
	return RepositoryObservation{
		Provider: backupasset.ProviderRestic, IdentityClass: IdentityNativeRepository, RepositoryIdentity: identity,
		VersionMode: backupasset.VersionNativeSnapshot, Capabilities: capabilities, AdapterRevision: resticAdapterRevision,
		SourceRevision: revisionDigest(identity + ":" + resticAdapterRevision), Availability: backupasset.PhysicalOnline, ObservedAt: adapter.now().UTC(),
	}, nil
}

func (adapter *ResticAdapter) ListPoints(ctx context.Context, snapshot ReadSnapshot, request PageRequest) (NativePointPage, error) {
	if err := adapter.validateSnapshot(snapshot); err != nil {
		return NativePointPage{}, err
	}
	limits, err := adapter.operationLimits()
	if err != nil {
		return NativePointPage{}, err
	}
	output, err := adapter.run(ctx, adapter.repositoryInvocation(snapshot.Access, OperationResticSnapshots, []string{"--password-file", "/dev/stdin", "snapshots", "--json"}, CommandPurposeList), limits)
	if err != nil {
		return NativePointPage{}, adapter.operationError(ctx, err)
	}
	points, err := parseResticSnapshots(output.Stdout, limits)
	if err != nil {
		return NativePointPage{}, err
	}
	sort.Slice(points, func(i, j int) bool {
		if points[i].CapturedAt.Equal(points[j].CapturedAt) {
			return points[i].OpaqueDigest < points[j].OpaqueDigest
		}
		return points[i].CapturedAt.Before(points[j].CapturedAt)
	})
	return adapter.pagePoints(ctx, snapshot, points, request)
}

func (adapter *ResticAdapter) ListEntries(ctx context.Context, snapshot ReadSnapshot, point PointLocator, parent EntryLocator, request PageRequest) (EntryPage, error) {
	if err := adapter.validateSnapshot(snapshot); err != nil {
		return EntryPage{}, err
	}
	if !lowerHex(point.Native, 64) {
		return EntryPage{}, fmt.Errorf("%w: exact Restic snapshot ID required", backupasset.ErrInvalidState)
	}
	limits, err := adapter.operationLimits()
	if err != nil {
		return EntryPage{}, err
	}
	parentPath, err := normalizeResticPath(parent.Native, true)
	if err != nil {
		return EntryPage{}, err
	}
	entries, err := adapter.loadEntries(ctx, snapshot.Access, point.Native, parentPath, false, CommandPurposeList, limits)
	if err != nil {
		return EntryPage{}, err
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Name == entries[j].Name {
			return entries[i].OpaqueDigest < entries[j].OpaqueDigest
		}
		return entries[i].Name < entries[j].Name
	})
	return adapter.pageEntries(ctx, snapshot, point, parentPath, entries, request)
}

func (adapter *ResticAdapter) StatEntry(ctx context.Context, snapshot ReadSnapshot, point PointLocator, locator EntryLocator) (Entry, error) {
	limits, err := adapter.operationLimits()
	if err != nil {
		return Entry{}, err
	}
	return adapter.statEntry(ctx, snapshot, point, locator, CommandPurposeList, limits)
}

func (adapter *ResticAdapter) statEntry(ctx context.Context, snapshot ReadSnapshot, point PointLocator, locator EntryLocator, purpose CommandPurpose, limits OperationLimits) (Entry, error) {
	if err := adapter.validateSnapshot(snapshot); err != nil {
		return Entry{}, err
	}
	if !lowerHex(point.Native, 64) {
		return Entry{}, fmt.Errorf("%w: exact Restic snapshot ID required", backupasset.ErrInvalidState)
	}
	entryPath, err := normalizeResticPath(locator.Native, false)
	if err != nil {
		return Entry{}, err
	}
	entries, err := adapter.loadEntries(ctx, snapshot.Access, point.Native, entryPath, true, purpose, limits)
	if err != nil {
		return Entry{}, err
	}
	for _, entry := range entries {
		if entry.Locator.Native == entryPath {
			return entry, nil
		}
	}
	return Entry{}, fmt.Errorf("%w: Restic entry", backupasset.ErrNotFound)
}

func (adapter *ResticAdapter) OpenSequential(ctx context.Context, snapshot ReadSnapshot, point PointLocator, locator EntryLocator, request ReadRequest) (ReadHandle, ContentStat, error) {
	if err := request.Validate(); err != nil {
		return nil, ContentStat{}, err
	}
	limits, err := adapter.operationLimits()
	if err != nil {
		return nil, ContentStat{}, err
	}
	entry, err := adapter.statEntry(ctx, snapshot, point, locator, CommandPurposeRead, limits)
	if err != nil {
		return nil, ContentStat{}, err
	}
	if entry.Type != backupasset.CatalogEntryFile {
		return nil, ContentStat{}, fmt.Errorf("%w: Restic entry is not a regular file", backupasset.ErrInvalidState)
	}
	invocation := adapter.repositoryInvocation(snapshot.Access, OperationResticDump, []string{"--password-file", "/dev/stdin", "dump", "--", point.Native, entry.Locator.Native}, CommandPurposeRead)
	if err := invocation.Validate(); err != nil {
		return nil, ContentStat{}, err
	}
	handle, err := adapter.transport.Open(ctx, invocation, limits, request.MaxBytes)
	if err != nil {
		return nil, ContentStat{}, adapter.operationError(ctx, err)
	}
	return newBoundedReadHandle(handle, request.MaxBytes), ContentStat{Size: entry.Size, ModTime: entry.ModTime, SourceRevision: entry.SourceRevision}, nil
}

func (adapter *ResticAdapter) loadEntries(ctx context.Context, binding AccessBinding, snapshotID, requestedPath string, exact bool, purpose CommandPurpose, limits OperationLimits) ([]Entry, error) {
	invocation := adapter.repositoryInvocation(binding, OperationResticList, []string{"--password-file", "/dev/stdin", "ls", "--json", "--", snapshotID, requestedPath}, purpose)
	output, err := adapter.run(ctx, invocation, limits)
	if err != nil {
		return nil, adapter.operationError(ctx, err)
	}
	return parseResticEntries(output.Stdout, snapshotID, requestedPath, exact, limits)
}

func (adapter *ResticAdapter) repositoryInvocation(binding AccessBinding, operation CommandOperation, arguments []string, purpose CommandPurpose) CommandInvocation {
	invocation := CommandInvocation{Tool: ToolRestic, Operation: operation, Purpose: purpose, Args: arguments}
	if operation != OperationResticVersion {
		invocation.SecretStdin = append([]byte(nil), binding.Secret...)
		invocation.PrivateLocator = binding.Locator
	}
	if runtimeAccess, ok := binding.AdapterData.(ResticRuntimeAccess); ok {
		invocation.Runtime = runtimeAccess.Command
	}
	return invocation
}

func (adapter *ResticAdapter) run(ctx context.Context, invocation CommandInvocation, limits OperationLimits) (CommandOutput, error) {
	if err := invocation.Validate(); err != nil {
		return CommandOutput{}, err
	}
	return adapter.transport.Run(ctx, invocation, limits)
}

func (adapter *ResticAdapter) validateBinding(binding AccessBinding) error {
	if adapter == nil || adapter.transport == nil || binding.Provider != backupasset.ProviderRestic || strings.TrimSpace(binding.Locator) == "" || len(binding.Secret) == 0 {
		return fmt.Errorf("%w: invalid Restic access binding", backupasset.ErrInvalidState)
	}
	return nil
}

func (adapter *ResticAdapter) operationLimits() (OperationLimits, error) {
	if adapter == nil {
		return OperationLimits{}, fmt.Errorf("%w: Restic adapter unavailable", backupasset.ErrInvalidState)
	}
	return resolveOperationLimits(adapter.limitsSource)
}

func (adapter *ResticAdapter) validateSnapshot(snapshot ReadSnapshot) error {
	if err := adapter.validateBinding(snapshot.Access); err != nil {
		return err
	}
	if backupasset.ValidateOpaqueID(snapshot.RepositoryID) != nil || snapshot.RepositoryID != snapshot.Access.RepositoryID || snapshot.CapabilityRevision <= 0 {
		return fmt.Errorf("%w: invalid Restic read snapshot", backupasset.ErrInvalidState)
	}
	access, ok := snapshot.Access.AdapterData.(ResticRuntimeAccess)
	if !ok || !lowerHex(access.NativeRepositoryID, 64) {
		return fmt.Errorf("%w: Restic native identity is unavailable", backupasset.ErrInvalidState)
	}
	return nil
}

func (adapter *ResticAdapter) operationError(ctx context.Context, err error) error {
	return mapCommandTransportError(ctx, err)
}

func protocolCapabilityError() error {
	return newCapabilityError(backupasset.CapabilityProviderProtocolIncompatible)
}

func decodeSingleJSON(payload []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("trailing JSON data")
	}
	return nil
}

func parseResticSnapshots(payload []byte, limits OperationLimits) ([]NativePoint, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('[') {
		return nil, protocolCapabilityError()
	}
	points := make([]NativePoint, 0)
	seen := make(map[string]struct{})
	for decoder.More() {
		if len(points) >= limits.MaxItems {
			return nil, newCapabilityError(backupasset.CapabilityProviderResourceLimit)
		}
		var row struct {
			ID   string    `json:"id"`
			Time time.Time `json:"time"`
		}
		if err := decoder.Decode(&row); err != nil || !lowerHex(row.ID, 64) || row.Time.IsZero() {
			return nil, protocolCapabilityError()
		}
		if _, duplicate := seen[row.ID]; duplicate {
			return nil, protocolCapabilityError()
		}
		seen[row.ID] = struct{}{}
		points = append(points, NativePoint{OpaqueDigest: stableDigest("restic-point", row.ID), CapturedAt: row.Time.UTC(), Semantics: backupasset.PointNativeSnapshot, SourceRevision: row.ID, Locator: PointLocator{Native: row.ID}})
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim(']') {
		return nil, protocolCapabilityError()
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, protocolCapabilityError()
	}
	return points, nil
}

func parseResticEntries(payload []byte, snapshotID, requestedPath string, exact bool, limits OperationLimits) ([]Entry, error) {
	scanner := bufio.NewScanner(bytes.NewReader(payload))
	scanner.Buffer(make([]byte, 4096), limits.MaxRecordBytes)
	entries := make([]Entry, 0)
	seen := make(map[string]struct{})
	headerSeen := false
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var envelope struct {
			StructType string `json:"struct_type"`
		}
		if err := json.Unmarshal(line, &envelope); err != nil {
			return nil, protocolCapabilityError()
		}
		switch envelope.StructType {
		case "snapshot":
			var header struct {
				StructType string `json:"struct_type"`
				ID         string `json:"id"`
			}
			if headerSeen || json.Unmarshal(line, &header) != nil || header.ID != snapshotID {
				return nil, protocolCapabilityError()
			}
			headerSeen = true
		case "node":
			if len(entries) >= limits.MaxItems {
				return nil, newCapabilityError(backupasset.CapabilityProviderResourceLimit)
			}
			var node struct {
				StructType string    `json:"struct_type"`
				Name       string    `json:"name"`
				Path       string    `json:"path"`
				Type       string    `json:"type"`
				Size       int64     `json:"size"`
				ModTime    time.Time `json:"mtime"`
			}
			if json.Unmarshal(line, &node) != nil || node.Size < 0 {
				return nil, protocolCapabilityError()
			}
			nodePath, err := normalizeResticPath(node.Path, false)
			if err != nil {
				return nil, protocolCapabilityError()
			}
			if exact {
				if nodePath != requestedPath {
					continue
				}
			} else if path.Dir(nodePath) != requestedPath {
				continue
			}
			entryType, ok := resticEntryType(node.Type)
			if !ok {
				return nil, protocolCapabilityError()
			}
			if _, duplicate := seen[nodePath]; duplicate {
				return nil, protocolCapabilityError()
			}
			seen[nodePath] = struct{}{}
			name := path.Base(nodePath)
			if node.Name != "" && node.Name != name {
				return nil, protocolCapabilityError()
			}
			entries = append(entries, Entry{OpaqueDigest: stableDigest("restic-entry", nodePath), Name: name, Type: entryType, Size: node.Size, ModTime: node.ModTime.UTC(), SourceRevision: stableDigest("restic-stat", fmt.Sprintf("%s:%d:%d", nodePath, node.Size, node.ModTime.UnixNano())), Locator: EntryLocator{Native: nodePath}})
		default:
			return nil, protocolCapabilityError()
		}
	}
	if err := scanner.Err(); err != nil || !headerSeen {
		return nil, protocolCapabilityError()
	}
	return entries, nil
}

func resticEntryType(value string) (backupasset.CatalogEntryType, bool) {
	switch value {
	case "file":
		return backupasset.CatalogEntryFile, true
	case "dir":
		return backupasset.CatalogEntryDirectory, true
	case "symlink":
		return backupasset.CatalogEntrySymlink, true
	case "fifo", "socket", "dev":
		return backupasset.CatalogEntrySpecial, true
	default:
		return "", false
	}
}

func normalizeResticPath(value string, allowRoot bool) (string, error) {
	if value == "" && allowRoot {
		return "/", nil
	}
	if strings.ContainsRune(value, '\x00') || !strings.HasPrefix(value, "/") {
		return "", fmt.Errorf("%w: invalid Restic entry locator", backupasset.ErrInvalidState)
	}
	clean := path.Clean(value)
	if clean == "/" && !allowRoot {
		return "", fmt.Errorf("%w: root is not an entry", backupasset.ErrInvalidState)
	}
	if clean != value {
		return "", fmt.Errorf("%w: non-canonical Restic entry locator", backupasset.ErrInvalidState)
	}
	return clean, nil
}

func (adapter *ResticAdapter) pagePoints(ctx context.Context, snapshot ReadSnapshot, items []NativePoint, request PageRequest) (NativePointPage, error) {
	request, err := request.Normalize(adapter.maxPageSize)
	if err != nil {
		return NativePointPage{}, err
	}
	listRevision := pointListRevision(snapshot.SourceRevision, items)
	start := 0
	if request.Cursor != "" {
		expected := CursorScope{Provider: backupasset.ProviderRestic, RepositoryID: snapshot.RepositoryID, CapabilityRevision: snapshot.CapabilityRevision, SourceRevision: listRevision, Direction: CursorForward}
		decoded, err := adapter.cursors.Decode(ctx, request.Cursor, expected)
		if err != nil {
			return NativePointPage{}, err
		}
		start = indexAfterPoint(items, decoded.LastItemDigest)
		if start == 0 {
			return NativePointPage{}, ErrStaleCursor
		}
	}
	end := min(start+request.Limit, len(items))
	page := NativePointPage{Items: append([]NativePoint(nil), items[start:end]...)}
	if end < len(items) && len(page.Items) > 0 {
		scope := CursorScope{Provider: backupasset.ProviderRestic, RepositoryID: snapshot.RepositoryID, CapabilityRevision: snapshot.CapabilityRevision, SourceRevision: listRevision, LastItemDigest: page.Items[len(page.Items)-1].OpaqueDigest, Direction: CursorForward}
		page.NextCursor, err = adapter.cursors.Encode(ctx, scope)
	}
	return page, err
}

func (adapter *ResticAdapter) pageEntries(ctx context.Context, snapshot ReadSnapshot, point PointLocator, parent string, items []Entry, request PageRequest) (EntryPage, error) {
	request, err := request.Normalize(adapter.maxPageSize)
	if err != nil {
		return EntryPage{}, err
	}
	listRevision := entryListRevision(snapshot.SourceRevision, items)
	start := 0
	if request.Cursor != "" {
		expected := CursorScope{Provider: backupasset.ProviderRestic, RepositoryID: snapshot.RepositoryID, PointScopeDigest: stableDigest("restic-point-scope", point.Native), ParentScopeDigest: stableDigest("restic-parent-scope", parent), CapabilityRevision: snapshot.CapabilityRevision, SourceRevision: listRevision, Direction: CursorForward}
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
		scope := CursorScope{Provider: backupasset.ProviderRestic, RepositoryID: snapshot.RepositoryID, PointScopeDigest: stableDigest("restic-point-scope", point.Native), ParentScopeDigest: stableDigest("restic-parent-scope", parent), CapabilityRevision: snapshot.CapabilityRevision, SourceRevision: listRevision, LastItemDigest: page.Items[len(page.Items)-1].OpaqueDigest, Direction: CursorForward}
		page.NextCursor, err = adapter.cursors.Encode(ctx, scope)
	}
	return page, err
}

func indexAfterPoint(items []NativePoint, digest string) int {
	for index := range items {
		if items[index].OpaqueDigest == digest {
			return index + 1
		}
	}
	return 0
}

func indexAfterEntry(items []Entry, digest string) int {
	for index := range items {
		if items[index].OpaqueDigest == digest {
			return index + 1
		}
	}
	return 0
}

func stableDigest(label, value string) string {
	digest := sha256.Sum256([]byte(label + "\x00" + value))
	return hex.EncodeToString(digest[:])
}

func pointListRevision(baseRevision string, items []NativePoint) string {
	parts := make([]string, 0, len(items)+1)
	parts = append(parts, revisionDigest(baseRevision))
	for _, item := range items {
		parts = append(parts, item.OpaqueDigest+":"+item.CapturedAt.UTC().Format(time.RFC3339Nano)+":"+revisionDigest(item.SourceRevision))
	}
	return stableDigest("provider-point-list", strings.Join(parts, "\x00"))
}

func entryListRevision(baseRevision string, items []Entry) string {
	parts := make([]string, 0, len(items)+1)
	parts = append(parts, revisionDigest(baseRevision))
	for _, item := range items {
		encoded, _ := json.Marshal(struct {
			Digest         string                       `json:"digest"`
			Name           string                       `json:"name"`
			Type           backupasset.CatalogEntryType `json:"type"`
			Size           int64                        `json:"size"`
			ModTime        time.Time                    `json:"mod_time"`
			SourceRevision string                       `json:"source_revision"`
		}{item.OpaqueDigest, item.Name, item.Type, item.Size, item.ModTime.UTC(), revisionDigest(item.SourceRevision)})
		parts = append(parts, string(encoded))
	}
	return stableDigest("provider-entry-list", strings.Join(parts, "\x00"))
}

func revisionDigest(value string) string {
	if lowerHex(value, 64) {
		return value
	}
	return stableDigest("source-revision", value)
}

type boundedReadHandle struct {
	underlying ReadHandle
	remaining  int64
	limitErr   error
	reachedEOF bool
	mu         sync.Mutex
}

func newBoundedReadHandle(underlying ReadHandle, maximum int64) ReadHandle {
	return &boundedReadHandle{underlying: underlying, remaining: maximum}
}

func (handle *boundedReadHandle) Read(buffer []byte) (int, error) {
	handle.mu.Lock()
	defer handle.mu.Unlock()
	if handle.limitErr != nil {
		return 0, handle.limitErr
	}
	if handle.remaining == 0 {
		var probe [1]byte
		count, err := handle.underlying.Read(probe[:])
		if count > 0 {
			handle.limitErr = newCapabilityError(backupasset.CapabilityProviderResourceLimit)
			return 0, handle.limitErr
		}
		if errors.Is(err, io.EOF) {
			handle.reachedEOF = true
		} else if err != nil {
			handle.limitErr = mapCommandTransportError(context.Background(), err)
			return 0, handle.limitErr
		}
		return 0, err
	}
	if int64(len(buffer)) > handle.remaining {
		buffer = buffer[:handle.remaining]
	}
	count, err := handle.underlying.Read(buffer)
	handle.remaining -= int64(count)
	if errors.Is(err, io.EOF) {
		handle.reachedEOF = true
	}
	return count, err
}

func (handle *boundedReadHandle) Close() error {
	handle.mu.Lock()
	if handle.limitErr == nil && handle.remaining == 0 && !handle.reachedEOF {
		var probe [1]byte
		count, err := io.ReadFull(handle.underlying, probe[:])
		switch {
		case count > 0:
			handle.limitErr = newCapabilityError(backupasset.CapabilityProviderResourceLimit)
		case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
			handle.reachedEOF = true
		case err != nil:
			handle.limitErr = mapCommandTransportError(context.Background(), err)
		}
	}
	limitErr := handle.limitErr
	handle.mu.Unlock()
	closeErr := handle.underlying.Close()
	if limitErr != nil {
		return limitErr
	}
	return closeErr
}

var (
	_ RepositoryProber = (*ResticAdapter)(nil)
	_ PointLister      = (*ResticAdapter)(nil)
	_ EntryLister      = (*ResticAdapter)(nil)
	_ EntryStatter     = (*ResticAdapter)(nil)
	_ SequentialReader = (*ResticAdapter)(nil)
)
