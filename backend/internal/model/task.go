package model

import (
	"fmt"
	"strings"
	"time"

	"xirang/backend/internal/secure"

	"gorm.io/gorm"
)

const (
	TaskRunNodeIDLegacyUnknown uint = 0

	TaskRunStatusPending  = "pending"
	TaskRunStatusRunning  = "running"
	TaskRunStatusRetrying = "retrying"
	TaskRunStatusSuccess  = "success"
	TaskRunStatusFailed   = "failed"
	TaskRunStatusCanceled = "canceled"
	TaskRunStatusWarning  = "warning"
	TaskRunStatusSkipped  = "skipped"
)

var (
	taskRunActiveStatuses = [...]string{
		TaskRunStatusPending,
		TaskRunStatusRunning,
		TaskRunStatusRetrying,
	}
	taskRunTerminalStatuses = [...]string{
		TaskRunStatusSuccess,
		TaskRunStatusFailed,
		TaskRunStatusCanceled,
		TaskRunStatusWarning,
		TaskRunStatusSkipped,
	}
)

func TaskRunActiveStatuses() []string {
	return append([]string(nil), taskRunActiveStatuses[:]...)
}

func TaskRunTerminalStatuses() []string {
	return append([]string(nil), taskRunTerminalStatuses[:]...)
}

func IsActiveTaskRunStatus(status string) bool {
	for _, active := range taskRunActiveStatuses {
		if status == active {
			return true
		}
	}
	return false
}

func IsTerminalTaskRunStatus(status string) bool {
	for _, terminal := range taskRunTerminalStatuses {
		if status == terminal {
			return true
		}
	}
	return false
}

func IsKnownTaskRunStatus(status string) bool {
	return IsActiveTaskRunStatus(status) || IsTerminalTaskRunStatus(status)
}

// IsTaskRunNodeSnapshotAuthoritative separates ordinary positive node identity
// from the migration-owned legacy_unknown terminal-history sentinel.
func IsTaskRunNodeSnapshotAuthoritative(nodeID uint) bool {
	return nodeID > TaskRunNodeIDLegacyUnknown
}

