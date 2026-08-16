package recovery

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrInvalidRecoveryWorker        = errors.New("invalid recovery worker contract")
	ErrRecoveryWorkerObjectNotFound = errors.New("recovery worker object not found")
	ErrRecoveryWorkerFenceLost      = errors.New("recovery worker fence lost")
	ErrRecoveryWorkerUnavailable    = errors.New("recovery worker unavailable")

	errRecoveryWorkerClaimConflict = errors.New("recovery worker claim conflict")
	errRecoverySchedulerConflict   = errors.New("recovery worker scheduler conflict")
	errRecoveryCleanupKeyConflict  = errors.New("recovery cleanup-key reconciliation conflict")
)

const (
	recoveryWorkerMaxLeaseRenewMargin = 5 * time.Minute
	recoveryWorkerMaxExecutionTimeout = 24 * time.Hour

	recoveryWorkspacePlaintextTTL                       = 24 * time.Hour
	recoveryUnresolvedProjectionTimeout                 = 5 * time.Second
	recoveryWorkspaceLocatorDirectory                   = "jobs"
	recoveryWorkspaceMarkerBindingDomain                = "xirang/recovery/workspace-marker/v1"
	recoveryPreWriteDriftFailureCategory                = "pre_write_drift"
	recoveryVerificationMismatchFailureCategory         = "verification_mismatch"
	recoveryRemoteOutcomeUnresolvedFailureCategory      = "remote_outcome_unresolved"
	recoveryCancellationAfterMutationArmFailureCategory = "canceled_after_mutation_arm"
	recoveryCleanupKeyUnavailableFailureCategory        = "cleanup_key_unavailable"
	recoverySchedulerStateKind                          = "scheduler_state"
	recoveryClaimSchedulerScope                         = "claim"
	recoveryTakeoverSchedulerScope                      = "takeover"
	recoveryClaimSchedulerRowID                         = "0000000000000000000000000000006a"
	recoveryTakeoverSchedulerRowID                      = "0000000000000000000000000000006b"
	recoverySchedulerCASAttempts                        = 8
)

// WorkerPolicy is the immutable timing authority shared by job creation and
// the managed claim owner.
type WorkerPolicy struct {
	LeaseRenewMargin time.Duration
	ExecutionTimeout time.Duration
}

func (policy WorkerPolicy) valid() bool {
	return policy.LeaseRenewMargin > 0 && policy.LeaseRenewMargin <= recoveryWorkerMaxLeaseRenewMargin &&
		policy.ExecutionTimeout > 0 && policy.ExecutionTimeout <= recoveryWorkerMaxExecutionTimeout
}

// Validate rejects policy products that were not produced by the bounded
// Recovery settings registry.
func (policy WorkerPolicy) Validate() error {
	if !policy.valid() {
		return ErrInvalidRecoveryWorker
	}
	return nil
}

// ClaimSchedulerRowID and TakeoverSchedulerRowID expose the immutable
// scheduler identifiers to runtime composition without duplicating values
// outside the Recovery package.
func ClaimSchedulerRowID() string { return recoveryClaimSchedulerRowID }

func TakeoverSchedulerRowID() string { return recoveryTakeoverSchedulerRowID }

type RecoverySourceLeaseCoordinator interface {
	RenewTx(context.Context, *gorm.DB, backupasset.LeaseFence) (backupasset.Lease, error)
	TakeoverTx(context.Context, *gorm.DB, backupasset.TakeoverLeaseRequest) (backupasset.Lease, error)
	ReleaseTx(context.Context, *gorm.DB, backupasset.LeaseFence) error
}

// RecoveryWorkspaceKeySource supplies the installation-stable key used to
// bind an isolated workspace marker before any target mutation is allowed.
type RecoveryWorkspaceKeySource interface {
	Active(context.Context, backupasset.KeyDomain) (backupasset.DomainKeyMaterial, error)
	ByVersion(context.Context, backupasset.KeyDomain, int) (backupasset.DomainKeyMaterial, error)
}

type WorkerCoordinatorDependencies struct {
	DB              *gorm.DB
	Metrics         Metrics
	Audit           RecoveryAPIAuditWriter
	SourceLeases    RecoverySourceLeaseCoordinator
	LiveRevalidator RecoveryAuthorityRevalidator
	WorkspaceKeys   RecoveryWorkspaceKeySource
	Target          TargetPort
	SourceResolver  provider.RsyncRestoreSourceResolver
	Now             func() time.Time
	LeaseTTL        time.Duration
	ScanLimit       int
}

type WorkerCoordinator struct {
	db              *gorm.DB
	metrics         Metrics
	audit           RecoveryAPIAuditWriter
	sourceValidator *SourceValidator
	sourceLeases    RecoverySourceLeaseCoordinator
	liveRevalidator RecoveryAuthorityRevalidator
	workspaceKeys   RecoveryWorkspaceKeySource
	target          TargetPort
	sourceResolver  provider.RsyncRestoreSourceResolver
	now             func() time.Time
	leaseTTL        time.Duration
	scanLimit       int
	declaredMu      sync.Mutex
	declaredWrites  map[string]*declaredWriteContext
}

type declaredWriteContext struct {
	permit  TargetWritePermit
	object  TargetObjectRef
	entry   provider.RestoreEntry
	target  provider.RsyncBoundRemoteTarget
	result  TargetWriteResult
	claimed bool
	ready   bool
}

type observedRecoveryAuthority struct {
	binding     RecoveryAuthorityBinding
	observation RecoveryAuthorityObservation
}

func (coordinator *WorkerCoordinator) observeRecoveryAuthority(
	ctx context.Context,
	plan model.BackupAssetRecoveryPlan,
	preflight model.BackupAssetRecoveryPreflight,
) (observedRecoveryAuthority, error) {
	if coordinator == nil || coordinator.liveRevalidator == nil {
		return observedRecoveryAuthority{}, ErrInvalidRecoveryWorker
	}
	providerKind, err := recoveryAuthorityProvider(ctx, coordinator.db, plan)
	if err != nil {
		return observedRecoveryAuthority{}, err
	}
	binding := recoveryAuthorityBinding(AuthorizationReceiptExecute, providerKind, plan, preflight)
	observation, err := coordinator.liveRevalidator.ObserveRecoveryAuthority(ctx, binding)
	if err != nil {
		return observedRecoveryAuthority{}, err
	}
	return observedRecoveryAuthority{binding: binding, observation: observation}, nil
}

// observeRecoveryAuthorityForJob loads only the closed durable binding needed
// by the external observation. The observation itself therefore always runs
// before the later caller-owned mutation transaction is opened.
func (coordinator *WorkerCoordinator) observeRecoveryAuthorityForJob(
	ctx context.Context,
	jobID string,
) (observedRecoveryAuthority, error) {
	if coordinator == nil || coordinator.db == nil || !validOpaqueID(jobID) {
		return observedRecoveryAuthority{}, ErrInvalidRecoveryWorker
	}
	var reference struct {
		PlanID      string `gorm:"column:plan_id"`
		PreflightID string `gorm:"column:preflight_id"`
	}
	loaded := coordinator.db.WithContext(ctx).Table((model.BackupAssetRecoveryJob{}).TableName()).
		Select("plan_id, preflight_id").Where("id = ?", jobID).Limit(1).Find(&reference)
	if loaded.Error != nil {
		return observedRecoveryAuthority{}, loaded.Error
	}
	if loaded.RowsAffected != 1 || !validOpaqueID(reference.PlanID) || !validOpaqueID(reference.PreflightID) {
		return observedRecoveryAuthority{}, ErrRecoveryWorkerFenceLost
	}
	var plan model.BackupAssetRecoveryPlan
	loaded = coordinator.db.WithContext(ctx).Where("id = ?", reference.PlanID).Limit(1).Find(&plan)
	if loaded.Error != nil {
		return observedRecoveryAuthority{}, loaded.Error
	}
	if loaded.RowsAffected != 1 || PlanState(plan.State) != PlanStateExecuted {
		return observedRecoveryAuthority{}, ErrRecoveryWorkerFenceLost
	}
	var preflight model.BackupAssetRecoveryPreflight
	loaded = coordinator.db.WithContext(ctx).
		Where("id = ? AND plan_id = ?", reference.PreflightID, plan.ID).Limit(1).Find(&preflight)
	if loaded.Error != nil {
		return observedRecoveryAuthority{}, loaded.Error
	}
	if loaded.RowsAffected != 1 {
		return observedRecoveryAuthority{}, ErrRecoveryWorkerFenceLost
	}
	return coordinator.observeRecoveryAuthority(ctx, plan, preflight)
}

func (coordinator *WorkerCoordinator) revalidateObservedRecoveryAuthorityTx(
	ctx context.Context,
	tx *gorm.DB,
	plan model.BackupAssetRecoveryPlan,
	preflight model.BackupAssetRecoveryPreflight,
	observed observedRecoveryAuthority,
) error {
	if coordinator == nil || coordinator.liveRevalidator == nil || tx == nil {
		return ErrInvalidRecoveryWorker
	}
	if !validRecoveryProvider(observed.binding.Provider) {
		return ErrRecoveryTargetChanged
	}
	binding := recoveryAuthorityBinding(
		AuthorizationReceiptExecute, observed.binding.Provider, plan, preflight,
	)
	if binding != observed.binding {
		return ErrRecoveryTargetChanged
	}
	return coordinator.liveRevalidator.RevalidateRecoveryAuthorityTx(
		ctx, tx, binding, observed.observation,
	)
}

// withTransactionDB creates the narrow coordinator view used by helpers that
// must stay inside a caller-owned transaction. Runtime declaration state is
// intentionally excluded because copying its mutex or sharing its map under a
// different lock would break the Provider writer boundary.
func (coordinator *WorkerCoordinator) withTransactionDB(tx *gorm.DB) *WorkerCoordinator {
	if coordinator == nil || tx == nil {
		return nil
	}
	return &WorkerCoordinator{
		db: tx.Session(&gorm.Session{DisableNestedTransaction: true}), metrics: coordinator.metrics,
		sourceValidator: coordinator.sourceValidator, sourceLeases: coordinator.sourceLeases,
		liveRevalidator: coordinator.liveRevalidator, workspaceKeys: coordinator.workspaceKeys,
		target: coordinator.target, sourceResolver: coordinator.sourceResolver,
		now: coordinator.now, leaseTTL: coordinator.leaseTTL, scanLimit: coordinator.scanLimit,
	}
}

type RecoveryWorkerClaim struct {
	JobID              string
	AttemptID          string
	NodeLeaseID        string
	WorkerID           string
	AttemptFence       uint64
	NodeFence          uint64
	TransitionRevision uint64
	LeaseExpiresAt     time.Time
	AbsoluteDeadline   time.Time
	SourceFence        backupasset.LeaseFence
}

type recoverySchedulerCandidate struct {
	KeyAt time.Time `gorm:"column:key_at"`
	KeyID string    `gorm:"column:key_id"`
	JobID string    `gorm:"column:job_id"`
}

type recoverySchedulerSpec struct {
	rowID       string
	scope       string
	keyAtColumn string
	keyIDColumn string
	jobIDColumn string
	eligible    func(*gorm.DB, time.Time) *gorm.DB
}

func NewWorkerCoordinator(dependencies WorkerCoordinatorDependencies) (*WorkerCoordinator, error) {
	if dependencies.DB == nil || dependencies.SourceLeases == nil || dependencies.LeaseTTL <= 0 ||
		dependencies.SourceResolver == nil || dependencies.LeaseTTL > 24*time.Hour ||
		dependencies.ScanLimit <= 0 || dependencies.ScanLimit > 1000 {
		return nil, ErrInvalidRecoveryWorker
	}
	sourceValidator, err := NewSourceValidator(dependencies.DB)
	if err != nil {
		return nil, ErrInvalidRecoveryWorker
	}
	if dependencies.Now == nil {
		dependencies.Now = func() time.Time { return time.Now().UTC() }
	}
	if dependencies.Metrics == nil {
		dependencies.Metrics = NoopMetrics{}
	}
	return &WorkerCoordinator{
		db: dependencies.DB, metrics: dependencies.Metrics, audit: dependencies.Audit,
		sourceValidator: sourceValidator, sourceLeases: dependencies.SourceLeases,
		liveRevalidator: dependencies.LiveRevalidator, workspaceKeys: dependencies.WorkspaceKeys,
		target: dependencies.Target, sourceResolver: dependencies.SourceResolver,
		now: dependencies.Now, leaseTTL: dependencies.LeaseTTL, scanLimit: dependencies.ScanLimit,
		declaredWrites: make(map[string]*declaredWriteContext),
	}, nil
}

// ExecuteClaimWithRsyncTargetWriter runs the normal fenced Recovery lifecycle
// while routing regular-file mutation through the injected Provider writer.
// Recovery still mints and validates the private item permit; the Provider only
// receives the public projection and the pinned declared stream.
func (coordinator *WorkerCoordinator) ExecuteClaimWithRsyncTargetWriter(
	ctx context.Context,
	claim RecoveryWorkerClaim,
	source provider.RsyncRestoreSource,
	writer provider.RsyncTargetWriter,
	target provider.RsyncBoundRemoteTarget,
) error {
	if writer == nil {
		return ErrInvalidRecoveryWorker
	}
	return coordinator.executeClaim(ctx, claim, source, "", writer, target)
}

// RsyncTargetWriter returns the only Provider-facing adapter that can consume
// a declaration registered by this coordinator. The adapter carries no target
// authority of its own; the private item permit remains inside Recovery.
func (coordinator *WorkerCoordinator) RsyncTargetWriter() provider.RsyncTargetWriter {
	return recoveryRsyncTargetWriter{coordinator: coordinator}
}

type recoveryRsyncTargetWriter struct {
	coordinator *WorkerCoordinator
}

func (writer recoveryRsyncTargetWriter) WriteDeclaredRegular(
	ctx context.Context,
	call provider.RsyncTargetWriteCall,
) error {
	if writer.coordinator == nil {
		return ErrInvalidRecoveryWorker
	}
	return writer.coordinator.forwardDeclaredRegular(ctx, call)
}

func (coordinator *WorkerCoordinator) forwardDeclaredRegular(
	ctx context.Context,
	call provider.RsyncTargetWriteCall,
) error {
	if coordinator == nil || coordinator.target == nil || call.Source == nil {
		return ErrInvalidRecoveryWorker
	}
	claim := recoveryWorkerClaimFromTargetPermit(call.Permit)
	key := declaredWriteKey(claim, call.Entry.AssetRef.EntryID)
	coordinator.declaredMu.Lock()
	declaration, ok := coordinator.declaredWrites[key]
	if !ok || declaration == nil || declaration.claimed || declaration.ready ||
		targetMutationPermitProjection(declaration.permit, call.Target.TargetBindingDigest) != call.Permit ||
		declaration.entry != call.Entry || !sameRsyncTargetBinding(declaration.target, call.Target) {
		coordinator.declaredMu.Unlock()
		return ErrRecoveryWorkerFenceLost
	}
	declaration.claimed = true
	coordinator.declaredMu.Unlock()

	result, err := coordinator.target.WriteAtomic(ctx, declaration.permit, TargetWriteAtomicRequest{
		Object: declaration.object, ExpectedBytes: declaration.entry.ExpectedSize,
		ExpectedDigest: declaration.entry.ExpectedDigest, Content: call.Source,
	})
	if err != nil {
		coordinator.discardDeclaredRegular(claim, call.Entry.AssetRef.EntryID)
		return err
	}
	coordinator.declaredMu.Lock()
	declaration, ok = coordinator.declaredWrites[key]
	if ok && declaration != nil {
		declaration.result = result
		declaration.ready = true
	}
	coordinator.declaredMu.Unlock()
	return nil
}

func (coordinator *WorkerCoordinator) discardDeclaredRegular(claim RecoveryWorkerClaim, entryID string) {
	if coordinator == nil {
		return
	}
	coordinator.declaredMu.Lock()
	delete(coordinator.declaredWrites, declaredWriteKey(claim, entryID))
	coordinator.declaredMu.Unlock()
}

func targetMutationPermitProjection(permit TargetWritePermit, targetBindingDigest string) provider.TargetMutationPermit {
	var credentialRevision string
	if permit.permit.proof != nil {
		credentialRevision = permit.permit.proof.sessionBinding.CredentialRevision
	}
	return provider.TargetMutationPermit{
		TargetBindingDigest:    targetBindingDigest,
		UseLatchID:             provider.RestoreSchemaUseLatchID,
		JobID:                  permit.permit.JobID,
		AttemptID:              permit.permit.AttemptID,
		NodeLeaseID:            permit.permit.NodeLeaseID,
		AttemptFence:           permit.permit.AttemptFence,
		NodeFence:              permit.permit.NodeFence,
		ExpectedTargetRevision: permit.permit.ExpectedTargetRevision,
		Session: provider.TargetSession{
			ID: permit.permit.AttemptID, Purpose: provider.TargetPurposeWrite,
			CredentialRevision: credentialRevision, ExpiresAt: permit.permit.ExpiresAt,
		},
	}
}

func (coordinator *WorkerCoordinator) takeDeclaredRegularResult(
	claim RecoveryWorkerClaim,
	entry provider.RestoreEntry,
) (TargetWriteResult, bool) {
	key := declaredWriteKey(claim, entry.AssetRef.EntryID)
	coordinator.declaredMu.Lock()
	declaration, ok := coordinator.declaredWrites[key]
	if ok {
		delete(coordinator.declaredWrites, key)
	}
	coordinator.declaredMu.Unlock()
	if !ok || declaration == nil || !declaration.ready {
		return TargetWriteResult{}, false
	}
	return declaration.result, true
}

func declaredWriteKey(claim RecoveryWorkerClaim, entryID string) string {
	return claim.JobID + "\x00" + claim.AttemptID + "\x00" + claim.NodeLeaseID + "\x00" +
		strconv.FormatUint(claim.AttemptFence, 10) + "\x00" + strconv.FormatUint(claim.NodeFence, 10) + "\x00" + entryID
}

func recoveryWorkerClaimFromTargetPermit(permit provider.TargetMutationPermit) RecoveryWorkerClaim {
	return RecoveryWorkerClaim{
		JobID: permit.JobID, AttemptID: permit.AttemptID, NodeLeaseID: permit.NodeLeaseID,
		AttemptFence: permit.AttemptFence, NodeFence: permit.NodeFence,
	}
}

func sameRsyncTargetBinding(left, right provider.RsyncBoundRemoteTarget) bool {
	return left.NodeID == right.NodeID && left.RootID == right.RootID &&
		left.TargetBindingDigest == right.TargetBindingDigest &&
		left.TargetPathDigest == right.TargetPathDigest && left.RootRevision == right.RootRevision &&
		left.TargetRevision == right.TargetRevision
}

type recoveryCleanupKeyCandidate struct {
	JobID     string `gorm:"column:job_id"`
	AttemptID string `gorm:"column:attempt_id"`
}

type recoveryCleanupKeyBinding struct {
	JobID                string `gorm:"column:job_id"`
	JobState             string `gorm:"column:job_state"`
	FailureCategory      string `gorm:"column:failure_category"`
	TransitionRevision   uint64 `gorm:"column:transition_revision"`
	WorkspacePhase       string `gorm:"column:workspace_phase"`
	TargetMode           string `gorm:"column:target_mode"`
	TargetChainRevision  string `gorm:"column:target_chain_revision"`
	AttemptID            string `gorm:"column:attempt_id"`
	AttemptOwnerID       string `gorm:"column:attempt_owner_id"`
	AttemptFence         uint64 `gorm:"column:attempt_fence"`
	NodeLeaseID          string `gorm:"column:node_lease_id"`
	RecoveryPointLeaseID string `gorm:"column:recovery_point_lease_id"`
}

func reconcilePermanentCleanupKeyLoss(
	ctx context.Context,
	db *gorm.DB,
	now time.Time,
	scanLimit int,
) (int, error) {
	if db == nil || now.IsZero() || scanLimit <= 0 || scanLimit > 1000 {
		return 0, ErrInvalidRecoveryWorker
	}
	ctx = recoveryWorkerContext(ctx)
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	now = now.UTC()

	var candidates []recoveryCleanupKeyCandidate
	result := recoveryCleanupKeyBindingQuery(db.WithContext(ctx), now).
		Select("recovery_job.id AS job_id, attempt_row.id AS attempt_id").
		Order("recovery_job.updated_at ASC, recovery_job.id ASC").
		Limit(scanLimit).
		Scan(&candidates)
	if result.Error != nil {
		return 0, fmt.Errorf("%w: list permanent cleanup-key recovery work", ErrRecoveryWorkerUnavailable)
	}

	reconciled := 0
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return reconciled, err
		}
		changed, err := reconcilePermanentCleanupKeyCandidate(ctx, db, now, candidate)
		if err != nil {
			return reconciled, fmt.Errorf("%w: reconcile permanent cleanup-key recovery work", ErrRecoveryWorkerUnavailable)
		}
		if changed {
			reconciled++
		}
	}
	return reconciled, nil
}

// ReconcilePermanentCleanupKeyLoss performs the startup-only metadata handoff
// required when cleanup ownership material can no longer be recovered. It
// deliberately reads only the columns needed to revalidate current fences, so
// encrypted workspace locators are never loaded or decrypted on this path.
func ReconcilePermanentCleanupKeyLoss(
	ctx context.Context,
	db *gorm.DB,
	now time.Time,
	scanLimit int,
) (int, error) {
	return reconcilePermanentCleanupKeyLoss(ctx, db, now, scanLimit)
}

func (coordinator *WorkerCoordinator) ReconcilePermanentCleanupKeyLoss(ctx context.Context) (int, error) {
	if coordinator == nil || coordinator.db == nil || coordinator.now == nil ||
		coordinator.scanLimit <= 0 || coordinator.scanLimit > 1000 {
		return 0, ErrInvalidRecoveryWorker
	}
	return ReconcilePermanentCleanupKeyLoss(ctx, coordinator.db, coordinator.now().UTC(), coordinator.scanLimit)
}

func recoveryCleanupKeyBindingQuery(db *gorm.DB, now time.Time) *gorm.DB {
	return db.Table("backup_asset_recovery_jobs AS recovery_job").
		Joins(`JOIN backup_asset_recovery_plans AS recovery_plan
			ON recovery_plan.id = recovery_job.plan_id`).
		Joins(`JOIN backup_asset_recovery_attempts AS attempt_row
			ON attempt_row.job_id = recovery_job.id
			AND attempt_row.state = ? AND attempt_row.mutation_armed = ?
			AND attempt_row.lease_expires_at > ?`, AttemptStateRunning, true, now).
		Joins(`JOIN backup_asset_recovery_node_leases AS node_lease
			ON node_lease.job_id = recovery_job.id
			AND node_lease.node_id = recovery_job.target_node_id
			AND node_lease.holder_kind = ?
			AND node_lease.attempt_id = attempt_row.id
			AND node_lease.owner_id = attempt_row.owner_id
			AND node_lease.fence = attempt_row.fence
			AND node_lease.state = ? AND node_lease.lease_expires_at > ?`,
			backupasset.LeaseHolderRecoveryJob, "active", now).
		Joins(`JOIN recovery_point_leases AS source_lease
			ON source_lease.recovery_point_id = recovery_plan.recovery_point_id
			AND source_lease.holder_type = ? AND source_lease.owner_id = recovery_job.id
			AND source_lease.attempt_id = attempt_row.id
			AND source_lease.fence_token <> ''
			AND source_lease.status = ? AND source_lease.lease_expires_at > ?
			AND source_lease.absolute_deadline > ?`,
			backupasset.LeaseHolderRecoveryJob, backupasset.LeaseActive, now, now).
		Where(`recovery_job.state IN ? AND recovery_job.transition_revision > 0
			AND ((recovery_job.target_mode = ? AND recovery_job.workspace_phase = ?)
				OR (recovery_job.target_mode = ? AND recovery_job.workspace_phase IN ?))`,
			[]string{string(JobStateRunning), string(JobStateVerifying), string(JobStateCancelRequested)},
			TargetModeInPlace, WorkspacePhaseNone, TargetModeIsolated,
			[]string{
				string(WorkspacePhaseReserved), string(WorkspacePhaseMarkerCreated),
				string(WorkspacePhaseWriting), string(WorkspacePhaseSealed), string(WorkspacePhaseCleanupDue),
			})
}

func reconcilePermanentCleanupKeyCandidate(
	ctx context.Context,
	db *gorm.DB,
	now time.Time,
	candidate recoveryCleanupKeyCandidate,
) (bool, error) {
	changed := false
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var binding recoveryCleanupKeyBinding
		loaded := recoveryCleanupKeyBindingQuery(
			tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}),
			now,
		).
			Select(`recovery_job.id AS job_id, recovery_job.state AS job_state,
				recovery_job.failure_category, recovery_job.transition_revision,
				recovery_job.workspace_phase, recovery_job.target_mode,
				recovery_job.target_chain_revision, attempt_row.id AS attempt_id,
				attempt_row.owner_id AS attempt_owner_id, attempt_row.fence AS attempt_fence,
				node_lease.id AS node_lease_id, source_lease.id AS recovery_point_lease_id`).
			Where("recovery_job.id = ? AND attempt_row.id = ?", candidate.JobID, candidate.AttemptID).
			Limit(1).
			Scan(&binding)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 {
			return nil
		}

		nextWorkspacePhase := string(WorkspacePhaseNone)
		switch TargetMode(binding.TargetMode) {
		case TargetModeIsolated:
			nextWorkspacePhase = string(WorkspacePhaseCleanupDue)
		case TargetModeInPlace:
		default:
			return errRecoveryCleanupKeyConflict
		}

		updated := tx.WithContext(ctx).Table((model.BackupAssetRecoveryAttempt{}).TableName()).
			Where(`id = ? AND job_id = ? AND owner_id = ? AND fence = ?
				AND state = ? AND mutation_armed = ? AND lease_expires_at > ?`,
				binding.AttemptID, binding.JobID, binding.AttemptOwnerID, binding.AttemptFence,
				AttemptStateRunning, true, now).
			Updates(map[string]any{
				"state": string(AttemptStateFailed), "closed_at": now, "updated_at": now,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return errRecoveryCleanupKeyConflict
		}

		updated = tx.WithContext(ctx).Table((model.BackupAssetRecoveryJob{}).TableName()).
			Where(`id = ? AND state = ? AND failure_category = ? AND transition_revision = ?
				AND workspace_phase = ? AND target_mode = ? AND target_chain_revision = ?`,
				binding.JobID, binding.JobState, binding.FailureCategory, binding.TransitionRevision,
				binding.WorkspacePhase, binding.TargetMode, binding.TargetChainRevision).
			Updates(map[string]any{
				"state":               string(JobStateNeedsAttention),
				"failure_category":    recoveryCleanupKeyUnavailableFailureCategory,
				"transition_revision": binding.TransitionRevision + 1,
				"workspace_phase":     nextWorkspacePhase,
				"updated_at":          now,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return errRecoveryCleanupKeyConflict
		}
		changed = true
		return nil
	})
	if errors.Is(err, errRecoveryCleanupKeyConflict) {
		return false, nil
	}
	return changed, err
}

func (coordinator *WorkerCoordinator) ClaimNext(
	ctx context.Context,
	workerID string,
) (RecoveryWorkerClaim, bool, error) {
	if coordinator == nil || coordinator.db == nil || !validRecoveryWorkerID(workerID) {
		return RecoveryWorkerClaim{}, false, ErrInvalidRecoveryWorker
	}
	ctx = recoveryWorkerContext(ctx)
	if err := ctx.Err(); err != nil {
		return RecoveryWorkerClaim{}, false, err
	}

	for scan := 0; scan < coordinator.scanLimit; scan++ {
		candidate, found, err := coordinator.reserveRecoverySchedulerCandidate(ctx, recoverySchedulerSpec{
			rowID: recoveryClaimSchedulerRowID, scope: recoveryClaimSchedulerScope,
			keyAtColumn: "recovery_job.updated_at", keyIDColumn: "recovery_job.id", jobIDColumn: "recovery_job.id",
			eligible: coordinator.claimEligibleRecoveryJobs,
		})
		if err != nil {
			return RecoveryWorkerClaim{}, false, err
		}
		if !found {
			return RecoveryWorkerClaim{}, false, nil
		}
		claim, err := coordinator.claimJob(ctx, candidate.JobID, workerID)
		if err == nil {
			coordinator.observeJobState(ctx, claim.JobID, JobStateRunning)
			return claim, true, nil
		}
		if errors.Is(err, errRecoveryWorkerClaimConflict) || errors.Is(err, ErrRecoveryWorkerFenceLost) {
			continue
		}
		return RecoveryWorkerClaim{}, false, err
	}
	return RecoveryWorkerClaim{}, false, nil
}

func (coordinator *WorkerCoordinator) claimEligibleRecoveryJobs(db *gorm.DB, now time.Time) *gorm.DB {
	return db.Table("backup_asset_recovery_jobs AS recovery_job").
		Joins("JOIN backup_asset_recovery_plans AS recovery_plan ON recovery_plan.id = recovery_job.plan_id").
		Joins(`JOIN recovery_point_leases AS source_lease
			ON source_lease.recovery_point_id = recovery_plan.recovery_point_id
			AND source_lease.holder_type = ? AND source_lease.owner_id = recovery_job.id
			AND source_lease.status = ? AND source_lease.lease_expires_at > ?
			AND source_lease.absolute_deadline > ?`, backupasset.LeaseHolderRecoveryJob, backupasset.LeaseActive, now, now).
		Joins(`JOIN backup_asset_recovery_node_leases AS node_lease
			ON node_lease.job_id = recovery_job.id AND node_lease.node_id = recovery_job.target_node_id
			AND node_lease.attempt_id = source_lease.attempt_id AND node_lease.state = ?
			AND node_lease.lease_expires_at > ?`, "active", now).
		Joins(`JOIN backup_asset_recovery_attempts AS attempt_row
			ON attempt_row.id = source_lease.attempt_id AND attempt_row.job_id = recovery_job.id
			AND attempt_row.owner_id = node_lease.owner_id AND attempt_row.fence = node_lease.fence
			AND attempt_row.state = ? AND attempt_row.lease_expires_at > ?`, AttemptStateClaimed, now).
		Where("recovery_job.state = ? AND recovery_job.transition_revision > 0", JobStateQueued)
}

