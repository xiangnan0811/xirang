package handlers

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/robfig/cron/v3"
)

var standardCronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

func parseID(c *gin.Context, field string) (uint, bool) {
	raw := c.Param(field)
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ID 格式错误"})
		return 0, false
	}
	return uint(id), true
}

func validateCronSpec(raw string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	if _, err := standardCronParser.Parse(trimmed); err != nil {
		return fmt.Errorf("cron 表达式不合法")
	}
	return nil
}

func parseCSVEnvList(key string) []string {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	result := make([]string, 0, len(parts))
	for _, one := range parts {
		value := strings.TrimSpace(one)
		if value == "" {
			continue
		}
		result = append(result, filepath.Clean(value))
	}
	return result
}

func validatePathByPrefix(path string, prefixes []string, label string) error {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return fmt.Errorf("%s 不能为空", label)
	}
	if len(prefixes) == 0 {
		return nil
	}

	normalizedPath := filepath.Clean(trimmed)
	for _, prefix := range prefixes {
		normalizedPrefix := filepath.Clean(strings.TrimSpace(prefix))
		if normalizedPrefix == "." || normalizedPrefix == "" {
			continue
		}
		if normalizedPath == normalizedPrefix || strings.HasPrefix(normalizedPath, normalizedPrefix+string(filepath.Separator)) {
			return nil
		}
	}
	return fmt.Errorf("%s 不在允许路径范围内", label)
}
