package retention

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	maxHoldReasonBytes            = 4096
	maxOperationalHoldExpiryBatch = 1000
	operationalExpiryReason       = "operational hold expired"
)

type HoldServiceDependencies struct {
	DB    *gorm.DB
	Now   func() time.Time
	NewID func() (string, error)
	Audit MutationAuditor
}

type LifecycleHoldAdmission interface {
	ValidateHoldAdmissionTx(context.Context, *gorm.DB, string) error
}

type HoldService struct {
	db          *gorm.DB
	now         func() time.Time
	newID       func() (string, error)
	audit       MutationAuditor
	admissionMu sync.RWMutex
	admission   LifecycleHoldAdmission
}

type CreateHoldRequest struct {
	Actor           backupasset.AuditActor
	RecoveryPointID string
	HoldType        backupasset.RecoveryPointHoldType
	Reason          string `json:"-"`
	ExpiresAt       *time.Time
}

func (CreateHoldRequest) String() string   { return "[recovery point hold create request]" }
func (CreateHoldRequest) GoString() string { return "[recovery point hold create request]" }

type ReleaseHoldRequest struct {
	Actor           backupasset.AuditActor
	RecoveryPointID string
	HoldID          string
	Reason          string `json:"-"`
}

func (ReleaseHoldRequest) String() string   { return "[recovery point hold release request]" }
func (ReleaseHoldRequest) GoString() string { return "[recovery point hold release request]" }

type HoldRecord struct {
	ID              string                            `json:"id"`
	RecoveryPointID string                            `json:"recovery_point_id"`
	HoldType        backupasset.RecoveryPointHoldType `json:"hold_type"`
	State           backupasset.HoldState             `json:"state"`
	CreatedBy       uint                              `json:"created_by"`
	ExpiresAt       *time.Time                        `json:"expires_at,omitempty"`
	ReleasedBy      *uint                             `json:"released_by,omitempty"`
	ReleasedAt      *time.Time                        `json:"released_at,omitempty"`
	CreatedAt       time.Time                         `json:"created_at"`
	UpdatedAt       time.Time                         `json:"updated_at"`
}

func NewHoldService(dependencies HoldServiceDependencies) (*HoldService, error) {
	if dependencies.DB == nil {
		return nil, fmt.Errorf("%w: recovery point hold database is unavailable", backupasset.ErrInvalidState)
	}
	if dependencies.Now == nil {
		dependencies.Now = func() time.Time { return time.Now().UTC() }
	}
	if dependencies.NewID == nil {
		dependencies.NewID = backupasset.NewOpaqueID
	}
	return &HoldService{db: dependencies.DB, now: dependencies.Now, newID: dependencies.NewID, audit: dependencies.Audit}, nil
}

func (service *HoldService) AuditsMutations() bool {
	return service != nil && service.audit != nil
}

func (service *HoldService) SetLifecycleHoldAdmission(admission LifecycleHoldAdmission) {
	service.admissionMu.Lock()
	service.admission = admission
	service.admissionMu.Unlock()
}

