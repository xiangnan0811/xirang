package export

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strconv"
	"sync/atomic"

	"xirang/backend/internal/backupasset"

	"golang.org/x/crypto/hkdf"
)

var cipherMagic = [8]byte{'X', 'R', 'E', 'X', '0', '0', '0', '1'}

type cryptoClearObserver func([]byte)

var exportCryptoClearObserver atomic.Pointer[cryptoClearObserver]

func observeExportCryptoClear(value []byte) {
	if observer := exportCryptoClearObserver.Load(); observer != nil {
		(*observer)(value)
	}
}

const (
	cipherAEADOverheadV1              = 16
	cipherTrailerPlaintextBytes       = 8 + 8 + sha256.Size
	maxCipherChunkBytesV1       int64 = 8 * 1024 * 1024
	cipherLengthPrefixBytesV1   int64 = 4
	cipherRecordOverheadV1      int64 = cipherLengthPrefixBytesV1 + cipherAEADOverheadV1
	cipherHeaderBytesV1         int64 = int64(len(cipherMagic)) + 8 + 4
	cipherTrailerBytesV1        int64 = cipherLengthPrefixBytesV1 + cipherTrailerPlaintextBytes + cipherAEADOverheadV1
	cipherFixedOverheadV1       int64 = cipherHeaderBytesV1 + cipherTrailerBytesV1
)

type CipherPurpose string

const (
	CipherPurposeFinalArchive CipherPurpose = "final_archive"
	CipherPurposeItemSpool    CipherPurpose = "item_spool"
)

type CipherBinding struct {
	ExportID           string
	SelectionDigest    string
	ArchiveProfile     string
	FormatVersion      int
	AttemptFenceDigest string
	Purpose            CipherPurpose
	ObjectID           string
}

type CipherResult struct {
	ChunkBytes       int64
	ChunkCount       int64
	PlaintextBytes   int64
	CiphertextBytes  int64
	PlaintextDigest  string
	ArchiveDigest    string
	CiphertextDigest string
	NoncePrefix      []byte
}

type CipherRangeMetadata struct {
	ChunkBytes       int64
	ChunkCount       int64
	PlaintextBytes   int64
	CiphertextBytes  int64
	PlaintextDigest  string
	CiphertextDigest string
	NoncePrefix      []byte
}

type CipherRangeResult struct {
	PlaintextBytes  int64
	CiphertextBytes int64
}

type JobKeyEnvelope struct {
	Nonce      []byte
	Ciphertext []byte
}

const JobKeyWrapAlgorithmV1 = "aes-256-gcm"

const selectionMetadataSubkeyDomainV1 = "selection_metadata.v1"

type JobKeyBinding struct {
	ExportID        string
	SelectionDigest string
	KEKVersion      int
	WrapAlgorithm   string
}

func cipherChunkCountV1(plaintextBytes, chunkBytes int64) (int64, error) {
	if plaintextBytes < 0 || !validCipherChunkBytesV1(chunkBytes) {
		return 0, ErrArchiveLimit
	}
	if plaintextBytes == 0 {
		return 0, nil
	}
	chunkCount := 1 + (plaintextBytes-1)/chunkBytes
	if chunkCount > int64(math.MaxUint32) {
		return 0, ErrArchiveLimit
	}
	return chunkCount, nil
}

func validCipherChunkBytesV1(chunkBytes int64) bool {
	return chunkBytes > 0 && chunkBytes <= maxCipherChunkBytesV1
}

func ciphertextSizeV1(plaintextBytes, chunkBytes int64) (int64, error) {
	chunkCount, err := cipherChunkCountV1(plaintextBytes, chunkBytes)
	if err != nil || plaintextBytes > math.MaxInt64-cipherFixedOverheadV1 {
		return 0, ErrArchiveLimit
	}
	remaining := math.MaxInt64 - plaintextBytes - cipherFixedOverheadV1
	if chunkCount > remaining/cipherRecordOverheadV1 {
		return 0, ErrArchiveLimit
	}
	return plaintextBytes + chunkCount*cipherRecordOverheadV1 + cipherFixedOverheadV1, nil
}

func EncryptStream(
	ctx context.Context,
	destination io.Writer,
	source io.Reader,
	dek []byte,
	binding CipherBinding,
	chunkBytes int,
) (CipherResult, error) {
	noncePrefix := make([]byte, 8)
	if _, err := io.ReadFull(rand.Reader, noncePrefix); err != nil {
		return CipherResult{}, err
	}
	return EncryptStreamWithNonce(ctx, destination, source, dek, binding, chunkBytes, noncePrefix)
}

