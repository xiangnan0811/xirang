package processing

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/content"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrGrantDenied         = errors.New("processing grant denied")
	ErrGrantBudgetExceeded = errors.New("processing grant budget exceeded")
)

type GrantKind string

const (
	GrantInput GrantKind = "input"
	GrantSink  GrantKind = "sink"
)

type GrantState string

const (
	GrantIssued  GrantState = "issued"
	GrantActive  GrantState = "active"
	GrantRevoked GrantState = "revoked"
	GrantExpired GrantState = "expired"
	GrantClosed  GrantState = "closed"
)

type GrantRequestKind string

const (
	GrantRequestStat       GrantRequestKind = "stat"
	GrantRequestSequential GrantRequestKind = "sequential"
	GrantRequestRange      GrantRequestKind = "range"
	GrantRequestUpload     GrantRequestKind = "upload"
)

type GrantRequestState string

const (
	GrantRequestReserved   GrantRequestState = "reserved"
	GrantRequestStreaming  GrantRequestState = "streaming"
	GrantRequestSucceeded  GrantRequestState = "succeeded"
	GrantRequestFailed     GrantRequestState = "failed"
	GrantRequestCanceled   GrantRequestState = "canceled"
	GrantRequestReconciled GrantRequestState = "reconciled"
)

type GrantFailureCode string

const (
	GrantFailureBudgetExhausted GrantFailureCode = "budget_exhausted"
	GrantFailureSourceChanged   GrantFailureCode = "source_changed"
	GrantFailureLeaseLost       GrantFailureCode = "lease_lost"
	GrantFailureClientCanceled  GrantFailureCode = "client_canceled"
	GrantFailureSourceFailed    GrantFailureCode = "source_failed"
	GrantFailureWriteFailed     GrantFailureCode = "write_failed"
	GrantFailureReconciledCrash GrantFailureCode = "reconciled_crash"
)

type GrantConfig struct {
	TTL                time.Duration
	InputLimits        GrantLimits
	SinkLimits         GrantLimits
	MaxRequests        int64
	MaxBytesPerRequest int64
	MaxCumulativeBytes int64
	MaxInFlight        int64
	Random             io.Reader
}

type IssueGrantsRequest struct {
	JobID              string
	AttemptID          string
	WorkerID           string
	RecoveryPointFence backupasset.LeaseFence
}

type GrantActivationMaterial struct {
	GrantID string
	Secret  string
}

type AttemptGrantMaterial struct {
	Input GrantActivationMaterial
	Sink  GrantActivationMaterial
}

type ActivateGrantRequest struct {
	GrantID   string
	Kind      GrantKind
	JobID     string
	AttemptID string
	WorkerID  string
	Secret    string
}

type ActivatedGrant struct {
	SessionID string
	Kind      GrantKind
	ExpiresAt time.Time
	Limits    GrantLimits
}

type GrantLimits struct {
	MaxRequests        int64
	MaxBytesPerRequest int64
	MaxCumulativeBytes int64
	MaxInFlight        int64
}

type ReserveGrantRequest struct {
	GrantID     string
	Kind        GrantRequestKind
	RangeOffset *int64
	RangeLength *int64
	Bytes       int64
}

type GrantReservation struct {
	ReservationID string
	GrantID       string
	Kind          GrantRequestKind
	ReservedBytes int64
}

type FinalizeGrantRequest struct {
	ReservationID string
	Outcome       GrantRequestState
	ProviderBytes int64
	StoredBytes   int64
	EvidenceKnown bool
	FailureCode   GrantFailureCode
}

type GrantService struct {
	db           *gorm.DB
	leaseService *backupasset.LeaseService
	now          func() time.Time
	config       GrantConfig
	inputLimits  GrantLimits
	sinkLimits   GrantLimits
}

