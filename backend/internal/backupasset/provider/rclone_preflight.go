package provider

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"time"

	"xirang/backend/internal/backupasset"
)

const rclonePreflightCanaryBytes = 64

type RclonePortableCommandPreflightRequest struct {
	PreflightID            string
	BoundConfig            RcloneBoundConfig
	ManagedRoot            RclonePrivateLocator
	Runtime                RemoteCommandAccess
	AbsoluteDeadline       time.Time
	LowLevelRetries        int
	ControlPayloadMaxBytes int64
	FullVerifyMaxBytes     int64
	ManifestOptions        RcloneManifestBuildOptions
}

type RclonePortableCommandPreflightResult struct {
	ManagedRootIdentityDigest string
	RepositoryMarkerDigest    string
	EvidenceDigest            string
	VerifiedBytes             uint64
}

type RcloneNativeCommandPreflightRequest struct {
	PreflightID            string
	RcloneConfig           []byte `json:"-"`
	Destination            RclonePrivateLocator
	Runtime                RemoteCommandAccess
	AbsoluteDeadline       time.Time
	LowLevelRetries        int
	ControlPayloadMaxBytes int64
}

type RcloneNativeCommandPreflightResult struct {
	PayloadDigest string
	PayloadBytes  uint64
	RangeDigest   string
	RangeBytes    uint64
}

type RclonePreflightCommandPlane interface {
	PreflightPortable(context.Context, RclonePortableCommandPreflightRequest) (RclonePortableCommandPreflightResult, error)
	WriteNativeCanary(context.Context, RcloneNativeCommandPreflightRequest) (RcloneNativeCommandPreflightResult, error)
}

type CommandRclonePreflightPlane struct {
	commands     CommandTransport
	streams      CommandStreamTransport
	staging      StagedPayloadTransport
	limitsSource OperationLimitsSource
	random       io.Reader
}

func NewCommandRclonePreflightPlane(
	commands CommandTransport,
	streams CommandStreamTransport,
	staging StagedPayloadTransport,
	limitsSource OperationLimitsSource,
	random io.Reader,
) (*CommandRclonePreflightPlane, error) {
	if commands == nil || streams == nil || staging == nil || limitsSource == nil {
		return nil, fmt.Errorf("%w: incomplete Rclone preflight command dependencies", backupasset.ErrInvalidState)
	}
	if random == nil {
		random = rand.Reader
	}
	limits, err := limitsSource()
	if err != nil {
		return nil, err
	}
	if err := limits.Validate(); err != nil {
		return nil, err
	}
	return &CommandRclonePreflightPlane{
		commands: commands, streams: streams, staging: staging, limitsSource: limitsSource, random: random,
	}, nil
}