func (coordinator *WorkerCoordinator) takeoverEligibleRecoveryAttempts(db *gorm.DB, now time.Time) *gorm.DB {
	return db.Table("backup_asset_recovery_attempts AS attempt_row").
		Joins(`JOIN backup_asset_recovery_jobs AS recovery_job
			ON recovery_job.id = attempt_row.job_id AND recovery_job.state IN ?
			AND recovery_job.transition_revision > 0`, []string{string(JobStateQueued), string(JobStateRunning)}).
		Joins("JOIN backup_asset_recovery_plans AS recovery_plan ON recovery_plan.id = recovery_job.plan_id").
		Joins(`JOIN recovery_point_leases AS source_lease
			ON source_lease.recovery_point_id = recovery_plan.recovery_point_id
			AND source_lease.holder_type = ? AND source_lease.owner_id = recovery_job.id
			AND source_lease.attempt_id = attempt_row.id AND source_lease.status = ?
			AND source_lease.lease_expires_at <= ? AND source_lease.absolute_deadline > ?`,
			backupasset.LeaseHolderRecoveryJob, backupasset.LeaseActive, now, now).
		Joins(`JOIN backup_asset_recovery_node_leases AS node_lease
			ON node_lease.job_id = recovery_job.id AND node_lease.node_id = recovery_job.target_node_id
			AND node_lease.attempt_id = attempt_row.id AND node_lease.owner_id = attempt_row.owner_id
			AND node_lease.fence = attempt_row.fence AND node_lease.state = ?
			AND node_lease.lease_expires_at <= ?`, "active", now).
		Where("attempt_row.state IN ? AND attempt_row.lease_expires_at <= ?",
			[]string{string(AttemptStateClaimed), string(AttemptStateRunning)}, now)
}

func (coordinator *WorkerCoordinator) reserveRecoverySchedulerCandidate(
	ctx context.Context,
	spec recoverySchedulerSpec,
) (recoverySchedulerCandidate, bool, error) {
	if spec.eligible == nil || !validOpaqueID(spec.rowID) ||
		(spec.scope != recoveryClaimSchedulerScope && spec.scope != recoveryTakeoverSchedulerScope) {
		return recoverySchedulerCandidate{}, false, ErrInvalidRecoveryWorker
	}
	for attempt := 0; attempt < recoverySchedulerCASAttempts; attempt++ {
		var candidate recoverySchedulerCandidate
		found := false
		err := coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			now := coordinator.now().UTC()
			if err := ensureRecoverySchedulerRowsTx(ctx, tx, now); err != nil {
				return err
			}
			var state model.BackupAssetRecoveryEvidence
			loaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
				Where("id = ?", spec.rowID).Limit(1).Find(&state)
			if loaded.Error != nil {
				return loaded.Error
			}
			if loaded.RowsAffected != 1 || !validRecoverySchedulerState(state, spec) {
				return ErrRecoveryWorkerUnavailable
			}

			highWater := recoverySchedulerCandidate{}
			if state.SchedulerHighWaterAt != nil {
				highWater.KeyAt = state.SchedulerHighWaterAt.UTC()
				highWater.KeyID = state.SchedulerHighWaterID
			}
			cursor := recoverySchedulerCandidate{}
			if state.SchedulerCursorAt != nil {
				cursor.KeyAt = state.SchedulerCursorAt.UTC()
				cursor.KeyID = state.SchedulerCursorID
			}

			var reserveErr error
			if highWater.KeyID != "" {
				candidate, found, reserveErr = selectRecoverySchedulerCandidate(ctx, tx, spec, now, cursor, highWater)
				if reserveErr != nil {
					return reserveErr
				}
			}
			if !found {
				highWater, found, reserveErr = selectRecoverySchedulerHighWater(ctx, tx, spec, now)
				if reserveErr != nil || !found {
					return reserveErr
				}
				candidate, found, reserveErr = selectRecoverySchedulerCandidate(
					ctx, tx, spec, now, recoverySchedulerCandidate{}, highWater,
				)
				if reserveErr != nil || !found {
					return reserveErr
				}
			}

			updated := tx.WithContext(ctx).Model(&model.BackupAssetRecoveryEvidence{}).
				Where("id = ? AND kind = ? AND scheduler_scope = ? AND scheduler_revision = ?",
					state.ID, recoverySchedulerStateKind, spec.scope, state.SchedulerRevision).
				Updates(map[string]any{
					"scheduler_cursor_at":     candidate.KeyAt.UTC(),
					"scheduler_cursor_id":     candidate.KeyID,
					"scheduler_high_water_at": highWater.KeyAt.UTC(),
					"scheduler_high_water_id": highWater.KeyID,
					"scheduler_revision":      state.SchedulerRevision + 1,
					"updated_at":              now,
				})
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return errRecoverySchedulerConflict
			}
			return nil
		})
		if errors.Is(err, errRecoverySchedulerConflict) {
			continue
		}
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return recoverySchedulerCandidate{}, false, err
			}
			return recoverySchedulerCandidate{}, false, fmt.Errorf("%w: reserve %s recovery candidate",
				ErrRecoveryWorkerUnavailable, spec.scope)
		}
		return candidate, found, nil
	}
	return recoverySchedulerCandidate{}, false, fmt.Errorf("%w: serialize %s recovery scheduler",
		ErrRecoveryWorkerUnavailable, spec.scope)
}

func selectRecoverySchedulerHighWater(
	ctx context.Context,
	tx *gorm.DB,
	spec recoverySchedulerSpec,
	now time.Time,
) (recoverySchedulerCandidate, bool, error) {
	var candidate recoverySchedulerCandidate
	result := spec.eligible(tx.WithContext(ctx), now).
		Select(fmt.Sprintf("%s AS key_at, %s AS key_id, %s AS job_id",
			spec.keyAtColumn, spec.keyIDColumn, spec.jobIDColumn)).
		Order(spec.keyAtColumn + " DESC").Order(spec.keyIDColumn + " DESC").
		Limit(1).Find(&candidate)
	if result.Error != nil {
		return recoverySchedulerCandidate{}, false, result.Error
	}
	if result.RowsAffected == 0 {
		return recoverySchedulerCandidate{}, false, nil
	}
	if !validRecoverySchedulerCandidate(candidate) {
		return recoverySchedulerCandidate{}, false, ErrRecoveryWorkerUnavailable
	}
	return candidate, true, nil
}

func selectRecoverySchedulerCandidate(
	ctx context.Context,
	tx *gorm.DB,
	spec recoverySchedulerSpec,
	now time.Time,
	cursor recoverySchedulerCandidate,
	highWater recoverySchedulerCandidate,
) (recoverySchedulerCandidate, bool, error) {
	query := spec.eligible(tx.WithContext(ctx), now).
		Select(fmt.Sprintf("%s AS key_at, %s AS key_id, %s AS job_id",
			spec.keyAtColumn, spec.keyIDColumn, spec.jobIDColumn)).
		Where(fmt.Sprintf("(%s < ? OR (%s = ? AND %s <= ?))",
			spec.keyAtColumn, spec.keyAtColumn, spec.keyIDColumn),
			highWater.KeyAt.UTC(), highWater.KeyAt.UTC(), highWater.KeyID)
	if cursor.KeyID != "" {
		query = query.Where(fmt.Sprintf("(%s > ? OR (%s = ? AND %s > ?))",
			spec.keyAtColumn, spec.keyAtColumn, spec.keyIDColumn),
			cursor.KeyAt.UTC(), cursor.KeyAt.UTC(), cursor.KeyID)
	}
	var candidate recoverySchedulerCandidate
	result := query.Order(spec.keyAtColumn + " ASC").Order(spec.keyIDColumn + " ASC").
		Limit(1).Find(&candidate)
	if result.Error != nil {
		return recoverySchedulerCandidate{}, false, result.Error
	}
	if result.RowsAffected == 0 {
		return recoverySchedulerCandidate{}, false, nil
	}
	if !validRecoverySchedulerCandidate(candidate) {
		return recoverySchedulerCandidate{}, false, ErrRecoveryWorkerUnavailable
	}
	return candidate, true, nil
}

func ensureRecoverySchedulerRowsTx(ctx context.Context, tx *gorm.DB, now time.Time) error {
	for _, row := range []model.BackupAssetRecoveryEvidence{
		{ID: recoveryClaimSchedulerRowID, Kind: recoverySchedulerStateKind,
			SchedulerScope: recoveryClaimSchedulerScope, SchedulerRevision: 1, CreatedAt: now, UpdatedAt: now},
		{ID: recoveryTakeoverSchedulerRowID, Kind: recoverySchedulerStateKind,
			SchedulerScope: recoveryTakeoverSchedulerScope, SchedulerRevision: 1, CreatedAt: now, UpdatedAt: now},
	} {
		if err := tx.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
			return err
		}
	}
	return nil
}

func validRecoverySchedulerState(state model.BackupAssetRecoveryEvidence, spec recoverySchedulerSpec) bool {
	if state.ID != spec.rowID || state.Kind != recoverySchedulerStateKind || state.SchedulerScope != spec.scope ||
		state.SchedulerRevision == 0 || state.CreatedAt.IsZero() || state.UpdatedAt.Before(state.CreatedAt) ||
		(state.SchedulerCursorAt == nil) != (state.SchedulerCursorID == "") ||
		(state.SchedulerHighWaterAt == nil) != (state.SchedulerHighWaterID == "") {
		return false
	}
	if state.SchedulerCursorAt != nil && state.SchedulerHighWaterAt == nil {
		return false
	}
	if state.SchedulerCursorAt != nil {
		cursor := recoverySchedulerCandidate{KeyAt: state.SchedulerCursorAt.UTC(), KeyID: state.SchedulerCursorID, JobID: state.SchedulerCursorID}
		highWater := recoverySchedulerCandidate{KeyAt: state.SchedulerHighWaterAt.UTC(), KeyID: state.SchedulerHighWaterID, JobID: state.SchedulerHighWaterID}
		return validRecoverySchedulerCandidate(cursor) && validRecoverySchedulerCandidate(highWater) &&
			compareRecoverySchedulerCandidate(cursor, highWater) <= 0
	}
	return state.SchedulerHighWaterAt == nil || validOpaqueID(state.SchedulerHighWaterID)
}

func validRecoverySchedulerCandidate(candidate recoverySchedulerCandidate) bool {
	return !candidate.KeyAt.IsZero() && validOpaqueID(candidate.KeyID) && validOpaqueID(candidate.JobID)
}

func compareRecoverySchedulerCandidate(left, right recoverySchedulerCandidate) int {
	if left.KeyAt.Before(right.KeyAt) {
		return -1
	}
	if left.KeyAt.After(right.KeyAt) {
		return 1
	}
	return strings.Compare(left.KeyID, right.KeyID)
}

func (coordinator *WorkerCoordinator) claimJob(
	ctx context.Context,
	jobID string,
	workerID string,
) (RecoveryWorkerClaim, error) {
	var claim RecoveryWorkerClaim
	err := coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job model.BackupAssetRecoveryJob
		loaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ?", jobID).Limit(1).Find(&job)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || job.State != string(JobStateQueued) || job.TransitionRevision == 0 {
			return errRecoveryWorkerClaimConflict
		}

		now := coordinator.now().UTC()
		var source model.RecoveryPointLease
		loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("holder_type = ? AND owner_id = ? AND status = ?", backupasset.LeaseHolderRecoveryJob, job.ID, backupasset.LeaseActive).
			Limit(1).Find(&source)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || !now.Before(source.LeaseExpiresAt.UTC()) ||
			!now.Before(source.AbsoluteDeadline.UTC()) {
			return errRecoveryWorkerClaimConflict
		}

		var node model.BackupAssetRecoveryNodeLease
		loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("job_id = ? AND state = ?", job.ID, "active").Limit(1).Find(&node)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || node.AttemptID == nil || *node.AttemptID != source.AttemptID ||
			node.NodeID != job.TargetNodeID || !now.Before(node.LeaseExpiresAt.UTC()) {
			return errRecoveryWorkerClaimConflict
		}

		var attempt model.BackupAssetRecoveryAttempt
		loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ? AND job_id = ?", source.AttemptID, job.ID).Limit(1).Find(&attempt)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || attempt.State != string(AttemptStateClaimed) ||
			attempt.LeaseExpiresAt == nil || !now.Before(attempt.LeaseExpiresAt.UTC()) ||
			attempt.Fence == 0 || attempt.Fence != node.Fence || attempt.OwnerID != node.OwnerID {
			return errRecoveryWorkerClaimConflict
		}

		sourceFence := recoverySourceFence(source)
		renewed, err := coordinator.sourceLeases.RenewTx(ctx, tx, sourceFence)
		if err != nil {
			return recoveryWorkerSourceError(err)
		}
		if !renewed.AbsoluteDeadline.UTC().Equal(source.AbsoluteDeadline.UTC()) ||
			renewed.LeaseExpiresAt.UTC().After(source.AbsoluteDeadline.UTC()) {
			return ErrRecoveryWorkerFenceLost
		}
		nextExpiry := now.Add(coordinator.leaseTTL)
		if renewed.LeaseExpiresAt.Before(nextExpiry) {
			nextExpiry = renewed.LeaseExpiresAt.UTC()
		}
		if !nextExpiry.After(now) {
			return ErrRecoveryWorkerFenceLost
		}

		updated := tx.WithContext(ctx).Model(&model.BackupAssetRecoveryNodeLease{}).
			Where("id = ? AND job_id = ? AND attempt_id = ? AND owner_id = ? AND fence = ? AND state = ? AND lease_expires_at > ?",
				node.ID, job.ID, attempt.ID, node.OwnerID, node.Fence, "active", now).
			Updates(map[string]any{"owner_id": workerID, "lease_expires_at": nextExpiry, "updated_at": now})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return errRecoveryWorkerClaimConflict
		}
		updated = tx.WithContext(ctx).Model(&model.BackupAssetRecoveryAttempt{}).
			Where("id = ? AND job_id = ? AND owner_id = ? AND fence = ? AND state = ? AND lease_expires_at > ?",
				attempt.ID, job.ID, attempt.OwnerID, attempt.Fence, AttemptStateClaimed, now).
			Updates(map[string]any{
				"owner_id": workerID, "state": string(AttemptStateRunning), "lease_expires_at": nextExpiry,
				"heartbeat_at": now, "updated_at": now,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return errRecoveryWorkerClaimConflict
		}
		nextRevision := job.TransitionRevision + 1
		updated = tx.WithContext(ctx).Model(&model.BackupAssetRecoveryJob{}).
			Where("id = ? AND state = ? AND transition_revision = ?", job.ID, JobStateQueued, job.TransitionRevision).
			Updates(map[string]any{"state": string(JobStateRunning), "transition_revision": nextRevision, "updated_at": now})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return errRecoveryWorkerClaimConflict
		}

		claim = RecoveryWorkerClaim{
			JobID: job.ID, AttemptID: attempt.ID, NodeLeaseID: node.ID, WorkerID: workerID,
			AttemptFence: attempt.Fence, NodeFence: node.Fence, TransitionRevision: nextRevision,
			LeaseExpiresAt: nextExpiry, AbsoluteDeadline: source.AbsoluteDeadline.UTC(), SourceFence: sourceFence,
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, errRecoveryWorkerClaimConflict) || errors.Is(err, ErrRecoveryWorkerFenceLost) {
			return RecoveryWorkerClaim{}, err
		}
		return RecoveryWorkerClaim{}, fmt.Errorf("%w: claim recovery job", ErrRecoveryWorkerUnavailable)
	}
	return claim, nil
}

func (coordinator *WorkerCoordinator) TakeoverExpired(
	ctx context.Context,
	workerID string,
) (RecoveryWorkerClaim, bool, error) {
	if coordinator == nil || coordinator.db == nil || !validRecoveryWorkerID(workerID) {
		return RecoveryWorkerClaim{}, false, ErrInvalidRecoveryWorker
	}
	ctx = recoveryWorkerContext(ctx)
	if err := ctx.Err(); err != nil {
		return RecoveryWorkerClaim{}, false, err
	}
	for scan := 0; scan < coordinator.scanLimit; scan++ {
		candidate, found, err := coordinator.reserveRecoverySchedulerCandidate(ctx, recoverySchedulerSpec{
			rowID: recoveryTakeoverSchedulerRowID, scope: recoveryTakeoverSchedulerScope,
			keyAtColumn: "attempt_row.lease_expires_at", keyIDColumn: "attempt_row.id", jobIDColumn: "attempt_row.job_id",
			eligible: coordinator.takeoverEligibleRecoveryAttempts,
		})
		if err != nil {
			return RecoveryWorkerClaim{}, false, err
		}
		if !found {
			return RecoveryWorkerClaim{}, false, nil
		}
		claim, err := coordinator.takeoverJob(ctx, candidate.JobID, candidate.KeyID, workerID)
		if err == nil {
			coordinator.observeJobState(ctx, claim.JobID, JobStateRunning)
			return claim, true, nil
		}
		if errors.Is(err, errRecoveryWorkerClaimConflict) || errors.Is(err, ErrRecoveryWorkerFenceLost) {
			continue
		}
		return RecoveryWorkerClaim{}, false, err
	}
	return RecoveryWorkerClaim{}, false, nil
}

