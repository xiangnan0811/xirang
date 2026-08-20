package retention

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	RetentionWorkerLeaseOwnerID = "retention-worker"
	maxRetentionWorkerAdvance   = 16
)

type AuditDetailPruner interface {
	PurgeEligibleDetails(context.Context, int) (int, error)
}

type ImportRebuildReconciler interface {
	ReconcileImports(context.Context, int) (int, error)
	ReconcileRebuilds(context.Context, int) (int, error)
}

type WorkerDependencies struct {
	Foundation    *backupasset.FoundationService
	Coordinator   *Coordinator
	Policies      *PolicyService
	Holds         *HoldService
	Audit         AuditDetailPruner
	ImportRebuild ImportRebuildReconciler
	Metrics       Metrics
	Now           func() time.Time
	After         func(time.Duration) <-chan time.Time
}

type Worker struct {
	foundation    *backupasset.FoundationService
	coordinator   *Coordinator
	policies      *PolicyService
	holds         *HoldService
	audit         AuditDetailPruner
	importRebuild ImportRebuildReconciler
	metrics       Metrics
	now           func() time.Time
	after         func(time.Duration) <-chan time.Time

	mu                 sync.Mutex
	stopping           bool
	passCancel         context.CancelFunc
	stop               chan struct{}
	wg                 sync.WaitGroup
	attemptAfterID     string
	policyAfterID      string
	policyPointCursors map[string]string
}

func NewWorker(dependencies WorkerDependencies) (*Worker, error) {
	if dependencies.Foundation == nil || dependencies.Coordinator == nil || dependencies.Policies == nil ||
		dependencies.Holds == nil || dependencies.Audit == nil || dependencies.ImportRebuild == nil ||
		dependencies.Metrics == nil {
		return nil, fmt.Errorf("%w: retention worker dependencies are unavailable", backupasset.ErrInvalidState)
	}
	if dependencies.Now == nil {
		dependencies.Now = func() time.Time { return time.Now().UTC() }
	}
	return &Worker{
		foundation: dependencies.Foundation, coordinator: dependencies.Coordinator,
		policies: dependencies.Policies, holds: dependencies.Holds, audit: dependencies.Audit,
		importRebuild: dependencies.ImportRebuild, metrics: dependencies.Metrics,
		now: dependencies.Now, after: dependencies.After, stop: make(chan struct{}),
		policyPointCursors: map[string]string{},
	}, nil
}

