package retention

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type CoordinatorDependencies struct {
	DB           *gorm.DB
	Leases       *backupasset.LeaseService
	Holds        *HoldService
	Now          func() time.Time
	NewID        func() (string, error)
	LeaseOwnerID string
	Admissions   LifecycleAdmission
	Cleanup      LifecycleCleanup
	Deleter      PointDeletion
	RetryDelay   time.Duration
	Audit        AssetAuditSink
}

type AssetAuditSink interface {
	Write(context.Context, backupasset.AuditEventInput) error
}

type Coordinator struct {
	db           *gorm.DB
	leases       *backupasset.LeaseService
	now          func() time.Time
	newID        func() (string, error)
	leaseOwnerID string
	admissions   LifecycleAdmission
	cleanup      LifecycleCleanup
	deleter      PointDeletion
	retryDelay   time.Duration
	audit        AssetAuditSink
}

type LifecyclePointRequest struct {
	RecoveryPointID string
	AttemptID       string
	Operation       backupasset.LifecycleOperation
	authority       lifecycleEffectAuthority
}

type lifecycleEffectAuthority struct {
	TransitionRevision int64
	LeaseID            string
	LeaseAttemptID     string
	LeaseFenceHash     string
	Deadline           time.Time
}

type LifecycleAdmission interface {
	RevokeRecoveryPoint(context.Context, LifecyclePointRequest) error
}

type LifecycleCleanup interface {
	CleanupRecoveryPoint(context.Context, LifecyclePointRequest) error
}

type PointDeletionOutcome string

const (
	PointDeletionDeleted       PointDeletionOutcome = "provider_deleted"
	PointDeletionAlreadyAbsent PointDeletionOutcome = "provider_already_absent"
)

type PointDeletionResult struct {
	Outcome       PointDeletionOutcome
	ReceiptDigest string
}

type PointDeletion interface {
	DeleteRecoveryPoint(context.Context, LifecyclePointRequest) (PointDeletionResult, error)
}

var (
	ErrPointDeletionWORM             = errors.New("point deletion blocked by WORM")
	ErrPointDeletionIdentityConflict = errors.New("point deletion identity conflict")
)

// PointDeletionAccessResolver materializes the exact provider delete request
// after RecoveryPoint and repository identity have been loaded. Runtime owns
// credential/locator reconstruction; this package never invents a generic
// command fallback.
type PointDeletionAccessResolver interface {
	ResolveDeletePoint(context.Context, LifecyclePointRequest, model.RecoveryPoint, model.BackupRepository) (provider.DeletePointRequest, error)
}

// RegistryPointDeletion is the production PointDeletion adapter. It resolves
// the RecoveryPoint, provider kind, private locator, and access snapshot, then
// calls the separately registered provider.PointDeleter. Only deleted or
// already_absent plus a receipt digest are accepted.
type RegistryPointDeletion struct {
	db       *gorm.DB
	registry *provider.Registry
	resolve  PointDeletionAccessResolver
}

func NewRegistryPointDeletion(db *gorm.DB, registry *provider.Registry, resolve PointDeletionAccessResolver) (*RegistryPointDeletion, error) {
	if db == nil || registry == nil || resolve == nil {
		return nil, fmt.Errorf("%w: registry point deletion adapter unavailable", backupasset.ErrInvalidState)
	}
	return &RegistryPointDeletion{db: db, registry: registry, resolve: resolve}, nil
}

func (adapter *RegistryPointDeletion) DeleteRecoveryPoint(ctx context.Context, request LifecyclePointRequest) (PointDeletionResult, error) {
	if adapter == nil || adapter.db == nil || adapter.registry == nil || adapter.resolve == nil {
		return PointDeletionResult{}, fmt.Errorf("%w: registry point deletion adapter unavailable", backupasset.ErrInvalidState)
	}
	if backupasset.ValidateOpaqueID(request.RecoveryPointID) != nil {
		return PointDeletionResult{}, fmt.Errorf("%w: invalid lifecycle deletion request", backupasset.ErrInvalidState)
	}
	var point model.RecoveryPoint
	if err := adapter.db.WithContext(ctx).First(&point, "id = ?", request.RecoveryPointID).Error; err != nil {
		return PointDeletionResult{}, fmt.Errorf("load recovery point for deletion: %w", err)
	}
	var repository model.BackupRepository
	if err := adapter.db.WithContext(ctx).First(&repository, "id = ?", point.RepositoryID).Error; err != nil {
		return PointDeletionResult{}, fmt.Errorf("load repository for deletion: %w", err)
	}
	kind := backupasset.ProviderKind(repository.ProviderKind)
	deleteRequest, err := adapter.resolve.ResolveDeletePoint(ctx, request, point, repository)
	if err != nil {
		return PointDeletionResult{}, mapProviderDeletionError(err)
	}
	deleter, err := adapter.registry.PointDeleter(kind)
	if err != nil {
		return PointDeletionResult{}, err
	}
	result, err := deleter.DeletePoint(ctx, deleteRequest)
	if err != nil {
		return PointDeletionResult{}, mapProviderDeletionError(err)
	}
	switch result.Outcome {
	case provider.DeletePointDeleted:
		if !isLowerHexString(result.ReceiptDigest, 64) {
			return PointDeletionResult{}, fmt.Errorf("%w: provider deletion receipt is unproven", backupasset.ErrInvalidState)
		}
		return PointDeletionResult{Outcome: PointDeletionDeleted, ReceiptDigest: result.ReceiptDigest}, nil
	case provider.DeletePointAlreadyAbsent:
		if !isLowerHexString(result.ReceiptDigest, 64) {
			return PointDeletionResult{}, fmt.Errorf("%w: provider deletion receipt is unproven", backupasset.ErrInvalidState)
		}
		return PointDeletionResult{Outcome: PointDeletionAlreadyAbsent, ReceiptDigest: result.ReceiptDigest}, nil
	case provider.DeletePointBlockedWORM:
		return PointDeletionResult{}, ErrPointDeletionWORM
	default:
		return PointDeletionResult{}, fmt.Errorf("%w: provider deletion is unproven", backupasset.ErrInvalidState)
	}
}

func mapProviderDeletionError(err error) error {
	switch {
	case errors.Is(err, provider.ErrDeletePointWORM):
		return ErrPointDeletionWORM
	case errors.Is(err, provider.ErrDeletePointIdentityConflict):
		return ErrPointDeletionIdentityConflict
	default:
		return err
	}
}

var _ PointDeletion = (*RegistryPointDeletion)(nil)

type ClaimRequest struct {
	RecoveryPointID string
	Operation       backupasset.LifecycleOperation
	PolicySelection *Selection
	MutablePoint    *MutableRetirementSnapshot
	PurgePlan       *PurgePlanSnapshot
}

type MutableRetirementSnapshot struct {
	PointRevision      int64
	CapabilityRevision int
}

type PurgePlanSnapshot struct {
	PlanID             string
	Revision           int64
	ActorID            uint
	PointRevision      int64
	CapabilityRevision int
}

type LifecycleAttempt struct {
	ID                  string                             `json:"id"`
	RecoveryPointID     string                             `json:"recovery_point_id"`
	Operation           backupasset.LifecycleOperation     `json:"operation"`
	Phase               backupasset.LifecyclePhase         `json:"phase"`
	TransitionRevision  int64                              `json:"transition_revision"`
	BlockedReason       backupasset.LifecycleBlockedReason `json:"blocked_reason,omitempty"`
	ClaimedAt           *time.Time                         `json:"claimed_at,omitempty"`
	HeartbeatAt         *time.Time                         `json:"heartbeat_at,omitempty"`
	RetryAt             *time.Time                         `json:"retry_at,omitempty"`
	CompletedAt         *time.Time                         `json:"completed_at,omitempty"`
	CreatedAt           time.Time                          `json:"created_at"`
	UpdatedAt           time.Time                          `json:"updated_at"`
	LeaseID             string                             `json:"-"`
	LeaseAttemptID      string                             `json:"-"`
	LeaseFenceTokenHash string                             `json:"-"`
}

func (attempt LifecycleAttempt) String() string {
	return fmt.Sprintf("{Operation:%q Phase:%q TransitionRevision:%d BlockedReason:%q}",
		attempt.Operation, attempt.Phase, attempt.TransitionRevision, attempt.BlockedReason)
}

func (attempt LifecycleAttempt) GoString() string { return attempt.String() }

func NewCoordinator(dependencies CoordinatorDependencies) (*Coordinator, error) {
	if dependencies.DB == nil || dependencies.Leases == nil || dependencies.Holds == nil {
		return nil, fmt.Errorf("%w: lifecycle coordinator persistence is unavailable", backupasset.ErrInvalidState)
	}
	if dependencies.Now == nil {
		dependencies.Now = func() time.Time { return time.Now().UTC() }
	}
	if dependencies.NewID == nil {
		dependencies.NewID = backupasset.NewOpaqueID
	}
	if dependencies.RetryDelay == 0 {
		dependencies.RetryDelay = time.Minute
	}
	if dependencies.RetryDelay < time.Second || dependencies.RetryDelay > 24*time.Hour {
		return nil, fmt.Errorf("%w: lifecycle retry delay is invalid", backupasset.ErrInvalidState)
	}
	ownerID := strings.TrimSpace(dependencies.LeaseOwnerID)
	if ownerID == "" || ownerID != dependencies.LeaseOwnerID || len(ownerID) > 64 || strings.ContainsAny(ownerID, "\r\n\x00") {
		return nil, fmt.Errorf("%w: lifecycle coordinator lease owner is invalid", backupasset.ErrInvalidState)
	}
	coordinator := &Coordinator{
		db: dependencies.DB, leases: dependencies.Leases, now: dependencies.Now,
		newID: dependencies.NewID, leaseOwnerID: ownerID,
		admissions: dependencies.Admissions, cleanup: dependencies.Cleanup,
		deleter: dependencies.Deleter, retryDelay: dependencies.RetryDelay,
		audit: dependencies.Audit,
	}
	dependencies.Leases.SetLifecycleLeaseAdmission(coordinator)
	dependencies.Holds.SetLifecycleHoldAdmission(coordinator)
	return coordinator, nil
}

func (coordinator *Coordinator) ListIncompleteAttempts(ctx context.Context, limit int) ([]LifecycleAttempt, error) {
	return coordinator.ListIncompleteAttemptsAfter(ctx, limit, "")
}

