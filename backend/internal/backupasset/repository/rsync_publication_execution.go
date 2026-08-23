package repository

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	rsyncParentLeaseOwner                     = "rsync-publication-parent"
	managedRsyncPointLocatorVersion           = 1
	managedRsyncPreparedAttemptVersion        = 1
	managedRsyncCommitEvidenceVersion         = 1
	maxManagedRsyncPreparedAttemptRecordBytes = 24 * 1024
)

type managedRsyncPublicationRuntime struct {
	repository model.BackupRepository
	task       model.Task
	link       model.TaskRepositoryLink
	binding    managedRsyncBindingDocumentV2
}

// managedRsyncPointLocatorV1 stays encrypted at rest. It binds a database
// point to the opaque final component and the marker authenticated by the
// provider without placing a filesystem root in a public model field.
type managedRsyncPointLocatorV1 struct {
	Version                   int    `json:"version"`
	Provider                  string `json:"provider"`
	RepositoryID              string `json:"repository_id"`
	RecoveryPointID           string `json:"recovery_point_id"`
	FinalComponent            string `json:"final_component"`
	ManagedRootIdentityDigest string `json:"managed_root_identity_digest"`
	CommitMarkerDigest        string `json:"commit_marker_digest"`
	TaggedAttempt             string `json:"tagged_attempt"`
	ChildFenceDigest          string `json:"child_fence_digest"`
}

// managedRsyncPreparedAttemptRecordV1 is the only provider-locator shape
// permitted while a managed Rsync point is preparing. It lets restart
// reconciliation recover the exact typed attempt and bind its final marker to
// the child fence that originally authorized provider work.
type managedRsyncPreparedAttemptRecordV1 struct {
	Version          int    `json:"version"`
	TaggedAttempt    string `json:"tagged_attempt"`
	ChildFenceDigest string `json:"child_fence_digest"`
}

type rsyncPublicationExecution struct {
	service     *PublicationService
	token       publication.AdmissionToken
	attempt     provider.RsyncTreeAttemptV1
	audit       backupasset.PublicationAuditContext
	binding     managedRsyncBindingDocumentV2
	markerKey   []byte
	childFence  backupasset.LeaseFence
	parentFence *backupasset.LeaseFence

	context        context.Context
	cancel         context.CancelCauseFunc
	deadlineCancel context.CancelFunc
	heartbeat      chan struct{}
	heartbeatW     sync.WaitGroup
	stopOnce       sync.Once
	closeOnce      sync.Once
}

// PublishImportedRsyncBaseline publishes one migration-only full copy from a
// validated local legacy root. It uses the same fenced session, provider
// commit, and reconciliation path as a producing managed Rsync TaskRun; only
// the immutable point semantics differ.
func (service *PublicationService) PublishImportedRsyncBaseline(ctx context.Context, run publication.Run, source provider.RsyncTreeCommandSource) (publication.Outcome, error) {
	if service == nil || service.registry == nil || run.Task.ID == 0 || run.TaskRunID == 0 ||
		backupasset.ValidatePublicationAuditContext(run.Audit) != nil || source.Remote != nil ||
		strings.TrimSpace(source.LocalPath) == "" || !filepath.IsAbs(filepath.Clean(source.LocalPath)) {
		return publication.Outcome{}, fmt.Errorf("%w: imported Rsync baseline request is invalid", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	transferObserved := false
	defer func() {
		if !transferObserved {
			// A migration-only TaskRun must never remain a synthetic running
			// transfer after preparation or provider setup fails. This update is
			// deliberately best effort so it cannot replace the original error.
			_ = service.recordImportedRsyncBaselineTaskRun(context.Background(), run.Task.ID, run.TaskRunID, provider.ProviderExecutionResult{}, backupasset.ErrInvalidState)
		}
	}()
	run.ImportedBaseline = true
	session, err := service.Prepare(ctx, run)
	if err != nil {
		return publication.Outcome{}, err
	}
	if session == nil || session.Mode() != publication.ModeEvidence || session.Attempt() == nil {
		if session != nil {
			_ = session.Reject(ctx, backupasset.FailurePublicationPreconditionMissing)
		}
		return publication.Outcome{}, fmt.Errorf("%w: imported Rsync baseline evidence session is unavailable", backupasset.ErrInvalidState)
	}
	resolved := false
	defer func() {
		if !resolved {
			_ = session.Abandon(backupasset.ErrPublicationSessionAbandoned)
		}
	}()
	attempt, err := session.Attempt().RsyncTreeAttempt()
	if err != nil || !attempt.ImportedBaseline || attempt.SeedFullCopy || attempt.PublicationMode != backupasset.PublicationVersionedFullCopy {
		_ = session.Reject(ctx, backupasset.FailurePublicationPreconditionMissing)
		if err != nil {
			return publication.Outcome{}, err
		}
		return publication.Outcome{}, fmt.Errorf("%w: imported Rsync baseline attempt is invalid", backupasset.ErrInvalidState)
	}
	inputProvider, ok := session.(interface {
		RsyncTreePublicationInput() (provider.RsyncTreePublicationInput, error)
	})
	if !ok {
		_ = session.Reject(ctx, backupasset.FailurePublicationPreconditionMissing)
		return publication.Outcome{}, fmt.Errorf("%w: imported Rsync baseline input is unavailable", backupasset.ErrInvalidState)
	}
	input, err := inputProvider.RsyncTreePublicationInput()
	if err != nil {
		_ = session.Reject(ctx, backupasset.FailurePublicationPreconditionMissing)
		return publication.Outcome{}, err
	}
	if input.Source.LocalPath != "" || input.Source.Remote != nil {
		_ = session.Reject(ctx, backupasset.FailurePublicationPreconditionMissing)
		return publication.Outcome{}, fmt.Errorf("%w: imported Rsync baseline input already has a source", backupasset.ErrInvalidState)
	}
	input.Source = source
	strategy, err := service.registry.PublicationStrategy(backupasset.ProviderRsync)
	if err != nil {
		_ = session.Reject(ctx, backupasset.FailurePublicationPreconditionMissing)
		return publication.Outcome{}, err
	}
	commandCtx := session.Context()
	if commandCtx == nil {
		_ = session.Reject(ctx, backupasset.FailurePublicationPreconditionMissing)
		return publication.Outcome{}, fmt.Errorf("%w: imported Rsync baseline command context is unavailable", backupasset.ErrInvalidState)
	}
	prepared, err := strategy.Prepare(commandCtx, provider.PublicationPrepareRequest{Attempt: *session.Attempt(), RsyncTreeInput: &input})
	if err != nil {
		_ = session.Reject(ctx, backupasset.FailurePublicationPreconditionMissing)
		return publication.Outcome{}, err
	}
	result, runErr := strategy.Execute(commandCtx, prepared, provider.PublicationProgress{})
	transferObserved = true
	if err := service.recordImportedRsyncBaselineTaskRun(context.Background(), run.Task.ID, run.TaskRunID, result, runErr); err != nil {
		_ = session.Abandon(backupasset.ErrPublicationSessionAbandoned)
		return publication.Outcome{}, err
	}
	if runErr != nil || result.Completion != backupasset.CompletionKnownExitZero || result.ExitCode != 0 || result.EvidenceCode != "" || result.ProviderCommit == nil {
		switch result.Completion {
		case backupasset.CompletionKnownNonzero:
			_ = session.Fail(ctx, backupasset.FailureProviderNonzeroExit)
		case backupasset.CompletionOutcomeUnknown:
			_ = session.Defer(ctx, publication.Deferral{Completion: backupasset.CompletionOutcomeUnknown, Code: backupasset.FailureProviderOutcomeUnknown})
		default:
			_ = session.Reject(ctx, backupasset.FailurePublicationPreconditionMissing)
		}
		resolved = true
		return publication.Outcome{}, fmt.Errorf("%w: imported Rsync baseline provider completion is not proven", backupasset.ErrConflict)
	}
	commit, err := strategy.RecordCommit(commandCtx, prepared, result)
	if err != nil {
		_ = session.Abandon(backupasset.ErrPublicationSessionAbandoned)
		resolved = true
		return publication.Outcome{}, err
	}
	outcome, err := session.RecordProviderCommit(ctx, commit)
	if err != nil {
		_ = session.Abandon(backupasset.ErrPublicationSessionAbandoned)
		resolved = true
		return publication.Outcome{}, err
	}
	if outcome.State != backupasset.RecoveryPointVerifying || !outcome.ProviderCommitRecorded {
		resolved = true
		return publication.Outcome{}, fmt.Errorf("%w: imported Rsync baseline provider commit is not verifying", backupasset.ErrConflict)
	}
	outcome, err = service.ProcessPoint(ctx, outcome.RecoveryPointID)
	resolved = true
	if err != nil {
		return publication.Outcome{}, err
	}
	if outcome.State != backupasset.RecoveryPointCommitted || !outcome.ProviderCommitRecorded {
		return publication.Outcome{}, fmt.Errorf("%w: imported Rsync baseline verification did not commit", backupasset.ErrConflict)
	}
	return outcome, nil
}

// recordImportedRsyncBaselineTaskRun records only the copy process fact. A
// provider marker or database publication can still fail after an exit-zero
// copy, and must not retroactively turn that transfer into a failed TaskRun.
func (service *PublicationService) recordImportedRsyncBaselineTaskRun(ctx context.Context, taskID, taskRunID uint, result provider.ProviderExecutionResult, runErr error) error {
	if service == nil || service.db == nil || taskID == 0 || taskRunID == 0 {
		return fmt.Errorf("%w: imported Rsync baseline TaskRun dependencies are unavailable", backupasset.ErrInvalidState)
	}
	status, failureCode := importedRsyncBaselineTaskRunOutcome(result, runErr)
	return service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var taskRun model.TaskRun
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&taskRun, taskRunID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: imported Rsync baseline TaskRun", backupasset.ErrNotFound)
			}
			return fmt.Errorf("lock imported Rsync baseline TaskRun: %w", err)
		}
		if taskRun.TaskID != taskID || !model.IsTaskRunNodeSnapshotAuthoritative(taskRun.NodeIDSnapshot) {
			return fmt.Errorf("%w: imported Rsync baseline TaskRun lineage changed", backupasset.ErrConflict)
		}
		if !activeTaskRunStatus(taskRun.Status) {
			if taskRun.Status == status && taskRun.LastError == failureCode {
				return nil
			}
			return fmt.Errorf("%w: imported Rsync baseline TaskRun is already terminal", backupasset.ErrConflict)
		}
		now := service.now().UTC()
		duration := int64(0)
		if taskRun.StartedAt != nil && !taskRun.StartedAt.IsZero() && !now.Before(taskRun.StartedAt.UTC()) {
			duration = now.Sub(taskRun.StartedAt.UTC()).Milliseconds()
		}
		updates := map[string]any{
			"status": status, "finished_at": now, "duration_ms": duration, "last_error": failureCode, "updated_at": now,
		}
		if err := tx.Model(&model.TaskRun{}).Where("id = ? AND task_id = ? AND status IN ?", taskRun.ID, taskID, []string{"pending", "running", "retrying"}).Updates(updates).Error; err != nil {
			return fmt.Errorf("record imported Rsync baseline TaskRun: %w", err)
		}
		return nil
	})
}

