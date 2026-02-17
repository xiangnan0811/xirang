package util

import "strings"

func IsRemotePathSpec(path string) bool {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return false
	}
	if strings.HasPrefix(trimmed, "rsync://") {
		return true
	}
	if len(trimmed) >= 3 {
		letter := trimmed[0]
		if ((letter >= 'a' && letter <= 'z') || (letter >= 'A' && letter <= 'Z')) && trimmed[1] == ':' {
			return false
		}
	}
	colon := strings.Index(trimmed, ":")
	slash := strings.Index(trimmed, "/")
	return colon > 0 && (slash < 0 || colon < slash)
}
