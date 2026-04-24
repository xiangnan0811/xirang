package alerting

import (
	"context"
	"time"

	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

// SilenceRetentionWorker hard-deletes silences whose ends_at is older than
// the configured grace window. Expired silences remain visible for a grace
// period so operators can audit "what was silenced last week"; after that
// they're dropped to keep the table bounded.
//
// Paired with idx_silences_cleanup (migrations/000036). Without this worker
// the index exists but is never used — silences table grows unbounded.
type SilenceRetentionWorker struct {
	db        *gorm.DB
	tick      time.Duration
	graceDays int
}

// DefaultSilenceRetentionGraceDays is the fallback keep-expired window.
const DefaultSilenceRetentionGraceDays = 30

// NewSilenceRetentionWorker creates a worker running every 6 hours by default.
func NewSilenceRetentionWorker(db *gorm.DB) *SilenceRetentionWorker {
	return &SilenceRetentionWorker{
		db:        db,
		tick:      6 * time.Hour,
		graceDays: DefaultSilenceRetentionGraceDays,
	}
}

// SetTickInterval overrides for tests.
func (w *SilenceRetentionWorker) SetTickInterval(d time.Duration) { w.tick = d }

// SetGraceDays overrides the expired-silence keep window.
func (w *SilenceRetentionWorker) SetGraceDays(days int) {
	if days > 0 {
		w.graceDays = days
	}
}

// Run drives the worker until ctx cancels.
func (w *SilenceRetentionWorker) Run(ctx context.Context) {
	t := time.NewTicker(w.tick)
	defer t.Stop()
	// Initial pass on startup so stale data from a long-stopped deployment
	// is cleaned immediately rather than after the first tick.
	w.Prune(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.Prune(ctx)
		}
	}
}

// Prune runs one retention pass. Exposed for tests.
func (w *SilenceRetentionWorker) Prune(ctx context.Context) {
	cutoff := time.Now().UTC().Add(-time.Duration(w.graceDays) * 24 * time.Hour)
	res := w.db.WithContext(ctx).
		Where("ends_at < ?", cutoff).
		Delete(&model.Silence{})
	if res.Error != nil {
		logger.Module("alerting").Warn().Err(res.Error).Msg("silence retention prune failed")
		return
	}
	if res.RowsAffected > 0 {
		logger.Module("alerting").Info().
			Int64("rows", res.RowsAffected).
			Int("grace_days", w.graceDays).
			Msg("silence retention pruned expired silences")
	}
}
