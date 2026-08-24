// Package settings provides runtime-configurable system settings.
//
// Service manages settings stored in the database (system_settings table).
// Settings changed via the API take effect immediately without restarting the
// server. TTL-based caching (30s) ensures reads stay fast even under frequent
// calls to GetEffective().
//
// Two-tier configuration model:
//
//   - config.Config (internal/config): boot-time env vars, immutable after
//     Load(). Provides startup defaults for DB connection, JWT secrets, listen
//     address, etc.
//
//   - Service (this package): runtime-configurable values persisted to DB.
//     Resolution precedence is: DB value > env var > code default. This means
//     a DB override always wins, even when the corresponding env var is set.
//
// Settings whose EnvVar matches a config.Config field are documented with an
// "Overlap note" comment: config.Config provides the boot-time default;
// Service can override at runtime. Consumers that need the live value should
// call GetEffective() rather than reading the env var directly.
package settings

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"
	"xirang/backend/internal/sshutil"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SettingType 设置值类型
type SettingType string

const (
	TypeInt      SettingType = "int"
	TypeBool     SettingType = "bool"
	TypeDuration SettingType = "duration"
	TypeString   SettingType = "string"

	cacheTTL       = 30 * time.Second
	maxValueLength = 256
)

const (
	ProcessingContentPipelineRevisionKey = "backup_assets.internal.processing_content_pipeline_revision"
	ProcessingOCRPipelineRevisionKey     = "backup_assets.internal.processing_ocr_pipeline_revision"
	RecoveryTargetRootKeyPrefix          = "backup_assets.internal.recovery_target_root.v1."
	RecoveryTargetRootReceiptKeyPrefix   = "backup_assets.internal.recovery_root_receipt.v1."
	RecoveryDowngradeReceiptKeyPrefix    = "backup_assets.internal.recovery_downgrade_receipt.v1."
)

const (
	recoveryTargetRootSchemaVersion     = 2
	recoveryTargetRootIDMaxBytes        = 32
	recoveryTargetRootSafeLabelMaxBytes = 128
	recoveryTargetRootLocatorMaxBytes   = 4096
	recoveryTargetRootDocumentMaxBytes  = 8 << 10
	recoveryTargetRootListMax           = 64
	recoveryTargetRootAllListMax        = 1024
	recoveryTargetRootDigestDomain      = "xirang/recovery/target-root/v1"
)

var (
	ErrInternalSettingUnavailable    = errors.New("internal setting state unavailable")
	ErrRecoveryTargetRootInvalid     = errors.New("recovery target root definition invalid")
	ErrRecoveryTargetRootNotFound    = errors.New("recovery target root not found")
	ErrRecoveryTargetRootUnavailable = errors.New("recovery target root state unavailable")
)

type RecoveryTargetRootDefinition struct {
	NodeID                  uint
	RootID                  string
	SafeLabel               string
	Locator                 string                   `json:"-"`
	AuthorityRevision       string                   `json:"-"`
	RootObservationRevision string                   `json:"-"`
	Policy                  RecoveryTargetRootPolicy `json:"-"`
}

func (definition RecoveryTargetRootDefinition) String() string {
	return "RecoveryTargetRootDefinition{NodeID:" + strconv.FormatUint(uint64(definition.NodeID), 10) +
		", RootID:" + strconv.Quote(definition.RootID) + ", SafeLabel:" + strconv.Quote(definition.SafeLabel) + "}"
}

func (definition RecoveryTargetRootDefinition) GoString() string { return definition.String() }

type RecoveryTargetRootPolicy struct {
	ReserveBytes         int64  `json:"-"`
	ReserveInodes        int64  `json:"-"`
	OverlapPolicyBinding string `json:"-"`
}

func (policy RecoveryTargetRootPolicy) String() string {
	return "RecoveryTargetRootPolicy{}"
}

func (policy RecoveryTargetRootPolicy) GoString() string { return policy.String() }

type RecoveryTargetRootSummary struct {
	NodeID    uint   `json:"node_id"`
	RootID    string `json:"root_id"`
	SafeLabel string `json:"safe_label"`
}

type RecoveryTargetRootReference struct {
	NodeID uint   `json:"node_id"`
	RootID string `json:"root_id"`
}

// ValidateRecoveryTargetRootReference applies the registry's canonical node
// and root identifier rules without reading durable state.
func ValidateRecoveryTargetRootReference(reference RecoveryTargetRootReference) error {
	_, err := recoveryTargetRootKey(reference.NodeID, reference.RootID)
	return err
}

type RecoveryTargetRootResolution struct {
	NodeID                  uint                     `json:"node_id"`
	RootID                  string                   `json:"root_id"`
	SafeLabel               string                   `json:"safe_label"`
	Locator                 string                   `json:"-"`
	LocatorDigest           string                   `json:"-"`
	AuthorityRevision       string                   `json:"-"`
	RootObservationRevision string                   `json:"-"`
	Policy                  RecoveryTargetRootPolicy `json:"-"`
}

func (resolution RecoveryTargetRootResolution) String() string {
	return "RecoveryTargetRootResolution{NodeID:" + strconv.FormatUint(uint64(resolution.NodeID), 10) +
		", RootID:" + strconv.Quote(resolution.RootID) + ", SafeLabel:" + strconv.Quote(resolution.SafeLabel) + "}"
}

func (resolution RecoveryTargetRootResolution) GoString() string { return resolution.String() }

type recoveryTargetRootRecord struct {
	SchemaVersion           int    `json:"schema_version"`
	NodeID                  uint   `json:"node_id"`
	RootID                  string `json:"root_id"`
	SafeLabel               string `json:"safe_label"`
	CanonicalLocator        string `json:"canonical_locator"`
	LocatorDigest           string `json:"locator_digest"`
	AuthorityRevision       string `json:"authority_revision"`
	RootObservationRevision string `json:"root_observation_revision"`
	ReserveBytes            int64  `json:"reserve_bytes"`
	ReserveInodes           int64  `json:"reserve_inodes"`
	OverlapPolicyBinding    string `json:"overlap_policy_binding"`
}

type ProcessingPipelineRevisions struct {
	Content int64
	OCR     int64
}

// SettingDef 设置项定义
type SettingDef struct {
	Key             string      `json:"key"`
	EnvVar          string      `json:"env_var"`
	CodeDefault     string      `json:"code_default"`
	Type            SettingType `json:"type"`
	Category        string      `json:"category"`
	Description     string      `json:"description"`
	Min             string      `json:"min,omitempty"`
	Max             string      `json:"max,omitempty"`
	MinDuration     string      `json:"min_duration,omitempty"` // 安全下限（duration 类型）
	MaxDuration     string      `json:"max_duration,omitempty"` // 安全上限（duration 类型）
	RequiresRestart bool        `json:"requires_restart"`
	Sensitive       bool        `json:"sensitive"`
}

// ResolvedSetting 已解析的设置值（含来源信息）
type ResolvedSetting struct {
	Value     string     `json:"value"`
	Source    string     `json:"source"` // "db" | "env" | "default"
	UpdatedAt *time.Time `json:"updated_at"`
}

type cachedValue struct {
	value     string
	expiresAt time.Time
}

type exclusiveMutationGate chan struct{}

func newExclusiveMutationGate() exclusiveMutationGate {
	gate := make(exclusiveMutationGate, 1)
	gate <- struct{}{}
	return gate
}

func (gate exclusiveMutationGate) acquire(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-gate:
		return nil
	}
}

func (gate exclusiveMutationGate) acquireBlocking() {
	<-gate
}

func (gate exclusiveMutationGate) release() {
	gate <- struct{}{}
}

// Service 系统设置服务
type Service struct {
	db                      *gorm.DB
	mu                      sync.RWMutex
	backupAssetMutationGate exclusiveMutationGate
	cache                   map[string]cachedValue
}

// BackupAssetOverrideSnapshot is an opaque, exact database-override snapshot.
// It records both raw stored rows and row absence so compensation can restore
// the same DB > env > default state without re-encrypting values.
type BackupAssetOverrideSnapshot struct {
	keys []string
	rows map[string]model.SystemSetting
}

// NewService 创建设置服务
func NewService(db *gorm.DB) *Service {
	return &Service{
		db:                      db,
		backupAssetMutationGate: newExclusiveMutationGate(),
		cache:                   make(map[string]cachedValue),
	}
}

