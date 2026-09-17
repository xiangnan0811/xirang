package retention

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// providerDeletePrepareProfile deliberately stays package-private. A prepared
// deletion contains provider credentials and opaque client capabilities and is
// therefore only allowed to cross the Coordinator-owned boundary.
type providerDeletePrepareProfile uint8

const (
	providerDeletePrepareObserver providerDeletePrepareProfile = iota + 1
	providerDeletePrepareExecution
)

type providerDeleteStage string

const (
	providerDeleteStagePreClaim     providerDeleteStage = "pre_claim"
	providerDeleteStageClaimObserve providerDeleteStage = "claim_observe"
	providerDeleteStageClaimAcquire providerDeleteStage = "claim_acquire"
	providerDeleteStageClaimRenew   providerDeleteStage = "claim_renew"
	providerDeleteStageProvider     providerDeleteStage = "provider_invoke"
	providerDeleteStageVerify       providerDeleteStage = "verify"
	providerDeleteStageReceipt      providerDeleteStage = "receipt_persist"
	providerDeleteStageAudit        providerDeleteStage = "audit_emit"
)

// errProviderDeletePreparationAuthority is returned after a blocking
// preparation step when the locked lifecycle authority is no longer current.
// It deliberately aborts Tx1 instead of converting a pre-claim race into a
// durable observational block.
var errProviderDeletePreparationAuthority = errors.New("provider-delete preparation authority changed")

// ErrEffectClaimInFlight is a pure loser observation. Callers must not turn it
// into a blocked lifecycle fact or update retry_at.
var ErrEffectClaimInFlight = errors.New("lifecycle provider-delete effect claim is in flight")

// preparedPointDeletion is process-local. Its String and GoString methods are
// intentionally closed representations: request contains private
// locator/config/secret and provider-owned runtime capabilities.
type preparedPointDeletion struct {
	request        provider.DeletePointRequest
	identity       provider.DeletionTargetIdentityInput
	identityDigest string
	deleter        provider.PointDeleter
	localExecute   func(context.Context) (provider.DeletePointResult, error)
}

func (preparedPointDeletion) String() string {
	return "[prepared provider deletion]"
}

func (preparedPointDeletion) GoString() string {
	return "[prepared provider deletion]"
}

type pointDeletionExecution struct {
	Result         PointDeletionResult
	ProviderCalled bool
	Stage          providerDeleteStage
}

// ProviderDeletePrepareProfile, PreparedPointDeletion, PointDeletionExecution,
// and LifecycleDeleteRows are exported aliases solely so runtime composition
// and black-box tests can provide a split provider-delete port without seeing
// private implementation details.
type ProviderDeletePrepareProfile = providerDeletePrepareProfile
type PreparedPointDeletion = preparedPointDeletion
type PointDeletionExecution = pointDeletionExecution
type LifecycleDeleteRows = lifecycleDeleteRows

type ProviderDeletePort interface {
	Prepare(context.Context, *gorm.DB, ProviderDeletePrepareProfile, LifecyclePointRequest, LifecycleDeleteRows) (PreparedPointDeletion, error)
	Execute(context.Context, PreparedPointDeletion) (PointDeletionExecution, error)
	Verify(context.Context, *gorm.DB, LifecyclePointRequest, PreparedPointDeletion, LifecycleDeleteRows) error
}

type providerDeletePort = ProviderDeletePort

func (adapter *RegistryPointDeletion) SetNow(now func() time.Time) {
	if adapter == nil || now == nil {
		return
	}
	adapter.now = now
}

func (adapter *RegistryPointDeletion) adapterNow() time.Time {
	if adapter != nil && adapter.now != nil {
		return adapter.now().UTC()
	}
	return time.Now().UTC()
}

type providerDeleteBoundContextValue struct {
	context.Context
	deadline time.Time
	timedOut atomic.Bool
}

func (ctx *providerDeleteBoundContextValue) Deadline() (time.Time, bool) {
	if ctx == nil {
		return time.Time{}, false
	}
	return ctx.deadline, true
}

func (ctx *providerDeleteBoundContextValue) Err() error {
	if ctx == nil {
		return context.Canceled
	}
	if ctx.timedOut.Load() {
		return context.DeadlineExceeded
	}
	return ctx.Context.Err()
}

// providerDeleteBoundContext composes the caller's cancellation with the
// lifecycle authority deadline. The injected clock is authoritative for
// deciding whether the lease is still usable. A logical deadline wrapper is
// used instead of context.WithDeadline so deterministic injected clocks that
// are not wall-clock aligned still receive a future resolver deadline.
func (coordinator *Coordinator) providerDeleteBoundContext(
	parent context.Context,
	deadline time.Time,
) (context.Context, context.CancelFunc, error) {
	if parent == nil {
		parent = context.Background()
	}
	if err := parent.Err(); err != nil {
		return nil, nil, err
	}
	now := coordinator.now().UTC()
	deadline = deadline.UTC()
	if deadline.IsZero() || !deadline.After(now) {
		return nil, nil, fmt.Errorf("%w: %w: lifecycle provider-delete authority deadline reached", errProviderDeletePreparationAuthority, context.DeadlineExceeded)
	}
	if parentDeadline, ok := parent.Deadline(); ok && parentDeadline.Before(deadline) {
		deadline = parentDeadline
	}
	if !deadline.After(now) {
		return nil, nil, fmt.Errorf("%w: %w: lifecycle provider-delete parent deadline reached", errProviderDeletePreparationAuthority, context.DeadlineExceeded)
	}
	base, cancelBase := context.WithCancel(parent)
	bounded := &providerDeleteBoundContextValue{Context: base, deadline: deadline}
	timer := time.AfterFunc(deadline.Sub(now), func() {
		bounded.timedOut.Store(true)
		cancelBase()
	})
	cancel := func() {
		timer.Stop()
		cancelBase()
	}
	if err := bounded.Err(); err != nil {
		cancel()
		return nil, nil, err
	}
	return bounded, cancel, nil
}

func (coordinator *Coordinator) revalidateProviderDeletePreparationTx(
	ctx context.Context,
	tx *gorm.DB,
	rows *providerDeleteRows,
	prepared preparedPointDeletion,
) (time.Time, error) {
	// Sample immediately after the blocking resolver returns. This timestamp
	// binds both the horizon check and the claim CAS; the pre-Prepare sample
	// must never be reused.
	now := coordinator.now().UTC()
	if err := refreshProviderDeleteRowsAfterFenceTx(ctx, tx, rows); err != nil {
		return now, fmt.Errorf("%w: %w", errProviderDeletePreparationAuthority, err)
	}
	if err := coordinator.validateProviderDeleteLeaseAuthority(*rows, now, false, true, false); err != nil {
		return now, fmt.Errorf("%w: %w", errProviderDeletePreparationAuthority, err)
	}
	request := coordinator.lifecyclePointRequestFromRows(*rows, true)
	if err := validateProviderDeleteExecutionRows(request, rows.attempt, rows.point, rows.lease, now); err != nil {
		return now, fmt.Errorf("%w: %w", errProviderDeletePreparationAuthority, err)
	}
	if prepared.identityDigest == "" || !isLowerHexString(prepared.identityDigest, 64) {
		return now, fmt.Errorf("%w: invalid prepared provider deletion identity", errProviderDeletePreparationAuthority)
	}
	if rows.claimFound && rows.claim.TargetIdentityDigest != prepared.identityDigest {
		return now, fmt.Errorf("%w: deletion target digest changed during preparation", errProviderDeletePreparationAuthority)
	}
	return now, nil
}

func (adapter *RegistryPointDeletion) Prepare(
	ctx context.Context,
	tx *gorm.DB,
	profile providerDeletePrepareProfile,
	request LifecyclePointRequest,
	rows lifecycleDeleteRows,
) (preparedPointDeletion, error) {
	var prepared preparedPointDeletion
	if adapter == nil || adapter.db == nil || adapter.registry == nil || adapter.resolve == nil || tx == nil {
		return prepared, fmt.Errorf("%s: %w: registry point deletion adapter unavailable", providerDeleteStageClaimObserve, backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if profile != providerDeletePrepareObserver && profile != providerDeletePrepareExecution {
		return prepared, fmt.Errorf("%s: %w: invalid provider delete prepare profile", providerDeleteStageClaimObserve, backupasset.ErrInvalidState)
	}
	if err := validateProviderDeleteBaseRows(request, rows.attempt, rows.point, rows.repository, profile == providerDeletePrepareObserver); err != nil {
		return prepared, fmt.Errorf("%s: %w", providerDeleteStageClaimObserve, err)
	}
	if profile == providerDeletePrepareExecution {
		if err := validateProviderDeleteExecutionRows(request, rows.attempt, rows.point, rows.lease, adapter.adapterNow()); err != nil {
			return prepared, fmt.Errorf("%s: %w", providerDeleteStageClaimAcquire, err)
		}
	}
	resolved, err := adapter.resolve.ResolveDeletePoint(ctx, tx, request, rows.point, rows.repository)
	if err != nil {
		return prepared, fmt.Errorf("%s: %w", providerDeleteStageClaimObserve, mapProviderDeletionError(err))
	}
	if err := validateProviderDeleteRowsUnchangedTx(ctx, tx, rows.point, rows.repository); err != nil {
		return prepared, fmt.Errorf("%s: %w", providerDeleteStageClaimObserve, err)
	}
	if err := validateProviderDeleteRequestShape(request, resolved, rows.attempt, rows.point, rows.repository); err != nil {
		return prepared, fmt.Errorf("%s: %w", providerDeleteStageClaimObserve, err)
	}
	kind := backupasset.ProviderKind(rows.repository.ProviderKind)
	deleter, err := adapter.registry.PointDeleter(kind)
	if err != nil {
		return prepared, fmt.Errorf("%s: %w", providerDeleteStageClaimObserve, mapProviderDeletionError(err))
	}
	repositoryIdentity := ""
	if rows.repository.RepositoryIdentity != nil {
		repositoryIdentity = strings.TrimSpace(*rows.repository.RepositoryIdentity)
	}
	identity := provider.DeletionTargetIdentityInput{
		RecoveryPointID:    rows.point.ID,
		AttemptID:          rows.attempt.ID,
		Operation:          backupasset.LifecycleOperation(rows.attempt.Operation),
		RepositoryIdentity: repositoryIdentity,
		Request:            resolved,
	}
	digest, err := provider.DeletionTargetIdentityDigest(identity)
	if err != nil {
		return prepared, fmt.Errorf("%s: %w", providerDeleteStageClaimObserve, err)
	}
	if !isLowerHexString(digest, 64) {
		return prepared, fmt.Errorf("%s: %w: invalid deletion target digest", providerDeleteStageClaimObserve, backupasset.ErrInvalidState)
	}
	// Resolve is deliberately performed while the caller's row locks are held;
	// no provider client is materialized here. The observer still has a future
	// deadline context so a resolver cannot accidentally inherit an expired
	// caller context as an authority decision.
	if profile == providerDeletePrepareObserver {
		if deadline, ok := ctx.Deadline(); ok && !deadline.After(adapter.adapterNow()) {
			return prepared, fmt.Errorf("%s: %w: observer preparation deadline elapsed", providerDeleteStageClaimObserve, context.DeadlineExceeded)
		}
	}
	prepared = preparedPointDeletion{
		request:        resolved,
		identity:       identity,
		identityDigest: digest,
		deleter:        deleter,
	}
	return prepared, nil
}

func (adapter *RegistryPointDeletion) Execute(ctx context.Context, prepared preparedPointDeletion) (pointDeletionExecution, error) {
	execution := pointDeletionExecution{Stage: providerDeleteStageProvider}
	if ctx == nil {
		ctx = context.Background()
	}
	if !isLowerHexString(prepared.identityDigest, 64) || prepared.request.Validate() != nil {
		return execution, fmt.Errorf("%s: %w: invalid prepared provider deletion", providerDeleteStageProvider, backupasset.ErrInvalidState)
	}
	var result provider.DeletePointResult
	var err error
	if prepared.localExecute != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return execution, fmt.Errorf("%s: %w", providerDeleteStageProvider, contextErr)
		}
		// This assignment is intentionally immediately before the provider call;
		// every subsequent error is potentially a partial remote effect.
		execution.ProviderCalled = true
		result, err = prepared.localExecute(ctx)
	} else if prepared.deleter != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return execution, fmt.Errorf("%s: %w", providerDeleteStageProvider, contextErr)
		}
		// This assignment is intentionally immediately before the provider call;
		// every subsequent error is potentially a partial remote effect.
		execution.ProviderCalled = true
		result, err = prepared.deleter.DeletePoint(ctx, prepared.request)
	} else {
		err = fmt.Errorf("%w: point deleter unavailable", backupasset.ErrInvalidState)
	}
	if err != nil {
		return execution, fmt.Errorf("%s: %w", providerDeleteStageProvider, mapProviderDeletionError(err))
	}
	validated, err := validateRegistryProviderDeletionResult(result)
	if err != nil {
		return execution, fmt.Errorf("%s: %w", providerDeleteStageProvider, err)
	}
	execution.Result = validated
	return execution, nil
}