type Task struct {
	ID                 uint       `gorm:"primaryKey" json:"id"`
	Name               string     `gorm:"size:128;not null" json:"name"`
	NodeID             uint       `gorm:"not null;index" json:"node_id"`
	Node               Node       `json:"node,omitempty"`
	PolicyID           *uint      `gorm:"index" json:"policy_id,omitempty"`
	Policy             *Policy    `json:"policy,omitempty"`
	DependsOnTaskID    *uint      `gorm:"index" json:"depends_on_task_id,omitempty"`
	Command            string     `gorm:"type:text" json:"command"`
	RsyncSource        string     `gorm:"size:512" json:"rsync_source"`
	RsyncTarget        string     `gorm:"size:512" json:"rsync_target"`
	ExecutorType       string     `gorm:"size:32;not null;default:local" json:"executor_type"`
	ExecutorConfig     string     `gorm:"type:text" json:"-"`
	CronSpec           string     `gorm:"size:128" json:"cron_spec"`
	Status             string     `gorm:"size:32;not null;index" json:"status"`
	BatchID            string     `gorm:"size:64;index" json:"batch_id,omitempty"`
	Source             string     `gorm:"size:32;not null;default:manual" json:"source"`
	VerifyStatus       string     `gorm:"size:16;not null;default:none" json:"verify_status"`
	RetryCount         int        `gorm:"not null;default:0" json:"retry_count"`
	Enabled            bool       `gorm:"not null;default:true" json:"enabled"`
	SkipNext           bool       `gorm:"not null;default:false" json:"skip_next"`
	LastError          string     `gorm:"type:text" json:"last_error"`
	LastRunAt          *time.Time `json:"last_run_at"`
	NextRunAt          *time.Time `json:"next_run_at"`
	ArchivedAt         *time.Time `json:"archived_at,omitempty"`
	Progress           *int       `gorm:"-" json:"progress,omitempty"`
	EscalationPolicyID *uint      `gorm:"index" json:"escalation_policy_id"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

func (t *Task) BeforeSave(_ *gorm.DB) error {
	if strings.TrimSpace(t.ExecutorConfig) == "" {
		return nil
	}
	encrypted, err := secure.EncryptIfNeeded(t.ExecutorConfig)
	if err != nil {
		return err
	}
	t.ExecutorConfig = encrypted
	return nil
}

func (t *Task) AfterFind(_ *gorm.DB) error {
	if strings.TrimSpace(t.ExecutorConfig) == "" {
		return nil
	}
	decrypted, err := secure.DecryptIfNeeded(t.ExecutorConfig)
	if err != nil {
		return err
	}
	t.ExecutorConfig = decrypted
	return nil
}

type TaskRun struct {
	ID                uint       `gorm:"primaryKey" json:"id"`
	TaskID            uint       `gorm:"not null;index;uniqueIndex:idx_task_runs_active_drill,where:trigger_type = 'drill' AND (status = 'pending' OR status = 'running' OR status = 'retrying')" json:"task_id"`
	Task              Task       `gorm:"foreignKey:TaskID" json:"-"`
	NodeIDSnapshot    uint       `gorm:"not null;index:idx_task_runs_node_snapshot_status,priority:1" json:"-"`
	TriggerType       string     `gorm:"size:32;not null;default:manual" json:"trigger_type"`
	Status            string     `gorm:"size:32;not null;default:pending;index;index:idx_task_runs_status_finished_at,priority:1;index:idx_task_runs_node_snapshot_status,priority:2" json:"status"`
	ChainRunID        string     `gorm:"size:64;index" json:"chain_run_id,omitempty"`
	UpstreamTaskRunID *uint      `gorm:"index" json:"upstream_task_run_id,omitempty"`
	SkipReason        string     `gorm:"type:text" json:"skip_reason,omitempty"`
	StartedAt         *time.Time `gorm:"index:idx_task_runs_started_at" json:"started_at"`
	FinishedAt        *time.Time `gorm:"index:idx_task_runs_status_finished_at,priority:2" json:"finished_at"`
	DurationMs        int64      `gorm:"not null;default:0" json:"duration_ms"`
	VerifyStatus      string     `gorm:"size:16;not null;default:none" json:"verify_status"`
	ThroughputMbps    float64    `gorm:"not null;default:0" json:"throughput_mbps"`
	Progress          int        `gorm:"not null;default:0" json:"progress"`
	LastError         string     `gorm:"type:text" json:"last_error"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

// BeforeCreate freezes the Task's current node for every GORM TaskRun writer.
// Paired migration guards remain the authoritative defense for raw SQL and
// reject explicit mismatches; the hook keeps legacy TaskRun producers on the
// same immutable identity contract without duplicating node lookups.
func (r *TaskRun) BeforeCreate(tx *gorm.DB) error {
	if r == nil || tx == nil {
		return fmt.Errorf("task run authority is unavailable")
	}
	if r.TaskID == 0 {
		return fmt.Errorf("task run requires an authoritative task")
	}
	var taskNode struct {
		NodeID uint
	}
	result := tx.Model(&Task{}).Select("node_id").Where("id = ?", r.TaskID).Limit(1).Find(&taskNode)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("task run requires an authoritative task")
	}
	if !IsTaskRunNodeSnapshotAuthoritative(taskNode.NodeID) {
		return fmt.Errorf("task run requires an authoritative task node snapshot")
	}
	if r.NodeIDSnapshot == 0 {
		r.NodeIDSnapshot = taskNode.NodeID
		return nil
	}
	if r.NodeIDSnapshot != taskNode.NodeID {
		return fmt.Errorf("task run node snapshot %d does not match task node %d", r.NodeIDSnapshot, taskNode.NodeID)
	}
	return nil
}

type TaskLog struct {
	ID        uint      `gorm:"primaryKey;index:idx_tasklog_task_cursor,priority:2,sort:desc" json:"id"`
	TaskID    uint      `gorm:"not null;index;index:idx_tasklog_task_cursor,priority:1" json:"task_id"`
	TaskRunID *uint     `gorm:"index" json:"task_run_id,omitempty"`
	Level     string    `gorm:"size:16;not null" json:"level"`
	Message   string    `gorm:"type:text;not null" json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

type TaskTrafficSample struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	TaskID         uint      `gorm:"not null;index:idx_task_traffic_task_run_sample,priority:1" json:"task_id"`
	NodeID         uint      `gorm:"not null;index:idx_task_traffic_node_sample,priority:1" json:"node_id"`
	RunStartedAt   time.Time `gorm:"not null;index:idx_task_traffic_task_run_sample,priority:2" json:"run_started_at"`
	SampledAt      time.Time `gorm:"not null;index:idx_task_traffic_task_run_sample,priority:3;index:idx_task_traffic_sampled_at;index:idx_task_traffic_node_sample,priority:2" json:"sampled_at"`
	ThroughputMbps float64   `gorm:"not null;default:0" json:"throughput_mbps"`
	CreatedAt      time.Time `json:"created_at"`
}
