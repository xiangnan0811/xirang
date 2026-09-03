package repository

import (
	"context"
	"crypto/hmac"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
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
	managedRclonePreparedAttemptVersion        = 1
	managedRclonePointLocatorLegacyVersion     = 1
	managedRclonePointLocatorVersion           = 2
	managedRcloneCommitEvidenceVersion         = 1
	maxManagedRclonePreparedAttemptRecordBytes = 64 << 10
	managedRcloneManifestSchemaRevision        = 1
	managedRcloneManifestLimitsRevision        = 1
	managedRclonePortableSettleInterval        = 2 * time.Second
	managedRcloneNativeSessionMargin           = 5 * time.Minute
	managedRcloneNativePageSize                = 1000
)

type managedRclonePublicationRuntime struct {
	repository model.BackupRepository
	task       model.Task
	link       model.TaskRepositoryLink
	binding    managedRcloneBindingDocumentV3
}

type managedRcloneNativeProcessInput struct {
	profile            provider.RcloneNativeProfile
	session            provider.RcloneNativeSession
	factory            RcloneNativeFactory
	rcloneConfig       []byte
	encryption         provider.RcloneNativeEncryptionSelection
	encryptionEvidence provider.RcloneNativeEncryptionEvidence
	keyBindings        []provider.RcloneNativeKMSKeyDigestBinding
	b0                 provider.RcloneNativeStableGraph
	observationLimits  provider.RcloneNativeObservationLimits
	baselineSource     provider.RclonePrivateLocator
	legacyOriginDigest string
}

type managedRcloneNativeBaselineRequest struct {
	source       provider.RcloneNativeBaselineSource
	maxReadBytes uint64
}

type managedRclonePreparedAttemptRecordV1 struct {
	Version          int    `json:"version"`
	TaggedAttempt    string `json:"tagged_attempt"`
	ChildFenceDigest string `json:"child_fence_digest"`
}
type managedRcloneFrozenNativeVersion struct {
	PhysicalKey string `json:"physical_key"`
	VersionID   string `json:"version_id"`
}

// managedRclonePointLocatorV1 is the compatibility view used by the
// repository/lifecycle package. Version 1 is the historical locator shape;
// version 2 is the table-backed shape emitted by current publications. Keep
// the historical native fields in this view so old points can be decoded, but
// never emit or accept them as part of a version 2 locator.
type managedRclonePointLocatorV1 struct {
	Version                      int                                `json:"version"`
	Provider                     backupasset.ProviderKind           `json:"provider"`
	RepositoryID                 string                             `json:"repository_id"`
	RecoveryPointID              string                             `json:"recovery_point_id"`
	AttemptID                    string                             `json:"attempt_id"`
	PublicationMode              backupasset.TaskPublicationMode    `json:"publication_mode"`
	TaggedAttempt                string                             `json:"tagged_attempt"`
	TaggedCommit                 string                             `json:"tagged_commit"`
	ChildFenceDigest             string                             `json:"child_fence_digest"`
	CommitPayloadDigest          string                             `json:"commit_payload_digest"`
	PortableAttemptRoot          string                             `json:"portable_attempt_root,omitempty"`
	NativeCommitKey              string                             `json:"native_commit_key,omitempty"`
	NativeCommitVersionID        string                             `json:"native_commit_version_id,omitempty"`
	FrozenNativeVersions         []managedRcloneFrozenNativeVersion `json:"frozen_native_versions,omitempty"`
	FrozenNativeVersionCount     uint64                             `json:"frozen_native_version_count"`
	FrozenNativeVersionsDigest   string                             `json:"frozen_native_versions_digest"`
	FrozenNativeReferenceCount   uint64                             `json:"frozen_native_reference_count"`
	FrozenNativeReferencesDigest string                             `json:"frozen_native_references_digest"`
	PhysicalIdentityDigest       string                             `json:"physical_identity_digest"`
	ProviderCommitDigest         string                             `json:"provider_commit_digest"`
	ManifestControlIdentity      string                             `json:"manifest_control_identity"`
}

type managedRclonePortablePointLocatorV1Wire struct {
	Version                 int                             `json:"version"`
	Provider                backupasset.ProviderKind        `json:"provider"`
	RepositoryID            string                          `json:"repository_id"`
	RecoveryPointID         string                          `json:"recovery_point_id"`
	AttemptID               string                          `json:"attempt_id"`
	PublicationMode         backupasset.TaskPublicationMode `json:"publication_mode"`
	TaggedAttempt           string                          `json:"tagged_attempt"`
	TaggedCommit            string                          `json:"tagged_commit"`
	ChildFenceDigest        string                          `json:"child_fence_digest"`
	CommitPayloadDigest     string                          `json:"commit_payload_digest"`
	PortableAttemptRoot     string                          `json:"portable_attempt_root,omitempty"`
	PhysicalIdentityDigest  string                          `json:"physical_identity_digest"`
	ProviderCommitDigest    string                          `json:"provider_commit_digest"`
	ManifestControlIdentity string                          `json:"manifest_control_identity"`
}

type rclonePublicationExecution struct {
	service     *PublicationService
	token       publication.AdmissionToken
	attempt     provider.RcloneAttemptV1
	audit       backupasset.PublicationAuditContext
	binding     managedRcloneBindingDocumentV3
	task        model.Task
	markerKey   []byte
	childFence  backupasset.LeaseFence
	nativeInput *managedRcloneNativeProcessInput

	context        context.Context
	cancel         context.CancelCauseFunc
	deadlineCancel context.CancelFunc
	heartbeat      chan struct{}
	heartbeatW     sync.WaitGroup
	stopOnce       sync.Once
	closeOnce      sync.Once
}

func (service *PublicationService) prepareRclonePublication(ctx context.Context, run publication.Run, token publication.AdmissionToken) (publication.Execution, error) {
	var links []model.TaskRepositoryLink
	if err := service.db.WithContext(ctx).Where("task_id = ? AND unlinked_at IS NULL", run.Task.ID).Find(&links).Error; err != nil {
		_ = token.Close()
		return nil, fmt.Errorf("load Rclone publication link: %w", err)
	}
	if len(links) == 0 || len(links) == 1 && links[0].PublicationMode == string(backupasset.PublicationLegacyMutable) {
		return service.prepareRcloneCompatibility(ctx, run, token)
	}
	if len(links) != 1 {
		_ = token.Close()
		return nil, fmt.Errorf("%w: ambiguous Rclone publication links", backupasset.ErrConflict)
	}
	switch backupasset.TaskPublicationMode(links[0].PublicationMode) {
	case backupasset.PublicationVersionedPrefix, backupasset.PublicationNativeObjectVersions:
		return service.prepareManagedRcloneEvidence(ctx, run, token)
	default:
		_ = token.Close()
		return nil, fmt.Errorf("%w: unsupported Rclone publication mode", backupasset.ErrConflict)
	}
}

