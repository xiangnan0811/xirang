package model

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"xirang/backend/internal/secure"

	"gorm.io/gorm"
)

const (
	TaskRunNodeIDLegacyUnknown uint = 0

	TaskRunStatusPending  = "pending"
	TaskRunStatusRunning  = "running"
	TaskRunStatusRetrying = "retrying"
	TaskRunStatusSuccess  = "success"
	TaskRunStatusFailed   = "failed"
	TaskRunStatusCanceled = "canceled"
	TaskRunStatusWarning  = "warning"
	TaskRunStatusSkipped  = "skipped"

	TaskRunCaptureLayoutDirectoryRoot     = "directory_root"
	TaskRunCaptureLayoutDirectoryContents = "directory_contents"
	TaskRunCaptureLayoutSingleFile        = "single_file"

	TaskRunGenerationStateDirty    = "dirty"
	TaskRunGenerationStateVerified = "verified"
	TaskRunGenerationStateWriting  = "writing"
	TaskRunGenerationStateUnknown  = "unknown"
	TaskRunGenerationStateNoStart  = "no_start"
)

var (
	taskRunActiveStatuses = [...]string{
		TaskRunStatusPending,
		TaskRunStatusRunning,
		TaskRunStatusRetrying,
	}
	taskRunTerminalStatuses = [...]string{
		TaskRunStatusSuccess,
		TaskRunStatusFailed,
		TaskRunStatusCanceled,
		TaskRunStatusWarning,
		TaskRunStatusSkipped,
	}
)

func TaskRunActiveStatuses() []string {
	return append([]string(nil), taskRunActiveStatuses[:]...)
}

func TaskRunTerminalStatuses() []string {
	return append([]string(nil), taskRunTerminalStatuses[:]...)
}

func IsActiveTaskRunStatus(status string) bool {
	for _, active := range taskRunActiveStatuses {
		if status == active {
			return true
		}
	}
	return false
}

func IsTerminalTaskRunStatus(status string) bool {
	for _, terminal := range taskRunTerminalStatuses {
		if status == terminal {
			return true
		}
	}
	return false
}

func IsKnownTaskRunStatus(status string) bool {
	return IsActiveTaskRunStatus(status) || IsTerminalTaskRunStatus(status)
}

// IsTaskRunNodeSnapshotAuthoritative separates ordinary positive node identity
// from the migration-owned legacy_unknown terminal-history sentinel.
func IsTaskRunNodeSnapshotAuthoritative(nodeID uint) bool {
	return nodeID > TaskRunNodeIDLegacyUnknown
}

type Task struct {
	ID                 uint       `gorm:"primaryKey" json:"id"`
	Name               string     `gorm:"size:128;not null" json:"name"`
	NodeID             uint       `gorm:"not null;index" json:"node_id"`
	Node               Node       `json:"node,omitempty"`
	PolicyID           *uint      `gorm:"index" json:"policy_id,omitempty"`
	Policy             *Policy    `json:"policy,omitempty"`
	DependsOnTaskID    *uint      `gorm:"index" json:"depends_on_task_id,omitempty"`
	Command            string     `gorm:"type:text" json:"command"`
	RsyncSource        string     `gorm:"size:512" json:"rsync_source"`
	RsyncTarget        string     `gorm:"size:512" json:"rsync_target"`
	ExecutorType       string     `gorm:"size:32;not null;default:local" json:"executor_type"`
	ExecutorConfig     string     `gorm:"type:text" json:"-"`
	CronSpec           string     `gorm:"size:128" json:"cron_spec"`
	Status             string     `gorm:"size:32;not null;index" json:"status"`
	BatchID            string     `gorm:"size:64;index" json:"batch_id,omitempty"`
	Source             string     `gorm:"size:32;not null;default:manual" json:"source"`
	VerifyStatus       string     `gorm:"size:16;not null;default:none" json:"verify_status"`
	RetryCount         int        `gorm:"not null;default:0" json:"retry_count"`
	Enabled            bool       `gorm:"not null;default:true" json:"enabled"`
	SkipNext           bool       `gorm:"not null;default:false" json:"skip_next"`
	LastError          string     `gorm:"type:text" json:"last_error"`
	LastRunAt          *time.Time `json:"last_run_at"`
	NextRunAt          *time.Time `json:"next_run_at"`
	ArchivedAt         *time.Time `json:"archived_at,omitempty"`
	Progress           *int       `gorm:"-" json:"progress,omitempty"`
	EscalationPolicyID *uint      `gorm:"index" json:"escalation_policy_id"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`

	// Rsync capture fields are populated only on an in-memory restore task.
	// They are deliberately not part of the Task table or API surface; durable
	// capture evidence belongs to the producing TaskRun.
	RsyncCaptureLayout       string `gorm:"-" json:"-"`
	RsyncCaptureRoot         string `gorm:"-" json:"-"`
	RsyncCaptureManifest     string `gorm:"-" json:"-"`
	RsyncCaptureGenerationID uint   `gorm:"-" json:"-"`
	// RsyncBinary is the exact local compatibility binary selected by the
	// executor factory. It is transient so capture/verification cannot silently
	// diverge from the configured transfer executable.
	RsyncBinary string `gorm:"-" json:"-"`
}

