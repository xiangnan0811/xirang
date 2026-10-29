package provider

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
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

var (
	ErrRcloneAttemptCollision       = errors.New("rclone attempt prefix collision")
	ErrRcloneAttemptPresenceUnknown = errors.New("rclone attempt prefix presence unknown")
	ErrRcloneCommitOutcomeUnknown   = errors.New("rclone commit outcome unknown")
	ErrRcloneMarkerMismatch         = errors.New("rclone marker mismatch")
	ErrRcloneCopyDestRetryRequired  = errors.New("rclone copy-dest retry requires a fresh attempt")
	ErrRcloneRemoteObjectNotFound   = errors.New("rclone remote object not found")
)

type RcloneAttemptPresence string

const (
	RcloneAttemptAbsent          RcloneAttemptPresence = "absent"
	RcloneAttemptPresent         RcloneAttemptPresence = "present"
	RcloneAttemptPresenceUnknown RcloneAttemptPresence = "unknown"
)

type RclonePortablePublicationRequest struct {
	Attempt                  RcloneAttemptV1
	BoundConfig              RcloneBoundConfig
	Source                   RclonePrivateLocator
	AttemptRoot              RclonePrivateLocator
	DataRoot                 RclonePrivateLocator
	ControlRoot              RclonePrivateLocator
	CopyDest                 *RclonePrivateLocator
	ParentLeaseEligible      bool
	MarkerKey                []byte `json:"-"`
	Manifest                 RcloneManifestBundle
	CapabilityEvidenceDigest string
	CostEvidenceDigest       string
	SettleInterval           time.Duration
	FullVerifyMaxBytes       int64
	ControlPayloadMaxBytes   int64
	LowLevelRetries          int
	Runtime                  RemoteCommandAccess
	ReadDeadline             time.Time `json:"-"`
	ManifestOptions          RcloneManifestBuildOptions
}

type RclonePortableRemote interface {
	ProbeAttempt(context.Context, RclonePortablePublicationRequest) (RcloneAttemptPresence, error)
	Observe(context.Context, RclonePortablePublicationRequest, RclonePrivateLocator) (RcloneManifestBundle, error)
	PutControl(context.Context, RclonePortablePublicationRequest, string, []byte) error
	ReadControl(context.Context, RclonePortablePublicationRequest, string, int64) ([]byte, error)
	TransferData(context.Context, RclonePortablePublicationRequest, *RclonePrivateLocator) error
	VerifyFullBytes(context.Context, RclonePortablePublicationRequest, uint64) (RclonePortableByteVerification, error)
}

type CommandRclonePortableRemote struct {
	commands       CommandTransport
	streamCommands CommandStreamTransport
	staging        StagedPayloadTransport
	limitsSource   OperationLimitsSource
}

func NewCommandRclonePortableRemote(commands CommandTransport, staging StagedPayloadTransport, limitsSource OperationLimitsSource) (*CommandRclonePortableRemote, error) {
	if commands == nil || staging == nil {
		return nil, fmt.Errorf("%w: incomplete portable Rclone remote dependencies", backupasset.ErrInvalidState)
	}
	if _, err := resolveOperationLimits(limitsSource); err != nil {
		return nil, err
	}
	streamCommands, _ := commands.(CommandStreamTransport)
	return &CommandRclonePortableRemote{commands: commands, streamCommands: streamCommands, staging: staging, limitsSource: limitsSource}, nil
}

func (remote *CommandRclonePortableRemote) ProbeAttempt(ctx context.Context, request RclonePortablePublicationRequest) (RcloneAttemptPresence, error) {
	if !request.ReadDeadline.IsZero() {
		return RcloneAttemptPresenceUnknown, fmt.Errorf("%w: portable read request cannot probe an attempt", backupasset.ErrInvalidState)
	}
	limits, err := resolveOperationLimits(remote.limitsSource)
	if err != nil {
		return RcloneAttemptPresenceUnknown, err
	}
	if remote.streamCommands != nil {
		execution, err := remote.streamCommands.OpenExecution(
			ctx,
			remote.invocation(request, OperationRcloneManagedExactStat, &request.AttemptRoot, nil, nil, nil),
			limits,
			limits.MaxMetadataBytes,
		)
		if err != nil {
			return RcloneAttemptPresenceUnknown, err
		}
		if _, err := io.Copy(io.Discard, execution); err != nil {
			_ = execution.Cancel()
			return RcloneAttemptPresenceUnknown, err
		}
		completion, err := execution.Join()
		if err != nil {
			return RcloneAttemptPresenceUnknown, err
		}
		if !completion.ExitCodeKnown {
			return RcloneAttemptPresenceUnknown, ErrRcloneAttemptPresenceUnknown
		}
		switch completion.ExitCode {
		case 0:
			return RcloneAttemptPresent, nil
		case 3:
			return RcloneAttemptAbsent, nil
		default:
			return RcloneAttemptPresenceUnknown, ErrRcloneAttemptPresenceUnknown
		}
	}
	_, err = remote.commands.Run(ctx, remote.invocation(request, OperationRcloneManagedExactStat, &request.AttemptRoot, nil, nil, nil), limits)
	if err == nil {
		return RcloneAttemptPresent, nil
	}
	if errors.Is(err, ErrRcloneRemoteObjectNotFound) {
		return RcloneAttemptAbsent, nil
	}
	return RcloneAttemptPresenceUnknown, err
}

func (remote *CommandRclonePortableRemote) Observe(ctx context.Context, request RclonePortablePublicationRequest, locator RclonePrivateLocator) (RcloneManifestBundle, error) {
	if err := validatePortableReadDeadline(ctx, request); err != nil {
		return RcloneManifestBundle{}, err
	}
	if !request.ReadDeadline.IsZero() && (!request.DataRoot.valid() || locator.value != request.DataRoot.value) {
		return RcloneManifestBundle{}, fmt.Errorf("%w: portable read observation must target committed data", backupasset.ErrInvalidState)
	}
	limits, err := resolveOperationLimits(remote.limitsSource)
	if err != nil {
		return RcloneManifestBundle{}, err
	}
	invocation := remote.invocation(request, OperationRcloneManagedRecursiveList, &locator, nil, nil, nil)
	handle, err := remote.commands.Open(ctx, invocation, limits, request.ManifestOptions.SpoolMaxBytes)
	if err != nil {
		return RcloneManifestBundle{}, err
	}
	options := request.ManifestOptions
	options.SymlinkTargetReader = func(readContext context.Context, physicalPath string, maxBytes int64) ([]byte, error) {
		object, err := joinRclonePrivateLocator(locator, physicalPath)
		if err != nil {
			return nil, err
		}
		return remote.readObject(readContext, request, object, maxBytes)
	}
	destination := locator.valid() && request.AttemptRoot.valid() && request.DataRoot.valid() &&
		locator.value == request.DataRoot.value && request.DataRoot.value == request.AttemptRoot.value+"/data" &&
		(!request.Source.valid() || locator.value != request.Source.value)
	manifest, buildErr := buildRcloneManifestV1(ctx, handle, options, destination)
	closeErr := handle.Close()
	if buildErr != nil {
		return RcloneManifestBundle{}, buildErr
	}
	if closeErr != nil {
		return RcloneManifestBundle{}, closeErr
	}
	return manifest, nil
}

