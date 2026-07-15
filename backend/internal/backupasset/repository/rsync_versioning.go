package repository

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"
	"xirang/backend/internal/util"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	defaultRsyncVersioningPreflightTTL = 15 * time.Minute
	rsyncVersioningConstrainedBytes    = 1 << 30
	rsyncVersioningConstrainedInodes   = 100_000
)

type rsyncVersioningPreflightPlan struct {
	task        model.Task
	repository  model.BackupRepository
	link        model.TaskRepositoryLink
	binding     bindingDocument
	managedRoot string
}

// rsyncImportedBaselineActivation contains the private facts needed to begin
// the migration-only publication after the admission transition has published
// its managed generation. The legacy locator never leaves this package.
type rsyncImportedBaselineActivation struct {
	taskID        uint
	taskRunID     uint
	startedAt     time.Time
	legacyLocator string
	preflightID   string
}

type rsyncVersioningActivationState struct {
	importedBaseline *rsyncImportedBaselineActivation
}

// rsyncVersioningPreflightRecord remains process-local by design. A process
// restart drops it, forcing an operator to obtain fresh root/revision evidence
// before activation rather than trusting a stale preflight proof.
type rsyncVersioningPreflightRecord struct {
	evidence     provider.RsyncTreePreflightEvidence
	repositoryID string
	linkID       string
	managedRoot  string
	binding      bindingDocument
}

type rsyncVersioningPreflightStore struct {
	mu      sync.Mutex
	now     func() time.Time
	records map[string]rsyncVersioningPreflightRecord
}

func newRsyncVersioningPreflightStore(now func() time.Time) *rsyncVersioningPreflightStore {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &rsyncVersioningPreflightStore{now: now, records: make(map[string]rsyncVersioningPreflightRecord)}
}