func (plane *CommandRclonePreflightPlane) PreflightPortable(
	ctx context.Context,
	request RclonePortableCommandPreflightRequest,
) (RclonePortableCommandPreflightResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if plane == nil || backupasset.ValidateOpaqueID(request.PreflightID) != nil || !request.ManagedRoot.valid() ||
		len(request.BoundConfig.ExactBytes()) == 0 || request.ControlPayloadMaxBytes < rclonePreflightCanaryBytes ||
		request.FullVerifyMaxBytes < rclonePreflightCanaryBytes || request.ManifestOptions.SpoolMaxBytes <= 0 {
		return RclonePortableCommandPreflightResult{}, fmt.Errorf("%w: invalid portable Rclone preflight request", backupasset.ErrInvalidState)
	}
	if err := plane.validateManagedVersion(ctx, request.BoundConfig.ExactBytes(), request.Runtime, request.AbsoluteDeadline, request.LowLevelRetries); err != nil {
		return RclonePortableCommandPreflightResult{}, err
	}

	preflightRoot, err := joinRclonePrivateLocator(request.ManagedRoot, "v1/preflight/"+request.PreflightID)
	if err != nil {
		return RclonePortableCommandPreflightResult{}, err
	}
	base := plane.invocation(request.BoundConfig.ExactBytes(), request.Runtime, request.AbsoluteDeadline, request.LowLevelRetries)
	if err := plane.requireAbsent(ctx, base, preflightRoot); err != nil {
		return RclonePortableCommandPreflightResult{}, err
	}
	sourceRoot, err := joinRclonePrivateLocator(preflightRoot, "source")
	if err != nil {
		return RclonePortableCommandPreflightResult{}, err
	}
	copyRoot, err := joinRclonePrivateLocator(preflightRoot, "copy")
	if err != nil {
		return RclonePortableCommandPreflightResult{}, err
	}
	sourceObject, err := joinRclonePrivateLocator(sourceRoot, "canary.bin")
	if err != nil {
		return RclonePortableCommandPreflightResult{}, err
	}
	copyObject, err := joinRclonePrivateLocator(copyRoot, "canary.bin")
	if err != nil {
		return RclonePortableCommandPreflightResult{}, err
	}
	payload, ref, err := plane.stageCanary(ctx, request.PreflightID, request.Runtime, request.ControlPayloadMaxBytes)
	if err != nil {
		return RclonePortableCommandPreflightResult{}, err
	}
	defer plane.staging.Cleanup(context.Background(), request.Runtime, ref) //nolint:errcheck // remote evidence is independent of local hygiene

	limits, err := plane.limits()
	if err != nil {
		return RclonePortableCommandPreflightResult{}, err
	}
	copyTo := base
	copyTo.Operation = OperationRcloneManagedCopyTo
	copyTo.RcloneStagedSource = &ref
	copyTo.RcloneDestination = &sourceObject
	if _, err := plane.commands.Run(ctx, copyTo, limits); err != nil {
		return RclonePortableCommandPreflightResult{}, err
	}
	copyInvocation := base
	copyInvocation.Operation = OperationRcloneManagedCopy
	copyInvocation.RcloneSource = &sourceRoot
	copyInvocation.RcloneDestination = &copyRoot
	if _, err := plane.commands.Run(ctx, copyInvocation, limits); err != nil {
		return RclonePortableCommandPreflightResult{}, err
	}
	features := base
	features.Operation = OperationRcloneManagedFeatures
	features.RcloneSource = &request.ManagedRoot
	if _, err := plane.commands.Run(ctx, features, limits); err != nil {
		return RclonePortableCommandPreflightResult{}, err
	}
	sourceManifest, err := plane.observe(ctx, base, sourceRoot, request.ManifestOptions)
	if err != nil {
		return RclonePortableCommandPreflightResult{}, err
	}
	copyManifest, err := plane.observe(ctx, base, copyRoot, request.ManifestOptions)
	if err != nil {
		return RclonePortableCommandPreflightResult{}, err
	}
	if sourceManifest.ObservationDigest != copyManifest.ObservationDigest || sourceManifest.EntryCount != 1 ||
		copyManifest.EntryCount != 1 || sourceManifest.LogicalBytes != uint64(len(payload)) || copyManifest.LogicalBytes != uint64(len(payload)) {
		return RclonePortableCommandPreflightResult{}, ErrRcloneManifestObservationMismatch
	}
	check := base
	check.Operation = OperationRcloneManagedCheckDownload
	check.RcloneSource = &sourceRoot
	check.RcloneDestination = &copyRoot
	if _, err := plane.commands.Run(ctx, check, limits); err != nil {
		return RclonePortableCommandPreflightResult{}, err
	}
	for _, object := range []RclonePrivateLocator{sourceObject, copyObject} {
		readback, err := plane.readExact(ctx, base, object, int64(len(payload)))
		if err != nil {
			return RclonePortableCommandPreflightResult{}, err
		}
		if !bytes.Equal(readback, payload) {
			return RclonePortableCommandPreflightResult{}, ErrRcloneManifestObservationMismatch
		}
	}

	markerDigest := rclonePreflightDigest("marker-v1", payload)
	rootDigest := rclonePreflightDigest("portable-root-v1", []byte(request.BoundConfig.KeyedDigest()+"\n"+request.ManagedRoot.value))
	evidenceDigest := rclonePreflightDigest("portable-evidence-v1", []byte(
		rootDigest+"\n"+markerDigest+"\n"+sourceManifest.ObservationDigest+"\n"+copyManifest.ObservationDigest,
	))
	return RclonePortableCommandPreflightResult{
		ManagedRootIdentityDigest: rootDigest, RepositoryMarkerDigest: markerDigest,
		EvidenceDigest: evidenceDigest, VerifiedBytes: uint64(len(payload)),
	}, nil
}

