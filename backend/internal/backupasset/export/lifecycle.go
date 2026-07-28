package export

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type LifecyclePort interface {
	FenceAttempts(context.Context, string) error
	RevokeDeliveries(context.Context, string) error
	DrainStreams(context.Context, string) error
	DestroyJobKeyAndSelection(context.Context, string) error
	ReleaseSourcesAndNonStore(context.Context, string) error
	PurgeCiphertext(context.Context, string) error
	ReleaseStoreBytes(context.Context, string) error
}

type LifecycleDependencies struct {
	DB   *gorm.DB
	Port LifecyclePort
	Now  func() time.Time
}

type Lifecycle struct {
	db   *gorm.DB
	port LifecyclePort
	now  func() time.Time

	reconcileMu sync.Mutex
}

const (
	maxLifecycleReconcileLimit  = 10000
	lifecycleReconcileScanScale = 32
	lifecycleSweepLeaseTTL      = 30 * time.Second
)

var runtimeStopActiveExecutionStates = []string{
	string(ExecutionQueued),
	string(ExecutionRunning),
	string(ExecutionRetryWait),
	string(ExecutionSealing),
	string(ExecutionReady),
}

var runtimeStopPurgedTransitionExecutionStates = []string{
	string(ExecutionCancelRequested),
	string(ExecutionExpiring),
}

var runtimeStopTerminalExecutionStates = []string{
	string(ExecutionCancelRequested),
	string(ExecutionFailed),
	string(ExecutionSourceExpired),
	string(ExecutionCanceled),
	string(ExecutionExpiring),
	string(ExecutionExpired),
}

var errLifecycleSweepContended = fmt.Errorf("%w: export lifecycle sweep lease is live", ErrUnavailable)

var errRuntimeStopEarlierTerminalCleanup = fmt.Errorf(
	"%w: earlier terminal export cleanup remains before lifecycle sweep cursor", ErrUnavailable,
)

type lifecycleSweepLease struct {
	bucketID  string
	revision  int64
	cursor    int64
	highWater int64
}

// RuntimeStopTerminalizationProgress describes one bounded, durable runtime-stop
// sweep pass. Advanced counts candidate cursors durably persisted by the pass;
// callers may continue after a per-job cleanup error only when it is non-zero.
// Complete is true only after the finite high-water window has been released.
type RuntimeStopTerminalizationProgress struct {
	Processed int
	Advanced  int
	Complete  bool
}

type ExportJobDeliveryLifecycle interface {
	BeginRevokeExportJob(context.Context, string, string) error
	DrainExportJob(context.Context, string) error
}

type ExportSourceLeaseLifecycle interface {
	ReleaseTx(context.Context, *gorm.DB, backupasset.LeaseFence) error
	TakeoverTx(context.Context, *gorm.DB, backupasset.TakeoverLeaseRequest) (backupasset.Lease, error)
}

type PersistentLifecyclePortDependencies struct {
	DB             *gorm.DB
	Delivery       ExportJobDeliveryLifecycle
	Sources        ExportSourceLeaseLifecycle
	Quota          *QuotaService
	Store          *Store
	Now            func() time.Time
	WorkerCapacity *WorkerCapacityLimits
	AttemptWork    *AttemptWorkRegistry
}

type PersistentLifecyclePort struct {
	db             *gorm.DB
	delivery       ExportJobDeliveryLifecycle
	sources        ExportSourceLeaseLifecycle
	quota          *QuotaService
	store          *Store
	now            func() time.Time
	workerCapacity *WorkerCapacityLimits
	attemptWork    *AttemptWorkRegistry
}

const persistentLifecycleItemAttemptUpdateBatchSize = 400

func NewPersistentLifecyclePort(dependencies PersistentLifecyclePortDependencies) (*PersistentLifecyclePort, error) {
	if dependencies.DB == nil || dependencies.Delivery == nil || dependencies.Sources == nil ||
		dependencies.Quota == nil || dependencies.Store == nil || dependencies.Store.closed() || dependencies.AttemptWork == nil {
		return nil, ErrUnavailable
	}
	if dependencies.WorkerCapacity != nil && !validWorkerCapacityLimits(*dependencies.WorkerCapacity) {
		return nil, ErrUnavailable
	}
	if dependencies.Now == nil {
		dependencies.Now = func() time.Time { return time.Now().UTC() }
	}
	return &PersistentLifecyclePort{
		db: dependencies.DB, delivery: dependencies.Delivery, sources: dependencies.Sources,
		quota: dependencies.Quota, store: dependencies.Store, now: dependencies.Now, workerCapacity: dependencies.WorkerCapacity,
		attemptWork: dependencies.AttemptWork,
	}, nil
}

func (port *PersistentLifecyclePort) FenceAttempts(ctx context.Context, jobID string) error {
	if port == nil || port.attemptWork == nil || backupasset.ValidateOpaqueID(jobID) != nil {
		return ErrUnavailable
	}
	now := port.now().UTC()
	var owner struct {
		OwnerUserID uint `gorm:"column:owner_user_id"`
	}
	if port.workerCapacity != nil {
		result := port.db.WithContext(ctx).Model(&model.BackupAssetExportJob{}).Select("owner_user_id").
			Where("id = ?", jobID).Limit(1).Scan(&owner)
		if result.Error != nil {
			return fmt.Errorf("discover export job owner for lifecycle fence: %w", result.Error)
		}
		if result.RowsAffected != 1 || owner.OwnerUserID == 0 {
			return ErrNotFound
		}
	}
	return port.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var buckets quotaBucketPair
		if port.workerCapacity != nil {
			var err error
			buckets, err = ensureAndLockQuotaBucketPairTx(tx, owner.OwnerUserID, now)
			if err != nil {
				return err
			}
		}
		var job model.BackupAssetExportJob
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", jobID).Limit(1).Find(&job)
		if result.Error != nil {
			return fmt.Errorf("lock export job for attempt fencing: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return ErrNotFound
		}
		if port.workerCapacity != nil && job.OwnerUserID != owner.OwnerUserID {
			return ErrAttemptFenceLost
		}

		observedAttemptID := job.CurrentAttemptID
		recomputedProjections := false
		if observedAttemptID != nil {
			var attempt model.BackupAssetExportAttempt
			result = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("id = ? AND job_id = ?", *observedAttemptID, job.ID).Limit(1).Find(&attempt)
			if result.Error != nil {
				return fmt.Errorf("lock pointed export attempt for fencing: %w", result.Error)
			}
			if result.RowsAffected != 1 {
				return ErrAttemptFenceLost
			}
			if port.workerCapacity != nil {
				_, pairErr := lockAttemptWorkerReservationPairTx(tx, buckets, job, attempt)
				if pairErr != nil {
					return pairErr
				}
			}

			observedAttemptState := AttemptState(attempt.State)
			switch observedAttemptState {
			case AttemptActive, AttemptSealing:
				if !attempt.IsCurrent {
					return ErrAttemptFenceLost
				}
				terminalAttemptState, failureCategory, err := persistentLifecycleAttemptOutcome(job)
				if err != nil {
					return err
				}
				var unfinishedItems []struct {
					ID string `gorm:"column:id"`
				}
				result = tx.Model(&model.BackupAssetExportItem{}).Select("id").
					Clauses(clause.Locking{Strength: "UPDATE"}).
					Where("job_id = ? AND current_attempt_id = ? AND state IN ?", job.ID, attempt.ID,
						[]string{string(ItemPending), string(ItemRead)}).
					Order("ordinal ASC, id ASC").Find(&unfinishedItems)
				if result.Error != nil {
					return fmt.Errorf("lock unfinished export item projections for fencing: %w", result.Error)
				}
				var unfinished []struct {
					ID string `gorm:"column:id"`
				}
				result = tx.Model(&model.BackupAssetExportItemAttempt{}).Select("id").
					Clauses(clause.Locking{Strength: "UPDATE"}).
					Where("job_id = ? AND attempt_id = ? AND state IN ?", job.ID, attempt.ID,
						[]string{string(ItemPending), string(ItemRead)}).
					Order("item_id ASC, id ASC").Find(&unfinished)
				if result.Error != nil {
					return fmt.Errorf("lock unfinished export item attempts for fencing: %w", result.Error)
				}
				if len(unfinishedItems) > 0 {
					unfinishedIDs := make([]string, 0, len(unfinishedItems))
					for _, item := range unfinishedItems {
						unfinishedIDs = append(unfinishedIDs, item.ID)
					}
					var updated int64
					for start := 0; start < len(unfinishedIDs); start += persistentLifecycleItemAttemptUpdateBatchSize {
						end := min(start+persistentLifecycleItemAttemptUpdateBatchSize, len(unfinishedIDs))
						batch := unfinishedIDs[start:end]
						result = tx.Model(&model.BackupAssetExportItem{}).
							Where("id IN ? AND job_id = ? AND current_attempt_id = ? AND state IN ?", batch, job.ID, attempt.ID,
								[]string{string(ItemPending), string(ItemRead)}).
							Updates(map[string]any{
								"state": string(ItemFailed), "error_category": failureCategory, "updated_at": now,
							})
						if result.Error != nil {
							return fmt.Errorf("fence export item projections: %w", result.Error)
						}
						if result.RowsAffected != int64(len(batch)) {
							return ErrAttemptFenceLost
						}
						updated += result.RowsAffected
					}
					if updated != int64(len(unfinishedItems)) {
						return ErrAttemptFenceLost
					}
				}
				if len(unfinished) > 0 {
					unfinishedIDs := make([]string, 0, len(unfinished))
					for _, itemAttempt := range unfinished {
						unfinishedIDs = append(unfinishedIDs, itemAttempt.ID)
					}
					finishedAt := now
					var updated int64
					for start := 0; start < len(unfinishedIDs); start += persistentLifecycleItemAttemptUpdateBatchSize {
						end := min(start+persistentLifecycleItemAttemptUpdateBatchSize, len(unfinishedIDs))
						batch := unfinishedIDs[start:end]
						result = tx.Model(&model.BackupAssetExportItemAttempt{}).
							Where("id IN ? AND job_id = ? AND attempt_id = ? AND state IN ?", batch, job.ID, attempt.ID,
								[]string{string(ItemPending), string(ItemRead)}).
							Updates(map[string]any{
								"state": string(ItemFailed), "error_category": failureCategory, "finished_at": finishedAt,
							})
						if result.Error != nil {
							return fmt.Errorf("fence export item attempts: %w", result.Error)
						}
						if result.RowsAffected != int64(len(batch)) {
							return ErrAttemptFenceLost
						}
						updated += result.RowsAffected
					}
					if updated != int64(len(unfinished)) {
						return ErrAttemptFenceLost
					}
				}
				counts, err := persistentLifecycleProjectionCountsTx(tx, job, attempt.ID)
				if err != nil {
					return err
				}
				finishedAt := now
				result = tx.Model(&model.BackupAssetExportAttempt{}).
					Where("id = ? AND job_id = ? AND is_current = ? AND state = ?", attempt.ID, job.ID, true,
						observedAttemptState).
					Updates(map[string]any{
						"state": string(terminalAttemptState), "is_current": false, "failure_category": failureCategory,
						"checkpoint_ordinal": counts.maxOrdinal, "checkpoint_item_count": counts.finalized,
						"checkpoint_logical_bytes": counts.logical, "checkpoint_provider_bytes": counts.provider,
						"finished_at": finishedAt, "updated_at": now,
					})
				if result.Error != nil {
					return fmt.Errorf("fence current export attempt: %w", result.Error)
				}
				if result.RowsAffected != 1 {
					return ErrAttemptFenceLost
				}
				job.PackedCount = counts.packed
				job.SkippedCount = counts.skipped
				job.FailedCount = counts.failed
				job.LogicalBytes = counts.logical
				job.ProviderBytes = counts.provider
				recomputedProjections = true
			case AttemptSealed:
				if attempt.IsCurrent ||
					(ExecutionState(job.ExecutionState) != ExecutionReady && ExecutionState(job.ExecutionState) != ExecutionExpiring &&
						ExecutionState(job.ExecutionState) != ExecutionCancelRequested) {
					return ErrAttemptFenceLost
				}
			default:
				return ErrAttemptFenceLost
			}
		}

		jobCAS := tx.Model(&model.BackupAssetExportJob{}).
			Where("id = ? AND current_fence_revision = ? AND transition_revision = ?",
				job.ID, job.CurrentFenceRevision, job.TransitionRevision)
		if observedAttemptID == nil {
			jobCAS = jobCAS.Where("current_attempt_id IS NULL")
		} else {
			jobCAS = jobCAS.Where("current_attempt_id = ?", *observedAttemptID)
		}
		updates := map[string]any{
			"current_attempt_id": nil, "current_fence_revision": gorm.Expr("current_fence_revision + 1"),
			"updated_at": now,
		}
		if recomputedProjections {
			updates["packed_count"] = job.PackedCount
			updates["skipped_count"] = job.SkippedCount
			updates["failed_count"] = job.FailedCount
			updates["logical_bytes"] = job.LogicalBytes
			updates["provider_bytes"] = job.ProviderBytes
		}
		result = jobCAS.Updates(updates)
		if result.Error != nil {
			return fmt.Errorf("persist export attempt fence: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return ErrAttemptFenceLost
		}
		return nil
	})
}