func (worker *Worker) StartupPass(ctx context.Context) error {
	if worker == nil {
		return fmt.Errorf("%w: retention worker is unavailable", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	passCtx, finish, ok := worker.beginPass(ctx)
	if !ok {
		return nil
	}
	defer finish()
	return worker.reconcile(passCtx)
}

func (worker *Worker) Run(ctx context.Context) {
	if worker == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if !worker.beginRun() {
		return
	}
	defer worker.wg.Done()
	for {
		if worker.isStopping() {
			return
		}
		config, err := worker.foundation.RetentionConfig()
		if err != nil {
			return
		}
		timer, stopTimer := worker.intervalWait(config.ReconcileInterval)
		select {
		case <-ctx.Done():
			stopTimer()
			return
		case <-worker.stop:
			stopTimer()
			return
		case <-timer:
			stopTimer()
			if worker.isStopping() {
				return
			}
			_ = worker.StartupPass(ctx)
		}
	}
}

func (worker *Worker) StopAccepting() {
	if worker == nil {
		return
	}
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if worker.stopping {
		return
	}
	worker.stopping = true
	close(worker.stop)
	if worker.passCancel != nil {
		worker.passCancel()
	}
}

func (worker *Worker) Shutdown(ctx context.Context) error {
	if worker == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	worker.StopAccepting()
	done := make(chan struct{})
	go func() {
		worker.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (worker *Worker) reconcile(ctx context.Context) error {
	config, err := worker.foundation.RetentionConfig()
	if err != nil {
		return err
	}
	if config.Enabled {
		if _, err := worker.holds.ExpireOperationalMaintenance(ctx, config.BatchSize); err != nil {
			return err
		}
		if err := worker.selectAndClaim(ctx, config.BatchSize); err != nil {
			return err
		}
	}
	if err := worker.settleClaimed(ctx, config.BatchSize); err != nil {
		return err
	}
	if !config.Enabled {
		return nil
	}
	if _, err := worker.importRebuild.ReconcileImports(ctx, config.BatchSize); err != nil {
		return err
	}
	if _, err := worker.importRebuild.ReconcileRebuilds(ctx, config.BatchSize); err != nil {
		return err
	}
	_, err = worker.audit.PurgeEligibleDetails(ctx, config.BatchSize)
	return err
}

func (worker *Worker) selectAndClaim(ctx context.Context, limit int) error {
	policyRemaining := limit
	claimRemaining := limit
	inspectedRemaining := defaultSelectionInspectedLimit
	afterID := worker.policyAfterID
	pageSize := limit
	if pageSize > 100 {
		pageSize = 100
	}
	for policyRemaining > 0 && claimRemaining > 0 && inspectedRemaining > 0 {
		policies, err := worker.policies.ListActiveAfter(ctx, pageSize, afterID)
		if err != nil {
			return err
		}
		if len(policies) == 0 {
			worker.policyAfterID = ""
			return nil
		}
		resumeAfter := afterID
		for _, policy := range policies {
			afterID = policy.ID
			policyRemaining--
			if err := ctx.Err(); err != nil {
				return err
			}
			if worker.policyPointCursors == nil {
				worker.policyPointCursors = map[string]string{}
			}
			cursor := worker.policyPointCursors[policy.ID]
			evaluatedAt := worker.now().UTC()
			if cursor != "" {
				evaluatedAt = time.Time{}
			}
			selection, err := worker.policies.Select(ctx, SelectionRequest{
				PolicyID: policy.ID, ExpectedRevision: policy.Revision, EvaluatedAt: evaluatedAt,
				Limit: claimRemaining, InspectedLimit: inspectedRemaining, Cursor: cursor,
			})
			if err != nil {
				if errors.Is(err, backupasset.ErrConflict) || errors.Is(err, backupasset.ErrNotFound) {
					delete(worker.policyPointCursors, policy.ID)
					worker.policyAfterID = afterID
					resumeAfter = afterID
					if policyRemaining <= 0 || claimRemaining <= 0 || inspectedRemaining <= 0 {
						return nil
					}
					continue
				}
				return err
			}
			inspectedRemaining -= selection.Inspected
			if inspectedRemaining < 0 {
				inspectedRemaining = 0
			}
			for _, point := range selection.Points {
				if claimRemaining <= 0 {
					worker.policyAfterID = resumeAfter
					return nil
				}
				if err := ctx.Err(); err != nil {
					return err
				}
				claimed, err := worker.coordinator.Claim(ctx, ClaimRequest{
					RecoveryPointID: point.RecoveryPointID,
					Operation:       backupasset.LifecycleRetentionExpire,
					PolicySelection: &selection,
				})
				if err != nil {
					if errors.Is(err, backupasset.ErrConflict) || errors.Is(err, backupasset.ErrNotFound) {
						continue
					}
					return err
				}
				claimRemaining--
				if claimed.Phase == backupasset.LifecyclePhaseSelected {
					worker.metrics.Observe(MetricSelected)
				}
			}
			if selection.NextCursor != "" {
				worker.policyPointCursors[policy.ID] = selection.NextCursor
				worker.policyAfterID = resumeAfter
				if policyRemaining <= 0 || claimRemaining <= 0 || inspectedRemaining <= 0 {
					return nil
				}
				continue
			}
			delete(worker.policyPointCursors, policy.ID)
			worker.policyAfterID = afterID
			resumeAfter = afterID
			if policyRemaining <= 0 || claimRemaining <= 0 || inspectedRemaining <= 0 {
				return nil
			}
		}
		if policyRemaining <= 0 || claimRemaining <= 0 || inspectedRemaining <= 0 {
			return nil
		}
		if len(policies) < pageSize {
			worker.policyAfterID = ""
			return nil
		}
	}
	return nil
}

func (worker *Worker) settleClaimed(ctx context.Context, limit int) error {
	remaining := limit
	afterID := worker.attemptAfterID
	pageSize := limit
	if pageSize > 100 {
		pageSize = 100
	}
	wrapped := false
	for remaining > 0 {
		attempts, err := worker.coordinator.ListIncompleteAttemptsAfter(ctx, pageSize, afterID)
		if err != nil {
			return err
		}
		if len(attempts) == 0 {
			worker.attemptAfterID = ""
			if afterID != "" && !wrapped {
				afterID = ""
				wrapped = true
				continue
			}
			return nil
		}
		for _, attempt := range attempts {
			afterID = attempt.ID
			if err := ctx.Err(); err != nil {
				worker.attemptAfterID = afterID
				return err
			}
			if _, err := worker.coordinator.Heartbeat(ctx, attempt.ID); err != nil {
				if ctx.Err() != nil {
					worker.attemptAfterID = afterID
					return ctx.Err()
				}
				if !errors.Is(err, backupasset.ErrNotFound) {
					worker.observeAttempt(attempt, attempt)
				}
			}
			current, err := worker.coordinator.loadAttempt(ctx, attempt.ID)
			if err != nil {
				if errors.Is(err, backupasset.ErrNotFound) {
					remaining--
					continue
				}
				worker.attemptAfterID = afterID
				return err
			}
			if err := worker.advanceUntilSettled(ctx, current); err != nil {
				worker.attemptAfterID = afterID
				return err
			}
			remaining--
			if remaining <= 0 {
				worker.attemptAfterID = afterID
				return nil
			}
		}
		worker.attemptAfterID = afterID
		if len(attempts) < pageSize {
			worker.attemptAfterID = ""
			return nil
		}
	}
	return nil
}

func (worker *Worker) advanceUntilSettled(ctx context.Context, attempt LifecycleAttempt) error {
	current := attempt
	for step := 0; step < maxRetentionWorkerAdvance; step++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if current.Phase == backupasset.LifecyclePhaseComplete {
			worker.observeAttempt(attempt, current)
			return nil
		}
		previous := current
		advanced, err := worker.coordinator.Advance(ctx, current.ID)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			worker.observeAttempt(previous, previous)
			return nil
		}
		worker.observeAttempt(previous, advanced)
		if advanced.Phase == backupasset.LifecyclePhaseBlocked || advanced.Phase == backupasset.LifecyclePhaseComplete {
			return nil
		}
		if advanced.Phase == previous.Phase && advanced.TransitionRevision == previous.TransitionRevision {
			return nil
		}
		current = advanced
	}
	return nil
}

func (worker *Worker) observeAttempt(previous LifecycleAttempt, current LifecycleAttempt) {
	if previous.Phase == backupasset.LifecyclePhaseBlocked && current.Phase != backupasset.LifecyclePhaseBlocked {
		worker.metrics.Observe(MetricRetried)
	}
	if previous.Phase != backupasset.LifecyclePhaseBlocked && current.Phase == backupasset.LifecyclePhaseBlocked {
		worker.metrics.Observe(MetricBlocked)
	}
	if current.Phase == backupasset.LifecyclePhaseComplete {
		switch current.Operation {
		case backupasset.LifecycleMutableRetire:
			worker.metrics.Observe(MetricRetired)
		case backupasset.LifecycleRetentionExpire, backupasset.LifecycleExplicitPurge:
			worker.metrics.Observe(MetricExpired)
		}
	}
}

func (worker *Worker) beginPass(parent context.Context) (context.Context, func(), bool) {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if worker.stopping {
		return nil, nil, false
	}
	ctx, cancel := context.WithCancel(parent)
	worker.passCancel = cancel
	worker.wg.Add(1)
	return ctx, func() {
		cancel()
		worker.mu.Lock()
		if worker.passCancel != nil {
			worker.passCancel = nil
		}
		worker.mu.Unlock()
		worker.wg.Done()
	}, true
}

func (worker *Worker) beginRun() bool {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if worker.stopping {
		return false
	}
	worker.wg.Add(1)
	return true
}

func (worker *Worker) isStopping() bool {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	return worker.stopping
}

func (worker *Worker) intervalWait(d time.Duration) (<-chan time.Time, func()) {
	if worker.after != nil {
		return worker.after(d), func() {}
	}
	timer := time.NewTimer(d)
	return timer.C, func() { _ = timer.Stop() }
}

type PurgeService struct {
	coordinator *Coordinator
	audit       MutationAuditor
}

func NewPurgeService(coordinator *Coordinator) (*PurgeService, error) {
	if coordinator == nil {
		return nil, fmt.Errorf("%w: retention purge facade is unavailable", backupasset.ErrInvalidState)
	}
	service := &PurgeService{coordinator: coordinator}
	if auditor, ok := coordinator.audit.(MutationAuditor); ok {
		service.audit = auditor
	}
	return service, nil
}

func (service *PurgeService) AuditsMutations() bool {
	return service != nil && service.audit != nil
}

func (service *PurgeService) SetMutationAuditor(auditor MutationAuditor) {
	if service == nil {
		return
	}
	service.audit = auditor
}

func (service *PurgeService) Claim(ctx context.Context, request ClaimRequest) (LifecycleAttempt, error) {
	if service == nil || service.coordinator == nil {
		return LifecycleAttempt{}, fmt.Errorf("%w: retention purge facade is unavailable", backupasset.ErrInvalidState)
	}
	if request.Operation != backupasset.LifecycleExplicitPurge {
		return LifecycleAttempt{}, fmt.Errorf("%w: purge facade accepts only explicit purge", backupasset.ErrInvalidState)
	}
	return service.coordinator.Claim(ctx, request)
}

const (
	maxPurgePlanItems = 200
	purgePlanTTL      = 15 * time.Minute
)

type PurgePlanItemInput struct {
	RecoveryPointID    string
	PointRevision      int64
	CapabilityRevision int
}

type PreviewPurgeRequest struct {
	Actor            backupasset.AuditActor
	RepositoryID     string
	Items            []PurgePlanItemInput
	RecoveryPointIDs []string
}

type CreatePurgePlanRequest struct {
	Actor                  backupasset.AuditActor
	RepositoryID           string
	ExpectedImpactRevision int64
	Items                  []PurgePlanItemInput
}

type ExecutePurgeRequest struct {
	Actor                  backupasset.AuditActor
	RepositoryID           string
	PlanID                 string
	ExpectedRevision       int64
	ExpectedImpactRevision int64
	Reason                 string
	ProofDigest            string
}

type PurgePlanView struct {
	ID             string
	RepositoryID   string
	Revision       int64
	ImpactRevision int64
	ExpiresAt      time.Time
	HoldCount      int64
	LeaseCount     int64
	WORMCount      int64
	Status         backupasset.PurgePlanStatus
	ItemCount      int
	Items          []PurgePlanItemView
}

type PurgePlanItemView struct {
	RecoveryPointID    string
	PointRevision      int64
	CapabilityRevision int
}

type PurgeExecuteResult struct {
	PlanID  string
	Claimed int
	Blocked int
}

func (service *PurgeService) Preview(ctx context.Context, request PreviewPurgeRequest) (PurgePlanView, error) {
	if service == nil || service.coordinator == nil || service.coordinator.db == nil {
		return PurgePlanView{}, fmt.Errorf("%w: retention purge facade is unavailable", backupasset.ErrInvalidState)
	}
	if err := validateAdminActor(request.Actor); err != nil {
		return PurgePlanView{}, err
	}
	if backupasset.ValidateOpaqueID(request.RepositoryID) != nil {
		return PurgePlanView{}, fmt.Errorf("%w: invalid explicit purge plan", backupasset.ErrInvalidState)
	}
	now := service.coordinator.now().UTC()
	var view PurgePlanView
	err := service.coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		items := request.Items
		if len(request.RecoveryPointIDs) > 0 {
			resolved, resolveErr := service.lockPurgeItemsByIDsTx(tx, request.RepositoryID, request.RecoveryPointIDs)
			if resolveErr != nil {
				return resolveErr
			}
			items = resolved
		} else if err := validatePurgePlanItems(request.RepositoryID, items); err != nil {
			return err
		}
		impact, err := service.lockPurgeImpactTx(ctx, tx, request.RepositoryID, items, now, "")
		if err != nil {
			return err
		}
		view = PurgePlanView{
			RepositoryID: request.RepositoryID, Revision: 0, ImpactRevision: impact.Revision,
			ExpiresAt: now.Add(purgePlanTTL), HoldCount: impact.Counts.HoldCount, LeaseCount: impact.Counts.LeaseCount,
			WORMCount: impact.Counts.WORMCount, Status: backupasset.PurgePlanReady, ItemCount: len(impact.Items),
			Items: impact.Items,
		}
		return nil
	})
	if err != nil {
		return PurgePlanView{}, err
	}
	return view, nil
}

