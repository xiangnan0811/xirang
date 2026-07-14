package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type publicationManifestClaim struct {
	point       model.RecoveryPoint
	lineage     backupasset.PublicationLineageV1
	consistency backupasset.PublicationConsistencyV1
	locator     resticPointLocatorV1
	lease       backupasset.Lease
}

type publicationPreparingClaim struct {
	point       model.RecoveryPoint
	lineage     backupasset.PublicationLineageV1
	consistency backupasset.PublicationConsistencyV1
	lease       backupasset.Lease
}

type manifestCommitEvidenceV1 struct {
	Version            int                      `json:"version"`
	Provider           backupasset.ProviderKind `json:"provider"`
	RepositoryIdentity string                   `json:"repository_identity"`
	NativePointID      string                   `json:"native_point_id"`
	CaptureStartedAt   time.Time                `json:"capture_started_at"`
	CaptureFinishedAt  time.Time                `json:"capture_finished_at"`
	FilesProcessed     uint64                   `json:"files_processed"`
	LogicalBytes       uint64                   `json:"logical_bytes"`
	ObservedTags       [2]string                `json:"observed_tags"`
	ObservedTagDigest  string                   `json:"observed_tag_digest"`
}

var (
	errStoredSummaryMissing = errors.New("stored Restic summary is missing")
	errStoredSummaryInvalid = errors.New("stored Restic summary is invalid")
)

// ProcessPoint handles one durable publication record. It deliberately takes
// no long-running database transaction: the claim is fenced and committed
// first, manifest enumeration happens outside it, and the final state change
// validates the same fence in its own short transaction.
func (service *PublicationService) ProcessPoint(ctx context.Context, pointID string) (publication.Outcome, error) {
	if service == nil || service.db == nil || backupasset.ValidateOpaqueID(pointID) != nil {
		return publication.Outcome{}, fmt.Errorf("%w: invalid publication point", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if outcome, expired, err := service.expireAtDeadline(ctx, pointID); err != nil {
		return publication.Outcome{}, err
	} else if expired {
		return outcome, nil
	}

	var state string
	if err := service.db.WithContext(ctx).Model(&model.RecoveryPoint{}).Select("state").Where("id = ?", pointID).Take(&state).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return publication.Outcome{}, fmt.Errorf("%w: publication point", backupasset.ErrNotFound)
		}
		return publication.Outcome{}, fmt.Errorf("load publication point state: %w", err)
	}
	switch backupasset.RecoveryPointState(state) {
	case backupasset.RecoveryPointVerifying:
		return service.processVerifyingPoint(ctx, pointID)
	case backupasset.RecoveryPointPreparing:
		return service.processPreparingPoint(ctx, pointID)
	default:
		return publication.Outcome{}, fmt.Errorf("%w: publication point is not reconcilable", backupasset.ErrConflict)
	}
}

// expireAtDeadline is the only publication terminalizer that runs without a
// current fence. The immutable point deadline makes it safe: no future lease
// acquisition or renewal can make an elapsed point live again.
func (service *PublicationService) expireAtDeadline(ctx context.Context, pointID string) (publication.Outcome, bool, error) {
	if service == nil || service.db == nil || backupasset.ValidateOpaqueID(pointID) != nil {
		return publication.Outcome{}, false, fmt.Errorf("%w: invalid publication deadline request", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}

	now := service.now().UTC()
	var outcome publication.Outcome
	expired := false
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var point model.RecoveryPoint
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&point, "id = ?", pointID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: publication point", backupasset.ErrNotFound)
			}
			return fmt.Errorf("lock deadline publication point: %w", err)
		}
		state := backupasset.RecoveryPointState(point.State)
		if state != backupasset.RecoveryPointPreparing && state != backupasset.RecoveryPointVerifying {
			return nil
		}
		lineage, err := backupasset.DecodePublicationLineage(point.LineageJSON)
		if err != nil {
			return err
		}
		if now.Before(lineage.PointDeadlineAt.UTC()) {
			return nil
		}

		var latest model.RecoveryPointLease
		leaseResult := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("recovery_point_id = ?", point.ID).
			Order("created_at DESC, id DESC").
			Limit(1).
			Find(&latest)
		if leaseResult.Error != nil {
			return fmt.Errorf("lock latest publication lease for deadline: %w", leaseResult.Error)
		}
		if leaseResult.RowsAffected == 1 && !latest.AbsoluteDeadline.IsZero() && !latest.AbsoluteDeadline.UTC().Equal(lineage.PointDeadlineAt.UTC()) {
			return fmt.Errorf("%w: publication lease changed immutable point deadline", backupasset.ErrConflict)
		}
		var live int64
		if err := tx.WithContext(ctx).Model(&model.RecoveryPointLease{}).
			Where("recovery_point_id = ? AND status = ? AND lease_expires_at > ? AND absolute_deadline > ?", point.ID, backupasset.LeaseActive, now, now).
			Count(&live).Error; err != nil {
			return fmt.Errorf("check live publication lease for deadline: %w", err)
		}
		if live != 0 {
			return nil
		}

		consistency, err := backupasset.DecodePublicationConsistency(point.ConsistencyJSON)
		if err != nil {
			return err
		}
		code := backupasset.FailurePublicationDeadlineExceeded
		if consistency.FirstMissingObservedAt != nil {
			code = backupasset.FailureSnapshotMissingAtDeadline
		}
		priorConsistency := point.ConsistencyJSON
		consistency.Completion = ""
		consistency.Code = code
		encodedConsistency, err := backupasset.EncodePublicationConsistency(consistency)
		if err != nil {
			return err
		}
		result := tx.WithContext(ctx).Model(&model.RecoveryPoint{}).
			Where("id = ? AND state = ? AND consistency_json = ?", point.ID, point.State, priorConsistency).
			Updates(map[string]any{
				"state":            backupasset.RecoveryPointFailed,
				"consistency_json": encodedConsistency,
				"updated_at":       now,
			})
		if result.Error != nil {
			return fmt.Errorf("expire publication point at deadline: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("%w: publication deadline compare-and-swap lost", backupasset.ErrConflict)
		}
		outcome = publication.Outcome{
			RepositoryID: point.RepositoryID, RecoveryPointID: point.ID, TaskID: lineage.TaskID, TaskRunID: lineage.TaskRunID,
			State: backupasset.RecoveryPointFailed, Code: code,
		}
		expired = true
		return nil
	})
	if err != nil {
		return publication.Outcome{}, false, err
	}
	return outcome, expired, nil
}

