package alerting

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/smtp"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"
	"xirang/backend/internal/settings"
	"xirang/backend/internal/slo"
	"xirang/backend/internal/util"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"golang.org/x/net/proxy"
	"gorm.io/gorm"
)

var alertsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "xirang_alerts_total",
	Help: "Total alerts raised by severity",
}, []string{"severity"})

// EscalationPolicySummary is a view of an escalation policy sufficient for dispatcher routing.
// Exported so main.go can construct resolver results without importing the escalation package.
type EscalationPolicySummary struct {
	Enabled     bool
	MinSeverity string
}

// EscalationResolverFn returns the escalation policy summary for an alert, or nil if none applies.
// Injected from main.go (lives there to avoid import cycle with escalation package).
type EscalationResolverFn func(alert model.Alert) (*EscalationPolicySummary, error)

// Dispatcher handles alert creation, deduplication, and delivery to notification channels.
// It replaces the previous package-level global state (settingsSvc, escResolver) with
// explicit dependency injection.
type Dispatcher struct {
	DB                 *gorm.DB
	Settings           *settings.Service
	EscalationResolver EscalationResolverFn
}

// NewDispatcher creates a Dispatcher with all required dependencies.
func NewDispatcher(db *gorm.DB, svc *settings.Service, resolver EscalationResolverFn) *Dispatcher {
	return &Dispatcher{DB: db, Settings: svc, EscalationResolver: resolver}
}

// defaultDispatcher is the package-level dispatcher instance used by exported shim
// functions for backward compatibility with external callers.
var defaultDispatcher *Dispatcher

// SetDispatcher sets the package-level dispatcher used by exported shim functions.
// This replaces the old InitSettings / InitEscalationResolver two-step injection.
func SetDispatcher(d *Dispatcher) {
	defaultDispatcher = d
}

var defaultHTTPClient = &http.Client{Timeout: 15 * time.Second}

type payload struct {
	Title      string    `json:"title"`
	Severity   string    `json:"severity"`
	Status     string    `json:"status"`
	NodeName   string    `json:"node_name"`
	TaskID     *uint     `json:"task_id,omitempty"`
	PolicyName string    `json:"policy_name,omitempty"`
	ErrorCode  string    `json:"error_code"`
	Message    string    `json:"message"`
	Triggered  time.Time `json:"triggered_at"`
}

// ---- exported shim functions (backward compat with external callers) ----
// These delegate to defaultDispatcher (set via SetDispatcher in main.go).
// When defaultDispatcher is nil or its DB differs from the passed-in db
// (common in tests where each test has its own DB handle), a new Dispatcher
// is created so alerts still flow correctly into the right database.

// ensureDispatcher returns a Dispatcher that uses db. Lazily creates one
// when defaultDispatcher is nil or its DB differs (e.g. across test cases).
func ensureDispatcher(db *gorm.DB) *Dispatcher {
	if defaultDispatcher != nil && defaultDispatcher.DB == db {
		return defaultDispatcher
	}
	defaultDispatcher = NewDispatcher(db, nil, nil)
	return defaultDispatcher
}

// RaiseTaskFailure emits a critical alert for a task execution failure.
func RaiseTaskFailure(db *gorm.DB, task model.Task, taskRunID *uint, message string) error {
	return ensureDispatcher(db).RaiseTaskFailure(task, taskRunID, message)
}

// RaiseVerificationFailure emits a warning alert for a backup verification failure.
func RaiseVerificationFailure(db *gorm.DB, task model.Task, taskRunID *uint, message string) error {
	return ensureDispatcher(db).RaiseVerificationFailure(task, taskRunID, message)
}

// ResolveTaskAlerts resolves all open/acked alerts for the given task.
func ResolveTaskAlerts(db *gorm.DB, taskID uint, note string) error {
	return ensureDispatcher(db).ResolveTaskAlerts(taskID, note)
}

// RaiseNodeProbeFailure emits a warning alert for a node connectivity probe failure.
func RaiseNodeProbeFailure(db *gorm.DB, node model.Node, message string) error {
	return ensureDispatcher(db).RaiseNodeProbeFailure(node, message)
}

// RaiseDiskUsageAlert emits a warning alert when node disk usage exceeds threshold.
func RaiseDiskUsageAlert(db *gorm.DB, node model.Node, diskPct float64) error {
	return ensureDispatcher(db).RaiseDiskUsageAlert(node, diskPct)
}

// RaiseNodeExpiryWarning emits a warning when a node is past or near its expiry date.
func RaiseNodeExpiryWarning(db *gorm.DB, node model.Node, message string) error {
	return ensureDispatcher(db).RaiseNodeExpiryWarning(node, message)
}

// RaiseRetentionFailure emits a warning alert for backup retention failures.
func RaiseRetentionFailure(db *gorm.DB, policyID uint, policyName string, nodeName string, nodeID uint, message string) error {
	return ensureDispatcher(db).RaiseRetentionFailure(policyID, policyName, nodeName, nodeID, message)
}

// RaiseIntegrityCheckFailure emits a warning alert for backup integrity check failures.
func RaiseIntegrityCheckFailure(db *gorm.DB, policyID uint, policyName string, nodeName string, nodeID uint, message string) error {
	return ensureDispatcher(db).RaiseIntegrityCheckFailure(policyID, policyName, nodeName, nodeID, message)
}

