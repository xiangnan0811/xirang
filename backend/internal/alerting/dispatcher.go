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
	"sync/atomic"
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
	"gorm.io/gorm/clause"
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

// RaiseTaskFailureForRun emits a causally bounded task failure alert.
func RaiseTaskFailureForRun(db *gorm.DB, task model.Task, runID uint, message string) error {
	return ensureDispatcher(db).RaiseTaskFailureForRun(task, runID, message)
}

// RaiseTaskFailureForRestoreRun emits a causally bounded restore failure alert.
func RaiseTaskFailureForRestoreRun(db *gorm.DB, task model.Task, runID uint, message string) error {
	return ensureDispatcher(db).RaiseTaskFailureForRestoreRun(task, runID, message)
}

// RaiseVerificationFailure emits a warning alert for a backup verification failure.
func RaiseVerificationFailure(db *gorm.DB, task model.Task, taskRunID *uint, message string) error {
	return ensureDispatcher(db).RaiseVerificationFailure(task, taskRunID, message)
}

// RaiseVerificationFailureForRun emits a causally bounded verification warning.
func RaiseVerificationFailureForRun(db *gorm.DB, task model.Task, runID uint, message string) error {
	return ensureDispatcher(db).RaiseVerificationFailureForRun(task, runID, message)
}

// RaiseVerificationFailureForRestoreRun emits a causally bounded restore verification warning.
func RaiseVerificationFailureForRestoreRun(db *gorm.DB, task model.Task, runID uint, message string) error {
	return ensureDispatcher(db).RaiseVerificationFailureForRestoreRun(task, runID, message)
}

// ResolveTaskAlerts resolves all open/acked alerts for the given task.
func ResolveTaskAlerts(db *gorm.DB, taskID uint, note string) error {
	return ensureDispatcher(db).ResolveTaskAlerts(taskID, note)
}

// ResolveTaskAlertsForRun is the causally bounded resolution shim.
func ResolveTaskAlertsForRun(db *gorm.DB, taskID, runID uint, note string) error {
	return ensureDispatcher(db).ResolveTaskAlertsForRun(taskID, runID, note)
}

// ResolveTaskAlertsForRestoreRun is the causally bounded restore resolution shim.
func ResolveTaskAlertsForRestoreRun(db *gorm.DB, taskID, runID uint, note string) error {
	return ensureDispatcher(db).ResolveTaskAlertsForRestoreRun(taskID, runID, note)
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

// RaiseTaskFailureForRun emits a failure alert only while runID remains the
// newest terminal ordinary execution for the task. The task-row lock makes the
// ordering check and alert insert one atomic boundary with terminal writers.
func (d *Dispatcher) RaiseTaskFailureForRun(task model.Task, runID uint, message string) error {
	return d.raiseFailureForCurrentRun(task, runID, message, model.TaskRunStatusFailed, "critical", fmt.Sprintf("XR-EXEC-%d", task.ID), true, "ordinary")
}

// RaiseTaskFailureForRestoreRun emits a failure alert only while runID remains
// the newest terminal restore execution for the task.
func (d *Dispatcher) RaiseTaskFailureForRestoreRun(task model.Task, runID uint, message string) error {
	return d.raiseFailureForCurrentRun(task, runID, message, model.TaskRunStatusFailed, "critical", fmt.Sprintf("XR-EXEC-%d", task.ID), true, "restore")
}

// RaiseVerificationFailureForRun emits a verification warning only while runID
// remains the newest terminal ordinary execution for the task. Restore and
// drill runs never create ordinary task alerts.
func (d *Dispatcher) RaiseVerificationFailureForRun(task model.Task, runID uint, message string) error {
	return d.raiseFailureForCurrentRun(task, runID, message, model.TaskRunStatusWarning, "warning", fmt.Sprintf("XR-VRFY-%d", task.ID), false, "ordinary")
}

// RaiseVerificationFailureForRestoreRun emits a verification warning only
// while runID remains the newest terminal restore execution for the task.
func (d *Dispatcher) RaiseVerificationFailureForRestoreRun(task model.Task, runID uint, message string) error {
	return d.raiseFailureForCurrentRun(task, runID, message, model.TaskRunStatusWarning, "warning", fmt.Sprintf("XR-VRFY-%d", task.ID), false, "restore")
}

// raiseFailureForCurrentRun persists one task-run/action alert while holding
// the task row lock. errorCode is the action identity: it separates task
// failure from verification failure without adding a schema column. The
// permanent task+run+action lookup is evaluated before the configurable
// notification dedup window, so replay never reopens or duplicates an alert.
func (d *Dispatcher) raiseFailureForCurrentRun(task model.Task, runID uint, message string, expectedStatus string, severity, errorCode string, retryable bool, triggerType string) error {
	if d == nil || d.DB == nil || task.ID == 0 || runID == 0 {
		return errors.New("task failure alert persistence unavailable")
	}
	policyName := ""
	if task.Policy != nil {
		policyName = task.Policy.Name
	}
	var created *model.Alert
	var createdNew bool
	err := d.DB.Transaction(func(tx *gorm.DB) error {
		var lockedTask model.Task
		taskResult := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id").Where("id = ?", task.ID).Limit(1).Find(&lockedTask)
		if taskResult.Error != nil {
			return taskResult.Error
		}
		if taskResult.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}

		// A durable row is the permanent replay receipt. Do not require it to
		// remain open/acked: manual resolution must not be undone by replay.
		var existing model.Alert
		existingResult := tx.Where(
			"task_id = ? AND task_run_id = ? AND error_code = ?",
			task.ID, runID, errorCode,
		).Limit(1).Find(&existing)
		if existingResult.Error != nil {
			return existingResult.Error
		}
		if existingResult.RowsAffected == 1 {
			created = &existing
			return nil
		}

		runQuery := tx.Where(
			"task_id = ? AND status IN ?",
			task.ID, model.TaskRunTerminalStatuses(),
		)
		if triggerType == "restore" {
			runQuery = runQuery.Where("trigger_type = ?", "restore")
		} else {
			runQuery = runQuery.Where("trigger_type NOT IN ?", []string{"restore", "drill"})
		}
		var latest model.TaskRun
		runResult := runQuery.Order("id DESC").Limit(1).Find(&latest)
		if runResult.Error != nil {
			return runResult.Error
		}
		if runResult.RowsAffected != 1 || latest.ID != runID || latest.Status != expectedStatus {
			return nil
		}

		now := time.Now()
		alert := model.Alert{
			NodeID:           task.NodeID,
			NodeName:         task.Node.Name,
			TaskID:           &task.ID,
			TaskRunID:        &runID,
			PolicyName:       policyName,
			Severity:         severity,
			Status:           "open",
			ErrorCode:        errorCode,
			Message:          message,
			Retryable:        retryable,
			TriggeredAt:      now,
			DeliveryDecision: model.AlertDeliveryDecisionPending,
		}
		if window := d.dedupWindow(); window > 0 {
			var count int64
			if err := tx.Model(&model.Alert{}).
				Where("node_id = ? AND error_code = ? AND created_at >= ?", alert.NodeID, alert.ErrorCode, now.Add(-window)).
				Where("task_id = ? AND task_run_id = ? AND status IN ?", task.ID, runID, []string{"open", "acked"}).
				Count(&count).Error; err != nil {
				return err
			}
			if count > 0 {
				return nil
			}
		}
		if err := tx.Create(&alert).Error; err != nil {
			return err
		}
		created = &alert
		createdNew = true
		return nil
	})
	if err != nil || created == nil {
		return err
	}
	if createdNew {
		alertsTotal.WithLabelValues(created.Severity).Inc()
	}
	return d.dispatchCreatedAlert(created)
}

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