// registry lists all dynamic settings definitions.
var registry = []SettingDef{
	// Overlap note: login.rate_limit is also defined in config.Config as
	// LoginRateLimit. Config provides the default at startup;
	// settings.Service can override at runtime.
	{Key: "login.rate_limit", EnvVar: "LOGIN_RATE_LIMIT", CodeDefault: "10", Type: TypeInt, Category: "security", Description: "登录接口每窗口最大请求数", Min: "5", Max: "1000"},
	// Overlap note: login.rate_window is also defined in config.Config as
	// LoginRateWindow. Config provides the default at startup;
	// settings.Service can override at runtime.
	{Key: "login.rate_window", EnvVar: "LOGIN_RATE_WINDOW", CodeDefault: "1m", Type: TypeDuration, Category: "security", Description: "登录限流时间窗口", MinDuration: "10s"},
	// Overlap note: login.fail_lock_threshold is also defined in
	// config.Config as LoginFailLockThreshold. Config provides the default
	// at startup; settings.Service can override at runtime.
	{Key: "login.fail_lock_threshold", EnvVar: "LOGIN_FAIL_LOCK_THRESHOLD", CodeDefault: "5", Type: TypeInt, Category: "security", Description: "连续登录失败锁定阈值", Min: "3", Max: "100"},
	// Overlap note: login.fail_lock_duration is also defined in
	// config.Config as LoginFailLockDuration. Config provides the default
	// at startup; settings.Service can override at runtime.
	{Key: "login.fail_lock_duration", EnvVar: "LOGIN_FAIL_LOCK_DURATION", CodeDefault: "15m", Type: TypeDuration, Category: "security", Description: "登录锁定持续时间", MinDuration: "1m"},
	{Key: "login.captcha_enabled", EnvVar: "LOGIN_CAPTCHA_ENABLED", CodeDefault: "false", Type: TypeBool, Category: "security", Description: "启用登录验证码"},
	{Key: "login.second_captcha_enabled", EnvVar: "LOGIN_SECOND_CAPTCHA_ENABLED", CodeDefault: "false", Type: TypeBool, Category: "security", Description: "启用登录二次验证码"},
	// Overlap note: node.probe_interval is also defined in config.Config as
	// NodeProbeInterval. Config provides the default at startup;
	// settings.Service can override at runtime. RequiresRestart: true.
	{Key: "node.probe_interval", EnvVar: "NODE_PROBE_INTERVAL", CodeDefault: "5m", Type: TypeDuration, Category: "node_monitor", Description: "节点探测间隔", RequiresRestart: true},
	// Overlap note: node.probe_fail_threshold is also defined in
	// config.Config as NodeProbeFailThreshold. Config provides the default
	// at startup; settings.Service can override at runtime.
	// RequiresRestart: true.
	{Key: "node.probe_fail_threshold", EnvVar: "NODE_PROBE_FAIL_THRESHOLD", CodeDefault: "3", Type: TypeInt, Category: "node_monitor", Description: "节点探测失败阈值", Min: "1", Max: "100", RequiresRestart: true},
	// Overlap note: node.probe_concurrency is also defined in config.Config
	// as NodeProbeConcurrency. Config provides the default at startup;
	// settings.Service can override at runtime. RequiresRestart: true.
	{Key: "node.probe_concurrency", EnvVar: "NODE_PROBE_CONCURRENCY", CodeDefault: "10", Type: TypeInt, Category: "node_monitor", Description: "节点探测并发数", Min: "1", Max: "100", RequiresRestart: true},
	// Overlap note: retention.task_traffic_days is also defined in
	// config.Config as TaskTrafficRetentionDays. Config provides the
	// default at startup; settings.Service can override at runtime.
	{Key: "retention.task_traffic_days", EnvVar: "TASK_TRAFFIC_RETENTION_DAYS", CodeDefault: "8", Type: TypeInt, Category: "retention", Description: "任务流量数据保留天数", Min: "1", Max: "365"},
	// Overlap note: retention.task_run_days is also defined in
	// config.Config as TaskRunRetentionDays. Config provides the default at
	// startup; settings.Service can override at runtime.
	{Key: "retention.task_run_days", EnvVar: "TASK_RUN_RETENTION_DAYS", CodeDefault: "90", Type: TypeInt, Category: "retention", Description: "任务执行记录保留天数", Min: "1", Max: "3650"},
	// Overlap note: retention.check_interval is also defined in
	// config.Config as RetentionCheckInterval. Config provides the default
	// at startup; settings.Service can override at runtime.
	{Key: "retention.check_interval", EnvVar: "RETENTION_CHECK_INTERVAL", CodeDefault: "6h", Type: TypeDuration, Category: "retention", Description: "保留策略检查间隔"},
	// Overlap note: storage.min_free_gb is also defined in config.Config as
	// BackupStorageMinFreeGB. Config provides the default at startup;
	// settings.Service can override at runtime.
	{Key: "storage.min_free_gb", EnvVar: "BACKUP_STORAGE_MIN_FREE_GB", CodeDefault: "10", Type: TypeInt, Category: "storage", Description: "备份存储最小可用空间 (GB)", Min: "0", Max: "10000"},
	// Overlap note: storage.max_usage_pct is also defined in
	// config.Config as BackupStorageMaxUsagePct. Config provides the
	// default at startup; settings.Service can override at runtime.
	{Key: "storage.max_usage_pct", EnvVar: "BACKUP_STORAGE_MAX_USAGE_PCT", CodeDefault: "90", Type: TypeInt, Category: "storage", Description: "备份存储最大使用率 (%)", Min: "0", Max: "100"},
	{Key: "alert.dedup_window", EnvVar: "ALERT_DEDUP_WINDOW", CodeDefault: "10m", Type: TypeDuration, Category: "alert", Description: "告警去重时间窗口"},
	{Key: "logs.retention_days_default", EnvVar: "LOG_RETENTION_DAYS_DEFAULT", CodeDefault: "30", Type: TypeInt, Category: "logs", Description: "节点日志默认保留天数（节点未单独配置时生效）", Min: "1", Max: "365"},
	{Key: "anomaly.enabled", EnvVar: "ANOMALY_ENABLED", CodeDefault: "true", Type: TypeBool, Category: "anomaly", Description: "启用基线异常检测总开关"},
	{Key: "anomaly.alerts_enabled", EnvVar: "ANOMALY_ALERTS_ENABLED", CodeDefault: "false", Type: TypeBool, Category: "anomaly", Description: "将异常事件升级为告警通知；默认仅记录事件"},
	{Key: "anomaly.ewma_alpha", EnvVar: "ANOMALY_EWMA_ALPHA", CodeDefault: "0.3", Type: TypeString, Category: "anomaly", Description: "EWMA 平滑因子 α (0.1-0.9)"},
	{Key: "anomaly.ewma_sigma", EnvVar: "ANOMALY_EWMA_SIGMA", CodeDefault: "5.0", Type: TypeString, Category: "anomaly", Description: "EWMA 异常判定 k 倍标准差 (默认 5.0)"},
	{Key: "anomaly.ewma_window_hours", EnvVar: "ANOMALY_EWMA_WINDOW_HOURS", CodeDefault: "6", Type: TypeInt, Category: "anomaly", Description: "EWMA 回看样本窗口 (小时)", Min: "1", Max: "6"},
	{Key: "anomaly.ewma_min_samples", EnvVar: "ANOMALY_EWMA_MIN_SAMPLES", CodeDefault: "24", Type: TypeInt, Category: "anomaly", Description: "EWMA 最少样本数", Min: "5", Max: "50"},
	{Key: "anomaly.disk_forecast_days", EnvVar: "ANOMALY_DISK_FORECAST_DAYS", CodeDefault: "7", Type: TypeInt, Category: "anomaly", Description: "磁盘预测事件天数阈值", Min: "1", Max: "30"},
	{Key: "anomaly.disk_forecast_min_history_hours", EnvVar: "ANOMALY_DISK_FORECAST_MIN_HISTORY_HOURS", CodeDefault: "72", Type: TypeInt, Category: "anomaly", Description: "磁盘预测所需最少历史小时", Min: "24", Max: "720"},
	{Key: "anomaly.events_retention_days", EnvVar: "ANOMALY_EVENTS_RETENTION_DAYS", CodeDefault: "30", Type: TypeInt, Category: "anomaly", Description: "异常事件保留天数", Min: "7", Max: "365"},
	{Key: "alerts.silence_retention_days", EnvVar: "SILENCE_RETENTION_DAYS", CodeDefault: "30", Type: TypeInt, Category: "retention", Description: "已过期静默规则的审计保留天数（超出后删除）", Min: "1", Max: "365"},
	{Key: "metrics.remote_url", EnvVar: "METRICS_REMOTE_URL", CodeDefault: "", Type: TypeString, Category: "metrics", Description: "Prometheus remote-write 端点 URL（如 https://mimir.example.com/api/v1/push）；留空禁用远程推送", RequiresRestart: true},
	{Key: "metrics.remote_bearer_token", EnvVar: "METRICS_REMOTE_BEARER_TOKEN", CodeDefault: "", Type: TypeString, Category: "metrics", Description: "Prometheus remote-write 鉴权 Bearer token；生产环境建议使用环境变量配置以避免明文存库", RequiresRestart: true, Sensitive: true},
	{Key: "smtp.host", EnvVar: "SMTP_HOST", CodeDefault: "", Type: TypeString, Category: "alerting", Description: "SMTP 服务器地址（启用邮件告警时必填）"},
	{Key: "smtp.port", EnvVar: "SMTP_PORT", CodeDefault: "587", Type: TypeString, Category: "alerting", Description: "SMTP 端口（默认 587 STARTTLS；465 走隐式 TLS）"},
	{Key: "smtp.user", EnvVar: "SMTP_USER", CodeDefault: "", Type: TypeString, Category: "alerting", Description: "SMTP 用户名"},
	{Key: "smtp.password", EnvVar: "SMTP_PASS", CodeDefault: "", Type: TypeString, Category: "alerting", Description: "SMTP 密码（生产环境建议通过环境变量注入而非入库）", Sensitive: true},
	{Key: "smtp.from", EnvVar: "SMTP_FROM", CodeDefault: "", Type: TypeString, Category: "alerting", Description: "发件人地址；为空时回退到 smtp.user"},
	{Key: "smtp.require_tls", EnvVar: "SMTP_REQUIRE_TLS", CodeDefault: "true", Type: TypeBool, Category: "alerting", Description: "强制 TLS 连接（465 隐式 / 587 STARTTLS）；false 回退明文"},
	{Key: "backup_assets.enabled", EnvVar: "BACKUP_ASSETS_ENABLED", CodeDefault: "false", Type: TypeBool, Category: "backup_assets", Description: "启用备份资产领域功能"},
	{Key: "backup_assets.retention_reconcile_interval", EnvVar: "BACKUP_ASSETS_RETENTION_RECONCILE_INTERVAL", CodeDefault: "5m", Type: TypeDuration, Category: "backup_assets", Description: "备份资产保留策略协调间隔", MinDuration: "30s", MaxDuration: "24h"},
	{Key: "backup_assets.retention_batch_size", EnvVar: "BACKUP_ASSETS_RETENTION_BATCH_SIZE", CodeDefault: "100", Type: TypeInt, Category: "backup_assets", Description: "备份资产保留策略单批处理上限", Min: "1", Max: "1000"},
	{Key: "backup_assets.retention_drain_timeout", EnvVar: "BACKUP_ASSETS_RETENTION_DRAIN_TIMEOUT", CodeDefault: "30s", Type: TypeDuration, Category: "backup_assets", Description: "备份资产保留策略读取排空超时", MinDuration: "5s", MaxDuration: "30m"},
	{Key: "backup_assets.content_preview_ttl", EnvVar: "BACKUP_ASSETS_CONTENT_PREVIEW_TTL", CodeDefault: "2m", Type: TypeDuration, Category: "backup_assets", Description: "备份内容预览票据绝对有效期", MinDuration: "15s", MaxDuration: "10m"},
	{Key: "backup_assets.content_media_ttl", EnvVar: "BACKUP_ASSETS_CONTENT_MEDIA_TTL", CodeDefault: "15m", Type: TypeDuration, Category: "backup_assets", Description: "备份媒体与下载票据绝对有效期", MinDuration: "1m", MaxDuration: "30m"},
	{Key: "backup_assets.content_idle_ttl", EnvVar: "BACKUP_ASSETS_CONTENT_IDLE_TTL", CodeDefault: "60s", Type: TypeDuration, Category: "backup_assets", Description: "备份内容会话空闲有效期", MinDuration: "15s", MaxDuration: "10m"},
	{Key: "backup_assets.content_write_idle_timeout", EnvVar: "BACKUP_ASSETS_CONTENT_WRITE_IDLE_TIMEOUT", CodeDefault: "30s", Type: TypeDuration, Category: "backup_assets", Description: "备份内容流单次写入空闲超时", MinDuration: "5s", MaxDuration: "2m"},
	{Key: "backup_assets.content_ticket_timeout", EnvVar: "BACKUP_ASSETS_CONTENT_TICKET_TIMEOUT", CodeDefault: "20s", Type: TypeDuration, Category: "backup_assets", Description: "备份内容签票处理超时", MinDuration: "1s", MaxDuration: "25s"},
	{Key: "backup_assets.content_request_max_bytes", EnvVar: "BACKUP_ASSETS_CONTENT_REQUEST_MAX_BYTES", CodeDefault: "67108864", Type: TypeInt, Category: "backup_assets", Description: "单次备份内容请求最大字节数", Min: "65536", Max: "1073741824"},
	{Key: "backup_assets.content_cumulative_max_bytes", EnvVar: "BACKUP_ASSETS_CONTENT_CUMULATIVE_MAX_BYTES", CodeDefault: "536870912", Type: TypeInt, Category: "backup_assets", Description: "单张备份内容票据累计最大字节数", Min: "65536", Max: "8589934592"},
	{Key: "backup_assets.content_max_requests", EnvVar: "BACKUP_ASSETS_CONTENT_MAX_REQUESTS", CodeDefault: "256", Type: TypeInt, Category: "backup_assets", Description: "单张备份内容票据最大请求数", Min: "1", Max: "4096"},
	{Key: "backup_assets.content_grant_max_in_flight", EnvVar: "BACKUP_ASSETS_CONTENT_GRANT_MAX_IN_FLIGHT", CodeDefault: "2", Type: TypeInt, Category: "backup_assets", Description: "单张备份内容票据最大并发数", Min: "1", Max: "8"},
	{Key: "backup_assets.content_user_max_concurrency", EnvVar: "BACKUP_ASSETS_CONTENT_USER_MAX_CONCURRENCY", CodeDefault: "4", Type: TypeInt, Category: "backup_assets", Description: "单用户备份内容最大并发数", Min: "1", Max: "32"},
	{Key: "backup_assets.content_provider_max_concurrency", EnvVar: "BACKUP_ASSETS_CONTENT_PROVIDER_MAX_CONCURRENCY", CodeDefault: "4", Type: TypeInt, Category: "backup_assets", Description: "单 Provider 备份内容最大并发数", Min: "1", Max: "32"},
	{Key: "backup_assets.content_global_max_concurrency", EnvVar: "BACKUP_ASSETS_CONTENT_GLOBAL_MAX_CONCURRENCY", CodeDefault: "16", Type: TypeInt, Category: "backup_assets", Description: "全局备份内容最大并发数", Min: "1", Max: "128"},
	{Key: "backup_assets.content_rate_window", EnvVar: "BACKUP_ASSETS_CONTENT_RATE_WINDOW", CodeDefault: "1m", Type: TypeDuration, Category: "backup_assets", Description: "备份内容范围预算窗口", MinDuration: "10s", MaxDuration: "10m"},
	{Key: "backup_assets.content_user_window_bytes", EnvVar: "BACKUP_ASSETS_CONTENT_USER_WINDOW_BYTES", CodeDefault: "1073741824", Type: TypeInt, Category: "backup_assets", Description: "单用户窗口字节预算", Min: "65536", Max: "17179869184"},
	{Key: "backup_assets.content_provider_window_bytes", EnvVar: "BACKUP_ASSETS_CONTENT_PROVIDER_WINDOW_BYTES", CodeDefault: "4294967296", Type: TypeInt, Category: "backup_assets", Description: "单 Provider 窗口字节预算", Min: "65536", Max: "68719476736"},
	{Key: "backup_assets.content_global_window_bytes", EnvVar: "BACKUP_ASSETS_CONTENT_GLOBAL_WINDOW_BYTES", CodeDefault: "8589934592", Type: TypeInt, Category: "backup_assets", Description: "全局窗口字节预算", Min: "65536", Max: "137438953472"},
	{Key: "backup_assets.content_user_window_requests", EnvVar: "BACKUP_ASSETS_CONTENT_USER_WINDOW_REQUESTS", CodeDefault: "1024", Type: TypeInt, Category: "backup_assets", Description: "单用户窗口请求预算", Min: "1", Max: "65536"},
	{Key: "backup_assets.content_provider_window_requests", EnvVar: "BACKUP_ASSETS_CONTENT_PROVIDER_WINDOW_REQUESTS", CodeDefault: "4096", Type: TypeInt, Category: "backup_assets", Description: "单 Provider 窗口请求预算", Min: "1", Max: "262144"},
	{Key: "backup_assets.content_global_window_requests", EnvVar: "BACKUP_ASSETS_CONTENT_GLOBAL_WINDOW_REQUESTS", CodeDefault: "8192", Type: TypeInt, Category: "backup_assets", Description: "全局窗口请求预算", Min: "1", Max: "1048576"},
	{Key: "backup_assets.content_classification_scan_bytes", EnvVar: "BACKUP_ASSETS_CONTENT_CLASSIFICATION_SCAN_BYTES", CodeDefault: "262144", Type: TypeInt, Category: "backup_assets", Description: "备份内容分类扫描字节上限", Min: "4096", Max: "4194304"},
	{Key: "backup_assets.content_text_preview_bytes", EnvVar: "BACKUP_ASSETS_CONTENT_TEXT_PREVIEW_BYTES", CodeDefault: "1048576", Type: TypeInt, Category: "backup_assets", Description: "备份文本预览字节上限", Min: "4096", Max: "16777216"},
	{Key: "backup_assets.content_hex_preview_bytes", EnvVar: "BACKUP_ASSETS_CONTENT_HEX_PREVIEW_BYTES", CodeDefault: "65536", Type: TypeInt, Category: "backup_assets", Description: "备份十六进制预览字节上限", Min: "1024", Max: "1048576"},
	{Key: "backup_assets.content_raster_max_pixels", EnvVar: "BACKUP_ASSETS_CONTENT_RASTER_MAX_PIXELS", CodeDefault: "100000000", Type: TypeInt, Category: "backup_assets", Description: "备份栅格预览像素上限", Min: "1000000", Max: "250000000"},
	{Key: "backup_assets.content_memory_global_bytes", EnvVar: "BACKUP_ASSETS_CONTENT_MEMORY_GLOBAL_BYTES", CodeDefault: "67108864", Type: TypeInt, Category: "backup_assets", Description: "备份内容内存缓存全局字节上限", Min: "1048576", Max: "1073741824"},
	{Key: "backup_assets.content_memory_object_bytes", EnvVar: "BACKUP_ASSETS_CONTENT_MEMORY_OBJECT_BYTES", CodeDefault: "4194304", Type: TypeInt, Category: "backup_assets", Description: "备份内容内存缓存单对象字节上限", Min: "65536", Max: "1073741824"},
	{Key: "backup_assets.content_memory_user_bytes", EnvVar: "BACKUP_ASSETS_CONTENT_MEMORY_USER_BYTES", CodeDefault: "16777216", Type: TypeInt, Category: "backup_assets", Description: "备份内容内存缓存单用户字节上限", Min: "65536", Max: "1073741824"},
	{Key: "backup_assets.content_memory_provider_bytes", EnvVar: "BACKUP_ASSETS_CONTENT_MEMORY_PROVIDER_BYTES", CodeDefault: "33554432", Type: TypeInt, Category: "backup_assets", Description: "备份内容内存缓存单 Provider 字节上限", Min: "65536", Max: "1073741824"},
	{Key: "backup_assets.content_cache_enabled", EnvVar: "BACKUP_ASSETS_CONTENT_CACHE_ENABLED", CodeDefault: "true", Type: TypeBool, Category: "backup_assets", Description: "启用备份内容认证磁盘缓存"},
	{Key: "backup_assets.content_cache_root", EnvVar: "BACKUP_ASSETS_CONTENT_CACHE_ROOT", CodeDefault: "/var/cache/xirang/asset-content", Type: TypeString, Category: "backup_assets", Description: "备份内容认证磁盘缓存专用根目录"},
	{Key: "backup_assets.content_cache_chunk_bytes", EnvVar: "BACKUP_ASSETS_CONTENT_CACHE_CHUNK_BYTES", CodeDefault: "1048576", Type: TypeInt, Category: "backup_assets", Description: "备份内容缓存分块字节数", Min: "65536", Max: "8388608"},
	{Key: "backup_assets.content_cache_object_bytes", EnvVar: "BACKUP_ASSETS_CONTENT_CACHE_OBJECT_BYTES", CodeDefault: "536870912", Type: TypeInt, Category: "backup_assets", Description: "备份内容缓存单对象字节上限", Min: "65536", Max: "8589934592"},
	{Key: "backup_assets.content_cache_user_bytes", EnvVar: "BACKUP_ASSETS_CONTENT_CACHE_USER_BYTES", CodeDefault: "2147483648", Type: TypeInt, Category: "backup_assets", Description: "备份内容缓存单用户字节上限", Min: "65536", Max: "34359738368"},
	{Key: "backup_assets.content_cache_provider_bytes", EnvVar: "BACKUP_ASSETS_CONTENT_CACHE_PROVIDER_BYTES", CodeDefault: "4294967296", Type: TypeInt, Category: "backup_assets", Description: "备份内容缓存单 Provider 字节上限", Min: "65536", Max: "68719476736"},
	{Key: "backup_assets.content_cache_global_bytes", EnvVar: "BACKUP_ASSETS_CONTENT_CACHE_GLOBAL_BYTES", CodeDefault: "8589934592", Type: TypeInt, Category: "backup_assets", Description: "备份内容缓存全局字节上限", Min: "65536", Max: "137438953472"},
	{Key: "backup_assets.content_cache_object_files", EnvVar: "BACKUP_ASSETS_CONTENT_CACHE_OBJECT_FILES", CodeDefault: "1024", Type: TypeInt, Category: "backup_assets", Description: "备份内容缓存单对象文件上限", Min: "2", Max: "131072"},
	{Key: "backup_assets.content_cache_user_files", EnvVar: "BACKUP_ASSETS_CONTENT_CACHE_USER_FILES", CodeDefault: "4096", Type: TypeInt, Category: "backup_assets", Description: "备份内容缓存单用户文件上限", Min: "2", Max: "262144"},
	{Key: "backup_assets.content_cache_provider_files", EnvVar: "BACKUP_ASSETS_CONTENT_CACHE_PROVIDER_FILES", CodeDefault: "8192", Type: TypeInt, Category: "backup_assets", Description: "备份内容缓存单 Provider 文件上限", Min: "2", Max: "262144"},
	{Key: "backup_assets.content_cache_global_files", EnvVar: "BACKUP_ASSETS_CONTENT_CACHE_GLOBAL_FILES", CodeDefault: "16384", Type: TypeInt, Category: "backup_assets", Description: "备份内容缓存全局文件上限", Min: "16", Max: "262144"},
	{Key: "backup_assets.content_cache_idle_ttl", EnvVar: "BACKUP_ASSETS_CONTENT_CACHE_IDLE_TTL", CodeDefault: "15m", Type: TypeDuration, Category: "backup_assets", Description: "备份内容缓存空闲有效期", MinDuration: "1m", MaxDuration: "24h"},
	{Key: "backup_assets.content_cache_absolute_ttl", EnvVar: "BACKUP_ASSETS_CONTENT_CACHE_ABSOLUTE_TTL", CodeDefault: "2h", Type: TypeDuration, Category: "backup_assets", Description: "备份内容缓存绝对有效期", MinDuration: "1m", MaxDuration: "24h"},
	{Key: "backup_assets.content_reconcile_interval", EnvVar: "BACKUP_ASSETS_CONTENT_RECONCILE_INTERVAL", CodeDefault: "1m", Type: TypeDuration, Category: "backup_assets", Description: "备份内容状态对账间隔", MinDuration: "10s", MaxDuration: "1h"},
	{Key: "backup_assets.content_reconcile_batch_size", EnvVar: "BACKUP_ASSETS_CONTENT_RECONCILE_BATCH_SIZE", CodeDefault: "100", Type: TypeInt, Category: "backup_assets", Description: "备份内容状态对账批次", Min: "1", Max: "1000"},
	{Key: "backup_assets.content_audit_backlog_max", EnvVar: "BACKUP_ASSETS_CONTENT_AUDIT_BACKLOG_MAX", CodeDefault: "10000", Type: TypeInt, Category: "backup_assets", Description: "备份内容审计积压上限", Min: "100", Max: "100000"},
	{Key: "backup_assets.content_allow_insecure_loopback", EnvVar: "BACKUP_ASSETS_CONTENT_ALLOW_INSECURE_LOOPBACK", CodeDefault: "false", Type: TypeBool, Category: "backup_assets", Description: "仅允许受控本机 HTTP 开发票据 Cookie"},
	{Key: "backup_assets.catalog_batch_size", EnvVar: "BACKUP_ASSETS_CATALOG_BATCH_SIZE", CodeDefault: "2000", Type: TypeInt, Category: "backup_assets", Description: "目录构建批次大小", Min: "1", Max: "100000"},
	{Key: "backup_assets.catalog_build_timeout", EnvVar: "BACKUP_ASSETS_CATALOG_BUILD_TIMEOUT", CodeDefault: "30m", Type: TypeDuration, Category: "backup_assets", Description: "目录构建超时", MinDuration: "1m", MaxDuration: "24h"},
	{Key: "backup_assets.repository_reconcile_interval", EnvVar: "BACKUP_ASSETS_REPOSITORY_RECONCILE_INTERVAL", CodeDefault: "15m", Type: TypeDuration, Category: "backup_assets", Description: "备份仓库对账间隔", MinDuration: "1m", MaxDuration: "24h"},
	{Key: "backup_assets.audit_segment_max_events", EnvVar: "BACKUP_ASSETS_AUDIT_SEGMENT_MAX_EVENTS", CodeDefault: "10000", Type: TypeInt, Category: "backup_assets", Description: "资产审计分段最大事件数", Min: "100", Max: "1000000"},
	{Key: "backup_assets.audit_segment_max_age", EnvVar: "BACKUP_ASSETS_AUDIT_SEGMENT_MAX_AGE", CodeDefault: "24h", Type: TypeDuration, Category: "backup_assets", Description: "资产审计分段最大持续时间", MinDuration: "1h", MaxDuration: "168h"},
	{Key: "backup_assets.audit_detail_retention_days", EnvVar: "BACKUP_ASSETS_AUDIT_DETAIL_RETENTION_DAYS", CodeDefault: "180", Type: TypeInt, Category: "backup_assets", Description: "资产审计明细保留天数", Min: "1", Max: "3650"},
	{Key: "backup_assets.audit_checkpoint_retention_days", EnvVar: "BACKUP_ASSETS_AUDIT_CHECKPOINT_RETENTION_DAYS", CodeDefault: "2555", Type: TypeInt, Category: "backup_assets", Description: "资产审计检查点保留天数", Min: "180", Max: "36500"},
	{Key: "backup_assets.lease_duration", EnvVar: "BACKUP_ASSETS_LEASE_DURATION", CodeDefault: "5m", Type: TypeDuration, Category: "backup_assets", Description: "RecoveryPoint 短租约时长", MinDuration: "30s", MaxDuration: "30m"},
	{Key: "backup_assets.lease_heartbeat", EnvVar: "BACKUP_ASSETS_LEASE_HEARTBEAT", CodeDefault: "60s", Type: TypeDuration, Category: "backup_assets", Description: "RecoveryPoint 租约心跳间隔", MinDuration: "10s", MaxDuration: "5m"},
	{Key: "backup_assets.lease_absolute_deadline", EnvVar: "BACKUP_ASSETS_LEASE_ABSOLUTE_DEADLINE", CodeDefault: "168h", Type: TypeDuration, Category: "backup_assets", Description: "RecoveryPoint 租约绝对截止时间", MinDuration: "5m", MaxDuration: "168h"},
	{Key: "backup_assets.provider_operation_timeout", EnvVar: "BACKUP_ASSETS_PROVIDER_OPERATION_TIMEOUT", CodeDefault: "2m", Type: TypeDuration, Category: "backup_assets", Description: "Provider 只读操作超时", MinDuration: "5s", MaxDuration: "30m"},
	{Key: "backup_assets.provider_max_concurrency", EnvVar: "BACKUP_ASSETS_PROVIDER_MAX_CONCURRENCY", CodeDefault: "4", Type: TypeInt, Category: "backup_assets", Description: "Provider 只读操作最大并发数", Min: "1", Max: "32"},
	{Key: "backup_assets.provider_metadata_limit_bytes", EnvVar: "BACKUP_ASSETS_PROVIDER_METADATA_LIMIT_BYTES", CodeDefault: "16777216", Type: TypeInt, Category: "backup_assets", Description: "Provider 元数据输出字节上限", Min: "65536", Max: "67108864"},
	{Key: "backup_assets.publication_reconcile_interval", EnvVar: "BACKUP_ASSETS_PUBLICATION_RECONCILE_INTERVAL", CodeDefault: "5m", Type: TypeDuration, Category: "backup_assets", Description: "恢复点发布对账间隔", MinDuration: "30s", MaxDuration: "24h"},
	{Key: "backup_assets.publication_reconcile_batch_size", EnvVar: "BACKUP_ASSETS_PUBLICATION_RECONCILE_BATCH_SIZE", CodeDefault: "100", Type: TypeInt, Category: "backup_assets", Description: "恢复点发布对账批次大小", Min: "1", Max: "1000"},
	{Key: "backup_assets.publication_worker_concurrency", EnvVar: "BACKUP_ASSETS_PUBLICATION_WORKER_CONCURRENCY", CodeDefault: "2", Type: TypeInt, Category: "backup_assets", Description: "恢复点发布工作并发数", Min: "1", Max: "32"},
	{Key: "backup_assets.publication_missing_grace", EnvVar: "BACKUP_ASSETS_PUBLICATION_MISSING_GRACE", CodeDefault: "30m", Type: TypeDuration, Category: "backup_assets", Description: "发布快照缺失宽限期", MinDuration: "1m", MaxDuration: "24h"},
	{Key: "backup_assets.publication_stream_max_bytes", EnvVar: "BACKUP_ASSETS_PUBLICATION_STREAM_MAX_BYTES", CodeDefault: "268435456", Type: TypeInt, Category: "backup_assets", Description: "发布备份 JSON 流总字节上限", Min: "1048576", Max: "1073741824"},
	{Key: "backup_assets.manifest_timeout", EnvVar: "BACKUP_ASSETS_MANIFEST_TIMEOUT", CodeDefault: "2h", Type: TypeDuration, Category: "backup_assets", Description: "恢复点清单构建超时", MinDuration: "1m", MaxDuration: "24h"},
	{Key: "backup_assets.manifest_max_bytes", EnvVar: "BACKUP_ASSETS_MANIFEST_MAX_BYTES", CodeDefault: "4294967296", Type: TypeInt, Category: "backup_assets", Description: "恢复点清单总字节上限", Min: "1048576", Max: "17179869184"},
	{Key: "backup_assets.manifest_max_entries", EnvVar: "BACKUP_ASSETS_MANIFEST_MAX_ENTRIES", CodeDefault: "10000000", Type: TypeInt, Category: "backup_assets", Description: "恢复点清单条目上限", Min: "1", Max: "100000000"},
	{Key: "backup_assets.manifest_max_record_bytes", EnvVar: "BACKUP_ASSETS_MANIFEST_MAX_RECORD_BYTES", CodeDefault: "1048576", Type: TypeInt, Category: "backup_assets", Description: "恢复点清单单记录字节上限", Min: "4096", Max: "4194304"},
	{Key: "backup_assets.manifest_max_depth", EnvVar: "BACKUP_ASSETS_MANIFEST_MAX_DEPTH", CodeDefault: "4096", Type: TypeInt, Category: "backup_assets", Description: "恢复点清单目录深度上限", Min: "1", Max: "65536"},
	{Key: "backup_assets.rclone_preflight_ttl", EnvVar: "BACKUP_ASSETS_RCLONE_PREFLIGHT_TTL", CodeDefault: "30m", Type: TypeDuration, Category: "backup_assets", Description: "Rclone 版本化预检有效期", MinDuration: "16m", MaxDuration: "24h"},
	{Key: "backup_assets.rclone_portable_deadline", EnvVar: "BACKUP_ASSETS_RCLONE_PORTABLE_DEADLINE", CodeDefault: "24h", Type: TypeDuration, Category: "backup_assets", Description: "Rclone portable 恢复点绝对时限", MinDuration: "5m", MaxDuration: "168h"},
	{Key: "backup_assets.rclone_native_deadline", EnvVar: "BACKUP_ASSETS_RCLONE_NATIVE_DEADLINE", CodeDefault: "45m", Type: TypeDuration, Category: "backup_assets", Description: "Rclone native 恢复点绝对时限", MinDuration: "5m", MaxDuration: "55m"},
	{Key: "backup_assets.rclone_bound_config_max_bytes", EnvVar: "BACKUP_ASSETS_RCLONE_BOUND_CONFIG_MAX_BYTES", CodeDefault: "65536", Type: TypeInt, Category: "backup_assets", Description: "Rclone 绑定配置最大字节数", Min: "1024", Max: "65536"},
	{Key: "backup_assets.rclone_control_payload_max_bytes", EnvVar: "BACKUP_ASSETS_RCLONE_CONTROL_PAYLOAD_MAX_BYTES", CodeDefault: "8388608", Type: TypeInt, Category: "backup_assets", Description: "Rclone 控制对象暂存最大字节数", Min: "65536", Max: "67108864"},
	{Key: "backup_assets.rclone_full_verify_max_bytes", EnvVar: "BACKUP_ASSETS_RCLONE_FULL_VERIFY_MAX_BYTES", CodeDefault: "1099511627776", Type: TypeInt, Category: "backup_assets", Description: "Rclone 全字节校验最大读取量", Min: "1048576", Max: "17592186044416"},
	{Key: "backup_assets.rclone_manifest_chunk_max_bytes", EnvVar: "BACKUP_ASSETS_RCLONE_MANIFEST_CHUNK_MAX_BYTES", CodeDefault: "8388608", Type: TypeInt, Category: "backup_assets", Description: "Rclone 清单分块最大字节数", Min: "65536", Max: "67108864"},
	{Key: "backup_assets.rclone_low_level_retries", EnvVar: "BACKUP_ASSETS_RCLONE_LOW_LEVEL_RETRIES", CodeDefault: "3", Type: TypeInt, Category: "backup_assets", Description: "Rclone 单次 attempt 低层重试次数", Min: "1", Max: "10"},
	{Key: "backup_assets.rclone_staging_orphan_age", EnvVar: "BACKUP_ASSETS_RCLONE_STAGING_ORPHAN_AGE", CodeDefault: "24h", Type: TypeDuration, Category: "backup_assets", Description: "Rclone 暂存孤儿判定年龄", MinDuration: "1h", MaxDuration: "168h"},
	{Key: "backup_assets.rclone_staging_scan_limit", EnvVar: "BACKUP_ASSETS_RCLONE_STAGING_SCAN_LIMIT", CodeDefault: "256", Type: TypeInt, Category: "backup_assets", Description: "Rclone 暂存孤儿扫描批次", Min: "1", Max: "4096"},
	{Key: "backup_assets.rclone_kms_read_key_max_count", EnvVar: "BACKUP_ASSETS_RCLONE_KMS_READ_KEY_MAX_COUNT", CodeDefault: "8", Type: TypeInt, Category: "backup_assets", Description: "Rclone KMS 保留读取密钥数量上限", Min: "1", Max: "32"},
	{Key: "backup_assets.rclone_health_interval", EnvVar: "BACKUP_ASSETS_RCLONE_HEALTH_INTERVAL", CodeDefault: "15m", Type: TypeDuration, Category: "backup_assets", Description: "Rclone 版本化健康检查间隔", MinDuration: "1m", MaxDuration: "24h"},
	{Key: "backup_assets.rclone_health_batch_size", EnvVar: "BACKUP_ASSETS_RCLONE_HEALTH_BATCH_SIZE", CodeDefault: "100", Type: TypeInt, Category: "backup_assets", Description: "Rclone 版本化健康检查批次", Min: "1", Max: "1000"},
	{Key: "backup_assets.rclone_aws_sdk_max_attempts", EnvVar: "BACKUP_ASSETS_RCLONE_AWS_SDK_MAX_ATTEMPTS", CodeDefault: "3", Type: TypeInt, Category: "backup_assets", Description: "Rclone AWS SDK 最大尝试次数", Min: "1", Max: "10"},
	{Key: "backup_assets.search_reconcile_interval", EnvVar: "BACKUP_ASSETS_SEARCH_RECONCILE_INTERVAL", CodeDefault: "1m", Type: TypeDuration, Category: "backup_assets", Description: "资产搜索索引对账间隔", MinDuration: "10s", MaxDuration: "1h"},
	{Key: "backup_assets.search_build_timeout", EnvVar: "BACKUP_ASSETS_SEARCH_BUILD_TIMEOUT", CodeDefault: "30m", Type: TypeDuration, Category: "backup_assets", Description: "资产搜索索引构建超时", MinDuration: "1m", MaxDuration: "24h"},
	{Key: "backup_assets.search_batch_size", EnvVar: "BACKUP_ASSETS_SEARCH_BATCH_SIZE", CodeDefault: "500", Type: TypeInt, Category: "backup_assets", Description: "资产搜索索引构建批次", Min: "50", Max: "5000"},
	{Key: "backup_assets.search_max_concurrency", EnvVar: "BACKUP_ASSETS_SEARCH_MAX_CONCURRENCY", CodeDefault: "2", Type: TypeInt, Category: "backup_assets", Description: "资产搜索索引最大并发", Min: "1", Max: "16"},
	{Key: "backup_assets.search_ast_max_depth", EnvVar: "BACKUP_ASSETS_SEARCH_AST_MAX_DEPTH", CodeDefault: "8", Type: TypeInt, Category: "backup_assets", Description: "资产搜索 AST 最大深度", Min: "1", Max: "16"},
	{Key: "backup_assets.search_ast_max_nodes", EnvVar: "BACKUP_ASSETS_SEARCH_AST_MAX_NODES", CodeDefault: "64", Type: TypeInt, Category: "backup_assets", Description: "资产搜索 AST 最大节点数", Min: "2", Max: "256"},
	{Key: "backup_assets.search_values_per_node", EnvVar: "BACKUP_ASSETS_SEARCH_VALUES_PER_NODE", CodeDefault: "32", Type: TypeInt, Category: "backup_assets", Description: "资产搜索 AST 单节点最大值数", Min: "1", Max: "64"},
	{Key: "backup_assets.search_body_max_bytes", EnvVar: "BACKUP_ASSETS_SEARCH_BODY_MAX_BYTES", CodeDefault: "65536", Type: TypeInt, Category: "backup_assets", Description: "资产搜索请求体最大字节数", Min: "1024", Max: "65536"},
	{Key: "backup_assets.search_value_max_bytes", EnvVar: "BACKUP_ASSETS_SEARCH_VALUE_MAX_BYTES", CodeDefault: "1024", Type: TypeInt, Category: "backup_assets", Description: "资产搜索单值最大字节数", Min: "1", Max: "4096"},
	{Key: "backup_assets.search_candidate_limit", EnvVar: "BACKUP_ASSETS_SEARCH_CANDIDATE_LIMIT", CodeDefault: "10000", Type: TypeInt, Category: "backup_assets", Description: "资产搜索候选上限", Min: "100", Max: "100000"},
	{Key: "backup_assets.search_query_timeout", EnvVar: "BACKUP_ASSETS_SEARCH_QUERY_TIMEOUT", CodeDefault: "5s", Type: TypeDuration, Category: "backup_assets", Description: "资产搜索查询超时", MinDuration: "100ms", MaxDuration: "30s"},
	{Key: "backup_assets.search_page_size_max", EnvVar: "BACKUP_ASSETS_SEARCH_PAGE_SIZE_MAX", CodeDefault: "200", Type: TypeInt, Category: "backup_assets", Description: "资产搜索单页最大条目数", Min: "1", Max: "500"},
	{Key: "backup_assets.search_suggestion_limit", EnvVar: "BACKUP_ASSETS_SEARCH_SUGGESTION_LIMIT", CodeDefault: "20", Type: TypeInt, Category: "backup_assets", Description: "资产搜索建议上限", Min: "0", Max: "50"},
	{Key: "backup_assets.saved_search_quota", EnvVar: "BACKUP_ASSETS_SAVED_SEARCH_QUOTA", CodeDefault: "100", Type: TypeInt, Category: "backup_assets", Description: "每用户保存搜索配额", Min: "1", Max: "1000"},
	{Key: "backup_assets.favorite_quota", EnvVar: "BACKUP_ASSETS_FAVORITE_QUOTA", CodeDefault: "5000", Type: TypeInt, Category: "backup_assets", Description: "每用户资产收藏配额", Min: "1", Max: "100000"},
	{Key: "backup_assets.tag_definition_quota", EnvVar: "BACKUP_ASSETS_TAG_DEFINITION_QUOTA", CodeDefault: "100", Type: TypeInt, Category: "backup_assets", Description: "每用户资产标签定义配额", Min: "1", Max: "1000"},
	{Key: "backup_assets.tag_assignment_quota", EnvVar: "BACKUP_ASSETS_TAG_ASSIGNMENT_QUOTA", CodeDefault: "10000", Type: TypeInt, Category: "backup_assets", Description: "每用户资产标签绑定配额", Min: "1", Max: "200000"},
	{Key: "backup_assets.overlay_bulk_max_items", EnvVar: "BACKUP_ASSETS_OVERLAY_BULK_MAX_ITEMS", CodeDefault: "200", Type: TypeInt, Category: "backup_assets", Description: "资产用户覆盖批量操作上限", Min: "1", Max: "1000"},
	{Key: "backup_assets.overlay_label_max_bytes", EnvVar: "BACKUP_ASSETS_OVERLAY_LABEL_MAX_BYTES", CodeDefault: "256", Type: TypeInt, Category: "backup_assets", Description: "资产用户标签最大字节数", Min: "1", Max: "4096"},
	{Key: "backup_assets.recent_quota", EnvVar: "BACKUP_ASSETS_RECENT_QUOTA", CodeDefault: "10000", Type: TypeInt, Category: "backup_assets", Description: "每用户最近访问资产配额", Min: "1", Max: "100000"},
	{Key: "backup_assets.recent_retention", EnvVar: "BACKUP_ASSETS_RECENT_RETENTION", CodeDefault: "720h", Type: TypeDuration, Category: "backup_assets", Description: "最近访问资产保留时长", MinDuration: "24h", MaxDuration: "8760h"},
	{Key: "backup_assets.recent_writes_per_minute", EnvVar: "BACKUP_ASSETS_RECENT_WRITES_PER_MINUTE", CodeDefault: "120", Type: TypeInt, Category: "backup_assets", Description: "每用户最近访问每分钟写入上限", Min: "1", Max: "10000"},
	{Key: "backup_assets.idempotency_ttl", EnvVar: "BACKUP_ASSETS_IDEMPOTENCY_TTL", CodeDefault: "24h", Type: TypeDuration, Category: "backup_assets", Description: "资产用户覆盖幂等回执保留时长", MinDuration: "1h", MaxDuration: "168h"},
	{Key: "backup_assets.idempotency_key_max_bytes", EnvVar: "BACKUP_ASSETS_IDEMPOTENCY_KEY_MAX_BYTES", CodeDefault: "128", Type: TypeInt, Category: "backup_assets", Description: "资产用户覆盖幂等键最大字节数", Min: "32", Max: "256"},
	{Key: "backup_assets.processing_queue_max", EnvVar: "BACKUP_ASSETS_PROCESSING_QUEUE_MAX", CodeDefault: "10000", Type: TypeInt, Category: "backup_assets", Description: "资产处理持久队列上限", Min: "1", Max: "100000"},
	{Key: "backup_assets.processing_interactive_slots", EnvVar: "BACKUP_ASSETS_PROCESSING_INTERACTIVE_SLOTS", CodeDefault: "2", Type: TypeInt, Category: "backup_assets", Description: "资产处理交互保留槽位", Min: "1", Max: "64"},
	{Key: "backup_assets.processing_background_slots", EnvVar: "BACKUP_ASSETS_PROCESSING_BACKGROUND_SLOTS", CodeDefault: "2", Type: TypeInt, Category: "backup_assets", Description: "资产处理后台槽位", Min: "1", Max: "64"},
	{Key: "backup_assets.processing_pull_lease", EnvVar: "BACKUP_ASSETS_PROCESSING_PULL_LEASE", CodeDefault: "90s", Type: TypeDuration, Category: "backup_assets", Description: "资产 Worker 拉取租约时长", MinDuration: "15s", MaxDuration: "5m"},
	{Key: "backup_assets.processing_pull_heartbeat", EnvVar: "BACKUP_ASSETS_PROCESSING_PULL_HEARTBEAT", CodeDefault: "20s", Type: TypeDuration, Category: "backup_assets", Description: "资产 Worker 拉取租约心跳", MinDuration: "5s", MaxDuration: "1m"},
	{Key: "backup_assets.processing_attempt_timeout", EnvVar: "BACKUP_ASSETS_PROCESSING_ATTEMPT_TIMEOUT", CodeDefault: "2h", Type: TypeDuration, Category: "backup_assets", Description: "资产处理 attempt 绝对超时", MinDuration: "1m", MaxDuration: "24h"},
	{Key: "backup_assets.processing_retry_max", EnvVar: "BACKUP_ASSETS_PROCESSING_RETRY_MAX", CodeDefault: "5", Type: TypeInt, Category: "backup_assets", Description: "资产处理最大重试次数", Min: "0", Max: "20"},
	{Key: "backup_assets.processing_retry_base", EnvVar: "BACKUP_ASSETS_PROCESSING_RETRY_BASE", CodeDefault: "5s", Type: TypeDuration, Category: "backup_assets", Description: "资产处理重试基础延迟", MinDuration: "1s", MaxDuration: "5m"},
	{Key: "backup_assets.processing_retry_max_delay", EnvVar: "BACKUP_ASSETS_PROCESSING_RETRY_MAX_DELAY", CodeDefault: "15m", Type: TypeDuration, Category: "backup_assets", Description: "资产处理重试最大延迟", MinDuration: "1s", MaxDuration: "2h"},
	{Key: "backup_assets.processing_input_request_max_bytes", EnvVar: "BACKUP_ASSETS_PROCESSING_INPUT_REQUEST_MAX_BYTES", CodeDefault: "67108864", Type: TypeInt, Category: "backup_assets", Description: "Worker Input 单次读取字节上限", Min: "65536", Max: "1073741824"},
	{Key: "backup_assets.processing_input_cumulative_max_bytes", EnvVar: "BACKUP_ASSETS_PROCESSING_INPUT_CUMULATIVE_MAX_BYTES", CodeDefault: "2147483648", Type: TypeInt, Category: "backup_assets", Description: "Worker Input attempt 累计读取字节上限", Min: "65536", Max: "17179869184"},
	{Key: "backup_assets.processing_input_max_requests", EnvVar: "BACKUP_ASSETS_PROCESSING_INPUT_MAX_REQUESTS", CodeDefault: "512", Type: TypeInt, Category: "backup_assets", Description: "Worker Input attempt 请求上限", Min: "1", Max: "4096"},
	{Key: "backup_assets.processing_input_max_in_flight", EnvVar: "BACKUP_ASSETS_PROCESSING_INPUT_MAX_IN_FLIGHT", CodeDefault: "4", Type: TypeInt, Category: "backup_assets", Description: "Worker Input attempt 并发请求上限", Min: "1", Max: "32"},
	{Key: "backup_assets.processing_sink_max_artifacts", EnvVar: "BACKUP_ASSETS_PROCESSING_SINK_MAX_ARTIFACTS", CodeDefault: "32", Type: TypeInt, Category: "backup_assets", Description: "Worker Sink 原子产物数量上限", Min: "1", Max: "256"},
	{Key: "backup_assets.processing_sink_artifact_max_bytes", EnvVar: "BACKUP_ASSETS_PROCESSING_SINK_ARTIFACT_MAX_BYTES", CodeDefault: "536870912", Type: TypeInt, Category: "backup_assets", Description: "Worker Sink 单产物字节上限", Min: "65536", Max: "4294967296"},
	{Key: "backup_assets.processing_sink_total_max_bytes", EnvVar: "BACKUP_ASSETS_PROCESSING_SINK_TOTAL_MAX_BYTES", CodeDefault: "1073741824", Type: TypeInt, Category: "backup_assets", Description: "Worker Sink 原子产物集总字节上限", Min: "65536", Max: "17179869184"},
	{Key: "backup_assets.processing_protocol_json_max_bytes", EnvVar: "BACKUP_ASSETS_PROCESSING_PROTOCOL_JSON_MAX_BYTES", CodeDefault: "65536", Type: TypeInt, Category: "backup_assets", Description: "Worker 协议 JSON 请求体上限", Min: "4096", Max: "1048576"},
	{Key: "backup_assets.processing_secret_classify", EnvVar: "BACKUP_ASSETS_PROCESSING_SECRET_CLASSIFY", CodeDefault: "false", Type: TypeBool, Category: "backup_assets", Description: "启用有限秘密分类增强"},
	{Key: "backup_assets.processing_backfill_paused", EnvVar: "BACKUP_ASSETS_PROCESSING_BACKFILL_PAUSED", CodeDefault: "true", Type: TypeBool, Category: "backup_assets", Description: "暂停资产处理后台回填"},
	{Key: "backup_assets.processing_backfill_batch_size", EnvVar: "BACKUP_ASSETS_PROCESSING_BACKFILL_BATCH_SIZE", CodeDefault: "100", Type: TypeInt, Category: "backup_assets", Description: "资产处理回填批次大小", Min: "1", Max: "10000"},
	{Key: "backup_assets.processing_backfill_jobs_per_hour", EnvVar: "BACKUP_ASSETS_PROCESSING_BACKFILL_JOBS_PER_HOUR", CodeDefault: "1000", Type: TypeInt, Category: "backup_assets", Description: "资产处理回填每小时任务上限", Min: "1", Max: "100000"},
	{Key: "backup_assets.processing_backfill_bytes_per_hour", EnvVar: "BACKUP_ASSETS_PROCESSING_BACKFILL_BYTES_PER_HOUR", CodeDefault: "10737418240", Type: TypeInt, Category: "backup_assets", Description: "资产处理回填每小时字节上限", Min: "65536", Max: "1099511627776"},
	{Key: "backup_assets.processing_backfill_provider_concurrency", EnvVar: "BACKUP_ASSETS_PROCESSING_BACKFILL_PROVIDER_CONCURRENCY", CodeDefault: "1", Type: TypeInt, Category: "backup_assets", Description: "资产处理回填单 Provider 并发上限", Min: "1", Max: "32"},
	{Key: "backup_assets.processing_backfill_capability_concurrency", EnvVar: "BACKUP_ASSETS_PROCESSING_BACKFILL_CAPABILITY_CONCURRENCY", CodeDefault: "1", Type: TypeInt, Category: "backup_assets", Description: "资产处理回填单能力并发上限", Min: "1", Max: "32"},
	{Key: "backup_assets.processing_backfill_recent_window", EnvVar: "BACKUP_ASSETS_PROCESSING_BACKFILL_RECENT_WINDOW", CodeDefault: "720h", Type: TypeDuration, Category: "backup_assets", Description: "资产处理近期回填窗口", MinDuration: "24h", MaxDuration: "8760h"},
	{Key: "backup_assets.processing_backfill_history_aging_step", EnvVar: "BACKUP_ASSETS_PROCESSING_BACKFILL_HISTORY_AGING_STEP", CodeDefault: "24h", Type: TypeDuration, Category: "backup_assets", Description: "资产处理历史回填老化步长", MinDuration: "1h", MaxDuration: "720h"},
	{Key: "backup_assets.worker_local_enabled", EnvVar: "BACKUP_ASSETS_WORKER_LOCAL_ENABLED", CodeDefault: "false", Type: TypeBool, Category: "backup_assets", Description: "启用本机资产 Worker 传输", RequiresRestart: true},
	{Key: "backup_assets.worker_local_socket", EnvVar: "BACKUP_ASSETS_WORKER_LOCAL_SOCKET", CodeDefault: "/run/xirang/asset-worker.sock", Type: TypeString, Category: "backup_assets", Description: "本机资产 Worker Unix socket", RequiresRestart: true},
	{Key: "backup_assets.worker_remote_enabled", EnvVar: "BACKUP_ASSETS_WORKER_REMOTE_ENABLED", CodeDefault: "false", Type: TypeBool, Category: "backup_assets", Description: "启用远程资产 Worker mTLS 传输", RequiresRestart: true},
	{Key: "backup_assets.worker_remote_listen_addr", EnvVar: "BACKUP_ASSETS_WORKER_REMOTE_LISTEN_ADDR", CodeDefault: "", Type: TypeString, Category: "backup_assets", Description: "远程资产 Worker 专用监听地址", RequiresRestart: true},
	{Key: "backup_assets.worker_remote_server_cert_file", EnvVar: "BACKUP_ASSETS_WORKER_REMOTE_SERVER_CERT_FILE", CodeDefault: "", Type: TypeString, Category: "backup_assets", Description: "远程资产 Worker 服务端证书路径", RequiresRestart: true, Sensitive: true},
	{Key: "backup_assets.worker_remote_server_key_file", EnvVar: "BACKUP_ASSETS_WORKER_REMOTE_SERVER_KEY_FILE", CodeDefault: "", Type: TypeString, Category: "backup_assets", Description: "远程资产 Worker 服务端私钥路径", RequiresRestart: true, Sensitive: true},
	{Key: "backup_assets.worker_remote_client_ca_file", EnvVar: "BACKUP_ASSETS_WORKER_REMOTE_CLIENT_CA_FILE", CodeDefault: "", Type: TypeString, Category: "backup_assets", Description: "远程资产 Worker 客户端 CA 路径", RequiresRestart: true, Sensitive: true},
	{Key: "backup_assets.worker_remote_trust_domain", EnvVar: "BACKUP_ASSETS_WORKER_REMOTE_TRUST_DOMAIN", CodeDefault: "", Type: TypeString, Category: "backup_assets", Description: "远程资产 Worker SPIFFE 信任域", RequiresRestart: true, Sensitive: true},
	{Key: "backup_assets.worker_updater_enabled", EnvVar: "BACKUP_ASSETS_WORKER_UPDATER_ENABLED", CodeDefault: "false", Type: TypeBool, Category: "backup_assets", Description: "启用独立资产 Worker updater", RequiresRestart: true},
	{Key: "backup_assets.worker_updater_online_enabled", EnvVar: "BACKUP_ASSETS_WORKER_UPDATER_ONLINE_ENABLED", CodeDefault: "false", Type: TypeBool, Category: "backup_assets", Description: "启用 updater 受限在线模式", RequiresRestart: true},
	{Key: "backup_assets.worker_updater_online_origins", EnvVar: "BACKUP_ASSETS_WORKER_UPDATER_ONLINE_ORIGINS", CodeDefault: "", Type: TypeString, Category: "backup_assets", Description: "updater 精确 HTTPS origin allowlist", RequiresRestart: true},
	{Key: "backup_assets.derived_store_root", EnvVar: "BACKUP_ASSETS_DERIVED_STORE_ROOT", CodeDefault: "/var/lib/xirang-asset-runtime/derived", Type: TypeString, Category: "backup_assets", Description: "加密派生资产专用根目录", RequiresRestart: true},
	{Key: "backup_assets.derived_store_chunk_bytes", EnvVar: "BACKUP_ASSETS_DERIVED_STORE_CHUNK_BYTES", CodeDefault: "1048576", Type: TypeInt, Category: "backup_assets", Description: "派生资产认证加密分块字节数", Min: "65536", Max: "8388608", RequiresRestart: true},
	{Key: "backup_assets.derived_store_blob_max_bytes", EnvVar: "BACKUP_ASSETS_DERIVED_STORE_BLOB_MAX_BYTES", CodeDefault: "4294967296", Type: TypeInt, Category: "backup_assets", Description: "派生资产单 blob 字节上限", Min: "65536", Max: "17179869184"},
	{Key: "backup_assets.derived_store_global_max_bytes", EnvVar: "BACKUP_ASSETS_DERIVED_STORE_GLOBAL_MAX_BYTES", CodeDefault: "107374182400", Type: TypeInt, Category: "backup_assets", Description: "派生资产全局字节配额", Min: "65536", Max: "1099511627776"},
	{Key: "backup_assets.derived_store_reconcile_interval", EnvVar: "BACKUP_ASSETS_DERIVED_STORE_RECONCILE_INTERVAL", CodeDefault: "15m", Type: TypeDuration, Category: "backup_assets", Description: "派生资产对账间隔", MinDuration: "1m", MaxDuration: "24h"},
	{Key: "backup_assets.derived_store_reconcile_batch_size", EnvVar: "BACKUP_ASSETS_DERIVED_STORE_RECONCILE_BATCH_SIZE", CodeDefault: "256", Type: TypeInt, Category: "backup_assets", Description: "派生资产对账批次", Min: "1", Max: "10000"},
	{Key: "backup_assets.recovery.receipt_replay_ttl", EnvVar: "BACKUP_ASSETS_RECOVERY_RECEIPT_REPLAY_TTL", CodeDefault: "20m", Type: TypeDuration, Category: "backup_assets", Description: "恢复授权回执回放与保留有效期", MinDuration: "5m", MaxDuration: "24h"},
	{Key: "backup_assets.recovery.write_grant_ttl", EnvVar: "BACKUP_ASSETS_RECOVERY_WRITE_GRANT_TTL", CodeDefault: "15m", Type: TypeDuration, Category: "backup_assets", Description: "恢复写入授权有效期", MinDuration: "1m", MaxDuration: "24h"},
	{Key: "backup_assets.recovery.delete_grant_ttl", EnvVar: "BACKUP_ASSETS_RECOVERY_DELETE_GRANT_TTL", CodeDefault: "10m", Type: TypeDuration, Category: "backup_assets", Description: "恢复精确镜像删除授权有效期", MinDuration: "1m", MaxDuration: "24h"},
	{Key: "backup_assets.recovery.receipt_reaper_cadence", EnvVar: "BACKUP_ASSETS_RECOVERY_RECEIPT_REAPER_CADENCE", CodeDefault: "1m", Type: TypeDuration, Category: "backup_assets", Description: "恢复授权回执清理周期", MinDuration: "10s", MaxDuration: "1h"},
	{Key: "backup_assets.recovery.receipt_reaper_batch_size", EnvVar: "BACKUP_ASSETS_RECOVERY_RECEIPT_REAPER_BATCH_SIZE", CodeDefault: "100", Type: TypeInt, Category: "backup_assets", Description: "恢复授权回执单次清理批次", Min: "1", Max: "1000"},
	{Key: "backup_assets.recovery.enabled", EnvVar: "BACKUP_ASSETS_RECOVERY_ENABLED", CodeDefault: "false", Type: TypeBool, Category: "backup_assets", Description: "启用受控恢复"},
	{Key: "backup_assets.recovery.preflight_ttl", EnvVar: "BACKUP_ASSETS_RECOVERY_PREFLIGHT_TTL", CodeDefault: "10m", Type: TypeDuration, Category: "backup_assets", Description: "受控恢复预检有效期", MinDuration: "1m", MaxDuration: "1h"},
	{Key: "backup_assets.recovery.max_selection_items", EnvVar: "BACKUP_ASSETS_RECOVERY_MAX_SELECTION_ITEMS", CodeDefault: "10000", Type: TypeInt, Category: "backup_assets", Description: "单次受控恢复条目上限", Min: "1", Max: "100000"},
	{Key: "backup_assets.recovery.max_logical_bytes", EnvVar: "BACKUP_ASSETS_RECOVERY_MAX_LOGICAL_BYTES", CodeDefault: "10737418240", Type: TypeInt, Category: "backup_assets", Description: "单次受控恢复逻辑字节上限", Min: "65536", Max: "1099511627776"},
	{Key: "backup_assets.recovery.worker_concurrency", EnvVar: "BACKUP_ASSETS_RECOVERY_WORKER_CONCURRENCY", CodeDefault: "2", Type: TypeInt, Category: "backup_assets", Description: "受控恢复 Worker 并发上限", Min: "1", Max: "16"},
	{Key: "backup_assets.recovery.lease_ttl", EnvVar: "BACKUP_ASSETS_RECOVERY_LEASE_TTL", CodeDefault: "90s", Type: TypeDuration, Category: "backup_assets", Description: "受控恢复尝试租约时长", MinDuration: "30s", MaxDuration: "10m"},
	{Key: "backup_assets.recovery.lease_renew_margin", EnvVar: "BACKUP_ASSETS_RECOVERY_LEASE_RENEW_MARGIN", CodeDefault: "20s", Type: TypeDuration, Category: "backup_assets", Description: "受控恢复租约续期余量", MinDuration: "5s", MaxDuration: "5m"},
	{Key: "backup_assets.recovery.takeover_cadence", EnvVar: "BACKUP_ASSETS_RECOVERY_TAKEOVER_CADENCE", CodeDefault: "15s", Type: TypeDuration, Category: "backup_assets", Description: "受控恢复过期尝试接管周期", MinDuration: "1s", MaxDuration: "5m"},
	{Key: "backup_assets.recovery.retry_base", EnvVar: "BACKUP_ASSETS_RECOVERY_RETRY_BASE", CodeDefault: "5s", Type: TypeDuration, Category: "backup_assets", Description: "受控恢复重试基础延迟", MinDuration: "1s", MaxDuration: "5m"},
	{Key: "backup_assets.recovery.retry_max_delay", EnvVar: "BACKUP_ASSETS_RECOVERY_RETRY_MAX_DELAY", CodeDefault: "5m", Type: TypeDuration, Category: "backup_assets", Description: "受控恢复重试最大延迟", MinDuration: "1s", MaxDuration: "1h"},
	{Key: "backup_assets.recovery.scan_limit", EnvVar: "BACKUP_ASSETS_RECOVERY_SCAN_LIMIT", CodeDefault: "100", Type: TypeInt, Category: "backup_assets", Description: "受控恢复持久调度扫描上限", Min: "1", Max: "1000"},
	{Key: "backup_assets.recovery.execution_timeout", EnvVar: "BACKUP_ASSETS_RECOVERY_EXECUTION_TIMEOUT", CodeDefault: "2h", Type: TypeDuration, Category: "backup_assets", Description: "受控恢复执行绝对时限", MinDuration: "5m", MaxDuration: "24h"},
	{Key: "backup_assets.recovery.result_default_ttl", EnvVar: "BACKUP_ASSETS_RECOVERY_RESULT_DEFAULT_TTL", CodeDefault: "1h", Type: TypeDuration, Category: "backup_assets", Description: "恢复结果默认明文有效期", MinDuration: "5m", MaxDuration: "24h"},
	{Key: "backup_assets.recovery.result_retain_hard_cap", EnvVar: "BACKUP_ASSETS_RECOVERY_RESULT_RETAIN_HARD_CAP", CodeDefault: "24h", Type: TypeDuration, Category: "backup_assets", Description: "恢复结果保留硬上限", MinDuration: "5m", MaxDuration: "720h"},
	{Key: "backup_assets.recovery.result_read_permit_ttl", EnvVar: "BACKUP_ASSETS_RECOVERY_RESULT_READ_PERMIT_TTL", CodeDefault: "2m", Type: TypeDuration, Category: "backup_assets", Description: "恢复结果读取许可有效期", MinDuration: "10s", MaxDuration: "10m"},
	{Key: "backup_assets.recovery.result_drain_timeout", EnvVar: "BACKUP_ASSETS_RECOVERY_RESULT_DRAIN_TIMEOUT", CodeDefault: "30s", Type: TypeDuration, Category: "backup_assets", Description: "恢复结果读取排空时限", MinDuration: "1s", MaxDuration: "5m"},
	{Key: "backup_assets.recovery.cleanup_cadence", EnvVar: "BACKUP_ASSETS_RECOVERY_CLEANUP_CADENCE", CodeDefault: "1m", Type: TypeDuration, Category: "backup_assets", Description: "恢复结果清理周期", MinDuration: "10s", MaxDuration: "1h"},
	{Key: "backup_assets.recovery.cleanup_batch_size", EnvVar: "BACKUP_ASSETS_RECOVERY_CLEANUP_BATCH_SIZE", CodeDefault: "100", Type: TypeInt, Category: "backup_assets", Description: "恢复结果清理批次", Min: "1", Max: "1000"},
	{Key: "backup_assets.recovery.cleanup_lease_ttl", EnvVar: "BACKUP_ASSETS_RECOVERY_CLEANUP_LEASE_TTL", CodeDefault: "2m", Type: TypeDuration, Category: "backup_assets", Description: "恢复结果清理租约时长", MinDuration: "30s", MaxDuration: "30m"},
	{Key: "backup_assets.recovery.cleanup_retry_base", EnvVar: "BACKUP_ASSETS_RECOVERY_CLEANUP_RETRY_BASE", CodeDefault: "10s", Type: TypeDuration, Category: "backup_assets", Description: "恢复结果清理重试基础延迟", MinDuration: "1s", MaxDuration: "10m"},
	{Key: "backup_assets.recovery.cleanup_retry_max_delay", EnvVar: "BACKUP_ASSETS_RECOVERY_CLEANUP_RETRY_MAX_DELAY", CodeDefault: "10m", Type: TypeDuration, Category: "backup_assets", Description: "恢复结果清理重试最大延迟", MinDuration: "1s", MaxDuration: "2h"},
	{Key: "backup_assets.recovery.reconciliation_finding_limit", EnvVar: "BACKUP_ASSETS_RECOVERY_RECONCILIATION_FINDING_LIMIT", CodeDefault: "100", Type: TypeInt, Category: "backup_assets", Description: "恢复对账单次 finding 上限", Min: "1", Max: "256"},
	{Key: "backup_assets.export.enabled", EnvVar: "BACKUP_ASSETS_EXPORT_ENABLED", CodeDefault: "false", Type: TypeBool, Category: "backup_assets", Description: "启用备份资产导出"},
	{Key: "backup_assets.export.root", EnvVar: "BACKUP_ASSETS_EXPORT_ROOT", CodeDefault: "/var/lib/xirang-asset-runtime/export", Type: TypeString, Category: "backup_assets", Description: "备份资产导出密文专用根目录", RequiresRestart: true},
	{Key: "backup_assets.export.default_profile", EnvVar: "BACKUP_ASSETS_EXPORT_DEFAULT_PROFILE", CodeDefault: "zip_deflate_v1", Type: TypeString, Category: "backup_assets", Description: "备份资产导出默认归档配置"},
	{Key: "backup_assets.export.chunk_bytes", EnvVar: "BACKUP_ASSETS_EXPORT_CHUNK_BYTES", CodeDefault: "1048576", Type: TypeInt, Category: "backup_assets", Description: "备份资产导出认证加密分块字节数", Min: "65536", Max: "8388608"},
	{Key: "backup_assets.export.max_items", EnvVar: "BACKUP_ASSETS_EXPORT_MAX_ITEMS", CodeDefault: "10000", Type: TypeInt, Category: "backup_assets", Description: "单次备份资产导出条目上限", Min: "1", Max: "100000"},
	{Key: "backup_assets.export.max_source_points", EnvVar: "BACKUP_ASSETS_EXPORT_MAX_SOURCE_POINTS", CodeDefault: "128", Type: TypeInt, Category: "backup_assets", Description: "单次备份资产导出恢复点上限", Min: "1", Max: "1024"},
	{Key: "backup_assets.export.max_item_bytes", EnvVar: "BACKUP_ASSETS_EXPORT_MAX_ITEM_BYTES", CodeDefault: "2147483648", Type: TypeInt, Category: "backup_assets", Description: "单条备份资产导出逻辑字节上限", Min: "65536", Max: "274877906944"},
	{Key: "backup_assets.export.max_logical_bytes", EnvVar: "BACKUP_ASSETS_EXPORT_MAX_LOGICAL_BYTES", CodeDefault: "10737418240", Type: TypeInt, Category: "backup_assets", Description: "单次备份资产导出逻辑字节上限", Min: "65536", Max: "1099511627776"},
	{Key: "backup_assets.export.max_provider_bytes", EnvVar: "BACKUP_ASSETS_EXPORT_MAX_PROVIDER_BYTES", CodeDefault: "21474836480", Type: TypeInt, Category: "backup_assets", Description: "单次备份资产导出 Provider 读取字节上限", Min: "65536", Max: "2199023255552"},
	{Key: "backup_assets.export.max_ciphertext_bytes", EnvVar: "BACKUP_ASSETS_EXPORT_MAX_CIPHERTEXT_BYTES", CodeDefault: "12884901888", Type: TypeInt, Category: "backup_assets", Description: "单次备份资产导出密文字节上限", Min: "65536", Max: "1374389534720"},
	{Key: "backup_assets.export.user_active_jobs", EnvVar: "BACKUP_ASSETS_EXPORT_USER_ACTIVE_JOBS", CodeDefault: "2", Type: TypeInt, Category: "backup_assets", Description: "单用户备份资产导出活动作业上限", Min: "1", Max: "16"},
	{Key: "backup_assets.export.global_active_jobs", EnvVar: "BACKUP_ASSETS_EXPORT_GLOBAL_ACTIVE_JOBS", CodeDefault: "8", Type: TypeInt, Category: "backup_assets", Description: "全局备份资产导出活动作业上限", Min: "1", Max: "64"},
	{Key: "backup_assets.export.worker_concurrency", EnvVar: "BACKUP_ASSETS_EXPORT_WORKER_CONCURRENCY", CodeDefault: "2", Type: TypeInt, Category: "backup_assets", Description: "备份资产导出 Worker 并发上限", Min: "1", Max: "16"},
	{Key: "backup_assets.export.max_open_readers", EnvVar: "BACKUP_ASSETS_EXPORT_MAX_OPEN_READERS", CodeDefault: "2", Type: TypeInt, Category: "backup_assets", Description: "单次备份资产导出读取器上限", Min: "1", Max: "8"},
	{Key: "backup_assets.export.max_duration", EnvVar: "BACKUP_ASSETS_EXPORT_MAX_DURATION", CodeDefault: "2h", Type: TypeDuration, Category: "backup_assets", Description: "单次备份资产导出绝对执行时长", MinDuration: "5m", MaxDuration: "24h"},
	{Key: "backup_assets.export.max_attempts", EnvVar: "BACKUP_ASSETS_EXPORT_MAX_ATTEMPTS", CodeDefault: "3", Type: TypeInt, Category: "backup_assets", Description: "单次备份资产导出最大尝试次数", Min: "1", Max: "10"},
	{Key: "backup_assets.export.retry_base", EnvVar: "BACKUP_ASSETS_EXPORT_RETRY_BASE", CodeDefault: "5s", Type: TypeDuration, Category: "backup_assets", Description: "备份资产导出重试基础延迟", MinDuration: "1s", MaxDuration: "1m"},
	{Key: "backup_assets.export.retry_max_delay", EnvVar: "BACKUP_ASSETS_EXPORT_RETRY_MAX_DELAY", CodeDefault: "5m", Type: TypeDuration, Category: "backup_assets", Description: "备份资产导出重试最大延迟", MinDuration: "5s", MaxDuration: "30m"},
	{Key: "backup_assets.export.lease_ttl", EnvVar: "BACKUP_ASSETS_EXPORT_LEASE_TTL", CodeDefault: "90s", Type: TypeDuration, Category: "backup_assets", Description: "备份资产导出内部租约时长", MinDuration: "30s", MaxDuration: "5m"},
	{Key: "backup_assets.export.lease_renew_margin", EnvVar: "BACKUP_ASSETS_EXPORT_LEASE_RENEW_MARGIN", CodeDefault: "20s", Type: TypeDuration, Category: "backup_assets", Description: "备份资产导出租约续期安全余量", MinDuration: "5s", MaxDuration: "2m"},
	{Key: "backup_assets.export.ready_ttl", EnvVar: "BACKUP_ASSETS_EXPORT_READY_TTL", CodeDefault: "24h", Type: TypeDuration, Category: "backup_assets", Description: "备份资产导出就绪产物绝对有效期", MinDuration: "15m", MaxDuration: "168h"},
	{Key: "backup_assets.export.summary_ttl", EnvVar: "BACKUP_ASSETS_EXPORT_SUMMARY_TTL", CodeDefault: "2160h", Type: TypeDuration, Category: "backup_assets", Description: "备份资产导出终态摘要保留期", MinDuration: "24h", MaxDuration: "8760h"},
	{Key: "backup_assets.export.ticket_ttl", EnvVar: "BACKUP_ASSETS_EXPORT_TICKET_TTL", CodeDefault: "5m", Type: TypeDuration, Category: "backup_assets", Description: "备份资产导出下载票据有效期", MinDuration: "30s", MaxDuration: "15m"},
	{Key: "backup_assets.export.ticket_max_requests", EnvVar: "BACKUP_ASSETS_EXPORT_TICKET_MAX_REQUESTS", CodeDefault: "256", Type: TypeInt, Category: "backup_assets", Description: "备份资产导出下载票据请求上限", Min: "1", Max: "4096"},
	{Key: "backup_assets.export.ticket_max_in_flight", EnvVar: "BACKUP_ASSETS_EXPORT_TICKET_MAX_IN_FLIGHT", CodeDefault: "2", Type: TypeInt, Category: "backup_assets", Description: "备份资产导出下载票据并发上限", Min: "1", Max: "8"},
	{Key: "backup_assets.export.ticket_max_cumulative_bytes", EnvVar: "BACKUP_ASSETS_EXPORT_TICKET_MAX_CUMULATIVE_BYTES", CodeDefault: "25769803776", Type: TypeInt, Category: "backup_assets", Description: "备份资产导出下载票据累计字节上限", Min: "65536", Max: "2748779069440"},
	{Key: "backup_assets.export.user_store_quota", EnvVar: "BACKUP_ASSETS_EXPORT_USER_STORE_QUOTA", CodeDefault: "26843545600", Type: TypeInt, Category: "backup_assets", Description: "单用户备份资产导出密文存储配额", Min: "1073741824", Max: "2199023255552"},
	{Key: "backup_assets.export.store_quota", EnvVar: "BACKUP_ASSETS_EXPORT_STORE_QUOTA", CodeDefault: "107374182400", Type: TypeInt, Category: "backup_assets", Description: "全局备份资产导出密文存储配额", Min: "1073741824", Max: "10995116277760"},
	{Key: "backup_assets.export.gc_cadence", EnvVar: "BACKUP_ASSETS_EXPORT_GC_CADENCE", CodeDefault: "5m", Type: TypeDuration, Category: "backup_assets", Description: "备份资产导出清理周期", MinDuration: "30s", MaxDuration: "1h"},
	{Key: "backup_assets.export.reconcile_batch_size", EnvVar: "BACKUP_ASSETS_EXPORT_RECONCILE_BATCH_SIZE", CodeDefault: "100", Type: TypeInt, Category: "backup_assets", Description: "备份资产导出对账批次", Min: "1", Max: "1000"},
	{Key: "backup_assets.archive.member_ttl", EnvVar: "BACKUP_ASSETS_ARCHIVE_MEMBER_TTL", CodeDefault: "1h", Type: TypeDuration, Category: "backup_assets", Description: "归档 member 临时产物有效期", MinDuration: "5m", MaxDuration: "24h"},
	{Key: "backup_assets.archive.max_expanded_bytes", EnvVar: "BACKUP_ASSETS_ARCHIVE_MAX_EXPANDED_BYTES", CodeDefault: "8589934592", Type: TypeInt, Category: "backup_assets", Description: "归档展开总字节上限", Min: "1048576", Max: "8589934592"},
	{Key: "backup_assets.archive.member_max_bytes", EnvVar: "BACKUP_ASSETS_ARCHIVE_MEMBER_MAX_BYTES", CodeDefault: "268435456", Type: TypeInt, Category: "backup_assets", Description: "归档单 member 字节上限", Min: "65536", Max: "268435456"},
	{Key: "backup_assets.archive.max_entries", EnvVar: "BACKUP_ASSETS_ARCHIVE_MAX_ENTRIES", CodeDefault: "100000", Type: TypeInt, Category: "backup_assets", Description: "归档条目上限", Min: "1", Max: "100000"},
	{Key: "backup_assets.archive.max_depth", EnvVar: "BACKUP_ASSETS_ARCHIVE_MAX_DEPTH", CodeDefault: "16", Type: TypeInt, Category: "backup_assets", Description: "归档目录深度上限", Min: "1", Max: "16"},
	{Key: "backup_assets.archive.max_compression_ratio", EnvVar: "BACKUP_ASSETS_ARCHIVE_MAX_COMPRESSION_RATIO", CodeDefault: "100", Type: TypeInt, Category: "backup_assets", Description: "归档压缩比上限", Min: "1", Max: "100"},
	{Key: "backup_assets.archive.max_duration", EnvVar: "BACKUP_ASSETS_ARCHIVE_MAX_DURATION", CodeDefault: "10m", Type: TypeDuration, Category: "backup_assets", Description: "归档处理绝对时长", MinDuration: "1s", MaxDuration: "10m"},
}

