package processing

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ReconcilerConfig struct {
	BatchSize int
	RetryBase time.Duration
}

type ReconcileResult struct {
	ExpiredAttempts int
	ExpiredJobs     int
	RetryingJobs    int
}

type Reconciler struct {
	coordinator *Coordinator
	grants      *GrantService
	now         func() time.Time
	config      ReconcilerConfig
}

const derivedReconcileOrphanGrace = time.Minute

var errStagedDerivedBlobUnreadable = errors.New("staged Derived blob unreadable")

type DerivedReconcileResult struct {
	ExaminedBlobs         int
	RepairedRefCounts     int
	RecoveredStaging      int
	RevokedUnreadableSets int
	PurgedBlobs           int
	RemovedFileOrphans    int
	RewrappedBlobs        int
}

type DerivedReconciler struct {
	store     *DerivedStore
	lifecycle *DerivedLifecycle
	batchSize int

	mu         sync.Mutex
	blobCursor string
	fileCursor string
}

func NewDerivedReconciler(store *DerivedStore, lifecycle *DerivedLifecycle, batchSize int) (*DerivedReconciler, error) {
	if store == nil || lifecycle == nil || lifecycle.store != store || lifecycle.db != store.db || batchSize <= 0 || batchSize > 10000 {
		return nil, ErrInvalidContract
	}
	return &DerivedReconciler{store: store, lifecycle: lifecycle, batchSize: batchSize}, nil
}

func (reconciler *DerivedReconciler) Reconcile(ctx context.Context) (DerivedReconcileResult, error) {
	if reconciler == nil || reconciler.store == nil || reconciler.lifecycle == nil {
		return DerivedReconcileResult{}, ErrInvalidContract
	}
	if ctx == nil {
		ctx = context.Background()
	}
	reconciler.mu.Lock()
	defer reconciler.mu.Unlock()
	result := DerivedReconcileResult{}
	if err := reconciler.reconcileRewrap(ctx, &result); err != nil {
		return result, err
	}
	if err := reconciler.reconcileBlobRows(ctx, &result); err != nil {
		return result, err
	}
	if err := reconciler.reconcileFiles(ctx, &result); err != nil {
		return result, err
	}
	return result, nil
}

func (reconciler *DerivedReconciler) reconcileRewrap(ctx context.Context, result *DerivedReconcileResult) error {
	active, err := reconciler.store.keyring.Active(ctx, backupasset.KeyDomainDerivedStore)
	if err != nil {
		if errors.Is(err, backupasset.ErrKeyLost) {
			return nil
		}
		return errors.Join(ErrDerivedStoreUnavailable, err)
	}
	defer zeroBytesLocal(active.Key)
	var versions []int
	if err := reconciler.store.db.WithContext(ctx).Model(&model.BackupAssetDerivedBlob{}).
		Distinct("derived_kek_version").
		Where("derived_kek_version <> ? AND state IN ?", active.Version, []string{"staged", "active"}).
		Order("derived_kek_version ASC").Limit(reconciler.batchSize).Pluck("derived_kek_version", &versions).Error; err != nil {
		return fmt.Errorf("load interrupted Derived rewrap versions: %w", err)
	}
	remaining := reconciler.batchSize
	for _, version := range versions {
		if remaining == 0 {
			break
		}
		count, err := reconciler.store.RewrapBatch(ctx, version, active.Version, remaining)
		if err != nil {
			return err
		}
		result.RewrappedBlobs += count
		remaining -= count
	}
	return nil
}

func (reconciler *DerivedReconciler) reconcileBlobRows(ctx context.Context, result *DerivedReconcileResult) error {
	var rows []model.BackupAssetDerivedBlob
	query := reconciler.store.db.WithContext(ctx).Order("id ASC").Limit(reconciler.batchSize)
	if reconciler.blobCursor != "" {
		query = query.Where("id > ?", reconciler.blobCursor)
	}
	if err := query.Find(&rows).Error; err != nil {
		return fmt.Errorf("load Derived reconciliation batch: %w", err)
	}
	if len(rows) == 0 {
		reconciler.blobCursor = ""
		return nil
	}
	reconciler.blobCursor = rows[len(rows)-1].ID
	if len(rows) < reconciler.batchSize {
		reconciler.blobCursor = ""
	}
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			reconciler.blobCursor = ""
			return err
		}
		result.ExaminedBlobs++
		if err := reconciler.reconcileBlob(ctx, row, result); err != nil {
			reconciler.blobCursor = ""
			return err
		}
	}
	return nil
}