func validateProviderDeleteRowsUnchangedTx(
	ctx context.Context,
	tx *gorm.DB,
	point model.RecoveryPoint,
	repository model.BackupRepository,
) error {
	if tx == nil {
		return fmt.Errorf("%w: lifecycle deletion transaction unavailable", backupasset.ErrInvalidState)
	}
	var currentRepository model.BackupRepository
	loaded := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", repository.ID).
		Limit(1).
		Find(&currentRepository)
	if loaded.Error != nil || loaded.RowsAffected != 1 {
		return lifecycleDeleteIdentityConflict("lifecycle repository changed during preparation")
	}
	var currentPoint model.RecoveryPoint
	loaded = tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", point.ID).
		Limit(1).
		Find(&currentPoint)
	if loaded.Error != nil || loaded.RowsAffected != 1 {
		return lifecycleDeleteIdentityConflict("lifecycle recovery point changed during preparation")
	}
	if currentPoint.ID != point.ID ||
		currentPoint.RepositoryID != point.RepositoryID ||
		currentPoint.EncryptedProviderLocator != point.EncryptedProviderLocator ||
		currentPoint.EncryptedRollbackLocator != point.EncryptedRollbackLocator ||
		currentPoint.Semantics != point.Semantics ||
		currentPoint.State != point.State ||
		currentPoint.SourceFingerprint != point.SourceFingerprint ||
		currentPoint.PointRevision != point.PointRevision ||
		currentPoint.CapabilityRevision != point.CapabilityRevision ||
		currentPoint.PhysicalAvailability != point.PhysicalAvailability ||
		currentPoint.HoldState != point.HoldState ||
		!sameLifecycleTimePtr(currentPoint.HoldUntil, point.HoldUntil) ||
		!sameLifecycleTimePtr(currentPoint.RetentionUntil, point.RetentionUntil) ||
		!sameLifecycleStringPtr(currentPoint.RetirementReason, point.RetirementReason) ||
		!sameLifecycleTimePtr(currentPoint.RetiredAt, point.RetiredAt) {
		return lifecycleDeleteIdentityConflict("lifecycle recovery point changed during preparation")
	}
	if currentRepository.ID != repository.ID ||
		currentRepository.ProviderKind != repository.ProviderKind ||
		!sameLifecycleStringPtr(currentRepository.RepositoryIdentity, repository.RepositoryIdentity) ||
		currentRepository.VersionMode != repository.VersionMode ||
		currentRepository.Status != repository.Status ||
		currentRepository.CapabilityRevision != repository.CapabilityRevision ||
		currentRepository.CapabilitiesJSON != repository.CapabilitiesJSON ||
		currentRepository.ImmutabilityLevel != repository.ImmutabilityLevel {
		return lifecycleDeleteIdentityConflict("lifecycle repository changed during preparation")
	}
	return nil
}

func sameLifecycleStringPtr(left, right *string) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func sameLifecycleTimePtr(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.Equal(*right)
}