func importedRsyncBaselineTaskRunOutcome(result provider.ProviderExecutionResult, runErr error) (string, string) {
	if runErr == nil && result.Completion == backupasset.CompletionKnownExitZero && result.ExitCode == 0 {
		return "success", ""
	}
	if errors.Is(runErr, context.Canceled) {
		return "canceled", string(backupasset.FailureProviderCanceled)
	}
	if errors.Is(runErr, context.DeadlineExceeded) {
		return "failed", string(backupasset.FailureProviderTimeout)
	}
	if result.Completion == backupasset.CompletionKnownNonzero || result.ExitCode != 0 {
		return "failed", string(backupasset.FailureProviderNonzeroExit)
	}
	if result.Completion == backupasset.CompletionOutcomeUnknown {
		return "failed", string(backupasset.FailureProviderOutcomeUnknown)
	}
	return "failed", string(backupasset.FailurePublicationPreconditionMissing)
}

func (service *PublicationService) prepareRsyncPublication(ctx context.Context, run publication.Run, token publication.AdmissionToken) (publication.Execution, error) {
	var links []model.TaskRepositoryLink
	if err := service.db.WithContext(ctx).Where("task_id = ? AND unlinked_at IS NULL", run.Task.ID).Find(&links).Error; err != nil {
		_ = token.Close()
		return nil, fmt.Errorf("load Rsync publication link: %w", err)
	}
	if len(links) == 0 {
		return service.prepareRsyncCompatibility(ctx, run, token)
	}
	if len(links) != 1 {
		_ = token.Close()
		return nil, fmt.Errorf("%w: ambiguous Rsync publication links", backupasset.ErrConflict)
	}
	switch backupasset.TaskPublicationMode(links[0].PublicationMode) {
	case backupasset.PublicationLegacyMutable:
		return service.prepareRsyncCompatibility(ctx, run, token)
	case backupasset.PublicationVersionedHardlink, backupasset.PublicationVersionedFullCopy:
		return service.prepareManagedRsyncEvidence(ctx, run, token)
	default:
		_ = token.Close()
		return nil, fmt.Errorf("%w: unsupported Rsync publication mode", backupasset.ErrConflict)
	}
}

func (service *PublicationService) prepareRsyncCompatibility(ctx context.Context, run publication.Run, token publication.AdmissionToken) (publication.Execution, error) {
	var taskEntity model.Task
	if err := service.db.WithContext(ctx).Where("archived_at IS NULL").First(&taskEntity, run.Task.ID).Error; err != nil {
		_ = token.Close()
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: Rsync compatibility Task", backupasset.ErrNotFound)
		}
		return nil, fmt.Errorf("load Rsync compatibility Task: %w", err)
	}
	allowed, err := service.history.legacyFallbackAllowed(ctx, taskEntity)
	if err != nil {
		_ = token.Close()
		return nil, err
	}
	if !allowed {
		service.metrics.ObserveLegacyBlocked(publication.OperationLegacyBackup)
		_ = token.Close()
		return nil, fmt.Errorf("%w: %s", backupasset.ErrForbidden, backupasset.FailureLegacyFallbackBlocked)
	}
	return newPublicationExecution(service, publication.ModeCompatibility, token, nil, nil, ctx), nil
}

func (service *PublicationService) prepareManagedRsyncEvidence(ctx context.Context, run publication.Run, token publication.AdmissionToken) (publication.Execution, error) {
	if _, err := service.registry.PublicationStrategy(backupasset.ProviderRsync); err != nil {
		_ = token.Close()
		return nil, err
	}
	runtime, err := service.loadExactManagedRsyncPublicationRuntime(ctx, run.Task.ID)
	if err != nil {
		_ = token.Close()
		return nil, err
	}
	leaseConfig, err := service.foundation.LeaseConfig()
	if err != nil {
		_ = token.Close()
		return nil, err
	}
	markerKey, err := service.rsyncMarkerKey(ctx, runtime.repository.ID)
	if err != nil {
		_ = token.Close()
		return nil, err
	}
	preparedAt := service.now().UTC()
	attempt, childLease, parentLease, err := service.prepareRsyncPoint(ctx, run, runtime, markerKey, leaseConfig, preparedAt)
	if err != nil {
		_ = token.Close()
		return nil, err
	}
	execution := newRsyncPublicationExecution(service, token, attempt, run.Audit, runtime.binding, markerKey, childLease.Fence, parentLease, leaseConfig, ctx)
	if err := service.writeRsyncPublicationAudit(ctx, run.Audit, backupasset.AuditActionRecoveryPointPublicationPrepare, backupasset.AuditOutcomeSuccess, attempt, publication.StageExecution, backupasset.RecoveryPointPreparing, "", ""); err != nil {
		service.metrics.ObserveAuditFailure(publication.StageExecution)
		_ = execution.Abandon(backupasset.ErrPublicationSessionAbandoned)
		return nil, err
	}
	return execution, nil
}

func (service *PublicationService) loadExactManagedRsyncPublicationRuntime(ctx context.Context, taskID uint) (managedRsyncPublicationRuntime, error) {
	if service == nil {
		return managedRsyncPublicationRuntime{}, fmt.Errorf("%w: managed Rsync publication runtime is unavailable", backupasset.ErrInvalidState)
	}
	return loadExactManagedRsyncPublicationRuntime(ctx, service.db, taskID)
}

func loadExactManagedRsyncPublicationRuntime(ctx context.Context, db *gorm.DB, taskID uint) (managedRsyncPublicationRuntime, error) {
	if db == nil || taskID == 0 {
		return managedRsyncPublicationRuntime{}, fmt.Errorf("%w: managed Rsync publication runtime is unavailable", backupasset.ErrInvalidState)
	}
	var taskEntity model.Task
	if err := db.WithContext(ctx).Where("archived_at IS NULL").First(&taskEntity, taskID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return managedRsyncPublicationRuntime{}, fmt.Errorf("%w: managed Rsync publication Task", backupasset.ErrNotFound)
		}
		return managedRsyncPublicationRuntime{}, fmt.Errorf("load managed Rsync publication Task: %w", err)
	}
	if bindingProviderForTask(taskEntity) != backupasset.ProviderRsync {
		return managedRsyncPublicationRuntime{}, fmt.Errorf("%w: managed Rsync publication requires an Rsync Task", backupasset.ErrInvalidState)
	}
	var link model.TaskRepositoryLink
	if err := db.WithContext(ctx).Where("task_id = ? AND unlinked_at IS NULL", taskID).First(&link).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return managedRsyncPublicationRuntime{}, fmt.Errorf("%w: %s", backupasset.ErrForbidden, backupasset.FailureLegacyFallbackBlocked)
		}
		return managedRsyncPublicationRuntime{}, fmt.Errorf("load managed Rsync publication link: %w", err)
	}
	mode := backupasset.TaskPublicationMode(link.PublicationMode)
	version, semantics, state, err := backupasset.MapPublicationMode(backupasset.ProviderRsync, mode)
	if err != nil || semantics != backupasset.PointXirangManifest || state != backupasset.RecoveryPointPreparing {
		if err != nil {
			return managedRsyncPublicationRuntime{}, err
		}
		return managedRsyncPublicationRuntime{}, fmt.Errorf("%w: managed Rsync publication link mode is invalid", backupasset.ErrConflict)
	}
	var repository model.BackupRepository
	if err := db.WithContext(ctx).First(&repository, "id = ?", link.RepositoryID).Error; err != nil {
		return managedRsyncPublicationRuntime{}, fmt.Errorf("load managed Rsync publication repository: %w", err)
	}
	if repository.ProviderKind != string(backupasset.ProviderRsync) || repository.VersionMode != string(version) ||
		repository.ImmutabilityLevel != string(backupasset.ImmutabilityXirangManaged) || repository.RepositoryIdentity == nil ||
		!strings.HasPrefix(*repository.RepositoryIdentity, provider.ScopedIdentityPrefix(backupasset.ProviderRsync)) {
		return managedRsyncPublicationRuntime{}, fmt.Errorf("%w: managed Rsync repository contract mismatch", backupasset.ErrConflict)
	}
	var binding model.RepositoryAccessBinding
	if err := db.WithContext(ctx).Where("repository_id = ? AND status = ?", repository.ID, bindingStatusActive).First(&binding).Error; err != nil {
		return managedRsyncPublicationRuntime{}, fmt.Errorf("load managed Rsync publication binding: %w", err)
	}
	stored, err := decodeStoredBindingDocument(binding.EncryptedConfig)
	if err != nil {
		return managedRsyncPublicationRuntime{}, err
	}
	if stored.ManagedRsyncV2 == nil || stored.V1 != nil {
		return managedRsyncPublicationRuntime{}, fmt.Errorf("%w: managed Rsync V2 binding required", backupasset.ErrConflict)
	}
	document := *stored.ManagedRsyncV2
	if err := validateManagedRsyncBindingAssociation(document, managedRsyncBindingAssociation{
		Task: taskEntity, Link: link, RootMarkerDigest: document.RootMarkerDigest,
	}); err != nil {
		return managedRsyncPublicationRuntime{}, err
	}
	if document.RollbackPrepared {
		return managedRsyncPublicationRuntime{}, fmt.Errorf("%w: managed Rsync rollback is prepared", backupasset.ErrForbidden)
	}
	expectedIdentity, err := managedRsyncRepositoryIdentity(document)
	if err != nil {
		return managedRsyncPublicationRuntime{}, err
	}
	if *repository.RepositoryIdentity != expectedIdentity {
		return managedRsyncPublicationRuntime{}, fmt.Errorf("%w: managed Rsync repository identity drift", backupasset.ErrConflict)
	}
	return managedRsyncPublicationRuntime{repository: repository, task: taskEntity, link: link, binding: document}, nil
}

