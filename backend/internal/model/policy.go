package model

import (
	"time"

	"xirang/backend/internal/secure"

	"gorm.io/gorm"
)

type Policy struct {
	ID            uint   `gorm:"primaryKey" json:"id"`
	Name          string `gorm:"size:128;not null;uniqueIndex" json:"name"`
	Description   string `gorm:"size:255" json:"description"`
	SourcePath    string `gorm:"size:512;not null" json:"source_path"`
	TargetPath    string `gorm:"size:512;not null" json:"target_path"`
	CronSpec      string `gorm:"size:128;not null" json:"cron_spec"`
	ExcludeRules  string `gorm:"type:text" json:"exclude_rules"`
	BwLimit       int    `gorm:"column:bwlimit;not null;default:0" json:"bwlimit"`
	RetentionDays int    `gorm:"not null;default:7" json:"retention_days"`
	// RPO/RTO 目标（分钟，0=未设置）
	RPOMinutes int `gorm:"not null;default:0" json:"rpo_minutes"`
	RTOMinutes int `gorm:"not null;default:0" json:"rto_minutes"`
	// GFS 保留模式: "simple" | "gfs"
	RetentionMode      string `gorm:"size:16;not null;default:'simple'" json:"retention_mode"`
	KeepDaily          int    `gorm:"not null;default:0" json:"keep_daily"`
	KeepWeekly         int    `gorm:"not null;default:0" json:"keep_weekly"`
	KeepMonthly        int    `gorm:"not null;default:0" json:"keep_monthly"`
	KeepYearly         int    `gorm:"not null;default:0" json:"keep_yearly"`
	MaxConcurrent      int    `gorm:"not null;default:1" json:"max_concurrent"`
	Enabled            bool   `gorm:"not null;default:true" json:"enabled"`
	SkipNext           bool   `gorm:"not null;default:false" json:"skip_next"`
	VerifyEnabled      bool   `gorm:"not null;default:true" json:"verify_enabled"`
	VerifySampleRate   int    `gorm:"not null;default:0" json:"verify_sample_rate"`
	IsTemplate         bool   `gorm:"not null;default:false" json:"is_template"`
	PreHook            string `gorm:"type:text;not null;default:''" json:"pre_hook"`
	PostHook           string `gorm:"type:text;not null;default:''" json:"post_hook"`
	HookTimeoutSeconds int    `gorm:"not null;default:300" json:"hook_timeout_seconds"`
	AppProfile         string `gorm:"size:32;not null;default:''" json:"app_profile"`
	AppCredentialID    *uint  `gorm:"index" json:"app_credential_id"`
	// MaxExecutionSeconds 0 = 使用环境变量 TASK_MAX_EXECUTION_SECONDS（默认 86400=24h）。
	// >0 = 该策略的任务最长执行秒数；超时后 ctx 被 cancel，executor 收到 SIGTERM 退出。
	MaxExecutionSeconds int    `gorm:"not null;default:0" json:"max_execution_seconds"`
	MaxRetries          int    `gorm:"not null;default:2" json:"max_retries"`
	RetryBaseSeconds    int    `gorm:"not null;default:30" json:"retry_base_seconds"`
	BandwidthSchedule   string `gorm:"type:text;not null;default:''" json:"bandwidth_schedule"`
	EscalationPolicyID  *uint  `gorm:"index" json:"escalation_policy_id"`
	// Drill 恢复演练配置
	DrillEnabled      bool      `gorm:"not null;default:false" json:"drill_enabled"`
	DrillCron         string    `gorm:"size:128;not null;default:''" json:"drill_cron"`
	DrillTargetNodeID *uint     `gorm:"index" json:"drill_target_node_id"`
	DrillRestorePath  string    `gorm:"size:512;not null;default:'/tmp/xirang-drill'" json:"drill_restore_path"`
	DrillPreVerify    string    `gorm:"type:text;not null;default:''" json:"drill_pre_verify"`
	DrillVerify       string    `gorm:"type:text;not null;default:''" json:"drill_verify"`
	DrillPostVerify   string    `gorm:"type:text;not null;default:''" json:"drill_post_verify"`
	DrillAutoCleanup  bool      `gorm:"not null;default:true" json:"drill_auto_cleanup"`
	Nodes             []Node    `gorm:"many2many:policy_nodes" json:"-"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// PolicyCreateExplicitColumns returns all persisted scalar columns whose
// explicit zero/false values must survive GORM's struct default callback.
// Callers should use CreatePolicyWithExplicitValues inside their existing
// transaction so model encryption hooks remain active.
func PolicyCreateExplicitColumns() []string {
	return []string{
		"name", "description", "source_path", "target_path", "cron_spec",
		"exclude_rules", "bwlimit", "retention_days", "rpo_minutes",
		"rto_minutes", "retention_mode", "keep_daily", "keep_weekly",
		"keep_monthly", "keep_yearly", "max_concurrent", "enabled",
		"skip_next", "verify_enabled", "verify_sample_rate", "is_template",
		"pre_hook", "post_hook", "hook_timeout_seconds", "app_profile",
		"app_credential_id", "max_execution_seconds", "max_retries",
		"retry_base_seconds", "bandwidth_schedule", "escalation_policy_id",
		"drill_enabled", "drill_cron", "drill_target_node_id",
		"drill_restore_path", "drill_pre_verify", "drill_verify",
		"drill_post_verify", "drill_auto_cleanup",
	}
}

// CreatePolicyWithExplicitValues performs the one struct Create boundary used
// by policy creation, cloning, and config import. GORM's Create callback still
// runs (including Policy encryption hooks); the selected corrective update is
// then executed in the same caller-owned transaction to restore explicit
// scalar false/0 values that model defaults would otherwise replace.
func CreatePolicyWithExplicitValues(tx *gorm.DB, policy *Policy, explicitColumns ...string) error {
	if tx == nil {
		return gorm.ErrInvalidDB
	}
	if policy == nil {
		return gorm.ErrInvalidData
	}
	// GORM's default callback mutates zero/false struct fields in place during
	// Create. Keep the caller's explicit values before that callback, then
	// restore them for the corrective update while retaining generated identity
	// and timestamps.
	original := *policy
	if err := tx.Create(policy).Error; err != nil {
		return err
	}
	createdID, createdAt, updatedAt := policy.ID, policy.CreatedAt, policy.UpdatedAt
	*policy = original
	policy.ID, policy.CreatedAt, policy.UpdatedAt = createdID, createdAt, updatedAt
	defer func() {
		*policy = original
		policy.ID, policy.CreatedAt, policy.UpdatedAt = createdID, createdAt, updatedAt
	}()
	if len(explicitColumns) == 0 {
		return nil
	}
	return tx.Model(policy).Select(explicitColumns).Updates(policy).Error
}

// PolicyNode 策略-节点关联表
type PolicyNode struct {
	PolicyID  uint `gorm:"primaryKey"`
	NodeID    uint `gorm:"primaryKey"`
	CreatedAt time.Time
}

func (p *Policy) BeforeSave(_ *gorm.DB) error {
	if err := encryptPolicyText(&p.PreHook); err != nil {
		return err
	}
	if err := encryptPolicyText(&p.PostHook); err != nil {
		return err
	}
	// Drill verify scripts may embed credentials/paths — same protection as hooks.
	if err := encryptPolicyText(&p.DrillPreVerify); err != nil {
		return err
	}
	if err := encryptPolicyText(&p.DrillVerify); err != nil {
		return err
	}
	if err := encryptPolicyText(&p.DrillPostVerify); err != nil {
		return err
	}
	return nil
}

func (p *Policy) AfterFind(_ *gorm.DB) error {
	if err := decryptPolicyText(&p.PreHook); err != nil {
		return err
	}
	if err := decryptPolicyText(&p.PostHook); err != nil {
		return err
	}
	if err := decryptPolicyText(&p.DrillPreVerify); err != nil {
		return err
	}
	if err := decryptPolicyText(&p.DrillVerify); err != nil {
		return err
	}
	if err := decryptPolicyText(&p.DrillPostVerify); err != nil {
		return err
	}
	return nil
}

func encryptPolicyText(field *string) error {
	if field == nil || *field == "" {
		return nil
	}
	if secure.IsEncrypted(*field) {
		return nil
	}
	// Use EncryptString so whitespace-only scripts are sealed too.
	// EncryptIfNeeded treats TrimSpace-empty values as skip (would leave plain).
	encrypted, err := secure.EncryptString(*field)
	if err != nil {
		return err
	}
	*field = encrypted
	return nil
}

func decryptPolicyText(field *string) error {
	if field == nil || *field == "" {
		return nil
	}
	decrypted, err := secure.DecryptIfNeeded(*field)
	if err != nil {
		return err
	}
	*field = decrypted
	return nil
}