func (store *rsyncVersioningPreflightStore) put(record rsyncVersioningPreflightRecord) error {
	if store == nil || backupasset.ValidateOpaqueID(record.evidence.ID) != nil ||
		backupasset.ValidateOpaqueID(record.repositoryID) != nil || backupasset.ValidateOpaqueID(record.linkID) != nil ||
		strings.TrimSpace(record.managedRoot) == "" || record.evidence.ExpiresAt.IsZero() ||
		validateBindingDocument(record.binding) != nil {
		return fmt.Errorf("%w: invalid Rsync versioning preflight record", backupasset.ErrInvalidState)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	now := store.now().UTC()
	for id, candidate := range store.records {
		if !candidate.evidence.ExpiresAt.After(now) {
			delete(store.records, id)
		}
	}
	store.records[record.evidence.ID] = record
	return nil
}

func (store *rsyncVersioningPreflightStore) get(id string) (rsyncVersioningPreflightRecord, bool) {
	if store == nil || backupasset.ValidateOpaqueID(id) != nil {
		return rsyncVersioningPreflightRecord{}, false
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	record, ok := store.records[id]
	if !ok {
		return rsyncVersioningPreflightRecord{}, false
	}
	if !record.evidence.ExpiresAt.After(store.now().UTC()) {
		delete(store.records, id)
		return rsyncVersioningPreflightRecord{}, false
	}
	return record, true
}

func (store *rsyncVersioningPreflightStore) delete(id string) {
	if store == nil || backupasset.ValidateOpaqueID(id) != nil {
		return
	}
	store.mu.Lock()
	delete(store.records, id)
	store.mu.Unlock()
}

// CreateRsyncVersioningPreflight proves one exact legacy Rsync task can enter
// a requested managed-tree mode. It deliberately leaves the task link and its
// active V1 binding untouched; only a later activation transaction may change
// them.
func (service *Service) CreateRsyncVersioningPreflight(ctx context.Context, request backupasset.RsyncVersioningPreflightRequest) (backupasset.RsyncVersioningPreflightResult, error) {
	if err := request.Validate(); err != nil {
		return backupasset.RsyncVersioningPreflightResult{}, err
	}
	if err := service.ensureEnabled(""); err != nil {
		return backupasset.RsyncVersioningPreflightResult{}, err
	}
	if service == nil || service.db == nil || service.keyring == nil || service.preflights == nil {
		return backupasset.RsyncVersioningPreflightResult{}, fmt.Errorf("%w: Rsync versioning preflight dependencies are unavailable", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	plan, err := service.loadRsyncVersioningPreflightPlan(ctx, request)
	if err != nil {
		return backupasset.RsyncVersioningPreflightResult{}, err
	}
	markerKey, err := service.rsyncVersioningMarkerKey(ctx, plan.repository.ID)
	if err != nil {
		return backupasset.RsyncVersioningPreflightResult{}, err
	}
	bootstrap, err := provider.BootstrapRsyncManagedRoot(ctx, provider.RsyncManagedRootBootstrapRequest{
		ManagedRoot: plan.managedRoot, RepositoryID: plan.repository.ID, MarkerKey: markerKey, CreatedAt: service.utcNow(),
	})
	if err != nil {
		return backupasset.RsyncVersioningPreflightResult{}, fmt.Errorf("%w: managed Rsync root preflight rejected", backupasset.ErrConflict)
	}
	if err := provider.ValidateRsyncManagedRootSeparation(ctx, plan.managedRoot, plan.link.EncryptedLegacyLocator); err != nil {
		return backupasset.RsyncVersioningPreflightResult{}, fmt.Errorf("%w: managed Rsync legacy target overlap", backupasset.ErrConflict)
	}
	if err := provider.ValidateRsyncManagedRootSeparation(ctx, plan.managedRoot, plan.task.RsyncSource); err != nil {
		return backupasset.RsyncVersioningPreflightResult{}, fmt.Errorf("%w: managed Rsync source overlap", backupasset.ErrConflict)
	}
	preflighter, err := provider.NewRsyncTreePreflighter(service.now, defaultRsyncVersioningPreflightTTL)
	if err != nil {
		return backupasset.RsyncVersioningPreflightResult{}, err
	}
	evidence, err := preflighter.PreflightManagedRoot(ctx, plan.managedRoot, provider.RsyncTreePreflightRequest{
		TaskID: request.TaskID, ExpectedTaskRevision: request.ExpectedTaskRevision, Mode: request.RequestedMode,
		LocalSourceRoot: plan.task.RsyncSource, RepositoryMarkerDigest: bootstrap.RepositoryMarkerDigest,
		CapabilityRevision: uint64(plan.repository.CapabilityRevision),
	})
	if err != nil {
		return backupasset.RsyncVersioningPreflightResult{}, fmt.Errorf("%w: managed Rsync preflight rejected", backupasset.ErrConflict)
	}
	if evidence.ManagedRootIdentityDigest != bootstrap.ManagedRootIdentityDigest {
		return backupasset.RsyncVersioningPreflightResult{}, fmt.Errorf("%w: managed Rsync preflight root drift", backupasset.ErrConflict)
	}
	if err := service.preflights.put(rsyncVersioningPreflightRecord{
		evidence: evidence, repositoryID: plan.repository.ID, linkID: plan.link.ID, managedRoot: plan.managedRoot, binding: plan.binding,
	}); err != nil {
		return backupasset.RsyncVersioningPreflightResult{}, err
	}
	return backupasset.RsyncVersioningPreflightResult{
		PreflightID: evidence.ID, Mode: evidence.Mode, State: backupasset.RsyncVersioningReady,
		ReasonCode: backupasset.RsyncVersioningReasonReady, CapabilityRevision: evidence.CapabilityRevision,
		ExpiresAt: evidence.ExpiresAt.UTC(), CapacityEstimate: rsyncVersioningCapacityBucket(evidence.FreeBytes),
		InodeEstimate: rsyncVersioningInodeBucket(evidence.FreeInodes),
	}, nil
}

// CreateRsyncVersioningPreflightForRequest carries the authenticated request
// context only to the safe audit seam. The underlying preflight request stays
// free of actor, path, root, command, and credential fields.
func (service *Service) CreateRsyncVersioningPreflightForRequest(ctx context.Context, request backupasset.RsyncVersioningPreflightRequest, requestContext RequestContext) (backupasset.RsyncVersioningPreflightResult, error) {
	result, err := service.CreateRsyncVersioningPreflight(ctx, request)
	service.writeRsyncVersioningAudit(ctx, requestContext, backupasset.AuditActionRsyncVersioningPreflight, request.TaskID, request.RequestedMode, result.State, result.ReasonCode, err)
	return result, err
}

// ActivateRsyncVersioning consumes an exact in-process preflight. The initial
// full-copy path records no historical point: first_new_point only switches to
// a managed root and pauses the task until an explicit later run is admitted.
func (service *Service) ActivateRsyncVersioning(ctx context.Context, request backupasset.RsyncVersioningActivationRequest) (backupasset.RsyncVersioningActivationResult, error) {
	if err := request.Validate(); err != nil {
		return backupasset.RsyncVersioningActivationResult{}, err
	}
	if err := service.ensureEnabled(""); err != nil {
		return backupasset.RsyncVersioningActivationResult{}, err
	}
	if service == nil || service.db == nil || service.keyring == nil || service.preflights == nil {
		return backupasset.RsyncVersioningActivationResult{}, fmt.Errorf("%w: Rsync versioning activation dependencies are unavailable", backupasset.ErrInvalidState)
	}
	transitioner, ok := service.admission.(publication.FeatureTransitioner)
	if !ok || transitioner == nil {
		return backupasset.RsyncVersioningActivationResult{}, fmt.Errorf("%w: Rsync versioning activation admission transition is unavailable", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	record, ok := service.preflights.get(request.PreflightID)
	if !ok {
		return backupasset.RsyncVersioningActivationResult{}, fmt.Errorf("%w: Rsync versioning preflight expired or unavailable", backupasset.ErrConflict)
	}
	if request.MigrationChoice != backupasset.RsyncVersioningFirstNewPoint && request.MigrationChoice != backupasset.RsyncVersioningImportedBaseline {
		return backupasset.RsyncVersioningActivationResult{}, fmt.Errorf("%w: requested Rsync versioning migration is not ready", backupasset.ErrCapabilityUnavailable)
	}
	if request.MigrationChoice == backupasset.RsyncVersioningImportedBaseline && service.publication == nil {
		return backupasset.RsyncVersioningActivationResult{}, fmt.Errorf("%w: imported Rsync baseline publication is unavailable", backupasset.ErrInvalidState)
	}
	if record.evidence.TaskID != request.TaskID || record.evidence.ExpectedTaskRevision != request.ExpectedTaskRevision {
		return backupasset.RsyncVersioningActivationResult{}, fmt.Errorf("%w: Rsync versioning preflight does not match Task revision", backupasset.ErrConflict)
	}
	var activation rsyncVersioningActivationState
	if err := transitioner.TransitionFeature(ctx, true, func() error {
		current, ok := service.preflights.get(request.PreflightID)
		if !ok || current.evidence.Digest != record.evidence.Digest || current.repositoryID != record.repositoryID || current.linkID != record.linkID {
			return fmt.Errorf("%w: Rsync versioning preflight expired or changed", backupasset.ErrConflict)
		}
		if !current.evidence.ExpiresAt.After(service.utcNow()) {
			return fmt.Errorf("%w: Rsync versioning preflight expired", backupasset.ErrConflict)
		}
		state, err := service.activateRsyncVersioningWithPreflight(ctx, request, current)
		if err != nil {
			return err
		}
		activation = state
		return nil
	}); err != nil {
		return backupasset.RsyncVersioningActivationResult{}, err
	}
	service.preflights.delete(request.PreflightID)
	if activation.importedBaseline != nil {
		// TransitionFeature keeps admission closed through the binding transaction.
		// Publication starts only after it exposes the new managed generation, so
		// Prepare can acquire a token without racing the legacy configuration.
		if _, err := service.publishImportedRsyncBaseline(ctx, *activation.importedBaseline); err != nil {
			return backupasset.RsyncVersioningActivationResult{}, err
		}
	}
	summary, err := service.RsyncVersioningSummary(ctx, request.TaskID)
	if err != nil {
		return backupasset.RsyncVersioningActivationResult{}, err
	}
	return backupasset.RsyncVersioningActivationResult{
		MigrationChoice: request.MigrationChoice,
		Summary:         summary,
	}, nil
}

// ActivateRsyncVersioningForRequest writes one bounded audit fact for the
// explicit migration decision while keeping provider publication audit facts
// separate and free to retain their own lifecycle state.
func (service *Service) ActivateRsyncVersioningForRequest(ctx context.Context, request backupasset.RsyncVersioningActivationRequest, requestContext RequestContext) (backupasset.RsyncVersioningActivationResult, error) {
	mode := backupasset.TaskPublicationMode("")
	if record, ok := service.preflights.get(request.PreflightID); ok {
		mode = record.evidence.Mode
	}
	result, err := service.ActivateRsyncVersioning(ctx, request)
	if err == nil {
		mode = result.Summary.Mode
	}
	service.writeRsyncVersioningAudit(ctx, requestContext, backupasset.AuditActionRsyncVersioningActivate, request.TaskID, mode, result.Summary.State, result.Summary.ReasonCode, err)
	return result, err
}

// PrepareRsyncVersioningRollback closes managed admission, proves that the
// encrypted legacy locator remains physically separate from the managed root,
// and leaves the task paused. It deliberately retains every managed binding,
// point, marker, and durable-history fact; it is not a destructive rollback.
func (service *Service) PrepareRsyncVersioningRollback(ctx context.Context, request backupasset.RsyncVersioningRollbackPreparationRequest) (backupasset.RsyncVersioningRollbackPreparationResult, error) {
	if err := request.Validate(); err != nil {
		return backupasset.RsyncVersioningRollbackPreparationResult{}, err
	}
	if service == nil || service.db == nil || service.keyring == nil {
		return backupasset.RsyncVersioningRollbackPreparationResult{}, fmt.Errorf("%w: Rsync versioning rollback dependencies are unavailable", backupasset.ErrInvalidState)
	}
	transitioner, ok := service.admission.(publication.FeatureTransitioner)
	if !ok || transitioner == nil {
		return backupasset.RsyncVersioningRollbackPreparationResult{}, fmt.Errorf("%w: Rsync versioning rollback admission transition is unavailable", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := transitioner.TransitionFeature(ctx, false, func() error {
		return service.prepareRsyncVersioningRollbackAfterDrain(ctx, request)
	}); err != nil {
		return backupasset.RsyncVersioningRollbackPreparationResult{}, err
	}
	summary, err := service.RsyncVersioningSummary(ctx, request.TaskID)
	if err != nil {
		return backupasset.RsyncVersioningRollbackPreparationResult{}, err
	}
	return backupasset.RsyncVersioningRollbackPreparationResult{Summary: summary}, nil
}

// PrepareRsyncVersioningRollbackForRequest records only the safe request and
// resulting state. It deliberately does not audit the retained legacy locator
// or managed-root proof used by the rollback-preparation transaction.
func (service *Service) PrepareRsyncVersioningRollbackForRequest(ctx context.Context, request backupasset.RsyncVersioningRollbackPreparationRequest, requestContext RequestContext) (backupasset.RsyncVersioningRollbackPreparationResult, error) {
	mode := backupasset.TaskPublicationMode("")
	if summary, summaryErr := service.RsyncVersioningSummary(ctx, request.TaskID); summaryErr == nil {
		mode = summary.Mode
	}
	result, err := service.PrepareRsyncVersioningRollback(ctx, request)
	if err == nil {
		mode = result.Summary.Mode
	}
	service.writeRsyncVersioningAudit(ctx, requestContext, backupasset.AuditActionRsyncVersioningRollback, request.TaskID, mode, result.Summary.State, result.Summary.ReasonCode, err)
	return result, err
}

func (service *Service) writeRsyncVersioningAudit(ctx context.Context, requestContext RequestContext, action backupasset.AuditAction, taskID uint, mode backupasset.TaskPublicationMode, state backupasset.RsyncVersioningState, reason backupasset.RsyncVersioningReasonCode, operationErr error) {
	if service == nil || service.audit == nil || taskID == 0 {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	outcome := backupasset.AuditOutcomeSuccess
	if operationErr != nil {
		if errors.Is(operationErr, backupasset.ErrForbidden) || errors.Is(operationErr, backupasset.ErrCapabilityUnavailable) {
			outcome = backupasset.AuditOutcomeBlocked
		} else {
			outcome = backupasset.AuditOutcomeFailure
		}
	}
	fields := map[backupasset.AuditField]any{
		backupasset.AuditFieldStage:         "rsync_versioning",
		backupasset.AuditFieldCorrelationID: requestContext.CorrelationID,
	}
	switch mode {
	case backupasset.PublicationLegacyMutable, backupasset.PublicationVersionedHardlink, backupasset.PublicationVersionedFullCopy:
		fields[backupasset.AuditFieldMode] = string(mode)
	}
	if state != "" {
		fields[backupasset.AuditFieldStatus] = string(state)
	}
	if reason != "" {
		fields[backupasset.AuditFieldReasonCode] = string(reason)
	}
	input := backupasset.AuditEventInput{Actor: requestContext.Actor, Action: action, Outcome: outcome, TaskID: &taskID, Fields: fields}
	if operationErr != nil {
		input.FailureCode = "operation_failed"
	}
	if err := service.audit.Write(ctx, input); err != nil {
		logger.Module("backup_repository").Warn().Str("action", string(action)).Uint("task_id", taskID).Msg("Rsync 版本化资产审计写入失败")
	}
}

// RsyncVersioningSummary projects only the task-facing publication facts. It
// intentionally converts malformed bindings, link drift, and unknown stored
// modes into a stable blocked result instead of exposing filesystem/provider
// diagnostics through a Task response.
func (service *Service) RsyncVersioningSummary(ctx context.Context, taskID uint) (backupasset.RsyncVersioningSummary, error) {
	if service == nil || service.db == nil || taskID == 0 {
		return backupasset.RsyncVersioningSummary{}, fmt.Errorf("%w: Rsync versioning summary dependencies are unavailable", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var taskEntity model.Task
	if err := service.db.WithContext(ctx).Where("id = ? AND archived_at IS NULL", taskID).First(&taskEntity).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return backupasset.RsyncVersioningSummary{}, fmt.Errorf("%w: Rsync versioning Task", backupasset.ErrNotFound)
		}
		return backupasset.RsyncVersioningSummary{}, fmt.Errorf("load Rsync versioning summary Task: %w", err)
	}
	if bindingProviderForTask(taskEntity) != backupasset.ProviderRsync {
		return backupasset.RsyncVersioningSummary{}, fmt.Errorf("%w: Rsync versioning summary requires an Rsync Task", backupasset.ErrCapabilityUnavailable)
	}
	taskRevision, err := managedRsyncTaskRevision(taskEntity)
	if err != nil {
		return backupasset.RsyncVersioningSummary{}, err
	}
	withTaskRevision := func(summary backupasset.RsyncVersioningSummary) backupasset.RsyncVersioningSummary {
		summary.TaskRevision = strconv.FormatUint(taskRevision, 10)
		return summary
	}
	var link model.TaskRepositoryLink
	if err := service.db.WithContext(ctx).Where("task_id = ? AND unlinked_at IS NULL", taskID).First(&link).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return withTaskRevision(backupasset.RsyncVersioningSummary{
				Mode: backupasset.PublicationLegacyMutable, State: backupasset.RsyncVersioningLegacy,
				ReasonCode: backupasset.RsyncVersioningReasonLegacy, CapabilityRevision: 1,
			}), nil
		}
		return backupasset.RsyncVersioningSummary{}, fmt.Errorf("load Rsync versioning summary link: %w", err)
	}
	var repository model.BackupRepository
	if err := service.db.WithContext(ctx).First(&repository, "id = ?", link.RepositoryID).Error; err != nil {
		return backupasset.RsyncVersioningSummary{}, fmt.Errorf("load Rsync versioning summary repository: %w", err)
	}
	capabilityRevision := uint64(repository.CapabilityRevision)
	if capabilityRevision == 0 {
		capabilityRevision = 1
	}
	mode := backupasset.TaskPublicationMode(link.PublicationMode)
	switch mode {
	case backupasset.PublicationLegacyMutable:
		return withTaskRevision(backupasset.RsyncVersioningSummary{
			Mode: mode, State: backupasset.RsyncVersioningLegacy, ReasonCode: backupasset.RsyncVersioningReasonLegacy,
			CapabilityRevision: capabilityRevision,
		}), nil
	case backupasset.PublicationVersionedHardlink, backupasset.PublicationVersionedFullCopy:
	default:
		return withTaskRevision(blockedRsyncVersioningSummary(capabilityRevision)), nil
	}
	if repository.ProviderKind != string(backupasset.ProviderRsync) {
		return withTaskRevision(blockedRsyncVersioningSummary(capabilityRevision)), nil
	}
	var access model.RepositoryAccessBinding
	if err := service.db.WithContext(ctx).Where("repository_id = ? AND status = ?", repository.ID, bindingStatusActive).First(&access).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return withTaskRevision(blockedRsyncVersioningSummary(capabilityRevision)), nil
		}
		return backupasset.RsyncVersioningSummary{}, fmt.Errorf("load Rsync versioning summary binding: %w", err)
	}
	stored, err := decodeStoredBindingDocument(access.EncryptedConfig)
	if err != nil || stored.ManagedRsyncV2 == nil || stored.V1 != nil {
		return withTaskRevision(blockedRsyncVersioningSummary(capabilityRevision)), nil
	}
	document := *stored.ManagedRsyncV2
	if err := validateManagedRsyncBindingAssociation(document, managedRsyncBindingAssociation{
		Task: taskEntity, Link: link, RootMarkerDigest: document.RootMarkerDigest,
	}); err != nil || document.PublicationMode != mode {
		return withTaskRevision(blockedRsyncVersioningSummary(capabilityRevision)), nil
	}
	if document.RollbackPrepared {
		return withTaskRevision(backupasset.RsyncVersioningSummary{
			Mode: mode, State: backupasset.RsyncVersioningRollbackPrepared, ReasonCode: backupasset.RsyncVersioningReasonRollbackPrepared,
			CapabilityRevision: capabilityRevision, SeedFullCopyRequired: document.SeedFullCopyRequired,
		}), nil
	}
	var point model.RecoveryPoint
	result := service.db.WithContext(ctx).
		Where("repository_id = ? AND producing_task_id = ? AND semantics IN ?", repository.ID, taskEntity.ID, []string{string(backupasset.PointXirangManifest), string(backupasset.PointImportedBaseline)}).
		Order("updated_at DESC, id DESC").Limit(1).Find(&point)
	if result.Error != nil {
		return backupasset.RsyncVersioningSummary{}, fmt.Errorf("load Rsync versioning summary point: %w", result.Error)
	}
	state := backupasset.RsyncVersioningReady
	if result.RowsAffected == 1 {
		switch backupasset.RecoveryPointState(point.State) {
		case backupasset.RecoveryPointPreparing:
			state = backupasset.RsyncVersioningPreparing
		case backupasset.RecoveryPointVerifying:
			state = backupasset.RsyncVersioningVerifying
		case backupasset.RecoveryPointCommitted:
			state = backupasset.RsyncVersioningCommitted
		case backupasset.RecoveryPointFailed:
			state = backupasset.RsyncVersioningFailed
		default:
			return withTaskRevision(blockedRsyncVersioningSummary(capabilityRevision)), nil
		}
	}
	return withTaskRevision(backupasset.RsyncVersioningSummary{
		Mode: mode, State: state, ReasonCode: backupasset.RsyncVersioningReasonReady,
		CapabilityRevision: capabilityRevision, SeedFullCopyRequired: document.SeedFullCopyRequired,
	}), nil
}

func blockedRsyncVersioningSummary(capabilityRevision uint64) backupasset.RsyncVersioningSummary {
	if capabilityRevision == 0 {
		capabilityRevision = 1
	}
	return backupasset.RsyncVersioningSummary{
		Mode: backupasset.PublicationLegacyMutable, State: backupasset.RsyncVersioningBlocked,
		ReasonCode: backupasset.RsyncVersioningReasonUnsupported, CapabilityRevision: capabilityRevision,
	}
}

func (service *Service) prepareRsyncVersioningRollbackAfterDrain(ctx context.Context, request backupasset.RsyncVersioningRollbackPreparationRequest) error {
	return service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var taskEntity model.Task
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND archived_at IS NULL", request.TaskID).First(&taskEntity).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: Rsync versioning Task", backupasset.ErrNotFound)
			}
			return fmt.Errorf("lock Rsync versioning rollback Task: %w", err)
		}
		revision, err := managedRsyncTaskRevision(taskEntity)
		if err != nil {
			return err
		}
		if revision != request.ExpectedTaskRevision || bindingProviderForTask(taskEntity) != backupasset.ProviderRsync {
			return fmt.Errorf("%w: Rsync versioning rollback Task revision changed", backupasset.ErrConflict)
		}
		var link model.TaskRepositoryLink
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("task_id = ? AND unlinked_at IS NULL", taskEntity.ID).First(&link).Error; err != nil {
			return fmt.Errorf("lock Rsync versioning rollback link: %w", err)
		}
		if link.TaskID == nil || *link.TaskID != taskEntity.ID || strings.TrimSpace(link.EncryptedLegacyLocator) == "" {
			return fmt.Errorf("%w: Rsync versioning rollback legacy locator is unavailable", backupasset.ErrConflict)
		}
		mode := backupasset.TaskPublicationMode(link.PublicationMode)
		if mode != backupasset.PublicationVersionedHardlink && mode != backupasset.PublicationVersionedFullCopy {
			return fmt.Errorf("%w: Rsync versioning rollback link mode is invalid", backupasset.ErrConflict)
		}
		var repository model.BackupRepository
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&repository, "id = ?", link.RepositoryID).Error; err != nil {
			return fmt.Errorf("lock Rsync versioning rollback repository: %w", err)
		}
		version, _, _, err := backupasset.MapPublicationMode(backupasset.ProviderRsync, mode)
		if err != nil {
			return err
		}
		if repository.ProviderKind != string(backupasset.ProviderRsync) || repository.VersionMode != string(version) ||
			repository.ImmutabilityLevel != string(backupasset.ImmutabilityXirangManaged) {
			return fmt.Errorf("%w: Rsync versioning rollback repository changed", backupasset.ErrConflict)
		}
		var access model.RepositoryAccessBinding
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("repository_id = ? AND status = ?", repository.ID, bindingStatusActive).First(&access).Error; err != nil {
			return fmt.Errorf("lock Rsync versioning rollback binding: %w", err)
		}
		stored, err := decodeStoredBindingDocument(access.EncryptedConfig)
		if err != nil || stored.ManagedRsyncV2 == nil || stored.V1 != nil {
			if err != nil {
				return err
			}
			return fmt.Errorf("%w: Rsync versioning rollback binding changed", backupasset.ErrConflict)
		}
		document := *stored.ManagedRsyncV2
		if err := validateManagedRsyncBindingAssociation(document, managedRsyncBindingAssociation{
			Task: taskEntity, Link: link, RootMarkerDigest: document.RootMarkerDigest,
		}); err != nil {
			return err
		}
		if taskEntity.RsyncTarget != link.EncryptedLegacyLocator {
			return fmt.Errorf("%w: Rsync versioning rollback legacy target drift", backupasset.ErrConflict)
		}
		if err := provider.ValidateRsyncManagedRootSeparation(ctx, document.ManagedRootLocator, link.EncryptedLegacyLocator); err != nil {
			return fmt.Errorf("%w: Rsync versioning rollback root overlap", backupasset.ErrConflict)
		}
		now := service.utcNow()
		taskEntity.RsyncTarget = link.EncryptedLegacyLocator
		taskEntity.Enabled = false
		taskEntity.SkipNext = false
		taskEntity.NextRunAt = nil
		if err := tx.Save(&taskEntity).Error; err != nil {
			return fmt.Errorf("pause Rsync versioning rollback Task: %w", err)
		}
		document.RollbackPrepared = true
		encoded, err := encodeManagedRsyncBindingDocumentV2(document)
		if err != nil {
			return err
		}
		access.EncryptedConfig = encoded
		access.UpdatedAt = now
		if err := tx.Save(&access).Error; err != nil {
			return fmt.Errorf("record Rsync versioning rollback preparation: %w", err)
		}
		return nil
	})
}