func persistentLifecycleAttemptOutcome(job model.BackupAssetExportJob) (AttemptState, string, error) {
	switch ExecutionState(job.ExecutionState) {
	case ExecutionCancelRequested:
		return AttemptCanceled, "canceled", nil
	case ExecutionFailed:
		if !validLifecycleFailureCategory(job.ErrorCategory) {
			return "", "", ErrAttemptFenceLost
		}
		return AttemptFailed, job.ErrorCategory, nil
	case ExecutionSourceExpired:
		if job.ErrorCategory != "source_expired" {
			return "", "", ErrAttemptFenceLost
		}
		return AttemptFailed, job.ErrorCategory, nil
	default:
		return "", "", ErrAttemptFenceLost
	}
}

type persistentLifecycleProjectionCounts struct {
	packed, skipped, failed, finalized, logical, provider int64
	maxOrdinal                                            int
}

func persistentLifecycleProjectionCountsTx(
	tx *gorm.DB,
	job model.BackupAssetExportJob,
	attemptID string,
) (persistentLifecycleProjectionCounts, error) {
	var items []model.BackupAssetExportItem
	if err := tx.Where("job_id = ?", job.ID).Order("ordinal ASC, id ASC").Find(&items).Error; err != nil {
		return persistentLifecycleProjectionCounts{}, fmt.Errorf("load closed export item projections: %w", err)
	}
	var observations []model.BackupAssetExportItemAttempt
	if err := tx.Where("job_id = ? AND attempt_id = ?", job.ID, attemptID).
		Order("item_id ASC, id ASC").Find(&observations).Error; err != nil {
		return persistentLifecycleProjectionCounts{}, fmt.Errorf("load closed export item-attempt observations: %w", err)
	}
	if int64(len(items)) != job.ItemCount || len(observations) != len(items) {
		return persistentLifecycleProjectionCounts{}, ErrAttemptFenceLost
	}
	byItem := make(map[string]model.BackupAssetExportItemAttempt, len(observations))
	for _, observation := range observations {
		if _, duplicate := byItem[observation.ItemID]; duplicate {
			return persistentLifecycleProjectionCounts{}, ErrAttemptFenceLost
		}
		byItem[observation.ItemID] = observation
	}
	var counts persistentLifecycleProjectionCounts
	for _, item := range items {
		observation, ok := byItem[item.ID]
		if !ok || item.CurrentAttemptID == nil || *item.CurrentAttemptID != attemptID ||
			item.State != observation.State || item.LogicalBytes != observation.LogicalBytes ||
			item.ProviderBytes != observation.ProviderBytes || item.ErrorCategory != observation.ErrorCategory {
			return persistentLifecycleProjectionCounts{}, ErrAttemptFenceLost
		}
		switch ItemState(item.State) {
		case ItemPacked:
			counts.packed++
		case ItemSkipped:
			counts.skipped++
		case ItemFailed:
			counts.failed++
		default:
			return persistentLifecycleProjectionCounts{}, ErrAttemptFenceLost
		}
		counts.finalized++
		counts.logical += item.LogicalBytes
		counts.provider += item.ProviderBytes
		if item.Ordinal > counts.maxOrdinal {
			counts.maxOrdinal = item.Ordinal
		}
	}
	return counts, nil
}