func (service *PublicationService) ListCandidates(ctx context.Context, limit int) ([]string, error) {
	if service == nil || service.db == nil || limit <= 0 {
		return nil, fmt.Errorf("%w: invalid publication candidate request", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	now := service.now().UTC()
	var points []model.RecoveryPoint
	scanLimit := limit * 10
	if scanLimit < limit {
		scanLimit = limit
	}
	if err := service.db.WithContext(ctx).
		Where("semantics = ? AND state IN ?", backupasset.PointNativeSnapshot, []string{string(backupasset.RecoveryPointPreparing), string(backupasset.RecoveryPointVerifying)}).
		Order("updated_at ASC, id ASC").Limit(scanLimit).Find(&points).Error; err != nil {
		return nil, fmt.Errorf("list publication candidates: %w", err)
	}
	result := make([]string, 0, limit)
	for _, point := range points {
		lineage, err := backupasset.DecodePublicationLineage(point.LineageJSON)
		if err != nil {
			return nil, err
		}
		if lineage.PublicationMode != string(backupasset.PublicationNativeSnapshot) {
			return nil, fmt.Errorf("%w: non-Restic publication candidate", backupasset.ErrInvalidState)
		}
		consistency, err := backupasset.DecodePublicationConsistency(point.ConsistencyJSON)
		if err != nil {
			return nil, err
		}
		if consistency.LastAttemptAt != nil && now.Before(consistency.LastAttemptAt.Add(publicationBackoff(consistency.AttemptCount))) && now.Before(lineage.PointDeadlineAt) {
			continue
		}
		var live int64
		if err := service.db.WithContext(ctx).Model(&model.RecoveryPointLease{}).
			Where("recovery_point_id = ? AND status = ? AND lease_expires_at > ?", point.ID, backupasset.LeaseActive, now).
			Count(&live).Error; err != nil {
			return nil, fmt.Errorf("check candidate publication lease: %w", err)
		}
		if live > 0 {
			continue
		}
		result = append(result, point.ID)
		if len(result) == limit {
			break
		}
	}
	return result, nil
}

func (service *PublicationService) HasUnresolvedPublication(ctx context.Context) (bool, error) {
	if service == nil || service.db == nil {
		return false, fmt.Errorf("%w: publication readiness dependencies are unavailable", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var points []model.RecoveryPoint
	if err := service.db.WithContext(ctx).
		Where("semantics = ? AND state IN ?", backupasset.PointNativeSnapshot, []string{string(backupasset.RecoveryPointPreparing), string(backupasset.RecoveryPointVerifying)}).
		Find(&points).Error; err != nil {
		return false, fmt.Errorf("list unresolved publications: %w", err)
	}
	for _, point := range points {
		lineage, err := backupasset.DecodePublicationLineage(point.LineageJSON)
		if err != nil {
			return false, err
		}
		if lineage.PublicationMode != string(backupasset.PublicationNativeSnapshot) {
			return false, fmt.Errorf("%w: unresolved publication has invalid lineage", backupasset.ErrInvalidState)
		}
		if _, err := backupasset.DecodePublicationConsistency(point.ConsistencyJSON); err != nil {
			return false, err
		}
	}
	return len(points) > 0, nil
}

func publicationBackoff(attempt uint64) time.Duration {
	if attempt <= 1 {
		return 30 * time.Second
	}
	shift := attempt - 1
	if shift > 5 {
		shift = 5
	}
	delay := 30 * time.Second * time.Duration(uint64(1)<<shift)
	if delay > 15*time.Minute {
		return 15 * time.Minute
	}
	return delay
}

func (service *PublicationService) processPreparingPoint(ctx context.Context, pointID string) (publication.Outcome, error) {
	token, err := service.admission.Acquire(ctx, publication.OperationReconcile)
	if err != nil {
		return publication.Outcome{}, err
	}
	defer func() { _ = token.Close() }()
	if token.Mode() != publication.AdmissionManaged {
		return publication.Outcome{}, fmt.Errorf("%w: preparing reconciliation is not admitted", backupasset.ErrForbidden)
	}
	leaseConfig, err := service.foundation.LeaseConfig()
	if err != nil {
		return publication.Outcome{}, err
	}
	claim, err := service.claimPreparingPoint(ctx, pointID)
	if err != nil {
		return publication.Outcome{}, err
	}
	audit, err := publicationSystemAuditContext(claim.point.ID, claim.consistency.PublicationRevision, publication.OperationReconcile)
	if err != nil {
		return publication.Outcome{}, err
	}
	attempt, err := service.rebuildPreparingAttempt(ctx, claim, audit)
	if err != nil {
		return publication.Outcome{}, err
	}
	publisher, err := service.registry.ResticPublisher(backupasset.ProviderRestic)
	if err != nil {
		return publication.Outcome{}, err
	}
	workCtx, stopHeartbeat := service.startPublicationHeartbeat(ctx, attempt.Fence, leaseConfig)
	service.metrics.ObserveAttempt(backupasset.ProviderRestic, publication.StageReconciliation)
	observations, lookupErr := publisher.LookupAttempt(workCtx, attempt)
	workCause := context.Cause(workCtx)
	stopHeartbeat()
	if workCause != nil && lookupErr == nil {
		lookupErr = workCause
	}
	if lookupErr != nil {
		if errors.Is(lookupErr, backupasset.ErrLeaseFenceLost) || errors.Is(lookupErr, backupasset.ErrLeaseDeadlineExceeded) {
			return publication.Outcome{}, lookupErr
		}
		service.metrics.ObserveReconcileMatch(publication.ReconcileMatchTransient)
		return pendingPreparingOutcome(claim, attempt), nil
	}
	if len(observations) == 0 {
		service.metrics.ObserveReconcileMatch(publication.ReconcileMatchZero)
		config, err := service.foundation.PublicationConfig()
		if err != nil {
			return publication.Outcome{}, err
		}
		outcome, graceReported, err := service.recordMissingAttempt(ctx, claim, attempt, config.MissingGrace)
		if err != nil {
			return publication.Outcome{}, err
		}
		if graceReported {
			service.metrics.ObserveOutcome(backupasset.ProviderRestic, publication.StageReconciliation, backupasset.PublicationOutcomeCode(backupasset.FailureManifestUnavailable))
			if err := service.writePublicationAudit(ctx, audit, backupasset.AuditActionRecoveryPointPublicationReconcile, backupasset.AuditOutcomeFailure, &attempt, publication.StageReconciliation, backupasset.RecoveryPointPreparing, string(backupasset.FailureManifestUnavailable), backupasset.FailureManifestUnavailable); err != nil {
				service.metrics.ObserveAuditFailure(publication.StageReconciliation)
			}
		}
		return outcome, nil
	}
	if len(observations) > 1 {
		service.metrics.ObserveReconcileMatch(publication.ReconcileMatchMultiple)
		outcome, err := service.failPreparing(ctx, claim, attempt, backupasset.FailureAmbiguousRunTags)
		if err != nil {
			return publication.Outcome{}, err
		}
		service.metrics.ObserveOutcome(backupasset.ProviderRestic, publication.StageReconciliation, backupasset.PublicationOutcomeCode(outcome.Code))
		if err := service.writePublicationAudit(ctx, audit, backupasset.AuditActionRecoveryPointPublicationReconcile, backupasset.AuditOutcomeFailure, &attempt, publication.StageReconciliation, outcome.State, string(outcome.Code), outcome.Code); err != nil {
			service.metrics.ObserveAuditFailure(publication.StageReconciliation)
		}
		return outcome, nil
	}
	observation := observations[0]
	if observation.RepositoryIdentity != attempt.RepositoryIdentity || !validFullNativeID(observation.NativePointID) || observation.SnapshotTime.IsZero() {
		service.metrics.ObserveReconcileMatch(publication.ReconcileMatchRewritten)
		outcome, err := service.failPreparing(ctx, claim, attempt, backupasset.FailureProviderSnapshotRewritten)
		if err != nil {
			return publication.Outcome{}, err
		}
		service.metrics.ObserveOutcome(backupasset.ProviderRestic, publication.StageReconciliation, backupasset.PublicationOutcomeCode(outcome.Code))
		if err := service.writePublicationAudit(ctx, audit, backupasset.AuditActionRecoveryPointPublicationReconcile, backupasset.AuditOutcomeFailure, &attempt, publication.StageReconciliation, outcome.State, string(outcome.Code), outcome.Code); err != nil {
			service.metrics.ObserveAuditFailure(publication.StageReconciliation)
		}
		return outcome, nil
	}
	if _, err := exactAttemptObservation(attempt, observations); err != nil {
		service.metrics.ObserveReconcileMatch(publication.ReconcileMatchRewritten)
		outcome, quarantineErr := service.quarantinePreparingObservation(ctx, claim, attempt, observation, backupasset.FailureProviderSnapshotRewritten)
		if quarantineErr != nil {
			return publication.Outcome{}, quarantineErr
		}
		service.metrics.ObserveOutcome(backupasset.ProviderRestic, publication.StageReconciliation, backupasset.PublicationOutcomeCode(outcome.Code))
		if auditErr := service.writePublicationAudit(ctx, audit, backupasset.AuditActionRecoveryPointPublicationReconcile, backupasset.AuditOutcomeFailure, &attempt, publication.StageReconciliation, outcome.State, string(outcome.Code), outcome.Code); auditErr != nil {
			service.metrics.ObserveAuditFailure(publication.StageReconciliation)
		}
		return outcome, nil
	}
	if claim.consistency.Completion != backupasset.CompletionKnownExitZero {
		outcome, err := service.quarantineCompletionUnproven(ctx, claim, attempt, observations)
		if err != nil {
			return publication.Outcome{}, err
		}
		outcomeCode, _ := backupasset.PublicationOutcomeFromFailure(outcome.Code)
		service.metrics.ObserveOutcome(backupasset.ProviderRestic, publication.StageReconciliation, outcomeCode)
		if err := service.writePublicationAudit(ctx, audit, backupasset.AuditActionRecoveryPointPublicationReconcile, backupasset.AuditOutcomeFailure, &attempt, publication.StageReconciliation, backupasset.RecoveryPointFailed, string(outcome.Code), outcome.Code); err != nil {
			service.metrics.ObserveAuditFailure(publication.StageReconciliation)
		}
		return outcome, nil
	}
	evidence, err := storedSummaryCommitEvidence(attempt, observations)
	if err != nil {
		code := backupasset.FailureEvidenceMalformedStream
		if errors.Is(err, errStoredSummaryMissing) {
			code = backupasset.FailureEvidenceMissingSummary
		}
		outcome, terminalErr := service.failPreparing(ctx, claim, attempt, code)
		if terminalErr != nil {
			return publication.Outcome{}, terminalErr
		}
		service.metrics.ObserveOutcome(backupasset.ProviderRestic, publication.StageReconciliation, backupasset.PublicationOutcomeCode(outcome.Code))
		if auditErr := service.writePublicationAudit(ctx, audit, backupasset.AuditActionRecoveryPointPublicationReconcile, backupasset.AuditOutcomeFailure, &attempt, publication.StageReconciliation, outcome.State, string(outcome.Code), outcome.Code); auditErr != nil {
			service.metrics.ObserveAuditFailure(publication.StageReconciliation)
		}
		return outcome, nil
	}
	commitContext, cancel := context.WithCancel(context.WithoutCancel(ctx))
	defer cancel()
	outcome, transitioned, err := service.recordProviderCommit(commitContext, attempt, evidence)
	if err != nil {
		return publication.Outcome{}, err
	}
	if transitioned {
		service.metrics.ObserveOutcome(backupasset.ProviderRestic, publication.StageReconciliation, backupasset.PublicationOutcomeSuccess)
		_ = service.tryWake(outcome.RecoveryPointID)
		if err := service.writePublicationAudit(ctx, audit, backupasset.AuditActionRecoveryPointPublicationReconcile, backupasset.AuditOutcomeSuccess, &attempt, publication.StageReconciliation, backupasset.RecoveryPointVerifying, "", ""); err != nil {
			service.metrics.ObserveAuditFailure(publication.StageReconciliation)
		}
	}
	return outcome, nil
}

func pendingPreparingOutcome(claim publicationPreparingClaim, attempt provider.PublicationAttempt) publication.Outcome {
	return publication.Outcome{
		RepositoryID: claim.point.RepositoryID, RecoveryPointID: claim.point.ID, TaskID: attempt.TaskID, TaskRunID: attempt.TaskRunID,
		State: backupasset.RecoveryPointPreparing,
	}
}

func (service *PublicationService) failPreparing(ctx context.Context, claim publicationPreparingClaim, attempt provider.PublicationAttempt, code backupasset.PublicationFailureCode) (publication.Outcome, error) {
	if backupasset.ValidatePublicationFailureCode(code) != nil {
		return publication.Outcome{}, fmt.Errorf("%w: invalid preparing publication failure", backupasset.ErrInvalidState)
	}
	var outcome publication.Outcome
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var point model.RecoveryPoint
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&point, "id = ?", claim.point.ID).Error; err != nil {
			return fmt.Errorf("lock failed preparing publication point: %w", err)
		}
		if point.State != string(backupasset.RecoveryPointPreparing) || point.ConsistencyJSON != claim.point.ConsistencyJSON {
			return fmt.Errorf("%w: preparing publication point changed", backupasset.ErrLeaseFenceLost)
		}
		if err := service.lease.ValidateFenceTx(ctx, tx, attempt.Fence); err != nil {
			return err
		}
		if err := backupasset.ValidateRecoveryPointTransition(backupasset.RecoveryPointProfile{
			VersionMode: backupasset.VersionNativeSnapshot, Semantics: backupasset.PointNativeSnapshot, State: backupasset.RecoveryPointPreparing,
			Immutability: backupasset.ImmutabilityBackendVersioned, Availability: backupasset.PhysicalUnknown, Hold: backupasset.HoldNone,
		}, backupasset.RecoveryPointFailed); err != nil {
			return err
		}
		consistency, err := backupasset.DecodePublicationConsistency(point.ConsistencyJSON)
		if err != nil {
			return err
		}
		if preservesKnownExitZeroCompletion(code) {
			if consistency.Completion != backupasset.CompletionKnownExitZero {
				return fmt.Errorf("%w: evidence failure lacks known exit-zero completion", backupasset.ErrInvalidState)
			}
		} else {
			consistency.Completion = ""
		}
		consistency.Code = code
		encoded, err := backupasset.EncodePublicationConsistency(consistency)
		if err != nil {
			return err
		}
		now := service.now().UTC()
		point.ConsistencyJSON = encoded
		point.State = string(backupasset.RecoveryPointFailed)
		point.UpdatedAt = now
		if err := tx.Save(&point).Error; err != nil {
			return fmt.Errorf("save failed preparing publication point: %w", err)
		}
		if err := service.lease.ReleaseTx(ctx, tx, attempt.Fence); err != nil {
			return err
		}
		outcome = publication.Outcome{
			RepositoryID: point.RepositoryID, RecoveryPointID: point.ID, TaskID: attempt.TaskID, TaskRunID: attempt.TaskRunID,
			State: backupasset.RecoveryPointFailed, Code: code,
		}
		return nil
	})
	if err != nil {
		return publication.Outcome{}, err
	}
	return outcome, nil
}

// recordMissingAttempt persists the first exact-marker miss while retaining
// the short-lived fence. The durable timestamp distinguishes a genuine
// never-observed point from other retryable publication failures at deadline.
func (service *PublicationService) recordMissingAttempt(ctx context.Context, claim publicationPreparingClaim, attempt provider.PublicationAttempt, missingGrace time.Duration) (publication.Outcome, bool, error) {
	if missingGrace <= 0 {
		return publication.Outcome{}, false, fmt.Errorf("%w: invalid publication missing grace", backupasset.ErrInvalidState)
	}
	var outcome publication.Outcome
	graceReported := false
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var point model.RecoveryPoint
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&point, "id = ?", claim.point.ID).Error; err != nil {
			return fmt.Errorf("lock missing publication point: %w", err)
		}
		if point.State != string(backupasset.RecoveryPointPreparing) || point.ConsistencyJSON != claim.point.ConsistencyJSON {
			return fmt.Errorf("%w: missing publication point changed", backupasset.ErrLeaseFenceLost)
		}
		if err := service.lease.ValidateFenceTx(ctx, tx, attempt.Fence); err != nil {
			return err
		}
		consistency, err := backupasset.DecodePublicationConsistency(point.ConsistencyJSON)
		if err != nil {
			return err
		}
		now := service.now().UTC()
		changed := false
		if consistency.FirstMissingObservedAt == nil {
			firstMissing := now
			consistency.FirstMissingObservedAt = &firstMissing
			changed = true
		} else if consistency.MissingGraceReportedAt == nil && !now.Before(consistency.FirstMissingObservedAt.UTC().Add(missingGrace)) {
			reportedAt := now
			consistency.MissingGraceReportedAt = &reportedAt
			graceReported = true
			changed = true
		}
		if changed {
			encoded, err := backupasset.EncodePublicationConsistency(consistency)
			if err != nil {
				return err
			}
			point.ConsistencyJSON = encoded
			point.UpdatedAt = now
			if err := tx.Save(&point).Error; err != nil {
				return fmt.Errorf("record first missing publication observation: %w", err)
			}
		}
		outcome = publication.Outcome{
			RepositoryID: point.RepositoryID, RecoveryPointID: point.ID, TaskID: attempt.TaskID, TaskRunID: attempt.TaskRunID,
			State: backupasset.RecoveryPointPreparing,
		}
		return nil
	})
	if err != nil {
		return publication.Outcome{}, false, err
	}
	return outcome, graceReported, nil
}