func (coordinator *WorkerCoordinator) takeoverJob(
	ctx context.Context,
	jobID string,
	oldAttemptID string,
	workerID string,
) (RecoveryWorkerClaim, error) {
	var claim RecoveryWorkerClaim
	err := coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := coordinator.now().UTC()
		var job model.BackupAssetRecoveryJob
		loaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ?", jobID).Limit(1).Find(&job)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || (job.State != string(JobStateQueued) && job.State != string(JobStateRunning)) ||
			job.TransitionRevision == 0 {
			return errRecoveryWorkerClaimConflict
		}

		var source model.RecoveryPointLease
		loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("holder_type = ? AND owner_id = ? AND attempt_id = ? AND status = ?",
				backupasset.LeaseHolderRecoveryJob, job.ID, oldAttemptID, backupasset.LeaseActive).
			Limit(1).Find(&source)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || now.Before(source.LeaseExpiresAt.UTC()) ||
			!now.Before(source.AbsoluteDeadline.UTC()) {
			return errRecoveryWorkerClaimConflict
		}
		renewed, err := coordinator.sourceLeases.TakeoverTx(ctx, tx, backupasset.TakeoverLeaseRequest{
			LeaseID: source.ID, OwnerID: job.ID,
		})
		if err != nil {
			return recoveryWorkerSourceError(err)
		}
		if !renewed.AbsoluteDeadline.UTC().Equal(source.AbsoluteDeadline.UTC()) ||
			renewed.LeaseExpiresAt.UTC().After(source.AbsoluteDeadline.UTC()) {
			return ErrRecoveryWorkerFenceLost
		}
		if renewed.Fence.AttemptID == oldAttemptID || !validOpaqueID(renewed.Fence.AttemptID) {
			return ErrRecoveryWorkerFenceLost
		}

		var node model.BackupAssetRecoveryNodeLease
		loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("job_id = ? AND state = ?", job.ID, "active").Limit(1).Find(&node)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || node.AttemptID == nil || *node.AttemptID != oldAttemptID ||
			node.NodeID != job.TargetNodeID || now.Before(node.LeaseExpiresAt.UTC()) || node.Fence == 0 {
			return errRecoveryWorkerClaimConflict
		}

		var oldAttempt model.BackupAssetRecoveryAttempt
		loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ? AND job_id = ?", oldAttemptID, job.ID).Limit(1).Find(&oldAttempt)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || (oldAttempt.State != string(AttemptStateClaimed) &&
			oldAttempt.State != string(AttemptStateRunning)) || oldAttempt.LeaseExpiresAt == nil ||
			now.Before(oldAttempt.LeaseExpiresAt.UTC()) || oldAttempt.OwnerID != node.OwnerID ||
			oldAttempt.Fence != node.Fence {
			return errRecoveryWorkerClaimConflict
		}

		nextExpiry := now.Add(coordinator.leaseTTL)
		if renewed.LeaseExpiresAt.Before(nextExpiry) {
			nextExpiry = renewed.LeaseExpiresAt.UTC()
		}
		if !nextExpiry.After(now) {
			return ErrRecoveryWorkerFenceLost
		}
		closed := tx.WithContext(ctx).Model(&model.BackupAssetRecoveryAttempt{}).
			Where("id = ? AND job_id = ? AND owner_id = ? AND fence = ? AND state IN ? AND lease_expires_at <= ?",
				oldAttempt.ID, job.ID, oldAttempt.OwnerID, oldAttempt.Fence,
				[]string{string(AttemptStateClaimed), string(AttemptStateRunning)}, now).
			Updates(map[string]any{"state": string(AttemptStateLost), "closed_at": now, "updated_at": now})
		if closed.Error != nil {
			return closed.Error
		}
		if closed.RowsAffected != 1 {
			return errRecoveryWorkerClaimConflict
		}

		nextFence := oldAttempt.Fence + 1
		if node.Fence >= nextFence {
			nextFence = node.Fence + 1
		}
		attempt := model.BackupAssetRecoveryAttempt{
			ID: renewed.Fence.AttemptID, JobID: job.ID, OwnerID: workerID, Fence: nextFence,
			State: string(AttemptStateRunning), MutationArmed: oldAttempt.MutationArmed,
			LeaseExpiresAt: timePointerValue(nextExpiry), HeartbeatAt: timePointerValue(now),
			CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.WithContext(ctx).Create(&attempt).Error; err != nil {
			return err
		}
		updated := tx.WithContext(ctx).Model(&model.BackupAssetRecoveryNodeLease{}).
			Where("id = ? AND job_id = ? AND attempt_id = ? AND owner_id = ? AND fence = ? AND state = ? AND lease_expires_at <= ?",
				node.ID, job.ID, oldAttempt.ID, node.OwnerID, node.Fence, "active", now).
			Updates(map[string]any{
				"attempt_id": attempt.ID, "owner_id": workerID, "fence": nextFence,
				"lease_expires_at": nextExpiry, "updated_at": now,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return errRecoveryWorkerClaimConflict
		}
		nextRevision := job.TransitionRevision + 1
		updated = tx.WithContext(ctx).Model(&model.BackupAssetRecoveryJob{}).
			Where("id = ? AND state = ? AND transition_revision = ?", job.ID, job.State, job.TransitionRevision).
			Updates(map[string]any{
				"state": string(JobStateRunning), "transition_revision": nextRevision, "updated_at": now,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return errRecoveryWorkerClaimConflict
		}

		claim = RecoveryWorkerClaim{
			JobID: job.ID, AttemptID: attempt.ID, NodeLeaseID: node.ID, WorkerID: workerID,
			AttemptFence: nextFence, NodeFence: nextFence, TransitionRevision: nextRevision,
			LeaseExpiresAt: nextExpiry, AbsoluteDeadline: source.AbsoluteDeadline.UTC(), SourceFence: renewed.Fence,
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, errRecoveryWorkerClaimConflict) || errors.Is(err, ErrRecoveryWorkerFenceLost) {
			return RecoveryWorkerClaim{}, err
		}
		return RecoveryWorkerClaim{}, fmt.Errorf("%w: take over recovery job: %w", ErrRecoveryWorkerUnavailable, err)
	}
	return claim, nil
}

func (coordinator *WorkerCoordinator) Heartbeat(
	ctx context.Context,
	claim RecoveryWorkerClaim,
) (RecoveryWorkerClaim, error) {
	if coordinator == nil || coordinator.db == nil || !validRecoveryWorkerClaim(claim) {
		return RecoveryWorkerClaim{}, ErrInvalidRecoveryWorker
	}
	ctx = recoveryWorkerContext(ctx)
	if err := ctx.Err(); err != nil {
		return RecoveryWorkerClaim{}, err
	}
	updatedClaim := claim
	err := coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := coordinator.now().UTC()
		var job model.BackupAssetRecoveryJob
		loaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ?", claim.JobID).Limit(1).Find(&job)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || job.State != string(JobStateRunning) ||
			job.TransitionRevision != claim.TransitionRevision {
			return ErrRecoveryWorkerFenceLost
		}
		renewed, err := coordinator.sourceLeases.RenewTx(ctx, tx, claim.SourceFence)
		if err != nil {
			return recoveryWorkerSourceError(err)
		}
		if !renewed.AbsoluteDeadline.UTC().Equal(claim.AbsoluteDeadline.UTC()) ||
			renewed.LeaseExpiresAt.UTC().After(claim.AbsoluteDeadline.UTC()) {
			return ErrRecoveryWorkerFenceLost
		}

		var node model.BackupAssetRecoveryNodeLease
		loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ?", claim.NodeLeaseID).Limit(1).Find(&node)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || node.JobID != claim.JobID || node.AttemptID == nil ||
			*node.AttemptID != claim.AttemptID || node.OwnerID != claim.WorkerID || node.Fence != claim.NodeFence ||
			node.State != "active" || !now.Before(node.LeaseExpiresAt.UTC()) {
			return ErrRecoveryWorkerFenceLost
		}

		var attempt model.BackupAssetRecoveryAttempt
		loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ?", claim.AttemptID).Limit(1).Find(&attempt)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || attempt.JobID != claim.JobID || attempt.OwnerID != claim.WorkerID ||
			attempt.Fence != claim.AttemptFence || attempt.State != string(AttemptStateRunning) ||
			attempt.LeaseExpiresAt == nil || !now.Before(attempt.LeaseExpiresAt.UTC()) {
			return ErrRecoveryWorkerFenceLost
		}

		nextExpiry := now.Add(coordinator.leaseTTL)
		if renewed.LeaseExpiresAt.Before(nextExpiry) {
			nextExpiry = renewed.LeaseExpiresAt.UTC()
		}
		if !nextExpiry.After(now) {
			return ErrRecoveryWorkerFenceLost
		}
		result := tx.WithContext(ctx).Model(&model.BackupAssetRecoveryNodeLease{}).
			Where("id = ? AND job_id = ? AND attempt_id = ? AND owner_id = ? AND fence = ? AND state = ? AND lease_expires_at > ?",
				node.ID, claim.JobID, claim.AttemptID, claim.WorkerID, claim.NodeFence, "active", now).
			Updates(map[string]any{"lease_expires_at": nextExpiry, "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrRecoveryWorkerFenceLost
		}
		result = tx.WithContext(ctx).Model(&model.BackupAssetRecoveryAttempt{}).
			Where("id = ? AND job_id = ? AND owner_id = ? AND fence = ? AND state = ? AND lease_expires_at > ?",
				attempt.ID, claim.JobID, claim.WorkerID, claim.AttemptFence, AttemptStateRunning, now).
			Updates(map[string]any{"lease_expires_at": nextExpiry, "heartbeat_at": now, "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrRecoveryWorkerFenceLost
		}
		updatedClaim.LeaseExpiresAt = nextExpiry
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrRecoveryWorkerFenceLost) {
			return RecoveryWorkerClaim{}, ErrRecoveryWorkerFenceLost
		}
		return RecoveryWorkerClaim{}, fmt.Errorf("%w: heartbeat recovery job", ErrRecoveryWorkerUnavailable)
	}
	return updatedClaim, nil
}

// PrepareFirstWrite is the only authority boundary that can mint a target
// mutation permit. It commits all durable first-write facts before the permit
// becomes usable, so a TargetPort can never receive mutation authority without
// the permanent latch and current source, attempt, and node fences.
func (coordinator *WorkerCoordinator) PrepareFirstWrite(
	ctx context.Context,
	claim RecoveryWorkerClaim,
) (TargetWritePermit, error) {
	return coordinator.prepareFirstWrite(ctx, claim, "", nil)
}

func (coordinator *WorkerCoordinator) prepareFirstWrite(
	ctx context.Context,
	claim RecoveryWorkerClaim,
	reconciledOverwriteCheckpointID string,
	validateLocked func(context.Context, *gorm.DB) error,
) (TargetWritePermit, error) {
	if coordinator == nil || coordinator.db == nil || coordinator.sourceValidator == nil ||
		coordinator.sourceLeases == nil || coordinator.liveRevalidator == nil || coordinator.workspaceKeys == nil ||
		!validRecoveryWorkerClaim(claim) ||
		(reconciledOverwriteCheckpointID != "" && !validOpaqueID(reconciledOverwriteCheckpointID)) {
		return TargetWritePermit{}, ErrInvalidRecoveryWorker
	}
	ctx = recoveryWorkerContext(ctx)
	if err := ctx.Err(); err != nil {
		return TargetWritePermit{}, err
	}
	observedAuthority, err := coordinator.observeRecoveryAuthorityForJob(ctx, claim.JobID)
	if err != nil {
		if errors.Is(err, ErrRecoveryWorkerFenceLost) || errors.Is(err, ErrRecoverySourceChanged) {
			return TargetWritePermit{}, err
		}
		return TargetWritePermit{}, fmt.Errorf("%w: observe first-write recovery authority", ErrRecoveryWorkerUnavailable)
	}

	ownershipKey, err := coordinator.workspaceKeys.Active(ctx, backupasset.KeyDomainRecoveryCleanupOwnership)
	if err != nil || !validRecoveryWorkspaceKey(ownershipKey) {
		return TargetWritePermit{}, ErrRecoveryWorkerUnavailable
	}

	var jobReference struct {
		PlanID string `gorm:"column:plan_id"`
	}
	loaded := coordinator.db.WithContext(ctx).Table((model.BackupAssetRecoveryJob{}).TableName()).
		Select("plan_id").Where("id = ?", claim.JobID).Limit(1).Find(&jobReference)
	if loaded.Error != nil {
		return TargetWritePermit{}, fmt.Errorf("%w: load recovery job plan", ErrRecoveryWorkerUnavailable)
	}
	if loaded.RowsAffected != 1 || !validOpaqueID(jobReference.PlanID) {
		return TargetWritePermit{}, ErrRecoveryWorkerFenceLost
	}

	var mutation TargetMutationPermit
	var sessionBinding recoveryTargetSessionBinding
	var workspaceRequest *CreateOwnedJobDirRequest
	var workspaceRelativeLocator string
	var preWriteDrift bool
	err = coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := coordinator.now().UTC()

		var plan model.BackupAssetRecoveryPlan
		loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ?", jobReference.PlanID).Limit(1).Find(&plan)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || PlanState(plan.State) != PlanStateExecuted {
			return ErrRecoveryWorkerFenceLost
		}
		sessionBinding, err = newRecoveryTargetSessionBinding(plan)
		if err != nil {
			return ErrRecoveryWorkerFenceLost
		}

		var job model.BackupAssetRecoveryJob
		loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ? AND plan_id = ?", claim.JobID, plan.ID).Limit(1).Find(&job)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || job.State != string(JobStateRunning) ||
			job.TransitionRevision != claim.TransitionRevision {
			return ErrRecoveryWorkerFenceLost
		}
		if TargetMode(job.TargetMode) == TargetModeIsolated {
			workspaceRelativeLocator = job.EncryptedWorkspaceRelativeLocator
		}

		var preflight model.BackupAssetRecoveryPreflight
		loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ? AND plan_id = ?", job.PreflightID, plan.ID).Limit(1).Find(&preflight)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 {
			return ErrRecoveryWorkerFenceLost
		}

		var grant model.BackupAssetRecoveryGrant
		loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ? AND plan_id = ?", job.AuthorityGrantID, plan.ID).Limit(1).Find(&grant)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || !validRecoveryJobBinding(plan, job, preflight, grant, now, false) {
			return ErrRecoveryWorkerFenceLost
		}

		sourceErr := coordinator.sourceValidator.RevalidatePlanTx(ctx, tx, plan)

		var source model.RecoveryPointLease
		loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ?", claim.SourceFence.LeaseID).Limit(1).Find(&source)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || !matchesCurrentRecoverySourceFence(source, claim.SourceFence, plan.RecoveryPointID, now) {
			return ErrRecoveryWorkerFenceLost
		}

		var node model.BackupAssetRecoveryNodeLease
		loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ?", claim.NodeLeaseID).Limit(1).Find(&node)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || !matchesCurrentRecoveryNodeFence(node, claim, job, now) {
			return ErrRecoveryWorkerFenceLost
		}

		authorityErr := coordinator.revalidateObservedRecoveryAuthorityTx(
			ctx, tx, plan, preflight, observedAuthority,
		)

		var attempt model.BackupAssetRecoveryAttempt
		loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ? AND job_id = ?", claim.AttemptID, job.ID).Limit(1).Find(&attempt)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || !matchesCurrentRecoveryAttemptFence(attempt, claim, now) ||
			(!attempt.MutationArmed && job.TargetChainRevision != preflight.TargetRevision) {
			return ErrRecoveryWorkerFenceLost
		}
		setMutation := func(permitPathDigest string) error {
			permitExpiry := earliestRecoveryFirstWriteExpiry(
				source.LeaseExpiresAt, source.AbsoluteDeadline, node.LeaseExpiresAt, *attempt.LeaseExpiresAt,
			)
			if !permitExpiry.After(now) {
				return ErrRecoveryWorkerFenceLost
			}
			mutation = TargetMutationPermit{
				SchemaVersion:          1,
				NodeID:                 job.TargetNodeID,
				Purpose:                TargetPurposeWrite,
				RootID:                 job.TargetRootID,
				RootLocatorDigest:      job.RootLocatorDigest,
				TargetPathDigest:       permitPathDigest,
				RootRevision:           plan.RootRevision,
				ExpiresAt:              permitExpiry,
				UseLatchID:             RecoverySchemaUseLatchID,
				JobID:                  job.ID,
				AttemptID:              attempt.ID,
				NodeLeaseID:            node.ID,
				AttemptFence:           attempt.Fence,
				NodeFence:              node.Fence,
				ExpectedTargetRevision: job.TargetChainRevision,
			}
			if TargetMode(job.TargetMode) == TargetModeIsolated &&
				WorkspacePhase(job.WorkspacePhase) != WorkspacePhaseWriting {
				object := TargetObjectRef{
					RootID: job.TargetRootID, RootLocatorDigest: job.RootLocatorDigest,
					TargetPathDigest:       permitPathDigest,
					PrivateRelativeLocator: workspaceRelativeLocator,
				}
				if !object.valid() || !validDigest(job.WorkspaceMarkerBindingDigest) ||
					!validRecoveryWorkerID(job.WorkspaceOwner) || job.WorkspaceFence == 0 {
					return fmt.Errorf("derive owned recovery workspace request: %w", ErrRecoveryWorkerFenceLost)
				}
				request := CreateOwnedJobDirRequest{
					Object: object, MarkerBindingDigest: job.WorkspaceMarkerBindingDigest,
					MarkerCreatorID: job.WorkspaceOwner, MarkerCreatorFence: job.WorkspaceFence,
				}
				workspaceRequest = &request
			}
			return nil
		}

		var checkpointCount int64
		if err := tx.WithContext(ctx).Model(&model.BackupAssetRecoveryCheckpoint{}).
			Where("job_id = ?", job.ID).Count(&checkpointCount).Error; err != nil {
			return err
		}
		if sourceErr == nil && authorityErr == nil && validateLocked != nil {
			sourceErr = validateLocked(ctx, tx)
		}
		if sourceErr != nil {
			if errors.Is(sourceErr, ErrRecoverySourceChanged) && authorityErr == nil &&
				canSupersedeBeforeFirstWrite(plan, attempt, checkpointCount) {
				if err := coordinator.supersedePreWriteDriftTx(ctx, tx, plan, job, attempt, source, node, claim, now); err != nil {
					return err
				}
				preWriteDrift = true
				return nil
			}
			return sourceErr
		}
		if authorityErr != nil {
			if isRecoveryPreWriteAuthorityDrift(authorityErr) &&
				canSupersedeBeforeFirstWrite(plan, attempt, checkpointCount) {
				if err := coordinator.supersedePreWriteDriftTx(ctx, tx, plan, job, attempt, source, node, claim, now); err != nil {
					return err
				}
				preWriteDrift = true
				return nil
			}
			return authorityErr
		}
		if attempt.MutationArmed || checkpointCount != 0 {
			permitPathDigest, err := coordinator.currentFirstWritePermitPathTx(
				ctx, tx, plan, preflight, job, attempt, claim, checkpointCount,
				reconciledOverwriteCheckpointID, now,
			)
			if err != nil {
				return err
			}
			if err := ensureRecoverySchemaUseLatchTx(ctx, tx, now); err != nil {
				return err
			}
			return setMutation(permitPathDigest)
		}

		permitPathDigest := job.PathDigest
		if TargetMode(job.TargetMode) == TargetModeIsolated {
			workspaceLocator := job.EncryptedWorkspaceRelativeLocator
			if !validPreallocatedRecoveryWorkspace(plan, job) {
				return ErrRecoveryWorkerFenceLost
			}
			workspacePathDigest, err := TargetPathDigest(job.TargetRootID, job.RootLocatorDigest, workspaceLocator)
			if err != nil {
				return ErrRecoveryWorkerFenceLost
			}
			workspaceDeadline := now.Add(recoveryWorkspacePlaintextTTL)
			markerBinding := recoveryWorkspaceMarkerBindingDigest(
				ownershipKey, job.ID, job.TargetRootID, plan.RootRevision, workspaceLocator, claim,
			)

			if err := ensureRecoverySchemaUseLatchTx(ctx, tx, now); err != nil {
				return err
			}

			job.WorkspacePhase = string(WorkspacePhaseReserved)
			job.WorkspaceMarkerBindingDigest = markerBinding
			job.WorkspaceOwner = claim.WorkerID
			job.WorkspaceFence = claim.AttemptFence
			job.WorkspaceMarkerValidationAttemptID = ""
			job.WorkspaceMarkerValidationAttemptFence = 0
			job.WorkspaceMarkerValidationNodeFence = 0
			job.PlaintextDeadline = timePointerValue(workspaceDeadline)
			job.UpdatedAt = now
			updated := tx.WithContext(ctx).Model(&job).
				Where("id = ? AND state = ? AND transition_revision = ? AND workspace_phase = ? AND workspace_binding_digest = ?",
					job.ID, JobStateRunning, job.TransitionRevision, WorkspacePhaseNone, job.WorkspaceBindingDigest).
				Select(
					"workspace_phase", "workspace_marker_binding_digest",
					"workspace_owner", "workspace_fence", "plaintext_deadline", "updated_at",
				).Updates(&job)
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return ErrRecoveryWorkerFenceLost
			}

			checkpointID, err := backupasset.NewOpaqueID()
			if err != nil {
				return err
			}
			checkpoint := recoveryWorkspaceReservedCheckpoint(checkpointID, job, claim, now)
			if err := tx.WithContext(ctx).Create(&checkpoint).Error; err != nil {
				return err
			}
			permitPathDigest = workspacePathDigest
		} else {
			if TargetMode(job.TargetMode) != TargetModeInPlace || !validPreallocatedRecoveryWorkspace(plan, job) {
				return ErrRecoveryWorkerFenceLost
			}
			if err := ensureRecoverySchemaUseLatchTx(ctx, tx, now); err != nil {
				return err
			}
		}

		updated := tx.WithContext(ctx).Model(&model.BackupAssetRecoveryAttempt{}).
			Where("id = ? AND job_id = ? AND owner_id = ? AND fence = ? AND state = ? AND mutation_armed = ? AND lease_expires_at > ?",
				attempt.ID, job.ID, claim.WorkerID, claim.AttemptFence, AttemptStateRunning, false, now).
			Updates(map[string]any{"mutation_armed": true, "updated_at": now})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrRecoveryWorkerFenceLost
		}

		return setMutation(permitPathDigest)
	})
	if err != nil {
		if errors.Is(err, ErrRecoveryWorkerFenceLost) || errors.Is(err, ErrRecoverySourceChanged) {
			return TargetWritePermit{}, err
		}
		return TargetWritePermit{}, fmt.Errorf("%w: prepare first recovery write", ErrRecoveryWorkerUnavailable)
	}
	if preWriteDrift {
		coordinator.observeJobOutcome(ctx, claim.JobID, JobStateFailed)
		return TargetWritePermit{}, ErrRecoverySourceChanged
	}

	mutation = issueTargetMutationPermit(mutation, func(now time.Time) error {
		return coordinator.validateFirstWritePermitAt(claim, mutation, now)
	}, sessionBinding)
	permit, err := NewTargetWritePermit(mutation, coordinator.now().UTC())
	if err != nil {
		return TargetWritePermit{}, fmt.Errorf("seal recovery first-write permit: %w", ErrRecoveryWorkerFenceLost)
	}
	if workspaceRequest != nil && coordinator.target != nil {
		if permit.ValidateOwnedJobDirRequestAt(coordinator.now().UTC(), *workspaceRequest) != nil {
			return TargetWritePermit{}, fmt.Errorf("validate owned recovery workspace permit: %w", ErrRecoveryWorkerFenceLost)
		}
		owned, createErr := coordinator.target.CreateOwnedJobDir(ctx, permit, *workspaceRequest)
		if createErr != nil {
			return TargetWritePermit{}, fmt.Errorf("%w: create owned recovery workspace", ErrRecoveryWorkerUnavailable)
		}
		if owned.Object != workspaceRequest.Object ||
			owned.MarkerBindingDigest != workspaceRequest.MarkerBindingDigest ||
			!validOpaqueRevision(owned.TargetRevision) {
			return TargetWritePermit{}, fmt.Errorf("validate owned recovery workspace result: %w", ErrRecoveryWorkerFenceLost)
		}
		if err := coordinator.markWorkspaceMarkerCreated(ctx, claim, *workspaceRequest); err != nil {
			return TargetWritePermit{}, err
		}
	}
	return permit, nil
}

func (coordinator *WorkerCoordinator) markWorkspaceMarkerCreated(
	ctx context.Context,
	claim RecoveryWorkerClaim,
	request CreateOwnedJobDirRequest,
) error {
	if coordinator == nil || coordinator.db == nil || !validRecoveryWorkerClaim(claim) ||
		!request.Object.valid() || !validDigest(request.MarkerBindingDigest) ||
		!validRecoveryWorkerID(request.MarkerCreatorID) || request.MarkerCreatorFence == 0 {
		return ErrInvalidRecoveryWorker
	}
	ctx = recoveryWorkerContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}

	err := coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := coordinator.now().UTC()

		var latch model.BackupAssetRecoveryEvidence
		loaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ?", recoverySchemaUseLatchRowID).Limit(1).Find(&latch)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || !validRecoverySchemaUseLatch(latch) {
			return ErrRecoveryWorkerFenceLost
		}

		var job model.BackupAssetRecoveryJob
		loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ?", claim.JobID).Limit(1).Find(&job)
		if loaded.Error != nil {
			return loaded.Error
		}
		workspacePathDigest, pathErr := TargetPathDigest(
			job.TargetRootID, job.RootLocatorDigest, job.EncryptedWorkspaceRelativeLocator,
		)
		expectedObject := TargetObjectRef{
			RootID: job.TargetRootID, RootLocatorDigest: job.RootLocatorDigest,
			TargetPathDigest: workspacePathDigest, PrivateRelativeLocator: job.EncryptedWorkspaceRelativeLocator,
		}
		if loaded.RowsAffected != 1 || pathErr != nil || job.State != string(JobStateRunning) ||
			job.TransitionRevision != claim.TransitionRevision || TargetMode(job.TargetMode) != TargetModeIsolated ||
			(job.WorkspacePhase != string(WorkspacePhaseReserved) &&
				job.WorkspacePhase != string(WorkspacePhaseMarkerCreated)) ||
			!validDigest(job.WorkspaceBindingDigest) || !validDigest(job.WorkspaceMarkerBindingDigest) ||
			!validRecoveryWorkerID(job.WorkspaceOwner) || job.WorkspaceFence == 0 ||
			job.PlaintextDeadline == nil || !job.PlaintextDeadline.After(now) ||
			request.Object != expectedObject || request.MarkerBindingDigest != job.WorkspaceMarkerBindingDigest ||
			request.MarkerCreatorID != job.WorkspaceOwner || request.MarkerCreatorFence != job.WorkspaceFence {
			return ErrRecoveryWorkerFenceLost
		}

		var attempt model.BackupAssetRecoveryAttempt
		loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ? AND job_id = ?", claim.AttemptID, job.ID).Limit(1).Find(&attempt)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || !attempt.MutationArmed ||
			!matchesCurrentRecoveryAttemptFence(attempt, claim, now) {
			return ErrRecoveryWorkerFenceLost
		}

		var source model.RecoveryPointLease
		loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ?", claim.SourceFence.LeaseID).Limit(1).Find(&source)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 ||
			!matchesCurrentRecoverySourceFence(source, claim.SourceFence, claim.SourceFence.RecoveryPointID, now) {
			return ErrRecoveryWorkerFenceLost
		}

		var node model.BackupAssetRecoveryNodeLease
		loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ?", claim.NodeLeaseID).Limit(1).Find(&node)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || !matchesCurrentRecoveryNodeFence(node, claim, job, now) {
			return ErrRecoveryWorkerFenceLost
		}

		if job.WorkspacePhase == string(WorkspacePhaseMarkerCreated) {
			if !recoveryMarkerValidationMatchesClaim(job, claim) {
				return ErrRecoveryWorkerFenceLost
			}
			return nil
		}
		if !emptyRecoveryMarkerValidation(job) {
			return ErrRecoveryWorkerFenceLost
		}
		updated := tx.WithContext(ctx).Model(&model.BackupAssetRecoveryJob{}).
			Where(`id = ? AND state = ? AND transition_revision = ? AND target_mode = ?
				AND workspace_phase = ? AND workspace_binding_digest = ? AND workspace_marker_binding_digest = ?
				AND workspace_owner = ? AND workspace_fence = ?
				AND workspace_marker_validation_attempt_id = ''
				AND workspace_marker_validation_attempt_fence = 0
				AND workspace_marker_validation_node_fence = 0 AND plaintext_deadline = ?`,
				job.ID, JobStateRunning, job.TransitionRevision, TargetModeIsolated,
				WorkspacePhaseReserved, job.WorkspaceBindingDigest, job.WorkspaceMarkerBindingDigest,
				job.WorkspaceOwner, job.WorkspaceFence, job.PlaintextDeadline).
			Updates(map[string]any{
				"workspace_phase":                           string(WorkspacePhaseMarkerCreated),
				"workspace_marker_validation_attempt_id":    claim.AttemptID,
				"workspace_marker_validation_attempt_fence": claim.AttemptFence,
				"workspace_marker_validation_node_fence":    claim.NodeFence,
				"updated_at":                                now,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrRecoveryWorkerFenceLost
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrRecoveryWorkerFenceLost) {
			return ErrRecoveryWorkerFenceLost
		}
		return fmt.Errorf("%w: persist owned recovery workspace marker", ErrRecoveryWorkerUnavailable)
	}
	return nil
}

func isRecoveryPreWriteAuthorityDrift(err error) bool {
	return errors.Is(err, ErrAuthorizationDenied) || errors.Is(err, ErrRecoverySourceChanged)
}

func validPreallocatedRecoveryWorkspace(
	plan model.BackupAssetRecoveryPlan,
	job model.BackupAssetRecoveryJob,
) bool {
	if job.WorkspacePhase != string(WorkspacePhaseNone) || job.WorkspaceMarkerBindingDigest != "" ||
		job.WorkspaceOwner != "" || job.WorkspaceFence != 0 || !emptyRecoveryMarkerValidation(job) ||
		job.PlaintextDeadline != nil {
		return false
	}
	switch TargetMode(job.TargetMode) {
	case TargetModeIsolated:
		locator := recoveryWorkspaceLocatorDirectory + "/" + job.ID
		return job.EncryptedWorkspaceRelativeLocator == locator &&
			job.WorkspaceBindingDigest == recoveryWorkspaceBindingDigest(plan, job.ID, locator)
	case TargetModeInPlace:
		return job.EncryptedWorkspaceRelativeLocator == "" && job.WorkspaceBindingDigest == ""
	default:
		return false
	}
}

func emptyRecoveryMarkerValidation(job model.BackupAssetRecoveryJob) bool {
	return job.WorkspaceMarkerValidationAttemptID == "" &&
		job.WorkspaceMarkerValidationAttemptFence == 0 &&
		job.WorkspaceMarkerValidationNodeFence == 0
}

func validRecoveryMarkerValidation(job model.BackupAssetRecoveryJob) bool {
	return validOpaqueID(job.WorkspaceMarkerValidationAttemptID) &&
		job.WorkspaceMarkerValidationAttemptFence > 0 &&
		job.WorkspaceMarkerValidationNodeFence > 0
}

func recoveryMarkerValidationMatchesClaim(
	job model.BackupAssetRecoveryJob,
	claim RecoveryWorkerClaim,
) bool {
	return validRecoveryMarkerValidation(job) &&
		job.WorkspaceMarkerValidationAttemptID == claim.AttemptID &&
		job.WorkspaceMarkerValidationAttemptFence == claim.AttemptFence &&
		job.WorkspaceMarkerValidationNodeFence == claim.NodeFence
}

// CancelJob fences an active recovery attempt before recording its terminal
// cancellation disposition. An armed or checkpointed job remains cleanup-only
// because an external target mutation may already be in flight.
func (coordinator *WorkerCoordinator) CancelJob(ctx context.Context, jobID string) error {
	return coordinator.cancelJob(ctx, CancelRecoveryJobRequest{JobID: jobID})
}

// CancelRecoveryJobRequest binds the API actor and opaque expected revision to
// the exact plan/job lock boundary. Zero requester/revision is reserved for
// internal runtime cancellation paths.
type CancelRecoveryJobRequest struct {
	RequesterID      uint
	JobID            string
	ExpectedRevision uint64
}

func (coordinator *WorkerCoordinator) CancelOwnedJob(ctx context.Context, request CancelRecoveryJobRequest) error {
	if request.RequesterID == 0 || request.ExpectedRevision == 0 {
		return ErrInvalidRecoveryWorker
	}
	return coordinator.cancelJob(ctx, request)
}

func (coordinator *WorkerCoordinator) cancelJob(ctx context.Context, request CancelRecoveryJobRequest) error {
	jobID := request.JobID
	if coordinator == nil || coordinator.db == nil || coordinator.sourceLeases == nil || !validOpaqueID(jobID) {
		return ErrInvalidRecoveryWorker
	}
	ctx = recoveryWorkerContext(ctx)
	if ctx.Err() != nil {
		// The terminal fence handoff must not be abandoned with a live write
		// permit solely because its caller has already stopped waiting.
		ctx = context.WithoutCancel(ctx)
	}

	var transitioned bool
	var terminalOutcome JobState
	err := coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := coordinator.now().UTC()
		planID := ""
		if request.RequesterID != 0 {
			var reference struct{ PlanID string }
			loaded := tx.WithContext(ctx).Table((model.BackupAssetRecoveryJob{}).TableName()).
				Select("plan_id").Where("id = ?", jobID).Limit(1).Find(&reference)
			if loaded.Error != nil {
				return loaded.Error
			}
			if loaded.RowsAffected != 1 || !validOpaqueID(reference.PlanID) {
				return ErrRecoveryWorkerObjectNotFound
			}
			var plan struct {
				ID          string
				RequesterID uint
			}
			loaded = tx.WithContext(ctx).Table((model.BackupAssetRecoveryPlan{}).TableName()).
				Select("id, requester_id").Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
				Where("id = ?", reference.PlanID).Limit(1).Find(&plan)
			if loaded.Error != nil {
				return loaded.Error
			}
			if loaded.RowsAffected != 1 || plan.RequesterID != request.RequesterID {
				return ErrRecoveryWorkerObjectNotFound
			}
			planID = plan.ID
		}
		var job model.BackupAssetRecoveryJob
		jobQuery := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ?", jobID).Limit(1).Find(&job)
		loaded := jobQuery
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 {
			return ErrRecoveryWorkerFenceLost
		}
		if request.RequesterID != 0 && (job.PlanID != planID || job.TransitionRevision != request.ExpectedRevision) {
			return ErrRecoveryWorkerFenceLost
		}
		if job.State == string(JobStateCanceled) ||
			(job.State == string(JobStateNeedsAttention) &&
				job.FailureCategory == recoveryCancellationAfterMutationArmFailureCategory) {
			return nil
		}
		if (job.State != string(JobStateQueued) && job.State != string(JobStateRunning) &&
			job.State != string(JobStateVerifying) && job.State != string(JobStateCancelRequested)) || job.TransitionRevision == 0 {
			return ErrRecoveryWorkerFenceLost
		}

		var attempt model.BackupAssetRecoveryAttempt
		loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("job_id = ? AND state IN ?", job.ID,
				[]string{string(AttemptStateClaimed), string(AttemptStateRunning)}).
			Order("created_at DESC, id DESC").Limit(1).Find(&attempt)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || attempt.Fence == 0 {
			return ErrRecoveryWorkerFenceLost
		}

		var source model.RecoveryPointLease
		loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("holder_type = ? AND owner_id = ? AND attempt_id = ? AND status = ?",
				backupasset.LeaseHolderRecoveryJob, job.ID, attempt.ID, backupasset.LeaseActive).
			Limit(1).Find(&source)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 {
			return ErrRecoveryWorkerFenceLost
		}
		sourceCurrent := now.Before(source.LeaseExpiresAt.UTC()) && now.Before(source.AbsoluteDeadline.UTC())

		var node model.BackupAssetRecoveryNodeLease
		loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("job_id = ? AND attempt_id = ? AND state = ?", job.ID, attempt.ID, "active").Limit(1).Find(&node)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || node.OwnerID != attempt.OwnerID || node.Fence != attempt.Fence {
			return ErrRecoveryWorkerFenceLost
		}

		var checkpointCount int64
		if err := tx.WithContext(ctx).Model(&model.BackupAssetRecoveryCheckpoint{}).
			Where("job_id = ?", job.ID).Count(&checkpointCount).Error; err != nil {
			return err
		}
		terminalState := JobStateCanceled
		failureCategory := ""
		workspacePhase := job.WorkspacePhase
		if attempt.MutationArmed || checkpointCount > 0 {
			terminalState = JobStateNeedsAttention
			failureCategory = recoveryCancellationAfterMutationArmFailureCategory
			if WorkspacePhase(workspacePhase) != WorkspacePhaseNone {
				workspacePhase = string(WorkspacePhaseCleanupDue)
			}
		}
		updated := tx.WithContext(ctx).Model(&model.BackupAssetRecoveryJob{}).
			Where("id = ? AND state = ? AND transition_revision = ? AND workspace_phase = ?",
				job.ID, job.State, job.TransitionRevision, job.WorkspacePhase).
			Updates(map[string]any{
				"state": string(JobStateCancelRequested), "transition_revision": job.TransitionRevision + 1,
				"updated_at": now,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrRecoveryWorkerFenceLost
		}
		updated = tx.WithContext(ctx).Model(&model.BackupAssetRecoveryAttempt{}).
			Where("id = ? AND job_id = ? AND owner_id = ? AND fence = ? AND state = ?",
				attempt.ID, job.ID, attempt.OwnerID, attempt.Fence, attempt.State).
			Updates(map[string]any{"state": string(AttemptStateCanceled), "closed_at": now, "updated_at": now})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrRecoveryWorkerFenceLost
		}
		if sourceCurrent {
			if err := coordinator.sourceLeases.ReleaseTx(ctx, tx, recoverySourceFence(source)); err != nil {
				return recoveryWorkerSourceError(err)
			}
		} else {
			updated = tx.WithContext(ctx).Model(&model.RecoveryPointLease{}).
				Where(`id = ? AND recovery_point_id = ? AND holder_type = ? AND owner_id = ? AND attempt_id = ? AND fence_token = ? AND status = ?`,
					source.ID, source.RecoveryPointID, source.HolderType, source.OwnerID, source.AttemptID,
					source.FenceToken, backupasset.LeaseActive).
				Updates(map[string]any{"status": backupasset.LeaseReleased, "released_at": now, "updated_at": now})
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return ErrRecoveryWorkerFenceLost
			}
		}
		updated = tx.WithContext(ctx).Model(&model.BackupAssetRecoveryNodeLease{}).
			Where("id = ? AND job_id = ? AND attempt_id = ? AND owner_id = ? AND fence = ? AND state = ?",
				node.ID, job.ID, attempt.ID, attempt.OwnerID, attempt.Fence, "active").
			Updates(map[string]any{"state": "released", "released_at": now, "updated_at": now})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrRecoveryWorkerFenceLost
		}
		updated = tx.WithContext(ctx).Model(&model.BackupAssetRecoveryJob{}).
			Where("id = ? AND state = ? AND transition_revision = ? AND workspace_phase = ?",
				job.ID, JobStateCancelRequested, job.TransitionRevision+1, job.WorkspacePhase).
			Updates(map[string]any{
				"state": string(terminalState), "failure_category": failureCategory,
				"transition_revision": job.TransitionRevision + 2, "workspace_phase": workspacePhase,
				"updated_at": now,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrRecoveryWorkerFenceLost
		}
		transitioned = true
		terminalOutcome = terminalState
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrRecoveryWorkerObjectNotFound) || errors.Is(err, ErrRecoveryWorkerFenceLost) ||
			errors.Is(err, ErrRecoverySourceChanged) {
			return err
		}
		return fmt.Errorf("%w: cancel recovery job", ErrRecoveryWorkerUnavailable)
	}
	if transitioned {
		coordinator.observeJobOutcome(ctx, jobID, terminalOutcome)
		coordinator.writeCancelAudit(ctx, request.RequesterID, jobID, terminalOutcome)
	}
	return nil
}

func (coordinator *WorkerCoordinator) writeCancelAudit(
	ctx context.Context,
	requesterID uint,
	jobID string,
	terminalOutcome JobState,
) {
	if coordinator == nil || coordinator.audit == nil || requesterID == 0 {
		return
	}
	auditCtx, cancel := context.WithTimeout(
		context.WithoutCancel(recoveryWorkerContext(ctx)), authorizationAuditTimeout,
	)
	defer cancel()
	_, _ = coordinator.audit.Write(auditCtx, backupasset.AuditEventInput{
		Actor:         backupasset.AuditActor{UserID: requesterID},
		Action:        backupasset.AuditActionRecoveryCancel,
		RecoveryJobID: jobID,
		Fields: map[backupasset.AuditField]any{
			backupasset.AuditFieldStage:  "job",
			backupasset.AuditFieldStatus: string(terminalOutcome),
		},
	})
}