// activateRsyncVersioningWithPreflight runs only while FeatureTransitioner has
// closed admission and drained the prior generation. It revalidates every
// mutable preflight fact before the expected-revision transaction begins.
func (service *Service) activateRsyncVersioningWithPreflight(ctx context.Context, request backupasset.RsyncVersioningActivationRequest, record rsyncVersioningPreflightRecord) (rsyncVersioningActivationState, error) {
	if !record.evidence.ExpiresAt.After(service.utcNow()) {
		return rsyncVersioningActivationState{}, fmt.Errorf("%w: Rsync versioning preflight expired", backupasset.ErrConflict)
	}
	plan, err := service.loadRsyncVersioningPreflightPlan(ctx, backupasset.RsyncVersioningPreflightRequest{
		TaskID: request.TaskID, ExpectedTaskRevision: request.ExpectedTaskRevision, RequestedMode: record.evidence.Mode,
	})
	if err != nil {
		return rsyncVersioningActivationState{}, err
	}
	if record.repositoryID != plan.repository.ID || record.linkID != plan.link.ID || record.managedRoot != plan.managedRoot ||
		!sameRsyncVersioningBinding(record.binding, plan.binding) {
		return rsyncVersioningActivationState{}, fmt.Errorf("%w: Rsync versioning preflight binding changed", backupasset.ErrConflict)
	}
	if err := service.revalidateRsyncVersioningPreflight(ctx, plan, record); err != nil {
		return rsyncVersioningActivationState{}, err
	}

	config, err := backupasset.EncodeRsyncPublicationConfig(record.evidence.Mode)
	if err != nil {
		return rsyncVersioningActivationState{}, err
	}
	managedBinding := managedRsyncBindingDocumentV2{
		Version: managedRsyncBindingDocumentVersion, Provider: backupasset.ProviderRsync,
		IdentityClass: provider.IdentityXirangManagedRepository, TaskID: plan.task.ID, NodeID: plan.task.NodeID,
		RepositoryID: plan.repository.ID, TaskRepositoryLinkID: plan.link.ID, LayoutRevision: managedRsyncLayoutRevisionV1,
		ManagedRootLocator: plan.managedRoot, RootMarkerDigest: record.evidence.RepositoryMarkerDigest,
		ManagedRootIdentityDigest: record.evidence.ManagedRootIdentityDigest, PublicationMode: record.evidence.Mode,
		PreflightID: record.evidence.ID, PreflightDigest: record.evidence.Digest,
		SeedFullCopyRequired: record.evidence.Mode == backupasset.PublicationVersionedHardlink && request.MigrationChoice == backupasset.RsyncVersioningFirstNewPoint,
		IdentitySalt:         plan.binding.IdentitySalt,
	}
	if err := validateManagedRsyncBindingDocumentV2(managedBinding); err != nil {
		return rsyncVersioningActivationState{}, err
	}
	managedIdentity, err := managedRsyncRepositoryIdentity(managedBinding)
	if err != nil {
		return rsyncVersioningActivationState{}, err
	}
	encodedBinding, err := encodeManagedRsyncBindingDocumentV2(managedBinding)
	if err != nil {
		return rsyncVersioningActivationState{}, err
	}

	var activation rsyncVersioningActivationState
	err = service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var taskEntity model.Task
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND archived_at IS NULL", request.TaskID).First(&taskEntity).Error; err != nil {
			return fmt.Errorf("lock Rsync versioning Task: %w", err)
		}
		revision, err := managedRsyncTaskRevision(taskEntity)
		if err != nil {
			return err
		}
		if revision != request.ExpectedTaskRevision || bindingProviderForTask(taskEntity) != backupasset.ProviderRsync {
			return fmt.Errorf("%w: Rsync versioning Task revision changed", backupasset.ErrConflict)
		}
		var activeRuns int64
		if err := tx.Model(&model.TaskRun{}).Where("task_id = ? AND status IN ?", taskEntity.ID, []string{"pending", "running", "retrying"}).Count(&activeRuns).Error; err != nil {
			return fmt.Errorf("count active Rsync versioning TaskRuns: %w", err)
		}
		if activeRuns != 0 {
			return fmt.Errorf("%w: Rsync versioning Task has an active run", backupasset.ErrConflict)
		}
		var link model.TaskRepositoryLink
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND task_id = ? AND unlinked_at IS NULL", plan.link.ID, taskEntity.ID).First(&link).Error; err != nil {
			return fmt.Errorf("lock Rsync versioning legacy link: %w", err)
		}
		if link.RepositoryID != plan.repository.ID || link.PublicationMode != string(backupasset.PublicationLegacyMutable) ||
			link.EncryptedLegacyLocator != plan.link.EncryptedLegacyLocator {
			return fmt.Errorf("%w: Rsync versioning legacy link changed", backupasset.ErrConflict)
		}
		legacyLocator := link.EncryptedLegacyLocator
		var repository model.BackupRepository
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&repository, "id = ?", plan.repository.ID).Error; err != nil {
			return fmt.Errorf("lock Rsync versioning repository: %w", err)
		}
		if repository.ProviderKind != string(backupasset.ProviderRsync) || repository.VersionMode != string(backupasset.VersionMutableHead) ||
			repository.ImmutabilityLevel != string(backupasset.ImmutabilityMutable) || repository.CapabilityRevision != plan.repository.CapabilityRevision {
			return fmt.Errorf("%w: Rsync versioning repository changed", backupasset.ErrConflict)
		}
		var binding model.RepositoryAccessBinding
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("repository_id = ? AND status = ?", repository.ID, bindingStatusActive).First(&binding).Error; err != nil {
			return fmt.Errorf("lock Rsync versioning binding: %w", err)
		}
		stored, err := decodeStoredBindingDocument(binding.EncryptedConfig)
		if err != nil || stored.V1 == nil || stored.ManagedRsyncV2 != nil || !sameRsyncVersioningBinding(*stored.V1, plan.binding) {
			if err != nil {
				return err
			}
			return fmt.Errorf("%w: Rsync versioning binding changed", backupasset.ErrConflict)
		}
		var mutablePoints []model.RecoveryPoint
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("repository_id = ? AND semantics = ?", repository.ID, backupasset.PointMutableHead).Find(&mutablePoints).Error; err != nil {
			return fmt.Errorf("load Rsync mutable-head observations: %w", err)
		}
		for _, point := range mutablePoints {
			if point.State != string(backupasset.RecoveryPointObserved) {
				return fmt.Errorf("%w: Rsync mutable-head point cannot be retired", backupasset.ErrConflict)
			}
			if err := tx.Delete(&point).Error; err != nil {
				return fmt.Errorf("remove synthetic Rsync mutable-head observation: %w", err)
			}
		}

		version, _, _, err := backupasset.MapPublicationMode(backupasset.ProviderRsync, record.evidence.Mode)
		if err != nil {
			return err
		}
		now := service.utcNow()
		repository.VersionMode = string(version)
		repository.ImmutabilityLevel = string(backupasset.ImmutabilityXirangManaged)
		repository.RepositoryIdentity = &managedIdentity
		repository.CapabilityRevision++
		repository.UpdatedAt = now
		if err := tx.Save(&repository).Error; err != nil {
			return fmt.Errorf("activate Rsync versioning repository: %w", err)
		}
		link.PublicationMode = string(record.evidence.Mode)
		link.UpdatedAt = now
		if err := tx.Save(&link).Error; err != nil {
			return fmt.Errorf("activate Rsync versioning link: %w", err)
		}
		binding.BindingKind = "managed_rsync_v2"
		binding.EncryptedConfig = encodedBinding
		binding.UpdatedAt = now
		if err := tx.Save(&binding).Error; err != nil {
			return fmt.Errorf("activate Rsync versioning binding: %w", err)
		}
		taskEntity.ExecutorConfig = config
		taskEntity.Enabled = false
		taskEntity.SkipNext = false
		taskEntity.NextRunAt = nil
		if err := tx.Save(&taskEntity).Error; err != nil {
			return fmt.Errorf("pause activated Rsync versioning Task: %w", err)
		}
		if request.MigrationChoice == backupasset.RsyncVersioningImportedBaseline {
			startedAt := now
			migrationRun := model.TaskRun{
				TaskID: taskEntity.ID, TriggerType: "migration", Status: "running", StartedAt: &startedAt,
				CreatedAt: now, UpdatedAt: now,
			}
			if err := tx.Create(&migrationRun).Error; err != nil {
				return fmt.Errorf("create imported Rsync baseline TaskRun: %w", err)
			}
			activation.importedBaseline = &rsyncImportedBaselineActivation{
				taskID: taskEntity.ID, taskRunID: migrationRun.ID, startedAt: startedAt,
				legacyLocator: legacyLocator, preflightID: record.evidence.ID,
			}
		}
		return nil
	})
	if err != nil {
		return rsyncVersioningActivationState{}, err
	}
	return activation, nil
}