func (coordinator *Coordinator) ListIncompleteAttemptsAfter(ctx context.Context, limit int, afterID string) ([]LifecycleAttempt, error) {
	if coordinator == nil || coordinator.db == nil {
		return nil, fmt.Errorf("%w: lifecycle coordinator is unavailable", backupasset.ErrInvalidState)
	}
	if limit < 1 || limit > 1000 {
		return nil, fmt.Errorf("%w: invalid lifecycle attempt batch", backupasset.ErrInvalidState)
	}
	if afterID != "" && backupasset.ValidateOpaqueID(afterID) != nil {
		return nil, fmt.Errorf("%w: invalid lifecycle attempt list cursor", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if !coordinator.db.Migrator().HasTable(&model.RecoveryPointLifecycleAttempt{}) {
		return nil, nil
	}
	query := coordinator.db.WithContext(ctx).Where("phase <> ?", backupasset.LifecyclePhaseComplete)
	if afterID != "" {
		query = query.Where("id > ?", afterID)
	}
	var rows []model.RecoveryPointLifecycleAttempt
	if err := query.Order("id ASC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list incomplete lifecycle attempts: %w", err)
	}
	result := make([]LifecycleAttempt, 0, len(rows))
	for _, row := range rows {
		result = append(result, lifecycleAttemptFromModel(row))
	}
	return result, nil
}

func (coordinator *Coordinator) Heartbeat(ctx context.Context, attemptID string) (LifecycleAttempt, error) {
	if coordinator == nil || coordinator.db == nil || backupasset.ValidateOpaqueID(attemptID) != nil {
		return LifecycleAttempt{}, fmt.Errorf("%w: invalid lifecycle attempt", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var heartbeated LifecycleAttempt
	err := coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		attempt, point, err := coordinator.lockAttemptAndPointTx(ctx, tx, attemptID)
		if err != nil {
			return err
		}
		if backupasset.LifecyclePhase(attempt.Phase) == backupasset.LifecyclePhaseComplete {
			heartbeated = lifecycleAttemptFromModel(attempt)
			return nil
		}
		if err := coordinator.ensureLifecycleFenceTx(ctx, tx, &attempt); err != nil {
			if errors.Is(err, errLifecycleFenceLost) || errors.Is(err, errLifecycleAbsoluteDeadline) {
				heartbeated, err = coordinator.blockAttemptTx(ctx, tx, &attempt, &point, backupasset.LifecycleBlockedFenceLost)
				return err
			}
			return err
		}
		fence, err := coordinator.lifecycleFenceTx(ctx, tx, attempt)
		if err != nil {
			if errors.Is(err, errLifecycleFenceLost) {
				heartbeated, err = coordinator.blockAttemptTx(ctx, tx, &attempt, &point, backupasset.LifecycleBlockedFenceLost)
				return err
			}
			return err
		}
		if _, err := coordinator.leases.RenewTx(ctx, tx, fence); err != nil {
			if errors.Is(err, backupasset.ErrLeaseFenceLost) || errors.Is(err, backupasset.ErrLeaseDeadlineExceeded) {
				heartbeated, err = coordinator.blockAttemptTx(ctx, tx, &attempt, &point, backupasset.LifecycleBlockedFenceLost)
				return err
			}
			return fmt.Errorf("renew lifecycle fence: %w", err)
		}
		now := coordinator.now().UTC()
		result := tx.WithContext(ctx).Model(&model.RecoveryPointLifecycleAttempt{}).
			Where("id = ? AND transition_revision = ?", attempt.ID, attempt.TransitionRevision).
			Updates(map[string]any{"heartbeat_at": now, "updated_at": now})
		if result.Error != nil {
			return fmt.Errorf("persist lifecycle heartbeat: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("%w: lifecycle attempt changed", backupasset.ErrConflict)
		}
		attempt.HeartbeatAt = &now
		attempt.UpdatedAt = now
		heartbeated = lifecycleAttemptFromModel(attempt)
		return nil
	})
	if err != nil {
		return LifecycleAttempt{}, err
	}
	return heartbeated, nil
}

var (
	errLifecycleLeaseLive        = errors.New("lifecycle lease is still live")
	errLifecycleFenceLost        = errors.New("lifecycle fence lost")
	errLifecycleAbsoluteDeadline = errors.New("lifecycle lease absolute deadline reached")
)

const maxLifecycleLeaseDrainBatch = 100

func (coordinator *Coordinator) Advance(ctx context.Context, attemptID string) (LifecycleAttempt, error) {
	if backupasset.ValidateOpaqueID(attemptID) != nil {
		return LifecycleAttempt{}, fmt.Errorf("%w: invalid lifecycle attempt", backupasset.ErrInvalidState)
	}
	attempt, err := coordinator.loadAttempt(ctx, attemptID)
	if err != nil {
		return LifecycleAttempt{}, err
	}
	switch attempt.Phase {
	case backupasset.LifecyclePhaseSelected:
		return coordinator.transition(ctx, attemptID, backupasset.LifecyclePhaseSelected, backupasset.LifecyclePhaseRevoking)
	case backupasset.LifecyclePhaseRevoking:
		if coordinator.admissions == nil {
			return coordinator.block(ctx, attemptID, backupasset.LifecycleBlockedOwnerCleanupUnproven)
		}
		current, authority, blocked, err := coordinator.prepareExternalEffect(ctx, attempt.ID, backupasset.LifecyclePhaseRevoking)
		if err != nil || blocked {
			return current, err
		}
		effectCtx, cancel, err := coordinator.effectContext(ctx, authority)
		if err != nil {
			return coordinator.blockUncertainEffect(ctx, attempt.ID, backupasset.LifecyclePhaseRevoking, authority)
		}
		err = coordinator.admissions.RevokeRecoveryPoint(effectCtx, LifecyclePointRequest{
			RecoveryPointID: current.RecoveryPointID, AttemptID: current.ID,
			Operation: current.Operation, authority: authority,
		})
		effectErr := effectCtx.Err()
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return LifecycleAttempt{}, ctx.Err()
			}
			if errors.Is(effectErr, context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
				return coordinator.blockUncertainEffect(ctx, attempt.ID, backupasset.LifecyclePhaseRevoking, authority)
			}
			return coordinator.blockAuthorized(ctx, attempt.ID, backupasset.LifecyclePhaseRevoking, authority, backupasset.LifecycleBlockedOwnerCleanupUnproven)
		}
		if errors.Is(effectErr, context.DeadlineExceeded) {
			return coordinator.blockUncertainEffect(ctx, attempt.ID, backupasset.LifecyclePhaseRevoking, authority)
		}
		return coordinator.transitionAuthorized(ctx, attemptID, backupasset.LifecyclePhaseRevoking, backupasset.LifecyclePhaseDraining, authority)
	case backupasset.LifecyclePhaseDraining:
		return coordinator.drainAndTransition(ctx, attemptID)
	case backupasset.LifecyclePhaseCleaning:
		return coordinator.cleanAndTransition(ctx, attempt.ID)
	case backupasset.LifecyclePhaseProviderDelete:
		return coordinator.deleteAndTransition(ctx, attempt)
	case backupasset.LifecyclePhaseTombstoning:
		return coordinator.tombstoneAndComplete(ctx, attemptID)
	case backupasset.LifecyclePhaseBlocked:
		return coordinator.retryBlocked(ctx, attemptID)
	case backupasset.LifecyclePhaseComplete:
		return attempt, nil
	default:
		return LifecycleAttempt{}, fmt.Errorf("%w: lifecycle phase is not implemented by this coordinator slice", backupasset.ErrInvalidState)
	}
}

func (coordinator *Coordinator) loadAttempt(ctx context.Context, attemptID string) (LifecycleAttempt, error) {
	var row model.RecoveryPointLifecycleAttempt
	result := coordinator.db.WithContext(ctx).Where("id = ?", attemptID).Limit(1).Find(&row)
	if result.Error != nil {
		return LifecycleAttempt{}, fmt.Errorf("load lifecycle attempt: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return LifecycleAttempt{}, fmt.Errorf("%w: lifecycle attempt", backupasset.ErrNotFound)
	}
	return lifecycleAttemptFromModel(row), nil
}

func (coordinator *Coordinator) transition(
	ctx context.Context,
	attemptID string,
	from backupasset.LifecyclePhase,
	to backupasset.LifecyclePhase,
) (LifecycleAttempt, error) {
	var transitioned LifecycleAttempt
	err := coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		attempt, point, err := coordinator.lockAttemptAndPointTx(ctx, tx, attemptID)
		if err != nil {
			return err
		}
		if backupasset.LifecyclePhase(attempt.Phase) != from {
			return fmt.Errorf("%w: lifecycle phase changed", backupasset.ErrConflict)
		}
		if err := coordinator.ensureLifecycleFenceTx(ctx, tx, &attempt); err != nil {
			if errors.Is(err, errLifecycleFenceLost) || errors.Is(err, errLifecycleAbsoluteDeadline) {
				var blockErr error
				transitioned, blockErr = coordinator.blockAttemptTx(ctx, tx, &attempt, &point, backupasset.LifecycleBlockedFenceLost)
				return blockErr
			}
			return err
		}
		blocked, err := coordinator.activeHoldTx(ctx, tx, point)
		if err != nil {
			return err
		}
		if blocked {
			transitioned, err = coordinator.blockAttemptTx(ctx, tx, &attempt, &point, backupasset.LifecycleBlockedActiveHold)
			return err
		}
		transitioned, err = coordinator.transitionAttemptTx(ctx, tx, &attempt, from, to)
		return err
	})
	if err != nil {
		return LifecycleAttempt{}, err
	}
	return transitioned, nil
}

func (coordinator *Coordinator) prepareExternalEffect(
	ctx context.Context,
	attemptID string,
	phase backupasset.LifecyclePhase,
) (LifecycleAttempt, lifecycleEffectAuthority, bool, error) {
	var current LifecycleAttempt
	var authority lifecycleEffectAuthority
	blocked := false
	err := coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		attempt, point, err := coordinator.lockAttemptAndPointTx(ctx, tx, attemptID)
		if err != nil {
			return err
		}
		if backupasset.LifecyclePhase(attempt.Phase) != phase {
			return fmt.Errorf("%w: lifecycle phase changed", backupasset.ErrConflict)
		}
		if err := coordinator.ensureLifecycleFenceTx(ctx, tx, &attempt); err != nil {
			current, err = coordinator.blockAttemptTx(ctx, tx, &attempt, &point, backupasset.LifecycleBlockedFenceLost)
			blocked = err == nil
			return err
		}
		held, err := coordinator.activeHoldTx(ctx, tx, point)
		if err != nil {
			return err
		}
		if held {
			current, err = coordinator.blockAttemptTx(ctx, tx, &attempt, &point, backupasset.LifecycleBlockedActiveHold)
			blocked = err == nil
			return err
		}
		authority, err = coordinator.effectAuthorityTx(ctx, tx, attempt)
		if err != nil {
			return err
		}
		current = lifecycleAttemptFromModel(attempt)
		return nil
	})
	if err != nil {
		return LifecycleAttempt{}, lifecycleEffectAuthority{}, false, err
	}
	return current, authority, blocked, nil
}

func (coordinator *Coordinator) effectAuthorityTx(
	ctx context.Context,
	tx *gorm.DB,
	attempt model.RecoveryPointLifecycleAttempt,
) (lifecycleEffectAuthority, error) {
	if attempt.LeaseID == nil || attempt.LeaseAttemptID == nil || attempt.LeaseFenceTokenHash == nil {
		return lifecycleEffectAuthority{}, errLifecycleFenceLost
	}
	var lease model.RecoveryPointLease
	loaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", *attempt.LeaseID).Limit(1).Find(&lease)
	if loaded.Error != nil {
		return lifecycleEffectAuthority{}, fmt.Errorf("load lifecycle effect lease: %w", loaded.Error)
	}
	if loaded.RowsAffected != 1 || lease.RecoveryPointID != attempt.RecoveryPointID ||
		lease.AttemptID != *attempt.LeaseAttemptID || hashFenceToken(lease.FenceToken) != *attempt.LeaseFenceTokenHash ||
		backupasset.LeaseStatus(lease.Status) != backupasset.LeaseActive {
		return lifecycleEffectAuthority{}, errLifecycleFenceLost
	}
	deadline := lease.LeaseExpiresAt.UTC()
	if lease.AbsoluteDeadline.UTC().Before(deadline) {
		deadline = lease.AbsoluteDeadline.UTC()
	}
	return lifecycleEffectAuthority{
		TransitionRevision: attempt.TransitionRevision,
		LeaseID:            *attempt.LeaseID,
		LeaseAttemptID:     *attempt.LeaseAttemptID,
		LeaseFenceHash:     *attempt.LeaseFenceTokenHash,
		Deadline:           deadline,
	}, nil
}

func (coordinator *Coordinator) effectContext(
	ctx context.Context,
	authority lifecycleEffectAuthority,
) (context.Context, context.CancelFunc, error) {
	remaining := authority.Deadline.Sub(coordinator.now().UTC())
	if remaining <= 0 {
		return nil, nil, errLifecycleFenceLost
	}
	effectCtx, cancel := context.WithTimeout(ctx, remaining)
	return effectCtx, cancel, nil
}

func (coordinator *Coordinator) validateEffectAuthorityTx(
	ctx context.Context,
	tx *gorm.DB,
	attempt *model.RecoveryPointLifecycleAttempt,
	point *model.RecoveryPoint,
	authority lifecycleEffectAuthority,
) (bool, error) {
	if attempt.TransitionRevision != authority.TransitionRevision || attempt.LeaseID == nil ||
		attempt.LeaseAttemptID == nil || attempt.LeaseFenceTokenHash == nil ||
		*attempt.LeaseID != authority.LeaseID || *attempt.LeaseAttemptID != authority.LeaseAttemptID ||
		*attempt.LeaseFenceTokenHash != authority.LeaseFenceHash {
		return false, fmt.Errorf("%w: lifecycle effect authority changed", backupasset.ErrConflict)
	}
	if err := coordinator.ensureLifecycleFenceTx(ctx, tx, attempt); err != nil {
		return false, err
	}
	if attempt.TransitionRevision != authority.TransitionRevision || attempt.LeaseAttemptID == nil ||
		attempt.LeaseFenceTokenHash == nil || *attempt.LeaseAttemptID != authority.LeaseAttemptID ||
		*attempt.LeaseFenceTokenHash != authority.LeaseFenceHash {
		return false, fmt.Errorf("%w: lifecycle effect authority changed", backupasset.ErrConflict)
	}
	return coordinator.activeHoldTx(ctx, tx, *point)
}

func (coordinator *Coordinator) transitionAuthorized(
	ctx context.Context,
	attemptID string,
	from backupasset.LifecyclePhase,
	to backupasset.LifecyclePhase,
	authority lifecycleEffectAuthority,
) (LifecycleAttempt, error) {
	var transitioned LifecycleAttempt
	err := coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		attempt, point, err := coordinator.lockAttemptAndPointTx(ctx, tx, attemptID)
		if err != nil {
			return err
		}
		if backupasset.LifecyclePhase(attempt.Phase) != from {
			return fmt.Errorf("%w: lifecycle phase changed", backupasset.ErrConflict)
		}
		held, err := coordinator.validateEffectAuthorityTx(ctx, tx, &attempt, &point, authority)
		if err != nil {
			return err
		}
		if held {
			transitioned, err = coordinator.blockAttemptTx(ctx, tx, &attempt, &point, backupasset.LifecycleBlockedActiveHold)
			return err
		}
		transitioned, err = coordinator.transitionAttemptTx(ctx, tx, &attempt, from, to)
		return err
	})
	if err != nil {
		return LifecycleAttempt{}, err
	}
	return transitioned, nil
}

