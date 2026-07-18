package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"path"
	"strings"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/sshutil"
)

const resticManifestTraversalProfile = "restic_depth_first_name_v1"

type manifestHeader struct {
	NativePointID string
	CapturedAt    time.Time
	Tags          []string
}

type manifestNode struct {
	Path       string
	Name       string
	NativeType string
	Size       *uint64
	Mode       *uint64
	UID        *uint64
	GID        *uint64
	ModTime    *time.Time
	AccessTime *time.Time
	ChangeTime *time.Time
	Inode      *uint64
}

type walkFrame struct {
	path      string
	lastChild string
}

type walkState struct {
	frames   []walkFrame
	maxDepth int
}

type manifestCanonicalizer struct {
	complete *backupasset.CanonicalSHA256
	partial  *backupasset.CanonicalSHA256
}

type resticManifestState struct {
	attempt ResticAttemptV1
	commit  ResticCommitV1
	limits  ManifestLimits

	walk   walkState
	header *manifestHeader
	hasher *manifestCanonicalizer

	entryCount uint64
	logical    uint64
	failure    backupasset.PublicationFailureCode
}

func (limits ManifestLimits) Validate() error {
	if limits.Timeout <= 0 || limits.MaxBytes <= 0 || limits.MaxEntries <= 0 || limits.MaxRecordBytes <= 0 || limits.MaxDepth <= 0 {
		return fmt.Errorf("%w: manifest limits must be positive", backupasset.ErrInvalidState)
	}
	if int64(limits.MaxRecordBytes) > limits.MaxBytes || limits.MaxRecordBytes > maxResticBackupRecordBuffer {
		return fmt.Errorf("%w: manifest record limit is invalid", backupasset.ErrInvalidState)
	}
	return nil
}

func (adapter *ResticAdapter) BuildManifest(ctx context.Context, attempt ResticAttemptV1, commit ResticCommitV1, limits ManifestLimits) (ResticManifestV1, error) {
	if err := limits.Validate(); err != nil {
		return ResticManifestV1{}, err
	}
	attempt, err := adapter.normalizePublicationAttempt(attempt)
	if err != nil {
		return ResticManifestV1{}, err
	}
	if err := validateManifestCommit(attempt, commit); err != nil {
		return ResticManifestV1{}, err
	}
	deadline := adapter.now().UTC().Add(limits.Timeout)
	if attempt.PointDeadlineAt.Before(deadline) {
		deadline = attempt.PointDeadlineAt
	}
	return adapter.buildManifestWithValidatedInput(ctx, attempt, commit, limits, attempt.Access, deadline)
}

// BuildCatalogManifest reruns the exact publication-compatible canonical
// codec against a committed snapshot without reviving its expired publication
// lease/deadline. It performs only probes and Restic `ls` reads.
func (adapter *ResticAdapter) BuildCatalogManifest(ctx context.Context, input ResticCatalogProofInput) (ResticManifestV1, error) {
	if err := input.Limits.Validate(); err != nil {
		return ResticManifestV1{}, err
	}
	attempt := input.Attempt
	if adapter == nil || adapter.transport == nil || adapter.streamTransport == nil || attempt.Provider != backupasset.ProviderRestic ||
		backupasset.ValidateOpaqueID(attempt.RepositoryID) != nil || backupasset.ValidateOpaqueID(attempt.TaskRepositoryLinkID) != nil ||
		backupasset.ValidateOpaqueID(attempt.RecoveryPointID) != nil || attempt.TaskID == 0 || attempt.TaskRunID == 0 ||
		attempt.CapabilityRevision <= 0 || attempt.AdapterRevision != resticAdapterRevision ||
		!strings.HasPrefix(attempt.RepositoryIdentity, NativeResticIdentityPrefix) ||
		!lowerHex(strings.TrimPrefix(attempt.RepositoryIdentity, NativeResticIdentityPrefix), 64) ||
		!validGeneratedResticTag(attempt.RequiredTags[0], 0) || !validGeneratedResticTag(attempt.RequiredTags[1], 1) {
		return ResticManifestV1{}, fmt.Errorf("%w: invalid Restic Catalog proof input", backupasset.ErrInvalidState)
	}
	if err := adapter.validateBinding(attempt.Access); err != nil || attempt.Access.RepositoryID != attempt.RepositoryID ||
		attempt.Access.TaskID != attempt.TaskID {
		return ResticManifestV1{}, fmt.Errorf("%w: invalid Restic Catalog access", backupasset.ErrInvalidState)
	}
	runtimeAccess, ok := attempt.Access.AdapterData.(ResticRuntimeAccess)
	if !ok || !lowerHex(runtimeAccess.NativeRepositoryID, 64) ||
		NativeResticIdentityPrefix+runtimeAccess.NativeRepositoryID != attempt.RepositoryIdentity || runtimeAccess.Command == nil {
		return ResticManifestV1{}, fmt.Errorf("%w: invalid Restic Catalog runtime", backupasset.ErrInvalidState)
	}
	if err := validateManifestCommit(attempt, input.Commit); err != nil {
		return ResticManifestV1{}, err
	}
	deadline := adapter.now().UTC().Add(input.Limits.Timeout)
	return adapter.buildManifestWithValidatedInput(ctx, attempt, input.Commit, input.Limits, attempt.Access, deadline)
}