func (plane *CommandRclonePreflightPlane) WriteNativeCanary(
	ctx context.Context,
	request RcloneNativeCommandPreflightRequest,
) (RcloneNativeCommandPreflightResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if plane == nil || backupasset.ValidateOpaqueID(request.PreflightID) != nil || !request.Destination.valid() ||
		len(request.RcloneConfig) == 0 || len(request.RcloneConfig) > 64<<10 || request.ControlPayloadMaxBytes < rclonePreflightCanaryBytes {
		return RcloneNativeCommandPreflightResult{}, fmt.Errorf("%w: invalid native Rclone preflight request", backupasset.ErrInvalidState)
	}
	if err := plane.validateManagedVersion(ctx, request.RcloneConfig, request.Runtime, request.AbsoluteDeadline, request.LowLevelRetries); err != nil {
		return RcloneNativeCommandPreflightResult{}, err
	}
	payload, ref, err := plane.stageCanary(ctx, request.PreflightID, request.Runtime, request.ControlPayloadMaxBytes)
	if err != nil {
		return RcloneNativeCommandPreflightResult{}, err
	}
	defer plane.staging.Cleanup(context.Background(), request.Runtime, ref) //nolint:errcheck // remote evidence is independent of local hygiene
	limits, err := plane.limits()
	if err != nil {
		return RcloneNativeCommandPreflightResult{}, err
	}
	invocation := plane.invocation(request.RcloneConfig, request.Runtime, request.AbsoluteDeadline, request.LowLevelRetries)
	invocation.Operation = OperationRcloneManagedCopyTo
	invocation.RcloneStagedSource = &ref
	invocation.RcloneDestination = &request.Destination
	if _, err := plane.commands.Run(ctx, invocation, limits); err != nil {
		return RcloneNativeCommandPreflightResult{}, err
	}
	readback, err := plane.readExact(ctx, plane.invocation(request.RcloneConfig, request.Runtime, request.AbsoluteDeadline, request.LowLevelRetries), request.Destination, int64(len(payload)))
	if err != nil {
		return RcloneNativeCommandPreflightResult{}, err
	}
	if !bytes.Equal(readback, payload) {
		return RcloneNativeCommandPreflightResult{}, ErrRcloneManifestObservationMismatch
	}
	const rangeBytes = 16
	return RcloneNativeCommandPreflightResult{
		PayloadDigest: rawRclonePreflightDigest(payload), PayloadBytes: uint64(len(payload)),
		RangeDigest: rawRclonePreflightDigest(payload[:rangeBytes]), RangeBytes: rangeBytes,
	}, nil
}

func (plane *CommandRclonePreflightPlane) validateManagedVersion(
	ctx context.Context,
	config []byte,
	runtime RemoteCommandAccess,
	deadline time.Time,
	retries int,
) error {
	limits, err := plane.limits()
	if err != nil {
		return err
	}
	invocation := plane.invocation(config, runtime, deadline, retries)
	invocation.Operation = OperationRcloneManagedVersion
	output, err := plane.commands.Run(ctx, invocation, limits)
	if err != nil {
		return err
	}
	return ValidateManagedRcloneVersion(output.Stdout)
}

func (plane *CommandRclonePreflightPlane) requireAbsent(ctx context.Context, base CommandInvocation, locator RclonePrivateLocator) error {
	limits, err := plane.limits()
	if err != nil {
		return err
	}
	invocation := base
	invocation.Operation = OperationRcloneManagedExactStat
	invocation.RcloneSource = &locator
	execution, err := plane.streams.OpenExecution(ctx, invocation, limits, limits.MaxMetadataBytes)
	if err != nil {
		return err
	}
	if _, err := io.Copy(io.Discard, execution); err != nil {
		_ = execution.Cancel()
		return err
	}
	completion, err := execution.Join()
	if err != nil {
		return err
	}
	if !completion.ExitCodeKnown {
		return ErrRcloneAttemptPresenceUnknown
	}
	if completion.ExitCode == 3 {
		return nil
	}
	if completion.ExitCode == 0 {
		return ErrRcloneAttemptCollision
	}
	return ErrRcloneAttemptPresenceUnknown
}