// registryMap O(1) key 查找（init 时构建）
var registryMap map[string]*SettingDef

func init() {
	if err := validateRegistryDefinitions(registry); err != nil {
		panic(err)
	}
	registryMap = make(map[string]*SettingDef, len(registry))
	for i := range registry {
		def := &registry[i]
		registryMap[def.Key] = def
	}
}

func validateRegistryDefinitions(definitions []SettingDef) error {
	seenKeys := make(map[string]bool, len(definitions))
	seenEnv := make(map[string]bool, len(definitions))
	for index := range definitions {
		def := &definitions[index]
		if strings.TrimSpace(def.Key) == "" || strings.TrimSpace(def.EnvVar) == "" {
			return fmt.Errorf("settings: key and EnvVar are required")
		}
		if seenKeys[def.Key] {
			return fmt.Errorf("settings: duplicate key %s", def.Key)
		}
		if seenEnv[def.EnvVar] {
			return fmt.Errorf("settings: duplicate EnvVar %s", def.EnvVar)
		}
		seenKeys[def.Key] = true
		seenEnv[def.EnvVar] = true

		switch def.Type {
		case TypeInt:
			if def.MinDuration != "" || def.MaxDuration != "" {
				return fmt.Errorf("settings: duration bounds on int setting %s", def.Key)
			}
			var min, max int64
			var err error
			if def.Min != "" {
				min, err = strconv.ParseInt(def.Min, 10, 64)
				if err != nil {
					return fmt.Errorf("settings: invalid Min for %s: %s", def.Key, def.Min)
				}
			}
			if def.Max != "" {
				max, err = strconv.ParseInt(def.Max, 10, 64)
				if err != nil {
					return fmt.Errorf("settings: invalid Max for %s: %s", def.Key, def.Max)
				}
			}
			if def.Min != "" && def.Max != "" && min > max {
				return fmt.Errorf("settings: Min exceeds Max for %s", def.Key)
			}
		case TypeDuration:
			if def.Min != "" || def.Max != "" {
				return fmt.Errorf("settings: integer bounds on duration setting %s", def.Key)
			}
			var min, max time.Duration
			var err error
			if def.MinDuration != "" {
				min, err = time.ParseDuration(def.MinDuration)
				if err != nil || min <= 0 {
					return fmt.Errorf("settings: invalid MinDuration for %s: %s", def.Key, def.MinDuration)
				}
			}
			if def.MaxDuration != "" {
				max, err = time.ParseDuration(def.MaxDuration)
				if err != nil || max <= 0 {
					return fmt.Errorf("settings: invalid MaxDuration for %s: %s", def.Key, def.MaxDuration)
				}
			}
			if def.MinDuration != "" && def.MaxDuration != "" && min > max {
				return fmt.Errorf("settings: MinDuration exceeds MaxDuration for %s", def.Key)
			}
		case TypeBool, TypeString:
			if def.Min != "" || def.Max != "" || def.MinDuration != "" || def.MaxDuration != "" {
				return fmt.Errorf("settings: unsupported bounds on %s", def.Key)
			}
		default:
			return fmt.Errorf("settings: invalid type for %s: %s", def.Key, def.Type)
		}
		if err := validateValue(def, def.CodeDefault); err != nil {
			return fmt.Errorf("settings: invalid default for %s: %w", def.Key, err)
		}
	}
	return nil
}

