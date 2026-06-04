package model

import (
	"encoding/json"
	"strings"
	"time"

	"xirang/backend/internal/secure"

	"gorm.io/gorm"
)

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
	ID                   uint       `gorm:"primaryKey" json:"id"`
	Name                 string     `gorm:"size:128;not null;uniqueIndex" json:"name"`
	Host                 string     `gorm:"size:255;not null" json:"host"`
	Port                 int        `gorm:"not null;default:22" json:"port"`
	Username             string     `gorm:"size:128;not null" json:"username"`
	AuthType             string     `gorm:"size:32;not null;default:key" json:"auth_type"`
	Password             string     `gorm:"size:255" json:"password,omitempty"`
	PrivateKey           string     `gorm:"type:text" json:"private_key,omitempty"`
	SSHKeyID             *uint      `gorm:"index" json:"ssh_key_id"`
	SSHKey               *SSHKey    `json:"ssh_key,omitempty"`
	Tags                 string     `gorm:"size:512" json:"tags"`
	Status               string     `gorm:"size:32;not null;default:offline" json:"status"`
	BasePath             string     `gorm:"size:255" json:"base_path"`
	BackupDir            string     `gorm:"size:128;not null;uniqueIndex" json:"backup_dir"`
	UseSudo              bool       `gorm:"not null;default:false" json:"use_sudo"`
	ConnectionLatency    int        `gorm:"not null;default:0" json:"connection_latency_ms"`
	DiskUsedGB           int        `gorm:"not null;default:0" json:"disk_used_gb"`
	DiskTotalGB          int        `gorm:"not null;default:0" json:"disk_total_gb"`
	LastSeenAt           *time.Time `json:"last_seen_at"`
	LastBackupAt         *time.Time `json:"last_backup_at"`
	LastProbeAt          *time.Time `json:"last_probe_at"`
	ConsecutiveFailures  int        `gorm:"not null;default:0" json:"consecutive_failures"`
	MaintenanceStart     *time.Time `json:"maintenance_start,omitempty"`
	MaintenanceEnd       *time.Time `json:"maintenance_end,omitempty"`
	ExpiryDate           *time.Time `gorm:"" json:"expiry_date,omitempty"`
	Archived             bool       `gorm:"not null;default:false" json:"archived"`
	LogPaths             string     `gorm:"type:text" json:"log_paths"`
	LogJournalctlEnabled bool       `gorm:"not null;default:true" json:"log_journalctl_enabled"`
	LogRetentionDays     int        `gorm:"not null;default:0" json:"log_retention_days"`
	EscalationPolicyID   *uint      `gorm:"index" json:"escalation_policy_id"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
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

// DecodedLogPaths returns the parsed whitelist. nil on empty/invalid JSON.
func (n *Node) DecodedLogPaths() []string {
	if strings.TrimSpace(n.LogPaths) == "" {
		return nil
	}
	var paths []string
	if err := json.Unmarshal([]byte(n.LogPaths), &paths); err != nil {
		return nil
	}
	return paths
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
	ProbeOK     bool      `gorm:"not null" json:"probe_ok"`
	SampledAt   time.Time `gorm:"not null;index:idx_node_metric_node_sampled,priority:2;index:idx_node_metric_sampled_at" json:"sampled_at"`
	CreatedAt   time.Time `json:"created_at"`
}

// NodeMetricSampleHourly 节点资源采样 1h 聚合桶
type NodeMetricSampleHourly struct {
	ID            uint      `gorm:"primaryKey" json:"id"`
	NodeID        uint      `gorm:"not null;uniqueIndex:idx_nmsh_node_bucket,priority:1" json:"node_id"`
	BucketStart   time.Time `gorm:"not null;uniqueIndex:idx_nmsh_node_bucket,priority:2;index:idx_nmsh_bucket" json:"bucket_start"`
	CpuPctAvg     *float64  `gorm:"column:cpu_pct_avg" json:"cpu_pct_avg,omitempty"`
	CpuPctMax     *float64  `gorm:"column:cpu_pct_max" json:"cpu_pct_max,omitempty"`
	MemPctAvg     *float64  `gorm:"column:mem_pct_avg" json:"mem_pct_avg,omitempty"`
	MemPctMax     *float64  `gorm:"column:mem_pct_max" json:"mem_pct_max,omitempty"`
	DiskPctAvg    *float64  `gorm:"column:disk_pct_avg" json:"disk_pct_avg,omitempty"`
	DiskPctMax    *float64  `gorm:"column:disk_pct_max" json:"disk_pct_max,omitempty"`
	Load1Avg      *float64  `gorm:"column:load1_avg" json:"load1_avg,omitempty"`
	Load1Max      *float64  `gorm:"column:load1_max" json:"load1_max,omitempty"`
	LatencyMsAvg  *float64  `gorm:"column:latency_ms_avg" json:"latency_ms_avg,omitempty"`
	LatencyMsMax  *float64  `gorm:"column:latency_ms_max" json:"latency_ms_max,omitempty"`
	DiskGBUsedAvg *float64  `gorm:"column:disk_gb_used_avg" json:"disk_gb_used_avg,omitempty"`
	DiskGBTotal   *float64  `gorm:"column:disk_gb_total" json:"disk_gb_total,omitempty"`
	ProbeOK       int64     `gorm:"column:probe_ok;not null;default:0" json:"probe_ok"`
	ProbeFail     int64     `gorm:"column:probe_fail;not null;default:0" json:"probe_fail"`
	SampleCount   int64     `gorm:"column:sample_count;not null;default:0" json:"sample_count"`
	CreatedAt     time.Time `json:"created_at"`
}

func (NodeMetricSampleHourly) TableName() string { return "node_metric_samples_hourly" }

// NodeMetricSampleDaily 节点资源采样 1d 聚合桶
type NodeMetricSampleDaily struct {
	ID            uint      `gorm:"primaryKey" json:"id"`
	NodeID        uint      `gorm:"not null;uniqueIndex:idx_nmsd_node_bucket,priority:1" json:"node_id"`
	BucketStart   time.Time `gorm:"not null;uniqueIndex:idx_nmsd_node_bucket,priority:2;index:idx_nmsd_bucket" json:"bucket_start"`
	CpuPctAvg     *float64  `gorm:"column:cpu_pct_avg" json:"cpu_pct_avg,omitempty"`
	CpuPctMax     *float64  `gorm:"column:cpu_pct_max" json:"cpu_pct_max,omitempty"`
	MemPctAvg     *float64  `gorm:"column:mem_pct_avg" json:"mem_pct_avg,omitempty"`
	MemPctMax     *float64  `gorm:"column:mem_pct_max" json:"mem_pct_max,omitempty"`
	DiskPctAvg    *float64  `gorm:"column:disk_pct_avg" json:"disk_pct_avg,omitempty"`
	DiskPctMax    *float64  `gorm:"column:disk_pct_max" json:"disk_pct_max,omitempty"`
	Load1Avg      *float64  `gorm:"column:load1_avg" json:"load1_avg,omitempty"`
	Load1Max      *float64  `gorm:"column:load1_max" json:"load1_max,omitempty"`
	LatencyMsAvg  *float64  `gorm:"column:latency_ms_avg" json:"latency_ms_avg,omitempty"`
	LatencyMsMax  *float64  `gorm:"column:latency_ms_max" json:"latency_ms_max,omitempty"`
	DiskGBUsedAvg *float64  `gorm:"column:disk_gb_used_avg" json:"disk_gb_used_avg,omitempty"`
	DiskGBTotal   *float64  `gorm:"column:disk_gb_total" json:"disk_gb_total,omitempty"`
	ProbeOK       int64     `gorm:"column:probe_ok;not null;default:0" json:"probe_ok"`
	ProbeFail     int64     `gorm:"column:probe_fail;not null;default:0" json:"probe_fail"`
	SampleCount   int64     `gorm:"column:sample_count;not null;default:0" json:"sample_count"`
	CreatedAt     time.Time `json:"created_at"`
}

func (NodeMetricSampleDaily) TableName() string { return "node_metric_samples_daily" }

// NodeOwner 节点 ownership 关联表（operator 只能访问自己负责的节点）
type NodeOwner struct {
	NodeID    uint      `gorm:"primaryKey" json:"node_id"`
	UserID    uint      `gorm:"primaryKey" json:"user_id"`
	User      User      `gorm:"foreignKey:UserID" json:"user"`
	CreatedAt time.Time `json:"created_at"`
}

// NodeLog is a single log line ingested from a node.
type NodeLog struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	NodeID    uint      `gorm:"not null;index:idx_node_logs_node_time,priority:1" json:"node_id"`
	Source    string    `gorm:"size:16;not null" json:"source"`
	Path      string    `gorm:"type:text;not null" json:"path"`
	Timestamp time.Time `gorm:"not null;index:idx_node_logs_node_time,priority:2,sort:desc" json:"timestamp"`
	Priority  string    `gorm:"size:16" json:"priority"`
	Message   string    `gorm:"type:text;not null" json:"message"`
	CreatedAt time.Time `gorm:"index" json:"created_at"`
}

// NodeLogCursor tracks incremental collection position per (node, source, path).
type NodeLogCursor struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	NodeID     uint      `gorm:"not null;uniqueIndex:uk_node_log_cursors,priority:1" json:"node_id"`
	Source     string    `gorm:"size:16;not null;uniqueIndex:uk_node_log_cursors,priority:2" json:"source"`
	Path       string    `gorm:"type:text;not null;uniqueIndex:uk_node_log_cursors,priority:3" json:"path"`
	CursorText string    `gorm:"type:text" json:"cursor_text"`
	FileOffset int64     `json:"file_offset"`
	FileInode  int64     `json:"file_inode"`
	UpdatedAt  time.Time `json:"updated_at"`
}
