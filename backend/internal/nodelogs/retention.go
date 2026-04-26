package nodelogs

import (
	"context"
	"strconv"
	"sync"
	"time"

	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"
	"xirang/backend/internal/retention"
	"xirang/backend/internal/settings"

	"gorm.io/gorm"
)

// Compile-time assertion: *RetentionWorker satisfies retention.Worker.
var _ retention.Worker = (*RetentionWorker)(nil)

// RetentionWorker prunes node_logs older than each node's configured
// retention. Embeds retention.Loop for the standard ticker + Shutdown
// scaffold. Reads the global default via the module-level settings service
// (injected by InitSettings; not constructor-injected).
type RetentionWorker struct {
	*retention.Loop
	db *gorm.DB
}

// NewRetentionWorker constructs the worker with a 1-hour default tick.
// Test code can override the tick by reassigning w.Loop.Tick before Run.
func NewRetentionWorker(db *gorm.DB) *RetentionWorker {
	w := &RetentionWorker{db: db}
	w.Loop = &retention.Loop{
		Tick:   time.Hour,
		Pruner: w.prune,
	}
	return w
}

// SetTickInterval overrides the tick for tests. Must be called before Run;
// Loop reads Tick once at startup and a later mutation has no effect on the
// live ticker.
func (w *RetentionWorker) SetTickInterval(d time.Duration) { w.Loop.Tick = d }

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

// Prune runs one retention pass synchronously across all nodes.
// Implements retention.Worker.
func (w *RetentionWorker) Prune(ctx context.Context) (int64, error) {
	return w.prune(ctx)
}

// prune is the Pruner closure for retention.Loop. Iterates all nodes,
// applies per-node retention, returns the total rows deleted across nodes.
func (w *RetentionWorker) prune(ctx context.Context) (int64, error) {
	defaultDays := defaultDaysFromSettings(w.db)
	var nodes []model.Node
	if err := w.db.WithContext(ctx).Find(&nodes).Error; err != nil {
		logger.Module("nodelogs").Warn().Err(err).Msg("retention: load nodes failed")
		return 0, err
	}
	var total int64
	for _, n := range nodes {
		total += w.pruneNode(n, defaultDays)
	}
	// Orphan cleanup not needed: ON DELETE CASCADE on node_logs (migration 000039) handles it.
	return total, nil
}

// pruneNode deletes rows older than node-specific or default days.
// Returns rows affected (so prune can sum across nodes).
func (w *RetentionWorker) pruneNode(node model.Node, defaultDays int) int64 {
	days := node.LogRetentionDays
	if days <= 0 {
		days = defaultDays
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -days)
	res := w.db.Where("node_id = ? AND created_at < ?", node.ID, cutoff).Delete(&model.NodeLog{})
	if res.RowsAffected > 0 {
		retentionDeleted.WithLabelValues(nodeIDLabel(node.ID)).Add(float64(res.RowsAffected))
	}
	return res.RowsAffected
}
