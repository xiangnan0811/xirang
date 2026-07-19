package backupasset

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const maxLeaseAbsoluteDeadline = 168 * time.Hour

const LeaseHolderSearchIndex LeaseHolderType = "search_index"

type LeaseStatus string

const (
	LeaseActive   LeaseStatus = "active"
	LeaseReleased LeaseStatus = "released"
	LeaseExpired  LeaseStatus = "expired"
)

type LeaseConfig struct {
	Duration         time.Duration
	Heartbeat        time.Duration
	AbsoluteDeadline time.Duration
}

type AcquireLeaseRequest struct {
	RecoveryPointID  string
	HolderType       LeaseHolderType
	OwnerID          string
	AbsoluteDeadline time.Time
}

type TakeoverLeaseRequest struct {
	LeaseID string
	OwnerID string
}

type LeaseFence struct {
	LeaseID         string
	RecoveryPointID string
	HolderType      LeaseHolderType
	OwnerID         string
	AttemptID       string
	FenceToken      string
}

type Lease struct {
	ID               string
	RecoveryPointID  string
	HolderType       LeaseHolderType
	OwnerID          string
	Status           LeaseStatus
	LeaseExpiresAt   time.Time
	AbsoluteDeadline time.Time
	LastHeartbeatAt  time.Time
	ReleasedAt       *time.Time
	Fence            LeaseFence `json:"-"`
}

type LeaseService struct {
	db     *gorm.DB
	now    func() time.Time
	config LeaseConfig
}

func NewLeaseService(db *gorm.DB, now func() time.Time, config LeaseConfig) (*LeaseService, error) {
	if db == nil {
		return nil, fmt.Errorf("%w: lease database is unavailable", ErrInvalidState)
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	if err := validateLeaseConfig(config); err != nil {
		return nil, err
	}
	return &LeaseService{db: db, now: now, config: config}, nil
}

func (service *LeaseService) Acquire(ctx context.Context, request AcquireLeaseRequest) (Lease, error) {
	var lease Lease
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		lease, err = service.AcquireTx(ctx, tx, request)
		return err
	})
	if err != nil {
		return Lease{}, err
	}
	return lease, nil
}

func (service *LeaseService) AcquireTx(ctx context.Context, tx *gorm.DB, request AcquireLeaseRequest) (Lease, error) {
	if tx == nil {
		return Lease{}, fmt.Errorf("%w: lease transaction is unavailable", ErrInvalidState)
	}
	if err := validateAcquireLeaseRequest(request); err != nil {
		return Lease{}, err
	}
	now := service.utcNow()
	absoluteDeadline, err := service.resolveAcquireDeadlineTx(ctx, tx, request, now)
	if err != nil {
		return Lease{}, err
	}
	row, err := service.newLeaseRow(request, now, absoluteDeadline)
	if err != nil {
		return Lease{}, err
	}
	var activeCount int64
	if err := tx.WithContext(ctx).Model(&model.RecoveryPointLease{}).
		Where("recovery_point_id = ? AND holder_type = ? AND owner_id = ? AND status = ?",
			request.RecoveryPointID, request.HolderType, request.OwnerID, LeaseActive).
		Count(&activeCount).Error; err != nil {
		return Lease{}, fmt.Errorf("check active lease owner slot: %w", err)
	}
	if activeCount > 0 {
		return Lease{}, fmt.Errorf("%w: active owner slot exists", ErrLeaseHeld)
	}
	if err := tx.WithContext(ctx).Create(&row).Error; err != nil {
		if isLeaseConstraintConflict(err) {
			return Lease{}, fmt.Errorf("%w: active owner slot exists", ErrLeaseHeld)
		}
		return Lease{}, fmt.Errorf("create recovery point lease: %w", err)
	}
	return leaseFromModel(row)
}

func (service *LeaseService) Renew(ctx context.Context, fence LeaseFence) (Lease, error) {
	var renewed Lease
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		renewed, err = service.RenewTx(ctx, tx, fence)
		return err
	})
	if err != nil {
		return Lease{}, err
	}
	return renewed, nil
}