func (service *Service) publishImportedRsyncBaseline(ctx context.Context, activation rsyncImportedBaselineActivation) (publication.Outcome, error) {
	if service == nil || service.publication == nil || activation.taskID == 0 || activation.taskRunID == 0 ||
		backupasset.ValidateOpaqueID(activation.preflightID) != nil || strings.TrimSpace(activation.legacyLocator) == "" || activation.startedAt.IsZero() {
		return publication.Outcome{}, fmt.Errorf("%w: imported Rsync baseline activation is invalid", backupasset.ErrInvalidState)
	}
	audit, err := backupasset.NewSystemPublicationAuditContext("rsync-baseline-" + activation.preflightID)
	if err != nil {
		return publication.Outcome{}, err
	}
	outcome, err := service.publication.PublishImportedRsyncBaseline(ctx, publication.Run{
		Task: model.Task{ID: activation.taskID}, TaskRunID: activation.taskRunID, Trigger: "migration", StartedAt: activation.startedAt,
		Audit: audit, ImportedBaseline: true,
	}, provider.RsyncTreeCommandSource{LocalPath: activation.legacyLocator})
	if err != nil {
		return publication.Outcome{}, fmt.Errorf("publish imported Rsync baseline: %w", err)
	}
	return outcome, nil
}

func (service *Service) revalidateRsyncVersioningPreflight(ctx context.Context, plan rsyncVersioningPreflightPlan, record rsyncVersioningPreflightRecord) error {
	markerKey, err := service.rsyncVersioningMarkerKey(ctx, plan.repository.ID)
	if err != nil {
		return err
	}
	bootstrap, err := provider.BootstrapRsyncManagedRoot(ctx, provider.RsyncManagedRootBootstrapRequest{
		ManagedRoot: plan.managedRoot, RepositoryID: plan.repository.ID, MarkerKey: markerKey, CreatedAt: service.utcNow(),
	})
	if err != nil || bootstrap.RepositoryMarkerDigest != record.evidence.RepositoryMarkerDigest ||
		bootstrap.ManagedRootIdentityDigest != record.evidence.ManagedRootIdentityDigest {
		return fmt.Errorf("%w: Rsync versioning root drift", backupasset.ErrConflict)
	}
	if err := provider.ValidateRsyncManagedRootSeparation(ctx, plan.managedRoot, plan.link.EncryptedLegacyLocator); err != nil {
		return fmt.Errorf("%w: Rsync versioning legacy target overlap", backupasset.ErrConflict)
	}
	if err := provider.ValidateRsyncManagedRootSeparation(ctx, plan.managedRoot, plan.task.RsyncSource); err != nil {
		return fmt.Errorf("%w: Rsync versioning source overlap", backupasset.ErrConflict)
	}
	preflighter, err := provider.NewRsyncTreePreflighter(service.now, defaultRsyncVersioningPreflightTTL)
	if err != nil {
		return err
	}
	current, err := preflighter.PreflightManagedRoot(ctx, plan.managedRoot, provider.RsyncTreePreflightRequest{
		TaskID: record.evidence.TaskID, ExpectedTaskRevision: record.evidence.ExpectedTaskRevision, Mode: record.evidence.Mode,
		LocalSourceRoot: plan.task.RsyncSource, RepositoryMarkerDigest: record.evidence.RepositoryMarkerDigest,
		CapabilityRevision: record.evidence.CapabilityRevision,
	})
	if err != nil || current.ManagedRootIdentityDigest != record.evidence.ManagedRootIdentityDigest ||
		current.SourceIdentityDigest != record.evidence.SourceIdentityDigest || current.HardlinkVerified != record.evidence.HardlinkVerified ||
		current.RenameNoReplaceVerified != record.evidence.RenameNoReplaceVerified || current.DirectoryFsyncVerified != record.evidence.DirectoryFsyncVerified {
		return fmt.Errorf("%w: Rsync versioning preflight drift", backupasset.ErrConflict)
	}
	return nil
}

