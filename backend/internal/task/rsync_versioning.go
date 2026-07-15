package task

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"xirang/backend/internal/backupasset"
)

// RsyncPublicationConfigV1Version is the only persisted configuration schema
// accepted for Rsync publication mode selection.
const RsyncPublicationConfigV1Version = backupasset.RsyncPublicationConfigSchemaVersion

// RsyncPublicationConfigV1 intentionally contains only publication policy.
// Provider roots, preflight evidence, command arguments, and credentials stay
// in their dedicated internal stores.
type RsyncPublicationConfigV1 struct {
	Version         int                             `json:"version"`
	PublicationMode backupasset.TaskPublicationMode `json:"publication_mode"`
}

type rsyncPublicationConfigV1Wire struct {
	Version         *int                             `json:"version"`
	PublicationMode *backupasset.TaskPublicationMode `json:"publication_mode"`
}

func defaultRsyncPublicationConfigV1() RsyncPublicationConfigV1 {
	return RsyncPublicationConfigV1{
		Version:         RsyncPublicationConfigV1Version,
		PublicationMode: backupasset.PublicationLegacyMutable,
	}
}

// ValidateDisconnectedImportedRsyncTask validates the intentionally incomplete
// task shape created when importing a foreign managed-Rsync configuration. It
// is not a normal create/update path: source and target must already be empty,
// so a later resume fails closed until a local migration supplies new inputs.
func ValidateDisconnectedImportedRsyncTask(req CreateTaskInput) error {
	if err := validateTaskIdentityAndSchedule(req); err != nil {
		return err
	}
	if req.ExecutorType != "rsync" {
		return newValidationError("断连导入仅支持 rsync 任务")
	}
	if strings.TrimSpace(req.RsyncSource) != "" || strings.TrimSpace(req.RsyncTarget) != "" {
		return newValidationError("断连 rsync 导入不得保留源路径或目标路径")
	}
	config, err := ParseRsyncPublicationConfigV1(req.ExecutorConfig)
	if err != nil {
		return err
	}
	if config.PublicationMode != backupasset.PublicationLegacyMutable {
		return newValidationError("断连 rsync 导入必须使用 legacy_mutable 发布模式")
	}
	return nil
}

// ParseRsyncPublicationConfigV1 decodes the persisted Rsync publication
// policy. Empty configuration remains the legacy-compatible default; every
// non-empty configuration is a closed schema.
func ParseRsyncPublicationConfigV1(raw string) (RsyncPublicationConfigV1, error) {
	if strings.TrimSpace(raw) == "" {
		return defaultRsyncPublicationConfigV1(), nil
	}
	if !strings.HasPrefix(strings.TrimSpace(raw), "{") {
		return RsyncPublicationConfigV1{}, newValidationError("rsync 发布配置必须是对象")
	}
	if err := validateRsyncPublicationConfigObject(raw); err != nil {
		return RsyncPublicationConfigV1{}, newValidationError("rsync 发布配置无效")
	}

	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var wire rsyncPublicationConfigV1Wire
	if err := decoder.Decode(&wire); err != nil {
		return RsyncPublicationConfigV1{}, newValidationError("rsync 发布配置无效")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return RsyncPublicationConfigV1{}, newValidationError("rsync 发布配置无效")
	}

	config := defaultRsyncPublicationConfigV1()
	if wire.Version != nil {
		config.Version = *wire.Version
	}
	if config.Version != RsyncPublicationConfigV1Version {
		return RsyncPublicationConfigV1{}, newValidationError(fmt.Sprintf("不支持的 rsync 发布配置版本：%d", config.Version))
	}
	if wire.PublicationMode != nil {
		config.PublicationMode = *wire.PublicationMode
	}
	switch config.PublicationMode {
	case backupasset.PublicationLegacyMutable,
		backupasset.PublicationVersionedHardlink,
		backupasset.PublicationVersionedFullCopy:
		return config, nil
	default:
		return RsyncPublicationConfigV1{}, newValidationError("不支持的 rsync 发布模式")
	}
}

// validateRsyncPublicationConfigObject provides the closed-object checks the
// standard JSON decoder intentionally does not provide: duplicate member
// names and explicit nulls must not become an accidental legacy default.
func validateRsyncPublicationConfigObject(raw string) error {
	decoder := json.NewDecoder(strings.NewReader(raw))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		if err != nil {
			return err
		}
		return fmt.Errorf("rsync publication config must be an object")
	}
	seen := make(map[string]struct{})
	for decoder.More() {
		nameToken, err := decoder.Token()
		if err != nil {
			return err
		}
		name, ok := nameToken.(string)
		if !ok {
			return fmt.Errorf("rsync publication config member is not a string")
		}
		if _, exists := seen[name]; exists {
			return fmt.Errorf("duplicate Rsync publication config member")
		}
		seen[name] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("null Rsync publication config member")
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		if err != nil {
			return err
		}
		return fmt.Errorf("invalid Rsync publication config terminator")
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return fmt.Errorf("trailing Rsync publication config data")
		}
		return err
	}
	return nil
}