func (service *LeaseService) RenewTx(ctx context.Context, tx *gorm.DB, fence LeaseFence) (Lease, error) {
	if tx == nil {
		return Lease{}, fmt.Errorf("%w: lease transaction is unavailable", ErrInvalidState)
	}
	if err := validateLeaseFence(fence); err != nil {
		return Lease{}, err
	}
	now := service.utcNow()
	var current model.RecoveryPointLease
	loaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", fence.LeaseID).Limit(1).Find(&current)
	if loaded.Error != nil {
		return Lease{}, fmt.Errorf("load lease for renewal: %w", loaded.Error)
	}
	if loaded.RowsAffected != 1 || current.RecoveryPointID != fence.RecoveryPointID || current.HolderType != string(fence.HolderType) ||
		current.OwnerID != fence.OwnerID || current.AttemptID != fence.AttemptID || current.FenceToken != fence.FenceToken ||
		LeaseStatus(current.Status) != LeaseActive || !now.Before(current.LeaseExpiresAt.UTC()) || !now.Before(current.AbsoluteDeadline.UTC()) {
		return Lease{}, service.fenceFailureTx(ctx, tx, fence.LeaseID, now)
	}
	nextExpiry := now.Add(service.config.Duration)
	if nextExpiry.After(current.AbsoluteDeadline.UTC()) {
		nextExpiry = current.AbsoluteDeadline.UTC()
	}
	result := tx.WithContext(ctx).Model(&model.RecoveryPointLease{}).
		Where(`id = ? AND recovery_point_id = ? AND holder_type = ? AND owner_id = ? AND attempt_id = ? AND fence_token = ?
			AND status = ? AND lease_expires_at > ? AND absolute_deadline > ?`,
			fence.LeaseID, fence.RecoveryPointID, fence.HolderType, fence.OwnerID, fence.AttemptID, fence.FenceToken,
			LeaseActive, now, now).
		Updates(map[string]any{
			"lease_expires_at":  nextExpiry,
			"last_heartbeat_at": now,
			"updated_at":        now,
		})
	if result.Error != nil {
		return Lease{}, fmt.Errorf("renew recovery point lease: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return Lease{}, service.fenceFailureTx(ctx, tx, fence.LeaseID, now)
	}
	current.LeaseExpiresAt = nextExpiry
	current.LastHeartbeatAt = now
	current.UpdatedAt = now
	return leaseFromModel(current)
}

func (service *LeaseService) Release(ctx context.Context, fence LeaseFence) error {
	return service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return service.ReleaseTx(ctx, tx, fence)
	})
}