func (service *PublicationService) prepareRsyncPoint(ctx context.Context, run publication.Run, runtime managedRsyncPublicationRuntime, markerKey []byte, leaseConfig backupasset.LeaseConfig, preparedAt time.Time) (provider.RsyncTreeAttemptV1, backupasset.Lease, *backupasset.Lease, error) {
	var attempt provider.RsyncTreeAttemptV1
	var childLease backupasset.Lease
	var parentLease *backupasset.Lease
	if len(markerKey) == 0 {
		return provider.RsyncTreeAttemptV1{}, backupasset.Lease{}, nil, fmt.Errorf("%w: managed Rsync marker key is unavailable", backupasset.ErrInvalidState)
	}
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var taskEntity model.Task
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Preload("Node").Where("archived_at IS NULL").First(&taskEntity, runtime.task.ID).Error; err != nil {
			return fmt.Errorf("lock managed Rsync publication Task: %w", err)
		}
		revision, err := managedRsyncTaskRevision(taskEntity)
		if err != nil {
			return err
		}
		var taskRun model.TaskRun
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&taskRun, run.TaskRunID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: TaskRun", backupasset.ErrNotFound)
			}
			return fmt.Errorf("lock managed Rsync publication TaskRun: %w", err)
		}
		if !authoritativeTaskRunForTask(taskRun, taskEntity) || !activeTaskRunStatus(taskRun.Status) {
			return fmt.Errorf("%w: TaskRun is not active for managed Rsync publication", backupasset.ErrConflict)
		}
		var link model.TaskRepositoryLink
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND task_id = ? AND unlinked_at IS NULL", runtime.link.ID, taskEntity.ID).First(&link).Error; err != nil {
			return fmt.Errorf("lock managed Rsync publication link: %w", err)
		}
		if link.RepositoryID != runtime.repository.ID || link.PublicationMode != string(runtime.binding.PublicationMode) {
			return fmt.Errorf("%w: managed Rsync publication link changed", backupasset.ErrConflict)
		}
		var repository model.BackupRepository
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&repository, "id = ?", runtime.repository.ID).Error; err != nil {
			return fmt.Errorf("lock managed Rsync publication repository: %w", err)
		}
		attemptMode, err := managedRsyncAttemptMode(runtime.binding)
		if err != nil {
			return err
		}
		if run.ImportedBaseline {
			if runtime.binding.SeedFullCopyRequired {
				return fmt.Errorf("%w: imported Rsync baseline cannot consume a pending hardlink seed", backupasset.ErrConflict)
			}
			attemptMode = backupasset.PublicationVersionedFullCopy
		}
		version, semantics, initialState, err := backupasset.MapPublicationMode(backupasset.ProviderRsync, runtime.binding.PublicationMode)
		if err != nil {
			return err
		}
		if repository.ProviderKind != string(backupasset.ProviderRsync) || repository.VersionMode != string(version) ||
			repository.ImmutabilityLevel != string(backupasset.ImmutabilityXirangManaged) {
			return fmt.Errorf("%w: managed Rsync publication repository changed", backupasset.ErrConflict)
		}
		if run.ImportedBaseline {
			semantics = backupasset.PointImportedBaseline
		}
		var binding model.RepositoryAccessBinding
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("repository_id = ? AND status = ?", repository.ID, bindingStatusActive).First(&binding).Error; err != nil {
			return fmt.Errorf("lock managed Rsync publication binding: %w", err)
		}
		stored, err := decodeStoredBindingDocument(binding.EncryptedConfig)
		if err != nil || stored.ManagedRsyncV2 == nil || stored.V1 != nil {
			if err != nil {
				return err
			}
			return fmt.Errorf("%w: managed Rsync publication binding changed", backupasset.ErrConflict)
		}
		document := *stored.ManagedRsyncV2
		if err := validateManagedRsyncBindingAssociation(document, managedRsyncBindingAssociation{
			Task: taskEntity, Link: link, RootMarkerDigest: document.RootMarkerDigest,
		}); err != nil || document != runtime.binding {
			if err != nil {
				return err
			}
			return fmt.Errorf("%w: managed Rsync publication binding changed", backupasset.ErrConflict)
		}
		pointID, err := deriveRecoveryPointID(link.ID, taskRun.ID)
		if err != nil {
			return err
		}
		startedAt := run.StartedAt.UTC()
		if startedAt.IsZero() {
			if taskRun.StartedAt == nil || taskRun.StartedAt.IsZero() {
				return fmt.Errorf("%w: managed Rsync publication TaskRun start time is missing", backupasset.ErrInvalidState)
			}
			startedAt = taskRun.StartedAt.UTC()
		}
		trigger := run.Trigger
		if trigger == "" {
			trigger = taskRun.TriggerType
		}
		deadline := preparedAt.Add(leaseConfig.AbsoluteDeadline).UTC()
		lineage, err := managedRsyncPublicationLineageForRun(link, taskEntity, taskRun, trigger, run.ChainRunID, startedAt, preparedAt, deadline, attemptMode)
		if err != nil {
			return err
		}
		encodedLineage, err := backupasset.EncodePublicationLineage(lineage)
		if err != nil {
			return err
		}
		emptyConsistency, err := backupasset.EncodePublicationConsistency(backupasset.PublicationConsistencyV1{Version: 1})
		if err != nil {
			return err
		}
		point := model.RecoveryPoint{
			ID: pointID, RepositoryID: repository.ID, ProducingTaskID: &taskEntity.ID, ProducingTaskRunID: &taskRun.ID,
			ProducingTaskNameSnapshot: taskEntity.Name, ProducingNodeIDSnapshot: taskEntity.NodeID, ProducingNodeNameSnapshot: taskEntity.Node.Name,
			LineageJSON: encodedLineage, Semantics: string(semantics), State: string(initialState), ManifestDigestAlgorithm: "sha256",
			ConsistencyJSON: emptyConsistency, FidelityJSON: "{}", CapabilityRevision: repository.CapabilityRevision,
			CapabilitiesJSON: repository.CapabilitiesJSON, ImmutabilityLevel: string(backupasset.ImmutabilityXirangManaged),
			PhysicalAvailability: string(backupasset.PhysicalUnknown), HoldState: string(backupasset.HoldNone), CreatedAt: preparedAt, UpdatedAt: preparedAt,
		}
		var existing model.RecoveryPoint
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", pointID).Limit(1).Find(&existing)
		if result.Error != nil {
			return fmt.Errorf("load deterministic managed Rsync point: %w", result.Error)
		}
		if result.RowsAffected == 1 {
			if !samePreparedPublicationPoint(existing, point) {
				return fmt.Errorf("%w: deterministic managed Rsync point immutable fields differ", backupasset.ErrConflict)
			}
			return fmt.Errorf("%w: deterministic managed Rsync point already exists", backupasset.ErrPublicationInProgress)
		}
		if err := tx.Create(&point).Error; err != nil {
			if isPublicationProducingRunConflict(err) {
				return fmt.Errorf("%w: producing TaskRun is already claimed", backupasset.ErrConflict)
			}
			return fmt.Errorf("create deterministic managed Rsync point: %w", err)
		}
		childLease, err = service.lease.AcquireTx(ctx, tx, backupasset.AcquireLeaseRequest{
			RecoveryPointID: point.ID, HolderType: backupasset.LeaseHolderPointPublication, OwnerID: publicationLeaseOwner, AbsoluteDeadline: lineage.PointDeadlineAt,
		})
		if err != nil {
			if errors.Is(err, backupasset.ErrLeaseHeld) {
				return fmt.Errorf("%w: managed Rsync execution publication lease", backupasset.ErrPublicationInProgress)
			}
			return err
		}
		attemptID, err := backupasset.NewOpaqueID()
		if err != nil {
			return err
		}
		attempt = provider.RsyncTreeAttemptV1{
			RepositoryID: repository.ID, TaskRepositoryLinkID: link.ID, RecoveryPointID: point.ID, AttemptID: attemptID,
			TaskID: taskEntity.ID, TaskRunID: taskRun.ID, PublicationMode: attemptMode, SeedFullCopy: runtime.binding.SeedFullCopyRequired,
			ImportedBaseline: run.ImportedBaseline,
			PointDeadlineAt:  lineage.PointDeadlineAt, ExpectedTaskRevision: revision, RepositoryMarkerDigest: document.RootMarkerDigest,
			ManagedRootIdentityDigest: document.ManagedRootIdentityDigest, StagingComponent: point.ID + "." + attemptID, FinalComponent: point.ID,
			CommandProfileVersion: 1, PreflightID: document.PreflightID, PreflightDigest: document.PreflightDigest,
		}
		if attempt.PublicationMode == backupasset.PublicationVersionedHardlink {
			parent, lease, err := service.acquireRsyncParentLeaseTx(ctx, tx, repository, link, attempt.PointDeadlineAt)
			if err != nil {
				return err
			}
			parentLease = &lease
			attempt.ParentRecoveryPointID = parent.ID
			attempt.ParentManifestDigest = parent.ManifestDigest
			consistency, err := backupasset.DecodePublicationConsistency(parent.ConsistencyJSON)
			if err != nil {
				return err
			}
			attempt.ParentCommitDigest = consistency.ProviderCommitDigest
		}
		if err := attempt.Validate(); err != nil {
			return err
		}
		preparedRecord, err := encodeManagedRsyncPreparedAttemptRecord(attempt, rsyncChildFenceDigest(markerKey, childLease.Fence))
		if err != nil {
			return err
		}
		point.EncryptedProviderLocator = preparedRecord
		point.UpdatedAt = preparedAt
		if err := tx.Save(&point).Error; err != nil {
			return fmt.Errorf("persist managed Rsync prepared attempt: %w", err)
		}
		return nil
	})
	if err != nil {
		return provider.RsyncTreeAttemptV1{}, backupasset.Lease{}, nil, err
	}
	return attempt, childLease, parentLease, nil
}

