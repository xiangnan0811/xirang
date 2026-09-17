package policy

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// TargetNamespace is reserved for newly-created policy/node backup targets.
// Historical targets remain in Task.RsyncTarget and are never inferred into
// this namespace.
const TargetNamespace = ".xirang"

// TargetOwner identifies the logical writer that owns one physical target.
// PolicyID+NodeID identify policy-managed targets. TaskID identifies a manual
// task when PolicyID is zero.
type TargetOwner struct {
	PolicyID uint
	NodeID   uint
	TaskID   uint
	Target   string
}

// IsCoreLocalTarget reports whether a task target is a path on the Core
// filesystem. Restic repositories and rclone destinations use the same model
// field but are interpreted on the node/provider side, so their absolute paths
// must not participate in Core target ownership checks.
func IsCoreLocalTarget(executorType, target string) bool {
	return strings.EqualFold(strings.TrimSpace(executorType), "rsync") &&
		filepath.IsAbs(strings.TrimSpace(target)) &&
		!strings.Contains(strings.TrimSpace(target), ":")
}

// CanonicalTargetPath returns an absolute, cleaned path with every existing
// symlink component resolved. If the leaf does not exist, the nearest existing
// ancestor is resolved and the missing suffix is appended. This lets callers
// reject aliases and ancestor/descendant overlaps before creating a target.
func CanonicalTargetPath(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("target path is empty")
	}
	if !filepath.IsAbs(raw) {
		return "", fmt.Errorf("target path must be an absolute local path")
	}

	absolute, err := filepath.Abs(filepath.Clean(raw))
	if err != nil {
		return "", fmt.Errorf("canonicalize target path: %w", err)
	}
	probe := absolute
	missing := make([]string, 0, 4)
	for {
		_, statErr := os.Lstat(probe)
		if statErr == nil {
			resolved, resolveErr := filepath.EvalSymlinks(probe)
			if resolveErr != nil {
				return "", fmt.Errorf("resolve target path alias: %w", resolveErr)
			}
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return filepath.Clean(resolved), nil
		}
		if !os.IsNotExist(statErr) {
			return "", fmt.Errorf("inspect target path: %w", statErr)
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return absolute, nil
		}
		missing = append(missing, filepath.Base(probe))
		probe = parent
	}
}

// ManualNodeTargetPath derives an isolated default for a standalone task that
// has no policy identity. Policy tasks should always use PolicyNodeTargetPath.
func ManualNodeTargetPath(basePath string, nodeID uint) string {
	if nodeID == 0 {
		return ""
	}
	base, err := CanonicalTargetPath(basePath)
	if err != nil || base == string(filepath.Separator) {
		return ""
	}
	return filepath.Join(base, TargetNamespace, "manual", "nodes", fmt.Sprintf("%d", nodeID))
}

// PolicyNodeTargetPath derives the isolated target used for newly-created
// policy tasks. Policy and node IDs are persisted identities, so a mutable
// BackupDir/name can never repoint a task to another policy's data.
func PolicyNodeTargetPath(basePath string, policyID, nodeID uint) string {
	if policyID == 0 || nodeID == 0 {
		return ""
	}
	base, err := CanonicalTargetPath(basePath)
	if err != nil || base == string(filepath.Separator) {
		return ""
	}
	return filepath.Join(base, TargetNamespace, "policies", fmt.Sprintf("%d", policyID), "nodes", fmt.Sprintf("%d", nodeID))
}

func targetsOverlap(a, b string) bool {
	if a == b {
		return true
	}
	if rel, err := filepath.Rel(a, b); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
		return true
	}
	if rel, err := filepath.Rel(b, a); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
		return true
	}
	return false
}

func sameTargetOwner(a, b TargetOwner) bool {
	return a.TaskID != 0 && b.TaskID != 0 && a.TaskID == b.TaskID
}

// ValidateTargetOwnership canonicalizes target and compares it with all known
// local targets. Exact reuse by the same logical owner is allowed; any
// canonical equality, alias, ancestor, or descendant owned by another task is
// rejected before the caller performs filesystem I/O.
func ValidateTargetOwnership(target string, owner TargetOwner, existing []TargetOwner) (string, error) {
	canonical, err := CanonicalTargetPath(target)
	if err != nil {
		return "", err
	}
	for _, candidate := range existing {
		if strings.TrimSpace(candidate.Target) == "" {
			continue
		}
		other, canonicalErr := CanonicalTargetPath(candidate.Target)
		if canonicalErr != nil {
			return "", fmt.Errorf("cannot prove target ownership for %q: %w", candidate.Target, canonicalErr)
		}
		if !targetsOverlap(canonical, other) {
			continue
		}
		if canonical == other && sameTargetOwner(owner, candidate) {
			continue
		}
		return "", fmt.Errorf("target path %q overlaps existing target %q", target, candidate.Target)
	}
	return canonical, nil
}

// ValidateTargetOwners validates a complete set in one pass. It is useful for
// policy synchronization and migration, where no writes may occur until all
// current and proposed targets are known to be isolated.
func ValidateTargetOwners(owners []TargetOwner) error {
	for i := range owners {
		if strings.TrimSpace(owners[i].Target) == "" {
			continue
		}
		canonical, err := CanonicalTargetPath(owners[i].Target)
		if err != nil {
			return fmt.Errorf("target %q is not safely addressable: %w", owners[i].Target, err)
		}
		owners[i].Target = canonical
		for j := 0; j < i; j++ {
			if strings.TrimSpace(owners[j].Target) == "" {
				continue
			}
			if targetsOverlap(canonical, owners[j].Target) && (canonical != owners[j].Target || !sameTargetOwner(owners[i], owners[j])) {
				return fmt.Errorf("target path %q overlaps target %q", owners[i].Target, owners[j].Target)
			}
		}
	}
	return nil
}