func (coordinator *Coordinator) blockAuthorized(
	ctx context.Context,
	attemptID string,
	phase backupasset.LifecyclePhase,
	authority lifecycleEffectAuthority,
	reason backupasset.LifecycleBlockedReason,
) (LifecycleAttempt, error) {
	var blocked LifecycleAttempt
	err := coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		attempt, point, err := coordinator.lockAttemptAndPointTx(ctx, tx, attemptID)
		if err != nil {
			return err
		}
		if backupasset.LifecyclePhase(attempt.Phase) != phase {
			return fmt.Errorf("%w: lifecycle phase changed", backupasset.ErrConflict)
		}
		held, err := coordinator.validateEffectAuthorityTx(ctx, tx, &attempt, &point, authority)
		if err != nil {
			return err
		}
		if held {
			reason = backupasset.LifecycleBlockedActiveHold
		}
		blocked, err = coordinator.blockAttemptTx(ctx, tx, &attempt, &point, reason)
		return err
	})
	if err != nil {
		return LifecycleAttempt{}, err
	}
	if phase == backupasset.LifecyclePhaseProviderDelete {
		if auditErr := coordinator.writeSettledDeletionAudit(ctx, blocked, settledDeletionStatus(reason)); auditErr != nil {
			if _, scheduleErr := coordinator.scheduleBlockedAuditRetry(ctx, blocked.ID); scheduleErr != nil {
				return blocked, errors.Join(auditErr, scheduleErr)
			}
			return blocked, auditErr
		}
	}
	return blocked, nil
}

func (coordinator *Coordinator) blockUncertainEffect(
	ctx context.Context,
	attemptID string,
	phase backupasset.LifecyclePhase,
	authority lifecycleEffectAuthority,
) (LifecycleAttempt, error) {
	var blocked LifecycleAttempt
	err := coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		attempt, point, err := coordinator.lockAttemptAndPointTx(ctx, tx, attemptID)
		if err != nil {
			return err
		}
		if backupasset.LifecyclePhase(attempt.Phase) != phase ||
			attempt.TransitionRevision != authority.TransitionRevision || attempt.LeaseID == nil ||
			attempt.LeaseAttemptID == nil || attempt.LeaseFenceTokenHash == nil ||
			*attempt.LeaseID != authority.LeaseID || *attempt.LeaseAttemptID != authority.LeaseAttemptID ||
			*attempt.LeaseFenceTokenHash != authority.LeaseFenceHash {
			return fmt.Errorf("%w: uncertain lifecycle effect authority changed", backupasset.ErrConflict)
		}
		var lease model.RecoveryPointLease
		loaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", authority.LeaseID).Limit(1).Find(&lease)
		if loaded.Error != nil {
			return fmt.Errorf("lock uncertain lifecycle effect lease: %w", loaded.Error)
		}
		deadline := lease.LeaseExpiresAt.UTC()
		if lease.AbsoluteDeadline.UTC().Before(deadline) {
			deadline = lease.AbsoluteDeadline.UTC()
		}
		if loaded.RowsAffected != 1 || lease.RecoveryPointID != attempt.RecoveryPointID ||
			lease.HolderType != string(backupasset.LeaseHolderRetentionWorker) || lease.OwnerID != coordinator.leaseOwnerID ||
			lease.AttemptID != authority.LeaseAttemptID || hashFenceToken(lease.FenceToken) != authority.LeaseFenceHash ||
			!deadline.Equal(authority.Deadline) {
			return fmt.Errorf("%w: uncertain lifecycle effect lease changed", backupasset.ErrConflict)
		}
		blocked, err = coordinator.blockAttemptTx(
			ctx, tx, &attempt, &point, backupasset.LifecycleBlockedFenceLost,
		)
		return err
	})
	if err != nil {
		return LifecycleAttempt{}, err
	}
	return blocked, nil
}

func (coordinator *Coordinator) drainAndTransition(ctx context.Context, attemptID string) (LifecycleAttempt, error) {
	var transitioned LifecycleAttempt
	err := coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		attempt, point, err := coordinator.lockAttemptAndPointTx(ctx, tx, attemptID)
		if err != nil {
			return err
		}
		if backupasset.LifecyclePhase(attempt.Phase) != backupasset.LifecyclePhaseDraining {
			return fmt.Errorf("%w: lifecycle phase changed", backupasset.ErrConflict)
		}
		if err := coordinator.ensureLifecycleFenceTx(ctx, tx, &attempt); err != nil {
			reason := backupasset.LifecycleBlockedLeaseDrainUnproven
			if errors.Is(err, errLifecycleFenceLost) || errors.Is(err, errLifecycleAbsoluteDeadline) {
				reason = backupasset.LifecycleBlockedFenceLost
			}
			transitioned, err = coordinator.blockAttemptTx(ctx, tx, &attempt, &point, reason)
			return err
		}
		blocked, err := coordinator.activeHoldTx(ctx, tx, point)
		if err != nil {
			return err
		}
		if blocked {
			transitioned, err = coordinator.blockAttemptTx(ctx, tx, &attempt, &point, backupasset.LifecycleBlockedActiveHold)
			return err
		}
		if err := coordinator.drainOrdinaryLeasesTx(ctx, tx, attempt); err != nil {
			reason := backupasset.LifecycleBlockedLeaseDrainUnproven
			if errors.Is(err, errLifecycleLeaseLive) {
				reason = backupasset.LifecycleBlockedLeaseLive
			}
			transitioned, err = coordinator.blockAttemptTx(ctx, tx, &attempt, &point, reason)
			return err
		}
		transitioned, err = coordinator.transitionAttemptTx(
			ctx, tx, &attempt, backupasset.LifecyclePhaseDraining, backupasset.LifecyclePhaseCleaning,
		)
		return err
	})
	if err != nil {
		return LifecycleAttempt{}, err
	}
	return transitioned, nil
}

func (coordinator *Coordinator) cleanAndTransition(ctx context.Context, attemptID string) (LifecycleAttempt, error) {
	if coordinator.cleanup == nil {
		return coordinator.block(ctx, attemptID, backupasset.LifecycleBlockedOwnerCleanupUnproven)
	}
	attempt, authority, blocked, err := coordinator.prepareExternalEffect(ctx, attemptID, backupasset.LifecyclePhaseCleaning)
	if err != nil || blocked {
		return attempt, err
	}
	effectCtx, cancel, err := coordinator.effectContext(ctx, authority)
	if err != nil {
		return coordinator.blockUncertainEffect(ctx, attemptID, backupasset.LifecyclePhaseCleaning, authority)
	}
	err = coordinator.cleanup.CleanupRecoveryPoint(effectCtx, LifecyclePointRequest{
		RecoveryPointID: attempt.RecoveryPointID, AttemptID: attempt.ID,
		Operation: attempt.Operation, authority: authority,
	})
	effectErr := effectCtx.Err()
	cancel()
	if err != nil {
		if ctx.Err() != nil {
			return LifecycleAttempt{}, ctx.Err()
		}
		if errors.Is(effectErr, context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
			return coordinator.blockUncertainEffect(ctx, attempt.ID, backupasset.LifecyclePhaseCleaning, authority)
		}
		return coordinator.blockAuthorized(ctx, attempt.ID, backupasset.LifecyclePhaseCleaning, authority, backupasset.LifecycleBlockedOwnerCleanupUnproven)
	}
	if errors.Is(effectErr, context.DeadlineExceeded) {
		return coordinator.blockUncertainEffect(ctx, attempt.ID, backupasset.LifecyclePhaseCleaning, authority)
	}
	next := backupasset.LifecyclePhaseProviderDelete
	if attempt.Operation == backupasset.LifecycleMutableRetire {
		return coordinator.persistMutableTombstoneAndTransition(ctx, attempt.ID, authority)
	}
	return coordinator.transitionAuthorized(ctx, attempt.ID, backupasset.LifecyclePhaseCleaning, next, authority)
}

func (coordinator *Coordinator) persistMutableTombstoneAndTransition(
	ctx context.Context,
	attemptID string,
	authority lifecycleEffectAuthority,
) (LifecycleAttempt, error) {
	var transitioned LifecycleAttempt
	err := coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		attempt, point, err := coordinator.lockAttemptAndPointTx(ctx, tx, attemptID)
		if err != nil {
			return err
		}
		if backupasset.LifecyclePhase(attempt.Phase) != backupasset.LifecyclePhaseCleaning ||
			backupasset.LifecycleOperation(attempt.Operation) != backupasset.LifecycleMutableRetire ||
			backupasset.PointVersionSemantics(point.Semantics) != backupasset.PointMutableHead ||
			backupasset.RecoveryPointState(point.State) != backupasset.RecoveryPointObserved {
			return fmt.Errorf("%w: mutable retirement phase changed", backupasset.ErrConflict)
		}
		blocked, err := coordinator.validateEffectAuthorityTx(ctx, tx, &attempt, &point, authority)
		if err != nil {
			return err
		}
		if blocked {
			transitioned, err = coordinator.blockAttemptTx(ctx, tx, &attempt, &point, backupasset.LifecycleBlockedActiveHold)
			return err
		}
		now := coordinator.now().UTC()
		tombstone := model.RecoveryPointLifecycleTombstone{
			RecoveryPointID: point.ID, RepositoryID: point.RepositoryID,
			OriginalSemantics: point.Semantics, TerminalOperation: attempt.Operation,
			TerminalState: string(backupasset.RecoveryPointRetired), ManagedHistory: true,
			RetiredAt: &now, ResultCode: "mutable_retired", CreatedAt: now,
		}
		if err := tx.WithContext(ctx).Create(&tombstone).Error; err != nil {
			return fmt.Errorf("persist mutable retirement tombstone: %w", err)
		}
		transitioned, err = coordinator.transitionAttemptTx(
			ctx, tx, &attempt, backupasset.LifecyclePhaseCleaning, backupasset.LifecyclePhaseTombstoning,
		)
		return err
	})
	if err != nil {
		return LifecycleAttempt{}, err
	}
	return transitioned, nil
}

func (coordinator *Coordinator) deleteAndTransition(ctx context.Context, attempt LifecycleAttempt) (LifecycleAttempt, error) {
	if coordinator.deleter == nil {
		return coordinator.block(ctx, attempt.ID, backupasset.LifecycleBlockedProviderDeleteUnproven)
	}
	receipt, found, err := coordinator.lookupProviderDeleteReceipt(ctx, attempt)
	if err != nil {
		return LifecycleAttempt{}, err
	}
	if found {
		return coordinator.confirmSettledProviderDelete(ctx, attempt, receipt)
	}
	current, authority, blocked, err := coordinator.prepareExternalEffect(ctx, attempt.ID, backupasset.LifecyclePhaseProviderDelete)
	if err != nil || blocked {
		return current, err
	}
	effectCtx, cancel, err := coordinator.effectContext(ctx, authority)
	if err != nil {
		return coordinator.blockUncertainEffect(ctx, attempt.ID, backupasset.LifecyclePhaseProviderDelete, authority)
	}
	result, err := coordinator.deleter.DeleteRecoveryPoint(effectCtx, LifecyclePointRequest{
		RecoveryPointID: current.RecoveryPointID, AttemptID: current.ID,
		Operation: current.Operation, authority: authority,
	})
	effectErr := effectCtx.Err()
	cancel()
	if err != nil {
		if ctx.Err() != nil {
			return LifecycleAttempt{}, ctx.Err()
		}
		if errors.Is(effectErr, context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
			return coordinator.blockUncertainEffect(ctx, attempt.ID, backupasset.LifecyclePhaseProviderDelete, authority)
		}
		return coordinator.blockAuthorized(ctx, attempt.ID, backupasset.LifecyclePhaseProviderDelete, authority, providerDeletionBlockedReason(err))
	}
	if errors.Is(effectErr, context.DeadlineExceeded) {
		return coordinator.blockUncertainEffect(ctx, attempt.ID, backupasset.LifecyclePhaseProviderDelete, authority)
	}
	if !validPointDeletionResult(result) {
		return coordinator.blockAuthorized(ctx, attempt.ID, backupasset.LifecyclePhaseProviderDelete, authority, backupasset.LifecycleBlockedProviderDeleteUnproven)
	}
	persisted, err := coordinator.persistProviderDeleteReceipt(ctx, attempt.ID, authority, result)
	if err != nil {
		return LifecycleAttempt{}, err
	}
	if persisted.Phase != backupasset.LifecyclePhaseProviderDelete {
		return persisted, nil
	}
	return coordinator.confirmSettledProviderDelete(ctx, persisted, result)
}