func (t *Task) BeforeSave(_ *gorm.DB) error {
	if strings.TrimSpace(t.ExecutorConfig) == "" {
		return nil
	}
	encrypted, err := secure.EncryptIfNeeded(t.ExecutorConfig)
	if err != nil {
		return err
	}
	t.ExecutorConfig = encrypted
	return nil
}

func (t *Task) AfterFind(_ *gorm.DB) error {
	if strings.TrimSpace(t.ExecutorConfig) == "" {
		return nil
	}
	decrypted, err := secure.DecryptIfNeeded(t.ExecutorConfig)
	if err != nil {
		return err
	}
	t.ExecutorConfig = decrypted
	return nil
}

type TaskRun struct {
	ExecutionOwnerID    string     `gorm:"size:64;not null;default:''" json:"-"`
	ExecutionLeaseUntil *time.Time `gorm:"index" json:"-"`
	ID                  uint       `gorm:"primaryKey" json:"id"`
	TaskID              uint       `gorm:"not null;index;uniqueIndex:idx_task_runs_active_drill,where:trigger_type = 'drill' AND (status = 'pending' OR status = 'running' OR status = 'retrying')" json:"task_id"`
	Task                Task       `gorm:"foreignKey:TaskID" json:"-"`
	NodeIDSnapshot      uint       `gorm:"not null;index:idx_task_runs_node_snapshot_status,priority:1" json:"-"`
	CronScheduledAt     *time.Time `gorm:"column:cron_scheduled_at" json:"-"`
	// CronOccurrenceID links a newly reserved TaskRun to its durable scheduler
	// intent. It is a reservation-only value and is never persisted as a column.
	CronOccurrenceID uint `gorm:"-" json:"-"`
	// ExecutorTypeSnapshot is the immutable executor classification used by
	// health/reporting consumers. Empty is retained for pre-000086 history and
	// is never interpreted as a provider.
	ExecutorTypeSnapshot string `gorm:"column:executor_type_snapshot;size:32;not null;default:''" json:"-"`
	// Resource identity is populated at TaskRun creation for legacy mutable
	// writers. It contains no credentials and remains stable across SSH key or
	// password rotation; unresolved generation checks use ResourceKey across
	// TaskIDs rather than the editable Task target.
	ResourceKey             string     `gorm:"column:resource_key;size:64;not null;default:''" json:"-"`
	ResourceProvider        string     `gorm:"column:resource_provider;size:32;not null;default:''" json:"-"`
	ResourceNodeID          uint       `gorm:"column:resource_node_id;not null;default:0" json:"-"`
	ResourceNamespace       string     `gorm:"column:resource_namespace;size:255;not null;default:''" json:"-"`
	ResourceLocator         string     `gorm:"column:resource_locator;size:512;not null;default:''" json:"-"`
	ResourceEvidence        string     `gorm:"column:resource_evidence;type:text;not null;default:''" json:"-"`
	BackupConfigFingerprint string     `gorm:"column:backup_config_fingerprint;size:64" json:"-"`
	BackupCaptureLayout     string     `gorm:"column:backup_capture_layout;size:32" json:"-"`
	BackupCaptureRoot       string     `gorm:"column:backup_capture_root;size:512" json:"-"`
	BackupCaptureManifest   string     `gorm:"column:backup_capture_manifest;type:text" json:"-"`
	BackupGenerationState   string     `gorm:"column:backup_generation_state;size:16" json:"-"`
	BackupSourceRunID       uint       `gorm:"column:backup_source_run_id" json:"-"`
	TriggerType             string     `gorm:"size:32;not null;default:manual" json:"trigger_type"`
	Status                  string     `gorm:"size:32;not null;default:pending;index;index:idx_task_runs_status_finished_at,priority:1;index:idx_task_runs_node_snapshot_status,priority:2" json:"status"`
	ChainRunID              string     `gorm:"size:64;index" json:"chain_run_id,omitempty"`
	UpstreamTaskRunID       *uint      `gorm:"index" json:"upstream_task_run_id,omitempty"`
	SkipReason              string     `gorm:"type:text" json:"skip_reason,omitempty"`
	StartedAt               *time.Time `gorm:"index:idx_task_runs_started_at" json:"started_at"`
	FinishedAt              *time.Time `gorm:"index:idx_task_runs_status_finished_at,priority:2" json:"finished_at"`
	DurationMs              int64      `gorm:"not null;default:0" json:"duration_ms"`
	VerifyStatus            string     `gorm:"size:16;not null;default:none" json:"verify_status"`
	ThroughputMbps          float64    `gorm:"not null;default:0" json:"throughput_mbps"`
	Progress                int        `gorm:"not null;default:0" json:"progress"`
	LastError               string     `gorm:"type:text" json:"last_error"`
	CreatedAt               time.Time  `json:"created_at"`
	UpdatedAt               time.Time  `json:"updated_at"`
}