func (service *PublicationService) prepareRcloneCompatibility(ctx context.Context, run publication.Run, token publication.AdmissionToken) (publication.Execution, error) {
	var taskEntity model.Task
	if err := service.db.WithContext(ctx).Where("archived_at IS NULL").First(&taskEntity, run.Task.ID).Error; err != nil {
		_ = token.Close()
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: Rclone compatibility Task", backupasset.ErrNotFound)
		}
		return nil, fmt.Errorf("load Rclone compatibility Task: %w", err)
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

func (service *PublicationService) prepareManagedRcloneEvidence(ctx context.Context, run publication.Run, token publication.AdmissionToken) (publication.Execution, error) {
	if _, err := service.registry.PublicationStrategy(backupasset.ProviderRclone); err != nil {
		_ = token.Close()
		return nil, err
	}
	runtime, err := service.loadExactManagedRclonePublicationRuntime(ctx, run.Task.ID)
	if err != nil {
		_ = token.Close()
		return nil, err
	}
	if err := service.ensureNoUnresolvedRcloneWriter(ctx, runtime.repository.ID); err != nil {
		_ = token.Close()
		return nil, err
	}
	leaseConfig, err := service.foundation.LeaseConfig()
	if err != nil {
		_ = token.Close()
		return nil, err
	}
	publicationConfig, err := service.foundation.PublicationConfig()
	if err != nil {
		_ = token.Close()
		return nil, err
	}
	markerKey, err := service.rcloneMarkerKey(ctx, runtime.repository.ID)
	if err != nil {
		_ = token.Close()
		return nil, err
	}
	preparedAt := service.now().UTC()
	var nativeInput *managedRcloneNativeProcessInput
	if runtime.binding.PublicationMode == backupasset.PublicationNativeObjectVersions {
		nativeInput, err = service.prepareRcloneNativeProcessInput(
			ctx, runtime.binding, markerKey, leaseConfig, publicationConfig, preparedAt, time.Time{}, true, nil,
		)
		if err != nil {
			_ = token.Close()
			return nil, err
		}
	}
	attempt, childLease, err := service.prepareRclonePoint(ctx, run, runtime, markerKey, leaseConfig, publicationConfig, preparedAt, nativeInput)
	if err != nil {
		_ = token.Close()
		return nil, err
	}
	execution := newRclonePublicationExecution(
		service, token, attempt, run.Audit, runtime.binding, runtime.task, markerKey,
		childLease.Fence, nativeInput, leaseConfig, ctx,
	)
	if err := service.writeRclonePublicationAudit(ctx, run.Audit, backupasset.AuditActionRecoveryPointPublicationPrepare,
		backupasset.AuditOutcomeSuccess, attempt, publication.StageExecution, backupasset.RecoveryPointPreparing, "", ""); err != nil {
		service.metrics.ObserveAuditFailure(publication.StageExecution)
		_ = execution.Abandon(backupasset.ErrPublicationSessionAbandoned)
		return nil, err
	}
	return execution, nil
}

// PublishImportedRcloneBaseline consumes the point, attempt, and lease that
// activation reserved atomically. It deliberately acquires managed admission
// only after that transaction commits, so no Provider command can observe a
// half-installed binding or an unreserved migration run.
func (service *PublicationService) PublishImportedRcloneBaseline(
	ctx context.Context,
	activation rcloneImportedBaselineActivation,
) (publication.Outcome, error) {
	if service == nil || service.registry == nil || service.admission == nil || service.lease == nil ||
		activation.taskID == 0 || activation.taskRunID == 0 || activation.attempt.TaskID != activation.taskID ||
		activation.attempt.TaskRunID != activation.taskRunID || !activation.attempt.ImportedBaseline ||
		backupasset.ValidateOpaqueID(activation.preflightID) != nil || activation.preflightID != activation.attempt.PreflightID ||
		strings.TrimSpace(activation.legacyLocator) == "" || activation.startedAt.IsZero() ||
		activation.childLease.RecoveryPointID != activation.attempt.RecoveryPointID ||
		activation.childLease.Fence.RecoveryPointID != activation.attempt.RecoveryPointID ||
		activation.attempt.Validate() != nil || len(activation.markerKey) < 32 {
		return publication.Outcome{}, fmt.Errorf("%w: imported Rclone baseline activation is invalid", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	token, err := service.admission.Acquire(ctx, publication.OperationEvidenceBackup)
	if err != nil {
		return publication.Outcome{}, err
	}
	if token == nil || token.Mode() != publication.AdmissionManaged {
		if token != nil {
			_ = token.Close()
		}
		return publication.Outcome{}, fmt.Errorf("%w: imported Rclone baseline requires managed admission", backupasset.ErrForbidden)
	}
	runtime, err := service.loadExactManagedRclonePublicationRuntime(ctx, activation.taskID)
	if err != nil {
		_ = token.Close()
		return publication.Outcome{}, err
	}
	if !reflect.DeepEqual(runtime.binding, activation.binding) ||
		runtime.repository.ID != activation.attempt.RepositoryID || runtime.link.ID != activation.attempt.TaskRepositoryLinkID {
		_ = token.Close()
		return publication.Outcome{}, fmt.Errorf("%w: imported Rclone baseline binding changed", backupasset.ErrConflict)
	}
	var point model.RecoveryPoint
	if err := service.db.WithContext(ctx).First(&point, "id = ?", activation.attempt.RecoveryPointID).Error; err != nil {
		_ = token.Close()
		return publication.Outcome{}, fmt.Errorf("load imported Rclone baseline reservation: %w", err)
	}
	persistedAttempt, childFenceDigest, err := decodeManagedRclonePreparedAttemptRecord(point.EncryptedProviderLocator)
	if err != nil || !reflect.DeepEqual(persistedAttempt, activation.attempt) ||
		childFenceDigest != activation.attempt.ChildFenceDigest || point.Semantics != string(backupasset.PointImportedBaseline) ||
		point.State != string(backupasset.RecoveryPointPreparing) || point.ProducingTaskRunID == nil ||
		*point.ProducingTaskRunID != activation.taskRunID {
		_ = token.Close()
		if err != nil {
			return publication.Outcome{}, err
		}
		return publication.Outcome{}, fmt.Errorf("%w: imported Rclone baseline reservation changed", backupasset.ErrConflict)
	}
	if err := service.lease.ValidateFence(ctx, activation.childLease.Fence); err != nil {
		_ = token.Close()
		return publication.Outcome{}, err
	}
	audit, err := backupasset.NewSystemPublicationAuditContext("rclone-baseline-" + activation.preflightID)
	if err != nil {
		_ = token.Close()
		return publication.Outcome{}, err
	}
	execution := newRclonePublicationExecution(
		service, token, activation.attempt, audit, runtime.binding, runtime.task,
		activation.markerKey, activation.childLease.Fence, activation.nativeInput,
		activation.leaseConfig, ctx,
	)
	input, err := execution.RclonePublicationInput()
	if err != nil {
		rejectErr := execution.Reject(context.WithoutCancel(ctx), backupasset.FailurePublicationPreconditionMissing)
		runErr := service.recordImportedRcloneBaselineTaskRun(context.WithoutCancel(ctx), activation.taskID, activation.taskRunID, provider.ProviderExecutionResult{}, err)
		return publication.Outcome{}, errors.Join(err, rejectErr, runErr)
	}
	switch activation.attempt.PublicationMode {
	case backupasset.PublicationVersionedPrefix:
		if input.PortableRequest == nil || input.NativeRequest != nil || input.PortableRequest.Source != (provider.RclonePrivateLocator{}) {
			err = fmt.Errorf("%w: imported portable Rclone baseline input is invalid", backupasset.ErrInvalidState)
		} else {
			source, sourceErr := provider.NewRclonePrivateLocator(strings.TrimSpace(activation.legacyLocator))
			if sourceErr != nil {
				err = sourceErr
			} else {
				input.PortableRequest.Source = source
			}
		}
	case backupasset.PublicationNativeObjectVersions:
		if input.NativeRequest == nil || input.PortableRequest != nil || input.NativeRequest.Source != (provider.RclonePrivateLocator{}) ||
			activation.nativeInput == nil || activation.nativeInput.baselineSource == (provider.RclonePrivateLocator{}) {
			err = fmt.Errorf("%w: imported native Rclone baseline input is invalid", backupasset.ErrInvalidState)
		} else {
			input.NativeRequest.Source = activation.nativeInput.baselineSource
		}
	default:
		err = fmt.Errorf("%w: imported Rclone baseline mode is unsupported", backupasset.ErrCapabilityUnavailable)
	}
	if err != nil {
		rejectErr := execution.Reject(context.WithoutCancel(ctx), backupasset.FailurePublicationPreconditionMissing)
		runErr := service.recordImportedRcloneBaselineTaskRun(context.WithoutCancel(ctx), activation.taskID, activation.taskRunID, provider.ProviderExecutionResult{}, err)
		return publication.Outcome{}, errors.Join(err, rejectErr, runErr)
	}
	strategy, err := service.registry.PublicationStrategy(backupasset.ProviderRclone)
	if err != nil {
		rejectErr := execution.Reject(context.WithoutCancel(ctx), backupasset.FailurePublicationPreconditionMissing)
		runErr := service.recordImportedRcloneBaselineTaskRun(context.WithoutCancel(ctx), activation.taskID, activation.taskRunID, provider.ProviderExecutionResult{}, err)
		return publication.Outcome{}, errors.Join(err, rejectErr, runErr)
	}
	prepared, err := strategy.Prepare(execution.Context(), provider.PublicationPrepareRequest{
		Attempt: provider.NewRclonePublicationAttempt(activation.attempt), RcloneInput: &input,
	})
	if err != nil {
		rejectErr := execution.Reject(context.WithoutCancel(ctx), backupasset.FailurePublicationPreconditionMissing)
		runErr := service.recordImportedRcloneBaselineTaskRun(context.WithoutCancel(ctx), activation.taskID, activation.taskRunID, provider.ProviderExecutionResult{}, err)
		return publication.Outcome{}, errors.Join(err, rejectErr, runErr)
	}
	result, executeErr := strategy.Execute(execution.Context(), prepared, provider.PublicationProgress{})
	if runErr := service.recordImportedRcloneBaselineTaskRun(
		context.WithoutCancel(ctx), activation.taskID, activation.taskRunID, result, executeErr,
	); runErr != nil {
		_ = execution.Abandon(backupasset.ErrPublicationSessionAbandoned)
		return publication.Outcome{}, runErr
	}
	if executeErr != nil || result.Completion != backupasset.CompletionKnownExitZero || result.ExitCode != 0 ||
		result.EvidenceCode != "" || result.ProviderCommit == nil {
		var resolutionErr error
		switch result.Completion {
		case backupasset.CompletionKnownNonzero:
			resolutionErr = execution.Fail(context.WithoutCancel(ctx), backupasset.FailureProviderNonzeroExit)
		case backupasset.CompletionOutcomeUnknown:
			resolutionErr = execution.Defer(context.WithoutCancel(ctx), publication.Deferral{
				Completion: backupasset.CompletionOutcomeUnknown, Code: backupasset.FailureProviderOutcomeUnknown,
			})
		default:
			resolutionErr = execution.Fail(context.WithoutCancel(ctx), backupasset.FailureManifestUnavailable)
		}
		return publication.Outcome{}, errors.Join(
			fmt.Errorf("%w: imported Rclone baseline provider completion is not proven", backupasset.ErrConflict),
			executeErr, resolutionErr,
		)
	}
	commit, err := strategy.RecordCommit(execution.Context(), prepared, result)
	if err != nil {
		_ = execution.Abandon(backupasset.ErrPublicationSessionAbandoned)
		return publication.Outcome{}, err
	}
	outcome, err := execution.RecordProviderCommit(context.WithoutCancel(ctx), commit)
	if err != nil {
		_ = execution.Abandon(backupasset.ErrPublicationSessionAbandoned)
		return publication.Outcome{}, err
	}
	if outcome.State != backupasset.RecoveryPointVerifying || !outcome.ProviderCommitRecorded {
		return publication.Outcome{}, fmt.Errorf("%w: imported Rclone baseline Provider commit is not verifying", backupasset.ErrConflict)
	}
	outcome, err = service.ProcessPoint(context.WithoutCancel(ctx), outcome.RecoveryPointID)
	if err != nil {
		return publication.Outcome{}, err
	}
	if outcome.State != backupasset.RecoveryPointCommitted || !outcome.ProviderCommitRecorded {
		return publication.Outcome{}, fmt.Errorf("%w: imported Rclone baseline verification did not commit", backupasset.ErrConflict)
	}
	return outcome, nil
}

func (service *PublicationService) recordImportedRcloneBaselineTaskRun(
	ctx context.Context,
	taskID, taskRunID uint,
	result provider.ProviderExecutionResult,
	runErr error,
) error {
	if service == nil || service.db == nil || taskID == 0 || taskRunID == 0 {
		return fmt.Errorf("%w: imported Rclone baseline TaskRun dependencies are unavailable", backupasset.ErrInvalidState)
	}
	status, failureCode := importedRsyncBaselineTaskRunOutcome(result, runErr)
	return service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var taskRun model.TaskRun
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&taskRun, taskRunID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: imported Rclone baseline TaskRun", backupasset.ErrNotFound)
			}
			return fmt.Errorf("lock imported Rclone baseline TaskRun: %w", err)
		}
		if taskRun.TaskID != taskID || !model.IsTaskRunNodeSnapshotAuthoritative(taskRun.NodeIDSnapshot) {
			return fmt.Errorf("%w: imported Rclone baseline TaskRun lineage changed", backupasset.ErrConflict)
		}
		if !activeTaskRunStatus(taskRun.Status) {
			if taskRun.Status == status && taskRun.LastError == failureCode {
				return nil
			}
			return fmt.Errorf("%w: imported Rclone baseline TaskRun is already terminal", backupasset.ErrConflict)
		}
		now := service.now().UTC()
		duration := int64(0)
		if taskRun.StartedAt != nil && !taskRun.StartedAt.IsZero() && !now.Before(taskRun.StartedAt.UTC()) {
			duration = now.Sub(taskRun.StartedAt.UTC()).Milliseconds()
		}
		updates := map[string]any{
			"status": status, "finished_at": now, "duration_ms": duration, "last_error": failureCode, "updated_at": now,
		}
		result := tx.Model(&model.TaskRun{}).Where(
			"id = ? AND task_id = ? AND status IN ?", taskRun.ID, taskID, []string{"pending", "running", "retrying"},
		).Updates(updates)
		if result.Error != nil {
			return fmt.Errorf("record imported Rclone baseline TaskRun: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("%w: imported Rclone baseline TaskRun changed", backupasset.ErrConflict)
		}
		return nil
	})
}

func (service *PublicationService) prepareRcloneNativeProcessInput(
	ctx context.Context,
	binding managedRcloneBindingDocumentV3,
	markerKey []byte,
	leaseConfig backupasset.LeaseConfig,
	publicationConfig backupasset.PublicationConfig,
	preparedAt time.Time,
	pointDeadline time.Time,
	captureB0 bool,
	baseline *managedRcloneNativeBaselineRequest,
) (*managedRcloneNativeProcessInput, error) {
	if service == nil || service.rcloneNativeFactoryBuilder == nil || binding.Native == nil || len(markerKey) < 32 {
		return nil, fmt.Errorf("%w: native Rclone publication dependencies are unavailable", backupasset.ErrInvalidState)
	}
	native := binding.Native
	profile, err := managedRcloneNativeProfile(binding)
	if err != nil {
		return nil, err
	}
	bootstrap := provider.RcloneNativeBootstrap{}
	switch native.Bootstrap.Mode {
	case managedRcloneBootstrapWorkloadChain:
		bootstrap.Kind = provider.RcloneNativeBootstrapWorkloadChain
	case managedRcloneBootstrapStaticSTS:
		bootstrap.Kind = provider.RcloneNativeBootstrapStaticSTS
		bootstrap.AccessKeyID = native.Bootstrap.Static.AccessKeyID
		bootstrap.SecretAccessKey = native.Bootstrap.Static.SecretAccessKey
	default:
		return nil, fmt.Errorf("%w: unsupported native Rclone bootstrap", backupasset.ErrInvalidState)
	}
	factory, err := service.rcloneNativeFactoryBuilder(ctx, bootstrap, native.Region, publicationConfig.Rclone.AWSSDKMaxAttempts)
	if err != nil {
		return nil, fmt.Errorf("create native Rclone AWS factory: %w", err)
	}
	if factory == nil {
		return nil, fmt.Errorf("%w: native Rclone AWS factory unavailable", backupasset.ErrCapabilityUnavailable)
	}
	bootstrapTemporary, err := factory.BootstrapCredentialsExpire(ctx)
	if err != nil {
		return nil, err
	}
	encryption := provider.RcloneNativeEncryptionSelection{Profile: native.EncryptionProfile, ActiveKeyARN: native.ActiveKMSKeyARN}
	keyBindings := make([]provider.RcloneNativeKMSKeyDigestBinding, 0, 1+len(native.RetainedReadKeys))
	if native.EncryptionProfile == provider.RcloneNativeSSEKMSV1 {
		keyBindings = append(keyBindings, provider.RcloneNativeKMSKeyDigestBinding{KeyARN: native.ActiveKMSKeyARN, Digest: native.ActiveKMSKeyDigest})
	}
	for _, key := range native.RetainedReadKeys {
		encryption.RetainedReadKeyARNs = append(encryption.RetainedReadKeyARNs, key.KeyARN)
		keyBindings = append(keyBindings, provider.RcloneNativeKMSKeyDigestBinding{KeyARN: key.KeyARN, Digest: key.KeyDigest})
	}
	expectedEncryption, err := provider.BuildRcloneNativeEncryptionEvidence(encryption, keyBindings, native.BucketKeyEnabled)
	if err != nil {
		return nil, err
	}
	deadline := pointDeadline.UTC()
	if pointDeadline.IsZero() {
		deadline = managedRclonePointDeadline(preparedAt, binding, leaseConfig, publicationConfig)
	}
	observationLimits, err := managedRcloneNativeObservationLimits(publicationConfig.ManifestMaxEntries)
	if err != nil {
		return nil, err
	}
	sessionRequest := provider.RcloneNativeSessionRequest{
		Profile: profile, RoleARN: native.RoleARN, ExternalID: native.ExternalID, PointDeadlineAt: deadline,
		SessionMargin: managedRcloneNativeSessionMargin, BootstrapTemporary: bootstrapTemporary,
		Encryption: encryption, BucketKeyEnabled: native.BucketKeyEnabled,
	}
	establishSession := func(sourcePrefixes, sourceKeys []string) (provider.RcloneNativeSessionResult, error) {
		request := sessionRequest
		request.SourceReadPrefixes = append([]string(nil), sourcePrefixes...)
		request.SourceDecryptKeyARNs = append([]string(nil), sourceKeys...)
		return provider.EstablishRcloneNativeSession(ctx, factory, factory, request, preparedAt, cryptorand.Reader)
	}

	var sessionResult provider.RcloneNativeSessionResult
	baselineSource := provider.RclonePrivateLocator{}
	legacyOriginDigest := ""
	if baseline == nil {
		sessionResult, err = establishSession(nil, nil)
		if err != nil {
			return nil, err
		}
	} else {
		if baseline.maxReadBytes == 0 || baseline.source.SourcePrefix == "" {
			return nil, fmt.Errorf("%w: native Rclone baseline source is invalid", backupasset.ErrInvalidState)
		}
		sourcePrefixes := []string{baseline.source.SourcePrefix}
		discoverySession, err := establishSession(sourcePrefixes, nil)
		if err != nil {
			return nil, err
		}
		discoveryS3, err := factory.BaselineS3(discoverySession.Session, profile, sourcePrefixes)
		if err != nil {
			return nil, err
		}
		if discoveryS3 == nil {
			return nil, fmt.Errorf("%w: native Rclone baseline discovery client unavailable", backupasset.ErrCapabilityUnavailable)
		}
		inventoryRequest := provider.RcloneNativeBaselineInventoryRequest{
			SourcePrefix: baseline.source.SourcePrefix, ObservationLimits: observationLimits, MaxReadBytes: baseline.maxReadBytes,
		}
		discovery, err := provider.DiscoverRcloneNativeBaselineSource(ctx, discoveryS3, inventoryRequest)
		if err != nil {
			return nil, err
		}
		sessionResult, err = establishSession(sourcePrefixes, discovery.SourceKMSKeyARNs)
		if err != nil {
			return nil, err
		}
		var inspector provider.KMSKeyInspector
		if len(discovery.SourceKMSKeyARNs) > 0 {
			inspector, err = factory.KMS(sessionResult.Session, profile.Region)
			if err != nil {
				return nil, err
			}
			if inspector == nil {
				return nil, fmt.Errorf("%w: native Rclone baseline KMS inspector unavailable", backupasset.ErrCapabilityUnavailable)
			}
		}
		accountID, ok := managedRcloneAWSRoleAccount(native.RoleARN)
		if !ok {
			return nil, fmt.Errorf("%w: native Rclone role identity is invalid", backupasset.ErrInvalidState)
		}
		sourceKeys, err := provider.ValidateRcloneNativeSourceKMSKeys(ctx, inspector, provider.RcloneNativeSourceKMSValidationRequest{
			KeyARNs: discovery.SourceKMSKeyARNs,
			SessionPolicy: provider.RcloneNativeSessionPolicyRequest{
				Profile: profile, AccountID: accountID, Encryption: encryption, BucketKeyEnabled: native.BucketKeyEnabled,
				SourceReadPrefixes: sourcePrefixes,
			},
			Limits: provider.RcloneNativeKMSLimits{
				MaxReadKeys: publicationConfig.Rclone.KMSReadKeyMaxCount, MaxSerializedBytes: managedRcloneRetainedReadKeyBytesMaximum,
			},
		})
		if err != nil {
			return nil, err
		}
		if sourceKeys.SessionPolicy != sessionResult.SessionPolicy {
			return nil, fmt.Errorf("%w: native Rclone baseline session policy drift", backupasset.ErrConflict)
		}
		finalS3, err := factory.BaselineS3(sessionResult.Session, profile, sourcePrefixes)
		if err != nil {
			return nil, err
		}
		if finalS3 == nil {
			return nil, fmt.Errorf("%w: native Rclone baseline verification client unavailable", backupasset.ErrCapabilityUnavailable)
		}
		inventory, err := provider.InspectRcloneNativeBaselineSource(ctx, finalS3, inventoryRequest)
		if err != nil {
			return nil, err
		}
		if discovery.ObjectCount != inventory.ObjectCount || discovery.LogicalBytes != inventory.LogicalBytes ||
			!reflect.DeepEqual(discovery.SourceKMSKeyARNs, inventory.SourceKMSKeyARNs) ||
			!reflect.DeepEqual(discovery.Objects, inventory.Objects) {
			return nil, fmt.Errorf("%w: native Rclone baseline source changed during admission", backupasset.ErrConflict)
		}
		legacyOriginDigest, err = digestRcloneNativeBaselineOriginEvidence(
			binding.LegacyBindingDigest, discovery.Digest, inventory.Digest, sourceKeys.KeySetDigest,
		)
		if err != nil {
			return nil, err
		}
		baselineSource = baseline.source.PublicationSource
	}

	s3, err := factory.S3(sessionResult.Session, profile, keyBindings)
	if err != nil {
		return nil, err
	}
	if s3 == nil {
		return nil, fmt.Errorf("%w: native Rclone S3 client unavailable", backupasset.ErrCapabilityUnavailable)
	}
	if err := validateRcloneNativeBoundCapability(
		ctx, factory, sessionResult.Session, s3, profile, binding, encryption, expectedEncryption,
		publicationConfig.Rclone.KMSReadKeyMaxCount, preparedAt,
	); err != nil {
		return nil, err
	}
	b0 := provider.RcloneNativeStableGraph{}
	if captureB0 {
		b0, err = provider.CaptureRcloneNativeStableGraph(ctx, s3, profile.ManagedPrefix, observationLimits)
		if err != nil {
			return nil, err
		}
	}
	return &managedRcloneNativeProcessInput{
		profile: profile, session: sessionResult.Session, factory: factory,
		rcloneConfig: append([]byte(nil), sessionResult.RcloneConfig...), encryption: encryption,
		encryptionEvidence: expectedEncryption, keyBindings: append([]provider.RcloneNativeKMSKeyDigestBinding(nil), keyBindings...),
		b0: b0, observationLimits: observationLimits, baselineSource: baselineSource, legacyOriginDigest: legacyOriginDigest,
	}, nil
}

