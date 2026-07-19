package processing

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"sync"
)

var (
	ErrDerivedInvalid = errors.New("invalid Derived Store crypto contract")
	ErrDerivedTamper  = errors.New("derived store ciphertext authentication failed")
	ErrDerivedWrite   = errors.New("derived store ciphertext write failed")
)

const (
	derivedCipherFormatVersion = 1
	derivedMinimumChunkSize    = int64(64 * 1024)
	derivedMaximumChunkSize    = int64(8 * 1024 * 1024)
	derivedMaximumBlobSize     = int64(16 * 1024 * 1024 * 1024)
	derivedDEKSize             = 32
	derivedNoncePrefixSize     = 8
	derivedEnvelopeNonceSize   = 12
)

var derivedCipherMagic = [4]byte{'X', 'R', 'D', 1}

type DerivedCryptoSpec struct {
	BlobID          string
	PlaintextDigest string
	PlaintextSize   int64
	ChunkSize       int64
	KEKVersion      int
	KEK             []byte
}

type DerivedCryptoMetadata struct {
	CipherFormatVersion int
	ChunkSize           int64
	ChunkCount          int64
	NoncePrefix         []byte
	WrappedDEK          []byte
	EnvelopeNonce       []byte
	KEKVersion          int
	PhysicalSize        int64
}

type DerivedCipher struct {
	randomMu sync.Mutex
	random   io.Reader
}

func NewDerivedCipher(random io.Reader) (*DerivedCipher, error) {
	if random == nil {
		return nil, ErrDerivedInvalid
	}
	return &DerivedCipher{random: random}, nil
}

func (derived *DerivedCipher) Encrypt(destination io.Writer, plaintext io.Reader, spec DerivedCryptoSpec) (DerivedCryptoMetadata, error) {
	if derived == nil || destination == nil || plaintext == nil || validateDerivedCryptoSpec(spec) != nil {
		return DerivedCryptoMetadata{}, ErrDerivedInvalid
	}
	dek := make([]byte, derivedDEKSize)
	noncePrefix := make([]byte, derivedNoncePrefixSize)
	envelopeNonce := make([]byte, derivedEnvelopeNonceSize)
	if err := derived.readRandom(dek, noncePrefix, envelopeNonce); err != nil {
		zeroBytesLocal(dek)
		return DerivedCryptoMetadata{}, err
	}
	defer zeroBytesLocal(dek)

	dataAEAD, err := newDerivedAEAD(dek)
	if err != nil {
		return DerivedCryptoMetadata{}, ErrDerivedInvalid
	}
	kekAEAD, err := newDerivedAEAD(spec.KEK)
	if err != nil {
		return DerivedCryptoMetadata{}, ErrDerivedInvalid
	}
	wrappedDEK := kekAEAD.Seal(nil, envelopeNonce, dek, derivedEnvelopeAAD(spec))
	chunkCount, err := expectedDerivedChunkCount(spec.PlaintextSize, spec.ChunkSize)
	if err != nil {
		return DerivedCryptoMetadata{}, err
	}

	counter := &countingWriter{writer: destination}
	if err := writeDerivedBytes(counter, derivedCipherMagic[:]); err != nil {
		return DerivedCryptoMetadata{}, err
	}
	hasher := sha256.New()
	remaining := spec.PlaintextSize
	for index := int64(0); index < chunkCount; index++ {
		plainSize := spec.ChunkSize
		if remaining < plainSize {
			plainSize = remaining
		}
		if spec.PlaintextSize == 0 {
			plainSize = 0
		}
		chunk := make([]byte, int(plainSize))
		if _, err := io.ReadFull(plaintext, chunk); err != nil {
			zeroBytesLocal(chunk)
			return DerivedCryptoMetadata{}, fmt.Errorf("%w: plaintext ended before declared size", ErrDerivedInvalid)
		}
		_, _ = hasher.Write(chunk)
		nonce, nonceErr := derivedChunkNonce(noncePrefix, index)
		if nonceErr != nil {
			zeroBytesLocal(chunk)
			return DerivedCryptoMetadata{}, nonceErr
		}
		sealed := dataAEAD.Seal(nil, nonce, chunk, derivedChunkAAD(spec, index, plainSize))
		zeroBytesLocal(chunk)
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(plainSize))
		if err := writeDerivedBytes(counter, length[:]); err != nil {
			return DerivedCryptoMetadata{}, err
		}
		if err := writeDerivedBytes(counter, sealed); err != nil {
			zeroBytesLocal(sealed)
			return DerivedCryptoMetadata{}, err
		}
		zeroBytesLocal(sealed)
		remaining -= plainSize
	}
	var extra [1]byte
	if count, readErr := plaintext.Read(extra[:]); count != 0 || (readErr != nil && !errors.Is(readErr, io.EOF)) {
		return DerivedCryptoMetadata{}, fmt.Errorf("%w: plaintext exceeds declared size", ErrDerivedInvalid)
	}
	if !constantDigestEqual(hasher.Sum(nil), spec.PlaintextDigest) {
		return DerivedCryptoMetadata{}, fmt.Errorf("%w: plaintext digest differs", ErrDerivedInvalid)
	}
	return DerivedCryptoMetadata{
		CipherFormatVersion: derivedCipherFormatVersion, ChunkSize: spec.ChunkSize, ChunkCount: chunkCount,
		NoncePrefix: append([]byte(nil), noncePrefix...), WrappedDEK: append([]byte(nil), wrappedDEK...),
		EnvelopeNonce: append([]byte(nil), envelopeNonce...), KEKVersion: spec.KEKVersion, PhysicalSize: counter.count,
	}, nil
}

