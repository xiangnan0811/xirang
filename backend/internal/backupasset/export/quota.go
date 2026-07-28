package export

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/content"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type QuotaLimits struct {
	GlobalActiveJobs int64
	UserActiveJobs   int64
	GlobalStoreBytes int64
	UserStoreBytes   int64
}

type WorkerCapacityLimits struct {
	WorkerConcurrency int64
	UserActiveJobs    int64
}

func validWorkerCapacityLimits(limits WorkerCapacityLimits) bool {
	return limits.WorkerConcurrency > 0 && limits.UserActiveJobs > 0
}

func (limits WorkerCapacityLimits) userWorkerLimit() int64 {
	return min(limits.UserActiveJobs, limits.WorkerConcurrency)
}

type QuotaJobRequest struct {
	UserID     uint
	JobID      string
	StoreBytes int64
	ExpiresAt  time.Time
}

type QuotaReservation struct {
	GlobalJobID   string
	UserJobID     string
	GlobalStoreID string
	UserStoreID   string
}

type QuotaService struct {
	db     *gorm.DB
	now    func() time.Time
	limits QuotaLimits
}

type AttemptBudgetService struct {
	db  *gorm.DB
	now func() time.Time

	readerSweepMu sync.Mutex
}

type quotaBucketPair struct {
	Global model.BackupAssetExportQuotaBucket
	User   model.BackupAssetExportQuotaBucket
}

type quotaBucketPairInsertions struct {
	Global bool
	User   bool
}

type attemptBudgetIdentity struct {
	ItemAttemptID string
	ItemID        string
	AttemptID     string
	JobID         string
	OwnerUserID   uint
}

type attemptBudgetFinalizationHint struct {
	identity    attemptBudgetIdentity
	leaseOwner  string
	ownerUserID uint
}

const (
	maxAttemptReadReconcileLimit = 1000
	readerSweepLeaseTTL          = 30 * time.Second
)

type attemptReadSweepLease struct {
	bucketID  string
	revision  int64
	cursor    int64
	highWater int64
}

func NewAttemptBudgetService(db *gorm.DB, now func() time.Time) (*AttemptBudgetService, error) {
	if db == nil {
		return nil, ErrUnavailable
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &AttemptBudgetService{db: db, now: now}, nil
}

func (service *AttemptBudgetService) ReserveAttemptRead(
	ctx context.Context,
	intent content.AttemptReadIntent,
) (content.AttemptReadReservation, error) {
	if service == nil || backupasset.ValidateOpaqueID(intent.SessionID) != nil || !validAttemptReadIntent(intent) {
		return content.AttemptReadReservation{}, content.ErrAttemptBudgetExceeded
	}
	var reservation content.AttemptReadReservation
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		identity, err := discoverAttemptBudgetIdentityTx(tx, intent.SessionID)
		if err != nil {
			return err
		}
		bucketPair, insertions, err := ensureAndLockQuotaBucketPairForAdmissionTx(tx, identity.OwnerUserID)
		if err != nil {
			if errors.Is(err, ErrUnavailable) {
				return content.ErrAttemptBudgetExceeded
			}
			return err
		}
		if !validReaderQuotaBucketPair(bucketPair) {
			return content.ErrAttemptBudgetExceeded
		}
		itemAttempt, item, attempt, job, err := loadAttemptBudgetTupleTx(tx, identity)
		if err != nil {
			return err
		}
		if job.OwnerUserID != identity.OwnerUserID {
			return content.ErrAttemptBudgetExceeded
		}
		if intent.Mode == content.SourceModeSequential && intent.Bytes != item.LogicalSize {
			return content.ErrAttemptBudgetExceeded
		}
		prefix := itemAttempt.ID + ":%"
		var requestCount int64
		if err := tx.Model(&model.BackupAssetExportReservation{}).
			Where("kind = ? AND lease_owner LIKE ?", "reader", prefix).
			Distinct("lease_owner").Count(&requestCount).Error; err != nil {
			return fmt.Errorf("count export attempt read requests: %w", err)
		}
		if requestCount >= 3 {
			return content.ErrAttemptBudgetExceeded
		}
		var activeCount int64
		if err := tx.Model(&model.BackupAssetExportReservation{}).
			Where("kind = ? AND state = ? AND lease_owner LIKE ?", "reader", "active", prefix).
			Distinct("lease_owner").Count(&activeCount).Error; err != nil {
			return fmt.Errorf("count active export attempt reads: %w", err)
		}
		if activeCount != 0 {
			return content.ErrAttemptBudgetExceeded
		}

		buckets := []model.BackupAssetExportQuotaBucket{bucketPair.Global, bucketPair.User}
		var jobActiveReaders int64
		if err := tx.Model(&model.BackupAssetExportReservation{}).
			Where("bucket_id = ? AND job_id = ? AND kind = ? AND state = ?", buckets[0].ID, job.ID, "reader", "active").
			Count(&jobActiveReaders).Error; err != nil {
			return fmt.Errorf("count job export readers: %w", err)
		}
		if jobActiveReaders >= int64(job.MaxOpenReaders) {
			return content.ErrAttemptBudgetExceeded
		}
		var observedProvider int64
		if err := tx.Model(&model.BackupAssetExportItemAttempt{}).
			Where("attempt_id = ?", attempt.ID).Select("COALESCE(SUM(provider_bytes), 0)").Scan(&observedProvider).Error; err != nil {
			return fmt.Errorf("sum export attempt provider bytes: %w", err)
		}
		var activeProvider int64
		if err := tx.Model(&model.BackupAssetExportReservation{}).
			Where("bucket_id = ? AND attempt_id = ? AND kind = ? AND state = ?", buckets[0].ID, attempt.ID, "reader", "active").
			Select("COALESCE(SUM(reserved_provider_bytes), 0)").Scan(&activeProvider).Error; err != nil {
			return fmt.Errorf("sum active export provider reservations: %w", err)
		}
		if intent.Bytes < 0 || observedProvider > job.MaxProviderBytes-activeProvider ||
			intent.Bytes > job.MaxProviderBytes-observedProvider-activeProvider {
			return content.ErrAttemptBudgetExceeded
		}
		now := service.now().UTC()
		if !now.Before(job.AbsoluteDeadline.UTC()) || !now.Before(attempt.LeaseExpiresAt.UTC()) {
			return content.ErrAttemptBudgetExceeded
		}
		if err := stampNewQuotaBucketPairTx(tx, bucketPair, insertions, now); err != nil {
			if errors.Is(err, ErrUnavailable) {
				return content.ErrAttemptBudgetExceeded
			}
			return err
		}
		readerSequence, err := allocateReaderEnqueueSequenceTx(tx, bucketPair.Global)
		if err != nil {
			if errors.Is(err, ErrUnavailable) {
				return content.ErrAttemptBudgetExceeded
			}
			return err
		}

		anchorID, err := backupasset.NewOpaqueID()
		if err != nil {
			return err
		}
		pairID, err := backupasset.NewOpaqueID()
		if err != nil {
			return err
		}
		leaseOwner := itemAttempt.ID + ":" + anchorID
		expiresAt := attempt.LeaseExpiresAt.UTC()
		if job.AbsoluteDeadline.UTC().Before(expiresAt) {
			expiresAt = job.AbsoluteDeadline.UTC()
		}
		for index, bucket := range buckets {
			ceiling := bucket.ActiveJobs * int64(job.MaxOpenReaders)
			if ceiling < int64(job.MaxOpenReaders) {
				ceiling = int64(job.MaxOpenReaders)
			}
			result := tx.Model(&model.BackupAssetExportQuotaBucket{}).
				Where("id = ? AND active_readers < ?", bucket.ID, ceiling).
				Updates(map[string]any{
					"active_readers":      gorm.Expr("active_readers + 1"),
					"transition_revision": gorm.Expr("transition_revision + 1"), "updated_at": now,
				})
			if result.Error != nil {
				return fmt.Errorf("reserve export reader quota bucket: %w", result.Error)
			}
			if result.RowsAffected != 1 {
				return content.ErrAttemptBudgetExceeded
			}
			rowID := anchorID
			if index == 1 {
				rowID = pairID
			}
			jobID, attemptID := job.ID, attempt.ID
			row := model.BackupAssetExportReservation{
				ID: rowID, BucketID: bucket.ID, JobID: &jobID, AttemptID: &attemptID, Kind: "reader",
				ReaderEnqueueSequence: readerSequence, ReservedSlots: 1, ReservedProviderBytes: intent.Bytes, LeaseOwner: leaseOwner,
				LeaseExpiresAt: expiresAt, State: "active", CreatedAt: now, UpdatedAt: now,
			}
			if err := tx.Create(&row).Error; err != nil {
				return fmt.Errorf("create export reader reservation: %w", err)
			}
		}
		reservation = content.AttemptReadReservation{ID: anchorID, ReservedBytes: intent.Bytes}
		return nil
	})
	if err != nil {
		return content.AttemptReadReservation{}, err
	}
	return reservation, nil
}