// Registry 返回所有设置定义（返回副本避免外部修改）
func (s *Service) Registry() []SettingDef {
	out := make([]SettingDef, len(registry))
	copy(out, registry)
	return out
}

// GetAll 返回所有设置的解析值（DB → env → default 优先级）
func (s *Service) GetAll() (map[string]ResolvedSetting, error) {
	var dbSettings []model.SystemSetting
	if err := s.db.Find(&dbSettings).Error; err != nil {
		return nil, fmt.Errorf("查询系统设置失败: %w", err)
	}
	dbMap := make(map[string]model.SystemSetting, len(dbSettings))
	for _, row := range dbSettings {
		dbMap[row.Key] = row
	}

	result := make(map[string]ResolvedSetting, len(registry))
	for _, def := range registry {
		if dbVal, ok := dbMap[def.Key]; ok {
			t := dbVal.UpdatedAt
			result[def.Key] = ResolvedSetting{Value: decryptSettingValue(def.Key, dbVal.Value), Source: "db", UpdatedAt: &t}
			continue
		}
		if envVal := strings.TrimSpace(os.Getenv(def.EnvVar)); envVal != "" {
			result[def.Key] = ResolvedSetting{Value: envVal, Source: "env"}
			continue
		}
		result[def.Key] = ResolvedSetting{Value: def.CodeDefault, Source: "default"}
	}
	return result, nil
}

// GetEffective 获取单项设置的有效值（带 TTL 缓存，供消费端调用）
func (s *Service) GetEffective(key string) string {
	if IsInternalSettingKey(key) {
		return ""
	}
	// 先查缓存
	s.mu.RLock()
	if cv, ok := s.cache[key]; ok && time.Now().Before(cv.expiresAt) {
		s.mu.RUnlock()
		return cv.value
	}
	s.mu.RUnlock()

	// 缓存未命中，查 DB
	value, err := s.resolveValue(key)
	if err != nil {
		// DB 短暂不可用时保留旧缓存（即使已过期），避免错误回退到 env/default 后污染缓存。
		s.mu.RLock()
		if cv, ok := s.cache[key]; ok {
			s.mu.RUnlock()
			return cv.value
		}
		s.mu.RUnlock()
		if def, ok := registryMap[key]; ok {
			if envVal := strings.TrimSpace(os.Getenv(def.EnvVar)); envVal != "" {
				return envVal
			}
			return def.CodeDefault
		}
		return ""
	}

	// 写入缓存
	s.mu.Lock()
	s.cache[key] = cachedValue{value: value, expiresAt: time.Now().Add(cacheTTL)}
	s.mu.Unlock()

	return value
}

// resolveValue 按 DB → env → default 优先级解析值（无缓存）
func (s *Service) resolveValue(key string) (string, error) {
	if IsInternalSettingKey(key) {
		return "", ErrInternalSettingUnavailable
	}
	// 使用 Limit(1).Find 代替 First，避免 GORM 对空结果打 "record not found" 错误日志
	var dbSettings []model.SystemSetting
	if err := s.db.Where("key = ?", key).Limit(1).Find(&dbSettings).Error; err != nil {
		return "", err
	}
	if len(dbSettings) > 0 {
		return decryptSettingValue(key, dbSettings[0].Value), nil
	}

	if def, ok := registryMap[key]; ok {
		if envVal := strings.TrimSpace(os.Getenv(def.EnvVar)); envVal != "" {
			return envVal, nil
		}
		return def.CodeDefault, nil
	}
	return "", nil
}

