package processing

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/processing/capabilityspec"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrInvalidArtifact       = errors.New("invalid Derived artifact")
	ErrInvalidManifest       = errors.New("invalid Derived artifact manifest")
	ErrManifestFenceLost     = errors.New("derived manifest publication fence lost")
	ErrManifestPolicyChanged = errors.New("derived manifest security policy changed")
	ErrManifestSourceChanged = errors.New("derived manifest source changed")
)

type ArtifactRole string

const (
	ArtifactRoleNoop      ArtifactRole = "noop"
	ArtifactRoleContent   ArtifactRole = "content"
	ArtifactRoleOCR       ArtifactRole = "ocr"
	ArtifactRoleThumbnail ArtifactRole = "thumbnail"
	ArtifactRoleMetadata  ArtifactRole = "metadata"
)

type ArtifactCompleteness string

const (
	ArtifactComplete ArtifactCompleteness = "complete"
	ArtifactPartial  ArtifactCompleteness = "partial"
)

type ArtifactDeclaration struct {
	Ordinal           int                  `json:"ordinal"`
	Role              ArtifactRole         `json:"role"`
	MediaType         string               `json:"media_type"`
	PlaintextSize     int64                `json:"plaintext_size"`
	PlaintextDigest   string               `json:"plaintext_digest"`
	Completeness      ArtifactCompleteness `json:"completeness"`
	CoverageCanonical []byte               `json:"coverage_canonical"`
}

type UploadArtifactRequest struct {
	JobID     string
	AttemptID string
	WorkerID  string
	GrantID   string
	Artifact  ArtifactDeclaration
}

type UploadedArtifact struct {
	UploadID string `json:"upload_id"`
	BlobID   string `json:"blob_id"`
	Ordinal  int    `json:"ordinal"`
}

type CommitManifestRequest struct {
	JobID                  string
	AttemptID              string
	WorkerID               string
	GrantID                string
	RecoveryPointFence     backupasset.LeaseFence
	SecurityPolicyRevision string
	Artifacts              []ArtifactDeclaration
}

type CommitManifestResult struct {
	ArtifactSetID      string
	ManifestDigest     string
	ProjectionRequired bool
}

type pendingProjection struct {
	job              model.BackupAssetProcessingJob
	attempt          model.BackupAssetProcessingAttempt
	set              model.BackupAssetDerivedArtifactSet
	fence            backupasset.LeaseFence
	rejectionPending bool
}

type manifestArtifactIdentity struct {
	artifactID  string
	referenceID string
}

type ArtifactSinkConfig struct {
	MaxArtifacts     int
	MaxArtifactBytes int64
	MaxTotalBytes    int64
}

type ProcessingSourceRevalidator interface {
	RevalidateProcessingSource(context.Context, WorkDescriptorV1) error
}

type ArtifactSink struct {
	db           *gorm.DB
	leaseService *backupasset.LeaseService
	grants       *GrantService
	store        *DerivedStore
	lifecycle    *DerivedLifecycle
	source       ProcessingSourceRevalidator
	policy       func(context.Context) (string, error)
	now          func() time.Time
	config       ArtifactSinkConfig
	metrics      Metrics
}

func NewArtifactSink(
	db *gorm.DB,
	leaseService *backupasset.LeaseService,
	grants *GrantService,
	store *DerivedStore,
	lifecycle *DerivedLifecycle,
	source ProcessingSourceRevalidator,
	policy func(context.Context) (string, error),
	now func() time.Time,
	config ArtifactSinkConfig,
) (*ArtifactSink, error) {
	if db == nil || leaseService == nil || grants == nil || store == nil || lifecycle == nil || source == nil || policy == nil ||
		config.MaxArtifacts <= 0 || config.MaxArtifacts > 256 || config.MaxArtifactBytes <= 0 ||
		config.MaxTotalBytes < config.MaxArtifactBytes {
		return nil, ErrInvalidManifest
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &ArtifactSink{
		db: db, leaseService: leaseService, grants: grants, store: store, lifecycle: lifecycle,
		source: source, policy: policy, now: now, config: config, metrics: NoopMetrics{},
	}, nil
}

func (sink *ArtifactSink) SetMetrics(metrics Metrics) {
	if sink == nil {
		return
	}
	if metrics == nil {
		metrics = NoopMetrics{}
	}
	sink.metrics = metrics
}

func (sink *ArtifactSink) UploadArtifact(ctx context.Context, request UploadArtifactRequest, body io.Reader) (UploadedArtifact, error) {
	if sink == nil || body == nil || !validUploadIdentity(request) || validateArtifactDeclaration(request.Artifact, sink.config) != nil {
		return UploadedArtifact{}, ErrInvalidArtifact
	}
	if err := sink.validateSinkGrant(ctx, request.JobID, request.AttemptID, request.WorkerID, request.GrantID); err != nil {
		return UploadedArtifact{}, err
	}
	descriptor, err := sink.loadUploadDescriptor(ctx, request.JobID, request.AttemptID)
	if err != nil || !validArtifactMediaForDescriptor(descriptor, request.Artifact.Role, request.Artifact.MediaType) {
		return UploadedArtifact{}, ErrInvalidArtifact
	}
	reservation, err := sink.grants.Reserve(ctx, ReserveGrantRequest{
		GrantID: request.GrantID, Kind: GrantRequestUpload, Bytes: request.Artifact.PlaintextSize,
	})
	if err != nil {
		if errors.Is(err, ErrGrantBudgetExceeded) {
			return UploadedArtifact{}, ErrDerivedQuotaExceeded
		}
		return UploadedArtifact{}, ErrGrantDenied
	}
	finalized := false
	defer func() {
		if !finalized {
			sink.finalizeReservationDetached(ctx, FinalizeGrantRequest{
				ReservationID: reservation.ReservationID, Outcome: GrantRequestReconciled,
				EvidenceKnown: false, FailureCode: GrantFailureReconciledCrash,
			})
		}
	}()
	upload, err := sink.admitArtifact(ctx, request)
	if err != nil {
		finalized = true
		sink.finalizeReservationDetached(ctx, FinalizeGrantRequest{
			ReservationID: reservation.ReservationID, Outcome: GrantRequestFailed,
			StoredBytes: 0, EvidenceKnown: true, FailureCode: GrantFailureBudgetExhausted,
		})
		return UploadedArtifact{}, err
	}
	blob, err := sink.store.PutBlob(ctx, DerivedBlobDeclaration{
		PlaintextSize: request.Artifact.PlaintextSize, PlaintextDigest: request.Artifact.PlaintextDigest,
	}, body)
	if err != nil {
		finalized = true
		sink.rejectUploadDetached(ctx, upload.ID, "invalid_output")
		sink.finalizeReservationDetached(ctx, FinalizeGrantRequest{
			ReservationID: reservation.ReservationID, Outcome: GrantRequestFailed,
			ProviderBytes: 0, StoredBytes: 0, EvidenceKnown: true, FailureCode: GrantFailureWriteFailed,
		})
		return UploadedArtifact{}, errors.Join(ErrInvalidArtifact, err)
	}
	now := sink.utcNow()
	updated := sink.db.WithContext(ctx).Model(&model.BackupAssetProcessingUpload{}).
		Where("id = ? AND state = ?", upload.ID, "reserved").Updates(map[string]any{
		"actual_size": request.Artifact.PlaintextSize, "actual_digest": request.Artifact.PlaintextDigest,
		"staging_id": blob.BlobID, "state": "staged", "finished_at": now, "updated_at": now,
	})
	if updated.Error != nil || updated.RowsAffected != 1 {
		sink.rejectUploadDetached(ctx, upload.ID, "invalid_output")
		sink.discardBlobDetached(ctx, blob.BlobID)
		return UploadedArtifact{}, errors.Join(ErrInvalidArtifact, updated.Error)
	}
	cleanupCtx, cancelCleanup := derivedManifestCleanupContext(ctx)
	err = sink.grants.Finalize(cleanupCtx, FinalizeGrantRequest{
		ReservationID: reservation.ReservationID, Outcome: GrantRequestSucceeded,
		ProviderBytes: 0, StoredBytes: request.Artifact.PlaintextSize, EvidenceKnown: true,
	})
	if err != nil {
		_ = sink.db.WithContext(cleanupCtx).Model(&model.BackupAssetProcessingUpload{}).Where("id = ?", upload.ID).
			Updates(map[string]any{"state": "orphaned", "failure_code": "quota_busy", "updated_at": now}).Error
		_ = sink.store.discardBlobIfUnreferenced(cleanupCtx, blob.BlobID)
		cancelCleanup()
		return UploadedArtifact{}, err
	}
	cancelCleanup()
	finalized = true
	sink.metrics.AddSinkBytes(request.Artifact.PlaintextSize)
	return UploadedArtifact{UploadID: upload.ID, BlobID: blob.BlobID, Ordinal: upload.Ordinal}, nil
}

func (sink *ArtifactSink) CommitManifest(ctx context.Context, request CommitManifestRequest) (CommitManifestResult, error) {
	if sink == nil || !validManifestIdentity(request) || len(request.Artifacts) == 0 || len(request.Artifacts) > sink.config.MaxArtifacts {
		return CommitManifestResult{}, ErrInvalidManifest
	}
	artifacts := cloneAndSortArtifacts(request.Artifacts)
	if err := validateManifestArtifacts(artifacts, sink.config); err != nil {
		return CommitManifestResult{}, err
	}
	manifestDigest := computeManifestDigest(artifacts)
	job, descriptor, err := sink.loadManifestJob(ctx, request)
	if err != nil {
		return CommitManifestResult{}, err
	}
	for _, artifact := range artifacts {
		if !validArtifactMediaForDescriptor(descriptor, artifact.Role, artifact.MediaType) {
			return CommitManifestResult{}, ErrInvalidManifest
		}
	}
	currentPolicy, err := sink.policy(ctx)
	if err != nil || currentPolicy != request.SecurityPolicyRevision || job.SecurityPolicyRevision != request.SecurityPolicyRevision {
		return CommitManifestResult{}, ErrManifestPolicyChanged
	}
	if err := sink.source.RevalidateProcessingSource(ctx, descriptor); err != nil {
		return CommitManifestResult{}, errors.Join(ErrManifestSourceChanged, err)
	}
	uploads, err := sink.loadAndValidateUploads(ctx, request, artifacts)
	if err != nil {
		return CommitManifestResult{}, err
	}
	setID, err := backupasset.NewOpaqueID()
	if err != nil {
		return CommitManifestResult{}, err
	}
	identities, err := newManifestArtifactIdentities(len(artifacts))
	if err != nil {
		return CommitManifestResult{}, err
	}
	result := CommitManifestResult{ArtifactSetID: setID, ManifestDigest: manifestDigest, ProjectionRequired: manifestNeedsProjection(descriptor, artifacts)}
	var preparedProjection PreparedDerivedProjection
	if result.ProjectionRequired {
		fields, classification, fieldErr := sink.prepareProjectionEvidence(ctx, descriptor, artifacts, uploads, identities)
		if fieldErr != nil {
			return result, fieldErr
		}
		preparedProjection, err = sink.lifecycle.projection.PreparePublish(ctx, DerivedProjectionPublish{
			ArtifactSetID: setID, RecoveryPointID: job.RecoveryPointID,
			CatalogGenerationID: job.CatalogGenerationID, EntryID: job.EntryID, SourceFingerprint: job.SourceFingerprint,
			RecoveryPointFence: request.RecoveryPointFence, Fields: fields, Classification: classification,
		})
		if err != nil {
			return result, fmt.Errorf("prepare Derived Search projection: %w", err)
		}
	}
	err = sink.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := sink.publishManifestTx(ctx, tx, request, job, artifacts, uploads, identities, result, setID); err != nil {
			return err
		}
		if !result.ProjectionRequired {
			return nil
		}
		publication, err := preparedProjection.PublishTx(ctx, tx)
		if err != nil {
			return fmt.Errorf("publish Derived Search projection: %w", err)
		}
		if !validProjectionPublication(publication, setID) {
			return ErrInvalidManifest
		}
		if err := sink.completeProjectedManifestTx(ctx, tx, request, setID, publication.Revision); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, backupasset.ErrLeaseFenceLost) || errors.Is(err, backupasset.ErrLeaseDeadlineExceeded) || errors.Is(err, ErrManifestFenceLost) {
			cleanupCtx, cancelCleanup := derivedManifestCleanupContext(ctx)
			sink.rejectInvisibleUploads(cleanupCtx, uploads, "lease_lost")
			cancelCleanup()
			return CommitManifestResult{}, ErrManifestFenceLost
		}
		return CommitManifestResult{}, err
	}
	return result, nil
}

