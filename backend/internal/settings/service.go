package settings

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"xirang/backend/internal/model"

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
	RequiresRestart bool        `json:"requires_restart"`
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
	db    *gorm.DB
	mu    sync.RWMutex
	cache map[string]cachedValue
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
	{Key: "login.rate_limit", EnvVar: "LOGIN_RATE_LIMIT", CodeDefault: "10", Type: TypeInt, Category: "security", Description: "登录接口每窗口最大请求数", Min: "5", Max: "1000"},
	{Key: "login.rate_window", EnvVar: "LOGIN_RATE_WINDOW", CodeDefault: "1m", Type: TypeDuration, Category: "security", Description: "登录限流时间窗口", MinDuration: "10s"},
	{Key: "login.fail_lock_threshold", EnvVar: "LOGIN_FAIL_LOCK_THRESHOLD", CodeDefault: "5", Type: TypeInt, Category: "security", Description: "连续登录失败锁定阈值", Min: "3", Max: "100"},
	{Key: "login.fail_lock_duration", EnvVar: "LOGIN_FAIL_LOCK_DURATION", CodeDefault: "15m", Type: TypeDuration, Category: "security", Description: "登录锁定持续时间", MinDuration: "1m"},
	{Key: "login.captcha_enabled", EnvVar: "LOGIN_CAPTCHA_ENABLED", CodeDefault: "false", Type: TypeBool, Category: "security", Description: "启用登录验证码"},
	{Key: "login.second_captcha_enabled", EnvVar: "LOGIN_SECOND_CAPTCHA_ENABLED", CodeDefault: "false", Type: TypeBool, Category: "security", Description: "启用登录二次验证码"},
	{Key: "node.probe_interval", EnvVar: "NODE_PROBE_INTERVAL", CodeDefault: "5m", Type: TypeDuration, Category: "node_monitor", Description: "节点探测间隔", RequiresRestart: true},
	{Key: "node.probe_fail_threshold", EnvVar: "NODE_PROBE_FAIL_THRESHOLD", CodeDefault: "3", Type: TypeInt, Category: "node_monitor", Description: "节点探测失败阈值", Min: "1", Max: "100", RequiresRestart: true},
	{Key: "node.probe_concurrency", EnvVar: "NODE_PROBE_CONCURRENCY", CodeDefault: "10", Type: TypeInt, Category: "node_monitor", Description: "节点探测并发数", Min: "1", Max: "100", RequiresRestart: true},
	{Key: "retention.task_traffic_days", EnvVar: "TASK_TRAFFIC_RETENTION_DAYS", CodeDefault: "8", Type: TypeInt, Category: "retention", Description: "任务流量数据保留天数", Min: "1", Max: "365"},
	{Key: "retention.task_run_days", EnvVar: "TASK_RUN_RETENTION_DAYS", CodeDefault: "90", Type: TypeInt, Category: "retention", Description: "任务执行记录保留天数", Min: "1", Max: "3650"},
	{Key: "retention.check_interval", EnvVar: "RETENTION_CHECK_INTERVAL", CodeDefault: "6h", Type: TypeDuration, Category: "retention", Description: "保留策略检查间隔"},
	{Key: "storage.min_free_gb", EnvVar: "BACKUP_STORAGE_MIN_FREE_GB", CodeDefault: "10", Type: TypeInt, Category: "storage", Description: "备份存储最小可用空间 (GB)", Min: "0", Max: "10000"},
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
	{Key: "metrics.remote_bearer_token", EnvVar: "METRICS_REMOTE_BEARER_TOKEN", CodeDefault: "", Type: TypeString, Category: "metrics", Description: "Prometheus remote-write 鉴权 Bearer token；生产环境建议使用环境变量配置以避免明文存库", RequiresRestart: true},
	{Key: "smtp.host", EnvVar: "SMTP_HOST", CodeDefault: "", Type: TypeString, Category: "alerting", Description: "SMTP 服务器地址（启用邮件告警时必填）"},
	{Key: "smtp.port", EnvVar: "SMTP_PORT", CodeDefault: "587", Type: TypeString, Category: "alerting", Description: "SMTP 端口（默认 587 STARTTLS；465 走隐式 TLS）"},
	{Key: "smtp.user", EnvVar: "SMTP_USER", CodeDefault: "", Type: TypeString, Category: "alerting", Description: "SMTP 用户名"},
	{Key: "smtp.password", EnvVar: "SMTP_PASS", CodeDefault: "", Type: TypeString, Category: "alerting", Description: "SMTP 密码（生产环境建议通过环境变量注入而非入库）"},
	{Key: "smtp.from", EnvVar: "SMTP_FROM", CodeDefault: "", Type: TypeString, Category: "alerting", Description: "发件人地址；为空时回退到 smtp.user"},
	{Key: "smtp.require_tls", EnvVar: "SMTP_REQUIRE_TLS", CodeDefault: "true", Type: TypeBool, Category: "alerting", Description: "强制 TLS 连接（465 隐式 / 587 STARTTLS）；false 回退明文"},
}