func (port *PersistentLifecyclePort) RevokeDeliveries(ctx context.Context, jobID string) error {
	if port == nil || backupasset.ValidateOpaqueID(jobID) != nil {
		return ErrUnavailable
	}
	var job model.BackupAssetExportJob
	result := port.db.WithContext(ctx).Select("id", "execution_state", "error_category").Where("id = ?", jobID).Limit(1).Find(&job)
	if result.Error != nil {
		return fmt.Errorf("load export job for delivery revocation: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return ErrNotFound
	}
	reason, err := persistentLifecycleRevokeReason(ExecutionState(job.ExecutionState))
	if err != nil {
		return err
	}
	if job.ErrorCategory == "source_expired" {
		reason = "source_expired"
	}
	return port.delivery.BeginRevokeExportJob(ctx, jobID, reason)
}

func (port *PersistentLifecyclePort) DrainStreams(ctx context.Context, jobID string) error {
	if port == nil || port.attemptWork == nil || backupasset.ValidateOpaqueID(jobID) != nil {
		return ErrUnavailable
	}
	if err := port.delivery.DrainExportJob(ctx, jobID); err != nil {
		return err
	}
	if err := port.attemptWork.Drain(ctx, jobID); err != nil {
		return err
	}
	return port.releaseFencedWorkerReservations(ctx, jobID)
}

func (port *PersistentLifecyclePort) releaseFencedWorkerReservations(ctx context.Context, jobID string) error {
	if port == nil || port.workerCapacity == nil || backupasset.ValidateOpaqueID(jobID) != nil {
		return nil
	}
	now := port.now().UTC()
	var owner struct {
		OwnerUserID uint `gorm:"column:owner_user_id"`
	}
	result := port.db.WithContext(ctx).Model(&model.BackupAssetExportJob{}).Select("owner_user_id").Where("id = ?", jobID).Limit(1).Scan(&owner)
	if result.Error != nil {
		return fmt.Errorf("discover export job owner for worker release: %w", result.Error)
	}
	if result.RowsAffected != 1 || owner.OwnerUserID == 0 {
		return ErrNotFound
	}
	return port.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		buckets, err := ensureAndLockQuotaBucketPairTx(tx, owner.OwnerUserID, now)
		if err != nil {
			return err
		}
		var job model.BackupAssetExportJob
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND owner_user_id = ?", jobID, owner.OwnerUserID).Limit(1).Find(&job)
		if result.Error != nil {
			return fmt.Errorf("lock export job for worker release: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return ErrAttemptFenceLost
		}
		var attemptIDs []string
		if err := tx.Model(&model.BackupAssetExportReservation{}).
			Where("bucket_id = ? AND job_id = ? AND kind = ? AND state = ?", buckets.Global.ID, job.ID, "worker", "active").
			Order("attempt_id ASC").Pluck("attempt_id", &attemptIDs).Error; err != nil {
			return fmt.Errorf("discover fenced export worker reservations: %w", err)
		}
		for _, attemptID := range attemptIDs {
			var attempt model.BackupAssetExportAttempt
			result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("id = ? AND job_id = ? AND is_current = ? AND state IN ?", attemptID, job.ID, false,
					[]string{string(AttemptFailed), string(AttemptCanceled), string(AttemptSuperseded)}).Limit(1).Find(&attempt)
			if result.Error != nil || result.RowsAffected != 1 {
				return ErrAttemptFenceLost
			}
			rows, err := lockAttemptWorkerReservationPairTx(tx, buckets, job, attempt)
			if err != nil {
				return err
			}
			if err := releaseAttemptWorkerReservationPairTx(tx, rows, attempt, now); err != nil {
				return err
			}
		}
		return nil
	})
}

func (port *PersistentLifecyclePort) DestroyJobKeyAndSelection(ctx context.Context, jobID string) error {
	if port == nil || backupasset.ValidateOpaqueID(jobID) != nil {
		return ErrUnavailable
	}
	now := port.now().UTC()
	err := port.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		validateTerminalTombstone := func(key model.BackupAssetExportKey) error {
			if (key.State != "destroyed" && key.State != "lost") || len(key.WrappedDEK) != 0 ||
				len(key.EnvelopeNonce) != 0 || key.DestroyedAt == nil {
				return ErrUnavailable
			}
			var liveSelectionCount int64
			if err := tx.Model(&model.BackupAssetExportItem{}).
				Where("job_id = ? AND (length(path_nonce) <> 0 OR length(path_ciphertext) <> 0)", jobID).
				Count(&liveSelectionCount).Error; err != nil {
				return fmt.Errorf("validate terminal export selection tombstone: %w", err)
			}
			if liveSelectionCount != 0 {
				return ErrUnavailable
			}
			return nil
		}

		var keys []model.BackupAssetExportKey
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("job_id = ?", jobID).Limit(2).Find(&keys).Error; err != nil {
			return fmt.Errorf("load export keys for destruction: %w", err)
		}
		if len(keys) != 1 {
			return ErrUnavailable
		}
		key := keys[0]
		if key.State == "destroyed" || key.State == "lost" {
			return validateTerminalTombstone(key)
		}
		if key.State != "active" {
			return ErrUnavailable
		}
		destroyedAt := now
		result := tx.Model(&model.BackupAssetExportKey{}).
			Where("id = ? AND job_id = ? AND state = ? AND key_revision = ?", key.ID, jobID, "active", key.KeyRevision).
			Updates(map[string]any{
				"state": "destroyed", "wrapped_dek": []byte{}, "envelope_nonce": []byte{},
				"destroyed_at": destroyedAt, "key_revision": gorm.Expr("key_revision + 1"),
			})
		if result.Error != nil {
			return fmt.Errorf("destroy export job key: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			var current []model.BackupAssetExportKey
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("job_id = ?", jobID).Limit(2).Find(&current).Error; err != nil {
				return fmt.Errorf("reload export key after lost destruction compare-and-swap: %w", err)
			}
			if len(current) != 1 {
				return ErrUnavailable
			}
			return validateTerminalTombstone(current[0])
		}
		if err := tx.Model(&model.BackupAssetExportItem{}).Where("job_id = ?", jobID).
			Updates(map[string]any{"path_nonce": []byte{}, "path_ciphertext": []byte{}, "updated_at": now}).Error; err != nil {
			return fmt.Errorf("destroy encrypted export selection: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("%w: destroy export job key and selection: %w", ErrUnavailable, err)
	}
	return nil
}

func (port *PersistentLifecyclePort) ReleaseSourcesAndNonStore(ctx context.Context, jobID string) error {
	if port == nil || backupasset.ValidateOpaqueID(jobID) != nil {
		return ErrUnavailable
	}
	now := port.now().UTC()
	if err := port.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var sources []model.BackupAssetExportSourceLease
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("job_id = ?", jobID).
			Order("recovery_point_id ASC, id ASC").Find(&sources).Error; err != nil {
			return fmt.Errorf("load export source leases for release: %w", err)
		}
		if len(sources) == 0 {
			return ErrUnavailable
		}
		for _, source := range sources {
			if source.State == "released" || source.State == "expired" {
				continue
			}
			if source.State != "active" {
				return ErrAttemptFenceLost
			}
			var lease model.RecoveryPointLease
			result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", source.LeaseID).Limit(1).Find(&lease)
			if result.Error != nil {
				return fmt.Errorf("lock Foundation source lease for release: %w", result.Error)
			}
			if result.RowsAffected != 1 || lease.RecoveryPointID != source.RecoveryPointID ||
				lease.HolderType != string(backupasset.LeaseHolderExportJob) || lease.OwnerID != jobID ||
				lease.AbsoluteDeadline.UTC() != source.AbsoluteDeadline.UTC() {
				return ErrAttemptFenceLost
			}
			digest := sha256.Sum256([]byte(lease.FenceToken))
			if lease.AttemptID != source.LeaseAttemptID || hex.EncodeToString(digest[:]) != source.FenceHash {
				return ErrAttemptFenceLost
			}
			targetState := "released"
			if lease.Status == string(backupasset.LeaseExpired) {
				targetState = "expired"
			} else if lease.Status == string(backupasset.LeaseReleased) {
				targetState = "released"
			} else if lease.Status == string(backupasset.LeaseActive) {
				if !now.Before(lease.AbsoluteDeadline.UTC()) {
					result = tx.Model(&model.RecoveryPointLease{}).
						Where(`id = ? AND recovery_point_id = ? AND holder_type = ? AND owner_id = ?
							AND attempt_id = ? AND fence_token = ? AND status = ? AND absolute_deadline = ?`,
							lease.ID, lease.RecoveryPointID, lease.HolderType, lease.OwnerID,
							lease.AttemptID, lease.FenceToken, backupasset.LeaseActive, lease.AbsoluteDeadline).
						Updates(map[string]any{"status": string(backupasset.LeaseExpired), "updated_at": now})
					if result.Error != nil {
						return fmt.Errorf("expire absolute-deadline Foundation source lease: %w", result.Error)
					}
					if result.RowsAffected != 1 {
						return ErrAttemptFenceLost
					}
					targetState = "expired"
				} else {
					fence := backupasset.LeaseFence{
						LeaseID: lease.ID, RecoveryPointID: lease.RecoveryPointID,
						HolderType: backupasset.LeaseHolderExportJob, OwnerID: lease.OwnerID,
						AttemptID: lease.AttemptID, FenceToken: lease.FenceToken,
					}
					if !now.Before(lease.LeaseExpiresAt.UTC()) {
						taken, err := port.sources.TakeoverTx(ctx, tx, backupasset.TakeoverLeaseRequest{LeaseID: lease.ID, OwnerID: jobID})
						if err != nil {
							return fmt.Errorf("take over expired Foundation source lease for release: %w", err)
						}
						if taken.RecoveryPointID != source.RecoveryPointID || taken.OwnerID != jobID ||
							taken.HolderType != backupasset.LeaseHolderExportJob || !taken.AbsoluteDeadline.UTC().Equal(source.AbsoluteDeadline.UTC()) {
							return ErrAttemptFenceLost
						}
						fence = taken.Fence
						takenDigest := sha256.Sum256([]byte(taken.Fence.FenceToken))
						result = tx.Model(&model.BackupAssetExportSourceLease{}).
							Where("id = ? AND job_id = ? AND state = ?", source.ID, jobID, "active").
							Updates(map[string]any{
								"lease_attempt_id": taken.Fence.AttemptID, "fence_hash": hex.EncodeToString(takenDigest[:]),
								"renewed_at": taken.LastHeartbeatAt.UTC(), "updated_at": taken.LastHeartbeatAt.UTC(),
							})
						if result.Error != nil {
							return fmt.Errorf("persist taken-over export source lease for release: %w", result.Error)
						}
						if result.RowsAffected != 1 {
							return ErrAttemptFenceLost
						}
					}
					if err := port.sources.ReleaseTx(ctx, tx, fence); err != nil {
						return fmt.Errorf("release Foundation source lease: %w", err)
					}
				}
			} else {
				return ErrAttemptFenceLost
			}
			releasedAt := now
			result = tx.Model(&model.BackupAssetExportSourceLease{}).
				Where("id = ? AND job_id = ? AND state = ?", source.ID, jobID, "active").
				Updates(map[string]any{"state": targetState, "released_at": releasedAt, "updated_at": now})
			if result.Error != nil {
				return fmt.Errorf("persist released export source lease: %w", result.Error)
			}
			if result.RowsAffected != 1 {
				return ErrAttemptFenceLost
			}
		}
		return nil
	}); err != nil {
		return err
	}
	ids, err := port.reservationIDs(ctx, jobID, "job")
	if err != nil {
		return err
	}
	if err := port.quota.release(ctx, ids); err != nil {
		return fmt.Errorf("release non-store export reservations: %w", err)
	}
	return nil
}