func (adapter *RegistryPointDeletion) Verify(
	ctx context.Context,
	tx *gorm.DB,
	request LifecyclePointRequest,
	prepared preparedPointDeletion,
	rows lifecycleDeleteRows,
) error {
	if adapter == nil || adapter.db == nil || adapter.resolve == nil || tx == nil {
		return fmt.Errorf("%s: %w: registry point deletion adapter unavailable", providerDeleteStageVerify, backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if !isLowerHexString(prepared.identityDigest, 64) || prepared.request.Validate() != nil {
		return fmt.Errorf("%s: %w: invalid prepared provider deletion", providerDeleteStageVerify, backupasset.ErrInvalidState)
	}
	if err := validateProviderDeleteBaseRows(request, rows.attempt, rows.point, rows.repository, true); err != nil {
		return fmt.Errorf("%s: %w", providerDeleteStageVerify, err)
	}
	if err := validateProviderDeleteReceiptRows(request, rows.attempt, rows.point, rows.lease, adapter.adapterNow(), true, false); err != nil {
		return fmt.Errorf("%s: %w", providerDeleteStageVerify, err)
	}
	current, err := adapter.resolve.ResolveDeletePoint(ctx, tx, request, rows.point, rows.repository)
	if err != nil {
		return fmt.Errorf("%s: %w", providerDeleteStageVerify, mapProviderDeletionError(err))
	}
	if err := validateProviderDeleteRequestShape(request, current, rows.attempt, rows.point, rows.repository); err != nil {
		return fmt.Errorf("%s: %w", providerDeleteStageVerify, err)
	}
	repositoryIdentity := ""
	if rows.repository.RepositoryIdentity != nil {
		repositoryIdentity = strings.TrimSpace(*rows.repository.RepositoryIdentity)
	}
	currentIdentity := provider.DeletionTargetIdentityInput{
		RecoveryPointID: rows.point.ID, AttemptID: rows.attempt.ID,
		Operation:          backupasset.LifecycleOperation(rows.attempt.Operation),
		RepositoryIdentity: repositoryIdentity, Request: current,
	}
	if err := provider.CompareDeletionTargetAuthority(prepared.identity, currentIdentity); err != nil {
		return fmt.Errorf("%s: %w", providerDeleteStageVerify, err)
	}
	currentDigest, err := provider.DeletionTargetIdentityDigest(currentIdentity)
	if err != nil {
		return fmt.Errorf("%s: %w", providerDeleteStageVerify, err)
	}
	if currentDigest != prepared.identityDigest {
		return fmt.Errorf("%s: %w: deletion target digest changed", providerDeleteStageVerify, provider.ErrDeletePointIdentityConflict)
	}
	return nil
}

func validateProviderDeleteBaseRows(
	request LifecyclePointRequest,
	attempt model.RecoveryPointLifecycleAttempt,
	point model.RecoveryPoint,
	repository model.BackupRepository,
	observer bool,
) error {
	if attempt.ID != request.AttemptID || attempt.RecoveryPointID != request.RecoveryPointID ||
		point.ID != request.RecoveryPointID || point.RepositoryID != repository.ID ||
		backupasset.LifecycleOperation(attempt.Operation) != request.Operation ||
		!providerDeleteLifecycleOperation(request.Operation) {
		return lifecycleDeleteIdentityConflict("lifecycle deletion authority changed")
	}
	phase := backupasset.LifecyclePhase(attempt.Phase)
	if phase != backupasset.LifecyclePhaseProviderDelete && (!observer || phase != backupasset.LifecyclePhaseBlocked) {
		return lifecycleDeleteIdentityConflict("lifecycle phase does not authorize provider deletion")
	}
	state := backupasset.RecoveryPointState(point.State)
	if state != backupasset.RecoveryPointExpiring && (!observer || state != backupasset.RecoveryPointPurgeBlocked) && state != backupasset.RecoveryPointExpired {
		return lifecycleDeleteIdentityConflict("lifecycle point does not authorize provider deletion")
	}
	if point.CapabilityRevision <= 0 || repository.CapabilityRevision <= 0 || point.CapabilityRevision != repository.CapabilityRevision {
		return lifecycleDeleteIdentityConflict("lifecycle capability revision changed")
	}
	if attempt.TransitionRevision <= 0 || attempt.LeaseID == nil || attempt.LeaseAttemptID == nil || attempt.LeaseFenceTokenHash == nil ||
		!isLowerHexString(*attempt.LeaseID, 32) || !isLowerHexString(*attempt.LeaseAttemptID, 32) || !isLowerHexString(*attempt.LeaseFenceTokenHash, 64) {
		return lifecycleDeleteIdentityConflict("lifecycle lease authority is incomplete")
	}
	return nil
}

func validateProviderDeleteReceiptRows(
	request LifecyclePointRequest,
	attempt model.RecoveryPointLifecycleAttempt,
	point model.RecoveryPoint,
	lease model.RecoveryPointLease,
	now time.Time,
	allowHeldPoint bool,
	requireLiveDeadline bool,
) error {
	if backupasset.LifecyclePhase(attempt.Phase) != backupasset.LifecyclePhaseProviderDelete ||
		backupasset.RecoveryPointState(point.State) != backupasset.RecoveryPointExpiring &&
			(!allowHeldPoint || backupasset.RecoveryPointState(point.State) != backupasset.RecoveryPointPurgeBlocked) {
		return lifecycleDeleteIdentityConflict("lifecycle execution phase changed")
	}
	if request.authority.TransitionRevision != attempt.TransitionRevision || attempt.LeaseID == nil ||
		attempt.LeaseAttemptID == nil || attempt.LeaseFenceTokenHash == nil ||
		request.authority.LeaseID != *attempt.LeaseID || request.authority.LeaseAttemptID != *attempt.LeaseAttemptID ||
		request.authority.LeaseFenceHash != *attempt.LeaseFenceTokenHash {
		return lifecycleDeleteIdentityConflict("lifecycle deletion fence changed")
	}
	if lease.ID != request.authority.LeaseID || lease.RecoveryPointID != point.ID ||
		lease.AttemptID != request.authority.LeaseAttemptID || hashFenceToken(lease.FenceToken) != request.authority.LeaseFenceHash ||
		lease.HolderType != string(backupasset.LeaseHolderRetentionWorker) || lease.OwnerID == "" ||
		lease.OwnerID != request.authority.LeaseOwnerID || backupasset.LeaseStatus(lease.Status) != backupasset.LeaseActive {
		return lifecycleDeleteIdentityConflict("lifecycle lease authority changed")
	}
	deadline := lease.LeaseExpiresAt.UTC()
	if lease.AbsoluteDeadline.UTC().Before(deadline) {
		deadline = lease.AbsoluteDeadline.UTC()
	}
	if deadline.IsZero() || (requireLiveDeadline && !deadline.After(now.UTC())) || request.authority.Deadline.IsZero() ||
		!deadline.Equal(request.authority.Deadline.UTC()) {
		return lifecycleDeleteIdentityConflict("lifecycle lease deadline changed")
	}
	return nil
}

func validateProviderDeleteExecutionRows(
	request LifecyclePointRequest,
	attempt model.RecoveryPointLifecycleAttempt,
	point model.RecoveryPoint,
	lease model.RecoveryPointLease,
	now time.Time,
) error {
	return validateProviderDeleteReceiptRows(request, attempt, point, lease, now, false, true)
}

func validateProviderDeleteRequestShape(
	request LifecyclePointRequest,
	deleteRequest provider.DeletePointRequest,
	attempt model.RecoveryPointLifecycleAttempt,
	point model.RecoveryPoint,
	repository model.BackupRepository,
) error {
	repositoryIdentity := ""
	if repository.RepositoryIdentity != nil {
		repositoryIdentity = strings.TrimSpace(*repository.RepositoryIdentity)
	}
	kind := backupasset.ProviderKind(repository.ProviderKind)
	if deleteRequest.OperationID != request.AttemptID || deleteRequest.OperationID != attempt.ID ||
		deleteRequest.Snapshot.RepositoryID != repository.ID || deleteRequest.Snapshot.CapabilityRevision != point.CapabilityRevision ||
		deleteRequest.Snapshot.SourceRevision == "" || deleteRequest.Snapshot.SourceRevision != point.SourceFingerprint ||
		deleteRequest.ExpectedSourceRevision != point.SourceFingerprint || deleteRequest.Snapshot.SourceRevision != deleteRequest.ExpectedSourceRevision ||
		deleteRequest.Snapshot.RepositoryIdentity != repositoryIdentity || deleteRequest.Snapshot.Access.Provider != kind ||
		deleteRequest.Snapshot.Access.RepositoryID != repository.ID || strings.TrimSpace(deleteRequest.Point.Native) == "" ||
		deleteRequest.Point.Native != strings.TrimSpace(deleteRequest.Point.Native) {
		return lifecycleDeleteIdentityConflict("provider delete request identity changed")
	}
	if err := deleteRequest.Validate(); err != nil {
		return fmt.Errorf("%w: invalid provider delete request shape", backupasset.ErrInvalidState)
	}
	return nil
}

type providerDeleteRows struct {
	lifecycleDeleteRows
	claim          model.RecoveryPointLifecycleEffectClaim
	claimFound     bool
	tombstone      model.RecoveryPointLifecycleTombstone
	tombstoneFound bool
	leaseFound     bool
}

func lockProviderDeleteRowsByAttemptTx(ctx context.Context, tx *gorm.DB, attemptID string) (providerDeleteRows, error) {
	var rows providerDeleteRows
	if tx == nil || backupasset.ValidateOpaqueID(attemptID) != nil {
		return rows, fmt.Errorf("%s: %w: invalid lifecycle attempt", providerDeleteStagePreClaim, backupasset.ErrInvalidState)
	}
	var reference struct{ RecoveryPointID string }
	loaded := tx.WithContext(ctx).Model(&model.RecoveryPointLifecycleAttempt{}).Select("recovery_point_id").Where("id = ?", attemptID).Limit(1).Find(&reference)
	if loaded.Error != nil {
		return rows, fmt.Errorf("%s: resolve lifecycle attempt point: %w", providerDeleteStagePreClaim, loaded.Error)
	}
	if loaded.RowsAffected != 1 || backupasset.ValidateOpaqueID(reference.RecoveryPointID) != nil {
		return rows, fmt.Errorf("%s: %w: lifecycle attempt", providerDeleteStagePreClaim, backupasset.ErrNotFound)
	}
	return lockProviderDeleteRowsTx(ctx, tx, LifecyclePointRequest{RecoveryPointID: reference.RecoveryPointID, AttemptID: attemptID}, true)
}

func lockProviderDeleteRowsTx(ctx context.Context, tx *gorm.DB, request LifecyclePointRequest, identityOnMissing bool) (providerDeleteRows, error) {
	var rows providerDeleteRows
	if tx == nil {
		return rows, fmt.Errorf("%s: %w: transaction unavailable", providerDeleteStagePreClaim, backupasset.ErrInvalidState)
	}
	var pointReference struct{ RepositoryID string }
	loaded := tx.WithContext(ctx).Model(&model.RecoveryPoint{}).Select("repository_id").Where("id = ?", request.RecoveryPointID).Limit(1).Find(&pointReference)
	if loaded.Error != nil {
		return rows, fmt.Errorf("%s: load lifecycle point repository: %w", providerDeleteStagePreClaim, loaded.Error)
	}
	if loaded.RowsAffected != 1 || backupasset.ValidateOpaqueID(pointReference.RepositoryID) != nil {
		if identityOnMissing {
			return rows, lifecycleDeleteIdentityConflict("lifecycle recovery point changed")
		}
		return rows, fmt.Errorf("%s: %w: lifecycle recovery point", providerDeleteStagePreClaim, backupasset.ErrNotFound)
	}
	loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", pointReference.RepositoryID).Limit(1).Find(&rows.repository)
	if loaded.Error != nil || loaded.RowsAffected != 1 {
		if loaded.Error != nil {
			return rows, fmt.Errorf("%s: lock lifecycle repository: %w", providerDeleteStagePreClaim, loaded.Error)
		}
		return rows, fmt.Errorf("%s: %w: lifecycle repository", providerDeleteStagePreClaim, backupasset.ErrNotFound)
	}
	loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", request.RecoveryPointID).Limit(1).Find(&rows.point)
	if loaded.Error != nil || loaded.RowsAffected != 1 {
		if loaded.Error != nil {
			return rows, fmt.Errorf("%s: lock lifecycle point: %w", providerDeleteStagePreClaim, loaded.Error)
		}
		return rows, fmt.Errorf("%s: %w: lifecycle point", providerDeleteStagePreClaim, backupasset.ErrNotFound)
	}
	if rows.point.RepositoryID != rows.repository.ID {
		return rows, lifecycleDeleteIdentityConflict("lifecycle point repository changed")
	}
	loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND recovery_point_id = ?", request.AttemptID, request.RecoveryPointID).Limit(1).Find(&rows.attempt)
	if loaded.Error != nil || loaded.RowsAffected != 1 {
		if loaded.Error != nil {
			return rows, fmt.Errorf("%s: lock lifecycle attempt: %w", providerDeleteStagePreClaim, loaded.Error)
		}
		return rows, lifecycleDeleteIdentityConflict("lifecycle attempt changed")
	}

	if rows.attempt.LeaseID != nil {
		leaseID := *rows.attempt.LeaseID
		// A proof-bearing tombstone is authoritative even when the
		// historical lease binding is stale or malformed. Defer lease
		// validation until after the tombstone has been checked; paths with
		// no proof still fail closed through requireProviderDeleteLease.
		if backupasset.ValidateOpaqueID(leaseID) == nil {
			loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("id = ?", leaseID).Limit(1).Find(&rows.lease)
			if loaded.Error != nil {
				return rows, fmt.Errorf("%s: lock lifecycle lease: %w", providerDeleteStagePreClaim, loaded.Error)
			}
			rows.leaseFound = loaded.RowsAffected == 1
		}
	}
	if rows.attempt.LeaseID == nil {
		rows.leaseFound = false
	}
	loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("attempt_id = ?", rows.attempt.ID).Limit(1).Find(&rows.claim)
	if loaded.Error != nil {
		return rows, fmt.Errorf("%s: lock lifecycle effect claim: %w", providerDeleteStagePreClaim, loaded.Error)
	}
	rows.claimFound = loaded.RowsAffected == 1
	loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("recovery_point_id = ? AND terminal_operation = ?", rows.point.ID, rows.attempt.Operation).Limit(1).Find(&rows.tombstone)
	if loaded.Error != nil {
		return rows, fmt.Errorf("%s: lock lifecycle tombstone: %w", providerDeleteStagePreClaim, loaded.Error)
	}
	rows.tombstoneFound = loaded.RowsAffected == 1
	return rows, nil
}

// providerDeleteProofFirst validates the durable provider proof before any
// claim, lease, deadline, retry, or observer classification. A valid
// tombstone intentionally wins over stale in-flight/uncertain claim state;
// callers must skip the claimed-state matrix in that case. Conversely, a
// proven claim without a tombstone is corruption and remains fail-closed.
func providerDeleteProofFirst(rows providerDeleteRows) (PointDeletionResult, bool, error) {
	if rows.tombstoneFound {
		if err := validateLifecycleTerminalEvent(rows.point, rows.attempt, rows.tombstone); err != nil {
			return PointDeletionResult{}, false, err
		}
		result, err := providerDeleteResultFromTombstone(rows.tombstone)
		if err != nil {
			return PointDeletionResult{}, false, err
		}
		return result, true, nil
	}
	if rows.claimFound && rows.claim.State == "proven" {
		return PointDeletionResult{}, false, fmt.Errorf("%w: proven lifecycle effect claim has no tombstone", backupasset.ErrInvalidState)
	}
	return PointDeletionResult{}, false, nil
}

// validateProviderDeleteRowsProofFirst is the common admission point for
// provider-delete recovery transactions. It preserves the strict claimed
// matrix whenever proof is absent, while allowing a valid tombstone to
// bypass stale/unproven claim classification.
func validateProviderDeleteRowsProofFirst(rows providerDeleteRows) (PointDeletionResult, bool, error) {
	result, proven, err := providerDeleteProofFirst(rows)
	if err != nil || proven {
		return result, proven, err
	}
	if err := validateProviderDeleteClaimMatrix(rows); err != nil {
		return PointDeletionResult{}, false, err
	}
	return PointDeletionResult{}, false, nil
}

// validateProviderDeleteRowsAuthority admits durable proof before consulting
// the current lease owner, holder, fence, or deadline. A valid tombstone is
// immutable proof even when the current lease has expired or been rebound;
// without proof, the ordinary identity gate remains fail-closed.
func (coordinator *Coordinator) validateProviderDeleteRowsAuthority(
	rows providerDeleteRows,
	requireClaim bool,
	allowStaleFence bool,
) (PointDeletionResult, bool, error) {
	result, proofFound, err := validateProviderDeleteRowsProofFirst(rows)
	if err != nil || proofFound {
		return result, proofFound, err
	}
	if err := coordinator.validateProviderDeleteLeaseIdentity(rows, requireClaim, allowStaleFence); err != nil {
		return PointDeletionResult{}, false, err
	}
	return PointDeletionResult{}, false, nil
}

func providerDeleteResultFromTombstone(tombstone model.RecoveryPointLifecycleTombstone) (PointDeletionResult, error) {
	if tombstone.DeletionReceiptDigest == nil {
		return PointDeletionResult{}, fmt.Errorf("%w: lifecycle tombstone is unproven", backupasset.ErrInvalidState)
	}
	result := PointDeletionResult{Outcome: PointDeletionOutcome(tombstone.ResultCode), ReceiptDigest: *tombstone.DeletionReceiptDigest}
	if !validPointDeletionResult(result) {
		return PointDeletionResult{}, fmt.Errorf("%w: lifecycle tombstone is unproven", backupasset.ErrInvalidState)
	}
	return result, nil
}

func providerDeleteClaimBlockedReason(reason backupasset.LifecycleBlockedReason) bool {
	switch reason {
	case backupasset.LifecycleBlockedActiveHold,
		backupasset.LifecycleBlockedProviderIdentityConflict,
		backupasset.LifecycleBlockedProviderNativeVersionReferenced:
		return true
	default:
		return false
	}
}

func validateProviderDeleteClaimMatrix(rows providerDeleteRows) error {
	if !rows.claimFound {
		return nil
	}
	state := rows.claim.State
	phase := backupasset.LifecyclePhase(rows.attempt.Phase)
	reason := backupasset.LifecycleBlockedReason(rows.attempt.BlockedReason)
	switch state {
	case "in_flight":
		if phase != backupasset.LifecyclePhaseProviderDelete {
			return fmt.Errorf("%w: in-flight lifecycle effect claim phase is invalid", backupasset.ErrInvalidState)
		}
	case "uncertain":
		if phase != backupasset.LifecyclePhaseProviderDelete &&
			(phase != backupasset.LifecyclePhaseBlocked || !providerDeleteClaimBlockedReason(reason)) {
			return fmt.Errorf("%w: uncertain lifecycle effect claim phase is invalid", backupasset.ErrInvalidState)
		}
	case "proven":
		if !rows.tombstoneFound {
			return fmt.Errorf("%w: proven lifecycle effect claim has no tombstone", backupasset.ErrInvalidState)
		}
		if phase != backupasset.LifecyclePhaseProviderDelete && phase != backupasset.LifecyclePhaseTombstoning && phase != backupasset.LifecyclePhaseComplete &&
			(phase != backupasset.LifecyclePhaseBlocked || reason != backupasset.LifecycleBlockedActiveHold) {
			return fmt.Errorf("%w: proven lifecycle effect claim phase is invalid", backupasset.ErrInvalidState)
		}
	default:
		return fmt.Errorf("%w: invalid lifecycle effect claim state", backupasset.ErrInvalidState)
	}
	return nil
}

func requireProviderDeleteLease(rows providerDeleteRows) error {
	if !rows.leaseFound || rows.lease.ID == "" {
		return lifecycleDeleteIdentityConflict("lifecycle lease authority is unavailable")
	}
	return nil
}

func (coordinator *Coordinator) providerDeleteClaimDeadline(lease model.RecoveryPointLease, now time.Time) time.Time {
	deadline := now.UTC().Add(coordinator.effectClaimTTL)
	if lease.LeaseExpiresAt.UTC().Before(deadline) {
		deadline = lease.LeaseExpiresAt.UTC()
	}
	if lease.AbsoluteDeadline.UTC().Before(deadline) {
		deadline = lease.AbsoluteDeadline.UTC()
	}
	return deadline.UTC()
}

// validateProviderDeleteLeaseIdentity is the coordinator-owned immutable
// authority gate for durable provider-delete state. A valid tombstone may
// carry legacy rows without a lease, but any present lease must still belong
// to this recovery point, retention worker, and configured coordinator owner.
// Recovery classification may allow a stale fence only to rotate it during
// takeover; IDs and claim snapshots remain coherent in every mode.
func (coordinator *Coordinator) validateProviderDeleteLeaseIdentity(
	rows providerDeleteRows,
	requireClaim bool,
	allowStaleFence bool,
) error {
	if coordinator == nil || coordinator.leaseOwnerID == "" {
		return lifecycleDeleteIdentityConflict("lifecycle lease coordinator authority is unavailable")
	}
	if !rows.leaseFound {
		if rows.tombstoneFound {
			return nil
		}
		return lifecycleDeleteIdentityConflict("lifecycle lease authority is unavailable")
	}
	if backupasset.ValidateOpaqueID(rows.point.ID) != nil ||
		rows.attempt.TransitionRevision <= 0 ||
		rows.attempt.RecoveryPointID != rows.point.ID ||
		rows.lease.RecoveryPointID != rows.point.ID ||
		rows.lease.HolderType != string(backupasset.LeaseHolderRetentionWorker) ||
		rows.lease.OwnerID != coordinator.leaseOwnerID ||
		rows.lease.LeaseExpiresAt.IsZero() ||
		rows.lease.AbsoluteDeadline.IsZero() ||
		backupasset.ValidateOpaqueID(rows.lease.ID) != nil ||
		backupasset.ValidateOpaqueID(rows.lease.AttemptID) != nil ||
		!isLowerHexString(rows.lease.FenceToken, 64) {
		return lifecycleDeleteIdentityConflict("lifecycle lease authority changed")
	}
	if rows.attempt.LeaseID == nil || rows.attempt.LeaseAttemptID == nil || rows.attempt.LeaseFenceTokenHash == nil ||
		!isLowerHexString(*rows.attempt.LeaseFenceTokenHash, 64) {
		return lifecycleDeleteIdentityConflict("lifecycle lease authority is incomplete")
	}
	if *rows.attempt.LeaseID != rows.lease.ID || *rows.attempt.LeaseAttemptID != rows.lease.AttemptID {
		return fmt.Errorf("%w: lifecycle lease binding changed", errLifecycleFenceLost)
	}
	if !allowStaleFence && *rows.attempt.LeaseFenceTokenHash != hashFenceToken(rows.lease.FenceToken) {
		return lifecycleDeleteIdentityConflict("lifecycle lease fence changed")
	}
	if requireClaim {
		if !rows.claimFound ||
			rows.claim.AttemptID != rows.attempt.ID ||
			(rows.claim.State == "in_flight" &&
				backupasset.LifecyclePhase(rows.attempt.Phase) == backupasset.LifecyclePhaseProviderDelete &&
				rows.claim.TransitionRevision != rows.attempt.TransitionRevision) ||
			rows.claim.LeaseID != rows.lease.ID ||
			rows.claim.LeaseAttemptID != rows.lease.AttemptID ||
			!isLowerHexString(rows.claim.LeaseFenceTokenHash, 64) ||
			rows.claim.LeaseFenceTokenHash != *rows.attempt.LeaseFenceTokenHash ||
			!isLowerHexString(rows.claim.TargetIdentityDigest, 64) ||
			rows.claim.DeadlineAt.IsZero() ||
			(!allowStaleFence && rows.claim.LeaseFenceTokenHash != hashFenceToken(rows.lease.FenceToken)) {
			return lifecycleDeleteIdentityConflict("lifecycle effect claim authority changed")
		}
	}
	return nil
}

func (coordinator *Coordinator) validateProviderDeleteAttemptLeaseIdentityTx(
	ctx context.Context,
	tx *gorm.DB,
	attempt *model.RecoveryPointLifecycleAttempt,
	point *model.RecoveryPoint,
) error {
	if tx == nil || attempt == nil || point == nil {
		return fmt.Errorf("%w: lifecycle lease authority is unavailable", backupasset.ErrInvalidState)
	}
	if attempt.LeaseID == nil {
		return errLifecycleFenceLost
	}
	var lease model.RecoveryPointLease
	loaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", *attempt.LeaseID).Limit(1).Find(&lease)
	if loaded.Error != nil {
		return fmt.Errorf("load lifecycle lease authority: %w", loaded.Error)
	}
	rows := providerDeleteRows{
		lifecycleDeleteRows: lifecycleDeleteRows{
			attempt: *attempt,
			point:   *point,
			lease:   lease,
		},
		leaseFound: loaded.RowsAffected == 1,
	}
	return coordinator.validateProviderDeleteLeaseIdentity(rows, false, true)
}

// validateProviderDeleteLeaseAuthority adds active/live checks to the
// immutable identity gate. Callers recovering an expired historical lease
// must use the identity gate and leave these checks to takeover.
func (coordinator *Coordinator) validateProviderDeleteLeaseAuthority(
	rows providerDeleteRows,
	now time.Time,
	requireClaim bool,
	requireLive bool,
	allowStaleFence bool,
) error {
	if err := coordinator.validateProviderDeleteLeaseIdentity(rows, requireClaim, allowStaleFence); err != nil {
		return err
	}
	if !rows.leaseFound || backupasset.LeaseStatus(rows.lease.Status) != backupasset.LeaseActive {
		return fmt.Errorf("%w: lifecycle lease is not active", backupasset.ErrConflict)
	}
	if requireLive {
		now = now.UTC()
		if !rows.lease.LeaseExpiresAt.UTC().After(now) ||
			!rows.lease.AbsoluteDeadline.UTC().After(now) ||
			(requireClaim && (!rows.claim.DeadlineAt.UTC().After(now) ||
				rows.claim.DeadlineAt.UTC().After(rows.lease.LeaseExpiresAt.UTC()) ||
				rows.claim.DeadlineAt.UTC().After(rows.lease.AbsoluteDeadline.UTC()))) {
			return fmt.Errorf("%w: lifecycle effect deadline reached", backupasset.ErrConflict)
		}
	}
	return nil
}

func (coordinator *Coordinator) lifecyclePointRequestFromRows(rows providerDeleteRows, authority bool) LifecyclePointRequest {
	request := LifecyclePointRequest{RecoveryPointID: rows.point.ID, AttemptID: rows.attempt.ID, Operation: backupasset.LifecycleOperation(rows.attempt.Operation)}
	if authority {
		request.authority = coordinator.lifecycleEffectAuthorityFromRows(rows.attempt, rows.lease)
	}
	return request
}

func (coordinator *Coordinator) lifecycleEffectAuthorityFromRows(attempt model.RecoveryPointLifecycleAttempt, lease model.RecoveryPointLease) lifecycleEffectAuthority {
	deadline := lease.LeaseExpiresAt.UTC()
	if lease.AbsoluteDeadline.UTC().Before(deadline) {
		deadline = lease.AbsoluteDeadline.UTC()
	}
	return lifecycleEffectAuthority{
		TransitionRevision: attempt.TransitionRevision,
		LeaseID:            lease.ID,
		LeaseOwnerID:       coordinator.leaseOwnerID,
		Deadline:           deadline,
		LeaseAttemptID:     valueOrEmpty(attempt.LeaseAttemptID),
		LeaseFenceHash:     valueOrEmpty(attempt.LeaseFenceTokenHash),
	}
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func (coordinator *Coordinator) providerDeleteClaimMatchesCurrent(rows providerDeleteRows, now time.Time) (bool, error) {
	if !rows.claimFound || rows.claim.State != "in_flight" {
		return false, nil
	}
	if err := coordinator.validateProviderDeleteLeaseIdentity(rows, true, true); err != nil {
		return false, err
	}
	if backupasset.LeaseStatus(rows.lease.Status) != backupasset.LeaseActive {
		return false, nil
	}
	if !rows.claim.DeadlineAt.UTC().After(now.UTC()) ||
		!rows.lease.LeaseExpiresAt.UTC().After(now.UTC()) ||
		!rows.lease.AbsoluteDeadline.UTC().After(now.UTC()) {
		return false, nil
	}
	return rows.claim.TransitionRevision == rows.attempt.TransitionRevision &&
		rows.attempt.LeaseID != nil && rows.attempt.LeaseAttemptID != nil && rows.attempt.LeaseFenceTokenHash != nil &&
		rows.claim.LeaseID == *rows.attempt.LeaseID &&
		rows.claim.LeaseAttemptID == *rows.attempt.LeaseAttemptID &&
		rows.claim.LeaseFenceTokenHash == *rows.attempt.LeaseFenceTokenHash &&
		rows.lease.ID == rows.claim.LeaseID &&
		rows.lease.AttemptID == rows.claim.LeaseAttemptID &&
		hashFenceToken(rows.lease.FenceToken) == rows.claim.LeaseFenceTokenHash &&
		backupasset.LeaseStatus(rows.lease.Status) == backupasset.LeaseActive, nil
}

func (coordinator *Coordinator) markEffectClaimUncertainTx(ctx context.Context, tx *gorm.DB, claim *model.RecoveryPointLifecycleEffectClaim, now time.Time) error {
	if claim == nil || claim.ID == "" || claim.State != "in_flight" {
		return nil
	}
	result := tx.WithContext(ctx).Model(&model.RecoveryPointLifecycleEffectClaim{}).
		Where("id = ? AND state = ? AND executor_id = ? AND execution_id = ? AND transition_revision = ? AND lease_id = ? AND lease_attempt_id = ? AND lease_fence_token_hash = ? AND target_identity_digest = ?",
			claim.ID, "in_flight", claim.ExecutorID, claim.ExecutionID, claim.TransitionRevision, claim.LeaseID, claim.LeaseAttemptID, claim.LeaseFenceTokenHash, claim.TargetIdentityDigest).
		Updates(map[string]any{"state": "uncertain", "heartbeat_at": now.UTC(), "updated_at": now.UTC()})
	if result.Error != nil {
		return fmt.Errorf("%s: mark lifecycle effect claim uncertain: %w", providerDeleteStageClaimAcquire, result.Error)
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("%w: lifecycle effect claim changed", backupasset.ErrConflict)
	}
	claim.State = "uncertain"
	claim.HeartbeatAt = now.UTC()
	claim.UpdatedAt = now.UTC()
	return nil
}

func (coordinator *Coordinator) insertEffectClaimTx(ctx context.Context, tx *gorm.DB, rows *providerDeleteRows, prepared preparedPointDeletion, executionID string, now time.Time) error {
	if rows == nil || rows.claimFound || !isLowerHexString(prepared.identityDigest, 64) || !isLowerHexString(executionID, 32) {
		return fmt.Errorf("%s: %w: invalid lifecycle effect claim acquisition", providerDeleteStageClaimAcquire, backupasset.ErrInvalidState)
	}
	if err := coordinator.validateProviderDeleteLeaseAuthority(*rows, now, false, true, false); err != nil {
		return err
	}
	claimID, err := backupasset.NewOpaqueID()
	if err != nil {
		return fmt.Errorf("%s: generate lifecycle effect claim ID: %w", providerDeleteStageClaimAcquire, err)
	}
	deadline := coordinator.providerDeleteClaimDeadline(rows.lease, now)
	if !deadline.After(now.UTC()) {
		return fmt.Errorf("%s: %w: lifecycle effect claim deadline elapsed", providerDeleteStageClaimAcquire, backupasset.ErrConflict)
	}
	claim := model.RecoveryPointLifecycleEffectClaim{
		ID: claimID, AttemptID: rows.attempt.ID, ExecutorID: coordinator.effectExecutorID, ExecutionID: executionID,
		TransitionRevision: rows.attempt.TransitionRevision, LeaseID: rows.lease.ID, LeaseAttemptID: rows.lease.AttemptID,
		LeaseFenceTokenHash: hashFenceToken(rows.lease.FenceToken), TargetIdentityDigest: prepared.identityDigest, State: "in_flight",
		DeadlineAt: deadline, HeartbeatAt: now.UTC(), CreatedAt: now.UTC(), UpdatedAt: now.UTC(),
	}
	if err := tx.WithContext(ctx).Create(&claim).Error; err != nil {
		return fmt.Errorf("%s: create lifecycle effect claim: %w", providerDeleteStageClaimAcquire, err)
	}
	rows.claim = claim
	rows.claimFound = true
	return nil
}

func (coordinator *Coordinator) updateEffectClaimTakeoverTx(ctx context.Context, tx *gorm.DB, claim *model.RecoveryPointLifecycleEffectClaim, rows *providerDeleteRows, prepared preparedPointDeletion, executionID string, now time.Time) error {
	if claim == nil || rows == nil || claim.State != "uncertain" || !isLowerHexString(executionID, 32) || !isLowerHexString(prepared.identityDigest, 64) {
		return fmt.Errorf("%s: %w: invalid lifecycle effect claim takeover", providerDeleteStageClaimAcquire, backupasset.ErrInvalidState)
	}
	if err := coordinator.validateProviderDeleteLeaseAuthority(*rows, now, false, true, false); err != nil {
		return err
	}
	deadline := coordinator.providerDeleteClaimDeadline(rows.lease, now)
	if !deadline.After(now.UTC()) {
		return fmt.Errorf("%s: %w: lifecycle effect claim deadline elapsed", providerDeleteStageClaimAcquire, backupasset.ErrConflict)
	}
	result := tx.WithContext(ctx).Model(&model.RecoveryPointLifecycleEffectClaim{}).
		Where("id = ? AND state = ? AND target_identity_digest = ?", claim.ID, "uncertain", claim.TargetIdentityDigest).
		Updates(map[string]any{
			"executor_id": coordinator.effectExecutorID, "execution_id": executionID,
			"transition_revision": rows.attempt.TransitionRevision, "lease_id": rows.lease.ID,
			"lease_attempt_id": rows.lease.AttemptID, "lease_fence_token_hash": hashFenceToken(rows.lease.FenceToken),
			"state": "in_flight", "deadline_at": deadline, "heartbeat_at": now.UTC(), "updated_at": now.UTC(),
		})
	if result.Error != nil {
		return fmt.Errorf("%s: takeover lifecycle effect claim: %w", providerDeleteStageClaimAcquire, result.Error)
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("%w: lifecycle effect claim changed", backupasset.ErrConflict)
	}
	claim.ExecutorID = coordinator.effectExecutorID
	claim.ExecutionID = executionID
	claim.TransitionRevision = rows.attempt.TransitionRevision
	claim.LeaseID = rows.lease.ID
	claim.LeaseAttemptID = rows.lease.AttemptID
	claim.LeaseFenceTokenHash = hashFenceToken(rows.lease.FenceToken)
	claim.State = "in_flight"
	claim.DeadlineAt = deadline
	claim.HeartbeatAt = now.UTC()
	claim.UpdatedAt = now.UTC()
	return nil
}

func (coordinator *Coordinator) blockProviderDeleteObserverTx(
	ctx context.Context,
	tx *gorm.DB,
	rows *providerDeleteRows,
	reason backupasset.LifecycleBlockedReason,
	now time.Time,
) (LifecycleAttempt, error) {
	if rows == nil || !rows.claimFound || rows.claim.State == "proven" {
		return LifecycleAttempt{}, fmt.Errorf("%w: invalid observer block claim", backupasset.ErrInvalidState)
	}
	if err := coordinator.validateProviderDeleteLeaseIdentity(*rows, true, true); err != nil {
		return LifecycleAttempt{}, err
	}
	if rows.claim.State == "in_flight" {
		if err := coordinator.markEffectClaimUncertainTx(ctx, tx, &rows.claim, now); err != nil {
			return LifecycleAttempt{}, err
		}
	}
	blocked, err := coordinator.blockAttemptTx(ctx, tx, &rows.attempt, &rows.point, reason)
	if err != nil {
		return LifecycleAttempt{}, err
	}
	return blocked, nil
}

func providerDeleteObserverReason(err error) backupasset.LifecycleBlockedReason {
	if err == nil {
		return backupasset.LifecycleBlockedProviderDeleteUnproven
	}
	if errors.Is(err, provider.ErrDeletePointNativeVersionReferenced) {
		return backupasset.LifecycleBlockedProviderNativeVersionReferenced
	}
	if errors.Is(err, provider.ErrDeletePointIdentityConflict) || errors.Is(err, ErrPointDeletionIdentityConflict) {
		return backupasset.LifecycleBlockedProviderIdentityConflict
	}
	if errors.Is(err, backupasset.ErrProviderUnavailable) {
		return backupasset.LifecycleBlockedProviderUnavailable
	}
	var capabilityErr *provider.CapabilityError
	if errors.As(err, &capabilityErr) && capabilityErr.Reason.Code == backupasset.CapabilityDeletionUnavailable {
		return backupasset.LifecycleBlockedDeletionUnavailable
	}
	if errors.Is(err, ErrPointDeletionWORM) || errors.Is(err, provider.ErrDeletePointWORM) {
		return backupasset.LifecycleBlockedProviderWORM
	}
	return backupasset.LifecycleBlockedProviderDeleteUnproven
}

func providerDeleteObserverBlockReason(err error) (backupasset.LifecycleBlockedReason, bool) {
	switch {
	case errors.Is(err, provider.ErrDeletePointNativeVersionReferenced):
		return backupasset.LifecycleBlockedProviderNativeVersionReferenced, true
	case errors.Is(err, provider.ErrDeletePointIdentityConflict) || errors.Is(err, ErrPointDeletionIdentityConflict):
		return backupasset.LifecycleBlockedProviderIdentityConflict, true
	default:
		// WORM, unavailable, deletion-unavailable, unproven, cancellation,
		// and generic observer failures remain uncertain provider_delete.
		return "", false
	}
}

func (coordinator *Coordinator) scheduleProviderDeleteRetryTx(
	ctx context.Context,
	tx *gorm.DB,
	rows *providerDeleteRows,
	now time.Time,
) (LifecycleAttempt, error) {
	if rows == nil || !rows.claimFound ||
		(rows.claim.State != "in_flight" && rows.claim.State != "uncertain") {
		return LifecycleAttempt{}, fmt.Errorf("%s: %w: invalid provider-delete retry fact", providerDeleteStageClaimAcquire, backupasset.ErrInvalidState)
	}
	if err := coordinator.validateProviderDeleteLeaseIdentity(*rows, true, true); err != nil {
		return LifecycleAttempt{}, err
	}
	phase := backupasset.LifecyclePhase(rows.attempt.Phase)
	if phase != backupasset.LifecyclePhaseProviderDelete &&
		(phase != backupasset.LifecyclePhaseBlocked ||
			!providerDeleteClaimBlockedReason(backupasset.LifecycleBlockedReason(rows.attempt.BlockedReason))) {
		return LifecycleAttempt{}, fmt.Errorf("%s: %w: invalid provider-delete retry phase", providerDeleteStageClaimAcquire, backupasset.ErrInvalidState)
	}
	retryAt := now.UTC().Add(coordinator.retryDelay)
	updates := map[string]any{
		"retry_at": retryAt, "heartbeat_at": now.UTC(), "updated_at": now.UTC(),
	}
	if phase == backupasset.LifecyclePhaseBlocked {
		updates["phase"] = backupasset.LifecyclePhaseProviderDelete
		updates["blocked_reason"] = ""
		updates["transition_revision"] = rows.attempt.TransitionRevision + 1
	}
	result := tx.WithContext(ctx).Model(&model.RecoveryPointLifecycleAttempt{}).
		Where("id = ? AND phase = ? AND transition_revision = ?",
			rows.attempt.ID, phase, rows.attempt.TransitionRevision).
		Updates(updates)
	if result.Error != nil {
		return LifecycleAttempt{}, fmt.Errorf("%s: schedule provider-delete retry: %w", providerDeleteStageClaimAcquire, result.Error)
	}
	if result.RowsAffected != 1 {
		return LifecycleAttempt{}, fmt.Errorf("%w: provider-delete retry attempt changed", backupasset.ErrConflict)
	}
	if phase == backupasset.LifecyclePhaseBlocked {
		rows.attempt.Phase = string(backupasset.LifecyclePhaseProviderDelete)
		rows.attempt.BlockedReason = ""
		rows.attempt.TransitionRevision++
	}
	rows.attempt.RetryAt = &retryAt
	rows.attempt.HeartbeatAt = &now
	rows.attempt.UpdatedAt = now
	return lifecycleAttemptFromModel(rows.attempt), nil
}
func (coordinator *Coordinator) clearDueProviderDeleteRetryAtTx(
	ctx context.Context,
	tx *gorm.DB,
	rows *providerDeleteRows,
	now time.Time,
) error {
	if rows == nil || rows.attempt.RetryAt == nil || now.Before(rows.attempt.RetryAt.UTC()) {
		return nil
	}
	if err := coordinator.validateProviderDeleteLeaseIdentity(*rows, true, true); err != nil {
		return err
	}
	phase := backupasset.LifecyclePhase(rows.attempt.Phase)
	if phase != backupasset.LifecyclePhaseProviderDelete &&
		(phase != backupasset.LifecyclePhaseBlocked ||
			!providerDeleteClaimBlockedReason(backupasset.LifecycleBlockedReason(rows.attempt.BlockedReason))) {
		return fmt.Errorf("%s: %w: invalid provider-delete retry phase", providerDeleteStageClaimAcquire, backupasset.ErrInvalidState)
	}
	retryAt := rows.attempt.RetryAt.UTC()
	result := tx.WithContext(ctx).Model(&model.RecoveryPointLifecycleAttempt{}).
		Where("id = ? AND phase = ? AND transition_revision = ? AND retry_at = ?",
			rows.attempt.ID, phase, rows.attempt.TransitionRevision, retryAt).
		Updates(map[string]any{"retry_at": nil, "heartbeat_at": now.UTC(), "updated_at": now.UTC()})
	if result.Error != nil {
		return fmt.Errorf("%s: clear provider-delete retry: %w", providerDeleteStageClaimAcquire, result.Error)
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("%w: provider-delete retry attempt changed", backupasset.ErrConflict)
	}
	rows.attempt.RetryAt = nil
	rows.attempt.HeartbeatAt = &now
	rows.attempt.UpdatedAt = now
	return nil
}

func (coordinator *Coordinator) prepareProviderDelete(ctx context.Context, attemptID string) (providerDeletePreparation, error) {
	var prepared providerDeletePreparation
	if coordinator == nil || coordinator.db == nil || coordinator.deleter == nil {
		return prepared, fmt.Errorf("%s: %w: provider deletion is unavailable", providerDeleteStagePreClaim, backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	err := coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		rows, err := lockProviderDeleteRowsByAttemptTx(ctx, tx, attemptID)
		if err != nil {
			return err
		}
		prepared.attempt = lifecycleAttemptFromModel(rows.attempt)
		prepared.claimFound = rows.claimFound
		proof, proofFound, err := coordinator.validateProviderDeleteRowsAuthority(
			rows, rows.claimFound && !rows.tombstoneFound, true,
		)
		if err != nil {
			return err
		}
		if proofFound {
			// Durable provider proof wins over stale claim/lease/deadline
			// state. Do not classify or mutate the claim before returning it.
			prepared.proof = &proof
			prepared.proofTombstone = true
			return nil
		}
		if rows.claimFound {
			if err := requireProviderDeleteLease(rows); err != nil {
				return err
			}
		}
		now := coordinator.now().UTC()
		if rows.claimFound {
			if err := coordinator.validateProviderDeleteLeaseIdentity(rows, true, true); err != nil {
				return err
			}
		}
		if rows.claimFound && rows.claim.State == "in_flight" {
			claimCurrent, matchErr := coordinator.providerDeleteClaimMatchesCurrent(rows, now)
			if matchErr != nil {
				return matchErr
			}
			if claimCurrent {
				prepared.attempt = lifecycleAttemptFromModel(rows.attempt)
				return ErrEffectClaimInFlight
			}
			if err := coordinator.markEffectClaimUncertainTx(ctx, tx, &rows.claim, now); err != nil {
				return err
			}
		}
		retryDue := false
		if rows.claimFound && rows.claim.State == "uncertain" && rows.attempt.RetryAt != nil {
			if now.Before(rows.attempt.RetryAt.UTC()) {
				// A durable effect retry owns resumption of an uncertain
				// provider delete. Do not let a direct Advance or worker pass
				// churn the observer, lease, claim, or provider before it is
				// due.
				prepared.attempt = lifecycleAttemptFromModel(rows.attempt)
				prepared.retryScheduled = true
				return nil
			}
			retryDue = true
		}
		if retryDue {
			if err := coordinator.clearDueProviderDeleteRetryAtTx(ctx, tx, &rows, now); err != nil {
				return err
			}
		}
		if rows.claimFound {
			held, err := coordinator.activeHoldTx(ctx, tx, rows.point)
			if err != nil {
				return err
			}
			if held {
				blocked, err := coordinator.blockProviderDeleteObserverTx(ctx, tx, &rows, backupasset.LifecycleBlockedActiveHold, now)
				if err != nil {
					return err
				}
				prepared.attempt, prepared.blocked = blocked, true
				return nil
			}
			observerNow := coordinator.now().UTC()
			observerCtx, observerCancel, err := coordinator.providerDeleteBoundContext(
				ctx, observerNow.Add(coordinator.effectClaimTTL),
			)
			if err != nil {
				return err
			}
			observerRequest := coordinator.lifecyclePointRequestFromRows(rows, false)
			observer, observerErr := coordinator.deleter.Prepare(
				observerCtx, tx, providerDeletePrepareObserver, observerRequest, rows.lifecycleDeleteRows,
			)
			observerContextErr := observerCtx.Err()
			observerCancel()
			observerNow = coordinator.now().UTC()
			if observerContextErr != nil {
				scheduled, scheduleErr := coordinator.scheduleProviderDeleteRetryTx(
					ctx, tx, &rows, observerNow,
				)
				if scheduleErr != nil {
					return scheduleErr
				}
				prepared.attempt, prepared.retryScheduled = scheduled, true
				prepared.claimFound = true
				return nil
			}
			if observerErr != nil {
				if reason, ok := providerDeleteObserverBlockReason(observerErr); ok {
					blocked, blockErr := coordinator.blockProviderDeleteObserverTx(
						ctx, tx, &rows, reason, observerNow,
					)
					if blockErr != nil {
						return blockErr
					}
					prepared.attempt, prepared.blocked = blocked, true
					return nil
				}
				scheduled, scheduleErr := coordinator.scheduleProviderDeleteRetryTx(
					ctx, tx, &rows, observerNow,
				)
				if scheduleErr != nil {
					return scheduleErr
				}
				prepared.attempt, prepared.retryScheduled = scheduled, true
				prepared.claimFound = true
				return nil
			}
			if observer.identityDigest != rows.claim.TargetIdentityDigest {
				blocked, err := coordinator.blockProviderDeleteObserverTx(
					ctx, tx, &rows,
					backupasset.LifecycleBlockedProviderIdentityConflict, observerNow,
				)
				if err != nil {
					return err
				}
				prepared.attempt, prepared.blocked = blocked, true
				return nil
			}
			if err := coordinator.prepareProviderDeleteTakeoverTx(ctx, tx, &rows, observer, observerNow, &prepared); err != nil {
				return err
			}
			return nil
		}
		if err := requireProviderDeleteLease(rows); err != nil {
			return err
		}
		// Only an unclaimed first acquisition may renew/ensure the lifecycle
		// fence before resolving the execution target.
		held, err := coordinator.activeHoldTx(ctx, tx, rows.point)
		if err != nil {
			return err
		}
		if held {
			blocked, err := coordinator.blockAttemptTx(ctx, tx, &rows.attempt, &rows.point, backupasset.LifecycleBlockedActiveHold)
			if err != nil {
				return err
			}
			prepared.attempt, prepared.blocked = blocked, true
			return nil
		}
		if err := coordinator.ensureLifecycleFenceTx(ctx, tx, &rows.attempt); err != nil {
			blocked, blockErr := coordinator.blockAttemptTx(ctx, tx, &rows.attempt, &rows.point, backupasset.LifecycleBlockedProviderDeleteUnproven)
			if blockErr != nil {
				return errors.Join(err, blockErr)
			}
			prepared.attempt, prepared.blocked = blocked, true
			return nil
		}
		if err := refreshProviderDeleteRowsAfterFenceTx(ctx, tx, &rows); err != nil {
			return err
		}
		executionCtx, executionCancel, err := coordinator.providerDeleteBoundContext(
			ctx, rows.lease.AbsoluteDeadline.UTC(),
		)
		if err != nil {
			return err
		}
		request := coordinator.lifecyclePointRequestFromRows(rows, true)
		executionPrepared, prepareErr := coordinator.deleter.Prepare(
			executionCtx, tx, providerDeletePrepareExecution, request, rows.lifecycleDeleteRows,
		)
		executionContextErr := executionCtx.Err()
		executionCancel()
		if prepareErr != nil {
			if executionContextErr != nil ||
				errors.Is(prepareErr, context.Canceled) ||
				errors.Is(prepareErr, context.DeadlineExceeded) {
				return fmt.Errorf("%w: %w", errProviderDeletePreparationAuthority, prepareErr)
			}
			blocked, blockErr := coordinator.blockAttemptTx(ctx, tx, &rows.attempt, &rows.point, providerDeleteObserverReason(prepareErr))
			if blockErr != nil {
				return errors.Join(prepareErr, blockErr)
			}
			prepared.attempt, prepared.blocked = blocked, true
			return nil
		}
		if executionContextErr != nil {
			return fmt.Errorf("%w: %w", errProviderDeletePreparationAuthority, executionContextErr)
		}
		freshNow, err := coordinator.revalidateProviderDeletePreparationTx(
			ctx, tx, &rows, executionPrepared,
		)
		if err != nil {
			return err
		}
		executionID, err := backupasset.NewOpaqueID()
		if err != nil {
			return fmt.Errorf("%s: generate lifecycle execution ID: %w", providerDeleteStageClaimAcquire, err)
		}
		// From this point the INSERT can be committed even if the surrounding
		// transaction later reports an ambiguous error. Keep a conservative
		// marker so callers never route a possibly durable acquisition through
		// the legacy generic block path.
		prepared.acquisitionMayHaveCommitted = true
		if err := coordinator.insertEffectClaimTx(ctx, tx, &rows, executionPrepared, executionID, freshNow); err != nil {
			return err
		}
		prepared.attempt = lifecycleAttemptFromModel(rows.attempt)
		prepared.claimFound = true
		prepared.prepared, prepared.binding, prepared.acquired = executionPrepared, providerDeleteBindingFromRows(coordinator, rows, executionID, freshNow), true
		return nil
	})
	if err != nil {
		return prepared, err
	}
	return prepared, nil
}

// providerDeletePreparationFailureObservation is the locked truth used when
// Tx1 returns an error after (or before) attempting claim acquisition. The
// observation deliberately carries no mutable rows out of the transaction:
// callers either prove a tombstone, return a claim observation, or use the
// same repository-first transaction to commit the legacy pre-claim block.
type providerDeletePreparationFailureObservation struct {
	attempt        LifecycleAttempt
	proof          PointDeletionResult
	proofFound     bool
	claimFound     bool
	claimCurrent   bool
	retryScheduled bool
	blocked        bool
}

// resolveProviderDeletePreparationFailure reconciles a failed preparation
// without assuming that Tx1 rolled back. In particular, a claim that became
// durable despite an ambiguous transaction error is never rewritten into a
// generic blocked fact. A no-claim block is committed while the repository,
// point and attempt rows remain locked, so a concurrent acquisition cannot
// slip between observation and blocking.
func (coordinator *Coordinator) resolveProviderDeletePreparationFailure(
	ctx context.Context,
	attemptID string,
	reason backupasset.LifecycleBlockedReason,
	cause error,
) (LifecycleAttempt, error) {
	var observation providerDeletePreparationFailureObservation
	if coordinator == nil || coordinator.db == nil {
		return LifecycleAttempt{}, fmt.Errorf("%w: provider-delete failure reconciliation unavailable", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	err := coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		rows, err := lockProviderDeleteRowsByAttemptTx(ctx, tx, attemptID)
		if err != nil {
			return err
		}
		observation.attempt = lifecycleAttemptFromModel(rows.attempt)
		proof, proofFound, err := validateProviderDeleteRowsProofFirst(rows)
		if err != nil {
			return err
		}
		if proofFound {
			observation.proof, observation.proofFound = proof, true
			return nil
		}
		if identityErr := coordinator.validateProviderDeleteLeaseIdentity(rows, rows.claimFound && !rows.tombstoneFound, true); identityErr != nil {
			if errors.Is(identityErr, errLifecycleFenceLost) &&
				!rows.claimFound && !rows.tombstoneFound &&
				backupasset.LifecyclePhase(rows.attempt.Phase) == backupasset.LifecyclePhaseProviderDelete {
				blocked, blockErr := coordinator.blockAttemptTx(
					ctx, tx, &rows.attempt, &rows.point,
					backupasset.LifecycleBlockedProviderDeleteUnproven,
				)
				if blockErr != nil {
					return blockErr
				}
				observation.attempt, observation.blocked = blocked, true
				return nil
			}
			return identityErr
		}
		if rows.claimFound {
			observation.claimFound = true
			now := coordinator.now().UTC()
			if err := coordinator.validateProviderDeleteLeaseIdentity(rows, true, true); err != nil {
				return err
			}
			var matchErr error
			observation.claimCurrent, matchErr = coordinator.providerDeleteClaimMatchesCurrent(rows, now)
			if matchErr != nil {
				return matchErr
			}
			if rows.claim.State == "uncertain" && rows.attempt.RetryAt != nil {
				observation.retryScheduled = now.Before(rows.attempt.RetryAt.UTC())
			}
			// A claim owns the effect authority. Do not mutate either the
			// claim or its attempt while reconciling another transaction's
			// ambiguous/pre-claim error.
			return nil
		}
		if backupasset.LifecyclePhase(rows.attempt.Phase) != backupasset.LifecyclePhaseProviderDelete ||
			!providerDeleteLifecycleOperation(backupasset.LifecycleOperation(rows.attempt.Operation)) {
			return fmt.Errorf("%w: lifecycle phase changed during provider-delete failure reconciliation", backupasset.ErrConflict)
		}
		blocked, err := coordinator.blockAttemptTx(ctx, tx, &rows.attempt, &rows.point, reason)
		if err != nil {
			return err
		}
		observation.attempt, observation.blocked = blocked, true
		return nil
	})
	if err != nil {
		return LifecycleAttempt{}, err
	}
	if observation.proofFound {
		return coordinator.confirmSettledProviderDelete(ctx, observation.attempt, observation.proof)
	}
	if observation.claimFound {
		if observation.claimCurrent {
			return observation.attempt, ErrEffectClaimInFlight
		}
		if observation.retryScheduled {
			return observation.attempt, nil
		}
		if cause != nil {
			return observation.attempt, cause
		}
		return observation.attempt, nil
	}
	if observation.blocked {
		return coordinator.auditProviderDeleteBlock(ctx, observation.attempt)
	}
	if cause != nil {
		return observation.attempt, cause
	}
	return observation.attempt, nil
}

type providerDeleteBinding struct {
	AttemptID            string
	ExecutorID           string
	ExecutionID          string
	TargetIdentityDigest string
	TransitionRevision   int64
	LeaseID              string
	LeaseAttemptID       string
	LeaseFenceTokenHash  string
	DeadlineAt           time.Time
	AbsoluteDeadlineAt   time.Time
}

type providerDeletePreparation struct {
	attempt        LifecycleAttempt
	prepared       preparedPointDeletion
	binding        providerDeleteBinding
	proof          *PointDeletionResult
	proofTombstone bool
	blocked        bool
	retryScheduled bool
	acquired       bool
	// Set before an insert/takeover CAS. A commit can be durable even when
	// GORM returns an error, so this marker must outlive the transaction error.
	acquisitionMayHaveCommitted bool
	claimFound                  bool
}

func providerDeleteBindingFromRows(coordinator *Coordinator, rows providerDeleteRows, executionID string, now time.Time) providerDeleteBinding {
	return providerDeleteBinding{
		AttemptID: rows.attempt.ID, ExecutorID: coordinator.effectExecutorID, ExecutionID: executionID,
		TargetIdentityDigest: rows.claim.TargetIdentityDigest, TransitionRevision: rows.attempt.TransitionRevision,
		LeaseID: rows.claim.LeaseID, LeaseAttemptID: rows.claim.LeaseAttemptID,
		LeaseFenceTokenHash: rows.claim.LeaseFenceTokenHash, DeadlineAt: rows.claim.DeadlineAt.UTC(),
		AbsoluteDeadlineAt: rows.lease.AbsoluteDeadline.UTC(),
	}
}
func validateProviderDeleteReceiptBinding(
	rows providerDeleteRows,
	binding providerDeleteBinding,
	now time.Time,
) error {
	if !rows.claimFound || rows.claim.ID == "" || rows.claim.AttemptID != binding.AttemptID ||
		rows.claim.State != "in_flight" || rows.claim.ExecutorID != binding.ExecutorID ||
		rows.claim.ExecutionID != binding.ExecutionID ||
		rows.claim.TargetIdentityDigest != binding.TargetIdentityDigest ||
		rows.claim.TransitionRevision != binding.TransitionRevision ||
		rows.claim.LeaseID != binding.LeaseID ||
		rows.claim.LeaseAttemptID != binding.LeaseAttemptID ||
		rows.claim.LeaseFenceTokenHash != binding.LeaseFenceTokenHash {
		return fmt.Errorf("%s: %w: lifecycle effect claim changed", providerDeleteStageReceipt, backupasset.ErrConflict)
	}
	if rows.attempt.ID != binding.AttemptID || rows.attempt.TransitionRevision != binding.TransitionRevision ||
		rows.attempt.LeaseID == nil || rows.attempt.LeaseAttemptID == nil ||
		rows.attempt.LeaseFenceTokenHash == nil ||
		*rows.attempt.LeaseID != binding.LeaseID ||
		*rows.attempt.LeaseAttemptID != binding.LeaseAttemptID ||
		*rows.attempt.LeaseFenceTokenHash != binding.LeaseFenceTokenHash {
		return fmt.Errorf("%s: %w: lifecycle effect authority changed", providerDeleteStageReceipt, backupasset.ErrConflict)
	}
	if !rows.leaseFound || rows.lease.ID != binding.LeaseID ||
		rows.lease.AttemptID != binding.LeaseAttemptID ||
		hashFenceToken(rows.lease.FenceToken) != binding.LeaseFenceTokenHash ||
		backupasset.LeaseStatus(rows.lease.Status) != backupasset.LeaseActive ||
		binding.AbsoluteDeadlineAt.IsZero() ||
		!rows.lease.AbsoluteDeadline.UTC().Equal(binding.AbsoluteDeadlineAt.UTC()) {
		return fmt.Errorf("%s: %w: lifecycle effect lease authority changed", providerDeleteStageReceipt, backupasset.ErrConflict)
	}
	claimDeadline := rows.claim.DeadlineAt.UTC()
	leaseDeadline := rows.lease.LeaseExpiresAt.UTC()
	absoluteDeadline := rows.lease.AbsoluteDeadline.UTC()
	if !claimDeadline.After(now.UTC()) || !leaseDeadline.After(now.UTC()) ||
		!absoluteDeadline.After(now.UTC()) ||
		claimDeadline.After(leaseDeadline) || claimDeadline.After(absoluteDeadline) {
		return fmt.Errorf("%s: %w: lifecycle effect deadline reached", providerDeleteStageReceipt, backupasset.ErrConflict)
	}
	return nil
}

func refreshProviderDeleteRowsAfterFenceTx(ctx context.Context, tx *gorm.DB, rows *providerDeleteRows) error {
	if rows == nil {
		return fmt.Errorf("%w: lifecycle deletion rows unavailable", backupasset.ErrInvalidState)
	}
	var attempt model.RecoveryPointLifecycleAttempt
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", rows.attempt.ID).Limit(1).Find(&attempt).Error; err != nil {
		return err
	}
	rows.attempt = attempt
	if attempt.LeaseID == nil {
		return lifecycleDeleteIdentityConflict("lifecycle lease authority is incomplete")
	}
	var lease model.RecoveryPointLease
	loaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", *attempt.LeaseID).Limit(1).Find(&lease)
	if loaded.Error != nil || loaded.RowsAffected != 1 {
		return lifecycleDeleteIdentityConflict("lifecycle lease authority changed")
	}
	rows.lease = lease
	return nil
}

func (coordinator *Coordinator) prepareProviderDeleteTakeoverTx(ctx context.Context, tx *gorm.DB, rows *providerDeleteRows, observer preparedPointDeletion, now time.Time, output *providerDeletePreparation) error {
	if rows == nil || output == nil {
		return fmt.Errorf("%s: %w: lifecycle takeover rows unavailable", providerDeleteStageClaimAcquire, backupasset.ErrInvalidState)
	}
	if err := coordinator.takeoverProviderLeaseTx(ctx, tx, rows); err != nil {
		return err
	}
	// A blocked observer fact is resumed only after the same-digest authority
	// has been atomically rebound. Do not call the legacy phase adopter here.
	if backupasset.RecoveryPointState(rows.point.State) == backupasset.RecoveryPointPurgeBlocked {
		result := tx.WithContext(ctx).Model(&model.RecoveryPoint{}).Where("id = ? AND point_revision = ? AND state = ?", rows.point.ID, rows.point.PointRevision, backupasset.RecoveryPointPurgeBlocked).
			Updates(map[string]any{"state": backupasset.RecoveryPointExpiring, "point_revision": rows.point.PointRevision + 1, "updated_at": now.UTC()})
		if result.Error != nil || result.RowsAffected != 1 {
			return fmt.Errorf("%w: resume lifecycle point", backupasset.ErrConflict)
		}
		rows.point.State = string(backupasset.RecoveryPointExpiring)
		rows.point.PointRevision++
	}
	if backupasset.LifecyclePhase(rows.attempt.Phase) == backupasset.LifecyclePhaseBlocked {
		result := tx.WithContext(ctx).Model(&model.RecoveryPointLifecycleAttempt{}).Where("id = ? AND phase = ? AND transition_revision = ?", rows.attempt.ID, backupasset.LifecyclePhaseBlocked, rows.attempt.TransitionRevision).
			Updates(map[string]any{"phase": backupasset.LifecyclePhaseProviderDelete, "blocked_reason": "", "retry_at": nil, "transition_revision": rows.attempt.TransitionRevision + 1, "heartbeat_at": now.UTC(), "updated_at": now.UTC()})
		if result.Error != nil || result.RowsAffected != 1 {
			return fmt.Errorf("%w: resume lifecycle attempt", backupasset.ErrConflict)
		}
		rows.attempt.Phase = string(backupasset.LifecyclePhaseProviderDelete)
		rows.attempt.BlockedReason = ""
		rows.attempt.RetryAt = nil
		rows.attempt.TransitionRevision++
		rows.attempt.HeartbeatAt = &now
		rows.attempt.UpdatedAt = now
	}
	if err := refreshProviderDeleteRowsAfterFenceTx(ctx, tx, rows); err != nil {
		return err
	}
	executionCtx, executionCancel, err := coordinator.providerDeleteBoundContext(
		ctx, rows.lease.AbsoluteDeadline.UTC(),
	)
	if err != nil {
		return err
	}
	request := coordinator.lifecyclePointRequestFromRows(*rows, true)
	executionPrepared, prepareErr := coordinator.deleter.Prepare(
		executionCtx, tx, providerDeletePrepareExecution, request, rows.lifecycleDeleteRows,
	)
	executionErr := executionCtx.Err()
	executionCancel()
	if prepareErr != nil {
		if executionErr != nil {
			return fmt.Errorf("%s: %w: %w", providerDeleteStageClaimAcquire, errProviderDeletePreparationAuthority, prepareErr)
		}
		return fmt.Errorf("%s: %w", providerDeleteStageClaimAcquire, prepareErr)
	}
	if executionErr != nil {
		return fmt.Errorf("%s: %w: %w", providerDeleteStageClaimAcquire, errProviderDeletePreparationAuthority, executionErr)
	}
	freshNow, err := coordinator.revalidateProviderDeletePreparationTx(
		ctx, tx, rows, executionPrepared,
	)
	if err != nil {
		return err
	}
	if executionPrepared.identityDigest != rows.claim.TargetIdentityDigest {
		return fmt.Errorf("%s: %w: deletion target digest changed during takeover", providerDeleteStageClaimAcquire, provider.ErrDeletePointIdentityConflict)
	}
	executionID, err := backupasset.NewOpaqueID()
	if err != nil {
		return fmt.Errorf("%s: generate lifecycle execution ID: %w", providerDeleteStageClaimAcquire, err)
	}
	output.acquisitionMayHaveCommitted = true
	if err := coordinator.updateEffectClaimTakeoverTx(ctx, tx, &rows.claim, rows, executionPrepared, executionID, freshNow); err != nil {
		return err
	}
	output.attempt = lifecycleAttemptFromModel(rows.attempt)
	output.prepared = executionPrepared
	output.binding = providerDeleteBindingFromRows(coordinator, *rows, executionID, freshNow)
	output.acquired = true
	return nil
}
func (coordinator *Coordinator) takeoverProviderLeaseTx(ctx context.Context, tx *gorm.DB, rows *providerDeleteRows) error {
	now := coordinator.now().UTC()
	lease := rows.lease
	if lease.RecoveryPointID != rows.attempt.RecoveryPointID {
		return lifecycleDeleteIdentityConflict("lifecycle lease point changed")
	}
	if err := coordinator.validateProviderDeleteLeaseIdentity(*rows, rows.claimFound, true); err != nil {
		return err
	}
	if backupasset.LeaseStatus(lease.Status) == backupasset.LeaseActive {
		if !now.Before(lease.AbsoluteDeadline.UTC()) {
			result := tx.WithContext(ctx).Model(&model.RecoveryPointLease{}).
				Where("id = ? AND status = ? AND absolute_deadline <= ?", lease.ID, backupasset.LeaseActive, now).
				Updates(map[string]any{"status": backupasset.LeaseExpired, "updated_at": now})
			if result.Error != nil {
				return fmt.Errorf("%s: expire lifecycle lease: %w: %w", providerDeleteStageClaimAcquire, backupasset.ErrConflict, result.Error)
			}
			if result.RowsAffected != 1 {
				return fmt.Errorf("%s: expire lifecycle lease: %w", providerDeleteStageClaimAcquire, backupasset.ErrConflict)
			}
			lease.Status = string(backupasset.LeaseExpired)
		} else if !now.Before(lease.LeaseExpiresAt.UTC()) {
			taken, err := coordinator.leases.TakeoverTx(ctx, tx, backupasset.TakeoverLeaseRequest{
				LeaseID: lease.ID, OwnerID: coordinator.leaseOwnerID,
			})
			if err != nil {
				return fmt.Errorf("%s: takeover lifecycle lease: %w: %w", providerDeleteStageClaimAcquire, backupasset.ErrConflict, err)
			}
			lease = modelLeaseFromPublic(taken)
		} else {
			// A digest-matching claim may still hold an active short lease.
			// Renew its exact locked fence before preparing execution so the
			// claim deadline cannot be committed at (or immediately before)
			// the old lease expiry.
			renewed, err := coordinator.leases.RenewTx(ctx, tx, rows.leaseFence())
			if err != nil {
				return fmt.Errorf("%s: renew lifecycle lease: %w: %w", providerDeleteStageClaimAcquire, backupasset.ErrConflict, err)
			}
			lease = modelLeaseFromPublic(renewed)
		}
	}
	if backupasset.LeaseStatus(lease.Status) != backupasset.LeaseActive {
		fresh, err := coordinator.leases.AcquireTx(ctx, tx, backupasset.AcquireLeaseRequest{
			RecoveryPointID: rows.attempt.RecoveryPointID,
			HolderType:      backupasset.LeaseHolderRetentionWorker,
			OwnerID:         coordinator.leaseOwnerID,
		})
		if err != nil {
			return fmt.Errorf("%s: acquire lifecycle takeover lease: %w: %w", providerDeleteStageClaimAcquire, backupasset.ErrConflict, err)
		}
		lease = modelLeaseFromPublic(fresh)
	}
	fenceHash := hashFenceToken(lease.FenceToken)
	changedLease := rows.attempt.LeaseID == nil || *rows.attempt.LeaseID != lease.ID ||
		rows.attempt.LeaseAttemptID == nil || *rows.attempt.LeaseAttemptID != lease.AttemptID ||
		rows.attempt.LeaseFenceTokenHash == nil || *rows.attempt.LeaseFenceTokenHash != fenceHash
	if changedLease {
		oldRevision := rows.attempt.TransitionRevision
		result := tx.WithContext(ctx).Model(&model.RecoveryPointLifecycleAttempt{}).
			Where("id = ? AND transition_revision = ?", rows.attempt.ID, oldRevision).
			Updates(map[string]any{
				"lease_id": lease.ID, "lease_attempt_id": lease.AttemptID, "lease_fence_token_hash": fenceHash,
				"transition_revision": oldRevision + 1, "heartbeat_at": now, "updated_at": now,
			})
		if result.Error != nil {
			return fmt.Errorf("%s: bind lifecycle takeover lease: %w: %w", providerDeleteStageClaimAcquire, backupasset.ErrConflict, result.Error)
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("%s: bind lifecycle takeover lease: %w", providerDeleteStageClaimAcquire, backupasset.ErrConflict)
		}
		rows.attempt.LeaseID = &lease.ID
		rows.attempt.LeaseAttemptID = &lease.AttemptID
		rows.attempt.LeaseFenceTokenHash = &fenceHash
		rows.attempt.TransitionRevision++
		rows.attempt.HeartbeatAt = &now
		rows.attempt.UpdatedAt = now
	}
	rows.lease = lease
	return nil
}

func modelLeaseFromPublic(lease backupasset.Lease) model.RecoveryPointLease {
	return model.RecoveryPointLease{ID: lease.ID, RecoveryPointID: lease.RecoveryPointID, HolderType: string(lease.HolderType), OwnerID: lease.OwnerID, AttemptID: lease.Fence.AttemptID, FenceToken: lease.Fence.FenceToken, Status: string(lease.Status), LeaseExpiresAt: lease.LeaseExpiresAt, AbsoluteDeadline: lease.AbsoluteDeadline, LastHeartbeatAt: lease.LastHeartbeatAt, ReleasedAt: lease.ReleasedAt}
}
func (coordinator *Coordinator) markEffectClaimUncertain(ctx context.Context, binding providerDeleteBinding, schedule bool) error {
	if coordinator == nil || coordinator.db == nil {
		return fmt.Errorf("%w: lifecycle effect claim persistence unavailable", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		rows, err := lockProviderDeleteRowsByAttemptTx(ctx, tx, binding.AttemptID)
		if err != nil {
			return err
		}
		_, proofFound, err := coordinator.validateProviderDeleteRowsAuthority(
			rows, rows.claimFound && !rows.tombstoneFound, true,
		)
		if err != nil {
			return err
		}
		if proofFound || !rows.claimFound || rows.claim.ID == "" || rows.claim.State == "proven" {
			return nil
		}
		if rows.claim.ExecutorID != binding.ExecutorID || rows.claim.ExecutionID != binding.ExecutionID ||
			rows.claim.TargetIdentityDigest != binding.TargetIdentityDigest || rows.claim.TransitionRevision != binding.TransitionRevision ||
			rows.claim.LeaseID != binding.LeaseID || rows.claim.LeaseAttemptID != binding.LeaseAttemptID ||
			rows.claim.LeaseFenceTokenHash != binding.LeaseFenceTokenHash {
			return nil
		}
		now := coordinator.now().UTC()
		if rows.claim.State == "in_flight" {
			if err := coordinator.markEffectClaimUncertainTx(ctx, tx, &rows.claim, now); err != nil {
				return err
			}
		}
		if schedule && backupasset.LifecyclePhase(rows.attempt.Phase) == backupasset.LifecyclePhaseProviderDelete {
			retryAt := now.Add(coordinator.retryDelay)
			result := tx.WithContext(ctx).Model(&model.RecoveryPointLifecycleAttempt{}).
				Where("id = ? AND phase = ? AND transition_revision = ?", rows.attempt.ID, backupasset.LifecyclePhaseProviderDelete, rows.attempt.TransitionRevision).
				Updates(map[string]any{"retry_at": retryAt, "heartbeat_at": now, "updated_at": now})
			if result.Error != nil {
				return result.Error
			}
		}
		return nil
	})
}

// persistProviderDeleteReceiptWithClaim is Tx2. It verifies the exact stable
// provider authority and claim binding before writing the immutable receipt.
func (coordinator *Coordinator) persistProviderDeleteReceiptWithClaim(ctx context.Context, preparation providerDeletePreparation, execution pointDeletionExecution) (LifecycleAttempt, error) {
	var persisted LifecycleAttempt
	if ctx == nil {
		ctx = context.Background()
	}
	err := coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		rows, err := lockProviderDeleteRowsByAttemptTx(ctx, tx, preparation.attempt.ID)
		if err != nil {
			return err
		}
		proof, proofFound, err := coordinator.validateProviderDeleteRowsAuthority(
			rows, rows.claimFound && !rows.tombstoneFound, true,
		)
		if err != nil {
			return err
		}
		if proofFound {
			if proof != execution.Result {
				return fmt.Errorf("%w: lifecycle tombstone result changed", backupasset.ErrInvalidState)
			}
			persisted = lifecycleAttemptFromModel(rows.attempt)
			return nil
		}
		if !rows.claimFound || rows.claim.State != "in_flight" ||
			rows.claim.ExecutorID != preparation.binding.ExecutorID ||
			rows.claim.ExecutionID != preparation.binding.ExecutionID ||
			rows.claim.TargetIdentityDigest != preparation.binding.TargetIdentityDigest ||
			rows.claim.TransitionRevision != preparation.binding.TransitionRevision ||
			rows.claim.LeaseID != preparation.binding.LeaseID ||
			rows.claim.LeaseAttemptID != preparation.binding.LeaseAttemptID ||
			rows.claim.LeaseFenceTokenHash != preparation.binding.LeaseFenceTokenHash {
			return fmt.Errorf("%s: %w: lifecycle effect claim changed", providerDeleteStageReceipt, backupasset.ErrConflict)
		}
		now := coordinator.now().UTC()
		if err := coordinator.validateProviderDeleteLeaseAuthority(rows, now, true, true, false); err != nil {
			return err
		}
		if !rows.claim.DeadlineAt.UTC().After(now) {
			return fmt.Errorf("%s: %w: lifecycle effect claim deadline reached", providerDeleteStageReceipt, backupasset.ErrConflict)
		}
		request := coordinator.lifecyclePointRequestFromRows(rows, true)
		held, err := coordinator.activeHoldTx(ctx, tx, rows.point)
		if err != nil {
			return err
		}
		if err := validateProviderDeleteReceiptRows(request, rows.attempt, rows.point, rows.lease, now, held, true); err != nil {
			return fmt.Errorf("%s: %w", providerDeleteStageReceipt, err)
		}

		verifyCtx, verifyCancel, err := coordinator.providerDeleteBoundContext(
			ctx, rows.lease.AbsoluteDeadline.UTC(),
		)
		if err != nil {
			return fmt.Errorf("%s: %w", providerDeleteStageVerify, err)
		}
		verifyErr := coordinator.deleter.Verify(
			verifyCtx, tx, request, preparation.prepared, rows.lifecycleDeleteRows,
		)
		verifyContextErr := verifyCtx.Err()
		verifyCancel()
		if verifyErr != nil {
			if verifyContextErr != nil {
				return fmt.Errorf("%s: %w", providerDeleteStageVerify, errors.Join(verifyErr, verifyContextErr))
			}
			return fmt.Errorf("%s: %w", providerDeleteStageVerify, verifyErr)
		}
		if verifyContextErr != nil {
			return fmt.Errorf("%s: %w", providerDeleteStageVerify, verifyContextErr)
		}

		refreshed, err := lockProviderDeleteRowsTx(ctx, tx, request, true)
		if err != nil {
			return fmt.Errorf("%s: %w", providerDeleteStageVerify, err)
		}
		rows = refreshed
		proof, proofFound, proofErr := coordinator.validateProviderDeleteRowsAuthority(
			rows, rows.claimFound && !rows.tombstoneFound, true,
		)
		if proofErr != nil {
			return fmt.Errorf("%s: %w", providerDeleteStageVerify, proofErr)
		}
		if proofFound {
			if proof != execution.Result {
				return fmt.Errorf("%w: lifecycle tombstone result changed", backupasset.ErrInvalidState)
			}
			persisted = lifecycleAttemptFromModel(rows.attempt)
			return nil
		}
		freshNow := coordinator.now().UTC()
		if err := coordinator.validateProviderDeleteLeaseAuthority(rows, freshNow, true, true, false); err != nil {
			return err
		}
		if !freshNow.Before(rows.lease.AbsoluteDeadline.UTC()) {
			return fmt.Errorf("%s: %w: lifecycle lease absolute deadline reached", providerDeleteStageVerify, context.DeadlineExceeded)
		}
		request = coordinator.lifecyclePointRequestFromRows(rows, true)
		held, err = coordinator.activeHoldTx(ctx, tx, rows.point)
		if err != nil {
			return err
		}
		if err := validateProviderDeleteReceiptRows(request, rows.attempt, rows.point, rows.lease, freshNow, held, true); err != nil {
			return fmt.Errorf("%s: %w", providerDeleteStageVerify, err)
		}
		if err := validateProviderDeleteReceiptBinding(rows, preparation.binding, freshNow); err != nil {
			return fmt.Errorf("%s: %w", providerDeleteStageVerify, err)
		}
		if preparation.prepared.identityDigest != rows.claim.TargetIdentityDigest {
			return fmt.Errorf("%s: %w: deletion target digest changed", providerDeleteStageVerify, provider.ErrDeletePointIdentityConflict)
		}
		if !validPointDeletionResult(execution.Result) {
			return fmt.Errorf("%s: %w: provider deletion result is unproven", providerDeleteStageReceipt, backupasset.ErrInvalidState)
		}
		digest := execution.Result.ReceiptDigest
		tombstone := model.RecoveryPointLifecycleTombstone{
			RecoveryPointID: rows.point.ID, RepositoryID: rows.point.RepositoryID,
			OriginalSemantics: rows.point.Semantics, TerminalOperation: rows.attempt.Operation,
			TerminalState: string(backupasset.RecoveryPointExpired), ManagedHistory: true,
			DeletionReceiptDigest: &digest, PurgedAt: &freshNow,
			ResultCode: string(execution.Result.Outcome), CreatedAt: freshNow,
		}
		if err := tx.WithContext(ctx).Create(&tombstone).Error; err != nil {
			return fmt.Errorf("%s: persist lifecycle tombstone: %w", providerDeleteStageReceipt, err)
		}
		update := tx.WithContext(ctx).Model(&model.RecoveryPointLifecycleEffectClaim{}).
			Where("id = ? AND state = ? AND executor_id = ? AND execution_id = ? AND transition_revision = ? AND lease_id = ? AND lease_attempt_id = ? AND lease_fence_token_hash = ? AND target_identity_digest = ?",
				rows.claim.ID, "in_flight", preparation.binding.ExecutorID, preparation.binding.ExecutionID,
				preparation.binding.TransitionRevision, preparation.binding.LeaseID,
				preparation.binding.LeaseAttemptID, preparation.binding.LeaseFenceTokenHash,
				preparation.binding.TargetIdentityDigest).
			Updates(map[string]any{"state": "proven", "updated_at": freshNow})
		if update.Error != nil {
			return fmt.Errorf("%s: prove lifecycle effect claim: %w", providerDeleteStageReceipt, update.Error)
		}
		if update.RowsAffected != 1 {
			return fmt.Errorf("%s: %w: lifecycle effect claim changed", providerDeleteStageReceipt, backupasset.ErrConflict)
		}
		held, err = coordinator.activeHoldTx(ctx, tx, rows.point)
		if err != nil {
			return err
		}
		if held {
			// A late hold leaves durable provider proof but blocks the
			// lifecycle before any terminal point mutation. Settle the exact
			// historical lease on this path; ordinary proof settlement runs
			// in progressProviderProof after audit success.
			if err := coordinator.settleLifecycleLeaseAfterProofTx(ctx, tx, rows); err != nil {
				return err
			}
		}
		if held {
			var blockErr error
			persisted, blockErr = coordinator.blockAttemptTx(ctx, tx, &rows.attempt, &rows.point, backupasset.LifecycleBlockedActiveHold)
			return blockErr
		}
		rows.claim.State = "proven"
		persisted = lifecycleAttemptFromModel(rows.attempt)
		return nil
	})
	if err != nil {
		return LifecycleAttempt{}, err
	}
	return persisted, nil
}