func (sink *ArtifactSink) ReconcilePendingProjections(ctx context.Context, batchSize int) (int, error) {
	if sink == nil || sink.db == nil || sink.leaseService == nil || sink.lifecycle == nil || batchSize <= 0 || batchSize > 10000 {
		return 0, ErrInvalidManifest
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var candidates []model.BackupAssetProcessingJob
	if err := sink.db.WithContext(ctx).
		Where("state = ? AND is_current = ? AND current_attempt_id IS NOT NULL AND current_artifact_set_id IS NOT NULL", ProcessingValidating, true).
		Order("updated_at ASC, id ASC").Limit(batchSize).Find(&candidates).Error; err != nil {
		return 0, fmt.Errorf("load pending Derived projections: %w", err)
	}
	recovered := 0
	for _, candidate := range candidates {
		pending, ready, err := sink.preparePendingProjection(ctx, candidate.ID)
		if err != nil {
			return recovered, err
		}
		if !ready {
			continue
		}
		if pending.rejectionPending {
			reason, reasonErr := sink.pendingSupersedeReason(pending)
			if reasonErr != nil {
				return recovered, reasonErr
			}
			if err := sink.finishPendingSuperseded(ctx, pending, reason); err != nil {
				return recovered, err
			}
			continue
		}
		descriptor, err := DecodeWorkDescriptorV1(pending.job.DescriptorCanonical)
		if err != nil {
			return recovered, ErrInvalidManifest
		}
		currentPolicy, err := sink.policy(ctx)
		if err != nil {
			return recovered, ErrManifestPolicyChanged
		}
		if currentPolicy != pending.job.SecurityPolicyRevision {
			if revokeErr := sink.lifecycle.RevokeSet(ctx, pending.set.ID, DerivedRevokePolicyChanged); revokeErr != nil {
				return recovered, errors.Join(ErrManifestPolicyChanged, revokeErr)
			}
			if finishErr := sink.finishPendingSuperseded(ctx, pending, SupersedeReasonPolicyChanged); finishErr != nil {
				return recovered, errors.Join(ErrManifestPolicyChanged, finishErr)
			}
			continue
		}
		if err := sink.source.RevalidateProcessingSource(ctx, descriptor); err != nil {
			if revokeErr := sink.lifecycle.RevokeSet(ctx, pending.set.ID, DerivedRevokeSourceChanged); revokeErr != nil {
				return recovered, errors.Join(ErrManifestSourceChanged, err, revokeErr)
			}
			if finishErr := sink.finishPendingSuperseded(ctx, pending, SupersedeReasonSourceChanged); finishErr != nil {
				return recovered, errors.Join(ErrManifestSourceChanged, err, finishErr)
			}
			continue
		}
		fields, classification, err := sink.loadProjectionEvidence(ctx, descriptor, pending.set.ID)
		if err != nil {
			return recovered, err
		}
		prepared, err := sink.lifecycle.projection.PreparePublish(ctx, DerivedProjectionPublish{
			ArtifactSetID: pending.set.ID, RecoveryPointID: pending.job.RecoveryPointID,
			CatalogGenerationID: pending.job.CatalogGenerationID, EntryID: pending.job.EntryID,
			SourceFingerprint: pending.job.SourceFingerprint, RecoveryPointFence: pending.fence,
			Fields: fields, Classification: classification,
		})
		if err != nil {
			return recovered, fmt.Errorf("prepare reconciled Derived Search projection: %w", err)
		}
		commitRequest := CommitManifestRequest{
			JobID: pending.job.ID, AttemptID: pending.attempt.ID, WorkerID: pending.attempt.WorkerID,
			RecoveryPointFence: pending.fence,
		}
		var projectionRevision int64
		err = sink.retryManifestConflicts(ctx, func() error {
			return sink.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				publication, publishErr := prepared.PublishTx(ctx, tx)
				if publishErr != nil {
					return fmt.Errorf("reconcile Derived Search projection: %w", publishErr)
				}
				if !validProjectionPublication(publication, pending.set.ID) {
					return ErrInvalidManifest
				}
				projectionRevision = publication.Revision
				return sink.completeProjectedManifestTx(ctx, tx, commitRequest, pending.set.ID, publication.Revision)
			})
		})
		if err != nil {
			completed, proofErr := sink.projectionCompletionMatches(ctx, pending.job.ID, pending.attempt.ID, pending.set.ID, projectionRevision)
			if proofErr != nil || !completed {
				return recovered, errors.Join(err, proofErr)
			}
			continue
		}
		recovered++
	}
	return recovered, nil
}