func (reconciler *DerivedReconciler) reconcileBlob(ctx context.Context, row model.BackupAssetDerivedBlob, result *DerivedReconcileResult) error {
	if !safeOpaqueLocator(row.OpaqueLocator) {
		return ErrDerivedBlobUnavailable
	}
	if row.State == "staged" {
		if reconciler.store.utcNow().Sub(row.UpdatedAt.UTC()) < derivedReconcileOrphanGrace {
			return nil
		}
		recovered, err := reconciler.recoverStagedBlob(ctx, row)
		if errors.Is(err, errStagedDerivedBlobUnreadable) {
			var references int64
			if err := reconciler.store.db.WithContext(ctx).Model(&model.BackupAssetDerivedBlobReference{}).
				Where("blob_id = ? AND state = ?", row.ID, "active").Count(&references).Error; err != nil {
				return fmt.Errorf("count Derived reconciliation references: %w", err)
			}
			return reconciler.revokeUnreadableBlob(ctx, row.ID, row.OpaqueLocator, references, result)
		}
		if err != nil {
			return err
		}
		if recovered {
			result.RecoveredStaging++
		}
		return nil
	}
	var references int64
	if err := reconciler.store.db.WithContext(ctx).Model(&model.BackupAssetDerivedBlobReference{}).
		Where("blob_id = ? AND state = ?", row.ID, "active").Count(&references).Error; err != nil {
		return fmt.Errorf("count Derived reconciliation references: %w", err)
	}
	finalPath := filepath.Join(reconciler.store.config.Root, row.OpaqueLocator)
	readable, err := derivedReconcileRegularFile(finalPath, row.PhysicalSize)
	if err != nil {
		return err
	}
	switch row.State {
	case "active":
		if !readable {
			return reconciler.revokeUnreadableBlob(ctx, row.ID, row.OpaqueLocator, references, result)
		}
		if row.RefCount != references {
			repaired, err := reconciler.repairRefCount(ctx, row.ID, row.OpaqueLocator, row.RefCount)
			if err != nil {
				return err
			}
			if repaired {
				result.RepairedRefCounts++
			}
		}
		var orphaned int64
		if reconciler.store.db.Migrator().HasTable(&model.BackupAssetProcessingUpload{}) {
			if err := reconciler.store.db.WithContext(ctx).Model(&model.BackupAssetProcessingUpload{}).Where("staging_id = ? AND state = ?", row.ID, "orphaned").Count(&orphaned).Error; err != nil {
				return err
			}
		}
		if references == 0 && (orphaned > 0 || reconciler.store.utcNow().Sub(row.UpdatedAt.UTC()) >= derivedReconcileOrphanGrace) {
			before := row.State
			if err := reconciler.store.discardBlobIfUnreferencedObserved(ctx, row.ID, row.OpaqueLocator); err != nil {
				return err
			}
			var after model.BackupAssetDerivedBlob
			loaded := reconciler.store.db.WithContext(ctx).Select("state").Where("id = ?", row.ID).Limit(1).Find(&after)
			if loaded.Error != nil {
				return loaded.Error
			}
			if loaded.RowsAffected == 1 && after.State != before {
				result.PurgedBlobs++
			}
		}
	case "unavailable", "purging", "purge_failed":
		if _, statErr := os.Lstat(finalPath); statErr == nil {
			if err := reconciler.store.removeFile(finalPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				return errors.Join(ErrDerivedBlobUnavailable, err)
			}
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return errors.Join(ErrDerivedBlobUnavailable, statErr)
		}
		if row.State != "unavailable" {
			updated := reconciler.store.db.WithContext(ctx).Model(&model.BackupAssetDerivedBlob{}).
				Where("id = ? AND state = ? AND opaque_locator = ?", row.ID, row.State, row.OpaqueLocator).
				Updates(map[string]any{"state": "unavailable", "updated_at": reconciler.store.utcNow()})
			if updated.Error != nil {
				return fmt.Errorf("finish Derived purge reconciliation: %w", updated.Error)
			}
			if updated.RowsAffected == 1 {
				result.PurgedBlobs++
			}
		}
	default:
		return ErrDerivedBlobUnavailable
	}
	return nil
}