func NewGrantService(db *gorm.DB, leaseService *backupasset.LeaseService, now func() time.Time, config GrantConfig) (*GrantService, error) {
	if db == nil || leaseService == nil {
		return nil, fmt.Errorf("%w: grant dependencies unavailable", ErrInvalidContract)
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	inputLimits, sinkLimits, ok := normalizeGrantLimits(config)
	if config.TTL <= 0 || !ok {
		return nil, fmt.Errorf("%w: invalid grant configuration", ErrInvalidContract)
	}
	if config.Random == nil {
		config.Random = rand.Reader
	}
	return &GrantService{
		db: db, leaseService: leaseService, now: now, config: config,
		inputLimits: inputLimits, sinkLimits: sinkLimits,
	}, nil
}

func (service *GrantService) IssueAttemptGrants(ctx context.Context, request IssueGrantsRequest) (AttemptGrantMaterial, error) {
	if !validIssueGrantRequest(request) {
		return AttemptGrantMaterial{}, ErrGrantDenied
	}
	var result AttemptGrantMaterial
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		result, err = service.issueAttemptGrantsTx(ctx, tx, request)
		return err
	})
	if err != nil {
		return AttemptGrantMaterial{}, err
	}
	return result, nil
}

func (service *GrantService) issueAttemptGrantsTx(ctx context.Context, tx *gorm.DB, request IssueGrantsRequest) (AttemptGrantMaterial, error) {
	if service == nil || tx == nil || !validIssueGrantRequest(request) {
		return AttemptGrantMaterial{}, ErrGrantDenied
	}
	input, inputHash, err := service.newActivationMaterial()
	if err != nil {
		return AttemptGrantMaterial{}, err
	}
	sink, sinkHash, err := service.newActivationMaterial()
	if err != nil {
		return AttemptGrantMaterial{}, err
	}
	result := AttemptGrantMaterial{Input: input, Sink: sink}
	attempt, lease, err := service.validateAttemptFenceTx(ctx, tx, request.JobID, request.AttemptID, request.WorkerID)
	if err != nil || leaseFenceDifferent(leaseFenceFromRow(lease), request.RecoveryPointFence) {
		return AttemptGrantMaterial{}, ErrGrantDenied
	}
	if err := service.leaseService.ValidateFenceTx(ctx, tx, request.RecoveryPointFence); err != nil {
		return AttemptGrantMaterial{}, ErrGrantDenied
	}
	now := service.utcNow()
	expiresAt := minimumTime(now.Add(service.config.TTL), attempt.WorkerLeaseExpiresAt.UTC(), attempt.AbsoluteDeadline.UTC(), lease.LeaseExpiresAt.UTC(), lease.AbsoluteDeadline.UTC())
	if !expiresAt.After(now) {
		return AttemptGrantMaterial{}, ErrGrantDenied
	}
	rows := []model.BackupAssetProcessingGrant{
		service.newGrantRow(result.Input.GrantID, inputHash, GrantInput, request, expiresAt, now),
		service.newGrantRow(result.Sink.GrantID, sinkHash, GrantSink, request, expiresAt, now),
	}
	if err := tx.WithContext(ctx).Create(&rows).Error; err != nil {
		return AttemptGrantMaterial{}, fmt.Errorf("create processing grants: %w", err)
	}
	return result, nil
}

func (service *GrantService) Activate(ctx context.Context, request ActivateGrantRequest) (ActivatedGrant, error) {
	if !validActivateGrantRequest(request) {
		return ActivatedGrant{}, ErrGrantDenied
	}
	var activated ActivatedGrant
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		activated, err = service.activateTx(ctx, tx, request)
		return err
	})
	if err != nil {
		return ActivatedGrant{}, normalizeGrantDenial(err)
	}
	return activated, nil
}

