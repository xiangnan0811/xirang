package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

	TaskRunCaptureLayoutDirectoryRoot     = "directory_root"
	TaskRunCaptureLayoutDirectoryContents = "directory_contents"
	TaskRunCaptureLayoutSingleFile        = "single_file"

	TaskRunGenerationStateDirty    = "dirty"
	TaskRunGenerationStateVerified = "verified"
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

	// Rsync capture fields are populated only on an in-memory restore task.
	// They are deliberately not part of the Task table or API surface; durable
	// capture evidence belongs to the producing TaskRun.
	RsyncCaptureLayout       string `gorm:"-" json:"-"`
	RsyncCaptureRoot         string `gorm:"-" json:"-"`
	RsyncCaptureManifest     string `gorm:"-" json:"-"`
	RsyncCaptureGenerationID uint   `gorm:"-" json:"-"`
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
	ExecutionOwnerID        string     `gorm:"size:64;not null;default:''" json:"-"`
	ExecutionLeaseUntil     *time.Time `gorm:"index" json:"-"`
	ID                      uint       `gorm:"primaryKey" json:"id"`
	TaskID                  uint       `gorm:"not null;index;uniqueIndex:idx_task_runs_active_drill,where:trigger_type = 'drill' AND (status = 'pending' OR status = 'running' OR status = 'retrying')" json:"task_id"`
	Task                    Task       `gorm:"foreignKey:TaskID" json:"-"`
	NodeIDSnapshot          uint       `gorm:"not null;index:idx_task_runs_node_snapshot_status,priority:1" json:"-"`
	CronScheduledAt         *time.Time `gorm:"column:cron_scheduled_at" json:"-"`
	BackupConfigFingerprint string     `gorm:"column:backup_config_fingerprint;size:64" json:"-"`
	BackupCaptureLayout     string     `gorm:"column:backup_capture_layout;size:32" json:"-"`
	BackupCaptureRoot       string     `gorm:"column:backup_capture_root;size:512" json:"-"`
	BackupCaptureManifest   string     `gorm:"column:backup_capture_manifest;type:text" json:"-"`
	BackupGenerationState   string     `gorm:"column:backup_generation_state;size:16" json:"-"`
	BackupSourceRunID       uint       `gorm:"column:backup_source_run_id" json:"-"`
	TriggerType             string     `gorm:"size:32;not null;default:manual" json:"trigger_type"`
	Status                  string     `gorm:"size:32;not null;default:pending;index;index:idx_task_runs_status_finished_at,priority:1;index:idx_task_runs_node_snapshot_status,priority:2" json:"status"`
	ChainRunID              string     `gorm:"size:64;index" json:"chain_run_id,omitempty"`
	UpstreamTaskRunID       *uint      `gorm:"index" json:"upstream_task_run_id,omitempty"`
	SkipReason              string     `gorm:"type:text" json:"skip_reason,omitempty"`
	StartedAt               *time.Time `gorm:"index:idx_task_runs_started_at" json:"started_at"`
	FinishedAt              *time.Time `gorm:"index:idx_task_runs_status_finished_at,priority:2" json:"finished_at"`
	DurationMs              int64      `gorm:"not null;default:0" json:"duration_ms"`
	VerifyStatus            string     `gorm:"size:16;not null;default:none" json:"verify_status"`
	ThroughputMbps          float64    `gorm:"not null;default:0" json:"throughput_mbps"`
	Progress                int        `gorm:"not null;default:0" json:"progress"`
	LastError               string     `gorm:"type:text" json:"last_error"`
	CreatedAt               time.Time  `json:"created_at"`
	UpdatedAt               time.Time  `json:"updated_at"`
}

const (
	RsyncCaptureManifestMaxEntries = 100000
	RsyncCaptureManifestMaxBytes   = 8 << 20
)

// RsyncCaptureManifest is the bounded, source-side evidence captured before a
// compatibility Rsync write. Paths are relative to the original source root.
// A directory entry proves an empty directory was selected; a symlink entry
// records its link target instead of pretending it is a regular file.
type RsyncCaptureManifest struct {
	Version int                         `json:"version"`
	Layout  string                      `json:"layout"`
	Root    string                      `json:"root,omitempty"`
	Entries []RsyncCaptureManifestEntry `json:"entries"`
}