type interruptedOperationHandoff struct {
	plan                    model.BackupAssetRecoveryPlan
	preflight               model.BackupAssetRecoveryPreflight
	job                     model.BackupAssetRecoveryJob
	item                    model.BackupAssetRecoveryJobItem
	operation               RecoveryOperation
	checkpointOperations    []ordinaryCheckpointOperation
	object                  TargetObjectRef
	expectation             TargetVerifyExpectation
	targetSessionBinding    recoveryTargetSessionBinding
	operationDigest         string
	durableDigest           string
	overwriteArtifacts      recoveryOverwriteArtifactBinding
	deleteAuthorityConsumed bool
	reconcileConsumedDelete bool
}

func newRecoveryTargetVerifyPermit(
	handoff interruptedOperationHandoff,
	expiresAt time.Time,
	now time.Time,
) (TargetVerifyPermit, error) {
	binding := handoff.targetSessionBinding
	mode := TargetMode(handoff.job.TargetMode)
	if !binding.valid() || mode.Validate() != nil || binding.PlanID != handoff.plan.ID ||
		binding.PlanBindingDigest != handoff.plan.BindingDigest || handoff.job.PlanID != handoff.plan.ID ||
		handoff.job.PlanBindingDigest != handoff.plan.BindingDigest || binding.NodeID != handoff.plan.TargetNodeID ||
		binding.NodeID != handoff.job.TargetNodeID || binding.NodeRevision != handoff.plan.TargetBaseRevision ||
		binding.CredentialRevision != handoff.plan.CredentialScopeRevision ||
		binding.RootID != handoff.plan.TargetRootID || binding.RootID != handoff.job.TargetRootID ||
		binding.RootID != handoff.object.RootID || binding.RootLocator != handoff.plan.EncryptedTargetRootLocator ||
		binding.RootLocatorDigest != handoff.plan.RootLocatorDigest ||
		binding.RootLocatorDigest != handoff.job.RootLocatorDigest ||
		binding.RootLocatorDigest != handoff.object.RootLocatorDigest ||
		binding.RootRevision != handoff.plan.RootRevision || handoff.plan.TargetMode != handoff.job.TargetMode ||
		handoff.object.TargetPathDigest != handoff.item.TargetObjectDigest ||
		handoff.item.OperationKind != string(handoff.operation.Kind) ||
		handoff.item.ExpectedPriorKind != string(handoff.operation.ExpectedPrior.Kind) ||
		handoff.item.ExpectedPriorDigest != handoff.operation.ExpectedPrior.Digest ||
		handoff.operationDigest != recoveryJobItemOperationDigest(handoff.item) {
		return TargetVerifyPermit{}, ErrRecoveryWorkerFenceLost
	}
	raw := TargetObservationPermit{
		SchemaVersion: 1, NodeID: binding.NodeID, Purpose: TargetPurposeVerify,
		RootID: handoff.object.RootID, RootLocatorDigest: handoff.object.RootLocatorDigest,
		TargetPathDigest: handoff.object.TargetPathDigest, RootRevision: binding.RootRevision,
		ExpiresAt: expiresAt,
	}
	permit, err := NewTargetVerifyPermit(
		issueTargetVerifyPermit(
			raw, binding, handoff.job.ID, mode,
			handoff.operation.Kind, handoff.operation.ExpectedPrior,
		), now,
	)
	if err != nil || permit.ValidateObjectAt(now, handoff.object) != nil {
		return TargetVerifyPermit{}, ErrRecoveryWorkerFenceLost
	}
	return permit, nil
}

type ordinaryCheckpointOperation struct {
	itemID    string
	digest    string
	kind      RecoveryOperationKind
	completed bool
}

// AdoptInterruptedOperation derives every target fact from the locked durable
// aggregate. Target I/O runs only after the load transaction closes, and a
// second locked load must reproduce the same handoff before projection.
func (coordinator *WorkerCoordinator) AdoptInterruptedOperation(
	ctx context.Context,
	claim RecoveryWorkerClaim,
	jobItemID string,
) (model.BackupAssetRecoveryCheckpoint, error) {
	if coordinator == nil || coordinator.db == nil || coordinator.sourceValidator == nil || coordinator.target == nil ||
		coordinator.sourceResolver == nil ||
		coordinator.sourceLeases == nil || coordinator.liveRevalidator == nil || coordinator.workspaceKeys == nil ||
		!validRecoveryWorkerClaim(claim) || !validOpaqueID(jobItemID) {
		return model.BackupAssetRecoveryCheckpoint{}, ErrInvalidRecoveryWorker
	}
	ctx = recoveryWorkerContext(ctx)
	if err := ctx.Err(); err != nil {
		return model.BackupAssetRecoveryCheckpoint{}, err
	}
	initial, err := coordinator.loadInterruptedOperationHandoff(ctx, claim, jobItemID)
	if err != nil {
		return model.BackupAssetRecoveryCheckpoint{}, publicInterruptedOperationError(err)
	}
	observedAuthority, err := coordinator.observeRecoveryAuthority(ctx, initial.plan, initial.preflight)
	if err != nil {
		return model.BackupAssetRecoveryCheckpoint{}, publicInterruptedOperationError(err)
	}
	sourceOutcome := coordinator.revalidateAdoptionSource(ctx, initial.plan)
	now := coordinator.now().UTC()
	permit, err := newRecoveryTargetVerifyPermit(initial, claim.LeaseExpiresAt, now)
	if err != nil {
		return model.BackupAssetRecoveryCheckpoint{}, ErrRecoveryWorkerFenceLost
	}
	observed, verifyErr := coordinator.target.Verify(
		ctx, permit, initial.object, cloneTargetVerifyExpectation(initial.expectation),
	)
	result := ordinaryOperationResult{}
	if initial.operation.Kind == RecoveryOperationCreate ||
		initial.operation.Kind == RecoveryOperationOverwrite ||
		initial.operation.Kind == RecoveryOperationDelete {
		result.adoptionNoWrite = true
	}
	if verifyErr != nil {
		result.observationCallFailed = true
		result.unresolvedCategory = UnresolvedOperationObservationInvalid
	} else {
		result.observation = observed
		result.observationReturned = true
		switch {
		case observed.Validate() != nil:
			result.unresolvedCategory = UnresolvedOperationObservationInvalid
		case observed.ValidateAgainst(initial.expectation) != nil:
			result.unresolvedCategory = UnresolvedOperationVerificationMismatch
		}
	}
	if result.unresolvedCategory.Valid() {
		var transitioned bool
		err = coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			current, loadErr := coordinator.loadInterruptedOperationHandoffTx(
				ctx, tx, claim, jobItemID, coordinator.now().UTC(), false, false,
			)
			if loadErr != nil {
				return loadErr
			}
			if authorityErr := coordinator.revalidateObservedRecoveryAuthorityTx(
				ctx, tx, current.plan, current.preflight, observedAuthority,
			); authorityErr != nil {
				return authorityErr
			}
			if current.durableDigest != initial.durableDigest {
				return ErrRecoveryWorkerFenceLost
			}
			lockedCoordinator := coordinator.withTransactionDB(tx)
			_, projectErr := lockedCoordinator.projectPendingOperationUnresolvedOwned(
				ctx, claim, current, current.job.TargetChainRevision, result,
				sourceOutcome, coordinator.now().UTC(), observedAuthority, false,
			)
			transitioned = projectErr == nil
			return projectErr
		})
		if err != nil {
			return model.BackupAssetRecoveryCheckpoint{}, publicInterruptedOperationError(err)
		}
		if transitioned {
			coordinator.observeJobOutcome(ctx, claim.JobID, JobStateNeedsAttention)
		}
		return model.BackupAssetRecoveryCheckpoint{}, ErrInvalidTargetVerification
	}

	var checkpoint model.BackupAssetRecoveryCheckpoint
	var terminalState JobState
	err = coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, loadErr := coordinator.loadInterruptedOperationHandoffTx(
			ctx, tx, claim, jobItemID, coordinator.now().UTC(), false, false,
		)
		if loadErr != nil {
			return loadErr
		}
		if authorityErr := coordinator.revalidateObservedRecoveryAuthorityTx(
			ctx, tx, current.plan, current.preflight, observedAuthority,
		); authorityErr != nil {
			return authorityErr
		}
		if current.durableDigest != initial.durableDigest || observed.ValidateAgainst(current.expectation) != nil {
			return ErrRecoveryWorkerFenceLost
		}
		var projectErr error
		checkpoint, projectErr = coordinator.projectInterruptedOperationTx(
			ctx, tx, claim, current, observed, sourceOutcome, coordinator.now().UTC(),
			&terminalState,
		)
		return projectErr
	})
	if err != nil {
		return model.BackupAssetRecoveryCheckpoint{}, publicInterruptedOperationError(err)
	}
	if terminalState.Valid() {
		coordinator.observeJobOutcome(ctx, claim.JobID, terminalState)
	}
	if sourceOutcome != SourceRevalidationMatched {
		return checkpoint, sourceRevalidationOutcomeError(sourceOutcome)
	}
	return checkpoint, nil
}

func (coordinator *WorkerCoordinator) revalidateAdoptionSource(
	ctx context.Context,
	plan model.BackupAssetRecoveryPlan,
) SourceRevalidationOutcome {
	if coordinator == nil || coordinator.sourceResolver == nil {
		return SourceRevalidationFailed
	}
	ref, err := NewRsyncRestoreSourceRef(plan)
	if err != nil {
		return SourceRevalidationFailed
	}
	source, resolveErr := coordinator.sourceResolver.ResolveRsyncRestoreSource(ctx, ref)
	if source == nil {
		if classifySourceRevalidationOutcome(resolveErr) == SourceRevalidationDrifted {
			return SourceRevalidationDrifted
		}
		return SourceRevalidationFailed
	}
	revalidateErr := source.Revalidate(ctx)
	closeErr := source.Close()
	outcome := SourceRevalidationMatched
	for _, candidate := range []error{resolveErr, revalidateErr, closeErr} {
		if candidate == nil {
			continue
		}
		if classifySourceRevalidationOutcome(candidate) == SourceRevalidationDrifted {
			outcome = SourceRevalidationDrifted
		} else if outcome == SourceRevalidationMatched {
			outcome = SourceRevalidationFailed
		}
	}
	return outcome
}

func sourceRevalidationOutcomeError(outcome SourceRevalidationOutcome) error {
	if outcome == SourceRevalidationDrifted {
		return ErrRecoverySourceChanged
	}
	return ErrRecoveryWorkerUnavailable
}

func (coordinator *WorkerCoordinator) loadInterruptedOperationHandoff(
	ctx context.Context,
	claim RecoveryWorkerClaim,
	jobItemID string,
) (interruptedOperationHandoff, error) {
	var handoff interruptedOperationHandoff
	err := coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var loadErr error
		handoff, loadErr = coordinator.loadInterruptedOperationHandoffTx(
			ctx, tx, claim, jobItemID, coordinator.now().UTC(), false, false,
		)
		return loadErr
	})
	return handoff, err
}

func (coordinator *WorkerCoordinator) loadOrdinaryOperationHandoff(
	ctx context.Context,
	claim RecoveryWorkerClaim,
	jobItemID string,
) (interruptedOperationHandoff, error) {
	var handoff interruptedOperationHandoff
	err := coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var loadErr error
		handoff, loadErr = coordinator.loadInterruptedOperationHandoffTx(
			ctx, tx, claim, jobItemID, coordinator.now().UTC(), true, false,
		)
		return loadErr
	})
	return handoff, err
}

func (coordinator *WorkerCoordinator) loadCompletedOrdinaryOverwriteHandoff(
	ctx context.Context,
	claim RecoveryWorkerClaim,
	jobItemID string,
) (interruptedOperationHandoff, error) {
	var handoff interruptedOperationHandoff
	err := coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var loadErr error
		handoff, loadErr = coordinator.loadInterruptedOperationHandoffTx(
			ctx, tx, claim, jobItemID, coordinator.now().UTC(), true, true,
		)
		return loadErr
	})
	return handoff, err
}

func (coordinator *WorkerCoordinator) loadInterruptedOperationHandoffTx(
	ctx context.Context,
	tx *gorm.DB,
	claim RecoveryWorkerClaim,
	jobItemID string,
	now time.Time,
	ordinaryExecution bool,
	completedOverwrite bool,
) (interruptedOperationHandoff, error) {
	if tx == nil || !validRecoveryWorkerClaim(claim) || !validOpaqueID(jobItemID) || now.IsZero() {
		return interruptedOperationHandoff{}, ErrRecoveryWorkerFenceLost
	}

	var jobReference struct {
		PlanID string `gorm:"column:plan_id"`
	}
	loaded := tx.WithContext(ctx).Table((model.BackupAssetRecoveryJob{}).TableName()).
		Select("plan_id").Where("id = ?", claim.JobID).Limit(1).Find(&jobReference)
	if loaded.Error != nil {
		return interruptedOperationHandoff{}, loaded.Error
	}
	if loaded.RowsAffected != 1 || !validOpaqueID(jobReference.PlanID) {
		return interruptedOperationHandoff{}, ErrRecoveryWorkerFenceLost
	}

	var plan model.BackupAssetRecoveryPlan
	loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ?", jobReference.PlanID).Limit(1).Find(&plan)
	if loaded.Error != nil {
		return interruptedOperationHandoff{}, loaded.Error
	}
	if loaded.RowsAffected != 1 || PlanState(plan.State) != PlanStateExecuted {
		return interruptedOperationHandoff{}, ErrRecoveryWorkerFenceLost
	}
	var job model.BackupAssetRecoveryJob
	loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ? AND plan_id = ?", claim.JobID, plan.ID).Limit(1).Find(&job)
	if loaded.Error != nil {
		return interruptedOperationHandoff{}, loaded.Error
	}
	if loaded.RowsAffected != 1 || job.PlanID != jobReference.PlanID ||
		job.State != string(JobStateRunning) || job.TransitionRevision != claim.TransitionRevision {
		return interruptedOperationHandoff{}, ErrRecoveryWorkerFenceLost
	}
	var preflight model.BackupAssetRecoveryPreflight
	loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ? AND plan_id = ?", job.PreflightID, plan.ID).Limit(1).Find(&preflight)
	if loaded.Error != nil {
		return interruptedOperationHandoff{}, loaded.Error
	}
	var grant model.BackupAssetRecoveryGrant
	loadedGrant := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ? AND plan_id = ?", job.AuthorityGrantID, plan.ID).Limit(1).Find(&grant)
	if loadedGrant.Error != nil {
		return interruptedOperationHandoff{}, loadedGrant.Error
	}
	if loaded.RowsAffected != 1 || loadedGrant.RowsAffected != 1 ||
		!validRecoveryJobBinding(plan, job, preflight, grant, now, false) {
		return interruptedOperationHandoff{}, ErrRecoveryWorkerFenceLost
	}
	if err := coordinator.sourceValidator.RevalidatePlanTx(ctx, tx, plan); err != nil {
		return interruptedOperationHandoff{}, err
	}
	var source model.RecoveryPointLease
	loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ?", claim.SourceFence.LeaseID).Limit(1).Find(&source)
	if loaded.Error != nil {
		return interruptedOperationHandoff{}, loaded.Error
	}
	if loaded.RowsAffected != 1 || !matchesCurrentRecoverySourceFence(source, claim.SourceFence, plan.RecoveryPointID, now) {
		return interruptedOperationHandoff{}, ErrRecoveryWorkerFenceLost
	}
	var node model.BackupAssetRecoveryNodeLease
	loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ?", claim.NodeLeaseID).Limit(1).Find(&node)
	if loaded.Error != nil {
		return interruptedOperationHandoff{}, loaded.Error
	}
	if loaded.RowsAffected != 1 || !matchesCurrentRecoveryNodeFence(node, claim, job, now) {
		return interruptedOperationHandoff{}, ErrRecoveryWorkerFenceLost
	}
	var attempt model.BackupAssetRecoveryAttempt
	loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ? AND job_id = ?", claim.AttemptID, job.ID).Limit(1).Find(&attempt)
	if loaded.Error != nil {
		return interruptedOperationHandoff{}, loaded.Error
	}
	if loaded.RowsAffected != 1 || !attempt.MutationArmed || !matchesCurrentRecoveryAttemptFence(attempt, claim, now) {
		return interruptedOperationHandoff{}, ErrRecoveryWorkerFenceLost
	}

	operationRows, err := rebuildExecuteOperationRows(plan, preflight, tx.WithContext(ctx))
	if err != nil {
		return interruptedOperationHandoff{}, ErrRecoveryWorkerFenceLost
	}
	var items []model.BackupAssetRecoveryJobItem
	loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("job_id = ? AND plan_id = ?", job.ID, plan.ID).Order("ordinal ASC").Find(&items)
	if loaded.Error != nil {
		return interruptedOperationHandoff{}, loaded.Error
	}
	if len(items) == 0 || len(items) != len(operationRows) {
		return interruptedOperationHandoff{}, ErrRecoveryWorkerFenceLost
	}

	materials := make(map[int]backupasset.DomainKeyMaterial)
	defer func() {
		for version := range materials {
			material := materials[version]
			clear(material.Key)
			materials[version] = material
		}
	}()
	itemDigests := make([]string, len(items))
	var selected model.BackupAssetRecoveryJobItem
	var selectedLocator string
	var selectedOperation RecoveryOperation
	for index := range items {
		item := items[index]
		operationRow := operationRows[index]
		locator, itemDigest, validateErr := coordinator.validateDurableRecoveryItem(
			ctx, plan, job, item, operationRow, index, materials,
		)
		if validateErr != nil {
			return interruptedOperationHandoff{}, validateErr
		}
		itemDigests[index] = itemDigest
		if item.ID == jobItemID {
			selected = item
			selectedLocator = locator
			selectedOperation = operationRow.operation
		}
	}
	validSelectedDisposition := selected.Outcome == "" && selected.FailureCategory == ""
	if completedOverwrite {
		validSelectedDisposition = selectedOperation.Kind == RecoveryOperationOverwrite &&
			TargetMode(job.TargetMode) == TargetModeInPlace && selected.Outcome == "succeeded" &&
			selected.FailureCategory == "" && selected.BytesWritten == selected.ExpectedPostBytes &&
			selected.VerifiedSize == selected.ExpectedPostBytes &&
			selected.VerifiedDigest == selected.ExpectedPostIdentityDigest
	}
	if selected.ID == "" || !validSelectedDisposition {
		return interruptedOperationHandoff{}, ErrRecoveryWorkerFenceLost
	}
	var checkpointOperations []ordinaryCheckpointOperation
	switch TargetMode(job.TargetMode) {
	case TargetModeIsolated:
		checkpointOperations, err = newIsolatedOrdinaryCheckpointOperations(plan, job, items, operationRows)
		if err != nil {
			return interruptedOperationHandoff{}, err
		}
	case TargetModeInPlace:
		checkpointOperations, err = newInPlaceOrdinaryCheckpointOperations(plan, job, items, operationRows)
		if err != nil {
			return interruptedOperationHandoff{}, err
		}
	default:
		return interruptedOperationHandoff{}, ErrRecoveryWorkerFenceLost
	}

	var checkpoints []model.BackupAssetRecoveryCheckpoint
	loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("job_id = ?", job.ID).Order("sequence ASC").Find(&checkpoints)
	if loaded.Error != nil {
		return interruptedOperationHandoff{}, loaded.Error
	}
	checkpointDigest, err := validateInterruptedOperationWorkspace(
		plan, job, claim, checkpoints, checkpointOperations, materials[selected.TargetLocatorKeyVersion],
		selectedOperation, selected.ID, ordinaryExecution, completedOverwrite, now,
	)
	if err != nil {
		return interruptedOperationHandoff{}, err
	}
	deleteAuthorityConsumed := false
	reconcileConsumedDelete := false
	if selectedOperation.Kind == RecoveryOperationDelete {
		required, consumed, found := ordinaryConsumedDeleteCheckpoints(checkpoints)
		if found {
			if err := validateConsumedOrdinaryDeleteGrantTx(
				ctx, tx, plan, job, required, consumed,
			); err != nil {
				return interruptedOperationHandoff{}, err
			}
			deleteAuthorityConsumed = true
			reconcileConsumedDelete = true
		} else if !ordinaryExecution {
			return interruptedOperationHandoff{}, ErrRecoveryWorkerFenceLost
		}
	}

	finalLocator := selectedLocator
	if TargetMode(job.TargetMode) == TargetModeIsolated {
		finalLocator = job.EncryptedWorkspaceRelativeLocator + "/" + selectedLocator
	}
	finalDigest, err := TargetObjectDigest(job.TargetRootID, job.RootLocatorDigest, finalLocator)
	if err != nil || finalDigest != selected.TargetObjectDigest {
		return interruptedOperationHandoff{}, ErrRecoveryWorkerFenceLost
	}
	object := TargetObjectRef{
		RootID: job.TargetRootID, RootLocatorDigest: job.RootLocatorDigest,
		TargetPathDigest: selected.TargetObjectDigest, PrivateRelativeLocator: finalLocator,
	}
	if !object.valid() {
		return interruptedOperationHandoff{}, ErrRecoveryWorkerFenceLost
	}
	expectation, err := recoveryOperationVerifyExpectation(selectedOperation, selected)
	if err != nil {
		return interruptedOperationHandoff{}, err
	}
	targetSessionBinding, err := newRecoveryTargetSessionBinding(plan)
	if err != nil {
		return interruptedOperationHandoff{}, ErrRecoveryWorkerFenceLost
	}
	operationDigest := recoveryJobItemOperationDigest(selected)
	var overwriteArtifacts recoveryOverwriteArtifactBinding
	if selectedOperation.Kind == RecoveryOperationOverwrite && TargetMode(job.TargetMode) == TargetModeInPlace {
		material, found := materials[selected.TargetLocatorKeyVersion]
		if !found {
			return interruptedOperationHandoff{}, ErrRecoveryWorkerFenceLost
		}
		overwriteArtifacts, err = newRecoveryOverwriteArtifactBinding(material, recoveryOverwriteArtifactBindingInput{
			keyVersion: selected.TargetLocatorKeyVersion,
			planID:     plan.ID, planBindingDigest: plan.BindingDigest,
			jobID: job.ID, jobItemID: selected.ID, operationDigest: operationDigest,
			targetMode: TargetMode(job.TargetMode), nodeID: job.TargetNodeID,
			rootID: job.TargetRootID, rootLocatorDigest: job.RootLocatorDigest,
			rootRevision: plan.RootRevision, object: object,
			expectedPrior: ExpectedTargetIdentity{
				Kind:   ExpectedTargetIdentityKind(selected.ExpectedPriorKind),
				Digest: selected.ExpectedPriorDigest,
			},
			expectedPriorBytes: selected.ExpectedPriorBytes,
			expectedPostDigest: selected.ExpectedPostIdentityDigest,
			expectedPostBytes:  selected.ExpectedPostBytes,
		})
		if err != nil {
			return interruptedOperationHandoff{}, ErrRecoveryWorkerFenceLost
		}
	}
	itemSetDigest := framedDigest("xirang/recovery/interrupted-item-set/v1", itemDigests...)
	durableDigest := interruptedOperationDurableDigest(
		claim, plan, preflight, grant, job, selected, source, node, attempt,
		itemSetDigest, checkpointDigest, operationDigest,
		targetSessionBinding.bindingDigest, object, expectation,
	)
	return interruptedOperationHandoff{
		plan: plan, preflight: preflight, job: job, item: selected, operation: selectedOperation, object: object,
		checkpointOperations: checkpointOperations, expectation: expectation,
		targetSessionBinding: targetSessionBinding,
		operationDigest:      operationDigest, durableDigest: durableDigest,
		overwriteArtifacts:      overwriteArtifacts,
		deleteAuthorityConsumed: deleteAuthorityConsumed, reconcileConsumedDelete: reconcileConsumedDelete,
	}, nil
}

func newIsolatedOrdinaryCheckpointOperations(
	plan model.BackupAssetRecoveryPlan,
	job model.BackupAssetRecoveryJob,
	items []model.BackupAssetRecoveryJobItem,
	operationRows []executeOperationRow,
) ([]ordinaryCheckpointOperation, error) {
	if TargetMode(plan.TargetMode) != TargetModeIsolated || TargetMode(job.TargetMode) != TargetModeIsolated ||
		plan.ID != job.PlanID || len(items) == 0 || len(items) != len(operationRows) {
		return nil, ErrRecoveryWorkerFenceLost
	}
	operations := make([]ordinaryCheckpointOperation, 0, len(items))
	seenDigests := make(map[string]struct{}, len(items))
	for index := range items {
		item := items[index]
		operationRow := operationRows[index]
		if !ordinarySourceJobItemMatchesOperation(plan, job, item, operationRow, index) ||
			item.FailureCategory != "" {
			return nil, ErrRecoveryWorkerFenceLost
		}
		kind := operationRow.operation.Kind
		completed := false
		switch kind {
		case RecoveryOperationCreate, RecoveryOperationOverwrite:
			if item.Outcome != "" && item.Outcome != "succeeded" {
				return nil, ErrRecoveryWorkerFenceLost
			}
			completed = item.Outcome == "succeeded"
		case RecoveryOperationSkip:
			if item.Outcome != "" && item.Outcome != "skipped" {
				return nil, ErrRecoveryWorkerFenceLost
			}
			completed = item.Outcome == "skipped"
		default:
			return nil, ErrRecoveryWorkerFenceLost
		}
		digest := recoveryJobItemOperationDigest(item)
		if !validDigest(digest) {
			return nil, ErrRecoveryWorkerFenceLost
		}
		if _, duplicate := seenDigests[digest]; duplicate {
			return nil, ErrRecoveryWorkerFenceLost
		}
		seenDigests[digest] = struct{}{}
		operations = append(operations, ordinaryCheckpointOperation{
			itemID: item.ID, digest: digest, kind: kind, completed: completed,
		})
	}
	return operations, nil
}

func newInPlaceOrdinaryCheckpointOperations(
	plan model.BackupAssetRecoveryPlan,
	job model.BackupAssetRecoveryJob,
	items []model.BackupAssetRecoveryJobItem,
	operationRows []executeOperationRow,
) ([]ordinaryCheckpointOperation, error) {
	if TargetMode(plan.TargetMode) != TargetModeInPlace || TargetMode(job.TargetMode) != TargetModeInPlace ||
		plan.ID != job.PlanID || len(items) == 0 || len(items) != len(operationRows) {
		return nil, ErrRecoveryWorkerFenceLost
	}
	operations := make([]ordinaryCheckpointOperation, 0, len(items))
	seenDigests := make(map[string]struct{}, len(items))
	pendingSeen := false
	deleteSeen := false
	for index := range items {
		item := items[index]
		operationRow := operationRows[index]
		if !ordinarySourceJobItemMatchesOperation(plan, job, item, operationRow, index) ||
			item.FailureCategory != "" {
			return nil, ErrRecoveryWorkerFenceLost
		}
		kind := operationRow.operation.Kind
		if deleteSeen && kind != RecoveryOperationDelete {
			return nil, ErrRecoveryWorkerFenceLost
		}
		if kind == RecoveryOperationDelete {
			deleteSeen = true
		}
		completed := false
		switch kind {
		case RecoveryOperationCreate, RecoveryOperationOverwrite, RecoveryOperationDelete:
			if item.Outcome != "" && item.Outcome != "succeeded" {
				return nil, ErrRecoveryWorkerFenceLost
			}
			completed = item.Outcome == "succeeded"
		case RecoveryOperationSkip:
			if item.Outcome != "" && item.Outcome != "skipped" {
				return nil, ErrRecoveryWorkerFenceLost
			}
			completed = item.Outcome == "skipped"
		default:
			return nil, ErrRecoveryWorkerFenceLost
		}
		if completed && pendingSeen {
			return nil, ErrRecoveryWorkerFenceLost
		}
		if !completed {
			pendingSeen = true
		}
		digest := recoveryJobItemOperationDigest(item)
		if !validDigest(digest) {
			return nil, ErrRecoveryWorkerFenceLost
		}
		if _, duplicate := seenDigests[digest]; duplicate {
			return nil, ErrRecoveryWorkerFenceLost
		}
		seenDigests[digest] = struct{}{}
		operations = append(operations, ordinaryCheckpointOperation{
			itemID: item.ID, digest: digest, kind: kind, completed: completed,
		})
	}
	return operations, nil
}

func loadInPlaceOrdinaryCheckpointOperationsTx(
	ctx context.Context,
	tx *gorm.DB,
	plan model.BackupAssetRecoveryPlan,
	preflight model.BackupAssetRecoveryPreflight,
	job model.BackupAssetRecoveryJob,
) ([]ordinaryCheckpointOperation, error) {
	operationRows, err := rebuildExecuteOperationRows(plan, preflight, tx.WithContext(ctx))
	if err != nil {
		return nil, ErrRecoveryWorkerFenceLost
	}
	var items []model.BackupAssetRecoveryJobItem
	loaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("job_id = ? AND plan_id = ?", job.ID, plan.ID).Order("ordinal ASC").Find(&items)
	if loaded.Error != nil {
		return nil, loaded.Error
	}
	return newInPlaceOrdinaryCheckpointOperations(plan, job, items, operationRows)
}

