package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"xirang/backend/internal/backupasset"
)

const rcloneAdapterRevision = "rclone-reader:v1"

const managedRcloneVersion = "rclone v1.74.4"

var safeRcloneBackend = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

func ValidateManagedRcloneVersion(output []byte) error {
	firstLine := strings.TrimSpace(strings.SplitN(string(output), "\n", 2)[0])
	if firstLine != managedRcloneVersion {
		return fmt.Errorf("%w: managed Rclone runtime must be v1.74.4", backupasset.ErrCapabilityUnavailable)
	}
	return nil
}

type RcloneConfigSource string

const (
	RcloneConfigBound       RcloneConfigSource = "bound"
	RcloneConfigNodeDefault RcloneConfigSource = "node_default"
)

type RcloneRuntimeAccess struct {
	Backend      string                    `json:"-"`
	RangeProven  bool                      `json:"-"`
	ConfigSource RcloneConfigSource        `json:"-"`
	Command      *RemoteCommandAccess      `json:"-"`
	ManagedPoint *RcloneManagedPointAccess `json:"-"`
}

type RcloneManagedPointAccess struct {
	RecoveryPointID string `json:"-"`
	AttemptID       string `json:"-"`
	DataLocator     string `json:"-"`
	ManifestDigest  string `json:"-"`
	SourceRevision  string `json:"-"`
	Committed       bool   `json:"-"`
}

type RcloneAdapter struct {
	transport    CommandTransport
	cursors      *CursorCodec
	limitsSource OperationLimitsSource
	maxPageSize  int
	now          func() time.Time
}

func NewRcloneAdapter(transport CommandTransport, cursors *CursorCodec, limits OperationLimits, maxPageSize int, now func() time.Time) (*RcloneAdapter, error) {
	return NewRcloneAdapterWithLimitsSource(transport, cursors, func() (OperationLimits, error) { return limits, nil }, maxPageSize, now)
}

func NewRcloneAdapterWithLimitsSource(transport CommandTransport, cursors *CursorCodec, limitsSource OperationLimitsSource, maxPageSize int, now func() time.Time) (*RcloneAdapter, error) {
	if transport == nil || cursors == nil || maxPageSize <= 0 {
		return nil, fmt.Errorf("%w: invalid Rclone adapter dependencies", backupasset.ErrInvalidState)
	}
	if _, err := resolveOperationLimits(limitsSource); err != nil {
		return nil, fmt.Errorf("%w: invalid Rclone adapter limits", backupasset.ErrInvalidState)
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &RcloneAdapter{transport: transport, cursors: cursors, limitsSource: limitsSource, maxPageSize: maxPageSize, now: now}, nil
}

func (adapter *RcloneAdapter) Probe(ctx context.Context, binding AccessBinding, limits OperationLimits) (RepositoryObservation, error) {
	if err := adapter.validateBinding(binding); err != nil {
		return RepositoryObservation{}, err
	}
	if err := limits.Validate(); err != nil {
		return RepositoryObservation{}, err
	}
	version, err := adapter.run(ctx, adapter.invocation(binding, OperationRcloneVersion, []string{"version"}, "", CommandPurposeProbe), limits)
	if err != nil {
		return RepositoryObservation{}, mapRcloneOperationError(ctx, err)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(version.Stdout)), "rclone v") {
		return RepositoryObservation{}, protocolCapabilityError()
	}
	features, err := adapter.run(ctx, adapter.invocation(binding, OperationRcloneFeatures, []string{"backend", "features", "--"}, binding.Locator, CommandPurposeProbe), limits)
	if err != nil {
		return RepositoryObservation{}, mapRcloneOperationError(ctx, err)
	}
	backend, err := parseRcloneBackend(features.Stdout)
	if err != nil {
		return RepositoryObservation{}, err
	}
	rootEntries, err := adapter.loadList(ctx, binding, "", limits, CommandPurposeProbe)
	if err != nil {
		return RepositoryObservation{}, err
	}
	sourceRevision := rcloneListFingerprint(rootEntries)
	facts := append([]string(nil), binding.EndpointFacts...)
	facts = append(facts, "backend:"+backend)
	identity, err := DeriveScopedIdentity(binding.IdentitySalt, ScopedIdentityDocument{Provider: backupasset.ProviderRclone, TaskID: binding.TaskID, NodeID: binding.NodeID, EndpointFacts: facts})
	if err != nil {
		return RepositoryObservation{}, newCapabilityError(backupasset.CapabilityRepositoryIdentityUnavailable)
	}
	rangeProven := adapter.proveRange(ctx, binding, rootEntries, limits, CommandPurposeProbe)
	capabilities := backupasset.CapabilitySet{List: true, SearchPath: true, OpenSequential: true, OpenRange: rangeProven, Download: true, Restore: true}
	fingerprintMaterial := binding.Secret
	if rcloneConfigSource(binding) == RcloneConfigNodeDefault {
		fingerprintMaterial = []byte("rclone-config-source:node_default")
	}
	fingerprint, _ := DeriveConfigFingerprint(binding.IdentitySalt, fingerprintMaterial)
	return RepositoryObservation{
		Provider: backupasset.ProviderRclone, IdentityClass: IdentityTaskScopedEndpoint, RepositoryIdentity: identity,
		VersionMode: backupasset.VersionMutableHead, Capabilities: capabilities, AdapterRevision: rcloneAdapterRevision,
		SourceRevision: sourceRevision, Availability: backupasset.PhysicalOnline, ObservedAt: adapter.now().UTC(), ConfigFingerprint: fingerprint,
		InternalProviderFacts: map[string]string{"backend": backend},
	}, nil
}

