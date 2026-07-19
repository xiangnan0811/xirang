package processing

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestDerivedCryptoRoundTripsEmptySingleAndMultiChunkBlobs(t *testing.T) {
	for _, test := range []struct {
		name    string
		payload []byte
	}{{"empty", nil}, {"single", []byte("small passive artifact")}, {"multi", bytes.Repeat([]byte("derived-data"), 14000)}} {
		t.Run(test.name, func(t *testing.T) {
			cipher := newDerivedTestCipher(t, 1)
			spec := derivedCryptoSpec(test.payload, 1, bytes.Repeat([]byte{0x42}, 32))
			var encrypted bytes.Buffer
			metadata, err := cipher.Encrypt(&encrypted, bytes.NewReader(test.payload), spec)
			if err != nil {
				t.Fatalf("Encrypt: %v", err)
			}
			if metadata.ChunkCount < 1 || metadata.PhysicalSize != int64(encrypted.Len()) || len(metadata.WrappedDEK) == 0 || len(metadata.NoncePrefix) != 8 || len(metadata.EnvelopeNonce) != 12 {
				t.Fatalf("invalid encrypted metadata: %+v", metadata)
			}
			var plaintext bytes.Buffer
			if err := cipher.Decrypt(&plaintext, bytes.NewReader(encrypted.Bytes()), spec, metadata); err != nil {
				t.Fatalf("Decrypt: %v", err)
			}
			if !bytes.Equal(plaintext.Bytes(), test.payload) {
				t.Fatalf("round trip differs: got=%d want=%d", plaintext.Len(), len(test.payload))
			}
		})
	}
}

func TestDerivedCryptoRejectsTamperTruncationWrongKeyAndAAD(t *testing.T) {
	payload := bytes.Repeat([]byte("tamper-evidence"), 9000)
	cipher := newDerivedTestCipher(t, 2)
	spec := derivedCryptoSpec(payload, 7, bytes.Repeat([]byte{0x33}, 32))
	var encrypted bytes.Buffer
	metadata, err := cipher.Encrypt(&encrypted, bytes.NewReader(payload), spec)
	if err != nil {
		t.Fatal(err)
	}
	mutations := []struct {
		name     string
		payload  []byte
		spec     DerivedCryptoSpec
		metadata DerivedCryptoMetadata
	}{
		{name: "bit tamper", payload: mutateByte(encrypted.Bytes(), encrypted.Len()/2), spec: spec, metadata: metadata},
		{name: "truncated", payload: append([]byte(nil), encrypted.Bytes()[:encrypted.Len()-1]...), spec: spec, metadata: metadata},
		{name: "trailing", payload: append(append([]byte(nil), encrypted.Bytes()...), 0), spec: spec, metadata: metadata},
		{name: "wrong key", payload: encrypted.Bytes(), spec: withDerivedKEK(spec, bytes.Repeat([]byte{0x44}, 32), spec.KEKVersion), metadata: metadata},
		{name: "wrong version", payload: encrypted.Bytes(), spec: withDerivedKEK(spec, spec.KEK, spec.KEKVersion+1), metadata: metadata},
		{name: "wrong blob", payload: encrypted.Bytes(), spec: withDerivedBlobID(spec, strings.Repeat("f", 32)), metadata: metadata},
		{name: "wrong digest", payload: encrypted.Bytes(), spec: withDerivedDigest(spec, strings.Repeat("0", 64)), metadata: metadata},
		{name: "wrong size", payload: encrypted.Bytes(), spec: withDerivedSize(spec, spec.PlaintextSize+1), metadata: metadata},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			err := cipher.Decrypt(io.Discard, bytes.NewReader(mutation.payload), mutation.spec, mutation.metadata)
			if !errors.Is(err, ErrDerivedTamper) {
				t.Fatalf("Decrypt got %v, want ErrDerivedTamper", err)
			}
		})
	}
}

func TestDerivedKEKRewrapChangesOnlyEnvelopeMetadata(t *testing.T) {
	payload := bytes.Repeat([]byte("rewrap"), 20000)
	cipher := newDerivedTestCipher(t, 3)
	oldSpec := derivedCryptoSpec(payload, 3, bytes.Repeat([]byte{0x11}, 32))
	var encrypted bytes.Buffer
	metadata, err := cipher.Encrypt(&encrypted, bytes.NewReader(payload), oldSpec)
	if err != nil {
		t.Fatal(err)
	}
	beforeCiphertext := append([]byte(nil), encrypted.Bytes()...)
	newSpec := withDerivedKEK(oldSpec, bytes.Repeat([]byte{0x22}, 32), 4)
	rewrapped, err := cipher.RewrapDEK(metadata, oldSpec, newSpec)
	if err != nil {
		t.Fatalf("RewrapDEK: %v", err)
	}
	if rewrapped.KEKVersion != 4 || bytes.Equal(rewrapped.WrappedDEK, metadata.WrappedDEK) || bytes.Equal(rewrapped.EnvelopeNonce, metadata.EnvelopeNonce) {
		t.Fatalf("envelope did not rotate: before=%+v after=%+v", metadata, rewrapped)
	}
	if !bytes.Equal(beforeCiphertext, encrypted.Bytes()) {
		t.Fatal("DEK rewrap rewrote ciphertext")
	}
	var plaintext bytes.Buffer
	if err := cipher.Decrypt(&plaintext, bytes.NewReader(encrypted.Bytes()), newSpec, rewrapped); err != nil {
		t.Fatalf("Decrypt after rewrap: %v", err)
	}
	if !bytes.Equal(plaintext.Bytes(), payload) {
		t.Fatal("rewrapped plaintext differs")
	}
	if err := cipher.Decrypt(io.Discard, bytes.NewReader(encrypted.Bytes()), oldSpec, rewrapped); !errors.Is(err, ErrDerivedTamper) {
		t.Fatalf("old KEK accepted rewrapped envelope: %v", err)
	}
}

func newDerivedTestCipher(t *testing.T, seed byte) *DerivedCipher {
	t.Helper()
	entropy := make([]byte, 512)
	for index := range entropy {
		entropy[index] = seed + byte(index)
	}
	cipher, err := NewDerivedCipher(bytes.NewReader(entropy))
	if err != nil {
		t.Fatal(err)
	}
	return cipher
}

func derivedCryptoSpec(payload []byte, version int, kek []byte) DerivedCryptoSpec {
	digest := sha256.Sum256(payload)
	return DerivedCryptoSpec{
		BlobID: strings.Repeat("a", 32), PlaintextDigest: hex.EncodeToString(digest[:]),
		PlaintextSize: int64(len(payload)), ChunkSize: 64 * 1024, KEKVersion: version, KEK: append([]byte(nil), kek...),
	}
}

func mutateByte(payload []byte, index int) []byte {
	result := append([]byte(nil), payload...)
	result[index] ^= 0x80
	return result
}

func withDerivedKEK(spec DerivedCryptoSpec, key []byte, version int) DerivedCryptoSpec {
	spec.KEK = append([]byte(nil), key...)
	spec.KEKVersion = version
	return spec
}

func withDerivedBlobID(spec DerivedCryptoSpec, id string) DerivedCryptoSpec {
	spec.BlobID = id
	return spec
}

func withDerivedDigest(spec DerivedCryptoSpec, digest string) DerivedCryptoSpec {
	spec.PlaintextDigest = digest
	return spec
}

func withDerivedSize(spec DerivedCryptoSpec, size int64) DerivedCryptoSpec {
	spec.PlaintextSize = size
	return spec
}