// ResolveTaskAlertsForRun resolves only alerts caused by earlier ordinary
// executions. It is deliberately separate from ResolveTaskAlerts, which is
// the manual task-wide resolution API.
func (d *Dispatcher) ResolveTaskAlertsForRun(taskID, runID uint, note string) error {
	return d.resolveTaskAlertsForRun(taskID, runID, note, "ordinary")
}

// ResolveTaskAlertsForRestoreRun resolves only alerts caused by earlier
// restore executions. Ordinary backup alerts are never touched.
func (d *Dispatcher) ResolveTaskAlertsForRestoreRun(taskID, runID uint, note string) error {
	return d.resolveTaskAlertsForRun(taskID, runID, note, "restore")
}

func (d *Dispatcher) resolveTaskAlertsForRun(taskID, runID uint, note, triggerType string) error {
	if d == nil || d.DB == nil || taskID == 0 || runID == 0 {
		return errors.New("task alert resolution unavailable")
	}
	updates := map[string]interface{}{
		"status":           "resolved",
		"retryable":        false,
		"last_notified_at": time.Now(),
	}
	if note != "" {
		updates["message"] = note
	}
	return d.DB.Transaction(func(tx *gorm.DB) error {
		var lockedTask model.Task
		taskResult := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id").Where("id = ?", taskID).Limit(1).Find(&lockedTask)
		if taskResult.Error != nil {
			return taskResult.Error
		}
		if taskResult.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		runQuery := tx.Where(
			"id = ? AND task_id = ? AND status = ?",
			runID, taskID, model.TaskRunStatusSuccess,
		)
		if triggerType == "restore" {
			runQuery = runQuery.Where("trigger_type = ?", "restore")
		} else {
			runQuery = runQuery.Where("trigger_type NOT IN ?", []string{"restore", "drill"})
		}
		var successfulRun model.TaskRun
		runResult := runQuery.Limit(1).Find(&successfulRun)
		if runResult.Error != nil {
			return runResult.Error
		}
		if runResult.RowsAffected != 1 {
			return nil
		}

		alerts := tx.Model(&model.Alert{}).
			Where(`task_id = ? AND task_run_id IS NOT NULL AND task_run_id <= ?
				AND status IN ?`,
				taskID, runID, []string{"open", "acked"})
		if triggerType == "restore" {
			alerts = alerts.Where(`EXISTS (
					SELECT 1 FROM task_runs AS alert_run
					WHERE alert_run.id = alerts.task_run_id
						AND alert_run.task_id = ?
						AND alert_run.trigger_type = ?
				)`, taskID, "restore")
		} else {
			alerts = alerts.Where(`EXISTS (
					SELECT 1 FROM task_runs AS alert_run
					WHERE alert_run.id = alerts.task_run_id
						AND alert_run.task_id = ?
						AND alert_run.trigger_type NOT IN ?
				)`, taskID, []string{"restore", "drill"})
		}
		return alerts.Updates(updates).Error
	})
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

// raiseAndDispatch creates the alert in the database and dispatches it to
// integrations. The alert row is born with a pending delivery decision; the
// decision and all first-send channel intents are committed before any network
// call is made.
func (d *Dispatcher) raiseAndDispatch(alert *model.Alert) error {
	if d == nil || d.DB == nil || alert == nil {
		return errors.New("alert persistence unavailable")
	}
	if alert.DeliveryDecision == "" {
		alert.DeliveryDecision = model.AlertDeliveryDecisionPending
	}
	if deduped, err := d.inDedupWindow(*alert, time.Now()); err != nil {
		return err
	} else if deduped {
		existing, err := d.findDedupAlert(*alert, time.Now())
		if err != nil {
			return err
		}
		if existing == nil {
			return nil
		}
		// A dedup hit is also a replay opportunity. Resume only the durable
		// decision/intents already associated with the existing alert.
		return d.dispatchCreatedAlert(existing)
	}
	if err := d.DB.Create(alert).Error; err != nil {
		return err
	}
	alertsTotal.WithLabelValues(alert.Severity).Inc()
	return d.dispatchCreatedAlert(alert)
}

// dispatchCreatedAlert dispatches an alert whose durable row already exists.
// Keeping persistence separate lets causally bounded alert producers commit
// their ordering check and row insert in one transaction before fan-out.
func (d *Dispatcher) dispatchCreatedAlert(alert *model.Alert) error {
	if d == nil || d.DB == nil || alert == nil || alert.ID == 0 {
		return errors.New("alert dispatch persistence unavailable")
	}
	// Historical alerts predate the durable decision column. A NULL decision
	// with no existing delivery row is deliberately left unknown; replaying it
	// would invent a policy decision after the fact. Existing delivery rows
	// remain eligible for their own retry state machine.
	if strings.TrimSpace(alert.DeliveryDecision) == "" {
		var count int64
		if err := d.DB.Model(&model.AlertDelivery{}).
			Where("alert_id = ?", alert.ID).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return nil
		}
	}
	return d.dispatchCreatedAlertWithSender(alert, d.send)
}

