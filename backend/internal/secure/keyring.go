package secure

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

const (
	DomainKeySize          = 32
	DomainKeyWrapAlgorithm = "aes-256-gcm"
	domainKeyEnvelopeV1    = "xirang-wrapped-key-v1"
)

type WrappedDomainKey struct {
	Envelope       string
	Algorithm      string
	KEKFingerprint string
}

type domainKeyEnvelope struct {
	Version        string `json:"version"`
	Algorithm      string `json:"algorithm"`
	Nonce          string `json:"nonce"`
	Ciphertext     string `json:"ciphertext"`
	KEKFingerprint string `json:"kek_fingerprint"`
}

func WrapDomainKey(domain string, version int, plaintext []byte) (WrappedDomainKey, error) {
	if err := validateDomainKeyInput(domain, version, plaintext); err != nil {
		return WrappedDomainKey{}, err
	}
	kek, err := getPrimaryKey()
	if err != nil {
		return WrappedDomainKey{}, fmt.Errorf("load wrapping key: %w", err)
	}
	return wrapDomainKeyWithKEK(domain, version, plaintext, kek)
}

func UnwrapDomainKey(domain string, version int, wrapped WrappedDomainKey) ([]byte, error) {
	if strings.TrimSpace(domain) == "" || version <= 0 || strings.TrimSpace(wrapped.Envelope) == "" {
		return nil, errors.New("invalid wrapped domain key input")
	}
	if wrapped.Algorithm != DomainKeyWrapAlgorithm || len(wrapped.KEKFingerprint) != sha256.Size*2 {
		return nil, errors.New("invalid wrapped domain key metadata")
	}

	var envelope domainKeyEnvelope
	decoder := json.NewDecoder(strings.NewReader(wrapped.Envelope))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return nil, errors.New("invalid wrapped domain key envelope")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("invalid wrapped domain key envelope")
	}
	if envelope.Version != domainKeyEnvelopeV1 || envelope.Algorithm != wrapped.Algorithm ||
		!constantTimeStringEqual(envelope.KEKFingerprint, wrapped.KEKFingerprint) {
		return nil, errors.New("invalid wrapped domain key envelope metadata")
	}

	nonce, err := base64.RawStdEncoding.DecodeString(envelope.Nonce)
	if err != nil {
		return nil, errors.New("invalid wrapped domain key nonce")
	}
	ciphertext, err := base64.RawStdEncoding.DecodeString(envelope.Ciphertext)
	if err != nil {
		return nil, errors.New("invalid wrapped domain key ciphertext")
	}

	candidates, err := domainWrappingKEKs()
	if err != nil {
		return nil, err
	}
	aad := domainKeyAAD(domain, version, wrapped.Algorithm)
	for _, candidate := range candidates {
		if !constantTimeStringEqual(kekFingerprint(candidate), wrapped.KEKFingerprint) {
			continue
		}
		plaintext, openErr := openDomainKey(candidate, nonce, ciphertext, aad)
		if openErr == nil && len(plaintext) == DomainKeySize {
			return plaintext, nil
		}
	}
	return nil, errors.New("wrapped domain key is unavailable")
}

func wrapDomainKeyWithKEK(domain string, version int, plaintext, kek []byte) (WrappedDomainKey, error) {
	block, err := aes.NewCipher(kek)
	if err != nil {
		return WrappedDomainKey{}, fmt.Errorf("create domain key cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return WrappedDomainKey{}, fmt.Errorf("create domain key GCM: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return WrappedDomainKey{}, fmt.Errorf("generate domain key nonce: %w", err)
	}
	fingerprint := kekFingerprint(kek)
	envelope := domainKeyEnvelope{
		Version:        domainKeyEnvelopeV1,
		Algorithm:      DomainKeyWrapAlgorithm,
		Nonce:          base64.RawStdEncoding.EncodeToString(nonce),
		Ciphertext:     base64.RawStdEncoding.EncodeToString(gcm.Seal(nil, nonce, plaintext, domainKeyAAD(domain, version, DomainKeyWrapAlgorithm))),
		KEKFingerprint: fingerprint,
	}
	serialized, err := json.Marshal(envelope)
	if err != nil {
		return WrappedDomainKey{}, fmt.Errorf("serialize wrapped domain key: %w", err)
	}
	return WrappedDomainKey{
		Envelope:       string(serialized),
		Algorithm:      DomainKeyWrapAlgorithm,
		KEKFingerprint: fingerprint,
	}, nil
}

func openDomainKey(kek, nonce, ciphertext, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(kek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(nonce) != gcm.NonceSize() {
		return nil, errors.New("invalid nonce size")
	}
	return gcm.Open(nil, nonce, ciphertext, aad)
}

func domainWrappingKEKs() ([][]byte, error) {
	current, err := getPrimaryKey()
	if err != nil {
		return nil, fmt.Errorf("load current wrapping key: %w", err)
	}
	candidates := [][]byte{append([]byte(nil), current...)}
	legacyRaw := strings.TrimSpace(os.Getenv("DATA_ENCRYPTION_LEGACY_KEY"))
	if legacyRaw == "" {
		return candidates, nil
	}
	legacyV2, _, err := deriveKeyPair(legacyRaw)
	if err != nil {
		return nil, fmt.Errorf("derive legacy v2 wrapping key: %w", err)
	}
	if !bytes.Equal(current, legacyV2) {
		candidates = append(candidates, legacyV2)
	}
	return candidates, nil
}

func validateDomainKeyInput(domain string, version int, plaintext []byte) error {
	if strings.TrimSpace(domain) == "" || strings.ContainsRune(domain, '\x00') {
		return errors.New("domain is required")
	}
	if version <= 0 {
		return errors.New("domain key version must be positive")
	}
	if len(plaintext) != DomainKeySize {
		return fmt.Errorf("domain key must be %d bytes", DomainKeySize)
	}
	return nil
}

func domainKeyAAD(domain string, version int, algorithm string) []byte {
	return []byte(domain + "\x00" + strconv.Itoa(version) + "\x00" + algorithm)
}

func kekFingerprint(kek []byte) string {
	sum := sha256.Sum256(kek)
	return hex.EncodeToString(sum[:])
}

func constantTimeStringEqual(left, right string) bool {
	if len(left) != len(right) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}
