package util

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

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

func GetEnvOrDefault(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func IsDevelopmentEnv() bool {
	appEnv := strings.ToLower(strings.TrimSpace(os.Getenv("APP_ENV")))
	environment := strings.ToLower(strings.TrimSpace(os.Getenv("ENVIRONMENT")))
	ginMode := strings.ToLower(strings.TrimSpace(os.Getenv("GIN_MODE")))
	return appEnv == "development" || environment == "development" || ginMode == "debug"
}

func IsProductionEnv() bool {
	appEnv := strings.ToLower(strings.TrimSpace(os.Getenv("APP_ENV")))
	environment := strings.ToLower(strings.TrimSpace(os.Getenv("ENVIRONMENT")))
	ginMode := strings.ToLower(strings.TrimSpace(os.Getenv("GIN_MODE")))
	return appEnv == "production" || environment == "production" || ginMode == "release"
}
