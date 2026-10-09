package alerting

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"xirang/backend/internal/cronbackup"
	"xirang/backend/internal/lifecycle"
	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	cronBackupTickInterval = time.Minute
	cronBackupDBTimeout    = 5 * time.Second
	cronBackupUsageID      = 1
)

var _ lifecycle.Worker = (*CronBackupWorker)(nil)

// CronBackupWorker reconciles one cron backup state source into a durable
// alert cursor. It deliberately persists the alert boundary directly inside
// the cursor transaction: the normal RetryWorker owns all delivery and network
// work after that transaction commits.
type CronBackupWorker struct {
	db              *gorm.DB
	dispatcher      *Dispatcher
	cfg             cronbackup.Config
	sourceKey       string
	enabled         bool
	clock           func() time.Time
	enrollmentClock func() time.Time
	done            chan struct{}
}

// NewCronBackupWorker constructs the cron backup health worker. An empty state
// directory is a deliberate opt-out and produces a worker that does no
// enrollment, file access, or database writes.
func NewCronBackupWorker(db *gorm.DB, dispatcher *Dispatcher, cfg cronbackup.Config) *CronBackupWorker {
	w := &CronBackupWorker{
		db: db, dispatcher: dispatcher, cfg: cfg,
		clock: time.Now, enrollmentClock: time.Now, done: make(chan struct{}),
	}
	if cronBackupConfigEnabled(cfg) {
		w.enabled = true
		w.sourceKey = cronBackupSourceKey(cfg)
	}
	return w
}

// Run performs one reconciliation immediately and then serially once per
// minute. There is intentionally no overlapping ticker goroutine: a slow tick
// delays the next tick rather than racing a second state/DB transaction.
func (w *CronBackupWorker) Run(ctx context.Context) {
	defer close(w.done)
	if ctx == nil {
		ctx = context.Background()
	}
	if !w.enabled || w.db == nil || ctx.Err() != nil {
		return
	}

	w.tick(ctx)
	ticker := time.NewTicker(cronBackupTickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.tick(ctx)
		}
	}
}

// Shutdown waits for Run to finish. Run must be started before Shutdown, in
// accordance with lifecycle.Worker; a nil stop context is treated as never
// expiring for parity with the other lifecycle workers.
func (w *CronBackupWorker) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-w.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *CronBackupWorker) tick(ctx context.Context) {
	clock := w.clock
	if clock == nil {
		clock = time.Now
	}
	enrollmentClock := w.enrollmentClock
	if enrollmentClock == nil {
		enrollmentClock = time.Now
	}
	w.tickWithClocks(ctx, clock, enrollmentClock)
}

func (w *CronBackupWorker) tickWithClocks(ctx context.Context, clock, enrollmentClock func() time.Time) {
	if !w.enabled || w.db == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return
	}

	// Enrollment intentionally precedes the file lock. The first enrollment
	// transaction also writes the migration usage marker, so a crash cannot
	// leave a cursor without the schema's downgrade fence. This timestamp is
	// only the enrollment anchor; classification and checked_at come from the
	// locked observation below. The observation clock is not sampled here.
	enrollmentNow := cronBackupWorkerNow(enrollmentClock)
	if err := w.enroll(ctx, enrollmentNow); err != nil {
		if ctx.Err() != nil {
			return
		}
		logger.Module("alerting").Warn().Msg("cron backup health enrollment unavailable")
		return
	}

	// WithLockedObservation owns state.lock for the complete callback and reads
	// state.json/run.lock after acquiring it. Never use a pre-lock Read result:
	// the callback snapshot is the only observation allowed to reach the DB.
	err := cronbackup.WithLockedObservation(ctx, w.cfg, clock, func(observation cronbackup.Observation) error {
		return w.reconcile(ctx, observation)
	})
	if err == nil || ctx.Err() != nil {
		return
	}
	if errors.Is(err, cronbackup.ErrStateUnavailable) || errors.Is(err, cronbackup.ErrNotInitialized) {
		// A lock/I/O boundary is not evidence of health or failure. In
		// particular, do not resolve a cursor from a stale observation.
		logger.Module("alerting").Warn().Msg("cron backup state unavailable")
		return
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return
	}
	logger.Module("alerting").Warn().Msg("cron backup health reconciliation unavailable")
}

func cronBackupWorkerNow(clock func() time.Time) time.Time {
	if clock == nil {
		return time.Now().UTC()
	}
	now := clock().UTC()
	if now.IsZero() {
		return time.Now().UTC()
	}
	return now
}