func managedRcloneNativeProfile(binding managedRcloneBindingDocumentV3) (provider.RcloneNativeProfile, error) {
	if binding.Native == nil {
		return provider.RcloneNativeProfile{}, fmt.Errorf("%w: native Rclone binding is unavailable", backupasset.ErrInvalidState)
	}
	profile := provider.RcloneNativeProfile{
		Code: binding.Native.ProfileCode, Region: binding.Native.Region, Bucket: binding.Native.Bucket,
		ManagedPrefix: binding.Native.ManagedPrefix, EndpointMode: provider.RcloneNativeEndpointAWSRegional,
		AddressingMode: provider.RcloneNativeAddressingDNS, BucketKind: provider.RcloneNativeBucketGeneralPurpose,
	}
	if err := provider.ValidateRcloneNativeProfile(profile); err != nil {
		return provider.RcloneNativeProfile{}, err
	}
	return profile, nil
}

func digestRcloneNativeBaselineOriginEvidence(legacyBinding, discovery, inventory, sourceKeySet string) (string, error) {
	writer := backupasset.NewCanonicalSHA256()
	writer.String("xirang-rclone-native-baseline-origin-v1")
	writer.String(legacyBinding)
	writer.String(discovery)
	writer.String(inventory)
	writer.String(sourceKeySet)
	return writer.HexDigest()
}

func validateRcloneNativeBoundCapability(
	ctx context.Context,
	factory RcloneNativeFactory,
	session provider.RcloneNativeSession,
	s3 provider.S3Native,
	profile provider.RcloneNativeProfile,
	binding managedRcloneBindingDocumentV3,
	encryption provider.RcloneNativeEncryptionSelection,
	expectedEncryption provider.RcloneNativeEncryptionEvidence,
	maxReadKeys int,
	checkedAt time.Time,
) error {
	native := binding.Native
	accountID, ok := managedRcloneAWSRoleAccount(native.RoleARN)
	if !ok {
		return fmt.Errorf("%w: native Rclone role identity is invalid", backupasset.ErrInvalidState)
	}
	identity, err := s3.BucketIdentity(ctx, profile)
	if err != nil {
		return err
	}
	if identity.AccountID != accountID || identity.Region != profile.Region || identity.Kind != provider.RcloneNativeBucketGeneralPurpose {
		return fmt.Errorf("%w: native Rclone bucket identity drift", backupasset.ErrConflict)
	}
	versioning, err := s3.GetVersioning(ctx, profile)
	if err != nil {
		return err
	}
	if err := provider.ValidateRcloneNativeVersioning(versioning, native.VersioningDigest, native.CapabilityStableObservedAt, checkedAt); err != nil {
		return err
	}
	lifecycle, err := s3.GetLifecycle(ctx, profile)
	if err != nil {
		return err
	}
	if err := provider.ValidateRcloneNativeLifecycle(lifecycle, profile.ManagedPrefix, native.LifecycleDigest, native.CapabilityStableObservedAt, checkedAt); err != nil {
		return err
	}
	bucketEncryption, err := s3.GetEncryption(ctx, profile)
	if err != nil {
		return err
	}
	bucketEncryptionDigest, err := provider.CanonicalRcloneNativeBucketEncryptionDigest(bucketEncryption)
	if err != nil || bucketEncryptionDigest != native.BucketEncryptionDigest {
		return fmt.Errorf("%w: native Rclone bucket encryption drift", backupasset.ErrConflict)
	}
	keys := make([]provider.RcloneNativeKMSKey, 0, 1+len(encryption.RetainedReadKeyARNs))
	if encryption.Profile == provider.RcloneNativeSSEKMSV1 {
		inspector, inspectorErr := factory.KMS(session, profile.Region)
		if inspectorErr != nil {
			return inspectorErr
		}
		if inspector == nil {
			return fmt.Errorf("%w: native Rclone KMS inspector unavailable", backupasset.ErrCapabilityUnavailable)
		}
		for _, arn := range append([]string{encryption.ActiveKeyARN}, encryption.RetainedReadKeyARNs...) {
			key, describeErr := inspector.DescribeKey(ctx, arn)
			if describeErr != nil {
				return describeErr
			}
			keys = append(keys, key)
		}
	}
	actualEncryption, err := provider.ValidateRcloneNativeEncryption(encryption, bucketEncryption, keys, provider.RcloneNativeKMSLimits{
		MaxReadKeys: maxReadKeys, MaxSerializedBytes: managedRcloneRetainedReadKeyBytesMaximum,
	})
	if err != nil {
		return err
	}
	if actualEncryption != expectedEncryption {
		return fmt.Errorf("%w: native Rclone encryption evidence drift", backupasset.ErrConflict)
	}
	return nil
}

func managedRcloneNativeObservationLimits(maxEntries int64) (provider.RcloneNativeObservationLimits, error) {
	if maxEntries <= 0 || maxEntries > math.MaxInt {
		return provider.RcloneNativeObservationLimits{}, fmt.Errorf("%w: native Rclone observation limit is invalid", backupasset.ErrInvalidState)
	}
	maxRecords := int(maxEntries)
	maxPages := (maxRecords + managedRcloneNativePageSize - 1) / managedRcloneNativePageSize
	return provider.RcloneNativeObservationLimits{
		PageSize: managedRcloneNativePageSize, MaxPages: maxPages, MaxRecords: maxRecords,
	}, nil
}

func (service *PublicationService) loadExactManagedRclonePublicationRuntime(ctx context.Context, taskID uint) (managedRclonePublicationRuntime, error) {
	if service == nil {
		return managedRclonePublicationRuntime{}, fmt.Errorf("%w: managed Rclone publication runtime is unavailable", backupasset.ErrInvalidState)
	}
	return service.loadExactManagedRclonePublicationRuntimeTx(ctx, service.db, taskID)
}

func (service *PublicationService) loadExactManagedRclonePublicationRuntimeTx(
	ctx context.Context,
	tx *gorm.DB,
	taskID uint,
) (managedRclonePublicationRuntime, error) {
	if service == nil || tx == nil || taskID == 0 {
		return managedRclonePublicationRuntime{}, fmt.Errorf("%w: managed Rclone publication runtime is unavailable", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// The active link is only a repository-ID preview. All authority rows are
	// then locked in repository -> Task -> link -> binding -> Node -> SSH key
	// order, and the preview is revalidated by the locked link query.
	var previewLink model.TaskRepositoryLink
	if err := tx.WithContext(ctx).Where("task_id = ? AND unlinked_at IS NULL", taskID).First(&previewLink).Error; err != nil {
		return managedRclonePublicationRuntime{}, fmt.Errorf("load managed Rclone publication link preview: %w", err)
	}
	if backupasset.ValidateOpaqueID(previewLink.RepositoryID) != nil {
		return managedRclonePublicationRuntime{}, fmt.Errorf("%w: managed Rclone publication link repository is invalid", backupasset.ErrConflict)
	}
	var repository model.BackupRepository
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", previewLink.RepositoryID).First(&repository).Error; err != nil {
		return managedRclonePublicationRuntime{}, fmt.Errorf("lock managed Rclone publication repository: %w", err)
	}
	var taskEntity model.Task
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND archived_at IS NULL", taskID).First(&taskEntity).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return managedRclonePublicationRuntime{}, fmt.Errorf("%w: managed Rclone publication Task", backupasset.ErrNotFound)
		}
		return managedRclonePublicationRuntime{}, fmt.Errorf("lock managed Rclone publication Task: %w", err)
	}
	if bindingProviderForTask(taskEntity) != backupasset.ProviderRclone {
		return managedRclonePublicationRuntime{}, fmt.Errorf("%w: managed Rclone publication requires a Rclone Task", backupasset.ErrInvalidState)
	}
	var link model.TaskRepositoryLink
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND task_id = ? AND repository_id = ? AND unlinked_at IS NULL",
			previewLink.ID, taskID, repository.ID).First(&link).Error; err != nil {
		return managedRclonePublicationRuntime{}, fmt.Errorf("lock managed Rclone publication link: %w", err)
	}
	mode := backupasset.TaskPublicationMode(link.PublicationMode)
	version, semantics, state, err := backupasset.MapPublicationMode(backupasset.ProviderRclone, mode)
	if err != nil || semantics != backupasset.PointXirangManifest || state != backupasset.RecoveryPointPreparing {
		return managedRclonePublicationRuntime{}, fmt.Errorf("%w: managed Rclone publication link mode is invalid", backupasset.ErrConflict)
	}
	if repository.ProviderKind != string(backupasset.ProviderRclone) || repository.VersionMode != string(version) ||
		repository.ImmutabilityLevel != string(rcloneImmutability(mode)) || repository.RepositoryIdentity == nil ||
		repository.Status != string(backupasset.RepositoryOnline) {
		return managedRclonePublicationRuntime{}, fmt.Errorf("%w: managed Rclone repository contract mismatch", backupasset.ErrConflict)
	}
	var binding model.RepositoryAccessBinding
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("repository_id = ? AND status = ?", repository.ID, bindingStatusActive).First(&binding).Error; err != nil {
		return managedRclonePublicationRuntime{}, fmt.Errorf("lock managed Rclone publication binding: %w", err)
	}
	stored, err := decodeStoredBindingDocument(binding.EncryptedConfig)
	if err != nil || stored.ManagedRcloneV3 == nil || stored.V1 != nil || stored.ManagedRsyncV2 != nil {
		if err != nil {
			return managedRclonePublicationRuntime{}, err
		}
		return managedRclonePublicationRuntime{}, fmt.Errorf("%w: managed Rclone V3 binding required", backupasset.ErrConflict)
	}
	document := *stored.ManagedRcloneV3
	if err := validateManagedRcloneBindingAssociation(document, managedRcloneBindingAssociation{
		Task: taskEntity, Link: link, Repository: repository,
	}); err != nil {
		return managedRclonePublicationRuntime{}, err
	}
	if document.RollbackPrepared || !document.PreflightExpiresAt.After(service.now().UTC()) ||
		document.CapabilityRevision != uint64(repository.CapabilityRevision) {
		return managedRclonePublicationRuntime{}, fmt.Errorf("%w: managed Rclone preflight or rollback state is not executable", backupasset.ErrForbidden)
	}
	if err := lockLifecycleTaskNodeSSHKeyTx(ctx, tx, &taskEntity); err != nil {
		return managedRclonePublicationRuntime{}, err
	}
	if link.TaskID == nil || *link.TaskID != taskEntity.ID || link.TaskNameSnapshot != taskEntity.Name ||
		link.NodeIDSnapshot != taskEntity.NodeID || link.NodeNameSnapshot != taskEntity.Node.Name {
		return managedRclonePublicationRuntime{}, fmt.Errorf("%w: managed Rclone publication link snapshot changed", backupasset.ErrConflict)
	}
	expectedIdentity, err := managedRcloneRepositoryIdentity(document)
	if err != nil {
		return managedRclonePublicationRuntime{}, err
	}
	if *repository.RepositoryIdentity != expectedIdentity {
		return managedRclonePublicationRuntime{}, fmt.Errorf("%w: managed Rclone repository identity drift", backupasset.ErrConflict)
	}
	return managedRclonePublicationRuntime{repository: repository, task: taskEntity, link: link, binding: document}, nil
}