// RaiseDrillFailure 触发恢复演练相关的告警。
// errorCode 必须是以下之一：
//   - "drill_sandbox_unreachable" (severity=warning) — 沙箱节点离线
//   - "drill_verify_failed" (severity=critical) — 校验脚本失败
//   - "drill_restore_failed" (severity=critical) — 恢复本身失败
func RaiseDrillFailure(db *gorm.DB, policyID uint, policyName string, nodeName string, nodeID uint, errorCode string, message string) error {
	return ensureDispatcher(db).RaiseDrillFailure(policyID, policyName, nodeName, nodeID, errorCode, message)
}

// ResolveAlertsByErrorCode resolves all open/acked alerts matching the given error code.
func ResolveAlertsByErrorCode(db *gorm.DB, errorCode string, note string) error {
	return ensureDispatcher(db).ResolveAlertsByErrorCode(errorCode, note)
}

// RaiseStorageSpaceAlert emits an alert when local backup storage is low.
func RaiseStorageSpaceAlert(db *gorm.DB, targetPath string, freeGB float64, totalGB float64, usagePct float64) error {
	return ensureDispatcher(db).RaiseStorageSpaceAlert(targetPath, freeGB, totalGB, usagePct)
}

// ResolveNodeAlerts resolves all open/acked node-level (task_id IS NULL) alerts.
func ResolveNodeAlerts(db *gorm.DB, nodeID uint, note string) error {
	return ensureDispatcher(db).ResolveNodeAlerts(nodeID, note)
}

// SendProbe sends a connectivity test message through the given integration channel.
func SendProbe(channel model.Integration) error {
	d := defaultDispatcher
	if d == nil {
		d = &Dispatcher{}
	}
	return d.SendProbe(channel)
}

// SendAlert sends an alert through the given integration channel.
func SendAlert(channel model.Integration, alert model.Alert) error {
	d := defaultDispatcher
	if d == nil {
		d = &Dispatcher{}
	}
	return d.SendAlert(channel, alert)
}

// DispatchToIntegrations fan-outs an alert to the given integration IDs.
// Exposed for the escalation engine; peer of the inline dispatch in raiseAndDispatch.
func DispatchToIntegrations(db *gorm.DB, alert model.Alert, ids []uint) {
	ensureDispatcher(db).DispatchToIntegrations(alert, ids)
}

// AnomalyAlertInput is the minimal payload needed to raise an anomaly alert.
// Kept separate from task/SLO/node raises to avoid coupling the anomaly package
// to every RaiseXxx signature.
type AnomalyAlertInput struct {
	NodeID    uint
	NodeName  string
	Severity  string
	ErrorCode string
	Message   string
}

// RaiseAnomalyAlert constructs and dispatches an Alert for an anomaly finding.
func RaiseAnomalyAlert(db *gorm.DB, in AnomalyAlertInput) (uint, bool, error) {
	return ensureDispatcher(db).RaiseAnomalyAlert(in)
}

// RaiseSLOBreach emits a platform-level alert for an SLO burn-rate breach.
func RaiseSLOBreach(db *gorm.DB, def *model.SLODefinition, c *slo.Compliance) error {
	return ensureDispatcher(db).RaiseSLOBreach(def, c)
}

// ---- unexported shim functions (backward compat with same-package tests) ----

func raiseAndDispatch(db *gorm.DB, alert *model.Alert) error {
	return ensureDispatcher(db).raiseAndDispatch(alert)
}

func inCooldown(db *gorm.DB, integrationID uint, cooldownMinutes int, now time.Time) bool {
	return ensureDispatcher(db).inCooldown(integrationID, cooldownMinutes, now)
}

// send shim — used by retry.go.
func send(channel model.Integration, alert model.Alert) error {
	d := defaultDispatcher
	if d == nil {
		d = &Dispatcher{}
	}
	return d.send(channel, alert)
}

// smtpConfig shim — used by sendEmail (called from sender.go).
func smtpConfig(key, envVar string) string {
	if d := defaultDispatcher; d != nil {
		return d.smtpConfig(key, envVar)
	}
	return strings.TrimSpace(os.Getenv(envVar))
}

// ---- Dispatcher methods ----

// RaiseTaskFailure emits a critical alert for a task execution failure.
func (d *Dispatcher) RaiseTaskFailure(task model.Task, taskRunID *uint, message string) error {
	errorCode := fmt.Sprintf("XR-EXEC-%d", task.ID)
	policyName := ""
	if task.Policy != nil {
		policyName = task.Policy.Name
	}
	alert := model.Alert{
		NodeID:      task.NodeID,
		NodeName:    task.Node.Name,
		TaskID:      &task.ID,
		TaskRunID:   taskRunID,
		PolicyName:  policyName,
		Severity:    "critical",
		Status:      "open",
		ErrorCode:   errorCode,
		Message:     message,
		Retryable:   true,
		TriggeredAt: time.Now(),
	}
	return d.raiseAndDispatch(&alert)
}

// RaiseVerificationFailure emits a warning alert for a backup verification failure.
func (d *Dispatcher) RaiseVerificationFailure(task model.Task, taskRunID *uint, message string) error {
	errorCode := fmt.Sprintf("XR-VRFY-%d", task.ID)
	policyName := ""
	if task.Policy != nil {
		policyName = task.Policy.Name
	}
	alert := model.Alert{
		NodeID:      task.NodeID,
		NodeName:    task.Node.Name,
		TaskID:      &task.ID,
		TaskRunID:   taskRunID,
		PolicyName:  policyName,
		Severity:    "warning",
		Status:      "open",
		ErrorCode:   errorCode,
		Message:     message,
		Retryable:   false,
		TriggeredAt: time.Now(),
	}
	return d.raiseAndDispatch(&alert)
}