func (service *GrantService) activateTx(ctx context.Context, tx *gorm.DB, request ActivateGrantRequest) (ActivatedGrant, error) {
	if service == nil || tx == nil || !validActivateGrantRequest(request) {
		return ActivatedGrant{}, ErrGrantDenied
	}
	attempt, err := service.loadAttemptForGrantTx(ctx, tx, request.JobID, request.AttemptID, request.WorkerID)
	if err != nil {
		return ActivatedGrant{}, ErrGrantDenied
	}
	var grant model.BackupAssetProcessingGrant
	result := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", request.GrantID).Limit(1).Find(&grant)
	if result.Error != nil || result.RowsAffected != 1 {
		return ActivatedGrant{}, ErrGrantDenied
	}
	now := service.utcNow()
	if grant.Kind != string(request.Kind) || grant.JobID != request.JobID || grant.AttemptID != request.AttemptID ||
		grant.WorkerID != request.WorkerID || grant.State != string(GrantIssued) || !now.Before(grant.ExpiresAt.UTC()) ||
		!constantTimeSecretMatch(grant.ActivationSecretHash, request.Secret) {
		return ActivatedGrant{}, ErrGrantDenied
	}
	lease, err := service.loadAttemptLeaseTx(ctx, tx, attempt)
	if err != nil || attempt.RecoveryPointFenceHash != grant.FenceHash || hashFence(lease.FenceToken) != grant.FenceHash {
		return ActivatedGrant{}, ErrGrantDenied
	}
	fence := leaseFenceFromRow(lease)
	if err := service.leaseService.ValidateFenceTx(ctx, tx, fence); err != nil {
		return ActivatedGrant{}, ErrGrantDenied
	}
	updated := tx.WithContext(ctx).Model(&model.BackupAssetProcessingGrant{}).
		Where("id = ? AND state = ? AND activation_secret_hash = ? AND version = ?", grant.ID, GrantIssued, grant.ActivationSecretHash, grant.Version).
		Updates(map[string]any{
			"state": string(GrantActive), "activation_secret_hash": "", "activated_at": now,
			"updated_at": now, "version": grant.Version + 1,
		})
	if updated.Error != nil || updated.RowsAffected != 1 {
		return ActivatedGrant{}, ErrGrantDenied
	}
	return ActivatedGrant{
		SessionID: grant.ID, Kind: request.Kind, ExpiresAt: grant.ExpiresAt.UTC(),
		Limits: GrantLimits{MaxRequests: grant.MaxRequests, MaxBytesPerRequest: grant.MaxBytesPerRequest, MaxCumulativeBytes: grant.MaxCumulativeBytes, MaxInFlight: grant.MaxInFlight},
	}, nil
}