func (service *PublicationService) quarantineCompletionUnproven(ctx context.Context, claim publicationPreparingClaim, attempt provider.PublicationAttempt, observations []provider.ResticSnapshotObservation) (publication.Outcome, error) {
	observation, err := exactAttemptObservation(attempt, observations)
	if err != nil {
		return publication.Outcome{}, err
	}
	return service.quarantinePreparingObservation(ctx, claim, attempt, observation, backupasset.FailureProviderCompletionUnproven)
}

func (service *PublicationService) quarantinePreparingObservation(ctx context.Context, claim publicationPreparingClaim, attempt provider.PublicationAttempt, observation provider.ResticSnapshotObservation, code backupasset.PublicationFailureCode) (publication.Outcome, error) {
	if observation.RepositoryIdentity != attempt.RepositoryIdentity || !validFullNativeID(observation.NativePointID) || observation.SnapshotTime.IsZero() ||
		backupasset.ValidatePublicationFailureCode(code) != nil {
		return publication.Outcome{}, fmt.Errorf("%w: invalid quarantined preparing observation", backupasset.ErrInvalidState)
	}
	locatorPayload, err := json.Marshal(resticPointLocatorV1{Version: 1, Provider: string(backupasset.ProviderRestic), FullSnapshotID: observation.NativePointID})
	if err != nil {
		return publication.Outcome{}, fmt.Errorf("encode completion-unproven locator: %w", err)
	}
	var outcome publication.Outcome
	err = service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var point model.RecoveryPoint
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&point, "id = ?", claim.point.ID).Error; err != nil {
			return fmt.Errorf("lock completion-unproven point: %w", err)
		}
		if point.State != string(backupasset.RecoveryPointPreparing) || point.ConsistencyJSON != claim.point.ConsistencyJSON {
			return fmt.Errorf("%w: completion-unproven point changed", backupasset.ErrLeaseFenceLost)
		}
		if err := service.lease.ValidateFenceTx(ctx, tx, attempt.Fence); err != nil {
			return err
		}
		if err := backupasset.ValidateRecoveryPointTransition(backupasset.RecoveryPointProfile{
			VersionMode: backupasset.VersionNativeSnapshot, Semantics: backupasset.PointNativeSnapshot, State: backupasset.RecoveryPointPreparing,
			Immutability: backupasset.ImmutabilityBackendVersioned, Availability: backupasset.PhysicalUnknown, Hold: backupasset.HoldNone,
		}, backupasset.RecoveryPointFailed); err != nil {
			return err
		}
		consistency, err := backupasset.DecodePublicationConsistency(point.ConsistencyJSON)
		if err != nil {
			return err
		}
		consistency.Completion = ""
		consistency.Code = code
		encoded, err := backupasset.EncodePublicationConsistency(consistency)
		if err != nil {
			return err
		}
		now := service.now().UTC()
		point.EncryptedProviderLocator = string(locatorPayload)
		point.SourceFingerprint = resticSourceFingerprint(observation.RepositoryIdentity, observation.NativePointID)
		point.ConsistencyJSON = encoded
		point.State = string(backupasset.RecoveryPointFailed)
		point.UpdatedAt = now
		if err := tx.Save(&point).Error; err != nil {
			if isPublicationNativeSourceConflict(err) {
				return fmt.Errorf("%w: native Restic point is already claimed", backupasset.ErrConflict)
			}
			return fmt.Errorf("save completion-unproven point: %w", err)
		}
		if err := service.lease.ReleaseTx(ctx, tx, attempt.Fence); err != nil {
			return err
		}
		outcome = publication.Outcome{
			RepositoryID: point.RepositoryID, RecoveryPointID: point.ID, TaskID: attempt.TaskID, TaskRunID: attempt.TaskRunID,
			State: backupasset.RecoveryPointFailed, NativePointID: observation.NativePointID, CapturedAt: observation.SnapshotTime.UTC(),
			ProviderCommitRecorded: false, Code: code,
		}
		return nil
	})
	return outcome, err
}