func (adapter *RcloneAdapter) ListPoints(ctx context.Context, snapshot ReadSnapshot, request PageRequest) (NativePointPage, error) {
	limits, err := adapter.operationLimits()
	if err != nil {
		return NativePointPage{}, err
	}
	runtimeAccess, err := adapter.runtimeAccess(snapshot.Access)
	if err != nil {
		return NativePointPage{}, err
	}
	pointLocator := rclonePointLocator(snapshot.SourceRevision)
	semantics := backupasset.PointMutableHead
	opaqueDigest := stableDigest("rclone-mutable-point", snapshot.RepositoryID)
	if runtimeAccess.ManagedPoint != nil {
		pointLocator = rcloneManagedPointLocator(*runtimeAccess.ManagedPoint)
		semantics = backupasset.PointXirangManifest
		opaqueDigest = stableDigest("rclone-managed-point", runtimeAccess.ManagedPoint.RecoveryPointID+"\x00"+runtimeAccess.ManagedPoint.AttemptID+"\x00"+runtimeAccess.ManagedPoint.ManifestDigest)
	}
	if _, err := adapter.validateOperation(ctx, snapshot, pointLocator, CommandPurposeList, limits); err != nil {
		return NativePointPage{}, err
	}
	request, err = request.Normalize(adapter.maxPageSize)
	if err != nil {
		return NativePointPage{}, err
	}
	point := NativePoint{OpaqueDigest: opaqueDigest, CapturedAt: adapter.now().UTC(), Semantics: semantics, SourceRevision: snapshot.SourceRevision, Locator: pointLocator}
	if request.Cursor == "" {
		return NativePointPage{Items: []NativePoint{point}}, nil
	}
	expected := CursorScope{Provider: backupasset.ProviderRclone, RepositoryID: snapshot.RepositoryID, CapabilityRevision: snapshot.CapabilityRevision, SourceRevision: revisionDigest(snapshot.SourceRevision), Direction: CursorForward}
	decoded, err := adapter.cursors.Decode(ctx, request.Cursor, expected)
	if err != nil {
		return NativePointPage{}, err
	}
	if decoded.LastItemDigest != point.OpaqueDigest {
		return NativePointPage{}, ErrStaleCursor
	}
	return NativePointPage{}, nil
}

