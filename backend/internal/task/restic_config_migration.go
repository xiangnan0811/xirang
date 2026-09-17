package task

import (
	"encoding/json"
	"fmt"
	"strings"

	"xirang/backend/internal/secure"
)

// MigrateLegacyResticConfig performs the one-time domain cutover from the
// historical append_only setting to repository_version. It is deliberately a
// pure JSON transformation: startup migration and explicit config import are
// the only callers. Runtime task reads and edits must not silently remigrate
// persisted data.
//
// append_only=true is equivalent to repository_version=2 and append_only=false
// is equivalent to the default (no repository_version). If both fields are
// present, an explicit repository_version must agree with that meaning;
// conflicting or malformed values are rejected rather than overwritten.
func MigrateLegacyResticConfig(raw string) (string, bool, error) {
	if strings.TrimSpace(raw) == "" {
		return raw, false, nil
	}
	config, err := decodeTaskJSONObject([]byte(raw))
	if err != nil {
		return "", false, fmt.Errorf("invalid Restic executor_config: %w", err)
	}
	legacyRaw, exists := config["append_only"]
	if !exists {
		return raw, false, nil
	}

	var appendOnly bool
	if strings.TrimSpace(string(legacyRaw)) == "null" || json.Unmarshal(legacyRaw, &appendOnly) != nil {
		return "", false, fmt.Errorf("invalid Restic append_only value")
	}

	explicitVersion, hasExplicitVersion, err := resticRepositoryVersion(config)
	if err != nil {
		return "", false, err
	}
	if hasExplicitVersion {
		wanted := 1
		if appendOnly {
			wanted = 2
		}
		if explicitVersion != wanted {
			return "", false, fmt.Errorf("restic append_only conflicts with explicit repository_version")
		}
	}

	delete(config, "append_only")
	if appendOnly {
		config["repository_version"] = json.RawMessage("2")
	} else {
		// The legacy false value means the default repository format. Remove an
		// explicit matching version as well so the persisted representation is
		// canonical and parser-compatible.
		delete(config, "repository_version")
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		return "", false, fmt.Errorf("encode migrated Restic executor_config: %w", err)
	}
	return string(encoded), true, nil
}

func resticRepositoryVersion(config map[string]json.RawMessage) (int, bool, error) {
	raw, exists := config["repository_version"]
	if !exists || strings.TrimSpace(string(raw)) == "null" {
		return 0, false, nil
	}
	var version int
	if err := json.Unmarshal(raw, &version); err != nil || (version != 1 && version != 2) {
		return 0, false, fmt.Errorf("invalid Restic repository_version")
	}
	return version, true, nil
}

// NormalizeImportedResticConfig is the explicit config-import boundary for
// snapshots that carry an old Restic configuration. Encrypted payloads must be
// readable with the installation encryption keys; model hooks re-encrypt the
// normalized plaintext on persistence.
func NormalizeImportedResticConfig(raw string) (string, error) {
	plain, err := secure.DecryptIfNeeded(raw)
	if err != nil {
		return "", fmt.Errorf("decrypt imported Restic executor_config: %w", err)
	}
	migrated, _, err := MigrateLegacyResticConfig(plain)
	if err != nil {
		return "", err
	}
	return migrated, nil
}