func (service *PurgeService) CreatePlan(ctx context.Context, request CreatePurgePlanRequest) (PurgePlanView, error) {
	if service == nil || service.coordinator == nil || service.coordinator.db == nil {
		return PurgePlanView{}, fmt.Errorf("%w: retention purge facade is unavailable", backupasset.ErrInvalidState)
	}
	if err := validateAdminActor(request.Actor); err != nil {
		return PurgePlanView{}, err
	}
	if request.ExpectedImpactRevision < 1 {
		return PurgePlanView{}, fmt.Errorf("%w: invalid explicit purge plan", backupasset.ErrInvalidState)
	}
	if err := validatePurgePlanItems(request.RepositoryID, request.Items); err != nil {
		return PurgePlanView{}, err
	}
	now := service.coordinator.now().UTC()
	var view PurgePlanView
	err := service.coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		impact, err := service.lockPurgeImpactTx(ctx, tx, request.RepositoryID, request.Items, now, "")
		if err != nil {
			return err
		}
		if request.ExpectedImpactRevision != impact.Revision {
			return fmt.Errorf("%w: purge impact revision", backupasset.ErrConflict)
		}
		planID, err := service.coordinator.newID()
		if err != nil || backupasset.ValidateOpaqueID(planID) != nil {
			return fmt.Errorf("%w: generate explicit purge plan ID", backupasset.ErrInvalidState)
		}
		plan := model.BackupAssetPurgePlan{
			ID: planID, RepositoryID: request.RepositoryID, RequesterID: request.Actor.UserID,
			Revision: 1, ImpactRevision: impact.Revision, ExpiresAt: now.Add(purgePlanTTL),
			HoldCount: impact.Counts.HoldCount, LeaseCount: impact.Counts.LeaseCount, WORMCount: impact.Counts.WORMCount,
			Status: string(backupasset.PurgePlanReady), CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.Create(&plan).Error; err != nil {
			return fmt.Errorf("create explicit purge plan: %w", err)
		}
		for index, item := range request.Items {
			itemID, err := service.coordinator.newID()
			if err != nil || backupasset.ValidateOpaqueID(itemID) != nil {
				return fmt.Errorf("%w: generate explicit purge plan item ID", backupasset.ErrInvalidState)
			}
			if err := tx.Create(&model.BackupAssetPurgePlanItem{
				ID: itemID, PlanID: planID, Ordinal: index + 1, RecoveryPointID: item.RecoveryPointID,
				ExpectedPointRevision: item.PointRevision, ExpectedCapabilityRevision: item.CapabilityRevision, CreatedAt: now,
			}).Error; err != nil {
				return fmt.Errorf("create explicit purge plan item: %w", err)
			}
		}
		view = PurgePlanView{
			ID: plan.ID, RepositoryID: plan.RepositoryID, Revision: plan.Revision, ImpactRevision: plan.ImpactRevision,
			ExpiresAt: plan.ExpiresAt.UTC(), HoldCount: plan.HoldCount, LeaseCount: plan.LeaseCount, WORMCount: plan.WORMCount,
			Status: backupasset.PurgePlanReady, ItemCount: len(impact.Items), Items: impact.Items,
		}
		return writeMutationAuditTx(ctx, tx, service.audit, mutationAuditInput(
			ctx, request.Actor, backupasset.AuditActionRepositoryPurgePlan, request.RepositoryID, "", int64(len(impact.Items)), "",
		))
	})
	if err != nil {
		return PurgePlanView{}, err
	}
	return view, nil
}