func EncryptStreamWithNonce(
	ctx context.Context,
	destination io.Writer,
	source io.Reader,
	dek []byte,
	binding CipherBinding,
	chunkBytes int,
	noncePrefix []byte,
) (CipherResult, error) {
	aead, err := newExportAEAD(dek, binding)
	if err != nil || destination == nil || source == nil || !validCipherChunkBytesV1(int64(chunkBytes)) ||
		len(noncePrefix) != 8 {
		return CipherResult{}, ErrCipherTampered
	}
	noncePrefix = append([]byte(nil), noncePrefix...)
	cipherHash := sha256.New()
	counted := &countingWriter{writer: io.MultiWriter(destination, cipherHash)}
	if _, err := counted.Write(cipherMagic[:]); err != nil {
		return CipherResult{}, err
	}
	if _, err := counted.Write(noncePrefix); err != nil {
		return CipherResult{}, err
	}
	if err := binary.Write(counted, binary.BigEndian, uint32(chunkBytes)); err != nil {
		return CipherResult{}, err
	}
	plainHash := sha256.New()
	buffer := make([]byte, chunkBytes)
	var chunkIndex uint32
	var plaintextBytes int64
	for {
		if err := ctx.Err(); err != nil {
			return CipherResult{}, err
		}
		count, readErr := io.ReadFull(source, buffer)
		if errors.Is(readErr, io.ErrUnexpectedEOF) || errors.Is(readErr, io.EOF) {
			if count > 0 {
				if err := writeCipherRecord(counted, aead, noncePrefix, uint32(chunkBytes), chunkIndex, buffer[:count], binding); err != nil {
					return CipherResult{}, err
				}
				plainHash.Write(buffer[:count])
				plaintextBytes += int64(count)
				chunkIndex++
			}
			break
		}
		if readErr != nil {
			return CipherResult{}, readErr
		}
		if err := writeCipherRecord(counted, aead, noncePrefix, uint32(chunkBytes), chunkIndex, buffer[:count], binding); err != nil {
			return CipherResult{}, err
		}
		plainHash.Write(buffer[:count])
		plaintextBytes += int64(count)
		chunkIndex++
	}
	plainDigest := plainHash.Sum(nil)
	if err := writeCipherTrailer(
		counted, aead, noncePrefix, uint32(chunkBytes), chunkIndex, plaintextBytes, plainDigest, binding,
	); err != nil {
		return CipherResult{}, err
	}
	plainDigestHex := hex.EncodeToString(plainDigest)
	return CipherResult{
		ChunkBytes: int64(chunkBytes), ChunkCount: int64(chunkIndex),
		PlaintextBytes: plaintextBytes, CiphertextBytes: counted.count,
		PlaintextDigest: plainDigestHex, ArchiveDigest: plainDigestHex,
		CiphertextDigest: hex.EncodeToString(cipherHash.Sum(nil)), NoncePrefix: append([]byte(nil), noncePrefix...),
	}, nil
}

func DecryptStream(
	ctx context.Context,
	destination io.Writer,
	source io.Reader,
	dek []byte,
	binding CipherBinding,
) (CipherResult, error) {
	ctx = nonNilCipherContext(ctx)
	aead, err := newExportAEAD(dek, binding)
	if err != nil || destination == nil || source == nil {
		return CipherResult{}, ErrCipherTampered
	}
	seekableSource, ok := source.(io.ReadSeeker)
	if !ok {
		return CipherResult{}, ErrCipherTampered
	}
	if err := ctx.Err(); err != nil {
		return CipherResult{}, err
	}
	startOffset, err := seekableSource.Seek(0, io.SeekCurrent)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return CipherResult{}, ctxErr
	}
	if err != nil {
		return CipherResult{}, err
	}
	// Authenticate the complete stream before allowing any plaintext to escape.
	if _, err := decryptCipherStreamPass(ctx, nil, seekableSource, aead, binding); err != nil {
		return CipherResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return CipherResult{}, err
	}
	if _, err := seekableSource.Seek(startOffset, io.SeekStart); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return CipherResult{}, ctxErr
		}
		return CipherResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return CipherResult{}, err
	}
	return decryptCipherStreamPass(ctx, destination, seekableSource, aead, binding)
}