// ProcessingPipelineRevisions reads the reserved publication state directly
// from the database. It deliberately bypasses the public registry and cache.
func (s *Service) ProcessingPipelineRevisions(ctx context.Context) (ProcessingPipelineRevisions, error) {
	if s == nil || s.db == nil || ctx == nil {
		return ProcessingPipelineRevisions{}, ErrInternalSettingUnavailable
	}
	var rows []model.SystemSetting
	if err := s.db.WithContext(ctx).Where("key IN ?", []string{
		ProcessingContentPipelineRevisionKey, ProcessingOCRPipelineRevisionKey,
	}).Limit(2).Find(&rows).Error; err != nil || len(rows) != 2 {
		return ProcessingPipelineRevisions{}, ErrInternalSettingUnavailable
	}
	values := make(map[string]int64, 2)
	for _, row := range rows {
		if !IsInternalSettingKey(row.Key) || values[row.Key] != 0 {
			return ProcessingPipelineRevisions{}, ErrInternalSettingUnavailable
		}
		value, err := parsePositiveRevision(row.Value)
		if err != nil {
			return ProcessingPipelineRevisions{}, err
		}
		values[row.Key] = value
	}
	if values[ProcessingContentPipelineRevisionKey] == 0 || values[ProcessingOCRPipelineRevisionKey] == 0 {
		return ProcessingPipelineRevisions{}, ErrInternalSettingUnavailable
	}
	return ProcessingPipelineRevisions{
		Content: values[ProcessingContentPipelineRevisionKey], OCR: values[ProcessingOCRPipelineRevisionKey],
	}, nil
}

// AdvanceProcessingPipelineRevisionsTx initializes both reserved revisions and
// advances only the fields affected by one already-verified bundle activation.
func (s *Service) AdvanceProcessingPipelineRevisionsTx(
	ctx context.Context,
	tx *gorm.DB,
	affectContent bool,
	affectOCR bool,
) (ProcessingPipelineRevisions, error) {
	if s == nil || s.db == nil || tx == nil || ctx == nil || !affectContent && !affectOCR {
		return ProcessingPipelineRevisions{}, ErrInternalSettingUnavailable
	}
	keys := []string{ProcessingContentPipelineRevisionKey, ProcessingOCRPipelineRevisionKey}
	var rows []model.SystemSetting
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("key IN ?", keys).Limit(2).Find(&rows).Error; err != nil {
		return ProcessingPipelineRevisions{}, ErrInternalSettingUnavailable
	}
	current := make(map[string]int64, 2)
	for _, row := range rows {
		if !IsInternalSettingKey(row.Key) || current[row.Key] != 0 {
			return ProcessingPipelineRevisions{}, ErrInternalSettingUnavailable
		}
		value, err := parsePositiveRevision(row.Value)
		if err != nil {
			return ProcessingPipelineRevisions{}, err
		}
		current[row.Key] = value
	}
	for _, key := range keys {
		value := current[key]
		affected := key == ProcessingContentPipelineRevisionKey && affectContent || key == ProcessingOCRPipelineRevisionKey && affectOCR
		switch {
		case value == 0:
			value = 1
		case affected && value == int64(^uint64(0)>>1):
			return ProcessingPipelineRevisions{}, ErrInternalSettingUnavailable
		case affected:
			value++
		}
		if err := s.upsert(tx.WithContext(ctx), key, strconv.FormatInt(value, 10)); err != nil {
			return ProcessingPipelineRevisions{}, ErrInternalSettingUnavailable
		}
		current[key] = value
	}
	return ProcessingPipelineRevisions{
		Content: current[ProcessingContentPipelineRevisionKey], OCR: current[ProcessingOCRPipelineRevisionKey],
	}, nil
}

func parsePositiveRevision(value string) (int64, error) {
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 || strconv.FormatInt(parsed, 10) != value {
		return 0, ErrInternalSettingUnavailable
	}
	return parsed, nil
}

func IsInternalSettingKey(key string) bool {
	return key == ProcessingContentPipelineRevisionKey || key == ProcessingOCRPipelineRevisionKey ||
		strings.HasPrefix(key, RecoveryTargetRootKeyPrefix) ||
		strings.HasPrefix(key, RecoveryTargetRootReceiptKeyPrefix) ||
		strings.HasPrefix(key, RecoveryDowngradeReceiptKeyPrefix)
}

// RecoveryTargetRootLocatorDigest validates and binds one canonical private
// target locator to its node and root identities.
func RecoveryTargetRootLocatorDigest(nodeID uint, rootID, locator string) (string, error) {
	if nodeID == 0 || !validRecoveryTargetRootID(rootID) || !validRecoveryTargetRootLocator(locator) {
		return "", ErrRecoveryTargetRootInvalid
	}
	buffer := bytes.NewBuffer(nil)
	writeRecoveryTargetRootDigestString(buffer, recoveryTargetRootDigestDomain)
	writeRecoveryTargetRootDigestUint64(buffer, 3)
	writeRecoveryTargetRootDigestString(buffer, strconv.FormatUint(uint64(nodeID), 10))
	writeRecoveryTargetRootDigestString(buffer, rootID)
	writeRecoveryTargetRootDigestString(buffer, locator)
	sum := sha256.Sum256(buffer.Bytes())
	return hex.EncodeToString(sum[:]), nil
}

// RegisterRecoveryTargetRootTx persists or rotates exactly one private root in
// the caller-owned transaction. An identical definition is a no-op.
func (s *Service) RegisterRecoveryTargetRootTx(
	ctx context.Context,
	tx *gorm.DB,
	definition RecoveryTargetRootDefinition,
) (RecoveryTargetRootResolution, error) {
	if err := validateRecoveryTargetRootCall(s, ctx, tx); err != nil {
		return RecoveryTargetRootResolution{}, err
	}
	resolution, err := normalizeRecoveryTargetRootDefinition(definition)
	if err != nil {
		return RecoveryTargetRootResolution{}, err
	}
	if err := requireActiveRecoveryTargetRootNode(ctx, tx, definition.NodeID); err != nil {
		return RecoveryTargetRootResolution{}, err
	}
	key, err := recoveryTargetRootKey(definition.NodeID, definition.RootID)
	if err != nil {
		return RecoveryTargetRootResolution{}, err
	}

	var rows []model.SystemSetting
	if queryErr := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("key = ?", key).Limit(2).Find(&rows).Error; queryErr != nil {
		return RecoveryTargetRootResolution{}, recoveryTargetRootUnavailableForContext(ctx)
	}
	if len(rows) > 1 {
		return RecoveryTargetRootResolution{}, ErrRecoveryTargetRootUnavailable
	}
	if len(rows) == 1 {
		current, decodeErr := decodeRecoveryTargetRootRow(rows[0].Key, rows[0].Value)
		if decodeErr != nil {
			return RecoveryTargetRootResolution{}, decodeErr
		}
		if recoveryTargetRootSecurityEquivalent(current, resolution) {
			if current.AuthorityRevision != resolution.AuthorityRevision {
				return RecoveryTargetRootResolution{}, ErrRecoveryTargetRootInvalid
			}
			if current == resolution {
				return current, nil
			}
		} else if current.AuthorityRevision == resolution.AuthorityRevision {
			return RecoveryTargetRootResolution{}, ErrRecoveryTargetRootInvalid
		}
	}

	record := recoveryTargetRootRecord{
		SchemaVersion: recoveryTargetRootSchemaVersion, NodeID: resolution.NodeID,
		RootID: resolution.RootID, SafeLabel: resolution.SafeLabel,
		CanonicalLocator: resolution.Locator, LocatorDigest: resolution.LocatorDigest,
		AuthorityRevision:       resolution.AuthorityRevision,
		RootObservationRevision: resolution.RootObservationRevision,
		ReserveBytes:            resolution.Policy.ReserveBytes,
		ReserveInodes:           resolution.Policy.ReserveInodes,
		OverlapPolicyBinding:    resolution.Policy.OverlapPolicyBinding,
	}
	document, marshalErr := json.Marshal(record)
	if marshalErr != nil || len(document) == 0 || len(document) > recoveryTargetRootDocumentMaxBytes {
		return RecoveryTargetRootResolution{}, ErrRecoveryTargetRootUnavailable
	}
	ciphertext, encryptErr := secure.EncryptString(string(document))
	if encryptErr != nil || !strings.HasPrefix(ciphertext, "enc:v2:") {
		return RecoveryTargetRootResolution{}, recoveryTargetRootUnavailableForContext(ctx)
	}
	row := model.SystemSetting{Key: key, Value: ciphertext, UpdatedAt: time.Now().UTC()}
	if persistErr := tx.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at"}),
	}).Create(&row).Error; persistErr != nil {
		return RecoveryTargetRootResolution{}, recoveryTargetRootUnavailableForContext(ctx)
	}
	return resolution, nil
}

// DeleteRecoveryTargetRootTx removes exactly one constructed private key from
// the caller-owned transaction.
func (s *Service) DeleteRecoveryTargetRootTx(ctx context.Context, tx *gorm.DB, nodeID uint, rootID string) error {
	if err := validateRecoveryTargetRootCall(s, ctx, tx); err != nil {
		return err
	}
	key, err := recoveryTargetRootKey(nodeID, rootID)
	if err != nil {
		return err
	}
	var rows []model.SystemSetting
	if queryErr := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("key = ?", key).Limit(2).Find(&rows).Error; queryErr != nil {
		return recoveryTargetRootUnavailableForContext(ctx)
	}
	if len(rows) == 0 {
		return ErrRecoveryTargetRootNotFound
	}
	if len(rows) != 1 {
		return ErrRecoveryTargetRootUnavailable
	}
	resolved, decodeErr := decodeRecoveryTargetRootRow(rows[0].Key, rows[0].Value)
	if decodeErr != nil || resolved.NodeID != nodeID || resolved.RootID != rootID {
		return ErrRecoveryTargetRootUnavailable
	}
	result := tx.WithContext(ctx).Where("key = ? AND value = ?", key, rows[0].Value).Delete(&model.SystemSetting{})
	if result.Error != nil {
		return recoveryTargetRootUnavailableForContext(ctx)
	}
	if result.RowsAffected != 1 {
		return ErrRecoveryTargetRootUnavailable
	}
	return nil
}

// ResolveRecoveryTargetRootTx loads one exact private root through the
// caller-owned transaction and validates its complete encrypted record.
func (s *Service) ResolveRecoveryTargetRootTx(
	ctx context.Context,
	tx *gorm.DB,
	nodeID uint,
	rootID string,
) (RecoveryTargetRootResolution, error) {
	if err := validateRecoveryTargetRootCall(s, ctx, tx); err != nil {
		return RecoveryTargetRootResolution{}, err
	}
	key, err := recoveryTargetRootKey(nodeID, rootID)
	if err != nil {
		return RecoveryTargetRootResolution{}, err
	}
	if err := requireActiveRecoveryTargetRootNode(ctx, tx, nodeID); err != nil {
		return RecoveryTargetRootResolution{}, err
	}
	var rows []model.SystemSetting
	if queryErr := tx.WithContext(ctx).Where("key = ?", key).Limit(2).Find(&rows).Error; queryErr != nil {
		return RecoveryTargetRootResolution{}, recoveryTargetRootUnavailableForContext(ctx)
	}
	if len(rows) == 0 {
		return RecoveryTargetRootResolution{}, ErrRecoveryTargetRootNotFound
	}
	if len(rows) != 1 {
		return RecoveryTargetRootResolution{}, ErrRecoveryTargetRootUnavailable
	}
	return decodeRecoveryTargetRootRow(rows[0].Key, rows[0].Value)
}