func (adapter *RcloneAdapter) ListEntries(ctx context.Context, snapshot ReadSnapshot, point PointLocator, parent EntryLocator, request PageRequest) (EntryPage, error) {
	limits, err := adapter.operationLimits()
	if err != nil {
		return EntryPage{}, err
	}
	rootEntries, err := adapter.validateOperation(ctx, snapshot, point, CommandPurposeList, limits)
	if err != nil {
		return EntryPage{}, err
	}
	parentPath, err := normalizeRclonePath(parent.Native, true)
	if err != nil {
		return EntryPage{}, err
	}
	entries := rootEntries
	if parentPath != "" {
		entries, err = adapter.loadList(ctx, snapshot.Access, parentPath, limits, CommandPurposeList)
		if err != nil {
			return EntryPage{}, err
		}
	}
	entries = rebaseRcloneEntries(parentPath, entries)
	rootAfter, err := adapter.loadList(ctx, snapshot.Access, "", limits, CommandPurposeList)
	if err != nil {
		return EntryPage{}, err
	}
	if rcloneListFingerprint(rootAfter) != snapshot.SourceRevision {
		return EntryPage{}, newCapabilityError(backupasset.CapabilityMutableSourceChanged)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Name == entries[j].Name {
			return entries[i].OpaqueDigest < entries[j].OpaqueDigest
		}
		return entries[i].Name < entries[j].Name
	})
	return adapter.pageEntries(ctx, snapshot, point, parentPath, entries, request)
}

func (adapter *RcloneAdapter) StatEntry(ctx context.Context, snapshot ReadSnapshot, point PointLocator, locator EntryLocator) (Entry, error) {
	limits, err := adapter.operationLimits()
	if err != nil {
		return Entry{}, err
	}
	return adapter.statEntry(ctx, snapshot, point, locator, CommandPurposeList, limits)
}

func (adapter *RcloneAdapter) statEntry(ctx context.Context, snapshot ReadSnapshot, point PointLocator, locator EntryLocator, purpose CommandPurpose, limits OperationLimits) (Entry, error) {
	if _, err := adapter.validateOperation(ctx, snapshot, point, purpose, limits); err != nil {
		return Entry{}, err
	}
	entryPath, err := normalizeRclonePath(locator.Native, false)
	if err != nil {
		return Entry{}, err
	}
	entry, err := adapter.stat(ctx, snapshot.Access, entryPath, limits, purpose)
	if err != nil {
		return Entry{}, err
	}
	rootAfter, err := adapter.loadList(ctx, snapshot.Access, "", limits, purpose)
	if err != nil {
		return Entry{}, err
	}
	if rcloneListFingerprint(rootAfter) != snapshot.SourceRevision {
		return Entry{}, newCapabilityError(backupasset.CapabilityMutableSourceChanged)
	}
	return entry, nil
}

func (adapter *RcloneAdapter) OpenSequential(ctx context.Context, snapshot ReadSnapshot, point PointLocator, locator EntryLocator, request ReadRequest) (ReadHandle, ContentStat, error) {
	if err := request.Validate(); err != nil {
		return nil, ContentStat{}, err
	}
	limits, err := adapter.operationLimits()
	if err != nil {
		return nil, ContentStat{}, err
	}
	if _, err := adapter.validateOperation(ctx, snapshot, point, CommandPurposeRead, limits); err != nil {
		return nil, ContentStat{}, err
	}
	entryPath, err := normalizeRclonePath(locator.Native, false)
	if err != nil {
		return nil, ContentStat{}, err
	}
	entry, err := adapter.stat(ctx, snapshot.Access, entryPath, limits, CommandPurposeRead)
	if err != nil {
		return nil, ContentStat{}, err
	}
	if entry.Type != backupasset.CatalogEntryFile {
		return nil, ContentStat{}, fmt.Errorf("%w: Rclone object is not a regular file", backupasset.ErrInvalidState)
	}
	invocation := adapter.invocation(snapshot.Access, OperationRcloneCat, []string{"cat", "--"}, rcloneObjectLocator(snapshot.Access.Locator, entryPath), CommandPurposeRead)
	if err := invocation.Validate(); err != nil {
		return nil, ContentStat{}, err
	}
	handle, err := adapter.transport.Open(ctx, invocation, limits, request.MaxBytes)
	if err != nil {
		return nil, ContentStat{}, mapRcloneOperationError(ctx, err)
	}
	checked := &commandInvariantHandle{underlying: handle, verify: func() error { return adapter.verifyObjectAndRoot(ctx, snapshot, entry, CommandPurposeRead, limits) }}
	return newBoundedReadHandle(checked, request.MaxBytes), ContentStat{Size: entry.Size, ModTime: entry.ModTime, SourceRevision: entry.SourceRevision}, nil
}