func (adapter *ResticAdapter) buildManifestWithValidatedInput(
	ctx context.Context,
	attempt ResticAttemptV1,
	commit ResticCommitV1,
	limits ManifestLimits,
	binding AccessBinding,
	deadline time.Time,
) (ResticManifestV1, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	manifestContext, cancel := context.WithTimeout(ctx, limits.Timeout)
	defer cancel()
	operationLimits, err := adapter.operationLimits()
	if err != nil {
		return unavailableManifestEvidence(backupasset.FailureManifestUnavailable), nil
	}
	remaining := deadline.Sub(adapter.now().UTC()) - sshutil.CommandExecutionJoinTimeout
	if remaining <= 0 {
		return unavailableManifestEvidence(backupasset.FailurePublicationDeadlineExceeded), nil
	}
	operationLimits.Timeout = remaining
	if err := operationLimits.Validate(); err != nil {
		return unavailableManifestEvidence(backupasset.FailureManifestUnavailable), nil
	}
	if !adapter.manifestProbeMatches(manifestContext, binding, operationLimits, attempt) {
		return unavailableManifestEvidence(backupasset.FailureRepositoryIdentityDrift), nil
	}
	invocation := adapter.repositoryInvocation(binding, OperationResticManifest, []string{
		"--password-file", "/dev/stdin", "ls", "--json", "--recursive", "--", commit.NativePointID, "/",
	}, CommandPurposeManifest)
	if err := invocation.Validate(); err != nil {
		return ResticManifestV1{}, err
	}
	execution, err := adapter.streamTransport.OpenExecution(manifestContext, invocation, operationLimits, limits.MaxBytes)
	if err != nil || execution == nil {
		return unavailableManifestEvidence(backupasset.FailureManifestUnavailable), nil
	}
	state := newResticManifestState(attempt, commit, limits)
	readErr, hardLimit := state.read(execution)
	if hardLimit {
		_ = execution.Cancel()
	}
	completion, joinErr := execution.Join()
	if readErr != nil || hardLimit || joinErr != nil || !completion.ExitCodeKnown || completion.ExitCode != 0 || manifestContext.Err() != nil {
		if state.failure == "" {
			state.fail(manifestFailureForLifecycle(readErr, hardLimit, joinErr, manifestContext.Err()))
		}
		return state.evidence(), nil
	}
	if state.failure != "" {
		return state.evidence(), nil
	}
	if !adapter.manifestProbeMatches(manifestContext, binding, operationLimits, attempt) {
		state.fail(backupasset.FailureRepositoryIdentityDrift)
		return state.evidence(), nil
	}
	return state.completeEvidence(), nil
}

func validateManifestCommit(attempt ResticAttemptV1, commit ResticCommitV1) error {
	if commit.Provider != backupasset.ProviderRestic || commit.RepositoryIdentity != attempt.RepositoryIdentity || !lowerHex(commit.NativePointID, 64) ||
		commit.CaptureStartedAt.IsZero() || commit.CaptureFinishedAt.IsZero() || commit.CaptureFinishedAt.Before(commit.CaptureStartedAt) {
		return fmt.Errorf("%w: invalid Restic manifest commit evidence", backupasset.ErrInvalidState)
	}
	return nil
}