func managedRsyncPublicationLineageForRun(link model.TaskRepositoryLink, taskEntity model.Task, taskRun model.TaskRun, trigger, chainRunID string, startedAt, preparedAt, deadline time.Time, attemptMode backupasset.TaskPublicationMode) (backupasset.PublicationLineageV1, error) {
	linkMode := backupasset.TaskPublicationMode(link.PublicationMode)
	if linkMode != backupasset.PublicationVersionedHardlink && linkMode != backupasset.PublicationVersionedFullCopy {
		return backupasset.PublicationLineageV1{}, fmt.Errorf("%w: managed Rsync lineage mode is invalid", backupasset.ErrInvalidState)
	}
	if attemptMode != linkMode && (linkMode != backupasset.PublicationVersionedHardlink || attemptMode != backupasset.PublicationVersionedFullCopy) {
		return backupasset.PublicationLineageV1{}, fmt.Errorf("%w: managed Rsync attempt mode is invalid", backupasset.ErrInvalidState)
	}
	chainDigest := ""
	if chainRunID != "" {
		sum := sha256.Sum256([]byte(chainRunID))
		chainDigest = hex.EncodeToString(sum[:])
	}
	return backupasset.PublicationLineageV1{
		Version: 1, TaskRepositoryLinkID: link.ID, TaskID: taskEntity.ID, TaskRunID: taskRun.ID, Trigger: trigger,
		ChainRunIDPresent: chainRunID != "", ChainRunIDDigest: chainDigest, PublicationMode: string(attemptMode),
		PointCodecVersion: 1, TagCodecVersion: 0, StartedAt: startedAt.UTC(), PreparedAt: preparedAt.UTC(), PointDeadlineAt: deadline.UTC(),
	}, nil
}

func (service *PublicationService) acquireRsyncParentLeaseTx(ctx context.Context, tx *gorm.DB, repository model.BackupRepository, link model.TaskRepositoryLink, childDeadline time.Time) (model.RecoveryPoint, backupasset.Lease, error) {
	var candidates []model.RecoveryPoint
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("repository_id = ? AND state = ? AND semantics IN ?", repository.ID, backupasset.RecoveryPointCommitted, []string{string(backupasset.PointXirangManifest), string(backupasset.PointImportedBaseline)}).
		Order("committed_at DESC, id DESC").Find(&candidates).Error; err != nil {
		return model.RecoveryPoint{}, backupasset.Lease{}, fmt.Errorf("load managed Rsync parent point: %w", err)
	}
	for _, candidate := range candidates {
		if candidate.CommittedAt == nil || !candidate.CommittedAt.UTC().Before(childDeadline.UTC()) || candidate.ManifestDigestAlgorithm != "sha256" ||
			!isLowerHex64(candidate.ManifestDigest) {
			continue
		}
		lineage, err := backupasset.DecodePublicationLineage(candidate.LineageJSON)
		if err != nil || lineage.TaskRepositoryLinkID != link.ID || lineage.PublicationMode == string(backupasset.PublicationNativeSnapshot) {
			continue
		}
		consistency, err := backupasset.DecodePublicationConsistency(candidate.ConsistencyJSON)
		if err != nil || consistency.Provider != backupasset.ProviderRsync || !isLowerHex64(consistency.ProviderCommitDigest) {
			continue
		}
		locator, err := decodeManagedRsyncPointLocator(candidate.EncryptedProviderLocator)
		if err != nil || locator.RepositoryID != repository.ID || locator.RecoveryPointID != candidate.ID || locator.FinalComponent != candidate.ID {
			continue
		}
		lease, err := service.lease.AcquireTx(ctx, tx, backupasset.AcquireLeaseRequest{
			RecoveryPointID: candidate.ID, HolderType: backupasset.LeaseHolderRsyncParent, OwnerID: rsyncParentLeaseOwner,
		})
		if err != nil {
			if errors.Is(err, backupasset.ErrLeaseHeld) {
				return model.RecoveryPoint{}, backupasset.Lease{}, fmt.Errorf("%w: managed Rsync parent lease", backupasset.ErrPublicationInProgress)
			}
			return model.RecoveryPoint{}, backupasset.Lease{}, err
		}
		return candidate, lease, nil
	}
	return model.RecoveryPoint{}, backupasset.Lease{}, fmt.Errorf("%w: managed Rsync hardlink parent is unavailable", backupasset.ErrConflict)
}

func newRsyncPublicationExecution(service *PublicationService, token publication.AdmissionToken, attempt provider.RsyncTreeAttemptV1, audit backupasset.PublicationAuditContext, binding managedRsyncBindingDocumentV2, markerKey []byte, childFence backupasset.LeaseFence, parentLease *backupasset.Lease, config backupasset.LeaseConfig, parent context.Context) *rsyncPublicationExecution {
	if parent == nil {
		parent = context.Background()
	}
	effectiveDeadline := attempt.PointDeadlineAt.UTC()
	var parentFence *backupasset.LeaseFence
	if parentLease != nil {
		fence := parentLease.Fence
		parentFence = &fence
		if parentLease.AbsoluteDeadline.UTC().Before(effectiveDeadline) {
			effectiveDeadline = parentLease.AbsoluteDeadline.UTC()
		}
	}
	bounded, deadlineCancel := context.WithDeadline(parent, effectiveDeadline)
	commandContext, cancel := context.WithCancelCause(bounded)
	execution := &rsyncPublicationExecution{
		service: service, token: token, attempt: attempt, audit: audit, binding: binding, markerKey: append([]byte(nil), markerKey...),
		childFence: childFence, parentFence: parentFence, context: commandContext, cancel: cancel, deadlineCancel: deadlineCancel, heartbeat: make(chan struct{}),
	}
	execution.heartbeatW.Add(1)
	go execution.runHeartbeat(config)
	return execution
}

func (execution *rsyncPublicationExecution) Mode() publication.ExecutionMode {
	return publication.ModeEvidence
}

func (execution *rsyncPublicationExecution) Attempt() *provider.TaggedPublicationAttempt {
	if execution == nil {
		return nil
	}
	copy := provider.NewRsyncTreePublicationAttempt(execution.attempt)
	return &copy
}

func (execution *rsyncPublicationExecution) Context() context.Context {
	if execution == nil || execution.context == nil {
		return context.Background()
	}
	return execution.context
}

