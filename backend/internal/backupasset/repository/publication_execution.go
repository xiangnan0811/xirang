package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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

const publicationLeaseOwner = "point-publication"

type PublicationDependencies struct {
	DB                         *gorm.DB
	Foundation                 *backupasset.FoundationService
	Registry                   *provider.Registry
	Keyring                    *backupasset.Keyring
	Lease                      *backupasset.LeaseService
	Admission                  publication.Admission
	Metrics                    publication.Metrics
	Audit                      AssetAuditSink
	History                    *ManagedHistoryResolver
	Now                        func() time.Time
	TryWake                    func(string) bool
	RcloneNativeFactoryBuilder RcloneNativeFactoryBuilder
}

type RcloneNativeFactory interface {
	provider.STSAssumer
	provider.BootstrapDenyProbe
	provider.RcloneNativeClientFactory
	provider.RcloneNativeBaselineClientFactory
	BootstrapCredentialsExpire(context.Context) (bool, error)
}

type RcloneNativeFactoryBuilder func(
	context.Context,
	provider.RcloneNativeBootstrap,
	string,
	int,
) (RcloneNativeFactory, error)

// PublicationService owns transactionally fenced RecoveryPoint publication. It
// deliberately remains separate from the Repository HTTP service and Task
// Manager so Provider evidence can be reused by later backends.
type PublicationService struct {
	db                         *gorm.DB
	foundation                 *backupasset.FoundationService
	registry                   *provider.Registry
	keyring                    *backupasset.Keyring
	lease                      *backupasset.LeaseService
	admission                  publication.Admission
	metrics                    publication.Metrics
	audit                      AssetAuditSink
	history                    *ManagedHistoryResolver
	now                        func() time.Time
	tryWake                    func(string) bool
	rcloneNativeFactoryBuilder RcloneNativeFactoryBuilder
	rcloneNativeHealthCheck    func(context.Context, string) (provider.RcloneNativeHealthResult, error)
}

func NewPublicationService(dependencies PublicationDependencies) (*PublicationService, error) {
	if dependencies.DB == nil || dependencies.Foundation == nil || dependencies.Registry == nil || dependencies.Lease == nil ||
		dependencies.Admission == nil || dependencies.Metrics == nil || dependencies.History == nil {
		return nil, fmt.Errorf("%w: publication service dependencies are unavailable", backupasset.ErrInvalidState)
	}
	if dependencies.Now == nil {
		dependencies.Now = func() time.Time { return time.Now().UTC() }
	}
	if dependencies.TryWake == nil {
		dependencies.TryWake = func(string) bool { return false }
	}
	if dependencies.RcloneNativeFactoryBuilder == nil {
		dependencies.RcloneNativeFactoryBuilder = func(
			ctx context.Context,
			bootstrap provider.RcloneNativeBootstrap,
			region string,
			maxAttempts int,
		) (RcloneNativeFactory, error) {
			return provider.NewRcloneNativeAWSFactory(ctx, bootstrap, region, maxAttempts)
		}
	}
	service := &PublicationService{
		db: dependencies.DB, foundation: dependencies.Foundation, registry: dependencies.Registry, keyring: dependencies.Keyring, lease: dependencies.Lease,
		admission: dependencies.Admission, metrics: dependencies.Metrics, audit: dependencies.Audit, history: dependencies.History,
		now: dependencies.Now, tryWake: dependencies.TryWake,
		rcloneNativeFactoryBuilder: dependencies.RcloneNativeFactoryBuilder,
	}
	service.rcloneNativeHealthCheck = service.checkRcloneNativeProviderHealth
	return service, nil
}

func (service *PublicationService) Prepare(ctx context.Context, run publication.Run) (publication.Execution, error) {
	if service == nil || run.Task.ID == 0 || run.TaskRunID == 0 || backupasset.ValidatePublicationAuditContext(run.Audit) != nil {
		return nil, fmt.Errorf("%w: invalid publication prepare request", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}

	for {
		enabled, err := service.foundation.FeatureEnabled()
		if err != nil {
			return nil, err
		}
		hintedOperation := publication.OperationLegacyBackup
		if enabled {
			hintedOperation = publication.OperationEvidenceBackup
		}
		token, err := service.admission.Acquire(ctx, hintedOperation)
		if err != nil {
			return nil, err
		}
		actualEnabled, err := service.foundation.FeatureEnabled()
		if err != nil {
			_ = token.Close()
			return nil, err
		}
		if actualEnabled != enabled || (actualEnabled && token.Mode() != publication.AdmissionManaged) ||
			(!actualEnabled && token.Mode() == publication.AdmissionManaged) {
			_ = token.Close()
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			continue
		}
		if !actualEnabled {
			return service.prepareDisabled(ctx, run, token)
		}
		return service.prepareEvidence(ctx, run, token)
	}
}

func (service *PublicationService) prepareDisabled(ctx context.Context, run publication.Run, token publication.AdmissionToken) (publication.Execution, error) {
	legacyAllowed, err := service.history.legacyFallbackAllowed(ctx, run.Task)
	if err != nil {
		_ = token.Close()
		return nil, err
	}
	if err := publication.ValidateAdmissionMode(token.Mode()); err != nil {
		_ = token.Close()
		return nil, err
	}
	if token.Mode() == publication.AdmissionManaged || !legacyAllowed {
		service.metrics.ObserveLegacyBlocked(publication.OperationLegacyBackup)
		_ = token.Close()
		return nil, fmt.Errorf("%w: %s", backupasset.ErrForbidden, backupasset.FailureLegacyFallbackBlocked)
	}
	return newPublicationExecution(service, publication.ModeCompatibility, token, nil, nil, ctx), nil
}

func (service *PublicationService) prepareEvidence(ctx context.Context, run publication.Run, token publication.AdmissionToken) (publication.Execution, error) {
	if token.Mode() != publication.AdmissionManaged {
		_ = token.Close()
		service.metrics.ObserveLegacyBlocked(publication.OperationEvidenceBackup)
		return nil, fmt.Errorf("%w: %s", backupasset.ErrForbidden, backupasset.FailureLegacyFallbackBlocked)
	}
	var taskEntity model.Task
	if err := service.db.WithContext(ctx).Where("archived_at IS NULL").First(&taskEntity, run.Task.ID).Error; err != nil {
		_ = token.Close()
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: publication Task", backupasset.ErrNotFound)
		}
		return nil, fmt.Errorf("load publication Task provider: %w", err)
	}
	switch bindingProviderForTask(taskEntity) {
	case backupasset.ProviderRestic:
		return service.prepareResticEvidence(ctx, run, token)
	case backupasset.ProviderRsync:
		return service.prepareRsyncPublication(ctx, run, token)
	case backupasset.ProviderRclone:
		return service.prepareRclonePublication(ctx, run, token)
	default:
		_ = token.Close()
		return nil, fmt.Errorf("%w: managed publication provider is unsupported", backupasset.ErrCapabilityUnavailable)
	}
}

