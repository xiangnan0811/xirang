package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	ListenAddr                string
	DBType                    string
	SQLitePath                string
	PostgresDSN               string
	JWTSecret                 string
	JWTTTL                    time.Duration
	ExecutorShell             string
	RsyncBinary               string
	AllowedOrigins            []string
	LoginRateLimit            int
	LoginRateWindow           time.Duration
	LoginFailLockThreshold    int
	LoginFailLockDuration     time.Duration
	LoginCaptchaEnabled       bool
	LoginSecondCaptchaEnabled bool
}

func Load() (Config, error) {
	cfg := Config{
		ListenAddr:     getEnv("SERVER_ADDR", ":8080"),
		DBType:         strings.ToLower(getEnv("DB_TYPE", "sqlite")),
		SQLitePath:     getEnv("SQLITE_PATH", "./xirang.db"),
		PostgresDSN:    getEnv("DB_DSN", ""),
		JWTSecret:      getEnv("JWT_SECRET", "xirang-dev-secret"),
		ExecutorShell:  getEnv("EXECUTOR_SHELL", "/bin/sh"),
		RsyncBinary:    getEnv("RSYNC_BINARY", "rsync"),
		AllowedOrigins: splitCSV(getEnv("CORS_ALLOWED_ORIGINS", "*")),
	}

	ttlRaw := getEnv("JWT_TTL", "24h")
	ttl, err := time.ParseDuration(ttlRaw)
	if err != nil {
		return Config{}, fmt.Errorf("解析 JWT_TTL 失败: %w", err)
	}
	cfg.JWTTTL = ttl

	rateLimitRaw := getEnv("LOGIN_RATE_LIMIT", "10")
	rateLimit, err := strconv.Atoi(rateLimitRaw)
	if err != nil || rateLimit <= 0 {
		return Config{}, fmt.Errorf("解析 LOGIN_RATE_LIMIT 失败")
	}
	cfg.LoginRateLimit = rateLimit

	rateWindowRaw := getEnv("LOGIN_RATE_WINDOW", "1m")
	rateWindow, err := time.ParseDuration(rateWindowRaw)
	if err != nil || rateWindow <= 0 {
		return Config{}, fmt.Errorf("解析 LOGIN_RATE_WINDOW 失败: %w", err)
	}
	cfg.LoginRateWindow = rateWindow

	failLockThresholdRaw := getEnv("LOGIN_FAIL_LOCK_THRESHOLD", "5")
	failLockThreshold, err := strconv.Atoi(failLockThresholdRaw)
	if err != nil || failLockThreshold <= 0 {
		return Config{}, fmt.Errorf("解析 LOGIN_FAIL_LOCK_THRESHOLD 失败")
	}
	cfg.LoginFailLockThreshold = failLockThreshold

	failLockDurationRaw := getEnv("LOGIN_FAIL_LOCK_DURATION", "15m")
	failLockDuration, err := time.ParseDuration(failLockDurationRaw)
	if err != nil || failLockDuration <= 0 {
		return Config{}, fmt.Errorf("解析 LOGIN_FAIL_LOCK_DURATION 失败: %w", err)
	}
	cfg.LoginFailLockDuration = failLockDuration

	loginCaptchaEnabled, err := getEnvAsBool("LOGIN_CAPTCHA_ENABLED", false)
	if err != nil {
		return Config{}, err
	}
	cfg.LoginCaptchaEnabled = loginCaptchaEnabled

	loginSecondCaptchaEnabled, err := getEnvAsBool("LOGIN_SECOND_CAPTCHA_ENABLED", false)
	if err != nil {
		return Config{}, err
	}
	cfg.LoginSecondCaptchaEnabled = loginSecondCaptchaEnabled

	switch cfg.DBType {
	case "sqlite":
	case "postgres":
		if cfg.PostgresDSN == "" {
			return Config{}, fmt.Errorf("DB_TYPE=postgres 时 DB_DSN 不能为空")
		}
	default:
		return Config{}, fmt.Errorf("不支持的 DB_TYPE: %s", cfg.DBType)
	}

	if isProductionEnv() {
		if isWeakJWTSecret(cfg.JWTSecret) {
			return Config{}, fmt.Errorf("生产环境必须配置强 JWT_SECRET")
		}
		encryptionKey := strings.TrimSpace(os.Getenv("DATA_ENCRYPTION_KEY"))
		if isWeakDataEncryptionKey(encryptionKey) {
			return Config{}, fmt.Errorf("生产环境必须配置强 DATA_ENCRYPTION_KEY")
		}
	}

	return cfg, nil
}

func getEnv(key, defaultValue string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return defaultValue
	}
	return value
}

func getEnvAsBool(key string, defaultValue bool) (bool, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return defaultValue, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s 必须是 true/false", key)
	}
	return parsed, nil
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
	if len(items) == 0 {
		return []string{"*"}
	}
	return items
}

func isProductionEnv() bool {
	appEnv := strings.ToLower(strings.TrimSpace(os.Getenv("APP_ENV")))
	environment := strings.ToLower(strings.TrimSpace(os.Getenv("ENVIRONMENT")))
	ginMode := strings.ToLower(strings.TrimSpace(os.Getenv("GIN_MODE")))
	return appEnv == "production" || environment == "production" || ginMode == "release"
}

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