func (coordinator *Coordinator) lookupProviderDeleteReceipt(
	ctx context.Context,
	attempt LifecycleAttempt,
) (PointDeletionResult, bool, error) {
	var point model.RecoveryPoint
	loaded := coordinator.db.WithContext(ctx).Where("id = ?", attempt.RecoveryPointID).Limit(1).Find(&point)
	if loaded.Error != nil {
		return PointDeletionResult{}, false, fmt.Errorf("load provider delete receipt point: %w", loaded.Error)
	}
	if loaded.RowsAffected != 1 {
		return PointDeletionResult{}, false, fmt.Errorf("%w: lifecycle recovery point", backupasset.ErrNotFound)
	}
	var event model.RecoveryPointLifecycleTombstone
	found := coordinator.db.WithContext(ctx).
		Where("recovery_point_id = ? AND terminal_operation = ?", attempt.RecoveryPointID, attempt.Operation).
		Limit(1).Find(&event)
	if found.Error != nil {
		return PointDeletionResult{}, false, fmt.Errorf("load provider delete receipt: %w", found.Error)
	}
	if found.RowsAffected == 0 {
		return PointDeletionResult{}, false, nil
	}
	row := model.RecoveryPointLifecycleAttempt{
		RecoveryPointID: attempt.RecoveryPointID,
		Operation:       string(attempt.Operation),
	}
	if err := validateLifecycleTerminalEvent(point, row, event); err != nil {
		return PointDeletionResult{}, true, err
	}
	if event.DeletionReceiptDigest == nil {
		return PointDeletionResult{}, true, fmt.Errorf("%w: lifecycle tombstone is unproven", backupasset.ErrInvalidState)
	}
	return PointDeletionResult{
		Outcome: PointDeletionOutcome(event.ResultCode), ReceiptDigest: *event.DeletionReceiptDigest,
	}, true, nil
}

func (coordinator *Coordinator) persistProviderDeleteReceipt(
	ctx context.Context,
	attemptID string,
	authority lifecycleEffectAuthority,
	result PointDeletionResult,
) (LifecycleAttempt, error) {
	var persisted LifecycleAttempt
	err := coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		attemptRow, point, lockErr := coordinator.lockAttemptAndPointTx(ctx, tx, attemptID)
		if lockErr != nil {
			return lockErr
		}
		if backupasset.LifecyclePhase(attemptRow.Phase) != backupasset.LifecyclePhaseProviderDelete {
			return fmt.Errorf("%w: lifecycle phase changed", backupasset.ErrConflict)
		}
		held, holdErr := coordinator.validateEffectAuthorityTx(ctx, tx, &attemptRow, &point, authority)
		if holdErr != nil {
			return holdErr
		}
		if held {
			var blockErr error
			persisted, blockErr = coordinator.blockAttemptTx(ctx, tx, &attemptRow, &point, backupasset.LifecycleBlockedActiveHold)
			return blockErr
		}
		now := coordinator.now().UTC()
		existing, found, existingErr := coordinator.lockLifecycleTerminalEventTx(ctx, tx, point, attemptRow)
		if existingErr != nil {
			return existingErr
		}
		if !found {
			tombstone := model.RecoveryPointLifecycleTombstone{
				RecoveryPointID: point.ID, RepositoryID: point.RepositoryID,
				OriginalSemantics: point.Semantics, TerminalOperation: attemptRow.Operation,
				TerminalState: string(backupasset.RecoveryPointExpired), ManagedHistory: true,
				DeletionReceiptDigest: &result.ReceiptDigest, PurgedAt: &now,
				ResultCode: string(result.Outcome), CreatedAt: now,
			}
			if createErr := tx.WithContext(ctx).Create(&tombstone).Error; createErr != nil {
				return fmt.Errorf("persist lifecycle deletion tombstone: %w", createErr)
			}
		} else if existing.ResultCode != string(result.Outcome) ||
			existing.DeletionReceiptDigest == nil || *existing.DeletionReceiptDigest != result.ReceiptDigest {
			return fmt.Errorf("%w: lifecycle tombstone is unproven", backupasset.ErrInvalidState)
		}
		retryAt := now.Add(coordinator.retryDelay)
		update := tx.WithContext(ctx).Model(&model.RecoveryPointLifecycleAttempt{}).
			Where("id = ? AND phase = ? AND transition_revision = ?", attemptRow.ID, backupasset.LifecyclePhaseProviderDelete, attemptRow.TransitionRevision).
			Updates(map[string]any{
				"retry_at": retryAt, "heartbeat_at": now, "updated_at": now,
			})
		if update.Error != nil {
			return fmt.Errorf("schedule provider delete audit retry: %w", update.Error)
		}
		if update.RowsAffected != 1 {
			return fmt.Errorf("%w: lifecycle attempt changed", backupasset.ErrConflict)
		}
		attemptRow.RetryAt = &retryAt
		attemptRow.HeartbeatAt = &now
		attemptRow.UpdatedAt = now
		persisted = lifecycleAttemptFromModel(attemptRow)
		return nil
	})
	if err != nil {
		return LifecycleAttempt{}, err
	}
	return persisted, nil
}

func (coordinator *Coordinator) confirmSettledProviderDelete(
	ctx context.Context,
	attempt LifecycleAttempt,
	result PointDeletionResult,
) (LifecycleAttempt, error) {
	status := "deleted"
	if result.Outcome == PointDeletionAlreadyAbsent {
		status = "already_absent"
	}
	if auditErr := coordinator.writeSettledDeletionAudit(ctx, attempt, status); auditErr != nil {
		scheduled, scheduleErr := coordinator.scheduleProviderDeleteAuditRetry(ctx, attempt.ID)
		if scheduleErr != nil {
			return attempt, errors.Join(auditErr, scheduleErr)
		}
		return scheduled, auditErr
	}
	return coordinator.transition(
		ctx, attempt.ID, backupasset.LifecyclePhaseProviderDelete, backupasset.LifecyclePhaseTombstoning,
	)
}

func (coordinator *Coordinator) scheduleProviderDeleteAuditRetry(
	ctx context.Context,
	attemptID string,
) (LifecycleAttempt, error) {
	now := coordinator.now().UTC()
	retryAt := now.Add(coordinator.retryDelay)
	var scheduled LifecycleAttempt
	err := coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		attempt, _, lockErr := coordinator.lockAttemptAndPointTx(ctx, tx, attemptID)
		if lockErr != nil {
			return lockErr
		}
		if backupasset.LifecyclePhase(attempt.Phase) != backupasset.LifecyclePhaseProviderDelete {
			scheduled = lifecycleAttemptFromModel(attempt)
			return nil
		}
		update := tx.WithContext(ctx).Model(&model.RecoveryPointLifecycleAttempt{}).
			Where("id = ? AND phase = ?", attempt.ID, backupasset.LifecyclePhaseProviderDelete).
			Updates(map[string]any{
				"retry_at": retryAt, "heartbeat_at": now, "updated_at": now,
			})
		if update.Error != nil {
			return fmt.Errorf("schedule provider delete audit retry: %w", update.Error)
		}
		if update.RowsAffected != 1 {
			return fmt.Errorf("%w: lifecycle attempt changed", backupasset.ErrConflict)
		}
		attempt.RetryAt = &retryAt
		attempt.HeartbeatAt = &now
		attempt.UpdatedAt = now
		scheduled = lifecycleAttemptFromModel(attempt)
		return nil
	})
	if err != nil {
		return LifecycleAttempt{}, err
	}
	return scheduled, nil
}

func (coordinator *Coordinator) scheduleBlockedAuditRetry(
	ctx context.Context,
	attemptID string,
) (LifecycleAttempt, error) {
	now := coordinator.now().UTC()
	retryAt := now.Add(coordinator.retryDelay)
	var scheduled LifecycleAttempt
	err := coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		attempt, _, lockErr := coordinator.lockAttemptAndPointTx(ctx, tx, attemptID)
		if lockErr != nil {
			return lockErr
		}
		if backupasset.LifecyclePhase(attempt.Phase) != backupasset.LifecyclePhaseBlocked {
			scheduled = lifecycleAttemptFromModel(attempt)
			return nil
		}
		update := tx.WithContext(ctx).Model(&model.RecoveryPointLifecycleAttempt{}).
			Where("id = ? AND phase = ?", attempt.ID, backupasset.LifecyclePhaseBlocked).
			Updates(map[string]any{
				"retry_at": retryAt, "heartbeat_at": now, "updated_at": now,
			})
		if update.Error != nil {
			return fmt.Errorf("schedule blocked audit retry: %w", update.Error)
		}
		if update.RowsAffected != 1 {
			return fmt.Errorf("%w: lifecycle attempt changed", backupasset.ErrConflict)
		}
		attempt.RetryAt = &retryAt
		attempt.HeartbeatAt = &now
		attempt.UpdatedAt = now
		scheduled = lifecycleAttemptFromModel(attempt)
		return nil
	})
	if err != nil {
		return LifecycleAttempt{}, err
	}
	return scheduled, nil
}

func providerDeletionBlocked(reason backupasset.LifecycleBlockedReason) bool {
	switch reason {
	case backupasset.LifecycleBlockedProviderWORM, backupasset.LifecycleBlockedProviderUnavailable,
		backupasset.LifecycleBlockedProviderIdentityConflict, backupasset.LifecycleBlockedProviderDeleteUnproven,
		backupasset.LifecycleBlockedDeletionUnavailable:
		return true
	default:
		return false
	}
}

func (coordinator *Coordinator) tombstoneAndComplete(ctx context.Context, attemptID string) (LifecycleAttempt, error) {
	var completed LifecycleAttempt
	err := coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		attempt, point, err := coordinator.lockAttemptAndPointTx(ctx, tx, attemptID)
		if err != nil {
			return err
		}
		if backupasset.LifecyclePhase(attempt.Phase) != backupasset.LifecyclePhaseTombstoning {
			return fmt.Errorf("%w: lifecycle phase changed", backupasset.ErrConflict)
		}
		if err := coordinator.ensureLifecycleFenceTx(ctx, tx, &attempt); err != nil {
			completed, err = coordinator.blockAttemptTx(ctx, tx, &attempt, &point, backupasset.LifecycleBlockedFenceLost)
			return err
		}
		held, err := coordinator.activeHoldTx(ctx, tx, point)
		if err != nil {
			return err
		}
		if held {
			completed, err = coordinator.blockAttemptTx(ctx, tx, &attempt, &point, backupasset.LifecycleBlockedActiveHold)
			return err
		}
		_, found, err := coordinator.lockLifecycleTerminalEventTx(ctx, tx, point, attempt)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("%w: lifecycle tombstone is unproven", backupasset.ErrInvalidState)
		}
		now := coordinator.now().UTC()
		if backupasset.LifecycleOperation(attempt.Operation) == backupasset.LifecycleMutableRetire {
			point.State = string(backupasset.RecoveryPointRetired)
			retirementReason := string(backupasset.RetirementWithdrawn)
			point.RetirementReason = &retirementReason
			point.RetiredAt = &now
			point.EncryptedRollbackLocator = point.EncryptedProviderLocator
			point.EncryptedProviderLocator = ""
		} else {
			point.State = string(backupasset.RecoveryPointExpired)
			point.PhysicalAvailability = string(backupasset.PhysicalMissing)
			point.EncryptedProviderLocator = ""
			point.EncryptedRollbackLocator = ""
		}
		point.PointRevision++
		point.UpdatedAt = now
		if err := tx.WithContext(ctx).Save(&point).Error; err != nil {
			return fmt.Errorf("terminalize lifecycle recovery point: %w", err)
		}
		leaseFence, err := coordinator.lifecycleFenceTx(ctx, tx, attempt)
		if err != nil {
			return err
		}
		if err := coordinator.leases.ReleaseTx(ctx, tx, leaseFence); err != nil {
			return fmt.Errorf("release lifecycle fence: %w", err)
		}
		result := tx.WithContext(ctx).Model(&model.RecoveryPointLifecycleAttempt{}).
			Where("id = ? AND phase = ? AND transition_revision = ?", attempt.ID, backupasset.LifecyclePhaseTombstoning, attempt.TransitionRevision).
			Updates(map[string]any{
				"phase": backupasset.LifecyclePhaseComplete, "completed_at": now,
				"transition_revision": attempt.TransitionRevision + 1, "heartbeat_at": now, "updated_at": now,
			})
		if result.Error != nil {
			return fmt.Errorf("complete lifecycle attempt: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("%w: lifecycle attempt changed", backupasset.ErrConflict)
		}
		attempt.Phase = string(backupasset.LifecyclePhaseComplete)
		attempt.CompletedAt = &now
		attempt.TransitionRevision++
		attempt.HeartbeatAt = &now
		attempt.UpdatedAt = now
		completed = lifecycleAttemptFromModel(attempt)
		return nil
	})
	if err != nil {
		return LifecycleAttempt{}, err
	}
	return completed, nil
}

