package config

import (
	"os"
	"testing"
)

func TestLoadParsesOriginsAndRateLimit(t *testing.T) {
	t.Setenv("DB_TYPE", "sqlite")
	t.Setenv("JWT_TTL", "2h")
	t.Setenv("CORS_ALLOWED_ORIGINS", "https://xirang.example.com,https://admin.example.com")
	t.Setenv("LOGIN_RATE_LIMIT", "9")
	t.Setenv("LOGIN_RATE_WINDOW", "90s")
	t.Setenv("LOGIN_FAIL_LOCK_THRESHOLD", "6")
	t.Setenv("LOGIN_FAIL_LOCK_DURATION", "20m")
	t.Setenv("LOGIN_CAPTCHA_ENABLED", "true")
	t.Setenv("LOGIN_SECOND_CAPTCHA_ENABLED", "false")
	t.Setenv("APP_ENV", "")
	t.Setenv("ENVIRONMENT", "")
	t.Setenv("GIN_MODE", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}

	if cfg.LoginRateLimit != 9 {
		t.Fatalf("期望限流次数为 9，实际: %d", cfg.LoginRateLimit)
	}
	if cfg.LoginRateWindow.String() != "1m30s" {
		t.Fatalf("期望限流窗口为 1m30s，实际: %s", cfg.LoginRateWindow)
	}
	if cfg.LoginFailLockThreshold != 6 {
		t.Fatalf("期望登录失败锁定阈值为 6，实际: %d", cfg.LoginFailLockThreshold)
	}
	if cfg.LoginFailLockDuration.String() != "20m0s" {
		t.Fatalf("期望登录失败锁定时长为 20m0s，实际: %s", cfg.LoginFailLockDuration)
	}
	if !cfg.LoginCaptchaEnabled {
		t.Fatalf("期望开启登录验证码开关")
	}
	if cfg.LoginSecondCaptchaEnabled {
		t.Fatalf("期望登录二次验证码开关为关闭")
	}
	if len(cfg.AllowedOrigins) != 2 {
		t.Fatalf("期望解析 2 个域名，实际: %d", len(cfg.AllowedOrigins))
	}
}

func TestLoadRejectsInvalidRateLimit(t *testing.T) {
	t.Setenv("DB_TYPE", "sqlite")
	t.Setenv("JWT_TTL", "2h")
	t.Setenv("LOGIN_RATE_LIMIT", "abc")
	t.Setenv("LOGIN_FAIL_LOCK_THRESHOLD", "5")
	t.Setenv("LOGIN_FAIL_LOCK_DURATION", "15m")
	t.Setenv("APP_ENV", "")
	t.Setenv("ENVIRONMENT", "")
	t.Setenv("GIN_MODE", "")

	_, err := Load()
	if err == nil {
		t.Fatalf("期望限流配置非法时返回错误")
	}
}

func TestLoadRejectsWeakSecretsInProduction(t *testing.T) {
	t.Setenv("DB_TYPE", "sqlite")
	t.Setenv("JWT_TTL", "2h")
	t.Setenv("LOGIN_RATE_LIMIT", "10")
	t.Setenv("LOGIN_RATE_WINDOW", "1m")
	t.Setenv("LOGIN_FAIL_LOCK_THRESHOLD", "5")
	t.Setenv("LOGIN_FAIL_LOCK_DURATION", "15m")
	t.Setenv("APP_ENV", "production")
	t.Setenv("JWT_SECRET", "xirang-dev-secret")
	t.Setenv("DATA_ENCRYPTION_KEY", "")

	_, err := Load()
	if err == nil {
		t.Fatalf("期望生产环境弱密钥返回错误")
	}
}

func TestLoadAcceptsStrongSecretsInProduction(t *testing.T) {
	t.Setenv("DB_TYPE", "sqlite")
	t.Setenv("JWT_TTL", "2h")
	t.Setenv("LOGIN_RATE_LIMIT", "10")
	t.Setenv("LOGIN_RATE_WINDOW", "1m")
	t.Setenv("LOGIN_FAIL_LOCK_THRESHOLD", "5")
	t.Setenv("LOGIN_FAIL_LOCK_DURATION", "15m")
	t.Setenv("APP_ENV", "production")
	t.Setenv("JWT_SECRET", "super-strong-secret-for-production")
	t.Setenv("DATA_ENCRYPTION_KEY", "prod-encryption-key-very-strong")
	t.Setenv("CORS_ALLOWED_ORIGINS", "https://xirang.example.com")

	if _, err := Load(); err != nil {
		t.Fatalf("期望生产环境强密钥配置可通过，实际错误: %v", err)
	}
}