const (
	RsyncCaptureManifestMaxEntries = 100000
	RsyncCaptureManifestMaxBytes   = 8 << 20
	rsyncCaptureManifestVersionV1  = 1
	rsyncCaptureManifestVersionV2  = 2
	rsyncCapturePathMaxBytes       = 4096
	rsyncCaptureRootSidecarMax     = 512
	rsyncCaptureRootSidecarPrefix  = "v2:"
)

// RsyncCaptureManifest is the bounded, source-side evidence captured before a
// compatibility Rsync write. Paths are relative to the original source root.
// A directory entry proves an empty directory was selected; a symlink entry
// records its link target instead of pretending it is a regular file.
type RsyncCaptureManifest struct {
	Version int                         `json:"version"`
	Layout  string                      `json:"layout"`
	Root    string                      `json:"root,omitempty"`
	Entries []RsyncCaptureManifestEntry `json:"entries"`
}

type RsyncCaptureManifestEntry struct {
	Path       string `json:"path"`
	Kind       string `json:"kind"`
	Size       int64  `json:"size,omitempty"`
	SHA256     string `json:"sha256,omitempty"`
	LinkTarget string `json:"link_target,omitempty"`
}

// EncodeRsyncCaptureManifest writes the current byte-safe manifest format.
// Version 1 remains readable for already-persisted history, while every new
// manifest encodes path-bearing bytes as canonical base64 in version 2.
func EncodeRsyncCaptureManifest(manifest RsyncCaptureManifest) (string, error) {
	if err := validateRsyncCaptureManifestShape(manifest, true); err != nil {
		return "", err
	}
	type wireEntry struct {
		PathB64       string `json:"path_b64"`
		Kind          string `json:"kind"`
		Size          int64  `json:"size,omitempty"`
		SHA256        string `json:"sha256,omitempty"`
		LinkTargetB64 string `json:"link_target_b64,omitempty"`
	}
	type wireManifest struct {
		Version int         `json:"version"`
		Layout  string      `json:"layout"`
		RootB64 string      `json:"root_b64"`
		Entries []wireEntry `json:"entries"`
	}
	wire := wireManifest{
		Version: rsyncCaptureManifestVersionV2,
		Layout:  manifest.Layout,
		RootB64: base64.RawStdEncoding.EncodeToString([]byte(manifest.Root)),
		Entries: make([]wireEntry, len(manifest.Entries)),
	}
	for index, entry := range manifest.Entries {
		wire.Entries[index] = wireEntry{
			PathB64:       base64.RawStdEncoding.EncodeToString([]byte(entry.Path)),
			Kind:          entry.Kind,
			Size:          entry.Size,
			SHA256:        entry.SHA256,
			LinkTargetB64: base64.RawStdEncoding.EncodeToString([]byte(entry.LinkTarget)),
		}
		if entry.LinkTarget == "" {
			wire.Entries[index].LinkTargetB64 = ""
		}
	}
	encoded, err := json.Marshal(wire)
	if err != nil {
		return "", err
	}
	if len(encoded) > RsyncCaptureManifestMaxBytes {
		return "", fmt.Errorf("rsync capture manifest is too large")
	}
	return string(encoded), nil
}