func (sink *ArtifactSink) preparePendingProjection(ctx context.Context, jobID string) (pendingProjection, bool, error) {
	var pending pendingProjection
	ready := false
	err := sink.retryManifestConflicts(ctx, func() error {
		pending = pendingProjection{}
		ready = false
		return sink.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", jobID).Limit(1).Find(&pending.job)
			if result.Error != nil {
				return fmt.Errorf("load pending projection job: %w", result.Error)
			}
			if result.RowsAffected != 1 || pending.job.State != string(ProcessingValidating) || !pending.job.IsCurrent ||
				pending.job.CurrentAttemptID == nil || pending.job.CurrentArtifactSetID == nil {
				return nil
			}
			result = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND job_id = ?", *pending.job.CurrentAttemptID, pending.job.ID).
				Limit(1).Find(&pending.attempt)
			if result.Error != nil {
				return fmt.Errorf("load pending projection attempt: %w", result.Error)
			}
			if result.RowsAffected != 1 || pending.attempt.State != "active" || !pending.attempt.IsCurrent {
				return ErrManifestFenceLost
			}
			result = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND job_id = ? AND attempt_id = ?",
				*pending.job.CurrentArtifactSetID, pending.job.ID, pending.attempt.ID).Limit(1).Find(&pending.set)
			if result.Error != nil {
				return fmt.Errorf("load pending Derived artifact set: %w", result.Error)
			}
			if result.RowsAffected != 1 || pending.set.State != "active" || !pending.set.ProjectionRequired {
				if result.RowsAffected == 1 && pending.set.ProjectionRequired && !pending.set.ProjectionPublished && derivedSetUnavailable(pending.set.State) {
					pending.rejectionPending = true
					ready = true
					return nil
				}
				return ErrInvalidManifest
			}
			if pending.set.ProjectionPublished {
				return nil
			}
			var lease model.RecoveryPointLease
			result = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", pending.attempt.RecoveryPointLeaseID).Limit(1).Find(&lease)
			if result.Error != nil {
				return fmt.Errorf("load pending projection lease: %w", result.Error)
			}
			if result.RowsAffected != 1 || lease.RecoveryPointID != pending.job.RecoveryPointID || lease.OwnerID != pending.job.ID ||
				lease.HolderType != string(backupasset.LeaseHolderProcessingJob) || lease.AttemptID != pending.attempt.RecoveryPointAttemptID ||
				hashFence(lease.FenceToken) != pending.attempt.RecoveryPointFenceHash {
				return ErrManifestFenceLost
			}
			now := sink.utcNow()
			if !now.Before(pending.job.AbsoluteDeadline.UTC()) || !now.Before(pending.attempt.AbsoluteDeadline.UTC()) ||
				!now.Before(lease.AbsoluteDeadline.UTC()) {
				return nil
			}
			recoveryAuthority := lease.LastHeartbeatAt.UTC().After(pending.attempt.LastHeartbeatAt.UTC())
			if lease.Status == string(backupasset.LeaseActive) && now.Before(lease.LeaseExpiresAt.UTC()) &&
				(now.Before(pending.attempt.WorkerLeaseExpiresAt.UTC()) || recoveryAuthority) {
				pending.fence = leaseFenceFromRow(lease)
				ready = true
				return nil
			}
			if now.Before(pending.attempt.WorkerLeaseExpiresAt.UTC()) || lease.Status != string(backupasset.LeaseActive) ||
				now.Before(lease.LeaseExpiresAt.UTC()) {
				return nil
			}
			takenOver, err := sink.leaseService.TakeoverTx(ctx, tx, backupasset.TakeoverLeaseRequest{LeaseID: lease.ID, OwnerID: pending.job.ID})
			if err != nil {
				return err
			}
			updated := tx.Model(&model.BackupAssetProcessingAttempt{}).
				Where("id = ? AND state = ? AND is_current = ? AND recovery_point_attempt_id = ? AND recovery_point_fence_hash = ?",
					pending.attempt.ID, "active", true, pending.attempt.RecoveryPointAttemptID, pending.attempt.RecoveryPointFenceHash).
				Updates(map[string]any{
					"recovery_point_attempt_id": takenOver.Fence.AttemptID,
					"recovery_point_fence_hash": hashFence(takenOver.Fence.FenceToken),
					"updated_at":                sink.utcNow(),
				})
			if updated.Error != nil || updated.RowsAffected != 1 {
				return errors.Join(ErrManifestFenceLost, updated.Error)
			}
			pending.attempt.RecoveryPointAttemptID = takenOver.Fence.AttemptID
			pending.attempt.RecoveryPointFenceHash = hashFence(takenOver.Fence.FenceToken)
			pending.fence = takenOver.Fence
			ready = true
			return nil
		})
	})
	return pending, ready, err
}

func (sink *ArtifactSink) finishPendingSuperseded(ctx context.Context, pending pendingProjection, reason SupersedeReason) error {
	if reason != SupersedeReasonSourceChanged && reason != SupersedeReasonPolicyChanged {
		return ErrInvalidManifest
	}
	return sink.retryManifestConflicts(ctx, func() error {
		return sink.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var job model.BackupAssetProcessingJob
			result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", pending.job.ID).Limit(1).Find(&job)
			if result.Error != nil || result.RowsAffected != 1 {
				return result.Error
			}
			if job.State == string(ProcessingSuperseded) && !job.IsCurrent {
				return nil
			}
			if job.State != string(ProcessingValidating) || !job.IsCurrent || job.CurrentAttemptID == nil || *job.CurrentAttemptID != pending.attempt.ID {
				return ErrManifestFenceLost
			}
			var attempt model.BackupAssetProcessingAttempt
			result = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND job_id = ?", pending.attempt.ID, job.ID).Limit(1).Find(&attempt)
			if result.Error != nil || result.RowsAffected != 1 {
				return result.Error
			}
			now := sink.utcNow()
			if attempt.State == "active" && attempt.IsCurrent {
				if err := revokeAttemptGrantsTx(tx, attempt.ID, now, transitionRevocationReason(AttemptTransitionRequest{
					To: ProcessingSuperseded, SupersedeReason: reason,
				})); err != nil {
					return err
				}
				if err := finishAttemptTx(tx, attempt.ID, now, "superseded", ""); err != nil {
					return err
				}
			}
			var lease model.RecoveryPointLease
			result = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", attempt.RecoveryPointLeaseID).Limit(1).Find(&lease)
			if result.Error != nil || result.RowsAffected != 1 {
				return result.Error
			}
			if lease.Status == string(backupasset.LeaseActive) {
				fence := leaseFenceFromRow(lease)
				if lease.OwnerID == job.ID && hashFence(lease.FenceToken) == attempt.RecoveryPointFenceHash &&
					now.Before(lease.LeaseExpiresAt.UTC()) && now.Before(lease.AbsoluteDeadline.UTC()) {
					if err := sink.leaseService.ReleaseTx(ctx, tx, fence); err != nil {
						return err
					}
				} else if err := tx.Model(&model.RecoveryPointLease{}).Where("id = ? AND status = ?", lease.ID, backupasset.LeaseActive).
					Updates(map[string]any{"status": backupasset.LeaseExpired, "updated_at": now}).Error; err != nil {
					return err
				}
			}
			revision, err := ValidateTransition(TransitionRequest{
				From: ProcessingValidating, To: ProcessingSuperseded,
				CurrentRevision: job.TransitionRevision, ExpectedRevision: job.TransitionRevision,
				SupersedeReason: reason,
			})
			if err != nil {
				return err
			}
			updated := tx.Model(&model.BackupAssetProcessingJob{}).Where("id = ? AND transition_revision = ?", job.ID, job.TransitionRevision).
				Updates(map[string]any{
					"state": string(ProcessingSuperseded), "transition_revision": revision,
					"supersede_reason": string(reason), "current_attempt_id": nil,
					"is_current": false, "finished_at": now, "updated_at": now, "version": gorm.Expr("version + 1"),
				})
			if updated.Error != nil || updated.RowsAffected != 1 {
				return errors.Join(ErrRevisionConflict, updated.Error)
			}
			return nil
		})
	})
}

