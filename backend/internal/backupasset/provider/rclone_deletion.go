package provider

import (
	"context"
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
	Command              *RemoteCommandAccess `json:"-"`
}

type RcloneNativeExactVersion struct {
	PhysicalKey string `json:"-"`
	VersionID   string `json:"-"`
}

type RcloneNativeDeletionAccess struct {
	Versions []RcloneNativeExactVersion      `json:"-"`
	Client   RcloneNativeExactVersionDeleter `json:"-"`
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
	marker, err := NewRclonePrivateLocator(strings.TrimSuffix(access.Prefix.value, "/") + "/commit.json")
	if err != nil {
		return ErrDeletePointIdentityConflict
	}
	invocation, err := rcloneDeletionInvocation(binding, OperationRcloneManagedCat, &marker, limits, deleter.now().UTC())
	if err != nil {
		return err
	}
	output, err := deleter.transport.Run(ctx, invocation, limits)
	if err != nil {
		return mapCommandTransportError(ctx, err)
	}
	var live struct {
		SourceFingerprint string `json:"source_fingerprint"`
	}
	if json.Unmarshal(output.Stdout, &live) != nil || live.SourceFingerprint != access.MarkerDigest {
		return ErrDeletePointIdentityConflict
	}
	return nil
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
		request.Point.Native != access.Prefix.value || access.Command == nil || access.Command.Node.ID == 0 {
		return RclonePrefixDeletionAccess{}, invalidDeletePointRequest("invalid Rclone prefix deletion access")
	}
	if !validCommittedRclonePointPrefix(access.Prefix.value) {
		return RclonePrefixDeletionAccess{}, invalidDeletePointRequest("exact committed Rclone prefix required")
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
	if cleanedRemote == "." || strings.HasPrefix(cleanedRemote, "..") || strings.Contains(cleanedRemote, "..") {
		return false
	}
	const marker = "/points/"
	index := strings.LastIndex("/"+cleanedRemote, marker)
	if index < 0 {
		return false
	}
	rest := ("/" + cleanedRemote)[index+len(marker):]
	if len(rest) < 32 || !lowerHex(rest[:32], 32) {
		return false
	}
	consumed := 32
	if len(rest) > 32 && rest[32] == '.' {
		if len(rest) < 65 || !lowerHex(rest[33:65], 32) {
			return false
		}
		consumed = 65
	}
	remainder := rest[consumed:]
	if remainder != "" && !strings.HasPrefix(remainder, "/") {
		return false
	}
	pointRoot := path.Clean(marker + rest[:consumed])
	cleaned := path.Clean(pointRoot + remainder)
	return cleaned == pointRoot || strings.HasPrefix(cleaned, pointRoot+"/")
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
	if !ok || len(access.Versions) == 0 {
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
