package alerting

import (
	"context"
	"errors"
	"sync"
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

func init() {
	if maxAttempts != len(backoffTable) {
		panic("alerting: maxAttempts must equal len(backoffTable)")
	}
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
type RetryWorker struct {
	mu     sync.Mutex
	db     *gorm.DB
	sendFn func(integration model.Integration, alert model.Alert) error
}

// NewRetryWorker 创建 RetryWorker，默认使用生产发送函数。
func NewRetryWorker(db *gorm.DB) *RetryWorker {
	return &RetryWorker{db: db, sendFn: dispatchSingle}
}

// Run 启动后台重试循环，每 10 秒扫描一次，直到 ctx 取消。
func (w *RetryWorker) Run(ctx context.Context) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			w.tick(now)
		}
	}
}

func (w *RetryWorker) tick(now time.Time) {
	var rows []model.AlertDelivery
	w.db.Where("status = ? AND next_retry_at <= ?", "retrying", now).Find(&rows)
	for _, d := range rows {
		w.attempt(d)
	}
}

func (w *RetryWorker) attempt(d model.AlertDelivery) {
	w.mu.Lock()
	defer w.mu.Unlock()

	var integ model.Integration
	if err := w.db.First(&integ, d.IntegrationID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			d.Status = "failed"
			d.NextRetryAt = nil
			d.LastError = "integration deleted"
			d.AttemptCount++
			w.db.Save(&d)
			logger.Module("alerting").Warn().
				Uint("delivery_id", d.ID).
				Uint("integration_id", d.IntegrationID).
				Msg("integration 已删除，投递标记为 failed")
		} else {
			logger.Module("alerting").Warn().
				Uint("delivery_id", d.ID).Err(err).
				Msg("读取 integration 失败，跳过本次重试")
		}
		return
	}
	var alert model.Alert
	if err := w.db.First(&alert, d.AlertID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			d.Status = "failed"
			d.NextRetryAt = nil
			d.LastError = "alert deleted"
			d.AttemptCount++
			w.db.Save(&d)
			logger.Module("alerting").Warn().
				Uint("delivery_id", d.ID).
				Uint("alert_id", d.AlertID).
				Msg("alert 已删除，投递标记为 failed")
		} else {
			logger.Module("alerting").Warn().
				Uint("delivery_id", d.ID).Err(err).
				Msg("读取 alert 失败，跳过本次重试")
		}
		return
	}
	err := w.sendFn(integ, alert)
	d.AttemptCount++
	if err == nil {
		d.Status = "sent"
		d.NextRetryAt = nil
		d.LastError = ""
	} else if d.AttemptCount >= maxAttempts {
		d.Status = "failed"
		d.NextRetryAt = nil
		d.LastError = err.Error()
		logger.Module("alerting").Warn().
			Uint("delivery_id", d.ID).
			Err(err).
			Msg("告警投递重试达到上限，终止")
	} else {
		next := time.Now().Add(backoffDuration(d.AttemptCount))
		d.Status = "retrying"
		d.NextRetryAt = &next
		d.LastError = err.Error()
	}
	w.db.Save(&d)
}

// ManualRetry 立即强制重试指定投递记录，绕过 NextRetryAt 调度。供管理员 API 调用。
func (w *RetryWorker) ManualRetry(deliveryID uint) error {
	var d model.AlertDelivery
	if err := w.db.First(&d, deliveryID).Error; err != nil {
		return err
	}
	if d.Status == "sent" {
		return errors.New("already sent")
	}
	w.attempt(d)
	return nil
}

// dispatchSingle 是生产路径的适配器：将 (Integration, Alert) 路由到 dispatcher.go 中的
// send() 函数（按 integration.Type 分发到各通道发送器）。Task 5 将把此路径与
// dispatcher 的完整发送逻辑统一。
func dispatchSingle(integ model.Integration, alert model.Alert) error {
	return send(integ, alert)
}