// recoverStagedBlob fences filesystem recovery with the observed incarnation.
// Unreadable blobs are handled after the transaction releases the blob lock,
// because revocation must acquire the source point and set locks first.
func (reconciler *DerivedReconciler) recoverStagedBlob(ctx context.Context, observed model.BackupAssetDerivedBlob) (bool, error) {
	if observed.State != "staged" {
		return false, nil
	}
	recovered := false
	err := reconciler.store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current model.BackupAssetDerivedBlob
		loaded := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", observed.ID).Limit(1).Find(&current)
		if loaded.Error != nil || loaded.RowsAffected != 1 {
			return loaded.Error
		}
		if current.State != "staged" || current.OpaqueLocator != observed.OpaqueLocator {
			return nil
		}
		if !safeOpaqueLocator(current.OpaqueLocator) {
			return ErrDerivedBlobUnavailable
		}
		finalPath := filepath.Join(reconciler.store.config.Root, current.OpaqueLocator)
		readable, err := derivedReconcileRegularFile(finalPath, current.PhysicalSize)
		if err != nil {
			return err
		}
		if !readable {
			stagingPath := filepath.Join(reconciler.store.config.Root, ".staging-"+strings.TrimSuffix(current.OpaqueLocator, ".xrd"))
			staged, err := derivedReconcileRegularFile(stagingPath, current.PhysicalSize)
			if err != nil {
				return err
			}
			if !staged {
				return errStagedDerivedBlobUnreadable
			}
			if err := os.Rename(stagingPath, finalPath); err != nil {
				return errors.Join(ErrDerivedStoreUnavailable, err)
			}
			if err := syncDerivedDirectory(reconciler.store.config.Root); err != nil {
				return err
			}
		}
		var references int64
		if err := tx.Model(&model.BackupAssetDerivedBlobReference{}).
			Where("blob_id = ? AND state = ?", current.ID, "active").Count(&references).Error; err != nil {
			return fmt.Errorf("count staged Derived references: %w", err)
		}
		updated := tx.Model(&model.BackupAssetDerivedBlob{}).
			Where("id = ? AND state = ? AND opaque_locator = ?", current.ID, "staged", current.OpaqueLocator).
			Updates(map[string]any{"state": "active", "ref_count": references, "updated_at": reconciler.store.utcNow()})
		if updated.Error != nil {
			return fmt.Errorf("recover staged Derived blob: %w", updated.Error)
		}
		recovered = updated.RowsAffected == 1
		return nil
	})
	return recovered, err
}

func (reconciler *DerivedReconciler) repairRefCount(ctx context.Context, blobID, expectedLocator string, previous int64) (bool, error) {
	repaired := false
	err := reconciler.store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row model.BackupAssetDerivedBlob
		loaded := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", blobID).Limit(1).Find(&row)
		if loaded.Error != nil || loaded.RowsAffected != 1 || row.State != "active" || row.OpaqueLocator != expectedLocator || row.RefCount != previous {
			return loaded.Error
		}
		var count int64
		if err := tx.Model(&model.BackupAssetDerivedBlobReference{}).
			Where("blob_id = ? AND state = ?", blobID, "active").Count(&count).Error; err != nil {
			return err
		}
		if row.RefCount == count {
			return nil
		}
		updated := tx.Model(&model.BackupAssetDerivedBlob{}).Where("id = ? AND ref_count = ?", blobID, row.RefCount).
			Updates(map[string]any{"ref_count": count, "updated_at": reconciler.store.utcNow()})
		if updated.Error != nil {
			return updated.Error
		}
		repaired = updated.RowsAffected == 1
		return nil
	})
	return repaired, err
}

