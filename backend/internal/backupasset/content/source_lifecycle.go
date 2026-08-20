package content

import (
	"context"
	"errors"
	"fmt"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SourceLifecycle owns Content delivery authority for one RecoveryPoint. It
// deliberately preserves grant/request history and never accepts result IDs.
type SourceLifecycle struct {
	db        *gorm.DB
	broker    *Broker
	cache     *AuthenticatedCache
	now       func() time.Time
	batchSize int
}

func NewSourceLifecycle(db *gorm.DB, broker *Broker, cache *AuthenticatedCache, now func() time.Time, batchSize int) (*SourceLifecycle, error) {
	if db == nil || broker == nil || broker.db != db || cache == nil || broker.currentCache() != cache || batchSize <= 0 || batchSize > 1000 {
		return nil, fmt.Errorf("%w: invalid content source lifecycle dependencies", backupasset.ErrInvalidState)
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &SourceLifecycle{db: db, broker: broker, cache: cache, now: now, batchSize: batchSize}, nil
}

func (owner *SourceLifecycle) RevokeAndDrainRecoveryPoint(ctx context.Context, request backupasset.SourceLifecycleRequest) error {
	if owner == nil || owner.db == nil || owner.broker == nil || owner.cache == nil {
		return fmt.Errorf("%w: content source lifecycle is unavailable", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := owner.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return backupasset.ValidateSourceLifecycleAttemptTx(ctx, tx, request)
	}); err != nil {
		return err
	}
	if err := owner.broker.waitForAssetIssues(ctx, request.RecoveryPointID); err != nil {
		return err
	}
	for {
		var grants []model.BackupAssetDeliveryGrant
		err := owner.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := backupasset.ValidateSourceLifecycleAttemptTx(ctx, tx, request); err != nil {
				return err
			}
			if err := tx.WithContext(ctx).Model(&model.BackupAssetDeliveryGrant{}).
				Select("backup_asset_delivery_grants.*").
				Joins("LEFT JOIN recovery_point_leases AS source_leases ON source_leases.id = backup_asset_delivery_grants.lease_id").
				Where("backup_asset_delivery_grants.resource_kind = ? AND backup_asset_delivery_grants.recovery_point_id = ?", DeliveryResourceBackupAsset, request.RecoveryPointID).
				Where("backup_asset_delivery_grants.state IN ? OR source_leases.status = ?", []string{string(DeliveryIssued), string(DeliveryActive), string(DeliveryDraining)}, backupasset.LeaseActive).
				Order("backup_asset_delivery_grants.id ASC").Limit(owner.batchSize).Find(&grants).Error; err != nil {
				return fmt.Errorf("load content source grants: %w", err)
			}
			return nil
		})
		if err != nil {
			return err
		}
		if len(grants) == 0 {
			break
		}
		grantIDs := make([]string, 0, len(grants))
		for _, grant := range grants {
			grantIDs = append(grantIDs, grant.ID)
		}
		if err := owner.broker.drainRecoveryPoint(ctx, request.RecoveryPointID, grantIDs); err != nil {
			return err
		}
		for _, grant := range grants {
			if err := owner.settleBackupAssetGrant(ctx, request, grant); err != nil {
				return err
			}
		}
	}
	if err := owner.provePreservedContentLeases(ctx, request); err != nil {
		return err
	}
	if err := owner.broker.completeRecoveryPointDrain(request.RecoveryPointID); err != nil {
		return err
	}
	if request.Stage != backupasset.SourceLifecycleCleanup {
		return nil
	}
	for {
		evicted, remaining, err := owner.cache.EvictRecoveryPoint(ctx, request.RecoveryPointID, owner.batchSize)
		if err != nil {
			if contextErr := ctx.Err(); contextErr != nil {
				return contextErr
			}
			if errors.Is(err, ErrCacheBusy) {
				return ErrCacheBusy
			}
			return fmt.Errorf("%w: content source cache cleanup failed", backupasset.ErrConflict)
		}
		if evicted == 0 {
			if remaining {
				return fmt.Errorf("%w: content source cache cleanup incomplete", backupasset.ErrConflict)
			}
			return nil
		}
	}
}

