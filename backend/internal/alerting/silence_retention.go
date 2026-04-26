package alerting

import (
	"context"
	"strconv"
	"strings"
	"time"

	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"
	"xirang/backend/internal/settings"

	"gorm.io/gorm"
)

// SilenceRetentionWorker hard-deletes silences whose ends_at is older than
// the configured grace window. Expired silences remain visible for a grace
// period so operators can audit "what was silenced last week"; after that
// they're dropped to keep the table bounded.
//
// Paired with idx_silences_cleanup (migrations/000036). Without this worker
// the index exists but is never used — silences table grows unbounded.
//
// Grace days come from `alerts.silence_retention_days` via settings.Service
// (three-tier resolution: DB → env → code default), matching the convention
// anomaly retention already uses. A stored default of 30 days means day-to-
// day ops can tune without a deploy.
type SilenceRetentionWorker struct {
	db       *gorm.DB
	settings *settings.Service
	tick     time.Duration
	done     chan struct{}
}

// DefaultSilenceRetentionGraceDays is the fallback when settings lookup fails
// or the configured value is invalid. Kept in sync with the registry entry.
const DefaultSilenceRetentionGraceDays = 30

const silenceRetentionKey = "alerts.silence_retention_days"

// NewSilenceRetentionWorker creates a worker running every 6 hours by default.
// settings may be nil (e.g. in tests) — grace_days falls back to the default.
func NewSilenceRetentionWorker(db *gorm.DB, s *settings.Service) *SilenceRetentionWorker {
	return &SilenceRetentionWorker{db: db, settings: s, tick: 6 * time.Hour, done: make(chan struct{})}
}

// SetTickInterval overrides for tests.
func (w *SilenceRetentionWorker) SetTickInterval(d time.Duration) { w.tick = d }

// Run drives the worker until ctx cancels.
func (w *SilenceRetentionWorker) Run(ctx context.Context) {
	t := time.NewTicker(w.tick)
	defer close(w.done) // fires last - terminal signal after all other cleanup
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

// Shutdown blocks until Run has returned or ctx expires.
// Run MUST be called before Shutdown; safe to call after Run has already returned.
func (w *SilenceRetentionWorker) Shutdown(ctx context.Context) error {
	select {
	case <-w.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Prune runs one retention pass. Exposed for tests.
func (w *SilenceRetentionWorker) Prune(ctx context.Context) {
	days := w.graceDays()
	cutoff := time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour)
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
			Int("grace_days", days).
			Msg("silence retention pruned expired silences")
	}
}

// graceDays resolves the current grace window. Invalid or missing config
// falls back to DefaultSilenceRetentionGraceDays so we never prune to zero.
func (w *SilenceRetentionWorker) graceDays() int {
	if w.settings == nil {
		return DefaultSilenceRetentionGraceDays
	}
	v := strings.TrimSpace(w.settings.GetEffective(silenceRetentionKey))
	if v == "" {
		return DefaultSilenceRetentionGraceDays
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return DefaultSilenceRetentionGraceDays
	}
	return n
}