func (sink *ArtifactSink) pendingSupersedeReason(pending pendingProjection) (SupersedeReason, error) {
	switch DerivedRevokeReason(pending.set.RevocationReason) {
	case DerivedRevokeSourceChanged:
		return SupersedeReasonSourceChanged, nil
	case DerivedRevokePolicyChanged:
		return SupersedeReasonPolicyChanged, nil
	default:
		return "", ErrInvalidManifest
	}
}

func (sink *ArtifactSink) finalizeReservationDetached(ctx context.Context, request FinalizeGrantRequest) {
	cleanupCtx, cancel := derivedManifestCleanupContext(ctx)
	defer cancel()
	_ = sink.grants.Finalize(cleanupCtx, request)
}

func (sink *ArtifactSink) discardBlobDetached(ctx context.Context, blobID string) {
	cleanupCtx, cancel := derivedManifestCleanupContext(ctx)
	defer cancel()
	_ = sink.store.discardBlobIfUnreferenced(cleanupCtx, blobID)
}

func (sink *ArtifactSink) rejectUploadDetached(ctx context.Context, uploadID, failure string) {
	cleanupCtx, cancel := derivedManifestCleanupContext(ctx)
	defer cancel()
	now := sink.utcNow()
	_ = sink.db.WithContext(cleanupCtx).Model(&model.BackupAssetProcessingUpload{}).
		Where("id = ? AND state = ?", uploadID, "reserved").Updates(map[string]any{
		"state": "rejected", "failure_code": failure, "finished_at": now, "updated_at": now,
	}).Error
}

func derivedManifestCleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
}