func (adapter *RcloneAdapter) OpenRange(ctx context.Context, snapshot ReadSnapshot, point PointLocator, locator EntryLocator, byteRange ByteRange) (ReadHandle, ContentStat, error) {
	if err := byteRange.Validate(); err != nil {
		return nil, ContentStat{}, err
	}
	runtimeAccess, err := adapter.runtimeAccess(snapshot.Access)
	if err != nil {
		return nil, ContentStat{}, err
	}
	if !runtimeAccess.RangeProven {
		return nil, ContentStat{}, newCapabilityError(backupasset.CapabilityRangeUnavailable)
	}
	limits, err := adapter.operationLimits()
	if err != nil {
		return nil, ContentStat{}, err
	}
	if _, err := adapter.validateOperation(ctx, snapshot, point, CommandPurposeRead, limits); err != nil {
		return nil, ContentStat{}, err
	}
	entryPath, err := normalizeRclonePath(locator.Native, false)
	if err != nil {
		return nil, ContentStat{}, err
	}
	entry, err := adapter.stat(ctx, snapshot.Access, entryPath, limits, CommandPurposeRead)
	if err != nil {
		return nil, ContentStat{}, err
	}
	if entry.Type != backupasset.CatalogEntryFile {
		return nil, ContentStat{}, fmt.Errorf("%w: Rclone object is not a regular file", backupasset.ErrInvalidState)
	}
	arguments := []string{"cat", "--offset", strconv.FormatInt(byteRange.Offset, 10), "--count", strconv.FormatInt(byteRange.Length, 10), "--"}
	invocation := adapter.invocation(snapshot.Access, OperationRcloneCat, arguments, rcloneObjectLocator(snapshot.Access.Locator, entryPath), CommandPurposeRead)
	handle, err := adapter.transport.Open(ctx, invocation, limits, byteRange.Length)
	if err != nil {
		return nil, ContentStat{}, mapRcloneOperationError(ctx, err)
	}
	checked := &commandInvariantHandle{underlying: newBoundedReadHandle(handle, byteRange.Length), verify: func() error { return adapter.verifyObjectAndRoot(ctx, snapshot, entry, CommandPurposeRead, limits) }}
	return checked, ContentStat{Size: entry.Size, ModTime: entry.ModTime, SourceRevision: entry.SourceRevision}, nil
}

func (adapter *RcloneAdapter) validateBinding(binding AccessBinding) error {
	if adapter == nil || adapter.transport == nil || binding.Provider != backupasset.ProviderRclone || binding.TaskID == 0 || binding.NodeID == 0 || len(binding.IdentitySalt) != IdentitySaltBytes || len(binding.EndpointFacts) == 0 || strings.TrimSpace(binding.Locator) == "" {
		return fmt.Errorf("%w: invalid Rclone access binding", backupasset.ErrInvalidState)
	}
	switch rcloneConfigSource(binding) {
	case RcloneConfigBound:
		if len(binding.Secret) == 0 {
			return fmt.Errorf("%w: bound Rclone config is missing", backupasset.ErrInvalidState)
		}
	case RcloneConfigNodeDefault:
		if len(binding.Secret) != 0 {
			return fmt.Errorf("%w: node-default Rclone config cannot include bound config", backupasset.ErrInvalidState)
		}
	default:
		return fmt.Errorf("%w: invalid Rclone config source", backupasset.ErrInvalidState)
	}
	return nil
}

