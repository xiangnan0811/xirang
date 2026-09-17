package model

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

// TaskRunCronCursorMode records which durable field owns a cron retry's
// readiness deadline. An empty mode is the historical format where
// Task.NextRunAt doubled as the retry deadline. The current format keeps the
// regular cron cursor on Task.NextRunAt and stores the retry deadline on the
// TaskRunEffect row.
type TaskRunCronCursorMode string

const (
	TaskRunCronCursorModeLegacy    TaskRunCronCursorMode = ""
	TaskRunCronCursorModeRegularV1 TaskRunCronCursorMode = "regular_cursor_v1"
)

// ParseTaskRunEffectCronCursorMode parses retry provenance without converting
// malformed or unknown values into legacy. Legacy is represented only by an
// omitted or empty string field.
func ParseTaskRunEffectCronCursorMode(payload string) (TaskRunCronCursorMode, error) {
	if strings.TrimSpace(payload) == "" {
		return TaskRunCronCursorModeLegacy, fmt.Errorf("retry effect payload is empty")
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(payload), &top); err != nil {
		return TaskRunCronCursorModeLegacy, fmt.Errorf("decode retry effect payload: %w", err)
	}
	if top == nil {
		return TaskRunCronCursorModeLegacy, fmt.Errorf("retry effect payload must be an object")
	}
	raw, ok := top["cron_cursor_mode"]
	if !ok {
		return TaskRunCronCursorModeLegacy, nil
	}
	var mode string
	if err := json.Unmarshal(raw, &mode); err != nil {
		return TaskRunCronCursorModeLegacy, fmt.Errorf("decode cron cursor mode: %w", err)
	}
	mode = strings.TrimSpace(mode)
	if mode == "" {
		return TaskRunCronCursorModeLegacy, nil
	}
	if mode != string(TaskRunCronCursorModeRegularV1) {
		return TaskRunCronCursorModeLegacy, fmt.Errorf("unsupported cron cursor mode %q", mode)
	}
	return TaskRunCronCursorModeRegularV1, nil
}

// RetryEffectCronCursorModeTx returns the mode carried by the retry effect
// generated for exactly predecessorRunID. Missing effects are legacy for
// compatibility. Callers must invoke this on the transaction that already
// locks the associated Task/TaskRun rows.
func RetryEffectCronCursorModeTx(tx *gorm.DB, predecessorRunID uint) (TaskRunCronCursorMode, error) {
	if tx == nil {
		return TaskRunCronCursorModeLegacy, fmt.Errorf("task run effect query unavailable")
	}
	if predecessorRunID == 0 {
		return TaskRunCronCursorModeLegacy, nil
	}
	var effect TaskRunEffect
	result := tx.Where("task_run_id = ? AND effect_type = ?", predecessorRunID, TaskRunEffectTypeRetry).
		Order("id DESC").Limit(1).Find(&effect)
	if result.Error != nil {
		return TaskRunCronCursorModeLegacy, result.Error
	}
	if result.RowsAffected != 1 {
		return TaskRunCronCursorModeLegacy, nil
	}
	return ParseTaskRunEffectCronCursorMode(effect.Payload)
}

// LatestTaskRetryEffectCronCursorModeTx returns the mode on the newest
// ordinary retry effect for taskID. This aggregate helper is only for
// configuration/reconciliation paths that do not have the predecessor run ID;
// terminal transitions must use RetryEffectCronCursorModeTx instead.
func LatestTaskRetryEffectCronCursorModeTx(tx *gorm.DB, taskID uint) (TaskRunCronCursorMode, error) {
	if tx == nil {
		return TaskRunCronCursorModeLegacy, fmt.Errorf("task run effect query unavailable")
	}
	if taskID == 0 {
		return TaskRunCronCursorModeLegacy, nil
	}
	var effect TaskRunEffect
	result := tx.Where(
		"effect_type = ? AND task_run_id IN "+
			"(SELECT id FROM task_runs WHERE task_id = ? AND trigger_type NOT IN ?)",
		TaskRunEffectTypeRetry, taskID, []string{"drill", "restore"},
	).Order("id DESC").Limit(1).Find(&effect)
	if result.Error != nil {
		return TaskRunCronCursorModeLegacy, result.Error
	}
	if result.RowsAffected != 1 {
		return TaskRunCronCursorModeLegacy, nil
	}
	return ParseTaskRunEffectCronCursorMode(effect.Payload)
}

const (
	TaskRunEffectStatusPending   = "pending"
	TaskRunEffectStatusRunning   = "running"
	TaskRunEffectStatusSucceeded = "succeeded"
	TaskRunEffectStatusFailed    = "failed"

	TaskRunEffectTypeAutomation     = "automation"
	TaskRunEffectTypeAutomationRule = "automation_rule"
	TaskRunEffectTypeRetry          = "retry"
	TaskRunEffectTypeDownstream     = "downstream"
	TaskRunEffectTypeDownstreamSkip = "downstream_skip"
	TaskRunEffectTypeAlert          = "alert"
)

// TaskRunEffect is a task-run-scoped durable post-commit effect. Effects are
// created in the same short transaction as the terminal Task/TaskRun pair and
// consumed after commit by the owning manager. The unique task-run/key pair
// makes enqueueing idempotent across retries and restarts.
type TaskRunEffect struct {
	ID              uint       `gorm:"primaryKey" json:"id"`
	TaskRunID       uint       `gorm:"not null;index;uniqueIndex:idx_task_run_effect_key" json:"task_run_id"`
	EffectKey       string     `gorm:"size:160;not null;uniqueIndex:idx_task_run_effect_key" json:"effect_key"`
	EffectType      string     `gorm:"size:32;not null;index" json:"effect_type"`
	Payload         string     `gorm:"type:text;not null;default:''" json:"-"`
	Status          string     `gorm:"size:16;not null;default:pending;index" json:"status"`
	Attempts        int        `gorm:"not null;default:0" json:"attempts"`
	NextAttemptAt   *time.Time `gorm:"index" json:"-"`
	ClaimedBy       string     `gorm:"size:64;not null;default:''" json:"-"`
	ClaimLeaseUntil *time.Time `gorm:"index" json:"-"`
	LastError       string     `gorm:"type:text;not null;default:''" json:"last_error,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}