func (d *Dispatcher) dispatchCreatedAlertWithSender(
	alert *model.Alert,
	sendFn func(model.Integration, model.Alert) error,
) error {
	if d == nil || d.DB == nil || alert == nil || alert.ID == 0 {
		return errors.New("alert dispatch persistence unavailable")
	}

	decision := strings.TrimSpace(alert.DeliveryDecision)
	if decision == "" || decision == model.AlertDeliveryDecisionPending {
		var err error
		decision, err = d.prepareDeliveryDecision(alert)
		if err != nil {
			return err
		}
	}
	if decision != model.AlertDeliveryDecisionDirect {
		// suppressed, escalated, and no_channel are terminal decisions for the
		// direct dispatcher. Their durable values prevent replay from fanning
		// out a notification that was intentionally not sent.
		return nil
	}
	return d.dispatchDeliveryRows(*alert, sendFn)
}

// prepareDeliveryDecision evaluates the non-network routing gates and commits
// the resulting decision plus all direct channel intents in one transaction.
func (d *Dispatcher) prepareDeliveryDecision(alert *model.Alert) (string, error) {
	var existing []model.AlertDelivery
	if err := d.DB.Where("alert_id = ?", alert.ID).Find(&existing).Error; err != nil {
		return "", err
	}
	if len(existing) > 0 {
		// A pre-migration row is evidence that this alert was already handed
		// to direct delivery. Never re-evaluate escalation/silence and never
		// create another logical intent for it.
		if err := d.commitDeliveryDecision(alert, model.AlertDeliveryDecisionDirect, "", nil); err != nil {
			return "", err
		}
		return model.AlertDeliveryDecisionDirect, nil
	}

	if d.EscalationResolver != nil {
		summary, err := d.EscalationResolver(*alert)
		if err != nil {
			// A resolver outage is not a no-channel decision. Leave pending so
			// RetryWorker can replay after the dependency recovers.
			return "", err
		}
		if summary != nil && summary.Enabled &&
			severityAtLeastForDispatch(alert.Severity, summary.MinSeverity) {
			if err := d.commitDeliveryDecision(alert, model.AlertDeliveryDecisionEscalated, model.AlertDeliveryReasonEscalation, nil); err != nil {
				return "", err
			}
			return model.AlertDeliveryDecisionEscalated, nil
		}
	}

	var integrations []model.Integration
	if err := d.DB.Where("enabled = ?", true).Find(&integrations).Error; err != nil {
		return "", err
	}
	if len(integrations) == 0 {
		if err := d.commitDeliveryDecision(alert, model.AlertDeliveryDecisionNoChannel, model.AlertDeliveryReasonNoEnabledChannel, nil); err != nil {
			return "", err
		}
		return model.AlertDeliveryDecisionNoChannel, nil
	}

	var openCount int64
	if err := d.DB.Model(&model.Alert{}).
		Where("node_id = ? AND status = ?", alert.NodeID, "open").
		Count(&openCount).Error; err != nil {
		return "", err
	}

	// An explicit pending decision was created at the alert boundary. If a
	// user resolves/acks the alert before replay, retain that routing intent
	// without reopening it; count the pending alert for its own threshold.
	if strings.TrimSpace(alert.DeliveryDecision) == model.AlertDeliveryDecisionPending && openCount == 0 {
		openCount = 1
	}
	now := time.Now()
	var node model.Node
	if alert.NodeID != 0 {
		if err := d.DB.First(&node, alert.NodeID).Error; err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return "", err
			}
			logger.Module("alerting").Info().
				Uint("alert_id", alert.ID).
				Uint("node_id", alert.NodeID).
				Msg("dispatch: 节点已删除，使用空 tags 继续")
		}
	}

	// 静默检查：若告警命中活跃静默规则，跳过所有通道投递。 A missing
	// historical silence table is treated as no active silences for compatibility
	silences, silenceErr := d.ActiveSilences(now)
	if silenceErr != nil && !isMissingSilenceTableError(silenceErr) {
		return "", silenceErr
	}
	if len(silences) > 0 {
		if matched := MatchSilence(*alert, node, silences, now); matched != nil {
			if err := d.commitDeliveryDecision(alert, model.AlertDeliveryDecisionSuppressed, model.AlertDeliveryReasonSilence, nil); err != nil {
				return "", err
			}
			logger.Module("alerting").Info().
				Uint("alert_id", alert.ID).
				Uint("silence_id", matched.ID).
				Msg("告警已静默，跳过投递")
			return model.AlertDeliveryDecisionSuppressed, nil
		}
	}
	key := GroupKey(alert.ErrorCode, alert.NodeID, splitNodeTags(node.Tags))
	if !GetSharedGrouping().ShouldSend(key, alert.ID) {
		if err := d.commitDeliveryDecision(alert, model.AlertDeliveryDecisionSuppressed, model.AlertDeliveryReasonGrouping, nil); err != nil {
			return "", err
		}
		logger.Module("alerting").Info().
			Uint("alert_id", alert.ID).
			Int("group_count", GetSharedGrouping().Count(key)).
			Msg("告警已被分组，跳过投递")
		return model.AlertDeliveryDecisionSuppressed, nil
	}

	eligible := make([]model.Integration, 0, len(integrations))
	for _, channel := range integrations {
		if int(openCount) < channel.FailThreshold {
			continue
		}
		if d.inCooldown(channel.ID, channel.CooldownMinutes, now) {
			continue
		}
		eligible = append(eligible, channel)
	}
	if len(eligible) == 0 {
		if err := d.commitDeliveryDecision(alert, model.AlertDeliveryDecisionSuppressed, model.AlertDeliveryReasonThresholdOrCooldown, nil); err != nil {
			return "", err
		}
		return model.AlertDeliveryDecisionSuppressed, nil
	}
	if err := d.commitDeliveryDecision(alert, model.AlertDeliveryDecisionDirect, "", eligible); err != nil {
		return "", err
	}
	return model.AlertDeliveryDecisionDirect, nil
}