func (adapter *RcloneAdapter) runtimeAccess(binding AccessBinding) (RcloneRuntimeAccess, error) {
	if err := adapter.validateBinding(binding); err != nil {
		return RcloneRuntimeAccess{}, err
	}
	runtimeAccess, ok := binding.AdapterData.(RcloneRuntimeAccess)
	if !ok || !safeRcloneBackend.MatchString(runtimeAccess.Backend) {
		return RcloneRuntimeAccess{}, fmt.Errorf("%w: Rclone runtime facts unavailable", backupasset.ErrInvalidState)
	}
	runtimeAccess.ConfigSource = rcloneConfigSource(binding)
	if runtimeAccess.ManagedPoint != nil {
		managed := runtimeAccess.ManagedPoint
		if runtimeAccess.ConfigSource != RcloneConfigBound || !managed.Committed || backupasset.ValidateOpaqueID(managed.RecoveryPointID) != nil ||
			backupasset.ValidateOpaqueID(managed.AttemptID) != nil || !validTaggedDigest(managed.ManifestDigest) ||
			!validTaggedDigest(managed.SourceRevision) || managed.DataLocator != binding.Locator {
			return RcloneRuntimeAccess{}, fmt.Errorf("%w: managed Rclone point facts unavailable", backupasset.ErrInvalidState)
		}
		if _, err := NewRclonePrivateLocator(managed.DataLocator); err != nil {
			return RcloneRuntimeAccess{}, fmt.Errorf("%w: invalid managed Rclone data locator", backupasset.ErrInvalidState)
		}
	}
	return runtimeAccess, nil
}

func (adapter *RcloneAdapter) validateOperation(ctx context.Context, snapshot ReadSnapshot, point PointLocator, purpose CommandPurpose, limits OperationLimits) ([]Entry, error) {
	runtimeAccess, err := adapter.runtimeAccess(snapshot.Access)
	if err != nil {
		return nil, err
	}
	expectedPoint := rclonePointLocator(snapshot.SourceRevision)
	if runtimeAccess.ManagedPoint != nil {
		expectedPoint = rcloneManagedPointLocator(*runtimeAccess.ManagedPoint)
		if snapshot.SourceRevision != runtimeAccess.ManagedPoint.SourceRevision {
			return nil, fmt.Errorf("%w: managed Rclone source revision mismatch", backupasset.ErrInvalidState)
		}
	}
	if backupasset.ValidateOpaqueID(snapshot.RepositoryID) != nil || snapshot.RepositoryID != snapshot.Access.RepositoryID || snapshot.CapabilityRevision <= 0 || snapshot.SourceRevision == "" || point.Native != expectedPoint.Native {
		return nil, fmt.Errorf("%w: invalid Rclone read snapshot", backupasset.ErrInvalidState)
	}
	entries, err := adapter.loadList(ctx, snapshot.Access, "", limits, purpose)
	if err != nil {
		return nil, err
	}
	if rcloneListFingerprint(entries) != snapshot.SourceRevision {
		return nil, newCapabilityError(backupasset.CapabilityMutableSourceChanged)
	}
	return entries, nil
}

func (adapter *RcloneAdapter) loadList(ctx context.Context, binding AccessBinding, relative string, limits OperationLimits, purpose CommandPurpose) ([]Entry, error) {
	privateLocator := binding.Locator
	if relative != "" {
		privateLocator = rcloneObjectLocator(binding.Locator, relative)
	}
	invocation := adapter.invocation(binding, OperationRcloneList, []string{"lsjson", "--max-depth", "1", "--"}, privateLocator, purpose)
	output, err := adapter.run(ctx, invocation, limits)
	if err != nil {
		return nil, mapRcloneOperationError(ctx, err)
	}
	return parseRcloneList(output.Stdout, limits)
}

