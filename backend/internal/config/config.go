// Package config 负责加载和验证应用配置。
//
// Config holds startup-critical settings from environment variables. These are
// immutable after Load(). Runtime-configurable settings belong to
// settings.Service (see internal/settings/service.go).
//
// Two-tier configuration model:
//
//   - Config (this package): boot-time env vars — DB connection, JWT secrets,
//     listen address, SSH host-key policy. Fixed for the lifetime of the
//     process; changing them requires a restart.
//
//   - settings.Service: runtime-configurable values stored in the database.
//     Settings changed via API take effect immediately (with optional caching).
//     Values override Config env vars where both exist — the precedence is
//     DB value > env var > code default.
//
// Fields marked with an "Overlap note" comment exist in both Config and
// settings.Service. Config provides the boot-time default; settings.Service
// can override them at runtime. Code that needs the live value should prefer
// settings.Service.GetEffective().
//
// 注意：Load() 在 logger.Init() 之前运行，因此本包使用标准库 log.Printf
// 输出早期启动警告，而非 zerolog。
package config

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"xirang/backend/internal/util"
)

// Config holds all application configuration loaded from environment variables.
type Config struct {
	ListenAddr               string
	DBType                   string
	SQLitePath               string
	PostgresDSN              string
	JWTSecret                string
	JWTTTL                   time.Duration
	RsyncBinary string
	// Overlap note: TaskTrafficRetentionDays is also defined in
	// settings.Service as "retention.task_traffic_days". Config provides the
	// default at startup; settings.Service can override at runtime.
	TaskTrafficRetentionDays int
	// Overlap note: TaskRunRetentionDays is also defined in settings.Service
	// as "retention.task_run_days". Config provides the default at startup;
	// settings.Service can override at runtime.
	TaskRunRetentionDays int
	AllowedOrigins           []string
	WSAllowEmptyOrigin       bool
	// Overlap note: LoginRateLimit is also defined in settings.Service as
	// "login.rate_limit". Config provides the default at startup;
	// settings.Service can override at runtime.
	LoginRateLimit  int
	// Overlap note: LoginRateWindow is also defined in settings.Service as
	// "login.rate_window". Config provides the default at startup;
	// settings.Service can override at runtime.
	LoginRateWindow time.Duration
	// Overlap note: LoginFailLockThreshold is also defined in settings.Service
	// as "login.fail_lock_threshold". Config provides the default at startup;
	// settings.Service can override at runtime.
	LoginFailLockThreshold int
	// Overlap note: LoginFailLockDuration is also defined in settings.Service
	// as "login.fail_lock_duration". Config provides the default at startup;
	// settings.Service can override at runtime.
	LoginFailLockDuration          time.Duration
	LoginGlobalFailLockThreshold   int
	LoginGlobalFailLockDuration    time.Duration
	// Overlap note: NodeProbeInterval is also defined in settings.Service as
	// "node.probe_interval". Config provides the default at startup;
	// settings.Service can override at runtime.
	NodeProbeInterval time.Duration
	// Overlap note: NodeProbeFailThreshold is also defined in settings.Service
	// as "node.probe_fail_threshold". Config provides the default at startup;
	// settings.Service can override at runtime.
	NodeProbeFailThreshold int
	// Overlap note: NodeProbeConcurrency is also defined in settings.Service as
	// "node.probe_concurrency". Config provides the default at startup;
	// settings.Service can override at runtime.
	NodeProbeConcurrency     int
	// Overlap note: RetentionCheckInterval is also defined in settings.Service
	// as "retention.check_interval". Config provides the default at startup;
	// settings.Service can override at runtime.
	RetentionCheckInterval   time.Duration
	// Overlap note: BackupStorageMinFreeGB is also defined in settings.Service
	// as "storage.min_free_gb". Config provides the default at startup;
	// settings.Service can override at runtime.
	BackupStorageMinFreeGB   int
	// Overlap note: BackupStorageMaxUsagePct is also defined in
	// settings.Service as "storage.max_usage_pct". Config provides the default
	// at startup; settings.Service can override at runtime.
	BackupStorageMaxUsagePct int
	MetricsToken             string
	MetricsRateLimit         int
	MetricsRateWindow        time.Duration
	SSHStrictHostKeyChecking bool
	SSHAutoAcceptNewHosts    bool
}