func loadIsolatedOrdinaryCheckpointOperationsTx(
	ctx context.Context,
	tx *gorm.DB,
	plan model.BackupAssetRecoveryPlan,
	preflight model.BackupAssetRecoveryPreflight,
	job model.BackupAssetRecoveryJob,
) ([]ordinaryCheckpointOperation, int, error) {
	operationRows, err := rebuildExecuteOperationRows(plan, preflight, tx.WithContext(ctx))
	if err != nil {
		return nil, 0, ErrRecoveryWorkerFenceLost
	}
	var items []model.BackupAssetRecoveryJobItem
	loaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("job_id = ? AND plan_id = ?", job.ID, plan.ID).Order("ordinal ASC").Find(&items)
	if loaded.Error != nil {
		return nil, 0, loaded.Error
	}
	operations, err := newIsolatedOrdinaryCheckpointOperations(plan, job, items, operationRows)
	if err != nil {
		return nil, 0, err
	}
	for index := range operations {
		if !operations[index].completed {
			return operations, items[index].TargetLocatorKeyVersion, nil
		}
	}
	return nil, 0, ErrRecoveryWorkerFenceLost
}

func loadFreshIsolatedCheckpointOperationsTx(
	ctx context.Context,
	tx *gorm.DB,
	plan model.BackupAssetRecoveryPlan,
	job model.BackupAssetRecoveryJob,
) ([]ordinaryCheckpointOperation, int, error) {
	if TargetMode(plan.TargetMode) != TargetModeIsolated || TargetMode(job.TargetMode) != TargetModeIsolated ||
		plan.ID != job.PlanID {
		return nil, 0, ErrRecoveryWorkerFenceLost
	}
	var items []model.BackupAssetRecoveryJobItem
	loaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("job_id = ? AND plan_id = ?", job.ID, plan.ID).Order("ordinal ASC").Find(&items)
	if loaded.Error != nil {
		return nil, 0, loaded.Error
	}
	if len(items) == 0 {
		return nil, 0, ErrRecoveryWorkerFenceLost
	}
	operations := make([]ordinaryCheckpointOperation, len(items))
	seenItems := make(map[string]struct{}, len(items))
	seenDigests := make(map[string]struct{}, len(items))
	markerKeyVersion := items[0].TargetLocatorKeyVersion
	for index := range items {
		item := items[index]
		kind := RecoveryOperationKind(item.OperationKind)
		digest := recoveryJobItemOperationDigest(item)
		if !validOpaqueID(item.ID) || item.Ordinal != index || item.Outcome != "" || item.FailureCategory != "" ||
			item.TargetLocatorKeyVersion != markerKeyVersion || markerKeyVersion <= 0 || !validDigest(digest) {
			return nil, 0, ErrRecoveryWorkerFenceLost
		}
		switch kind {
		case RecoveryOperationCreate, RecoveryOperationOverwrite, RecoveryOperationSkip:
		default:
			return nil, 0, ErrRecoveryWorkerFenceLost
		}
		if _, duplicate := seenItems[item.ID]; duplicate {
			return nil, 0, ErrRecoveryWorkerFenceLost
		}
		seenItems[item.ID] = struct{}{}
		if _, duplicate := seenDigests[digest]; duplicate {
			return nil, 0, ErrRecoveryWorkerFenceLost
		}
		seenDigests[digest] = struct{}{}
		operations[index] = ordinaryCheckpointOperation{
			itemID: item.ID, digest: digest, kind: kind,
		}
	}
	return operations, markerKeyVersion, nil
}

func (coordinator *WorkerCoordinator) validateDurableRecoveryItem(
	ctx context.Context,
	plan model.BackupAssetRecoveryPlan,
	job model.BackupAssetRecoveryJob,
	item model.BackupAssetRecoveryJobItem,
	operationRow executeOperationRow,
	ordinal int,
	materials map[int]backupasset.DomainKeyMaterial,
) (string, string, error) {
	operation := operationRow.operation
	semanticDigest, err := SemanticTargetDigest(
		TargetMode(job.TargetMode), job.TargetRootID, job.RootLocatorDigest, operation.TargetRelativeLocator,
	)
	if err != nil || semanticDigest != operation.SemanticTargetDigest {
		return "", "", ErrRecoveryWorkerFenceLost
	}
	finalLocator := operation.TargetRelativeLocator
	workspaceLocator := ""
	workspaceBindingDigest := ""
	if TargetMode(job.TargetMode) == TargetModeIsolated {
		workspaceLocator = job.EncryptedWorkspaceRelativeLocator
		workspaceBindingDigest = job.WorkspaceBindingDigest
		finalLocator = workspaceLocator + "/" + finalLocator
	}
	objectDigest, err := TargetObjectDigest(job.TargetRootID, job.RootLocatorDigest, finalLocator)
	if err != nil || objectDigest == semanticDigest {
		return "", "", ErrRecoveryWorkerFenceLost
	}
	if !sameAuthorizationString(item.PlanItemID, operationRow.planItemID) ||
		item.PlanID != plan.ID || item.JobID != job.ID || item.Ordinal != ordinal ||
		item.OperationKind != string(operation.Kind) || item.TargetPathDigest != operation.TargetPathDigest ||
		item.SemanticTargetDigest != semanticDigest || item.TargetObjectDigest != objectDigest ||
		item.ExpectedPriorKind != string(operation.ExpectedPrior.Kind) || item.ExpectedPriorDigest != operation.ExpectedPrior.Digest ||
		item.ExpectedPostIdentityDigest != operation.ExpectedPostIdentityDigest ||
		item.ExpectedPostBytes != operation.ExpectedPostBytes || item.ExpectedPriorBytes != operation.ExpectedPriorBytes ||
		item.TargetLocatorKeyVersion <= 0 || item.TargetLocatorCipherVersion != targetLocatorCipherVersion ||
		item.DisplayClass != string(operation.DisplayClass) || item.EstimatedBytes != operation.EstimatedBytes {
		return "", "", ErrRecoveryWorkerFenceLost
	}
	material, found := materials[item.TargetLocatorKeyVersion]
	if !found {
		material, err = coordinator.workspaceKeys.ByVersion(
			ctx, backupasset.KeyDomainRecoveryCleanupOwnership, item.TargetLocatorKeyVersion,
		)
		if err != nil || !validTargetLocatorKey(material, item.TargetLocatorKeyVersion) {
			return "", "", ErrRecoveryWorkerFenceLost
		}
		materials[item.TargetLocatorKeyVersion] = material
	}
	binding := targetLocatorBindingForExecute(
		plan, job.ID, item.ID, operationRow, workspaceLocator, workspaceBindingDigest,
		objectDigest, item.TargetLocatorKeyVersion,
	)
	locator, err := OpenTargetLocatorEnvelope(material, binding, item.EncryptedTargetRelativeLocator)
	if err != nil || locator != operation.TargetRelativeLocator {
		return "", "", ErrRecoveryWorkerFenceLost
	}
	planItemID := authorizationIntentStringPointer(item.PlanItemID)
	itemDigest := framedDigest(
		"xirang/recovery/interrupted-item/v1", item.ID, item.PlanID, item.JobID, planItemID,
		strconv.Itoa(item.Ordinal), item.OperationKind, item.TargetPathDigest, item.SemanticTargetDigest,
		item.TargetObjectDigest, item.ExpectedPriorKind, item.ExpectedPriorDigest,
		item.ExpectedPostIdentityDigest, strconv.FormatInt(item.ExpectedPostBytes, 10),
		strconv.FormatInt(item.ExpectedPriorBytes, 10), item.EncryptedTargetRelativeLocator,
		strconv.Itoa(item.TargetLocatorKeyVersion), strconv.Itoa(item.TargetLocatorCipherVersion),
		item.DisplayClass, strconv.FormatInt(item.EstimatedBytes, 10), item.Outcome,
		strconv.FormatInt(item.BytesWritten, 10), strconv.FormatInt(item.VerifiedSize, 10),
		item.VerifiedDigest, item.FailureCategory, locator,
	)
	return locator, itemDigest, nil
}

func validateInterruptedOperationWorkspace(
	plan model.BackupAssetRecoveryPlan,
	job model.BackupAssetRecoveryJob,
	claim RecoveryWorkerClaim,
	checkpoints []model.BackupAssetRecoveryCheckpoint,
	checkpointOperations []ordinaryCheckpointOperation,
	material backupasset.DomainKeyMaterial,
	operation RecoveryOperation,
	selectedItemID string,
	ordinaryExecution bool,
	completedOverwrite bool,
	now time.Time,
) (string, error) {
	switch TargetMode(job.TargetMode) {
	case TargetModeIsolated:
		locator := recoveryWorkspaceLocatorDirectory + "/" + job.ID
		markerClaim := RecoveryWorkerClaim{WorkerID: job.WorkspaceOwner, AttemptFence: job.WorkspaceFence}
		workspacePhase := WorkspacePhase(job.WorkspacePhase)
		admitted := len(checkpoints) >= 1 && checkpoints[0].AttemptID != claim.AttemptID &&
			(workspacePhase == WorkspacePhaseMarkerCreated || workspacePhase == WorkspacePhaseWriting) &&
			validRecoveryMarkerValidation(job)
		if ordinaryExecution {
			reservationOnly := len(checkpoints) == 1
			reservedMarkerValidation := reservationOnly && workspacePhase == WorkspacePhaseReserved &&
				emptyRecoveryMarkerValidation(job)
			currentMarkerValidation := reservationOnly && workspacePhase == WorkspacePhaseMarkerCreated &&
				recoveryMarkerValidationMatchesClaim(job, claim)
			continuation := len(checkpoints) > 1 &&
				workspacePhase == WorkspacePhaseWriting && validRecoveryMarkerValidation(job) &&
				checkpoints[len(checkpoints)-1].AttemptID == claim.AttemptID &&
				checkpoints[len(checkpoints)-1].AttemptFence == claim.AttemptFence &&
				checkpoints[len(checkpoints)-1].NodeFence == claim.NodeFence
			admitted = reservedMarkerValidation || currentMarkerValidation || continuation
		}
		if !admitted ||
			job.EncryptedWorkspaceRelativeLocator != locator ||
			job.WorkspaceBindingDigest != recoveryWorkspaceBindingDigest(plan, job.ID, locator) ||
			!validRecoveryWorkerID(job.WorkspaceOwner) || job.WorkspaceFence == 0 ||
			job.PlaintextDeadline == nil || !job.PlaintextDeadline.After(now) ||
			!validTargetLocatorKey(material, material.Version) ||
			job.WorkspaceMarkerBindingDigest != recoveryWorkspaceMarkerBindingDigest(
				material, job.ID, job.TargetRootID, plan.RootRevision, locator, markerClaim,
			) {
			return "", ErrRecoveryWorkerFenceLost
		}
		reservation := checkpoints[0]
		if !validOpaqueID(reservation.ID) || reservation.JobID != job.ID || reservation.JobItemID != "" ||
			!validOpaqueID(reservation.AttemptID) || reservation.Sequence != 0 ||
			reservation.Phase != string(CheckpointPhaseWorkspaceReserved) ||
			reservation.AuthorityCategory != "" || reservation.OperationDigest != "" ||
			reservation.PriorTargetRevision != "" || reservation.NextTargetRevision != "" ||
			reservation.UnresolvedCategory != "" || reservation.WriteResultDigest != "" ||
			reservation.WriteTargetRevision != "" || reservation.ObservationDigest != "" ||
			reservation.ObservedTargetRevision != "" || reservation.ObservedPresence != "" ||
			reservation.SourceRevalidationOutcome != "" || reservation.NodeFence != 0 ||
			reservation.AttemptFence != 0 ||
			reservation.PlanBindingDigest != job.PlanBindingDigest ||
			reservation.SourceRevisionDigest != job.SourceRevisionDigest || reservation.PreflightID != job.PreflightID ||
			reservation.PreflightRevision != job.PreflightRevision || !reservation.PreflightExpiresAt.Equal(job.PreflightExpiresAt) ||
			reservation.SecurityDecision != job.SecurityDecision || reservation.SecurityDecisionDigest != job.SecurityDecisionDigest ||
			reservation.SecurityFindingSetDigest != job.SecurityFindingSetDigest ||
			reservation.SecurityPolicyRevision != job.SecurityPolicyRevision || reservation.AuthorityGrantID != job.AuthorityGrantID ||
			reservation.JobAuthorityCategory != job.AuthorityCategory ||
			reservation.AuthorityBindingDigest != job.AuthorityBindingDigest ||
			!reservation.AuthorityExpiresAt.Equal(job.AuthorityExpiresAt) ||
			reservation.DeleteNodeRevision != "" || reservation.DeleteRootRevision != "" ||
			reservation.DeleteAuthorityExpiresAt != nil || reservation.DeleteGrantID != "" ||
			reservation.DeleteGrantBindingDigest != "" || reservation.DeleteGrantExpiresAt != nil ||
			reservation.DeleteGrantConsumedAt != nil || reservation.CreatedAt.IsZero() || reservation.CreatedAt.After(now) {
			return "", ErrRecoveryWorkerFenceLost
		}
		return validateIsolatedInterruptedCheckpointHistory(
			plan, job, claim, checkpoints, checkpointOperations, selectedItemID, now,
		)
	case TargetModeInPlace:
		if job.WorkspacePhase != string(WorkspacePhaseNone) ||
			job.EncryptedWorkspaceRelativeLocator != "" || job.WorkspaceBindingDigest != "" ||
			job.WorkspaceMarkerBindingDigest != "" || job.WorkspaceOwner != "" || job.WorkspaceFence != 0 ||
			!emptyRecoveryMarkerValidation(job) || job.PlaintextDeadline != nil {
			return "", ErrRecoveryWorkerFenceLost
		}
		if len(checkpoints) == 0 {
			return EmptyDeleteSetDigest, nil
		}
		required, hasRequired, checkpointDigest, err := validateInPlaceOrdinaryCheckpointHistory(
			plan, job, claim, checkpoints, checkpointOperations, now,
		)
		if err != nil {
			return "", ErrRecoveryWorkerFenceLost
		}
		if hasRequired && !completedOverwrite &&
			(operation.Kind != RecoveryOperationDelete || required.ID == "") {
			return "", ErrRecoveryWorkerFenceLost
		}
		return checkpointDigest, nil
	default:
		return "", ErrRecoveryWorkerFenceLost
	}
}

func validateIsolatedInterruptedCheckpointHistory(
	plan model.BackupAssetRecoveryPlan,
	job model.BackupAssetRecoveryJob,
	claim RecoveryWorkerClaim,
	checkpoints []model.BackupAssetRecoveryCheckpoint,
	operations []ordinaryCheckpointOperation,
	selectedItemID string,
	now time.Time,
) (string, error) {
	if TargetMode(plan.TargetMode) != TargetModeIsolated || TargetMode(job.TargetMode) != TargetModeIsolated ||
		plan.ID != job.PlanID || !validRecoveryWorkerClaim(claim) || claim.JobID != job.ID ||
		!validOpaqueID(selectedItemID) || len(checkpoints) == 0 ||
		!validOpaqueRevision(job.PreflightTargetRevision) || !validOpaqueRevision(job.TargetChainRevision) {
		return "", ErrRecoveryWorkerFenceLost
	}

	operationsByItemID := make(map[string]ordinaryCheckpointOperation, len(operations))
	completedOperations := 0
	selectedFound := false
	for _, operation := range operations {
		if !validOpaqueID(operation.itemID) || !validDigest(operation.digest) {
			return "", ErrRecoveryWorkerFenceLost
		}
		switch operation.kind {
		case RecoveryOperationCreate, RecoveryOperationOverwrite, RecoveryOperationSkip:
		default:
			return "", ErrRecoveryWorkerFenceLost
		}
		if _, duplicate := operationsByItemID[operation.itemID]; duplicate {
			return "", ErrRecoveryWorkerFenceLost
		}
		operationsByItemID[operation.itemID] = operation
		if operation.completed {
			completedOperations++
		}
		if operation.itemID == selectedItemID {
			selectedFound = true
			if operation.completed {
				return "", ErrRecoveryWorkerFenceLost
			}
		}
	}
	if !selectedFound || len(checkpoints) != completedOperations+1 {
		return "", ErrRecoveryWorkerFenceLost
	}

	chainRevision := job.PreflightTargetRevision
	checkpointedItems := make(map[string]struct{}, completedOperations)
	for index := 1; index < len(checkpoints); index++ {
		checkpoint := checkpoints[index]
		operation, found := operationsByItemID[checkpoint.JobItemID]
		if !found || !operation.completed || checkpoint.JobItemID == selectedItemID {
			return "", ErrRecoveryWorkerFenceLost
		}
		if _, duplicate := checkpointedItems[checkpoint.JobItemID]; duplicate {
			return "", ErrRecoveryWorkerFenceLost
		}
		checkpointedItems[checkpoint.JobItemID] = struct{}{}
		if !validOpaqueID(checkpoint.ID) || checkpoint.JobID != job.ID ||
			!validOpaqueID(checkpoint.AttemptID) || checkpoint.Sequence != index ||
			checkpoint.Phase != string(CheckpointPhaseOperation) ||
			checkpoint.AuthorityCategory != string(AuthorityWrite) ||
			checkpoint.OperationDigest != operation.digest ||
			checkpoint.PriorTargetRevision != chainRevision ||
			!validOpaqueRevision(checkpoint.NextTargetRevision) ||
			((operation.kind == RecoveryOperationSkip) !=
				(checkpoint.NextTargetRevision == checkpoint.PriorTargetRevision)) ||
			checkpoint.NodeFence == 0 || checkpoint.AttemptFence == 0 ||
			checkpoint.PlanBindingDigest != job.PlanBindingDigest ||
			checkpoint.SourceRevisionDigest != job.SourceRevisionDigest || checkpoint.PreflightID != job.PreflightID ||
			checkpoint.PreflightRevision != job.PreflightRevision ||
			!checkpoint.PreflightExpiresAt.Equal(job.PreflightExpiresAt) ||
			checkpoint.SecurityDecision != job.SecurityDecision ||
			checkpoint.SecurityDecisionDigest != job.SecurityDecisionDigest ||
			checkpoint.SecurityFindingSetDigest != job.SecurityFindingSetDigest ||
			checkpoint.SecurityPolicyRevision != job.SecurityPolicyRevision ||
			checkpoint.AuthorityGrantID != job.AuthorityGrantID ||
			checkpoint.JobAuthorityCategory != job.AuthorityCategory ||
			checkpoint.AuthorityBindingDigest != job.AuthorityBindingDigest ||
			!checkpoint.AuthorityExpiresAt.Equal(job.AuthorityExpiresAt) ||
			checkpoint.UnresolvedCategory != "" || checkpoint.WriteResultDigest != "" ||
			checkpoint.WriteTargetRevision != "" || checkpoint.ObservationDigest != "" ||
			checkpoint.ObservedTargetRevision != "" || checkpoint.ObservedPresence != "" ||
			checkpoint.SourceRevalidationOutcome != "" || checkpoint.DeleteNodeRevision != "" ||
			checkpoint.DeleteRootRevision != "" || checkpoint.DeleteAuthorityExpiresAt != nil ||
			checkpoint.DeleteGrantID != "" || checkpoint.DeleteGrantBindingDigest != "" ||
			checkpoint.DeleteGrantExpiresAt != nil || checkpoint.DeleteGrantConsumedAt != nil ||
			checkpoint.CreatedAt.IsZero() || checkpoint.CreatedAt.After(now) {
			return "", ErrRecoveryWorkerFenceLost
		}
		chainRevision = checkpoint.NextTargetRevision
	}
	if len(checkpointedItems) != completedOperations || chainRevision != job.TargetChainRevision {
		return "", ErrRecoveryWorkerFenceLost
	}
	return interruptedCheckpointHistoryDigest(checkpoints), nil
}

func interruptedCheckpointHistoryDigest(checkpoints []model.BackupAssetRecoveryCheckpoint) string {
	checkpointDigests := make([]string, len(checkpoints))
	for index := range checkpoints {
		checkpoint := checkpoints[index]
		checkpointDigests[index] = framedDigest(
			"xirang/recovery/interrupted-checkpoints/v1", checkpoint.ID, checkpoint.JobID,
			checkpoint.JobItemID, checkpoint.AttemptID, strconv.Itoa(checkpoint.Sequence), checkpoint.Phase,
			checkpoint.AuthorityCategory, checkpoint.OperationDigest, checkpoint.PriorTargetRevision,
			checkpoint.NextTargetRevision, checkpoint.UnresolvedCategory, checkpoint.WriteResultDigest,
			checkpoint.WriteTargetRevision, checkpoint.ObservationDigest, checkpoint.ObservedTargetRevision,
			checkpoint.ObservedPresence, checkpoint.SourceRevalidationOutcome,
			strconv.FormatUint(checkpoint.NodeFence, 10), strconv.FormatUint(checkpoint.AttemptFence, 10),
			checkpoint.PlanBindingDigest, checkpoint.SourceRevisionDigest, checkpoint.PreflightID,
			checkpoint.PreflightRevision, authorizationIntentTime(checkpoint.PreflightExpiresAt),
			checkpoint.SecurityDecision, checkpoint.SecurityDecisionDigest,
			checkpoint.SecurityFindingSetDigest, checkpoint.SecurityPolicyRevision,
			checkpoint.AuthorityGrantID, checkpoint.JobAuthorityCategory, checkpoint.AuthorityBindingDigest,
			authorizationIntentTime(checkpoint.AuthorityExpiresAt), checkpoint.DeleteNodeRevision,
			checkpoint.DeleteRootRevision, authorizationIntentTimePointer(checkpoint.DeleteAuthorityExpiresAt),
			checkpoint.DeleteGrantID, checkpoint.DeleteGrantBindingDigest,
			authorizationIntentTimePointer(checkpoint.DeleteGrantExpiresAt),
			authorizationIntentTimePointer(checkpoint.DeleteGrantConsumedAt),
			authorizationIntentTime(checkpoint.CreatedAt),
		)
	}
	return framedDigest("xirang/recovery/interrupted-checkpoints/v1", checkpointDigests...)
}

func validateInPlaceOrdinaryCheckpointHistory(
	plan model.BackupAssetRecoveryPlan,
	job model.BackupAssetRecoveryJob,
	claim RecoveryWorkerClaim,
	checkpoints []model.BackupAssetRecoveryCheckpoint,
	operations []ordinaryCheckpointOperation,
	now time.Time,
) (model.BackupAssetRecoveryCheckpoint, bool, string, error) {
	if TargetMode(plan.TargetMode) != TargetModeInPlace || TargetMode(job.TargetMode) != TargetModeInPlace ||
		plan.ID != job.PlanID || !validRecoveryWorkerClaim(claim) || claim.JobID != job.ID ||
		!validOpaqueRevision(job.PreflightTargetRevision) || !validOpaqueRevision(job.TargetChainRevision) {
		return model.BackupAssetRecoveryCheckpoint{}, false, "", ErrRecoveryWorkerFenceLost
	}
	completedOperations := 0
	firstDeleteOperation := len(operations)
	deleteOperations := 0
	for index, operation := range operations {
		if !validOpaqueID(operation.itemID) || !validDigest(operation.digest) {
			return model.BackupAssetRecoveryCheckpoint{}, false, "", ErrRecoveryWorkerFenceLost
		}
		switch operation.kind {
		case RecoveryOperationCreate, RecoveryOperationOverwrite, RecoveryOperationSkip, RecoveryOperationDelete:
		default:
			return model.BackupAssetRecoveryCheckpoint{}, false, "", ErrRecoveryWorkerFenceLost
		}
		if operation.completed {
			completedOperations++
		}
		if operation.kind == RecoveryOperationDelete {
			if firstDeleteOperation == len(operations) {
				firstDeleteOperation = index
			}
			deleteOperations++
		} else if firstDeleteOperation != len(operations) {
			return model.BackupAssetRecoveryCheckpoint{}, false, "", ErrRecoveryWorkerFenceLost
		}
	}
	if len(checkpoints) == 0 {
		if completedOperations != 0 {
			return model.BackupAssetRecoveryCheckpoint{}, false, "", ErrRecoveryWorkerFenceLost
		}
		return model.BackupAssetRecoveryCheckpoint{}, false, EmptyDeleteSetDigest, nil
	}

	chainRevision := job.PreflightTargetRevision
	checkpointDigests := make([]string, len(checkpoints))
	var required model.BackupAssetRecoveryCheckpoint
	operationIndex := 0
	consumedDeleteAuthority := false
	for index := range checkpoints {
		checkpoint := checkpoints[index]
		phase := CheckpointPhase(checkpoint.Phase)
		guard := CheckpointAppendGuard{
			SameAttempt: true, SameAttemptFence: true, SameNodeFence: true, MutationArmed: true,
			ExactMirror: ConflictPolicy(plan.ConflictPolicy) == ConflictExactMirror, NextSequence: index,
		}
		if !validOpaqueID(checkpoint.ID) || checkpoint.JobID != job.ID || !validOpaqueID(checkpoint.AttemptID) ||
			checkpoint.Sequence != index || checkpoint.NodeFence == 0 || checkpoint.AttemptFence == 0 ||
			checkpoint.PlanBindingDigest != job.PlanBindingDigest ||
			checkpoint.SourceRevisionDigest != job.SourceRevisionDigest || checkpoint.PreflightID != job.PreflightID ||
			checkpoint.PreflightRevision != job.PreflightRevision ||
			!checkpoint.PreflightExpiresAt.Equal(job.PreflightExpiresAt) ||
			checkpoint.SecurityDecision != job.SecurityDecision ||
			checkpoint.SecurityDecisionDigest != job.SecurityDecisionDigest ||
			checkpoint.SecurityFindingSetDigest != job.SecurityFindingSetDigest ||
			checkpoint.SecurityPolicyRevision != job.SecurityPolicyRevision ||
			checkpoint.AuthorityGrantID != job.AuthorityGrantID ||
			checkpoint.JobAuthorityCategory != job.AuthorityCategory ||
			checkpoint.AuthorityBindingDigest != job.AuthorityBindingDigest ||
			!checkpoint.AuthorityExpiresAt.Equal(job.AuthorityExpiresAt) || checkpoint.CreatedAt.IsZero() ||
			checkpoint.CreatedAt.After(now) {
			return model.BackupAssetRecoveryCheckpoint{}, false, "", ErrRecoveryWorkerFenceLost
		}
		if index == 0 {
			if !CanStartCheckpoint(phase, TargetModeInPlace, guard) {
				return model.BackupAssetRecoveryCheckpoint{}, false, "", ErrRecoveryWorkerFenceLost
			}
		} else {
			previous := checkpoints[index-1]
			if !(CheckpointCursor{Sequence: previous.Sequence, Phase: CheckpointPhase(previous.Phase)}).
				CanAppend(phase, guard) {
				return model.BackupAssetRecoveryCheckpoint{}, false, "", ErrRecoveryWorkerFenceLost
			}
		}

		switch phase {
		case CheckpointPhaseOperation:
			if operationIndex >= len(operations) || !operations[operationIndex].completed ||
				checkpoint.JobItemID != operations[operationIndex].itemID ||
				checkpoint.OperationDigest != operations[operationIndex].digest ||
				(operations[operationIndex].kind == RecoveryOperationDelete) != consumedDeleteAuthority ||
				checkpoint.AuthorityCategory != string(AuthorityWrite) ||
				!validDigest(checkpoint.OperationDigest) || checkpoint.PriorTargetRevision != chainRevision ||
				!validOpaqueRevision(checkpoint.NextTargetRevision) ||
				((operations[operationIndex].kind == RecoveryOperationSkip) !=
					(checkpoint.NextTargetRevision == checkpoint.PriorTargetRevision)) ||
				checkpoint.DeleteNodeRevision != "" ||
				checkpoint.DeleteRootRevision != "" || checkpoint.DeleteAuthorityExpiresAt != nil ||
				checkpoint.DeleteGrantID != "" || checkpoint.DeleteGrantBindingDigest != "" ||
				checkpoint.DeleteGrantExpiresAt != nil || checkpoint.DeleteGrantConsumedAt != nil ||
				checkpoint.UnresolvedCategory != "" ||
				checkpoint.WriteResultDigest != "" || checkpoint.WriteTargetRevision != "" ||
				checkpoint.ObservationDigest != "" || checkpoint.ObservedTargetRevision != "" ||
				checkpoint.ObservedPresence != "" || checkpoint.SourceRevalidationOutcome != "" {
				return model.BackupAssetRecoveryCheckpoint{}, false, "", ErrRecoveryWorkerFenceLost
			}
			operationIndex++
			chainRevision = checkpoint.NextTargetRevision
		case CheckpointPhaseDeleteAuthorityRequired:
			if required.ID != "" ||
				deleteOperations == 0 || operationIndex != firstDeleteOperation ||
				ConflictPolicy(plan.ConflictPolicy) != ConflictExactMirror ||
				checkpoint.AuthorityCategory != string(AuthorityExactMirrorDelete) ||
				checkpoint.OperationDigest != job.DeleteSetDigest ||
				checkpoint.PriorTargetRevision != chainRevision || checkpoint.NextTargetRevision != "" ||
				checkpoint.DeleteNodeRevision != job.PreflightNodeRevision ||
				checkpoint.DeleteRootRevision != plan.RootRevision || checkpoint.DeleteAuthorityExpiresAt == nil ||
				checkpoint.DeleteGrantID != "" ||
				checkpoint.DeleteGrantBindingDigest != "" || checkpoint.DeleteGrantExpiresAt != nil ||
				checkpoint.DeleteGrantConsumedAt != nil {
				return model.BackupAssetRecoveryCheckpoint{}, false, "", ErrRecoveryWorkerFenceLost
			}
			required = checkpoint
		case CheckpointPhaseDeleteAuthorityConsumed:
			if index == 0 || required.ID == "" || consumedDeleteAuthority ||
				operationIndex != firstDeleteOperation ||
				CheckpointPhase(checkpoints[index-1].Phase) != CheckpointPhaseDeleteAuthorityRequired ||
				checkpoint.AuthorityCategory != string(AuthorityExactMirrorDelete) ||
				checkpoint.OperationDigest != job.DeleteSetDigest ||
				checkpoint.PriorTargetRevision != chainRevision || checkpoint.NextTargetRevision != chainRevision ||
				checkpoint.DeleteNodeRevision != required.DeleteNodeRevision ||
				checkpoint.DeleteRootRevision != required.DeleteRootRevision ||
				checkpoint.DeleteAuthorityExpiresAt == nil || required.DeleteAuthorityExpiresAt == nil ||
				!checkpoint.DeleteAuthorityExpiresAt.Equal(*required.DeleteAuthorityExpiresAt) ||
				!validOpaqueID(checkpoint.DeleteGrantID) || !validDigest(checkpoint.DeleteGrantBindingDigest) ||
				checkpoint.DeleteGrantExpiresAt == nil || checkpoint.DeleteGrantConsumedAt == nil ||
				checkpoint.DeleteGrantExpiresAt.After(*required.DeleteAuthorityExpiresAt) ||
				checkpoint.DeleteGrantConsumedAt.After(*checkpoint.DeleteGrantExpiresAt) ||
				checkpoint.DeleteGrantConsumedAt.After(checkpoint.CreatedAt) {
				return model.BackupAssetRecoveryCheckpoint{}, false, "", ErrRecoveryWorkerFenceLost
			}
			consumedDeleteAuthority = true
		default:
			return model.BackupAssetRecoveryCheckpoint{}, false, "", ErrRecoveryWorkerFenceLost
		}
		checkpointDigests[index] = framedDigest(
			"xirang/recovery/in-place-checkpoint/v1", checkpoint.ID, checkpoint.JobID,
			checkpoint.JobItemID, checkpoint.AttemptID, strconv.Itoa(checkpoint.Sequence), checkpoint.Phase,
			checkpoint.AuthorityCategory, checkpoint.OperationDigest, checkpoint.PriorTargetRevision,
			checkpoint.NextTargetRevision, checkpoint.UnresolvedCategory, checkpoint.WriteResultDigest,
			checkpoint.WriteTargetRevision, checkpoint.ObservationDigest, checkpoint.ObservedTargetRevision,
			checkpoint.ObservedPresence, checkpoint.SourceRevalidationOutcome,
			strconv.FormatUint(checkpoint.NodeFence, 10),
			strconv.FormatUint(checkpoint.AttemptFence, 10), checkpoint.PlanBindingDigest,
			checkpoint.SourceRevisionDigest, checkpoint.PreflightID, checkpoint.PreflightRevision,
			checkpoint.SecurityDecision, checkpoint.SecurityDecisionDigest,
			checkpoint.SecurityFindingSetDigest, checkpoint.SecurityPolicyRevision,
			checkpoint.AuthorityGrantID, checkpoint.JobAuthorityCategory,
			checkpoint.AuthorityBindingDigest, checkpoint.DeleteNodeRevision,
			checkpoint.DeleteRootRevision, authorizationIntentTimePointer(checkpoint.DeleteAuthorityExpiresAt),
			checkpoint.DeleteGrantID, checkpoint.DeleteGrantBindingDigest,
			authorizationIntentTimePointer(checkpoint.DeleteGrantExpiresAt),
			authorizationIntentTimePointer(checkpoint.DeleteGrantConsumedAt),
		)
	}
	if operationIndex != completedOperations || (required.ID != "" && deleteOperations == 0) {
		return model.BackupAssetRecoveryCheckpoint{}, false, "", ErrRecoveryWorkerFenceLost
	}
	lastPhase := CheckpointPhase(checkpoints[len(checkpoints)-1].Phase)
	if lastPhase == CheckpointPhaseDeleteAuthorityRequired &&
		(required.DeleteAuthorityExpiresAt == nil || !required.DeleteAuthorityExpiresAt.After(now)) {
		return model.BackupAssetRecoveryCheckpoint{}, false, "", ErrRecoveryWorkerFenceLost
	}
	if chainRevision != job.TargetChainRevision {
		return model.BackupAssetRecoveryCheckpoint{}, false, "", ErrRecoveryWorkerFenceLost
	}
	return required, required.ID != "", framedDigest(
		"xirang/recovery/in-place-checkpoint-history/v1", checkpointDigests...,
	), nil
}