func (service *PublicationService) ensureNoUnresolvedRcloneWriter(ctx context.Context, repositoryID string) error {
	var count int64
	if err := service.db.WithContext(ctx).Model(&model.RecoveryPoint{}).
		Where("repository_id = ? AND state IN ?", repositoryID, []string{
			string(backupasset.RecoveryPointPreparing), string(backupasset.RecoveryPointVerifying),
		}).Count(&count).Error; err != nil {
		return fmt.Errorf("query unresolved managed Rclone writer: %w", err)
	}
	if count != 0 {
		return fmt.Errorf("%w: unresolved managed Rclone writer exists", backupasset.ErrPublicationInProgress)
	}
	return nil
}

func (service *PublicationService) prepareRclonePoint(
	ctx context.Context,
	run publication.Run,
	runtime managedRclonePublicationRuntime,
	markerKey []byte,
	leaseConfig backupasset.LeaseConfig,
	publicationConfig backupasset.PublicationConfig,
	preparedAt time.Time,
	nativeInput *managedRcloneNativeProcessInput,
) (provider.RcloneAttemptV1, backupasset.Lease, error) {
	var attempt provider.RcloneAttemptV1
	var childLease backupasset.Lease
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		attempt, childLease, err = service.prepareRclonePointTx(
			ctx, tx, run, runtime, markerKey, leaseConfig, publicationConfig, preparedAt, nativeInput,
		)
		return err
	})
	return attempt, childLease, err
}

func (service *PublicationService) prepareRclonePointTx(
	ctx context.Context,
	tx *gorm.DB,
	run publication.Run,
	runtime managedRclonePublicationRuntime,
	markerKey []byte,
	leaseConfig backupasset.LeaseConfig,
	publicationConfig backupasset.PublicationConfig,
	preparedAt time.Time,
	nativeInput *managedRcloneNativeProcessInput,
) (provider.RcloneAttemptV1, backupasset.Lease, error) {
	if service == nil || tx == nil {
		return provider.RcloneAttemptV1{}, backupasset.Lease{}, fmt.Errorf("%w: managed Rclone publication transaction is unavailable", backupasset.ErrInvalidState)
	}
	var repository model.BackupRepository
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&repository, "id = ?", runtime.repository.ID).Error; err != nil {
		return provider.RcloneAttemptV1{}, backupasset.Lease{}, fmt.Errorf("lock managed Rclone publication repository: %w", err)
	}
	if runtime.binding.PublicationMode == backupasset.PublicationNativeObjectVersions {
		if err := rejectManagedRcloneNativeDeletionReservationTx(ctx, tx, repository.ID); err != nil {
			return provider.RcloneAttemptV1{}, backupasset.Lease{}, err
		}
	}
	var taskEntity model.Task
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("archived_at IS NULL").First(&taskEntity, runtime.task.ID).Error; err != nil {
		return provider.RcloneAttemptV1{}, backupasset.Lease{}, fmt.Errorf("lock managed Rclone publication Task: %w", err)
	}
	var taskRun model.TaskRun
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&taskRun, run.TaskRunID).Error; err != nil {
		return provider.RcloneAttemptV1{}, backupasset.Lease{}, fmt.Errorf("lock managed Rclone publication TaskRun: %w", err)
	}
	if !authoritativeTaskRunForTask(taskRun, taskEntity) || !activeTaskRunStatus(taskRun.Status) {
		return provider.RcloneAttemptV1{}, backupasset.Lease{}, fmt.Errorf("%w: TaskRun is not active for managed Rclone publication", backupasset.ErrConflict)
	}
	var link model.TaskRepositoryLink
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND task_id = ? AND repository_id = ? AND unlinked_at IS NULL", runtime.link.ID, taskEntity.ID, repository.ID).
		First(&link).Error; err != nil {
		return provider.RcloneAttemptV1{}, backupasset.Lease{}, fmt.Errorf("lock managed Rclone publication link: %w", err)
	}
	var unresolved int64
	if err := tx.Model(&model.RecoveryPoint{}).Where("repository_id = ? AND state IN ?", repository.ID, []string{
		string(backupasset.RecoveryPointPreparing), string(backupasset.RecoveryPointVerifying),
	}).Count(&unresolved).Error; err != nil {
		return provider.RcloneAttemptV1{}, backupasset.Lease{}, fmt.Errorf("lock unresolved managed Rclone writers: %w", err)
	}
	if unresolved != 0 {
		return provider.RcloneAttemptV1{}, backupasset.Lease{}, fmt.Errorf("%w: unresolved managed Rclone writer exists", backupasset.ErrPublicationInProgress)
	}
	var binding model.RepositoryAccessBinding
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("repository_id = ? AND status = ?", repository.ID, bindingStatusActive).First(&binding).Error; err != nil {
		return provider.RcloneAttemptV1{}, backupasset.Lease{}, fmt.Errorf("lock managed Rclone publication binding: %w", err)
	}
	stored, err := decodeStoredBindingDocument(binding.EncryptedConfig)
	if err != nil || stored.ManagedRcloneV3 == nil || !reflect.DeepEqual(*stored.ManagedRcloneV3, runtime.binding) {
		if err != nil {
			return provider.RcloneAttemptV1{}, backupasset.Lease{}, err
		}
		return provider.RcloneAttemptV1{}, backupasset.Lease{}, fmt.Errorf("%w: managed Rclone publication binding changed", backupasset.ErrConflict)
	}
	if err := validateManagedRcloneBindingAssociation(runtime.binding, managedRcloneBindingAssociation{
		Task: taskEntity, Link: link, Repository: repository,
	}); err != nil {
		return provider.RcloneAttemptV1{}, backupasset.Lease{}, err
	}
	if err := lockLifecycleTaskNodeSSHKeyTx(ctx, tx, &taskEntity); err != nil {
		return provider.RcloneAttemptV1{}, backupasset.Lease{}, err
	}
	pointID, err := deriveRecoveryPointID(link.ID, taskRun.ID)
	if err != nil {
		return provider.RcloneAttemptV1{}, backupasset.Lease{}, err
	}
	startedAt := run.StartedAt.UTC()
	if startedAt.IsZero() && taskRun.StartedAt != nil {
		startedAt = taskRun.StartedAt.UTC()
	}
	if startedAt.IsZero() {
		return provider.RcloneAttemptV1{}, backupasset.Lease{}, fmt.Errorf("%w: managed Rclone TaskRun start time is missing", backupasset.ErrInvalidState)
	}
	deadline := managedRclonePointDeadline(preparedAt, runtime.binding, leaseConfig, publicationConfig)
	trigger := run.Trigger
	if trigger == "" {
		trigger = taskRun.TriggerType
	}
	lineage, err := rclonePublicationLineageForRun(link, taskEntity, taskRun, trigger, run.ChainRunID, startedAt, preparedAt, deadline)
	if err != nil {
		return provider.RcloneAttemptV1{}, backupasset.Lease{}, err
	}
	encodedLineage, err := backupasset.EncodePublicationLineage(lineage)
	if err != nil {
		return provider.RcloneAttemptV1{}, backupasset.Lease{}, err
	}
	emptyConsistency, err := backupasset.EncodePublicationConsistency(backupasset.PublicationConsistencyV1{Version: 1})
	if err != nil {
		return provider.RcloneAttemptV1{}, backupasset.Lease{}, err
	}
	semantics := backupasset.PointXirangManifest
	if run.ImportedBaseline {
		semantics = backupasset.PointImportedBaseline
	}
	point := model.RecoveryPoint{
		ID: pointID, RepositoryID: repository.ID, ProducingTaskID: &taskEntity.ID, ProducingTaskRunID: &taskRun.ID,
		ProducingTaskNameSnapshot: taskEntity.Name, ProducingNodeIDSnapshot: taskEntity.NodeID, ProducingNodeNameSnapshot: taskEntity.Node.Name,
		LineageJSON: encodedLineage, Semantics: string(semantics), State: string(backupasset.RecoveryPointPreparing),
		ManifestDigestAlgorithm: "sha256", ConsistencyJSON: emptyConsistency, FidelityJSON: "{}",
		CapabilityRevision: repository.CapabilityRevision, CapabilitiesJSON: repository.CapabilitiesJSON,
		ImmutabilityLevel: string(rcloneImmutability(runtime.binding.PublicationMode)), PhysicalAvailability: string(backupasset.PhysicalUnknown),
		HoldState: string(backupasset.HoldNone), CreatedAt: preparedAt, UpdatedAt: preparedAt,
	}
	var existing model.RecoveryPoint
	result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", pointID).Limit(1).Find(&existing)
	if result.Error != nil {
		return provider.RcloneAttemptV1{}, backupasset.Lease{}, fmt.Errorf("load deterministic managed Rclone point: %w", result.Error)
	}
	if result.RowsAffected != 0 {
		return provider.RcloneAttemptV1{}, backupasset.Lease{}, fmt.Errorf("%w: deterministic managed Rclone point already exists", backupasset.ErrPublicationInProgress)
	}
	if err := tx.Create(&point).Error; err != nil {
		return provider.RcloneAttemptV1{}, backupasset.Lease{}, fmt.Errorf("create deterministic managed Rclone point: %w", err)
	}
	childLease, err := service.lease.AcquireTx(ctx, tx, backupasset.AcquireLeaseRequest{
		RecoveryPointID: point.ID, HolderType: backupasset.LeaseHolderPointPublication,
		OwnerID: publicationLeaseOwner, AbsoluteDeadline: lineage.PointDeadlineAt,
	})
	if err != nil {
		return provider.RcloneAttemptV1{}, backupasset.Lease{}, err
	}
	attemptID, err := provider.NewRclonePortableAttemptID(nil)
	if err != nil {
		return provider.RcloneAttemptV1{}, backupasset.Lease{}, err
	}
	taskRevision, err := managedRcloneTaskRevision(taskEntity)
	if err != nil {
		return provider.RcloneAttemptV1{}, backupasset.Lease{}, err
	}
	limits := rcloneManifestLimits(publicationConfig)
	limitsDigest, err := digestRcloneManifestLimits(limits)
	if err != nil {
		return provider.RcloneAttemptV1{}, backupasset.Lease{}, err
	}
	configDigest := runtime.binding.PreflightDigest
	if runtime.binding.Portable != nil {
		configDigest = runtime.binding.Portable.ConfigDigest
	} else if nativeInput != nil {
		configDigest = digestText(string(nativeInput.rcloneConfig))
	}
	legacyOriginDigest := runtime.binding.LegacyBindingDigest
	if run.ImportedBaseline && nativeInput != nil && nativeInput.legacyOriginDigest != "" {
		legacyOriginDigest = nativeInput.legacyOriginDigest
	}
	attempt := provider.RcloneAttemptV1{
		SchemaVersion: 1, LayoutVersion: 1, MinimumRuntimeRevision: runtime.binding.MinimumRuntimeRevision,
		Provider: backupasset.ProviderRclone, RepositoryID: repository.ID, TaskRepositoryLinkID: link.ID,
		RecoveryPointID: point.ID, AttemptID: attemptID, TaskID: taskEntity.ID, TaskRunID: taskRun.ID,
		Trigger: trigger, PublicationMode: runtime.binding.PublicationMode, ImportedBaseline: run.ImportedBaseline,
		CaptureStartedAt: startedAt, PreparedAt: preparedAt, PointDeadlineAt: deadline,
		ExpectedTaskRevision: taskRevision, BindingRevision: runtime.binding.BindingRevision,
		ConfigRevision: runtime.binding.ConfigRevision, ConfigDigest: configDigest,
		CapabilityRevision: runtime.binding.CapabilityRevision, CredentialRevision: runtime.binding.CredentialRevision,
		PreflightID: runtime.binding.PreflightID, PreflightRevision: runtime.binding.PreflightRevision,
		PreflightDigest: runtime.binding.PreflightDigest, ManifestSchemaRevision: managedRcloneManifestSchemaRevision,
		ManifestLimitsRevision: managedRcloneManifestLimitsRevision, ManifestLimitsDigest: limitsDigest,
		RepositoryIdentityDigest:   digestText(*repository.RepositoryIdentity),
		ManagedRootIdentityDigest:  runtime.binding.ManagedRootIdentityDigest,
		ChildFenceDigest:           rcloneChildFenceDigest(markerKey, childLease.Fence),
		LegacyOriginEvidenceDigest: legacyOriginDigest,
	}
	if runtime.binding.PublicationMode == backupasset.PublicationVersionedPrefix {
		attempt.Portable = &provider.RclonePortableAttemptV1{
			AttemptComponent: point.ID + "." + attemptID, DataComponent: "data", ControlComponent: "control",
			AttemptMarkerDigest:      rcloneAttemptMarkerDigest(markerKey, point.ID, attemptID),
			ExpectedConsistencyClass: string(backupasset.RcloneConsistencyObservationallyStable),
			ExpectedHashFidelity:     string(backupasset.RcloneHashDownloadVerifiedBytes),
		}
	} else if runtime.binding.PublicationMode == backupasset.PublicationNativeObjectVersions && nativeInput != nil && runtime.binding.Native != nil {
		native := runtime.binding.Native
		attempt.Native = &provider.RcloneNativeAttemptV1{
			ProfileCode: native.ProfileCode, RegionIdentityDigest: native.RegionIdentityDigest,
			BucketIdentityDigest: native.BucketIdentityDigest, ManagedPrefixIdentityDigest: native.ManagedPrefixIdentityDigest,
			RoleSessionIdentityDigest: nativeInput.session.IdentityDigest(), SessionExpiresAt: nativeInput.session.ExpiresAt(),
			VersioningDigest: native.VersioningDigest, LifecycleDigest: native.LifecycleDigest,
			CapabilityStableObservedAt: native.CapabilityStableObservedAt, EncryptionProfile: native.EncryptionProfile,
			BucketEncryptionDigest:   native.BucketEncryptionDigest,
			ActiveKeyDigest:          nativeInput.encryptionEvidence.ActiveKeyDigest,
			RetainedReadKeySetDigest: nativeInput.encryptionEvidence.ReadKeySetDigest,
			KMSCapabilityRevision:    native.KMSCapabilityRevision, B0VersionGraphDigest: nativeInput.b0.Digest,
			StartMarkerIdentityDigest: rcloneNativeControlIdentityDigest(markerKey, "start", point.ID, attemptID),
			CanaryIdentityDigest:      native.CanaryEncryptionEvidenceDigest,
		}
	}
	if err := attempt.Validate(); err != nil {
		return provider.RcloneAttemptV1{}, backupasset.Lease{}, err
	}
	preparedRecord, err := encodeManagedRclonePreparedAttemptRecord(attempt, attempt.ChildFenceDigest)
	if err != nil {
		return provider.RcloneAttemptV1{}, backupasset.Lease{}, err
	}
	point.EncryptedProviderLocator = preparedRecord
	if err := tx.Save(&point).Error; err != nil {
		return provider.RcloneAttemptV1{}, backupasset.Lease{}, fmt.Errorf("persist managed Rclone prepared attempt: %w", err)
	}
	return attempt, childLease, nil
}