func (reconciler *DerivedReconciler) revokeUnreadableBlob(ctx context.Context, blobID, expectedLocator string, references int64, result *DerivedReconcileResult) error {
	if references > 0 {
		var setIDs []string
		if err := reconciler.store.db.WithContext(ctx).Table("backup_asset_derived_artifact_sets AS sets").
			Distinct("sets.id").
			Joins("JOIN backup_asset_derived_artifacts AS artifacts ON artifacts.artifact_set_id = sets.id").
			Joins("JOIN backup_asset_derived_blob_references AS refs ON refs.artifact_id = artifacts.id AND refs.blob_id = artifacts.blob_id").
			Where("artifacts.blob_id = ? AND refs.state = ? AND sets.state IN ?", blobID, "active", []string{"active", "stale"}).
			Order("sets.id ASC").Limit(reconciler.batchSize).Pluck("sets.id", &setIDs).Error; err != nil {
			return fmt.Errorf("load Derived sets for unreadable blob: %w", err)
		}
		if len(setIDs) == 0 {
			return reconciler.store.discardBlobIfUnreferencedObserved(ctx, blobID, expectedLocator)
		}
		for _, setID := range setIDs {
			if err := reconciler.lifecycle.revokeSetWithManagedFenceObserved(ctx, setID, DerivedRevokeKeyLoss, blobID, expectedLocator); err != nil {
				return err
			}
			result.RevokedUnreadableSets++
		}
	}
	return reconciler.store.discardBlobIfUnreferencedObserved(ctx, blobID, expectedLocator)
}

func (reconciler *DerivedReconciler) reconcileFiles(ctx context.Context, result *DerivedReconcileResult) error {
	entries, err := os.ReadDir(reconciler.store.config.Root)
	if err != nil {
		return errors.Join(ErrDerivedStoreUnavailable, err)
	}
	processed := 0
	last := ""
	for _, entry := range entries {
		name := entry.Name()
		if reconciler.fileCursor != "" && name <= reconciler.fileCursor {
			continue
		}
		if processed >= reconciler.batchSize {
			break
		}
		processed++
		last = name
		var count int64
		switch {
		case strings.HasPrefix(name, ".writing-") || strings.HasPrefix(name, ".verify-") || strings.HasPrefix(name, ".replay-"):
			info, err := entry.Info()
			if err != nil {
				return errors.Join(ErrDerivedStoreUnavailable, err)
			}
			if time.Since(info.ModTime()) < time.Hour {
				continue
			}
		case strings.HasPrefix(name, ".staging-"):
			parts := strings.Split(strings.TrimPrefix(name, ".staging-"), "-")
			if len(parts) < 1 || len(parts) > 2 || backupasset.ValidateOpaqueID(parts[0]) != nil || (len(parts) == 2 && backupasset.ValidateOpaqueID(parts[1]) != nil) {
				continue
			}
			if err := reconciler.store.db.WithContext(ctx).Model(&model.BackupAssetDerivedBlob{}).
				Where("id = ? AND state = ? AND opaque_locator = ?", parts[0], "staged", strings.TrimPrefix(name, ".staging-")+".xrd").Count(&count).Error; err != nil {
				return err
			}
		case strings.HasSuffix(name, ".xrd"):
			parts := strings.Split(strings.TrimSuffix(name, ".xrd"), "-")
			if len(parts) < 1 || len(parts) > 2 || backupasset.ValidateOpaqueID(parts[0]) != nil || (len(parts) == 2 && backupasset.ValidateOpaqueID(parts[1]) != nil) || !safeOpaqueLocator(name) {
				continue
			}
			if err := reconciler.store.db.WithContext(ctx).Model(&model.BackupAssetDerivedBlob{}).
				Where("opaque_locator = ? AND state IN ?", name, []string{"staged", "active", "purging", "purge_failed"}).Count(&count).Error; err != nil {
				return err
			}
		default:
			continue
		}
		if count == 0 {
			path := filepath.Join(reconciler.store.config.Root, name)
			if err := reconciler.store.removeFile(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return errors.Join(ErrDerivedBlobUnavailable, err)
			}
			result.RemovedFileOrphans++
		}
	}
	if processed < reconciler.batchSize {
		reconciler.fileCursor = ""
	} else {
		reconciler.fileCursor = last
	}
	return nil
}

func derivedReconcileRegularFile(path string, physicalSize int64) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, errors.Join(ErrDerivedBlobUnavailable, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 || info.Size() != physicalSize {
		return false, nil
	}
	return true, nil
}