func (service *PublicationService) claimPreparingPoint(ctx context.Context, pointID string) (publicationPreparingClaim, error) {
	var claim publicationPreparingClaim
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var point model.RecoveryPoint
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&point, "id = ?", pointID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: publication point", backupasset.ErrNotFound)
			}
			return fmt.Errorf("lock preparing publication point: %w", err)
		}
		if point.State != string(backupasset.RecoveryPointPreparing) || point.Semantics != string(backupasset.PointNativeSnapshot) {
			return fmt.Errorf("%w: publication point is no longer preparing", backupasset.ErrConflict)
		}
		lineage, err := backupasset.DecodePublicationLineage(point.LineageJSON)
		if err != nil {
			return err
		}
		consistency, err := backupasset.DecodePublicationConsistency(point.ConsistencyJSON)
		if err != nil {
			return err
		}
		lease, err := service.acquireOrTakeoverPublicationLeaseTx(ctx, tx, point.ID, lineage.PointDeadlineAt, "preparing reconciliation")
		if err != nil {
			return err
		}
		now := service.now().UTC()
		consistency.PublicationRevision++
		consistency.AttemptCount++
		consistency.LastAttemptAt = &now
		encoded, err := backupasset.EncodePublicationConsistency(consistency)
		if err != nil {
			return err
		}
		point.ConsistencyJSON = encoded
		point.UpdatedAt = now
		if err := tx.Save(&point).Error; err != nil {
			return fmt.Errorf("record preparing reconciliation claim: %w", err)
		}
		claim = publicationPreparingClaim{point: point, lineage: lineage, consistency: consistency, lease: lease}
		return nil
	})
	return claim, err
}

func (service *PublicationService) rebuildPreparingAttempt(ctx context.Context, claim publicationPreparingClaim, audit backupasset.PublicationAuditContext) (provider.PublicationAttempt, error) {
	runtime, link, err := service.loadExactPublicationRuntime(ctx, claim.lineage.TaskID, audit)
	if err != nil {
		return provider.PublicationAttempt{}, err
	}
	if runtime.repository.ID != claim.point.RepositoryID || link.ID != claim.lineage.TaskRepositoryLinkID || runtime.repository.RepositoryIdentity == nil ||
		runtime.repository.CapabilityRevision != claim.point.CapabilityRevision || runtime.document.AdapterRevision == "" {
		return provider.PublicationAttempt{}, fmt.Errorf("%w: preparing reconciliation binding drift", backupasset.ErrConflict)
	}
	limits, err := service.providerOperationLimits()
	if err != nil {
		return provider.PublicationAttempt{}, err
	}
	prober, err := service.registry.Prober(backupasset.ProviderRestic)
	if err != nil {
		return provider.PublicationAttempt{}, err
	}
	observation, err := prober.Probe(ctx, runtime.access, limits)
	if err != nil {
		return provider.PublicationAttempt{}, err
	}
	if err := validateObservation(runtime.access, observation); err != nil || observation.RepositoryIdentity != *runtime.repository.RepositoryIdentity ||
		observation.AdapterRevision != runtime.document.AdapterRevision {
		if err != nil {
			return provider.PublicationAttempt{}, err
		}
		return provider.PublicationAttempt{}, fmt.Errorf("%w: preparing reconciliation repository identity drift", backupasset.ErrConflict)
	}
	tags, err := deriveResticPublicationTags(link.ID, claim.point.ID)
	if err != nil {
		return provider.PublicationAttempt{}, err
	}
	return provider.PublicationAttempt{
		Provider: backupasset.ProviderRestic, RepositoryID: claim.point.RepositoryID, RepositoryIdentity: observation.RepositoryIdentity,
		TaskRepositoryLinkID: link.ID, RecoveryPointID: claim.point.ID, TaskID: claim.lineage.TaskID, TaskRunID: claim.lineage.TaskRunID,
		RequiredTags: tags, PointDeadlineAt: claim.lineage.PointDeadlineAt, CapabilityRevision: runtime.repository.CapabilityRevision,
		AdapterRevision: observation.AdapterRevision, Audit: audit, Access: runtime.access, Fence: claim.lease.Fence,
	}, nil
}

func storedSummaryCommitEvidence(attempt provider.PublicationAttempt, observations []provider.ResticSnapshotObservation) (provider.ProviderCommitEvidence, error) {
	observation, err := exactAttemptObservation(attempt, observations)
	if err != nil {
		return provider.ProviderCommitEvidence{}, err
	}
	if observation.Summary == nil {
		return provider.ProviderCommitEvidence{}, fmt.Errorf("%w: %w", backupasset.ErrConflict, errStoredSummaryMissing)
	}
	summary := observation.Summary
	if summary.BackupStartedAt.IsZero() || summary.BackupFinishedAt.IsZero() || summary.BackupFinishedAt.Before(summary.BackupStartedAt) ||
		!observation.SnapshotTime.UTC().Equal(summary.BackupStartedAt.UTC()) {
		return provider.ProviderCommitEvidence{}, fmt.Errorf("%w: %w", backupasset.ErrConflict, errStoredSummaryInvalid)
	}
	return provider.ProviderCommitEvidence{
		Provider: backupasset.ProviderRestic, RepositoryIdentity: observation.RepositoryIdentity, NativePointID: observation.NativePointID,
		CaptureStartedAt: summary.BackupStartedAt.UTC(), CaptureFinishedAt: summary.BackupFinishedAt.UTC(),
		FilesProcessed: summary.FilesProcessed, LogicalBytes: summary.LogicalBytes,
	}, nil
}

func exactAttemptObservation(attempt provider.PublicationAttempt, observations []provider.ResticSnapshotObservation) (provider.ResticSnapshotObservation, error) {
	if len(observations) != 1 {
		return provider.ResticSnapshotObservation{}, fmt.Errorf("%w: exact Restic run tags are ambiguous", backupasset.ErrConflict)
	}
	observation := observations[0]
	if observation.RepositoryIdentity != attempt.RepositoryIdentity || !validFullNativeID(observation.NativePointID) || observation.SnapshotTime.IsZero() ||
		(observation.OriginalPresent && observation.Original != nil) || !exactAttemptTags(observation.Tags, attempt.RequiredTags) {
		return provider.ResticSnapshotObservation{}, fmt.Errorf("%w: exact Restic snapshot was rewritten", backupasset.ErrConflict)
	}
	return observation, nil
}

func exactAttemptTags(observed []string, expected [2]string) bool {
	if len(observed) != 2 || observed[0] == observed[1] {
		return false
	}
	return (observed[0] == expected[0] && observed[1] == expected[1]) || (observed[0] == expected[1] && observed[1] == expected[0])
}