func rclonePublicationLineageForRun(link model.TaskRepositoryLink, taskEntity model.Task, taskRun model.TaskRun, trigger, chainRunID string, startedAt, preparedAt, deadline time.Time) (backupasset.PublicationLineageV1, error) {
	mode := backupasset.TaskPublicationMode(link.PublicationMode)
	if mode != backupasset.PublicationVersionedPrefix && mode != backupasset.PublicationNativeObjectVersions {
		return backupasset.PublicationLineageV1{}, fmt.Errorf("%w: managed Rclone lineage mode is invalid", backupasset.ErrInvalidState)
	}
	chainDigest := ""
	if chainRunID != "" {
		sum := sha256.Sum256([]byte(chainRunID))
		chainDigest = hex.EncodeToString(sum[:])
	}
	return backupasset.PublicationLineageV1{
		Version: 1, TaskRepositoryLinkID: link.ID, TaskID: taskEntity.ID, TaskRunID: taskRun.ID,
		Trigger: trigger, ChainRunIDPresent: chainRunID != "", ChainRunIDDigest: chainDigest,
		PublicationMode: string(mode), PointCodecVersion: 1, TagCodecVersion: 0,
		StartedAt: startedAt.UTC(), PreparedAt: preparedAt.UTC(), PointDeadlineAt: deadline.UTC(),
	}, nil
}

func earlierRcloneDeadline(left, right time.Time) time.Time {
	if right.Before(left) {
		return right
	}
	return left
}

func managedRclonePointDeadline(
	preparedAt time.Time,
	binding managedRcloneBindingDocumentV3,
	leaseConfig backupasset.LeaseConfig,
	publicationConfig backupasset.PublicationConfig,
) time.Time {
	deadline := preparedAt.Add(leaseConfig.AbsoluteDeadline).UTC()
	modeDeadline := publicationConfig.Rclone.PortableDeadline
	if binding.PublicationMode == backupasset.PublicationNativeObjectVersions {
		modeDeadline = publicationConfig.Rclone.NativeDeadline
	}
	deadline = earlierRcloneDeadline(deadline, preparedAt.Add(modeDeadline).UTC())
	return earlierRcloneDeadline(deadline, binding.PreflightExpiresAt.UTC())
}

func rcloneImmutability(mode backupasset.TaskPublicationMode) backupasset.ImmutabilityLevel {
	if mode == backupasset.PublicationNativeObjectVersions {
		return backupasset.ImmutabilityBackendVersioned
	}
	return backupasset.ImmutabilityXirangManaged
}

func managedRcloneTaskRevision(taskEntity model.Task) (uint64, error) {
	if taskEntity.ID == 0 || taskEntity.UpdatedAt.IsZero() || taskEntity.UpdatedAt.UTC().UnixNano() <= 0 {
		return 0, fmt.Errorf("%w: managed Rclone Task revision unavailable", backupasset.ErrInvalidState)
	}
	return uint64(taskEntity.UpdatedAt.UTC().UnixNano()), nil
}

func rcloneManifestLimits(config backupasset.PublicationConfig) provider.ManifestLimits {
	return provider.ManifestLimits{
		Timeout: config.ManifestTimeout, MaxBytes: config.ManifestMaxBytes, MaxEntries: config.ManifestMaxEntries,
		MaxRecordBytes: config.ManifestMaxRecordBytes, MaxDepth: config.ManifestMaxDepth,
	}
}

func digestRcloneManifestLimits(limits provider.ManifestLimits) (string, error) {
	writer := backupasset.NewCanonicalSHA256()
	writer.String("xirang-rclone-manifest-limits-v1")
	writer.Int64(int64(limits.Timeout))
	writer.Int64(limits.MaxBytes)
	writer.Int64(limits.MaxEntries)
	writer.Int64(int64(limits.MaxRecordBytes))
	writer.Int64(int64(limits.MaxDepth))
	return writer.HexDigest()
}

func encodeManagedRclonePreparedAttemptRecord(attempt provider.RcloneAttemptV1, childFenceDigest string) (string, error) {
	if err := attempt.Validate(); err != nil || childFenceDigest != attempt.ChildFenceDigest {
		return "", fmt.Errorf("%w: invalid managed Rclone prepared attempt", backupasset.ErrInvalidState)
	}
	tagged, err := provider.EncodePublicationAttempt(provider.NewRclonePublicationAttempt(attempt))
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(managedRclonePreparedAttemptRecordV1{
		Version: managedRclonePreparedAttemptVersion, TaggedAttempt: tagged, ChildFenceDigest: childFenceDigest,
	})
	if err != nil || len(payload) > maxManagedRclonePreparedAttemptRecordBytes {
		return "", fmt.Errorf("%w: encode managed Rclone prepared attempt", backupasset.ErrInvalidState)
	}
	return string(payload), nil
}

func decodeManagedRclonePreparedAttemptRecord(payload string) (provider.RcloneAttemptV1, string, error) {
	if payload == "" || len(payload) > maxManagedRclonePreparedAttemptRecordBytes || rejectDuplicateOrNullJSONMembers(payload) != nil {
		return provider.RcloneAttemptV1{}, "", fmt.Errorf("%w: invalid managed Rclone prepared attempt", backupasset.ErrInvalidState)
	}
	var record managedRclonePreparedAttemptRecordV1
	decoder := json.NewDecoder(strings.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return provider.RcloneAttemptV1{}, "", fmt.Errorf("%w: invalid managed Rclone prepared attempt", backupasset.ErrInvalidState)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) || record.Version != managedRclonePreparedAttemptVersion ||
		!isLowerHex64(record.ChildFenceDigest) {
		return provider.RcloneAttemptV1{}, "", fmt.Errorf("%w: invalid managed Rclone prepared attempt", backupasset.ErrInvalidState)
	}
	attempt, err := provider.DecodeRcloneAttemptV1(record.TaggedAttempt)
	if err != nil || attempt.ChildFenceDigest != record.ChildFenceDigest {
		return provider.RcloneAttemptV1{}, "", fmt.Errorf("%w: managed Rclone prepared attempt mismatch", backupasset.ErrInvalidState)
	}
	return attempt, record.ChildFenceDigest, nil
}