func (remote *CommandRclonePortableRemote) PutControl(ctx context.Context, request RclonePortablePublicationRequest, name string, payload []byte) (resultErr error) {
	commandInvoked := false
	defer func() {
		if resultErr != nil && !commandInvoked {
			resultErr = rcloneCommandNotAttempted(resultErr)
		}
	}()
	if !request.ReadDeadline.IsZero() {
		return fmt.Errorf("%w: portable read request cannot write controls", backupasset.ErrInvalidState)
	}
	if !validStagedPayloadName(name) || len(payload) == 0 || int64(len(payload)) > request.ControlPayloadMaxBytes {
		return fmt.Errorf("%w: invalid portable control payload", backupasset.ErrInvalidState)
	}
	ref, err := remote.staging.Stage(ctx, request.Runtime, StagedPayloadRequest{
		AttemptID: request.Attempt.AttemptID, Name: name, Payload: payload, MaxBytes: request.ControlPayloadMaxBytes,
	})
	if err != nil {
		return err
	}
	defer remote.staging.Cleanup(context.Background(), request.Runtime, ref) //nolint:errcheck // remote outcome is independent of local hygiene
	destination, err := joinRclonePrivateLocator(request.ControlRoot, name)
	if err != nil {
		return err
	}
	limits, err := resolveOperationLimits(remote.limitsSource)
	if err != nil {
		return err
	}
	commandInvoked = true
	_, err = remote.commands.Run(ctx, remote.invocation(request, OperationRcloneManagedCopyTo, nil, &destination, &ref, nil), limits)
	return err
}

func (remote *CommandRclonePortableRemote) ReadControl(ctx context.Context, request RclonePortablePublicationRequest, name string, maxBytes int64) ([]byte, error) {
	if !validStagedPayloadName(name) || maxBytes <= 0 || maxBytes > request.ControlPayloadMaxBytes {
		return nil, fmt.Errorf("%w: invalid portable control read", backupasset.ErrInvalidState)
	}
	locator, err := joinRclonePrivateLocator(request.ControlRoot, name)
	if err != nil {
		return nil, err
	}
	return remote.readObject(ctx, request, locator, maxBytes)
}

func (remote *CommandRclonePortableRemote) TransferData(ctx context.Context, request RclonePortablePublicationRequest, copyDest *RclonePrivateLocator) (resultErr error) {
	commandInvoked := false
	defer func() {
		if resultErr != nil && !commandInvoked {
			resultErr = rcloneCommandNotAttempted(resultErr)
		}
	}()
	if !request.ReadDeadline.IsZero() {
		return fmt.Errorf("%w: portable read request cannot transfer data", backupasset.ErrInvalidState)
	}
	limits, err := resolveOperationLimits(remote.limitsSource)
	if err != nil {
		return err
	}
	invocation := remote.invocation(request, OperationRcloneManagedCopy, &request.Source, &request.DataRoot, nil, nil)
	invocation.RcloneCopyDest = copyDest
	commandInvoked = true
	_, err = remote.commands.Run(ctx, invocation, limits)
	return err
}

func (remote *CommandRclonePortableRemote) VerifyFullBytes(ctx context.Context, request RclonePortablePublicationRequest, expectedBytes uint64) (RclonePortableByteVerification, error) {
	if !request.ReadDeadline.IsZero() {
		return RclonePortableByteVerification{}, fmt.Errorf("%w: portable read request cannot verify a transfer", backupasset.ErrInvalidState)
	}
	if request.FullVerifyMaxBytes <= 0 || expectedBytes > uint64(request.FullVerifyMaxBytes) {
		return RclonePortableByteVerification{}, ErrRcloneManifestFullByteProofRequired
	}
	limits, err := resolveOperationLimits(remote.limitsSource)
	if err != nil {
		return RclonePortableByteVerification{}, err
	}
	_, err = remote.commands.Run(ctx, remote.invocation(request, OperationRcloneManagedCheckDownload, &request.Source, &request.DataRoot, nil, nil), limits)
	if err != nil {
		return RclonePortableByteVerification{}, err
	}
	return RclonePortableByteVerification{VerifiedBytes: expectedBytes, Complete: true}, nil
}

func (remote *CommandRclonePortableRemote) readObject(ctx context.Context, request RclonePortablePublicationRequest, locator RclonePrivateLocator, maxBytes int64) ([]byte, error) {
	if err := validatePortableReadDeadline(ctx, request); err != nil {
		return nil, err
	}
	if !request.ReadDeadline.IsZero() {
		control := request.ControlRoot.valid() && strings.HasPrefix(locator.value, request.ControlRoot.value+"/")
		link := request.DataRoot.valid() && strings.HasPrefix(locator.value, request.DataRoot.value+"/") &&
			strings.HasSuffix(locator.value, ".rclonelink")
		if !control && !link {
			return nil, fmt.Errorf("%w: portable read object is outside the committed controls or link target", backupasset.ErrInvalidState)
		}
	}
	limits, err := resolveOperationLimits(remote.limitsSource)
	if err != nil {
		return nil, err
	}
	handle, err := remote.commands.Open(ctx, remote.invocation(request, OperationRcloneManagedCat, &locator, nil, nil, nil), limits, maxBytes)
	if err != nil {
		return nil, err
	}
	payload, readErr := io.ReadAll(io.LimitReader(handle, maxBytes+1))
	closeErr := handle.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if int64(len(payload)) > maxBytes {
		return nil, ErrRcloneManifestLimitExceeded
	}
	return payload, nil
}

func (remote *CommandRclonePortableRemote) invocation(
	request RclonePortablePublicationRequest,
	operation CommandOperation,
	source, destination *RclonePrivateLocator,
	stagedSource, stagedDestination *StagedPayloadRef,
) CommandInvocation {
	purpose, deadline := CommandPurposePublish, request.Attempt.PointDeadlineAt
	if !request.ReadDeadline.IsZero() {
		purpose, deadline = CommandPurposeRead, request.ReadDeadline
	}
	return CommandInvocation{
		Tool: ToolRclone, Operation: operation, Purpose: purpose,
		SecretStdin: request.BoundConfig.ExactBytes(), Runtime: &request.Runtime,
		RcloneSource: source, RcloneDestination: destination,
		RcloneStagedSource: stagedSource, RcloneStagedDestination: stagedDestination,
		RcloneLowLevelRetries: request.LowLevelRetries, AbsoluteDeadline: deadline,
	}
}

func joinRclonePrivateLocator(root RclonePrivateLocator, relative string) (RclonePrivateLocator, error) {
	if !root.valid() || !validRcloneLogicalPath(relative, 4096) {
		return RclonePrivateLocator{}, fmt.Errorf("%w: invalid Rclone private child locator", backupasset.ErrInvalidState)
	}
	return NewRclonePrivateLocator(strings.TrimSuffix(root.value, "/") + "/" + path.Clean(relative))
}

type RclonePortablePublisher struct {
	remote RclonePortableRemote
	sleep  func(time.Duration)
	now    func() time.Time
}

type RclonePublicationStrategy struct {
	portable *RclonePortablePublisher
	native   *RcloneNativePublisher
}