func (service *PurgeService) Execute(ctx context.Context, request ExecutePurgeRequest) (PurgeExecuteResult, error) {
	if service == nil || service.coordinator == nil || service.coordinator.db == nil {
		return PurgeExecuteResult{}, fmt.Errorf("%w: retention purge facade is unavailable", backupasset.ErrInvalidState)
	}
	if err := validateAdminActor(request.Actor); err != nil {
		return PurgeExecuteResult{}, err
	}
	if backupasset.ValidateOpaqueID(request.RepositoryID) != nil || backupasset.ValidateOpaqueID(request.PlanID) != nil ||
		request.ExpectedRevision < 1 || request.ExpectedImpactRevision < 1 ||
		len(strings.TrimSpace(request.Reason)) == 0 || !isSHA256Hex(request.ProofDigest) {
		return PurgeExecuteResult{}, fmt.Errorf("%w: invalid explicit purge execute", backupasset.ErrInvalidState)
	}
	reasonDigest := sha256Hex(strings.TrimSpace(request.Reason))
	now := service.coordinator.now().UTC()
	result := PurgeExecuteResult{PlanID: request.PlanID}
	err := service.coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var plan model.BackupAssetPurgePlan
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&plan, "id = ?", request.PlanID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: purge plan", backupasset.ErrNotFound)
			}
			return fmt.Errorf("load explicit purge plan: %w", err)
		}
		if plan.RepositoryID != request.RepositoryID || plan.Revision != request.ExpectedRevision ||
			plan.ImpactRevision != request.ExpectedImpactRevision || plan.RequesterID != request.Actor.UserID ||
			plan.Status != string(backupasset.PurgePlanReady) || !now.Before(plan.ExpiresAt.UTC()) {
			return fmt.Errorf("%w: explicit purge plan changed", backupasset.ErrConflict)
		}
		var items []model.BackupAssetPurgePlanItem
		if err := tx.Where("plan_id = ?", plan.ID).Order("ordinal ASC").Find(&items).Error; err != nil {
			return fmt.Errorf("load explicit purge plan items: %w", err)
		}
		if len(items) == 0 {
			return fmt.Errorf("%w: explicit purge plan items", backupasset.ErrConflict)
		}
		inputs := make([]PurgePlanItemInput, 0, len(items))
		for _, item := range items {
			inputs = append(inputs, PurgePlanItemInput{
				RecoveryPointID: item.RecoveryPointID, PointRevision: item.ExpectedPointRevision,
				CapabilityRevision: item.ExpectedCapabilityRevision,
			})
		}
		impact, err := service.lockPurgeImpactTx(ctx, tx, plan.RepositoryID, inputs, now, plan.ID)
		if err != nil {
			return err
		}
		if impact.Revision != plan.ImpactRevision {
			return fmt.Errorf("%w: purge impact revision", backupasset.ErrConflict)
		}
		bound := tx.Model(&model.BackupAssetPurgePlan{}).
			Where("id = ? AND revision = ? AND status = ?", plan.ID, plan.Revision, backupasset.PurgePlanReady).
			Updates(map[string]any{
				"status":                backupasset.PurgePlanExecuting,
				"execute_actor_id":      request.Actor.UserID,
				"execute_proof_digest":  request.ProofDigest,
				"execute_reason_digest": reasonDigest,
				"execute_bound_at":      now,
				"updated_at":            now,
			})
		if bound.Error != nil {
			return fmt.Errorf("bind explicit purge plan: %w", bound.Error)
		}
		if bound.RowsAffected != 1 {
			return fmt.Errorf("%w: explicit purge plan changed", backupasset.ErrConflict)
		}
		for _, item := range items {
			attempt, claimErr := service.coordinator.ClaimTx(ctx, tx, ClaimRequest{
				RecoveryPointID: item.RecoveryPointID,
				Operation:       backupasset.LifecycleExplicitPurge,
				PurgePlan: &PurgePlanSnapshot{
					PlanID: request.PlanID, Revision: request.ExpectedRevision, ActorID: request.Actor.UserID,
					PointRevision: item.ExpectedPointRevision, CapabilityRevision: item.ExpectedCapabilityRevision,
				},
			})
			if claimErr != nil {
				return claimErr
			}
			result.Claimed++
			if attempt.BlockedReason != "" {
				result.Blocked++
			}
		}
		consumed := tx.Model(&model.BackupAssetPurgePlan{}).
			Where("id = ? AND revision = ? AND status = ?", request.PlanID, request.ExpectedRevision, backupasset.PurgePlanExecuting).
			Updates(map[string]any{
				"status":      backupasset.PurgePlanConsumed,
				"consumed_at": now,
				"updated_at":  service.coordinator.now().UTC(),
			})
		if consumed.Error != nil {
			return fmt.Errorf("consume explicit purge plan: %w", consumed.Error)
		}
		if consumed.RowsAffected != 1 {
			return fmt.Errorf("%w: explicit purge plan changed", backupasset.ErrConflict)
		}
		return writeMutationAuditTx(ctx, tx, service.audit, mutationAuditInput(
			ctx, request.Actor, backupasset.AuditActionRepositoryPurge, request.RepositoryID, "", int64(result.Claimed), "",
		))
	})
	if err != nil {
		return PurgeExecuteResult{}, err
	}
	return result, nil
}

