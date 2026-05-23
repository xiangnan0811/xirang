package nodelogs

import (
	"crypto/sha256"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"xirang/backend/internal/util"
)

const (
	nodeLogPathPrefix         = "[日志路径:"
	nodeLogMessagePlaceholder = "[日志内容已隐藏]"
)

var (
	nodeLogSanitizedPathPattern  = regexp.MustCompile(`^\[日志路径:[0-9a-f]{16}\]$`)
	nodeLogURLPattern            = regexp.MustCompile(`\b[a-zA-Z][a-zA-Z0-9+.-]*://[^\s"'<>，,;。)]+`)
	nodeLogOutputMarkerPattern   = regexp.MustCompile(`(?is)(输出\s*[:：=]|output\s*[:=]|stdout\s*[:=]|stderr\s*[:=])\s*.*`)
	nodeLogRemotePathPattern     = regexp.MustCompile(`\b[^\s"'，,;]+@[^\s"'，,;:]+(?::\d{1,5})?:/[^\s"'，,;]+`)
	nodeLogNamedPathPattern      = regexp.MustCompile(`\b[a-zA-Z0-9_.-]+:[^\s"'，,;]*[/][^\s"'，,;]+`)
	nodeLogAbsolutePathPattern   = regexp.MustCompile(`(^|[\s"'(=：,;，。]|→|: )(/[^\s"'<>，,;。)]+)`)
	nodeLogWindowsPathPattern    = regexp.MustCompile(`\b[A-Za-z]:\\[^\s"'<>，,;。)]+`)
	nodeLogIPv4Pattern           = regexp.MustCompile(`\b(?:[a-zA-Z0-9._%+-]+@)?(?:\d{1,3}\.){3}\d{1,3}(?::\d{1,5})?\b`)
	nodeLogHostnamePattern       = regexp.MustCompile(`\b(?:[a-zA-Z0-9._%+-]+@)?(?:[a-zA-Z0-9-]+\.)+[a-zA-Z]{2,}(?::\d{1,5})?\b`)
	nodeLogHostSensitivePattern  = regexp.MustCompile(`(?i)\b[^\s"'，,;。]*(?:host|node|sandbox|backup|restore|server|tenant|internal|prod|private|secret|token)[^\s"'，,;。]*\b`)
	nodeLogSanitizerSafePrefixes = []string{"stricthostkeychecking", "userknownhostsfile"}
	nodeLogSanitizerSafeTokens   = map[string]struct{}{
		"backup": {}, "endpoint": {}, "endpoints": {}, "host": {}, "hostname": {}, "hostnames": {}, "hosts": {},
		"internal": {}, "node": {}, "nodes": {}, "output": {}, "path": {}, "paths": {}, "private": {},
		"restore": {}, "sandbox": {}, "secret": {}, "server": {}, "task": {}, "tasks": {}, "token": {}, "tokens": {},
	}
)

func SanitizeLogPath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	if nodeLogSanitizedPathPattern.MatchString(trimmed) {
		return trimmed
	}
	cleaned := strings.TrimSpace(sanitizeNodeLogEvidence(trimmed))
	if cleaned == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(trimmed))
	return fmt.Sprintf("%s%x]", nodeLogPathPrefix, sum[:8])
}

func SanitizeLogMessage(message string) string {
	trimmed := strings.TrimSpace(message)
	if trimmed == "" {
		return ""
	}
	cleaned := strings.TrimSpace(sanitizeNodeLogEvidence(trimmed))
	if cleaned == "" {
		return ""
	}
	if cleaned == nodeLogMessagePlaceholder {
		return cleaned
	}
	return nodeLogMessagePlaceholder
}

func sanitizeNodeLogError(err error) string {
	if err == nil {
		return ""
	}
	return sanitizeNodeLogEvidence(err.Error())
}

func sanitizeLogEntries(entries []LogEntry) {
	for i := range entries {
		entries[i].Path = SanitizeLogPath(entries[i].Path)
		entries[i].Message = SanitizeLogMessage(entries[i].Message)
	}
}

func sanitizeNodeLogEvidence(message string) string {
	message = util.SanitizeMessage(message)
	message = nodeLogOutputMarkerPattern.ReplaceAllString(message, "$1 [输出已隐藏]")
	message = nodeLogURLPattern.ReplaceAllStringFunc(message, func(match string) string {
		parsed, err := url.Parse(match)
		if err != nil || parsed.Scheme == "" {
			return "[端点已隐藏]"
		}
		return parsed.Scheme + "://***"
	})
	message = nodeLogRemotePathPattern.ReplaceAllString(message, "[远程路径已隐藏]")
	message = nodeLogNamedPathPattern.ReplaceAllStringFunc(message, func(match string) string {
		parsed, err := url.Parse(match)
		if err == nil && parsed.Scheme != "" {
			return match
		}
		return "[远程路径已隐藏]"
	})
	message = nodeLogAbsolutePathPattern.ReplaceAllString(message, "$1[路径已隐藏]")
	message = nodeLogWindowsPathPattern.ReplaceAllString(message, "[路径已隐藏]")
	message = nodeLogIPv4Pattern.ReplaceAllString(message, "[主机已隐藏]")
	message = nodeLogHostnamePattern.ReplaceAllString(message, "[主机已隐藏]")
	message = sanitizeNodeLogHostSensitiveFragments(message)
	return message
}

func sanitizeNodeLogHostSensitiveFragments(message string) string {
	return nodeLogHostSensitivePattern.ReplaceAllStringFunc(message, func(match string) string {
		if shouldKeepNodeLogRuntimeToken(match) {
			return match
		}
		return "[主机信息已隐藏]"
	})
}

func shouldKeepNodeLogRuntimeToken(value string) bool {
	trimmed := strings.TrimSpace(value)
	lower := strings.ToLower(trimmed)
	if lower == "" {
		return true
	}
	if strings.Contains(lower, "*") || strings.Contains(lower, "[") || strings.Contains(lower, "]") {
		return true
	}
	for _, safePrefix := range nodeLogSanitizerSafePrefixes {
		if strings.HasPrefix(lower, safePrefix) {
			return true
		}
	}
	_, ok := nodeLogSanitizerSafeTokens[lower]
	return ok
}