func (service *GrantService) Reserve(ctx context.Context, request ReserveGrantRequest) (GrantReservation, error) {
	if !validReserveGrantRequest(request) {
		return GrantReservation{}, ErrGrantDenied
	}
	var reservation GrantReservation
	err := service.retryConflicts(ctx, func() error {
		return service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var locator model.BackupAssetProcessingGrant
			located := tx.WithContext(ctx).Select("id", "job_id", "attempt_id", "worker_id").
				Where("id = ?", request.GrantID).Limit(1).Find(&locator)
			if located.Error != nil || located.RowsAffected != 1 {
				return ErrGrantDenied
			}
			attempt, err := service.loadAttemptForGrantTx(ctx, tx, locator.JobID, locator.AttemptID, locator.WorkerID)
			if err != nil {
				return ErrGrantDenied
			}
			var grant model.BackupAssetProcessingGrant
			result := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", request.GrantID).Limit(1).Find(&grant)
			if result.Error != nil || result.RowsAffected != 1 || grant.State != string(GrantActive) || !service.utcNow().Before(grant.ExpiresAt.UTC()) ||
				grant.JobID != locator.JobID || grant.AttemptID != locator.AttemptID || grant.WorkerID != locator.WorkerID ||
				!requestKindMatchesGrant(request.Kind, GrantKind(grant.Kind)) {
				return ErrGrantDenied
			}
			lease, err := service.loadAttemptLeaseTx(ctx, tx, attempt)
			if err != nil || attempt.RecoveryPointFenceHash != grant.FenceHash || hashFence(lease.FenceToken) != grant.FenceHash {
				return ErrGrantDenied
			}
			if err := service.leaseService.ValidateFenceTx(ctx, tx, leaseFenceFromRow(lease)); err != nil {
				return ErrGrantDenied
			}
			if request.Bytes > grant.MaxBytesPerRequest || grant.RequestCount >= grant.MaxRequests || grant.InFlight >= grant.MaxInFlight ||
				request.Bytes > grant.MaxCumulativeBytes-grant.ConsumedBytes-grant.ReservedBytes {
				return ErrGrantBudgetExceeded
			}
			requestID, err := backupasset.NewOpaqueID()
			if err != nil {
				return err
			}
			now := service.utcNow()
			row := model.BackupAssetProcessingGrantRequest{
				ID: requestID, GrantID: grant.ID, RequestKind: string(request.Kind), RangeOffset: request.RangeOffset,
				RangeLength: request.RangeLength, State: string(GrantRequestReserved), ReservedBytes: request.Bytes,
				StartedAt: now, CreatedAt: now, UpdatedAt: now,
			}
			if err := tx.WithContext(ctx).Create(&row).Error; err != nil {
				return fmt.Errorf("create processing grant reservation: %w", err)
			}
			updated := tx.WithContext(ctx).Model(&model.BackupAssetProcessingGrant{}).Where("id = ? AND version = ?", grant.ID, grant.Version).
				Updates(map[string]any{
					"request_count": grant.RequestCount + 1, "reserved_bytes": grant.ReservedBytes + request.Bytes,
					"in_flight": grant.InFlight + 1, "updated_at": now, "version": grant.Version + 1,
				})
			if updated.Error != nil {
				return fmt.Errorf("reserve processing grant budget: %w", updated.Error)
			}
			if updated.RowsAffected != 1 {
				return errGrantReservationConflict
			}
			reservation = GrantReservation{ReservationID: row.ID, GrantID: grant.ID, Kind: request.Kind, ReservedBytes: request.Bytes}
			return nil
		})
	})
	if err != nil {
		return GrantReservation{}, normalizeGrantDenial(err)
	}
	return reservation, nil
}

func (service *GrantService) Finalize(ctx context.Context, request FinalizeGrantRequest) error {
	if backupasset.ValidateOpaqueID(request.ReservationID) != nil || !validFinalGrantRequest(request) {
		return ErrGrantDenied
	}
	return service.retryConflicts(ctx, func() error {
		return service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var reservation model.BackupAssetProcessingGrantRequest
			result := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", request.ReservationID).Limit(1).Find(&reservation)
			if result.Error != nil || result.RowsAffected != 1 ||
				(reservation.State != string(GrantRequestReserved) && reservation.State != string(GrantRequestStreaming)) {
				return ErrGrantDenied
			}
			var grant model.BackupAssetProcessingGrant
			result = tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", reservation.GrantID).Limit(1).Find(&grant)
			if result.Error != nil || result.RowsAffected != 1 || grant.InFlight <= 0 || grant.ReservedBytes < reservation.ReservedBytes {
				return ErrGrantDenied
			}
			charge := reservation.ReservedBytes
			providerBytes := reservation.ReservedBytes
			storedBytes := request.StoredBytes
			if request.EvidenceKnown {
				charge = request.ProviderBytes
				providerBytes = request.ProviderBytes
				if charge < 0 || charge > reservation.ReservedBytes || storedBytes < 0 || storedBytes > reservation.ReservedBytes {
					return ErrGrantDenied
				}
				if reservation.RequestKind == string(GrantRequestUpload) {
					charge = storedBytes
				}
			}
			now := service.utcNow()
			if err := tx.WithContext(ctx).Model(&model.BackupAssetProcessingGrantRequest{}).Where("id = ?", reservation.ID).
				Updates(map[string]any{
					"state": string(request.Outcome), "provider_bytes": providerBytes, "stored_bytes": storedBytes,
					"failure_code": string(request.FailureCode), "finished_at": now, "updated_at": now,
				}).Error; err != nil {
				return fmt.Errorf("finalize processing grant reservation: %w", err)
			}
			if err := tx.WithContext(ctx).Model(&model.BackupAssetProcessingGrant{}).Where("id = ? AND version = ?", grant.ID, grant.Version).
				Updates(map[string]any{
					"reserved_bytes": grant.ReservedBytes - reservation.ReservedBytes,
					"consumed_bytes": grant.ConsumedBytes + charge, "in_flight": grant.InFlight - 1,
					"updated_at": now, "version": grant.Version + 1,
				}).Error; err != nil {
				return fmt.Errorf("finalize processing grant budget: %w", err)
			}
			return nil
		})
	})
}