func NewRclonePublicationStrategy(portable *RclonePortablePublisher, native *RcloneNativePublisher) (*RclonePublicationStrategy, error) {
	if portable == nil || portable.remote == nil || native == nil || native.dataPlane == nil {
		return nil, fmt.Errorf("%w: Rclone publication strategy unavailable", backupasset.ErrInvalidState)
	}
	return &RclonePublicationStrategy{portable: portable, native: native}, nil
}

func (*RclonePublicationStrategy) Kind() backupasset.ProviderKind { return backupasset.ProviderRclone }

func (strategy *RclonePublicationStrategy) Prepare(ctx context.Context, request PublicationPrepareRequest) (prepared PreparedPublication, resultErr error) {
	defer func() { resultErr = RcloneUnattemptedPublicationError(resultErr) }()
	if request.RcloneInput != nil && request.RcloneInput.PortableRequest != nil &&
		!request.RcloneInput.PortableRequest.ReadDeadline.IsZero() {
		return PreparedPublication{}, fmt.Errorf("%w: portable read request cannot prepare a publication", backupasset.ErrInvalidState)
	}
	if strategy == nil || strategy.portable == nil || strategy.native == nil {
		return PreparedPublication{}, fmt.Errorf("%w: Rclone publication strategy unavailable", backupasset.ErrInvalidState)
	}
	if err := request.Validate(); err != nil {
		return PreparedPublication{}, errors.Join(fmt.Errorf("%w: invalid Rclone publication prepare request", backupasset.ErrInvalidState), err)
	}
	if request.Attempt.Provider != backupasset.ProviderRclone || request.RcloneInput == nil {
		return PreparedPublication{}, fmt.Errorf("%w: invalid Rclone publication prepare request", backupasset.ErrInvalidState)
	}
	attempt, err := request.Attempt.RcloneAttempt()
	if err != nil {
		return PreparedPublication{}, errors.Join(fmt.Errorf("%w: unsupported Rclone publication variant", backupasset.ErrInvalidState), err)
	}
	encodedAttempt, err := EncodePublicationAttempt(NewRclonePublicationAttempt(attempt))
	if err != nil {
		return PreparedPublication{}, err
	}
	input := *request.RcloneInput
	switch attempt.PublicationMode {
	case backupasset.PublicationVersionedPrefix:
		if attempt.MinimumRuntimeRevision != 2 || attempt.ManifestSchemaRevision != 2 {
			return PreparedPublication{}, fmt.Errorf("%w: historical portable attempts cannot be prepared again", backupasset.ErrInvalidState)
		}
		portableRequest := cloneRclonePortablePublicationRequest(*request.RcloneInput.PortableRequest)
		encodedVariant, encodeErr := EncodePublicationAttempt(NewRclonePublicationAttempt(portableRequest.Attempt))
		if encodeErr != nil || encodedVariant != encodedAttempt {
			return PreparedPublication{}, errors.Join(fmt.Errorf("%w: Rclone portable attempt mismatch", backupasset.ErrInvalidState), encodeErr)
		}
		if portableRequest.Manifest.Version == 0 && portableRequest.Source.valid() {
			if !emptyRcloneManifestBundle(portableRequest.Manifest) {
				return PreparedPublication{}, fmt.Errorf("%w: partial Rclone portable manifest", backupasset.ErrInvalidState)
			}
			portableRequest.Manifest, err = strategy.portable.remote.Observe(ctx, portableRequest, portableRequest.Source)
			if err != nil {
				return PreparedPublication{}, err
			}
		}
		input.PortableRequest = &portableRequest
		input.NativeRequest = nil
	case backupasset.PublicationNativeObjectVersions:
		nativeRequest := cloneRcloneNativePublicationRequest(*request.RcloneInput.NativeRequest)
		encodedVariant, encodeErr := EncodePublicationAttempt(NewRclonePublicationAttempt(nativeRequest.Attempt))
		if encodeErr != nil || encodedVariant != encodedAttempt {
			return PreparedPublication{}, errors.Join(fmt.Errorf("%w: Rclone native attempt mismatch", backupasset.ErrInvalidState), encodeErr)
		}
		if nativeRequest.Manifest.Version == 0 && nativeRequest.Source.valid() {
			if !emptyRcloneManifestBundle(nativeRequest.Manifest) {
				return PreparedPublication{}, fmt.Errorf("%w: partial Rclone native manifest", backupasset.ErrInvalidState)
			}
			nativeRequest.Manifest, err = strategy.native.dataPlane.ObserveSource(ctx, nativeRequest)
			if err != nil {
				return PreparedPublication{}, err
			}
		}
		input.NativeRequest = &nativeRequest
		input.PortableRequest = nil
	default:
		return PreparedPublication{}, fmt.Errorf("%w: unsupported Rclone publication variant", backupasset.ErrInvalidState)
	}
	return PreparedPublication{Attempt: request.Attempt, RcloneInput: &input}, nil
}

func emptyRcloneManifestBundle(value RcloneManifestBundle) bool {
	return value.Version == 0 && len(value.Chunks) == 0 && len(value.IndexEncoded) == 0 && value.IndexDigest == "" &&
		value.EntryCount == 0 && value.LogicalBytes == 0 && value.ObservationDigest == "" && value.Fidelity.Version == 0
}

func (strategy *RclonePublicationStrategy) Execute(ctx context.Context, prepared PreparedPublication, _ PublicationProgress) (ProviderExecutionResult, error) {
	if prepared.RcloneInput != nil && prepared.RcloneInput.PortableRequest != nil &&
		!prepared.RcloneInput.PortableRequest.ReadDeadline.IsZero() {
		return ProviderExecutionResult{}, fmt.Errorf("%w: portable read request cannot execute a publication", backupasset.ErrInvalidState)
	}
	if strategy == nil || strategy.portable == nil || strategy.native == nil || prepared.RcloneInput == nil {
		return ProviderExecutionResult{}, fmt.Errorf("%w: Rclone publication was not prepared", backupasset.ErrInvalidState)
	}
	attempt, err := prepared.Attempt.RcloneAttempt()
	if err != nil || !prepared.RcloneInput.validateVariant(attempt.PublicationMode) {
		return ProviderExecutionResult{}, fmt.Errorf("%w: Rclone publication was not prepared", backupasset.ErrInvalidState)
	}
	var commit RcloneCommitV1
	switch attempt.PublicationMode {
	case backupasset.PublicationVersionedPrefix:
		commit, err = strategy.portable.Publish(ctx, *prepared.RcloneInput.PortableRequest)
	case backupasset.PublicationNativeObjectVersions:
		commit, err = strategy.native.Publish(ctx, *prepared.RcloneInput.NativeRequest)
	default:
		err = fmt.Errorf("%w: unsupported Rclone publication variant", backupasset.ErrInvalidState)
	}
	if err != nil {
		fact := ClassifyRclonePublicationFailure(err)
		result := ProviderExecutionResult{ExitCode: UnknownProviderExitCode}
		if fact.KnownNonzero {
			result.ExitCode = fact.ExitCode
			result.Completion = backupasset.CompletionKnownNonzero
		} else if !fact.Unattempted && !fact.CompletionUnproven {
			result.Completion = backupasset.CompletionOutcomeUnknown
		}
		return result, err
	}
	tagged := NewRcloneProviderCommit(commit)
	return ProviderExecutionResult{ExitCode: 0, Completion: backupasset.CompletionKnownExitZero, ProviderCommit: &tagged}, nil
}