func (service *PublicationService) prepareResticEvidence(ctx context.Context, run publication.Run, token publication.AdmissionToken) (publication.Execution, error) {
	if _, err := service.registry.PublicationStrategy(backupasset.ProviderRestic); err != nil {
		_ = token.Close()
		return nil, err
	}
	runtime, link, err := service.loadExactPublicationRuntime(ctx, run.Task.ID, run.Audit)
	if err != nil {
		_ = token.Close()
		return nil, err
	}
	limits, err := service.providerOperationLimits()
	if err != nil {
		_ = token.Close()
		return nil, err
	}
	prober, err := service.registry.Prober(backupasset.ProviderRestic)
	if err != nil {
		_ = token.Close()
		return nil, err
	}
	observation, err := prober.Probe(ctx, runtime.access, limits)
	if err != nil {
		_ = token.Close()
		return nil, err
	}
	if err := validateObservation(runtime.access, observation); err != nil || runtime.repository.RepositoryIdentity == nil ||
		observation.RepositoryIdentity != *runtime.repository.RepositoryIdentity || observation.AdapterRevision != runtime.document.AdapterRevision {
		_ = token.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%w: Restic publication repository identity drift", backupasset.ErrConflict)
	}
	leaseConfig, err := service.foundation.LeaseConfig()
	if err != nil {
		_ = token.Close()
		return nil, err
	}

	preparedAt := service.now().UTC()
	attempt, err := service.preparePoint(ctx, run, runtime, link, observation, leaseConfig, preparedAt)
	if err != nil {
		_ = token.Close()
		return nil, err
	}
	execution := newPublicationExecution(service, publication.ModeEvidence, token, &attempt, &leaseConfig, ctx)
	if err := service.writePublicationAudit(ctx, run.Audit, backupasset.AuditActionRecoveryPointPublicationPrepare, backupasset.AuditOutcomeSuccess, &attempt, publication.StageExecution, backupasset.RecoveryPointPreparing, "", ""); err != nil {
		service.metrics.ObserveAuditFailure(publication.StageExecution)
		_ = execution.Abandon(backupasset.ErrPublicationSessionAbandoned)
		return nil, err
	}
	return execution, nil
}

func (service *PublicationService) preparePoint(ctx context.Context, run publication.Run, runtime publicationRepositoryRuntime, link model.TaskRepositoryLink, observation provider.RepositoryObservation, leaseConfig backupasset.LeaseConfig, preparedAt time.Time) (provider.ResticAttemptV1, error) {
	var attempt provider.ResticAttemptV1
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var taskRun model.TaskRun
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&taskRun, run.TaskRunID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: TaskRun", backupasset.ErrNotFound)
			}
			return fmt.Errorf("lock publication TaskRun: %w", err)
		}
		if !authoritativeTaskRunForTask(taskRun, runtime.task) || !activeTaskRunStatus(taskRun.Status) {
			return fmt.Errorf("%w: TaskRun is not active for publication", backupasset.ErrConflict)
		}
		pointID, err := deriveRecoveryPointID(link.ID, taskRun.ID)
		if err != nil {
			return err
		}
		tags, err := deriveResticPublicationTags(link.ID, pointID)
		if err != nil {
			return err
		}
		startedAt := run.StartedAt.UTC()
		if startedAt.IsZero() {
			if taskRun.StartedAt == nil || taskRun.StartedAt.IsZero() {
				return fmt.Errorf("%w: publication TaskRun start time is missing", backupasset.ErrInvalidState)
			}
			startedAt = taskRun.StartedAt.UTC()
		}
		trigger := run.Trigger
		if trigger == "" {
			trigger = taskRun.TriggerType
		}
		lineage, err := publicationLineageForRun(link, runtime.task, taskRun, trigger, run.ChainRunID, startedAt, preparedAt, preparedAt.Add(leaseConfig.AbsoluteDeadline))
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
			ID: pointID, RepositoryID: runtime.repository.ID, ProducingTaskID: &runtime.task.ID, ProducingTaskRunID: &taskRun.ID,
			ProducingTaskNameSnapshot: runtime.task.Name, ProducingNodeIDSnapshot: runtime.task.NodeID, ProducingNodeNameSnapshot: runtime.node.Name,
			LineageJSON: encodedLineage, Semantics: string(backupasset.PointNativeSnapshot), State: string(backupasset.RecoveryPointPreparing),
			ManifestDigestAlgorithm: "sha256", ConsistencyJSON: emptyConsistency, FidelityJSON: "{}", CapabilityRevision: runtime.repository.CapabilityRevision,
			CapabilitiesJSON: runtime.repository.CapabilitiesJSON, ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned),
			PhysicalAvailability: string(backupasset.PhysicalUnknown), HoldState: string(backupasset.HoldNone), CreatedAt: preparedAt, UpdatedAt: preparedAt,
		}
		var existing model.RecoveryPoint
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", pointID).Limit(1).Find(&existing)
		if result.Error != nil {
			return fmt.Errorf("load deterministic publication point: %w", result.Error)
		}
		if result.RowsAffected == 1 {
			if !samePreparedPublicationPoint(existing, point) {
				return fmt.Errorf("%w: deterministic publication point immutable fields differ", backupasset.ErrConflict)
			}
			return fmt.Errorf("%w: deterministic publication point already exists", backupasset.ErrPublicationInProgress)
		}
		if err := tx.Create(&point).Error; err != nil {
			if isPublicationProducingRunConflict(err) {
				return fmt.Errorf("%w: producing TaskRun is already claimed", backupasset.ErrConflict)
			}
			return fmt.Errorf("create deterministic publication point: %w", err)
		}
		lease, err := service.lease.AcquireTx(ctx, tx, backupasset.AcquireLeaseRequest{
			RecoveryPointID: point.ID, HolderType: backupasset.LeaseHolderPointPublication, OwnerID: publicationLeaseOwner, AbsoluteDeadline: lineage.PointDeadlineAt,
		})
		if err != nil {
			if errors.Is(err, backupasset.ErrLeaseHeld) {
				return fmt.Errorf("%w: execution publication lease", backupasset.ErrPublicationInProgress)
			}
			return err
		}
		attempt = provider.ResticAttemptV1{
			Provider: backupasset.ProviderRestic, RepositoryID: runtime.repository.ID, RepositoryIdentity: observation.RepositoryIdentity, TaskRepositoryLinkID: link.ID,
			RecoveryPointID: point.ID, TaskID: run.Task.ID, TaskRunID: taskRun.ID, RequiredTags: tags, PointDeadlineAt: lineage.PointDeadlineAt,
			CapabilityRevision: runtime.repository.CapabilityRevision, AdapterRevision: observation.AdapterRevision, Audit: run.Audit, Access: runtime.access, Fence: lease.Fence,
		}
		return nil
	})
	if err != nil {
		return provider.ResticAttemptV1{}, err
	}
	return attempt, nil
}