func (coordinator *Coordinator) settleLifecycleLeaseAfterProofTx(ctx context.Context, tx *gorm.DB, rows providerDeleteRows) error {
	// Proof is durable and independent of the current lifecycle lease. A
	// missing, expired, or rebound lease must never prevent tombstoning, and a
	// rebound lease must never be released by the proof owner.
	if !rows.leaseFound ||
		rows.attempt.LeaseID == nil ||
		rows.attempt.LeaseAttemptID == nil ||
		rows.attempt.LeaseFenceTokenHash == nil {
		return nil
	}
	if rows.lease.ID != *rows.attempt.LeaseID ||
		rows.lease.AttemptID != *rows.attempt.LeaseAttemptID ||
		hashFenceToken(rows.lease.FenceToken) != *rows.attempt.LeaseFenceTokenHash ||
		rows.lease.RecoveryPointID != rows.point.ID {
		return nil
	}
	// Owner/holder are the mutable hand-off boundary. Only the still-current
	// retention lease may be released or expired; any foreign/restarted owner
	// is somebody else's authority and is intentionally left untouched.
	if coordinator == nil || coordinator.leaseOwnerID == "" ||
		rows.lease.HolderType != string(backupasset.LeaseHolderRetentionWorker) ||
		rows.lease.OwnerID != coordinator.leaseOwnerID ||
		rows.lease.LeaseExpiresAt.IsZero() ||
		rows.lease.AbsoluteDeadline.IsZero() {
		return nil
	}
	switch backupasset.LeaseStatus(rows.lease.Status) {
	case backupasset.LeaseReleased, backupasset.LeaseExpired:
		return nil
	case backupasset.LeaseActive:
		now := coordinator.now().UTC()
		if now.Before(rows.lease.LeaseExpiresAt.UTC()) && now.Before(rows.lease.AbsoluteDeadline.UTC()) {
			if coordinator.leases == nil {
				return nil
			}
			return coordinator.leases.ReleaseTx(ctx, tx, rows.leaseFence())
		}
		result := tx.WithContext(ctx).Model(&model.RecoveryPointLease{}).
			Where("id = ? AND recovery_point_id = ? AND holder_type = ? AND owner_id = ? AND attempt_id = ? AND fence_token = ? AND status = ?",
				rows.lease.ID, rows.lease.RecoveryPointID, rows.lease.HolderType, rows.lease.OwnerID,
				rows.lease.AttemptID, rows.lease.FenceToken, backupasset.LeaseActive).
			Updates(map[string]any{"status": backupasset.LeaseExpired, "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return nil
		}
		return nil
	default:
		return fmt.Errorf("%w: lifecycle proof lease state is invalid", backupasset.ErrInvalidState)
	}
}

func (rows providerDeleteRows) leaseFence() backupasset.LeaseFence {
	return backupasset.LeaseFence{LeaseID: rows.lease.ID, RecoveryPointID: rows.lease.RecoveryPointID, HolderType: backupasset.LeaseHolderType(rows.lease.HolderType), OwnerID: rows.lease.OwnerID, AttemptID: rows.lease.AttemptID, FenceToken: rows.lease.FenceToken}
}

// startEffectRenewer owns the only renewal path for a committed execution.
func (coordinator *Coordinator) startEffectRenewer(parent context.Context, binding providerDeleteBinding, cancelExecute context.CancelFunc) func() {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			remaining := binding.DeadlineAt.Sub(coordinator.now().UTC())
			if remaining <= 0 {
				cancelExecute()
				return
			}
			wait := remaining / 2
			if wait < time.Millisecond {
				wait = time.Millisecond
			}
			wake := coordinator.effectClaimAfter(wait)
			select {
			case <-ctx.Done():
				return
			case <-wake:
			}
			if err := coordinator.renewEffectClaim(ctx, &binding); err != nil {
				cancelExecute()
				return
			}
		}
	}()
	return func() { cancel(); <-done }
}