func (service *PublicationService) processVerifyingPoint(ctx context.Context, pointID string) (publication.Outcome, error) {
	token, err := service.admission.Acquire(ctx, publication.OperationManifest)
	if err != nil {
		return publication.Outcome{}, err
	}
	defer func() { _ = token.Close() }()
	if token.Mode() != publication.AdmissionManaged {
		return publication.Outcome{}, fmt.Errorf("%w: manifest publication is not admitted", backupasset.ErrForbidden)
	}

	leaseConfig, err := service.foundation.LeaseConfig()
	if err != nil {
		return publication.Outcome{}, err
	}
	claim, err := service.claimVerifyingPoint(ctx, pointID)
	if err != nil {
		return publication.Outcome{}, err
	}
	audit, err := publicationSystemAuditContext(claim.point.ID, claim.consistency.PublicationRevision, publication.OperationManifest)
	if err != nil {
		return publication.Outcome{}, err
	}
	attempt, evidence, err := service.rebuildManifestAttempt(ctx, claim, audit)
	if err != nil {
		return publication.Outcome{}, err
	}
	config, err := service.foundation.PublicationConfig()
	if err != nil {
		return publication.Outcome{}, err
	}
	limits := provider.ManifestLimits{
		Timeout: config.ManifestTimeout, MaxBytes: config.ManifestMaxBytes, MaxEntries: config.ManifestMaxEntries,
		MaxRecordBytes: config.ManifestMaxRecordBytes, MaxDepth: config.ManifestMaxDepth,
	}
	if err := limits.Validate(); err != nil {
		return publication.Outcome{}, err
	}
	builder, err := service.registry.ManifestBuilder(backupasset.ProviderRestic)
	if err != nil {
		return publication.Outcome{}, err
	}

	workCtx, stopHeartbeat := service.startPublicationHeartbeat(ctx, attempt.Fence, leaseConfig)
	service.metrics.ObserveAttempt(backupasset.ProviderRestic, publication.StageManifest)
	manifest, buildErr := builder.BuildManifest(workCtx, attempt, evidence, limits)
	workCause := context.Cause(workCtx)
	stopHeartbeat()
	if workCause != nil && buildErr == nil {
		buildErr = workCause
	}
	if buildErr != nil {
		if errors.Is(buildErr, backupasset.ErrLeaseFenceLost) || errors.Is(buildErr, backupasset.ErrLeaseDeadlineExceeded) {
			return publication.Outcome{}, buildErr
		}
		return service.commitTransientManifestDiagnostic(ctx, claim, attempt, evidence, provider.ManifestEvidence{
			DigestAlgorithm: "sha256", Generator: "xirang-restic-ls", GeneratorVersion: "1", Completeness: backupasset.ManifestUnavailable,
			Fidelity: provider.ResticManifestFidelityV1(), FailureCode: backupasset.FailureManifestUnavailable,
		})
	}
	if manifest.Completeness != backupasset.ManifestComplete {
		if manifest.FailureCode == backupasset.FailureManifestUnavailable || manifest.FailureCode == backupasset.FailurePublicationDeadlineExceeded {
			rewritten, err := service.unavailableManifestWasRewritten(ctx, attempt, evidence)
			if err != nil {
				return publication.Outcome{}, err
			}
			if !rewritten {
				return service.commitTransientManifestDiagnostic(ctx, claim, attempt, evidence, manifest)
			}
			manifest.FailureCode = backupasset.FailureProviderSnapshotRewritten
		}
		if !terminalManifestFailure(manifest.FailureCode) {
			// The Provider parser uses partial/malformed codes for protocol
			// incompatibility. Persist the safe public terminal category without
			// treating a transient unavailable command as a protocol failure.
			manifest.FailureCode = backupasset.FailureManifestUnavailable
		}
		outcome, err := service.commitDiagnosticManifest(ctx, claim, attempt, evidence, manifest)
		if err != nil {
			return publication.Outcome{}, err
		}
		outcomeCode, _ := backupasset.PublicationOutcomeFromFailure(outcome.Code)
		service.metrics.ObserveOutcome(backupasset.ProviderRestic, publication.StageManifest, outcomeCode)
		if err := service.writePublicationAudit(ctx, audit, backupasset.AuditActionRecoveryPointPublicationFail, backupasset.AuditOutcomeFailure, &attempt, publication.StageManifest, outcome.State, string(outcome.Code), outcome.Code); err != nil {
			service.metrics.ObserveAuditFailure(publication.StageManifest)
		}
		return outcome, nil
	}
	outcome, err := service.commitCompleteManifest(ctx, claim, attempt, evidence, manifest)
	if err != nil {
		return publication.Outcome{}, err
	}
	service.metrics.ObserveOutcome(backupasset.ProviderRestic, publication.StageManifest, backupasset.PublicationOutcomeSuccess)
	if err := service.writePublicationAudit(ctx, audit, backupasset.AuditActionRecoveryPointPublicationCommit, backupasset.AuditOutcomeSuccess, &attempt, publication.StageManifest, backupasset.RecoveryPointCommitted, "", ""); err != nil {
		service.metrics.ObserveAuditFailure(publication.StageManifest)
	}
	return outcome, nil
}

func pendingVerifyingOutcome(claim publicationManifestClaim, attempt provider.PublicationAttempt) publication.Outcome {
	return publication.Outcome{
		RepositoryID: claim.point.RepositoryID, RecoveryPointID: claim.point.ID, TaskID: attempt.TaskID, TaskRunID: attempt.TaskRunID,
		State: backupasset.RecoveryPointVerifying, ProviderCommitRecorded: true,
	}
}

// unavailableManifestWasRewritten performs the one exact-marker lookup that
// distinguishes a temporarily unreadable committed ID from a tag/original/ID
// rewrite. It runs under the manifest admission token and publication fence;
// no repository-wide fallback, prefix, or latest selection is permitted.
func (service *PublicationService) unavailableManifestWasRewritten(ctx context.Context, attempt provider.PublicationAttempt, evidence provider.ProviderCommitEvidence) (bool, error) {
	publisher, err := service.registry.ResticPublisher(backupasset.ProviderRestic)
	if err != nil {
		return false, err
	}
	observations, err := publisher.LookupAttempt(ctx, attempt)
	if err != nil {
		// Lookup unavailability is itself transient. The durable verifying row
		// remains the source of truth and its lease will short-expire.
		return false, nil
	}
	if len(observations) == 0 {
		return false, nil
	}
	if len(observations) != 1 {
		return true, nil
	}
	observation, err := exactAttemptObservation(attempt, observations)
	if err != nil || observation.NativePointID != evidence.NativePointID || !observation.SnapshotTime.UTC().Equal(evidence.CaptureStartedAt.UTC()) {
		return true, nil
	}
	return false, nil
}

// commitTransientManifestDiagnostic keeps a bounded inactive diagnostic for a
// transient manifest failure while deliberately leaving the point verifying
// and its fence active. The caller has already stopped its heartbeat, so a
// later worker can take over without extending the immutable deadline.
func (service *PublicationService) commitTransientManifestDiagnostic(ctx context.Context, claim publicationManifestClaim, attempt provider.PublicationAttempt, evidence provider.ProviderCommitEvidence, manifest provider.ManifestEvidence) (publication.Outcome, error) {
	if manifest.Completeness != backupasset.ManifestPartial && manifest.Completeness != backupasset.ManifestUnavailable {
		return publication.Outcome{}, fmt.Errorf("%w: invalid transient manifest completeness", backupasset.ErrInvalidState)
	}
	if manifest.FailureCode == "" {
		manifest.FailureCode = backupasset.FailureManifestUnavailable
	}
	if backupasset.ValidatePublicationFailureCode(manifest.FailureCode) != nil {
		return publication.Outcome{}, fmt.Errorf("%w: invalid transient manifest failure", backupasset.ErrInvalidState)
	}
	if manifest.Completeness == backupasset.ManifestUnavailable && (manifest.Digest != "" || manifest.EntryCount != 0 || manifest.LogicalBytes != 0) {
		return publication.Outcome{}, fmt.Errorf("%w: invalid unavailable manifest diagnostic", backupasset.ErrInvalidState)
	}
	if manifest.Completeness == backupasset.ManifestPartial && (!isLowerHex64(manifest.Digest) || manifest.EntryCount < 0 || manifest.LogicalBytes < 0) {
		return publication.Outcome{}, fmt.Errorf("%w: invalid partial manifest diagnostic", backupasset.ErrInvalidState)
	}
	fidelityJSON, err := json.Marshal(manifest.Fidelity)
	if err != nil {
		return publication.Outcome{}, fmt.Errorf("encode transient manifest fidelity: %w", err)
	}
	commitEvidenceJSON, err := json.Marshal(manifestCommitEvidenceV1{
		Version: 1, Provider: evidence.Provider, RepositoryIdentity: evidence.RepositoryIdentity, NativePointID: evidence.NativePointID,
		CaptureStartedAt: evidence.CaptureStartedAt.UTC(), CaptureFinishedAt: evidence.CaptureFinishedAt.UTC(), FilesProcessed: evidence.FilesProcessed,
		LogicalBytes: evidence.LogicalBytes, ObservedTags: attempt.RequiredTags, ObservedTagDigest: manifest.ObservedTagDigest,
	})
	if err != nil {
		return publication.Outcome{}, fmt.Errorf("encode transient manifest provider commit evidence: %w", err)
	}
	diagnosticID, err := diagnosticManifestID(claim.point.ID, attempt.Fence.AttemptID)
	if err != nil {
		return publication.Outcome{}, err
	}
	var outcome publication.Outcome
	err = service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var point model.RecoveryPoint
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&point, "id = ?", claim.point.ID).Error; err != nil {
			return fmt.Errorf("lock transient manifest point: %w", err)
		}
		if point.State != string(backupasset.RecoveryPointVerifying) || point.ConsistencyJSON != claim.point.ConsistencyJSON {
			return fmt.Errorf("%w: transient manifest point changed", backupasset.ErrLeaseFenceLost)
		}
		if err := service.lease.ValidateFenceTx(ctx, tx, attempt.Fence); err != nil {
			return err
		}
		var existing model.RecoveryPointManifest
		if err := tx.Where("id = ?", diagnosticID).Limit(1).Find(&existing).Error; err != nil {
			return fmt.Errorf("load transient manifest diagnostic: %w", err)
		}
		if existing.ID == "" {
			var latestRevision int
			if err := tx.Model(&model.RecoveryPointManifest{}).Where("recovery_point_id = ?", point.ID).Select("COALESCE(MAX(revision), 0)").Scan(&latestRevision).Error; err != nil {
				return fmt.Errorf("load transient manifest revision: %w", err)
			}
			now := service.now().UTC()
			row := model.RecoveryPointManifest{
				ID: diagnosticID, RecoveryPointID: point.ID, Revision: latestRevision + 1, DigestAlgorithm: manifest.DigestAlgorithm, Digest: manifest.Digest,
				Generator: manifest.Generator, GeneratorVersion: manifest.GeneratorVersion, Completeness: string(manifest.Completeness),
				EntryCount: manifest.EntryCount, LogicalBytes: manifest.LogicalBytes, FidelityJSON: string(fidelityJSON), EncryptedCommitEvidence: string(commitEvidenceJSON),
				IsActive: false, CreatedAt: now, UpdatedAt: now,
			}
			if err := tx.Create(&row).Error; err != nil {
				return fmt.Errorf("create transient manifest diagnostic: %w", err)
			}
		}
		outcome = pendingVerifyingOutcome(claim, attempt)
		return nil
	})
	if err != nil {
		return publication.Outcome{}, err
	}
	service.metrics.ObserveOutcome(backupasset.ProviderRestic, publication.StageManifest, backupasset.PublicationOutcomeCode(manifest.FailureCode))
	audit, auditErr := publicationSystemAuditContext(claim.point.ID, claim.consistency.PublicationRevision, publication.OperationManifest)
	if auditErr == nil {
		if err := service.writePublicationAudit(ctx, audit, backupasset.AuditActionRecoveryPointPublicationVerify, backupasset.AuditOutcomeFailure, &attempt, publication.StageManifest, backupasset.RecoveryPointVerifying, string(manifest.FailureCode), manifest.FailureCode); err != nil {
			service.metrics.ObserveAuditFailure(publication.StageManifest)
		}
	}
	return outcome, nil
}