type publicationRepositoryRuntime struct {
	repository model.BackupRepository
	task       model.Task
	document   bindingDocument
	access     provider.AccessBinding
	node       model.Node
}

func (service *PublicationService) loadExactPublicationRuntime(ctx context.Context, taskID uint, audit backupasset.PublicationAuditContext) (publicationRepositoryRuntime, model.TaskRepositoryLink, error) {
	var taskEntity model.Task
	if err := service.db.WithContext(ctx).Where("archived_at IS NULL").Preload("Node.SSHKey").First(&taskEntity, taskID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return publicationRepositoryRuntime{}, model.TaskRepositoryLink{}, fmt.Errorf("%w: publication Task", backupasset.ErrNotFound)
		}
		return publicationRepositoryRuntime{}, model.TaskRepositoryLink{}, fmt.Errorf("load publication Task: %w", err)
	}
	if bindingProviderForTask(taskEntity) != backupasset.ProviderRestic {
		return publicationRepositoryRuntime{}, model.TaskRepositoryLink{}, fmt.Errorf("%w: publication requires Restic Task", backupasset.ErrInvalidState)
	}
	var link model.TaskRepositoryLink
	if err := service.db.WithContext(ctx).Where("task_id = ? AND unlinked_at IS NULL", taskID).First(&link).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return publicationRepositoryRuntime{}, model.TaskRepositoryLink{}, fmt.Errorf("%w: %s", backupasset.ErrForbidden, backupasset.FailureLegacyFallbackBlocked)
		}
		return publicationRepositoryRuntime{}, model.TaskRepositoryLink{}, fmt.Errorf("load publication link: %w", err)
	}
	if link.PublicationMode != string(backupasset.PublicationNativeSnapshot) {
		return publicationRepositoryRuntime{}, model.TaskRepositoryLink{}, fmt.Errorf("%w: native snapshot link required", backupasset.ErrConflict)
	}
	var repository model.BackupRepository
	if err := service.db.WithContext(ctx).First(&repository, "id = ?", link.RepositoryID).Error; err != nil {
		return publicationRepositoryRuntime{}, model.TaskRepositoryLink{}, fmt.Errorf("load publication repository: %w", err)
	}
	if repository.ProviderKind != string(backupasset.ProviderRestic) || repository.VersionMode != string(backupasset.VersionNativeSnapshot) {
		return publicationRepositoryRuntime{}, model.TaskRepositoryLink{}, fmt.Errorf("%w: native Restic repository required", backupasset.ErrConflict)
	}
	var binding model.RepositoryAccessBinding
	if err := service.db.WithContext(ctx).Where("repository_id = ? AND status = ?", repository.ID, bindingStatusActive).First(&binding).Error; err != nil {
		return publicationRepositoryRuntime{}, model.TaskRepositoryLink{}, fmt.Errorf("load active publication binding: %w", err)
	}
	document, err := decodeBindingDocument(binding.EncryptedConfig)
	if err != nil {
		return publicationRepositoryRuntime{}, model.TaskRepositoryLink{}, err
	}
	var bindingTask model.Task
	if err := service.db.WithContext(ctx).Where("archived_at IS NULL").First(&bindingTask, document.TaskID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return publicationRepositoryRuntime{}, model.TaskRepositoryLink{}, fmt.Errorf("%w: active publication binding Task unavailable", backupasset.ErrConflict)
		}
		return publicationRepositoryRuntime{}, model.TaskRepositoryLink{}, fmt.Errorf("load active publication binding Task: %w", err)
	}
	if document.Provider != backupasset.ProviderRestic || bindingProviderForTask(bindingTask) != backupasset.ProviderRestic ||
		bindingTask.NodeID != document.NodeID {
		return publicationRepositoryRuntime{}, model.TaskRepositoryLink{}, fmt.Errorf("%w: active publication binding lineage mismatch", backupasset.ErrConflict)
	}
	access, err := publicationAccessForCurrentTask(document, repository, taskEntity)
	if err != nil {
		return publicationRepositoryRuntime{}, model.TaskRepositoryLink{}, err
	}
	access = withRemoteAuditContext(access, RequestContext{Actor: audit.Actor, CorrelationID: audit.CorrelationID}, taskID)
	return publicationRepositoryRuntime{repository: repository, task: taskEntity, document: document, access: access, node: taskEntity.Node}, link, nil
}

