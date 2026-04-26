package task

import (
	"context"
	"strconv"
	"sync"
	"time"

	"xirang/backend/internal/logger"
	"xirang/backend/internal/retention"
	"xirang/backend/internal/settings"
	"xirang/backend/internal/util"
)

// Compile-time assertion: *RetentionWorker satisfies retention.Worker.
var _ retention.Worker = (*RetentionWorker)(nil)

// RetentionWorker runs the task package's retention/integrity/expiry loop.
// Extracted from Manager.runRetentionWorker so it can satisfy lifecycle.Worker
// uniformly with the other retention workers, while keeping its multi-cadence
// shape (retention/storage/expiry every tick; integrity every Nth tick) that
// prevented embedding retention.Loop.
//
// The worker holds a *Manager pointer because the actual cleanup methods
// (enforceRetention, checkLocalStorageSpace, checkNodeExpiry, checkIntegrity)
// touch Manager state that lives outside this struct's concern.
type RetentionWorker struct {
	settingsSvc *settings.Service
	manager     *Manager

	mu   sync.Mutex
	done chan struct{}
}

// NewRetentionWorker constructs a worker. m must be non-nil; Run will call
// the four cleanup methods on it.
func NewRetentionWorker(settingsSvc *settings.Service, m *Manager) *RetentionWorker {
	return &RetentionWorker{
		settingsSvc: settingsSvc,
		manager:     m,
	}
}

// Run drives the multi-cadence retention loop until ctx is done. Implements
// lifecycle.Worker.
func (w *RetentionWorker) Run(ctx context.Context) {
	w.mu.Lock()
	w.done = make(chan struct{})
	done := w.done
	w.mu.Unlock()
	defer close(done)

	integrityMultiplier := 4
	if raw := util.GetEnvOrDefault("INTEGRITY_CHECK_MULTIPLIER", ""); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			integrityMultiplier = v
		}
	}

	var tickCount int
	for {
		// 每次循环动态读取间隔配置
		interval := 6 * time.Hour
		if w.settingsSvc != nil {
			raw := w.settingsSvc.GetEffective("retention.check_interval")
			if parsed, err := time.ParseDuration(raw); err == nil && parsed >= 1*time.Minute {
				interval = parsed
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
			tickCount++
			w.manager.enforceRetention()
			w.manager.checkLocalStorageSpace()
			w.manager.checkNodeExpiry()
			// 完整性检查频率低于保留清理（默认每 4 个周期运行一次，即默认间隔 6h 时每 24h 一次）
			if tickCount%integrityMultiplier == 0 {
				w.manager.checkIntegrity()
			}
		}
	}
}

// Shutdown blocks until Run returns or ctx expires. Implements lifecycle.Worker.
// If Run was never started, returns nil immediately (mirrors retention.Loop).
func (w *RetentionWorker) Shutdown(ctx context.Context) error {
	w.mu.Lock()
	done := w.done
	w.mu.Unlock()
	if done == nil {
		return nil // never started
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Prune runs one synchronous pass through every cadence (retention,
// storage, expiry, integrity). Implements retention.Worker. Returns 0 for
// rowsAffected because the underlying enforce/check methods do not surface
// row counts; admin tooling can call this to force-trigger the work
// off-tick. Errors from sub-methods are logged inside each method, not
// returned.
func (w *RetentionWorker) Prune(_ context.Context) (int64, error) {
	w.manager.enforceRetention()
	w.manager.checkLocalStorageSpace()
	w.manager.checkNodeExpiry()
	w.manager.checkIntegrity()
	logger.Module("task").Info().Msg("retention prune ran all four sub-jobs synchronously")
	return 0, nil
}