func (*RclonePublicationStrategy) RecordCommit(_ context.Context, prepared PreparedPublication, result ProviderExecutionResult) (ProviderCommit, error) {
	if prepared.RcloneInput != nil && prepared.RcloneInput.PortableRequest != nil &&
		!prepared.RcloneInput.PortableRequest.ReadDeadline.IsZero() {
		return ProviderCommit{}, fmt.Errorf("%w: portable read request cannot record a publication", backupasset.ErrInvalidState)
	}
	if prepared.Attempt.Provider != backupasset.ProviderRclone || result.ExitCode != 0 || result.Completion != backupasset.CompletionKnownExitZero ||
		result.ProviderCommit == nil || result.EvidenceCode != "" || result.ProviderCommit.Provider != backupasset.ProviderRclone {
		return ProviderCommit{}, fmt.Errorf("%w: invalid Rclone execution result", backupasset.ErrInvalidState)
	}
	if err := result.ProviderCommit.Validate(); err != nil {
		return ProviderCommit{}, err
	}
	attempt, err := prepared.Attempt.RcloneAttempt()
	if err != nil {
		return ProviderCommit{}, err
	}
	commit, err := result.ProviderCommit.RcloneCommit()
	if err != nil || ValidateRcloneRuntimeCompatibility(attempt, commit) != nil {
		return ProviderCommit{}, fmt.Errorf("%w: incompatible Rclone execution evidence", backupasset.ErrInvalidState)
	}
	return *result.ProviderCommit, nil
}

func (strategy *RclonePublicationStrategy) VerifyOrBuildManifest(ctx context.Context, prepared PreparedPublication, tagged ProviderCommit, _ ManifestLimits) (ManifestResult, error) {
	if prepared.RcloneInput != nil && prepared.RcloneInput.PortableRequest != nil &&
		!prepared.RcloneInput.PortableRequest.ReadDeadline.IsZero() {
		return ManifestResult{}, fmt.Errorf("%w: portable read request cannot verify a publication", backupasset.ErrInvalidState)
	}
	if strategy == nil || strategy.portable == nil || prepared.RcloneInput == nil {
		return ManifestResult{}, fmt.Errorf("%w: Rclone manifest request unavailable", backupasset.ErrInvalidState)
	}
	attempt, err := prepared.Attempt.RcloneAttempt()
	if err != nil || !prepared.RcloneInput.validateVariant(attempt.PublicationMode) {
		return ManifestResult{}, fmt.Errorf("%w: invalid Rclone manifest attempt", backupasset.ErrInvalidState)
	}
	commit, err := tagged.RcloneCommit()
	if err != nil || ValidateRcloneRuntimeCompatibility(attempt, commit) != nil {
		return ManifestResult{}, fmt.Errorf("%w: incompatible Rclone manifest evidence", backupasset.ErrInvalidState)
	}
	if prepared.RcloneInput.PortableRequest != nil {
		request := *prepared.RcloneInput.PortableRequest
		preparedAttempt, encodeErr := EncodePublicationAttempt(NewRclonePublicationAttempt(attempt))
		requestAttempt, requestErr := EncodePublicationAttempt(NewRclonePublicationAttempt(request.Attempt))
		if encodeErr != nil || requestErr != nil || preparedAttempt != requestAttempt {
			return ManifestResult{}, fmt.Errorf("%w: Rclone manifest attempt mismatch", backupasset.ErrInvalidState)
		}
		bundle := request.Manifest
		if bundle.Version != 1 || commit.ManifestEntryCount != bundle.EntryCount || commit.LogicalBytes != bundle.LogicalBytes ||
			commit.SourceObservationDigest != bundle.ObservationDigest {
			return ManifestResult{}, ErrRcloneManifestObservationMismatch
		}
		actual, err := strategy.portable.Reconcile(ctx, request)
		if err != nil {
			return ManifestResult{}, err
		}
		if !equalRcloneCatalogCommit(actual, commit) {
			return ManifestResult{}, ErrRcloneMarkerMismatch
		}
	} else if prepared.RcloneInput.NativeRequest == nil || commit.PublicationMode != backupasset.PublicationNativeObjectVersions ||
		commit.ManifestEntryCount != prepared.RcloneInput.NativeRequest.Manifest.EntryCount ||
		commit.LogicalBytes != prepared.RcloneInput.NativeRequest.Manifest.LogicalBytes {
		return ManifestResult{}, ErrRcloneManifestObservationMismatch
	}
	manifest := RcloneManifestV1{
		ManifestIndexDigest: commit.ManifestIndexDigest, ManifestChunkDigests: append([]string(nil), commit.ManifestChunkDigests...),
		EntryCount: commit.ManifestEntryCount, LogicalBytes: commit.LogicalBytes, FidelityEvidenceDigest: commit.FidelityEvidenceDigest,
	}
	if err := manifest.Validate(); err != nil {
		return ManifestResult{}, err
	}
	return ManifestResult{Provider: backupasset.ProviderRclone, Version: taggedPublicationSchemaV1, Rclone: &manifest}, nil
}

func (strategy *RclonePublicationStrategy) Reconcile(ctx context.Context, request PublicationReconcileRequest) (PublicationReconcileResult, error) {
	if request.RcloneInput != nil && request.RcloneInput.PortableRequest != nil &&
		!request.RcloneInput.PortableRequest.ReadDeadline.IsZero() {
		return PublicationReconcileResult{}, fmt.Errorf("%w: portable read request cannot reconcile publication state", backupasset.ErrInvalidState)
	}
	if strategy == nil || strategy.portable == nil || strategy.native == nil || request.Attempt.Provider != backupasset.ProviderRclone || request.RcloneInput == nil {
		return PublicationReconcileResult{}, fmt.Errorf("%w: invalid Rclone reconciliation request", backupasset.ErrInvalidState)
	}
	attempt, err := request.Attempt.RcloneAttempt()
	if err != nil || !request.RcloneInput.validateVariant(attempt.PublicationMode) {
		return PublicationReconcileResult{}, fmt.Errorf("%w: invalid Rclone reconciliation request", backupasset.ErrInvalidState)
	}
	var variantAttempt RcloneAttemptV1
	if request.RcloneInput.PortableRequest != nil {
		variantAttempt = request.RcloneInput.PortableRequest.Attempt
	} else {
		variantAttempt = request.RcloneInput.NativeRequest.Attempt
	}
	expectedAttempt, err := EncodePublicationAttempt(NewRclonePublicationAttempt(attempt))
	actualAttempt, variantErr := EncodePublicationAttempt(NewRclonePublicationAttempt(variantAttempt))
	if err != nil || variantErr != nil || expectedAttempt != actualAttempt {
		return PublicationReconcileResult{}, fmt.Errorf("%w: Rclone reconciliation attempt mismatch", backupasset.ErrInvalidState)
	}
	var commit RcloneCommitV1
	var entryCount, logicalBytes uint64
	switch attempt.PublicationMode {
	case backupasset.PublicationVersionedPrefix:
		commit, err = strategy.portable.Reconcile(ctx, *request.RcloneInput.PortableRequest)
	case backupasset.PublicationNativeObjectVersions:
		commit, err = strategy.native.Reconcile(ctx, *request.RcloneInput.NativeRequest)
	default:
		err = fmt.Errorf("%w: unsupported Rclone reconciliation variant", backupasset.ErrInvalidState)
	}
	if err != nil {
		return PublicationReconcileResult{}, err
	}
	if err := ValidateRcloneRuntimeCompatibility(attempt, commit); err != nil {
		return PublicationReconcileResult{}, err
	}
	entryCount = commit.ManifestEntryCount
	logicalBytes = commit.LogicalBytes
	manifest := &RcloneManifestV1{
		ManifestIndexDigest: commit.ManifestIndexDigest, ManifestChunkDigests: append([]string(nil), commit.ManifestChunkDigests...),
		EntryCount: entryCount, LogicalBytes: logicalBytes, FidelityEvidenceDigest: commit.FidelityEvidenceDigest,
	}
	reconcile := &RcloneReconcileV1{State: RcloneReconcileProviderCommitted, Commit: &commit, Manifest: manifest}
	if err := reconcile.Validate(); err != nil {
		return PublicationReconcileResult{}, err
	}
	return PublicationReconcileResult{Rclone: reconcile}, nil
}

