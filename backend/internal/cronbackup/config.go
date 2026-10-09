package cronbackup

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	defaultMaxAgeHours = 26
	minMaxAgeHours     = 1
	maxMaxAgeHours     = 8760
)

// ParseMaxAge preserves the cron backup configuration contract: blank means
// 26 hours and a non-blank value must be a trimmed decimal integer in the
// inclusive range 1..8760.
func ParseMaxAge(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultMaxAgeHours * time.Hour, nil
	}
	hours, err := strconv.Atoi(raw)
	if err != nil || hours < minMaxAgeHours || hours > maxMaxAgeHours {
		return 0, fmt.Errorf("%w: CRON_DB_BACKUP_MAX_AGE_HOURS must be an integer from %d to %d", ErrInvalidConfiguration, minMaxAgeHours, maxMaxAgeHours)
	}
	return time.Duration(hours) * time.Hour, nil
}

// LoadConfig reads the shared cron backup configuration. Empty state
// directory is intentionally valid and means that cron job evidence is not
// configured; callers can then report not_configured without creating files.
func LoadConfig(engine string) (Config, error) {
	engine = strings.TrimSpace(strings.ToLower(engine))
	if !validEngine(engine) {
		return Config{}, fmt.Errorf("%w: unsupported engine", ErrInvalidConfiguration)
	}

	maxAge, err := ParseMaxAge(os.Getenv(MaxAgeEnv))
	if err != nil {
		return Config{}, err
	}

	directory := strings.TrimSpace(os.Getenv(StateDirectoryEnv))
	if directory == "" {
		return Config{Engine: engine, MaxAge: maxAge}, nil
	}
	if strings.IndexByte(directory, 0) >= 0 {
		return Config{}, fmt.Errorf("%w: state directory contains NUL", ErrInvalidConfiguration)
	}
	absolute, err := filepath.Abs(filepath.Clean(directory))
	if err != nil {
		return Config{}, fmt.Errorf("%w: state directory: %v", ErrInvalidConfiguration, err)
	}
	return Config{StateDirectory: absolute, Engine: engine, MaxAge: maxAge}, nil
}

func validEngine(engine string) bool {
	return engine == "sqlite" || engine == "postgres"
}

func validateConfig(cfg Config) error {
	if cfg.StateDirectory == "" {
		return ErrNotConfigured
	}
	if !filepath.IsAbs(cfg.StateDirectory) || strings.IndexByte(cfg.StateDirectory, 0) >= 0 {
		return fmt.Errorf("%w: state directory must be an absolute path", ErrInvalidConfiguration)
	}
	if !validEngine(cfg.Engine) {
		return fmt.Errorf("%w: unsupported engine", ErrInvalidConfiguration)
	}
	if cfg.MaxAge < minMaxAgeHours*time.Hour || cfg.MaxAge > maxMaxAgeHours*time.Hour || cfg.MaxAge%time.Second != 0 {
		return fmt.Errorf("%w: invalid max age", ErrInvalidConfiguration)
	}
	return nil
}

func normalizedTime(now time.Time) time.Time {
	return now.UTC()
}

func operationTime(now time.Time) (time.Time, error) {
	now = normalizedTime(now)
	if now.IsZero() {
		return time.Time{}, fmt.Errorf("%w: zero operation time", ErrInvalidConfiguration)
	}
	return now, nil
}