func (service *LeaseService) ReleaseTx(ctx context.Context, tx *gorm.DB, fence LeaseFence) error {
	if tx == nil {
		return fmt.Errorf("%w: lease transaction is unavailable", ErrInvalidState)
	}
	if err := validateLeaseFence(fence); err != nil {
		return err
	}
	now := service.utcNow()
	result := tx.WithContext(ctx).Model(&model.RecoveryPointLease{}).
		Where(`id = ? AND recovery_point_id = ? AND holder_type = ? AND owner_id = ? AND attempt_id = ? AND fence_token = ?
			AND status = ? AND lease_expires_at > ? AND absolute_deadline > ?`,
			fence.LeaseID, fence.RecoveryPointID, fence.HolderType, fence.OwnerID, fence.AttemptID, fence.FenceToken,
			LeaseActive, now, now).
		Updates(map[string]any{
			"status":      LeaseReleased,
			"released_at": now,
			"updated_at":  now,
		})
	if result.Error != nil {
		return fmt.Errorf("release recovery point lease: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return service.fenceFailureTx(ctx, tx, fence.LeaseID, now)
	}
	return nil
}

func (service *LeaseService) Takeover(ctx context.Context, request TakeoverLeaseRequest) (Lease, error) {
	if ValidateOpaqueID(request.LeaseID) != nil || !validLeaseOwnerID(request.OwnerID) {
		return Lease{}, fmt.Errorf("%w: invalid takeover request", ErrInvalidState)
	}
	var lastErr error
	for attempt := 0; attempt < 12; attempt++ {
		lease, err := service.takeoverOnce(ctx, request)
		if err == nil {
			return lease, nil
		}
		if errors.Is(err, ErrLeaseHeld) || errors.Is(err, ErrLeaseFenceLost) || errors.Is(err, ErrLeaseDeadlineExceeded) {
			return Lease{}, err
		}
		lastErr = err
		if !retryableLeaseConflict(err) {
			return Lease{}, err
		}
		delay := time.Duration(attempt+1) * time.Millisecond
		select {
		case <-ctx.Done():
			return Lease{}, fmt.Errorf("take over recovery point lease: %w", ctx.Err())
		case <-time.After(delay):
		}
	}
	return Lease{}, fmt.Errorf("take over recovery point lease after retries: %w", lastErr)
}

func (service *LeaseService) ValidateFence(ctx context.Context, fence LeaseFence) error {
	return service.ValidateFenceTx(ctx, service.db.WithContext(ctx), fence)
}

func (service *LeaseService) ValidateFenceTx(ctx context.Context, tx *gorm.DB, fence LeaseFence) error {
	if tx == nil {
		return fmt.Errorf("%w: lease transaction is unavailable", ErrInvalidState)
	}
	if err := validateLeaseFence(fence); err != nil {
		return err
	}
	now := service.utcNow()
	var count int64
	err := tx.WithContext(ctx).Model(&model.RecoveryPointLease{}).
		Where(`id = ? AND recovery_point_id = ? AND holder_type = ? AND owner_id = ? AND attempt_id = ? AND fence_token = ?
			AND status = ? AND lease_expires_at > ? AND absolute_deadline > ?`,
			fence.LeaseID, fence.RecoveryPointID, fence.HolderType, fence.OwnerID, fence.AttemptID, fence.FenceToken,
			LeaseActive, now, now).
		Count(&count).Error
	if err != nil {
		return fmt.Errorf("validate recovery point lease fence: %w", err)
	}
	if count != 1 {
		return service.fenceFailureTx(ctx, tx, fence.LeaseID, now)
	}
	return nil
}

func (service *LeaseService) ReconcileExpired(ctx context.Context) (int64, error) {
	now := service.utcNow()
	result := service.db.WithContext(ctx).Model(&model.RecoveryPointLease{}).
		Where("status = ? AND absolute_deadline <= ?", LeaseActive, now).
		Updates(map[string]any{
			"status":     LeaseExpired,
			"updated_at": now,
		})
	if result.Error != nil {
		return 0, fmt.Errorf("reconcile expired recovery point leases: %w", result.Error)
	}
	return result.RowsAffected, nil
}

func (service *LeaseService) takeoverOnce(ctx context.Context, request TakeoverLeaseRequest) (Lease, error) {
	var updated Lease
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		updated, err = service.TakeoverTx(ctx, tx, request)
		return err
	})
	if err != nil {
		return Lease{}, err
	}
	return updated, nil
}

