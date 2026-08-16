package sshutil

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"xirang/backend/internal/model"
)

const (
	PurposeSSHKeyTest                     = "ssh_key_test"
	PurposeSSHKeyExport                   = "ssh_key_export"
	PurposeNodeTest                       = "node_test"
	PurposeTerminal                       = "terminal"
	PurposeTaskCommand                    = "task_command"
	PurposeBatchCommand                   = "batch_command"
	PurposeDrill                          = "drill"
	PurposeProbe                          = "probe"
	PurposeFileBrowser                    = "file_browser"
	PurposeDockerVolumes                  = "docker_volumes"
	PurposeNodeLogs                       = "node_logs"
	PurposeTaskBackup                     = "task_backup"
	PurposeTaskRestore                    = "task_restore"
	PurposeTaskHook                       = "task_hook"
	PurposeSnapshot                       = "snapshot"
	PurposeSnapshotDiff                   = "snapshot_diff"
	PurposeIntegrityCheck                 = "integrity_check"
	PurposeRetention                      = "retention"
	PurposeNodeMigration                  = "node_migration"
	PurposeRepositoryProbe                = "repository_probe"
	PurposeRepositoryList                 = "repository_list"
	PurposeRepositoryRead                 = "repository_read"
	PurposeRecoveryPreflight              = "recovery_preflight"
	PurposeRecoveryWrite                  = "recovery_write"
	PurposeRecoveryVerify                 = "recovery_verify"
	PurposeRecoveryResultRead             = "recovery_result_read"
	PurposeRecoveryCleanup                = "recovery_cleanup"
	PurposeRecoveryReconcile              = "recovery_reconcile"
	PurposeRecoveryTargetRootRegistration = "recovery_target_root_registration"
)

var KnownPurposes = []string{
	PurposeSSHKeyTest,
	PurposeSSHKeyExport,
	PurposeNodeTest,
	PurposeTerminal,
	PurposeTaskCommand,
	PurposeBatchCommand,
	PurposeDrill,
	PurposeProbe,
	PurposeFileBrowser,
	PurposeDockerVolumes,
	PurposeNodeLogs,
	PurposeTaskBackup,
	PurposeTaskRestore,
	PurposeTaskHook,
	PurposeSnapshot,
	PurposeSnapshotDiff,
	PurposeIntegrityCheck,
	PurposeRetention,
	PurposeNodeMigration,
	PurposeRepositoryProbe,
	PurposeRepositoryList,
	PurposeRepositoryRead,
	PurposeRecoveryPreflight,
	PurposeRecoveryWrite,
	PurposeRecoveryVerify,
	PurposeRecoveryResultRead,
	PurposeRecoveryCleanup,
	PurposeRecoveryReconcile,
	PurposeRecoveryTargetRootRegistration,
}

// ResolvedCredential describes which credential source was selected without
// exposing the secret material.
type ResolvedCredential struct {
	Kind     string
	Source   string
	Provider string
	KeyID    *uint
}

func NormalizePurpose(purpose string) string {
	trimmed := strings.ToLower(strings.TrimSpace(purpose))
	for _, known := range KnownPurposes {
		if trimmed == known {
			return trimmed
		}
	}
	return trimmed
}

func NormalizeCSVList(raw string) string {
	items := normalizeList(raw)
	if len(items) == 0 {
		return ""
	}
	return strings.Join(items, ",")
}

func NormalizePurposeList(raw string) (string, error) {
	items := normalizeList(raw)
	if len(items) == 0 {
		return "", nil
	}
	known := make(map[string]struct{}, len(KnownPurposes))
	for _, purpose := range KnownPurposes {
		known[purpose] = struct{}{}
	}
	for i, item := range items {
		item = NormalizePurpose(item)
		if _, ok := known[item]; !ok {
			return "", fmt.Errorf("不支持的 SSH Key 用途: %s", item)
		}
		items[i] = item
	}
	return strings.Join(dedupe(items), ","), nil
}

func NormalizeNodeIDList(raw string) (string, error) {
	items := normalizeList(raw)
	if len(items) == 0 {
		return "", nil
	}
	out := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		parsed, err := strconv.ParseUint(item, 10, 64)
		if err != nil || parsed == 0 {
			return "", fmt.Errorf("SSH Key 节点范围包含无效 ID")
		}
		value := strconv.FormatUint(parsed, 10)
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return strings.Join(out, ","), nil
}

func NormalizeTagList(raw string) string {
	return strings.Join(dedupe(normalizeList(raw)), ",")
}

func ValidateSSHKeyScope(key model.SSHKey, node model.Node, purpose string) error {
	if err := ValidateSSHKeyPurpose(key, purpose); err != nil {
		return err
	}
	if !nodeIDAllowed(key.AllowedNodeIDs, node.ID) {
		return fmt.Errorf("SSH Key 不允许用于该节点")
	}
	if !nodeTagsAllowed(key.AllowedNodeTags, node.Tags) {
		return fmt.Errorf("SSH Key 不允许用于该节点标签")
	}
	return nil
}

func ValidateSSHKeyPurpose(key model.SSHKey, purpose string) error {
	if key.Disabled {
		return fmt.Errorf("SSH Key 已禁用")
	}
	if key.ExpiresAt != nil && !key.ExpiresAt.After(time.Now().UTC()) {
		return fmt.Errorf("SSH Key 已过期")
	}
	purpose = NormalizePurpose(purpose)
	if purpose != "" && !scopeContains(key.AllowedPurposes, purpose) {
		return fmt.Errorf("SSH Key 不允许用于当前操作")
	}
	return nil
}

func IsBroadScope(key model.SSHKey) bool {
	return strings.TrimSpace(key.AllowedPurposes) == "" ||
		strings.TrimSpace(key.AllowedNodeIDs) == "" && strings.TrimSpace(key.AllowedNodeTags) == ""
}

func nodeIDAllowed(raw string, nodeID uint) bool {
	allowed := normalizeList(raw)
	if len(allowed) == 0 {
		return true
	}
	if nodeID == 0 {
		return false
	}
	want := strconv.FormatUint(uint64(nodeID), 10)
	for _, item := range allowed {
		if item == want {
			return true
		}
	}
	return false
}

func nodeTagsAllowed(allowedRaw, nodeTagsRaw string) bool {
	allowed := normalizeList(allowedRaw)
	if len(allowed) == 0 {
		return true
	}
	nodeTags := normalizeList(nodeTagsRaw)
	if len(nodeTags) == 0 {
		return false
	}
	return anyTagMatch(allowed, nodeTags)
}

func scopeContains(raw string, value string) bool {
	items := normalizeList(raw)
	if len(items) == 0 {
		return true
	}
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}

func normalizeList(raw string) []string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "[]" || trimmed == "null" {
		return nil
	}
	var jsonItems []string
	if strings.HasPrefix(trimmed, "[") && json.Unmarshal([]byte(trimmed), &jsonItems) == nil {
		return dedupe(cleanList(jsonItems))
	}
	parts := strings.FieldsFunc(trimmed, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r' || r == '\t' || r == ';'
	})
	return dedupe(cleanList(parts))
}

func cleanList(items []string) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(strings.Trim(item, `"'`))
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}

func dedupe(items []string) []string {
	out := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func anyTagMatch(wanted, have []string) bool {
	idx := make(map[string]struct{}, len(have))
	for _, tag := range have {
		idx[tag] = struct{}{}
	}
	for _, tag := range wanted {
		if _, ok := idx[tag]; ok {
			return true
		}
	}
	return false
}