func (adapter *RcloneAdapter) stat(ctx context.Context, binding AccessBinding, relative string, limits OperationLimits, purpose CommandPurpose) (Entry, error) {
	invocation := adapter.invocation(binding, OperationRcloneStat, []string{"lsjson", "--stat", "--"}, rcloneObjectLocator(binding.Locator, relative), purpose)
	output, err := adapter.run(ctx, invocation, limits)
	if err != nil {
		return Entry{}, mapRcloneOperationError(ctx, err)
	}
	entry, err := parseRcloneStat(output.Stdout, limits)
	if err != nil {
		return Entry{}, err
	}
	entry.Locator = EntryLocator{Native: relative}
	entry.OpaqueDigest = stableDigest("rclone-entry", relative)
	return entry, nil
}

func (adapter *RcloneAdapter) verifyObjectAndRoot(ctx context.Context, snapshot ReadSnapshot, expected Entry, purpose CommandPurpose, limits OperationLimits) error {
	current, err := adapter.stat(ctx, snapshot.Access, expected.Locator.Native, limits, purpose)
	if err != nil || current.SourceRevision != expected.SourceRevision {
		return newCapabilityError(backupasset.CapabilityMutableSourceChanged)
	}
	root, err := adapter.loadList(ctx, snapshot.Access, "", limits, purpose)
	if err != nil || rcloneListFingerprint(root) != snapshot.SourceRevision {
		return newCapabilityError(backupasset.CapabilityMutableSourceChanged)
	}
	return nil
}

func (adapter *RcloneAdapter) operationLimits() (OperationLimits, error) {
	if adapter == nil {
		return OperationLimits{}, fmt.Errorf("%w: Rclone adapter unavailable", backupasset.ErrInvalidState)
	}
	return resolveOperationLimits(adapter.limitsSource)
}

func (adapter *RcloneAdapter) proveRange(ctx context.Context, binding AccessBinding, entries []Entry, limits OperationLimits, purpose CommandPurpose) bool {
	for _, entry := range entries {
		if entry.Type != backupasset.CatalogEntryFile || entry.Size <= 0 {
			continue
		}
		before, err := adapter.stat(ctx, binding, entry.Locator.Native, limits, purpose)
		if err != nil {
			return false
		}
		length := min(int64(16), entry.Size)
		sequential, err := adapter.run(ctx, adapter.invocation(binding, OperationRcloneCat, []string{"cat", "--count", strconv.FormatInt(length, 10), "--"}, rcloneObjectLocator(binding.Locator, entry.Locator.Native), purpose), limits)
		if err != nil || int64(len(sequential.Stdout)) != length {
			return false
		}
		ranged, err := adapter.run(ctx, adapter.invocation(binding, OperationRcloneCat, []string{"cat", "--offset", "0", "--count", strconv.FormatInt(length, 10), "--"}, rcloneObjectLocator(binding.Locator, entry.Locator.Native), purpose), limits)
		if err != nil || !bytes.Equal(sequential.Stdout, ranged.Stdout) {
			return false
		}
		after, err := adapter.stat(ctx, binding, entry.Locator.Native, limits, purpose)
		return err == nil && before.SourceRevision == after.SourceRevision
	}
	return false
}

func (adapter *RcloneAdapter) invocation(binding AccessBinding, operation CommandOperation, arguments []string, privateLocator string, purpose CommandPurpose) CommandInvocation {
	invocation := CommandInvocation{Tool: ToolRclone, Operation: operation, Purpose: purpose, Args: append([]string(nil), arguments...), PrivateLocator: privateLocator}
	if operation != OperationRcloneVersion && rcloneConfigSource(binding) == RcloneConfigBound {
		invocation.Args = append([]string{"--config", "/dev/stdin"}, invocation.Args...)
		invocation.SecretStdin = append([]byte(nil), binding.Secret...)
	}
	if runtimeAccess, ok := binding.AdapterData.(RcloneRuntimeAccess); ok {
		invocation.Runtime = runtimeAccess.Command
	}
	return invocation
}

func rcloneConfigSource(binding AccessBinding) RcloneConfigSource {
	if runtimeAccess, ok := binding.AdapterData.(RcloneRuntimeAccess); ok && runtimeAccess.ConfigSource != "" {
		return runtimeAccess.ConfigSource
	}
	if len(binding.Secret) > 0 {
		return RcloneConfigBound
	}
	return ""
}