func ordinaryConsumedDeleteCheckpoints(
	checkpoints []model.BackupAssetRecoveryCheckpoint,
) (model.BackupAssetRecoveryCheckpoint, model.BackupAssetRecoveryCheckpoint, bool) {
	for index := 1; index < len(checkpoints); index++ {
		if CheckpointPhase(checkpoints[index].Phase) == CheckpointPhaseDeleteAuthorityConsumed &&
			CheckpointPhase(checkpoints[index-1].Phase) == CheckpointPhaseDeleteAuthorityRequired {
			return checkpoints[index-1], checkpoints[index], true
		}
	}
	return model.BackupAssetRecoveryCheckpoint{}, model.BackupAssetRecoveryCheckpoint{}, false
}

func validateConsumedOrdinaryDeleteGrantTx(
	ctx context.Context,
	tx *gorm.DB,
	plan model.BackupAssetRecoveryPlan,
	job model.BackupAssetRecoveryJob,
	required model.BackupAssetRecoveryCheckpoint,
	consumed model.BackupAssetRecoveryCheckpoint,
) error {
	if tx == nil || !validOpaqueID(plan.ID) || !validOpaqueID(job.ID) || job.PlanID != plan.ID ||
		required.ID == "" || consumed.ID == "" ||
		required.JobID != job.ID || consumed.JobID != job.ID ||
		!validOpaqueID(required.AttemptID) || required.AttemptID != consumed.AttemptID ||
		required.AttemptFence == 0 || required.AttemptFence != consumed.AttemptFence ||
		required.NodeFence == 0 || required.NodeFence != consumed.NodeFence ||
		consumed.Sequence != required.Sequence+1 ||
		CheckpointPhase(required.Phase) != CheckpointPhaseDeleteAuthorityRequired ||
		CheckpointPhase(consumed.Phase) != CheckpointPhaseDeleteAuthorityConsumed {
		return ErrRecoveryWorkerFenceLost
	}
	var grant model.BackupAssetRecoveryGrant
	loaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ? AND plan_id = ?", consumed.DeleteGrantID, plan.ID).Limit(1).Find(&grant)
	if loaded.Error != nil {
		return loaded.Error
	}
	if loaded.RowsAffected != 1 || grant.JobID == nil || *grant.JobID != job.ID ||
		grant.DeleteCheckpointID == nil || *grant.DeleteCheckpointID != required.ID ||
		grant.DeleteAttemptID == nil || *grant.DeleteAttemptID != required.AttemptID ||
		grant.AuthorityCategory != string(AuthorityExactMirrorDelete) ||
		grant.DeleteSetDigest != job.DeleteSetDigest ||
		grant.DeleteTargetRevision != required.PriorTargetRevision ||
		grant.DeleteAttemptFence != required.AttemptFence || grant.DeleteNodeFence != required.NodeFence ||
		!validDigest(grant.GrantHash) || grant.ConsumedAt == nil || grant.RevokedAt != nil ||
		consumed.DeleteGrantExpiresAt == nil || consumed.DeleteGrantConsumedAt == nil ||
		!grant.ExpiresAt.Equal(*consumed.DeleteGrantExpiresAt) ||
		!grant.ConsumedAt.Equal(*consumed.DeleteGrantConsumedAt) ||
		grant.ConsumedAt.After(grant.ExpiresAt) || grant.ConsumedAt.Before(required.CreatedAt) ||
		grant.ConsumedAt.After(consumed.CreatedAt) {
		return ErrRecoveryWorkerFenceLost
	}
	bindingDigest := framedDigest(
		recoveryAuthorizationGrantBindingDomain,
		string(AuthorizationReceiptCategoryExactMirrorDelete),
		plan.ID,
		job.ID,
		required.ID,
		grant.GrantHash,
		grant.ExpiresAt.Format(time.RFC3339Nano),
	)
	if grant.BindingDigest != bindingDigest || consumed.DeleteGrantBindingDigest != bindingDigest {
		return ErrRecoveryWorkerFenceLost
	}
	return nil
}

func recoveryOperationVerifyExpectation(
	operation RecoveryOperation,
	item model.BackupAssetRecoveryJobItem,
) (TargetVerifyExpectation, error) {
	var expectation TargetVerifyExpectation
	switch operation.Kind {
	case RecoveryOperationCreate, RecoveryOperationOverwrite:
		expectation = TargetVerifyExpectation{
			Kind: TargetPresencePresent,
			Present: &PresentExpectation{
				IdentityDigest: item.ExpectedPostIdentityDigest,
				Bytes:          item.ExpectedPostBytes,
			},
		}
	case RecoveryOperationSkip:
		expectation = TargetVerifyExpectation{
			Kind: TargetPresencePresent,
			Present: &PresentExpectation{
				IdentityDigest: item.ExpectedPriorDigest,
				Bytes:          item.ExpectedPriorBytes,
			},
		}
	case RecoveryOperationDelete:
		expectation = TargetVerifyExpectation{Kind: TargetPresenceAbsent, Absent: &AbsentExpectation{}}
	default:
		return TargetVerifyExpectation{}, ErrRecoveryWorkerFenceLost
	}
	if expectation.Validate() != nil {
		return TargetVerifyExpectation{}, ErrRecoveryWorkerFenceLost
	}
	return expectation, nil
}

func cloneTargetVerifyExpectation(expectation TargetVerifyExpectation) TargetVerifyExpectation {
	clone := TargetVerifyExpectation{Kind: expectation.Kind}
	if expectation.Present != nil {
		present := *expectation.Present
		clone.Present = &present
	}
	if expectation.Absent != nil {
		absent := *expectation.Absent
		clone.Absent = &absent
	}
	return clone
}

func interruptedOperationDurableDigest(
	claim RecoveryWorkerClaim,
	plan model.BackupAssetRecoveryPlan,
	preflight model.BackupAssetRecoveryPreflight,
	grant model.BackupAssetRecoveryGrant,
	job model.BackupAssetRecoveryJob,
	item model.BackupAssetRecoveryJobItem,
	source model.RecoveryPointLease,
	node model.BackupAssetRecoveryNodeLease,
	attempt model.BackupAssetRecoveryAttempt,
	itemSetDigest,
	checkpointDigest,
	operationDigest,
	targetSessionBindingDigest string,
	object TargetObjectRef,
	expectation TargetVerifyExpectation,
) string {
	presence, identity, bytes := string(expectation.Kind), "", int64(-1)
	if expectation.Present != nil {
		identity, bytes = expectation.Present.IdentityDigest, expectation.Present.Bytes
	}
	return framedDigest(
		"xirang/recovery/interrupted-operation-handoff/v1", claim.JobID, claim.AttemptID,
		claim.NodeLeaseID, claim.WorkerID, strconv.FormatUint(claim.AttemptFence, 10),
		strconv.FormatUint(claim.NodeFence, 10), strconv.FormatUint(claim.TransitionRevision, 10),
		claim.SourceFence.LeaseID, claim.SourceFence.FenceToken, plan.ID, plan.BindingDigest,
		plan.OperationSetDigest, plan.DeleteSetDigest, plan.RootRevision, preflight.ID,
		preflight.Revision, preflight.OperationSetDigest, preflight.DeleteSetDigest, grant.ID,
		grant.BindingDigest, authorizationIntentTimePointer(grant.ConsumedAt), job.ID, job.PlanID,
		job.State, strconv.FormatUint(job.TransitionRevision, 10), job.WorkspacePhase,
		job.EncryptedWorkspaceRelativeLocator, job.WorkspaceBindingDigest, job.WorkspaceMarkerBindingDigest,
		job.WorkspaceOwner, strconv.FormatUint(job.WorkspaceFence, 10),
		job.WorkspaceMarkerValidationAttemptID,
		strconv.FormatUint(job.WorkspaceMarkerValidationAttemptFence, 10),
		strconv.FormatUint(job.WorkspaceMarkerValidationNodeFence, 10), job.TargetChainRevision,
		item.ID, itemSetDigest, checkpointDigest, operationDigest, source.ID, source.AttemptID,
		source.FenceToken, node.ID, authorizationIntentStringPointer(node.AttemptID), node.OwnerID,
		strconv.FormatUint(node.Fence, 10), attempt.ID, attempt.OwnerID,
		strconv.FormatUint(attempt.Fence, 10), strconv.FormatBool(attempt.MutationArmed),
		targetSessionBindingDigest,
		object.RootID, object.RootLocatorDigest, object.TargetPathDigest, object.PrivateRelativeLocator,
		presence, identity, strconv.FormatInt(bytes, 10),
	)
}

func (coordinator *WorkerCoordinator) projectInterruptedOperationTx(
	ctx context.Context,
	tx *gorm.DB,
	claim RecoveryWorkerClaim,
	handoff interruptedOperationHandoff,
	observed TargetVerifyObservation,
	sourceOutcome SourceRevalidationOutcome,
	now time.Time,
	terminalState *JobState,
) (model.BackupAssetRecoveryCheckpoint, error) {
	if !sourceOutcome.Valid() || observed.ValidateAgainst(handoff.expectation) != nil {
		return model.BackupAssetRecoveryCheckpoint{}, ErrRecoveryWorkerFenceLost
	}
	item := handoff.item
	job := handoff.job
	outcome := "succeeded"
	if RecoveryOperationKind(item.OperationKind) == RecoveryOperationSkip {
		outcome = "skipped"
	}
	bytesWritten := int64(0)
	verifiedSize := int64(0)
	verifiedDigest := ""
	if observed.Present != nil {
		verifiedSize = observed.Present.Bytes
		verifiedDigest = observed.Present.IdentityDigest
		if outcome == "succeeded" {
			bytesWritten = observed.Present.Bytes
		}
	}
	var nextRevision string
	var err error
	switch RecoveryOperationKind(item.OperationKind) {
	case RecoveryOperationCreate, RecoveryOperationOverwrite:
		if item.PlanItemID == nil || observed.Present == nil {
			return model.BackupAssetRecoveryCheckpoint{}, ErrRecoveryWorkerFenceLost
		}
		advance := TargetChainAdvance{
			PriorRevision: job.TargetChainRevision, OperationDigest: handoff.operationDigest,
			PlanItemID: *item.PlanItemID, SourceRevisionDigest: job.SourceRevisionDigest,
			AttemptID: claim.AttemptID, AttemptFence: claim.AttemptFence, NodeFence: claim.NodeFence,
			VerifiedIdentity: observed.Present.IdentityDigest, TargetRevision: observed.ObservedRevision,
		}
		nextRevision, err = advance.NextRevision()
	case RecoveryOperationDelete:
		if item.PlanItemID != nil || observed.Absent == nil {
			return model.BackupAssetRecoveryCheckpoint{}, ErrRecoveryWorkerFenceLost
		}
		advance := TargetAbsenceChainAdvance{
			PriorRevision: job.TargetChainRevision, OperationDigest: handoff.operationDigest,
			JobItemID: item.ID, SourceRevisionDigest: job.SourceRevisionDigest,
			AttemptID: claim.AttemptID, AttemptFence: claim.AttemptFence, NodeFence: claim.NodeFence,
			AbsenceEvidence: observed.Absent.Evidence, TargetRevision: observed.ObservedRevision,
		}
		nextRevision, err = advance.NextRevision()
	case RecoveryOperationSkip:
		nextRevision = job.TargetChainRevision
	default:
		return model.BackupAssetRecoveryCheckpoint{}, ErrRecoveryWorkerFenceLost
	}
	if err != nil {
		return model.BackupAssetRecoveryCheckpoint{}, ErrRecoveryWorkerFenceLost
	}
	sequence := 0
	var last model.BackupAssetRecoveryCheckpoint
	loaded := tx.WithContext(ctx).Where("job_id = ?", job.ID).
		Order("sequence DESC").Limit(1).Find(&last)
	if loaded.Error != nil {
		return model.BackupAssetRecoveryCheckpoint{}, loaded.Error
	}
	if loaded.RowsAffected == 1 {
		sequence = last.Sequence + 1
	}
	checkpointID, err := backupasset.NewOpaqueID()
	if err != nil {
		return model.BackupAssetRecoveryCheckpoint{}, err
	}
	checkpoint := model.BackupAssetRecoveryCheckpoint{
		ID: checkpointID, JobID: job.ID, JobItemID: item.ID,
		AttemptID: claim.AttemptID, Sequence: sequence,
		Phase: string(CheckpointPhaseOperation), AuthorityCategory: string(AuthorityWrite),
		OperationDigest: handoff.operationDigest, PriorTargetRevision: job.TargetChainRevision,
		NextTargetRevision: nextRevision, NodeFence: claim.NodeFence, AttemptFence: claim.AttemptFence,
		PlanBindingDigest: job.PlanBindingDigest, SourceRevisionDigest: job.SourceRevisionDigest,
		PreflightID: job.PreflightID, PreflightRevision: job.PreflightRevision,
		PreflightExpiresAt: job.PreflightExpiresAt, SecurityDecision: job.SecurityDecision,
		SecurityDecisionDigest: job.SecurityDecisionDigest, SecurityFindingSetDigest: job.SecurityFindingSetDigest,
		SecurityPolicyRevision: job.SecurityPolicyRevision, AuthorityGrantID: job.AuthorityGrantID,
		JobAuthorityCategory: job.AuthorityCategory, AuthorityBindingDigest: job.AuthorityBindingDigest,
		AuthorityExpiresAt: job.AuthorityExpiresAt, CreatedAt: now,
	}
	if err := tx.WithContext(ctx).Create(&checkpoint).Error; err != nil {
		return model.BackupAssetRecoveryCheckpoint{}, err
	}
	updated := tx.WithContext(ctx).Model(&model.BackupAssetRecoveryJobItem{}).
		Where(`id = ? AND job_id = ? AND plan_id = ? AND outcome = '' AND failure_category = ''
			AND semantic_target_digest = ? AND target_object_digest = ?
			AND encrypted_target_relative_locator = ? AND target_locator_key_version = ?
			AND target_locator_cipher_version = ?`,
			item.ID, job.ID, item.PlanID, item.SemanticTargetDigest, item.TargetObjectDigest,
			item.EncryptedTargetRelativeLocator, item.TargetLocatorKeyVersion, item.TargetLocatorCipherVersion).
		Updates(map[string]any{
			"outcome": outcome, "bytes_written": bytesWritten, "verified_size": verifiedSize,
			"verified_digest": verifiedDigest, "updated_at": now,
		})
	if updated.Error != nil {
		return model.BackupAssetRecoveryCheckpoint{}, updated.Error
	}
	if updated.RowsAffected != 1 {
		return model.BackupAssetRecoveryCheckpoint{}, ErrRecoveryWorkerFenceLost
	}
	nextWorkspacePhase := job.WorkspacePhase
	if TargetMode(job.TargetMode) == TargetModeIsolated {
		nextWorkspacePhase = string(WorkspacePhaseWriting)
	}
	updated = tx.WithContext(ctx).Model(&model.BackupAssetRecoveryJob{}).
		Where(`id = ? AND state = ? AND transition_revision = ? AND workspace_phase = ?
			AND workspace_binding_digest = ? AND target_chain_revision = ?`,
			job.ID, JobStateRunning, job.TransitionRevision, job.WorkspacePhase,
			job.WorkspaceBindingDigest, job.TargetChainRevision).
		Updates(map[string]any{
			"target_chain_revision": nextRevision, "workspace_phase": nextWorkspacePhase, "updated_at": now,
		})
	if updated.Error != nil {
		return model.BackupAssetRecoveryCheckpoint{}, updated.Error
	}
	if updated.RowsAffected != 1 {
		return model.BackupAssetRecoveryCheckpoint{}, ErrRecoveryWorkerFenceLost
	}
	job.TargetChainRevision = nextRevision
	job.WorkspacePhase = nextWorkspacePhase
	if sourceOutcome != SourceRevalidationMatched {
		if _, err := coordinator.projectSourceRevalidationFailureTx(
			ctx, tx, claim, handoff.plan, job, checkpoint, sourceOutcome, now, now, false,
		); err != nil {
			return model.BackupAssetRecoveryCheckpoint{}, err
		}
		if terminalState != nil {
			*terminalState = JobStateNeedsAttention
		}
		return checkpoint, nil
	}
	var pending int64
	if err := tx.WithContext(ctx).Model(&model.BackupAssetRecoveryJobItem{}).
		Where("job_id = ? AND outcome = '' AND failure_category = ''", job.ID).Count(&pending).Error; err != nil {
		return model.BackupAssetRecoveryCheckpoint{}, err
	}
	if pending != 0 {
		return checkpoint, nil
	}
	if err := coordinator.completeOrdinaryRecoveryJobTx(ctx, tx, claim, job, nextRevision, now); err != nil {
		return model.BackupAssetRecoveryCheckpoint{}, err
	}
	if terminalState != nil {
		*terminalState = JobStateSucceeded
	}
	return checkpoint, nil
}

func (coordinator *WorkerCoordinator) completeOrdinaryRecoveryJobTx(
	ctx context.Context,
	tx *gorm.DB,
	claim RecoveryWorkerClaim,
	job model.BackupAssetRecoveryJob,
	targetRevision string,
	now time.Time,
) error {
	if coordinator == nil || coordinator.sourceLeases == nil || tx == nil ||
		!validRecoveryWorkerClaim(claim) || job.ID != claim.JobID ||
		job.State != string(JobStateRunning) || job.TransitionRevision != claim.TransitionRevision ||
		!validOpaqueRevision(targetRevision) || now.IsZero() {
		return ErrRecoveryWorkerFenceLost
	}
	if err := closeInterruptedOperationAttemptTx(ctx, tx, claim, now); err != nil {
		return err
	}
	if err := coordinator.sourceLeases.ReleaseTx(ctx, tx, claim.SourceFence); err != nil {
		return recoveryWorkerSourceError(err)
	}
	updated := tx.WithContext(ctx).Model(&model.BackupAssetRecoveryNodeLease{}).
		Where("id = ? AND job_id = ? AND attempt_id = ? AND owner_id = ? AND fence = ? AND state = ?",
			claim.NodeLeaseID, job.ID, claim.AttemptID, claim.WorkerID, claim.NodeFence, "active").
		Updates(map[string]any{"state": "released", "released_at": now, "updated_at": now})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return ErrRecoveryWorkerFenceLost
	}
	terminalPhase := job.WorkspacePhase
	if TargetMode(job.TargetMode) == TargetModeIsolated {
		terminalPhase = string(WorkspacePhaseSealed)
	}
	updated = tx.WithContext(ctx).Model(&model.BackupAssetRecoveryJob{}).
		Where("id = ? AND state = ? AND transition_revision = ? AND workspace_phase = ? AND target_chain_revision = ?",
			job.ID, JobStateRunning, claim.TransitionRevision, job.WorkspacePhase, targetRevision).
		Updates(map[string]any{
			"state": string(JobStateVerifying), "transition_revision": claim.TransitionRevision + 1,
			"workspace_phase": terminalPhase, "updated_at": now,
		})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return ErrRecoveryWorkerFenceLost
	}
	updated = tx.WithContext(ctx).Model(&model.BackupAssetRecoveryJob{}).
		Where("id = ? AND state = ? AND transition_revision = ? AND workspace_phase = ? AND target_chain_revision = ?",
			job.ID, JobStateVerifying, claim.TransitionRevision+1, terminalPhase, targetRevision).
		Updates(map[string]any{
			"state": string(JobStateSucceeded), "transition_revision": claim.TransitionRevision + 2,
			"updated_at": now,
		})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return ErrRecoveryWorkerFenceLost
	}
	return nil
}

func closeInterruptedOperationAttemptTx(
	ctx context.Context,
	tx *gorm.DB,
	claim RecoveryWorkerClaim,
	now time.Time,
) error {
	updated := tx.WithContext(ctx).Model(&model.BackupAssetRecoveryAttempt{}).
		Where(`id = ? AND job_id = ? AND owner_id = ? AND fence = ? AND state = ?
			AND mutation_armed = ? AND lease_expires_at > ?`,
			claim.AttemptID, claim.JobID, claim.WorkerID, claim.AttemptFence,
			AttemptStateRunning, true, now).
		Updates(map[string]any{
			"state": string(AttemptStateCompleted), "closed_at": now, "updated_at": now,
		})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return ErrRecoveryWorkerFenceLost
	}
	return nil
}

func publicInterruptedOperationError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, ErrRecoveryWorkerFenceLost) || errors.Is(err, ErrRecoverySourceChanged) {
		return err
	}
	return fmt.Errorf("%w: adopt interrupted recovery operation", ErrRecoveryWorkerUnavailable)
}

// ProjectOperationVerification attaches a verified item projection to the
// current immutable operation checkpoint. The ordinary evidence row carries
// only sanitized facts; its summary digest binds the otherwise private
// attempt, node, checkpoint, and target-chain tuple.
func (coordinator *WorkerCoordinator) ProjectOperationVerification(
	ctx context.Context,
	claim RecoveryWorkerClaim,
	jobItemID string,
	checkpointID string,
	bytesWritten int64,
	verifiedSize int64,
	verifiedDigest string,
	verifiedAt time.Time,
) (model.BackupAssetRecoveryEvidence, error) {
	if coordinator == nil || coordinator.db == nil || coordinator.sourceValidator == nil ||
		coordinator.sourceLeases == nil || coordinator.liveRevalidator == nil ||
		!validRecoveryWorkerClaim(claim) || !validOpaqueID(jobItemID) || !validOpaqueID(checkpointID) ||
		bytesWritten < 0 || verifiedSize < 0 || !validDigest(verifiedDigest) || verifiedAt.IsZero() {
		return model.BackupAssetRecoveryEvidence{}, ErrInvalidRecoveryWorker
	}
	ctx = recoveryWorkerContext(ctx)
	if err := ctx.Err(); err != nil {
		return model.BackupAssetRecoveryEvidence{}, err
	}
	verifiedAt = verifiedAt.UTC()
	observedAuthority, err := coordinator.observeRecoveryAuthorityForJob(ctx, claim.JobID)
	if err != nil {
		if errors.Is(err, ErrRecoveryWorkerFenceLost) || errors.Is(err, ErrRecoverySourceChanged) {
			return model.BackupAssetRecoveryEvidence{}, err
		}
		return model.BackupAssetRecoveryEvidence{}, fmt.Errorf(
			"%w: observe recovery operation verification authority", ErrRecoveryWorkerUnavailable,
		)
	}

	var evidence model.BackupAssetRecoveryEvidence
	err = coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := coordinator.now().UTC()
		if verifiedAt.After(now) {
			return ErrRecoveryWorkerFenceLost
		}
		var jobReference struct {
			PlanID string `gorm:"column:plan_id"`
		}
		loaded := tx.WithContext(ctx).Table((model.BackupAssetRecoveryJob{}).TableName()).
			Select("plan_id").Where("id = ?", claim.JobID).Limit(1).Find(&jobReference)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || !validOpaqueID(jobReference.PlanID) {
			return ErrRecoveryWorkerFenceLost
		}
		var plan model.BackupAssetRecoveryPlan
		loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ?", jobReference.PlanID).Limit(1).Find(&plan)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || PlanState(plan.State) != PlanStateExecuted {
			return ErrRecoveryWorkerFenceLost
		}
		var job model.BackupAssetRecoveryJob
		loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ? AND plan_id = ?", claim.JobID, plan.ID).Limit(1).Find(&job)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || job.PlanID != jobReference.PlanID ||
			job.State != string(JobStateRunning) || job.TransitionRevision != claim.TransitionRevision {
			return ErrRecoveryWorkerFenceLost
		}
		var preflight model.BackupAssetRecoveryPreflight
		loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ? AND plan_id = ?", job.PreflightID, plan.ID).Limit(1).Find(&preflight)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 {
			return ErrRecoveryWorkerFenceLost
		}
		var grant model.BackupAssetRecoveryGrant
		loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ? AND plan_id = ?", job.AuthorityGrantID, plan.ID).Limit(1).Find(&grant)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || !validRecoveryJobBinding(plan, job, preflight, grant, now, false) {
			return ErrRecoveryWorkerFenceLost
		}
		if err := coordinator.sourceValidator.RevalidatePlanTx(ctx, tx, plan); err != nil {
			return err
		}
		if err := coordinator.revalidateObservedRecoveryAuthorityTx(
			ctx, tx, plan, preflight, observedAuthority,
		); err != nil {
			return err
		}

		var source model.RecoveryPointLease
		loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ?", claim.SourceFence.LeaseID).Limit(1).Find(&source)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || !matchesCurrentRecoverySourceFence(source, claim.SourceFence, plan.RecoveryPointID, now) {
			return ErrRecoveryWorkerFenceLost
		}
		var node model.BackupAssetRecoveryNodeLease
		loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ?", claim.NodeLeaseID).Limit(1).Find(&node)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || !matchesCurrentRecoveryNodeFence(node, claim, job, now) {
			return ErrRecoveryWorkerFenceLost
		}
		var attempt model.BackupAssetRecoveryAttempt
		loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ? AND job_id = ?", claim.AttemptID, job.ID).Limit(1).Find(&attempt)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || !attempt.MutationArmed || !matchesCurrentRecoveryAttemptFence(attempt, claim, now) {
			return ErrRecoveryWorkerFenceLost
		}
		var item model.BackupAssetRecoveryJobItem
		loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ? AND job_id = ? AND plan_id = ?", jobItemID, job.ID, plan.ID).Limit(1).Find(&item)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || item.Outcome != "succeeded" || item.FailureCategory != "" {
			return ErrRecoveryWorkerFenceLost
		}
		var checkpoint model.BackupAssetRecoveryCheckpoint
		loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ? AND job_id = ? AND attempt_id = ?", checkpointID, job.ID, claim.AttemptID).
			Limit(1).Find(&checkpoint)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || checkpoint.Phase != string(CheckpointPhaseOperation) ||
			checkpoint.AttemptFence != claim.AttemptFence || checkpoint.NodeFence != claim.NodeFence ||
			checkpoint.OperationDigest != recoveryJobItemOperationDigest(item) ||
			checkpoint.NextTargetRevision != job.TargetChainRevision || checkpoint.PriorTargetRevision == "" {
			return ErrRecoveryWorkerFenceLost
		}

		summaryDigest := recoveryVerificationEvidenceSummaryDigest(
			claim, checkpoint, item, bytesWritten, verifiedSize, verifiedDigest, verifiedAt,
		)
		var existing model.BackupAssetRecoveryEvidence
		loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("job_id = ? AND kind = ? AND summary_digest = ?", job.ID, "verification", summaryDigest).
			Limit(1).Find(&existing)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected == 1 {
			if existing.JobID == nil || *existing.JobID != job.ID || existing.PlanID == nil || *existing.PlanID != plan.ID ||
				existing.CheckpointID == nil || *existing.CheckpointID != checkpoint.ID ||
				existing.GrantID == nil || *existing.GrantID != grant.ID ||
				existing.AttemptID == nil || *existing.AttemptID != attempt.ID ||
				existing.SourceLeaseID == nil || *existing.SourceLeaseID != source.ID ||
				existing.NodeLeaseID == nil || *existing.NodeLeaseID != node.ID ||
				existing.NodeLeaseFence != node.Fence || existing.Outcome != "succeeded" ||
				existing.DifferenceCount != 0 || existing.VerifiedAt == nil ||
				!existing.VerifiedAt.Equal(verifiedAt) || item.BytesWritten != bytesWritten ||
				item.VerifiedSize != verifiedSize || item.VerifiedDigest != verifiedDigest {
				return ErrRecoveryWorkerFenceLost
			}
			evidence = existing
			return nil
		}

		updated := tx.WithContext(ctx).Model(&model.BackupAssetRecoveryJobItem{}).
			Where("id = ? AND job_id = ? AND outcome = ? AND bytes_written = ? AND verified_size = ? AND verified_digest = ? AND failure_category = ?",
				item.ID, job.ID, "succeeded", 0, 0, "", "").
			Updates(map[string]any{
				"bytes_written": bytesWritten, "verified_size": verifiedSize, "verified_digest": verifiedDigest,
				"updated_at": now,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrRecoveryWorkerFenceLost
		}
		evidenceID, err := backupasset.NewOpaqueID()
		if err != nil {
			return err
		}
		jobID, planID, boundCheckpointID, grantID := job.ID, plan.ID, checkpoint.ID, grant.ID
		attemptID, sourceLeaseID, nodeLeaseID := attempt.ID, source.ID, node.ID
		evidence = model.BackupAssetRecoveryEvidence{
			ID: evidenceID, JobID: &jobID, PlanID: &planID, CheckpointID: &boundCheckpointID, GrantID: &grantID,
			AttemptID: &attemptID, SourceLeaseID: &sourceLeaseID, NodeLeaseID: &nodeLeaseID,
			NodeLeaseFence: node.Fence, Kind: "verification", Outcome: "succeeded",
			SummaryDigest: summaryDigest, DifferenceCount: 0, VerifiedAt: timePointerValue(verifiedAt),
			CreatedAt: verifiedAt, UpdatedAt: now,
		}
		return tx.WithContext(ctx).Create(&evidence).Error
	})
	if err != nil {
		if errors.Is(err, ErrRecoveryWorkerFenceLost) || errors.Is(err, ErrRecoverySourceChanged) {
			return model.BackupAssetRecoveryEvidence{}, err
		}
		return model.BackupAssetRecoveryEvidence{}, fmt.Errorf("%w: project recovery operation verification", ErrRecoveryWorkerUnavailable)
	}
	return evidence, nil
}