func (sink *ArtifactSink) publishManifestTx(
	ctx context.Context,
	tx *gorm.DB,
	request CommitManifestRequest,
	loadedJob model.BackupAssetProcessingJob,
	artifacts []ArtifactDeclaration,
	uploads []model.BackupAssetProcessingUpload,
	identities []manifestArtifactIdentity,
	manifest CommitManifestResult,
	setID string,
) error {
	if backupasset.ValidateOpaqueID(setID) != nil || manifest.ArtifactSetID != setID || len(identities) != len(artifacts) {
		return ErrInvalidManifest
	}
	var job model.BackupAssetProcessingJob
	result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", request.JobID).Limit(1).Find(&job)
	if result.Error != nil || result.RowsAffected != 1 || job.State != string(ProcessingUploading) || !job.IsCurrent ||
		job.TransitionRevision != loadedJob.TransitionRevision || job.CurrentAttemptID == nil || *job.CurrentAttemptID != request.AttemptID ||
		job.SecurityPolicyRevision != request.SecurityPolicyRevision {
		return ErrInvalidManifest
	}
	var attempt model.BackupAssetProcessingAttempt
	result = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND job_id = ? AND worker_id = ?", request.AttemptID, job.ID, request.WorkerID).Limit(1).Find(&attempt)
	if result.Error != nil || result.RowsAffected != 1 || attempt.State != "active" || !attempt.IsCurrent ||
		attempt.RecoveryPointFenceHash != hashFence(request.RecoveryPointFence.FenceToken) {
		return ErrManifestFenceLost
	}
	var grant model.BackupAssetProcessingGrant
	result = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", request.GrantID).Limit(1).Find(&grant)
	if result.Error != nil || result.RowsAffected != 1 || grant.Kind != string(GrantSink) || grant.State != string(GrantActive) ||
		grant.JobID != job.ID || grant.AttemptID != attempt.ID || grant.WorkerID != request.WorkerID || grant.FenceHash != attempt.RecoveryPointFenceHash {
		return ErrManifestFenceLost
	}
	if err := sink.leaseService.ValidateFenceTx(ctx, tx, request.RecoveryPointFence); err != nil {
		return errors.Join(ErrManifestFenceLost, err)
	}
	now := sink.utcNow()
	completeness := string(ArtifactComplete)
	totalBytes := int64(0)
	for _, artifact := range artifacts {
		totalBytes += artifact.PlaintextSize
		if artifact.Completeness == ArtifactPartial {
			completeness = string(ArtifactPartial)
		}
	}
	set := model.BackupAssetDerivedArtifactSet{
		ID: setID, JobID: job.ID, AttemptID: attempt.ID, WorkKey: job.WorkKey,
		RecoveryPointID: job.RecoveryPointID, CatalogGenerationID: job.CatalogGenerationID, EntryID: job.EntryID,
		SourceFingerprint: job.SourceFingerprint, SecurityPolicyRevision: job.SecurityPolicyRevision,
		ManifestDigest: manifest.ManifestDigest, State: "active", Completeness: completeness,
		ArtifactCount: len(artifacts), TotalPlaintextBytes: totalBytes,
		ProjectionRequired: manifest.ProjectionRequired, ProjectionPublished: false, ProjectionRevision: 0,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := tx.Create(&set).Error; err != nil {
		return fmt.Errorf("create Derived artifact set: %w", err)
	}
	for index, declaration := range artifacts {
		upload := uploads[index]
		identity := identities[index]
		if backupasset.ValidateOpaqueID(identity.artifactID) != nil || backupasset.ValidateOpaqueID(identity.referenceID) != nil {
			return ErrInvalidManifest
		}
		var blob model.BackupAssetDerivedBlob
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND state = ?", upload.StagingID, "active").Limit(1).Find(&blob)
		if result.Error != nil || result.RowsAffected != 1 || blob.PlaintextSize != declaration.PlaintextSize || blob.PlaintextDigest != declaration.PlaintextDigest {
			return ErrInvalidManifest
		}
		artifact := model.BackupAssetDerivedArtifact{
			ID: identity.artifactID, ArtifactSetID: set.ID, Ordinal: declaration.Ordinal, Role: string(declaration.Role),
			MediaType: declaration.MediaType, PlaintextSize: declaration.PlaintextSize, PlaintextDigest: declaration.PlaintextDigest,
			Completeness: string(declaration.Completeness), CoverageCanonical: append([]byte(nil), declaration.CoverageCanonical...),
			BlobID: blob.ID, ExcerptRef: projectionExcerptRef(declaration, identity.artifactID), CreatedAt: now,
		}
		reference := model.BackupAssetDerivedBlobReference{
			ID: identity.referenceID, BlobID: blob.ID, ArtifactID: artifact.ID, RecoveryPointID: job.RecoveryPointID,
			CatalogGenerationID: job.CatalogGenerationID, EntryID: job.EntryID,
			SourceFingerprint: job.SourceFingerprint, State: "active", CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.Create(&artifact).Error; err != nil {
			return fmt.Errorf("create Derived artifact: %w", err)
		}
		if err := tx.Create(&reference).Error; err != nil {
			return fmt.Errorf("create Derived blob reference: %w", err)
		}
		if err := tx.Model(&model.BackupAssetDerivedBlob{}).Where("id = ? AND state = ?", blob.ID, "active").
			Update("ref_count", gorm.Expr("ref_count + 1")).Error; err != nil {
			return fmt.Errorf("increment Derived blob reference count: %w", err)
		}
		if err := tx.Model(&model.BackupAssetProcessingUpload{}).Where("id = ? AND state = ?", upload.ID, "staged").
			Update("state", "committed").Error; err != nil {
			return fmt.Errorf("commit Derived upload: %w", err)
		}
	}
	if err := tx.Model(&model.BackupAssetProcessingGrant{}).Where("id = ? AND state = ?", grant.ID, GrantActive).
		Updates(map[string]any{"state": string(GrantClosed), "updated_at": now, "version": gorm.Expr("version + 1")}).Error; err != nil {
		return fmt.Errorf("close Sink grant: %w", err)
	}
	validatingRevision, err := ValidateTransition(TransitionRequest{
		From: ProcessingUploading, To: ProcessingValidating,
		CurrentRevision: job.TransitionRevision, ExpectedRevision: job.TransitionRevision,
	})
	if err != nil {
		return err
	}
	updates := map[string]any{
		"state": string(ProcessingValidating), "transition_revision": validatingRevision,
		"current_artifact_set_id": set.ID, "updated_at": now, "version": gorm.Expr("version + 1"),
	}
	if !manifest.ProjectionRequired {
		succeededRevision, err := ValidateTransition(TransitionRequest{
			From: ProcessingValidating, To: ProcessingSucceeded,
			CurrentRevision: validatingRevision, ExpectedRevision: validatingRevision,
		})
		if err != nil {
			return err
		}
		updates["state"] = string(ProcessingSucceeded)
		updates["transition_revision"] = succeededRevision
		updates["is_current"] = false
		updates["finished_at"] = now
		if err := finishAttemptTx(tx, attempt.ID, now, "succeeded", ""); err != nil {
			return err
		}
		if err := sink.leaseService.ReleaseTx(ctx, tx, request.RecoveryPointFence); err != nil {
			return err
		}
	}
	updated := tx.Model(&model.BackupAssetProcessingJob{}).Where("id = ? AND transition_revision = ?", job.ID, job.TransitionRevision).Updates(updates)
	if updated.Error != nil || updated.RowsAffected != 1 {
		return errors.Join(ErrRevisionConflict, updated.Error)
	}
	return nil
}

func (sink *ArtifactSink) completeProjectedManifestTx(
	ctx context.Context,
	tx *gorm.DB,
	request CommitManifestRequest,
	setID string,
	projectionRevision int64,
) error {
	if projectionRevision <= 0 {
		return ErrInvalidManifest
	}
	if err := sink.leaseService.ValidateFenceTx(ctx, tx, request.RecoveryPointFence); err != nil {
		return errors.Join(ErrManifestFenceLost, err)
	}
	var job model.BackupAssetProcessingJob
	result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", request.JobID).Limit(1).Find(&job)
	if result.Error != nil || result.RowsAffected != 1 || job.State != string(ProcessingValidating) || !job.IsCurrent ||
		job.CurrentAttemptID == nil || *job.CurrentAttemptID != request.AttemptID {
		return ErrManifestFenceLost
	}
	var attempt model.BackupAssetProcessingAttempt
	result = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND job_id = ? AND worker_id = ?",
		request.AttemptID, job.ID, request.WorkerID).Limit(1).Find(&attempt)
	if result.Error != nil || result.RowsAffected != 1 || attempt.State != "active" || !attempt.IsCurrent ||
		attempt.RecoveryPointLeaseID != request.RecoveryPointFence.LeaseID ||
		attempt.RecoveryPointAttemptID != request.RecoveryPointFence.AttemptID ||
		attempt.RecoveryPointFenceHash != hashFence(request.RecoveryPointFence.FenceToken) {
		return ErrManifestFenceLost
	}
	now := sink.utcNow()
	setUpdate := tx.Model(&model.BackupAssetDerivedArtifactSet{}).Where("id = ? AND job_id = ? AND attempt_id = ? AND state = ? AND projection_required = ? AND projection_published = ?",
		setID, job.ID, attempt.ID, "active", true, false).Updates(map[string]any{
		"projection_published": true, "projection_revision": projectionRevision, "updated_at": now,
	})
	if setUpdate.Error != nil {
		return fmt.Errorf("mark Derived projection published: %w", setUpdate.Error)
	}
	if setUpdate.RowsAffected != 1 {
		return ErrManifestFenceLost
	}
	revision, err := ValidateTransition(TransitionRequest{
		From: ProcessingValidating, To: ProcessingSucceeded,
		CurrentRevision: job.TransitionRevision, ExpectedRevision: job.TransitionRevision,
	})
	if err != nil {
		return err
	}
	updated := tx.Model(&model.BackupAssetProcessingJob{}).Where("id = ? AND transition_revision = ?", job.ID, job.TransitionRevision).
		Updates(map[string]any{
			"state": string(ProcessingSucceeded), "transition_revision": revision, "is_current": false,
			"finished_at": now, "updated_at": now, "version": gorm.Expr("version + 1"),
		})
	if updated.Error != nil || updated.RowsAffected != 1 {
		return ErrRevisionConflict
	}
	if err := finishAttemptTx(tx, request.AttemptID, now, "succeeded", ""); err != nil {
		return err
	}
	return sink.leaseService.ReleaseTx(ctx, tx, request.RecoveryPointFence)
}

func (sink *ArtifactSink) projectionCompletionMatches(ctx context.Context, jobID, attemptID, setID string, projectionRevision int64) (bool, error) {
	var job model.BackupAssetProcessingJob
	var attempt model.BackupAssetProcessingAttempt
	var set model.BackupAssetDerivedArtifactSet
	if err := sink.db.WithContext(ctx).Where("id = ?", jobID).Limit(1).Find(&job).Error; err != nil {
		return false, err
	}
	if err := sink.db.WithContext(ctx).Where("id = ? AND job_id = ?", attemptID, jobID).Limit(1).Find(&attempt).Error; err != nil {
		return false, err
	}
	if err := sink.db.WithContext(ctx).Where("id = ? AND job_id = ? AND attempt_id = ?", setID, jobID, attemptID).Limit(1).Find(&set).Error; err != nil {
		return false, err
	}
	return job.State == string(ProcessingSucceeded) && !job.IsCurrent && job.CurrentAttemptID != nil && *job.CurrentAttemptID == attemptID &&
		job.CurrentArtifactSetID != nil && *job.CurrentArtifactSetID == setID && attempt.State == "succeeded" && !attempt.IsCurrent &&
		set.State == "active" && set.ProjectionRequired && set.ProjectionPublished && set.ProjectionRevision == projectionRevision, nil
}

func (sink *ArtifactSink) retryManifestConflicts(ctx context.Context, operation func() error) error {
	var lastErr error
	for attempt := 0; attempt < 20; attempt++ {
		if err := operation(); err != nil {
			lastErr = err
			if !retryableCoordinatorConflict(err) {
				return err
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt+1) * time.Millisecond):
			}
			continue
		}
		return nil
	}
	return fmt.Errorf("reconcile Derived manifest after retries: %w", lastErr)
}

func validProjectionPublication(publication DerivedProjectionPublication, artifactSetID string) bool {
	return publication.ArtifactSetID == artifactSetID && backupasset.ValidateOpaqueID(publication.ArtifactSetID) == nil && publication.Revision > 0
}

func finishAttemptTx(tx *gorm.DB, attemptID string, now time.Time, state, outcome string) error {
	updated := tx.Model(&model.BackupAssetProcessingAttempt{}).Where("id = ? AND state = ? AND is_current = ?", attemptID, "active", true).
		Updates(map[string]any{
			"state": state, "outcome_code": outcome, "is_current": false, "finished_at": now, "updated_at": now,
		})
	if updated.Error != nil || updated.RowsAffected != 1 {
		return errors.Join(ErrManifestFenceLost, updated.Error)
	}
	return nil
}