func (coordinator *Coordinator) writeSettledDeletionAudit(ctx context.Context, attempt LifecycleAttempt, status string) error {
	if coordinator == nil || coordinator.audit == nil {
		return nil
	}
	if backupasset.ValidateOpaqueID(attempt.RecoveryPointID) != nil {
		return fmt.Errorf("%w: settled deletion audit identifiers", backupasset.ErrInvalidState)
	}
	var point model.RecoveryPoint
	if err := coordinator.db.WithContext(ctx).Select("id", "repository_id").
		Where("id = ?", attempt.RecoveryPointID).Limit(1).Find(&point).Error; err != nil {
		return fmt.Errorf("load settled deletion audit point: %w", err)
	}
	if backupasset.ValidateOpaqueID(point.RepositoryID) != nil {
		return fmt.Errorf("%w: settled deletion audit identifiers", backupasset.ErrInvalidState)
	}
	outcome := backupasset.AuditOutcomeSuccess
	if status == "blocked" || status == "identity_conflict" {
		outcome = backupasset.AuditOutcomeBlocked
	}
	return coordinator.audit.Write(ctx, backupasset.AuditEventInput{
		Action:          backupasset.AuditActionRepositoryPurge,
		Outcome:         outcome,
		RepositoryID:    point.RepositoryID,
		RecoveryPointID: attempt.RecoveryPointID,
		ItemCount:       1,
		Fields: map[backupasset.AuditField]any{
			backupasset.AuditFieldStage:     "settled",
			backupasset.AuditFieldStatus:    status,
			backupasset.AuditFieldItemCount: int64(1),
			backupasset.AuditFieldSource:    attempt.ID,
		},
	})
}

type settledDeletionLookup interface {
	HasSettledDeletion(recoveryPointID, attemptID, status string) bool
}

func (coordinator *Coordinator) flushSettledBlockedAudit(ctx context.Context, attempt LifecycleAttempt) error {
	reason := backupasset.LifecycleBlockedReason(attempt.BlockedReason)
	status := settledDeletionStatus(reason)
	now := coordinator.now().UTC()
	if attempt.RetryAt != nil && now.Before(attempt.RetryAt.UTC()) {
		return nil
	}
	if coordinator.hasSettledDeletionAudit(ctx, attempt, status) {
		return nil
	}
	if !providerDeletionBlocked(reason) && reason != backupasset.LifecycleBlockedActiveHold {
		return nil
	}
	return coordinator.writeSettledDeletionAudit(ctx, attempt, status)
}

func (coordinator *Coordinator) hasSettledDeletionAudit(ctx context.Context, attempt LifecycleAttempt, status string) bool {
	if lookup, ok := coordinator.audit.(settledDeletionLookup); ok {
		return lookup.HasSettledDeletion(attempt.RecoveryPointID, attempt.ID, status)
	}
	if coordinator.db == nil || !coordinator.db.Migrator().HasTable(&model.BackupAssetAuditEvent{}) {
		return false
	}
	var events []model.BackupAssetAuditEvent
	if err := coordinator.db.WithContext(ctx).
		Where("recovery_point_id = ? AND action = ?", attempt.RecoveryPointID, backupasset.AuditActionRepositoryPurge).
		Order("id DESC").Limit(20).Find(&events).Error; err != nil {
		return false
	}
	for _, event := range events {
		var fields map[string]any
		if json.Unmarshal([]byte(event.FieldsJSON), &fields) != nil {
			continue
		}
		if fields[string(backupasset.AuditFieldStage)] != "settled" || fields[string(backupasset.AuditFieldStatus)] != status {
			continue
		}
		if source, _ := fields[string(backupasset.AuditFieldSource)].(string); source != "" && source != attempt.ID {
			continue
		}
		return true
	}
	return false
}

func settledDeletionStatus(reason backupasset.LifecycleBlockedReason) string {
	if reason == backupasset.LifecycleBlockedProviderIdentityConflict {
		return "identity_conflict"
	}
	return "blocked"
}

func providerDeletionBlockedReason(err error) backupasset.LifecycleBlockedReason {
	var capabilityErr *provider.CapabilityError
	if errors.As(err, &capabilityErr) && capabilityErr.Reason.Code == backupasset.CapabilityDeletionUnavailable {
		return backupasset.LifecycleBlockedDeletionUnavailable
	}
	switch {
	case errors.Is(err, ErrPointDeletionWORM):
		return backupasset.LifecycleBlockedProviderWORM
	case errors.Is(err, backupasset.ErrProviderUnavailable):
		return backupasset.LifecycleBlockedProviderUnavailable
	case errors.Is(err, ErrPointDeletionIdentityConflict):
		return backupasset.LifecycleBlockedProviderIdentityConflict
	default:
		return backupasset.LifecycleBlockedProviderDeleteUnproven
	}
}

func validPointDeletionResult(result PointDeletionResult) bool {
	return (result.Outcome == PointDeletionDeleted || result.Outcome == PointDeletionAlreadyAbsent) &&
		isLowerHexString(result.ReceiptDigest, 64)
}

func isLowerHexString(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			if character < 'a' || character > 'f' {
				return false
			}
		}
	}
	return true
}

func (coordinator *Coordinator) block(
	ctx context.Context,
	attemptID string,
	reason backupasset.LifecycleBlockedReason,
) (LifecycleAttempt, error) {
	var blocked LifecycleAttempt
	err := coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		attempt, point, err := coordinator.lockAttemptAndPointTx(ctx, tx, attemptID)
		if err != nil {
			return err
		}
		blocked, err = coordinator.blockAttemptTx(ctx, tx, &attempt, &point, reason)
		return err
	})
	if err != nil {
		return LifecycleAttempt{}, err
	}
	return blocked, nil
}

func (coordinator *Coordinator) retryBlocked(ctx context.Context, attemptID string) (LifecycleAttempt, error) {
	var current model.RecoveryPointLifecycleAttempt
	if err := coordinator.db.WithContext(ctx).First(&current, "id = ?", attemptID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return LifecycleAttempt{}, fmt.Errorf("%w: lifecycle attempt", backupasset.ErrNotFound)
		}
		return LifecycleAttempt{}, fmt.Errorf("load blocked lifecycle attempt: %w", err)
	}
	loaded := lifecycleAttemptFromModel(current)
	if backupasset.LifecyclePhase(current.Phase) != backupasset.LifecyclePhaseBlocked {
		return loaded, fmt.Errorf("%w: lifecycle phase changed", backupasset.ErrConflict)
	}
	if providerDeletionBlocked(backupasset.LifecycleBlockedReason(current.BlockedReason)) ||
		backupasset.LifecycleBlockedReason(current.BlockedReason) == backupasset.LifecycleBlockedActiveHold {
		if auditErr := coordinator.flushSettledBlockedAudit(ctx, loaded); auditErr != nil {
			scheduled, scheduleErr := coordinator.scheduleBlockedAuditRetry(ctx, attemptID)
			if scheduleErr != nil {
				return scheduled, errors.Join(auditErr, scheduleErr)
			}
			return scheduled, auditErr
		}
	}
	var retried LifecycleAttempt
	err := coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		attempt, point, err := coordinator.lockAttemptAndPointTx(ctx, tx, attemptID)
		if err != nil {
			return err
		}
		if backupasset.LifecyclePhase(attempt.Phase) != backupasset.LifecyclePhaseBlocked {
			return fmt.Errorf("%w: lifecycle phase changed", backupasset.ErrConflict)
		}
		now := coordinator.now().UTC()
		if attempt.RetryAt != nil && now.Before(attempt.RetryAt.UTC()) {
			retried = lifecycleAttemptFromModel(attempt)
			return nil
		}
		if err := coordinator.ensureLifecycleFenceTx(ctx, tx, &attempt); err != nil {
			if errors.Is(err, errLifecycleAbsoluteDeadline) {
				if err := coordinator.adoptExpiredLifecycleFenceTx(ctx, tx, &attempt); err != nil {
					retried, err = coordinator.blockAttemptTx(ctx, tx, &attempt, &point, backupasset.LifecycleBlockedFenceLost)
					return err
				}
			} else if errors.Is(err, errLifecycleFenceLost) {
				retried, err = coordinator.blockAttemptTx(ctx, tx, &attempt, &point, backupasset.LifecycleBlockedFenceLost)
				return err
			} else {
				return err
			}
		}
		blocked, err := coordinator.activeHoldTx(ctx, tx, point)
		if err != nil {
			return err
		}
		if blocked {
			retried, err = coordinator.blockAttemptTx(ctx, tx, &attempt, &point, backupasset.LifecycleBlockedActiveHold)
			return err
		}
		resume := blockedResumePhase(backupasset.LifecycleBlockedReason(attempt.BlockedReason))
		if backupasset.LifecycleBlockedReason(attempt.BlockedReason) == backupasset.LifecycleBlockedFenceLost {
			_, terminalEventExists, err := coordinator.lockLifecycleTerminalEventTx(ctx, tx, point, attempt)
			if err != nil {
				return err
			}
			if terminalEventExists {
				resume = backupasset.LifecyclePhaseTombstoning
			}
		}
		if backupasset.LifecycleOperation(attempt.Operation) != backupasset.LifecycleMutableRetire &&
			backupasset.RecoveryPointState(point.State) == backupasset.RecoveryPointPurgeBlocked {
			result := tx.WithContext(ctx).Model(&model.RecoveryPoint{}).
				Where("id = ? AND point_revision = ? AND state = ?", point.ID, point.PointRevision, backupasset.RecoveryPointPurgeBlocked).
				Updates(map[string]any{
					"state": backupasset.RecoveryPointExpiring, "point_revision": point.PointRevision + 1, "updated_at": now,
				})
			if result.Error != nil {
				return fmt.Errorf("retry blocked lifecycle point: %w", result.Error)
			}
			if result.RowsAffected != 1 {
				return fmt.Errorf("%w: blocked lifecycle point changed", backupasset.ErrConflict)
			}
			point.State = string(backupasset.RecoveryPointExpiring)
			point.PointRevision++
		}
		result := tx.WithContext(ctx).Model(&model.RecoveryPointLifecycleAttempt{}).
			Where("id = ? AND phase = ? AND transition_revision = ?", attempt.ID, backupasset.LifecyclePhaseBlocked, attempt.TransitionRevision).
			Updates(map[string]any{
				"phase": resume, "blocked_reason": "", "retry_at": nil,
				"transition_revision": attempt.TransitionRevision + 1, "heartbeat_at": now, "updated_at": now,
			})
		if result.Error != nil {
			return fmt.Errorf("retry blocked lifecycle attempt: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("%w: blocked lifecycle attempt changed", backupasset.ErrConflict)
		}
		attempt.Phase = string(resume)
		attempt.BlockedReason = ""
		attempt.RetryAt = nil
		attempt.TransitionRevision++
		attempt.HeartbeatAt = &now
		attempt.UpdatedAt = now
		retried = lifecycleAttemptFromModel(attempt)
		return nil
	})
	if err != nil {
		return LifecycleAttempt{}, err
	}
	return retried, nil
}

func blockedResumePhase(reason backupasset.LifecycleBlockedReason) backupasset.LifecyclePhase {
	switch reason {
	case backupasset.LifecycleBlockedActiveHold:
		// A hold can appear before any destructive phase. Re-running the
		// idempotent revocation chain is the only safe resume without guessing
		// where the hold was observed.
		return backupasset.LifecyclePhaseRevoking
	case backupasset.LifecycleBlockedLeaseLive, backupasset.LifecycleBlockedLeaseDrainUnproven:
		return backupasset.LifecyclePhaseDraining
	case backupasset.LifecycleBlockedOwnerCleanupUnproven:
		// Revoke and cleanup share this closed reason because 000070 cannot
		// grow a new CHECK value. Re-running the revoke chain is the only
		// safe resume: cleanup-only failures are idempotent after revoke+drain.
		return backupasset.LifecyclePhaseRevoking
	case backupasset.LifecycleBlockedProviderWORM, backupasset.LifecycleBlockedProviderUnavailable,
		backupasset.LifecycleBlockedProviderIdentityConflict, backupasset.LifecycleBlockedProviderDeleteUnproven,
		backupasset.LifecycleBlockedDeletionUnavailable:
		return backupasset.LifecyclePhaseProviderDelete
	default:
		return backupasset.LifecyclePhaseRevoking
	}
}

func (coordinator *Coordinator) lockLifecycleTerminalEventTx(
	ctx context.Context,
	tx *gorm.DB,
	point model.RecoveryPoint,
	attempt model.RecoveryPointLifecycleAttempt,
) (model.RecoveryPointLifecycleTombstone, bool, error) {
	var event model.RecoveryPointLifecycleTombstone
	loaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("recovery_point_id = ? AND terminal_operation = ?", point.ID, attempt.Operation).
		Limit(1).Find(&event)
	if loaded.Error != nil {
		return model.RecoveryPointLifecycleTombstone{}, false, fmt.Errorf("load lifecycle tombstone: %w", loaded.Error)
	}
	if loaded.RowsAffected == 0 {
		return model.RecoveryPointLifecycleTombstone{}, false, nil
	}
	if err := validateLifecycleTerminalEvent(point, attempt, event); err != nil {
		return model.RecoveryPointLifecycleTombstone{}, true, err
	}
	return event, true, nil
}