func (port *PersistentLifecyclePort) PurgeCiphertext(ctx context.Context, jobID string) error {
	if port == nil || backupasset.ValidateOpaqueID(jobID) != nil {
		return ErrUnavailable
	}
	now := port.now().UTC()
	locators, err := port.markCiphertextPurging(ctx, jobID, now)
	if err != nil {
		return err
	}
	if err := port.store.PurgeBatch(locators); err != nil {
		if markErr := port.markPurgeFailure(context.WithoutCancel(ctx), jobID, now); markErr != nil {
			return errors.Join(err, fmt.Errorf("mark export purge failure: %w", markErr))
		}
		return err
	}
	if err := port.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		purgedAt := now
		if err := tx.Model(&model.BackupAssetExportArtifact{}).
			Where("job_id = ? AND state IN ?", jobID, []string{"purging", "purge_failed", "revoked", "sealed", "staged"}).
			Updates(map[string]any{"state": "purged", "purged_at": purgedAt, "purge_error": "", "updated_at": now}).Error; err != nil {
			return fmt.Errorf("close purged export artifacts: %w", err)
		}
		if err := tx.Model(&model.BackupAssetExportItemAttempt{}).Where("job_id = ?", jobID).
			Update("spool_locator", "").Error; err != nil {
			return fmt.Errorf("clear purged export spool locators: %w", err)
		}
		if err := tx.Model(&model.BackupAssetExportAttempt{}).Where("job_id = ?", jobID).
			Update("staging_locator", "").Error; err != nil {
			return fmt.Errorf("clear purged export staging locators: %w", err)
		}
		return nil
	}); err != nil {
		return err
	}
	return port.purgeUnreferencedCiphertext(ctx)
}

func (port *PersistentLifecyclePort) markPurgeFailure(ctx context.Context, jobID string, now time.Time) error {
	return port.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var owner struct {
			OwnerUserID uint `gorm:"column:owner_user_id"`
		}
		result := tx.Model(&model.BackupAssetExportJob{}).Select("owner_user_id").
			Where("id = ?", jobID).Limit(1).Scan(&owner)
		if result.Error != nil {
			return fmt.Errorf("discover export job owner for purge failure: %w", result.Error)
		}
		if result.RowsAffected != 1 || owner.OwnerUserID == 0 {
			return ErrUnavailable
		}
		buckets, err := ensureAndLockQuotaBucketPairTx(tx, owner.OwnerUserID, now)
		if err != nil {
			return err
		}
		var job model.BackupAssetExportJob
		result = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND owner_user_id = ?", jobID, owner.OwnerUserID).Limit(1).Find(&job)
		if result.Error != nil {
			return fmt.Errorf("lock export job for purge failure: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return ErrUnavailable
		}
		var artifacts []model.BackupAssetExportArtifact
		result = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("job_id = ? AND state = ?", jobID, "purging").Order("id ASC").Limit(2).Find(&artifacts)
		if result.Error != nil {
			return fmt.Errorf("lock export artifacts for purge failure: %w", result.Error)
		}
		if len(artifacts) > 1 {
			return ErrUnavailable
		}
		if len(artifacts) == 0 {
			return nil
		}
		if err := markStoreReservationsPurgePendingTx(tx, buckets, job, now); err != nil {
			return err
		}
		result = tx.Model(&model.BackupAssetExportArtifact{}).
			Where("id = ? AND job_id = ? AND state = ?", artifacts[0].ID, jobID, "purging").
			Updates(map[string]any{"state": "purge_failed", "purge_error": "purge_failed", "updated_at": now})
		if result.Error != nil {
			return fmt.Errorf("mark export artifact purge failed: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return ErrUnavailable
		}
		return nil
	})
}

func (port *PersistentLifecyclePort) ReleaseStoreBytes(ctx context.Context, jobID string) error {
	if port == nil || backupasset.ValidateOpaqueID(jobID) != nil {
		return ErrUnavailable
	}
	ids, err := port.reservationIDs(ctx, jobID, "store")
	if err != nil {
		return err
	}
	if err := port.purgeUnreferencedCiphertext(ctx); err != nil {
		return err
	}
	return port.quota.release(ctx, ids)
}

func (port *PersistentLifecyclePort) purgeUnreferencedCiphertext(ctx context.Context) error {
	_, err := purgeUnreferencedStore(ctx, port.store, func(loadCtx context.Context) (map[string]struct{}, error) {
		return referencedStoreLocators(loadCtx, port.db)
	})
	return err
}

func (port *PersistentLifecyclePort) reservationIDs(ctx context.Context, jobID, kind string) ([]string, error) {
	var rows []model.BackupAssetExportReservation
	if err := port.db.WithContext(ctx).Where("job_id = ? AND kind = ?", jobID, kind).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("load %s export reservations: %w", kind, err)
	}
	if len(rows) != 2 {
		return nil, ErrUnavailable
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return ids, nil
}

func (port *PersistentLifecyclePort) markCiphertextPurging(
	ctx context.Context, jobID string, now time.Time,
) ([]string, error) {
	locators := make([]string, 0)
	err := port.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var artifacts []model.BackupAssetExportArtifact
		if err := tx.Where("job_id = ?", jobID).Find(&artifacts).Error; err != nil {
			return fmt.Errorf("load export artifacts for purge: %w", err)
		}
		for _, artifact := range artifacts {
			if artifact.State == "purged" {
				continue
			}
			if !validStoreLocator(artifact.Locator) {
				return ErrInvalidStore
			}
			locators = append(locators, artifact.Locator)
		}
		var spoolLocators []string
		if err := tx.Model(&model.BackupAssetExportItemAttempt{}).
			Where("job_id = ? AND spool_locator <> ?", jobID, "").Pluck("spool_locator", &spoolLocators).Error; err != nil {
			return fmt.Errorf("load export spools for purge: %w", err)
		}
		var stagingLocators []string
		if err := tx.Model(&model.BackupAssetExportAttempt{}).
			Where("job_id = ? AND staging_locator <> ?", jobID, "").Pluck("staging_locator", &stagingLocators).Error; err != nil {
			return fmt.Errorf("load export staging objects for purge: %w", err)
		}
		for _, locator := range append(spoolLocators, stagingLocators...) {
			if !validStoreLocator(locator) {
				return ErrInvalidStore
			}
			locators = append(locators, locator)
		}
		if err := tx.Model(&model.BackupAssetExportArtifact{}).
			Where("job_id = ? AND state <> ?", jobID, "purged").
			Updates(map[string]any{"state": "purging", "purge_error": "", "updated_at": now}).Error; err != nil {
			return fmt.Errorf("mark export artifacts purging: %w", err)
		}
		return nil
	})
	return locators, err
}

func persistentLifecycleRevokeReason(state ExecutionState) (string, error) {
	switch state {
	case ExecutionCancelRequested, ExecutionCanceled:
		return "job_canceled", nil
	case ExecutionSourceExpired:
		return "source_expired", nil
	case ExecutionExpiring, ExecutionExpired:
		return "artifact_expired", nil
	case ExecutionFailed:
		return "job_failed", nil
	default:
		return "", ErrInvalidTransition
	}
}

func NewLifecycle(dependencies LifecycleDependencies) (*Lifecycle, error) {
	if dependencies.DB == nil || dependencies.Port == nil {
		return nil, ErrUnavailable
	}
	if dependencies.Now == nil {
		dependencies.Now = func() time.Time { return time.Now().UTC() }
	}
	return &Lifecycle{db: dependencies.DB, port: dependencies.Port, now: dependencies.Now}, nil
}