func decryptCipherStreamPass(
	ctx context.Context,
	destination io.Writer,
	source io.Reader,
	aead cipher.AEAD,
	binding CipherBinding,
) (CipherResult, error) {
	ctx = nonNilCipherContext(ctx)
	cipherHash := sha256.New()
	reader := io.TeeReader(source, cipherHash)
	header := make([]byte, int(cipherHeaderBytesV1))
	if _, err := io.ReadFull(reader, header); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return CipherResult{}, ctxErr
		}
		return CipherResult{}, cipherReadFailure(err)
	}
	if err := ctx.Err(); err != nil {
		return CipherResult{}, err
	}
	if !bytes.Equal(header[:8], cipherMagic[:]) {
		return CipherResult{}, ErrCipherTampered
	}
	noncePrefix := header[8:16]
	chunkBytes := binary.BigEndian.Uint32(header[16:20])
	if !validCipherChunkBytesV1(int64(chunkBytes)) {
		return CipherResult{}, ErrCipherTampered
	}
	plainHash := sha256.New()
	var chunkIndex uint32
	var plaintextBytes int64
	ciphertextBytes := int64(len(header))
	for {
		if err := ctx.Err(); err != nil {
			return CipherResult{}, err
		}
		var plainLength uint32
		if err := binary.Read(reader, binary.BigEndian, &plainLength); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return CipherResult{}, ctxErr
			}
			return CipherResult{}, cipherReadFailure(err)
		}
		if err := ctx.Err(); err != nil {
			return CipherResult{}, err
		}
		if plainLength > chunkBytes {
			return CipherResult{}, ErrCipherTampered
		}
		ciphertextBytes += cipherLengthPrefixBytesV1
		if plainLength == 0 {
			sealed := make([]byte, int(cipherTrailerBytesV1-cipherLengthPrefixBytesV1))
			if _, err := io.ReadFull(reader, sealed); err != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return CipherResult{}, ctxErr
				}
				return CipherResult{}, cipherReadFailure(err)
			}
			if err := ctx.Err(); err != nil {
				return CipherResult{}, err
			}
			ciphertextBytes += int64(len(sealed))
			trailer, err := aead.Open(nil, recordNonce(noncePrefix, chunkIndex), sealed,
				cipherAssociatedData(binding, noncePrefix, chunkBytes, chunkIndex, 0, true))
			if err != nil || !validCipherTrailer(trailer, chunkIndex, plaintextBytes, plainHash.Sum(nil)) {
				return CipherResult{}, ErrCipherTampered
			}
			var trailing [1]byte
			count, readErr := reader.Read(trailing[:])
			if err := ctx.Err(); err != nil {
				return CipherResult{}, err
			}
			if readErr != nil && !errors.Is(readErr, io.EOF) {
				return CipherResult{}, cipherReadFailure(readErr)
			}
			if count != 0 || !errors.Is(readErr, io.EOF) {
				return CipherResult{}, ErrCipherTampered
			}
			break
		}
		sealed := make([]byte, int64(plainLength)+cipherRecordOverheadV1-cipherLengthPrefixBytesV1)
		if _, err := io.ReadFull(reader, sealed); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return CipherResult{}, ctxErr
			}
			return CipherResult{}, cipherReadFailure(err)
		}
		if err := ctx.Err(); err != nil {
			return CipherResult{}, err
		}
		ciphertextBytes += int64(len(sealed))
		plaintext, err := aead.Open(nil, recordNonce(noncePrefix, chunkIndex), sealed,
			cipherAssociatedData(binding, noncePrefix, chunkBytes, chunkIndex, plainLength, false))
		if err != nil {
			return CipherResult{}, ErrCipherTampered
		}
		if destination != nil {
			if _, err := destination.Write(plaintext); err != nil {
				return CipherResult{}, err
			}
		}
		plainHash.Write(plaintext)
		plaintextBytes += int64(len(plaintext))
		chunkIndex++
	}
	if err := ctx.Err(); err != nil {
		return CipherResult{}, err
	}
	plainDigest := hex.EncodeToString(plainHash.Sum(nil))
	return CipherResult{
		ChunkBytes: int64(chunkBytes), ChunkCount: int64(chunkIndex),
		PlaintextBytes: plaintextBytes, CiphertextBytes: ciphertextBytes,
		PlaintextDigest: plainDigest, ArchiveDigest: plainDigest,
		CiphertextDigest: hex.EncodeToString(cipherHash.Sum(nil)), NoncePrefix: append([]byte(nil), noncePrefix...),
	}, nil
}