func (service *AttemptBudgetService) FinalizeAttemptRead(
	ctx context.Context,
	finalization content.AttemptReadFinalization,
) error {
	if service == nil || backupasset.ValidateOpaqueID(finalization.ReservationID) != nil ||
		finalization.ReservedBytes < 0 || finalization.ProviderBytes < 0 ||
		(finalization.EvidenceKnown && finalization.ProviderBytes > finalization.ReservedBytes) {
		return content.ErrAttemptBudgetExceeded
	}
	now := service.now().UTC()
	// This discovery is only a bucket-selection hint; locked rows remain authoritative.
	hint, err := discoverAttemptBudgetFinalizationHint(service.db.WithContext(ctx), finalization.ReservationID)
	if err != nil {
		return err
	}
	var rejected bool
	err = service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		bucketPair, err := ensureAndLockQuotaBucketPairTx(tx, hint.ownerUserID, now)
		if err != nil {
			if errors.Is(err, ErrUnavailable) {
				return content.ErrAttemptBudgetExceeded
			}
			return err
		}
		if !validReaderQuotaBucketPair(bucketPair) {
			return content.ErrAttemptBudgetExceeded
		}
		itemAttempt, _, _, job, tupleErr := loadAttemptBudgetTupleTx(tx, hint.identity)
		if tupleErr != nil && !errors.Is(tupleErr, content.ErrAttemptBudgetExceeded) {
			return tupleErr
		}
		if job.OwnerUserID != hint.ownerUserID {
			return content.ErrAttemptBudgetExceeded
		}
		rows := make([]model.BackupAssetExportReservation, 0, 2)
		for _, bucket := range []model.BackupAssetExportQuotaBucket{bucketPair.Global, bucketPair.User} {
			var matches []model.BackupAssetExportReservation
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("lease_owner = ? AND kind = ? AND bucket_id = ?", hint.leaseOwner, "reader", bucket.ID).
				Limit(2).Find(&matches).Error; err != nil {
				return fmt.Errorf("lock paired export reader reservation: %w", err)
			}
			if len(matches) != 1 {
				return content.ErrAttemptBudgetExceeded
			}
			rows = append(rows, matches[0])
		}
		if rows[0].ID != finalization.ReservationID && rows[1].ID != finalization.ReservationID {
			return content.ErrAttemptBudgetExceeded
		}
		if !validAttemptReadReservationPair(rows, hint, finalization) {
			return content.ErrAttemptBudgetExceeded
		}
		terminalRows := 0
		for _, row := range rows {
			if row.State == "released" || row.State == "expired" {
				terminalRows++
				continue
			}
			if row.State != "active" {
				return content.ErrAttemptBudgetExceeded
			}
		}
		if terminalRows == len(rows) {
			return nil
		}
		if terminalRows != 0 {
			return content.ErrAttemptBudgetExceeded
		}
		releaseRows := func() error {
			for _, row := range rows {
				result := tx.Model(&model.BackupAssetExportQuotaBucket{}).
					Where("id = ? AND active_readers > 0", row.BucketID).
					Updates(map[string]any{
						"active_readers":      gorm.Expr("active_readers - 1"),
						"transition_revision": gorm.Expr("transition_revision + 1"), "updated_at": now,
					})
				if result.Error != nil {
					return fmt.Errorf("release export reader quota bucket: %w", result.Error)
				}
				if result.RowsAffected != 1 {
					return content.ErrAttemptBudgetExceeded
				}
				releasedAt := now
				result = tx.Model(&model.BackupAssetExportReservation{}).
					Where("id = ? AND state = ?", row.ID, "active").
					Updates(map[string]any{"state": "released", "released_at": releasedAt, "updated_at": now})
				if result.Error != nil {
					return fmt.Errorf("release export reader reservation: %w", result.Error)
				}
				if result.RowsAffected != 1 {
					return content.ErrAttemptBudgetExceeded
				}
			}
			return nil
		}
		if tupleErr != nil {
			if err := releaseRows(); err != nil {
				return err
			}
			rejected = true
			return nil
		}
		charge := finalization.ReservedBytes
		if finalization.EvidenceKnown {
			charge = finalization.ProviderBytes
		}
		if err := releaseRows(); err != nil {
			return err
		}
		result := tx.Model(&model.BackupAssetExportItemAttempt{}).
			Where("id = ? AND attempt_id = ?", itemAttempt.ID, itemAttempt.AttemptID).
			Update("provider_bytes", gorm.Expr("provider_bytes + ?", charge))
		if result.Error != nil {
			return fmt.Errorf("charge export reader item attempt: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return content.ErrAttemptBudgetExceeded
		}
		return nil
	})
	if err != nil {
		return err
	}
	if rejected {
		return content.ErrAttemptBudgetExceeded
	}
	return nil
}

func discoverAttemptBudgetFinalizationHint(
	db *gorm.DB, reservationID string,
) (attemptBudgetFinalizationHint, error) {
	var hint attemptBudgetFinalizationHint
	if db == nil {
		return hint, content.ErrAttemptBudgetExceeded
	}
	anchor, ownerUserID, err := discoverAttemptBudgetFinalizationAnchor(db, reservationID)
	if err != nil {
		return hint, err
	}
	separator := strings.IndexByte(anchor.LeaseOwner, ':')
	if separator != 32 || backupasset.ValidateOpaqueID(anchor.LeaseOwner[:separator]) != nil {
		return hint, content.ErrAttemptBudgetExceeded
	}
	identity, err := discoverAttemptBudgetIdentityTx(db, anchor.LeaseOwner[:separator])
	if err != nil {
		return hint, err
	}
	if anchor.JobID == nil || anchor.AttemptID == nil || identity.OwnerUserID != ownerUserID ||
		identity.JobID != *anchor.JobID || identity.AttemptID != *anchor.AttemptID {
		return hint, content.ErrAttemptBudgetExceeded
	}
	return attemptBudgetFinalizationHint{identity: identity, leaseOwner: anchor.LeaseOwner, ownerUserID: ownerUserID}, nil
}

func discoverAttemptBudgetFinalizationAnchor(
	db *gorm.DB, reservationID string,
) (model.BackupAssetExportReservation, uint, error) {
	var anchor model.BackupAssetExportReservation
	result := db.Where("id = ? AND kind = ?", reservationID, "reader").Limit(1).Find(&anchor)
	if result.Error != nil {
		return anchor, 0, fmt.Errorf("discover export reader reservation: %w", result.Error)
	}
	if result.RowsAffected != 1 || anchor.JobID == nil || anchor.AttemptID == nil {
		return anchor, 0, content.ErrAttemptBudgetExceeded
	}
	var job struct {
		OwnerUserID uint
	}
	result = db.Model(&model.BackupAssetExportJob{}).Select("owner_user_id").
		Where("id = ?", *anchor.JobID).Limit(1).Scan(&job)
	if result.Error != nil {
		return anchor, 0, fmt.Errorf("discover export reader job owner: %w", result.Error)
	}
	if result.RowsAffected != 1 || job.OwnerUserID == 0 {
		return anchor, 0, content.ErrAttemptBudgetExceeded
	}
	return anchor, job.OwnerUserID, nil
}