func (owner *SourceLifecycle) provePreservedContentLeases(ctx context.Context, request backupasset.SourceLifecycleRequest) error {
	type contentLeaseBinding struct {
		LeaseID                string  `gorm:"column:lease_id"`
		LeaseOwnerID           string  `gorm:"column:lease_owner_id"`
		LeaseAttemptID         string  `gorm:"column:lease_attempt_id"`
		LeaseFenceToken        string  `gorm:"column:lease_fence_token"`
		GrantID                *string `gorm:"column:grant_id"`
		GrantLeaseID           *string `gorm:"column:grant_lease_id"`
		GrantResourceKind      *string `gorm:"column:grant_resource_kind"`
		GrantRecoveryPointID   *string `gorm:"column:grant_recovery_point_id"`
		GrantCatalogGeneration *string `gorm:"column:grant_catalog_generation_id"`
		GrantEntryID           *string `gorm:"column:grant_entry_id"`
		GrantRecoveryJobID     *string `gorm:"column:grant_recovery_job_id"`
		GrantRecoveryResultID  *string `gorm:"column:grant_recovery_result_id"`
		GrantAttemptID         *string `gorm:"column:grant_attempt_id"`
		GrantFenceTokenHash    *string `gorm:"column:grant_fence_token_hash"`
		ResultJobID            *string `gorm:"column:result_job_id"`
		JobPlanID              *string `gorm:"column:job_plan_id"`
		PlanRecoveryPointID    *string `gorm:"column:plan_recovery_point_id"`
	}
	cursor := ""
	for {
		loaded := 0
		err := owner.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := backupasset.ValidateSourceLifecycleAttemptTx(ctx, tx, request); err != nil {
				return err
			}
			leaseIDs := make([]string, 0, owner.batchSize)
			leaseQuery := tx.WithContext(ctx).Model(&model.RecoveryPointLease{}).
				Where("recovery_point_id = ? AND holder_type = ? AND status = ?", request.RecoveryPointID, backupasset.LeaseHolderContentSession, backupasset.LeaseActive)
			if cursor != "" {
				leaseQuery = leaseQuery.Where("id > ?", cursor)
			}
			if err := leaseQuery.Order("id ASC").Limit(owner.batchSize).Pluck("id", &leaseIDs).Error; err != nil {
				return fmt.Errorf("load preserved recovery-result content leases: %w", err)
			}
			loaded = len(leaseIDs)
			if loaded == 0 {
				return nil
			}
			bindings := make([]contentLeaseBinding, 0, owner.batchSize)
			query := tx.WithContext(ctx).Table("recovery_point_leases AS source_leases").
				Select(`source_leases.id AS lease_id,
					source_leases.owner_id AS lease_owner_id,
					source_leases.attempt_id AS lease_attempt_id,
					source_leases.fence_token AS lease_fence_token,
					source_grants.id AS grant_id,
					source_grants.lease_id AS grant_lease_id,
					source_grants.resource_kind AS grant_resource_kind,
					source_grants.recovery_point_id AS grant_recovery_point_id,
					source_grants.catalog_generation_id AS grant_catalog_generation_id,
					source_grants.entry_id AS grant_entry_id,
					source_grants.recovery_job_id AS grant_recovery_job_id,
					source_grants.recovery_result_id AS grant_recovery_result_id,
					source_grants.lease_attempt_id AS grant_attempt_id,
					source_grants.lease_fence_token_hash AS grant_fence_token_hash,
					source_results.job_id AS result_job_id,
					source_jobs.plan_id AS job_plan_id,
					source_plans.recovery_point_id AS plan_recovery_point_id`).
				Joins(`LEFT JOIN backup_asset_delivery_grants AS source_grants
					ON source_grants.id = source_leases.owner_id
					AND source_grants.lease_id = source_leases.id`).
				Joins(`LEFT JOIN backup_asset_recovery_results AS source_results
					ON source_results.id = source_grants.recovery_result_id
					AND source_results.job_id = source_grants.recovery_job_id`).
				Joins(`LEFT JOIN backup_asset_recovery_jobs AS source_jobs
					ON source_jobs.id = source_grants.recovery_job_id`).
				Joins(`LEFT JOIN backup_asset_recovery_plans AS source_plans
					ON source_plans.id = source_jobs.plan_id`).
				Where("source_leases.id IN ?", leaseIDs)
			if err := query.Order("source_leases.id ASC").Limit(owner.batchSize).Scan(&bindings).Error; err != nil {
				return fmt.Errorf("prove preserved recovery-result content leases: %w", err)
			}
			if len(bindings) != loaded {
				return fmt.Errorf("%w: preserved content lease proof changed", backupasset.ErrConflict)
			}
			for _, binding := range bindings {
				if binding.GrantID == nil || binding.GrantLeaseID == nil || binding.GrantResourceKind == nil ||
					binding.GrantRecoveryJobID == nil || binding.GrantRecoveryResultID == nil || binding.GrantAttemptID == nil || binding.GrantFenceTokenHash == nil ||
					binding.ResultJobID == nil || binding.JobPlanID == nil || binding.PlanRecoveryPointID == nil ||
					*binding.GrantID != binding.LeaseOwnerID || *binding.GrantLeaseID != binding.LeaseID ||
					*binding.GrantResourceKind != string(DeliveryResourceRecoveryResult) || binding.GrantRecoveryPointID != nil ||
					binding.GrantCatalogGeneration != nil || binding.GrantEntryID != nil ||
					*binding.ResultJobID != *binding.GrantRecoveryJobID || *binding.JobPlanID == "" ||
					*binding.PlanRecoveryPointID != request.RecoveryPointID {
					return fmt.Errorf("%w: unowned content source lease remains live", backupasset.ErrConflict)
				}
				if binding.LeaseAttemptID != *binding.GrantAttemptID ||
					contentSourceFenceHash(binding.LeaseFenceToken) != *binding.GrantFenceTokenHash {
					return fmt.Errorf("%w: recovery-result content lease binding changed", backupasset.ErrConflict)
				}
				cursor = binding.LeaseID
			}
			return nil
		})
		if err != nil {
			return err
		}
		if loaded < owner.batchSize {
			return nil
		}
	}
}

