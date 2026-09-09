package model

import "time"

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