// DecryptRange verifies the exact sealed-object digest and authenticated
// trailer before output. That proves the key/binding and every ciphertext byte
// before selected records are individually opened and their authenticated
// plaintext slices are written with chunk-bounded memory.
func DecryptRange(
	ctx context.Context,
	destination io.Writer,
	source io.ReaderAt,
	dek []byte,
	binding CipherBinding,
	metadata CipherRangeMetadata,
	offset int64,
	length int64,
) (CipherRangeResult, error) {
	ctx = nonNilCipherContext(ctx)
	aead, err := newExportAEAD(dek, binding)
	if err != nil || destination == nil || source == nil || !validCipherRangeMetadata(metadata, offset, length) {
		return CipherRangeResult{}, ErrCipherTampered
	}
	physicalRead, err := authenticateCipherObject(ctx, source, aead, binding, metadata)
	if err != nil {
		return CipherRangeResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return CipherRangeResult{}, err
	}
	if length == 0 {
		return CipherRangeResult{CiphertextBytes: physicalRead}, nil
	}
	startChunk := offset / metadata.ChunkBytes
	endChunk := (offset + length - 1) / metadata.ChunkBytes
	var plaintextWritten int64
	for chunk := startChunk; chunk <= endChunk; chunk++ {
		plaintext, bytesRead, err := decryptCipherRangeChunk(ctx, source, aead, binding, metadata, chunk)
		physicalRead += bytesRead
		if err != nil {
			return CipherRangeResult{PlaintextBytes: plaintextWritten, CiphertextBytes: physicalRead}, err
		}
		chunkStart := chunk * metadata.ChunkBytes
		writeStart := max(offset, chunkStart) - chunkStart
		writeEnd := min(offset+length, chunkStart+int64(len(plaintext))) - chunkStart
		if writeStart < 0 || writeEnd < writeStart || writeEnd > int64(len(plaintext)) {
			return CipherRangeResult{}, ErrCipherTampered
		}
		selected := plaintext[writeStart:writeEnd]
		written, writeErr := destination.Write(selected)
		plaintextWritten += int64(written)
		if writeErr != nil {
			return CipherRangeResult{PlaintextBytes: plaintextWritten, CiphertextBytes: physicalRead}, writeErr
		}
		if written != len(selected) {
			return CipherRangeResult{PlaintextBytes: plaintextWritten, CiphertextBytes: physicalRead}, io.ErrShortWrite
		}
	}
	if err := ctx.Err(); err != nil {
		return CipherRangeResult{PlaintextBytes: plaintextWritten, CiphertextBytes: physicalRead}, err
	}
	return CipherRangeResult{PlaintextBytes: plaintextWritten, CiphertextBytes: physicalRead}, nil
}

func validCipherRangeMetadata(metadata CipherRangeMetadata, offset, length int64) bool {
	if !validCipherChunkBytesV1(metadata.ChunkBytes) ||
		metadata.ChunkCount < 0 || metadata.ChunkCount > int64(^uint32(0)) ||
		metadata.PlaintextBytes < 0 || metadata.CiphertextBytes <= 0 ||
		!lowerHex(metadata.PlaintextDigest, 64) || !lowerHex(metadata.CiphertextDigest, 64) ||
		len(metadata.NoncePrefix) != 8 || offset < 0 || length < 0 ||
		offset > metadata.PlaintextBytes || length > metadata.PlaintextBytes-offset {
		return false
	}
	expectedChunks, err := cipherChunkCountV1(metadata.PlaintextBytes, metadata.ChunkBytes)
	if err != nil || metadata.ChunkCount != expectedChunks {
		return false
	}
	expectedCiphertextBytes, err := ciphertextSizeV1(metadata.PlaintextBytes, metadata.ChunkBytes)
	return err == nil && metadata.CiphertextBytes == expectedCiphertextBytes
}