func (service *HoldService) Create(ctx context.Context, request CreateHoldRequest) (HoldRecord, error) {
	if err := validateAdminActor(request.Actor); err != nil {
		return HoldRecord{}, err
	}
	now := service.utcNow()
	reason, expiresAt, err := validateCreateHoldRequest(request, now)
	if err != nil {
		return HoldRecord{}, err
	}
	hold := model.RecoveryPointHold{
		RecoveryPointID: request.RecoveryPointID, HoldType: string(request.HoldType), State: string(backupasset.HoldActive),
		EncryptedReason: reason, CreatedBy: request.Actor.UserID, ExpiresAt: expiresAt, CreatedAt: now, UpdatedAt: now,
	}
	err = service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		point, err := lockHoldPoint(tx, request.RecoveryPointID)
		if err != nil {
			return err
		}
		service.admissionMu.RLock()
		admission := service.admission
		service.admissionMu.RUnlock()
		if admission != nil {
			if err := admission.ValidateHoldAdmissionTx(ctx, tx, request.RecoveryPointID); err != nil {
				return err
			}
		}
		if !holdEligibleRecoveryPoint(point) {
			return fmt.Errorf("%w: hold requires an immutable non-terminal recovery point", backupasset.ErrConflict)
		}
		var activeCount int64
		if err := tx.Model(&model.RecoveryPointHold{}).
			Where("recovery_point_id = ? AND hold_type = ? AND state = ?", request.RecoveryPointID, request.HoldType, backupasset.HoldActive).
			Count(&activeCount).Error; err != nil {
			return fmt.Errorf("load active recovery point hold: %w", err)
		}
		if activeCount != 0 {
			return fmt.Errorf("%w: active recovery point hold type", backupasset.ErrConflict)
		}
		id, err := service.newID()
		if err != nil || backupasset.ValidateOpaqueID(id) != nil {
			return fmt.Errorf("%w: generate recovery point hold ID", backupasset.ErrInvalidState)
		}
		hold.ID = id
		if err := tx.Create(&hold).Error; err != nil {
			return mapHoldCreateError(tx, request.RecoveryPointID, request.HoldType, err)
		}
		if err := updatePointHoldProjection(tx, request.RecoveryPointID, now); err != nil {
			return err
		}
		return writeMutationAuditTx(ctx, tx, service.audit, mutationAuditInput(
			ctx, request.Actor, backupasset.AuditActionHoldCreate, "", request.RecoveryPointID, 1, "",
		))
	})
	if err != nil {
		return HoldRecord{}, err
	}
	return holdRecordFromModel(hold), nil
}

func (service *HoldService) List(ctx context.Context, actor backupasset.AuditActor, recoveryPointID string) ([]HoldRecord, error) {
	if err := validateAdminActor(actor); err != nil {
		return nil, err
	}
	if backupasset.ValidateOpaqueID(recoveryPointID) != nil {
		return nil, fmt.Errorf("%w: invalid recovery point hold list", backupasset.ErrInvalidState)
	}
	var holds []HoldRecord
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := lockHoldPoint(tx, recoveryPointID); err != nil {
			return err
		}
		var rows []model.RecoveryPointHold
		if err := tx.Where("recovery_point_id = ? AND state = ?", recoveryPointID, backupasset.HoldActive).
			Order("hold_type ASC, id ASC").Find(&rows).Error; err != nil {
			return fmt.Errorf("list recovery point holds: %w", err)
		}
		holds = make([]HoldRecord, 0, len(rows))
		for _, row := range rows {
			holds = append(holds, holdRecordFromModel(row))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return holds, nil
}

func (service *HoldService) Release(ctx context.Context, request ReleaseHoldRequest) (HoldRecord, error) {
	if err := validateAdminActor(request.Actor); err != nil {
		return HoldRecord{}, err
	}
	if backupasset.ValidateOpaqueID(request.RecoveryPointID) != nil || backupasset.ValidateOpaqueID(request.HoldID) != nil {
		return HoldRecord{}, fmt.Errorf("%w: invalid recovery point hold release", backupasset.ErrInvalidState)
	}
	reason, err := validateHoldReason(request.Reason)
	if err != nil {
		return HoldRecord{}, err
	}
	now := service.utcNow()
	var hold model.RecoveryPointHold
	err = service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := lockHoldPoint(tx, request.RecoveryPointID); err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&hold, "id = ? AND recovery_point_id = ?", request.HoldID, request.RecoveryPointID).Error; err != nil {
			return mapHoldLookupError(err)
		}
		if hold.State != string(backupasset.HoldActive) {
			return fmt.Errorf("%w: recovery point hold is already released", backupasset.ErrConflict)
		}
		sensitive := model.RecoveryPointHold{EncryptedReleaseReason: reason}
		if err := sensitive.BeforeSave(nil); err != nil {
			return fmt.Errorf("encrypt recovery point hold release reason: %w", err)
		}
		result := tx.Model(&model.RecoveryPointHold{}).
			Where("id = ? AND recovery_point_id = ? AND state = ?", request.HoldID, request.RecoveryPointID, backupasset.HoldActive).
			Updates(map[string]any{
				"state": backupasset.HoldReleased, "released_by": request.Actor.UserID, "released_at": now,
				"encrypted_release_reason": sensitive.EncryptedReleaseReason, "updated_at": now,
			})
		if result.Error != nil {
			return fmt.Errorf("release recovery point hold: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("%w: recovery point hold release", backupasset.ErrConflict)
		}
		hold.State = string(backupasset.HoldReleased)
		hold.ReleasedBy = uintPointer(request.Actor.UserID)
		hold.ReleasedAt = &now
		hold.UpdatedAt = now
		if err := updatePointHoldProjection(tx, request.RecoveryPointID, now); err != nil {
			return err
		}
		return writeMutationAuditTx(ctx, tx, service.audit, mutationAuditInput(
			ctx, request.Actor, backupasset.AuditActionHoldRelease, "", request.RecoveryPointID, 1, "",
		))
	})
	if err != nil {
		return HoldRecord{}, err
	}
	return holdRecordFromModel(hold), nil
}