// isMissingSilenceTableError preserves compatibility with databases created
// before the silence migration while allowing real read failures to remain
// retryable instead of being recorded as a no-channel decision.
func isMissingSilenceTableError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such table") ||
		strings.Contains(msg, "relation \"silences\" does not exist")
}

// commitDeliveryDecision is the durable boundary between routing and network
// I/O. For direct dispatch it inserts one pending logical intent per channel.
func (d *Dispatcher) commitDeliveryDecision(alert *model.Alert, decision, reason string, channels []model.Integration) error {
	if alert == nil || alert.ID == 0 {
		return errors.New("commit delivery decision: invalid alert")
	}
	return d.DB.Transaction(func(tx *gorm.DB) error {
		var current model.Alert
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, alert.ID).Error; err != nil {
			return err
		}
		currentDecision := strings.TrimSpace(current.DeliveryDecision)
		if decision == model.AlertDeliveryDecisionDirect &&
			(currentDecision == "" ||
				currentDecision == model.AlertDeliveryDecisionPending ||
				currentDecision == model.AlertDeliveryDecisionDirect) {
			for _, channel := range channels {
				if _, err := ensureDeliveryIntentTx(tx, current.ID, channel.ID); err != nil {
					return err
				}
			}
		}
		if currentDecision != "" && currentDecision != model.AlertDeliveryDecisionPending {
			alert.DeliveryDecision = currentDecision
			alert.DeliveryReason = current.DeliveryReason
			alert.DeliveryDecidedAt = current.DeliveryDecidedAt
			return nil
		}
		now := time.Now()
		if err := tx.Model(&model.Alert{}).Where("id = ?", current.ID).Updates(map[string]interface{}{
			"delivery_decision":   decision,
			"delivery_reason":     reason,
			"delivery_decided_at": now,
		}).Error; err != nil {
			return err
		}
		alert.DeliveryDecision = decision
		alert.DeliveryReason = reason
		alert.DeliveryDecidedAt = &now
		return nil
	})
}