// RsyncTreePublicationInput returns the repository-owned material required by
// the managed-tree Provider strategy. It intentionally leaves Source empty:
// the Task executor supplies the typed current-task source immediately before
// invoking the strategy, without persisting or logging it here.
func (execution *rsyncPublicationExecution) RsyncTreePublicationInput() (provider.RsyncTreePublicationInput, error) {
	if execution == nil || execution.service == nil || execution.service.foundation == nil || execution.context == nil {
		return provider.RsyncTreePublicationInput{}, fmt.Errorf("%w: managed Rsync provider input is unavailable", backupasset.ErrInvalidState)
	}
	if err := execution.context.Err(); err != nil {
		return provider.RsyncTreePublicationInput{}, err
	}
	if err := execution.attempt.Validate(); err != nil {
		return provider.RsyncTreePublicationInput{}, err
	}
	config, err := execution.service.foundation.PublicationConfig()
	if err != nil {
		return provider.RsyncTreePublicationInput{}, err
	}
	maxBytes := config.ManifestMaxBytes
	if maxBytes > provider.MaxRsyncTreeMetadataBytes {
		maxBytes = provider.MaxRsyncTreeMetadataBytes
	}
	input := provider.RsyncTreePublicationInput{
		ManagedRoot: execution.binding.ManagedRootLocator, CaptureACLs: true, CaptureXattrs: true,
		MarkerKey:         append([]byte(nil), execution.markerKey...),
		SourceFingerprint: managedRsyncSourceFingerprint(execution.markerKey, execution.binding, execution.attempt.RecoveryPointID),
		ChildFenceDigest:  rsyncChildFenceDigest(execution.markerKey, execution.childFence),
		ManifestLimits: provider.ManifestLimits{
			Timeout: config.ManifestTimeout, MaxBytes: maxBytes, MaxEntries: config.ManifestMaxEntries,
			MaxRecordBytes: config.ManifestMaxRecordBytes, MaxDepth: config.ManifestMaxDepth,
		},
		MaxCommandOutputBytes: config.BackupStreamMaxBytes,
	}
	return input, nil
}

func (execution *rsyncPublicationExecution) Cancel(cause error) error {
	if execution == nil {
		return nil
	}
	if !validPublicationCancelCause(cause) {
		return fmt.Errorf("%w: unsafe managed Rsync publication cancellation cause", backupasset.ErrInvalidState)
	}
	execution.stopOnce.Do(func() {
		execution.cancel(cause)
		close(execution.heartbeat)
		execution.heartbeatW.Wait()
		if execution.deadlineCancel != nil {
			execution.deadlineCancel()
		}
	})
	return nil
}

func (execution *rsyncPublicationExecution) Abandon(cause error) error {
	if err := execution.Cancel(cause); err != nil {
		return err
	}
	execution.closeAdmission()
	return nil
}

func (execution *rsyncPublicationExecution) CompleteCompatibility(context.Context) error {
	return fmt.Errorf("%w: compatibility completion is unavailable", backupasset.ErrInvalidState)
}

func (execution *rsyncPublicationExecution) RecordProviderCommit(ctx context.Context, evidence provider.ProviderCommit) (publication.Outcome, error) {
	if execution == nil || execution.service == nil {
		return publication.Outcome{}, fmt.Errorf("%w: managed Rsync execution is unavailable", backupasset.ErrInvalidState)
	}
	commit, err := evidence.RsyncTreeCommit()
	if err != nil {
		return publication.Outcome{}, err
	}
	if err := execution.Cancel(nil); err != nil {
		return publication.Outcome{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	commitContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), sshutil.CommandExecutionJoinTimeout)
	defer cancel()
	outcome, transitioned, err := execution.service.recordRsyncProviderCommit(commitContext, execution.attempt, execution.binding, execution.markerKey, execution.childFence, execution.parentFence, commit)
	if err != nil {
		return publication.Outcome{}, err
	}
	if transitioned {
		execution.service.metrics.ObserveOutcome(backupasset.ProviderRsync, publication.StageExecution, backupasset.PublicationOutcomeSuccess)
		_ = execution.service.tryWake(outcome.RecoveryPointID)
		if err := execution.service.writeRsyncPublicationAudit(commitContext, execution.audit, backupasset.AuditActionRecoveryPointPublicationCommit, backupasset.AuditOutcomeSuccess, execution.attempt, publication.StageExecution, backupasset.RecoveryPointVerifying, "", ""); err != nil {
			execution.service.metrics.ObserveAuditFailure(publication.StageExecution)
		}
	}
	execution.closeAdmission()
	return outcome, nil
}

func (execution *rsyncPublicationExecution) Defer(ctx context.Context, deferral publication.Deferral) error {
	if execution == nil || execution.service == nil {
		return fmt.Errorf("%w: managed Rsync execution is unavailable", backupasset.ErrInvalidState)
	}
	if err := backupasset.ValidatePublicationDeferral(deferral.Completion, deferral.Code); err != nil {
		return err
	}
	if err := execution.Cancel(nil); err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return execution.recordDeferral(ctx, deferral)
}

func (execution *rsyncPublicationExecution) recordDeferral(ctx context.Context, deferral publication.Deferral) error {
	updated := false
	err := execution.service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var point model.RecoveryPoint
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&point, "id = ?", execution.attempt.RecoveryPointID).Error; err != nil {
			return fmt.Errorf("lock deferred managed Rsync point: %w", err)
		}
		if point.State != string(backupasset.RecoveryPointPreparing) && point.State != string(backupasset.RecoveryPointVerifying) {
			return fmt.Errorf("%w: deferred managed Rsync point is terminal", backupasset.ErrConflict)
		}
		if err := execution.validateFencesTx(ctx, tx); err != nil {
			return err
		}
		consistency, err := backupasset.DecodePublicationConsistency(point.ConsistencyJSON)
		if err != nil {
			return err
		}
		if consistency.Completion == deferral.Completion && consistency.Code == deferral.Code {
			return nil
		}
		now := execution.service.now().UTC()
		consistency.PublicationRevision++
		consistency.AttemptCount++
		consistency.Completion = deferral.Completion
		consistency.Code = deferral.Code
		consistency.LastAttemptAt = &now
		encoded, err := backupasset.EncodePublicationConsistency(consistency)
		if err != nil {
			return err
		}
		point.ConsistencyJSON = encoded
		point.UpdatedAt = now
		if err := tx.Save(&point).Error; err != nil {
			return err
		}
		updated = true
		return nil
	})
	if err != nil {
		return err
	}
	if updated {
		if err := execution.service.writeRsyncPublicationAudit(ctx, execution.audit, backupasset.AuditActionRecoveryPointPublicationVerify, backupasset.AuditOutcomeFailure, execution.attempt, publication.StageExecution, backupasset.RecoveryPointPreparing, string(deferral.Code), deferral.Code); err != nil {
			execution.service.metrics.ObserveAuditFailure(publication.StageExecution)
		}
	}
	execution.closeAdmission()
	return nil
}

func (execution *rsyncPublicationExecution) Reject(ctx context.Context, code backupasset.PublicationFailureCode) error {
	if code != backupasset.FailurePublicationPreconditionMissing {
		return fmt.Errorf("%w: invalid pre-command managed Rsync rejection", backupasset.ErrInvalidState)
	}
	return execution.terminalFail(ctx, code, false)
}

func (execution *rsyncPublicationExecution) Fail(ctx context.Context, code backupasset.PublicationFailureCode) error {
	if !rsyncPostCommandPublicationFailure(code) {
		return fmt.Errorf("%w: invalid managed Rsync post-command failure", backupasset.ErrInvalidState)
	}
	return execution.terminalFail(ctx, code, true)
}

func (execution *rsyncPublicationExecution) terminalFail(ctx context.Context, code backupasset.PublicationFailureCode, recordCompletion bool) error {
	if execution == nil || execution.service == nil {
		return fmt.Errorf("%w: managed Rsync execution is unavailable", backupasset.ErrInvalidState)
	}
	if err := execution.Cancel(nil); err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	transitioned := false
	err := execution.service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var point model.RecoveryPoint
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&point, "id = ?", execution.attempt.RecoveryPointID).Error; err != nil {
			return fmt.Errorf("lock failed managed Rsync point: %w", err)
		}
		if point.State == string(backupasset.RecoveryPointFailed) {
			consistency, err := backupasset.DecodePublicationConsistency(point.ConsistencyJSON)
			if err != nil {
				return err
			}
			if terminalFailureReplayMatches(consistency, code, recordCompletion) {
				return nil
			}
			return fmt.Errorf("%w: managed Rsync failed point records different terminal facts", backupasset.ErrConflict)
		}
		if point.State != string(backupasset.RecoveryPointPreparing) && point.State != string(backupasset.RecoveryPointVerifying) {
			return fmt.Errorf("%w: managed Rsync failed point is terminal", backupasset.ErrConflict)
		}
		if err := execution.validateFencesTx(ctx, tx); err != nil {
			return err
		}
		if recordCompletion {
			consistency, err := backupasset.DecodePublicationConsistency(point.ConsistencyJSON)
			if err != nil {
				return err
			}
			now := execution.service.now().UTC()
			consistency.PublicationRevision++
			consistency.AttemptCount++
			consistency.Completion = backupasset.CompletionKnownNonzero
			consistency.Code = code
			consistency.LastAttemptAt = &now
			encoded, err := backupasset.EncodePublicationConsistency(consistency)
			if err != nil {
				return err
			}
			point.ConsistencyJSON = encoded
		}
		if err := backupasset.ValidateRecoveryPointTransition(rsyncPointProfile(point, execution.attempt.PublicationMode), backupasset.RecoveryPointFailed); err != nil {
			return err
		}
		point.State = string(backupasset.RecoveryPointFailed)
		point.UpdatedAt = execution.service.now().UTC()
		if err := tx.Save(&point).Error; err != nil {
			return err
		}
		if err := execution.releaseFencesTx(ctx, tx); err != nil {
			return err
		}
		transitioned = true
		return nil
	})
	if err != nil {
		return err
	}
	if transitioned {
		if err := execution.service.writeRsyncPublicationAudit(ctx, execution.audit, backupasset.AuditActionRecoveryPointPublicationFail, backupasset.AuditOutcomeFailure, execution.attempt, publication.StageExecution, backupasset.RecoveryPointFailed, string(code), code); err != nil {
			execution.service.metrics.ObserveAuditFailure(publication.StageExecution)
		}
	}
	execution.closeAdmission()
	return nil
}

