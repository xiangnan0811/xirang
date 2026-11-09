package processing

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var errDerivedAuthorityReplan = errors.New("derived staged holder census changed")

type derivedStagedHolder struct {
	ID        string
	StagingID string
	JobID     string
	AttemptID string
	GrantID   string
}

// Each retry starts a new transaction and rebuilds the entire lock census.
func derivedCleanupTransaction(ctx context.Context, db *gorm.DB, operation func(*gorm.DB) error) error {
	var lastErr error
	for attempt := range 20 {
		var opts []*sql.TxOptions
		if db.Dialector.Name() == "postgres" {
			opts = append(opts, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
		}
		err := db.WithContext(ctx).Transaction(operation, opts...)
		if err == nil {
			return nil
		}
		lastErr = err
		if !errors.Is(err, errDerivedAuthorityReplan) && !retryableCoordinatorConflict(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("derived cleanup conflict: %w", ctx.Err())
		case <-time.After(time.Duration(attempt+1) * time.Millisecond):
		}
	}
	return fmt.Errorf("derived cleanup conflict after retries: %w", lastErr)
}

func derivedStagedCensusTx(tx *gorm.DB, blobIDs []string) ([]derivedStagedHolder, error) {
	if len(blobIDs) == 0 {
		return nil, nil
	}
	var holders []derivedStagedHolder
	err := tx.Model(&model.BackupAssetProcessingUpload{}).
		Select("id", "staging_id", "job_id", "attempt_id", "grant_id").
		Where("staging_id IN ? AND state = ?", blobIDs, "staged").Order("id ASC").Find(&holders).Error
	return holders, err
}

// Missing jobs are explicitly tracked separately: uploads have a RESTRICT job
// foreign key and generated job IDs are never resurrected. An absent row is not
// claimed as locked; if it appears, the whole transaction must be replanned.
func lockDerivedHolderJobsTx(tx *gorm.DB, blobIDs []string) (map[string]bool, error) {
	holders, err := derivedStagedCensusTx(tx, blobIDs)
	if err != nil {
		return nil, err
	}
	ids := make(map[string]bool, len(holders))
	for _, holder := range holders {
		ids[holder.JobID] = false
	}
	ordered := make([]string, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)
	for _, id := range ordered {
		var job model.BackupAssetProcessingJob
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").Where("id = ?", id).Limit(1).Find(&job)
		if result.Error != nil {
			return nil, result.Error
		}
		ids[id] = result.RowsAffected == 1
	}
	if err := checkDerivedHolderCensusTx(tx, blobIDs, ids); err != nil {
		return nil, err
	}
	return ids, nil
}

func checkDerivedHolderCensusTx(tx *gorm.DB, blobIDs []string, jobs map[string]bool) error {
	holders, err := derivedStagedCensusTx(tx, blobIDs)
	if err != nil {
		return err
	}
	for _, holder := range holders {
		locked, known := jobs[holder.JobID]
		if !known {
			return errDerivedAuthorityReplan
		}
		if !locked {
			var count int64
			if err := tx.Model(&model.BackupAssetProcessingJob{}).Where("id = ?", holder.JobID).Count(&count).Error; err != nil {
				return err
			}
			if count != 0 {
				return errDerivedAuthorityReplan
			}
		}
	}
	return nil
}

// Retire only identities covered by the job census, before acquiring blob locks.
func orphanDerivedHoldersTx(tx *gorm.DB, blobIDs []string, jobs map[string]bool, now time.Time) error {
	if err := checkDerivedHolderCensusTx(tx, blobIDs, jobs); err != nil {
		return err
	}
	holders, err := derivedStagedCensusTx(tx, blobIDs)
	if err != nil {
		return err
	}
	for _, h := range holders {
		if _, ok := jobs[h.JobID]; !ok {
			return errDerivedAuthorityReplan
		}
		result := tx.Model(&model.BackupAssetProcessingUpload{}).Where(`id = ? AND staging_id = ? AND state = ? AND job_id = ? AND attempt_id = ? AND grant_id = ? AND NOT EXISTS (
			SELECT 1 FROM backup_asset_processing_jobs j
			JOIN backup_asset_processing_attempts a ON a.id = backup_asset_processing_uploads.attempt_id AND a.job_id = j.id
			JOIN backup_asset_processing_grants g ON g.id = backup_asset_processing_uploads.grant_id AND g.job_id = j.id AND g.attempt_id = a.id AND g.worker_id = a.worker_id
			JOIN recovery_point_leases l ON l.id = a.recovery_point_lease_id AND l.recovery_point_id = j.recovery_point_id AND l.owner_id = j.id AND l.attempt_id = a.recovery_point_attempt_id
			WHERE j.id = backup_asset_processing_uploads.job_id AND j.is_current = ? AND j.current_attempt_id = a.id AND j.absolute_deadline > ?
			AND a.state = ? AND a.is_current = ? AND a.worker_lease_expires_at > ? AND a.absolute_deadline > ?
			AND g.kind = ? AND g.state = ? AND g.expires_at > ? AND g.fence_hash = a.recovery_point_fence_hash
			AND l.holder_type = ? AND l.status = ? AND l.lease_expires_at > ? AND l.absolute_deadline > ?
		)`, h.ID, h.StagingID, "staged", h.JobID, h.AttemptID, h.GrantID, true, now, "active", true, now, now, GrantSink, GrantActive, now, backupasset.LeaseHolderProcessingJob, backupasset.LeaseActive, now, now).
			Updates(map[string]any{"state": "orphaned", "failure_code": "lease_lost", "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
	}
	var mismatches []struct {
		UploadID       string
		StagingID      string
		JobID          string
		AttemptID      string
		GrantID        string
		LeaseID        string
		LeaseAttemptID string
		FenceHash      string
		FenceToken     string
	}
	if len(blobIDs) == 0 {
		return nil
	}
	if err := tx.Table("backup_asset_processing_uploads AS u").
		Select("u.id AS upload_id, u.staging_id, u.job_id, u.attempt_id, u.grant_id, a.recovery_point_lease_id AS lease_id, a.recovery_point_attempt_id AS lease_attempt_id, a.recovery_point_fence_hash AS fence_hash, l.fence_token").
		Joins("JOIN backup_asset_processing_attempts AS a ON a.id = u.attempt_id AND a.job_id = u.job_id").
		Joins("JOIN recovery_point_leases AS l ON l.id = a.recovery_point_lease_id").
		Where("u.staging_id IN ? AND u.state = ?", blobIDs, "staged").Scan(&mismatches).Error; err != nil {
		return err
	}
	for _, h := range mismatches {
		if _, ok := jobs[h.JobID]; !ok {
			return errDerivedAuthorityReplan
		}
		if hashFence(h.FenceToken) == h.FenceHash {
			continue
		}
		result := tx.Model(&model.BackupAssetProcessingUpload{}).Where(`id = ? AND staging_id = ? AND state = ? AND job_id = ? AND attempt_id = ? AND grant_id = ? AND EXISTS (
			SELECT 1 FROM backup_asset_processing_attempts a JOIN recovery_point_leases l ON l.id = a.recovery_point_lease_id
			WHERE a.id = ? AND a.job_id = ? AND a.recovery_point_lease_id = ? AND a.recovery_point_attempt_id = ? AND a.recovery_point_fence_hash = ? AND l.fence_token = ?
		)`, h.UploadID, h.StagingID, "staged", h.JobID, h.AttemptID, h.GrantID, h.AttemptID, h.JobID, h.LeaseID, h.LeaseAttemptID, h.FenceHash, h.FenceToken).
			Updates(map[string]any{"state": "orphaned", "failure_code": "lease_lost", "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errDerivedAuthorityReplan
		}
	}
	return nil
}