// publicationAccessForCurrentTask keeps RepositoryAccessBinding repository
// scoped. A shared native Restic repository may have one retained binding but
// multiple linked Tasks; each backup must still use its own Node, locator,
// secret, Task ID, and audit context. The subsequent live probe proves that
// the current Task's access reaches the retained native Repository identity.
func publicationAccessForCurrentTask(document bindingDocument, repository model.BackupRepository, taskEntity model.Task) (provider.AccessBinding, error) {
	if document.Provider != backupasset.ProviderRestic || repository.ProviderKind != string(backupasset.ProviderRestic) ||
		repository.RepositoryIdentity == nil || taskEntity.ID == 0 || taskEntity.NodeID == 0 || taskEntity.Node.ID != taskEntity.NodeID {
		return provider.AccessBinding{}, fmt.Errorf("%w: invalid shared Restic publication access", backupasset.ErrInvalidState)
	}
	nativeRepositoryID := strings.TrimPrefix(*repository.RepositoryIdentity, provider.NativeResticIdentityPrefix)
	if _, err := provider.NativeRepositoryIdentity(backupasset.ProviderRestic, nativeRepositoryID); err != nil || nativeRepositoryID != document.NativeRepositoryID {
		return provider.AccessBinding{}, fmt.Errorf("%w: active publication binding repository identity mismatch", backupasset.ErrConflict)
	}
	salt, err := hexDecodeSalt(document.IdentitySalt)
	if err != nil {
		return provider.AccessBinding{}, err
	}
	_, access, err := bindingFromTask(taskEntity, taskEntity.Node, salt)
	if err != nil {
		return provider.AccessBinding{}, err
	}
	runtimeAccess, ok := access.AdapterData.(provider.ResticRuntimeAccess)
	if !ok || runtimeAccess.Command == nil {
		return provider.AccessBinding{}, fmt.Errorf("%w: current Restic Task command access unavailable", backupasset.ErrInvalidState)
	}
	runtimeAccess.NativeRepositoryID = nativeRepositoryID
	access.AdapterData = runtimeAccess
	access.RepositoryID = repository.ID
	return access, nil
}

func (service *PublicationService) providerOperationLimits() (provider.OperationLimits, error) {
	config, err := service.foundation.ProviderConfig()
	if err != nil {
		return provider.OperationLimits{}, err
	}
	return provider.NewMetadataOperationLimits(config.OperationTimeout, config.MetadataLimitBytes)
}

func publicationLineageForRun(link model.TaskRepositoryLink, taskEntity model.Task, taskRun model.TaskRun, trigger, chainRunID string, startedAt, preparedAt, deadline time.Time) (backupasset.PublicationLineageV1, error) {
	chainDigest := ""
	if chainRunID != "" {
		sum := sha256.Sum256([]byte(chainRunID))
		chainDigest = hex.EncodeToString(sum[:])
	}
	return backupasset.PublicationLineageV1{
		Version: 1, TaskRepositoryLinkID: link.ID, TaskID: taskEntity.ID, TaskRunID: taskRun.ID, Trigger: trigger,
		ChainRunIDPresent: chainRunID != "", ChainRunIDDigest: chainDigest, PublicationMode: string(backupasset.PublicationNativeSnapshot),
		PointCodecVersion: 1, TagCodecVersion: 1, StartedAt: startedAt.UTC(), PreparedAt: preparedAt.UTC(), PointDeadlineAt: deadline.UTC(),
	}, nil
}

func activeTaskRunStatus(status string) bool {
	return model.IsActiveTaskRunStatus(status)
}

func authoritativeTaskRunForTask(run model.TaskRun, taskEntity model.Task) bool {
	return run.TaskID == taskEntity.ID &&
		model.IsTaskRunNodeSnapshotAuthoritative(run.NodeIDSnapshot) &&
		run.NodeIDSnapshot == taskEntity.NodeID
}

func countAuthoritativeActiveTaskRuns(tx *gorm.DB, taskEntity model.Task) (int64, error) {
	if tx == nil || !model.IsTaskRunNodeSnapshotAuthoritative(taskEntity.NodeID) {
		return 0, fmt.Errorf("%w: Task node identity is not authoritative", backupasset.ErrConflict)
	}
	activeStatuses := model.TaskRunActiveStatuses()
	var invalid int64
	if err := tx.Model(&model.TaskRun{}).
		Where("task_id = ? AND status IN ? AND (node_id_snapshot <= ? OR node_id_snapshot <> ?)",
			taskEntity.ID, activeStatuses, model.TaskRunNodeIDLegacyUnknown, taskEntity.NodeID).
		Count(&invalid).Error; err != nil {
		return 0, err
	}
	if invalid != 0 {
		return 0, fmt.Errorf("%w: active TaskRun node snapshot is not authoritative", backupasset.ErrConflict)
	}
	var active int64
	if err := tx.Model(&model.TaskRun{}).
		Where("task_id = ? AND node_id_snapshot = ? AND status IN ?", taskEntity.ID, taskEntity.NodeID, activeStatuses).
		Count(&active).Error; err != nil {
		return 0, err
	}
	return active, nil
}

func samePreparedPublicationPoint(left, right model.RecoveryPoint) bool {
	return left.ID == right.ID && left.RepositoryID == right.RepositoryID && uintPointerEqual(left.ProducingTaskID, right.ProducingTaskID) &&
		uintPointerEqual(left.ProducingTaskRunID, right.ProducingTaskRunID) && left.ProducingTaskNameSnapshot == right.ProducingTaskNameSnapshot &&
		left.ProducingNodeIDSnapshot == right.ProducingNodeIDSnapshot && left.ProducingNodeNameSnapshot == right.ProducingNodeNameSnapshot &&
		left.LineageJSON == right.LineageJSON && left.Semantics == right.Semantics && left.State == right.State &&
		left.CapabilityRevision == right.CapabilityRevision && left.CapabilitiesJSON == right.CapabilitiesJSON && left.ImmutabilityLevel == right.ImmutabilityLevel &&
		left.PhysicalAvailability == right.PhysicalAvailability && left.HoldState == right.HoldState
}

func uintPointerEqual(left, right *uint) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func (service *PublicationService) writePublicationAudit(ctx context.Context, audit backupasset.PublicationAuditContext, action backupasset.AuditAction, outcome backupasset.AuditOutcome, attempt *provider.ResticAttemptV1, stage publication.PublicationStage, status backupasset.RecoveryPointState, code string, failure backupasset.PublicationFailureCode) error {
	if service.audit == nil {
		return nil
	}
	if attempt == nil || backupasset.ValidatePublicationAuditContext(audit) != nil || publication.ValidatePublicationStage(stage) != nil || status == "" {
		return fmt.Errorf("%w: invalid publication audit input", backupasset.ErrInvalidState)
	}
	taskID, runID := attempt.TaskID, attempt.TaskRunID
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
		TaskID: &taskID, TaskRunID: &runID, Fields: fields,
	}
	if failure != "" {
		input.FailureCode = string(failure)
	}
	return service.audit.Write(ctx, input)
}

