package executor

import (
	"net/url"
	"regexp"
	"strings"

	"xirang/backend/internal/util"
)

var (
	executorURLPattern           = regexp.MustCompile(`\b[a-zA-Z][a-zA-Z0-9+.-]*://[^\s"'<>，,;。)]+`)
	executorOutputMarkerPattern  = regexp.MustCompile(`(?is)(输出\s*[:：=]|output\s*[:=]|stdout\s*[:=]|stderr\s*[:=])\s*.*`)
	executorRemotePathPattern    = regexp.MustCompile(`\b[^\s"'，,;]+@[^\s"'，,;:]+(?::\d{1,5})?:/[^\s"'，,;]+`)
	executorNamedPathPattern     = regexp.MustCompile(`\b[a-zA-Z0-9_.-]+:[^\s"'，,;]*[/][^\s"'，,;]+`)
	executorAbsolutePathPattern  = regexp.MustCompile(`(^|[\s"'(=：,;，。]|→|: )(/[^\s"'<>，,;。)]+)`)
	executorWindowsPathPattern   = regexp.MustCompile(`\b[A-Za-z]:\\[^\s"'<>，,;。)]+`)
	executorIPv4Pattern          = regexp.MustCompile(`\b(?:[a-zA-Z0-9._%+-]+@)?(?:\d{1,3}\.){3}\d{1,3}(?::\d{1,5})?\b`)
	executorHostnamePattern      = regexp.MustCompile(`\b(?:[a-zA-Z0-9._%+-]+@)?(?:[a-zA-Z0-9-]+\.)+[a-zA-Z]{2,}(?::\d{1,5})?\b`)
	executorHostSensitivePattern = regexp.MustCompile(`(?i)\b[^\s"'，,;。]*(?:host|node|sandbox|backup|restore|server|tenant|internal|prod|private|secret|token)[^\s"'，,;。]*\b`)
)

func sanitizeExecutorRuntimeOutput(output string) string {
	if strings.TrimSpace(output) == "" {
		return ""
	}
	return "[输出已隐藏]"
}

func sanitizeExecutorRuntimeEvidence(message string) string {
	message = util.SanitizeMessage(message)
	message = executorOutputMarkerPattern.ReplaceAllString(message, "$1 [输出已隐藏]")
	message = executorURLPattern.ReplaceAllStringFunc(message, func(match string) string {
		parsed, err := url.Parse(match)
		if err != nil || parsed.Scheme == "" {
			return "[端点已隐藏]"
		}
		return parsed.Scheme + "://***"
	})
	message = executorRemotePathPattern.ReplaceAllString(message, "[远程路径已隐藏]")
	message = executorNamedPathPattern.ReplaceAllStringFunc(message, func(match string) string {
		parsed, err := url.Parse(match)
		if err == nil && parsed.Scheme != "" {
			return match
		}
		return "[远程路径已隐藏]"
	})
	message = executorAbsolutePathPattern.ReplaceAllString(message, "$1[路径已隐藏]")
	message = executorWindowsPathPattern.ReplaceAllString(message, "[路径已隐藏]")
	message = executorIPv4Pattern.ReplaceAllString(message, "[主机已隐藏]")
	message = executorHostnamePattern.ReplaceAllString(message, "[主机已隐藏]")
	message = sanitizeExecutorHostSensitiveFragments(message)
	return message
}

func sanitizeExecutorHostSensitiveFragments(message string) string {
	return executorHostSensitivePattern.ReplaceAllStringFunc(message, func(match string) string {
		if shouldKeepExecutorRuntimeToken(match) {
			return match
		}
		return "[主机信息已隐藏]"
	})
}

func shouldKeepExecutorRuntimeToken(value string) bool {
	trimmed := strings.TrimSpace(value)
	lower := strings.ToLower(trimmed)
	if lower == "" {
		return true
	}
	if strings.Contains(lower, "*") || strings.Contains(lower, "[") || strings.Contains(lower, "]") {
		return true
	}
	for _, safePrefix := range []string{"stricthostkeychecking", "userknownhostsfile"} {
		if strings.HasPrefix(lower, safePrefix) {
			return true
		}
	}
	for _, safe := range []string{
		"backup", "post-hook", "pre-hook", "task", "tasks", "restore", "restic", "rclone",
		"hostname", "hostnames", "host", "hosts", "sandbox", "node", "nodes", "endpoint", "endpoints",
		"token", "tokens", "private", "server", "internal", "output", "secret", "path", "paths",
	} {
		if lower == safe {
			return true
		}
	}
	return false
}
