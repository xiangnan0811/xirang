package provider

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"xirang/backend/internal/backupasset"
)

type RclonePrefixDeletionAccess struct {
	Prefix               RclonePrivateLocator `json:"-"`
	MarkerDigest         string               `json:"-"`
	ExpectedBackend      string               `json:"-"`
	ExpectedRootIdentity string               `json:"-"`
	ConfigDigest         string               `json:"-"`
	MarkerKey            []byte               `json:"-"`
	Attempt              RcloneAttemptV1      `json:"-"`
	Commit               RcloneCommitV1       `json:"-"`
	ExpectedAttemptRoot  string               `json:"-"`
	Command              *RemoteCommandAccess `json:"-"`
}

type RcloneNativeExactVersion struct {
	PhysicalKey string `json:"-"`
	VersionID   string `json:"-"`
}

type RcloneNativeDeletionAccess struct {
	Versions        []RcloneNativeExactVersion      `json:"-"`
	AuthorityDigest string                          `json:"-"`
	Client          RcloneNativeExactVersionDeleter `json:"-"`
}

type RcloneNativeVersionProbe struct {
	Present bool
	Locked  bool
}

type RcloneNativeExactVersionDeleter interface {
	ProbeExactVersion(context.Context, RcloneNativeExactVersion) (RcloneNativeVersionProbe, error)
	DeleteExactVersion(context.Context, RcloneNativeExactVersion) error
}

type RclonePrefixPointDeleter struct {
	transport    CommandTransport
	limitsSource OperationLimitsSource
	now          func() time.Time
}