func (sink *ArtifactSink) loadManifestJob(ctx context.Context, request CommitManifestRequest) (model.BackupAssetProcessingJob, WorkDescriptorV1, error) {
	var job model.BackupAssetProcessingJob
	result := sink.db.WithContext(ctx).Where("id = ?", request.JobID).Limit(1).Find(&job)
	if result.Error != nil || result.RowsAffected != 1 || job.State != string(ProcessingUploading) || !job.IsCurrent ||
		job.CurrentAttemptID == nil || *job.CurrentAttemptID != request.AttemptID {
		return job, WorkDescriptorV1{}, ErrInvalidManifest
	}
	descriptor, err := DecodeWorkDescriptorV1(job.DescriptorCanonical)
	if err != nil {
		return job, WorkDescriptorV1{}, ErrInvalidManifest
	}
	return job, descriptor, nil
}

func (sink *ArtifactSink) loadUploadDescriptor(ctx context.Context, jobID, attemptID string) (WorkDescriptorV1, error) {
	var job model.BackupAssetProcessingJob
	result := sink.db.WithContext(ctx).Where("id = ?", jobID).Limit(1).Find(&job)
	if result.Error != nil || result.RowsAffected != 1 || job.State != string(ProcessingUploading) || !job.IsCurrent ||
		job.CurrentAttemptID == nil || *job.CurrentAttemptID != attemptID {
		return WorkDescriptorV1{}, ErrInvalidArtifact
	}
	descriptor, err := DecodeWorkDescriptorV1(job.DescriptorCanonical)
	if err != nil {
		return WorkDescriptorV1{}, ErrInvalidArtifact
	}
	return descriptor, nil
}

func (sink *ArtifactSink) loadAndValidateUploads(ctx context.Context, request CommitManifestRequest, artifacts []ArtifactDeclaration) ([]model.BackupAssetProcessingUpload, error) {
	var uploads []model.BackupAssetProcessingUpload
	if err := sink.db.WithContext(ctx).Where("job_id = ? AND attempt_id = ? AND grant_id = ? AND state = ?",
		request.JobID, request.AttemptID, request.GrantID, "staged").Order("ordinal ASC").Find(&uploads).Error; err != nil {
		return nil, fmt.Errorf("load staged Derived uploads: %w", err)
	}
	if len(uploads) != len(artifacts) {
		return nil, ErrInvalidManifest
	}
	for index := range uploads {
		artifact := artifacts[index]
		upload := uploads[index]
		if upload.Ordinal != artifact.Ordinal || upload.Role != string(artifact.Role) || upload.MediaType != artifact.MediaType ||
			upload.DeclaredSize != artifact.PlaintextSize || upload.ActualSize != artifact.PlaintextSize ||
			upload.DeclaredDigest != artifact.PlaintextDigest || upload.ActualDigest != artifact.PlaintextDigest ||
			upload.Completeness != string(artifact.Completeness) || !bytes.Equal(upload.CoverageCanonical, artifact.CoverageCanonical) {
			return nil, ErrInvalidManifest
		}
	}
	return uploads, nil
}

func (sink *ArtifactSink) validateSinkGrant(ctx context.Context, jobID, attemptID, workerID, grantID string) error {
	return sink.grants.retryConflicts(ctx, func() error {
		var grant model.BackupAssetProcessingGrant
		result := sink.db.WithContext(ctx).Where("id = ?", grantID).Limit(1).Find(&grant)
		if result.Error != nil {
			if retryableCoordinatorConflict(result.Error) {
				return result.Error
			}
			return ErrGrantDenied
		}
		if result.RowsAffected != 1 || grant.Kind != string(GrantSink) || grant.State != string(GrantActive) ||
			grant.JobID != jobID || grant.AttemptID != attemptID || grant.WorkerID != workerID || !sink.utcNow().Before(grant.ExpiresAt.UTC()) {
			return ErrGrantDenied
		}
		return nil
	})
}

func (sink *ArtifactSink) admitArtifact(ctx context.Context, request UploadArtifactRequest) (model.BackupAssetProcessingUpload, error) {
	uploadID, err := backupasset.NewOpaqueID()
	if err != nil {
		return model.BackupAssetProcessingUpload{}, err
	}
	now := sink.utcNow()
	row := model.BackupAssetProcessingUpload{
		ID: uploadID, JobID: request.JobID, AttemptID: request.AttemptID, GrantID: request.GrantID,
		Ordinal: request.Artifact.Ordinal, Role: string(request.Artifact.Role), MediaType: request.Artifact.MediaType,
		DeclaredSize: request.Artifact.PlaintextSize, DeclaredDigest: request.Artifact.PlaintextDigest,
		ActualSize: 0, ActualDigest: "", Completeness: string(request.Artifact.Completeness),
		CoverageCanonical: append([]byte(nil), request.Artifact.CoverageCanonical...),
		StagingID:         uploadID, State: "reserved", CreatedAt: now, UpdatedAt: now,
	}
	err = sink.grants.retryConflicts(ctx, func() error {
		return sink.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var grant model.BackupAssetProcessingGrant
			result := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("id = ? AND state = ?", request.GrantID, GrantActive).Limit(1).Find(&grant)
			if result.Error != nil || result.RowsAffected != 1 || grant.JobID != request.JobID ||
				grant.AttemptID != request.AttemptID || grant.WorkerID != request.WorkerID {
				return ErrGrantDenied
			}
			states := []string{"reserved", "streaming", "staged", "committed"}
			var count int64
			if err := tx.WithContext(ctx).Model(&model.BackupAssetProcessingUpload{}).
				Where("grant_id = ? AND state IN ?", request.GrantID, states).Count(&count).Error; err != nil {
				return err
			}
			if count >= int64(sink.config.MaxArtifacts) {
				return ErrInvalidArtifact
			}
			var total int64
			if err := tx.WithContext(ctx).Model(&model.BackupAssetProcessingUpload{}).
				Where("grant_id = ? AND state IN ?", request.GrantID, states).
				Select("COALESCE(SUM(declared_size), 0)").Scan(&total).Error; err != nil {
				return err
			}
			if total < 0 || request.Artifact.PlaintextSize > sink.config.MaxTotalBytes-total {
				return ErrDerivedQuotaExceeded
			}
			if err := tx.WithContext(ctx).Create(&row).Error; err != nil {
				return ErrInvalidArtifact
			}
			return nil
		})
	})
	if err != nil {
		if errors.Is(err, ErrDerivedQuotaExceeded) {
			return model.BackupAssetProcessingUpload{}, ErrDerivedQuotaExceeded
		}
		if errors.Is(err, ErrGrantDenied) {
			return model.BackupAssetProcessingUpload{}, ErrGrantDenied
		}
		return model.BackupAssetProcessingUpload{}, ErrInvalidArtifact
	}
	return row, nil
}

func (sink *ArtifactSink) rejectInvisibleUploads(ctx context.Context, uploads []model.BackupAssetProcessingUpload, failure string) {
	now := sink.utcNow()
	for _, upload := range uploads {
		_ = sink.db.WithContext(ctx).Model(&model.BackupAssetProcessingUpload{}).Where("id = ? AND state = ?", upload.ID, "staged").
			Updates(map[string]any{"state": "orphaned", "failure_code": failure, "updated_at": now}).Error
		_ = sink.store.discardBlobIfUnreferenced(ctx, upload.StagingID)
	}
}

func validateArtifactDeclaration(value ArtifactDeclaration, config ArtifactSinkConfig) error {
	if value.Ordinal < 0 || value.Ordinal >= config.MaxArtifacts || value.PlaintextSize < 0 ||
		value.PlaintextSize > config.MaxArtifactBytes || !lowerHex(value.PlaintextDigest, 64) ||
		(value.Completeness != ArtifactComplete && value.Completeness != ArtifactPartial) ||
		!validArtifactMedia(value.Role, value.MediaType) || !validCoverageCanonical(value.CoverageCanonical) {
		return ErrInvalidArtifact
	}
	return nil
}

func validArtifactMedia(role ArtifactRole, mediaType string) bool {
	switch role {
	case ArtifactRoleNoop:
		return mediaType == "application/octet-stream"
	case ArtifactRoleContent, ArtifactRoleOCR:
		return mediaType == "text/plain" || mediaType == "application/json" || mediaType == "application/pdf" ||
			mediaType == "application/octet-stream" || mediaType == "video/mp4" || mediaType == "video/webm" ||
			mediaType == "audio/mpeg" || mediaType == "audio/mp4" || mediaType == "audio/ogg"
	case ArtifactRoleThumbnail:
		return mediaType == "image/png" || mediaType == "image/jpeg" || mediaType == "image/webp"
	case ArtifactRoleMetadata:
		return mediaType == "application/json"
	default:
		return false
	}
}