// DecodeRsyncCaptureManifest accepts the legacy JSON string fields and the
// current byte-safe base64 fields. It rejects unknown/duplicate fields and
// non-canonical base64 so persisted evidence has one unambiguous identity.
func DecodeRsyncCaptureManifest(raw string) (RsyncCaptureManifest, error) {
	if strings.TrimSpace(raw) == "" {
		return RsyncCaptureManifest{}, fmt.Errorf("rsync capture manifest is empty")
	}
	if len(raw) > RsyncCaptureManifestMaxBytes {
		return RsyncCaptureManifest{}, fmt.Errorf("rsync capture manifest is too large")
	}
	top, err := decodeRsyncJSONObject([]byte(raw), map[string]struct{}{
		"version": {}, "layout": {}, "root": {}, "root_b64": {}, "entries": {},
	})
	if err != nil {
		return RsyncCaptureManifest{}, err
	}
	version, err := decodeRsyncJSONInt(top["version"], "version")
	if err != nil {
		return RsyncCaptureManifest{}, err
	}
	var manifest RsyncCaptureManifest
	switch version {
	case rsyncCaptureManifestVersionV1:
		if _, present := top["root_b64"]; present {
			return RsyncCaptureManifest{}, fmt.Errorf("rsync capture v1 has noncanonical root field")
		}
		manifest, err = decodeRsyncCaptureManifestV1(top)
	case rsyncCaptureManifestVersionV2:
		if _, present := top["root"]; present {
			return RsyncCaptureManifest{}, fmt.Errorf("rsync capture v2 has noncanonical root field")
		}
		manifest, err = decodeRsyncCaptureManifestV2(top)
	default:
		return RsyncCaptureManifest{}, fmt.Errorf("unsupported rsync capture manifest version")
	}
	if err != nil {
		return RsyncCaptureManifest{}, err
	}
	if err := validateRsyncCaptureManifestShape(manifest, version == rsyncCaptureManifestVersionV2); err != nil {
		return RsyncCaptureManifest{}, err
	}
	return manifest, nil
}

func decodeRsyncCaptureManifestV1(top map[string]json.RawMessage) (RsyncCaptureManifest, error) {
	layout, err := decodeRsyncJSONRequiredString(top["layout"], "layout")
	if err != nil {
		return RsyncCaptureManifest{}, err
	}
	root, err := decodeRsyncJSONOptionalString(top["root"], "root")
	if err != nil {
		return RsyncCaptureManifest{}, err
	}
	entries, err := decodeRsyncCaptureEntries(top["entries"], false)
	if err != nil {
		return RsyncCaptureManifest{}, err
	}
	return RsyncCaptureManifest{Version: rsyncCaptureManifestVersionV1, Layout: layout, Root: root, Entries: entries}, nil
}

func decodeRsyncCaptureManifestV2(top map[string]json.RawMessage) (RsyncCaptureManifest, error) {
	rootRaw, ok := top["root_b64"]
	if !ok {
		return RsyncCaptureManifest{}, fmt.Errorf("rsync capture v2 root field is missing")
	}
	root, err := decodeRsyncJSONBase64(rootRaw, "root_b64")
	if err != nil {
		return RsyncCaptureManifest{}, err
	}
	layout, err := decodeRsyncJSONRequiredString(top["layout"], "layout")
	if err != nil {
		return RsyncCaptureManifest{}, err
	}
	entries, err := decodeRsyncCaptureEntries(top["entries"], true)
	if err != nil {
		return RsyncCaptureManifest{}, err
	}
	return RsyncCaptureManifest{Version: rsyncCaptureManifestVersionV2, Layout: layout, Root: root, Entries: entries}, nil
}

func decodeRsyncCaptureEntries(raw json.RawMessage, byteSafe bool) ([]RsyncCaptureManifestEntry, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, fmt.Errorf("rsync capture manifest entries are missing")
	}
	var rawEntries []json.RawMessage
	if err := json.Unmarshal(raw, &rawEntries); err != nil || rawEntries == nil {
		return nil, fmt.Errorf("rsync capture manifest entries are invalid")
	}
	if len(rawEntries) > RsyncCaptureManifestMaxEntries {
		return nil, fmt.Errorf("rsync capture manifest has too many entries")
	}
	entries := make([]RsyncCaptureManifestEntry, len(rawEntries))
	for index, rawEntry := range rawEntries {
		allowed := map[string]struct{}{"kind": {}, "size": {}, "sha256": {}}
		if byteSafe {
			allowed["path_b64"] = struct{}{}
			allowed["link_target_b64"] = struct{}{}
		} else {
			allowed["path"] = struct{}{}
			allowed["link_target"] = struct{}{}
		}
		object, err := decodeRsyncJSONObject(rawEntry, allowed)
		if err != nil {
			return nil, fmt.Errorf("rsync capture manifest entry %d is invalid: %w", index, err)
		}
		if byteSafe {
			entries[index].Path, err = decodeRsyncJSONBase64Required(object["path_b64"], "path_b64")
			if err != nil {
				return nil, err
			}
			entries[index].LinkTarget, err = decodeRsyncJSONBase64Optional(object["link_target_b64"], "link_target_b64")
		} else {
			entries[index].Path, err = decodeRsyncJSONRequiredString(object["path"], "path")
			if err != nil {
				return nil, err
			}
			entries[index].LinkTarget, err = decodeRsyncJSONOptionalString(object["link_target"], "link_target")
		}
		if err != nil {
			return nil, err
		}
		entries[index].Kind, err = decodeRsyncJSONRequiredString(object["kind"], "kind")
		if err != nil {
			return nil, err
		}
		entries[index].Size, err = decodeRsyncJSONOptionalInt(object["size"], "size")
		if err != nil {
			return nil, err
		}
		entries[index].SHA256, err = decodeRsyncJSONOptionalString(object["sha256"], "sha256")
		if err != nil {
			return nil, err
		}
	}
	return entries, nil
}