func (service *PublicationService) recordProviderCommit(ctx context.Context, attempt provider.ResticAttemptV1, evidence provider.ResticCommitV1) (publication.Outcome, bool, error) {
	if err := validateCommitEvidence(attempt, evidence); err != nil {
		return publication.Outcome{}, false, err
	}
	var outcome publication.Outcome
	transitioned := false
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var point model.RecoveryPoint
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&point, "id = ?", attempt.RecoveryPointID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: publication point", backupasset.ErrNotFound)
			}
			return fmt.Errorf("lock provider commit point: %w", err)
		}
		if point.RepositoryID != attempt.RepositoryID || point.ProducingTaskID == nil || *point.ProducingTaskID != attempt.TaskID ||
			point.ProducingTaskRunID == nil || *point.ProducingTaskRunID != attempt.TaskRunID {
			return fmt.Errorf("%w: provider commit point lineage mismatch", backupasset.ErrConflict)
		}
		lineage, err := backupasset.DecodePublicationLineage(point.LineageJSON)
		if err != nil {
			return err
		}
		if lineage.TaskRepositoryLinkID != attempt.TaskRepositoryLinkID || lineage.TaskID != attempt.TaskID || lineage.TaskRunID != attempt.TaskRunID ||
			!lineage.PointDeadlineAt.Equal(attempt.PointDeadlineAt.UTC()) {
			return fmt.Errorf("%w: provider commit immutable lineage mismatch", backupasset.ErrConflict)
		}
		if point.State == string(backupasset.RecoveryPointVerifying) || point.State == string(backupasset.RecoveryPointCommitted) {
			matching, replayOutcome, err := providerCommitReplayMatches(point, attempt, evidence)
			if err != nil {
				return err
			}
			if !matching {
				return fmt.Errorf("%w: provider commit replay differs", backupasset.ErrConflict)
			}
			outcome = replayOutcome
			return nil
		}
		if point.State != string(backupasset.RecoveryPointPreparing) {
			return fmt.Errorf("%w: provider commit point is not preparing", backupasset.ErrConflict)
		}
		if err := service.lease.ValidateFenceTx(ctx, tx, attempt.Fence); err != nil {
			return err
		}
		locatorPayload, err := json.Marshal(resticPointLocatorV1{Version: 1, Provider: string(backupasset.ProviderRestic), FullSnapshotID: evidence.NativePointID})
		if err != nil {
			return fmt.Errorf("encode provider commit locator: %w", err)
		}
		requestedTagDigest := publicationTagDigest(attempt.RequiredTags)
		commitDigest, err := canonicalProviderCommitDigest(attempt, evidence, requestedTagDigest)
		if err != nil {
			return err
		}
		startedAt, finishedAt := evidence.CaptureStartedAt.UTC(), evidence.CaptureFinishedAt.UTC()
		consistency := backupasset.PublicationConsistencyV1{
			Version: 1, CaptureStartedAt: &startedAt, CaptureFinishedAt: &finishedAt, FilesProcessed: evidence.FilesProcessed, LogicalBytes: evidence.LogicalBytes,
			Provider: backupasset.ProviderRestic, RepositoryIdentityDigest: digestText(evidence.RepositoryIdentity), RequestedTagDigest: requestedTagDigest,
			ProviderCommitDigest: commitDigest, AdapterRevision: attempt.AdapterRevision, CapabilityRevision: attempt.CapabilityRevision,
		}
		encodedConsistency, err := backupasset.EncodePublicationConsistency(consistency)
		if err != nil {
			return err
		}
		if err := backupasset.ValidateRecoveryPointTransition(backupasset.RecoveryPointProfile{
			VersionMode: backupasset.VersionNativeSnapshot, Semantics: backupasset.PointNativeSnapshot, State: backupasset.RecoveryPointPreparing,
			Immutability: backupasset.ImmutabilityBackendVersioned, Availability: backupasset.PhysicalUnknown, Hold: backupasset.HoldNone,
		}, backupasset.RecoveryPointVerifying); err != nil {
			return err
		}
		point.EncryptedProviderLocator = string(locatorPayload)
		point.SourceFingerprint = resticSourceFingerprint(evidence.RepositoryIdentity, evidence.NativePointID)
		point.ConsistencyJSON = encodedConsistency
		point.State = string(backupasset.RecoveryPointVerifying)
		point.UpdatedAt = service.now().UTC()
		if err := tx.Save(&point).Error; err != nil {
			if isPublicationNativeSourceConflict(err) {
				return fmt.Errorf("%w: native Restic point is already claimed", backupasset.ErrConflict)
			}
			return fmt.Errorf("save provider commit point: %w", err)
		}
		if err := service.lease.ReleaseTx(ctx, tx, attempt.Fence); err != nil {
			return err
		}
		outcome = publication.Outcome{
			RepositoryID: point.RepositoryID, RecoveryPointID: point.ID, TaskID: attempt.TaskID, TaskRunID: attempt.TaskRunID,
			State: backupasset.RecoveryPointVerifying, NativePointID: evidence.NativePointID, CapturedAt: startedAt, ProviderCommitRecorded: true,
		}
		transitioned = true
		return nil
	})
	if err != nil {
		return publication.Outcome{}, false, err
	}
	return outcome, transitioned, nil
}

func validateCommitEvidence(attempt provider.ResticAttemptV1, evidence provider.ResticCommitV1) error {
	if attempt.Provider != backupasset.ProviderRestic || evidence.Provider != backupasset.ProviderRestic || evidence.RepositoryIdentity != attempt.RepositoryIdentity ||
		!validFullNativeID(evidence.NativePointID) || evidence.CaptureStartedAt.IsZero() || evidence.CaptureFinishedAt.IsZero() || evidence.CaptureFinishedAt.Before(evidence.CaptureStartedAt) {
		return fmt.Errorf("%w: provider commit evidence mismatch", backupasset.ErrConflict)
	}
	if !evidence.CaptureStartedAt.UTC().Before(attempt.PointDeadlineAt.UTC()) || !evidence.CaptureFinishedAt.UTC().Before(attempt.PointDeadlineAt.UTC()) {
		return fmt.Errorf("%w: provider commit evidence exceeded point deadline", backupasset.ErrConflict)
	}
	return nil
}