type purgeImpactSnapshot struct {
	Revision int64
	Counts   lifecycleImpactCounts
	Items    []PurgePlanItemView
}

func validatePurgePlanItems(repositoryID string, items []PurgePlanItemInput) error {
	if backupasset.ValidateOpaqueID(repositoryID) != nil || len(items) < 1 || len(items) > maxPurgePlanItems {
		return fmt.Errorf("%w: invalid explicit purge plan", backupasset.ErrInvalidState)
	}
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		if backupasset.ValidateOpaqueID(item.RecoveryPointID) != nil || item.PointRevision < 1 || item.CapabilityRevision < 1 || seen[item.RecoveryPointID] {
			return fmt.Errorf("%w: invalid explicit purge plan item", backupasset.ErrInvalidState)
		}
		seen[item.RecoveryPointID] = true
	}
	return nil
}

func (service *PurgeService) lockPurgeItemsByIDsTx(
	tx *gorm.DB,
	repositoryID string,
	pointIDs []string,
) ([]PurgePlanItemInput, error) {
	if len(pointIDs) < 1 || len(pointIDs) > maxPurgePlanItems {
		return nil, fmt.Errorf("%w: invalid explicit purge plan", backupasset.ErrInvalidState)
	}
	seen := make(map[string]bool, len(pointIDs))
	items := make([]PurgePlanItemInput, 0, len(pointIDs))
	for _, pointID := range pointIDs {
		if backupasset.ValidateOpaqueID(pointID) != nil || seen[pointID] {
			return nil, fmt.Errorf("%w: invalid explicit purge plan item", backupasset.ErrInvalidState)
		}
		seen[pointID] = true
		var point model.RecoveryPoint
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&point, "id = ?", pointID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, fmt.Errorf("%w: recovery point", backupasset.ErrNotFound)
			}
			return nil, fmt.Errorf("load explicit purge recovery point: %w", err)
		}
		if point.RepositoryID != repositoryID || !explicitPurgeEligible(point) {
			return nil, fmt.Errorf("%w: recovery point is not explicit-purge eligible", backupasset.ErrConflict)
		}
		items = append(items, PurgePlanItemInput{
			RecoveryPointID: point.ID, PointRevision: point.PointRevision, CapabilityRevision: point.CapabilityRevision,
		})
	}
	return items, nil
}

