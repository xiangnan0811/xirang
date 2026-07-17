package task

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/util"
)

const (
	RcloneTaskConfigV1Version    = backupasset.RcloneTaskConfigSchemaVersion
	MaxRcloneBandwidthLimitBytes = 256
	MaxRcloneTransfers           = 256
)

type RcloneTaskConfigV1 struct {
	Version         int                             `json:"version"`
	PublicationMode backupasset.TaskPublicationMode `json:"publication_mode"`
	BandwidthLimit  string                          `json:"bandwidth_limit,omitempty"`
	Transfers       int                             `json:"transfers,omitempty"`
}

type rcloneTaskConfigV1Wire struct {
	Version         *int                             `json:"version"`
	PublicationMode *backupasset.TaskPublicationMode `json:"publication_mode"`
	BandwidthLimit  *string                          `json:"bandwidth_limit"`
	Transfers       *int                             `json:"transfers"`
}

func defaultRcloneTaskConfigV1() RcloneTaskConfigV1 {
	return RcloneTaskConfigV1{Version: RcloneTaskConfigV1Version, PublicationMode: backupasset.PublicationLegacyMutable}
}

// ValidateDisconnectedImportedRcloneTask validates the deliberately incomplete
// shape used for a foreign managed-Rclone import. The trusted local source is
// retained, but the foreign target and all managed binding/preflight material
// must already have been discarded so no mutable command can run.
func ValidateDisconnectedImportedRcloneTask(req CreateTaskInput) error {
	if err := validateTaskIdentityAndSchedule(req); err != nil {
		return err
	}
	if req.ExecutorType != "rclone" {
		return newValidationError("断连导入仅支持 rclone 任务")
	}
	source := strings.TrimSpace(req.RsyncSource)
	if source == "" {
		return newValidationError("断连 rclone 导入必须保留本地源路径")
	}
	if util.IsRemotePathSpec(source) || !strings.HasPrefix(source, "/") || strings.Contains(source, "..") {
		return newValidationError("断连 rclone 导入的源路径无效")
	}
	if err := validatePathChars(source, "rsync_source"); err != nil {
		return newValidationError(err.Error())
	}
	if err := validatePathByPrefix(source, parseCSVEnvList("RSYNC_ALLOWED_SOURCE_PREFIXES"), "rsync_source"); err != nil {
		return newValidationError(err.Error())
	}
	if strings.TrimSpace(req.RsyncTarget) != "" {
		return newValidationError("断连 rclone 导入不得保留目标 Remote")
	}
	config, err := ParseRcloneTaskConfigV1(req.ExecutorConfig)
	if err != nil {
		return err
	}
	if config.PublicationMode != backupasset.PublicationLegacyMutable {
		return newValidationError("断连 rclone 导入必须使用 legacy_mutable 发布模式")
	}
	return nil
}

func ParseRcloneTaskConfigV1(raw string) (RcloneTaskConfigV1, error) {
	if strings.TrimSpace(raw) == "" {
		return defaultRcloneTaskConfigV1(), nil
	}
	if !strings.HasPrefix(strings.TrimSpace(raw), "{") || validateClosedRcloneTaskConfigObject(raw) != nil {
		return RcloneTaskConfigV1{}, newValidationError("rclone 发布配置无效")
	}

	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var wire rcloneTaskConfigV1Wire
	if err := decoder.Decode(&wire); err != nil {
		return RcloneTaskConfigV1{}, newValidationError("rclone 发布配置无效")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return RcloneTaskConfigV1{}, newValidationError("rclone 发布配置无效")
	}

	config := defaultRcloneTaskConfigV1()
	if wire.Version != nil {
		config.Version = *wire.Version
	}
	if config.Version != RcloneTaskConfigV1Version {
		return RcloneTaskConfigV1{}, newValidationError(fmt.Sprintf("不支持的 rclone 发布配置版本：%d", config.Version))
	}
	if wire.PublicationMode != nil {
		if wire.Version == nil {
			return RcloneTaskConfigV1{}, newValidationError("rclone publication_mode 需要版本化配置")
		}
		config.PublicationMode = *wire.PublicationMode
	}
	if wire.BandwidthLimit != nil {
		config.BandwidthLimit = *wire.BandwidthLimit
	}
	if wire.Transfers != nil {
		config.Transfers = *wire.Transfers
	}
	if err := validateRcloneTaskConfigV1(config); err != nil {
		return RcloneTaskConfigV1{}, err
	}
	return config, nil
}

func EncodeRcloneTaskConfigV1(config RcloneTaskConfigV1) (string, error) {
	if err := validateRcloneTaskConfigV1(config); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		return "", fmt.Errorf("encode Rclone task config: %w", err)
	}
	return string(encoded), nil
}

func validateRcloneTaskConfigV1(config RcloneTaskConfigV1) error {
	if config.Version != RcloneTaskConfigV1Version {
		return newValidationError("不支持的 rclone 发布配置版本")
	}
	switch config.PublicationMode {
	case backupasset.PublicationLegacyMutable, backupasset.PublicationVersionedPrefix, backupasset.PublicationNativeObjectVersions:
	default:
		return newValidationError("不支持的 rclone 发布模式")
	}
	if len(config.BandwidthLimit) > MaxRcloneBandwidthLimitBytes || !utf8.ValidString(config.BandwidthLimit) ||
		strings.ContainsRune(config.BandwidthLimit, '\x00') || strings.TrimSpace(config.BandwidthLimit) != config.BandwidthLimit {
		return newValidationError("rclone 带宽限制无效")
	}
	for _, character := range config.BandwidthLimit {
		if character < 0x20 || character == 0x7f {
			return newValidationError("rclone 带宽限制无效")
		}
	}
	if config.Transfers < 0 || config.Transfers > MaxRcloneTransfers {
		return newValidationError("rclone 并发传输数超出范围")
	}
	return nil
}

func validateClosedRcloneTaskConfigObject(raw string) error {
	decoder := json.NewDecoder(strings.NewReader(raw))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return fmt.Errorf("rclone task config must be an object")
	}
	seen := make(map[string]struct{})
	for decoder.More() {
		nameToken, err := decoder.Token()
		if err != nil {
			return err
		}
		name, ok := nameToken.(string)
		if !ok {
			return fmt.Errorf("rclone task config member must be a string")
		}
		if _, exists := seen[name]; exists {
			return fmt.Errorf("duplicate Rclone task config member")
		}
		seen[name] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("null Rclone task config member")
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return fmt.Errorf("invalid Rclone task config terminator")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return fmt.Errorf("trailing Rclone task config data")
	}
	return nil
}