func authenticateCipherObject(
	ctx context.Context,
	source io.ReaderAt,
	aead cipher.AEAD,
	binding CipherBinding,
	metadata CipherRangeMetadata,
) (int64, error) {
	hash := sha256.New()
	buffer := make([]byte, 64*1024)
	var position int64
	for position < metadata.CiphertextBytes {
		if err := ctx.Err(); err != nil {
			return position, err
		}
		count := min(int64(len(buffer)), metadata.CiphertextBytes-position)
		read, readErr := source.ReadAt(buffer[:count], position)
		if read > 0 {
			_, _ = hash.Write(buffer[:read])
			position += int64(read)
		}
		if err := ctx.Err(); err != nil {
			return position, err
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return position, cipherReadFailure(readErr)
		}
		if int64(read) != count {
			return position, ErrCipherTampered
		}
	}
	var trailing [1]byte
	read, readErr := source.ReadAt(trailing[:], metadata.CiphertextBytes)
	if err := ctx.Err(); err != nil {
		return position, err
	}
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return position, cipherReadFailure(readErr)
	}
	if read != 0 || !errors.Is(readErr, io.EOF) {
		return position, ErrCipherTampered
	}
	expectedCipherDigest, err := hex.DecodeString(metadata.CiphertextDigest)
	if err != nil || !bytes.Equal(hash.Sum(nil), expectedCipherDigest) {
		return position, ErrCipherTampered
	}

	header, err := readCipherAt(ctx, source, 0, cipherHeaderBytesV1)
	position += int64(len(header))
	if err != nil {
		return position, err
	}
	if !bytes.Equal(header[:len(cipherMagic)], cipherMagic[:]) ||
		!bytes.Equal(header[len(cipherMagic):len(cipherMagic)+8], metadata.NoncePrefix) ||
		int64(binary.BigEndian.Uint32(header[len(cipherMagic)+8:])) != metadata.ChunkBytes {
		return position, ErrCipherTampered
	}
	trailerOffset, totalBytes, ok := cipherRangeLayout(metadata, aead.Overhead())
	if !ok || totalBytes != metadata.CiphertextBytes {
		return position, ErrCipherTampered
	}
	trailerRecord, err := readCipherAt(ctx, source, trailerOffset, cipherTrailerBytesV1)
	position += int64(len(trailerRecord))
	if err != nil {
		return position, err
	}
	if binary.BigEndian.Uint32(trailerRecord[:4]) != 0 {
		return position, ErrCipherTampered
	}
	trailer, err := aead.Open(nil, recordNonce(metadata.NoncePrefix, uint32(metadata.ChunkCount)), trailerRecord[4:],
		cipherAssociatedData(binding, metadata.NoncePrefix, uint32(metadata.ChunkBytes), uint32(metadata.ChunkCount), 0, true))
	expectedPlainDigest, digestErr := hex.DecodeString(metadata.PlaintextDigest)
	if err != nil || digestErr != nil ||
		!validCipherTrailer(trailer, uint32(metadata.ChunkCount), metadata.PlaintextBytes, expectedPlainDigest) {
		return position, ErrCipherTampered
	}
	if err := ctx.Err(); err != nil {
		return position, err
	}
	return position, nil
}

func decryptCipherRangeChunk(
	ctx context.Context,
	source io.ReaderAt,
	aead cipher.AEAD,
	binding CipherBinding,
	metadata CipherRangeMetadata,
	chunkIndex int64,
) ([]byte, int64, error) {
	if chunkIndex < 0 || chunkIndex >= metadata.ChunkCount {
		return nil, 0, ErrCipherTampered
	}
	recordOffset, plainLength, ok := cipherRangeChunkLayout(metadata, aead.Overhead(), chunkIndex)
	if !ok {
		return nil, 0, ErrCipherTampered
	}
	record, err := readCipherAt(ctx, source, recordOffset, plainLength+cipherRecordOverheadV1)
	if err != nil {
		return nil, int64(len(record)), err
	}
	if int64(binary.BigEndian.Uint32(record[:4])) != plainLength {
		return nil, int64(len(record)), ErrCipherTampered
	}
	plaintext, err := aead.Open(nil, recordNonce(metadata.NoncePrefix, uint32(chunkIndex)), record[4:],
		cipherAssociatedData(
			binding, metadata.NoncePrefix, uint32(metadata.ChunkBytes), uint32(chunkIndex), uint32(plainLength), false,
		))
	if err != nil || int64(len(plaintext)) != plainLength {
		return nil, int64(len(record)), ErrCipherTampered
	}
	return plaintext, int64(len(record)), nil
}