func (service *LeaseService) TakeoverTx(ctx context.Context, tx *gorm.DB, request TakeoverLeaseRequest) (Lease, error) {
	if tx == nil {
		return Lease{}, fmt.Errorf("%w: lease transaction is unavailable", ErrInvalidState)
	}
	if ValidateOpaqueID(request.LeaseID) != nil || !validLeaseOwnerID(request.OwnerID) {
		return Lease{}, fmt.Errorf("%w: invalid takeover request", ErrInvalidState)
	}
	now := service.utcNow()
	var current model.RecoveryPointLease
	result := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", request.LeaseID).
		Limit(1).
		Find(&current)
	if result.Error != nil {
		return Lease{}, fmt.Errorf("load takeover lease: %w", result.Error)
	}
	if result.RowsAffected != 1 || current.OwnerID != request.OwnerID || LeaseStatus(current.Status) != LeaseActive {
		return Lease{}, fmt.Errorf("%w: takeover lease identity changed", ErrLeaseFenceLost)
	}
	if !now.Before(current.AbsoluteDeadline.UTC()) {
		return Lease{}, fmt.Errorf("%w: absolute lease deadline reached", ErrLeaseDeadlineExceeded)
	}
	if now.Before(current.LeaseExpiresAt.UTC()) {
		return Lease{}, fmt.Errorf("%w: short lease is still active", ErrLeaseHeld)
	}

	attemptID, err := NewOpaqueID()
	if err != nil {
		return Lease{}, err
	}
	fenceToken, err := newFenceToken()
	if err != nil {
		return Lease{}, err
	}
	nextExpiry := now.Add(service.config.Duration)
	if nextExpiry.After(current.AbsoluteDeadline.UTC()) {
		nextExpiry = current.AbsoluteDeadline.UTC()
	}
	result = tx.WithContext(ctx).Model(&model.RecoveryPointLease{}).
		Where(`id = ? AND owner_id = ? AND attempt_id = ? AND fence_token = ? AND status = ?
			AND lease_expires_at <= ? AND absolute_deadline > ?`,
			current.ID, current.OwnerID, current.AttemptID, current.FenceToken, LeaseActive, now, now).
		Updates(map[string]any{
			"attempt_id":        attemptID,
			"fence_token":       fenceToken,
			"lease_expires_at":  nextExpiry,
			"last_heartbeat_at": now,
			"released_at":       nil,
			"updated_at":        now,
		})
	if result.Error != nil {
		return Lease{}, fmt.Errorf("update takeover lease: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return Lease{}, fmt.Errorf("%w: concurrent takeover won", ErrConflict)
	}
	current.AttemptID = attemptID
	current.FenceToken = fenceToken
	current.LeaseExpiresAt = nextExpiry
	current.LastHeartbeatAt = now
	current.ReleasedAt = nil
	current.UpdatedAt = now
	return leaseFromModel(current)
}

func (service *LeaseService) resolveAcquireDeadlineTx(ctx context.Context, tx *gorm.DB, request AcquireLeaseRequest, now time.Time) (time.Time, error) {
	// Existing callers do not coordinate stages with a point-wide deadline.
	// Preserve their historical behavior: each zero-deadline acquisition gets
	// a fresh deadline from the service configuration. Only explicit deadlines
	// participate in the immutable publication-stage contract below.
	if request.AbsoluteDeadline.IsZero() {
		return now.Add(service.config.AbsoluteDeadline), nil
	}

	var previous model.RecoveryPointLease
	result := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("recovery_point_id = ?", request.RecoveryPointID).
		Order("created_at DESC, id DESC").
		Limit(1).
		Find(&previous)
	if result.Error != nil {
		return time.Time{}, fmt.Errorf("load prior point lease deadline: %w", result.Error)
	}
	if result.RowsAffected == 1 {
		deadline := previous.AbsoluteDeadline.UTC()
		if !request.AbsoluteDeadline.IsZero() && !request.AbsoluteDeadline.UTC().Equal(deadline) {
			return time.Time{}, fmt.Errorf("%w: fresh lease stage changed point deadline", ErrConflict)
		}
		if !now.Before(deadline) {
			return time.Time{}, fmt.Errorf("%w: absolute lease deadline reached", ErrLeaseDeadlineExceeded)
		}
		return deadline, nil
	}

	deadline := request.AbsoluteDeadline.UTC()
	if !now.Before(deadline) {
		return time.Time{}, fmt.Errorf("%w: absolute lease deadline reached", ErrLeaseDeadlineExceeded)
	}
	if deadline.After(now.Add(maxLeaseAbsoluteDeadline)) {
		return time.Time{}, fmt.Errorf("%w: supplied absolute deadline exceeds maximum", ErrInvalidState)
	}
	return deadline, nil
}

func (service *LeaseService) newLeaseRow(request AcquireLeaseRequest, now, absoluteDeadline time.Time) (model.RecoveryPointLease, error) {
	id, err := NewOpaqueID()
	if err != nil {
		return model.RecoveryPointLease{}, err
	}
	attemptID, err := NewOpaqueID()
	if err != nil {
		return model.RecoveryPointLease{}, err
	}
	fenceToken, err := newFenceToken()
	if err != nil {
		return model.RecoveryPointLease{}, err
	}
	leaseExpiresAt := now.Add(service.config.Duration)
	if leaseExpiresAt.After(absoluteDeadline) {
		leaseExpiresAt = absoluteDeadline
	}
	return model.RecoveryPointLease{
		ID:               id,
		RecoveryPointID:  request.RecoveryPointID,
		HolderType:       string(request.HolderType),
		OwnerID:          request.OwnerID,
		AttemptID:        attemptID,
		FenceToken:       fenceToken,
		Status:           string(LeaseActive),
		LeaseExpiresAt:   leaseExpiresAt,
		AbsoluteDeadline: absoluteDeadline,
		LastHeartbeatAt:  now,
		CreatedAt:        now,
		UpdatedAt:        now,
	}, nil
}

func (service *LeaseService) fenceFailureTx(ctx context.Context, tx *gorm.DB, leaseID string, now time.Time) error {
	var row model.RecoveryPointLease
	result := tx.WithContext(ctx).Select("id", "absolute_deadline").Where("id = ?", leaseID).Limit(1).Find(&row)
	if result.Error != nil {
		return fmt.Errorf("classify recovery point lease fence: %w", result.Error)
	}
	if result.RowsAffected == 1 && !now.Before(row.AbsoluteDeadline.UTC()) {
		return fmt.Errorf("%w: absolute lease deadline reached", ErrLeaseDeadlineExceeded)
	}
	return fmt.Errorf("%w: lease fence no longer matches", ErrLeaseFenceLost)
}

func leaseFromModel(row model.RecoveryPointLease) (Lease, error) {
	holder := LeaseHolderType(row.HolderType)
	status := LeaseStatus(row.Status)
	if ValidateOpaqueID(row.ID) != nil || ValidateOpaqueID(row.RecoveryPointID) != nil ||
		ValidateOpaqueID(row.AttemptID) != nil || !isLowerHex(row.FenceToken, 64) ||
		!validLeaseHolderTypes[holder] || !validLeaseStatuses[status] || !validLeaseOwnerID(row.OwnerID) {
		return Lease{}, fmt.Errorf("%w: invalid persisted recovery point lease", ErrInvalidState)
	}
	releasedAt := row.ReleasedAt
	if releasedAt != nil {
		utc := releasedAt.UTC()
		releasedAt = &utc
	}
	fence := LeaseFence{
		LeaseID:         row.ID,
		RecoveryPointID: row.RecoveryPointID,
		HolderType:      holder,
		OwnerID:         row.OwnerID,
		AttemptID:       row.AttemptID,
		FenceToken:      row.FenceToken,
	}
	return Lease{
		ID:               row.ID,
		RecoveryPointID:  row.RecoveryPointID,
		HolderType:       holder,
		OwnerID:          row.OwnerID,
		Status:           status,
		LeaseExpiresAt:   row.LeaseExpiresAt.UTC(),
		AbsoluteDeadline: row.AbsoluteDeadline.UTC(),
		LastHeartbeatAt:  row.LastHeartbeatAt.UTC(),
		ReleasedAt:       releasedAt,
		Fence:            fence,
	}, nil
}

func validateLeaseConfig(config LeaseConfig) error {
	if config.Duration <= 0 || config.Heartbeat <= 0 || config.Heartbeat >= config.Duration ||
		config.AbsoluteDeadline < config.Duration || config.AbsoluteDeadline > maxLeaseAbsoluteDeadline {
		return fmt.Errorf("%w: invalid lease configuration", ErrInvalidState)
	}
	return nil
}

func validateAcquireLeaseRequest(request AcquireLeaseRequest) error {
	if ValidateOpaqueID(request.RecoveryPointID) != nil || !validLeaseHolderTypes[request.HolderType] || !validLeaseOwnerID(request.OwnerID) {
		return fmt.Errorf("%w: invalid acquire lease request", ErrInvalidState)
	}
	return nil
}

func validateLeaseFence(fence LeaseFence) error {
	if ValidateOpaqueID(fence.LeaseID) != nil || ValidateOpaqueID(fence.RecoveryPointID) != nil ||
		ValidateOpaqueID(fence.AttemptID) != nil || !isLowerHex(fence.FenceToken, 64) ||
		!validLeaseHolderTypes[fence.HolderType] || !validLeaseOwnerID(fence.OwnerID) {
		return fmt.Errorf("%w: malformed lease fence", ErrLeaseFenceLost)
	}
	return nil
}

func validLeaseOwnerID(ownerID string) bool {
	trimmed := strings.TrimSpace(ownerID)
	return trimmed != "" && trimmed == ownerID && len(ownerID) <= 64 && !strings.ContainsAny(ownerID, "\r\n\x00")
}

func newFenceToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate lease fence: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

func (service *LeaseService) utcNow() time.Time {
	return service.now().UTC()
}

func isLeaseConstraintConflict(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unique constraint") ||
		strings.Contains(message, "duplicate key") ||
		strings.Contains(message, "sqlstate 23505")
}

func retryableLeaseConflict(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "locked") || strings.Contains(message, "busy")
}

var (
	validLeaseHolderTypes = setOf(
		LeaseHolderRsyncParent, LeaseHolderCatalogBuild, LeaseHolderContentSession,
		LeaseHolderProcessingJob, LeaseHolderExportJob, LeaseHolderRecoveryJob, LeaseHolderPointPublication,
		LeaseHolderSearchIndex,
	)
	validLeaseStatuses = setOf(LeaseActive, LeaseReleased, LeaseExpired)
)