func (derived *DerivedCipher) Decrypt(destination io.Writer, ciphertext io.Reader, spec DerivedCryptoSpec, metadata DerivedCryptoMetadata) error {
	if derived == nil || destination == nil || ciphertext == nil || validateDerivedCryptoSpec(spec) != nil ||
		validateDerivedCryptoMetadata(spec, metadata) != nil {
		return ErrDerivedTamper
	}
	kekAEAD, err := newDerivedAEAD(spec.KEK)
	if err != nil {
		return ErrDerivedTamper
	}
	dek, err := kekAEAD.Open(nil, metadata.EnvelopeNonce, metadata.WrappedDEK, derivedEnvelopeAAD(spec))
	if err != nil || len(dek) != derivedDEKSize {
		zeroBytesLocal(dek)
		return ErrDerivedTamper
	}
	defer zeroBytesLocal(dek)
	dataAEAD, err := newDerivedAEAD(dek)
	if err != nil {
		return ErrDerivedTamper
	}
	counter := &countingReader{reader: ciphertext}
	var magic [4]byte
	if _, err := io.ReadFull(counter, magic[:]); err != nil || magic != derivedCipherMagic {
		return ErrDerivedTamper
	}
	hasher := sha256.New()
	written := int64(0)
	for index := int64(0); index < metadata.ChunkCount; index++ {
		var encodedLength [4]byte
		if _, err := io.ReadFull(counter, encodedLength[:]); err != nil {
			return ErrDerivedTamper
		}
		plainSize := int64(binary.BigEndian.Uint32(encodedLength[:]))
		expectedSize := spec.ChunkSize
		remaining := spec.PlaintextSize - written
		if remaining < expectedSize {
			expectedSize = remaining
		}
		if spec.PlaintextSize == 0 {
			expectedSize = 0
		}
		if plainSize != expectedSize || plainSize < 0 || plainSize > spec.ChunkSize {
			return ErrDerivedTamper
		}
		sealedSize := plainSize + int64(dataAEAD.Overhead())
		sealed := make([]byte, int(sealedSize))
		if _, err := io.ReadFull(counter, sealed); err != nil {
			return ErrDerivedTamper
		}
		nonce, err := derivedChunkNonce(metadata.NoncePrefix, index)
		if err != nil {
			return ErrDerivedTamper
		}
		chunk, err := dataAEAD.Open(nil, nonce, sealed, derivedChunkAAD(spec, index, plainSize))
		zeroBytesLocal(sealed)
		if err != nil || int64(len(chunk)) != plainSize {
			zeroBytesLocal(chunk)
			return ErrDerivedTamper
		}
		_, _ = hasher.Write(chunk)
		if err := writeDerivedPlaintext(destination, chunk); err != nil {
			zeroBytesLocal(chunk)
			return err
		}
		zeroBytesLocal(chunk)
		written += plainSize
	}
	var extra [1]byte
	count, readErr := counter.Read(extra[:])
	if count != 0 || !errors.Is(readErr, io.EOF) || counter.count != metadata.PhysicalSize ||
		written != spec.PlaintextSize || !constantDigestEqual(hasher.Sum(nil), spec.PlaintextDigest) {
		return ErrDerivedTamper
	}
	return nil
}