// projectPendingOperationMismatch preserves the legacy focused projection
// boundary while delegating to the closed unresolved-outcome product.
func (coordinator *WorkerCoordinator) projectPendingOperationMismatch(
	ctx context.Context,
	claim RecoveryWorkerClaim,
	jobItemID string,
	writeResult TargetWriteResult,
	observation TargetVerifyObservation,
	verifiedAt time.Time,
) (model.BackupAssetRecoveryEvidence, error) {
	if coordinator == nil || coordinator.db == nil || !validRecoveryWorkerClaim(claim) ||
		!validOpaqueID(jobItemID) || verifiedAt.IsZero() {
		return model.BackupAssetRecoveryEvidence{}, ErrInvalidRecoveryWorker
	}
	handoff, err := coordinator.loadOrdinaryOperationHandoff(ctx, claim, jobItemID)
	if err != nil {
		return model.BackupAssetRecoveryEvidence{}, err
	}
	category := UnresolvedOperationVerificationMismatch
	if writeResult.TargetRevision != observation.ObservedRevision {
		category = UnresolvedOperationRevisionDisagreement
	}
	result := ordinaryOperationResult{
		writeResult: writeResult, observation: observation,
		writeResultReturned: true, observationReturned: true,
		unresolvedCategory: category,
	}
	return coordinator.projectPendingOperationUnresolved(
		ctx, claim, handoff, handoff.job.TargetChainRevision, result,
		SourceRevalidationMatched, verifiedAt,
	)
}

func (coordinator *WorkerCoordinator) projectPendingOperationUnresolved(
	ctx context.Context,
	claim RecoveryWorkerClaim,
	handoff interruptedOperationHandoff,
	expectedRevision string,
	result ordinaryOperationResult,
	sourceOutcome SourceRevalidationOutcome,
	verifiedAt time.Time,
) (model.BackupAssetRecoveryEvidence, error) {
	effectCtx := recoveryWorkerContext(ctx)
	effectCtx, cancel := context.WithTimeout(
		context.WithoutCancel(effectCtx), recoveryUnresolvedProjectionTimeout,
	)
	defer cancel()
	observedAuthority, err := coordinator.observeRecoveryAuthority(
		effectCtx, handoff.plan, handoff.preflight,
	)
	if err != nil {
		return model.BackupAssetRecoveryEvidence{}, err
	}
	return coordinator.projectPendingOperationUnresolvedOwned(
		effectCtx, claim, handoff, expectedRevision, result, sourceOutcome, verifiedAt, observedAuthority, true,
	)
}

func (coordinator *WorkerCoordinator) projectPendingOperationUnresolvedOwned(
	ctx context.Context,
	claim RecoveryWorkerClaim,
	handoff interruptedOperationHandoff,
	expectedRevision string,
	result ordinaryOperationResult,
	sourceOutcome SourceRevalidationOutcome,
	verifiedAt time.Time,
	observedAuthority observedRecoveryAuthority,
	observe bool,
) (model.BackupAssetRecoveryEvidence, error) {
	if coordinator == nil || coordinator.db == nil || coordinator.sourceValidator == nil ||
		coordinator.sourceLeases == nil || coordinator.liveRevalidator == nil || coordinator.workspaceKeys == nil ||
		!validRecoveryWorkerClaim(claim) || handoff.item.ID == "" ||
		!validOpaqueRevision(expectedRevision) || !result.unresolvedCategory.Valid() ||
		!sourceOutcome.Valid() || verifiedAt.IsZero() ||
		!validOrdinaryUnresolvedResult(handoff, result) {
		return model.BackupAssetRecoveryEvidence{}, ErrInvalidRecoveryWorker
	}
	ctx = recoveryWorkerContext(ctx)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recoveryUnresolvedProjectionTimeout)
	defer cancel()
	verifiedAt = verifiedAt.UTC()

	var evidence model.BackupAssetRecoveryEvidence
	err := coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := coordinator.now().UTC()
		if verifiedAt.After(now) {
			return ErrRecoveryWorkerFenceLost
		}
		fence, err := coordinator.lockOrdinaryExecutionTx(
			ctx, tx, claim, handoff, expectedRevision, now, observedAuthority,
		)
		if err != nil {
			return err
		}

		var checkpoints []model.BackupAssetRecoveryCheckpoint
		loaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("job_id = ?", fence.job.ID).Order("sequence ASC").Find(&checkpoints)
		if loaded.Error != nil {
			return loaded.Error
		}
		for index := range checkpoints {
			if checkpoints[index].Sequence != index {
				return ErrRecoveryWorkerFenceLost
			}
		}
		guard := CheckpointAppendGuard{
			SameAttempt: true, SameAttemptFence: true, SameNodeFence: true, MutationArmed: true,
			ExactMirror:  ConflictPolicy(fence.plan.ConflictPolicy) == ConflictExactMirror,
			NextSequence: len(checkpoints),
		}
		if len(checkpoints) == 0 {
			if !CanStartCheckpoint(CheckpointPhaseOperationUnresolved, TargetMode(fence.job.TargetMode), guard) {
				return ErrRecoveryWorkerFenceLost
			}
		} else if !(CheckpointCursor{
			Sequence: checkpoints[len(checkpoints)-1].Sequence,
			Phase:    CheckpointPhase(checkpoints[len(checkpoints)-1].Phase),
		}).CanAppend(CheckpointPhaseOperationUnresolved, guard) {
			return ErrRecoveryWorkerFenceLost
		}

		checkpointID, err := backupasset.NewOpaqueID()
		if err != nil {
			return err
		}
		checkpoint := model.BackupAssetRecoveryCheckpoint{
			ID: checkpointID, JobID: fence.job.ID, JobItemID: fence.item.ID,
			AttemptID: claim.AttemptID, Sequence: len(checkpoints),
			Phase: string(CheckpointPhaseOperationUnresolved), AuthorityCategory: string(AuthorityWrite),
			OperationDigest: handoff.operationDigest, PriorTargetRevision: expectedRevision,
			UnresolvedCategory: string(result.unresolvedCategory),
			NodeFence:          claim.NodeFence, AttemptFence: claim.AttemptFence,
			PlanBindingDigest: fence.job.PlanBindingDigest, SourceRevisionDigest: fence.job.SourceRevisionDigest,
			PreflightID: fence.job.PreflightID, PreflightRevision: fence.job.PreflightRevision,
			PreflightExpiresAt: fence.job.PreflightExpiresAt, SecurityDecision: fence.job.SecurityDecision,
			SecurityDecisionDigest:   fence.job.SecurityDecisionDigest,
			SecurityFindingSetDigest: fence.job.SecurityFindingSetDigest,
			SecurityPolicyRevision:   fence.job.SecurityPolicyRevision,
			AuthorityGrantID:         fence.job.AuthorityGrantID, JobAuthorityCategory: fence.job.AuthorityCategory,
			AuthorityBindingDigest:    fence.job.AuthorityBindingDigest,
			AuthorityExpiresAt:        fence.job.AuthorityExpiresAt,
			SourceRevalidationOutcome: string(sourceOutcome), CreatedAt: now,
		}
		populateUnresolvedCheckpointTargetFacts(&checkpoint, result)
		if err := tx.WithContext(ctx).Create(&checkpoint).Error; err != nil {
			return err
		}

		updated := tx.WithContext(ctx).Model(&model.BackupAssetRecoveryJobItem{}).
			Where("id = ? AND job_id = ? AND outcome = '' AND failure_category = ''", fence.item.ID, fence.job.ID).
			Updates(map[string]any{
				"outcome":          "failed",
				"failure_category": recoveryRemoteOutcomeUnresolvedFailureCategory, "updated_at": now,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrRecoveryWorkerFenceLost
		}

		summaryDigest := recoveryUnresolvedOutcomeSummaryDigest(claim, checkpoint, fence.item, verifiedAt)
		evidenceID, err := backupasset.NewOpaqueID()
		if err != nil {
			return err
		}
		jobID, planID, checkpointIDValue, grantID :=
			fence.job.ID, fence.plan.ID, checkpoint.ID, fence.job.AuthorityGrantID
		attemptID, sourceLeaseID, nodeLeaseID := claim.AttemptID, claim.SourceFence.LeaseID, claim.NodeLeaseID
		evidence = model.BackupAssetRecoveryEvidence{
			ID: evidenceID, JobID: &jobID, PlanID: &planID, CheckpointID: &checkpointIDValue, GrantID: &grantID,
			AttemptID: &attemptID, SourceLeaseID: &sourceLeaseID, NodeLeaseID: &nodeLeaseID,
			NodeLeaseFence: claim.NodeFence, Kind: "failure", Outcome: "needs_attention",
			SummaryDigest: summaryDigest, DifferenceCount: 0, VerifiedAt: timePointerValue(verifiedAt),
			CreatedAt: verifiedAt, UpdatedAt: now,
		}
		if err := tx.WithContext(ctx).Create(&evidence).Error; err != nil {
			return err
		}

		updated = tx.WithContext(ctx).Model(&model.BackupAssetRecoveryAttempt{}).
			Where(`id = ? AND job_id = ? AND owner_id = ? AND fence = ? AND state = ?
				AND mutation_armed = ? AND lease_expires_at > ?`,
				claim.AttemptID, claim.JobID, claim.WorkerID, claim.AttemptFence,
				AttemptStateRunning, true, now).
			Updates(map[string]any{"state": string(AttemptStateFailed), "closed_at": now, "updated_at": now})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrRecoveryWorkerFenceLost
		}
		if err := coordinator.sourceLeases.ReleaseTx(ctx, tx, claim.SourceFence); err != nil {
			return recoveryWorkerSourceError(err)
		}
		updated = tx.WithContext(ctx).Model(&model.BackupAssetRecoveryNodeLease{}).
			Where("id = ? AND job_id = ? AND attempt_id = ? AND owner_id = ? AND fence = ? AND state = ?",
				claim.NodeLeaseID, claim.JobID, claim.AttemptID, claim.WorkerID, claim.NodeFence, "active").
			Updates(map[string]any{"state": "released", "released_at": now, "updated_at": now})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrRecoveryWorkerFenceLost
		}

		nextWorkspacePhase := string(WorkspacePhaseNone)
		if TargetMode(fence.job.TargetMode) == TargetModeIsolated {
			nextWorkspacePhase = string(WorkspacePhaseCleanupDue)
		}
		updated = tx.WithContext(ctx).Model(&model.BackupAssetRecoveryJob{}).
			Where(`id = ? AND state = ? AND failure_category = '' AND transition_revision = ?
				AND workspace_phase = ? AND target_chain_revision = ?`,
				fence.job.ID, JobStateRunning, fence.job.TransitionRevision,
				fence.job.WorkspacePhase, expectedRevision).
			Updates(map[string]any{
				"state":               string(JobStateNeedsAttention),
				"failure_category":    recoveryRemoteOutcomeUnresolvedFailureCategory,
				"transition_revision": fence.job.TransitionRevision + 1,
				"workspace_phase":     nextWorkspacePhase, "updated_at": now,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrRecoveryWorkerFenceLost
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrRecoveryWorkerFenceLost) || errors.Is(err, ErrRecoverySourceChanged) {
			return model.BackupAssetRecoveryEvidence{}, err
		}
		return model.BackupAssetRecoveryEvidence{}, fmt.Errorf(
			"%w: project unresolved recovery operation", ErrRecoveryWorkerUnavailable,
		)
	}
	if observe {
		coordinator.observeJobOutcome(ctx, claim.JobID, JobStateNeedsAttention)
	}
	return evidence, nil
}

func validOrdinaryUnresolvedResult(
	handoff interruptedOperationHandoff,
	result ordinaryOperationResult,
) bool {
	writeEmpty := result.writeResult == (TargetWriteResult{})
	observationEmpty := result.observation.Kind == "" && result.observation.Present == nil &&
		result.observation.Absent == nil && result.observation.ObservedRevision == ""
	if (result.writeResultReturned && result.writeCallFailed) ||
		(result.observationReturned && result.observationCallFailed) ||
		(result.adoptionNoWrite && (result.writeResultReturned || result.writeCallFailed)) ||
		(!result.writeResultReturned && !writeEmpty) ||
		(!result.observationReturned && !observationEmpty) {
		return false
	}
	writeValid := validOrdinaryWriteResult(handoff, result.writeResult)
	observationValid := result.observation.Validate() == nil
	noWriteCall := !result.writeResultReturned && !result.writeCallFailed
	noObservationCall := !result.observationReturned && !result.observationCallFailed
	mutatingOperation := handoff.operation.Kind == RecoveryOperationCreate ||
		handoff.operation.Kind == RecoveryOperationOverwrite ||
		handoff.operation.Kind == RecoveryOperationDelete
	noWriteExpected := handoff.operation.Kind == RecoveryOperationSkip ||
		(handoff.operation.Kind == RecoveryOperationDelete && handoff.deleteAuthorityConsumed) ||
		(result.adoptionNoWrite && mutatingOperation)
	switch result.unresolvedCategory {
	case UnresolvedOperationWriteResultInvalid:
		return !result.adoptionNoWrite && noObservationCall &&
			((result.writeCallFailed && writeEmpty) ||
				(result.writeResultReturned && !result.writeCallFailed && !writeValid))
	case UnresolvedOperationObservationInvalid:
		return ((result.observationCallFailed && observationEmpty) ||
			(result.observationReturned && !result.observationCallFailed && !observationValid)) &&
			((noWriteCall && noWriteExpected) ||
				(result.writeResultReturned && !result.writeCallFailed && writeValid))
	case UnresolvedOperationRevisionDisagreement:
		return !result.adoptionNoWrite && !result.writeCallFailed && !result.observationCallFailed &&
			result.writeResultReturned && writeValid && result.observationReturned && observationValid &&
			result.writeResult.TargetRevision != result.observation.ObservedRevision
	case UnresolvedOperationVerificationMismatch:
		return !result.writeCallFailed && !result.observationCallFailed &&
			((noWriteCall && noWriteExpected) || (result.writeResultReturned && writeValid)) &&
			result.observationReturned && observationValid &&
			(noWriteCall || result.writeResult.TargetRevision == result.observation.ObservedRevision) &&
			result.observation.ValidateAgainst(handoff.expectation) != nil
	default:
		return false
	}
}

func validOrdinaryWriteResult(handoff interruptedOperationHandoff, result TargetWriteResult) bool {
	if !validOpaqueRevision(result.TargetRevision) {
		return false
	}
	switch handoff.operation.Kind {
	case RecoveryOperationCreate, RecoveryOperationOverwrite:
		return result.BytesWritten == handoff.item.ExpectedPostBytes &&
			result.IdentityDigest == handoff.item.ExpectedPostIdentityDigest
	case RecoveryOperationDelete:
		return result.BytesWritten == 0 && result.IdentityDigest == ""
	default:
		return false
	}
}

func populateUnresolvedCheckpointTargetFacts(
	checkpoint *model.BackupAssetRecoveryCheckpoint,
	result ordinaryOperationResult,
) {
	if result.writeResultReturned {
		checkpoint.WriteResultDigest = framedDigest(
			"xirang/recovery/unresolved-write-result/v1",
			strconv.FormatInt(result.writeResult.BytesWritten, 10),
			result.writeResult.IdentityDigest,
			result.writeResult.TargetRevision,
		)
		if result.unresolvedCategory != UnresolvedOperationWriteResultInvalid {
			checkpoint.WriteTargetRevision = result.writeResult.TargetRevision
		}
	}
	if result.observationReturned {
		presentIdentity, presentBytes, absentEvidence := "", "", ""
		if result.observation.Present != nil {
			presentIdentity = result.observation.Present.IdentityDigest
			presentBytes = strconv.FormatInt(result.observation.Present.Bytes, 10)
		}
		if result.observation.Absent != nil {
			absentEvidence = string(result.observation.Absent.Evidence)
		}
		checkpoint.ObservationDigest = framedDigest(
			"xirang/recovery/unresolved-observation/v1",
			string(result.observation.Kind), presentIdentity, presentBytes,
			absentEvidence, result.observation.ObservedRevision,
		)
		if result.unresolvedCategory != UnresolvedOperationObservationInvalid {
			checkpoint.ObservedTargetRevision = result.observation.ObservedRevision
			checkpoint.ObservedPresence = string(result.observation.Kind)
		}
	}
}

func recoveryUnresolvedOutcomeSummaryDigest(
	claim RecoveryWorkerClaim,
	checkpoint model.BackupAssetRecoveryCheckpoint,
	item model.BackupAssetRecoveryJobItem,
	verifiedAt time.Time,
) string {
	return framedDigest(
		"xirang/recovery/unresolved-outcome-evidence/v1",
		claim.JobID, claim.AttemptID, claim.NodeLeaseID,
		strconv.FormatUint(claim.AttemptFence, 10), strconv.FormatUint(claim.NodeFence, 10),
		checkpoint.ID, checkpoint.JobItemID, checkpoint.OperationDigest,
		checkpoint.PriorTargetRevision, checkpoint.UnresolvedCategory,
		checkpoint.WriteResultDigest, checkpoint.WriteTargetRevision,
		checkpoint.ObservationDigest, checkpoint.ObservedTargetRevision,
		checkpoint.ObservedPresence, checkpoint.SourceRevalidationOutcome,
		item.ID, verifiedAt.UTC().Format(time.RFC3339Nano),
	)
}

// ProjectOperationMismatch records a verified mismatch as a terminal
// needs-attention outcome. It closes the current attempt and releases both
// write leases in the same transaction, so the previous claim cannot issue a
// later target mutation after the inconsistent observation.
func (coordinator *WorkerCoordinator) ProjectOperationMismatch(
	ctx context.Context,
	claim RecoveryWorkerClaim,
	jobItemID string,
	checkpointID string,
	bytesWritten int64,
	verifiedSize int64,
	verifiedDigest string,
	verifiedAt time.Time,
	differenceCount int64,
) (model.BackupAssetRecoveryEvidence, error) {
	if coordinator == nil || coordinator.db == nil || coordinator.sourceValidator == nil ||
		coordinator.sourceLeases == nil || coordinator.liveRevalidator == nil ||
		!validRecoveryWorkerClaim(claim) || !validOpaqueID(jobItemID) || !validOpaqueID(checkpointID) ||
		bytesWritten < 0 || verifiedSize < 0 || !validDigest(verifiedDigest) || verifiedAt.IsZero() ||
		differenceCount <= 0 {
		return model.BackupAssetRecoveryEvidence{}, ErrInvalidRecoveryWorker
	}
	ctx = recoveryWorkerContext(ctx)
	if err := ctx.Err(); err != nil {
		return model.BackupAssetRecoveryEvidence{}, err
	}
	verifiedAt = verifiedAt.UTC()
	observedAuthority, err := coordinator.observeRecoveryAuthorityForJob(ctx, claim.JobID)
	if err != nil {
		if errors.Is(err, ErrRecoveryWorkerFenceLost) || errors.Is(err, ErrRecoverySourceChanged) {
			return model.BackupAssetRecoveryEvidence{}, err
		}
		return model.BackupAssetRecoveryEvidence{}, fmt.Errorf(
			"%w: observe recovery verification mismatch authority", ErrRecoveryWorkerUnavailable,
		)
	}

	var evidence model.BackupAssetRecoveryEvidence
	err = coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := coordinator.now().UTC()
		if verifiedAt.After(now) {
			return ErrRecoveryWorkerFenceLost
		}
		var jobReference struct {
			PlanID string `gorm:"column:plan_id"`
		}
		loaded := tx.WithContext(ctx).Table((model.BackupAssetRecoveryJob{}).TableName()).
			Select("plan_id").Where("id = ?", claim.JobID).Limit(1).Find(&jobReference)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || !validOpaqueID(jobReference.PlanID) {
			return ErrRecoveryWorkerFenceLost
		}
		var plan model.BackupAssetRecoveryPlan
		loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ?", jobReference.PlanID).Limit(1).Find(&plan)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || PlanState(plan.State) != PlanStateExecuted {
			return ErrRecoveryWorkerFenceLost
		}
		var job model.BackupAssetRecoveryJob
		loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ? AND plan_id = ?", claim.JobID, plan.ID).Limit(1).Find(&job)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || job.PlanID != jobReference.PlanID ||
			job.State != string(JobStateRunning) || job.TransitionRevision != claim.TransitionRevision ||
			job.WorkspacePhase != string(WorkspacePhaseWriting) {
			return ErrRecoveryWorkerFenceLost
		}
		var preflight model.BackupAssetRecoveryPreflight
		loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ? AND plan_id = ?", job.PreflightID, plan.ID).Limit(1).Find(&preflight)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 {
			return ErrRecoveryWorkerFenceLost
		}
		var grant model.BackupAssetRecoveryGrant
		loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ? AND plan_id = ?", job.AuthorityGrantID, plan.ID).Limit(1).Find(&grant)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || !validRecoveryJobBinding(plan, job, preflight, grant, now, false) {
			return ErrRecoveryWorkerFenceLost
		}
		if err := coordinator.sourceValidator.RevalidatePlanTx(ctx, tx, plan); err != nil {
			return err
		}
		if err := coordinator.revalidateObservedRecoveryAuthorityTx(
			ctx, tx, plan, preflight, observedAuthority,
		); err != nil {
			return err
		}

		var source model.RecoveryPointLease
		loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ?", claim.SourceFence.LeaseID).Limit(1).Find(&source)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || !matchesCurrentRecoverySourceFence(source, claim.SourceFence, plan.RecoveryPointID, now) {
			return ErrRecoveryWorkerFenceLost
		}
		var node model.BackupAssetRecoveryNodeLease
		loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ?", claim.NodeLeaseID).Limit(1).Find(&node)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || !matchesCurrentRecoveryNodeFence(node, claim, job, now) {
			return ErrRecoveryWorkerFenceLost
		}
		var attempt model.BackupAssetRecoveryAttempt
		loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ? AND job_id = ?", claim.AttemptID, job.ID).Limit(1).Find(&attempt)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || !attempt.MutationArmed || !matchesCurrentRecoveryAttemptFence(attempt, claim, now) {
			return ErrRecoveryWorkerFenceLost
		}
		var item model.BackupAssetRecoveryJobItem
		loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ? AND job_id = ? AND plan_id = ?", jobItemID, job.ID, plan.ID).Limit(1).Find(&item)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || item.Outcome != "succeeded" || item.FailureCategory != "" ||
			item.BytesWritten != 0 || item.VerifiedSize != 0 || item.VerifiedDigest != "" {
			return ErrRecoveryWorkerFenceLost
		}
		var checkpoint model.BackupAssetRecoveryCheckpoint
		loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ? AND job_id = ? AND attempt_id = ?", checkpointID, job.ID, claim.AttemptID).
			Limit(1).Find(&checkpoint)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || checkpoint.Phase != string(CheckpointPhaseOperation) ||
			checkpoint.AttemptFence != claim.AttemptFence || checkpoint.NodeFence != claim.NodeFence ||
			checkpoint.OperationDigest != recoveryJobItemOperationDigest(item) ||
			checkpoint.NextTargetRevision != job.TargetChainRevision || checkpoint.PriorTargetRevision == "" {
			return ErrRecoveryWorkerFenceLost
		}

		summaryDigest := recoveryVerificationMismatchSummaryDigest(
			claim, checkpoint, item, bytesWritten, verifiedSize, verifiedDigest, verifiedAt, differenceCount,
		)
		updated := tx.WithContext(ctx).Model(&model.BackupAssetRecoveryJobItem{}).
			Where("id = ? AND job_id = ? AND outcome = ? AND bytes_written = ? AND verified_size = ? AND verified_digest = ? AND failure_category = ?",
				item.ID, job.ID, "succeeded", 0, 0, "", "").
			Updates(map[string]any{
				"outcome": "failed", "bytes_written": bytesWritten, "verified_size": verifiedSize,
				"verified_digest": verifiedDigest, "failure_category": recoveryVerificationMismatchFailureCategory,
				"updated_at": now,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrRecoveryWorkerFenceLost
		}
		evidenceID, err := backupasset.NewOpaqueID()
		if err != nil {
			return err
		}
		jobID, planID, boundCheckpointID, grantID := job.ID, plan.ID, checkpoint.ID, grant.ID
		attemptID, sourceLeaseID, nodeLeaseID := attempt.ID, source.ID, node.ID
		evidence = model.BackupAssetRecoveryEvidence{
			ID: evidenceID, JobID: &jobID, PlanID: &planID, CheckpointID: &boundCheckpointID, GrantID: &grantID,
			AttemptID: &attemptID, SourceLeaseID: &sourceLeaseID, NodeLeaseID: &nodeLeaseID,
			NodeLeaseFence: node.Fence, Kind: "difference", Outcome: "needs_attention",
			SummaryDigest: summaryDigest, DifferenceCount: differenceCount, VerifiedAt: timePointerValue(verifiedAt),
			CreatedAt: verifiedAt, UpdatedAt: now,
		}
		if err := tx.WithContext(ctx).Create(&evidence).Error; err != nil {
			return err
		}
		updated = tx.WithContext(ctx).Model(&model.BackupAssetRecoveryAttempt{}).
			Where("id = ? AND job_id = ? AND owner_id = ? AND fence = ? AND state = ? AND mutation_armed = ?",
				attempt.ID, job.ID, claim.WorkerID, claim.AttemptFence, AttemptStateRunning, true).
			Updates(map[string]any{"state": string(AttemptStateFailed), "closed_at": now, "updated_at": now})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrRecoveryWorkerFenceLost
		}
		if err := coordinator.sourceLeases.ReleaseTx(ctx, tx, recoverySourceFence(source)); err != nil {
			return recoveryWorkerSourceError(err)
		}
		updated = tx.WithContext(ctx).Model(&model.BackupAssetRecoveryNodeLease{}).
			Where("id = ? AND job_id = ? AND attempt_id = ? AND owner_id = ? AND fence = ? AND state = ?",
				node.ID, job.ID, claim.AttemptID, claim.WorkerID, claim.NodeFence, "active").
			Updates(map[string]any{"state": "released", "released_at": now, "updated_at": now})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrRecoveryWorkerFenceLost
		}
		updated = tx.WithContext(ctx).Model(&model.BackupAssetRecoveryJob{}).
			Where("id = ? AND state = ? AND transition_revision = ? AND workspace_phase = ? AND target_chain_revision = ?",
				job.ID, JobStateRunning, job.TransitionRevision, WorkspacePhaseWriting, checkpoint.NextTargetRevision).
			Updates(map[string]any{
				"state": string(JobStateNeedsAttention), "failure_category": recoveryVerificationMismatchFailureCategory,
				"transition_revision": job.TransitionRevision + 1, "workspace_phase": string(WorkspacePhaseCleanupDue),
				"updated_at": now,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrRecoveryWorkerFenceLost
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrRecoveryWorkerFenceLost) || errors.Is(err, ErrRecoverySourceChanged) {
			return model.BackupAssetRecoveryEvidence{}, err
		}
		return model.BackupAssetRecoveryEvidence{}, fmt.Errorf("%w: project recovery verification mismatch", ErrRecoveryWorkerUnavailable)
	}
	coordinator.observeJobOutcome(ctx, claim.JobID, JobStateNeedsAttention)
	return evidence, nil
}