func ensureDeliveryIntentTx(tx *gorm.DB, alertID, integrationID uint) (model.AlertDelivery, error) {
	return ensureDeliveryIntentWithKeyTx(
		tx, alertID, integrationID, deliveryIntentKey(alertID, integrationID), true,
	)
}

func ensureEscalationDeliveryIntentTx(
	tx *gorm.DB,
	alertID, eventID, integrationID uint,
) (model.AlertDelivery, error) {
	return ensureDeliveryIntentWithKeyTx(
		tx, alertID, integrationID,
		escalationDeliveryIntentKey(alertID, eventID, integrationID), false,
	)
}

func ensureDeliveryIntentWithKeyTx(
	tx *gorm.DB,
	alertID, integrationID uint,
	key string,
	adoptLegacy bool,
) (model.AlertDelivery, error) {
	var intent model.AlertDelivery
	err := tx.Where("delivery_key = ?", key).First(&intent).Error
	if err == nil {
		return intent, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return intent, err
	}
	if adoptLegacy {
		// Adopt one pre-migration row, preserving any duplicate historical rows.
		err = tx.Where(
			"alert_id = ? AND integration_id = ? AND (delivery_key = '' OR delivery_key IS NULL)",
			alertID, integrationID,
		).Order("id ASC").First(&intent).Error
		if err == nil {
			if intent.DeliveryKey == "" {
				if updateErr := tx.Model(&intent).Where("delivery_key = '' OR delivery_key IS NULL").
					Update("delivery_key", key).Error; updateErr != nil {
					return intent, updateErr
				}
				intent.DeliveryKey = key
			}
			if intent.Decision == "" {
				intent.Decision = "deliver"
				if updateErr := tx.Model(&intent).Update("decision", "deliver").Error; updateErr != nil {
					return intent, updateErr
				}
			}
			return intent, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return intent, err
		}
	}
	intent = model.AlertDelivery{
		AlertID:       alertID,
		IntegrationID: integrationID,
		Status:        model.AlertDeliveryStatusPending,
		Decision:      "deliver",
		DeliveryKey:   key,
	}
	if err := tx.Create(&intent).Error; err != nil {
		return intent, err
	}
	return intent, nil
}

func (d *Dispatcher) dispatchDeliveryRows(alert model.Alert, sendFn func(model.Integration, model.Alert) error) error {
	var rows []model.AlertDelivery
	now := time.Now()
	if err := d.DB.Where(
		"alert_id = ? AND (decision = ? OR decision = '' OR decision IS NULL) AND "+
			"(status = ? OR (status = ? AND (next_retry_at IS NULL OR next_retry_at <= ?)) OR "+
			"(status = ? AND lease_expires_at IS NOT NULL AND lease_expires_at <= ?))",
		alert.ID, "deliver",
		model.AlertDeliveryStatusPending,
		model.AlertDeliveryStatusRetrying, now,
		model.AlertDeliveryStatusSending, now,
	).Find(&rows).Error; err != nil {
		return err
	}
	return d.dispatchDeliveryCandidates(context.Background(), alert, rows, sendFn)
}

