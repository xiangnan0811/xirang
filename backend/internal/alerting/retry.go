package alerting

import (
	"context"
	"errors"
	"time"
	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

var backoffTable = []time.Duration{
	30 * time.Second,
	2 * time.Minute,
	8 * time.Minute,
	30 * time.Minute,
}

const maxAttempts = 4

// ValidateConfig verifies package-level constants are in sync.
// Call once at startup before starting RetryWorker.
func ValidateConfig() error {
	if maxAttempts != len(backoffTable) {
		return errors.New("alerting: maxAttempts must equal len(backoffTable)")
	}
	return nil
}

func backoffDuration(attempt int) time.Duration {
	if attempt < 0 {
		return backoffTable[0]
	}
	if attempt >= len(backoffTable) {
		return backoffTable[len(backoffTable)-1]
	}
	return backoffTable[attempt]
}

// RetryWorker 定期扫描 status='retrying' 的告警投递记录并重新发送。
// RetryWorker 定期扫描告警投递记录并重新发送。所有首次、自动和手动发送
// 共用 alerting/delivery.go 中的数据库租约与 CAS 状态机。
type RetryWorker struct {
	db         *gorm.DB
	sendFn     func(integration model.Integration, alert model.Alert) error
	dispatcher *Dispatcher
	done       chan struct{}
}

// NewRetryWorker 创建 RetryWorker，默认使用生产发送函数。
func NewRetryWorker(db *gorm.DB) *RetryWorker {
	dispatcher := defaultDispatcher
	if dispatcher == nil || dispatcher.DB != db {
		dispatcher = NewDispatcher(db, nil, nil)
	}
	return &RetryWorker{db: db, sendFn: dispatcher.send, dispatcher: dispatcher, done: make(chan struct{})}
}

// Run 启动后台重试循环，每 10 秒扫描一次，直到 ctx 取消。
func (w *RetryWorker) Run(ctx context.Context) {
	defer close(w.done)
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			w.tick(ctx, now)
		}
	}
}

// Shutdown blocks until Run has returned or ctx expires.
// Run MUST be called before Shutdown; safe to call after Run has already returned.
func (w *RetryWorker) Shutdown(ctx context.Context) error {
	select {
	case <-w.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *RetryWorker) tick(ctx context.Context, now time.Time) {
	if w.dispatcher == nil {
		w.dispatcher = NewDispatcher(w.db, nil, nil)
	}
	// An alert may have committed successfully just before a process crash,
	// before its first channel intents were materialized. Replay only rows
	// explicitly marked pending; historical NULL decisions remain unknown.
	var pendingAlerts []model.Alert
	if err := w.db.WithContext(ctx).
		Where("delivery_decision = ?", model.AlertDeliveryDecisionPending).
		Order("id ASC").Limit(1000).Find(&pendingAlerts).Error; err != nil {
		logger.Module("alerting").Warn().Err(err).Msg("retry tick: load pending alerts failed")
	} else {
		for i := range pendingAlerts {
			if ctx.Err() != nil {
				return
			}
			if err := w.dispatcher.dispatchCreatedAlertWithSender(&pendingAlerts[i], w.sendFn); err != nil {
				logger.Module("alerting").Warn().
					Uint("alert_id", pendingAlerts[i].ID).
					Err(err).
					Msg("retry tick: replay pending alert failed")
			}
		}
	}

	var rows []model.AlertDelivery
	if err := w.db.WithContext(ctx).
		Where(
			"(decision = ? OR decision = '' OR decision IS NULL) AND "+
				"((status = ?) OR (status = ? AND (next_retry_at IS NULL OR next_retry_at <= ?)) OR "+
				"(status = ? AND lease_expires_at IS NOT NULL AND lease_expires_at <= ?))",
			"deliver",
			model.AlertDeliveryStatusPending,
			model.AlertDeliveryStatusRetrying, now,
			model.AlertDeliveryStatusSending, now,
		).
		Order("id ASC").Limit(1000).Find(&rows).Error; err != nil {
		logger.Module("alerting").Warn().Err(err).Msg("retry tick: load deliveries failed")
		return
	}
	for _, d := range rows {
		if ctx.Err() != nil {
			return
		}
		w.attempt(ctx, d)
	}
}

// attempt claims the row before loading dependent records or sending. A stale
// snapshot from tick is only an ID hint; the durable UPDATE predicate decides
// whether this invocation is still allowed to send.
func (w *RetryWorker) attempt(ctx context.Context, d model.AlertDelivery) {
	if err := runDeliveryAttempt(ctx, w.db, d, w.sendFn, false); err != nil {
		logger.Module("alerting").Warn().
			Uint("delivery_id", d.ID).
			Err(err).
			Msg("retry attempt failed")
	}
}

// ManualRetry immediately forces one attempt for a delivery, bypassing
// NextRetryAt but not the sending lease. It uses the same atomic claim and CAS
// completion path as automatic and initial delivery.
func (w *RetryWorker) ManualRetry(deliveryID uint) error {
	var d model.AlertDelivery
	if err := w.db.First(&d, deliveryID).Error; err != nil {
		return err
	}
	if d.Status == model.AlertDeliveryStatusSent {
		return errors.New("already sent")
	}
	return runDeliveryAttempt(context.Background(), w.db, d, w.sendFn, true)
}

// dispatchSingle 是生产路径的适配器：将 (Integration, Alert) 路由到 dispatcher.go 中的
// send() 函数（按 integration.Type 分发到各通道发送器）。
func dispatchSingle(integ model.Integration, alert model.Alert) error {
	return send(integ, alert)
}