func sameRsyncVersioningBinding(left, right bindingDocument) bool {
	return left.Version == right.Version && left.Provider == right.Provider && left.IdentityClass == right.IdentityClass &&
		left.TaskID == right.TaskID && left.NodeID == right.NodeID && left.IdentitySalt == right.IdentitySalt &&
		left.Locator == right.Locator && left.Secret == right.Secret && left.Backend == right.Backend &&
		left.RangeProven == right.RangeProven && left.ConfigSource == right.ConfigSource &&
		left.NativeRepositoryID == right.NativeRepositoryID && left.AdapterRevision == right.AdapterRevision &&
		slices.Equal(left.EndpointFacts, right.EndpointFacts)
}

func (service *Service) loadRsyncVersioningPreflightPlan(ctx context.Context, request backupasset.RsyncVersioningPreflightRequest) (rsyncVersioningPreflightPlan, error) {
	var plan rsyncVersioningPreflightPlan
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var taskEntity model.Task
		if err := tx.Preload("Node").Where("id = ? AND archived_at IS NULL", request.TaskID).First(&taskEntity).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: Rsync versioning Task", backupasset.ErrNotFound)
			}
			return fmt.Errorf("load Rsync versioning Task: %w", err)
		}
		if bindingProviderForTask(taskEntity) != backupasset.ProviderRsync || strings.TrimSpace(taskEntity.Node.Host) != "" {
			return fmt.Errorf("%w: Rsync versioning preflight is unavailable", backupasset.ErrCapabilityUnavailable)
		}
		revision, err := managedRsyncTaskRevision(taskEntity)
		if err != nil {
			return err
		}
		if revision != request.ExpectedTaskRevision {
			return fmt.Errorf("%w: Rsync versioning Task revision changed", backupasset.ErrConflict)
		}

		var link model.TaskRepositoryLink
		if err := tx.Where("task_id = ? AND unlinked_at IS NULL", taskEntity.ID).First(&link).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: Rsync versioning legacy link", backupasset.ErrNotFound)
			}
			return fmt.Errorf("load Rsync versioning legacy link: %w", err)
		}
		if link.TaskID == nil || *link.TaskID != taskEntity.ID || link.PublicationMode != string(backupasset.PublicationLegacyMutable) ||
			strings.TrimSpace(link.EncryptedLegacyLocator) == "" || link.EncryptedLegacyLocator != taskEntity.RsyncTarget {
			return fmt.Errorf("%w: Rsync versioning requires an exact legacy link", backupasset.ErrConflict)
		}

		var repository model.BackupRepository
		if err := tx.First(&repository, "id = ?", link.RepositoryID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: Rsync versioning repository", backupasset.ErrNotFound)
			}
			return fmt.Errorf("load Rsync versioning repository: %w", err)
		}
		if repository.ProviderKind != string(backupasset.ProviderRsync) || repository.VersionMode != string(backupasset.VersionMutableHead) ||
			repository.ImmutabilityLevel != string(backupasset.ImmutabilityMutable) || repository.CapabilityRevision <= 0 {
			return fmt.Errorf("%w: Rsync versioning repository changed", backupasset.ErrConflict)
		}

		var binding model.RepositoryAccessBinding
		if err := tx.Where("repository_id = ? AND status = ?", repository.ID, bindingStatusActive).First(&binding).Error; err != nil {
			return fmt.Errorf("load Rsync versioning binding: %w", err)
		}
		stored, err := decodeStoredBindingDocument(binding.EncryptedConfig)
		if err != nil {
			return err
		}
		if stored.V1 == nil || stored.ManagedRsyncV2 != nil || validateBindingDocument(*stored.V1) != nil ||
			stored.V1.Provider != backupasset.ProviderRsync || stored.V1.TaskID != taskEntity.ID || stored.V1.NodeID != taskEntity.NodeID ||
			stored.V1.Locator != link.EncryptedLegacyLocator {
			return fmt.Errorf("%w: Rsync versioning binding changed", backupasset.ErrConflict)
		}
		managedRoot, err := deriveRsyncManagedRoot(link.EncryptedLegacyLocator)
		if err != nil {
			return err
		}
		plan = rsyncVersioningPreflightPlan{task: taskEntity, repository: repository, link: link, binding: *stored.V1, managedRoot: managedRoot}
		return nil
	})
	if err != nil {
		return rsyncVersioningPreflightPlan{}, err
	}
	return plan, nil
}