func (adapter *RcloneAdapter) run(ctx context.Context, invocation CommandInvocation, limits OperationLimits) (CommandOutput, error) {
	if err := invocation.Validate(); err != nil {
		return CommandOutput{}, err
	}
	return adapter.transport.Run(ctx, invocation, limits)
}

func parseRcloneBackend(payload []byte) (string, error) {
	var facts struct {
		Name string `json:"Name"`
	}
	if err := decodeSingleJSON(payload, &facts); err != nil || !safeRcloneBackend.MatchString(strings.ToLower(facts.Name)) {
		return "", protocolCapabilityError()
	}
	return strings.ToLower(facts.Name), nil
}

func parseRcloneList(payload []byte, limits OperationLimits) ([]Entry, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('[') {
		return nil, protocolCapabilityError()
	}
	entries := make([]Entry, 0)
	seen := make(map[string]struct{})
	for decoder.More() {
		if len(entries) >= limits.MaxItems {
			return nil, newCapabilityError(backupasset.CapabilityProviderResourceLimit)
		}
		var row rcloneJSONEntry
		if err := decoder.Decode(&row); err != nil {
			return nil, protocolCapabilityError()
		}
		entry, err := row.entry()
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[entry.Locator.Native]; duplicate {
			return nil, protocolCapabilityError()
		}
		seen[entry.Locator.Native] = struct{}{}
		entries = append(entries, entry)
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim(']') {
		return nil, protocolCapabilityError()
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, protocolCapabilityError()
	}
	return entries, nil
}

func parseRcloneStat(payload []byte, limits OperationLimits) (Entry, error) {
	var row rcloneJSONEntry
	if int64(len(payload)) > limits.MaxMetadataBytes || decodeSingleJSON(payload, &row) != nil {
		return Entry{}, protocolCapabilityError()
	}
	return row.entry()
}

type rcloneJSONEntry struct {
	Path     string            `json:"Path"`
	Name     string            `json:"Name"`
	Size     int64             `json:"Size"`
	MimeType string            `json:"MimeType"`
	ModTime  time.Time         `json:"ModTime"`
	IsDir    bool              `json:"IsDir"`
	Hashes   map[string]string `json:"Hashes"`
}

func (row rcloneJSONEntry) entry() (Entry, error) {
	entryPath, err := normalizeRclonePath(row.Path, false)
	if err != nil || (!row.IsDir && row.Size < 0) {
		return Entry{}, protocolCapabilityError()
	}
	if row.IsDir && row.Size < 0 {
		row.Size = 0
	}
	name := path.Base(entryPath)
	if (row.Name != "" && row.Name != name) || strings.ContainsRune(name, '\x00') {
		return Entry{}, protocolCapabilityError()
	}
	entryType := backupasset.CatalogEntryFile
	if row.IsDir {
		entryType = backupasset.CatalogEntryDirectory
	}
	revisionParts := []string{entryPath, strconv.FormatInt(row.Size, 10), strconv.FormatInt(row.ModTime.UnixNano(), 10)}
	keys := make([]string, 0, len(row.Hashes))
	for key := range row.Hashes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		revisionParts = append(revisionParts, strings.ToLower(key)+"="+row.Hashes[key])
	}
	return Entry{OpaqueDigest: stableDigest("rclone-entry", entryPath), Name: name, Type: entryType, Size: row.Size, ModTime: row.ModTime.UTC(), SourceRevision: stableDigest("rclone-stat", strings.Join(revisionParts, "\x00")), Locator: EntryLocator{Native: entryPath}}, nil
}

func normalizeRclonePath(value string, allowRoot bool) (string, error) {
	if value == "" && allowRoot {
		return "", nil
	}
	if value == "" || strings.ContainsRune(value, '\x00') || strings.HasPrefix(value, "/") {
		return "", fmt.Errorf("%w: invalid Rclone object locator", backupasset.ErrInvalidState)
	}
	clean := path.Clean(value)
	if clean != value || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("%w: non-canonical Rclone object locator", backupasset.ErrInvalidState)
	}
	return clean, nil
}

