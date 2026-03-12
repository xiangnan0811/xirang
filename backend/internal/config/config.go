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

type Config struct {
	ListenAddr                string
	DBType                    string
	SQLitePath                string
	PostgresDSN               string
	JWTSecret                 string
	JWTTTL                    time.Duration
	RsyncBinary               string
	TaskTrafficRetentionDays  int
	TaskRunRetentionDays      int
	AllowedOrigins            []string
	WSAllowEmptyOrigin        bool
	LoginRateLimit            int
	LoginRateWindow           time.Duration
	LoginFailLockThreshold    int
	LoginFailLockDuration     time.Duration
	LoginCaptchaEnabled       bool
	LoginSecondCaptchaEnabled bool
	NodeProbeInterval         time.Duration
	NodeProbeFailThreshold    int
	NodeProbeConcurrency      int
}

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
		ListenAddr:     util.GetEnvOrDefault("SERVER_ADDR", ":8080"),
		DBType:         strings.ToLower(util.GetEnvOrDefault("DB_TYPE", "sqlite")),
		SQLitePath:     util.GetEnvOrDefault("SQLITE_PATH", "./xirang.db"),
		PostgresDSN:    util.GetEnvOrDefault("DB_DSN", ""),
		JWTSecret:      jwtSecret,
		RsyncBinary:    util.GetEnvOrDefault("RSYNC_BINARY", "rsync"),
		TaskTrafficRetentionDays: 8,
		AllowedOrigins: splitCSV(allowedOriginsRaw),
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

	loginCaptchaEnabled, err := util.ReadBoolEnv("LOGIN_CAPTCHA_ENABLED", false)
	if err != nil {
		return Config{}, err
	}
	cfg.LoginCaptchaEnabled = loginCaptchaEnabled

	loginSecondCaptchaEnabled, err := util.ReadBoolEnv("LOGIN_SECOND_CAPTCHA_ENABLED", false)
	if err != nil {
		return Config{}, err
	}
	cfg.LoginSecondCaptchaEnabled = loginSecondCaptchaEnabled

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

	wsAllowEmptyOrigin, err := util.ReadBoolEnv("WS_ALLOW_EMPTY_ORIGIN", false)
	if err != nil {
		return Config{}, err
	}
	cfg.WSAllowEmptyOrigin = wsAllowEmptyOrigin

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
		if isWeakJWTSecret(cfg.JWTSecret) {
			return Config{}, fmt.Errorf("必须配置强 JWT_SECRET（仅 APP_ENV=development 可使用默认值）")
		}
		encryptionKey := strings.TrimSpace(os.Getenv("DATA_ENCRYPTION_KEY"))
		if isWeakDataEncryptionKey(encryptionKey) {
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

func isWeakJWTSecret(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return true
	}
	weakSet := map[string]struct{}{
		"xirang-dev-secret":                   {},
		"change-me":                           {},
		"change-me-in-production":             {},
		"replace-with-a-strong-random-secret": {},
	}
	_, weak := weakSet[trimmed]
	return weak
}

func isWeakDataEncryptionKey(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return true
	}
	weakSet := map[string]struct{}{
		"xirang-dev-encryption-key-change-me": {},
		"change-me":                           {},
		"change-me-encryption-key":            {},
		"replace-with-32-byte-base64-key":     {},
	}
	_, weak := weakSet[trimmed]
	return weak
}