func (owner *SourceLifecycle) settleBackupAssetGrant(
	ctx context.Context,
	request backupasset.SourceLifecycleRequest,
	expected model.BackupAssetDeliveryGrant,
) error {
	activeLease := false
	err := owner.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := backupasset.ValidateSourceLifecycleAttemptTx(ctx, tx, request); err != nil {
			return err
		}
		grant, lease, leaseFound, err := lockBackupAssetGrantLease(ctx, tx, request.RecoveryPointID, expected.ID)
		if err != nil {
			return err
		}
		if grant.LeaseID != expected.LeaseID {
			return fmt.Errorf("%w: content source grant lease changed", backupasset.ErrConflict)
		}
		if err := proveContentSourceGrantDrained(ctx, tx, grant); err != nil {
			return err
		}
		if !leaseFound {
			// The grant is the durable authority for the exact lease identity.
			// Let the fenced controller prove that identity is unavailable; never
			// discover or force-update a different lease through owner-only lookup.
			activeLease = true
			return nil
		}
		switch backupasset.LeaseStatus(lease.Status) {
		case backupasset.LeaseActive:
			if lease.AttemptID != grant.LeaseAttemptID || contentSourceFenceHash(lease.FenceToken) != grant.LeaseFenceTokenHash {
				return fmt.Errorf("%w: content source grant fence changed", backupasset.ErrConflict)
			}
			activeLease = true
		case backupasset.LeaseReleased, backupasset.LeaseExpired:
		default:
			return fmt.Errorf("%w: content source lease state is invalid", backupasset.ErrConflict)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if activeLease {
		if owner.broker.lease == nil {
			return fmt.Errorf("%w: content source lease controller is unavailable", backupasset.ErrConflict)
		}
		cleanup, err := TakeoverContentLeaseForCleanup(ctx, owner.broker.lease, expected.LeaseID, expected.ID)
		if err != nil || cleanup == nil {
			if contextErr := ctx.Err(); contextErr != nil {
				return contextErr
			}
			return fmt.Errorf("%w: content source lease cleanup failed", backupasset.ErrConflict)
		}
		if err := cleanup.Release(ctx); err != nil {
			if contextErr := ctx.Err(); contextErr != nil {
				return contextErr
			}
			return fmt.Errorf("%w: content source lease cleanup failed", backupasset.ErrConflict)
		}
	}
	err = owner.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := backupasset.ValidateSourceLifecycleAttemptTx(ctx, tx, request); err != nil {
			return err
		}
		grant, lease, leaseFound, err := lockBackupAssetGrantLease(ctx, tx, request.RecoveryPointID, expected.ID)
		if err != nil {
			return err
		}
		if grant.LeaseID != expected.LeaseID || !leaseFound || backupasset.LeaseStatus(lease.Status) == backupasset.LeaseActive {
			return fmt.Errorf("%w: content source lease release is unproven", backupasset.ErrConflict)
		}
		if err := proveContentSourceGrantDrained(ctx, tx, grant); err != nil {
			return err
		}
		if DeliveryState(grant.State) == DeliveryIssued || DeliveryState(grant.State) == DeliveryActive || DeliveryState(grant.State) == DeliveryDraining {
			now := owner.now().UTC()
			updated := tx.WithContext(ctx).Model(&model.BackupAssetDeliveryGrant{}).
				Where("id = ? AND lease_id = ? AND state IN ?", grant.ID, grant.LeaseID, []string{string(DeliveryIssued), string(DeliveryActive), string(DeliveryDraining)}).
				Updates(map[string]any{
					"state": DeliveryRevoked, "revocation_reason": "point_unavailable", "revoked_at": now,
					"updated_at": now, "version": gorm.Expr("version + 1"),
				})
			if updated.Error != nil {
				return fmt.Errorf("revoke content source grant: %w", updated.Error)
			}
			if updated.RowsAffected != 1 {
				return fmt.Errorf("%w: content source grant changed", backupasset.ErrConflict)
			}
		}
		return nil
	})
	if err == nil {
		owner.broker.reclaimRevokedGrant(expected.ID)
	}
	return err
}

