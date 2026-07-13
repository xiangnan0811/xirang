package model

import "time"

type BackupAssetAuditCheckpoint struct {
	SegmentNo              int64      `gorm:"primaryKey;autoIncrement:false" json:"segment_no"`
	Status                 string     `gorm:"size:16;not null" json:"status"`
	PreviousCheckpointHash string     `gorm:"size:64;not null;default:''" json:"previous_checkpoint_hash"`
	FirstEntryHash         string     `gorm:"size:64;not null;default:''" json:"first_entry_hash"`
	LastEntryHash          string     `gorm:"size:64;not null;default:''" json:"last_entry_hash"`
	EntryCount             int64      `gorm:"not null;default:0" json:"entry_count"`
	OpenedAt               time.Time  `gorm:"not null" json:"opened_at"`
	ClosedAt               *time.Time `json:"closed_at,omitempty"`
	DetailsPurgedAt        *time.Time `json:"details_purged_at,omitempty"`
	CheckpointHash         string     `gorm:"size:64;not null;default:''" json:"checkpoint_hash"`
}

func (BackupAssetAuditCheckpoint) TableName() string { return "backup_asset_audit_checkpoints" }

type BackupAssetAuditEvent struct {
	ID                    uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	SegmentNo             int64     `gorm:"not null" json:"segment_no"`
	SegmentSequence       int64     `gorm:"not null" json:"segment_sequence"`
	ActorUserID           uint      `gorm:"not null;default:0" json:"actor_user_id"`
	ActorUsername         string    `gorm:"size:64;not null;default:''" json:"actor_username"`
	ActorRole             string    `gorm:"size:32;not null;default:''" json:"actor_role"`
	Action                string    `gorm:"size:64;not null" json:"action"`
	Outcome               string    `gorm:"size:16;not null" json:"outcome"`
	RepositoryID          string    `gorm:"size:32;not null;default:''" json:"repository_id,omitempty"`
	RecoveryPointID       string    `gorm:"size:32;not null;default:''" json:"recovery_point_id,omitempty"`
	EntryID               string    `gorm:"size:64;not null;default:''" json:"entry_id,omitempty"`
	TaskID                *uint     `json:"task_id,omitempty"`
	TaskRunID             *uint     `json:"task_run_id,omitempty"`
	RecoveryJobID         string    `gorm:"size:32;not null;default:''" json:"recovery_job_id,omitempty"`
	ExportJobID           string    `gorm:"size:32;not null;default:''" json:"export_job_id,omitempty"`
	ItemCount             int64     `gorm:"not null;default:0" json:"item_count"`
	ByteCount             int64     `gorm:"not null;default:0" json:"byte_count"`
	RangeCount            int64     `gorm:"not null;default:0" json:"range_count"`
	RangeBytes            int64     `gorm:"not null;default:0" json:"range_bytes"`
	FingerprintKeyVersion *int      `json:"fingerprint_key_version,omitempty"`
	PathFingerprint       string    `gorm:"size:64;not null;default:''" json:"path_fingerprint,omitempty"`
	QueryFingerprint      string    `gorm:"size:64;not null;default:''" json:"query_fingerprint,omitempty"`
	StepUpAction          string    `gorm:"size:64;not null;default:''" json:"step_up_action,omitempty"`
	StepUpProofID         string    `gorm:"size:64;not null;default:''" json:"step_up_proof_id,omitempty"`
	GrantID               string    `gorm:"size:32;not null;default:''" json:"grant_id,omitempty"`
	FailureCode           string    `gorm:"size:64;not null;default:''" json:"failure_code,omitempty"`
	FieldsJSON            string    `gorm:"type:text;not null;default:'{}'" json:"-"`
	PrevHash              string    `gorm:"size:64;not null" json:"-"`
	EntryHash             string    `gorm:"size:64;not null" json:"-"`
	CreatedAt             time.Time `gorm:"not null" json:"created_at"`
}

func (BackupAssetAuditEvent) TableName() string { return "backup_asset_audit_events" }