func validArtifactMediaForDescriptor(descriptor WorkDescriptorV1, role ArtifactRole, mediaType string) bool {
	if !validArtifactMedia(role, mediaType) || mediaType == "text/html" || mediaType == "image/svg+xml" || mediaType == "application/xhtml+xml" {
		return false
	}
	if descriptor.Capability == "noop" && descriptor.OutputProfile == "noop.v1" {
		switch role {
		case ArtifactRoleNoop:
			return mediaType == "application/octet-stream"
		case ArtifactRoleContent, ArtifactRoleOCR:
			return mediaType == "text/plain" || mediaType == "application/json"
		case ArtifactRoleThumbnail:
			return mediaType == "image/png" || mediaType == "image/jpeg" || mediaType == "image/webp"
		case ArtifactRoleMetadata:
			return mediaType == "application/json"
		default:
			return false
		}
	}
	profile, ok := capabilityspec.Lookup(descriptor.Capability, descriptor.OutputProfile, true)
	if !ok || profile.CapabilitySchema != descriptor.CapabilitySchema {
		return false
	}
	for _, output := range profile.Outputs {
		if output.Role != string(role) {
			continue
		}
		for _, allowed := range output.MediaTypes {
			if allowed == mediaType {
				return true
			}
		}
	}
	return false
}

type coverageV1 struct {
	SchemaVersion int    `json:"schema_version"`
	Kind          string `json:"kind"`
}

func validCoverageCanonical(payload []byte) bool {
	if len(payload) == 0 || len(payload) > 4096 || !json.Valid(payload) || rejectDuplicateJSONMembers(payload) != nil {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var value coverageV1
	if decoder.Decode(&value) != nil || ensureJSONEOF(decoder) != nil || value.SchemaVersion != 1 || value.Kind != "all" {
		return false
	}
	canonical, err := json.Marshal(value)
	return err == nil && bytes.Equal(canonical, payload)
}

func validateManifestArtifacts(artifacts []ArtifactDeclaration, config ArtifactSinkConfig) error {
	total := int64(0)
	for index, artifact := range artifacts {
		if artifact.Ordinal != index || validateArtifactDeclaration(artifact, config) != nil ||
			artifact.PlaintextSize > config.MaxTotalBytes-total {
			return ErrInvalidManifest
		}
		total += artifact.PlaintextSize
	}
	return nil
}

func cloneAndSortArtifacts(values []ArtifactDeclaration) []ArtifactDeclaration {
	result := make([]ArtifactDeclaration, len(values))
	for index, value := range values {
		value.CoverageCanonical = append([]byte(nil), value.CoverageCanonical...)
		result[index] = value
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Ordinal < result[right].Ordinal })
	return result
}

func computeManifestDigest(artifacts []ArtifactDeclaration) string {
	digest := sha256.New()
	writeManifestString(digest, "xirang.derived.manifest.v1")
	for _, artifact := range artifacts {
		writeManifestInt(digest, int64(artifact.Ordinal))
		writeManifestString(digest, string(artifact.Role))
		writeManifestString(digest, artifact.MediaType)
		writeManifestInt(digest, artifact.PlaintextSize)
		writeManifestString(digest, artifact.PlaintextDigest)
		writeManifestString(digest, string(artifact.Completeness))
		writeManifestString(digest, string(artifact.CoverageCanonical))
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func writeManifestString(destination io.Writer, value string) {
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(value)))
	_, _ = destination.Write(length[:])
	_, _ = destination.Write([]byte(value))
}

func writeManifestInt(destination io.Writer, value int64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], uint64(value))
	_, _ = destination.Write(encoded[:])
}

func manifestNeedsProjection(descriptor WorkDescriptorV1, artifacts []ArtifactDeclaration) bool {
	if descriptor.Capability == capabilityspec.CapabilitySecretClassify {
		return true
	}
	for _, artifact := range artifacts {
		if artifact.MediaType == "text/plain" && (artifact.Role == ArtifactRoleContent || artifact.Role == ArtifactRoleOCR) {
			return true
		}
	}
	return false
}

func newManifestArtifactIdentities(count int) ([]manifestArtifactIdentity, error) {
	identities := make([]manifestArtifactIdentity, count)
	for index := range identities {
		artifactID, err := backupasset.NewOpaqueID()
		if err != nil {
			return nil, err
		}
		referenceID, err := backupasset.NewOpaqueID()
		if err != nil {
			return nil, err
		}
		identities[index] = manifestArtifactIdentity{artifactID: artifactID, referenceID: referenceID}
	}
	return identities, nil
}

func projectionExcerptRef(declaration ArtifactDeclaration, artifactID string) string {
	if declaration.MediaType == "text/plain" && (declaration.Role == ArtifactRoleContent || declaration.Role == ArtifactRoleOCR) {
		return artifactID
	}
	return ""
}

type secretClassificationMetadataV1 struct {
	SchemaVersion int      `json:"schema_version"`
	Sensitivity   string   `json:"sensitivity"`
	Categories    []string `json:"categories"`
}

