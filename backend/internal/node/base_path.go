package node

import (
	"errors"
	"path"
	"strings"
	"unicode/utf8"
)

var errInvalidBasePath = errors.New("base_path must be a valid UTF-8 absolute POSIX path without NUL, .. components, or leading or trailing whitespace")

// ValidateBasePath validates a newly configured base path without changing its bytes.
// An empty path means no base path is configured.
func ValidateBasePath(raw string) error {
	if raw == "" {
		return nil
	}
	if !utf8.ValidString(raw) || raw[0] != '/' || strings.ContainsRune(raw, '\x00') || strings.TrimSpace(raw) != raw {
		return errInvalidBasePath
	}
	remaining := raw
	for {
		component, rest, found := strings.Cut(remaining, "/")
		if component == ".." {
			return errInvalidBasePath
		}
		if !found {
			break
		}
		remaining = rest
	}
	return nil
}

// FileBrowserBasePath preserves the file browser's historical interpretation of stored paths.
func FileBrowserBasePath(stored string) string {
	trimmed := strings.TrimSpace(stored)
	if trimmed == "" {
		return ""
	}
	return path.Clean(trimmed)
}