// MarkKeyVersionLost closes every Export that still depends on one KEK version
// before recording the coordinated keyring loss. Cleanup deliberately runs
// outside the keyring transaction because it revokes and drains live delivery
// state before the callback verifies the durable terminal tombstones.
func (lifecycle *Lifecycle) MarkKeyVersionLost(
	ctx context.Context,
	keyring *backupasset.Keyring,
	version, batchSize int,
) error {
	if lifecycle == nil || lifecycle.db == nil || keyring == nil || version <= 0 || batchSize <= 0 || batchSize > 10000 {
		return ErrUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		var jobIDs []string
		unfinishedJobs := lifecycle.db.WithContext(ctx).Model(&model.BackupAssetExportJob{}).
			Select("id").Where("cleanup_state <> ?", CleanupPurged)
		if err := lifecycle.db.WithContext(ctx).Model(&model.BackupAssetExportKey{}).
			Distinct("job_id").
			Where("kek_version = ? AND (state = ? OR job_id IN (?))", version, "active", unfinishedJobs).
			Order("job_id ASC").Limit(batchSize).Pluck("job_id", &jobIDs).Error; err != nil {
			return fmt.Errorf("load Export jobs for key loss: %w", err)
		}
		if len(jobIDs) == 0 {
			break
		}
		for _, jobID := range jobIDs {
			if err := lifecycle.FailUnpublishable(ctx, jobID, "key_unavailable"); err != nil {
				return fmt.Errorf("revoke Export job for key loss: %w", err)
			}
			var activeKeys int64
			if err := lifecycle.db.WithContext(ctx).Model(&model.BackupAssetExportKey{}).
				Where("job_id = ? AND kek_version = ? AND state = ?", jobID, version, "active").
				Count(&activeKeys).Error; err != nil {
				return fmt.Errorf("verify Export key destruction after key loss: %w", err)
			}
			if activeKeys != 0 {
				return fmt.Errorf("%w: active Export job key remains after cleanup", backupasset.ErrConflict)
			}
		}
	}

	err := keyring.MarkRebuildableLost(
		ctx,
		backupasset.KeyDomainExportStore,
		version,
		func(_ context.Context, tx *gorm.DB, transition backupasset.RebuildableKeyTransition) error {
			if transition.Domain != backupasset.KeyDomainExportStore || transition.PreviousVersion != version || transition.NextVersion != 0 {
				return ErrUnavailable
			}
			var keys []model.BackupAssetExportKey
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("kek_version = ?", version).Order("id ASC").Find(&keys).Error; err != nil {
				return fmt.Errorf("lock Export job keys for key loss: %w", err)
			}
			for _, key := range keys {
				if (key.State != "destroyed" && key.State != "lost") || len(key.WrappedDEK) != 0 ||
					len(key.EnvelopeNonce) != 0 || key.DestroyedAt == nil {
					return fmt.Errorf("%w: Export job key remains readable during key loss", backupasset.ErrConflict)
				}
			}
			result := tx.Model(&model.BackupAssetExportKey{}).
				Where("kek_version = ? AND state = ?", version, "destroyed").
				Update("state", "lost")
			if result.Error != nil {
				return fmt.Errorf("mark Export job keys lost: %w", result.Error)
			}
			return nil
		},
	)
	if err == nil {
		return nil
	}
	var key model.WrappedDomainKey
	result := lifecycle.db.WithContext(ctx).
		Where("domain = ? AND version = ?", backupasset.KeyDomainExportStore, version).
		Limit(1).Find(&key)
	if result.Error == nil && result.RowsAffected == 1 && backupasset.DomainKeyState(key.State) == backupasset.DomainKeyLost {
		return nil
	}
	return err
}

func (lifecycle *Lifecycle) Cleanup(ctx context.Context, jobID string) (CleanupState, error) {
	if lifecycle == nil || backupasset.ValidateOpaqueID(jobID) != nil {
		return CleanupPurgeFailed, ErrUnavailable
	}

	job, err := lifecycle.loadJob(ctx, jobID)
	if err != nil {
		return CleanupPurgeFailed, err
	}
	state := CleanupState(job.CleanupState)
	if state == CleanupPurged {
		return state, nil
	}
	// A released store reservation paired with unpurged ciphertext has no quota
	// authority for another purge attempt. Quarantine it before advancing any
	// cleanup state so reconciliation cannot disguise the conflict as a retry.
	if err := lifecycle.releasedStorePairConflict(ctx, jobID); err != nil {
		return state, err
	}
	if state == CleanupNone {
		if err := lifecycle.transitionCleanup(ctx, &job, CleanupNone, CleanupRevoking); err != nil {
			return CleanupPurgeFailed, err
		}
		state = CleanupRevoking
	}

	if state == CleanupRevoking {
		steps := []struct {
			name string
			run  func(context.Context, string) error
		}{
			{name: "fence attempts", run: lifecycle.port.FenceAttempts},
			{name: "revoke deliveries", run: lifecycle.port.RevokeDeliveries},
			{name: "drain streams", run: lifecycle.port.DrainStreams},
			{name: "destroy job key and selection", run: lifecycle.port.DestroyJobKeyAndSelection},
			{name: "release sources and non-store reservations", run: lifecycle.port.ReleaseSourcesAndNonStore},
		}
		for _, step := range steps {
			if err := step.run(ctx, jobID); err != nil {
				return CleanupRevoking, fmt.Errorf("%s: %w", step.name, err)
			}
		}
		if err := lifecycle.transitionCleanup(ctx, &job, CleanupRevoking, CleanupPurging); err != nil {
			return CleanupRevoking, err
		}
		state = CleanupPurging
	}

	if state == CleanupPurgeFailed {
		if err := lifecycle.transitionCleanup(ctx, &job, CleanupPurgeFailed, CleanupPurging); err != nil {
			return CleanupPurgeFailed, err
		}
		state = CleanupPurging
	}
	if state != CleanupPurging {
		return CleanupPurgeFailed, ErrInvalidTransition
	}
	if err := lifecycle.releasedStorePairConflict(ctx, jobID); err != nil {
		return CleanupPurging, err
	}
	if err := lifecycle.port.PurgeCiphertext(ctx, jobID); err != nil {
		return lifecycle.recordPurgeFailure(ctx, &job, fmt.Errorf("purge ciphertext: %w", err))
	}
	if err := lifecycle.port.ReleaseStoreBytes(ctx, jobID); err != nil {
		return lifecycle.recordPurgeFailure(ctx, &job, fmt.Errorf("release store bytes: %w", err))
	}
	if err := lifecycle.transitionCleanup(ctx, &job, CleanupPurging, CleanupPurged); err != nil {
		return CleanupPurging, err
	}
	return CleanupPurged, nil
}

func (lifecycle *Lifecycle) Reconcile(ctx context.Context, limit int) (int, error) {
	if lifecycle == nil || limit <= 0 || limit > maxLifecycleReconcileLimit {
		return 0, ErrUnavailable
	}
	lifecycle.reconcileMu.Lock()
	defer lifecycle.reconcileMu.Unlock()

	now := lifecycle.now().UTC()
	sweep, acquired, err := lifecycle.acquireLifecycleSweep(ctx, now)
	if errors.Is(err, errLifecycleSweepContended) {
		return 0, nil
	}
	if err != nil || !acquired {
		return 0, err
	}
	scanBudget := limit * lifecycleReconcileScanScale
	if scanBudget > maxLifecycleReconcileLimit {
		scanBudget = maxLifecycleReconcileLimit
	}
	candidates, err := lifecycle.loadReconcileCandidates(ctx, now, scanBudget, sweep.cursor, sweep.highWater)
	if err != nil {
		releaseErr := lifecycle.persistLifecycleSweepProgress(ctx, &sweep, sweep.cursor, true)
		return 0, errors.Join(err, releaseErr)
	}

	processed := 0
	attempted := 0
	var reconcileErr error
	for _, candidate := range candidates {
		if processed == limit {
			break
		}
		succeeded, candidateErr := lifecycle.reconcileCandidate(ctx, candidate)
		reconcileErr = errors.Join(reconcileErr, candidateErr)
		if succeeded {
			processed++
		}
		attempted++
		if err := lifecycle.persistLifecycleSweepProgress(
			ctx, &sweep, candidate.LifecycleEnqueueSequence, false,
		); err != nil {
			return processed, errors.Join(reconcileErr, err)
		}
	}
	completedSweep := attempted == len(candidates) && len(candidates) < scanBudget
	releaseCursor := sweep.cursor
	if completedSweep {
		releaseCursor = sweep.highWater
	}
	releaseErr := lifecycle.persistLifecycleSweepProgress(ctx, &sweep, releaseCursor, true)
	return processed, errors.Join(reconcileErr, releaseErr)
}