func validateRsyncCaptureManifestShape(manifest RsyncCaptureManifest, requireSidecar bool) error {
	if manifest.Layout != TaskRunCaptureLayoutDirectoryRoot &&
		manifest.Layout != TaskRunCaptureLayoutDirectoryContents &&
		manifest.Layout != TaskRunCaptureLayoutSingleFile {
		return fmt.Errorf("invalid rsync capture manifest layout")
	}
	if len(manifest.Root) > rsyncCapturePathMaxBytes || strings.ContainsRune(manifest.Root, '\x00') {
		return fmt.Errorf("rsync capture manifest root is invalid")
	}
	if requireSidecar {
		if _, err := EncodeRsyncCaptureRootSidecar(manifest.Root); err != nil {
			return err
		}
	}
	if len(manifest.Entries) == 0 || len(manifest.Entries) > RsyncCaptureManifestMaxEntries {
		return fmt.Errorf("rsync capture manifest entries are invalid")
	}
	seen := make(map[string]struct{}, len(manifest.Entries))
	for _, entry := range manifest.Entries {
		if entry.Kind != "directory" && entry.Kind != "file" && entry.Kind != "symlink" {
			return fmt.Errorf("rsync capture manifest entry type is invalid")
		}
		if len(entry.Path) > rsyncCapturePathMaxBytes || strings.ContainsRune(entry.Path, '\x00') {
			return fmt.Errorf("rsync capture manifest path is invalid")
		}
		if _, exists := seen[entry.Path]; exists {
			return fmt.Errorf("rsync capture manifest has duplicate paths")
		}
		seen[entry.Path] = struct{}{}
		if entry.Size < 0 {
			return fmt.Errorf("rsync capture manifest size is invalid")
		}
		if len(entry.LinkTarget) > rsyncCapturePathMaxBytes || strings.ContainsRune(entry.LinkTarget, '\x00') {
			return fmt.Errorf("rsync capture manifest link target is invalid")
		}
	}
	return nil
}

func decodeRsyncJSONObject(raw []byte, allowed map[string]struct{}) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, fmt.Errorf("rsync capture manifest object is invalid")
	}
	result := make(map[string]json.RawMessage)
	for decoder.More() {
		keyToken, keyErr := decoder.Token()
		key, ok := keyToken.(string)
		if keyErr != nil || !ok {
			return nil, fmt.Errorf("rsync capture manifest field name is invalid")
		}
		if _, exists := result[key]; exists {
			return nil, fmt.Errorf("rsync capture manifest has duplicate field %q", key)
		}
		if _, known := allowed[key]; !known {
			return nil, fmt.Errorf("rsync capture manifest has unknown field %q", key)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("rsync capture manifest field %q is invalid", key)
		}
		result[key] = value
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return nil, fmt.Errorf("rsync capture manifest object is invalid")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("rsync capture manifest has trailing data")
	}
	return result, nil
}

func decodeRsyncJSONRequiredString(raw json.RawMessage, field string) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", fmt.Errorf("rsync capture manifest field %q is missing", field)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("rsync capture manifest field %q is invalid", field)
	}
	return value, nil
}

func decodeRsyncJSONOptionalString(raw json.RawMessage, field string) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	return decodeRsyncJSONRequiredString(raw, field)
}

