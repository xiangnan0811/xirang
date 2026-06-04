package model

import (
	"time"
)

// RestoreDrillEvidence stores structured restore-drill proof for later trust/confidence consumers.
type RestoreDrillEvidence struct {
	ID                   uint       `gorm:"primaryKey" json:"id"`
	PolicyID             uint       `gorm:"not null;index" json:"policy_id"`
	TaskID               uint       `gorm:"not null;index" json:"task_id"`
	TaskRunID            uint       `gorm:"not null;uniqueIndex" json:"task_run_id"`
	SourceTaskRunID      *uint      `gorm:"index" json:"source_task_run_id,omitempty"`
	SnapshotRef          string     `gorm:"size:128;not null;default:''" json:"snapshot_ref"`
	SandboxNodeID        uint       `gorm:"not null;index" json:"sandbox_node_id"`
	SandboxNodeName      string     `gorm:"size:128;not null;default:''" json:"sandbox_node_name"`
	SandboxPath          string     `gorm:"size:512;not null" json:"sandbox_path"`
	Status               string     `gorm:"size:32;not null;default:pending;index" json:"status"`
	FailedStep           string     `gorm:"size:64;not null;default:''" json:"failed_step"`
	ConfidenceEligible   bool       `gorm:"not null;default:false" json:"confidence_eligible"`
	StartedAt            *time.Time `json:"started_at"`
	FinishedAt           *time.Time `json:"finished_at"`
	DurationMs           int64      `gorm:"not null;default:0" json:"duration_ms"`
	RestoreStatus        string     `gorm:"size:32;not null;default:pending" json:"restore_status"`
	RestoreStartedAt     *time.Time `json:"restore_started_at"`
	RestoreFinishedAt    *time.Time `json:"restore_finished_at"`
	RestoreError         string     `gorm:"type:text" json:"restore_error"`
	VerifyStatus         string     `gorm:"size:32;not null;default:pending" json:"verify_status"`
	VerifyStartedAt      *time.Time `json:"verify_started_at"`
	VerifyFinishedAt     *time.Time `json:"verify_finished_at"`
	VerifyError          string     `gorm:"type:text" json:"verify_error"`
	PostVerifyStatus     string     `gorm:"size:32;not null;default:skipped" json:"post_verify_status"`
	PostVerifyFinishedAt *time.Time `json:"post_verify_finished_at"`
	PostVerifyError      string     `gorm:"type:text" json:"post_verify_error"`
	CleanupStatus        string     `gorm:"size:32;not null;default:pending" json:"cleanup_status"`
	CleanupStartedAt     *time.Time `json:"cleanup_started_at"`
	CleanupFinishedAt    *time.Time `json:"cleanup_finished_at"`
	CleanupError         string     `gorm:"type:text" json:"cleanup_error"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
}

// SnapshotDiffHistory 记录每次备份的快照差异统计，作为异常检测基线数据。
type SnapshotDiffHistory struct {
	ID               uint      `gorm:"primaryKey" json:"id"`
	PolicyID         uint      `gorm:"not null;index:idx_sdh_policy" json:"policy_id"`
	TaskID           uint      `gorm:"not null;index" json:"task_id"`
	TaskRunID        uint      `gorm:"not null;index" json:"task_run_id"`
	AddedCount       int       `gorm:"not null;default:0" json:"added_count"`
	RemovedCount     int       `gorm:"not null;default:0" json:"removed_count"`
	ChangedCount     int       `gorm:"not null;default:0" json:"changed_count"`
	TotalSizeBytes   int64     `gorm:"not null;default:0" json:"total_size_bytes"`
	RansomSuffixHits int       `gorm:"not null;default:0" json:"ransom_suffix_hits"`
	CreatedAt        time.Time `json:"created_at"`
}

// SnapshotFileIndex 快照文件索引表，支持跨快照按文件名/路径搜索。
// 联合唯一索引 (task_id, snapshot_id, path) 防止重复索引。
type SnapshotFileIndex struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	TaskID     uint      `gorm:"not null;uniqueIndex:idx_sfi_task_snap_path" json:"task_id"`
	SnapshotID string    `gorm:"size:64;not null;uniqueIndex:idx_sfi_task_snap_path" json:"snapshot_id"`
	Path       string    `gorm:"type:text;not null;uniqueIndex:idx_sfi_task_snap_path;index:idx_sfi_path" json:"path"`
	Size       int64     `gorm:"not null;default:0" json:"size"`
	Mtime      string    `gorm:"size:64;not null;default:''" json:"mtime"`
	CreatedAt  time.Time `json:"created_at"`
}

// AutomationRule 自动化规则：事件触发条件 → 执行动作
type AutomationRule struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	Name         string    `gorm:"size:128;not null;uniqueIndex" json:"name"`
	Description  string    `gorm:"size:255" json:"description"`
	EventType    string    `gorm:"size:64;not null;index" json:"event_type"`
	EventFilter  string    `gorm:"type:text;not null;default:'{}'" json:"event_filter"` // JSON
	ActionType   string    `gorm:"size:64;not null" json:"action_type"`
	ActionConfig string    `gorm:"type:text;not null;default:'{}'" json:"action_config"` // JSON
	Enabled      bool      `gorm:"not null" json:"enabled"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// AutomationRuleLog 自动化规则执行日志
type AutomationRuleLog struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	RuleID     uint      `gorm:"not null;index" json:"rule_id"`
	EventType  string    `gorm:"size:64;not null" json:"event_type"`
	ActionType string    `gorm:"size:64;not null" json:"action_type"`
	Result     string    `gorm:"size:16;not null" json:"result"` // "success" | "error"
	Error      string    `gorm:"type:text" json:"error,omitempty"`
	Details    string    `gorm:"type:text" json:"details,omitempty"` // JSON
	CreatedAt  time.Time `json:"created_at"`
}