func lockBackupAssetGrantLease(
	ctx context.Context,
	tx *gorm.DB,
	pointID string,
	grantID string,
) (model.BackupAssetDeliveryGrant, model.RecoveryPointLease, bool, error) {
	var grant model.BackupAssetDeliveryGrant
	loadedGrant := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND resource_kind = ? AND recovery_point_id = ?", grantID, DeliveryResourceBackupAsset, pointID).
		Limit(1).Find(&grant)
	if loadedGrant.Error != nil {
		return model.BackupAssetDeliveryGrant{}, model.RecoveryPointLease{}, false, fmt.Errorf("lock content source grant: %w", loadedGrant.Error)
	}
	if loadedGrant.RowsAffected != 1 {
		return model.BackupAssetDeliveryGrant{}, model.RecoveryPointLease{}, false, fmt.Errorf("%w: content source grant is unavailable", backupasset.ErrConflict)
	}
	var lease model.RecoveryPointLease
	loadedLease := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", grant.LeaseID).Limit(1).Find(&lease)
	if loadedLease.Error != nil {
		return model.BackupAssetDeliveryGrant{}, model.RecoveryPointLease{}, false, fmt.Errorf("lock content source lease: %w", loadedLease.Error)
	}
	if loadedLease.RowsAffected == 0 {
		return grant, model.RecoveryPointLease{}, false, nil
	}
	if lease.ID != grant.LeaseID || lease.RecoveryPointID != pointID ||
		lease.HolderType != string(backupasset.LeaseHolderContentSession) || lease.OwnerID != grant.ID {
		return model.BackupAssetDeliveryGrant{}, model.RecoveryPointLease{}, false, fmt.Errorf("%w: content source lease binding changed", backupasset.ErrConflict)
	}
	return grant, lease, true, nil
}

