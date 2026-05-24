package verifier

import (
	"net/url"
	"regexp"
	"strings"

	"xirang/backend/internal/util"
)

var (
	verifierURLPattern           = regexp.MustCompile(`\b[a-zA-Z][a-zA-Z0-9+.-]*://[^\s"'<>，,;。)]+`)
	verifierOutputMarkerPattern  = regexp.MustCompile(`(?is)(输出\s*[:：=]|output\s*[:=]|stdout\s*[:=]|stderr\s*[:=])\s*.*`)
	verifierRemotePathPattern    = regexp.MustCompile(`\b[^\s"'，,;]+@[^\s"'，,;:]+(?::\d{1,5})?:/[^\s"'，,;]+`)
	verifierNamedPathPattern     = regexp.MustCompile(`\b[a-zA-Z0-9_.-]+:[^\s"'，,;]*[/][^\s"'，,;]+`)
	verifierAbsolutePathPattern  = regexp.MustCompile(`(^|[\s"'(=：,;，。]|→|: )(/[^\s"'<>，,;。)]+)`)
	verifierWindowsPathPattern   = regexp.MustCompile(`\b[A-Za-z]:\\[^\s"'<>，,;。)]+`)
	verifierIPv4Pattern          = regexp.MustCompile(`\b(?:[a-zA-Z0-9._%+-]+@)?(?:\d{1,3}\.){3}\d{1,3}(?::\d{1,5})?\b`)
	verifierHostnamePattern      = regexp.MustCompile(`\b(?:[a-zA-Z0-9._%+-]+@)?(?:[a-zA-Z0-9-]+\.)+[a-zA-Z]{2,}(?::\d{1,5})?\b`)
	verifierHostSensitivePattern = regexp.MustCompile(`(?i)\b[^\s"'，,;。]*(?:host|node|sandbox|backup|restore|server|tenant|internal|prod|private|secret|token)[^\s"'，,;。]*\b`)
)

func sanitizeVerifierRuntimeEvidence(message string) string {
	message = util.SanitizeMessage(message)
	message = verifierOutputMarkerPattern.ReplaceAllString(message, "$1 [输出已隐藏]")
	message = verifierURLPattern.ReplaceAllStringFunc(message, func(match string) string {
		parsed, err := url.Parse(match)
		if err != nil || parsed.Scheme == "" {
			return "[端点已隐藏]"
		}
		return parsed.Scheme + "://***"
	})
	message = verifierRemotePathPattern.ReplaceAllString(message, "[远程路径已隐藏]")
	message = verifierNamedPathPattern.ReplaceAllStringFunc(message, func(match string) string {
		if strings.Contains(match, "://") {
			return match
		}
		return "[远程路径已隐藏]"
	})
	message = verifierAbsolutePathPattern.ReplaceAllString(message, "$1[路径已隐藏]")
	message = verifierWindowsPathPattern.ReplaceAllString(message, "[路径已隐藏]")
	message = verifierIPv4Pattern.ReplaceAllString(message, "[主机已隐藏]")
	message = verifierHostnamePattern.ReplaceAllString(message, "[主机已隐藏]")
	message = sanitizeVerifierHostSensitiveFragments(message)
	return message
}

func sanitizeVerifierHostSensitiveFragments(message string) string {
	return verifierHostSensitivePattern.ReplaceAllStringFunc(message, func(match string) string {
		if shouldKeepVerifierRuntimeToken(match) {
			return match
		}
		return "[主机信息已隐藏]"
	})
}

func shouldKeepVerifierRuntimeToken(value string) bool {
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