// Load reads environment variables and returns a validated Config. It exits early
// with descriptive errors when required variables are missing or malformed. In
// development mode some security checks are relaxed; in production mode additional
// hardening checks are enforced.
func Load() (Config, error) {
	allowedOriginsRaw, hasAllowedOrigins := os.LookupEnv("CORS_ALLOWED_ORIGINS")
	if !hasAllowedOrigins {
		allowedOriginsRaw = "http://localhost:5173,http://127.0.0.1:5173"
	}

	jwtSecret := os.Getenv("JWT_SECRET")
	if jwtSecret == "" {
		if util.IsDevelopmentEnv() {
			jwtSecret = "xirang-dev-secret"
			log.Printf("warn: 使用默认 JWT_SECRET，仅适用于开发环境，生产环境必须设置 JWT_SECRET")
		} else {
			return Config{}, fmt.Errorf("JWT_SECRET 环境变量未设置（仅 APP_ENV=development 可省略）")
		}
	}

	cfg := Config{
		ListenAddr:               util.GetEnvOrDefault("SERVER_ADDR", ":8080"),
		DBType:                   strings.ToLower(util.GetEnvOrDefault("DB_TYPE", "sqlite")),
		SQLitePath:               util.GetEnvOrDefault("SQLITE_PATH", "./xirang.db"),
		PostgresDSN:              util.GetEnvOrDefault("DB_DSN", ""),
		JWTSecret:                jwtSecret,
		RsyncBinary:              util.GetEnvOrDefault("RSYNC_BINARY", "rsync"),
		TaskTrafficRetentionDays: 8,
		AllowedOrigins:           splitCSV(allowedOriginsRaw),
	}

	retentionDaysRaw := util.GetEnvOrDefault("TASK_TRAFFIC_RETENTION_DAYS", "8")
	retentionDays, err := strconv.Atoi(retentionDaysRaw)
	if err != nil || retentionDays < 0 {
		return Config{}, fmt.Errorf("解析 TASK_TRAFFIC_RETENTION_DAYS 失败")
	}
	cfg.TaskTrafficRetentionDays = retentionDays

	taskRunRetentionRaw := util.GetEnvOrDefault("TASK_RUN_RETENTION_DAYS", "90")
	taskRunRetention, err := strconv.Atoi(taskRunRetentionRaw)
	if err != nil || taskRunRetention < 0 {
		return Config{}, fmt.Errorf("解析 TASK_RUN_RETENTION_DAYS 失败")
	}
	cfg.TaskRunRetentionDays = taskRunRetention

	ttlRaw := util.GetEnvOrDefault("JWT_TTL", "24h")
	ttl, err := time.ParseDuration(ttlRaw)
	if err != nil {
		return Config{}, fmt.Errorf("解析 JWT_TTL 失败: %w", err)
	}
	cfg.JWTTTL = ttl

	rateLimitRaw := util.GetEnvOrDefault("LOGIN_RATE_LIMIT", "10")
	rateLimit, err := strconv.Atoi(rateLimitRaw)
	if err != nil || rateLimit <= 0 {
		return Config{}, fmt.Errorf("解析 LOGIN_RATE_LIMIT 失败")
	}
	cfg.LoginRateLimit = rateLimit

	rateWindowRaw := util.GetEnvOrDefault("LOGIN_RATE_WINDOW", "1m")
	rateWindow, err := time.ParseDuration(rateWindowRaw)
	if err != nil || rateWindow <= 0 {
		return Config{}, fmt.Errorf("解析 LOGIN_RATE_WINDOW 失败: %w", err)
	}
	cfg.LoginRateWindow = rateWindow

	failLockThresholdRaw := util.GetEnvOrDefault("LOGIN_FAIL_LOCK_THRESHOLD", "5")
	failLockThreshold, err := strconv.Atoi(failLockThresholdRaw)
	if err != nil || failLockThreshold <= 0 {
		return Config{}, fmt.Errorf("解析 LOGIN_FAIL_LOCK_THRESHOLD 失败")
	}
	cfg.LoginFailLockThreshold = failLockThreshold

	failLockDurationRaw := util.GetEnvOrDefault("LOGIN_FAIL_LOCK_DURATION", "15m")
	failLockDuration, err := time.ParseDuration(failLockDurationRaw)
	if err != nil || failLockDuration <= 0 {
		return Config{}, fmt.Errorf("解析 LOGIN_FAIL_LOCK_DURATION 失败: %w", err)
	}
	cfg.LoginFailLockDuration = failLockDuration

	failGlobalLockThresholdRaw := util.GetEnvOrDefault("LOGIN_GLOBAL_FAIL_LOCK_THRESHOLD", "50")
	failGlobalLockThreshold, err := strconv.Atoi(failGlobalLockThresholdRaw)
	if err != nil || failGlobalLockThreshold <= 0 {
		return Config{}, fmt.Errorf("解析 LOGIN_GLOBAL_FAIL_LOCK_THRESHOLD 失败")
	}
	cfg.LoginGlobalFailLockThreshold = failGlobalLockThreshold

	failGlobalLockDurationRaw := util.GetEnvOrDefault("LOGIN_GLOBAL_FAIL_LOCK_DURATION", "15m")
	failGlobalLockDuration, err := time.ParseDuration(failGlobalLockDurationRaw)
	if err != nil || failGlobalLockDuration <= 0 {
		return Config{}, fmt.Errorf("解析 LOGIN_GLOBAL_FAIL_LOCK_DURATION 失败: %w", err)
	}
	cfg.LoginGlobalFailLockDuration = failGlobalLockDuration

	probeIntervalRaw := util.GetEnvOrDefault("NODE_PROBE_INTERVAL", "5m")
	probeInterval, err := time.ParseDuration(probeIntervalRaw)
	if err != nil || probeInterval < 30*time.Second {
		return Config{}, fmt.Errorf("解析 NODE_PROBE_INTERVAL 失败")
	}
	cfg.NodeProbeInterval = probeInterval

	probeFailThresholdRaw := util.GetEnvOrDefault("NODE_PROBE_FAIL_THRESHOLD", "3")
	probeFailThreshold, err := strconv.Atoi(probeFailThresholdRaw)
	if err != nil || probeFailThreshold <= 0 {
		return Config{}, fmt.Errorf("解析 NODE_PROBE_FAIL_THRESHOLD 失败")
	}
	cfg.NodeProbeFailThreshold = probeFailThreshold

	probeConcurrencyRaw := util.GetEnvOrDefault("NODE_PROBE_CONCURRENCY", "10")
	probeConcurrency, err := strconv.Atoi(probeConcurrencyRaw)
	if err != nil || probeConcurrency <= 0 {
		return Config{}, fmt.Errorf("解析 NODE_PROBE_CONCURRENCY 失败")
	}
	cfg.NodeProbeConcurrency = probeConcurrency

	retentionCheckIntervalRaw := util.GetEnvOrDefault("RETENTION_CHECK_INTERVAL", "6h")
	retentionCheckInterval, err := time.ParseDuration(retentionCheckIntervalRaw)
	if err != nil || retentionCheckInterval < 1*time.Minute {
		return Config{}, fmt.Errorf("解析 RETENTION_CHECK_INTERVAL 失败")
	}
	cfg.RetentionCheckInterval = retentionCheckInterval

	backupStorageMinFreeRaw := util.GetEnvOrDefault("BACKUP_STORAGE_MIN_FREE_GB", "10")
	backupStorageMinFree, err := strconv.Atoi(backupStorageMinFreeRaw)
	if err != nil || backupStorageMinFree < 0 {
		return Config{}, fmt.Errorf("解析 BACKUP_STORAGE_MIN_FREE_GB 失败")
	}
	cfg.BackupStorageMinFreeGB = backupStorageMinFree

	backupStorageMaxUsageRaw := util.GetEnvOrDefault("BACKUP_STORAGE_MAX_USAGE_PCT", "90")
	backupStorageMaxUsage, err := strconv.Atoi(backupStorageMaxUsageRaw)
	if err != nil || backupStorageMaxUsage < 0 || backupStorageMaxUsage > 100 {
		return Config{}, fmt.Errorf("解析 BACKUP_STORAGE_MAX_USAGE_PCT 失败")
	}
	cfg.BackupStorageMaxUsagePct = backupStorageMaxUsage

	wsAllowEmptyOrigin, err := util.ReadBoolEnv("WS_ALLOW_EMPTY_ORIGIN", false)
	if err != nil {
		return Config{}, err
	}
	cfg.WSAllowEmptyOrigin = wsAllowEmptyOrigin

	cfg.MetricsToken = strings.TrimSpace(os.Getenv("METRICS_TOKEN"))

	metricsRateLimitRaw := util.GetEnvOrDefault("METRICS_RATE_LIMIT", "5")
	metricsRateLimit, err := strconv.Atoi(metricsRateLimitRaw)
	if err != nil || metricsRateLimit <= 0 {
		return Config{}, fmt.Errorf("解析 METRICS_RATE_LIMIT 失败")
	}
	cfg.MetricsRateLimit = metricsRateLimit

	metricsRateWindowRaw := util.GetEnvOrDefault("METRICS_RATE_WINDOW", "1s")
	metricsRateWindow, err := time.ParseDuration(metricsRateWindowRaw)
	if err != nil || metricsRateWindow <= 0 {
		return Config{}, fmt.Errorf("解析 METRICS_RATE_WINDOW 失败: %w", err)
	}
	cfg.MetricsRateWindow = metricsRateWindow

	sshStrict, err := util.ReadBoolEnv("SSH_STRICT_HOST_KEY_CHECKING", true)
	if err != nil {
		return Config{}, err
	}
	cfg.SSHStrictHostKeyChecking = sshStrict

	sshAutoAccept, err := util.ReadBoolEnv("SSH_AUTO_ACCEPT_NEW_HOSTS", false)
	if err != nil {
		return Config{}, err
	}
	cfg.SSHAutoAcceptNewHosts = sshAutoAccept

	if !cfg.SSHStrictHostKeyChecking && !util.IsDevelopmentEnv() {
		log.Printf("warn: SSH_STRICT_HOST_KEY_CHECKING 已关闭，生产环境建议开启以防御中间人攻击")
	}

	if len(cfg.AllowedOrigins) == 0 {
		log.Printf("warn: CORS_ALLOWED_ORIGINS 为空，仅同主机（忽略端口）Origin 会被放行")
	}

	switch cfg.DBType {
	case "sqlite":
	case "postgres":
		if cfg.PostgresDSN == "" {
			return Config{}, fmt.Errorf("DB_TYPE=postgres 时 DB_DSN 不能为空")
		}
	default:
		return Config{}, fmt.Errorf("不支持的 DB_TYPE: %s", cfg.DBType)
	}

	if !util.IsDevelopmentEnv() {
		if IsWeakJWTSecret(cfg.JWTSecret) {
			return Config{}, fmt.Errorf("必须配置强 JWT_SECRET（仅 APP_ENV=development 可使用默认值）")
		}
		encryptionKey := strings.TrimSpace(os.Getenv("DATA_ENCRYPTION_KEY"))
		if IsWeakDataEncryptionKey(encryptionKey) {
			return Config{}, fmt.Errorf("必须配置强 DATA_ENCRYPTION_KEY（仅 APP_ENV=development 可省略）")
		}
	}
	if util.IsProductionEnv() {
		for _, origin := range cfg.AllowedOrigins {
			if strings.TrimSpace(origin) == "*" {
				return Config{}, fmt.Errorf("生产环境禁止将 CORS_ALLOWED_ORIGINS 配置为 *")
			}
		}
	}

	return cfg, nil
}