func proveContentSourceGrantDrained(ctx context.Context, tx *gorm.DB, grant model.BackupAssetDeliveryGrant) error {
	var activeRequests int64
	if err := tx.WithContext(ctx).Model(&model.BackupAssetDeliveryRequest{}).
		Where("grant_id = ? AND state IN ?", grant.ID, []string{string(RequestReserved), string(RequestStreaming)}).
		Count(&activeRequests).Error; err != nil {
		return fmt.Errorf("prove content source requests drained: %w", err)
	}
	if grant.InFlight != 0 || activeRequests != 0 {
		return fmt.Errorf("%w: content source read remains in flight", backupasset.ErrConflict)
	}
	return nil
}

func contentSourceFenceHash(token string) string {
	return hashContentLeaseFenceToken(token)
}

func (broker *Broker) drainRecoveryPoint(ctx context.Context, recoveryPointID string, grantIDs []string) error {
	if broker == nil || backupasset.ValidateOpaqueID(recoveryPointID) != nil || len(grantIDs) == 0 {
		return fmt.Errorf("%w: invalid content source broker drain", backupasset.ErrInvalidState)
	}
	ctx = nonNilContext(ctx)

	type drainSession struct {
		grantID string
		session *ContentLeaseSession
	}
	waits := make(map[string][]<-chan struct{})
	sessions := make([]drainSession, 0)
	broker.mu.Lock()
	for _, grantID := range grantIDs {
		asset, found := broker.assets[grantID]
		if !found {
			continue
		}
		if asset.Ref.RecoveryPointID != recoveryPointID {
			broker.mu.Unlock()
			return fmt.Errorf("%w: content source lease binding is unavailable", backupasset.ErrConflict)
		}
		session := broker.leases[grantID]
		if session == nil || session.fence.RecoveryPointID != recoveryPointID ||
			session.fence.HolderType != backupasset.LeaseHolderContentSession || session.fence.OwnerID != grantID {
			broker.mu.Unlock()
			return fmt.Errorf("%w: content source lease binding is unavailable", backupasset.ErrConflict)
		}
		sessions = append(sessions, drainSession{grantID: grantID, session: session})
	}
	for _, retained := range sessions {
		broker.revokedGrants[retained.grantID] = struct{}{}
		for _, read := range broker.reads[retained.grantID] {
			read.cancel()
			waits[retained.grantID] = append(waits[retained.grantID], read.done)
		}
	}
	broker.mu.Unlock()
	if err := waitForActiveReads(ctx, waits); err != nil {
		return err
	}
	for _, retained := range sessions {
		active, err := broker.proveLiveAssetSession(ctx, recoveryPointID, retained.grantID, retained.session)
		if err != nil {
			return err
		}
		if !active {
			continue
		}
		if err := retained.session.Release(ctx); err != nil {
			if contextErr := ctx.Err(); contextErr != nil {
				return contextErr
			}
			return fmt.Errorf("%w: content source lease release failed", backupasset.ErrConflict)
		}
		if active, err := broker.proveLiveAssetSession(ctx, recoveryPointID, retained.grantID, retained.session); err != nil {
			return err
		} else if active {
			return fmt.Errorf("%w: content source lease release is unproven", backupasset.ErrConflict)
		}
	}

	broker.mu.Lock()
	defer broker.mu.Unlock()
	for _, retained := range sessions {
		grantID := retained.grantID
		asset, found := broker.assets[grantID]
		if !found {
			continue
		}
		if asset.Ref.RecoveryPointID != recoveryPointID {
			return fmt.Errorf("%w: content source lease binding is unavailable", backupasset.ErrConflict)
		}
		if len(broker.reads[grantID]) != 0 {
			return fmt.Errorf("%w: content source read remains active", backupasset.ErrConflict)
		}
		delete(broker.leases, grantID)
		delete(broker.assets, grantID)
		delete(broker.derivedBindings, grantID)
		delete(broker.revokedGrants, grantID)
	}
	if state := broker.assetIssues[recoveryPointID]; state != nil && state.draining && state.active == 0 {
		delete(broker.assetIssues, recoveryPointID)
	}
	return nil
}