func validAttemptReadReservationPair(
	rows []model.BackupAssetExportReservation,
	hint attemptBudgetFinalizationHint,
	finalization content.AttemptReadFinalization,
) bool {
	if len(rows) != 2 || rows[0].BucketID == rows[1].BucketID || rows[0].ReaderEnqueueSequence <= 0 ||
		rows[0].ReaderEnqueueSequence != rows[1].ReaderEnqueueSequence ||
		rows[0].LeaseOwner != rows[1].LeaseOwner || rows[0].LeaseOwner != hint.leaseOwner ||
		rows[0].State != rows[1].State || rows[0].ReservedProviderBytes != rows[1].ReservedProviderBytes ||
		rows[0].ReservedProviderBytes != finalization.ReservedBytes || rows[0].JobID == nil || rows[1].JobID == nil ||
		rows[0].AttemptID == nil || rows[1].AttemptID == nil {
		return false
	}
	return *rows[0].JobID == *rows[1].JobID && *rows[0].JobID == hint.identity.JobID &&
		*rows[0].AttemptID == *rows[1].AttemptID && *rows[0].AttemptID == hint.identity.AttemptID
}

func (service *AttemptBudgetService) ReconcileExpiredAttemptReads(ctx context.Context, limit int) (int, error) {
	if service == nil || service.db == nil || limit <= 0 || limit > maxAttemptReadReconcileLimit {
		return 0, ErrUnavailable
	}
	service.readerSweepMu.Lock()
	defer service.readerSweepMu.Unlock()

	now := service.now().UTC()
	sweep, acquired, err := service.acquireExpiredAttemptReadSweep(ctx, now)
	if err != nil || !acquired {
		return 0, err
	}
	candidates, err := service.loadExpiredAttemptReadSweepCandidates(ctx, now, limit, sweep)
	if err != nil {
		releaseErr := service.persistExpiredAttemptReadSweepProgress(ctx, &sweep, sweep.cursor, true)
		return 0, errors.Join(err, releaseErr)
	}

	processed := 0
	attempted := 0
	var reconcileErr error
	for _, candidate := range candidates {
		if err := service.FinalizeAttemptRead(ctx, content.AttemptReadFinalization{
			ReservationID: candidate.ID, ReservedBytes: candidate.ReservedProviderBytes,
			EvidenceKnown: false, Succeeded: false,
		}); err != nil {
			reconcileErr = errors.Join(reconcileErr, err)
		} else {
			processed++
		}
		attempted++
		if err := service.persistExpiredAttemptReadSweepProgress(
			ctx, &sweep, candidate.ReaderEnqueueSequence, false,
		); err != nil {
			return processed, errors.Join(reconcileErr, err)
		}
	}
	completionCursor := sweep.cursor
	if attempted == len(candidates) && len(candidates) < limit {
		completionCursor = sweep.highWater
	}
	releaseErr := service.persistExpiredAttemptReadSweepProgress(ctx, &sweep, completionCursor, true)
	return processed, errors.Join(reconcileErr, releaseErr)
}

func (service *AttemptBudgetService) acquireExpiredAttemptReadSweep(
	ctx context.Context,
	now time.Time,
) (attemptReadSweepLease, bool, error) {
	var sweep attemptReadSweepLease
	if service == nil || service.db == nil || now.IsZero() || now.Location() != time.UTC {
		return sweep, false, ErrUnavailable
	}
	acquired := false
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var bucket model.BackupAssetExportQuotaBucket
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("scope = ? AND subject = ?", "global", "global").Limit(1).Find(&bucket)
		if result.Error != nil {
			return fmt.Errorf("lock export reader sweep: %w", result.Error)
		}
		if result.RowsAffected == 0 {
			return readerSweepNoLatchWorkTx(tx)
		}
		if !validReaderGlobalQuotaBucket(bucket) || bucket.ReaderSweepRevision == math.MaxInt64 {
			return ErrUnavailable
		}
		if bucket.ReaderSweepLeaseExpiresAt != nil && bucket.ReaderSweepLeaseExpiresAt.UTC().After(now) {
			return nil
		}
		cursor := bucket.ReaderSweepCursor
		highWater := bucket.ReaderSweepHighWater
		if cursor == highWater {
			cursor = 0
			highWater = bucket.ReaderNextSequence - 1
		}
		if highWater == 0 {
			return nil
		}
		revision := bucket.ReaderSweepRevision + 1
		leaseExpiresAt := now.Add(readerSweepLeaseTTL)
		result = tx.Model(&model.BackupAssetExportQuotaBucket{}).
			Where("id = ? AND scope = ? AND subject = ? AND reader_sweep_revision = ?",
				bucket.ID, "global", "global", bucket.ReaderSweepRevision).
			UpdateColumns(map[string]any{
				"reader_sweep_cursor":           cursor,
				"reader_sweep_high_water":       highWater,
				"reader_sweep_revision":         revision,
				"reader_sweep_lease_expires_at": leaseExpiresAt,
			})
		if result.Error != nil {
			return fmt.Errorf("acquire export reader sweep: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return ErrUnavailable
		}
		sweep = attemptReadSweepLease{
			bucketID: bucket.ID, revision: revision, cursor: cursor, highWater: highWater,
		}
		acquired = true
		return nil
	})
	return sweep, acquired, err
}

func readerSweepNoLatchWorkTx(tx *gorm.DB) error {
	if tx == nil {
		return ErrUnavailable
	}
	var job model.BackupAssetExportJob
	result := tx.Model(&model.BackupAssetExportJob{}).Select("id").Limit(1).Find(&job)
	if result.Error != nil {
		return fmt.Errorf("check export jobs without reader sweep latch: %w", result.Error)
	}
	if result.RowsAffected != 0 {
		return ErrUnavailable
	}
	var reader model.BackupAssetExportReservation
	result = tx.Model(&model.BackupAssetExportReservation{}).Select("id").
		Where("kind = ?", "reader").Limit(1).Find(&reader)
	if result.Error != nil {
		return fmt.Errorf("check export readers without reader sweep latch: %w", result.Error)
	}
	if result.RowsAffected != 0 {
		return ErrUnavailable
	}
	return nil
}

func (service *AttemptBudgetService) loadExpiredAttemptReadSweepCandidates(
	ctx context.Context,
	now time.Time,
	limit int,
	sweep attemptReadSweepLease,
) ([]model.BackupAssetExportReservation, error) {
	if service == nil || service.db == nil || limit <= 0 || limit > maxAttemptReadReconcileLimit ||
		backupasset.ValidateOpaqueID(sweep.bucketID) != nil || sweep.cursor < 0 || sweep.highWater <= sweep.cursor {
		return nil, ErrUnavailable
	}
	var rows []model.BackupAssetExportReservation
	result := service.db.WithContext(ctx).
		Where("bucket_id = ? AND kind = ? AND state = ? AND lease_expires_at <= ?", sweep.bucketID, "reader", "active", now).
		Where("reader_enqueue_sequence > ? AND reader_enqueue_sequence <= ?", sweep.cursor, sweep.highWater).
		Order("reader_enqueue_sequence ASC").Limit(limit).Find(&rows)
	if result.Error != nil {
		return nil, fmt.Errorf("load expired export reader sweep candidates: %w", result.Error)
	}
	previous := sweep.cursor
	seenOwners := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		if row.BucketID != sweep.bucketID || row.Kind != "reader" || row.State != "active" ||
			row.ReaderEnqueueSequence <= previous || row.ReaderEnqueueSequence > sweep.highWater ||
			row.LeaseExpiresAt.UTC().After(now) || row.LeaseOwner == "" {
			return nil, ErrUnavailable
		}
		if _, duplicate := seenOwners[row.LeaseOwner]; duplicate {
			return nil, ErrUnavailable
		}
		seenOwners[row.LeaseOwner] = struct{}{}
		previous = row.ReaderEnqueueSequence
	}
	return rows, nil
}

