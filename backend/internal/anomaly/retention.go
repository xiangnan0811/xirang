package anomaly

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

// RetentionWorker prunes anomaly_events older than the configured threshold.
type RetentionWorker struct {
	db       *gorm.DB
	settings *settings.Service
	tick     time.Duration
	done     chan struct{}
}

func NewRetentionWorker(db *gorm.DB, s *settings.Service) *RetentionWorker {
	return &RetentionWorker{db: db, settings: s, tick: 6 * time.Hour, done: make(chan struct{})}
}

// SetTickInterval overrides for tests.
func (w *RetentionWorker) SetTickInterval(d time.Duration) { w.tick = d }

// Run blocks until ctx is cancelled, pruning on each tick.
// Implements lifecycle.Worker.
func (w *RetentionWorker) Run(ctx context.Context) {
	defer close(w.done)
	t := time.NewTicker(w.tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.prune(ctx)
		}
	}
}

// Shutdown blocks until Run returns or ctx expires. Implements lifecycle.Worker.
func (w *RetentionWorker) Shutdown(ctx context.Context) error {
	select {
	case <-w.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Prune runs one retention pass (exposed for tests).
func (w *RetentionWorker) Prune(ctx context.Context) { w.prune(ctx) }

func (w *RetentionWorker) prune(ctx context.Context) {
	days := w.retentionDays()
	if days <= 0 {
		return
	}
	cutoff := time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour)
	res := w.db.WithContext(ctx).
		Where("fired_at < ?", cutoff).
		Delete(&model.AnomalyEvent{})
	if res.Error != nil {
		logger.Module("anomaly").Warn().Err(res.Error).Msg("retention prune failed")
		return
	}
	if res.RowsAffected > 0 {
		logger.Module("anomaly").Info().
			Int64("rows", res.RowsAffected).
			Int("days", days).
			Msg("retention pruned anomaly_events")
	}
}

func (w *RetentionWorker) retentionDays() int {
	v := strings.TrimSpace(w.settings.GetEffective("anomaly.events_retention_days"))
	if v == "" {
		return 30
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 30
	}
	return n
}
