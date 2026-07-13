package provider

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
)

type staticCursorKeys struct {
	active   backupasset.DomainKeyMaterial
	versions map[int]backupasset.DomainKeyMaterial
}

func TestCursorRejectsMalformedTrailingData(t *testing.T) {
	now := time.Date(2026, 7, 13, 4, 5, 6, 0, time.UTC)
	material := backupasset.DomainKeyMaterial{Version: 1, Domain: backupasset.KeyDomainCursorSigning, Key: []byte("FAKE_CURSOR_SIGNING_KEY_FOR_TEST_ONLY")}
	keys := staticCursorKeys{active: material, versions: map[int]backupasset.DomainKeyMaterial{1: material}}
	codec := NewCursorCodec(keys, func() time.Time { return now }, time.Minute)
	scope := CursorScope{Provider: backupasset.ProviderRsync, RepositoryID: strings.Repeat("a", 32), CapabilityRevision: 1, LastItemDigest: strings.Repeat("c", 64), Direction: CursorForward}
	token, err := codec.Encode(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(token, ".")
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	payload = append(payload, 'x')
	forged := base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(signCursor(material.Key, payload))
	expected := scope
	expected.LastItemDigest = ""
	if _, err := codec.Decode(context.Background(), forged, expected); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("trailing data error=%v", err)
	}
}

func (keys staticCursorKeys) Active(context.Context, backupasset.KeyDomain) (backupasset.DomainKeyMaterial, error) {
	return keys.active, nil
}

func (keys staticCursorKeys) ByVersion(_ context.Context, _ backupasset.KeyDomain, version int) (backupasset.DomainKeyMaterial, error) {
	material, ok := keys.versions[version]
	if !ok {
		return backupasset.DomainKeyMaterial{}, backupasset.ErrKeyUnavailable
	}
	return material, nil
}

func TestCursorRoundTripAndScope(t *testing.T) {
	now := time.Date(2026, 7, 13, 4, 5, 6, 0, time.UTC)
	material := backupasset.DomainKeyMaterial{Version: 3, Domain: backupasset.KeyDomainCursorSigning, Key: []byte("FAKE_CURSOR_SIGNING_KEY_FOR_TEST_ONLY")}
	keys := staticCursorKeys{active: material, versions: map[int]backupasset.DomainKeyMaterial{3: material}}
	codec := NewCursorCodec(keys, func() time.Time { return now }, time.Hour)
	scope := CursorScope{
		Provider:           backupasset.ProviderRestic,
		RepositoryID:       strings.Repeat("a", 32),
		PointScopeDigest:   strings.Repeat("b", 64),
		ParentScopeDigest:  strings.Repeat("c", 64),
		CapabilityRevision: 7,
		SourceRevision:     strings.Repeat("d", 64),
		LastItemDigest:     strings.Repeat("e", 64),
		Direction:          CursorForward,
	}
	token, err := codec.Encode(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"/private/path", "remote:bucket", "entry-name"} {
		if strings.Contains(token, forbidden) {
			t.Fatalf("token leaked %q", forbidden)
		}
	}
	expected := scope
	expected.LastItemDigest = ""
	decoded, err := codec.Decode(context.Background(), token, expected)
	if err != nil || decoded != scope {
		t.Fatalf("decoded=%+v err=%v", decoded, err)
	}
}

func TestCursorRejectsTamperExpiryAndScopeMismatch(t *testing.T) {
	now := time.Date(2026, 7, 13, 4, 5, 6, 0, time.UTC)
	material := backupasset.DomainKeyMaterial{Version: 1, Domain: backupasset.KeyDomainCursorSigning, Key: []byte("FAKE_CURSOR_SIGNING_KEY_FOR_TEST_ONLY")}
	keys := staticCursorKeys{active: material, versions: map[int]backupasset.DomainKeyMaterial{1: material}}
	codec := NewCursorCodec(keys, func() time.Time { return now }, time.Minute)
	scope := CursorScope{Provider: backupasset.ProviderRsync, RepositoryID: strings.Repeat("a", 32), CapabilityRevision: 1, SourceRevision: strings.Repeat("b", 64), LastItemDigest: strings.Repeat("c", 64), Direction: CursorForward}
	token, err := codec.Encode(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	expected := scope
	expected.LastItemDigest = ""

	tampered := token[:len(token)-1] + "A"
	if _, err := codec.Decode(context.Background(), tampered, expected); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("tampered token error=%v", err)
	}
	mismatch := expected
	mismatch.CapabilityRevision++
	if _, err := codec.Decode(context.Background(), token, mismatch); !errors.Is(err, ErrStaleCursor) {
		t.Fatalf("scope mismatch error=%v", err)
	}
	now = now.Add(2 * time.Minute)
	if _, err := codec.Decode(context.Background(), token, expected); !errors.Is(err, ErrStaleCursor) {
		t.Fatalf("expired token error=%v", err)
	}
}
