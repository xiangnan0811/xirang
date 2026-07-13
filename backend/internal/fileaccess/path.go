package fileaccess

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

func ParseLocator(raw string, policy Policy) (Locator, error) {
	if !utf8.ValidString(raw) || strings.ContainsRune(raw, '\x00') {
		return Locator{}, fmt.Errorf("%w: invalid encoding", ErrInvalidLocator)
	}
	switch policy.Input {
	case StrictRelativeLocator:
		if raw == "" || raw == "." || filepath.IsAbs(raw) || filepath.VolumeName(raw) != "" || strings.Contains(raw, "\\") || strings.HasSuffix(raw, "/") {
			return Locator{}, fmt.Errorf("%w: strict locator must be relative", ErrInvalidLocator)
		}
		parts := strings.Split(raw, "/")
		for _, part := range parts {
			if part == "" || part == "." || part == ".." {
				return Locator{}, fmt.Errorf("%w: ambiguous path component", ErrInvalidLocator)
			}
		}
		clean := filepath.Clean(filepath.FromSlash(raw))
		if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return Locator{}, fmt.Errorf("%w: strict locator escapes root", ErrInvalidLocator)
		}
		return Locator{Path: clean}, nil
	case LegacyAbsoluteOrRelative:
		if strings.TrimSpace(raw) == "" {
			return Locator{}, fmt.Errorf("%w: empty legacy locator", ErrInvalidLocator)
		}
		return Locator{Path: filepath.Clean(raw)}, nil
	default:
		return Locator{}, fmt.Errorf("%w: unknown input policy", ErrInvalidLocator)
	}
}

func Resolve(root Root, locator Locator, policy Policy) (string, string, error) {
	cleanRoot := filepath.Clean(strings.TrimSpace(root.Path))
	if cleanRoot == "." || !filepath.IsAbs(cleanRoot) {
		return "", "", fmt.Errorf("%w: root must be absolute", ErrOutsideRoot)
	}
	if locator.root {
		return ".", cleanRoot, nil
	}
	if policy.Input == StrictRelativeLocator {
		parsed, err := ParseLocator(locator.Path, policy)
		if err != nil {
			return "", "", err
		}
		absolute := filepath.Join(cleanRoot, parsed.Path)
		if !Contains(cleanRoot, absolute) {
			return "", "", fmt.Errorf("%w", ErrOutsideRoot)
		}
		return parsed.Path, absolute, nil
	}
	if policy.Input != LegacyAbsoluteOrRelative {
		return "", "", fmt.Errorf("%w: unknown input policy", ErrInvalidLocator)
	}
	raw := locator.Path
	if raw == string(filepath.Separator) {
		return ".", cleanRoot, nil
	}
	var absolute string
	if filepath.IsAbs(raw) {
		absolute = filepath.Clean(raw)
	} else {
		absolute = filepath.Join(cleanRoot, raw)
	}
	if !Contains(cleanRoot, absolute) {
		return "", "", fmt.Errorf("%w", ErrOutsideRoot)
	}
	relative, err := filepath.Rel(cleanRoot, absolute)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("%w", ErrOutsideRoot)
	}
	if relative == "" {
		relative = "."
	}
	return relative, absolute, nil
}

func Contains(root, candidate string) bool {
	root = filepath.Clean(root)
	candidate = filepath.Clean(candidate)
	if root == string(filepath.Separator) {
		return filepath.IsAbs(candidate)
	}
	return candidate == root || strings.HasPrefix(candidate, root+string(filepath.Separator))
}
