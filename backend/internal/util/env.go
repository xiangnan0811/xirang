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

// IsDevelopmentEnv returns true when APP_ENV, ENVIRONMENT, or GIN_MODE
// indicates a development/debug runtime.
func IsDevelopmentEnv() bool {
	appEnv := strings.ToLower(strings.TrimSpace(os.Getenv("APP_ENV")))
	environment := strings.ToLower(strings.TrimSpace(os.Getenv("ENVIRONMENT")))
	ginMode := strings.ToLower(strings.TrimSpace(os.Getenv("GIN_MODE")))
	return appEnv == "development" || environment == "development" || ginMode == "debug"
}

// IsProductionEnv returns true when APP_ENV, ENVIRONMENT, or GIN_MODE
// indicates a production/release runtime.
func IsProductionEnv() bool {
	appEnv := strings.ToLower(strings.TrimSpace(os.Getenv("APP_ENV")))
	environment := strings.ToLower(strings.TrimSpace(os.Getenv("ENVIRONMENT")))
	ginMode := strings.ToLower(strings.TrimSpace(os.Getenv("GIN_MODE")))
	return appEnv == "production" || environment == "production" || ginMode == "release"
}
