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

// PolicyNode 策略-节点关联表
type PolicyNode struct {
	PolicyID  uint `gorm:"primaryKey"`
	NodeID    uint `gorm:"primaryKey"`
	CreatedAt time.Time
}