func (lifecycle *Lifecycle) loadReconcileCandidates(
	ctx context.Context,
	now time.Time,
	limit int,
	cursor int64,
	highWater int64,
) ([]model.BackupAssetExportJob, error) {
	if lifecycle == nil || lifecycle.db == nil || limit <= 0 || cursor < 0 || highWater <= cursor {
		return nil, ErrUnavailable
	}
	var jobs []model.BackupAssetExportJob
	query := lifecycle.db.WithContext(ctx).
		Where("lifecycle_enqueue_sequence > ? AND lifecycle_enqueue_sequence <= ?", cursor, highWater).
		Where(`(
			cleanup_state IN ?
			OR execution_state IN ?
			OR (execution_state = ? AND expires_at IS NOT NULL AND expires_at <= ?)
		) AND NOT (cleanup_state = ? AND execution_state IN ?)`,
			[]string{string(CleanupRevoking), string(CleanupPurging), string(CleanupPurgeFailed)},
			[]string{string(ExecutionFailed), string(ExecutionSourceExpired), string(ExecutionCancelRequested), string(ExecutionCanceled), string(ExecutionExpiring)},
			ExecutionReady, now, CleanupPurged,
			[]string{string(ExecutionFailed), string(ExecutionSourceExpired), string(ExecutionCanceled)})
	if err := query.Order("lifecycle_enqueue_sequence ASC").Limit(limit).Find(&jobs).Error; err != nil {
		return nil, fmt.Errorf("load export lifecycle work: %w", err)
	}
	previous := cursor
	for _, job := range jobs {
		if job.LifecycleEnqueueSequence <= previous || job.LifecycleEnqueueSequence > highWater {
			return nil, ErrUnavailable
		}
		previous = job.LifecycleEnqueueSequence
	}
	return jobs, nil
}

func (lifecycle *Lifecycle) acquireLifecycleSweep(
	ctx context.Context,
	now time.Time,
) (lifecycleSweepLease, bool, error) {
	var sweep lifecycleSweepLease
	if lifecycle == nil || lifecycle.db == nil || now.IsZero() || now.Location() != time.UTC {
		return sweep, false, ErrUnavailable
	}
	acquired := false
	err := lifecycle.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var bucket model.BackupAssetExportQuotaBucket
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("scope = ? AND subject = ?", "global", "global").Limit(1).Find(&bucket)
		if result.Error != nil {
			return fmt.Errorf("lock export lifecycle sweep: %w", result.Error)
		}
		if result.RowsAffected == 0 {
			var orphan model.BackupAssetExportJob
			jobResult := tx.Select("id").Limit(1).Find(&orphan)
			if jobResult.Error != nil {
				return fmt.Errorf("check export jobs without lifecycle latch: %w", jobResult.Error)
			}
			if jobResult.RowsAffected != 0 {
				return ErrUnavailable
			}
			return nil
		}
		if bucket.Scope != "global" || bucket.Subject != "global" ||
			backupasset.ValidateOpaqueID(bucket.ID) != nil || bucket.LifecycleNextSequence <= 0 ||
			bucket.LifecycleSweepCursor < 0 || bucket.LifecycleSweepHighWater < 0 ||
			bucket.LifecycleSweepCursor > bucket.LifecycleSweepHighWater ||
			bucket.LifecycleSweepHighWater >= bucket.LifecycleNextSequence ||
			bucket.LifecycleSweepRevision < 0 || bucket.LifecycleSweepRevision == int64(^uint64(0)>>1) {
			return ErrUnavailable
		}
		if bucket.LifecycleSweepLeaseExpiresAt != nil && bucket.LifecycleSweepLeaseExpiresAt.UTC().After(now) {
			return errLifecycleSweepContended
		}
		cursor := bucket.LifecycleSweepCursor
		highWater := bucket.LifecycleSweepHighWater
		if cursor == highWater {
			cursor = 0
			highWater = bucket.LifecycleNextSequence - 1
		}
		if highWater == 0 {
			return nil
		}
		revision := bucket.LifecycleSweepRevision + 1
		leaseExpiresAt := now.Add(lifecycleSweepLeaseTTL)
		result = tx.Model(&model.BackupAssetExportQuotaBucket{}).
			Where("id = ? AND lifecycle_sweep_revision = ?", bucket.ID, bucket.LifecycleSweepRevision).
			UpdateColumns(map[string]any{
				"lifecycle_sweep_cursor":           cursor,
				"lifecycle_sweep_high_water":       highWater,
				"lifecycle_sweep_revision":         revision,
				"lifecycle_sweep_lease_expires_at": leaseExpiresAt,
			})
		if result.Error != nil {
			return fmt.Errorf("acquire export lifecycle sweep: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return ErrUnavailable
		}
		sweep = lifecycleSweepLease{
			bucketID: bucket.ID, revision: revision, cursor: cursor, highWater: highWater,
		}
		acquired = true
		return nil
	})
	return sweep, acquired, err
}

func (lifecycle *Lifecycle) persistLifecycleSweepProgress(
	ctx context.Context,
	sweep *lifecycleSweepLease,
	cursor int64,
	release bool,
) error {
	if lifecycle == nil || lifecycle.db == nil || sweep == nil ||
		backupasset.ValidateOpaqueID(sweep.bucketID) != nil || sweep.revision <= 0 ||
		cursor < sweep.cursor || cursor > sweep.highWater {
		return ErrUnavailable
	}
	updates := map[string]any{"lifecycle_sweep_cursor": cursor}
	if release {
		updates["lifecycle_sweep_lease_expires_at"] = nil
	} else {
		updates["lifecycle_sweep_lease_expires_at"] = lifecycle.now().UTC().Add(lifecycleSweepLeaseTTL)
	}
	result := lifecycle.db.WithContext(ctx).Model(&model.BackupAssetExportQuotaBucket{}).
		Where(`id = ? AND scope = ? AND subject = ? AND lifecycle_sweep_revision = ?
			AND lifecycle_sweep_cursor = ? AND lifecycle_sweep_high_water = ?
			AND lifecycle_sweep_lease_expires_at IS NOT NULL`,
			sweep.bucketID, "global", "global", sweep.revision, sweep.cursor, sweep.highWater).
		UpdateColumns(updates)
	if result.Error != nil {
		return fmt.Errorf("persist export lifecycle sweep progress: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return ErrUnavailable
	}
	sweep.cursor = cursor
	return nil
}

func (lifecycle *Lifecycle) reconcileCandidate(
	ctx context.Context,
	job model.BackupAssetExportJob,
) (bool, error) {
	if ExecutionState(job.ExecutionState) == ExecutionReady {
		if err := lifecycle.transitionExecution(ctx, &job, ExecutionReady, ExecutionExpiring); err != nil {
			return false, err
		}
	}
	cleanupState, cleanupErr := lifecycle.Cleanup(ctx, job.ID)
	if errors.Is(cleanupErr, errReleasedStorePairConflict) {
		return false, cleanupErr
	}
	finalizeErr := lifecycle.finalizeExecutionAfterCleanup(ctx, job.ID, cleanupState)
	if cleanupErr != nil || finalizeErr != nil {
		return false, errors.Join(cleanupErr, finalizeErr)
	}
	return true, nil
}

var errReleasedStorePairConflict = fmt.Errorf("%w: released store reservations retain an unpurged artifact", ErrUnavailable)

func (lifecycle *Lifecycle) releasedStorePairConflict(ctx context.Context, jobID string) error {
	if lifecycle == nil || lifecycle.db == nil || backupasset.ValidateOpaqueID(jobID) != nil {
		return ErrUnavailable
	}
	return lifecycle.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var artifacts []model.BackupAssetExportArtifact
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("job_id = ? AND state <> ?", jobID, "purged").Order("id ASC").Limit(2).Find(&artifacts)
		if result.Error != nil {
			return fmt.Errorf("lock unpurged export artifacts for cleanup: %w", result.Error)
		}
		if len(artifacts) > 1 {
			return ErrUnavailable
		}
		if len(artifacts) == 0 {
			return nil
		}
		var rows []model.BackupAssetExportReservation
		result = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("job_id = ? AND kind = ?", jobID, "store").Order("bucket_id ASC, id ASC").Limit(3).Find(&rows)
		if result.Error != nil {
			return fmt.Errorf("lock export store reservations for cleanup: %w", result.Error)
		}
		if len(rows) != 2 {
			return ErrUnavailable
		}
		if rows[0].State == "released" && rows[1].State == "released" &&
			rows[0].ReleasedAt != nil && rows[1].ReleasedAt != nil {
			return errReleasedStorePairConflict
		}
		return nil
	})
}

// TerminalizeForRuntimeStop fences every durable job that remains eligible for
// cleanup after export admission has stopped. It records cancellation before
// starting the existing revoke-first cleanup machine so a late worker cannot
// publish or retain a newly terminal artifact.
func (lifecycle *Lifecycle) TerminalizeForRuntimeStop(ctx context.Context, limit int) (int, error) {
	progress, err := lifecycle.TerminalizeForRuntimeStopPass(ctx, limit)
	return progress.Processed, err
}