func (d *Dispatcher) dispatchDeliveryCandidates(
	ctx context.Context,
	alert model.Alert,
	rows []model.AlertDelivery,
	sendFn func(model.Integration, model.Alert) error,
) error {
	if len(rows) == 0 {
		return nil
	}
	var wg sync.WaitGroup
	deliveryDone := make(chan struct{})
	for _, row := range rows {
		wg.Add(1)
		go func(intent model.AlertDelivery) {
			defer wg.Done()
			if err := runDeliveryAttempt(ctx, d.DB, intent, sendFn, false); err != nil {
				logger.Module("alerting").Warn().
					Uint("alert_id", alert.ID).
					Uint("delivery_id", intent.ID).
					Err(err).
					Msg("保存告警投递结果失败")
			}
		}(row)
	}
	go func() {
		wg.Wait()
		close(deliveryDone)
		d.updateLastNotifiedAt(&alert)
	}()
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

func (d *Dispatcher) findDedupAlert(alert model.Alert, now time.Time) (*model.Alert, error) {
	window := d.dedupWindow()
	if window <= 0 {
		return nil, nil
	}
	query := d.DB.Model(&model.Alert{}).
		Where("node_id = ? AND error_code = ? AND created_at >= ?", alert.NodeID, alert.ErrorCode, now.Add(-window)).
		Where("status IN ?", []string{"open", "acked"})
	if alert.TaskID == nil {
		query = query.Where("task_id IS NULL")
	} else {
		query = query.Where("task_id = ?", *alert.TaskID)
	}
	var existing model.Alert
	if err := query.Order("created_at DESC").First(&existing).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &existing, nil
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

// send routes one alert through the registered integration sender.
func (d *Dispatcher) send(channel model.Integration, alert model.Alert) error {
	sender, ok := senderRegistry[strings.ToLower(strings.TrimSpace(channel.Type))]
	if !ok {
		return fmt.Errorf("unsupported integration type: %s", channel.Type)
	}
	body := payload{
		Title:      alert.ErrorCode,
		Severity:   alert.Severity,
		Status:     alert.Status,
		NodeName:   alert.NodeName,
		TaskID:     alert.TaskID,
		PolicyName: alert.PolicyName,
		ErrorCode:  alert.ErrorCode,
		Message:    alert.Message,
		Triggered:  alert.TriggeredAt,
	}
	return sender.Send(getHTTPClient(channel.ProxyURL), channel.Endpoint, channel.Secret, body)
}

// ---- proxy client caching ----

// proxyClients 缓存按代理 URL 创建的 HTTP 客户端，避免每次调用创建新 Transport
var proxyClients sync.Map // proxyURL -> *proxyClientEntry

// proxyClientEntry wraps an HTTP client and stores the last-access timestamp
// atomically. sync.Map only synchronizes map operations; it does not protect
// fields inside a stored pointer.
type proxyClientEntry struct {
	client   *http.Client
	accessed atomic.Int64 // UTC UnixNano
	mu       sync.Mutex
}

func newProxyClientEntry(client *http.Client, now time.Time) *proxyClientEntry {
	entry := &proxyClientEntry{client: client}
	entry.accessed.Store(now.UTC().UnixNano())
	return entry
}

func (entry *proxyClientEntry) touch(now time.Time) {
	entry.mu.Lock()
	entry.accessed.Store(now.UTC().UnixNano())
	entry.mu.Unlock()
}

func (entry *proxyClientEntry) lastAccess() time.Time {
	entry.mu.Lock()
	defer entry.mu.Unlock()
	ns := entry.accessed.Load()
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns).UTC()
}

func (entry *proxyClientEntry) evictIfExpired(key interface{}, now time.Time) {
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if now.Sub(time.Unix(0, entry.accessed.Load())) > proxyClientTTL {
		// The entry lock serializes this timestamp decision with touch. The
		// map CAS still protects a replacement installed under the same key.
		proxyClients.CompareAndDelete(key, entry)
	}
}

// proxyClientTTL 是代理客户端的缓存 TTL，超时未访问的条目会被后台清理协程移除。
const proxyClientTTL = 10 * time.Minute

// proxyClientCleanupInterval 是后台清理协程的运行间隔。
const proxyClientCleanupInterval = 5 * time.Minute

func cleanupExpiredProxyClients(now time.Time) {
	proxyClients.Range(func(key, value interface{}) bool {
		entry, ok := value.(*proxyClientEntry)
		if !ok {
			proxyClients.Delete(key)
			return true
		}
		entry.evictIfExpired(key, now)
		return true
	})
}

func init() {
	go func() {
		ticker := time.NewTicker(proxyClientCleanupInterval)
		defer ticker.Stop()
		for range ticker.C {
			cleanupExpiredProxyClients(time.Now())
		}
	}()
}

// getHTTPClient 根据代理配置返回 HTTP 客户端（带缓存）
func getHTTPClient(proxyURL string) *http.Client {
	if proxyURL == "" {
		return defaultHTTPClient
	}
	if cached, ok := proxyClients.Load(proxyURL); ok {
		entry, ok := cached.(*proxyClientEntry)
		if !ok {
			proxyClients.Delete(proxyURL)
			return defaultHTTPClient
		}
		entry.touch(time.Now())
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
	entry := newProxyClientEntry(client, time.Now())
	actual, loaded := proxyClients.LoadOrStore(proxyURL, entry)
	if loaded {
		if existing, ok := actual.(*proxyClientEntry); ok {
			existing.touch(time.Now())
			return existing.client
		}
		proxyClients.Store(proxyURL, entry)
		return client
	}
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

// EnqueueEscalationDeliveriesTx materializes one durable deliverable intent
// per enabled integration for an already-created escalation event. It is
// deliberately transaction-scoped: callers must invoke it before the event
// transaction commits, and it never performs network I/O.
func (d *Dispatcher) EnqueueEscalationDeliveriesTx(
	tx *gorm.DB,
	alert model.Alert,
	event model.AlertEscalationEvent,
	integrationIDs []uint,
) ([]uint, error) {
	if d == nil || d.DB == nil || tx == nil || alert.ID == 0 || event.ID == 0 || event.AlertID != alert.ID {
		return nil, errors.New("enqueue escalation deliveries: invalid identifiers")
	}
	if len(integrationIDs) == 0 {
		return nil, nil
	}

	var integrations []model.Integration
	if err := tx.Where("id IN ? AND enabled = ?", integrationIDs, true).Find(&integrations).Error; err != nil {
		return nil, err
	}
	enabled := make(map[uint]struct{}, len(integrations))
	for _, integration := range integrations {
		enabled[integration.ID] = struct{}{}
	}

	intentIDs := make([]uint, 0, len(integrations))
	seen := make(map[uint]struct{}, len(integrations))
	for _, integrationID := range integrationIDs {
		if _, duplicate := seen[integrationID]; duplicate {
			continue
		}
		seen[integrationID] = struct{}{}
		if _, ok := enabled[integrationID]; !ok {
			continue
		}
		intent, err := ensureEscalationDeliveryIntentTx(tx, alert.ID, event.ID, integrationID)
		if err != nil {
			return nil, err
		}
		intentIDs = append(intentIDs, intent.ID)
	}
	return intentIDs, nil
}

// DispatchEscalationDeliveries dispatches only the intents materialized for
// one committed event. Each row still goes through the existing lease/CAS
// state machine; this method only runs after the fire transaction commits.
func (d *Dispatcher) DispatchEscalationDeliveries(
	ctx context.Context,
	alert model.Alert,
	eventID uint,
	intentIDs []uint,
) error {
	if d == nil || d.DB == nil || alert.ID == 0 || eventID == 0 {
		return errors.New("dispatch escalation deliveries: invalid identifiers")
	}
	if len(intentIDs) == 0 {
		return nil
	}
	now := time.Now()
	var rows []model.AlertDelivery
	if err := d.DB.WithContext(ctx).Where(
		"id IN ? AND alert_id = ? AND (decision = ? OR decision = '' OR decision IS NULL) AND "+
			"(status = ? OR (status = ? AND (next_retry_at IS NULL OR next_retry_at <= ?)) OR "+
			"(status = ? AND lease_expires_at IS NOT NULL AND lease_expires_at <= ?))",
		intentIDs, alert.ID, "deliver",
		model.AlertDeliveryStatusPending,
		model.AlertDeliveryStatusRetrying, now,
		model.AlertDeliveryStatusSending, now,
	).Find(&rows).Error; err != nil {
		return err
	}
	filtered := rows[:0]
	for _, row := range rows {
		if row.DeliveryKey == escalationDeliveryIntentKey(alert.ID, eventID, row.IntegrationID) {
			filtered = append(filtered, row)
		}
	}
	return d.dispatchDeliveryCandidates(ctx, alert, filtered, d.send)
}

var errDeliveryAlreadySent = errors.New("already sent")

// RetryDeliveryByID performs one explicit manual attempt for an existing
// logical intent. It shares the lease and attempt CAS used by automatic
// retries, so a concurrent sender cannot duplicate a confirmed delivery.
func (d *Dispatcher) RetryDeliveryByID(ctx context.Context, deliveryID uint) (model.AlertDelivery, error) {
	var intent model.AlertDelivery
	if d == nil || d.DB == nil || deliveryID == 0 {
		return intent, errors.New("retry delivery: invalid identifiers")
	}
	if err := d.DB.WithContext(ctx).First(&intent, deliveryID).Error; err != nil {
		return intent, err
	}
	return d.retryDeliveryIntent(ctx, intent)
}

// RetryDelivery performs one explicit manual attempt for an alert/channel.
// Existing rows are selected by newest logical intent, preserving escalation
// event identity. Only an alert/channel with no prior intent gets the legacy
// direct key behavior.
func (d *Dispatcher) RetryDelivery(ctx context.Context, alertID, integrationID uint) (model.AlertDelivery, error) {
	var intent model.AlertDelivery
	if d == nil || d.DB == nil || alertID == 0 || integrationID == 0 {
		return intent, errors.New("retry delivery: invalid identifiers")
	}
	if err := d.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var alert model.Alert
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&alert, alertID).Error; err != nil {
			return err
		}
		err := tx.Where("alert_id = ? AND integration_id = ?", alertID, integrationID).
			Order("id DESC").First(&intent).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if strings.TrimSpace(alert.DeliveryDecision) == model.AlertDeliveryDecisionEscalated {
				return gorm.ErrRecordNotFound
			}
			var ensureErr error
			intent, ensureErr = ensureDeliveryIntentTx(tx, alert.ID, integrationID)
			return ensureErr
		}
		if err != nil {
			return err
		}
		return canonicalizeDeliveryIntentTx(tx, &intent, deliveryIntentKey(alertID, integrationID))
	}); err != nil {
		return intent, err
	}
	return d.retryDeliveryIntent(ctx, intent)
}