func (service *PublicationService) claimVerifyingPoint(ctx context.Context, pointID string) (publicationManifestClaim, error) {
	var claim publicationManifestClaim
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var point model.RecoveryPoint
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&point, "id = ?", pointID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: publication point", backupasset.ErrNotFound)
			}
			return fmt.Errorf("lock verifying publication point: %w", err)
		}
		if point.State != string(backupasset.RecoveryPointVerifying) || point.Semantics != string(backupasset.PointNativeSnapshot) {
			return fmt.Errorf("%w: publication point is no longer verifying", backupasset.ErrConflict)
		}
		lineage, err := backupasset.DecodePublicationLineage(point.LineageJSON)
		if err != nil {
			return err
		}
		consistency, err := backupasset.DecodePublicationConsistency(point.ConsistencyJSON)
		if err != nil {
			return err
		}
		if consistency.Provider != backupasset.ProviderRestic || consistency.CaptureStartedAt == nil || consistency.CaptureFinishedAt == nil ||
			consistency.ProviderCommitDigest == "" || consistency.RequestedTagDigest == "" || consistency.RepositoryIdentityDigest == "" {
			return fmt.Errorf("%w: verifying point has no durable provider commit", backupasset.ErrConflict)
		}
		locator, err := decodeResticPointLocator(point.EncryptedProviderLocator)
		if err != nil {
			return err
		}
		lease, err := service.acquireOrTakeoverPublicationLeaseTx(ctx, tx, point.ID, lineage.PointDeadlineAt, "manifest publication")
		if err != nil {
			return err
		}
		now := service.now().UTC()
		consistency.PublicationRevision++
		consistency.AttemptCount++
		consistency.LastAttemptAt = &now
		encoded, err := backupasset.EncodePublicationConsistency(consistency)
		if err != nil {
			return err
		}
		point.ConsistencyJSON = encoded
		point.UpdatedAt = now
		if err := tx.Save(&point).Error; err != nil {
			return fmt.Errorf("record manifest publication claim: %w", err)
		}
		claim = publicationManifestClaim{point: point, lineage: lineage, consistency: consistency, locator: locator, lease: lease}
		return nil
	})
	return claim, err
}

// acquireOrTakeoverPublicationLeaseTx keeps a reconciling stage on the
// immutable point deadline. A live point-publication lease belongs to another
// worker, while an expired one must rotate its fence in place rather than
// creating a second owner slot.
func (service *PublicationService) acquireOrTakeoverPublicationLeaseTx(ctx context.Context, tx *gorm.DB, pointID string, deadline time.Time, stage string) (backupasset.Lease, error) {
	if tx == nil || backupasset.ValidateOpaqueID(pointID) != nil || deadline.IsZero() || stage == "" {
		return backupasset.Lease{}, fmt.Errorf("%w: invalid publication lease claim", backupasset.ErrInvalidState)
	}

	var active model.RecoveryPointLease
	result := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("recovery_point_id = ? AND holder_type = ? AND owner_id = ? AND status = ?", pointID, backupasset.LeaseHolderPointPublication, publicationLeaseOwner, backupasset.LeaseActive).
		Limit(1).
		Find(&active)
	if result.Error != nil {
		return backupasset.Lease{}, fmt.Errorf("lock %s lease: %w", stage, result.Error)
	}
	if result.RowsAffected == 0 {
		lease, err := service.lease.AcquireTx(ctx, tx, backupasset.AcquireLeaseRequest{
			RecoveryPointID:  pointID,
			HolderType:       backupasset.LeaseHolderPointPublication,
			OwnerID:          publicationLeaseOwner,
			AbsoluteDeadline: deadline,
		})
		if errors.Is(err, backupasset.ErrLeaseHeld) {
			return backupasset.Lease{}, fmt.Errorf("%w: %s lease", backupasset.ErrPublicationInProgress, stage)
		}
		return lease, err
	}
	if !active.AbsoluteDeadline.UTC().Equal(deadline.UTC()) {
		return backupasset.Lease{}, fmt.Errorf("%w: %s lease changed immutable point deadline", backupasset.ErrConflict, stage)
	}
	if service.now().UTC().Before(active.LeaseExpiresAt.UTC()) {
		return backupasset.Lease{}, fmt.Errorf("%w: %s lease", backupasset.ErrPublicationInProgress, stage)
	}
	lease, err := service.lease.TakeoverTx(ctx, tx, backupasset.TakeoverLeaseRequest{LeaseID: active.ID, OwnerID: publicationLeaseOwner})
	if errors.Is(err, backupasset.ErrLeaseHeld) {
		return backupasset.Lease{}, fmt.Errorf("%w: %s lease", backupasset.ErrPublicationInProgress, stage)
	}
	return lease, err
}

func (service *PublicationService) rebuildManifestAttempt(ctx context.Context, claim publicationManifestClaim, audit backupasset.PublicationAuditContext) (provider.PublicationAttempt, provider.ProviderCommitEvidence, error) {
	runtime, link, err := service.loadExactPublicationRuntime(ctx, claim.lineage.TaskID, audit)
	if err != nil {
		return provider.PublicationAttempt{}, provider.ProviderCommitEvidence{}, err
	}
	if runtime.repository.ID != claim.point.RepositoryID || link.ID != claim.lineage.TaskRepositoryLinkID || runtime.repository.RepositoryIdentity == nil ||
		runtime.repository.CapabilityRevision != claim.consistency.CapabilityRevision || runtime.document.AdapterRevision != claim.consistency.AdapterRevision {
		return provider.PublicationAttempt{}, provider.ProviderCommitEvidence{}, fmt.Errorf("%w: manifest publication binding drift", backupasset.ErrConflict)
	}
	limits, err := service.providerOperationLimits()
	if err != nil {
		return provider.PublicationAttempt{}, provider.ProviderCommitEvidence{}, err
	}
	prober, err := service.registry.Prober(backupasset.ProviderRestic)
	if err != nil {
		return provider.PublicationAttempt{}, provider.ProviderCommitEvidence{}, err
	}
	observation, err := prober.Probe(ctx, runtime.access, limits)
	if err != nil {
		return provider.PublicationAttempt{}, provider.ProviderCommitEvidence{}, err
	}
	if err := validateObservation(runtime.access, observation); err != nil || observation.RepositoryIdentity != *runtime.repository.RepositoryIdentity ||
		observation.AdapterRevision != runtime.document.AdapterRevision {
		if err != nil {
			return provider.PublicationAttempt{}, provider.ProviderCommitEvidence{}, err
		}
		return provider.PublicationAttempt{}, provider.ProviderCommitEvidence{}, fmt.Errorf("%w: manifest repository identity drift", backupasset.ErrConflict)
	}
	tags, err := deriveResticPublicationTags(link.ID, claim.point.ID)
	if err != nil {
		return provider.PublicationAttempt{}, provider.ProviderCommitEvidence{}, err
	}
	if claim.consistency.RequestedTagDigest != publicationTagDigest(tags) || claim.consistency.RepositoryIdentityDigest != digestText(observation.RepositoryIdentity) {
		return provider.PublicationAttempt{}, provider.ProviderCommitEvidence{}, fmt.Errorf("%w: manifest provider evidence drift", backupasset.ErrConflict)
	}
	attempt := provider.PublicationAttempt{
		Provider: backupasset.ProviderRestic, RepositoryID: claim.point.RepositoryID, RepositoryIdentity: observation.RepositoryIdentity,
		TaskRepositoryLinkID: link.ID, RecoveryPointID: claim.point.ID, TaskID: claim.lineage.TaskID, TaskRunID: claim.lineage.TaskRunID,
		RequiredTags: tags, PointDeadlineAt: claim.lineage.PointDeadlineAt, CapabilityRevision: runtime.repository.CapabilityRevision,
		AdapterRevision: observation.AdapterRevision, Audit: audit, Access: runtime.access, Fence: claim.lease.Fence,
	}
	evidence := provider.ProviderCommitEvidence{
		Provider: backupasset.ProviderRestic, RepositoryIdentity: observation.RepositoryIdentity, NativePointID: claim.locator.FullSnapshotID,
		CaptureStartedAt: *claim.consistency.CaptureStartedAt, CaptureFinishedAt: *claim.consistency.CaptureFinishedAt,
		FilesProcessed: claim.consistency.FilesProcessed, LogicalBytes: claim.consistency.LogicalBytes,
	}
	digest, err := canonicalProviderCommitDigest(attempt, evidence, claim.consistency.RequestedTagDigest)
	if err != nil {
		return provider.PublicationAttempt{}, provider.ProviderCommitEvidence{}, err
	}
	if digest != claim.consistency.ProviderCommitDigest || claim.point.SourceFingerprint != resticSourceFingerprint(evidence.RepositoryIdentity, evidence.NativePointID) {
		return provider.PublicationAttempt{}, provider.ProviderCommitEvidence{}, fmt.Errorf("%w: manifest provider commit digest drift", backupasset.ErrConflict)
	}
	return attempt, evidence, nil
}