func (service *AttemptBudgetService) persistExpiredAttemptReadSweepProgress(
	ctx context.Context,
	sweep *attemptReadSweepLease,
	cursor int64,
	release bool,
) error {
	if service == nil || service.db == nil || sweep == nil || backupasset.ValidateOpaqueID(sweep.bucketID) != nil ||
		sweep.revision <= 0 || cursor < sweep.cursor || cursor > sweep.highWater {
		return ErrUnavailable
	}
	updates := map[string]any{"reader_sweep_cursor": cursor}
	if release {
		updates["reader_sweep_lease_expires_at"] = nil
	} else {
		now := service.now().UTC()
		if now.IsZero() || now.Location() != time.UTC {
			return ErrUnavailable
		}
		updates["reader_sweep_lease_expires_at"] = now.Add(readerSweepLeaseTTL)
	}
	result := service.db.WithContext(ctx).Model(&model.BackupAssetExportQuotaBucket{}).
		Where(`id = ? AND scope = ? AND subject = ? AND reader_sweep_revision = ?
			AND reader_sweep_cursor = ? AND reader_sweep_high_water = ?
			AND reader_sweep_lease_expires_at IS NOT NULL`,
			sweep.bucketID, "global", "global", sweep.revision, sweep.cursor, sweep.highWater).
		UpdateColumns(updates)
	if result.Error != nil {
		return fmt.Errorf("persist export reader sweep progress: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return ErrUnavailable
	}
	sweep.cursor = cursor
	return nil
}

func validAttemptReadIntent(intent content.AttemptReadIntent) bool {
	switch intent.Mode {
	case content.SourceModeStat:
		return intent.Bytes == 0 && intent.Offset == nil && intent.Length == nil
	case content.SourceModeSequential:
		return intent.Bytes > 0 && intent.Offset == nil && intent.Length == nil
	default:
		return false
	}
}

func discoverAttemptBudgetIdentityTx(tx *gorm.DB, sessionID string) (attemptBudgetIdentity, error) {
	var identity attemptBudgetIdentity
	result := tx.Table("backup_asset_export_item_attempts AS item_attempt").
		Select("item_attempt.id AS item_attempt_id, item_attempt.item_id, item_attempt.attempt_id, "+
			"item_attempt.job_id, job.owner_user_id").
		Joins("JOIN backup_asset_export_jobs AS job ON job.id = item_attempt.job_id").
		Where("item_attempt.id = ?", sessionID).Limit(1).Scan(&identity)
	if result.Error != nil {
		return identity, fmt.Errorf("discover export attempt budget identity: %w", result.Error)
	}
	if result.RowsAffected != 1 || identity.OwnerUserID == 0 || identity.ItemAttemptID != sessionID ||
		backupasset.ValidateOpaqueID(identity.ItemAttemptID) != nil || backupasset.ValidateOpaqueID(identity.ItemID) != nil ||
		backupasset.ValidateOpaqueID(identity.AttemptID) != nil || backupasset.ValidateOpaqueID(identity.JobID) != nil {
		return identity, content.ErrAttemptBudgetExceeded
	}
	return identity, nil
}

func loadAttemptBudgetTupleTx(
	tx *gorm.DB, identity attemptBudgetIdentity,
) (model.BackupAssetExportItemAttempt, model.BackupAssetExportItem, model.BackupAssetExportAttempt, model.BackupAssetExportJob, error) {
	var job model.BackupAssetExportJob
	result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", identity.JobID).Limit(1).Find(&job)
	if result.Error != nil {
		return model.BackupAssetExportItemAttempt{}, model.BackupAssetExportItem{}, model.BackupAssetExportAttempt{}, job,
			fmt.Errorf("lock export reader job: %w", result.Error)
	}
	jobFound := result.RowsAffected == 1
	var attempt model.BackupAssetExportAttempt
	result = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", identity.AttemptID).Limit(1).Find(&attempt)
	if result.Error != nil {
		return model.BackupAssetExportItemAttempt{}, model.BackupAssetExportItem{}, attempt, job,
			fmt.Errorf("lock export reader attempt: %w", result.Error)
	}
	attemptFound := result.RowsAffected == 1
	var item model.BackupAssetExportItem
	result = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", identity.ItemID).Limit(1).Find(&item)
	if result.Error != nil {
		return model.BackupAssetExportItemAttempt{}, item, attempt, job, fmt.Errorf("lock export reader item: %w", result.Error)
	}
	itemFound := result.RowsAffected == 1
	var itemAttempt model.BackupAssetExportItemAttempt
	result = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", identity.ItemAttemptID).Limit(1).Find(&itemAttempt)
	if result.Error != nil {
		return itemAttempt, item, attempt, job, fmt.Errorf("lock export reader item attempt: %w", result.Error)
	}
	itemAttemptFound := result.RowsAffected == 1
	if !jobFound || job.OwnerUserID != identity.OwnerUserID || job.CurrentAttemptID == nil ||
		*job.CurrentAttemptID != identity.AttemptID || ExecutionState(job.ExecutionState) != ExecutionRunning ||
		job.MaxOpenReaders <= 0 ||
		!attemptFound || attempt.JobID != job.ID || !attempt.IsCurrent || AttemptState(attempt.State) != AttemptActive ||
		!itemFound || item.JobID != job.ID || item.CurrentAttemptID == nil || *item.CurrentAttemptID != attempt.ID ||
		!itemAttemptFound || itemAttempt.JobID != job.ID || itemAttempt.AttemptID != attempt.ID ||
		itemAttempt.ItemID != item.ID || (ItemState(itemAttempt.State) != ItemPending && ItemState(itemAttempt.State) != ItemRead) {
		return itemAttempt, item, attempt, job, content.ErrAttemptBudgetExceeded
	}
	return itemAttempt, item, attempt, job, nil
}

var _ content.AttemptBudget = (*AttemptBudgetService)(nil)

func NewQuotaService(db *gorm.DB, now func() time.Time, limits QuotaLimits) (*QuotaService, error) {
	if db == nil || !validQuotaLimits(limits) {
		return nil, ErrUnavailable
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &QuotaService{db: db, now: now, limits: limits}, nil
}

func validQuotaLimits(limits QuotaLimits) bool {
	return limits.GlobalActiveJobs > 0 && limits.UserActiveJobs > 0 &&
		limits.UserActiveJobs <= limits.GlobalActiveJobs && limits.GlobalStoreBytes > 0 &&
		limits.UserStoreBytes > 0 && limits.UserStoreBytes <= limits.GlobalStoreBytes
}

func (service *QuotaService) ReserveJob(ctx context.Context, request QuotaJobRequest) (QuotaReservation, error) {
	if service == nil || request.UserID == 0 || backupasset.ValidateOpaqueID(request.JobID) != nil ||
		request.StoreBytes <= 0 || request.ExpiresAt.IsZero() {
		return QuotaReservation{}, ErrQuotaExceeded
	}
	var reservation QuotaReservation
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		buckets, insertions, err := ensureAndLockQuotaBucketPairForAdmissionTx(tx, request.UserID)
		if err != nil {
			return err
		}
		now := service.now().UTC()
		if !request.ExpiresAt.After(now) {
			return ErrQuotaExceeded
		}
		if err := stampNewQuotaBucketPairTx(tx, buckets, insertions, now); err != nil {
			return err
		}
		if err := reserveQuotaBucket(tx, buckets.Global.ID, service.limits.GlobalActiveJobs, service.limits.GlobalStoreBytes, request.StoreBytes, now); err != nil {
			return err
		}
		if err := reserveQuotaBucket(tx, buckets.User.ID, service.limits.UserActiveJobs, service.limits.UserStoreBytes, request.StoreBytes, now); err != nil {
			return err
		}
		globalJobID, err := createQuotaReservation(tx, buckets.Global.ID, request, "job", now)
		if err != nil {
			return err
		}
		userJobID, err := createQuotaReservation(tx, buckets.User.ID, request, "job", now)
		if err != nil {
			return err
		}
		globalStoreID, err := createQuotaReservation(tx, buckets.Global.ID, request, "store", now)
		if err != nil {
			return err
		}
		userStoreID, err := createQuotaReservation(tx, buckets.User.ID, request, "store", now)
		if err != nil {
			return err
		}
		reservation = QuotaReservation{
			GlobalJobID: globalJobID, UserJobID: userJobID,
			GlobalStoreID: globalStoreID, UserStoreID: userStoreID,
		}
		return nil
	})
	return reservation, err
}

