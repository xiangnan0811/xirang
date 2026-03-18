package settings

import (
	"fmt"
	"os"
	"strconv"
	"strings"
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
)

// SettingDef 设置项定义
type SettingDef struct {
	Key             string     `json:"key"`
	EnvVar          string     `json:"env_var"`
	CodeDefault     string     `json:"code_default"`
	Type            SettingType `json:"type"`
	Category        string     `json:"category"`
	Description     string     `json:"description"`
	Min             string     `json:"min,omitempty"`
	Max             string     `json:"max,omitempty"`
	RequiresRestart bool       `json:"requires_restart"`
}

// ResolvedSetting 已解析的设置值（含来源信息）
type ResolvedSetting struct {
	Value     string     `json:"value"`
	Source    string     `json:"source"` // "db" | "env" | "default"
	UpdatedAt *time.Time `json:"updated_at"`
}

// Service 系统设置服务
type Service struct {
	db *gorm.DB
}

// NewService 创建设置服务
func NewService(db *gorm.DB) *Service {
	return &Service{db: db}
}

// registry 14 项设置定义
var registry = []SettingDef{
	{Key: "login.rate_limit", EnvVar: "LOGIN_RATE_LIMIT", CodeDefault: "10", Type: TypeInt, Category: "security", Description: "登录接口每窗口最大请求数", Min: "1", Max: "1000"},
	{Key: "login.rate_window", EnvVar: "LOGIN_RATE_WINDOW", CodeDefault: "1m", Type: TypeDuration, Category: "security", Description: "登录限流时间窗口"},
	{Key: "login.fail_lock_threshold", EnvVar: "LOGIN_FAIL_LOCK_THRESHOLD", CodeDefault: "5", Type: TypeInt, Category: "security", Description: "连续登录失败锁定阈值", Min: "1", Max: "100"},
	{Key: "login.fail_lock_duration", EnvVar: "LOGIN_FAIL_LOCK_DURATION", CodeDefault: "15m", Type: TypeDuration, Category: "security", Description: "登录锁定持续时间"},
	{Key: "login.captcha_enabled", EnvVar: "LOGIN_CAPTCHA_ENABLED", CodeDefault: "false", Type: TypeBool, Category: "security", Description: "启用登录验证码"},
	{Key: "node.probe_interval", EnvVar: "NODE_PROBE_INTERVAL", CodeDefault: "5m", Type: TypeDuration, Category: "node_monitor", Description: "节点探测间隔", RequiresRestart: true},
	{Key: "node.probe_fail_threshold", EnvVar: "NODE_PROBE_FAIL_THRESHOLD", CodeDefault: "3", Type: TypeInt, Category: "node_monitor", Description: "节点探测失败阈值", Min: "1", Max: "100", RequiresRestart: true},
	{Key: "node.probe_concurrency", EnvVar: "NODE_PROBE_CONCURRENCY", CodeDefault: "10", Type: TypeInt, Category: "node_monitor", Description: "节点探测并发数", Min: "1", Max: "100", RequiresRestart: true},
	{Key: "retention.task_traffic_days", EnvVar: "TASK_TRAFFIC_RETENTION_DAYS", CodeDefault: "8", Type: TypeInt, Category: "retention", Description: "任务流量数据保留天数", Min: "1", Max: "365"},
	{Key: "retention.task_run_days", EnvVar: "TASK_RUN_RETENTION_DAYS", CodeDefault: "90", Type: TypeInt, Category: "retention", Description: "任务执行记录保留天数", Min: "1", Max: "3650"},
	{Key: "retention.check_interval", EnvVar: "RETENTION_CHECK_INTERVAL", CodeDefault: "6h", Type: TypeDuration, Category: "retention", Description: "保留策略检查间隔"},
	{Key: "storage.min_free_gb", EnvVar: "BACKUP_STORAGE_MIN_FREE_GB", CodeDefault: "10", Type: TypeInt, Category: "storage", Description: "备份存储最小可用空间 (GB)", Min: "0", Max: "10000"},
	{Key: "storage.max_usage_pct", EnvVar: "BACKUP_STORAGE_MAX_USAGE_PCT", CodeDefault: "90", Type: TypeInt, Category: "storage", Description: "备份存储最大使用率 (%)", Min: "0", Max: "100"},
	{Key: "alert.dedup_window", EnvVar: "ALERT_DEDUP_WINDOW", CodeDefault: "10m", Type: TypeDuration, Category: "alert", Description: "告警去重时间窗口"},
}

// Registry 返回所有设置定义
func (s *Service) Registry() []SettingDef {
	return registry
}

// GetAll 返回所有设置的解析值（DB → env → default 优先级）
func (s *Service) GetAll() (map[string]ResolvedSetting, error) {
	// 从 DB 加载所有已覆盖的值
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

// GetEffective 获取单项设置的有效值（供消费端调用）
func (s *Service) GetEffective(key string) string {
	// 查 DB
	var dbSetting model.SystemSetting
	if err := s.db.Where("key = ?", key).First(&dbSetting).Error; err == nil {
		return dbSetting.Value
	}

	// 查环境变量
	for _, def := range registry {
		if def.Key == key {
			if envVal := strings.TrimSpace(os.Getenv(def.EnvVar)); envVal != "" {
				return envVal
			}
			return def.CodeDefault
		}
	}
	return ""
}

// findDef 查找设置定义
func findDef(key string) *SettingDef {
	for i := range registry {
		if registry[i].Key == key {
			return &registry[i]
		}
	}
	return nil
}

// Update 更新设置值（含校验）
func (s *Service) Update(key, value string) error {
	def := findDef(key)
	if def == nil {
		return fmt.Errorf("未知的设置项: %s", key)
	}

	if err := validateValue(def, value); err != nil {
		return err
	}

	setting := model.SystemSetting{
		Key:       key,
		Value:     value,
		UpdatedAt: time.Now(),
	}
	return s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at"}),
	}).Create(&setting).Error
}

// Delete 删除 DB 覆盖值（恢复为环境变量或默认值）
func (s *Service) Delete(key string) error {
	def := findDef(key)
	if def == nil {
		return fmt.Errorf("未知的设置项: %s", key)
	}
	return s.db.Where("key = ?", key).Delete(&model.SystemSetting{}).Error
}

// validateValue 校验设置值
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
	}
	return nil
}