func deriveRsyncManagedRoot(legacyLocator string) (string, error) {
	if util.IsRemotePathSpec(legacyLocator) {
		return "", fmt.Errorf("%w: managed Rsync requires a local legacy target", backupasset.ErrCapabilityUnavailable)
	}
	legacyRoot := filepath.Clean(strings.TrimSpace(legacyLocator))
	if legacyRoot == "." || legacyRoot == string(filepath.Separator) || !filepath.IsAbs(legacyRoot) {
		return "", fmt.Errorf("%w: invalid Rsync legacy target", backupasset.ErrInvalidState)
	}
	return legacyRoot + ".xirang-rsync-v1", nil
}

func (service *Service) rsyncVersioningMarkerKey(ctx context.Context, repositoryID string) ([]byte, error) {
	if service == nil || service.keyring == nil || backupasset.ValidateOpaqueID(repositoryID) != nil {
		return nil, fmt.Errorf("%w: managed Rsync marker key is unavailable", backupasset.ErrInvalidState)
	}
	material, err := service.keyring.Ensure(ctx, backupasset.KeyDomainRecoveryCleanupOwnership)
	if err != nil {
		return nil, err
	}
	return rsyncOwnershipDigest(material.Key, "xirang.rsync.tree.marker-key.v1", repositoryID), nil
}

func rsyncVersioningCapacityBucket(value uint64) backupasset.RsyncVersioningEstimateBucket {
	if value == 0 {
		return backupasset.RsyncVersioningEstimateUnknown
	}
	if value < rsyncVersioningConstrainedBytes {
		return backupasset.RsyncVersioningEstimateConstrained
	}
	return backupasset.RsyncVersioningEstimateAvailable
}

func rsyncVersioningInodeBucket(value uint64) backupasset.RsyncVersioningEstimateBucket {
	if value == 0 {
		return backupasset.RsyncVersioningEstimateUnknown
	}
	if value < rsyncVersioningConstrainedInodes {
		return backupasset.RsyncVersioningEstimateConstrained
	}
	return backupasset.RsyncVersioningEstimateAvailable
}
