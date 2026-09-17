package rsyncconfinement

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	AllowedSourceRootsEnv = "RSYNC_ALLOWED_SOURCE_PREFIXES"
	AllowedTargetRootsEnv = "RSYNC_ALLOWED_TARGET_PREFIXES"
)

// Policy is the shared lexical projection of the Rsync filesystem policy.
// Empty roots deliberately mean unrestricted operation for compatibility.
// Enforcement must still happen in the filesystem that owns each operand.
type Policy struct {
	SourceRoots []string
	TargetRoots []string
}

// LoadPolicyFromEnv parses both configured allowlists. A configured list is a
// security boundary: malformed entries are rejected rather than ignored.
func LoadPolicyFromEnv() (Policy, error) {
	source, err := ParseRoots(os.Getenv(AllowedSourceRootsEnv))
	if err != nil {
		return Policy{}, fmt.Errorf("%s: %w", AllowedSourceRootsEnv, err)
	}
	target, err := ParseRoots(os.Getenv(AllowedTargetRootsEnv))
	if err != nil {
		return Policy{}, fmt.Errorf("%s: %w", AllowedTargetRootsEnv, err)
	}
	return Policy{SourceRoots: source, TargetRoots: target}, nil
}

// ParseRoots parses a comma-separated list of absolute directory boundaries.
// Empty entries are ignored so a trailing comma does not create a boundary.
func ParseRoots(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	roots := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		if err := validatePathSyntax(trimmed); err != nil {
			return nil, err
		}
		clean := filepath.Clean(trimmed)
		if !filepath.IsAbs(clean) {
			return nil, fmt.Errorf("root %q must be an absolute path", trimmed)
		}
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		roots = append(roots, clean)
	}
	return roots, nil
}

func (policy Policy) Configured() bool {
	return len(policy.SourceRoots) != 0 || len(policy.TargetRoots) != 0
}

func (policy Policy) ValidateSource(path, label string) error {
	return ValidatePath(path, policy.SourceRoots, label)
}

func (policy Policy) ValidateTarget(path, label string) error {
	return ValidatePath(path, policy.TargetRoots, label)
}

// ValidatePath applies component-aware lexical containment. It intentionally
// does not resolve symlinks: links are a valid part of an allowed tree and the
// execution helper enforces the same boundary against the owning filesystem.
func ValidatePath(path string, roots []string, label string) error {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return fmt.Errorf("%s 不能为空", label)
	}
	if err := validatePathSyntax(trimmed); err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	if len(roots) == 0 {
		return nil
	}
	cleanPath := filepath.Clean(trimmed)
	if !filepath.IsAbs(cleanPath) {
		return fmt.Errorf("%s 必须是绝对路径", label)
	}
	for _, rawRoot := range roots {
		root := filepath.Clean(strings.TrimSpace(rawRoot))
		if root == "" || root == "." {
			continue
		}
		if !filepath.IsAbs(root) {
			return fmt.Errorf("%s allowlist root %q must be absolute", label, rawRoot)
		}
		relative, err := filepath.Rel(root, cleanPath)
		if err != nil {
			continue
		}
		if relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
			return nil
		}
	}
	return fmt.Errorf("%s 不在允许路径范围内", label)
}

func validatePathSyntax(path string) error {
	if !utf8.ValidString(path) {
		return fmt.Errorf("path is not valid UTF-8")
	}
	for _, r := range path {
		switch r {
		case '\x00':
			return fmt.Errorf("path 不能包含 NUL 字符")
		case '\r', '\n':
			return fmt.Errorf("path 不能包含换行符")
		}
	}
	return nil
}