func resticSourceFingerprint(identity, fullID string) string {
	sum := sha256.Sum256([]byte("xirang.restic.native-point.v1\x00" + identity + "\x00" + fullID))
	return hex.EncodeToString(sum[:])
}

func publicationTagDigest(tags [2]string) string {
	sum := sha256.Sum256([]byte(tags[0] + "\x00" + tags[1]))
	return hex.EncodeToString(sum[:])
}

func digestText(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func isPublicationNativeSourceConflict(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	if !strings.Contains(message, "unique constraint") && !strings.Contains(message, "duplicate key") && !strings.Contains(message, "sqlstate 23505") {
		return false
	}
	return strings.Contains(message, "idx_recovery_points_native_source_unique") ||
		(strings.Contains(message, "recovery_points.repository_id") && strings.Contains(message, "recovery_points.source_fingerprint"))
}

func isPublicationProducingRunConflict(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	if !strings.Contains(message, "unique constraint") && !strings.Contains(message, "duplicate key") && !strings.Contains(message, "sqlstate 23505") {
		return false
	}
	return strings.Contains(message, "idx_recovery_points_producing_task_run_unique") ||
		strings.Contains(message, "recovery_points.producing_task_run_id")
}

func canonicalProviderCommitDigest(attempt provider.ResticAttemptV1, evidence provider.ResticCommitV1, requestedTagDigest string) (string, error) {
	writer := backupasset.NewCanonicalSHA256()
	writer.String("xirang.restic.provider-commit.v1")
	writer.String(string(evidence.Provider))
	writer.String(evidence.RepositoryIdentity)
	writer.String(evidence.NativePointID)
	writer.Int64(evidence.CaptureStartedAt.UTC().UnixNano())
	writer.Int64(evidence.CaptureFinishedAt.UTC().UnixNano())
	writer.Uint64(evidence.FilesProcessed)
	writer.Uint64(evidence.LogicalBytes)
	writer.String(requestedTagDigest)
	writer.String(attempt.AdapterRevision)
	if attempt.CapabilityRevision < 0 {
		return "", fmt.Errorf("%w: negative capability revision", backupasset.ErrInvalidState)
	}
	writer.Uint64(uint64(attempt.CapabilityRevision))
	digest, err := writer.HexDigest()
	if err != nil {
		return "", fmt.Errorf("canonical provider commit digest: %w", err)
	}
	return digest, nil
}

func providerCommitReplayMatches(point model.RecoveryPoint, attempt provider.ResticAttemptV1, evidence provider.ResticCommitV1) (bool, publication.Outcome, error) {
	locator, err := decodeResticPointLocator(point.EncryptedProviderLocator)
	if err != nil {
		return false, publication.Outcome{}, err
	}
	consistency, err := backupasset.DecodePublicationConsistency(point.ConsistencyJSON)
	if err != nil {
		return false, publication.Outcome{}, err
	}
	requestedTagDigest := publicationTagDigest(attempt.RequiredTags)
	commitDigest, err := canonicalProviderCommitDigest(attempt, evidence, requestedTagDigest)
	if err != nil {
		return false, publication.Outcome{}, err
	}
	matching := locator.FullSnapshotID == evidence.NativePointID && point.SourceFingerprint == resticSourceFingerprint(evidence.RepositoryIdentity, evidence.NativePointID) &&
		consistency.Provider == backupasset.ProviderRestic && consistency.CaptureStartedAt != nil && consistency.CaptureFinishedAt != nil &&
		consistency.CaptureStartedAt.Equal(evidence.CaptureStartedAt.UTC()) && consistency.CaptureFinishedAt.Equal(evidence.CaptureFinishedAt.UTC()) &&
		consistency.FilesProcessed == evidence.FilesProcessed && consistency.LogicalBytes == evidence.LogicalBytes && consistency.RepositoryIdentityDigest == digestText(evidence.RepositoryIdentity) &&
		consistency.RequestedTagDigest == requestedTagDigest && consistency.ProviderCommitDigest == commitDigest && consistency.AdapterRevision == attempt.AdapterRevision && consistency.CapabilityRevision == attempt.CapabilityRevision
	if !matching {
		return false, publication.Outcome{}, nil
	}
	capturedAt := evidence.CaptureStartedAt.UTC()
	return true, publication.Outcome{
		RepositoryID: point.RepositoryID, RecoveryPointID: point.ID, TaskID: attempt.TaskID, TaskRunID: attempt.TaskRunID,
		State: backupasset.RecoveryPointState(point.State), NativePointID: evidence.NativePointID, CapturedAt: capturedAt, ProviderCommitRecorded: true,
	}, nil
}

type publicationExecution struct {
	service    *PublicationService
	mode       publication.ExecutionMode
	token      publication.AdmissionToken
	attempt    *provider.ResticAttemptV1
	context    context.Context
	cancel     context.CancelCauseFunc
	heartbeat  chan struct{}
	heartbeatW sync.WaitGroup
	stopOnce   sync.Once
	closeOnce  sync.Once
}

func newPublicationExecution(service *PublicationService, mode publication.ExecutionMode, token publication.AdmissionToken, attempt *provider.ResticAttemptV1, config *backupasset.LeaseConfig, parent context.Context) *publicationExecution {
	if parent == nil {
		parent = context.Background()
	}
	commandContext, cancel := context.WithCancelCause(parent)
	execution := &publicationExecution{service: service, mode: mode, token: token, attempt: attempt, context: commandContext, cancel: cancel, heartbeat: make(chan struct{})}
	if mode == publication.ModeEvidence && attempt != nil && config != nil {
		execution.heartbeatW.Add(1)
		go execution.runHeartbeat(*config)
	}
	return execution
}

func (execution *publicationExecution) Mode() publication.ExecutionMode { return execution.mode }
func (execution *publicationExecution) Attempt() *provider.TaggedPublicationAttempt {
	if execution == nil || execution.attempt == nil {
		return nil
	}
	copy := provider.NewResticPublicationAttempt(*execution.attempt)
	return &copy
}
func (execution *publicationExecution) Context() context.Context {
	if execution == nil || execution.context == nil {
		return context.Background()
	}
	return execution.context
}

func (execution *publicationExecution) Cancel(cause error) error {
	if execution == nil {
		return nil
	}
	if !validPublicationCancelCause(cause) {
		return fmt.Errorf("%w: unsafe publication cancellation cause", backupasset.ErrInvalidState)
	}
	execution.stopOnce.Do(func() {
		execution.cancel(cause)
		close(execution.heartbeat)
		execution.heartbeatW.Wait()
	})
	return nil
}

func validPublicationCancelCause(cause error) bool {
	if cause == nil {
		return true
	}
	for _, accepted := range []error{
		backupasset.ErrPublicationSessionAbandoned,
		backupasset.ErrPublicationUnconfirmed,
		backupasset.ErrLeaseFenceLost,
		backupasset.ErrLeaseDeadlineExceeded,
		context.Canceled,
		context.DeadlineExceeded,
		sshutil.ErrCommandTimeout,
		sshutil.ErrCommandOutputLimit,
		sshutil.ErrCommandFailed,
	} {
		if errors.Is(cause, accepted) {
			return true
		}
	}
	return false
}

func (execution *publicationExecution) Abandon(cause error) error {
	if err := execution.Cancel(cause); err != nil {
		return err
	}
	execution.closeAdmission()
	return nil
}

func (execution *publicationExecution) CompleteCompatibility(_ context.Context) error {
	if execution == nil || execution.mode != publication.ModeCompatibility {
		return fmt.Errorf("%w: compatibility completion is unavailable", backupasset.ErrInvalidState)
	}
	execution.closeAdmission()
	return nil
}

func (execution *publicationExecution) RecordProviderCommit(ctx context.Context, evidence provider.ProviderCommit) (publication.Outcome, error) {
	if execution == nil || execution.mode != publication.ModeEvidence || execution.attempt == nil || execution.service == nil {
		return publication.Outcome{}, fmt.Errorf("%w: evidence publication execution required", backupasset.ErrInvalidState)
	}
	resticEvidence, err := evidence.ResticCommit()
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
	outcome, transitioned, err := execution.service.recordProviderCommit(commitContext, *execution.attempt, resticEvidence)
	if err != nil {
		return publication.Outcome{}, err
	}
	if transitioned {
		execution.service.metrics.ObserveOutcome(backupasset.ProviderRestic, publication.StageExecution, backupasset.PublicationOutcomeSuccess)
		_ = execution.service.tryWake(outcome.RecoveryPointID)
		if err := execution.service.writePublicationAudit(commitContext, execution.attempt.Audit, backupasset.AuditActionRecoveryPointPublicationCommit, backupasset.AuditOutcomeSuccess, execution.attempt, publication.StageExecution, backupasset.RecoveryPointVerifying, "", ""); err != nil {
			execution.service.metrics.ObserveAuditFailure(publication.StageExecution)
		}
	}
	execution.closeAdmission()
	return outcome, nil
}

func (execution *publicationExecution) Defer(ctx context.Context, deferral publication.Deferral) error {
	if execution == nil || execution.mode != publication.ModeEvidence || execution.attempt == nil || execution.service == nil {
		return fmt.Errorf("%w: evidence publication execution required", backupasset.ErrInvalidState)
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
	deferredState := backupasset.RecoveryPointState("")
	deferred := false
	err := execution.service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var point model.RecoveryPoint
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&point, "id = ?", execution.attempt.RecoveryPointID).Error; err != nil {
			return fmt.Errorf("lock deferred publication point: %w", err)
		}
		if point.State != string(backupasset.RecoveryPointPreparing) && point.State != string(backupasset.RecoveryPointVerifying) {
			return fmt.Errorf("%w: deferred point is terminal", backupasset.ErrConflict)
		}
		if err := execution.service.lease.ValidateFenceTx(ctx, tx, execution.attempt.Fence); err != nil {
			return err
		}
		consistency, err := backupasset.DecodePublicationConsistency(point.ConsistencyJSON)
		if err != nil {
			return err
		}
		if consistency.Completion == deferral.Completion && consistency.Code == deferral.Code {
			deferredState = backupasset.RecoveryPointState(point.State)
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
		deferredState = backupasset.RecoveryPointState(point.State)
		deferred = true
		return nil
	})
	if err != nil {
		return err
	}
	if deferred {
		if err := execution.service.writePublicationAudit(ctx, execution.attempt.Audit, backupasset.AuditActionRecoveryPointPublicationVerify, backupasset.AuditOutcomeFailure, execution.attempt, publication.StageExecution, deferredState, string(deferral.Code), deferral.Code); err != nil {
			execution.service.metrics.ObserveAuditFailure(publication.StageExecution)
		}
	}
	execution.closeAdmission()
	return nil
}