func (broker *Broker) completeRecoveryPointDrain(recoveryPointID string) error {
	if broker == nil || backupasset.ValidateOpaqueID(recoveryPointID) != nil {
		return fmt.Errorf("%w: invalid content source broker drain", backupasset.ErrInvalidState)
	}
	broker.mu.Lock()
	defer broker.mu.Unlock()
	state := broker.assetIssues[recoveryPointID]
	if state == nil {
		return nil
	}
	if !state.draining || state.active != 0 {
		return fmt.Errorf("%w: content source issue drain is incomplete", backupasset.ErrConflict)
	}
	delete(broker.assetIssues, recoveryPointID)
	return nil
}

func (broker *Broker) proveLiveAssetSession(
	ctx context.Context,
	recoveryPointID string,
	grantID string,
	session *ContentLeaseSession,
) (bool, error) {
	if broker == nil || broker.db == nil || broker.lease == nil {
		return false, fmt.Errorf("%w: content source lease binding is unavailable", backupasset.ErrConflict)
	}
	snapshot, err := session.snapshotForLifecycle(broker.lease)
	if err != nil || snapshot.fence.RecoveryPointID != recoveryPointID || snapshot.fence.OwnerID != grantID {
		return false, fmt.Errorf("%w: content source lease binding is unavailable", backupasset.ErrConflict)
	}
	active := false
	err = broker.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		grant, lease, leaseFound, lockErr := lockBackupAssetGrantLease(ctx, tx, recoveryPointID, grantID)
		if lockErr != nil {
			return lockErr
		}
		if !leaseFound || grant.LeaseID != snapshot.binding.LeaseID ||
			grant.LeaseAttemptID != snapshot.binding.AttemptID ||
			grant.LeaseFenceTokenHash != snapshot.binding.FenceTokenHash ||
			lease.AttemptID != snapshot.fence.AttemptID ||
			lease.FenceToken != snapshot.fence.FenceToken ||
			contentSourceFenceHash(lease.FenceToken) != grant.LeaseFenceTokenHash ||
			!lease.LeaseExpiresAt.UTC().Equal(snapshot.binding.LeaseExpiresAt) ||
			!lease.AbsoluteDeadline.UTC().Equal(snapshot.binding.AbsoluteDeadline) ||
			!lease.LastHeartbeatAt.UTC().Equal(snapshot.lastHeartbeatAt) {
			return fmt.Errorf("%w: content source lease binding changed", backupasset.ErrConflict)
		}
		if err := proveContentSourceGrantDrained(ctx, tx, grant); err != nil {
			return err
		}
		switch backupasset.LeaseStatus(lease.Status) {
		case backupasset.LeaseActive:
			if snapshot.released {
				return fmt.Errorf("%w: content source lease release is unproven", backupasset.ErrConflict)
			}
			active = true
		case backupasset.LeaseReleased, backupasset.LeaseExpired:
			active = false
		default:
			return fmt.Errorf("%w: content source lease state is invalid", backupasset.ErrConflict)
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	if active {
		if err := broker.lease.ValidateFence(ctx, snapshot.fence); err != nil {
			if contextErr := ctx.Err(); contextErr != nil {
				return false, contextErr
			}
			return false, fmt.Errorf("%w: content source lease fence is unavailable", backupasset.ErrConflict)
		}
	}
	return active, nil
}

func (broker *Broker) reclaimRevokedGrant(grantID string) {
	if broker == nil || backupasset.ValidateOpaqueID(grantID) != nil {
		return
	}
	broker.mu.Lock()
	delete(broker.revokedGrants, grantID)
	broker.mu.Unlock()
}