func cipherRangeChunkLayout(metadata CipherRangeMetadata, overhead int, chunkIndex int64) (int64, int64, bool) {
	expectedChunks, err := cipherChunkCountV1(metadata.PlaintextBytes, metadata.ChunkBytes)
	if err != nil || overhead != cipherAEADOverheadV1 || metadata.ChunkCount != expectedChunks ||
		chunkIndex < 0 || chunkIndex >= metadata.ChunkCount ||
		metadata.ChunkBytes > math.MaxInt64-cipherRecordOverheadV1 {
		return 0, 0, false
	}
	recordStride := metadata.ChunkBytes + cipherRecordOverheadV1
	if chunkIndex > (math.MaxInt64-cipherHeaderBytesV1)/recordStride {
		return 0, 0, false
	}
	offset := cipherHeaderBytesV1 + chunkIndex*recordStride
	plainLength := min(metadata.ChunkBytes, metadata.PlaintextBytes-chunkIndex*metadata.ChunkBytes)
	return offset, plainLength, plainLength > 0
}

func cipherRangeLayout(metadata CipherRangeMetadata, overhead int) (int64, int64, bool) {
	expectedChunks, err := cipherChunkCountV1(metadata.PlaintextBytes, metadata.ChunkBytes)
	if err != nil || overhead != cipherAEADOverheadV1 || metadata.ChunkCount != expectedChunks {
		return 0, 0, false
	}
	totalBytes, err := ciphertextSizeV1(metadata.PlaintextBytes, metadata.ChunkBytes)
	if err != nil {
		return 0, 0, false
	}
	return totalBytes - cipherTrailerBytesV1, totalBytes, true
}

func readCipherAt(ctx context.Context, source io.ReaderAt, offset, length int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if source == nil || offset < 0 || length < 0 || length > int64(math.MaxInt) {
		return nil, ErrCipherTampered
	}
	buffer := make([]byte, int(length))
	read, readErr := source.ReadAt(buffer, offset)
	if err := ctx.Err(); err != nil {
		return buffer[:read], err
	}
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return buffer[:read], cipherReadFailure(readErr)
	}
	if read != len(buffer) {
		return buffer[:read], ErrCipherTampered
	}
	return buffer, nil
}

func cipherReadFailure(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return ErrCipherTampered
	}
	return err
}

func nonNilCipherContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func WrapJobDEK(binding JobKeyBinding, kek, dek []byte) (JobKeyEnvelope, error) {
	if len(kek) != 32 || len(dek) != 32 || !validJobKeyBinding(binding) {
		return JobKeyEnvelope{}, ErrCipherTampered
	}
	block, err := aes.NewCipher(kek)
	if err != nil {
		return JobKeyEnvelope{}, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return JobKeyEnvelope{}, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return JobKeyEnvelope{}, err
	}
	return JobKeyEnvelope{Nonce: nonce, Ciphertext: aead.Seal(nil, nonce, dek, jobKeyAD(binding))}, nil
}

func UnwrapJobDEK(binding JobKeyBinding, kek []byte, envelope JobKeyEnvelope) ([]byte, error) {
	if len(kek) != 32 || !validJobKeyBinding(binding) {
		return nil, ErrCipherTampered
	}
	block, err := aes.NewCipher(kek)
	if err != nil {
		return nil, ErrCipherTampered
	}
	aead, err := cipher.NewGCM(block)
	if err != nil || len(envelope.Nonce) != aead.NonceSize() {
		return nil, ErrCipherTampered
	}
	plaintext, err := aead.Open(nil, envelope.Nonce, envelope.Ciphertext, jobKeyAD(binding))
	if err != nil {
		return nil, ErrCipherTampered
	}
	if len(plaintext) != 32 {
		clear(plaintext)
		observeExportCryptoClear(plaintext)
		return nil, ErrCipherTampered
	}
	return plaintext, nil
}

func encryptSelectionPath(
	dek []byte,
	jobID, itemID, selectionDigest string,
	components []string,
) ([]byte, []byte, error) {
	if !validSelectionMetadataBinding(dek, jobID, itemID, selectionDigest) {
		return nil, nil, ErrCipherTampered
	}
	plaintext, err := json.Marshal(components)
	if err != nil {
		return nil, nil, err
	}
	subkey, err := deriveSelectionMetadataSubkey(dek, jobID, selectionDigest)
	if err != nil {
		return nil, nil, err
	}
	defer observeExportCryptoClear(subkey)
	defer clear(subkey)
	block, err := aes.NewCipher(subkey)
	if err != nil {
		return nil, nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, err
	}
	return nonce, aead.Seal(nil, nonce, plaintext, selectionMetadataAD(jobID, itemID, selectionDigest)), nil
}