func decodeRsyncJSONOptionalInt(raw json.RawMessage, field string) (int64, error) {
	if len(raw) == 0 {
		return 0, nil
	}
	if string(raw) == "null" {
		return 0, fmt.Errorf("rsync capture manifest field %q is invalid", field)
	}
	var value int64
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, fmt.Errorf("rsync capture manifest field %q is invalid", field)
	}
	return value, nil
}

func decodeRsyncJSONInt(raw json.RawMessage, field string) (int, error) {
	value, err := decodeRsyncJSONOptionalInt(raw, field)
	if err != nil || len(raw) == 0 {
		if err == nil {
			err = fmt.Errorf("rsync capture manifest field %q is missing", field)
		}
		return 0, err
	}
	return int(value), nil
}

func decodeRsyncJSONBase64(raw json.RawMessage, field string) (string, error) {
	value, err := decodeRsyncJSONRequiredString(raw, field)
	if err != nil {
		return "", err
	}
	decoded, err := base64.RawStdEncoding.DecodeString(value)
	if err != nil || base64.RawStdEncoding.EncodeToString(decoded) != value {
		return "", fmt.Errorf("rsync capture manifest field %q is not canonical base64", field)
	}
	return string(decoded), nil
}

func decodeRsyncJSONBase64Required(raw json.RawMessage, field string) (string, error) {
	return decodeRsyncJSONBase64(raw, field)
}

func decodeRsyncJSONBase64Optional(raw json.RawMessage, field string) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	return decodeRsyncJSONBase64(raw, field)
}

// EncodeRsyncCaptureRootSidecar creates the PostgreSQL-text-safe root value
// paired with a version-2 manifest.
func EncodeRsyncCaptureRootSidecar(root string) (string, error) {
	if len(root) > rsyncCapturePathMaxBytes || strings.ContainsRune(root, '\x00') {
		return "", fmt.Errorf("rsync capture root is invalid")
	}
	sidecar := rsyncCaptureRootSidecarPrefix + base64.RawStdEncoding.EncodeToString([]byte(root))
	if len(sidecar) > rsyncCaptureRootSidecarMax {
		return "", fmt.Errorf("rsync capture root exceeds sidecar limit")
	}
	return sidecar, nil
}

// DecodeRsyncCaptureRootSidecar decodes the version-2 root sidecar. Version 1
// rows retain their historical raw UTF-8 value and are returned unchanged.
func DecodeRsyncCaptureRootSidecar(raw string, manifestVersion int) (string, error) {
	if manifestVersion == rsyncCaptureManifestVersionV1 {
		return raw, nil
	}
	if manifestVersion != rsyncCaptureManifestVersionV2 || !strings.HasPrefix(raw, rsyncCaptureRootSidecarPrefix) {
		return "", fmt.Errorf("rsync capture root sidecar is invalid")
	}
	encoded := strings.TrimPrefix(raw, rsyncCaptureRootSidecarPrefix)
	decoded, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil || base64.RawStdEncoding.EncodeToString(decoded) != encoded {
		return "", fmt.Errorf("rsync capture root sidecar is not canonical base64")
	}
	if len(raw) > rsyncCaptureRootSidecarMax || len(decoded) > rsyncCapturePathMaxBytes ||
		strings.ContainsRune(string(decoded), '\x00') {
		return "", fmt.Errorf("rsync capture root sidecar exceeds limit")
	}
	return string(decoded), nil
}