func (service *PurgeService) lockPurgeImpactTx(
	ctx context.Context,
	tx *gorm.DB,
	repositoryID string,
	items []PurgePlanItemInput,
	now time.Time,
	excludePlanID string,
) (purgeImpactSnapshot, error) {
	var repository model.BackupRepository
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&repository, "id = ?", repositoryID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return purgeImpactSnapshot{}, fmt.Errorf("%w: repository", backupasset.ErrNotFound)
		}
		return purgeImpactSnapshot{}, fmt.Errorf("load explicit purge repository: %w", err)
	}
	pointIDs := make([]string, 0, len(items))
	views := make([]PurgePlanItemView, 0, len(items))
	points := make([]model.RecoveryPoint, 0, len(items))
	for _, item := range items {
		var point model.RecoveryPoint
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&point, "id = ?", item.RecoveryPointID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return purgeImpactSnapshot{}, fmt.Errorf("%w: recovery point", backupasset.ErrNotFound)
			}
			return purgeImpactSnapshot{}, fmt.Errorf("load explicit purge recovery point: %w", err)
		}
		if point.RepositoryID != repositoryID || point.PointRevision != item.PointRevision ||
			point.CapabilityRevision != item.CapabilityRevision || !explicitPurgeEligible(point) {
			return purgeImpactSnapshot{}, fmt.Errorf("%w: recovery point is not explicit-purge eligible", backupasset.ErrConflict)
		}
		pointIDs = append(pointIDs, point.ID)
		points = append(points, point)
		views = append(views, PurgePlanItemView{
			RecoveryPointID: point.ID, PointRevision: item.PointRevision, CapabilityRevision: item.CapabilityRevision,
		})
	}
	counts, err := countLifecycleImpact(ctx, tx, pointIDs, now)
	if err != nil {
		return purgeImpactSnapshot{}, err
	}
	maxPlan, err := maxRepositoryPurgeImpactRevisionTx(tx, repositoryID, excludePlanID)
	if err != nil {
		return purgeImpactSnapshot{}, err
	}
	return purgeImpactSnapshot{
		Revision: computePurgeImpactRevision(maxPlan, points, counts),
		Counts:   counts,
		Items:    views,
	}, nil
}

