package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	TaskRunResourceProviderRclone = "rclone"
	TaskRunResourceStateReserved  = "reserved"
	TaskRunResourceStateWriting   = "writing"
	TaskRunResourceStateUnknown   = "unknown"
	TaskRunResourceStateReleased  = "released"
)

// TaskRunResourceIdentity is the immutable, non-secret identity of a mutable
// provider destination used by a TaskRun. The namespace intentionally uses the
// durable node ID rather than credentials so SSH password/key rotation cannot
// evade an unresolved write hold. Remote names are case-sensitive; only the
// backend/remote name is used for the collision key, conservatively treating
// all subpaths of one configured remote as one write domain.
type TaskRunResourceIdentity struct {
	Key       string `json:"key"`
	Provider  string `json:"provider"`
	NodeID    uint   `json:"node_id"`
	Namespace string `json:"namespace"`
	Locator   string `json:"locator"`
}

// TaskRunResourceIdentityForTask returns a resource identity only for a legacy
// mutable Rclone backup task. It is safe to call on a task loaded from the
// current database: an empty publication mode is the historical legacy mode,
// while managed/versioned modes are deliberately excluded.
func TaskRunResourceIdentityForTask(task Task) (TaskRunResourceIdentity, bool) {
	if task.NodeID == 0 || !strings.EqualFold(strings.TrimSpace(task.ExecutorType), TaskRunResourceProviderRclone) {
		return TaskRunResourceIdentity{}, false
	}
	if strings.TrimSpace(task.RsyncSource) == "" || strings.TrimSpace(task.RsyncTarget) == "" {
		return TaskRunResourceIdentity{}, false
	}
	if !isLegacyMutableRcloneConfig(task.ExecutorConfig) {
		return TaskRunResourceIdentity{}, false
	}
	locator := strings.TrimSpace(task.RsyncTarget)
	remoteName := locator
	if separator := strings.IndexByte(locator, ':'); separator > 0 {
		// Rclone remote names are case-sensitive. Preserve bytes exactly and
		// intentionally omit the path to conservatively fence overlapping
		// subpaths under the same backend remote.
		remoteName = locator[:separator]
	}
	namespace := fmt.Sprintf("node:%d", task.NodeID)
	keyDigest := sha256.Sum256([]byte(TaskRunResourceProviderRclone + "\x00" + namespace + "\x00" + remoteName))
	key := hex.EncodeToString(keyDigest[:])
	return TaskRunResourceIdentity{
		Key:       key,
		Provider:  TaskRunResourceProviderRclone,
		NodeID:    task.NodeID,
		Namespace: namespace,
		Locator:   locator,
	}, true
}

func isLegacyMutableRcloneConfig(raw string) bool {
	if strings.TrimSpace(raw) == "" {
		return true
	}
	var config struct {
		PublicationMode string `json:"publication_mode"`
	}
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		return false
	}
	return strings.TrimSpace(config.PublicationMode) == "" || config.PublicationMode == "legacy_mutable"
}

// SetTaskRunResourceIdentity freezes the identity into a TaskRun before it is
// persisted. Existing non-empty identity fields are never rebound.
func SetTaskRunResourceIdentity(run *TaskRun, task Task) {
	if run == nil || strings.TrimSpace(run.ResourceKey) != "" {
		return
	}
	if !isBackupTaskRunTriggerForResource(run.TriggerType) {
		return
	}
	identity, ok := TaskRunResourceIdentityForTask(task)
	if !ok {
		return
	}
	run.ResourceKey = identity.Key
	run.ResourceProvider = identity.Provider
	run.ResourceNodeID = identity.NodeID
	run.ResourceNamespace = identity.Namespace
	run.ResourceLocator = identity.Locator
	// Persist a compact immutable evidence envelope. It intentionally contains
	// only public identity values, never executor configuration or credentials.
	run.ResourceEvidence = fmt.Sprintf(`{"provider":%q,"node_id":%d,"namespace":%q,"locator":%q}`,
		identity.Provider, identity.NodeID, identity.Namespace, identity.Locator)
}

func isBackupTaskRunTriggerForResource(trigger string) bool {
	switch strings.ToLower(strings.TrimSpace(trigger)) {
	case "restore", "drill":
		return false
	default:
		return true
	}
}
