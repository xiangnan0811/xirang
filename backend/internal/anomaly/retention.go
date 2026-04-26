package anomaly

import (
	"context"
	"strconv"
	"strings"
	"time"

	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"
	"xirang/backend/internal/retention"
	"xirang/backend/internal/settings"

	"gorm.io/gorm"
)

// RetentionWorker prunes anomaly_events older than the configured threshold.
// Embeds retention.Loop for the standard ticker + Shutdown scaffold.
type RetentionWorker struct {
	*retention.Loop
	db       *gorm.DB
	settings *settings.Service
}

// NewRetentionWorker constructs the worker with a 6-hour default tick.
// Test code can override the tick by reassigning w.Loop.Tick before Run.
func NewRetentionWorker(db *gorm.DB, s *settings.Service) *RetentionWorker {
	w := &RetentionWorker{db: db, settings: s}
	w.Loop = &retention.Loop{
		Tick:   6 * time.Hour,
		Pruner: w.prune,
	}
	return w
}

// SetTickInterval overrides the tick for tests. Must be called before Run;
// Loop reads Tick once at startup and a later mutation has no effect on the
// live ticker.
func (w *RetentionWorker) SetTickInterval(d time.Duration) { w.Tick = d }

// Prune runs one retention pass synchronously. Implements retention.Worker.
func (w *RetentionWorker) Prune(ctx context.Context) (int64, error) {
	return w.prune(ctx)
}

func (w *RetentionWorker) prune(ctx context.Context) (int64, error) {
	days := w.retentionDays()
	if days <= 0 {
		return 0, nil
	}
	cutoff := time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour)
	res := w.db.WithContext(ctx).
		Where("fired_at < ?", cutoff).
		Delete(&model.AnomalyEvent{})
	if res.Error != nil {
		logger.Module("anomaly").Warn().Err(res.Error).Msg("retention prune failed")
		return 0, res.Error
	}
	if res.RowsAffected > 0 {
		logger.Module("anomaly").Info().
			Int64("rows", res.RowsAffected).
			Int("days", days).
			Msg("retention pruned anomaly_events")
	}
	return res.RowsAffected, nil
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
