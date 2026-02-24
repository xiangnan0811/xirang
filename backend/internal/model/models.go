package model

import (
	"time"

	"xirang/backend/internal/secure"

	"gorm.io/gorm"
)

type User struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	Username     string    `gorm:"size:64;uniqueIndex;not null" json:"username"`
	PasswordHash string    `gorm:"size:255;not null" json:"-"`
	Role         string    `gorm:"size:32;not null;index" json:"role"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
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

type Node struct {
	ID                uint       `gorm:"primaryKey" json:"id"`
	Name              string     `gorm:"size:128;not null;uniqueIndex" json:"name"`
	Host              string     `gorm:"size:255;not null" json:"host"`
	Port              int        `gorm:"not null;default:22" json:"port"`
	Username          string     `gorm:"size:128;not null" json:"username"`
	AuthType          string     `gorm:"size:32;not null;default:key" json:"auth_type"`
	Password          string     `gorm:"size:255" json:"password,omitempty"`
	PrivateKey        string     `gorm:"type:text" json:"private_key,omitempty"`
	SSHKeyID          *uint      `gorm:"index" json:"ssh_key_id"`
	SSHKey            *SSHKey    `json:"ssh_key,omitempty"`
	Tags              string     `gorm:"size:512" json:"tags"`
	Status            string     `gorm:"size:32;not null;default:offline" json:"status"`
	BasePath          string     `gorm:"size:255" json:"base_path"`
	ConnectionLatency int        `gorm:"not null;default:0" json:"connection_latency_ms"`
	DiskUsedGB        int        `gorm:"not null;default:0" json:"disk_used_gb"`
	DiskTotalGB       int        `gorm:"not null;default:0" json:"disk_total_gb"`
	LastSeenAt        *time.Time `json:"last_seen_at"`
	LastBackupAt      *time.Time `json:"last_backup_at"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

type Policy struct {
	ID            uint      `gorm:"primaryKey" json:"id"`
	Name          string    `gorm:"size:128;not null;uniqueIndex" json:"name"`
	Description   string    `gorm:"size:255" json:"description"`
	SourcePath    string    `gorm:"size:512;not null" json:"source_path"`
	TargetPath    string    `gorm:"size:512;not null" json:"target_path"`
	CronSpec      string    `gorm:"size:128;not null" json:"cron_spec"`
	ExcludeRules  string    `gorm:"type:text" json:"exclude_rules"`
	BwLimit       int       `gorm:"not null;default:0" json:"bwlimit"`
	RetentionDays int       `gorm:"not null;default:7" json:"retention_days"`
	MaxConcurrent int       `gorm:"not null;default:1" json:"max_concurrent"`
	Enabled       bool      `gorm:"not null;default:true" json:"enabled"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type Integration struct {
	ID              uint      `gorm:"primaryKey" json:"id"`
	Type            string    `gorm:"size:32;not null" json:"type"`
	Name            string    `gorm:"size:128;not null;uniqueIndex" json:"name"`
	Endpoint        string    `gorm:"size:1024;not null" json:"endpoint"`
	Enabled         bool      `gorm:"not null;default:true" json:"enabled"`
	FailThreshold   int       `gorm:"not null;default:1" json:"fail_threshold"`
	CooldownMinutes int       `gorm:"not null;default:5" json:"cooldown_minutes"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type Alert struct {
	ID             uint       `gorm:"primaryKey" json:"id"`
	NodeID         uint       `gorm:"not null;index:idx_alerts_dedup" json:"node_id"`
	NodeName       string     `gorm:"size:128;not null" json:"node_name"`
	TaskID         *uint      `gorm:"index" json:"task_id"`
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
	ID           uint       `gorm:"primaryKey" json:"id"`
	Name         string     `gorm:"size:128;not null" json:"name"`
	NodeID       uint       `gorm:"not null;index" json:"node_id"`
	Node         Node       `json:"node,omitempty"`
	PolicyID     *uint      `gorm:"index" json:"policy_id,omitempty"`
	Policy       *Policy    `json:"policy,omitempty"`
	Command      string     `gorm:"type:text" json:"command"`
	RsyncSource  string     `gorm:"size:512" json:"rsync_source"`
	RsyncTarget  string     `gorm:"size:512" json:"rsync_target"`
	ExecutorType string     `gorm:"size:32;not null;default:local" json:"executor_type"`
	CronSpec     string     `gorm:"size:128" json:"cron_spec"`
	Status       string     `gorm:"size:32;not null;index" json:"status"`
	RetryCount   int        `gorm:"not null;default:0" json:"retry_count"`
	LastError    string     `gorm:"type:text" json:"last_error"`
	LastRunAt    *time.Time `json:"last_run_at"`
	NextRunAt    *time.Time `json:"next_run_at"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
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
	Level     string    `gorm:"size:16;not null" json:"level"`
	Message   string    `gorm:"type:text;not null" json:"message"`
	CreatedAt time.Time `json:"created_at"`
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