func validateLifecycleTerminalEvent(
	point model.RecoveryPoint,
	attempt model.RecoveryPointLifecycleAttempt,
	event model.RecoveryPointLifecycleTombstone,
) error {
	invalid := func() error {
		return fmt.Errorf("%w: lifecycle tombstone is unproven", backupasset.ErrInvalidState)
	}
	if event.RecoveryPointID != point.ID || event.RepositoryID != point.RepositoryID ||
		event.OriginalSemantics != point.Semantics || event.TerminalOperation != attempt.Operation ||
		!event.ManagedHistory || event.CreatedAt.IsZero() {
		return invalid()
	}
	operation := backupasset.LifecycleOperation(attempt.Operation)
	switch operation {
	case backupasset.LifecycleMutableRetire:
		if backupasset.RecoveryPointState(point.State) != backupasset.RecoveryPointObserved ||
			event.TerminalState != string(backupasset.RecoveryPointRetired) || event.ResultCode != "mutable_retired" ||
			event.RetiredAt == nil || !event.RetiredAt.Equal(event.CreatedAt) || event.PurgedAt != nil ||
			event.DeletionReceiptDigest != nil {
			return invalid()
		}
	case backupasset.LifecycleRetentionExpire, backupasset.LifecycleExplicitPurge:
		pointState := backupasset.RecoveryPointState(point.State)
		if pointState != backupasset.RecoveryPointExpiring && pointState != backupasset.RecoveryPointPurgeBlocked ||
			event.TerminalState != string(backupasset.RecoveryPointExpired) || event.RetiredAt != nil ||
			event.PurgedAt == nil || !event.PurgedAt.Equal(event.CreatedAt) || event.DeletionReceiptDigest == nil ||
			!validPointDeletionResult(PointDeletionResult{
				Outcome: PointDeletionOutcome(event.ResultCode), ReceiptDigest: *event.DeletionReceiptDigest,
			}) {
			return invalid()
		}
	default:
		return invalid()
	}
	return nil
}

func (coordinator *Coordinator) ValidateLeaseAdmissionTx(
	ctx context.Context,
	tx *gorm.DB,
	request backupasset.AcquireLeaseRequest,
) error {
	if tx == nil || backupasset.ValidateOpaqueID(request.RecoveryPointID) != nil {
		return fmt.Errorf("%w: lifecycle lease admission is invalid", backupasset.ErrInvalidState)
	}
	var point model.RecoveryPoint
	loaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id", "state").Where("id = ?", request.RecoveryPointID).Limit(1).Find(&point)
	if loaded.Error != nil {
		return fmt.Errorf("load lifecycle lease admission point: %w", loaded.Error)
	}
	if loaded.RowsAffected != 1 {
		return fmt.Errorf("%w: lifecycle lease admission point", backupasset.ErrNotFound)
	}
	var activeAttempts int64
	if err := tx.WithContext(ctx).Model(&model.RecoveryPointLifecycleAttempt{}).
		Where("recovery_point_id = ? AND phase <> ?", request.RecoveryPointID, backupasset.LifecyclePhaseComplete).
		Count(&activeAttempts).Error; err != nil {
		return fmt.Errorf("load lifecycle lease admission fence: %w", err)
	}
	state := backupasset.RecoveryPointState(point.State)
	if activeAttempts != 0 || state == backupasset.RecoveryPointExpiring || state == backupasset.RecoveryPointExpired ||
		state == backupasset.RecoveryPointPurgeBlocked || state == backupasset.RecoveryPointRetired {
		return fmt.Errorf("%w: recovery point lifecycle admission is fenced", backupasset.ErrLeaseHeld)
	}
	return nil
}

func (coordinator *Coordinator) ValidateHoldAdmissionTx(
	ctx context.Context,
	tx *gorm.DB,
	recoveryPointID string,
) error {
	if tx == nil || backupasset.ValidateOpaqueID(recoveryPointID) != nil {
		return fmt.Errorf("%w: lifecycle hold admission is invalid", backupasset.ErrInvalidState)
	}
	var point model.RecoveryPoint
	loaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id", "state").Where("id = ?", recoveryPointID).Limit(1).Find(&point)
	if loaded.Error != nil {
		return fmt.Errorf("load lifecycle hold admission point: %w", loaded.Error)
	}
	if loaded.RowsAffected != 1 {
		return fmt.Errorf("%w: lifecycle hold admission point", backupasset.ErrNotFound)
	}
	var activeAttempts int64
	if err := tx.WithContext(ctx).Model(&model.RecoveryPointLifecycleAttempt{}).
		Where("recovery_point_id = ? AND phase <> ?", recoveryPointID, backupasset.LifecyclePhaseComplete).
		Count(&activeAttempts).Error; err != nil {
		return fmt.Errorf("load lifecycle hold admission fence: %w", err)
	}
	state := backupasset.RecoveryPointState(point.State)
	if activeAttempts != 0 || state == backupasset.RecoveryPointExpiring || state == backupasset.RecoveryPointExpired ||
		state == backupasset.RecoveryPointPurgeBlocked || state == backupasset.RecoveryPointRetired {
		return fmt.Errorf("%w: recovery point lifecycle admission is fenced", backupasset.ErrConflict)
	}
	return nil
}

func (coordinator *Coordinator) Claim(ctx context.Context, request ClaimRequest) (LifecycleAttempt, error) {
	var claimed LifecycleAttempt
	err := coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		claimed, err = coordinator.ClaimTx(ctx, tx, request)
		return err
	})
	if err != nil {
		return LifecycleAttempt{}, err
	}
	return claimed, nil
}

func (coordinator *Coordinator) ClaimTx(ctx context.Context, tx *gorm.DB, request ClaimRequest) (LifecycleAttempt, error) {
	if err := validateClaimRequest(request); err != nil {
		return LifecycleAttempt{}, err
	}
	if tx == nil {
		return LifecycleAttempt{}, fmt.Errorf("%w: lifecycle claim transaction is unavailable", backupasset.ErrInvalidState)
	}
	var claimed LifecycleAttempt
	var err error
	err = func() error {
		var point model.RecoveryPoint
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&point, "id = ?", request.RecoveryPointID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: recovery point", backupasset.ErrNotFound)
			}
			return fmt.Errorf("load lifecycle recovery point: %w", err)
		}
		var existing model.RecoveryPointLifecycleAttempt
		loaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("recovery_point_id = ? AND phase <> ?", point.ID, backupasset.LifecyclePhaseComplete).
			Limit(1).Find(&existing)
		if loaded.Error != nil {
			return fmt.Errorf("load active lifecycle attempt: %w", loaded.Error)
		}
		if loaded.RowsAffected == 1 {
			if !sameLifecycleClaim(existing, request) {
				return fmt.Errorf("%w: contradictory active lifecycle claim", backupasset.ErrConflict)
			}
			claimed = lifecycleAttemptFromModel(existing)
			return nil
		}
		var selected SelectedPoint
		switch request.Operation {
		case backupasset.LifecycleRetentionExpire:
			selected, err = validateRetentionClaimPoint(request, point)
		case backupasset.LifecycleMutableRetire:
			err = validateMutableRetirementClaimPoint(request, point)
		case backupasset.LifecycleExplicitPurge:
			err = coordinator.validateExplicitPurgeClaimPointTx(ctx, tx, request, point)
		default:
			err = fmt.Errorf("%w: unsupported lifecycle claim", backupasset.ErrInvalidState)
		}
		if err != nil {
			return err
		}
		var activeHolds int64
		if err := tx.WithContext(ctx).Model(&model.RecoveryPointHold{}).
			Where("recovery_point_id = ? AND state = ?", point.ID, backupasset.HoldActive).
			Count(&activeHolds).Error; err != nil {
			return fmt.Errorf("load lifecycle recovery point holds: %w", err)
		}
		held := point.HoldState == string(backupasset.HoldActive) || activeHolds != 0
		if held && request.Operation != backupasset.LifecycleExplicitPurge {
			return fmt.Errorf("%w: recovery point has an active hold", backupasset.ErrConflict)
		}

		attemptID, err := coordinator.newID()
		if err != nil || backupasset.ValidateOpaqueID(attemptID) != nil {
			return fmt.Errorf("%w: generate lifecycle attempt ID", backupasset.ErrInvalidState)
		}
		now := coordinator.now().UTC()
		lease, err := coordinator.leases.AcquireTx(ctx, tx, backupasset.AcquireLeaseRequest{
			RecoveryPointID: point.ID,
			HolderType:      backupasset.LeaseHolderRetentionWorker,
			OwnerID:         coordinator.leaseOwnerID,
		})
		if err != nil {
			return fmt.Errorf("claim lifecycle lease: %w", err)
		}
		fenceHash := hashFenceToken(lease.Fence.FenceToken)
		attempt := model.RecoveryPointLifecycleAttempt{
			ID: attemptID, RecoveryPointID: point.ID,
			Operation: string(request.Operation), Phase: string(backupasset.LifecyclePhaseSelected), TransitionRevision: 1,
			LeaseID: &lease.ID, LeaseAttemptID: &lease.Fence.AttemptID, LeaseFenceTokenHash: &fenceHash,
			ClaimedAt: &now, HeartbeatAt: &now, CreatedAt: now, UpdatedAt: now,
		}
		if request.PolicySelection != nil {
			policyID := request.PolicySelection.PolicyID
			policyRevision := request.PolicySelection.PolicyRevision
			policyDigest := request.PolicySelection.RuleDigest
			evaluatedAt := request.PolicySelection.EvaluatedAt.UTC()
			attempt.PolicyID = &policyID
			attempt.PolicyRevision = &policyRevision
			attempt.PolicyRuleDigest = &policyDigest
			attempt.EvaluationTime = &evaluatedAt
		}
		if request.PurgePlan != nil {
			attempt.PurgePlanID = &request.PurgePlan.PlanID
			attempt.PurgePlanRevision = &request.PurgePlan.Revision
			attempt.PurgeActorID = &request.PurgePlan.ActorID
		}
		if err := tx.WithContext(ctx).Create(&attempt).Error; err != nil {
			return fmt.Errorf("create lifecycle attempt: %w", err)
		}
		if request.Operation == backupasset.LifecycleRetentionExpire || request.Operation == backupasset.LifecycleExplicitPurge {
			pointRevision := selected.PointRevision
			capabilityRevision := selected.CapabilityRevision
			states := []string{string(backupasset.RecoveryPointCommitted), string(backupasset.RecoveryPointDegraded)}
			if request.Operation == backupasset.LifecycleExplicitPurge {
				pointRevision = request.PurgePlan.PointRevision
				capabilityRevision = request.PurgePlan.CapabilityRevision
				states = []string{
					string(backupasset.RecoveryPointCommitted), string(backupasset.RecoveryPointDegraded),
					string(backupasset.RecoveryPointObserved), string(backupasset.RecoveryPointRetired),
				}
			}
			nextState := backupasset.RecoveryPointExpiring
			if held && request.Operation == backupasset.LifecycleExplicitPurge {
				nextState = backupasset.RecoveryPointPurgeBlocked
			}
			result := tx.WithContext(ctx).Model(&model.RecoveryPoint{}).
				Where("id = ? AND point_revision = ? AND capability_revision = ? AND state IN ?",
					point.ID, pointRevision, capabilityRevision, states).
				Updates(map[string]any{
					"state":          nextState,
					"point_revision": pointRevision + 1,
					"updated_at":     now,
				})
			if result.Error != nil {
				return fmt.Errorf("claim lifecycle recovery point: %w", result.Error)
			}
			if result.RowsAffected != 1 {
				return fmt.Errorf("%w: recovery point claim changed", backupasset.ErrConflict)
			}
			point.State = string(nextState)
			point.PointRevision = pointRevision + 1
			if held && request.Operation == backupasset.LifecycleExplicitPurge {
				claimed, err = coordinator.blockAttemptTx(
					ctx, tx, &attempt, &point, backupasset.LifecycleBlockedActiveHold,
				)
				return err
			}
		}
		claimed = lifecycleAttemptFromModel(attempt)
		return nil
	}()
	if err != nil {
		return LifecycleAttempt{}, err
	}
	return claimed, nil
}