func (service *GrantService) ReserveAttemptRead(ctx context.Context, intent content.AttemptReadIntent) (content.AttemptReadReservation, error) {
	request := ReserveGrantRequest{GrantID: intent.SessionID, Bytes: intent.Bytes}
	switch intent.Mode {
	case content.SourceModeStat:
		request.Kind = GrantRequestStat
	case content.SourceModeSequential:
		request.Kind = GrantRequestSequential
	case content.SourceModeRange:
		request.Kind = GrantRequestRange
		request.RangeOffset = intent.Offset
		request.RangeLength = intent.Length
	default:
		return content.AttemptReadReservation{}, ErrGrantDenied
	}
	reservation, err := service.Reserve(ctx, request)
	if err != nil {
		if errors.Is(err, ErrGrantBudgetExceeded) {
			return content.AttemptReadReservation{}, content.ErrAttemptBudgetExceeded
		}
		return content.AttemptReadReservation{}, content.ErrAttemptSessionDenied
	}
	return content.AttemptReadReservation{ID: reservation.ReservationID, ReservedBytes: reservation.ReservedBytes}, nil
}

func (service *GrantService) FinalizeAttemptRead(ctx context.Context, finalization content.AttemptReadFinalization) error {
	request := FinalizeGrantRequest{
		ReservationID: finalization.ReservationID, ProviderBytes: finalization.ProviderBytes,
		EvidenceKnown: finalization.EvidenceKnown,
	}
	switch {
	case !finalization.EvidenceKnown:
		request.Outcome = GrantRequestReconciled
		request.FailureCode = GrantFailureReconciledCrash
	case finalization.Succeeded:
		request.Outcome = GrantRequestSucceeded
	case !finalization.Succeeded:
		request.Outcome = GrantRequestCanceled
		request.FailureCode = GrantFailureClientCanceled
	}
	return service.Finalize(ctx, request)
}

func (service *GrantService) RevokeAttempt(ctx context.Context, attemptID, reason string) error {
	if backupasset.ValidateOpaqueID(attemptID) != nil || !oneOf(reason, "cancel", "lease_lost", "source_changed", "expired", "quarantine", "shutdown") {
		return ErrGrantDenied
	}
	now := service.utcNow()
	return service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.BackupAssetProcessingGrantRequest{}).
			Where("grant_id IN (?) AND state IN ?", tx.Model(&model.BackupAssetProcessingGrant{}).Select("id").Where("attempt_id = ?", attemptID), []string{"reserved", "streaming"}).
			Updates(map[string]any{
				"state": string(GrantRequestReconciled), "provider_bytes": gorm.Expr("reserved_bytes"),
				"failure_code": string(GrantFailureReconciledCrash), "finished_at": now, "updated_at": now,
			}).Error; err != nil {
			return fmt.Errorf("reconcile revoked grant reservations: %w", err)
		}
		if err := tx.Model(&model.BackupAssetProcessingGrant{}).Where("attempt_id = ? AND state IN ?", attemptID, []string{"issued", "active"}).
			Updates(map[string]any{
				"state": string(GrantRevoked), "activation_secret_hash": "", "revoked_at": now,
				"revocation_reason": reason, "reserved_bytes": 0, "in_flight": 0,
				"consumed_bytes": gorm.Expr("consumed_bytes + reserved_bytes"),
				"updated_at":     now, "version": gorm.Expr("version + 1"),
			}).Error; err != nil {
			return fmt.Errorf("revoke attempt grants: %w", err)
		}
		return nil
	})
}