func (plane *CommandRclonePreflightPlane) observe(
	ctx context.Context,
	base CommandInvocation,
	root RclonePrivateLocator,
	options RcloneManifestBuildOptions,
) (RcloneManifestBundle, error) {
	limits, err := plane.limits()
	if err != nil {
		return RcloneManifestBundle{}, err
	}
	invocation := base
	invocation.Operation = OperationRcloneManagedRecursiveList
	invocation.RcloneSource = &root
	handle, err := plane.commands.Open(ctx, invocation, limits, options.SpoolMaxBytes)
	if err != nil {
		return RcloneManifestBundle{}, err
	}
	manifest, buildErr := BuildRcloneManifestV1(ctx, handle, options)
	closeErr := handle.Close()
	if buildErr != nil {
		return RcloneManifestBundle{}, buildErr
	}
	if closeErr != nil {
		return RcloneManifestBundle{}, closeErr
	}
	return manifest, nil
}

func (plane *CommandRclonePreflightPlane) readExact(
	ctx context.Context,
	base CommandInvocation,
	object RclonePrivateLocator,
	expectedBytes int64,
) ([]byte, error) {
	limits, err := plane.limits()
	if err != nil {
		return nil, err
	}
	invocation := base
	invocation.Operation = OperationRcloneManagedCat
	invocation.RcloneSource = &object
	handle, err := plane.commands.Open(ctx, invocation, limits, expectedBytes+1)
	if err != nil {
		return nil, err
	}
	payload, readErr := io.ReadAll(io.LimitReader(handle, expectedBytes+1))
	closeErr := handle.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if int64(len(payload)) != expectedBytes {
		return nil, ErrRcloneManifestObservationMismatch
	}
	return payload, nil
}

func (plane *CommandRclonePreflightPlane) stageCanary(
	ctx context.Context,
	preflightID string,
	runtime RemoteCommandAccess,
	maxBytes int64,
) ([]byte, StagedPayloadRef, error) {
	payload := make([]byte, rclonePreflightCanaryBytes)
	if _, err := io.ReadFull(plane.random, payload); err != nil {
		return nil, StagedPayloadRef{}, err
	}
	ref, err := plane.staging.Stage(ctx, runtime, StagedPayloadRequest{
		AttemptID: preflightID, Name: "canary.bin", Payload: payload, MaxBytes: maxBytes,
	})
	if err != nil {
		return nil, StagedPayloadRef{}, err
	}
	return payload, ref, nil
}

func (plane *CommandRclonePreflightPlane) limits() (OperationLimits, error) {
	if plane == nil || plane.limitsSource == nil {
		return OperationLimits{}, fmt.Errorf("%w: Rclone preflight operation limits unavailable", backupasset.ErrInvalidState)
	}
	limits, err := plane.limitsSource()
	if err != nil {
		return OperationLimits{}, err
	}
	if err := limits.Validate(); err != nil {
		return OperationLimits{}, err
	}
	return limits, nil
}

func (*CommandRclonePreflightPlane) invocation(config []byte, runtime RemoteCommandAccess, deadline time.Time, retries int) CommandInvocation {
	return CommandInvocation{
		Tool: ToolRclone, Purpose: CommandPurposePublish, SecretStdin: append([]byte(nil), config...), Runtime: &runtime,
		RcloneLowLevelRetries: retries, AbsoluteDeadline: deadline,
	}
}

func rclonePreflightDigest(domain string, payload []byte) string {
	digest := sha256.New()
	_, _ = io.WriteString(digest, "xirang-rclone-preflight-"+domain+"\n")
	_, _ = digest.Write(payload)
	return hex.EncodeToString(digest.Sum(nil))
}

func rawRclonePreflightDigest(payload []byte) string {
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

var _ RclonePreflightCommandPlane = (*CommandRclonePreflightPlane)(nil)