func rsyncPostCommandPublicationFailure(code backupasset.PublicationFailureCode) bool {
	switch code {
	case backupasset.FailureProviderNonzeroExit,
		backupasset.FailureProviderTimeout,
		backupasset.FailureProviderCanceled,
		backupasset.FailureProviderResourceLimit,
		backupasset.FailureProviderOutcomeUnknown,
		backupasset.FailureManifestPartial,
		backupasset.FailureManifestUnavailable,
		backupasset.FailureLeaseFenceLost,
		backupasset.FailurePublicationDeadlineExceeded,
		backupasset.FailureRepositoryIdentityDrift:
		return true
	default:
		return false
	}
}

func (execution *rsyncPublicationExecution) validateFencesTx(ctx context.Context, tx *gorm.DB) error {
	if err := execution.service.lease.ValidateFenceTx(ctx, tx, execution.childFence); err != nil {
		return err
	}
	if execution.parentFence != nil {
		if err := execution.service.lease.ValidateFenceTx(ctx, tx, *execution.parentFence); err != nil {
			return err
		}
	}
	return nil
}

func (execution *rsyncPublicationExecution) releaseFencesTx(ctx context.Context, tx *gorm.DB) error {
	if execution.parentFence != nil {
		if err := execution.service.lease.ReleaseTx(ctx, tx, *execution.parentFence); err != nil {
			return err
		}
	}
	return execution.service.lease.ReleaseTx(ctx, tx, execution.childFence)
}

func (execution *rsyncPublicationExecution) runHeartbeat(config backupasset.LeaseConfig) {
	defer execution.heartbeatW.Done()
	ticker := time.NewTicker(config.Heartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-execution.context.Done():
			return
		case <-execution.heartbeat:
			return
		case <-ticker.C:
			if _, err := execution.service.lease.Renew(execution.context, execution.childFence); err != nil {
				execution.cancel(err)
				return
			}
			if execution.parentFence != nil {
				if _, err := execution.service.lease.Renew(execution.context, *execution.parentFence); err != nil {
					execution.cancel(err)
					return
				}
			}
		}
	}
}

func (execution *rsyncPublicationExecution) closeAdmission() {
	if execution == nil || execution.token == nil {
		return
	}
	execution.closeOnce.Do(func() { _ = execution.token.Close() })
}

func (service *PublicationService) recordRsyncProviderCommit(ctx context.Context, attempt provider.RsyncTreeAttemptV1, binding managedRsyncBindingDocumentV2, markerKey []byte, childFence backupasset.LeaseFence, parentFence *backupasset.LeaseFence, evidence provider.RsyncTreeCommitV1) (publication.Outcome, bool, error) {
	if err := validateRsyncCommitEvidence(attempt, binding, markerKey, childFence, evidence); err != nil {
		return publication.Outcome{}, false, err
	}
	var outcome publication.Outcome
	transitioned := false
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var point model.RecoveryPoint
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&point, "id = ?", attempt.RecoveryPointID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: managed Rsync publication point", backupasset.ErrNotFound)
			}
			return fmt.Errorf("lock managed Rsync provider commit point: %w", err)
		}
		if point.RepositoryID != attempt.RepositoryID || point.ProducingTaskID == nil || *point.ProducingTaskID != attempt.TaskID ||
			point.ProducingTaskRunID == nil || *point.ProducingTaskRunID != attempt.TaskRunID {
			return fmt.Errorf("%w: managed Rsync provider commit point lineage mismatch", backupasset.ErrConflict)
		}
		lineage, err := backupasset.DecodePublicationLineage(point.LineageJSON)
		if err != nil {
			return err
		}
		if lineage.TaskRepositoryLinkID != attempt.TaskRepositoryLinkID || lineage.TaskID != attempt.TaskID || lineage.TaskRunID != attempt.TaskRunID ||
			lineage.PublicationMode != string(attempt.PublicationMode) || !lineage.PointDeadlineAt.Equal(attempt.PointDeadlineAt.UTC()) {
			return fmt.Errorf("%w: managed Rsync provider commit immutable lineage mismatch", backupasset.ErrConflict)
		}
		if point.State == string(backupasset.RecoveryPointVerifying) || point.State == string(backupasset.RecoveryPointCommitted) {
			matching, replay, err := rsyncProviderCommitReplayMatches(point, attempt, evidence)
			if err != nil {
				return err
			}
			if !matching {
				return fmt.Errorf("%w: managed Rsync provider commit replay differs", backupasset.ErrConflict)
			}
			outcome = replay
			return nil
		}
		if point.State != string(backupasset.RecoveryPointPreparing) {
			return fmt.Errorf("%w: managed Rsync provider commit point is not preparing", backupasset.ErrConflict)
		}
		if err := service.lease.ValidateFenceTx(ctx, tx, childFence); err != nil {
			return err
		}
		if parentFence != nil {
			if err := service.lease.ValidateFenceTx(ctx, tx, *parentFence); err != nil {
				return err
			}
		}
		locatorPayload, err := encodeManagedRsyncPointLocatorForAttempt(attempt, evidence)
		if err != nil {
			return err
		}
		commitDigest, err := canonicalRsyncProviderCommitDigest(attempt, evidence)
		if err != nil {
			return err
		}
		fidelityPayload, err := json.Marshal(struct {
			Version int    `json:"version"`
			Digest  string `json:"digest"`
		}{Version: managedRsyncCommitEvidenceVersion, Digest: evidence.FidelityDigest})
		if err != nil {
			return fmt.Errorf("encode managed Rsync fidelity evidence: %w", err)
		}
		consistency := backupasset.PublicationConsistencyV1{
			Version: 1, Provider: backupasset.ProviderRsync, RepositoryIdentityDigest: binding.ManagedRootIdentityDigest,
			ProviderCommitDigest: commitDigest, CapabilityRevision: point.CapabilityRevision,
		}
		encodedConsistency, err := backupasset.EncodePublicationConsistency(consistency)
		if err != nil {
			return err
		}
		if err := backupasset.ValidateRecoveryPointTransition(rsyncPointProfile(point, attempt.PublicationMode), backupasset.RecoveryPointVerifying); err != nil {
			return err
		}
		capturedAt := evidence.ProviderCommittedAt.UTC()
		point.EncryptedProviderLocator = locatorPayload
		point.SourceFingerprint = evidence.SourceFingerprint
		point.ManifestDigestAlgorithm = evidence.ManifestDigestAlgorithm
		point.ManifestDigest = evidence.ManifestDigest
		point.EntryCount = int64(evidence.ManifestEntryCount)
		point.LogicalBytes = int64(evidence.LogicalBytes)
		point.FidelityJSON = string(fidelityPayload)
		point.ConsistencyJSON = encodedConsistency
		point.CapturedAt = &capturedAt
		point.State = string(backupasset.RecoveryPointVerifying)
		point.UpdatedAt = service.now().UTC()
		if err := tx.Save(&point).Error; err != nil {
			if isPublicationManagedTreeSourceConflict(err) {
				return fmt.Errorf("%w: managed Rsync tree is already claimed", backupasset.ErrConflict)
			}
			return fmt.Errorf("save managed Rsync provider commit point: %w", err)
		}
		var repository model.BackupRepository
		if err := tx.First(&repository, "id = ?", point.RepositoryID).Error; err != nil {
			return fmt.Errorf("load managed Rsync provider commit repository: %w", err)
		}
		if err := upsertManagedRsyncHistoryLatchesTx(ctx, tx, repository, point, evidence.ProviderCommittedAt.UTC(), service.now().UTC()); err != nil {
			return err
		}
		if parentFence != nil {
			if err := service.lease.ReleaseTx(ctx, tx, *parentFence); err != nil {
				return err
			}
		}
		if err := service.lease.ReleaseTx(ctx, tx, childFence); err != nil {
			return err
		}
		outcome = publication.Outcome{
			RepositoryID: point.RepositoryID, RecoveryPointID: point.ID, TaskID: attempt.TaskID, TaskRunID: attempt.TaskRunID,
			State: backupasset.RecoveryPointVerifying, CapturedAt: capturedAt, ProviderCommitRecorded: true,
		}
		transitioned = true
		return nil
	})
	if err != nil {
		return publication.Outcome{}, false, err
	}
	return outcome, transitioned, nil
}