// CanonicalizeRetryCandidates resolves failed rows into logical retry
// identities. Distinct nonempty delivery keys remain independent. Blank-key
// historical rows share the canonical direct intent for their channel, with
// an existing direct key taking precedence over legacy duplicates.
func (d *Dispatcher) CanonicalizeRetryCandidates(
	ctx context.Context, alertID uint, records []model.AlertDelivery,
) ([]model.AlertDelivery, error) {
	if d == nil || d.DB == nil || alertID == 0 {
		return nil, errors.New("canonicalize retry candidates: invalid identifiers")
	}
	candidates := make([]model.AlertDelivery, 0, len(records))
	if len(records) == 0 {
		return candidates, nil
	}
	err := d.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var alert model.Alert
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&alert, alertID).Error; err != nil {
			return err
		}
		seenIDs := make(map[uint]struct{}, len(records))
		for _, record := range records {
			if record.ID == 0 || record.AlertID != alertID || record.IntegrationID == 0 {
				return errors.New("canonicalize retry candidates: invalid delivery")
			}
			candidate, err := selectRetryCandidateTx(tx, alertID, record)
			if err != nil {
				return err
			}
			if candidate.ID == 0 {
				return errors.New("canonicalize retry candidates: missing delivery")
			}
			if _, seen := seenIDs[candidate.ID]; seen {
				continue
			}
			key := strings.TrimSpace(candidate.DeliveryKey)
			if key == "" {
				key = deliveryIntentKey(alertID, candidate.IntegrationID)
			}
			if err := canonicalizeDeliveryIntentTx(tx, &candidate, key); err != nil {
				return err
			}
			seenIDs[candidate.ID] = struct{}{}
			candidates = append(candidates, candidate)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return candidates, nil
}