func (coordinator *Coordinator) renewEffectClaim(ctx context.Context, binding *providerDeleteBinding) error {
	if binding == nil {
		return fmt.Errorf("%s: %w: invalid renewal binding", providerDeleteStageClaimRenew, backupasset.ErrInvalidState)
	}
	err := coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		rows, err := lockProviderDeleteRowsByAttemptTx(ctx, tx, binding.AttemptID)
		if err != nil {
			return err
		}
		_, proofFound, err := coordinator.validateProviderDeleteRowsAuthority(
			rows, rows.claimFound && !rows.tombstoneFound, true,
		)
		if err != nil {
			return err
		}
		if proofFound {
			return nil
		}
		if !rows.claimFound || rows.claim.State != "in_flight" ||
			rows.claim.ExecutorID != binding.ExecutorID || rows.claim.ExecutionID != binding.ExecutionID ||
			rows.claim.TargetIdentityDigest != binding.TargetIdentityDigest || rows.claim.TransitionRevision != binding.TransitionRevision ||
			rows.claim.LeaseID != binding.LeaseID || rows.claim.LeaseAttemptID != binding.LeaseAttemptID ||
			rows.claim.LeaseFenceTokenHash != binding.LeaseFenceTokenHash {
			return fmt.Errorf("%w: effect claim binding changed", ErrEffectClaimInFlight)
		}
		now := coordinator.now().UTC()
		if err := coordinator.validateProviderDeleteLeaseAuthority(rows, now, true, true, false); err != nil {
			return err
		}
		if !rows.claim.DeadlineAt.UTC().After(now) {
			return fmt.Errorf("%w: effect claim deadline reached", backupasset.ErrConflict)
		}
		request := coordinator.lifecyclePointRequestFromRows(rows, true)
		if err := validateProviderDeleteExecutionRows(request, rows.attempt, rows.point, rows.lease, now); err != nil {
			return err
		}
		lease, err := coordinator.leases.RenewTx(ctx, tx, rows.leaseFence())
		if err != nil {
			return fmt.Errorf("renew lifecycle lease: %w", err)
		}
		rows.lease = modelLeaseFromPublic(lease)
		deadline := coordinator.providerDeleteClaimDeadline(rows.lease, now)
		if !deadline.After(now) {
			return fmt.Errorf("%w: renewed effect claim deadline elapsed", backupasset.ErrConflict)
		}
		result := tx.WithContext(ctx).Model(&model.RecoveryPointLifecycleEffectClaim{}).
			Where("id = ? AND state = ? AND executor_id = ? AND execution_id = ? AND transition_revision = ? AND lease_id = ? AND lease_attempt_id = ? AND lease_fence_token_hash = ? AND target_identity_digest = ?",
				rows.claim.ID, "in_flight", binding.ExecutorID, binding.ExecutionID, binding.TransitionRevision,
				binding.LeaseID, binding.LeaseAttemptID, binding.LeaseFenceTokenHash, binding.TargetIdentityDigest).
			Updates(map[string]any{"deadline_at": deadline, "heartbeat_at": now, "updated_at": now})
		if result.Error != nil {
			return fmt.Errorf("renew effect claim update: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("%w: renew effect claim CAS changed", backupasset.ErrConflict)
		}
		binding.DeadlineAt = deadline
		binding.AbsoluteDeadlineAt = rows.lease.AbsoluteDeadline.UTC()
		return nil
	})
	if err != nil {
		return fmt.Errorf("%s: %w", providerDeleteStageClaimRenew, err)
	}
	return nil
}