func (coordinator *WorkerCoordinator) supersedePreWriteDriftTx(
	ctx context.Context,
	tx *gorm.DB,
	plan model.BackupAssetRecoveryPlan,
	job model.BackupAssetRecoveryJob,
	attempt model.BackupAssetRecoveryAttempt,
	source model.RecoveryPointLease,
	node model.BackupAssetRecoveryNodeLease,
	claim RecoveryWorkerClaim,
	now time.Time,
) error {
	if !canSupersedeBeforeFirstWrite(plan, attempt, 0) {
		return ErrRecoveryWorkerFenceLost
	}

	planUpdate := tx.WithContext(ctx).Model(&model.BackupAssetRecoveryPlan{}).
		Where("id = ? AND state = ? AND transition_revision = ?", plan.ID, PlanStateExecuted, plan.TransitionRevision).
		Updates(map[string]any{
			"state": string(PlanStateSuperseded), "transition_revision": plan.TransitionRevision + 1, "updated_at": now,
		})
	if planUpdate.Error != nil {
		return planUpdate.Error
	}
	if planUpdate.RowsAffected != 1 {
		return ErrRecoveryWorkerFenceLost
	}

	attemptUpdate := tx.WithContext(ctx).Model(&model.BackupAssetRecoveryAttempt{}).
		Where("id = ? AND job_id = ? AND owner_id = ? AND fence = ? AND state = ? AND mutation_armed = ?",
			attempt.ID, job.ID, claim.WorkerID, claim.AttemptFence, AttemptStateRunning, false).
		Updates(map[string]any{"state": string(AttemptStateSuperseded), "closed_at": now, "updated_at": now})
	if attemptUpdate.Error != nil {
		return attemptUpdate.Error
	}
	if attemptUpdate.RowsAffected != 1 {
		return ErrRecoveryWorkerFenceLost
	}

	jobUpdate := tx.WithContext(ctx).Model(&model.BackupAssetRecoveryJob{}).
		Where("id = ? AND state = ? AND transition_revision = ? AND workspace_phase = ?", job.ID, JobStateRunning, job.TransitionRevision, WorkspacePhaseNone).
		Updates(map[string]any{
			"state": string(JobStateFailed), "failure_category": recoveryPreWriteDriftFailureCategory,
			"transition_revision": job.TransitionRevision + 1, "updated_at": now,
		})
	if jobUpdate.Error != nil {
		return jobUpdate.Error
	}
	if jobUpdate.RowsAffected != 1 {
		return ErrRecoveryWorkerFenceLost
	}

	grants := tx.WithContext(ctx).Model(&model.BackupAssetRecoveryGrant{}).
		Where("plan_id = ? AND consumed_at IS NULL AND revoked_at IS NULL", plan.ID).
		Updates(map[string]any{"revoked_at": now, "updated_at": now})
	if grants.Error != nil {
		return grants.Error
	}

	if err := coordinator.sourceLeases.ReleaseTx(ctx, tx, recoverySourceFence(source)); err != nil {
		return recoveryWorkerSourceError(err)
	}
	nodeUpdate := tx.WithContext(ctx).Model(&model.BackupAssetRecoveryNodeLease{}).
		Where("id = ? AND job_id = ? AND attempt_id = ? AND owner_id = ? AND fence = ? AND state = ?",
			node.ID, job.ID, claim.AttemptID, claim.WorkerID, claim.NodeFence, "active").
		Updates(map[string]any{"state": "released", "released_at": now, "updated_at": now})
	if nodeUpdate.Error != nil {
		return nodeUpdate.Error
	}
	if nodeUpdate.RowsAffected != 1 {
		return ErrRecoveryWorkerFenceLost
	}
	return nil
}

func (coordinator *WorkerCoordinator) validateFirstWritePermitAt(
	claim RecoveryWorkerClaim,
	permit TargetMutationPermit,
	now time.Time,
) error {
	if coordinator == nil || coordinator.db == nil || now.IsZero() {
		return ErrInvalidTargetPermit
	}
	ctx := context.Background()
	var latch model.BackupAssetRecoveryEvidence
	loaded := coordinator.db.WithContext(ctx).Where("id = ?", recoverySchemaUseLatchRowID).Limit(1).Find(&latch)
	if loaded.Error != nil || loaded.RowsAffected != 1 || !validRecoverySchemaUseLatch(latch) {
		return ErrInvalidTargetPermit
	}

	var job model.BackupAssetRecoveryJob
	loaded = coordinator.db.WithContext(ctx).Where("id = ?", claim.JobID).Limit(1).Find(&job)
	if loaded.Error != nil || loaded.RowsAffected != 1 || job.State != string(JobStateRunning) ||
		job.TransitionRevision != claim.TransitionRevision || job.TargetNodeID != permit.NodeID ||
		job.TargetRootID != permit.RootID || job.RootLocatorDigest != permit.RootLocatorDigest ||
		job.TargetChainRevision != permit.ExpectedTargetRevision {
		return ErrInvalidTargetPermit
	}
	if TargetMode(job.TargetMode) == TargetModeIsolated {
		workspacePathDigest, err := TargetPathDigest(job.TargetRootID, job.RootLocatorDigest, job.EncryptedWorkspaceRelativeLocator)
		itemPermit := currentRecoveryItemPermitPath(coordinator.db, job.ID, permit.TargetPathDigest)
		workspacePermit := err == nil && workspacePathDigest == permit.TargetPathDigest
		workspacePhase := WorkspacePhase(job.WorkspacePhase)
		currentMarkerValidation := recoveryMarkerValidationMatchesClaim(job, claim)
		validMarkerShape := (workspacePhase == WorkspacePhaseReserved && emptyRecoveryMarkerValidation(job)) ||
			((workspacePhase == WorkspacePhaseMarkerCreated || workspacePhase == WorkspacePhaseWriting) &&
				validRecoveryMarkerValidation(job))
		validWorkspacePermit := workspacePermit &&
			((workspacePhase == WorkspacePhaseReserved && emptyRecoveryMarkerValidation(job)) ||
				(workspacePhase == WorkspacePhaseMarkerCreated && currentMarkerValidation) ||
				(workspacePhase == WorkspacePhaseWriting && validRecoveryMarkerValidation(job)))
		validItemPermit := itemPermit &&
			((workspacePhase == WorkspacePhaseMarkerCreated && currentMarkerValidation) ||
				(workspacePhase == WorkspacePhaseWriting && validRecoveryMarkerValidation(job)))
		if err != nil || (!validWorkspacePermit && !validItemPermit) ||
			!validMarkerShape ||
			!validDigest(job.WorkspaceMarkerBindingDigest) || !validRecoveryWorkerID(job.WorkspaceOwner) ||
			job.WorkspaceFence == 0 || job.PlaintextDeadline == nil ||
			!job.PlaintextDeadline.After(now) {
			return ErrInvalidTargetPermit
		}
	} else if TargetMode(job.TargetMode) != TargetModeInPlace ||
		(job.PathDigest != permit.TargetPathDigest &&
			!currentRecoveryItemPermitPath(coordinator.db, job.ID, permit.TargetPathDigest)) {
		return ErrInvalidTargetPermit
	}

	var plan struct {
		ID    string `gorm:"column:id"`
		State string `gorm:"column:state"`
	}
	loaded = coordinator.db.WithContext(ctx).Table((model.BackupAssetRecoveryPlan{}).TableName()).
		Select("id", "state").Where("id = ? AND state = ?", job.PlanID, PlanStateExecuted).Limit(1).Find(&plan)
	if loaded.Error != nil || loaded.RowsAffected != 1 || plan.ID != job.PlanID || plan.State != string(PlanStateExecuted) {
		return ErrInvalidTargetPermit
	}

	var attempt model.BackupAssetRecoveryAttempt
	loaded = coordinator.db.WithContext(ctx).Where("id = ?", claim.AttemptID).Limit(1).Find(&attempt)
	if loaded.Error != nil || loaded.RowsAffected != 1 || !attempt.MutationArmed ||
		!matchesCurrentRecoveryAttemptFence(attempt, claim, now) {
		return ErrInvalidTargetPermit
	}

	var node model.BackupAssetRecoveryNodeLease
	loaded = coordinator.db.WithContext(ctx).Where("id = ?", claim.NodeLeaseID).Limit(1).Find(&node)
	if loaded.Error != nil || loaded.RowsAffected != 1 || !matchesCurrentRecoveryNodeFence(node, claim, job, now) {
		return ErrInvalidTargetPermit
	}

	var source model.RecoveryPointLease
	loaded = coordinator.db.WithContext(ctx).Where("id = ?", claim.SourceFence.LeaseID).Limit(1).Find(&source)
	if loaded.Error != nil || loaded.RowsAffected != 1 ||
		!matchesCurrentRecoverySourceFence(source, claim.SourceFence, claim.SourceFence.RecoveryPointID, now) {
		return ErrInvalidTargetPermit
	}
	return nil
}

func currentRecoveryItemPermitPath(db *gorm.DB, jobID, targetObjectDigest string) bool {
	if db == nil || !validOpaqueID(jobID) || !validDigest(targetObjectDigest) {
		return false
	}
	var count int64
	result := db.Model(&model.BackupAssetRecoveryJobItem{}).
		Where("job_id = ? AND target_object_digest = ? AND outcome = '' AND failure_category = ''",
			jobID, targetObjectDigest).
		Count(&count)
	return result.Error == nil && count == 1
}

func ensureRecoverySchemaUseLatchTx(ctx context.Context, tx *gorm.DB, now time.Time) error {
	var latch model.BackupAssetRecoveryEvidence
	loaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ?", recoverySchemaUseLatchRowID).Limit(1).Find(&latch)
	if loaded.Error != nil {
		return loaded.Error
	}
	if loaded.RowsAffected == 1 {
		if !validRecoverySchemaUseLatch(latch) {
			return ErrRecoveryWorkerFenceLost
		}
		return nil
	}
	latch = model.BackupAssetRecoveryEvidence{
		ID: recoverySchemaUseLatchRowID, Kind: RecoverySchemaUseLatchID, CreatedAt: now, UpdatedAt: now,
	}
	return tx.WithContext(ctx).Create(&latch).Error
}

func validRecoverySchemaUseLatch(latch model.BackupAssetRecoveryEvidence) bool {
	return latch.ID == recoverySchemaUseLatchRowID && latch.Kind == RecoverySchemaUseLatchID &&
		latch.JobID == nil && latch.Outcome == "" && latch.SummaryDigest == "" && latch.DifferenceCount == 0 &&
		latch.VerifiedAt == nil && latch.PlanID == nil && latch.CheckpointID == nil && latch.GrantID == nil &&
		latch.AttemptID == nil && latch.SourceLeaseID == nil && latch.NodeLeaseID == nil && latch.RequesterID == nil &&
		latch.Operation == "" && latch.Category == "" && latch.Endpoint == "" && latch.IdempotencyKeyDigest == "" &&
		latch.IntentDigest == "" && latch.StepUpJTIDigest == "" && latch.PresentingSessionDigest == "" &&
		latch.PresentingSessionUserID == nil && latch.PresentingSessionRole == "" &&
		latch.PresentingSessionTokenVersion == 0 && latch.ProofExpiresAt == nil &&
		latch.PresentingSessionExpiresAt == nil && latch.ReplayExpiresAt == nil &&
		latch.ExpectedPlanTransitionRevision == 0 && latch.ResultPlanTransitionRevision == 0 &&
		latch.GrantBindingDigest == "" && latch.SourceLeaseBindingDigest == "" && latch.NodeLeaseFence == 0
}

func recoveryVerificationMismatchSummaryDigest(
	claim RecoveryWorkerClaim,
	checkpoint model.BackupAssetRecoveryCheckpoint,
	item model.BackupAssetRecoveryJobItem,
	bytesWritten int64,
	verifiedSize int64,
	verifiedDigest string,
	verifiedAt time.Time,
	differenceCount int64,
) string {
	return framedDigest(
		"xirang/recovery/verification-mismatch/v1",
		claim.JobID,
		claim.AttemptID,
		claim.NodeLeaseID,
		strconv.FormatUint(claim.AttemptFence, 10),
		strconv.FormatUint(claim.NodeFence, 10),
		checkpoint.ID,
		checkpoint.OperationDigest,
		checkpoint.PriorTargetRevision,
		checkpoint.NextTargetRevision,
		item.ID,
		strconv.FormatInt(bytesWritten, 10),
		strconv.FormatInt(verifiedSize, 10),
		verifiedDigest,
		verifiedAt.UTC().Format(time.RFC3339Nano),
		strconv.FormatInt(differenceCount, 10),
	)
}

func validRecoveryJobBinding(
	plan model.BackupAssetRecoveryPlan,
	job model.BackupAssetRecoveryJob,
	preflight model.BackupAssetRecoveryPreflight,
	grant model.BackupAssetRecoveryGrant,
	now time.Time,
	requireBaseTargetRevision bool,
) bool {
	if validateAuthorizationPreflightBindings(plan, preflight, now) != nil || job.PlanID != plan.ID ||
		job.PlanBindingDigest != plan.BindingDigest || job.SelectionDigest != plan.SelectionDigest ||
		job.SourceRevisionDigest != plan.SourceRevisionDigest || job.PreflightID != preflight.ID ||
		job.PreflightRevision != preflight.Revision || !job.PreflightExpiresAt.Equal(preflight.ExpiresAt) ||
		job.PreflightTargetRevision != preflight.TargetRevision || job.PreflightNodeRevision != preflight.NodeRevision ||
		job.CapabilityRevision != preflight.CapabilityRevision || job.OperationSetDigest != preflight.OperationSetDigest ||
		job.DeleteSetDigest != preflight.DeleteSetDigest || job.SecurityDecision != plan.SecurityDecision ||
		job.SecurityDecisionDigest != plan.SecurityDecisionDigest ||
		job.SecurityFindingSetDigest != plan.SecurityFindingSetDigest ||
		job.SecurityPolicyRevision != plan.SecurityPolicyRevision ||
		job.SecurityOverrideBindingDigest != plan.SecurityOverrideBindingDigest ||
		job.TargetMode != plan.TargetMode || job.TargetNodeID != plan.TargetNodeID ||
		job.TargetRootID != plan.TargetRootID || job.RootLocatorDigest != plan.RootLocatorDigest ||
		job.PathDigest != plan.PathDigest || !validOpaqueRevision(job.TargetChainRevision) ||
		job.AuthorityGrantID != grant.ID || job.AuthorityCategory != string(AuthorityWrite) ||
		job.AuthorityBindingDigest != grant.BindingDigest || !job.AuthorityExpiresAt.Equal(grant.ExpiresAt) ||
		grant.AuthorityCategory != string(AuthorityWrite) || grant.ConsumedAt == nil || grant.RevokedAt != nil ||
		!grant.ConsumedAt.Equal(job.AuthorityConsumedAt) || !now.Before(job.AuthorityExpiresAt.UTC()) ||
		TargetMode(job.TargetMode).Validate() != nil {
		return false
	}
	return !requireBaseTargetRevision || job.TargetChainRevision == preflight.TargetRevision
}

func matchesCurrentRecoverySourceFence(
	source model.RecoveryPointLease,
	fence backupasset.LeaseFence,
	recoveryPointID string,
	now time.Time,
) bool {
	return source.ID == fence.LeaseID && source.RecoveryPointID == recoveryPointID &&
		source.RecoveryPointID == fence.RecoveryPointID && source.HolderType == string(backupasset.LeaseHolderRecoveryJob) &&
		source.OwnerID == fence.OwnerID && source.AttemptID == fence.AttemptID &&
		source.FenceToken == fence.FenceToken && source.Status == string(backupasset.LeaseActive) &&
		now.Before(source.LeaseExpiresAt.UTC()) && now.Before(source.AbsoluteDeadline.UTC())
}

func matchesCurrentRecoveryNodeFence(
	node model.BackupAssetRecoveryNodeLease,
	claim RecoveryWorkerClaim,
	job model.BackupAssetRecoveryJob,
	now time.Time,
) bool {
	return node.ID == claim.NodeLeaseID && node.JobID == job.ID && node.NodeID == job.TargetNodeID &&
		node.AttemptID != nil && *node.AttemptID == claim.AttemptID && node.OwnerID == claim.WorkerID &&
		node.Fence == claim.NodeFence && node.State == "active" && now.Before(node.LeaseExpiresAt.UTC())
}

func matchesCurrentRecoveryAttemptFence(
	attempt model.BackupAssetRecoveryAttempt,
	claim RecoveryWorkerClaim,
	now time.Time,
) bool {
	return attempt.ID == claim.AttemptID && attempt.JobID == claim.JobID && attempt.OwnerID == claim.WorkerID &&
		attempt.Fence == claim.AttemptFence && attempt.State == string(AttemptStateRunning) &&
		attempt.LeaseExpiresAt != nil && now.Before(attempt.LeaseExpiresAt.UTC())
}

func canSupersedeBeforeFirstWrite(
	plan model.BackupAssetRecoveryPlan,
	attempt model.BackupAssetRecoveryAttempt,
	checkpointCount int64,
) bool {
	return PlanState(plan.State).CanTransitionTo(PlanStateSuperseded, PlanTransitionGuard{
		HasDurableJob: true, HasCurrentFence: true, MutationArmed: attempt.MutationArmed,
		HasCheckpoint: checkpointCount != 0, TargetAtBaseRevision: true,
	})
}

func (coordinator *WorkerCoordinator) currentFirstWritePermitPathTx(
	ctx context.Context,
	tx *gorm.DB,
	plan model.BackupAssetRecoveryPlan,
	preflight model.BackupAssetRecoveryPreflight,
	job model.BackupAssetRecoveryJob,
	attempt model.BackupAssetRecoveryAttempt,
	claim RecoveryWorkerClaim,
	checkpointCount int64,
	reconciledOverwriteCheckpointID string,
	now time.Time,
) (string, error) {
	if !attempt.MutationArmed {
		return "", ErrRecoveryWorkerFenceLost
	}
	switch TargetMode(job.TargetMode) {
	case TargetModeIsolated:
		if coordinator == nil || coordinator.workspaceKeys == nil || checkpointCount < 1 {
			return "", ErrRecoveryWorkerFenceLost
		}
		var checkpoints []model.BackupAssetRecoveryCheckpoint
		loaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("job_id = ?", job.ID).Order("sequence ASC").Find(&checkpoints)
		if loaded.Error != nil {
			return "", loaded.Error
		}
		if int64(len(checkpoints)) != checkpointCount {
			return "", ErrRecoveryWorkerFenceLost
		}
		reservationOnly := len(checkpoints) == 1
		var operations []ordinaryCheckpointOperation
		var markerKeyVersion int
		var err error
		if reservationOnly {
			operations, markerKeyVersion, err = loadFreshIsolatedCheckpointOperationsTx(ctx, tx, plan, job)
		} else {
			operations, markerKeyVersion, err = loadIsolatedOrdinaryCheckpointOperationsTx(
				ctx, tx, plan, preflight, job,
			)
		}
		if err != nil {
			return "", err
		}
		selectedItemID := ""
		for _, operation := range operations {
			if !operation.completed {
				selectedItemID = operation.itemID
				break
			}
		}
		if selectedItemID == "" {
			return "", ErrRecoveryWorkerFenceLost
		}
		markerKey, err := coordinator.workspaceKeys.ByVersion(
			ctx, backupasset.KeyDomainRecoveryCleanupOwnership, markerKeyVersion,
		)
		if err != nil || !validTargetLocatorKey(markerKey, markerKeyVersion) {
			return "", ErrRecoveryWorkerFenceLost
		}
		if _, err := validateInterruptedOperationWorkspace(
			plan, job, claim, checkpoints, operations, markerKey, RecoveryOperation{},
			selectedItemID, true, false, now,
		); err != nil {
			return "", err
		}
		pathDigest, err := TargetPathDigest(job.TargetRootID, job.RootLocatorDigest, job.EncryptedWorkspaceRelativeLocator)
		if err != nil {
			return "", ErrRecoveryWorkerFenceLost
		}
		return pathDigest, nil
	case TargetModeInPlace:
		if job.WorkspacePhase != string(WorkspacePhaseNone) ||
			job.EncryptedWorkspaceRelativeLocator != "" || job.WorkspaceMarkerBindingDigest != "" ||
			job.WorkspaceOwner != "" || job.WorkspaceFence != 0 || !emptyRecoveryMarkerValidation(job) ||
			job.PlaintextDeadline != nil || !validDigest(job.PathDigest) {
			return "", ErrRecoveryWorkerFenceLost
		}
		if checkpointCount == 0 {
			var priorArmedAttempts int64
			if err := tx.WithContext(ctx).Model(&model.BackupAssetRecoveryAttempt{}).
				Where("job_id = ? AND id <> ? AND mutation_armed = ?", job.ID, attempt.ID, true).
				Count(&priorArmedAttempts).Error; err != nil {
				return "", err
			}
			if priorArmedAttempts != 0 {
				return "", ErrRecoveryWorkerFenceLost
			}
			return job.PathDigest, nil
		}
		var checkpoints []model.BackupAssetRecoveryCheckpoint
		loaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("job_id = ?", job.ID).Order("sequence ASC").Find(&checkpoints)
		if loaded.Error != nil {
			return "", loaded.Error
		}
		if int64(len(checkpoints)) != checkpointCount {
			return "", ErrRecoveryWorkerFenceLost
		}
		operations, err := loadInPlaceOrdinaryCheckpointOperationsTx(ctx, tx, plan, preflight, job)
		if err != nil {
			return "", err
		}
		if _, _, _, err := validateInPlaceOrdinaryCheckpointHistory(
			plan, job, claim, checkpoints, operations, now,
		); err != nil {
			return "", err
		}
		last := checkpoints[len(checkpoints)-1]
		if last.AttemptID != claim.AttemptID || last.AttemptFence != claim.AttemptFence ||
			last.NodeFence != claim.NodeFence {
			var reconciledOverwrite bool
			if last.ID == reconciledOverwriteCheckpointID &&
				last.Phase == string(CheckpointPhaseOperation) {
				for _, operation := range operations {
					if operation.itemID == last.JobItemID && operation.completed &&
						operation.kind == RecoveryOperationOverwrite {
						reconciledOverwrite = true
						break
					}
				}
			}
			if !reconciledOverwrite {
				return "", ErrRecoveryWorkerFenceLost
			}
		}
		return job.PathDigest, nil
	default:
		return "", ErrRecoveryWorkerFenceLost
	}
}

func recoveryWorkspaceReservedCheckpoint(
	id string,
	job model.BackupAssetRecoveryJob,
	claim RecoveryWorkerClaim,
	now time.Time,
) model.BackupAssetRecoveryCheckpoint {
	return model.BackupAssetRecoveryCheckpoint{
		ID: id, JobID: job.ID, AttemptID: claim.AttemptID, Sequence: 0,
		Phase: string(CheckpointPhaseWorkspaceReserved), PlanBindingDigest: job.PlanBindingDigest,
		SourceRevisionDigest: job.SourceRevisionDigest, PreflightID: job.PreflightID,
		PreflightRevision: job.PreflightRevision, PreflightExpiresAt: job.PreflightExpiresAt,
		SecurityDecision: job.SecurityDecision, SecurityDecisionDigest: job.SecurityDecisionDigest,
		SecurityFindingSetDigest: job.SecurityFindingSetDigest, SecurityPolicyRevision: job.SecurityPolicyRevision,
		AuthorityGrantID: job.AuthorityGrantID, JobAuthorityCategory: job.AuthorityCategory,
		AuthorityBindingDigest: job.AuthorityBindingDigest, AuthorityExpiresAt: job.AuthorityExpiresAt,
		CreatedAt: now,
	}
}

func earliestRecoveryFirstWriteExpiry(values ...time.Time) time.Time {
	var earliest time.Time
	for _, value := range values {
		value = value.UTC()
		if earliest.IsZero() || value.Before(earliest) {
			earliest = value
		}
	}
	return earliest
}

func validRecoveryWorkspaceKey(material backupasset.DomainKeyMaterial) bool {
	return material.Domain == backupasset.KeyDomainRecoveryCleanupOwnership &&
		material.State == backupasset.DomainKeyActive && material.Version > 0 && len(material.Key) == sha256.Size
}

func recoveryWorkspaceMarkerBindingDigest(
	material backupasset.DomainKeyMaterial,
	jobID string,
	rootID string,
	rootRevision string,
	workspaceLocator string,
	claim RecoveryWorkerClaim,
) string {
	mac := hmac.New(sha256.New, material.Key)
	for _, component := range []string{
		recoveryWorkspaceMarkerBindingDomain, fmt.Sprintf("%d", material.Version), jobID, rootID,
		rootRevision, workspaceLocator, claim.WorkerID, fmt.Sprintf("%d", claim.AttemptFence),
	} {
		_, _ = mac.Write([]byte(component))
		_, _ = mac.Write([]byte{0})
	}
	return hex.EncodeToString(mac.Sum(nil))
}

func recoveryVerificationEvidenceSummaryDigest(
	claim RecoveryWorkerClaim,
	checkpoint model.BackupAssetRecoveryCheckpoint,
	item model.BackupAssetRecoveryJobItem,
	bytesWritten int64,
	verifiedSize int64,
	verifiedDigest string,
	verifiedAt time.Time,
) string {
	return framedDigest(
		"xirang/recovery/verification-evidence/v1",
		claim.JobID,
		claim.AttemptID,
		claim.NodeLeaseID,
		strconv.FormatUint(claim.AttemptFence, 10),
		strconv.FormatUint(claim.NodeFence, 10),
		checkpoint.ID,
		checkpoint.OperationDigest,
		checkpoint.PriorTargetRevision,
		checkpoint.NextTargetRevision,
		item.ID,
		strconv.FormatInt(bytesWritten, 10),
		strconv.FormatInt(verifiedSize, 10),
		verifiedDigest,
		verifiedAt.UTC().Format(time.RFC3339Nano),
	)
}

func recoverySourceFence(source model.RecoveryPointLease) backupasset.LeaseFence {
	return backupasset.LeaseFence{
		LeaseID: source.ID, RecoveryPointID: source.RecoveryPointID,
		HolderType: backupasset.LeaseHolderType(source.HolderType), OwnerID: source.OwnerID,
		AttemptID: source.AttemptID, FenceToken: source.FenceToken,
	}
}

func recoveryWorkerSourceError(err error) error {
	if errors.Is(err, backupasset.ErrLeaseFenceLost) || errors.Is(err, backupasset.ErrLeaseDeadlineExceeded) ||
		errors.Is(err, backupasset.ErrConflict) {
		return ErrRecoveryWorkerFenceLost
	}
	return err
}

func validRecoveryWorkerClaim(claim RecoveryWorkerClaim) bool {
	return validOpaqueID(claim.JobID) && validOpaqueID(claim.AttemptID) && validOpaqueID(claim.NodeLeaseID) &&
		validRecoveryWorkerID(claim.WorkerID) && claim.AttemptFence > 0 && claim.NodeFence > 0 &&
		claim.TransitionRevision > 0 && !claim.LeaseExpiresAt.IsZero() && !claim.AbsoluteDeadline.IsZero() &&
		!claim.LeaseExpiresAt.UTC().After(claim.AbsoluteDeadline.UTC()) &&
		claim.SourceFence.LeaseID != "" && claim.SourceFence.OwnerID == claim.JobID &&
		claim.SourceFence.AttemptID == claim.AttemptID && claim.SourceFence.HolderType == backupasset.LeaseHolderRecoveryJob
}

func validRecoveryWorkerID(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 64 || !utf8.ValidString(value) {
		return false
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return false
		}
	}
	return true
}

func recoveryWorkerContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