func (adapter *ResticAdapter) manifestProbeMatches(ctx context.Context, binding AccessBinding, limits OperationLimits, attempt ResticAttemptV1) bool {
	observation, err := adapter.Probe(ctx, binding, limits)
	return err == nil && observation.Provider == backupasset.ProviderRestic && observation.RepositoryIdentity == attempt.RepositoryIdentity &&
		observation.AdapterRevision == attempt.AdapterRevision && observation.VersionMode == backupasset.VersionNativeSnapshot
}

func manifestFailureForLifecycle(readErr error, hardLimit bool, joinErr error, contextErr error) backupasset.PublicationFailureCode {
	switch {
	case hardLimit || errors.Is(readErr, sshutil.ErrCommandOutputLimit):
		return backupasset.FailureProviderResourceLimit
	case errors.Is(contextErr, context.DeadlineExceeded), errors.Is(joinErr, sshutil.ErrCommandTimeout):
		return backupasset.FailureManifestUnavailable
	case errors.Is(contextErr, context.Canceled), errors.Is(joinErr, context.Canceled):
		return backupasset.FailureManifestUnavailable
	default:
		return backupasset.FailureManifestUnavailable
	}
}

func newResticManifestState(attempt ResticAttemptV1, commit ResticCommitV1, limits ManifestLimits) *resticManifestState {
	return &resticManifestState{
		attempt: attempt,
		commit:  commit,
		limits:  limits,
		walk:    walkState{frames: []walkFrame{{path: "/"}}, maxDepth: limits.MaxDepth},
	}
}

func (state *resticManifestState) read(execution CommandExecution) (error, bool) {
	reader := bufio.NewReaderSize(execution, state.limits.MaxRecordBytes+1)
	var total int64
	discarding := false
	for {
		fragment, err := reader.ReadSlice('\n')
		total += int64(len(fragment))
		if total > state.limits.MaxBytes {
			state.fail(backupasset.FailureProviderResourceLimit)
			return sshutil.ErrCommandOutputLimit, true
		}
		switch {
		case errors.Is(err, bufio.ErrBufferFull):
			state.fail(backupasset.FailureProviderResourceLimit)
			discarding = true
			continue
		case errors.Is(err, io.EOF):
			if len(fragment) > 0 || discarding {
				state.fail(backupasset.FailureEvidenceMalformedStream)
			}
			return nil, false
		case err != nil:
			return err, false
		}
		if discarding {
			discarding = false
			continue
		}
		if len(fragment) > 0 {
			state.record(fragment[:len(fragment)-1])
		}
	}
}

func (state *resticManifestState) record(line []byte) {
	if state.failure != "" {
		return
	}
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		state.fail(backupasset.FailureManifestPartial)
		return
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(line, &object); err != nil {
		state.fail(backupasset.FailureEvidenceMalformedStream)
		return
	}
	kind, err := resticManifestRecordKind(object)
	if err != nil {
		state.fail(backupasset.FailureManifestPartial)
		return
	}
	switch kind {
	case "snapshot":
		state.acceptHeader(object)
	case "node":
		state.acceptNode(object)
	default:
		state.fail(backupasset.FailureManifestPartial)
	}
}

func resticManifestRecordKind(object map[string]json.RawMessage) (string, error) {
	var values []string
	for _, key := range []string{"message_type", "struct_type"} {
		raw, exists := object[key]
		if !exists {
			continue
		}
		value, err := decodeJSONString(raw)
		if err != nil || value == "" {
			return "", errors.New("invalid Restic manifest record kind")
		}
		values = append(values, value)
	}
	if len(values) == 0 || (len(values) == 2 && values[0] != values[1]) {
		return "", errors.New("missing or conflicting Restic manifest record kind")
	}
	return values[0], nil
}