func (coordinator *Coordinator) executeProviderDelete(ctx context.Context, preparation providerDeletePreparation) (pointDeletionExecution, error) {
	if !preparation.acquired {
		return pointDeletionExecution{}, fmt.Errorf("%s: %w: provider effect claim was not acquired", providerDeleteStageClaimAcquire, backupasset.ErrConflict)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	executeCtx, cancel, err := coordinator.providerDeleteBoundContext(
		ctx, preparation.binding.AbsoluteDeadlineAt,
	)
	if err != nil {
		return pointDeletionExecution{}, err
	}
	stopRenewer := coordinator.startEffectRenewer(executeCtx, preparation.binding, cancel)
	execution, executeErr := coordinator.deleter.Execute(executeCtx, preparation.prepared)
	stopRenewer()
	cancel()
	if executeErr != nil {
		return execution, executeErr
	}
	if execution.Stage == "" {
		execution.Stage = providerDeleteStageProvider
	}
	return execution, nil
}

func (coordinator *Coordinator) heartbeatProviderDelete(ctx context.Context, attemptID string) (LifecycleAttempt, error) {
	var observed LifecycleAttempt
	err := coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		rows, err := lockProviderDeleteRowsByAttemptTx(ctx, tx, attemptID)
		if err != nil {
			return err
		}
		_, proofFound, err := validateProviderDeleteRowsProofFirst(rows)
		if err != nil {
			return err
		}
		if proofFound {
			observed = lifecycleAttemptFromModel(rows.attempt)
			return nil
		}
		if identityErr := coordinator.validateProviderDeleteLeaseIdentity(rows, rows.claimFound && !rows.tombstoneFound, true); identityErr != nil {
			if errors.Is(identityErr, errLifecycleFenceLost) &&
				!rows.claimFound && !rows.tombstoneFound &&
				backupasset.LifecyclePhase(rows.attempt.Phase) == backupasset.LifecyclePhaseProviderDelete {
				var blockErr error
				observed, blockErr = coordinator.blockAttemptTx(
					ctx, tx, &rows.attempt, &rows.point,
					backupasset.LifecycleBlockedProviderDeleteUnproven,
				)
				return blockErr
			}
			return identityErr
		}
		if rows.claimFound {
			if err := coordinator.validateProviderDeleteLeaseAuthority(rows, coordinator.now().UTC(), true, false, false); err != nil {
				return err
			}
			observed = lifecycleAttemptFromModel(rows.attempt)
			return nil
		}
		if err := coordinator.ensureLifecycleFenceTx(ctx, tx, &rows.attempt); err != nil {
			if errors.Is(err, errLifecycleFenceLost) || errors.Is(err, errLifecycleAbsoluteDeadline) {
				var blockErr error
				observed, blockErr = coordinator.blockAttemptTx(
					ctx, tx, &rows.attempt, &rows.point,
					lifecycleFenceLostReason(backupasset.LifecyclePhase(rows.attempt.Phase)),
				)
				return blockErr
			}
			return err
		}
		fence, err := coordinator.lifecycleFenceTx(ctx, tx, rows.attempt)
		if err != nil {
			if errors.Is(err, errLifecycleFenceLost) {
				observed, err = coordinator.blockAttemptTx(
					ctx, tx, &rows.attempt, &rows.point,
					lifecycleFenceLostReason(backupasset.LifecyclePhase(rows.attempt.Phase)),
				)
				return err
			}
			return err
		}
		if _, err := coordinator.leases.RenewTx(ctx, tx, fence); err != nil {
			if errors.Is(err, backupasset.ErrLeaseFenceLost) || errors.Is(err, backupasset.ErrLeaseDeadlineExceeded) {
				observed, err = coordinator.blockAttemptTx(
					ctx, tx, &rows.attempt, &rows.point,
					lifecycleFenceLostReason(backupasset.LifecyclePhase(rows.attempt.Phase)),
				)
				return err
			}
			return fmt.Errorf("renew lifecycle fence: %w", err)
		}
		now := coordinator.now().UTC()
		result := tx.WithContext(ctx).Model(&model.RecoveryPointLifecycleAttempt{}).
			Where("id = ? AND transition_revision = ?", rows.attempt.ID, rows.attempt.TransitionRevision).
			Updates(map[string]any{"heartbeat_at": now, "updated_at": now})
		if result.Error != nil {
			return fmt.Errorf("persist lifecycle heartbeat: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("%w: lifecycle attempt changed", backupasset.ErrConflict)
		}
		rows.attempt.HeartbeatAt = &now
		rows.attempt.UpdatedAt = now
		observed = lifecycleAttemptFromModel(rows.attempt)
		return nil
	})
	if err != nil {
		return LifecycleAttempt{}, err
	}
	return observed, nil
}