func (derived *DerivedCipher) RewrapDEK(metadata DerivedCryptoMetadata, oldSpec, newSpec DerivedCryptoSpec) (DerivedCryptoMetadata, error) {
	if derived == nil || validateDerivedCryptoSpec(oldSpec) != nil || validateDerivedCryptoSpec(newSpec) != nil ||
		validateDerivedCryptoMetadata(oldSpec, metadata) != nil || !sameDerivedBlobContract(oldSpec, newSpec) {
		return DerivedCryptoMetadata{}, ErrDerivedTamper
	}
	oldAEAD, err := newDerivedAEAD(oldSpec.KEK)
	if err != nil {
		return DerivedCryptoMetadata{}, ErrDerivedTamper
	}
	dek, err := oldAEAD.Open(nil, metadata.EnvelopeNonce, metadata.WrappedDEK, derivedEnvelopeAAD(oldSpec))
	if err != nil || len(dek) != derivedDEKSize {
		zeroBytesLocal(dek)
		return DerivedCryptoMetadata{}, ErrDerivedTamper
	}
	defer zeroBytesLocal(dek)
	newAEAD, err := newDerivedAEAD(newSpec.KEK)
	if err != nil {
		return DerivedCryptoMetadata{}, ErrDerivedTamper
	}
	nonce := make([]byte, derivedEnvelopeNonceSize)
	if err := derived.readRandom(nonce); err != nil {
		return DerivedCryptoMetadata{}, err
	}
	result := cloneDerivedMetadata(metadata)
	result.EnvelopeNonce = nonce
	result.KEKVersion = newSpec.KEKVersion
	result.WrappedDEK = newAEAD.Seal(nil, nonce, dek, derivedEnvelopeAAD(newSpec))
	return result, nil
}

func validateDerivedCryptoSpec(spec DerivedCryptoSpec) error {
	if !lowerHex(spec.BlobID, 32) || !lowerHex(spec.PlaintextDigest, 64) || spec.PlaintextSize < 0 ||
		spec.PlaintextSize > derivedMaximumBlobSize || spec.ChunkSize < derivedMinimumChunkSize ||
		spec.ChunkSize > derivedMaximumChunkSize || spec.KEKVersion <= 0 || len(spec.KEK) != derivedDEKSize {
		return ErrDerivedInvalid
	}
	_, err := expectedDerivedChunkCount(spec.PlaintextSize, spec.ChunkSize)
	return err
}

func validateDerivedCryptoMetadata(spec DerivedCryptoSpec, metadata DerivedCryptoMetadata) error {
	count, err := expectedDerivedChunkCount(spec.PlaintextSize, spec.ChunkSize)
	if err != nil || metadata.CipherFormatVersion != derivedCipherFormatVersion || metadata.ChunkSize != spec.ChunkSize ||
		metadata.ChunkCount != count || len(metadata.NoncePrefix) != derivedNoncePrefixSize || len(metadata.EnvelopeNonce) != derivedEnvelopeNonceSize ||
		len(metadata.WrappedDEK) != derivedDEKSize+16 || metadata.KEKVersion != spec.KEKVersion || metadata.PhysicalSize <= 0 {
		return ErrDerivedTamper
	}
	return nil
}

func expectedDerivedChunkCount(size, chunkSize int64) (int64, error) {
	if size < 0 || chunkSize <= 0 {
		return 0, ErrDerivedInvalid
	}
	if size == 0 {
		return 1, nil
	}
	count := (size + chunkSize - 1) / chunkSize
	if count <= 0 || uint64(count) > uint64(math.MaxUint32)+1 {
		return 0, ErrDerivedInvalid
	}
	return count, nil
}

func derivedChunkNonce(prefix []byte, index int64) ([]byte, error) {
	if len(prefix) != derivedNoncePrefixSize || index < 0 || uint64(index) > uint64(math.MaxUint32) {
		return nil, ErrDerivedInvalid
	}
	nonce := make([]byte, derivedEnvelopeNonceSize)
	copy(nonce, prefix)
	binary.BigEndian.PutUint32(nonce[8:], uint32(index))
	return nonce, nil
}

