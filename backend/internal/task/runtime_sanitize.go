package task

import (
	"net/url"
	"regexp"
	"strings"

	"xirang/backend/internal/util"
)

var (
	taskURLPattern              = regexp.MustCompile(`\b[a-zA-Z][a-zA-Z0-9+.-]*://[^\s"'<>，,;。)]+`)
	taskCommandLifecyclePattern = regexp.MustCompile(`(?is)(执行命令|在远程节点执行|执行\s+(?:restic|rclone)\s+check|command|cmd)\s*[:：=]\s*.*`)
	taskOutputMarkerPattern     = regexp.MustCompile(`(?is)(输出\s*[:：=]|output\s*[:=]|stdout\s*[:=]|stderr\s*[:=])\s*.*`)
	taskRemotePathPattern       = regexp.MustCompile(`\b[^\s"'，,;]+@[^\s"'，,;:]+(?::\d{1,5})?:/[^\s"'，,;]+`)
	taskNamedPathPattern        = regexp.MustCompile(`\b[a-zA-Z0-9_.-]+:[^\s"'，,;]*[/][^\s"'，,;]+`)
	taskAbsolutePathPattern     = regexp.MustCompile(`(^|[\s"'(=：,;，。]|→|: )(/[^\s"'<>，,;。)]+)`)
	taskWindowsPathPattern      = regexp.MustCompile(`\b[A-Za-z]:\\[^\s"'<>，,;。)]+`)
	taskIPv4Pattern             = regexp.MustCompile(`\b(?:[a-zA-Z0-9._%+-]+@)?(?:\d{1,3}\.){3}\d{1,3}(?::\d{1,5})?\b`)
	taskHostnamePattern         = regexp.MustCompile(`\b(?:[a-zA-Z0-9._%+-]+@)?(?:[a-zA-Z0-9-]+\.)+[a-zA-Z]{2,}(?::\d{1,5})?\b`)
	taskHostSensitivePattern    = regexp.MustCompile(`(?i)\b[^\s"'，,;。]*(?:host|node|sandbox|backup|restore|server|tenant|internal|prod|private|secret|token)[^\s"'，,;。]*\b`)
)

func sanitizeTaskLogMessage(message string) string {
	return sanitizeTaskRuntimeEvidence(message)
}

// SanitizeRuntimeEvidenceForRead sanitizes stored task evidence for API reads.
func SanitizeRuntimeEvidenceForRead(message string) string {
	return sanitizeTaskRuntimeEvidence(message)
}

func sanitizeTaskLastError(message string) string {
	return sanitizeTaskRuntimeEvidence(message)
}

func sanitizeTaskRuntimeError(err error) string {
	if err == nil {
		return ""
	}
	return sanitizeTaskLastError(err.Error())
}

func sanitizeTaskRuntimeOutput(output string) string {
	if strings.TrimSpace(output) == "" {
		return ""
	}
	return "[输出已隐藏]"
}

func sanitizeTaskRuntimeEvidence(message string) string {
	message = util.SanitizeMessage(message)
	message = taskOutputMarkerPattern.ReplaceAllString(message, "$1 [输出已隐藏]")
	message = taskURLPattern.ReplaceAllStringFunc(message, func(match string) string {
		parsed, err := url.Parse(match)
		if err != nil || parsed.Scheme == "" {
			return "[端点已隐藏]"
		}
		return parsed.Scheme + "://***"
	})
	message = taskCommandLifecyclePattern.ReplaceAllString(message, "$1: [命令已隐藏]")
	message = taskRemotePathPattern.ReplaceAllString(message, "[远程路径已隐藏]")
	message = taskNamedPathPattern.ReplaceAllStringFunc(message, func(match string) string {
		if strings.Contains(match, "://") {
			return match
		}
		return "[远程路径已隐藏]"
	})
	message = taskAbsolutePathPattern.ReplaceAllString(message, "$1[路径已隐藏]")
	message = taskWindowsPathPattern.ReplaceAllString(message, "[路径已隐藏]")
	message = taskIPv4Pattern.ReplaceAllString(message, "[主机已隐藏]")
	message = taskHostnamePattern.ReplaceAllString(message, "[主机已隐藏]")
	message = sanitizeTaskHostSensitiveFragments(message)
	return message
}

func sanitizeTaskHostSensitiveFragments(message string) string {
	return taskHostSensitivePattern.ReplaceAllStringFunc(message, func(match string) string {
		if shouldKeepTaskRuntimeToken(match) {
			return match
		}
		return "[主机信息已隐藏]"
	})
}

func shouldKeepTaskRuntimeToken(value string) bool {
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
