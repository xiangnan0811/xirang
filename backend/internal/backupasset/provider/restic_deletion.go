package provider

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"xirang/backend/internal/backupasset"
)

// resticForgetExactMaxStdoutBytes is the closed forget/prune receipt bound.
// Forget output is a small confirmation, not a catalog stream; the bound stays
// far below MaxMetadataBytes so oversized provider stdout is cancelled.
const resticForgetExactMaxStdoutBytes int64 = 2048

type ResticPointDeleter struct {
	transport       CommandTransport
	streamTransport CommandStreamTransport
	limitsSource    OperationLimitsSource
	now             func() time.Time
}

func NewResticPointDeleter(
	transport CommandTransport,
	streamTransport CommandStreamTransport,
	limitsSource OperationLimitsSource,
	now func() time.Time,
) (*ResticPointDeleter, error) {
	if transport == nil || streamTransport == nil {
		return nil, fmt.Errorf("%w: invalid Restic point deleter dependencies", backupasset.ErrInvalidState)
	}
	if _, err := resolveOperationLimits(limitsSource); err != nil {
		return nil, fmt.Errorf("%w: invalid Restic point deleter limits", backupasset.ErrInvalidState)
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &ResticPointDeleter{
		transport: transport, streamTransport: streamTransport, limitsSource: limitsSource, now: now,
	}, nil
}

func (deleter *ResticPointDeleter) ProviderKind() backupasset.ProviderKind {
	return backupasset.ProviderRestic
}

func (deleter *ResticPointDeleter) DeletePoint(ctx context.Context, request DeletePointRequest) (DeletePointResult, error) {
	if err := request.requireSourceRevision(); err != nil {
		return DeletePointResult{}, err
	}
	if request.Snapshot.Access.Provider != backupasset.ProviderRestic || !lowerHex(request.Point.Native, 64) ||
		request.Point.Native == "latest" || strings.Contains(request.Point.Native, ",") {
		return DeletePointResult{}, invalidDeletePointRequest("exact Restic snapshot ID required")
	}
	if err := deleter.validateAccess(request.Snapshot); err != nil {
		return DeletePointResult{}, err
	}
	limits, err := resolveOperationLimits(deleter.limitsSource)
	if err != nil {
		return DeletePointResult{}, err
	}
	if err := deleter.verifyLiveRepositoryIdentity(ctx, request.Snapshot, limits); err != nil {
		return DeletePointResult{}, err
	}
	present, err := deleter.snapshotPresent(ctx, request.Snapshot.Access, request.Point.Native, limits)
	if err != nil {
		return DeletePointResult{}, err
	}
	if !present {
		return deleter.receipt(DeletePointAlreadyAbsent, request)
	}
	if err := deleter.forgetExact(ctx, request.Snapshot.Access, request.Point.Native, limits); err != nil {
		return DeletePointResult{}, err
	}
	present, err = deleter.snapshotPresent(ctx, request.Snapshot.Access, request.Point.Native, limits)
	if err != nil {
		return DeletePointResult{}, err
	}
	if present {
		return DeletePointResult{}, fmt.Errorf("%w: exact Restic snapshot remained after forget", backupasset.ErrInvalidState)
	}
	return deleter.receipt(DeletePointDeleted, request)
}

func (deleter *ResticPointDeleter) validateAccess(snapshot ReadSnapshot) error {
	binding := snapshot.Access
	if deleter == nil || deleter.transport == nil || deleter.streamTransport == nil ||
		binding.Provider != backupasset.ProviderRestic || strings.TrimSpace(binding.Locator) == "" || len(binding.Secret) == 0 {
		return invalidDeletePointRequest("invalid Restic deletion access")
	}
	access, ok := binding.AdapterData.(ResticRuntimeAccess)
	if !ok || !lowerHex(access.NativeRepositoryID, 64) {
		return invalidDeletePointRequest("Restic native identity is unavailable")
	}
	if snapshot.RepositoryIdentity == "" {
		return ErrDeletePointIdentityConflict
	}
	return nil
}

func (deleter *ResticPointDeleter) verifyLiveRepositoryIdentity(ctx context.Context, snapshot ReadSnapshot, limits OperationLimits) error {
	access, ok := snapshot.Access.AdapterData.(ResticRuntimeAccess)
	if !ok || !lowerHex(access.NativeRepositoryID, 64) {
		return ErrDeletePointIdentityConflict
	}
	expectedBinding, err := NativeRepositoryIdentity(backupasset.ProviderRestic, access.NativeRepositoryID)
	if err != nil {
		return ErrDeletePointIdentityConflict
	}
	invocation := resticDeletionInvocation(snapshot.Access, OperationResticConfig, []string{
		"--password-file", "/dev/stdin", "cat", "config",
	}, CommandPurposeProbe)
	if err := invocation.Validate(); err != nil {
		return err
	}
	output, err := deleter.transport.Run(ctx, invocation, limits)
	if err != nil {
		return mapCommandTransportError(ctx, err)
	}
	var config struct {
		Version int    `json:"version"`
		ID      string `json:"id"`
	}
	if err := decodeSingleJSON(output.Stdout, &config); err != nil || config.Version <= 0 {
		return ErrDeletePointIdentityConflict
	}
	live, err := NativeRepositoryIdentity(backupasset.ProviderRestic, config.ID)
	if err != nil || live != snapshot.RepositoryIdentity || live != expectedBinding {
		return ErrDeletePointIdentityConflict
	}
	return nil
}

func (deleter *ResticPointDeleter) snapshotPresent(ctx context.Context, binding AccessBinding, snapshotID string, limits OperationLimits) (bool, error) {
	invocation := resticDeletionInvocation(binding, OperationResticSnapshots, []string{
		"--password-file", "/dev/stdin", "snapshots", "--json", "--", snapshotID,
	}, CommandPurposeList)
	if err := invocation.Validate(); err != nil {
		return false, err
	}
	output, err := deleter.transport.Run(ctx, invocation, limits)
	if err != nil {
		return false, mapCommandTransportError(ctx, err)
	}
	points, err := parseResticSnapshots(output.Stdout, limits)
	if err != nil {
		return false, err
	}
	for _, point := range points {
		if point.Locator.Native == snapshotID {
			return true, nil
		}
	}
	return false, nil
}

func (deleter *ResticPointDeleter) forgetExact(ctx context.Context, binding AccessBinding, snapshotID string, limits OperationLimits) error {
	invocation := resticDeletionInvocation(binding, OperationResticForgetExact, []string{
		"--password-file", "/dev/stdin", "forget", "--prune", "--", snapshotID,
	}, CommandPurposeDelete)
	if err := invocation.Validate(); err != nil {
		return err
	}
	bound := resticForgetExactBound(limits)
	execution, err := deleter.streamTransport.OpenExecution(ctx, invocation, limits, bound)
	if err != nil {
		return mapCommandTransportError(ctx, err)
	}
	if execution == nil {
		return fmt.Errorf("%w: nil Restic forget execution", backupasset.ErrProviderUnavailable)
	}
	if ctx != nil && ctx.Err() != nil {
		_ = execution.Cancel()
		_, _ = execution.Join()
		return ctx.Err()
	}
	limited := io.LimitReader(execution, bound+1)
	consumed, readErr := io.Copy(io.Discard, limited)
	if consumed > bound || readErr != nil {
		_ = execution.Cancel()
		_, _ = execution.Join()
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		return newCapabilityError(backupasset.CapabilityProviderResourceLimit)
	}
	_, joinErr := execution.Join()
	if joinErr != nil {
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		return mapCommandTransportError(ctx, joinErr)
	}
	return nil
}

func (deleter *ResticPointDeleter) receipt(outcome DeletePointOutcome, request DeletePointRequest) (DeletePointResult, error) {
	digest, err := deletionReceiptDigest(backupasset.ProviderRestic, outcome, request.OperationID, request.Point.Native)
	if err != nil {
		return DeletePointResult{}, err
	}
	return DeletePointResult{Outcome: outcome, ReceiptDigest: digest}, nil
}

func resticForgetExactBound(limits OperationLimits) int64 {
	bound := resticForgetExactMaxStdoutBytes
	if limits.MaxMetadataBytes > 0 && limits.MaxMetadataBytes < bound {
		bound = limits.MaxMetadataBytes
	}
	if int64(limits.MaxRecordBytes) > 0 && int64(limits.MaxRecordBytes) < bound {
		bound = int64(limits.MaxRecordBytes)
	}
	return bound
}

func resticDeletionInvocation(binding AccessBinding, operation CommandOperation, arguments []string, purpose CommandPurpose) CommandInvocation {
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

var _ PointDeleter = (*ResticPointDeleter)(nil)