func selectRetryCandidateTx(tx *gorm.DB, alertID uint, record model.AlertDelivery) (model.AlertDelivery, error) {
	var candidate model.AlertDelivery
	key := strings.TrimSpace(record.DeliveryKey)
	if key != "" {
		err := tx.Where(
			"alert_id = ? AND integration_id = ? AND delivery_key = ?",
			alertID, record.IntegrationID, key,
		).Order("id DESC").First(&candidate).Error
		if err == nil {
			return candidate, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return candidate, err
		}
		return record, nil
	}

	directKey := deliveryIntentKey(alertID, record.IntegrationID)
	err := tx.Where(
		"alert_id = ? AND integration_id = ? AND delivery_key = ?",
		alertID, record.IntegrationID, directKey,
	).Order("id DESC").First(&candidate).Error
	if err == nil {
		return candidate, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return candidate, err
	}
	err = tx.Where(
		"alert_id = ? AND integration_id = ? AND (delivery_key = '' OR delivery_key IS NULL)",
		alertID, record.IntegrationID,
	).Order("id DESC").First(&candidate).Error
	if err == nil {
		return candidate, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return candidate, err
	}
	return record, nil
}

func canonicalizeDeliveryIntentTx(tx *gorm.DB, intent *model.AlertDelivery, key string) error {
	if intent == nil || intent.ID == 0 {
		return errors.New("canonicalize delivery: invalid intent")
	}
	if intent.DeliveryKey == "" {
		if err := tx.Model(intent).Where("delivery_key = '' OR delivery_key IS NULL").
			Update("delivery_key", key).Error; err != nil {
			return err
		}
		intent.DeliveryKey = key
	}
	if intent.Decision == "" {
		if err := tx.Model(intent).Update("decision", "deliver").Error; err != nil {
			return err
		}
		intent.Decision = "deliver"
	}
	return nil
}

func (d *Dispatcher) retryDeliveryIntent(ctx context.Context, intent model.AlertDelivery) (model.AlertDelivery, error) {
	if intent.Status == model.AlertDeliveryStatusSent {
		return intent, errDeliveryAlreadySent
	}
	if err := runDeliveryAttempt(ctx, d.DB, intent, d.send, true); err != nil {
		var latest model.AlertDelivery
		if loadErr := d.DB.WithContext(ctx).First(&latest, intent.ID).Error; loadErr == nil {
			intent = latest
		}
		return intent, err
	}
	if err := d.DB.WithContext(ctx).First(&intent, intent.ID).Error; err != nil {
		return intent, err
	}
	return intent, nil
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
	return sendEmailWithoutTLS(addr, host, auth, from, to, message)
}

const (
	smtpDialTimeout      = 15 * time.Second
	smtpOperationTimeout = 30 * time.Second
)

func sendEmailWithoutTLS(addr, host string, auth smtp.Auth, from string, to []string, msg []byte) error {
	conn, err := (&net.Dialer{Timeout: smtpDialTimeout}).Dial("tcp", addr)
	if err != nil {
		return fmt.Errorf("SMTP 连接失败: %w", err)
	}
	defer conn.Close() //nolint:errcheck
	_ = conn.SetDeadline(time.Now().Add(smtpOperationTimeout))
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		return fmt.Errorf("创建 SMTP 客户端失败: %w", err)
	}
	defer c.Close() //nolint:errcheck
	return smtpSend(c, auth, from, to, msg)
}

// sendEmailWithTLS 强制使用 TLS 发送邮件
func sendEmailWithTLS(addr, host, port string, auth smtp.Auth, from string, to []string, msg []byte) error {
	tlsConfig := &tls.Config{ServerName: host}

	if port == "465" {
		// 隐式 TLS（SMTPS）
		dialer := &net.Dialer{Timeout: smtpDialTimeout}
		conn, err := tls.DialWithDialer(dialer, "tcp", addr, tlsConfig)
		if err != nil {
			return fmt.Errorf("TLS 连接失败: %w", err)
		}
		defer conn.Close() //nolint:errcheck
		_ = conn.SetDeadline(time.Now().Add(smtpOperationTimeout))
		c, err := smtp.NewClient(conn, host)
		if err != nil {
			return fmt.Errorf("创建 SMTP 客户端失败: %w", err)
		}
		defer c.Close() //nolint:errcheck
		return smtpSend(c, auth, from, to, msg)
	}

	// 显式 TLS（STARTTLS，端口 587 等）
	conn, err := (&net.Dialer{Timeout: smtpDialTimeout}).Dial("tcp", addr)
	if err != nil {
		return fmt.Errorf("SMTP 连接失败: %w", err)
	}
	defer conn.Close() //nolint:errcheck
	_ = conn.SetDeadline(time.Now().Add(smtpOperationTimeout))
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		return fmt.Errorf("创建 SMTP 客户端失败: %w", err)
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
		var existingAlert model.Alert
		if err := d.DB.First(&existingAlert, existing).Error; err != nil {
			return 0, false, err
		}
		if err := d.dispatchCreatedAlert(&existingAlert); err != nil {
			return existing, false, err
		}
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