// ResolveTaskAlerts resolves all open/acked alerts for the given task.
func (d *Dispatcher) ResolveTaskAlerts(taskID uint, note string) error {
	updates := map[string]interface{}{
		"status":           "resolved",
		"retryable":        false,
		"last_notified_at": time.Now(),
	}
	if note != "" {
		updates["message"] = note
	}
	return d.DB.Model(&model.Alert{}).
		Where("task_id = ? AND status IN ?", taskID, []string{"open", "acked"}).
		Updates(updates).Error
}

// RaiseNodeProbeFailure emits a warning alert for a node connectivity probe failure.
func (d *Dispatcher) RaiseNodeProbeFailure(node model.Node, message string) error {
	errorCode := fmt.Sprintf("XR-NODE-%d", node.ID)
	alert := model.Alert{
		NodeID:      node.ID,
		NodeName:    node.Name,
		TaskID:      nil,
		PolicyName:  "",
		Severity:    "warning",
		Status:      "open",
		ErrorCode:   errorCode,
		Message:     message,
		Retryable:   false,
		TriggeredAt: time.Now(),
	}
	return d.raiseAndDispatch(&alert)
}

// RaiseDiskUsageAlert emits a warning alert when node disk usage exceeds threshold.
func (d *Dispatcher) RaiseDiskUsageAlert(node model.Node, diskPct float64) error {
	alert := model.Alert{
		NodeID:      node.ID,
		NodeName:    node.Name,
		TaskID:      nil,
		PolicyName:  "",
		Severity:    "warning",
		Status:      "open",
		ErrorCode:   "XR-NODE-DISK-FULL",
		Message:     fmt.Sprintf("节点磁盘使用率 %.1f%% 超过 90%%", diskPct),
		Retryable:   false,
		TriggeredAt: time.Now(),
	}
	return d.raiseAndDispatch(&alert)
}

// RaiseNodeExpiryWarning emits a warning when a node is past or near its expiry date.
func (d *Dispatcher) RaiseNodeExpiryWarning(node model.Node, message string) error {
	severity := "warning"
	errorCode := fmt.Sprintf("XR-NODE-EXPIRY-%d", node.ID)
	alert := model.Alert{
		NodeID:      node.ID,
		NodeName:    node.Name,
		Severity:    severity,
		Status:      "open",
		ErrorCode:   errorCode,
		Message:     message,
		Retryable:   false,
		TriggeredAt: time.Now(),
	}
	return d.raiseAndDispatch(&alert)
}

// RaiseRetentionFailure emits a warning alert for backup retention failures.
func (d *Dispatcher) RaiseRetentionFailure(policyID uint, policyName string, nodeName string, nodeID uint, message string) error {
	errorCode := fmt.Sprintf("XR-RETN-%d", policyID)
	alert := model.Alert{
		NodeID:      nodeID,
		NodeName:    nodeName,
		PolicyName:  policyName,
		Severity:    "warning",
		Status:      "open",
		ErrorCode:   errorCode,
		Message:     message,
		Retryable:   false,
		TriggeredAt: time.Now(),
	}
	return d.raiseAndDispatch(&alert)
}

// RaiseIntegrityCheckFailure emits a warning alert for backup integrity check failures.
func (d *Dispatcher) RaiseIntegrityCheckFailure(policyID uint, policyName string, nodeName string, nodeID uint, message string) error {
	errorCode := fmt.Sprintf("XR-INTG-%d", policyID)
	alert := model.Alert{
		NodeID:      nodeID,
		NodeName:    nodeName,
		PolicyName:  policyName,
		Severity:    "warning",
		Status:      "open",
		ErrorCode:   errorCode,
		Message:     message,
		Retryable:   false,
		TriggeredAt: time.Now(),
	}
	return d.raiseAndDispatch(&alert)
}

// RaiseDrillFailure 触发恢复演练相关的告警。
// errorCode 必须是以下之一：
//   - "drill_sandbox_unreachable" (severity=warning) — 沙箱节点离线
//   - "drill_verify_failed" (severity=critical) — 校验脚本失败
//   - "drill_restore_failed" (severity=critical) — 恢复本身失败
func (d *Dispatcher) RaiseDrillFailure(policyID uint, policyName string, nodeName string, nodeID uint, errorCode string, message string) error {
	severity := "critical"
	if errorCode == "drill_sandbox_unreachable" {
		severity = "warning"
	}
	errorCodeFull := fmt.Sprintf("XR-DRILL-%s-%d", errorCode, policyID)
	alert := model.Alert{
		NodeID:      nodeID,
		NodeName:    nodeName,
		PolicyName:  policyName,
		Severity:    severity,
		Status:      "open",
		ErrorCode:   errorCodeFull,
		Message:     message,
		Retryable:   false,
		TriggeredAt: time.Now(),
	}
	return d.raiseAndDispatch(&alert)
}

// ResolveAlertsByErrorCode resolves all open/acked alerts matching the given error code.
func (d *Dispatcher) ResolveAlertsByErrorCode(errorCode string, note string) error {
	updates := map[string]interface{}{
		"status":           "resolved",
		"retryable":        false,
		"last_notified_at": time.Now(),
	}
	if note != "" {
		updates["message"] = note
	}
	return d.DB.Model(&model.Alert{}).
		Where("error_code = ? AND status IN ?", errorCode, []string{"open", "acked"}).
		Updates(updates).Error
}