func (state *resticManifestState) acceptHeader(object map[string]json.RawMessage) {
	if state.header != nil {
		state.fail(backupasset.FailureManifestPartial)
		return
	}
	nativePointID, err := requiredJSONString(object, "id")
	if err != nil || !lowerHex(nativePointID, 64) || nativePointID != state.commit.NativePointID {
		state.fail(backupasset.FailureProviderSnapshotRewritten)
		return
	}
	timeRaw, err := requiredJSONString(object, "time")
	if err != nil {
		state.fail(backupasset.FailureProviderSnapshotRewritten)
		return
	}
	capturedAt, err := time.Parse(time.RFC3339Nano, timeRaw)
	if err != nil || capturedAt.IsZero() || !capturedAt.UTC().Equal(state.commit.CaptureStartedAt.UTC()) {
		state.fail(backupasset.FailureProviderSnapshotRewritten)
		return
	}
	var tags []string
	if rawTags, exists := object["tags"]; !exists || json.Unmarshal(rawTags, &tags) != nil || !matchesExactTags(tags, state.attempt.RequiredTags) {
		state.fail(backupasset.FailureProviderSnapshotRewritten)
		return
	}
	if rawOriginal, exists := object["original"]; exists && string(rawOriginal) != "null" {
		state.fail(backupasset.FailureProviderSnapshotRewritten)
		return
	}
	state.header = &manifestHeader{NativePointID: nativePointID, CapturedAt: capturedAt.UTC(), Tags: append([]string(nil), tags...)}
	state.hasher = newManifestCanonicalizer(nativePointID)
}

func matchesExactTags(actual []string, expected [2]string) bool {
	if len(actual) != 2 {
		return false
	}
	seen := map[string]int{expected[0]: 0, expected[1]: 0}
	for _, tag := range actual {
		if _, exists := seen[tag]; !exists {
			return false
		}
		seen[tag]++
	}
	return seen[expected[0]] == 1 && seen[expected[1]] == 1
}

func (state *resticManifestState) acceptNode(object map[string]json.RawMessage) {
	if state.header == nil || state.hasher == nil {
		state.fail(backupasset.FailureManifestPartial)
		return
	}
	if state.entryCount >= uint64(state.limits.MaxEntries) {
		state.fail(backupasset.FailureProviderResourceLimit)
		return
	}
	node, err := decodeManifestNode(object)
	if err != nil {
		state.fail(backupasset.FailureManifestPartial)
		return
	}
	if err := state.walk.accept(node); err != nil {
		if errors.Is(err, errManifestDepth) {
			state.fail(backupasset.FailureProviderResourceLimit)
		} else {
			state.fail(backupasset.FailureManifestPartial)
		}
		return
	}
	if err := state.hasher.writeNode(node); err != nil {
		state.fail(backupasset.FailureManifestPartial)
		return
	}
	if node.NativeType == "file" {
		if state.logical > math.MaxUint64-*node.Size {
			state.fail(backupasset.FailureProviderResourceLimit)
			return
		}
		state.logical += *node.Size
	}
	state.entryCount++
}

var (
	errManifestTraversal = errors.New("invalid Restic manifest traversal")
	errManifestDepth     = errors.New("restic manifest depth limit reached")
)

func (state *walkState) accept(node manifestNode) error {
	canonical, parent, err := validateManifestPathAndName(node.Path, node.Name)
	if err != nil {
		return err
	}
	for len(state.frames) > 1 && state.frames[len(state.frames)-1].path != parent {
		state.frames = state.frames[:len(state.frames)-1]
	}
	if len(state.frames) == 0 || state.frames[len(state.frames)-1].path != parent {
		return errManifestTraversal
	}
	frame := &state.frames[len(state.frames)-1]
	if frame.lastChild != "" && node.Name <= frame.lastChild {
		return errManifestTraversal
	}
	frame.lastChild = node.Name
	if node.NativeType == "dir" {
		if len(state.frames) > state.maxDepth {
			return errManifestDepth
		}
		state.frames = append(state.frames, walkFrame{path: canonical})
	}
	return nil
}

func validateManifestPathAndName(value, name string) (string, string, error) {
	if value == "" || name == "" || strings.ContainsRune(value, '\x00') || strings.ContainsRune(name, '\x00') || !strings.HasPrefix(value, "/") || value == "/" || path.Clean(value) != value || path.Base(value) != name {
		return "", "", errManifestTraversal
	}
	return value, path.Dir(value), nil
}