// TerminalizeForRuntimeStopPass consumes one finite, sequence-ordered window
// of durable jobs. It deliberately shares the lifecycle sweep cursor with
// ordinary reconciliation, rather than rotating failed rows by timestamp or
// revision. Each candidate's cursor is persisted after it is attempted so a
// fresh Lifecycle instance can move past an unchanged cleanup failure.
func (lifecycle *Lifecycle) TerminalizeForRuntimeStopPass(
	ctx context.Context,
	limit int,
) (RuntimeStopTerminalizationProgress, error) {
	var progress RuntimeStopTerminalizationProgress
	if lifecycle == nil || limit <= 0 || limit > maxLifecycleReconcileLimit {
		return progress, ErrUnavailable
	}
	lifecycle.reconcileMu.Lock()
	defer lifecycle.reconcileMu.Unlock()

	now := lifecycle.now().UTC()
	sweep, acquired, err := lifecycle.acquireLifecycleSweep(ctx, now)
	if err != nil {
		return progress, err
	}
	if !acquired {
		progress.Complete = true
		return progress, nil
	}
	priorTerminalCleanup, err := lifecycle.restartRuntimeStopSweepForEarlierCandidates(ctx, &sweep)
	if err != nil {
		releaseErr := lifecycle.persistLifecycleSweepProgress(ctx, &sweep, sweep.cursor, true)
		return progress, errors.Join(err, releaseErr)
	}

	scanBudget := limit * lifecycleReconcileScanScale
	if scanBudget > maxLifecycleReconcileLimit {
		scanBudget = maxLifecycleReconcileLimit
	}
	candidates, err := lifecycle.loadRuntimeStopCandidates(ctx, scanBudget, sweep.cursor, sweep.highWater)
	if err != nil {
		releaseErr := lifecycle.persistLifecycleSweepProgress(ctx, &sweep, sweep.cursor, true)
		return progress, errors.Join(err, releaseErr)
	}

	attempted := 0
	var terminalizeErr error
	if priorTerminalCleanup {
		terminalizeErr = errRuntimeStopEarlierTerminalCleanup
	}
	for _, candidate := range candidates {
		if progress.Processed == limit {
			break
		}
		if err := lifecycle.requestRuntimeStop(ctx, candidate.ID); err != nil {
			terminalizeErr = errors.Join(terminalizeErr, fmt.Errorf("request export runtime stop: %w", err))
		} else {
			cleanupState, cleanupErr := lifecycle.Cleanup(ctx, candidate.ID)
			finalizeErr := lifecycle.finalizeExecutionAfterCleanup(ctx, candidate.ID, cleanupState)
			if cleanupErr != nil || finalizeErr != nil {
				terminalizeErr = errors.Join(terminalizeErr, cleanupErr, finalizeErr)
			} else {
				progress.Processed++
			}
		}
		attempted++
		if err := lifecycle.persistLifecycleSweepProgress(
			ctx, &sweep, candidate.LifecycleEnqueueSequence, false,
		); err != nil {
			return progress, errors.Join(terminalizeErr, err)
		}
		progress.Advanced++
	}

	completedSweep := attempted == len(candidates) && (len(candidates) < scanBudget ||
		(len(candidates) > 0 && candidates[len(candidates)-1].LifecycleEnqueueSequence == sweep.highWater))
	releaseCursor := sweep.cursor
	if completedSweep {
		releaseCursor = sweep.highWater
	}
	if err := lifecycle.persistLifecycleSweepProgress(ctx, &sweep, releaseCursor, true); err != nil {
		return progress, errors.Join(terminalizeErr, err)
	}
	progress.Complete = completedSweep
	return progress, terminalizeErr
}

// restartRuntimeStopSweepForEarlierCandidates protects jobs that ordinary
// reconciliation skipped before its current cursor because they were not yet
// cleanup-eligible, including purged rows that crashed before execution-state
// finalization. It reports terminal cleanup work already attempted by ordinary
// reconciliation as a blocker instead of rewinding it, so a persistent failure
// cannot restart the same bounded runtime-stop window forever.
func (lifecycle *Lifecycle) restartRuntimeStopSweepForEarlierCandidates(
	ctx context.Context,
	sweep *lifecycleSweepLease,
) (bool, error) {
	if lifecycle == nil || lifecycle.db == nil || sweep == nil ||
		backupasset.ValidateOpaqueID(sweep.bucketID) != nil || sweep.revision <= 0 ||
		sweep.cursor <= 0 || sweep.highWater <= sweep.cursor {
		return false, nil
	}
	priorTerminalCleanup := false
	err := lifecycle.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var bucket model.BackupAssetExportQuotaBucket
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where(`id = ? AND scope = ? AND subject = ? AND lifecycle_sweep_revision = ?
				AND lifecycle_sweep_cursor = ? AND lifecycle_sweep_high_water = ?
				AND lifecycle_sweep_lease_expires_at IS NOT NULL`,
				sweep.bucketID, "global", "global", sweep.revision, sweep.cursor, sweep.highWater).
			Limit(1).Find(&bucket)
		if result.Error != nil {
			return fmt.Errorf("lock export lifecycle sweep for runtime stop: %w", result.Error)
		}
		if result.RowsAffected != 1 || bucket.LifecycleNextSequence <= 0 {
			return ErrUnavailable
		}
		var earlierCandidate model.BackupAssetExportJob
		result = tx.Select("id").
			Where("lifecycle_enqueue_sequence <= ?", sweep.cursor).
			Where(`(
					cleanup_state <> ? AND execution_state IN ?
				) OR (
					cleanup_state = ? AND execution_state IN ?
				)`,
				CleanupPurged, runtimeStopActiveExecutionStates,
				CleanupPurged, runtimeStopPurgedTransitionExecutionStates,
			).
			Limit(1).Find(&earlierCandidate)
		if result.Error != nil {
			return fmt.Errorf("find earlier export candidate for runtime stop: %w", result.Error)
		}
		if result.RowsAffected != 0 {
			highWater := bucket.LifecycleNextSequence - 1
			result = tx.Model(&model.BackupAssetExportQuotaBucket{}).
				Where(`id = ? AND scope = ? AND subject = ? AND lifecycle_sweep_revision = ?
					AND lifecycle_sweep_cursor = ? AND lifecycle_sweep_high_water = ?
					AND lifecycle_sweep_lease_expires_at IS NOT NULL`,
					bucket.ID, "global", "global", sweep.revision, sweep.cursor, sweep.highWater).
				UpdateColumns(map[string]any{
					"lifecycle_sweep_cursor":           0,
					"lifecycle_sweep_high_water":       highWater,
					"lifecycle_sweep_lease_expires_at": lifecycle.now().UTC().Add(lifecycleSweepLeaseTTL),
				})
			if result.Error != nil {
				return fmt.Errorf("restart export lifecycle sweep for runtime stop: %w", result.Error)
			}
			if result.RowsAffected != 1 {
				return ErrUnavailable
			}
			sweep.cursor = 0
			sweep.highWater = highWater
			return nil
		}

		var earlierTerminal model.BackupAssetExportJob
		result = tx.Select("id").
			Where("lifecycle_enqueue_sequence <= ?", sweep.cursor).
			Where("cleanup_state <> ? AND execution_state IN ?", CleanupPurged, runtimeStopTerminalExecutionStates).
			Limit(1).Find(&earlierTerminal)
		if result.Error != nil {
			return fmt.Errorf("find earlier terminal export cleanup for runtime stop: %w", result.Error)
		}
		priorTerminalCleanup = result.RowsAffected != 0
		return nil
	})
	return priorTerminalCleanup, err
}

func (lifecycle *Lifecycle) loadRuntimeStopCandidates(
	ctx context.Context,
	limit int,
	cursor int64,
	highWater int64,
) ([]model.BackupAssetExportJob, error) {
	if lifecycle == nil || lifecycle.db == nil || limit <= 0 || cursor < 0 || highWater <= cursor {
		return nil, ErrUnavailable
	}
	var jobs []model.BackupAssetExportJob
	if err := lifecycle.db.WithContext(ctx).
		Where("lifecycle_enqueue_sequence > ? AND lifecycle_enqueue_sequence <= ?", cursor, highWater).
		Where(`cleanup_state <> ? OR (
			cleanup_state = ? AND execution_state IN ?
		)`, CleanupPurged, CleanupPurged, runtimeStopPurgedTransitionExecutionStates).
		Order("lifecycle_enqueue_sequence ASC").Limit(limit).Find(&jobs).Error; err != nil {
		return nil, fmt.Errorf("load export jobs for runtime stop: %w", err)
	}
	previous := cursor
	for _, job := range jobs {
		if job.LifecycleEnqueueSequence <= previous || job.LifecycleEnqueueSequence > highWater {
			return nil, ErrUnavailable
		}
		previous = job.LifecycleEnqueueSequence
	}
	return jobs, nil
}