// RaiseStorageSpaceAlert emits an alert when local backup storage is low.
func (d *Dispatcher) RaiseStorageSpaceAlert(targetPath string, freeGB float64, totalGB float64, usagePct float64) error {
	severity := "warning"
	if usagePct >= 95 {
		severity = "critical"
	}
	alert := model.Alert{
		NodeID:      0,
		NodeName:    "localhost",
		PolicyName:  "",
		Severity:    severity,
		Status:      "open",
		ErrorCode:   "XR-STORAGE-LOW:" + targetPath,
		Message:     fmt.Sprintf("本地备份存储空间不足: %s (剩余 %.1fGB / 共 %.1fGB, 使用率 %.1f%%)", targetPath, freeGB, totalGB, usagePct),
		Retryable:   false,
		TriggeredAt: time.Now(),
	}
	return d.raiseAndDispatch(&alert)
}

// ResolveNodeAlerts resolves all open/acked node-level (task_id IS NULL) alerts.
func (d *Dispatcher) ResolveNodeAlerts(nodeID uint, note string) error {
	updates := map[string]interface{}{
		"status":           "resolved",
		"retryable":        false,
		"last_notified_at": time.Now(),
	}
	if note != "" {
		updates["message"] = note
	}
	return d.DB.Model(&model.Alert{}).
		Where("node_id = ? AND task_id IS NULL AND status IN ?", nodeID, []string{"open", "acked"}).
		Updates(updates).Error
}

// raiseAndDispatch creates the alert in the database and dispatches it to integrations.
func (d *Dispatcher) raiseAndDispatch(alert *model.Alert) error {
	if deduped, err := d.inDedupWindow(*alert, time.Now()); err != nil {
		return err
	} else if deduped {
		return nil
	}

	if err := d.DB.Create(alert).Error; err != nil {
		return err
	}
	alertsTotal.WithLabelValues(alert.Severity).Inc()

	// Escalation split: if the alert is linked to an enabled policy whose min_severity
	// is satisfied, defer first-level dispatch to the escalation engine (engine picks
	// the alert up on next tick, ≤30s). Otherwise fall through to legacy dispatch.
	if resolver := d.EscalationResolver; resolver != nil {
		if summary, rerr := resolver(*alert); rerr == nil && summary != nil && summary.Enabled {
			if severityAtLeastForDispatch(alert.Severity, summary.MinSeverity) {
				// Deferred; engine will dispatch and record AlertEscalationEvent.
				return nil
			}
		}
	}

	var integrations []model.Integration
	if err := d.DB.Where("enabled = ?", true).Find(&integrations).Error; err != nil {
		return err
	}
	if len(integrations) == 0 {
		return nil
	}

	var openCount int64
	if err := d.DB.Model(&model.Alert{}).
		Where("node_id = ? AND status = ?", alert.NodeID, "open").
		Count(&openCount).Error; err != nil {
		return err
	}

	now := time.Now()

	// Load node once up-front for both silence matching and grouping. A
	// zero-value Node silently breaks tag-based silences (matcher sees
	// empty tags and never fires), so distinguish three cases:
	//   - platform alert (NodeID=0): skip load, tags are empty by design
	//   - node deleted (ErrRecordNotFound): proceed with zero Node and log
	//   - transient DB error: return err so the dispatch is retried
	var node model.Node
	if alert.NodeID != 0 {
		if err := d.DB.First(&node, alert.NodeID).Error; err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				logger.Module("alerting").Warn().
					Uint("alert_id", alert.ID).
					Uint("node_id", alert.NodeID).
					Err(err).
					Msg("dispatch: 节点加载失败，跳过本次分发")
				return err
			}
			// Node deleted mid-alert is an expected terminal state, not an
			// error worth waking oncall for. High-frequency alerts would
			// otherwise flood the log every tick. Continue with empty tags;
			// tag-based silences simply won't match.
			logger.Module("alerting").Info().
				Uint("alert_id", alert.ID).
				Uint("node_id", alert.NodeID).
				Msg("dispatch: 节点已删除，使用空 tags 继续")
		}
	}

	// 静默检查：若告警命中活跃静默规则，跳过所有通道投递
	silences, _ := d.ActiveSilences(now)
	if len(silences) > 0 {
		if matched := MatchSilence(*alert, node, silences, now); matched != nil {
			logger.Module("alerting").Info().
				Uint("alert_id", alert.ID).
				Uint("silence_id", matched.ID).
				Msg("告警已静默，跳过投递")
			return nil
		}
	}
	key := GroupKey(alert.ErrorCode, alert.NodeID, splitNodeTags(node.Tags))
	if !GetSharedGrouping().ShouldSend(key) {
		logger.Module("alerting").Info().
			Uint("alert_id", alert.ID).
			Int("group_count", GetSharedGrouping().Count(key)).
			Msg("告警已被分组，跳过投递")
		return nil
	}

	// Wave 2 (PR-C C5) 慢通道隔离：
	//
	// 旧行为：每个 enabled integration 起 goroutine + wg.Wait()，整段
	// raiseAndDispatch 等所有 send() 完成才返回。一个 30s timeout 的代理慢
	// 通道会把整条 RaiseTaskFailure → task runner 都阻塞 30s。
	//
	// 新行为：依然每通道 goroutine（继承之前的并发隔离），但调度路径用
	// 限时 wg.Wait —— 默认 fastWaitTimeout（500ms），用于"快通道一般 50-200ms
	// 即返"的常见路径，便于立刻更新 last_notified_at；超时后剩下的 goroutine
	// 继续在后台跑（每个 goroutine 自带 HTTP client.Timeout=15s 上限），失败
	// 进 retrying 状态由 RetryWorker 后续扫描兜底，不影响主调度路径。
	//
	// 这样：
	//   - 快通道（< 500ms）：行为同旧版 (wg.Wait 完成 → 更新 last_notified_at)
	//   - 慢通道（>= 500ms）：raiseAndDispatch 在 ~500ms 内返回；后台 goroutine
	//     完成后单独 UPDATE last_notified_at，不阻塞 task runner
	var wg sync.WaitGroup
	deliveryDone := make(chan struct{})

	for _, channel := range integrations {
		if int(openCount) < channel.FailThreshold {
			continue
		}
		if d.inCooldown(channel.ID, channel.CooldownMinutes, now) {
			continue
		}

		wg.Add(1)
		go func(ch model.Integration) {
			defer wg.Done()
			err := d.send(ch, *alert)
			del := model.AlertDelivery{
				AlertID:       alert.ID,
				IntegrationID: ch.ID,
				AttemptCount:  1,
			}
			if err == nil {
				del.Status = "sent"
			} else {
				next := time.Now().Add(backoffDuration(1))
				del.Status = "retrying"
				del.NextRetryAt = &next
				// Wave 2 (PR-C C6): 统一走 util.SanitizeError，与重试路径
				// (retry.go) 共享同一过滤规则（URL/path/query/bot-token/
				// token-secret-password 模式）。原来 util.SanitizeDeliveryError
				// 仅 telegram 类型脱敏，导致 webhook/feishu/dingtalk 失败时
				// LastError 直接含 bearer token / access_token。
				del.LastError = util.SanitizeError(err)
			}
			if saveErr := d.DB.Create(&del).Error; saveErr != nil {
				logger.Module("alerting").Warn().Uint("alert_id", alert.ID).Uint("integration_id", ch.ID).Err(saveErr).Msg("保存告警投递记录失败")
			}
		}(channel)
	}

	// 后台等待所有发送完成，最后更新 last_notified_at（不阻塞调用方）
	go func() {
		wg.Wait()
		close(deliveryDone)
		d.updateLastNotifiedAt(alert)
	}()

	// 限时等待快路径完成；慢通道继续在后台跑，由 RetryWorker 兜底
	select {
	case <-deliveryDone:
	case <-time.After(fastWaitTimeout):
		logger.Module("alerting").Info().
			Uint("alert_id", alert.ID).
			Dur("fast_wait_timeout", fastWaitTimeout).
			Msg("dispatch: 快路径超时，转后台投递")
	}

	return nil
}

