package provider

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"xirang/backend/internal/backupasset"
)

const (
	IdentitySaltBytes          = 32
	NativeResticIdentityPrefix = "restic-native:v1:"
	identityDomainLabel        = "repository-identity:v1"
	configDomainLabel          = "repository-config:v1"
)

type ScopedIdentityDocument struct {
	Provider      backupasset.ProviderKind
	TaskID        uint
	NodeID        uint
	EndpointFacts []string
}

func GenerateIdentitySalt() ([]byte, error) {
	return generateIdentitySaltFrom(rand.Reader)
}

func generateIdentitySaltFrom(source io.Reader) ([]byte, error) {
	salt := make([]byte, IdentitySaltBytes)
	if _, err := io.ReadFull(source, salt); err != nil {
		return nil, fmt.Errorf("generate repository identity salt: %w", err)
	}
	return salt, nil
}

func ScopedIdentityPrefix(provider backupasset.ProviderKind) string {
	if provider != backupasset.ProviderRsync && provider != backupasset.ProviderRclone {
		return ""
	}
	return string(provider) + "-task-endpoint:v1:"
}

func DeriveScopedIdentity(salt []byte, document ScopedIdentityDocument) (string, error) {
	if len(salt) != IdentitySaltBytes || ScopedIdentityPrefix(document.Provider) == "" || document.TaskID == 0 || document.NodeID == 0 {
		return "", fmt.Errorf("%w: invalid scoped repository identity input", backupasset.ErrInvalidState)
	}
	facts := append([]string(nil), document.EndpointFacts...)
	if len(facts) == 0 {
		return "", fmt.Errorf("%w: endpoint identity facts are required", backupasset.ErrInvalidState)
	}
	sort.Strings(facts)
	for index, fact := range facts {
		if strings.TrimSpace(fact) == "" || strings.ContainsAny(fact, "\x00\r\n") || len(fact) > 2048 || (index > 0 && fact == facts[index-1]) {
			return "", fmt.Errorf("%w: invalid endpoint identity fact", backupasset.ErrInvalidState)
		}
	}
	canonical, err := json.Marshal(struct {
		Schema   int                      `json:"schema"`
		Provider backupasset.ProviderKind `json:"provider"`
		TaskID   uint                     `json:"task_id"`
		NodeID   uint                     `json:"node_id"`
		Facts    []string                 `json:"facts"`
	}{1, document.Provider, document.TaskID, document.NodeID, facts})
	if err != nil {
		return "", fmt.Errorf("%w: canonicalize repository identity", backupasset.ErrInvalidState)
	}
	return ScopedIdentityPrefix(document.Provider) + domainHMAC(salt, identityDomainLabel, canonical), nil
}

func DeriveConfigFingerprint(salt, canonicalBinding []byte) (string, error) {
	if len(salt) != IdentitySaltBytes || len(canonicalBinding) == 0 || len(canonicalBinding) > 1<<20 {
		return "", fmt.Errorf("%w: invalid repository config fingerprint input", backupasset.ErrInvalidState)
	}
	return domainHMAC(salt, configDomainLabel, canonicalBinding), nil
}

func NativeRepositoryIdentity(provider backupasset.ProviderKind, nativeID string) (string, error) {
	if provider != backupasset.ProviderRestic || !lowerHex(nativeID, 64) {
		return "", fmt.Errorf("%w: native repository identity is unavailable", backupasset.ErrCapabilityUnavailable)
	}
	return NativeResticIdentityPrefix + nativeID, nil
}

func domainHMAC(key []byte, label string, payload []byte) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(label))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

func lowerHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}
