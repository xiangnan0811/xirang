package alerting

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"strings"
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
//
// 并发约束: tick 由单 ticker 驱动, 不会自我并发。mu 仅保护 attempt 的"状态转移"
// 段（DB 读取-决策-写入），让慢速的 HTTP send() 落在锁外，避免 ManualRetry 与
// tick 的状态写入撞车。
type RetryWorker struct {
	mu     sync.Mutex
	db     *gorm.DB
	sendFn func(integration model.Integration, alert model.Alert) error
	done   chan struct{}
}

// NewRetryWorker 创建 RetryWorker，默认使用生产发送函数。
func NewRetryWorker(db *gorm.DB) *RetryWorker {
	return &RetryWorker{db: db, sendFn: dispatchSingle, done: make(chan struct{})}
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
	var rows []model.AlertDelivery
	if err := w.db.WithContext(ctx).
		Where("status = ? AND next_retry_at <= ?", "retrying", now).
		Find(&rows).Error; err != nil {
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

// attempt performs a single delivery attempt and updates the delivery row state machine.
//
// Semantics:
//   - Retries do NOT re-evaluate silences. Once an alert has been dispatched and a retry
//     is scheduled, the retry continues to terminal state (sent|failed) regardless of
//     silences created afterward. Silences suppress NEW dispatches, not pending retries.
//   - ErrRecordNotFound on integration/alert lookup → mark delivery failed with descriptive
//     LastError. Other DB errors → warn log and skip this tick (retry scheduler will try again).
//   - The mutex wraps only the state-transition section; the HTTP send is deliberately
//     unguarded so one slow channel cannot serialize the retry queue.
func (w *RetryWorker) attempt(ctx context.Context, d model.AlertDelivery) {
	var integ model.Integration
	if err := w.db.WithContext(ctx).First(&integ, d.IntegrationID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			w.finalizeTerminal(d, "integration deleted", integrationDeletedLogger(d))
		} else {
			logger.Module("alerting").Warn().
				Uint("delivery_id", d.ID).Err(err).
				Msg("读取 integration 失败，跳过本次重试")
		}
		return
	}
	var alert model.Alert
	if err := w.db.WithContext(ctx).First(&alert, d.AlertID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			w.finalizeTerminal(d, "alert deleted", alertDeletedLogger(d))
		} else {
			logger.Module("alerting").Warn().
				Uint("delivery_id", d.ID).Err(err).
				Msg("读取 alert 失败，跳过本次重试")
		}
		return
	}

	// Slow path: HTTP send, intentionally NOT holding mu.
	sendErr := w.sendFn(integ, alert)

	// State transition is guarded so ManualRetry and tick cannot race.
	w.mu.Lock()
	defer w.mu.Unlock()
	d.AttemptCount++
	switch {
	case sendErr == nil:
		d.Status = "sent"
		d.NextRetryAt = nil
		d.LastError = ""
	case d.AttemptCount >= maxAttempts:
		d.Status = "failed"
		d.NextRetryAt = nil
		d.LastError = sanitizeDeliveryError(sendErr)
		logger.Module("alerting").Warn().
			Uint("delivery_id", d.ID).
			Err(sendErr).
			Msg("告警投递重试达到上限，终止")
	default:
		next := time.Now().Add(backoffDuration(d.AttemptCount))
		d.Status = "retrying"
		d.NextRetryAt = &next
		d.LastError = sanitizeDeliveryError(sendErr)
	}
	if err := w.db.Save(&d).Error; err != nil {
		logger.Module("alerting").Warn().Err(err).Uint("delivery_id", d.ID).Msg("save delivery failed")
	}
}

// finalizeTerminal marks a delivery failed because the referenced integration
// or alert no longer exists. Caller provides a human-readable reason and a
// log emitter closure so the caller's context (integration_id / alert_id)
// stays attached.
//
// CONTRACT: caller MUST NOT already hold w.mu. finalizeTerminal acquires it
// itself; a caller that wraps this in another Lock() would deadlock. The
// only callsites today are the two ErrRecordNotFound branches in attempt(),
// which run before attempt() takes the lock.
func (w *RetryWorker) finalizeTerminal(d model.AlertDelivery, reason string, logFn func()) {
	w.mu.Lock()
	defer w.mu.Unlock()
	d.Status = "failed"
	d.NextRetryAt = nil
	d.LastError = reason
	d.AttemptCount++
	if err := w.db.Save(&d).Error; err != nil {
		logger.Module("alerting").Warn().Err(err).Uint("delivery_id", d.ID).Msg("save terminal delivery failed")
	}
	logFn()
}

func integrationDeletedLogger(d model.AlertDelivery) func() {
	return func() {
		logger.Module("alerting").Warn().
			Uint("delivery_id", d.ID).
			Uint("integration_id", d.IntegrationID).
			Msg("integration 已删除，投递标记为 failed")
	}
}

func alertDeletedLogger(d model.AlertDelivery) func() {
	return func() {
		logger.Module("alerting").Warn().
			Uint("delivery_id", d.ID).
			Uint("alert_id", d.AlertID).
			Msg("alert 已删除，投递标记为 failed")
	}
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
	w.attempt(context.Background(), d)
	return nil
}

// dispatchSingle 是生产路径的适配器：将 (Integration, Alert) 路由到 dispatcher.go 中的
// send() 函数（按 integration.Type 分发到各通道发送器）。
func dispatchSingle(integ model.Integration, alert model.Alert) error {
	return send(integ, alert)
}

// sanitizeDeliveryError redacts URL credentials and scrubs common token/key
// patterns from a sender error message before it is persisted to
// alert_deliveries.last_error. Stored LastError is readable by any user with
// alerts:deliveries permission (viewer included), so leaking webhook URLs
// that embed bearer tokens or API keys is a direct A09 risk.
func sanitizeDeliveryError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	msg = redactURLs(msg)
	for _, re := range sensitivePatterns {
		msg = re.ReplaceAllString(msg, "$1=***")
	}
	if len(msg) > 500 {
		msg = msg[:500] + "…"
	}
	return msg
}

var urlLike = regexp.MustCompile(`(https?|wss?)://[^\s"'<>]+`)

var sensitivePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(authorization|bearer|token|api[_-]?key|secret|password)[=:]\s*[^\s"',;)]+`),
}

// redactURLs drops credentials, query strings, and path-segment secrets from
// any http(s)/ws URL embedded in msg. Webhook targets (Slack /services/T/B/X,
// Feishu /open-apis/bot/v2/hook/<token>, DingTalk /robot/send?access_token=...,
// Telegram /bot<token>/sendMessage, etc.) routinely carry bearer tokens in the
// URL *path*, so keeping scheme+host alone is what's safe to persist. Query
// strings are also redacted (DingTalk's access_token lives there).
func redactURLs(msg string) string {
	return urlLike.ReplaceAllStringFunc(msg, func(match string) string {
		u, err := url.Parse(match)
		if err != nil {
			return match
		}
		if u.User != nil {
			u.User = url.User("***")
		}
		if u.RawQuery != "" {
			u.RawQuery = "***"
		}
		// Path can contain tokens — truncate to "/…" when non-trivial. A bare
		// "/" or empty path is fine (no secrets).
		if u.Path != "" && u.Path != "/" {
			u.Path = "/***"
		}
		return strings.TrimSuffix(u.String(), "?")
	})
}