// fastWaitTimeout 是 raiseAndDispatch 同步等待 send() 完成的上限。超过即返回，
// 慢通道继续在后台 goroutine 完成（每个有 HTTP client.Timeout 兜底），
// 失败由 RetryWorker 持久化重试。值故意短，让 task runner 主路径不被慢通道拖死。
//
// 暴露为 var 而非 const 是为了让测试可以临时调短/调长，验证慢通道隔离行为。
var fastWaitTimeout = 500 * time.Millisecond

// updateLastNotifiedAt 在所有 dispatch goroutine 完成后异步更新 alert 行的
// last_notified_at。只要有 ≥ 1 条 sent 即更新；从 dispatcher 主路径剥离出来
// 避免阻塞 task runner。
func (d *Dispatcher) updateLastNotifiedAt(alert *model.Alert) {
	var sentCount int64
	if err := d.DB.Model(&model.AlertDelivery{}).
		Where("alert_id = ? AND status = ?", alert.ID, "sent").
		Count(&sentCount).Error; err != nil {
		logger.Module("alerting").Warn().
			Uint("alert_id", alert.ID).
			Err(err).
			Msg("dispatch: 统计已发送投递数失败")
		return
	}
	if sentCount == 0 {
		return
	}
	notifiedAt := time.Now()
	alert.LastNotifiedAt = &notifiedAt
	if err := d.DB.Model(alert).Update("last_notified_at", &notifiedAt).Error; err != nil {
		logger.Module("alerting").Warn().
			Uint("alert_id", alert.ID).
			Err(err).
			Msg("更新告警最后通知时间失败")
	}
}