func (service *GrantService) renewAttemptGrantsTx(ctx context.Context, tx *gorm.DB, attemptID string, authorityExpiresAt time.Time) (time.Time, error) {
	if service == nil || tx == nil || backupasset.ValidateOpaqueID(attemptID) != nil || authorityExpiresAt.IsZero() {
		return time.Time{}, ErrGrantDenied
	}
	now := service.utcNow()
	expiresAt := minimumTime(now.Add(service.config.TTL), authorityExpiresAt.UTC())
	if !expiresAt.After(now) {
		return time.Time{}, ErrGrantDenied
	}
	var grants []model.BackupAssetProcessingGrant
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("attempt_id = ? AND state IN ?", attemptID, []string{string(GrantIssued), string(GrantActive)}).
		Find(&grants).Error; err != nil {
		return time.Time{}, fmt.Errorf("load processing grants for renewal: %w", err)
	}
	for _, grant := range grants {
		if !now.Before(grant.ExpiresAt.UTC()) {
			return time.Time{}, ErrGrantDenied
		}
	}
	if len(grants) == 0 {
		return expiresAt, nil
	}
	updated := tx.WithContext(ctx).Model(&model.BackupAssetProcessingGrant{}).
		Where("attempt_id = ? AND state IN ? AND expires_at > ?", attemptID, []string{string(GrantIssued), string(GrantActive)}, now).
		Updates(map[string]any{
			"expires_at": expiresAt, "updated_at": now, "version": gorm.Expr("version + 1"),
		})
	if updated.Error != nil {
		return time.Time{}, fmt.Errorf("renew processing grants: %w", updated.Error)
	}
	if updated.RowsAffected != int64(len(grants)) {
		return time.Time{}, ErrGrantDenied
	}
	return expiresAt, nil
}

var errGrantReservationConflict = errors.New("processing grant reservation conflict")

func (service *GrantService) retryConflicts(ctx context.Context, operation func() error) error {
	var lastErr error
	for attempt := 0; attempt < 20; attempt++ {
		err := operation()
		if err == nil {
			return nil
		}
		lastErr = err
		if !errors.Is(err, errGrantReservationConflict) && !retryableCoordinatorConflict(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("processing grant conflict: %w", ctx.Err())
		case <-time.After(time.Duration(attempt+1) * time.Millisecond):
		}
	}
	return fmt.Errorf("processing grant conflict after retries: %w", lastErr)
}

func (service *GrantService) validateAttemptFenceTx(ctx context.Context, tx *gorm.DB, jobID, attemptID, workerID string) (model.BackupAssetProcessingAttempt, model.RecoveryPointLease, error) {
	attempt, err := service.loadAttemptForGrantTx(ctx, tx, jobID, attemptID, workerID)
	if err != nil {
		return attempt, model.RecoveryPointLease{}, err
	}
	lease, err := service.loadAttemptLeaseTx(ctx, tx, attempt)
	return attempt, lease, err
}

func (service *GrantService) loadAttemptForGrantTx(ctx context.Context, tx *gorm.DB, jobID, attemptID, workerID string) (model.BackupAssetProcessingAttempt, error) {
	var attempt model.BackupAssetProcessingAttempt
	result := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND job_id = ? AND worker_id = ?", attemptID, jobID, workerID).Limit(1).Find(&attempt)
	if result.Error != nil || result.RowsAffected != 1 || attempt.State != "active" || !attempt.IsCurrent {
		return attempt, ErrGrantDenied
	}
	now := service.utcNow()
	if !now.Before(attempt.WorkerLeaseExpiresAt.UTC()) || !now.Before(attempt.AbsoluteDeadline.UTC()) {
		return attempt, ErrGrantDenied
	}
	var job model.BackupAssetProcessingJob
	result = tx.WithContext(ctx).Where("id = ? AND current_attempt_id = ? AND is_current = ?", jobID, attemptID, true).Limit(1).Find(&job)
	if result.Error != nil || result.RowsAffected != 1 {
		return attempt, ErrGrantDenied
	}
	return attempt, nil
}