func decodeManifestNode(object map[string]json.RawMessage) (manifestNode, error) {
	node := manifestNode{}
	var err error
	if node.Path, err = requiredJSONString(object, "path"); err != nil {
		return manifestNode{}, err
	}
	if node.Name, err = requiredJSONString(object, "name"); err != nil {
		return manifestNode{}, err
	}
	if node.NativeType, err = requiredJSONString(object, "type"); err != nil || !validManifestNodeType(node.NativeType) {
		return manifestNode{}, errors.New("invalid Restic manifest node type")
	}
	if node.Size, err = optionalManifestUint(object, "size"); err != nil {
		return manifestNode{}, err
	}
	if node.Mode, err = optionalManifestUint(object, "mode"); err != nil {
		return manifestNode{}, err
	}
	if node.UID, err = optionalManifestUint(object, "uid"); err != nil {
		return manifestNode{}, err
	}
	if node.GID, err = optionalManifestUint(object, "gid"); err != nil {
		return manifestNode{}, err
	}
	if node.Inode, err = optionalManifestUint(object, "inode"); err != nil {
		return manifestNode{}, err
	}
	if node.ModTime, err = optionalManifestTime(object, "mtime"); err != nil {
		return manifestNode{}, err
	}
	if node.AccessTime, err = optionalManifestTime(object, "atime"); err != nil {
		return manifestNode{}, err
	}
	if node.ChangeTime, err = optionalManifestTime(object, "ctime"); err != nil {
		return manifestNode{}, err
	}
	if node.NativeType == "file" && node.Size == nil {
		return manifestNode{}, errors.New("missing Restic file size")
	}
	if node.NativeType != "file" && node.Size != nil {
		return manifestNode{}, errors.New("unexpected Restic non-file size")
	}
	return node, nil
}

func validManifestNodeType(value string) bool {
	switch value {
	case "file", "dir", "symlink", "dev", "chardev", "fifo", "socket", "irregular":
		return true
	default:
		return false
	}
}

func optionalManifestUint(object map[string]json.RawMessage, key string) (*uint64, error) {
	value, exists, err := optionalJSONUint(object, key)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, nil
	}
	return &value, nil
}