func ensureAndLockQuotaBucketPairTx(tx *gorm.DB, ownerUserID uint, now time.Time) (quotaBucketPair, error) {
	var pair quotaBucketPair
	if tx == nil || ownerUserID == 0 || now.IsZero() || now.Location() != time.UTC {
		return pair, ErrUnavailable
	}
	userSubject := strconv.FormatUint(uint64(ownerUserID), 10)
	globalID, err := ensureQuotaBucketTx(tx, "global", "global", now)
	if err != nil {
		return pair, err
	}
	userID, err := ensureQuotaBucketTx(tx, "user", userSubject, now)
	if err != nil {
		return pair, err
	}
	pair, err = lockQuotaBucketPairTx(tx, ownerUserID)
	if err != nil {
		return quotaBucketPair{}, err
	}
	if pair.Global.ID != globalID || pair.User.ID != userID {
		return quotaBucketPair{}, ErrUnavailable
	}
	return pair, nil
}

func ensureAndLockQuotaBucketPairForAdmissionTx(
	tx *gorm.DB,
	ownerUserID uint,
) (quotaBucketPair, quotaBucketPairInsertions, error) {
	var insertions quotaBucketPairInsertions
	if tx == nil || ownerUserID == 0 {
		return quotaBucketPair{}, insertions, ErrUnavailable
	}
	userSubject := strconv.FormatUint(uint64(ownerUserID), 10)
	globalID, globalInserted, err := ensureQuotaBucketForAdmissionTx(tx, "global", "global")
	if err != nil {
		return quotaBucketPair{}, insertions, err
	}
	userID, userInserted, err := ensureQuotaBucketForAdmissionTx(tx, "user", userSubject)
	if err != nil {
		return quotaBucketPair{}, insertions, err
	}
	pair, err := lockQuotaBucketPairTx(tx, ownerUserID)
	if err != nil {
		return quotaBucketPair{}, insertions, err
	}
	if (globalInserted && pair.Global.ID != globalID) || (userInserted && pair.User.ID != userID) {
		return quotaBucketPair{}, insertions, ErrUnavailable
	}
	insertions.Global = globalInserted
	insertions.User = userInserted
	return pair, insertions, nil
}

func ensureQuotaBucketForAdmissionTx(tx *gorm.DB, scope, subject string) (string, bool, error) {
	digest := sha256.Sum256([]byte("xirang.backup_asset.export.quota.v1\x00" + scope + "\x00" + subject))
	id := hex.EncodeToString(digest[:16])
	pendingTimestamp := time.Unix(0, 0).UTC()
	row := model.BackupAssetExportQuotaBucket{
		ID: id, Scope: scope, Subject: subject, TransitionRevision: 1,
		LifecycleNextSequence: 1, ReaderNextSequence: 1,
		CreatedAt: pendingTimestamp, UpdatedAt: pendingTimestamp,
	}
	result := tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "scope"}, {Name: "subject"}}, DoNothing: true,
	}).Create(&row)
	if result.Error != nil {
		return "", false, fmt.Errorf("ensure export quota bucket: %w", result.Error)
	}
	if result.RowsAffected != 0 && result.RowsAffected != 1 {
		return "", false, ErrUnavailable
	}
	return id, result.RowsAffected == 1, nil
}

func lockQuotaBucketPairTx(tx *gorm.DB, ownerUserID uint) (quotaBucketPair, error) {
	var pair quotaBucketPair
	if tx == nil || ownerUserID == 0 {
		return pair, ErrUnavailable
	}
	userSubject := strconv.FormatUint(uint64(ownerUserID), 10)
	for rank, expected := range []struct {
		scope   string
		subject string
	}{
		{scope: "global", subject: "global"},
		{scope: "user", subject: userSubject},
	} {
		var bucket model.BackupAssetExportQuotaBucket
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("scope = ? AND subject = ?", expected.scope, expected.subject).Limit(1).Find(&bucket)
		if result.Error != nil {
			return pair, fmt.Errorf("lock %s export quota bucket: %w", expected.scope, result.Error)
		}
		if result.RowsAffected != 1 || bucket.Scope != expected.scope || bucket.Subject != expected.subject {
			return pair, ErrUnavailable
		}
		if rank == 0 {
			pair.Global = bucket
		} else {
			pair.User = bucket
		}
	}
	return pair, nil
}