func derivedChunkAAD(spec DerivedCryptoSpec, index, plainSize int64) []byte {
	var payload bytes.Buffer
	writeAADString(&payload, "xirang.derived.chunk.v1")
	writeAADString(&payload, spec.BlobID)
	writeAADString(&payload, spec.PlaintextDigest)
	writeAADInt64(&payload, spec.PlaintextSize)
	writeAADInt64(&payload, spec.ChunkSize)
	writeAADInt64(&payload, index)
	writeAADInt64(&payload, plainSize)
	return payload.Bytes()
}

func derivedEnvelopeAAD(spec DerivedCryptoSpec) []byte {
	var payload bytes.Buffer
	writeAADString(&payload, "xirang.derived.envelope.v1")
	writeAADString(&payload, spec.BlobID)
	writeAADString(&payload, spec.PlaintextDigest)
	writeAADInt64(&payload, spec.PlaintextSize)
	writeAADInt64(&payload, spec.ChunkSize)
	writeAADInt64(&payload, int64(spec.KEKVersion))
	return payload.Bytes()
}

func writeAADString(destination *bytes.Buffer, value string) {
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(value)))
	destination.Write(length[:])
	destination.WriteString(value)
}

func writeAADInt64(destination *bytes.Buffer, value int64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], uint64(value))
	destination.Write(encoded[:])
}

func newDerivedAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func (derived *DerivedCipher) readRandom(values ...[]byte) error {
	derived.randomMu.Lock()
	defer derived.randomMu.Unlock()
	for _, value := range values {
		if _, err := io.ReadFull(derived.random, value); err != nil {
			return fmt.Errorf("generate Derived Store key material: %w", err)
		}
	}
	return nil
}

func writeDerivedBytes(destination io.Writer, payload []byte) error {
	written, err := destination.Write(payload)
	if err != nil || written != len(payload) {
		return errors.Join(ErrDerivedWrite, err, io.ErrShortWrite)
	}
	return nil
}

func writeDerivedPlaintext(destination io.Writer, payload []byte) error {
	written, err := destination.Write(payload)
	if err != nil || written != len(payload) {
		return errors.Join(ErrDerivedWrite, err, io.ErrShortWrite)
	}
	return nil
}

type countingWriter struct {
	writer io.Writer
	count  int64
}

func (writer *countingWriter) Write(payload []byte) (int, error) {
	count, err := writer.writer.Write(payload)
	writer.count += int64(count)
	return count, err
}

type countingReader struct {
	reader io.Reader
	count  int64
}

func (reader *countingReader) Read(payload []byte) (int, error) {
	count, err := reader.reader.Read(payload)
	reader.count += int64(count)
	return count, err
}

func constantDigestEqual(digest []byte, encoded string) bool {
	want := make([]byte, sha256.Size)
	for index := 0; index < len(want); index++ {
		var high, low byte
		if !decodeHexNibble(encoded[index*2], &high) || !decodeHexNibble(encoded[index*2+1], &low) {
			return false
		}
		want[index] = high<<4 | low
	}
	return bytes.Equal(digest, want)
}

func decodeHexNibble(value byte, decoded *byte) bool {
	switch {
	case value >= '0' && value <= '9':
		*decoded = value - '0'
	case value >= 'a' && value <= 'f':
		*decoded = value - 'a' + 10
	default:
		return false
	}
	return true
}

func sameDerivedBlobContract(left, right DerivedCryptoSpec) bool {
	return left.BlobID == right.BlobID && left.PlaintextDigest == right.PlaintextDigest &&
		left.PlaintextSize == right.PlaintextSize && left.ChunkSize == right.ChunkSize
}

func cloneDerivedMetadata(value DerivedCryptoMetadata) DerivedCryptoMetadata {
	value.NoncePrefix = append([]byte(nil), value.NoncePrefix...)
	value.WrappedDEK = append([]byte(nil), value.WrappedDEK...)
	value.EnvelopeNonce = append([]byte(nil), value.EnvelopeNonce...)
	return value
}

func zeroBytesLocal(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