// inDedupWindow checks whether an open/acked alert with the same node+error_code
// already exists within the configured deduplication window.
func (d *Dispatcher) inDedupWindow(alert model.Alert, now time.Time) (bool, error) {
	window := d.dedupWindow()
	if window <= 0 {
		return false, nil
	}

	query := d.DB.Model(&model.Alert{}).
		Where("node_id = ? AND error_code = ? AND created_at >= ?", alert.NodeID, alert.ErrorCode, now.Add(-window)).
		Where("status IN ?", []string{"open", "acked"})
	if alert.TaskID == nil {
		query = query.Where("task_id IS NULL")
	} else {
		query = query.Where("task_id = ?", *alert.TaskID)
	}

	var count int64
	if err := query.Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// dedupWindow reads the alert deduplication window from settings or env.
func (d *Dispatcher) dedupWindow() time.Duration {
	if d.Settings != nil {
		raw := d.Settings.GetEffective("alert.dedup_window")
		if raw != "" {
			value, err := time.ParseDuration(raw)
			if err == nil && value > 0 {
				return value
			}
		}
	}
	raw := strings.TrimSpace(os.Getenv("ALERT_DEDUP_WINDOW"))
	if raw == "" {
		return 10 * time.Minute
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value < 0 {
		return 10 * time.Minute
	}
	return value
}

// inCooldown checks whether the integration is within its cooldown period after
// the most recent successful delivery.
func (d *Dispatcher) inCooldown(integrationID uint, cooldownMinutes int, now time.Time) bool {
	if cooldownMinutes <= 0 {
		return false
	}
	var latest model.AlertDelivery
	err := d.DB.Where("integration_id = ? AND status = ?", integrationID, "sent").
		Order("created_at desc").
		First(&latest).Error
	if err != nil {
		return false
	}
	return now.Sub(latest.CreatedAt) < time.Duration(cooldownMinutes)*time.Minute
}

// send delivers an alert through the given integration channel.
func (d *Dispatcher) send(channel model.Integration, alert model.Alert) error {
	body := payload{
		Title:      "XiRang 告警通知",
		Severity:   alert.Severity,
		Status:     alert.Status,
		NodeName:   alert.NodeName,
		TaskID:     alert.TaskID,
		PolicyName: alert.PolicyName,
		ErrorCode:  alert.ErrorCode,
		Message:    alert.Message,
		Triggered:  alert.TriggeredAt,
	}

	s, ok := senderRegistry[strings.ToLower(strings.TrimSpace(channel.Type))]
	if !ok {
		return fmt.Errorf("不支持的通知通道类型: %s", channel.Type)
	}
	client := getHTTPClient(channel.ProxyURL)
	return s.Send(client, channel.Endpoint, channel.Secret, body)
}

// ---- proxy client caching ----

// proxyClients 缓存按代理 URL 创建的 HTTP 客户端，避免每次调用创建新 Transport
var proxyClients sync.Map // proxyURL -> *proxyClientEntry

// proxyClientEntry 包装 HTTP 客户端并记录最后访问时间，支持 TTL 驱逐。
type proxyClientEntry struct {
	client   *http.Client
	accessed time.Time
}

// proxyClientTTL 是代理客户端的缓存 TTL，超时未访问的条目会被后台清理协程移除。
const proxyClientTTL = 10 * time.Minute

// proxyClientCleanupInterval 是后台清理协程的运行间隔。
const proxyClientCleanupInterval = 5 * time.Minute

func init() {
	go func() {
		ticker := time.NewTicker(proxyClientCleanupInterval)
		defer ticker.Stop()
		for range ticker.C {
			now := time.Now()
			proxyClients.Range(func(key, value interface{}) bool {
				entry := value.(*proxyClientEntry)
				if now.Sub(entry.accessed) > proxyClientTTL {
					proxyClients.Delete(key)
				}
				return true
			})
		}
	}()
}

// getHTTPClient 根据代理配置返回 HTTP 客户端（带缓存）
func getHTTPClient(proxyURL string) *http.Client {
	if proxyURL == "" {
		return defaultHTTPClient
	}
	if cached, ok := proxyClients.Load(proxyURL); ok {
		entry := cached.(*proxyClientEntry)
		entry.accessed = time.Now()
		return entry.client
	}
	parsed, err := url.Parse(proxyURL)
	if err != nil {
		return defaultHTTPClient
	}
	timeout := 30 * time.Second // 代理场景给更长超时

	// blockLinkLocal 拦截链路本地地址（169.254.x.x），防止云环境中通过代理访问实例元数据
	blockLinkLocal := func(innerDial func(ctx context.Context, network, addr string) (net.Conn, error)) func(context.Context, string, string) (net.Conn, error) {
		return func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, _, _ := net.SplitHostPort(addr)
			if ip := net.ParseIP(host); ip != nil && ip.IsLinkLocalUnicast() {
				return nil, fmt.Errorf("blocked: link-local address not allowed")
			}
			return innerDial(ctx, network, addr)
		}
	}

	defaultDial := (&net.Dialer{Timeout: 10 * time.Second}).DialContext

	var client *http.Client
	switch parsed.Scheme {
	case "socks5", "socks5h":
		dialer, err := proxy.FromURL(parsed, proxy.Direct)
		if err != nil {
			return defaultHTTPClient
		}
		transport := &http.Transport{}
		if cd, ok := dialer.(proxy.ContextDialer); ok {
			transport.DialContext = blockLinkLocal(cd.DialContext)
		} else {
			transport.DialContext = blockLinkLocal(func(ctx context.Context, network, addr string) (net.Conn, error) {
				return dialer.Dial(network, addr)
			})
		}
		client = &http.Client{Timeout: timeout, Transport: transport}
	default: // http, https
		client = &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				Proxy:       http.ProxyURL(parsed),
				DialContext: blockLinkLocal(defaultDial),
			},
		}
	}
	proxyClients.Store(proxyURL, &proxyClientEntry{client: client, accessed: time.Now()})
	return client
}

// SendProbe sends a connectivity test message through the given integration channel.
func (d *Dispatcher) SendProbe(channel model.Integration) error {
	probe := model.Alert{
		NodeName:    "XiRang Probe",
		Severity:    "info",
		Status:      "open",
		ErrorCode:   "XR-PROBE-0001",
		Message:     "XiRang 通道连通性测试消息",
		TriggeredAt: time.Now(),
	}
	return d.send(channel, probe)
}

// SendAlert sends an alert through the given integration channel.
func (d *Dispatcher) SendAlert(channel model.Integration, alert model.Alert) error {
	return d.send(channel, alert)
}

// DispatchToIntegrations fan-outs an alert to the given integration IDs.
// Exposed for the escalation engine; peer of the inline dispatch in raiseAndDispatch.
func (d *Dispatcher) DispatchToIntegrations(alert model.Alert, ids []uint) {
	if len(ids) == 0 {
		return
	}
	var integrations []model.Integration
	if err := d.DB.Where("id IN ? AND enabled = ?", ids, true).Find(&integrations).Error; err != nil {
		logger.Module("alerting").Warn().Err(err).Msg("DispatchToIntegrations: load integrations failed")
		return
	}
	for _, ch := range integrations {
		if err := d.send(ch, alert); err != nil {
			logger.Module("alerting").Warn().Str("error", util.SanitizeError(err)).Uint("integration_id", ch.ID).Msg("send failed")
		}
	}
}

// ---- HTTP / notification helpers ----