func decryptSelectionPath(
	dek []byte, jobID, itemID, selectionDigest string, nonce, ciphertext []byte,
) ([]string, error) {
	if !validSelectionMetadataBinding(dek, jobID, itemID, selectionDigest) {
		return nil, ErrCipherTampered
	}
	subkey, err := deriveSelectionMetadataSubkey(dek, jobID, selectionDigest)
	if err != nil {
		return nil, ErrCipherTampered
	}
	defer observeExportCryptoClear(subkey)
	defer clear(subkey)
	block, err := aes.NewCipher(subkey)
	if err != nil {
		return nil, ErrCipherTampered
	}
	aead, err := cipher.NewGCM(block)
	if err != nil || len(nonce) != aead.NonceSize() {
		return nil, ErrCipherTampered
	}
	plaintext, err := aead.Open(nil, nonce, ciphertext, selectionMetadataAD(jobID, itemID, selectionDigest))
	if err != nil {
		return nil, ErrCipherTampered
	}
	var components []string
	if err := json.Unmarshal(plaintext, &components); err != nil || len(components) == 0 {
		return nil, ErrCipherTampered
	}
	return components, nil
}

func deriveSelectionMetadataSubkey(dek []byte, jobID, selectionDigest string) ([]byte, error) {
	if len(dek) != 32 || !lowerHex(jobID, 32) || !lowerHex(selectionDigest, 64) {
		return nil, ErrCipherTampered
	}
	var info bytes.Buffer
	writeString(&info, selectionMetadataSubkeyDomainV1)
	writeString(&info, jobID)
	writeString(&info, selectionDigest)
	subkey := make([]byte, 32)
	if _, err := io.ReadFull(hkdf.New(sha256.New, dek, nil, info.Bytes()), subkey); err != nil {
		clear(subkey)
		observeExportCryptoClear(subkey)
		return nil, ErrCipherTampered
	}
	return subkey, nil
}

func validSelectionMetadataBinding(dek []byte, jobID, itemID, selectionDigest string) bool {
	return len(dek) == 32 && lowerHex(jobID, 32) && lowerHex(itemID, 32) && lowerHex(selectionDigest, 64)
}

func selectionMetadataAD(jobID, itemID, selectionDigest string) []byte {
	return []byte("xirang.backup_asset.export.selection_path.v1\x00" + jobID + "\x00" + itemID + "\x00" + selectionDigest)
}