func (service *PublicationService) recordRcloneProviderCommit(
	ctx context.Context,
	attempt provider.RcloneAttemptV1,
	binding managedRcloneBindingDocumentV3,
	markerKey []byte,
	childFence backupasset.LeaseFence,
	evidence provider.RcloneCommitV1,
) (publication.Outcome, bool, error) {
	if err := validateRcloneCommitEvidence(attempt, binding, markerKey, childFence, evidence); err != nil {
		return publication.Outcome{}, false, err
	}
	var outcome publication.Outcome
	transitioned := false
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var repository model.BackupRepository
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&repository, "id = ?", attempt.RepositoryID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: managed Rclone publication repository", backupasset.ErrNotFound)
			}
			return fmt.Errorf("lock managed Rclone provider commit repository: %w", err)
		}
		var point model.RecoveryPoint
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&point, "id = ?", attempt.RecoveryPointID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: managed Rclone publication point", backupasset.ErrNotFound)
			}
			return fmt.Errorf("lock managed Rclone provider commit point: %w", err)
		}
		if repository.CapabilityRevision <= 0 || uint64(repository.CapabilityRevision) != attempt.CapabilityRevision ||
			point.CapabilityRevision <= 0 || uint64(point.CapabilityRevision) != attempt.CapabilityRevision {
			return fmt.Errorf("%w: managed Rclone provider commit capability revision changed", backupasset.ErrConflict)
		}
		if point.RepositoryID != attempt.RepositoryID || point.ProducingTaskID == nil || *point.ProducingTaskID != attempt.TaskID ||
			point.ProducingTaskRunID == nil || *point.ProducingTaskRunID != attempt.TaskRunID {
			return fmt.Errorf("%w: managed Rclone provider commit point lineage mismatch", backupasset.ErrConflict)
		}
		lineage, err := backupasset.DecodePublicationLineage(point.LineageJSON)
		if err != nil {
			return err
		}
		if lineage.TaskRepositoryLinkID != attempt.TaskRepositoryLinkID || lineage.TaskID != attempt.TaskID || lineage.TaskRunID != attempt.TaskRunID ||
			lineage.PublicationMode != string(attempt.PublicationMode) || !lineage.PointDeadlineAt.Equal(attempt.PointDeadlineAt.UTC()) {
			return fmt.Errorf("%w: managed Rclone provider commit immutable lineage mismatch", backupasset.ErrConflict)
		}
		if point.State == string(backupasset.RecoveryPointVerifying) || point.State == string(backupasset.RecoveryPointCommitted) {
			matching, replay, err := rcloneProviderCommitReplayMatches(point, attempt, evidence)
			if err != nil {
				return err
			}
			if !matching {
				return fmt.Errorf("%w: managed Rclone provider commit replay differs", backupasset.ErrConflict)
			}
			if evidence.Native != nil {
				locator, locatorErr := decodeManagedRclonePointLocator(point.EncryptedProviderLocator)
				if locatorErr != nil {
					return locatorErr
				}
				_, incomingLocator, locatorErr := encodeManagedRclonePointLocator(attempt, binding, markerKey, evidence)
				if locatorErr != nil {
					return fmt.Errorf("%w: managed Rclone native replay exact evidence is invalid: %v", backupasset.ErrConflict, locatorErr)
				}
				if locator.FrozenNativeVersionCount != incomingLocator.FrozenNativeVersionCount ||
					locator.FrozenNativeVersionsDigest != incomingLocator.FrozenNativeVersionsDigest ||
					locator.FrozenNativeReferenceCount != incomingLocator.FrozenNativeReferenceCount ||
					locator.FrozenNativeReferencesDigest != incomingLocator.FrozenNativeReferencesDigest ||
					locator.PhysicalIdentityDigest != incomingLocator.PhysicalIdentityDigest {
					return fmt.Errorf("%w: managed Rclone native replay exact evidence differs", backupasset.ErrConflict)
				}
				if _, _, locatorErr = loadManagedRcloneNativeVersionEvidenceTx(
					ctx, tx, repository.ID, point.ID, markerKey, locator,
					managedRcloneNativeControlCommitKey(binding, attempt),
				); locatorErr != nil {
					return locatorErr
				}
			}
			outcome = replay
			return nil
		}
		if point.State != string(backupasset.RecoveryPointPreparing) {
			return fmt.Errorf("%w: managed Rclone provider commit point is not preparing", backupasset.ErrConflict)
		}
		if evidence.Native != nil {
			if err := rejectManagedRcloneNativeDeletionReservationTx(ctx, tx, repository.ID); err != nil {
				return err
			}
		}
		if err := service.lease.ValidateFenceTx(ctx, tx, childFence); err != nil {
			return err
		}
		locatorPayload, locator, err := encodeManagedRclonePointLocator(attempt, binding, markerKey, evidence)
		if err != nil {
			return err
		}
		fidelityPayload, err := json.Marshal(struct {
			Version int    `json:"version"`
			Digest  string `json:"digest"`
		}{Version: managedRcloneCommitEvidenceVersion, Digest: evidence.FidelityEvidenceDigest})
		if err != nil {
			return fmt.Errorf("encode managed Rclone fidelity evidence: %w", err)
		}
		consistency := backupasset.PublicationConsistencyV1{
			Version: 1, Provider: backupasset.ProviderRclone, RepositoryIdentityDigest: attempt.RepositoryIdentityDigest,
			ProviderCommitDigest: locator.ProviderCommitDigest, CapabilityRevision: point.CapabilityRevision,
		}
		if err := validateRcloneDurableCapabilityEvidence(point, consistency, attempt, evidence); err != nil {
			return err
		}
		encodedConsistency, err := backupasset.EncodePublicationConsistency(consistency)
		if err != nil {
			return err
		}
		if err := backupasset.ValidateRecoveryPointTransition(rclonePointProfile(point, attempt.PublicationMode), backupasset.RecoveryPointVerifying); err != nil {
			return err
		}
		if evidence.ManifestEntryCount > math.MaxInt64 || evidence.LogicalBytes > math.MaxInt64 {
			return fmt.Errorf("%w: managed Rclone provider commit exceeds model bounds", backupasset.ErrInvalidState)
		}
		capturedAt := evidence.ProviderCommittedAt.UTC()
		point.EncryptedProviderLocator = locatorPayload
		point.SourceFingerprint = locator.PhysicalIdentityDigest
		point.ManifestDigestAlgorithm = "sha256"
		point.ManifestDigest = evidence.ManifestIndexDigest
		point.EntryCount = int64(evidence.ManifestEntryCount)
		point.LogicalBytes = int64(evidence.LogicalBytes)
		point.FidelityJSON = string(fidelityPayload)
		point.ConsistencyJSON = encodedConsistency
		point.CapturedAt = &capturedAt
		point.State = string(backupasset.RecoveryPointVerifying)
		point.UpdatedAt = service.now().UTC()
		if err := tx.Save(&point).Error; err != nil {
			if isPublicationManagedTreeSourceConflict(err) {
				return fmt.Errorf("%w: managed Rclone point identity is already claimed", backupasset.ErrConflict)
			}
			return fmt.Errorf("save managed Rclone provider commit point: %w", err)
		}
		if evidence.Native != nil {
			ownedDigest, referenceDigest, evidenceErr := persistManagedRcloneNativeVersionEvidenceTx(
				ctx, tx, repository.ID, point.ID, markerKey, evidence.Native, point.UpdatedAt,
			)
			if evidenceErr != nil {
				return evidenceErr
			}
			if locator.FrozenNativeVersionsDigest != ownedDigest || locator.FrozenNativeReferencesDigest != referenceDigest ||
				locator.FrozenNativeVersionCount != uint64(len(evidence.Native.FrozenNativeVersions)) ||
				locator.FrozenNativeReferenceCount != uint64(len(evidence.Native.FrozenNativeReferences)) {
				return lifecycleDeleteIdentityConflict("managed Rclone native locator and durable evidence differ")
			}
		}
		if err := upsertManagedRcloneHistoryLatchesTx(ctx, tx, repository, point, capturedAt, service.now().UTC()); err != nil {
			return err
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

func validateRcloneCommitEvidence(
	attempt provider.RcloneAttemptV1,
	binding managedRcloneBindingDocumentV3,
	markerKey []byte,
	childFence backupasset.LeaseFence,
	evidence provider.RcloneCommitV1,
) error {
	if err := attempt.Validate(); err != nil {
		return err
	}
	if err := validateManagedRcloneBindingDocumentV3(binding); err != nil {
		return err
	}
	if err := validateManagedRcloneNativeControlIdentity(attempt, binding, evidence.Native); err != nil {
		return err
	}
	if err := evidence.Validate(); err != nil {
		return err
	}
	if binding.RepositoryID != attempt.RepositoryID || binding.TaskRepositoryLinkID != attempt.TaskRepositoryLinkID ||
		binding.TaskID != attempt.TaskID || binding.PublicationMode != attempt.PublicationMode ||
		evidence.RepositoryID != attempt.RepositoryID || evidence.TaskRepositoryLinkID != attempt.TaskRepositoryLinkID ||
		evidence.RecoveryPointID != attempt.RecoveryPointID || evidence.AttemptID != attempt.AttemptID ||
		evidence.PublicationMode != attempt.PublicationMode || !evidence.PointDeadlineAt.Equal(attempt.PointDeadlineAt.UTC()) ||
		evidence.ProviderCommittedAt.After(attempt.PointDeadlineAt) || evidence.ChildFenceDigest != rcloneChildFenceDigest(markerKey, childFence) ||
		evidence.ChildFenceDigest != attempt.ChildFenceDigest || evidence.CapabilityEvidenceDigest != attempt.PreflightDigest ||
		(evidence.Native != nil && evidence.Native.CapabilityRevision != attempt.CapabilityRevision) {
		return fmt.Errorf("%w: managed Rclone provider commit evidence mismatch", backupasset.ErrConflict)
	}
	switch attempt.PublicationMode {
	case backupasset.PublicationVersionedPrefix:
		if attempt.Portable == nil || evidence.Portable == nil || evidence.Native != nil ||
			evidence.Portable.AttemptMarkerDigest != attempt.Portable.AttemptMarkerDigest {
			return fmt.Errorf("%w: managed Rclone portable commit evidence mismatch", backupasset.ErrConflict)
		}
	case backupasset.PublicationNativeObjectVersions:
		if attempt.Native == nil || evidence.Native == nil || evidence.Portable != nil {
			return fmt.Errorf("%w: managed Rclone native commit evidence mismatch", backupasset.ErrConflict)
		}
	default:
		return fmt.Errorf("%w: unsupported managed Rclone commit publication mode", backupasset.ErrInvalidState)
	}
	return nil
}

func encodeManagedRclonePointLocator(
	attempt provider.RcloneAttemptV1,
	binding managedRcloneBindingDocumentV3,
	markerKey []byte,
	evidence provider.RcloneCommitV1,
) (string, managedRclonePointLocatorV1, error) {
	taggedAttempt, err := provider.EncodePublicationAttempt(provider.NewRclonePublicationAttempt(attempt))
	if err != nil {
		return "", managedRclonePointLocatorV1{}, err
	}
	taggedCommit, err := provider.EncodeProviderCommit(provider.NewRcloneProviderCommit(evidence))
	if err != nil {
		return "", managedRclonePointLocatorV1{}, err
	}
	commitDigest := digestText(taggedCommit)
	locator := managedRclonePointLocatorV1{
		Version: managedRclonePointLocatorLegacyVersion, Provider: backupasset.ProviderRclone,
		RepositoryID: attempt.RepositoryID, RecoveryPointID: attempt.RecoveryPointID, AttemptID: attempt.AttemptID,
		PublicationMode: attempt.PublicationMode, TaggedAttempt: taggedAttempt, TaggedCommit: taggedCommit,
		ChildFenceDigest: attempt.ChildFenceDigest, ProviderCommitDigest: commitDigest,
	}
	switch attempt.PublicationMode {
	case backupasset.PublicationVersionedPrefix:
		if evidence.Portable == nil {
			return "", managedRclonePointLocatorV1{}, fmt.Errorf("%w: portable Rclone provider commit evidence is missing", backupasset.ErrInvalidState)
		}
		locator.CommitPayloadDigest = evidence.Portable.CommitPayloadDigest
		locator.ManifestControlIdentity = evidence.Portable.ControlIdentityDigest
		locator.PortableAttemptRoot = managedRclonePortableAttemptRoot(binding, attempt)
		locator.PhysicalIdentityDigest = hex.EncodeToString(rcloneOwnershipDigest(markerKey,
			"xirang.rclone.portable-point-identity.v1", attempt.RepositoryID, locator.PortableAttemptRoot,
			evidence.Portable.CommitComponent, evidence.Portable.CommitPayloadDigest))
	case backupasset.PublicationNativeObjectVersions:
		if evidence.Native == nil {
			return "", managedRclonePointLocatorV1{}, fmt.Errorf("%w: native Rclone provider commit evidence is missing", backupasset.ErrInvalidState)
		}
		locator.Version = managedRclonePointLocatorVersion
		locator.CommitPayloadDigest = evidence.Native.CommitContentDigest
		locator.ManifestControlIdentity = evidence.Native.ManifestControlGraphDigest
		_, ownedDigest, referenceDigest, evidenceErr := buildManagedRcloneNativeVersionEvidenceRows(
			attempt.RepositoryID, attempt.RecoveryPointID, markerKey, evidence.Native, time.Time{},
		)
		if evidenceErr != nil {
			return "", managedRclonePointLocatorV1{}, evidenceErr
		}
		locator.FrozenNativeVersionCount = uint64(len(evidence.Native.FrozenNativeVersions))
		locator.FrozenNativeVersionsDigest = ownedDigest
		locator.FrozenNativeReferenceCount = uint64(len(evidence.Native.FrozenNativeReferences))
		locator.FrozenNativeReferencesDigest = referenceDigest
		locator.PhysicalIdentityDigest = managedRcloneNativePointIdentityDigest(
			markerKey, attempt.RepositoryID, evidence.Native.CommitContentDigest,
			locator.FrozenNativeVersionCount, locator.FrozenNativeVersionsDigest,
			locator.FrozenNativeReferenceCount, locator.FrozenNativeReferencesDigest,
		)
	default:
		return "", managedRclonePointLocatorV1{}, fmt.Errorf("%w: unsupported managed Rclone point locator", backupasset.ErrInvalidState)
	}
	if err := validateManagedRclonePointLocator(locator); err != nil {
		return "", managedRclonePointLocatorV1{}, err
	}
	var payload []byte
	if attempt.PublicationMode == backupasset.PublicationVersionedPrefix {
		payload, err = json.Marshal(managedRclonePortablePointLocatorV1Wire{
			Version: locator.Version, Provider: locator.Provider, RepositoryID: locator.RepositoryID,
			RecoveryPointID: locator.RecoveryPointID, AttemptID: locator.AttemptID,
			PublicationMode: locator.PublicationMode, TaggedAttempt: locator.TaggedAttempt,
			TaggedCommit: locator.TaggedCommit, ChildFenceDigest: locator.ChildFenceDigest,
			CommitPayloadDigest: locator.CommitPayloadDigest, PortableAttemptRoot: locator.PortableAttemptRoot,
			PhysicalIdentityDigest: locator.PhysicalIdentityDigest, ProviderCommitDigest: locator.ProviderCommitDigest,
			ManifestControlIdentity: locator.ManifestControlIdentity,
		})
	} else {
		payload, err = json.Marshal(locator)
	}
	if err != nil {
		return "", managedRclonePointLocatorV1{}, fmt.Errorf("encode managed Rclone point locator: %w", err)
	}
	if len(payload) > maxManagedRclonePreparedAttemptRecordBytes {
		return "", managedRclonePointLocatorV1{}, fmt.Errorf("%w: managed Rclone point locator exceeds %d bytes", backupasset.ErrInvalidState, maxManagedRclonePreparedAttemptRecordBytes)
	}
	return string(payload), locator, nil
}

func decodeManagedRclonePointLocator(payload string) (managedRclonePointLocatorV1, error) {
	if payload == "" || len(payload) > maxManagedRclonePreparedAttemptRecordBytes || rejectDuplicateOrNullJSONMembers(payload) != nil {
		return managedRclonePointLocatorV1{}, fmt.Errorf("%w: invalid managed Rclone point locator", backupasset.ErrInvalidState)
	}
	var locator managedRclonePointLocatorV1
	decoder := json.NewDecoder(strings.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&locator); err != nil {
		return managedRclonePointLocatorV1{}, fmt.Errorf("%w: invalid managed Rclone point locator", backupasset.ErrInvalidState)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return managedRclonePointLocatorV1{}, fmt.Errorf("%w: trailing managed Rclone point locator", backupasset.ErrInvalidState)
	}
	if err := validateManagedRclonePointLocator(locator); err != nil {
		return managedRclonePointLocatorV1{}, err
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal([]byte(payload), &members); err != nil {
		return managedRclonePointLocatorV1{}, fmt.Errorf("%w: invalid managed Rclone point locator members", backupasset.ErrInvalidState)
	}
	if err := validateManagedRclonePointLocatorWireMembers(members, locator); err != nil {
		return managedRclonePointLocatorV1{}, err
	}
	return locator, nil
}

func validateManagedRclonePointLocatorWireMembers(
	members map[string]json.RawMessage,
	locator managedRclonePointLocatorV1,
) error {
	reject := func(member string, version int) error {
		return fmt.Errorf("%w: managed Rclone point locator version %d contains forbidden member %q",
			backupasset.ErrInvalidState, version, member)
	}
	switch locator.Version {
	case managedRclonePointLocatorLegacyVersion:
		for _, member := range []string{
			"frozen_native_version_count", "frozen_native_versions_digest",
			"frozen_native_reference_count", "frozen_native_references_digest",
		} {
			if _, present := members[member]; present {
				return reject(member, locator.Version)
			}
		}
		switch locator.PublicationMode {
		case backupasset.PublicationVersionedPrefix:
			for _, member := range []string{
				"native_commit_key", "native_commit_version_id", "frozen_native_versions",
			} {
				if _, present := members[member]; present {
					return reject(member, locator.Version)
				}
			}
		case backupasset.PublicationNativeObjectVersions:
			if _, present := members["portable_attempt_root"]; present {
				return reject("portable_attempt_root", locator.Version)
			}
		}
	case managedRclonePointLocatorVersion:
		for _, member := range []string{
			"portable_attempt_root", "native_commit_key", "native_commit_version_id", "frozen_native_versions",
		} {
			if _, present := members[member]; present {
				return reject(member, locator.Version)
			}
		}
	}
	return nil
}

func validateManagedRclonePointLocator(locator managedRclonePointLocatorV1) error {
	if (locator.Version != managedRclonePointLocatorLegacyVersion && locator.Version != managedRclonePointLocatorVersion) ||
		locator.Provider != backupasset.ProviderRclone ||
		backupasset.ValidateOpaqueID(locator.RepositoryID) != nil || backupasset.ValidateOpaqueID(locator.RecoveryPointID) != nil ||
		backupasset.ValidateOpaqueID(locator.AttemptID) != nil || !isLowerHex64(locator.ChildFenceDigest) ||
		!isLowerHex64(locator.CommitPayloadDigest) || !isLowerHex64(locator.PhysicalIdentityDigest) ||
		!isLowerHex64(locator.ProviderCommitDigest) || !isLowerHex64(locator.ManifestControlIdentity) {
		return fmt.Errorf("%w: invalid managed Rclone point locator", backupasset.ErrInvalidState)
	}
	attempt, err := provider.DecodeRcloneAttemptV1(locator.TaggedAttempt)
	if err != nil || attempt.RepositoryID != locator.RepositoryID || attempt.RecoveryPointID != locator.RecoveryPointID ||
		attempt.AttemptID != locator.AttemptID || attempt.PublicationMode != locator.PublicationMode ||
		attempt.ChildFenceDigest != locator.ChildFenceDigest {
		return fmt.Errorf("%w: managed Rclone point locator attempt mismatch", backupasset.ErrInvalidState)
	}
	taggedCommit, err := provider.DecodeProviderCommit(locator.TaggedCommit)
	if err != nil || digestText(locator.TaggedCommit) != locator.ProviderCommitDigest {
		return fmt.Errorf("%w: managed Rclone point locator commit mismatch", backupasset.ErrInvalidState)
	}
	commit, err := taggedCommit.RcloneCommit()
	if err != nil || commit.RepositoryID != locator.RepositoryID || commit.RecoveryPointID != locator.RecoveryPointID ||
		commit.AttemptID != locator.AttemptID || commit.PublicationMode != locator.PublicationMode ||
		commit.ChildFenceDigest != locator.ChildFenceDigest {
		return fmt.Errorf("%w: managed Rclone point locator commit mismatch", backupasset.ErrInvalidState)
	}
	switch locator.PublicationMode {
	case backupasset.PublicationVersionedPrefix:
		if locator.Version != managedRclonePointLocatorLegacyVersion ||
			locator.NativeCommitKey != "" || locator.NativeCommitVersionID != "" ||
			len(locator.FrozenNativeVersions) != 0 || locator.FrozenNativeVersionCount != 0 ||
			locator.FrozenNativeVersionsDigest != "" || locator.FrozenNativeReferenceCount != 0 ||
			locator.FrozenNativeReferencesDigest != "" || locator.PortableAttemptRoot == "" ||
			commit.Native != nil || commit.Portable == nil || attempt.Portable == nil {
			return fmt.Errorf("%w: invalid managed Rclone portable point locator", backupasset.ErrInvalidState)
		}
		if _, err := provider.NewRclonePrivateLocator(locator.PortableAttemptRoot); err != nil {
			return fmt.Errorf("%w: invalid managed Rclone portable point locator root", backupasset.ErrInvalidState)
		}
		if attempt.Portable.AttemptMarkerDigest != commit.Portable.AttemptMarkerDigest ||
			commit.Portable.CommitPayloadDigest != locator.CommitPayloadDigest ||
			commit.Portable.ControlIdentityDigest != locator.ManifestControlIdentity {
			return fmt.Errorf("%w: managed Rclone portable point locator evidence mismatch", backupasset.ErrInvalidState)
		}
	case backupasset.PublicationNativeObjectVersions:
		if locator.PortableAttemptRoot != "" || commit.Portable != nil || commit.Native == nil || attempt.Native == nil ||
			commit.Native.CommitKey != "" || commit.Native.CommitVersionID != "" {
			return fmt.Errorf("%w: invalid managed Rclone native point locator", backupasset.ErrInvalidState)
		}
		if locator.Version == managedRclonePointLocatorLegacyVersion {
			if locator.FrozenNativeVersionCount != 0 || locator.FrozenNativeVersionsDigest != "" ||
				locator.FrozenNativeReferenceCount != 0 || locator.FrozenNativeReferencesDigest != "" {
				return fmt.Errorf("%w: invalid managed Rclone legacy native point locator", backupasset.ErrInvalidState)
			}
			if _, err := managedRcloneLegacyNativeVersions(locator); err != nil {
				return err
			}
		} else if locator.NativeCommitKey != "" || locator.NativeCommitVersionID != "" ||
			len(locator.FrozenNativeVersions) != 0 || locator.FrozenNativeVersionCount == 0 ||
			!isLowerHex64(locator.FrozenNativeVersionsDigest) ||
			!isLowerHex64(locator.FrozenNativeReferencesDigest) {
			return fmt.Errorf("%w: invalid managed Rclone native point locator", backupasset.ErrInvalidState)
		}
		if commit.Native.CommitContentDigest != locator.CommitPayloadDigest ||
			commit.Native.ManifestControlGraphDigest != locator.ManifestControlIdentity {
			return fmt.Errorf("%w: managed Rclone native point locator evidence mismatch", backupasset.ErrInvalidState)
		}
	default:
		return fmt.Errorf("%w: invalid managed Rclone point locator mode", backupasset.ErrInvalidState)
	}
	return nil
}

func managedRclonePortableAttemptRoot(binding managedRcloneBindingDocumentV3, attempt provider.RcloneAttemptV1) string {
	if binding.Portable == nil || attempt.Portable == nil {
		return ""
	}
	return strings.TrimSuffix(binding.Portable.ManagedRootLocator, "/") + "/points/" + attempt.Portable.AttemptComponent
}

func canonicalRcloneProviderCommitDigest(evidence provider.RcloneCommitV1) (string, error) {
	encoded, err := provider.EncodeProviderCommit(provider.NewRcloneProviderCommit(evidence))
	if err != nil {
		return "", err
	}
	return digestText(encoded), nil
}

func rcloneProviderCommitReplayMatches(point model.RecoveryPoint, attempt provider.RcloneAttemptV1, evidence provider.RcloneCommitV1) (bool, publication.Outcome, error) {
	consistency, err := backupasset.DecodePublicationConsistency(point.ConsistencyJSON)
	if err != nil {
		return false, publication.Outcome{}, err
	}
	if err := validateRcloneDurableCapabilityEvidence(point, consistency, attempt, evidence); err != nil {
		return false, publication.Outcome{}, err
	}
	digest, err := canonicalRcloneProviderCommitDigest(evidence)
	if err != nil {
		return false, publication.Outcome{}, err
	}
	matching := consistency.Provider == backupasset.ProviderRclone && consistency.ProviderCommitDigest == digest &&
		point.ManifestDigest == evidence.ManifestIndexDigest && point.EntryCount == int64(evidence.ManifestEntryCount) &&
		point.LogicalBytes == int64(evidence.LogicalBytes)
	capturedAt := time.Time{}
	if point.CapturedAt != nil {
		capturedAt = point.CapturedAt.UTC()
	}
	return matching, publication.Outcome{
		RepositoryID: point.RepositoryID, RecoveryPointID: point.ID, TaskID: attempt.TaskID, TaskRunID: attempt.TaskRunID,
		State: backupasset.RecoveryPointState(point.State), CapturedAt: capturedAt, ProviderCommitRecorded: true,
	}, nil
}

func rclonePointProfile(point model.RecoveryPoint, mode backupasset.TaskPublicationMode) backupasset.RecoveryPointProfile {
	version, semantics, _, err := backupasset.MapPublicationMode(backupasset.ProviderRclone, mode)
	if err != nil {
		return backupasset.RecoveryPointProfile{}
	}
	if point.Semantics == string(backupasset.PointImportedBaseline) {
		semantics = backupasset.PointImportedBaseline
	}
	return backupasset.RecoveryPointProfile{
		VersionMode: version, Semantics: semantics, State: backupasset.RecoveryPointState(point.State),
		Immutability: backupasset.ImmutabilityLevel(point.ImmutabilityLevel),
		Availability: backupasset.PhysicalAvailability(point.PhysicalAvailability), Hold: backupasset.HoldState(point.HoldState),
	}
}

func upsertManagedRcloneHistoryLatchesTx(ctx context.Context, tx *gorm.DB, repository model.BackupRepository, point model.RecoveryPoint, firstSeenAt, now time.Time) error {
	if tx == nil || backupasset.ValidateOpaqueID(repository.ID) != nil || repository.RepositoryIdentity == nil ||
		!isLowerHex64(point.SourceFingerprint) || (point.Semantics != string(backupasset.PointXirangManifest) && point.Semantics != string(backupasset.PointImportedBaseline)) {
		return fmt.Errorf("%w: invalid managed Rclone history latch input", backupasset.ErrInvalidState)
	}
	identityDigest := digestText(*repository.RepositoryIdentity)
	rows := []model.BackupAssetManagedHistoryLatch{
		{
			ID: "managed-history-installation", Scope: managedHistoryLatchScopeInstallation,
			FirstSemantics: point.Semantics, FirstOrigin: "rclone_provider_commit_v1",
			FirstSeenAt: firstSeenAt.UTC(), CreatedAt: now.UTC(), UpdatedAt: now.UTC(),
		},
		{
			ID: "managed-history-repository-" + repository.ID, Scope: managedHistoryLatchScopeRepository,
			RepositoryID: &repository.ID, RepositoryIdentityDigest: identityDigest,
			FirstSemantics: point.Semantics, FirstOrigin: "rclone_provider_commit_v1",
			FirstSeenAt: firstSeenAt.UTC(), CreatedAt: now.UTC(), UpdatedAt: now.UTC(),
		},
	}
	for _, row := range rows {
		if err := tx.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
			return fmt.Errorf("upsert managed Rclone history latch: %w", err)
		}
	}
	return nil
}

func (service *PublicationService) rcloneMarkerKey(ctx context.Context, repositoryID string) ([]byte, error) {
	if service == nil || service.keyring == nil || backupasset.ValidateOpaqueID(repositoryID) != nil {
		return nil, fmt.Errorf("%w: managed Rclone marker key is unavailable", backupasset.ErrInvalidState)
	}
	material, err := service.keyring.Ensure(ctx, backupasset.KeyDomainRecoveryCleanupOwnership)
	if err != nil {
		return nil, err
	}
	return rcloneOwnershipDigest(material.Key, "xirang.rclone.marker-key.v1", repositoryID), nil
}

func (service *PublicationService) rcloneMarkerKeyTx(ctx context.Context, tx *gorm.DB, repositoryID string) ([]byte, error) {
	if service == nil || service.keyring == nil || tx == nil || backupasset.ValidateOpaqueID(repositoryID) != nil {
		return nil, fmt.Errorf("%w: managed Rclone marker key is unavailable", backupasset.ErrInvalidState)
	}
	material, err := service.keyring.ActiveTx(ctx, tx, backupasset.KeyDomainRecoveryCleanupOwnership)
	if err != nil {
		return nil, err
	}
	return rcloneOwnershipDigest(material.Key, "xirang.rclone.marker-key.v1", repositoryID), nil
}

func rcloneChildFenceDigest(markerKey []byte, fence backupasset.LeaseFence) string {
	if len(markerKey) == 0 || backupasset.ValidateOpaqueID(fence.LeaseID) != nil || backupasset.ValidateOpaqueID(fence.RecoveryPointID) != nil ||
		backupasset.ValidateOpaqueID(fence.AttemptID) != nil || !isLowerHex64(fence.FenceToken) {
		return ""
	}
	return hex.EncodeToString(rcloneOwnershipDigest(markerKey, "xirang.rclone.child-fence.v1",
		fence.LeaseID, fence.RecoveryPointID, string(fence.HolderType), fence.OwnerID, fence.AttemptID, fence.FenceToken))
}

func rcloneAttemptMarkerDigest(markerKey []byte, pointID, attemptID string) string {
	if len(markerKey) == 0 || backupasset.ValidateOpaqueID(pointID) != nil || backupasset.ValidateOpaqueID(attemptID) != nil {
		return ""
	}
	return hex.EncodeToString(rcloneOwnershipDigest(markerKey, "xirang.rclone.attempt-marker.v1", pointID, attemptID))
}

func rcloneNativeControlIdentityDigest(markerKey []byte, phase, pointID, attemptID string) string {
	if len(markerKey) == 0 || (phase != "start" && phase != "end") ||
		backupasset.ValidateOpaqueID(pointID) != nil || backupasset.ValidateOpaqueID(attemptID) != nil {
		return ""
	}
	return hex.EncodeToString(rcloneOwnershipDigest(
		markerKey, "xirang.rclone.native-control-identity.v1", phase, pointID, attemptID,
	))
}

func rcloneOwnershipDigest(key []byte, domain string, values ...string) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = io.WriteString(mac, domain)
	for _, value := range values {
		_, _ = mac.Write([]byte{0})
		_, _ = io.WriteString(mac, value)
	}
	return mac.Sum(nil)
}