func stampNewQuotaBucketPairTx(
	tx *gorm.DB,
	pair quotaBucketPair,
	insertions quotaBucketPairInsertions,
	now time.Time,
) error {
	if tx == nil || now.IsZero() || now.Location() != time.UTC {
		return ErrUnavailable
	}
	for _, target := range []struct {
		bucket   model.BackupAssetExportQuotaBucket
		inserted bool
	}{
		{bucket: pair.Global, inserted: insertions.Global},
		{bucket: pair.User, inserted: insertions.User},
	} {
		if !target.inserted {
			continue
		}
		result := tx.Model(&model.BackupAssetExportQuotaBucket{}).
			Where("id = ? AND scope = ? AND subject = ?", target.bucket.ID, target.bucket.Scope, target.bucket.Subject).
			UpdateColumns(map[string]any{"created_at": now, "updated_at": now})
		if result.Error != nil {
			return fmt.Errorf("stamp export quota bucket: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return ErrUnavailable
		}
	}
	return nil
}

func validReaderQuotaBucketPair(pair quotaBucketPair) bool {
	return validReaderGlobalQuotaBucket(pair.Global) && validReaderUserQuotaBucket(pair.User)
}

func validReaderGlobalQuotaBucket(bucket model.BackupAssetExportQuotaBucket) bool {
	if bucket.Scope != "global" || bucket.Subject != "global" || backupasset.ValidateOpaqueID(bucket.ID) != nil ||
		bucket.ReaderNextSequence <= 0 || bucket.ReaderSweepCursor < 0 || bucket.ReaderSweepHighWater < 0 ||
		bucket.ReaderSweepCursor > bucket.ReaderSweepHighWater || bucket.ReaderSweepHighWater >= bucket.ReaderNextSequence ||
		bucket.ReaderSweepRevision < 0 {
		return false
	}
	if bucket.ReaderSweepRevision == 0 {
		return bucket.ReaderSweepCursor == 0 && bucket.ReaderSweepHighWater == 0 && bucket.ReaderSweepLeaseExpiresAt == nil
	}
	return bucket.ReaderSweepHighWater > 0
}

func validReaderUserQuotaBucket(bucket model.BackupAssetExportQuotaBucket) bool {
	return bucket.Scope == "user" && bucket.ReaderNextSequence == 1 && bucket.ReaderSweepCursor == 0 &&
		bucket.ReaderSweepHighWater == 0 && bucket.ReaderSweepRevision == 0 && bucket.ReaderSweepLeaseExpiresAt == nil
}

func allocateReaderEnqueueSequenceTx(
	tx *gorm.DB,
	globalBucket model.BackupAssetExportQuotaBucket,
) (int64, error) {
	sequence := globalBucket.ReaderNextSequence
	if tx == nil || !validReaderGlobalQuotaBucket(globalBucket) || sequence == math.MaxInt64 {
		return 0, ErrUnavailable
	}
	result := tx.Model(&model.BackupAssetExportQuotaBucket{}).
		Where("id = ? AND scope = ? AND subject = ? AND reader_next_sequence = ?",
			globalBucket.ID, "global", "global", sequence).
		UpdateColumn("reader_next_sequence", sequence+1)
	if result.Error != nil {
		return 0, fmt.Errorf("allocate export reader sequence: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return 0, ErrUnavailable
	}
	return sequence, nil
}

func lockStoreReservationPairTx(
	tx *gorm.DB,
	buckets quotaBucketPair,
	job model.BackupAssetExportJob,
) ([]model.BackupAssetExportReservation, error) {
	if tx == nil || backupasset.ValidateOpaqueID(job.ID) != nil || job.OwnerUserID == 0 || job.AbsoluteDeadline.IsZero() {
		return nil, ErrUnavailable
	}
	rows := make([]model.BackupAssetExportReservation, 0, 2)
	for _, bucket := range []model.BackupAssetExportQuotaBucket{buckets.Global, buckets.User} {
		var matches []model.BackupAssetExportReservation
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("bucket_id = ? AND job_id = ? AND kind = ?", bucket.ID, job.ID, "store").
			Limit(2).Find(&matches)
		if result.Error != nil {
			return nil, fmt.Errorf("lock export store reservation: %w", result.Error)
		}
		if len(matches) != 1 {
			return nil, ErrUnavailable
		}
		row := matches[0]
		if row.BucketID != bucket.ID || row.JobID == nil || *row.JobID != job.ID || row.AttemptID != nil ||
			row.Kind != "store" || row.ReservedSlots != 0 || row.ReservedLogicalBytes != 0 ||
			row.ReservedProviderBytes != 0 || row.ReservedCipherBytes != 0 || row.ReservedStoreBytes <= 0 ||
			row.LeaseOwner != job.ID || !row.LeaseExpiresAt.UTC().Equal(job.AbsoluteDeadline.UTC()) {
			return nil, ErrUnavailable
		}
		rows = append(rows, row)
	}
	if rows[0].ReservedStoreBytes != rows[1].ReservedStoreBytes {
		return nil, ErrUnavailable
	}
	return rows, nil
}

func chargeSealedStoreBytesTx(
	tx *gorm.DB,
	buckets quotaBucketPair,
	job model.BackupAssetExportJob,
	ciphertextBytes, expectedPeakStoreBytes int64,
	now time.Time,
) error {
	if tx == nil || ciphertextBytes < 0 || expectedPeakStoreBytes <= 0 || now.IsZero() || now.Location() != time.UTC {
		return ErrUnavailable
	}
	rows, err := lockStoreReservationPairTx(tx, buckets, job)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.State != "active" || row.ReleasedAt != nil || row.ReservedStoreBytes != expectedPeakStoreBytes {
			return ErrAttemptFenceLost
		}
	}
	for _, bucket := range []model.BackupAssetExportQuotaBucket{buckets.Global, buckets.User} {
		result := tx.Model(&model.BackupAssetExportQuotaBucket{}).
			Where("id = ? AND used_store_bytes <= reserved_store_bytes - ?", bucket.ID, ciphertextBytes).
			Updates(map[string]any{
				"used_store_bytes":    gorm.Expr("used_store_bytes + ?", ciphertextBytes),
				"transition_revision": gorm.Expr("transition_revision + 1"),
				"updated_at":          now,
			})
		if result.Error != nil {
			return fmt.Errorf("charge sealed export store quota bucket: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return ErrQuotaExceeded
		}
	}
	return nil
}

func debitDiscardedSealedStoreBytesTx(
	tx *gorm.DB,
	buckets quotaBucketPair,
	job model.BackupAssetExportJob,
	ciphertextBytes int64,
	now time.Time,
) error {
	if tx == nil || ciphertextBytes <= 0 || now.IsZero() || now.Location() != time.UTC {
		return ErrUnavailable
	}
	rows, err := lockStoreReservationPairTx(tx, buckets, job)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.State != "active" || row.ReleasedAt != nil {
			return ErrAttemptFenceLost
		}
	}
	for _, bucket := range []model.BackupAssetExportQuotaBucket{buckets.Global, buckets.User} {
		result := tx.Model(&model.BackupAssetExportQuotaBucket{}).
			Where("id = ? AND used_store_bytes >= ?", bucket.ID, ciphertextBytes).
			Updates(map[string]any{
				"used_store_bytes":    gorm.Expr("used_store_bytes - ?", ciphertextBytes),
				"transition_revision": gorm.Expr("transition_revision + 1"),
				"updated_at":          now,
			})
		if result.Error != nil {
			return fmt.Errorf("debit discarded sealed export store quota bucket: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return ErrAttemptFenceLost
		}
	}
	return nil
}

func markStoreReservationsPurgePendingTx(
	tx *gorm.DB,
	buckets quotaBucketPair,
	job model.BackupAssetExportJob,
	now time.Time,
) error {
	if tx == nil || now.IsZero() || now.Location() != time.UTC {
		return ErrUnavailable
	}
	rows, err := lockStoreReservationPairTx(tx, buckets, job)
	if err != nil {
		return err
	}
	switch {
	case rows[0].State == "purge_pending" && rows[1].State == "purge_pending":
		if rows[0].ReleasedAt != nil || rows[1].ReleasedAt != nil {
			return ErrUnavailable
		}
		return nil
	case rows[0].State != "active" || rows[1].State != "active" || rows[0].ReleasedAt != nil || rows[1].ReleasedAt != nil:
		return ErrUnavailable
	}
	for _, row := range rows {
		result := tx.Model(&model.BackupAssetExportReservation{}).
			Where("id = ? AND state = ?", row.ID, "active").
			Updates(map[string]any{"state": "purge_pending", "updated_at": now})
		if result.Error != nil {
			return fmt.Errorf("mark export store reservation purge pending: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return ErrUnavailable
		}
	}
	return nil
}

func (service *QuotaService) Release(ctx context.Context, reservation QuotaReservation) error {
	return service.release(ctx, []string{
		reservation.GlobalJobID, reservation.UserJobID, reservation.GlobalStoreID, reservation.UserStoreID,
	})
}

func (service *QuotaService) ReleaseNonStore(ctx context.Context, reservation QuotaReservation) error {
	return service.release(ctx, []string{reservation.GlobalJobID, reservation.UserJobID})
}

func (service *QuotaService) ReleaseStore(ctx context.Context, reservation QuotaReservation) error {
	return service.release(ctx, []string{reservation.GlobalStoreID, reservation.UserStoreID})
}

func (service *QuotaService) release(ctx context.Context, ids []string) error {
	if service == nil {
		return ErrUnavailable
	}
	for _, id := range ids {
		if backupasset.ValidateOpaqueID(id) != nil {
			return ErrUnavailable
		}
	}
	return service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := service.now().UTC()
		var discovered []model.BackupAssetExportReservation
		if err := tx.Where("id IN ?", ids).Find(&discovered).Error; err != nil {
			return fmt.Errorf("discover export quota reservations: %w", err)
		}
		if len(discovered) != len(ids) || (len(discovered) != 2 && len(discovered) != 4) {
			return ErrUnavailable
		}
		bucketIDs := make([]string, 0, 2)
		seenBuckets := make(map[string]struct{}, 2)
		seenIDs := make(map[string]struct{}, len(ids))
		jobID := ""
		hasStoreReservation := false
		for _, row := range discovered {
			if _, duplicate := seenIDs[row.ID]; duplicate || row.JobID == nil || row.AttemptID != nil ||
				(row.Kind != "job" && row.Kind != "store") {
				return ErrUnavailable
			}
			seenIDs[row.ID] = struct{}{}
			if jobID == "" {
				jobID = *row.JobID
			} else if jobID != *row.JobID {
				return ErrUnavailable
			}
			if _, exists := seenBuckets[row.BucketID]; !exists {
				seenBuckets[row.BucketID] = struct{}{}
				bucketIDs = append(bucketIDs, row.BucketID)
			}
			switch row.Kind {
			case "store":
				hasStoreReservation = true
			}
		}
		if len(bucketIDs) != 2 {
			return ErrUnavailable
		}
		var discoveredBuckets []model.BackupAssetExportQuotaBucket
		if err := tx.Where("id IN ?", bucketIDs).Find(&discoveredBuckets).Error; err != nil {
			return fmt.Errorf("discover export quota buckets: %w", err)
		}
		if len(discoveredBuckets) != 2 {
			return ErrUnavailable
		}
		var ownerUserID uint
		for _, bucket := range discoveredBuckets {
			switch {
			case bucket.Scope == "global" && bucket.Subject == "global":
			case bucket.Scope == "user":
				parsed, err := strconv.ParseUint(bucket.Subject, 10, 64)
				if err != nil || parsed == 0 || strconv.FormatUint(parsed, 10) != bucket.Subject || uint64(uint(parsed)) != parsed {
					return ErrUnavailable
				}
				ownerUserID = uint(parsed)
			default:
				return ErrUnavailable
			}
		}
		if ownerUserID == 0 {
			return ErrUnavailable
		}
		buckets, err := ensureAndLockQuotaBucketPairTx(tx, ownerUserID, now)
		if err != nil {
			return err
		}
		if _, ok := seenBuckets[buckets.Global.ID]; !ok {
			return ErrUnavailable
		}
		if _, ok := seenBuckets[buckets.User.ID]; !ok {
			return ErrUnavailable
		}

		var artifacts []model.BackupAssetExportArtifact
		var job model.BackupAssetExportJob
		jobFound := false
		if hasStoreReservation {
			result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", jobID).Limit(1).Find(&job)
			if result.Error != nil {
				return fmt.Errorf("lock export job for store quota release: %w", result.Error)
			}
			jobFound = result.RowsAffected == 1
			if jobFound && job.OwnerUserID != ownerUserID {
				return ErrUnavailable
			}
			result = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("job_id = ?", jobID).
				Order("id ASC").Limit(2).Find(&artifacts)
			if result.Error != nil {
				return fmt.Errorf("lock export artifact for store quota release: %w", result.Error)
			}
			if len(artifacts) > 1 {
				return ErrUnavailable
			}
		}

		canonical := make([]model.BackupAssetExportReservation, 0, len(discovered))
		for _, bucketID := range []string{buckets.Global.ID, buckets.User.ID} {
			for _, kind := range []string{"job", "store"} {
				for _, row := range discovered {
					if row.BucketID == bucketID && row.Kind == kind {
						canonical = append(canonical, row)
					}
				}
			}
		}
		if len(canonical) != len(discovered) {
			return ErrUnavailable
		}
		locked := make([]model.BackupAssetExportReservation, 0, len(canonical))
		for _, expected := range canonical {
			var row model.BackupAssetExportReservation
			result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", expected.ID).Limit(1).Find(&row)
			if result.Error != nil {
				return fmt.Errorf("lock export quota reservation: %w", result.Error)
			}
			if result.RowsAffected != 1 || row.BucketID != expected.BucketID || row.JobID == nil || *row.JobID != jobID ||
				row.AttemptID != nil || row.Kind != expected.Kind {
				return ErrUnavailable
			}
			locked = append(locked, row)
		}
		jobRows := make([]model.BackupAssetExportReservation, 0, 2)
		storeRows := make([]model.BackupAssetExportReservation, 0, 2)
		for _, row := range locked {
			switch row.Kind {
			case "job":
				jobRows = append(jobRows, row)
			case "store":
				storeRows = append(storeRows, row)
			}
		}
		releaseJob, err := quotaReservationPairCanRelease(jobRows, "job", jobID)
		if err != nil {
			return err
		}
		releaseStore, err := quotaReservationPairCanRelease(storeRows, "store", jobID)
		if err != nil {
			return err
		}
		storeCiphertextBytes := int64(0)
		if releaseStore && len(artifacts) == 1 {
			artifact := artifacts[0]
			if !jobFound || artifact.State != "purged" || artifact.PurgedAt == nil || artifact.CiphertextSize < 0 ||
				job.ArtifactBytes != artifact.CiphertextSize {
				return ErrUnavailable
			}
			storeCiphertextBytes = artifact.CiphertextSize
		}
		for _, row := range locked {
			release := (row.Kind == "job" && releaseJob) || (row.Kind == "store" && releaseStore)
			if !release {
				continue
			}
			usedStoreBytes := int64(0)
			if row.Kind == "store" {
				usedStoreBytes = storeCiphertextBytes
			}
			result := tx.Model(&model.BackupAssetExportQuotaBucket{}).
				Where("id = ? AND active_jobs >= ? AND reserved_store_bytes >= ? AND used_store_bytes >= ?",
					row.BucketID, row.ReservedSlots, row.ReservedStoreBytes, usedStoreBytes).
				Updates(map[string]any{
					"active_jobs":          gorm.Expr("active_jobs - ?", row.ReservedSlots),
					"reserved_store_bytes": gorm.Expr("reserved_store_bytes - ?", row.ReservedStoreBytes),
					"used_store_bytes":     gorm.Expr("used_store_bytes - ?", usedStoreBytes),
					"transition_revision":  gorm.Expr("transition_revision + 1"),
					"updated_at":           now,
				})
			if result.Error != nil {
				return fmt.Errorf("release export quota bucket: %w", result.Error)
			}
			if result.RowsAffected != 1 {
				return ErrUnavailable
			}
			releasedAt := now
			result = tx.Model(&model.BackupAssetExportReservation{}).Where("id = ? AND state = ?", row.ID, row.State).
				Updates(map[string]any{"state": "released", "released_at": releasedAt, "updated_at": now})
			if result.Error != nil {
				return fmt.Errorf("release export quota reservation: %w", result.Error)
			}
			if result.RowsAffected != 1 {
				return ErrUnavailable
			}
		}
		return nil
	})
}

func quotaReservationPairCanRelease(
	rows []model.BackupAssetExportReservation,
	kind, jobID string,
) (bool, error) {
	if len(rows) == 0 {
		return false, nil
	}
	if len(rows) != 2 || (kind != "job" && kind != "store") || backupasset.ValidateOpaqueID(jobID) != nil ||
		rows[0].BucketID == rows[1].BucketID {
		return false, ErrUnavailable
	}
	for _, row := range rows {
		if row.JobID == nil || *row.JobID != jobID || row.AttemptID != nil || row.Kind != kind || row.LeaseOwner != jobID ||
			row.LeaseExpiresAt.IsZero() {
			return false, ErrUnavailable
		}
		if kind == "job" {
			if row.ReservedSlots != 1 || row.ReservedLogicalBytes != 0 || row.ReservedProviderBytes != 0 ||
				row.ReservedCipherBytes != 0 || row.ReservedStoreBytes != 0 {
				return false, ErrUnavailable
			}
		} else if row.ReservedSlots != 0 || row.ReservedLogicalBytes != 0 || row.ReservedProviderBytes != 0 ||
			row.ReservedCipherBytes != 0 || row.ReservedStoreBytes <= 0 {
			return false, ErrUnavailable
		}
	}
	if !rows[0].LeaseExpiresAt.UTC().Equal(rows[1].LeaseExpiresAt.UTC()) ||
		(kind == "store" && rows[0].ReservedStoreBytes != rows[1].ReservedStoreBytes) {
		return false, ErrUnavailable
	}
	switch {
	case rows[0].State == "released" && rows[1].State == "released":
		if rows[0].ReleasedAt == nil || rows[1].ReleasedAt == nil {
			return false, ErrUnavailable
		}
		return false, nil
	case rows[0].State == "active" && rows[1].State == "active":
		if rows[0].ReleasedAt != nil || rows[1].ReleasedAt != nil {
			return false, ErrUnavailable
		}
		return true, nil
	case kind == "store" && rows[0].State == "purge_pending" && rows[1].State == "purge_pending":
		if rows[0].ReleasedAt != nil || rows[1].ReleasedAt != nil {
			return false, ErrUnavailable
		}
		return true, nil
	default:
		return false, ErrUnavailable
	}
}

func reserveQuotaBucket(
	tx *gorm.DB,
	bucketID string,
	maxJobs, maxStoreBytes, storeBytes int64,
	now time.Time,
) error {
	result := tx.Model(&model.BackupAssetExportQuotaBucket{}).
		Where("id = ? AND active_jobs < ? AND reserved_store_bytes <= ?", bucketID, maxJobs, maxStoreBytes-storeBytes).
		Updates(map[string]any{
			"active_jobs":          gorm.Expr("active_jobs + 1"),
			"reserved_store_bytes": gorm.Expr("reserved_store_bytes + ?", storeBytes),
			"transition_revision":  gorm.Expr("transition_revision + 1"),
			"updated_at":           now,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrQuotaExceeded
	}
	return nil
}

func createQuotaReservation(tx *gorm.DB, bucketID string, request QuotaJobRequest, kind string, now time.Time) (string, error) {
	if kind != "job" && kind != "store" {
		return "", ErrUnavailable
	}
	id, err := backupasset.NewOpaqueID()
	if err != nil {
		return "", err
	}
	row := model.BackupAssetExportReservation{
		ID: id, BucketID: bucketID, JobID: &request.JobID, Kind: kind,
		LeaseOwner:     request.JobID,
		LeaseExpiresAt: request.ExpiresAt.UTC(), State: "active", CreatedAt: now, UpdatedAt: now,
	}
	if kind == "job" {
		row.ReservedSlots = 1
	} else {
		row.ReservedStoreBytes = request.StoreBytes
	}
	if err := tx.Create(&row).Error; err != nil {
		return "", err
	}
	return id, nil
}

func reserveAttemptWorkerCapacityTx(
	tx *gorm.DB,
	buckets quotaBucketPair,
	job model.BackupAssetExportJob,
	attempt model.BackupAssetExportAttempt,
	limits WorkerCapacityLimits,
	now time.Time,
) error {
	if tx == nil || !validWorkerCapacityLimits(limits) || job.ID == "" || attempt.ID == "" || attempt.JobID != job.ID ||
		attempt.LeaseExpiresAt.IsZero() || !attempt.LeaseExpiresAt.After(now) {
		return ErrAttemptNotClaimable
	}
	for _, target := range []struct {
		bucket model.BackupAssetExportQuotaBucket
		limit  int64
	}{
		{bucket: buckets.Global, limit: limits.WorkerConcurrency},
		{bucket: buckets.User, limit: limits.userWorkerLimit()},
	} {
		result := tx.Model(&model.BackupAssetExportQuotaBucket{}).
			Where("id = ? AND active_workers < ?", target.bucket.ID, target.limit).
			Updates(map[string]any{
				"active_workers":      gorm.Expr("active_workers + 1"),
				"transition_revision": gorm.Expr("transition_revision + 1"),
				"updated_at":          now,
			})
		if result.Error != nil {
			return fmt.Errorf("reserve export worker quota bucket: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return ErrAttemptNotClaimable
		}
		id, err := backupasset.NewOpaqueID()
		if err != nil {
			return err
		}
		jobID, attemptID := job.ID, attempt.ID
		row := model.BackupAssetExportReservation{
			ID: id, BucketID: target.bucket.ID, JobID: &jobID, AttemptID: &attemptID,
			Kind: "worker", ReservedSlots: 1, LeaseOwner: attempt.ID,
			LeaseExpiresAt: attempt.LeaseExpiresAt.UTC(), State: "active", CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.Create(&row).Error; err != nil {
			return fmt.Errorf("create export worker reservation: %w", err)
		}
	}
	return nil
}

func lockAttemptWorkerReservationPairTx(
	tx *gorm.DB,
	buckets quotaBucketPair,
	job model.BackupAssetExportJob,
	attempt model.BackupAssetExportAttempt,
) ([]model.BackupAssetExportReservation, error) {
	if tx == nil || job.ID == "" || attempt.ID == "" || attempt.JobID != job.ID || attempt.LeaseExpiresAt.IsZero() {
		return nil, ErrAttemptFenceLost
	}
	rows := make([]model.BackupAssetExportReservation, 0, 2)
	for _, bucket := range []model.BackupAssetExportQuotaBucket{buckets.Global, buckets.User} {
		var matches []model.BackupAssetExportReservation
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("bucket_id = ? AND job_id = ? AND attempt_id = ? AND kind = ?", bucket.ID, job.ID, attempt.ID, "worker").
			Limit(2).Find(&matches)
		if result.Error != nil {
			return nil, fmt.Errorf("lock export worker reservation: %w", result.Error)
		}
		if len(matches) != 1 {
			return nil, ErrAttemptFenceLost
		}
		row := matches[0]
		if row.State != "active" || row.LeaseOwner != attempt.ID || row.ReservedSlots != 1 ||
			row.ReservedLogicalBytes != 0 || row.ReservedProviderBytes != 0 || row.ReservedCipherBytes != 0 ||
			row.ReservedStoreBytes != 0 || !row.LeaseExpiresAt.UTC().Equal(attempt.LeaseExpiresAt.UTC()) {
			return nil, ErrAttemptFenceLost
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func renewAttemptWorkerReservationPairTx(
	tx *gorm.DB,
	rows []model.BackupAssetExportReservation,
	attempt model.BackupAssetExportAttempt,
	nextExpiry, now time.Time,
) error {
	if tx == nil || len(rows) != 2 || attempt.ID == "" || nextExpiry.IsZero() || !nextExpiry.After(now) {
		return ErrAttemptFenceLost
	}
	for _, row := range rows {
		result := tx.Model(&model.BackupAssetExportReservation{}).
			Where("id = ? AND state = ? AND lease_owner = ? AND lease_expires_at = ?", row.ID, "active", attempt.ID, row.LeaseExpiresAt).
			Updates(map[string]any{"lease_expires_at": nextExpiry.UTC(), "updated_at": now})
		if result.Error != nil {
			return fmt.Errorf("renew export worker reservation: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return ErrAttemptFenceLost
		}
	}
	return nil
}

func releaseAttemptWorkerReservationPairTx(
	tx *gorm.DB,
	rows []model.BackupAssetExportReservation,
	attempt model.BackupAssetExportAttempt,
	now time.Time,
) error {
	if tx == nil || len(rows) != 2 || attempt.ID == "" {
		return ErrAttemptFenceLost
	}
	for _, row := range rows {
		result := tx.Model(&model.BackupAssetExportQuotaBucket{}).
			Where("id = ? AND active_workers >= ?", row.BucketID, row.ReservedSlots).
			Updates(map[string]any{
				"active_workers":      gorm.Expr("active_workers - ?", row.ReservedSlots),
				"transition_revision": gorm.Expr("transition_revision + 1"),
				"updated_at":          now,
			})
		if result.Error != nil {
			return fmt.Errorf("release export worker quota bucket: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return ErrAttemptFenceLost
		}
		releasedAt := now
		result = tx.Model(&model.BackupAssetExportReservation{}).
			Where("id = ? AND state = ? AND lease_owner = ? AND lease_expires_at = ?", row.ID, "active", attempt.ID, row.LeaseExpiresAt).
			Updates(map[string]any{"state": "released", "released_at": releasedAt, "updated_at": now})
		if result.Error != nil {
			return fmt.Errorf("release export worker reservation: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return ErrAttemptFenceLost
		}
	}
	return nil
}