func newExportAEAD(dek []byte, binding CipherBinding) (cipher.AEAD, error) {
	if len(dek) != 32 || !lowerHex(binding.ExportID, 32) || !lowerHex(binding.SelectionDigest, 64) ||
		binding.ArchiveProfile == "" || binding.FormatVersion <= 0 || !lowerHex(binding.AttemptFenceDigest, 64) ||
		!validCipherPurpose(binding) {
		return nil, ErrCipherTampered
	}
	subkey := make([]byte, 32)
	defer observeExportCryptoClear(subkey)
	defer clear(subkey)
	if _, err := io.ReadFull(hkdf.New(sha256.New, dek, nil, cipherSubkeyInfo(binding)), subkey); err != nil {
		return nil, ErrCipherTampered
	}
	block, err := aes.NewCipher(subkey)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func writeCipherRecord(
	destination io.Writer,
	aead cipher.AEAD,
	noncePrefix []byte,
	chunkBytes uint32,
	chunkIndex uint32,
	plaintext []byte,
	binding CipherBinding,
) error {
	if len(plaintext) == 0 {
		return ErrCipherTampered
	}
	if chunkIndex == math.MaxUint32 {
		return ErrArchiveLimit
	}
	if err := binary.Write(destination, binary.BigEndian, uint32(len(plaintext))); err != nil {
		return err
	}
	sealed := aead.Seal(nil, recordNonce(noncePrefix, chunkIndex), plaintext,
		cipherAssociatedData(binding, noncePrefix, chunkBytes, chunkIndex, uint32(len(plaintext)), false))
	_, err := destination.Write(sealed)
	return err
}

func writeCipherTrailer(
	destination io.Writer, aead cipher.AEAD, noncePrefix []byte, chunkBytes, chunkCount uint32,
	plaintextBytes int64, plaintextDigest []byte, binding CipherBinding,
) error {
	if plaintextBytes < 0 || len(plaintextDigest) != sha256.Size {
		return ErrCipherTampered
	}
	if err := binary.Write(destination, binary.BigEndian, uint32(0)); err != nil {
		return err
	}
	trailer := make([]byte, cipherTrailerPlaintextBytes)
	binary.BigEndian.PutUint64(trailer[:8], uint64(chunkCount))
	binary.BigEndian.PutUint64(trailer[8:16], uint64(plaintextBytes))
	copy(trailer[16:], plaintextDigest)
	sealed := aead.Seal(nil, recordNonce(noncePrefix, chunkCount), trailer,
		cipherAssociatedData(binding, noncePrefix, chunkBytes, chunkCount, 0, true))
	_, err := destination.Write(sealed)
	return err
}

func validCipherTrailer(trailer []byte, chunkCount uint32, plaintextBytes int64, plaintextDigest []byte) bool {
	return len(trailer) == cipherTrailerPlaintextBytes && binary.BigEndian.Uint64(trailer[:8]) == uint64(chunkCount) &&
		plaintextBytes >= 0 && binary.BigEndian.Uint64(trailer[8:16]) == uint64(plaintextBytes) &&
		bytes.Equal(trailer[16:], plaintextDigest)
}

func recordNonce(prefix []byte, chunkIndex uint32) []byte {
	nonce := make([]byte, 12)
	copy(nonce, prefix)
	binary.BigEndian.PutUint32(nonce[8:], chunkIndex)
	return nonce
}

func cipherAssociatedData(
	binding CipherBinding, noncePrefix []byte, chunkBytes, chunkIndex, plainLength uint32, final bool,
) []byte {
	var buffer bytes.Buffer
	writeString(&buffer, "xirang.export.chunk.v1")
	writeString(&buffer, binding.ExportID)
	writeString(&buffer, binding.SelectionDigest)
	writeString(&buffer, binding.ArchiveProfile)
	writeUint64(&buffer, uint64(binding.FormatVersion))
	writeString(&buffer, binding.AttemptFenceDigest)
	writeString(&buffer, string(binding.Purpose))
	writeString(&buffer, binding.ObjectID)
	writeString(&buffer, hex.EncodeToString(noncePrefix))
	writeUint64(&buffer, uint64(chunkBytes))
	writeUint64(&buffer, uint64(chunkIndex))
	writeUint64(&buffer, uint64(plainLength))
	if final {
		buffer.WriteByte(1)
	} else {
		buffer.WriteByte(0)
	}
	return buffer.Bytes()
}

func cipherSubkeyInfo(binding CipherBinding) []byte {
	var buffer bytes.Buffer
	writeString(&buffer, "xirang.backup_asset.export.subkey.v1")
	writeString(&buffer, string(binding.Purpose))
	writeString(&buffer, binding.ObjectID)
	writeString(&buffer, binding.ExportID)
	writeString(&buffer, binding.AttemptFenceDigest)
	return buffer.Bytes()
}

func validCipherPurpose(binding CipherBinding) bool {
	switch binding.Purpose {
	case CipherPurposeFinalArchive:
		return binding.ObjectID == ""
	case CipherPurposeItemSpool:
		return backupasset.ValidateOpaqueID(binding.ObjectID) == nil
	default:
		return false
	}
}

func jobKeyAD(binding JobKeyBinding) []byte {
	var buffer bytes.Buffer
	writeString(&buffer, "xirang.backup_asset.export.job_dek.v1")
	writeString(&buffer, binding.ExportID)
	writeString(&buffer, binding.SelectionDigest)
	writeString(&buffer, strconv.Itoa(binding.KEKVersion))
	writeString(&buffer, binding.WrapAlgorithm)
	return buffer.Bytes()
}

func validJobKeyBinding(binding JobKeyBinding) bool {
	return lowerHex(binding.ExportID, 32) && lowerHex(binding.SelectionDigest, 64) &&
		binding.KEKVersion > 0 && binding.WrapAlgorithm == JobKeyWrapAlgorithmV1
}

type countingWriter struct {
	writer io.Writer
	count  int64
}

func (writer *countingWriter) Write(buffer []byte) (int, error) {
	count, err := writer.writer.Write(buffer)
	writer.count += int64(count)
	return count, err
}