func rcloneObjectLocator(root, relative string) string {
	colon := strings.Index(root, ":")
	if colon < 0 {
		return ""
	}
	prefix := root[:colon+1]
	base := strings.Trim(root[colon+1:], "/")
	joined := path.Join(base, relative)
	if joined == "." {
		joined = ""
	}
	return prefix + joined
}

func rebaseRcloneEntries(parent string, entries []Entry) []Entry {
	if parent == "" {
		return entries
	}
	result := make([]Entry, len(entries))
	for index, entry := range entries {
		fullPath := path.Join(parent, entry.Locator.Native)
		entry.Locator = EntryLocator{Native: fullPath}
		entry.OpaqueDigest = stableDigest("rclone-entry", fullPath)
		result[index] = entry
	}
	return result
}

func rcloneListFingerprint(entries []Entry) string {
	parts := make([]string, 0, len(entries))
	for _, entry := range entries {
		parts = append(parts, entry.Locator.Native+"="+entry.SourceRevision)
	}
	sort.Strings(parts)
	return stableDigest("rclone-list", strings.Join(parts, "\x00"))
}

func rclonePointLocator(sourceRevision string) PointLocator {
	return PointLocator{Native: "mutable:" + revisionDigest(sourceRevision)}
}

func rcloneManagedPointLocator(value RcloneManagedPointAccess) PointLocator {
	return PointLocator{Native: "managed:" + value.RecoveryPointID + ":" + value.AttemptID + ":" + value.ManifestDigest}
}

func mapRcloneOperationError(ctx context.Context, err error) error {
	return mapCommandTransportError(ctx, err)
}

type commandInvariantHandle struct {
	underlying ReadHandle
	verify     func() error
}

func (handle *commandInvariantHandle) Read(buffer []byte) (int, error) {
	return handle.underlying.Read(buffer)
}
func (handle *commandInvariantHandle) Close() error {
	closeErr := handle.underlying.Close()
	verifyErr := handle.verify()
	if closeErr != nil {
		return closeErr
	}
	return verifyErr
}

func (handle *commandInvariantHandle) ProviderBytes() int64 {
	if handle == nil {
		return -1
	}
	reporter, ok := handle.underlying.(ProviderByteReporter)
	if !ok {
		return -1
	}
	return reporter.ProviderBytes()
}

func (adapter *RcloneAdapter) pageEntries(ctx context.Context, snapshot ReadSnapshot, point PointLocator, parent string, items []Entry, request PageRequest) (EntryPage, error) {
	request, err := request.Normalize(adapter.maxPageSize)
	if err != nil {
		return EntryPage{}, err
	}
	listRevision := entryListRevision(snapshot.SourceRevision, items)
	start := 0
	if request.Cursor != "" {
		expected := CursorScope{Provider: backupasset.ProviderRclone, RepositoryID: snapshot.RepositoryID, PointScopeDigest: stableDigest("rclone-point-scope", point.Native), ParentScopeDigest: stableDigest("rclone-parent-scope", parent), CapabilityRevision: snapshot.CapabilityRevision, SourceRevision: listRevision, Direction: CursorForward}
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
		scope := CursorScope{Provider: backupasset.ProviderRclone, RepositoryID: snapshot.RepositoryID, PointScopeDigest: stableDigest("rclone-point-scope", point.Native), ParentScopeDigest: stableDigest("rclone-parent-scope", parent), CapabilityRevision: snapshot.CapabilityRevision, SourceRevision: listRevision, LastItemDigest: page.Items[len(page.Items)-1].OpaqueDigest, Direction: CursorForward}
		page.NextCursor, err = adapter.cursors.Encode(ctx, scope)
	}
	return page, err
}

var (
	_ RepositoryProber = (*RcloneAdapter)(nil)
	_ PointLister      = (*RcloneAdapter)(nil)
	_ EntryLister      = (*RcloneAdapter)(nil)
	_ EntryStatter     = (*RcloneAdapter)(nil)
	_ SequentialReader = (*RcloneAdapter)(nil)
	_ RangeReader      = (*RcloneAdapter)(nil)
)