func (lifecycle *Lifecycle) requestRuntimeStop(ctx context.Context, jobID string) error {
	if lifecycle == nil || backupasset.ValidateOpaqueID(jobID) != nil {
		return ErrUnavailable
	}
	now := lifecycle.now().UTC()
	return lifecycle.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job model.BackupAssetExportJob
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", jobID).Limit(1).Find(&job)
		if result.Error != nil {
			return fmt.Errorf("lock export job for runtime stop: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return ErrNotFound
		}
		state := ExecutionState(job.ExecutionState)
		if !validExecutionStates[state] {
			return ErrUnavailable
		}
		switch state {
		case ExecutionQueued, ExecutionRunning, ExecutionRetryWait, ExecutionSealing, ExecutionReady:
			if ValidateExecutionTransition(state, ExecutionCancelRequested) != nil {
				return ErrInvalidTransition
			}
			result = tx.Model(&model.BackupAssetExportJob{}).
				Where("id = ? AND execution_state = ? AND transition_revision = ?", job.ID, state, job.TransitionRevision).
				Updates(map[string]any{
					"execution_state":     string(ExecutionCancelRequested),
					"transition_revision": gorm.Expr("transition_revision + 1"),
					"updated_at":          now,
				})
			if result.Error != nil {
				return fmt.Errorf("persist export runtime-stop cancellation: %w", result.Error)
			}
			if result.RowsAffected != 1 {
				return ErrInvalidTransition
			}
		case ExecutionCancelRequested, ExecutionFailed, ExecutionSourceExpired, ExecutionCanceled, ExecutionExpiring, ExecutionExpired:
			return nil
		default:
			return ErrUnavailable
		}
		return nil
	})
}

func (lifecycle *Lifecycle) FailUnpublishable(ctx context.Context, jobID, category string) error {
	if lifecycle == nil || backupasset.ValidateOpaqueID(jobID) != nil || !validLifecycleFailureCategory(category) {
		return ErrUnavailable
	}
	now := lifecycle.now().UTC()
	err := lifecycle.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job model.BackupAssetExportJob
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", jobID).Limit(1).Find(&job)
		if result.Error != nil {
			return fmt.Errorf("load unpublishable export job: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return ErrNotFound
		}
		state := ExecutionState(job.ExecutionState)
		switch state {
		case ExecutionCancelRequested, ExecutionFailed, ExecutionSourceExpired,
			ExecutionCanceled, ExecutionExpiring, ExecutionExpired:
			return nil
		}
		next := ExecutionFailed
		if state == ExecutionReady {
			next = ExecutionExpiring
		}
		if ValidateExecutionTransition(state, next) != nil {
			return ErrInvalidTransition
		}
		result = tx.Model(&model.BackupAssetExportJob{}).
			Where("id = ? AND execution_state = ? AND transition_revision = ?", job.ID, state, job.TransitionRevision).
			Updates(map[string]any{
				"execution_state": string(next), "error_category": category,
				"transition_revision": gorm.Expr("transition_revision + 1"), "updated_at": now,
			})
		if result.Error != nil {
			return fmt.Errorf("persist unpublishable export failure: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return ErrInvalidTransition
		}
		return nil
	})
	if err != nil {
		return err
	}
	cleanupState, cleanupErr := lifecycle.Cleanup(ctx, jobID)
	finalizeErr := lifecycle.finalizeExecutionAfterCleanup(ctx, jobID, cleanupState)
	if cleanupErr != nil || finalizeErr != nil {
		return errors.Join(cleanupErr, finalizeErr)
	}
	return nil
}

func (lifecycle *Lifecycle) FailSourceExpired(ctx context.Context, jobID string) error {
	if lifecycle == nil || backupasset.ValidateOpaqueID(jobID) != nil {
		return ErrUnavailable
	}
	now := lifecycle.now().UTC()
	cancelRequested := false
	err := lifecycle.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job model.BackupAssetExportJob
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", jobID).Limit(1).Find(&job)
		if result.Error != nil {
			return fmt.Errorf("load source-expired Export job: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return ErrNotFound
		}
		state := ExecutionState(job.ExecutionState)
		if state == ExecutionCancelRequested {
			cancelRequested = true
			return nil
		}
		if (state == ExecutionSourceExpired || state == ExecutionExpired) && job.ErrorCategory == "source_expired" {
			return nil
		}
		next := ExecutionSourceExpired
		if state == ExecutionReady {
			next = ExecutionExpiring
		}
		if ValidateExecutionTransition(state, next) != nil {
			return ErrInvalidTransition
		}
		result = tx.Model(&model.BackupAssetExportJob{}).
			Where("id = ? AND execution_state = ? AND transition_revision = ?", job.ID, state, job.TransitionRevision).
			Updates(map[string]any{
				"execution_state": string(next), "error_category": "source_expired",
				"transition_revision": gorm.Expr("transition_revision + 1"), "updated_at": now,
			})
		if result.Error != nil || result.RowsAffected != 1 {
			return ErrInvalidTransition
		}
		return nil
	})
	if err != nil {
		return err
	}
	if cancelRequested {
		if _, err := lifecycle.Cleanup(ctx, jobID); err != nil {
			return err
		}
		job, err := lifecycle.loadJob(ctx, jobID)
		if err != nil {
			return err
		}
		if ExecutionState(job.ExecutionState) != ExecutionCancelRequested {
			return nil
		}
		return lifecycle.transitionExecution(ctx, &job, ExecutionCancelRequested, ExecutionCanceled)
	}
	if _, err := lifecycle.Cleanup(ctx, jobID); err != nil {
		return err
	}
	job, err := lifecycle.loadJob(ctx, jobID)
	if err != nil {
		return err
	}
	if ExecutionState(job.ExecutionState) == ExecutionExpiring {
		return lifecycle.transitionExecution(ctx, &job, ExecutionExpiring, ExecutionExpired)
	}
	return nil
}

func validLifecycleFailureCategory(category string) bool {
	switch category {
	case "artifact_missing", "artifact_tampered", "key_unavailable", "deadline", "internal_failure":
		return true
	default:
		return false
	}
}

func (lifecycle *Lifecycle) loadJob(ctx context.Context, jobID string) (model.BackupAssetExportJob, error) {
	var job model.BackupAssetExportJob
	result := lifecycle.db.WithContext(ctx).Where("id = ?", jobID).Limit(1).Find(&job)
	if result.Error != nil {
		return job, fmt.Errorf("load export lifecycle job: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return job, ErrNotFound
	}
	return job, nil
}

func (lifecycle *Lifecycle) finalizeExecutionAfterCleanup(
	ctx context.Context, jobID string, cleanupState CleanupState,
) error {
	if cleanupState != CleanupPurged && cleanupState != CleanupPurgeFailed {
		return nil
	}
	job, err := lifecycle.loadJob(ctx, jobID)
	if err != nil {
		return err
	}
	from := ExecutionState(job.ExecutionState)
	var to ExecutionState
	switch from {
	case ExecutionCancelRequested:
		to = ExecutionCanceled
	case ExecutionExpiring:
		to = ExecutionExpired
	default:
		return nil
	}
	return lifecycle.transitionExecution(ctx, &job, from, to)
}

func (lifecycle *Lifecycle) transitionCleanup(
	ctx context.Context,
	job *model.BackupAssetExportJob,
	from, to CleanupState,
) error {
	if job == nil || ValidateCleanupTransition(from, to) != nil {
		return ErrInvalidTransition
	}
	now := lifecycle.now().UTC()
	result := lifecycle.db.WithContext(ctx).Model(&model.BackupAssetExportJob{}).
		Where("id = ? AND cleanup_state = ? AND transition_revision = ?", job.ID, from, job.TransitionRevision).
		Updates(map[string]any{
			"cleanup_state": to, "transition_revision": gorm.Expr("transition_revision + 1"), "updated_at": now,
		})
	if result.Error != nil {
		return fmt.Errorf("persist export cleanup transition: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return ErrInvalidTransition
	}
	job.CleanupState = string(to)
	job.TransitionRevision++
	job.UpdatedAt = now
	return nil
}

func (lifecycle *Lifecycle) transitionExecution(
	ctx context.Context,
	job *model.BackupAssetExportJob,
	from, to ExecutionState,
) error {
	if job == nil || ValidateExecutionTransition(from, to) != nil {
		return ErrInvalidTransition
	}
	now := lifecycle.now().UTC()
	result := lifecycle.db.WithContext(ctx).Model(&model.BackupAssetExportJob{}).
		Where("id = ? AND execution_state = ? AND transition_revision = ?", job.ID, from, job.TransitionRevision).
		Updates(map[string]any{
			"execution_state": to, "transition_revision": gorm.Expr("transition_revision + 1"), "updated_at": now,
		})
	if result.Error != nil {
		return fmt.Errorf("persist export execution transition: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return ErrInvalidTransition
	}
	job.ExecutionState = string(to)
	job.TransitionRevision++
	job.UpdatedAt = now
	return nil
}

func (lifecycle *Lifecycle) recordPurgeFailure(
	ctx context.Context,
	job *model.BackupAssetExportJob,
	cause error,
) (CleanupState, error) {
	if err := lifecycle.transitionCleanup(ctx, job, CleanupPurging, CleanupPurgeFailed); err != nil {
		return CleanupPurging, errors.Join(cause, err)
	}
	return CleanupPurgeFailed, cause
}