func NewReconciler(coordinator *Coordinator, grants *GrantService, now func() time.Time, config ReconcilerConfig) (*Reconciler, error) {
	if coordinator == nil || grants == nil || config.BatchSize <= 0 || config.BatchSize > 10000 {
		return nil, ErrInvalidContract
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	if config.RetryBase == 0 {
		config.RetryBase = 5 * time.Second
	}
	if config.RetryBase < time.Second || config.RetryBase > 5*time.Minute {
		return nil, ErrInvalidContract
	}
	return &Reconciler{coordinator: coordinator, grants: grants, now: now, config: config}, nil
}

func (reconciler *Reconciler) Reconcile(ctx context.Context) (ReconcileResult, error) {
	now := reconciler.utcNow()
	var attempts []model.BackupAssetProcessingAttempt
	if err := reconciler.coordinator.db.WithContext(ctx).
		Where("state = ? AND is_current = ? AND (worker_lease_expires_at <= ? OR absolute_deadline <= ?)", "active", true, now, now).
		Order("worker_lease_expires_at ASC, id ASC").Limit(reconciler.config.BatchSize).Find(&attempts).Error; err != nil {
		return ReconcileResult{}, fmt.Errorf("load expired processing attempts: %w", err)
	}
	result := ReconcileResult{}
	for _, attempt := range attempts {
		expired, retrying, err := reconciler.expireAttempt(ctx, attempt.ID, now)
		if err != nil {
			return result, err
		}
		if expired || retrying {
			result.ExpiredAttempts++
		}
		if expired {
			result.ExpiredJobs++
		}
		if retrying {
			result.RetryingJobs++
		}
	}
	return result, nil
}

func (reconciler *Reconciler) PromoteRetries(ctx context.Context) (int, error) {
	now := reconciler.utcNow()
	var jobs []model.BackupAssetProcessingJob
	if err := reconciler.coordinator.db.WithContext(ctx).
		Where("state = ? AND is_current = ? AND retry_at IS NOT NULL AND retry_at <= ?", ProcessingRetryWait, true, now).
		Order("retry_at ASC, id ASC").Limit(reconciler.config.BatchSize).Find(&jobs).Error; err != nil {
		return 0, fmt.Errorf("load due processing retries: %w", err)
	}
	promoted := 0
	for _, candidate := range jobs {
		err := reconciler.coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var job model.BackupAssetProcessingJob
			result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", candidate.ID).Limit(1).Find(&job)
			if result.Error != nil || result.RowsAffected != 1 || job.State != string(ProcessingRetryWait) ||
				job.RetryAt == nil || job.RetryAt.UTC().After(now) || !job.IsCurrent {
				return nil
			}
			if !now.Before(job.AbsoluteDeadline.UTC()) {
				return reconciler.expireRetryJobTx(tx, job, now)
			}
			var interests int64
			if err := tx.Model(&model.BackupAssetProcessingInterest{}).Where("job_id = ? AND active = ?", job.ID, true).Count(&interests).Error; err != nil {
				return err
			}
			if interests == 0 {
				return reconciler.cancelRetryJobTx(tx, job, now)
			}
			if job.RetryCount > reconciler.coordinator.config.RetryMax {
				return reconciler.expireRetryJobTx(tx, job, now)
			}
			revision, err := ValidateTransition(TransitionRequest{
				From: ProcessingRetryWait, To: ProcessingQueued,
				CurrentRevision: job.TransitionRevision, ExpectedRevision: job.TransitionRevision,
			})
			if err != nil {
				return err
			}
			updated := tx.Model(&model.BackupAssetProcessingJob{}).Where("id = ? AND transition_revision = ?", job.ID, job.TransitionRevision).
				Updates(map[string]any{
					"state": string(ProcessingQueued), "transition_revision": revision, "error_code": "",
					"retry_at": nil, "queued_at": now, "started_at": nil, "updated_at": now,
					"version": gorm.Expr("version + 1"),
				})
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected == 1 {
				promoted++
			}
			return nil
		})
		if err != nil {
			return promoted, err
		}
	}
	return promoted, nil
}