func (execution *publicationExecution) Reject(ctx context.Context, code backupasset.PublicationFailureCode) error {
	if code != backupasset.FailurePublicationPreconditionMissing {
		return fmt.Errorf("%w: invalid pre-command publication rejection", backupasset.ErrInvalidState)
	}
	return execution.terminalFail(ctx, code, false)
}

func (execution *publicationExecution) Fail(ctx context.Context, code backupasset.PublicationFailureCode) error {
	if !postCommandPublicationFailure(code) {
		return fmt.Errorf("%w: invalid post-command publication failure", backupasset.ErrInvalidState)
	}
	return execution.terminalFail(ctx, code, true)
}

func (execution *publicationExecution) terminalFail(ctx context.Context, code backupasset.PublicationFailureCode, recordNonzeroCompletion bool) error {
	if execution == nil || execution.mode != publication.ModeEvidence || execution.attempt == nil || execution.service == nil {
		return fmt.Errorf("%w: evidence publication execution required", backupasset.ErrInvalidState)
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
			return fmt.Errorf("lock failed publication point: %w", err)
		}
		if point.State == string(backupasset.RecoveryPointFailed) {
			consistency, err := backupasset.DecodePublicationConsistency(point.ConsistencyJSON)
			if err != nil {
				return err
			}
			if terminalFailureReplayMatches(consistency, code, recordNonzeroCompletion) {
				return nil
			}
			return fmt.Errorf("%w: failed point records different terminal facts", backupasset.ErrConflict)
		}
		if point.State != string(backupasset.RecoveryPointPreparing) && point.State != string(backupasset.RecoveryPointVerifying) {
			return fmt.Errorf("%w: failed point is terminal", backupasset.ErrConflict)
		}
		if err := execution.service.lease.ValidateFenceTx(ctx, tx, execution.attempt.Fence); err != nil {
			return err
		}
		if recordNonzeroCompletion {
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
		if err := backupasset.ValidateRecoveryPointTransition(backupasset.RecoveryPointProfile{
			VersionMode: backupasset.VersionNativeSnapshot, Semantics: backupasset.PointNativeSnapshot, State: backupasset.RecoveryPointState(point.State),
			Immutability: backupasset.ImmutabilityBackendVersioned, Availability: backupasset.PhysicalUnknown, Hold: backupasset.HoldNone,
		}, backupasset.RecoveryPointFailed); err != nil {
			return err
		}
		point.State = string(backupasset.RecoveryPointFailed)
		point.UpdatedAt = execution.service.now().UTC()
		if err := tx.Save(&point).Error; err != nil {
			return err
		}
		if err := execution.service.lease.ReleaseTx(ctx, tx, execution.attempt.Fence); err != nil {
			return err
		}
		transitioned = true
		return nil
	})
	if err != nil {
		return err
	}
	if transitioned {
		if err := execution.service.writePublicationAudit(ctx, execution.attempt.Audit, backupasset.AuditActionRecoveryPointPublicationFail, backupasset.AuditOutcomeFailure, execution.attempt, publication.StageExecution, backupasset.RecoveryPointFailed, string(code), code); err != nil {
			execution.service.metrics.ObserveAuditFailure(publication.StageExecution)
		}
	}
	execution.closeAdmission()
	return nil
}