func validateRsyncCommitEvidence(attempt provider.RsyncTreeAttemptV1, binding managedRsyncBindingDocumentV2, markerKey []byte, childFence backupasset.LeaseFence, evidence provider.RsyncTreeCommitV1) error {
	if err := attempt.Validate(); err != nil {
		return err
	}
	if err := binding.validateForAttempt(attempt); err != nil {
		return err
	}
	if err := evidence.Validate(); err != nil {
		return err
	}
	if evidence.RepositoryID != attempt.RepositoryID || evidence.TaskRepositoryLinkID != attempt.TaskRepositoryLinkID ||
		evidence.RecoveryPointID != attempt.RecoveryPointID || evidence.AttemptID != attempt.AttemptID || evidence.PublicationMode != attempt.PublicationMode ||
		!evidence.PointDeadlineAt.Equal(attempt.PointDeadlineAt.UTC()) || !evidence.ProviderCommittedAt.UTC().Before(attempt.PointDeadlineAt.UTC()) ||
		evidence.SourceFingerprint != managedRsyncSourceFingerprint(markerKey, binding, attempt.RecoveryPointID) ||
		evidence.ChildFenceDigest != rsyncChildFenceDigest(markerKey, childFence) {
		return fmt.Errorf("%w: managed Rsync provider commit evidence mismatch", backupasset.ErrConflict)
	}
	if evidence.ManifestEntryCount > uint64(^uint64(0)>>1) || evidence.LogicalBytes > uint64(^uint64(0)>>1) {
		return fmt.Errorf("%w: managed Rsync provider commit exceeds model bounds", backupasset.ErrInvalidState)
	}
	if attempt.PublicationMode == backupasset.PublicationVersionedHardlink {
		if evidence.ParentRecoveryPointID != attempt.ParentRecoveryPointID || evidence.ParentCommitDigest != attempt.ParentCommitDigest {
			return fmt.Errorf("%w: managed Rsync provider commit parent mismatch", backupasset.ErrConflict)
		}
	}
	return nil
}

func (document managedRsyncBindingDocumentV2) validateForAttempt(attempt provider.RsyncTreeAttemptV1) error {
	if err := validateManagedRsyncBindingDocumentV2(document); err != nil {
		return err
	}
	fullCopyException := document.PublicationMode == backupasset.PublicationVersionedHardlink &&
		attempt.PublicationMode == backupasset.PublicationVersionedFullCopy && (attempt.SeedFullCopy || attempt.ImportedBaseline)
	normalMode := document.PublicationMode == attempt.PublicationMode && !attempt.SeedFullCopy &&
		(!attempt.ImportedBaseline || attempt.PublicationMode == backupasset.PublicationVersionedFullCopy)
	modeMatches := normalMode || fullCopyException
	if document.RepositoryID != attempt.RepositoryID || document.TaskRepositoryLinkID != attempt.TaskRepositoryLinkID ||
		!modeMatches || document.SeedFullCopyRequired && (!attempt.SeedFullCopy || attempt.ImportedBaseline) ||
		attempt.ImportedBaseline && document.SeedFullCopyRequired || document.RootMarkerDigest != attempt.RepositoryMarkerDigest ||
		document.ManagedRootIdentityDigest != attempt.ManagedRootIdentityDigest || document.PreflightID != attempt.PreflightID ||
		document.PreflightDigest != attempt.PreflightDigest {
		return fmt.Errorf("%w: managed Rsync attempt binding mismatch", backupasset.ErrConflict)
	}
	return nil
}

func managedRsyncAttemptMode(binding managedRsyncBindingDocumentV2) (backupasset.TaskPublicationMode, error) {
	if err := validateManagedRsyncBindingDocumentV2(binding); err != nil {
		return "", err
	}
	if binding.SeedFullCopyRequired {
		return backupasset.PublicationVersionedFullCopy, nil
	}
	return binding.PublicationMode, nil
}

func canonicalRsyncProviderCommitDigest(attempt provider.RsyncTreeAttemptV1, evidence provider.RsyncTreeCommitV1) (string, error) {
	writer := backupasset.NewCanonicalSHA256()
	writer.String("xirang.rsync.tree.provider-commit.v1")
	writer.String(attempt.RepositoryID)
	writer.String(attempt.TaskRepositoryLinkID)
	writer.String(attempt.RecoveryPointID)
	writer.String(attempt.AttemptID)
	writer.String(string(attempt.PublicationMode))
	writer.String(evidence.ParentRecoveryPointID)
	writer.String(evidence.ParentCommitDigest)
	writer.String(evidence.ManifestDigestAlgorithm)
	writer.String(evidence.ManifestDigest)
	writer.Uint64(evidence.ManifestEntryCount)
	writer.Uint64(evidence.LogicalBytes)
	writer.String(evidence.FidelityDigest)
	writer.String(evidence.SourceFingerprint)
	writer.Int64(evidence.ProviderCommittedAt.UTC().UnixNano())
	writer.String(evidence.CommitMarkerDigest)
	writer.String(evidence.ChildFenceDigest)
	writer.Int64(evidence.PointDeadlineAt.UTC().UnixNano())
	return writer.HexDigest()
}

func rsyncProviderCommitReplayMatches(point model.RecoveryPoint, attempt provider.RsyncTreeAttemptV1, evidence provider.RsyncTreeCommitV1) (bool, publication.Outcome, error) {
	locator, err := decodeManagedRsyncPointLocator(point.EncryptedProviderLocator)
	if err != nil {
		return false, publication.Outcome{}, err
	}
	if locator.RepositoryID != attempt.RepositoryID || locator.RecoveryPointID != attempt.RecoveryPointID || locator.FinalComponent != attempt.FinalComponent ||
		locator.ManagedRootIdentityDigest != attempt.ManagedRootIdentityDigest || locator.CommitMarkerDigest != evidence.CommitMarkerDigest ||
		point.SourceFingerprint != evidence.SourceFingerprint || point.ManifestDigestAlgorithm != evidence.ManifestDigestAlgorithm ||
		point.ManifestDigest != evidence.ManifestDigest || point.EntryCount != int64(evidence.ManifestEntryCount) || point.LogicalBytes != int64(evidence.LogicalBytes) {
		return false, publication.Outcome{}, nil
	}
	persistedAttempt, err := provider.DecodeRsyncTreeAttemptV1(locator.TaggedAttempt)
	if err != nil {
		return false, publication.Outcome{}, err
	}
	if persistedAttempt != attempt || locator.ChildFenceDigest != evidence.ChildFenceDigest {
		return false, publication.Outcome{}, nil
	}
	return true, publication.Outcome{
		RepositoryID: point.RepositoryID, RecoveryPointID: point.ID, TaskID: attempt.TaskID, TaskRunID: attempt.TaskRunID,
		State: backupasset.RecoveryPointState(point.State), CapturedAt: evidence.ProviderCommittedAt.UTC(), ProviderCommitRecorded: true,
	}, nil
}

func encodeManagedRsyncPreparedAttemptRecord(attempt provider.RsyncTreeAttemptV1, childFenceDigest string) (string, error) {
	taggedAttempt, err := provider.EncodePublicationAttempt(provider.NewRsyncTreePublicationAttempt(attempt))
	if err != nil {
		return "", err
	}
	record := managedRsyncPreparedAttemptRecordV1{
		Version:          managedRsyncPreparedAttemptVersion,
		TaggedAttempt:    taggedAttempt,
		ChildFenceDigest: childFenceDigest,
	}
	if err := validateManagedRsyncPreparedAttemptRecord(record); err != nil {
		return "", err
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return "", fmt.Errorf("encode managed Rsync prepared attempt: %w", err)
	}
	return string(payload), nil
}

func decodeManagedRsyncPreparedAttemptRecord(payload string) (provider.RsyncTreeAttemptV1, string, error) {
	if len(payload) == 0 || len(payload) > maxManagedRsyncPreparedAttemptRecordBytes {
		return provider.RsyncTreeAttemptV1{}, "", fmt.Errorf("%w: invalid managed Rsync prepared attempt record size", backupasset.ErrInvalidState)
	}
	if err := rejectDuplicateBindingDocumentMembers(payload); err != nil {
		return provider.RsyncTreeAttemptV1{}, "", fmt.Errorf("%w: invalid managed Rsync prepared attempt record", backupasset.ErrInvalidState)
	}
	var record managedRsyncPreparedAttemptRecordV1
	decoder := json.NewDecoder(strings.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return provider.RsyncTreeAttemptV1{}, "", fmt.Errorf("%w: invalid managed Rsync prepared attempt record", backupasset.ErrInvalidState)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return provider.RsyncTreeAttemptV1{}, "", fmt.Errorf("%w: trailing managed Rsync prepared attempt record", backupasset.ErrInvalidState)
	}
	if err := validateManagedRsyncPreparedAttemptRecord(record); err != nil {
		return provider.RsyncTreeAttemptV1{}, "", err
	}
	taggedAttempt, err := provider.DecodePublicationAttempt(record.TaggedAttempt)
	if err != nil {
		return provider.RsyncTreeAttemptV1{}, "", err
	}
	attempt, err := taggedAttempt.RsyncTreeAttempt()
	if err != nil {
		return provider.RsyncTreeAttemptV1{}, "", err
	}
	return attempt, record.ChildFenceDigest, nil
}

func validateManagedRsyncPreparedAttemptRecord(record managedRsyncPreparedAttemptRecordV1) error {
	if record.Version != managedRsyncPreparedAttemptVersion || record.TaggedAttempt == "" || !isLowerHex64(record.ChildFenceDigest) {
		return fmt.Errorf("%w: invalid managed Rsync prepared attempt record", backupasset.ErrInvalidState)
	}
	return nil
}

func encodeManagedRsyncPointLocator(locator managedRsyncPointLocatorV1) (string, error) {
	if err := validateManagedRsyncPointLocator(locator); err != nil {
		return "", err
	}
	payload, err := json.Marshal(locator)
	if err != nil {
		return "", fmt.Errorf("encode managed Rsync point locator: %w", err)
	}
	return string(payload), nil
}

