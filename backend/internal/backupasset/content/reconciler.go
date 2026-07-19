package content

import (
	"context"
	"errors"
	"fmt"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

var ErrInvalidReconciler = errors.New("invalid content reconciler")

type ReconcilerBudget interface {
	Finalize(context.Context, FinalizeIntent) (Finalization, error)
}

type ReconcilerAudit interface {
	FlushGrant(context.Context, string) error
}

type ReconcilerLeaseExpiry interface {
	ReconcileExpired(context.Context) (int64, error)
}

type ReconcilerDependencies struct {
	DB        *gorm.DB
	Budget    ReconcilerBudget
	Audit     ReconcilerAudit
	Lease     ContentLeaseController
	Now       func() time.Time
	BatchSize int
	Metrics   Metrics
}

type Reconciler struct {
	db        *gorm.DB
	budget    ReconcilerBudget
	audit     ReconcilerAudit
	lease     ContentLeaseController
	now       func() time.Time
	batchSize int
	metrics   Metrics
}

func NewReconciler(dependencies ReconcilerDependencies) (*Reconciler, error) {
	if dependencies.DB == nil || dependencies.Budget == nil || dependencies.Audit == nil ||
		dependencies.Lease == nil || dependencies.Now == nil || dependencies.BatchSize <= 0 || dependencies.BatchSize > 1000 {
		return nil, ErrInvalidReconciler
	}
	if dependencies.Metrics == nil {
		dependencies.Metrics = NoopMetrics{}
	}
	return &Reconciler{
		db: dependencies.DB, budget: dependencies.Budget, audit: dependencies.Audit,
		lease: dependencies.Lease, now: dependencies.Now, batchSize: dependencies.BatchSize,
		metrics: dependencies.Metrics,
	}, nil
}

// Startup reconciles prior-process state in a security-preserving order:
// reservations are conservatively charged before grants are revoked and their
// cleanup-only lease fences are taken over. Audit cleanup always runs last.
func (reconciler *Reconciler) Startup(ctx context.Context) error {
	if reconciler == nil {
		return ErrInvalidReconciler
	}
	ctx = nonNilContext(ctx)
	err := errors.Join(
		reconciler.reconcileReservations(ctx),
		reconciler.releaseTerminalLeases(ctx),
		reconciler.revokeAndReleaseGrants(ctx),
		reconciler.flushPendingAudit(ctx),
	)
	reconciler.observeReconciliationAge(ctx)
	return err
}

func (reconciler *Reconciler) Reconcile(ctx context.Context) error {
	if reconciler == nil {
		return ErrInvalidReconciler
	}
	ctx = nonNilContext(ctx)
	err := errors.Join(
		reconciler.expireDueGrants(ctx),
		reconciler.reconcileExpiredLeases(ctx),
		reconciler.releaseTerminalLeases(ctx),
		reconciler.flushPendingAudit(ctx),
	)
	reconciler.observeReconciliationAge(ctx)
	return err
}

func (reconciler *Reconciler) observeReconciliationAge(ctx context.Context) {
	if reconciler == nil || reconciler.metrics == nil {
		return
	}
	now := reconciler.now().UTC()
	oldest := now
	found := false
	var request model.BackupAssetDeliveryRequest
	requestResult := reconciler.db.WithContext(ctx).
		Where("state IN ?", []string{string(RequestReserved), string(RequestStreaming)}).
		Order("updated_at ASC").Limit(1).Find(&request)
	if requestResult.Error == nil && requestResult.RowsAffected == 1 {
		oldest, found = request.UpdatedAt.UTC(), true
	}
	var grant model.BackupAssetDeliveryGrant
	grantResult := reconciler.db.WithContext(ctx).
		Where("(state IN ? AND (absolute_expires_at <= ? OR idle_expires_at <= ?)) OR audit_state IN ?",
			[]string{string(DeliveryIssued), string(DeliveryActive), string(DeliveryDraining)}, now, now,
			[]string{"pending", "retry_wait", "failed"}).
		Order("updated_at ASC").Limit(1).Find(&grant)
	if grantResult.Error == nil && grantResult.RowsAffected == 1 && (!found || grant.UpdatedAt.UTC().Before(oldest)) {
		oldest, found = grant.UpdatedAt.UTC(), true
	}
	age := time.Duration(0)
	if found && oldest.Before(now) {
		age = now.Sub(oldest)
	}
	reconciler.metrics.SetReconciliationAge(age)
}

func (reconciler *Reconciler) expireDueGrants(ctx context.Context) error {
	now := reconciler.now().UTC()
	var stageErrors []error
	cursor := ""
	for {
		var grantIDs []string
		query := reconciler.db.WithContext(ctx).Model(&model.BackupAssetDeliveryGrant{}).
			Where("state IN ? AND (absolute_expires_at <= ? OR idle_expires_at <= ?)",
				[]string{string(DeliveryIssued), string(DeliveryActive), string(DeliveryDraining)}, now, now)
		if cursor != "" {
			query = query.Where("id > ?", cursor)
		}
		if err := query.Order("id ASC").Limit(reconciler.batchSize).Pluck("id", &grantIDs).Error; err != nil {
			stageErrors = append(stageErrors, fmt.Errorf("load expired content grants: %w", err))
			break
		}
		if len(grantIDs) == 0 {
			break
		}
		for _, grantID := range grantIDs {
			result := reconciler.db.WithContext(ctx).Model(&model.BackupAssetDeliveryGrant{}).
				Where("id = ? AND state IN ? AND (absolute_expires_at <= ? OR idle_expires_at <= ?)", grantID,
					[]string{string(DeliveryIssued), string(DeliveryActive), string(DeliveryDraining)}, now, now).
				Updates(map[string]any{
					"state": DeliveryExpired, "revocation_reason": "expired", "revoked_at": now,
					"updated_at": now, "version": gorm.Expr("version + 1"),
				})
			if result.Error != nil {
				stageErrors = append(stageErrors, fmt.Errorf("expire content grant: %w", result.Error))
			}
			cursor = grantID
		}
		if len(grantIDs) < reconciler.batchSize {
			break
		}
	}
	return errors.Join(stageErrors...)
}

func (reconciler *Reconciler) reconcileExpiredLeases(ctx context.Context) error {
	leases, ok := reconciler.lease.(ReconcilerLeaseExpiry)
	if !ok {
		return ErrInvalidReconciler
	}
	_, err := leases.ReconcileExpired(ctx)
	if err != nil {
		return fmt.Errorf("reconcile expired content leases: %w", err)
	}
	return nil
}

func (reconciler *Reconciler) reconcileReservations(ctx context.Context) error {
	var stageErrors []error
	cursor := ""
	for {
		var requests []model.BackupAssetDeliveryRequest
		query := reconciler.db.WithContext(ctx).
			Select("id", "version").
			Where("state IN ?", []string{string(RequestReserved), string(RequestStreaming)})
		if cursor != "" {
			query = query.Where("id > ?", cursor)
		}
		if err := query.Order("id ASC").Limit(reconciler.batchSize).Find(&requests).Error; err != nil {
			stageErrors = append(stageErrors, fmt.Errorf("reconcile reservations: %w", err))
			break
		}
		if len(requests) == 0 {
			break
		}
		for _, request := range requests {
			_, err := reconciler.budget.Finalize(ctx, FinalizeIntent{
				RequestID: request.ID, ExpectedRequestVersion: request.Version,
				State: RequestReconciled, HTTPStatus: 500,
				FailureCode: RequestFailureReconciledCrash, EvidenceKnown: false,
			})
			if err != nil {
				stageErrors = append(stageErrors, fmt.Errorf("reconcile reservation: %w", err))
			}
			cursor = request.ID
		}
		if len(requests) < reconciler.batchSize {
			break
		}
	}
	return errors.Join(stageErrors...)
}

func (reconciler *Reconciler) revokeAndReleaseGrants(ctx context.Context) error {
	type staleGrant struct {
		ID      string
		LeaseID string
	}
	var stageErrors []error
	cursor := ""
	for {
		var grants []staleGrant
		query := reconciler.db.WithContext(ctx).Model(&model.BackupAssetDeliveryGrant{}).
			Select("id", "lease_id").
			Where("state IN ?", []string{string(DeliveryIssued), string(DeliveryActive), string(DeliveryDraining)})
		if cursor != "" {
			query = query.Where("id > ?", cursor)
		}
		if err := query.Order("id ASC").Limit(reconciler.batchSize).Scan(&grants).Error; err != nil {
			stageErrors = append(stageErrors, fmt.Errorf("load stale grants: %w", err))
			break
		}
		if len(grants) == 0 {
			break
		}
		for _, grant := range grants {
			now := reconciler.now().UTC()
			result := reconciler.db.WithContext(ctx).Model(&model.BackupAssetDeliveryGrant{}).
				Where("id = ? AND state IN ?", grant.ID, []string{string(DeliveryIssued), string(DeliveryActive), string(DeliveryDraining)}).
				Updates(map[string]any{
					"state": DeliveryRevoked, "revocation_reason": "process_restarted", "revoked_at": now,
					"updated_at": now, "version": gorm.Expr("version + 1"),
				})
			if result.Error != nil {
				stageErrors = append(stageErrors, fmt.Errorf("revoke stale grant: %w", result.Error))
			} else if result.RowsAffected == 1 {
				cleanup, err := TakeoverContentLeaseForCleanup(ctx, reconciler.lease, grant.LeaseID, grant.ID)
				if errors.Is(err, backupasset.ErrLeaseHeld) || errors.Is(err, backupasset.ErrLeaseDeadlineExceeded) {
					// A just-crashed process can still own a valid short fence. The
					// revoked grant makes it non-authorizing; periodic reconciliation
					// retries after the short lease expires.
				} else if err != nil {
					stageErrors = append(stageErrors, fmt.Errorf("take over stale content lease: %w", err))
				} else if err := cleanup.Release(ctx); err != nil {
					stageErrors = append(stageErrors, fmt.Errorf("release stale content lease: %w", err))
				}
			}
			cursor = grant.ID
		}
		if len(grants) < reconciler.batchSize {
			break
		}
	}
	return errors.Join(stageErrors...)
}

func (reconciler *Reconciler) releaseTerminalLeases(ctx context.Context) error {
	type terminalLease struct {
		GrantID string `gorm:"column:grant_id"`
		LeaseID string `gorm:"column:lease_id"`
	}
	var stageErrors []error
	cursor := ""
	for {
		var leases []terminalLease
		query := reconciler.db.WithContext(ctx).
			Table("backup_asset_delivery_grants AS content_grants").
			Select("content_grants.id AS grant_id, content_grants.lease_id AS lease_id").
			Joins("JOIN recovery_point_leases AS content_leases ON content_leases.id = content_grants.lease_id").
			Where("content_grants.state IN ?", []string{
				string(DeliveryRevoked), string(DeliveryExpired), string(DeliveryClosed),
			}).
			Where("content_leases.holder_type = ? AND content_leases.status = ?",
				backupasset.LeaseHolderContentSession, backupasset.LeaseActive)
		if cursor != "" {
			query = query.Where("content_grants.id > ?", cursor)
		}
		if err := query.Order("content_grants.id ASC").Limit(reconciler.batchSize).Scan(&leases).Error; err != nil {
			stageErrors = append(stageErrors, fmt.Errorf("load terminal content leases: %w", err))
			break
		}
		if len(leases) == 0 {
			break
		}
		for _, lease := range leases {
			cleanup, err := TakeoverContentLeaseForCleanup(ctx, reconciler.lease, lease.LeaseID, lease.GrantID)
			if errors.Is(err, backupasset.ErrLeaseHeld) || errors.Is(err, backupasset.ErrLeaseDeadlineExceeded) {
				// Safely fenced and retried by a later pass or expired by the
				// foundation lease reconciler.
			} else if err != nil {
				stageErrors = append(stageErrors, fmt.Errorf("take over terminal content lease: %w", err))
			} else if cleanup == nil {
				stageErrors = append(stageErrors, ErrInvalidContentLease)
			} else if releaseErr := cleanup.Release(ctx); releaseErr != nil {
				stageErrors = append(stageErrors, fmt.Errorf("release terminal content lease: %w", releaseErr))
			}
			cursor = lease.GrantID
		}
		if len(leases) < reconciler.batchSize {
			break
		}
	}
	return errors.Join(stageErrors...)
}

func (reconciler *Reconciler) flushPendingAudit(ctx context.Context) error {
	var stageErrors []error
	cursor := ""
	now := reconciler.now().UTC()
	for {
		var grantIDs []string
		query := reconciler.db.WithContext(ctx).Model(&model.BackupAssetDeliveryGrant{}).
			Where("state IN ? AND in_flight = 0", []string{
				string(DeliveryRevoked), string(DeliveryExpired), string(DeliveryClosed),
			}).
			Where("audit_state = ? OR (audit_state IN ? AND (audit_next_attempt_at IS NULL OR audit_next_attempt_at <= ?))",
				"pending", []string{"retry_wait", "failed"}, now)
		if cursor != "" {
			query = query.Where("id > ?", cursor)
		}
		if err := query.Order("id ASC").Limit(reconciler.batchSize).Pluck("id", &grantIDs).Error; err != nil {
			stageErrors = append(stageErrors, fmt.Errorf("load pending content audit: %w", err))
			break
		}
		if len(grantIDs) == 0 {
			break
		}
		for _, grantID := range grantIDs {
			if err := reconciler.audit.FlushGrant(ctx, grantID); err != nil {
				stageErrors = append(stageErrors, fmt.Errorf("flush content audit: %w", err))
			}
			cursor = grantID
		}
		if len(grantIDs) < reconciler.batchSize {
			break
		}
	}
	return errors.Join(stageErrors...)
}