func (coordinator *Coordinator) tombstoneAndCompleteProviderProof(ctx context.Context, attemptID string) (LifecycleAttempt, error) {
	var completed LifecycleAttempt
	err := coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		rows, err := lockProviderDeleteRowsByAttemptTx(ctx, tx, attemptID)
		if err != nil {
			return err
		}
		_, proofFound, err := coordinator.validateProviderDeleteRowsAuthority(
			rows, rows.claimFound && !rows.tombstoneFound, true,
		)
		if err != nil {
			return err
		}
		if backupasset.LifecyclePhase(rows.attempt.Phase) == backupasset.LifecyclePhaseComplete {
			completed = lifecycleAttemptFromModel(rows.attempt)
			return nil
		}
		if backupasset.LifecyclePhase(rows.attempt.Phase) != backupasset.LifecyclePhaseTombstoning {
			return fmt.Errorf("%w: lifecycle phase changed", backupasset.ErrConflict)
		}
		if !proofFound {
			return fmt.Errorf("%w: lifecycle tombstone is unproven", backupasset.ErrInvalidState)
		}
		held, err := coordinator.activeHoldTx(ctx, tx, rows.point)
		if err != nil {
			return err
		}
		if err := coordinator.settleLifecycleLeaseAfterProofTx(ctx, tx, rows); err != nil {
			return err
		}
		if held {
			completed, err = coordinator.blockAttemptTx(
				ctx, tx, &rows.attempt, &rows.point,
				backupasset.LifecycleBlockedActiveHold,
			)
			return err
		}
		now := coordinator.now().UTC()
		rows.point.State = string(backupasset.RecoveryPointExpired)
		rows.point.PhysicalAvailability = string(backupasset.PhysicalMissing)
		rows.point.EncryptedProviderLocator = ""
		rows.point.EncryptedRollbackLocator = ""
		rows.point.PointRevision++
		rows.point.UpdatedAt = now
		if err := tx.WithContext(ctx).Save(&rows.point).Error; err != nil {
			return fmt.Errorf("terminalize lifecycle recovery point from proof: %w", err)
		}
		result := tx.WithContext(ctx).Model(&model.RecoveryPointLifecycleAttempt{}).
			Where("id = ? AND phase = ? AND transition_revision = ?",
				rows.attempt.ID, backupasset.LifecyclePhaseTombstoning, rows.attempt.TransitionRevision).
			Updates(map[string]any{
				"phase": backupasset.LifecyclePhaseComplete, "completed_at": now,
				"transition_revision": rows.attempt.TransitionRevision + 1,
				"heartbeat_at":        now, "updated_at": now,
			})
		if result.Error != nil {
			return fmt.Errorf("complete lifecycle attempt from proof: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("%w: lifecycle attempt changed", backupasset.ErrConflict)
		}
		rows.attempt.Phase = string(backupasset.LifecyclePhaseComplete)
		rows.attempt.CompletedAt = &now
		rows.attempt.TransitionRevision++
		rows.attempt.HeartbeatAt = &now
		rows.attempt.UpdatedAt = now
		completed = lifecycleAttemptFromModel(rows.attempt)
		return nil
	})
	if err != nil {
		return LifecycleAttempt{}, err
	}
	return completed, nil
}