// registryMap O(1) key 查找（init 时构建）
var registryMap map[string]*SettingDef

func init() {
	registryMap = make(map[string]*SettingDef, len(registry))
	for i := range registry {
		def := &registry[i]
		registryMap[def.Key] = def
		// 启动时校验 Min/Max 定义合法性
		if def.Min != "" && def.Type == TypeInt {
			if _, err := strconv.Atoi(def.Min); err != nil {
				panic(fmt.Sprintf("settings: invalid Min for %s: %s", def.Key, def.Min))
			}
		}
		if def.Max != "" && def.Type == TypeInt {
			if _, err := strconv.Atoi(def.Max); err != nil {
				panic(fmt.Sprintf("settings: invalid Max for %s: %s", def.Key, def.Max))
			}
		}
		if def.MinDuration != "" {
			if _, err := time.ParseDuration(def.MinDuration); err != nil {
				panic(fmt.Sprintf("settings: invalid MinDuration for %s: %s", def.Key, def.MinDuration))
			}
		}
	}
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
			result[def.Key] = ResolvedSetting{Value: dbVal.Value, Source: "db", UpdatedAt: &t}
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
	value := s.resolveValue(key)

	// 写入缓存
	s.mu.Lock()
	s.cache[key] = cachedValue{value: value, expiresAt: time.Now().Add(cacheTTL)}
	s.mu.Unlock()

	return value
}

// resolveValue 按 DB → env → default 优先级解析值（无缓存）
func (s *Service) resolveValue(key string) string {
	// 使用 Limit(1).Find 代替 First，避免 GORM 对空结果打 "record not found" 错误日志
	var dbSettings []model.SystemSetting
	s.db.Where("key = ?", key).Limit(1).Find(&dbSettings)
	if len(dbSettings) > 0 {
		return dbSettings[0].Value
	}

	if def, ok := registryMap[key]; ok {
		if envVal := strings.TrimSpace(os.Getenv(def.EnvVar)); envVal != "" {
			return envVal
		}
		return def.CodeDefault
	}
	return ""
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
	setting := model.SystemSetting{
		Key:       key,
		Value:     value,
		UpdatedAt: time.Now(),
	}
	return db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at"}),
	}).Create(&setting).Error
}

// Delete 删除 DB 覆盖值（恢复为环境变量或默认值），写入后自动失效缓存
func (s *Service) Delete(key string) error {
	def := findDef(key)
	if def == nil {
		return fmt.Errorf("未知的设置项: %s", key)
	}
	if err := s.db.Where("key = ?", key).Delete(&model.SystemSetting{}).Error; err != nil {
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
		v, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("设置项 %s 值必须为整数", def.Key)
		}
		if def.Min != "" {
			min, _ := strconv.Atoi(def.Min)
			if v < min {
				return fmt.Errorf("设置项 %s 值不能小于 %s", def.Key, def.Min)
			}
		}
		if def.Max != "" {
			max, _ := strconv.Atoi(def.Max)
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
	}
	return nil
}
