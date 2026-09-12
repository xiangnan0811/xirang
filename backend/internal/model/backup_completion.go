package model

import "time"

// Backup completion facts are the durable source of backup availability. A
// fact is append-only: its classification, provenance and observed time must
// never be rewritten after a terminal/provider boundary records it.
const (
	BackupCompletionKindLegacyTransferCompleted = "legacy_transfer_completed"
	BackupCompletionKindManagedCommitted        = "managed_committed"
	BackupCompletionKindLegacyUnverified        = "legacy_unverified"

	BackupCompletionEvidenceVerified   = "verified"
	BackupCompletionEvidenceUnverified = "unverified"
)

// BackupCompletion stores one immutable availability fact for a backup
// attempt. TaskRunID is nullable for conservative historical rows whose old
// Node.last_backup_at timestamp or managed point cannot be tied to a provable
// execution. Such rows are explicitly unverified and never participate in
// authoritative freshness/RPO calculations. For managed points, EvidenceRef
// is retained even when the point is unprovable so replay cannot poison the
// same page forever; unverified rows never reserve a TaskRunID.
type BackupCompletion struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	TaskID         *uint     `gorm:"index" json:"task_id,omitempty"`
	TaskRunID      *uint     `gorm:"uniqueIndex:idx_backup_completions_task_run" json:"task_run_id,omitempty"`
	NodeID         uint      `gorm:"not null;index:idx_backup_completions_node_completed,priority:1" json:"node_id"`
	ExecutorType   string    `gorm:"size:32;not null;default:''" json:"executor_type"`
	FactKind       string    `gorm:"column:fact_kind;size:32;not null" json:"fact_kind"`
	EvidenceStatus string    `gorm:"column:evidence_status;size:16;not null" json:"evidence_status"`
	CompletedAt    time.Time `gorm:"not null;index:idx_backup_completions_node_completed,priority:2" json:"completed_at"`
	EvidenceRef    string    `gorm:"column:evidence_ref;size:64;not null;default:'';uniqueIndex:idx_backup_completions_unverified_ref,where:evidence_status = 'unverified' AND evidence_ref <> ''" json:"evidence_ref,omitempty"`
	CreatedAt      time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt      time.Time `gorm:"not null" json:"updated_at"`
}

func (BackupCompletion) TableName() string { return "backup_completions" }