func (w *CronBackupWorker) enroll(ctx context.Context, now time.Time) error {
	dbCtx, cancel := context.WithTimeout(ctx, cronBackupDBTimeout)
	defer cancel()
	now = now.UTC()
	return w.db.WithContext(dbCtx).Transaction(func(tx *gorm.DB) error {
		// Both rows are protected by one transaction. OnConflict keeps two Core
		// instances (or two engines sharing a database) from turning a retry into
		// an enrollment error, while never refreshing an existing enrolled_at.
		usage := model.CronBackupHealthUsage{ID: cronBackupUsageID}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&usage).Error; err != nil {
			return err
		}
		cursor := model.CronBackupHealth{
			SourceKey:       w.sourceKey,
			SourceID:        "",
			HighestRevision: 0,
			EnrolledAt:      now,
			FaultActive:     false,
			AlertID:         nil,
			UpdatedAt:       now,
		}
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&cursor).Error
	})
}

func (w *CronBackupWorker) reconcile(ctx context.Context, observation cronbackup.Observation) error {
	dbCtx, cancel := context.WithTimeout(ctx, cronBackupDBTimeout)
	defer cancel()
	now := observation.Job.CheckedAt.UTC()
	return w.db.WithContext(dbCtx).Transaction(func(tx *gorm.DB) error {
		// Application SQLite connections use _txlock=immediate. The marker
		// no-op write below also promotes a custom/deferred test connection
		// before any cursor decision; SQLite then has one writer boundary for
		// the complete source-row reconciliation.
		if strings.EqualFold(tx.Name(), "sqlite") {
			if result := tx.Model(&model.CronBackupHealthUsage{}).
				Where("id = ?", cronBackupUsageID).
				UpdateColumn("id", gorm.Expr("id")); result.Error != nil {
				return result.Error
			} else if result.RowsAffected != 1 {
				return gorm.ErrRecordNotFound
			}
		}

		query := tx.WithContext(dbCtx)
		if strings.EqualFold(tx.Name(), "postgres") {
			query = query.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		var cursor model.CronBackupHealth
		if err := query.Where("source_key = ?", w.sourceKey).Take(&cursor).Error; err != nil {
			return err
		}
		// SQLite has no row-level FOR UPDATE. A no-op source update makes the
		// actual cursor row the write lock boundary after the marker promotes
		// the transaction; PostgreSQL uses FOR UPDATE above.
		if strings.EqualFold(tx.Name(), "sqlite") {
			if err := tx.Model(&model.CronBackupHealth{}).
				Where("source_key = ?", w.sourceKey).
				UpdateColumn("updated_at", gorm.Expr("updated_at")).Error; err != nil {
				return err
			}
		}
		return reconcileCronBackupCursor(tx, &cursor, w.cfg, now, observation)
	})
}

func reconcileCronBackupCursor(tx *gorm.DB, cursor *model.CronBackupHealth, cfg cronbackup.Config, now time.Time, observation cronbackup.Observation) error {
	if tx == nil || cursor == nil {
		return errors.New("cron backup health: nil transaction or cursor")
	}

	status := observation.Job.Status
	// state.lock was acquired, but the state snapshot itself may still be
	// unavailable (for example, a run.lock probe failed). Preserve the complete
	// fault cycle and high-water mark until a trusted snapshot is available.
	if status == cronbackup.JobStatusStateUnavailable {
		return nil
	}

	stateUsable := observation.StatePresent() && observation.StateValid()
	fenced := false
	if stateUsable {
		if cursor.SourceID != "" && observation.SourceID() != cursor.SourceID {
			fenced = true
		}
		if cursor.HighestRevision > observation.Revision() {
			fenced = true
		}
	}

	changed := false
	if stateUsable && !fenced {
		if cursor.SourceID == "" {
			cursor.SourceID = observation.SourceID()
			changed = true
		}
		if observation.Revision() > cursor.HighestRevision {
			cursor.HighestRevision = observation.Revision()
			changed = true
		}
	}

	fault, faultKind := cronBackupFault(cursor, cfg, now, observation, fenced)
	if fault {
		if !cursor.FaultActive {
			alert := cronBackupAlert(cfg.Engine, cursor.SourceKey, faultKind, now)
			if err := tx.Create(&alert).Error; err != nil {
				return err
			}
			cursor.FaultActive = true
			cursor.AlertID = &alert.ID
			changed = true
		} else if cursor.AlertID == nil {
			// The v92 CHECK constraint forbids this. Fail closed if a test or
			// manual repair presents an inconsistent cursor rather than creating
			// an untracked alert cycle.
			return errors.New("cron backup health: active cursor has no alert")
		}
	} else if cronBackupHealthySuccess(observation) && cursor.FaultActive {
		if cursor.AlertID == nil {
			return errors.New("cron backup health: active cursor has no alert")
		}
		if err := resolveCronBackupAlert(tx, *cursor.AlertID, now); err != nil {
			return err
		}
		cursor.FaultActive = false
		changed = true
	}

	if !changed {
		return nil
	}
	cursor.UpdatedAt = now
	return tx.Model(&model.CronBackupHealth{}).
		Where("source_key = ?", cursor.SourceKey).
		Updates(map[string]interface{}{
			"source_id":        cursor.SourceID,
			"highest_revision": cursor.HighestRevision,
			"fault_active":     cursor.FaultActive,
			"alert_id":         cursor.AlertID,
			"updated_at":       cursor.UpdatedAt,
		}).Error
}

func cronBackupFault(cursor *model.CronBackupHealth, cfg cronbackup.Config, now time.Time, observation cronbackup.Observation, fenced bool) (bool, string) {
	if fenced {
		return true, string(cronbackup.JobStatusStateInvalid)
	}
	switch observation.Job.Status {
	case cronbackup.JobStatusStateInvalid:
		return true, string(cronbackup.JobStatusStateInvalid)
	case cronbackup.JobStatusClockAnomaly:
		return true, string(cronbackup.JobStatusClockAnomaly)
	case cronbackup.JobStatusFailed:
		return true, string(cronbackup.JobStatusFailed)
	case cronbackup.JobStatusInterrupted:
		return true, string(cronbackup.JobStatusInterrupted)
	case cronbackup.JobStatusOverdueRunning:
		return true, string(cronbackup.JobStatusOverdueRunning)
	case cronbackup.JobStatusStale:
		return true, string(cronbackup.JobStatusStale)
	case cronbackup.JobStatusNotInitialized:
		if cursor.SourceID != "" || cursor.HighestRevision > 0 {
			return true, string(cronbackup.JobStatusStateInvalid)
		}
		if !cronBackupWithinInitialGrace(cursor, observation, cfg.MaxAge, now) {
			return true, string(cronbackup.JobStatusStale)
		}
		return false, ""
	case cronbackup.JobStatusNeverRun:
		if !cronBackupWithinInitialGrace(cursor, observation, cfg.MaxAge, now) {
			return true, string(cronbackup.JobStatusStale)
		}
		return false, ""
	case cronbackup.JobStatusRunning, cronbackup.JobStatusSuccess:
		return false, ""
	default:
		// The enabled worker never expects not_configured or invalid_configuration
		// from WithLockedObservation. Treat any future status conservatively.
		return true, string(cronbackup.JobStatusStateInvalid)
	}
}

func cronBackupHealthySuccess(observation cronbackup.Observation) bool {
	return observation.Job.Status == cronbackup.JobStatusSuccess &&
		observation.Job.LastSuccess != nil &&
		observation.StatePresent() && observation.StateValid()
}

func cronBackupWithinInitialGrace(cursor *model.CronBackupHealth, observation cronbackup.Observation, maxAge time.Duration, now time.Time) bool {
	anchor := cursor.EnrolledAt
	initializedAt := observation.InitializedAt()
	if !initializedAt.IsZero() && (anchor.IsZero() || initializedAt.Before(anchor)) {
		anchor = initializedAt
	}
	if anchor.IsZero() {
		return false
	}
	return !now.After(anchor.Add(maxAge))
}

func cronBackupAlert(engine, sourceKey, faultKind string, now time.Time) model.Alert {
	return model.Alert{
		NodeID:           0,
		NodeName:         "localhost",
		Severity:         "warning",
		Status:           "open",
		ErrorCode:        "XR-CRON-DB-BACKUP-" + sourceKey,
		Message:          fmt.Sprintf("cron database backup %s job is %s", engine, faultKind),
		Retryable:        false,
		TriggeredAt:      now.UTC(),
		Tags:             "[]",
		LastLevelFired:   -1,
		DeliveryDecision: model.AlertDeliveryDecisionPending,
	}
}

func resolveCronBackupAlert(tx *gorm.DB, alertID uint, now time.Time) error {
	var alert model.Alert
	query := tx.Where("id = ?", alertID)
	if strings.EqualFold(tx.Name(), "postgres") {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := query.First(&alert).Error; err != nil {
		return err
	}
	if alert.Status != "open" && alert.Status != "acked" {
		// Manual resolution (or another terminal state) closes the cursor cycle
		// without sending a synthetic recovery notification.
		return nil
	}
	return tx.Model(&model.Alert{}).
		Where("id = ? AND status IN ?", alertID, []string{"open", "acked"}).
		Updates(map[string]interface{}{
			"status":           "resolved",
			"retryable":        false,
			"last_notified_at": now.UTC(),
		}).Error
}

func cronBackupConfigEnabled(cfg cronbackup.Config) bool {
	if cfg.StateDirectory == "" || !filepath.IsAbs(cfg.StateDirectory) || strings.IndexByte(cfg.StateDirectory, 0) >= 0 {
		return false
	}
	if cfg.Engine != "sqlite" && cfg.Engine != "postgres" {
		return false
	}
	return cfg.MaxAge >= time.Hour && cfg.MaxAge <= 8760*time.Hour && cfg.MaxAge%time.Second == 0
}

func cronBackupSourceKey(cfg cronbackup.Config) string {
	clean := filepath.Clean(cfg.StateDirectory)
	absolute, err := filepath.Abs(clean)
	if err == nil {
		clean = filepath.Clean(absolute)
	}
	digest := sha256.Sum256([]byte(clean + "\x00" + cfg.Engine))
	return hex.EncodeToString(digest[:])
}