func newRclonePublicationExecution(
	service *PublicationService,
	token publication.AdmissionToken,
	attempt provider.RcloneAttemptV1,
	audit backupasset.PublicationAuditContext,
	binding managedRcloneBindingDocumentV3,
	taskEntity model.Task,
	markerKey []byte,
	childFence backupasset.LeaseFence,
	nativeInput *managedRcloneNativeProcessInput,
	config backupasset.LeaseConfig,
	parent context.Context,
) *rclonePublicationExecution {
	if parent == nil {
		parent = context.Background()
	}
	bounded, deadlineCancel := context.WithDeadline(parent, attempt.PointDeadlineAt.UTC())
	commandContext, cancel := context.WithCancelCause(bounded)
	execution := &rclonePublicationExecution{
		service: service, token: token, attempt: attempt, audit: audit, binding: binding, task: taskEntity,
		markerKey: append([]byte(nil), markerKey...), childFence: childFence, nativeInput: nativeInput,
		context: commandContext, cancel: cancel, deadlineCancel: deadlineCancel, heartbeat: make(chan struct{}),
	}
	execution.heartbeatW.Add(1)
	go execution.runHeartbeat(config)
	return execution
}

func (*rclonePublicationExecution) Mode() publication.ExecutionMode { return publication.ModeEvidence }

func (execution *rclonePublicationExecution) Attempt() *provider.TaggedPublicationAttempt {
	if execution == nil {
		return nil
	}
	value := provider.NewRclonePublicationAttempt(execution.attempt)
	return &value
}

func (execution *rclonePublicationExecution) Context() context.Context {
	if execution == nil || execution.context == nil {
		return context.Background()
	}
	return execution.context
}