// TaskRunBackupConfigFingerprint returns a stable, non-secret identity of the
// task values that the executor is about to use. Executor configuration and
// policy hooks are represented only by their SHA-256 digests so credentials
// never enter TaskRun history. The node connection identity is included because
// a node ID alone does not prove that the same endpoint was used.
//
// A policy-bound task must carry the matching Policy snapshot. Returning an
// empty digest for a missing or mismatched snapshot makes reservation and
// restore validation fail closed; standalone tasks without PolicyID remain
// supported.
func TaskRunBackupConfigFingerprint(task Task) string {
	type binding struct {
		NodeID               uint   `json:"node_id"`
		NodeName             string `json:"node_name"`
		NodeHost             string `json:"node_host"`
		NodePort             int    `json:"node_port"`
		NodeUsername         string `json:"node_username"`
		NodeAuthType         string `json:"node_auth_type"`
		NodeSSHKeyID         uint   `json:"node_ssh_key_id"`
		NodeUseSudo          bool   `json:"node_use_sudo"`
		NodeBackupDir        string `json:"node_backup_dir"`
		ExecutorType         string `json:"executor_type"`
		RsyncSource          string `json:"rsync_source"`
		RsyncTarget          string `json:"rsync_target"`
		ExecutorConfigDigest string `json:"executor_config_digest"`

		PolicyBound               bool   `json:"policy_bound"`
		PolicyID                  uint   `json:"policy_id"`
		PolicyExcludeRules        string `json:"policy_exclude_rules"`
		PolicyBwLimit             int    `json:"policy_bw_limit"`
		PolicyBandwidthSchedule   string `json:"policy_bandwidth_schedule"`
		PolicyPreHookDigest       string `json:"policy_pre_hook_digest"`
		PolicyPostHookDigest      string `json:"policy_post_hook_digest"`
		PolicyHookTimeoutSeconds  int    `json:"policy_hook_timeout_seconds"`
		PolicyMaxExecutionSeconds int    `json:"policy_max_execution_seconds"`
		PolicyAppProfile          string `json:"policy_app_profile"`
		PolicyAppCredentialID     uint   `json:"policy_app_credential_id"`
	}

	executorConfigDigest, ok := taskRunFingerprintDigest(task.ExecutorConfig)
	if !ok {
		return ""
	}

	policyBound := task.PolicyID != nil
	policyID := uint(0)
	policyExcludeRules := ""
	policyBwLimit := 0
	policyBandwidthSchedule := ""
	policyPreHookDigest := ""
	policyPostHookDigest := ""
	policyHookTimeoutSeconds := 0
	policyMaxExecutionSeconds := 0
	policyAppProfile := ""
	policyAppCredentialID := uint(0)
	if policyBound {
		policyID = *task.PolicyID
		if policyID == 0 || task.Policy == nil {
			return ""
		}
		if task.Policy.ID != 0 && task.Policy.ID != policyID {
			return ""
		}
		policyPreHookDigest, ok = taskRunFingerprintDigest(task.Policy.PreHook)
		if !ok {
			return ""
		}
		policyPostHookDigest, ok = taskRunFingerprintDigest(task.Policy.PostHook)
		if !ok {
			return ""
		}
		policyExcludeRules = canonicalTaskRunExcludeRules(task.Policy.ExcludeRules)
		policyBwLimit = task.Policy.BwLimit
		policyBandwidthSchedule = strings.TrimSpace(task.Policy.BandwidthSchedule)
		policyHookTimeoutSeconds = task.Policy.HookTimeoutSeconds
		policyMaxExecutionSeconds = task.Policy.MaxExecutionSeconds
		policyAppProfile = task.Policy.AppProfile
		if task.Policy.AppCredentialID != nil {
			policyAppCredentialID = *task.Policy.AppCredentialID
		}
	}

	nodeSSHKeyID := uint(0)
	if task.Node.SSHKeyID != nil {
		nodeSSHKeyID = *task.Node.SSHKeyID
	}
	payload := binding{
		NodeID:                    task.NodeID,
		NodeName:                  strings.TrimSpace(task.Node.Name),
		NodeHost:                  strings.TrimSpace(task.Node.Host),
		NodePort:                  task.Node.Port,
		NodeUsername:              strings.TrimSpace(task.Node.Username),
		NodeAuthType:              strings.ToLower(strings.TrimSpace(task.Node.AuthType)),
		NodeSSHKeyID:              nodeSSHKeyID,
		NodeUseSudo:               task.Node.UseSudo,
		NodeBackupDir:             strings.TrimSpace(task.Node.BackupDir),
		ExecutorType:              strings.ToLower(strings.TrimSpace(task.ExecutorType)),
		RsyncSource:               strings.TrimSpace(task.RsyncSource),
		RsyncTarget:               strings.TrimSpace(task.RsyncTarget),
		ExecutorConfigDigest:      executorConfigDigest,
		PolicyBound:               policyBound,
		PolicyID:                  policyID,
		PolicyExcludeRules:        policyExcludeRules,
		PolicyBwLimit:             policyBwLimit,
		PolicyBandwidthSchedule:   policyBandwidthSchedule,
		PolicyPreHookDigest:       policyPreHookDigest,
		PolicyPostHookDigest:      policyPostHookDigest,
		PolicyHookTimeoutSeconds:  policyHookTimeoutSeconds,
		PolicyMaxExecutionSeconds: policyMaxExecutionSeconds,
		PolicyAppProfile:          policyAppProfile,
		PolicyAppCredentialID:     policyAppCredentialID,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		// The binding contains only scalar values and cannot currently fail to
		// marshal. Keep the function total if fields are extended later.
		return ""
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

// taskRunFingerprintDigest decrypts encrypted-at-rest values before hashing so
// random ciphertext bytes never become part of the durable configuration
// identity. It also lets direct callers pass either plaintext or a persisted
// encrypted value without changing the result.
func taskRunFingerprintDigest(raw string) (string, bool) {
	plaintext, err := secure.DecryptIfNeeded(raw)
	if err != nil {
		return "", false
	}
	digest := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(digest[:]), true
}

// canonicalTaskRunExcludeRules matches the executor's input normalization:
// CRLF is treated as LF, surrounding whitespace is ignored, and blank lines
// are skipped before rules are passed to rsync. Internal rule bytes remain
// untouched because they affect validation and the rsync arguments.
func canonicalTaskRunExcludeRules(raw string) string {
	normalized := strings.TrimSpace(strings.ReplaceAll(raw, "\r\n", "\n"))
	if normalized == "" {
		return ""
	}
	lines := strings.Split(normalized, "\n")
	rules := lines[:0]
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		rules = append(rules, line)
	}
	return strings.Join(rules, "\n")
}

// BeforeCreate freezes the Task's current node and executor classification for
// every GORM TaskRun writer. Paired migration guards remain the authoritative
// defense for raw SQL and reject explicit mismatches; this hook also records a
// legacy Rclone resource identity before any provider arm can occur.
func (r *TaskRun) BeforeCreate(tx *gorm.DB) error {
	if r == nil || tx == nil {
		return fmt.Errorf("task run authority is unavailable")
	}
	if r.TaskID == 0 {
		return fmt.Errorf("task run requires an authoritative task")
	}
	var taskSnapshot Task
	result := tx.Model(&Task{}).
		Select("id, node_id, executor_type, rsync_source, rsync_target, executor_config").
		Where("id = ?", r.TaskID).Limit(1).Find(&taskSnapshot)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("task run requires an authoritative task")
	}
	if !IsTaskRunNodeSnapshotAuthoritative(taskSnapshot.NodeID) {
		return fmt.Errorf("task run requires an authoritative task node snapshot")
	}
	if r.NodeIDSnapshot == 0 {
		r.NodeIDSnapshot = taskSnapshot.NodeID
	}
	if r.NodeIDSnapshot != taskSnapshot.NodeID {
		return fmt.Errorf("task run node snapshot %d does not match task node %d", r.NodeIDSnapshot, taskSnapshot.NodeID)
	}
	executorType := strings.ToLower(strings.TrimSpace(taskSnapshot.ExecutorType))
	if strings.TrimSpace(r.ExecutorTypeSnapshot) == "" {
		r.ExecutorTypeSnapshot = executorType
	} else if strings.ToLower(strings.TrimSpace(r.ExecutorTypeSnapshot)) != executorType {
		return fmt.Errorf("task run executor snapshot %q does not match task executor %q", r.ExecutorTypeSnapshot, executorType)
	}
	SetTaskRunResourceIdentity(r, Task{
		ID:             taskSnapshot.ID,
		NodeID:         taskSnapshot.NodeID,
		ExecutorType:   taskSnapshot.ExecutorType,
		RsyncSource:    taskSnapshot.RsyncSource,
		RsyncTarget:    taskSnapshot.RsyncTarget,
		ExecutorConfig: taskSnapshot.ExecutorConfig,
	})
	return nil
}