// Shutdown retires every active Worker attempt without discarding its durable
// interest. The job remains current in retry_wait, while grants and both lease
// authorities are made unusable before the process releases its runtime graph.
func (reconciler *Reconciler) Shutdown(ctx context.Context) (int, error) {
	if reconciler == nil || reconciler.coordinator == nil || reconciler.grants == nil {
		return 0, ErrInvalidContract
	}
	if ctx == nil {
		ctx = context.Background()
	}
	now := reconciler.utcNow()
	if err := reconciler.coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.BackupAssetWorkerIdentity{}).
			Where("trust_state = ? AND health_state IN ?", "active", []string{"ready", "degraded"}).
			Updates(map[string]any{"health_state": "draining", "updated_at": now}).Error; err != nil {
			return fmt.Errorf("mark Processing Workers draining: %w", err)
		}
		if err := tx.Model(&model.BackupAssetWorkerCapability{}).
			Where("health_state IN ?", []string{"ready", "degraded"}).
			Updates(map[string]any{"health_state": "draining", "updated_at": now}).Error; err != nil {
			return fmt.Errorf("mark Processing capabilities draining: %w", err)
		}
		return nil
	}); err != nil {
		return 0, err
	}

	drained := 0
	for {
		if err := ctx.Err(); err != nil {
			return drained, err
		}
		var attempts []model.BackupAssetProcessingAttempt
		if err := reconciler.coordinator.db.WithContext(ctx).
			Where("state = ? AND is_current = ?", "active", true).
			Order("started_at ASC, id ASC").Limit(reconciler.config.BatchSize).Find(&attempts).Error; err != nil {
			return drained, fmt.Errorf("load Processing shutdown attempts: %w", err)
		}
		if len(attempts) == 0 {
			return drained, nil
		}
		progressed := 0
		for _, attempt := range attempts {
			retired, err := reconciler.retireAttemptForShutdown(ctx, attempt.ID, now)
			if err != nil {
				return drained, err
			}
			if retired {
				drained++
				progressed++
			}
		}
		if progressed == 0 {
			return drained, ErrAttemptLost
		}
	}
}

func (reconciler *Reconciler) retireAttemptForShutdown(ctx context.Context, attemptID string, now time.Time) (bool, error) {
	retired := false
	err := reconciler.coordinator.retryConflicts(ctx, func() error {
		retired = false
		return reconciler.coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			job, attempt, found, err := lockRetiringAttemptTx(tx, attemptID)
			if err != nil || !found {
				return err
			}
			if err := revokeAttemptGrantsTx(tx, attempt.ID, now, "shutdown"); err != nil {
				return err
			}
			if err := reconciler.retireRecoveryLeaseTx(ctx, tx, attempt, now); err != nil {
				return err
			}
			if err := finishAttemptTx(tx, attempt.ID, now, "canceled", ""); err != nil {
				return err
			}

			updates := map[string]any{
				"current_attempt_id": nil, "updated_at": now, "version": gorm.Expr("version + 1"),
			}
			var nextRevision int64
			if ProcessingState(job.State) == ProcessingCancelRequested {
				nextRevision, err = ValidateTransition(TransitionRequest{
					From: ProcessingCancelRequested, To: ProcessingCanceled,
					CurrentRevision: job.TransitionRevision, ExpectedRevision: job.TransitionRevision,
					CancelReason: CancelReason(job.CancelReason),
				})
				updates["state"] = string(ProcessingCanceled)
				updates["is_current"] = false
				updates["finished_at"] = now
			} else {
				retryAt := now.Add(reconciler.retryDelay(job.RetryCount + 1))
				nextRevision, err = ValidateTransition(TransitionRequest{
					From: ProcessingState(job.State), To: ProcessingRetryWait,
					CurrentRevision: job.TransitionRevision, ExpectedRevision: job.TransitionRevision,
					ErrorCode: ProcessingErrorWorkerUnavailable, RetryAt: &retryAt,
				})
				updates["state"] = string(ProcessingRetryWait)
				updates["error_code"] = string(ProcessingErrorWorkerUnavailable)
				updates["retry_at"] = retryAt
			}
			if err != nil {
				return err
			}
			updates["transition_revision"] = nextRevision
			updated := tx.Model(&model.BackupAssetProcessingJob{}).
				Where("id = ? AND transition_revision = ?", job.ID, job.TransitionRevision).Updates(updates)
			if updated.Error != nil {
				return fmt.Errorf("retire Processing shutdown job: %w", updated.Error)
			}
			if updated.RowsAffected != 1 {
				return ErrRevisionConflict
			}
			if updates["state"] == string(ProcessingRetryWait) {
				if err := reconciler.coordinator.recomputePriorityTx(ctx, tx, job.ID); err != nil {
					return err
				}
			}
			retired = true
			return nil
		})
	})
	return retired, err
}