func (service *PublicationService) commitCompleteManifest(ctx context.Context, claim publicationManifestClaim, attempt provider.PublicationAttempt, evidence provider.ProviderCommitEvidence, manifest provider.ManifestEvidence) (publication.Outcome, error) {
	if manifest.DigestAlgorithm != "sha256" || !isLowerHex64(manifest.Digest) || manifest.EntryCount < 0 || manifest.LogicalBytes < 0 ||
		manifest.HeaderCapturedAt.IsZero() || !manifest.HeaderCapturedAt.UTC().Equal(evidence.CaptureStartedAt.UTC()) || manifest.ObservedTagDigest != publicationTagDigest(attempt.RequiredTags) {
		return publication.Outcome{}, fmt.Errorf("%w: invalid complete manifest evidence", backupasset.ErrConflict)
	}
	fidelityJSON, err := json.Marshal(manifest.Fidelity)
	if err != nil {
		return publication.Outcome{}, fmt.Errorf("encode manifest fidelity: %w", err)
	}
	commitEvidenceJSON, err := json.Marshal(manifestCommitEvidenceV1{
		Version: 1, Provider: evidence.Provider, RepositoryIdentity: evidence.RepositoryIdentity, NativePointID: evidence.NativePointID,
		CaptureStartedAt: evidence.CaptureStartedAt.UTC(), CaptureFinishedAt: evidence.CaptureFinishedAt.UTC(), FilesProcessed: evidence.FilesProcessed,
		LogicalBytes: evidence.LogicalBytes, ObservedTags: attempt.RequiredTags, ObservedTagDigest: manifest.ObservedTagDigest,
	})
	if err != nil {
		return publication.Outcome{}, fmt.Errorf("encode manifest provider commit evidence: %w", err)
	}

	var outcome publication.Outcome
	err = service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var point model.RecoveryPoint
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&point, "id = ?", claim.point.ID).Error; err != nil {
			return fmt.Errorf("lock manifest commit point: %w", err)
		}
		if point.State != string(backupasset.RecoveryPointVerifying) || point.ConsistencyJSON != claim.point.ConsistencyJSON || point.SourceFingerprint != claim.point.SourceFingerprint {
			return fmt.Errorf("%w: manifest point changed during enumeration", backupasset.ErrLeaseFenceLost)
		}
		if err := service.lease.ValidateFenceTx(ctx, tx, attempt.Fence); err != nil {
			return err
		}
		if err := backupasset.ValidateRecoveryPointTransition(backupasset.RecoveryPointProfile{
			VersionMode: backupasset.VersionNativeSnapshot, Semantics: backupasset.PointNativeSnapshot, State: backupasset.RecoveryPointVerifying,
			Immutability: backupasset.ImmutabilityBackendVersioned, Availability: backupasset.PhysicalUnknown, Hold: backupasset.HoldNone,
		}, backupasset.RecoveryPointCommitted); err != nil {
			return err
		}
		var latestRevision int
		if err := tx.Model(&model.RecoveryPointManifest{}).Where("recovery_point_id = ?", point.ID).Select("COALESCE(MAX(revision), 0)").Scan(&latestRevision).Error; err != nil {
			return fmt.Errorf("load manifest revision: %w", err)
		}
		manifestID, err := backupasset.NewOpaqueID()
		if err != nil {
			return err
		}
		now := service.now().UTC()
		if err := tx.Model(&model.RecoveryPointManifest{}).Where("recovery_point_id = ? AND is_active = ?", point.ID, true).Update("is_active", false).Error; err != nil {
			return fmt.Errorf("deactivate prior manifest: %w", err)
		}
		manifestRow := model.RecoveryPointManifest{
			ID: manifestID, RecoveryPointID: point.ID, Revision: latestRevision + 1, DigestAlgorithm: manifest.DigestAlgorithm, Digest: manifest.Digest,
			Generator: manifest.Generator, GeneratorVersion: manifest.GeneratorVersion, Completeness: string(backupasset.ManifestComplete),
			EntryCount: manifest.EntryCount, LogicalBytes: manifest.LogicalBytes, FidelityJSON: string(fidelityJSON), EncryptedCommitEvidence: string(commitEvidenceJSON),
			IsActive: true, CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.Create(&manifestRow).Error; err != nil {
			return fmt.Errorf("create complete manifest: %w", err)
		}
		capturedAt := manifest.HeaderCapturedAt.UTC()
		committedAt := now
		point.CapturedAt = &capturedAt
		point.CommittedAt = &committedAt
		point.ManifestDigestAlgorithm = manifest.DigestAlgorithm
		point.ManifestDigest = manifest.Digest
		point.EntryCount = manifest.EntryCount
		point.LogicalBytes = manifest.LogicalBytes
		point.FidelityJSON = string(fidelityJSON)
		point.State = string(backupasset.RecoveryPointCommitted)
		point.UpdatedAt = now
		if err := tx.Save(&point).Error; err != nil {
			return fmt.Errorf("save committed publication point: %w", err)
		}
		if err := service.lease.ReleaseTx(ctx, tx, attempt.Fence); err != nil {
			return err
		}
		previousNativePointID, err := previousCommittedNativePointTx(ctx, tx, point, claim.lineage, capturedAt)
		if err != nil {
			return err
		}
		outcome = publication.Outcome{
			RepositoryID: point.RepositoryID, RecoveryPointID: point.ID, TaskID: attempt.TaskID, TaskRunID: attempt.TaskRunID,
			State: backupasset.RecoveryPointCommitted, NativePointID: evidence.NativePointID, PreviousNativePointID: previousNativePointID,
			CapturedAt: capturedAt, ProviderCommitRecorded: true,
		}
		return nil
	})
	return outcome, err
}

// previousCommittedNativePointTx returns the immediately preceding committed
// native point for this immutable Task/link lineage. It deliberately replays
// the LineageGuard ownership checks inside the publication transaction, so a
// stale live foreign key cannot silently turn a shared Repository point into
// this Task's anomaly predecessor.
func previousCommittedNativePointTx(ctx context.Context, tx *gorm.DB, current model.RecoveryPoint, lineage backupasset.PublicationLineageV1, capturedAt time.Time) (string, error) {
	if tx == nil || current.ID == "" || current.RepositoryID == "" || capturedAt.IsZero() {
		return "", fmt.Errorf("%w: invalid committed predecessor query", backupasset.ErrInvalidState)
	}
	var candidates []model.RecoveryPoint
	if err := tx.WithContext(ctx).
		Where("repository_id = ? AND semantics = ? AND state = ? AND id <> ?", current.RepositoryID, backupasset.PointNativeSnapshot, backupasset.RecoveryPointCommitted, current.ID).
		Order("captured_at DESC, id DESC").
		Find(&candidates).Error; err != nil {
		return "", fmt.Errorf("list committed publication predecessors: %w", err)
	}
	for _, candidate := range candidates {
		if candidate.CapturedAt == nil || candidate.CapturedAt.IsZero() {
			continue
		}
		candidateCapturedAt := candidate.CapturedAt.UTC()
		if candidateCapturedAt.After(capturedAt.UTC()) || (candidateCapturedAt.Equal(capturedAt.UTC()) && candidate.ID >= current.ID) {
			continue
		}
		candidateLineage, err := backupasset.DecodePublicationLineage(candidate.LineageJSON)
		if err != nil {
			// A malformed foreign row proves no ownership and matches the guarded
			// legacy read behavior: it cannot become a Task predecessor.
			continue
		}
		if candidateLineage.TaskID != lineage.TaskID || candidateLineage.TaskRepositoryLinkID != lineage.TaskRepositoryLinkID ||
			candidateLineage.PublicationMode != string(backupasset.PublicationNativeSnapshot) {
			continue
		}
		if (candidate.ProducingTaskID != nil && *candidate.ProducingTaskID != candidateLineage.TaskID) ||
			(candidate.ProducingTaskRunID != nil && *candidate.ProducingTaskRunID != candidateLineage.TaskRunID) {
			return "", fmt.Errorf("%w: committed predecessor live foreign key conflicts with immutable lineage", backupasset.ErrConflict)
		}
		locator, err := decodeResticPointLocator(candidate.EncryptedProviderLocator)
		if err != nil {
			return "", err
		}
		return locator.FullSnapshotID, nil
	}
	return "", nil
}

