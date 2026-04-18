package model

import (
	"strings"
	"time"

	"xirang/backend/internal/secure"

	"gorm.io/gorm"
)

type User struct {
	ID            uint      `gorm:"primaryKey" json:"id"`
	Username      string    `gorm:"size:64;uniqueIndex;not null" json:"username"`
	PasswordHash  string    `gorm:"size:255;not null" json:"-"`
	Role          string    `gorm:"size:32;not null;index" json:"role"`
	TOTPSecret    string    `gorm:"size:255" json:"-"`
	TOTPEnabled   bool      `json:"totp_enabled"`
	RecoveryCodes string    `gorm:"type:text" json:"-"`
	TokenVersion  uint      `gorm:"not null;default:0" json:"-"`
	Onboarded     bool      `gorm:"not null;default:false" json:"onboarded"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type SSHKey struct {
	ID          uint       `gorm:"primaryKey" json:"id"`
	Name        string     `gorm:"size:128;not null;uniqueIndex" json:"name"`
	Username    string     `gorm:"size:128;not null" json:"username"`
	KeyType     string     `gorm:"size:32;not null;default:auto" json:"key_type"`
	PrivateKey  string     `gorm:"type:text;not null" json:"private_key"`
	Fingerprint string     `gorm:"size:255;not null" json:"fingerprint"`
	LastUsedAt  *time.Time `json:"last_used_at"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// Sanitized 返回去除敏感字段（密码、私钥）的节点副本，用于 API 响应。
func (n Node) Sanitized() Node {
	safe := n
	safe.Password = ""
	safe.PrivateKey = ""
	if safe.SSHKey != nil {
		keyCopy := *safe.SSHKey
		keyCopy.PrivateKey = ""
		safe.SSHKey = &keyCopy
	}
	return safe
}

type Node struct {
	ID                  uint       `gorm:"primaryKey" json:"id"`
	Name                string     `gorm:"size:128;not null;uniqueIndex" json:"name"`
	Host                string     `gorm:"size:255;not null" json:"host"`
	Port                int        `gorm:"not null;default:22" json:"port"`
	Username            string     `gorm:"size:128;not null" json:"username"`
	AuthType            string     `gorm:"size:32;not null;default:key" json:"auth_type"`
	Password            string     `gorm:"size:255" json:"password,omitempty"`
	PrivateKey          string     `gorm:"type:text" json:"private_key,omitempty"`
	SSHKeyID            *uint      `gorm:"index" json:"ssh_key_id"`
	SSHKey              *SSHKey    `json:"ssh_key,omitempty"`
	Tags                string     `gorm:"size:512" json:"tags"`
	Status              string     `gorm:"size:32;not null;default:offline" json:"status"`
	BasePath            string     `gorm:"size:255" json:"base_path"`
	BackupDir           string     `gorm:"size:128;not null;uniqueIndex" json:"backup_dir"`
	UseSudo             bool       `gorm:"not null;default:false" json:"use_sudo"`
	ConnectionLatency   int        `gorm:"not null;default:0" json:"connection_latency_ms"`
	DiskUsedGB          int        `gorm:"not null;default:0" json:"disk_used_gb"`
	DiskTotalGB         int        `gorm:"not null;default:0" json:"disk_total_gb"`
	LastSeenAt          *time.Time `json:"last_seen_at"`
	LastBackupAt        *time.Time `json:"last_backup_at"`
	LastProbeAt         *time.Time `json:"last_probe_at"`
	ConsecutiveFailures int        `gorm:"not null;default:0" json:"consecutive_failures"`
	MaintenanceStart    *time.Time `json:"maintenance_start,omitempty"`
	MaintenanceEnd      *time.Time `json:"maintenance_end,omitempty"`
	ExpiryDate          *time.Time `gorm:"" json:"expiry_date,omitempty"`
	Archived            bool       `gorm:"not null;default:false" json:"archived"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

type Policy struct {
	ID               uint      `gorm:"primaryKey" json:"id"`
	Name             string    `gorm:"size:128;not null;uniqueIndex" json:"name"`
	Description      string    `gorm:"size:255" json:"description"`
	SourcePath       string    `gorm:"size:512;not null" json:"source_path"`
	TargetPath       string    `gorm:"size:512;not null" json:"target_path"`
	CronSpec         string    `gorm:"size:128;not null" json:"cron_spec"`
	ExcludeRules     string    `gorm:"type:text" json:"exclude_rules"`
	BwLimit          int       `gorm:"column:bwlimit;not null;default:0" json:"bwlimit"`
	RetentionDays    int       `gorm:"not null;default:7" json:"retention_days"`
	MaxConcurrent    int       `gorm:"not null;default:1" json:"max_concurrent"`
	Enabled          bool      `gorm:"not null;default:true" json:"enabled"`
	VerifyEnabled    bool      `gorm:"not null;default:true" json:"verify_enabled"`
	VerifySampleRate int       `gorm:"not null;default:0" json:"verify_sample_rate"`
	IsTemplate         bool      `gorm:"not null;default:false" json:"is_template"`
	PreHook            string    `gorm:"type:text;not null;default:''" json:"pre_hook"`
	PostHook           string    `gorm:"type:text;not null;default:''" json:"post_hook"`
	HookTimeoutSeconds int       `gorm:"not null;default:300" json:"hook_timeout_seconds"`
	MaxRetries         int       `gorm:"not null;default:2" json:"max_retries"`
	RetryBaseSeconds   int       `gorm:"not null;default:30" json:"retry_base_seconds"`
	BandwidthSchedule  string    `gorm:"type:text;not null;default:''" json:"bandwidth_schedule"`
	Nodes              []Node    `gorm:"many2many:policy_nodes" json:"-"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// PolicyNode 策略-节点关联表
type PolicyNode struct {
	PolicyID  uint      `gorm:"primaryKey"`
	NodeID    uint      `gorm:"primaryKey"`
	CreatedAt time.Time
}

type Integration struct {
	ID              uint      `gorm:"primaryKey" json:"id"`
	Type            string    `gorm:"size:32;not null" json:"type"`
	Name            string    `gorm:"size:128;not null;uniqueIndex" json:"name"`
	Endpoint        string    `gorm:"size:1024;not null" json:"endpoint"`
	Secret          string    `gorm:"size:512" json:"-"`
	HasSecret       bool      `gorm:"-" json:"has_secret"`
	Enabled         bool      `gorm:"not null;default:true" json:"enabled"`
	FailThreshold   int       `gorm:"not null;default:1" json:"fail_threshold"`
	CooldownMinutes int       `gorm:"not null;default:5" json:"cooldown_minutes"`
	ProxyURL        string    `gorm:"size:512;not null;default:''" json:"proxy_url"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

func (i *Integration) BeforeSave(_ *gorm.DB) error {
	if i.Endpoint != "" {
		encrypted, err := secure.EncryptIfNeeded(i.Endpoint)
		if err != nil {
			return err
		}
		i.Endpoint = encrypted
	}
	if i.Secret != "" {
		encrypted, err := secure.EncryptIfNeeded(i.Secret)
		if err != nil {
			return err
		}
		i.Secret = encrypted
	}
	if i.ProxyURL != "" {
		encrypted, err := secure.EncryptIfNeeded(i.ProxyURL)
		if err != nil {
			return err
		}
		i.ProxyURL = encrypted
	}
	return nil
}

func (i *Integration) AfterFind(_ *gorm.DB) error {
	if i.Endpoint != "" {
		decrypted, err := secure.DecryptIfNeeded(i.Endpoint)
		if err != nil {
			return err
		}
		i.Endpoint = decrypted
	}
	if i.Secret != "" {
		decrypted, err := secure.DecryptIfNeeded(i.Secret)
		if err != nil {
			return err
		}
		i.Secret = decrypted
	}
	if i.ProxyURL != "" {
		decrypted, err := secure.DecryptIfNeeded(i.ProxyURL)
		if err != nil {
			return err
		}
		i.ProxyURL = decrypted
	}
	i.HasSecret = i.Secret != ""
	return nil
}

type Alert struct {
	ID             uint       `gorm:"primaryKey" json:"id"`
	NodeID         uint       `gorm:"not null;index:idx_alerts_dedup" json:"node_id"`
	NodeName       string     `gorm:"size:128;not null" json:"node_name"`
	TaskID         *uint      `gorm:"index" json:"task_id"`
	TaskRunID      *uint      `gorm:"index" json:"task_run_id,omitempty"`
	PolicyName     string     `gorm:"size:128" json:"policy_name"`
	Severity       string     `gorm:"size:16;not null;index" json:"severity"`
	Status         string     `gorm:"size:16;not null;index" json:"status"`
	ErrorCode      string     `gorm:"size:64;not null;index:idx_alerts_dedup" json:"error_code"`
	Message        string     `gorm:"type:text;not null" json:"message"`
	Retryable      bool       `gorm:"not null;default:false" json:"retryable"`
	TriggeredAt    time.Time  `gorm:"index" json:"triggered_at"`
	LastNotifiedAt *time.Time `json:"last_notified_at"`
	CreatedAt      time.Time  `gorm:"index:idx_alerts_dedup" json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type AlertDelivery struct {
	ID            uint      `gorm:"primaryKey" json:"id"`
	AlertID       uint      `gorm:"index;not null" json:"alert_id"`
	IntegrationID uint      `gorm:"index;not null" json:"integration_id"`
	Status        string    `gorm:"size:16;not null" json:"status"`
	Error         string    `gorm:"type:text" json:"error"`
	CreatedAt     time.Time `json:"created_at"`
}

type Task struct {
	ID               uint       `gorm:"primaryKey" json:"id"`
	Name             string     `gorm:"size:128;not null" json:"name"`
	NodeID           uint       `gorm:"not null;index" json:"node_id"`
	Node             Node       `json:"node,omitempty"`
	PolicyID         *uint      `gorm:"index" json:"policy_id,omitempty"`
	Policy           *Policy    `json:"policy,omitempty"`
	DependsOnTaskID  *uint      `gorm:"index" json:"depends_on_task_id,omitempty"`
	Command          string     `gorm:"type:text" json:"command"`
	RsyncSource      string     `gorm:"size:512" json:"rsync_source"`
	RsyncTarget      string     `gorm:"size:512" json:"rsync_target"`
	ExecutorType     string     `gorm:"size:32;not null;default:local" json:"executor_type"`
	ExecutorConfig   string     `gorm:"type:text" json:"executor_config,omitempty"`
	CronSpec         string     `gorm:"size:128" json:"cron_spec"`
	Status           string     `gorm:"size:32;not null;index" json:"status"`
	BatchID          string     `gorm:"size:64;index" json:"batch_id,omitempty"`
	Source           string     `gorm:"size:32;not null;default:manual" json:"source"`
	VerifyStatus     string     `gorm:"size:16;not null;default:none" json:"verify_status"`
	RetryCount       int        `gorm:"not null;default:0" json:"retry_count"`
	Enabled          bool       `gorm:"not null;default:true" json:"enabled"`
	SkipNext         bool       `gorm:"not null;default:false" json:"skip_next"`
	LastError        string     `gorm:"type:text" json:"last_error"`
	LastRunAt        *time.Time `json:"last_run_at"`
	NextRunAt        *time.Time `json:"next_run_at"`
	Progress         *int       `gorm:"-" json:"progress,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
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
	ID                 uint       `gorm:"primaryKey" json:"id"`
	TaskID             uint       `gorm:"not null;index" json:"task_id"`
	Task               Task       `gorm:"foreignKey:TaskID" json:"-"`
	TriggerType        string     `gorm:"size:32;not null;default:manual" json:"trigger_type"`
	Status             string     `gorm:"size:32;not null;default:pending;index" json:"status"`
	ChainRunID         string     `gorm:"size:64;index" json:"chain_run_id,omitempty"`
	UpstreamTaskRunID  *uint      `gorm:"index" json:"upstream_task_run_id,omitempty"`
	SkipReason         string     `gorm:"type:text" json:"skip_reason,omitempty"`
	StartedAt          *time.Time `json:"started_at"`
	FinishedAt         *time.Time `json:"finished_at"`
	DurationMs         int64      `gorm:"not null;default:0" json:"duration_ms"`
	VerifyStatus       string     `gorm:"size:16;not null;default:none" json:"verify_status"`
	ThroughputMbps     float64    `gorm:"not null;default:0" json:"throughput_mbps"`
	Progress           int        `gorm:"not null;default:0" json:"progress"`
	LastError          string     `gorm:"type:text" json:"last_error"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

type AuditLog struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	UserID     uint      `gorm:"index" json:"user_id"`
	Username   string    `gorm:"size:64;index" json:"username"`
	Role       string    `gorm:"size:32;index" json:"role"`
	Method     string    `gorm:"size:16;index" json:"method"`
	Path       string    `gorm:"size:255;index" json:"path"`
	StatusCode int       `gorm:"index" json:"status_code"`
	ClientIP   string    `gorm:"size:64" json:"client_ip"`
	UserAgent  string    `gorm:"size:255" json:"user_agent"`
	PrevHash   string    `gorm:"size:64;index" json:"prev_hash,omitempty"`
	EntryHash  string    `gorm:"size:64;index" json:"entry_hash,omitempty"`
	CreatedAt  time.Time `gorm:"index" json:"created_at"`
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

// NodeMetricSample 节点资源采样记录
type NodeMetricSample struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	NodeID      uint      `gorm:"not null;index:idx_node_metric_node_sampled,priority:1" json:"node_id"`
	CpuPct      float64   `gorm:"not null;default:0" json:"cpu_pct"`
	MemPct      float64   `gorm:"not null;default:0" json:"mem_pct"`
	DiskPct     float64   `gorm:"not null;default:0" json:"disk_pct"`
	Load1m      float64   `gorm:"column:load_1m;not null;default:0" json:"load_1m"`
	LatencyMs   *int64    `gorm:"column:latency_ms" json:"latency_ms,omitempty"`
	DiskGBUsed  *float64  `gorm:"column:disk_gb_used" json:"disk_gb_used,omitempty"`
	DiskGBTotal *float64  `gorm:"column:disk_gb_total" json:"disk_gb_total,omitempty"`
	ProbeOK     bool      `gorm:"not null;default:true" json:"probe_ok"`
	SampledAt   time.Time `gorm:"not null;index:idx_node_metric_node_sampled,priority:2;index:idx_node_metric_sampled_at" json:"sampled_at"`
	CreatedAt   time.Time `json:"created_at"`
}

// NodeOwner 节点 ownership 关联表（operator 只能访问自己负责的节点）
type NodeOwner struct {
	NodeID    uint      `gorm:"primaryKey" json:"node_id"`
	UserID    uint      `gorm:"primaryKey" json:"user_id"`
	User      User      `gorm:"foreignKey:UserID" json:"user"`
	CreatedAt time.Time `json:"created_at"`
}

// ReportConfig SLA 报告配置
type ReportConfig struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	Name           string    `gorm:"size:128;not null;uniqueIndex" json:"name"`
	ScopeType      string    `gorm:"size:32;not null;default:all" json:"scope_type"` // all | tag | node_ids
	ScopeValue     string    `gorm:"type:text;not null;default:''" json:"scope_value"`
	Period         string    `gorm:"size:32;not null;default:weekly" json:"period"` // weekly | monthly
	Cron           string    `gorm:"size:128;not null" json:"cron"`
	IntegrationIDs string    `gorm:"type:text;not null;default:'[]'" json:"integration_ids"` // JSON array
	Enabled        bool      `gorm:"not null;default:true" json:"enabled"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// Report 已生成的 SLA 报告
type Report struct {
	ID            uint      `gorm:"primaryKey" json:"id"`
	ConfigID      uint      `gorm:"not null;index" json:"config_id"`
	Config        *ReportConfig `gorm:"foreignKey:ConfigID" json:"config"`
	PeriodStart   time.Time `gorm:"not null;index" json:"period_start"`
	PeriodEnd     time.Time `gorm:"not null" json:"period_end"`
	TotalRuns     int       `gorm:"not null;default:0" json:"total_runs"`
	SuccessRuns   int       `gorm:"not null;default:0" json:"success_runs"`
	FailedRuns    int       `gorm:"not null;default:0" json:"failed_runs"`
	SuccessRate   float64   `gorm:"not null;default:0" json:"success_rate"`
	AvgDurationMs int64     `gorm:"not null;default:0" json:"avg_duration_ms"`
	TopFailures   string    `gorm:"type:text;not null;default:'[]'" json:"top_failures"` // JSON
	DiskTrend     string    `gorm:"type:text;not null;default:'[]'" json:"disk_trend"`   // JSON
	GeneratedAt   time.Time `gorm:"not null" json:"generated_at"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func (s *SSHKey) BeforeSave(_ *gorm.DB) error {
	if s.PrivateKey == "" {
		return nil
	}
	encrypted, err := secure.EncryptIfNeeded(s.PrivateKey)
	if err != nil {
		return err
	}
	s.PrivateKey = encrypted
	return nil
}

func (s *SSHKey) AfterFind(_ *gorm.DB) error {
	if s.PrivateKey == "" {
		return nil
	}
	decrypted, err := secure.DecryptIfNeeded(s.PrivateKey)
	if err != nil {
		return err
	}
	s.PrivateKey = decrypted
	return nil
}

func (n *Node) BeforeSave(_ *gorm.DB) error {
	if n.Password != "" {
		encrypted, err := secure.EncryptIfNeeded(n.Password)
		if err != nil {
			return err
		}
		n.Password = encrypted
	}
	if n.PrivateKey != "" {
		encrypted, err := secure.EncryptIfNeeded(n.PrivateKey)
		if err != nil {
			return err
		}
		n.PrivateKey = encrypted
	}
	return nil
}

func (n *Node) AfterFind(_ *gorm.DB) error {
	if n.Password != "" {
		decrypted, err := secure.DecryptIfNeeded(n.Password)
		if err != nil {
			return err
		}
		n.Password = decrypted
	}
	if n.PrivateKey != "" {
		decrypted, err := secure.DecryptIfNeeded(n.PrivateKey)
		if err != nil {
			return err
		}
		n.PrivateKey = decrypted
	}
	return nil
}

// User TOTP 敏感字段加解密 hooks

func (u *User) BeforeSave(_ *gorm.DB) error {
	if u.TOTPSecret != "" {
		encrypted, err := secure.EncryptIfNeeded(u.TOTPSecret)
		if err != nil {
			return err
		}
		u.TOTPSecret = encrypted
	}
	if u.RecoveryCodes != "" {
		encrypted, err := secure.EncryptIfNeeded(u.RecoveryCodes)
		if err != nil {
			return err
		}
		u.RecoveryCodes = encrypted
	}
	return nil
}

func (u *User) AfterFind(_ *gorm.DB) error {
	if u.TOTPSecret != "" {
		decrypted, err := secure.DecryptIfNeeded(u.TOTPSecret)
		if err != nil {
			return err
		}
		u.TOTPSecret = decrypted
	}
	if u.RecoveryCodes != "" {
		decrypted, err := secure.DecryptIfNeeded(u.RecoveryCodes)
		if err != nil {
			return err
		}
		u.RecoveryCodes = decrypted
	}
	return nil
}

// LoginFailure 登录失败记录，持久化存储以防重启绕过锁定。
type LoginFailure struct {
	ID          uint       `gorm:"primaryKey" json:"id"`
	Username    string     `gorm:"size:64;not null;uniqueIndex:idx_login_failures_user_ip" json:"username"`
	ClientIP    string     `gorm:"size:45;not null;uniqueIndex:idx_login_failures_user_ip" json:"client_ip"`
	FailCount   int        `gorm:"not null;default:0" json:"fail_count"`
	LockedUntil *time.Time `json:"locked_until"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// SystemSetting 系统设置（key-value 存储，DB 覆盖值 → 环境变量 → 代码默认值）
type SystemSetting struct {
	Key       string    `gorm:"primaryKey;size:128" json:"key"`
	Value     string    `gorm:"type:text;not null" json:"value"`
	UpdatedAt time.Time `json:"updated_at"`
}