func (service *HoldService) ExpireOperational(ctx context.Context, actor backupasset.AuditActor, limit int) ([]HoldRecord, error) {
	if err := validateAdminActor(actor); err != nil {
		return nil, err
	}
	return service.expireOperationalBatch(ctx, actor, uintPointer(actor.UserID), limit)
}

func (service *HoldService) ExpireOperationalMaintenance(ctx context.Context, limit int) ([]HoldRecord, error) {
	return service.expireOperationalBatch(ctx, backupasset.AuditActor{Username: "system", Role: "system"}, nil, limit)
}

func (service *HoldService) expireOperationalBatch(ctx context.Context, actor backupasset.AuditActor, releasedBy *uint, limit int) ([]HoldRecord, error) {
	if service == nil || service.db == nil {
		return nil, fmt.Errorf("%w: recovery point hold service is unavailable", backupasset.ErrInvalidState)
	}
	if limit < 1 || limit > maxOperationalHoldExpiryBatch {
		return nil, fmt.Errorf("%w: invalid operational hold expiry batch", backupasset.ErrInvalidState)
	}
	now := service.utcNow()
	var candidates []expiryHoldCandidate
	if err := service.db.WithContext(ctx).Table("recovery_point_holds").
		Select("id", "recovery_point_id").
		Where("hold_type = ? AND state = ? AND expires_at IS NOT NULL AND expires_at <= ?",
			backupasset.RecoveryPointHoldOperational, backupasset.HoldActive, now).
		Order("expires_at ASC, id ASC").Limit(limit).Scan(&candidates).Error; err != nil {
		return nil, fmt.Errorf("load expired operational holds: %w", err)
	}
	result := make([]HoldRecord, 0, len(candidates))
	for _, candidate := range candidates {
		released, changed, err := service.expireOperationalHold(ctx, actor, releasedBy, candidate, now)
		if err != nil {
			return nil, err
		}
		if changed {
			result = append(result, released)
		}
	}
	return result, nil
}

func (service *HoldService) utcNow() time.Time { return service.now().UTC() }

func validateCreateHoldRequest(request CreateHoldRequest, now time.Time) (string, *time.Time, error) {
	if backupasset.ValidateOpaqueID(request.RecoveryPointID) != nil {
		return "", nil, fmt.Errorf("%w: invalid recovery point hold target", backupasset.ErrInvalidState)
	}
	if err := backupasset.ValidateRecoveryPointHoldType(request.HoldType); err != nil {
		return "", nil, err
	}
	reason, err := validateHoldReason(request.Reason)
	if err != nil {
		return "", nil, err
	}
	switch request.HoldType {
	case backupasset.RecoveryPointHoldOperational:
		if request.ExpiresAt == nil || !request.ExpiresAt.After(now) {
			return "", nil, fmt.Errorf("%w: operational hold requires a future expiry", backupasset.ErrInvalidState)
		}
		expiresAt := request.ExpiresAt.UTC()
		return reason, &expiresAt, nil
	case backupasset.RecoveryPointHoldLegal:
		if request.ExpiresAt != nil {
			return "", nil, fmt.Errorf("%w: legal hold cannot expire automatically", backupasset.ErrInvalidState)
		}
		return reason, nil, nil
	default:
		return "", nil, fmt.Errorf("%w: invalid recovery point hold type", backupasset.ErrInvalidState)
	}
}