type RsyncCaptureManifestEntry struct {
	Path       string `json:"path"`
	Kind       string `json:"kind"`
	Size       int64  `json:"size,omitempty"`
	SHA256     string `json:"sha256,omitempty"`
	LinkTarget string `json:"link_target,omitempty"`
}

func EncodeRsyncCaptureManifest(manifest RsyncCaptureManifest) (string, error) {
	if manifest.Version == 0 {
		manifest.Version = 1
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}
func DecodeRsyncCaptureManifest(raw string) (RsyncCaptureManifest, error) {
	var manifest RsyncCaptureManifest
	if strings.TrimSpace(raw) == "" {
		return manifest, fmt.Errorf("rsync capture manifest is empty")
	}
	if len(raw) > RsyncCaptureManifestMaxBytes {
		return RsyncCaptureManifest{}, fmt.Errorf("rsync capture manifest is too large")
	}
	if err := json.Unmarshal([]byte(raw), &manifest); err != nil {
		return RsyncCaptureManifest{}, err
	}
	if len(manifest.Entries) > RsyncCaptureManifestMaxEntries {
		return RsyncCaptureManifest{}, fmt.Errorf("rsync capture manifest has too many entries")
	}
	if manifest.Version != 1 {
		return RsyncCaptureManifest{}, fmt.Errorf("unsupported rsync capture manifest version")
	}
	if manifest.Layout != TaskRunCaptureLayoutDirectoryRoot &&
		manifest.Layout != TaskRunCaptureLayoutDirectoryContents &&
		manifest.Layout != TaskRunCaptureLayoutSingleFile {
		return RsyncCaptureManifest{}, fmt.Errorf("invalid rsync capture manifest layout")
	}
	if manifest.Entries == nil {
		return RsyncCaptureManifest{}, fmt.Errorf("rsync capture manifest entries are missing")
	}
	return manifest, nil
}

// TaskRunBackupConfigFingerprint returns a stable, non-secret identity of the
// task values that the executor is about to use. Executor configuration and
// policy hooks are represented only by their SHA-256 digests so credentials
// never enter TaskRun history. The node connection identity is included because
// a node ID alone does not prove that the same endpoint was used.
//
// A policy-bound task must carry the matching Policy snapshot. Returning an
// empty digest for a missing or mismatched snapshot makes reservation and
// restore validation fail closed; standalone tasks without PolicyID remain
// supported.
func TaskRunBackupConfigFingerprint(task Task) string {
	type binding struct {
		NodeID               uint   `json:"node_id"`
		NodeName             string `json:"node_name"`
		NodeHost             string `json:"node_host"`
		NodePort             int    `json:"node_port"`
		NodeUsername         string `json:"node_username"`
		NodeAuthType         string `json:"node_auth_type"`
		NodeSSHKeyID         uint   `json:"node_ssh_key_id"`
		NodeUseSudo          bool   `json:"node_use_sudo"`
		NodeBackupDir        string `json:"node_backup_dir"`
		ExecutorType         string `json:"executor_type"`
		RsyncSource          string `json:"rsync_source"`
		RsyncTarget          string `json:"rsync_target"`
		ExecutorConfigDigest string `json:"executor_config_digest"`

		PolicyBound               bool   `json:"policy_bound"`
		PolicyID                  uint   `json:"policy_id"`
		PolicyExcludeRules        string `json:"policy_exclude_rules"`
		PolicyBwLimit             int    `json:"policy_bw_limit"`
		PolicyBandwidthSchedule   string `json:"policy_bandwidth_schedule"`
		PolicyPreHookDigest       string `json:"policy_pre_hook_digest"`
		PolicyPostHookDigest      string `json:"policy_post_hook_digest"`
		PolicyHookTimeoutSeconds  int    `json:"policy_hook_timeout_seconds"`
		PolicyMaxExecutionSeconds int    `json:"policy_max_execution_seconds"`
		PolicyAppProfile          string `json:"policy_app_profile"`
		PolicyAppCredentialID     uint   `json:"policy_app_credential_id"`
	}

	executorConfigDigest, ok := taskRunFingerprintDigest(task.ExecutorConfig)
	if !ok {
		return ""
	}

	policyBound := task.PolicyID != nil
	policyID := uint(0)
	policyExcludeRules := ""
	policyBwLimit := 0
	policyBandwidthSchedule := ""
	policyPreHookDigest := ""
	policyPostHookDigest := ""
	policyHookTimeoutSeconds := 0
	policyMaxExecutionSeconds := 0
	policyAppProfile := ""
	policyAppCredentialID := uint(0)
	if policyBound {
		policyID = *task.PolicyID
		if policyID == 0 || task.Policy == nil {
			return ""
		}
		if task.Policy.ID != 0 && task.Policy.ID != policyID {
			return ""
		}
		policyPreHookDigest, ok = taskRunFingerprintDigest(task.Policy.PreHook)
		if !ok {
			return ""
		}
		policyPostHookDigest, ok = taskRunFingerprintDigest(task.Policy.PostHook)
		if !ok {
			return ""
		}
		policyExcludeRules = canonicalTaskRunExcludeRules(task.Policy.ExcludeRules)
		policyBwLimit = task.Policy.BwLimit
		policyBandwidthSchedule = strings.TrimSpace(task.Policy.BandwidthSchedule)
		policyHookTimeoutSeconds = task.Policy.HookTimeoutSeconds
		policyMaxExecutionSeconds = task.Policy.MaxExecutionSeconds
		policyAppProfile = task.Policy.AppProfile
		if task.Policy.AppCredentialID != nil {
			policyAppCredentialID = *task.Policy.AppCredentialID
		}
	}

	nodeSSHKeyID := uint(0)
	if task.Node.SSHKeyID != nil {
		nodeSSHKeyID = *task.Node.SSHKeyID
	}
	payload := binding{
		NodeID:                    task.NodeID,
		NodeName:                  strings.TrimSpace(task.Node.Name),
		NodeHost:                  strings.TrimSpace(task.Node.Host),
		NodePort:                  task.Node.Port,
		NodeUsername:              strings.TrimSpace(task.Node.Username),
		NodeAuthType:              strings.ToLower(strings.TrimSpace(task.Node.AuthType)),
		NodeSSHKeyID:              nodeSSHKeyID,
		NodeUseSudo:               task.Node.UseSudo,
		NodeBackupDir:             strings.TrimSpace(task.Node.BackupDir),
		ExecutorType:              strings.ToLower(strings.TrimSpace(task.ExecutorType)),
		RsyncSource:               strings.TrimSpace(task.RsyncSource),
		RsyncTarget:               strings.TrimSpace(task.RsyncTarget),
		ExecutorConfigDigest:      executorConfigDigest,
		PolicyBound:               policyBound,
		PolicyID:                  policyID,
		PolicyExcludeRules:        policyExcludeRules,
		PolicyBwLimit:             policyBwLimit,
		PolicyBandwidthSchedule:   policyBandwidthSchedule,
		PolicyPreHookDigest:       policyPreHookDigest,
		PolicyPostHookDigest:      policyPostHookDigest,
		PolicyHookTimeoutSeconds:  policyHookTimeoutSeconds,
		PolicyMaxExecutionSeconds: policyMaxExecutionSeconds,
		PolicyAppProfile:          policyAppProfile,
		PolicyAppCredentialID:     policyAppCredentialID,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		// The binding contains only scalar values and cannot currently fail to
		// marshal. Keep the function total if fields are extended later.
		return ""
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

// taskRunFingerprintDigest decrypts encrypted-at-rest values before hashing so
// random ciphertext bytes never become part of the durable configuration
// identity. It also lets direct callers pass either plaintext or a persisted
// encrypted value without changing the result.
func taskRunFingerprintDigest(raw string) (string, bool) {
	plaintext, err := secure.DecryptIfNeeded(raw)
	if err != nil {
		return "", false
	}
	digest := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(digest[:]), true
}

// canonicalTaskRunExcludeRules matches the executor's input normalization:
// CRLF is treated as LF, surrounding whitespace is ignored, and blank lines
// are skipped before rules are passed to rsync. Internal rule bytes remain
// untouched because they affect validation and the rsync arguments.
func canonicalTaskRunExcludeRules(raw string) string {
	normalized := strings.TrimSpace(strings.ReplaceAll(raw, "\r\n", "\n"))
	if normalized == "" {
		return ""
	}
	lines := strings.Split(normalized, "\n")
	rules := lines[:0]
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		rules = append(rules, line)
	}
	return strings.Join(rules, "\n")
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
