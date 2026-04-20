package nodelogs

import (
	"context"
	"strconv"
	"sync"
	"time"

	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"
	"xirang/backend/internal/settings"

	"gorm.io/gorm"
)

type RetentionWorker struct {
	db   *gorm.DB
	tick time.Duration
}

func NewRetentionWorker(db *gorm.DB) *RetentionWorker {
	return &RetentionWorker{db: db, tick: time.Hour}
}

// settingsSvc 模块级设置服务引用，由 InitSettings 注入
var (
	settingsSvc    *settings.Service
	settingsInitMu sync.Mutex
)

// InitSettings 注入设置服务（在 main 中调用）
func InitSettings(svc *settings.Service) {
	settingsInitMu.Lock()
	settingsSvc = svc
	settingsInitMu.Unlock()
}

func getSettingsSvc() *settings.Service {
	settingsInitMu.Lock()
	svc := settingsSvc
	settingsInitMu.Unlock()
	return svc
}

// defaultDaysFromSettings returns the global default retention days.
// Falls back to DefaultRetentionDays when service not injected or value invalid.
var defaultDaysFromSettings = func(_ *gorm.DB) int {
	svc := getSettingsSvc()
	if svc == nil {
		return DefaultRetentionDays
	}
	v := svc.GetEffective("logs.retention_days_default")
	if v == "" {
		return DefaultRetentionDays
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return DefaultRetentionDays
	}
	return n
}

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
	// Orphan cleanup not needed: ON DELETE CASCADE on node_logs (migration 000039) handles it.
}

func (r *RetentionWorker) pruneNode(node model.Node, defaultDays int) {
	days := node.LogRetentionDays
	if days <= 0 {
		days = defaultDays
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -days)
	res := r.db.Where("node_id = ? AND created_at < ?", node.ID, cutoff).Delete(&model.NodeLog{})
	if res.RowsAffected > 0 {
		retentionDeleted.WithLabelValues(nodeIDLabel(node.ID)).Add(float64(res.RowsAffected))
	}
}