func (service *GrantService) loadAttemptLeaseTx(ctx context.Context, tx *gorm.DB, attempt model.BackupAssetProcessingAttempt) (model.RecoveryPointLease, error) {
	var lease model.RecoveryPointLease
	result := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", attempt.RecoveryPointLeaseID).Limit(1).Find(&lease)
	if result.Error != nil || result.RowsAffected != 1 || lease.AttemptID != attempt.RecoveryPointAttemptID ||
		hashFence(lease.FenceToken) != attempt.RecoveryPointFenceHash {
		return model.RecoveryPointLease{}, ErrGrantDenied
	}
	return lease, nil
}

func (service *GrantService) newActivationMaterial() (GrantActivationMaterial, string, error) {
	grantID, err := backupasset.NewOpaqueID()
	if err != nil {
		return GrantActivationMaterial{}, "", err
	}
	raw := make([]byte, 32)
	if _, err := io.ReadFull(service.config.Random, raw); err != nil {
		return GrantActivationMaterial{}, "", fmt.Errorf("generate processing grant secret: %w", err)
	}
	secret := hex.EncodeToString(raw)
	digest := sha256.Sum256([]byte(secret))
	for index := range raw {
		raw[index] = 0
	}
	return GrantActivationMaterial{GrantID: grantID, Secret: secret}, hex.EncodeToString(digest[:]), nil
}

func (service *GrantService) newGrantRow(id, secretHash string, kind GrantKind, request IssueGrantsRequest, expiresAt, now time.Time) model.BackupAssetProcessingGrant {
	limits := service.inputLimits
	if kind == GrantSink {
		limits = service.sinkLimits
	}
	return model.BackupAssetProcessingGrant{
		ID: id, JobID: request.JobID, AttemptID: request.AttemptID, WorkerID: request.WorkerID,
		Kind: string(kind), ActivationSecretHash: secretHash, FenceHash: hashFence(request.RecoveryPointFence.FenceToken),
		State: string(GrantIssued), MaxRequests: limits.MaxRequests, MaxBytesPerRequest: limits.MaxBytesPerRequest,
		MaxCumulativeBytes: limits.MaxCumulativeBytes, MaxInFlight: limits.MaxInFlight,
		ExpiresAt: expiresAt, CreatedAt: now, UpdatedAt: now, Version: 1,
	}
}

func normalizeGrantLimits(config GrantConfig) (GrantLimits, GrantLimits, bool) {
	input, sink := config.InputLimits, config.SinkLimits
	if input == (GrantLimits{}) && sink == (GrantLimits{}) {
		legacy := GrantLimits{
			MaxRequests: config.MaxRequests, MaxBytesPerRequest: config.MaxBytesPerRequest,
			MaxCumulativeBytes: config.MaxCumulativeBytes, MaxInFlight: config.MaxInFlight,
		}
		return legacy, legacy, validGrantLimits(legacy)
	}
	if config.MaxRequests != 0 || config.MaxBytesPerRequest != 0 || config.MaxCumulativeBytes != 0 || config.MaxInFlight != 0 {
		return GrantLimits{}, GrantLimits{}, false
	}
	return input, sink, validGrantLimits(input) && validGrantLimits(sink)
}

func validGrantLimits(limits GrantLimits) bool {
	return limits.MaxRequests > 0 && limits.MaxBytesPerRequest > 0 &&
		limits.MaxCumulativeBytes >= limits.MaxBytesPerRequest && limits.MaxInFlight > 0
}

func leaseFenceFromRow(row model.RecoveryPointLease) backupasset.LeaseFence {
	return backupasset.LeaseFence{
		LeaseID: row.ID, RecoveryPointID: row.RecoveryPointID, HolderType: backupasset.LeaseHolderType(row.HolderType),
		OwnerID: row.OwnerID, AttemptID: row.AttemptID, FenceToken: row.FenceToken,
	}
}