func validateHoldReason(value string) (string, error) {
	value = strings.TrimSpace(value)
	if len(value) == 0 || len(value) > maxHoldReasonBytes || strings.ContainsRune(value, '\x00') {
		return "", fmt.Errorf("%w: invalid recovery point hold reason", backupasset.ErrInvalidState)
	}
	return value, nil
}

func lockHoldPoint(tx *gorm.DB, pointID string) (model.RecoveryPoint, error) {
	var point model.RecoveryPoint
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&point, "id = ?", pointID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.RecoveryPoint{}, fmt.Errorf("%w: recovery point hold target", backupasset.ErrNotFound)
		}
		return model.RecoveryPoint{}, fmt.Errorf("load recovery point hold target: %w", err)
	}
	return point, nil
}

func holdEligibleRecoveryPoint(point model.RecoveryPoint) bool {
	semantics := backupasset.PointVersionSemantics(point.Semantics)
	state := backupasset.RecoveryPointState(point.State)
	return semantics != backupasset.PointMutableHead &&
		(semantics == backupasset.PointNativeSnapshot || semantics == backupasset.PointXirangManifest || semantics == backupasset.PointImportedBaseline) &&
		(state == backupasset.RecoveryPointCommitted || state == backupasset.RecoveryPointDegraded || state == backupasset.RecoveryPointPurgeBlocked)
}

type activeHoldProjection struct {
	HoldType  string
	ExpiresAt *time.Time
}

type expiryHoldCandidate struct {
	ID              string
	RecoveryPointID string
}

type expiryHoldRow struct {
	ID              string
	RecoveryPointID string
	HoldType        string
	State           string
	CreatedBy       uint
	ExpiresAt       *time.Time
	ReleasedBy      *uint
	ReleasedAt      *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (service *HoldService) expireOperationalHold(
	ctx context.Context,
	actor backupasset.AuditActor,
	releasedBy *uint,
	candidate expiryHoldCandidate,
	now time.Time,
) (HoldRecord, bool, error) {
	var released HoldRecord
	changed := false
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := lockHoldPoint(tx, candidate.RecoveryPointID); err != nil {
			return err
		}
		var hold expiryHoldRow
		if err := tx.Table("recovery_point_holds").Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND recovery_point_id = ?", candidate.ID, candidate.RecoveryPointID).Take(&hold).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return fmt.Errorf("load expired operational hold: %w", err)
		}
		if hold.HoldType != string(backupasset.RecoveryPointHoldOperational) || hold.State != string(backupasset.HoldActive) ||
			hold.ExpiresAt == nil || hold.ExpiresAt.After(now) {
			return nil
		}
		sensitive := model.RecoveryPointHold{EncryptedReleaseReason: operationalExpiryReason}
		if err := sensitive.BeforeSave(nil); err != nil {
			return fmt.Errorf("encrypt operational hold expiry reason: %w", err)
		}
		result := tx.Model(&model.RecoveryPointHold{}).
			Where("id = ? AND recovery_point_id = ? AND hold_type = ? AND state = ? AND expires_at <= ?",
				hold.ID, hold.RecoveryPointID, backupasset.RecoveryPointHoldOperational, backupasset.HoldActive, now).
			Updates(map[string]any{
				"state": backupasset.HoldReleased, "released_by": releasedBy, "released_at": now,
				"encrypted_release_reason": sensitive.EncryptedReleaseReason, "updated_at": now,
			})
		if result.Error != nil {
			return fmt.Errorf("expire operational hold: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return nil
		}
		if err := updatePointHoldProjection(tx, hold.RecoveryPointID, now); err != nil {
			return err
		}
		if err := writeMutationAuditTx(ctx, tx, service.audit, mutationAuditInput(
			ctx, actor, backupasset.AuditActionHoldRelease, "", hold.RecoveryPointID, 1, "",
		)); err != nil {
			return err
		}
		hold.State = string(backupasset.HoldReleased)
		hold.ReleasedBy = releasedBy
		hold.ReleasedAt = &now
		hold.UpdatedAt = now
		released = holdRecordFromExpiryRow(hold)
		changed = true
		return nil
	})
	return released, changed, err
}