func optionalManifestTime(object map[string]json.RawMessage, key string) (*time.Time, error) {
	raw, exists := object[key]
	if !exists {
		return nil, nil
	}
	value, err := decodeJSONString(raw)
	if err != nil {
		return nil, err
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || parsed.IsZero() {
		return nil, errors.New("invalid Restic manifest time")
	}
	utc := parsed.UTC()
	return &utc, nil
}

func newManifestCanonicalizer(nativePointID string) *manifestCanonicalizer {
	complete := backupasset.NewCanonicalSHA256()
	partial := backupasset.NewCanonicalSHA256()
	writeManifestPrelude(complete, "xirang.restic.manifest.complete.v1", nativePointID)
	writeManifestPrelude(partial, "xirang.restic.manifest.partial.v1", nativePointID)
	return &manifestCanonicalizer{complete: complete, partial: partial}
}

func writeManifestPrelude(writer *backupasset.CanonicalSHA256, domain, nativePointID string) {
	writer.String(domain)
	writer.String("restic")
	writer.String(nativePointID)
	writer.Uint32(1)
	writer.String(resticManifestTraversalProfile)
}

func (canonicalizer *manifestCanonicalizer) writeNode(node manifestNode) error {
	for _, writer := range []*backupasset.CanonicalSHA256{canonicalizer.complete, canonicalizer.partial} {
		writer.String(node.Path)
		writer.String(node.NativeType)
		bitmap := manifestNodeBitmap(node)
		writer.Uint8(bitmap)
		if node.Size != nil {
			writer.Uint64(*node.Size)
		}
		if node.Mode != nil {
			writer.Uint64(*node.Mode)
		}
		if node.UID != nil {
			writer.Uint64(*node.UID)
		}
		if node.GID != nil {
			writer.Uint64(*node.GID)
		}
		if node.ModTime != nil {
			writer.Int64(node.ModTime.UTC().UnixNano())
		}
		if node.AccessTime != nil {
			writer.Int64(node.AccessTime.UTC().UnixNano())
		}
		if node.ChangeTime != nil {
			writer.Int64(node.ChangeTime.UTC().UnixNano())
		}
		if node.Inode != nil {
			writer.Uint64(*node.Inode)
		}
	}
	return nil
}

func manifestNodeBitmap(node manifestNode) uint8 {
	var bitmap uint8
	if node.Size != nil {
		bitmap |= 1 << 0
	}
	if node.Mode != nil {
		bitmap |= 1 << 1
	}
	if node.UID != nil {
		bitmap |= 1 << 2
	}
	if node.GID != nil {
		bitmap |= 1 << 3
	}
	if node.ModTime != nil {
		bitmap |= 1 << 4
	}
	if node.AccessTime != nil {
		bitmap |= 1 << 5
	}
	if node.ChangeTime != nil {
		bitmap |= 1 << 6
	}
	if node.Inode != nil {
		bitmap |= 1 << 7
	}
	return bitmap
}

func (state *resticManifestState) completeEvidence() ResticManifestV1 {
	if state.header == nil || state.hasher == nil || state.entryCount > math.MaxInt64 || state.logical > math.MaxInt64 {
		state.fail(backupasset.FailureProviderResourceLimit)
		return state.evidence()
	}
	state.hasher.complete.Uint64(state.entryCount)
	state.hasher.complete.Uint64(state.logical)
	digest, err := state.hasher.complete.HexDigest()
	if err != nil {
		return unavailableManifestEvidence(backupasset.FailureManifestUnavailable)
	}
	return ResticManifestV1{
		DigestAlgorithm: "sha256", Digest: digest, Generator: "xirang-restic-ls", GeneratorVersion: "1",
		Completeness: backupasset.ManifestComplete, EntryCount: int64(state.entryCount), LogicalBytes: int64(state.logical),
		Fidelity: ResticManifestFidelityV1(), HeaderCapturedAt: state.header.CapturedAt.UTC(), ObservedTagDigest: digestExactTags(state.header.Tags),
	}
}

func (state *resticManifestState) evidence() ResticManifestV1 {
	if state.failure == "" {
		state.failure = backupasset.FailureManifestUnavailable
	}
	if state.header == nil || state.hasher == nil || state.entryCount > math.MaxInt64 || state.logical > math.MaxInt64 {
		return unavailableManifestEvidence(state.failure)
	}
	state.hasher.partial.String("partial_terminator")
	state.hasher.partial.String(string(state.failure))
	state.hasher.partial.Uint64(state.entryCount)
	state.hasher.partial.Uint64(state.logical)
	digest, err := state.hasher.partial.HexDigest()
	if err != nil {
		return unavailableManifestEvidence(backupasset.FailureManifestUnavailable)
	}
	return ResticManifestV1{
		DigestAlgorithm: "sha256", Digest: digest, Generator: "xirang-restic-ls", GeneratorVersion: "1",
		Completeness: backupasset.ManifestPartial, EntryCount: int64(state.entryCount), LogicalBytes: int64(state.logical),
		Fidelity: ResticManifestFidelityV1(), HeaderCapturedAt: state.header.CapturedAt.UTC(), ObservedTagDigest: digestExactTags(state.header.Tags), FailureCode: state.failure,
	}
}

func unavailableManifestEvidence(code backupasset.PublicationFailureCode) ResticManifestV1 {
	return ResticManifestV1{DigestAlgorithm: "sha256", Generator: "xirang-restic-ls", GeneratorVersion: "1", Completeness: backupasset.ManifestUnavailable, Fidelity: ResticManifestFidelityV1(), FailureCode: code}
}

func (state *resticManifestState) fail(code backupasset.PublicationFailureCode) {
	if state.failure == "" {
		state.failure = code
	}
}

func digestExactTags(tags []string) string {
	writer := backupasset.NewCanonicalSHA256()
	writer.String("xirang.restic.tags.v1")
	for _, tag := range tags {
		writer.String(tag)
	}
	digest, err := writer.HexDigest()
	if err != nil {
		return ""
	}
	return digest
}

var _ ManifestBuilder = (*ResticAdapter)(nil)