type TaskLog struct {
	ID        uint      `gorm:"primaryKey;index:idx_tasklog_task_cursor,priority:2,sort:desc" json:"id"`
	TaskID    uint      `gorm:"not null;index;index:idx_tasklog_task_cursor,priority:1" json:"task_id"`
	TaskRunID *uint     `gorm:"index" json:"task_run_id,omitempty"`
	Level     string    `gorm:"size:16;not null" json:"level"`
	Message   string    `gorm:"type:text;not null" json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

type TaskTrafficSample struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	TaskID         uint      `gorm:"not null;index:idx_task_traffic_task_run_sample,priority:1" json:"task_id"`
	NodeID         uint      `gorm:"not null;index:idx_task_traffic_node_sample,priority:1" json:"node_id"`
	RunStartedAt   time.Time `gorm:"not null;index:idx_task_traffic_task_run_sample,priority:2" json:"run_started_at"`
	SampledAt      time.Time `gorm:"not null;index:idx_task_traffic_task_run_sample,priority:3;index:idx_task_traffic_sampled_at;index:idx_task_traffic_node_sample,priority:2" json:"sampled_at"`
	ThroughputMbps float64   `gorm:"not null;default:0" json:"throughput_mbps"`
	CreatedAt      time.Time `json:"created_at"`
}
