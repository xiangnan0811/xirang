package util

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ReadBoolEnv reads a boolean environment variable, returning defaultValue
// when the variable is empty. Returns an error for non-boolean values.
func ReadBoolEnv(key string, defaultValue bool) (bool, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return defaultValue, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s 必须是 true/false", key)
	}
	return value, nil
}

// ExpandHomePath expands a leading ~ or ~/ in path to the user's home
// directory. Returns the path unchanged if no expansion is needed.
func ExpandHomePath(path string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", nil
	}
	if trimmed == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return home, nil
	}
	if strings.HasPrefix(trimmed, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, strings.TrimPrefix(trimmed, "~/")), nil
	}
	return trimmed, nil
}

// GetEnvOrDefault returns the trimmed value of the given environment variable,
// or fallback if the variable is unset or empty (after trimming).
func GetEnvOrDefault(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

// appEnvironment returns APP_ENV if set, else ENVIRONMENT. Empty if neither set.
// GIN_MODE is not consulted here — see IsProductionEnv for release-mode legacy.
func appEnvironment() string {
	if v := strings.ToLower(strings.TrimSpace(os.Getenv("APP_ENV"))); v != "" {
		return v
	}
	return strings.ToLower(strings.TrimSpace(os.Getenv("ENVIRONMENT")))
}

// IsDevelopmentEnv returns true only when APP_ENV or ENVIRONMENT is explicitly
// "development" (APP_ENV wins). GIN_MODE=debug never enables development
// secret relaxations.
func IsDevelopmentEnv() bool {
	return appEnvironment() == "development"
}

// IsProductionEnv returns true when security hardening for production should apply.
//
// Only explicit APP_ENV/ENVIRONMENT=development opts out. Everything else —
// including undeclared env, production/prod/staging aliases, and unknown
// labels — fail closed as production so Swagger defaults off and
// METRICS_TOKEN / CORS * protections remain active without requiring
// GIN_MODE=release. APP_ENV=development always wins over GIN_MODE=release.
func IsProductionEnv() bool {
	return !IsDevelopmentEnv()
}

// IsGinDebug reports whether GIN_MODE is "debug".
func IsGinDebug() bool {
	return strings.ToLower(strings.TrimSpace(os.Getenv("GIN_MODE"))) == "debug"
}

// IsGinRelease reports whether GIN_MODE is "release".
func IsGinRelease() bool {
	return strings.ToLower(strings.TrimSpace(os.Getenv("GIN_MODE"))) == "release"
}
