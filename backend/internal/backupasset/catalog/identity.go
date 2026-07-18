package catalog

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"xirang/backend/internal/backupasset"
)

const (
	entryIdentityDomain = "xirang.catalog.entry.v1"
	maxEntryPathBytes   = 8192
)

type EntryIdentity struct {
	EntryID        string
	ParentEntryID  *string
	Name           string
	NormalizedPath string
}

type IdentityRegistry struct {
	paths map[string]string
}

func NewIdentityRegistry() *IdentityRegistry {
	return &IdentityRegistry{paths: make(map[string]string)}
}

func (registry *IdentityRegistry) Add(identity EntryIdentity) error {
	if registry == nil || !lowerHexLength(identity.EntryID, 64) {
		return fmt.Errorf("%w: invalid entry identity", ErrInvalidAssetReference)
	}
	components, err := validateNormalizedEntryPath(identity.NormalizedPath)
	if err != nil || identity.Name != components[len(components)-1] {
		return fmt.Errorf("%w: invalid entry identity path", ErrInvalidAssetReference)
	}
	if registry.paths == nil {
		registry.paths = make(map[string]string)
	}
	if existing, ok := registry.paths[identity.EntryID]; ok {
		if existing == identity.NormalizedPath {
			return fmt.Errorf("%w: repeated canonical path", ErrDuplicateEntry)
		}
		return fmt.Errorf("%w: distinct paths share an entry ID", ErrIdentityCollision)
	}
	registry.paths[identity.EntryID] = identity.NormalizedPath
	return nil
}

func DeriveEntryIdentity(key []byte, recoveryPointID, normalizedPath string) (EntryIdentity, error) {
	if len(key) < 32 {
		return EntryIdentity{}, fmt.Errorf("%w: entry identity key must contain at least 32 bytes", ErrIdentityKeyUnavailable)
	}
	if backupasset.ValidateOpaqueID(recoveryPointID) != nil {
		return EntryIdentity{}, fmt.Errorf("%w: invalid recovery point ID", ErrInvalidAssetReference)
	}
	components, err := validateNormalizedEntryPath(normalizedPath)
	if err != nil {
		return EntryIdentity{}, err
	}
	identity := EntryIdentity{
		EntryID:        deriveEntryID(key, recoveryPointID, normalizedPath),
		Name:           components[len(components)-1],
		NormalizedPath: normalizedPath,
	}
	if len(components) > 1 {
		parentPath := strings.Join(components[:len(components)-1], "/")
		parentID := deriveEntryID(key, recoveryPointID, parentPath)
		identity.ParentEntryID = &parentID
	}
	return identity, nil
}

func validateNormalizedEntryPath(value string) ([]string, error) {
	if value == "" || len(value) > maxEntryPathBytes || strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") ||
		strings.Contains(value, "\\") || strings.ContainsRune(value, '\x00') {
		return nil, fmt.Errorf("%w: entry path is not a bounded relative path", ErrUnsafeEntryPath)
	}
	components := strings.Split(value, "/")
	for _, component := range components {
		if component == "" || component == "." || component == ".." {
			return nil, fmt.Errorf("%w: entry path contains an unsafe component", ErrUnsafeEntryPath)
		}
	}
	return components, nil
}

func deriveEntryID(key []byte, recoveryPointID, normalizedPath string) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(entryIdentityDomain))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(recoveryPointID))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(normalizedPath))
	return hex.EncodeToString(mac.Sum(nil))
}