func parseSecretClassificationEvidence(
	payload []byte,
	completeness ArtifactCompleteness,
	artifactID string,
) (DerivedClassificationEvidence, error) {
	if len(payload) == 0 || len(payload) > 256<<10 || backupasset.ValidateOpaqueID(artifactID) != nil ||
		!json.Valid(payload) || rejectDuplicateJSONMembers(payload) != nil {
		return DerivedClassificationEvidence{}, ErrInvalidManifest
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var metadata secretClassificationMetadataV1
	if decoder.Decode(&metadata) != nil || ensureJSONEOF(decoder) != nil || metadata.SchemaVersion != 1 || metadata.Categories == nil {
		return DerivedClassificationEvidence{}, ErrInvalidManifest
	}
	canonical, err := json.Marshal(metadata)
	if err != nil || !bytes.Equal(canonical, payload) {
		return DerivedClassificationEvidence{}, ErrInvalidManifest
	}
	categories := make([]string, len(metadata.Categories))
	copy(categories, metadata.Categories)
	evidence := DerivedClassificationEvidence{ArtifactID: artifactID, Categories: categories}
	switch metadata.Sensitivity {
	case string(DerivedSensitivityPublic):
		if completeness != ArtifactComplete || len(metadata.Categories) != 0 {
			return DerivedClassificationEvidence{}, ErrInvalidManifest
		}
		evidence.Sensitivity = DerivedSensitivityPublic
	case string(DerivedSensitivitySecret):
		if completeness != ArtifactComplete || len(metadata.Categories) != 1 || metadata.Categories[0] != "credential_pattern" {
			return DerivedClassificationEvidence{}, ErrInvalidManifest
		}
		evidence.Sensitivity = DerivedSensitivitySecret
	case string(DerivedSensitivityUnknown):
		if completeness != ArtifactPartial || len(metadata.Categories) != 0 {
			return DerivedClassificationEvidence{}, ErrInvalidManifest
		}
		evidence.Sensitivity = DerivedSensitivityUnknown
	default:
		return DerivedClassificationEvidence{}, ErrInvalidManifest
	}
	return evidence, nil
}

func (sink *ArtifactSink) prepareProjectionEvidence(
	ctx context.Context,
	descriptor WorkDescriptorV1,
	artifacts []ArtifactDeclaration,
	uploads []model.BackupAssetProcessingUpload,
	identities []manifestArtifactIdentity,
) ([]DerivedProjectionField, *DerivedClassificationEvidence, error) {
	if descriptor.Capability != capabilityspec.CapabilitySecretClassify {
		fields, err := sink.prepareProjectionFields(ctx, artifacts, uploads, identities)
		return fields, nil, err
	}
	if len(artifacts) != 1 || len(uploads) != 1 || len(identities) != 1 ||
		artifacts[0].Role != ArtifactRoleMetadata || artifacts[0].MediaType != "application/json" ||
		artifacts[0].PlaintextSize > 256<<10 {
		return nil, nil, ErrInvalidManifest
	}
	evidence, err := sink.classificationEvidenceFromBlob(
		ctx, uploads[0].StagingID, identities[0].artifactID, artifacts[0].Completeness, artifacts[0].PlaintextSize,
	)
	if err != nil {
		return nil, nil, err
	}
	return []DerivedProjectionField{}, &evidence, nil
}

func (sink *ArtifactSink) loadProjectionEvidence(
	ctx context.Context,
	descriptor WorkDescriptorV1,
	artifactSetID string,
) ([]DerivedProjectionField, *DerivedClassificationEvidence, error) {
	if descriptor.Capability != capabilityspec.CapabilitySecretClassify {
		fields, err := sink.loadProjectionFields(ctx, artifactSetID)
		return fields, nil, err
	}
	var artifacts []model.BackupAssetDerivedArtifact
	if err := sink.db.WithContext(ctx).Where("artifact_set_id = ?", artifactSetID).Order("ordinal ASC").Find(&artifacts).Error; err != nil {
		return nil, nil, fmt.Errorf("load pending classification evidence: %w", err)
	}
	if len(artifacts) != 1 || artifacts[0].Role != string(ArtifactRoleMetadata) || artifacts[0].MediaType != "application/json" ||
		artifacts[0].PlaintextSize > 256<<10 {
		return nil, nil, ErrInvalidManifest
	}
	evidence, err := sink.classificationEvidenceFromBlob(
		ctx, artifacts[0].BlobID, artifacts[0].ID, ArtifactCompleteness(artifacts[0].Completeness), artifacts[0].PlaintextSize,
	)
	if err != nil {
		return nil, nil, err
	}
	return []DerivedProjectionField{}, &evidence, nil
}

func (sink *ArtifactSink) classificationEvidenceFromBlob(
	ctx context.Context,
	blobID string,
	artifactID string,
	completeness ArtifactCompleteness,
	plaintextSize int64,
) (DerivedClassificationEvidence, error) {
	if sink == nil || sink.store == nil || backupasset.ValidateOpaqueID(blobID) != nil || plaintextSize < 0 || plaintextSize > 256<<10 {
		return DerivedClassificationEvidence{}, ErrInvalidManifest
	}
	var plaintext bytes.Buffer
	if err := sink.store.readBlob(ctx, blobID, &plaintext); err != nil || int64(plaintext.Len()) != plaintextSize {
		return DerivedClassificationEvidence{}, errors.Join(ErrInvalidManifest, err)
	}
	return parseSecretClassificationEvidence(plaintext.Bytes(), completeness, artifactID)
}

func (sink *ArtifactSink) prepareProjectionFields(
	ctx context.Context,
	artifacts []ArtifactDeclaration,
	uploads []model.BackupAssetProcessingUpload,
	identities []manifestArtifactIdentity,
) ([]DerivedProjectionField, error) {
	if len(artifacts) != len(uploads) || len(artifacts) != len(identities) {
		return nil, ErrInvalidManifest
	}
	fields := make([]DerivedProjectionField, 0, 2)
	seen := make(map[ArtifactRole]bool, 2)
	for index, artifact := range artifacts {
		if artifact.MediaType != "text/plain" || artifact.Role != ArtifactRoleContent && artifact.Role != ArtifactRoleOCR {
			continue
		}
		if seen[artifact.Role] {
			return nil, ErrInvalidManifest
		}
		seen[artifact.Role] = true
		field, err := sink.projectionFieldFromBlob(ctx, uploads[index].StagingID, identities[index].artifactID, artifact.Role, artifact.Completeness)
		if err != nil {
			return nil, err
		}
		fields = append(fields, field)
	}
	if len(fields) == 0 {
		return nil, ErrInvalidManifest
	}
	return fields, nil
}

func (sink *ArtifactSink) loadProjectionFields(ctx context.Context, artifactSetID string) ([]DerivedProjectionField, error) {
	var artifacts []model.BackupAssetDerivedArtifact
	if err := sink.db.WithContext(ctx).Where(
		"artifact_set_id = ? AND media_type = ? AND role IN ?", artifactSetID, "text/plain", []string{string(ArtifactRoleContent), string(ArtifactRoleOCR)},
	).Order("ordinal ASC").Find(&artifacts).Error; err != nil {
		return nil, fmt.Errorf("load pending projection artifacts: %w", err)
	}
	fields := make([]DerivedProjectionField, 0, len(artifacts))
	seen := make(map[ArtifactRole]bool, 2)
	for _, artifact := range artifacts {
		role := ArtifactRole(artifact.Role)
		completeness := ArtifactCompleteness(artifact.Completeness)
		if seen[role] || backupasset.ValidateOpaqueID(artifact.ID) != nil {
			return nil, ErrInvalidManifest
		}
		seen[role] = true
		field, err := sink.projectionFieldFromBlob(ctx, artifact.BlobID, artifact.ID, role, completeness)
		if err != nil {
			return nil, err
		}
		fields = append(fields, field)
	}
	if len(fields) == 0 {
		return nil, ErrInvalidManifest
	}
	return fields, nil
}

func (sink *ArtifactSink) projectionFieldFromBlob(
	ctx context.Context,
	blobID string,
	artifactID string,
	role ArtifactRole,
	completeness ArtifactCompleteness,
) (DerivedProjectionField, error) {
	if backupasset.ValidateOpaqueID(blobID) != nil || backupasset.ValidateOpaqueID(artifactID) != nil ||
		(role != ArtifactRoleContent && role != ArtifactRoleOCR) ||
		(completeness != ArtifactComplete && completeness != ArtifactPartial) {
		return DerivedProjectionField{}, ErrInvalidManifest
	}
	var plaintext bytes.Buffer
	if err := sink.store.readBlob(ctx, blobID, &plaintext); err != nil {
		return DerivedProjectionField{}, errors.Join(ErrInvalidManifest, err)
	}
	if plaintext.Len() > 16<<20 || !utf8.Valid(plaintext.Bytes()) || bytes.IndexByte(plaintext.Bytes(), 0) >= 0 {
		return DerivedProjectionField{}, ErrInvalidManifest
	}
	frequencies := make(map[string]int)
	for _, term := range strings.Fields(plaintext.String()) {
		if len(term) > 4096 || utf8.RuneCountInString(term) > 2048 || len(frequencies) >= 2048 && frequencies[term] == 0 {
			return DerivedProjectionField{}, ErrInvalidManifest
		}
		frequencies[term]++
		if frequencies[term] > 1_000_000 {
			return DerivedProjectionField{}, ErrInvalidManifest
		}
	}
	terms := make([]DerivedProjectionTerm, 0, len(frequencies))
	for term, frequency := range frequencies {
		terms = append(terms, DerivedProjectionTerm{Term: term, Frequency: frequency})
	}
	sort.Slice(terms, func(left, right int) bool { return terms[left].Term < terms[right].Term })
	return DerivedProjectionField{
		ExcerptArtifactID: artifactID, Role: role, Completeness: completeness, Terms: terms,
	}, nil
}

func validUploadIdentity(request UploadArtifactRequest) bool {
	return backupasset.ValidateOpaqueID(request.JobID) == nil && backupasset.ValidateOpaqueID(request.AttemptID) == nil &&
		backupasset.ValidateOpaqueID(request.WorkerID) == nil && backupasset.ValidateOpaqueID(request.GrantID) == nil
}

func validManifestIdentity(request CommitManifestRequest) bool {
	return backupasset.ValidateOpaqueID(request.JobID) == nil && backupasset.ValidateOpaqueID(request.AttemptID) == nil &&
		backupasset.ValidateOpaqueID(request.WorkerID) == nil && backupasset.ValidateOpaqueID(request.GrantID) == nil &&
		strings.TrimSpace(request.SecurityPolicyRevision) == request.SecurityPolicyRevision && request.SecurityPolicyRevision != "" &&
		request.RecoveryPointFence.HolderType == backupasset.LeaseHolderProcessingJob
}

func (sink *ArtifactSink) utcNow() time.Time { return sink.now().UTC() }