func maxRepositoryPurgeImpactRevisionTx(tx *gorm.DB, repositoryID string, excludePlanID string) (int64, error) {
	query := tx.Model(&model.BackupAssetPurgePlan{}).Where("repository_id = ?", repositoryID)
	if excludePlanID != "" {
		query = query.Where("id <> ?", excludePlanID)
	}
	var maxRevision int64
	if err := query.Select("COALESCE(MAX(impact_revision), 0)").Scan(&maxRevision).Error; err != nil {
		return 0, fmt.Errorf("load repository purge impact revision: %w", err)
	}
	return maxRevision, nil
}

func computePurgeImpactRevision(maxPlanRevision int64, points []model.RecoveryPoint, counts lifecycleImpactCounts) int64 {
	ids := make([]string, 0, len(points))
	for _, point := range points {
		ids = append(ids, point.ID)
	}
	sort.Strings(ids)
	digest := sha256.New()
	_, _ = digest.Write([]byte("xirang.purge-impact.v1"))
	var header [8]byte
	binary.BigEndian.PutUint64(header[:], uint64(maxPlanRevision+1))
	_, _ = digest.Write(header[:])
	for _, point := range points {
		binary.BigEndian.PutUint64(header[:], uint64(point.PointRevision))
		_, _ = digest.Write(header[:])
		binary.BigEndian.PutUint64(header[:], uint64(point.CapabilityRevision))
		_, _ = digest.Write(header[:])
	}
	binary.BigEndian.PutUint64(header[:], uint64(counts.HoldCount))
	_, _ = digest.Write(header[:])
	binary.BigEndian.PutUint64(header[:], uint64(counts.LeaseCount))
	_, _ = digest.Write(header[:])
	binary.BigEndian.PutUint64(header[:], uint64(counts.WORMCount))
	_, _ = digest.Write(header[:])
	for _, id := range ids {
		_, _ = digest.Write([]byte(id))
		_, _ = digest.Write([]byte{0})
	}
	sum := digest.Sum(nil)
	revision := int64(binary.BigEndian.Uint64(sum[:8]) & 0x7fffffffffffffff)
	if revision < 1 {
		return 1
	}
	return revision
}

func sha256Hex(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func isSHA256Hex(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

var _ interface {
	StartupPass(context.Context) error
	Run(context.Context)
	Shutdown(context.Context) error
} = (*Worker)(nil)

func explicitPurgeEligible(point model.RecoveryPoint) bool {
	semantics := backupasset.PointVersionSemantics(point.Semantics)
	state := backupasset.RecoveryPointState(point.State)
	switch semantics {
	case backupasset.PointNativeSnapshot, backupasset.PointXirangManifest, backupasset.PointImportedBaseline:
		return state == backupasset.RecoveryPointCommitted || state == backupasset.RecoveryPointDegraded
	case backupasset.PointMutableHead:
		return state == backupasset.RecoveryPointObserved || state == backupasset.RecoveryPointRetired
	default:
		return false
	}
}

var _ AuditDetailPruner = (*AuditRetention)(nil)
