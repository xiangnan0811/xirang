package secure

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"
)

func TestWrapDomainKeyRoundTripWithAAD(t *testing.T) {
	configureDomainWrappingKeys(t, "FAKE_CURRENT_KEK_FOR_TEST_ONLY", "")
	plain := bytes.Repeat([]byte{0x42}, DomainKeySize)

	wrapped, err := WrapDomainKey("entry_identity", 7, plain)
	if err != nil {
		t.Fatalf("WrapDomainKey: %v", err)
	}
	got, err := UnwrapDomainKey("entry_identity", 7, wrapped)
	if err != nil {
		t.Fatalf("UnwrapDomainKey: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("round trip mismatch: got %x want %x", got, plain)
	}
	if wrapped.Algorithm != DomainKeyWrapAlgorithm || len(wrapped.KEKFingerprint) != 64 {
		t.Fatalf("unexpected wrapped metadata: %+v", wrapped)
	}
}

func TestWrappedDomainKeyRejectsWrongDomainVersionAndTamper(t *testing.T) {
	configureDomainWrappingKeys(t, "FAKE_CURRENT_KEK_FOR_TEST_ONLY", "")
	plain := bytes.Repeat([]byte{0x24}, DomainKeySize)
	wrapped, err := WrapDomainKey("cursor_signing", 3, plain)
	if err != nil {
		t.Fatalf("WrapDomainKey: %v", err)
	}

	if _, err := UnwrapDomainKey("audit_fingerprint", 3, wrapped); err == nil {
		t.Fatal("wrong domain unexpectedly unwrapped")
	}
	if _, err := UnwrapDomainKey("cursor_signing", 4, wrapped); err == nil {
		t.Fatal("wrong version unexpectedly unwrapped")
	}

	tampered := wrapped
	last := len(tampered.Envelope) - 2
	if tampered.Envelope[last] == 'a' {
		tampered.Envelope = tampered.Envelope[:last] + "b" + tampered.Envelope[last+1:]
	} else {
		tampered.Envelope = tampered.Envelope[:last] + "a" + tampered.Envelope[last+1:]
	}
	if _, err := UnwrapDomainKey("cursor_signing", 3, tampered); err == nil {
		t.Fatal("tampered envelope unexpectedly unwrapped")
	}
}

func TestWrappedDomainKeyRejectsTrailingEnvelopeData(t *testing.T) {
	configureDomainWrappingKeys(t, "FAKE_CURRENT_KEK_FOR_TEST_ONLY", "")
	plain := bytes.Repeat([]byte{0x35}, DomainKeySize)
	wrapped, err := WrapDomainKey("cursor_signing", 5, plain)
	if err != nil {
		t.Fatalf("WrapDomainKey: %v", err)
	}

	wrapped.Envelope += " trailing-data"
	if _, err := UnwrapDomainKey("cursor_signing", 5, wrapped); err == nil {
		t.Fatal("wrapped envelope with trailing data unexpectedly unwrapped")
	}
}

func TestUnwrapDomainKeyAcceptsLegacyV2KEK(t *testing.T) {
	oldKEK := "FAKE_OLD_V2_KEK_FOR_TEST_ONLY"
	configureDomainWrappingKeys(t, oldKEK, "")
	plain := bytes.Repeat([]byte{0x7a}, DomainKeySize)
	wrapped, err := WrapDomainKey("audit_fingerprint", 11, plain)
	if err != nil {
		t.Fatalf("wrap with old KEK: %v", err)
	}

	configureDomainWrappingKeys(t, "FAKE_NEW_V2_KEK_FOR_TEST_ONLY", oldKEK)
	got, err := UnwrapDomainKey("audit_fingerprint", 11, wrapped)
	if err != nil {
		t.Fatalf("unwrap through legacy v2 KEK: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("legacy KEK round trip mismatch: got %x want %x", got, plain)
	}
}

func TestWrappedEnvelopeContainsNoPlaintextOrKEK(t *testing.T) {
	kek := "FAKE_CURRENT_KEK_FOR_TEST_ONLY"
	configureDomainWrappingKeys(t, kek, "")
	plain := bytes.Repeat([]byte{0x5c}, DomainKeySize)
	wrapped, err := WrapDomainKey("recovery_cleanup_ownership", 1, plain)
	if err != nil {
		t.Fatalf("WrapDomainKey: %v", err)
	}

	serialized := wrapped.Envelope + wrapped.Algorithm + wrapped.KEKFingerprint
	if strings.Contains(serialized, string(plain)) || strings.Contains(serialized, hex.EncodeToString(plain)) {
		t.Fatalf("wrapped envelope contains plaintext domain key: %s", serialized)
	}
	if strings.Contains(serialized, kek) {
		t.Fatalf("wrapped envelope contains KEK material: %s", serialized)
	}
}

func configureDomainWrappingKeys(t *testing.T, current, legacy string) {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", current)
	t.Setenv("DATA_ENCRYPTION_LEGACY_KEY", legacy)
	ResetForTesting()
	t.Cleanup(ResetForTesting)
}
