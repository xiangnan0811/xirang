package nodelogs

import (
	"context"
	"time"

	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

type RetentionWorker struct {
	db   *gorm.DB
	tick time.Duration
}

func NewRetentionWorker(db *gorm.DB) *RetentionWorker {
	return &RetentionWorker{db: db, tick: time.Hour}
}

// defaultDaysFromSettings returns the global default retention days.
// Stubbed to DefaultRetentionDays here; main.go (Task 8) re-points this
// to the real system_settings service.
var defaultDaysFromSettings = func(_ *gorm.DB) int { return DefaultRetentionDays }

func (r *RetentionWorker) Run(ctx context.Context) {
	t := time.NewTicker(r.tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.pruneAll()
		}
	}
}

func (r *RetentionWorker) pruneAll() {
	defaultDays := defaultDaysFromSettings(r.db)
	var nodes []model.Node
	if err := r.db.Find(&nodes).Error; err != nil {
		logger.Module("nodelogs").Warn().Err(err).Msg("retention: load nodes failed")
		return
	}
	for _, n := range nodes {
		r.pruneNode(n, defaultDays)
	}
	// Orphan cleanup (defensive; ON DELETE CASCADE normally handles this).
	r.db.Exec("DELETE FROM node_logs WHERE node_id NOT IN (SELECT id FROM nodes)")
}

func (r *RetentionWorker) pruneNode(node model.Node, defaultDays int) {
	days := node.LogRetentionDays
	if days <= 0 {
		days = defaultDays
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -days)
	r.db.Where("node_id = ? AND created_at < ?", node.ID, cutoff).Delete(&model.NodeLog{})
}