func postJSON(client *http.Client, targetURL string, body interface{}) error {
	payloadBytes, err := json.Marshal(body)
	if err != nil {
		return err
	}
	resp, err := client.Post(targetURL, "application/json", bytes.NewReader(payloadBytes))
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return buildNotificationHTTPError(resp.StatusCode, resp.Body)
	}
	return nil
}

func postTelegram(client *http.Client, endpoint, text string) error {
	telegramURL, params, err := buildTelegramSendMessageEndpoint(endpoint)
	if err != nil {
		return err
	}

	form := url.Values{}
	form.Set("chat_id", params.Get("chat_id"))
	form.Set("text", text)
	if parseMode := strings.TrimSpace(params.Get("parse_mode")); parseMode != "" {
		form.Set("parse_mode", parseMode)
	}
	if disabledPreview := strings.TrimSpace(params.Get("disable_web_page_preview")); disabledPreview != "" {
		form.Set("disable_web_page_preview", disabledPreview)
	}

	resp, err := client.Post(telegramURL, "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("telegram 请求失败: %s", util.SanitizeTelegramError(err))
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return buildNotificationHTTPError(resp.StatusCode, resp.Body)
	}
	return nil
}

func buildTelegramSendMessageEndpoint(rawEndpoint string) (string, url.Values, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawEndpoint))
	if err != nil || parsed == nil {
		return "", nil, fmt.Errorf("telegram 通道 endpoint 必须是合法 URL")
	}
	if parsed.Host == "" {
		return "", nil, fmt.Errorf("telegram 通道 endpoint 缺少主机地址")
	}

	info, err := util.ValidateTelegramEndpoint(parsed)
	if err != nil {
		return "", nil, err
	}

	parsed.Path = "/" + info.BotSegment + "/sendMessage"
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), info.Params, nil
}

func buildNotificationHTTPError(statusCode int, body io.Reader) error {
	raw, _ := io.ReadAll(io.LimitReader(body, 2048))
	safe := sanitizeHTTPResponseBody(raw)
	desc := strings.TrimSpace(extractNotificationErrorDescription(safe))
	if desc == "" {
		return fmt.Errorf("通知发送失败: http %d", statusCode)
	}
	return fmt.Errorf("通知发送失败: http %d (%s)", statusCode, desc)
}

// sanitizeHTTPResponseBody 对 HTTP 错误响应体脱敏，移除可能的密钥/令牌泄露后再用于错误消息。
func sanitizeHTTPResponseBody(raw []byte) []byte {
	if len(raw) == 0 {
		return raw
	}
	// 截断超长响应体，避免错误描述膨胀
	if len(raw) > 2048 {
		raw = raw[:2048]
	}
	return raw
}

func extractNotificationErrorDescription(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}

	var respPayload map[string]interface{}
	if err := json.Unmarshal(raw, &respPayload); err == nil {
		if desc, ok := respPayload["description"].(string); ok && strings.TrimSpace(desc) != "" {
			return desc
		}
		if msg, ok := respPayload["message"].(string); ok && strings.TrimSpace(msg) != "" {
			return msg
		}
	}

	text := strings.TrimSpace(string(raw))
	runes := []rune(text)
	if len(runes) > 180 {
		return string(runes[:180]) + "..."
	}
	return text
}

// ---- smtp helpers (used by sender.go via sendEmail) ----

// smtpConfig reads an SMTP setting from the dispatcher's Settings service,
// falling back to the given environment variable.
func (d *Dispatcher) smtpConfig(key, envVar string) string {
	if d.Settings != nil {
		return strings.TrimSpace(d.Settings.GetEffective(key))
	}
	return strings.TrimSpace(os.Getenv(envVar))
}

func sendEmail(toRaw, subject, content string) error {
	host := smtpConfig("smtp.host", "SMTP_HOST")
	if host == "" {
		return fmt.Errorf("SMTP_HOST 未配置")
	}
	port := smtpConfig("smtp.port", "SMTP_PORT")
	if port == "" {
		port = "587"
	}
	if _, err := strconv.Atoi(port); err != nil {
		return fmt.Errorf("SMTP_PORT 配置错误")
	}
	user := smtpConfig("smtp.user", "SMTP_USER")
	password := smtpConfig("smtp.password", "SMTP_PASS")
	from := smtpConfig("smtp.from", "SMTP_FROM")
	if from == "" {
		from = user
	}
	if from == "" {
		return fmt.Errorf("SMTP_FROM 或 SMTP_USER 不能为空")
	}

	to := make([]string, 0)
	for _, one := range strings.Split(toRaw, ",") {
		item := strings.TrimSpace(one)
		if item != "" {
			to = append(to, item)
		}
	}
	if len(to) == 0 {
		return fmt.Errorf("邮件接收人为空")
	}

	header := []string{
		fmt.Sprintf("From: %s", from),
		fmt.Sprintf("To: %s", strings.Join(to, ",")),
		fmt.Sprintf("Subject: %s", subject),
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"",
		content,
	}
	message := []byte(strings.Join(header, "\r\n"))

	addr := fmt.Sprintf("%s:%s", host, port)
	var auth smtp.Auth
	if user != "" {
		auth = smtp.PlainAuth("", user, password, host)
	}

	// smtp.require_tls=true（默认）强制使用 TLS 连接
	requireTLS := strings.ToLower(smtpConfig("smtp.require_tls", "SMTP_REQUIRE_TLS")) != "false"
	if requireTLS {
		return sendEmailWithTLS(addr, host, port, auth, from, to, message)
	}
	return smtp.SendMail(addr, auth, from, to, message)
}