func TestLoadRejectsInvalidCaptchaSwitch(t *testing.T) {
	t.Setenv("DB_TYPE", "sqlite")
	t.Setenv("JWT_TTL", "2h")
	t.Setenv("LOGIN_RATE_LIMIT", "10")
	t.Setenv("LOGIN_RATE_WINDOW", "1m")
	t.Setenv("LOGIN_FAIL_LOCK_THRESHOLD", "5")
	t.Setenv("LOGIN_FAIL_LOCK_DURATION", "15m")
	t.Setenv("LOGIN_CAPTCHA_ENABLED", "abc")
	t.Setenv("APP_ENV", "")
	t.Setenv("ENVIRONMENT", "")
	t.Setenv("GIN_MODE", "")

	if _, err := Load(); err == nil {
		t.Fatalf("期望验证码开关非法时返回错误")
	}
}

func TestSplitCSVEmptyShouldNotFallbackWildcard(t *testing.T) {
	values := splitCSV("")
	if len(values) != 0 {
		t.Fatalf("期望空字符串解析为空切片，实际: %+v", values)
	}
}

func TestLoadRejectsWildcardOriginInProduction(t *testing.T) {
	t.Setenv("DB_TYPE", "sqlite")
	t.Setenv("JWT_TTL", "2h")
	t.Setenv("LOGIN_RATE_LIMIT", "10")
	t.Setenv("LOGIN_RATE_WINDOW", "1m")
	t.Setenv("LOGIN_FAIL_LOCK_THRESHOLD", "5")
	t.Setenv("LOGIN_FAIL_LOCK_DURATION", "15m")
	t.Setenv("APP_ENV", "production")
	t.Setenv("JWT_SECRET", "super-strong-secret-for-production")
	t.Setenv("DATA_ENCRYPTION_KEY", "prod-encryption-key-very-strong")
	t.Setenv("CORS_ALLOWED_ORIGINS", "*")

	if _, err := Load(); err == nil {
		t.Fatalf("期望生产环境禁止 CORS 通配符")
	}
}

func TestLoadParsesWSAllowEmptyOrigin(t *testing.T) {
	t.Setenv("DB_TYPE", "sqlite")
	t.Setenv("JWT_TTL", "2h")
	t.Setenv("LOGIN_RATE_LIMIT", "10")
	t.Setenv("LOGIN_RATE_WINDOW", "1m")
	t.Setenv("LOGIN_FAIL_LOCK_THRESHOLD", "5")
	t.Setenv("LOGIN_FAIL_LOCK_DURATION", "15m")
	t.Setenv("APP_ENV", "")
	t.Setenv("ENVIRONMENT", "")
	t.Setenv("GIN_MODE", "")
	t.Setenv("WS_ALLOW_EMPTY_ORIGIN", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	if !cfg.WSAllowEmptyOrigin {
		t.Fatalf("期望 WS_ALLOW_EMPTY_ORIGIN=true 被正确解析")
	}
}

func TestLoadKeepsEmptyCORSOrigins(t *testing.T) {
	t.Setenv("DB_TYPE", "sqlite")
	t.Setenv("JWT_TTL", "2h")
	t.Setenv("LOGIN_RATE_LIMIT", "10")
	t.Setenv("LOGIN_RATE_WINDOW", "1m")
	t.Setenv("LOGIN_FAIL_LOCK_THRESHOLD", "5")
	t.Setenv("LOGIN_FAIL_LOCK_DURATION", "15m")
	t.Setenv("APP_ENV", "")
	t.Setenv("ENVIRONMENT", "")
	t.Setenv("GIN_MODE", "")
	t.Setenv("CORS_ALLOWED_ORIGINS", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	if len(cfg.AllowedOrigins) != 0 {
		t.Fatalf("期望空 CORS 配置不回退为 *，实际: %+v", cfg.AllowedOrigins)
	}
}

func TestLoadUsesSafeDefaultCORSOriginsWhenUnset(t *testing.T) {
	t.Setenv("DB_TYPE", "sqlite")
	t.Setenv("JWT_TTL", "2h")
	t.Setenv("LOGIN_RATE_LIMIT", "10")
	t.Setenv("LOGIN_RATE_WINDOW", "1m")
	t.Setenv("LOGIN_FAIL_LOCK_THRESHOLD", "5")
	t.Setenv("LOGIN_FAIL_LOCK_DURATION", "15m")
	t.Setenv("APP_ENV", "")
	t.Setenv("ENVIRONMENT", "")
	t.Setenv("GIN_MODE", "")

	original, existed := os.LookupEnv("CORS_ALLOWED_ORIGINS")
	if err := os.Unsetenv("CORS_ALLOWED_ORIGINS"); err != nil {
		t.Fatalf("清理 CORS_ALLOWED_ORIGINS 失败: %v", err)
	}
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv("CORS_ALLOWED_ORIGINS", original)
			return
		}
		_ = os.Unsetenv("CORS_ALLOWED_ORIGINS")
	})

	cfg, err := Load()
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	if len(cfg.AllowedOrigins) != 2 {
		t.Fatalf("期望使用安全默认跨域白名单，实际: %+v", cfg.AllowedOrigins)
	}
	if cfg.AllowedOrigins[0] != "http://localhost:5173" || cfg.AllowedOrigins[1] != "http://127.0.0.1:5173" {
		t.Fatalf("默认跨域白名单不符合预期，实际: %+v", cfg.AllowedOrigins)
	}
}