func (coordinator *Coordinator) lockAttemptAndPointTx(
	ctx context.Context,
	tx *gorm.DB,
	attemptID string,
) (model.RecoveryPointLifecycleAttempt, model.RecoveryPoint, error) {
	var identity struct {
		RecoveryPointID string
	}
	loaded := tx.WithContext(ctx).Model(&model.RecoveryPointLifecycleAttempt{}).
		Select("recovery_point_id").Where("id = ?", attemptID).Limit(1).Find(&identity)
	if loaded.Error != nil {
		return model.RecoveryPointLifecycleAttempt{}, model.RecoveryPoint{}, fmt.Errorf("resolve lifecycle attempt point: %w", loaded.Error)
	}
	if loaded.RowsAffected != 1 {
		return model.RecoveryPointLifecycleAttempt{}, model.RecoveryPoint{}, fmt.Errorf("%w: lifecycle attempt", backupasset.ErrNotFound)
	}
	var point model.RecoveryPoint
	loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", identity.RecoveryPointID).Limit(1).Find(&point)
	if loaded.Error != nil {
		return model.RecoveryPointLifecycleAttempt{}, model.RecoveryPoint{}, fmt.Errorf("lock lifecycle recovery point: %w", loaded.Error)
	}
	if loaded.RowsAffected != 1 {
		return model.RecoveryPointLifecycleAttempt{}, model.RecoveryPoint{}, fmt.Errorf("%w: lifecycle recovery point", backupasset.ErrNotFound)
	}
	var attempt model.RecoveryPointLifecycleAttempt
	loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND recovery_point_id = ?", attemptID, point.ID).Limit(1).Find(&attempt)
	if loaded.Error != nil {
		return model.RecoveryPointLifecycleAttempt{}, model.RecoveryPoint{}, fmt.Errorf("lock lifecycle attempt: %w", loaded.Error)
	}
	if loaded.RowsAffected != 1 {
		return model.RecoveryPointLifecycleAttempt{}, model.RecoveryPoint{}, fmt.Errorf("%w: lifecycle attempt changed", backupasset.ErrConflict)
	}
	return attempt, point, nil
}