func leaseFenceDifferent(left, right backupasset.LeaseFence) bool {
	return left.LeaseID != right.LeaseID || left.RecoveryPointID != right.RecoveryPointID || left.HolderType != right.HolderType ||
		left.OwnerID != right.OwnerID || left.AttemptID != right.AttemptID || left.FenceToken != right.FenceToken
}

func validIssueGrantRequest(request IssueGrantsRequest) bool {
	return backupasset.ValidateOpaqueID(request.JobID) == nil && backupasset.ValidateOpaqueID(request.AttemptID) == nil &&
		backupasset.ValidateOpaqueID(request.WorkerID) == nil && request.RecoveryPointFence.HolderType == backupasset.LeaseHolderProcessingJob
}

func validActivateGrantRequest(request ActivateGrantRequest) bool {
	return backupasset.ValidateOpaqueID(request.GrantID) == nil && backupasset.ValidateOpaqueID(request.JobID) == nil &&
		backupasset.ValidateOpaqueID(request.AttemptID) == nil && backupasset.ValidateOpaqueID(request.WorkerID) == nil &&
		(request.Kind == GrantInput || request.Kind == GrantSink) && lowerHex(request.Secret, 64)
}

func validReserveGrantRequest(request ReserveGrantRequest) bool {
	if backupasset.ValidateOpaqueID(request.GrantID) != nil || request.Bytes < 0 {
		return false
	}
	switch request.Kind {
	case GrantRequestStat:
		return request.Bytes == 0 && request.RangeOffset == nil && request.RangeLength == nil
	case GrantRequestSequential, GrantRequestUpload:
		return request.Bytes > 0 && request.RangeOffset == nil && request.RangeLength == nil
	case GrantRequestRange:
		return request.Bytes > 0 && request.RangeOffset != nil && request.RangeLength != nil && *request.RangeOffset >= 0 &&
			*request.RangeLength == request.Bytes && *request.RangeLength > 0
	default:
		return false
	}
}

func validFinalGrantRequest(request FinalizeGrantRequest) bool {
	if request.ProviderBytes < 0 || request.StoredBytes < 0 {
		return false
	}
	switch request.Outcome {
	case GrantRequestSucceeded:
		return request.EvidenceKnown && request.FailureCode == ""
	case GrantRequestFailed, GrantRequestCanceled:
		return request.EvidenceKnown && validGrantFailure(request.FailureCode)
	case GrantRequestReconciled:
		return !request.EvidenceKnown && request.FailureCode == GrantFailureReconciledCrash
	default:
		return false
	}
}

func validGrantFailure(value GrantFailureCode) bool {
	switch value {
	case GrantFailureBudgetExhausted, GrantFailureSourceChanged, GrantFailureLeaseLost, GrantFailureClientCanceled,
		GrantFailureSourceFailed, GrantFailureWriteFailed, GrantFailureReconciledCrash:
		return true
	default:
		return false
	}
}

func requestKindMatchesGrant(request GrantRequestKind, grant GrantKind) bool {
	return grant == GrantInput && (request == GrantRequestStat || request == GrantRequestSequential || request == GrantRequestRange) ||
		grant == GrantSink && request == GrantRequestUpload
}

func constantTimeSecretMatch(storedHash, secret string) bool {
	if !lowerHex(storedHash, 64) || !lowerHex(secret, 64) {
		return false
	}
	digest := sha256.Sum256([]byte(secret))
	want, err := hex.DecodeString(storedHash)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(want, digest[:]) == 1
}

func lowerHex(value string, size int) bool {
	if len(value) != size {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

func minimumTime(values ...time.Time) time.Time {
	minimum := values[0].UTC()
	for _, value := range values[1:] {
		if value.UTC().Before(minimum) {
			minimum = value.UTC()
		}
	}
	return minimum
}

func normalizeGrantDenial(err error) error {
	if errors.Is(err, ErrGrantDenied) || errors.Is(err, ErrGrantBudgetExceeded) {
		return err
	}
	return err
}

func (service *GrantService) utcNow() time.Time { return service.now().UTC() }