func cloneRclonePortablePublicationRequest(value RclonePortablePublicationRequest) RclonePortablePublicationRequest {
	value.MarkerKey = append([]byte(nil), value.MarkerKey...)
	value.Manifest.IndexEncoded = append([]byte(nil), value.Manifest.IndexEncoded...)
	value.Manifest.Chunks = append([]RcloneManifestChunk(nil), value.Manifest.Chunks...)
	for index := range value.Manifest.Chunks {
		value.Manifest.Chunks[index].Encoded = append([]byte(nil), value.Manifest.Chunks[index].Encoded...)
	}
	if value.CopyDest != nil {
		copy := *value.CopyDest
		value.CopyDest = &copy
	}
	return value
}

func cloneRcloneNativePublicationRequest(value RcloneNativePublicationRequest) RcloneNativePublicationRequest {
	value.RcloneConfig = append([]byte(nil), value.RcloneConfig...)
	value.MarkerKey = append([]byte(nil), value.MarkerKey...)
	value.KMSKeyBindings = append([]RcloneNativeKMSKeyDigestBinding(nil), value.KMSKeyBindings...)
	value.Encryption.RetainedReadKeyARNs = append([]string(nil), value.Encryption.RetainedReadKeyARNs...)
	value.Manifest.IndexEncoded = append([]byte(nil), value.Manifest.IndexEncoded...)
	value.Manifest.Chunks = append([]RcloneManifestChunk(nil), value.Manifest.Chunks...)
	for index := range value.Manifest.Chunks {
		value.Manifest.Chunks[index].Encoded = append([]byte(nil), value.Manifest.Chunks[index].Encoded...)
	}
	return value
}