// splitCSV splits a comma-separated string into a trimmed slice, discarding
// empty entries.
func splitCSV(raw string) []string {
	parts := strings.Split(raw, ",")
	items := make([]string, 0, len(parts))
	for _, one := range parts {
		value := strings.TrimSpace(one)
		if value == "" {
			continue
		}
		items = append(items, value)
	}
	return items
}

// isProductionEnv 和 isDevelopmentEnv 已迁移至 util.IsProductionEnv / util.IsDevelopmentEnv

// IsWeakJWTSecret returns true if the secret is too short, matches a known weak
// value, or has insufficient entropy (a single character appearing in >50% of
// the string).
func IsWeakJWTSecret(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || len(trimmed) < 32 {
		return true
	}
	weakSet := map[string]struct{}{
		"xirang-dev-secret":                   {},
		"xirang-docker-secret":                {},
		"change-me":                           {},
		"change-me-in-production":             {},
		"replace-with-a-strong-random-secret": {},
		"please-change-this-jwt-secret":       {},
		"CHANGE-ME-use-a-strong-jwt-secret":   {},
	}
	if _, weak := weakSet[trimmed]; weak {
		return true
	}
	return hasLowEntropy(trimmed)
}

// hasLowEntropy returns true if any single character appears in more than 50% of the string,
// indicating insufficient randomness for a cryptographic secret.
func hasLowEntropy(s string) bool {
	if len(s) == 0 {
		return true
	}
	counts := make(map[rune]int)
	for _, ch := range s {
		counts[ch]++
	}
	threshold := len(s) / 2 // >50% means strictly greater than half
	for _, c := range counts {
		if c > threshold {
			return true
		}
	}
	return false
}

// IsWeakDataEncryptionKey returns true if the key is too short or matches a
// known weak placeholder value.
func IsWeakDataEncryptionKey(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || len(trimmed) < 16 {
		return true
	}
	weakSet := map[string]struct{}{
		"xirang-dev-encryption-key-change-me":   {},
		"change-me":                             {},
		"change-me-encryption-key":              {},
		"replace-with-32-byte-base64-key":       {},
		"please-change-this-encryption-key":     {},
		"CHANGE-ME-use-a-strong-encryption-key": {},
	}
	_, weak := weakSet[trimmed]
	return weak
}
