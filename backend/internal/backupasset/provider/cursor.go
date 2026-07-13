package provider

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"xirang/backend/internal/backupasset"
)

const (
	cursorFormatVersion = 1
	maxCursorBytes      = 8192
	CursorForward       = "forward"
	CursorBackward      = "backward"
)

type CursorScope struct {
	Provider           backupasset.ProviderKind `json:"provider"`
	RepositoryID       string                   `json:"repository_id"`
	PointScopeDigest   string                   `json:"point_scope_digest,omitempty"`
	ParentScopeDigest  string                   `json:"parent_scope_digest,omitempty"`
	CapabilityRevision int                      `json:"capability_revision"`
	SourceRevision     string                   `json:"source_revision,omitempty"`
	LastItemDigest     string                   `json:"last_item_digest"`
	Direction          string                   `json:"direction"`
}

type CursorKeySource interface {
	Active(context.Context, backupasset.KeyDomain) (backupasset.DomainKeyMaterial, error)
	ByVersion(context.Context, backupasset.KeyDomain, int) (backupasset.DomainKeyMaterial, error)
}

type CursorCodec struct {
	keys CursorKeySource
	now  func() time.Time
	ttl  time.Duration
}

type cursorEnvelope struct {
	FormatVersion int         `json:"format_version"`
	KeyVersion    int         `json:"key_version"`
	IssuedAt      int64       `json:"issued_at"`
	ExpiresAt     int64       `json:"expires_at"`
	Scope         CursorScope `json:"scope"`
}

func NewCursorCodec(keys CursorKeySource, now func() time.Time, ttl time.Duration) *CursorCodec {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &CursorCodec{keys: keys, now: now, ttl: ttl}
}

func (codec *CursorCodec) Encode(ctx context.Context, scope CursorScope) (string, error) {
	if codec == nil || codec.keys == nil || codec.ttl <= 0 || codec.ttl > 7*24*time.Hour {
		return "", fmt.Errorf("%w: cursor codec unavailable", ErrInvalidCursor)
	}
	if err := validateCursorScope(scope, true); err != nil {
		return "", err
	}
	material, err := codec.keys.Active(ctx, backupasset.KeyDomainCursorSigning)
	if err != nil || material.Version <= 0 || len(material.Key) < 16 {
		return "", fmt.Errorf("%w: cursor signing key unavailable", ErrInvalidCursor)
	}
	now := codec.now().UTC()
	envelope := cursorEnvelope{FormatVersion: cursorFormatVersion, KeyVersion: material.Version, IssuedAt: now.Unix(), ExpiresAt: now.Add(codec.ttl).Unix(), Scope: scope}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return "", fmt.Errorf("%w: encode cursor payload", ErrInvalidCursor)
	}
	signature := signCursor(material.Key, payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func (codec *CursorCodec) Decode(ctx context.Context, token string, expected CursorScope) (CursorScope, error) {
	if codec == nil || codec.keys == nil || len(token) == 0 || len(token) > maxCursorBytes {
		return CursorScope{}, fmt.Errorf("%w: cursor token unavailable", ErrInvalidCursor)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return CursorScope{}, fmt.Errorf("%w: cursor token format", ErrInvalidCursor)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || len(payload) == 0 || len(payload) > maxCursorBytes {
		return CursorScope{}, fmt.Errorf("%w: cursor payload encoding", ErrInvalidCursor)
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return CursorScope{}, fmt.Errorf("%w: cursor signature encoding", ErrInvalidCursor)
	}
	var envelope cursorEnvelope
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return CursorScope{}, fmt.Errorf("%w: cursor payload schema", ErrInvalidCursor)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return CursorScope{}, fmt.Errorf("%w: cursor payload schema", ErrInvalidCursor)
	}
	if envelope.FormatVersion != cursorFormatVersion || envelope.KeyVersion <= 0 {
		return CursorScope{}, fmt.Errorf("%w: cursor version", ErrInvalidCursor)
	}
	material, err := codec.keys.ByVersion(ctx, backupasset.KeyDomainCursorSigning, envelope.KeyVersion)
	if err != nil || len(material.Key) < 16 || !hmac.Equal(signature, signCursor(material.Key, payload)) {
		return CursorScope{}, fmt.Errorf("%w: cursor authentication", ErrInvalidCursor)
	}
	if err := validateCursorScope(envelope.Scope, true); err != nil {
		return CursorScope{}, err
	}
	now := codec.now().UTC().Unix()
	if envelope.IssuedAt <= 0 || envelope.ExpiresAt <= envelope.IssuedAt || now > envelope.ExpiresAt || envelope.IssuedAt > now+60 {
		return CursorScope{}, fmt.Errorf("%w: cursor expired", ErrStaleCursor)
	}
	if !cursorScopeMatches(envelope.Scope, expected) {
		return CursorScope{}, fmt.Errorf("%w: cursor scope changed", ErrStaleCursor)
	}
	return envelope.Scope, nil
}

func validateCursorScope(scope CursorScope, requireLast bool) error {
	if !readableProvider(scope.Provider) || backupasset.ValidateOpaqueID(scope.RepositoryID) != nil || scope.CapabilityRevision <= 0 ||
		(scope.Direction != CursorForward && scope.Direction != CursorBackward) {
		return fmt.Errorf("%w: invalid cursor scope", ErrInvalidCursor)
	}
	for _, digest := range []string{scope.PointScopeDigest, scope.ParentScopeDigest, scope.SourceRevision} {
		if digest != "" && !lowerHex(digest, 64) {
			return fmt.Errorf("%w: invalid cursor scope digest", ErrInvalidCursor)
		}
	}
	if requireLast && !lowerHex(scope.LastItemDigest, 64) {
		return fmt.Errorf("%w: invalid cursor item digest", ErrInvalidCursor)
	}
	return nil
}

func cursorScopeMatches(actual, expected CursorScope) bool {
	return (expected.Provider == "" || actual.Provider == expected.Provider) &&
		(expected.RepositoryID == "" || actual.RepositoryID == expected.RepositoryID) &&
		(expected.PointScopeDigest == "" || actual.PointScopeDigest == expected.PointScopeDigest) &&
		(expected.ParentScopeDigest == "" || actual.ParentScopeDigest == expected.ParentScopeDigest) &&
		(expected.CapabilityRevision == 0 || actual.CapabilityRevision == expected.CapabilityRevision) &&
		(expected.SourceRevision == "" || actual.SourceRevision == expected.SourceRevision) &&
		(expected.Direction == "" || actual.Direction == expected.Direction) &&
		(expected.LastItemDigest == "" || actual.LastItemDigest == expected.LastItemDigest)
}

func signCursor(key, payload []byte) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(payload)
	return mac.Sum(nil)
}