func NewRclonePrefixPointDeleter(
	transport CommandTransport,
	limitsSource OperationLimitsSource,
	now func() time.Time,
) (*RclonePrefixPointDeleter, error) {
	if transport == nil {
		return nil, fmt.Errorf("%w: invalid Rclone prefix deleter dependencies", backupasset.ErrInvalidState)
	}
	if _, err := resolveOperationLimits(limitsSource); err != nil {
		return nil, fmt.Errorf("%w: invalid Rclone prefix deleter limits", backupasset.ErrInvalidState)
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &RclonePrefixPointDeleter{transport: transport, limitsSource: limitsSource, now: now}, nil
}

func (deleter *RclonePrefixPointDeleter) ProviderKind() backupasset.ProviderKind {
	return backupasset.ProviderRclone
}

func (deleter *RclonePrefixPointDeleter) DeletePoint(ctx context.Context, request DeletePointRequest) (DeletePointResult, error) {
	if err := request.requireSourceRevision(); err != nil {
		return DeletePointResult{}, err
	}
	access, err := rclonePrefixDeletionAccess(request)
	if err != nil {
		return DeletePointResult{}, err
	}
	if request.ExpectedSourceRevision != access.MarkerDigest {
		return DeletePointResult{}, ErrDeletePointIdentityConflict
	}
	limits, err := resolveOperationLimits(deleter.limitsSource)
	if err != nil {
		return DeletePointResult{}, err
	}
	if err := deleter.verifyLiveRepositoryIdentity(ctx, request.Snapshot.Access, access, limits); err != nil {
		return DeletePointResult{}, err
	}
	present, err := deleter.prefixPresent(ctx, request.Snapshot.Access, access.Prefix, limits)
	if err != nil {
		return DeletePointResult{}, err
	}
	if present {
		if err := deleter.verifyLivePrefixMarker(ctx, request.Snapshot.Access, access, limits); err != nil {
			return DeletePointResult{}, err
		}
	}
	if !present {
		return rcloneDeletionReceipt(DeletePointAlreadyAbsent, request, access.MarkerDigest)
	}
	if err := deleter.purgePrefix(ctx, request.Snapshot.Access, access.Prefix, limits); err != nil {
		return DeletePointResult{}, err
	}
	present, err = deleter.prefixPresent(ctx, request.Snapshot.Access, access.Prefix, limits)
	if err != nil {
		return DeletePointResult{}, err
	}
	if present {
		return DeletePointResult{}, fmt.Errorf("%w: exact Rclone prefix remained after delete", backupasset.ErrInvalidState)
	}
	return rcloneDeletionReceipt(DeletePointDeleted, request, access.MarkerDigest)
}

func (deleter *RclonePrefixPointDeleter) prefixPresent(ctx context.Context, binding AccessBinding, prefix RclonePrivateLocator, limits OperationLimits) (bool, error) {
	invocation, err := rcloneDeletionInvocation(binding, OperationRcloneManagedExactStat, &prefix, limits, deleter.now().UTC())
	if err != nil {
		return false, err
	}
	stream, ok := deleter.transport.(CommandStreamTransport)
	if !ok || stream == nil {
		return false, ErrRcloneAttemptPresenceUnknown
	}
	execution, err := stream.OpenExecution(ctx, invocation, limits, limits.MaxMetadataBytes)
	if err != nil {
		return false, mapCommandTransportError(ctx, err)
	}
	if _, err := io.Copy(io.Discard, execution); err != nil {
		_ = execution.Cancel()
		_, _ = execution.Join()
		return false, mapCommandTransportError(ctx, err)
	}
	completion, err := execution.Join()
	if err != nil {
		return false, mapCommandTransportError(ctx, err)
	}
	if !completion.ExitCodeKnown {
		return false, ErrRcloneAttemptPresenceUnknown
	}
	switch completion.ExitCode {
	case 0:
		return true, nil
	case 3:
		return false, nil
	default:
		return false, ErrRcloneAttemptPresenceUnknown
	}
}

func (deleter *RclonePrefixPointDeleter) verifyLiveRepositoryIdentity(
	ctx context.Context,
	binding AccessBinding,
	access RclonePrefixDeletionAccess,
	limits OperationLimits,
) error {
	if strings.TrimSpace(access.ExpectedBackend) == "" || !validTaggedDigest(access.ExpectedRootIdentity) ||
		!validTaggedDigest(access.ConfigDigest) {
		return ErrDeletePointIdentityConflict
	}
	root, err := rcloneManagedRootFromPointPrefix(access.Prefix)
	if err != nil {
		return ErrDeletePointIdentityConflict
	}
	invocation, err := rcloneDeletionInvocation(binding, OperationRcloneManagedFeatures, &root, limits, deleter.now().UTC())
	if err != nil {
		return err
	}
	output, err := deleter.transport.Run(ctx, invocation, limits)
	if err != nil {
		return mapCommandTransportError(ctx, err)
	}
	backend, err := parseRcloneBackend(output.Stdout)
	if err != nil || backend != strings.ToLower(strings.TrimSpace(access.ExpectedBackend)) {
		return ErrDeletePointIdentityConflict
	}
	if rclonePortableRootIdentity(access.ConfigDigest, root) != access.ExpectedRootIdentity {
		return ErrDeletePointIdentityConflict
	}
	return nil
}

func rclonePortableRootIdentity(configDigest string, root RclonePrivateLocator) string {
	if !validTaggedDigest(configDigest) || !root.valid() {
		return ""
	}
	return rclonePreflightDigest("portable-root-v1", []byte(configDigest+"\n"+root.value))
}

func rcloneManagedRootFromPointPrefix(prefix RclonePrivateLocator) (RclonePrivateLocator, error) {
	const marker = "/points/"
	index := strings.LastIndex(prefix.value, marker)
	if index <= 0 {
		return RclonePrivateLocator{}, ErrDeletePointIdentityConflict
	}
	root, err := NewRclonePrivateLocator(prefix.value[:index])
	if err != nil {
		return RclonePrivateLocator{}, ErrDeletePointIdentityConflict
	}
	return root, nil
}

func (deleter *RclonePrefixPointDeleter) verifyLivePrefixMarker(
	ctx context.Context,
	binding AccessBinding,
	access RclonePrefixDeletionAccess,
	limits OperationLimits,
) error {
	controlRoot, err := joinRclonePrivateLocator(access.Prefix, "control")
	if err != nil {
		return ErrDeletePointIdentityConflict
	}
	attemptMarker, err := joinRclonePrivateLocator(controlRoot, "attempt.json")
	if err != nil {
		return ErrDeletePointIdentityConflict
	}
	commitMarker, err := joinRclonePrivateLocator(controlRoot, "commit.json")
	if err != nil {
		return ErrDeletePointIdentityConflict
	}
	readControl := func(locator RclonePrivateLocator) ([]byte, error) {
		invocation, invocationErr := rcloneDeletionInvocation(binding, OperationRcloneManagedCat, &locator, limits, deleter.now().UTC())
		if invocationErr != nil {
			return nil, invocationErr
		}
		output, runErr := deleter.transport.Run(ctx, invocation, limits)
		if runErr != nil {
			return nil, mapCommandTransportError(ctx, runErr)
		}
		return output.Stdout, nil
	}

	attemptPayload, err := readControl(attemptMarker)
	if err != nil {
		return err
	}
	attempt, attemptIdentity, err := decodeRcloneDeletionAttemptMarker(attemptPayload, access.MarkerKey)
	if err != nil {
		return ErrDeletePointIdentityConflict
	}
	if !rcloneAttemptDeletionAuthorityMatches(attempt, access.Attempt) ||
		access.Commit.Portable == nil || attempt.Portable == nil ||
		access.Commit.Portable.AttemptIdentityDigest != attemptIdentity ||
		attempt.RepositoryID != access.Commit.RepositoryID ||
		attempt.TaskRepositoryLinkID != access.Commit.TaskRepositoryLinkID ||
		attempt.RecoveryPointID != access.Commit.RecoveryPointID ||
		attempt.AttemptID != access.Commit.AttemptID ||
		attempt.PublicationMode != access.Commit.PublicationMode ||
		!attempt.PointDeadlineAt.Equal(access.Commit.PointDeadlineAt.UTC()) ||
		attempt.ChildFenceDigest != access.Commit.ChildFenceDigest {
		return ErrDeletePointIdentityConflict
	}

	commitPayload, err := readControl(commitMarker)
	if err != nil {
		return err
	}
	commit, commitDigest, commitAuthentication, err := decodeRcloneDeletionCommitMarker(commitPayload, access.MarkerKey)
	if err != nil {
		return ErrDeletePointIdentityConflict
	}
	if !rcloneCommitDeletionAuthorityMatches(commit, access.Commit) ||
		commit.Portable == nil ||
		commit.Portable.AttemptIdentityDigest != attemptIdentity ||
		commit.Portable.AttemptMarkerDigest != attempt.Portable.AttemptMarkerDigest ||
		commit.Portable.CommitPayloadDigest != commitDigest ||
		commit.Portable.CommitAuthenticationDigest != commitAuthentication ||
		commit.Portable.ControlIdentityDigest != keyedPrivateLocatorDigest(access.MarkerKey, controlRoot.value) ||
		commit.Portable.DataIdentityDigest != keyedPrivateLocatorDigest(access.MarkerKey, strings.TrimSuffix(access.Prefix.value, "/")+"/data") {
		return ErrDeletePointIdentityConflict
	}
	if rclonePortableSourceIdentity(access.MarkerKey, commit.RepositoryID, access.Prefix.value,
		commit.Portable.CommitComponent, commit.Portable.CommitPayloadDigest) != access.MarkerDigest {
		return ErrDeletePointIdentityConflict
	}
	return nil
}

func decodeRcloneDeletionAttemptMarker(payload, key []byte) (RcloneAttemptV1, string, error) {
	document, _, _, err := decodeRcloneDeletionControlEnvelope(payload, key, "attempt")
	if err != nil {
		return RcloneAttemptV1{}, "", err
	}
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	var wire rcloneAttemptWireV1
	if err := decoder.Decode(&wire); err != nil {
		return RcloneAttemptV1{}, "", err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return RcloneAttemptV1{}, "", fmt.Errorf("trailing Rclone attempt control")
	}
	attempt := rcloneAttemptFromWire(&wire)
	if attempt == nil || attempt.Validate() != nil {
		return RcloneAttemptV1{}, "", fmt.Errorf("invalid Rclone attempt control")
	}
	return *attempt, sha256Hex(payload), nil
}

func decodeRcloneDeletionCommitMarker(payload, key []byte) (RcloneCommitV1, string, string, error) {
	document, identity, authentication, err := decodeRcloneDeletionControlEnvelope(payload, key, "commit")
	if err != nil {
		return RcloneCommitV1{}, "", "", err
	}
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	var wire rcloneCommitWireV1
	if err := decoder.Decode(&wire); err != nil {
		return RcloneCommitV1{}, "", "", err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return RcloneCommitV1{}, "", "", fmt.Errorf("trailing Rclone commit control")
	}
	commit := rcloneCommitFromWire(&wire)
	if commit == nil || commit.Portable == nil {
		return RcloneCommitV1{}, "", "", fmt.Errorf("portable Rclone commit control required")
	}
	commit.Portable.CommitPayloadDigest = identity
	commit.Portable.CommitAuthenticationDigest = authentication
	if commit.Validate() != nil {
		return RcloneCommitV1{}, "", "", fmt.Errorf("invalid Rclone commit control")
	}
	return *commit, identity, authentication, nil
}

func decodeRcloneDeletionControlEnvelope(payload, key []byte, kind string) (json.RawMessage, string, string, error) {
	if len(payload) == 0 || len(payload) > 8<<20 || len(key) < 32 ||
		(kind != "attempt" && kind != "commit") || rejectDuplicateJSONMembers(string(payload)) != nil {
		return nil, "", "", fmt.Errorf("invalid Rclone control envelope")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var envelope rcloneAuthenticatedControlV1
	if err := decoder.Decode(&envelope); err != nil || envelope.Version != 1 ||
		envelope.Kind != kind || !lowerHex(envelope.Authentication, 64) {
		return nil, "", "", fmt.Errorf("invalid Rclone control envelope")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, "", "", fmt.Errorf("trailing Rclone control envelope")
	}
	_, identity, authentication, err := encodeRcloneAuthenticatedControl(kind, envelope.Document, key)
	if err != nil || !hmac.Equal([]byte(authentication), []byte(envelope.Authentication)) ||
		len(envelope.Document) == 0 || rejectDuplicateJSONMembers(string(envelope.Document)) != nil {
		return nil, "", "", fmt.Errorf("rclone control authentication mismatch")
	}
	return envelope.Document, identity, authentication, nil
}

func rcloneAttemptDeletionAuthorityMatches(got, want RcloneAttemptV1) bool {
	if got.SchemaVersion != want.SchemaVersion || got.LayoutVersion != want.LayoutVersion ||
		got.MinimumRuntimeRevision != want.MinimumRuntimeRevision || got.Provider != want.Provider ||
		got.RepositoryID != want.RepositoryID || got.TaskRepositoryLinkID != want.TaskRepositoryLinkID ||
		got.RecoveryPointID != want.RecoveryPointID || got.AttemptID != want.AttemptID ||
		got.TaskID != want.TaskID || got.TaskRunID != want.TaskRunID || got.Trigger != want.Trigger ||
		got.PublicationMode != want.PublicationMode || got.ImportedBaseline != want.ImportedBaseline ||
		!got.CaptureStartedAt.Equal(want.CaptureStartedAt) || !got.PreparedAt.Equal(want.PreparedAt) ||
		!got.PointDeadlineAt.Equal(want.PointDeadlineAt) || got.ExpectedTaskRevision != want.ExpectedTaskRevision ||
		got.BindingRevision != want.BindingRevision || got.ConfigRevision != want.ConfigRevision ||
		got.ConfigDigest != want.ConfigDigest || got.CapabilityRevision != want.CapabilityRevision ||
		got.CredentialRevision != want.CredentialRevision || got.PreflightID != want.PreflightID ||
		got.PreflightRevision != want.PreflightRevision || got.PreflightDigest != want.PreflightDigest ||
		got.ManifestSchemaRevision != want.ManifestSchemaRevision ||
		got.ManifestLimitsRevision != want.ManifestLimitsRevision ||
		got.ManifestLimitsDigest != want.ManifestLimitsDigest ||
		got.RepositoryIdentityDigest != want.RepositoryIdentityDigest ||
		got.ManagedRootIdentityDigest != want.ManagedRootIdentityDigest ||
		got.ChildFenceDigest != want.ChildFenceDigest ||
		got.LegacyOriginEvidenceDigest != want.LegacyOriginEvidenceDigest {
		return false
	}
	if !rclonePortableAttemptDeletionAuthorityMatches(got.Portable, want.Portable) {
		return false
	}
	return got.Native == nil && want.Native == nil
}

func rclonePortableAttemptDeletionAuthorityMatches(got, want *RclonePortableAttemptV1) bool {
	if got == nil || want == nil {
		return got == nil && want == nil
	}
	if got.AttemptComponent != want.AttemptComponent || got.DataComponent != want.DataComponent ||
		got.ControlComponent != want.ControlComponent || got.AttemptMarkerDigest != want.AttemptMarkerDigest ||
		got.ExpectedConsistencyClass != want.ExpectedConsistencyClass || got.ExpectedHashFidelity != want.ExpectedHashFidelity {
		return false
	}
	if got.CopyDest == nil || want.CopyDest == nil {
		return got.CopyDest == nil && want.CopyDest == nil
	}
	return *got.CopyDest == *want.CopyDest
}

func rcloneCommitDeletionAuthorityMatches(got, want RcloneCommitV1) bool {
	if got.SchemaVersion != want.SchemaVersion || got.LayoutVersion != want.LayoutVersion ||
		got.MinimumRuntimeRevision != want.MinimumRuntimeRevision || got.RepositoryID != want.RepositoryID ||
		got.TaskRepositoryLinkID != want.TaskRepositoryLinkID || got.RecoveryPointID != want.RecoveryPointID ||
		got.AttemptID != want.AttemptID || got.PublicationMode != want.PublicationMode ||
		!got.PointDeadlineAt.Equal(want.PointDeadlineAt) || !got.ProviderCommittedAt.Equal(want.ProviderCommittedAt) ||
		got.ManifestIndexDigest != want.ManifestIndexDigest || got.ManifestEntryCount != want.ManifestEntryCount ||
		got.LogicalBytes != want.LogicalBytes || got.SourceObservationDigest != want.SourceObservationDigest ||
		got.DestinationObservationDigest != want.DestinationObservationDigest ||
		got.ContentProofDigest != want.ContentProofDigest || got.FidelityEvidenceDigest != want.FidelityEvidenceDigest ||
		got.CostEvidenceDigest != want.CostEvidenceDigest || got.CapabilityEvidenceDigest != want.CapabilityEvidenceDigest ||
		got.ChildFenceDigest != want.ChildFenceDigest ||
		len(got.ManifestChunkDigests) != len(want.ManifestChunkDigests) {
		return false
	}
	for index := range got.ManifestChunkDigests {
		if got.ManifestChunkDigests[index] != want.ManifestChunkDigests[index] {
			return false
		}
	}
	if !rclonePortableCommitDeletionAuthorityMatches(got.Portable, want.Portable) {
		return false
	}
	return got.Native == nil && want.Native == nil
}

func rclonePortableCommitDeletionAuthorityMatches(got, want *RclonePortableCommitV1) bool {
	if got == nil || want == nil {
		return got == nil && want == nil
	}
	return got.AttemptIdentityDigest == want.AttemptIdentityDigest &&
		got.ControlIdentityDigest == want.ControlIdentityDigest &&
		got.DataIdentityDigest == want.DataIdentityDigest &&
		got.AttemptMarkerDigest == want.AttemptMarkerDigest &&
		got.ParentRecoveryPointID == want.ParentRecoveryPointID &&
		got.ParentCommitDigest == want.ParentCommitDigest &&
		got.ParentManifestDigest == want.ParentManifestDigest &&
		got.CommitComponent == want.CommitComponent &&
		got.CommitPayloadDigest == want.CommitPayloadDigest &&
		got.CommitAuthenticationDigest == want.CommitAuthenticationDigest &&
		got.ConsistencyEvidenceDigest == want.ConsistencyEvidenceDigest &&
		got.HashEvidenceDigest == want.HashEvidenceDigest &&
		got.DownloadVerifiedBytes == want.DownloadVerifiedBytes
}

func rclonePortableSourceIdentity(key []byte, repositoryID, attemptRoot, commitComponent, commitPayloadDigest string) string {
	if len(key) < 32 || backupasset.ValidateOpaqueID(repositoryID) != nil ||
		!validTaggedDigest(commitPayloadDigest) || commitComponent != "commit.json" {
		return ""
	}
	mac := hmac.New(sha256.New, key)
	_, _ = io.WriteString(mac, "xirang.rclone.portable-point-identity.v1")
	for _, value := range []string{repositoryID, attemptRoot, commitComponent, commitPayloadDigest} {
		_, _ = mac.Write([]byte{0})
		_, _ = io.WriteString(mac, value)
	}
	return hex.EncodeToString(mac.Sum(nil))
}

func (deleter *RclonePrefixPointDeleter) purgePrefix(ctx context.Context, binding AccessBinding, prefix RclonePrivateLocator, limits OperationLimits) error {
	invocation, err := rcloneDeletionInvocation(binding, OperationRcloneManagedDeleteExactPrefix, &prefix, limits, deleter.now().UTC())
	if err != nil {
		return err
	}
	if _, err := deleter.transport.Run(ctx, invocation, limits); err != nil {
		return mapCommandTransportError(ctx, err)
	}
	return nil
}

func rclonePrefixDeletionAccess(request DeletePointRequest) (RclonePrefixDeletionAccess, error) {
	if request.Snapshot.Access.Provider != backupasset.ProviderRclone {
		return RclonePrefixDeletionAccess{}, invalidDeletePointRequest("invalid Rclone prefix deletion provider")
	}
	access, ok := request.Snapshot.Access.AdapterData.(RclonePrefixDeletionAccess)
	if !ok || !access.Prefix.valid() || !validTaggedDigest(access.MarkerDigest) ||
		!validTaggedDigest(access.ExpectedRootIdentity) || !validTaggedDigest(access.ConfigDigest) ||
		len(access.MarkerKey) < 32 || access.Attempt.Portable == nil || access.Commit.Portable == nil ||
		access.ExpectedAttemptRoot == "" || request.Point.Native != access.Prefix.value ||
		access.Command == nil || access.Command.Node.ID == 0 {
		return RclonePrefixDeletionAccess{}, invalidDeletePointRequest("invalid Rclone prefix deletion access")
	}
	if access.Attempt.Validate() != nil || access.Commit.Validate() != nil {
		return RclonePrefixDeletionAccess{}, ErrDeletePointIdentityConflict
	}
	if access.Attempt.PublicationMode != backupasset.PublicationVersionedPrefix ||
		access.Commit.PublicationMode != backupasset.PublicationVersionedPrefix {
		return RclonePrefixDeletionAccess{}, ErrDeletePointIdentityConflict
	}
	if !validCommittedRclonePointPrefix(access.Prefix.value) {
		return RclonePrefixDeletionAccess{}, invalidDeletePointRequest("exact committed Rclone prefix required")
	}
	if access.ExpectedAttemptRoot != access.Prefix.value ||
		access.Attempt.RepositoryID != request.Snapshot.RepositoryID ||
		access.Commit.RepositoryID != request.Snapshot.RepositoryID ||
		access.Attempt.TaskRepositoryLinkID != access.Commit.TaskRepositoryLinkID ||
		access.Attempt.RecoveryPointID != access.Commit.RecoveryPointID ||
		access.Attempt.AttemptID != access.Commit.AttemptID ||
		access.Attempt.Portable.AttemptComponent != path.Base(access.Prefix.value[strings.IndexByte(access.Prefix.value, ':')+1:]) {
		return RclonePrefixDeletionAccess{}, ErrDeletePointIdentityConflict
	}
	controlRoot, err := joinRclonePrivateLocator(access.Prefix, "control")
	if err != nil {
		return RclonePrefixDeletionAccess{}, ErrDeletePointIdentityConflict
	}
	dataRoot, err := joinRclonePrivateLocator(access.Prefix, "data")
	if err != nil {
		return RclonePrefixDeletionAccess{}, ErrDeletePointIdentityConflict
	}
	if access.ConfigDigest != access.Attempt.ConfigDigest ||
		access.ExpectedRootIdentity != access.Attempt.ManagedRootIdentityDigest ||
		!access.Attempt.PointDeadlineAt.Equal(access.Commit.PointDeadlineAt) ||
		access.Attempt.ChildFenceDigest != access.Commit.ChildFenceDigest ||
		access.Commit.Portable.AttemptMarkerDigest != access.Attempt.Portable.AttemptMarkerDigest ||
		access.Commit.Portable.ControlIdentityDigest != keyedPrivateLocatorDigest(access.MarkerKey, controlRoot.value) ||
		access.Commit.Portable.DataIdentityDigest != keyedPrivateLocatorDigest(access.MarkerKey, dataRoot.value) {
		return RclonePrefixDeletionAccess{}, ErrDeletePointIdentityConflict
	}
	return access, nil
}

func validCommittedRclonePointPrefix(value string) bool {
	if strings.Contains(value, "..") {
		return false
	}
	_, remotePath, ok := strings.Cut(value, ":")
	if !ok || remotePath == "" || strings.Contains(remotePath, "..") {
		return false
	}
	cleanedRemote := path.Clean(remotePath)
	if cleanedRemote == "." || cleanedRemote != remotePath || strings.HasPrefix(cleanedRemote, "..") || strings.Contains(cleanedRemote, "..") {
		return false
	}
	const marker = "/points/"
	index := strings.LastIndex("/"+cleanedRemote, marker)
	if index < 0 {
		return false
	}
	rest := ("/" + cleanedRemote)[index+len(marker):]
	if len(rest) < 65 || !lowerHex(rest[:32], 32) || rest[32] != '.' || !lowerHex(rest[33:65], 32) {
		return false
	}
	consumed := 65
	remainder := rest[consumed:]
	if remainder != "" && !strings.HasPrefix(remainder, "/") {
		return false
	}
	pointRoot := path.Clean(marker + rest[:consumed])
	cleaned := path.Clean(pointRoot + remainder)
	return cleaned == pointRoot
}

func RcloneNativeExactVersionDeleterFromS3(s3 S3Native) (RcloneNativeExactVersionDeleter, bool) {
	if s3 == nil {
		return nil, false
	}
	deleter, ok := s3.(RcloneNativeExactVersionDeleter)
	return deleter, ok
}

type RclonePointDeleter struct {
	prefix *RclonePrefixPointDeleter
	now    func() time.Time
}

func NewRclonePointDeleter(prefix *RclonePrefixPointDeleter, now func() time.Time) (*RclonePointDeleter, error) {
	if prefix == nil {
		return nil, fmt.Errorf("%w: invalid Rclone deleter dependencies", backupasset.ErrInvalidState)
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &RclonePointDeleter{prefix: prefix, now: now}, nil
}

func (deleter *RclonePointDeleter) ProviderKind() backupasset.ProviderKind {
	return backupasset.ProviderRclone
}

func (deleter *RclonePointDeleter) DeletePoint(ctx context.Context, request DeletePointRequest) (DeletePointResult, error) {
	switch access := request.Snapshot.Access.AdapterData.(type) {
	case RclonePrefixDeletionAccess:
		return deleter.prefix.DeletePoint(ctx, request)
	case RcloneNativeDeletionAccess:
		if access.Client == nil {
			return DeletePointResult{}, invalidDeletePointRequest("native exact-version client unavailable")
		}
		native, err := NewRcloneNativePointDeleter(access.Client, deleter.now)
		if err != nil {
			return DeletePointResult{}, err
		}
		return native.DeletePoint(ctx, request)
	default:
		return DeletePointResult{}, invalidDeletePointRequest("unsupported rclone deletion access")
	}
}

func rcloneDeletionInvocation(
	binding AccessBinding,
	operation CommandOperation,
	source *RclonePrivateLocator,
	limits OperationLimits,
	now time.Time,
) (CommandInvocation, error) {
	if source == nil || !source.valid() || len(binding.Secret) == 0 || limits.Timeout <= 0 {
		return CommandInvocation{}, invalidDeletePointRequest("invalid Rclone deletion invocation")
	}
	purpose := CommandPurposeList
	if operation == OperationRcloneManagedDeleteExactPrefix {
		purpose = CommandPurposeDelete
	}
	invocation := CommandInvocation{
		Tool: ToolRclone, Operation: operation, Purpose: purpose,
		SecretStdin: append([]byte(nil), binding.Secret...), RcloneSource: source,
		RcloneLowLevelRetries: 1, AbsoluteDeadline: now.Add(limits.Timeout).UTC(),
	}
	switch access := binding.AdapterData.(type) {
	case RclonePrefixDeletionAccess:
		invocation.Runtime = access.Command
	case RcloneRuntimeAccess:
		invocation.Runtime = access.Command
	}
	if err := invocation.Validate(); err != nil {
		return CommandInvocation{}, err
	}
	return invocation, nil
}

type RcloneNativePointDeleter struct {
	versions RcloneNativeExactVersionDeleter
	now      func() time.Time
}

func NewRcloneNativePointDeleter(versions RcloneNativeExactVersionDeleter, now func() time.Time) (*RcloneNativePointDeleter, error) {
	if versions == nil {
		return nil, fmt.Errorf("%w: invalid Rclone native deleter dependencies", backupasset.ErrInvalidState)
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &RcloneNativePointDeleter{versions: versions, now: now}, nil
}

func (deleter *RcloneNativePointDeleter) ProviderKind() backupasset.ProviderKind {
	return backupasset.ProviderRclone
}

func (deleter *RcloneNativePointDeleter) DeletePoint(ctx context.Context, request DeletePointRequest) (DeletePointResult, error) {
	if err := request.requireSourceRevision(); err != nil {
		return DeletePointResult{}, err
	}
	access, err := rcloneNativeDeletionAccess(request)
	if err != nil {
		return DeletePointResult{}, err
	}
	present := 0
	for _, version := range access.Versions {
		probe, probeErr := deleter.versions.ProbeExactVersion(ctx, version)
		if probeErr != nil {
			return DeletePointResult{}, probeErr
		}
		if probe.Locked {
			return DeletePointResult{Outcome: DeletePointBlockedWORM}, ErrDeletePointWORM
		}
		if probe.Present {
			present++
		}
	}
	if present == 0 {
		return rcloneDeletionReceipt(DeletePointAlreadyAbsent, request, request.ExpectedSourceRevision)
	}
	for _, version := range access.Versions {
		if err := deleter.versions.DeleteExactVersion(ctx, version); err != nil {
			if errors.Is(err, ErrDeletePointWORM) {
				return DeletePointResult{Outcome: DeletePointBlockedWORM}, ErrDeletePointWORM
			}
			return DeletePointResult{}, err
		}
	}
	for _, version := range access.Versions {
		probe, probeErr := deleter.versions.ProbeExactVersion(ctx, version)
		if probeErr != nil {
			return DeletePointResult{}, probeErr
		}
		if probe.Present || probe.Locked {
			return DeletePointResult{}, fmt.Errorf("%w: exact native object version remained after delete", backupasset.ErrInvalidState)
		}
	}
	return rcloneDeletionReceipt(DeletePointDeleted, request, request.ExpectedSourceRevision)
}

func rcloneNativeDeletionAccess(request DeletePointRequest) (RcloneNativeDeletionAccess, error) {
	if request.Snapshot.Access.Provider != backupasset.ProviderRclone {
		return RcloneNativeDeletionAccess{}, invalidDeletePointRequest("invalid Rclone native deletion provider")
	}
	access, ok := request.Snapshot.Access.AdapterData.(RcloneNativeDeletionAccess)
	if !ok || len(access.Versions) == 0 || !validTaggedDigest(access.AuthorityDigest) {
		return RcloneNativeDeletionAccess{}, invalidDeletePointRequest("invalid Rclone native deletion access")
	}
	access.Client = nil
	seen := make(map[string]struct{}, len(access.Versions))
	for _, version := range access.Versions {
		if !validRcloneNativePhysicalKey(version.PhysicalKey) || version.VersionID == "" || !validRcloneNativeVersionID(version.VersionID) {
			return RcloneNativeDeletionAccess{}, invalidDeletePointRequest("exact frozen native version set required")
		}
		key := version.PhysicalKey + "\x00" + version.VersionID
		if _, exists := seen[key]; exists {
			return RcloneNativeDeletionAccess{}, invalidDeletePointRequest("duplicate native version")
		}
		seen[key] = struct{}{}
	}
	return access, nil
}

func rcloneDeletionReceipt(outcome DeletePointOutcome, request DeletePointRequest, identity string) (DeletePointResult, error) {
	digest, err := deletionReceiptDigest(backupasset.ProviderRclone, outcome, request.OperationID, identity)
	if err != nil {
		return DeletePointResult{}, err
	}
	return DeletePointResult{Outcome: outcome, ReceiptDigest: digest}, nil
}

var _ PointDeleter = (*RclonePrefixPointDeleter)(nil)
var _ PointDeleter = (*RcloneNativePointDeleter)(nil)
var _ PointDeleter = (*RclonePointDeleter)(nil)
