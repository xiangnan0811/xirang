package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

// BatchCommand retains the requester-scoped idempotency identity even after deletion.
// It intentionally stores a request hash rather than a second copy of command text.
type BatchCommand struct {
	ID                string     `gorm:"size:64;primaryKey" json:"batch_id"`
	RequesterID       uint       `gorm:"not null;uniqueIndex:idx_batch_commands_request_key" json:"-"`
	IdempotencyKey    string     `gorm:"size:256;not null;uniqueIndex:idx_batch_commands_request_key" json:"-"`
	RequestHash       string     `gorm:"size:64;not null" json:"-"`
	ProvenanceVersion int        `gorm:"column:provenance_version;not null;default:0" json:"-"`
	SourceBatchID     *string    `gorm:"column:source_batch_id;size:64" json:"-"`
	Retain            bool       `gorm:"not null;default:false" json:"retain"`
	DeletedAt         *time.Time `json:"-"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

// BatchCommandDispatch describes initial dispatch, not the result of task execution.
// Keep these receipts after task deletion so a replay cannot create new commands.
type BatchCommandDispatch struct {
	TaskID             uint      `gorm:"primaryKey;autoIncrement:false" json:"task_id"`
	BatchID            string    `gorm:"size:64;not null;index" json:"batch_id"`
	NodeID             uint      `gorm:"not null" json:"node_id"`
	Status             string    `gorm:"size:32;not null" json:"status"`
	RunID              uint      `gorm:"not null;default:0" json:"run_id"`
	LastError          string    `gorm:"type:text;not null;default:''" json:"last_error,omitempty"`
	CommandFingerprint string    `gorm:"column:command_fingerprint;size:64;not null;default:''" json:"-"`
	RetryOfTaskID      *uint     `gorm:"column:retry_of_task_id;uniqueIndex:idx_batch_command_dispatches_retry_source,priority:1" json:"-"`
	RetryOfRunID       *uint     `gorm:"column:retry_of_run_id;uniqueIndex:idx_batch_command_dispatches_retry_source,priority:2" json:"-"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// BatchCommandRunEvidence is the permanent, non-secret proof of a v1 command
// run's outcome. It intentionally has no JSON surface: handlers expose only
// purpose-built eligibility and provenance DTOs.
type BatchCommandRunEvidence struct {
	RunID              uint       `gorm:"column:run_id;primaryKey;autoIncrement:false;index:idx_batch_command_run_evidences_task_run,priority:2" json:"-"`
	TaskID             uint       `gorm:"column:task_id;not null;index:idx_batch_command_run_evidences_task_run,priority:1" json:"-"`
	NodeID             uint       `gorm:"column:node_id;not null" json:"-"`
	CommandFingerprint string     `gorm:"column:command_fingerprint;size:64;not null;default:''" json:"-"`
	Outcome            string     `gorm:"size:16;not null;default:pending" json:"-"`
	ExitCode           *int       `gorm:"column:exit_code" json:"-"`
	FinishedAt         *time.Time `gorm:"column:finished_at" json:"-"`
	CreatedAt          time.Time  `gorm:"column:created_at;not null" json:"-"`
}

func (BatchCommandRunEvidence) TableName() string {
	return "batch_command_run_evidences"
}

// BatchCommandTaskFingerprint returns the canonical v1 identity for a batch
// command task. Empty output means the task cannot be used as a retry source.
// Only the persisted command bytes participate; mutable labels, status, times,
// and node credential details are deliberately excluded.
func BatchCommandTaskFingerprint(task Task) string {
	if task.ID == 0 ||
		task.NodeID == 0 ||
		task.BatchID == "" ||
		task.Source != "batch" ||
		task.ExecutorType != "command" ||
		task.Command == "" ||
		task.PolicyID != nil ||
		task.DependsOnTaskID != nil ||
		task.CronSpec != "" ||
		task.RsyncSource != "" ||
		task.RsyncTarget != "" ||
		task.ExecutorConfig != "" {
		return ""
	}

	type canonicalBatchCommandTask struct {
		Version int    `json:"version"`
		TaskID  uint   `json:"task_id"`
		BatchID string `json:"batch_id"`
		NodeID  uint   `json:"node_id"`
		Command string `json:"command"`
	}
	payload := canonicalBatchCommandTask{
		Version: 1,
		TaskID:  task.ID,
		BatchID: task.BatchID,
		NodeID:  task.NodeID,
		Command: task.Command,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}