func (execution *rclonePublicationExecution) RclonePublicationInput() (provider.RclonePublicationInput, error) {
	if execution == nil || execution.service == nil || execution.context == nil || execution.service.foundation == nil {
		return provider.RclonePublicationInput{}, fmt.Errorf("%w: managed Rclone provider input is unavailable", backupasset.ErrInvalidState)
	}
	if err := execution.context.Err(); err != nil {
		return provider.RclonePublicationInput{}, err
	}
	if err := execution.attempt.Validate(); err != nil {
		return provider.RclonePublicationInput{}, err
	}
	config, err := execution.service.foundation.PublicationConfig()
	if err != nil {
		return provider.RclonePublicationInput{}, err
	}
	limits := rcloneManifestLimits(config)
	input := provider.RclonePublicationInput{ManifestLimits: limits}
	if config.Rclone.ManifestChunkMaxBytes <= 0 || config.Rclone.ManifestChunkMaxBytes > math.MaxInt ||
		config.Rclone.ControlPayloadMaxBytes <= 0 || config.Rclone.FullVerifyMaxBytes <= 0 {
		return provider.RclonePublicationInput{}, fmt.Errorf("%w: managed Rclone limits are invalid", backupasset.ErrInvalidState)
	}
	chunkMaxEntries := int64(10000)
	if limits.MaxEntries < chunkMaxEntries {
		chunkMaxEntries = limits.MaxEntries
	}
	if chunkMaxEntries <= 0 {
		return provider.RclonePublicationInput{}, fmt.Errorf("%w: managed Rclone manifest entry limit is invalid", backupasset.ErrInvalidState)
	}
	manifestOptions := provider.RcloneManifestBuildOptions{
		Limits: limits, ChunkMaxBytes: int(config.Rclone.ManifestChunkMaxBytes), ChunkMaxEntries: int(chunkMaxEntries),
		SpoolMaxBytes: limits.MaxBytes,
	}
	if execution.attempt.PublicationMode == backupasset.PublicationNativeObjectVersions {
		if execution.binding.Native == nil || execution.nativeInput == nil ||
			execution.nativeInput.factory == nil || len(execution.nativeInput.rcloneConfig) == 0 {
			return provider.RclonePublicationInput{}, fmt.Errorf("%w: managed Rclone native input is unavailable", backupasset.ErrCapabilityUnavailable)
		}
		input.NativeRequest = &provider.RcloneNativePublicationRequest{
			Attempt: execution.attempt, Profile: execution.nativeInput.profile, Session: execution.nativeInput.session,
			ClientFactory: execution.nativeInput.factory, RcloneConfig: append([]byte(nil), execution.nativeInput.rcloneConfig...),
			Runtime: provider.RemoteCommandAccess{Node: execution.task.Node}, ManifestOptions: manifestOptions,
			ObservationLimits: execution.nativeInput.observationLimits, Encryption: execution.nativeInput.encryption,
			EncryptionEvidence: execution.nativeInput.encryptionEvidence,
			KMSKeyBindings:     append([]provider.RcloneNativeKMSKeyDigestBinding(nil), execution.nativeInput.keyBindings...),
			MarkerKey:          append([]byte(nil), execution.markerKey...), CapabilityEvidenceDigest: execution.binding.PreflightDigest,
			CostEvidenceDigest: digestRclonePublicationCost(config.Rclone), MaxVerifyBytes: uint64(config.Rclone.FullVerifyMaxBytes),
			ControlPayloadMaxBytes: uint64(config.Rclone.ControlPayloadMaxBytes), LowLevelRetries: config.Rclone.LowLevelRetries,
		}
		return input, nil
	}
	if execution.attempt.PublicationMode != backupasset.PublicationVersionedPrefix || execution.binding.Portable == nil {
		return provider.RclonePublicationInput{}, fmt.Errorf("%w: managed Rclone publication mode is unsupported", backupasset.ErrCapabilityUnavailable)
	}
	salt, err := hexDecodeSalt(execution.binding.IdentitySalt)
	if err != nil {
		return provider.RclonePublicationInput{}, err
	}
	bound, err := provider.ValidateRcloneBoundConfigV1744(
		[]byte(execution.binding.Portable.BoundConfig), execution.binding.Portable.TargetRemote, salt, config.Rclone.BoundConfigMaxBytes,
	)
	if err != nil || bound.KeyedDigest() != execution.binding.Portable.ConfigDigest {
		return provider.RclonePublicationInput{}, fmt.Errorf("%w: managed Rclone bound config drift", backupasset.ErrConflict)
	}
	attemptRootValue := strings.TrimSuffix(execution.binding.Portable.ManagedRootLocator, "/") + "/points/" + execution.attempt.Portable.AttemptComponent
	attemptRoot, err := provider.NewRclonePrivateLocator(attemptRootValue)
	if err != nil {
		return provider.RclonePublicationInput{}, err
	}
	dataRoot, err := provider.NewRclonePrivateLocator(attemptRootValue + "/data")
	if err != nil {
		return provider.RclonePublicationInput{}, err
	}
	controlRoot, err := provider.NewRclonePrivateLocator(attemptRootValue + "/control")
	if err != nil {
		return provider.RclonePublicationInput{}, err
	}
	costDigest := digestRclonePublicationCost(config.Rclone)
	request := &provider.RclonePortablePublicationRequest{
		Attempt: execution.attempt, BoundConfig: bound, AttemptRoot: attemptRoot, DataRoot: dataRoot, ControlRoot: controlRoot,
		MarkerKey: append([]byte(nil), execution.markerKey...), CapabilityEvidenceDigest: execution.binding.PreflightDigest,
		CostEvidenceDigest: costDigest, SettleInterval: managedRclonePortableSettleInterval,
		FullVerifyMaxBytes: config.Rclone.FullVerifyMaxBytes, ControlPayloadMaxBytes: config.Rclone.ControlPayloadMaxBytes,
		LowLevelRetries: config.Rclone.LowLevelRetries, Runtime: provider.RemoteCommandAccess{Node: execution.task.Node},
		ManifestOptions: manifestOptions,
	}
	input.PortableRequest = request
	return input, nil
}

func digestRclonePublicationCost(config backupasset.RclonePublicationConfig) string {
	writer := backupasset.NewCanonicalSHA256()
	writer.String("xirang-rclone-publication-cost-v1")
	writer.Int64(config.FullVerifyMaxBytes)
	writer.Int64(config.ControlPayloadMaxBytes)
	writer.Int64(config.ManifestChunkMaxBytes)
	writer.Int64(int64(config.LowLevelRetries))
	digest, _ := writer.HexDigest()
	return digest
}

func (execution *rclonePublicationExecution) Cancel(cause error) error {
	if execution == nil {
		return nil
	}
	if !validPublicationCancelCause(cause) {
		return fmt.Errorf("%w: unsafe managed Rclone publication cancellation cause", backupasset.ErrInvalidState)
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

func (execution *rclonePublicationExecution) Abandon(cause error) error {
	if err := execution.Cancel(cause); err != nil {
		return err
	}
	execution.closeAdmission()
	return nil
}

func (*rclonePublicationExecution) CompleteCompatibility(context.Context) error {
	return fmt.Errorf("%w: compatibility completion is unavailable", backupasset.ErrInvalidState)
}

func (execution *rclonePublicationExecution) RecordProviderCommit(ctx context.Context, tagged provider.ProviderCommit) (publication.Outcome, error) {
	if execution == nil || execution.service == nil {
		return publication.Outcome{}, fmt.Errorf("%w: managed Rclone execution is unavailable", backupasset.ErrInvalidState)
	}
	commit, err := tagged.RcloneCommit()
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
	outcome, transitioned, err := execution.service.recordRcloneProviderCommit(
		commitContext, execution.attempt, execution.binding, execution.markerKey, execution.childFence, commit,
	)
	if err != nil {
		return publication.Outcome{}, err
	}
	if transitioned {
		execution.service.metrics.ObserveOutcome(backupasset.ProviderRclone, publication.StageExecution, backupasset.PublicationOutcomeSuccess)
		_ = execution.service.tryWake(outcome.RecoveryPointID)
		if err := execution.service.writeRclonePublicationAudit(
			commitContext, execution.audit, backupasset.AuditActionRecoveryPointPublicationCommit,
			backupasset.AuditOutcomeSuccess, execution.attempt, publication.StageExecution,
			backupasset.RecoveryPointVerifying, "", "",
		); err != nil {
			execution.service.metrics.ObserveAuditFailure(publication.StageExecution)
		}
	}
	execution.closeAdmission()
	return outcome, nil
}

func (execution *rclonePublicationExecution) Defer(ctx context.Context, deferral publication.Deferral) error {
	if execution == nil || execution.service == nil {
		return fmt.Errorf("%w: managed Rclone execution is unavailable", backupasset.ErrInvalidState)
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
	updated := false
	deferredState := backupasset.RecoveryPointPreparing
	err := execution.service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var point model.RecoveryPoint
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&point, "id = ?", execution.attempt.RecoveryPointID).Error; err != nil {
			return fmt.Errorf("lock deferred managed Rclone point: %w", err)
		}
		if point.State != string(backupasset.RecoveryPointPreparing) && point.State != string(backupasset.RecoveryPointVerifying) {
			return fmt.Errorf("%w: deferred managed Rclone point is terminal", backupasset.ErrConflict)
		}
		if err := execution.service.lease.ValidateFenceTx(ctx, tx, execution.childFence); err != nil {
			return err
		}
		consistency, err := backupasset.DecodePublicationConsistency(point.ConsistencyJSON)
		if err != nil {
			return err
		}
		deferredState = backupasset.RecoveryPointState(point.State)
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
			return fmt.Errorf("save deferred managed Rclone point: %w", err)
		}
		updated = true
		return nil
	})
	if err != nil {
		return err
	}
	if updated {
		if err := execution.service.writeRclonePublicationAudit(
			ctx, execution.audit, backupasset.AuditActionRecoveryPointPublicationVerify,
			backupasset.AuditOutcomeFailure, execution.attempt, publication.StageExecution,
			deferredState, string(deferral.Code), deferral.Code,
		); err != nil {
			execution.service.metrics.ObserveAuditFailure(publication.StageExecution)
		}
	}
	execution.closeAdmission()
	return nil
}

func (execution *rclonePublicationExecution) Reject(ctx context.Context, code backupasset.PublicationFailureCode) error {
	if code != backupasset.FailurePublicationPreconditionMissing {
		return fmt.Errorf("%w: invalid pre-command managed Rclone rejection", backupasset.ErrInvalidState)
	}
	return execution.finishFailed(ctx, code)
}

func (execution *rclonePublicationExecution) Fail(ctx context.Context, code backupasset.PublicationFailureCode) error {
	if !rsyncPostCommandPublicationFailure(code) {
		return fmt.Errorf("%w: invalid managed Rclone post-command failure", backupasset.ErrInvalidState)
	}
	return execution.finishFailed(ctx, code)
}

func (execution *rclonePublicationExecution) finishFailed(ctx context.Context, code backupasset.PublicationFailureCode) error {
	if err := execution.Cancel(nil); err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	err := execution.service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var point model.RecoveryPoint
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&point, "id = ?", execution.attempt.RecoveryPointID).Error; err != nil {
			return err
		}
		if point.State != string(backupasset.RecoveryPointPreparing) && point.State != string(backupasset.RecoveryPointVerifying) {
			return fmt.Errorf("%w: managed Rclone point is terminal", backupasset.ErrConflict)
		}
		if err := execution.service.lease.ValidateFenceTx(ctx, tx, execution.childFence); err != nil {
			return err
		}
		consistency, err := backupasset.DecodePublicationConsistency(point.ConsistencyJSON)
		if err != nil {
			return err
		}
		now := execution.service.now().UTC()
		consistency.PublicationRevision++
		consistency.AttemptCount++
		consistency.Code = code
		consistency.LastAttemptAt = &now
		encoded, err := backupasset.EncodePublicationConsistency(consistency)
		if err != nil {
			return err
		}
		point.ConsistencyJSON = encoded
		point.State = string(backupasset.RecoveryPointFailed)
		point.UpdatedAt = now
		if err := tx.Save(&point).Error; err != nil {
			return err
		}
		return execution.service.lease.ReleaseTx(ctx, tx, execution.childFence)
	})
	execution.closeAdmission()
	return err
}

func (execution *rclonePublicationExecution) runHeartbeat(config backupasset.LeaseConfig) {
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
		}
	}
}

func (execution *rclonePublicationExecution) closeAdmission() {
	if execution == nil || execution.token == nil {
		return
	}
	execution.closeOnce.Do(func() { _ = execution.token.Close() })
}

func (service *PublicationService) writeRclonePublicationAudit(
	ctx context.Context,
	audit backupasset.PublicationAuditContext,
	action backupasset.AuditAction,
	outcome backupasset.AuditOutcome,
	attempt provider.RcloneAttemptV1,
	stage publication.PublicationStage,
	status backupasset.RecoveryPointState,
	code string,
	failure backupasset.PublicationFailureCode,
) error {
	if service.audit == nil {
		return nil
	}
	if err := attempt.Validate(); err != nil || backupasset.ValidatePublicationAuditContext(audit) != nil ||
		publication.ValidatePublicationStage(stage) != nil || status == "" {
		return fmt.Errorf("%w: invalid managed Rclone publication audit input", backupasset.ErrInvalidState)
	}
	fields := map[backupasset.AuditField]any{
		backupasset.AuditFieldStage: string(stage), backupasset.AuditFieldStatus: string(status),
		backupasset.AuditFieldCorrelationID: audit.CorrelationID,
	}
	if code != "" {
		fields[backupasset.AuditFieldCode] = code
	}
	input := backupasset.AuditEventInput{
		Actor: audit.Actor, Action: action, Outcome: outcome, RepositoryID: attempt.RepositoryID,
		RecoveryPointID: attempt.RecoveryPointID, TaskID: &attempt.TaskID, TaskRunID: &attempt.TaskRunID, Fields: fields,
	}
	if failure != "" {
		input.FailureCode = string(failure)
	}
	return service.audit.Write(ctx, input)
}

var _ publication.Execution = (*rclonePublicationExecution)(nil)