// ListRecoveryTargetRoots returns only safe summaries for one active node. A
// malformed row fails the whole bounded result.
func (s *Service) ListRecoveryTargetRoots(ctx context.Context, nodeID uint) ([]RecoveryTargetRootSummary, error) {
	if s == nil || s.db == nil || ctx == nil {
		return nil, ErrRecoveryTargetRootUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if nodeID == 0 {
		return nil, ErrRecoveryTargetRootInvalid
	}
	if err := requireActiveRecoveryTargetRootNode(ctx, s.db, nodeID); err != nil {
		return nil, err
	}
	nodePrefix := RecoveryTargetRootKeyPrefix + strconv.FormatUint(uint64(nodeID), 10) + "."
	var rows []model.SystemSetting
	if queryErr := s.db.WithContext(ctx).
		Where("substr(key, 1, ?) = ?", len(nodePrefix), nodePrefix).
		Order("key ASC").Limit(recoveryTargetRootListMax + 1).Find(&rows).Error; queryErr != nil {
		return nil, recoveryTargetRootUnavailableForContext(ctx)
	}
	if len(rows) > recoveryTargetRootListMax {
		return nil, ErrRecoveryTargetRootUnavailable
	}
	summaries := make([]RecoveryTargetRootSummary, 0, len(rows))
	for _, row := range rows {
		resolution, err := decodeRecoveryTargetRootRow(row.Key, row.Value)
		if err != nil || resolution.NodeID != nodeID {
			return nil, ErrRecoveryTargetRootUnavailable
		}
		summaries = append(summaries, RecoveryTargetRootSummary{
			NodeID: resolution.NodeID, RootID: resolution.RootID, SafeLabel: resolution.SafeLabel,
		})
	}
	return summaries, nil
}

// ListAllRecoveryTargetRoots returns the bounded reconciliation catalog without
// exposing labels or locators. Any malformed row or inactive node invalidates
// the complete result.
func (s *Service) ListAllRecoveryTargetRoots(ctx context.Context) ([]RecoveryTargetRootReference, error) {
	if s == nil || s.db == nil || ctx == nil {
		return nil, ErrRecoveryTargetRootUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var rows []model.SystemSetting
	if err := s.db.WithContext(ctx).
		Where("substr(key, 1, ?) = ?", len(RecoveryTargetRootKeyPrefix), RecoveryTargetRootKeyPrefix).
		Order("key ASC").Limit(recoveryTargetRootAllListMax + 1).Find(&rows).Error; err != nil {
		return nil, recoveryTargetRootUnavailableForContext(ctx)
	}
	if len(rows) > recoveryTargetRootAllListMax {
		return nil, ErrRecoveryTargetRootUnavailable
	}

	type rootIdentity struct {
		nodeID uint
		rootID string
	}
	references := make([]RecoveryTargetRootReference, 0, len(rows))
	identities := make(map[rootIdentity]struct{}, len(rows))
	nodeIDs := make(map[uint]struct{})
	for _, row := range rows {
		resolution, err := decodeRecoveryTargetRootRow(row.Key, row.Value)
		if err != nil {
			return nil, ErrRecoveryTargetRootUnavailable
		}
		identity := rootIdentity{nodeID: resolution.NodeID, rootID: resolution.RootID}
		if _, duplicate := identities[identity]; duplicate {
			return nil, ErrRecoveryTargetRootUnavailable
		}
		identities[identity] = struct{}{}
		nodeIDs[resolution.NodeID] = struct{}{}
		references = append(references, RecoveryTargetRootReference{
			NodeID: resolution.NodeID, RootID: resolution.RootID,
		})
	}

	if len(nodeIDs) > 0 {
		ids := make([]uint, 0, len(nodeIDs))
		for nodeID := range nodeIDs {
			ids = append(ids, nodeID)
		}
		var nodes []struct {
			ID       uint
			Archived bool
		}
		if err := s.db.WithContext(ctx).Model(&model.Node{}).Select("id", "archived").
			Where("id IN ?", ids).Limit(len(ids) + 1).Find(&nodes).Error; err != nil {
			return nil, recoveryTargetRootUnavailableForContext(ctx)
		}
		active := make(map[uint]struct{}, len(nodes))
		for _, node := range nodes {
			if node.ID == 0 || node.Archived {
				return nil, ErrRecoveryTargetRootUnavailable
			}
			if _, duplicate := active[node.ID]; duplicate {
				return nil, ErrRecoveryTargetRootUnavailable
			}
			active[node.ID] = struct{}{}
		}
		if len(active) != len(nodeIDs) {
			return nil, ErrRecoveryTargetRootUnavailable
		}
	}

	sort.Slice(references, func(left, right int) bool {
		if references[left].NodeID != references[right].NodeID {
			return references[left].NodeID < references[right].NodeID
		}
		return references[left].RootID < references[right].RootID
	})
	return references, nil
}

func validateRecoveryTargetRootCall(s *Service, ctx context.Context, tx *gorm.DB) error {
	if s == nil || s.db == nil || ctx == nil || tx == nil {
		return ErrRecoveryTargetRootUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func normalizeRecoveryTargetRootDefinition(definition RecoveryTargetRootDefinition) (RecoveryTargetRootResolution, error) {
	if !validRecoveryTargetRootSafeLabel(definition.SafeLabel) ||
		!validRecoveryTargetRootAuthorityRevision(definition.AuthorityRevision) ||
		!validRecoveryTargetRootOpaqueBinding(definition.RootObservationRevision) ||
		definition.Policy.ReserveBytes < 0 || definition.Policy.ReserveInodes < 0 ||
		!validRecoveryTargetRootOpaqueBinding(definition.Policy.OverlapPolicyBinding) {
		return RecoveryTargetRootResolution{}, ErrRecoveryTargetRootInvalid
	}
	digest, err := RecoveryTargetRootLocatorDigest(definition.NodeID, definition.RootID, definition.Locator)
	if err != nil {
		return RecoveryTargetRootResolution{}, err
	}
	return RecoveryTargetRootResolution{
		NodeID: definition.NodeID, RootID: definition.RootID, SafeLabel: definition.SafeLabel,
		Locator: definition.Locator, LocatorDigest: digest,
		AuthorityRevision:       definition.AuthorityRevision,
		RootObservationRevision: definition.RootObservationRevision,
		Policy:                  definition.Policy,
	}, nil
}

func recoveryTargetRootSecurityEquivalent(left, right RecoveryTargetRootResolution) bool {
	return left.NodeID == right.NodeID && left.RootID == right.RootID &&
		left.Locator == right.Locator && left.LocatorDigest == right.LocatorDigest &&
		left.RootObservationRevision == right.RootObservationRevision &&
		left.Policy == right.Policy
}

func requireActiveRecoveryTargetRootNode(ctx context.Context, db *gorm.DB, nodeID uint) error {
	if ctx == nil || db == nil || nodeID == 0 {
		return ErrRecoveryTargetRootInvalid
	}
	var nodes []struct {
		ID       uint
		Archived bool
	}
	if err := db.WithContext(ctx).Model(&model.Node{}).Select("id", "archived").
		Where("id = ?", nodeID).Limit(2).Find(&nodes).Error; err != nil {
		return recoveryTargetRootUnavailableForContext(ctx)
	}
	if len(nodes) != 1 || nodes[0].ID != nodeID || nodes[0].Archived {
		return ErrRecoveryTargetRootNotFound
	}
	return nil
}

func recoveryTargetRootKey(nodeID uint, rootID string) (string, error) {
	if nodeID == 0 || !validRecoveryTargetRootID(rootID) {
		return "", ErrRecoveryTargetRootInvalid
	}
	key := RecoveryTargetRootKeyPrefix + strconv.FormatUint(uint64(nodeID), 10) + "." + rootID
	if len(key) > 128 {
		return "", ErrRecoveryTargetRootInvalid
	}
	return key, nil
}

func parseRecoveryTargetRootKey(key string) (uint, string, error) {
	if !strings.HasPrefix(key, RecoveryTargetRootKeyPrefix) || len(key) > 128 {
		return 0, "", ErrRecoveryTargetRootUnavailable
	}
	nodeText, rootID, found := strings.Cut(strings.TrimPrefix(key, RecoveryTargetRootKeyPrefix), ".")
	if !found || nodeText == "" || rootID == "" || !validRecoveryTargetRootID(rootID) {
		return 0, "", ErrRecoveryTargetRootUnavailable
	}
	parsed, err := strconv.ParseUint(nodeText, 10, 64)
	if err != nil || parsed == 0 || uint64(uint(parsed)) != parsed || strconv.FormatUint(parsed, 10) != nodeText {
		return 0, "", ErrRecoveryTargetRootUnavailable
	}
	return uint(parsed), rootID, nil
}

func decodeRecoveryTargetRootRow(key, value string) (RecoveryTargetRootResolution, error) {
	nodeID, rootID, err := parseRecoveryTargetRootKey(key)
	if err != nil || value == "" || !strings.HasPrefix(value, "enc:v2:") {
		return RecoveryTargetRootResolution{}, ErrRecoveryTargetRootUnavailable
	}
	document, decryptErr := secure.DecryptString(value)
	if decryptErr != nil || len(document) == 0 || len(document) > recoveryTargetRootDocumentMaxBytes {
		return RecoveryTargetRootResolution{}, ErrRecoveryTargetRootUnavailable
	}
	record, decodeErr := decodeRecoveryTargetRootDocument(document)
	if decodeErr != nil || record.SchemaVersion != recoveryTargetRootSchemaVersion ||
		record.NodeID != nodeID || record.RootID != rootID {
		return RecoveryTargetRootResolution{}, ErrRecoveryTargetRootUnavailable
	}
	resolution, normalizeErr := normalizeRecoveryTargetRootDefinition(RecoveryTargetRootDefinition{
		NodeID: record.NodeID, RootID: record.RootID, SafeLabel: record.SafeLabel, Locator: record.CanonicalLocator,
		AuthorityRevision:       record.AuthorityRevision,
		RootObservationRevision: record.RootObservationRevision,
		Policy: RecoveryTargetRootPolicy{
			ReserveBytes: record.ReserveBytes, ReserveInodes: record.ReserveInodes,
			OverlapPolicyBinding: record.OverlapPolicyBinding,
		},
	})
	if normalizeErr != nil || resolution.LocatorDigest != record.LocatorDigest {
		return RecoveryTargetRootResolution{}, ErrRecoveryTargetRootUnavailable
	}
	return resolution, nil
}

func decodeRecoveryTargetRootDocument(document string) (recoveryTargetRootRecord, error) {
	decoder := json.NewDecoder(strings.NewReader(document))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return recoveryTargetRootRecord{}, ErrRecoveryTargetRootUnavailable
	}
	var record recoveryTargetRootRecord
	seen := make(map[string]struct{}, 11)
	for decoder.More() {
		nameToken, tokenErr := decoder.Token()
		name, ok := nameToken.(string)
		if tokenErr != nil || !ok {
			return recoveryTargetRootRecord{}, ErrRecoveryTargetRootUnavailable
		}
		if _, duplicate := seen[name]; duplicate {
			return recoveryTargetRootRecord{}, ErrRecoveryTargetRootUnavailable
		}
		seen[name] = struct{}{}
		switch name {
		case "schema_version":
			err = decoder.Decode(&record.SchemaVersion)
		case "node_id":
			err = decoder.Decode(&record.NodeID)
		case "root_id":
			err = decoder.Decode(&record.RootID)
		case "safe_label":
			err = decoder.Decode(&record.SafeLabel)
		case "canonical_locator":
			err = decoder.Decode(&record.CanonicalLocator)
		case "locator_digest":
			err = decoder.Decode(&record.LocatorDigest)
		case "authority_revision":
			err = decoder.Decode(&record.AuthorityRevision)
		case "root_observation_revision":
			err = decoder.Decode(&record.RootObservationRevision)
		case "reserve_bytes":
			err = decodeRecoveryTargetRootRequiredInt64(decoder, &record.ReserveBytes)
		case "reserve_inodes":
			err = decodeRecoveryTargetRootRequiredInt64(decoder, &record.ReserveInodes)
		case "overlap_policy_binding":
			err = decoder.Decode(&record.OverlapPolicyBinding)
		default:
			return recoveryTargetRootRecord{}, ErrRecoveryTargetRootUnavailable
		}
		if err != nil {
			return recoveryTargetRootRecord{}, ErrRecoveryTargetRootUnavailable
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') || len(seen) != 11 {
		return recoveryTargetRootRecord{}, ErrRecoveryTargetRootUnavailable
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return recoveryTargetRootRecord{}, ErrRecoveryTargetRootUnavailable
	}
	return record, nil
}

func decodeRecoveryTargetRootRequiredInt64(decoder *json.Decoder, target *int64) error {
	if decoder == nil || target == nil {
		return ErrRecoveryTargetRootUnavailable
	}
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return ErrRecoveryTargetRootUnavailable
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return ErrRecoveryTargetRootUnavailable
	}
	return nil
}

func validRecoveryTargetRootAuthorityRevision(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func validRecoveryTargetRootOpaqueBinding(value string) bool {
	if !utf8.ValidString(value) || len(value) == 0 || len(value) > 256 || strings.TrimSpace(value) == "" {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validRecoveryTargetRootID(rootID string) bool {
	if len(rootID) == 0 || len(rootID) > recoveryTargetRootIDMaxBytes {
		return false
	}
	validEndpoint := func(value byte) bool {
		return value >= 'a' && value <= 'z' || value >= '0' && value <= '9'
	}
	if !validEndpoint(rootID[0]) || !validEndpoint(rootID[len(rootID)-1]) {
		return false
	}
	for index := 1; index < len(rootID)-1; index++ {
		value := rootID[index]
		if !validEndpoint(value) && value != '-' && value != '_' {
			return false
		}
	}
	return true
}

func validRecoveryTargetRootSafeLabel(label string) bool {
	if !utf8.ValidString(label) || len(label) == 0 || len(label) > recoveryTargetRootSafeLabelMaxBytes || strings.TrimSpace(label) == "" {
		return false
	}
	for _, character := range label {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validRecoveryTargetRootLocator(locator string) bool {
	if !utf8.ValidString(locator) || len(locator) < 2 || len(locator) > recoveryTargetRootLocatorMaxBytes ||
		locator == "/" || !path.IsAbs(locator) || path.Clean(locator) != locator ||
		strings.HasSuffix(locator, "/") || strings.Contains(locator, `\`) {
		return false
	}
	for index := 0; index < len(locator); index++ {
		if locator[index] < 0x20 || locator[index] == 0x7f {
			return false
		}
	}
	components := strings.Split(locator, "/")
	if len(components) < 2 || components[0] != "" {
		return false
	}
	for _, component := range components[1:] {
		if component == "" || component == "." || component == ".." {
			return false
		}
	}
	return true
}

func recoveryTargetRootUnavailableForContext(ctx context.Context) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return ErrRecoveryTargetRootUnavailable
}

func writeRecoveryTargetRootDigestString(buffer *bytes.Buffer, value string) {
	writeRecoveryTargetRootDigestUint64(buffer, uint64(len(value)))
	buffer.WriteString(value)
}

func writeRecoveryTargetRootDigestUint64(buffer *bytes.Buffer, value uint64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	buffer.Write(encoded[:])
}

// GetFallback resolves a known setting from environment or its code default,
// intentionally excluding any database override.
func (s *Service) GetFallback(key string) (string, error) {
	if IsInternalSettingKey(key) {
		return "", ErrInternalSettingUnavailable
	}
	def := findDef(key)
	if def == nil {
		return "", fmt.Errorf("未知的设置项: %s", key)
	}
	if envValue := strings.TrimSpace(os.Getenv(def.EnvVar)); envValue != "" {
		return envValue, nil
	}
	return def.CodeDefault, nil
}

// ValidateBackupAssetEffectiveUpdate validates an explicitly supplied current
// foundation snapshot plus a requested foundation-only overlay without reading
// database, environment, cache, or mutating either input map.
func (s *Service) ValidateBackupAssetEffectiveUpdate(current, overrides map[string]string) error {
	resolved := make(map[string]string, len(backupAssetFoundationSettingKeys))
	for _, key := range backupAssetFoundationSettingKeys {
		value, ok := current[key]
		if !ok {
			return fmt.Errorf("缺少备份资产当前设置: %s", key)
		}
		def := findDef(key)
		if def == nil {
			return fmt.Errorf("缺少备份资产设置定义: %s", key)
		}
		if err := validateValue(def, value); err != nil {
			return err
		}
		resolved[key] = value
	}
	for key, value := range overrides {
		if !IsBackupAssetFoundationSetting(key) {
			return fmt.Errorf("不是备份资产基础设置: %s", key)
		}
		def := findDef(key)
		if err := validateValue(def, value); err != nil {
			return err
		}
		resolved[key] = value
	}
	return validateBackupAssetFoundationConfig(resolved, true)
}

// WithBackupAssetMutation serializes callbacks that coordinate a foundation
// settings persistence transaction with external admission transitions.
func (s *Service) WithBackupAssetMutation(ctx context.Context, callback func(current map[string]string) error) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("settings service is unavailable")
	}
	if callback == nil {
		return fmt.Errorf("backup asset mutation callback is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.backupAssetMutationGate.acquire(ctx); err != nil {
		return err
	}
	defer s.backupAssetMutationGate.release()
	if err := ctx.Err(); err != nil {
		return err
	}
	current, err := s.backupAssetFoundationSnapshot()
	if err != nil {
		return err
	}
	return callback(copyStringMap(current))
}

// BackupAssetSettingsSnapshot returns one validated copy while coordinated
// multi-key backup-asset mutations are excluded.
func (s *Service) BackupAssetSettingsSnapshot() (map[string]string, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("settings service is unavailable")
	}
	s.backupAssetMutationGate.acquireBlocking()
	defer s.backupAssetMutationGate.release()
	values, err := s.backupAssetFoundationSnapshot()
	if err != nil {
		return nil, err
	}
	return copyStringMap(values), nil
}

func (s *Service) backupAssetFoundationSnapshot() (map[string]string, error) {
	values := make(map[string]string, len(backupAssetFoundationSettingKeys))
	for _, key := range backupAssetFoundationSettingKeys {
		value, err := s.resolveValue(key)
		if err != nil {
			return nil, fmt.Errorf("resolve %s: %w", key, err)
		}
		values[key] = value
	}
	if err := validateBackupAssetFoundationConfig(values, true); err != nil {
		return nil, err
	}
	return values, nil
}

func copyStringMap(values map[string]string) map[string]string {
	copy := make(map[string]string, len(values))
	for key, value := range values {
		copy[key] = value
	}
	return copy
}

// Validate 校验设置值（不写入），用于批量更新前的预检
func (s *Service) Validate(key, value string) error {
	if IsInternalSettingKey(key) {
		return ErrInternalSettingUnavailable
	}
	def := findDef(key)
	if def == nil {
		return fmt.Errorf("未知的设置项: %s", key)
	}
	if len(value) > maxValueLength {
		return fmt.Errorf("设置值长度不能超过 %d 字符", maxValueLength)
	}
	return validateValue(def, value)
}

// Update 更新设置值（含校验），写入后自动失效缓存
func (s *Service) Update(key, value string) error {
	return s.UpdateContext(context.Background(), key, value)
}

// UpdateContext updates one setting while honoring the caller's cancellation.
func (s *Service) UpdateContext(ctx context.Context, key, value string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.Validate(key, value); err != nil {
		return err
	}
	if err := s.upsert(s.db.WithContext(ctx), key, value); err != nil {
		return err
	}
	s.invalidateCache(key)
	return nil
}

// UpdateWithTx 在指定事务内更新设置值（供 config import 使用）
func (s *Service) UpdateWithTx(tx *gorm.DB, key, value string) error {
	return s.UpdateWithTxContext(context.Background(), tx, key, value)
}

// UpdateWithTxContext updates one setting through the caller-owned transaction
// while binding every database operation to the supplied context.
func (s *Service) UpdateWithTxContext(ctx context.Context, tx *gorm.DB, key, value string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if tx == nil {
		return fmt.Errorf("settings transaction is unavailable")
	}
	if err := s.Validate(key, value); err != nil {
		return err
	}
	if err := s.upsert(tx.WithContext(ctx), key, value); err != nil {
		return err
	}
	s.invalidateCache(key)
	return nil
}

// UpdateMany validates and persists a bounded setting set atomically.
func (s *Service) UpdateMany(values map[string]string) error {
	return s.UpdateManyContext(context.Background(), values)
}

// UpdateManyContext validates and persists a bounded setting set atomically
// while honoring the caller's cancellation.
func (s *Service) UpdateManyContext(ctx context.Context, values map[string]string) error {
	if s == nil || s.db == nil || len(values) == 0 || len(values) > 64 {
		return fmt.Errorf("settings batch is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	keys := make([]string, 0, len(values))
	for key, value := range values {
		if err := s.Validate(key, value); err != nil {
			return err
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, key := range keys {
			if err := s.upsert(tx, key, values[key]); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	for _, key := range keys {
		s.invalidateCache(key)
	}
	return nil
}

// CaptureSettingOverridesContext snapshots the exact persisted state for a
// bounded set of registered settings. A caller that also mutates Foundation
// settings must already own the higher-level mutation gate.
func (s *Service) CaptureSettingOverridesContext(
	ctx context.Context,
	keys []string,
) (*BackupAssetOverrideSnapshot, error) {
	return s.captureSettingOverridesContext(ctx, keys, false)
}

// CaptureBackupAssetOverridesContext snapshots the exact persisted state for a
// bounded set of Foundation settings. The caller owns higher-level mutation
// serialization; this method deliberately does not reacquire that gate.
func (s *Service) CaptureBackupAssetOverridesContext(
	ctx context.Context,
	keys []string,
) (*BackupAssetOverrideSnapshot, error) {
	return s.captureSettingOverridesContext(ctx, keys, true)
}

func (s *Service) captureSettingOverridesContext(
	ctx context.Context,
	keys []string,
	foundationOnly bool,
) (*BackupAssetOverrideSnapshot, error) {
	maxKeys := len(registry)
	if foundationOnly {
		maxKeys = 64
	}
	if s == nil || s.db == nil || len(keys) == 0 || len(keys) > maxKeys {
		return nil, fmt.Errorf("settings override snapshot is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sortedKeys := append([]string(nil), keys...)
	sort.Strings(sortedKeys)
	for index, key := range sortedKeys {
		if findDef(key) == nil || (foundationOnly && !IsBackupAssetFoundationSetting(key)) ||
			(index > 0 && key == sortedKeys[index-1]) {
			return nil, fmt.Errorf("invalid settings override snapshot key: %s", key)
		}
	}
	var rows []model.SystemSetting
	if err := s.db.WithContext(ctx).Where("key IN ?", sortedKeys).Limit(len(sortedKeys) + 1).Find(&rows).Error; err != nil {
		return nil, err
	}
	requested := make(map[string]bool, len(sortedKeys))
	for _, key := range sortedKeys {
		requested[key] = true
	}
	rowByKey := make(map[string]model.SystemSetting, len(rows))
	for _, row := range rows {
		if !requested[row.Key] {
			return nil, fmt.Errorf("unexpected backup asset override snapshot row: %s", row.Key)
		}
		if _, duplicate := rowByKey[row.Key]; duplicate {
			return nil, fmt.Errorf("duplicate backup asset override snapshot row: %s", row.Key)
		}
		rowByKey[row.Key] = row
	}
	return &BackupAssetOverrideSnapshot{keys: sortedKeys, rows: rowByKey}, nil
}

// RestoreSettingOverridesContext atomically restores every captured raw row
// and deletes keys that were absent. Cache invalidation occurs only after the
// transaction commits.
func (s *Service) RestoreSettingOverridesContext(
	ctx context.Context,
	snapshot *BackupAssetOverrideSnapshot,
) error {
	return s.restoreSettingOverridesContext(ctx, snapshot, false)
}

// RestoreBackupAssetOverridesContext atomically restores every captured raw row
// and deletes keys that were absent. Cache invalidation occurs only after the
// transaction commits, matching UpdateManyContext semantics.
func (s *Service) RestoreBackupAssetOverridesContext(
	ctx context.Context,
	snapshot *BackupAssetOverrideSnapshot,
) error {
	return s.restoreSettingOverridesContext(ctx, snapshot, true)
}

func (s *Service) restoreSettingOverridesContext(
	ctx context.Context,
	snapshot *BackupAssetOverrideSnapshot,
	foundationOnly bool,
) error {
	maxKeys := len(registry)
	if foundationOnly {
		maxKeys = 64
	}
	if s == nil || s.db == nil || snapshot == nil || len(snapshot.keys) == 0 || len(snapshot.keys) > maxKeys {
		return fmt.Errorf("settings override restoration is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, key := range snapshot.keys {
			row, exists := snapshot.rows[key]
			if !exists {
				if err := tx.WithContext(ctx).Where("key = ?", key).Delete(&model.SystemSetting{}).Error; err != nil {
					return err
				}
				continue
			}
			if row.Key != key || findDef(key) == nil || (foundationOnly && !IsBackupAssetFoundationSetting(key)) {
				return fmt.Errorf("invalid settings override restoration row: %s", key)
			}
			if err := tx.WithContext(ctx).Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "key"}},
				DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at"}),
			}).Create(&row).Error; err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	for _, key := range snapshot.keys {
		s.invalidateCache(key)
	}
	return nil
}

func (s *Service) upsert(db *gorm.DB, key, value string) error {
	storedValue := value
	if isSensitiveSetting(key) {
		encrypted, err := secure.EncryptIfNeeded(value)
		if err != nil {
			return err
		}
		storedValue = encrypted
	}
	setting := model.SystemSetting{
		Key:       key,
		Value:     storedValue,
		UpdatedAt: time.Now(),
	}
	return db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at"}),
	}).Create(&setting).Error
}

func isSensitiveSetting(key string) bool {
	def := findDef(key)
	return def != nil && def.Sensitive
}

func decryptSettingValue(key, value string) string {
	if !isSensitiveSetting(key) || strings.TrimSpace(value) == "" {
		return value
	}
	decrypted, err := secure.DecryptIfNeeded(value)
	if err != nil {
		return ""
	}
	return decrypted
}

// Delete 删除 DB 覆盖值（恢复为环境变量或默认值），写入后自动失效缓存
func (s *Service) Delete(key string) error {
	return s.DeleteContext(context.Background(), key)
}

// DeleteContext removes one database override while honoring cancellation.
func (s *Service) DeleteContext(ctx context.Context, key string) error {
	if IsInternalSettingKey(key) {
		return ErrInternalSettingUnavailable
	}
	def := findDef(key)
	if def == nil {
		return fmt.Errorf("未知的设置项: %s", key)
	}
	if err := s.DeleteWithTxContext(ctx, s.db, key); err != nil {
		return err
	}
	return nil
}

// DeleteWithTx removes one database override through the caller's existing
// transaction. Foundation-setting callers use it only after the admission
// transition has drained, so a rollback leaves the prior persisted value.
func (s *Service) DeleteWithTx(tx *gorm.DB, key string) error {
	return s.DeleteWithTxContext(context.Background(), tx, key)
}

// DeleteWithTxContext removes one override through a caller-owned transaction
// while binding the delete to the supplied context.
func (s *Service) DeleteWithTxContext(ctx context.Context, tx *gorm.DB, key string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if IsInternalSettingKey(key) {
		return ErrInternalSettingUnavailable
	}
	def := findDef(key)
	if def == nil {
		return fmt.Errorf("未知的设置项: %s", key)
	}
	if tx == nil {
		return fmt.Errorf("settings transaction is unavailable")
	}
	if err := tx.WithContext(ctx).Where("key = ?", key).Delete(&model.SystemSetting{}).Error; err != nil {
		return err
	}
	s.invalidateCache(key)
	return nil
}

// invalidateCache 清除指定 key 的缓存
func (s *Service) invalidateCache(key string) {
	s.mu.Lock()
	delete(s.cache, key)
	s.mu.Unlock()
}

// InvalidateCachedValues discards only the named values after a caller-owned
// transaction restores their raw rows. It does not read or persist settings.
func (s *Service) InvalidateCachedValues(keys []string) {
	if s == nil {
		return
	}
	for _, key := range keys {
		s.invalidateCache(key)
	}
}

// findDef O(1) 查找设置定义
func findDef(key string) *SettingDef {
	return registryMap[key]
}

// validateValue 校验设置值（含安全下限）
func validateValue(def *SettingDef, value string) error {
	switch def.Type {
	case TypeInt:
		v, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return fmt.Errorf("设置项 %s 值必须为整数", def.Key)
		}
		if def.Min != "" {
			min, _ := strconv.ParseInt(def.Min, 10, 64)
			if v < min {
				return fmt.Errorf("设置项 %s 值不能小于 %s", def.Key, def.Min)
			}
		}
		if def.Max != "" {
			max, _ := strconv.ParseInt(def.Max, 10, 64)
			if v > max {
				return fmt.Errorf("设置项 %s 值不能大于 %s", def.Key, def.Max)
			}
		}
	case TypeBool:
		lower := strings.ToLower(value)
		if lower != "true" && lower != "false" {
			return fmt.Errorf("设置项 %s 值必须为 true 或 false", def.Key)
		}
	case TypeDuration:
		d, err := time.ParseDuration(value)
		if err != nil {
			return fmt.Errorf("设置项 %s 值必须为有效的时间格式 (如 5m, 1h)", def.Key)
		}
		if d <= 0 {
			return fmt.Errorf("设置项 %s 值必须大于 0", def.Key)
		}
		// 安全下限校验
		if def.MinDuration != "" {
			minD, _ := time.ParseDuration(def.MinDuration)
			if d < minD {
				return fmt.Errorf("设置项 %s 值不能小于 %s", def.Key, def.MinDuration)
			}
		}
		if def.MaxDuration != "" {
			maxD, _ := time.ParseDuration(def.MaxDuration)
			if d > maxD {
				return fmt.Errorf("设置项 %s 值不能大于 %s", def.Key, def.MaxDuration)
			}
		}
	}
	return nil
}

var backupAssetCoreSettingKeys = []string{
	"backup_assets.enabled",
	"backup_assets.retention_reconcile_interval",
	"backup_assets.retention_batch_size",
	"backup_assets.retention_drain_timeout",
	"backup_assets.catalog_batch_size",
	"backup_assets.catalog_build_timeout",
	"backup_assets.repository_reconcile_interval",
	"backup_assets.audit_segment_max_events",
	"backup_assets.audit_segment_max_age",
	"backup_assets.audit_detail_retention_days",
	"backup_assets.audit_checkpoint_retention_days",
	"backup_assets.lease_duration",
	"backup_assets.lease_heartbeat",
	"backup_assets.lease_absolute_deadline",
	"backup_assets.provider_operation_timeout",
	"backup_assets.provider_max_concurrency",
	"backup_assets.provider_metadata_limit_bytes",
	"backup_assets.publication_reconcile_interval",
	"backup_assets.publication_reconcile_batch_size",
	"backup_assets.publication_worker_concurrency",
	"backup_assets.publication_missing_grace",
	"backup_assets.publication_stream_max_bytes",
	"backup_assets.manifest_timeout",
	"backup_assets.manifest_max_bytes",
	"backup_assets.manifest_max_entries",
	"backup_assets.manifest_max_record_bytes",
	"backup_assets.manifest_max_depth",
	"backup_assets.rclone_preflight_ttl",
	"backup_assets.rclone_portable_deadline",
	"backup_assets.rclone_native_deadline",
	"backup_assets.rclone_bound_config_max_bytes",
	"backup_assets.rclone_control_payload_max_bytes",
	"backup_assets.rclone_full_verify_max_bytes",
	"backup_assets.rclone_manifest_chunk_max_bytes",
	"backup_assets.rclone_low_level_retries",
	"backup_assets.rclone_staging_orphan_age",
	"backup_assets.rclone_staging_scan_limit",
	"backup_assets.rclone_kms_read_key_max_count",
	"backup_assets.rclone_health_interval",
	"backup_assets.rclone_health_batch_size",
	"backup_assets.rclone_aws_sdk_max_attempts",
}

var backupAssetSearchOverlaySettingKeys = []string{
	"backup_assets.search_reconcile_interval",
	"backup_assets.search_build_timeout",
	"backup_assets.search_batch_size",
	"backup_assets.search_max_concurrency",
	"backup_assets.search_ast_max_depth",
	"backup_assets.search_ast_max_nodes",
	"backup_assets.search_values_per_node",
	"backup_assets.search_body_max_bytes",
	"backup_assets.search_value_max_bytes",
	"backup_assets.search_candidate_limit",
	"backup_assets.search_query_timeout",
	"backup_assets.search_page_size_max",
	"backup_assets.search_suggestion_limit",
	"backup_assets.saved_search_quota",
	"backup_assets.favorite_quota",
	"backup_assets.tag_definition_quota",
	"backup_assets.tag_assignment_quota",
	"backup_assets.overlay_bulk_max_items",
	"backup_assets.overlay_label_max_bytes",
	"backup_assets.recent_quota",
	"backup_assets.recent_retention",
	"backup_assets.recent_writes_per_minute",
	"backup_assets.idempotency_ttl",
	"backup_assets.idempotency_key_max_bytes",
}

var backupAssetContentSettingKeys = []string{
	"backup_assets.content_preview_ttl",
	"backup_assets.content_media_ttl",
	"backup_assets.content_idle_ttl",
	"backup_assets.content_write_idle_timeout",
	"backup_assets.content_ticket_timeout",
	"backup_assets.content_request_max_bytes",
	"backup_assets.content_cumulative_max_bytes",
	"backup_assets.content_max_requests",
	"backup_assets.content_grant_max_in_flight",
	"backup_assets.content_user_max_concurrency",
	"backup_assets.content_provider_max_concurrency",
	"backup_assets.content_global_max_concurrency",
	"backup_assets.content_rate_window",
	"backup_assets.content_user_window_bytes",
	"backup_assets.content_provider_window_bytes",
	"backup_assets.content_global_window_bytes",
	"backup_assets.content_user_window_requests",
	"backup_assets.content_provider_window_requests",
	"backup_assets.content_global_window_requests",
	"backup_assets.content_classification_scan_bytes",
	"backup_assets.content_text_preview_bytes",
	"backup_assets.content_hex_preview_bytes",
	"backup_assets.content_raster_max_pixels",
	"backup_assets.content_memory_global_bytes",
	"backup_assets.content_memory_object_bytes",
	"backup_assets.content_memory_user_bytes",
	"backup_assets.content_memory_provider_bytes",
	"backup_assets.content_cache_enabled",
	"backup_assets.content_cache_root",
	"backup_assets.content_cache_chunk_bytes",
	"backup_assets.content_cache_object_bytes",
	"backup_assets.content_cache_user_bytes",
	"backup_assets.content_cache_provider_bytes",
	"backup_assets.content_cache_global_bytes",
	"backup_assets.content_cache_object_files",
	"backup_assets.content_cache_user_files",
	"backup_assets.content_cache_provider_files",
	"backup_assets.content_cache_global_files",
	"backup_assets.content_cache_idle_ttl",
	"backup_assets.content_cache_absolute_ttl",
	"backup_assets.content_reconcile_interval",
	"backup_assets.content_reconcile_batch_size",
	"backup_assets.content_audit_backlog_max",
	"backup_assets.content_allow_insecure_loopback",
}

var backupAssetProcessingSettingKeys = []string{
	"backup_assets.processing_queue_max",
	"backup_assets.processing_interactive_slots",
	"backup_assets.processing_background_slots",
	"backup_assets.processing_pull_lease",
	"backup_assets.processing_pull_heartbeat",
	"backup_assets.processing_attempt_timeout",
	"backup_assets.processing_retry_max",
	"backup_assets.processing_retry_base",
	"backup_assets.processing_retry_max_delay",
	"backup_assets.processing_input_request_max_bytes",
	"backup_assets.processing_input_cumulative_max_bytes",
	"backup_assets.processing_input_max_requests",
	"backup_assets.processing_input_max_in_flight",
	"backup_assets.processing_sink_max_artifacts",
	"backup_assets.processing_sink_artifact_max_bytes",
	"backup_assets.processing_sink_total_max_bytes",
	"backup_assets.processing_protocol_json_max_bytes",
	"backup_assets.processing_secret_classify",
	"backup_assets.processing_backfill_paused",
	"backup_assets.processing_backfill_batch_size",
	"backup_assets.processing_backfill_jobs_per_hour",
	"backup_assets.processing_backfill_bytes_per_hour",
	"backup_assets.processing_backfill_provider_concurrency",
	"backup_assets.processing_backfill_capability_concurrency",
	"backup_assets.processing_backfill_recent_window",
	"backup_assets.processing_backfill_history_aging_step",
	"backup_assets.worker_local_enabled",
	"backup_assets.worker_local_socket",
	"backup_assets.worker_remote_enabled",
	"backup_assets.worker_remote_listen_addr",
	"backup_assets.worker_remote_server_cert_file",
	"backup_assets.worker_remote_server_key_file",
	"backup_assets.worker_remote_client_ca_file",
	"backup_assets.worker_remote_trust_domain",
	"backup_assets.worker_updater_enabled",
	"backup_assets.worker_updater_online_enabled",
	"backup_assets.worker_updater_online_origins",
	"backup_assets.derived_store_root",
	"backup_assets.derived_store_chunk_bytes",
	"backup_assets.derived_store_blob_max_bytes",
	"backup_assets.derived_store_global_max_bytes",
	"backup_assets.derived_store_reconcile_interval",
	"backup_assets.derived_store_reconcile_batch_size",
}

var backupAssetExportSettingKeys = []string{
	"backup_assets.export.enabled",
	"backup_assets.export.root",
	"backup_assets.export.default_profile",
	"backup_assets.export.chunk_bytes",
	"backup_assets.export.max_items",
	"backup_assets.export.max_source_points",
	"backup_assets.export.max_item_bytes",
	"backup_assets.export.max_logical_bytes",
	"backup_assets.export.max_provider_bytes",
	"backup_assets.export.max_ciphertext_bytes",
	"backup_assets.export.user_active_jobs",
	"backup_assets.export.global_active_jobs",
	"backup_assets.export.worker_concurrency",
	"backup_assets.export.max_open_readers",
	"backup_assets.export.max_duration",
	"backup_assets.export.max_attempts",
	"backup_assets.export.retry_base",
	"backup_assets.export.retry_max_delay",
	"backup_assets.export.lease_ttl",
	"backup_assets.export.lease_renew_margin",
	"backup_assets.export.ready_ttl",
	"backup_assets.export.summary_ttl",
	"backup_assets.export.ticket_ttl",
	"backup_assets.export.ticket_max_requests",
	"backup_assets.export.ticket_max_in_flight",
	"backup_assets.export.ticket_max_cumulative_bytes",
	"backup_assets.export.user_store_quota",
	"backup_assets.export.store_quota",
	"backup_assets.export.gc_cadence",
	"backup_assets.export.reconcile_batch_size",
	"backup_assets.archive.member_ttl",
	"backup_assets.archive.max_expanded_bytes",
	"backup_assets.archive.member_max_bytes",
	"backup_assets.archive.max_entries",
	"backup_assets.archive.max_depth",
	"backup_assets.archive.max_compression_ratio",
	"backup_assets.archive.max_duration",
}

var backupAssetRecoverySettingKeys = []string{
	"backup_assets.recovery.receipt_replay_ttl",
	"backup_assets.recovery.write_grant_ttl",
	"backup_assets.recovery.delete_grant_ttl",
	"backup_assets.recovery.receipt_reaper_cadence",
	"backup_assets.recovery.receipt_reaper_batch_size",
	"backup_assets.recovery.enabled",
	"backup_assets.recovery.preflight_ttl",
	"backup_assets.recovery.max_selection_items",
	"backup_assets.recovery.max_logical_bytes",
	"backup_assets.recovery.worker_concurrency",
	"backup_assets.recovery.lease_ttl",
	"backup_assets.recovery.lease_renew_margin",
	"backup_assets.recovery.takeover_cadence",
	"backup_assets.recovery.retry_base",
	"backup_assets.recovery.retry_max_delay",
	"backup_assets.recovery.scan_limit",
	"backup_assets.recovery.execution_timeout",
	"backup_assets.recovery.result_default_ttl",
	"backup_assets.recovery.result_retain_hard_cap",
	"backup_assets.recovery.result_read_permit_ttl",
	"backup_assets.recovery.result_drain_timeout",
	"backup_assets.recovery.cleanup_cadence",
	"backup_assets.recovery.cleanup_batch_size",
	"backup_assets.recovery.cleanup_lease_ttl",
	"backup_assets.recovery.cleanup_retry_base",
	"backup_assets.recovery.cleanup_retry_max_delay",
	"backup_assets.recovery.reconciliation_finding_limit",
}

var backupAssetFoundationSettingKeys = func() []string {
	keys := make([]string, 0, len(backupAssetCoreSettingKeys)+len(backupAssetSearchOverlaySettingKeys)+len(backupAssetContentSettingKeys)+len(backupAssetProcessingSettingKeys)+len(backupAssetExportSettingKeys)+len(backupAssetRecoverySettingKeys))
	keys = append(keys, backupAssetCoreSettingKeys...)
	keys = append(keys, backupAssetSearchOverlaySettingKeys...)
	keys = append(keys, backupAssetContentSettingKeys...)
	keys = append(keys, backupAssetProcessingSettingKeys...)
	keys = append(keys, backupAssetExportSettingKeys...)
	keys = append(keys, backupAssetRecoverySettingKeys...)
	return keys
}()

var backupAssetFoundationSettingSet = func() map[string]bool {
	values := make(map[string]bool, len(backupAssetFoundationSettingKeys))
	for _, key := range backupAssetFoundationSettingKeys {
		values[key] = true
	}
	return values
}()

// BackupAssetFoundationSettingKeys returns an immutable-by-convention copy of
// the exact setting set shared by foundation readers and validators.
func BackupAssetFoundationSettingKeys() []string {
	keys := make([]string, len(backupAssetFoundationSettingKeys))
	copy(keys, backupAssetFoundationSettingKeys)
	return keys
}

// BackupAssetCoreSettingKeys returns the pre-Search setting set used by legacy
// typed getters. Search and Overlay must use BackupAssetSettingsSnapshot so
// their coupled limits are read atomically.
func BackupAssetCoreSettingKeys() []string {
	keys := make([]string, len(backupAssetCoreSettingKeys))
	copy(keys, backupAssetCoreSettingKeys)
	return keys
}

func IsBackupAssetFoundationSetting(key string) bool {
	return backupAssetFoundationSettingSet[key]
}

// ValidateBackupAssetFoundationConfig validates cross-setting constraints and
// accepts partial maps only for legacy callers by filling omitted values from
// registry defaults. New mutation paths use explicit full snapshots.
func ValidateBackupAssetFoundationConfig(values map[string]string) error {
	return validateBackupAssetFoundationConfig(values, false)
}

func validateBackupAssetFoundationConfig(values map[string]string, requireComplete bool) error {
	resolved := make(map[string]string, len(backupAssetFoundationSettingKeys))
	for _, key := range backupAssetFoundationSettingKeys {
		def := findDef(key)
		if def == nil {
			return fmt.Errorf("缺少备份资产设置定义: %s", key)
		}
		value, exists := values[key]
		if !exists {
			if requireComplete {
				return fmt.Errorf("缺少备份资产设置值: %s", key)
			}
			value = def.CodeDefault
		}
		if err := validateValue(def, value); err != nil {
			return err
		}
		resolved[key] = value
	}

	leaseDuration, _ := time.ParseDuration(resolved["backup_assets.lease_duration"])
	heartbeat, _ := time.ParseDuration(resolved["backup_assets.lease_heartbeat"])
	absoluteDeadline, _ := time.ParseDuration(resolved["backup_assets.lease_absolute_deadline"])
	missingGrace, _ := time.ParseDuration(resolved["backup_assets.publication_missing_grace"])
	manifestTimeout, _ := time.ParseDuration(resolved["backup_assets.manifest_timeout"])
	manifestMaxBytes, _ := strconv.ParseInt(resolved["backup_assets.manifest_max_bytes"], 10, 64)
	manifestMaxRecordBytes, _ := strconv.ParseInt(resolved["backup_assets.manifest_max_record_bytes"], 10, 64)
	rclonePreflightTTL, _ := time.ParseDuration(resolved["backup_assets.rclone_preflight_ttl"])
	rcloneNativeDeadline, _ := time.ParseDuration(resolved["backup_assets.rclone_native_deadline"])
	rcloneBoundConfigMaxBytes, _ := strconv.ParseInt(resolved["backup_assets.rclone_bound_config_max_bytes"], 10, 64)
	rcloneControlPayloadMaxBytes, _ := strconv.ParseInt(resolved["backup_assets.rclone_control_payload_max_bytes"], 10, 64)
	rcloneManifestChunkMaxBytes, _ := strconv.ParseInt(resolved["backup_assets.rclone_manifest_chunk_max_bytes"], 10, 64)
	searchBuildTimeout, _ := time.ParseDuration(resolved["backup_assets.search_build_timeout"])
	searchASTDepth, _ := strconv.ParseInt(resolved["backup_assets.search_ast_max_depth"], 10, 64)
	searchASTNodes, _ := strconv.ParseInt(resolved["backup_assets.search_ast_max_nodes"], 10, 64)
	searchBodyBytes, _ := strconv.ParseInt(resolved["backup_assets.search_body_max_bytes"], 10, 64)
	searchValueBytes, _ := strconv.ParseInt(resolved["backup_assets.search_value_max_bytes"], 10, 64)
	searchCandidateLimit, _ := strconv.ParseInt(resolved["backup_assets.search_candidate_limit"], 10, 64)
	searchQueryTimeout, _ := time.ParseDuration(resolved["backup_assets.search_query_timeout"])
	searchPageSize, _ := strconv.ParseInt(resolved["backup_assets.search_page_size_max"], 10, 64)
	searchSuggestionLimit, _ := strconv.ParseInt(resolved["backup_assets.search_suggestion_limit"], 10, 64)
	tagAssignmentQuota, _ := strconv.ParseInt(resolved["backup_assets.tag_assignment_quota"], 10, 64)
	overlayBulkMaxItems, _ := strconv.ParseInt(resolved["backup_assets.overlay_bulk_max_items"], 10, 64)
	contentPreviewTTL, _ := time.ParseDuration(resolved["backup_assets.content_preview_ttl"])
	contentMediaTTL, _ := time.ParseDuration(resolved["backup_assets.content_media_ttl"])
	contentIdleTTL, _ := time.ParseDuration(resolved["backup_assets.content_idle_ttl"])
	contentTicketTimeout, _ := time.ParseDuration(resolved["backup_assets.content_ticket_timeout"])
	contentRequestMaxBytes, _ := strconv.ParseInt(resolved["backup_assets.content_request_max_bytes"], 10, 64)
	contentCumulativeMaxBytes, _ := strconv.ParseInt(resolved["backup_assets.content_cumulative_max_bytes"], 10, 64)
	contentGrantMaxInFlight, _ := strconv.ParseInt(resolved["backup_assets.content_grant_max_in_flight"], 10, 64)
	contentUserMaxConcurrency, _ := strconv.ParseInt(resolved["backup_assets.content_user_max_concurrency"], 10, 64)
	contentProviderMaxConcurrency, _ := strconv.ParseInt(resolved["backup_assets.content_provider_max_concurrency"], 10, 64)
	contentGlobalMaxConcurrency, _ := strconv.ParseInt(resolved["backup_assets.content_global_max_concurrency"], 10, 64)
	providerMaxConcurrency, _ := strconv.ParseInt(resolved["backup_assets.provider_max_concurrency"], 10, 64)
	contentUserWindowBytes, _ := strconv.ParseInt(resolved["backup_assets.content_user_window_bytes"], 10, 64)
	contentProviderWindowBytes, _ := strconv.ParseInt(resolved["backup_assets.content_provider_window_bytes"], 10, 64)
	contentGlobalWindowBytes, _ := strconv.ParseInt(resolved["backup_assets.content_global_window_bytes"], 10, 64)
	contentUserWindowRequests, _ := strconv.ParseInt(resolved["backup_assets.content_user_window_requests"], 10, 64)
	contentProviderWindowRequests, _ := strconv.ParseInt(resolved["backup_assets.content_provider_window_requests"], 10, 64)
	contentGlobalWindowRequests, _ := strconv.ParseInt(resolved["backup_assets.content_global_window_requests"], 10, 64)
	contentClassificationScanBytes, _ := strconv.ParseInt(resolved["backup_assets.content_classification_scan_bytes"], 10, 64)
	contentTextPreviewBytes, _ := strconv.ParseInt(resolved["backup_assets.content_text_preview_bytes"], 10, 64)
	contentMemoryObjectBytes, _ := strconv.ParseInt(resolved["backup_assets.content_memory_object_bytes"], 10, 64)
	contentMemoryUserBytes, _ := strconv.ParseInt(resolved["backup_assets.content_memory_user_bytes"], 10, 64)
	contentMemoryProviderBytes, _ := strconv.ParseInt(resolved["backup_assets.content_memory_provider_bytes"], 10, 64)
	contentMemoryGlobalBytes, _ := strconv.ParseInt(resolved["backup_assets.content_memory_global_bytes"], 10, 64)
	contentCacheChunkBytes, _ := strconv.ParseInt(resolved["backup_assets.content_cache_chunk_bytes"], 10, 64)
	contentCacheObjectBytes, _ := strconv.ParseInt(resolved["backup_assets.content_cache_object_bytes"], 10, 64)
	contentCacheUserBytes, _ := strconv.ParseInt(resolved["backup_assets.content_cache_user_bytes"], 10, 64)
	contentCacheProviderBytes, _ := strconv.ParseInt(resolved["backup_assets.content_cache_provider_bytes"], 10, 64)
	contentCacheGlobalBytes, _ := strconv.ParseInt(resolved["backup_assets.content_cache_global_bytes"], 10, 64)
	contentCacheObjectFiles, _ := strconv.ParseInt(resolved["backup_assets.content_cache_object_files"], 10, 64)
	contentCacheUserFiles, _ := strconv.ParseInt(resolved["backup_assets.content_cache_user_files"], 10, 64)
	contentCacheProviderFiles, _ := strconv.ParseInt(resolved["backup_assets.content_cache_provider_files"], 10, 64)
	contentCacheGlobalFiles, _ := strconv.ParseInt(resolved["backup_assets.content_cache_global_files"], 10, 64)
	contentCacheIdleTTL, _ := time.ParseDuration(resolved["backup_assets.content_cache_idle_ttl"])
	contentCacheAbsoluteTTL, _ := time.ParseDuration(resolved["backup_assets.content_cache_absolute_ttl"])
	processingPullLease, _ := time.ParseDuration(resolved["backup_assets.processing_pull_lease"])
	processingPullHeartbeat, _ := time.ParseDuration(resolved["backup_assets.processing_pull_heartbeat"])
	processingAttemptTimeout, _ := time.ParseDuration(resolved["backup_assets.processing_attempt_timeout"])
	processingRetryBase, _ := time.ParseDuration(resolved["backup_assets.processing_retry_base"])
	processingRetryMaxDelay, _ := time.ParseDuration(resolved["backup_assets.processing_retry_max_delay"])
	processingInputRequestMaxBytes, _ := strconv.ParseInt(resolved["backup_assets.processing_input_request_max_bytes"], 10, 64)
	processingInputCumulativeMaxBytes, _ := strconv.ParseInt(resolved["backup_assets.processing_input_cumulative_max_bytes"], 10, 64)
	processingSinkArtifactMaxBytes, _ := strconv.ParseInt(resolved["backup_assets.processing_sink_artifact_max_bytes"], 10, 64)
	processingSinkTotalMaxBytes, _ := strconv.ParseInt(resolved["backup_assets.processing_sink_total_max_bytes"], 10, 64)
	derivedStoreChunkBytes, _ := strconv.ParseInt(resolved["backup_assets.derived_store_chunk_bytes"], 10, 64)
	derivedStoreBlobMaxBytes, _ := strconv.ParseInt(resolved["backup_assets.derived_store_blob_max_bytes"], 10, 64)
	derivedStoreGlobalMaxBytes, _ := strconv.ParseInt(resolved["backup_assets.derived_store_global_max_bytes"], 10, 64)
	exportChunkBytes, _ := strconv.ParseInt(resolved["backup_assets.export.chunk_bytes"], 10, 64)
	exportMaxItems, _ := strconv.ParseInt(resolved["backup_assets.export.max_items"], 10, 64)
	exportMaxSourcePoints, _ := strconv.ParseInt(resolved["backup_assets.export.max_source_points"], 10, 64)
	exportMaxItemBytes, _ := strconv.ParseInt(resolved["backup_assets.export.max_item_bytes"], 10, 64)
	exportMaxLogicalBytes, _ := strconv.ParseInt(resolved["backup_assets.export.max_logical_bytes"], 10, 64)
	exportMaxProviderBytes, _ := strconv.ParseInt(resolved["backup_assets.export.max_provider_bytes"], 10, 64)
	exportMaxCiphertextBytes, _ := strconv.ParseInt(resolved["backup_assets.export.max_ciphertext_bytes"], 10, 64)
	exportUserActiveJobs, _ := strconv.ParseInt(resolved["backup_assets.export.user_active_jobs"], 10, 64)
	exportGlobalActiveJobs, _ := strconv.ParseInt(resolved["backup_assets.export.global_active_jobs"], 10, 64)
	exportRetryBase, _ := time.ParseDuration(resolved["backup_assets.export.retry_base"])
	exportRetryMaxDelay, _ := time.ParseDuration(resolved["backup_assets.export.retry_max_delay"])
	exportLeaseTTL, _ := time.ParseDuration(resolved["backup_assets.export.lease_ttl"])
	exportLeaseRenewMargin, _ := time.ParseDuration(resolved["backup_assets.export.lease_renew_margin"])
	exportTicketMaxCumulativeBytes, _ := strconv.ParseInt(resolved["backup_assets.export.ticket_max_cumulative_bytes"], 10, 64)
	exportUserStoreQuota, _ := strconv.ParseInt(resolved["backup_assets.export.user_store_quota"], 10, 64)
	exportStoreQuota, _ := strconv.ParseInt(resolved["backup_assets.export.store_quota"], 10, 64)
	archiveMaxExpandedBytes, _ := strconv.ParseInt(resolved["backup_assets.archive.max_expanded_bytes"], 10, 64)
	archiveMemberMaxBytes, _ := strconv.ParseInt(resolved["backup_assets.archive.member_max_bytes"], 10, 64)
	archiveMaxEntries, _ := strconv.ParseInt(resolved["backup_assets.archive.max_entries"], 10, 64)
	archiveMaxDepth, _ := strconv.ParseInt(resolved["backup_assets.archive.max_depth"], 10, 64)
	archiveMaxCompressionRatio, _ := strconv.ParseInt(resolved["backup_assets.archive.max_compression_ratio"], 10, 64)
	archiveMaxDuration, _ := time.ParseDuration(resolved["backup_assets.archive.max_duration"])
	recoveryReceiptReplayTTL, _ := time.ParseDuration(resolved["backup_assets.recovery.receipt_replay_ttl"])
	recoveryWriteGrantTTL, _ := time.ParseDuration(resolved["backup_assets.recovery.write_grant_ttl"])
	recoveryDeleteGrantTTL, _ := time.ParseDuration(resolved["backup_assets.recovery.delete_grant_ttl"])
	recoveryLeaseTTL, _ := time.ParseDuration(resolved["backup_assets.recovery.lease_ttl"])
	recoveryLeaseRenewMargin, _ := time.ParseDuration(resolved["backup_assets.recovery.lease_renew_margin"])
	recoveryRetryBase, _ := time.ParseDuration(resolved["backup_assets.recovery.retry_base"])
	recoveryRetryMaxDelay, _ := time.ParseDuration(resolved["backup_assets.recovery.retry_max_delay"])
	recoveryResultDefaultTTL, _ := time.ParseDuration(resolved["backup_assets.recovery.result_default_ttl"])
	recoveryResultRetainHardCap, _ := time.ParseDuration(resolved["backup_assets.recovery.result_retain_hard_cap"])
	recoveryResultDrainTimeout, _ := time.ParseDuration(resolved["backup_assets.recovery.result_drain_timeout"])
	recoveryCleanupLeaseTTL, _ := time.ParseDuration(resolved["backup_assets.recovery.cleanup_lease_ttl"])
	recoveryCleanupRetryBase, _ := time.ParseDuration(resolved["backup_assets.recovery.cleanup_retry_base"])
	recoveryCleanupRetryMaxDelay, _ := time.ParseDuration(resolved["backup_assets.recovery.cleanup_retry_max_delay"])
	if heartbeat >= leaseDuration {
		return fmt.Errorf("backup_assets.lease_heartbeat 必须小于 backup_assets.lease_duration")
	}
	if leaseDuration-heartbeat <= sshutil.CommandExecutionJoinTimeout {
		return fmt.Errorf("backup_assets.lease_duration 与 backup_assets.lease_heartbeat 之间必须大于命令收尾时限")
	}
	if absoluteDeadline < leaseDuration {
		return fmt.Errorf("backup_assets.lease_absolute_deadline 不能小于 backup_assets.lease_duration")
	}
	if missingGrace < leaseDuration || missingGrace >= absoluteDeadline {
		return fmt.Errorf("backup_assets.publication_missing_grace 必须不小于租约时长且小于绝对截止时间")
	}
	if manifestTimeout >= absoluteDeadline {
		return fmt.Errorf("backup_assets.manifest_timeout 必须小于绝对截止时间")
	}
	if manifestMaxRecordBytes > manifestMaxBytes {
		return fmt.Errorf("backup_assets.manifest_max_record_bytes 不能大于 manifest_max_bytes")
	}
	if rclonePreflightTTL <= 15*time.Minute+sshutil.CommandExecutionJoinTimeout {
		return fmt.Errorf("backup_assets.rclone_preflight_ttl 必须覆盖 15 分钟稳定观察与命令收尾余量")
	}
	if rcloneNativeDeadline > 55*time.Minute {
		return fmt.Errorf("backup_assets.rclone_native_deadline 必须为 STS role-chain 保留安全余量")
	}
	if rcloneBoundConfigMaxBytes > sshutil.MaximumSecretStdinBytes {
		return fmt.Errorf("backup_assets.rclone_bound_config_max_bytes 不能超过 SecretStdin 上限")
	}
	if rcloneManifestChunkMaxBytes > rcloneControlPayloadMaxBytes {
		return fmt.Errorf("backup_assets.rclone_manifest_chunk_max_bytes 不能超过控制对象暂存上限")
	}
	if searchASTNodes < searchASTDepth {
		return fmt.Errorf("backup_assets.search_ast_max_nodes 不能小于 AST 最大深度")
	}
	if searchBodyBytes < searchValueBytes {
		return fmt.Errorf("backup_assets.search_body_max_bytes 不能小于单值字节上限")
	}
	if searchCandidateLimit < searchPageSize {
		return fmt.Errorf("backup_assets.search_candidate_limit 不能小于单页条目上限")
	}
	if searchPageSize < searchSuggestionLimit {
		return fmt.Errorf("backup_assets.search_page_size_max 不能小于建议上限")
	}
	if tagAssignmentQuota < overlayBulkMaxItems {
		return fmt.Errorf("backup_assets.tag_assignment_quota 不能小于批量操作上限")
	}
	if searchBuildTimeout > absoluteDeadline {
		return fmt.Errorf("backup_assets.search_build_timeout 不能超过租约绝对截止时间")
	}
	if searchQueryTimeout > 30*time.Second {
		return fmt.Errorf("backup_assets.search_query_timeout 不能超过服务器写超时")
	}
	if recoveryWriteGrantTTL > recoveryReceiptReplayTTL {
		return fmt.Errorf("backup_assets.recovery.write_grant_ttl 不能超过 receipt_replay_ttl")
	}
	if recoveryDeleteGrantTTL > recoveryReceiptReplayTTL {
		return fmt.Errorf("backup_assets.recovery.delete_grant_ttl 不能超过 receipt_replay_ttl")
	}
	if recoveryLeaseRenewMargin >= recoveryLeaseTTL {
		return fmt.Errorf("backup_assets.recovery.lease_renew_margin 必须小于 lease_ttl")
	}
	if recoveryRetryBase > recoveryRetryMaxDelay {
		return fmt.Errorf("backup_assets.recovery.retry_base 不能超过 retry_max_delay")
	}
	if recoveryResultDefaultTTL > recoveryResultRetainHardCap {
		return fmt.Errorf("backup_assets.recovery.result_default_ttl 不能超过 result_retain_hard_cap")
	}
	if recoveryResultDrainTimeout >= recoveryCleanupLeaseTTL {
		return fmt.Errorf("backup_assets.recovery.result_drain_timeout 必须小于 cleanup_lease_ttl")
	}
	if recoveryCleanupRetryBase > recoveryCleanupRetryMaxDelay {
		return fmt.Errorf("backup_assets.recovery.cleanup_retry_base 不能超过 cleanup_retry_max_delay")
	}
	validateContent := requireComplete
	if !validateContent {
		for _, key := range backupAssetContentSettingKeys {
			if _, exists := values[key]; exists {
				validateContent = true
				break
			}
		}
	}
	if !validateContent {
		return nil
	}
	if contentIdleTTL > contentPreviewTTL || contentIdleTTL > contentMediaTTL {
		return fmt.Errorf("backup_assets.content_idle_ttl 不能超过内容绝对有效期")
	}
	if contentTicketTimeout >= 30*time.Second {
		return fmt.Errorf("backup_assets.content_ticket_timeout 必须小于服务器写超时")
	}
	if contentRequestMaxBytes > contentCumulativeMaxBytes {
		return fmt.Errorf("backup_assets.content_request_max_bytes 不能超过累计字节上限")
	}
	if contentGrantMaxInFlight > contentUserMaxConcurrency {
		return fmt.Errorf("backup_assets.content_grant_max_in_flight 不能超过用户并发上限")
	}
	if contentProviderMaxConcurrency > providerMaxConcurrency {
		return fmt.Errorf("backup_assets.content_provider_max_concurrency 不能超过 Provider admission 上限")
	}
	if contentGlobalMaxConcurrency < contentUserMaxConcurrency || contentGlobalMaxConcurrency < contentProviderMaxConcurrency {
		return fmt.Errorf("backup_assets.content_global_max_concurrency 不能小于用户或 Provider 并发上限")
	}
	if contentUserWindowBytes < contentRequestMaxBytes || contentProviderWindowBytes < contentRequestMaxBytes ||
		contentGlobalWindowBytes < contentProviderWindowBytes || contentGlobalWindowBytes < contentUserWindowBytes {
		return fmt.Errorf("备份内容窗口字节预算顺序无效")
	}
	if contentProviderWindowRequests < contentUserWindowRequests || contentGlobalWindowRequests < contentProviderWindowRequests {
		return fmt.Errorf("备份内容窗口请求预算顺序无效")
	}
	if contentClassificationScanBytes > contentTextPreviewBytes {
		return fmt.Errorf("backup_assets.content_classification_scan_bytes 不能超过文本预览上限")
	}
	if contentMemoryObjectBytes > contentMemoryUserBytes || contentMemoryObjectBytes > contentMemoryProviderBytes ||
		contentMemoryUserBytes > contentMemoryGlobalBytes || contentMemoryProviderBytes > contentMemoryGlobalBytes {
		return fmt.Errorf("备份内容内存配额顺序无效")
	}
	if contentCacheChunkBytes > contentCacheObjectBytes || contentCacheObjectBytes > contentCacheUserBytes ||
		contentCacheObjectBytes > contentCacheProviderBytes || contentCacheUserBytes > contentCacheGlobalBytes ||
		contentCacheProviderBytes > contentCacheGlobalBytes {
		return fmt.Errorf("备份内容磁盘缓存字节配额顺序无效")
	}
	requiredObjectFiles := contentCacheObjectBytes/contentCacheChunkBytes + 1
	if contentCacheObjectBytes%contentCacheChunkBytes != 0 {
		requiredObjectFiles++
	}
	if contentCacheObjectFiles < requiredObjectFiles || contentCacheObjectFiles > contentCacheUserFiles ||
		contentCacheObjectFiles > contentCacheProviderFiles || contentCacheUserFiles > contentCacheGlobalFiles ||
		contentCacheProviderFiles > contentCacheGlobalFiles {
		return fmt.Errorf("备份内容磁盘缓存文件配额顺序无效")
	}
	if contentCacheIdleTTL > contentCacheAbsoluteTTL {
		return fmt.Errorf("backup_assets.content_cache_idle_ttl 不能超过缓存绝对有效期")
	}
	if !safeContentCacheRoot(resolved["backup_assets.content_cache_root"]) {
		return fmt.Errorf("backup_assets.content_cache_root 不是安全的专用绝对路径")
	}
	if processingPullHeartbeat*2 >= processingPullLease {
		return fmt.Errorf("backup_assets.processing_pull_heartbeat 必须小于 processing_pull_lease 的一半")
	}
	if processingAttemptTimeout > absoluteDeadline {
		return fmt.Errorf("backup_assets.processing_attempt_timeout 不能超过 RecoveryPoint 租约绝对截止时间")
	}
	if processingRetryMaxDelay < processingRetryBase {
		return fmt.Errorf("backup_assets.processing_retry_max_delay 不能小于 processing_retry_base")
	}
	if processingInputCumulativeMaxBytes < processingInputRequestMaxBytes {
		return fmt.Errorf("backup_assets.processing_input_cumulative_max_bytes 不能小于单次读取上限")
	}
	if processingSinkTotalMaxBytes < processingSinkArtifactMaxBytes {
		return fmt.Errorf("backup_assets.processing_sink_total_max_bytes 不能小于单产物上限")
	}
	if derivedStoreBlobMaxBytes < derivedStoreChunkBytes || derivedStoreGlobalMaxBytes < derivedStoreBlobMaxBytes {
		return fmt.Errorf("派生资产分块、单 blob 与全局字节配额顺序无效")
	}
	if !cleanAbsolutePath(resolved["backup_assets.worker_local_socket"]) {
		return fmt.Errorf("backup_assets.worker_local_socket 不是 clean absolute path")
	}
	derivedRoot := resolved["backup_assets.derived_store_root"]
	if !safePrivateRuntimeRootPath(derivedRoot) {
		return fmt.Errorf("backup_assets.derived_store_root 不是安全的专用绝对路径")
	}
	if privateRuntimePathsRelated(derivedRoot, resolved["backup_assets.content_cache_root"]) {
		return fmt.Errorf("backup_assets.derived_store_root 不能与内容缓存根目录重叠")
	}
	if exportMaxSourcePoints > exportMaxItems {
		return fmt.Errorf("backup_assets.export.max_source_points 不能超过 max_items")
	}
	if exportMaxItemBytes > exportMaxLogicalBytes {
		return fmt.Errorf("backup_assets.export.max_item_bytes 不能超过 max_logical_bytes")
	}
	if exportMaxProviderBytes < exportMaxLogicalBytes {
		return fmt.Errorf("backup_assets.export.max_provider_bytes 不能小于 max_logical_bytes")
	}
	minimumCiphertextBytes, ok := backupAssetExportMinimumCiphertextBytesV1(
		exportMaxLogicalBytes, exportMaxItems, exportChunkBytes,
	)
	if !ok || exportMaxCiphertextBytes < minimumCiphertextBytes {
		return fmt.Errorf("backup_assets.export.max_ciphertext_bytes 未覆盖逻辑内容与归档开销")
	}
	if exportTicketMaxCumulativeBytes < exportMaxCiphertextBytes {
		return fmt.Errorf("backup_assets.export.ticket_max_cumulative_bytes 不能小于 max_ciphertext_bytes")
	}
	if exportUserStoreQuota > exportStoreQuota {
		return fmt.Errorf("backup_assets.export.user_store_quota 不能超过 store_quota")
	}
	maximumStorePeakBytes, ok := BackupAssetExportMaximumStorePeakV1(
		exportMaxCiphertextBytes, exportMaxItems, exportMaxItemBytes, exportMaxLogicalBytes, exportChunkBytes,
	)
	if !ok {
		return fmt.Errorf("backup_assets.export 存储峰值无法安全计算")
	}
	if exportUserStoreQuota < maximumStorePeakBytes {
		return fmt.Errorf("backup_assets.export.user_store_quota 未覆盖归档与全部合法 spool")
	}
	if exportStoreQuota < maximumStorePeakBytes {
		return fmt.Errorf("backup_assets.export.store_quota 未覆盖归档与全部合法 spool")
	}
	if exportUserActiveJobs > exportGlobalActiveJobs {
		return fmt.Errorf("backup_assets.export.user_active_jobs 不能超过 global_active_jobs")
	}
	if exportRetryBase > exportRetryMaxDelay {
		return fmt.Errorf("backup_assets.export.retry_base 不能超过 retry_max_delay")
	}
	if exportLeaseRenewMargin*2 >= exportLeaseTTL {
		return fmt.Errorf("backup_assets.export.lease_renew_margin 必须小于 lease_ttl 的一半")
	}
	if archiveMemberMaxBytes > archiveMaxExpandedBytes {
		return fmt.Errorf("backup_assets.archive.member_max_bytes 不能超过 max_expanded_bytes")
	}
	if archiveMaxExpandedBytes > 8<<30 || archiveMemberMaxBytes > 256<<20 || archiveMaxEntries > 100000 ||
		archiveMaxDepth > 16 || archiveMaxCompressionRatio > 100 || archiveMaxDuration > 10*time.Minute {
		return fmt.Errorf("backup_assets.archive 限制不能放宽 Worker archive hard caps")
	}
	switch resolved["backup_assets.export.default_profile"] {
	case "zip_deflate_v1", "tar_none_v1", "tar_gzip_v1":
	default:
		return fmt.Errorf("backup_assets.export.default_profile 不在允许列表")
	}
	exportRoot := resolved["backup_assets.export.root"]
	if !safePrivateRuntimeRootPath(exportRoot) {
		return fmt.Errorf("backup_assets.export.root 不是安全的专用绝对路径")
	}
	if privateRuntimePathsRelated(exportRoot, resolved["backup_assets.content_cache_root"]) ||
		privateRuntimePathsRelated(exportRoot, derivedRoot) {
		return fmt.Errorf("backup_assets.export.root 不能与 Content 或 Derived 根目录重叠")
	}
	if err := validateRemoteWorkerSettings(resolved); err != nil {
		return err
	}
	if err := validateUpdaterSettings(resolved); err != nil {
		return err
	}
	return nil
}

const (
	backupAssetExportV1MinimumChunkBytes int64 = 64 << 10
	backupAssetExportV1MaximumChunkBytes int64 = 8 << 20
	backupAssetExportV1RecordOverhead    int64 = 20
	backupAssetExportV1FixedOverhead     int64 = 20 + 68
)

// BackupAssetExportMaximumStorePeakV1 returns the largest V1 final-artifact
// plus regular-spool peak allowed by the supplied immutable Export limits.
// The boolean is false for invalid limits or any counter/size overflow.
func BackupAssetExportMaximumStorePeakV1(
	archiveCiphertextBytes, maxItems, maxItemBytes, maxLogicalBytes, chunkBytes int64,
) (int64, bool) {
	if archiveCiphertextBytes < 0 || maxItems <= 0 || maxItemBytes <= 0 || maxLogicalBytes <= 0 ||
		chunkBytes <= 0 || chunkBytes > backupAssetExportV1MaximumChunkBytes {
		return 0, false
	}

	totalLogicalBytes := maxLogicalBytes
	if maxItemBytes <= math.MaxInt64/maxItems {
		if itemCapacity := maxItemBytes * maxItems; itemCapacity < totalLogicalBytes {
			totalLogicalBytes = itemCapacity
		}
	}
	maxRegularLogicalBytes := min(maxItemBytes, totalLogicalBytes)
	if _, ok := backupAssetExportV1CiphertextSize(maxRegularLogicalBytes, chunkBytes); !ok {
		return 0, false
	}

	if maxItems > math.MaxInt64/backupAssetExportV1FixedOverhead {
		return 0, false
	}
	peakStoreBytes := maxItems * backupAssetExportV1FixedOverhead
	if archiveCiphertextBytes > math.MaxInt64-peakStoreBytes {
		return 0, false
	}
	peakStoreBytes += archiveCiphertextBytes
	if totalLogicalBytes > math.MaxInt64-peakStoreBytes {
		return 0, false
	}
	peakStoreBytes += totalLogicalBytes

	// Every positive regular item opens its first authenticated record. Each
	// additional record costs exactly one full chunk after that first byte.
	activeItems := min(maxItems, totalLogicalBytes)
	remainingLogicalBytes := totalLogicalBytes - activeItems
	additionalChunks := remainingLogicalBytes / chunkBytes
	additionalChunksPerItem := (maxItemBytes - 1) / chunkBytes
	if additionalChunksPerItem <= additionalChunks/activeItems {
		additionalChunks = additionalChunksPerItem * activeItems
	}
	chunkCount := activeItems + additionalChunks
	if chunkCount > math.MaxInt64/backupAssetExportV1RecordOverhead ||
		chunkCount*backupAssetExportV1RecordOverhead > math.MaxInt64-peakStoreBytes {
		return 0, false
	}
	return peakStoreBytes + chunkCount*backupAssetExportV1RecordOverhead, true
}

func backupAssetExportCiphertextSizeV1(plaintextBytes, chunkBytes int64) (int64, bool) {
	if chunkBytes < backupAssetExportV1MinimumChunkBytes {
		return 0, false
	}
	return backupAssetExportV1CiphertextSize(plaintextBytes, chunkBytes)
}

func backupAssetExportV1CiphertextSize(plaintextBytes, chunkBytes int64) (int64, bool) {
	if plaintextBytes < 0 || chunkBytes <= 0 || chunkBytes > backupAssetExportV1MaximumChunkBytes {
		return 0, false
	}
	if plaintextBytes == 0 {
		return backupAssetExportV1FixedOverhead, true
	}
	chunkCount := 1 + (plaintextBytes-1)/chunkBytes
	if chunkCount > int64(math.MaxUint32) || plaintextBytes > math.MaxInt64-backupAssetExportV1FixedOverhead {
		return 0, false
	}
	remaining := math.MaxInt64 - plaintextBytes - backupAssetExportV1FixedOverhead
	if chunkCount > remaining/backupAssetExportV1RecordOverhead {
		return 0, false
	}
	return plaintextBytes + chunkCount*backupAssetExportV1RecordOverhead + backupAssetExportV1FixedOverhead, true
}

func backupAssetExportMinimumCiphertextBytesV1(logicalBytes, maxItems, chunkBytes int64) (int64, bool) {
	archivePlaintextBytes, ok := backupAssetExportArchivePlaintextUpperBoundV1(logicalBytes, maxItems)
	if !ok {
		return 0, false
	}
	return backupAssetExportCiphertextSizeV1(archivePlaintextBytes, chunkBytes)
}

func backupAssetExportArchivePlaintextUpperBoundV1(logicalBytes, maxItems int64) (int64, bool) {
	const (
		archiveFixedOverheadBytes int64 = 64 << 20
		archiveMemberPathBytes    int64 = 4096
		// Reserve duplicated ZIP/PAX names and escaped report paths plus framing.
		archivePerItemOverheadBytes          = 16 * archiveMemberPathBytes
		archiveCompressionSlackDivisor int64 = 8
	)
	if logicalBytes <= 0 || maxItems <= 0 || maxItems > math.MaxInt64/archivePerItemOverheadBytes {
		return 0, false
	}
	compressionSlack := logicalBytes / archiveCompressionSlackDivisor
	if logicalBytes%archiveCompressionSlackDivisor != 0 {
		compressionSlack++
	}
	if logicalBytes > math.MaxInt64-compressionSlack {
		return 0, false
	}
	archivePlaintextBytes := logicalBytes + compressionSlack
	perItemOverheadBytes := maxItems * archivePerItemOverheadBytes
	if perItemOverheadBytes > math.MaxInt64-archivePlaintextBytes {
		return 0, false
	}
	archivePlaintextBytes += perItemOverheadBytes
	if archiveFixedOverheadBytes > math.MaxInt64-archivePlaintextBytes {
		return 0, false
	}
	return archivePlaintextBytes + archiveFixedOverheadBytes, true
}

func safeContentCacheRoot(value string) bool {
	return safePrivateRuntimeRootPath(value)
}

func safePrivateRuntimeRootPath(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || trimmed != value || !filepath.IsAbs(trimmed) || filepath.Clean(trimmed) != trimmed || trimmed == string(filepath.Separator) {
		return false
	}
	for _, forbidden := range []string{"/data", "/backup", "/logs"} {
		if trimmed == forbidden || strings.HasPrefix(trimmed, forbidden+string(filepath.Separator)) ||
			strings.HasPrefix(forbidden, trimmed+string(filepath.Separator)) {
			return false
		}
	}
	return true
}

func cleanAbsolutePath(value string) bool {
	trimmed := strings.TrimSpace(value)
	return trimmed != "" && trimmed == value && filepath.IsAbs(trimmed) && filepath.Clean(trimmed) == trimmed && trimmed != string(filepath.Separator)
}

func privateRuntimePathsRelated(left, right string) bool {
	if !cleanAbsolutePath(left) || !cleanAbsolutePath(right) {
		return false
	}
	left, right = filepath.Clean(left), filepath.Clean(right)
	separator := string(filepath.Separator)
	return left == right || strings.HasPrefix(left, right+separator) || strings.HasPrefix(right, left+separator)
}

func validateRemoteWorkerSettings(values map[string]string) error {
	remoteEnabled, _ := strconv.ParseBool(values["backup_assets.worker_remote_enabled"])
	keys := []string{
		"backup_assets.worker_remote_listen_addr",
		"backup_assets.worker_remote_server_cert_file",
		"backup_assets.worker_remote_server_key_file",
		"backup_assets.worker_remote_client_ca_file",
		"backup_assets.worker_remote_trust_domain",
	}
	configured := remoteEnabled
	for _, key := range keys {
		configured = configured || strings.TrimSpace(values[key]) != ""
	}
	if !configured {
		return nil
	}
	for _, key := range keys {
		if strings.TrimSpace(values[key]) == "" {
			return fmt.Errorf("远程资产 Worker trust material 必须完整")
		}
	}
	if !safeWorkerListenAddress(values["backup_assets.worker_remote_listen_addr"]) {
		return fmt.Errorf("backup_assets.worker_remote_listen_addr 必须是非 wildcard 地址")
	}
	for _, key := range []string{
		"backup_assets.worker_remote_server_cert_file",
		"backup_assets.worker_remote_server_key_file",
		"backup_assets.worker_remote_client_ca_file",
	} {
		if !cleanAbsolutePath(values[key]) {
			return fmt.Errorf("%s 不是 clean absolute path", key)
		}
	}
	if !canonicalWorkerTrustDomain(values["backup_assets.worker_remote_trust_domain"]) {
		return fmt.Errorf("backup_assets.worker_remote_trust_domain 不是 canonical trust domain")
	}
	return nil
}

func validateUpdaterSettings(values map[string]string) error {
	enabled, _ := strconv.ParseBool(values["backup_assets.worker_updater_enabled"])
	online, _ := strconv.ParseBool(values["backup_assets.worker_updater_online_enabled"])
	if !online {
		return nil
	}
	if !enabled || !validUpdaterOriginList(values["backup_assets.worker_updater_online_origins"]) {
		return fmt.Errorf("backup_assets updater 在线配置无效")
	}
	return nil
}

func validUpdaterOriginList(value string) bool {
	if value == "" || strings.TrimSpace(value) != value {
		return false
	}
	origins := strings.Split(value, ",")
	if len(origins) == 0 || len(origins) > 32 {
		return false
	}
	last := ""
	for _, origin := range origins {
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Host == "" || parsed.Port() == "" ||
			parsed.Path != "" || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.String() != origin ||
			strings.ToLower(parsed.Host) != parsed.Host || origin <= last || !validUpdaterOriginHost(parsed.Hostname()) {
			return false
		}
		port, err := strconv.ParseUint(parsed.Port(), 10, 16)
		if err != nil || port == 0 {
			return false
		}
		last = origin
	}
	return true
}

func validUpdaterOriginHost(value string) bool {
	if ip := net.ParseIP(value); ip != nil {
		return !ip.IsUnspecified() && !ip.IsMulticast()
	}
	if value == "" || len(value) > 253 || strings.ToLower(value) != value || strings.ContainsAny(value, "*/:@/?#\\\x00") {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-' {
				continue
			}
			return false
		}
	}
	return true
}

func safeWorkerListenAddress(value string) bool {
	if strings.TrimSpace(value) != value || value == "" {
		return false
	}
	host, portText, err := net.SplitHostPort(value)
	if err != nil || host == "" || host == "*" {
		return false
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return false
	}
	if address := net.ParseIP(host); address != nil && address.IsUnspecified() {
		return false
	}
	return true
}

func canonicalWorkerTrustDomain(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || value != strings.ToLower(value) || len(value) > 253 || net.ParseIP(value) != nil {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
				return false
			}
		}
	}
	return true
}