func updatePointHoldProjection(tx *gorm.DB, pointID string, now time.Time) error {
	var active []activeHoldProjection
	if err := tx.Table("recovery_point_holds").Select("hold_type", "expires_at").
		Where("recovery_point_id = ? AND state = ?", pointID, backupasset.HoldActive).
		Order("hold_type ASC").Scan(&active).Error; err != nil {
		return fmt.Errorf("load active recovery point hold projection: %w", err)
	}
	state := backupasset.HoldReleased
	var holdUntil *time.Time
	if len(active) != 0 {
		state = backupasset.HoldActive
		for _, hold := range active {
			if backupasset.RecoveryPointHoldType(hold.HoldType) == backupasset.RecoveryPointHoldLegal {
				holdUntil = nil
				break
			}
			if hold.ExpiresAt != nil && (holdUntil == nil || hold.ExpiresAt.After(*holdUntil)) {
				value := hold.ExpiresAt.UTC()
				holdUntil = &value
			}
		}
	}
	result := tx.Model(&model.RecoveryPoint{}).Where("id = ?", pointID).
		Updates(map[string]any{
			"hold_state": state, "hold_until": holdUntil, "updated_at": now,
			"point_revision": gorm.Expr("point_revision + 1"),
		})
	if result.Error != nil {
		return fmt.Errorf("update recovery point hold projection: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("%w: recovery point hold target changed", backupasset.ErrConflict)
	}
	return nil
}

func mapHoldCreateError(tx *gorm.DB, pointID string, holdType backupasset.RecoveryPointHoldType, createErr error) error {
	var activeCount int64
	if err := tx.Model(&model.RecoveryPointHold{}).
		Where("recovery_point_id = ? AND hold_type = ? AND state = ?", pointID, holdType, backupasset.HoldActive).
		Count(&activeCount).Error; err == nil && activeCount != 0 {
		return fmt.Errorf("%w: active recovery point hold type", backupasset.ErrConflict)
	}
	return fmt.Errorf("create recovery point hold: %w", createErr)
}

func mapHoldLookupError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("%w: recovery point hold", backupasset.ErrNotFound)
	}
	return fmt.Errorf("load recovery point hold: %w", err)
}

func holdRecordFromModel(hold model.RecoveryPointHold) HoldRecord {
	return HoldRecord{
		ID: hold.ID, RecoveryPointID: hold.RecoveryPointID,
		HoldType: backupasset.RecoveryPointHoldType(hold.HoldType), State: backupasset.HoldState(hold.State),
		CreatedBy: hold.CreatedBy, ExpiresAt: utcTimePointer(hold.ExpiresAt), ReleasedBy: copyUintPointer(hold.ReleasedBy),
		ReleasedAt: utcTimePointer(hold.ReleasedAt), CreatedAt: hold.CreatedAt.UTC(), UpdatedAt: hold.UpdatedAt.UTC(),
	}
}

func holdRecordFromExpiryRow(hold expiryHoldRow) HoldRecord {
	return HoldRecord{
		ID: hold.ID, RecoveryPointID: hold.RecoveryPointID,
		HoldType: backupasset.RecoveryPointHoldType(hold.HoldType), State: backupasset.HoldState(hold.State),
		CreatedBy: hold.CreatedBy, ExpiresAt: utcTimePointer(hold.ExpiresAt), ReleasedBy: copyUintPointer(hold.ReleasedBy),
		ReleasedAt: utcTimePointer(hold.ReleasedAt), CreatedAt: hold.CreatedAt.UTC(), UpdatedAt: hold.UpdatedAt.UTC(),
	}
}

func uintPointer(value uint) *uint { return &value }

func copyUintPointer(value *uint) *uint {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}