func (reconciler *Reconciler) expireAttempt(ctx context.Context, attemptID string, now time.Time) (bool, bool, error) {
	expiredJob := false
	retryingJob := false
	err := reconciler.coordinator.retryConflicts(ctx, func() error {
		expiredJob, retryingJob = false, false
		return reconciler.coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			job, attempt, found, err := lockRetiringAttemptTx(tx, attemptID)
			if err != nil || !found {
				return err
			}
			if now.Before(attempt.WorkerLeaseExpiresAt.UTC()) && now.Before(attempt.AbsoluteDeadline.UTC()) {
				return nil
			}
			if err := revokeAttemptGrantsTx(tx, attempt.ID, now, "lease_lost"); err != nil {
				return err
			}
			if err := reconciler.retireRecoveryLeaseTx(ctx, tx, attempt, now); err != nil {
				return err
			}
			if err := finishAttemptTx(tx, attempt.ID, now, "expired", string(ProcessingErrorLeaseLost)); err != nil {
				return err
			}
			if !now.Before(job.AbsoluteDeadline.UTC()) || !now.Before(attempt.AbsoluteDeadline.UTC()) {
				revision, err := ValidateTransition(TransitionRequest{
					From: ProcessingState(job.State), To: ProcessingExpired,
					CurrentRevision: job.TransitionRevision, ExpectedRevision: job.TransitionRevision,
					ExpiryReason: ExpiryReasonDeadline,
				})
				if err != nil {
					return err
				}
				updated := tx.Model(&model.BackupAssetProcessingJob{}).Where("id = ? AND transition_revision = ?", job.ID, job.TransitionRevision).
					Updates(map[string]any{
						"state": string(ProcessingExpired), "transition_revision": revision,
						"expiry_reason": string(ExpiryReasonDeadline), "current_attempt_id": nil,
						"is_current": false, "finished_at": now, "updated_at": now, "version": gorm.Expr("version + 1"),
					})
				if updated.Error != nil || updated.RowsAffected != 1 {
					return errors.Join(ErrRevisionConflict, updated.Error)
				}
				expiredJob = true
				return nil
			}
			retryAt := now.Add(reconciler.retryDelay(job.RetryCount + 1))
			revision, err := ValidateTransition(TransitionRequest{
				From: ProcessingState(job.State), To: ProcessingRetryWait,
				CurrentRevision: job.TransitionRevision, ExpectedRevision: job.TransitionRevision,
				ErrorCode: ProcessingErrorLeaseLost, RetryAt: &retryAt,
			})
			if err != nil {
				return err
			}
			updated := tx.Model(&model.BackupAssetProcessingJob{}).Where("id = ? AND transition_revision = ?", job.ID, job.TransitionRevision).
				Updates(map[string]any{
					"state": string(ProcessingRetryWait), "transition_revision": revision,
					"error_code": string(ProcessingErrorLeaseLost), "retry_count": job.RetryCount + 1,
					"retry_at": retryAt, "current_attempt_id": nil, "updated_at": now, "version": gorm.Expr("version + 1"),
				})
			if updated.Error != nil || updated.RowsAffected != 1 {
				return errors.Join(ErrRevisionConflict, updated.Error)
			}
			if err := reconciler.coordinator.recomputePriorityTx(ctx, tx, job.ID); err != nil {
				return err
			}
			retryingJob = true
			return nil
		})
	})
	return expiredJob, retryingJob, err
}

// Discover without locks, then take the shared point, job and attempt order.
func lockRetiringAttemptTx(tx *gorm.DB, attemptID string) (model.BackupAssetProcessingJob, model.BackupAssetProcessingAttempt, bool, error) {
	var locator model.BackupAssetProcessingAttempt
	var job model.BackupAssetProcessingJob
	var attempt model.BackupAssetProcessingAttempt
	result := tx.Select("id", "job_id").Where("id = ?", attemptID).Limit(1).Find(&locator)
	if result.Error != nil || result.RowsAffected != 1 {
		return job, attempt, false, result.Error
	}
	var initial model.BackupAssetProcessingJob
	result = tx.Select("id", "recovery_point_id").Where("id = ?", locator.JobID).Limit(1).Find(&initial)
	if result.Error != nil || result.RowsAffected != 1 {
		return job, attempt, false, result.Error
	}
	var point model.RecoveryPoint
	result = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").Where("id = ?", initial.RecoveryPointID).Limit(1).Find(&point)
	if result.Error != nil || result.RowsAffected != 1 {
		return job, attempt, false, result.Error
	}
	result = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", locator.JobID).Limit(1).Find(&job)
	if result.Error != nil || result.RowsAffected != 1 {
		return job, attempt, false, result.Error
	}
	result = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND job_id = ?", attemptID, job.ID).Limit(1).Find(&attempt)
	if result.Error != nil || result.RowsAffected != 1 {
		return job, attempt, false, result.Error
	}
	if job.RecoveryPointID != initial.RecoveryPointID || !job.IsCurrent || job.CurrentAttemptID == nil || *job.CurrentAttemptID != attempt.ID || attempt.State != "active" || !attempt.IsCurrent {
		return job, attempt, false, nil
	}
	return job, attempt, true, nil
}