func NewRclonePortablePublisher(remote RclonePortableRemote, sleep func(time.Duration), now func() time.Time) *RclonePortablePublisher {
	if sleep == nil {
		sleep = time.Sleep
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &RclonePortablePublisher{remote: remote, sleep: sleep, now: now}
}

func (publisher *RclonePortablePublisher) Publish(ctx context.Context, request RclonePortablePublicationRequest) (result RcloneCommitV1, resultErr error) {
	tracker := newRclonePublicationTracker()
	defer func() { resultErr = tracker.failure(resultErr) }()
	if !request.ReadDeadline.IsZero() {
		return RcloneCommitV1{}, fmt.Errorf("%w: portable read request cannot publish", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if publisher == nil || publisher.remote == nil || publisher.sleep == nil || publisher.now == nil {
		return RcloneCommitV1{}, fmt.Errorf("%w: portable publisher unavailable", backupasset.ErrInvalidState)
	}
	if err := request.validate(publisher.now().UTC()); err != nil {
		return RcloneCommitV1{}, err
	}
	presence, err := publisher.remote.ProbeAttempt(ctx, request)
	if err != nil {
		return RcloneCommitV1{}, errors.Join(ErrRcloneAttemptPresenceUnknown, err)
	}
	switch presence {
	case RcloneAttemptAbsent:
	case RcloneAttemptPresent:
		return RcloneCommitV1{}, ErrRcloneAttemptCollision
	default:
		return RcloneCommitV1{}, ErrRcloneAttemptPresenceUnknown
	}

	sourceBefore, err := publisher.remote.Observe(ctx, request, request.Source)
	if err != nil {
		return RcloneCommitV1{}, err
	}
	if !equalRcloneManifestBundleIdentity(sourceBefore, request.Manifest) {
		return RcloneCommitV1{}, ErrRcloneManifestObservationMismatch
	}
	attemptDocument, err := json.Marshal(rcloneAttemptToWire(request.Attempt))
	if err != nil {
		return RcloneCommitV1{}, fmt.Errorf("encode portable attempt marker: %w", err)
	}
	attemptPayload, _, _, err := encodeRcloneAuthenticatedControl("attempt", attemptDocument, request.MarkerKey)
	if err != nil {
		return RcloneCommitV1{}, err
	}
	if err := publisher.putAndReadback(ctx, request, "attempt.json", attemptPayload, false, tracker); err != nil {
		return RcloneCommitV1{}, err
	}

	var copyDest *RclonePrivateLocator
	if request.ParentLeaseEligible && request.CopyDest != nil {
		copy := *request.CopyDest
		copyDest = &copy
	}
	if err := publisher.remote.TransferData(ctx, request, copyDest); err != nil {
		tracker.mutation(err, false)
		return RcloneCommitV1{}, err
	}
	tracker.mutation(nil, false)
	sourceAfter, err := publisher.remote.Observe(ctx, request, request.Source)
	if err != nil {
		return RcloneCommitV1{}, err
	}
	destinationFirst, err := publisher.remote.Observe(ctx, request, request.DataRoot)
	if err != nil {
		return RcloneCommitV1{}, err
	}
	if err := ctx.Err(); err != nil {
		return RcloneCommitV1{}, err
	}
	publisher.sleep(request.SettleInterval)
	if err := ctx.Err(); err != nil {
		return RcloneCommitV1{}, err
	}
	destinationSecond, err := publisher.remote.Observe(ctx, request, request.DataRoot)
	if err != nil {
		return RcloneCommitV1{}, err
	}
	if err := ValidateRclonePortableObservations(
		rcloneObservationFromManifest(sourceBefore), rcloneObservationFromManifest(sourceAfter),
		rcloneObservationFromManifest(destinationFirst), rcloneObservationFromManifest(destinationSecond),
	); err != nil {
		return RcloneCommitV1{}, err
	}

	manifest, err := buildRclonePortableManifestV2(ctx, sourceAfter, destinationSecond, request.ManifestOptions)
	if err != nil {
		return RcloneCommitV1{}, err
	}
	verification := RclonePortableByteVerification{}
	if manifest.Fidelity.RequiresFullByteVerification {
		if manifest.LogicalBytes > uint64(request.FullVerifyMaxBytes) {
			return RcloneCommitV1{}, ErrRcloneManifestFullByteProofRequired
		}
		verification, err = publisher.remote.VerifyFullBytes(ctx, request, manifest.LogicalBytes)
		if err != nil {
			return RcloneCommitV1{}, err
		}
		if !verification.Complete || verification.VerifiedBytes != manifest.LogicalBytes {
			return RcloneCommitV1{}, ErrRcloneManifestFullByteProofRequired
		}
		sourceChecked, err := publisher.remote.Observe(ctx, request, request.Source)
		if err != nil {
			return RcloneCommitV1{}, err
		}
		destinationChecked, err := publisher.remote.Observe(ctx, request, request.DataRoot)
		if err != nil {
			return RcloneCommitV1{}, err
		}
		if rcloneObservationFromManifest(sourceChecked) != rcloneObservationFromManifest(sourceAfter) {
			return RcloneCommitV1{}, ErrRcloneSourceDrift
		}
		if rcloneObservationFromManifest(destinationChecked) != rcloneObservationFromManifest(destinationSecond) {
			return RcloneCommitV1{}, ErrRcloneDestinationUnstable
		}
	}
	manifest, err = completeRclonePortableManifestV2(manifest, verification, request.ManifestOptions)
	if err != nil {
		return RcloneCommitV1{}, err
	}

	chunkDigests := make([]string, len(manifest.Chunks))
	for index, chunk := range manifest.Chunks {
		name := fmt.Sprintf("manifest-%06d.jsonl", index)
		if err := publisher.putAndReadback(ctx, request, name, chunk.Encoded, false, tracker); err != nil {
			return RcloneCommitV1{}, err
		}
		chunkDigests[index] = chunk.Digest
	}
	if err := publisher.putAndReadback(ctx, request, "manifest-index.json", manifest.IndexEncoded, false, tracker); err != nil {
		return RcloneCommitV1{}, err
	}

	commit, err := buildRclonePortableCommit(request, manifest, attemptPayload, chunkDigests, publisher.now().UTC())
	if err != nil {
		return RcloneCommitV1{}, err
	}
	markerPayload, commitPayloadDigest, commitAuthenticationDigest, err := encodeRcloneCommitMarker(commit, request.MarkerKey)
	if err != nil {
		return RcloneCommitV1{}, err
	}
	commit.Portable.CommitPayloadDigest = commitPayloadDigest
	commit.Portable.CommitAuthenticationDigest = commitAuthenticationDigest
	if err := ValidateRcloneRuntimeCompatibility(request.Attempt, commit); err != nil {
		return RcloneCommitV1{}, err
	}
	if err := publisher.putAndReadback(ctx, request, "commit.json", markerPayload, true, tracker); err != nil {
		return RcloneCommitV1{}, err
	}
	return commit, nil
}

func (publisher *RclonePortablePublisher) putAndReadback(ctx context.Context, request RclonePortablePublicationRequest, name string, payload []byte, commit bool, tracker *rclonePublicationTracker) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	err := publisher.remote.PutControl(ctx, request, name, payload)
	tracker.mutation(err, commit)
	if err != nil {
		return err
	}
	readback, err := publisher.remote.ReadControl(ctx, request, name, int64(len(payload)))
	if err != nil || !bytes.Equal(readback, payload) {
		if commit {
			return errors.Join(ErrRcloneCommitOutcomeUnknown, err)
		}
		return errors.Join(ErrRcloneMarkerMismatch, err)
	}
	return nil
}

func (publisher *RclonePortablePublisher) Reconcile(ctx context.Context, request RclonePortablePublicationRequest) (RcloneCommitV1, error) {
	if !request.ReadDeadline.IsZero() {
		return RcloneCommitV1{}, fmt.Errorf("%w: portable read request cannot reconcile publication state", backupasset.ErrInvalidState)
	}
	return publisher.readPublished(ctx, request, nil, nil)
}

func (publisher *RclonePortablePublisher) readPublished(
	ctx context.Context,
	request RclonePortablePublicationRequest,
	expected *RcloneCommitV1,
	visit func(rcloneCanonicalManifestEntry, rcloneCanonicalManifestEntry) error,
) (RcloneCommitV1, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	commit, err := publisher.readCommit(ctx, request, expected)
	if err != nil {
		return RcloneCommitV1{}, err
	}
	if !emptyRcloneManifestBundle(request.Manifest) &&
		(request.Manifest.Version != 1 || commit.ManifestEntryCount != request.Manifest.EntryCount ||
			commit.LogicalBytes != request.Manifest.LogicalBytes || commit.SourceObservationDigest != request.Manifest.ObservationDigest) {
		return RcloneCommitV1{}, ErrRcloneMarkerMismatch
	}
	if _, err := readRclonePortablePublishedManifest(ctx, publisher.remote, request, commit, visit); err != nil {
		return RcloneCommitV1{}, err
	}
	return commit, nil
}

func (publisher *RclonePortablePublisher) readCommit(
	ctx context.Context, request RclonePortablePublicationRequest, expected *RcloneCommitV1,
) (RcloneCommitV1, error) {
	if publisher == nil || publisher.remote == nil || len(request.MarkerKey) < 32 || request.ControlPayloadMaxBytes <= 0 ||
		request.Attempt.Validate() != nil || request.Attempt.Portable == nil ||
		!request.AttemptRoot.valid() || !request.DataRoot.valid() || !request.ControlRoot.valid() ||
		request.DataRoot.value != request.AttemptRoot.value+"/data" || request.ControlRoot.value != request.AttemptRoot.value+"/control" {
		return RcloneCommitV1{}, fmt.Errorf("%w: portable reconciler unavailable", backupasset.ErrInvalidState)
	}
	if err := validatePortableReadDeadline(ctx, request); err != nil {
		return RcloneCommitV1{}, err
	}
	payload, err := publisher.remote.ReadControl(ctx, request, "commit.json", min(int64(8<<20), request.ControlPayloadMaxBytes))
	if err != nil {
		return RcloneCommitV1{}, err
	}
	commit, err := decodeRcloneCommitMarker(payload, request.MarkerKey)
	if err != nil || validatePortableCommitIdentity(request, commit) != nil ||
		(expected != nil && !equalRcloneCatalogCommit(commit, *expected)) {
		return RcloneCommitV1{}, ErrRcloneMarkerMismatch
	}
	return commit, nil
}

// validatePortableExpectedIdentity authenticates the expected commit before a
// caller is allowed to create a transient historical read deadline.
func validatePortableExpectedIdentity(request RclonePortablePublicationRequest, commit RcloneCommitV1) error {
	if err := validatePortableCommitIdentity(request, commit); err != nil {
		return err
	}
	_, digest, authentication, err := encodeRcloneCommitMarker(commit, request.MarkerKey)
	if err != nil || digest != commit.Portable.CommitPayloadDigest ||
		!hmac.Equal([]byte(authentication), []byte(commit.Portable.CommitAuthenticationDigest)) {
		return ErrRcloneMarkerMismatch
	}
	return nil
}

func validatePortableCommitIdentity(request RclonePortablePublicationRequest, commit RcloneCommitV1) error {
	if len(request.MarkerKey) < 32 || request.ControlPayloadMaxBytes <= 0 || request.Attempt.Portable == nil ||
		!request.AttemptRoot.valid() || !request.DataRoot.valid() || !request.ControlRoot.valid() ||
		request.DataRoot.value != request.AttemptRoot.value+"/data" || request.ControlRoot.value != request.AttemptRoot.value+"/control" ||
		ValidateRcloneRuntimeCompatibility(request.Attempt, commit) != nil || commit.Portable == nil ||
		commit.RepositoryID != request.Attempt.RepositoryID || commit.TaskRepositoryLinkID != request.Attempt.TaskRepositoryLinkID ||
		commit.RecoveryPointID != request.Attempt.RecoveryPointID || commit.AttemptID != request.Attempt.AttemptID ||
		commit.PublicationMode != request.Attempt.PublicationMode || !commit.PointDeadlineAt.Equal(request.Attempt.PointDeadlineAt) ||
		commit.ChildFenceDigest != request.Attempt.ChildFenceDigest ||
		commit.Portable.AttemptMarkerDigest != request.Attempt.Portable.AttemptMarkerDigest ||
		commit.Portable.ControlIdentityDigest != keyedPrivateLocatorDigest(request.MarkerKey, request.ControlRoot.value) ||
		commit.Portable.DataIdentityDigest != keyedPrivateLocatorDigest(request.MarkerKey, request.DataRoot.value) {
		return ErrRcloneMarkerMismatch
	}
	attemptDocument, err := json.Marshal(rcloneAttemptToWire(request.Attempt))
	if err != nil {
		return err
	}
	attemptPayload, _, _, err := encodeRcloneAuthenticatedControl("attempt", attemptDocument, request.MarkerKey)
	if err != nil || sha256Hex(attemptPayload) != commit.Portable.AttemptIdentityDigest {
		return ErrRcloneMarkerMismatch
	}
	return nil
}

func validatePortableReadDeadline(ctx context.Context, request RclonePortablePublicationRequest) error {
	if request.ReadDeadline.IsZero() {
		return nil
	}
	if ctx == nil {
		return fmt.Errorf("%w: portable read context is unavailable", backupasset.ErrInvalidState)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validTaggedPublicationTime(request.ReadDeadline) || !request.ReadDeadline.After(time.Now().UTC()) {
		return context.DeadlineExceeded
	}
	return nil
}

// portableReadRequest creates no durable authority: its deadline applies only
// to authenticated committed-point reads and is bounded by the caller.
func portableReadRequest(ctx context.Context, request RclonePortablePublicationRequest) (RclonePortablePublicationRequest, error) {
	if ctx == nil || !request.ReadDeadline.IsZero() || request.ManifestOptions.Limits.Timeout <= 0 {
		return RclonePortablePublicationRequest{}, fmt.Errorf("%w: invalid portable historical read request", backupasset.ErrInvalidState)
	}
	if err := ctx.Err(); err != nil {
		return RclonePortablePublicationRequest{}, err
	}
	now := time.Now().UTC()
	deadline := now.Add(request.ManifestOptions.Limits.Timeout)
	if callerDeadline, ok := ctx.Deadline(); ok && callerDeadline.Before(deadline) {
		deadline = callerDeadline.UTC()
	}
	if !deadline.After(now) {
		return RclonePortablePublicationRequest{}, context.DeadlineExceeded
	}
	request.ReadDeadline = deadline
	request.Source = RclonePrivateLocator{}
	request.CopyDest = nil
	request.ParentLeaseEligible = false
	request.Manifest = RcloneManifestBundle{}
	request.ManifestOptions.SymlinkTargetReader = nil
	return request, nil
}

func (request RclonePortablePublicationRequest) validate(now time.Time) error {
	if !request.ReadDeadline.IsZero() {
		return fmt.Errorf("%w: portable read request cannot publish", backupasset.ErrInvalidState)
	}
	if err := request.Attempt.Validate(); err != nil || request.Attempt.PublicationMode != backupasset.PublicationVersionedPrefix ||
		request.Attempt.MinimumRuntimeRevision != 2 || request.Attempt.ManifestSchemaRevision != 2 ||
		request.Attempt.Portable == nil || !request.Source.valid() || !request.AttemptRoot.valid() || !request.DataRoot.valid() || !request.ControlRoot.valid() ||
		request.DataRoot.value != request.AttemptRoot.value+"/data" || request.ControlRoot.value != request.AttemptRoot.value+"/control" ||
		len(request.MarkerKey) < 32 || request.BoundConfig.KeyedDigest() == "" || request.BoundConfig.ClassificationRevision() != rcloneBackendClassificationRevision ||
		request.Manifest.Version != 1 || request.Manifest.IndexDigest == "" || !validTaggedDigest(request.CapabilityEvidenceDigest) ||
		!validTaggedDigest(request.CostEvidenceDigest) || request.SettleInterval <= 0 || request.FullVerifyMaxBytes <= 0 ||
		request.ControlPayloadMaxBytes <= 0 || request.LowLevelRetries < 1 || request.LowLevelRetries > 10 || request.Runtime.Node.ID == 0 ||
		!request.Attempt.PointDeadlineAt.After(now) {
		return fmt.Errorf("%w: invalid portable publication request", backupasset.ErrInvalidState)
	}
	if request.CopyDest != nil && (!request.CopyDest.valid() || request.CopyDest.value == request.DataRoot.value || strings.HasPrefix(request.CopyDest.value, request.DataRoot.value+"/")) {
		return fmt.Errorf("%w: invalid portable copy-dest", backupasset.ErrInvalidState)
	}
	return nil
}

func buildRclonePortableCommit(
	request RclonePortablePublicationRequest,
	manifest RclonePortableManifestV2,
	attemptPayload []byte,
	chunkDigests []string,
	committedAt time.Time,
) (RcloneCommitV1, error) {
	proofDocument, err := json.Marshal(manifest.ContentProof)
	if err != nil {
		return RcloneCommitV1{}, err
	}
	consistencyDocument, err := json.Marshal(struct {
		Source      string `json:"source"`
		Destination string `json:"destination"`
		Class       string `json:"class"`
	}{manifest.SourceObservationDigest, manifest.DestinationObservationDigest, string(backupasset.RcloneConsistencyObservationallyStable)})
	if err != nil {
		return RcloneCommitV1{}, err
	}
	fidelityDocument, err := json.Marshal(manifest.Fidelity)
	if err != nil {
		return RcloneCommitV1{}, err
	}
	portable := &RclonePortableCommitV1{
		AttemptIdentityDigest: sha256Hex(attemptPayload),
		ControlIdentityDigest: keyedPrivateLocatorDigest(request.MarkerKey, request.ControlRoot.value),
		DataIdentityDigest:    keyedPrivateLocatorDigest(request.MarkerKey, request.DataRoot.value),
		AttemptMarkerDigest:   request.Attempt.Portable.AttemptMarkerDigest,
		CommitComponent:       "commit.json", ConsistencyEvidenceDigest: sha256Hex(consistencyDocument),
		HashEvidenceDigest: sha256Hex(proofDocument), DownloadVerifiedBytes: manifest.ContentProof.VerifiedBytes,
	}
	if copyDest := request.Attempt.Portable.CopyDest; copyDest != nil {
		portable.ParentRecoveryPointID = copyDest.ParentRecoveryPointID
		portable.ParentCommitDigest = copyDest.ParentCommitDigest
		portable.ParentManifestDigest = copyDest.ParentManifestDigest
	}
	return RcloneCommitV1{
		SchemaVersion: 1, LayoutVersion: request.Attempt.LayoutVersion, MinimumRuntimeRevision: request.Attempt.MinimumRuntimeRevision,
		RepositoryID: request.Attempt.RepositoryID, TaskRepositoryLinkID: request.Attempt.TaskRepositoryLinkID,
		RecoveryPointID: request.Attempt.RecoveryPointID, AttemptID: request.Attempt.AttemptID,
		PublicationMode: request.Attempt.PublicationMode, PointDeadlineAt: request.Attempt.PointDeadlineAt,
		ProviderCommittedAt: committedAt, ManifestIndexDigest: manifest.IndexDigest,
		ManifestChunkDigests: append([]string(nil), chunkDigests...), ManifestEntryCount: manifest.EntryCount,
		LogicalBytes: manifest.LogicalBytes, SourceObservationDigest: manifest.SourceObservationDigest,
		DestinationObservationDigest: manifest.DestinationObservationDigest, ContentProofDigest: sha256Hex(proofDocument),
		FidelityEvidenceDigest: sha256Hex(fidelityDocument), CostEvidenceDigest: request.CostEvidenceDigest,
		CapabilityEvidenceDigest: request.CapabilityEvidenceDigest, ChildFenceDigest: request.Attempt.ChildFenceDigest,
		Portable: portable,
	}, nil
}

type rcloneAuthenticatedControlV1 struct {
	Version        int             `json:"version"`
	Kind           string          `json:"kind"`
	Document       json.RawMessage `json:"document"`
	Authentication string          `json:"authentication"`
}

func encodeRcloneAuthenticatedControl(kind string, document, key []byte) ([]byte, string, string, error) {
	if (kind != "attempt" && kind != "commit") || len(document) == 0 || len(key) < 32 {
		return nil, "", "", fmt.Errorf("%w: invalid authenticated Rclone control", backupasset.ErrInvalidState)
	}
	mac := hmac.New(sha256.New, key)
	_, _ = io.WriteString(mac, "xirang-rclone-control-v1\n"+kind+"\n")
	_, _ = mac.Write(document)
	authentication := hex.EncodeToString(mac.Sum(nil))
	payload, err := json.Marshal(rcloneAuthenticatedControlV1{Version: 1, Kind: kind, Document: document, Authentication: authentication})
	if err != nil {
		return nil, "", "", err
	}
	return payload, sha256Hex(document), authentication, nil
}

func encodeRcloneCommitMarker(commit RcloneCommitV1, key []byte) ([]byte, string, string, error) {
	copy := commit
	if copy.Portable == nil {
		return nil, "", "", fmt.Errorf("%w: portable commit required", backupasset.ErrInvalidState)
	}
	copy.Portable = cloneRclonePortableCommit(copy.Portable)
	copy.Portable.CommitPayloadDigest = ""
	copy.Portable.CommitAuthenticationDigest = ""
	document, err := json.Marshal(rcloneCommitToWire(copy))
	if err != nil {
		return nil, "", "", err
	}
	return encodeRcloneAuthenticatedControl("commit", document, key)
}

func decodeRcloneCommitMarker(payload, key []byte) (RcloneCommitV1, error) {
	if len(payload) == 0 || len(payload) > 8<<20 || rejectDuplicateJSONMembers(string(payload)) != nil {
		return RcloneCommitV1{}, ErrRcloneMarkerMismatch
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var envelope rcloneAuthenticatedControlV1
	if err := decoder.Decode(&envelope); err != nil || envelope.Version != 1 || envelope.Kind != "commit" || !lowerHex(envelope.Authentication, 64) {
		return RcloneCommitV1{}, ErrRcloneMarkerMismatch
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return RcloneCommitV1{}, ErrRcloneMarkerMismatch
	}
	_, digest, authentication, err := encodeRcloneAuthenticatedControl("commit", envelope.Document, key)
	if err != nil || !hmac.Equal([]byte(authentication), []byte(envelope.Authentication)) {
		return RcloneCommitV1{}, ErrRcloneMarkerMismatch
	}
	documentDecoder := json.NewDecoder(bytes.NewReader(envelope.Document))
	documentDecoder.DisallowUnknownFields()
	var wire rcloneCommitWireV1
	if err := documentDecoder.Decode(&wire); err != nil {
		return RcloneCommitV1{}, ErrRcloneMarkerMismatch
	}
	commit := rcloneCommitFromWire(&wire)
	if commit == nil || commit.Portable == nil {
		return RcloneCommitV1{}, ErrRcloneMarkerMismatch
	}
	commit.Portable.CommitPayloadDigest = digest
	commit.Portable.CommitAuthenticationDigest = authentication
	if err := commit.Validate(); err != nil {
		return RcloneCommitV1{}, ErrRcloneMarkerMismatch
	}
	return *commit, nil
}

func cloneRclonePortableCommit(value *RclonePortableCommitV1) *RclonePortableCommitV1 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func keyedPrivateLocatorDigest(key []byte, locator string) string {
	mac := hmac.New(sha256.New, key)
	_, _ = io.WriteString(mac, "xirang-rclone-private-locator-v1\n")
	_, _ = io.WriteString(mac, locator)
	return hex.EncodeToString(mac.Sum(nil))
}

func rcloneObservationFromManifest(manifest RcloneManifestBundle) RcloneObservationV1 {
	return RcloneObservationV1{Digest: manifest.ObservationDigest, EntryCount: manifest.EntryCount, LogicalBytes: manifest.LogicalBytes}
}

func NewRclonePortableAttemptID(reader io.Reader) (string, error) {
	if reader == nil {
		reader = rand.Reader
	}
	entropy := make([]byte, 16)
	if _, err := io.ReadFull(reader, entropy); err != nil {
		return "", fmt.Errorf("generate Rclone attempt ID: %w", err)
	}
	return hex.EncodeToString(entropy), nil
}

func NewRclonePortableRetryAttempt(previous RcloneAttemptV1, reader io.Reader) (RcloneAttemptV1, error) {
	if err := previous.Validate(); err != nil || previous.PublicationMode != backupasset.PublicationVersionedPrefix || previous.Portable == nil {
		return RcloneAttemptV1{}, fmt.Errorf("%w: invalid prior portable attempt", backupasset.ErrInvalidState)
	}
	attemptID, err := NewRclonePortableAttemptID(reader)
	if err != nil {
		return RcloneAttemptV1{}, err
	}
	next := previous
	next.AttemptID = attemptID
	portable := *previous.Portable
	portable.AttemptComponent = next.RecoveryPointID + "." + attemptID
	reservation := sha256.Sum256([]byte("xirang-rclone-retry-marker-v1\n" + next.RecoveryPointID + "\n" + attemptID))
	portable.AttemptMarkerDigest = hex.EncodeToString(reservation[:])
	next.Portable = &portable
	return next, nil
}