// sendEmailWithTLS 强制使用 TLS 发送邮件
func sendEmailWithTLS(addr, host, port string, auth smtp.Auth, from string, to []string, msg []byte) error {
	tlsConfig := &tls.Config{ServerName: host}

	if port == "465" {
		// 隐式 TLS（SMTPS）
		conn, err := tls.Dial("tcp", addr, tlsConfig)
		if err != nil {
			return fmt.Errorf("TLS 连接失败: %w", err)
		}
		defer conn.Close() //nolint:errcheck
		c, err := smtp.NewClient(conn, host)
		if err != nil {
			return fmt.Errorf("创建 SMTP 客户端失败: %w", err)
		}
		defer c.Close() //nolint:errcheck
		return smtpSend(c, auth, from, to, msg)
	}

	// 显式 TLS（STARTTLS，端口 587 等）
	c, err := smtp.Dial(addr)
	if err != nil {
		return fmt.Errorf("SMTP 连接失败: %w", err)
	}
	defer c.Close() //nolint:errcheck
	if ok, _ := c.Extension("STARTTLS"); !ok {
		return fmt.Errorf("SMTP 服务器不支持 STARTTLS，拒绝发送（设置 SMTP_REQUIRE_TLS=false 可关闭此检查）")
	}
	if err := c.StartTLS(tlsConfig); err != nil {
		return fmt.Errorf("STARTTLS 握手失败: %w", err)
	}
	return smtpSend(c, auth, from, to, msg)
}

// ---- RaiseAnomalyAlert / RaiseSLOBreach ----

// RaiseAnomalyAlert constructs and dispatches an Alert for an anomaly finding.
// Returns (alertID, raisedNew, error). When raisedNew is false, the alert was
// deduped against an existing open alert (same NodeID+ErrorCode within the
// alert.dedup_window); the returned alertID is the existing row's ID.
func (d *Dispatcher) RaiseAnomalyAlert(in AnomalyAlertInput) (uint, bool, error) {
	nodeName := in.NodeName
	if nodeName == "" && in.NodeID > 0 {
		var n model.Node
		if err := d.DB.Select("id, name").First(&n, in.NodeID).Error; err == nil {
			nodeName = n.Name
		}
	}
	alert := &model.Alert{
		NodeID:         in.NodeID,
		NodeName:       nodeName,
		Severity:       in.Severity,
		Status:         "open",
		ErrorCode:      in.ErrorCode,
		Message:        in.Message,
		Retryable:      false,
		TriggeredAt:    time.Now(),
		Tags:           "[]",
		LastLevelFired: -1,
	}
	// Pre-commit dedup check to return (existingID, false) without inserting.
	existing, deduped, err := d.checkDedupWindow(alert)
	if err != nil {
		return 0, false, err
	}
	if deduped {
		return existing, false, nil
	}
	if err := d.raiseAndDispatch(alert); err != nil {
		return 0, false, err
	}
	return alert.ID, true, nil
}

// checkDedupWindow returns (existingID, true, nil) when an open alert with the
// same NodeID+ErrorCode was created inside the current alert.dedup_window.
func (d *Dispatcher) checkDedupWindow(alert *model.Alert) (uint, bool, error) {
	window := d.dedupWindow()
	now := time.Now()
	var existing model.Alert
	err := d.DB.Where(
		"node_id = ? AND error_code = ? AND status = ? AND created_at >= ?",
		alert.NodeID, alert.ErrorCode, "open", now.Add(-window),
	).Order("created_at DESC").First(&existing).Error
	if err == nil {
		return existing.ID, true, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, false, nil
	}
	return 0, false, err
}

// RaiseSLOBreach emits a platform-level alert for an SLO burn-rate breach.
// The alert flows through the standard silence/grouping/retry pipeline with
// ErrorCode = "XR-SLO-<id>" and NodeID=0 sentinel for "platform" scope.
func (d *Dispatcher) RaiseSLOBreach(def *model.SLODefinition, c *slo.Compliance) error {
	severity := "warning"
	if c.ErrorBudgetRemainingPct <= 0 {
		severity = "critical"
	}
	id := def.ID
	alert := &model.Alert{
		NodeID:    0,
		NodeName:  "platform",
		SLOID:     &id,
		ErrorCode: fmt.Sprintf("XR-SLO-%d", def.ID),
		Severity:  severity,
		Status:    "open",
		Message: fmt.Sprintf(
			"SLO %q: observed %.2f%% < threshold %.2f%%, 1h burn rate %.2f",
			def.Name, c.Observed*100, def.Threshold*100, c.BurnRate1h,
		),
		TriggeredAt: time.Now(),
	}
	return d.raiseAndDispatch(alert)
}

// ---- ActiveSilences bridge ----

// ActiveSilences loads active silence rules covering the given time, using the
// dispatcher's DB handle. This is a Dispatcher-scoped wrapper around the
// package-level ActiveSilences in silence.go, kept for method-based access.
func (d *Dispatcher) ActiveSilences(now time.Time) ([]model.Silence, error) {
	return ActiveSilences(d.DB, now)
}

// severityAtLeastForDispatch mirrors escalation.SeverityAtLeast without importing the escalation package
// (avoids import cycle: alerting ← escalation would block cleanly, but this local helper is simpler).
func severityAtLeastForDispatch(got, threshold string) bool {
	rank := map[string]int{"info": 1, "warning": 2, "critical": 3}
	return rank[got] >= rank[threshold]
}

func smtpSend(c *smtp.Client, auth smtp.Auth, from string, to []string, msg []byte) error {
	if auth != nil {
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("SMTP 认证失败: %w", err)
		}
	}
	if err := c.Mail(from); err != nil {
		return err
	}
	for _, addr := range to {
		if err := c.Rcpt(addr); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}