func (reconciler *Reconciler) retireRecoveryLeaseTx(ctx context.Context, tx *gorm.DB, attempt model.BackupAssetProcessingAttempt, now time.Time) error {
	var lease model.RecoveryPointLease
	result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", attempt.RecoveryPointLeaseID).Limit(1).Find(&lease)
	if result.Error != nil {
		return fmt.Errorf("load Processing recovery lease: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return ErrAttemptLost
	}
	fence := leaseFenceFromRow(lease)
	if lease.Status == string(backupasset.LeaseActive) && now.Before(lease.LeaseExpiresAt.UTC()) && now.Before(lease.AbsoluteDeadline.UTC()) &&
		hashFence(lease.FenceToken) == attempt.RecoveryPointFenceHash {
		return reconciler.coordinator.leaseService.ReleaseTx(ctx, tx, fence)
	}
	if lease.Status == string(backupasset.LeaseActive) {
		if err := tx.Model(&model.RecoveryPointLease{}).Where("id = ? AND status = ?", lease.ID, backupasset.LeaseActive).
			Updates(map[string]any{"status": backupasset.LeaseExpired, "updated_at": now}).Error; err != nil {
			return err
		}
	}
	return nil
}

func (reconciler *Reconciler) expireRetryJobTx(tx *gorm.DB, job model.BackupAssetProcessingJob, now time.Time) error {
	revision, err := ValidateTransition(TransitionRequest{
		From: ProcessingRetryWait, To: ProcessingExpired,
		CurrentRevision: job.TransitionRevision, ExpectedRevision: job.TransitionRevision,
		ExpiryReason: ExpiryReasonDeadline,
	})
	if err != nil {
		return err
	}
	return tx.Model(&model.BackupAssetProcessingJob{}).Where("id = ? AND transition_revision = ?", job.ID, job.TransitionRevision).
		Updates(map[string]any{
			"state": string(ProcessingExpired), "transition_revision": revision, "error_code": "", "retry_at": nil,
			"expiry_reason": string(ExpiryReasonDeadline), "is_current": false, "finished_at": now,
			"updated_at": now, "version": gorm.Expr("version + 1"),
		}).Error
}

func (reconciler *Reconciler) cancelRetryJobTx(tx *gorm.DB, job model.BackupAssetProcessingJob, now time.Time) error {
	revision, err := ValidateTransition(TransitionRequest{
		From: ProcessingRetryWait, To: ProcessingCancelRequested,
		CurrentRevision: job.TransitionRevision, ExpectedRevision: job.TransitionRevision,
		CancelReason: CancelReasonInterestWithdrawn,
	})
	if err != nil {
		return err
	}
	return tx.Model(&model.BackupAssetProcessingJob{}).Where("id = ? AND transition_revision = ?", job.ID, job.TransitionRevision).
		Updates(map[string]any{
			"state": string(ProcessingCancelRequested), "transition_revision": revision, "error_code": "", "retry_at": nil,
			"cancel_reason": string(CancelReasonInterestWithdrawn), "updated_at": now, "version": gorm.Expr("version + 1"),
		}).Error
}

func (reconciler *Reconciler) retryDelay(retry int) time.Duration {
	delay := reconciler.config.RetryBase
	for index := 1; index < retry && delay < 15*time.Minute; index++ {
		delay *= 2
	}
	if delay > 15*time.Minute {
		return 15 * time.Minute
	}
	return delay
}

func (reconciler *Reconciler) utcNow() time.Time { return reconciler.now().UTC() }
