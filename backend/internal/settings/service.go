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
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

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

// Service 系统设置服务
type Service struct {
	db                    *gorm.DB
	mu                    sync.RWMutex
	backupAssetMutationMu sync.Mutex
	cache                 map[string]cachedValue
}

// NewService 创建设置服务
func NewService(db *gorm.DB) *Service {
	return &Service{
		db:    db,
		cache: make(map[string]cachedValue),
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

// GetFallback resolves a known setting from environment or its code default,
// intentionally excluding any database override.
func (s *Service) GetFallback(key string) (string, error) {
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
	s.backupAssetMutationMu.Lock()
	defer s.backupAssetMutationMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	current, err := s.backupAssetFoundationSnapshot()
	if err != nil {
		return err
	}
	return callback(copyStringMap(current))
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
	if err := s.Validate(key, value); err != nil {
		return err
	}
	if err := s.upsert(s.db, key, value); err != nil {
		return err
	}
	s.invalidateCache(key)
	return nil
}

// UpdateWithTx 在指定事务内更新设置值（供 config import 使用）
func (s *Service) UpdateWithTx(tx *gorm.DB, key, value string) error {
	if err := s.Validate(key, value); err != nil {
		return err
	}
	if err := s.upsert(tx, key, value); err != nil {
		return err
	}
	s.invalidateCache(key)
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
	def := findDef(key)
	if def == nil {
		return fmt.Errorf("未知的设置项: %s", key)
	}
	if err := s.DeleteWithTx(s.db, key); err != nil {
		return err
	}
	return nil
}

// DeleteWithTx removes one database override through the caller's existing
// transaction. Foundation-setting callers use it only after the admission
// transition has drained, so a rollback leaves the prior persisted value.
func (s *Service) DeleteWithTx(tx *gorm.DB, key string) error {
	def := findDef(key)
	if def == nil {
		return fmt.Errorf("未知的设置项: %s", key)
	}
	if tx == nil {
		return fmt.Errorf("settings transaction is unavailable")
	}
	if err := tx.Where("key = ?", key).Delete(&model.SystemSetting{}).Error; err != nil {
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

var backupAssetFoundationSettingKeys = []string{
	"backup_assets.enabled",
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
}

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
	return nil
}