func terminalFailureReplayMatches(consistency backupasset.PublicationConsistencyV1, code backupasset.PublicationFailureCode, recordNonzeroCompletion bool) bool {
	if recordNonzeroCompletion {
		return consistency.Completion == backupasset.CompletionKnownNonzero && consistency.Code == code
	}
	return consistency.Completion == "" && consistency.Code == "" && code == backupasset.FailurePublicationPreconditionMissing
}

// RecordLegacyBlock records the one safe, typed side effect for an operation
// blocked by the managed-lineage safety boundary. The caller remains
// responsible for stopping before credential or Provider access.
func (service *PublicationService) RecordLegacyBlock(ctx context.Context, block publication.LegacyBlock) error {
	if service == nil || service.db == nil || service.metrics == nil || block.TaskID == 0 ||
		backupasset.ValidatePublicationAuditContext(block.Audit) != nil || !legacyResticOperation(block.Operation) {
		return fmt.Errorf("%w: invalid legacy Restic block", backupasset.ErrInvalidState)
	}
	if block.TaskRunID != nil && *block.TaskRunID == 0 {
		return fmt.Errorf("%w: invalid legacy block TaskRun", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}

	var taskEntity model.Task
	if err := service.db.WithContext(ctx).First(&taskEntity, block.TaskID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("%w: legacy block Task", backupasset.ErrNotFound)
		}
		return fmt.Errorf("validate legacy block Task: %w", err)
	}
	if block.TaskRunID != nil {
		var taskRun model.TaskRun
		if err := service.db.WithContext(ctx).First(&taskRun, *block.TaskRunID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: legacy block TaskRun", backupasset.ErrNotFound)
			}
			return fmt.Errorf("validate legacy block TaskRun: %w", err)
		}
		if taskRun.TaskID != block.TaskID {
			return fmt.Errorf("%w: legacy block TaskRun belongs to another Task", backupasset.ErrConflict)
		}
	}

	service.metrics.ObserveLegacyBlocked(block.Operation)
	if service.audit == nil {
		return nil
	}
	if err := service.audit.Write(ctx, backupasset.AuditEventInput{
		Actor: block.Audit.Actor, Action: backupasset.AuditActionResticLegacyOperationBlocked, Outcome: backupasset.AuditOutcomeBlocked,
		TaskID: &block.TaskID, TaskRunID: block.TaskRunID,
		Fields: map[backupasset.AuditField]any{
			backupasset.AuditFieldStage:         string(publication.StageExecution),
			backupasset.AuditFieldCode:          string(backupasset.FailureLegacyOperationBlocked),
			backupasset.AuditFieldCorrelationID: block.Audit.CorrelationID,
			backupasset.AuditFieldOperation:     string(block.Operation),
		},
	}); err != nil {
		service.metrics.ObserveAuditFailure(publication.StageExecution)
		return fmt.Errorf("%w: legacy block audit write failed", backupasset.ErrInvalidState)
	}
	return nil
}

func legacyResticOperation(operation publication.ResticOperation) bool {
	switch operation {
	case publication.OperationLegacyBackup,
		publication.OperationLegacySnapshotList,
		publication.OperationLegacySnapshotFiles,
		publication.OperationLegacyIndex,
		publication.OperationLegacySearch,
		publication.OperationLegacyDiff,
		publication.OperationLegacySnapshotRestore,
		publication.OperationLegacyRestoreLatest,
		publication.OperationLegacyAnomaly,
		publication.OperationLegacyRetention,
		publication.OperationLegacyIntegrity:
		return true
	default:
		return false
	}
}

func postCommandPublicationFailure(code backupasset.PublicationFailureCode) bool {
	switch code {
	case backupasset.FailureProviderNonzeroExit,
		backupasset.FailureRepositoryIdentityDrift,
		backupasset.FailureNativePointConflict,
		backupasset.FailureProviderSnapshotRewritten,
		backupasset.FailureAmbiguousRunTags,
		backupasset.FailureRunTagMissing,
		backupasset.FailureProviderCompletionUnproven:
		return true
	default:
		return false
	}
}

func (execution *publicationExecution) runHeartbeat(config backupasset.LeaseConfig) {
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
			if execution.attempt == nil {
				return
			}
			if _, err := execution.service.lease.Renew(execution.context, execution.attempt.Fence); err != nil {
				execution.cancel(err)
				return
			}
		}
	}
}

func (execution *publicationExecution) closeAdmission() {
	if execution == nil || execution.token == nil {
		return
	}
	execution.closeOnce.Do(func() { _ = execution.token.Close() })
}

var _ publication.Coordinator = (*PublicationService)(nil)
var _ publication.LegacyBlockRecorder = (*PublicationService)(nil)
var _ publication.Execution = (*publicationExecution)(nil)