func (service *PublicationService) commitDiagnosticManifest(ctx context.Context, claim publicationManifestClaim, attempt provider.PublicationAttempt, evidence provider.ProviderCommitEvidence, manifest provider.ManifestEvidence) (publication.Outcome, error) {
	if manifest.Completeness != backupasset.ManifestPartial && manifest.Completeness != backupasset.ManifestUnavailable {
		return publication.Outcome{}, fmt.Errorf("%w: invalid diagnostic manifest completeness", backupasset.ErrInvalidState)
	}
	code := manifest.FailureCode
	if code == "" {
		code = backupasset.FailureManifestUnavailable
	}
	if backupasset.ValidatePublicationFailureCode(code) != nil {
		return publication.Outcome{}, fmt.Errorf("%w: invalid diagnostic manifest failure", backupasset.ErrInvalidState)
	}
	if !terminalManifestFailure(code) {
		return publication.Outcome{}, fmt.Errorf("%w: transient diagnostic manifest is not terminal", backupasset.ErrInvalidState)
	}
	if manifest.EntryCount < 0 || manifest.LogicalBytes < 0 || (manifest.Completeness == backupasset.ManifestPartial && !isLowerHex64(manifest.Digest)) ||
		(manifest.Completeness == backupasset.ManifestUnavailable && (manifest.Digest != "" || manifest.EntryCount != 0 || manifest.LogicalBytes != 0)) {
		return publication.Outcome{}, fmt.Errorf("%w: invalid diagnostic manifest evidence", backupasset.ErrInvalidState)
	}
	fidelityJSON, err := json.Marshal(manifest.Fidelity)
	if err != nil {
		return publication.Outcome{}, fmt.Errorf("encode diagnostic manifest fidelity: %w", err)
	}
	commitEvidenceJSON, err := json.Marshal(manifestCommitEvidenceV1{
		Version: 1, Provider: evidence.Provider, RepositoryIdentity: evidence.RepositoryIdentity, NativePointID: evidence.NativePointID,
		CaptureStartedAt: evidence.CaptureStartedAt.UTC(), CaptureFinishedAt: evidence.CaptureFinishedAt.UTC(), FilesProcessed: evidence.FilesProcessed,
		LogicalBytes: evidence.LogicalBytes, ObservedTags: attempt.RequiredTags, ObservedTagDigest: manifest.ObservedTagDigest,
	})
	if err != nil {
		return publication.Outcome{}, fmt.Errorf("encode diagnostic manifest provider commit evidence: %w", err)
	}
	diagnosticID, err := diagnosticManifestID(claim.point.ID, attempt.Fence.AttemptID)
	if err != nil {
		return publication.Outcome{}, err
	}
	var outcome publication.Outcome
	err = service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var point model.RecoveryPoint
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&point, "id = ?", claim.point.ID).Error; err != nil {
			return fmt.Errorf("lock diagnostic manifest point: %w", err)
		}
		if point.State != string(backupasset.RecoveryPointVerifying) || point.ConsistencyJSON != claim.point.ConsistencyJSON {
			return fmt.Errorf("%w: diagnostic manifest point changed", backupasset.ErrLeaseFenceLost)
		}
		if err := service.lease.ValidateFenceTx(ctx, tx, attempt.Fence); err != nil {
			return err
		}
		if err := backupasset.ValidateRecoveryPointTransition(backupasset.RecoveryPointProfile{
			VersionMode: backupasset.VersionNativeSnapshot, Semantics: backupasset.PointNativeSnapshot, State: backupasset.RecoveryPointVerifying,
			Immutability: backupasset.ImmutabilityBackendVersioned, Availability: backupasset.PhysicalUnknown, Hold: backupasset.HoldNone,
		}, backupasset.RecoveryPointFailed); err != nil {
			return err
		}
		var latestRevision int
		if err := tx.Model(&model.RecoveryPointManifest{}).Where("recovery_point_id = ?", point.ID).Select("COALESCE(MAX(revision), 0)").Scan(&latestRevision).Error; err != nil {
			return fmt.Errorf("load diagnostic manifest revision: %w", err)
		}
		now := service.now().UTC()
		row := model.RecoveryPointManifest{
			ID: diagnosticID, RecoveryPointID: point.ID, Revision: latestRevision + 1, DigestAlgorithm: manifest.DigestAlgorithm, Digest: manifest.Digest,
			Generator: manifest.Generator, GeneratorVersion: manifest.GeneratorVersion, Completeness: string(manifest.Completeness),
			EntryCount: manifest.EntryCount, LogicalBytes: manifest.LogicalBytes, FidelityJSON: string(fidelityJSON), EncryptedCommitEvidence: string(commitEvidenceJSON),
			IsActive: false, CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.Create(&row).Error; err != nil {
			return fmt.Errorf("create diagnostic manifest: %w", err)
		}
		point.State = string(backupasset.RecoveryPointFailed)
		consistency, err := backupasset.DecodePublicationConsistency(point.ConsistencyJSON)
		if err != nil {
			return err
		}
		if preservesKnownExitZeroCompletion(code) {
			if consistency.Completion != backupasset.CompletionKnownExitZero {
				return fmt.Errorf("%w: evidence failure lacks known exit-zero completion", backupasset.ErrInvalidState)
			}
		} else {
			consistency.Completion = ""
		}
		consistency.Code = code
		encodedConsistency, err := backupasset.EncodePublicationConsistency(consistency)
		if err != nil {
			return err
		}
		point.ConsistencyJSON = encodedConsistency
		point.UpdatedAt = now
		if err := tx.Save(&point).Error; err != nil {
			return fmt.Errorf("save diagnostic publication point: %w", err)
		}
		if err := service.lease.ReleaseTx(ctx, tx, attempt.Fence); err != nil {
			return err
		}
		outcome = publication.Outcome{
			RepositoryID: point.RepositoryID, RecoveryPointID: point.ID, TaskID: attempt.TaskID, TaskRunID: attempt.TaskRunID,
			State: backupasset.RecoveryPointFailed, NativePointID: evidence.NativePointID, CapturedAt: evidence.CaptureStartedAt.UTC(),
			ProviderCommitRecorded: true, Code: code,
		}
		return nil
	})
	return outcome, err
}

func preservesKnownExitZeroCompletion(code backupasset.PublicationFailureCode) bool {
	switch code {
	case backupasset.FailureEvidenceMissingSummary,
		backupasset.FailureEvidenceMalformedStream,
		backupasset.FailureEvidenceDuplicateSummary,
		backupasset.FailureEvidenceNonFinalSummary,
		backupasset.FailureEvidenceInvalidNativeID:
		return true
	default:
		return false
	}
}

func terminalManifestFailure(code backupasset.PublicationFailureCode) bool {
	switch code {
	case backupasset.FailureProviderResourceLimit,
		backupasset.FailureProviderSnapshotRewritten,
		backupasset.FailureRepositoryIdentityDrift,
		backupasset.FailureManifestUnavailable:
		return true
	default:
		return false
	}
}

func diagnosticManifestID(pointID, attemptID string) (string, error) {
	if backupasset.ValidateOpaqueID(pointID) != nil || backupasset.ValidateOpaqueID(attemptID) != nil {
		return "", fmt.Errorf("%w: invalid diagnostic manifest identity", backupasset.ErrInvalidState)
	}
	sum := sha256.Sum256([]byte("xirang.restic.manifest.diagnostic.v1\x00" + pointID + "\x00" + attemptID))
	return hex.EncodeToString(sum[:16]), nil
}

func publicationSystemAuditContext(pointID string, revision uint64, operation publication.ResticOperation) (backupasset.PublicationAuditContext, error) {
	if backupasset.ValidateOpaqueID(pointID) != nil || revision == 0 || (operation != publication.OperationManifest && operation != publication.OperationReconcile) {
		return backupasset.PublicationAuditContext{}, fmt.Errorf("%w: invalid publication worker audit identity", backupasset.ErrInvalidState)
	}
	sum := sha256.Sum256([]byte("xirang.publication.worker-correlation.v1\x00" + pointID + "\x00" + strconv.FormatUint(revision, 10) + "\x00" + string(operation)))
	return backupasset.NewSystemPublicationAuditContext("pubw-" + hex.EncodeToString(sum[:16]))
}

func (service *PublicationService) startPublicationHeartbeat(parent context.Context, fence backupasset.LeaseFence, config backupasset.LeaseConfig) (context.Context, func()) {
	contextWithCause, cancel := context.WithCancelCause(parent)
	stop := make(chan struct{})
	var once sync.Once
	var wait sync.WaitGroup
	wait.Add(1)
	go func() {
		defer wait.Done()
		ticker := time.NewTicker(config.Heartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-contextWithCause.Done():
				return
			case <-stop:
				return
			case <-ticker.C:
				if _, err := service.lease.Renew(contextWithCause, fence); err != nil {
					cancel(err)
					return
				}
			}
		}
	}()
	return contextWithCause, func() {
		once.Do(func() {
			cancel(nil)
			close(stop)
			wait.Wait()
		})
	}
}

var _ publication.Reconciler = (*PublicationService)(nil)
