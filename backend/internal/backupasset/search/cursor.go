package search

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
	searchCursorFormatVersion = 1
	maxSearchCursorBytes      = 8192
)

type CursorKeySource interface {
	Active(context.Context, backupasset.KeyDomain) (backupasset.DomainKeyMaterial, error)
	ByVersion(context.Context, backupasset.KeyDomain, int) (backupasset.DomainKeyMaterial, error)
}

type SearchCursorBinding struct {
	UserID               uint       `json:"user_id"`
	Role                 string     `json:"role"`
	Sort                 SearchSort `json:"sort"`
	QueryHMAC            string     `json:"query_hmac"`
	ScopeDigest          string     `json:"scope_digest"`
	SelectionDigest      string     `json:"selection_digest"`
	ProjectionDigest     string     `json:"projection_digest"`
	ClassificationDigest string     `json:"classification_digest"`
	TagDigest            string     `json:"tag_digest,omitempty"`
	SearchKeyVersion     int        `json:"search_key_version"`
	ProofDigest          string     `json:"proof_digest,omitempty"`
	AnchorID             string     `json:"anchor_id,omitempty"`
}

type searchCursorEnvelope struct {
	FormatVersion int                 `json:"format_version"`
	KeyVersion    int                 `json:"key_version"`
	IssuedAt      int64               `json:"issued_at"`
	ExpiresAt     int64               `json:"expires_at"`
	Binding       SearchCursorBinding `json:"binding"`
}

type CursorCodec struct {
	keys CursorKeySource
	now  func() time.Time
	ttl  time.Duration
}

func NewCursorCodec(keys CursorKeySource, now func() time.Time, ttl time.Duration) *CursorCodec {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &CursorCodec{keys: keys, now: now, ttl: ttl}
}

func (codec *CursorCodec) Encode(ctx context.Context, binding SearchCursorBinding) (string, error) {
	if codec == nil || codec.keys == nil || codec.ttl <= 0 || codec.ttl > 15*time.Minute {
		return "", cursorError("codec unavailable")
	}
	if !binding.valid(true) {
		return "", cursorError("binding")
	}
	material, err := codec.keys.Active(ctx, backupasset.KeyDomainCursorSigning)
	if err != nil || material.Version <= 0 || material.State != backupasset.DomainKeyActive || len(material.Key) != 32 {
		return "", cursorError("signing key")
	}
	now := codec.now().UTC()
	envelope := searchCursorEnvelope{
		FormatVersion: searchCursorFormatVersion, KeyVersion: material.Version,
		IssuedAt: now.Unix(), ExpiresAt: now.Add(codec.ttl).Unix(), Binding: binding,
	}
	payload, err := json.Marshal(envelope)
	if err != nil || len(payload) > maxSearchCursorBytes {
		return "", cursorError("payload")
	}
	signature := signSearchCursor(material.Key, payload)
	token := base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(signature)
	if len(token) > maxSearchCursorBytes {
		return "", cursorError("token size")
	}
	return token, nil
}

func (codec *CursorCodec) Decode(ctx context.Context, token string, expected SearchCursorBinding) (SearchCursorBinding, error) {
	if codec == nil || codec.keys == nil || token == "" || len(token) > maxSearchCursorBytes || !expected.valid(false) {
		return SearchCursorBinding{}, ErrInvalidCursor
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return SearchCursorBinding{}, ErrInvalidCursor
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || len(payload) == 0 || len(payload) > maxSearchCursorBytes {
		return SearchCursorBinding{}, ErrInvalidCursor
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(signature) != sha256.Size {
		return SearchCursorBinding{}, ErrInvalidCursor
	}
	var envelope searchCursorEnvelope
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return SearchCursorBinding{}, ErrInvalidCursor
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) || envelope.FormatVersion != searchCursorFormatVersion ||
		envelope.KeyVersion <= 0 || !envelope.Binding.valid(true) {
		return SearchCursorBinding{}, ErrInvalidCursor
	}
	now := codec.now().UTC()
	material, err := codec.keys.ByVersion(ctx, backupasset.KeyDomainCursorSigning, envelope.KeyVersion)
	if err != nil || len(material.Key) != 32 || !searchCursorKeyMayVerify(material, now) ||
		!hmac.Equal(signature, signSearchCursor(material.Key, payload)) {
		return SearchCursorBinding{}, ErrInvalidCursor
	}
	if envelope.IssuedAt <= 0 || envelope.ExpiresAt <= envelope.IssuedAt ||
		!now.Before(time.Unix(envelope.ExpiresAt, 0).UTC()) || envelope.IssuedAt > now.Add(time.Minute).Unix() {
		return SearchCursorBinding{}, ErrStaleCursor
	}
	if !searchCursorBindingsMatch(envelope.Binding, expected) {
		return SearchCursorBinding{}, ErrStaleCursor
	}
	return envelope.Binding, nil
}

func (binding SearchCursorBinding) valid(requireAnchor bool) bool {
	if binding.UserID == 0 || (binding.Role != "admin" && binding.Role != "operator") || !validSearchSort(binding.Sort) ||
		!lowerHex(binding.QueryHMAC, 64) || !lowerHex(binding.ScopeDigest, 64) ||
		!lowerHex(binding.SelectionDigest, 64) || !lowerHex(binding.ProjectionDigest, 64) ||
		!lowerHex(binding.ClassificationDigest, 64) || binding.SearchKeyVersion <= 0 ||
		(binding.TagDigest != "" && !lowerHex(binding.TagDigest, 64)) ||
		(binding.ProofDigest != "" && !lowerHex(binding.ProofDigest, 64)) {
		return false
	}
	if requireAnchor {
		return lowerHex(binding.AnchorID, 32) || lowerHex(binding.AnchorID, 64)
	}
	return binding.AnchorID == "" || lowerHex(binding.AnchorID, 32) || lowerHex(binding.AnchorID, 64)
}

func searchCursorBindingsMatch(actual, expected SearchCursorBinding) bool {
	if actual.UserID != expected.UserID || actual.Role != expected.Role || actual.Sort != expected.Sort ||
		actual.QueryHMAC != expected.QueryHMAC || actual.ScopeDigest != expected.ScopeDigest ||
		actual.SelectionDigest != expected.SelectionDigest || actual.ProjectionDigest != expected.ProjectionDigest ||
		actual.ClassificationDigest != expected.ClassificationDigest || actual.TagDigest != expected.TagDigest ||
		actual.SearchKeyVersion != expected.SearchKeyVersion ||
		actual.ProofDigest != expected.ProofDigest {
		return false
	}
	return expected.AnchorID == "" || actual.AnchorID == expected.AnchorID
}

func lowerHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func searchCursorKeyMayVerify(material backupasset.DomainKeyMaterial, now time.Time) bool {
	switch material.State {
	case backupasset.DomainKeyActive:
		return true
	case backupasset.DomainKeyVerifyOnly:
		return material.VerifyUntil != nil && now.Before(material.VerifyUntil.UTC())
	default:
		return false
	}
}

func signSearchCursor(key, payload []byte) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("xirang.search.cursor.v1"))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(payload)
	return mac.Sum(nil)
}

func cursorError(detail string) error {
	return fmt.Errorf("%w: %s", ErrInvalidCursor, detail)
}
