package processing

import (
	"context"
	"fmt"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type SearchSourceRevocationProof interface {
	ProveRecoveryPointRevoked(context.Context, backupasset.SourceLifecycleRequest) error
}

// SourceLifecycle owns Processing work authority and Derived payloads for one
// source. Search cleanup proof is mandatory before any Derived destruction.
type SourceLifecycle struct {
	db        *gorm.DB
	derived   *DerivedLifecycle
	search    SearchSourceRevocationProof
	now       func() time.Time
	batchSize int
}

func NewSourceLifecycle(
	db *gorm.DB,
	derived *DerivedLifecycle,
	search SearchSourceRevocationProof,
	now func() time.Time,
	batchSize int,
) (*SourceLifecycle, error) {
	if db == nil || derived == nil || derived.db != db || derived.store == nil || search == nil || batchSize <= 0 || batchSize > 1000 {
		return nil, fmt.Errorf("%w: invalid Processing source lifecycle dependencies", backupasset.ErrInvalidState)
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &SourceLifecycle{db: db, derived: derived, search: search, now: now, batchSize: batchSize}, nil
}

func (owner *SourceLifecycle) RevokeRecoveryPoint(ctx context.Context, request backupasset.SourceLifecycleRequest) error {
	if owner == nil || owner.db == nil {
		return fmt.Errorf("%w: Processing source lifecycle is unavailable", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := owner.settleWork(ctx, request); err != nil {
		return err
	}
	if request.Stage == backupasset.SourceLifecyclePrepare {
		return nil
	}
	if err := owner.search.ProveRecoveryPointRevoked(ctx, request); err != nil {
		return fmt.Errorf("prove Search cleanup before Processing destruction: %w", err)
	}
	return owner.destroyDerived(ctx, request)
}

func (owner *SourceLifecycle) settleWork(ctx context.Context, request backupasset.SourceLifecycleRequest) error {
	if err := owner.revokePublicationAuthority(ctx, request); err != nil {
		return err
	}
	for {
		settled := false
		err := owner.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := backupasset.ValidateSourceLifecycleAttemptTx(ctx, tx, request); err != nil {
				return err
			}
			var jobs []model.BackupAssetProcessingJob
			if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
				Where(`recovery_point_id = ? AND (
					is_current = ? OR current_attempt_id IS NOT NULL OR
					EXISTS (SELECT 1 FROM backup_asset_processing_interests AS interest
						WHERE interest.job_id = backup_asset_processing_jobs.id AND interest.active = ?) OR
					EXISTS (SELECT 1 FROM backup_asset_processing_attempts AS attempt
						WHERE attempt.job_id = backup_asset_processing_jobs.id AND attempt.state = ? AND attempt.is_current = ?) OR
					EXISTS (SELECT 1 FROM backup_asset_processing_grants AS grant_authority
						WHERE grant_authority.job_id = backup_asset_processing_jobs.id AND grant_authority.state IN ?) OR
					EXISTS (SELECT 1 FROM backup_asset_processing_grants AS grant_flight
						WHERE grant_flight.job_id = backup_asset_processing_jobs.id AND grant_flight.in_flight <> 0) OR
					EXISTS (SELECT 1 FROM recovery_point_leases AS lease
						WHERE lease.recovery_point_id = backup_asset_processing_jobs.recovery_point_id
						AND lease.holder_type = ? AND lease.owner_id = backup_asset_processing_jobs.id AND lease.status = ?)
				)`, request.RecoveryPointID, true, true, "active", true, []string{"issued", "active"},
					backupasset.LeaseHolderProcessingJob, backupasset.LeaseActive).
				Order("id ASC").Limit(owner.batchSize).Find(&jobs).Error; err != nil {
				return fmt.Errorf("load Processing source jobs: %w", err)
			}
			now := owner.now().UTC()
			if len(jobs) == 0 {
				var currentJobs, activeInterests, currentAttempts, usableGrants, inFlightGrants, liveLeases int64
				if err := tx.Model(&model.BackupAssetProcessingJob{}).
					Where("recovery_point_id = ? AND (is_current = ? OR current_attempt_id IS NOT NULL)", request.RecoveryPointID, true).
					Count(&currentJobs).Error; err != nil {
					return fmt.Errorf("prove Processing source jobs settled: %w", err)
				}
				if err := tx.Table("backup_asset_processing_interests AS interest").
					Joins("JOIN backup_asset_processing_jobs AS job ON job.id = interest.job_id").
					Where("job.recovery_point_id = ? AND interest.active = ?", request.RecoveryPointID, true).
					Count(&activeInterests).Error; err != nil {
					return fmt.Errorf("prove Processing source interests removed: %w", err)
				}
				if err := tx.Table("backup_asset_processing_attempts AS attempt").
					Joins("JOIN backup_asset_processing_jobs AS job ON job.id = attempt.job_id").
					Where("job.recovery_point_id = ? AND attempt.state = ? AND attempt.is_current = ?", request.RecoveryPointID, "active", true).
					Count(&currentAttempts).Error; err != nil {
					return fmt.Errorf("prove Processing source attempts canceled: %w", err)
				}
				if err := tx.Table("backup_asset_processing_grants AS grant_authority").
					Joins("JOIN backup_asset_processing_jobs AS job ON job.id = grant_authority.job_id").
					Where("job.recovery_point_id = ? AND grant_authority.state IN ?", request.RecoveryPointID, []string{"issued", "active"}).
					Count(&usableGrants).Error; err != nil {
					return fmt.Errorf("prove Processing source grants revoked: %w", err)
				}
				if err := tx.Table("backup_asset_processing_grants AS grant_flight").
					Joins("JOIN backup_asset_processing_jobs AS job ON job.id = grant_flight.job_id").
					Where("job.recovery_point_id = ? AND grant_flight.in_flight <> 0", request.RecoveryPointID).
					Count(&inFlightGrants).Error; err != nil {
					return fmt.Errorf("prove Processing source grants drained: %w", err)
				}
				if err := tx.Model(&model.RecoveryPointLease{}).
					Where("recovery_point_id = ? AND holder_type = ? AND status = ?", request.RecoveryPointID, backupasset.LeaseHolderProcessingJob, backupasset.LeaseActive).
					Count(&liveLeases).Error; err != nil {
					return fmt.Errorf("prove Processing source leases released: %w", err)
				}
				if currentJobs != 0 || activeInterests != 0 || currentAttempts != 0 || usableGrants != 0 || inFlightGrants != 0 || liveLeases != 0 {
					return fmt.Errorf("%w: Processing source authority remains live", backupasset.ErrConflict)
				}
				settled = true
				return nil
			}
			for _, job := range jobs {
				if err := ctx.Err(); err != nil {
					return err
				}
				var inFlight int64
				if err := tx.Model(&model.BackupAssetProcessingGrant{}).
					Where("job_id = ?", job.ID).
					Select("COALESCE(SUM(in_flight), 0)").Scan(&inFlight).Error; err != nil {
					return fmt.Errorf("prove Processing grants drained: %w", err)
				}
				if inFlight != 0 {
					return fmt.Errorf("%w: Processing source grant remains in flight", backupasset.ErrConflict)
				}
				if err := tx.Model(&model.BackupAssetProcessingInterest{}).Where("job_id = ? AND active = ?", job.ID, true).
					Updates(map[string]any{"active": false, "removed_reason": InterestRemovedExpired, "removed_at": now, "updated_at": now}).Error; err != nil {
					return fmt.Errorf("remove Processing source interests: %w", err)
				}
				if err := tx.Model(&model.BackupAssetProcessingGrant{}).Where("job_id = ? AND state IN ?", job.ID, []string{"issued", "active"}).
					Updates(map[string]any{"activation_secret_hash": "", "state": "revoked", "revoked_at": now, "revocation_reason": "expired", "updated_at": now, "version": gorm.Expr("version + 1")}).Error; err != nil {
					return fmt.Errorf("revoke Processing source grants: %w", err)
				}
				if err := tx.Model(&model.BackupAssetProcessingAttempt{}).Where("job_id = ? AND state = ? AND is_current = ?", job.ID, "active", true).
					Updates(map[string]any{"state": "canceled", "outcome_code": "", "is_current": false, "finished_at": now, "updated_at": now}).Error; err != nil {
					return fmt.Errorf("cancel Processing source attempt: %w", err)
				}
				if err := tx.Model(&model.RecoveryPointLease{}).
					Where("recovery_point_id = ? AND holder_type = ? AND owner_id = ? AND status = ?", request.RecoveryPointID, backupasset.LeaseHolderProcessingJob, job.ID, backupasset.LeaseActive).
					Updates(map[string]any{"status": backupasset.LeaseReleased, "released_at": now, "updated_at": now}).Error; err != nil {
					return fmt.Errorf("release Processing source lease: %w", err)
				}
				updates := map[string]any{"current_attempt_id": nil, "is_current": false, "updated_at": now, "version": gorm.Expr("version + 1")}
				if !isTerminalState(ProcessingState(job.State)) {
					updates["state"] = ProcessingCanceled
					updates["cancel_reason"] = CancelReasonInterestWithdrawn
					updates["finished_at"] = now
				}
				if err := tx.Model(&model.BackupAssetProcessingJob{}).Where("id = ?", job.ID).Updates(updates).Error; err != nil {
					return fmt.Errorf("cancel Processing source job: %w", err)
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		if settled {
			return nil
		}
	}
}

func (owner *SourceLifecycle) revokePublicationAuthority(ctx context.Context, request backupasset.SourceLifecycleRequest) error {
	for {
		found := false
		err := owner.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := backupasset.ValidateSourceLifecycleAttemptTx(ctx, tx, request); err != nil {
				return err
			}
			var jobs []model.BackupAssetProcessingJob
			if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
				Where(`recovery_point_id = ? AND EXISTS (
					SELECT 1 FROM backup_asset_processing_grants AS grant_authority
					WHERE grant_authority.job_id = backup_asset_processing_jobs.id
					AND grant_authority.state IN ?
				)`, request.RecoveryPointID, []string{string(GrantIssued), string(GrantActive)}).
				Order("id ASC").Limit(owner.batchSize).Find(&jobs).Error; err != nil {
				return fmt.Errorf("load Processing publication authority: %w", err)
			}
			if len(jobs) == 0 {
				return nil
			}
			found = true
			jobIDs := make([]string, 0, len(jobs))
			for _, job := range jobs {
				jobIDs = append(jobIDs, job.ID)
			}
			now := owner.now().UTC()
			if err := tx.WithContext(ctx).Model(&model.BackupAssetProcessingGrant{}).
				Where("job_id IN ? AND state IN ?", jobIDs, []string{string(GrantIssued), string(GrantActive)}).
				Updates(map[string]any{
					"activation_secret_hash": "", "state": string(GrantRevoked), "revoked_at": now,
					"revocation_reason": "expired", "updated_at": now, "version": gorm.Expr("version + 1"),
				}).Error; err != nil {
				return fmt.Errorf("revoke Processing publication authority: %w", err)
			}
			return nil
		})
		if err != nil {
			return err
		}
		if !found {
			return nil
		}
	}
}

func (owner *SourceLifecycle) destroyDerived(ctx context.Context, request backupasset.SourceLifecycleRequest) error {
	type doomedBlob struct {
		ID      string
		Locator string
	}
	for {
		settled := false
		var doomed []doomedBlob
		err := owner.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := backupasset.ValidateSourceLifecycleAttemptTx(ctx, tx, request); err != nil {
				return err
			}
			if err := tx.Table("backup_asset_derived_blobs AS blob").
				Select("blob.id AS id, blob.opaque_locator AS locator").
				Joins("JOIN backup_asset_derived_blob_references AS reference ON reference.blob_id = blob.id").
				Where("reference.recovery_point_id = ? AND blob.state = ?", request.RecoveryPointID, "purge_failed").
				Group("blob.id, blob.opaque_locator").Order("blob.id ASC").Limit(owner.batchSize).
				Scan(&doomed).Error; err != nil {
				return fmt.Errorf("load retryable Processing Derived purges: %w", err)
			}
			if len(doomed) != 0 {
				for _, blob := range doomed {
					if !safeOpaqueLocator(blob.Locator) {
						return ErrDerivedBlobUnavailable
					}
				}
				return nil
			}
			var sets []model.BackupAssetDerivedArtifactSet
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("recovery_point_id = ? AND state NOT IN ?", request.RecoveryPointID, []string{"unavailable", "revoked", "superseded"}).
				Order("id ASC").Limit(owner.batchSize).Find(&sets).Error; err != nil {
				return fmt.Errorf("load Processing Derived sets: %w", err)
			}
			if len(sets) == 0 {
				settled = true
				return nil
			}
			now := owner.now().UTC()
			for _, set := range sets {
				var artifacts []model.BackupAssetDerivedArtifact
				if err := tx.Where("artifact_set_id = ?", set.ID).Find(&artifacts).Error; err != nil {
					return fmt.Errorf("load Processing Derived artifacts: %w", err)
				}
				if err := tx.Model(&model.BackupAssetDerivedBlobReference{}).
					Where("recovery_point_id = ? AND artifact_id IN ? AND state = ?", request.RecoveryPointID, artifactIDs(artifacts), "active").
					Updates(map[string]any{"state": "unavailable", "revoked_at": now, "updated_at": now}).Error; err != nil {
					return fmt.Errorf("revoke Processing Derived references: %w", err)
				}
				processedBlobIDs := make(map[string]struct{}, len(artifacts))
				for _, artifact := range artifacts {
					if _, processed := processedBlobIDs[artifact.BlobID]; processed {
						continue
					}
					processedBlobIDs[artifact.BlobID] = struct{}{}
					var blob model.BackupAssetDerivedBlob
					result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", artifact.BlobID).Limit(1).Find(&blob)
					if result.Error != nil {
						return fmt.Errorf("load Processing Derived blob: %w", result.Error)
					}
					if result.RowsAffected != 1 {
						return ErrDerivedBlobUnavailable
					}
					var activeReferences int64
					if err := tx.Model(&model.BackupAssetDerivedBlobReference{}).Where("blob_id = ? AND state = ?", artifact.BlobID, "active").Count(&activeReferences).Error; err != nil {
						return fmt.Errorf("count Processing Derived references: %w", err)
					}
					if activeReferences != 0 {
						updated := tx.Model(&model.BackupAssetDerivedBlob{}).Where("id = ?", artifact.BlobID).Update("ref_count", activeReferences)
						if updated.Error != nil {
							return fmt.Errorf("reconcile Processing Derived reference count: %w", updated.Error)
						}
						if updated.RowsAffected != 1 {
							return ErrDerivedBlobUnavailable
						}
						continue
					}
					if blob.State != "unavailable" {
						if !safeOpaqueLocator(blob.OpaqueLocator) {
							return ErrDerivedBlobUnavailable
						}
						if err := tx.Model(&model.BackupAssetDerivedBlob{}).Where("id = ?", blob.ID).
							Updates(map[string]any{"state": "purge_failed", "ref_count": 0, "wrapped_dek": []byte{}, "unavailable_at": now, "updated_at": now}).Error; err != nil {
							return fmt.Errorf("revoke Processing Derived blob key: %w", err)
						}
						doomed = append(doomed, doomedBlob{ID: blob.ID, Locator: blob.OpaqueLocator})
					}
				}
				if err := tx.Model(&model.BackupAssetDerivedArtifactSet{}).Where("id = ?", set.ID).
					Updates(map[string]any{"state": "unavailable", "revocation_reason": DerivedRevokeExpired, "projection_published": false, "revoked_at": now, "updated_at": now}).Error; err != nil {
					return fmt.Errorf("revoke Processing Derived set: %w", err)
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		for _, blob := range doomed {
			if err := owner.derived.removeCiphertext(blob.Locator); err != nil {
				return err
			}
			now := owner.now().UTC()
			updated := owner.db.WithContext(ctx).Model(&model.BackupAssetDerivedBlob{}).
				Where("id = ? AND state = ?", blob.ID, "purge_failed").
				Updates(map[string]any{"state": "unavailable", "unavailable_at": now, "updated_at": now})
			if updated.Error != nil || updated.RowsAffected != 1 {
				return ErrDerivedBlobUnavailable
			}
		}
		if settled {
			return nil
		}
	}
}

func artifactIDs(artifacts []model.BackupAssetDerivedArtifact) []string {
	ids := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		ids = append(ids, artifact.ID)
	}
	return ids
}