func encodeManagedRsyncPointLocatorForAttempt(attempt provider.RsyncTreeAttemptV1, evidence provider.RsyncTreeCommitV1) (string, error) {
	if err := attempt.Validate(); err != nil {
		return "", err
	}
	if err := evidence.Validate(); err != nil {
		return "", err
	}
	taggedAttempt, err := provider.EncodePublicationAttempt(provider.NewRsyncTreePublicationAttempt(attempt))
	if err != nil {
		return "", err
	}
	return encodeManagedRsyncPointLocator(managedRsyncPointLocatorV1{
		Version: managedRsyncPointLocatorVersion, Provider: string(backupasset.ProviderRsync), RepositoryID: attempt.RepositoryID,
		RecoveryPointID: attempt.RecoveryPointID, FinalComponent: attempt.FinalComponent,
		ManagedRootIdentityDigest: attempt.ManagedRootIdentityDigest, CommitMarkerDigest: evidence.CommitMarkerDigest,
		TaggedAttempt: taggedAttempt, ChildFenceDigest: evidence.ChildFenceDigest,
	})
}

func decodeManagedRsyncPointLocator(payload string) (managedRsyncPointLocatorV1, error) {
	var locator managedRsyncPointLocatorV1
	decoder := json.NewDecoder(strings.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&locator); err != nil {
		return managedRsyncPointLocatorV1{}, fmt.Errorf("%w: invalid managed Rsync point locator", backupasset.ErrInvalidState)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return managedRsyncPointLocatorV1{}, fmt.Errorf("%w: trailing managed Rsync point locator", backupasset.ErrInvalidState)
	}
	if err := validateManagedRsyncPointLocator(locator); err != nil {
		return managedRsyncPointLocatorV1{}, err
	}
	return locator, nil
}

func validateManagedRsyncPointLocator(locator managedRsyncPointLocatorV1) error {
	if locator.Version != managedRsyncPointLocatorVersion || locator.Provider != string(backupasset.ProviderRsync) ||
		backupasset.ValidateOpaqueID(locator.RepositoryID) != nil || backupasset.ValidateOpaqueID(locator.RecoveryPointID) != nil ||
		locator.FinalComponent != locator.RecoveryPointID || !isLowerHex64(locator.ManagedRootIdentityDigest) || !isLowerHex64(locator.CommitMarkerDigest) ||
		!isLowerHex64(locator.ChildFenceDigest) {
		return fmt.Errorf("%w: invalid managed Rsync point locator", backupasset.ErrInvalidState)
	}
	attempt, err := provider.DecodeRsyncTreeAttemptV1(locator.TaggedAttempt)
	if err != nil {
		return err
	}
	if attempt.RepositoryID != locator.RepositoryID || attempt.RecoveryPointID != locator.RecoveryPointID ||
		attempt.FinalComponent != locator.FinalComponent || attempt.ManagedRootIdentityDigest != locator.ManagedRootIdentityDigest {
		return fmt.Errorf("%w: managed Rsync point locator attempt mismatch", backupasset.ErrInvalidState)
	}
	return nil
}

func (service *PublicationService) rsyncMarkerKey(ctx context.Context, repositoryID string) ([]byte, error) {
	if service == nil || service.keyring == nil || backupasset.ValidateOpaqueID(repositoryID) != nil {
		return nil, fmt.Errorf("%w: managed Rsync marker key is unavailable", backupasset.ErrInvalidState)
	}
	material, err := service.keyring.Ensure(ctx, backupasset.KeyDomainRecoveryCleanupOwnership)
	if err != nil {
		return nil, err
	}
	return rsyncOwnershipDigest(material.Key, "xirang.rsync.tree.marker-key.v1", repositoryID), nil
}

func managedRsyncSourceFingerprint(markerKey []byte, binding managedRsyncBindingDocumentV2, pointID string) string {
	if len(markerKey) == 0 || validateManagedRsyncBindingDocumentV2(binding) != nil || backupasset.ValidateOpaqueID(pointID) != nil {
		return ""
	}
	return hex.EncodeToString(rsyncOwnershipDigest(markerKey, "xirang.rsync.tree.source-fingerprint.v1", binding.RepositoryID, binding.ManagedRootIdentityDigest, pointID))
}

func rsyncChildFenceDigest(markerKey []byte, fence backupasset.LeaseFence) string {
	if len(markerKey) == 0 || backupasset.ValidateOpaqueID(fence.LeaseID) != nil || backupasset.ValidateOpaqueID(fence.RecoveryPointID) != nil ||
		backupasset.ValidateOpaqueID(fence.AttemptID) != nil || !isLowerHex64(fence.FenceToken) || fence.HolderType != backupasset.LeaseHolderPointPublication || fence.OwnerID != publicationLeaseOwner {
		return ""
	}
	return hex.EncodeToString(rsyncOwnershipDigest(markerKey, "xirang.rsync.tree.child-fence.v1", fence.LeaseID, fence.RecoveryPointID, string(fence.HolderType), fence.OwnerID, fence.AttemptID, fence.FenceToken))
}

func rsyncOwnershipDigest(key []byte, domain string, values ...string) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(domain))
	for _, value := range values {
		_, _ = mac.Write([]byte{0})
		_, _ = mac.Write([]byte(value))
	}
	return mac.Sum(nil)
}

func rsyncPointProfile(point model.RecoveryPoint, mode backupasset.TaskPublicationMode) backupasset.RecoveryPointProfile {
	version, semantics, _, err := backupasset.MapPublicationMode(backupasset.ProviderRsync, mode)
	if err != nil {
		return backupasset.RecoveryPointProfile{}
	}
	if pointSemantics := backupasset.PointVersionSemantics(point.Semantics); pointSemantics == backupasset.PointXirangManifest || pointSemantics == backupasset.PointImportedBaseline {
		semantics = pointSemantics
	}
	return backupasset.RecoveryPointProfile{
		VersionMode: version, Semantics: semantics, State: backupasset.RecoveryPointState(point.State),
		Immutability: backupasset.ImmutabilityLevel(point.ImmutabilityLevel), Availability: backupasset.PhysicalAvailability(point.PhysicalAvailability),
		Hold: backupasset.HoldState(point.HoldState),
	}
}

func isPublicationManagedTreeSourceConflict(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	if !strings.Contains(message, "unique constraint") && !strings.Contains(message, "duplicate key") && !strings.Contains(message, "sqlstate 23505") {
		return false
	}
	return strings.Contains(message, "idx_recovery_points_managed_tree_source_unique") ||
		(strings.Contains(message, "recovery_points.repository_id") && strings.Contains(message, "recovery_points.source_fingerprint"))
}

func upsertManagedRsyncHistoryLatchesTx(ctx context.Context, tx *gorm.DB, repository model.BackupRepository, point model.RecoveryPoint, firstSeenAt, now time.Time) error {
	if tx == nil || backupasset.ValidateOpaqueID(repository.ID) != nil || repository.RepositoryIdentity == nil || !isLowerHex64(point.SourceFingerprint) ||
		(point.Semantics != string(backupasset.PointXirangManifest) && point.Semantics != string(backupasset.PointImportedBaseline)) {
		return fmt.Errorf("%w: invalid managed Rsync history latch input", backupasset.ErrInvalidState)
	}
	identityDigest := digestText(*repository.RepositoryIdentity)
	rows := []model.BackupAssetManagedHistoryLatch{
		{
			ID: "managed-history-installation", Scope: managedHistoryLatchScopeInstallation, FirstSemantics: point.Semantics,
			FirstOrigin: "rsync_provider_commit_v1", FirstSeenAt: firstSeenAt.UTC(), CreatedAt: now.UTC(), UpdatedAt: now.UTC(),
		},
		{
			ID: "managed-history-repository-" + repository.ID, Scope: managedHistoryLatchScopeRepository, RepositoryID: &repository.ID,
			RepositoryIdentityDigest: identityDigest, FirstSemantics: point.Semantics, FirstOrigin: "rsync_provider_commit_v1",
			FirstSeenAt: firstSeenAt.UTC(), CreatedAt: now.UTC(), UpdatedAt: now.UTC(),
		},
	}
	for _, row := range rows {
		if err := tx.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
			return fmt.Errorf("upsert managed Rsync history latch: %w", err)
		}
	}
	return nil
}

func (service *PublicationService) writeRsyncPublicationAudit(ctx context.Context, audit backupasset.PublicationAuditContext, action backupasset.AuditAction, outcome backupasset.AuditOutcome, attempt provider.RsyncTreeAttemptV1, stage publication.PublicationStage, status backupasset.RecoveryPointState, code string, failure backupasset.PublicationFailureCode) error {
	if service.audit == nil {
		return nil
	}
	if err := attempt.Validate(); err != nil || backupasset.ValidatePublicationAuditContext(audit) != nil || publication.ValidatePublicationStage(stage) != nil || status == "" {
		if err != nil {
			return err
		}
		return fmt.Errorf("%w: invalid managed Rsync publication audit input", backupasset.ErrInvalidState)
	}
	fields := map[backupasset.AuditField]any{
		backupasset.AuditFieldStage:         string(stage),
		backupasset.AuditFieldStatus:        string(status),
		backupasset.AuditFieldCorrelationID: audit.CorrelationID,
	}
	if code != "" {
		fields[backupasset.AuditFieldCode] = code
	}
	input := backupasset.AuditEventInput{
		Actor: audit.Actor, Action: action, Outcome: outcome, RepositoryID: attempt.RepositoryID, RecoveryPointID: attempt.RecoveryPointID,
		TaskID: &attempt.TaskID, TaskRunID: &attempt.TaskRunID, Fields: fields,
	}
	if failure != "" {
		input.FailureCode = string(failure)
	}
	return service.audit.Write(ctx, input)
}

var _ publication.Execution = (*rsyncPublicationExecution)(nil)