func (coordinator *Coordinator) ensureLifecycleFenceTx(
	ctx context.Context,
	tx *gorm.DB,
	attempt *model.RecoveryPointLifecycleAttempt,
) error {
	if attempt.LeaseID == nil || attempt.LeaseAttemptID == nil || attempt.LeaseFenceTokenHash == nil {
		return errLifecycleFenceLost
	}
	var leaseRow model.RecoveryPointLease
	loaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", *attempt.LeaseID).Limit(1).Find(&leaseRow)
	if loaded.Error != nil {
		return fmt.Errorf("load lifecycle lease fence: %w", loaded.Error)
	}
	if loaded.RowsAffected != 1 || leaseRow.RecoveryPointID != attempt.RecoveryPointID ||
		leaseRow.HolderType != string(backupasset.LeaseHolderRetentionWorker) || leaseRow.OwnerID != coordinator.leaseOwnerID ||
		leaseRow.AttemptID != *attempt.LeaseAttemptID || hashFenceToken(leaseRow.FenceToken) != *attempt.LeaseFenceTokenHash {
		return errLifecycleFenceLost
	}
	now := coordinator.now().UTC()
	if !now.Before(leaseRow.AbsoluteDeadline.UTC()) {
		status := backupasset.LeaseStatus(leaseRow.Status)
		if status == backupasset.LeaseActive || status == backupasset.LeaseExpired {
			return errLifecycleAbsoluteDeadline
		}
		return errLifecycleFenceLost
	}
	if backupasset.LeaseStatus(leaseRow.Status) != backupasset.LeaseActive {
		return errLifecycleFenceLost
	}
	if now.Before(leaseRow.LeaseExpiresAt.UTC()) {
		return nil
	}
	taken, err := coordinator.leases.TakeoverTx(ctx, tx, backupasset.TakeoverLeaseRequest{
		LeaseID: leaseRow.ID, OwnerID: leaseRow.OwnerID,
	})
	if err != nil {
		return fmt.Errorf("%w: take over lifecycle fence", errLifecycleFenceLost)
	}
	fenceHash := hashFenceToken(taken.Fence.FenceToken)
	result := tx.WithContext(ctx).Model(&model.RecoveryPointLifecycleAttempt{}).
		Where("id = ? AND transition_revision = ? AND lease_id = ? AND lease_attempt_id = ? AND lease_fence_token_hash = ?",
			attempt.ID, attempt.TransitionRevision, taken.ID, *attempt.LeaseAttemptID, *attempt.LeaseFenceTokenHash).
		Updates(map[string]any{
			"lease_attempt_id": taken.Fence.AttemptID, "lease_fence_token_hash": fenceHash,
			"transition_revision": attempt.TransitionRevision + 1, "heartbeat_at": now, "updated_at": now,
		})
	if result.Error != nil {
		return fmt.Errorf("persist lifecycle fence takeover: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return errLifecycleFenceLost
	}
	attempt.LeaseAttemptID = &taken.Fence.AttemptID
	attempt.LeaseFenceTokenHash = &fenceHash
	attempt.TransitionRevision++
	attempt.HeartbeatAt = &now
	attempt.UpdatedAt = now
	return nil
}

func (coordinator *Coordinator) adoptExpiredLifecycleFenceTx(
	ctx context.Context,
	tx *gorm.DB,
	attempt *model.RecoveryPointLifecycleAttempt,
) error {
	if attempt.LeaseID == nil || attempt.LeaseAttemptID == nil || attempt.LeaseFenceTokenHash == nil {
		return errLifecycleFenceLost
	}
	now := coordinator.now().UTC()
	var oldLease model.RecoveryPointLease
	loaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", *attempt.LeaseID).Limit(1).Find(&oldLease)
	if loaded.Error != nil {
		return fmt.Errorf("load expired lifecycle lease: %w", loaded.Error)
	}
	if loaded.RowsAffected != 1 || oldLease.RecoveryPointID != attempt.RecoveryPointID ||
		oldLease.HolderType != string(backupasset.LeaseHolderRetentionWorker) || oldLease.OwnerID != coordinator.leaseOwnerID ||
		oldLease.AttemptID != *attempt.LeaseAttemptID || hashFenceToken(oldLease.FenceToken) != *attempt.LeaseFenceTokenHash ||
		now.Before(oldLease.AbsoluteDeadline.UTC()) {
		return errLifecycleFenceLost
	}
	status := backupasset.LeaseStatus(oldLease.Status)
	if status == backupasset.LeaseActive {
		result := tx.WithContext(ctx).Model(&model.RecoveryPointLease{}).
			Where(`id = ? AND recovery_point_id = ? AND holder_type = ? AND owner_id = ? AND attempt_id = ? AND fence_token = ?
				AND status = ? AND absolute_deadline <= ?`,
				oldLease.ID, oldLease.RecoveryPointID, backupasset.LeaseHolderRetentionWorker, oldLease.OwnerID,
				oldLease.AttemptID, oldLease.FenceToken, backupasset.LeaseActive, now).
			Updates(map[string]any{"status": backupasset.LeaseExpired, "updated_at": now})
		if result.Error != nil {
			return fmt.Errorf("expire old lifecycle lease: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return errLifecycleFenceLost
		}
	} else if status != backupasset.LeaseExpired {
		return errLifecycleFenceLost
	}
	fresh, err := coordinator.leases.AcquireTx(ctx, tx, backupasset.AcquireLeaseRequest{
		RecoveryPointID: attempt.RecoveryPointID,
		HolderType:      backupasset.LeaseHolderRetentionWorker,
		OwnerID:         coordinator.leaseOwnerID,
	})
	if err != nil {
		return fmt.Errorf("adopt fresh lifecycle lease: %w", err)
	}
	freshHash := hashFenceToken(fresh.Fence.FenceToken)
	result := tx.WithContext(ctx).Model(&model.RecoveryPointLifecycleAttempt{}).
		Where("id = ? AND phase = ? AND transition_revision = ? AND lease_id = ? AND lease_attempt_id = ? AND lease_fence_token_hash = ?",
			attempt.ID, backupasset.LifecyclePhaseBlocked, attempt.TransitionRevision,
			oldLease.ID, oldLease.AttemptID, *attempt.LeaseFenceTokenHash).
		Updates(map[string]any{
			"lease_id": fresh.ID, "lease_attempt_id": fresh.Fence.AttemptID, "lease_fence_token_hash": freshHash,
			"transition_revision": attempt.TransitionRevision + 1, "heartbeat_at": now, "updated_at": now,
		})
	if result.Error != nil {
		return fmt.Errorf("persist fresh lifecycle lease: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return errLifecycleFenceLost
	}
	attempt.LeaseID = &fresh.ID
	attempt.LeaseAttemptID = &fresh.Fence.AttemptID
	attempt.LeaseFenceTokenHash = &freshHash
	attempt.TransitionRevision++
	attempt.HeartbeatAt = &now
	attempt.UpdatedAt = now
	return nil
}

func (coordinator *Coordinator) lifecycleFenceTx(
	ctx context.Context,
	tx *gorm.DB,
	attempt model.RecoveryPointLifecycleAttempt,
) (backupasset.LeaseFence, error) {
	if attempt.LeaseID == nil || attempt.LeaseAttemptID == nil || attempt.LeaseFenceTokenHash == nil {
		return backupasset.LeaseFence{}, errLifecycleFenceLost
	}
	var row model.RecoveryPointLease
	loaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", *attempt.LeaseID).Limit(1).Find(&row)
	if loaded.Error != nil {
		return backupasset.LeaseFence{}, fmt.Errorf("load lifecycle release fence: %w", loaded.Error)
	}
	if loaded.RowsAffected != 1 || row.RecoveryPointID != attempt.RecoveryPointID ||
		row.HolderType != string(backupasset.LeaseHolderRetentionWorker) || row.OwnerID != coordinator.leaseOwnerID ||
		row.AttemptID != *attempt.LeaseAttemptID || hashFenceToken(row.FenceToken) != *attempt.LeaseFenceTokenHash ||
		backupasset.LeaseStatus(row.Status) != backupasset.LeaseActive {
		return backupasset.LeaseFence{}, errLifecycleFenceLost
	}
	return backupasset.LeaseFence{
		LeaseID: row.ID, RecoveryPointID: row.RecoveryPointID,
		HolderType: backupasset.LeaseHolderType(row.HolderType), OwnerID: row.OwnerID,
		AttemptID: row.AttemptID, FenceToken: row.FenceToken,
	}, nil
}

func (coordinator *Coordinator) activeHoldTx(ctx context.Context, tx *gorm.DB, point model.RecoveryPoint) (bool, error) {
	var activeHolds int64
	if err := tx.WithContext(ctx).Model(&model.RecoveryPointHold{}).
		Where("recovery_point_id = ? AND state = ?", point.ID, backupasset.HoldActive).
		Count(&activeHolds).Error; err != nil {
		return false, fmt.Errorf("load lifecycle active holds: %w", err)
	}
	return point.HoldState == string(backupasset.HoldActive) || activeHolds != 0, nil
}

func (coordinator *Coordinator) drainOrdinaryLeasesTx(
	ctx context.Context,
	tx *gorm.DB,
	attempt model.RecoveryPointLifecycleAttempt,
) error {
	if attempt.LeaseID == nil {
		return fmt.Errorf("lifecycle lease is unavailable")
	}
	var leases []model.RecoveryPointLease
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("recovery_point_id = ? AND status = ? AND id <> ?", attempt.RecoveryPointID, backupasset.LeaseActive, *attempt.LeaseID).
		Where("holder_type <> ?", backupasset.LeaseHolderContentSession).
		Order("id ASC").Limit(maxLifecycleLeaseDrainBatch + 1).Find(&leases).Error; err != nil {
		return fmt.Errorf("load ordinary lifecycle leases: %w", err)
	}
	if len(leases) > maxLifecycleLeaseDrainBatch {
		return fmt.Errorf("lifecycle lease drain exceeds bounded batch")
	}
	now := coordinator.now().UTC()
	for _, row := range leases {
		if !now.Before(row.AbsoluteDeadline.UTC()) {
			result := tx.WithContext(ctx).Model(&model.RecoveryPointLease{}).
				Where("id = ? AND status = ? AND absolute_deadline <= ?", row.ID, backupasset.LeaseActive, now).
				Updates(map[string]any{"status": backupasset.LeaseExpired, "updated_at": now})
			if result.Error != nil || result.RowsAffected != 1 {
				return fmt.Errorf("expire ordinary lifecycle lease")
			}
			continue
		}
		if now.Before(row.LeaseExpiresAt.UTC()) {
			return errLifecycleLeaseLive
		}
		taken, err := coordinator.leases.TakeoverTx(ctx, tx, backupasset.TakeoverLeaseRequest{LeaseID: row.ID, OwnerID: row.OwnerID})
		if err != nil {
			return fmt.Errorf("take over ordinary lifecycle lease: %w", err)
		}
		if err := coordinator.leases.ReleaseTx(ctx, tx, taken.Fence); err != nil {
			return fmt.Errorf("release ordinary lifecycle lease: %w", err)
		}
	}
	return nil
}

func (coordinator *Coordinator) transitionAttemptTx(
	ctx context.Context,
	tx *gorm.DB,
	attempt *model.RecoveryPointLifecycleAttempt,
	from backupasset.LifecyclePhase,
	to backupasset.LifecyclePhase,
) (LifecycleAttempt, error) {
	now := coordinator.now().UTC()
	result := tx.WithContext(ctx).Model(&model.RecoveryPointLifecycleAttempt{}).
		Where("id = ? AND phase = ? AND transition_revision = ?", attempt.ID, from, attempt.TransitionRevision).
		Updates(map[string]any{
			"phase": to, "blocked_reason": "", "retry_at": nil,
			"transition_revision": attempt.TransitionRevision + 1, "heartbeat_at": now, "updated_at": now,
		})
	if result.Error != nil {
		return LifecycleAttempt{}, fmt.Errorf("advance lifecycle attempt: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return LifecycleAttempt{}, fmt.Errorf("%w: lifecycle attempt changed", backupasset.ErrConflict)
	}
	attempt.Phase = string(to)
	attempt.BlockedReason = ""
	attempt.RetryAt = nil
	attempt.TransitionRevision++
	attempt.HeartbeatAt = &now
	attempt.UpdatedAt = now
	return lifecycleAttemptFromModel(*attempt), nil
}

func (coordinator *Coordinator) blockAttemptTx(
	ctx context.Context,
	tx *gorm.DB,
	attempt *model.RecoveryPointLifecycleAttempt,
	point *model.RecoveryPoint,
	reason backupasset.LifecycleBlockedReason,
) (LifecycleAttempt, error) {
	if err := backupasset.ValidateLifecycleBlockedReason(reason); err != nil {
		return LifecycleAttempt{}, err
	}
	now := coordinator.now().UTC()
	retryAt := now.Add(coordinator.retryDelay)
	if backupasset.LifecycleOperation(attempt.Operation) != backupasset.LifecycleMutableRetire &&
		backupasset.RecoveryPointState(point.State) != backupasset.RecoveryPointPurgeBlocked {
		result := tx.WithContext(ctx).Model(&model.RecoveryPoint{}).
			Where("id = ? AND point_revision = ?", point.ID, point.PointRevision).
			Updates(map[string]any{
				"state": backupasset.RecoveryPointPurgeBlocked, "point_revision": point.PointRevision + 1, "updated_at": now,
			})
		if result.Error != nil {
			return LifecycleAttempt{}, fmt.Errorf("block lifecycle recovery point: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return LifecycleAttempt{}, fmt.Errorf("%w: lifecycle recovery point changed", backupasset.ErrConflict)
		}
		point.State = string(backupasset.RecoveryPointPurgeBlocked)
		point.PointRevision++
	}
	result := tx.WithContext(ctx).Model(&model.RecoveryPointLifecycleAttempt{}).
		Where("id = ? AND transition_revision = ? AND phase <> ?", attempt.ID, attempt.TransitionRevision, backupasset.LifecyclePhaseComplete).
		Updates(map[string]any{
			"phase": backupasset.LifecyclePhaseBlocked, "blocked_reason": reason, "retry_at": retryAt,
			"transition_revision": attempt.TransitionRevision + 1, "heartbeat_at": now, "updated_at": now,
		})
	if result.Error != nil {
		return LifecycleAttempt{}, fmt.Errorf("block lifecycle attempt: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return LifecycleAttempt{}, fmt.Errorf("%w: lifecycle attempt changed", backupasset.ErrConflict)
	}
	attempt.Phase = string(backupasset.LifecyclePhaseBlocked)
	attempt.BlockedReason = string(reason)
	attempt.RetryAt = &retryAt
	attempt.TransitionRevision++
	attempt.HeartbeatAt = &now
	attempt.UpdatedAt = now
	return lifecycleAttemptFromModel(*attempt), nil
}

func validateClaimRequest(request ClaimRequest) error {
	if backupasset.ValidateOpaqueID(request.RecoveryPointID) != nil {
		return fmt.Errorf("%w: invalid lifecycle claim", backupasset.ErrInvalidState)
	}
	switch request.Operation {
	case backupasset.LifecycleRetentionExpire:
		if request.PolicySelection == nil || request.MutablePoint != nil || request.PurgePlan != nil {
			return fmt.Errorf("%w: invalid retention expiry claim", backupasset.ErrInvalidState)
		}
		selection := request.PolicySelection
		if backupasset.ValidateOpaqueID(selection.PolicyID) != nil || selection.PolicyRevision < 1 ||
			backupasset.ValidateOpaqueID(selection.ScopeID) != nil || len(selection.RuleDigest) != 64 ||
			selection.EvaluatedAt.IsZero() || selection.EvaluatedAt.Location() != time.UTC {
			return fmt.Errorf("%w: invalid lifecycle policy selection", backupasset.ErrInvalidState)
		}
	case backupasset.LifecycleMutableRetire:
		if request.PolicySelection != nil || request.MutablePoint == nil || request.PurgePlan != nil ||
			request.MutablePoint.PointRevision < 1 || request.MutablePoint.CapabilityRevision < 1 {
			return fmt.Errorf("%w: invalid mutable retirement claim", backupasset.ErrInvalidState)
		}
	case backupasset.LifecycleExplicitPurge:
		if request.PolicySelection != nil || request.MutablePoint != nil || request.PurgePlan == nil ||
			backupasset.ValidateOpaqueID(request.PurgePlan.PlanID) != nil || request.PurgePlan.Revision < 1 ||
			request.PurgePlan.ActorID == 0 || request.PurgePlan.PointRevision < 1 || request.PurgePlan.CapabilityRevision < 1 {
			return fmt.Errorf("%w: invalid explicit purge claim", backupasset.ErrInvalidState)
		}
	default:
		return fmt.Errorf("%w: invalid lifecycle operation", backupasset.ErrInvalidState)
	}
	return nil
}

func sameLifecycleClaim(attempt model.RecoveryPointLifecycleAttempt, request ClaimRequest) bool {
	if attempt.RecoveryPointID != request.RecoveryPointID ||
		backupasset.LifecycleOperation(attempt.Operation) != request.Operation {
		return false
	}
	switch request.Operation {
	case backupasset.LifecycleRetentionExpire:
		selection := request.PolicySelection
		return selection != nil && attempt.PolicyID != nil && *attempt.PolicyID == selection.PolicyID &&
			attempt.PolicyRevision != nil && *attempt.PolicyRevision == selection.PolicyRevision &&
			attempt.PolicyRuleDigest != nil && *attempt.PolicyRuleDigest == selection.RuleDigest &&
			attempt.EvaluationTime != nil && attempt.EvaluationTime.UTC().Equal(selection.EvaluatedAt.UTC())
	case backupasset.LifecycleExplicitPurge:
		snapshot := request.PurgePlan
		return snapshot != nil && attempt.PurgePlanID != nil && *attempt.PurgePlanID == snapshot.PlanID &&
			attempt.PurgePlanRevision != nil && *attempt.PurgePlanRevision == snapshot.Revision &&
			attempt.PurgeActorID != nil && *attempt.PurgeActorID == snapshot.ActorID
	case backupasset.LifecycleMutableRetire:
		return request.MutablePoint != nil
	default:
		return false
	}
}

func validateRetentionClaimPoint(request ClaimRequest, point model.RecoveryPoint) (SelectedPoint, error) {
	selection := request.PolicySelection
	if selection.ScopeKind == backupasset.RetentionPolicyScopeRepository && selection.ScopeID != point.RepositoryID {
		return SelectedPoint{}, fmt.Errorf("%w: recovery point is outside policy scope", backupasset.ErrConflict)
	}
	var selected *SelectedPoint
	for index := range selection.Points {
		candidate := &selection.Points[index]
		if candidate.RecoveryPointID == request.RecoveryPointID {
			if selected != nil {
				return SelectedPoint{}, fmt.Errorf("%w: duplicate recovery point selection", backupasset.ErrInvalidState)
			}
			selected = candidate
		}
	}
	if selected == nil || selected.PointRevision < 1 || selected.CapabilityRevision < 1 {
		return SelectedPoint{}, fmt.Errorf("%w: recovery point is absent from policy selection", backupasset.ErrConflict)
	}
	semantics := backupasset.PointVersionSemantics(point.Semantics)
	state := backupasset.RecoveryPointState(point.State)
	if semantics == backupasset.PointMutableHead ||
		state != backupasset.RecoveryPointCommitted && state != backupasset.RecoveryPointDegraded ||
		point.PointRevision != selected.PointRevision || point.CapabilityRevision != selected.CapabilityRevision ||
		point.PhysicalAvailability != string(backupasset.PhysicalOnline) {
		return SelectedPoint{}, fmt.Errorf("%w: recovery point no longer matches policy selection", backupasset.ErrConflict)
	}
	return *selected, nil
}

func validateMutableRetirementClaimPoint(request ClaimRequest, point model.RecoveryPoint) error {
	if request.MutablePoint == nil || backupasset.PointVersionSemantics(point.Semantics) != backupasset.PointMutableHead ||
		backupasset.RecoveryPointState(point.State) != backupasset.RecoveryPointObserved ||
		point.PointRevision != request.MutablePoint.PointRevision ||
		point.CapabilityRevision != request.MutablePoint.CapabilityRevision ||
		point.PhysicalAvailability != string(backupasset.PhysicalOnline) || point.EncryptedProviderLocator == "" {
		return fmt.Errorf("%w: mutable head no longer matches retirement claim", backupasset.ErrConflict)
	}
	return nil
}

func (coordinator *Coordinator) validateExplicitPurgeClaimPointTx(
	ctx context.Context,
	tx *gorm.DB,
	request ClaimRequest,
	point model.RecoveryPoint,
) error {
	snapshot := request.PurgePlan
	if snapshot == nil || point.PointRevision != snapshot.PointRevision ||
		point.CapabilityRevision != snapshot.CapabilityRevision ||
		point.PhysicalAvailability != string(backupasset.PhysicalOnline) {
		return fmt.Errorf("%w: recovery point no longer matches purge plan", backupasset.ErrConflict)
	}
	state := backupasset.RecoveryPointState(point.State)
	semantics := backupasset.PointVersionSemantics(point.Semantics)
	if semantics == backupasset.PointMutableHead {
		if state != backupasset.RecoveryPointObserved && state != backupasset.RecoveryPointRetired {
			return fmt.Errorf("%w: mutable point is not purgeable", backupasset.ErrConflict)
		}
		if state == backupasset.RecoveryPointObserved && point.EncryptedProviderLocator == "" ||
			state == backupasset.RecoveryPointRetired && point.EncryptedRollbackLocator == "" {
			return fmt.Errorf("%w: mutable point locator is unavailable", backupasset.ErrConflict)
		}
	} else if state != backupasset.RecoveryPointCommitted && state != backupasset.RecoveryPointDegraded {
		return fmt.Errorf("%w: immutable point is not purgeable", backupasset.ErrConflict)
	}
	var plan model.BackupAssetPurgePlan
	loaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", snapshot.PlanID).Limit(1).Find(&plan)
	if loaded.Error != nil {
		return fmt.Errorf("load explicit purge plan: %w", loaded.Error)
	}
	now := coordinator.now().UTC()
	if loaded.RowsAffected != 1 || plan.RepositoryID != point.RepositoryID || plan.Revision != snapshot.Revision ||
		plan.RequesterID != snapshot.ActorID || plan.ExecuteActorID == nil || *plan.ExecuteActorID != snapshot.ActorID ||
		plan.Status != string(backupasset.PurgePlanExecuting) || !now.Before(plan.ExpiresAt.UTC()) {
		return fmt.Errorf("%w: explicit purge plan changed", backupasset.ErrConflict)
	}
	var item model.BackupAssetPurgePlanItem
	loaded = tx.WithContext(ctx).Where("plan_id = ? AND recovery_point_id = ?", plan.ID, point.ID).Limit(1).Find(&item)
	if loaded.Error != nil {
		return fmt.Errorf("load explicit purge plan item: %w", loaded.Error)
	}
	if loaded.RowsAffected != 1 || item.ExpectedPointRevision != snapshot.PointRevision ||
		item.ExpectedCapabilityRevision != snapshot.CapabilityRevision {
		return fmt.Errorf("%w: explicit purge plan item changed", backupasset.ErrConflict)
	}
	return nil
}

func hashFenceToken(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}

func lifecycleAttemptFromModel(attempt model.RecoveryPointLifecycleAttempt) LifecycleAttempt {
	result := LifecycleAttempt{
		ID: attempt.ID, RecoveryPointID: attempt.RecoveryPointID,
		Operation: backupasset.LifecycleOperation(attempt.Operation), Phase: backupasset.LifecyclePhase(attempt.Phase),
		TransitionRevision: attempt.TransitionRevision, BlockedReason: backupasset.LifecycleBlockedReason(attempt.BlockedReason),
		ClaimedAt: attempt.ClaimedAt, HeartbeatAt: attempt.HeartbeatAt, RetryAt: attempt.RetryAt,
		CompletedAt: attempt.CompletedAt, CreatedAt: attempt.CreatedAt.UTC(), UpdatedAt: attempt.UpdatedAt.UTC(),
	}
	if attempt.LeaseID != nil {
		result.LeaseID = *attempt.LeaseID
	}
	if attempt.LeaseAttemptID != nil {
		result.LeaseAttemptID = *attempt.LeaseAttemptID
	}
	if attempt.LeaseFenceTokenHash != nil {
		result.LeaseFenceTokenHash = *attempt.LeaseFenceTokenHash
	}
	return result
}
